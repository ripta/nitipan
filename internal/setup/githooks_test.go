package setup

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/space-armada/nitipan/internal/gitfence"
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
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "protocol.file.allow")
	t.Setenv("GIT_CONFIG_VALUE_0", "always")
}

// git runs git in dir and returns its combined output.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = dir

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}

	return strings.TrimSpace(string(out))
}

// gitFails runs git and returns its combined output and whether it failed.
func gitFails(dir string, args ...string) (string, bool) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir

	out, err := cmd.CombinedOutput()

	return string(out), err != nil
}

func writeScript(t *testing.T, path, body string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// newRepo makes a repository with one commit.
func newRepo(t *testing.T) string {
	t.Helper()

	requireGit(t)

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}

	git(t, repo, "init", "-q", "-b", "main")
	writeScript(t, filepath.Join(repo, "a.txt"), "one\n")
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-qm", "one")

	return repo
}

// fakeNitipan writes a nitipan that records the arguments it ran with and exits with status.
func fakeNitipan(t *testing.T, status string) (path, log string) {
	t.Helper()

	dir := t.TempDir()
	log = filepath.Join(dir, "calls")
	path = filepath.Join(dir, "nitipan")

	writeScript(t, path, "#!/bin/sh\necho \"$@\" >> '"+log+"'\ncat > /dev/null\nexit "+status+"\n")

	return path, log
}

func install(t *testing.T, repo, exe string) []Installed {
	t.Helper()

	done, err := InstallGit(gitfence.ExecClean, repo, exe)
	if err != nil {
		t.Fatalf("InstallGit: %v", err)
	}

	return done
}

func read(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

func TestInstallAndUninstallRoundTripAnExistingHook(t *testing.T) {
	repo := newRepo(t)
	exe, log := fakeNitipan(t, "0")
	hook := filepath.Join(repo, ".git", "hooks", refTransaction)
	chained := filepath.Join(repo, "chained-calls")

	original := "#!/bin/sh\necho \"$1\" >> '" + chained + "'\ncat >> '" + chained + "'\n"
	writeScript(t, hook, original)

	done := install(t, repo, exe)
	if len(done) != 2 || done[0].Action != "wrote" || !strings.Contains(done[0].Note, "kept the existing hook") || done[1].Action != "wrote" {
		t.Fatalf("installed = %+v", done)
	}

	if st := status(t, repo); !st.InForce || st.Checkout != repo {
		t.Fatalf("status = %+v", st)
	}

	writeScript(t, filepath.Join(repo, "b.txt"), "two\n")
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-qm", "two")

	if !strings.Contains(read(t, log), "hook reference-transaction prepared") {
		t.Errorf("nitipan was not asked about the commit: %q", read(t, log))
	}

	if got := read(t, chained); !strings.Contains(got, "prepared") || !strings.Contains(got, "refs/heads/main") {
		t.Errorf("the kept hook did not see the transaction: %q", got)
	}

	if again := install(t, repo, exe); again[0].Action != "unchanged" {
		t.Errorf("a second install = %+v, want unchanged", again)
	}

	removed, err := UninstallGit(gitfence.ExecClean, repo)
	if err != nil {
		t.Fatalf("UninstallGit: %v", err)
	}

	if len(removed) != 3 || removed[1].Action != "restored" || removed[2].Action != "removed" {
		t.Errorf("uninstalled = %+v", removed)
	}

	if got := read(t, hook); got != original {
		t.Errorf("the original hook came back as %q, want %q", got, original)
	}

	if st := status(t, repo); st.InForce {
		t.Errorf("the hook is still in force after uninstall: %+v", st)
	}
}

func TestOnlyBranchAndHeadUpdatesReachNitipan(t *testing.T) {
	repo := newRepo(t)
	exe, log := fakeNitipan(t, "0")
	install(t, repo, exe)

	git(t, repo, "tag", "t1")
	git(t, repo, "update-ref", "refs/nitipan/salvage/x", "HEAD")
	git(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")

	if _, err := os.Stat(log); err == nil {
		t.Fatalf("nitipan ran for a tag, a salvage pin, and a remote-tracking ref: %q", read(t, log))
	}

	git(t, repo, "branch", "feature")

	if !strings.Contains(read(t, log), "hook reference-transaction prepared") {
		t.Errorf("nitipan did not run for a new branch: %q", read(t, log))
	}
}

func TestARefusalFromNitipanAbortsTheUpdate(t *testing.T) {
	repo := newRepo(t)
	exe, _ := fakeNitipan(t, "3")
	install(t, repo, exe)

	before := git(t, repo, "rev-parse", "HEAD")

	writeScript(t, filepath.Join(repo, "b.txt"), "two\n")
	git(t, repo, "add", "-A")

	if out, failed := gitFails(repo, "commit", "-qm", "two"); !failed {
		t.Fatalf("the commit went through: %s", out)
	}

	if after := git(t, repo, "rev-parse", "HEAD"); after != before {
		t.Errorf("HEAD moved to %s", after)
	}

	if out, failed := gitFails(repo, "tag", "t1"); failed {
		t.Errorf("a tag was refused: %s", out)
	}
}

func TestAMissingBinaryFailsClosedForBranchesAndHeadOnly(t *testing.T) {
	repo := newRepo(t)
	install(t, repo, "/nonexistent/nitipan")

	t.Setenv("PATH", "/usr/bin:/bin")

	writeScript(t, filepath.Join(repo, "b.txt"), "two\n")
	git(t, repo, "add", "-A")

	out, failed := gitFails(repo, "commit", "-qm", "two")
	if !failed {
		t.Fatal("a commit went through with no nitipan to check it")
	}

	for _, want := range []string{"nitipan binary", "missing", "Reinstall nitipan", "setup git --uninstall"} {
		if !strings.Contains(out, want) {
			t.Errorf("message %q does not contain %q", out, want)
		}
	}

	if strings.Contains(out, "hooksPath") || strings.Contains(out, "/dev/null") {
		t.Errorf("message %q names a way around the hook", out)
	}

	for _, args := range [][]string{
		{"tag", "t1"},
		{"update-ref", "refs/nitipan/salvage/x", "HEAD"},
		{"update-ref", "refs/remotes/origin/main", "HEAD"},
		{"notes", "add", "-m", "n"},
	} {
		if out, failed := gitFails(repo, args...); failed {
			t.Errorf("git %s was refused with no nitipan: %s", strings.Join(args, " "), out)
		}
	}

	if _, failed := gitFails(repo, "branch", "feature"); !failed {
		t.Error("a branch update went through with no nitipan to check it")
	}
}

func TestABinaryWithoutTheSubcommandIsReportedAsTooOld(t *testing.T) {
	repo := newRepo(t)
	exe, _ := fakeNitipan(t, "2")
	install(t, repo, exe)

	out, failed := gitFails(repo, "branch", "feature")
	if !failed {
		t.Fatal("a branch update went through")
	}

	for _, want := range []string{"too old", "Upgrade nitipan", "setup git --uninstall"} {
		if !strings.Contains(out, want) {
			t.Errorf("message %q does not contain %q", out, want)
		}
	}
}

func TestTheBakedPathFallsBackToPath(t *testing.T) {
	repo := newRepo(t)
	exe, log := fakeNitipan(t, "0")
	install(t, repo, "/nonexistent/nitipan")

	t.Setenv("PATH", filepath.Dir(exe)+":/usr/bin:/bin")

	git(t, repo, "branch", "feature")

	if !strings.Contains(read(t, log), "hook reference-transaction prepared") {
		t.Errorf("the nitipan on PATH was not used: %q", read(t, log))
	}
}

func TestATrackedHooksDirectoryIsNeverWritten(t *testing.T) {
	repo := newRepo(t)
	exe, log := fakeNitipan(t, "0")
	husky := filepath.Join(repo, ".husky")
	ran := filepath.Join(repo, "husky-ran")

	writeScript(t, filepath.Join(husky, "pre-commit"), "#!/bin/sh\necho pre-commit >> '"+ran+"'\n")
	writeScript(t, filepath.Join(husky, refTransaction), "#!/bin/sh\necho \"husky $1\" >> '"+ran+"'\ncat > /dev/null\n")
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-qm", "husky")
	git(t, repo, "config", "--local", "core.hooksPath", ".husky")

	before := git(t, repo, "status", "--porcelain")

	done := install(t, repo, exe)
	if len(done) < 20 {
		t.Errorf("only %d hooks were chained", len(done))
	}

	if after := git(t, repo, "status", "--porcelain"); after != before {
		t.Errorf("git sees a change: %q, was %q", after, before)
	}

	own := filepath.Join(repo, ".git", trackedDirName)
	if got := git(t, repo, "config", "--local", "core.hooksPath"); got != own {
		t.Errorf("core.hooksPath = %q, want %q", got, own)
	}

	if st := status(t, repo); !st.InForce {
		t.Fatalf("status = %+v", st)
	}

	writeScript(t, filepath.Join(repo, "b.txt"), "two\n")
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-qm", "two")

	if got := read(t, ran); !strings.Contains(got, "pre-commit") || !strings.Contains(got, "husky prepared") {
		t.Errorf("the tracked hooks did not run through the chain: %q", got)
	}

	if !strings.Contains(read(t, log), "hook reference-transaction prepared") {
		t.Errorf("nitipan was not asked: %q", read(t, log))
	}

	if _, err := UninstallGit(gitfence.ExecClean, repo); err != nil {
		t.Fatalf("UninstallGit: %v", err)
	}

	if got := git(t, repo, "config", "--local", "core.hooksPath"); got != ".husky" {
		t.Errorf("core.hooksPath = %q after uninstall, want .husky", got)
	}

	if _, err := os.Stat(own); !os.IsNotExist(err) {
		t.Errorf("%s is still there: %v", own, err)
	}
}

func TestUninstallUnsetsAHooksPathItAdded(t *testing.T) {
	repo := newRepo(t)
	exe, _ := fakeNitipan(t, "0")
	tracked := filepath.Join(repo, ".githooks")

	writeScript(t, filepath.Join(tracked, "pre-commit"), "#!/bin/sh\n")
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-qm", "hooks")

	global := filepath.Join(filepath.Dir(repo), "gitconfig")
	if err := os.WriteFile(global, []byte("[core]\n\thooksPath = "+tracked+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GIT_CONFIG_GLOBAL", global)

	install(t, repo, exe)

	own := filepath.Join(repo, ".git", trackedDirName)
	if got := git(t, repo, "config", "--local", "core.hooksPath"); got != own {
		t.Fatalf("core.hooksPath = %q, want %q", got, own)
	}

	if _, err := UninstallGit(gitfence.ExecClean, repo); err != nil {
		t.Fatal(err)
	}

	if out, failed := gitFails(repo, "config", "--local", "core.hooksPath"); !failed {
		t.Errorf("the local core.hooksPath is still set: %q", out)
	}
}

func TestOneInstallCoversEveryLinkedWorktree(t *testing.T) {
	repo := newRepo(t)
	exe, log := fakeNitipan(t, "0")
	seen := filepath.Join(filepath.Dir(repo), "seen")
	hook := filepath.Join(repo, ".git", "hooks", refTransaction)

	writeScript(t, hook, "#!/bin/sh\necho \"$1 cwd=$(pwd -P) gitdir=$GIT_DIR\" >> '"+seen+"'\ncat > /dev/null\n")

	install(t, repo, exe)

	wt := filepath.Join(filepath.Dir(repo), "wt")
	git(t, repo, "worktree", "add", "-q", "-b", "wtbranch", wt)

	if st := status(t, wt); !st.InForce || st.Checkout != wt {
		t.Fatalf("status of the worktree = %+v", st)
	}

	writeScript(t, filepath.Join(wt, "w.txt"), "w\n")
	git(t, wt, "add", "-A")
	git(t, wt, "commit", "-qm", "w")

	got := read(t, seen)
	if !strings.Contains(got, "prepared cwd="+wt+" gitdir="+filepath.Join(repo, ".git", "worktrees", "wt")) {
		t.Errorf("the hook ran with %q", got)
	}

	if !strings.Contains(read(t, log), "prepared") {
		t.Errorf("nitipan was not asked from the worktree: %q", read(t, log))
	}
}

func TestSubmodulesGetTheirOwnHook(t *testing.T) {
	repo := newRepo(t)
	exe, _ := fakeNitipan(t, "0")

	lib := filepath.Join(filepath.Dir(repo), "lib")
	if err := os.MkdirAll(lib, 0o755); err != nil {
		t.Fatal(err)
	}

	git(t, lib, "init", "-q", "-b", "main")
	writeScript(t, filepath.Join(lib, "l.txt"), "l\n")
	git(t, lib, "add", "-A")
	git(t, lib, "commit", "-qm", "l")
	git(t, repo, "submodule", "add", "-q", lib, "vendor/lib")
	git(t, repo, "commit", "-qm", "sub")

	done := install(t, repo, exe)
	if len(done) != 4 {
		t.Fatalf("installed = %+v, want both hooks in the checkout and its submodule", done)
	}

	sub := filepath.Join(repo, "vendor", "lib")
	if st := status(t, sub); !st.InForce || st.Checkout != sub {
		t.Errorf("status of the submodule = %+v", st)
	}

	if _, err := UninstallGit(gitfence.ExecClean, repo); err != nil {
		t.Fatal(err)
	}

	if st := status(t, sub); st.InForce {
		t.Errorf("the submodule still has the hook: %+v", st)
	}
}

type statusTest struct {
	Name    string
	Setup   func(t *testing.T, repo string)
	Outside bool
	InForce bool
	Why     string
}

var statusTests = []statusTest{
	{Name: "no hook", Why: "there is no reference-transaction hook"},
	{
		Name: "someone else's hook",
		Setup: func(t *testing.T, repo string) {
			writeScript(t, filepath.Join(repo, ".git", "hooks", refTransaction), "#!/bin/sh\n")
		},
		Why: "is not nitipan's",
	},
	{
		Name: "not executable",
		Setup: func(t *testing.T, repo string) {
			exe, _ := fakeNitipan(t, "0")
			install(t, repo, exe)

			if err := os.Chmod(filepath.Join(repo, ".git", "hooks", refTransaction), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		Why: "not executable",
	},
	{
		Name: "installed",
		Setup: func(t *testing.T, repo string) {
			exe, _ := fakeNitipan(t, "0")
			install(t, repo, exe)
		},
		InForce: true,
	},
	{
		Name: "a scope that does not exist yet",
		Setup: func(t *testing.T, repo string) {
			exe, _ := fakeNitipan(t, "0")
			install(t, repo, exe)
		},
		InForce: true,
	},
}

func TestHooksInForce(t *testing.T) {
	for _, test := range statusTests {
		t.Run(test.Name, func(t *testing.T) {
			repo := newRepo(t)

			if test.Setup != nil {
				test.Setup(t, repo)
			}

			path := repo
			if test.Name == "a scope that does not exist yet" {
				path = filepath.Join(repo, "not", "yet")
			}

			st := status(t, path)
			if st.Checkout != repo || st.InForce != test.InForce || !strings.Contains(st.Why, test.Why) {
				t.Errorf("status = %+v, want checkout %s in force %v why %q", st, repo, test.InForce, test.Why)
			}
		})
	}
}

func TestAPathOutsideAnyCheckoutHasNoStatus(t *testing.T) {
	requireGit(t)

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	if st := status(t, filepath.Join(root, "plain")); st != (HooksStatus{}) {
		t.Errorf("status = %+v, want none", st)
	}
}

func TestInstallRefusesAnythingButAnAbsoluteNitipan(t *testing.T) {
	repo := newRepo(t)

	if _, err := InstallGit(gitfence.ExecClean, repo, "nitipan"); err == nil {
		t.Error("a relative nitipan path was accepted")
	}

	if _, err := InstallGit(gitfence.ExecClean, filepath.Dir(repo), "/usr/bin/nitipan"); err == nil {
		t.Error("an install outside any checkout was accepted")
	}
}

func TestReinstallKeepsChainingToATrackedDirectoryNamedByGlobalConfig(t *testing.T) {
	repo := newRepo(t)
	exe, _ := fakeNitipan(t, "0")
	ran := filepath.Join(repo, "ran")

	writeScript(t, filepath.Join(repo, ".husky", "pre-commit"), "#!/bin/sh\necho pre-commit >> '"+ran+"'\n")
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-qm", "husky")

	global := filepath.Join(filepath.Dir(repo), "gitconfig")
	if err := os.WriteFile(global, []byte("[core]\n\thooksPath = .husky\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GIT_CONFIG_GLOBAL", global)

	install(t, repo, exe)
	install(t, repo, exe)

	own := filepath.Join(repo, ".git", trackedDirName)
	if got := read(t, filepath.Join(own, "pre-commit")); !strings.Contains(got, "'.husky/pre-commit'") {
		t.Fatalf("the chain stub after a second install is %q", got)
	}

	writeScript(t, filepath.Join(repo, "b.txt"), "two\n")
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-qm", "two")

	if !strings.Contains(read(t, ran), "pre-commit") {
		t.Error("the tracked pre-commit hook no longer runs")
	}
}

func status(t *testing.T, path string) HooksStatus {
	t.Helper()

	st, err := HooksInForce(gitfence.ExecClean, path)
	if err != nil {
		t.Fatalf("HooksInForce: %v", err)
	}

	return st
}

func TestASharedGlobalHooksDirectoryIsNeverWritten(t *testing.T) {
	repo := newRepo(t)
	exe, log := fakeNitipan(t, "0")
	shared := filepath.Join(filepath.Dir(repo), "shared-hooks")
	ran := filepath.Join(filepath.Dir(repo), "shared-ran")

	writeScript(t, filepath.Join(shared, "pre-commit"), "#!/bin/sh\necho shared >> '"+ran+"'\n")

	global := filepath.Join(filepath.Dir(repo), "gitconfig")
	if err := os.WriteFile(global, []byte("[core]\n\thooksPath = "+shared+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GIT_CONFIG_GLOBAL", global)

	install(t, repo, exe)

	if _, err := os.Stat(filepath.Join(shared, refTransaction)); !os.IsNotExist(err) {
		t.Fatalf("nitipan wrote into the shared directory: %v", err)
	}

	if st := status(t, repo); !st.InForce {
		t.Fatalf("status = %+v", st)
	}

	writeScript(t, filepath.Join(repo, "b.txt"), "two\n")
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-qm", "two")

	if !strings.Contains(read(t, ran), "shared") || !strings.Contains(read(t, log), "prepared") {
		t.Error("the shared hook or nitipan did not run")
	}

	other := newRepo(t)
	if st := status(t, other); st.InForce {
		t.Errorf("another repository sharing the directory has the hook: %+v", st)
	}

	if _, err := UninstallGit(gitfence.ExecClean, repo); err != nil {
		t.Fatal(err)
	}

	if st := status(t, repo); st.InForce {
		t.Errorf("hook still in force: %+v", st)
	}
}

func TestOnlyANonRepositoryOrNoGitCountsAsOutsideACheckout(t *testing.T) {
	requireGit(t)

	dir := t.TempDir()

	failing := func(stderr string, err error) gitfence.Runner {
		return func(string, []byte, ...string) ([]byte, error) {
			return nil, &gitfence.GitError{Err: err, Stderr: stderr}
		}
	}

	tests := []struct {
		Name    string
		Run     gitfence.Runner
		WantErr bool
	}{
		{Name: "not a repository", Run: failing("fatal: not a git repository", errors.New("exit status 128"))},
		{Name: "no git binary", Run: failing("", exec.ErrNotFound)},
		{Name: "an unsafe directory", Run: failing("fatal: detected dubious ownership", errors.New("exit status 128")), WantErr: true},
		{Name: "a timeout", Run: failing("", context.DeadlineExceeded), WantErr: true},
	}

	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			st, err := HooksInForce(test.Run, dir)
			if (err != nil) != test.WantErr || st != (HooksStatus{}) {
				t.Errorf("HooksInForce = %+v, %v; want an error: %v", st, err, test.WantErr)
			}
		})
	}
}

func TestATildeHooksPathIsExpandedTheWayGitExpandsIt(t *testing.T) {
	requireGit(t)

	tests := []struct {
		Name  string
		Local bool
	}{
		{Name: "from global config"},
		{Name: "from local config", Local: true},
	}

	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			repo := newRepo(t)
			exe, _ := fakeNitipan(t, "0")
			home := filepath.Dir(repo)
			ran := filepath.Join(home, "tilde-ran")

			t.Setenv("HOME", home)
			writeScript(t, filepath.Join(home, "tilde-hooks", "pre-commit"), "#!/bin/sh\necho tilde >> '"+ran+"'\n")

			if test.Local {
				git(t, repo, "config", "--local", "core.hooksPath", "~/tilde-hooks")
			} else {
				global := filepath.Join(home, "gitconfig")
				if err := os.WriteFile(global, []byte("[core]\n\thooksPath = ~/tilde-hooks\n"), 0o644); err != nil {
					t.Fatal(err)
				}

				t.Setenv("GIT_CONFIG_GLOBAL", global)
			}

			install(t, repo, exe)

			writeScript(t, filepath.Join(repo, "b.txt"), "two\n")
			git(t, repo, "add", "-A")
			git(t, repo, "commit", "-qm", "two")

			if !strings.Contains(read(t, ran), "tilde") {
				t.Error("the hook in the home directory was skipped")
			}

			if _, err := UninstallGit(gitfence.ExecClean, repo); err != nil {
				t.Fatal(err)
			}

			if !test.Local {
				return
			}

			if got := read(t, filepath.Join(repo, ".git", "config")); !strings.Contains(got, "hooksPath = ~/tilde-hooks\n") {
				t.Errorf("uninstall did not restore the raw core.hooksPath:\n%s", got)
			}
		})
	}
}

func TestAnExistingPrePushHookIsKeptChainedAndPutBack(t *testing.T) {
	repo := newRepo(t)
	exe, _ := fakeNitipan(t, "0")
	hook := filepath.Join(repo, ".git", "hooks", prePush)
	ran := filepath.Join(repo, "pre-push-ran")

	original := "#!/bin/sh\necho \"$1 $2\" >> '" + ran + "'\ncat >> '" + ran + "'\n"
	writeScript(t, hook, original)

	install(t, repo, exe)

	if _, err := os.Stat(hook + chainedSuffix); err != nil {
		t.Fatalf("the original pre-push hook was not kept: %v", err)
	}

	cmd := exec.Command(hook, "origin", "url")
	cmd.Stdin = strings.NewReader("refs/tags/t aaa refs/tags/t 000\n")

	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("running the installed hook: %v\n%s", err, out)
	}

	if got := read(t, ran); !strings.Contains(got, "origin url") || !strings.Contains(got, "refs/tags/t") {
		t.Errorf("the kept hook saw %q", got)
	}

	if _, err := UninstallGit(gitfence.ExecClean, repo); err != nil {
		t.Fatal(err)
	}

	if got := read(t, hook); got != original {
		t.Errorf("the original pre-push hook came back as %q", got)
	}
}

func TestACheckoutWithOnlyTheReferenceTransactionHookIsNotInForce(t *testing.T) {
	repo := newRepo(t)
	exe, _ := fakeNitipan(t, "0")

	install(t, repo, exe)

	if err := os.Remove(filepath.Join(repo, ".git", "hooks", prePush)); err != nil {
		t.Fatal(err)
	}

	st := status(t, repo)
	if st.InForce || !strings.Contains(st.Why, "there is no pre-push hook") {
		t.Errorf("status = %+v, want the missing pre-push hook named", st)
	}
}

type prePushScriptTest struct {
	Name  string
	Input string
}

// Every input starts nitipan. git lists no ref that a pruning push deletes, so an empty input or a
// push of tags alone says nothing about whether the push is harmless.
var prePushScriptTests = []prePushScriptTest{
	{Name: "a branch update", Input: "refs/heads/main aaa refs/heads/main 000\n"},
	{Name: "a fence ref update", Input: "aaa aaa refs/nitipan/fence/r/x 000\n"},
	{Name: "a tag alone", Input: "refs/tags/t aaa refs/tags/t 000\n"},
	{Name: "nothing", Input: ""},
}

func TestThePrePushScriptAlwaysStartsNitipan(t *testing.T) {
	for _, test := range prePushScriptTests {
		t.Run(test.Name, func(t *testing.T) {
			repo := newRepo(t)
			exe, log := fakeNitipan(t, "0")
			install(t, repo, exe)

			cmd := exec.Command(filepath.Join(repo, ".git", "hooks", prePush), "origin", "url")
			cmd.Stdin = strings.NewReader(test.Input)

			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("hook: %v\n%s", err, out)
			}

			if !strings.Contains(readIfAny(log), "hook pre-push origin url") {
				t.Errorf("nitipan was not asked about the push: %q", readIfAny(log))
			}
		})
	}
}

func readIfAny(path string) string {
	data, _ := os.ReadFile(path)

	return string(data)
}

func TestThePrePushScriptReportsATooOldBinaryAndAMissingOne(t *testing.T) {
	repo := newRepo(t)
	old, _ := fakeNitipan(t, "2")
	install(t, repo, old)

	hook := filepath.Join(repo, ".git", "hooks", prePush)
	input := "refs/heads/main aaa refs/heads/main 000\n"

	cmd := exec.Command(hook, "origin", "url")
	cmd.Stdin = strings.NewReader(input)

	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "too old for the hook installed in this clone") {
		t.Errorf("a binary that exits 2: err %v\n%s", err, out)
	}

	if err := os.Remove(old); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", t.TempDir())

	cmd = exec.Command("/bin/sh", hook, "origin", "url")
	cmd.Stdin = strings.NewReader(input)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}

	out, err = cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "the nitipan binary that checks leases is missing") {
		t.Errorf("a missing binary: err %v\n%s", err, out)
	}

	if strings.Contains(string(out), "core.hooksPath=/dev/null") {
		t.Errorf("the message names the escape hatch:\n%s", out)
	}
}
