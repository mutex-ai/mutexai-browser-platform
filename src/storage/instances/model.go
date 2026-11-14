// Package instances owns registered deployment addresses.
package instances

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	ErrNotFound        = errors.New("instance not found")
	ErrRequestConflict = errors.New("idempotency key was used for a different instance request")
	ErrConsoleConflict = errors.New("console URL already belongs to another instance")
	ErrCapacity        = errors.New("instance ID range is exhausted")
)

// CreateInput registers an existing instance; it does not deploy infrastructure.
type CreateInput struct {
	Name             string `json:"name"`
	ConsoleURL       string `json:"console_url"`
	BackendURL       string `json:"backend_url"`
	AcceptingClients *bool  `json:"accepting_clients,omitempty"`
}

// Normalize validates the instance name and immutable addresses.
func (in *CreateInput) Normalize() error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || utf8.RuneCountInString(in.Name) > 200 || strings.ContainsFunc(in.Name, unicode.IsControl) {
		return errors.New("name must contain 1 to 200 characters without control characters")
	}
	var err error
	in.ConsoleURL, err = Origin(in.ConsoleURL, true)
	if err != nil {
		return errors.New("console_url must be an HTTPS origin, or HTTP loopback origin for local development")
	}
	in.BackendURL, err = Origin(in.BackendURL, false)
	if err != nil {
		return errors.New("backend_url must be an approved HTTP(S) origin without credentials, query or fragment")
	}
	if in.AcceptingClients == nil {
		in.AcceptingClients = new(true)
	}
	return nil
}

// Origin validates and canonicalizes an address before it enters the directory.
// Backend HTTP supports the private local Kubernetes service; production uses TLS.
func Origin(raw string, console bool) (string, error) {
	u, err := url.ParseRequestURI(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") || strings.ContainsAny(u.Host, "*%\\") {
		return "", errors.New("invalid instance origin")
	}
	ip := net.ParseIP(u.Hostname())
	local := strings.EqualFold(u.Hostname(), "localhost") || ip != nil && ip.IsLoopback()
	if u.Scheme != "https" && (u.Scheme != "http" || console && !local) {
		return "", errors.New("invalid instance origin transport")
	}
	if strings.HasPrefix(u.Host, "[") && ip == nil || strings.HasSuffix(u.Host, ":") {
		return "", errors.New("invalid instance host or port")
	}
	if port := u.Port(); port != "" {
		number, err := strconv.ParseUint(port, 10, 16)
		if err != nil || number == 0 {
			return "", errors.New("invalid instance port")
		}
		u.Host = net.JoinHostPort(u.Hostname(), strconv.FormatUint(number, 10))
		if u.Scheme == "https" && number == 443 || u.Scheme == "http" && number == 80 {
			u.Host = strings.TrimSuffix(u.Host, ":"+u.Port())
		}
	}
	u.Host = strings.ToLower(u.Host)
	u.Path, u.RawPath = "", ""
	return u.String(), nil
}

// Instance is an operator-managed directory record. A console client ID is stable
// per instance and repeats in each partner realm served by this instance.
type Instance struct {
	InstanceID       int       `json:"instance_id"`
	Name             string    `json:"name"`
	ConsoleURL       string    `json:"console_url"`
	BackendURL       string    `json:"backend_url"`
	ConsoleClientID  string    `json:"console_client_id"`
	ConsoleJWKSURL   string    `json:"console_jwks_url"`
	AcceptingClients bool      `json:"accepting_clients"`
	CreatedTSUTC     time.Time `json:"created_ts_utc"`
}

// ConsoleClientID identifies one UI instance within each partner realm it serves.
func ConsoleClientID(id int) string { return fmt.Sprintf("platform-ui-%d", id) }

// ConsoleJWKSURL is the public-key endpoint on the registered UI origin.
func ConsoleJWKSURL(origin string) string { return origin + "/.well-known/jwks.json" }
