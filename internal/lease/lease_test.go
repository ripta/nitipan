package lease

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/adapter"
	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/liveness"
	"github.com/space-armada/nitipan/internal/proc/proctest"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/state"
)

var testMachine = record.Machine{Host: "laptop", ID: "machine-1"}

func startOf(pid int) record.ProcessStart {
	return record.ProcessStart{Platform: record.PlatformDarwin, Sec: int64(pid)}
}

type fixture struct {
	t      *testing.T
	root   string
	engine *Engine
	table  *proctest.Table
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	table := proctest.New(record.PlatformDarwin, 100)
	table.Add(100, proctest.Process{Start: startOf(100)})

	checker := &liveness.Checker{Procs: table, Local: testMachine}

	dir, err := state.Open(filepath.Join(root, "state"), state.Options{
		Checker: checker, Procs: table, Machine: testMachine, SelfPID: 100, SelfStart: startOf(100),
		LockBudget: 40 * time.Millisecond, LockPoll: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	engine := NewEngine(dir, checker, testMachine, nil)

	return &fixture{t: t, root: root, engine: engine, table: table}
}

// holder returns a holder of a multi-session agent that is judged by process, a shape no shipped
// adapter has. It exercises the match rule for a multi-session agent that is not yet known.
func (f *fixture) holder(pid int, session string) adapter.Holder {
	f.table.Add(pid, proctest.Process{Start: startOf(pid), PPID: 1})

	return adapter.Holder{
		Adapter: adapter.Adapter{
			Name:            "multi",
			MultiSession:    true,
			Liveness:        record.LivenessProcess,
			PermissionModes: []string{"default"},
		},
		PID:       pid,
		Start:     startOf(pid),
		SessionID: session,
		User:      "tester",
	}
}

func (f *fixture) acquire(h adapter.Holder, scope string) (*AcquireResult, error) {
	f.t.Helper()

	return f.engine.Acquire(AcquireRequest{Holder: h, Domain: Domain{Checkout: f.root, Scopes: []string{scope}}})
}

func TestAMultiSessionAgentNeedsASessionIDToAcquire(t *testing.T) {
	f := newFixture(t)

	_, err := f.acquire(f.holder(200, ""), "a")
	if !errors.Is(err, ErrMissingSession) {
		t.Fatalf("err = %v, want ErrMissingSession", err)
	}
}

func TestAMultiSessionAgentMatchesOnlyWhenTheSessionIDMatchesToo(t *testing.T) {
	f := newFixture(t)

	first := f.holder(200, "session-1")

	res, err := f.acquire(first, "a")
	if err != nil {
		t.Fatal(err)
	}

	again, err := f.acquire(first, "a")
	if err != nil || again.Outcome != Unchanged || again.Lease.ID != res.Lease.ID {
		t.Fatalf("same session: %v, %+v", err, again)
	}

	// Same process, another session. The lease is not theirs, and the refusal says so.
	second := f.holder(200, "session-2")

	_, err = f.acquire(second, "a")
	if !errors.Is(err, exitcode.ErrRefused) {
		t.Fatalf("err = %v, want a refusal", err)
	}

	if !strings.Contains(err.Error(), "another session in this same process") {
		t.Errorf("refusal %q does not say another session holds it", err)
	}

	_, err = f.engine.Release(ReleaseRequest{Holder: second, IDs: []string{res.Lease.ID}})
	if !errors.Is(err, exitcode.ErrNotYours) || !strings.Contains(err.Error(), "same process") {
		t.Errorf("release by the other session = %v", err)
	}
}

func TestAHandleCollisionWithADifferentLiveHolderFailsTheCall(t *testing.T) {
	f := newFixture(t)
	f.engine.HandleOf = func(adapter.Holder) string { return "same-same-same-same" }

	if _, err := f.acquire(f.holder(200, "s1"), "a"); err != nil {
		t.Fatal(err)
	}

	_, err := f.acquire(f.holder(300, "s2"), "b")
	if !errors.Is(err, ErrHandleCollision) {
		t.Fatalf("err = %v, want a handle collision", err)
	}

	if !strings.Contains(err.Error(), "same-same-same-same") {
		t.Errorf("error %q does not name the handle", err)
	}

	delete(f.table.Procs, 200)

	if _, err := f.acquire(f.holder(300, "s2"), "b"); err != nil {
		t.Errorf("a dead holder's handle blocked a claim: %v", err)
	}
}

type identityTest struct {
	Name string
	A, B adapter.Holder
	Same bool
}

func sessionHolder(pid int, session string) adapter.Holder {
	return adapter.Holder{
		Adapter:   adapter.Adapter{Name: "hosted", MultiSession: true, Liveness: record.LivenessSession},
		PID:       pid,
		Start:     startOf(pid),
		SessionID: session,
	}
}

func processHolder(pid int, session string) adapter.Holder {
	return adapter.Holder{Adapter: adapter.Claude(), PID: pid, Start: startOf(pid), SessionID: session}
}

var identityTests = []identityTest{
	{Name: "session kind survives a host restart", A: sessionHolder(10, "s"), B: sessionHolder(99, "s"), Same: true},
	{Name: "session kind separates sessions on one host", A: sessionHolder(10, "s"), B: sessionHolder(10, "t")},
	{Name: "process kind differs for a new process", A: processHolder(10, "s"), B: processHolder(11, "s")},
	{Name: "single-session process kind survives a rotated session ID", A: processHolder(10, "s"), B: processHolder(10, "t"), Same: true},
}

func TestIdentityFollowsTheMatchRule(t *testing.T) {
	for _, test := range identityTests {
		t.Run(test.Name, func(t *testing.T) {
			if got := identity(test.A) == identity(test.B); got != test.Same {
				t.Errorf("identities equal = %v, want %v", got, test.Same)
			}
		})
	}
}
