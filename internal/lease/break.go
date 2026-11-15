package lease

import (
	"errors"
	"fmt"
	"io/fs"
	"slices"

	"github.com/space-armada/nitipan/internal/adapter"
	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/liveness"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/state"
)

// BreakRequest names the leases to remove. Break takes lease IDs only, so a destructive action
// always names its exact target.
type BreakRequest struct {
	// Caller is who breaks. A holder when an agent matched, an anonymous process otherwise.
	Caller adapter.Caller

	// CallerErr is set when the caller could not be identified. A break that needs no tombstone does
	// not need the caller, so it fails only when it would have to name one.
	CallerErr error

	IDs   []string
	Force bool
}

// BreakOutcome says what a break did to one target.
type BreakOutcome int

// Outcomes of breaking one target.
const (
	// Cleared means the holder was dead. No tombstone was written, because nobody can receive it.
	Cleared BreakOutcome = iota

	// Broken means the holder was alive or unknown, and a tombstone now waits for it.
	Broken

	// RemovedUnusable means the file was a record or a tombstone that nitipan cannot use. It was
	// removed with no tombstone, because the holder named in it cannot be trusted.
	RemovedUnusable
)

// BrokenLease reports one target of a break.
type BrokenLease struct {
	ID      string
	Outcome BreakOutcome

	// Tombstone is the one written for a Broken lease.
	Tombstone *record.Tombstone

	// Holder is the lease's holder, when the record could be read.
	Holder *record.Holder

	// WasTombstone is true when the target was an unusable tombstone and not a lease.
	WasTombstone bool
}

// BreakResult lists what a break removed.
type BreakResult struct {
	Targets []BrokenLease
}

// breakPlan is one target, resolved but not yet acted on.
type breakPlan struct {
	id      string
	outcome BreakOutcome
	lease   *entry
	tomb    *tombEntry
}

// Break removes leases other holders own, under the same lock as acquire.
//
// Every target is resolved before anything is removed. A dead holder's lease goes without
// force when its record allows clearing and this binary may rewrite it. Anything else needs force:
// a live or unknown holder, a record that asks for a newer nitipan, and a record that cannot be used.
// Breaking a live holder's lease leaves a tombstone addressed to that holder.
func (e *Engine) Break(req BreakRequest) (*BreakResult, error) {
	if len(req.IDs) == 0 {
		return nil, usagef("break needs one or more lease IDs")
	}

	for _, id := range req.IDs {
		if !state.ValidID(id) {
			return nil, usagef("%q is not a lease ID; break takes lease IDs, as list prints them", id)
		}
	}

	c, err := e.begin(req.Caller.Holder)
	if err != nil {
		return nil, err
	}
	defer c.close()

	plans, err := c.planBreaks(req)
	if err != nil {
		return nil, err
	}

	res := &BreakResult{}

	for _, p := range plans {
		done, err := c.execute(p, req)
		if err != nil {
			return res, err
		}

		res.Targets = append(res.Targets, done)
	}

	return res, nil
}

// planBreaks resolves every ID. All failures are reported together, each with its own status.
func (c *call) planBreaks(req BreakRequest) ([]breakPlan, error) {
	var (
		plans []breakPlan
		errs  []error
	)

	for _, id := range slices.Compact(slices.Sorted(slices.Values(req.IDs))) {
		p, err := c.planBreak(id, req)
		if err != nil {
			errs = append(errs, err)

			continue
		}

		plans = append(plans, p)
	}

	switch len(errs) {
	case 0:
		return plans, nil
	case 1:
		return nil, errs[0]
	default:
		return nil, errors.Join(errs...)
	}
}

func (c *call) planBreak(id string, req BreakRequest) (breakPlan, error) {
	if en := c.entryByID(id); en != nil {
		return c.planLease(en, req)
	}

	tombs, err := c.e.loadTombstones()
	if err != nil {
		return breakPlan{}, err
	}

	for _, te := range tombs {
		if te.ID == id {
			return c.planTombstone(te, req)
		}
	}

	return breakPlan{}, failf(exitcode.ErrNoSuchLease, "no such lease %s; run list to see the leases that exist", id)
}

func (c *call) planTombstone(te *tombEntry, req BreakRequest) (breakPlan, error) {
	if te.Tomb != nil {
		return breakPlan{}, failf(exitcode.ErrNoSuchLease, "%s is a tombstone, not a lease. The broken holder receives it on "+
			"its next call", te.ID)
	}

	if !req.Force {
		return breakPlan{}, failf(record.ErrUnusable, "tombstone %s cannot be used by this nitipan (%s); upgrade nitipan "+
			"or ask the user", te.Path, te.Bad.Reason)
	}

	return breakPlan{id: te.ID, outcome: RemovedUnusable, tomb: te}, nil
}

func (c *call) planLease(en *entry, req BreakRequest) (breakPlan, error) {
	if en.Rec == nil {
		if !req.Force {
			return breakPlan{}, unusableErr(en)
		}

		return breakPlan{id: en.ID, outcome: RemovedUnusable, lease: en}, nil
	}

	if c.mineByID(en.ID) != nil {
		return breakPlan{}, usagef("lease %s is yours. Give it up with release", en.ID)
	}

	rec := en.Rec
	dead := en.Live.Status == liveness.Dead

	switch {
	case dead && rec.Clearable() && rec.Rewritable() == nil:
		return breakPlan{id: en.ID, outcome: Cleared, lease: en}, nil
	case dead && !req.Force:
		return breakPlan{}, failf(exitcode.ErrRefused, "lease %s on %s is held by %s, which has died, but its record asks "+
			"that only a newer nitipan clear it (record version %s, this nitipan writes %s). Upgrade nitipan, "+
			"coordinate with the holder, or ask the user.", en.ID, en.pathList(), describeHolder(rec.Holder),
			rec.Version, record.Current)
	case dead:
		return breakPlan{id: en.ID, outcome: Cleared, lease: en}, nil
	case !req.Force:
		return breakPlan{}, failf(exitcode.ErrRefused, "%s", c.liveHolderRefusal(en))
	case req.CallerErr != nil:
		return breakPlan{}, fmt.Errorf("a tombstone must name who broke the lease: %w", req.CallerErr)
	}

	return breakPlan{id: en.ID, outcome: Broken, lease: en}, nil
}

// liveHolderRefusal names the holder. It never names the flag that overrides it, because an agent
// follows the command its error names.
func (c *call) liveHolderRefusal(en *entry) string {
	msg := fmt.Sprintf("lease %s on %s is held by %s", en.ID, en.pathList(), describeHolder(en.Rec.Holder))

	if en.Live.Status != liveness.Alive {
		msg += fmt.Sprintf(" (its liveness is unknown, which counts as alive: %s)", en.Live.Reason)
	}

	if en.Rec.Description != "" {
		msg += fmt.Sprintf(": %s", en.Rec.Description)
	}

	return msg + ". Breaking a live lease can interrupt the holder's work, and the holder keeps working until its " +
		"next nitipan call. Coordinate with the holder or ask the user."
}

func (c *call) execute(p breakPlan, req BreakRequest) (BrokenLease, error) {
	done := BrokenLease{ID: p.id, Outcome: p.outcome}

	if p.tomb != nil {
		done.WasTombstone = true

		return done, c.removeFile(state.TombstonesDir, p.id)
	}

	if p.lease.Rec != nil {
		holder := p.lease.Rec.Holder
		done.Holder = &holder
	}

	if p.outcome == Broken {
		t, err := c.writeTombstone(p.lease, req)
		if err != nil {
			return done, err
		}

		done.Tombstone = t
	}

	return done, c.removeFile(state.LeasesDir, p.id)
}

func (c *call) removeFile(sub, id string) error {
	err := c.l.Remove(sub, id)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("breaking %s: %w", id, err)
	}

	return nil
}

// writeTombstone publishes the notice for a broken holder. It runs before the lease is removed, so
// a crash between the two can send a notice for a lease that still exists. The other order could
// lose the only signal the holder gets.
func (c *call) writeTombstone(en *entry, req BreakRequest) (*record.Tombstone, error) {
	by, anonymous := c.breaker(req.Caller)
	rec := en.Rec

	var paths []string

	if ks, ok := en.keysOf(); ok {
		for _, k := range ks {
			paths = append(paths, k.Path)
		}
	}

	t := &record.Tombstone{
		Envelope: record.Envelope{
			Version:       record.Current,
			ID:            c.e.NewID(),
			Footprint:     record.NoneFootprint(),
			ClearWhenDead: true,
			Liveness:      rec.Liveness,
			Machine:       rec.Machine,
			Holder:        rec.Holder,
		},
		Lease:     en.ID,
		BrokenBy:  by,
		Anonymous: anonymous,
		BrokenAt:  c.e.Now().UTC(),
		Paths:     paths,
	}

	data, err := t.Encode()
	if err != nil {
		return nil, fmt.Errorf("encoding the tombstone for %s: %w", en.ID, err)
	}

	if err := c.l.Publish(state.TombstonesDir, t.ID, data); err != nil {
		return nil, fmt.Errorf("publishing the tombstone for %s: %w", en.ID, err)
	}

	return t, nil
}

// breaker returns who the tombstone names. An agent session is recorded as a holder, with the handle
// others know it by. A caller with no adapter is recorded as an anonymous process.
func (c *call) breaker(caller adapter.Caller) (record.Holder, bool) {
	if caller.Holder == nil {
		return caller.Anonymous.Record(), true
	}

	h := *caller.Holder

	by := h.Record()
	by.Handle = c.e.handleFor(h)

	if len(c.mine) > 0 {
		by.Name = c.mine[0].Rec.Holder.Name
	}

	return by, false
}
