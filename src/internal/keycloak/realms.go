package keycloak

import (
	"context"
	"errors"
	"net/http"
)

// Realm contains the settings Platform owns. Pointer fields distinguish an
// explicit false/zero from an omitted setting on updates.
type Realm struct {
	ID                    string            `json:"id,omitempty"`
	Name                  string            `json:"realm,omitempty"`
	Enabled               *bool             `json:"enabled,omitempty"`
	OrganizationsEnabled  *bool             `json:"organizationsEnabled,omitempty"`
	RegistrationAllowed   *bool             `json:"registrationAllowed,omitempty"`
	ResetPasswordAllowed  *bool             `json:"resetPasswordAllowed,omitempty"`
	LoginWithEmail        *bool             `json:"loginWithEmailAllowed,omitempty"`
	BruteForceProtected   *bool             `json:"bruteForceProtected,omitempty"`
	SSLRequired           string            `json:"sslRequired,omitempty"`
	AccessTokenLifespan   *int              `json:"accessTokenLifespan,omitempty"`
	SSOSessionIdleTimeout *int              `json:"ssoSessionIdleTimeout,omitempty"`
	SSOSessionMaxLifespan *int              `json:"ssoSessionMaxLifespan,omitempty"`
	Attributes            map[string]string `json:"attributes,omitempty"`
}

// GetRealm requires a full representation, rather than Keycloak's limited
// metadata response that can be returned without view-realm permission.
func (c *Client) GetRealm(ctx context.Context, name string) (*Realm, error) {
	var realm Realm
	if _, err := c.do(ctx, http.MethodGet, []string{name}, nil, nil, &realm); err != nil {
		return nil, err
	}
	if realm.ID == "" {
		return nil, &APIError{StatusCode: http.StatusForbidden}
	}
	if realm.Name != name {
		return nil, errors.New("keycloak returned a different realm")
	}
	return &realm, nil
}

// CreateRealm creates a realm. Conflicts are returned to the caller for ownership
// verification; an unrelated realm must never be silently adopted.
func (c *Client) CreateRealm(ctx context.Context, realm Realm) error {
	if realm.Name == "" || realm.ID != "" {
		return errors.New("new realm requires a name and no existing internal ID")
	}
	if _, err := c.endpoint([]string{realm.Name}, nil); err != nil {
		return err
	}
	if _, err := c.do(ctx, http.MethodPost, nil, nil, realm, nil); err != nil {
		return err
	}
	// create-realm grants access to the new realm. The old token does not yet
	// contain those roles, so the next request must obtain a fresh token.
	c.epoch.Add(1)
	return nil
}

// UpdateRealm changes only the supplied settings. Renaming a realm changes its
// issuer, so it is deliberately excluded from this operation.
func (c *Client) UpdateRealm(ctx context.Context, name string, settings Realm) error {
	if settings.Name != "" && settings.Name != name {
		return errors.New("realm rename is not supported")
	}
	settings.ID, settings.Name = "", ""
	_, err := c.do(ctx, http.MethodPut, []string{name}, nil, settings, nil)
	return err
}

// DeleteRealm deletes a realm. Partner retirement authorization belongs to the caller.
func (c *Client) DeleteRealm(ctx context.Context, name string) error {
	_, err := c.do(ctx, http.MethodDelete, []string{name}, nil, nil, nil)
	return err
}
