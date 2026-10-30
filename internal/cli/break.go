package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/space-armada/nitipan/internal/lease"
)

func (a *app) breakCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "break <lease-id> ...",
		Short: "Remove another holder's lease",
		Long: `Remove other holders' leases by lease ID, as list prints them.

A dead holder's lease goes without a flag when its record allows clearing. A live holder's lease,
a holder whose liveness is unknown, a dead holder's record that asks for a newer nitipan, and a
record or tombstone nitipan cannot use all need --force.

Breaking a live holder's lease leaves a tombstone addressed to that holder. The holder keeps working
until its next nitipan call, which reports the tombstone and removes it. Removing a record nitipan
cannot use leaves no tombstone, because nitipan cannot tell whose it was.

Breaking a lease that has sub-leases removes only that lease. Its sub-leases stand alone from then on,
as ordinary leases of their holders, and the output lists them. Breaking a sub-lease returns its keys to
the lease it came from, and the output says so.

A file whose name is not a lease ID cannot be named here. It has to be deleted by hand.

Breaking a live holder's lease also leaves a fence mark for that holder. The mark outlives the
tombstone: it refuses the holder's claim on those paths, and, where nitipan's git hook is in force,
its ref updates there. The mark lifts for a path once another session holds it, or once the holder
dies. list shows marks, and break takes a mark's ID to remove one without --force. A holder cannot
remove a mark that guards against itself.`,
		RunE: func(cmd *cobra.Command, ids []string) error {
			return a.wrapErr(a.runBreak(cmd, ids))
		},
	}

	cmd.Flags().Bool("force", false, "break a live holder's lease, or remove a record nitipan cannot use")

	return cmd
}

func (a *app) runBreak(cmd *cobra.Command, ids []string) error {
	force, err := cmd.Flags().GetBool("force")
	if err != nil {
		return fmt.Errorf("reading --force: %w", err)
	}

	stateDir, err := a.deps.StateDir()
	if err != nil {
		return fmt.Errorf("resolving the state directory: %w", err)
	}

	var remote []string

	local := ids[:0:0]

	for _, id := range ids {
		if isLocalID(stateDir, id) {
			local = append(local, id)
		} else {
			remote = append(remote, id)
		}
	}

	if len(remote) > 0 && len(local) == 0 {
		if _, err := a.requireNode(); err == nil {
			return a.runBreakBroker(stateDir, remote, force)
		}
	}

	var brokerErr error

	if len(remote) > 0 && len(local) > 0 {
		brokerErr = a.runBreakBroker(stateDir, remote, force)
		ids = local
	}

	req := lease.BreakRequest{IDs: ids, Force: force}

	req.Caller, req.CallerErr = a.resolver().Resolve().Caller()

	e, err := a.engine()
	if err != nil {
		return err
	}

	res, err := e.Break(req)
	if brokerErr != nil {
		defer func() { fmt.Fprintf(a.deps.Stderr, "nitipan: %v\n", brokerErr) }()
	}

	if res != nil {
		for _, t := range res.Targets {
			fmt.Fprintln(a.deps.Stdout, breakLine(t))

			if t.ReturnedTo != "" {
				fmt.Fprintf(a.deps.Stdout, "  its keys went back to lease %s\n", t.ReturnedTo)
			}

			for _, line := range standingLines(t.Standing) {
				fmt.Fprintln(a.deps.Stdout, line)
			}

			for _, line := range hooksLines(t.Hooks) {
				fmt.Fprintln(a.deps.Stdout, line)
			}
		}
	}

	return err
}

func breakLine(t lease.BrokenLease) string {
	kind := "lease"

	switch {
	case t.WasTombstone:
		kind = "tombstone"
	case t.WasMark:
		kind = "fence mark"
	}

	switch t.Outcome {
	case lease.Cleared:
		return fmt.Sprintf("cleared %s %s of a dead holder", kind, t.ID)
	case lease.Broken:
		return fmt.Sprintf("broken %s %s; a tombstone waits for its holder", kind, t.ID)
	case lease.RemovedMark:
		return fmt.Sprintf("removed fence mark %s; its holder may claim those paths again", t.ID)
	default:
		return fmt.Sprintf("removed unusable %s %s; no tombstone written, because nitipan cannot tell whose it was",
			kind, t.ID)
	}
}

// hooksLines says whether the broken lease is fenced. A break is fenced against the holder's next ref
// update only where nitipan's git hook is in force.
func hooksLines(hooks []lease.GitHooks) []string {
	var out []string

	for _, h := range hooks {
		switch {
		case h.Checkout == "":
			out = append(out, "  this lease is not inside a git checkout, so no git hook fences it")
		case h.InForce:
			out = append(out, fmt.Sprintf("  the git hook is in force in %s, so the holder's ref updates there are fenced", h.Checkout))
		default:
			out = append(out, fmt.Sprintf("  the git hook is not in force in %s (%s), so the holder's ref updates there are "+
				"not fenced", h.Checkout, h.Why))
		}
	}

	return out
}
