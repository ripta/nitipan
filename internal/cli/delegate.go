package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/lease"
	"github.com/space-armada/nitipan/internal/listing"
	"github.com/space-armada/nitipan/internal/liveness"
	"github.com/space-armada/nitipan/internal/record"
)

func oneArg(what string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != 1 {
			return fmt.Errorf("%w: %s takes exactly one %s, got %d", exitcode.ErrUsage, cmd.Name(), what, len(args))
		}

		return nil
	}
}

func (a *app) requestCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "request <lease-id>",
		Short: "Ask the holder of a lease for part of it",
		Long: `Ask the holder of a lease to delegate part of it to you as a sub-lease.

The domain flags and advertisement flags are the ones acquire takes. The granted sub-lease takes its
description, name, and permission mode from this request. The request is refused when the lease does
not cover the keys, when a live sub-lease of it already covers or overlaps them, and when the lease is
a sub-lease or a resource lease, since neither can delegate.

Request prints the request ID and the lease holder's handle. Ask that holder to run grant with the ID,
for example through SendMessage. You learn the outcome from a notice on your next nitipan call. Once
granted, acquire on the same keys reports the sub-lease as already held.`,
		Args: oneArg("lease ID"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.wrapErr(a.runRequest(cmd, args[0]))
		},
	}

	addDomainFlags(cmd, "to ask for")
	cmd.Flags().String("name", "", "the session name others can reach you by, such as the /rename name")
	cmd.Flags().String("permission-mode", "", "the session's permission mode, as the agent names it")
	cmd.Flags().String("description", "", "what this session will do with the part it is granted")

	return cmd
}

func (a *app) runRequest(cmd *cobra.Command, parentID string) error {
	h, err := a.holder()
	if err != nil {
		return err
	}

	domain, err := a.domain(cmd)
	if err != nil {
		return err
	}

	e, err := a.engine()
	if err != nil {
		return err
	}

	res, err := e.Request(lease.SubLeaseRequest{
		Holder:      h,
		ParentID:    parentID,
		Domain:      domain,
		Name:        optString(cmd, "name"),
		Mode:        optString(cmd, "permission-mode"),
		Description: optString(cmd, "description"),
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(a.deps.Stdout, "requested %s\n", res.Request.ID)
	fmt.Fprintf(a.deps.Stdout, "  from lease: %s\n", res.Parent.ID)
	fmt.Fprintf(a.deps.Stdout, "  parent handle: %s\n", orNone(res.Parent.Holder.Handle))
	fmt.Fprintf(a.deps.Stdout, "  parent agent name: %s\n", orNone(res.Parent.Holder.Name))
	fmt.Fprintf(a.deps.Stdout, "Ask the parent to run: nitipan grant %s\n", res.Request.ID)

	return nil
}

func (a *app) grantCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "grant <request-id>",
		Short: "Grant a request for part of a lease you hold",
		Long: `Turn a request into a sub-lease of a lease you hold. Only the holder of the parent lease can grant.

The sub-lease belongs to the requester alone while it lives. It takes the request's ID as its lease
ID, and its description, name, and permission mode from the request. The output names the requester's
handle and agent name, so you see whom you granted to.

Grant refuses a request from your own holder, a request whose requester has died, keys the lease no
longer covers, and keys that another live lease overlaps. Subagents share their parent's holder, so
any subagent of yours can grant.`,
		Args: oneArg("request ID"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.wrapErr(a.runGrant(args[0]))
		},
	}

	return cmd
}

func (a *app) runGrant(requestID string) error {
	h, err := a.holder()
	if err != nil {
		return err
	}

	e, err := a.engine()
	if err != nil {
		return err
	}

	res, err := e.Grant(h, requestID)
	if err != nil {
		return err
	}

	if len(res.Cleared) > 0 {
		fmt.Fprintf(a.deps.Stderr, "nitipan: cleared %s of a dead holder: %s\n",
			lease.Plural(len(res.Cleared), "lease", "leases"), strings.Join(res.Cleared, ", "))
	}

	holder := res.Lease.Holder
	fmt.Fprintf(a.deps.Stdout, "granted %s to handle %s, agent name %s\n", res.Lease.ID, orNone(holder.Handle), orNone(holder.Name))

	out := listing.Output{Leases: []listing.Lease{listing.FromRecord(res.Lease, liveness.Result{Status: liveness.Alive})}}

	return listing.WriteText(a.deps.Stdout, out)
}

func (a *app) declineCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "decline <request-id>",
		Short: "Refuse a request, or withdraw your own",
		Long: `Remove a request for part of a lease. The holder of the parent lease runs it to refuse, and the
requester runs it to withdraw. A refusal leaves the requester a notice. A withdrawal leaves none.`,
		Args: oneArg("request ID"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.wrapErr(a.runDecline(args[0]))
		},
	}

	return cmd
}

func (a *app) runDecline(requestID string) error {
	h, err := a.holder()
	if err != nil {
		return err
	}

	e, err := a.engine()
	if err != nil {
		return err
	}

	res, err := e.Decline(h, requestID)
	if err != nil {
		return err
	}

	if res.ByRequester {
		fmt.Fprintf(a.deps.Stdout, "withdrew request %s\n", res.Request.ID)

		return nil
	}

	fmt.Fprintf(a.deps.Stdout, "declined request %s from handle %s, agent name %s\n", res.Request.ID,
		orNone(res.Request.Holder.Handle), orNone(res.Request.Holder.Name))

	return nil
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}

	return s
}

// delegationLines describes the live sub-leases a refresh found, one per line.
func delegationLines(recs []*record.Record) []string {
	lines := make([]string, len(recs))

	for i, r := range recs {
		paths := make([]string, len(r.Domain.Keys))
		for j, k := range r.Domain.Keys {
			paths[j] = k.Path
		}

		lines[i] = fmt.Sprintf("delegated: %s on %s to %s", r.ID, strings.Join(paths, ", "), lease.DescribeHolder(r.Holder))
	}

	return lines
}
