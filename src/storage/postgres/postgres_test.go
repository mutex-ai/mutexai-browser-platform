package postgres

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestOpenFailure(t *testing.T) {
	t.Run("invalid settings", func(t *testing.T) {
		db, err := Open(t.Context(), "postgres://user:private-secret@localhost:invalid/platform")
		if db != nil || err == nil || strings.Contains(err.Error(), "private-secret") {
			t.Fatal("invalid settings must return no pool and a safe error")
		}
	})
	t.Run("canceled startup", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		db, err := Open(ctx, "postgres://user:password@127.0.0.1:1/platform?sslmode=disable")
		if db != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("Open() = %v, %v; want no pool and cancellation", db, err)
		}
	})
}

func TestPoolLifecycle(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	db, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var zone string
	if err := db.QueryRowContext(ctx, "SHOW timezone").Scan(&zone); err != nil || zone != "UTC" {
		t.Fatalf("application connection timezone = %q, want UTC: %v", zone, err)
	}

	t.Run("pool saturation respects cancellation", func(t *testing.T) {
		var held []*sql.Conn
		defer func() {
			for _, conn := range held {
				_ = conn.Close()
			}
		}()
		limit := db.Stats().MaxOpenConnections
		if limit != 10 {
			t.Fatalf("connection limit = %d, want 10", limit)
		}
		for range limit {
			conn, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			held = append(held, conn)
		}
		waitCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		conn, err := db.Conn(waitCtx)
		if conn != nil {
			_ = conn.Close()
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("exhausted pool returned %v, want deadline exceeded", err)
		}
		if db.Stats().OpenConnections != limit {
			t.Fatal("pool exceeded its connection limit")
		}
	})
	t.Run("slow query cancels and pool recovers", func(t *testing.T) {
		queryCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		if _, err := db.ExecContext(queryCtx, "SELECT pg_sleep(10)"); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("slow query returned %v, want deadline exceeded", err)
		}
		if err := db.PingContext(ctx); err != nil {
			t.Fatalf("pool did not recover after cancellation: %v", err)
		}
	})
	t.Run("terminated connection is replaced", func(t *testing.T) {
		// Leave only one connection so recovery must establish a new one.
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		// Terminate only this test's own connection, never another session.
		if _, err := db.ExecContext(ctx, "SELECT pg_terminate_backend(pg_backend_pid())"); err == nil {
			t.Fatal("terminated connection unexpectedly succeeded")
		}
		if err := db.PingContext(ctx); err != nil {
			t.Fatalf("pool did not recover after disconnect: %v", err)
		}
	})

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if db.Stats().OpenConnections != 0 {
		t.Fatal("close left idle connections open")
	}
	if err := db.PingContext(ctx); err == nil {
		t.Fatal("closed pool accepted new work")
	}
}
