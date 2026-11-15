package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/space-armada/nitipan/internal/adapter"
	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/handoff"
	"github.com/space-armada/nitipan/internal/lease"
)

func (a *app) releaseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "release [lease-id ...]",
		Short: "Give up a lease you hold",
		Long: `Give up leases you hold. Name them by lease ID, by the domain flags, by --resource, or with --all.

The domain flags match a lease whose canonical keys equal them, using the keys stored in the lease,
so the lease of a deleted worktree can still be released. --resource matches a lease whose resource
name is equal. With no target, release refuses and lists your leases.

Releasing a lease you delegated parts of removes only that lease. Its sub-leases stand alone from then
on, as ordinary leases of their holders, and the output lists them. Releasing a sub-lease returns its
keys to the lease it came from.`,
		RunE: func(cmd *cobra.Command, ids []string) error {
			return a.wrapErr(a.runRelease(cmd, ids))
		},
	}

	addDomainFlags(cmd, "whose lease to release")
	addResourceFlag(cmd, "name of the resource whose lease to release")
	cmd.Flags().Bool("all", false,
		"release every lease you hold; subagents share their parent's holder, so this releases the parent's leases too")

	return cmd
}

func (a *app) runRelease(cmd *cobra.Command, ids []string) error {
	req, err := a.releaseRequest(cmd, ids)
	if err != nil {
		return err
	}

	h, err := a.holder()
	if err != nil {
		if errors.Is(err, adapter.ErrNoAdapter) {
			return fmt.Errorf("%w; release acts for an agent session. To remove a dead holder's lease, use break "+
				"with its lease ID", err)
		}

		return err
	}

	req.Holder = h

	stateDir, err := a.deps.StateDir()
	if err != nil {
		return fmt.Errorf("resolving the state directory: %w", err)
	}

	e, err := a.engine()
	if err != nil {
		return err
	}

	machine, err := a.deps.Machine()
	if err != nil {
		return fmt.Errorf("identifying this machine: %w", err)
	}

	env := holderEnv(h, machine)

	local, remote, names, err := a.brokerTargets(stateDir, env, ids)
	if err != nil {
		return err
	}

	if req.All {
		mine, err := a.mySidecars(stateDir, handoff.HolderKey(env.Holder, env.Liveness))
		if err != nil {
			return err
		}

		for _, sc := range mine {
			remote = append(remote, sc.ID)
		}
	}

	var brokerErr error

	if len(remote)+len(names) > 0 {
		var removed []handoff.Sidecar

		removed, brokerErr = a.releaseBroker(stateDir, env, remote, names)

		if !req.All {
			a.releaseAnchors(e, h, stateDir, removed)
		}
	}

	if len(remote)+len(names) > 0 && len(local) == 0 && !req.All && req.Domain == nil && req.Resource == "" {
		a.reportRisk(h)

		return brokerErr
	}

	req.IDs = local

	res, err := e.Release(req)
	if res != nil {
		for _, rec := range res.Released {
			fmt.Fprintf(a.deps.Stdout, "released %s\n", rec.ID)
		}

		for _, line := range standingLines(res.Standing) {
			fmt.Fprintln(a.deps.Stdout, line)
		}

		if err == nil && len(res.Released) == 0 && brokerErr == nil {
			fmt.Fprintln(a.deps.Stdout, "no leases to release")
		}
	}

	a.reportRisk(h)

	return errors.Join(brokerErr, err)
}

func (a *app) releaseRequest(cmd *cobra.Command, ids []string) (lease.ReleaseRequest, error) {
	all, err := cmd.Flags().GetBool("all")
	if err != nil {
		return lease.ReleaseRequest{}, fmt.Errorf("reading --all: %w", err)
	}

	resource, err := resourceName(cmd)
	if err != nil {
		return lease.ReleaseRequest{}, err
	}

	given := 0

	for _, set := range []bool{len(ids) > 0, domainGiven(cmd), resource != "", all} {
		if set {
			given++
		}
	}

	if given > 1 {
		return lease.ReleaseRequest{}, fmt.Errorf("%w: name lease IDs, a domain, a resource, or --all, not a mix",
			exitcode.ErrUsage)
	}

	req := lease.ReleaseRequest{IDs: ids, All: all, Resource: resource}

	if domainGiven(cmd) {
		d, err := a.domain(cmd)
		if err != nil {
			return lease.ReleaseRequest{}, err
		}

		req.Domain = &d
	}

	return req, nil
}
