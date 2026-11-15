package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/handoff"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/wire"
)

// twoMachines sets up one repository checked out on two machines, a claude session on each, and
// nodes for both.
type twoMachines struct {
	f            *wideFixture
	m1, m2       *wideMachine
	c1, c2       caller
	repo1, repo2 string
	rootSHA      string
}

func newTwoMachines(t *testing.T) *twoMachines {
	t.Helper()

	f := newWideFixture(t)
	tm := &twoMachines{f: f, m1: f.machine("m1"), m2: f.machine("m2")}

	tm.repo1 = tm.m1.w.repoIn("repo")
	tm.repo2 = tm.m2.w.cloneInto(tm.repo1, "clone")
	tm.rootSHA = gitIn(t, tm.repo1, "rev-list", "--max-parents=0", "HEAD")

	tm.c1 = tm.m1.w.claude(100, "s1")
	tm.c2 = tm.m2.w.claude(200, "s2")

	tm.m1.start()
	tm.m2.start()

	tm.m1.w.installHooks(tm.c1, tm.repo1)
	tm.m2.w.installHooks(tm.c2, tm.repo2)

	return tm
}

func (tm *twoMachines) claim1(extra ...string) result {
	args := append([]string{"acquire", "--reach", "user", "--checkout", tm.repo1}, extra...)

	return tm.m1.w.run(tm.c1, args...)
}

func (tm *twoMachines) claim2(reach string, extra ...string) result {
	args := append([]string{"acquire", "--reach", reach, "--checkout", tm.repo2}, extra...)

	return tm.m2.w.run(tm.c2, args...)
}

func TestUserClaimsFromCheckoutsOnTwoMachinesConflict(t *testing.T) {
	tm := newTwoMachines(t)

	res := tm.claim1("--scope", "svc")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "acquired user lease", "repo:"+tm.rootSHA+"#svc", "anchor: machine lease", "taken new")

	if len(tm.m1.w.leases()) != 1 {
		t.Fatalf("the claim takes its anchor: %v", tm.m1.w.leaseIDs())
	}

	refused := tm.claim2("user", "--scope", "svc/deeper")
	wantStatus(t, refused, exitcode.Refused)
	wantContains(t, "stderr", refused.Stderr, "overlaps", "lease "+leaseIDIn(t, res.Stdout))

	if got := tm.m2.w.leaseIDs(); len(got) != 0 {
		t.Fatalf("a refused claim releases the anchor it took: %v", got)
	}

	wantStatus(t, tm.claim2("user", "--scope", "other"), exitcode.OK)
}

func TestSharedAndUserLeasesConflictAcrossMachines(t *testing.T) {
	tm := newTwoMachines(t)

	wantStatus(t, tm.claim1("--scope", "svc"), exitcode.OK)
	wantStatus(t, tm.claim2("shared", "--scope", "svc"), exitcode.Refused)
}

type refusalCase struct {
	Name  string
	Setup func(t *testing.T, w *world) string
	Want  string

	// NoHooks skips installing the git hooks, for a directory that is no checkout.
	NoHooks bool
}

var refusalCases = []refusalCase{
	{"unborn HEAD", func(t *testing.T, w *world) string {
		dir := w.dir("empty")
		gitIn(t, dir, "init")

		return dir
	}, "no commits yet", false},
	{"not a checkout", func(t *testing.T, w *world) string { return w.dir("plain") }, "not inside a git checkout", true},
	{"shallow clone", func(t *testing.T, w *world) string {
		origin := w.repoIn("o")
		gitIn(t, origin, "commit", "--allow-empty", "-m", "second")

		dir := filepath.Join(w.root, "shallow")
		gitIn(t, w.root, "clone", "--depth", "1", "file://"+origin, dir)

		return dir
	}, "shallow", false},
}

func TestWiderClaimsOfAnUnusableRepoAreRefusedWithAdvice(t *testing.T) {
	for _, test := range refusalCases {
		t.Run(test.Name, func(t *testing.T) {
			f := newWideFixture(t)
			m := f.machine("m1")
			c := m.w.claude(100, "s1")
			dir := test.Setup(t, m.w)
			m.start()

			if !test.NoHooks {
				m.w.installHooks(c, dir)
			}

			res := m.w.run(c, "acquire", "--reach", "user", "--checkout", dir)
			wantStatus(t, res, exitcode.Refused)
			wantContains(t, "stderr", res.Stderr, test.Want)

			if len(f.leases()) != 0 || len(m.w.leases()) != 0 {
				t.Fatal("a refused derivation takes nothing")
			}
		})
	}
}

func TestClaimTimeoutDeliversTheOutcomeAsANoticeAndARetryNeverGrantsTwice(t *testing.T) {
	tm := newTwoMachines(t)
	tm.m1.stop()

	tm.m1.replyGrace = 100 * time.Millisecond
	tm.m1.w.handoffWait = 300 * time.Millisecond
	tm.m1.start()

	gate := &gateRule{path: wire.PathClaim, open: make(chan struct{})}
	tm.f.gate.Store(gate)

	res := tm.claim1("--scope", "svc")
	wantStatus(t, res, exitcode.Error)
	wantContains(t, "stderr", res.Stderr, "outcome is not known yet", "running the same command again")

	gate.release()
	tm.f.gate.Store(nil)

	waitFor(t, "the late outcome notice", func() bool {
		names, _ := os.ReadDir(filepath.Join(tm.m1.w.state, "notices"))

		return len(names) > 0
	})

	list := tm.m1.w.run(tm.c1, "list")
	wantStatus(t, list, exitcode.OK)
	wantContains(t, "stderr", list.Stderr, "granted after nitipan stopped waiting")

	counter := tm.f.store.Data().Counter

	tm.m1.w.handoffWait = 5 * time.Second

	again := tm.claim1("--scope", "svc")
	wantStatus(t, again, exitcode.OK)

	if tm.f.store.Data().Counter != counter || len(tm.f.leases()) != 1 {
		t.Fatalf("a retry of the same command must not grant twice: counter %d -> %d, leases %d", counter,
			tm.f.store.Data().Counter, len(tm.f.leases()))
	}
}

type anchorReleaseCase struct {
	Name     string
	Prepare  func(t *testing.T, tm *twoMachines)
	Claim    []string
	Release  func(tm *twoMachines, id string) []string
	Anchor   bool
	Contains string
}

var anchorReleaseCases = []anchorReleaseCase{
	{"created and untouched", nil, []string{"--scope", "svc"}, nil, false, "released anchor"},
	{"already held", func(t *testing.T, tm *twoMachines) {
		wantStatus(t, tm.m1.w.run(tm.c1, "acquire", "--checkout", tm.repo1, "--scope", "svc"), 0)
	}, []string{"--scope", "svc"}, nil, true, ""},
	{"covered by a wider machine lease", func(t *testing.T, tm *twoMachines) {
		wantStatus(t, tm.m1.w.run(tm.c1, "acquire", "--checkout", tm.repo1), 0)
	}, []string{"--scope", "svc"}, nil, true, ""},
}

func TestReleasingAUserLeaseReleasesOnlyAnAnchorItCreatedAndLeftUnchanged(t *testing.T) {
	for _, test := range anchorReleaseCases {
		t.Run(test.Name, func(t *testing.T) {
			tm := newTwoMachines(t)

			if test.Prepare != nil {
				test.Prepare(t, tm)
			}

			res := tm.claim1(test.Claim...)
			wantStatus(t, res, exitcode.OK)

			id := leaseIDIn(t, res.Stdout)
			rel := tm.m1.w.run(tm.c1, "release", id)
			wantStatus(t, rel, exitcode.OK)

			if (len(tm.m1.w.leases()) == 1) != test.Anchor {
				t.Fatalf("anchor kept = %v, want %v\n%s", len(tm.m1.w.leases()) == 1, test.Anchor, rel.Stdout)
			}

			if test.Anchor {
				wantContains(t, "stdout", rel.Stdout, "stays as an ordinary machine lease")
			}

			if len(tm.f.leases()) != 0 {
				t.Fatal("the broker lease is gone")
			}
		})
	}
}

func TestAnchorWidenedMergedOrSharedStaysAndTheSecondLeaseSurvives(t *testing.T) {
	t.Run("widened by replace", func(t *testing.T) {
		tm := newTwoMachines(t)
		res := tm.claim1("--scope", "svc")
		wantStatus(t, res, exitcode.OK)

		anchor := tm.m1.w.leaseIDs()[0]
		wantStatus(t, tm.m1.w.run(tm.c1, "acquire", "--checkout", tm.repo1, "--replace", anchor), exitcode.OK)

		rel := tm.m1.w.run(tm.c1, "release", leaseIDIn(t, res.Stdout))
		wantStatus(t, rel, exitcode.OK)
		wantContains(t, "stdout", rel.Stdout, "keys changed")

		if len(tm.m1.w.leases()) != 1 {
			t.Fatal("a widened anchor stays")
		}
	})

	t.Run("covered by a second user lease", func(t *testing.T) {
		tm := newTwoMachines(t)
		first := tm.claim1("--scope", "svc")
		wantStatus(t, first, exitcode.OK)

		second := tm.claim1("--scope", "svc/a.txt")
		wantStatus(t, second, exitcode.OK)
		wantContains(t, "stdout", second.Stdout, "reused")

		rel := tm.m1.w.run(tm.c1, "release", leaseIDIn(t, first.Stdout))
		wantStatus(t, rel, exitcode.OK)
		wantContains(t, "stdout", rel.Stdout, "stays as an ordinary machine lease")

		if len(tm.m1.w.leases()) != 1 || len(tm.f.leases()) != 1 {
			t.Fatalf("the second lease and the shared anchor survive: local %d, broker %d", len(tm.m1.w.leases()), len(tm.f.leases()))
		}
	})

	t.Run("a later covered report clears created", func(t *testing.T) {
		tm := newTwoMachines(t)
		res := tm.claim1("--scope", "svc")
		wantStatus(t, res, exitcode.OK)

		covered := tm.m1.w.run(tm.c1, "acquire", "--checkout", tm.repo1, "--scope", "svc/a.txt")
		wantStatus(t, covered, exitcode.AlreadyCovered)

		rel := tm.m1.w.run(tm.c1, "release", leaseIDIn(t, res.Stdout))
		wantContains(t, "stdout", rel.Stdout, "stays as an ordinary machine lease")
	})
}

func TestBreakForceOfAnotherMachinesLeaseNamesTheBreakerInTheHoldersNotice(t *testing.T) {
	tm := newTwoMachines(t)

	res := tm.claim1("--scope", "svc")
	wantStatus(t, res, exitcode.OK)

	id := leaseIDIn(t, res.Stdout)

	refused := tm.m2.w.run(tm.c2, "break", id)
	wantStatus(t, refused, exitcode.Refused)
	wantContains(t, "stderr", refused.Stderr, "--force")

	if len(tm.f.leases()) != 1 {
		t.Fatal("break without --force leaves the lease")
	}

	forced := tm.m2.w.run(tm.c2, "break", "--force", id)
	wantStatus(t, forced, exitcode.OK)
	wantContains(t, "stdout", forced.Stdout, "anchor stays")

	waitFor(t, "the holder's notice", func() bool {
		names, _ := os.ReadDir(filepath.Join(tm.m1.w.state, "notices"))

		return len(names) > 0
	})

	list := tm.m1.w.run(tm.c1, "list")
	wantContains(t, "stderr", list.Stderr, "was broken by", "host-m2")

	if len(tm.m1.w.leases()) != 1 || len(tm.f.leases()) != 0 {
		t.Fatalf("the anchor stays and the broker lease ends: local %d broker %d", len(tm.m1.w.leases()), len(tm.f.leases()))
	}
}

func TestListShowsBrokerLeasesWithReachAndState(t *testing.T) {
	tm := newTwoMachines(t)
	wantStatus(t, tm.claim1("--scope", "svc"), exitcode.OK)

	from2 := tm.m2.w.run(tm.c2, "list")
	wantContains(t, "stdout", from2.Stdout, "broker leases:", "user  live", "repo:", "#svc")

	doc := tm.m1.w.listJSON(tm.c1)
	leases, _ := doc["broker_leases"].([]any)

	if len(leases) != 1 {
		t.Fatalf("broker_leases = %v", doc["broker_leases"])
	}

	one, _ := leases[0].(map[string]any)
	if one["reach"] != "user" || one["state"] != "live" || one["mine"] != true {
		t.Fatalf("broker lease = %v", one)
	}

	tm.m1.stop()

	offline := tm.m1.w.run(tm.c1, "list")
	wantContains(t, "stdout", offline.Stdout, "broker leases:", "unknown")
}

func TestCommandsSayLeasesAreAtRiskWhileTheNodeIsDisconnectedAndLapsedAfter(t *testing.T) {
	tm := newTwoMachines(t)
	wantStatus(t, tm.claim1("--scope", "svc"), exitcode.OK)

	g := &gateRule{path: wire.PathRegister, open: make(chan struct{})}
	tm.f.gate.Store(g)
	tm.f.srv.CloseClientConnections()

	hk := handoff.HolderKey(record.Holder{Agent: "claude", PID: 100, Start: startOf(100), SessionID: "s1"}, record.LivenessProcess)

	waitFor(t, "a disconnected deadline file", func() bool {
		d, err := handoff.ReadDeadlines(tm.m1.w.state, hk)

		return err == nil && !d.Connected
	})

	list := tm.m1.w.run(tm.c1, "list")
	wantContains(t, "stderr", list.Stderr, "is at risk", "the node is disconnected")

	later := tm.m1.w.now.Add(6 * time.Minute)
	tm.m1.w.now = later

	past := tm.m1.w.run(tm.c1, "list")
	wantContains(t, "stderr", past.Stderr, "lapsed at")
	wantAbsent(t, "stderr", past.Stderr, "is at risk")

	g.release()
	tm.f.gate.Store(nil)
}

func TestRenewPrintsTheNewExpiry(t *testing.T) {
	tm := newTwoMachines(t)
	res := tm.claim1("--scope", "svc")
	wantStatus(t, res, exitcode.OK)

	renew := tm.m1.w.run(tm.c1, "renew")
	wantStatus(t, renew, exitcode.OK)
	wantContains(t, "stdout", renew.Stdout, "renewed "+leaseIDIn(t, res.Stdout), "expires")

	none := tm.m2.w.run(tm.c2, "renew")
	wantContains(t, "stdout", none.Stdout, "no broker leases to renew")

	gone := tm.m1.w.run(tm.c1, "renew", "01H0000000000000000000000Z")
	wantStatus(t, gone, exitcode.NoSuchLease)
}

func TestTypedNameReleasesTheCallersLease(t *testing.T) {
	tm := newTwoMachines(t)
	wantStatus(t, tm.claim1("--scope", "svc"), exitcode.OK)

	other := tm.m2.w.run(tm.c2, "release", "repo:"+tm.rootSHA+"#svc")
	wantStatus(t, other, exitcode.NoSuchLease)

	rel := tm.m1.w.run(tm.c1, "release", "repo:"+tm.rootSHA+"#svc")
	wantStatus(t, rel, exitcode.OK)
	wantContains(t, "stdout", rel.Stdout, "released")

	if len(tm.f.leases()) != 0 {
		t.Fatal("the typed name releases the lease")
	}
}

func TestSkewErrorsAreNamed(t *testing.T) {
	t.Run("node not running", func(t *testing.T) {
		w := newWorld(t)
		c := w.claude(100, "s1")
		repo := w.repoIn("repo")
		w.installHooks(c, repo)

		res := w.run(c, "acquire", "--reach", "user", "--checkout", repo)
		wantStatus(t, res, exitcode.Error)
		wantContains(t, "stderr", res.Stderr, "node not running", "launchctl bootstrap")

		if len(w.leases()) != 0 {
			t.Fatal("no anchor is taken when the node is not running")
		}
	})

	t.Run("node too old", func(t *testing.T) {
		tm := newTwoMachines(t)
		tm.m1.stop()

		old := handoff.Presence{PID: nodePID, Start: startOf(nodePID), Machine: tm.m1.w.machine, NodeVersion: "old", RequestVersion: 1,
			Kinds: []string{"ping"}}
		if err := handoff.WritePresence(tm.m1.w.state, old); err != nil {
			t.Fatal(err)
		}

		res := tm.claim1("--scope", "svc")
		wantStatus(t, res, exitcode.Error)
		wantContains(t, "stderr", res.Stderr, "node too old")

		if len(tm.m1.w.leases()) != 0 {
			t.Fatal("no anchor is taken from a node that cannot serve the claim")
		}
	})

	t.Run("node too old for discovery", func(t *testing.T) {
		tm := newTwoMachines(t)
		tm.m1.stop()

		old := handoff.Presence{PID: nodePID, Start: startOf(nodePID), Machine: tm.m1.w.machine, NodeVersion: "old", RequestVersion: 2,
			Kinds: []string{"ping", handoff.KindClaim, handoff.KindList}}
		if err := handoff.WritePresence(tm.m1.w.state, old); err != nil {
			t.Fatal(err)
		}

		for _, sub := range [][]string{{"who", "holds", tm.name("svc")}, {"who", "is", "guardian"}} {
			res := tm.m1.w.run(tm.c1, sub...)
			wantStatus(t, res, exitcode.Error)
			wantContains(t, "stderr", res.Stderr, "node too old")
		}
	})

	t.Run("broker too old for discovery", func(t *testing.T) {
		tm := newTwoMachines(t)

		for _, p := range []string{wire.PathWhoHolds, wire.PathWhoIs} {
			path := p
			tm.f.missing.Store(&path)

			args := []string{"who", "holds", tm.name("svc")}
			if p == wire.PathWhoIs {
				args = []string{"who", "is", "guardian"}
			}

			res := tm.m1.w.run(tm.c1, args...)
			wantStatus(t, res, exitcode.Error)
			wantContains(t, "stderr", res.Stderr, "broker too old")
		}
	})

	t.Run("broker too old", func(t *testing.T) {
		tm := newTwoMachines(t)

		path := wire.PathClaim
		tm.f.missing.Store(&path)

		res := tm.claim1("--scope", "svc")
		wantStatus(t, res, exitcode.Error)
		wantContains(t, "stderr", res.Stderr, "broker too old")

		if got := tm.m1.w.leases(); len(got) != 0 {
			t.Fatalf("a definite failure releases the anchor the claim took: %v", tm.m1.w.leaseIDs())
		}
	})
}

func TestUsageErrorsOfWiderReach(t *testing.T) {
	tm := newTwoMachines(t)

	for _, args := range [][]string{
		{"acquire", "--reach", "galaxy"},
		{"acquire", "--reach", "user", "--resource", "cpu:heavy"},
		{"acquire", "--reach", "user", "--wait", "5s"},
	} {
		wantStatus(t, tm.m1.w.run(tm.c1, args...), exitcode.Usage)
	}
}

func TestRepeatClaimReturnsTheSameLeaseAndNoLossNotice(t *testing.T) {
	tm := newTwoMachines(t)

	first := tm.claim1("--scope", "svc")
	wantStatus(t, first, exitcode.OK)

	second := tm.claim1("--scope", "svc")
	wantStatus(t, second, exitcode.OK)

	if leaseIDIn(t, first.Stdout) != leaseIDIn(t, second.Stdout) {
		t.Fatalf("a repeat returns the held lease:\n%s\n%s", first.Stdout, second.Stdout)
	}

	scs, _, _ := handoff.Sidecars(tm.m1.w.state)
	if len(scs) != 1 {
		t.Fatalf("sidecars = %d, want 1", len(scs))
	}

	time.Sleep(300 * time.Millisecond)

	list := tm.m1.w.run(tm.c1, "list")
	wantAbsent(t, "stderr", list.Stderr, "no record")
}

func TestLateRefusalReleasesTheNewAnchorOnTheNextCall(t *testing.T) {
	tm := newTwoMachines(t)
	tm.m2.stop()

	tm.m2.replyGrace = 100 * time.Millisecond
	tm.m2.w.handoffWait = 300 * time.Millisecond
	tm.m2.start()

	wantStatus(t, tm.claim1("--scope", "svc"), exitcode.OK)

	gate := &gateRule{path: wire.PathClaim, open: make(chan struct{})}
	tm.f.gate.Store(gate)

	res := tm.claim2("user", "--scope", "svc")
	wantStatus(t, res, exitcode.Error)

	if len(tm.m2.w.leases()) != 1 {
		t.Fatal("an unknown outcome keeps the anchor")
	}

	gate.release()
	tm.f.gate.Store(nil)

	waitFor(t, "the late refusal notice", func() bool {
		names, _ := os.ReadDir(filepath.Join(tm.m2.w.state, "notices"))

		return len(names) > 0
	})

	list := tm.m2.w.run(tm.c2, "list")
	wantStatus(t, list, exitcode.OK)
	wantContains(t, "stderr", list.Stderr, "was refused after nitipan stopped waiting")
	wantContains(t, "stdout", list.Stdout, "released anchor")

	if got := tm.m2.w.leases(); len(got) != 0 {
		t.Fatalf("the call that delivered the late refusal releases the anchor: %v", tm.m2.w.leaseIDs())
	}
}

func TestRetriedReleaseAfterALostReplyStillReleasesACreatedAnchor(t *testing.T) {
	tm := newTwoMachines(t)
	tm.m1.w.handoffWait = 300 * time.Millisecond

	res := tm.claim1("--scope", "svc")
	wantStatus(t, res, exitcode.OK)

	id := leaseIDIn(t, res.Stdout)

	gate := &gateRule{path: wire.PathRelease, open: make(chan struct{})}
	tm.f.gate.Store(gate)

	lost := tm.m1.w.run(tm.c1, "release", id)
	wantStatus(t, lost, exitcode.Error)

	gate.release()
	tm.f.gate.Store(nil)

	waitFor(t, "the node to finish the release", func() bool {
		_, err := handoff.ReadSidecar(tm.m1.w.state, id)

		return err != nil
	})

	tm.m1.w.handoffWait = 5 * time.Second

	again := tm.m1.w.run(tm.c1, "release", id)
	wantStatus(t, again, exitcode.OK)
	wantContains(t, "stdout", again.Stdout, "released anchor")

	if len(tm.m1.w.leases()) != 0 || len(tm.f.leases()) != 0 {
		t.Fatalf("local %v broker %d", tm.m1.w.leaseIDs(), len(tm.f.leases()))
	}
}
