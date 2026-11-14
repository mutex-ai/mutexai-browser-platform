package schema

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/mutex-ai/mutexai-browser-platform/src/storage/applications"
	api "github.com/mutex-ai/mutexai-browser-platform/src/web_api"
)

func applicationDefinition(t *testing.T, slug string, hosts ...string) applications.Definition {
	t.Helper()
	in, err := applications.ParseDefinition(applicationJSON(slug, hosts...))
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func applicationJSON(slug string, hosts ...string) []byte {
	if hosts == nil {
		hosts = []string{}
	}
	encoded, _ := json.Marshal(hosts)
	return []byte(fmt.Sprintf(`{"slug":%q,"kind":"application","profile":{"name":"Example","urls":{"homepage":["example.com"],"hosts":%s}},"classification":{"category":"productivity","subcategory":"documents","modalities":{"in":[],"out":[]}}}`, slug, encoded))
}

func testApplications(t *testing.T, db *sql.DB) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	store, other := applications.NewStore(db), applications.NewStore(db)
	in := applicationDefinition(t, "original", " WWW.Example.COM. ", "EXAMPLE.com", "*.BÜCHER.de.")
	start := time.Now().UTC()
	record, replayed, err := store.Create(ctx, "app-original", in)
	if err != nil || replayed || !applications.ValidID(record.ID) || record.Revision != 1 {
		t.Fatalf("create: %+v replayed=%v error=%v", record, replayed, err)
	}
	if record.CreatedTSUTC.Before(start) || record.CreatedTSUTC.Location() != time.UTC || !record.CreatedTSUTC.Equal(record.UpdatedTSUTC) {
		t.Fatal("record timestamps must come from Go in UTC")
	}
	var storedSeparate bool
	if err := db.QueryRowContext(ctx, `SELECT NOT definition ? 'id' AND definition #> '{meta,revision}' IS NULL AND definition #> '{profile,urls,hosts}' IS NULL FROM platform.applications WHERE app_id=$1`, record.ID).Scan(&storedSeparate); err != nil || !storedSeparate {
		t.Fatalf("managed values/hosts duplicated in JSON: %v", err)
	}
	same, replayed, err := other.Create(ctx, "app-original", applicationDefinition(t, " ORIGINAL ", "*.xn--bcher-kva.de", "example.com"))
	if err != nil || !replayed || same.ID != record.ID || !same.UpdatedTSUTC.Equal(record.UpdatedTSUTC) {
		t.Fatalf("normalized retry changed record: %v", err)
	}
	if _, _, err := store.Create(ctx, "app-original", applicationDefinition(t, "changed", "changed.example")); !errors.Is(err, applications.ErrRequestConflict) {
		t.Fatalf("changed creation retry accepted: %v", err)
	}
	if _, _, err := store.Create(ctx, "app-slug-conflict", applicationDefinition(t, "Original", "other.example")); !errors.Is(err, applications.ErrSlugConflict) {
		t.Fatalf("duplicate slug accepted: %v", err)
	}
	if _, _, err := store.Create(ctx, "app-host-conflict", applicationDefinition(t, "other-host-owner", "www.EXAMPLE.COM")); !errors.Is(err, applications.ErrHostConflict) {
		t.Fatalf("duplicate normalized host accepted: %v", err)
	}

	t.Run("concurrent creation retries", func(t *testing.T) {
		input := applicationDefinition(t, "retry-race", "retry.example")
		var wg sync.WaitGroup
		results := make(chan applications.Record, 12)
		failures := make(chan error, 12)
		for range 12 {
			wg.Go(func() {
				r, _, err := other.Create(ctx, "app-retry-race", input)
				if err != nil {
					failures <- err
				} else {
					results <- r
				}
			})
		}
		wg.Wait()
		close(results)
		close(failures)
		for err := range failures {
			t.Error(err)
		}
		id := ""
		for r := range results {
			if id == "" {
				id = r.ID
			}
			if r.ID != id || r.Revision != 1 {
				t.Fatal("retry allocated another application or changed revision")
			}
		}
	})
	t.Run("concurrent normalized host claims", func(t *testing.T) {
		var wg sync.WaitGroup
		results := make(chan error, 12)
		for i := range 12 {
			host := " WWW.BÜCHER.EXAMPLE. "
			if i%2 == 0 {
				host = "xn--bcher-kva.example"
			}
			input := applicationDefinition(t, fmt.Sprintf("host-race-%d", i), host)
			wg.Go(func() { _, _, err := other.Create(ctx, fmt.Sprintf("app-host-race-%d", i), input); results <- err })
		}
		wg.Wait()
		close(results)
		successes := 0
		for err := range results {
			if err == nil {
				successes++
			} else if !errors.Is(err, applications.ErrHostConflict) {
				t.Error(err)
			}
		}
		if successes != 1 {
			t.Fatalf("%d apps claimed the same normalized host", successes)
		}
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM platform.applications WHERE definition->>'slug' LIKE 'host-race-%'`).Scan(&count); err != nil || count != 1 {
			t.Fatal("conflicting create left orphaned application rows")
		}
	})
	t.Run("concurrent slug claims", func(t *testing.T) {
		var wg sync.WaitGroup
		results := make(chan error, 8)
		for i := range 8 {
			input := applicationDefinition(t, " SLUG-RACE ", fmt.Sprintf("slug-%d.example", i))
			wg.Go(func() { _, _, err := other.Create(ctx, fmt.Sprintf("app-slug-race-%d", i), input); results <- err })
		}
		wg.Wait()
		close(results)
		successes := 0
		for err := range results {
			if err == nil {
				successes++
			} else if !errors.Is(err, applications.ErrSlugConflict) {
				t.Error(err)
			}
		}
		if successes != 1 {
			t.Fatalf("%d apps claimed the same slug", successes)
		}
	})
	t.Run("concurrent edits and rollback", func(t *testing.T) {
		var wg sync.WaitGroup
		results := make(chan error, 10)
		for i := range 10 {
			input := applicationDefinition(t, "original", fmt.Sprintf("edited-%d.example", i))
			wg.Go(func() { _, err := other.Update(ctx, record.ID, 1, input); results <- err })
		}
		wg.Wait()
		close(results)
		successes := 0
		for err := range results {
			if err == nil {
				successes++
			} else if !errors.Is(err, applications.ErrRevisionConflict) {
				t.Error(err)
			}
		}
		if successes != 1 {
			t.Fatalf("%d concurrent edits overwrote revision 1", successes)
		}
		before, err := store.Get(ctx, record.ID)
		if err != nil || before.Revision != 2 || !before.CreatedTSUTC.Equal(record.CreatedTSUTC) {
			t.Fatalf("incorrect edited record: %v", err)
		}
		if _, _, err := store.Create(ctx, "app-reserved-host", applicationDefinition(t, "reserved", "reserved.example")); err != nil {
			t.Fatal(err)
		}
		if _, err := other.Update(ctx, record.ID, 2, applicationDefinition(t, "replacement", "reserved.example")); !errors.Is(err, applications.ErrHostConflict) {
			t.Fatalf("conflicting edit accepted: %v", err)
		}
		after, err := store.Get(ctx, record.ID)
		if err != nil || string(after.App) != string(before.App) || after.Revision != before.Revision || !after.UpdatedTSUTC.Equal(before.UpdatedTSUTC) {
			t.Fatal("failed edit changed definition, hosts, revision or timestamps")
		}
		var oldHosts int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM platform.application_hosts WHERE host_pattern IN ('example.com','*.xn--bcher-kva.de')`).Scan(&oldHosts); err != nil || oldHosts != 0 {
			t.Fatal("successful edit retained removed host claims")
		}
		current, replayed, err := other.Create(ctx, "app-original", in)
		if err != nil || !replayed || current.Revision != 2 || string(current.App) != string(after.App) {
			t.Fatalf("creation replay overwrote a later edit: %v", err)
		}
	})
	t.Run("dependencies", func(t *testing.T) {
		raw := strings.Replace(string(applicationJSON("dependent", "dependent.example")), `"name":"Example"`, `"name":"Example","depends_on":["ffffffffffff"]`, 1)
		input, err := applications.ParseDefinition([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.Create(ctx, "app-dependency", input); !errors.Is(err, applications.ErrDependency) {
			t.Fatalf("missing reference accepted: %v", err)
		}
		raw = strings.Replace(raw, "ffffffffffff", record.ID, 1)
		input, err = applications.ParseDefinition([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.Create(ctx, "app-dependency", input); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Update(ctx, record.ID, 2, input); !errors.Is(err, applications.ErrDependency) {
			t.Fatalf("self-reference accepted: %v", err)
		}
	})
	t.Run("complete read and identity records", func(t *testing.T) {
		raw := strings.Replace(string(applicationJSON("identity-node", "accounts.example")), `"kind":"application"`, `"kind":"identity"`, 1)
		raw = strings.Replace(raw, `"category":"productivity","subcategory":"documents",`, "", 1)
		input, err := applications.ParseDefinition([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		identity, _, err := store.Create(ctx, "app-identity", input)
		if err != nil {
			t.Fatal(err)
		}
		all, err := other.List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		previous := ""
		for _, app := range all {
			if app.ID <= previous {
				t.Fatal("registry order unstable")
			}
			previous = app.ID
			if app.ID == identity.ID {
				found = true
			}
		}
		if !found {
			t.Fatal("complete registry omitted identity hosts")
		}
		if _, err := store.Get(ctx, "ffffffffffff"); !errors.Is(err, applications.ErrNotFound) {
			t.Fatalf("missing app read: %v", err)
		}
		if _, err := store.Update(ctx, "ffffffffffff", 1, applicationDefinition(t, "missing")); !errors.Is(err, applications.ErrNotFound) {
			t.Fatalf("PUT created an unknown app: %v", err)
		}
	})
	t.Run("HTTP end to end", func(t *testing.T) { testApplicationHTTP(t, db) })
}

func testApplicationHTTP(t *testing.T, db *sql.DB) {
	router := api.NewRouter(db, zap.NewNop(), nil, nil, applications.NewStore(db))
	request := func(method, path string, body []byte, etag string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "http-application")
		if etag != "" {
			req.Header.Set("If-Match", etag)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	body := applicationJSON(" HTTP-APP ", " WWW.HTTP-APP.EXAMPLE. ")
	created := request("POST", "/internal/v1/applications", body, "")
	if created.Code != 201 {
		t.Fatalf("POST: %d %s", created.Code, created.Body.String())
	}
	path, etag := created.Header().Get("Location"), created.Header().Get("ETag")
	if !strings.HasSuffix(etag, `:1"`) || !strings.Contains(created.Body.String(), `"hosts":["http-app.example"]`) || !strings.Contains(created.Body.String(), `Z"`) {
		t.Fatal("POST did not return normalized hosts, revision and UTC times")
	}
	if replay := request("POST", "/internal/v1/applications", applicationJSON("http-app", "http-app.example"), ""); replay.Code != 200 || replay.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatal("HTTP creation retry not idempotent")
	}
	if got := request("GET", path, nil, ""); got.Code != 200 || got.Header().Get("ETag") != etag {
		t.Fatal("HTTP GET differs from create")
	}
	updated := request("PUT", path, applicationJSON("http-app", "updated-http.example"), etag)
	if updated.Code != 200 || updated.Header().Get("ETag") == etag {
		t.Fatalf("PUT failed: %d %s", updated.Code, updated.Body.String())
	}
	if stale := request("PUT", path, body, etag); stale.Code != 412 {
		t.Fatalf("stale PUT returned %d", stale.Code)
	}
	if missing := request("PUT", path, body, ""); missing.Code != 428 {
		t.Fatal("unguarded PUT accepted")
	}
	if all := request("GET", "/internal/v1/applications", nil, ""); all.Code != 200 || !strings.Contains(all.Body.String(), `"slug":"http-app"`) {
		t.Fatal("HTTP registry read omitted app")
	}
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	if got := request("GET", path, nil, ""); got.Code != 200 || !strings.Contains(got.Body.String(), "updated-http.example") {
		t.Fatal("startup migrations overwrote an operator edit")
	}
}
