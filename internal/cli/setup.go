package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/setup"
)

func (a *app) setupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "setup [agent]",
		Short: "Print the entries an agent needs, or install the opencode plugins",
		Long: `Print the entries an agent needs before it can claim leases: the sandbox allowance for the state
directory, and the hooks or plugins that report when a session ends.

The agent is the argument, or the agent that runs this command. setup never writes settings. The same
text appears whenever a write to the state directory is blocked.

For opencode, --install-plugins <dir> writes nitipan's two plugins into <dir>/nitipan, with this
nitipan's absolute path baked in. It writes plugin files and nothing else. It refuses to overwrite a
file that differs from the one nitipan ships, unless --replace is given.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 1 {
				return fmt.Errorf("%w: %s takes at most one agent, got %q", exitcode.ErrUsage, cmd.Name(), args)
			}

			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runSetup(cmd, args)
		},
	}

	cmd.Flags().String("install-plugins", "", "write the opencode plugins into this plugins directory")
	cmd.Flags().Bool("replace", false, "overwrite a plugin file that differs from the one nitipan ships")

	return cmd
}

func (a *app) runSetup(cmd *cobra.Command, args []string) error {
	agent, err := a.setupAgent(args)
	if err != nil {
		return err
	}

	dir, err := cmd.Flags().GetString("install-plugins")
	if err != nil {
		return fmt.Errorf("reading --install-plugins: %w", err)
	}

	replace, err := cmd.Flags().GetBool("replace")
	if err != nil {
		return fmt.Errorf("reading --replace: %w", err)
	}

	if dir != "" {
		return a.installPlugins(agent, dir, replace)
	}

	if replace {
		return fmt.Errorf("%w: --replace goes with --install-plugins", exitcode.ErrUsage)
	}

	in, err := a.setupInput()
	if err != nil {
		return fmt.Errorf("resolving the state directory: %w", err)
	}

	text, err := setup.Text(agent, in)
	if err != nil {
		return fmt.Errorf("%w: %w", exitcode.ErrUsage, err)
	}

	_, err = fmt.Fprint(a.deps.Stdout, text)
	if err != nil {
		return fmt.Errorf("writing the setup text: %w", err)
	}

	return nil
}

// installPlugins writes the opencode plugin files. It prints only what it did to each file, never
// the plugin source.
func (a *app) installPlugins(agent, dir string, replace bool) error {
	if agent != setup.Opencode {
		return fmt.Errorf("%w: --install-plugins is for opencode, not %s", exitcode.ErrUsage, agent)
	}

	exe, err := a.deps.Executable()
	if err != nil {
		return err
	}

	abs, err := a.absPath(dir)
	if err != nil {
		return err
	}

	stateDir, err := a.deps.StateDir()
	if err != nil {
		return fmt.Errorf("resolving the state directory: %w", err)
	}

	done, err := setup.InstallOpencode(abs, exe, stateDir, replace)
	if err != nil {
		return err
	}

	for _, f := range done {
		fmt.Fprintf(a.deps.Stdout, "%s %s\n", f.Action, f.Path)
	}

	fmt.Fprintf(a.deps.Stdout, "\nRestart the opencode service and each TUI so they load the plugins.\n"+
		"nitipan setup opencode prints the rest of the steps.\n")

	return nil
}

// absPath makes a path absolute against the working directory.
func (a *app) absPath(p string) (string, error) {
	if filepath.IsAbs(p) {
		return filepath.Clean(p), nil
	}

	cwd, err := a.deps.Cwd()
	if err != nil {
		return "", fmt.Errorf("reading the working directory: %w", err)
	}

	return filepath.Join(cwd, p), nil
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
