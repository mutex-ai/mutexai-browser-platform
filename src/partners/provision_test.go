package partners

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/mutex-ai/mutexai-browser-platform/src/internal/keycloak"
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
	p, err := NewProvisioner(kc, SetupConfig{ConsoleURL: "https://console.example.test", ConsoleSecret: rand.Text() + rand.Text()})
	if err != nil {
		t.Fatal(err)
	}
	partner := Partner{PartnerID: 9999, Enabled: true, ClientName: "Initial client"}
	ids, err := p.Provision(ctx, job, partner)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := p.Provision(ctx, job, partner)
	if err != nil || repeated != ids {
		t.Fatalf("setup did not resume using the same identity resources: %v", err)
	}
	wrong := job
	wrong.SetupID = rand.Text()
	if _, err := p.Provision(ctx, wrong, partner); !errors.Is(err, ErrOwnership) {
		t.Fatal("unowned realm was adopted")
	}
	app, err := kc.FindApplication(ctx, "9999", "platform-ui")
	if err != nil {
		t.Fatal(err)
	}
	if err := kc.UpdateApplication(ctx, "9999", app.ID, keycloak.Application{StandardFlowEnabled: new(false)}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Provision(ctx, job, partner); !errors.Is(err, ErrConfiguration) {
		t.Fatal("unexpected login configuration was overwritten")
	}
	if err := kc.UpdateApplication(ctx, "9999", app.ID, keycloak.Application{StandardFlowEnabled: new(true), Secret: rand.Text() + rand.Text()}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Provision(ctx, job, partner); !errors.Is(err, ErrConfiguration) {
		t.Fatal("console credential mismatch was ignored")
	}
}

func TestSetupValidation(t *testing.T) {
	for _, origin := range []string{"https://console.example.test/path", "http://console.example.test", "https://user:password@console.example.test"} {
		if _, err := NewProvisioner(nil, SetupConfig{ConsoleURL: origin, ConsoleSecret: rand.Text() + rand.Text()}); err == nil {
			t.Fatal("unsafe console origin accepted")
		}
	}
	in := CreateInput{Name: " MSP ", ClientName: "Client", SiteName: "Site", ConsoleDomains: []string{"B.EXAMPLE.", "a.example", "A.EXAMPLE"}}
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
