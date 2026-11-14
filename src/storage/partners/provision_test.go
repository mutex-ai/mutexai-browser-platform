package partners

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/mutex-ai/mutexai-browser-platform/src/internal/keycloak"
	"github.com/mutex-ai/mutexai-browser-platform/src/storage/instances"
)

func TestProvisioner(t *testing.T) {
	cfg := keycloak.Config{URL: os.Getenv("TEST_KEYCLOAK_URL"), ClientID: os.Getenv("TEST_KEYCLOAK_CLIENT_ID"), ClientSecret: os.Getenv("TEST_KEYCLOAK_CLIENT_SECRET")}
	if cfg.URL == "" {
		t.Skip("set TEST_KEYCLOAK_* for local Keycloak integration")
	}
	kc, err := keycloak.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(kc.CloseIdleConnections)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	job := Job{PartnerID: 9999, SetupID: rand.Text()}
	for _, name := range []string{"9999", "9999-sso"} {
		if _, err := kc.GetRealm(ctx, name); !apiStatus(err, 404) {
			t.Skip("integration realm name is already occupied; preserved it")
		}
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(t.Context()), 20*time.Second)
		defer stop()
		for _, name := range []string{"9999", "9999-sso"} {
			realm, err := kc.GetRealm(cleanup, name)
			if apiStatus(err, 404) {
				continue
			}
			if err != nil || realm.Attributes["mutex.setup_id"] != job.SetupID {
				t.Errorf("test realm ownership changed; preserved %s", name)
				continue
			}
			if err := kc.DeleteRealm(cleanup, name); err != nil {
				t.Error(err)
			}
		}
	})
	p := NewProvisioner(kc)
	partner := Partner{PartnerID: 9999, Enabled: true, ClientName: "Initial client", InstanceID: 1000, ConsoleURL: "https://console.example.test", ConsoleClientID: "platform-ui-1000"}
	ids, err := p.Provision(ctx, job, partner)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := p.Provision(ctx, job, partner)
	if err != nil || repeated != ids {
		t.Fatalf("setup did not resume using the same identity resources: %v", err)
	}
	if err := kc.UpdateRealm(ctx, "9999", keycloak.Realm{BruteForceProtected: new(false)}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Provision(ctx, job, partner); !errors.Is(err, ErrConfiguration) {
		t.Fatal("weakened realm security settings were accepted")
	}
	if err := kc.UpdateRealm(ctx, "9999", keycloak.Realm{BruteForceProtected: new(true)}); err != nil {
		t.Fatal(err)
	}
	wrong := job
	wrong.SetupID = rand.Text()
	if _, err := p.Provision(ctx, wrong, partner); !errors.Is(err, ErrOwnership) {
		t.Fatal("unowned realm was adopted")
	}
	app, err := kc.FindApplication(ctx, "9999", "platform-ui-1000")
	if err != nil {
		t.Fatal(err)
	}
	if app.ClientAuthenticatorType != "client-jwt" || app.Attributes["jwks.url"] != "https://console.example.test/.well-known/jwks.json" || app.Attributes["use.jwks.url"] != "true" || app.Attributes["token.endpoint.auth.signing.alg"] != "RS256" || app.Attributes["token.endpoint.auth.signing.max.exp"] != "60" {
		t.Fatal("console public-key authentication was not configured")
	}
	// One partner can later have clients on different instances. Their console
	// registrations must coexist without changing either callback or key source.
	second := partner
	second.InstanceID, second.ConsoleClientID, second.ConsoleURL = 1001, "platform-ui-1001", "https://console-b.example.test"
	if got, err := p.Provision(ctx, job, second); err != nil || got != ids {
		t.Fatalf("second instance changed the partner's identity resources: %v", err)
	}
	other, err := kc.FindApplication(ctx, "9999", second.ConsoleClientID)
	if err != nil || other.Attributes["jwks.url"] != instances.ConsoleJWKSURL(second.ConsoleURL) || other.RedirectURIs == nil || (*other.RedirectURIs)[0] != second.ConsoleURL+"/api/auth/callback/keycloak-9999" {
		t.Fatal("second instance inherited another instance's login configuration")
	}
	if _, err := p.Provision(ctx, job, partner); err != nil {
		t.Fatalf("first instance stopped working after adding another: %v", err)
	}
	if app.OptionalClientScopes == nil || len(*app.OptionalClientScopes) != 0 {
		t.Fatal("console inherited optional scopes including offline access")
	}
	attributes := app.Attributes
	attributes["jwks.url"] = "https://unrelated.example.test/keys"
	if err := kc.UpdateApplication(ctx, "9999", app.ID, keycloak.Application{Attributes: attributes}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Provision(ctx, job, partner); !errors.Is(err, ErrConfiguration) {
		t.Fatal("unexpected public-key source was overwritten")
	}
	attributes["jwks.url"] = instances.ConsoleJWKSURL(partner.ConsoleURL)
	if err := kc.UpdateApplication(ctx, "9999", app.ID, keycloak.Application{Attributes: attributes}); err != nil {
		t.Fatal(err)
	}
	if err := kc.UpdateApplication(ctx, "9999", app.ID, keycloak.Application{StandardFlowEnabled: new(false)}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Provision(ctx, job, partner); !errors.Is(err, ErrConfiguration) {
		t.Fatal("unexpected login configuration was overwritten")
	}
	if err := kc.UpdateApplication(ctx, "9999", app.ID, keycloak.Application{StandardFlowEnabled: new(true), ClientAuthenticatorType: "client-secret"}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Provision(ctx, job, partner); !errors.Is(err, ErrConfiguration) {
		t.Fatal("console client authentication drift was ignored")
	}
}

func TestSetupValidation(t *testing.T) {
	for _, origin := range []string{"https://console.example.test/path", "http://console.example.test", "https://user:password@console.example.test"} {
		if _, err := instances.Origin(origin, true); err == nil {
			t.Fatal("unsafe console origin accepted")
		}
	}
	in := CreateInput{InstanceID: 1000, Name: " MSP ", ClientName: "Client", SiteName: "Site", ConsoleDomains: []string{"B.EXAMPLE.", "a.example", "A.EXAMPLE"}}
	if err := in.Normalize(); err != nil || len(in.ConsoleDomains) != 2 || in.ConsoleDomains[0] != "a.example" {
		t.Fatal("request was not normalized")
	}
	for _, domain := range []string{"http://msp.example", "person@msp.example", "127.0.0.1", "*.example", "bad-.example"} {
		in.ConsoleDomains = []string{domain}
		if err := in.Normalize(); err == nil {
			t.Fatal("invalid domain accepted")
		}
	}
}
