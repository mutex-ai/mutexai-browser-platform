package keycloak

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestAdminAPI creates only randomly named test realms. The supplied service
// account needs create-realm in master; no human admin password is accepted.
func TestAdminAPI(t *testing.T) {
	cfg := Config{URL: os.Getenv("TEST_KEYCLOAK_URL"), ClientID: os.Getenv("TEST_KEYCLOAK_CLIENT_ID"), ClientSecret: os.Getenv("TEST_KEYCLOAK_CLIENT_SECRET")}
	if cfg.URL == "" && cfg.ClientID == "" && cfg.ClientSecret == "" {
		t.Skip("set TEST_KEYCLOAK_URL, TEST_KEYCLOAK_CLIENT_ID and TEST_KEYCLOAK_CLIENT_SECRET")
	}
	client, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.CloseIdleConnections)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	prefix := "platform-kc-test-" + strings.ToLower(rand.Text()[:10])
	console, sso := prefix+"-1000", prefix+"-1000-sso"
	var created []string
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 20*time.Second)
		defer cancel()
		for _, realm := range created {
			if err := client.DeleteRealm(cleanupCtx, realm); err != nil {
				t.Errorf("remove test realm %s: %v", realm, err)
			}
		}
	})
	for _, realm := range []Realm{
		{Name: console, Enabled: new(true), RegistrationAllowed: new(false), AccessTokenLifespan: new(300), Attributes: map[string]string{"mutex.setup": prefix, "mutex.test-client": cfg.ClientID}},
		{Name: sso, Attributes: map[string]string{"mutex.test-client": cfg.ClientID}, Enabled: new(true), OrganizationsEnabled: new(true), RegistrationAllowed: new(false)},
	} {
		if err := client.CreateRealm(ctx, realm); err != nil {
			t.Fatal(err)
		}
		created = append(created, realm.Name)
		// This call fails with 403 if the cached pre-creation token is reused.
		got, err := client.GetRealm(ctx, realm.Name)
		if err != nil || got.ID == "" || got.Name != realm.Name {
			t.Fatalf("new realm is not manageable: %v", err)
		}
	}
	err = client.CreateRealm(ctx, Realm{Name: console})
	var status *APIError
	if !errors.As(err, &status) || status.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate realm returned %v, want conflict", err)
	}
	got, err := client.GetRealm(ctx, console)
	if err != nil || got.Attributes["mutex.setup"] != prefix {
		t.Fatal("conflicting creation changed the existing realm")
	}
	if err := client.UpdateRealm(ctx, console, Realm{Name: "renamed"}); err == nil {
		t.Fatal("realm rename was accepted")
	}
	if _, err := client.GetRealm(ctx, "master"); !errors.As(err, &status) || status.StatusCode != http.StatusForbidden {
		t.Fatalf("provisioner unexpectedly has full master access: %v", err)
	}

	resource, err := client.CreateApplication(ctx, console, Application{
		ClientID: "platform-api", PublicClient: new(false), StandardFlowEnabled: new(false), DirectAccessGrantsEnabled: new(false), ServiceAccountsEnabled: new(false),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"admin", "operator", "viewer"} {
		if err := client.CreateClientRole(ctx, console, resource, Role{Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	roles, err := client.ClientRoles(ctx, console, resource)
	if err != nil || len(roles) != 3 {
		t.Fatalf("application roles were not created: %v", err)
	}
	callback := "https://console.example.test/api/auth/callback/keycloak"
	loginID, err := client.CreateApplication(ctx, console, Application{
		ClientID: "platform-ui", Secret: rand.Text(), PublicClient: new(false), StandardFlowEnabled: new(true),
		DirectAccessGrantsEnabled: new(false), ServiceAccountsEnabled: new(false), FullScopeAllowed: new(false),
		RedirectURIs: new([]string{callback}), Attributes: map[string]string{"pkce.code.challenge.method": "S256"},
		ProtocolMappers: new([]Mapper{{Name: "platform-api-audience", Protocol: "openid-connect", ProtocolMapper: "oidc-audience-mapper", Config: map[string]string{"included.client.audience": "platform-api", "access.token.claim": "true"}}}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.AllowClientRoles(ctx, console, loginID, resource, roles); err != nil {
		t.Fatal(err)
	}
	if err := client.UpdateApplication(ctx, console, loginID, Application{Secret: rand.Text()}); err != nil {
		t.Fatal(err)
	}
	app, err := client.FindApplication(ctx, console, "platform-ui")
	if err != nil || app.ID != loginID || app.RedirectURIs == nil || len(*app.RedirectURIs) != 1 || (*app.RedirectURIs)[0] != callback || app.StandardFlowEnabled == nil || !*app.StandardFlowEnabled {
		t.Fatal("partial secret rotation changed the login client's other settings")
	}
	if _, err := client.CreateApplication(ctx, sso, Application{
		ClientID: "browser-extension", PublicClient: new(true), StandardFlowEnabled: new(true),
		DirectAccessGrantsEnabled: new(false), ServiceAccountsEnabled: new(false), FullScopeAllowed: new(false),
		RedirectURIs: new([]string{"https://cb.mutex.ai/ext/oauth/cb"}), Attributes: map[string]string{"pkce.code.challenge.method": "S256"},
	}); err != nil {
		t.Fatal(err)
	}
	firstID, err := client.CreateOrganization(ctx, sso, Organization{Name: "Client one", Alias: "1000", Enabled: new(true)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreateOrganization(ctx, sso, Organization{Name: "Client two", Alias: "1001", Enabled: new(true)}); err != nil {
		t.Fatal(err)
	}
	pageOne, err := client.Organizations(ctx, sso, 0, 1)
	if err != nil || len(pageOne) != 1 {
		t.Fatalf("first organization page failed: %v", err)
	}
	pageTwo, err := client.Organizations(ctx, sso, 1, 1)
	if err != nil || len(pageTwo) != 1 || pageTwo[0].ID == pageOne[0].ID {
		t.Fatalf("organization pagination failed: %v", err)
	}
	if err := client.UpdateOrganization(ctx, sso, firstID, Organization{Name: "Client one", Alias: "1000", Enabled: new(false)}); err != nil {
		t.Fatal(err)
	}
	org, err := client.GetOrganization(ctx, sso, firstID)
	if err != nil || org.Alias != "1000" || org.Enabled == nil || *org.Enabled {
		t.Fatal("organization disable was not applied")
	}
	scopes, err := client.ClientScopes(ctx, sso)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, scope := range scopes {
		if scope.Name != "organization" {
			continue
		}
		mappers, err := client.ScopeMappers(ctx, sso, scope.ID)
		if err != nil || len(mappers) != 1 {
			t.Fatalf("organization membership mapper missing: %v", err)
		}
		mappers[0].Config["addOrganizationId"] = "true"
		if err := client.UpdateScopeMapper(ctx, sso, scope.ID, mappers[0]); err != nil {
			t.Fatal(err)
		}
		found = true
	}
	if !found {
		t.Fatal("organization scope missing")
	}
}
