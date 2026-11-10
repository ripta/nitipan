package broker

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/wire"
)

const (
	senderSession    = "sess-sender-1"
	recipientSession = "sess-recipient-1"
)

// registerMail registers a node that speaks mail over HTTPS.
func (h *harness) registerMail(n nodeID) string {
	h.t.Helper()

	resp := h.do(http.MethodPost, wire.PathRegister, n, wire.Register{
		Version: wire.Current, MachineID: n.Machine, UID: n.UID, User: "u", Host: "host-" + n.Machine, NodeVersion: "test",
		HTTPS: true, MailCapability: wire.MailCapability,
	})
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("register %+v: status %d", n, resp.StatusCode)
	}

	var out wire.Registered
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		h.t.Fatal(err)
	}

	return out.Label
}

// approve pins a node's key the way the broker host's user does, by the fingerprint the node prints.
func (h *harness) approve(n nodeID, label string) {
	h.t.Helper()

	if _, err := h.b.Approve(label, wire.Fingerprint(wire.KeyHash(n.Key))); err != nil {
		h.t.Fatal(err)
	}
}

// mailNode registers and approves a node and reports one live session for it.
func (h *harness) mailNode(n nodeID, sessions ...string) (label string) {
	h.t.Helper()

	label = h.registerMail(n)
	h.approve(n, label)
	h.reportSessions(n, sessions...)

	return label
}

func (h *harness) reportSessions(n nodeID, sessions ...string) {
	h.t.Helper()

	var ms []wire.MailSession
	for _, s := range sessions {
		ms = append(ms, wire.MailSession{Agent: "claude", SessionID: s, Live: true})
	}

	resp := h.do(http.MethodPost, wire.PathSnapshot, n, wire.Snapshot{Version: wire.Current, Sessions: ms})
	if resp.StatusCode/100 != 2 {
		h.t.Fatalf("snapshot status %d", resp.StatusCode)
	}
}

func (h *harness) mailName(label, session string) string {
	h.t.Helper()

	name, err := DeriveMailName("claude", session, h.data().Labels[label].MachineID)
	if err != nil {
		h.t.Fatal(err)
	}

	return name + "@" + label
}

// mailIDs are valid request IDs, which the broker requires of a message.
var mailIDs = []string{fakeID(1), fakeID(2), fakeID(3), fakeID(4)}

func fakeID(n int) string { return fmt.Sprintf("01JM%022d", n) }

func (h *harness) send(n nodeID, id, to, body, fromSession string) *http.Response {
	h.t.Helper()

	return h.do(http.MethodPost, wire.PathMailSend, n, wire.MailSend{
		Version: wire.Current, ID: id, To: to, Body: body, FromAgent: "claude", FromSession: fromSession,
	})
}

func errCode(t *testing.T, resp *http.Response) string {
	t.Helper()

	var e wire.Error
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		t.Fatalf("status %d with no error body: %v", resp.StatusCode, err)
	}

	return e.Code
}

func mailEvent(t *testing.T, ev wire.SSE) wire.MailEvent {
	t.Helper()

	var m wire.MailEvent
	if err := json.Unmarshal([]byte(ev.Data), &m); err != nil {
		t.Fatal(err)
	}

	return m
}

// nextKind reads events until one has the kind, skipping the others.
func nextKind(t *testing.T, r *bufio.Reader, kind string) wire.SSE {
	t.Helper()

	for range 10 {
		if ev := nextEvent(t, r); ev.Event == kind {
			return ev
		}
	}

	t.Fatalf("no %s event arrived", kind)

	return wire.SSE{}
}

func (h *harness) ackStored(n nodeID, id string) {
	h.t.Helper()

	resp := h.do(http.MethodPost, wire.PathMailAck, n, wire.MailAck{Version: wire.Current, ID: id, Outcome: wire.MailStored})
	if resp.StatusCode != http.StatusNoContent {
		h.t.Fatalf("mail ack status %d", resp.StatusCode)
	}
}

func (h *harness) status(n nodeID, id string) wire.MailStatus {
	h.t.Helper()

	resp := h.do(http.MethodPost, wire.PathMailStatus, n, wire.MailStatusRequest{Version: wire.Current, ID: id})
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("mail status: %d", resp.StatusCode)
	}

	var st wire.MailStatus
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		h.t.Fatal(err)
	}

	return st
}

func TestMailReachesTheRecipientsStreamWithTheSenderStampedByTheBroker(t *testing.T) {
	h := newHarness(t, nil)

	fromLabel := h.mailNode(alice, senderSession)
	toLabel := h.mailNode(bob, recipientSession)
	to := h.mailName(toLabel, recipientSession)

	r, _, _ := h.openStream(bob)

	// The body also sets sender fields. The broker ignores every one of them.
	raw := `{"version":"1.0","id":"` + mailIDs[0] + `","to":"` + to + `","body":"hello","from_agent":"claude","from_session":"` + senderSession +
		`","sender":"forged@elsewhere","sender_verified":false,"from_label":"forged"}`

	req, err := http.NewRequest(http.MethodPost, h.srv.URL+wire.PathMailSend, strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}

	req.Header.Set("Authorization", "Bearer "+alice.Key)
	req.Header.Set(wire.HeaderMachine, alice.Machine)
	req.Header.Set(wire.HeaderUID, strconv.Itoa(alice.UID))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("send status %d", resp.StatusCode)
	}

	ev := nextKind(t, r, wire.EventMail)
	got := mailEvent(t, ev)

	wantSender := h.mailName(fromLabel, senderSession)
	if got.Sender != wantSender || !got.SenderVerified || got.Body != "hello" || got.ToSession != recipientSession || got.ID != mailIDs[0] {
		t.Errorf("event = %+v, want sender %s verified", got, wantSender)
	}

	if st := h.status(alice, mailIDs[0]); st.Stage != wire.StageQueued || !st.SentUnacked {
		t.Errorf("status after send = %+v, want queued and sent without an ack", st)
	}
}

func TestQueuedMailSurvivesABrokerRestartAndIsStoredThenAcked(t *testing.T) {
	h := newHarness(t, nil)

	h.mailNode(alice, senderSession)
	toLabel := h.mailNode(bob, recipientSession)

	if resp := h.send(alice, mailIDs[0], h.mailName(toLabel, recipientSession), "while you slept", senderSession); resp.StatusCode != http.StatusOK {
		t.Fatalf("send status %d", resp.StatusCode)
	}

	h.restart(nil)

	if got := h.data().Mail[mailIDs[0]]; got.Body != "while you slept" || got.Stage != wire.StageQueued {
		t.Fatalf("mail after restart = %+v", got)
	}

	// The node must reconnect and register again after a restart, and the pin stays.
	h.registerMail(bob)

	r, _, _ := h.openStream(bob)
	if got := mailEvent(t, nextKind(t, r, wire.EventMail)); got.Body != "while you slept" {
		t.Errorf("event after restart = %+v", got)
	}

	h.ackStored(bob, mailIDs[0])

	if got := h.data().Mail[mailIDs[0]]; got.Stage != wire.StageStored || got.Body != "" {
		t.Errorf("after the ack the broker holds %+v, want stored with the body dropped", got)
	}

	// A resend after a lost ack never reaches a node that stored the message.
	h.registerMail(alice)
	ra, _, _ := h.openStream(alice)

	if ev := nextKind(t, ra, wire.EventStage); !strings.Contains(ev.Data, wire.StageStored) {
		t.Errorf("stage event = %s", ev.Data)
	}
}

func TestABodyOverTheCapIsRefusedAtSend(t *testing.T) {
	h := newHarness(t, nil)

	h.mailNode(alice, senderSession)
	toLabel := h.mailNode(bob, recipientSession)
	to := h.mailName(toLabel, recipientSession)

	resp := h.send(alice, mailIDs[0], to, strings.Repeat("x", wire.MaxMailBody+1), senderSession)
	if resp.StatusCode != http.StatusRequestEntityTooLarge || errCode(t, resp) != wire.CodeTooLarge {
		t.Errorf("an oversize body got status %d", resp.StatusCode)
	}

	if resp := h.send(alice, mailIDs[1], to, strings.Repeat("x", wire.MaxMailBody), senderSession); resp.StatusCode != http.StatusOK {
		t.Errorf("a body at the cap got status %d", resp.StatusCode)
	}
}

func TestANewLabelCanNeitherSendNorReceiveMailUntilApproved(t *testing.T) {
	h := newHarness(t, nil)

	// alice is approved. bob registers but is not.
	h.mailNode(alice, senderSession)
	bobLabel := h.mailNode(bob, recipientSession)

	// Forgetting the label makes it pending again, and its reported addresses stay valid.
	if err := h.b.Forget(bobLabel); err != nil {
		t.Fatal(err)
	}

	h.registerMail(bob)

	to := h.mailName(bobLabel, recipientSession)

	if resp := h.send(bob, mailIDs[0], h.mailName(h.data().Nodes[alice.key().String()].Label, senderSession), "x", recipientSession); resp.StatusCode != http.StatusForbidden || errCode(t, resp) != wire.CodeNotApproved {
		t.Errorf("an unapproved label sent mail: status %d", resp.StatusCode)
	}

	if resp := h.send(alice, mailIDs[1], to, "to the unapproved", senderSession); resp.StatusCode != http.StatusOK {
		t.Fatalf("mail to a pending label waits on the broker: status %d", resp.StatusCode)
	}

	r, _, _ := h.openStream(bob)

	// Leases follow NIT-004 for a pending label. Mail does not move.
	h.approve(bob, bobLabel)

	if got := mailEvent(t, nextKind(t, r, wire.EventMail)); got.Body != "to the unapproved" {
		t.Errorf("after approval the event = %+v", got)
	}
}

func TestAPendingLabelStillGetsLeaseServiceButNoMailEvent(t *testing.T) {
	h := newHarness(t, nil)

	h.mailNode(alice, senderSession)
	label := h.mailNode(bob, recipientSession)

	if err := h.b.Forget(label); err != nil {
		t.Fatal(err)
	}

	h.registerMail(bob)

	if resp := h.send(alice, mailIDs[0], h.mailName(label, recipientSession), "wait", senderSession); resp.StatusCode != http.StatusOK {
		t.Fatalf("send status %d", resp.StatusCode)
	}

	r, _, _ := h.openStream(bob)

	if _, err := h.b.Enqueue(bob.key(), "note", map[string]string{"n": "1"}); err != nil {
		t.Fatal(err)
	}

	if ev := nextEvent(t, r); ev.Event != "note" {
		t.Errorf("the first event on a pending node's stream was %s, want the lease-path note", ev.Event)
	}

	if got := h.data().Mail[mailIDs[0]]; got.Sent {
		t.Error("the broker sent mail to an unapproved key")
	}
}

func TestASecondKeyForAnApprovedLabelIsRefusedWhileItsMachineSleeps(t *testing.T) {
	h := newHarness(t, nil)

	label := h.mailNode(alice, senderSession)

	// No stream is open, so NIT-004's live-stream rule would accept the new key.
	other := nodeID{Machine: alice.Machine, UID: alice.UID, Key: "an-impostor"}

	_, e := h.tryRegister(other)
	if e == nil || e.Code != wire.CodeKeyConflict {
		t.Fatalf("register with a second key = %+v, want a key conflict", e)
	}

	if h.data().Pins[label].PinnedHash != wire.KeyHash(alice.Key) || h.data().Nodes[alice.key().String()].KeyHash != wire.KeyHash(alice.Key) {
		t.Error("the refused key replaced the pinned one")
	}

	// A pinned label also refuses the key's lease calls.
	if code := h.snapshot(other); code != http.StatusUnauthorized {
		t.Errorf("snapshot with the second key = %d, want 401", code)
	}
}

func TestAnUnpinnedLabelKeepsTheLiveStreamKeyRule(t *testing.T) {
	h := newHarness(t, nil)

	h.register(alice)
	h.openStream(alice)

	other := nodeID{Machine: alice.Machine, UID: alice.UID, Key: "later-key"}
	if _, e := h.tryRegister(other); e == nil {
		t.Error("a second key was accepted while the old stream was live")
	}

	h.clock.Advance(testTerm + time.Second)

	if _, e := h.tryRegister(other); e != nil {
		t.Errorf("a second key was refused after a term with no heartbeat: %v", e)
	}
}

func TestApproveByFingerprintPinsThatKeyAndDiscardsTheOthers(t *testing.T) {
	h := newHarness(t, nil)

	label := h.registerMail(alice)

	// A host on the LAN registers under the same machine and uid with a key of its own.
	h.clock.Advance(testTerm + time.Second)

	impostor := nodeID{Machine: alice.Machine, UID: alice.UID, Key: "lan-host-key"}
	if got := h.registerMail(impostor); got != label {
		t.Fatalf("label = %s", got)
	}

	if n := len(h.data().Pins[label].Pending); n != 2 {
		t.Fatalf("pending keys = %d, want both", n)
	}

	want := wire.Fingerprint(wire.KeyHash(alice.Key))

	if pinned, err := h.b.Approve(label, strings.ToUpper(want)); err != nil || !pinned {
		t.Fatalf("approve by fingerprint: %v", err)
	}

	pin := h.data().Pins[label]
	if pin.PinnedHash != wire.KeyHash(alice.Key) || len(pin.Pending) != 0 {
		t.Errorf("pin = %+v, want only the approved key", pin)
	}

	if h.data().Nodes[alice.key().String()].KeyHash != wire.KeyHash(alice.Key) {
		t.Error("the node record still holds the impostor's key")
	}

	for _, bad := range []string{"", "zzzz", "0000-0000-0000", "gggg-0000-0000-0000"} {
		if _, err := h.b.Approve(label, bad); !errors.Is(err, ErrBadFingerprint) {
			t.Errorf("approve %q = %v, want a malformed fingerprint error", bad, err)
		}
	}
}

func TestAFloodOfFreshKeysCannotEvictTheRealKeyFromAnApprovalItNeverGets(t *testing.T) {
	h := newHarness(t, nil)

	real := alice
	label := ""

	// A host on the LAN registers many fresh keys under the sleeping machine's label, each accepted
	// because the last one's stream went quiet. The pending list holds only the newest few.
	for i := range 3 * maxPendingKeys {
		hostile := nodeID{Machine: real.Machine, UID: real.UID, Key: fmt.Sprintf("hostile-%d", i)}
		label = h.registerMail(hostile)

		h.clock.Advance(testTerm + time.Second)
	}

	hostile := nodeID{Machine: real.Machine, UID: real.UID, Key: "hostile-streaming"}
	h.registerMail(hostile)

	r, _, _ := h.openStream(hostile)

	// The real machine wakes. Its key is refused while the hostile stream is live, so it is not pending.
	if _, e := h.tryRegister(real); e == nil {
		t.Fatal("the real key registered while the hostile stream was live")
	}

	pinned, err := h.b.Approve(label, wire.Fingerprint(wire.KeyHash(real.Key)))
	if err != nil || pinned {
		t.Fatalf("approve of a key that never became pending = %v, %v, want it recorded as awaited", pinned, err)
	}

	if got := h.data().Pins[label]; got.PinnedHash != "" || got.Awaiting == "" {
		t.Fatalf("pin = %+v", got)
	}

	// The real key arrives, takes the label, and the hostile stream closes.
	h.registerMail(real)

	if got := h.data().Pins[label]; got.PinnedHash != wire.KeyHash(real.Key) || got.Awaiting != "" {
		t.Errorf("pin after the real key arrived = %+v", got)
	}

	if _, e := h.tryRegister(hostile); e == nil {
		t.Error("the hostile key registered after approval")
	}

	done := make(chan error, 1)

	go func() {
		_, err := r.ReadString('\x00')
		done <- err
	}()

	select {
	case err := <-done:
		t.Log("the hostile stream ended:", err)
	case <-time.After(5 * time.Second):
		t.Error("the hostile stream stayed open")
	}
}

func TestASendOrAckWithoutTheLabelsKeyIsRefused(t *testing.T) {
	h := newHarness(t, nil)

	h.mailNode(alice, senderSession)
	toLabel := h.mailNode(bob, recipientSession)
	to := h.mailName(toLabel, recipientSession)

	wrong := nodeID{Machine: alice.Machine, UID: alice.UID, Key: "wrong"}

	if resp := h.send(wrong, mailIDs[0], to, "x", senderSession); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("send with the wrong key = %d", resp.StatusCode)
	}

	if resp := h.do(http.MethodPost, wire.PathMailAck, wrong, wire.MailAck{Version: wire.Current, ID: mailIDs[0], Outcome: wire.MailStored}); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("ack with the wrong key = %d", resp.StatusCode)
	}

	if resp := h.do(http.MethodPost, wire.PathMailSend, nodeID{Machine: alice.Machine, UID: alice.UID}, wire.MailSend{Version: wire.Current}); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("send with no key = %d", resp.StatusCode)
	}
}

func TestANodeOnPlainHTTPGetsNoMailAndTheErrorSaysWhy(t *testing.T) {
	h := newHarness(t, nil)

	// alice is approved but reached the broker over plain HTTP.
	resp := h.do(http.MethodPost, wire.PathRegister, alice, wire.Register{
		Version: wire.Current, MachineID: alice.Machine, UID: alice.UID, HTTPS: false, MailCapability: wire.MailCapability,
	})

	var reg wire.Registered
	if err := json.NewDecoder(resp.Body).Decode(&reg); err != nil {
		t.Fatal(err)
	}

	h.approve(alice, reg.Label)
	h.reportSessions(alice, senderSession)

	toLabel := h.mailNode(bob, recipientSession)

	resp = h.send(alice, mailIDs[0], h.mailName(toLabel, recipientSession), "x", senderSession)

	var e wire.Error
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		t.Fatal(err)
	}

	if resp.StatusCode != http.StatusForbidden || e.Code != wire.CodePlainHTTP || !strings.Contains(e.Message, "HTTPS") {
		t.Errorf("plain HTTP send = %d %+v, want a refusal that names HTTPS", resp.StatusCode, e)
	}
}

func TestSendRefusesAnUnknownLabelAnUnreportedNameAndAnUnreportedSender(t *testing.T) {
	h := newHarness(t, nil)

	h.mailNode(alice, senderSession)
	toLabel := h.mailNode(bob, recipientSession)

	cases := []struct {
		Name     string
		To       string
		From     string
		WantCode string
	}{
		{"label never seen", "alpha-beta-gamma-delta@no-such-label-ever", senderSession, wire.CodeNoSuchSession},
		{"name no node reported", "never-reported-name-here@" + toLabel, senderSession, wire.CodeNoSuchSession},
		{"sender not a live registration", h.mailName(toLabel, recipientSession), "someone-else", wire.CodeNotMailable},
		{"not an address", "nonsense", senderSession, wire.CodeBadRequest},
	}

	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			resp := h.send(alice, mailIDs[0], tc.To, "x", tc.From)
			if got := errCode(t, resp); got != tc.WantCode {
				t.Errorf("code = %s, want %s (status %d)", got, tc.WantCode, resp.StatusCode)
			}
		})
	}
}

func TestAMailNameIsDerivedFromTheSessionIDAndSurvivesAResume(t *testing.T) {
	m := "machine-x"

	a, _ := DeriveMailName("claude", "sess-1", m)
	b, _ := DeriveMailName("claude", "sess-1", m)
	c, _ := DeriveMailName("claude", "sess-2", m)

	if a != b || a == c || len(strings.Split(a, "-")) != 4 {
		t.Errorf("claude names = %s %s %s", a, b, c)
	}

	// Codex and opencode keep the NIT-001 identity string, so the mail name equals their handle.
	for _, agent := range []string{"codex", "opencode"} {
		got, err := DeriveMailName(agent, "sess-1", m)
		if err != nil || got != handleOf(agent+"\x00sess-1", m) {
			t.Errorf("%s mail name = %q, %v, want the handle", agent, got, err)
		}
	}

	if claudeHandle := handleOf("claude\x00"+"sess-1", m); a == claudeHandle {
		t.Error("a claude mail name equals a handle derived from the same inputs")
	}

	if _, err := DeriveMailName("mystery", "s", m); err == nil {
		t.Error("an unknown agent got a mail name")
	}
}

func TestASentMessageNeverBouncesForExpiryAndAReportedStoreStopsTheResend(t *testing.T) {
	h := newHarness(t, nil)

	h.mailNode(alice, senderSession)
	toLabel := h.mailNode(bob, recipientSession)
	to := h.mailName(toLabel, recipientSession)

	if resp := h.send(alice, mailIDs[0], to, "never acked", senderSession); resp.StatusCode != http.StatusOK {
		t.Fatalf("send: %d", resp.StatusCode)
	}

	r, _, cancel := h.openStream(bob)
	nextKind(t, r, wire.EventMail)
	cancel()

	// The broker sent the message and saw no ack. Eight days pass.
	h.wall.Set(wallStart.Add(8 * 24 * time.Hour))

	h.snapshot(alice)

	if got := h.data().Mail[mailIDs[0]]; got.Bounced || got.Stage != wire.StageQueued {
		t.Fatalf("a sent message expired as unsent: %+v", got)
	}

	// The node returns and reports what it stored. Only unstored messages are sent again.
	h.registerMail(bob)

	resp := h.do(http.MethodPost, wire.PathMailReport, bob, wire.MailReport{Version: wire.Current, Stored: []string{mailIDs[0]}})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("report: %d", resp.StatusCode)
	}

	r2, _, _ := h.openStream(bob)

	if _, err := h.b.Enqueue(bob.key(), "probe", map[string]string{}); err != nil {
		t.Fatal(err)
	}

	if ev := nextEvent(t, r2); ev.Event != "probe" {
		t.Errorf("first event after the report was %s, want only the probe", ev.Event)
	}

	if st := h.data().Mail[mailIDs[0]]; st.Stage != wire.StageStored {
		t.Errorf("stage = %s", st.Stage)
	}
}

func TestMailTheBrokerNeverSentBouncesToItsSenderAfterSevenDaysWithItsBody(t *testing.T) {
	h := newHarness(t, nil)

	h.mailNode(alice, senderSession)
	toLabel := h.mailNode(bob, recipientSession)

	if resp := h.send(alice, mailIDs[0], h.mailName(toLabel, recipientSession), "important", senderSession); resp.StatusCode != http.StatusOK {
		t.Fatalf("send: %d", resp.StatusCode)
	}

	h.wall.Set(wallStart.Add(wire.MailRetention + time.Hour))
	h.snapshot(alice)

	orig := h.data().Mail[mailIDs[0]]
	if !orig.Bounced || orig.BounceReason != wire.ReasonUnsent {
		t.Fatalf("original = %+v", orig)
	}

	r, _, _ := h.openStream(alice)
	bounce := mailEvent(t, nextKind(t, r, wire.EventBounce))

	if bounce.Body != "important" || bounce.Reason != wire.ReasonUnsent || bounce.Orig != mailIDs[0] || bounce.ToSession != senderSession {
		t.Errorf("bounce = %+v", bounce)
	}

	// Storing the bounce produces no stage or bounce of its own.
	h.ackStored(alice, bounce.ID)

	for id, m := range h.data().Mail {
		if m.Kind == wire.EventBounce {
			t.Errorf("bounce %s is still held after it was stored", id)
		}
	}

	if got := h.b.Unacked(alice.key()); len(got) != 0 {
		t.Errorf("a bounce produced events: %+v", got)
	}
}

func TestABounceThatCannotReachItsSenderIsDroppedAfterSevenDays(t *testing.T) {
	h := newHarness(t, nil)

	h.mailNode(alice, senderSession)
	toLabel := h.mailNode(bob, recipientSession)

	h.send(alice, mailIDs[0], h.mailName(toLabel, recipientSession), "x", senderSession)

	h.wall.Set(wallStart.Add(wire.MailRetention + time.Hour))
	h.snapshot(alice)

	if countKind(h, wire.EventBounce) != 1 {
		t.Fatalf("bounces = %d, want one", countKind(h, wire.EventBounce))
	}

	h.wall.Set(wallStart.Add(2*wire.MailRetention + 2*time.Hour))
	h.snapshot(alice)

	if countKind(h, wire.EventBounce) != 0 || len(h.data().Mail) != 0 {
		t.Errorf("mail after the bounce expired = %+v", h.data().Mail)
	}
}

func countKind(h *harness, kind string) int {
	n := 0

	for _, m := range h.data().Mail {
		if m.Kind == kind {
			n++
		}
	}

	return n
}

func TestAReadReportAdvancesTheStageAndTellsTheSenderWithoutWakingIt(t *testing.T) {
	h := newHarness(t, nil)

	h.mailNode(alice, senderSession)
	toLabel := h.mailNode(bob, recipientSession)

	h.send(alice, mailIDs[0], h.mailName(toLabel, recipientSession), "x", senderSession)
	h.ackStored(bob, mailIDs[0])

	resp := h.do(http.MethodPost, wire.PathMailReport, bob, wire.MailReport{Version: wire.Current, Read: []string{mailIDs[0]}})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("report: %d", resp.StatusCode)
	}

	st := h.status(alice, mailIDs[0])
	if st.Stage != wire.StageRead || st.SentUnacked {
		t.Errorf("status = %+v", st)
	}

	var kinds []string
	for _, e := range h.b.Unacked(alice.key()) {
		kinds = append(kinds, e.Kind)
	}

	if strings.Join(kinds, ",") != "stage,stage" {
		t.Errorf("the sender's events = %v, want only stage updates, no mail or bounce", kinds)
	}

	// A message nobody reported cannot be probed by another machine.
	if resp := h.do(http.MethodPost, wire.PathMailStatus, bob, wire.MailStatusRequest{Version: wire.Current, ID: mailIDs[0]}); resp.StatusCode != http.StatusNotFound {
		t.Errorf("the recipient read the sender's status: %d", resp.StatusCode)
	}
}

func TestAnOldNodeGetsNoMailAndTheSenderLearnsTheRecipientNodeIsTooOld(t *testing.T) {
	h := newHarness(t, nil)

	h.mailNode(alice, senderSession)

	// bob comes back as an older binary after it reported a session: same key, no capability.
	label := h.mailNode(bob, recipientSession)
	h.register(bob)

	resp := h.send(alice, mailIDs[0], h.mailName(label, recipientSession), "x", senderSession)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("send to an old node = %d", resp.StatusCode)
	}

	var e wire.Error
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil || e.Code != wire.CodeNodeTooOld || !strings.Contains(e.Message, "recipient node too old") {
		t.Errorf("error = %+v, %v", e, err)
	}

	if len(h.data().Mail) != 0 {
		t.Error("the broker kept a message for a node that cannot take it")
	}
}

func TestForgetKeepsQueuedMailForTheNextKeyAndBouncesStoredUnreadMailWithoutItsBody(t *testing.T) {
	h := newHarness(t, nil)

	h.mailNode(alice, senderSession)
	toLabel := h.mailNode(bob, recipientSession)
	to := h.mailName(toLabel, recipientSession)

	h.send(alice, mailIDs[0], to, "stored but never read", senderSession)
	h.ackStored(bob, mailIDs[0])
	h.send(alice, mailIDs[1], to, "still queued", senderSession)

	if err := h.b.Forget(toLabel); err != nil {
		t.Fatal(err)
	}

	if got := h.data().Mail[mailIDs[0]]; !got.Bounced || got.BounceReason != wire.ReasonForgotten {
		t.Errorf("stored unread = %+v, want bounced", got)
	}

	var bounce MailRecord

	for _, m := range h.data().Mail {
		if m.Kind == wire.EventBounce {
			bounce = m
		}
	}

	if bounce.Body != "" || !bounce.BodyDropped || bounce.Orig != mailIDs[0] {
		t.Errorf("bounce = %+v, want one without a body", bounce)
	}

	if got := h.data().Mail[mailIDs[1]]; got.Bounced || got.Stage != wire.StageQueued {
		t.Errorf("queued mail = %+v, want kept", got)
	}

	// The old key is dead. A new key is pending, and mail is never handed to it unapproved.
	if code := h.snapshot(bob); code != http.StatusUnauthorized {
		t.Errorf("the forgotten key still authenticates: %d", code)
	}

	newBob := nodeID{Machine: bob.Machine, UID: bob.UID, Key: "reinstalled"}
	h.registerMail(newBob)

	r, _, _ := h.openStream(newBob)

	if _, err := h.b.Enqueue(newBob.key(), "probe", map[string]string{}); err != nil {
		t.Fatal(err)
	}

	if ev := nextEvent(t, r); ev.Event != "probe" {
		t.Fatalf("an unapproved key was sent %s", ev.Event)
	}

	h.approve(newBob, toLabel)

	h.reportSessions(newBob, recipientSession)

	if got := mailEvent(t, nextKind(t, r, wire.EventMail)); got.Body != "still queued" {
		t.Errorf("the next approved key got %+v", got)
	}
}

func TestEveryStageOfAMessageCarriesAMonotonicRank(t *testing.T) {
	if stageRank(wire.StageQueued) >= stageRank(wire.StageStored) || stageRank(wire.StageStored) >= stageRank(wire.StageRead) {
		t.Error("stage ranks do not rise from queued to read")
	}
}

func TestTheNetworkHandlerServesNoApprovalRoute(t *testing.T) {
	h := newHarness(t, nil)

	for _, p := range []string{AdminPathPending, AdminPathApprove, AdminPathForget, "/v1/approve", "/v1/forget", "/v1/pending"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			resp := h.do(method, p, alice, map[string]string{"label": "x", "fingerprint": "y"})
			if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusMethodNotAllowed {
				t.Errorf("%s %s answered %d on the network handler", method, p, resp.StatusCode)
			}
		}
	}
}

func TestAStore10ReadsUnder20AndAnOlderBrokerRefusesA20Store(t *testing.T) {
	dir, err := os.MkdirTemp("", "nitipan-store")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	path := dir + "/broker.json"

	old, err := OpenStore(path, StoreOptions{Volume: localVolume{}, Current: record.Version{Major: 1, Minor: 0}})
	if err != nil {
		t.Fatal(err)
	}

	_ = old.Close()

	cur, err := OpenStore(path, StoreOptions{
		Volume: localVolume{},
		Migrations: map[int]Migration{
			1: MigrateV1ToV2,
		},
	})
	if err != nil {
		t.Fatalf("a 2.0 broker could not read a 1.0 store: %v", err)
	}

	if cur.Data().Mail == nil || cur.Data().Pins == nil {
		t.Error("the new maps are nil after reading a 1.0 store")
	}

	if err := cur.Persist(cur.Data()); err != nil {
		t.Fatal(err)
	}

	_ = cur.Close()

	if _, err := OpenStore(path, StoreOptions{Volume: localVolume{}, Current: record.Version{Major: 1, Minor: 0}}); err == nil {
		t.Error("a 1.0 broker opened a store that holds mail")
	}
}

func TestAnAddressOnceReportedStaysValidAndMailQueuedForAnUnapprovedLabelBouncesWithTheLabelReason(t *testing.T) {
	h := newHarness(t, nil)

	h.mailNode(alice, senderSession)
	bobLabel := h.mailNode(bob, recipientSession)

	if err := h.b.Forget(bobLabel); err != nil {
		t.Fatal(err)
	}

	h.registerMail(bob)

	to := h.mailName(bobLabel, recipientSession)

	if resp := h.send(alice, mailIDs[0], to, "waits for approval", senderSession); resp.StatusCode != http.StatusOK {
		t.Fatalf("send: %d", resp.StatusCode)
	}

	// A month later the address is still valid, and the label was never approved.
	h.wall.Set(wallStart.Add(30 * 24 * time.Hour))
	h.snapshot(alice)

	orig := h.data().Mail[mailIDs[0]]
	if !orig.Bounced || orig.BounceReason != wire.ReasonLabelState {
		t.Fatalf("original = %+v, want bounced for the label state", orig)
	}

	h.approve(bob, bobLabel)
	h.reportSessions(alice, senderSession)

	if resp := h.send(alice, mailIDs[1], to, "a month on", senderSession); resp.StatusCode != http.StatusOK {
		t.Errorf("an address once reported was refused after a month: %d", resp.StatusCode)
	}
}

func TestQueuedMailBouncesAtOnceWhenItsNodeLaterReportsNoMailCapability(t *testing.T) {
	h := newHarness(t, nil)

	h.mailNode(alice, senderSession)
	toLabel := h.mailNode(bob, recipientSession)

	if resp := h.send(alice, mailIDs[0], h.mailName(toLabel, recipientSession), "before the downgrade", senderSession); resp.StatusCode != http.StatusOK {
		t.Fatalf("send: %d", resp.StatusCode)
	}

	// The node comes back as an older binary: same key, no capability.
	h.register(bob)

	r, _, _ := h.openStream(bob)

	if _, err := h.b.Enqueue(bob.key(), "probe", map[string]string{}); err != nil {
		t.Fatal(err)
	}

	if ev := nextEvent(t, r); ev.Event != "probe" {
		t.Errorf("an old node was sent %s", ev.Event)
	}

	if got := h.data().Mail[mailIDs[0]]; !got.Bounced || got.BounceReason != wire.ReasonNodeTooOld {
		t.Errorf("original = %+v, want bounced because the node is too old", got)
	}

	ra, _, _ := h.openStream(alice)
	if got := mailEvent(t, nextKind(t, ra, wire.EventBounce)); got.Body != "before the downgrade" || got.Reason != wire.ReasonNodeTooOld {
		t.Errorf("bounce = %+v", got)
	}
}

func TestApprovingTheRealKeyClosesAnImpostorsOpenStreamAndSendsItNoMail(t *testing.T) {
	h := newHarness(t, nil)

	h.mailNode(alice, senderSession)

	label := h.mailNode(bob, recipientSession)

	if err := h.b.Forget(label); err != nil {
		t.Fatal(err)
	}

	h.registerMail(bob)

	// A host on the LAN registers under the sleeping machine's label with a key of its own, and opens a
	// stream. The label is unpinned, so the live-stream rule accepts it.
	h.clock.Advance(testTerm + time.Second)

	impostor := nodeID{Machine: bob.Machine, UID: bob.UID, Key: "lan-host-key"}
	h.registerMail(impostor)

	r, _, _ := h.openStream(impostor)

	if resp := h.send(alice, mailIDs[0], h.mailName(label, recipientSession), "for the real machine only", senderSession); resp.StatusCode != http.StatusOK {
		t.Fatalf("send: %d", resp.StatusCode)
	}

	lines := make(chan string, 64)

	go func() {
		defer close(lines)

		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}

			lines <- line
		}
	}()

	h.approve(bob, label)

	closed := false
	deadline := time.After(5 * time.Second)

	for !closed {
		select {
		case line, ok := <-lines:
			if !ok {
				closed = true

				break
			}

			if strings.Contains(line, "for the real machine only") || strings.HasPrefix(line, "event: mail") {
				t.Fatalf("mail was written to the impostor's stream: %q", line)
			}
		case <-deadline:
			t.Fatal("the impostor's stream stayed open after the real key was approved")
		}
	}

	if got := h.data().Mail[mailIDs[0]]; got.Sent {
		t.Error("the broker marked the message sent to a stream that did not hold the pinned key")
	}
}

func (h *harness) reportRaw(n nodeID, sessions ...wire.MailSession) {
	h.t.Helper()

	resp := h.do(http.MethodPost, wire.PathSnapshot, n, wire.Snapshot{Version: wire.Current, Sessions: sessions})
	if resp.StatusCode/100 != 2 {
		h.t.Fatalf("snapshot status %d", resp.StatusCode)
	}
}

func TestAReportedSessionWithAnUnknownAgentOrAnUnsafeIDIsNotStored(t *testing.T) {
	h := newHarness(t, nil)

	label := h.mailNode(alice, senderSession)

	h.reportRaw(alice,
		wire.MailSession{Agent: "claude", SessionID: senderSession, Live: true},
		wire.MailSession{Agent: "mallory", SessionID: "sess-odd-agent", Live: true},
		wire.MailSession{Agent: "claude", SessionID: "../escape", Live: true},
		wire.MailSession{Agent: "claude", SessionID: strings.Repeat("a", 129), Live: true},
	)

	got := h.data().Sessions[label]
	if len(got) != 1 || got[senderSession].Agent != "claude" {
		t.Errorf("stored sessions = %+v, want only %s", got, senderSession)
	}
}

func TestAPendingLabelsReportedSessionsAreNotStored(t *testing.T) {
	h := newHarness(t, nil)

	label := h.registerMail(bob)
	h.reportSessions(bob, recipientSession)

	if got := h.data().Sessions[label]; len(got) != 0 {
		t.Errorf("a label no one approved stored sessions %+v", got)
	}

	h.approve(bob, label)
	h.reportSessions(bob, recipientSession)

	if _, ok := h.data().Sessions[label][recipientSession]; !ok {
		t.Error("an approved label's next report was not stored")
	}
}

func TestASnapshotPastItsSessionCapKeepsTheFirstOnesAndLogsTheRest(t *testing.T) {
	var logs strings.Builder

	h := newHarness(t, func(c *Config) {
		c.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	})

	label := h.mailNode(alice, senderSession)

	ms := []wire.MailSession{{Agent: "claude", SessionID: senderSession, Live: true}}

	for i := range wire.MaxSnapshotSessions + 2 {
		ms = append(ms, wire.MailSession{Agent: "claude", SessionID: fmt.Sprintf("sess-%04d", i), Live: true})
	}

	h.reportRaw(alice, ms...)

	if got := len(h.data().Sessions[label]); got != wire.MaxSnapshotSessions {
		t.Errorf("stored %d sessions from one snapshot, want %d", got, wire.MaxSnapshotSessions)
	}

	if _, ok := h.data().Sessions[label][ms[len(ms)-1].SessionID]; ok {
		t.Error("a session past the cap was stored")
	}

	if !strings.Contains(logs.String(), "refused") {
		t.Errorf("the refusal was not logged: %q", logs.String())
	}
}

func TestALabelAtItsStoredSessionCapRefusesNewIDsAndKeepsKnownOnesLive(t *testing.T) {
	var logs strings.Builder

	h := newHarness(t, func(c *Config) {
		c.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	})

	label := h.mailNode(alice, senderSession)

	err := h.b.commit(func(d *Data) error {
		for i := range maxLabelSessions {
			d.Sessions[label][fmt.Sprintf("filler-%05d", i)] = SessionRecord{Agent: "claude", NameVersion: MailNameDerivation}
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	h.reportRaw(alice,
		wire.MailSession{Agent: "claude", SessionID: senderSession, Live: true},
		wire.MailSession{Agent: "claude", SessionID: "sess-one-too-many", Live: true},
	)

	if _, ok := h.data().Sessions[label]["sess-one-too-many"]; ok {
		t.Error("a new session was stored past the label's cap")
	}

	if !strings.Contains(logs.String(), "refused") {
		t.Errorf("the refusal was not logged: %q", logs.String())
	}

	h.b.mu.Lock()
	live := h.b.node(alice.key()).liveSessions
	h.b.mu.Unlock()

	if !live[senderSession] || live["sess-one-too-many"] {
		t.Errorf("live set = %v, want the known session only", live)
	}
}
