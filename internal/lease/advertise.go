package lease

import (
	"github.com/space-armada/nitipan/internal/adapter"
	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/record"
)

// AdvertiseRequest changes what the caller's leases say about the session. A nil field was left out
// and keeps its stored value.
type AdvertiseRequest struct {
	Holder      adapter.Holder
	Name        *string
	Mode        *string
	Description *string

	// LeaseID picks the lease a description applies to. It may be empty when the caller holds one lease.
	LeaseID string
}

// AdvertiseResult lists the caller's leases as they stand after the call.
type AdvertiseResult struct {
	Leases []*record.Record
}

// Advertise rewrites the name and permission mode in every lease the caller holds, and the
// description in one. Every record is checked before the first rename, so a record of a newer
// version refuses the call without leaving a partial rewrite.
func (e *Engine) Advertise(req AdvertiseRequest) (*AdvertiseResult, error) {
	h := req.Holder

	c, err := e.begin(&h)
	if err != nil {
		return nil, err
	}
	defer c.close()

	if err := validMode(h.Adapter, req.Mode); err != nil {
		return nil, err
	}

	if len(c.mine) == 0 {
		return nil, failf(exitcode.ErrNoSuchLease, "no lease to advertise on; pass --name to acquire")
	}

	w := newWrites()
	c.syncHolder(w, holderFields{Name: req.Name, Mode: req.Mode, Session: h.SessionID, Host: c.hostSync(h)})

	if req.Description != nil {
		target, err := c.descriptionTarget(req.LeaseID)
		if err != nil {
			return nil, err
		}

		desc := *req.Description

		w.edit(target, true, func(r *record.Record) bool {
			changed := r.Description != desc
			r.Description = desc

			return changed
		})
	}

	if err := w.validate(); err != nil {
		return nil, err
	}

	if err := c.flush(w); err != nil {
		return nil, err
	}

	c.reportMoves(h, w)

	res := &AdvertiseResult{}
	for _, en := range c.mine {
		res.Leases = append(res.Leases, w.result(en))
	}

	return res, nil
}

func (c *call) descriptionTarget(id string) (*entry, error) {
	if id != "" {
		if en := c.mineByID(id); en != nil {
			return en, nil
		}

		return nil, failf(exitcode.ErrNoSuchLease, "no lease %s of yours; %s", id, c.leaseList())
	}

	if len(c.mine) == 1 {
		return c.mine[0], nil
	}

	return nil, usagef("the description belongs to one lease and you hold %d; name one with --lease. %s",
		len(c.mine), c.leaseList())
}
