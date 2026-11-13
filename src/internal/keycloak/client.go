// Package keycloak provides the Admin API operations used by Platform setup.
package keycloak

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

const maxResponseBytes = 1 << 20

// Config identifies Platform's service-account client in the master realm.
type Config struct {
	URL          string
	ClientID     string
	ClientSecret string
}

// Validate checks configuration without returning credentials in errors.
func (cfg Config) Validate() error {
	u, err := url.Parse(cfg.URL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return errors.New("KEYCLOAK_URL must be an HTTP(S) base URL without credentials, query or fragment")
	}
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return errors.New("KEYCLOAK_CLIENT_ID and KEYCLOAK_CLIENT_SECRET are required")
	}
	return nil
}

// Client is safe for concurrent use. It owns one HTTP connection pool and token
// cache; it starts no background refresh goroutine.
type Client struct {
	base       *url.URL
	http       *http.Client
	id         string
	secret     string
	tokenGuard chan struct{}
	token      string
	expires    time.Time
	tokenEpoch uint64
	epoch      atomic.Uint64
}

// New creates a client without making network calls. Requests have a ten-second
// transport deadline as well as the caller's context deadline.
func New(cfg Config) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	base, _ := url.Parse(cfg.URL) // Validate checked the URL.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 10
	transport.MaxIdleConnsPerHost = 10
	transport.MaxConnsPerHost = 10
	return &Client{
		base: base,
		http: &http.Client{
			Transport: transport,
			Timeout:   10 * time.Second,
			// Credentials must never follow a redirect to another endpoint.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		id: cfg.ClientID, secret: cfg.ClientSecret,
		tokenGuard: make(chan struct{}, 1),
	}, nil
}

// CloseIdleConnections releases keep-alive connections after callers stop using
// the client. In-flight requests are controlled by their contexts.
func (c *Client) CloseIdleConnections() { c.http.CloseIdleConnections() }

// APIError reports a rejected request without exposing the response body.
// Callers can use errors.As to distinguish conflicts, missing records and denials.
type APIError struct {
	StatusCode int
}

func (e *APIError) Error() string { return fmt.Sprintf("keycloak returned HTTP %d", e.StatusCode) }

func (c *Client) endpoint(parts []string, query url.Values) (*url.URL, error) {
	for _, part := range parts {
		if !validIdentifier(part) {
			return nil, errors.New("invalid Keycloak resource identifier")
		}
	}
	u := c.base.JoinPath(parts...)
	u.RawQuery = query.Encode()
	return u, nil
}

func (c *Client) accessToken(ctx context.Context, rejected string) (string, error) {
	// Waiting for another caller's refresh must also respect cancellation.
	select {
	case c.tokenGuard <- struct{}{}:
		defer func() { <-c.tokenGuard }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	epoch := c.epoch.Load()
	if c.token != "" && c.token != rejected && c.tokenEpoch == epoch && time.Now().Before(c.expires) {
		return c.token, nil
	}
	u, err := c.endpoint([]string{"realms", "master", "protocol", "openid-connect", "token"}, nil)
	if err != nil {
		return "", err
	}
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {c.id}, "client_secret": {c.secret}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("build Keycloak token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	started := time.Now()
	var body struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if _, err := c.send(req, &body); err != nil {
		return "", fmt.Errorf("authenticate Keycloak service account: %w", err)
	}
	if body.AccessToken == "" || !strings.EqualFold(body.TokenType, "Bearer") || body.ExpiresIn <= 0 || body.ExpiresIn > 86400 {
		return "", errors.New("invalid Keycloak token response")
	}
	lifetime := time.Duration(body.ExpiresIn) * time.Second
	expires := started.Add(lifetime - min(10*time.Second, lifetime/10))
	if !time.Now().Before(expires) {
		return "", errors.New("keycloak token expired during authentication")
	}
	c.token, c.expires, c.tokenEpoch = body.AccessToken, expires, epoch
	return c.token, nil
}

func (c *Client) do(ctx context.Context, method string, parts []string, query url.Values, input, output any) (http.Header, error) {
	u, err := c.endpoint(append([]string{"admin", "realms"}, parts...), query)
	if err != nil {
		return nil, err
	}
	var data []byte
	if input != nil {
		data, err = json.Marshal(input)
		if err != nil {
			return nil, errors.New("invalid Keycloak request body")
		}
	}
	rejected := ""
	for range 2 {
		token, err := c.accessToken(ctx, rejected)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("build Keycloak admin request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")
		if input != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		headers, err := c.send(req, output)
		var status *APIError
		if errors.As(err, &status) && status.StatusCode == http.StatusUnauthorized && rejected == "" {
			rejected = token
			continue
		}
		return headers, err
	}
	return nil, &APIError{StatusCode: http.StatusUnauthorized}
}

func (c *Client) send(req *http.Request, output any) (http.Header, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("keycloak request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Do not echo Keycloak bodies: they can contain submitted secrets.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, &APIError{StatusCode: resp.StatusCode}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read Keycloak response: %w", err)
	}
	if len(data) > maxResponseBytes {
		return nil, errors.New("keycloak response exceeds size limit")
	}
	if output != nil {
		if err := json.Unmarshal(data, output); err != nil {
			return nil, errors.New("invalid Keycloak JSON response")
		}
	}
	return resp.Header, nil
}

func createdID(headers http.Header) (string, error) {
	u, err := url.Parse(headers.Get("Location"))
	if err != nil || u.Path == "" {
		return "", errors.New("keycloak creation response is missing a resource ID")
	}
	id := u.Path[strings.LastIndex(u.Path, "/")+1:]
	// Validate without ever requesting the Location URL.
	if !validIdentifier(id) {
		return "", errors.New("invalid Keycloak creation resource ID")
	}
	return id, nil
}

func validIdentifier(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}
