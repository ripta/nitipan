package lease

import (
	"errors"
	"fmt"
	"io/fs"
	"slices"

	"github.com/space-armada/nitipan/internal/adapter"
	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/keys"
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

	// RemovedMark means the target was a fence mark. The holder it guarded against can claim the
	// keys again.
	RemovedMark
)

// BrokenLease reports one target of a break.
type BrokenLease struct {
	ID      string
	Outcome BreakOutcome

	// Tombstone is the one written for a Broken lease.
	Tombstone *record.Tombstone

	// Holder is the lease's holder, when the record could be read.
	Holder *record.Holder

	// ReturnedTo is the ID of the live parent that got the keys back, when the target was a sub-lease.
	ReturnedTo string

	// Standing lists the sub-leases of the target. They stand alone now, each as an ordinary lease of
	// its sub-lessee.
	Standing []*record.Record

	// WasTombstone is true when the target was an unusable tombstone and not a lease.
	WasTombstone bool

	// WasMark is true when the target was a fence mark. Mark is the mark that was removed, when it
	// could be read.
	WasMark bool
	Mark    *record.Mark

	// Hooks says, for each git checkout the broken lease's keys lie in, whether nitipan's git hooks
	// were in force there. A broken lease with the hooks in force is fenced against its holder's
	// next ref update. It is set only for a Broken lease.
	Hooks []GitHooks
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
	mark    *markEntry
}

// Break removes leases other holders own, under the same lock as acquire.
//
// Every target is resolved before anything is removed. A dead holder's lease goes without
// force when its record allows clearing and this binary may rewrite it. Anything else needs force:
// a live or unknown holder, a record that asks for a newer nitipan, and a record that cannot be used.
// Breaking a live holder's lease leaves a tombstone addressed to that holder.
//
// Nothing cascades. Breaking a parent removes only the parent, and its live sub-leases are told and
// rewritten without the link in the same call. Breaking a sub-lease returns its keys to the parent,
// and tells the parent.
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

	marks, err := c.e.loadMarks()
	if err != nil {
		return breakPlan{}, err
	}

	for _, me := range marks {
		if me.ID == id {
			return c.planMark(me, req)
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

// planMark lets anyone but the guarded holder remove a fence mark. A mark that nitipan cannot use
// needs force, like an unusable tombstone.
func (c *call) planMark(me *markEntry, req BreakRequest) (breakPlan, error) {
	if me.Mark == nil {
		if !req.Force {
			return breakPlan{}, failf(record.ErrUnusable, "fence mark %s cannot be used by this nitipan (%s); upgrade nitipan "+
				"or ask the user", me.Path, me.Bad.Reason)
		}

		return breakPlan{id: me.ID, outcome: RemovedUnusable, mark: me}, nil
	}

	if req.Caller.Holder != nil && c.e.isHolder(*req.Caller.Holder, me.Mark.Envelope) {
		return breakPlan{}, failf(exitcode.ErrNotYours, "fence mark %s guards against you. Only someone else can remove it; "+
			"ask the user", me.ID)
	}

	return breakPlan{id: me.ID, outcome: RemovedMark, mark: me}, nil
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
			"coordinate with the holder, or ask the user.", en.ID, en.domainList(), describeHolder(rec.Holder),
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
	msg := fmt.Sprintf("lease %s on %s is held by %s", en.ID, en.domainList(), describeHolder(en.Rec.Holder))

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

	if p.mark != nil {
		done.WasMark, done.Mark = true, p.mark.Mark

		return done, c.removeFile(state.MarksDir, p.id)
	}

	if p.lease.Rec == nil {
		return done, c.removeLeaseFile(p.lease)
	}

	holder := p.lease.Rec.Holder
	done.Holder = &holder

	var tomb *record.Tombstone

	parentHow, childHow := "ended because its holder died", "its holder died, and nitipan cleared it"

	if p.outcome == Broken {
		tomb = c.newTombstone(p.lease, req)
		tomb.Fenced = true
		parentHow = "was broken by " + describeBreaker(tomb)
		childHow = "it was broken by " + describeBreaker(tomb)
	}

	kids, err := c.beforeRemoval(p.lease, parentHow, childHow)
	if err != nil {
		return done, err
	}

	if tomb != nil {
		held, markID, err := c.fence(p.lease, tomb)
		if err != nil {
			return done, err
		}

		if err := c.publishTombstone(tomb); err != nil {
			return done, c.unfence(markID, err)
		}

		done.Tombstone = tomb
		done.Hooks = c.hooksOf(held)
	}

	if err := c.removeLeaseFile(p.lease); err != nil {
		return done, err
	}

	done.Standing, err = c.afterRemoval(p.lease, kids)
	if err != nil {
		return done, err
	}

	if parent := c.liveParent(p.lease); parent != nil {
		done.ReturnedTo = parent.ID
	}

	return done, nil
}

// fence guards the keys of a broken path lease with a mark, and returns the keys it guards and the
// mark's ID. The mark is written in the same locked call as the tombstone, and before it. A mark
// that fails to write then leaves no tombstone behind to tell a holder whose lease still stands
// that it was broken. Both exist before the lease is removed, so a reader that finds the lease gone
// finds the tombstone and the mark.
func (c *call) fence(en *entry, tomb *record.Tombstone) ([]keys.Key, string, error) {
	held, ok := en.keysOf()
	if !ok {
		return nil, "", nil
	}

	if err := runHook(c.e.Hooks.BeforeBreakMark); err != nil {
		return nil, "", err
	}

	markID, err := c.publishMark(tomb, held)
	if err != nil {
		return nil, "", err
	}

	return held, markID, nil
}

// unfence takes back a mark whose tombstone could not be written, so the lease the break left
// standing does not stay fenced against its own holder. It returns cause, joined with the failure
// to take the mark back.
func (c *call) unfence(markID string, cause error) error {
	if markID == "" {
		return cause
	}

	if err := c.removeFile(state.MarksDir, markID); err != nil {
		return errors.Join(cause, err)
	}

	return cause
}

// hooksOf records whether the git hooks that enforce the mark are in force where the keys lie.
func (c *call) hooksOf(held []keys.Key) []GitHooks {
	if held == nil {
		return nil
	}

	hooks, err := c.e.hooksFor(held)
	if err != nil {
		c.e.Notify.warn(err)
	}

	return hooks
}

// removeLeaseFile is removeLease for a break, which treats a file that is already gone as removed.
func (c *call) removeLeaseFile(en *entry) error {
	err := c.removeLease(en)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("breaking %s: %w", en.ID, err)
	}

	return nil
}

func (c *call) removeFile(sub, id string) error {
	err := c.l.Remove(sub, id)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("breaking %s: %w", id, err)
	}

	return nil
}

// newTombstone builds the notice for a broken holder.
func (c *call) newTombstone(en *entry, req BreakRequest) *record.Tombstone {
	by, anonymous := c.breaker(req.Caller)

	return c.tombstoneBy(en, by, anonymous)
}

// tombstoneBy builds the tombstone for a lease that the named breaker ended.
func (c *call) tombstoneBy(en *entry, by record.Holder, anonymous bool) *record.Tombstone {
	rec := en.Rec

	var paths []string

	if ks, ok := en.keysOf(); ok {
		for _, k := range ks {
			paths = append(paths, k.Path)
		}
	}

	if name, ok := en.resource(); ok {
		paths = append(paths, name)
	}

	return &record.Tombstone{
		Envelope: record.Envelope{
			Version:       record.TombstoneVersion,
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
}

// publishTombstone writes the tombstone before the lease is removed, so a crash between the two can
// send a notice for a lease that still exists. The other order could lose the only signal the holder
// gets.
func (c *call) publishTombstone(t *record.Tombstone) error {
	data, err := t.Encode()
	if err != nil {
		return fmt.Errorf("encoding the tombstone for %s: %w", t.Lease, err)
	}

	if err := c.l.Publish(state.TombstonesDir, t.ID, data); err != nil {
		return fmt.Errorf("publishing the tombstone for %s: %w", t.Lease, err)
	}

	return nil
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
