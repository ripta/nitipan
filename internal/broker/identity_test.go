package broker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/space-armada/nitipan/internal/oidc"
	"github.com/space-armada/nitipan/internal/oidc/oidctest"
	"github.com/space-armada/nitipan/internal/wire"
)

// TestAnIssuerThatIsDownAtStartLeavesTheBrokerServing proves a broker whose identity provider cannot be
// reached when it starts still serves nodes, and its sign-in page says the provider is unavailable. It
// catches a discovery fetch at startup that fails the broker, so an outage of the provider takes leases and
// mail down with it, against the accepted risk that only new proofs wait.
func TestAnIssuerThatIsDownAtStartLeavesTheBrokerServing(t *testing.T) {
	down := httptest.NewServer(http.NotFoundHandler())
	issuer := down.URL
	down.Close()

	h := newHarness(t, func(c *Config) {
		c.OIDCConfig = &oidc.Config{Issuer: issuer, ClientID: "c", RedirectURL: "https://broker.test/callback"}
	})

	select {
	case <-h.b.Done():
		t.Fatalf("the broker stopped: %v", h.b.Err())
	default:
	}

	h.register(alice)

	if got := h.do(http.MethodPost, wire.PathPing, alice, wire.Ping{Version: wire.Current}).StatusCode; got != http.StatusOK {
		t.Fatalf("a node's ping answered %d", got)
	}

	resp := h.do(http.MethodPost, "/bind", nodeID{}, nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("starting a sign-in with the provider down answered %d, want %d", resp.StatusCode, http.StatusServiceUnavailable)
	}
}

type unbindTest struct {
	Name   string
	Unbind func(t *testing.T, w *authzWorld, labels ...string)
}

var unbindTests = []unbindTest{
	{Name: "forget", Unbind: func(t *testing.T, w *authzWorld, labels ...string) {
		for _, label := range labels {
			if rec := w.h.admin(http.MethodPost, AdminPathForget, map[string]string{"label": label}); rec.Code != http.StatusNoContent {
				t.Fatalf("forget %s: %d %s", label, rec.Code, rec.Body)
			}
		}
	}},
	{Name: "revoke-user", Unbind: func(t *testing.T, w *authzWorld, _ ...string) {
		if rec := w.h.admin(http.MethodPost, AdminPathRevokeUser, map[string]string{"user_id": w.v.ID}); rec.Code != http.StatusOK {
			t.Fatalf("revoke-user: %d %s", rec.Code, rec.Body)
		}
	}},
}

// TestAForgottenKeyThatRegistersAgainActsForNobody proves that once a label is forgotten, or its user
// revoked, its old key can register again but acts for nobody while cross-user features are on. It gets no
// claim in its old user's scope, no claim under a delegation the label held, no discovery, and no break of
// the old user's lease, and neither whoami nor the bindings list names the old user for it. It catches a
// forget that leaves the label's user in its binding, so a key that re-registers after the node record is
// gone acts as that user again.
func TestAForgottenKeyThatRegistersAgainActsForNobody(t *testing.T) {
	for _, test := range unbindTests {
		t.Run(test.Name, func(t *testing.T) {
			w := newAuthzWorld(t, true)

			laptop := nodeID{Machine: "machine-v-laptop", UID: 1004, Key: "key-v-laptop"}
			laptopLabel := w.bind(t, laptop, w.v.ID)

			w.claimAs(t, w.vNode, "L-v", wire.ReachUser, name("svc"), "v", w.v.UserLabel)

			test.Unbind(t, w, laptopLabel, w.agentLabel)

			w.h.register(laptop)
			w.h.register(w.agent)

			claims := []struct {
				n         nodeID
				actingFor string
			}{
				{laptop, w.v.UserLabel},
				{w.agent, w.u.UserLabel},
			}

			for _, c := range claims {
				status := w.h.call(c.n, wire.PathClaim, wire.Claim{
					Version: wire.Current, ID: "L-again-" + c.n.Machine, Reach: wire.ReachUser, Entries: name("other"),
					Derivation: 1, Anchor: "anchor", Holder: holder("again"), ActingFor: c.actingFor, ActingForExplicit: true,
				}, nil)
				if status != http.StatusForbidden {
					t.Errorf("%s claiming for %s after %s: status %d, want %d", c.n.Machine, c.actingFor, test.Name, status,
						http.StatusForbidden)
				}
			}

			if status := w.h.call(laptop, wire.PathList, wire.ListRequest{Version: wire.Current}, nil); status != http.StatusForbidden {
				t.Errorf("discovery from the forgotten key: status %d, want %d", status, http.StatusForbidden)
			}

			status := w.h.call(laptop, wire.PathBreak, wire.Break{
				Version: wire.Current, RequestID: "b1", ID: "L-v", Force: true, By: holder("breaker"),
			}, nil)
			if status != http.StatusForbidden {
				t.Errorf("breaking V's lease from the forgotten key: status %d, want %d", status, http.StatusForbidden)
			}

			var who wire.WhoAmIAnswer
			if status := w.h.call(laptop, wire.PathWhoAmI, wire.WhoAmI{Version: wire.Current}, &who); status == http.StatusOK && who.UserLabel != "" {
				t.Errorf("whoami from the forgotten key names %q", who.UserLabel)
			}

			for _, b := range adminBindings(t, w.h) {
				if (b.Label == laptopLabel || b.Label == w.agentLabel) && b.UserID == w.v.ID {
					t.Errorf("bindings still list %s under V after %s", b.Label, test.Name)
				}
			}
		})
	}
}

type listedBinding struct {
	Label  string `json:"label"`
	UserID string `json:"user_id"`
}

func adminBindings(t *testing.T, h *harness) []listedBinding {
	t.Helper()

	rec := h.admin(http.MethodGet, AdminPathBindings, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("bindings: %d %s", rec.Code, rec.Body)
	}

	var ans struct {
		Bindings []listedBinding `json:"bindings"`
	}

	if err := json.NewDecoder(rec.Body).Decode(&ans); err != nil {
		t.Fatal(err)
	}

	return ans.Bindings
}

// pageHarness is a broker whose sign-in page signs users in at a local issuer.
func pageHarness(t *testing.T, iss *oidctest.Issuer) (*harness, *oidctest.Browser) {
	t.Helper()

	cfg := iss.Config("https://broker.test/callback")
	h := newHarness(t, func(c *Config) { c.OIDCConfig = &cfg })

	return h, oidctest.NewBrowser(t, h.srv.URL, "", iss)
}

func (h *harness) enableCrossUser() *httptest.ResponseRecorder {
	h.t.Helper()

	return h.admin(http.MethodPost, AdminPathCrossUser, map[string]bool{"enable": true})
}

// TestThePageBindPinsTheKeyThatSentTheCode proves a pairing code binds only the key that sent it. A
// forgotten label has two pending keys: the old one, which registered again, and the new one from
// nitipan node rekey, which sent the code. The bind pins the new key, and the old one is refused. It
// catches a bind that pins whichever pending key it finds first, so a code another key sent can pin a
// key that crossed a wire the user no longer trusts.
func TestThePageBindPinsTheKeyThatSentTheCode(t *testing.T) {
	iss := oidctest.New(t, "v-sub", "v@example.com")
	h, page := pageHarness(t, iss)

	v, err := h.b.AdmitUser(iss.URL, "v-sub", "v@example.com", h.wall.Now())
	if err != nil {
		t.Fatal(err)
	}

	old := nodeID{Machine: "machine-v", UID: 501, Key: "old-key"}
	label := h.registerOver(old, true).Label

	if status, _ := h.pair(old, "alpha-bravo-charlie-delta"); status/100 != 2 {
		t.Fatalf("pairing the first key: status %d", status)
	}

	if status, body := page.Submit("bind", url.Values{"label": {label}, "code": {"alpha-bravo-charlie-delta"}}); status != http.StatusOK {
		t.Fatalf("binding the first key: %d %s", status, body)
	}

	if rec := h.enableCrossUser(); rec.Code/100 != 2 {
		t.Fatalf("turning cross-user features on: %d %s", rec.Code, rec.Body)
	}

	if rec := h.admin(http.MethodPost, AdminPathForget, map[string]string{"label": label}); rec.Code != http.StatusNoContent {
		t.Fatalf("forget: %d %s", rec.Code, rec.Body)
	}

	rekeyed := nodeID{Machine: old.Machine, UID: old.UID, Key: "new-key"}
	h.registerOver(rekeyed, true)

	status, paired := h.pair(rekeyed, "echo-foxtrot-golf-hotel")
	if status/100 != 2 {
		t.Fatalf("pairing the new key: status %d", status)
	}

	h.registerOver(old, true)

	if status, body := page.Submit("bind", url.Values{"label": {label}, "code": {"echo-foxtrot-golf-hotel"}}); status != http.StatusOK {
		t.Fatalf("binding the new key: %d %s", status, body)
	}

	var who wire.WhoAmIAnswer
	if status := h.call(rekeyed, wire.PathWhoAmI, wire.WhoAmI{Version: wire.Current}, &who); status != http.StatusOK || who.UserLabel != v.UserLabel {
		t.Fatalf("whoami from the key that sent the code: status %d, %+v, want %s", status, who, v.UserLabel)
	}

	if status := h.call(old, wire.PathPing, wire.Ping{Version: wire.Current}, nil); status != http.StatusUnauthorized {
		t.Fatalf("the old key after the bind: status %d, want %d", status, http.StatusUnauthorized)
	}

	if paired != label {
		t.Errorf("the pair answer names label %q, want %q", paired, label)
	}
}

type plainKeyTest struct {
	Name string

	// Then runs after a key registered over plain HTTP and was approved.
	Then func(h *harness, label string)

	Blocked bool
}

func approveKey(h *harness, label, key string) {
	h.t.Helper()

	rec := h.admin(http.MethodPost, AdminPathApprove, map[string]string{"label": label, "fingerprint": wire.Fingerprint(keyHash(key))})
	if rec.Code != http.StatusOK {
		h.t.Fatalf("approve %s: %d %s", label, rec.Code, rec.Body)
	}
}

func forgetLabel(h *harness, label string) {
	h.t.Helper()

	if rec := h.admin(http.MethodPost, AdminPathForget, map[string]string{"label": label}); rec.Code != http.StatusNoContent {
		h.t.Fatalf("forget %s: %d %s", label, rec.Code, rec.Body)
	}
}

var plainMachine = nodeID{Machine: "machine-plain", UID: 501, Key: "plain-key"}

var plainKeyTests = []plainKeyTest{
	{Name: "the key reports HTTPS later", Blocked: true, Then: func(h *harness, _ string) {
		h.registerOver(plainMachine, true)
	}},
	{Name: "the label is forgotten and approved again under the same key", Blocked: true, Then: func(h *harness, label string) {
		forgetLabel(h, label)
		h.registerOver(plainMachine, true)
		approveKey(h, label, plainMachine.Key)
	}},
	{Name: "the label is forgotten", Then: func(h *harness, label string) {
		forgetLabel(h, label)
	}},
	{Name: "the label is forgotten and approved again under a new key", Then: func(h *harness, label string) {
		forgetLabel(h, label)

		rekeyed := nodeID{Machine: plainMachine.Machine, UID: plainMachine.UID, Key: "fresh-key"}
		h.registerOver(rekeyed, true)
		approveKey(h, label, rekeyed.Key)
	}},
}

// TestAKeyThatCrossedPlainHTTPBlocksTheSwitchUntilItIsGone proves a key that once reported plain HTTP
// keeps turning cross-user features on refused, whatever it reports later and however often its label is
// approved, until the label is forgotten or approved under another key. It catches a scheme report that
// each registration overwrites, so a read key passes by reporting HTTPS once, and a mark that outlives a
// forgotten machine and blocks the switch for good.
func TestAKeyThatCrossedPlainHTTPBlocksTheSwitchUntilItIsGone(t *testing.T) {
	for _, test := range plainKeyTests {
		t.Run(test.Name, func(t *testing.T) {
			h := newHarness(t, func(c *Config) {
				c.OIDCConfig = &oidc.Config{Issuer: "https://issuer.test", ClientID: "c", RedirectURL: "https://broker.test/callback"}
			})

			label := h.registerOver(plainMachine, false).Label
			approveKey(h, label, plainMachine.Key)

			test.Then(h, label)

			rec := h.enableCrossUser()

			switch {
			case test.Blocked && rec.Code/100 == 2:
				t.Fatalf("turning cross-user features on succeeded, want a refusal naming %s", label)
			case test.Blocked && !strings.Contains(rec.Body.String(), "plain HTTP"):
				t.Fatalf("the refusal does not name plain HTTP: %s", rec.Body)
			case !test.Blocked && rec.Code/100 != 2:
				t.Fatalf("turning cross-user features on: %d %s", rec.Code, rec.Body)
			}
		})
	}
}

// TestTurningCrossUserOnAnswersWithTheUnboundLabels proves the admin answer to turning cross-user features
// on names every label nobody bound, and no bound one, since each is refused from then on. It catches an
// answer that sends the list only to the broker's log, where the owner running the command never sees it.
func TestTurningCrossUserOnAnswersWithTheUnboundLabels(t *testing.T) {
	h := newHarness(t, func(c *Config) {
		c.OIDCConfig = &oidc.Config{Issuer: "https://issuer.test", ClientID: "c", RedirectURL: "https://broker.test/callback"}
	})

	bound := h.registerOver(alice, true).Label
	approveKey(h, bound, alice.Key)

	unbound := h.registerOver(bob, true).Label

	rec := h.enableCrossUser()
	if rec.Code != http.StatusOK {
		t.Fatalf("turning cross-user features on: %d %s", rec.Code, rec.Body)
	}

	var ans struct {
		Unbound []string `json:"unbound_labels"`
	}

	if err := json.NewDecoder(rec.Body).Decode(&ans); err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(ans.Unbound, []string{unbound}) {
		t.Fatalf("unbound labels = %v, want [%s]", ans.Unbound, unbound)
	}
}

// TestARevokedUserCannotSubmitABindFormSignedInBefore proves a bind form a user signed in for is refused
// once the owner revokes that user, and binds nothing. The bind checks admission under the lock that makes
// the binding, as delegations and mail grants do. A revoke that lands between a check outside the lock and
// the bind cannot be timed without a hook inside the handler, so this test pins the rule. It does not
// reproduce the race.
func TestARevokedUserCannotSubmitABindFormSignedInBefore(t *testing.T) {
	iss := oidctest.New(t, "v-sub", "v@example.com")
	h, page := pageHarness(t, iss)

	v, err := h.b.AdmitUser(iss.URL, "v-sub", "v@example.com", h.wall.Now())
	if err != nil {
		t.Fatal(err)
	}

	n := nodeID{Machine: "machine-v", UID: 501, Key: "v-key"}
	label := h.registerOver(n, true).Label

	if status, _ := h.pair(n, "alpha-bravo-charlie-delta"); status/100 != 2 {
		t.Fatalf("pairing: status %d", status)
	}

	status, form := page.SignIn("bind")
	if status != http.StatusOK {
		t.Fatalf("signing in: %d %s", status, form)
	}

	if rec := h.admin(http.MethodPost, AdminPathRevokeUser, map[string]string{"user_id": v.ID}); rec.Code != http.StatusOK {
		t.Fatalf("revoke-user: %d %s", rec.Code, rec.Body)
	}

	status, body := page.Post("/bind-submit", url.Values{
		"token": {oidctest.FormToken(t, form)}, "label": {label}, "code": {"alpha-bravo-charlie-delta"},
	})
	if status != http.StatusForbidden {
		t.Fatalf("the revoked user's bind: %d %s, want %d", status, body, http.StatusForbidden)
	}

	for _, b := range adminBindings(t, h) {
		if b.Label == label {
			t.Fatalf("the revoked user's bind bound %s to %s", label, b.UserID)
		}
	}
}
