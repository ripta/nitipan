package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/handoff"
)

// pushWorld is two machines that share a bare remote. The git hooks are installed only while a claim
// needs them. The hooks bake a nitipan path that does not exist here, and a push that ran them would
// fail closed.
type pushWorld struct {
	*twoMachines
	t    *testing.T
	bare string
}

func newPushWorld(t *testing.T, bareConfig ...string) *pushWorld {
	t.Helper()

	tm := newTwoMachines(t)
	pw := &pushWorld{twoMachines: tm, t: t, bare: filepath.Join(t.TempDir(), "origin.git")}

	gitIn(t, filepath.Dir(pw.bare), "init", "--bare", "-b", "main", pw.bare)

	for i := 0; i+1 < len(bareConfig); i += 2 {
		gitIn(t, pw.bare, "config", bareConfig[i], bareConfig[i+1])
	}

	pw.uninstall(1)
	pw.uninstall(2)

	gitIn(t, tm.repo1, "remote", "add", "origin", pw.bare)
	gitIn(t, tm.repo1, "push", "origin", "main")
	gitIn(t, tm.repo2, "remote", "set-url", "origin", pw.bare)
	gitIn(t, tm.repo2, "fetch", "origin")
	gitIn(t, tm.repo2, "branch", "--set-upstream-to=origin/main", "main")

	tm.m1.w.cwd, tm.m2.w.cwd = tm.repo1, tm.repo2

	return pw
}

func (pw *pushWorld) uninstall(machine int) {
	pw.t.Helper()

	if machine == 1 {
		wantStatus(pw.t, pw.m1.w.run(pw.c1, "setup", "git", "--uninstall", pw.repo1), 0)

		return
	}

	wantStatus(pw.t, pw.m2.w.run(pw.c2, "setup", "git", "--uninstall", pw.repo2), 0)
}

func (pw *pushWorld) install(machine int) {
	pw.t.Helper()

	if machine == 1 {
		pw.m1.w.installHooks(pw.c1, pw.repo1)

		return
	}

	pw.m2.w.installHooks(pw.c2, pw.repo2)
}

// commitIn changes a file in the repo and commits it.
func (pw *pushWorld) commitIn(repo, file string) {
	pw.t.Helper()

	writeFile(pw.t, filepath.Join(repo, file), file+" "+time.Now().String())
	gitIn(pw.t, repo, "add", "-A")
	gitIn(pw.t, repo, "commit", "-m", "change "+file)
}

func (pw *pushWorld) remoteMain() string { return gitIn(pw.t, pw.bare, "rev-parse", "main") }

func (pw *pushWorld) remoteFences() string {
	return gitIn(pw.t, pw.bare, "for-each-ref", "--format=%(refname)", "refs/nitipan/fence/")
}

func (pw *pushWorld) fenceToken(path string) string {
	pw.t.Helper()

	obj := gitIn(pw.t, pw.bare, "cat-file", "blob", "refs/nitipan/fence/"+pw.rootSHA+"/"+path+":fence.json")

	return obj
}

func TestAHolderThatWasPartitionedAndLostItsLeaseHasItsStalePushRefused(t *testing.T) {
	pw := newPushWorld(t)

	pw.install(1)
	wantStatus(t, pw.claim1("--scope", "svc"), exitcode.OK)
	pw.uninstall(1)

	pw.commitIn(pw.repo1, "svc/a.txt")

	first := pw.m1.w.run(pw.c1, "push", "origin", "main")
	wantStatus(t, first, exitcode.OK)

	if got := pw.remoteFences(); got != "refs/nitipan/fence/"+pw.rootSHA+"/svc" {
		t.Fatalf("fence refs = %q", got)
	}

	oldToken := pw.fenceToken("svc")

	pw.m1.stop()
	pw.f.clock.Advance(6 * time.Minute)

	pw.install(2)
	wantStatus(t, pw.claim2("user", "--scope", "svc"), exitcode.OK)
	pw.uninstall(2)

	gitIn(t, pw.repo2, "pull", "--ff-only")
	pw.commitIn(pw.repo2, "svc/b.txt")
	wantStatus(t, pw.m2.w.run(pw.c2, "push", "origin", "main"), exitcode.OK)

	landed := pw.remoteMain()
	newToken := pw.fenceToken("svc")

	if newToken == oldToken {
		t.Fatalf("the new holder's push did not raise the fence: %s", newToken)
	}

	gitIn(t, pw.repo1, "fetch", "origin")
	pw.commitIn(pw.repo1, "svc/c.txt")

	stale := pw.m1.w.run(pw.c1, "push", "origin", "main")
	wantStatus(t, stale, exitcode.Refused)
	wantContains(t, "stderr", stale.Stderr, "was granted to another session", "holds the fence token", "Stop protected actions in this domain and report to the user")
	wantAbsent(t, "stderr", stale.Stderr, "fence delete", "--no-verify")

	if got := pw.remoteMain(); got != landed {
		t.Errorf("the stale push moved main to %s from %s", got, landed)
	}
}

func TestAHostWithoutAtomicPushGetsALeaseCheckThatRenewsThenRefusesALostLease(t *testing.T) {
	pw := newPushWorld(t, "receive.advertiseAtomic", "false")

	pw.install(1)
	wantStatus(t, pw.claim1("--scope", "svc"), exitcode.OK)
	pw.uninstall(1)

	pw.commitIn(pw.repo1, "svc/a.txt")

	res := pw.m1.w.run(pw.c1, "push", "origin", "main")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stderr", res.Stderr, "origin does not support atomic push", "lease check and not fencing")

	if got := pw.remoteFences(); got != "" {
		t.Errorf("a fence landed on a host without atomic push: %q", got)
	}

	if got, want := pw.remoteMain(), gitIn(t, pw.repo1, "rev-parse", "main"); got != want {
		t.Errorf("remote main = %s, want %s", got, want)
	}

	held := readHeld(t, pw.m1.w.state)

	pw.m1.stop()
	pw.f.clock.Advance(6 * time.Minute)

	pw.install(2)
	wantStatus(t, pw.claim2("user", "--scope", "svc"), exitcode.OK)
	pw.uninstall(2)

	pw.m1.start()

	// The restarted node drops the sidecar of a lease the broker no longer has. A session that still
	// holds the file, as one mid-push does, must be refused by the lease check. The renewal makes the
	// node learn the lease is gone, and the file is put back once it has dropped it.
	wantStatus(t, pw.m1.w.run(pw.c1, "renew"), exitcode.NoSuchLease)

	waitFor(t, "the node to drop the sidecar", func() bool { return len(readHeld(t, pw.m1.w.state)) == 0 })
	restoreHeld(t, pw.m1.w.state, held)

	pw.commitIn(pw.repo1, "svc/b.txt")

	lost := pw.m1.w.run(pw.c1, "push", "origin", "main")
	wantStatus(t, lost, exitcode.Refused)
	wantContains(t, "stderr", lost.Stderr, "lease check, which is not fencing", "the broker has no record of lease")
}

func TestThePushOfAnAgentWithNoLeaseNeedsTheBrokerOnlyWhereAFenceSitsAbove(t *testing.T) {
	pw := newPushWorld(t)

	pw.install(1)
	wantStatus(t, pw.claim1("--scope", "svc"), exitcode.OK)
	pw.uninstall(1)

	pw.commitIn(pw.repo1, "svc/a.txt")
	wantStatus(t, pw.m1.w.run(pw.c1, "push", "origin", "main"), exitcode.OK)

	gitIn(t, pw.repo2, "pull", "--ff-only")

	pw.commitIn(pw.repo2, "other/d.txt")
	wantStatus(t, pw.m2.w.run(pw.c2, "push", "origin", "main"), exitcode.OK)

	pw.commitIn(pw.repo2, "svc/e.txt")

	held := pw.m2.w.run(pw.c2, "push", "origin", "main")
	wantStatus(t, held, exitcode.Refused)
	wantContains(t, "stderr", held.Stderr, "svc/e.txt is covered by lease", "which is live")

	pw.m2.stop()

	unverified := pw.m2.w.run(pw.c2, "push", "origin", "main")
	wantStatus(t, unverified, exitcode.CouldNotVerify)
	wantContains(t, "stderr", unverified.Stderr, "not a lost lease")

	if !strings.Contains(unverified.Stderr, "could not verify") {
		t.Errorf("stderr lacks the could-not-verify label:\n%s", unverified.Stderr)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readHeld(t *testing.T, stateDir string) map[string][]byte {
	t.Helper()

	dir := filepath.Join(stateDir, handoff.HeldDir)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	out := map[string][]byte{}

	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}

		out[e.Name()] = data
	}

	return out
}

func restoreHeld(t *testing.T, stateDir string, held map[string][]byte) {
	t.Helper()

	dir := filepath.Join(stateDir, handoff.HeldDir)

	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	for name, data := range held {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
