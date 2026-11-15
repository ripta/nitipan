package broker

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/space-armada/nitipan/internal/oidc"
	"github.com/space-armada/nitipan/internal/oidc/oidctest"
	"github.com/space-armada/nitipan/internal/wire"
)

type authedTest struct {
	Name      string
	Bound     bool
	CrossUser bool
	Status    int
}

var authedTests = []authedTest{
	{Name: "unbound with cross-user on", CrossUser: true, Status: http.StatusForbidden},
	{Name: "bound with cross-user on", Bound: true, CrossUser: true, Status: http.StatusOK},
	{Name: "unbound with cross-user off", Status: http.StatusOK},
}

// TestAuthedRefusesAnUnboundLabelOnlyOnceCrossUserIsOn proves a node call from a label nobody bound is
// refused, naming the approve step, once cross-user features are on, and served while they are off. A bound
// label is served either way. It catches an unbound-label check that runs before the switch, or never.
func TestAuthedRefusesAnUnboundLabelOnlyOnceCrossUserIsOn(t *testing.T) {
	for _, test := range authedTests {
		t.Run(test.Name, func(t *testing.T) {
			h := newHarness(t, func(c *Config) {
				c.OIDCConfig = &oidc.Config{Issuer: "https://test.example", ClientID: "test", RedirectURL: "https://broker.example/callback"}
			})

			label := h.registerOver(alice, true).Label

			if test.Bound {
				approveKey(h, label, alice.Key)
			}

			if test.CrossUser {
				if rec := h.enableCrossUser(); rec.Code != http.StatusOK {
					t.Fatalf("turning cross-user features on: %d %s", rec.Code, rec.Body)
				}
			}

			resp := h.do(http.MethodPost, wire.PathPing, alice, wire.Ping{Version: wire.Current})
			if resp.StatusCode != test.Status {
				t.Fatalf("ping: status %d, want %d", resp.StatusCode, test.Status)
			}

			if test.Status == http.StatusForbidden {
				var e wire.Error
				if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
					t.Fatal(err)
				}

				if e.Code != wire.CodeForbidden || !strings.Contains(e.Message, "nitipan broker approve") {
					t.Errorf("refusal %+v does not name the approve step", e)
				}
			}
		})
	}
}

// TestSetCrossUserEnabledRefusals proves turning cross-user features on is refused, naming the reason, with
// no identity provider, with a key that reported plain HTTP or never reported its scheme, and on a listen
// address that is neither TLS nor loopback. It catches a switch that skips any one precondition, which
// would expose node keys or the sign-in page to a network the owner does not trust.
func TestSetCrossUserEnabledRefusals(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*harness)
		listen  string
		tls     bool
		wantErr string
	}{
		{
			name:    "no OIDC configured",
			setup:   func(*harness) {},
			wantErr: "no identity provider configured",
		},
		{
			name: "label reported plain HTTP",
			setup: func(h *harness) {
				h.registerOver(alice, false)
			},
			wantErr: "reported plain HTTP",
		},
		{
			name: "label never reported scheme",
			setup: func(h *harness) {
				approveKey(h, h.registerOver(alice, true).Label, alice.Key)

				// A key a 1.x store held keeps this mark through the upgrade, whatever it reports later. The
				// mark is the precondition under test, and only a store from before scheme reports holds it, so
				// the test writes it into the store directly.
				h.store.Data().KeySchemes[keyHash(alice.Key)] = schemeUnreported
			},
			wantErr: "never reported scheme",
		},
		{
			name:    "private listen without TLS",
			setup:   func(*harness) {},
			listen:  "192.168.1.1:8080",
			tls:     false,
			wantErr: "requires TLS or a loopback address",
		},
		{
			name:    "public listen without TLS",
			setup:   func(*harness) {},
			listen:  "0.0.0.0:8080",
			tls:     false,
			wantErr: "requires TLS or a loopback address",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mutate := func(cfg *Config) {
				if tt.name != "no OIDC configured" {
					cfg.OIDCConfig = &oidc.Config{
						Issuer:       "https://test.example",
						ClientID:     "test",
						RedirectURL:  "https://broker.example/callback",
						ClientSecret: "secret",
					}
				}
				if tt.listen != "" {
					cfg.Listen = tt.listen
					cfg.TLS = tt.tls
				}
			}

			h := newHarness(t, mutate)
			tt.setup(h)

			_, err := h.b.SetCrossUserEnabled(true)
			if err == nil {
				t.Fatal("expected error, got nil")
			}

			if tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("expected error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

// TestSetCrossUserEnabledSuccess proves a fresh broker turns cross-user features on once every precondition
// holds, on loopback without TLS and on a public address with it. Its owner is the one its store assigned
// at open. It catches a fresh broker that can never turn cross-user features on because only an upgrade
// created an owner.
func TestSetCrossUserEnabledSuccess(t *testing.T) {
	tests := []struct {
		name   string
		listen string
		tls    bool
	}{
		{"loopback without TLS", "127.0.0.1:8080", false},
		{"public with TLS", "0.0.0.0:8443", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mutate := func(cfg *Config) {
				cfg.OIDCConfig = &oidc.Config{
					Issuer:       "https://test.example",
					ClientID:     "test",
					RedirectURL:  "https://broker.example/callback",
					ClientSecret: "secret",
				}
				cfg.Listen = tt.listen
				cfg.TLS = tt.tls
			}

			h := newHarness(t, mutate)
			approveKey(h, h.registerOver(alice, true).Label, alice.Key)

			if _, err := h.b.SetCrossUserEnabled(true); err != nil {
				t.Fatalf("SetCrossUserEnabled failed: %v", err)
			}

			if !h.data().CrossUserEnabled {
				t.Error("CrossUserEnabled not set")
			}
		})
	}
}

// TestRegisteredCarriesCrossUserFlag proves the registration answer says whether cross-user features are
// on. It catches an answer that omits the flag, which leaves a node that would refuse to talk to a broker
// serving several users unaware that it does.
func TestRegisteredCarriesCrossUserFlag(t *testing.T) {
	for _, enable := range []bool{false, true} {
		t.Run(fmt.Sprintf("cross-user %t", enable), func(t *testing.T) {
			h := newCrossUserHarness(t)

			approveKey(h, h.register(alice).Label, alice.Key)

			if enable {
				h.crossUserOn(t)
			}

			if got := h.register(bob).CrossUser; got != enable {
				t.Errorf("CrossUser = %v, want %v", got, enable)
			}
		})
	}
}

// TestTurningCrossUserOffRefusesEveryCoworkerLabelAndKeepsTheirState proves the broker serves only its
// owner's labels and unbound labels while cross-user features are off. A coworker can still bind on the
// page then, so setup happens before the switch, but every node-API call from the coworker's label is
// refused with the step that turns the features on. Turning them off again refuses the coworker once more
// and keeps the coworker's lease and delegation. It catches a switch that only gates the unbound-label
// check, so a coworker's label acts in its own scope, or in the owner's, on a broker meant to run one user.
func TestTurningCrossUserOffRefusesEveryCoworkerLabelAndKeepsTheirState(t *testing.T) {
	iss := oidctest.New(t, "v-sub", "v@example.com")
	h, page := pageHarness(t, iss)

	v, err := h.b.AdmitUser(iss.URL, "v-sub", "v@example.com", h.wall.Now())
	if err != nil {
		t.Fatal(err)
	}

	ownerLabel := h.registerOver(alice, true).Label
	approveKey(h, ownerLabel, alice.Key)

	coworker := nodeID{Machine: "machine-v", UID: 501, Key: "key-v"}
	coworkerLabel := h.registerOver(coworker, true).Label

	if status, _ := h.pair(coworker, "alpha-bravo-charlie-delta"); status/100 != 2 {
		t.Fatalf("pairing the coworker's key: status %d", status)
	}

	if status, body := page.Submit("bind", url.Values{"label": {coworkerLabel}, "code": {"alpha-bravo-charlie-delta"}}); status != http.StatusOK {
		t.Fatalf("the coworker's bind while the features are off: %d %s", status, body)
	}

	if got := h.claim(alice, "L-owner", wire.ReachUser, name("owner"), "a"); got.Code != wire.CodeGranted {
		t.Fatalf("the owner's claim while the features are off: %+v", got)
	}

	refusedEverywhere := func(when string) {
		t.Helper()

		calls := []struct {
			path string
			body any
		}{
			{wire.PathClaim, wire.Claim{Version: wire.Current, ID: "L-v2", Reach: wire.ReachUser, Entries: name("v2"),
				Derivation: 1, Anchor: "anchor", Holder: holder("v")}},
			{wire.PathList, wire.ListRequest{Version: wire.Current}},
			{wire.PathWhoHolds, wire.WhoHolds{Version: wire.Current}},
			{wire.PathBreak, wire.Break{Version: wire.Current, RequestID: "b1", ID: "L-owner", Force: true, By: holder("v")}},
			{wire.PathMailSend, wire.MailSend{Version: wire.Current}},
		}

		for _, c := range calls {
			resp := h.do(http.MethodPost, c.path, coworker, c.body)

			var e wire.Error
			if resp.StatusCode == http.StatusForbidden {
				if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
					t.Fatal(err)
				}
			}

			if resp.StatusCode != http.StatusForbidden || !strings.Contains(e.Message, "nitipan broker cross-user true") {
				t.Errorf("%s from the coworker %s: status %d, %+v, want a refusal naming the switch", c.path, when, resp.StatusCode, e)
			}
		}
	}

	refusedEverywhere("before the features are on")

	if rec := h.enableCrossUser(); rec.Code != http.StatusOK {
		t.Fatalf("turning cross-user features on: %d %s", rec.Code, rec.Body)
	}

	if got := h.claim(coworker, "L-v", wire.ReachUser, name("v"), "v"); got.Code != wire.CodeGranted {
		t.Fatalf("the coworker's claim once the features are on: %+v", got)
	}

	if _, err := h.b.GrantDelegation(iss.URL, "v-sub", ownerLabel, h.wall.Now()); err != nil {
		t.Fatal(err)
	}

	if rec := h.admin(http.MethodPost, AdminPathCrossUser, map[string]bool{"enable": false}); rec.Code/100 != 2 {
		t.Fatalf("turning cross-user features off: %d %s", rec.Code, rec.Body)
	}

	refusedEverywhere("after the features are off again")

	if got := h.claim(alice, "L-owner2", wire.ReachUser, name("owner2"), "a"); got.Code != wire.CodeGranted {
		t.Errorf("the owner's claim after the features are off again: %+v", got)
	}

	h.b.mu.Lock()
	lease := h.b.g.grants["L-v"]
	live := lease != nil && lease.State == wire.StateLive
	h.b.mu.Unlock()

	if !live {
		t.Error("turning the features off ended the coworker's lease")
	}

	d := h.data()

	if _, ok := d.Delegations[v.ID][ownerLabel]; !ok {
		t.Error("turning the features off dropped the coworker's delegation")
	}

	if d.Bindings[coworkerLabel].UserID != v.ID {
		t.Errorf("turning the features off changed the coworker's binding to %+v", d.Bindings[coworkerLabel])
	}
}
