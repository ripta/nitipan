package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/space-armada/nitipan/internal/repoid"
	"github.com/space-armada/nitipan/internal/wire"
)

const (
	// MaxDerivation is the highest root-set derivation this broker knows. A claim above it is refused
	// rather than compared by guess.
	MaxDerivation = 1

	// DefaultDerivationFloor is the lowest root-set derivation a user claim may carry.
	DefaultDerivationFloor = 1

	// OutcomeTTL is how long a request outcome is kept, so a retry within the window finds it.
	OutcomeTTL = 7 * 24 * time.Hour

	// UnseenLabelTTL is how long a lapsed lease outlives the last time its label was seen.
	UnseenLabelTTL = 30 * 24 * time.Hour
)

// grant is one persisted lease. The in-memory deadline lives beside it in Broker.dl, since a monotonic
// reading means nothing after a restart.
type grant struct {
	wire.Lease

	Machine    string `json:"machine"`
	UID        int    `json:"uid"`
	Derivation int    `json:"derivation"`
}

func (g *grant) nodeKey() NodeKey { return NodeKey{MachineID: g.Machine, UID: g.UID} }

// outcome is a stored answer to a request. The request ID is the idempotency key.
type outcome struct {
	At     time.Time       `json:"at"`
	Result json.RawMessage `json:"result"`

	// Node is the node that made the request. Another node's retry of the ID is refused. An outcome
	// stored without one replays to anyone.
	Node string `json:"node,omitempty"`
}

// gstate is the typed form of the store's lease fields. A change is built on a copy, persisted, then
// swapped in.
type gstate struct {
	grants   map[string]*grant
	outcomes map[string]*outcome
	counter  uint64
}

func (s *gstate) clone() *gstate {
	out := &gstate{grants: make(map[string]*grant, len(s.grants)), outcomes: make(map[string]*outcome, len(s.outcomes)), counter: s.counter}

	for k, v := range s.grants {
		cp := *v
		out.grants[k] = &cp
	}

	for k, v := range s.outcomes {
		out.outcomes[k] = v
	}

	return out
}

func loadGState(d *Data) (*gstate, error) {
	s := &gstate{grants: map[string]*grant{}, outcomes: map[string]*outcome{}, counter: d.Counter}

	for k, raw := range d.Leases {
		var g grant
		if err := json.Unmarshal(raw, &g); err != nil {
			return nil, fmt.Errorf("decoding the stored lease %s: %w", k, err)
		}

		s.grants[k] = &g
	}

	for k, raw := range d.Outcomes {
		var o outcome
		if err := json.Unmarshal(raw, &o); err != nil {
			return nil, fmt.Errorf("decoding the stored outcome %s: %w", k, err)
		}

		s.outcomes[k] = &o
	}

	return s, nil
}

func (s *gstate) encode(d *Data) error {
	d.Leases = make(map[string]json.RawMessage, len(s.grants))
	d.Outcomes = make(map[string]json.RawMessage, len(s.outcomes))
	d.Counter = s.counter

	for k, g := range s.grants {
		raw, err := json.Marshal(g)
		if err != nil {
			return fmt.Errorf("encoding lease %s: %w", k, err)
		}

		d.Leases[k] = raw
	}

	for k, o := range s.outcomes {
		raw, err := json.Marshal(o)
		if err != nil {
			return fmt.Errorf("encoding outcome %s: %w", k, err)
		}

		d.Outcomes[k] = raw
	}

	return nil
}

// mutate applies fn to a copy of the lease state, persists the copy together with the rest of the
// store in one write, and swaps it in. The caller holds b.mu.
func (b *Broker) mutate(fn func(*gstate) error) error {
	next := b.g.clone()

	if err := fn(next); err != nil {
		return err
	}

	err := b.commit(func(d *Data) error { return next.encode(d) })
	if err != nil {
		return err
	}

	b.g = next

	return nil
}

// startGrants loads the stored leases. Every live lease gets one full term from now, which is the
// restart grace.
func (b *Broker) startGrants() error {
	s, err := loadGState(b.store.Data())
	if err != nil {
		return err
	}

	b.g = s
	b.dl = map[string]time.Duration{}

	now := b.cfg.Clock.Now()
	b.graceUntil = now + b.cfg.Term
	b.lastSweep = now

	for id, g := range s.grants {
		if g.State == wire.StateLive {
			b.dl[id] = now + b.cfg.Term
		}
	}

	return nil
}

// Wake tells the broker the machine woke from sleep. Nothing expires for one more term, so every node
// has a chance to renew.
func (b *Broker) Wake() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.graceUntil = b.cfg.Clock.Now() + b.cfg.Term
}

// Sweep lapses the leases whose term ran out and prunes what has aged out.
func (b *Broker) Sweep() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.sweepLocked()
}

func (b *Broker) sweepLocked() {
	now := b.cfg.Clock.Now()
	b.lastSweep = now

	if now < b.graceUntil {
		return
	}

	wall := b.cfg.Now()

	var lapse, drop []string

	for id, g := range b.g.grants {
		switch {
		case g.State == wire.StateLive && b.dl[id] < now:
			lapse = append(lapse, id)
		case g.State == wire.StateLapsed && b.unseen(g, wall):
			drop = append(drop, id)
		}
	}

	var stale []string

	for k, o := range b.g.outcomes {
		if wall.Sub(o.At) > OutcomeTTL {
			stale = append(stale, k)
		}
	}

	if len(lapse)+len(drop)+len(stale) == 0 {
		return
	}

	sort.Strings(lapse)

	err := b.mutate(func(s *gstate) error {
		for _, id := range lapse {
			g := s.grants[id]
			g.State = wire.StateLapsed
			g.Revision++
			g.LapsedMillis = wall.UnixMilli()
			g.ExpiresMillis = wall.Add(-(now - b.dl[id])).UnixMilli()
		}

		for _, id := range drop {
			delete(s.grants, id)
		}

		for _, k := range stale {
			delete(s.outcomes, k)
		}

		return nil
	})
	if err != nil {
		return
	}

	for _, id := range lapse {
		b.announceLocked(b.g.grants[id])
	}
}

// unseen reports whether the lease's label has not been seen for 30 days.
func (b *Broker) unseen(g *grant, wall time.Time) bool {
	row, ok := b.store.Data().Labels[g.Node]

	return ok && !row.LastSeen.IsZero() && wall.Sub(row.LastSeen) > UnseenLabelTTL
}

// announceLocked queues the lease's new state for its node. The reply to a call can arrive before or
// after this event, and the node orders them by revision.
func (b *Broker) announceLocked(g *grant) {
	_, _ = b.enqueueLocked(g.nodeKey(), wire.EventLease, b.viewOf(g))
}

// viewOf renders a grant for the wire. A live lease's expiry follows its monotonic deadline.
func (b *Broker) viewOf(g *grant) wire.Lease {
	l := g.Lease
	l.TermMillis = b.cfg.Term.Milliseconds()

	if g.State == wire.StateLive {
		l.ExpiresMillis = b.cfg.Now().Add(b.dl[g.ID] - b.cfg.Clock.Now()).UnixMilli()
	}

	return l
}

func holderText(h wire.Holder) string {
	var parts []string

	if h.Handle != "" {
		parts = append(parts, fmt.Sprintf("handle %q", h.Handle))
	}

	if h.Name != "" {
		parts = append(parts, fmt.Sprintf("name %q", h.Name))
	}

	agent := h.Agent
	if agent == "" {
		agent = "an unidentified process"
	}

	if h.SessionID != "" {
		agent += " session " + h.SessionID
	}

	return strings.Join(append(parts, agent), ", ")
}

func (b *Broker) holderLabel(g *grant) string {
	return holderText(g.Holder) + " on " + g.Node
}

func sameHolder(a *grant, node NodeKey, h wire.Holder) bool {
	return a.nodeKey() == node && a.Holder.Key == h.Key
}

func (b *Broker) replay(w http.ResponseWriter, node NodeKey, id string) bool {
	if o, ok := b.g.outcomes[id]; ok {
		if o.Node != "" && o.Node != node.String() {
			writeErr(w, http.StatusConflict, wire.CodeBadRequest, "the request ID belongs to another node's request")

			return true
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(o.Result)

		return true
	}

	return false
}

// answer stores the result as the request's outcome in the same write as the change, then replies.
func (b *Broker) answer(w http.ResponseWriter, node NodeKey, id string, res wire.Result, change func(*gstate)) {
	res.Version = wire.Current

	raw, err := json.Marshal(res)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, wire.CodeUnavailable, err.Error())

		return
	}

	err = b.mutate(func(s *gstate) error {
		if change != nil {
			change(s)
		}

		s.outcomes[id] = &outcome{At: b.cfg.Now(), Result: raw, Node: node.String()}

		return nil
	})
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, wire.CodeUnavailable, err.Error())

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

// FenceMargin is how far past the broker's clock a reported fence token may sit and still raise the
// counter. A token above it cannot have come from an honest counter, so one garbage value never pushes
// the counter to its top.
const FenceMargin = 24 * time.Hour

// nextToken returns the token for a new grant: the larger of the next counter value and the wall clock in
// microseconds. The clock term keeps a counter restored from an older file ahead of every token issued
// before the restore, as long as the clock has moved forward.
func nextToken(counter uint64, now time.Time) uint64 {
	return max(counter+1, microseconds(now))
}

// counterExhausted answers 503 and reports true when the counter cannot advance. Only a corrupt or
// hand-edited store holds a counter that high. Adding one would wrap to zero and issue a token below every
// earlier one, so a claim fails closed instead.
func (b *Broker) counterExhausted(w http.ResponseWriter) bool {
	if b.g.counter != math.MaxUint64 {
		return false
	}

	writeErr(w, http.StatusServiceUnavailable, wire.CodeUnavailable, "the broker's token counter is at its limit, "+
		"which only a damaged store causes. Restore the store or repair its counter")

	return true
}

// microseconds is t as microseconds since the epoch. A time before the epoch reads as zero.
func microseconds(t time.Time) uint64 {
	if us := t.UnixMicro(); us > 0 {
		return uint64(us)
	}

	return 0
}

func validRequestID(id string) bool {
	return id != "" && len(id) <= 64 && !strings.ContainsAny(id, "/\x00")
}

func (b *Broker) handleClaim(w http.ResponseWriter, r *http.Request, node NodeKey) {
	var req wire.Claim

	if !decodeBody(w, r, &req, func() wire.Version { return req.Version }) {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.sweepLocked()

	if !validRequestID(req.ID) {
		writeErr(w, http.StatusBadRequest, wire.CodeBadRequest, "the claim names no usable ID")

		return
	}

	if b.replay(w, node, req.ID) {
		return
	}

	if req.Derivation > MaxDerivation {
		writeErr(w, http.StatusBadRequest, wire.CodeVersion, fmt.Sprintf("broker too old: the claim's root-set derivation is %d, and this "+
			"broker knows up to %d. Upgrade the broker", req.Derivation, MaxDerivation))

		return
	}

	if msg := b.invalidClaim(req); msg != "" {
		writeErr(w, http.StatusBadRequest, wire.CodeBadRequest, msg)

		return
	}

	var live, lapsed []*grant

	for _, g := range b.g.grants {
		if g.ID == req.ID || !repoid.OverlapAny(g.Entries, req.Entries) {
			continue
		}

		if sameHolder(g, node, req.Holder) {
			// A holder never conflicts with itself. The same names again return the lease it holds.
			if g.State == wire.StateLive && sameEntries(g.Entries, req.Entries) {
				res := wire.Result{Code: wire.CodeGranted, Message: "already held", Lease: ptr(b.viewOf(g))}
				b.answer(w, node, req.ID, res, nil)

				return
			}

			continue
		}

		switch g.State {
		case wire.StateLive:
			live = append(live, g)
		case wire.StateLapsed:
			lapsed = append(lapsed, g)
		}
	}

	if len(live) > 0 {
		sort.Slice(live, func(i, j int) bool { return live[i].ID < live[j].ID })

		res := wire.Result{Code: wire.CodeRefused, Message: "the claim overlaps " + b.holders(live)}
		for _, g := range live {
			res.Others = append(res.Others, b.viewOf(g))
		}

		b.answer(w, node, req.ID, res, nil)

		return
	}

	rec := b.store.Data().Nodes[node.String()]
	taker := holderText(req.Holder) + " on " + rec.Label

	fresh := &grant{
		Lease: wire.Lease{
			ID: req.ID, Reach: req.Reach, State: wire.StateLive, Revision: 1,
			Entries: req.Entries, Anchor: req.Anchor, Holder: req.Holder, ActingFor: req.ActingFor,
			Description: req.Description, Node: rec.Label, Liveness: wire.LiveAlive,
		},
		Machine: node.MachineID, UID: node.UID, Derivation: req.Derivation,
	}

	if b.counterExhausted(w) {
		return
	}

	b.dl[req.ID] = b.cfg.Clock.Now() + b.cfg.Term

	taken := slices.Clone(lapsed)

	// The token is fixed once, here. The reply and the committed change both use it, so they never differ.
	fresh.Token = nextToken(b.g.counter, b.cfg.Now())

	res := wire.Result{Code: wire.CodeGranted, Lease: ptr(b.viewOf(fresh))}
	change := func(s *gstate) {
		s.counter = fresh.Token
		s.grants[fresh.ID] = fresh

		for _, g := range taken {
			t := s.grants[g.ID]
			t.State = wire.StateTaken
			t.Revision++
			t.TakenBy = taker
		}
	}

	b.answer(w, node, req.ID, res, change)

	if b.g.grants[req.ID] == nil {
		delete(b.dl, req.ID)

		return
	}

	for _, g := range taken {
		delete(b.dl, g.ID)
		b.announceLocked(b.g.grants[g.ID])
	}
}

func ptr[T any](v T) *T { return &v }

func (b *Broker) invalidClaim(req wire.Claim) string {
	switch {
	case req.Reach != wire.ReachUser && req.Reach != wire.ReachShared:
		return fmt.Sprintf("reach %q: the broker grants user and shared leases", req.Reach)
	case req.Anchor == "":
		return "a user or shared claim names the machine lease that anchors it"
	case req.Derivation < b.cfg.DerivationFloor:
		return fmt.Sprintf("the claim's root-set derivation is %d, and this broker accepts %d or later. Upgrade nitipan",
			req.Derivation, b.cfg.DerivationFloor)
	case len(req.Entries) == 0:
		return "the claim names no repo entries"
	}

	for _, e := range req.Entries {
		if len(e.Roots) == 0 {
			return "a repo entry names no root commit"
		}
	}

	return ""
}

func sameEntries(a, b []repoid.Name) bool {
	if len(a) != len(b) {
		return false
	}

	for _, x := range a {
		if !slices.ContainsFunc(b, func(y repoid.Name) bool { return repoid.Equal(x, y) }) {
			return false
		}
	}

	return true
}

func (b *Broker) holders(gs []*grant) string {
	parts := make([]string, len(gs))

	for i, g := range gs {
		parts[i] = fmt.Sprintf("lease %s held by %s", g.ID, b.holderLabel(g))
		if g.Liveness == wire.LiveUnknown {
			parts[i] += " (its liveness is unknown, which counts as alive)"
		}
	}

	return strings.Join(parts, "; ")
}

func (b *Broker) handleRenew(w http.ResponseWriter, r *http.Request, node NodeKey) {
	var req wire.Renew

	if !decodeBody(w, r, &req, func() wire.Version { return req.Version }) {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.sweepLocked()

	ns := b.node(node)
	b.beatIfStreaming(ns)

	now := b.cfg.Clock.Now()
	wall := b.cfg.Now()
	out := wire.Renewed{Version: wire.Current}

	var revive []string

	for _, it := range req.Items {
		g := b.g.grants[it.ID]

		res := wire.RenewResult{ID: it.ID}

		switch {
		case g == nil || g.nodeKey() != node:
			res.Code = wire.CodeNotFound
		case g.State == wire.StateLive:
			g.Liveness = it.Liveness
			b.dl[g.ID] = now + b.cfg.Term
			res.Code, res.Lease = wire.CodeRenewed, ptr(b.viewOf(g))
		case g.State == wire.StateLapsed && !ns.hasAnchor(g.Anchor):
			res.Code = wire.CodeEnded
		case g.State == wire.StateLapsed:
			revive = append(revive, g.ID)
			res.Code = wire.CodeRevived
			res.LapsedMillis = wall.UnixMilli() - g.LapsedMillis
		case g.State == wire.StateTaken:
			res.Code, res.TakenBy = wire.CodeTaken, g.TakenBy
		default:
			res.Code, res.EndedBy = wire.CodeEnded, g.EndedBy
		}

		out.Results = append(out.Results, res)
	}

	if len(revive) > 0 {
		err := b.mutate(func(s *gstate) error {
			for _, id := range revive {
				g := s.grants[id]
				g.State = wire.StateLive
				g.Revision++
				g.LapsedMillis = 0
			}

			return nil
		})
		if err != nil {
			writeErr(w, http.StatusServiceUnavailable, wire.CodeUnavailable, err.Error())

			return
		}

		for _, id := range revive {
			b.dl[id] = now + b.cfg.Term
		}

		for i := range out.Results {
			if out.Results[i].Code == wire.CodeRevived {
				out.Results[i].Lease = ptr(b.viewOf(b.g.grants[out.Results[i].ID]))
			}
		}
	}

	writeJSON(w, http.StatusOK, out)
}

func (ns *nodeState) hasAnchor(id string) bool {
	if !ns.hasSnapshot {
		return true
	}

	return slices.ContainsFunc(ns.snapshot, func(m wire.MirroredLease) bool { return m.ID == id })
}

func (b *Broker) handleRelease(w http.ResponseWriter, r *http.Request, node NodeKey) {
	var req wire.Release

	if !decodeBody(w, r, &req, func() wire.Version { return req.Version }) {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.sweepLocked()

	if !validRequestID(req.RequestID) {
		writeErr(w, http.StatusBadRequest, wire.CodeBadRequest, "the release names no usable request ID")

		return
	}

	if b.replay(w, node, req.RequestID) {
		return
	}

	var hit []*grant

	for _, g := range b.g.grants {
		if g.nodeKey() != node {
			continue
		}

		if req.Cause == wire.CauseReleased && g.Holder.Key != req.Holder.Key {
			continue
		}

		switch {
		case req.ID != "" && g.ID == req.ID:
			hit = append(hit, g)
		case req.ID == "" && req.Name != nil && g.State != wire.StateTaken && g.State != wire.StateEnded &&
			repoid.OverlapAny(g.Entries, []repoid.Name{*req.Name}):
			hit = append(hit, g)
		}
	}

	if len(hit) == 0 {
		b.answer(w, node, req.RequestID, wire.Result{Code: wire.CodeNotFound, Message: "the broker has no such lease of yours"}, nil)

		return
	}

	sort.Slice(hit, func(i, j int) bool { return hit[i].ID < hit[j].ID })

	res := wire.Result{Code: wire.CodeReleased, Lease: ptr(b.viewOf(hit[0]))}
	for _, g := range hit[1:] {
		res.Others = append(res.Others, b.viewOf(g))
	}

	b.answer(w, node, req.RequestID, res, func(s *gstate) {
		for _, g := range hit {
			delete(s.grants, g.ID)
		}
	})

	for _, g := range hit {
		delete(b.dl, g.ID)
	}
}

func (b *Broker) handleBreak(w http.ResponseWriter, r *http.Request, node NodeKey) {
	var req wire.Break

	if !decodeBody(w, r, &req, func() wire.Version { return req.Version }) {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.sweepLocked()

	if !validRequestID(req.RequestID) {
		writeErr(w, http.StatusBadRequest, wire.CodeBadRequest, "the break names no usable request ID")

		return
	}

	if b.replay(w, node, req.RequestID) {
		return
	}

	g := b.g.grants[req.ID]
	if g == nil {
		b.answer(w, node, req.RequestID, wire.Result{Code: wire.CodeNotFound, Message: "the broker has no such lease"}, nil)

		return
	}

	over := g.State == wire.StateTaken || g.State == wire.StateEnded

	// An end fact stays until its owner acks it, so the holder still learns who took or ended the
	// lease. Only the owner's own node may clear it early.
	if over && g.nodeKey() != node {
		b.answer(w, node, req.RequestID, wire.Result{Code: wire.CodeEnded, Lease: ptr(b.viewOf(g))}, nil)

		return
	}

	if !over && !req.Force {
		why := "its holder is alive"
		if g.Liveness == wire.LiveUnknown || g.State == wire.StateLapsed {
			why = "its holder's liveness is unknown, which counts as alive"
		}

		msg := fmt.Sprintf("lease %s is held by %s, and %s. Pass --force to break it", g.ID, b.holderLabel(g), why)
		b.answer(w, node, req.RequestID, wire.Result{Code: wire.CodeRefused, Message: msg, Lease: ptr(b.viewOf(g))}, nil)

		return
	}

	by := holderText(req.By)
	prev := b.viewOf(g)

	b.answer(w, node, req.RequestID, wire.Result{Code: wire.CodeBroken, Lease: &prev}, func(s *gstate) {
		if over {
			delete(s.grants, g.ID)

			return
		}

		e := s.grants[g.ID]
		e.State = wire.StateEnded
		e.Revision++
		e.EndedBy = by
	})

	delete(b.dl, g.ID)

	if !over && b.g.grants[g.ID] != nil {
		b.announceLocked(b.g.grants[g.ID])
	}
}

// handleFence takes a fence token that a pusher or receiver saw on a remote. A token above the counter and
// within the bound proves the broker lost state, so the counter rises to it. A token above the bound is
// refused without a write.
//
// The report grants nothing. It can only raise the counter, within a bound, so a forged report at worst
// skips token values. No ref, lease, or holder changes on its word, which is why the request handoff may
// carry it without a proof the session cannot make.
func (b *Broker) handleFence(w http.ResponseWriter, r *http.Request, _ NodeKey) {
	var req wire.FenceReport

	if !decodeBody(w, r, &req, func() wire.Version { return req.Version }) {
		return
	}

	if req.Token == 0 {
		writeErr(w, http.StatusBadRequest, wire.CodeBadRequest, "the fence report names no token")

		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	verdict := wire.FenceKnown

	switch bound := microseconds(b.cfg.Now().Add(FenceMargin)); {
	case req.Token <= b.g.counter:
	case req.Token > bound:
		verdict = wire.FenceUnusable
	default:
		err := b.mutate(func(s *gstate) error {
			s.counter = req.Token

			return nil
		})
		if err != nil {
			writeErr(w, http.StatusServiceUnavailable, wire.CodeUnavailable, err.Error())

			return
		}

		verdict = wire.FenceRaised
	}

	writeJSON(w, http.StatusOK, wire.FenceVerdict{Version: wire.Current, Verdict: verdict, Counter: b.g.counter})
}

func (b *Broker) handleList(w http.ResponseWriter, r *http.Request, _ NodeKey) {
	var req wire.ListRequest

	if !decodeBody(w, r, &req, func() wire.Version { return req.Version }) {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.sweepLocked()

	writeJSON(w, http.StatusOK, wire.Listed{Version: wire.Current, Leases: b.leasesLocked(func(g *grant) bool {
		return g.State == wire.StateLive || g.State == wire.StateLapsed
	})})
}

func (b *Broker) leasesLocked(keep func(*grant) bool) []wire.Lease {
	var ids []string

	for id, g := range b.g.grants {
		if keep(g) {
			ids = append(ids, id)
		}
	}

	sort.Strings(ids)

	out := make([]wire.Lease, 0, len(ids))
	for _, id := range ids {
		out = append(out, b.viewOf(b.g.grants[id]))
	}

	return out
}

// Leases returns the broker's view of every live and lapsed lease, for discovery.
func (b *Broker) Leases() []wire.Lease {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.sweepLocked()

	return b.leasesLocked(func(g *grant) bool { return g.State == wire.StateLive || g.State == wire.StateLapsed })
}

// Facts returns the end facts still waiting for their node's ack.
func (b *Broker) Facts() []wire.Lease {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.leasesLocked(func(g *grant) bool { return g.State == wire.StateTaken || g.State == wire.StateEnded })
}

// afterSnapshot applies what a node's snapshot says about its leases: acked end facts are dropped,
// and a lapsed lease whose anchor the snapshot omits is removed. It returns the node's leases.
func (b *Broker) afterSnapshot(node NodeKey, acked []string) []wire.Lease {
	ns := b.node(node)

	var drop []string

	for id, g := range b.g.grants {
		if g.nodeKey() != node {
			continue
		}

		switch {
		case (g.State == wire.StateTaken || g.State == wire.StateEnded) && slices.Contains(acked, id):
			drop = append(drop, id)
		case g.State == wire.StateLapsed && !ns.hasAnchor(g.Anchor):
			drop = append(drop, id)
		}
	}

	if len(drop) > 0 {
		_ = b.mutate(func(s *gstate) error {
			for _, id := range drop {
				delete(s.grants, id)
			}

			return nil
		})
	}

	return b.leasesLocked(func(g *grant) bool { return g.nodeKey() == node })
}

// RunSweeper lapses leases on a timer until ctx ends. A gap between ticks that the monotonic clock
// counts and Go's own clock does not is a sleep, and it starts a new grace.
func (b *Broker) RunSweeper(ctx context.Context, every time.Duration) {
	tick := time.NewTicker(every)
	defer tick.Stop()

	lastMono, lastGo := b.cfg.Clock.Now(), time.Now()

	for {
		select {
		case <-ctx.Done():
			return
		case <-b.done:
			return
		case <-tick.C:
			mono, goNow := b.cfg.Clock.Now(), time.Now()

			if slept := (mono - lastMono) - goNow.Sub(lastGo); slept > 2*every {
				b.Wake()
			}

			lastMono, lastGo = mono, goNow

			b.Sweep()
		}
	}
}
