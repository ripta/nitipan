package broker

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/oidc/oidctest"
	"github.com/space-armada/nitipan/internal/repoid"
	"github.com/space-armada/nitipan/internal/wire"
)

// TestRevokeUserForgetsLabelsRemovesAdmissionAndDropsGrants proves "revoke user" forgets every label bound
// to the user, removes each issuer plus sub that resolves to them, and drops every delegation and mail
// grant that names them, made or received. Another user's label stays pinned. It catches a revoke that
// leaves one of the user's keys pinned, or a grant that lets the revoked user's work reach another user.
func TestRevokeUserForgetsLabelsRemovesAdmissionAndDropsGrants(t *testing.T) {
	h := newCrossUserHarness(t)

	now := time.Now()
	user, err := h.b.AdmitUser(createIssuer, "user@example.com", "user@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	if err := h.b.LinkIssuerSub("https://issuer.example", "sub123", user.ID); err != nil {
		t.Fatal(err)
	}

	otherUser, err := h.b.AdmitUser(createIssuer, "other@example.com", "other@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	alice := nodeID{Machine: "machine1", UID: 1000, Key: "key1"}
	bob := nodeID{Machine: "machine2", UID: 1001, Key: "key2"}
	charlie := nodeID{Machine: "machine3", UID: 1002, Key: "key3"}

	for _, n := range []nodeID{alice, bob, charlie} {
		h.register(n)
	}

	labelAlice := h.bindNode(t, alice, user.ID)
	labelBob := h.bindNode(t, bob, user.ID)
	labelCharlie := h.bindNode(t, charlie, otherUser.ID)

	h.delegate(t, user, labelCharlie)
	h.delegate(t, otherUser, labelAlice)
	h.grantMail(t, user, otherUser)
	h.grantMail(t, otherUser, user)

	if _, err := h.b.RevokeUser(user.ID); err != nil {
		t.Fatal(err)
	}

	d := h.data()

	if !d.Pins[labelAlice].Forgotten {
		t.Error("label alice not forgotten")
	}

	if !d.Pins[labelBob].Forgotten {
		t.Error("label bob not forgotten")
	}

	if d.Pins[labelCharlie].Forgotten {
		t.Error("label charlie, another user's, was forgotten")
	}

	for key, id := range d.IssuerSubs {
		if id == user.ID {
			t.Errorf("issuer sub %q still resolves to the revoked user", key)
		}
	}

	if _, ok := d.Delegations[user.ID]; ok {
		t.Error("user delegations not removed")
	}

	if _, ok := d.MailGrants[user.ID]; ok {
		t.Error("user mail grants not removed")
	}

	if _, ok := d.Delegations[otherUser.ID][labelAlice]; ok {
		t.Error("delegation to user's label not removed")
	}

	if _, ok := d.MailGrants[otherUser.ID][user.ID]; ok {
		t.Error("mail grant to user not removed")
	}
}

// TestRevokeUserAfterSignInRestoresNothing proves a revoked user who signs in again on the page is not
// admitted, and that admitting them again makes a new user with none of the old one's labels, delegations,
// or mail grants. It catches a revoke that keeps the issuer plus sub, so the next sign-in resolves to the
// old user and everything the owner revoked comes back.
func TestRevokeUserAfterSignInRestoresNothing(t *testing.T) {
	iss := oidctest.New(t, "revoked-sub", "revoked@example.com")
	h, page := pageHarness(t, iss)

	now := time.Now()
	user, err := h.b.AdmitUser(iss.URL, "revoked-sub", "revoked@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	otherUser, err := h.b.AdmitUser(createIssuer, "other@example.com", "other@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	n := nodeID{Machine: "machine1", UID: 1000, Key: "key1"}
	other := nodeID{Machine: "machine2", UID: 1001, Key: "key2"}

	h.register(n)
	h.register(other)

	label := h.bindNode(t, n, user.ID)
	otherLabel := h.bindNode(t, other, otherUser.ID)

	h.delegate(t, user, otherLabel)
	h.grantMail(t, user, otherUser)

	if _, err := h.b.RevokeUser(user.ID); err != nil {
		t.Fatal(err)
	}

	if status, body := page.SignIn("bind"); status != http.StatusOK || !strings.Contains(body, "Pending Admission") {
		t.Fatalf("signing in again after the revoke: %d %s, want the pending admission page", status, body)
	}

	if id, ok := h.data().IssuerSubs[issuerSubKey(iss.URL, "revoked-sub")]; ok {
		t.Fatalf("the sign-in after the revoke resolves to user %q", id)
	}

	user2, err := h.b.AdmitUser(iss.URL, "revoked-sub", "revoked@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	d := h.data()

	if user2.ID == user.ID {
		t.Error("re-admitted user got the same ID as the revoked user")
	}

	if !d.Pins[label].Forgotten {
		t.Error("label not forgotten after revoke")
	}

	if _, ok := d.Delegations[user2.ID]; ok {
		t.Error("delegations restored after sign-in")
	}

	if _, ok := d.MailGrants[user2.ID]; ok {
		t.Error("mail grants restored after sign-in")
	}
}

// TestRevokeUserEndsAllLeasesHeldForUser proves "revoke user" ends every lease held for the user, on the
// user's own label and on a label the user delegated to, with the revoke as the cause. The delegated
// label's lease for its own user stays live. It catches a revoke that ends only the user's own labels'
// leases, which leaves an agent working for a user the owner removed.
func TestRevokeUserEndsAllLeasesHeldForUser(t *testing.T) {
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
	labelBob := h.bindNode(t, bob, user2.ID)

	h.crossUserOn(t)

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

	if _, err := h.b.RevokeUser(user1.ID); err != nil {
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

// TestRevokeUserLeavesOtherUsersUntouched proves "revoke user" leaves another user's lease on that user's
// own label live. It catches a revoke that ends every lease in the broker.
func TestRevokeUserLeavesOtherUsersUntouched(t *testing.T) {
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

	if _, err := h.b.RevokeUser(user1.ID); err != nil {
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

// TestForgetAndRevokeClosesStreamAndRefusesReconnect proves a forget closes the label's open stream at once,
// keeps its last bound user, and refuses the old key's reconnect. It catches a forget that leaves the
// stream open, so a key the owner removed keeps receiving events until it disconnects.
func TestForgetAndRevokeClosesStreamAndRefusesReconnect(t *testing.T) {
	h := newCrossUserHarness(t)

	now := time.Now()
	user, err := h.b.AdmitUser(createIssuer, "user@example.com", "user@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	n := nodeID{Machine: "machine1", UID: 1000, Key: "key1"}
	h.register(n)
	label := h.bindNode(t, n, user.ID)

	r, _, _ := h.openStream(n)

	streamDone := make(chan struct{})

	go func() {
		defer close(streamDone)

		_, _ = io.Copy(io.Discard, r)
	}()

	d := h.data()
	oldLastBoundUser := d.Bindings[label].UserID

	if err := h.b.Forget(label); err != nil {
		t.Fatal(err)
	}

	select {
	case <-streamDone:
	case <-time.After(2 * time.Second):
		t.Fatal("stream not closed after forget")
	}

	d = h.data()
	if !d.Pins[label].Forgotten {
		t.Error("label not forgotten")
	}

	if d.Bindings[label].LastBoundUser != oldLastBoundUser {
		t.Errorf("last bound user = %q, want %q", d.Bindings[label].LastBoundUser, oldLastBoundUser)
	}

	resp := h.do(http.MethodGet, wire.PathStream, n, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("reconnect status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

// TestRevokeUserRefusesBrokerOwner proves "revoke user" refuses the broker owner's own user ID, leaves the
// owner's labels pinned, and reports a bad request on the admin socket. It catches a revoke that unpins every
// machine the owner approved. It cannot prove that no other path forgets the owner's labels.
func TestRevokeUserRefusesBrokerOwner(t *testing.T) {
	h := newCrossUserHarness(t)

	ownerID := h.data().OwnerID
	if ownerID == "" {
		t.Fatal("the harness broker has no owner")
	}

	n := nodeID{Machine: "machine1", UID: 1000, Key: "key1"}

	h.register(n)

	label := h.bindNode(t, n, ownerID)

	_, err := h.b.RevokeUser(ownerID)
	if !errors.Is(err, ErrOwnerUser) {
		t.Fatalf("RevokeUser(owner) = %v, want ErrOwnerUser", err)
	}

	if !strings.Contains(err.Error(), "broker forget") {
		t.Errorf("error %q does not point at broker forget", err)
	}

	if h.data().Pins[label].Forgotten {
		t.Error("the owner's label was forgotten")
	}

	if _, ok := h.data().Users[ownerID]; !ok {
		t.Error("the owner's record is gone")
	}

	rec := httptest.NewRecorder()

	h.b.adminResult(rec, err)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("admin status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}
