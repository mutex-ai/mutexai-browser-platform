package config

import (
	"os"

	"github.com/mutex-ai/mutexai-browser-platform/src/partners"

	"github.com/mutex-ai/mutexai-browser-platform/src/internal/keycloak"
)

// LoadKeycloak loads the service-account configuration for identity setup.
// Call it when constructing the setup client, independently of database commands.
func LoadKeycloak() (keycloak.Config, error) {
	cfg := keycloak.Config{
		URL:          os.Getenv("KEYCLOAK_URL"),
		ClientID:     os.Getenv("KEYCLOAK_CLIENT_ID"),
		ClientSecret: os.Getenv("KEYCLOAK_CLIENT_SECRET"),
	}
	if cfg.ClientID == "" {
		cfg.ClientID = "platform-provisioner"
	}
	return cfg, cfg.Validate()
}

// LoadPartnerSetup loads environment-owned application settings for realm setup.
func LoadPartnerSetup() partners.SetupConfig {
	return partners.SetupConfig{ConsoleURL: os.Getenv("CONSOLE_URL"), ConsoleSecret: os.Getenv("CONSOLE_CLIENT_SECRET")}
}
