package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/lease"
	"github.com/space-armada/nitipan/internal/listing"
	"github.com/space-armada/nitipan/internal/liveness"
)

func (a *app) advertiseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "advertise",
		Short: "Update your session name, permission mode, or a lease's description",
		Long: `Update what your leases say about the session, without naming keys again. Run it after /rename.

The name and the permission mode belong to the holder, so they change in every lease you hold. The
description belongs to one lease. A flag you leave out keeps its stored value.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.wrapErr(a.runAdvertise(cmd))
		},
	}

	cmd.Flags().String("name", "", "the session name others can reach you by, such as the /rename name")
	cmd.Flags().String("permission-mode", "", "the session's permission mode, as the agent names it")
	cmd.Flags().String("description", "", "what this session does with the lease named by --lease")
	cmd.Flags().String("lease", "", "the lease a description applies to, when you hold several")

	return cmd
}

func (a *app) runAdvertise(cmd *cobra.Command) error {
	req := lease.AdvertiseRequest{
		Name:        optString(cmd, "name"),
		Mode:        optString(cmd, "permission-mode"),
		Description: optString(cmd, "description"),
	}

	if req.Name == nil && req.Mode == nil && req.Description == nil {
		return fmt.Errorf("%w: pass --name, --permission-mode, or --description", exitcode.ErrUsage)
	}

	id, err := cmd.Flags().GetString("lease")
	if err != nil {
		return fmt.Errorf("reading --lease: %w", err)
	}

	req.LeaseID = id

	h, err := a.holder()
	if err != nil {
		return err
	}

	req.Holder = h

	e, err := a.engine()
	if err != nil {
		return err
	}

	res, err := e.Advertise(req)
	if err != nil {
		return err
	}

	out := listing.Output{}
	for _, rec := range res.Leases {
		out.Leases = append(out.Leases, listing.FromRecord(rec, liveness.Result{Status: liveness.Alive}))
	}

	fmt.Fprintln(a.deps.Stdout, "advertised")

	return listing.WriteText(a.deps.Stdout, out)
}
