package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/space-armada/nitipan/internal/adapter"
	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/session"
	"github.com/space-armada/nitipan/internal/state"
)

// hookLockBudget is how long a hook waits for the lock. Codex gives a hook 3 seconds in all.
const hookLockBudget = 2 * time.Second

// hookInputLimit bounds the input a hook reads. The input comes from an agent, and a shell command in
// a session can reach the opencode plugin's side of it.
const hookInputLimit = 1 << 20

// hookInput is what a hook or plugin sends on standard input. Codex sends more fields than these,
// and they are ignored.
type hookInput struct {
	SessionID string `json:"session_id"`
	Present   *bool  `json:"present"`
	Executing *bool  `json:"executing"`
}

// hookCmd registers the commands that agent hooks and plugins call, and the one that the git hook
// calls. They stay out of help and out of every error message, because only the entries `setup`
// prints, and the script `setup git` installs, should ever invoke them.
//
// Each but the git one reads its session ID from its input and never from its environment. Each
// finds its host with the process walk and fails closed when the walk finds no matching agent. None
// delivers or removes a tombstone, because nobody reads a hook's output. The git one walks only when
// agent variables are set, and passes the user's own terminal.
func (a *app) hookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "hook",
		Short:  "Called by agent hooks and plugins",
		Hidden: true,
		Args: func(*cobra.Command, []string) error {
			return fmt.Errorf("%w: unrecognized arguments", exitcode.ErrUsage)
		},
		RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("%w: nothing to run", exitcode.ErrUsage)
		},
	}

	cmd.AddCommand(
		a.hookSub("session-start", a.runSessionStart),
		a.hookSub("session-end", a.runSessionEnd),
		a.hookSub("presence", a.runPresence),
		a.hookSub("execution", a.runExecution),
		a.hookRefTransaction(),
	)

	return cmd
}

func (a *app) hookSub(name string, run func(hookInput, adapter.Holder, []string) error) *cobra.Command {
	return &cobra.Command{
		Use:    name,
		Hidden: true,
		Args: func(*cobra.Command, []string) error {
			return nil
		},
		RunE: func(*cobra.Command, []string) error {
			in, err := a.readInput()
			if err != nil {
				return err
			}

			if !session.ValidSession(in.SessionID) {
				return errors.New("the session ID in the input is missing or outside the allowed set")
			}

			host, err := a.resolver().Host()
			if err != nil {
				return err
			}

			args, err := a.deps.Procs.Args(host.PID)
			if err != nil {
				return fmt.Errorf("reading the arguments of process %d: %w", host.PID, err)
			}

			return run(in, host, args)
		},
	}
}

func (a *app) readInput() (hookInput, error) {
	var in hookInput

	if a.deps.Stdin == nil {
		return in, errors.New("no input")
	}

	data, err := io.ReadAll(io.LimitReader(a.deps.Stdin, hookInputLimit))
	if err != nil {
		return in, fmt.Errorf("reading the input: %w", err)
	}

	if err := json.Unmarshal(data, &in); err != nil {
		return in, fmt.Errorf("decoding the input: %w", err)
	}

	return in, nil
}

func (a *app) requireHost(host adapter.Holder, want string) error {
	if host.Adapter.Name != want {
		return fmt.Errorf("the calling agent is %s, not %s", host.Adapter.Name, want)
	}

	return nil
}

// codexHome is the Codex home the host uses: CODEX_HOME, else .codex under the home directory.
func (a *app) codexHome() (string, error) {
	if home := a.deps.Getenv("CODEX_HOME"); home != "" {
		return home, nil
	}

	home := a.deps.Getenv("HOME")
	if !filepath.IsAbs(home) {
		var err error

		home, err = a.deps.UserHome()
		if err != nil {
			return "", err
		}
	}

	return filepath.Join(home, ".codex"), nil
}

func (a *app) runSessionStart(in hookInput, host adapter.Holder, args []string) error {
	if err := a.requireHost(host, session.AgentCodex); err != nil {
		return err
	}

	home, err := a.codexHome()
	if err != nil {
		return err
	}

	exe, err := a.deps.Executable()
	if err != nil {
		return err
	}

	a.lockBudget = hookLockBudget

	e, err := a.engine()
	if err != nil {
		return err
	}

	return e.RegisterCodex(session.RegisterInput{
		Host:       host,
		HostArgs:   args,
		Session:    in.SessionID,
		CodexHome:  home,
		Executable: exe,
	})
}

func (a *app) runSessionEnd(in hookInput, host adapter.Holder, _ []string) error {
	if err := a.requireHost(host, session.AgentCodex); err != nil {
		return err
	}

	e, err := a.engine()
	if err != nil {
		return err
	}

	return e.Sessions.EndCodex(host, in.SessionID)
}

func (a *app) runPresence(in hookInput, host adapter.Holder, args []string) error {
	if err := a.requireHost(host, session.AgentOpencode); err != nil {
		return err
	}

	if adapter.OpencodeIsServe(args) {
		return errors.New("a presence mark comes from the TUI, and the calling process is the service")
	}

	present := in.Present == nil || *in.Present

	return a.setMark(state.PresenceDir, host, in.SessionID, present)
}

func (a *app) runExecution(in hookInput, host adapter.Holder, args []string) error {
	if err := a.requireHost(host, session.AgentOpencode); err != nil {
		return err
	}

	if !adapter.OpencodeIsServe(args) {
		return errors.New("an execution mark comes from the service, and the calling process is not it")
	}

	executing := in.Executing == nil || *in.Executing

	return a.setMark(state.ExecutingDir, host, in.SessionID, executing)
}

func (a *app) setMark(dir string, host adapter.Holder, id string, on bool) error {
	a.lockBudget = hookLockBudget

	e, err := a.engine()
	if err != nil {
		return err
	}

	return e.SetOpencodeMark(dir, host, id, on)
}
