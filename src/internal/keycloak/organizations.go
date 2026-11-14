package keycloak

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
)

// Organization represents one client within its partner's SSO realm. Alias is
// the client number; the generated ID is stored with the full ownership path.
type Organization struct {
	Attributes map[string][]string `json:"attributes,omitempty"`
	ID         string              `json:"id,omitempty"`
	Name       string              `json:"name"`
	Alias      string              `json:"alias"`
	Enabled    *bool               `json:"enabled,omitempty"`
}

// CreateOrganization creates a client organization and returns its Keycloak ID.
func (c *Client) CreateOrganization(ctx context.Context, realm string, org Organization) (string, error) {
	if org.Name == "" || org.Alias == "" || org.ID != "" {
		return "", errors.New("new organization requires a name, alias and no existing internal ID")
	}
	headers, err := c.do(ctx, http.MethodPost, []string{realm, "organizations"}, nil, org, nil)
	if err != nil {
		return "", err
	}
	return createdID(headers)
}

// GetOrganization reads an organization by its permanent Keycloak ID.
func (c *Client) GetOrganization(ctx context.Context, realm, id string) (*Organization, error) {
	var org Organization
	if _, err := c.do(ctx, http.MethodGet, []string{realm, "organizations", id}, nil, nil, &org); err != nil {
		return nil, err
	}
	if org.ID != id || org.Alias == "" {
		return nil, errors.New("invalid Keycloak organization response")
	}
	return &org, nil
}

// Organizations returns one bounded page. Callers must paginate when recovering
// an interrupted setup rather than assuming the first page contains every client.
func (c *Client) Organizations(ctx context.Context, realm string, first, pageSize int) ([]Organization, error) {
	if first < 0 || pageSize < 1 || pageSize > 1000 {
		return nil, errors.New("organization page requires first >= 0 and max between 1 and 1000")
	}
	query := url.Values{"first": {strconv.Itoa(first)}, "max": {strconv.Itoa(pageSize)}}
	var orgs []Organization
	_, err := c.do(ctx, http.MethodGet, []string{realm, "organizations"}, query, nil, &orgs)
	return orgs, err
}

// UpdateOrganization updates a client organization. Preserve its existing alias.
func (c *Client) UpdateOrganization(ctx context.Context, realm, id string, org Organization) error {
	org.ID = ""
	_, err := c.do(ctx, http.MethodPut, []string{realm, "organizations", id}, nil, org, nil)
	return err
}

// ClientScope describes a realm's built-in OIDC scope, including organization.
type ClientScope struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ClientScopes reads the realm's scopes so setup can find organization by name.
func (c *Client) ClientScopes(ctx context.Context, realm string) ([]ClientScope, error) {
	var scopes []ClientScope
	_, err := c.do(ctx, http.MethodGet, []string{realm, "client-scopes"}, nil, nil, &scopes)
	return scopes, err
}

// ScopeMappers reads the mappers attached to an OIDC scope.
func (c *Client) ScopeMappers(ctx context.Context, realm, scopeID string) ([]Mapper, error) {
	var mappers []Mapper
	_, err := c.do(ctx, http.MethodGet, []string{realm, "client-scopes", scopeID, "protocol-mappers", "models"}, nil, nil, &mappers)
	return mappers, err
}

// UpdateScopeMapper configures membership claims, including permanent organization IDs.
func (c *Client) UpdateScopeMapper(ctx context.Context, realm, scopeID string, mapper Mapper) error {
	_, err := c.do(ctx, http.MethodPut, []string{realm, "client-scopes", scopeID, "protocol-mappers", "models", mapper.ID}, nil, mapper, nil)
	return err
}
