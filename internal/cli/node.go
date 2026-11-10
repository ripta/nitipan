package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/space-armada/nitipan/internal/broker"
	"github.com/space-armada/nitipan/internal/config"
	"github.com/space-armada/nitipan/internal/handoff"
	"github.com/space-armada/nitipan/internal/liveness"
	"github.com/space-armada/nitipan/internal/node"
	"github.com/space-armada/nitipan/internal/session"
	"github.com/space-armada/nitipan/internal/setup"
	"github.com/space-armada/nitipan/internal/state"
	"github.com/space-armada/nitipan/internal/version"
)

// nodeCmd runs the node. launchd starts it, so it is hidden from help.
func (a *app) nodeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "node",
		Short:  "Run the node that mirrors this machine's leases to the broker",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, _ := cmd.Flags().GetString("codex-path")
			home, _ := cmd.Flags().GetString("codex-home")
			return a.runNode(path, home)
		},
	}

	cmd.Flags().String("codex-path", "", "trusted Codex executable from the LaunchAgent")
	cmd.Flags().String("codex-home", "", "trusted Codex home from the LaunchAgent")
	cmd.AddCommand(a.nodeFingerprintCmd())
	cmd.AddCommand(a.nodePairCmd())
	cmd.AddCommand(a.nodeRekeyCmd())

	return cmd
}

func (a *app) brokerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "broker",
		Short: "Run the lease broker",
		Long: `Run the broker that nodes on several machines connect to. launchd runs it. nitipan setup broker
prints the LaunchAgent. The broker reads ~/.config/nitipan/broker.json.`,
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return a.runBroker()
		},
	}

	cmd.AddCommand(a.brokerAdminCmds()...)

	return cmd
}

func (a *app) runNode(codexPath, codexHome string) error {
	if codexPath != "" && (!filepath.IsAbs(codexPath) || !filepath.IsAbs(codexHome)) {
		return errors.New("codex path and home must both be absolute")
	}
	stateDir, err := a.deps.StateDir()
	if err != nil {
		return fmt.Errorf("resolving the state directory: %w", err)
	}

	home, err := a.deps.UserHome()
	if err != nil {
		return err
	}

	uid := os.Getuid()

	cfg, err := config.LoadNode(home, uid)
	if err != nil {
		return err
	}

	local, err := a.deps.Machine()
	if err != nil {
		return fmt.Errorf("identifying this machine: %w", err)
	}

	user, err := a.deps.User()
	if err != nil {
		return err
	}

	exe, err := a.stableExecutable()
	if err != nil {
		return err
	}

	self := a.deps.Procs.Self()
	start, _ := a.deps.Procs.Start(self)

	sessions := &session.Service{Root: stateDir, Procs: a.deps.Procs, Local: local, Now: a.deps.Now, DB: a.opencodeDB()}
	checker := &liveness.Checker{Procs: a.deps.Procs, Local: local, Sessions: sessions.Rules()}

	n, err := node.New(node.Options{
		Checker: checker,
		Finder:  a.deps.Finder,
		Roots:   a.deriver(stateDir),
		StateOptions: state.Options{
			Checker: checker, Procs: a.deps.Procs, Machine: local, SelfPID: self, SelfStart: start, Command: "nitipan node",
			Warn: func(err error) { fmt.Fprintf(a.deps.Stderr, "nitipan: node: %v\n", err) },
		},
		StateDir:  stateDir,
		UID:       uid,
		User:      user,
		Machine:   local,
		SelfPID:   self,
		SelfStart: start,
		Version:   version.Version,
		Config:    cfg,
		ExecPath:  exe,
		CodexPath: codexPath,
		CodexHome: codexHome,
		Home:      home,
		Now:       a.deps.Now,
		Warn:      func(msg string) { fmt.Fprintf(a.deps.Stderr, "nitipan: node: %s\n", msg) },
	})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err = n.Run(ctx)
	if errors.Is(err, node.ErrExecutableChanged) {
		fmt.Fprintln(a.deps.Stderr, "nitipan: node: the executable changed. Exiting so launchd restarts it.")

		return nil
	}

	return err
}

func (a *app) runBroker() error {
	home, err := a.deps.UserHome()
	if err != nil {
		return err
	}

	cfg, err := config.LoadBroker(home, os.Getuid())
	if err != nil {
		return err
	}

	if err := broker.CheckListen(cfg.Listen, cfg.CertFile != "" || cfg.KeyFile != ""); err != nil {
		return err
	}

	path := cfg.Store
	if path == "" {
		path = broker.DefaultStorePath(home)
	}

	store, err := broker.OpenStore(path, broker.StoreOptions{
		Migrations: map[int]broker.Migration{
			1: broker.MigrateV1ToV2,
		},
	})
	if err != nil {
		return err
	}

	defer store.Close()

	b := broker.New(broker.Config{
		Store:       store,
		AdminSocket: broker.AdminSocketPath(path),
		Version:     version.Version,
		Listen:      cfg.Listen,
		TLS:         cfg.CertFile != "" && cfg.KeyFile != "",
		Logger:      slog.New(slog.NewTextHandler(a.deps.Stderr, nil)),
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return b.Serve(ctx, cfg.Listen, cfg.CertFile, cfg.KeyFile)
}

// daemonInput describes this nitipan for the LaunchAgent text.
func (a *app) daemonInput() (setup.DaemonInput, error) {
	home, err := a.deps.UserHome()
	if err != nil {
		return setup.DaemonInput{}, err
	}

	exe, err := a.stableExecutable()
	if err != nil {
		return setup.DaemonInput{}, err
	}

	in := setup.DaemonInput{Executable: exe, Home: home, UID: os.Getuid()}
	if path, err := a.deps.LookPath("codex"); err == nil && filepath.IsAbs(path) {
		in.CodexPath = path
		in.CodexHome, err = a.codexHome()
		if err != nil {
			return in, err
		}
	}
	return in, nil
}

// runSetupDaemon prints the LaunchAgent for the node or the broker.
func (a *app) runSetupDaemon(daemon string) error {
	in, err := a.daemonInput()
	if err != nil {
		return err
	}

	text := setup.NodeText(in)
	if daemon == setup.Broker {
		text = setup.BrokerText(in)
	} else {
		text += a.approvalText()
	}

	_, err = fmt.Fprint(a.deps.Stdout, text)

	return err
}

// requireNode fails at once with the install text when no live node published a presence record.
// A command that needs the broker calls it before it writes a request.
func (a *app) requireNode() (handoff.Presence, error) {
	stateDir, err := a.deps.StateDir()
	if err != nil {
		return handoff.Presence{}, fmt.Errorf("resolving the state directory: %w", err)
	}

	local, err := a.deps.Machine()
	if err != nil {
		return handoff.Presence{}, fmt.Errorf("identifying this machine: %w", err)
	}

	in, err := a.daemonInput()
	if err != nil {
		return handoff.Presence{}, err
	}

	checker := &liveness.Checker{Procs: a.deps.Procs, Local: local}

	p, err := handoff.Require(stateDir, checker, setup.NodeText(in))
	if err != nil {
		return handoff.Presence{}, err
	}

	return p, nil
}

// approvalText tells the user how to approve the node for mail. It reads the node's key when one exists
// and creates nothing, since setup writes nothing.
func (a *app) approvalText() string {
	dir, err := a.deps.StateDir()
	if err == nil {
		if fp, ok := node.ExistingFingerprint(dir); ok {
			return fmt.Sprintf("\nThis node's key fingerprint is %[1]s. Once the node runs, the broker lists the key under its label.\n"+
				"Approve it on the broker host with: nitipan broker approve <label> %[1]s\n", fp)
		}
	}

	return "\nMail needs the broker host's user to approve this node's key. Once the node has run, print its fingerprint with:\n\n" +
		"  nitipan node fingerprint\n\nand approve it on the broker host with: nitipan broker approve <label> <fingerprint>\n"
}
