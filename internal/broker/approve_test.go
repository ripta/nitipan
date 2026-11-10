package broker

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/oidc/oidctest"
	"github.com/space-armada/nitipan/internal/wire"
)

// legacyLease gives a label a live `user` lease whose acting_for is an OS user name, as a lease from
// before user identity carries.
func (h *harness) legacyLease(t *testing.T, n nodeID, label, id string) {
	t.Helper()

	h.b.mu.Lock()
	err := h.b.mutate(func(s *gstate) error {
		s.grants[id] = &grant{
			Lease: wire.Lease{
				ID: id, Reach: wire.ReachUser, State: wire.StateLive, Revision: 1, Entries: name("svc-" + id),
				Holder: holder(id), ActingFor: "old-os-user", Node: label,
			},
			Machine: n.Machine, UID: n.UID, Derivation: 1,
		}

		return nil
	})
	h.b.mu.Unlock()

	if err != nil {
		t.Fatal(err)
	}
}

// TestApprovingALabelBindsItToTheOwner proves approval on the broker host binds the label to the broker
// owner, moves its legacy lease into the owner's scope, and leaves it accepted once cross-user features
// are on. It catches an approval that pins the key and binds nothing, which leaves the label unbound, so
// every call from it is refused once the owner turns cross-user features on.
func TestApprovingALabelBindsItToTheOwner(t *testing.T) {
	w := newUnboundWorld(t)

	w.h.legacyLease(t, w.unbound, w.unboundLabel, "L-legacy")

	ans, err := w.h.b.Approve(w.unboundLabel, wire.Fingerprint(wire.KeyHash(w.unbound.Key)))
	if err != nil || !ans.Pinned || ans.Owner != w.owner.UserLabel {
		t.Fatalf("approve = %+v, %v, want pinned and bound to the owner %s", ans, err, w.owner.UserLabel)
	}

	if got := w.h.data().Bindings[w.unboundLabel]; got.UserID != w.owner.ID || !got.BoundAt.Equal(wallStart) {
		t.Errorf("binding = %+v, want the owner %s bound at the broker's clock", got, w.owner.ID)
	}

	if held, stored := w.h.leaseActingFor(t, "L-legacy"); held != w.owner.UserLabel || stored != w.owner.UserLabel {
		t.Errorf("the legacy lease: held %q, stored %q, want the owner %q", held, stored, w.owner.UserLabel)
	}

	w.h.crossUserOn(t)

	if status, res, e := w.claim(w.unbound, "L-after", "", false); status != http.StatusOK || res.Code != wire.CodeGranted {
		t.Errorf("a claim from the approved label with cross-user features on: status %d, %+v, %+v", status, res, e)
	}
}

// TestApprovalOnAFreshBrokerBindsToTheOwnerItsStoreAssigned proves a broker that never ran an upgrade
// binds an approved label to the owner its store assigned at open. It catches a fresh broker with no
// owner, whose approvals pin keys and bind nothing.
func TestApprovalOnAFreshBrokerBindsToTheOwnerItsStoreAssigned(t *testing.T) {
	h := newHarness(t, nil)

	owner := h.data().Users[h.data().OwnerID]
	label := h.register(alice).Label

	ans, err := h.b.Approve(label, wire.Fingerprint(wire.KeyHash(alice.Key)))
	if err != nil || !ans.Pinned || owner.UserLabel == "" || ans.Owner != owner.UserLabel {
		t.Fatalf("approve = %+v, %v, want pinned and bound to the owner %q", ans, err, owner.UserLabel)
	}

	if got := h.data().Bindings[label]; got.UserID != owner.ID {
		t.Errorf("binding = %+v, want the owner %s", got, owner.ID)
	}
}

// TestApprovalBindsToAnOwnerWhoseUserRecordWentMissing proves a broker whose store names an owner with no
// user record restores that owner under the same ID when it opens, and approval then binds to it. It
// catches an open that leaves the owner unresolved, so approval pins keys and binds nothing, and one that
// mints a new owner, which orphans the labels already bound to the old ID.
func TestApprovalBindsToAnOwnerWhoseUserRecordWentMissing(t *testing.T) {
	h := newHarness(t, nil)

	ownerID := h.data().OwnerID

	h.b.mu.Lock()
	err := h.b.commit(func(d *Data) error {
		delete(d.Users, d.OwnerID)

		return nil
	})
	h.b.mu.Unlock()

	if err != nil {
		t.Fatal(err)
	}

	h.restart(nil)

	owner, ok := h.data().Users[ownerID]
	if got := h.data().OwnerID; got != ownerID || !ok || owner.ID != ownerID || owner.UserLabel != DeriveUserLabel(ownerID) {
		t.Fatalf("after the restart: owner %q, record %+v, want %q restored", got, owner, ownerID)
	}

	label := h.register(alice).Label

	ans, err := h.b.Approve(label, wire.Fingerprint(wire.KeyHash(alice.Key)))
	if err != nil || !ans.Pinned || ans.Owner != owner.UserLabel {
		t.Fatalf("approve = %+v, %v, want pinned and bound to the owner %s", ans, err, owner.UserLabel)
	}

	if got := h.data().Bindings[label]; got.UserID != ownerID {
		t.Errorf("binding = %+v, want the owner %s", got, ownerID)
	}
}

// TestApprovingAFingerprintInAdvanceBindsTheLabelWhenItsKeyArrives proves a fingerprint approved before
// its key reached the broker binds the label to the owner when the key arrives and pins. It catches a
// registration that pins an awaited key and binds nothing.
func TestApprovingAFingerprintInAdvanceBindsTheLabelWhenItsKeyArrives(t *testing.T) {
	w := newUnboundWorld(t)

	w.h.legacyLease(t, w.unbound, w.unboundLabel, "L-legacy")

	arrived := nodeID{Machine: w.unbound.Machine, UID: w.unbound.UID, Key: "key-unbound-real"}

	ans, err := w.h.b.Approve(w.unboundLabel, wire.Fingerprint(wire.KeyHash(arrived.Key)))
	if err != nil || ans.Pinned || ans.Owner != w.owner.UserLabel {
		t.Fatalf("approve in advance = %+v, %v, want it awaited for the owner %s", ans, err, w.owner.UserLabel)
	}

	if _, bound := w.h.data().Bindings[w.unboundLabel]; bound {
		t.Fatal("the label is bound before its key arrived")
	}

	w.h.register(arrived)

	if got := w.h.data().Pins[w.unboundLabel]; got.PinnedHash != wire.KeyHash(arrived.Key) {
		t.Fatalf("pin after the key arrived = %+v", got)
	}

	if got := w.h.data().Bindings[w.unboundLabel]; got.UserID != w.owner.ID {
		t.Errorf("binding after the key arrived = %+v, want the owner %s", got, w.owner.ID)
	}

	if held, stored := w.h.leaseActingFor(t, "L-legacy"); held != w.owner.UserLabel || stored != w.owner.UserLabel {
		t.Errorf("the legacy lease: held %q, stored %q, want the owner %q", held, stored, w.owner.UserLabel)
	}
}

// TestABindingToADifferentUserBouncesEveryMessageHeldForTheLabel proves a label forgotten with mail held
// for it, then bound to a different user, bounces that mail to its senders with its body, and the new
// user's node never receives it. One message was sent and never acked, and one queued after the forget.
// The binding also drops the delegation a third user made to the label. It catches a rebind that bounces
// only stored mail, which leaves queued mail for the new user's node, and one that keeps a delegation the
// third user made to someone else's machine.
func TestABindingToADifferentUserBouncesEveryMessageHeldForTheLabel(t *testing.T) {
	h := newCrossUserHarness(t)

	// The sender is the owner, whose scope a forgotten label counts in while cross-user features are off,
	// so its mail to the forgotten label stays mail between one user's machines.
	a := h.data().Users[h.data().OwnerID]

	b, err := h.b.AdmitUser(createIssuer, "b@example.com", "b@example.com", wallStart)
	if err != nil {
		t.Fatal(err)
	}

	c, err := h.b.AdmitUser(createIssuer, "c@example.com", "c@example.com", wallStart)
	if err != nil {
		t.Fatal(err)
	}

	for _, n := range []nodeID{alice, bob} {
		h.registerMail(n)
		h.bindNode(t, n, a.ID)
	}

	fromLabel := h.data().Nodes[alice.key().String()].Label
	toLabel := h.data().Nodes[bob.key().String()].Label

	h.delegate(t, c, toLabel)

	h.reportSessions(alice, senderSession)
	h.reportSessions(bob, recipientSession)

	to := h.mailName(toLabel, recipientSession)

	r, _, _ := h.openStream(bob)

	if resp := h.send(alice, mailIDs[0], to, "sent, never acked", senderSession); resp.StatusCode != http.StatusOK {
		t.Fatalf("send: status %d", resp.StatusCode)
	}

	if got := mailEvent(t, nextKind(t, r, wire.EventMail)); got.Body != "sent, never acked" {
		t.Fatalf("the old user's node got %+v", got)
	}

	if err := h.b.Forget(toLabel); err != nil {
		t.Fatal(err)
	}

	if resp := h.send(alice, mailIDs[1], to, "queued after the forget", senderSession); resp.StatusCode != http.StatusOK {
		t.Fatalf("send to the forgotten label: status %d", resp.StatusCode)
	}

	h.wall.Set(wallStart.Add(time.Hour))

	newBob := nodeID{Machine: bob.Machine, UID: bob.UID, Key: "reissued"}
	h.registerMail(newBob)
	h.bindNode(t, newBob, b.ID)

	if _, ok := h.data().Delegations[c.ID][toLabel]; ok {
		t.Error("the binding to a different user kept C's delegation to the label")
	}

	// The new user's node is served only once cross-user features are on.
	h.crossUserOn(t)

	bodies := map[string]string{mailIDs[0]: "sent, never acked", mailIDs[1]: "queued after the forget"}

	for id := range bodies {
		if got := h.data().Mail[id]; !got.Bounced || got.BounceReason != wire.ReasonRebound || !got.StageAt.Equal(wallStart.Add(time.Hour)) {
			t.Errorf("message %s = %+v, want bounced for the rebind at the broker's clock", id, got)
		}
	}

	bounced := map[string]string{}

	for _, m := range h.data().Mail {
		if m.Kind == wire.EventBounce && m.ToLabel == fromLabel && m.ToSession == senderSession {
			bounced[m.Orig] = m.Body
		}
	}

	if len(bounced) != len(bodies) || bounced[mailIDs[0]] != bodies[mailIDs[0]] || bounced[mailIDs[1]] != bodies[mailIDs[1]] {
		t.Errorf("bounces to the sender = %v, want both messages with their bodies", bounced)
	}

	h.report(newBob, wire.MailReport{Complete: true})

	nr, _, _ := h.openStream(newBob)
	h.reportSessions(newBob, recipientSession)

	if got := mailOf(h.settle(newBob, nr)); len(got) != 0 {
		t.Errorf("the new user's node received %v", kinds(got))
	}
}

// pagePairingCode is the code the page rebind tests pair with.
const pagePairingCode = "page-pairing-code-words"

// bindOnPage has node n send a pairing code for label and submits the page's bind form with it as the
// signed-in user, as a user binding a machine on the sign-in page does. A node the broker refuses to pair
// leaves no code, so the bind meets the page's answer to a wrong code.
func bindOnPage(t *testing.T, h *harness, page *oidctest.Browser, n nodeID, label string) (status int, body string) {
	t.Helper()

	h.pair(n, pagePairingCode)

	return page.Submit("bind", url.Values{"label": {label}, "code": {pagePairingCode}})
}

// pageRebindWorld is a broker with the sign-in page and cross-user features off. Alice's and Bob's labels
// are bound to the owner A and hold mail sessions, and the page's signer is B.
type pageRebindWorld struct {
	h    *harness
	page *oidctest.Browser

	a, b             UserRecord
	fromLabel, label string
	to               string
}

func newPageRebindWorld(t *testing.T) *pageRebindWorld {
	t.Helper()

	iss := oidctest.New(t, "b-sub", "b@example.com")
	h, page := pageHarness(t, iss)

	b, err := h.b.AdmitUser(iss.URL, "b-sub", "b@example.com", wallStart)
	if err != nil {
		t.Fatal(err)
	}

	a := h.data().Users[h.data().OwnerID]

	for _, n := range []nodeID{alice, bob} {
		h.registerMail(n)
		h.bindNode(t, n, a.ID)
	}

	w := &pageRebindWorld{h: h, page: page, a: a, b: b}
	w.fromLabel = h.data().Nodes[alice.key().String()].Label
	w.label = h.data().Nodes[bob.key().String()].Label

	h.reportSessions(alice, senderSession)
	h.reportSessions(bob, recipientSession)

	w.to = h.mailName(w.label, recipientSession)

	return w
}

// TestAPageRebindStampsItsBouncesWithTheBrokersClock proves a bind on the sign-in page that moves a
// forgotten label to a different user bounces the mail held for it at the broker's clock, and records the
// binding at that clock. It catches a page bind that reads the process clock, which stamps the bounce out
// of step with every other stage the broker records.
func TestAPageRebindStampsItsBouncesWithTheBrokersClock(t *testing.T) {
	w := newPageRebindWorld(t)

	if resp := w.h.send(alice, mailIDs[0], w.to, "held for the label", senderSession); resp.StatusCode != http.StatusOK {
		t.Fatalf("send: status %d", resp.StatusCode)
	}

	if err := w.h.b.Forget(w.label); err != nil {
		t.Fatal(err)
	}

	rebindAt := wallStart.Add(time.Hour)
	w.h.wall.Set(rebindAt)
	reissued := nodeID{Machine: bob.Machine, UID: bob.UID, Key: "reissued"}
	w.h.registerMail(reissued)

	if status, body := bindOnPage(t, w.h, w.page, reissued, w.label); status != http.StatusOK {
		t.Fatalf("page bind: status %d, %s", status, body)
	}

	if got := w.h.data().Bindings[w.label]; got.UserID != w.b.ID || !got.BoundAt.Equal(rebindAt) {
		t.Errorf("binding = %+v, want B bound at the broker's clock %v", got, rebindAt)
	}

	if got := w.h.data().Mail[mailIDs[0]]; !got.Bounced || got.BounceReason != wire.ReasonRebound || !got.StageAt.Equal(rebindAt) {
		t.Errorf("message = %+v, want bounced for the rebind at the broker's clock %v", got, rebindAt)
	}

	for _, m := range w.h.data().Mail {
		if m.Kind == wire.EventBounce && m.Orig == mailIDs[0] && !m.EnqueuedAt.Equal(rebindAt) {
			t.Errorf("bounce = %+v, want it queued at the broker's clock %v", m, rebindAt)
		}
	}
}

// TestAPinnedLabelIsNeverReboundWhileTheBrokerAwaitsAnAck proves the page cannot bind a pinned label
// straight to a different user. Mail the broker sent to the label and saw no ack for then stays with the
// machine that may hold it, unbounced and still marked sent. A rebind needs an unpinned label, and only a
// forget unpins one. The forget clears the sent mark of each queued message, so a rebind never meets one
// marked sent. It catches a page bind that takes a pinned label for another user, which would bounce mail
// the old machine may already hold.
func TestAPinnedLabelIsNeverReboundWhileTheBrokerAwaitsAnAck(t *testing.T) {
	w := newPageRebindWorld(t)

	r, _, _ := w.h.openStream(bob)

	if resp := w.h.send(alice, mailIDs[0], w.to, "sent, never acked", senderSession); resp.StatusCode != http.StatusOK {
		t.Fatalf("send: status %d", resp.StatusCode)
	}

	if got := mailEvent(t, nextKind(t, r, wire.EventMail)); got.ID != mailIDs[0] {
		t.Fatalf("A's node got %+v", got)
	}

	if status, _ := bindOnPage(t, w.h, w.page, bob, w.label); status == http.StatusOK {
		t.Fatal("the page bound a pinned label to a different user without a forget")
	}

	if got := w.h.data().Bindings[w.label]; got.UserID != w.a.ID {
		t.Errorf("binding = %+v, want A's kept", got)
	}

	if got := w.h.data().Mail[mailIDs[0]]; got.Bounced || !got.Sent || got.Stage != wire.StageQueued {
		t.Errorf("message = %+v, want it sent, unacked, and never bounced", got)
	}
}

// TestApprovingAPinnedLabelAgainBindsItUnlessACoworkerHoldsIt proves approving a label again under its
// pinned key binds it to the owner when it was pinned and left unbound, as an approval on a store whose
// owner record was missing leaves it. A label a coworker bound through the page is refused and keeps its
// binding. It catches a repeat approval that does nothing, which leaves an unbound label no way back, and
// one that takes a coworker's label for the owner and bounces the coworker's mail.
func TestApprovingAPinnedLabelAgainBindsItUnlessACoworkerHoldsIt(t *testing.T) {
	h := newHarness(t, nil)

	owner := h.data().Users[h.data().OwnerID]
	setOwner := func(id string) {
		h.b.mu.Lock()
		defer h.b.mu.Unlock()

		err := h.b.commit(func(d *Data) error {
			d.OwnerID = id

			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	setOwner("")

	label := h.register(alice).Label
	fp := wire.Fingerprint(wire.KeyHash(alice.Key))

	if ans, err := h.b.Approve(label, fp); err != nil || !ans.Pinned || ans.Owner != "" {
		t.Fatalf("approve with no owner = %+v, %v, want pinned and bound to nobody", ans, err)
	}

	if _, bound := h.data().Bindings[label]; bound {
		t.Fatal("a broker with no owner bound the label")
	}

	setOwner(owner.ID)

	if ans, err := h.b.Approve(label, fp); err != nil || !ans.Pinned || ans.Owner != owner.UserLabel {
		t.Fatalf("approve again = %+v, %v, want bound to the owner %s", ans, err, owner.UserLabel)
	}

	if got := h.data().Bindings[label]; got.UserID != owner.ID {
		t.Errorf("binding after approving again = %+v, want the owner", got)
	}

	coworker, err := h.b.AdmitUser(createIssuer, "coworker@example.com", "coworker@example.com", wallStart)
	if err != nil {
		t.Fatal(err)
	}

	h.register(bob)
	coworkerLabel := h.bindNode(t, bob, coworker.ID)

	if _, err := h.b.Approve(coworkerLabel, wire.Fingerprint(wire.KeyHash(bob.Key))); !errors.Is(err, ErrAlreadyPinned) ||
		!strings.Contains(err.Error(), coworker.UserLabel) {
		t.Errorf("approve of the coworker's label = %v, want a refusal naming %s", err, coworker.UserLabel)
	}

	if got := h.data().Bindings[coworkerLabel]; got.UserID != coworker.ID {
		t.Errorf("the coworker's binding = %+v, want it kept", got)
	}
}

// TestRebindToSameUserKeepsMailAndDelegations proves a label forgotten and then bound again to the same
// user, as a reinstalled machine is, keeps the mail queued for it and a delegation another user made to it.
// It catches a rebind that treats every binding after a forget as a new user, which bounces the user's own
// mail and drops a delegation nobody revoked.
func TestRebindToSameUserKeepsMailAndDelegations(t *testing.T) {
	h := newCrossUserHarness(t)

	a := h.data().Users[h.data().OwnerID]

	c, err := h.b.AdmitUser(createIssuer, "c@example.com", "c@example.com", wallStart)
	if err != nil {
		t.Fatal(err)
	}

	for _, n := range []nodeID{alice, bob} {
		h.registerMail(n)
		h.bindNode(t, n, a.ID)
	}

	toLabel := h.data().Nodes[bob.key().String()].Label

	h.delegate(t, c, toLabel)

	h.reportSessions(alice, senderSession)
	h.reportSessions(bob, recipientSession)

	to := h.mailName(toLabel, recipientSession)

	if err := h.b.Forget(toLabel); err != nil {
		t.Fatal(err)
	}

	if resp := h.send(alice, mailIDs[0], to, "queued after the forget", senderSession); resp.StatusCode != http.StatusOK {
		t.Fatalf("send to the forgotten label: status %d", resp.StatusCode)
	}

	newBob := nodeID{Machine: bob.Machine, UID: bob.UID, Key: "reissued"}
	h.registerMail(newBob)
	h.bindNode(t, newBob, a.ID)

	if got := h.data().Mail[mailIDs[0]]; got.Bounced {
		t.Errorf("message = %+v, bounced by a rebind to the same user", got)
	}

	if _, ok := h.data().Delegations[c.ID][toLabel]; !ok {
		t.Error("the rebind to the same user dropped C's delegation to the label")
	}
}
