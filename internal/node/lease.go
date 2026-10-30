package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/space-armada/nitipan/internal/handoff"
	"github.com/space-armada/nitipan/internal/keys"
	"github.com/space-armada/nitipan/internal/liveness"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/state"
	"github.com/space-armada/nitipan/internal/wire"
)

// ErrBrokerTooOld means the broker does not serve a call this node makes.
var ErrBrokerTooOld = errors.New("broker too old")

// DefaultWakeGap is how long the main loop may go between ticks before the node treats the gap as a
// sleep and renews at once.
const DefaultWakeGap = 5 * time.Second

// DefaultReplyGrace is how long a reply may wait unread before the node delivers its outcome as a
// notice. It outlasts the CLI's own wait.
const DefaultReplyGrace = 90 * time.Second

// held is the node's view of one broker lease. sentAt is the clock reading when the renewal the broker
// acked was sent, so the node's deadline is never later than the broker's.
type held struct {
	lease  wire.Lease
	sentAt time.Duration

	// writtenOff is set when the node's own deadline passed with no acked renewal. A later sign that
	// the broker kept the lease live is reported as a restore.
	writtenOff bool
}

func (bk *leaseBook) clearWrittenOff(id string) bool {
	bk.mu.Lock()
	defer bk.mu.Unlock()

	h := bk.leases[id]
	if h == nil || !h.writtenOff {
		return false
	}

	h.writtenOff = false

	return true
}

// leaseBook is the node's lease state, shared by the stream handler and the main loop. The copy with
// the higher revision wins, whichever channel carried it.
type leaseBook struct {
	mu       sync.Mutex
	leases   map[string]*held
	notified map[string]bool
	acked    []string
	late     map[string]lateReply
	connect  bool
}

type lateReply struct {
	holder record.Envelope
	kind   string
	anchor handoff.Anchor
}

func newBook() *leaseBook {
	return &leaseBook{leases: map[string]*held{}, notified: map[string]bool{}, late: map[string]lateReply{}}
}

// apply keeps l when its revision is higher than the held one. It returns the previous state and
// whether the lease was kept.
func (bk *leaseBook) apply(l wire.Lease, sentAt time.Duration, haveSent bool) (prev string, kept bool) {
	bk.mu.Lock()
	defer bk.mu.Unlock()

	cur := bk.leases[l.ID]
	if cur != nil && cur.lease.Revision >= l.Revision {
		if haveSent && cur.lease.Revision == l.Revision && cur.lease.State == wire.StateLive {
			cur.sentAt = sentAt
		}

		return "", false
	}

	next := &held{lease: l}
	if cur != nil {
		prev, next.sentAt = cur.lease.State, cur.sentAt
	}

	if haveSent {
		next.sentAt = sentAt
	}

	bk.leases[l.ID] = next

	return prev, true
}

func (bk *leaseBook) state(id string) (string, bool) {
	bk.mu.Lock()
	defer bk.mu.Unlock()

	h := bk.leases[id]
	if h == nil {
		return "", false
	}

	return h.lease.State, true
}

func (bk *leaseBook) once(key string) bool {
	bk.mu.Lock()
	defer bk.mu.Unlock()

	if bk.notified[key] {
		return false
	}

	bk.notified[key] = true

	return true
}

func (bk *leaseBook) ack(id string) {
	bk.mu.Lock()
	defer bk.mu.Unlock()

	if !slices.Contains(bk.acked, id) {
		bk.acked = append(bk.acked, id)
	}
}

func (bk *leaseBook) takeAcked() []string {
	bk.mu.Lock()
	defer bk.mu.Unlock()

	out := bk.acked
	bk.acked = nil

	return out
}

func (bk *leaseBook) setConnected(c bool) {
	bk.mu.Lock()
	bk.connect = c
	bk.mu.Unlock()
}

func (bk *leaseBook) connected() bool {
	bk.mu.Lock()
	defer bk.mu.Unlock()

	return bk.connect
}

func wireHolder(env record.Envelope) wire.Holder {
	h := env.Holder

	return wire.Holder{
		Key: handoff.HolderKey(h, env.Liveness), Agent: h.Agent, SessionID: h.SessionID, Handle: h.Handle, Name: h.Name, PID: h.PID,
		User: h.User,
	}
}

func (n *Node) stateOpts() state.Options {
	o := n.opts.StateOptions
	o.UID = n.opts.UID

	return o
}

// withLock runs fn under the state lock. The node holds it for a local write only, never across a
// broker round trip.
func (n *Node) withLock(fn func(*state.Locked) error) error {
	dir, err := state.Open(n.opts.StateDir, n.stateOpts())
	if err != nil {
		return err
	}

	l, err := dir.Lock()
	if err != nil {
		return err
	}

	defer func() { _ = l.Unlock() }()

	return fn(l)
}

// notice writes a notice to a holder. Its ID carries the revision it reports, so two lapses before the
// holder's next call give two notices.
func (n *Node) notice(env record.Envelope, target, cause string, rev uint64, detail string) {
	cause = fmt.Sprintf("%s-r%d", cause, rev)

	note := &record.Notice{
		Envelope: record.Envelope{
			Version: record.ParentVersion, ID: record.NoticeID(target, cause), Footprint: record.NoneFootprint(),
			ClearWhenDead: true, Liveness: env.Liveness, Machine: env.Machine, Holder: env.Holder,
		},
		Cause: cause, Target: target, Detail: detail, At: n.opts.Now().UTC(),
	}

	data, err := note.Encode()
	if err != nil {
		n.warn(fmt.Sprintf("encoding a notice for %s: %v", target, err))

		return
	}

	err = n.withLock(func(l *state.Locked) error {
		err := l.PublishNamed(state.NoticesDir, note.ID+".json", data)
		if errors.Is(err, state.ErrExists) {
			return nil
		}

		return err
	})
	if err != nil {
		n.warn(fmt.Sprintf("writing a notice for %s: %v", target, err))
	}
}

func (n *Node) clockNow() time.Duration { return n.opts.Clock.Now() }

// onLease handles a lease state from the stream, a snapshot answer, or a reply. It writes the notices a
// change calls for, once per revision.
func (n *Node) onLease(l wire.Lease, fromReply bool) {
	written := l.State == wire.StateLive && n.book.clearWrittenOff(l.ID)

	prev, kept := n.book.apply(l, 0, false)
	if !kept && !written {
		return
	}

	sc, err := handoff.ReadSidecar(n.opts.StateDir, l.ID)
	if err != nil {
		return
	}

	rev := l.Revision

	switch l.State {
	case wire.StateLive:
		if (prev == wire.StateLapsed || written) && !fromReply && n.book.once(fmt.Sprintf("%s/restored/%d", l.ID, rev)) {
			n.notice(sc.Holder, l.ID, "broker-restored", rev, fmt.Sprintf("your broker lease %s showed as lapsed here, but the "+
				"broker has it live. It is restored", l.ID))
		}
	case wire.StateTaken:
		n.lost(sc, l, rev, fmt.Sprintf("your broker lease %s lapsed and was lost to %s, who claimed names it covered. You no longer "+
			"hold it", l.ID, l.TakenBy))
	case wire.StateEnded:
		n.lost(sc, l, rev, fmt.Sprintf("your broker lease %s was broken by %s. Its anchor lease %s stays as an ordinary lease of "+
			"yours", l.ID, l.EndedBy, l.Anchor))
	}
}

// lost writes the loss notice for an end fact, drops the sidecar, and queues the ack that lets the
// broker forget the fact.
func (n *Node) lost(sc handoff.Sidecar, l wire.Lease, rev uint64, detail string) {
	if n.book.once(fmt.Sprintf("%s/%s/%d", l.ID, l.State, rev)) {
		n.notice(sc.Holder, l.ID, "broker-"+l.State, rev, detail)
	}

	_ = n.withLock(func(lk *state.Locked) error { return handoff.RemoveSidecar(lk, l.ID) })

	n.book.ack(l.ID)
}

// handleEvent applies a lease event and then hands every event to the node's own handler.
func (n *Node) handleEvent(ev wire.Event) error {
	if ev.Kind == wire.EventLease {
		var l wire.Lease
		if err := json.Unmarshal(ev.Data, &l); err != nil {
			return fmt.Errorf("decoding a lease event: %w", err)
		}

		n.onLease(l, false)
	}

	if n.opts.OnEvent != nil {
		return n.opts.OnEvent(ev)
	}

	return nil
}

func (n *Node) applySnapshotAck(ack wire.SnapshotAck) {
	for _, l := range ack.Leases {
		n.onLease(l, false)
	}

	n.applyOverlaps(ack.Overlaps)
}

type localView struct {
	recs    map[string]*record.Record
	unknown map[string]bool
}

func (n *Node) local() (localView, error) {
	dir, err := state.OpenRead(n.opts.StateDir, state.Options{UID: n.opts.UID})
	if err != nil {
		return localView{}, err
	}

	files, err := dir.Scan(state.LeasesDir)
	if err != nil {
		return localView{}, err
	}

	v := localView{recs: map[string]*record.Record{}, unknown: map[string]bool{}}

	for _, f := range files {
		rec, err := record.Decode(f.Data)
		if err != nil || f.BadName || rec.ID != f.ID || !rec.BodyKnown {
			v.unknown[f.ID] = true

			continue
		}

		v.recs[f.ID] = rec
	}

	return v, nil
}

type anchorVerdict int

const (
	anchorOK anchorVerdict = iota
	anchorEnded
	anchorUnknown
)

// checkAnchor follows the anchor through absorbs and checks that its keys still cover the lease's
// paths. What it cannot read counts as unchecked, never as ended.
func (n *Node) checkAnchor(sc handoff.Sidecar, lv localView) (verdict anchorVerdict, why string) {
	id := sc.Anchor.ID

	for range 16 {
		if rec, ok := lv.recs[id]; ok {
			return n.covers(sc, rec)
		}

		if lv.unknown[id] {
			return anchorUnknown, ""
		}

		next := ""

		for _, rec := range lv.recs {
			if slices.Contains(rec.Absorbs, id) {
				next = rec.ID

				break
			}
		}

		if next == "" {
			return anchorEnded, fmt.Sprintf("its anchor lease %s no longer exists", sc.Anchor.ID)
		}

		id = next
	}

	return anchorUnknown, ""
}

func (n *Node) covers(sc handoff.Sidecar, rec *record.Record) (verdict anchorVerdict, why string) {
	if rec.Domain.Kind != record.KindPath {
		return anchorEnded, fmt.Sprintf("its anchor lease %s no longer names paths", sc.Anchor.ID)
	}

	for _, k := range sc.Anchor.Covered {
		if !keys.Covered(k, rec.Domain.Keys, n.opts.Finder) {
			return anchorEnded, fmt.Sprintf("its anchor lease %s no longer covers %s", sc.Anchor.ID, k.Path)
		}
	}

	return anchorOK, ""
}

// maintain is the node's whole lease upkeep in one pass: check each anchor, report deaths, and renew
// every holder's broker leases in one call.
func (n *Node) maintain(ctx context.Context) {
	scs, unknown, err := handoff.Sidecars(n.opts.StateDir)
	if err != nil {
		n.warn(fmt.Sprintf("reading the sidecars: %v", err))

		return
	}

	if len(unknown) > 0 {
		n.warn(fmt.Sprintf("sidecars %v cannot be used and are left unchecked", unknown))
	}

	if len(scs) == 0 {
		n.publishDeadlines(scs)

		return
	}

	lv, err := n.local()
	if err != nil {
		n.warn(fmt.Sprintf("reading the leases: %v", err))

		return
	}

	var items []wire.RenewItem

	live := map[string]handoff.Sidecar{}

	for _, sc := range scs {
		if st, ok := n.book.state(sc.ID); ok && (st == wire.StateTaken || st == wire.StateEnded) {
			continue
		}

		if verdict, why := n.checkAnchor(sc, lv); verdict == anchorEnded {
			n.endLease(ctx, sc, wire.CauseAnchorEnded, fmt.Sprintf("your broker lease %s ended because %s. The broker will not "+
				"revive it", sc.ID, why))

			continue
		}

		res := wire.LiveAlive

		if n.opts.Checker != nil {
			switch r := n.opts.Checker.Check(sc.Holder); r.Status {
			case liveness.Dead:
				n.endLease(ctx, sc, wire.CauseHolderDead, "")

				continue
			case liveness.Unknown:
				res = wire.LiveUnknown
			}
		}

		items = append(items, wire.RenewItem{ID: sc.ID, Liveness: res})
		live[sc.ID] = sc
	}

	if len(items) > 0 {
		sent := n.clockNow()

		var out wire.Renewed
		if err := n.cl.call(ctx, wire.PathRenew, wire.Renew{Version: wire.Current, Items: items}, &out); err != nil {
			n.warn(fmt.Sprintf("renewing: %v", err))
		} else {
			n.applyRenewed(out, sent, live)
		}
	}

	n.publishDeadlines(scs)
}

// endLease reports an anchor failure or a death to the broker and forgets the lease.
func (n *Node) endLease(ctx context.Context, sc handoff.Sidecar, cause, detail string) {
	var res wire.Result

	err := n.cl.call(ctx, wire.PathRelease, wire.Release{
		Version: wire.Current, RequestID: sc.ID + "-" + cause, ID: sc.ID, Holder: wireHolder(sc.Holder), Cause: cause,
	}, &res)
	if err != nil {
		n.warn(fmt.Sprintf("ending %s: %v", sc.ID, err))

		return
	}

	if detail != "" {
		n.notice(sc.Holder, sc.ID, "broker-anchor-ended", 0, detail)
	}

	_ = n.withLock(func(l *state.Locked) error { return handoff.RemoveSidecar(l, sc.ID) })

	n.book.mu.Lock()
	delete(n.book.leases, sc.ID)
	n.book.mu.Unlock()
}

// applyRenewed applies one renew answer. sent is the clock reading before the call.
func (n *Node) applyRenewed(out wire.Renewed, sent time.Duration, live map[string]handoff.Sidecar) {
	for _, r := range out.Results {
		sc, ok := live[r.ID]
		if !ok {
			sc, _ = handoff.ReadSidecar(n.opts.StateDir, r.ID)
		}

		switch r.Code {
		case wire.CodeRenewed:
			if r.Lease == nil {
				continue
			}

			n.recordToken(sc, r.Lease.Token)

			restored := n.book.clearWrittenOff(r.ID)

			n.book.apply(*r.Lease, sent, true)

			if restored && n.book.once(fmt.Sprintf("%s/restored/%d", r.ID, r.Lease.Revision)) {
				n.notice(sc.Holder, r.ID, "broker-restored", r.Lease.Revision, fmt.Sprintf("your broker lease %s showed as lapsed here, "+
					"but the broker has it live. It is restored", r.ID))
			}
		case wire.CodeRevived:
			if r.Lease == nil {
				continue
			}

			n.recordToken(sc, r.Lease.Token)

			n.book.apply(*r.Lease, sent, true)

			if n.book.once(fmt.Sprintf("%s/revived/%d", r.ID, r.Lease.Revision)) {
				n.notice(sc.Holder, r.ID, "broker-revived", r.Lease.Revision, fmt.Sprintf("your broker lease %s was unprotected for %s "+
					"because its renewals stopped, and nobody claimed it. It is live again", r.ID,
					(time.Duration(r.LapsedMillis)*time.Millisecond).Round(time.Second)))
			}
		case wire.CodeTaken:
			n.onLease(wire.Lease{ID: r.ID, State: wire.StateTaken, Revision: n.nextRev(r.ID), TakenBy: r.TakenBy, Anchor: sc.Anchor.ID}, true)
		case wire.CodeEnded:
			n.onLease(wire.Lease{ID: r.ID, State: wire.StateEnded, Revision: n.nextRev(r.ID), EndedBy: r.EndedBy, Anchor: sc.Anchor.ID}, true)
		case wire.CodeNotFound:
			n.notFound(sc, r.ID)
		}
	}
}

// nextRev gives a reply-only state the next revision after the one held, since the broker's own
// revision for a fact was not in the reply.
func (n *Node) nextRev(id string) uint64 {
	n.book.mu.Lock()
	defer n.book.mu.Unlock()

	if h := n.book.leases[id]; h != nil {
		return h.lease.Revision + 1
	}

	return 1
}

// notFound handles a renewal the broker had no record of. A lease the node already holds as ended or
// taken has already been announced.
func (n *Node) notFound(sc handoff.Sidecar, id string) {
	if st, ok := n.book.state(id); ok && (st == wire.StateTaken || st == wire.StateEnded) {
		return
	}

	if sc.ID == "" {
		return
	}

	rev := n.nextRev(id)

	if n.book.once(fmt.Sprintf("%s/notfound", id)) {
		n.notice(sc.Holder, id, "broker-not-found", rev, fmt.Sprintf("your broker lease %s is gone: the broker had no record of it. "+
			"Claim it again if you still need it", id))
	}

	_ = n.withLock(func(l *state.Locked) error { return handoff.RemoveSidecar(l, id) })

	n.book.mu.Lock()
	delete(n.book.leases, id)
	n.book.mu.Unlock()
}

// publishDeadlines writes one deadline file per holder, and removes the file of a holder with no
// leases left.
func (n *Node) publishDeadlines(scs []handoff.Sidecar) {
	byHolder := map[string][]handoff.DeadlineLease{}
	now := n.clockNow()
	wall := n.opts.Now()

	for _, sc := range scs {
		hk := handoff.HolderKey(sc.Holder.Holder, sc.Holder.Liveness)

		n.book.mu.Lock()
		h := n.book.leases[sc.ID]
		n.book.mu.Unlock()

		if h == nil || h.lease.State == wire.StateTaken || h.lease.State == wire.StateEnded {
			continue
		}

		term := time.Duration(h.lease.TermMillis) * time.Millisecond
		if term == 0 {
			term = n.term
		}

		lapses := wall.Add(h.sentAt + term - now)

		if h.lease.State == wire.StateLive && !lapses.After(wall) {
			n.book.mu.Lock()
			h.writtenOff = true
			n.book.mu.Unlock()
		}

		byHolder[hk] = append(byHolder[hk], handoff.DeadlineLease{
			ID: sc.ID, State: h.lease.State, Liveness: h.lease.Liveness, LapsesAt: lapses,
		})
	}

	n.deadlineMu.Lock()
	defer n.deadlineMu.Unlock()

	for hk, ls := range byHolder {
		sort.Slice(ls, func(i, j int) bool { return ls[i].ID < ls[j].ID })

		err := handoff.WriteDeadlines(n.opts.StateDir, handoff.Deadlines{
			Holder: hk, Connected: n.book.connected(), WrittenAt: wall, Leases: ls,
		})
		if err != nil {
			n.warn(fmt.Sprintf("writing the deadline file: %v", err))
		}

		n.written[hk] = true
	}

	for hk := range n.written {
		if _, ok := byHolder[hk]; !ok {
			handoff.RemoveDeadlines(n.opts.StateDir, hk)
			delete(n.written, hk)
		}
	}
}

func (n *Node) sendSnapshotAck(ctx context.Context) error {
	snap, err := n.snapshot()
	if err != nil {
		return fmt.Errorf("reading the leases to mirror: %w", err)
	}

	snap.Acked = n.book.takeAcked()

	var ack wire.SnapshotAck

	if err := n.cl.callOpt(ctx, wire.PathSnapshot, snap, &ack); err != nil {
		n.book.mu.Lock()
		n.book.acked = append(n.book.acked, snap.Acked...)
		n.book.mu.Unlock()

		return err
	}

	n.applySnapshotAck(ack)

	return nil
}
