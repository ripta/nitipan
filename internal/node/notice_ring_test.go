package node

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/config"
	"github.com/space-armada/nitipan/internal/keys"
	"github.com/space-armada/nitipan/internal/lease"
	"github.com/space-armada/nitipan/internal/liveness"
	"github.com/space-armada/nitipan/internal/mailbox"
	"github.com/space-armada/nitipan/internal/proc/proctest"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/state"
)

func plantNotice(t *testing.T, root string, reg mailbox.Registration, cause, target string, at time.Time) lease.Ring {
	t.Helper()

	env := reg.Envelope()
	env.Version = record.ParentVersion
	env.ID = record.NoticeID(target, cause)
	env.Footprint = record.NoneFootprint()
	env.ClearWhenDead = true

	n := &record.Notice{Envelope: env, Cause: cause, Target: target, Detail: "private detail", At: at}

	data, err := n.Encode()
	if err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(root, state.NoticesDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, n.ID+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	return lease.Ring{Kind: lease.RingNotice, ID: n.ID, At: at}
}

type codexRing struct {
	n       *Node
	root    string
	capture string
	reg     mailbox.Registration
}

// newCodexRing builds a node whose codex executable records its arguments, and a live Codex session.
func newCodexRing(t *testing.T) *codexRing {
	t.Helper()

	root := t.TempDir()
	home := t.TempDir()
	capture := filepath.Join(t.TempDir(), "calls")
	exe := filepath.Join(t.TempDir(), "codex")

	script := "#!/bin/sh\nprintf '%s\\n' \"$CODEX_HOME\" \"$@\" >> '" + capture + "'\n"
	if err := os.WriteFile(exe, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	table := proctest.New(record.PlatformDarwin, 1)
	table.Add(42, proctest.Process{Start: startA})

	n, err := New(Options{
		StateDir: root, Machine: secondMachine, Config: config.Node{Broker: "https://broker.example"}, CodexPath: exe, CodexHome: home,
		Checker: &liveness.Checker{Procs: table, Local: secondMachine},
	})
	if err != nil {
		t.Fatal(err)
	}

	reg, err := mailbox.Refresh(root, mailbox.Registration{
		Agent: "codex", PID: 42, Start: startA, Machine: secondMachine, Current: "one", Endpoint: mailbox.Endpoint{CodexHome: home},
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	return &codexRing{n: n, root: root, capture: capture, reg: reg}
}

func (cr *codexRing) queued(t *testing.T) string {
	t.Helper()

	data, err := os.ReadFile(cr.capture)
	if os.IsNotExist(err) {
		return ""
	}

	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

// The node judges notices with the lease package's own verdict, over an unlocked read. The state lock is
// held for the whole test, so a node that took it would never ring.
func TestCodexQueueRingsForANoticeWithoutTheLeaseLockAndAgainOnlyForAReplacedOne(t *testing.T) {
	cr := newCodexRing(t)

	dir, err := state.Open(cr.root, state.Options{})
	if err != nil {
		t.Fatal(err)
	}

	locked, err := dir.Lock()
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = locked.Unlock() }()

	at := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	target := state.NewID()

	ring := plantNotice(t, cr.root, cr.reg, record.CauseRequestGranted, target, at)
	plantNotice(t, cr.root, cr.reg, record.CauseSharedClaimRefused, state.NewID(), at)
	plantNotice(t, cr.root, cr.reg, record.CauseParentEnded, state.NewID(), at)

	cr.n.ringMail()
	cr.n.ringMail()

	got := cr.queued(t)
	wantArgs := cr.n.opts.CodexHome + "\nqueue\n--thread\none\n--message\n" + mailbox.LeaseRingText(1, 0) + "\n"
	if got != wantArgs {
		t.Fatalf("queue arguments %q, want %q", got, wantArgs)
	}

	if !strings.Contains(got, "nitipan list") || strings.Contains(got, "private detail") {
		t.Errorf("ring text %q must name nitipan list and carry no detail", got)
	}

	rung := mailbox.Rung(cr.root, cr.reg.Identity)
	if _, ok := rung[ring.Key()]; !ok || len(rung) != 1 {
		t.Errorf("ring ledger = %v, want only %s", rung, ring.Key())
	}

	replaced := plantNotice(t, cr.root, cr.reg, record.CauseRequestGranted, target, at.Add(time.Minute))

	cr.n.ringMail()
	cr.n.ringMail()

	if count := strings.Count(cr.queued(t), "queue\n"); count != 2 {
		t.Errorf("the queue ran %d times, want 2: once for the notice and once for the replaced one", count)
	}

	if _, ok := mailbox.Rung(cr.root, cr.reg.Identity)[replaced.Key()]; !ok {
		t.Errorf("the replaced notice's key %s is not in the ledger", replaced.Key())
	}

	if _, err := os.Stat(filepath.Join(cr.root, state.NoticesDir, ring.ID+".json")); err != nil {
		t.Error("a ring consumed the notice")
	}
}

// A request rings the registration whose lease it targets, through the same holder mapping as a notice.
func TestCodexQueueRingsForAPendingRequestOnALeaseTheRegistrationHolds(t *testing.T) {
	cr := newCodexRing(t)

	path := "/work/repo"
	env := cr.reg.Envelope()
	env.Version = record.PathVersion
	env.ID = state.NewID()
	env.Footprint = record.PathFootprint([]keys.Key{{Path: path}})
	env.ClearWhenDead = true

	lease := record.Record{
		Envelope: env, Reach: record.ReachMachine, ActingFor: "tester",
		Domain:     record.Domain{Kind: record.KindPath, Checkout: path, Keys: []keys.Key{{Path: path}}},
		AcquiredAt: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC),
	}

	writeEncoded(t, cr.root, state.LeasesDir, env.ID, &lease)

	reqEnv := env
	reqEnv.ID = state.NewID()
	reqEnv.Liveness = record.LivenessProcess
	reqEnv.Holder = record.Holder{Agent: "claude", PID: 42, Start: startA}

	req := record.Request{
		Envelope: reqEnv, Parent: env.ID, ActingFor: "tester", RequestedAt: time.Date(2026, 11, 1, 9, 5, 0, 0, time.UTC),
		Domain: record.Domain{Kind: record.KindPath, Checkout: path, Keys: []keys.Key{{Path: path + "/src"}}},
	}

	writeEncoded(t, cr.root, state.RequestsDir, reqEnv.ID, &req)

	cr.n.ringMail()
	cr.n.ringMail()

	want := cr.n.opts.CodexHome + "\nqueue\n--thread\none\n--message\n" + mailbox.LeaseRingText(0, 1) + "\n"
	if got := cr.queued(t); got != want {
		t.Fatalf("queue arguments %q, want %q", got, want)
	}
}

type encoder interface{ Encode() ([]byte, error) }

func writeEncoded(t *testing.T, root, sub, id string, rec encoder) {
	t.Helper()

	data, err := rec.Encode()
	if err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(root, sub)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, id+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}
