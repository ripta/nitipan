package cli

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/space-armada/nitipan/internal/exitcode"
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
--slots states the count you expect for the resource, and is refused when it differs from the count
that applies. A name admits as many holders as the user configured for it, and one holder when the user
configured none. When a slot is full, the refusal names the holders. Ask the user to raise the count.

--wait <duration> waits for a live holder or a full slot to clear instead of being refused at once. It
applies to path and resource claims, and the duration is required, such as 30s or 5m. The wait polls
with backoff and holds the state lock only during each attempt. There is no queue, so a waiter can lose
a freed slot to a claim that arrived later. Only a live overlap, a full slot, or a busy lock is waited
on. Every other refusal exits at once. When the time runs out, acquire exits with status 10 and names
the holders it waited on. No file is created while waiting.

If an acquire was killed, such as by a shell tool's time limit, the lease may already be published.
Retry the same acquire, which reports it as already held, or run list.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.wrapErr(a.runAcquire(cmd))
		},
	}

	addDomainFlags(cmd, "to claim")
	addResourceFlag(cmd, "resource name to claim, such as cpu:heavy; one per call, and not with --checkout or --scope")
	cmd.Flags().Int("slots", 0, "the count you expect for the resource; refused when it differs from the count that applies")
	cmd.Flags().String("wait", "", "wait up to this long, such as 30s or 5m, for a live holder or a full slot to clear")
	cmd.Flags().String("name", "", "the session name others can reach you by, such as the /rename name")
	cmd.Flags().String("permission-mode", "", "the session's permission mode, as the agent names it")
	cmd.Flags().String("description", "", "what this session does with the domain")
	cmd.Flags().String("reach", "", "how far the lease reaches: machine (the default), user, or shared. user and shared go through the node to the broker")
	cmd.Flags().String("acting-for", "", "act in this user's scope under their standing delegation to your label; names a user label")
	cmd.Flags().StringSlice("replace", nil, "rewrite your lease with this ID to the new domain; repeat to merge several")

	return cmd
}

func (a *app) runAcquire(cmd *cobra.Command) error {
	h, err := a.holder()
	if err != nil {
		return err
	}

	reach, err := wideReach(cmd)
	if err != nil {
		return err
	}

	if reach != "" {
		if err := a.runAcquireWide(cmd, h, reach); err != nil {
			return err
		}

		a.reportRisk(h)

		return nil
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

	wait, err := waitFlag(cmd)
	if err != nil {
		return err
	}

	e, err := a.engine()
	if err != nil {
		return err
	}

	req := lease.AcquireRequest{
		Holder:      h,
		Domain:      domain,
		Resource:    resource,
		Slots:       slots,
		Name:        optString(cmd, "name"),
		Mode:        optString(cmd, "permission-mode"),
		Description: optString(cmd, "description"),
		Replace:     replace,
	}

	var res *lease.AcquireResult
	if wait > 0 {
		res, err = e.AcquireWait(req, wait)
	} else {
		res, err = e.Acquire(req)
	}

	var covered *lease.CoveredError
	if errors.As(err, &covered) {
		for _, l := range covered.Leases {
			a.clearCreated(l.ID)
		}
	}

	if err != nil {
		return err
	}

	a.reportRisk(h)

	if resource == "" {
		a.recordDerived(res.Lease)
	}

	return a.printAcquired(res)
}

// waitFlag reads --wait. It returns zero when the flag was left out. A value must be a positive Go
// duration, since a wait of nothing would silently be no wait.
func waitFlag(cmd *cobra.Command) (time.Duration, error) {
	if !cmd.Flags().Changed("wait") {
		return 0, nil
	}

	raw, err := cmd.Flags().GetString("wait")
	if err != nil {
		return 0, fmt.Errorf("reading --wait: %w", err)
	}

	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%w: --wait needs a duration such as 30s or 5m, got %q", exitcode.ErrUsage, raw)
	}

	if d <= 0 {
		return 0, fmt.Errorf("%w: --wait must be a positive duration, got %q", exitcode.ErrUsage, raw)
	}

	return d, nil
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

	if err := listing.WriteText(a.deps.Stdout, out); err != nil {
		return err
	}

	if len(res.Delegated) == 0 {
		return nil
	}

	lines := delegationLines(res.Delegated, res.Recalled)
	fmt.Fprintln(a.deps.Stdout, strings.Join(lines, "\n"))

	return fmt.Errorf("%w: you hold %s, but %s of it %s delegated, so you do not own all of it right now",
		exitcode.ErrDelegated, res.Lease.ID, lease.Plural(len(lines), "part", "parts"), lease.Plural(len(lines), "is", "are"))
}
