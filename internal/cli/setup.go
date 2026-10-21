package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/setup"
)

func (a *app) setupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "setup [agent]",
		Short: "Print the sandbox allowance entries for an agent",
		Long: `Print the entries an agent's sandbox needs before it can write the state directory.

The agent is the argument, or the agent that runs this command. setup never writes settings. The same
text appears whenever a write to the state directory is blocked.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 1 {
				return fmt.Errorf("%w: %s takes at most one agent, got %q", exitcode.ErrUsage, cmd.Name(), args)
			}

			return nil
		},
		RunE: func(_ *cobra.Command, args []string) error {
			return a.runSetup(args)
		},
	}
}

func (a *app) runSetup(args []string) error {
	agent, err := a.setupAgent(args)
	if err != nil {
		return err
	}

	dir, err := a.deps.StateDir()
	if err != nil {
		return fmt.Errorf("resolving the state directory: %w", err)
	}

	text, err := setup.Text(agent, dir)
	if err != nil {
		return fmt.Errorf("%w: %w", exitcode.ErrUsage, err)
	}

	_, err = fmt.Fprint(a.deps.Stdout, text)
	if err != nil {
		return fmt.Errorf("writing the setup text: %w", err)
	}

	return nil
}

func (a *app) setupAgent(args []string) (string, error) {
	if len(args) == 1 {
		return args[0], nil
	}

	res := a.resolver().Resolve()
	if res.Holder == nil {
		return "", fmt.Errorf("%w: no agent runs this command. Name one: %s", exitcode.ErrUsage,
			strings.Join(setup.Agents(), ", "))
	}

	return res.Holder.Adapter.Name, nil
}
