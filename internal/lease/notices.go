package lease

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/space-armada/nitipan/internal/adapter"
	"github.com/space-armada/nitipan/internal/keys"
	"github.com/space-armada/nitipan/internal/liveness"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/state"
)

const noticeSuffix = ".json"

// noticeFor builds a notice for the holder an envelope names. The notice's ID is derived from its
// target and cause, so writing it twice publishes it once.
func (e *Engine) noticeFor(to record.Envelope, target, cause, detail string, paths []string) *record.Notice {
	return &record.Notice{
		Envelope: record.Envelope{
			Version:       record.ParentVersion,
			ID:            record.NoticeID(target, cause),
			Footprint:     record.NoneFootprint(),
			ClearWhenDead: true,
			Liveness:      to.Liveness,
			Machine:       to.Machine,
			Holder:        to.Holder,
		},
		Cause:  cause,
		Target: target,
		Detail: detail,
		Paths:  paths,
		At:     e.Now().UTC(),
	}
}

// writeNotice publishes a notice. A notice with the same ID that is already there counts as written,
// which is how a retry after a crash tells the holder once.
func (c *call) writeNotice(n *record.Notice) error {
	data, err := n.Encode()
	if err != nil {
		return fmt.Errorf("encoding the notice %s: %w", n.ID, err)
	}

	err = c.l.PublishNamed(state.NoticesDir, n.ID+noticeSuffix, data)
	if err != nil && !errors.Is(err, state.ErrExists) {
		return fmt.Errorf("publishing the notice %s: %w", n.ID, err)
	}

	return nil
}

// writeLossNotice publishes a notice that announces a write the caller is about to make. An earlier
// attempt that crashed may have left the same notice behind, still undelivered, with an account of
// how the loss came about that this attempt may know better. The new notice replaces it, so the
// addressee reads the account of the loss that happened.
func (c *call) writeLossNotice(n *record.Notice) error {
	data, err := n.Encode()
	if err != nil {
		return fmt.Errorf("encoding the notice %s: %w", n.ID, err)
	}

	if err := c.l.ReplaceNamed(state.NoticesDir, n.ID+noticeSuffix, data); err != nil {
		return fmt.Errorf("publishing the notice %s: %w", n.ID, err)
	}

	return nil
}

// noticeVerdict says what delivery does with a notice.
type noticeVerdict int

const (
	deliverNotice noticeVerdict = iota

	// keepNotice leaves a loss notice in place until the loss it announces has happened. The actor
	// published it first and may have crashed, and a retry then finds it and does not tell twice.
	keepNotice

	// dropNotice removes a loss notice that no longer says anything true.
	dropNotice
)

// lossVerdict judges a loss notice against the leases as they stand after this call's repairs.
//
// A "parent ended" notice is due once the sub-lease stands alone. While the sub-lease still names a
// live parent, the loss has not happened. If the sub-lease is gone, its sub-lessee already knows more
// than the notice would tell it.
//
// A "sub-lease ended" notice is due once the sub-lease is gone, and only while the addressee still
// holds a lease that covers its keys, since the notice says the keys are the addressee's again. A
// parent that was released since has no keys to get back.
//
// A recalled notice is due while its recall binds the sub-lease, and a recall-cancelled notice is due
// unless a recall is in force again.
func (c *call) lossVerdict(n *record.Notice) noticeVerdict {
	kid := c.entryByID(n.Target)

	switch n.Cause {
	case record.CauseParentEnded:
		switch {
		case kid == nil:
			return dropNotice
		case kid.Rec != nil && kid.Rec.Parent == "":
			return deliverNotice
		}

		return keepNotice
	case record.CauseSubLeaseEnded:
		switch {
		case kid != nil:
			return keepNotice
		case c.holdsCoverOf(n.Paths):
			return deliverNotice
		}

		return dropNotice
	case record.CauseRecalled:
		return c.recalledVerdict(n)
	case record.CauseRecallCancelled:
		return c.cancelledVerdict(n)
	}

	return deliverNotice
}

// holdsCoverOf reports whether the caller holds a lease that covers every one of the paths.
func (c *call) holdsCoverOf(paths []string) bool {
	var held []keys.Key

	for _, en := range c.mine {
		if ks, ok := en.keysOf(); ok && !en.removed {
			held = append(held, ks...)
		}
	}

	for _, p := range paths {
		if !keys.Covered(keys.Key{Path: p}, held, c.e.Finder) {
			return false
		}
	}

	return true
}

// dueNotice is a notice addressed to the caller that a call is done with. Report says the caller is
// told. A notice that is not reported is dropped, since it no longer says anything true.
type dueNotice struct {
	Name   string
	Notice *record.Notice
	Report bool
}

// dueNotices finds the notices addressed to the caller that this call delivers or drops, after the
// call's repairs. A notice that cannot be used is left alone, and so is a loss notice that waits for
// its loss.
func (c *call) dueNotices(h adapter.Holder) []dueNotice {
	names, err := state.ListNames(c.e.Dir.Path(), state.NoticesDir)
	if err != nil {
		c.e.Notify.warn(err)

		return nil
	}

	var due []dueNotice

	for _, name := range names {
		data, err := state.ReadName(c.e.Dir.Path(), state.NoticesDir, name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}

		if err != nil {
			c.e.Notify.warn(err)

			continue
		}

		n, err := record.DecodeNotice(data)
		if err != nil || name != n.ID+noticeSuffix || !c.e.isHolder(h, n.Envelope) {
			continue
		}

		switch c.lossVerdict(n) {
		case keepNotice:
			continue
		case deliverNotice:
			due = append(due, dueNotice{Name: name, Notice: n, Report: true})
		case dropNotice:
			due = append(due, dueNotice{Name: name, Notice: n})
		}
	}

	return due
}

// deliverNotices reports every notice addressed to the caller, then removes it.
//
// It runs under the lock after the call's repairs, so a notice never describes a state that a repair
// is about to change.
//
// A failed removal is reported without failing the call.
func (c *call) deliverNotices(h adapter.Holder) {
	for _, d := range c.dueNotices(h) {
		if d.Report {
			c.e.Notify.notice(d.Notice)
		}

		c.removeNotice(d.Name)
	}
}

func (c *call) removeNotice(name string) {
	if err := c.l.RemoveNamed(state.NoticesDir, name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		c.e.Notify.warn(fmt.Errorf("removing the notice %s: %w", filepath.Join(c.e.Dir.Path(), state.NoticesDir, name), err))
	}
}

// reapNotices removes the notices whose holder is dead, since nobody can receive them. Files that
// cannot be used are left alone, as delivery leaves them.
func (c *call) reapNotices() {
	names, err := state.ListNames(c.e.Dir.Path(), state.NoticesDir)
	if err != nil {
		c.e.Notify.warn(err)

		return
	}

	for _, name := range names {
		data, err := state.ReadName(c.e.Dir.Path(), state.NoticesDir, name)
		if err != nil {
			continue
		}

		n, err := record.DecodeNotice(data)
		if err != nil || name != n.ID+noticeSuffix || !n.ClearWhenDead {
			continue
		}

		if c.e.Checker.Check(n.Envelope).Status != liveness.Dead {
			continue
		}

		if err := c.l.RemoveNamed(state.NoticesDir, name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			c.e.Notify.warn(fmt.Errorf("removing the notice %s: %w", filepath.Join(c.e.Dir.Path(), state.NoticesDir, name), err))
		}
	}
}

// requestEntry is one file in requests/. Req is nil when the file cannot be used.
type requestEntry struct {
	ID   string
	Path string
	Req  *record.Request
	Bad  *record.UnusableError
	Live liveness.Result
}

// loadRequests reads and classifies every file in requests/, in file-name order.
func (e *Engine) loadRequests() ([]*requestEntry, error) {
	files, err := e.Dir.Scan(state.RequestsDir)
	if err != nil {
		return nil, fmt.Errorf("reading the requests: %w", err)
	}

	out := make([]*requestEntry, 0, len(files))

	for _, f := range files {
		re := &requestEntry{ID: f.ID, Path: filepath.Join(e.Dir.Path(), state.RequestsDir, f.ID+".json")}

		switch req, err := record.DecodeRequest(f.Data); {
		case f.BadName:
			re.Bad = &record.UnusableError{Reason: reasonBadName}
		case err != nil:
			re.Bad = asUnusable(err)
		case req.ID != f.ID:
			re.Bad = &record.UnusableError{Reason: reasonNameVsBody, Version: req.Version}
		default:
			re.Req = req
			re.Live = e.Checker.Check(req.Envelope)
		}

		out = append(out, re)
	}

	return out, nil
}

// repairRequests finishes what a crashed or older binary left in requests/. It runs under the lock
// before anything else uses the requests.
//
// A request marked granting is handled first. If a lease carries its ID, the grant happened, so the
// granted notice is written when missing and the request goes. If none does, the grant was
// interrupted or the lease was removed before this repair. The request goes with a lapsed notice. A
// granting request never counts as declined, and it is never granted again, so a lease ID is not
// reused.
//
// A request whose requester is dead is removed. A request whose parent lease no longer exists is
// removed with a declined notice. Files that cannot be used are left alone.
func (c *call) repairRequests() error {
	reqs, err := c.e.loadRequests()
	if err != nil {
		return err
	}

	for _, re := range reqs {
		if re.Req != nil && re.Req.State == record.RequestGranting {
			if err := c.repairGranting(re.Req); err != nil {
				return err
			}
		}
	}

	for _, re := range reqs {
		if re.Req == nil || re.Req.State == record.RequestGranting {
			continue
		}

		switch {
		case re.Live.Status == liveness.Dead && re.Req.ClearWhenDead:
			err = c.removeRequest(re.Req.ID)
		case c.entryByID(re.Req.Parent) == nil:
			err = c.declineRequest(re.Req, "the lease it asked to share ended")
		}

		if err != nil {
			return err
		}
	}

	return nil
}

func (c *call) repairGranting(req *record.Request) error {
	var n *record.Notice

	if en := c.entryByID(req.ID); en != nil {
		n = c.grantedNotice(req, c.entryByID(req.Parent))
	} else {
		n = c.e.noticeFor(req.Envelope, req.ID, record.CauseRequestLapsed, fmt.Sprintf("your request %s for %s was "+
			"interrupted, or its lease was removed before you heard of it, so you hold nothing there. Request again if you "+
			"still need it",
			req.ID, strings.Join(keys.Paths(req.Domain.Keys), ", ")), keys.Paths(req.Domain.Keys))
	}

	if err := c.writeNotice(n); err != nil {
		return err
	}

	return c.removeRequest(req.ID)
}

// grantedNotice tells a requester that its request became a sub-lease. The parent entry may be gone
// by the time a repair writes it.
func (c *call) grantedNotice(req *record.Request, parent *entry) *record.Notice {
	by := ""
	if parent != nil && parent.Rec != nil {
		by = " by " + describeHolder(parent.Rec.Holder)
	}

	paths := keys.Paths(req.Domain.Keys)

	return c.e.noticeFor(req.Envelope, req.ID, record.CauseRequestGranted, fmt.Sprintf("your request %s was granted%s. "+
		"Lease %s now holds %s", req.ID, by, req.ID, strings.Join(paths, ", ")), paths)
}

// declineRequest leaves the requester a declined notice, then removes the request. The notice goes
// first, so a crash between the two repeats it harmlessly instead of losing it.
func (c *call) declineRequest(req *record.Request, why string) error {
	paths := keys.Paths(req.Domain.Keys)

	n := c.e.noticeFor(req.Envelope, req.ID, record.CauseRequestDeclined, fmt.Sprintf("your request %s for %s was "+
		"declined: %s", req.ID, strings.Join(paths, ", "), why), paths)

	if err := c.writeNotice(n); err != nil {
		return err
	}

	return c.removeRequest(req.ID)
}

func (c *call) removeRequest(id string) error {
	err := c.l.Remove(state.RequestsDir, id)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing the request %s: %w", id, err)
	}

	return nil
}
