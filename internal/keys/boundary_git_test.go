package keys

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func requireGit(t *testing.T) {
	t.Helper()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()

	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = cleanGitEnv(os.Environ())

	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// gitlinkRepo makes a repo whose index holds a gitlink at each given path, with a `.git` file at
// each path on disk, as a checked-out submodule has.
func gitlinkRepo(t *testing.T, gitlinks []string) string {
	t.Helper()

	root := resolvedTempDir(t)
	git(t, root, "init", "-q")

	for _, path := range gitlinks {
		git(t, root, "--literal-pathspecs", "update-index", "--add", "--cacheinfo",
			"160000,0123456789abcdef0123456789abcdef01234567,"+path)
		writeFile(t, filepath.Join(root, path, ".git"), "gitdir: elsewhere\n")
	}

	return root
}

type realGitTest struct {
	Name     string
	Gitlinks []string
	Plain    []string
	Dir      string
	Want     bool
}

var realGitTests = []realGitTest{
	{Name: "ordinary submodule", Gitlinks: []string{"lib"}, Dir: "lib", Want: false},
	{Name: "glob characters in the submodule path", Gitlinks: []string{"sub[1]"}, Dir: "sub[1]", Want: false},
	{
		Name:     "a glob gitlink does not make a lookalike a submodule",
		Gitlinks: []string{"sub[1]"},
		Plain:    []string{"sub1"},
		Dir:      "sub1",
		Want:     true,
	},
	{Name: "star in the submodule path", Gitlinks: []string{"a*b"}, Plain: []string{"axb"}, Dir: "axb", Want: true},
	{Name: "non-ASCII submodule path", Gitlinks: []string{"sübmodule"}, Dir: "sübmodule", Want: false},
	{Name: "nested repo that is not a gitlink", Plain: []string{"vendor"}, Dir: "vendor", Want: true},
}

func TestGitBoundariesAgainstARealRepo(t *testing.T) {
	requireGit(t)

	for _, test := range realGitTests {
		t.Run(test.Name, func(t *testing.T) {
			root := gitlinkRepo(t, test.Gitlinks)

			for _, path := range test.Plain {
				writeFile(t, filepath.Join(root, path, ".git"), "gitdir: elsewhere\n")
			}

			finder := NewGitBoundaries(ExecGit)
			if got := finder.IsBoundary(filepath.Join(root, test.Dir)); got != test.Want {
				t.Errorf("IsBoundary(%q) = %v, want %v", test.Dir, got, test.Want)
			}
		})
	}
}

func TestExecGitIgnoresRepoLocatingVariables(t *testing.T) {
	requireGit(t)

	root := gitlinkRepo(t, []string{"lib"})

	other := resolvedTempDir(t)
	git(t, other, "init", "-q")

	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_INDEX_FILE", filepath.Join(other, "no-such-index"))
	t.Setenv("GIT_WORK_TREE", other)

	if NewGitBoundaries(ExecGit).IsBoundary(filepath.Join(root, "lib")) {
		t.Error("an inherited GIT_DIR changed which repo answered")
	}
}

func TestCleanGitEnvKeepsUnrelatedVariables(t *testing.T) {
	got := cleanGitEnv([]string{"PATH=/bin", "GIT_DIR=/x", "GIT_AUTHOR_NAME=n", "GIT_INDEX_FILE=/y", "HOME=/h"})
	want := []string{"PATH=/bin", "GIT_AUTHOR_NAME=n", "HOME=/h"}

	if !slices.Equal(got, want) {
		t.Errorf("env = %v, want %v", got, want)
	}
}

func TestBoundaryLookupUsesLiteralPathspecsAndNULOutput(t *testing.T) {
	root := resolvedTempDir(t)
	mkdir(t, filepath.Join(root, ".git"))
	writeFile(t, filepath.Join(root, "w", ".git"), "gitdir: x\n")

	fake := &fakeGit{}
	NewGitBoundaries(fake.run).IsBoundary(filepath.Join(root, "w"))

	if len(fake.calls) != 1 {
		t.Fatalf("calls = %v", fake.calls)
	}

	want := []string{"--literal-pathspecs", "ls-files", "-s", "-z", "--", "w"}
	if !slices.Equal(fake.calls[0].Args, want) {
		t.Errorf("args = %v, want %v", fake.calls[0].Args, want)
	}
}
