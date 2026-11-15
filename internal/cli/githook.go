package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/gitfence"
	"github.com/space-armada/nitipan/internal/lease"
	"github.com/space-armada/nitipan/internal/state"
)

// transactionLimit bounds the ref transaction a git hook feeds in. A pack-refs run lists every ref.
const transactionLimit = 64 << 20

// refusedError is a ref update that the fence refused. It exits with the refused status.
type refusedError struct {
	text string
}

// Error implements error.
func (e *refusedError) Error() string { return e.text }

// Is matches the refused status.
func (e *refusedError) Is(target error) bool { return target == exitcode.ErrRefused }

// hookRefTransaction is the command git's reference-transaction hook runs, through the script that
// `setup git --install` writes. It is read-only. It takes no lock, and it delivers and removes
// nothing.
//
// A caller with no agent variables is the user's own terminal and passes. A caller with agent
// variables and no matching ancestor fails closed. The command stays out of help like the rest of
// the hook commands, and its usage errors never name it.
func (a *app) hookRefTransaction() *cobra.Command {
	return &cobra.Command{
		Use:    "reference-transaction <state>",
		Hidden: true,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("%w: unrecognized arguments", exitcode.ErrUsage)
			}

			return nil
		},
		RunE: func(_ *cobra.Command, args []string) error {
			return a.runRefTransaction(args[0])
		},
	}
}

func (a *app) runRefTransaction(phase string) error {
	if phase != "prepared" {
		return nil
	}

	updates, err := a.readTransaction()
	if err != nil {
		return err
	}

	if !anyJudged(updates) {
		return nil
	}

	res := a.resolver().Resolve()
	if res.Holder == nil {
		if res.Failure == nil {
			return nil
		}

		return fmt.Errorf("git refused to update a branch or HEAD, because nitipan could not identify the calling "+
			"agent: %w. Ask the user", res.Failure)
	}

	touches, err := a.deps.Touched(updates)
	if err != nil {
		return fmt.Errorf("git refused to update a branch or HEAD, because nitipan could not work out which paths it "+
			"touches: %w", err)
	}

	if len(touches) == 0 {
		return nil
	}

	e, err := a.readEngine()
	if err != nil {
		return a.failClosed(err)
	}

	judge, err := e.NewJudge(*res.Holder)
	if err != nil {
		return a.failClosed(err)
	}

	for _, w := range judge.Warnings() {
		a.warn(errors.New(w))
	}

	var report []string

	for _, t := range touches {
		refusals := judge.Refusals(t.Checkout, t.Keys)
		if len(refusals) > 0 {
			report = append(report, refusalText(t, refusals))
		}
	}

	if len(report) == 0 {
		for _, n := range judge.RecallNotices() {
			fmt.Fprintf(a.deps.Stderr, "nitipan: %s\n", n)
		}

		return nil
	}

	return &refusedError{text: strings.Join(report, "\n")}
}

func (a *app) failClosed(err error) error {
	return fmt.Errorf("git refused to update a branch or HEAD, because nitipan could not read the leases: %w",
		a.wrapErr(err))
}

func (a *app) readTransaction() ([]gitfence.Update, error) {
	var data []byte

	if a.deps.Stdin != nil {
		var err error

		data, err = io.ReadAll(io.LimitReader(a.deps.Stdin, transactionLimit))
		if err != nil {
			return nil, fmt.Errorf("reading the ref transaction: %w", err)
		}
	}

	updates, err := gitfence.ParseUpdates(data)
	if err != nil {
		return nil, fmt.Errorf("git refused the ref update, because nitipan could not read it: %w", err)
	}

	return updates, nil
}

func anyJudged(updates []gitfence.Update) bool {
	for _, u := range updates {
		if gitfence.Judged(u) {
			return true
		}
	}

	return false
}

// refusalText describes one refused ref: each cause, then the command that keeps the commit the
// update would have made. The pin writes outside refs/heads, so the hook lets it through.
func refusalText(t gitfence.RefTouch, refusals []lease.Refusal) string {
	var b strings.Builder

	subject := "to update " + t.Ref
	if t.Ref == "HEAD" {
		subject = "to move HEAD"
	}

	fmt.Fprintf(&b, "git refused %s. It touches paths that are not yours:", subject)

	for _, r := range refusals {
		fmt.Fprintf(&b, "\n  %s", strings.ReplaceAll(r.Message, "\n", "\n  "))
	}

	if t.New != "" {
		fmt.Fprintf(&b, "\nTo keep the commit this update would have pointed at, run: git update-ref "+
			"refs/nitipan/salvage/%s %s", state.NewID(), t.New)
	}

	return b.String()
}
