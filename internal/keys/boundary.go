package keys

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// BoundaryFinder says whether a directory starts a separate git checkout.
type BoundaryFinder interface {
	IsBoundary(dir string) bool
}

// GitRunner runs git in a directory and returns its standard output.
type GitRunner func(dir string, args ...string) ([]byte, error)

const gitTimeout = 5 * time.Second

const gitlinkMode = "160000"

// ExecGit is the GitRunner that runs the git binary found on PATH.
func ExecGit(dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = cleanGitEnv(os.Environ())

	return cmd.Output()
}

// GitBoundaries finds checkout boundaries from the filesystem.
//
// A directory holding a `.git` file or directory is a boundary, unless it is a submodule of the
// enclosing repo. A submodule stays part of its parent, because the parent's git commands write into
// it. Results are cached for the life of the value, so use one value per command.
type GitBoundaries struct {
	run GitRunner

	mu    sync.Mutex
	cache map[string]bool
}

// NewGitBoundaries returns a finder that asks run about submodules.
func NewGitBoundaries(run GitRunner) *GitBoundaries {
	return &GitBoundaries{run: run, cache: map[string]bool{}}
}

// IsBoundary implements BoundaryFinder.
//
// Any error while deciding counts as "not a boundary". That reads more keys as overlapping, which
// refuses a claim instead of letting two sessions share a directory.
func (g *GitBoundaries) IsBoundary(dir string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	if v, ok := g.cache[dir]; ok {
		return v
	}

	v := g.compute(dir)
	g.cache[dir] = v

	return v
}

func (g *GitBoundaries) compute(dir string) bool {
	if _, err := os.Lstat(filepath.Join(dir, ".git")); err != nil {
		return false
	}

	root, ok := enclosingRepo(dir)
	if !ok {
		return true
	}

	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return false
	}

	rel = filepath.ToSlash(rel)

	if inGitmodules(root, rel) {
		return false
	}

	// The path is data, never a pattern. -z keeps non-ASCII names unquoted.
	out, err := g.run(root, "--literal-pathspecs", "ls-files", "-s", "-z", "--", rel)
	if err != nil {
		return false
	}

	return !hasGitlink(out, rel)
}

// enclosingRepo returns the nearest directory above dir that holds a `.git` entry.
func enclosingRepo(dir string) (string, bool) {
	cur := dir

	for {
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", false
		}

		if _, err := os.Lstat(filepath.Join(parent, ".git")); err == nil {
			return parent, true
		}

		cur = parent
	}
}

func inGitmodules(root, rel string) bool {
	data, err := os.ReadFile(filepath.Join(root, ".gitmodules"))
	if err != nil {
		return false
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		key, value, found := strings.Cut(strings.TrimSpace(scanner.Text()), "=")
		if !found || strings.TrimSpace(key) != "path" {
			continue
		}

		if strings.TrimSpace(value) == rel {
			return true
		}
	}

	return false
}

func hasGitlink(out []byte, rel string) bool {
	for _, rec := range bytes.Split(out, []byte{0}) {
		meta, path, found := strings.Cut(string(rec), "\t")
		if !found || path != rel {
			continue
		}

		if strings.HasPrefix(meta, gitlinkMode+" ") {
			return true
		}
	}

	return false
}

// repoLocatingVars are the variables that point git at a repository other than the one in the
// directory it is run in. They are removed so an inherited value cannot change the answer.
var repoLocatingVars = []string{
	"GIT_DIR",
	"GIT_WORK_TREE",
	"GIT_INDEX_FILE",
	"GIT_COMMON_DIR",
	"GIT_OBJECT_DIRECTORY",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES",
	"GIT_NAMESPACE",
	"GIT_PREFIX",
	"GIT_CEILING_DIRECTORIES",
}

// cleanGitEnv returns the environment without the repo-locating variables.
func cleanGitEnv(env []string) []string {
	out := make([]string, 0, len(env))

	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if slices.Contains(repoLocatingVars, name) {
			continue
		}

		out = append(out, kv)
	}

	return out
}
