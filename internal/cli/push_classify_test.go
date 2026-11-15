package cli

import (
	"cmp"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/fenceref"
)

// The fence ref names below use this root so the unit tests read like real porcelain output.
const classifyFence = "refs/nitipan/fence/" + rootKey + "/src"

func porcelain(lines ...string) string {
	return "To origin\n" + strings.Join(lines, "\n") + "\nDone\n"
}

type classifyTest struct {
	Name   string
	Stdout string
	Stderr string

	// Moved is nil when the push carried no fence ref.
	Moved func() (bool, error)

	// Want is the sentinel the failure must be. WantRefused takes the place of a sentinel for a fence
	// refusal, and WantReason is the host's reason it must carry.
	Want        error
	WantRefused bool
	WantReason  string
}

func movedTo(moved bool, err error) func() (bool, error) {
	return func() (bool, error) { return moved, err }
}

var classifyTests = []classifyTest{
	{
		Name: "a fence ref refused with a reason of its own",
		Stdout: porcelain(
			"!\trefs/heads/main:refs/heads/main\t[remote rejected] (atomic push failure)",
			"!\t"+"refs/f:"+classifyFence+"\t[remote rejected] (hook declined)"),
		Moved:       movedTo(false, nil),
		WantRefused: true,
		WantReason:  "hook declined",
	},
	{
		Name: "every ref refused with the same reason of its own",
		Stdout: porcelain(
			"!\trefs/heads/main:refs/heads/main\t[remote rejected] (pre-receive hook declined)",
			"!\trefs/f:"+classifyFence+"\t[remote rejected] (pre-receive hook declined)"),
		Moved:       movedTo(false, nil),
		WantRefused: true,
		WantReason:  "pre-receive hook declined",
	},
	{
		Name: "an atomic push failure on the fence ref blames another ref",
		Stdout: porcelain(
			"!\trefs/heads/main:refs/heads/main\t[remote rejected] (hook declined)",
			"!\trefs/f:"+classifyFence+"\t[remote rejected] (atomic push failure)"),
		Moved: movedTo(false, nil),
	},
	{
		Name: "an atomic transaction failure lands on every ref",
		Stdout: porcelain(
			"!\trefs/heads/main:refs/heads/main\t[remote rejected] (atomic transaction failed)",
			"!\trefs/f:"+classifyFence+"\t[remote rejected] (atomic transaction failed)"),
		Moved: movedTo(false, nil),
	},
	{
		Name: "git's own client rejects the fence ref because the branch is behind",
		Stdout: porcelain(
			"!\trefs/heads/main:refs/heads/main\t[rejected] (non-fast-forward)",
			"!\trefs/f:"+classifyFence+"\t[rejected] (atomic push failed)"),
		Moved: movedTo(false, nil),
	},
	{
		Name: "the branch is the only ref refused with a reason of its own",
		Stdout: porcelain(
			"!\trefs/heads/main:refs/heads/main\t[remote rejected] (hook declined)",
			"*\trefs/f:"+classifyFence+"\t[new reference]"),
		Moved: movedTo(false, nil),
	},
	{
		Name:   "a remote without atomic push",
		Stderr: "fatal: the receiving end does not support --atomic push\nfatal: the remote end hung up unexpectedly\n",
		Moved:  movedTo(false, nil),
		Want:   errNoAtomic,
	},
	{
		Name:   "a network failure has no ref lines",
		Stderr: "fatal: Could not read from remote repository.\n",
		Moved:  movedTo(false, nil),
	},
	{
		Name: "a moved fence ref is a lost compare-and-swap whatever the host said",
		Stdout: porcelain(
			"!\trefs/f:" + classifyFence + "\t[remote rejected] (hook declined)"),
		Moved: movedTo(true, nil),
		Want:  errLostCAS,
	},
	{
		Name: "a remote that cannot be re-read is not a fence refusal",
		Stdout: porcelain(
			"!\trefs/f:" + classifyFence + "\t[remote rejected] (hook declined)"),
		Moved: movedTo(false, errors.New("listing the refs of origin: unreachable")),
	},
	{
		Name: "a push with no fence ref does not ask the remote",
		Stdout: porcelain(
			"!\trefs/heads/main:refs/heads/main\t[rejected] (non-fast-forward)"),
	},
	{
		Name:   "a ref line with too few fields",
		Stdout: "To origin\n!\trefs/f:" + classifyFence + "\nDone\n",
		Moved:  movedTo(false, nil),
	},
	{
		Name:   "a rejected line with no reason",
		Stdout: porcelain("!\trefs/f:" + classifyFence + "\t[remote rejected]"),
		Moved:  movedTo(false, nil),
	},
	{
		Name:   "an unknown flag",
		Stdout: porcelain("?\trefs/f:" + classifyFence + "\t[remote rejected] (hook declined)"),
		Moved:  movedTo(false, nil),
	},
	{
		Name: "a garbled line spoils a refusal that parses",
		Stdout: porcelain(
			"!\trefs/f:"+classifyFence+"\t[remote rejected] (hook declined)",
			"garbled\ttext"),
		Moved: movedTo(false, nil),
	},
	{
		Name:   "prose in place of porcelain",
		Stdout: "everything went wrong, and the fence is to blame\n",
		Moved:  movedTo(false, nil),
	},
}

// This proves the order and the evidence of the four rules without a remote. The shell tests below prove
// that the strings it reads are the ones git and receive-pack print.
func TestAFailedFencedPushIsClassifiedByTheFirstRuleThatMatches(t *testing.T) {
	for _, test := range classifyTests {
		t.Run(test.Name, func(t *testing.T) {
			got := classifyPushFailure(pushFailure{Remote: "origin", Stdout: test.Stdout, Stderr: test.Stderr, Moved: test.Moved})

			var refused *fenceRefusedError

			switch {
			case test.WantRefused:
				if !errors.As(got, &refused) || refused.Reason != test.WantReason || refused.Ref != classifyFence {
					t.Fatalf("classify = %v, want a fence refusal for %s with reason %q", got, classifyFence, test.WantReason)
				}
			case test.Want != nil:
				if !errors.Is(got, test.Want) {
					t.Fatalf("classify = %v, want %v", got, test.Want)
				}
			default:
				if errors.Is(got, errLostCAS) || errors.Is(got, errNoAtomic) || errors.As(got, &refused) {
					t.Fatalf("classify = %v, want rule 4 and no fallback", got)
				}
			}
		})
	}
}

// A second push that fails must name why the remote could not be asked, so the user is not left with a
// bare status.
func TestAFailedFencedPushThatCannotRereadTheRemoteSaysSo(t *testing.T) {
	got := classifyPushFailure(pushFailure{Remote: "origin", Moved: movedTo(false, errors.New("unreachable"))})
	if got == nil || !strings.Contains(got.Error(), "could not be asked") || !strings.Contains(got.Error(), "unreachable") {
		t.Fatalf("classify = %v", got)
	}
}

type remoteFailureTest struct {
	Name string

	// Arrange sets the remote up after the agent committed.
	Arrange func(r *rig)

	// Refused is set when the push must read as a host refusal of the fence ref. With the key unset it falls
	// back to a lease check, which has no broker to renew with in this rig.
	Refused bool

	// BranchMoves is set when another writer moves the remote branch, so the comparison to the old tip is
	// moot.
	BranchMoves bool

	// WantFailure is how the push ends when the rules fall through. It is git's status unless the
	// pre-push hook refused first.
	WantFailure string

	// WantOutput is what the user must see of git's own output, on stdout or stderr.
	WantOutput []string
}

// remoteHook installs a hook in the bare remote.
func (r *rig) remoteHook(name, body string) {
	r.t.Helper()

	path := filepath.Join(r.root, "origin.git", "hooks", name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		r.t.Fatal(err)
	}
}

var remoteFailureTests = []remoteFailureTest{
	{
		Name: "an update hook that refuses the fence namespace",
		Arrange: func(r *rig) {
			r.remoteHook("update", `case "$1" in refs/nitipan/fence/*) echo "no fence here" >&2; exit 1;; esac`)
		},
		Refused:    true,
		WantOutput: []string{"[remote rejected] (hook declined)", "[remote rejected] (atomic push failure)", "no fence here"},
	},
	{
		Name: "a pre-receive hook that declines every ref",
		Arrange: func(r *rig) {
			r.remoteHook("pre-receive", `echo "declined by policy" >&2; exit 1`)
		},
		Refused:    true,
		WantOutput: []string{"[remote rejected] (pre-receive hook declined)", "declined by policy"},
	},
	{
		Name: "an update hook that refuses every ref one by one",
		Arrange: func(r *rig) {
			r.remoteHook("update", `echo "no refs at all" >&2; exit 1`)
		},
		WantOutput: []string{"[remote rejected] (hook declined)", "[remote rejected] (atomic push failure)", "no refs at all"},
	},
	{
		Name: "a branch that is not a fast-forward",
		Arrange: func(r *rig) {
			r.must(r.user(`git clone -q "$ROOT/origin.git" "$SCRATCH/other"
cd "$SCRATCH/other"
echo n > src/other.txt
git add src/other.txt
git commit -qm newer
git push -q origin main
cd "$REPO"
git fetch -q origin
`))
		},
		BranchMoves: true,
		// Git leaves a branch it rejects out of the hook's input. The hook then sees a fence ref that no
		// branch calls for, and refuses it before the remote is asked.
		WantFailure: "git did not push",
		WantOutput:  []string{"do not call for", "error: failed to push some refs"},
	},
	{
		Name: "a lock held on the fence ref",
		Arrange: func(r *rig) {
			r.must(r.user(`mkdir -p "$ROOT/origin.git/refs/nitipan/fence/` + r.rootSHA() + `"
: > "$ROOT/origin.git/` + r.fenceName("src") + `.lock"
`))
		},
		WantOutput: []string{"[remote rejected] (atomic transaction failed)", "cannot lock ref"},
	},
	{
		Name: "a fence ref that fails at commit on a directory and file conflict",
		Arrange: func(r *rig) {
			// The parent name is hidden from ls-remote, so the pusher plans a fence under it. Receive-pack
			// still sees the file, and the transaction fails at commit.
			r.must(r.user(`cd "$ROOT/origin.git"
git update-ref refs/nitipan/fence/` + r.rootSHA() + ` main
git config uploadpack.hideRefs refs/nitipan/fence/` + r.rootSHA() + `
`))
		},
		WantOutput: []string{"[remote rejected] (atomic transaction failed)", "error: cannot lock ref"},
	},
}

// Each case is a remote failing a fenced push in its own way. The branch is behind or refused in some,
// and the fence ref in others. Only a fence ref refused with a reason of its own is a host refusal.
// Everything else must fail with git's output on screen, and must record nothing.
func TestAFencedPushFailsAsTheRulesSayForEachKindOfRemote(t *testing.T) {
	for _, test := range remoteFailureTests {
		t.Run(test.Name, func(t *testing.T) {
			r := newRig(t)
			r.must(r.user(remoteSetup))

			alpha := r.agent("alpha")
			r.plantBrokerLease(alpha.claim("src"), 5, "src")
			alpha.must(t, `echo x >> src/a.txt; git commit -qam c`)

			before := strings.TrimSpace(r.must(r.user(`git --git-dir "$ROOT/origin.git" rev-parse main`)).Stdout)

			test.Arrange(r)

			res := alpha.run(`nit push origin main`)
			if res.Status == 0 {
				t.Fatalf("the push must fail\n%s%s", res.Stdout, res.Stderr)
			}

			out := res.Stdout + res.Stderr

			for _, want := range test.WantOutput {
				if !strings.Contains(out, want) {
					t.Errorf("the user must see %q of git's output:\n%s", want, out)
				}
			}

			if got := strings.Contains(res.Stderr, "refused the fence ref"); got != test.Refused {
				t.Errorf("fence refusal reported = %v, want %v\n%s", got, test.Refused, res.Stderr)
			}

			if test.Refused && !strings.Contains(res.Stderr, "not a lost lease") {
				t.Errorf("a fence refusal must say it is no lost lease:\n%s", res.Stderr)
			}

			wantFailure := cmp.Or(test.WantFailure, "git push failed with status")

			if !test.Refused && !strings.Contains(res.Stderr, wantFailure) {
				t.Errorf("rule 4 must fail with %q:\n%s", wantFailure, res.Stderr)
			}

			if fellBack := strings.Contains(res.Stderr, "lease check and not fencing"); fellBack != test.Refused {
				t.Errorf("fell back to a lease check = %v, want %v: only a host refusal of the fence falls back\n%s", fellBack, test.Refused, res.Stderr)
			}

			if got := r.fenceRefs(); strings.Contains(got, r.fenceName("src")) {
				t.Errorf("a failed push left a fence behind: %q", got)
			}

			if !test.BranchMoves {
				if got := strings.TrimSpace(r.must(r.user(`git --git-dir "$ROOT/origin.git" rev-parse main`)).Stdout); got != before {
					t.Errorf("the remote branch moved to %s from %s", got, before)
				}
			}

			for _, key := range []string{"fence", "atomic"} {
				if got := r.user(`cd "$REPO"; git config nitipan.origin.` + key); got.Status == 0 {
					t.Errorf("nitipan.origin.%s = %q, want nothing recorded", key, got.Stdout)
				}
			}
		})
	}
}

// The remote's update hook moves the repository away while it refuses the fence ref. The refusal looks
// like rule 3, but the pusher cannot ask the remote whether a fence moved, and rule 1 comes first.
func TestAFencedPushToARemoteThatVanishesFailsWithoutFallingBack(t *testing.T) {
	r := newRig(t)
	r.must(r.user(remoteSetup))

	alpha := r.agent("alpha")
	r.plantBrokerLease(alpha.claim("src"), 5, "src")
	alpha.must(t, `echo x >> src/a.txt; git commit -qam c`)

	r.remoteHook("update", `case "$1" in refs/nitipan/fence/*) cd /; mv `+filepath.Join(r.root, "origin.git")+` `+
		filepath.Join(r.root, "gone.git")+`; exit 1;; esac`)

	res := alpha.run(`nit push origin main`)
	if res.Status == 0 {
		t.Fatalf("the push must fail\n%s%s", res.Stdout, res.Stderr)
	}

	if !strings.Contains(res.Stderr, "could not be asked whether a fence ref moved") {
		t.Errorf("the failure must say rule 1 could not run:\n%s", res.Stderr)
	}

	if strings.Contains(res.Stderr, "lease check") || strings.Contains(res.Stderr, "refused the fence ref") {
		t.Errorf("a vanished remote is no host refusal:\n%s", res.Stderr)
	}
}

// An unreachable remote fails at the pusher's own ls-remote, before git push runs. The classifier is
// never reached. The table row for a failure with no ref lines owns the classifier side of this case.
func TestAPushToAnUnreachableRemoteFailsBeforeGitPushRuns(t *testing.T) {
	r := newRig(t)
	r.must(r.user(remoteSetup))

	alpha := r.agent("alpha")
	r.plantBrokerLease(alpha.claim("src"), 5, "src")
	alpha.must(t, `echo x >> src/a.txt; git commit -qam c`)
	r.must(r.user(`cd "$REPO"; git remote set-url origin "$ROOT/nowhere.git"`))

	res := alpha.run(`nit push origin main`)
	if res.Status == 0 {
		t.Fatalf("the push must fail\n%s%s", res.Stdout, res.Stderr)
	}

	if strings.Contains(res.Stderr, "lease check") || strings.Contains(res.Stderr, "refused the fence ref") {
		t.Errorf("an unreachable remote is no host refusal:\n%s", res.Stderr)
	}
}

type higherTokenTest struct {
	Name string

	// Config is the shell the user runs to set the remote's mode.
	Config string
}

var higherTokenTests = []higherTokenTest{
	{Name: "the key unset"},
	{Name: "the key true", Config: `git config nitipan.origin.fence true`},
}

// A fence ref with a higher token than the pusher's is refused before git runs, so git never sees the
// push and the remote is left as it was. This holds in the unset and true states, where the fenced
// attempt would have made it.
func TestAHigherTokenOnTheRemoteIsRefusedBeforeGitRuns(t *testing.T) {
	for _, test := range higherTokenTests {
		t.Run(test.Name, func(t *testing.T) {
			r := newRig(t)
			r.must(r.user(remoteSetup))
			r.plantFence("src", `{"version":"1.0","token":9,"path":"src","lease":"X"}`)

			if test.Config != "" {
				r.must(r.user(`cd "$REPO"; ` + test.Config))
			}

			alpha := r.agent("alpha")
			r.plantBrokerLease(alpha.claim("src"), 5, "src")
			alpha.must(t, `echo x >> src/a.txt; git commit -qam c`)

			res := alpha.run(`nit push origin main`)
			if res.Status != exitcode.Refused {
				t.Fatalf("status %d, want %d\n%s%s", res.Status, exitcode.Refused, res.Stdout, res.Stderr)
			}

			if strings.Contains(res.Stdout+res.Stderr, "[remote rejected]") || strings.Contains(res.Stdout, "To ") {
				t.Errorf("git ran:\n%s%s", res.Stdout, res.Stderr)
			}
		})
	}
}

// Git words a ref it rejects before sending anything differently from receive-pack. The client says
// "[rejected]" and "atomic push failed" on the fence ref. Receive-pack says "[remote rejected]". The
// classifier needs the second form, so the first can never read as a host refusal.
func TestGitsOwnClientWordsACollateralRejectionAsLocalNotRemote(t *testing.T) {
	r := newRig(t)
	r.must(r.user(remoteSetup))

	res := r.must(r.user(`git clone -q "$ROOT/origin.git" "$SCRATCH/a"
git clone -q "$ROOT/origin.git" "$SCRATCH/b"
cd "$SCRATCH/b"
git commit -q --allow-empty -m b
git push -q origin main
cd "$SCRATCH/a"
git commit -q --allow-empty -m a
git update-ref refs/f HEAD
git push --porcelain --atomic origin main refs/f:` + fenceref.Namespace + `x/src || true
`))

	for _, want := range []string{
		"!\trefs/heads/main:refs/heads/main\t[rejected] (fetch first)",
		"!\trefs/f:" + fenceref.Namespace + "x/src\t[rejected] (atomic push failed)",
	} {
		if !strings.Contains(res.Stdout, want) {
			t.Errorf("git's porcelain lacks %q:\n%s", want, res.Stdout)
		}
	}

	if got := classifyPushFailure(pushFailure{Remote: "origin", Stdout: res.Stdout, Moved: movedTo(false, nil)}); got != nil {
		t.Errorf("classify = %v, want rule 4", got)
	}
}
