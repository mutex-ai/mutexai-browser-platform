package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestPanicsAbortResponse(t *testing.T) {
	for _, tc := range []struct {
		name    string
		partial bool
		panic   any
		logs    int
	}{
		{name: "before response", panic: "private-secret", logs: 1},
		{name: "partial response", partial: true, panic: "private-secret", logs: 1},
		{name: "intentional abort", panic: http.ErrAbortHandler},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core, logs := observer.New(zap.ErrorLevel)
			srv := httptest.NewServer(recoverPanics(zap.New(core))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.partial {
					_, _ = w.Write([]byte(`{"unfinished":`))
				}
				panic(tc.panic)
			})))
			t.Cleanup(srv.Close)
			resp, err := srv.Client().Get(srv.URL)
			if resp != nil {
				_ = resp.Body.Close()
			}
			if err == nil {
				t.Fatal("panicked handler returned a completed HTTP response")
			}
			if logs.Len() != tc.logs {
				t.Fatalf("got %d log entries, want %d", logs.Len(), tc.logs)
			}
			for _, entry := range logs.All() {
				if strings.Contains(entry.Message, "private-secret") {
					t.Fatal("log exposed panic value")
				}
				for _, field := range entry.Context {
					if field.Key != "stack" {
						t.Fatalf("unexpected panic detail: %s", field.Key)
					}
				}
			}
		})
	}
}
