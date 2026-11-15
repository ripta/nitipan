package lease

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/space-armada/nitipan/internal/adapter"
	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/keys"
	"github.com/space-armada/nitipan/internal/liveness"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/state"
)

// SubLeaseRequest asks the holder of a lease to carve a sub-lease out of it. A nil Name, Mode, or
// Description means the flag was left out.
type SubLeaseRequest struct {
	Holder      adapter.Holder
	ParentID    string
	Domain      Domain
	Name        *string
	Mode        *string
	Description *string
}

// SubLeaseRequestResult describes a request that was written.
type SubLeaseRequestResult struct {
	Request *record.Request
	Parent  *record.Record
}

// Request writes a request for part of another holder's lease.
//
// The checks are the ones `grant` would always refuse: the parent must be a live path lease that is
// not itself a sub-lease, the keys must be covered by it, and no live sub-lease may cover or overlap
// them. The requester learns the outcome through a notice.
func (e *Engine) Request(req SubLeaseRequest) (*SubLeaseRequestResult, error) {
	h := req.Holder

	if h.MissingSession() {
		return nil, ErrMissingSession
	}

	c, err := e.begin(&h)
	if err != nil {
		return nil, err
	}
	defer c.close()

	if err := validMode(h.Adapter, req.Mode); err != nil {
		return nil, err
	}

	if err := e.admit(h); err != nil {
		return nil, err
	}

	if !state.ValidID(req.ParentID) {
		return nil, usagef("%q is not a lease ID", req.ParentID)
	}

	domain, err := e.Canon.Domain(req.Domain.Checkout, req.Domain.Scopes)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", exitcode.ErrUsage, err)
	}

	if err := c.refuseMarked(h, domain.Keys); err != nil {
		return nil, err
	}

	if err := c.firstUnusable(); err != nil {
		return nil, err
	}

	if err := c.checkHandle(e.handleFor(h)); err != nil {
		return nil, err
	}

	parent, err := c.delegable(req.ParentID)
	if err != nil {
		return nil, err
	}

	if err := c.coveredByParent(parent, domain.Keys); err != nil {
		return nil, err
	}

	if err := c.noLiveChildIn(parent, record.PathFootprint(domain.Keys)); err != nil {
		return nil, err
	}

	rec := c.newRequest(req, domain)

	data, err := rec.Encode()
	if err != nil {
		return nil, fmt.Errorf("encoding the request: %w", err)
	}

	if err := c.l.Publish(state.RequestsDir, rec.ID, data); err != nil {
		return nil, fmt.Errorf("publishing the request: %w", err)
	}

	return &SubLeaseRequestResult{Request: rec, Parent: parent.Rec}, nil
}

// delegable returns the lease a request names, or the reason nothing can be carved from it. Only a
// live path lease that is not itself a sub-lease can delegate. A resource lease cannot, since a slot
// has no part to hand over.
func (c *call) delegable(id string) (*entry, error) {
	en := c.entryByID(id)

	switch {
	case en == nil:
		return nil, failf(exitcode.ErrNoSuchLease, "no such lease %s; run list to see the leases that exist", id)
	case en.Rec == nil:
		return nil, unusableErr(en)
	case c.mineByID(id) != nil:
		return nil, failf(exitcode.ErrRefused, "lease %s is yours. A request asks another session for part of its lease", id)
	case en.Rec.Domain.Kind == record.KindResource:
		return nil, failf(exitcode.ErrRefused, "lease %s is on the resource %s, and a resource lease cannot be shared in parts",
			id, en.Rec.Domain.Name)
	case en.Rec.Parent != "":
		return nil, failf(exitcode.ErrRefused, "lease %s is a sub-lease, which cannot delegate again. Coordinate with its "+
			"holder or ask the user", id)
	}

	if _, ok := en.keysOf(); !ok {
		return nil, failf(exitcode.ErrRefused, "lease %s is a record this nitipan cannot read the keys of; upgrade nitipan "+
			"or ask the user", id)
	}

	if en.Live.Status == liveness.Dead {
		return nil, failf(exitcode.ErrRefused, "lease %s is held by %s, which has died, so nobody can grant from it. "+
			"Claim the keys with acquire instead", id, describeHolder(en.Rec.Holder))
	}

	return en, nil
}

// coveredByParent refuses keys the parent does not cover.
func (c *call) coveredByParent(parent *entry, want []keys.Key) error {
	held, _ := parent.keysOf()

	var outside []string

	for _, k := range want {
		if !keys.Covered(k, held, c.e.Finder) {
			outside = append(outside, k.Path)
		}
	}

	if len(outside) == 0 {
		return nil
	}

	return failf(exitcode.ErrRefused, "lease %s covers %s, and the request names %s, which it does not cover",
		parent.ID, parent.domainList(), strings.Join(outside, ", "))
}

// noLiveChildIn refuses a footprint that a live sub-lease of the parent covers or overlaps. The error
// names the sub-lessee.
func (c *call) noLiveChildIn(parent *entry, fp record.Footprint) error {
	var blocks []string

	for _, kid := range c.liveChildren(parent.ID) {
		if kid.Rec.Footprint.Overlaps(fp, c.e.Finder) {
			blocks = append(blocks, c.heldBy(kid))
		}
	}

	if len(blocks) == 0 {
		return nil
	}

	return failf(exitcode.ErrRefused, "the keys overlap %s.\n  %s\nCoordinate with the holder or ask the user.",
		Plural(len(blocks), "a sub-lease", "sub-leases"), strings.Join(blocks, "\n  "))
}

// children returns the usable records that name the lease as their parent, in lease ID order.
func (c *call) children(id string) []*entry {
	var out []*entry

	for _, en := range c.entries {
		if en.Rec != nil && en.Rec.Parent == id && !en.removed {
			out = append(out, en)
		}
	}

	return out
}

// liveChildren is children without those whose holder is dead.
func (c *call) liveChildren(id string) []*entry {
	var out []*entry

	for _, en := range c.children(id) {
		if en.Live.Status != liveness.Dead {
			out = append(out, en)
		}
	}

	return out
}

func (c *call) newRequest(req SubLeaseRequest, domain keys.Domain) *record.Request {
	h := req.Holder
	name, mode := c.effective(holderFields{Name: req.Name, Mode: req.Mode})

	holder := h.Record()
	holder.Handle = c.e.handleFor(h)
	holder.Name = name

	desc := ""
	if req.Description != nil {
		desc = *req.Description
	}

	return &record.Request{
		Envelope: record.Envelope{
			Version:       record.ParentVersion,
			ID:            c.e.NewID(),
			Footprint:     record.NoneFootprint(),
			ClearWhenDead: true,
			Liveness:      h.Adapter.Liveness,
			Machine:       c.e.Machine,
			Holder:        holder,
		},
		Parent: req.ParentID,
		Domain: record.Domain{
			Kind:     record.KindPath,
			Checkout: req.Domain.Checkout,
			Scopes:   req.Domain.Scopes,
			Keys:     domain.Keys,
		},
		ActingFor:      h.User,
		PermissionMode: mode,
		Description:    desc,
		RequestedAt:    c.e.Now().UTC(),
	}
}

// GrantResult describes a sub-lease that was granted.
type GrantResult struct {
	Lease *record.Record

	// Cleared lists the dead holders' leases the grant removed.
	Cleared []string
}

// Grant turns a request into a sub-lease. Only the holder of the parent may grant.
//
// The order of the writes is the crash contract. The request is marked granting first, so no other
// call can grant it again and its lease ID is never reused. The sub-lease is published next. The
// notice that announces it comes after the fact, so a positive notice never precedes what it
// announces. The request is removed last. Any later locked call finishes an interrupted grant.
func (e *Engine) Grant(h adapter.Holder, requestID string) (*GrantResult, error) {
	c, err := e.begin(&h)
	if err != nil {
		return nil, err
	}
	defer c.close()

	req, err := c.requestByID(requestID)
	if err != nil {
		return nil, err
	}

	parent, err := c.grantable(h, req)
	if err != nil {
		return nil, err
	}

	fp := record.PathFootprint(req.Domain.Keys)

	cleared, err := c.noOtherLeaseIn(parent, fp)
	if err != nil {
		return nil, err
	}

	rec := c.subLease(req)

	data, err := rec.Encode()
	if err != nil {
		return nil, fmt.Errorf("encoding the sub-lease: %w", err)
	}

	marked := *req
	marked.State = record.RequestGranting

	markedData, err := marked.Encode()
	if err != nil {
		return nil, fmt.Errorf("marking the request %s: %w", req.ID, err)
	}

	if err := c.l.Replace(state.RequestsDir, req.ID, markedData); err != nil {
		return nil, fmt.Errorf("marking the request %s as granting: %w", req.ID, err)
	}

	if err := runHook(e.Hooks.AfterGrantMark); err != nil {
		return nil, err
	}

	removed := make([]string, 0, len(cleared))

	for _, en := range cleared {
		if err := c.clearDead(en); err != nil {
			return nil, fmt.Errorf("clearing the lease of a dead holder: %w", err)
		}

		removed = append(removed, en.ID)
	}

	if err := c.l.Publish(state.LeasesDir, rec.ID, data); err != nil {
		return nil, fmt.Errorf("publishing the sub-lease: %w", err)
	}

	if err := runHook(e.Hooks.AfterGrantPublish); err != nil {
		return nil, err
	}

	if err := c.writeNotice(c.grantedNotice(req, parent)); err != nil {
		return nil, err
	}

	if err := runHook(e.Hooks.AfterGrantNotice); err != nil {
		return nil, err
	}

	if err := c.removeRequest(req.ID); err != nil {
		return nil, err
	}

	return &GrantResult{Lease: rec, Cleared: removed}, nil
}

func runHook(hook func() error) error {
	if hook == nil {
		return nil
	}

	return hook()
}

// requestByID reads a pending request. A request that cannot be used is reported by file.
func (c *call) requestByID(id string) (*record.Request, error) {
	if !state.ValidID(id) {
		return nil, usagef("%q is not a request ID", id)
	}

	data, err := c.e.Dir.Read(state.RequestsDir, id)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, failf(exitcode.ErrNoSuchLease, "no such request %s. It may have been granted, declined, or reaped; "+
			"run list to see the leases that exist", id)
	}

	if err != nil {
		return nil, err
	}

	req, err := record.DecodeRequest(data)
	if err != nil || req.ID != id {
		return nil, failf(record.ErrUnusable, "request %s cannot be used by this nitipan; upgrade nitipan or ask the user", id)
	}

	if req.State == record.RequestGranting {
		return nil, failf(exitcode.ErrRefused, "request %s is being granted. The next nitipan call finishes it", id)
	}

	return req, nil
}

// grantable rechecks, under the lock, what made the request grantable when it was written. The
// caller must still hold the parent, the parent must still cover the keys, the requester must be
// alive, and the requester must not be the parent's own holder, since subagents share their parent's
// holder and would otherwise grant to themselves.
func (c *call) grantable(h adapter.Holder, req *record.Request) (*entry, error) {
	parent := c.entryByID(req.Parent)

	switch {
	case parent == nil || parent.Rec == nil:
		return nil, failf(exitcode.ErrNoSuchLease, "request %s asks for part of lease %s, which no longer exists", req.ID,
			req.Parent)
	case c.mineByID(req.Parent) == nil:
		return nil, failf(exitcode.ErrNotYours, "request %s asks for part of lease %s, which is held by %s. Only that "+
			"holder can grant it", req.ID, req.Parent, describeHolder(parent.Rec.Holder))
	case parent.Rec.Parent != "":
		return nil, failf(exitcode.ErrRefused, "lease %s is a sub-lease, which cannot delegate again", req.Parent)
	case c.e.isHolder(h, req.Envelope):
		return nil, failf(exitcode.ErrRefused, "request %s was made by your own holder. Subagents share their parent's "+
			"holder, so a grant to yourself would delegate nothing", req.ID)
	case c.e.Checker.Check(req.Envelope).Status == liveness.Dead:
		return nil, failf(exitcode.ErrRefused, "the requester of %s, %s, has died. The request is reaped by the next "+
			"nitipan call", req.ID, describeHolder(req.Holder))
	}

	if err := c.coveredByParent(parent, req.Domain.Keys); err != nil {
		return nil, err
	}

	if err := c.requesterHandleFree(req); err != nil {
		return nil, err
	}

	return parent, nil
}

// requesterHandleFree refuses a grant when a different live holder now uses the requester's handle.
// Another holder may have claimed it since the request was written, and a sender who learned the
// handle would then reach the wrong session.
func (c *call) requesterHandleFree(req *record.Request) error {
	if req.Holder.Handle == "" {
		return nil
	}

	for _, en := range c.entries {
		if en.Rec == nil || en.Rec.Holder.Handle != req.Holder.Handle || !en.Live.TreatAsAlive() {
			continue
		}

		if sameIdentity(en.Rec.Holder, req.Holder, req.Liveness) {
			continue
		}

		return fmt.Errorf("%w: handle %q is already used by %s; ask the user", ErrHandleCollision, req.Holder.Handle,
			describeHolder(en.Rec.Holder))
	}

	return nil
}

// sameIdentity compares the identity fields a record stores, the way the holder match rule does. A
// session-kind holder is its session, because its host process can change.
func sameIdentity(a, b record.Holder, kind string) bool {
	if a.Agent != b.Agent {
		return false
	}

	if kind == record.LivenessSession {
		return a.SessionID != "" && a.SessionID == b.SessionID
	}

	return a.PID == b.PID && a.Start.Equal(b.Start) && a.SessionID == b.SessionID
}

// noOtherLeaseIn refuses a grant when any lease other than the parent overlaps the keys. That
// includes the parent's other sub-leases and the caller's own other leases. A dead holder's lease that
// allows clearing does not block, and is returned to be removed once the grant goes through.
func (c *call) noOtherLeaseIn(parent *entry, fp record.Footprint) (cleared []*entry, err error) {
	if err := c.firstUnusable(); err != nil {
		return nil, err
	}

	var blocks []string

	for _, en := range c.entries {
		if en == parent || !en.Rec.Footprint.Overlaps(fp, c.e.Finder) {
			continue
		}

		switch {
		case en.Live.Status == liveness.Dead && en.Rec.Clearable():
			cleared = append(cleared, en)
		default:
			blocks = append(blocks, c.heldBy(en))
		}
	}

	if len(blocks) > 0 {
		return nil, failf(exitcode.ErrRefused, "the keys also overlap %s.\n  %s\nCoordinate with the holder or ask the user.",
			Plural(len(blocks), "a lease", "leases"), strings.Join(blocks, "\n  "))
	}

	return cleared, nil
}

// subLease builds the record a grant publishes. It takes the request's ID as its lease ID, and the
// requester's exact identity, description, name, and permission mode.
func (c *call) subLease(req *record.Request) *record.Record {
	return &record.Record{
		Envelope: record.Envelope{
			Version:       record.ParentVersion,
			ID:            req.ID,
			Footprint:     record.PathFootprint(req.Domain.Keys),
			ClearWhenDead: true,
			Liveness:      req.Liveness,
			Machine:       req.Machine,
			Holder:        req.Holder,
		},
		Reach:          record.ReachMachine,
		Domain:         req.Domain,
		ActingFor:      req.ActingFor,
		PermissionMode: req.PermissionMode,
		Description:    req.Description,
		AcquiredAt:     c.e.Now().UTC(),
		Parent:         req.Parent,
		BodyKnown:      true,
	}
}

// DeclineResult describes a request that was removed.
type DeclineResult struct {
	Request *record.Request

	// ByRequester is true when the requester withdrew its own request.
	ByRequester bool
}

// Decline removes a request. The holder of the parent refuses it, and the requester withdraws it.
// A refusal leaves the requester a notice. A withdrawal leaves none, since the requester is here.
func (e *Engine) Decline(h adapter.Holder, requestID string) (*DeclineResult, error) {
	c, err := e.begin(&h)
	if err != nil {
		return nil, err
	}
	defer c.close()

	req, err := c.requestByID(requestID)
	if err != nil {
		return nil, err
	}

	switch {
	case c.e.isHolder(h, req.Envelope):
		if err := c.removeRequest(req.ID); err != nil {
			return nil, err
		}

		return &DeclineResult{Request: req, ByRequester: true}, nil
	case c.mineByID(req.Parent) != nil:
		why := "the holder of " + req.Parent + " refused it"
		if err := c.declineRequest(req, why); err != nil {
			return nil, err
		}

		return &DeclineResult{Request: req}, nil
	}

	return nil, failf(exitcode.ErrNotYours, "request %s is between %s and the holder of lease %s. Only they can decline it",
		req.ID, describeHolder(req.Holder), req.Parent)
}
