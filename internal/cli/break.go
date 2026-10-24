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

A file whose name is not a lease ID cannot be named here. It has to be deleted by hand.`,
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

	req := lease.BreakRequest{IDs: ids, Force: force}

	req.Caller, req.CallerErr = a.resolver().Resolve().Caller()

	e, err := a.engine()
	if err != nil {
		return err
	}

	res, err := e.Break(req)
	if res != nil {
		for _, t := range res.Targets {
			fmt.Fprintln(a.deps.Stdout, breakLine(t))

			if t.ReturnedTo != "" {
				fmt.Fprintf(a.deps.Stdout, "  its keys went back to lease %s\n", t.ReturnedTo)
			}

			for _, line := range standingLines(t.Standing) {
				fmt.Fprintln(a.deps.Stdout, line)
			}
		}
	}

	return err
}

func breakLine(t lease.BrokenLease) string {
	kind := "lease"
	if t.WasTombstone {
		kind = "tombstone"
	}

	switch t.Outcome {
	case lease.Cleared:
		return fmt.Sprintf("cleared %s %s of a dead holder", kind, t.ID)
	case lease.Broken:
		return fmt.Sprintf("broken %s %s; a tombstone waits for its holder", kind, t.ID)
	default:
		return fmt.Sprintf("removed unusable %s %s; no tombstone written, because nitipan cannot tell whose it was",
			kind, t.ID)
	}
}
