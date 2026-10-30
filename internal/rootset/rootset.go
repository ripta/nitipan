// Package rootset derives the repo-relative names of a wider-reach lease from a checkout.
//
// A repo is identified by the root commits reachable from HEAD. Git runs with replace refs off, and
// the derivation refuses every repo state whose root set could differ between machines.
package rootset

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/space-armada/nitipan/internal/keys"
	"github.com/space-armada/nitipan/internal/repoid"
)

// Version names the derivation. The broker refuses a request below its floor, so a change to what the
// derivation emits raises this number.
const Version = 1

// Timeouts. The root walk has its own limit because a long history can outlast the five seconds
// other git calls get.
const (
	DefaultWalkTimeout = 30 * time.Second
	queryTimeout       = 5 * time.Second
	maxCached          = 64
)

// Refusals. Each message says what to do.
var (
	ErrNotRepo         = errors.New("the path is not inside a git checkout")
	ErrShallow         = errors.New("the clone is shallow, so its root commits are unknown. Run: git fetch --unshallow")
	ErrUnborn          = errors.New("the checkout has no commits yet. Make a first commit, then claim again")
	ErrGrafts          = errors.New("the repo has an info/grafts file, which rewrites history. Remove the file or claim at machine reach")
	ErrSubmoduleUninit = errors.New("a submodule the claim covers is not initialized. Run: git submodule update --init")
	ErrTimeout         = errors.New("walking the root commits timed out. The claim is refused rather than made on a partial set")
)

// Runner runs git in a directory under ctx and returns its standard output.
type Runner func(ctx context.Context, dir string, args ...string) ([]byte, error)

// ExecRunner runs the git binary on PATH. It turns replace refs off and strips the variables that
// point git at another repo or a graft file.
func ExecRunner(ctx context.Context, dir string, args ...string) ([]byte, error) {
	full := append([]string{"--no-replace-objects", "-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = keys.CleanGitEnv(os.Environ())

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), ctx.Err())
		}

		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}

	return out, nil
}

// Result is what a derivation returns for one key.
type Result struct {
	Version int `json:"version"`

	// Text is the first-parent root, the root the typed form carries.
	Text string `json:"text"`

	// Names are the key's entries: the innermost checkout's, then each superproject's, then each
	// covered submodule's.
	Names []repoid.Name `json:"names"`
}

// Deriver derives names. The zero value runs git with the default timeouts.
type Deriver struct {
	Run         Runner
	WalkTimeout time.Duration

	// CachePath, when set, keeps the root sets between commands. It must be in a directory no V1
	// binary scans.
	CachePath string

	cache map[string][]string
}

func (d *Deriver) run(dir string, timeout time.Duration, args ...string) (string, error) {
	run := d.Run
	if run == nil {
		run = ExecRunner
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	out, err := run(ctx, dir, args...)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return "", fmt.Errorf("%w: %w", ErrTimeout, err)
		}

		return "", err
	}

	return strings.TrimSpace(string(out)), nil
}

func (d *Deriver) walkTimeout() time.Duration {
	if d.WalkTimeout > 0 {
		return d.WalkTimeout
	}

	return DefaultWalkTimeout
}

// Derive returns the names of one canonical key.
func (d *Deriver) Derive(key keys.Key) (Result, error) {
	dir := nearestDir(key.Path)

	top, err := d.top(dir)
	if err != nil {
		return Result{}, err
	}

	res := Result{Version: Version}

	for i := 0; ; i++ {
		roots, text, err := d.repoRoots(top)
		if err != nil {
			return Result{}, err
		}

		if i == 0 {
			res.Text = text
		}

		rel := relPath(top, key.Path)
		res.Names = append(res.Names, repoid.New(roots, rel, key.FoldCase, key.Normalize))

		if i == 0 {
			if err := d.coveredSubmodules(top, key, &res); err != nil {
				return Result{}, err
			}
		}

		super, err := d.run(top, queryTimeout, "rev-parse", "--show-superproject-working-tree")
		if err != nil || super == "" {
			break
		}

		top = realPath(super)
	}

	return res, nil
}

// top finds the innermost checkout's top directory, resolved through symlinks.
func (d *Deriver) top(dir string) (string, error) {
	out, err := d.run(dir, queryTimeout, "rev-parse", "--show-toplevel")
	if err != nil {
		if errors.Is(err, ErrTimeout) {
			return "", err
		}

		return "", fmt.Errorf("%s: %w", dir, ErrNotRepo)
	}

	return realPath(out), nil
}

// repoRoots refuses the repo states whose roots cannot be trusted and then walks the roots.
func (d *Deriver) repoRoots(top string) (roots []string, text string, err error) {
	if shallow, err := d.run(top, queryTimeout, "rev-parse", "--is-shallow-repository"); err != nil {
		return nil, "", err
	} else if shallow == "true" {
		return nil, "", fmt.Errorf("%s: %w", top, ErrShallow)
	}

	head, err := d.run(top, queryTimeout, "rev-parse", "--verify", "-q", "HEAD")
	if err != nil || head == "" {
		if errors.Is(err, ErrTimeout) {
			return nil, "", err
		}

		return nil, "", fmt.Errorf("%s: %w", top, ErrUnborn)
	}

	common, err := d.run(top, queryTimeout, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, "", err
	}

	if _, err := os.Stat(filepath.Join(common, "info", "grafts")); err == nil {
		return nil, "", fmt.Errorf("%s: %w", top, ErrGrafts)
	}

	roots, err = d.walk(top, head)
	if err != nil {
		return nil, "", err
	}

	first, err := d.run(top, d.walkTimeout(), "rev-list", "--max-parents=0", "--first-parent", "HEAD")
	if err != nil {
		return nil, "", err
	}

	text, _, _ = strings.Cut(first, "\n")

	return roots, text, nil
}

// walk returns the whole root set at head. A cached ancestor limits the walk to the new commits.
func (d *Deriver) walk(top, head string) ([]string, error) {
	d.loadCache()

	if roots, ok := d.cache[head]; ok {
		return slices.Clone(roots), nil
	}

	var base []string

	rangeArg := "HEAD"

	for cached, roots := range d.cache {
		if _, err := d.run(top, queryTimeout, "merge-base", "--is-ancestor", cached, head); err == nil {
			base, rangeArg = roots, cached+"..HEAD"

			break
		}
	}

	out, err := d.run(top, d.walkTimeout(), "rev-list", "--max-parents=0", rangeArg)
	if err != nil {
		return nil, err
	}

	roots := slices.Clone(base)

	for _, line := range strings.Fields(out) {
		roots = append(roots, strings.ToLower(line))
	}

	slices.Sort(roots)
	roots = slices.Compact(roots)

	if len(roots) == 0 {
		return nil, fmt.Errorf("%s: %w", top, ErrUnborn)
	}

	d.remember(head, roots)

	return roots, nil
}

// coveredSubmodules adds an entry for each submodule the key covers, and refuses one that is not
// initialized, since its roots cannot be read.
func (d *Deriver) coveredSubmodules(top string, key keys.Key, res *Result) error {
	out, err := d.run(top, queryTimeout, "ls-files", "--stage")
	if err != nil {
		return err
	}

	for _, line := range strings.Split(out, "\n") {
		meta, path, found := strings.Cut(line, "\t")
		if !found || !strings.HasPrefix(meta, "160000 ") {
			continue
		}

		abs := filepath.Join(top, path)

		if !keys.Contains(key, keys.Key{Path: abs}, nil) {
			continue
		}

		if _, err := os.Lstat(filepath.Join(abs, ".git")); err != nil {
			return fmt.Errorf("%s: %w", abs, ErrSubmoduleUninit)
		}

		sub, err := d.top(abs)
		if err != nil {
			return err
		}

		roots, _, err := d.repoRoots(sub)
		if err != nil {
			return err
		}

		res.Names = append(res.Names, repoid.New(roots, "", key.FoldCase, key.Normalize))
	}

	return nil
}

func (d *Deriver) loadCache() {
	if d.cache != nil {
		return
	}

	d.cache = map[string][]string{}

	if d.CachePath == "" {
		return
	}

	data, err := os.ReadFile(d.CachePath)
	if err != nil {
		return
	}

	_ = json.Unmarshal(data, &d.cache)
}

func (d *Deriver) remember(head string, roots []string) {
	if len(d.cache) >= maxCached {
		d.cache = map[string][]string{}
	}

	d.cache[head] = roots

	if d.CachePath == "" {
		return
	}

	data, err := json.Marshal(d.cache)
	if err != nil {
		return
	}

	dir := filepath.Dir(d.CachePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}

	tmp, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(d.CachePath)+"-*")
	if err != nil {
		return
	}

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())

		return
	}

	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())

		return
	}

	if err := os.Rename(tmp.Name(), d.CachePath); err != nil {
		_ = os.Remove(tmp.Name())
	}
}

// Union merges the entries of several results, dropping equal ones.
func Union(results ...Result) []repoid.Name {
	var out []repoid.Name

	for _, r := range results {
		for _, n := range r.Names {
			if !slices.ContainsFunc(out, func(o repoid.Name) bool { return repoid.Equal(o, n) }) {
				out = append(out, n)
			}
		}
	}

	return out
}

func nearestDir(p string) string {
	for {
		if info, err := os.Stat(p); err == nil && info.IsDir() {
			return p
		}

		parent := filepath.Dir(p)
		if parent == p {
			return p
		}

		p = parent
	}
}

func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}

	return p
}

func relPath(top, p string) string {
	rel, err := filepath.Rel(top, realPath(nearestDir(p))+strings.TrimPrefix(p, nearestDir(p)))
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return ""
	}

	return filepath.ToSlash(rel)
}
