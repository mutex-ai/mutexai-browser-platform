package config

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func setDatabaseEnv(t *testing.T) {
	t.Helper()
	for key, value := range map[string]string{
		"POSTGRES_HOST": "localhost", "POSTGRES_DATABASE": "platform",
		"POSTGRES_USERNAME": "platform", "POSTGRES_PASSWORD": "private-secret:@/?#%",
		"POSTGRES_PORT": "", "POSTGRES_SSLMODE": "", "LISTEN_ADDR": "", "LOG_LEVEL": "",
		"HTTP_READ_TIMEOUT_SEC": "", "HTTP_READ_HEADER_TIMEOUT_SEC": "",
		"HTTP_WRITE_TIMEOUT_SEC": "", "HTTP_IDLE_TIMEOUT_SEC": "", "HTTP_SHUTDOWN_TIMEOUT_SEC": "",
	} {
		t.Setenv(key, value)
	}
}

func TestServerTimeoutEnvironment(t *testing.T) {
	setDatabaseEnv(t)
	t.Setenv("HTTP_SHUTDOWN_TIMEOUT_SEC", "45")
	cfg, err := Load()
	if err != nil || cfg.Server.ShutdownTimeout != 45*time.Second {
		t.Fatal("shutdown timeout environment setting was not applied")
	}
	t.Setenv("HTTP_READ_TIMEOUT_SEC", "0")
	if _, err := Load(); err == nil {
		t.Fatal("zero read timeout must not silently disable the deadline")
	}
}

func TestDatabaseConnectionSettings(t *testing.T) {
	setDatabaseEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	password, _ := u.User.Password()
	if password != "private-secret:@/?#%" || u.User.Username() != "platform" || u.Host != "localhost:5432" || u.Path != "/platform" {
		t.Fatal("database credentials or address did not survive URL encoding")
	}
	if u.Query().Get("sslmode") != "verify-full" || cfg.Server.ListenAddr != "127.0.0.1:8080" {
		t.Fatal("expected verified database TLS and a loopback listener by default")
	}
	t.Setenv("POSTGRES_SSLMODE", "disable")
	cfg, err = Load()
	if err != nil || !strings.Contains(cfg.DatabaseURL, "sslmode=disable") {
		t.Fatal("explicit local TLS setting was not retained")
	}
}

func TestInvalidDatabaseSettings(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"POSTGRES_HOST", ""}, {"POSTGRES_DATABASE", ""},
		{"POSTGRES_USERNAME", ""}, {"POSTGRES_PASSWORD", ""},
		{"POSTGRES_PORT", "70000"}, {"POSTGRES_PORT", "invalid"},
		{"POSTGRES_SSLMODE", "private-secret"},
		{"LISTEN_ADDR", "localhost"}, {"LISTEN_ADDR", "localhost:0"},
		{"LISTEN_ADDR", "localhost:70000"}, {"LISTEN_ADDR", "localhost:http"},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			setDatabaseEnv(t)
			t.Setenv(tc.key, tc.value)
			_, err := Load()
			if err == nil {
				t.Fatal("invalid setting was accepted")
			}
			if strings.Contains(err.Error(), "private-secret") {
				t.Fatal("configuration error exposed a setting value")
			}
		})
	}
}
