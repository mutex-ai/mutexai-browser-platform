package config

import (
	"strings"
	"testing"
)

func TestKeycloakConfiguration(t *testing.T) {
	t.Setenv("KEYCLOAK_URL", "https://auth.example.test")
	t.Setenv("KEYCLOAK_CLIENT_ID", "")
	t.Setenv("KEYCLOAK_CLIENT_SECRET", "private-secret")
	cfg, err := LoadKeycloak()
	if err != nil || cfg.ClientID != "platform-provisioner" || cfg.ClientSecret != "private-secret" {
		t.Fatal("Keycloak service-account configuration was not loaded")
	}
	t.Setenv("KEYCLOAK_CLIENT_SECRET", "")
	if _, err := LoadKeycloak(); err == nil || strings.Contains(err.Error(), "private-secret") {
		t.Fatal("missing Keycloak credentials must produce a safe validation error")
	}
}
