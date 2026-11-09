package gitfence

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/space-armada/nitipan/internal/keys"
)

func requireGit(t *testing.T) {
	t.Helper()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.com")
}

// testRepo is a repository with src/, docs/, and a README on main.
type testRepo struct {
	t    *testing.T
	root string
	dir  string
}

func newTestRepo(t *testing.T) *testRepo {
	t.Helper()

	requireGit(t)

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	r := &testRepo{t: t, root: root, dir: filepath.Join(root, "repo")}

	if err := os.MkdirAll(r.dir, 0o755); err != nil {
		t.Fatal(err)
	}

	r.git("init", "-q", "-b", "main")
	r.write("src/a.txt", "a\n")
	r.write("docs/b.txt", "b\n")
	r.write("README", "r\n")
	r.git("add", "-A")
	r.git("commit", "-qm", "base")

	return r
}

func (r *testRepo) git(args ...string) string {
	r.t.Helper()

	return r.gitIn(r.dir, args...)
}

func (r *testRepo) gitIn(dir string, args ...string) string {
	r.t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = dir

	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}

	return strings.TrimSpace(string(out))
}

func (r *testRepo) write(rel, content string) {
	r.t.Helper()

	path := filepath.Join(r.dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		r.t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// commit changes one file and commits it on the current branch.
func (r *testRepo) commit(rel string) string {
	r.t.Helper()

	r.write(rel, rel+" changed by "+r.git("rev-parse", "HEAD")+"\n")
	r.git("add", "-A")
	r.git("commit", "-qm", "change "+rel)

	return r.git("rev-parse", "HEAD")
}

// detached makes a commit that no branch holds, and leaves the current branch checked out.
func (r *testRepo) detached(rel string) string {
	r.t.Helper()

	r.git("checkout", "-q", "--detach")
	sha := r.commit(rel)
	r.git("checkout", "-q", "main")

	return sha
}

func (r *testRepo) fence() *Fence {
	run := func(dir string, stdin []byte, args ...string) ([]byte, error) {
		if dir == "" {
			dir = r.dir
		}

		return ExecClean(dir, stdin, args...)
	}

	return &Fence{Git: run, GitIn: ExecClean, Canon: keys.NewCanonicalizer()}
}

type touchedTest struct {
	Name  string
	Setup func(r *testRepo) Update

	// Want lists the touched paths relative to the worktree named in Root, or the main one.
	Want []string
	Root string
}

var touchedTests = []touchedTest{
	{
		Name: "a commit on the checked out branch",
		Setup: func(r *testRepo) Update {
			return Update{Ref: "refs/heads/main", New: r.detached("src/a.txt")}
		},
		Want: []string{"src/a.txt"},
	},
	{
		Name: "a rewind drops the commits it leaves",
		Setup: func(r *testRepo) Update {
			r.commit("src/a.txt")

			return Update{Ref: "refs/heads/main", New: r.git("rev-parse", "HEAD~1")}
		},
		Want: []string{"src/a.txt"},
	},
	{
		Name: "a deletion touches what no other branch holds",
		Setup: func(r *testRepo) Update {
			r.git("checkout", "-q", "-b", "gone")
			r.commit("src/a.txt")
			r.git("checkout", "-q", "main")

			return Update{Ref: "refs/heads/gone"}
		},
		Want: []string{"src/a.txt"},
	},
	{
		Name: "a deletion of a branch another branch holds touches nothing",
		Setup: func(r *testRepo) Update {
			r.git("branch", "twin")

			return Update{Ref: "refs/heads/twin"}
		},
	},
	{
		Name: "a new branch at an existing commit touches nothing",
		Setup: func(r *testRepo) Update {
			return Update{Ref: "refs/heads/feature", New: r.git("rev-parse", "HEAD")}
		},
	},
	{
		Name: "a branch moved onto a commit another branch holds, checked out nowhere",
		Setup: func(r *testRepo) Update {
			r.git("checkout", "-q", "-b", "side")
			side := r.commit("src/a.txt")
			r.git("checkout", "-q", "main")
			r.git("branch", "feat", "main")

			return Update{Ref: "refs/heads/feat", New: side}
		},
	},
	{
		Name: "the checked out branch moved onto a commit another branch holds",
		Setup: func(r *testRepo) Update {
			r.git("checkout", "-q", "-b", "side")
			side := r.commit("src/a.txt")
			r.git("checkout", "-q", "main")

			return Update{Ref: "refs/heads/main", New: side}
		},
		Want: []string{"src/a.txt"},
	},
	{
		Name: "a branch checked out in a sibling worktree maps to that worktree",
		Setup: func(r *testRepo) Update {
			r.git("checkout", "-q", "-b", "side")
			side := r.commit("src/a.txt")
			r.git("checkout", "-q", "main")
			r.git("branch", "feat", "main")
			r.git("worktree", "add", "-q", filepath.Join(r.root, "wt"), "feat")

			return Update{Ref: "refs/heads/feat", New: side}
		},
		Want: []string{"src/a.txt"},
		Root: "wt",
	},
	{
		Name: "an update after which the index already matches the new tree counts no tree diff",
		Setup: func(r *testRepo) Update {
			r.git("checkout", "-q", "-b", "side")
			side := r.commit("src/a.txt")
			r.git("checkout", "-q", "main")
			r.git("read-tree", "-m", "-u", "HEAD", side)

			return Update{Ref: "refs/heads/main", New: side}
		},
	},
	{
		Name: "a commit on a detached HEAD",
		Setup: func(r *testRepo) Update {
			return Update{Ref: "HEAD", New: r.detached("docs/b.txt")}
		},
		Want: []string{"docs/b.txt"},
	},
	{
		Name: "HEAD moved onto a commit a branch holds, and the commits it leaves, count nothing",
		Setup: func(r *testRepo) Update {
			r.git("checkout", "-q", "--detach")
			r.commit("src/a.txt")

			return Update{Ref: "HEAD", New: r.git("rev-parse", "main")}
		},
	},
	{
		Name: "an upstream commit that a remote-tracking ref holds counts nothing",
		Setup: func(r *testRepo) Update {
			upstream := r.detached("src/a.txt")
			r.git("update-ref", "refs/remotes/origin/main", upstream)
			r.git("branch", "feat", "main")

			return Update{Ref: "refs/heads/feat", New: upstream}
		},
	},
	{
		Name: "the checked out branch moved onto an upstream commit counts the tree it changes",
		Setup: func(r *testRepo) Update {
			upstream := r.detached("src/a.txt")
			r.git("update-ref", "refs/remotes/origin/main", upstream)

			return Update{Ref: "refs/heads/main", New: upstream}
		},
		Want: []string{"src/a.txt"},
	},
	{
		Name: "a clean merge counts nothing",
		Setup: func(r *testRepo) Update {
			r.git("checkout", "-q", "-b", "side")
			r.commit("src/a.txt")
			r.git("checkout", "-q", "main")
			r.commit("docs/b.txt")
			r.git("checkout", "-q", "--detach")
			r.git("merge", "-q", "--no-edit", "side")

			merged := r.git("rev-parse", "HEAD")
			r.git("checkout", "-q", "main")
			r.git("branch", "feat", "main")

			return Update{Ref: "refs/heads/feat", New: merged}
		},
	},
	{
		Name: "a merge counts a path it changes from every parent",
		Setup: func(r *testRepo) Update {
			r.git("checkout", "-q", "-b", "side")
			r.write("src/a.txt", "side\n")
			r.git("commit", "-qam", "side")
			r.git("checkout", "-q", "main")
			r.write("src/a.txt", "main\n")
			r.git("commit", "-qam", "main")
			r.git("checkout", "-q", "--detach")

			cmd := exec.Command("git", "merge", "--no-commit", "side")
			cmd.Dir = r.dir
			_ = cmd.Run()

			r.write("src/a.txt", "resolved\n")
			r.git("add", "-A")
			r.git("commit", "-qm", "merge")

			merged := r.git("rev-parse", "HEAD")
			r.git("checkout", "-q", "main")
			r.git("branch", "feat", "main")

			return Update{Ref: "refs/heads/feat", New: merged}
		},
		Want: []string{"src/a.txt"},
	},
	{
		Name: "a salvage ref is not part of the baseline",
		Setup: func(r *testRepo) Update {
			pinned := r.detached("src/a.txt")
			r.git("update-ref", "refs/nitipan/salvage/x", pinned)

			return Update{Ref: "refs/heads/landed", New: pinned}
		},
		Want: []string{"src/a.txt"},
	},
	{
		Name: "a tag is not part of the baseline",
		Setup: func(r *testRepo) Update {
			tagged := r.detached("src/a.txt")
			r.git("tag", "t1", tagged)

			return Update{Ref: "refs/heads/landed", New: tagged}
		},
		Want: []string{"src/a.txt"},
	},
	{
		Name: "pruning the loose copy of a packed branch counts nothing",
		Setup: func(r *testRepo) Update {
			r.git("checkout", "-q", "-b", "unique")
			r.commit("src/a.txt")
			r.git("checkout", "-q", "main")
			r.git("pack-refs", "--all")

			return Update{Ref: "refs/heads/unique"}
		},
	},
	{
		Name: "deleting a packed branch holds the packed-refs lock and counts its commits",
		Setup: func(r *testRepo) Update {
			r.git("checkout", "-q", "-b", "unique")
			r.commit("src/a.txt")
			r.git("checkout", "-q", "main")
			r.git("pack-refs", "--all")
			r.write(".git/packed-refs.lock", "")

			return Update{Ref: "refs/heads/unique"}
		},
		Want: []string{"src/a.txt"},
	},
	{
		Name: "a ref that already holds the new value counts nothing",
		Setup: func(r *testRepo) Update {
			return Update{Ref: "refs/heads/main", New: r.git("rev-parse", "main")}
		},
	},
	{
		Name: "a branch update outside the judged refs is not touched",
		Setup: func(r *testRepo) Update {
			return Update{Ref: "refs/tags/t1", New: r.detached("src/a.txt")}
		},
	},
}

func TestTouched(t *testing.T) {
	for _, test := range touchedTests {
		t.Run(test.Name, func(t *testing.T) {
			r := newTestRepo(t)
			u := test.Setup(r)

			got, err := r.fence().Touched([]Update{u})
			if err != nil {
				t.Fatalf("Touched: %v", err)
			}

			var paths []string

			for _, touch := range got {
				for _, k := range touch.Keys {
					root := r.dir
					if test.Root != "" {
						root = filepath.Join(r.root, test.Root)
					}

					rel, err := filepath.Rel(root, k.Path)
					if err != nil {
						t.Fatal(err)
					}

					paths = append(paths, rel)
				}
			}

			if !slices.Equal(paths, test.Want) {
				t.Errorf("touched = %v, want %v", paths, test.Want)
			}
		})
	}
}

func TestTouchedFailsClosedOnAMissingObject(t *testing.T) {
	r := newTestRepo(t)
	missing := strings.Repeat("ab", 20)

	_, err := r.fence().Touched([]Update{{Ref: "refs/heads/main", New: missing}})

	var gone *MissingObjectError
	if !errors.As(err, &gone) || gone.Object != missing || !strings.Contains(err.Error(), "Fetch") {
		t.Fatalf("Touched = %v, want a missing-object error that says to fetch", err)
	}
}

type parseTest struct {
	Name    string
	In      string
	Want    []Update
	WantErr bool
}

var parseTests = []parseTest{
	{Name: "empty"},
	{
		Name: "a creation, a deletion, and a symbolic update",
		In: "0000000000000000000000000000000000000000 " + strings.Repeat("a", 40) + " refs/heads/x\n" +
			strings.Repeat("b", 40) + " 0000000000000000000000000000000000000000 refs/heads/y\n" +
			strings.Repeat("c", 40) + " ref:refs/heads/z HEAD\n",
		Want: []Update{
			{Ref: "refs/heads/x", New: strings.Repeat("a", 40)},
			{Ref: "refs/heads/y"},
			{Ref: "HEAD", Symbolic: true},
		},
	},
	{Name: "too few fields", In: "abc refs/heads/x\n", WantErr: true},
	{Name: "a new value that is no object ID", In: strings.Repeat("a", 40) + " nonsense refs/heads/x\n", WantErr: true},
}

func TestParseUpdates(t *testing.T) {
	for _, test := range parseTests {
		t.Run(test.Name, func(t *testing.T) {
			got, err := ParseUpdates([]byte(test.In))
			if (err != nil) != test.WantErr {
				t.Fatalf("ParseUpdates error = %v, want error %v", err, test.WantErr)
			}

			if !slices.Equal(got, test.Want) {
				t.Errorf("ParseUpdates = %+v, want %+v", got, test.Want)
			}
		})
	}
}

func TestJudgedRefs(t *testing.T) {
	for ref, want := range map[string]bool{
		"HEAD":                       true,
		"refs/heads/main":            true,
		"refs/heads/feature/x":       true,
		"refs/tags/v1":               false,
		"refs/remotes/origin/main":   false,
		"refs/nitipan/salvage/01ABC": false,
		"ORIG_HEAD":                  false,
		"AUTO_MERGE":                 false,
		"refs/stash":                 false,
	} {
		if got := Judged(Update{Ref: ref}); got != want {
			t.Errorf("Judged(%s) = %v, want %v", ref, got, want)
		}
	}

	if Judged(Update{Ref: "HEAD", Symbolic: true}) {
		t.Error("a symbolic update of HEAD moves no commit and must pass")
	}
}

func TestParseWorktrees(t *testing.T) {
	out := "worktree /r\x00HEAD aaa\x00branch refs/heads/main\x00\x00" +
		"worktree /wt\x00HEAD bbb\x00detached\x00\x00" +
		"worktree /bare\x00bare\x00\x00"

	got := parseWorktrees([]byte(out))
	want := []worktree{{Path: "/r", Branch: "refs/heads/main"}, {Path: "/wt"}, {Path: "/bare", Bare: true}}

	if !slices.Equal(got, want) {
		t.Errorf("parseWorktrees = %+v, want %+v", got, want)
	}
}

type remoteTouchedTest struct {
	Name string

	// Setup returns the old and new values, and the advertised branches.
	Setup func(r *testRepo) (oldSHA, newSHA string, heads map[string]string)

	Ref  string
	Want []string
}

var remoteTouchedTests = []remoteTouchedTest{
	{
		Name: "new commits on an advertised branch",
		Ref:  "refs/heads/main",
		Setup: func(r *testRepo) (string, string, map[string]string) {
			base := r.git("rev-parse", "HEAD")
			r.commit("src/a.txt")
			r.commit("docs/b.txt")

			return base, r.git("rev-parse", "HEAD"), map[string]string{"refs/heads/main": base}
		},
		Want: []string{"docs/b.txt", "src/a.txt"},
	},
	{
		Name: "a creation of a branch another advertised branch already holds touches nothing",
		Ref:  "refs/heads/copy",
		Setup: func(r *testRepo) (string, string, map[string]string) {
			head := r.git("rev-parse", "HEAD")

			return "", head, map[string]string{"refs/heads/main": head}
		},
	},
	{
		Name: "a rewind counts the commits the branch loses",
		Ref:  "refs/heads/main",
		Setup: func(r *testRepo) (string, string, map[string]string) {
			base := r.git("rev-parse", "HEAD")
			tip := r.commit("src/a.txt")

			return tip, base, map[string]string{"refs/heads/main": tip}
		},
		Want: []string{"src/a.txt"},
	},
	{
		Name: "a deletion counts what no other advertised branch holds",
		Ref:  "refs/heads/topic",
		Setup: func(r *testRepo) (string, string, map[string]string) {
			base := r.git("rev-parse", "HEAD")
			r.git("checkout", "-q", "-b", "topic")
			r.commit("docs/b.txt")

			tip := r.git("rev-parse", "HEAD")

			return tip, "", map[string]string{"refs/heads/main": base, "refs/heads/topic": tip}
		},
		Want: []string{"docs/b.txt"},
	},
	{
		Name: "a deletion of a branch another advertised branch still holds touches nothing",
		Ref:  "refs/heads/topic",
		Setup: func(r *testRepo) (string, string, map[string]string) {
			tip := r.commit("docs/b.txt")

			return tip, "", map[string]string{"refs/heads/main": tip, "refs/heads/topic": tip}
		},
	},
	{
		Name: "an advertised commit this clone lacks widens the touched set",
		Ref:  "refs/heads/main",
		Setup: func(r *testRepo) (string, string, map[string]string) {
			base := r.git("rev-parse", "HEAD")
			r.commit("src/a.txt")

			return base, r.git("rev-parse", "HEAD"), map[string]string{
				"refs/heads/main":  base,
				"refs/heads/other": strings.Repeat("ab", 20),
			}
		},
		Want: []string{"src/a.txt"},
	},
	{
		Name: "no advertised branches",
		Ref:  "refs/heads/main",
		Setup: func(r *testRepo) (string, string, map[string]string) {
			return "", r.git("rev-parse", "HEAD"), nil
		},
		Want: []string{"README", "docs/b.txt", "src/a.txt"},
	},
}

func TestRemoteTouchedAppliesTheCommitRuleAgainstTheAdvertisedBranches(t *testing.T) {
	for _, test := range remoteTouchedTests {
		t.Run(test.Name, func(t *testing.T) {
			r := newTestRepo(t)
			oldSHA, newSHA, heads := test.Setup(r)

			got, err := r.fence().RemoteTouched(test.Ref, oldSHA, newSHA, heads)
			if err != nil {
				t.Fatal(err)
			}

			if !slices.Equal(got, test.Want) {
				t.Errorf("paths = %v, want %v", got, test.Want)
			}
		})
	}
}

// A remote-tracking ref must not count as trusted on the remote side. The pusher's unpushed commits
// are held by origin/main only because a stale tracking ref says so.
func TestRemoteTouchedIgnoresRemoteTrackingRefs(t *testing.T) {
	r := newTestRepo(t)
	base := r.git("rev-parse", "HEAD")
	tip := r.commit("src/a.txt")
	r.git("update-ref", "refs/remotes/origin/main", tip)

	got, err := r.fence().RemoteTouched("refs/heads/main", base, tip, map[string]string{"refs/heads/main": base})
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(got, []string{"src/a.txt"}) {
		t.Errorf("paths = %v, want [src/a.txt]", got)
	}
}

func TestRemoteTouchedFailsClosedWhenTheOldValueIsNotHere(t *testing.T) {
	r := newTestRepo(t)
	head := r.git("rev-parse", "HEAD")
	gone := strings.Repeat("cd", 20)

	_, err := r.fence().RemoteTouched("refs/heads/main", gone, head, map[string]string{"refs/heads/main": gone})

	var missing *MissingObjectError
	if !errors.As(err, &missing) || missing.Object != gone {
		t.Fatalf("error = %v, want a missing object %s", err, gone)
	}
}
