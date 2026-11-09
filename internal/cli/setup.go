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
		Short: "Print the entries an agent needs, or install the opencode plugins and the git fence",
		Long: `Print the entries an agent needs before it can claim leases: the sandbox allowance for the state
directory, and the hooks or plugins that report when a session ends.

The agent is the argument, or the agent that runs this command. setup never writes settings. The same
text appears whenever a write to the state directory is blocked.

For opencode, --install-plugins <dir> writes nitipan's two plugins into <dir>/nitipan, with this
nitipan's absolute path baked in. It writes plugin files and nothing else. It refuses to overwrite a
file that differs from the one nitipan ships, unless --replace is given.

setup node and setup broker print the LaunchAgent that runs the per-user node, and the one that runs the
broker, with the launchctl command that loads each. They write nothing.

setup git --install <checkout> installs two git hooks. The reference-transaction hook refuses a branch
or HEAD update touching paths another session holds, or that a broken lease used to cover. The pre-push
hook refuses a push of a fenced branch that lacks the fence refs of nitipan push. A path claim inside
a git checkout is refused until both are in force there. The hooks go into the directory git uses for
hooks, in every initialized submodule too, and an existing hook of either name is kept and chained. A
hooks directory that git tracks is never written. The hook goes under the git directory instead, the
checkout's local core.hooksPath points at it, and every hook chains to the tracked directory.
setup git --uninstall <checkout> removes the hooks, puts back what it chained, and restores the
previous core.hooksPath. A caller that is no agent, such as the user's own terminal, is never refused.`,
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
	cmd.Flags().String("install", "", "with git: install the git hook in this checkout")
	cmd.Flags().String("uninstall", "", "with git: remove the git hook from this checkout")

	return cmd
}

func (a *app) runSetup(cmd *cobra.Command, args []string) error {
	if len(args) == 1 && args[0] == setup.Git {
		return a.runSetupGit(cmd)
	}

	if len(args) == 1 && (args[0] == setup.Node || args[0] == setup.Broker) {
		return a.runSetupDaemon(args[0])
	}

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

// runSetupGit installs or removes the git hook.
func (a *app) runSetupGit(cmd *cobra.Command) error {
	install, err := cmd.Flags().GetString("install")
	if err != nil {
		return fmt.Errorf("reading --install: %w", err)
	}

	uninstall, err := cmd.Flags().GetString("uninstall")
	if err != nil {
		return fmt.Errorf("reading --uninstall: %w", err)
	}

	changed := cmd.Flags().Changed("install")

	switch {
	case changed && cmd.Flags().Changed("uninstall"):
		return fmt.Errorf("%w: give --install or --uninstall, not both", exitcode.ErrUsage)
	case !changed && !cmd.Flags().Changed("uninstall"):
		_, err := fmt.Fprint(a.deps.Stdout, setup.GitText())

		return err
	case changed && install == "", !changed && uninstall == "":
		return fmt.Errorf("%w: --install and --uninstall need the checkout to act on", exitcode.ErrUsage)
	}

	if changed {
		return a.installGit(install)
	}

	abs, err := a.absPath(uninstall)
	if err != nil {
		return err
	}

	done, err := setup.UninstallGit(a.deps.Git, abs)
	a.printInstalled(done)

	if err != nil {
		return err
	}

	if len(done) == 0 {
		fmt.Fprintf(a.deps.Stdout, "nitipan's git hook was not installed in %s\n", abs)
	}

	return nil
}

func (a *app) installGit(dir string) error {
	abs, err := a.absPath(dir)
	if err != nil {
		return err
	}

	exe, err := a.stableExecutable()
	if err != nil {
		return err
	}

	done, err := setup.InstallGit(a.deps.Git, abs, exe)
	a.printInstalled(done)

	if err != nil {
		return err
	}

	fmt.Fprintf(a.deps.Stdout, "\nThe hook runs %s, or the nitipan on PATH when that path is gone.\n", exe)

	return nil
}

func (a *app) printInstalled(done []setup.Installed) {
	for _, f := range done {
		line := fmt.Sprintf("%s %s", f.Action, f.Path)
		if f.Note != "" {
			line += " (" + f.Note + ")"
		}

		fmt.Fprintln(a.deps.Stdout, line)
	}
}

// stableExecutable is the path the git hook bakes in. A symlink-resolved path can name a versioned
// directory that an upgrade deletes, so the location on PATH wins when it leads to this nitipan.
func (a *app) stableExecutable() (string, error) {
	exe, err := a.deps.Executable()
	if err != nil {
		return "", err
	}

	found, err := a.deps.LookPath("nitipan")
	if err != nil {
		return exe, nil
	}

	found, err = filepath.Abs(found)
	if err != nil {
		return exe, nil
	}

	resolved, err := filepath.EvalSymlinks(found)
	if err != nil || resolved != exe {
		return exe, nil
	}

	return found, nil
}
