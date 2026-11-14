package keycloak

import (
	"context"
	"errors"
	"net/http"
	"net/url"
)

// Application is a Keycloak client registration. Pointer fields preserve omitted
// settings during updates, including explicit empty redirect/origin lists.
type Application struct {
	ID                        string            `json:"id,omitempty"`
	ClientAuthenticatorType   string            `json:"clientAuthenticatorType,omitempty"`
	ClientID                  string            `json:"clientId,omitempty"`
	Protocol                  string            `json:"protocol,omitempty"`
	Secret                    string            `json:"secret,omitempty"`
	Enabled                   *bool             `json:"enabled,omitempty"`
	PublicClient              *bool             `json:"publicClient,omitempty"`
	StandardFlowEnabled       *bool             `json:"standardFlowEnabled,omitempty"`
	ImplicitFlowEnabled       *bool             `json:"implicitFlowEnabled,omitempty"`
	DirectAccessGrantsEnabled *bool             `json:"directAccessGrantsEnabled,omitempty"`
	ServiceAccountsEnabled    *bool             `json:"serviceAccountsEnabled,omitempty"`
	FullScopeAllowed          *bool             `json:"fullScopeAllowed,omitempty"`
	DefaultClientScopes       *[]string         `json:"defaultClientScopes,omitempty"`
	OptionalClientScopes      *[]string         `json:"optionalClientScopes,omitempty"`
	RedirectURIs              *[]string         `json:"redirectUris,omitempty"`
	WebOrigins                *[]string         `json:"webOrigins,omitempty"`
	Attributes                map[string]string `json:"attributes,omitempty"`
	ProtocolMappers           *[]Mapper         `json:"protocolMappers,omitempty"`
}

// Mapper is an OIDC protocol mapper. Secrets must not be put in mapper configuration.
type Mapper struct {
	ID             string            `json:"id,omitempty"`
	Name           string            `json:"name"`
	Protocol       string            `json:"protocol"`
	ProtocolMapper string            `json:"protocolMapper"`
	Config         map[string]string `json:"config"`
}

// Role is a client role, such as admin, operator or viewer.
type Role struct {
	Composite   bool   `json:"composite,omitempty"`
	ID          string `json:"id,omitempty"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// FindApplication looks up an exact client ID. Subsequent operations use the
// returned internal ID, which is different from the public client ID.
func (c *Client) FindApplication(ctx context.Context, realm, clientID string) (*Application, error) {
	if clientID == "" {
		return nil, errors.New("client ID is required")
	}
	var apps []Application
	if _, err := c.do(ctx, http.MethodGet, []string{realm, "clients"}, url.Values{"clientId": {clientID}}, nil, &apps); err != nil {
		return nil, err
	}
	if len(apps) != 1 || apps[0].ClientID != clientID || apps[0].ID == "" {
		if len(apps) == 0 {
			return nil, &APIError{StatusCode: http.StatusNotFound}
		}
		return nil, errors.New("keycloak client lookup returned an unexpected result")
	}
	return &apps[0], nil
}

// CreateApplication creates a client registration and returns its internal ID.
func (c *Client) CreateApplication(ctx context.Context, realm string, app Application) (string, error) {
	if app.ClientID == "" || app.ID != "" {
		return "", errors.New("new application requires a client ID and no existing internal ID")
	}
	if app.ProtocolMappers != nil {
		for _, mapper := range *app.ProtocolMappers {
			if mapper.ID != "" {
				return "", errors.New("new protocol mapper must not supply an existing internal ID")
			}
		}
	}
	headers, err := c.do(ctx, http.MethodPost, []string{realm, "clients"}, nil, app, nil)
	if err != nil {
		return "", err
	}
	return createdID(headers)
}

// UpdateApplication changes supplied settings, including secret rotation.
// Existing client-scope assignments use Keycloak's dedicated scope endpoints.
func (c *Client) UpdateApplication(ctx context.Context, realm, id string, settings Application) error {
	if settings.DefaultClientScopes != nil || settings.OptionalClientScopes != nil {
		return errors.New("client-scope assignments cannot be changed through the application update endpoint")
	}
	settings.ID = ""
	_, err := c.do(ctx, http.MethodPut, []string{realm, "clients", id}, nil, settings, nil)
	return err
}

// CreateClientRole creates an application role, not a Keycloak administration role.
func (c *Client) CreateClientRole(ctx context.Context, realm, clientID string, role Role) error {
	if role.Name == "" || role.ID != "" {
		return errors.New("new client role requires a name and no existing internal ID")
	}
	_, err := c.do(ctx, http.MethodPost, []string{realm, "clients", clientID, "roles"}, nil, role, nil)
	return err
}

// ClientRoles reads an application's role definitions.
func (c *Client) ClientRoles(ctx context.Context, realm, clientID string) ([]Role, error) {
	var roles []Role
	_, err := c.do(ctx, http.MethodGet, []string{realm, "clients", clientID, "roles"}, nil, nil, &roles)
	return roles, err
}

// AllowClientRoles permits the login client's tokens to contain the specified
// resource-client roles. It does not assign any role to a person.
func (c *Client) AllowClientRoles(ctx context.Context, realm, loginClientID, resourceClientID string, roles []Role) error {
	_, err := c.do(ctx, http.MethodPost, []string{realm, "clients", loginClientID, "scope-mappings", "clients", resourceClientID}, nil, roles, nil)
	return err
}
