package lease

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/space-armada/nitipan/internal/adapter"
	"github.com/space-armada/nitipan/internal/keys"
	"github.com/space-armada/nitipan/internal/liveness"
	"github.com/space-armada/nitipan/internal/record"
)

// RefusalKind says why a ref update was refused. Each kind has its own message and its own next
// step.
type RefusalKind int

// Kinds of refusal.
const (
	// RefusedHeld means another holder's live record owns the paths.
	RefusedHeld RefusalKind = iota

	// RefusedUnknown means a record owns the paths whose holder's liveness is unknown.
	RefusedUnknown

	// RefusedUnusable means a record nitipan cannot use may cover the paths.
	RefusedUnusable

	// RefusedTombstone means an undelivered tombstone addressed to the caller lists the paths.
	RefusedTombstone

	// RefusedMark means a fence mark addressed to the caller guards the paths.
	RefusedMark
)

// Refusal is one cause for refusing a ref update, with the paths it covers.
type Refusal struct {
	Kind RefusalKind

	// Paths are the touched paths this cause refuses.
	Paths []string

	// Message is the account for the caller. It names what the caller should do next, and never
	// names a command that would take the paths from their owner.
	Message string
}

// Judge answers, for a caller, whether it may change a path. It reads the state directory without
// the lock, delivers nothing, and removes nothing, so a git hook can run it between any two writes.
type Judge struct {
	c      *call
	caller adapter.Holder
	tombs  []*tombEntry
	marks  []*markEntry
}

// NewJudge reads the records, tombstones, and fence marks that decide for an agent caller.
func (e *Engine) NewJudge(h adapter.Holder) (*Judge, error) {
	entries, err := e.load()
	if err != nil {
		return nil, err
	}

	tombs, err := e.loadTombstones()
	if err != nil {
		return nil, err
	}

	marks, err := e.loadMarks()
	if err != nil {
		return nil, err
	}

	return &Judge{c: &call{e: e, entries: entries, sameProcess: map[*entry]bool{}}, caller: h, tombs: tombs, marks: marks}, nil
}

// Warnings reports what the judge had to ignore: a fence mark that nitipan cannot use and whose keys
// cannot be read either, so it guards nothing here.
func (j *Judge) Warnings() []string {
	var out []string

	for _, me := range j.marks {
		if me.Mark == nil && me.rawKeys() == nil {
			out = append(out, fmt.Sprintf("the fence mark %s cannot be used by this nitipan (%s) and names no keys it can "+
				"read, so it guards nothing here. Upgrade nitipan or ask the user", me.Path, me.Bad.Reason))
		}
	}

	return out
}

// cause groups the paths one record, tombstone, or mark refuses.
type cause struct {
	kind  RefusalKind
	id    string
	paths []string
	en    *entry
	tomb  *record.Tombstone
	mark  *record.Mark
	why   string
}

// Refusals judges each path on its own and returns one refusal per cause. checkout is the worktree
// the paths were mapped against.
//
// A path passes when no live record covers it and no tombstone or mark names it for the caller.
// Ownership follows the owner rule of sub-leases: a parent does not own a path inside one of its
// live sub-leases, and the sub-lessee does. A dead holder's record does not refuse, because the
// hook cannot clear it.
func (j *Judge) Refusals(checkout keys.Key, touched []keys.Key) []Refusal {
	var (
		causes []*cause
		byID   = map[string]*cause{}
	)

	add := func(c *cause, k keys.Key) {
		key := fmt.Sprintf("%d/%s", c.kind, c.id)

		if have, ok := byID[key]; ok {
			have.paths = append(have.paths, k.Path)

			return
		}

		c.paths = []string{k.Path}
		byID[key] = c
		causes = append(causes, c)
	}

	for _, k := range touched {
		if c := j.judge(k); c != nil {
			add(c, k)
		}
	}

	out := make([]Refusal, len(causes))

	for i, c := range causes {
		out[i] = Refusal{Kind: c.kind, Paths: c.paths, Message: j.message(checkout, c)}
	}

	return out
}

// judge returns the cause that refuses one path, or nil when the path passes.
func (j *Judge) judge(k keys.Key) *cause {
	if c := j.markCause(k); c != nil {
		return c
	}

	if c := j.tombstoneCause(k); c != nil {
		return c
	}

	return j.recordCause(k)
}

func (j *Judge) markCause(k keys.Key) *cause {
	for _, me := range j.marks {
		switch {
		case me.Mark == nil:
			if guards(me.rawKeys(), k, j.c.e.Finder) {
				return &cause{kind: RefusedUnusable, id: me.ID, why: fmt.Sprintf("the fence mark %s cannot be used by this "+
					"nitipan (%s), and it names these paths", me.Path, me.Bad.Reason)}
			}
		case j.c.e.isHolder(j.caller, me.Mark.Envelope) && guards(me.Mark.Keys, k, j.c.e.Finder):
			return &cause{kind: RefusedMark, id: me.ID, mark: me.Mark}
		}
	}

	return nil
}

func (j *Judge) tombstoneCause(k keys.Key) *cause {
	for _, te := range j.tombs {
		if te.Tomb == nil || te.Tomb.Kind != "" || !j.c.e.addressedTo(j.caller, te.Tomb) {
			continue
		}

		paths := make([]keys.Key, len(te.Tomb.Paths))
		for i, p := range te.Tomb.Paths {
			paths[i] = keys.Key{Path: p, FoldCase: k.FoldCase, Normalize: k.Normalize}
		}

		if guards(paths, k, j.c.e.Finder) {
			return &cause{kind: RefusedTombstone, id: te.ID, tomb: te.Tomb}
		}
	}

	return nil
}

// guards reports whether one of the keys contains the path.
func guards(held []keys.Key, k keys.Key, finder keys.BoundaryFinder) bool {
	return keys.Covered(k, held, finder)
}

// rawKeys reads the keys of a mark that cannot be used as a whole, when its keys still decode.
func (me *markEntry) rawKeys() []keys.Key {
	return rawMarkKeys(me.Bad)
}

// recordCause finds who owns a path among the records that cover it. Another holder's live record
// wins over a record nitipan cannot use, since it names someone to coordinate with. A path that the
// caller owns still fails when a record nitipan cannot use may cover it.
func (j *Judge) recordCause(k keys.Key) *cause {
	var (
		query    = []keys.Key{k}
		fp       = record.PathFootprint(query)
		other    *cause
		unusable *cause
	)

	for _, en := range j.c.entries {
		if en.Rec == nil {
			if unusable == nil && footprintOf(en.Bad).Overlaps(fp, j.c.e.Finder) {
				unusable = &cause{kind: RefusedUnusable, id: en.ID, en: en}
			}

			continue
		}

		if en.removed || en.Live.Status == liveness.Dead || !en.Rec.Footprint.Overlaps(fp, j.c.e.Finder) {
			continue
		}

		if owner, _ := j.c.ownerOf(en, query); !owner || j.c.e.isHolder(j.caller, en.Rec.Envelope) {
			continue
		}

		if other == nil {
			kind := RefusedHeld
			if en.Live.Status != liveness.Alive {
				kind = RefusedUnknown
			}

			other = &cause{kind: kind, id: en.ID, en: en}
		}
	}

	if other != nil {
		return other
	}

	return unusable
}

// footprintOf reads the footprint of a record nitipan cannot use. Whatever cannot be read counts
// as "all", so such a record covers every path.
func footprintOf(bad *record.UnusableError) record.Footprint {
	var fp record.Footprint

	if bad != nil {
		if raw, ok := bad.Raw["footprint"]; ok {
			_ = json.Unmarshal(raw, &fp)
		}
	}

	return fp
}

// rawMarkKeys reads the keys of a mark that cannot be used as a whole, when they still decode.
func rawMarkKeys(bad *record.UnusableError) []keys.Key {
	if bad == nil {
		return nil
	}

	var ks []keys.Key

	if raw, ok := bad.Raw["keys"]; !ok || json.Unmarshal(raw, &ks) != nil {
		return nil
	}

	return ks
}

func (j *Judge) message(checkout keys.Key, c *cause) string {
	where := pathList(c.paths)

	switch c.kind {
	case RefusedHeld:
		msg := fmt.Sprintf("%s: lease %s on %s is held by %s. These paths are theirs while they hold it.", where, c.en.ID,
			c.en.domainList(), describeHolder(c.en.Rec.Holder))

		if c.en.Rec.Description != "" {
			msg += fmt.Sprintf(" It says: %s.", c.en.Rec.Description)
		}

		msg += " Coordinate with the holder or ask the user."

		if hint := j.requestHint(checkout, c); hint != "" {
			msg += "\n" + hint
		}

		return msg
	case RefusedUnknown:
		return fmt.Sprintf("%s: lease %s on %s is held by %s, and nitipan cannot tell whether that holder is alive (%s). "+
			"That counts as alive. Ask the user.", where, c.en.ID, c.en.domainList(), describeHolder(c.en.Rec.Holder),
			c.en.Live.Reason)
	case RefusedUnusable:
		if c.en != nil {
			return fmt.Sprintf("%s: %s cannot be used by this nitipan (%s), so nitipan cannot tell who owns these paths. "+
				"Upgrade nitipan or ask the user.", where, c.en.file(), c.en.Bad.Reason)
		}

		return fmt.Sprintf("%s: %s. Upgrade nitipan or ask the user.", where, c.why)
	case RefusedTombstone:
		return fmt.Sprintf("%s: %s. Your lease no longer exists. Report this to the user and stop work on these paths.",
			where, DescribeBroken(c.tomb))
	default:
		return fmt.Sprintf("%s: %s. A fence mark still guards these paths against you. Report this to the user and stop "+
			"work on them.", where, DescribeBroken(c.mark.AsTombstone()))
	}
}

// requestHint names `request` for the refused paths of one blocking lease, the way a refused
// acquire does.
func (j *Judge) requestHint(checkout keys.Key, c *cause) string {
	scoped := make([]keys.Key, len(c.paths))
	for i, p := range c.paths {
		scoped[i] = keys.Key{Path: p, FoldCase: checkout.FoldCase, Normalize: checkout.Normalize}
	}

	return j.c.requestHint(&keys.Domain{Checkout: checkout, Keys: scoped}, []*entry{c.en})
}

const pathsShown = 3

func pathList(paths []string) string {
	sort.Strings(paths)

	if len(paths) <= pathsShown {
		return strings.Join(paths, ", ")
	}

	return fmt.Sprintf("%s, and %d more", strings.Join(paths[:pathsShown], ", "), len(paths)-pathsShown)
}
