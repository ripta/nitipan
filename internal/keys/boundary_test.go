package keys

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type gitCall struct {
	Dir  string
	Args []string
}

type fakeGit struct {
	out   string
	err   error
	calls []gitCall
}

func (f *fakeGit) run(dir string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, gitCall{Dir: dir, Args: args})

	return []byte(f.out), f.err
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	mkdir(t, filepath.Dir(path))

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

type boundaryTest struct {
	Name       string
	Setup      func(t *testing.T, root string)
	GitOut     string
	GitErr     error
	Dir        string
	Want       bool
	WantGitRun bool
}

var boundaryTests = []boundaryTest{
	{
		Name:  "plain directory",
		Setup: func(t *testing.T, root string) { mkdir(t, filepath.Join(root, "repo", "src")) },
		Dir:   "repo/src",
		Want:  false,
	},
	{
		Name: "worktree with a .git file inside a repo",
		Setup: func(t *testing.T, root string) {
			mkdir(t, filepath.Join(root, "repo", ".git"))
			writeFile(t, filepath.Join(root, "repo", "w", ".git"), "gitdir: /elsewhere\n")
		},
		Dir:        "repo/w",
		Want:       true,
		WantGitRun: true,
	},
	{
		Name: "nested repo with a .git directory",
		Setup: func(t *testing.T, root string) {
			mkdir(t, filepath.Join(root, "repo", ".git"))
			mkdir(t, filepath.Join(root, "repo", "vendor", "lib", ".git"))
		},
		Dir:        "repo/vendor/lib",
		Want:       true,
		WantGitRun: true,
	},
	{
		Name: "repo with no enclosing repo",
		Setup: func(t *testing.T, root string) {
			mkdir(t, filepath.Join(root, "repo", ".git"))
		},
		Dir:  "repo",
		Want: true,
	},
	{
		Name: "submodule named in .gitmodules",
		Setup: func(t *testing.T, root string) {
			mkdir(t, filepath.Join(root, "repo", ".git"))
			writeFile(t, filepath.Join(root, "repo", ".gitmodules"), "[submodule \"lib\"]\n\tpath = third_party/lib\n\turl = x\n")
			writeFile(t, filepath.Join(root, "repo", "third_party", "lib", ".git"), "gitdir: ../../.git/modules/lib\n")
		},
		Dir:  "repo/third_party/lib",
		Want: false,
	},
	{
		Name: "submodule known only from an index gitlink",
		Setup: func(t *testing.T, root string) {
			mkdir(t, filepath.Join(root, "repo", ".git"))
			writeFile(t, filepath.Join(root, "repo", "sub", ".git"), "gitdir: ../.git/modules/sub\n")
		},
		GitOut:     "160000 0123456789abcdef0123456789abcdef01234567 0\tsub\x00",
		Dir:        "repo/sub",
		Want:       false,
		WantGitRun: true,
	},
	{
		Name: "tracked files under the path are not a gitlink",
		Setup: func(t *testing.T, root string) {
			mkdir(t, filepath.Join(root, "repo", ".git"))
			writeFile(t, filepath.Join(root, "repo", "w", ".git"), "gitdir: /elsewhere\n")
		},
		GitOut:     "100644 0123456789abcdef0123456789abcdef01234567 0\tw/file.txt\x00",
		Dir:        "repo/w",
		Want:       true,
		WantGitRun: true,
	},
	{
		Name: "a failing git is not a boundary",
		Setup: func(t *testing.T, root string) {
			mkdir(t, filepath.Join(root, "repo", ".git"))
			writeFile(t, filepath.Join(root, "repo", "w", ".git"), "gitdir: /elsewhere\n")
		},
		GitErr:     errors.New("git: not found"),
		Dir:        "repo/w",
		Want:       false,
		WantGitRun: true,
	},
}

func TestGitBoundaries(t *testing.T) {
	for _, test := range boundaryTests {
		t.Run(test.Name, func(t *testing.T) {
			root := resolvedTempDir(t)
			test.Setup(t, root)

			git := &fakeGit{out: test.GitOut, err: test.GitErr}
			finder := NewGitBoundaries(git.run)

			dir := filepath.Join(root, test.Dir)
			if got := finder.IsBoundary(dir); got != test.Want {
				t.Errorf("IsBoundary(%s) = %v, want %v", test.Dir, got, test.Want)
			}

			if ran := len(git.calls) > 0; ran != test.WantGitRun {
				t.Errorf("git ran = %v, want %v", ran, test.WantGitRun)
			}

			finder.IsBoundary(dir)

			if test.WantGitRun && len(git.calls) != 1 {
				t.Errorf("git ran %d times, want the second answer cached", len(git.calls))
			}
		})
	}
}

func TestBoundaryIsFoundAtCheckTime(t *testing.T) {
	root := resolvedTempDir(t)
	mkdir(t, filepath.Join(root, "repo", ".git"))
	mkdir(t, filepath.Join(root, "repo", "w", "src"))

	parent := Key{Path: filepath.Join(root, "repo")}
	child := Key{Path: filepath.Join(root, "repo", "w", "src")}

	before := NewGitBoundaries((&fakeGit{}).run)
	if !Overlap(parent, child, before) {
		t.Error("a plain directory should overlap its parent")
	}

	writeFile(t, filepath.Join(root, "repo", "w", ".git"), "gitdir: /elsewhere\n")

	after := NewGitBoundaries((&fakeGit{}).run)
	if Overlap(parent, child, after) {
		t.Error("a directory that became a worktree should be separate from its parent")
	}
}
