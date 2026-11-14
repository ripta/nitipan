package cli

import (
	"fmt"
	"math"
	"time"

	"github.com/spf13/cobra"

	"github.com/space-armada/nitipan/internal/adapter"
	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/lease"
)

// waitMargin is added to the time left before a deadline when recall prints the wait that takes the
// keys back, so the wait's last attempt runs after the deadline.
const waitMargin = 10 * time.Second

func (a *app) recallCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "recall <lease-id> --deadline <duration>",
		Short: "Give a sub-lease you delegated a deadline to finish",
		Long: `Give a sub-lease of a lease you hold a deadline. The sub-lessee is told, and it has until the deadline
to finish, commit, and release. If it still holds the sub-lease then, the sub-lease ends as a revoke
would, and its keys go back to your lease. Only the holder of the sub-lease's parent lease can run it.

--deadline is required, such as 30m or 2h. It is the time the sub-lessee has to finish, commit, and
release, and nitipan picks no default. A second recall of the same sub-lease replaces the deadline.
--cancel takes a recall back, and the sub-lessee is told. There is no timer. The first nitipan call by
anyone after the deadline ends the sub-lease, and your acquire --wait is such a call.

Recall prints the sub-lessee's handle and agent name and the absolute deadline. Tell the sub-lessee
out of band, for example through SendMessage or mail. It also learns on its next nitipan call. Recall
prints an acquire --wait command on your whole key set. That claim waits until the sub-lease ends and
then returns the keys, and it exits 11 if other delegations remain.

Subagents share their parent's holder, so any subagent of yours can recall. A recall can end a live
session, so name the sub-lease you mean.`,
		Args: oneArg("lease ID"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.wrapErr(a.runRecall(cmd, args[0]))
		},
	}

	cmd.Flags().String("deadline", "", "time the sub-lessee has to finish, commit, and release, such as 30m or 2h")
	cmd.Flags().Bool("cancel", false, "take the recall back instead of making one")

	return cmd
}

func (a *app) runRecall(cmd *cobra.Command, id string) error {
	cancel, err := cmd.Flags().GetBool("cancel")
	if err != nil {
		return fmt.Errorf("reading --cancel: %w", err)
	}

	within, err := deadlineFlag(cmd, cancel)
	if err != nil {
		return err
	}

	h, err := a.holder()
	if err != nil {
		return err
	}

	e, err := a.engine()
	if err != nil {
		return err
	}

	if cancel {
		return a.runCancelRecall(e, h, id)
	}

	res, err := e.Recall(h, id, within)
	if err != nil {
		return err
	}

	holder := res.Lease.Holder

	if res.Ended {
		fmt.Fprintf(a.deps.Stdout, "ended sub-lease %s held by handle %s, agent name %s\n", res.Lease.ID, orNone(holder.Handle),
			orNone(holder.Name))
		fmt.Fprintf(a.deps.Stdout, "  its holder had died, so no recall was written and no tombstone either\n")
		fmt.Fprintf(a.deps.Stdout, "  its keys went back to lease %s\n", res.Parent.ID)

		return nil
	}

	deadline := res.Recall.Deadline.UTC()
	wait := time.Duration(math.Ceil(within.Seconds()))*time.Second + waitMargin

	fmt.Fprintf(a.deps.Stdout, "recalled sub-lease %s held by handle %s, agent name %s\n", res.Lease.ID, orNone(holder.Handle),
		orNone(holder.Name))
	fmt.Fprintf(a.deps.Stdout, "  deadline: %s\n", deadline.Format(time.RFC3339))
	fmt.Fprintf(a.deps.Stdout, "  if it has not released by then, its keys go back to lease %s\n", res.Parent.ID)
	fmt.Fprintf(a.deps.Stdout, "Tell the sub-lessee out of band, for example through SendMessage or mail to its handle. It also "+
		"learns on its next nitipan call.\n")
	fmt.Fprintf(a.deps.Stdout, "To take the keys back, run: nitipan acquire --wait %s %s\n", wait, lease.ClaimFlags(res.Parent.Domain))

	return nil
}

func (a *app) runCancelRecall(e *lease.Engine, h adapter.Holder, id string) error {
	res, err := e.CancelRecall(h, id)
	if err != nil {
		return err
	}

	holder := res.Lease.Holder
	fmt.Fprintf(a.deps.Stdout, "cancelled the recall of sub-lease %s held by handle %s, agent name %s\n", res.Lease.ID,
		orNone(holder.Handle), orNone(holder.Name))
	fmt.Fprintf(a.deps.Stdout, "Tell the sub-lessee out of band if it is waiting on it. It also learns on its next nitipan call.\n")

	return nil
}

// deadlineFlag reads --deadline. A recall needs a positive duration, and a cancel takes none.
func deadlineFlag(cmd *cobra.Command, cancel bool) (time.Duration, error) {
	given := cmd.Flags().Changed("deadline")

	switch {
	case cancel && given:
		return 0, fmt.Errorf("%w: --cancel takes no --deadline", exitcode.ErrUsage)
	case cancel:
		return 0, nil
	case !given:
		return 0, fmt.Errorf("%w: --deadline is required. It is the time the sub-lessee has to finish, commit, and release, "+
			"such as 30m or 2h", exitcode.ErrUsage)
	}

	raw, err := cmd.Flags().GetString("deadline")
	if err != nil {
		return 0, fmt.Errorf("reading --deadline: %w", err)
	}

	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%w: --deadline needs a duration such as 30m or 2h, got %q", exitcode.ErrUsage, raw)
	}

	if d <= 0 {
		return 0, fmt.Errorf("%w: --deadline must be a positive duration, got %q. It is the time the sub-lessee has to "+
			"finish, commit, and release", exitcode.ErrUsage, raw)
	}

	return d, nil
}
