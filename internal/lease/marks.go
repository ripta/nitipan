package lease

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/space-armada/nitipan/internal/adapter"
	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/keys"
	"github.com/space-armada/nitipan/internal/liveness"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/state"
)

const reasonBadMarkName record.Reason = "file name is not a mark ID"

// markEntry is one file in marks/. Mark is nil when the file cannot be used, and Bad says why.
type markEntry struct {
	ID   string
	Path string
	Mark *record.Mark
	Bad  *record.UnusableError
	Live liveness.Result
}

// loadMarks reads and classifies every file in marks/, in file-name order.
func (e *Engine) loadMarks() ([]*markEntry, error) {
	files, err := e.Dir.Scan(state.MarksDir)
	if err != nil {
		return nil, fmt.Errorf("reading the fence marks: %w", err)
	}

	out := make([]*markEntry, 0, len(files))

	for _, f := range files {
		me := &markEntry{ID: f.ID, Path: filepath.Join(e.Dir.Path(), state.MarksDir, f.ID+".json")}

		switch m, err := record.DecodeMark(f.Data); {
		case f.BadName:
			me.Bad = &record.UnusableError{Reason: reasonBadMarkName}
		case err != nil:
			me.Bad = asUnusable(err)
		case m.ID != f.ID:
			me.Bad = &record.UnusableError{Reason: reasonNameVsBody, Version: m.Version}
		default:
			me.Mark = m
			me.Live = e.Checker.Check(m.Envelope)
		}

		out = append(out, me)
	}

	return out, nil
}

// markFor builds the mark for a broken lease. It guards the lease's path keys, and nothing else: a
// resource has no checkout for a ref update to collide in.
func (c *call) markFor(t *record.Tombstone, held []keys.Key) *record.Mark {
	return &record.Mark{
		Envelope: record.Envelope{
			Version:       record.MarkVersion,
			ID:            c.e.NewID(),
			Footprint:     record.NoneFootprint(),
			ClearWhenDead: true,
			Liveness:      t.Liveness,
			Machine:       t.Machine,
			Holder:        t.Holder,
		},
		Lease:     t.Lease,
		Keys:      held,
		BrokenBy:  t.BrokenBy,
		Anonymous: t.Anonymous,
		BrokenAt:  t.BrokenAt,
		Cause:     t.Cause,
	}
}

// publishMark writes the fence mark of a break. A key that another holder's live record already
// covers is left out, and a mark with no key left is not written.
func (c *call) publishMark(t *record.Tombstone, held []keys.Key) error {
	var guarded []keys.Key

	for _, k := range held {
		if !c.coveredByOther(t.Envelope, k) {
			guarded = append(guarded, k)
		}
	}

	if len(guarded) == 0 {
		return nil
	}

	m := c.markFor(t, guarded)

	data, err := m.Encode()
	if err != nil {
		return fmt.Errorf("encoding the fence mark for %s: %w", t.Lease, err)
	}

	if err := c.l.Publish(state.MarksDir, m.ID, data); err != nil {
		return fmt.Errorf("publishing the fence mark for %s: %w", t.Lease, err)
	}

	return nil
}

// markDelivered writes the mark of a break that an older binary did without one. It runs when the
// tombstone is delivered. The tombstone lists paths only, so the keys take their canonical defaults.
//
// A tombstone marked Fenced was handled at break time. Its mark may be gone since, removed by a user
// or lifted, and delivery must not bring it back.
func (c *call) markDelivered(t *record.Tombstone, marks []*markEntry) error {
	if t.Kind != "" || t.Cause != "" || t.Fenced {
		return nil
	}

	for _, me := range marks {
		if me.Mark != nil && me.Mark.Lease == t.Lease {
			return nil
		}
	}

	var held []keys.Key

	for _, p := range t.Paths {
		if filepath.IsAbs(p) {
			held = append(held, keys.Key{Path: p})
		}
	}

	return c.publishMark(t, held)
}

// coveredByOther reports whether a live record of some other holder covers the key.
func (c *call) coveredByOther(holder record.Envelope, k keys.Key) bool {
	for _, en := range c.entries {
		if en.Rec == nil || en.removed || en.Live.Status == liveness.Dead || sameEnvelopeHolder(en.Rec.Envelope, holder) {
			continue
		}

		if held, ok := en.keysOf(); ok && keys.Covered(k, held, c.e.Finder) {
			return true
		}
	}

	return false
}

// sameEnvelopeHolder compares the holders two envelopes name, by the identity fields the holder
// match rule uses.
func sameEnvelopeHolder(a, b record.Envelope) bool {
	return liveness.SameMachine(a.Machine, b.Machine) && sameIdentity(a.Holder, b.Holder, b.Liveness)
}

// repairMarks reaps the marks whose holder is dead, and lets go of the keys that another holder's
// live record now covers. A mark with no key left goes. It runs under the lock with the other
// repairs. A mark of a newer minor, or one nitipan cannot use, is left alone.
func (c *call) repairMarks() error {
	marks, err := c.e.loadMarks()
	if err != nil {
		return err
	}

	for _, me := range marks {
		if me.Mark == nil || me.Mark.Rewritable() != nil {
			continue
		}

		if me.Live.Status == liveness.Dead && me.Mark.ClearWhenDead {
			if err := c.removeMark(me.ID); err != nil {
				return err
			}

			continue
		}

		var kept []keys.Key

		for _, k := range me.Mark.Keys {
			if !c.coveredByOther(me.Mark.Envelope, k) {
				kept = append(kept, k)
			}
		}

		if err := c.shrinkMark(me, kept); err != nil {
			return err
		}
	}

	return nil
}

// liftMarks lets go of the keys a record just written covers, for every mark but the record's own
// holder's. A holder's claim is not the consent that lifts a mark against itself. A grant is the
// owner's consent. The grant's parent holds a live record over the granted keys, so the cover rule
// lifts the mark without a rule of its own.
func (c *call) liftMarks(rec *record.Record) error {
	held, ok := pathKeysOf(rec)
	if !ok {
		return nil
	}

	marks, err := c.e.loadMarks()
	if err != nil {
		return err
	}

	for _, me := range marks {
		if me.Mark == nil || me.Mark.Rewritable() != nil {
			continue
		}

		if sameEnvelopeHolder(rec.Envelope, me.Mark.Envelope) {
			continue
		}

		var kept []keys.Key

		for _, k := range me.Mark.Keys {
			if !keys.Covered(k, held, c.e.Finder) {
				kept = append(kept, k)
			}
		}

		if err := c.shrinkMark(me, kept); err != nil {
			return err
		}
	}

	return nil
}

func pathKeysOf(rec *record.Record) ([]keys.Key, bool) {
	if !rec.BodyKnown || rec.Domain.Kind != record.KindPath {
		return nil, false
	}

	return rec.Domain.Keys, true
}

// shrinkMark rewrites a mark to the keys that are left, or removes it when none are.
func (c *call) shrinkMark(me *markEntry, kept []keys.Key) error {
	if len(kept) == len(me.Mark.Keys) {
		return nil
	}

	if len(kept) == 0 {
		return c.removeMark(me.ID)
	}

	next := *me.Mark
	next.Keys = kept

	data, err := next.Encode()
	if err != nil {
		return fmt.Errorf("encoding the fence mark %s: %w", me.ID, err)
	}

	if err := c.l.Replace(state.MarksDir, me.ID, data); err != nil {
		return fmt.Errorf("rewriting the fence mark %s: %w", me.Path, err)
	}

	return nil
}

func (c *call) removeMark(id string) error {
	err := c.l.Remove(state.MarksDir, id)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing the fence mark %s: %w", id, err)
	}

	return nil
}

// refuseMarked refuses a claim, a replace, or a request over keys that a fence mark guards for the
// caller. The refusal quotes how the lease ended and names no command, since an agent follows the
// command its error names.
func (c *call) refuseMarked(h adapter.Holder, claimed []keys.Key) error {
	marks, err := c.e.loadMarks()
	if err != nil {
		return err
	}

	for _, me := range marks {
		if me.Mark == nil || !c.e.isHolder(h, me.Mark.Envelope) {
			continue
		}

		for _, mk := range me.Mark.Keys {
			for _, k := range claimed {
				if !keys.Overlap(mk, k, c.e.Finder) {
					continue
				}

				return failf(exitcode.ErrBroken, "%s. A fence mark keeps %s from being claimed again, so this claim is "+
					"refused. Report this to the user and stop work on those paths.", DescribeBroken(me.Mark.AsTombstone()),
					mk.Path)
			}
		}
	}

	return nil
}
