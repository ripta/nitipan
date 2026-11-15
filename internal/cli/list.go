package cli

import (
	"fmt"

	"github.com/spf13/cobra"

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

	caller, callerErr := a.resolver().Resolve().Caller()
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

	req.Emit = func(out listing.Output) error {
		for _, t := range out.Tombstones {
			fmt.Fprintf(a.deps.Stderr, "nitipan: tombstone %s: %s\n", t.Action, t.Summary)
		}

		if asJSON {
			return listing.Encode(a.deps.Stdout, out)
		}

		return listing.WriteText(a.deps.Stdout, out)
	}

	res, err := e.List(req)
	if err != nil {
		return err
	}

	if res.LockErr != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("listing without the state lock, so tombstones are not delivered: %v",
			a.wrapErr(res.LockErr)))
	}

	if callerErr != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%v; tombstones are not delivered", callerErr))
	}

	for _, w := range res.Warnings {
		fmt.Fprintf(a.deps.Stderr, "nitipan: warning: %s\n", w)
	}

	return nil
}
