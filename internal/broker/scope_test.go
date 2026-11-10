package broker

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/oidc"
	"github.com/space-armada/nitipan/internal/repoid"
	"github.com/space-armada/nitipan/internal/wire"
)

// crossUserOn turns cross-user features on through the admin socket, as the owner's command does. The
// harness needs an identity provider, as newCrossUserHarness gives it, and every key must have reported
// HTTPS.
func (h *harness) crossUserOn(t *testing.T) {
	t.Helper()

	if rec := h.enableCrossUser(); rec.Code != http.StatusOK {
		t.Fatalf("turning cross-user features on: %d %s", rec.Code, rec.Body)
	}
}

// crossUserIssuer is the identity provider a cross-user harness names. No test signs in at it, so the
// broker never fetches it.
const crossUserIssuer = "https://issuer.test"

// newCrossUserHarness is a harness with an identity provider configured, so the owner may turn cross-user
// features on.
func newCrossUserHarness(t *testing.T) *harness {
	t.Helper()

	return newHarness(t, func(c *Config) {
		c.OIDCConfig = &oidc.Config{Issuer: crossUserIssuer, ClientID: "nitipan", RedirectURL: "https://broker.test/callback"}
	})
}

// unboundWorld is a broker with cross-user features off. The owner has one bound label, V is a second
// user, and one more label registered but nobody approved it.
type unboundWorld struct {
	h *harness

	owner, v UserRecord

	ownerNode, unbound nodeID
	unboundLabel       string
}

func newUnboundWorld(t *testing.T) *unboundWorld {
	t.Helper()

	h := newCrossUserHarness(t)

	owner := h.data().Users[h.data().OwnerID]

	v, err := h.b.AdmitUser(createIssuer, "v@example.com", "v@example.com", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	w := &unboundWorld{
		h: h, owner: owner, v: v,
		ownerNode: nodeID{Machine: "machine-owner", UID: 1000, Key: "key-owner"},
		unbound:   nodeID{Machine: "machine-unbound", UID: 1001, Key: "key-unbound"},
	}

	approveKey(h, h.register(w.ownerNode).Label, w.ownerNode.Key)

	w.unboundLabel = h.register(w.unbound).Label

	return w
}

// claim sends a claim from node n and returns the status and the decoded result.
func (w *unboundWorld) claim(n nodeID, id, actingFor string, explicit bool) (int, wire.Result, wire.Error) {
	w.h.t.Helper()

	resp := w.h.do(http.MethodPost, wire.PathClaim, n, wire.Claim{
		Version: wire.Current, ID: id, Reach: wire.ReachUser, Entries: name("svc"), Derivation: 1, Anchor: "anchor-" + id,
		Holder: holder(id), ActingFor: actingFor, ActingForExplicit: explicit,
	})

	var (
		res wire.Result
		e   wire.Error
	)

	into := any(&res)
	if resp.StatusCode != http.StatusOK {
		into = &e
	}

	if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
		w.h.t.Fatal(err)
	}

	return resp.StatusCode, res, e
}

// TestWithCrossUserOffAnUnboundLabelsLeaseConflictsWithTheOwners proves an unbound label's `user`
// lease counts in the owner's scope while cross-user features are off, whatever OS user its claim names.
// It catches a broker that passes an unbound label's unauthenticated acting_for through, which would
// put the lease in a scope of its own and let it sit beside the owner's lease on the same files.
func TestWithCrossUserOffAnUnboundLabelsLeaseConflictsWithTheOwners(t *testing.T) {
	w := newUnboundWorld(t)

	if status, res, _ := w.claim(w.ownerNode, "L-owner", w.owner.UserLabel, false); status != http.StatusOK || res.Code != wire.CodeGranted {
		t.Fatalf("the owner's claim: status %d, %+v", status, res)
	}

	status, res, _ := w.claim(w.unbound, "L-unbound", "alice", false)
	if status != http.StatusOK || res.Code != wire.CodeRefused || len(res.Others) != 1 || res.Others[0].ID != "L-owner" {
		t.Fatalf("the unbound label's claim: status %d, %+v, want a refusal naming L-owner", status, res)
	}
}

// TestAnUnboundLabelsClaimForAnotherUserIsRefused proves an explicit acting_for from a label nobody
// approved is refused, and the error names the approve step. It catches a broker that lets an unbound
// label claim in any user's scope it names, since no binding or delegation is there to check.
func TestAnUnboundLabelsClaimForAnotherUserIsRefused(t *testing.T) {
	w := newUnboundWorld(t)

	status, _, e := w.claim(w.unbound, "L1", w.v.UserLabel, true)
	if status != http.StatusForbidden || e.Code != wire.CodeForbidden || !strings.Contains(e.Message, "nitipan broker approve") {
		t.Fatalf("the unbound label's claim for V: status %d, %+v", status, e)
	}

	w.h.b.mu.Lock()
	g := w.h.b.g.grants["L1"]
	w.h.b.mu.Unlock()

	if g != nil {
		t.Fatalf("the refused claim left a grant: %+v", g)
	}
}

// TestAnUnboundLabelsLeaseNeverLandsInAnotherUsersScope proves an old-version claim from an unbound
// label that names V in acting_for still counts in the owner's scope, and stays out of V's scope once
// the label is bound. It catches a broker that stores the unbound label's value, which a later binding
// keeps, since it already reads as a user label.
func TestAnUnboundLabelsLeaseNeverLandsInAnotherUsersScope(t *testing.T) {
	w := newUnboundWorld(t)

	status, res, _ := w.claim(w.unbound, "L1", w.v.UserLabel, false)
	if status != http.StatusOK || res.Code != wire.CodeGranted || res.Lease.ActingFor != w.owner.UserLabel {
		t.Fatalf("the unbound label's claim: status %d, %+v, want a grant for the owner", status, res)
	}

	u, err := w.h.b.AdmitUser(createIssuer, "u@example.com", "u@example.com", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	w.h.bindNode(t, w.unbound, u.ID)

	w.h.b.mu.Lock()
	defer w.h.b.mu.Unlock()

	for _, g := range w.h.b.g.grants {
		if g.Node == w.unboundLabel && g.ActingFor == w.v.UserLabel {
			t.Errorf("lease %s of the newly bound label sits in V's scope", g.ID)
		}
	}
}

// leaseActingFor returns a lease's acting_for as the broker holds it in memory and as it persisted it,
// since a renewal reads the first and a restart reloads the second.
func (h *harness) leaseActingFor(t *testing.T, id string) (held, stored string) {
	t.Helper()

	h.b.mu.Lock()
	defer h.b.mu.Unlock()

	if g := h.b.g.grants[id]; g != nil {
		held = g.ActingFor
	}

	if raw, ok := h.b.store.Data().Leases[id]; ok {
		var l wire.Lease
		if err := json.Unmarshal(raw, &l); err != nil {
			t.Fatal(err)
		}

		stored = l.ActingFor
	}

	return held, stored
}

// TestBindingALabelMovesItsLeasesToTheNewlyBoundUser proves an unbound label's lease, held in the
// owner's scope while cross-user features are off, moves to the user the label is then bound to. A lease
// the label holds for a user who delegated to it keeps that user, and binding the label again to the same
// user changes nothing. A lease from before user identity, whose acting_for is an OS user name, moves too.
// It catches a binding that rewrites only an acting_for naming no known user, which leaves the lease in the
// owner's scope, since renewals carry only the node credential, and one that leaves the legacy value, which
// a renewal would carry past the binding. It also catches a rewrite of the store alone, which the broker's
// in-memory leases then overwrite.
func TestBindingALabelMovesItsLeasesToTheNewlyBoundUser(t *testing.T) {
	w := newUnboundWorld(t)

	status, res, _ := w.claim(w.unbound, "L-before", "alice", false)
	if status != http.StatusOK || res.Code != wire.CodeGranted || res.Lease.ActingFor != w.owner.UserLabel {
		t.Fatalf("the unbound label's claim: status %d, %+v, want a grant for the owner", status, res)
	}

	w.h.legacyLease(t, w.unbound, w.unboundLabel, "L-legacy")

	coworker, err := w.h.b.AdmitUser(createIssuer, "w@example.com", "w@example.com", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	w.h.bindNode(t, w.unbound, coworker.ID)

	for _, id := range []string{"L-before", "L-legacy"} {
		if held, stored := w.h.leaseActingFor(t, id); held != coworker.UserLabel || stored != coworker.UserLabel {
			t.Fatalf("%s after binding to W: held %q, stored %q, want %q", id, held, stored, coworker.UserLabel)
		}
	}

	w.h.crossUserOn(t)

	w.h.delegate(t, w.v, w.unboundLabel)

	status = w.h.call(w.unbound, wire.PathClaim, wire.Claim{
		Version: wire.Current, ID: "L-for-v", Reach: wire.ReachUser, Entries: name("for-v"), Derivation: 1,
		Anchor: "anchor-L-for-v", Holder: holder("L-for-v"), ActingFor: w.v.UserLabel, ActingForExplicit: true,
	}, &res)
	if status != http.StatusOK || res.Code != wire.CodeGranted || res.Lease.ActingFor != w.v.UserLabel {
		t.Fatalf("the label's claim for V: status %d, %+v, want a grant for V", status, res)
	}

	if err := w.h.b.Forget(w.unboundLabel); err != nil {
		t.Fatal(err)
	}

	rekeyed := nodeID{Machine: w.unbound.Machine, UID: w.unbound.UID, Key: "key-unbound-new"}
	w.h.register(rekeyed)
	w.h.bindNode(t, rekeyed, coworker.ID)

	want := map[string]string{"L-before": coworker.UserLabel, "L-for-v": w.v.UserLabel}
	for id, label := range want {
		if held, stored := w.h.leaseActingFor(t, id); held != label || stored != label {
			t.Errorf("%s after binding again to W: held %q, stored %q, want %q", id, held, stored, label)
		}
	}
}

// TestWithCrossUserOffAnUnboundLabelSharesTheOwnersUserScope proves the owner's `user` lease and an
// unbound label's `user` lease on one name conflict while cross-user features are off, since the broker
// then runs one scope. It catches a scope check that splits labels before the owner turns the features on.
func TestWithCrossUserOffAnUnboundLabelSharesTheOwnersUserScope(t *testing.T) {
	w := newAuthzWorld(t, false)
	w.claimAs(t, w.uNode, "LU", wire.ReachUser, name("svc"), "u", w.u.UserLabel)

	res, _ := w.claimRaw(t, w.vNode, "LV", wire.ReachUser, name("svc"), "v", "")
	if res.Code != wire.CodeRefused || len(res.Others) != 1 || res.Others[0].ID != "LU" {
		t.Fatalf("the unbound label's user claim with cross-user off: %+v, want a refusal naming LU", res)
	}
}

// TestUserLeasesAcrossUsersDontConflict tests that, with cross-user features on, user leases of two
// bound users on overlapping names do not conflict. Two labels of one user do conflict.
func TestUserLeasesAcrossUsersDontConflict(t *testing.T) {
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

	alice := nodeID{Machine: "machine1", UID: 1000, Key: "key1"}
	h.register(alice)
	h.bindNode(t, alice, user1.ID)

	bob := nodeID{Machine: "machine2", UID: 1001, Key: "key2"}
	h.register(bob)
	h.bindNode(t, bob, user2.ID)

	charlie := nodeID{Machine: "machine3", UID: 1002, Key: "key3"}
	h.register(charlie)
	h.bindNode(t, charlie, user1.ID)

	h.crossUserOn(t)

	entries := []repoid.Name{repoid.New([]string{rootA}, "svc", false, false)}

	var res1 wire.Result
	status := h.call(alice, wire.PathClaim, wire.Claim{
		Version: wire.Current, ID: "L1", Reach: wire.ReachUser, Entries: entries, Derivation: 1,
		Anchor: "anchor-L1", Holder: holder("a"), ActingFor: user1.UserLabel,
	}, &res1)
	if status != http.StatusOK {
		t.Fatalf("alice claim: status %d", status)
	}
	if res1.Code != wire.CodeGranted {
		t.Fatalf("alice claim should be granted: %+v", res1)
	}

	var res2 wire.Result
	status = h.call(bob, wire.PathClaim, wire.Claim{
		Version: wire.Current, ID: "L2", Reach: wire.ReachUser, Entries: entries, Derivation: 1,
		Anchor: "anchor-L2", Holder: holder("b"), ActingFor: user2.UserLabel,
	}, &res2)
	if status != http.StatusOK {
		t.Fatalf("bob claim: status %d", status)
	}
	if res2.Code != wire.CodeGranted {
		t.Fatalf("bob claim should be granted (different user scope): %+v", res2)
	}

	var res3 wire.Result
	status = h.call(charlie, wire.PathClaim, wire.Claim{
		Version: wire.Current, ID: "L3", Reach: wire.ReachUser, Entries: entries, Derivation: 1,
		Anchor: "anchor-L3", Holder: holder("c"), ActingFor: user1.UserLabel,
	}, &res3)
	if status != http.StatusOK {
		t.Fatalf("charlie claim: status %d", status)
	}
	if res3.Code != wire.CodeRefused {
		t.Fatalf("charlie claim should be refused (same user scope as alice): %+v", res3)
	}
	if len(res3.Others) != 1 || res3.Others[0].ID != "L1" {
		t.Fatalf("charlie refusal should name alice's lease: %+v", res3)
	}
}

// TestSharedLeaseConflictsAcrossUsers tests that shared leases conflict across all bound users.
func TestSharedLeaseConflictsAcrossUsers(t *testing.T) {
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

	alice := nodeID{Machine: "machine1", UID: 1000, Key: "key1"}
	h.register(alice)
	h.bindNode(t, alice, user1.ID)

	bob := nodeID{Machine: "machine2", UID: 1001, Key: "key2"}
	h.register(bob)
	h.bindNode(t, bob, user2.ID)

	h.crossUserOn(t)

	entries := []repoid.Name{repoid.New([]string{rootA}, "svc", false, false)}

	var res1 wire.Result
	status := h.call(alice, wire.PathClaim, wire.Claim{
		Version: wire.Current, ID: "L1", Reach: wire.ReachShared, Entries: entries, Derivation: 1,
		Anchor: "anchor-L1", Holder: holder("a"), ActingFor: user1.UserLabel,
	}, &res1)
	if status != http.StatusOK {
		t.Fatalf("alice claim: status %d", status)
	}
	if res1.Code != wire.CodeGranted {
		t.Fatalf("alice shared claim should be granted: %+v", res1)
	}

	var res2 wire.Result
	status = h.call(bob, wire.PathClaim, wire.Claim{
		Version: wire.Current, ID: "L2", Reach: wire.ReachShared, Entries: entries, Derivation: 1,
		Anchor: "anchor-L2", Holder: holder("b"), ActingFor: user2.UserLabel,
	}, &res2)
	if status != http.StatusOK {
		t.Fatalf("bob claim: status %d", status)
	}
	if res2.Code != wire.CodeRefused {
		t.Fatalf("bob shared claim should be refused (shared scope is global): %+v", res2)
	}
	if len(res2.Others) != 1 || res2.Others[0].ID != "L1" {
		t.Fatalf("bob refusal should name alice's lease: %+v", res2)
	}
}

// TestForwardedSessionRequestAttributedToBoundUser tests that a session request forwarded by a
// bound label, whose attestation names another OS user, is attributed to the label's bound user.
func TestForwardedSessionRequestAttributedToBoundUser(t *testing.T) {
	h := newCrossUserHarness(t)

	now := time.Now()
	user, err := h.b.AdmitUser(createIssuer, "user@example.com", "user@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	n := nodeID{Machine: "machine1", UID: 1000, Key: "key1"}
	h.register(n)
	h.bindNode(t, n, user.ID)

	h.crossUserOn(t)

	claimReq := wire.Claim{
		Version:    wire.Current,
		ID:         "claim-1",
		Reach:      "user",
		Entries:    []repoid.Name{repoid.New([]string{"root1"}, "path1", false, false)},
		Derivation: 1,
		Anchor:     "anchor-1",
		Holder: wire.Holder{
			Key:       "holder-key-1",
			Agent:     "test-agent",
			SessionID: "session1",
			Handle:    "handle-1",
			User:      "different-os-user",
		},
		ActingFor: "different-os-user",
	}

	claimBody, _ := json.Marshal(claimReq)
	req := httptest.NewRequest("POST", wire.PathClaim, bytes.NewReader(claimBody))
	req.Header.Set("Authorization", "Bearer "+n.Key)
	req.Header.Set(wire.HeaderMachine, n.Machine)
	req.Header.Set(wire.HeaderUID, "1000")

	w := httptest.NewRecorder()
	h.b.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("claim failed: status = %d, body = %s", w.Code, w.Body.String())
	}

	d := h.data()
	var foundLease map[string]interface{}

	for _, raw := range d.Leases {
		var lease map[string]interface{}
		if err := json.Unmarshal(raw, &lease); err != nil {
			continue
		}

		if lease["id"] == "claim-1" {
			foundLease = lease
			break
		}
	}

	if foundLease == nil {
		t.Fatal("lease not found")
	}

	actingFor, ok := foundLease["acting_for"].(string)
	if !ok {
		t.Fatal("acting_for not found in lease")
	}

	if actingFor != user.UserLabel {
		t.Errorf("acting_for = %q, want %q (bound user label, not OS user from attestation)", actingFor, user.UserLabel)
	}

	holder, _ := foundLease["holder"].(map[string]interface{})
	holderUser, _ := holder["user"].(string)
	if holderUser != "different-os-user" {
		t.Errorf("holder user preserved as %q, expected %q", holderUser, "different-os-user")
	}
}
