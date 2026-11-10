package broker

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/oidc/oidctest"
	"github.com/space-armada/nitipan/internal/repoid"
	"github.com/space-armada/nitipan/internal/wire"
)

const (
	pageSub   = "page-sub"
	pageEmail = "page@example.com"
	pageCode  = "alpha-bravo-charlie-delta"
)

// newPageWorld is a broker with the sign-in page whose issuer signs in pageSub. admit says whether that
// user is admitted first.
func newPageWorld(t *testing.T, admit bool) (*harness, *oidctest.Issuer, *oidctest.Browser, UserRecord) {
	t.Helper()

	iss := oidctest.New(t, pageSub, pageEmail)
	h, page := pageHarness(t, iss)

	var user UserRecord

	if admit {
		var err error

		if user, err = h.b.AdmitUser(iss.URL, pageSub, pageEmail, h.wall.Now()); err != nil {
			t.Fatal(err)
		}
	}

	return h, iss, page, user
}

// pairedLabel registers n and sends code through the pair route, as nitipan node pair does. It returns
// the label the code binds.
func pairedLabel(t *testing.T, h *harness, n nodeID, code string) string {
	t.Helper()

	h.register(n)

	status, label := h.pair(n, code)
	if status != http.StatusOK {
		t.Fatalf("pairing %s: status %d", n.Machine, status)
	}

	return label
}

// callbackQuery starts a sign-in at startPath and follows the issuer's redirect, and returns the query
// the issuer sends back to the broker's callback. A test then adds to it what a cross-site request would.
func callbackQuery(t *testing.T, h *harness, iss *oidctest.Issuer, startPath string) string {
	t.Helper()

	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	resp, err := noRedirect.PostForm(h.srv.URL+startPath, nil)
	if err != nil {
		t.Fatal(err)
	}

	resp.Body.Close()

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("starting %s: status %d", startPath, resp.StatusCode)
	}

	resp, err = iss.Client(t).Get(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}

	resp.Body.Close()

	back, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}

	return back.RawQuery
}

const wantCSP = "frame-ancestors 'none'"

// TestEveryPageAnswerForbidsFraming proves the sign-in page, the callback's form, and a bind submit each
// carry a CSP that forbids framing. It catches a page answer that leaves the CSP off, so another site can
// frame the form and trick a signed-in user into submitting it.
func TestEveryPageAnswerForbidsFraming(t *testing.T) {
	h, _, page, _ := newPageWorld(t, true)

	label := pairedLabel(t, h, nodeID{Machine: "machine-p", UID: 501, Key: "key-p"}, pageCode)

	if status, body := page.Get("/signin"); status != http.StatusOK || page.Header.Get("Content-Security-Policy") != wantCSP {
		t.Errorf("sign-in page: status %d, CSP %q: %s", status, page.Header.Get("Content-Security-Policy"), body)
	}

	status, form := page.SignIn("bind")
	if status != http.StatusOK || page.Header.Get("Content-Security-Policy") != wantCSP {
		t.Errorf("callback: status %d, CSP %q", status, page.Header.Get("Content-Security-Policy"))
	}

	page.Post("/bind-submit", url.Values{"token": {oidctest.FormToken(t, form)}, "label": {label}, "code": {"wrong-code"}})

	if got := page.Header.Get("Content-Security-Policy"); got != wantCSP {
		t.Errorf("bind submit CSP = %q, want %q", got, wantCSP)
	}
}

// TestAnUnadmittedSignInRecordsAPendingAdmission proves a sign-in by a user nobody admitted shows the
// pending-admission page, admits nobody, and records the issuer, sub, and email for the owner to admit.
// It catches a callback that admits anyone the identity provider signs in, and one that drops the request
// so the owner never sees it.
func TestAnUnadmittedSignInRecordsAPendingAdmission(t *testing.T) {
	h, iss, page, _ := newPageWorld(t, false)

	status, body := page.SignIn("bind")
	if status != http.StatusOK || !strings.Contains(body, "Pending Admission") {
		t.Fatalf("an unadmitted sign-in: status %d: %s", status, body)
	}

	d := h.data()

	if _, ok := d.IssuerSubs[issuerSubKey(iss.URL, pageSub)]; ok {
		t.Error("the unadmitted user was admitted")
	}

	found := false

	for _, pa := range d.PendingAdmissions {
		if pa.Issuer == iss.URL && pa.Sub == pageSub && pa.Email == pageEmail {
			found = true
		}
	}

	if !found {
		t.Errorf("pending admissions = %+v, want one for %s", d.PendingAdmissions, pageSub)
	}
}

// TestAFormTokenIsSingleUse proves a form token binds once and is refused when submitted again, even for
// another paired label. It catches a token kept after its first use, which lets a replayed form act a
// second time.
func TestAFormTokenIsSingleUse(t *testing.T) {
	h, _, page, _ := newPageWorld(t, true)

	first := pairedLabel(t, h, nodeID{Machine: "machine-1", UID: 501, Key: "key-1"}, pageCode)
	second := pairedLabel(t, h, nodeID{Machine: "machine-2", UID: 501, Key: "key-2"}, "echo-foxtrot-golf-hotel")

	_, form := page.SignIn("bind")
	token := oidctest.FormToken(t, form)

	if status, body := page.Post("/bind-submit", url.Values{"token": {token}, "label": {first}, "code": {pageCode}}); status != http.StatusOK {
		t.Fatalf("the first submit: %d %s", status, body)
	}

	status, _ := page.Post("/bind-submit", url.Values{"token": {token}, "label": {second}, "code": {"echo-foxtrot-golf-hotel"}})
	if status == http.StatusOK {
		t.Error("the token was accepted a second time")
	}

	if got := h.data().Bindings[second].UserID; got != "" {
		t.Errorf("the reused token bound %s to %q", second, got)
	}
}

// TestACallbackWithAnUnknownStateIsRefused proves the callback refuses a state the broker never issued.
// It catches a callback that skips the state check, so a cross-site request can finish a sign-in it
// never started.
func TestACallbackWithAnUnknownStateIsRefused(t *testing.T) {
	_, _, page, _ := newPageWorld(t, true)

	if status, _ := page.Get("/callback?code=test-code&state=wrong-state"); status == http.StatusOK {
		t.Error("a callback with an unknown state was accepted")
	}
}

// TestAnIDTokenWithTheWrongNonceIsRefused proves the callback refuses an ID token whose nonce is not the
// one its sign-in sent. It catches a callback that skips the nonce check, so a token replayed from another
// sign-in signs the user in.
func TestAnIDTokenWithTheWrongNonceIsRefused(t *testing.T) {
	_, iss, page, _ := newPageWorld(t, true)

	iss.ForgeNonce("a-nonce-from-another-sign-in")

	if status, _ := page.SignIn("bind"); status == http.StatusOK {
		t.Error("an ID token with the wrong nonce was accepted")
	}
}

// TestACrossSiteBindWithoutATokenChangesNothing proves a bind submit with a token the page never issued
// binds nothing, even with the right label and code. It catches a submit that trusts its form fields
// without the token that proves a sign-in.
func TestACrossSiteBindWithoutATokenChangesNothing(t *testing.T) {
	h, _, page, _ := newPageWorld(t, true)

	label := pairedLabel(t, h, nodeID{Machine: "machine-p", UID: 501, Key: "key-p"}, pageCode)

	if status, _ := page.Post("/bind-submit", url.Values{"token": {"fake-token"}, "label": {label}, "code": {pageCode}}); status == http.StatusOK {
		t.Error("a bind with a fake token was accepted")
	}

	if got := h.data().Bindings[label].UserID; got != "" {
		t.Errorf("a cross-site bind bound %s to %q", label, got)
	}
}

// TestTheRightLabelAndCodeBindAndPinTheKey proves the right label and code bind the label to the
// signed-in user, pin the key that sent the code, and clear only that label's code. It catches a bind that
// leaves the key pending, keeps a spent code that a second signer could use, or clears another label's
// code.
func TestTheRightLabelAndCodeBindAndPinTheKey(t *testing.T) {
	h, _, page, user := newPageWorld(t, true)

	n := nodeID{Machine: "machine-p", UID: 501, Key: "key-p"}
	label := pairedLabel(t, h, n, pageCode)
	otherLabel := pairedLabel(t, h, nodeID{Machine: "machine-o", UID: 502, Key: "key-o"}, "echo-foxtrot-golf-hotel")

	if status, body := page.Submit("bind", url.Values{"label": {label}, "code": {pageCode}}); status != http.StatusOK {
		t.Fatalf("bind: %d %s", status, body)
	}

	d := h.data()

	if got := d.Bindings[label].UserID; got != user.ID {
		t.Errorf("%s is bound to %q, want %s", label, got, user.ID)
	}

	if got := d.Pins[label].PinnedHash; got != keyHash(n.Key) {
		t.Errorf("pin = %q, want the key that sent the code", got)
	}

	if d.PairingCodes[label].Hash != "" {
		t.Error("the spent pairing code was kept")
	}

	if d.PairingCodes[otherLabel].Hash == "" {
		t.Error("the other label's pairing code was cleared")
	}
}

type refusedCodeTest struct {
	Name string

	// Code is what the signer types. Wait is how far the broker's clock moves after the pairing.
	Code string
	Wait time.Duration
}

var refusedCodeTests = []refusedCodeTest{
	{Name: "wrong code", Code: "wrong-code-here-now"},
	{Name: "expired code", Code: pageCode, Wait: PairingCodeExpiry + time.Second},
}

// TestAWrongOrExpiredCodeBindsNothing proves a wrong code, and the right code past its expiry by the
// broker's clock, bind nothing. It catches a code check that compares the wrong field, or that ignores the
// expiry or reads it from the process clock.
func TestAWrongOrExpiredCodeBindsNothing(t *testing.T) {
	for _, test := range refusedCodeTests {
		t.Run(test.Name, func(t *testing.T) {
			h, _, page, _ := newPageWorld(t, true)

			label := pairedLabel(t, h, nodeID{Machine: "machine-p", UID: 501, Key: "key-p"}, pageCode)
			h.wall.Set(h.wall.Now().Add(test.Wait))

			if status, _ := page.Submit("bind", url.Values{"label": {label}, "code": {test.Code}}); status == http.StatusOK {
				t.Error("the bind was accepted")
			}

			if got := h.data().Bindings[label].UserID; got != "" {
				t.Errorf("%s is bound to %q", label, got)
			}
		})
	}
}

// TestWrongCodesLockOutOnlyTheSignerWhoTypedThem proves MaxWrongPairingAttempts wrong codes lock that
// signer out of the label, so even the right code is then refused, while the code itself stays for
// another signer, who binds with it. It catches a check with no lockout, which lets a signer guess codes,
// and one that counts every signer's attempts together, so one user's guesses lock out the machine's real
// owner.
func TestWrongCodesLockOutOnlyTheSignerWhoTypedThem(t *testing.T) {
	h, iss, page, guesser := newPageWorld(t, true)

	label := pairedLabel(t, h, nodeID{Machine: "machine-p", UID: 501, Key: "key-p"}, pageCode)

	for i := range MaxWrongPairingAttempts {
		if status, _ := page.Submit("bind", url.Values{"label": {label}, "code": {fmt.Sprintf("wrong-code-%d", i)}}); status == http.StatusOK {
			t.Fatalf("wrong code %d was accepted", i)
		}
	}

	d := h.data()

	if got := d.PairingCodes[label].WrongAttempts[guesser.ID]; got < MaxWrongPairingAttempts {
		t.Errorf("wrong attempts recorded = %d, want %d", got, MaxWrongPairingAttempts)
	}

	if status, _ := page.Submit("bind", url.Values{"label": {label}, "code": {pageCode}}); status == http.StatusOK {
		t.Fatal("the right code was accepted after the lockout")
	}

	if after := h.data(); after.Bindings[label].UserID != "" || after.PairingCodes[label].Hash == "" {
		t.Fatalf("after the lockout: binding %+v, code %+v, want no binding and the code kept", after.Bindings[label],
			after.PairingCodes[label])
	}

	owner, err := h.b.AdmitUser(iss.URL, "owner-sub", "owner@example.com", h.wall.Now())
	if err != nil {
		t.Fatal(err)
	}

	iss.SetUser("owner-sub", "owner@example.com")

	if status, body := page.Submit("bind", url.Values{"label": {label}, "code": {pageCode}}); status != http.StatusOK {
		t.Fatalf("another signer's bind with the right code: %d %s", status, body)
	}

	if got := h.data().Bindings[label].UserID; got != owner.ID {
		t.Errorf("%s is bound to %q, want the second signer %s", label, got, owner.ID)
	}
}

// TestThePageNeverListsPendingLabels proves the form after a sign-in names no label waiting for a code.
// It catches a form that lists pending labels, which tells any admitted user which machines are waiting
// and lets them pick one to guess codes for.
func TestThePageNeverListsPendingLabels(t *testing.T) {
	h, _, page, _ := newPageWorld(t, true)

	label := pairedLabel(t, h, nodeID{Machine: "machine-p", UID: 501, Key: "key-p"}, pageCode)

	if _, body := page.SignIn("bind"); strings.Contains(body, label) {
		t.Errorf("the form names the pending label %s", label)
	}
}

// TestAnEmailWithAScriptTagIsEscaped proves the form escapes the email the identity provider sends. It
// catches a page that renders the claim as raw HTML, which runs a script in the broker's origin.
func TestAnEmailWithAScriptTagIsEscaped(t *testing.T) {
	email := "<script>alert('xss')</script>@example.com"

	iss := oidctest.New(t, pageSub, email)
	h, page := pageHarness(t, iss)

	if _, err := h.b.AdmitUser(iss.URL, pageSub, email, h.wall.Now()); err != nil {
		t.Fatal(err)
	}

	_, body := page.SignIn("bind")

	if strings.Contains(body, "<script>") || !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("the form does not escape the email:\n%s", body)
	}
}

// TestASignInResolvesTheUserByIssuerAndSubNotEmail proves a user whose email changed at the provider is
// still the same user, and keeps their binding. It catches a sign-in resolved by email, which loses the
// user on an email change and lets a recycled address take over another user's machines.
func TestASignInResolvesTheUserByIssuerAndSubNotEmail(t *testing.T) {
	h, iss, page, user := newPageWorld(t, true)

	label := pairedLabel(t, h, nodeID{Machine: "machine-p", UID: 501, Key: "key-p"}, pageCode)

	if status, body := page.Submit("bind", url.Values{"label": {label}, "code": {pageCode}}); status != http.StatusOK {
		t.Fatalf("bind: %d %s", status, body)
	}

	iss.SetUser(pageSub, "changed@example.com")

	status, body := page.SignIn("bind")
	if status != http.StatusOK || !strings.Contains(body, "changed@example.com") || strings.Contains(body, "Pending Admission") {
		t.Fatalf("sign-in after the email change: status %d: %s", status, body)
	}

	d := h.data()

	if got := d.IssuerSubs[issuerSubKey(iss.URL, pageSub)]; got != user.ID {
		t.Errorf("the issuer and sub resolve to %q, want %s", got, user.ID)
	}

	if got := d.Bindings[label].UserID; got != user.ID {
		t.Errorf("%s is bound to %q after the email change, want %s", label, got, user.ID)
	}
}

// TestParametersSentBeforeTheSignInAreIgnored proves the bind form after a sign-in echoes no parameter
// sent with the start of the sign-in or added to the callback. It catches a page that carries a cross-site
// request's parameters into the form, where a user might submit them unread.
func TestParametersSentBeforeTheSignInAreIgnored(t *testing.T) {
	h, iss, page, _ := newPageWorld(t, true)

	q := callbackQuery(t, h, iss, "/bind?evil=parameter&another=bad")

	_, body := page.Get("/callback?" + q + "&evil=injected")

	if strings.Contains(body, "evil") || strings.Contains(body, "parameter") || strings.Contains(body, "injected") {
		t.Errorf("the form echoes a parameter sent before the sign-in:\n%s", body)
	}
}

// TestADelegationFormStartsEmpty proves the grant form after a sign-in carries no label sent with the
// callback. It catches a form prefilled from a cross-site request, which turns one click into a
// delegation the user never chose.
func TestADelegationFormStartsEmpty(t *testing.T) {
	h, iss, page, _ := newPageWorld(t, true)

	label := h.register(nodeID{Machine: "machine-p", UID: 501, Key: "key-p"}).Label

	_, body := page.Get("/callback?" + callbackQuery(t, h, iss, "/grant") + "&label=" + label)

	if !strings.Contains(body, `<input type="text" name="label"`) || strings.Contains(body, label) {
		t.Errorf("the grant form is not empty:\n%s", body)
	}
}

// TestACrossSiteGrantWithoutATokenChangesNothing proves a grant submit with a token the page never issued
// makes no delegation. It catches a submit that trusts its form fields without the token that proves a
// sign-in.
func TestACrossSiteGrantWithoutATokenChangesNothing(t *testing.T) {
	h, _, page, user := newPageWorld(t, true)

	label := h.register(nodeID{Machine: "machine-p", UID: 501, Key: "key-p"}).Label

	if status, _ := page.Post("/grant-submit", url.Values{"token": {"fake-token"}, "label": {label}}); status == http.StatusOK {
		t.Error("a grant with a fake token was accepted")
	}

	if _, ok := h.data().Delegations[user.ID][label]; ok {
		t.Error("a cross-site grant made a delegation")
	}
}

// TestAGrantFromASignerRevokedAfterSignInIsRefused proves a grant form submitted after the owner revoked
// its signer is refused as not admitted, and makes no delegation. An unadmitted signer never gets a form,
// so a revoke between the sign-in and the submit is the only way to reach the submit's admission check. It
// catches a submit that trusts the sign-in alone.
func TestAGrantFromASignerRevokedAfterSignInIsRefused(t *testing.T) {
	h, _, page, user := newPageWorld(t, true)

	label := h.register(nodeID{Machine: "machine-p", UID: 501, Key: "key-p"}).Label

	_, form := page.SignIn("grant")

	if rec := h.admin(http.MethodPost, AdminPathRevokeUser, map[string]string{"user_id": user.ID}); rec.Code != http.StatusOK {
		t.Fatalf("revoke-user: %d %s", rec.Code, rec.Body)
	}

	status, body := page.Post("/grant-submit", url.Values{"token": {oidctest.FormToken(t, form)}, "label": {label}})
	if status != http.StatusForbidden || !strings.Contains(body, "not admitted") {
		t.Errorf("the revoked signer's grant: %d %s, want %d naming admission", status, body, http.StatusForbidden)
	}

	if len(h.data().Delegations[user.ID]) != 0 {
		t.Errorf("delegations = %+v, want none", h.data().Delegations)
	}
}

// TestAGrantOnThePageMakesTheDelegation proves the grant form, submitted after a sign-in, delegates the
// signer to the label typed. It catches a grant action that answers success and writes nothing, or writes
// the delegation under another user.
func TestAGrantOnThePageMakesTheDelegation(t *testing.T) {
	h, _, page, user := newPageWorld(t, true)

	label := h.register(nodeID{Machine: "machine-p", UID: 501, Key: "key-p"}).Label

	if status, body := page.Submit("grant", url.Values{"label": {label}}); status != http.StatusOK {
		t.Fatalf("grant: %d %s", status, body)
	}

	if _, ok := h.data().Delegations[user.ID][label]; !ok {
		t.Errorf("delegations = %+v, want %s's to %s", h.data().Delegations, user.ID, label)
	}
}

// TestARevokeOnThePageEndsTheDelegationsLeases proves the revoke form, submitted after a sign-in, removes
// the signer's delegation and ends the lease the label held for the signer. It is the page's own proof of
// the revoke. The broker-side effects of a revoke are proven by TestRevokingADelegationTellsTheAgentsNode.
// It catches a page revoke that removes the delegation and leaves its lease live.
func TestARevokeOnThePageEndsTheDelegationsLeases(t *testing.T) {
	h, _, page, user := newPageWorld(t, true)

	other, err := h.b.AdmitUser(createIssuer, "other@example.com", "other@example.com", h.wall.Now())
	if err != nil {
		t.Fatal(err)
	}

	n := nodeID{Machine: "machine-p", UID: 501, Key: "key-p"}
	h.register(n)
	label := h.bindNode(t, n, other.ID)

	h.crossUserOn(t)

	if status, body := page.Submit("grant", url.Values{"label": {label}}); status != http.StatusOK {
		t.Fatalf("grant: %d %s", status, body)
	}

	var res wire.Result

	status := h.call(n, wire.PathClaim, wire.Claim{
		Version: wire.Current, ID: "L1", Reach: wire.ReachUser,
		Entries:    []repoid.Name{repoid.New([]string{rootA}, "svc", false, false)},
		Derivation: 1, Anchor: "anchor-L1", Holder: holder("a"), ActingFor: user.UserLabel, ActingForExplicit: true,
	}, &res)
	if status != http.StatusOK || res.Code != wire.CodeGranted {
		t.Fatalf("claim for the signer: status %d, %+v", status, res)
	}

	if status, body := page.Submit("revoke", url.Values{"label": {label}}); status != http.StatusOK {
		t.Fatalf("revoke: %d %s", status, body)
	}

	if _, ok := h.data().Delegations[user.ID][label]; ok {
		t.Error("the delegation outlived the revoke")
	}

	ended := false

	for _, l := range h.b.Facts() {
		if l.ID == "L1" && l.State == wire.StateEnded && l.EndedBy == "delegation revoked" {
			ended = true
		}
	}

	if !ended {
		t.Errorf("facts = %+v, want L1 ended by the revoke", h.b.Facts())
	}
}
