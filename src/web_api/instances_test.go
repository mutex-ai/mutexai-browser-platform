package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mutex-ai/mutexai-browser-platform/src/storage/instances"
	"go.uber.org/zap"
)

type instanceStub struct{ calls int }

func (s *instanceStub) Create(_ context.Context, _ string, in instances.CreateInput) (instances.Instance, bool, error) {
	s.calls++
	return instances.Instance{InstanceID: 1000, ConsoleURL: in.ConsoleURL, ConsoleClientID: "platform-ui-1000"}, false, nil
}
func (s *instanceStub) Get(context.Context, int) (instances.Instance, error) {
	s.calls++
	return instances.Instance{InstanceID: 1000}, nil
}
func (s *instanceStub) SetAvailability(_ context.Context, id int, active bool) (instances.Instance, error) {
	s.calls++
	return instances.Instance{InstanceID: id, AcceptingClients: active}, nil
}
func TestInstanceAPI(t *testing.T) {
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"POST", "/internal/v1/instances", `{"name":"Shared","console_url":"https://console.example","backend_url":"https://api.example"}`, 201},
		{"POST", "/internal/v1/instances", `{"name":"Shared","console_url":"https://console.example","backend_url":"https://api.example","console_secret":"not-allowed"}`, 400},
		{"POST", "/internal/v1/instances", `{"name":"Shared","console_url":"https://console.example","backend_url":"https://api.example","console_secret_ref":"../PASSWORD"}`, 400},
		{"GET", "/internal/v1/instances/1000", "", 200},
		{"GET", "/internal/v1/instances/999", "", 400},
		{"PATCH", "/internal/v1/instances/1000", `{"accepting_clients":false}`, 200},
		{"PATCH", "/internal/v1/instances/1000", `{"console_url":"https://attacker.example"}`, 400},
	} {
		t.Run(tc.method+tc.body, func(t *testing.T) {
			store := &instanceStub{}
			router := NewRouter(pingFunc(func(context.Context) error { return nil }), zap.NewNop(), nil, store)
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", "instance-key")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			if response.Code != tc.status {
				t.Fatalf("unexpected response: %d %s", response.Code, response.Body.String())
			}
			if tc.status == 400 && store.calls != 0 {
				t.Fatal("invalid request reached storage")
			}
		})
	}
}
