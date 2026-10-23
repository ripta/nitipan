package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/liveness"
	"github.com/space-armada/nitipan/internal/proc/proctest"
	"github.com/space-armada/nitipan/internal/state"
)

// waiter drives the fake clock of a wait. Each sleep advances the clock, records its length, and runs
// the step the test registered for that attempt.
type waiter struct {
	w      *world
	slept  []time.Duration
	onWait func(n int)
}

func (w *world) fakeWait(onWait func(n int)) *waiter {
	wt := &waiter{w: w, onWait: onWait}

	w.sleep = func(d time.Duration) {
		wt.slept = append(wt.slept, d)
		w.now = w.now.Add(d)

		if wt.onWait != nil {
			wt.onWait(len(wt.slept))
		}
	}

	return wt
}

// as runs a command as another caller from inside a wait, and restores the process the waiter runs as.
func (w *world) as(c caller, args ...string) result {
	w.t.Helper()

	self := w.table.Procs[selfPID].PPID
	res := w.run(c, args...)
	w.table.Procs[selfPID].PPID = self

	return res
}

// holdLock takes the state lock as another process and returns its release.
func (w *world) holdLock() func() {
	w.t.Helper()

	w.table.Add(4242, proctest.Process{Start: startOf(4242)})

	dir, err := state.Open(w.state, state.Options{
		Checker:   &liveness.Checker{Procs: w.table, Local: testMachine},
		Procs:     w.table,
		Machine:   testMachine,
		SelfPID:   4242,
		SelfStart: startOf(4242),
		Command:   "nitipan acquire --elsewhere",
	})
	if err != nil {
		w.t.Fatal(err)
	}

	held, err := dir.Lock()
	if err != nil {
		w.t.Fatal(err)
	}

	released := false

	w.t.Cleanup(func() {
		if !released {
			_ = held.Unlock()
		}
	})

	return func() {
		released = true
		_ = held.Unlock()
	}
}

func TestWaitIsGrantedOnceTheHolderReleases(t *testing.T) {
	w := newWorld(t)
	holder, waiting := w.claude(200, "s1"), w.claude(300, "s2")

	w.acquireOK(holder, "--resource", "cpu:heavy", "--name", "alpha")

	wt := w.fakeWait(func(n int) {
		if n == 2 {
			wantStatus(t, w.as(holder, "release", "--resource", "cpu:heavy"), exitcode.OK)
		}
	})

	res := w.run(waiting, "acquire", "--resource", "cpu:heavy", "--wait", "30s")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "acquired")

	if len(wt.slept) != 2 {
		t.Errorf("slept %d times, want 2 failed attempts before the grant", len(wt.slept))
	}

	if got := len(w.leases()); got != 1 {
		t.Errorf("%d records, want only the waiter's", got)
	}
}

func TestWaitIsGrantedOnceTheHolderDies(t *testing.T) {
	w := newWorld(t)
	holder, waiting := w.claude(200, "s1"), w.claude(300, "s2")

	w.acquireOK(holder, "--resource", "cpu:heavy")

	w.fakeWait(func(n int) {
		if n == 1 {
			w.kill(200)
		}
	})

	res := w.run(waiting, "acquire", "--resource", "cpu:heavy", "--wait", "30s")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stderr", res.Stderr, "cleared")
}

func TestWaitOnAPathClaimIsGrantedOnceTheHolderReleases(t *testing.T) {
	w := newWorld(t)
	repo := w.dir("repo")
	w.finder[repo] = true

	holder, waiting := w.claude(200, "s1"), w.claude(300, "s2")

	w.acquireOK(holder, "--checkout", repo)

	w.fakeWait(func(n int) {
		wantStatus(t, w.as(holder, "release", "--checkout", repo), exitcode.OK)
	})

	wantStatus(t, w.run(waiting, "acquire", "--checkout", repo, "--wait", "30s"), exitcode.OK)
}

func TestWaitOnAFullSlotIsGrantedOnceOneHolderReleases(t *testing.T) {
	w := newWorld(t)
	w.writeSlots(gpuSlots2)

	a, b, c := w.claude(200, "s1"), w.claude(300, "s2"), w.claude(400, "s3")

	w.acquireOK(a, "--resource", "gpu:main")
	w.acquireOK(b, "--resource", "gpu:main")

	w.fakeWait(func(int) {
		wantStatus(t, w.as(a, "release", "--resource", "gpu:main"), exitcode.OK)
	})

	wantStatus(t, w.run(c, "acquire", "--resource", "gpu:main", "--wait", "30s"), exitcode.OK)
}

func TestWaitWarnsAboutABadSlotConfigOnce(t *testing.T) {
	w := newWorld(t)
	w.writeSlots(`{"version": 1, "slots": {"gpu:main": 0}}`)

	w.acquireOK(w.claude(200, "s1"), "--resource", "gpu:main")

	wt := w.fakeWait(nil)

	res := w.run(w.claude(300, "s2"), "acquire", "--resource", "gpu:main", "--wait", "5s")
	wantStatus(t, res, exitcode.WaitTimeout)

	if len(wt.slept) < 3 {
		t.Fatalf("slept %d times, want several attempts", len(wt.slept))
	}

	if got := strings.Count(res.Stderr, "Ask the user to fix it"); got != 1 {
		t.Errorf("the warning appeared %d times, want once:\n%s", got, res.Stderr)
	}
}

func TestWaitHoldsTheLockOnlyDuringEachAttempt(t *testing.T) {
	w := newWorld(t)
	holder, other, waiting := w.claude(200, "s1"), w.claude(300, "s2"), w.claude(400, "s3")

	w.acquireOK(holder, "--resource", "cpu:heavy")

	polls := 0

	w.fakeWait(func(n int) {
		polls++

		wantStatus(t, w.as(other, "list"), exitcode.OK)
		wantStatus(t, w.as(other, "acquire", "--resource", "disk:scratch"), exitcode.OK)

		if n == 3 {
			wantStatus(t, w.as(holder, "release", "--resource", "cpu:heavy"), exitcode.OK)
		}
	})

	wantStatus(t, w.run(waiting, "acquire", "--resource", "cpu:heavy", "--wait", "30s"), exitcode.OK)

	if polls != 3 {
		t.Errorf("polled %d times, want 3", polls)
	}
}

func TestBusyLockDuringAWaitCountsAsAFailedAttempt(t *testing.T) {
	w := newWorld(t)
	release := w.holdLock()

	wt := w.fakeWait(func(n int) {
		if n == 1 {
			release()
		}
	})

	res := w.run(w.claude(200, "s1"), "acquire", "--resource", "cpu:heavy", "--wait", "30s")
	wantStatus(t, res, exitcode.OK)

	if len(wt.slept) != 1 {
		t.Errorf("slept %d times, want 1 after the busy attempt", len(wt.slept))
	}
}

func TestTimeoutNamesTheHoldersWaitedOn(t *testing.T) {
	w := newWorld(t)

	w.acquireOK(w.claude(200, "s1"), "--resource", "cpu:heavy", "--name", "alpha")

	wt := w.fakeWait(nil)

	res := w.run(w.claude(300, "s2"), "acquire", "--resource", "cpu:heavy", "--wait", "1s")
	wantStatus(t, res, exitcode.WaitTimeout)
	wantContains(t, "stderr", res.Stderr, "no lease was granted within 1s", "alpha", "pid 200")
	wantAbsent(t, "stderr", res.Stderr, "state lock")

	var total time.Duration

	for _, d := range wt.slept {
		total += d
	}

	if total != time.Second {
		t.Errorf("slept %s in all, want the wait to end exactly at the deadline", total)
	}

	if len(wt.slept) < 3 {
		t.Errorf("slept %d times, want a backoff of several attempts", len(wt.slept))
	}

	if got := len(w.leases()); got != 1 {
		t.Errorf("%d records, want only the holder's", got)
	}
}

func TestBackoffStartsSmallAndStopsAtTheCap(t *testing.T) {
	w := newWorld(t)

	w.acquireOK(w.claude(200, "s1"), "--resource", "cpu:heavy")

	wt := w.fakeWait(nil)

	wantStatus(t, w.run(w.claude(300, "s2"), "acquire", "--resource", "cpu:heavy", "--wait", "20s"), exitcode.WaitTimeout)

	if first := wt.slept[0]; first < 50*time.Millisecond || first > 100*time.Millisecond {
		t.Errorf("first sleep = %s, want within 50ms and 100ms", first)
	}

	for i, d := range wt.slept {
		if d > 2*time.Second {
			t.Errorf("sleep %d = %s, want at most the 2s cap", i, d)
		}
	}

	late := wt.slept[len(wt.slept)-2]
	if late < time.Second {
		t.Errorf("a late sleep = %s, want the backoff to have grown to at least 1s", late)
	}
}

func TestTimeoutWithTheLockBusyAtTheEndNamesTheLockHolderAndTheHolders(t *testing.T) {
	w := newWorld(t)

	w.acquireOK(w.claude(200, "s1"), "--resource", "cpu:heavy", "--name", "alpha")

	w.fakeWait(func(n int) {
		if n == 1 {
			w.holdLock()
		}
	})

	res := w.run(w.claude(300, "s2"), "acquire", "--resource", "cpu:heavy", "--wait", "1s")
	wantStatus(t, res, exitcode.WaitTimeout)
	wantContains(t, "stderr", res.Stderr, "alpha", "pid 200", "state lock", "process 4242",
		"nitipan acquire --elsewhere")
}

func TestTimeoutWithOnlyABusyLockIsNotTheLockBusyStatus(t *testing.T) {
	w := newWorld(t)
	w.holdLock()
	w.fakeWait(nil)

	res := w.run(w.claude(200, "s1"), "acquire", "--resource", "cpu:heavy", "--wait", "1s")
	wantStatus(t, res, exitcode.WaitTimeout)
	wantContains(t, "stderr", res.Stderr, "state lock", "process 4242")
	wantAbsent(t, "stderr", res.Stderr, "Waited on")
}

func TestWedgedLockCannotStretchAWaitPastItsDeadline(t *testing.T) {
	const timeout = time.Second

	w := newWorld(t)
	w.budget = 300 * time.Millisecond
	w.holdLock()

	start := w.now

	var lockWaited time.Duration

	w.lockSleep = func(d time.Duration) {
		lockWaited += d
		w.now = w.now.Add(d)
	}

	wt := w.fakeWait(nil)

	res := w.run(w.claude(200, "s1"), "acquire", "--resource", "cpu:heavy", "--wait", timeout.String())
	wantStatus(t, res, exitcode.WaitTimeout)

	if lockWaited == 0 || len(wt.slept) == 0 {
		t.Fatalf("lock waiting %s and %d sleeps, want both to have happened", lockWaited, len(wt.slept))
	}

	if elapsed := w.now.Sub(start); elapsed != timeout {
		t.Errorf("the wait took %s in all, locking %s and sleeping the rest, want exactly %s", elapsed, lockWaited, timeout)
	}
}

type nonWaitableTest struct {
	Name  string
	Setup func(w *world)
	Args  []string
	Want  int
}

var nonWaitableTests = []nonWaitableTest{
	{
		Name: "unusable record",
		Setup: func(w *world) {
			w.writeLeaseFile("junk.json", []byte("not a record"))
		},
		Args: []string{"--resource", "cpu:heavy"},
		Want: exitcode.Unusable,
	},
	{
		Name: "dead holder whose record asks to be kept",
		Setup: func(w *world) {
			w.plant(plantSpec{PID: 700, Resource: "cpu:heavy", KeepAfterDeath: true})
		},
		Args: []string{"--resource", "cpu:heavy"},
		Want: exitcode.Refused,
	},
	{
		Name: "mismatched slots",
		Args: []string{"--resource", "cpu:heavy", "--slots", "3"},
		Want: exitcode.Refused,
	},
	{
		Name: "replace on a resource claim",
		Args: []string{"--resource", "cpu:heavy", "--replace", "01JAAAAAAAAAAAAAAAAAAAAAAA"},
		Want: exitcode.Refused,
	},
	{
		Name: "a name outside the grammar",
		Args: []string{"--resource", "CPU:heavy"},
		Want: exitcode.Usage,
	},
	{
		Name: "the format marker",
		Setup: func(w *world) {
			if err := os.MkdirAll(w.state, 0o700); err != nil {
				w.t.Fatal(err)
			}

			if err := os.WriteFile(filepath.Join(w.state, "format"), []byte("7"), 0o600); err != nil {
				w.t.Fatal(err)
			}
		},
		Args: []string{"--resource", "cpu:heavy"},
		Want: exitcode.Error,
	},
}

func TestEveryOtherRefusalExitsAtOnceWithItsOwnStatus(t *testing.T) {
	for _, test := range nonWaitableTests {
		t.Run(test.Name, func(t *testing.T) {
			w := newWorld(t)
			wt := w.fakeWait(nil)

			if test.Setup != nil {
				test.Setup(w)
			}

			res := w.run(w.claude(200, "s1"), append([]string{"acquire", "--wait", "30s"}, test.Args...)...)
			wantStatus(t, res, test.Want)

			if len(wt.slept) != 0 {
				t.Errorf("slept %d times, want the refusal to end the wait at once", len(wt.slept))
			}
		})
	}
}

func TestSelfOverlapExitsAtOnce(t *testing.T) {
	w := newWorld(t)
	repo := w.dir("repo")
	w.finder[repo] = true

	c := w.claude(200, "s1")
	w.acquireOK(c, "--checkout", repo, "--scope", "sub")

	wt := w.fakeWait(nil)

	res := w.run(c, "acquire", "--checkout", repo, "--wait", "30s")
	wantStatus(t, res, exitcode.Refused)
	wantContains(t, "stderr", res.Stderr, "overlaps your own")

	if len(wt.slept) != 0 {
		t.Errorf("slept %d times, want none", len(wt.slept))
	}
}

func TestMissingSessionExitsAtOnce(t *testing.T) {
	w := newWorld(t)
	wt := w.fakeWait(nil)

	res := w.run(w.stray(901), "acquire", "--resource", "cpu:heavy", "--wait", "30s")
	wantStatus(t, res, exitcode.Error)

	if len(wt.slept) != 0 {
		t.Errorf("slept %d times, want none", len(wt.slept))
	}
}

func TestDeadUnclearableHolderOfAPathIsNotWaitedOn(t *testing.T) {
	w := newWorld(t)
	repo := w.dir("repo")
	w.finder[repo] = true

	w.plant(plantSpec{PID: 700, Paths: []string{repo}, KeepAfterDeath: true})

	wt := w.fakeWait(nil)

	wantStatus(t, w.run(w.claude(200, "s1"), "acquire", "--checkout", repo, "--wait", "30s"), exitcode.Refused)

	if len(wt.slept) != 0 {
		t.Errorf("slept %d times, want none", len(wt.slept))
	}
}

func TestRetryAfterPublicationIsAReacquire(t *testing.T) {
	w := newWorld(t)
	c := w.claude(200, "s1")

	id := w.acquireOK(c, "--resource", "cpu:heavy")
	wt := w.fakeWait(nil)

	res := w.run(c, "acquire", "--resource", "cpu:heavy", "--wait", "30s")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "already held; nothing changed")

	if ids := w.leaseIDs(); len(ids) != 1 || ids[0] != id {
		t.Errorf("records = %v, want only %s", ids, id)
	}

	if len(wt.slept) != 0 {
		t.Errorf("slept %d times, want none", len(wt.slept))
	}
}

type waitUsageTest struct {
	Name string
	Args []string
}

var waitUsageTests = []waitUsageTest{
	{Name: "no value", Args: []string{"acquire", "--resource", "cpu:heavy", "--wait"}},
	{Name: "empty value", Args: []string{"acquire", "--resource", "cpu:heavy", "--wait", ""}},
	{Name: "a number with no unit", Args: []string{"acquire", "--resource", "cpu:heavy", "--wait", "5"}},
	{Name: "not a duration", Args: []string{"acquire", "--resource", "cpu:heavy", "--wait", "soon"}},
	{Name: "zero", Args: []string{"acquire", "--resource", "cpu:heavy", "--wait", "0s"}},
	{Name: "negative", Args: []string{"acquire", "--resource", "cpu:heavy", "--wait", "-5s"}},
}

func TestWaitNeedsAPositiveDuration(t *testing.T) {
	for _, test := range waitUsageTests {
		t.Run(test.Name, func(t *testing.T) {
			w := newWorld(t)

			wantStatus(t, w.run(w.claude(200, "s1"), test.Args...), exitcode.Usage)

			if len(w.leases()) != 0 {
				t.Error("a usage error wrote a lease")
			}
		})
	}
}

func TestAcquireHelpTellsCallersToRetryAfterAKill(t *testing.T) {
	w := newWorld(t)

	res := w.run(w.claude(200, "s1"), "acquire", "--help")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "--wait", "Retry the same acquire", "run list", "no queue")
}

func TestWaitDoesNotChangeTheRefusalWithoutTheFlag(t *testing.T) {
	w := newWorld(t)

	w.acquireOK(w.claude(200, "s1"), "--resource", "cpu:heavy")

	res := w.run(w.claude(300, "s2"), "acquire", "--resource", "cpu:heavy")
	wantStatus(t, res, exitcode.Refused)

	if strings.Contains(res.Stderr, "no lease was granted") {
		t.Error("a claim without --wait reported a wait timeout")
	}
}
