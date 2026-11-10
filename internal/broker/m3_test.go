package broker

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/wire"
)

// crossWorld is a broker with cross-user features on. U's node holds the recipient session and V's node
// the sender session. Both speak mail across users unless a test registers one of them again.
type crossWorld struct {
	h *harness

	u, v         UserRecord
	uNode, vNode nodeID

	uLabel, vLabel string
	uAddr, vAddr   string
}

var (
	crossU = nodeID{Machine: "machine-u", UID: 501, Key: "key-u"}
	crossV = nodeID{Machine: "machine-v", UID: 501, Key: "key-v"}
)

func newCrossWorld(t *testing.T) *crossWorld {
	t.Helper()

	h := newHarness(t, nil)

	u, err := h.b.CreateUser("u@example.com", wallStart)
	if err != nil {
		t.Fatal(err)
	}

	v, err := h.b.CreateUser("v@example.com", wallStart)
	if err != nil {
		t.Fatal(err)
	}

	w := &crossWorld{h: h, u: u, v: v, uNode: crossU, vNode: crossV}

	w.uLabel = w.bindMail(t, crossU, u.ID, recipientSession)
	w.vLabel = w.bindMail(t, crossV, v.ID, senderSession)
	w.uAddr, w.vAddr = h.mailName(w.uLabel, recipientSession), h.mailName(w.vLabel, senderSession)

	h.crossUserOn(t)

	return w
}

// bindMail registers a node that speaks mail across users, binds its label to a user as the page does,
// and reports its live sessions.
func (w *crossWorld) bindMail(t *testing.T, n nodeID, userID string, sessions ...string) string {
	t.Helper()

	label := w.h.registerMail(n)

	if err := w.h.b.BindLabelToUser(label, userID, wallStart); err != nil {
		t.Fatal(err)
	}

	w.h.reportSessions(n, sessions...)

	return label
}

// registerTooOld registers a node again below the cross-user mail capability, as an older binary on the
// same key would.
func (h *harness) registerTooOld(n nodeID) {
	h.t.Helper()

	resp := h.do(http.MethodPost, wire.PathRegister, n, wire.Register{
		Version: wire.Current, MachineID: n.Machine, UID: n.UID, User: "u", Host: "host-" + n.Machine, NodeVersion: "old",
		HTTPS: true, MailCapability: wire.MailCapabilityBasic,
	})
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("register %+v below the cross-user capability: status %d", n, resp.StatusCode)
	}
}

// grantMail makes U's grant to V, as U's page action does after U signs in.
func (w *crossWorld) grantMail(t *testing.T) {
	t.Helper()

	w.h.signIn(t, w.u)

	if _, _, err := w.h.b.GrantMail(testDelegationIssuer, w.u.ID, w.v.UserLabel, wallStart); err != nil {
		t.Fatal(err)
	}
}

// revokeMail withdraws U's grant to V, as U's page action does.
func (w *crossWorld) revokeMail(t *testing.T) {
	t.Helper()

	w.h.signIn(t, w.u)

	if _, _, err := w.h.b.RevokeMail(testDelegationIssuer, w.u.ID, w.v.UserLabel); err != nil {
		t.Fatal(err)
	}
}

// sharedLease gives the session at n a lease at the reach, and returns its ID.
func (w *crossWorld) sharedLease(t *testing.T, n nodeID, id, reach, session string) {
	t.Helper()

	var res wire.Result

	status := w.h.call(n, wire.PathClaim, wire.Claim{
		Version: wire.Current, ID: id, Reach: reach, Entries: name("svc-" + id), Derivation: 1, Anchor: "anchor-" + id,
		Holder: wire.Holder{Key: "k-" + id, Agent: "claude", SessionID: session},
	}, &res)
	if status != http.StatusOK || res.Code != wire.CodeGranted {
		t.Fatalf("claim %s at %s: status %d, %+v", id, reach, status, res)
	}
}

func (w *crossWorld) release(t *testing.T, n nodeID, id, session string) {
	t.Helper()

	var res wire.Result

	status := w.h.call(n, wire.PathRelease, wire.Release{
		Version: wire.Current, RequestID: "rel-" + id, ID: id, Holder: wire.Holder{Key: "k-" + id, Agent: "claude", SessionID: session},
		Cause: wire.CauseReleased,
	}, &res)
	if status != http.StatusOK {
		t.Fatalf("release %s: status %d, %+v", id, status, res)
	}
}

// reply sends a reply from U's recipient session to V's sender session.
func (w *crossWorld) reply(id, replyTo string) *http.Response {
	w.h.t.Helper()

	return w.h.do(http.MethodPost, wire.PathMailSend, w.uNode, wire.MailSend{
		Version: wire.Current, ID: id, To: w.vAddr, Body: "the answer", FromAgent: "claude", FromSession: recipientSession, ReplyTo: replyTo,
	})
}

// report sends a node's mail report. A complete one is what a reconnecting node sends first.
func (h *harness) report(n nodeID, rep wire.MailReport) {
	h.t.Helper()

	rep.Version = wire.Current

	if resp := h.do(http.MethodPost, wire.PathMailReport, n, rep); resp.StatusCode != http.StatusNoContent {
		h.t.Fatalf("mail report: status %d", resp.StatusCode)
	}
}

// firstMessage grants, gives U's recipient session a `shared` lease, and sends V's first message.
func (w *crossWorld) firstMessage(t *testing.T, id string) {
	t.Helper()

	w.grantMail(t)
	w.sharedLease(t, w.uNode, "L-u", wire.ReachShared, recipientSession)

	if resp := w.h.send(w.vNode, id, w.uAddr, "a question", senderSession); resp.StatusCode != http.StatusOK {
		t.Fatalf("first message: status %d, code %s", resp.StatusCode, errCode(t, resp))
	}
}

// readByU marks a message stored and read on U's machine, as U's node reports a reader's read.
func (w *crossWorld) readByU(id string) {
	w.h.report(w.uNode, wire.MailReport{Stored: []string{id}, Read: []string{id}})
}

// openReported opens a node's stream after its complete report, as a reconnecting node does, settles it,
// and returns the events it sent.
func (w *crossWorld) openReported(n nodeID) []wire.SSE {
	w.h.report(n, wire.MailReport{Complete: true})

	r, _, _ := w.h.openStream(n)

	return w.h.settle(n, r)
}

// settle enqueues a probe twice and reads the stream up to each, acking it. The stream writes the second
// probe only in a pass of its loop after the one that wrote the first, so the mail flush of that earlier
// pass has finished. It returns the other events read on the way.
func (h *harness) settle(n nodeID, r *bufio.Reader) []wire.SSE {
	h.t.Helper()

	var other []wire.SSE

	for range 2 {
		id, err := h.b.Enqueue(n.key(), "probe", map[string]string{})
		if err != nil {
			h.t.Fatal(err)
		}

		for {
			ev := nextEvent(h.t, r)
			if ev.Event == "probe" && ev.ID == id {
				break
			}

			if ev.Event != "probe" {
				other = append(other, ev)
			}
		}

		if resp := h.do(http.MethodPost, wire.PathAck, n, wire.Ack{Version: wire.Current, EventID: id}); resp.StatusCode/100 != 2 {
			h.t.Fatalf("ack %s: status %d", id, resp.StatusCode)
		}
	}

	return other
}

// mailOf keeps the mail and bounce events.
func mailOf(evs []wire.SSE) []wire.SSE {
	var out []wire.SSE

	for _, ev := range evs {
		if ev.Event == wire.EventMail || ev.Event == wire.EventBounce {
			out = append(out, ev)
		}
	}

	return out
}

func kinds(evs []wire.SSE) []string {
	out := make([]string, 0, len(evs))
	for _, ev := range evs {
		out = append(out, ev.Event)
	}

	return out
}

// TestCrossUserMailCarriesTheSenderUserAndNoSenderVerified proves mail from V's node to U's carries V's
// user label in its own field and leaves sender_verified out of the event entirely. It catches a broker
// that stamps a coworker's mail as the user's own, which an older reader would show as verified.
func TestCrossUserMailCarriesTheSenderUserAndNoSenderVerified(t *testing.T) {
	w := newCrossWorld(t)
	w.firstMessage(t, mailIDs[0])

	evs := w.openReported(w.uNode)
	if got := mailOf(evs); len(got) != 1 || got[0].Event != wire.EventMail {
		t.Fatalf("the recipient's stream sent %v, want one message", kinds(evs))
	}

	ev := mailOf(evs)[0]
	got := mailEvent(t, ev)

	if got.SenderUser != w.v.UserLabel || got.SenderVerified || got.Sender != w.vAddr || got.Body != "a question" {
		t.Errorf("event = %+v, want sender user %s and no sender_verified", got, w.v.UserLabel)
	}

	if strings.Contains(ev.Data, "sender_verified") {
		t.Errorf("the event carries sender_verified: %s", ev.Data)
	}
}

// TestMailBetweenOneUsersBoundMachinesKeepsSenderVerified proves that mail between two labels bound to
// one user keeps sender_verified, and carries no sender user, while cross-user features are on.
// It catches a cross-user check that treats every bound label as another user's.
func TestMailBetweenOneUsersBoundMachinesKeepsSenderVerified(t *testing.T) {
	w := newCrossWorld(t)

	second := nodeID{Machine: "machine-u2", UID: 501, Key: "key-u2"}
	label := w.bindMail(t, second, w.u.ID, senderSession)

	if resp := w.h.send(second, mailIDs[0], w.uAddr, "from my other machine", senderSession); resp.StatusCode != http.StatusOK {
		t.Fatalf("same-user send: status %d, code %s", resp.StatusCode, errCode(t, resp))
	}

	r, _, _ := w.h.openStream(w.uNode)
	got := mailEvent(t, nextKind(t, r, wire.EventMail))

	if !got.SenderVerified || got.SenderUser != "" || got.Sender != w.h.mailName(label, senderSession) {
		t.Errorf("event = %+v, want verified with no sender user", got)
	}
}

// TestCrossUserFirstMessageNeedsTheRecipientsGrant proves a first message from V to U is refused until U
// grants V, and accepted once U does. It catches a send path that lets any bound coworker mail U.
func TestCrossUserFirstMessageNeedsTheRecipientsGrant(t *testing.T) {
	w := newCrossWorld(t)
	w.sharedLease(t, w.uNode, "L-u", wire.ReachShared, recipientSession)

	resp := w.h.send(w.vNode, mailIDs[0], w.uAddr, "x", senderSession)
	if resp.StatusCode != http.StatusForbidden || errCode(t, resp) != wire.CodeNotGranted {
		t.Fatalf("a send with no grant got status %d", resp.StatusCode)
	}

	if len(w.h.data().Mail) != 0 {
		t.Errorf("a refused send left mail: %+v", w.h.data().Mail)
	}

	w.grantMail(t)

	if resp := w.h.send(w.vNode, mailIDs[1], w.uAddr, "x", senderSession); resp.StatusCode != http.StatusOK {
		t.Errorf("a send under U's grant got status %d", resp.StatusCode)
	}
}

// TestAGrantRunsOneWay proves U's grant to V admits V's first messages to U and none of U's to V. It
// catches a grant check that reads the grant map the wrong way round.
func TestAGrantRunsOneWay(t *testing.T) {
	w := newCrossWorld(t)
	w.grantMail(t)
	w.sharedLease(t, w.vNode, "L-v", wire.ReachShared, senderSession)

	resp := w.h.do(http.MethodPost, wire.PathMailSend, w.uNode, wire.MailSend{
		Version: wire.Current, ID: mailIDs[0], To: w.vAddr, Body: "x", FromAgent: "claude", FromSession: recipientSession,
	})
	if resp.StatusCode != http.StatusForbidden || errCode(t, resp) != wire.CodeNotGranted {
		t.Errorf("U's first message to V rode U's own grant: status %d", resp.StatusCode)
	}
}

type reachTest struct {
	Name  string
	Reach string
	Lapse bool
	Take  bool
	Want  int
}

var reachTests = []reachTest{
	{Name: "no lease", Want: http.StatusForbidden},
	{Name: "a user lease", Reach: wire.ReachUser, Want: http.StatusForbidden},
	{Name: "a live shared lease", Reach: wire.ReachShared, Want: http.StatusOK},
	{Name: "a lapsed shared lease", Reach: wire.ReachShared, Lapse: true, Want: http.StatusOK},
	{Name: "a taken marker", Reach: wire.ReachShared, Lapse: true, Take: true, Want: http.StatusForbidden},
}

// TestCrossUserFirstMessageReachesOnlyASessionHoldingALiveOrLapsedSharedLease proves that even under a
// grant, a first message reaches only an address whose session holds a live or lapsed `shared` lease,
// and that a lease another holder took is not reach. It catches a reach rule that admits a `user`
// lease, forgets a lapsed one, or counts a taken marker.
func TestCrossUserFirstMessageReachesOnlyASessionHoldingALiveOrLapsedSharedLease(t *testing.T) {
	for _, test := range reachTests {
		t.Run(test.Name, func(t *testing.T) {
			w := newCrossWorld(t)
			w.grantMail(t)

			if test.Reach != "" {
				w.sharedLease(t, w.uNode, "L-u", test.Reach, recipientSession)
			}

			if test.Lapse {
				w.h.clock.Sleep(2 * testTerm)
				w.h.clock.Advance(testTerm)
				w.h.b.Sweep()
			}

			if test.Take {
				other := nodeID{Machine: "machine-u3", UID: 501, Key: "key-u3"}
				w.bindMail(t, other, w.u.ID)

				var res wire.Result

				status := w.h.call(other, wire.PathClaim, wire.Claim{
					Version: wire.Current, ID: "L-take", Reach: wire.ReachShared, Entries: name("svc-L-u"), Derivation: 1,
					Anchor: "anchor-take", Holder: holder("taker"),
				}, &res)
				if status != http.StatusOK || res.Code != wire.CodeGranted {
					t.Fatalf("the overlapping claim: status %d, %+v", status, res)
				}

				if got := w.h.b.Facts(); len(got) != 1 || got[0].State != wire.StateTaken {
					t.Fatalf("facts = %+v, want the taken marker", got)
				}
			}

			resp := w.h.send(w.vNode, mailIDs[0], w.uAddr, "x", senderSession)
			if resp.StatusCode != test.Want {
				t.Errorf("status = %d, want %d", resp.StatusCode, test.Want)
			}
		})
	}
}

// TestMailToASharedAgentFollowsTheAgentsOwnUsersGrants proves mail to a shared agent is judged by the
// grants of the agent's own bound user, even when its `shared` lease is held for a delegating user. It
// catches a broker that reads acting_for to find the recipient's user.
func TestMailToASharedAgentFollowsTheAgentsOwnUsersGrants(t *testing.T) {
	w := newCrossWorld(t)
	w.grantMail(t)

	owner, err := w.h.b.CreateUser("agent-owner@example.com", wallStart)
	if err != nil {
		t.Fatal(err)
	}

	agent := nodeID{Machine: "machine-agent", UID: 501, Key: "key-agent"}
	agentLabel := w.bindMail(t, agent, owner.ID, recipientSession)
	w.h.delegate(t, w.u, agentLabel)

	var res wire.Result

	status := w.h.call(agent, wire.PathClaim, wire.Claim{
		Version: wire.Current, ID: "L-agent", Reach: wire.ReachShared, Entries: name("svc-agent"), Derivation: 1, Anchor: "anchor-agent",
		Holder: wire.Holder{Key: "k-agent", Agent: "claude", SessionID: recipientSession}, ActingFor: w.u.UserLabel, ActingForExplicit: true,
	}, &res)
	if status != http.StatusOK || res.Code != wire.CodeGranted || res.Lease.ActingFor != w.u.UserLabel {
		t.Fatalf("the agent's claim for U: status %d, %+v", status, res)
	}

	to := w.h.mailName(agentLabel, recipientSession)

	if resp := w.h.send(w.vNode, mailIDs[0], to, "x", senderSession); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("U's grant admitted mail to an agent U delegated to: status %d", resp.StatusCode)
	}

	w.h.signIn(t, owner)

	if _, _, err := w.h.b.GrantMail(testDelegationIssuer, owner.ID, w.v.UserLabel, wallStart); err != nil {
		t.Fatal(err)
	}

	if resp := w.h.send(w.vNode, mailIDs[1], to, "x", senderSession); resp.StatusCode != http.StatusOK {
		t.Errorf("the agent's own user's grant did not admit mail: status %d", resp.StatusCode)
	}
}

// TestCrossUserMailIsRefusedWhileCrossUserFeaturesAreOff proves a broker with cross-user features off
// carries no mail between two users' labels, grant or not. It catches a send path that skips the switch.
func TestCrossUserMailIsRefusedWhileCrossUserFeaturesAreOff(t *testing.T) {
	w := newCrossWorld(t)
	w.grantMail(t)
	w.sharedLease(t, w.uNode, "L-u", wire.ReachShared, recipientSession)

	w.h.b.mu.Lock()
	err := w.h.b.commit(func(d *Data) error {
		d.CrossUserEnabled = false

		return nil
	})
	w.h.b.mu.Unlock()

	if err != nil {
		t.Fatal(err)
	}

	resp := w.h.send(w.vNode, mailIDs[0], w.uAddr, "x", senderSession)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(errMessage(t, resp), "cross-user features on") {
		t.Errorf("status = %d, want refused naming the switch", resp.StatusCode)
	}
}

func errMessage(t *testing.T, resp *http.Response) string {
	t.Helper()

	var e wire.Error
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		t.Fatalf("status %d with no error body: %v", resp.StatusCode, err)
	}

	return e.Code + ": " + e.Message
}

// TestACrossUserSendFromAnOldSenderNodeIsRefused proves a sender node that registered below the
// cross-user mail capability is refused at send with its own code. It catches a broker that accepts mail
// from a node that cannot show bounces for a revoked grant or a lapsed reach.
func TestACrossUserSendFromAnOldSenderNodeIsRefused(t *testing.T) {
	w := newCrossWorld(t)
	w.firstMessage(t, mailIDs[0])

	w.h.registerTooOld(w.vNode)
	w.h.reportSessions(w.vNode, senderSession)

	resp := w.h.send(w.vNode, mailIDs[1], w.uAddr, "x", senderSession)
	if resp.StatusCode != http.StatusForbidden || errCode(t, resp) != wire.CodeSenderTooOld {
		t.Errorf("an old sender node got status %d", resp.StatusCode)
	}
}

// TestACrossUserSendToAnOldRecipientNodeIsToldRecipientNodeTooOld proves the sender learns "recipient
// node too old" at send when U's node registered below the cross-user capability, and that U's own
// mail still reaches that node. It catches a capability check that refuses all mail to an older node, or
// none.
func TestACrossUserSendToAnOldRecipientNodeIsToldRecipientNodeTooOld(t *testing.T) {
	w := newCrossWorld(t)
	w.grantMail(t)
	w.sharedLease(t, w.uNode, "L-u", wire.ReachShared, recipientSession)

	w.h.registerTooOld(w.uNode)

	resp := w.h.send(w.vNode, mailIDs[0], w.uAddr, "x", senderSession)
	if msg := errMessage(t, resp); resp.StatusCode != http.StatusConflict || !strings.Contains(msg, wire.CodeNodeTooOld) ||
		!strings.Contains(msg, "recipient node too old") {
		t.Errorf("status %d, %s", resp.StatusCode, msg)
	}

	second := nodeID{Machine: "machine-u2", UID: 501, Key: "key-u2"}
	w.bindMail(t, second, w.u.ID, senderSession)

	if resp := w.h.send(second, mailIDs[1], w.uAddr, "mine", senderSession); resp.StatusCode != http.StatusOK {
		t.Errorf("same-user mail to an older mail node got status %d", resp.StatusCode)
	}
}

// TestQueuedCrossUserMailBouncesWhenTheRecipientNodeLaterRegistersTooOld proves mail queued for U's node
// bounces with "the recipient node is too old" when that node comes back below the cross-user capability.
// It catches a broker that hands a coworker's mail to a reader that would show it as unverified only by
// luck.
func TestQueuedCrossUserMailBouncesWhenTheRecipientNodeLaterRegistersTooOld(t *testing.T) {
	w := newCrossWorld(t)
	w.firstMessage(t, mailIDs[0])

	w.h.registerTooOld(w.uNode)

	r, _, _ := w.h.openStream(w.uNode)

	if evs := w.h.settle(w.uNode, r); len(mailOf(evs)) != 0 {
		t.Errorf("an old node was sent %v", kinds(evs))
	}

	if got := w.h.data().Mail[mailIDs[0]]; !got.Bounced || got.BounceReason != wire.ReasonNodeTooOld {
		t.Errorf("original = %+v, want bounced because the node is too old", got)
	}
}

// TestMailSentToANodeThatComesBackTooOldWaitsForItsCompleteReport proves cross-user mail the broker sent to
// U's node, and saw no ack for, is not bounced when the node comes back below the cross-user capability
// until that connection's complete report arrives. A message the report lists as stored is never bounced.
// One it leaves out bounces with "the recipient node is too old". It catches a broker that bounces a
// message the node stored before its ack was lost, which the old binary may then show to its reader.
func TestMailSentToANodeThatComesBackTooOldWaitsForItsCompleteReport(t *testing.T) {
	w := newCrossWorld(t)
	w.firstMessage(t, mailIDs[0])

	if resp := w.h.send(w.vNode, mailIDs[1], w.uAddr, "never stored", senderSession); resp.StatusCode != http.StatusOK {
		t.Fatalf("second message: status %d", resp.StatusCode)
	}

	r, _, _ := w.h.openStream(w.uNode)
	w.h.report(w.uNode, wire.MailReport{Complete: true})
	nextKind(t, r, wire.EventMail)
	nextKind(t, r, wire.EventMail)

	w.h.registerTooOld(w.uNode)

	r2, _, _ := w.h.openStream(w.uNode)

	if evs := w.h.settle(w.uNode, r2); len(mailOf(evs)) != 0 {
		t.Fatalf("an old node was sent %v", kinds(evs))
	}

	for _, id := range mailIDs[:2] {
		if got := w.h.data().Mail[id]; got.Bounced {
			t.Fatalf("message %s bounced before the node's complete report: %+v", id, got)
		}
	}

	w.h.report(w.uNode, wire.MailReport{Complete: true, Stored: []string{mailIDs[0]}})

	if evs := w.h.settle(w.uNode, r2); len(mailOf(evs)) != 0 {
		t.Errorf("after its report an old node was sent %v", kinds(evs))
	}

	d := w.h.data()

	if got := d.Mail[mailIDs[0]]; got.Bounced || got.Stage != wire.StageStored {
		t.Errorf("the message the node reported stored = %+v, want stored and never bounced", got)
	}

	if got := d.Mail[mailIDs[1]]; !got.Bounced || got.BounceReason != wire.ReasonNodeTooOld {
		t.Errorf("the message the node never stored = %+v, want bounced because the node is too old", got)
	}
}

// TestCrossUserMailIsHeldUntilTheConnectionsCompleteReport proves the broker sends no mail from another
// user on a stream until the node's complete report for that connection arrived, and sends it then. It
// catches a stream that re-checks, and may bounce, a message before it knows what the node stored.
func TestCrossUserMailIsHeldUntilTheConnectionsCompleteReport(t *testing.T) {
	w := newCrossWorld(t)
	w.firstMessage(t, mailIDs[0])

	r, _, _ := w.h.openStream(w.uNode)

	if evs := w.h.settle(w.uNode, r); len(mailOf(evs)) != 0 {
		t.Fatalf("the stream sent %v before the complete report", kinds(evs))
	}

	w.h.report(w.uNode, wire.MailReport{Complete: true})

	if got := mailEvent(t, nextKind(t, r, wire.EventMail)); got.ID != mailIDs[0] {
		t.Errorf("after the report the stream sent %+v", got)
	}
}

// TestAMessageTheNodeReportsStoredOnReconnectIsNeverBounced proves a complete report vouches only for
// its own connection, and only for mail sent before it. A message the node stored, whose ack was lost, is
// not bounced after the grant is revoked, and a message the node never stored is.
//
// The node's first stream stays open on the broker throughout, as a connection that dropped without the
// broker noticing does. It catches a re-check that bounces mail sent after the report the stream relies
// on, and a stream left behind by an earlier registration that re-checks on its successor's report.
func TestAMessageTheNodeReportsStoredOnReconnectIsNeverBounced(t *testing.T) {
	w := newCrossWorld(t)
	w.firstMessage(t, mailIDs[0])

	if resp := w.h.send(w.vNode, mailIDs[1], w.uAddr, "never stored", senderSession); resp.StatusCode != http.StatusOK {
		t.Fatalf("second message: status %d", resp.StatusCode)
	}

	r, _, _ := w.h.openStream(w.uNode)
	w.h.report(w.uNode, wire.MailReport{Complete: true})
	nextKind(t, r, wire.EventMail)
	nextKind(t, r, wire.EventMail)

	// The node stored the first message, and its connection dropped before the ack. The grant goes.
	w.revokeMail(t)

	if evs := w.h.settle(w.uNode, r); len(mailOf(evs)) != 0 {
		t.Fatalf("after the revoke the old stream sent %v", kinds(evs))
	}

	for _, id := range mailIDs[:2] {
		if got := w.h.data().Mail[id]; got.Bounced {
			t.Fatalf("the old stream bounced mail sent after its report: %+v", got)
		}
	}

	// The node reconnects. Its stream opens only after the report, so the old one is still the broker's.
	w.h.registerMail(w.uNode)

	if evs := w.h.settle(w.uNode, r); len(mailOf(evs)) != 0 {
		t.Fatalf("before the report the old stream sent %v", kinds(evs))
	}

	if got := w.h.data().Mail[mailIDs[1]]; got.Bounced {
		t.Fatalf("the re-check ran before the report: %+v", got)
	}

	w.h.report(w.uNode, wire.MailReport{Complete: true, Stored: []string{mailIDs[0]}, Read: []string{mailIDs[0]}})

	if evs := w.h.settle(w.uNode, r); len(mailOf(evs)) != 0 {
		t.Fatalf("the old stream sent %v on its successor's report", kinds(evs))
	}

	if got := w.h.data().Mail[mailIDs[1]]; got.Bounced {
		t.Fatalf("the old stream re-checked on its successor's report: %+v", got)
	}

	r2, _, _ := w.h.openStream(w.uNode)

	if evs := w.h.settle(w.uNode, r2); len(mailOf(evs)) != 0 {
		t.Errorf("after the report the stream sent %v, want nothing", kinds(evs))
	}

	d := w.h.data()

	if got := d.Mail[mailIDs[0]]; got.Bounced || got.Stage != wire.StageRead {
		t.Errorf("the message the node reported read = %+v, want read and never bounced", got)
	}

	if got := d.Mail[mailIDs[1]]; !got.Bounced || got.BounceReason != wire.ReasonNotAllowed {
		t.Errorf("the message the node never stored = %+v, want bounced", got)
	}
}

// TestRevokingTheGrantBouncesLaterMessagesOfItsConversationsRepliesIncluded proves the broker re-checks
// the grant before delivery. Once U revokes V's grant, a queued first message to U and a queued reply to
// V both bounce with "sender no longer allowed". It catches a re-check that spares replies, or that
// checks only at send.
func TestRevokingTheGrantBouncesLaterMessagesOfItsConversationsRepliesIncluded(t *testing.T) {
	w := newCrossWorld(t)
	w.firstMessage(t, mailIDs[0])
	w.readByU(mailIDs[0])

	if resp := w.reply(mailIDs[1], mailIDs[0]); resp.StatusCode != http.StatusOK {
		t.Fatalf("reply: status %d, %s", resp.StatusCode, errMessage(t, resp))
	}

	if resp := w.h.send(w.vNode, mailIDs[2], w.uAddr, "a new question", senderSession); resp.StatusCode != http.StatusOK {
		t.Fatalf("second first message: status %d", resp.StatusCode)
	}

	w.revokeMail(t)

	w.openReported(w.vNode)
	w.openReported(w.uNode)

	d := w.h.data()

	for _, id := range []string{mailIDs[1], mailIDs[2]} {
		if got := d.Mail[id]; !got.Bounced || got.BounceReason != wire.ReasonNotAllowed {
			t.Errorf("message %s = %+v, want bounced as no longer allowed", id, got)
		}
	}
}

// TestRevokingTheGrantRefusesAReplyAtSend proves a reply rides the first message's grant and never one
// of its own. It catches a reply rule that waives the grant, so a revoked coworker keeps talking.
func TestRevokingTheGrantRefusesAReplyAtSend(t *testing.T) {
	w := newCrossWorld(t)
	w.firstMessage(t, mailIDs[0])
	w.readByU(mailIDs[0])
	w.revokeMail(t)

	resp := w.reply(mailIDs[1], mailIDs[0])
	if msg := errMessage(t, resp); resp.StatusCode != http.StatusForbidden || !strings.Contains(msg, wire.ReasonNotAllowed) {
		t.Errorf("a reply after the revoke: status %d, %s", resp.StatusCode, msg)
	}
}

// TestAFirstMessageReusingAHeldConversationsIDIsRefused proves a first message whose ID names a
// conversation the broker still holds is refused, though the message that opened it was already swept.
// U read V's first message, U replied, and V's answer to the reply is queued. U then revokes V, and V
// sends a new first message under the swept ID to W, who grants V. The send is refused, the conversation
// keeps its users, and V's queued answer bounces. A repeat of the first message while it is held still
// gets its stored answer. It catches a send that rebinds the conversation to W's grant, which lets the
// queued answer through U's revoke.
func TestAFirstMessageReusingAHeldConversationsIDIsRefused(t *testing.T) {
	w := newCrossWorld(t)
	w.firstMessage(t, mailIDs[0])

	if resp := w.h.send(w.vNode, mailIDs[0], w.uAddr, "a question", senderSession); resp.StatusCode != http.StatusOK {
		t.Fatalf("a repeat of the first message while it is held: status %d, want its stored answer", resp.StatusCode)
	}

	w.readByU(mailIDs[0])

	w.h.wall.Set(wallStart.Add(6 * 24 * time.Hour))

	if resp := w.reply(mailIDs[1], mailIDs[0]); resp.StatusCode != http.StatusOK {
		t.Fatalf("reply: status %d, %s", resp.StatusCode, errMessage(t, resp))
	}

	w.h.report(w.vNode, wire.MailReport{Stored: []string{mailIDs[1]}, Read: []string{mailIDs[1]}})

	resp := w.h.do(http.MethodPost, wire.PathMailSend, w.vNode, wire.MailSend{
		Version: wire.Current, ID: mailIDs[2], To: w.uAddr, Body: "the follow-up", FromAgent: "claude", FromSession: senderSession,
		ReplyTo: mailIDs[1],
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("V's answer to the reply: status %d, %s", resp.StatusCode, errMessage(t, resp))
	}

	w.h.wall.Set(wallStart.Add(wire.MailRetention + time.Hour))
	w.h.b.Sweep()

	if _, held := w.h.data().Mail[mailIDs[0]]; held {
		t.Fatal("the first message outlived its retention")
	}

	before, ok := w.h.data().Conversations[mailIDs[0]]
	if !ok {
		t.Fatal("the conversation was dropped while its messages are held")
	}

	w.revokeMail(t)

	other, err := w.h.b.CreateUser("w@example.com", wallStart)
	if err != nil {
		t.Fatal(err)
	}

	otherNode := nodeID{Machine: "machine-w", UID: 501, Key: "key-w"}
	otherLabel := w.bindMail(t, otherNode, other.ID, "sess-w")

	w.h.signIn(t, other)

	if _, _, err := w.h.b.GrantMail(testDelegationIssuer, other.ID, w.v.UserLabel, wallStart); err != nil {
		t.Fatal(err)
	}

	w.sharedLease(t, otherNode, "L-w", wire.ReachShared, "sess-w")

	resp = w.h.send(w.vNode, mailIDs[0], w.h.mailName(otherLabel, "sess-w"), "under a reused ID", senderSession)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("a first message reusing the conversation's ID: status %d, want refused", resp.StatusCode)
	}

	after := w.h.data().Conversations[mailIDs[0]]
	if after.SenderUser != before.SenderUser || after.RecipientUser != before.RecipientUser || !after.CreatedAt.Equal(before.CreatedAt) {
		t.Errorf("conversation = %+v, want it unchanged from %+v", after, before)
	}

	for _, ev := range mailOf(w.openReported(w.uNode)) {
		if got := mailEvent(t, ev); got.ID == mailIDs[2] {
			t.Errorf("U's node received V's answer after the revoke: %+v", got)
		}
	}

	if got := w.h.data().Mail[mailIDs[2]]; !got.Bounced || got.BounceReason != wire.ReasonNotAllowed {
		t.Errorf("V's queued answer = %+v, want bounced as no longer allowed", got)
	}
}

// TestANewFirstMessageBouncesWhenTheRecipientsSharedLeaseEnded proves the reach rule is re-checked before
// delivery. A first message queued while U's session held a `shared` lease bounces with "recipient holds
// no `shared` lease" once the lease ended. It catches a broker that checks reach only at send.
func TestANewFirstMessageBouncesWhenTheRecipientsSharedLeaseEnded(t *testing.T) {
	w := newCrossWorld(t)
	w.firstMessage(t, mailIDs[0])
	w.release(t, w.uNode, "L-u", recipientSession)

	if evs := w.openReported(w.uNode); len(mailOf(evs)) != 0 {
		t.Errorf("the recipient was sent %v", kinds(evs))
	}

	if got := w.h.data().Mail[mailIDs[0]]; !got.Bounced || got.BounceReason != wire.ReasonNoSharedLease {
		t.Errorf("message = %+v, want bounced for the lapsed reach", got)
	}

	evs := w.openReported(w.vNode)
	if b := mailOf(evs); len(b) != 1 || b[0].Event != wire.EventBounce {
		t.Fatalf("the sender's stream sent %v, want the bounce", kinds(evs))
	}

	if got := mailEvent(t, mailOf(evs)[0]); got.Reason != wire.ReasonNoSharedLease || got.Orig != mailIDs[0] {
		t.Errorf("bounce = %+v", got)
	}
}

// TestAReplyReachesASenderHoldingNoLeaseWithinSevenDaysOfTheRead proves a reply from the address that
// received a cross-user message reaches its sender on day 7 after the read, though neither side holds a
// `shared` lease by then. The reply carries U's user label. It catches a reply rule that asks for reach.
func TestAReplyReachesASenderHoldingNoLeaseWithinSevenDaysOfTheRead(t *testing.T) {
	w := newCrossWorld(t)
	w.firstMessage(t, mailIDs[0])
	w.readByU(mailIDs[0])
	w.release(t, w.uNode, "L-u", recipientSession)

	w.h.wall.Set(wallStart.Add(wire.MailRetention - time.Hour))

	if resp := w.reply(mailIDs[1], mailIDs[0]); resp.StatusCode != http.StatusOK {
		t.Fatalf("reply on day 7: status %d, %s", resp.StatusCode, errMessage(t, resp))
	}

	all := w.openReported(w.vNode)

	evs := mailOf(all)
	if len(evs) != 1 || evs[0].Event != wire.EventMail {
		t.Fatalf("the sender's stream sent %v, want the reply", kinds(evs))
	}

	got := mailEvent(t, evs[0])
	if got.ID != mailIDs[1] || got.SenderUser != w.u.UserLabel || got.SenderVerified || got.Sender != w.uAddr {
		t.Errorf("reply event = %+v", got)
	}
}

// TestAReplyToAMessageReadEightDaysEarlierIsRefused proves the reply window closes 7 days after the read
// of the message the reply answers. It catches a window counted from the send, or never closed.
func TestAReplyToAMessageReadEightDaysEarlierIsRefused(t *testing.T) {
	w := newCrossWorld(t)
	w.firstMessage(t, mailIDs[0])
	w.readByU(mailIDs[0])

	w.h.wall.Set(wallStart.Add(8 * 24 * time.Hour))

	resp := w.reply(mailIDs[1], mailIDs[0])
	if msg := errMessage(t, resp); resp.StatusCode != http.StatusForbidden || !strings.Contains(msg, "7 days") {
		t.Errorf("a reply 8 days after the read: status %d, %s", resp.StatusCode, msg)
	}
}

type replyRuleTest struct {
	Name        string
	FromSession string
	To          func(w *crossWorld) string
	Read        bool
}

var replyRuleTests = []replyRuleTest{
	{Name: "another session on the recipient's machine", FromSession: "sess-other", To: func(w *crossWorld) string { return w.vAddr }, Read: true},
	{Name: "an address other than the sender", FromSession: recipientSession, To: func(w *crossWorld) string {
		return w.h.mailName(w.vLabel, "sess-v-other")
	}, Read: true},
	{Name: "a message not yet read", FromSession: recipientSession, To: func(w *crossWorld) string { return w.vAddr }},
}

// TestAReplyComesOnlyFromTheReceivingAddressToTheSenderOfAReadMessage proves a reply is mail from the
// address that received a cross-user message, to that message's sender, after the read. It catches a
// reply rule that lets any session of U's, or any address of V's, ride the conversation.
func TestAReplyComesOnlyFromTheReceivingAddressToTheSenderOfAReadMessage(t *testing.T) {
	for _, test := range replyRuleTests {
		t.Run(test.Name, func(t *testing.T) {
			w := newCrossWorld(t)
			w.h.reportSessions(w.vNode, senderSession, "sess-v-other")
			w.h.reportSessions(w.uNode, recipientSession, "sess-other")
			w.firstMessage(t, mailIDs[0])

			if test.Read {
				w.readByU(mailIDs[0])
			}

			resp := w.h.do(http.MethodPost, wire.PathMailSend, w.uNode, wire.MailSend{
				Version: wire.Current, ID: mailIDs[1], To: test.To(w), Body: "x", FromAgent: "claude", FromSession: test.FromSession,
				ReplyTo: mailIDs[0],
			})
			if resp.StatusCode != http.StatusForbidden || errCode(t, resp) != wire.CodeNotGranted {
				t.Errorf("status = %d, want refused", resp.StatusCode)
			}
		})
	}
}

// TestAConversationIsKeptUntilSevenDaysAfterItsLastRead proves the broker stores each cross-user
// conversation, notes its last read, and drops it only once 7 days passed since that read and none of its
// messages is held. It catches a record dropped while a reply could still need it, or kept forever.
func TestAConversationIsKeptUntilSevenDaysAfterItsLastRead(t *testing.T) {
	w := newCrossWorld(t)
	w.firstMessage(t, mailIDs[0])

	c, ok := w.h.data().Conversations[mailIDs[0]]
	if !ok || c.SenderUser != w.v.ID || c.RecipientUser != w.u.ID || !c.LastMessageRead.IsZero() {
		t.Fatalf("conversation = %+v, %v", c, ok)
	}

	readAt := wallStart.Add(time.Hour)
	w.h.wall.Set(readAt)
	w.readByU(mailIDs[0])

	if got := w.h.data().Conversations[mailIDs[0]]; !got.LastMessageRead.Equal(readAt) {
		t.Fatalf("last read = %v, want %v", got.LastMessageRead, readAt)
	}

	w.h.wall.Set(readAt.Add(wire.MailRetention - time.Minute))
	w.h.b.Sweep()

	if _, ok := w.h.data().Conversations[mailIDs[0]]; !ok {
		t.Fatal("the conversation was dropped within 7 days of its last read")
	}

	w.h.wall.Set(readAt.Add(wire.MailRetention + time.Minute))
	w.h.b.Sweep()

	if _, ok := w.h.data().Mail[mailIDs[0]]; ok {
		t.Fatal("the read message outlived its retention")
	}

	w.h.b.Sweep()

	if _, ok := w.h.data().Conversations[mailIDs[0]]; ok {
		t.Error("the conversation outlived 7 days after its last read")
	}
}

// TestRegisteredSaysTheBrokerReadsReplies proves the registration answer carries the flag a node needs
// before it forwards a reply. It catches a broker that drops the flag, which would leave every new node
// answering replies "broker too old".
func TestRegisteredSaysTheBrokerReadsReplies(t *testing.T) {
	h := newHarness(t, nil)

	if reg := h.register(alice); !reg.MailReplies {
		t.Errorf("registered = %+v, want mail_replies", reg)
	}
}

// startMailAction signs U in on the page for a mail-grant action, and returns the form's token.
func startMailAction(t *testing.T, h *harness, si *signInIssuer, action string) string {
	t.Helper()

	authURL, err := h.b.signInMgr.StartAction(action)
	if err != nil {
		t.Fatal(err)
	}

	u, _ := url.Parse(authURL)
	si.expectedNonce = u.Query().Get("nonce")

	token, _, _, err := h.b.signInMgr.HandleCallback(context.Background(), u.Query().Get("state"), "test-code")
	if err != nil {
		t.Fatal(err)
	}

	return token
}

func submitMailAction(h *harness, path, token, userLabel string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, nil)
	req.PostForm = url.Values{"token": {token}, "user_label": {userLabel}}

	w := httptest.NewRecorder()

	switch path {
	case "/mail-grant-submit":
		h.b.handleMailGrantSubmit(w, req)
	default:
		h.b.handleMailRevokeSubmit(w, req)
	}

	return w
}

// TestAMailGrantMadeOnThePageTakesEffectAndItsRevokeWithdrawsIt proves U grants V by typing V's user label
// on the form the page renders after U's sign-in, and revokes it the same way. It catches a page action
// that writes the grant the wrong way round, or a revoke that leaves it.
func TestAMailGrantMadeOnThePageTakesEffectAndItsRevokeWithdrawsIt(t *testing.T) {
	h, si := newSignInHarness(t)

	u, err := h.b.AdmitUser(si.issuer, si.subject, si.email, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	v, err := h.b.CreateUser("v@example.com", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	rec := submitMailAction(h, "/mail-grant-submit", startMailAction(t, h, si, actionMailGrant), v.UserLabel)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Security-Policy") != "frame-ancestors 'none'" {
		t.Fatalf("grant: %d %s", rec.Code, rec.Body.String())
	}

	if _, ok := h.data().MailGrants[u.ID][v.ID]; !ok || len(h.data().MailGrants[v.ID]) != 0 {
		t.Fatalf("grants after the grant = %+v, want U's grant to V only", h.data().MailGrants)
	}

	rec = submitMailAction(h, "/mail-revoke-submit", startMailAction(t, h, si, actionMailRevoke), v.UserLabel)
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body.String())
	}

	if _, ok := h.data().MailGrants[u.ID][v.ID]; ok {
		t.Errorf("grants after the revoke = %+v", h.data().MailGrants)
	}
}

// TestAMailGrantFormStartsEmptyAndATokenOfAnotherActionChangesNothing proves the form after the callback
// carries no user label sent before the sign-in, and that a delegation's token cannot submit a mail grant.
// It catches a page that prefills a cross-site request's parameters, or that accepts any token.
func TestAMailGrantFormStartsEmptyAndATokenOfAnotherActionChangesNothing(t *testing.T) {
	h, si := newSignInHarness(t)

	u, err := h.b.AdmitUser(si.issuer, si.subject, si.email, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	v, err := h.b.CreateUser("v@example.com", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	authURL, err := h.b.signInMgr.StartAction(actionMailGrant)
	if err != nil {
		t.Fatal(err)
	}

	pu, _ := url.Parse(authURL)
	si.expectedNonce = pu.Query().Get("nonce")

	req := httptest.NewRequest(http.MethodGet, "/callback?code=c&state="+pu.Query().Get("state")+"&user_label="+v.UserLabel, nil)
	rec := httptest.NewRecorder()
	h.b.handleCallback(rec, req)

	if body := rec.Body.String(); strings.Contains(body, v.UserLabel) || !strings.Contains(body, `name="user_label"`) {
		t.Errorf("the form after the callback = %s", body)
	}

	other := submitMailAction(h, "/mail-grant-submit", startMailAction(t, h, si, "grant"), v.UserLabel)
	if other.Code == http.StatusOK {
		t.Error("a delegation's token submitted a mail grant")
	}

	if len(h.data().MailGrants[u.ID]) != 0 {
		t.Errorf("grants = %+v, want none", h.data().MailGrants)
	}
}

// TestAnUnadmittedSignerCannotGrantMail proves the page checks the signer's admission before a mail
// grant. It catches a grant made by anyone the identity provider signs in.
func TestAnUnadmittedSignerCannotGrantMail(t *testing.T) {
	h, si := newSignInHarness(t)

	v, err := h.b.CreateUser("v@example.com", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	rec := submitMailAction(h, "/mail-grant-submit", startMailAction(t, h, si, actionMailGrant), v.UserLabel)
	if rec.Code != http.StatusForbidden || len(h.data().MailGrants) != 0 {
		t.Errorf("an unadmitted grant: %d, grants %+v", rec.Code, h.data().MailGrants)
	}
}
