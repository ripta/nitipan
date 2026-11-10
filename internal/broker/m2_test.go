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

	"github.com/space-armada/nitipan/internal/repoid"
	"github.com/space-armada/nitipan/internal/wire"
)

// TestUserLeasesAcrossUsersDontConflict tests that, with cross-user features on, user leases of two
// bound users on overlapping names do not conflict. Two labels of one user do conflict.
func TestUserLeasesAcrossUsersDontConflict(t *testing.T) {
	h := newHarness(t, nil)

	now := time.Now()

	user1, err := h.b.CreateUser("user1@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	user2, err := h.b.CreateUser("user2@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	alice := nodeID{Machine: "machine1", UID: 1000, Key: "key1"}
	regAlice := h.register(alice)
	labelAlice := regAlice.Label

	if err := h.b.BindLabelToUser(labelAlice, user1.ID, now); err != nil {
		t.Fatal(err)
	}

	bob := nodeID{Machine: "machine2", UID: 1001, Key: "key2"}
	regBob := h.register(bob)
	labelBob := regBob.Label

	if err := h.b.BindLabelToUser(labelBob, user2.ID, now); err != nil {
		t.Fatal(err)
	}

	charlie := nodeID{Machine: "machine3", UID: 1002, Key: "key3"}
	regCharlie := h.register(charlie)
	labelCharlie := regCharlie.Label

	if err := h.b.BindLabelToUser(labelCharlie, user1.ID, now); err != nil {
		t.Fatal(err)
	}

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
	h := newHarness(t, nil)

	now := time.Now()

	user1, err := h.b.CreateUser("user1@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	user2, err := h.b.CreateUser("user2@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	alice := nodeID{Machine: "machine1", UID: 1000, Key: "key1"}
	regAlice := h.register(alice)
	labelAlice := regAlice.Label

	if err := h.b.BindLabelToUser(labelAlice, user1.ID, now); err != nil {
		t.Fatal(err)
	}

	bob := nodeID{Machine: "machine2", UID: 1001, Key: "key2"}
	regBob := h.register(bob)
	labelBob := regBob.Label

	if err := h.b.BindLabelToUser(labelBob, user2.ID, now); err != nil {
		t.Fatal(err)
	}

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

// TestOverlapReportsOnlyWithinUserScope tests that, with cross-user features on, overlap reports
// between user leases and mirrored machine leases go only between leases of one user.
func TestOverlapReportsOnlyWithinUserScope(t *testing.T) {
	h := newHarness(t, nil)

	now := time.Now()

	user1, err := h.b.CreateUser("user1@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	user2, err := h.b.CreateUser("user2@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	alice := nodeID{Machine: "machine1", UID: 1000, Key: "key1"}
	regAlice := h.register(alice)
	labelAlice := regAlice.Label

	if err := h.b.BindLabelToUser(labelAlice, user1.ID, now); err != nil {
		t.Fatal(err)
	}

	bob := nodeID{Machine: "machine2", UID: 1001, Key: "key2"}
	regBob := h.register(bob)
	labelBob := regBob.Label

	if err := h.b.BindLabelToUser(labelBob, user2.ID, now); err != nil {
		t.Fatal(err)
	}

	h.crossUserOn(t)

	entries := []repoid.Name{repoid.New([]string{rootA}, "/home/user/svc", false, false)}

	derived := &wire.Derived{State: wire.DerivedOK, Derivation: 1, Entries: entries}
	h.snapshot(bob, machineMirror(t, "M1", "holder-bob", derived, "/home/user/svc"))

	var res wire.Result
	status := h.call(alice, wire.PathClaim, wire.Claim{
		Version: wire.Current, ID: "L1", Reach: wire.ReachUser, Entries: entries, Derivation: 1,
		Anchor: "anchor-L1", Holder: holder("a"), ActingFor: user1.UserLabel,
	}, &res)
	if status != http.StatusOK {
		t.Fatalf("alice claim: status %d", status)
	}
	if res.Code != wire.CodeGranted {
		t.Fatalf("alice claim should be granted: %+v", res)
	}

	if len(res.Overlaps) > 0 {
		t.Fatalf("alice's user lease should not report overlaps with bob's machine lease (different user): %+v", res.Overlaps)
	}
}

// TestDelegationAllowsActingFor tests that a label can claim with acting_for set to a user who
// delegated to that label.
func TestDelegationAllowsActingFor(t *testing.T) {
	h := newHarness(t, nil)

	now := time.Now()

	user1, err := h.b.CreateUser("user1@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	user2, err := h.b.CreateUser("user2@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	bob := nodeID{Machine: "machine2", UID: 1001, Key: "key2"}
	regBob := h.register(bob)
	labelBob := regBob.Label

	if err := h.b.BindLabelToUser(labelBob, user2.ID, now); err != nil {
		t.Fatal(err)
	}

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

// TestGrantAndCheckDelegation tests the basic delegation grant and check functionality.
func TestGrantAndCheckDelegation(t *testing.T) {
	h := newHarness(t, nil)

	now := time.Now()

	user1, err := h.b.CreateUser("user1@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	user2, err := h.b.CreateUser("user2@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	bob := nodeID{Machine: "machine2", UID: 1001, Key: "key2"}
	regBob := h.register(bob)
	labelBob := regBob.Label

	if err := h.b.BindLabelToUser(labelBob, user2.ID, now); err != nil {
		t.Fatal(err)
	}

	h.b.mu.Lock()
	hasDel := h.b.labelHoldsDelegation(labelBob, user1.UserLabel)
	h.b.mu.Unlock()

	if hasDel {
		t.Fatal("should not have delegation yet")
	}

	h.delegate(t, user1, labelBob)

	h.b.mu.Lock()
	hasDel = h.b.labelHoldsDelegation(labelBob, user1.UserLabel)
	delegations := h.b.store.Data().Delegations[user1.ID]
	h.b.mu.Unlock()

	if !hasDel {
		t.Fatalf("should have delegation now, delegations: %+v", delegations)
	}
}

// TestRevokingDelegationEndsLeases tests that revoking a delegation ends every lease that label
// holds with acting_for set to the delegating user.
func TestRevokingDelegationEndsLeases(t *testing.T) {
	h := newHarness(t, nil)

	now := time.Now()

	user1, err := h.b.CreateUser("user1@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	user2, err := h.b.CreateUser("user2@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	bob := nodeID{Machine: "machine2", UID: 1001, Key: "key2"}
	regBob := h.register(bob)
	labelBob := regBob.Label

	if err := h.b.BindLabelToUser(labelBob, user2.ID, now); err != nil {
		t.Fatal(err)
	}

	h.delegate(t, user1, labelBob)

	entries := []repoid.Name{repoid.New([]string{rootA}, "svc", false, false)}

	var res wire.Result
	status := h.call(bob, wire.PathClaim, wire.Claim{
		Version: wire.Current, ID: "L1", Reach: wire.ReachUser, Entries: entries, Derivation: 1,
		Anchor: "anchor-L1", Holder: holder("b"), ActingFor: user1.UserLabel, ActingForExplicit: true,
	}, &res)
	if status != http.StatusOK {
		t.Fatalf("bob claim: status %d", status)
	}
	if res.Code != wire.CodeGranted {
		t.Fatalf("bob claim with acting_for should be granted: %+v", res)
	}

	leases := h.b.Leases()
	if len(leases) != 1 || leases[0].State != wire.StateLive {
		t.Fatalf("expected 1 live lease, got %+v", leases)
	}

	h.undelegate(t, user1, labelBob)

	facts := h.b.Facts()
	foundEnded := false
	for _, l := range facts {
		if l.ID == "L1" && l.State == wire.StateEnded {
			foundEnded = true

			break
		}
	}
	if !foundEnded {
		t.Fatalf("lease L1 should be ended after delegation revoked, facts: %+v", facts)
	}
}

// TestRevokeDelegationPreventsRevival tests that after a delegation is revoked, a renew returns
// CodeEnded and the lease is never revived.
func TestRevokeDelegationPreventsRevival(t *testing.T) {
	h := newHarness(t, nil)

	now := time.Now()

	user1, err := h.b.CreateUser("user1@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	user2, err := h.b.CreateUser("user2@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	bob := nodeID{Machine: "machine2", UID: 1001, Key: "key2"}
	regBob := h.register(bob)
	labelBob := regBob.Label

	if err := h.b.BindLabelToUser(labelBob, user2.ID, now); err != nil {
		t.Fatal(err)
	}

	h.delegate(t, user1, labelBob)

	entries := []repoid.Name{repoid.New([]string{rootA}, "svc", false, false)}

	var res wire.Result
	status := h.call(bob, wire.PathClaim, wire.Claim{
		Version: wire.Current, ID: "L1", Reach: wire.ReachUser, Entries: entries, Derivation: 1,
		Anchor: "anchor-L1", Holder: holder("b"), ActingFor: user1.UserLabel, ActingForExplicit: true,
	}, &res)
	if status != http.StatusOK {
		t.Fatalf("bob claim: status %d", status)
	}
	if res.Code != wire.CodeGranted {
		t.Fatalf("bob claim should be granted: %+v", res)
	}

	h.undelegate(t, user1, labelBob)

	renewRes := h.renew(bob, "L1")
	if len(renewRes) != 1 {
		t.Fatalf("expected 1 renew result, got %d", len(renewRes))
	}
	if renewRes[0].Code != wire.CodeEnded {
		t.Fatalf("renew after delegation revoked should return CodeEnded, got %s", renewRes[0].Code)
	}
	if renewRes[0].EndedBy != "delegation revoked" {
		t.Fatalf("EndedBy should be 'delegation revoked', got %q", renewRes[0].EndedBy)
	}

	facts := h.b.Facts()
	for _, l := range facts {
		if l.ID == "L1" && l.State != wire.StateEnded {
			t.Fatalf("lease L1 should remain ended after renew attempt, got state %s", l.State)
		}
	}
}

// TestRevokeUserEndsAllLeasesHeldForUser tests that RevokeUser ends every lease held for U.
func TestRevokeUserEndsAllLeasesHeldForUser(t *testing.T) {
	h := newHarness(t, nil)

	now := time.Now()

	user1, err := h.b.CreateUser("user1@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	user2, err := h.b.CreateUser("user2@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	alice := nodeID{Machine: "machine1", UID: 1000, Key: "key1"}
	regAlice := h.register(alice)
	labelAlice := regAlice.Label

	if err := h.b.BindLabelToUser(labelAlice, user1.ID, now); err != nil {
		t.Fatal(err)
	}

	bob := nodeID{Machine: "machine2", UID: 1001, Key: "key2"}
	regBob := h.register(bob)
	labelBob := regBob.Label

	if err := h.b.BindLabelToUser(labelBob, user2.ID, now); err != nil {
		t.Fatal(err)
	}

	h.delegate(t, user1, labelBob)

	entries1 := []repoid.Name{repoid.New([]string{rootA}, "svc1", false, false)}
	entries2 := []repoid.Name{repoid.New([]string{rootA}, "svc2", false, false)}
	entries3 := []repoid.Name{repoid.New([]string{rootA}, "svc3", false, false)}

	var res1 wire.Result
	status := h.call(alice, wire.PathClaim, wire.Claim{
		Version: wire.Current, ID: "L1", Reach: wire.ReachUser, Entries: entries1, Derivation: 1,
		Anchor: "anchor-L1", Holder: holder("a"), ActingFor: user1.UserLabel,
	}, &res1)
	if status != http.StatusOK || res1.Code != wire.CodeGranted {
		t.Fatalf("alice L1 claim failed: status %d, code %s", status, res1.Code)
	}

	var res2 wire.Result
	status = h.call(bob, wire.PathClaim, wire.Claim{
		Version: wire.Current, ID: "L2", Reach: wire.ReachUser, Entries: entries2, Derivation: 1,
		Anchor: "anchor-L2", Holder: holder("b"), ActingFor: user1.UserLabel, ActingForExplicit: true,
	}, &res2)
	if status != http.StatusOK || res2.Code != wire.CodeGranted {
		t.Fatalf("bob L2 claim for user1 failed: status %d, code %s", status, res2.Code)
	}

	var res3 wire.Result
	status = h.call(bob, wire.PathClaim, wire.Claim{
		Version: wire.Current, ID: "L3", Reach: wire.ReachUser, Entries: entries3, Derivation: 1,
		Anchor: "anchor-L3", Holder: holder("b2"), ActingFor: user2.UserLabel,
	}, &res3)
	if status != http.StatusOK || res3.Code != wire.CodeGranted {
		t.Fatalf("bob L3 claim for user2 failed: status %d, code %s", status, res3.Code)
	}

	leases := h.b.Leases()
	if len(leases) != 3 {
		t.Fatalf("expected 3 live leases, got %d", len(leases))
	}

	if err := h.b.RevokeUser(user1.ID); err != nil {
		t.Fatal(err)
	}

	facts := h.b.Facts()
	endedCount := 0
	for _, l := range facts {
		if l.State == wire.StateEnded {
			endedCount++
			if l.ID == "L1" || l.ID == "L2" {
				if l.EndedBy != "user revoked" {
					t.Fatalf("lease %s should have EndedBy='user revoked', got %q", l.ID, l.EndedBy)
				}
			}
			if l.ID == "L3" {
				t.Fatalf("lease L3 (user2's own lease) should not be ended")
			}
		}
	}
	if endedCount != 2 {
		t.Fatalf("expected 2 ended leases (L1 and L2), got %d: %+v", endedCount, facts)
	}

	leases = h.b.Leases()
	stillLive := false
	for _, l := range leases {
		if l.ID == "L3" && l.State == wire.StateLive {
			stillLive = true

			break
		}
	}
	if !stillLive {
		t.Fatalf("lease L3 (user2's lease) should still be live")
	}
}

// TestRevokeUserLeavesOtherUsersUntouched tests that RevokeUser ends only leases held for the
// revoked user and leaves other users' leases untouched.
func TestRevokeUserLeavesOtherUsersUntouched(t *testing.T) {
	h := newHarness(t, nil)

	now := time.Now()

	user1, err := h.b.CreateUser("user1@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	user2, err := h.b.CreateUser("user2@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	alice := nodeID{Machine: "machine1", UID: 1000, Key: "key1"}
	regAlice := h.register(alice)
	labelAlice := regAlice.Label

	if err := h.b.BindLabelToUser(labelAlice, user1.ID, now); err != nil {
		t.Fatal(err)
	}

	bob := nodeID{Machine: "machine2", UID: 1001, Key: "key2"}
	regBob := h.register(bob)
	labelBob := regBob.Label

	if err := h.b.BindLabelToUser(labelBob, user2.ID, now); err != nil {
		t.Fatal(err)
	}

	entries1 := []repoid.Name{repoid.New([]string{rootA}, "svc1", false, false)}
	entries2 := []repoid.Name{repoid.New([]string{rootA}, "svc2", false, false)}

	var res1 wire.Result
	status := h.call(alice, wire.PathClaim, wire.Claim{
		Version: wire.Current, ID: "L1", Reach: wire.ReachUser, Entries: entries1, Derivation: 1,
		Anchor: "anchor-L1", Holder: holder("a"), ActingFor: user1.UserLabel,
	}, &res1)
	if status != http.StatusOK || res1.Code != wire.CodeGranted {
		t.Fatalf("alice L1 claim failed")
	}

	var res2 wire.Result
	status = h.call(bob, wire.PathClaim, wire.Claim{
		Version: wire.Current, ID: "L2", Reach: wire.ReachUser, Entries: entries2, Derivation: 1,
		Anchor: "anchor-L2", Holder: holder("b"), ActingFor: user2.UserLabel,
	}, &res2)
	if status != http.StatusOK || res2.Code != wire.CodeGranted {
		t.Fatalf("bob L2 claim failed")
	}

	if err := h.b.RevokeUser(user1.ID); err != nil {
		t.Fatal(err)
	}

	leases := h.b.Leases()
	for _, l := range leases {
		if l.ID == "L2" {
			if l.State != wire.StateLive {
				t.Fatalf("lease L2 (user2's lease) should remain live after user1 revoked, got state %s", l.State)
			}

			return
		}
	}

	t.Fatal("lease L2 not found in leases")
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
	wire.PathRegister, wire.PathStream, wire.PathAck, wire.PathSnapshot, wire.PathHeartbeat, wire.PathPing, wire.PathPair,
	wire.PathClaim, wire.PathRenew, wire.PathRelease, wire.PathBreak, wire.PathList, wire.PathFence, wire.PathWhoHolds,
	wire.PathWhoIs, wire.PathWhoAmI, wire.PathMailSend, wire.PathMailAck, wire.PathMailReport, wire.PathMailStatus,
	wire.Prefix + "/grant", wire.Prefix + "/revoke", wire.Prefix + "/delegate", wire.Prefix + "/delegation",
	wire.Prefix + "/delegations", wire.Prefix + "/grant-delegation", wire.Prefix + "/revoke-delegation",
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

// TestNoNetworkRouteGrantsOrRevokesADelegationWithoutAnIdentityProof proves the broker's network
// handler, sign-in page included, grants and revokes no delegation for a caller holding only a node key.
// It catches a node route, or a page route that trusts a node key or a form field, that changes a
// delegation without the user's identity proof. The page's own grant with a proof is proven by the
// sign-in page tests.
func TestNoNetworkRouteGrantsOrRevokesADelegationWithoutAnIdentityProof(t *testing.T) {
	h, _ := newSignInHarness(t)

	now := time.Now()

	u, err := h.b.CreateUser("u@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	v, err := h.b.CreateUser("v@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	uNode := nodeID{Machine: "machine-u", UID: 1000, Key: "key-u"}
	vNode := nodeID{Machine: "machine-v", UID: 1001, Key: "key-v"}
	keptNode := nodeID{Machine: "machine-kept", UID: 1002, Key: "key-kept"}

	labels := map[nodeID]string{}

	for n, userID := range map[nodeID]string{uNode: u.ID, vNode: v.ID, keptNode: v.ID} {
		labels[n] = h.register(n).Label
		if err := h.b.BindLabelToUser(labels[n], userID, now); err != nil {
			t.Fatal(err)
		}
	}

	h.delegate(t, u, labels[keptNode])

	for _, p := range delegationProbes() {
		for _, n := range []nodeID{uNode, vNode} {
			for _, target := range []string{labels[vNode], labels[keptNode]} {
				h.probe(p, n, map[string]string{
					"user_id": u.ID, "user": u.UserLabel, "user_label": u.UserLabel, "email": "u@example.com",
					"label": target, "token": "not-a-token", "action": "grant",
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
}

// TestNewVersionClaimWithoutDelegationRefused tests that a new-version claim (ActingForExplicit=true)
// naming another user is refused without that user's delegation.
func TestNewVersionClaimWithoutDelegationRefused(t *testing.T) {
	h := newHarness(t, nil)

	now := time.Now()

	user1, err := h.b.CreateUser("user1@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	user2, err := h.b.CreateUser("user2@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	bob := nodeID{Machine: "machine2", UID: 1001, Key: "key2"}
	regBob := h.register(bob)
	labelBob := regBob.Label

	if err := h.b.BindLabelToUser(labelBob, user2.ID, now); err != nil {
		t.Fatal(err)
	}

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

// TestHolderCanReleaseDelegatedLease tests that the holder can release a lease held with acting_for.
func TestHolderCanReleaseDelegatedLease(t *testing.T) {
	h := newHarness(t, nil)

	now := time.Now()

	user1, err := h.b.CreateUser("user1@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	user2, err := h.b.CreateUser("user2@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	bob := nodeID{Machine: "machine2", UID: 1001, Key: "key2"}
	regBob := h.register(bob)
	labelBob := regBob.Label

	if err := h.b.BindLabelToUser(labelBob, user2.ID, now); err != nil {
		t.Fatal(err)
	}

	h.delegate(t, user1, labelBob)

	entries := []repoid.Name{repoid.New([]string{rootA}, "svc", false, false)}

	var claimRes wire.Result
	status := h.call(bob, wire.PathClaim, wire.Claim{
		Version: wire.Current, ID: "L1", Reach: wire.ReachUser, Entries: entries, Derivation: 1,
		Anchor: "anchor-L1", Holder: holder("b"), ActingFor: user1.UserLabel, ActingForExplicit: true,
	}, &claimRes)
	if status != http.StatusOK {
		t.Fatalf("bob claim for user1 with delegation: status %d", status)
	}
	if claimRes.Code != wire.CodeGranted {
		t.Fatalf("bob claim for user1 should be granted with delegation: %+v", claimRes)
	}
	if claimRes.Lease.ActingFor != user1.UserLabel {
		t.Fatalf("lease acting_for = %q, want %q", claimRes.Lease.ActingFor, user1.UserLabel)
	}

	var releaseRes wire.Result
	status = h.call(bob, wire.PathRelease, wire.Release{
		Version: wire.Current, RequestID: "release-L1", ID: "L1", Holder: holder("b"), Cause: wire.CauseReleased,
	}, &releaseRes)
	if status != http.StatusOK {
		t.Fatalf("bob release of L1: status %d", status)
	}
	if releaseRes.Code != wire.CodeReleased {
		t.Fatalf("bob should be able to release his own delegated lease: %+v", releaseRes)
	}
}

// TestOldVersionClaimWithActingForIsSubstituted tests that an old-version claim (ActingForExplicit=false)
// has its acting_for replaced by the label's bound user, regardless of what the CLI sent.
func TestOldVersionClaimWithActingForIsSubstituted(t *testing.T) {
	h := newHarness(t, nil)

	now := time.Now()

	user1, err := h.b.CreateUser("user1@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	user2, err := h.b.CreateUser("user2@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	bob := nodeID{Machine: "machine2", UID: 1001, Key: "key2"}
	regBob := h.register(bob)
	labelBob := regBob.Label

	if err := h.b.BindLabelToUser(labelBob, user2.ID, now); err != nil {
		t.Fatal(err)
	}

	entries := []repoid.Name{repoid.New([]string{rootA}, "svc", false, false)}

	var res wire.Result
	status := h.call(bob, wire.PathClaim, wire.Claim{
		Version: wire.Current, ID: "L1", Reach: wire.ReachUser, Entries: entries, Derivation: 1,
		Anchor: "anchor-L1", Holder: holder("b"), ActingFor: user1.UserLabel, ActingForExplicit: false,
	}, &res)
	if status != http.StatusOK {
		t.Fatalf("old-version claim: status %d", status)
	}
	if res.Code != wire.CodeGranted {
		t.Fatalf("old-version claim should be granted with substituted acting_for: %+v", res)
	}
	if res.Lease.ActingFor != user2.UserLabel {
		t.Fatalf("old-version claim acting_for should be substituted to user2, got %q", res.Lease.ActingFor)
	}
}
