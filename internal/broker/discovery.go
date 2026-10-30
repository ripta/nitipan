package broker

import (
	"fmt"
	"net/http"
	"sort"

	"github.com/space-armada/nitipan/internal/handoff"
	"github.com/space-armada/nitipan/internal/keys"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/repoid"
	"github.com/space-armada/nitipan/internal/wire"
)

// mirror is one machine lease from a node's last snapshot, decoded for a query. A record the broker
// cannot use has a nil rec and says why.
type mirror struct {
	node  NodeKey
	label string
	host  string
	stale bool

	id      string
	rec     *record.Record
	why     string
	derived *wire.Derived
}

// mirrorsLocked decodes every node's snapshot. A node that has sent nothing for a term reads as stale,
// whether or not its connection is open.
func (b *Broker) mirrorsLocked() []mirror {
	now := b.cfg.Clock.Now()

	var out []mirror

	for k, ns := range b.nodes {
		reg, ok := b.store.Data().Nodes[k.String()]
		if !ok {
			continue
		}

		stale := !ns.hasBeat || now-ns.lastBeat > b.cfg.Term
		host := b.store.Data().Labels[reg.Label].Host

		for _, ml := range ns.snapshot {
			m := mirror{node: k, label: reg.Label, host: host, stale: stale, id: ml.ID, derived: ml.Derived}
			m.rec, m.why = decodeMirror(ml)

			out = append(out, m)
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].label != out[j].label {
			return out[i].label < out[j].label
		}

		return out[i].id < out[j].id
	})

	return out
}

func decodeMirror(ml wire.MirroredLease) (rec *record.Record, why string) {
	if ml.Unknown != nil {
		return nil, "the node could not use the record: " + ml.Unknown.Reason
	}

	rec, err := record.Decode(ml.Record)
	if err != nil {
		return nil, "the record cannot be read: " + err.Error()
	}

	if !rec.BodyKnown {
		return nil, "the record names a domain kind this broker does not know"
	}

	return rec, ""
}

func (m mirror) isPath() bool { return m.rec != nil && m.rec.Domain.Kind == record.KindPath }

// repoUnknown says why the mirror's repo entries cannot be compared, or returns the empty string when
// they can.
func (m mirror) repoUnknown() string {
	switch {
	case m.derived == nil:
		return "the node sent no repo entries for it, as a node that predates them does"
	case m.derived.State != wire.DerivedOK:
		return "deriving its repo entries failed: " + m.derived.Reason
	case m.derived.Derivation > MaxDerivation:
		return fmt.Sprintf("its repo entries come from derivation %d, and this broker knows up to %d", m.derived.Derivation, MaxDerivation)
	}

	return ""
}

func (m mirror) overlapsPath(k keys.Key) bool {
	if !m.isPath() {
		return false
	}

	for _, mk := range m.rec.Domain.Keys {
		if keys.Overlap(k, mk, nil) {
			return true
		}
	}

	return false
}

func holderOfRecord(rec *record.Record) wire.Holder {
	h := rec.Holder

	return wire.Holder{
		Key: handoff.HolderKey(h, rec.Liveness), Agent: h.Agent, SessionID: h.SessionID, Handle: h.Handle, Name: h.Name,
		PID: h.PID, User: h.User,
	}
}

func (b *Broker) nodeStaleLocked(k NodeKey) bool {
	ns, ok := b.nodes[k]

	return !ok || !ns.hasBeat || b.cfg.Clock.Now()-ns.lastBeat > b.cfg.Term
}

func (b *Broker) foundGrant(g *grant) wire.Found {
	v := b.viewOf(g)

	return wire.Found{
		Lease: g.ID, Qualified: FormatAddress(g.Holder.Handle, g.Node), Reach: g.Reach, State: g.State,
		Stale: b.nodeStaleLocked(g.nodeKey()), Holder: g.Holder, Node: g.Node, Host: b.store.Data().Labels[g.Node].Host,
		Description: g.Description, Liveness: g.Liveness, Anchor: g.Anchor, Names: g.Entries, ExpiresMillis: v.ExpiresMillis,
	}
}

// foundMirror describes a mirror. State is mirrored, or stale when the node has gone quiet. A caller that
// cannot judge the mirror passes StateUnknown and says why.
func foundMirror(m mirror, state, why string) wire.Found {
	f := wire.Found{Lease: m.id, Reach: record.ReachMachine, State: state, Stale: m.stale, Node: m.label, Host: m.host, Why: why}

	if f.State == wire.StateMirrored && m.stale {
		f.State = wire.StateStale
	}

	if m.rec == nil {
		return f
	}

	f.Holder = holderOfRecord(m.rec)
	f.Qualified = FormatAddress(m.rec.Holder.Handle, m.label)
	f.Description = m.rec.Description
	f.Resource = m.rec.Domain.Name

	if m.derived != nil && m.derived.State == wire.DerivedOK {
		f.Names = m.derived.Entries
	}

	for _, k := range m.rec.Domain.Keys {
		f.Paths = append(f.Paths, k.Path)
	}

	return f
}

func shownGrant(g *grant) bool { return g.State == wire.StateLive || g.State == wire.StateLapsed }

func (b *Broker) sortedGrantsLocked(keep func(*grant) bool) []*grant {
	var out []*grant

	for _, g := range b.g.grants {
		if keep(g) {
			out = append(out, g)
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })

	return out
}

func sortFound(fs []wire.Found) {
	sort.Slice(fs, func(i, j int) bool {
		if fs[i].Node != fs[j].Node {
			return fs[i].Node < fs[j].Node
		}

		return fs[i].Lease < fs[j].Lease
	})
}

// judge decides how a mirror answers a who-holds query: it overlaps, it cannot be judged, or it does
// not match. A query about this machine's paths settles a same-machine mirror by its keys. A repo name
// needs the mirror's repo entries, and a mirror without usable ones reads as unknown, never as no
// overlap.
func (b *Broker) judge(asker NodeKey, q wire.WhoHolds, addr *Address, m mirror) (match bool, unknown string) {
	if addr != nil {
		return m.label == addr.Label && m.rec != nil && m.rec.Holder.Handle == addr.Handle, ""
	}

	if q.Resource != "" {
		switch {
		case m.rec == nil:
			unknown = m.why
		case m.rec.Domain.Kind == record.KindResource && m.rec.Domain.Name == q.Resource:
			return true, ""
		}

		return false, unknown
	}

	if q.Path != nil && m.node == asker {
		if m.rec == nil {
			return false, m.why
		}

		if m.overlapsPath(*q.Path) {
			return true, ""
		}
	}

	if len(q.Names) == 0 {
		return false, ""
	}

	switch {
	case m.rec == nil:
		return false, m.why
	case !m.isPath():
		return false, ""
	}

	if why := m.repoUnknown(); why != "" {
		return false, why
	}

	return repoid.OverlapAny(m.derived.Entries, q.Names), ""
}

func (b *Broker) handleWhoHolds(w http.ResponseWriter, r *http.Request, asker NodeKey) {
	var req wire.WhoHolds

	if !decodeBody(w, r, &req, func() wire.Version { return req.Version }) {
		return
	}

	if len(req.Names) == 0 && req.Path == nil && req.Resource == "" && req.Address == "" {
		writeErr(w, http.StatusBadRequest, wire.CodeBadRequest, "the query names nothing to look up")

		return
	}

	var addr *Address

	if req.Address != "" {
		a, err := b.ValidateAddress(req.Address)
		if err != nil {
			writeErr(w, http.StatusBadRequest, wire.CodeBadRequest, err.Error())

			return
		}

		addr = &a
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.sweepLocked()

	ans := wire.WhoHoldsAnswer{Version: wire.Current, Found: []wire.Found{}}

	for _, g := range b.sortedGrantsLocked(shownGrant) {
		hit := len(req.Names) > 0 && repoid.OverlapAny(g.Entries, req.Names)
		if addr != nil {
			hit = g.Node == addr.Label && g.Holder.Handle == addr.Handle
		}

		if hit {
			ans.Found = append(ans.Found, b.foundGrant(g))
		}
	}

	unjudged := 0

	for _, m := range b.mirrorsLocked() {
		match, unknown := b.judge(asker, req, addr, m)

		switch {
		case match:
			ans.Found = append(ans.Found, foundMirror(m, wire.StateMirrored, ""))
		case unknown != "":
			unjudged++

			ans.Found = append(ans.Found, foundMirror(m, wire.StateUnknown, unknown))
		}
	}

	sortFound(ans.Found)

	if unjudged > 0 {
		ans.Notes = append(ans.Notes, fmt.Sprintf("%d mirrored lease(s) could not be judged and are listed as unknown. "+
			"They may overlap the subject", unjudged))
	}

	writeJSON(w, http.StatusOK, ans)
}

func (b *Broker) handleWhoIs(w http.ResponseWriter, r *http.Request, _ NodeKey) {
	var req wire.WhoIs

	if !decodeBody(w, r, &req, func() wire.Version { return req.Version }) {
		return
	}

	if req.Name == "" {
		writeErr(w, http.StatusBadRequest, wire.CodeBadRequest, "the query names no holder")

		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.sweepLocked()

	ans := wire.WhoIsAnswer{Version: wire.Current, Found: []wire.Found{}}
	holders := map[string]bool{}

	for _, g := range b.sortedGrantsLocked(shownGrant) {
		if g.Holder.Name == req.Name || g.Holder.Handle == req.Name {
			ans.Found = append(ans.Found, b.foundGrant(g))
			holders[g.Node+"\x00"+g.Holder.Key] = true
		}
	}

	unjudged := 0

	for _, m := range b.mirrorsLocked() {
		if m.rec == nil {
			// An unreadable record names no holder, so it cannot be matched. It may be the one asked for.
			unjudged++

			ans.Found = append(ans.Found, foundMirror(m, wire.StateUnknown, m.why))

			continue
		}

		if m.rec.Holder.Name != req.Name && m.rec.Holder.Handle != req.Name {
			continue
		}

		ans.Found = append(ans.Found, foundMirror(m, wire.StateMirrored, ""))
		holders[m.label+"\x00"+holderOfRecord(m.rec).Key] = true
	}

	sortFound(ans.Found)

	if unjudged > 0 {
		ans.Notes = append(ans.Notes, fmt.Sprintf("%d mirrored lease(s) have a record the broker cannot read and are listed as unknown. "+
			"Their holder may carry this name, so the holder count may be low", unjudged))
	}

	ans.Holders = len(holders)
	ans.Duplicate = len(holders) > 1

	writeJSON(w, http.StatusOK, ans)
}

// overlapsFor lists the mirrored machine leases a live user or shared grant overlaps. Neither side is
// refused. A mirror with no usable repo entries cannot be compared, so it is left to the discovery
// query, which reports it as unknown.
//
// The grant's own anchor and its holder's other leases on the same machine are the holder's own files,
// and a sub-lease delegated from the anchor stays inside it.
func (b *Broker) overlapsFor(g *grant, mirrors []mirror) []wire.Overlap {
	if g.State != wire.StateLive || (g.Reach != wire.ReachUser && g.Reach != wire.ReachShared) {
		return nil
	}

	var out []wire.Overlap

	for _, m := range mirrors {
		if !m.isPath() || m.rec.Reach != record.ReachMachine || m.repoUnknown() != "" {
			continue
		}

		if g.Anchor != "" && m.rec.Parent == g.Anchor {
			continue
		}

		if m.node == g.nodeKey() && (m.id == g.Anchor || holderOfRecord(m.rec).Key == g.Holder.Key) {
			continue
		}

		if !repoid.OverlapAny(g.Entries, m.derived.Entries) {
			continue
		}

		out = append(out, wire.Overlap{
			ID: g.ID + "/" + m.id, Grant: b.foundGrant(g), Mirror: foundMirror(m, wire.StateMirrored, ""),
		})
	}

	return out
}

// overlapsForNode lists the overlaps that touch a lease of the node: its grants, and its mirrors.
func (b *Broker) overlapsForNode(node NodeKey) []wire.Overlap {
	mirrors := b.mirrorsLocked()

	var out []wire.Overlap

	for _, g := range b.sortedGrantsLocked(func(g *grant) bool { return g.State == wire.StateLive }) {
		for _, o := range b.overlapsFor(g, mirrors) {
			if g.nodeKey() == node || o.Mirror.Node == b.labelOfLocked(node) {
				out = append(out, o)
			}
		}
	}

	return out
}

func (b *Broker) labelOfLocked(k NodeKey) string {
	return b.store.Data().Nodes[k.String()].Label
}
