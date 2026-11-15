package setup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/space-armada/nitipan/internal/gitfence"
)

// hookMarker opens every file nitipan writes into a hooks directory. Install, uninstall, and the
// acquire check tell nitipan's hooks from anyone else's by it.
const hookMarker = "# nitipan-managed hook"

// refTransaction is the hook that judges ref updates against the leases of one machine.
const refTransaction = "reference-transaction"

// prePush is the hook that refuses a push of a fenced branch that lacks its fence refs, and any push
// while the clone's fence refs differ from the remote's.
const prePush = "pre-push"

// hookNames are the hooks nitipan's checks run in.
var hookNames = []string{refTransaction, prePush}

// chainedSuffix names where an existing hook goes when nitipan's takes its place.
const chainedSuffix = ".nitipan-orig"

// trackedDirName is the directory under the git common directory that holds nitipan's hooks when
// the checkout's own hooks directory is tracked by git.
const trackedDirName = "nitipan-hooks"

// priorFile records the checkout's local core.hooksPath from before the install.
const priorFile = "prior-hooks-path"

// gitHookNames are the hooks git documents. A tracked hooks directory is chained hook by hook,
// since a local core.hooksPath replaces the whole directory. A name git adds later is not chained
// until nitipan learns it.
var gitHookNames = []string{
	"applypatch-msg", "pre-applypatch", "post-applypatch", "pre-commit", "pre-merge-commit", "prepare-commit-msg",
	"commit-msg", "post-commit", "pre-rebase", "post-checkout", "post-merge", "pre-push", "pre-receive", "update",
	"proc-receive", "post-receive", "post-update", "reference-transaction", "push-to-checkout", "pre-auto-gc",
	"post-rewrite", "sendemail-validate", "fsmonitor-watchman", "p4-changelist", "p4-prepare-changelist",
	"p4-post-changelist", "p4-pre-submit", "post-index-change",
}

// HooksStatus says whether a path lies in a git checkout and whether nitipan's hook runs there.
type HooksStatus struct {
	// Checkout is the top of the git checkout. It is empty for a path outside any checkout.
	Checkout string

	InForce bool

	// Why says what is missing.
	Why string
}

// HooksInForce reports whether git runs both of nitipan's hooks, reference-transaction and pre-push,
// for the checkout that holds path. In force means the directory git uses for hooks, which honors a
// local core.hooksPath, holds nitipan's executable script for each. Why joins what is missing. A path outside any checkout is reported with an empty Checkout.
// Only "not a git repository" and a missing git binary count as outside. Any other failure to ask
// git, such as a timeout or an unsafe directory, is an error.
func HooksInForce(run gitfence.Runner, path string) (HooksStatus, error) {
	top, err := topOf(run, ExistingDir(path))
	if errors.Is(err, errOutside) {
		return HooksStatus{}, nil
	}

	if err != nil {
		return HooksStatus{}, err
	}

	st := HooksStatus{Checkout: top}

	dir, err := hooksDir(run, top)
	if err != nil {
		st.Why = err.Error()

		return st, nil
	}

	var why []string

	for _, name := range hookNames {
		if reason := hookProblem(dir, name); reason != "" {
			why = append(why, reason)
		}
	}

	st.Why = strings.Join(why, "; ")
	st.InForce = len(why) == 0

	return st, nil
}

// hookProblem says what is wrong with one of nitipan's hooks in a directory, or returns "".
func hookProblem(dir, name string) string {
	file := filepath.Join(dir, name)

	info, err := os.Stat(file)

	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Sprintf("there is no %s hook in %s", name, dir)
	case err != nil:
		return fmt.Sprintf("cannot read %s: %v", file, err)
	case info.Mode()&0o111 == 0:
		return fmt.Sprintf("%s is not executable, so git skips it", file)
	case !ownsHook(file):
		return fmt.Sprintf("the %s hook in %s is not nitipan's", name, dir)
	}

	return ""
}

// ExistingDir returns the nearest ancestor of path that is a directory, since a scope may name a
// path that does not exist yet.
func ExistingDir(path string) string {
	cur := path

	for {
		if info, err := os.Stat(cur); err == nil && info.IsDir() {
			return cur
		}

		parent := filepath.Dir(cur)
		if parent == cur {
			return cur
		}

		cur = parent
	}
}

// errOutside means a directory is not inside a git checkout: git says it is no repository, or git
// is not installed so no hook can run. Any other failure to ask git is a real error.
var errOutside = errors.New("not inside a git checkout")

func topOf(run gitfence.Runner, dir string) (string, error) {
	out, err := run(dir, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		var failed *gitfence.GitError

		switch {
		case errors.Is(err, exec.ErrNotFound):
			return "", errOutside
		case errors.As(err, &failed) && strings.Contains(failed.Stderr, "not a git repository"):
			return "", errOutside
		case errors.As(err, &failed) && strings.Contains(failed.Stderr, "must be run in a work tree"):
			return "", errOutside
		}

		return "", fmt.Errorf("asking git about %s: %w", dir, err)
	}

	top := strings.TrimSpace(string(out))
	if top == "" {
		return "", errOutside
	}

	return top, nil
}

func hooksDir(run gitfence.Runner, top string) (string, error) {
	out, err := run(top, nil, "rev-parse", "--path-format=absolute", "--git-path", "hooks")
	if err != nil {
		return "", fmt.Errorf("asking git for the hooks directory of %s: %w", top, err)
	}

	return strings.TrimSpace(string(out)), nil
}

func commonDir(run gitfence.Runner, top string) (string, error) {
	out, err := run(top, nil, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("asking git for the git directory of %s: %w", top, err)
	}

	return strings.TrimSpace(string(out)), nil
}

func ownsHook(file string) bool {
	data, err := os.ReadFile(file)

	return err == nil && strings.Contains(string(data), hookMarker)
}

// InstallGit installs nitipan's reference-transaction hook in the checkout that holds dir, and in
// every initialized submodule below it.
//
// The hook goes into the directory git uses, so a linked worktree shares it. An existing hook of
// the same name is kept beside it and chained. When that directory holds files git tracks, nothing
// there is written. The hooks go into a directory under the git directory, the checkout's local
// core.hooksPath points at it, and every hook git documents chains to the tracked directory.
func InstallGit(run gitfence.Runner, dir, executable string) ([]Installed, error) {
	if !filepath.IsAbs(executable) {
		return nil, fmt.Errorf("nitipan path %q: want an absolute path", executable)
	}

	return eachCheckout(run, dir, func(top string) ([]Installed, error) {
		return installCheckout(run, top, executable)
	})
}

// UninstallGit removes what InstallGit wrote, restores the hooks it chained, and resets the local
// core.hooksPath it changed.
func UninstallGit(run gitfence.Runner, dir string) ([]Installed, error) {
	return eachCheckout(run, dir, func(top string) ([]Installed, error) {
		return uninstallCheckout(run, top)
	})
}

// eachCheckout runs fn for the checkout that holds dir, then for each initialized submodule, deepest
// last.
func eachCheckout(run gitfence.Runner, dir string, fn func(top string) ([]Installed, error)) ([]Installed, error) {
	top, err := topOf(run, dir)
	if errors.Is(err, errOutside) {
		return nil, fmt.Errorf("%s is not inside a git checkout", dir)
	}

	if err != nil {
		return nil, err
	}

	subs, err := submodules(run, top)
	if err != nil {
		return nil, err
	}

	var all []Installed

	for _, t := range append([]string{top}, subs...) {
		done, err := fn(t)
		if err != nil {
			return all, err
		}

		all = append(all, done...)
	}

	return all, nil
}

// submodules lists the initialized submodules below a checkout, nested ones included.
func submodules(run gitfence.Runner, top string) ([]string, error) {
	out, err := run(top, nil, "submodule", "--quiet", "foreach", "--recursive", `printf '%s\n' "$toplevel/$sm_path"`)
	if err != nil {
		return nil, fmt.Errorf("listing the submodules of %s: %w", top, err)
	}

	var subs []string

	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			subs = append(subs, l)
		}
	}

	return subs, nil
}

func installCheckout(run gitfence.Runner, top, executable string) ([]Installed, error) {
	common, err := commonDir(run, top)
	if err != nil {
		return nil, err
	}

	own := filepath.Join(common, trackedDirName)

	if prior, err := os.ReadFile(filepath.Join(own, priorFile)); err == nil {
		return installTracked(run, top, own, priorTracked(run, top, own, common, string(prior)), executable)
	}

	dir, err := hooksDir(run, top)
	if err != nil {
		return nil, err
	}

	outside, err := outsideGitDir(common, dir)
	if err != nil {
		return nil, err
	}

	if outside {
		return installTracked(run, top, own, configuredHooksPath(run, top, own, dir), executable)
	}

	var done []Installed

	for _, name := range hookNames {
		script, err := renderHook(name, executable, `"$here/`+name+chainedSuffix+`"`)
		if err != nil {
			return done, err
		}

		wrote, err := writeHook(dir, name, script)
		if err != nil {
			return done, err
		}

		done = append(done, wrote...)
	}

	return done, nil
}

// renderHook renders one of nitipan's hooks.
func renderHook(name, executable, chained string) ([]byte, error) {
	if name == prePush {
		return prePushScript(executable, chained)
	}

	return hookScript(executable, chained)
}

// priorTracked finds the hooks directory an earlier install chained to. The prior-hooks-path file
// holds "unset" when the local core.hooksPath was not set, and then another scope named the
// directory.
func priorTracked(run gitfence.Runner, top, own, common, content string) string {
	if value, set := strings.CutPrefix(strings.TrimSpace(content), "set "); set {
		return expandPath(run, top, value)
	}

	return configuredHooksPath(run, top, own, filepath.Join(common, "hooks"))
}

// expandPath expands a leading tilde the way git does for a path-typed value, which the shell does
// not do inside the quotes of a chain stub. A relative value stays relative. When git cannot
// expand it, the value is returned as written.
func expandPath(run gitfence.Runner, top, value string) string {
	out, err := run(top, nil, "config", "--type=path", "--default", value, "--get", "nitipan.unset")
	if err != nil {
		return value
	}

	if expanded := strings.TrimSuffix(string(out), "\n"); expanded != "" {
		return expanded
	}

	return value
}

// configuredHooksPath returns the core.hooksPath that applies apart from nitipan's own, with a
// leading tilde expanded. A relative value is relative to each worktree and must stay that way.
// Without one it returns fallback.
func configuredHooksPath(run gitfence.Runner, top, own, fallback string) string {
	out, err := run(top, nil, "config", "--type=path", "--show-scope", "--get-all", "core.hooksPath")
	if err != nil {
		return fallback
	}

	found := fallback

	for _, line := range strings.Split(string(out), "\n") {
		_, value, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if ok && value != "" && value != own {
			found = value
		}
	}

	return found
}

// outsideGitDir reports whether a hooks directory lies outside the repository's git directory. Such
// a directory is either tracked by the checkout or shared with other repositories, and nitipan
// writes into neither.
func outsideGitDir(common, dir string) (bool, error) {
	realCommon, err := filepath.EvalSymlinks(common)
	if err != nil {
		return false, fmt.Errorf("resolving %s: %w", common, err)
	}

	realDir, err := filepath.EvalSymlinks(dir)
	if errors.Is(err, fs.ErrNotExist) {
		realDir = dir
	} else if err != nil {
		return false, fmt.Errorf("resolving %s: %w", dir, err)
	}

	rel, err := filepath.Rel(realCommon, realDir)
	if err != nil {
		return true, nil
	}

	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)), nil
}

// installTracked writes nitipan's hooks under the git directory and points the checkout's local
// core.hooksPath at them. tracked is the directory the checkout used before, as written.
func installTracked(run gitfence.Runner, top, own, tracked, executable string) ([]Installed, error) {
	var done []Installed

	_, statErr := os.Stat(filepath.Join(own, priorFile))
	if statErr != nil {
		prior, err := rawLocalHooksPath(run, top)
		if err != nil {
			return nil, err
		}

		if err := os.MkdirAll(own, 0o755); err != nil {
			return nil, fmt.Errorf("creating %s: %w", own, err)
		}

		if err := writeAtomicMode(filepath.Join(own, priorFile), []byte(prior+"\n"), 0o644); err != nil {
			return nil, err
		}
	}

	for _, name := range hookNames {
		script, err := renderHook(name, executable, shellQuote(filepath.ToSlash(tracked)+"/"+name))
		if err != nil {
			return nil, err
		}

		wrote, err := writeHook(own, name, script)
		if err != nil {
			return nil, err
		}

		done = append(done, wrote...)
	}

	for _, name := range gitHookNames {
		if slices.Contains(hookNames, name) {
			continue
		}

		stub := chainScript(shellQuote(filepath.ToSlash(tracked) + "/" + name))

		more, err := writeHook(own, name, stub)
		if err != nil {
			return done, err
		}

		done = append(done, more...)
	}

	if _, err := run(top, nil, "config", "--local", "core.hooksPath", own); err != nil {
		return done, fmt.Errorf("pointing core.hooksPath of %s at %s: %w", top, own, err)
	}

	return done, nil
}

// rawLocalHooksPath returns the prior state in the prior-hooks-path file's form.
func rawLocalHooksPath(run gitfence.Runner, top string) (string, error) {
	out, err := run(top, nil, "config", "--local", "--get", "core.hooksPath")
	if err != nil {
		if gitfence.ExitCode(err) == 1 {
			return "unset", nil
		}

		return "", fmt.Errorf("reading core.hooksPath of %s: %w", top, err)
	}

	return "set " + strings.TrimSpace(string(out)), nil
}

func uninstallCheckout(run gitfence.Runner, top string) ([]Installed, error) {
	common, err := commonDir(run, top)
	if err != nil {
		return nil, err
	}

	own := filepath.Join(common, trackedDirName)

	if prior, err := os.ReadFile(filepath.Join(own, priorFile)); err == nil {
		return uninstallTracked(run, top, own, strings.TrimSpace(string(prior)))
	}

	dir, err := hooksDir(run, top)
	if err != nil {
		return nil, err
	}

	var done []Installed

	for _, name := range hookNames {
		removed, err := removeHook(dir, name)
		if err != nil {
			return done, err
		}

		done = append(done, removed...)
	}

	return done, nil
}

func uninstallTracked(run gitfence.Runner, top, own, prior string) ([]Installed, error) {
	var done []Installed

	if value, set := strings.CutPrefix(prior, "set "); set {
		if _, err := run(top, nil, "config", "--local", "core.hooksPath", value); err != nil {
			return nil, fmt.Errorf("restoring core.hooksPath of %s: %w", top, err)
		}

		done = append(done, Installed{Path: top, Action: "restored", Note: "core.hooksPath to " + value})
	} else if _, err := run(top, nil, "config", "--local", "--unset", "core.hooksPath"); err != nil {
		return nil, fmt.Errorf("unsetting core.hooksPath of %s: %w", top, err)
	} else {
		done = append(done, Installed{Path: top, Action: "unset", Note: "core.hooksPath"})
	}

	for _, name := range gitHookNames {
		removed, err := removeHook(own, name)
		if err != nil {
			return done, err
		}

		done = append(done, removed...)
	}

	if err := os.Remove(filepath.Join(own, priorFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return done, fmt.Errorf("removing %s: %w", filepath.Join(own, priorFile), err)
	}

	if err := os.Remove(own); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return done, fmt.Errorf("removing %s: %w", own, err)
	}

	return done, nil
}

// writeHook writes a hook script, checking before it writes. An existing hook that is not
// nitipan's is kept beside it under chainedSuffix, and the script chains to it.
func writeHook(dir, name string, script []byte) ([]Installed, error) {
	dest := filepath.Join(dir, name)

	existing, err := os.ReadFile(dest)

	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, fmt.Errorf("reading %s: %w", dest, err)
	case strings.Contains(string(existing), hookMarker):
		if string(existing) == string(script) {
			return []Installed{{Path: dest, Action: "unchanged"}}, nil
		}

		if err := writeAtomicMode(dest, script, 0o755); err != nil {
			return nil, err
		}

		return []Installed{{Path: dest, Action: "replaced"}}, nil
	default:
		return keepAndWrite(dir, name, script)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating %s: %w", dir, err)
	}

	if err := writeAtomicMode(dest, script, 0o755); err != nil {
		return nil, err
	}

	return []Installed{{Path: dest, Action: "wrote"}}, nil
}

// keepAndWrite moves a hook that is not nitipan's aside and puts nitipan's script in its place. The
// script chains to the kept hook, but only the scripts of hookNames do, so any other name is
// refused here.
func keepAndWrite(dir, name string, script []byte) ([]Installed, error) {
	dest := filepath.Join(dir, name)
	aside := dest + chainedSuffix

	if !slices.Contains(hookNames, name) {
		return nil, fmt.Errorf("%s exists and is not nitipan's; nitipan does not replace it", dest)
	}

	if _, err := os.Lstat(aside); err == nil {
		return nil, fmt.Errorf("%s exists and is not nitipan's, and %s is already taken; resolve it by hand", dest, aside)
	}

	if err := os.Rename(dest, aside); err != nil {
		return nil, fmt.Errorf("keeping %s as %s: %w", dest, aside, err)
	}

	if err := writeAtomicMode(dest, script, 0o755); err != nil {
		return nil, err
	}

	return []Installed{{Path: dest, Action: "wrote", Note: "kept the existing hook as " + aside + " and chained to it"}}, nil
}

// removeHook removes nitipan's hook and puts back the hook it chained. A hook that is not nitipan's
// stays.
func removeHook(dir, name string) ([]Installed, error) {
	dest := filepath.Join(dir, name)

	if !ownsHook(dest) {
		return nil, nil
	}

	if err := os.Remove(dest); err != nil {
		return nil, fmt.Errorf("removing %s: %w", dest, err)
	}

	done := []Installed{{Path: dest, Action: "removed"}}
	aside := dest + chainedSuffix

	if _, err := os.Lstat(aside); err == nil {
		if err := os.Rename(aside, dest); err != nil {
			return done, fmt.Errorf("restoring %s: %w", dest, err)
		}

		done = append(done, Installed{Path: dest, Action: "restored", Note: "the hook that was kept as " + aside})
	}

	return done, nil
}

// shellQuote single-quotes a word for the hook scripts.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// chainScript is the hook that passes straight through to a tracked hook of the same name.
func chainScript(target string) []byte {
	return []byte("#!/bin/sh\n" + hookMarker + ": chains to a tracked hooks directory\n" +
		"hook=" + target + "\n" +
		"if [ -x \"$hook\" ]; then\n\texec \"$hook\" \"$@\"\nfi\n\nexit 0\n")
}

// GitText is what `nitipan setup git` prints when it is given neither flag.
func GitText() string {
	return `nitipan's git hooks guard two things. The reference-transaction hook refuses a branch or HEAD update
that touches paths another session holds, or paths a broken lease used to cover. It covers rebase,
reset, update-ref, and branch -f along with commit, and --no-verify does not skip it.

The pre-push hook refuses a push of a fenced branch that lacks the fence refs that nitipan push writes.
It also refuses any update of a fence ref that is not a creation or a fast-forward, which stops a
mirror push from deleting them. --no-verify skips it.

A caller that is no agent, such as your own terminal, is never refused.

  nitipan setup git --install <checkout>     install both hooks in the checkout and its submodules
  nitipan setup git --uninstall <checkout>   remove them, and put back what they chained

A path claim inside a git checkout is refused until both hooks are in force there.
`
}
