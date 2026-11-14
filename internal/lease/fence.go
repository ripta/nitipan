package lease

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

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

	// RefusedRecalled means the caller's sub-lease ended at the deadline of its recall, and the paths
	// are the parent's again.
	RefusedRecalled
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

	// ended are the sub-leases whose recall is past its deadline. The judge treats them as gone, so the
	// parent owns their keys.
	ended []*recallState

	// pending are the sub-leases whose recall is in force with the deadline still ahead.
	pending []*recallState

	// recallWarnings are the recalls the judge could not use.
	recallWarnings []string

	// noted are the pending recalls that a passing path of the caller fell under.
	noted map[string]*recallState
}

// recallState is a recall as the judge sees it, with the sub-lease and the parent it names.
type recallState struct {
	rc     *record.Recall
	kid    *entry
	parent *entry
}

// heldBySubLessee reports whether the caller holds the sub-lease and not its parent.
//
// A caller that holds both owns the keys either way, so a recall changes nothing for it.
func (j *Judge) heldBySubLessee(rs *recallState) bool {
	return j.c.e.isHolder(j.caller, rs.kid.Rec.Envelope) && !j.c.e.isHolder(j.caller, rs.parent.Rec.Envelope)
}

// NewJudge reads the records, tombstones, and fence marks that decide for an agent caller.
//
// The recalls are read first, then the leases, then the tombstones. An expiry publishes the tombstone,
// removes the sub-lease, and drops the recall last. In this order a sub-lease that the read still finds
// has its recall in the recalls, and one the expiry already removed has its tombstone.
func (e *Engine) NewJudge(h adapter.Holder) (*Judge, error) {
	recalls, err := e.loadRecalls()
	if err != nil {
		return nil, err
	}

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

	j := &Judge{c: &call{e: e, entries: entries, sameProcess: map[*entry]bool{}}, caller: h, tombs: tombs, marks: marks,
		noted: map[string]*recallState{}}

	j.readRecalls(recalls)

	return j, nil
}

// readRecalls judges every recall with the rule the locked calls use, and writes nothing.
//
// A sub-lease past its recall's deadline is marked removed in the judge's own copy, so every later
// lookup reads the parent as the owner.
//
// A recall that cannot be used, or whose parent cannot be used, ends no sub-lease.
func (j *Judge) readRecalls(recalls []*recallEntry) {
	var ended []*recallState

	for _, re := range recalls {
		if re.Bad != nil {
			j.recallWarnings = append(j.recallWarnings, fmt.Sprintf("the recall record %s cannot be used by this nitipan (%s), "+
				"so it ends no sub-lease. Upgrade nitipan or ask the user", re.Path, re.Bad.Reason))

			continue
		}

		verdict := j.c.judgeRecall(re.Rec)

		switch verdict.Status {
		case RecallUnusable:
			j.recallWarnings = append(j.recallWarnings, fmt.Sprintf("the recall of sub-lease %s is not enforced: %s. Upgrade nitipan "+
				"or ask the user", re.ID, verdict.Why))
		case RecallInForce:
			j.pending = append(j.pending, j.recallStateOf(re.Rec))
		case RecallPastDeadline:
			rs := j.recallStateOf(re.Rec)
			if rs.kid.Live.Status == liveness.Dead {
				continue
			}

			j.ended = append(j.ended, rs)
			ended = append(ended, rs)
		}
	}

	for _, rs := range ended {
		rs.kid.removed = true
	}
}

func (j *Judge) recallStateOf(rc *record.Recall) *recallState {
	return &recallState{rc: rc, kid: j.c.entryByID(rc.ID), parent: j.c.entryByID(rc.Parent)}
}

// Warnings reports what the judge had to ignore: a fence mark that nitipan cannot use and whose keys
// cannot be read either, so it guards nothing here, and a recall that ends no sub-lease because it or
// its parent cannot be used.
func (j *Judge) Warnings() []string {
	out := slices.Clone(j.recallWarnings)

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

	// recall and parent are set for a recalled cause.
	recall *record.Recall
	parent *entry
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
		c := j.judge(k)
		if c == nil {
			j.noteRecall(k)

			continue
		}

		add(c, k)
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

	if c := j.recalledCause(k); c != nil {
		return c
	}

	return j.recordCause(k)
}

// recalledCause refuses a path inside a sub-lease of the caller that ended at its recall's deadline.
// The sub-lease is gone from the judge's records, so nothing else would name the caller.
func (j *Judge) recalledCause(k keys.Key) *cause {
	fp := record.PathFootprint([]keys.Key{k})

	for _, rs := range j.ended {
		if j.heldBySubLessee(rs) && rs.kid.Rec.Footprint.Overlaps(fp, j.c.e.Finder) {
			return &cause{kind: RefusedRecalled, id: rs.kid.ID, en: rs.kid, recall: rs.rc, parent: rs.parent}
		}
	}

	return nil
}

// noteRecall remembers the recall a passing path falls under, when the caller is that sub-lease's
// holder.
func (j *Judge) noteRecall(k keys.Key) {
	fp := record.PathFootprint([]keys.Key{k})

	for _, rs := range j.pending {
		if j.heldBySubLessee(rs) && rs.kid.Rec.Footprint.Overlaps(fp, j.c.e.Finder) {
			j.noted[rs.kid.ID] = rs
		}
	}
}

// RecallNotices says, for each recall in force that a passing path of the caller fell under, what the
// recall is and when it ends. The hook prints these. It only signals, so nothing is written or
// consumed.
func (j *Judge) RecallNotices() []string {
	ids := make([]string, 0, len(j.noted))
	for id := range j.noted {
		ids = append(ids, id)
	}

	sort.Strings(ids)

	out := make([]string, len(ids))

	for i, id := range ids {
		rs := j.noted[id]

		out[i] = fmt.Sprintf("your sub-lease %s on %s was recalled by the holder of lease %s. Finish, commit, and release it "+
			"before %s. If it is still held then, it ends and its keys go back to that lease", id, rs.kid.domainList(),
			rs.rc.Parent, rs.rc.Deadline.UTC().Format(time.RFC3339))
	}

	return out
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
	case RefusedRecalled:
		return fmt.Sprintf("%s: your sub-lease %s was recalled by %s, and the deadline %s passed. Your sub-lease no longer "+
			"exists, and these paths are back with lease %s. Report this to the user and stop work on these paths.", where,
			c.id, describeHolder(c.parent.Rec.Holder), c.recall.Deadline.UTC().Format(time.RFC3339), c.recall.Parent)
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
