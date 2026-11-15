package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/space-armada/nitipan/internal/adapter"
	"github.com/space-armada/nitipan/internal/lease"
	"github.com/space-armada/nitipan/internal/listing"
)

func (a *app) listCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Print every lease with its holder's liveness",
		Long: `Print every lease with its holder's liveness. With the domain flags, print only the leases that
overlap that domain, by nitipan's own canonicalization and overlap rule. With --resource, print the
leases on that resource name. A resource lease written on another machine is marked remote. A local
resource lease shows how many holders its name admits and which leases hold it. The text output also
prints where the counts are configured.

A sub-lease prints indented under its parent, and a pending request under the lease it asks to share. A
list that takes the lock also delivers the caller's notices, as any other command does. A list that
reads without the lock delivers none.

--json is the supported interface for scripts and guardians. Its schema is versioned apart from the
lease files, which are nitipan's own format. A liveness of "unknown" means treat the holder as alive.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.wrapErr(a.runList(cmd))
		},
	}

	addDomainFlags(cmd, "to filter by")
	addResourceFlag(cmd, "resource name to filter by")
	cmd.Flags().Bool("json", false, "print the versioned JSON document")

	return cmd
}

func (a *app) runList(cmd *cobra.Command) error {
	req := lease.ListRequest{}

	caller, callerErr := a.caller()
	if callerErr == nil {
		req.Caller = caller

		if a.deps.OnCaller != nil {
			a.deps.OnCaller(caller)
		}
	}

	resource, err := resourceName(cmd)
	if err != nil {
		return err
	}

	req.Resource = resource

	if domainGiven(cmd) {
		d, err := a.domain(cmd)
		if err != nil {
			return err
		}

		req.Domain = &d
	}

	asJSON, err := cmd.Flags().GetBool("json")
	if err != nil {
		return fmt.Errorf("reading --json: %w", err)
	}

	e, err := a.engine()
	if err != nil {
		return err
	}

	var me *adapter.Holder
	if callerErr == nil {
		me = caller.Holder
	}

	req.Emit = func(out listing.Output) error {
		if req.Domain == nil && req.Resource == "" {
			out.BrokerLeases = a.brokerLeases(me)
		}

		for _, t := range out.Tombstones {
			fmt.Fprintf(a.deps.Stderr, "nitipan: tombstone %s: %s\n", t.Action, t.Summary)
		}

		for _, n := range out.Notices {
			a.noteNoticed(n.Target)
			a.printRecordNotice(n.Summary)
		}

		if asJSON {
			return listing.Encode(a.deps.Stdout, out)
		}

		if err := listing.WriteText(a.deps.Stdout, out); err != nil {
			return err
		}

		return listing.WriteBroker(a.deps.Stdout, out.BrokerLeases)
	}

	res, err := e.List(req)
	if err != nil {
		return err
	}

	if res.LockErr != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("listing without the state lock, so tombstones and notices are not delivered: %v",
			a.wrapErr(res.LockErr)))
	}

	if callerErr != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%v; tombstones and notices are not delivered", callerErr))
	}

	if me != nil {
		a.reportRisk(*me)
	}

	for _, w := range res.Warnings {
		fmt.Fprintf(a.deps.Stderr, "nitipan: warning: %s\n", w)
	}

	return nil
}
