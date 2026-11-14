package schema

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/mutex-ai/mutexai-browser-platform/src/partners"
)

func testPartners(t *testing.T, db *sql.DB) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	store := partners.NewStore(db)
	in := partners.CreateInput{Name: "MSP", ClientName: "Default", SiteName: "Office", ConsoleDomains: []string{"ACME.EXAMPLE."}}
	var wg sync.WaitGroup
	results := make(chan partners.Partner, 8)
	failures := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			p, _, err := store.Create(ctx, "same-key", in)
			if err != nil {
				failures <- err
			} else {
				results <- p
			}
		})
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	for p := range results {
		if p.PartnerID != 1000 || p.ClientID != 1000 || p.SiteID != 1000 || p.SetupStatus != "pending" {
			t.Fatalf("incorrect bootstrap IDs/status: %+v", p)
		}
	}
	changed := in
	changed.Name = "Changed"
	if _, _, err := store.Create(ctx, "same-key", changed); !errors.Is(err, partners.ErrRequestConflict) {
		t.Fatalf("changed retry accepted: %v", err)
	}
	if _, _, err := store.Create(ctx, "other-key", in); !errors.Is(err, partners.ErrDomainConflict) {
		t.Fatalf("domain conflict accepted: %v", err)
	}
	in.ConsoleDomains = []string{"second.example"}
	second, _, err := store.Create(ctx, "second-key", in)
	if err != nil || second.PartnerID != 1001 {
		t.Fatalf("failed creation consumed an ID: %v", err)
	}
	var orphanCount int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM platform.partners").Scan(&orphanCount); err != nil || orphanCount != 2 {
		t.Fatal("duplicate or orphan partners were created")
	}
	job, err := store.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.Claim(ctx)
	if err != nil || other.PartnerID == job.PartnerID {
		t.Fatal("two workers claimed the same live task")
	}
	if _, err := store.Claim(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("leased task was claimed again")
	}
	if _, err := db.ExecContext(ctx, "UPDATE platform.partner_setup SET lease_until=now()-interval '1 second' WHERE partner_id=$1", job.PartnerID); err != nil {
		t.Fatal(err)
	}
	recovered, err := store.Claim(ctx)
	if err != nil || recovered.PartnerID != job.PartnerID || recovered.LeaseToken == job.LeaseToken {
		t.Fatal("expired task did not recover")
	}
	ids := partners.IdentityIDs{ConsoleRealmID: "console-id", SSORealmID: "sso-id", OrganizationID: "org-id"}
	if err := store.Complete(ctx, job, ids); !errors.Is(err, partners.ErrLeaseLost) {
		t.Fatal("stale worker completed another worker's task")
	}
	if err := store.Complete(ctx, recovered, ids); err != nil {
		t.Fatal(err)
	}
	if err := store.Fail(ctx, job, "test_failure"); !errors.Is(err, partners.ErrLeaseLost) {
		t.Fatal("stale worker resurrected completed task")
	}
	done, err := store.Get(ctx, job.PartnerID)
	if err != nil || done.SetupStatus != "complete" {
		t.Fatal("completion was not persisted")
	}
	if err := store.Fail(ctx, other, "keycloak_access_denied"); err != nil {
		t.Fatal(err)
	}
	failed, err := store.Get(ctx, other.PartnerID)
	if err != nil || failed.SetupError != "keycloak_access_denied" || failed.RetryAt == nil {
		t.Fatal("safe failure and retry were not persisted")
	}
	if _, err := db.ExecContext(ctx, "UPDATE platform.id_counters SET next_id=9999 WHERE scope='partners'"); err != nil {
		t.Fatal(err)
	}
	in.ConsoleDomains = []string{"last.example"}
	last, _, err := store.Create(ctx, "last", in)
	if err != nil || last.PartnerID != 9999 {
		t.Fatalf("last ID failed: %v", err)
	}
	in.ConsoleDomains = []string{"exhausted.example"}
	if _, _, err := store.Create(ctx, "exhausted", in); !errors.Is(err, partners.ErrCapacity) {
		t.Fatal("ID range wrapped or expanded")
	}
	in.ConsoleDomains = []string{"last.example"}
	if p, replayed, err := store.Create(ctx, "last", in); err != nil || !replayed || p.PartnerID != 9999 {
		t.Fatal("idempotent retry failed after capacity exhaustion")
	}
	// Full parent keys must reject a site referring to a client under another MSP.
	if _, err := db.ExecContext(ctx, fmt.Sprintf("INSERT INTO platform.sites(partner_id,client_id,site_id,name) VALUES(%d,1001,1000,'invalid')", last.PartnerID)); err == nil {
		t.Fatal("site without matching parent client was accepted")
	}
}
