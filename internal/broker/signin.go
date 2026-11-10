package broker

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"sync"
	"time"

	"github.com/space-armada/nitipan/internal/oidc"
)

// SignInManager handles OIDC sign-in flows with state management.
type SignInManager struct {
	mu         sync.Mutex
	oidcClient *oidc.Client
	pending    map[string]*pendingAction
	broker     *Broker
}

// pendingAction tracks one OIDC round trip.
type pendingAction struct {
	action    string
	state     string
	nonce     string
	verifier  string
	token     string
	claims    *oidc.Claims
	createdAt time.Time
	used      bool
}

const actionExpiry = 15 * time.Minute

// NewSignInManager creates a sign-in manager.
func NewSignInManager(broker *Broker, oidcClient *oidc.Client) *SignInManager {
	sm := &SignInManager{
		oidcClient: oidcClient,
		pending:    make(map[string]*pendingAction),
		broker:     broker,
	}

	go sm.cleanupExpired()

	return sm
}

func (sm *SignInManager) cleanupExpired() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		sm.mu.Lock()
		now := sm.broker.cfg.Now()

		for id, pa := range sm.pending {
			if now.Sub(pa.createdAt) > actionExpiry {
				delete(sm.pending, id)
			}
		}

		sm.mu.Unlock()
	}
}

// StartAction initiates an OIDC flow for an action.
func (sm *SignInManager) StartAction(action string) (authURL string, err error) {
	state := oidc.GenerateState()
	nonce := oidc.GenerateNonce()
	verifier := oidc.GeneratePKCEVerifier()

	sm.mu.Lock()
	sm.pending[state] = &pendingAction{
		action:    action,
		state:     state,
		nonce:     nonce,
		verifier:  verifier,
		createdAt: sm.broker.cfg.Now(),
	}
	sm.mu.Unlock()

	return sm.oidcClient.AuthorizationURL(state, nonce, verifier), nil
}

// HandleCallback processes the OIDC callback and returns a single-use token.
func (sm *SignInManager) HandleCallback(ctx context.Context, state, code string) (token string, action string, claims *oidc.Claims, err error) {
	sm.mu.Lock()
	pa, ok := sm.pending[state]
	if !ok {
		sm.mu.Unlock()

		return "", "", nil, errors.New("invalid or expired state")
	}

	if pa.used {
		sm.mu.Unlock()

		return "", "", nil, errors.New("state already used")
	}

	verifier := pa.verifier
	nonce := pa.nonce
	action = pa.action
	sm.mu.Unlock()

	claims, err = sm.oidcClient.Exchange(ctx, code, verifier, nonce)
	if err != nil {
		return "", "", nil, err
	}

	token = generateToken()

	sm.mu.Lock()
	pa.token = token
	pa.claims = claims
	pa.used = true
	sm.mu.Unlock()

	return token, action, claims, nil
}

// ConsumeToken validates and consumes a single-use token, returning the claims and action.
func (sm *SignInManager) ConsumeToken(token string) (action string, claims *oidc.Claims, err error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	for state, pa := range sm.pending {
		if pa.token != "" && subtle.ConstantTimeCompare([]byte(pa.token), []byte(token)) == 1 {
			if sm.broker.cfg.Now().Sub(pa.createdAt) > actionExpiry {
				delete(sm.pending, state)

				return "", nil, errors.New("token expired")
			}

			if pa.claims == nil {
				return "", nil, errors.New("token not yet completed")
			}

			action = pa.action
			claims = pa.claims

			delete(sm.pending, state)

			return action, claims, nil
		}
	}

	return "", nil, errors.New("invalid token")
}

func generateToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}

	return hex.EncodeToString(b)
}

// handleSignInPage serves the main sign-in page.
// Only broker.go calls this handler.
func (b *Broker) handleSignInPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		return
	}

	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	tmpl := template.Must(template.New("signin").Parse(signInPageHTML))
	if err := tmpl.Execute(w, nil); err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
	}
}

// handleBindLabel initiates the bind action.
// Only broker.go calls this handler.
func (b *Broker) handleBindLabel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		return
	}

	if b.signInMgr == nil {
		http.Error(w, "OIDC not configured", http.StatusServiceUnavailable)

		return
	}

	authURL, err := b.signInMgr.StartAction("bind")
	if err != nil {
		http.Error(w, "Failed to start sign-in", http.StatusInternalServerError)

		return
	}

	http.Redirect(w, r, authURL, http.StatusFound)
}

// handleCallback processes the OIDC callback and either presents a bind form
// or records a pending admission for unadmitted users.
// Only broker.go calls this handler.
func (b *Broker) handleCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		return
	}

	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")

	if code == "" || state == "" {
		http.Error(w, "Missing code or state", http.StatusBadRequest)

		return
	}

	token, action, claims, err := b.signInMgr.HandleCallback(r.Context(), state, code)
	if err != nil {
		http.Error(w, fmt.Sprintf("Sign-in failed: %v", err), http.StatusUnauthorized)

		return
	}

	issuerSub := claims.Issuer + "\x00" + claims.Subject

	b.mu.Lock()
	userID, admitted := b.store.Data().IssuerSubs[issuerSub]
	b.mu.Unlock()

	if !admitted {
		if err := b.RecordPendingAdmission(claims.Issuer, claims.Subject, claims.Email, b.cfg.Now()); err != nil {
			http.Error(w, "Failed to record pending admission", http.StatusInternalServerError)

			return
		}

		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		tmpl := template.Must(template.New("pending").Parse(pendingAdmissionHTML))
		if err := tmpl.Execute(w, nil); err != nil {
			http.Error(w, "Internal error", http.StatusInternalServerError)
		}

		return
	}

	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	tmpl := template.Must(template.New("action").Parse(actionFormHTML))
	data := struct {
		Action string
		Token  string
		Email  string
		UserID string
	}{
		Action: action,
		Token:  token,
		Email:  claims.Email,
		UserID: userID,
	}

	if err := tmpl.Execute(w, data); err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
	}
}

// handleBindSubmit processes the bind form submission.
// The form token is single-use and consumed by ConsumeToken.
// Only broker.go calls this handler.
func (b *Broker) handleBindSubmit(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)

		return
	}

	token := r.FormValue("token")
	label := r.FormValue("label")
	code := r.FormValue("code")

	if token == "" || label == "" || code == "" {
		http.Error(w, "Missing required fields", http.StatusBadRequest)

		return
	}

	action, claims, err := b.signInMgr.ConsumeToken(token)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid token: %v", err), http.StatusUnauthorized)

		return
	}

	if action != "bind" {
		http.Error(w, "Token not for bind action", http.StatusBadRequest)

		return
	}

	issuerSub := claims.Issuer + "\x00" + claims.Subject

	b.mu.Lock()
	userID, admitted := b.store.Data().IssuerSubs[issuerSub]
	b.mu.Unlock()

	if !admitted {
		http.Error(w, "User not admitted", http.StatusForbidden)

		return
	}

	ok, err := b.CheckPairingCode(label, code, userID, b.cfg.Now())
	if err != nil {
		http.Error(w, fmt.Sprintf("Pairing code check failed: %v", err), http.StatusInternalServerError)

		return
	}

	if !ok {
		http.Error(w, "Wrong or expired pairing code", http.StatusUnauthorized)

		return
	}

	if err := b.BindLabelToUser(label, userID, b.cfg.Now()); err != nil {
		http.Error(w, fmt.Sprintf("Failed to bind label: %v", err), http.StatusInternalServerError)

		return
	}

	if err := b.ClearPairingCode(label); err != nil {
		http.Error(w, "Failed to clear pairing code", http.StatusInternalServerError)

		return
	}

	b.mu.Lock()
	user := b.store.Data().Users[userID]
	b.mu.Unlock()

	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	tmpl := template.Must(template.New("success").Parse(successHTML))
	data := struct {
		Label string
		Email string
	}{
		Label: label,
		Email: user.Email,
	}

	if err := tmpl.Execute(w, data); err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
	}
}

func (b *Broker) handleGrantDelegation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		return
	}

	if b.signInMgr == nil {
		http.Error(w, "OIDC not configured", http.StatusServiceUnavailable)

		return
	}

	authURL, err := b.signInMgr.StartAction("grant")
	if err != nil {
		http.Error(w, "Failed to start sign-in", http.StatusInternalServerError)

		return
	}

	http.Redirect(w, r, authURL, http.StatusFound)
}

func (b *Broker) handleRevokeDelegation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		return
	}

	if b.signInMgr == nil {
		http.Error(w, "OIDC not configured", http.StatusServiceUnavailable)

		return
	}

	authURL, err := b.signInMgr.StartAction("revoke")
	if err != nil {
		http.Error(w, "Failed to start sign-in", http.StatusInternalServerError)

		return
	}

	http.Redirect(w, r, authURL, http.StatusFound)
}

func (b *Broker) handleGrantSubmit(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)

		return
	}

	token := r.FormValue("token")
	label := r.FormValue("label")

	if token == "" || label == "" {
		http.Error(w, "Missing required fields", http.StatusBadRequest)

		return
	}

	action, claims, err := b.signInMgr.ConsumeToken(token)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid token: %v", err), http.StatusUnauthorized)

		return
	}

	if action != "grant" {
		http.Error(w, "Token not for grant action", http.StatusBadRequest)

		return
	}

	user, err := b.GrantDelegation(claims.Issuer, claims.Subject, label, b.cfg.Now())
	if errors.Is(err, ErrNotAdmitted) {
		http.Error(w, "User not admitted", http.StatusForbidden)

		return
	}

	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to grant delegation: %v", err), http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	tmpl := template.Must(template.New("grant-success").Parse(grantSuccessHTML))
	data := struct {
		Label string
		Email string
	}{
		Label: label,
		Email: user.Email,
	}

	if err := tmpl.Execute(w, data); err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
	}
}

func (b *Broker) handleRevokeSubmit(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)

		return
	}

	token := r.FormValue("token")
	label := r.FormValue("label")

	if token == "" || label == "" {
		http.Error(w, "Missing required fields", http.StatusBadRequest)

		return
	}

	action, claims, err := b.signInMgr.ConsumeToken(token)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid token: %v", err), http.StatusUnauthorized)

		return
	}

	if action != "revoke" {
		http.Error(w, "Token not for revoke action", http.StatusBadRequest)

		return
	}

	user, err := b.RevokeDelegation(claims.Issuer, claims.Subject, label)
	if errors.Is(err, ErrNotAdmitted) {
		http.Error(w, "User not admitted", http.StatusForbidden)

		return
	}

	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to revoke delegation: %v", err), http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	tmpl := template.Must(template.New("revoke-success").Parse(revokeSuccessHTML))
	data := struct {
		Label string
		Email string
	}{
		Label: label,
		Email: user.Email,
	}

	if err := tmpl.Execute(w, data); err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
	}
}

// Page actions for mail grants. Each runs its own OIDC round trip, as a delegation does.
const (
	actionMailGrant  = "mail-grant"
	actionMailRevoke = "mail-revoke"
)

func (b *Broker) handleStartMailGrant(w http.ResponseWriter, r *http.Request) {
	b.startAction(w, r, actionMailGrant)
}

func (b *Broker) handleStartMailRevoke(w http.ResponseWriter, r *http.Request) {
	b.startAction(w, r, actionMailRevoke)
}

// startAction begins an action's OIDC round trip. The parameters of the action are never read here, so
// nothing sent before the sign-in is acted on, shown, or prefilled.
func (b *Broker) startAction(w http.ResponseWriter, r *http.Request, action string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		return
	}

	if b.signInMgr == nil {
		http.Error(w, "OIDC not configured", http.StatusServiceUnavailable)

		return
	}

	authURL, err := b.signInMgr.StartAction(action)
	if err != nil {
		http.Error(w, "Failed to start sign-in", http.StatusInternalServerError)

		return
	}

	http.Redirect(w, r, authURL, http.StatusFound)
}

func (b *Broker) handleMailGrantSubmit(w http.ResponseWriter, r *http.Request) {
	b.mailGrantSubmit(w, r, actionMailGrant, b.GrantMail, mailGrantSuccessHTML)
}

func (b *Broker) handleMailRevokeSubmit(w http.ResponseWriter, r *http.Request) {
	revoke := func(issuer, sub, label string, _ time.Time) (UserRecord, UserRecord, error) {
		return b.RevokeMail(issuer, sub, label)
	}

	b.mailGrantSubmit(w, r, actionMailRevoke, revoke, mailRevokeSuccessHTML)
}

// mailGrantSubmit takes the form rendered after an action's callback. The single-use token ties it to
// that round trip, and the signer's admission is checked under the change's own lock.
func (b *Broker) mailGrantSubmit(w http.ResponseWriter, r *http.Request, want string,
	change func(issuer, sub, senderLabel string, now time.Time) (UserRecord, UserRecord, error), page string,
) {
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)

		return
	}

	token := r.FormValue("token")
	userLabel := r.FormValue("user_label")

	if token == "" || userLabel == "" {
		http.Error(w, "Missing required fields", http.StatusBadRequest)

		return
	}

	action, claims, err := b.signInMgr.ConsumeToken(token)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid token: %v", err), http.StatusUnauthorized)

		return
	}

	if action != want {
		http.Error(w, "Token not for this action", http.StatusBadRequest)

		return
	}

	granter, sender, err := change(claims.Issuer, claims.Subject, userLabel, b.cfg.Now())

	switch {
	case errors.Is(err, ErrNotAdmitted):
		http.Error(w, "User not admitted", http.StatusForbidden)

		return
	case errors.Is(err, ErrUnknownUserLabel), errors.Is(err, ErrSelfGrant):
		http.Error(w, err.Error(), http.StatusBadRequest)

		return
	case err != nil:
		http.Error(w, fmt.Sprintf("Failed to change the mail grant: %v", err), http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	tmpl := template.Must(template.New("mail-grant-success").Parse(page))
	data := struct {
		Sender string
		Email  string
	}{
		Sender: sender.UserLabel,
		Email:  granter.Email,
	}

	if err := tmpl.Execute(w, data); err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
	}
}

const signInPageHTML = `<!DOCTYPE html>
<html>
<head>
    <title>nitipan Sign In</title>
    <style>
        body { font-family: sans-serif; max-width: 600px; margin: 50px auto; padding: 20px; }
        button { padding: 10px 20px; font-size: 16px; margin: 10px 5px; }
    </style>
</head>
<body>
    <h1>nitipan Broker Sign In</h1>
    <p>Choose an action:</p>
    <form method="POST" action="/bind">
        <button type="submit">Bind a Label</button>
    </form>
    <form method="POST" action="/grant">
        <button type="submit">Grant Delegation</button>
    </form>
    <form method="POST" action="/revoke">
        <button type="submit">Revoke Delegation</button>
    </form>
    <form method="POST" action="/mail-grant">
        <button type="submit">Grant Mail From a User</button>
    </form>
    <form method="POST" action="/mail-revoke">
        <button type="submit">Revoke Mail From a User</button>
    </form>
</body>
</html>`

const actionFormHTML = `<!DOCTYPE html>
<html>
<head>
    <title>{{if eq .Action "bind"}}Bind Label{{else if eq .Action "grant"}}Grant Delegation{{else if eq .Action "revoke"}}Revoke Delegation{{else if eq .Action "mail-grant"}}Grant Mail{{else if eq .Action "mail-revoke"}}Revoke Mail{{end}}</title>
    <style>
        body { font-family: sans-serif; max-width: 600px; margin: 50px auto; padding: 20px; }
        input { padding: 8px; margin: 5px 0; width: 100%; box-sizing: border-box; }
        button { padding: 10px 20px; font-size: 16px; margin-top: 10px; }
        .info { background: #f0f0f0; padding: 10px; margin: 10px 0; }
    </style>
</head>
<body>
    <h1>{{if eq .Action "bind"}}Bind Label{{else if eq .Action "grant"}}Grant Delegation{{else if eq .Action "revoke"}}Revoke Delegation{{else if eq .Action "mail-grant"}}Grant Mail{{else if eq .Action "mail-revoke"}}Revoke Mail{{end}}</h1>
    <div class="info">
        <p>Signed in as: {{.Email}}</p>
        <p>User ID: {{.UserID}}</p>
    </div>
    {{if eq .Action "bind"}}
    <p>Enter the label and pairing code shown on your machine:</p>
    <form method="POST" action="/bind-submit">
        <input type="hidden" name="token" value="{{.Token}}">
        <label>Label:</label>
        <input type="text" name="label" placeholder="e.g. alpha-bravo-charlie-delta" required>
        <label>Pairing Code:</label>
        <input type="text" name="code" placeholder="e.g. word-word-word-word" required>
        <p><strong>You are binding this label to your user.</strong></p>
        <button type="submit">Confirm Bind</button>
    </form>
    {{else if eq .Action "grant"}}
    <p>Enter the label to delegate to:</p>
    <form method="POST" action="/grant-submit">
        <input type="hidden" name="token" value="{{.Token}}">
        <label>Label:</label>
        <input type="text" name="label" placeholder="e.g. alpha-bravo-charlie-delta" required>
        <p><strong>You are granting this label permission to act in your scope.</strong></p>
        <button type="submit">Confirm Grant</button>
    </form>
    {{else if eq .Action "revoke"}}
    <p>Enter the label to revoke delegation from:</p>
    <form method="POST" action="/revoke-submit">
        <input type="hidden" name="token" value="{{.Token}}">
        <label>Label:</label>
        <input type="text" name="label" placeholder="e.g. alpha-bravo-charlie-delta" required>
        <p><strong>You are revoking this label's permission to act in your scope. Its leases held for you will be ended.</strong></p>
        <button type="submit">Confirm Revoke</button>
    </form>
    {{else if eq .Action "mail-grant"}}
    <p>Enter the user label of the user whose mail you accept:</p>
    <form method="POST" action="/mail-grant-submit">
        <input type="hidden" name="token" value="{{.Token}}">
        <label>User label:</label>
        <input type="text" name="user_label" placeholder="e.g. word-word-word-word" required>
        <p><strong>You are letting this user's sessions mail yours. A first message reaches only a session of yours that holds a shared lease. Replies within its conversation reach the session that sent you mail.</strong></p>
        <button type="submit">Confirm Grant</button>
    </form>
    {{else if eq .Action "mail-revoke"}}
    <p>Enter the user label of the user whose mail you no longer accept:</p>
    <form method="POST" action="/mail-revoke-submit">
        <input type="hidden" name="token" value="{{.Token}}">
        <label>User label:</label>
        <input type="text" name="user_label" placeholder="e.g. word-word-word-word" required>
        <p><strong>You are revoking this user's mail grant. Every later message of its conversations, replies included, comes back to its sender.</strong></p>
        <button type="submit">Confirm Revoke</button>
    </form>
    {{end}}
</body>
</html>`

const successHTML = `<!DOCTYPE html>
<html>
<head>
    <title>Success</title>
    <style>
        body { font-family: sans-serif; max-width: 600px; margin: 50px auto; padding: 20px; }
    </style>
</head>
<body>
    <h1>Success</h1>
    <p>Label {{.Label}} bound to {{.Email}}.</p>
</body>
</html>`

const pendingAdmissionHTML = `<!DOCTYPE html>
<html>
<head>
    <title>Pending Admission</title>
    <style>
        body { font-family: sans-serif; max-width: 600px; margin: 50px auto; padding: 20px; }
    </style>
</head>
<body>
    <h1>Pending Admission</h1>
    <p>Your sign-in was recorded. Ask the broker owner to admit you.</p>
</body>
</html>`

const grantSuccessHTML = `<!DOCTYPE html>
<html>
<head>
    <title>Success</title>
    <style>
        body { font-family: sans-serif; max-width: 600px; margin: 50px auto; padding: 20px; }
    </style>
</head>
<body>
    <h1>Success</h1>
    <p>Delegation granted to {{.Label}} for {{.Email}}.</p>
</body>
</html>`

const revokeSuccessHTML = `<!DOCTYPE html>
<html>
<head>
    <title>Success</title>
    <style>
        body { font-family: sans-serif; max-width: 600px; margin: 50px auto; padding: 20px; }
    </style>
</head>
<body>
    <h1>Success</h1>
    <p>Delegation revoked from {{.Label}} for {{.Email}}.</p>
</body>
</html>`

const mailGrantSuccessHTML = `<!DOCTYPE html>
<html>
<head>
    <title>Success</title>
    <style>
        body { font-family: sans-serif; max-width: 600px; margin: 50px auto; padding: 20px; }
    </style>
</head>
<body>
    <h1>Success</h1>
    <p>{{.Email}} now accepts mail from user {{.Sender}}.</p>
</body>
</html>`

const mailRevokeSuccessHTML = `<!DOCTYPE html>
<html>
<head>
    <title>Success</title>
    <style>
        body { font-family: sans-serif; max-width: 600px; margin: 50px auto; padding: 20px; }
    </style>
</head>
<body>
    <h1>Success</h1>
    <p>{{.Email}} no longer accepts mail from user {{.Sender}}.</p>
</body>
</html>`
