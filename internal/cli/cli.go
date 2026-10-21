// Package cli builds the nitipan command tree.
//
// The command layer receives every dependency from main, so tests inject the process table, the
// environment, the user lookup, and the state directory.
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
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
	"github.com/space-armada/nitipan/internal/state"
	"github.com/space-armada/nitipan/internal/version"
)

// errNotImplemented marks a command whose behavior has not landed yet.
var errNotImplemented = errors.New("not implemented yet")

// Deps are the dependencies the commands share. A nil optional field takes its production default.
type Deps struct {
	Stdout io.Writer
	Stderr io.Writer

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

	// Finder finds checkout boundaries. Default: asks git.
	Finder keys.BoundaryFinder

	// StateOptions tunes the state directory, such as the lock retry budget in tests.
	StateOptions state.Options

	// OnCaller, when set, sees the caller each `list` resolved. Tests use it to check who a command
	// acts as.
	OnCaller func(adapter.Caller)

	// Hooks stop an operation between two writes. Only tests set them.
	Hooks lease.Hooks
}

func (d Deps) withDefaults() Deps {
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

	if d.Finder == nil {
		d.Finder = keys.NewGitBoundaries(keys.ExecGit)
	}

	return d
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
		a.setupCmd(),
		hookStub(),
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

// hookStub registers the command that agent hooks and plugins call. It stays out of help and
// error messages, because only the entries `setup` prints should ever invoke it.
func hookStub() *cobra.Command {
	return &cobra.Command{
		Use:    "hook",
		Short:  "Called by agent hooks and plugins",
		Hidden: true,
		RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("this command is %w", errNotImplemented)
		},
	}
}
