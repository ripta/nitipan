package rootset

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/keys"
	"github.com/space-armada/nitipan/internal/repoid"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()

	full := append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false",
		"-c", "protocol.file.allow=always", "-c", "init.defaultBranch=main"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = keys.CleanGitEnv(os.Environ())

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}

	return strings.TrimSpace(string(out))
}

func newRepo(t *testing.T) string {
	t.Helper()

	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	git(t, dir, "init")

	return dir
}

func commit(t *testing.T, dir, file string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, file)), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, file), []byte(file), 0o644); err != nil {
		t.Fatal(err)
	}

	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-m", file)
}

func TestDeriveSameRootsFromTwoClones(t *testing.T) {
	origin := newRepo(t)
	commit(t, origin, "svc/a.txt")

	clone, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	git(t, clone, "clone", origin, "c")

	d := &Deriver{}

	a, err := d.Derive(keys.Key{Path: filepath.Join(origin, "svc")})
	if err != nil {
		t.Fatal(err)
	}

	b, err := (&Deriver{}).Derive(keys.Key{Path: filepath.Join(clone, "c", "svc", "deeper")})
	if err != nil {
		t.Fatal(err)
	}

	if len(a.Names) != 1 || a.Names[0].Path != "svc" || b.Names[0].Path != "svc/deeper" {
		t.Fatalf("paths: %+v %+v", a.Names, b.Names)
	}

	if !repoid.Overlap(a.Names[0], b.Names[0]) {
		t.Fatalf("clones of one repo must derive overlapping names: %v vs %v", a.Names[0], b.Names[0])
	}

	if a.Version != Version || a.Text == "" || !a.Names[0].HasRoot(a.Text) {
		t.Fatalf("result = %+v", a)
	}
}

func TestDeriveRootSetIncludesEveryRoot(t *testing.T) {
	dir := newRepo(t)
	commit(t, dir, "a.txt")

	git(t, dir, "checkout", "--orphan", "other")
	commit(t, dir, "b.txt")
	git(t, dir, "checkout", "main")
	git(t, dir, "merge", "--allow-unrelated-histories", "-m", "merge", "other")

	res, err := (&Deriver{}).Derive(keys.Key{Path: dir})
	if err != nil {
		t.Fatal(err)
	}

	if got := len(res.Names[0].Roots); got != 2 {
		t.Fatalf("root set = %v, want two roots", res.Names[0].Roots)
	}
}

type refusalTest struct {
	Name  string
	Setup func(t *testing.T) string
	Want  error
}

var refusalTests = []refusalTest{
	{"unborn HEAD", newRepo, ErrUnborn},
	{"shallow clone", func(t *testing.T) string {
		origin := newRepo(t)
		commit(t, origin, "a")
		commit(t, origin, "b")

		parent, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}

		git(t, parent, "clone", "--depth", "1", "file://"+origin, "s")

		return filepath.Join(parent, "s")
	}, ErrShallow},
	{"graft file", func(t *testing.T) string {
		dir := newRepo(t)
		commit(t, dir, "a")

		if err := os.MkdirAll(filepath.Join(dir, ".git", "info"), 0o755); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(filepath.Join(dir, ".git", "info", "grafts"), nil, 0o644); err != nil {
			t.Fatal(err)
		}

		return dir
	}, ErrGrafts},
	{"not a repo", func(t *testing.T) string { return t.TempDir() }, ErrNotRepo},
}

func TestDeriveRefusals(t *testing.T) {
	for _, test := range refusalTests {
		t.Run(test.Name, func(t *testing.T) {
			dir := test.Setup(t)

			_, err := (&Deriver{}).Derive(keys.Key{Path: dir})
			if !errors.Is(err, test.Want) {
				t.Fatalf("err = %v, want %v", err, test.Want)
			}
		})
	}
}

func TestDeriveGraftEnvIsStripped(t *testing.T) {
	dir := newRepo(t)
	commit(t, dir, "a")
	t.Setenv("GIT_GRAFT_FILE", filepath.Join(dir, "nonexistent"))

	if _, err := (&Deriver{}).Derive(keys.Key{Path: dir}); err != nil {
		t.Fatalf("an inherited GIT_GRAFT_FILE must not change the answer: %v", err)
	}
}

func TestDeriveSubmodules(t *testing.T) {
	lib := newRepo(t)
	commit(t, lib, "lib.txt")

	super := newRepo(t)
	commit(t, super, "main.txt")
	git(t, super, "submodule", "add", lib, "vendor/lib")
	git(t, super, "commit", "-m", "add submodule")

	libRoot := git(t, lib, "rev-list", "--max-parents=0", "HEAD")

	t.Run("covering an initialized submodule", func(t *testing.T) {
		res, err := (&Deriver{}).Derive(keys.Key{Path: super})
		if err != nil {
			t.Fatal(err)
		}

		if len(res.Names) != 2 || !res.Names[1].HasRoot(libRoot) || res.Names[1].Path != "" {
			t.Fatalf("names = %+v", res.Names)
		}
	})

	t.Run("inside a submodule", func(t *testing.T) {
		res, err := (&Deriver{}).Derive(keys.Key{Path: filepath.Join(super, "vendor", "lib", "sub")})
		if err != nil {
			t.Fatal(err)
		}

		if len(res.Names) != 2 || !res.Names[0].HasRoot(libRoot) || res.Names[0].Path != "sub" ||
			res.Names[1].Path != "vendor/lib/sub" {
			t.Fatalf("names = %+v", res.Names)
		}
	})

	t.Run("an uninitialized submodule is refused", func(t *testing.T) {
		parent, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}

		git(t, parent, "clone", super, "bare")

		_, err = (&Deriver{}).Derive(keys.Key{Path: filepath.Join(parent, "bare")})
		if !errors.Is(err, ErrSubmoduleUninit) {
			t.Fatalf("err = %v, want ErrSubmoduleUninit", err)
		}
	})
}

func TestRootWalkTimeoutRefusesRatherThanFallingBack(t *testing.T) {
	dir := newRepo(t)
	commit(t, dir, "a")

	d := &Deriver{WalkTimeout: 20 * time.Millisecond}
	d.Run = func(ctx context.Context, dir string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "rev-list" {
			<-ctx.Done()

			return nil, ctx.Err()
		}

		return ExecRunner(ctx, dir, args...)
	}

	_, err := d.Derive(keys.Key{Path: dir})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
}

func TestCachedAncestorWalksOnlyNewCommits(t *testing.T) {
	dir := newRepo(t)
	commit(t, dir, "a")

	var walks []string

	d := &Deriver{CachePath: filepath.Join(t.TempDir(), "cache.json")}
	d.Run = func(ctx context.Context, dir string, args ...string) ([]byte, error) {
		if len(args) > 1 && args[0] == "rev-list" && args[2] != "--first-parent" {
			walks = append(walks, args[len(args)-1])
		}

		return ExecRunner(ctx, dir, args...)
	}

	first, err := d.Derive(keys.Key{Path: dir})
	if err != nil {
		t.Fatal(err)
	}

	commit(t, dir, "b")

	second, err := d.Derive(keys.Key{Path: dir})
	if err != nil {
		t.Fatal(err)
	}

	if len(walks) != 2 || walks[0] != "HEAD" || !strings.HasSuffix(walks[1], "..HEAD") {
		t.Fatalf("walks = %v, want a full walk and then an incremental one", walks)
	}

	if !repoid.Equal(first.Names[0], second.Names[0]) {
		t.Fatalf("incremental walk changed the set: %v vs %v", first.Names[0], second.Names[0])
	}

	var calls atomic.Int32

	again := &Deriver{CachePath: d.CachePath, Run: func(ctx context.Context, dir string, args ...string) ([]byte, error) {
		if len(args) > 1 && args[0] == "rev-list" && args[2] != "--first-parent" {
			calls.Add(1)
		}

		return ExecRunner(ctx, dir, args...)
	}}

	if _, err := again.Derive(keys.Key{Path: dir}); err != nil {
		t.Fatal(err)
	}

	if calls.Load() != 0 {
		t.Fatal("a persisted cache entry for HEAD must skip the walk")
	}
}

func TestCacheWriteLeavesNoTempFile(t *testing.T) {
	dir := newRepo(t)
	commit(t, dir, "a")

	cacheDir := t.TempDir()
	d := &Deriver{CachePath: filepath.Join(cacheDir, "sub", "cache.json")}

	if _, err := d.Derive(keys.Key{Path: dir}); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(filepath.Join(cacheDir, "sub"))
	if err != nil || len(entries) != 1 || entries[0].Name() != "cache.json" {
		t.Fatalf("entries = %v, %v", entries, err)
	}
}
