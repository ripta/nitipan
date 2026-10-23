package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/space-armada/nitipan/internal/lease"
	"github.com/space-armada/nitipan/internal/listing"
	"github.com/space-armada/nitipan/internal/liveness"
)

func (a *app) acquireCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "acquire",
		Short: "Claim a domain or a resource",
		Long: `Claim a domain: a checkout, optionally narrowed to path scopes.

The claim is refused while a live holder owns an overlapping domain, and the refusal names the
holder. A claim against a dead holder succeeds. Claiming a domain you already hold is a no-op that
refreshes the advertisement, and a flag you leave out keeps its stored value.

--resource claims a machine resource by name instead of a path, such as cpu:heavy. A name is
<namespace>:<name>, in lowercase letters, digits, '-', '_', and '.'. Two names overlap only when they
are equal, and a resource never overlaps a path. Resource leases are advisory and coordinate
cooperating sessions only. Acquire them from the parent session, since subagents share its holder.
--slots states the count you expect for the resource, and is refused when it differs.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.wrapErr(a.runAcquire(cmd))
		},
	}

	addDomainFlags(cmd, "to claim")
	addResourceFlag(cmd, "resource name to claim, such as cpu:heavy; one per call, and not with --checkout or --scope")
	cmd.Flags().Int("slots", 0, "the count you expect for the resource; refused when it differs from the count that applies")
	cmd.Flags().String("name", "", "the session name others can reach you by, such as the /rename name")
	cmd.Flags().String("permission-mode", "", "the session's permission mode, as the agent names it")
	cmd.Flags().String("description", "", "what this session does with the domain")
	cmd.Flags().StringSlice("replace", nil, "rewrite your lease with this ID to the new domain; repeat to merge several")

	return cmd
}

func (a *app) runAcquire(cmd *cobra.Command) error {
	h, err := a.holder()
	if err != nil {
		return err
	}

	resource, err := resourceName(cmd)
	if err != nil {
		return err
	}

	var domain lease.Domain

	if resource == "" {
		domain, err = a.domain(cmd)
		if err != nil {
			return err
		}
	}

	slots, err := optInt(cmd, "slots")
	if err != nil {
		return err
	}

	replace, err := cmd.Flags().GetStringSlice("replace")
	if err != nil {
		return fmt.Errorf("reading --replace: %w", err)
	}

	e, err := a.engine()
	if err != nil {
		return err
	}

	res, err := e.Acquire(lease.AcquireRequest{
		Holder:      h,
		Domain:      domain,
		Resource:    resource,
		Slots:       slots,
		Name:        optString(cmd, "name"),
		Mode:        optString(cmd, "permission-mode"),
		Description: optString(cmd, "description"),
		Replace:     replace,
	})
	if err != nil {
		return err
	}

	return a.printAcquired(res)
}

func (a *app) printAcquired(res *lease.AcquireResult) error {
	heading := map[lease.Outcome]string{
		lease.Created:   "acquired",
		lease.Refreshed: "already held; advertisement updated",
		lease.Unchanged: "already held; nothing changed",
		lease.Replaced:  "replaced",
	}[res.Outcome]

	if len(res.Cleared) > 0 {
		fmt.Fprintf(a.deps.Stderr, "nitipan: cleared %s of a dead holder: %s\n",
			lease.Plural(len(res.Cleared), "lease", "leases"), strings.Join(res.Cleared, ", "))
	}

	if len(res.Absorbed) > 0 {
		heading += " (merged " + strings.Join(res.Absorbed, ", ") + ")"
	}

	fmt.Fprintln(a.deps.Stdout, heading)

	if len(res.Dropped) > 0 {
		fmt.Fprintf(a.deps.Stdout, "released keys the new claim does not contain: %s\n", strings.Join(res.Dropped, ", "))
	}

	out := listing.Output{Leases: []listing.Lease{listing.FromRecord(res.Lease, liveness.Result{Status: liveness.Alive})}}

	return listing.WriteText(a.deps.Stdout, out)
}
