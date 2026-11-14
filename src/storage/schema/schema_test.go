package schema

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/mutex-ai/mutexai-browser-platform/src/storage/postgres"
)

// This test requires an unused disposable database. It never drops existing data.
func TestPostgresConcurrentMigrations(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var occupied bool
	if err := db.QueryRowContext(ctx, "SELECT to_regnamespace('platform') IS NOT NULL OR to_regclass('public.goose_db_version') IS NOT NULL").Scan(&occupied); err != nil {
		t.Fatal(err)
	}
	if occupied {
		t.Fatal("database already contains Platform objects; use a new disposable database")
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Go(func() { results <- Migrate(ctx, db) })
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("migration retry failed: %v", err)
	}
	var exists bool
	if err := db.QueryRowContext(ctx, "SELECT to_regnamespace('platform') IS NOT NULL").Scan(&exists); err != nil || !exists {
		t.Fatalf("Platform schema missing: %v", err)
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM public.goose_db_version WHERE version_id = 1 AND is_applied").Scan(&count); err != nil || count != 1 {
		t.Fatalf("migration applied more than once: count=%d error=%v", count, err)
	}
	if err := db.QueryRowContext(ctx, "SELECT to_regclass('platform.goose_db_version') IS NOT NULL").Scan(&exists); err != nil || exists {
		t.Fatalf("migration history must not follow a changed search path: %v", err)
	}
	t.Run("partner storage", func(t *testing.T) { testPartners(t, db) })

}
