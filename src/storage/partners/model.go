// Package partners owns partner creation and durable identity setup.
package partners

import (
	"errors"
	"net"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	ErrNotFound            = errors.New("partner not found")
	ErrRequestConflict     = errors.New("idempotency key was used for a different request")
	ErrDomainConflict      = errors.New("a console domain already belongs to another partner")
	ErrCapacity            = errors.New("partner ID range is exhausted")
	ErrInstanceUnavailable = errors.New("instance must exist and accept new clients")
	ErrLeaseLost           = errors.New("setup lease is no longer owned")
)

// CreateInput contains operator-approved names and console domains. Credentials
// and infrastructure addresses are deliberately absent from the request.
type CreateInput struct {
	InstanceID     int      `json:"instance_id"`
	Name           string   `json:"name"`
	ClientName     string   `json:"client_name"`
	SiteName       string   `json:"site_name"`
	ConsoleDomains []string `json:"console_domains"`
}

// Normalize validates and canonicalizes a creation request before hashing it.
func (in *CreateInput) Normalize() error {
	if in.InstanceID < 1000 || in.InstanceID > 9999 {
		return errors.New("instance_id must identify a registered instance from 1000 through 9999")
	}
	for _, name := range []*string{&in.Name, &in.ClientName, &in.SiteName} {
		*name = strings.TrimSpace(*name)
		if *name == "" || utf8.RuneCountInString(*name) > 200 || strings.ContainsFunc(*name, unicode.IsControl) {
			return errors.New("name, client_name and site_name must contain 1 to 200 characters without control characters")
		}
	}
	if len(in.ConsoleDomains) == 0 || len(in.ConsoleDomains) > 100 {
		return errors.New("console_domains must contain 1 to 100 domains")
	}
	in.ConsoleDomains = slices.Clone(in.ConsoleDomains)
	for i, domain := range in.ConsoleDomains {
		domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
		if len(domain) > 253 || !strings.Contains(domain, ".") || net.ParseIP(domain) != nil {
			return errors.New("console_domains must contain complete ASCII domain names, without URLs or email addresses")
		}
		for _, label := range strings.Split(domain, ".") {
			if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return errors.New("invalid console domain; use punycode for international domain names")
			}
			for _, c := range label {
				valid := c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-'
				if !valid {
					return errors.New("invalid console domain; use punycode for international domain names")
				}
			}
		}
		in.ConsoleDomains[i] = domain
	}
	slices.Sort(in.ConsoleDomains)
	in.ConsoleDomains = slices.Compact(in.ConsoleDomains)
	return nil
}

// Partner is the creation/status view, including its initial client and site.
// The initial client owns its instance assignment; all its sites inherit it.
type Partner struct {
	InstanceID      int        `json:"instance_id,omitempty"`
	ConsoleURL      string     `json:"console_url,omitempty"`
	BackendURL      string     `json:"backend_url,omitempty"`
	ConsoleClientID string     `json:"console_client_id,omitempty"`
	ConsoleJWKSURL  string     `json:"console_jwks_url,omitempty"`
	PartnerID       int        `json:"partner_id"`
	Name            string     `json:"name"`
	Enabled         bool       `json:"enabled"`
	ClientID        int        `json:"client_id"`
	ClientName      string     `json:"client_name"`
	SiteID          int        `json:"site_id"`
	SiteName        string     `json:"site_name"`
	ConsoleDomains  []string   `json:"console_domains"`
	ConsoleRealm    string     `json:"console_realm"`
	SSORealm        string     `json:"sso_realm"`
	SetupStatus     string     `json:"setup_status"`
	SetupError      string     `json:"setup_error,omitempty"`
	RetryTSUTC      *time.Time `json:"retry_ts_utc,omitempty"`
	CreatedTSUTC    time.Time  `json:"created_ts_utc"`
}

// Job carries one fenced setup lease. Its setup ID also marks owned KC resources.
type Job struct {
	PartnerID  int
	SetupID    string
	LeaseToken string
	Attempts   int
}

// IdentityIDs contains the permanent Keycloak references saved at completion.
type IdentityIDs struct {
	ConsoleRealmID string
	SSORealmID     string
	OrganizationID string
}
