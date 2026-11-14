package lease

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/space-armada/nitipan/internal/adapter"
	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/keys"
	"github.com/space-armada/nitipan/internal/liveness"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/state"
)

// RecallStatus is what a reader finds when it judges a recall.
type RecallStatus int

// Statuses of a recall.
const (
	// RecallInForce means the sub-lease still stands and its deadline lies ahead.
	RecallInForce RecallStatus = iota

	// RecallPastDeadline means the recall is in force and its deadline has passed. The sub-lease is over
	// for every reader, and the first locked call removes it.
	RecallPastDeadline

	// RecallNotInForce means the recall ends nothing. It is cancelling, its sub-lease is gone or no
	// longer names the parent, or its parent is gone. A locked call reaps it.
	RecallNotInForce

	// RecallUnusable means a record the judgement needs cannot be used. It ends nothing, and the reader
	// reports it. The recall is enforced once the record reads usable again.
	RecallUnusable
)

// RecallVerdict is the outcome of judging one recall. Why says what decided a status other than in
// force or past the deadline.
type RecallVerdict struct {
	Status RecallStatus
	Why    string
}

// RecallLease is a lease record as the judgement of a recall sees it. Exists is true when a file of
// that ID is there. Rec is nil when the file cannot be used.
type RecallLease struct {
	Exists bool
	Rec    *record.Record
	Live   liveness.Result
}

// JudgeRecall decides whether a recall ends its sub-lease. It reads plain data and takes no lock, so a
// locked call, an unlocked list, and a node all reach the same verdict.
//
// A recall is in force exactly while the sub-lease still names the recall's parent, the parent is not
// gone, the parent's record is usable, and the recall is not cancelling. A parent is gone when its
// file is missing or its holder is dead, the test that decides when a sub-lease stands alone. The
// deadline is inclusive, so a wait whose last attempt runs at the deadline sees the sub-lease ended.
func JudgeRecall(rc *record.Recall, child, parent RecallLease, now time.Time) RecallVerdict {
	switch {
	case rc.State == record.RecallCancelling:
		return RecallVerdict{RecallNotInForce, "the recall is being cancelled"}
	case !child.Exists:
		return RecallVerdict{RecallNotInForce, "the sub-lease is gone"}
	case child.Rec == nil:
		return RecallVerdict{RecallUnusable, "the sub-lease record cannot be used by this nitipan"}
	case child.Rec.Parent != rc.Parent:
		return RecallVerdict{RecallNotInForce, "the sub-lease no longer names the parent the recall was made from"}
	case !parent.Exists:
		return RecallVerdict{RecallNotInForce, "the parent lease is gone"}
	case parent.Rec == nil:
		return RecallVerdict{RecallUnusable, "the parent lease record cannot be used by this nitipan"}
	case parent.Live.Status == liveness.Dead:
		return RecallVerdict{RecallNotInForce, "the holder of the parent lease died"}
	case !now.Before(rc.Deadline):
		return RecallVerdict{Status: RecallPastDeadline}
	}

	return RecallVerdict{Status: RecallInForce}
}

// Pending reports whether the recall still binds its sub-lease: in force, or past the deadline and
// waiting for the call that removes the sub-lease.
func (v RecallVerdict) Pending() bool {
	return v.Status == RecallInForce || v.Status == RecallPastDeadline
}

// recallEntry is one file in recalls/. Rec is nil when the file cannot be used, and Bad says why.
type recallEntry struct {
	ID   string
	Path string
	Rec  *record.Recall
	Bad  *record.UnusableError
}

// loadRecalls reads and classifies every file in recalls/, in file-name order. It needs no lock.
func (e *Engine) loadRecalls() ([]*recallEntry, error) {
	files, err := e.Dir.Scan(state.RecallsDir)
	if err != nil {
		return nil, fmt.Errorf("reading the recalls: %w", err)
	}

	out := make([]*recallEntry, 0, len(files))

	for _, f := range files {
		out = append(out, e.classifyRecall(f))
	}

	return out, nil
}

func (e *Engine) classifyRecall(f state.File) *recallEntry {
	re := &recallEntry{ID: f.ID, Path: filepath.Join(e.Dir.Path(), state.RecallsDir, f.ID+".json")}

	switch rc, err := record.DecodeRecall(f.Data); {
	case f.BadName:
		re.Bad = &record.UnusableError{Reason: reasonBadRecallName}
	case err != nil:
		re.Bad = asUnusable(err)
	case rc.ID != f.ID:
		re.Bad = &record.UnusableError{Reason: reasonNameVsBody, Version: rc.Version}
	default:
		re.Rec = rc
	}

	return re
}

const reasonBadRecallName record.Reason = "file name is not a lease ID"

// recallByID reads the recall of one sub-lease. It is nil when there is none.
func (c *call) recallByID(id string) (*recallEntry, error) {
	if !state.ValidID(id) {
		return nil, nil
	}

	data, err := c.e.Dir.Read(state.RecallsDir, id)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	return c.e.classifyRecall(state.File{ID: id, Data: data}), nil
}

// leaseView shows an entry to the judgement of a recall.
func (c *call) leaseView(id string) RecallLease {
	en := c.entryByID(id)
	if en == nil {
		return RecallLease{}
	}

	return RecallLease{Exists: true, Rec: en.Rec, Live: en.Live}
}

// judgeRecall judges a recall against the leases as the call stands.
func (c *call) judgeRecall(rc *record.Recall) RecallVerdict {
	return JudgeRecall(rc, c.leaseView(rc.ID), c.leaseView(rc.Parent), c.e.Now())
}

// RecallResult describes a recall that was written. Ended is true when the sub-lessee was already
// dead, so the sub-lease was removed at once.
type RecallResult struct {
	Lease  *record.Record
	Parent *record.Record
	Recall *record.Recall
	Ended  bool
}

// Recall gives a sub-lease a deadline. Only the holder of its parent lease may do it. A second recall
// replaces the deadline and the notice.
//
// The order of the writes is the crash contract. The record is written as announcing first, then the
// notice that carries the deadline, then the record is marked announced. Any later locked call
// finishes a record it finds announcing, so a crash never leaves a recall unannounced or a notice
// that shows an old deadline.
func (e *Engine) Recall(h adapter.Holder, id string, within time.Duration) (*RecallResult, error) {
	if !state.ValidID(id) {
		return nil, usagef("%q is not a lease ID; run list to see the leases that exist", id)
	}

	if within <= 0 {
		return nil, usagef("the recall deadline must be a positive duration, since it is the time the sub-lessee has to " +
			"finish, commit, and release")
	}

	c, err := e.begin(&h)
	if err != nil {
		return nil, err
	}
	defer c.close()

	kid, parent, err := c.revocable(id, "recall it")
	if err != nil {
		return nil, err
	}

	res := &RecallResult{Lease: kid.Rec, Parent: parent.Rec}

	if _, err := c.rewritableRecall(id); err != nil {
		return nil, err
	}

	if kid.Live.Status == liveness.Dead {
		return c.recallDead(kid, res)
	}

	now := e.Now().UTC()

	rc := &record.Recall{
		Envelope: record.Envelope{
			Version:       record.RecallVersion,
			ID:            id,
			Footprint:     record.NoneFootprint(),
			ClearWhenDead: true,
			Liveness:      parent.Rec.Liveness,
			Machine:       parent.Rec.Machine,
			Holder:        parent.Rec.Holder,
		},
		Parent:     parent.ID,
		RecalledAt: now,
		Deadline:   now.Add(within),
		State:      record.RecallAnnouncing,
	}

	if err := c.writeRecall(rc); err != nil {
		return nil, err
	}

	if err := runHook(e.Hooks.AfterRecallRecord); err != nil {
		return nil, err
	}

	if err := c.announceRecall(kid, rc); err != nil {
		return nil, err
	}

	if err := runHook(e.Hooks.AfterRecallNotice); err != nil {
		return nil, err
	}

	if err := c.markAnnounced(rc); err != nil {
		return nil, err
	}

	res.Recall = rc

	return res, nil
}

// recallDead removes the sub-lease of a dead sub-lessee at once, as a revoke does. Nobody can receive a
// tombstone or a notice, so none is written.
func (c *call) recallDead(kid *entry, res *RecallResult) (*RecallResult, error) {
	c.dropSubLeaseEnded(kid)

	if err := c.removeLease(kid); err != nil {
		return nil, err
	}

	if err := c.dropRecall(kid.ID); err != nil {
		return nil, err
	}

	res.Ended = true

	return res, nil
}

// rewritableRecall reads the recall a sub-lease already has and refuses one this binary must not
// rewrite: a record it cannot use, or one of a higher minor. The refusal names the upgrade.
func (c *call) rewritableRecall(id string) (*recallEntry, error) {
	re, err := c.recallByID(id)
	if err != nil || re == nil {
		return nil, err
	}

	if re.Bad != nil {
		return nil, failf(record.ErrUnusable, "%s cannot be used by this nitipan (%s), so it cannot be replaced; upgrade nitipan "+
			"or ask the user", re.Path, re.Bad.Reason)
	}

	if err := re.Rec.Rewritable(); err != nil {
		return nil, failf(record.ErrUnusable, "%s: %v; upgrade nitipan or ask the user", re.Path, err)
	}

	return re, nil
}

func (c *call) writeRecall(rc *record.Recall) error {
	data, err := rc.Encode()
	if err != nil {
		return fmt.Errorf("encoding the recall of %s: %w", rc.ID, err)
	}

	if err := c.l.Replace(state.RecallsDir, rc.ID, data); err != nil {
		return fmt.Errorf("writing the recall of %s: %w", rc.ID, err)
	}

	return nil
}

func (c *call) markAnnounced(rc *record.Recall) error {
	next := *rc
	next.State = record.RecallAnnounced

	return c.writeRecall(&next)
}

// announceRecall publishes the notice that carries the recall's deadline, replacing any older one in
// place. A pending recall-cancelled notice goes first, so the sub-lessee never reads that the recall
// was cancelled after a newer recall.
func (c *call) announceRecall(kid *entry, rc *record.Recall) error {
	c.removeNotice(record.NoticeID(kid.ID, record.CauseRecallCancelled) + noticeSuffix)

	paths := keys.Paths(kid.Rec.Domain.Keys)

	n := c.e.noticeFor(kid.Rec.Envelope, kid.ID, record.CauseRecalled, fmt.Sprintf("your sub-lease %s on %s was recalled by "+
		"the holder of lease %s. Finish, commit, and release it before %s. If it is still held then, it ends and its keys go "+
		"back to that lease", kid.ID, strings.Join(paths, ", "), rc.Parent, rc.Deadline.UTC().Format(time.RFC3339)), paths)
	n.Version = record.RecallVersion

	return c.writeLossNotice(n)
}

// CancelResult describes a recall that was cancelled.
type CancelResult struct {
	Lease  *record.Record
	Parent *record.Record
}

// CancelRecall takes a recall back. Only the holder of the sub-lease's parent lease may do it.
//
// The record is rewritten as cancelling first. A cancelling recall is not in force, so no reader ends
// the sub-lease from then on. The recalled notice goes next, then the cancelled notice is written, and
// the record is removed last. Every locked call finishes a cancelling record it finds, so a crash
// never revives the recall and never leaves the sub-lessee unaware of the cancel.
func (e *Engine) CancelRecall(h adapter.Holder, id string) (*CancelResult, error) {
	if !state.ValidID(id) {
		return nil, usagef("%q is not a lease ID; run list to see the leases that exist", id)
	}

	c, err := e.begin(&h)
	if err != nil {
		return nil, err
	}
	defer c.close()

	kid, parent, err := c.revocable(id, "cancel its recall")
	if err != nil {
		return nil, err
	}

	re, err := c.rewritableRecall(id)
	if err != nil {
		return nil, err
	}

	if re == nil {
		return nil, failf(exitcode.ErrNoSuchLease, "lease %s has no recall to cancel", id)
	}

	if re.Rec.State != record.RecallCancelling {
		marked := *re.Rec
		marked.State = record.RecallCancelling

		if err := c.writeRecall(&marked); err != nil {
			return nil, err
		}

		re.Rec = &marked
	}

	if err := runHook(e.Hooks.AfterCancelMark); err != nil {
		return nil, err
	}

	if err := c.finishCancel(re.Rec, e.Hooks.AfterCancelDrop, e.Hooks.AfterCancelNotice); err != nil {
		return nil, err
	}

	return &CancelResult{Lease: kid.Rec, Parent: parent.Rec}, nil
}

// finishCancel does the steps after a cancel marked its record. The hooks are nil when a repair runs
// it. The cancelled notice is written only for a sub-lease that still stands and names the parent, since
// otherwise there is nobody it would be true for.
func (c *call) finishCancel(rc *record.Recall, afterDrop, afterNotice func() error) error {
	c.removeNotice(record.NoticeID(rc.ID, record.CauseRecalled) + noticeSuffix)

	if err := runHook(afterDrop); err != nil {
		return err
	}

	if kid := c.entryByID(rc.ID); kid != nil && kid.Rec != nil && kid.Rec.Parent == rc.Parent {
		paths := keys.Paths(kid.Rec.Domain.Keys)

		n := c.e.noticeFor(kid.Rec.Envelope, kid.ID, record.CauseRecallCancelled, fmt.Sprintf("the recall of your sub-lease %s "+
			"on %s was cancelled. You keep it, and the deadline no longer applies", kid.ID, strings.Join(paths, ", ")), paths)
		n.Version = record.RecallVersion

		if err := c.writeLossNotice(n); err != nil {
			return err
		}
	}

	if err := runHook(afterNotice); err != nil {
		return err
	}

	return c.removeRecallRecord(rc.ID)
}

// dropRecall removes a recall that ends nothing, after it drops the recall's notice. The notice goes
// first, so a crash between the two never leaves a notice for a recall that is gone.
func (c *call) dropRecall(id string) error {
	c.removeNotice(record.NoticeID(id, record.CauseRecalled) + noticeSuffix)

	return c.removeRecallRecord(id)
}

// removeRecallRecord removes a recall. A record of a higher minor is left for a newer binary, since an
// older one removes such a record only on its own holder's release.
func (c *call) removeRecallRecord(id string) error {
	re, err := c.recallByID(id)
	if err != nil {
		return err
	}

	if re != nil && re.Rec != nil && re.Rec.Rewritable() != nil {
		return nil
	}

	err = c.l.Remove(state.RecallsDir, id)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing the recall of %s: %w", id, err)
	}

	return nil
}

// repairRecalls is the repair every locked call runs for recalls.
//
// It finishes a record found announcing or cancelling, reaps a recall that is not in force, and ends
// each sub-lease whose recall is in force and past its deadline. A recall nitipan cannot use is
// reported and ignored, and so is one whose parent record cannot be used. Neither ends anything.
func (c *call) repairRecalls() error {
	recalls, err := c.e.loadRecalls()
	if err != nil {
		return err
	}

	for _, re := range recalls {
		if re.Bad != nil {
			c.e.Notify.warn(fmt.Errorf("the recall record %s cannot be used by this nitipan (%s), so it ends no sub-lease; "+
				"upgrade nitipan or ask the user", re.Path, re.Bad.Reason))

			continue
		}

		if err := c.repairRecall(re); err != nil {
			return err
		}
	}

	c.entries = slices.DeleteFunc(c.entries, func(en *entry) bool { return en.removed })

	return nil
}

func (c *call) repairRecall(re *recallEntry) error {
	rc := re.Rec

	if rc.State == record.RecallCancelling {
		if rc.Rewritable() != nil {
			return nil
		}

		return c.finishCancel(rc, nil, nil)
	}

	verdict := c.judgeRecall(rc)

	switch verdict.Status {
	case RecallNotInForce:
		return c.dropRecall(rc.ID)
	case RecallUnusable:
		c.e.Notify.warn(fmt.Errorf("the recall of sub-lease %s is not enforced: %s. It is enforced once that record reads "+
			"usable again", rc.ID, verdict.Why))

		return nil
	case RecallPastDeadline:
		return c.expireRecall(rc)
	}

	if rc.State != record.RecallAnnouncing || rc.Rewritable() != nil {
		return nil
	}

	if err := c.announceRecall(c.entryByID(rc.ID), rc); err != nil {
		return err
	}

	return c.markAnnounced(rc)
}

// expireRecall ends a sub-lease whose recall deadline passed. A sub-lease whose own holder is dead is
// left for the clearing that removes it.
//
// The sub-lessee's tombstone is published first, so a crash never loses the only signal it gets. The
// parent's notice comes next, then the sub-lease is removed, and the recall is removed last. A retry
// finds each step done. The tombstone names the parent's holder as the breaker, taken from the
// parent's record, since the call that runs the expiry may be a third party's.
func (c *call) expireRecall(rc *record.Recall) error {
	kid, parent := c.entryByID(rc.ID), c.entryByID(rc.Parent)

	if kid.Live.Status == liveness.Dead {
		return nil
	}

	if err := c.ensureExpiryTombstone(kid, parent); err != nil {
		return err
	}

	if err := runHook(c.e.Hooks.AfterExpiryTombstone); err != nil {
		return err
	}

	if err := c.returnToParent(kid, "the deadline of its recall passed"); err != nil {
		return err
	}

	if err := c.removeLease(kid); err != nil {
		return err
	}

	if err := runHook(c.e.Hooks.AfterExpiryRemoval); err != nil {
		return err
	}

	c.e.Notify.expired(Expiry{Lease: kid.Rec, Recall: rc})

	return c.dropRecall(rc.ID)
}

// ensureExpiryTombstone publishes the recalled tombstone for the sub-lessee unless an earlier revoke or
// expiry that crashed already did.
func (c *call) ensureExpiryTombstone(kid, parent *entry) error {
	found, err := c.endTombstone(kid)
	if err != nil || found != nil {
		return err
	}

	t := c.tombstoneBy(kid, parent.Rec.Holder, false)
	t.Version = record.RecallVersion
	t.Cause = record.CauseRecallExpired

	if err := c.publishTombstone(t); err != nil {
		return fmt.Errorf("ending %s: %w", kid.ID, err)
	}

	return nil
}

// recalledChildren says which of the sub-leases a refresh found are under a recall that still binds,
// and when each ends.
func (c *call) recalledChildren(kids []*entry) map[string]time.Time {
	out := map[string]time.Time{}

	for _, kid := range kids {
		re, err := c.recallByID(kid.ID)
		if err != nil {
			c.e.Notify.warn(err)

			continue
		}

		if re != nil && re.Rec != nil && c.judgeRecall(re.Rec).Pending() {
			out[kid.ID] = re.Rec.Deadline
		}
	}

	return out
}

// pendingRecalls finds the recalls a list shows on their sub-leases: those that still bind.
func (c *call) pendingRecalls() map[string]*record.Recall {
	recalls, err := c.e.loadRecalls()
	if err != nil {
		c.e.Notify.warn(err)

		return nil
	}

	out := map[string]*record.Recall{}

	for _, re := range recalls {
		if re.Rec != nil && c.judgeRecall(re.Rec).Pending() {
			out[re.ID] = re.Rec
		}
	}

	return out
}

// hideEndedRecalls is the read-only form of the expiry, for a list that holds no lock. It treats each
// sub-lease whose recall is in force and past its deadline as ended, so the parent reads as the owner.
// It writes nothing. The returned lines are warnings: the sub-leases it hid, and the recalls it could
// not use.
func (c *call) hideEndedRecalls(recalls []*recallEntry) []string {
	var out []string

	for _, re := range recalls {
		if re.Bad != nil {
			out = append(out, fmt.Sprintf("the recall record %s cannot be used by this nitipan (%s), so it ends no sub-lease",
				re.Path, re.Bad.Reason))

			continue
		}

		verdict := c.judgeRecall(re.Rec)

		switch verdict.Status {
		case RecallUnusable:
			out = append(out, fmt.Sprintf("the recall of sub-lease %s is not enforced: %s", re.ID, verdict.Why))
		case RecallPastDeadline:
			kid := c.entryByID(re.ID)
			if kid.Live.Status == liveness.Dead {
				continue
			}

			kid.removed = true

			out = append(out, fmt.Sprintf("sub-lease %s is past the deadline of its recall and counts as ended, so lease %s "+
				"owns its keys. The next locked nitipan call removes it", re.ID, re.Rec.Parent))
		}
	}

	c.entries = slices.DeleteFunc(c.entries, func(en *entry) bool { return en.removed })

	return out
}

// recalledVerdict judges a recalled notice. It is due while its recall binds, and it says nothing true
// once the recall is gone or not in force. A recall that cannot be judged keeps the notice.
func (c *call) recalledVerdict(n *record.Notice) noticeVerdict {
	re, err := c.recallByID(n.Target)

	switch {
	case err != nil:
		return keepNotice
	case re == nil:
		return dropNotice
	case re.Rec == nil:
		return keepNotice
	}

	switch verdict := c.judgeRecall(re.Rec); {
	case verdict.Pending():
		return deliverNotice
	case verdict.Status == RecallUnusable:
		return keepNotice
	}

	return dropNotice
}

// cancelledVerdict judges a recall-cancelled notice. A recall in force makes it false, so it is
// dropped. A cancel not yet finished keeps it. A sub-lease that is gone has nobody to tell.
func (c *call) cancelledVerdict(n *record.Notice) noticeVerdict {
	if c.entryByID(n.Target) == nil {
		return dropNotice
	}

	re, err := c.recallByID(n.Target)

	switch {
	case err != nil:
		return keepNotice
	case re == nil || re.Rec == nil:
		return deliverNotice
	case re.Rec.State == record.RecallCancelling:
		return keepNotice
	case c.judgeRecall(re.Rec).Pending():
		return dropNotice
	}

	return deliverNotice
}

// ClaimFlags renders a domain as the flags of the acquire that claims its whole key set.
func ClaimFlags(d record.Domain) string {
	parts := []string{"--checkout " + shellQuote(d.Checkout)}

	for _, s := range d.Scopes {
		parts = append(parts, "--scope "+shellQuote(s))
	}

	return strings.Join(parts, " ")
}
