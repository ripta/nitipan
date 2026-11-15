package node

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/space-armada/nitipan/internal/handoff"
	"github.com/space-armada/nitipan/internal/lease"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/state"
	"github.com/space-armada/nitipan/internal/wire"
)

// snapshot reads every lease in the state directory and returns the mirror to send.
//
// A record this binary can use travels byte for byte, so a V1 record is mirrored with no change. One
// it cannot use, such as a record of a higher major, travels as an unknown holder with an all
// footprint, because every local claim refuses it.
func (n *Node) snapshot() (wire.Snapshot, error) {
	dir, err := state.OpenRead(n.opts.StateDir, state.Options{UID: n.opts.UID})
	if err != nil {
		return wire.Snapshot{}, err
	}

	// The derived files are listed before the leases are scanned. A file listed here belongs to a lease
	// that existed at the scan unless it ended since, so a lease claimed in between never loses its file.
	derived, err := handoff.DerivedIDs(n.opts.StateDir)
	if err != nil {
		n.warn(fmt.Sprintf("listing the derived entries: %v", err))
	}

	recalls, recallsErr := lease.ReadRecalls(dir)

	files, err := dir.Scan(state.LeasesDir)
	if err != nil {
		return wire.Snapshot{}, err
	}

	budget := &deriveBudget{until: time.Now().Add(n.deriveBudget())}
	leases := make([]wire.MirroredLease, 0, len(files))
	present := make(map[string]bool, len(files))
	records := make(map[string]*record.Record, len(files))

	for _, f := range files {
		present[f.ID] = true

		m, rec := mirrorOf(f)
		records[f.ID] = rec

		if rec != nil {
			m.Derived = n.derivedFor(rec, budget)
		}

		leases = append(leases, m)
	}

	n.pruneDerived(derived, present)

	sort.Slice(leases, func(i, j int) bool { return leases[i].ID < leases[j].ID })

	judged := n.applyRecalls(leases, recalls, recallsErr, n.recallViews(records))

	if judged.next.IsZero() {
		n.recallNext.Store(0)
	} else {
		n.recallNext.Store(judged.next.UnixNano())
	}

	return wire.Snapshot{Version: wire.Current, Leases: judged.leases}, nil
}

func (n *Node) deriveBudget() time.Duration {
	if n.opts.DeriveBudget > 0 {
		return n.opts.DeriveBudget
	}

	return DefaultDeriveBudget
}

// mirrorOf builds the mirror of one file. It also returns the decoded record when the node can use it.
func mirrorOf(f state.File) (wire.MirroredLease, *record.Record) {
	if f.BadName {
		return unknownLease(f.ID, "the file name is not a lease ID", ""), nil
	}

	rec, err := record.Decode(f.Data)

	var bad *record.UnusableError

	switch {
	case errors.As(err, &bad):
		version := ""
		if bad.Version != (record.Version{}) {
			version = bad.Version.String()
		}

		return unknownLease(f.ID, string(bad.Reason), version), nil
	case err != nil:
		return unknownLease(f.ID, err.Error(), ""), nil
	case rec.ID != f.ID:
		return unknownLease(f.ID, "the record's ID differs from its file name", ""), nil
	}

	return wire.MirroredLease{ID: f.ID, Record: append([]byte(nil), f.Data...)}, rec
}

func unknownLease(id, reason, version string) wire.MirroredLease {
	return wire.MirroredLease{ID: id, Unknown: &wire.UnknownLease{Reason: reason, Version: version, Footprint: wire.FootprintAll}}
}

// recalledOut is the mirror's judgement of the recalls it read. It holds the next time a recall in
// force reaches its deadline, so the event loop knows when to resend.
type recalledOut struct {
	leases []wire.MirroredLease
	next   time.Time
}

// applyRecalls drops each sub-lease whose recall is in force and past its deadline, so discovery
// reports the parent as the owner of those keys. It uses the rule the locked calls and the hook use.
//
// A sub-lease whose own holder is dead is left in the mirror. Nobody can receive its tombstone, and
// the clearing that removes it is the locked call's job. A recall record or a lease record this binary
// cannot use ends nothing, and is warned about once per change. So does a recalls directory that could
// not be read, which the caller passes as recallsErr.
//
// The node never writes lease state here. The locked call that finds the deadline passed removes the
// sub-lease, and this only keeps discovery from saying the ex-sub-lessee still owns the keys.
func (n *Node) applyRecalls(leases []wire.MirroredLease, recalls []*lease.RecallEntry, recallsErr error, view func(id string) lease.RecallLease) recalledOut {
	out := recalledOut{leases: leases}
	now := n.opts.Now()
	ended := map[string]bool{}
	warned := map[string]string{}

	if recallsErr != nil {
		warned[""] = fmt.Sprintf("%v, so no recall ends a sub-lease in the mirror", recallsErr)
	}

	for _, j := range lease.JudgeRecalls(recalls, view, now) {
		rc := j.Entry

		if rc.Bad != nil {
			warned[rc.ID] = fmt.Sprintf("the recall record %s/%s.json cannot be used by this node (%s), so it ends no sub-lease",
				state.RecallsDir, rc.ID, rc.Bad.Reason)

			continue
		}

		switch {
		case j.Verdict.Status == lease.RecallUnusable:
			warned[rc.ID] = fmt.Sprintf("the recall of sub-lease %s is not enforced in the mirror: %s", rc.ID, j.Verdict.Why)
		case j.Verdict.Status == lease.RecallInForce:
			if out.next.IsZero() || rc.Rec.Deadline.Before(out.next) {
				out.next = rc.Rec.Deadline
			}
		case j.Ended:
			ended[rc.Rec.ID] = true
		}
	}

	n.warnRecalls(warned)

	if len(ended) == 0 {
		return out
	}

	kept := make([]wire.MirroredLease, 0, len(leases))

	for _, m := range leases {
		if !ended[m.ID] {
			kept = append(kept, m)
		}
	}

	out.leases = kept

	return out
}

// warnRecalls reports each problem once until it changes or clears, since the mirror is rebuilt on
// every heartbeat.
func (n *Node) warnRecalls(now map[string]string) {
	n.recallWarnMu.Lock()
	defer n.recallWarnMu.Unlock()

	for id, msg := range now {
		if n.recallWarned[id] != msg {
			n.warn(msg)
		}
	}

	n.recallWarned = now
}

// recallViews shows the scanned lease files to the judgement of a recall. A holder's liveness can cost
// a directory read or a database query, so it is looked up only for a lease some recall names, and
// once per snapshot.
func (n *Node) recallViews(records map[string]*record.Record) func(id string) lease.RecallLease {
	seen := map[string]lease.RecallLease{}

	return func(id string) lease.RecallLease {
		if v, ok := seen[id]; ok {
			return v
		}

		rec, exists := records[id]
		v := lease.RecallLease{Exists: exists, Rec: rec}

		if rec != nil && n.opts.Checker != nil {
			v.Live = n.opts.Checker.Check(rec.Envelope)
		}

		seen[id] = v

		return v
	}
}
