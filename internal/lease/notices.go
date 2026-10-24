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

// deliverNotices reports every notice addressed to the caller, then removes it. It runs under the
// lock after the call's repairs, so a notice never describes a state that a repair is about to change.
// A notice that cannot be used is left alone, and a failed removal is reported without failing the call.
func (c *call) deliverNotices(h adapter.Holder) {
	names, err := state.ListNames(c.e.Dir.Path(), state.NoticesDir)
	if err != nil {
		c.e.Notify.warn(err)

		return
	}

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

		c.e.Notify.notice(n)

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
			req.ID, strings.Join(keyPaths(req.Domain.Keys), ", ")), keyPaths(req.Domain.Keys))
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

	paths := keyPaths(req.Domain.Keys)

	return c.e.noticeFor(req.Envelope, req.ID, record.CauseRequestGranted, fmt.Sprintf("your request %s was granted%s. "+
		"Lease %s now holds %s", req.ID, by, req.ID, strings.Join(paths, ", ")), paths)
}

// declineRequest leaves the requester a declined notice, then removes the request. The notice goes
// first, so a crash between the two repeats it harmlessly instead of losing it.
func (c *call) declineRequest(req *record.Request, why string) error {
	paths := keyPaths(req.Domain.Keys)

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

func keyPaths(ks []keys.Key) []string {
	out := make([]string, len(ks))
	for i, k := range ks {
		out[i] = k.Path
	}

	return out
}
