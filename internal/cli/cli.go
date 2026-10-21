// Package cli builds the nitipan command tree.
//
// The command layer receives every dependency from main, so tests inject the process table, the
// environment, and the state directory.
package cli

import (
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/proc"
	"github.com/space-armada/nitipan/internal/version"
)

// ErrNotImplemented marks a command whose behavior has not landed yet.
var ErrNotImplemented = errors.New("not implemented yet")

// Deps are the dependencies the commands share.
type Deps struct {
	Stdout io.Writer
	Stderr io.Writer

	// Getenv reads an environment variable.
	Getenv func(string) string

	// Procs reads the local process table.
	Procs proc.Table

	// StateDir resolves the state directory path. It runs only for commands that need the directory.
	StateDir func() (string, error)
}

// NewRoot builds the root command with every subcommand registered.
func NewRoot(deps Deps) *cobra.Command {
	root := &cobra.Command{
		Use:           "nitipan",
		Short:         "Claim a domain on this machine and see who holds what",
		Version:       version.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.SetOut(deps.Stdout)
	root.SetErr(deps.Stderr)

	// A flag mistake and a missing argument both exit with the usage status.
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return fmt.Errorf("%w: %w", exitcode.ErrUsage, err)
	})

	root.AddCommand(
		stub("acquire", "Claim a domain"),
		stub("release", "Give up a lease you hold"),
		stub("list", "Print every lease with its holder's liveness"),
		stub("break", "Remove another holder's lease"),
		stub("advertise", "Update your session name, permission mode, or a lease's description"),
		stub("setup", "Print the sandbox allowance entries for an agent"),
		hookStub(),
	)

	return root
}

// Execute runs the command tree and returns the process exit status. Errors go to stderr.
func Execute(deps Deps, args []string) int {
	root := NewRoot(deps)
	root.SetArgs(args)

	err := root.Execute()
	if err != nil {
		fmt.Fprintf(deps.Stderr, "nitipan: %v\n", err)
	}

	return exitcode.For(err)
}

func stub(name, short string) *cobra.Command {
	return &cobra.Command{
		Use:   name,
		Short: short,
		RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("%s: %w", name, ErrNotImplemented)
		},
	}
}

// hookStub registers the command that agent hooks and plugins call. It stays out of help and
// error messages, because only the entries `setup` prints should ever invoke it.
func hookStub() *cobra.Command {
	return &cobra.Command{
		Use:    "hook",
		Short:  "Called by agent hooks and plugins",
		Hidden: true,
		RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("this command is %w", ErrNotImplemented)
		},
	}
}
