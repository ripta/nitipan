package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/space-armada/nitipan/internal/adapter"
	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/lease"
	"github.com/space-armada/nitipan/internal/liveness"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/session"
	"github.com/space-armada/nitipan/internal/setup"
	"github.com/space-armada/nitipan/internal/state"
)

// app holds what the commands share.
type app struct {
	deps Deps

	// command is the command line, recorded in the lock note so a busy lock can name its holder.
	command string

	// lockBudget overrides the lock retry budget when it is not zero. The hook command has 3 seconds
	// in all, so it waits for the lock at most 2.
	lockBudget time.Duration
}

func (a *app) resolver() *adapter.Resolver {
	return &adapter.Resolver{
		Adapters: adapter.All(),
		Procs:    a.deps.Procs,
		Getenv:   a.deps.Getenv,
		User:     a.deps.User,
	}
}

// holder identifies the calling agent session. A command that acts as a holder fails closed when it
// cannot.
func (a *app) holder() (adapter.Holder, error) {
	return a.resolver().Resolve().RequireHolder()
}

// engine opens the state directory and builds the engine over it.
func (a *app) engine() (*lease.Engine, error) {
	path, err := a.deps.StateDir()
	if err != nil {
		return nil, fmt.Errorf("resolving the state directory: %w", err)
	}

	local, err := a.deps.Machine()
	if err != nil {
		return nil, fmt.Errorf("identifying this machine: %w", err)
	}

	self := a.deps.Procs.Self()

	// The start is advisory, so a failed read leaves it empty. The lock note then reads as unverified.
	selfStart, _ := a.deps.Procs.Start(self)

	sessions := &session.Service{Root: path, Procs: a.deps.Procs, Local: local, Now: a.deps.Now, DB: a.opencodeDB()}
	checker := &liveness.Checker{Procs: a.deps.Procs, Local: local, Sessions: sessions.Rules()}

	opts := a.deps.StateOptions
	opts.Checker = checker
	opts.Procs = a.deps.Procs
	opts.Machine = local
	opts.SelfPID = self
	opts.SelfStart = selfStart
	opts.Command = a.command
	opts.AfterLock = sessions.Maintain
	opts.Warn = a.warn

	if a.lockBudget > 0 {
		opts.LockBudget = a.lockBudget
	}

	dir, err := state.Open(path, opts)
	if err != nil {
		return nil, err
	}

	e := lease.NewEngine(dir, checker, local, a.deps.Finder)
	e.Now = a.deps.Now
	e.Hooks = a.deps.Hooks
	e.Notify = lease.Notify{Delivered: a.deliverNotice, Warn: a.warn}
	e.Sessions = sessions

	return e, nil
}

// opencodeDB reads opencode's database, whose home comes from the user database like the state
// directory's does. A failed lookup leaves it nil, and every answer that needs it reads unknown.
func (a *app) opencodeDB() session.OpencodeDB {
	if a.deps.OpencodeDB != nil {
		return a.deps.OpencodeDB
	}

	home, err := a.deps.UserHome()
	if err != nil {
		return nil
	}

	return session.SqliteCLI{Path: filepath.Join(home, ".local", "share", "opencode", "opencode.db")}
}

// deliverNotice tells the broken holder what happened to its lease. It goes to stderr, because a
// command's stdout is its result.
func (a *app) deliverNotice(t *record.Tombstone) {
	if t.Kind != "" {
		fmt.Fprintf(a.deps.Stderr, "nitipan: notice: %s.\n", lease.DescribeNotice(t))

		return
	}

	fmt.Fprintf(a.deps.Stderr, "nitipan: notice: %s. The lease no longer exists.\n", lease.DescribeBroken(t))
}

// warn reports a failure that did not fail the call. A permission failure carries the sandbox text.
func (a *app) warn(err error) {
	fmt.Fprintf(a.deps.Stderr, "nitipan: warning: %v\n", a.wrapErr(err))
}

// domain turns the checkout and scope flags into a lease domain. A relative checkout is joined to the
// working directory.
func (a *app) domain(cmd *cobra.Command) (lease.Domain, error) {
	checkout, err := cmd.Flags().GetString("checkout")
	if err != nil {
		return lease.Domain{}, fmt.Errorf("reading --checkout: %w", err)
	}

	scopes, err := cmd.Flags().GetStringArray("scope")
	if err != nil {
		return lease.Domain{}, fmt.Errorf("reading --scope: %w", err)
	}

	if checkout == "" || !filepath.IsAbs(checkout) {
		cwd, err := a.deps.Cwd()
		if err != nil {
			return lease.Domain{}, fmt.Errorf("reading the working directory: %w", err)
		}

		checkout = filepath.Join(cwd, checkout)
	}

	return lease.Domain{Checkout: checkout, Scopes: scopes}, nil
}

func addDomainFlags(cmd *cobra.Command, what string) {
	cmd.Flags().String("checkout", "", "checkout "+what+" (default: the working directory)")
	cmd.Flags().StringArray("scope", nil, "path scope inside the checkout; repeat for several")
}

// domainGiven reports whether either domain flag was passed.
func domainGiven(cmd *cobra.Command) bool {
	return cmd.Flags().Changed("checkout") || cmd.Flags().Changed("scope")
}

// optString returns the flag's value, or nil when the flag was left out so the stored value stays.
func optString(cmd *cobra.Command, name string) *string {
	if !cmd.Flags().Changed(name) {
		return nil
	}

	v, _ := cmd.Flags().GetString(name)

	return &v
}

func noArgs(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("%w: %s takes no arguments, got %q", exitcode.ErrUsage, cmd.Name(), args)
	}

	return nil
}

// wrapErr adds the sandbox text to a permission failure. A blocked write gives the same errno as a
// plain file-mode problem, so the message says "likely".
func (a *app) wrapErr(err error) error {
	var gate *session.GateError
	if errors.As(err, &gate) {
		return &gateError{err: err, text: a.setupText(gate.Agent)}
	}

	if err == nil || !errors.Is(err, fs.ErrPermission) {
		return err
	}

	return &sandboxError{err: err, text: a.sandboxText()}
}

// setupInput gathers what the setup text names: the state directory and this nitipan's own path.
func (a *app) setupInput() (setup.Input, error) {
	dir, err := a.deps.StateDir()
	if err != nil {
		return setup.Input{}, err
	}

	exe, err := a.deps.Executable()
	if err != nil || exe == "" {
		exe = "nitipan"
	}

	return setup.Input{StateDir: dir, Executable: exe}, nil
}

// setupText is what `nitipan setup <agent>` prints, or empty when it cannot be built.
func (a *app) setupText(agent string) string {
	in, err := a.setupInput()
	if err != nil {
		return ""
	}

	text, err := setup.Text(agent, in)
	if err != nil {
		return ""
	}

	return text
}

func (a *app) sandboxText() string {
	in, err := a.setupInput()
	if err != nil {
		return ""
	}

	res := a.resolver().Resolve()
	if res.Holder == nil {
		return fmt.Sprintf("No supported agent was detected. Run nitipan setup with the agent's name (%s) for the "+
			"entries it needs for %s.", strings.Join(setup.Agents(), ", "), in.StateDir)
	}

	text, err := setup.Text(res.Holder.Adapter.Name, in)
	if err != nil {
		return ""
	}

	return fmt.Sprintf("Detected agent: %s.\n\n%s", res.Holder.Adapter.Name, text)
}

// gateError is a refused acquire with the entries that would let it through.
type gateError struct {
	err  error
	text string
}

// Error implements error.
func (e *gateError) Error() string {
	if e.text == "" {
		return e.err.Error()
	}

	return e.err.Error() + "\n\n" + strings.TrimRight(e.text, "\n")
}

// Unwrap exposes the gate error.
func (e *gateError) Unwrap() error { return e.err }

// sandboxError is a permission failure with the entries that would fix it.
type sandboxError struct {
	err  error
	text string
}

// Error implements error.
func (e *sandboxError) Error() string {
	msg := fmt.Sprintf("%v\nThe agent's sandbox likely blocked access to the state directory.", e.err)
	if e.text == "" {
		return msg
	}

	return msg + "\n\n" + strings.TrimRight(e.text, "\n")
}

// Unwrap exposes the permission error.
func (e *sandboxError) Unwrap() error { return e.err }
