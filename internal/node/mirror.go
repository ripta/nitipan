package node

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/space-armada/nitipan/internal/handoff"
	"github.com/space-armada/nitipan/internal/lease"
	"github.com/space-armada/nitipan/internal/liveness"
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

	recalls, recallsErr := n.readRecalls(dir)

	files, err := dir.Scan(state.LeasesDir)
	if err != nil {
		return wire.Snapshot{}, err
	}

	budget := &deriveBudget{until: time.Now().Add(n.deriveBudget())}
	leases := make([]wire.MirroredLease, 0, len(files))
	present := make(map[string]bool, len(files))
	views := make(map[string]lease.RecallLease, len(files))

	for _, f := range files {
		present[f.ID] = true

		m, rec := mirrorOf(f)
		views[f.ID] = n.viewOf(rec)

		if rec != nil {
			m.Derived = n.derivedFor(rec, budget)
		}

		leases = append(leases, m)
	}

	n.pruneDerived(derived, present)

	sort.Slice(leases, func(i, j int) bool { return leases[i].ID < leases[j].ID })

	judged := n.applyRecalls(leases, recalls, recallsErr, views)

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
func (n *Node) applyRecalls(leases []wire.MirroredLease, recalls []*mirroredRecall, recallsErr error, views map[string]lease.RecallLease) recalledOut {
	out := recalledOut{leases: leases}
	now := n.opts.Now()
	ended := map[string]bool{}
	warned := map[string]string{}

	if recallsErr != nil {
		warned[""] = fmt.Sprintf("%v, so no recall ends a sub-lease in the mirror", recallsErr)
	}

	for _, rc := range recalls {
		if rc.rec == nil {
			warned[rc.id] = fmt.Sprintf("the recall record %s/%s.json cannot be used by this node (%s), so it ends no sub-lease",
				state.RecallsDir, rc.id, rc.why)

			continue
		}

		verdict := lease.JudgeRecall(rc.rec, views[rc.rec.ID], views[rc.rec.Parent], now)

		switch verdict.Status {
		case lease.RecallUnusable:
			warned[rc.id] = fmt.Sprintf("the recall of sub-lease %s is not enforced in the mirror: %s", rc.id, verdict.Why)
		case lease.RecallInForce:
			if out.next.IsZero() || rc.rec.Deadline.Before(out.next) {
				out.next = rc.rec.Deadline
			}
		case lease.RecallPastDeadline:
			if views[rc.rec.ID].Live.Status != liveness.Dead {
				ended[rc.rec.ID] = true
			}
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

// mirroredRecall is one file in recalls/. The record is nil when this binary cannot use it, and why
// says so.
type mirroredRecall struct {
	id  string
	rec *record.Recall
	why string
}

// readRecalls reads every file in recalls/. It runs before the leases are scanned. An expiry removes
// the sub-lease and drops the recall last, so a sub-lease the later scan still finds has its recall
// in hand here, and one the scan no longer finds is gone.
func (n *Node) readRecalls(dir *state.Dir) ([]*mirroredRecall, error) {
	files, err := dir.Scan(state.RecallsDir)
	if err != nil {
		return nil, fmt.Errorf("reading the recalls: %w", err)
	}

	out := make([]*mirroredRecall, 0, len(files))

	for _, f := range files {
		mr := &mirroredRecall{id: f.ID}

		rc, err := record.DecodeRecall(f.Data)

		var bad *record.UnusableError

		switch {
		case f.BadName:
			mr.why = "the file name is not a lease ID"
		case errors.As(err, &bad):
			mr.why = string(bad.Reason)
		case err != nil:
			mr.why = err.Error()
		case rc.ID != f.ID:
			mr.why = "the record's ID differs from its file name"
		default:
			mr.rec = rc
		}

		out = append(out, mr)
	}

	return out, nil
}

// viewOf shows a scanned lease file to the judgement of a recall.
func (n *Node) viewOf(rec *record.Record) lease.RecallLease {
	v := lease.RecallLease{Exists: true, Rec: rec}

	if rec != nil && n.opts.Checker != nil {
		v.Live = n.opts.Checker.Check(rec.Envelope)
	}

	return v
}
