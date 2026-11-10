package oidc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type testIssuer struct {
	server   *httptest.Server
	issuer   string
	clientID string

	wrongIssuer      bool
	expiredToken     bool
	wrongNonce       bool
	wrongAud         bool
	redirectEndpoint bool

	expectedNonce string
}

func newTestIssuer(t *testing.T) *testIssuer {
	t.Helper()

	ti := &testIssuer{
		clientID: "test-client",
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		issuer := ti.issuer
		if ti.wrongIssuer {
			issuer = "https://wrong-issuer.example"
		}

		doc := map[string]string{
			"issuer":                 issuer,
			"authorization_endpoint": ti.issuer + "/authorize",
			"token_endpoint":         ti.issuer + "/token",
		}

		_ = json.NewEncoder(w).Encode(doc)
	})

	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if ti.redirectEndpoint {
			http.Redirect(w, r, "https://evil.example", http.StatusFound)

			return
		}

		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		idToken := ti.makeIDToken()
		resp := map[string]string{
			"id_token": idToken,
		}

		_ = json.NewEncoder(w).Encode(resp)
	})

	ti.server = httptest.NewUnstartedServer(mux)
	ti.server.StartTLS()
	ti.issuer = ti.server.URL

	t.Cleanup(ti.server.Close)

	return ti
}

func (ti *testIssuer) makeIDToken() string {
	now := time.Now().Unix()
	exp := now + 3600

	if ti.expiredToken {
		exp = now - 3600
	}

	nonce := ti.expectedNonce
	if ti.wrongNonce {
		nonce = "wrong-nonce"
	}

	aud := ti.clientID
	if ti.wrongAud {
		aud = "wrong-client"
	}

	claims := map[string]interface{}{
		"iss":   ti.issuer,
		"sub":   "test-subject",
		"aud":   aud,
		"exp":   exp,
		"iat":   now,
		"nonce": nonce,
		"email": "test@example.com",
	}

	claimsJSON, _ := json.Marshal(claims)
	claimsB64 := base64.RawURLEncoding.EncodeToString(claimsJSON)

	header := `{"alg":"RS256","typ":"JWT"}`
	headerB64 := base64.RawURLEncoding.EncodeToString([]byte(header))

	signature := base64.RawURLEncoding.EncodeToString([]byte("fake-signature"))

	return headerB64 + "." + claimsB64 + "." + signature
}

func (ti *testIssuer) writeCACert(t *testing.T) string {
	t.Helper()

	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: ti.server.Certificate().Raw,
	})

	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func (ti *testIssuer) config(caPath string) Config {
	return Config{
		Issuer:       ti.issuer,
		ClientID:     ti.clientID,
		ClientSecret: "",
		RedirectURL:  ti.issuer + "/callback",
		CAFile:       caPath,
	}
}

func TestNewClientFetchesDiscoveryDocument(t *testing.T) {
	ti := newTestIssuer(t)
	caPath := ti.writeCACert(t)

	client, err := fetchedClient(ti.config(caPath))
	if err != nil {
		t.Fatal(err)
	}

	if client.discovery == nil {
		t.Error("discovery document is nil")
	}

	if client.discovery.Issuer != ti.issuer {
		t.Errorf("discovery issuer = %q, want %q", client.discovery.Issuer, ti.issuer)
	}
}

func TestNewClientRefusesWrongIssuerInDiscovery(t *testing.T) {
	ti := newTestIssuer(t)
	ti.wrongIssuer = true
	caPath := ti.writeCACert(t)

	_, err := fetchedClient(ti.config(caPath))
	if err == nil {
		t.Error("client accepted wrong issuer in discovery")
	}

	if !strings.Contains(err.Error(), "issuer") {
		t.Errorf("error = %v, want issuer mismatch", err)
	}
}

func TestNewClientRefusesPlainHTTPIssuerOffLoopback(t *testing.T) {
	cfg := Config{
		Issuer:      "http://example.com",
		ClientID:    "test",
		RedirectURL: "https://broker.example/callback",
	}

	_, err := fetchedClient(cfg)
	if err == nil {
		t.Error("client accepted plain HTTP issuer off loopback")
	}
}

func TestNewClientAcceptsPlainHTTPOnLoopback(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		baseURL := "http://" + r.Host

		doc := map[string]string{
			"issuer":                 baseURL,
			"authorization_endpoint": baseURL + "/authorize",
			"token_endpoint":         baseURL + "/token",
		}

		_ = json.NewEncoder(w).Encode(doc)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	cfg := Config{
		Issuer:      server.URL,
		ClientID:    "test",
		RedirectURL: server.URL + "/callback",
	}

	if _, err := fetchedClient(cfg); err != nil {
		t.Errorf("client refused loopback HTTP: %v", err)
	}
}

func TestExchangeValidatesClaimsAndReturnsSubject(t *testing.T) {
	ti := newTestIssuer(t)
	caPath := ti.writeCACert(t)

	client, err := fetchedClient(ti.config(caPath))
	if err != nil {
		t.Fatal(err)
	}

	nonce := GenerateNonce()
	ti.expectedNonce = nonce
	verifier := GeneratePKCEVerifier()

	claims, err := client.Exchange(context.Background(), "test-code", verifier, nonce)
	if err != nil {
		t.Fatal(err)
	}

	if claims.Subject != "test-subject" {
		t.Errorf("subject = %q, want %q", claims.Subject, "test-subject")
	}

	if claims.Email != "test@example.com" {
		t.Errorf("email = %q, want %q", claims.Email, "test@example.com")
	}
}

func TestExchangeRefusesExpiredToken(t *testing.T) {
	ti := newTestIssuer(t)
	ti.expiredToken = true
	caPath := ti.writeCACert(t)

	client, err := fetchedClient(ti.config(caPath))
	if err != nil {
		t.Fatal(err)
	}

	nonce := GenerateNonce()
	ti.expectedNonce = nonce
	verifier := GeneratePKCEVerifier()

	_, err = client.Exchange(context.Background(), "test-code", verifier, nonce)
	if err == nil {
		t.Error("client accepted expired token")
	}
}

func TestExchangeRefusesWrongNonce(t *testing.T) {
	ti := newTestIssuer(t)
	ti.wrongNonce = true
	caPath := ti.writeCACert(t)

	client, err := fetchedClient(ti.config(caPath))
	if err != nil {
		t.Fatal(err)
	}

	nonce := GenerateNonce()
	ti.expectedNonce = nonce
	verifier := GeneratePKCEVerifier()

	_, err = client.Exchange(context.Background(), "test-code", verifier, nonce)
	if err == nil {
		t.Error("client accepted wrong nonce")
	}
}

func TestExchangeRefusesWrongAudience(t *testing.T) {
	ti := newTestIssuer(t)
	ti.wrongAud = true
	caPath := ti.writeCACert(t)

	client, err := fetchedClient(ti.config(caPath))
	if err != nil {
		t.Fatal(err)
	}

	nonce := GenerateNonce()
	ti.expectedNonce = nonce
	verifier := GeneratePKCEVerifier()

	_, err = client.Exchange(context.Background(), "test-code", verifier, nonce)
	if err == nil {
		t.Error("client accepted wrong audience")
	}
}

func TestExchangeRefusesRedirectingTokenEndpoint(t *testing.T) {
	ti := newTestIssuer(t)
	ti.redirectEndpoint = true
	caPath := ti.writeCACert(t)

	client, err := fetchedClient(ti.config(caPath))
	if err != nil {
		t.Fatal(err)
	}

	nonce := GenerateNonce()
	ti.expectedNonce = nonce
	verifier := GeneratePKCEVerifier()

	_, err = client.Exchange(context.Background(), "test-code", verifier, nonce)
	if err == nil {
		t.Error("client followed redirect from token endpoint")
	}
}

func TestAuthorizationURLContainsPKCEAndNonce(t *testing.T) {
	ti := newTestIssuer(t)
	caPath := ti.writeCACert(t)

	client, err := fetchedClient(ti.config(caPath))
	if err != nil {
		t.Fatal(err)
	}

	state := GenerateState()
	nonce := GenerateNonce()
	verifier := GeneratePKCEVerifier()

	authURL, err := client.AuthorizationURL(state, nonce, verifier)
	if err != nil {
		t.Fatal(err)
	}

	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}

	q := u.Query()

	if q.Get("state") != state {
		t.Error("authorization URL missing state")
	}

	if q.Get("nonce") != nonce {
		t.Error("authorization URL missing nonce")
	}

	if q.Get("code_challenge") == "" {
		t.Error("authorization URL missing code_challenge")
	}

	if q.Get("code_challenge_method") != "S256" {
		t.Error("authorization URL missing or wrong code_challenge_method")
	}
}

func TestGenerateStateNonceVerifierProduceRandomStrings(t *testing.T) {
	state1 := GenerateState()
	state2 := GenerateState()

	if state1 == state2 {
		t.Error("GenerateState produced same value twice")
	}

	nonce1 := GenerateNonce()
	nonce2 := GenerateNonce()

	if nonce1 == nonce2 {
		t.Error("GenerateNonce produced same value twice")
	}

	verifier1 := GeneratePKCEVerifier()
	verifier2 := GeneratePKCEVerifier()

	if verifier1 == verifier2 {
		t.Error("GeneratePKCEVerifier produced same value twice")
	}
}

func TestAudienceMatchesHandlesStringAndArray(t *testing.T) {
	if !audienceMatches("test-client", "test-client") {
		t.Error("string audience did not match")
	}

	if audienceMatches("wrong-client", "test-client") {
		t.Error("wrong string audience matched")
	}

	audArray := []interface{}{"other-client", "test-client", "another-client"}
	if !audienceMatches(audArray, "test-client") {
		t.Error("array audience did not match")
	}

	audArrayWrong := []interface{}{"other-client", "another-client"}
	if audienceMatches(audArrayWrong, "test-client") {
		t.Error("wrong array audience matched")
	}
}

func TestPlainHTTPTokenEndpointRefusedOffLoopback(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		scheme := "http"
		if strings.Contains(r.Host, "localhost") || strings.Contains(r.Host, "127.0.0.1") {
			scheme = "https"
		}

		doc := map[string]string{
			"issuer":                 fmt.Sprintf("%s://%s", scheme, r.Host),
			"authorization_endpoint": fmt.Sprintf("%s://%s/authorize", scheme, r.Host),
			"token_endpoint":         "http://example.com/token",
		}

		_ = json.NewEncoder(w).Encode(doc)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	cfg := Config{
		Issuer:      server.URL,
		ClientID:    "test",
		RedirectURL: server.URL + "/callback",
	}

	_, err := fetchedClient(cfg)
	if err == nil {
		t.Error("client accepted plain HTTP token endpoint off loopback")
	}
}

// fetchedClient builds a client and fetches its discovery document at once, so a test sees a discovery
// refusal where it happens.
func fetchedClient(cfg Config) (*Client, error) {
	c, err := New(cfg)
	if err != nil {
		return nil, err
	}

	if _, err := c.discover(); err != nil {
		return nil, err
	}

	return c, nil
}

type redirectURLTest struct {
	Name        string
	RedirectURL string
	Refused     bool
}

var redirectURLTests = []redirectURLTest{
	{Name: "https", RedirectURL: "https://broker.example/callback"},
	{Name: "plain HTTP on loopback", RedirectURL: "http://127.0.0.1:8443/callback"},
	{Name: "plain HTTP on localhost", RedirectURL: "http://localhost:8443/callback"},
	{Name: "plain HTTP off loopback", RedirectURL: "http://broker.example/callback", Refused: true},
	{Name: "missing", RedirectURL: "", Refused: true},
}

// TestNewClientRefusesAPlainHTTPRedirectURLOffLoopback proves the client refuses a redirect URL that is
// missing or plain HTTP off loopback, as it refuses such an issuer. The provider sends the authorization
// code there, so a plain-HTTP hop lets anyone on the path take the code. It catches a config the broker
// accepts and then leaks codes through.
func TestNewClientRefusesAPlainHTTPRedirectURLOffLoopback(t *testing.T) {
	for _, test := range redirectURLTests {
		t.Run(test.Name, func(t *testing.T) {
			_, err := New(Config{Issuer: "https://issuer.example", ClientID: "test", RedirectURL: test.RedirectURL})

			if refused := errors.Is(err, ErrRedirectURLHTTP); refused != test.Refused {
				t.Errorf("New with redirect URL %q: %v, want refused %v", test.RedirectURL, err, test.Refused)
			}
		})
	}
}
