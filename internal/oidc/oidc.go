// Package oidc implements OpenID Connect authorization code flow over the standard library, with
// claim checks and no signature verification. Per OIDC Core 1.0 §3.1.3.7 step 6, TLS server
// validation stands in for checking the token's signature when the ID token comes by direct
// communication between the client and the token endpoint.
package oidc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Errors for OIDC operations.
var (
	ErrInvalidIssuer       = errors.New("issuer in discovery document does not match configured issuer")
	ErrInvalidClaims       = errors.New("ID token claims are invalid")
	ErrTokenEndpointHTTP   = errors.New("token endpoint must be https (except loopback)")
	ErrIssuerHTTP          = errors.New("issuer must be https (except loopback)")
	ErrRedirectingEndpoint = errors.New("token endpoint redirected")
)

// Config configures an OIDC client.
type Config struct {
	// Issuer is the OIDC issuer URL.
	Issuer string

	// ClientID is the OAuth2 client ID.
	ClientID string

	// ClientSecret is the optional OAuth2 client secret. Empty for a public client.
	ClientSecret string

	// RedirectURL is where the provider sends the authorization response.
	RedirectURL string

	// CAFile is an optional PEM file of certificate authorities. Empty uses system roots.
	CAFile string
}

// Claims are the validated ID token claims.
type Claims struct {
	Issuer    string `json:"iss"`
	Subject   string `json:"sub"`
	Audience  any    `json:"aud"` // string or []string
	ExpiresAt int64  `json:"exp"`
	IssuedAt  int64  `json:"iat"`
	Nonce     string `json:"nonce,omitempty"`
	Email     string `json:"email,omitempty"`
}

// Client is an OIDC client.
type Client struct {
	cfg        Config
	httpClient *http.Client
	discovery  *discoveryDocument
}

type discoveryDocument struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
}

// NewClient creates an OIDC client and fetches the discovery document.
func NewClient(cfg Config) (*Client, error) {
	if err := checkHTTPS(cfg.Issuer, "issuer"); err != nil {
		return nil, err
	}

	rootCAs, err := loadRootCAs(cfg.CAFile)
	if err != nil {
		return nil, err
	}

	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs: rootCAs,
			},
			DisableKeepAlives: true,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Timeout: 30 * time.Second,
	}

	c := &Client{
		cfg:        cfg,
		httpClient: httpClient,
	}

	if err := c.fetchDiscovery(); err != nil {
		return nil, err
	}

	return c, nil
}

func loadRootCAs(caFile string) (*x509.CertPool, error) {
	if caFile == "" {
		return nil, nil
	}

	data, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("reading CA file %s: %w", caFile, err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		return nil, fmt.Errorf("no valid certificates in %s", caFile)
	}

	return pool, nil
}

func checkHTTPS(urlStr, name string) error {
	u, err := url.Parse(urlStr)
	if err != nil {
		return fmt.Errorf("parsing %s URL: %w", name, err)
	}

	if u.Scheme != "https" && !isLoopback(u.Host) {
		if name == "issuer" {
			return fmt.Errorf("%s: %w", urlStr, ErrIssuerHTTP)
		}

		return fmt.Errorf("%s: %w", urlStr, ErrTokenEndpointHTTP)
	}

	return nil
}

func isLoopback(host string) bool {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}

	if h == "localhost" {
		return true
	}

	ip := net.ParseIP(h)

	return ip != nil && ip.IsLoopback()
}

func (c *Client) fetchDiscovery() error {
	discoveryURL := strings.TrimSuffix(c.cfg.Issuer, "/") + "/.well-known/openid-configuration"

	resp, err := c.httpClient.Get(discoveryURL)
	if err != nil {
		return fmt.Errorf("fetching discovery document: %w", err)
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("discovery document: HTTP %d", resp.StatusCode)
	}

	var doc discoveryDocument
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return fmt.Errorf("decoding discovery document: %w", err)
	}

	if doc.Issuer != c.cfg.Issuer {
		return fmt.Errorf("discovery issuer %q != configured %q: %w", doc.Issuer, c.cfg.Issuer, ErrInvalidIssuer)
	}

	if err := checkHTTPS(doc.TokenEndpoint, "token endpoint"); err != nil {
		return err
	}

	c.discovery = &doc

	return nil
}

// AuthorizationURL returns the authorization URL with state, nonce, and PKCE verifier.
func (c *Client) AuthorizationURL(state, nonce, verifier string) string {
	challenge := pkceChallenge(verifier)

	params := url.Values{
		"response_type":         {"code"},
		"client_id":             {c.cfg.ClientID},
		"redirect_uri":          {c.cfg.RedirectURL},
		"scope":                 {"openid email"},
		"state":                 {state},
		"nonce":                 {nonce},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}

	return c.discovery.AuthorizationEndpoint + "?" + params.Encode()
}

// GenerateState generates a random state parameter.
func GenerateState() string {
	return randomString(32)
}

// GenerateNonce generates a random nonce.
func GenerateNonce() string {
	return randomString(32)
}

// GeneratePKCEVerifier generates a random PKCE verifier.
func GeneratePKCEVerifier() string {
	return randomString(43)
}

func randomString(length int) string {
	b := make([]byte, length)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}

	return base64.RawURLEncoding.EncodeToString(b)[:length]
}

func pkceChallenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))

	return base64.RawURLEncoding.EncodeToString(h[:])
}

// Exchange exchanges an authorization code for an ID token and validates its claims.
func (c *Client) Exchange(ctx context.Context, code, verifier, nonce string) (*Claims, error) {
	params := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {c.cfg.RedirectURL},
		"client_id":     {c.cfg.ClientID},
		"code_verifier": {verifier},
	}

	if c.cfg.ClientSecret != "" {
		params.Set("client_secret", c.cfg.ClientSecret)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.discovery.TokenEndpoint, strings.NewReader(params.Encode()))
	if err != nil {
		return nil, fmt.Errorf("building token request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request: %w", err)
	}

	defer resp.Body.Close()

	if resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusMovedPermanently {
		return nil, ErrRedirectingEndpoint
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)

		return nil, fmt.Errorf("token endpoint: HTTP %d: %s", resp.StatusCode, body)
	}

	var tokenResp struct {
		IDToken string `json:"id_token"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("decoding token response: %w", err)
	}

	claims, err := c.validateClaims(tokenResp.IDToken, nonce)
	if err != nil {
		return nil, err
	}

	return claims, nil
}

// claimLeeway tolerates clock skew between the broker and the OIDC provider.
const claimLeeway = 60 * time.Second

func (c *Client) validateClaims(idToken, expectedNonce string) (*Claims, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("ID token: not a JWT")
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decoding ID token payload: %w", err)
	}

	var claims Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("decoding ID token claims: %w", err)
	}

	if claims.Issuer != c.cfg.Issuer {
		return nil, fmt.Errorf("iss %q != %q: %w", claims.Issuer, c.cfg.Issuer, ErrInvalidClaims)
	}

	if !audienceMatches(claims.Audience, c.cfg.ClientID) {
		return nil, fmt.Errorf("aud does not contain client ID: %w", ErrInvalidClaims)
	}

	if claims.Nonce != expectedNonce {
		return nil, fmt.Errorf("nonce %q != %q: %w", claims.Nonce, expectedNonce, ErrInvalidClaims)
	}

	now := time.Now().Unix()
	if claims.ExpiresAt < now-int64(claimLeeway.Seconds()) {
		return nil, fmt.Errorf("token expired: %w", ErrInvalidClaims)
	}

	if claims.IssuedAt > now+int64(claimLeeway.Seconds()) {
		return nil, fmt.Errorf("token issued in the future: %w", ErrInvalidClaims)
	}

	return &claims, nil
}

func audienceMatches(aud any, clientID string) bool {
	switch v := aud.(type) {
	case string:
		return v == clientID
	case []interface{}:
		for _, a := range v {
			if s, ok := a.(string); ok && s == clientID {
				return true
			}
		}
	}

	return false
}
