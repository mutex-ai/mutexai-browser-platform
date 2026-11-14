package keycloak

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client, err := New(Config{URL: srv.URL, ClientID: "platform-provisioner", ClientSecret: "private-secret"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.CloseIdleConnections)
	return client
}

func tokenResponse(w http.ResponseWriter, token string) {
	_, _ = fmt.Fprintf(w, `{"access_token":%q,"token_type":"Bearer","expires_in":60}`, token)
}

func TestConcurrentTokenUseAndRealmCreation(t *testing.T) {
	var tokenCalls atomic.Int32
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/realms/master/protocol/openid-connect/token" {
			if err := r.ParseForm(); err != nil || r.Form.Get("grant_type") != "client_credentials" || r.Form.Get("client_secret") != "private-secret" {
				t.Error("wrong service-account authentication")
			}
			tokenResponse(w, fmt.Sprintf("token-%d", tokenCalls.Add(1)))
			return
		}
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
			return
		}
		_ = json.NewEncoder(w).Encode(Realm{ID: "realm-id", Name: strings.TrimPrefix(r.URL.Path, "/admin/realms/")})
	})
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if _, err := client.GetRealm(t.Context(), "1000"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if tokenCalls.Load() != 1 {
		t.Fatalf("concurrent calls fetched %d tokens, want one", tokenCalls.Load())
	}
	if err := client.CreateRealm(t.Context(), Realm{Name: "1001"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetRealm(t.Context(), "1001"); err != nil {
		t.Fatal(err)
	}
	if tokenCalls.Load() != 2 {
		t.Fatal("realm creation must renew the cached token for its newly granted roles")
	}
}

func TestAuthenticationRetryAndFailureHandling(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		wantCalls  int32
		wantTokens int32
	}{
		{name: "unauthorized refreshes once", status: 401, wantCalls: 2, wantTokens: 2},
		{name: "forbidden is final", status: 403, wantCalls: 1, wantTokens: 1},
		{name: "conflict is final", status: 409, wantCalls: 1, wantTokens: 1},
		{name: "server failure is not replayed", status: 503, wantCalls: 1, wantTokens: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls, tokens atomic.Int32
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/token") {
					tokenResponse(w, fmt.Sprintf("token-%d", tokens.Add(1)))
					return
				}
				calls.Add(1)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"error":"private-secret and bearer-token"}`))
			})
			err := client.CreateRealm(t.Context(), Realm{Name: "1000"})
			var status *APIError
			if !errors.As(err, &status) || status.StatusCode != tc.status || strings.Contains(err.Error(), "private-secret") || strings.Contains(err.Error(), "bearer-token") {
				t.Fatalf("unexpected or unsafe error: %v", err)
			}
			if calls.Load() != tc.wantCalls || tokens.Load() != tc.wantTokens {
				t.Fatalf("calls=%d tokens=%d, want calls=%d tokens=%d", calls.Load(), tokens.Load(), tc.wantCalls, tc.wantTokens)
			}
		})
	}
}

func TestUnauthorizedRefreshSucceeds(t *testing.T) {
	var tokens atomic.Int32
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			tokenResponse(w, fmt.Sprintf("token-%d", tokens.Add(1)))
			return
		}
		if r.Header.Get("Authorization") == "Bearer token-1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"id":"realm-id","realm":"1000"}`))
	})
	if _, err := client.GetRealm(t.Context(), "1000"); err != nil || tokens.Load() != 2 {
		t.Fatalf("token refresh failed: %v", err)
	}
}

func TestTokenWaitRespectsCancellation(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			close(started)
			<-release
			tokenResponse(w, "token")
			return
		}
		_, _ = w.Write([]byte(`{"id":"realm-id","realm":"1000"}`))
	})
	// Release the test server even when an assertion fails.
	var once sync.Once
	defer once.Do(func() { close(release) })
	result := make(chan error, 1)
	go func() { _, err := client.GetRealm(t.Context(), "1000"); result <- err }()
	<-started
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := client.GetRealm(ctx, "1000"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("token wait returned %v, want deadline exceeded", err)
	}
	once.Do(func() { close(release) })
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestRedirectsNeverReceiveCredentials(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Add(1) }))
	t.Cleanup(target.Close)
	for _, redirectToken := range []bool{true, false} {
		t.Run(strconv.FormatBool(redirectToken), func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/token") && !redirectToken {
					tokenResponse(w, "token")
					return
				}
				http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
			})
			_, err := client.GetRealm(t.Context(), "1000")
			var status *APIError
			if !errors.As(err, &status) || status.StatusCode != 307 {
				t.Fatalf("redirect was not rejected: %v", err)
			}
		})
	}
	if redirected.Load() != 0 {
		t.Fatal("credentials followed a redirect")
	}
}

func TestInvalidResponsesAndIdentifiers(t *testing.T) {
	for _, body := range []string{`not-json private-secret`, `{"access_token":"private-secret","token_type":"Bearer","expires_in":0}`, `{"access_token":"private-secret","token_type":"other","expires_in":60}`} {
		client := testClient(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) })
		if _, err := client.GetRealm(t.Context(), "1000"); err == nil || strings.Contains(err.Error(), "private-secret") {
			t.Fatal("invalid token response was accepted or disclosed")
		}
	}
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			tokenResponse(w, "token")
			return
		}
		_, _ = w.Write([]byte(`{"realm":"1000"}`))
	})
	if _, err := client.GetRealm(t.Context(), "../master"); err == nil {
		t.Fatal("path traversal was accepted")
	}
	if _, err := client.GetRealm(t.Context(), "1000"); err == nil {
		t.Fatal("limited realm metadata was accepted as full administration access")
	}
	large := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			tokenResponse(w, "token")
			return
		}
		_, _ = w.Write([]byte(strings.Repeat("x", maxResponseBytes+1)))
	})
	if _, err := large.GetRealm(t.Context(), "1000"); err == nil {
		t.Fatal("oversized response was accepted")
	}
}

func TestPartialUpdateAndExactLookup(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			tokenResponse(w, "token")
			return
		}
		if r.Method == http.MethodPut {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if len(body) != 2 || body["enabled"] != false || len(body["redirectUris"].([]any)) != 0 {
				t.Error("update failed to distinguish omitted fields from false and empty values")
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Query().Get("clientId") != "platform-ui&unexpected=value" || r.URL.Query().Get("unexpected") != "" {
			t.Error("client lookup did not encode its exact query value")
		}
		_, _ = w.Write([]byte(`[{"id":"unexpected-id","clientId":"wrong-client"}]`))
	})
	if err := client.UpdateApplication(t.Context(), "1000", "client-id", Application{Enabled: new(false), RedirectURIs: new([]string{})}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.FindApplication(t.Context(), "1000", "platform-ui&unexpected=value"); err == nil {
		t.Fatal("lookup accepted a different client ID")
	}
}

func TestInvalidConfiguration(t *testing.T) {
	for _, address := range []string{"", "file:///tmp/keycloak", "https://user:private-secret@auth.example.test", "https://auth.example.test?secret=private-secret", "https://auth.example.test#private-secret"} {
		if _, err := New(Config{URL: address, ClientID: "client", ClientSecret: "private-secret"}); err == nil || strings.Contains(err.Error(), "private-secret") {
			t.Fatal("invalid configuration was accepted or disclosed")
		}
	}
}

func TestExpiredTokenIsRenewed(t *testing.T) {
	var tokens atomic.Int32
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			_, _ = fmt.Fprintf(w, `{"access_token":"token-%d","token_type":"Bearer","expires_in":1}`, tokens.Add(1))
			return
		}
		_, _ = w.Write([]byte(`{"id":"realm-id","realm":"1000"}`))
	})
	if _, err := client.GetRealm(t.Context(), "1000"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	if _, err := client.GetRealm(t.Context(), "1000"); err != nil {
		t.Fatal(err)
	}
	if tokens.Load() != 2 {
		t.Fatal("expired token was reused")
	}
}

func TestAmbiguousCreationFailureIsNotReplayed(t *testing.T) {
	var calls atomic.Int32
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			tokenResponse(w, "token")
			return
		}
		calls.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close() // Model a committed creation whose response was lost.
	})
	if err := client.CreateRealm(t.Context(), Realm{Name: "1000"}); err == nil {
		t.Fatal("lost creation response was treated as success")
	}
	if calls.Load() != 1 {
		t.Fatal("ambiguous creation request was replayed")
	}
}

func TestCanceledRequestCanBeFollowedBySuccessfulRequest(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{})
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			tokenResponse(w, "token")
			return
		}
		if calls.Add(1) == 1 {
			close(started)
			<-r.Context().Done()
			return
		}
		_, _ = w.Write([]byte(`{"id":"realm-id","realm":"1000"}`))
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := client.GetRealm(ctx, "1000"); result <- err }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request returned %v", err)
	}
	if _, err := client.GetRealm(t.Context(), "1000"); err != nil {
		t.Fatal(err)
	}
}

func TestCreationRejectsCopiedInternalIDs(t *testing.T) {
	client := testClient(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid creation reached the server") })
	if err := client.CreateRealm(t.Context(), Realm{ID: "copied-id", Name: "1000"}); err == nil {
		t.Fatal("copied realm ID accepted")
	}
	if _, err := client.CreateApplication(t.Context(), "1000", Application{ID: "copied-id", ClientID: "platform-ui"}); err == nil {
		t.Fatal("copied application ID accepted")
	}
	if _, err := client.CreateOrganization(t.Context(), "1000-sso", Organization{ID: "copied-id", Name: "Client", Alias: "1000"}); err == nil {
		t.Fatal("copied organization ID accepted")
	}
	if err := client.CreateClientRole(t.Context(), "1000", "client-id", Role{ID: "copied-id", Name: "admin"}); err == nil {
		t.Fatal("copied role ID accepted")
	}
}

func TestRealmResponseMustMatchRequest(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			tokenResponse(w, "token")
			return
		}
		_, _ = w.Write([]byte(`{"id":"other-realm-id","realm":"1001"}`))
	})
	if _, err := client.GetRealm(t.Context(), "1000"); err == nil {
		t.Fatal("response for another realm was accepted")
	}
}
