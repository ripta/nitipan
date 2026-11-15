package lease

import (
	"time"

	"github.com/space-armada/nitipan/internal/adapter"
	"github.com/space-armada/nitipan/internal/liveness"
	"github.com/space-armada/nitipan/internal/mailbox"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/state"
)

// RingKind says what a ring points at.
type RingKind string

// Kinds of ring.
const (
	RingNotice  RingKind = "notice"
	RingRequest RingKind = "request"
)

// Prefix starts the ledger key of every ring of the kind.
func (k RingKind) Prefix() string {
	if k == RingRequest {
		return mailbox.RequestKeyPrefix
	}

	return mailbox.NoticeKeyPrefix
}

// Ring is one thing a wake path tells a holder about: a notice that delivery would deliver now, or a
// pending request on a lease the holder holds. It carries no content, because a ring only points the
// holder at `nitipan list`, and only a read consumes.
type Ring struct {
	Kind RingKind
	ID   string

	// At is when the notice or the request was written. A notice replaced in place gets a new At, so it
	// is a new ring.
	At time.Time
}

// Key is the ring's key in the ring ledger. The write time is part of it, so a replaced notice rings
// again and an ignored one does not.
func (r Ring) Key() string {
	return r.Kind.Prefix() + r.ID + "@" + r.At.UTC().Format(time.RFC3339Nano)
}

// RingKeys lists the ledger keys of the rings, in order.
func RingKeys(rings []Ring) []string {
	out := make([]string, len(rings))
	for i, r := range rings {
		out[i] = r.Key()
	}

	return out
}

// CountRings returns how many of the rings are notices and how many are requests.
func CountRings(rings []Ring) (notices, requests int) {
	for _, r := range rings {
		if r.Kind == RingRequest {
			requests++
		} else {
			notices++
		}
	}

	return notices, requests
}

// PendingRings lists what a wake path rings for a holder: the notices addressed to it that a locked
// call would deliver now, and the pending requests on leases it holds.
//
// It takes no lock and writes nothing. The recalls are read before the leases, in the order the git
// hook reads them, and a sub-lease past its recall deadline counts as ended as it does for an unlocked
// list. Delivery's own verdict judges each notice, so a notice a locked call would keep or drop never
// rings. A notice that waits on a repair rings once a locked call has run the repair.
//
// The notice that a coworker's `shared` claim was refused never rings. A refused claim costs the
// coworker nothing, so ringing it would let them wake the holder's agent with no mail grant.
func (e *Engine) PendingRings(h adapter.Holder) ([]Ring, error) {
	view, err := e.ReadRings()
	if err != nil || view == nil {
		return nil, err
	}

	return view.For(h)
}

// RingView is one unlocked read of the leases, ready to judge the rings of several holders. A node
// that rings many registrations in one tick reads the leases once and asks the view for each.
type RingView struct {
	c *call
}

// ReadRings reads what PendingRings judges against. It returns a nil view when neither notices/ nor
// requests/ holds a file, since nothing could ring.
func (e *Engine) ReadRings() (*RingView, error) {
	if !RingsPossible(e.Dir.Path()) {
		return nil, nil
	}

	recalls, err := e.loadRecalls()
	if err != nil {
		return nil, err
	}

	entries, err := e.load()
	if err != nil {
		return nil, err
	}

	c := &call{e: e, entries: entries, sameProcess: map[*entry]bool{}}

	// The warnings say how a list explains a hidden sub-lease. A ring has no use for them.
	_ = c.hideEndedRecalls(recalls)

	return &RingView{c: c}, nil
}

// For lists the rings of one holder.
func (v *RingView) For(h adapter.Holder) ([]Ring, error) {
	c := v.c

	c.mine, c.others = nil, nil
	clear(c.sameProcess)

	c.classify(h)

	rings := c.noticeRings(h)

	reqs, err := c.requestRings()
	if err != nil {
		return nil, err
	}

	return append(rings, reqs...), nil
}

// RingsPossible reports whether notices/ or requests/ in the state directory holds a file. It spares a
// ring path that runs on every tool call from opening the leases when nothing could ring.
func RingsPossible(root string) bool {
	for _, dir := range []string{state.NoticesDir, state.RequestsDir} {
		names, err := state.ListNames(root, dir)
		if err != nil || len(names) > 0 {
			return true
		}
	}

	return false
}

func (c *call) noticeRings(h adapter.Holder) []Ring {
	var out []Ring

	for _, d := range c.dueNotices(h) {
		if !d.Report || d.Notice.Cause == record.CauseSharedClaimRefused {
			continue
		}

		out = append(out, Ring{Kind: RingNotice, ID: d.Notice.ID, At: d.Notice.At})
	}

	return out
}

// requestRings finds the pending requests whose target lease the caller holds. A request whose
// requester is dead is left out, since a locked call removes it.
func (c *call) requestRings() ([]Ring, error) {
	reqs, err := c.e.loadRequests()
	if err != nil {
		return nil, err
	}

	var out []Ring

	for _, re := range reqs {
		if re.Req == nil || re.Req.State != "" {
			continue
		}

		if re.Live.Status == liveness.Dead && re.Req.ClearWhenDead {
			continue
		}

		if c.mineByID(re.Req.Parent) == nil {
			continue
		}

		out = append(out, Ring{Kind: RingRequest, ID: re.ID, At: re.Req.RequestedAt})
	}

	return out, nil
}
