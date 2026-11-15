package broker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/oidc/oidctest"
	"github.com/space-armada/nitipan/internal/repoid"
	"github.com/space-armada/nitipan/internal/wire"
)

// TestDelegationAllowsActingFor proves a label's explicit acting_for naming another user is honored once
// that user delegates to the label, and that before the delegation an old-version claim naming the user
// acts for the label's own user. It catches a delegation check that never passes, and one that passes
// before the grant.
func TestDelegationAllowsActingFor(t *testing.T) {
	h := newCrossUserHarness(t)

	now := time.Now()

	user1, err := h.b.AdmitUser(createIssuer, "user1@example.com", "user1@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	user2, err := h.b.AdmitUser(createIssuer, "user2@example.com", "user2@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	bob := nodeID{Machine: "machine2", UID: 1001, Key: "key2"}
	h.register(bob)
	labelBob := h.bindNode(t, bob, user2.ID)

	h.crossUserOn(t)

	entries := []repoid.Name{repoid.New([]string{rootA}, "svc", false, false)}

	var res1 wire.Result
	status := h.call(bob, wire.PathClaim, wire.Claim{
		Version: wire.Current, ID: "L1", Reach: wire.ReachUser, Entries: entries, Derivation: 1,
		Anchor: "anchor-L1", Holder: holder("b"), ActingFor: user1.UserLabel,
	}, &res1)
	if status != http.StatusOK {
		t.Fatalf("bob claim for user1 without delegation: status %d", status)
	}
	if res1.Code != wire.CodeGranted {
		t.Fatalf("bob claim without delegation should be granted (acting_for substituted): %+v", res1)
	}
	if res1.Lease.ActingFor == user1.UserLabel {
		t.Fatalf("acting_for should be substituted to user2 without delegation, got %q", res1.Lease.ActingFor)
	}
	if res1.Lease.ActingFor != user2.UserLabel {
		t.Fatalf("acting_for should be user2, got %q", res1.Lease.ActingFor)
	}

	status = h.call(bob, wire.PathRelease, wire.Release{
		Version: wire.Current, RequestID: "release-L1", ID: "L1", Holder: holder("b"),
	}, nil)
	if status != http.StatusOK {
		t.Fatalf("release L1: status %d", status)
	}

	h.delegate(t, user1, labelBob)

	var res2 wire.Result
	status = h.call(bob, wire.PathClaim, wire.Claim{
		Version: wire.Current, ID: "L2", Reach: wire.ReachUser, Entries: entries, Derivation: 1,
		Anchor: "anchor-L2", Holder: holder("b2"), ActingFor: user1.UserLabel, ActingForExplicit: true,
	}, &res2)
	if status != http.StatusOK {
		t.Fatalf("bob claim for user1 with delegation: status %d", status)
	}
	if res2.Code != wire.CodeGranted {
		t.Fatalf("bob claim for user1 should be granted with delegation: %+v", res2)
	}
	if res2.Lease.ActingFor != user1.UserLabel {
		t.Fatalf("lease should have acting_for set to user1, got %q, expected %q", res2.Lease.ActingFor, user1.UserLabel)
	}
}

// TestNewVersionClaimWithoutDelegationRefused proves an explicit acting_for naming another user, without
// that user's delegation, is refused with an error that names the delegation and the sign-in page. It
// catches a broker that substitutes an explicit acting_for silently, which hides the missing delegation.
func TestNewVersionClaimWithoutDelegationRefused(t *testing.T) {
	h := newCrossUserHarness(t)

	now := time.Now()

	user1, err := h.b.AdmitUser(createIssuer, "user1@example.com", "user1@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	user2, err := h.b.AdmitUser(createIssuer, "user2@example.com", "user2@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	bob := nodeID{Machine: "machine2", UID: 1001, Key: "key2"}
	h.register(bob)
	h.bindNode(t, bob, user2.ID)

	h.crossUserOn(t)

	entries := []repoid.Name{repoid.New([]string{rootA}, "svc", false, false)}

	resp := h.do(http.MethodPost, wire.PathClaim, bob, wire.Claim{
		Version: wire.Current, ID: "L1", Reach: wire.ReachUser, Entries: entries, Derivation: 1,
		Anchor: "anchor-L1", Holder: holder("b"), ActingFor: user1.UserLabel, ActingForExplicit: true,
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("new-version claim without delegation: status %d, want 403", resp.StatusCode)
	}

	var e wire.Error
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		t.Fatalf("decoding error response: %v", err)
	}

	if e.Code != wire.CodeForbidden {
		t.Fatalf("error code = %q, want %q", e.Code, wire.CodeForbidden)
	}
	if !strings.Contains(e.Message, "delegation") || !strings.Contains(e.Message, "sign-in page") {
		t.Fatalf("refusal message should mention delegation and sign-in page, got: %s", e.Message)
	}
}

// delegationProbe is one request a node might make to grant or revoke a delegation.
type delegationProbe struct {
	Method, Path string
	Form         bool
}

// delegationProbePaths are the sign-in page's routes and every node route, plus node routes a
// delegation call would plausibly live at.
var delegationProbePaths = []string{
	"/signin", "/bind", "/grant", "/revoke", "/callback", "/bind-submit", "/grant-submit", "/revoke-submit",
	"/mail-grant", "/mail-revoke", "/mail-grant-submit", "/mail-revoke-submit",
	wire.PathRegister, wire.PathStream, wire.PathAck, wire.PathSnapshot, wire.PathHeartbeat, wire.PathPing, wire.PathPair,
	wire.PathClaim, wire.PathRenew, wire.PathRelease, wire.PathBreak, wire.PathList, wire.PathFence, wire.PathWhoHolds,
	wire.PathWhoIs, wire.PathWhoAmI, wire.PathMailSend, wire.PathMailAck, wire.PathMailReport, wire.PathMailStatus,
	wire.Prefix + "/grant", wire.Prefix + "/revoke", wire.Prefix + "/delegate", wire.Prefix + "/delegation",
	wire.Prefix + "/delegations", wire.Prefix + "/grant-delegation", wire.Prefix + "/revoke-delegation",
	wire.Prefix + "/mail/grant", wire.Prefix + "/mail/revoke", wire.Prefix + "/mail-grant", wire.Prefix + "/mail-revoke",
}

func delegationProbes() []delegationProbe {
	var out []delegationProbe

	for _, p := range delegationProbePaths {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			out = append(out, delegationProbe{Method: method, Path: p}, delegationProbe{Method: method, Path: p, Form: true})
		}
	}

	return out
}

// probe sends one request with a node's bearer key and no identity proof. It does not follow redirects,
// so a page route's hand-off to the identity provider stops at the broker.
func (h *harness) probe(p delegationProbe, n nodeID, fields map[string]string) {
	h.t.Helper()

	var (
		body        string
		contentType string
	)

	if p.Form {
		v := url.Values{}
		for k, val := range fields {
			v.Set(k, val)
		}

		body, contentType = v.Encode(), "application/x-www-form-urlencoded"
	} else {
		data, err := json.Marshal(fields)
		if err != nil {
			h.t.Fatal(err)
		}

		body, contentType = string(data), "application/json"
	}

	req, err := http.NewRequest(p.Method, h.srv.URL+p.Path, strings.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}

	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+n.Key)
	req.Header.Set(wire.HeaderMachine, n.Machine)
	req.Header.Set(wire.HeaderUID, strconv.Itoa(n.UID))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	// The stream route holds its response open, so the probe reads only the status and cancels.
	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		h.t.Fatalf("%s %s: %v", p.Method, p.Path, err)
	}

	_ = resp.Body.Close()
}

// TestNoNetworkRouteChangesADelegationOrAMailGrantWithoutAnIdentityProof proves the broker's network
// handler, sign-in page included, grants and revokes no delegation and no mail grant for a caller holding
// only a node key. Every handoff kind reaches the broker through one of these node routes, so no request
// through the handoff changes either. It catches a node route, or a page route that trusts a node key or
// a form field, that changes a delegation or a mail grant without the user's identity proof. The page's
// own actions with a proof are proven by the sign-in page tests.
func TestNoNetworkRouteChangesADelegationOrAMailGrantWithoutAnIdentityProof(t *testing.T) {
	h, _ := pageHarness(t, oidctest.New(t, "u-sub", "u@example.com"))

	now := time.Now()

	u, err := h.b.AdmitUser(createIssuer, "u@example.com", "u@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	v, err := h.b.AdmitUser(createIssuer, "v@example.com", "v@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	uNode := nodeID{Machine: "machine-u", UID: 1000, Key: "key-u"}
	vNode := nodeID{Machine: "machine-v", UID: 1001, Key: "key-v"}
	keptNode := nodeID{Machine: "machine-kept", UID: 1002, Key: "key-kept"}

	labels := map[nodeID]string{}

	for n, userID := range map[nodeID]string{uNode: u.ID, vNode: v.ID, keptNode: v.ID} {
		h.register(n)
		labels[n] = h.bindNode(t, n, userID)
	}

	h.delegate(t, u, labels[keptNode])

	kept, err := h.b.AdmitUser(createIssuer, "kept@example.com", "kept@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := h.b.GrantMail(testDelegationIssuer, u.ID, kept.UserLabel, now); err != nil {
		t.Fatal(err)
	}

	for _, p := range delegationProbes() {
		for _, n := range []nodeID{uNode, vNode} {
			for _, target := range []string{labels[vNode], labels[keptNode]} {
				h.probe(p, n, map[string]string{
					"user_id": u.ID, "user": u.UserLabel, "user_label": u.UserLabel, "email": "u@example.com",
					"label": target, "token": "not-a-token", "action": "grant",
				})
			}

			for _, sender := range []UserRecord{v, kept} {
				h.probe(p, n, map[string]string{
					"user_id": u.ID, "user": u.UserLabel, "user_label": sender.UserLabel, "sender": sender.UserLabel,
					"email": "u@example.com", "token": "not-a-token", "action": actionMailGrant, "from": sender.ID, "to": u.ID,
				})
			}
		}
	}

	d := h.data()
	if _, ok := d.Delegations[u.ID][labels[vNode]]; ok {
		t.Errorf("a node-key request granted U's delegation to V's label: %v", d.Delegations)
	}

	if _, ok := d.Delegations[u.ID][labels[keptNode]]; !ok {
		t.Errorf("a node-key request revoked U's delegation to the kept label: %v", d.Delegations)
	}

	if len(d.Delegations[v.ID]) != 0 {
		t.Errorf("a node-key request granted a delegation from V: %v", d.Delegations)
	}

	if _, ok := d.MailGrants[u.ID][v.ID]; ok {
		t.Errorf("a node-key request granted U's mail to V: %v", d.MailGrants)
	}

	if _, ok := d.MailGrants[u.ID][kept.ID]; !ok {
		t.Errorf("a node-key request revoked U's mail grant to the kept user: %v", d.MailGrants)
	}

	if len(d.MailGrants[v.ID]) != 0 {
		t.Errorf("a node-key request granted mail from V: %v", d.MailGrants)
	}
}
