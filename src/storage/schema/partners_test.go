package schema

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/mutex-ai/mutexai-browser-platform/src/storage/instances"
	"github.com/mutex-ai/mutexai-browser-platform/src/storage/partners"
)

func testPartners(t *testing.T, db *sql.DB) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	// Record timestamps must come from Go even when a SQL default differs.
	if _, err := db.ExecContext(ctx, `ALTER TABLE platform.instances ALTER COLUMN created_ts_utc SET DEFAULT '2000-01-01T00:00:00Z';
 ALTER TABLE platform.partners ALTER COLUMN created_ts_utc SET DEFAULT '2000-01-01T00:00:00Z';
 ALTER TABLE platform.changes ALTER COLUMN created_ts_utc SET DEFAULT '2000-01-01T00:00:00Z';
 ALTER TABLE platform.partner_setup ALTER COLUMN updated_ts_utc SET DEFAULT '2000-01-01T00:00:00Z';`); err != nil {
		t.Fatal(err)
	}
	store := partners.NewStore(db)
	registry := instances.NewStore(db)
	instanceInput := instances.CreateInput{Name: "Shared", ConsoleURL: "https://console-a.example.test", BackendURL: "https://api-a.example.test"}
	instance, _, err := registry.Create(ctx, "instance-a", instanceInput)
	if err != nil || instance.InstanceID != 1000 {
		t.Fatalf("instance registration failed: %v", err)
	}
	if same, replayed, err := registry.Create(ctx, "instance-a", instanceInput); err != nil || !replayed || same.InstanceID != 1000 {
		t.Fatal("instance retry was not idempotent")
	}
	changedInstance := instanceInput
	changedInstance.Name = "Different"
	if _, _, err := registry.Create(ctx, "instance-a", changedInstance); !errors.Is(err, instances.ErrRequestConflict) {
		t.Fatal("instance retry with changed input was accepted")
	}
	if _, _, err := registry.Create(ctx, "duplicate-console", instanceInput); !errors.Is(err, instances.ErrConsoleConflict) {
		t.Fatal("console origin was assigned to two instances")
	}
	if instance.CreatedTSUTC.IsZero() || instance.CreatedTSUTC.Location() != time.UTC {
		t.Fatal("instance creation timestamp is missing or not UTC")
	}
	var timestampType string
	var current bool
	if err := db.QueryRowContext(ctx, `SELECT pg_typeof(created_ts_utc)::text,
 abs(extract(epoch FROM created_ts_utc)-extract(epoch FROM statement_timestamp()))<5
 FROM platform.instances WHERE instance_id=1000`).Scan(&timestampType, &current); err != nil || timestampType != "timestamp with time zone" || !current {
		t.Fatalf("application timestamp did not save the current instant: %v", err)
	}
	instanceInput.ConsoleURL = "https://console-b.example.test"
	instanceInput.BackendURL = "https://api-b.example.test"
	if second, _, err := registry.Create(ctx, "instance-b", instanceInput); err != nil || second.InstanceID != 1001 {
		t.Fatal("second instance was not registered")
	}
	instanceInput.ConsoleURL = "https://console-c.example.test"
	instanceInput.AcceptingClients = new(false)
	if _, _, err := registry.Create(ctx, "instance-c", instanceInput); err != nil {
		t.Fatal(err)
	}

	in := partners.CreateInput{InstanceID: 1000, Name: "MSP", ClientName: "Default", SiteName: "Office", ConsoleDomains: []string{"ACME.EXAMPLE."}}
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
		if p.PartnerID != 1000 || p.ClientID != 1000 || p.SiteID != 1000 || p.SetupStatus != "pending" || p.InstanceID != 1000 || p.ConsoleURL != "https://console-a.example.test" {
			t.Fatalf("incorrect bootstrap IDs/status: %+v", p)
		}
		if p.CreatedTSUTC.IsZero() || p.CreatedTSUTC.Location() != time.UTC {
			t.Fatal("partner creation timestamp is missing or not UTC")
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
	bad := in
	bad.ConsoleDomains = []string{"unavailable.example"}
	bad.InstanceID = 1002
	if _, _, err := store.Create(ctx, "unavailable", bad); !errors.Is(err, partners.ErrInstanceUnavailable) {
		t.Fatal("unavailable instance accepted")
	}
	bad.InstanceID = 9998
	if _, _, err := store.Create(ctx, "missing-instance", bad); !errors.Is(err, partners.ErrInstanceUnavailable) {
		t.Fatal("unknown instance accepted")
	}
	if _, err := registry.SetAvailability(ctx, 1000, false); err != nil {
		t.Fatal(err)
	}
	if _, replayed, err := store.Create(ctx, "same-key", partners.CreateInput{InstanceID: 1000, Name: "MSP", ClientName: "Default", SiteName: "Office", ConsoleDomains: []string{"acme.example"}}); err != nil || !replayed {
		t.Fatal("replay failed when instance stopped accepting assignments")
	}
	if _, err := registry.SetAvailability(ctx, 1000, true); err != nil {
		t.Fatal(err)
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
	if _, err := db.ExecContext(ctx, "UPDATE platform.partner_setup SET lease_expires_ts_utc=now()-interval '1 second' WHERE partner_id=$1", job.PartnerID); err != nil {
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
	if err != nil || failed.SetupError != "keycloak_access_denied" || failed.RetryTSUTC == nil || failed.RetryTSUTC.Location() != time.UTC {
		t.Fatal("safe failure and retry were not persisted")
	}
	// Model a record retained by the upgrade from the unassigned schema.
	if _, err := db.ExecContext(ctx, `ALTER TABLE platform.clients DROP CONSTRAINT clients_require_instance;
 UPDATE platform.clients SET instance_id=NULL WHERE partner_id=1001 AND client_id=1000;
 ALTER TABLE platform.clients ADD CONSTRAINT clients_require_instance CHECK(instance_id IS NOT NULL) NOT VALID;`); err != nil {
		t.Fatal(err)
	}
	legacy, err := store.Get(ctx, 1001)
	if err != nil || legacy.SetupStatus != "assignment_required" {
		t.Fatal("legacy missing assignment was hidden")
	}
	if _, err := store.Claim(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("unassigned legacy record was processed")
	}
	if _, err := registry.SetAvailability(ctx, 9998, false); !errors.Is(err, instances.ErrNotFound) {
		t.Fatal("missing instance update was accepted")
	}
	if _, err := db.ExecContext(ctx, "UPDATE platform.id_counters SET next_id=9999 WHERE scope='instances'"); err != nil {
		t.Fatal(err)
	}
	instanceInput.ConsoleURL = "https://console-last.example.test"
	if last, _, err := registry.Create(ctx, "last-instance", instanceInput); err != nil || last.InstanceID != 9999 {
		t.Fatalf("last instance ID was not allocated: %v", err)
	}
	instanceInput.ConsoleURL = "https://console-exhausted.example.test"
	if _, _, err := registry.Create(ctx, "exhausted-instance", instanceInput); !errors.Is(err, instances.ErrCapacity) {
		t.Fatal("instance ID range wrapped or expanded")
	}
	instanceInput.ConsoleURL = "https://console-last.example.test"
	if _, replayed, err := registry.Create(ctx, "last-instance", instanceInput); err != nil || !replayed {
		t.Fatal("instance retry failed after capacity exhaustion")
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
