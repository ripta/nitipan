package broker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/oidc"
)

type signInIssuer struct {
	server   *httptest.Server
	issuer   string
	clientID string

	wrongIssuer  bool
	expiredToken bool
	wrongNonce   bool

	expectedNonce string
	subject       string
	email         string
}

func newSignInIssuer(t *testing.T) *signInIssuer {
	t.Helper()

	si := &signInIssuer{
		clientID: "test-client",
		subject:  "test-subject",
		email:    "test@example.com",
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		issuer := si.issuer
		if si.wrongIssuer {
			issuer = "https://wrong-issuer.example"
		}

		doc := map[string]string{
			"issuer":                 issuer,
			"authorization_endpoint": si.issuer + "/authorize",
			"token_endpoint":         si.issuer + "/token",
		}

		_ = json.NewEncoder(w).Encode(doc)
	})

	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		idToken := si.makeIDToken()
		resp := map[string]string{
			"id_token": idToken,
		}

		_ = json.NewEncoder(w).Encode(resp)
	})

	si.server = httptest.NewUnstartedServer(mux)
	si.server.StartTLS()
	si.issuer = si.server.URL

	t.Cleanup(si.server.Close)

	return si
}

func (si *signInIssuer) makeIDToken() string {
	now := time.Now().Unix()
	exp := now + 3600

	if si.expiredToken {
		exp = now - 3600
	}

	nonce := si.expectedNonce
	if si.wrongNonce {
		nonce = "wrong-nonce"
	}

	claims := map[string]interface{}{
		"iss":   si.issuer,
		"sub":   si.subject,
		"aud":   si.clientID,
		"exp":   exp,
		"iat":   now,
		"nonce": nonce,
		"email": si.email,
	}

	claimsJSON, _ := json.Marshal(claims)
	claimsB64 := base64.RawURLEncoding.EncodeToString(claimsJSON)

	header := `{"alg":"RS256","typ":"JWT"}`
	headerB64 := base64.RawURLEncoding.EncodeToString([]byte(header))

	signature := base64.RawURLEncoding.EncodeToString([]byte("fake-signature"))

	return headerB64 + "." + claimsB64 + "." + signature
}

func (si *signInIssuer) writeCACert(t *testing.T) string {
	t.Helper()

	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: si.server.Certificate().Raw,
	})

	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func (si *signInIssuer) config(t *testing.T, redirectURL string) oidc.Config {
	t.Helper()

	caPath := si.writeCACert(t)

	return oidc.Config{
		Issuer:       si.issuer,
		ClientID:     si.clientID,
		ClientSecret: "",
		RedirectURL:  redirectURL,
		CAFile:       caPath,
	}
}

func newSignInHarness(t *testing.T) (*harness, *signInIssuer) {
	t.Helper()

	si := newSignInIssuer(t)

	oidcCfg := si.config(t, "http://localhost/callback")

	h := newHarness(t, func(cfg *Config) {
		cfg.OIDCConfig = &oidcCfg
	})

	return h, si
}

func storePairingCode(t *testing.T, b *Broker, label, code string, expiresAt time.Time) {
	t.Helper()

	hash := HashPairingCode(code)
	if err := b.StorePairingCode(label, hash, expiresAt); err != nil {
		t.Fatal(err)
	}
}

func TestSignInPageSetsCSP(t *testing.T) {
	h, _ := newSignInHarness(t)

	req := httptest.NewRequest("GET", "/signin", nil)
	w := httptest.NewRecorder()

	h.b.handleSignInPage(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	csp := w.Header().Get("Content-Security-Policy")
	if csp != "frame-ancestors 'none'" {
		t.Errorf("CSP = %q, want %q", csp, "frame-ancestors 'none'")
	}
}

func TestCallbackSetsCSPOnAllPages(t *testing.T) {
	h, si := newSignInHarness(t)

	if _, err := h.b.AdmitUser(si.issuer, si.subject, si.email, time.Now()); err != nil {
		t.Fatal(err)
	}

	authURL, err := h.b.signInMgr.StartAction("bind")
	if err != nil {
		t.Fatal(err)
	}

	u, _ := url.Parse(authURL)
	state := u.Query().Get("state")
	nonce := u.Query().Get("nonce")

	si.expectedNonce = nonce

	req := httptest.NewRequest("GET", "/callback?code=test-code&state="+state, nil)
	w := httptest.NewRecorder()

	h.b.handleCallback(w, req)

	csp := w.Header().Get("Content-Security-Policy")
	if csp != "frame-ancestors 'none'" {
		t.Errorf("callback CSP = %q, want %q", csp, "frame-ancestors 'none'")
	}

	authURL2, err := h.b.signInMgr.StartAction("bind")
	if err != nil {
		t.Fatal(err)
	}

	u2, _ := url.Parse(authURL2)
	state2 := u2.Query().Get("state")
	nonce2 := u2.Query().Get("nonce")

	si.expectedNonce = nonce2

	token, _, _, err := h.b.signInMgr.HandleCallback(context.Background(), state2, "test-code")
	if err != nil {
		t.Fatal(err)
	}

	n := nodeID{Machine: "test-machine", UID: 1000, Key: "test-key"}
	reg := h.register(n)

	req2 := httptest.NewRequest("POST", "/bind-submit", nil)
	req2.PostForm = url.Values{
		"token": {token},
		"label": {reg.Label},
		"code":  {"wrong-code"},
	}
	w2 := httptest.NewRecorder()

	h.b.handleBindSubmit(w2, req2)

	csp2 := w2.Header().Get("Content-Security-Policy")
	if csp2 != "frame-ancestors 'none'" {
		t.Errorf("bind-submit CSP = %q, want %q", csp2, "frame-ancestors 'none'")
	}
}

func TestUnadmittedSignInRecordsPendingAdmission(t *testing.T) {
	h, si := newSignInHarness(t)

	authURL, err := h.b.signInMgr.StartAction("bind")
	if err != nil {
		t.Fatal(err)
	}

	u, _ := url.Parse(authURL)
	state := u.Query().Get("state")
	nonce := u.Query().Get("nonce")

	si.expectedNonce = nonce

	req := httptest.NewRequest("GET", "/callback?code=test-code&state="+state, nil)
	w := httptest.NewRecorder()

	h.b.handleCallback(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	body := w.Body.String()
	if !strings.Contains(body, "Pending Admission") {
		t.Error("unadmitted user did not see pending admission page")
	}

	d := h.data()
	issuerSub := si.issuer + "\x00" + si.subject

	if _, ok := d.IssuerSubs[issuerSub]; ok {
		t.Error("unadmitted user was admitted")
	}

	found := false
	for _, pa := range d.PendingAdmissions {
		if pa.Issuer == si.issuer && pa.Sub == si.subject && pa.Email == si.email {
			found = true

			break
		}
	}

	if !found {
		t.Error("pending admission not recorded")
	}
}

func TestFormTokenIsSingleUse(t *testing.T) {
	h, si := newSignInHarness(t)

	if _, err := h.b.AdmitUser(si.issuer, si.subject, si.email, time.Now()); err != nil {
		t.Fatal(err)
	}

	authURL, err := h.b.signInMgr.StartAction("bind")
	if err != nil {
		t.Fatal(err)
	}

	u, _ := url.Parse(authURL)
	state := u.Query().Get("state")
	nonce := u.Query().Get("nonce")

	si.expectedNonce = nonce

	token, _, _, err := h.b.signInMgr.HandleCallback(context.Background(), state, "test-code")
	if err != nil {
		t.Fatal(err)
	}

	action1, claims1, err1 := h.b.signInMgr.ConsumeToken(token)
	if err1 != nil {
		t.Fatalf("first consume failed: %v", err1)
	}

	if action1 != "bind" || claims1 == nil {
		t.Error("first consume returned wrong data")
	}

	_, _, err2 := h.b.signInMgr.ConsumeToken(token)
	if err2 == nil {
		t.Error("token was reused")
	}
}

func TestWrongStateRefused(t *testing.T) {
	h, _ := newSignInHarness(t)

	req := httptest.NewRequest("GET", "/callback?code=test-code&state=wrong-state", nil)
	w := httptest.NewRecorder()

	h.b.handleCallback(w, req)

	if w.Code == http.StatusOK {
		t.Error("wrong state was accepted")
	}
}

func TestWrongNonceRefused(t *testing.T) {
	h, si := newSignInHarness(t)

	authURL, err := h.b.signInMgr.StartAction("bind")
	if err != nil {
		t.Fatal(err)
	}

	u, _ := url.Parse(authURL)
	state := u.Query().Get("state")

	si.expectedNonce = "correct-nonce"
	si.wrongNonce = true

	req := httptest.NewRequest("GET", "/callback?code=test-code&state="+state, nil)
	w := httptest.NewRecorder()

	h.b.handleCallback(w, req)

	if w.Code == http.StatusOK {
		t.Error("wrong nonce was accepted")
	}
}

func TestCrossSitePostWithoutTokenChangesNothing(t *testing.T) {
	h, si := newSignInHarness(t)

	if _, err := h.b.AdmitUser(si.issuer, si.subject, si.email, time.Now()); err != nil {
		t.Fatal(err)
	}

	n := nodeID{Machine: "test-machine", UID: 1000, Key: "test-key"}
	reg := h.register(n)
	label := reg.Label

	storePairingCode(t, h.b, label, "word-word-word-word", time.Now().Add(PairingCodeExpiry))

	req := httptest.NewRequest("POST", "/bind-submit", nil)
	req.PostForm = url.Values{
		"token": {"fake-token"},
		"label": {label},
		"code":  {"word-word-word-word"},
	}
	w := httptest.NewRecorder()

	h.b.handleBindSubmit(w, req)

	if w.Code == http.StatusOK {
		t.Error("cross-site post with fake token was accepted")
	}

	d := h.data()
	if d.Bindings[label].UserID != "" {
		t.Error("cross-site post changed the label binding")
	}
}

func TestCorrectLabelAndCodePinKeyAndDiscardOtherPending(t *testing.T) {
	h, si := newSignInHarness(t)

	if _, err := h.b.AdmitUser(si.issuer, si.subject, si.email, time.Now()); err != nil {
		t.Fatal(err)
	}

	n := nodeID{Machine: "test-machine", UID: 1000, Key: "test-key"}
	reg := h.register(n)
	label := reg.Label

	n2 := nodeID{Machine: "other-machine", UID: 1001, Key: "other-key"}
	reg2 := h.register(n2)
	otherLabel := reg2.Label

	storePairingCode(t, h.b, label, "word-word-word-word", time.Now().Add(PairingCodeExpiry))
	storePairingCode(t, h.b, otherLabel, "other-code-here-now", time.Now().Add(PairingCodeExpiry))

	authURL, err := h.b.signInMgr.StartAction("bind")
	if err != nil {
		t.Fatal(err)
	}

	u, _ := url.Parse(authURL)
	state := u.Query().Get("state")
	nonce := u.Query().Get("nonce")

	si.expectedNonce = nonce

	token, _, _, err := h.b.signInMgr.HandleCallback(context.Background(), state, "test-code")
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("POST", "/bind-submit", nil)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.PostForm = url.Values{
		"token": {token},
		"label": {label},
		"code":  {"word-word-word-word"},
	}

	if err := req.ParseForm(); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()

	h.b.handleBindSubmit(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("bind failed: status = %d, body = %s", w.Code, w.Body.String())
	}

	d := h.data()
	issuerSub := si.issuer + "\x00" + si.subject
	userID := d.IssuerSubs[issuerSub]

	if d.Bindings[label].UserID != userID {
		t.Errorf("label not bound to user: got %q, want %q", d.Bindings[label].UserID, userID)
	}

	if d.PairingCodes[label].Hash != "" {
		t.Error("pairing code not cleared after binding")
	}

	if d.PairingCodes[otherLabel].Hash == "" {
		t.Error("other label's pairing code was cleared")
	}
}

func TestWrongCodeBindsNothing(t *testing.T) {
	h, si := newSignInHarness(t)

	if _, err := h.b.AdmitUser(si.issuer, si.subject, si.email, time.Now()); err != nil {
		t.Fatal(err)
	}

	n := nodeID{Machine: "test-machine", UID: 1000, Key: "test-key"}
	reg := h.register(n)
	label := reg.Label

	storePairingCode(t, h.b, label, "word-word-word-word", time.Now().Add(PairingCodeExpiry))

	authURL, err := h.b.signInMgr.StartAction("bind")
	if err != nil {
		t.Fatal(err)
	}

	u, _ := url.Parse(authURL)
	state := u.Query().Get("state")
	nonce := u.Query().Get("nonce")

	si.expectedNonce = nonce

	token, _, _, err := h.b.signInMgr.HandleCallback(context.Background(), state, "test-code")
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("POST", "/bind-submit", nil)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.PostForm = url.Values{
		"token": {token},
		"label": {label},
		"code":  {"wrong-code-here-now"},
	}

	if err := req.ParseForm(); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()

	h.b.handleBindSubmit(w, req)

	if w.Code == http.StatusOK {
		t.Error("wrong code was accepted")
	}

	d := h.data()
	if d.Bindings[label].UserID != "" {
		t.Error("label was bound with wrong code")
	}
}

func TestExpiredCodeBindsNothing(t *testing.T) {
	h, si := newSignInHarness(t)

	if _, err := h.b.AdmitUser(si.issuer, si.subject, si.email, time.Now()); err != nil {
		t.Fatal(err)
	}

	n := nodeID{Machine: "test-machine", UID: 1000, Key: "test-key"}
	h.register(n)

	now := time.Now()
	storePairingCode(t, h.b, "test-label", "word-word-word-word", now.Add(-2*time.Hour))

	authURL, err := h.b.signInMgr.StartAction("bind")
	if err != nil {
		t.Fatal(err)
	}

	u, _ := url.Parse(authURL)
	state := u.Query().Get("state")
	nonce := u.Query().Get("nonce")

	si.expectedNonce = nonce

	token, _, _, err := h.b.signInMgr.HandleCallback(context.Background(), state, "test-code")
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("POST", "/bind-submit", nil)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.PostForm = url.Values{
		"token": {token},
		"label": {"test-label"},
		"code":  {"word-word-word-word"},
	}

	if err := req.ParseForm(); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()

	h.b.handleBindSubmit(w, req)

	if w.Code == http.StatusOK {
		t.Error("expired code was accepted")
	}

	d := h.data()
	if d.Bindings["test-label"].UserID != "" {
		t.Error("label was bound with expired code")
	}
}

func TestFiveWrongCodesLockOut(t *testing.T) {
	h, si := newSignInHarness(t)

	issuerSub := si.issuer + "\x00" + si.subject
	if _, err := h.b.AdmitUser(si.issuer, si.subject, si.email, time.Now()); err != nil {
		t.Fatal(err)
	}

	n := nodeID{Machine: "test-machine", UID: 1000, Key: "test-key"}
	h.register(n)

	storePairingCode(t, h.b, "test-label", "word-word-word-word", time.Now().Add(PairingCodeExpiry))

	for i := 0; i < 5; i++ {
		authURL, err := h.b.signInMgr.StartAction("bind")
		if err != nil {
			t.Fatal(err)
		}

		u, _ := url.Parse(authURL)
		state := u.Query().Get("state")
		nonce := u.Query().Get("nonce")

		si.expectedNonce = nonce

		token, _, _, err := h.b.signInMgr.HandleCallback(context.Background(), state, "test-code")
		if err != nil {
			t.Fatal(err)
		}

		req := httptest.NewRequest("POST", "/bind-submit", nil)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.PostForm = url.Values{
			"token": {token},
			"label": {"test-label"},
			"code":  {fmt.Sprintf("wrong-code-%d", i)},
		}

		if err := req.ParseForm(); err != nil {
			t.Fatal(err)
		}

		w := httptest.NewRecorder()

		h.b.handleBindSubmit(w, req)

		if w.Code == http.StatusOK {
			t.Errorf("wrong code %d was accepted", i)
		}
	}

	d := h.data()
	if d.Bindings["test-label"].UserID != "" {
		t.Error("label was bound after 5 wrong codes")
	}

	if d.PairingCodes["test-label"].Hash == "" {
		t.Error("pending key was discarded after lockout")
	}

	userID := d.IssuerSubs[issuerSub]

	if d.PairingCodes["test-label"].WrongAttempts == nil || d.PairingCodes["test-label"].WrongAttempts[userID] < 5 {
		t.Error("lockout not recorded")
	}

	authURL, err := h.b.signInMgr.StartAction("bind")
	if err != nil {
		t.Fatal(err)
	}

	u, _ := url.Parse(authURL)
	state := u.Query().Get("state")
	nonce := u.Query().Get("nonce")

	si.expectedNonce = nonce

	token, _, _, err := h.b.signInMgr.HandleCallback(context.Background(), state, "test-code")
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("POST", "/bind-submit", nil)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.PostForm = url.Values{
		"token": {token},
		"label": {"test-label"},
		"code":  {"word-word-word-word"},
	}

	if err := req.ParseForm(); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()

	h.b.handleBindSubmit(w, req)

	if w.Code == http.StatusOK {
		t.Error("correct code was accepted after lockout")
	}
}

func TestOwnerBoundLabelJoinsOwnerScope(t *testing.T) {
	h, si := newSignInHarness(t)

	if _, err := h.b.AdmitUser(si.issuer, si.subject, si.email, time.Now()); err != nil {
		t.Fatal(err)
	}

	n := nodeID{Machine: "test-machine", UID: 1000, Key: "test-key"}
	reg := h.register(n)
	label := reg.Label

	storePairingCode(t, h.b, label, "word-word-word-word", time.Now().Add(PairingCodeExpiry))

	authURL, err := h.b.signInMgr.StartAction("bind")
	if err != nil {
		t.Fatal(err)
	}

	u, _ := url.Parse(authURL)
	state := u.Query().Get("state")
	nonce := u.Query().Get("nonce")

	si.expectedNonce = nonce

	token, _, _, err := h.b.signInMgr.HandleCallback(context.Background(), state, "test-code")
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("POST", "/bind-submit", nil)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.PostForm = url.Values{
		"token": {token},
		"label": {label},
		"code":  {"word-word-word-word"},
	}

	if err := req.ParseForm(); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()

	h.b.handleBindSubmit(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("bind failed: %s", w.Body.String())
	}

	d := h.data()
	issuerSub := si.issuer + "\x00" + si.subject
	userID := d.IssuerSubs[issuerSub]

	if userID == "" {
		t.Error("user ID not found for issuer+sub")
	}

	if d.Bindings[label].UserID != userID {
		t.Errorf("label bound to %q, want %q", d.Bindings[label].UserID, userID)
	}
}

func TestPageNeverListsPendingLabels(t *testing.T) {
	h, si := newSignInHarness(t)

	if _, err := h.b.AdmitUser(si.issuer, si.subject, si.email, time.Now()); err != nil {
		t.Fatal(err)
	}

	n := nodeID{Machine: "test-machine", UID: 1000, Key: "test-key"}
	h.register(n)

	storePairingCode(t, h.b, "pending-label", "word-word-word-word", time.Now().Add(PairingCodeExpiry))

	authURL, err := h.b.signInMgr.StartAction("bind")
	if err != nil {
		t.Fatal(err)
	}

	u, _ := url.Parse(authURL)
	state := u.Query().Get("state")
	nonce := u.Query().Get("nonce")

	si.expectedNonce = nonce

	req := httptest.NewRequest("GET", "/callback?code=test-code&state="+state, nil)
	w := httptest.NewRecorder()

	h.b.handleCallback(w, req)

	body := w.Body.String()

	if strings.Contains(body, "pending-label") {
		t.Error("callback page listed pending label")
	}
}

func TestLabelAndEmailWithScriptTagsAreEscaped(t *testing.T) {
	h, si := newSignInHarness(t)

	si.email = "<script>alert('xss')</script>@example.com"

	if _, err := h.b.AdmitUser(si.issuer, si.subject, si.email, time.Now()); err != nil {
		t.Fatal(err)
	}

	n := nodeID{Machine: "test-machine", UID: 1000, Key: "test-key"}
	h.register(n)

	authURL, err := h.b.signInMgr.StartAction("bind")
	if err != nil {
		t.Fatal(err)
	}

	u, _ := url.Parse(authURL)
	state := u.Query().Get("state")
	nonce := u.Query().Get("nonce")

	si.expectedNonce = nonce

	req := httptest.NewRequest("GET", "/callback?code=test-code&state="+state, nil)
	w := httptest.NewRecorder()

	h.b.handleCallback(w, req)

	body := w.Body.String()

	if strings.Contains(body, "<script>") {
		t.Error("script tag not escaped in email")
	}

	if !strings.Contains(body, "&lt;script&gt;") {
		t.Error("email not properly escaped")
	}
}

func TestSignInResolvesThroughIssuerPlusSub(t *testing.T) {
	h, si := newSignInHarness(t)

	originalEmail := "original@example.com"
	si.email = originalEmail

	if _, err := h.b.AdmitUser(si.issuer, si.subject, si.email, time.Now()); err != nil {
		t.Fatal(err)
	}

	n := nodeID{Machine: "test-machine", UID: 1000, Key: "test-key"}
	reg := h.register(n)
	label := reg.Label

	storePairingCode(t, h.b, label, "word-word-word-word", time.Now().Add(PairingCodeExpiry))

	authURL, err := h.b.signInMgr.StartAction("bind")
	if err != nil {
		t.Fatal(err)
	}

	u, _ := url.Parse(authURL)
	state := u.Query().Get("state")
	nonce := u.Query().Get("nonce")

	si.expectedNonce = nonce

	token, _, _, err := h.b.signInMgr.HandleCallback(context.Background(), state, "test-code")
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("POST", "/bind-submit", nil)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.PostForm = url.Values{
		"token": {token},
		"label": {label},
		"code":  {"word-word-word-word"},
	}

	if err := req.ParseForm(); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()

	h.b.handleBindSubmit(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("first bind failed: %s", w.Body.String())
	}

	si.email = "changed@example.com"

	authURL2, err := h.b.signInMgr.StartAction("bind")
	if err != nil {
		t.Fatal(err)
	}

	u2, _ := url.Parse(authURL2)
	state2 := u2.Query().Get("state")
	nonce2 := u2.Query().Get("nonce")

	si.expectedNonce = nonce2

	req2 := httptest.NewRequest("GET", "/callback?code=test-code&state="+state2, nil)
	w2 := httptest.NewRecorder()

	h.b.handleCallback(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("second callback failed: %s", w2.Body.String())
	}

	body := w2.Body.String()
	if !strings.Contains(body, "changed@example.com") {
		t.Error("callback did not show changed email")
	}

	d := h.data()
	issuerSub := si.issuer + "\x00" + si.subject
	userID := d.IssuerSubs[issuerSub]

	if userID == "" {
		t.Error("user ID not found for issuer+sub after email change")
	}

	if d.Bindings[label].UserID != userID {
		t.Error("label binding changed after email change")
	}
}

func TestParametersBeforeSignInIgnored(t *testing.T) {
	h, si := newSignInHarness(t)

	if _, err := h.b.AdmitUser(si.issuer, si.subject, si.email, time.Now()); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("POST", "/bind?evil=parameter&another=bad", nil)
	w := httptest.NewRecorder()

	h.b.handleBindLabel(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("bind label failed: %d", w.Code)
	}

	location := w.Header().Get("Location")
	u, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}

	state := u.Query().Get("state")

	si.expectedNonce = u.Query().Get("nonce")

	req2 := httptest.NewRequest("GET", "/callback?code=test-code&state="+state+"&evil=injected", nil)
	w2 := httptest.NewRecorder()

	h.b.handleCallback(w2, req2)

	body := w2.Body.String()

	if strings.Contains(body, "evil") || strings.Contains(body, "parameter") || strings.Contains(body, "injected") {
		t.Error("callback page echoed query parameters")
	}
}
