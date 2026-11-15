// Package cli builds the nitipan command tree.
//
// The command layer receives every dependency from main, so tests inject the process table, the
// environment, the user lookup, and the state directory.
package cli

import (
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/space-armada/nitipan/internal/adapter"
	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/keys"
	"github.com/space-armada/nitipan/internal/lease"
	"github.com/space-armada/nitipan/internal/machine"
	"github.com/space-armada/nitipan/internal/proc"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/session"
	"github.com/space-armada/nitipan/internal/state"
	"github.com/space-armada/nitipan/internal/version"
)

// Deps are the dependencies the commands share. A nil optional field takes its production default.
type Deps struct {
	Stdout io.Writer
	Stderr io.Writer

	// Stdin carries the input of the hook command. Nil reads as empty.
	Stdin io.Reader

	// Getenv reads an environment variable.
	Getenv func(string) string

	// Procs reads the local process table.
	Procs proc.Table

	// StateDir resolves the state directory path. It runs only for commands that need the directory.
	StateDir func() (string, error)

	// Machine names this machine. Default: the host name and machine ID.
	Machine func() (record.Machine, error)

	// User returns the OS user running the process. Default: the user database entry.
	User func() (string, error)

	// Cwd returns the working directory, the default checkout. Default: os.Getwd.
	Cwd func() (string, error)

	// Now returns the current time. Default: time.Now.
	Now func() time.Time

	// Sleep pauses between the attempts of `acquire --wait`. Default: time.Sleep. A test that injects
	// Now and Sleep together runs a wait on a fake clock.
	Sleep func(time.Duration)

	// Finder finds checkout boundaries. Default: asks git.
	Finder keys.BoundaryFinder

	// StateOptions tunes the state directory, such as the lock retry budget in tests.
	StateOptions state.Options

	// OnCaller, when set, sees the caller each `list` resolved. Tests use it to check who a command
	// acts as.
	OnCaller func(adapter.Caller)

	// Hooks stop an operation between two writes. Only tests set them.
	Hooks lease.Hooks

	// Executable returns the absolute path of this nitipan with symlinks resolved. Hook entries and
	// plugins run nitipan by that path. Default: os.Executable.
	Executable func() (string, error)

	// UserHome returns the home directory from the user database. It locates opencode's database and
	// the default Codex home. Like the state directory, it never reads $HOME. Default: the user
	// database entry.
	UserHome func() (string, error)

	// OpencodeDB reads opencode's database. Nil reads it with the sqlite3 program.
	OpencodeDB session.OpencodeDB
}

func (d Deps) withDefaults() Deps {
	if d.Executable == nil {
		d.Executable = executablePath
	}

	if d.UserHome == nil {
		d.UserHome = userHome
	}

	if d.Machine == nil {
		d.Machine = machine.Local
	}

	if d.User == nil {
		d.User = currentUser
	}

	if d.Cwd == nil {
		d.Cwd = os.Getwd
	}

	if d.Now == nil {
		d.Now = time.Now
	}

	if d.Sleep == nil {
		d.Sleep = time.Sleep
	}

	if d.Finder == nil {
		d.Finder = keys.NewGitBoundaries(keys.ExecGit)
	}

	return d
}

func executablePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("finding the nitipan executable: %w", err)
	}

	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", fmt.Errorf("resolving the nitipan executable %s: %w", exe, err)
	}

	return resolved, nil
}

func userHome() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("reading the current user: %w", err)
	}

	if u.HomeDir == "" || !filepath.IsAbs(u.HomeDir) {
		return "", fmt.Errorf("user database gives %q as the home directory, want an absolute path", u.HomeDir)
	}

	return u.HomeDir, nil
}

func currentUser() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("reading the current user: %w", err)
	}

	return u.Username, nil
}

// NewRoot builds the root command with every subcommand registered.
func NewRoot(deps Deps) *cobra.Command {
	return newRoot(&app{deps: deps.withDefaults()})
}

func newRoot(a *app) *cobra.Command {
	root := &cobra.Command{
		Use:           "nitipan",
		Short:         "Claim a domain on this machine and see who holds what",
		Version:       version.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.SetOut(a.deps.Stdout)
	root.SetErr(a.deps.Stderr)

	// A flag mistake and a missing argument both exit with the usage status.
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return fmt.Errorf("%w: %w", exitcode.ErrUsage, err)
	})

	root.AddCommand(
		a.acquireCmd(),
		a.releaseCmd(),
		a.listCmd(),
		a.breakCmd(),
		a.advertiseCmd(),
		a.requestCmd(),
		a.grantCmd(),
		a.declineCmd(),
		a.revokeCmd(),
		a.setupCmd(),
		a.hookCmd(),
	)

	return root
}

// Execute runs the command tree and returns the process exit status. Errors go to stderr.
func Execute(deps Deps, args []string) int {
	a := &app{deps: deps.withDefaults(), command: "nitipan " + strings.Join(args, " ")}

	root := newRoot(a)
	root.SetArgs(args)

	err := root.Execute()
	if err != nil {
		fmt.Fprintf(deps.Stderr, "nitipan: %v\n", err)
	}

	return exitcode.For(err)
}
