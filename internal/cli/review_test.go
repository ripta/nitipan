package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/space-armada/nitipan/internal/adapter"
	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/liveness"
	"github.com/space-armada/nitipan/internal/proc/proctest"
	"github.com/space-armada/nitipan/internal/state"
)

type callerTest struct {
	Name        string
	Caller      func(w *world) caller
	WantHolder  bool
	WantParent  int
	WantProcess string
}

var callerTests = []callerTest{
	{Name: "no agent variables", Caller: func(w *world) caller { return w.terminal(900) }, WantParent: 900, WantProcess: "zsh"},
	{Name: "stray agent variables", Caller: func(w *world) caller { return w.stray(900) }, WantParent: 900, WantProcess: "zsh"},
	{Name: "an agent", Caller: func(w *world) caller { return w.claude(200, "s1") }, WantHolder: true},
}

func TestListResolvesItsCallerThroughTheSamePathAsTombstoneDelivery(t *testing.T) {
	for _, test := range callerTests {
		t.Run(test.Name, func(t *testing.T) {
			w := newWorld(t)

			var got *adapter.Caller

			deps := w.deps(test.Caller(w), &bytes.Buffer{}, &bytes.Buffer{})
			deps.OnCaller = func(c adapter.Caller) { got = &c }

			if status := Execute(deps, []string{"list"}); status != exitcode.OK {
				t.Fatalf("status = %d", status)
			}

			if got == nil {
				t.Fatal("list never resolved its caller")
			}

			if test.WantHolder {
				if got.Holder == nil || got.Holder.PID != 200 {
					t.Errorf("caller = %+v, want the claude holder", got)
				}

				return
			}

			anon := got.Anonymous

			if got.Holder != nil || anon.User != tester || anon.PID != test.WantParent ||
				!anon.Start.Equal(startOf(test.WantParent)) || anon.Name != test.WantProcess {
				t.Errorf("caller = %+v, want an anonymous process of user %s under pid %d (%s)",
					got, tester, test.WantParent, test.WantProcess)
			}
		})
	}
}

func TestListWithABusyLockWarnsStillPrintsAndExitsZero(t *testing.T) {
	w := newWorld(t)
	w.table.Add(4242, proctest.Process{Start: startOf(4242)})

	id := w.acquireOK(w.claude(200, "s1"), "--checkout", w.dir("repo"))

	dir, err := state.Open(w.state, state.Options{
		Checker: &liveness.Checker{Procs: w.table, Local: testMachine}, Procs: w.table, Machine: testMachine,
		SelfPID: 4242, SelfStart: startOf(4242), Command: "nitipan acquire",
	})
	if err != nil {
		t.Fatal(err)
	}

	held, err := dir.Lock()
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = held.Unlock() }()

	res := w.run(w.terminal(900), "list")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stderr", res.Stderr, "listing without the lock", "process 4242")
	wantContains(t, "stdout", res.Stdout, id)
}

func TestListWithASandboxBlockedLockFilePrintsTheSandboxTextAndStillLists(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file modes")
	}

	w := newWorld(t)
	c := w.claude(200, "s1")

	id := w.acquireOK(c, "--checkout", w.dir("repo"))

	lock := filepath.Join(w.state, "lock")
	if err := os.Chmod(lock, 0); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = os.Chmod(lock, 0o600) })

	res := w.run(c, "list")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stderr", res.Stderr, "listing without the state lock", "likely", "Detected agent: claude",
		`"Edit(`+w.state+`/**)"`)
	wantContains(t, "stdout", res.Stdout, id)
}

func TestReplaceThatDropsKeysSaysWhichKeysItReleased(t *testing.T) {
	w := newWorld(t)
	repo := w.dir("repo")
	c := w.claude(200, "s1")

	id := w.acquireOK(c, "--checkout", repo)

	res := w.run(c, "acquire", "--checkout", repo, "--scope", "docs", "--replace", id)
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "replaced", "released keys the new claim does not contain: "+repo)

	// A replace that drops nothing says nothing about released keys.
	res = w.run(c, "acquire", "--checkout", repo, "--replace", id)
	wantStatus(t, res, exitcode.OK)
	wantAbsent(t, "stdout", res.Stdout, "released keys")
}

func TestSelfOverlapSuggestionQuotesPathsWithSpaces(t *testing.T) {
	w := newWorld(t)
	repo := w.dir("my repo")
	nested := w.dir("my repo", "work tree")
	c := w.claude(200, "s1")

	w.acquireOK(c, "--checkout", repo)

	w.finder[nested] = true

	res := w.run(c, "acquire", "--checkout", repo, "--scope", "docs", "--scope", "work tree")
	wantStatus(t, res, exitcode.Refused)
	wantContains(t, "stderr", res.Stderr, "acquire --checkout '"+repo+"' --scope 'work tree'")
}

func TestReleaseAllWithNoLeasesSaysSo(t *testing.T) {
	w := newWorld(t)

	res := w.run(w.claude(200, "s1"), "release", "--all")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "no leases to release")
}
