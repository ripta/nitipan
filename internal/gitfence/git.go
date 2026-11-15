// Package gitfence works out which paths a git ref update touches, from inside a
// reference-transaction hook.
//
// The hook runs in git's own environment. The runner in this package keeps that environment on
// purpose, because a quarantined object directory or a linked worktree's git directory is only
// visible through it.
package gitfence

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// Runner runs git and returns its standard output. An empty dir is the process's working
// directory. A non-nil stdin is fed to the command.
type Runner func(dir string, stdin []byte, args ...string) ([]byte, error)

const (
	hookTimeout  = 2 * time.Minute
	cleanTimeout = 10 * time.Second
)

// Exec runs git with the caller's environment. A reference-transaction hook uses it.
func Exec(dir string, stdin []byte, args ...string) ([]byte, error) {
	return runGit(hookTimeout, os.Environ(), dir, stdin, args)
}

// ExecClean runs git without the variables that point it at another repository. Setup and the
// acquire gate use it, since they ask about a directory and not about the transaction in flight.
func ExecClean(dir string, stdin []byte, args ...string) ([]byte, error) {
	return runGit(cleanTimeout, cleanEnv(os.Environ()), dir, stdin, args)
}

// ExecWith returns a runner that adds environment entries to the process's own. A later entry wins
// over an earlier one of the same name. With clean set, the variables that point git at another
// repository are dropped first, as ExecClean does.
//
// The timeout is the hook's in both modes. Callers run network and history-walking commands through
// it, such as ls-remote, fetch, and rev-list over a long first push. Cleaning the environment says
// where git looks and has no bearing on how long a command may take. A command with a tighter bound
// uses ExecClean.
func ExecWith(clean bool, extra ...string) Runner {
	return func(dir string, stdin []byte, args ...string) ([]byte, error) {
		env := os.Environ()

		if clean {
			env = cleanEnv(env)
		}

		return runGit(hookTimeout, append(slices.Clone(env), extra...), dir, stdin, args)
	}
}

// ExecWithout is ExecWith for a caller that must not pass named variables on. The listed variables
// are dropped from the process's environment before git starts.
func ExecWithout(clean bool, drop ...string) Runner {
	return func(dir string, stdin []byte, args ...string) ([]byte, error) {
		env := os.Environ()

		if clean {
			env = cleanEnv(env)
		}

		env = slices.DeleteFunc(slices.Clone(env), func(kv string) bool {
			name, _, _ := strings.Cut(kv, "=")

			return slices.Contains(drop, name)
		})

		return runGit(hookTimeout, env, dir, stdin, args)
	}
}

// runGit is the seam a test replaces to see which timeout a runner passes.
var runGit = run

// repoVars are the variables that locate a repository, an index, or a worktree. A command that
// runs in another worktree must not inherit them.
var repoVars = []string{
	"GIT_DIR",
	"GIT_WORK_TREE",
	"GIT_INDEX_FILE",
	"GIT_COMMON_DIR",
	"GIT_PREFIX",
	"GIT_CEILING_DIRECTORIES",
}

func cleanEnv(env []string) []string {
	out := make([]string, 0, len(env))

	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if slices.Contains(repoVars, name) {
			continue
		}

		out = append(out, kv)
	}

	return out
}

// run asks git for its untranslated messages. Callers classify some failures by the English text
// on standard error, such as "not a git repository". Nothing else about git's environment changes.
func run(timeout time.Duration, env []string, dir string, stdin []byte, args []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(slices.Clone(env), "LC_ALL=C", "LANGUAGE=C")

	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		return out, &GitError{Args: args, Err: err, Stderr: strings.TrimSpace(stderr.String())}
	}

	return out, nil
}

// GitError is a failed git command with what it printed to standard error.
type GitError struct {
	Args   []string
	Err    error
	Stderr string
}

// Error implements error.
func (e *GitError) Error() string {
	msg := fmt.Sprintf("git %s: %v", strings.Join(e.Args, " "), e.Err)
	if e.Stderr != "" {
		msg += ": " + e.Stderr
	}

	return msg
}

// Unwrap exposes the exec error.
func (e *GitError) Unwrap() error { return e.Err }

// ExitCode returns the exit status of a failed git command, or -1 when the error is not one.
func ExitCode(err error) int {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}

	return -1
}

func lines(out []byte) []string {
	var res []string

	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			res = append(res, l)
		}
	}

	return res
}

func nulFields(out []byte) []string {
	var res []string

	for _, f := range bytes.Split(out, []byte{0}) {
		if len(f) > 0 {
			res = append(res, string(f))
		}
	}

	return res
}
