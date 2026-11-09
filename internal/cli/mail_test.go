package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/broker"
	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/handoff"
	"github.com/space-armada/nitipan/internal/mailbox"
	"github.com/space-armada/nitipan/internal/node"
	"github.com/space-armada/nitipan/internal/proc/proctest"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/state"
	"github.com/space-armada/nitipan/internal/wire"
)

// mailWorld is two machines on one TLS broker, with approved nodes, and a Claude Code session on each.
type mailWorld struct {
	f      *wideFixture
	m1, m2 *wideMachine

	// c1 and c2 are the sessions s1 and s2. c3 is a second process that also holds s2. c2b is a process
	// that resumes s2 after c2 is gone.
	c1, c2, c3, c2b caller
}

func newMailWorld(t *testing.T) *mailWorld {
	t.Helper()

	f := newWideFixtureTLS(t)
	mw := &mailWorld{f: f, m1: f.machine("m1"), m2: f.machine("m2")}

	mw.c1 = mw.m1.w.claude(100, "s1")
	mw.c2 = mw.m2.w.claude(200, "s2")
	mw.c3 = mw.m2.w.claude(400, "s2")
	mw.c2b = mw.m2.w.claude(300, "s2")

	mw.m1.start()
	mw.m2.start()

	for _, m := range []*wideMachine{mw.m1, mw.m2} {
		mw.approve(t, m)
	}

	return mw
}

func (mw *mailWorld) label(t *testing.T, machineID string) string {
	t.Helper()

	var label string

	waitFor(t, "the node of "+machineID+" to register", func() bool {
		for _, v := range mw.f.b.Nodes() {
			if v.Key.MachineID == machineID {
				label = v.Label
			}
		}

		return label != ""
	})

	return label
}

func (mw *mailWorld) approve(t *testing.T, m *wideMachine) {
	t.Helper()

	fp, err := node.Fingerprint(m.w.state)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := mw.f.b.Approve(mw.label(t, m.w.machine.ID), fp); err != nil {
		t.Fatal(err)
	}
}

func (mw *mailWorld) addr(t *testing.T, m *wideMachine, session string) string {
	t.Helper()

	name, err := broker.DeriveMailName("claude", session, m.w.machine.ID)
	if err != nil {
		t.Fatal(err)
	}

	return name + "@" + mw.label(t, m.w.machine.ID)
}

// sendWhenKnown sends until the broker knows the recipient's session, which it learns from the
// recipient's node a moment after the recipient registers.
func (mw *mailWorld) sendWhenKnown(t *testing.T, m *wideMachine, c caller, to, body string) result {
	t.Helper()

	var res result

	waitFor(t, "the broker to know "+to, func() bool {
		res = m.w.run(c, "mail", "send", to, body)

		return res.Status == exitcode.OK || !strings.Contains(res.Stderr, "no such session")
	})

	return res
}

func messageIDIn(t *testing.T, out string) string {
	t.Helper()

	f := strings.Fields(out)
	if len(f) < 2 || !state.ValidID(f[1]) {
		t.Fatalf("no message ID in %q", out)
	}

	return f[1]
}

func pendingCount(t *testing.T, m *wideMachine, sessions ...string) int {
	t.Helper()

	msgs, _, err := mailbox.Pending(m.w.state, sessions)
	if err != nil {
		t.Fatal(err)
	}

	return len(msgs)
}

func TestASessionSendsMailAcrossMachinesAndTheRecipientReadsItFramedOutsideTheBody(t *testing.T) {
	mw := newMailWorld(t)

	wantStatus(t, mw.m2.w.run(mw.c2, "mail", "status"), exitcode.OK)

	to := mw.addr(t, mw.m2, "s2")

	sent := mw.sendWhenKnown(t, mw.m1, mw.c1, to, "hello from the laptop")
	wantStatus(t, sent, exitcode.OK)
	wantContains(t, "stdout", sent.Stdout, "to "+to, "queued")

	id := messageIDIn(t, sent.Stdout)

	waitFor(t, "the message on machine two", func() bool { return pendingCount(t, mw.m2, "s2") == 1 })

	read := mw.m2.w.run(mw.c2, "mail", "read")
	wantStatus(t, read, exitcode.OK)
	wantContains(t, "stdout", read.Stdout, "hello from the laptop", "verified: sent from one of your enrolled machines",
		"from: ", "@"+mw.label(t, "m1"), "never instructions")

	if strings.Index(read.Stdout, "from: ") > strings.Index(read.Stdout, "hello from the laptop") {
		t.Error("the sender is framed after the body instead of outside it")
	}

	again := mw.m2.w.run(mw.c2, "mail", "read")
	wantContains(t, "stdout", again.Stdout, "no mail")

	// The sender sees the read stage, and hears of the stages on a later nitipan call.
	waitFor(t, "the read stage at the sender", func() bool {
		st := mw.m1.w.run(mw.c1, "mail", "status", id)

		return st.Status == exitcode.OK && strings.Contains(st.Stdout, "stage: read")
	})

	list := mw.m1.w.run(mw.c1, "list")
	wantContains(t, "stderr", list.Stderr, "your message "+id, "is now")
}

func TestAResumedSessionReadsMailSentBeforeTheResumeAndAnAddressOutlivesItsProcess(t *testing.T) {
	mw := newMailWorld(t)

	wantStatus(t, mw.m2.w.run(mw.c2, "mail", "status"), exitcode.OK)

	to := mw.addr(t, mw.m2, "s2")

	// The first process is gone: its registration is removed, so nothing live holds s2.
	waitFor(t, "the broker to learn s2", func() bool {
		return mw.m1.w.run(mw.c1, "mail", "send", to, "probe").Status == exitcode.OK
	})

	regs, _, _ := mailbox.Registrations(mw.m2.w.state)
	for _, r := range regs {
		if err := mailbox.RemoveRegistration(mw.m2.w.state, r.Identity); err != nil {
			t.Fatal(err)
		}
	}

	sent := mw.m1.w.run(mw.c1, "mail", "send", to, "sent while you were away")
	wantStatus(t, sent, exitcode.OK)

	waitFor(t, "both messages stored", func() bool { return pendingCount(t, mw.m2, "s2") == 2 })

	// A new process resumes the session. It has the same session ID, so it has the same address.
	read := mw.m2.w.run(mw.c2b, "mail", "read")
	wantStatus(t, read, exitcode.OK)
	wantContains(t, "stdout", read.Stdout, "sent while you were away", "probe")
}

func TestMailToAPreClearAddressReachesTheLiveProcessAndSaysSo(t *testing.T) {
	mw := newMailWorld(t)

	wantStatus(t, mw.m2.w.run(mw.c2.withSession("s2-old"), "mail", "status"), exitcode.OK)

	cleared := mw.c2.withSession("s2-new")
	wantStatus(t, mw.m2.w.run(cleared, "mail", "status"), exitcode.OK)

	to := mw.addr(t, mw.m2, "s2-old")

	wantStatus(t, mw.sendWhenKnown(t, mw.m1, mw.c1, to, "to your old address"), exitcode.OK)

	waitFor(t, "the message", func() bool { return pendingCount(t, mw.m2, "s2-old") == 1 })

	read := mw.m2.w.run(cleared, "mail", "read")
	wantContains(t, "stdout", read.Stdout, "to your old address", "from before a /clear")
}

func TestTwoLiveSessionsWithOneSessionIDBounceTheMailToItsSender(t *testing.T) {
	mw := newMailWorld(t)

	wantStatus(t, mw.m2.w.run(mw.c2, "mail", "status"), exitcode.OK)
	wantStatus(t, mw.m2.w.run(mw.c3, "mail", "status"), exitcode.OK)

	to := mw.addr(t, mw.m2, "s2")

	wantStatus(t, mw.sendWhenKnown(t, mw.m1, mw.c1, to, "who is s2"), exitcode.OK)

	waitFor(t, "the bounce", func() bool { return pendingCount(t, mw.m1, "s1") == 1 })

	read := mw.m1.w.run(mw.c1, "mail", "read")
	wantContains(t, "stdout", read.Stdout, "returned by the broker", "who is s2", "two live sessions hold the recipient's address")

	if pendingCount(t, mw.m2, "s2") != 0 {
		t.Error("an ambiguous address stored mail")
	}
}

func TestAClaudeHolderWithNoSessionIDIsNotMailable(t *testing.T) {
	mw := newMailWorld(t)

	bare := mw.c1
	bare.env = map[string]string{"CLAUDE_PID": "100"}

	res := mw.m1.w.run(bare, "mail", "status")
	wantStatus(t, res, exitcode.Error)
	wantContains(t, "stderr", res.Stderr, "not mailable", "no session ID")

	res = mw.m1.w.run(bare, "mail", "send", "some-four-word-name@some-label-for-a-machine", "x")
	wantContains(t, "stderr", res.Stderr, "not mailable")
}

func TestMailRefusesABodyOverTheCapBeforeTheBrokerSeesIt(t *testing.T) {
	mw := newMailWorld(t)

	res := mw.m1.w.run(mw.c1, "mail", "send", "a-b-c-d@e-f-g-h", strings.Repeat("x", wire.MaxMailBody+1))
	wantStatus(t, res, exitcode.Usage)
	wantContains(t, "stderr", res.Stderr, "over")
}

func TestANodeOnPlainHTTPGetsNoMailAndTheErrorSaysWhy(t *testing.T) {
	f := newWideFixture(t)
	m := f.machine("m1")
	c := m.w.claude(100, "s1")

	m.start()

	res := m.w.run(c, "mail", "send", "a-b-c-d@e-f-g-h", "x")
	wantStatus(t, res, exitcode.Error)
	wantContains(t, "stderr", res.Stderr, "plain HTTP", "HTTPS")
}

func TestANewNodeWithAnOldBrokerReportsBrokerTooOldForMail(t *testing.T) {
	mw := newMailWorld(t)

	path := wire.PathMailSend
	mw.f.missing.Store(&path)

	wantStatus(t, mw.m2.w.run(mw.c2, "mail", "status"), exitcode.OK)

	res := mw.m1.w.run(mw.c1, "mail", "send", mw.addr(t, mw.m2, "s2"), "x")
	wantStatus(t, res, exitcode.Error)
	wantContains(t, "stderr", res.Stderr, "broker too old", "only a broker that serves mail")
}

func TestAnOldNodeIsToldToUpgradeBeforeMailIsHandedToIt(t *testing.T) {
	mw := newMailWorld(t)

	mw.m1.stop()

	old := handoff.Presence{PID: nodePID, Start: startOf(nodePID), Machine: mw.m1.w.machine, NodeVersion: "old", RequestVersion: 3,
		Kinds: []string{"ping", handoff.KindClaim, handoff.KindWhoHolds}}
	if err := handoff.WritePresence(mw.m1.w.state, old); err != nil {
		t.Fatal(err)
	}

	res := mw.m1.w.run(mw.c1, "mail", "send", "a-b-c-d@e-f-g-h", "x")
	wantStatus(t, res, exitcode.Error)
	wantContains(t, "stderr", res.Stderr, "node too old")
}

// waiterDeps builds the deps of a hook process that runs beside the test: its own process table, so
// two waiters have two PIDs, and its own buffers.
func waiterDeps(w *world, c caller, self int, ctx context.Context, stdout, stderr *bytes.Buffer) Deps {
	d := w.deps(c, stdout, stderr)

	table := proctest.New(record.PlatformDarwin, self)
	for pid, p := range w.table.Procs {
		table.Add(pid, *p)
	}

	table.Add(self, proctest.Process{Start: startOf(self), PPID: c.shellPID})

	d.Procs, d.Context = table, ctx

	return d
}

type waiterRun struct {
	status         int
	stdout, stderr *bytes.Buffer
	done           chan struct{}
}

func startWaiter(w *world, c caller, self int, ctx context.Context) *waiterRun {
	run := &waiterRun{stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, done: make(chan struct{})}
	deps := waiterDeps(w, c, self, ctx, run.stdout, run.stderr)

	go func() {
		run.status = Execute(deps, []string{"hook", "mail-wait"})

		close(run.done)
	}()

	return run
}

func (r *waiterRun) finished(wait time.Duration) bool {
	select {
	case <-r.done:
		return true
	case <-time.After(wait):
		return false
	}
}

func TestTheWaiterWakesAnIdleSessionWithStatusTwoAndLeavesTheMailInPlace(t *testing.T) {
	mw := newMailWorld(t)

	wantStatus(t, mw.m2.w.run(mw.c2, "mail", "status"), exitcode.OK)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	waiter := startWaiter(mw.m2.w, mw.c2, 8001, ctx)

	if waiter.finished(200 * time.Millisecond) {
		t.Fatalf("the waiter exited with no mail: %d %s", waiter.status, waiter.stderr)
	}

	wantStatus(t, mw.sendWhenKnown(t, mw.m1, mw.c1, mw.addr(t, mw.m2, "s2"), "wake up"), exitcode.OK)

	if !waiter.finished(5 * time.Second) {
		t.Fatal("the waiter never woke the session")
	}

	if waiter.status != 2 || !strings.Contains(waiter.stderr.String(), "nitipan mail read") || strings.Contains(waiter.stderr.String(), "wake up") {
		t.Errorf("waiter = status %d, stderr %q, want status 2 and a ring with no content", waiter.status, waiter.stderr)
	}

	// No wake path removes mail.
	if pendingCount(t, mw.m2, "s2") != 1 {
		t.Error("the ring consumed the message")
	}

	// A session that ignores the ring is not woken again for the same message.
	second := startWaiter(mw.m2.w, mw.c2, 8002, ctx)
	if second.finished(300 * time.Millisecond) {
		t.Fatalf("a second waiter rang for a message already rung: %d %s", second.status, second.stderr)
	}

	cancel()

	if !second.finished(5*time.Second) || second.status != 0 {
		t.Errorf("a cancelled waiter = %d", second.status)
	}
}

func TestANewerWaiterReplacesTheOlderAndADayOfTurnsLeavesOneWaiter(t *testing.T) {
	mw := newMailWorld(t)

	wantStatus(t, mw.m2.w.run(mw.c2, "mail", "status"), exitcode.OK)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var waiters []*waiterRun

	for i := range 6 {
		waiters = append(waiters, startWaiter(mw.m2.w, mw.c2, 8100+i, ctx))

		time.Sleep(60 * time.Millisecond)
	}

	for i, w := range waiters[:5] {
		if !w.finished(5*time.Second) || w.status != 0 {
			t.Fatalf("waiter %d was not replaced: finished %v status %d", i, w.finished(0), w.status)
		}
	}

	if waiters[5].finished(100 * time.Millisecond) {
		t.Fatal("the newest waiter exited")
	}

	regs, _, _ := mailbox.Registrations(mw.m2.w.state)
	if w, ok := mailbox.Waiter(mw.m2.w.state, regs[0].Identity); !ok || w.PID != 8105 {
		t.Errorf("presence = %+v, %v, want the newest waiter", w, ok)
	}

	cancel()
	waiters[5].finished(5 * time.Second)

	if _, ok := mailbox.Waiter(mw.m2.w.state, regs[0].Identity); ok {
		t.Error("a waiter left its presence file behind")
	}
}

func TestAWaiterFacingCrossSessionInboundRefuseStandsDownInEveryScope(t *testing.T) {
	scopes := []struct {
		Name string
		Path func(w *world) string
	}{
		{"user", func(w *world) string { return filepath.Join(w.root, "userhome", ".claude", "settings.json") }},
		{"project", func(w *world) string { return filepath.Join(w.root, ".claude", "settings.json") }},
		{"local", func(w *world) string { return filepath.Join(w.root, ".claude", "settings.local.json") }},
		{"managed", func(w *world) string { return filepath.Join(w.root, "managed", "managed-settings.json") }},
		{"managed drop-in", func(w *world) string { return filepath.Join(w.root, "managed", "managed-settings.d", "10-org.json") }},
	}

	for _, sc := range scopes {
		t.Run(sc.Name, func(t *testing.T) {
			w := newWorld(t)
			c := w.claude(100, "s1")

			path := sc.Path(w)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}

			if err := os.WriteFile(path, []byte(`{"crossSessionInbound":"refuse"}`), 0o600); err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			var stdout, stderr bytes.Buffer

			d := waiterDeps(w, c, 8001, ctx, &stdout, &stderr)
			d.ManagedSettingsDirs = []string{filepath.Join(w.root, "managed")}

			if status := Execute(d, []string{"hook", "mail-wait"}); status != 0 || stderr.Len() != 0 {
				t.Errorf("status %d, stderr %q, want a quiet stand-down", status, &stderr)
			}

			if ctx.Err() != nil {
				t.Error("the waiter kept waiting")
			}

			regs, _, _ := mailbox.Registrations(w.state)
			for _, r := range regs {
				if _, armed := mailbox.Waiter(w.state, r.Identity); armed {
					t.Error("a waiter that stood down left a presence file")
				}
			}
		})
	}
}

func TestTurnHooksAndIdentifiedHolderCallsRingWithoutRecordingOrRemoving(t *testing.T) {
	mw := newMailWorld(t)

	wantStatus(t, mw.m2.w.run(mw.c2, "mail", "status"), exitcode.OK)
	wantStatus(t, mw.sendWhenKnown(t, mw.m1, mw.c1, mw.addr(t, mw.m2, "s2"), "ring me"), exitcode.OK)

	waitFor(t, "the message", func() bool { return pendingCount(t, mw.m2, "s2") == 1 })

	for range 2 {
		hook := mw.m2.w.run(mw.c2, "hook", "mail-ring", "--event", "UserPromptSubmit")
		wantStatus(t, hook, exitcode.OK)

		var out struct {
			Specific struct {
				Event   string `json:"hookEventName"`
				Context string `json:"additionalContext"`
			} `json:"hookSpecificOutput"`
		}

		if err := json.Unmarshal([]byte(hook.Stdout), &out); err != nil || out.Specific.Event != "UserPromptSubmit" ||
			!strings.Contains(out.Specific.Context, "nitipan mail read") || strings.Contains(hook.Stdout, "ring me") {
			t.Fatalf("hook output %q: %v", hook.Stdout, err)
		}
	}

	if bad := mw.m2.w.run(mw.c2, "hook", "mail-ring", "--event", "Stop"); bad.Status != exitcode.Usage {
		t.Errorf("an event the hook does not serve = %d", bad.Status)
	}

	for range 2 {
		list := mw.m2.w.run(mw.c2, "list")
		wantContains(t, "stderr", list.Stderr, "1 message", "nitipan mail read")
	}

	regs, _, _ := mailbox.Registrations(mw.m2.w.state)
	if len(mailbox.Rung(mw.m2.w.state, regs[0].Identity)) != 0 {
		t.Error("a non-waking ring wrote the ring ledger")
	}

	if pendingCount(t, mw.m2, "s2") != 1 {
		t.Error("a non-waking ring removed mail")
	}
}

func TestMailOfAnUnknownVersionIsNeverRungOrReadAndStatusSaysWhy(t *testing.T) {
	mw := newMailWorld(t)

	wantStatus(t, mw.m2.w.run(mw.c2, "mail", "status"), exitcode.OK)

	id := state.NewID()
	path := filepath.Join(mw.m2.w.state, mailbox.InboxDir, "s2."+id+".json")

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(`{"version":"9.0","id":"`+id+`","to_session":"s2","body":"from the future"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	reg := filepath.Join(mw.m2.w.state, mailbox.RegDir, "future.json")
	if err := os.WriteFile(reg, []byte(`{"version":"9.0","identity":"future"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	status := mw.m2.w.run(mw.c2, "mail", "status")
	wantContains(t, "stdout", status.Stdout, "mail waiting: 0", "cannot use", "Upgrade nitipan", "registration future.json is not usable")

	hook := mw.m2.w.run(mw.c2, "hook", "mail-ring", "--event", "PostToolUse")
	if hook.Stdout != "" {
		t.Errorf("an unknown version was rung: %q", hook.Stdout)
	}

	read := mw.m2.w.run(mw.c2, "mail", "read")
	wantContains(t, "stdout", read.Stdout, "no mail")

	if _, err := os.Stat(path); err != nil {
		t.Error("a file of an unknown version was removed")
	}
}

func TestSetupClaudePrintsTheMailHooksWithTheWaiterAsAsyncRewake(t *testing.T) {
	w := newWorld(t)
	c := w.terminal(50)

	res := w.run(c, "setup", "claude")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "hook mail-register", "hook mail-wait", "asyncRewake", "86400", "hook mail-ring --event UserPromptSubmit",
		"hook mail-ring --event PostToolUse", `"Stop"`, `"SessionStart"`, "crossSessionInbound")

	_, afterIntro, _ := strings.Cut(res.Stdout, `"hooks" key of your Claude Code settings:`)
	hooks, _, _ := strings.Cut(afterIntro, "SessionStart registers")

	var parsed map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(hooks)), &parsed); err != nil {
		t.Errorf("the printed hooks are not JSON: %v\n%s", err, hooks)
	}
}

func TestTheApprovalCommandsReachTheBrokerOnlyThroughItsAdminSocket(t *testing.T) {
	mw := newMailWorld(t)

	dir, err := os.MkdirTemp("", "ns")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	sock := filepath.Join(dir, "admin.sock")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() { done <- mw.f.b.ServeAdmin(ctx, sock) }()

	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Log(err)
		}
	})

	waitFor(t, "the admin socket", func() bool {
		_, err := os.Stat(sock)

		return err == nil
	})

	term := mw.m1.w.terminal(70)

	run := func(args ...string) result {
		var stdout, stderr bytes.Buffer

		d := mw.m1.w.deps(term, &stdout, &stderr)
		d.AdminSocket = sock

		return result{Status: Execute(d, args), Stdout: stdout.String(), Stderr: stderr.String()}
	}

	label := mw.label(t, "m2")

	forget := run("broker", "forget", label)
	wantStatus(t, forget, exitcode.OK)
	wantContains(t, "stdout", forget.Stdout, "forgot "+label)

	// The machine's node registers again under a new key. The user compares the fingerprint it prints.
	if err := os.Remove(filepath.Join(mw.m2.w.state, "node-key")); err != nil {
		t.Fatal(err)
	}

	fp, err := node.Fingerprint(mw.m2.w.state)
	if err != nil {
		t.Fatal(err)
	}

	mw.m2.stop()
	mw.m2.start()

	var pending result

	waitFor(t, "the new key to wait for approval", func() bool {
		pending = run("broker", "pending")

		return strings.Contains(pending.Stdout, fp)
	})

	wantContains(t, "stdout", pending.Stdout, label, "forgotten", "key "+fp)

	early := run("broker", "approve", label, "0000-0000-0000-0000")
	wantStatus(t, early, exitcode.OK)
	wantContains(t, "stdout", early.Stdout, "no key with that fingerprint has reached the broker", "agent session runs as this OS")

	malformed := run("broker", "approve", label, "not-a-fingerprint")
	wantStatus(t, malformed, exitcode.Error)
	wantContains(t, "stderr", malformed.Stderr, "four groups of four hex digits")

	approve := run("broker", "approve", label, fp)
	wantStatus(t, approve, exitcode.OK)
	wantContains(t, "stdout", approve.Stdout, "approved "+label, "no agent session runs as this OS", fp)

	again := run("broker", "approve", label, "1111-1111-1111-1111")
	wantStatus(t, again, exitcode.Error)
	wantContains(t, "stderr", again.Stderr, "already approved", "forget")

	printed := mw.m2.w.run(mw.c2, "node", "fingerprint")
	wantStatus(t, printed, exitcode.OK)
	wantContains(t, "stdout", printed.Stdout, fp)

	setupNode := mw.m2.w.run(mw.c2, "setup", "node")
	wantContains(t, "stdout", setupNode.Stdout, fp, "nitipan broker approve")

	fresh := newWorld(t)
	bare := fresh.run(fresh.terminal(50), "setup", "node")
	wantContains(t, "stdout", bare.Stdout, "nitipan node fingerprint")

	if _, err := os.Stat(fresh.state); err == nil {
		t.Error("setup node created the state directory")
	}

	setupBroker := mw.m2.w.run(mw.c2, "setup", "broker")
	wantContains(t, "stdout", setupBroker.Stdout, "agent session may run as that OS user")

	// No handoff kind carries an approval, since the handoff directory is writable from a sandbox.
	p, err := handoff.ReadPresence(mw.m1.w.state)
	if err != nil {
		t.Fatal(err)
	}

	for _, k := range p.Kinds {
		if strings.Contains(k, "approve") || strings.Contains(k, "forget") || strings.Contains(k, "pending") {
			t.Errorf("the node serves the handoff kind %q", k)
		}
	}
}
