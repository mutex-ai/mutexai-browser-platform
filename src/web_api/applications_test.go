package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/mutex-ai/mutexai-browser-platform/src/storage/applications"
)

const applicationBody = `{"slug":"example","kind":"application","profile":{"name":"Example","urls":{"homepage":["example.com"],"hosts":["EXAMPLE.COM."]}},"classification":{"category":"productivity","subcategory":"documents","modalities":{"in":[],"out":[]}}}`

type applicationStub struct {
	calls    int
	err      error
	replayed bool
}

func (s *applicationStub) result() (applications.Record, error) {
	s.calls++
	return applications.Record{ID: "abcdef123456", Revision: 2, App: json.RawMessage(`{"id":"abcdef123456","meta":{"revision":2}}`)}, s.err
}
func (s *applicationStub) Create(context.Context, string, applications.Definition) (applications.Record, bool, error) {
	r, e := s.result()
	return r, s.replayed, e
}
func (s *applicationStub) Update(_ context.Context, _ string, revision int64, _ applications.Definition) (applications.Record, error) {
	if revision != 1 {
		return applications.Record{}, applications.ErrRevisionConflict
	}
	return s.result()
}
func (s *applicationStub) Get(context.Context, string) (applications.Record, error) {
	return s.result()
}
func (s *applicationStub) List(context.Context) ([]applications.Record, error) {
	s.calls++
	return nil, s.err
}

func TestApplicationAPIValidationAndVersions(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body, match string
		status, calls                   int
	}{
		{"list", "GET", "/internal/v1/applications", "", "", 200, 1},
		{"get", "GET", "/internal/v1/applications/abcdef123456", "", "", 200, 1},
		{"create", "POST", "/internal/v1/applications", applicationBody, "", 201, 1},
		{"update", "PUT", "/internal/v1/applications/abcdef123456", applicationBody, `"abcdef123456:1"`, 200, 1},
		{"invalid ID", "GET", "/internal/v1/applications/1000", "", "", 400, 0},
		{"unknown body field", "POST", "/internal/v1/applications", strings.Replace(applicationBody, `"slug":`, `"unknown":true,"slug":`, 1), "", 400, 0},
		{"invalid host", "POST", "/internal/v1/applications", strings.Replace(applicationBody, "EXAMPLE.COM.", "https://example.com", 1), "", 400, 0},
		{"missing precondition", "PUT", "/internal/v1/applications/abcdef123456", applicationBody, "", 428, 0},
		{"wildcard precondition", "PUT", "/internal/v1/applications/abcdef123456", applicationBody, "*", 400, 0},
		{"weak precondition", "PUT", "/internal/v1/applications/abcdef123456", applicationBody, `W/"abcdef123456:1"`, 400, 0},
		{"other app precondition", "PUT", "/internal/v1/applications/abcdef123456", applicationBody, `"abcdef123457:1"`, 400, 0},
		{"oversized", "POST", "/internal/v1/applications", `{"slug":"` + strings.Repeat("x", 65536) + `"}`, "", 413, 0},
		{"delete unavailable", "DELETE", "/internal/v1/applications/abcdef123456", "", "", 405, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &applicationStub{}
			router := NewRouter(nil, zap.NewNop(), nil, nil, store)
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", "application-test")
			if tc.match != "" {
				req.Header.Set("If-Match", tc.match)
			}
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != tc.status || store.calls != tc.calls {
				t.Fatalf("status=%d calls=%d body=%s", w.Code, store.calls, w.Body.String())
			}
			if tc.status == 200 && tc.name == "list" && w.Body.String() != "{\"items\":[],\"count\":0}\n" {
				t.Fatal("empty registry must be an explicit empty collection")
			}
			if tc.status < 300 && tc.name != "list" && w.Header().Get("ETag") != `"abcdef123456:2"` {
				t.Fatal("successful record response has no usable ETag")
			}
		})
	}
}

func TestApplicationAPIErrors(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code int
		safe string
	}{
		{applications.ErrNotFound, 404, "not_found"},
		{applications.ErrSlugConflict, 409, "slug_conflict"},
		{applications.ErrHostConflict, 409, "host_conflict"},
		{applications.ErrRequestConflict, 409, "idempotency_conflict"},
		{applications.ErrRevisionConflict, 412, "revision_conflict"},
		{applications.ErrDependency, 400, "invalid_request"},
		{errors.New("postgres://user:private-secret@host"), 503, "storage_unavailable"},
	} {
		t.Run(tc.safe, func(t *testing.T) {
			router := NewRouter(nil, zap.NewNop(), nil, nil, &applicationStub{err: tc.err})
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest("GET", "/internal/v1/applications/abcdef123456", nil))
			if w.Code != tc.code || !strings.Contains(w.Body.String(), tc.safe) || strings.Contains(w.Body.String(), "private-secret") {
				t.Fatalf("unsafe or wrong error: %d %s", w.Code, w.Body.String())
			}
		})
	}
	store := &applicationStub{replayed: true}
	router := NewRouter(nil, zap.NewNop(), nil, nil, store)
	req := httptest.NewRequest("POST", "/internal/v1/applications", strings.NewReader(applicationBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "application-test")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 || w.Header().Get("Idempotency-Replayed") != "true" || w.Header().Get("Location") != "/internal/v1/applications/abcdef123456" {
		t.Fatal("creation replay metadata missing")
	}
}
