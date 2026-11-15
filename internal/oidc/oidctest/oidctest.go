// Package oidctest runs a local OIDC issuer for tests in other packages. It serves discovery, an
// authorization endpoint that answers with a code, and a token endpoint that returns an ID token naming
// the nonce the authorization request carried.
package oidctest

import (
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/oidc"
	"github.com/space-armada/nitipan/internal/tlstest"
)

// Issuer is a local OIDC issuer served over TLS on loopback.
type Issuer struct {
	// URL is the issuer, and CAFile the certificate a client trusts for it.
	URL    string
	CAFile string

	ClientID string

	// ClientSecret, when set before the first sign-in, is what the token endpoint requires.
	ClientSecret string

	mu      sync.Mutex
	subject string
	email   string
	nonces  map[string]string
	forged  string
}

// New starts an issuer that signs in subject with email until SetUser changes them.
func New(t *testing.T, subject, email string) *Issuer {
	t.Helper()

	iss := &Issuer{ClientID: "test-client", subject: subject, email: email, nonces: map[string]string{}}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", iss.discovery)
	mux.HandleFunc("GET /authorize", iss.authorize)
	mux.HandleFunc("POST /token", iss.token)

	files := tlstest.SelfSigned(t, t.TempDir())

	cert, err := tls.LoadX509KeyPair(files.Cert, files.Key)
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewUnstartedServer(mux)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()

	t.Cleanup(srv.Close)

	iss.URL = srv.URL
	iss.CAFile = files.Cert

	return iss
}

// SetUser changes who the next sign-in names.
func (iss *Issuer) SetUser(subject, email string) {
	iss.mu.Lock()
	defer iss.mu.Unlock()

	iss.subject, iss.email = subject, email
}

// ForgeNonce makes the token endpoint name nonce in every later ID token, in place of the nonce the
// authorization request carried, as a token replayed from another sign-in would.
func (iss *Issuer) ForgeNonce(nonce string) {
	iss.mu.Lock()
	defer iss.mu.Unlock()

	iss.forged = nonce
}

// Client is an HTTP client that trusts the issuer's certificate and follows no redirect, so a test can
// read each hop of a sign-in.
func (iss *Issuer) Client(t *testing.T) *http.Client {
	t.Helper()

	pem, err := os.ReadFile(iss.CAFile)
	if err != nil {
		t.Fatal(err)
	}

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem)

	return &http.Client{
		Transport:     &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Config is the broker's OIDC config for this issuer.
func (iss *Issuer) Config(redirectURL string) oidc.Config {
	return oidc.Config{Issuer: iss.URL, ClientID: iss.ClientID, RedirectURL: redirectURL, CAFile: iss.CAFile}
}

func (iss *Issuer) discovery(w http.ResponseWriter, _ *http.Request) {
	_ = json.NewEncoder(w).Encode(map[string]string{
		"issuer":                 iss.URL,
		"authorization_endpoint": iss.URL + "/authorize",
		"token_endpoint":         iss.URL + "/token",
	})
}

// authorize signs the user in at once and redirects to the redirect URI with a code tied to the request's
// nonce, as a provider with a live session does.
func (iss *Issuer) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	var raw [16]byte

	_, _ = rand.Read(raw[:])
	code := hex.EncodeToString(raw[:])

	iss.mu.Lock()
	iss.nonces[code] = q.Get("nonce")
	iss.mu.Unlock()

	to, err := url.Parse(q.Get("redirect_uri"))
	if err != nil {
		http.Error(w, "bad redirect_uri", http.StatusBadRequest)

		return
	}

	back := to.Query()
	back.Set("code", code)
	back.Set("state", q.Get("state"))
	to.RawQuery = back.Encode()

	http.Redirect(w, r, to.String(), http.StatusFound)
}

func (iss *Issuer) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)

		return
	}

	if iss.ClientSecret != "" && r.PostForm.Get("client_secret") != iss.ClientSecret {
		http.Error(w, "wrong client secret", http.StatusUnauthorized)

		return
	}

	iss.mu.Lock()
	nonce, ok := iss.nonces[r.PostForm.Get("code")]
	delete(iss.nonces, r.PostForm.Get("code"))
	subject, email := iss.subject, iss.email

	if iss.forged != "" {
		nonce = iss.forged
	}
	iss.mu.Unlock()

	if !ok {
		http.Error(w, "unknown code", http.StatusBadRequest)

		return
	}

	now := time.Now().Unix()

	claims, _ := json.Marshal(map[string]any{
		"iss": iss.URL, "sub": subject, "aud": iss.ClientID, "exp": now + 3600, "iat": now, "nonce": nonce, "email": email,
	})

	enc := base64.RawURLEncoding
	idToken := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + enc.EncodeToString(claims) + "." +
		enc.EncodeToString([]byte("unchecked"))

	_ = json.NewEncoder(w).Encode(map[string]string{"id_token": idToken})
}
