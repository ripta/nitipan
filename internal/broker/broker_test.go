package broker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/wire"
)

var (
	alice   = nodeID{Machine: "machine-a", UID: 501, Key: "key-a"}
	aliceRB = nodeID{Machine: "machine-a", UID: 502, Key: "key-a2"}
	bob     = nodeID{Machine: "machine-b", UID: 501, Key: "key-b"}
)

func TestRegisterGivesAFourWordLabelDerivedByTheBroker(t *testing.T) {
	h := newHarness(t, nil)

	got, e := h.tryRegister(alice)
	if e != nil {
		t.Fatalf("register: %v", e)
	}

	if want := DeriveLabel("machine-a", 501); got.Label != want || len(strings.Split(got.Label, "-")) != 4 {
		t.Errorf("label = %q, want %q with four words", got.Label, want)
	}

	if got.LabelVersion != LabelDerivation || got.TermMillis != testTerm.Milliseconds() || got.BrokerVersion != "test" {
		t.Errorf("registered = %+v", got)
	}

	row := h.store.Data().Labels[got.Label]
	if row.MachineID != "machine-a" || row.UID != 501 || row.Derivation != LabelDerivation || row.Host != "host-machine-a" {
		t.Errorf("seen-label row = %+v", row)
	}
}

func TestLabelDerivationUsesADomainSeparatorAndBothInputs(t *testing.T) {
	if DeriveLabel("m", 1) == DeriveLabel("m", 2) || DeriveLabel("m", 1) == DeriveLabel("n", 1) {
		t.Error("label ignores the uid or the machine ID")
	}

	if DeriveLabel("m", 1) == handleOf("m", "1") {
		t.Error("label equals the handle derived from the same inputs")
	}
}

func TestTwoOSUsersOnOneMacGetTwoLabelsAndKeepTheirOwnSnapshots(t *testing.T) {
	h := newHarness(t, nil)

	a := h.register(alice)
	b := h.register(aliceRB)

	if a.Label == b.Label {
		t.Fatalf("both users got the label %q", a.Label)
	}

	h.snapshot(alice, rec("A1"))
	h.snapshot(aliceRB, rec("B1"), rec("B2"))
	h.snapshot(alice, rec("A2"))

	if got := h.view(alice.key()).Leases; len(got) != 1 || got[0].ID != "A2" {
		t.Errorf("alice mirror = %+v, want only A2", got)
	}

	if got := h.view(aliceRB.key()).Leases; len(got) != 2 {
		t.Errorf("second user's mirror = %+v, replaced by the first user's snapshot", got)
	}
}

func TestSameCheckoutPathOnTwoMachinesStaysApart(t *testing.T) {
	h := newHarness(t, nil)

	h.register(alice)
	h.register(bob)
	h.snapshot(alice, rec("SAME"))
	h.snapshot(bob, rec("SAME"))

	if h.view(alice.key()).Leases[0].ID != "SAME" || h.view(bob.key()).Leases[0].ID != "SAME" {
		t.Fatal("each machine keeps its own mirror")
	}

	if h.view(alice.key()).Label == h.view(bob.key()).Label {
		t.Error("two machines share a label")
	}
}

type authCase struct {
	Name   string
	Method string
	Path   string
	Body   any
}

var authCases = []authCase{
	{"snapshot", http.MethodPost, wire.PathSnapshot, wire.Snapshot{Version: wire.Current}},
	{"ack", http.MethodPost, wire.PathAck, wire.Ack{Version: wire.Current, EventID: "x"}},
	{"heartbeat", http.MethodPost, wire.PathHeartbeat, wire.Heartbeat{Version: wire.Current}},
	{"ping", http.MethodPost, wire.PathPing, wire.Ping{Version: wire.Current}},
	{"stream", http.MethodGet, wire.PathStream, nil},
}

func TestEveryCallChecksTheKeyRegisteredForTheMachineAndUID(t *testing.T) {
	h := newHarness(t, nil)

	h.register(alice)
	h.register(bob)

	for _, tc := range authCases {
		t.Run(tc.Name, func(t *testing.T) {
			wrong := []nodeID{
				{Machine: alice.Machine, UID: alice.UID, Key: "guess"},
				{Machine: alice.Machine, UID: alice.UID},
				{Machine: alice.Machine, UID: alice.UID, Key: bob.Key},
				{Machine: "machine-unknown", UID: alice.UID, Key: alice.Key},
				{Machine: alice.Machine, UID: 999, Key: alice.Key},
			}

			for _, n := range wrong {
				if got := h.do(tc.Method, tc.Path, n, tc.Body).StatusCode; got != http.StatusUnauthorized {
					t.Errorf("%+v got status %d, want 401", n, got)
				}
			}
		})
	}

	if got := h.snapshot(alice, rec("OK")); got != http.StatusNoContent {
		t.Errorf("the right key got status %d", got)
	}
}

func TestRegisterNeedsAKeyAndAMachineID(t *testing.T) {
	h := newHarness(t, nil)

	_, e := h.tryRegister(nodeID{Machine: "m", UID: 1})
	if e == nil || e.Code != wire.CodeUnauthorized {
		t.Errorf("no key: %v", e)
	}

	_, e = h.tryRegister(nodeID{Machine: "", UID: 1, Key: "k"})
	if e == nil || e.Code != wire.CodeBadRequest {
		t.Errorf("no machine ID: %v", e)
	}
}

func TestRegisterRefusesAMessageOfAnotherMajor(t *testing.T) {
	h := newHarness(t, nil)

	resp := h.do(http.MethodPost, wire.PathRegister, alice, map[string]any{"version": "2.0", "machine_id": "machine-a", "uid": 501})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

type keyCase struct {
	Name       string
	In         KeyInput
	WantRefuse bool
}

var keyCases = []keyCase{
	{"new node", KeyInput{}, false},
	{"same key", KeyInput{Registered: true, Same: true, StreamLive: true}, false},
	{"different key, old stream gone", KeyInput{Registered: true}, false},
	{"different key, old stream live", KeyInput{Registered: true, StreamLive: true}, true},
	{"different key, pinned label, old stream gone", KeyInput{Registered: true, Pinned: true}, true},
	{"same key, pinned label", KeyInput{Registered: true, Same: true, Pinned: true}, false},
}

func TestDefaultKeyPolicy(t *testing.T) {
	for _, tc := range keyCases {
		t.Run(tc.Name, func(t *testing.T) {
			if err := DefaultKeyPolicy(tc.In); (err != nil) != tc.WantRefuse {
				t.Errorf("policy = %v, want refuse %v", err, tc.WantRefuse)
			}
		})
	}
}

// openStream opens the stream and returns a reader of its lines plus a cancel.
func (h *harness) openStream(n nodeID) (*bufio.Reader, *http.Response, context.CancelFunc) {
	h.t.Helper()

	ctx, cancel := context.WithCancel(context.Background())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.srv.URL+wire.PathStream, nil)
	if err != nil {
		h.t.Fatal(err)
	}

	req.Header.Set("Authorization", "Bearer "+n.Key)
	req.Header.Set(wire.HeaderMachine, n.Machine)
	req.Header.Set(wire.HeaderUID, strconv.Itoa(n.UID))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		h.t.Fatal(err)
	}

	h.t.Cleanup(func() {
		cancel()
		resp.Body.Close()
	})

	return bufio.NewReader(resp.Body), resp, cancel
}

func nextEvent(t *testing.T, r *bufio.Reader) wire.SSE {
	t.Helper()

	done := make(chan wire.SSE, 1)
	errc := make(chan error, 1)

	go func() {
		ev, err := wire.NewParser(r).Next()
		if err != nil {
			errc <- err

			return
		}

		done <- ev
	}()

	select {
	case ev := <-done:
		return ev
	case err := <-errc:
		t.Fatalf("stream ended: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("no event within 5s")
	}

	return wire.SSE{}
}

func TestStreamHeadersAndKeepalive(t *testing.T) {
	h := newHarness(t, nil)

	h.register(alice)

	r, resp, _ := h.openStream(alice)
	if resp.Header.Get("Cache-Control") != "no-cache" || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Errorf("headers = %v", resp.Header)
	}

	seen := 0

	for range 3 {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}

		if strings.HasPrefix(line, ": keepalive") {
			seen++
		}
	}

	if seen == 0 {
		t.Error("no keepalive comment arrived on an idle stream")
	}
}

func TestEventsStayQueuedUntilAckedAndReplayOnReconnect(t *testing.T) {
	h := newHarness(t, nil)

	h.register(alice)

	r, _, cancel := h.openStream(alice)

	id, err := h.b.Enqueue(alice.key(), "note", map[string]string{"n": "1"})
	if err != nil {
		t.Fatal(err)
	}

	ev := nextEvent(t, r)
	if ev.ID != id || ev.Event != "note" || ev.Data != `{"n":"1"}` {
		t.Errorf("event = %+v", ev)
	}

	if got := h.b.Unacked(alice.key()); len(got) != 1 {
		t.Fatalf("queue after delivery = %+v, want the event still queued", got)
	}

	cancel()

	r2, _, _ := h.openStream(alice)
	if again := nextEvent(t, r2); again.ID != id {
		t.Errorf("replayed event = %+v, want %s", again, id)
	}

	ack := h.do(http.MethodPost, wire.PathAck, alice, wire.Ack{Version: wire.Current, EventID: id})
	if ack.StatusCode != http.StatusNoContent || len(h.b.Unacked(alice.key())) != 0 {
		t.Errorf("ack status %d, queue %+v", ack.StatusCode, h.b.Unacked(alice.key()))
	}
}

func TestEnqueueForAnUnknownNodeFails(t *testing.T) {
	h := newHarness(t, nil)

	if _, err := h.b.Enqueue(alice.key(), "note", 1); err == nil {
		t.Error("enqueue for a node that never registered succeeded")
	}
}

func TestReconnectWithTheSameKeyReplacesAHalfOpenStream(t *testing.T) {
	h := newHarness(t, nil)

	h.register(alice)

	old, _, _ := h.openStream(alice)

	if _, e := h.tryRegister(alice); e != nil {
		t.Fatalf("re-register with the same key: %v", e)
	}

	h.openStream(alice)

	buf := make([]byte, 1024)

	for {
		if _, err := old.Read(buf); err != nil {
			break
		}
	}

	if !h.view(alice.key()).StreamLive {
		t.Error("the new stream is not live")
	}
}

func TestADifferentKeyWhileTheOldStreamIsLiveIsRefusedAndAfterOneTermIsAccepted(t *testing.T) {
	h := newHarness(t, nil)

	h.register(alice)
	h.openStream(alice)

	other := alice
	other.Key = "someone-else"

	if _, e := h.tryRegister(other); e == nil || e.Code != wire.CodeKeyConflict {
		t.Fatalf("different key while live: %v", e)
	}

	if got := h.snapshot(alice, rec("STILL")); got != http.StatusNoContent {
		t.Errorf("the refused takeover broke the real node: status %d", got)
	}

	h.clock.Advance(testTerm + time.Second)

	got, e := h.tryRegister(other)
	if e != nil {
		t.Fatalf("different key after the stream went silent: %v", e)
	}

	if got.Label != DeriveLabel("machine-a", 501) {
		t.Errorf("label changed with the key: %q", got.Label)
	}

	if h.snapshot(alice, rec("OLD")) != http.StatusUnauthorized {
		t.Error("the old key still works after the new one was accepted")
	}
}

func TestCloseStreamEndsOneNodesStream(t *testing.T) {
	h := newHarness(t, nil)

	h.register(alice)

	r, _, _ := h.openStream(alice)
	readEnded := make(chan error, 1)

	go func() {
		_, err := r.ReadString('\x00')
		readEnded <- err
	}()

	deadline := time.Now().Add(5 * time.Second)

	for !h.b.CloseStream(alice.key()) {
		if time.Now().After(deadline) {
			t.Fatal("no stream to close")
		}

		time.Sleep(5 * time.Millisecond)
	}

	select {
	case err := <-readEnded:
		if err == nil {
			t.Error("the stream delivered data after CloseStream")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stream stayed open after CloseStream")
	}

	if h.b.CloseStream(alice.key()) {
		t.Error("CloseStream reported a stream after it closed")
	}
}

func TestMirrorsGoStaleOneTermAfterTheLastHeartbeatWhileTheConnectionStaysOpen(t *testing.T) {
	h := newHarness(t, nil)

	h.register(alice)
	h.openStream(alice)
	h.snapshot(alice, rec("L"))

	h.clock.Advance(testTerm - time.Second)

	if v := h.view(alice.key()); v.Stale || !v.StreamLive {
		t.Fatalf("view before the term = %+v", v)
	}

	h.clock.Advance(2 * time.Second)

	if v := h.view(alice.key()); !v.Stale || v.StreamLive || len(v.Leases) != 1 {
		t.Fatalf("view after the term, connection still open = %+v", v)
	}

	h.do(http.MethodPost, wire.PathHeartbeat, alice, wire.Heartbeat{Version: wire.Current})

	if v := h.view(alice.key()); v.Stale || !v.StreamLive {
		t.Errorf("a heartbeat did not revive the view: %+v", v)
	}
}

func TestMirrorsGoStaleOneTermAfterTheStreamCloses(t *testing.T) {
	h := newHarness(t, nil)

	h.register(alice)
	h.openStream(alice)
	h.snapshot(alice, rec("L"))
	h.b.CloseStream(alice.key())

	h.clock.Advance(testTerm / 2)

	if h.view(alice.key()).Stale {
		t.Error("stale before the term")
	}

	h.clock.Advance(testTerm)

	if !h.view(alice.key()).Stale {
		t.Error("not stale a term after the stream closed")
	}
}

func TestPingEchoesTheLabel(t *testing.T) {
	h := newHarness(t, nil)

	reg := h.register(alice)

	resp := h.do(http.MethodPost, wire.PathPing, alice, wire.Ping{Version: wire.Current})

	var pong wire.Pong
	if err := json.NewDecoder(resp.Body).Decode(&pong); err != nil || pong.Label != reg.Label {
		t.Errorf("pong = %+v, %v, want %s", pong, err, reg.Label)
	}
}

func TestReplacingTheKeyClosesTheOldStreamThatStayedAttached(t *testing.T) {
	h := newHarness(t, nil)

	h.register(alice)

	old, _, _ := h.openStream(alice)

	h.clock.Advance(testTerm + time.Second)

	other := alice
	other.Key = "replacement"

	if _, e := h.tryRegister(other); e != nil {
		t.Fatalf("replacement key: %v", e)
	}

	ended := make(chan struct{})

	go func() {
		defer close(ended)

		buf := make([]byte, 256)
		for {
			if _, err := old.Read(buf); err != nil {
				return
			}
		}
	}()

	select {
	case <-ended:
	case <-time.After(3 * time.Second):
		t.Fatal("the old stream stayed open and keeps receiving events under the old key")
	}
}

func TestBeatsWithoutAStreamDoNotKeepMirrorsFresh(t *testing.T) {
	h := newHarness(t, nil)

	h.register(alice)
	h.openStream(alice)
	h.snapshot(alice, rec("L"))
	h.b.CloseStream(alice.key())

	deadline := time.Now().Add(3 * time.Second)
	for h.view(alice.key()).StreamLive {
		if time.Now().After(deadline) {
			t.Fatal("stream never detached")
		}

		time.Sleep(5 * time.Millisecond)
	}

	for range 3 {
		h.clock.Advance(testTerm / 2)
		h.do(http.MethodPost, wire.PathHeartbeat, alice, wire.Heartbeat{Version: wire.Current})
		h.snapshot(alice, rec("L"))
	}

	if !h.view(alice.key()).Stale {
		t.Error("a node that keeps posting with no stream attached never reads stale")
	}
}

// TestEveryNodeAPIRouteNeedsNodeCredential tests that every route on the node API refuses
// a request without the node credential.
func TestEveryNodeAPIRouteNeedsNodeCredential(t *testing.T) {
	routes := []struct {
		method string
		path   string
	}{
		{"GET", wire.PathStream},
		{"POST", wire.PathAck},
		{"POST", wire.PathSnapshot},
		{"POST", wire.PathHeartbeat},
		{"POST", wire.PathPing},
		{"POST", wire.PathPair},
		{"POST", wire.PathClaim},
		{"POST", wire.PathRenew},
		{"POST", wire.PathRelease},
		{"POST", wire.PathBreak},
		{"POST", wire.PathList},
		{"POST", wire.PathFence},
		{"POST", wire.PathWhoHolds},
		{"POST", wire.PathWhoIs},
		{"POST", wire.PathWhoAmI},
		{"POST", wire.PathMailSend},
		{"POST", wire.PathMailAck},
		{"POST", wire.PathMailReport},
		{"POST", wire.PathMailStatus},
	}

	for _, tt := range routes {
		t.Run(tt.path, func(t *testing.T) {
			h := newCrossUserHarness(t)

			body := []byte(`{"version":1}`)
			if tt.path == wire.PathStream {
				body = nil
			}

			req := httptest.NewRequest(tt.method, tt.path, bytes.NewReader(body))
			req.Header.Set(wire.HeaderMachine, "machine1")
			req.Header.Set(wire.HeaderUID, "1000")

			w := httptest.NewRecorder()
			h.b.Handler().ServeHTTP(w, req)

			if w.Code != http.StatusUnauthorized {
				t.Errorf("route %s %s without credential: status = %d, want %d", tt.method, tt.path, w.Code, http.StatusUnauthorized)
			}
		})
	}
}

// TestRegisterNeedsNodeCredential tests that register also needs the node credential.
func TestRegisterNeedsNodeCredential(t *testing.T) {
	h := newCrossUserHarness(t)

	req := httptest.NewRequest("POST", wire.PathRegister, bytes.NewReader([]byte(`{"version":1,"machine_id":"m1","uid":1000}`)))
	req.Header.Set(wire.HeaderMachine, "m1")
	req.Header.Set(wire.HeaderUID, "1000")

	w := httptest.NewRecorder()
	h.b.Handler().ServeHTTP(w, req)

	if w.Code == http.StatusOK {
		t.Error("register succeeded without bearer token")
	}
}
