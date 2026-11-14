package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

type pingFunc func(context.Context) error

func (f pingFunc) PingContext(ctx context.Context) error { return f(ctx) }

func TestReadinessFailureDoesNotFailLiveness(t *testing.T) {
	calls := 0
	router := NewRouter(pingFunc(func(ctx context.Context) error {
		calls++
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 2*time.Second {
			t.Error("database check must have a bounded deadline")
		}
		return errors.New("postgres://user:private-secret@host/platform")
	}), zap.NewNop(), nil, nil, nil)
	for _, tc := range []struct {
		path string
		code int
	}{
		{"/internal/v1/health", http.StatusOK},
		{"/internal/v1/health/ready", http.StatusServiceUnavailable},
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if w.Code != tc.code || strings.Contains(w.Body.String(), "private-secret") {
			t.Fatalf("unexpected response: %d %s", w.Code, w.Body.String())
		}
	}
	if calls != 1 {
		t.Fatal("liveness must not query PostgreSQL")
	}
}

func TestReadinessRecovery(t *testing.T) {
	available := false
	router := NewRouter(pingFunc(func(context.Context) error {
		if !available {
			return errors.New("database unavailable")
		}
		return nil
	}), zap.NewNop(), nil, nil, nil)
	for _, code := range []int{http.StatusServiceUnavailable, http.StatusOK} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/internal/v1/health/ready", nil))
		if w.Code != code {
			t.Fatalf("expected %d, got %d", code, w.Code)
		}
		available = true
	}
}

func TestRemovedRoutesAndMethods(t *testing.T) {
	router := NewRouter(pingFunc(func(context.Context) error { return nil }), zap.NewNop(), nil, nil, nil)
	for _, path := range []string{"/api/v1/health", "/api/v1/devices", "/api/v1/agent/telemetry", "/api/v1/identity/token", "/internal/v1/partners"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusNotFound {
			t.Fatalf("unexpected route retained: %s", path)
		}
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/internal/v1/health", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("unexpected health method accepted: %d", w.Code)
	}
}
