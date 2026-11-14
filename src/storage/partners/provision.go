package partners

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"

	"github.com/mutex-ai/mutexai-browser-platform/src/internal/keycloak"
	"github.com/mutex-ai/mutexai-browser-platform/src/storage/instances"
)

var (
	ErrOwnership     = errors.New("keycloak resource ownership does not match this setup")
	ErrConfiguration = errors.New("existing keycloak configuration does not match this setup")
)

// Provisioner registers identity resources using the assigned instance's addresses.
type Provisioner struct{ kc *keycloak.Client }

// NewProvisioner shares the process's Admin API client.
func NewProvisioner(kc *keycloak.Client) *Provisioner { return &Provisioner{kc: kc} }

func apiStatus(err error, status int) bool {
	var e *keycloak.APIError
	return errors.As(err, &e) && e.StatusCode == status
}
func ownership(job Job, purpose string) map[string]string {
	return map[string]string{"mutex.setup_id": job.SetupID, "mutex.partner_id": strconv.Itoa(job.PartnerID), "mutex.purpose": purpose}
}
func owns(actual, expected map[string]string) bool {
	for k, v := range expected {
		if actual[k] != v {
			return false
		}
	}
	return true
}

// Provision can be repeated after partial failure. Existing resources are verified
// against their saved setup identity; retries do not overwrite unrelated objects.
func (p *Provisioner) Provision(ctx context.Context, job Job, partner Partner) (IdentityIDs, error) {
	if partner.PartnerID != job.PartnerID || !partner.Enabled {
		return IdentityIDs{}, ErrOwnership
	}
	if partner.InstanceID < 1000 || partner.InstanceID > 9999 || partner.ConsoleClientID != instances.ConsoleClientID(partner.InstanceID) {
		return IdentityIDs{}, ErrInstanceUnavailable
	}
	origin, err := instances.Origin(partner.ConsoleURL, true)
	if err != nil {
		return IdentityIDs{}, ErrConfiguration
	}
	console, sso := strconv.Itoa(job.PartnerID), strconv.Itoa(job.PartnerID)+"-sso"
	cr, err := p.realm(ctx, console, false, ownership(job, "console"))
	if err != nil {
		return IdentityIDs{}, err
	}
	sr, err := p.realm(ctx, sso, true, ownership(job, "sso"))
	if err != nil {
		return IdentityIDs{}, err
	}
	resource := keycloak.Application{ClientID: "platform-api", Protocol: "openid-connect", Enabled: new(true), PublicClient: new(false), StandardFlowEnabled: new(false), ImplicitFlowEnabled: new(false), DirectAccessGrantsEnabled: new(false), ServiceAccountsEnabled: new(false), FullScopeAllowed: new(false), Attributes: ownership(job, "platform-api")}
	api, err := p.application(ctx, console, resource)
	if err != nil {
		return IdentityIDs{}, err
	}
	roleNames := []string{"admin", "operator", "viewer"}
	roles, err := p.kc.ClientRoles(ctx, console, api.ID)
	if err != nil {
		return IdentityIDs{}, err
	}
	for _, role := range roles {
		if !slices.Contains(roleNames, role.Name) || role.Composite {
			return IdentityIDs{}, ErrConfiguration
		}
	}
	for _, name := range roleNames {
		found := false
		for _, role := range roles {
			if role.Name == name {
				found = true
			}
		}
		if !found {
			if err := p.kc.CreateClientRole(ctx, console, api.ID, keycloak.Role{Name: name}); err != nil && !apiStatus(err, http.StatusConflict) {
				return IdentityIDs{}, err
			}
		}
	}
	roles, err = p.kc.ClientRoles(ctx, console, api.ID)
	if err != nil {
		return IdentityIDs{}, err
	}
	if len(roles) != 3 {
		return IdentityIDs{}, ErrConfiguration
	}
	for _, role := range roles {
		if !slices.Contains(roleNames, role.Name) || role.Composite {
			return IdentityIDs{}, ErrConfiguration
		}
	}
	attrs := ownership(job, partner.ConsoleClientID)
	attrs["mutex.instance_id"] = strconv.Itoa(partner.InstanceID)
	attrs["pkce.code.challenge.method"] = "S256"
	attrs["use.jwks.url"] = "true"
	attrs["jwks.url"] = instances.ConsoleJWKSURL(origin)
	attrs["token.endpoint.auth.signing.alg"] = "RS256"
	attrs["token.endpoint.auth.signing.max.exp"] = "60"
	attrs["post.logout.redirect.uris"] = origin + "/"
	login := keycloak.Application{ClientID: partner.ConsoleClientID, Protocol: "openid-connect", ClientAuthenticatorType: "client-jwt", Enabled: new(true), PublicClient: new(false), StandardFlowEnabled: new(true), ImplicitFlowEnabled: new(false), DirectAccessGrantsEnabled: new(false), ServiceAccountsEnabled: new(false), FullScopeAllowed: new(false), Attributes: attrs,
		DefaultClientScopes: new([]string{"profile", "email", "roles"}), OptionalClientScopes: new([]string{}),
		RedirectURIs: new([]string{origin + "/api/auth/callback/keycloak-" + console}), WebOrigins: new([]string{origin}),
		ProtocolMappers: new([]keycloak.Mapper{{Name: "platform-api-audience", Protocol: "openid-connect", ProtocolMapper: "oidc-audience-mapper", Config: map[string]string{"included.client.audience": "platform-api", "access.token.claim": "true", "id.token.claim": "false"}}})}
	ui, err := p.application(ctx, console, login)
	if err != nil {
		return IdentityIDs{}, err
	}
	if err := p.kc.AllowClientRoles(ctx, console, ui.ID, api.ID, roles); err != nil {
		return IdentityIDs{}, err
	}
	extAttrs := ownership(job, "browser-extension")
	extAttrs["pkce.code.challenge.method"] = "S256"
	_, err = p.application(ctx, sso, keycloak.Application{ClientID: "browser-extension", Protocol: "openid-connect", Enabled: new(true), PublicClient: new(true), StandardFlowEnabled: new(true), ImplicitFlowEnabled: new(false), DirectAccessGrantsEnabled: new(false), ServiceAccountsEnabled: new(false), FullScopeAllowed: new(false), Attributes: extAttrs, RedirectURIs: new([]string{"https://cb.mutex.ai/ext/oauth/cb"})})
	if err != nil {
		return IdentityIDs{}, err
	}
	scopes, err := p.kc.ClientScopes(ctx, sso)
	if err != nil {
		return IdentityIDs{}, err
	}
	configured := false
	for _, scope := range scopes {
		if scope.Name != "organization" {
			continue
		}
		mappers, err := p.kc.ScopeMappers(ctx, sso, scope.ID)
		if err != nil {
			return IdentityIDs{}, err
		}
		for _, mapper := range mappers {
			if mapper.ProtocolMapper != "oidc-organization-membership-mapper" {
				continue
			}
			if mapper.Config == nil {
				return IdentityIDs{}, ErrConfiguration
			}
			mapper.Config["addOrganizationId"] = "true"
			mapper.Config["id.token.claim"] = "true"
			mapper.Config["access.token.claim"] = "true"
			if err := p.kc.UpdateScopeMapper(ctx, sso, scope.ID, mapper); err != nil {
				return IdentityIDs{}, err
			}
			configured = true
		}
	}
	if !configured {
		return IdentityIDs{}, ErrConfiguration
	}
	org, err := p.organization(ctx, sso, job, partner.ClientName)
	if err != nil {
		return IdentityIDs{}, err
	}
	if err := p.kc.UpdateRealm(ctx, console, keycloak.Realm{Enabled: new(true)}); err != nil {
		return IdentityIDs{}, err
	}
	return IdentityIDs{ConsoleRealmID: cr.ID, SSORealmID: sr.ID, OrganizationID: org.ID}, nil
}

func (p *Provisioner) realm(ctx context.Context, name string, sso bool, owner map[string]string) (*keycloak.Realm, error) {
	r, err := p.kc.GetRealm(ctx, name)
	if apiStatus(err, http.StatusNotFound) {
		err = p.kc.CreateRealm(ctx, keycloak.Realm{Name: name, Enabled: new(false), OrganizationsEnabled: new(sso), RegistrationAllowed: new(false), ResetPasswordAllowed: new(false), LoginWithEmail: new(true), BruteForceProtected: new(true), SSLRequired: "external", PasswordPolicy: "length(15)", AccessTokenLifespan: new(300), SSOSessionIdleTimeout: new(1800), SSOSessionMaxLifespan: new(28800), Attributes: owner})
		if err != nil && !apiStatus(err, http.StatusConflict) {
			return nil, err
		}
		r, err = p.kc.GetRealm(ctx, name)
	}
	if err != nil {
		return nil, err
	}
	if !owns(r.Attributes, owner) {
		return nil, ErrOwnership
	}
	if r.OrganizationsEnabled == nil || *r.OrganizationsEnabled != sso || r.RegistrationAllowed == nil || *r.RegistrationAllowed || r.SSLRequired != "external" || r.PasswordPolicy != "length(15)" {
		return nil, ErrConfiguration
	}
	if r.ResetPasswordAllowed == nil || *r.ResetPasswordAllowed ||
		r.LoginWithEmail == nil || !*r.LoginWithEmail ||
		r.BruteForceProtected == nil || !*r.BruteForceProtected {
		return nil, ErrConfiguration
	}
	if r.AccessTokenLifespan == nil || *r.AccessTokenLifespan != 300 ||
		r.SSOSessionIdleTimeout == nil || *r.SSOSessionIdleTimeout != 1800 ||
		r.SSOSessionMaxLifespan == nil || *r.SSOSessionMaxLifespan != 28800 {
		return nil, ErrConfiguration
	}
	if sso && (r.Enabled == nil || *r.Enabled) {
		return nil, ErrConfiguration
	}
	return r, nil
}

func (p *Provisioner) application(ctx context.Context, realm string, want keycloak.Application) (*keycloak.Application, error) {
	app, err := p.kc.FindApplication(ctx, realm, want.ClientID)
	if apiStatus(err, http.StatusNotFound) {
		if _, err = p.kc.CreateApplication(ctx, realm, want); err != nil && !apiStatus(err, http.StatusConflict) {
			return nil, err
		}
		app, err = p.kc.FindApplication(ctx, realm, want.ClientID)
	}
	if err != nil {
		return nil, err
	}
	owner := map[string]string{}
	for _, name := range []string{"mutex.setup_id", "mutex.partner_id", "mutex.purpose", "mutex.instance_id"} {
		if value, ok := want.Attributes[name]; ok {
			owner[name] = value
		}
	}
	if !owns(app.Attributes, owner) {
		return nil, ErrOwnership
	}
	if !owns(app.Attributes, want.Attributes) || app.Protocol != want.Protocol || (want.ClientAuthenticatorType != "" && app.ClientAuthenticatorType != want.ClientAuthenticatorType) {
		return nil, ErrConfiguration
	}
	for _, pair := range [][2]*bool{{app.Enabled, want.Enabled}, {app.PublicClient, want.PublicClient}, {app.StandardFlowEnabled, want.StandardFlowEnabled}, {app.ImplicitFlowEnabled, want.ImplicitFlowEnabled}, {app.DirectAccessGrantsEnabled, want.DirectAccessGrantsEnabled}, {app.ServiceAccountsEnabled, want.ServiceAccountsEnabled}, {app.FullScopeAllowed, want.FullScopeAllowed}} {
		if pair[0] == nil || pair[1] == nil || *pair[0] != *pair[1] {
			return nil, ErrConfiguration
		}
	}
	for _, scopes := range [][2]*[]string{{app.DefaultClientScopes, want.DefaultClientScopes}, {app.OptionalClientScopes, want.OptionalClientScopes}} {
		if scopes[1] != nil && (scopes[0] == nil || !sameStrings(*scopes[0], *scopes[1])) {
			return nil, ErrConfiguration
		}
	}
	if want.RedirectURIs != nil && (app.RedirectURIs == nil || !slices.Equal(*app.RedirectURIs, *want.RedirectURIs)) {
		return nil, ErrConfiguration
	}
	if want.WebOrigins != nil && (app.WebOrigins == nil || !slices.Equal(*app.WebOrigins, *want.WebOrigins)) {
		return nil, ErrConfiguration
	}
	if want.ProtocolMappers != nil {
		if app.ProtocolMappers == nil || len(*app.ProtocolMappers) != len(*want.ProtocolMappers) {
			return nil, ErrConfiguration
		}
		for i, mapper := range *want.ProtocolMappers {
			actual := (*app.ProtocolMappers)[i]
			if actual.Name != mapper.Name || actual.ProtocolMapper != mapper.ProtocolMapper || !owns(actual.Config, mapper.Config) {
				return nil, ErrConfiguration
			}
		}
	}
	return app, nil
}

func (p *Provisioner) organization(ctx context.Context, realm string, job Job, name string) (*keycloak.Organization, error) {
	owner := map[string][]string{"mutex.setup_id": {job.SetupID}, "mutex.partner_id": {strconv.Itoa(job.PartnerID)}, "mutex.client_id": {"1000"}}
	find := func() (*keycloak.Organization, error) {
		for first := 0; ; first += 100 {
			page, err := p.kc.Organizations(ctx, realm, first, 100)
			if err != nil {
				return nil, err
			}
			for _, org := range page {
				if org.Alias == "1000" {
					return p.kc.GetOrganization(ctx, realm, org.ID)
				}
			}
			if len(page) < 100 {
				return nil, nil
			}
		}
	}
	org, err := find()
	if err != nil {
		return nil, err
	}
	if org == nil {
		_, err = p.kc.CreateOrganization(ctx, realm, keycloak.Organization{Name: name + " (1000)", Alias: "1000", Enabled: new(false), Attributes: owner})
		if err != nil && !apiStatus(err, http.StatusConflict) {
			return nil, err
		}
		org, err = find()
		if err != nil {
			return nil, err
		}
	}
	if org == nil {
		return nil, ErrConfiguration
	}
	for key, value := range owner {
		if !slices.Equal(org.Attributes[key], value) {
			return nil, ErrOwnership
		}
	}
	if org.Enabled == nil || *org.Enabled {
		return nil, ErrConfiguration
	}
	return org, nil
}

func sameStrings(actual, expected []string) bool {
	a, b := slices.Clone(actual), slices.Clone(expected)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}
