// Package lease implements the commands that read and change lease records: acquire, release,
// advertise, break, and the read side of list.
//
// Every operation runs under the state lock. The command layer builds an Engine from injected
// dependencies, so tests drive the same code with a fake process table.
package lease

import (
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/space-armada/nitipan/internal/adapter"
	"github.com/space-armada/nitipan/internal/keys"
	"github.com/space-armada/nitipan/internal/liveness"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/session"
	"github.com/space-armada/nitipan/internal/state"
)

// Hooks let a test stop an operation between two writes, the way a crash would.
type Hooks struct {
	// AfterRewrite runs once `--replace` has rewritten its target and before it removes the leases it
	// merged. An error aborts the operation there, leaving the disk as a crash would.
	AfterRewrite func() error

	// AfterGrantMark lets a test crash a grant where no lease exists yet, to check the lapsed repair.
	AfterGrantMark func() error

	// AfterGrantPublish lets a test crash a grant that has a lease and no notice, to check the granted repair.
	AfterGrantPublish func() error

	// AfterGrantNotice lets a test crash a grant that has both, to check that the repair tells nobody twice.
	AfterGrantNotice func() error

	// AfterLossNotice lets a test crash a call that published a loss notice and has not yet made the
	// write it announces: a sub-lease ending, or a parent ending.
	AfterLossNotice func() error

	// AfterRevokeTombstone lets a test crash a revoke that published its tombstone and has not yet
	// removed the sub-lease.
	AfterRevokeTombstone func() error
}

// Engine runs lease operations against one state directory.
type Engine struct {
	Dir     *state.Dir
	Checker *liveness.Checker
	Canon   keys.Canonicalizer
	Finder  keys.BoundaryFinder
	Machine record.Machine

	// Now and NewID default to the clock and a fresh ULID.
	Now   func() time.Time
	NewID func() string

	// Sleep and Jitter drive the wait between attempts of AcquireWait. Jitter returns a number in
	// [0, 1). Tests replace them so a wait runs without real sleeping.
	Sleep  func(time.Duration)
	Jitter func() float64

	// HandleOf derives a holder's handle. Nil means the standard derivation. Tests force collisions
	// through it.
	HandleOf func(adapter.Holder) string

	// Notify receives the tombstones a call delivers and the failures that do not fail it.
	Notify Notify

	Hooks Hooks

	// GitHooks reports whether nitipan's git hooks are in force for a path. Nil means no path is
	// checked, which is how a test without git runs. A path claim in a checkout needs the hooks.
	GitHooks func(path string) (GitHooks, error)

	// Sessions reads and writes the files that Codex hooks and opencode plugins report. Nil means no
	// agent of liveness kind session can acquire.
	Sessions *session.Service
}

// NewEngine returns an Engine with the default clock and ID source.
func NewEngine(dir *state.Dir, checker *liveness.Checker, machine record.Machine, finder keys.BoundaryFinder) *Engine {
	return &Engine{
		Dir:     dir,
		Checker: checker,
		Canon:   keys.NewCanonicalizer(),
		Finder:  finder,
		Machine: machine,
		Now:     time.Now,
		NewID:   state.NewID,
		Sleep:   time.Sleep,
		Jitter:  rand.Float64,
	}
}

// entry is one file in leases/. Rec is nil when the file cannot be used, and Bad says why.
type entry struct {
	ID string

	// Path is the absolute path of the file, which is what an error names.
	Path string
	Rec  *record.Record
	Bad  *record.UnusableError
	Live liveness.Result

	// Hint is advice for a person that never changes the verdict.
	Hint string

	// removed is set once this call has removed the file. Later steps of the same call then treat the
	// lease as gone, so nothing counts it as a parent or a sub-lease, and no rewrite recreates it.
	removed bool
}

// Reasons this package adds to the ones the record package defines.
const (
	reasonBadName    record.Reason = "file name is not a lease ID"
	reasonNameVsBody record.Reason = "record id does not match its file name"
)

func (en *entry) file() string {
	return en.Path
}

// keysOf returns the canonical keys of a path record whose body this binary understands.
func (en *entry) keysOf() ([]keys.Key, bool) {
	if en.Rec == nil || !en.Rec.BodyKnown || en.Rec.Domain.Kind != record.KindPath {
		return nil, false
	}

	return en.Rec.Domain.Keys, true
}

// resource returns the name of a resource record whose body this binary understands.
func (en *entry) resource() (string, bool) {
	if en.Rec == nil || !en.Rec.BodyKnown || en.Rec.Domain.Kind != record.KindResource {
		return "", false
	}

	return en.Rec.Domain.Name, true
}

// domainList names what a lease holds: its paths, or its resource name.
func (en *entry) domainList() string {
	if name, ok := en.resource(); ok {
		return name
	}

	ks, ok := en.keysOf()
	if !ok {
		return "an unreadable domain"
	}

	out := make([]string, len(ks))
	for i, k := range ks {
		out[i] = k.Path
	}

	return strings.Join(out, ", ")
}

// load reads and classifies every file in leases/, in file-name order.
func (e *Engine) load() ([]*entry, error) {
	files, err := e.Dir.Scan(state.LeasesDir)
	if err != nil {
		return nil, fmt.Errorf("reading the leases: %w", err)
	}

	out := make([]*entry, 0, len(files))

	for _, f := range files {
		en := &entry{ID: f.ID, Path: filepath.Join(e.Dir.Path(), state.LeasesDir, f.ID+".json")}

		switch rec, err := record.Decode(f.Data); {
		case f.BadName:
			en.Bad = &record.UnusableError{Reason: reasonBadName}
		case err != nil:
			en.Bad = asUnusable(err)
		case rec.ID != f.ID:
			en.Bad = &record.UnusableError{Reason: reasonNameVsBody, Version: rec.Version}
		default:
			en.Rec = rec
			en.Live, en.Hint = e.Checker.CheckHint(rec.Envelope)
		}

		out = append(out, en)
	}

	return out, nil
}

func asUnusable(err error) *record.UnusableError {
	var bad *record.UnusableError
	if errors.As(err, &bad) {
		return bad
	}

	return &record.UnusableError{Reason: record.ReasonUndecodable, Detail: err}
}

// call is one locked operation. Callers close it when done.
type call struct {
	e       *Engine
	l       *state.Locked
	entries []*entry

	// mine holds the caller's records, in lease ID order. others holds every other usable record.
	// sameProcess marks the others that belong to another session of the caller's own process.
	mine        []*entry
	others      []*entry
	sameProcess map[*entry]bool

	// slots is the slot config as read under the lock. It is nil for a call that never reads it.
	slots *SlotConfig

	// delivered holds the tombstones this call reported to the caller and removed.
	delivered []*record.Tombstone

	// held says why the caller's leases stayed on their old host, when a move was needed and refused.
	held string

	// hs caches the host decision, since acquire asks for it more than once.
	hs *hostSync
}

func (c *call) close() {
	_ = c.l.Unlock()
}

// begin takes the lock and reads the leases. It refuses at once when the format marker forbids
// writes, because every caller changes something. With a holder, the records are split into the
// caller's and everyone else's, and the tombstones addressed to it are delivered.
func (e *Engine) begin(h *adapter.Holder) (*call, error) {
	return e.beginWith(h, e.Dir.Lock)
}

// beginWith is begin with the way the lock is taken left to the caller. A wait attempt passes a lock
// whose retry budget is cut to the time left.
func (e *Engine) beginWith(h *adapter.Holder, lock func() (*state.Locked, error)) (*call, error) {
	l, err := lock()
	if err != nil {
		return nil, err
	}

	c := &call{e: e, l: l, sameProcess: map[*entry]bool{}}

	if err := l.FormatErr(); err != nil {
		c.close()

		return nil, err
	}

	c.slots = LoadSlotConfig(e.Dir.Path())
	if warning := c.slots.Warning(); warning != "" {
		e.Notify.warn(errors.New(warning))
	}

	if err := c.load(); err != nil {
		c.close()

		return nil, err
	}

	if err := c.runRepairs(); err != nil {
		c.close()

		return nil, err
	}

	if h != nil {
		c.classify(*h)

		if err := c.deliver(*h); err != nil {
			c.close()

			return nil, err
		}

		c.deliverNotices(*h)
	}

	return c, nil
}

func (c *call) load() error {
	entries, err := c.e.load()
	if err != nil {
		return err
	}

	if c.l.FormatErr() == nil {
		entries, err = c.absorb(entries)
		if err != nil {
			return err
		}
	}

	c.entries = entries

	return nil
}

// absorb removes the leases that a record's absorbs field names. A crash after `--replace` rewrote
// its target leaves them behind, and any locked call finishes the merge. Nothing else is inferred:
// a lease on a nested worktree can become covered when the worktree is removed, and it must survive.
func (c *call) absorb(entries []*entry) ([]*entry, error) {
	absorbed := map[string]bool{}

	for _, en := range entries {
		if en.Rec == nil {
			continue
		}

		for _, id := range en.Rec.Absorbs {
			absorbed[id] = true
		}
	}

	if len(absorbed) == 0 {
		return entries, nil
	}

	kept := entries[:0:0]

	for _, en := range entries {
		if !absorbed[en.ID] {
			kept = append(kept, en)

			continue
		}

		err := c.removeLease(en)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("finishing a merge: %w", err)
		}
	}

	return kept, nil
}

func (c *call) classify(h adapter.Holder) {
	for _, en := range c.entries {
		if en.Rec == nil {
			continue
		}

		switch matchHolder(h, en.Rec.Holder, en.Rec.Liveness) {
		case matchSame:
			c.mine = append(c.mine, en)
		case matchSameProcess:
			c.others = append(c.others, en)
			c.sameProcess[en] = true
		default:
			c.others = append(c.others, en)
		}
	}
}

// firstUnusable returns the first file that cannot be used, as the refusal an acquire reports.
func (c *call) firstUnusable() error {
	for _, en := range c.entries {
		if en.Bad != nil {
			return unusableErr(en)
		}
	}

	return nil
}

func (c *call) mineByID(id string) *entry {
	for _, en := range c.mine {
		if en.ID == id && !en.removed {
			return en
		}
	}

	return nil
}

// entryByID finds any file by lease ID, usable or not.
func (c *call) entryByID(id string) *entry {
	for _, en := range c.entries {
		if en.ID == id && !en.removed {
			return en
		}
	}

	return nil
}

// leaseList is how an error names the caller's leases.
func (c *call) leaseList() string {
	if len(c.mine) == 0 {
		return "you hold no leases"
	}

	parts := make([]string, len(c.mine))
	for i, en := range c.mine {
		parts[i] = fmt.Sprintf("%s (%s)", en.ID, en.domainList())
	}

	return "your leases are " + strings.Join(parts, "; ")
}

// pending is a record waiting to be rewritten in place.
type pending struct {
	en  *entry
	rec *record.Record

	// forced means the caller asked for this change. A record this binary must not rewrite then
	// refuses the whole operation. Without it the rewrite is housekeeping and is skipped.
	forced bool
}

// writes collects the in-place rewrites of one operation, so they are checked before the first rename.
type writes struct {
	byEntry map[*entry]*pending
}

func newWrites() *writes {
	return &writes{byEntry: map[*entry]*pending{}}
}

// edit applies fn to a private copy of the record. fn reports whether it changed anything.
func (w *writes) edit(en *entry, forced bool, fn func(*record.Record) bool) {
	p := w.byEntry[en]
	if p == nil {
		cp := *en.Rec
		p = &pending{en: en, rec: &cp}
	}

	if !fn(p.rec) {
		return
	}

	p.forced = p.forced || forced
	w.byEntry[en] = p
}

// result returns the record as it will stand after the writes.
func (w *writes) result(en *entry) *record.Record {
	if p := w.byEntry[en]; p != nil {
		return p.rec
	}

	return en.Rec
}

// validate drops the housekeeping rewrites that this binary must not make, and refuses when a
// requested change would rewrite a record of a higher minor. Nothing is written yet.
func (w *writes) validate() error {
	for en, p := range w.byEntry {
		err := p.rec.Rewritable()
		if err == nil {
			continue
		}

		if p.forced {
			return fmt.Errorf("%s: %w; upgrade nitipan or ask the user", en.file(), err)
		}

		delete(w.byEntry, en)
	}

	return nil
}

// ordered returns the pending rewrites in lease ID order.
func (w *writes) ordered() []*pending {
	out := make([]*pending, 0, len(w.byEntry))
	for _, p := range w.byEntry {
		out = append(out, p)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].en.ID < out[j].en.ID })

	return out
}

func (c *call) writeOne(p *pending) error {
	data, err := p.rec.Encode()
	if err != nil {
		return fmt.Errorf("encoding %s: %w", p.en.file(), err)
	}

	if err := c.l.Replace(state.LeasesDir, p.en.ID, data); err != nil {
		return fmt.Errorf("rewriting %s: %w", p.en.file(), err)
	}

	return nil
}

// flush writes every pending rewrite, one rename per record, in lease ID order.
func (c *call) flush(w *writes) error {
	for _, p := range w.ordered() {
		if err := c.writeOne(p); err != nil {
			return err
		}
	}

	return nil
}

// holderFields are the advertisement fields that belong to the holder, not to one lease.
type holderFields struct {
	Name    *string
	Mode    *string
	Session string

	// Host is the host the caller runs under now. It matters only for liveness kind session.
	Host hostSync
}

// effective returns the name and mode every record of the caller should carry. A flag left out keeps
// the stored value. After a crash mid-rewrite the records can disagree, and rewrites go in lease ID
// order, so the first record holds the newest value.
func (c *call) effective(f holderFields) (name, mode string) {
	if len(c.mine) > 0 {
		name, mode = c.mine[0].Rec.Holder.Name, c.mine[0].Rec.PermissionMode
	}

	if f.Name != nil {
		name = *f.Name
	}

	if f.Mode != nil {
		mode = *f.Mode
	}

	return name, mode
}

// syncHolder brings the name, permission mode, and session ID of every record the caller holds in
// line with one value. A supplied name or mode is a request. The rest is housekeeping.
func (c *call) syncHolder(w *writes, f holderFields) {
	name, mode := c.effective(f)
	asked := f.Name != nil || f.Mode != nil

	for _, en := range c.mine {
		w.edit(en, asked, func(r *record.Record) bool {
			changed := r.Holder.Name != name || r.PermissionMode != mode
			r.Holder.Name, r.PermissionMode = name, mode

			return changed
		})

		w.edit(en, false, func(r *record.Record) bool {
			if f.Session == "" || r.Holder.SessionID == f.Session {
				return false
			}

			r.Holder.SessionID = f.Session

			return true
		})

		w.edit(en, false, func(r *record.Record) bool {
			if !f.Host.Move || r.Liveness != record.LivenessSession {
				return false
			}

			if r.Holder.PID == f.Host.PID && r.Holder.Start.Equal(f.Host.Start) {
				return false
			}

			r.Holder.PID, r.Holder.Start = f.Host.PID, f.Host.Start

			return true
		})
	}
}

func validMode(a adapter.Adapter, mode *string) error {
	if mode == nil || *mode == "" {
		return nil
	}

	for _, ok := range a.PermissionModes {
		if ok == *mode {
			return nil
		}
	}

	return usagef("permission mode %q is not one %s offers; accepted values: %s", *mode, a.Name,
		strings.Join(a.PermissionModes, ", "))
}
