package node

import (
	"bufio"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/broker"
	"github.com/space-armada/nitipan/internal/config"
	"github.com/space-armada/nitipan/internal/handoff"
	"github.com/space-armada/nitipan/internal/liveness"
	"github.com/space-armada/nitipan/internal/mailbox"
	"github.com/space-armada/nitipan/internal/proc/proctest"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/state"
	"github.com/space-armada/nitipan/internal/tlstest"
	"github.com/space-armada/nitipan/internal/wire"
)

var secondMachine = record.Machine{Host: "desktop", ID: "machine-2"}

var startA = record.ProcessStart{Platform: record.PlatformDarwin, Sec: 1}

// mailPair is two approved nodes on one broker over TLS, and the clock of the second.
type mailPair struct {
	r      *rig
	a, b   *running
	bState string
	bNow   atomic.Pointer[time.Time]

	// ackFails counts the mail acks the broker's front door refuses before it lets one through.
	ackFails atomic.Int32
}

func newMailPair(t *testing.T, mutateB func(*Options)) *mailPair {
	t.Helper()

	r := newRig(t)
	files := tlstest.SelfSigned(t, t.TempDir())
	mp := &mailPair{r: r}

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == wire.PathMailAck && mp.ackFails.Add(-1) >= 0 {
			http.Error(w, "the broker lost the connection", http.StatusInternalServerError)

			return
		}

		if f := r.intercept.Load(); f != nil && (*f)(w, req) {
			return
		}

		r.b.Handler().ServeHTTP(w, req)
	}))

	cert, err := tls.LoadX509KeyPair(files.Cert, files.Key)
	if err != nil {
		t.Fatal(err)
	}

	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	optsA := r.options()
	optsA.Config = config.Node{Broker: srv.URL, CAFile: files.Cert}
	optsA.Warn = func(m string) { t.Log("node A: " + m) }

	stateB, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	mp.bState = filepath.Join(stateB, "state")

	now := time.Now()
	mp.bNow.Store(&now)

	optsB := optsA
	optsB.StateDir, optsB.Machine = mp.bState, secondMachine
	optsB.Now = func() time.Time { return *mp.bNow.Load() }
	optsB.Warn = func(m string) { t.Log("node B: " + m) }

	if mutateB != nil {
		mutateB(&optsB)
	}

	mp.a, mp.b = r.start(optsA), r.start(optsB)

	for _, run := range []*running{mp.a, mp.b} {
		eventually(t, "registration", func() bool { return run.n.Label() != "" })
	}

	mp.approve(t, mp.a, r.state)
	mp.approve(t, mp.b, mp.bState)

	return mp
}

// approve pins a node's key the way the user does on the broker host, by the fingerprint the node prints.
func (mp *mailPair) approve(t *testing.T, run *running, stateDir string) {
	t.Helper()

	fp, err := Fingerprint(stateDir)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := mp.r.b.Approve(run.n.Label(), fp); err != nil {
		t.Fatal(err)
	}
}

func (mp *mailPair) register(t *testing.T, stateDir string, m record.Machine, pid int, start record.ProcessStart, session, socket string) mailbox.Registration {
	t.Helper()

	reg, err := mailbox.Refresh(stateDir, mailbox.Registration{
		Agent: "claude", PID: pid, Start: start, Machine: m, Current: session, Endpoint: mailbox.Endpoint{Socket: socket},
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	return reg
}

func (mp *mailPair) address(t *testing.T, run *running, m record.Machine, session string) string {
	t.Helper()

	name, err := broker.DeriveMailName("claude", session, m.ID)
	if err != nil {
		t.Fatal(err)
	}

	return name + "@" + run.n.Label()
}

// send hands a mail-send request to the sender's node and returns its reply.
func (mp *mailPair) send(t *testing.T, stateDir string, m record.Machine, to, body string) (id string, rep handoff.Reply) {
	t.Helper()

	raw, err := json.Marshal(handoff.MailSendBody{To: to, Body: body, FromAgent: "claude", FromSession: "sess-a"})
	if err != nil {
		t.Fatal(err)
	}

	req := handoff.Request{
		Kind: handoff.KindMailSend, ID: state.NewID(), Requester: handoff.Requester{PID: os.Getpid(), Machine: m}, CreatedAt: time.Now(), Body: raw,
	}

	if err := handoff.Submit(stateDir, req); err != nil {
		t.Fatal(err)
	}

	eventually(t, "the reply to the mail request", func() bool {
		got, err := handoff.ReadReply(stateDir, req.ID)
		rep = got

		return err == nil
	})

	return req.ID, rep
}

func (mp *mailPair) inbox(t *testing.T, stateDir string, sessions ...string) []mailbox.Message {
	t.Helper()

	msgs, _, err := mailbox.Pending(stateDir, sessions)
	if err != nil {
		t.Fatal(err)
	}

	return msgs
}

func TestMailIsStoredOnTheRecipientsMachineBeforeTheBrokerDropsItAndTheSenderSeesTheStage(t *testing.T) {
	mp := newMailPair(t, nil)

	mp.register(t, mp.r.state, testMachine, os.Getpid(), startA, "sess-a", "")
	mp.register(t, mp.bState, secondMachine, os.Getpid(), startA, "sess-b", "")

	to := mp.address(t, mp.b, secondMachine, "sess-b")

	id, rep := mp.send(t, mp.r.state, testMachine, to, "from the laptop")
	if rep.Error != nil {
		t.Fatalf("send failed: %+v", rep.Error)
	}

	eventually(t, "the message in the recipient's inbox", func() bool { return len(mp.inbox(t, mp.bState, "sess-b")) == 1 })

	got := mp.inbox(t, mp.bState, "sess-b")[0]

	senderName, _ := broker.DeriveMailName("claude", "sess-a", testMachine.ID)
	if got.ID != id || got.Body != "from the laptop" || !got.SenderVerified || got.Sender != senderName+"@"+mp.a.n.Label() {
		t.Errorf("stored = %+v", got)
	}

	// The sender's next call learns the stage from the broker. No stage is mail.
	eventually(t, "the stored stage on the sender", func() bool {
		s, err := mailbox.ReadSent(mp.r.state, id)

		return err == nil && s.Stage == wire.StageStored
	})

	if msgs := mp.inbox(t, mp.r.state, "sess-a"); len(msgs) != 0 {
		t.Errorf("a stage reached the sender's mailbox as mail: %+v", msgs)
	}

	if _, ok := mailbox.AcceptedState(mp.bState, id); !ok {
		t.Error("the accepted ledger has no entry for the stored message")
	}
}

func TestACrashBetweenStoringAndAckingLeavesOneCopyEvenAfterTheFirstWasRead(t *testing.T) {
	mp := newMailPair(t, nil)

	mp.register(t, mp.r.state, testMachine, os.Getpid(), startA, "sess-a", "")
	mp.register(t, mp.bState, secondMachine, os.Getpid(), startA, "sess-b", "")

	// The broker's front door refuses the first ack, as a crash between the write and the ack would.
	mp.ackFails.Store(1)

	// Reports fail too, so the broker resends instead of learning the message is stored.
	refuse := func(w http.ResponseWriter, req *http.Request) bool {
		if req.URL.Path == wire.PathMailReport {
			http.Error(w, "no", http.StatusInternalServerError)

			return true
		}

		return false
	}
	mp.r.intercept.Store(&refuse)

	to := mp.address(t, mp.b, secondMachine, "sess-b")

	id, rep := mp.send(t, mp.r.state, testMachine, to, "once only")
	if rep.Error != nil {
		t.Fatalf("send failed: %+v", rep.Error)
	}

	eventually(t, "the first copy", func() bool { return len(mp.inbox(t, mp.bState, "sess-b")) == 1 })

	// The reader consumes it before the resend arrives.
	msg := mp.inbox(t, mp.bState, "sess-b")[0]
	if err := mailbox.Remove(mp.bState, msg); err != nil {
		t.Fatal(err)
	}

	if err := mailbox.MarkRead(mp.bState, id); err != nil {
		t.Fatal(err)
	}

	// The failed ack ends the stream. The node reconnects, and the broker sends the message again.
	eventually(t, "the resend to be acked", func() bool {
		deadline := mp.ackFails.Load()

		return deadline < 0 && mp.stageOf(t, id) != wire.StageQueued
	})

	if msgs := mp.inbox(t, mp.bState, "sess-b"); len(msgs) != 0 {
		t.Errorf("a resend after the first copy was read stored a second copy: %+v", msgs)
	}
}

// stageOf asks the sender's node for the message's stage at the broker.
func (mp *mailPair) stageOf(t *testing.T, id string) string {
	t.Helper()

	raw, _ := json.Marshal(handoff.MailStatusBody{ID: id})
	req := handoff.Request{
		Kind: handoff.KindMailStatus, ID: state.NewID(), Requester: handoff.Requester{PID: os.Getpid(), Machine: testMachine}, CreatedAt: time.Now(), Body: raw,
	}

	if err := handoff.Submit(mp.r.state, req); err != nil {
		t.Fatal(err)
	}

	var rep handoff.Reply

	eventually(t, "the status reply", func() bool {
		got, err := handoff.ReadReply(mp.r.state, req.ID)
		rep = got

		return err == nil
	})

	var st wire.MailStatus
	if rep.Error != nil || json.Unmarshal(rep.Body, &st) != nil {
		t.Fatalf("status reply = %+v", rep)
	}

	return st.Stage
}

func TestANodeOnPlainHTTPSendsAndAcceptsNoMailAndSaysWhy(t *testing.T) {
	r := newRig(t)
	run := r.start(r.options())

	eventually(t, "registration", func() bool { return run.n.Label() != "" })

	raw, _ := json.Marshal(handoff.MailSendBody{To: "a-b-c-d@e-f-g-h", Body: "x", FromAgent: "claude", FromSession: "s"})
	req := handoff.Request{Kind: handoff.KindMailSend, ID: state.NewID(), Requester: handoff.Requester{PID: os.Getpid(), Machine: testMachine}, CreatedAt: time.Now(), Body: raw}

	if err := handoff.Submit(r.state, req); err != nil {
		t.Fatal(err)
	}

	rep := waitReply(t, r, req.ID)
	if rep.Error == nil || !strings.Contains(rep.Error.Message, "HTTPS") || !strings.Contains(rep.Error.Message, "plain HTTP") {
		t.Errorf("reply = %+v, want an error that says why", rep.Error)
	}
}

// inboxSocket serves a fake Claude Code inbox socket and sends each line it receives on a channel.
func inboxSocket(t *testing.T) (path string, lines <-chan string) {
	t.Helper()

	dir, err := os.MkdirTemp("", "ns")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	path = filepath.Join(dir, "i.sock")

	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = ln.Close() })

	out := make(chan string, 16)

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}

			go func() {
				defer conn.Close()

				sc := bufio.NewScanner(conn)
				for sc.Scan() {
					out <- sc.Text()
				}
			}()
		}
	}()

	return path, out
}

func expectNoLine(t *testing.T, lines <-chan string) {
	t.Helper()

	select {
	case l := <-lines:
		t.Fatalf("an unexpected ring arrived: %s", l)
	case <-time.After(800 * time.Millisecond):
	}
}

func TestTheNodeRingsAClaudeSocketOnceWithNoContentAndOnlyForThePeerItNamed(t *testing.T) {
	mp := newMailPair(t, nil)

	path, lines := inboxSocket(t)

	mp.register(t, mp.r.state, testMachine, os.Getpid(), startA, "sess-a", "")
	reg := mp.register(t, mp.bState, secondMachine, os.Getpid(), startA, "sess-b", path)
	to := mp.address(t, mp.b, secondMachine, "sess-b")

	if _, rep := mp.send(t, mp.r.state, testMachine, to, "secret body text"); rep.Error != nil {
		t.Fatalf("send failed: %+v", rep.Error)
	}

	var first string

	select {
	case first = <-lines:
	case <-time.After(5 * time.Second):
		t.Fatal("no ring arrived on the inbox socket")
	}

	var ring ringLine
	if err := json.Unmarshal([]byte(first), &ring); err != nil {
		t.Fatalf("ring %q: %v", first, err)
	}

	if ring.Type != "user" || !strings.Contains(ring.Content, "nitipan mail read") || strings.Contains(first, "secret body text") ||
		strings.Contains(first, "token") {
		t.Errorf("ring = %s", first)
	}

	// The same message never wakes the session again, though the node keeps ticking.
	expectNoLine(t, lines)

	if _, ok := mailbox.Rung(mp.bState, reg.Identity)[mailbox.MailKey(mp.inbox(t, mp.bState, "sess-b")[0].ID)]; !ok {
		t.Error("the ring ledger does not hold the rung message")
	}
}

func TestTheNodeNeverRingsASocketWhoseKernelPeerIsNotTheRegisteredPID(t *testing.T) {
	mp := newMailPair(t, nil)

	path, lines := inboxSocket(t)

	mp.register(t, mp.r.state, testMachine, os.Getpid(), startA, "sess-a", "")

	// The registration names a live process that is not the socket's owner.
	reg := mp.register(t, mp.bState, secondMachine, os.Getppid(), startA, "sess-b", path)
	to := mp.address(t, mp.b, secondMachine, "sess-b")

	if _, rep := mp.send(t, mp.r.state, testMachine, to, "x"); rep.Error != nil {
		t.Fatalf("send failed: %+v", rep.Error)
	}

	eventually(t, "the message", func() bool { return len(mp.inbox(t, mp.bState, "sess-b")) == 1 })
	expectNoLine(t, lines)

	if got := mailbox.Rung(mp.bState, reg.Identity); len(got) != 0 {
		t.Errorf("a refused ring was marked rung: %v", got)
	}
}

func TestTheNodeSkipsTheSocketRingWhileALiveWaiterIsArmedAndForADeadRegistration(t *testing.T) {
	table := proctest.New(record.PlatformDarwin, 1)
	table.Add(os.Getpid(), proctest.Process{Start: startA})

	mp := newMailPair(t, func(o *Options) {
		o.Checker = &liveness.Checker{Procs: table, Local: secondMachine}
	})

	path, lines := inboxSocket(t)

	mp.register(t, mp.r.state, testMachine, os.Getpid(), startA, "sess-a", "")
	reg := mp.register(t, mp.bState, secondMachine, os.Getpid(), startA, "sess-b", path)
	to := mp.address(t, mp.b, secondMachine, "sess-b")

	if err := mailbox.Arm(mp.bState, mailbox.WaiterPresence{Identity: reg.Identity, PID: os.Getpid(), Start: startA, Machine: secondMachine, ArmedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	if _, rep := mp.send(t, mp.r.state, testMachine, to, "x"); rep.Error != nil {
		t.Fatalf("send failed: %+v", rep.Error)
	}

	eventually(t, "the message", func() bool { return len(mp.inbox(t, mp.bState, "sess-b")) == 1 })
	expectNoLine(t, lines)

	// A waiter that is gone leaves the ring to the socket.
	mailbox.Disarm(mp.bState, mailbox.WaiterPresence{Identity: reg.Identity, PID: os.Getpid(), Start: startA})

	select {
	case <-lines:
	case <-time.After(5 * time.Second):
		t.Fatal("no socket ring after the waiter was gone")
	}

	// A registration whose process is dead is never rung, though its session is a valid address.
	deadPath, deadLines := inboxSocket(t)
	dead := mp.register(t, mp.bState, secondMachine, 4242, startA, "sess-dead", deadPath)

	eventually(t, "the dead session to be reported", func() bool {
		_, rep := mp.send(t, mp.r.state, testMachine, mp.address(t, mp.b, secondMachine, "sess-dead"), "y")

		return rep.Error == nil
	})

	eventually(t, "the message for the dead session", func() bool { return len(mp.inbox(t, mp.bState, "sess-dead")) >= 1 })
	expectNoLine(t, deadLines)

	if len(mailbox.Rung(mp.bState, dead.Identity)) != 0 {
		t.Error("a dead registration was marked rung")
	}
}

func TestTwoLiveRegistrationsWithOneSessionIDMakeTheAddressAmbiguousAndTheSenderIsBounced(t *testing.T) {
	mp := newMailPair(t, nil)

	mp.register(t, mp.r.state, testMachine, os.Getpid(), startA, "sess-a", "")
	mp.register(t, mp.bState, secondMachine, os.Getpid(), startA, "sess-b", "")
	mp.register(t, mp.bState, secondMachine, os.Getppid(), startA, "sess-b", "")

	to := mp.address(t, mp.b, secondMachine, "sess-b")

	if _, rep := mp.send(t, mp.r.state, testMachine, to, "who gets this"); rep.Error != nil {
		t.Fatalf("send failed: %+v", rep.Error)
	}

	eventually(t, "the bounce in the sender's mailbox", func() bool { return len(mp.inbox(t, mp.r.state, "sess-a")) == 1 })

	b := mp.inbox(t, mp.r.state, "sess-a")[0]
	if b.Kind != mailbox.KindBounce || b.Body != "who gets this" || !strings.Contains(b.Reason, "pid") || b.Sender != "" {
		t.Errorf("bounce = %+v", b)
	}

	if msgs := mp.inbox(t, mp.bState, "sess-b"); len(msgs) != 0 {
		t.Errorf("an ambiguous address stored mail: %+v", msgs)
	}
}

func TestMailForASessionNobodyResumesBouncesToItsSenderWithTheBodyAfterSevenDays(t *testing.T) {
	mp := newMailPair(t, func(o *Options) { o.MailEvery = 50 * time.Millisecond })

	mp.register(t, mp.r.state, testMachine, os.Getpid(), startA, "sess-a", "")
	gone := mp.register(t, mp.bState, secondMachine, os.Getpid(), startA, "sess-gone", "")

	// The broker must report the session at least once so the address is valid.
	eventually(t, "the session to be reported", func() bool {
		to := mp.address(t, mp.b, secondMachine, "sess-gone")
		_, rep := mp.send(t, mp.r.state, testMachine, to, "while you were out")

		return rep.Error == nil
	})

	eventually(t, "the message", func() bool { return len(mp.inbox(t, mp.bState, "sess-gone")) >= 1 })

	if err := mailbox.RemoveRegistration(mp.bState, gone.Identity); err != nil {
		t.Fatal(err)
	}

	later := time.Now().Add(wire.MailRetention + time.Hour)
	mp.bNow.Store(&later)

	eventually(t, "the bounce", func() bool { return len(mp.inbox(t, mp.r.state, "sess-a")) >= 1 })

	b := mp.inbox(t, mp.r.state, "sess-a")[0]
	if b.Kind != mailbox.KindBounce || b.Body != "while you were out" || b.Reason != wire.ReasonNotRunning {
		t.Errorf("bounce = %+v", b)
	}

	eventually(t, "the node to drop its copy", func() bool { return len(mp.inbox(t, mp.bState, "sess-gone")) == 0 })
}

func TestARegistrationDeadForADayIsReapedWithItsLedgerAndWaiterPresence(t *testing.T) {
	table := proctest.New(record.PlatformDarwin, 1)

	mp := newMailPair(t, func(o *Options) {
		o.Checker = &liveness.Checker{Procs: table, Local: secondMachine}
		o.MailEvery = 50 * time.Millisecond
	})

	reg := mp.register(t, mp.bState, secondMachine, 4242, startA, "sess-dead", "")

	if _, err := mailbox.Claim(mp.bState, reg.Identity, []string{mailbox.MailKey(state.NewID())}, time.Now()); err != nil {
		t.Fatal(err)
	}

	later := time.Now().Add(reapAfter + time.Hour)
	mp.bNow.Store(&later)

	eventually(t, "the registration, its ring ledger, and its waiter presence to be reaped", func() bool {
		_, err := mailbox.Find(mp.bState, reg.Identity)

		return err != nil && len(mailbox.Rung(mp.bState, reg.Identity)) == 0
	})
}

func TestAnUnusableRegistrationMakesItsSessionUnknownSoItsMailIsNeverExpired(t *testing.T) {
	mp := newMailPair(t, func(o *Options) { o.MailEvery = 50 * time.Millisecond })

	mp.register(t, mp.r.state, testMachine, os.Getpid(), startA, "sess-a", "")
	gone := mp.register(t, mp.bState, secondMachine, os.Getpid(), startA, "sess-gone", "")

	eventually(t, "the message", func() bool {
		_, rep := mp.send(t, mp.r.state, testMachine, mp.address(t, mp.b, secondMachine, "sess-gone"), "keep me")

		return rep.Error == nil && len(mp.inbox(t, mp.bState, "sess-gone")) >= 1
	})

	// The only registration that held the session is replaced by a file this node cannot read. It may
	// name the session, so the session is unknown, never dead.
	if err := mailbox.RemoveRegistration(mp.bState, gone.Identity); err != nil {
		t.Fatal(err)
	}

	future := filepath.Join(mp.bState, mailbox.RegDir, "future.json")
	if err := os.WriteFile(future, []byte(`{"version":"9.0","identity":"future"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	later := time.Now().Add(wire.MailRetention + time.Hour)
	mp.bNow.Store(&later)

	// Several service ticks pass.
	time.Sleep(600 * time.Millisecond)

	if len(mp.inbox(t, mp.bState, "sess-gone")) == 0 {
		t.Error("stored mail was expired because its session's registration is unreadable")
	}

	if msgs := mp.inbox(t, mp.r.state, "sess-a"); len(msgs) != 0 {
		t.Errorf("the sender was bounced mail for an unknown session: %+v", msgs)
	}

	if _, err := os.Stat(future); err != nil {
		t.Error("the unreadable registration was reaped")
	}
}
