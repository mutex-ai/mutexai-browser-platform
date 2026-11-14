package api

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mutex-ai/mutexai-browser-platform/src/storage/partners"
	"go.uber.org/zap"
)

type partnerStub struct {
	calls int
	err   error
}

func (s *partnerStub) Create(_ context.Context, _ string, in partners.CreateInput) (partners.Partner, bool, error) {
	s.calls++
	return partners.Partner{PartnerID: 1000, ClientID: 1000, SiteID: 1000, SetupStatus: "pending", ConsoleDomains: in.ConsoleDomains}, false, s.err
}
func (s *partnerStub) Get(context.Context, int) (partners.Partner, error) {
	s.calls++
	return partners.Partner{PartnerID: 1000}, s.err
}
func TestPartnerAPI(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body, key, content string
		status                                 int
		err                                    error
	}{
		{name: "create", method: "POST", path: "/internal/v1/partners", body: `{"instance_id":1000,"name":"MSP","client_name":"Client","site_name":"Office","console_domains":["MSP.EXAMPLE."]}`, key: "request-1", content: "application/json", status: 202},
		{name: "oversized request", method: "POST", path: "/internal/v1/partners", body: strings.Repeat(" ", 65<<10) + `{}`, key: "large", content: "application/json", status: 413},
		{name: "missing key", method: "POST", path: "/internal/v1/partners", body: `{}`, content: "application/json", status: 400},
		{name: "unknown field", method: "POST", path: "/internal/v1/partners", body: `{"secret":"not-accepted"}`, key: "a", content: "application/json", status: 400},
		{name: "multiple objects", method: "POST", path: "/internal/v1/partners", body: `{} {}`, key: "a", content: "application/json", status: 400},
		{name: "content type", method: "POST", path: "/internal/v1/partners", body: `{}`, key: "a", status: 415},
		{name: "read", method: "GET", path: "/internal/v1/partners/1000", status: 200},
		{name: "bad id", method: "GET", path: "/internal/v1/partners/0999", status: 400},
		{name: "missing", method: "GET", path: "/internal/v1/partners/1000", status: 404, err: partners.ErrNotFound},
		{name: "storage failure", method: "GET", path: "/internal/v1/partners/1000", status: 503, err: errors.New("private-secret")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &partnerStub{err: tc.err}
			router := NewRouter(pingFunc(func(context.Context) error { return nil }), zap.NewNop(), store, nil, nil)
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			r.Header.Set("Idempotency-Key", tc.key)
			r.Header.Set("Content-Type", tc.content)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			if w.Code != tc.status || strings.Contains(w.Body.String(), "private-secret") {
				t.Fatalf("unexpected response: %d %s", w.Code, w.Body.String())
			}
			if tc.status == 202 && (w.Header().Get("Location") != "/internal/v1/partners/1000" || !strings.Contains(w.Body.String(), "msp.example")) {
				t.Fatal("creation response did not contain canonical domain and resource location")
			}
			if tc.status == 400 || tc.status == 413 || tc.status == 415 {
				if store.calls != 0 {
					t.Fatal("invalid request reached storage")
				}
			}
		})
	}
}
