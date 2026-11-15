package node

import (
	"errors"
	"fmt"
	"time"

	"github.com/space-armada/nitipan/internal/handoff"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/state"
	"github.com/space-armada/nitipan/internal/wire"
)

// DefaultDeriveBudget is how long one snapshot may spend deriving the entries of leases that have none. The
// snapshot runs on the loop that heartbeats and renews, and a git walk can take 30 seconds, so unbounded work
// there could keep the node quiet past a term. A lease left over reads as unknown until the next snapshot.
const DefaultDeriveBudget = 5 * time.Second

// deriveBudget is the time one snapshot has left to derive. The lease that starts a derivation is allowed to
// finish it, so a snapshot always makes progress.
type deriveBudget struct{ until time.Time }

func (b *deriveBudget) spent() bool { return time.Now().After(b.until) }

// derivedFor returns the repo entries a mirrored machine lease carries. A sidecar written for the same
// keys is used as it stands. A lease with none, or whose keys changed, is derived here, which covers a
// record a V1 binary wrote. A sidecar this node cannot read, such as one of a higher major, is left
// alone and the lease reads as unknown.
func (n *Node) derivedFor(rec *record.Record, budget *deriveBudget) *wire.Derived {
	if rec.Domain.Kind != record.KindPath || rec.Reach != record.ReachMachine {
		return nil
	}

	have, err := handoff.ReadDerived(n.opts.StateDir, rec.ID)

	switch {
	case errors.Is(err, handoff.ErrUnusableFile):
		return nil
	case err == nil && have.Matches(rec.Domain.Keys) && !n.derivationRetryable(have):
		return have.Wire()
	}

	if n.opts.Roots == nil || budget.spent() {
		return nil
	}

	d := handoff.Derive(n.opts.Roots, rec.ID, handoff.DerivedByNode, rec.Domain.Keys, n.opts.Now())

	// The CLI writes its file right after the lease appears, and the node's own derivation takes longer
	// than that. A file the CLI wrote meanwhile wins, since only it marks a holder that can read a notice.
	// A file this node cannot read that appeared meanwhile, such as one of a higher major, is never
	// rewritten, and the lease reads as unknown.
	unreadable := false

	werr := n.withLock(func(l *state.Locked) error {
		cur, err := handoff.ReadDerived(n.opts.StateDir, rec.ID)

		switch {
		case errors.Is(err, handoff.ErrUnusableFile):
			unreadable = true

			return nil
		case err == nil && cur.By == handoff.DerivedByCLI && cur.Matches(rec.Domain.Keys):
			d = cur

			return nil
		}

		return handoff.WriteDerived(l, d)
	})
	if werr != nil {
		n.warn(fmt.Sprintf("writing the derived entries of lease %s: %v", rec.ID, werr))
	}

	if unreadable {
		return nil
	}

	return d.Wire()
}

// derivationRetryable reports whether a failed derivation the node made itself is old enough to try
// again. One the CLI made is left to the CLI, which runs in the agent's shell and sees what the node
// cannot.
func (n *Node) derivationRetryable(d handoff.Derived) bool {
	return d.State == wire.DerivedFailed && d.By == handoff.DerivedByNode && n.opts.Now().Sub(d.At) > handoff.DerivedRetry
}

// pruneDerived removes the derived files of leases that no longer exist.
func (n *Node) pruneDerived(listed []string, present map[string]bool) {
	var gone []string

	for _, id := range listed {
		if !present[id] {
			gone = append(gone, id)
		}
	}

	if len(gone) == 0 {
		return
	}

	err := n.withLock(func(l *state.Locked) error {
		var errs []error

		for _, id := range gone {
			errs = append(errs, handoff.RemoveDerived(l, id))
		}

		return errors.Join(errs...)
	})
	if err != nil {
		n.warn(fmt.Sprintf("removing derived entries: %v", err))
	}
}

// Sides of an overlap, for dedupe and addressing.
const (
	sideGrant  = "grant"
	sideMirror = "mirror"
)

func overlapKey(o wire.Overlap, side string) string {
	return "overlap/" + o.ID + "/" + side
}

// applyOverlaps writes a notice for each overlap that touches a lease on this node, once per pair and
// side. The grant's holder always has a sidecar. A mirrored machine lease gets a notice only when the
// CLI wrote its derived sidecar, which marks a holder whose binary can deliver one.
func (n *Node) applyOverlaps(overlaps []wire.Overlap) {
	if len(overlaps) == 0 {
		return
	}

	label := n.Label()

	var lv localView

	loaded := false

	for _, o := range overlaps {
		if o.Grant.Node == label && !n.overlapAwaitsReader(overlapKey(o, sideGrant)) {
			if sc, err := handoff.ReadSidecar(n.opts.StateDir, o.Grant.Lease); err == nil && n.book.once(overlapKey(o, sideGrant)) {
				n.notice(sc.Holder, o.Grant.Lease, "overlap-"+o.Mirror.Lease, 0, fmt.Sprintf("your %s lease %s names repo files "+
					"that the machine lease %s of %s also covers. Nothing refuses either one. Ask them before you both change the same files",
					o.Grant.Reach, o.Grant.Lease, o.Mirror.Lease, mirrorOwner(o.Mirror)))
			}
		}

		if o.Mirror.Node != label {
			continue
		}

		// A lease whose derived file the node wrote is a V1 record or a holder that cannot read a notice. The
		// check comes before the once-per-pair mark, so a file the CLI writes later still gets its notice.
		d, err := handoff.ReadDerived(n.opts.StateDir, o.Mirror.Lease)
		if err != nil || d.By != handoff.DerivedByCLI || !n.book.once(overlapKey(o, sideMirror)) {
			continue
		}

		if !loaded {
			v, err := n.local()
			if err != nil {
				n.warn(fmt.Sprintf("reading the leases for an overlap notice: %v", err))

				return
			}

			lv, loaded = v, true
		}

		mine, ok := lv.recs[o.Mirror.Lease]
		if !ok {
			continue
		}

		n.notice(mine.Envelope, o.Mirror.Lease, "overlap-"+o.Grant.Lease, 0, fmt.Sprintf("your machine lease %s covers repo files "+
			"that the %s lease %s of %s also names. Nothing refuses either one. Ask them before you both change the same files",
			o.Mirror.Lease, o.Grant.Reach, o.Grant.Lease, mirrorOwner(o.Grant)))
	}
}

func mirrorOwner(f wire.Found) string {
	name := f.Qualified
	if name == "" {
		name = f.Holder.Agent
	}

	if f.Stale {
		name += " (its node has gone quiet)"
	}

	return name
}
