package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/space-armada/nitipan/internal/adapter"
	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/lease"
)

func (a *app) releaseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "release [lease-id ...]",
		Short: "Give up a lease you hold",
		Long: `Give up leases you hold. Name them by lease ID, by the domain flags, or with --all.

The domain flags match a lease whose canonical keys equal them, using the keys stored in the lease,
so the lease of a deleted worktree can still be released. With no target, release refuses and lists
your leases.`,
		RunE: func(cmd *cobra.Command, ids []string) error {
			return a.wrapErr(a.runRelease(cmd, ids))
		},
	}

	addDomainFlags(cmd, "whose lease to release")
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

	e, err := a.engine()
	if err != nil {
		return err
	}

	res, err := e.Release(req)
	if res != nil {
		for _, rec := range res.Released {
			fmt.Fprintf(a.deps.Stdout, "released %s\n", rec.ID)
		}

		if err == nil && len(res.Released) == 0 {
			fmt.Fprintln(a.deps.Stdout, "no leases to release")
		}
	}

	return err
}

func (a *app) releaseRequest(cmd *cobra.Command, ids []string) (lease.ReleaseRequest, error) {
	all, err := cmd.Flags().GetBool("all")
	if err != nil {
		return lease.ReleaseRequest{}, fmt.Errorf("reading --all: %w", err)
	}

	given := 0

	for _, set := range []bool{len(ids) > 0, domainGiven(cmd), all} {
		if set {
			given++
		}
	}

	if given > 1 {
		return lease.ReleaseRequest{}, fmt.Errorf("%w: name lease IDs, a domain, or --all, not a mix", exitcode.ErrUsage)
	}

	req := lease.ReleaseRequest{IDs: ids, All: all}

	if domainGiven(cmd) {
		d, err := a.domain(cmd)
		if err != nil {
			return lease.ReleaseRequest{}, err
		}

		req.Domain = &d
	}

	return req, nil
}
