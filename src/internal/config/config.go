// Package config loads the service's environment settings.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"time"
)

// Config contains only settings used by the Platform service.
type Config struct {
	DatabaseURL string
	LogLevel    string
	Server      Server
}

// Server controls the private HTTP listener. Cluster TLS termination is external.
type Server struct {
	ListenAddr        string
	ReadTimeout       time.Duration
	ReadHeaderTimeout time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
}

// Load validates settings without including database credentials in errors.
func Load() (*Config, error) {
	for _, name := range []string{"POSTGRES_HOST", "POSTGRES_DATABASE", "POSTGRES_USERNAME", "POSTGRES_PASSWORD"} {
		if os.Getenv(name) == "" {
			return nil, fmt.Errorf("%s is required", name)
		}
	}
	port := os.Getenv("POSTGRES_PORT")
	if port == "" {
		port = "5432"
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return nil, errors.New("POSTGRES_PORT must be between 1 and 65535")
	}
	sslMode := os.Getenv("POSTGRES_SSLMODE")
	if sslMode == "" {
		sslMode = "verify-full"
	}
	switch sslMode {
	case "disable", "allow", "prefer", "require", "verify-ca", "verify-full":
	default:
		return nil, errors.New("invalid POSTGRES_SSLMODE")
	}
	u := url.URL{
		Scheme:   "postgres",
		Host:     net.JoinHostPort(os.Getenv("POSTGRES_HOST"), port),
		Path:     "/" + os.Getenv("POSTGRES_DATABASE"),
		User:     url.UserPassword(os.Getenv("POSTGRES_USERNAME"), os.Getenv("POSTGRES_PASSWORD")),
		RawQuery: url.Values{"sslmode": {sslMode}}.Encode(),
	}
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	_, listenPort, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, errors.New("LISTEN_ADDR must contain a host and port")
	}
	n, err = strconv.Atoi(listenPort)
	if err != nil || n < 1 || n > 65535 {
		return nil, errors.New("LISTEN_ADDR port must be between 1 and 65535")
	}
	level := os.Getenv("LOG_LEVEL")
	if level == "" {
		level = "info"
	}
	server := Server{ListenAddr: addr}
	for _, setting := range []struct {
		name        string
		seconds     int
		destination *time.Duration
	}{
		{"HTTP_READ_TIMEOUT_SEC", 30, &server.ReadTimeout},
		{"HTTP_READ_HEADER_TIMEOUT_SEC", 5, &server.ReadHeaderTimeout},
		{"HTTP_WRITE_TIMEOUT_SEC", 30, &server.WriteTimeout},
		{"HTTP_IDLE_TIMEOUT_SEC", 120, &server.IdleTimeout},
		{"HTTP_SHUTDOWN_TIMEOUT_SEC", 30, &server.ShutdownTimeout},
	} {
		seconds := setting.seconds
		if raw := os.Getenv(setting.name); raw != "" {
			seconds, err = strconv.Atoi(raw)
			if err != nil || seconds < 1 || seconds > 3600 {
				return nil, fmt.Errorf("%s must be between 1 and 3600 seconds", setting.name)
			}
		}
		*setting.destination = time.Duration(seconds) * time.Second
	}
	return &Config{
		DatabaseURL: u.String(),
		LogLevel:    level,
		Server:      server,
	}, nil
}
