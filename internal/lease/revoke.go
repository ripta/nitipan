package lease

import (
	"fmt"

	"github.com/space-armada/nitipan/internal/adapter"
	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/liveness"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/state"
)

// RevokeResult describes a sub-lease that was ended.
type RevokeResult struct {
	Lease  *record.Record
	Parent *record.Record

	// Tombstone waits for the sub-lessee. It is nil when the sub-lessee was already dead, since nobody
	// can receive it.
	Tombstone *record.Tombstone
}

// Revoke ends a sub-lease at once. Only the holder of its parent lease may do it, and it needs no
// force.
//
// The sub-lessee's tombstone is published before the sub-lease is removed, so a crash between the
// two never loses the only signal the sub-lessee gets. A retry finds the tombstone already there.
// The parent did this itself, so it gets no "sub-lease ended" notice.
func (e *Engine) Revoke(h adapter.Holder, id string) (*RevokeResult, error) {
	if !state.ValidID(id) {
		return nil, usagef("%q is not a lease ID; run list to see the leases that exist", id)
	}

	c, err := e.begin(&h)
	if err != nil {
		return nil, err
	}
	defer c.close()

	kid, parent, err := c.revocable(id)
	if err != nil {
		return nil, err
	}

	res := &RevokeResult{Lease: kid.Rec, Parent: parent.Rec}

	if kid.Live.Status != liveness.Dead {
		res.Tombstone, err = c.revokeTombstone(h, kid)
		if err != nil {
			return nil, err
		}

		if err := runHook(e.Hooks.AfterRevokeTombstone); err != nil {
			return nil, err
		}
	}

	c.dropSubLeaseEnded(kid)

	if err := c.removeLease(kid); err != nil {
		return nil, err
	}

	return res, nil
}

// dropSubLeaseEnded removes a "sub-lease ended" notice that the sub-lessee's own release or break
// published and then crashed before removing the lease. The parent did this end itself, so that
// notice would tell it the sub-lessee released a lease it revoked. The notice goes before the lease,
// so a crash in between leaves a lease that the retry still ends, and never a stale notice.
func (c *call) dropSubLeaseEnded(kid *entry) {
	c.removeNotice(record.NoticeID(kid.ID, record.CauseSubLeaseEnded) + noticeSuffix)
}

// revocable finds the sub-lease and its parent, and refuses anyone but the parent's holder. The
// refusal names the parent.
func (c *call) revocable(id string) (kid, parent *entry, err error) {
	kid = c.entryByID(id)

	switch {
	case kid == nil:
		return nil, nil, failf(exitcode.ErrNoSuchLease, "no such lease %s; run list to see the leases that exist", id)
	case kid.Rec == nil:
		return nil, nil, unusableErr(kid)
	case kid.Rec.Parent == "":
		return nil, nil, failf(exitcode.ErrRefused, "lease %s is not a sub-lease, so it has no grantor to end it. Its holder "+
			"is %s", id, describeHolder(kid.Rec.Holder))
	}

	parent = c.entryByID(kid.Rec.Parent)

	switch {
	case parent == nil || parent.Rec == nil:
		return nil, nil, failf(exitcode.ErrRefused, "lease %s is a sub-lease of %s, which is gone or cannot be read, so nobody "+
			"holds the right to end it. Coordinate with its holder or ask the user", id, kid.Rec.Parent)
	case c.mineByID(parent.ID) == nil:
		return nil, nil, failf(exitcode.ErrNotYours, "lease %s is a sub-lease of %s, which is held by %s. Only that holder can "+
			"end it. Coordinate with the holder or ask the user", id, parent.ID, describeHolder(parent.Rec.Holder))
	}

	return kid, parent, nil
}

// revokeTombstone publishes the sub-lessee's tombstone, naming the parent's holder as the breaker. An
// earlier revoke that crashed may have published it already, and then it is reused.
func (c *call) revokeTombstone(h adapter.Holder, kid *entry) (*record.Tombstone, error) {
	tombs, err := c.e.loadTombstones()
	if err != nil {
		return nil, err
	}

	for _, te := range tombs {
		if te.Tomb != nil && te.Tomb.Lease == kid.ID && te.Tomb.Cause == record.CauseRevoked {
			return te.Tomb, nil
		}
	}

	t := c.newTombstone(kid, BreakRequest{Caller: adapter.Caller{Holder: &h}})
	t.Version = record.ParentVersion
	t.Cause = record.CauseRevoked

	if err := c.publishTombstone(t); err != nil {
		return nil, fmt.Errorf("revoking %s: %w", kid.ID, err)
	}

	return t, nil
}
