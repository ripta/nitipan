package gitfence

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/space-armada/nitipan/internal/keys"
)

// Update is one line of a reference-transaction hook's input.
//
// git passes the caller's expected old value there, not the value the ref holds, so the old value
// is never read from the input. A symbolic update moves no commit and carries no new commit.
type Update struct {
	Ref      string
	New      string
	Symbolic bool
}

// ParseUpdates reads the hook's input. A deletion has an empty New.
func ParseUpdates(data []byte) ([]Update, error) {
	var out []Update

	for _, line := range lines(data) {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return nil, fmt.Errorf("reference transaction line %q: want <old> <new> <ref>", line)
		}

		u := Update{Ref: fields[2], New: fields[1]}

		switch {
		case strings.HasPrefix(u.New, "ref:"):
			u.Symbolic = true
			u.New = ""
		case isZero(u.New):
			u.New = ""
		case !isObjectID(u.New):
			return nil, fmt.Errorf("reference transaction line %q: %q is not an object ID", line, u.New)
		}

		out = append(out, u)
	}

	return out, nil
}

func isZero(id string) bool {
	return strings.Trim(id, "0") == ""
}

func isObjectID(id string) bool {
	if len(id) != 40 && len(id) != 64 {
		return false
	}

	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}

	return true
}

// Judged reports whether the fence looks at a ref. Branches and HEAD are judged. Tags, remote
// tracking refs, salvage pins, and git's own scratch refs pass.
func Judged(u Update) bool {
	return !u.Symbolic && (u.Ref == "HEAD" || strings.HasPrefix(u.Ref, "refs/heads/"))
}

// MissingObjectError means a commit that a rule needs is not in this clone.
type MissingObjectError struct {
	Ref    string
	Object string
}

// Error implements error.
func (e *MissingObjectError) Error() string {
	return fmt.Sprintf("object %s, which the update of %s needs, is not in this clone. Fetch it, then try again",
		e.Object, e.Ref)
}

// RefTouch is what one judged ref update touches.
type RefTouch struct {
	Ref string

	// New is the commit the ref moves to. It is empty for a deletion.
	New string

	// Checkout is the worktree the keys were mapped against.
	Checkout keys.Key

	// Keys are the touched paths as absolute keys, without duplicates.
	Keys []keys.Key
}

// Fence computes the touched set.
type Fence struct {
	// Git runs in the hook's environment and working directory.
	Git Runner

	// GitIn runs in another worktree, without the hook's repository variables.
	GitIn Runner

	Canon keys.Canonicalizer
}

// New returns a Fence over the real git binary.
func New() *Fence {
	return &Fence{Git: Exec, GitIn: ExecClean, Canon: keys.NewCanonicalizer()}
}

// repoFacts are the facts about the repository that more than one update needs. Each is read the
// first time an update asks for it, so a HEAD update costs no more git calls than it must.
type repoFacts struct {
	top       string
	common    string
	worktrees []worktree

	haveTop, haveCommon, haveWorktrees bool
}

type worktree struct {
	Path   string
	Branch string
	Bare   bool
}

// Touched works out the paths of every judged update.
//
// The baseline is every branch of every worktree plus every remote-tracking ref. Added commits are
// the ones the new value has that the baseline does not, so a pull, a rebase onto upstream, and a
// checkout of origin/main add nothing. Removed commits are the ones a branch loses that no other
// branch holds, which covers a rewind and a deletion. HEAD counts added commits only, since a
// checkout of a branch from a detached commit would otherwise count the commits it leaves.
//
// A branch that is checked out in a worktree also counts the tree difference between its old and
// new values when that worktree's index still matches the old value. Then the ref moves under a
// tree git did not rewrite.
func (f *Fence) Touched(updates []Update) ([]RefTouch, error) {
	var (
		out   []RefTouch
		facts repoFacts
	)

	for _, u := range updates {
		if !Judged(u) {
			continue
		}

		touch, ok, err := f.touchOf(u, &facts)
		if err != nil {
			return nil, err
		}

		if ok {
			out = append(out, touch)
		}
	}

	return out, nil
}

func (f *Fence) touchOf(u Update, facts *repoFacts) (RefTouch, bool, error) {
	cur, err := f.resolve(u.Ref)
	if err != nil {
		return RefTouch{}, false, err
	}

	if cur == u.New {
		return RefTouch{}, false, nil
	}

	if u.New == "" && f.prunedCopy(facts, u.Ref, cur) {
		return RefTouch{}, false, nil
	}

	commits, err := f.commits(u, cur, localBaseline(u.Ref))
	if err != nil {
		return RefTouch{}, false, err
	}

	paths, err := f.diffCommits(commits)
	if err != nil {
		return RefTouch{}, false, err
	}

	checkedOut, err := f.checkedOut(facts, u.Ref)
	if err != nil {
		return RefTouch{}, false, err
	}

	if checkedOut != "" && cur != "" && u.New != "" && f.indexMatches(checkedOut, cur) {
		more, err := f.treeDiff(cur, u.New)
		if err != nil {
			return RefTouch{}, false, err
		}

		paths = append(paths, more...)
	}

	if len(paths) == 0 {
		return RefTouch{}, false, nil
	}

	root := checkedOut
	if root == "" {
		root = f.topOf(facts)
	}

	if root == "" {
		return RefTouch{}, false, nil
	}

	checkout, err := f.Canon.Domain(root, nil)
	if err != nil {
		return RefTouch{}, false, fmt.Errorf("canonicalizing the worktree %s: %w", root, err)
	}

	return RefTouch{Ref: u.Ref, New: u.New, Checkout: checkout.Checkout, Keys: keysOf(checkout.Checkout, paths)}, true, nil
}

func keysOf(root keys.Key, paths []string) []keys.Key {
	slices.Sort(paths)

	paths = slices.Compact(paths)
	out := make([]keys.Key, len(paths))

	for i, p := range paths {
		out[i] = keys.Key{
			Path:      filepath.Join(root.Path, filepath.FromSlash(p)),
			FoldCase:  root.FoldCase,
			Normalize: root.Normalize,
		}
	}

	return out
}

// resolve returns the commit a ref holds now, or "" when it holds none.
func (f *Fence) resolve(ref string) (string, error) {
	out, err := f.Git("", nil, "rev-parse", "--verify", "-q", ref+"^{commit}")
	if err != nil {
		if ExitCode(err) == 1 {
			return "", nil
		}

		return "", fmt.Errorf("reading %s: %w", ref, err)
	}

	return strings.TrimSpace(string(out)), nil
}

// baseline is the revision arguments that follow --not: what a rule may trust. added is for the
// commits a ref gains, and removed is for the commits it loses, which leave out the ref itself.
type baseline struct {
	added   []string
	removed []string
}

// localBaseline trusts every branch of every worktree and every remote-tracking ref.
func localBaseline(ref string) baseline {
	return baseline{
		added:   []string{"--branches", "--remotes"},
		removed: []string{"--exclude=" + strings.TrimPrefix(ref, "refs/heads/"), "--branches", "--remotes"},
	}
}

// commits lists the added and removed commits of one update.
func (f *Fence) commits(u Update, cur string, base baseline) ([]string, error) {
	var all []string

	if u.New != "" {
		added, err := f.revList(u, cur, append([]string{u.New, "--not"}, base.added...)...)
		if err != nil {
			return nil, err
		}

		all = append(all, added...)
	}

	if cur == "" || u.Ref == "HEAD" {
		return all, nil
	}

	args := []string{cur, "--not"}
	if u.New != "" {
		args = append(args, u.New)
	}

	removed, err := f.revList(u, cur, append(args, base.removed...)...)
	if err != nil {
		return nil, err
	}

	return append(all, removed...), nil
}

// RemoteTouched returns the repo-relative paths that pushing newSHA over oldSHA touches in ref, the
// way the remote will see it. Either value is empty for a creation or a deletion.
//
// A remote has no worktrees, so only the commit rule applies. The baseline is the branches the remote
// advertised for this push, never refs/remotes, since an unpruned remote-tracking ref would make
// unpushed commits look pushed. An advertised commit this clone does not hold is left out of the
// baseline, which only widens the touched set. A missing old or new value fails closed.
func (f *Fence) RemoteTouched(ref, oldSHA, newSHA string, heads map[string]string) ([]string, error) {
	var held []string

	if len(heads) > 0 {
		have, err := f.present(heads)
		if err != nil {
			return nil, err
		}

		held = have
	}

	base := baseline{added: held}

	for name, sha := range heads {
		if name != ref && slices.Contains(held, sha) {
			base.removed = append(base.removed, sha)
		}
	}

	slices.Sort(base.removed)

	base.removed = slices.Compact(base.removed)

	commits, err := f.commits(Update{Ref: ref, New: newSHA}, oldSHA, base)
	if err != nil {
		return nil, err
	}

	paths, err := f.diffCommits(commits)
	if err != nil {
		return nil, err
	}

	slices.Sort(paths)

	return slices.Compact(paths), nil
}

// present returns the distinct advertised commits this clone holds.
func (f *Fence) present(heads map[string]string) ([]string, error) {
	var ids []string

	for _, sha := range heads {
		ids = append(ids, sha)
	}

	slices.Sort(ids)

	ids = slices.Compact(ids)

	out, err := f.Git("", []byte(strings.Join(ids, "\n")+"\n"), "cat-file", "--batch-check=%(objectname) %(objecttype)")
	if err != nil {
		return nil, fmt.Errorf("checking the advertised commits: %w", err)
	}

	var held []string

	for _, line := range lines(out) {
		id, kind, _ := strings.Cut(line, " ")
		if kind == "commit" {
			held = append(held, id)
		}
	}

	return held, nil
}

// revList runs rev-list, and turns a failure into a missing-object error when a commit it needs is
// absent from this clone.
func (f *Fence) revList(u Update, cur string, args ...string) ([]string, error) {
	out, err := f.Git("", nil, append([]string{"rev-list"}, args...)...)
	if err == nil {
		return lines(out), nil
	}

	for _, id := range []string{cur, u.New} {
		if id == "" {
			continue
		}

		if _, cerr := f.Git("", nil, "cat-file", "-e", id+"^{commit}"); cerr != nil {
			return nil, &MissingObjectError{Ref: u.Ref, Object: id}
		}
	}

	return nil, fmt.Errorf("listing the commits of %s: %w", u.Ref, err)
}

// diffCommits returns the union of the paths the commits change. A merge counts only the paths
// that differ from every parent.
func (f *Fence) diffCommits(commits []string) ([]string, error) {
	if len(commits) == 0 {
		return nil, nil
	}

	stdin := []byte(strings.Join(commits, "\n") + "\n")

	out, err := f.Git("", stdin, "diff-tree", "--stdin", "-r", "-c", "-z", "--name-only", "--root", "--no-commit-id")
	if err != nil {
		return nil, fmt.Errorf("listing the paths of %d commits: %w", len(commits), err)
	}

	return nulFields(out), nil
}

func (f *Fence) treeDiff(old, next string) ([]string, error) {
	out, err := f.Git("", nil, "diff-tree", "-r", "-z", "--name-only", "--no-commit-id", old, next)
	if err != nil {
		return nil, fmt.Errorf("comparing the trees of %s and %s: %w", old, next, err)
	}

	return nulFields(out), nil
}

// indexMatches reports whether a worktree's index equals the tree of a commit. Any failure reads
// as "does not match", so the commit rule alone applies.
func (f *Fence) indexMatches(path, commit string) bool {
	_, err := f.GitIn(path, nil, "diff-index", "--cached", "--quiet", commit)

	return err == nil
}

// topOf returns the top of the worktree the hook runs in, or "" in a bare repository.
func (f *Fence) topOf(facts *repoFacts) string {
	if !facts.haveTop {
		facts.haveTop = true

		if out, err := f.Git("", nil, "rev-parse", "--show-toplevel"); err == nil {
			facts.top = strings.TrimSpace(string(out))
		}
	}

	return facts.top
}

func (f *Fence) commonOf(facts *repoFacts) string {
	if !facts.haveCommon {
		facts.haveCommon = true

		if out, err := f.Git("", nil, "rev-parse", "--path-format=absolute", "--git-common-dir"); err == nil {
			facts.common = strings.TrimSpace(string(out))
		}
	}

	return facts.common
}

// checkedOut returns the worktree that has the branch checked out, or "".
func (f *Fence) checkedOut(facts *repoFacts, ref string) (string, error) {
	if ref == "HEAD" {
		return "", nil
	}

	if !facts.haveWorktrees {
		facts.haveWorktrees = true

		out, err := f.Git("", nil, "worktree", "list", "--porcelain", "-z")
		if err != nil {
			return "", fmt.Errorf("listing the worktrees: %w", err)
		}

		facts.worktrees = parseWorktrees(out)
	}

	for _, w := range facts.worktrees {
		if !w.Bare && w.Branch == ref {
			return w.Path, nil
		}
	}

	return "", nil
}

// parseWorktrees reads `worktree list --porcelain -z`: NUL-terminated fields, and an empty field
// that ends each record.
func parseWorktrees(out []byte) []worktree {
	var (
		res []worktree
		cur worktree
	)

	flush := func() {
		if cur.Path != "" {
			res = append(res, cur)
		}

		cur = worktree{}
	}

	for _, field := range bytes.Split(out, []byte{0}) {
		key, value, _ := strings.Cut(string(field), " ")

		switch {
		case len(field) == 0:
			flush()
		case key == "worktree":
			cur.Path = value
		case key == "branch":
			cur.Branch = value
		case key == "bare":
			cur.Bare = true
		}
	}

	flush()

	return res
}

// prunedCopy reports whether a deletion only removes a loose copy of a ref that packed-refs holds
// at the same value. git does that after pack-refs, and the ref still resolves afterwards.
//
// A real deletion of a packed ref holds packed-refs.lock while the hook runs. A pruned loose copy
// does not.
func (f *Fence) prunedCopy(facts *repoFacts, ref, value string) bool {
	common := f.commonOf(facts)
	if common == "" || value == "" {
		return false
	}

	if _, err := os.Stat(filepath.Join(common, "packed-refs.lock")); !errors.Is(err, os.ErrNotExist) {
		return false
	}

	data, err := os.ReadFile(filepath.Join(common, "packed-refs"))
	if err != nil {
		return false
	}

	return bytes.Contains(data, []byte("\n"+value+" "+ref+"\n")) ||
		bytes.HasPrefix(data, []byte(value+" "+ref+"\n"))
}
