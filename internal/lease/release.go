package lease

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/space-armada/nitipan/internal/adapter"
	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/keys"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/state"
)

// ReleaseRequest names what to release. Exactly one of IDs, Domain, and All is set. A release never
// acts on a target the caller did not name.
type ReleaseRequest struct {
	Holder adapter.Holder
	IDs    []string
	Domain *Domain
	All    bool
}

// ReleaseResult lists the leases that were removed.
type ReleaseResult struct {
	Released []*record.Record
}

// Release removes leases the caller holds.
//
// Targets are resolved first, and nothing is removed unless every one of them resolves. A lease of a
// newer minor version is removed like any other, because removing is not a rewrite.
func (e *Engine) Release(req ReleaseRequest) (*ReleaseResult, error) {
	h := req.Holder

	c, err := e.begin(&h)
	if err != nil {
		return nil, err
	}
	defer c.close()

	var claimed keys.Domain

	if req.Domain != nil {
		d, err := e.Canon.Domain(req.Domain.Checkout, req.Domain.Scopes)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", exitcode.ErrUsage, err)
		}

		claimed = d
	}

	var targets []*entry

	switch {
	case req.All:
		targets = slices.Clone(c.mine)
	case req.Domain != nil:
		targets, err = c.resolveDomain(claimed)
	case len(req.IDs) > 0:
		targets, err = c.resolveIDs(req.IDs)
	default:
		err = usagef("release needs a lease ID, a domain, or --all; %s", c.leaseList())
	}

	if err != nil {
		return nil, err
	}

	res := &ReleaseResult{}

	for _, en := range targets {
		if err := c.l.Remove(state.LeasesDir, en.ID); err != nil {
			return res, fmt.Errorf("releasing %s: %w", en.ID, err)
		}

		res.Released = append(res.Released, en.Rec)
	}

	return res, nil
}

// resolveIDs maps lease IDs to the caller's leases. Each failure keeps its own status, and all of
// them are reported together.
func (c *call) resolveIDs(ids []string) ([]*entry, error) {
	var (
		targets []*entry
		errs    []error
	)

	for _, id := range ids {
		if !state.ValidID(id) {
			errs = append(errs, usagef("%q is not a lease ID", id))

			continue
		}

		en, err := c.resolveID(id)
		if err != nil {
			errs = append(errs, err)

			continue
		}

		if !slices.Contains(targets, en) {
			targets = append(targets, en)
		}
	}

	if len(errs) == 1 {
		return nil, errs[0]
	}

	if len(errs) > 1 {
		return nil, errors.Join(errs...)
	}

	return targets, nil
}

func (c *call) resolveID(id string) (*entry, error) {
	en := c.entryByID(id)

	switch {
	case en == nil:
		return nil, c.notFound(id)
	case en.Rec == nil:
		return nil, failf(record.ErrUnusable, "%s is an unusable record (%s); upgrade nitipan or ask the user", id,
			en.Bad.Reason)
	case c.mineByID(id) != nil:
		return en, nil
	case c.sameProcess[en]:
		return nil, failf(exitcode.ErrNotYours, "lease %s is held by another session in this same process: %s. "+
			"Coordinate with it or ask the user.", id, describeHolder(en.Rec.Holder))
	default:
		return nil, failf(exitcode.ErrNotYours, "lease %s is not yours. It is held by %s. Coordinate with the holder "+
			"or ask the user.", id, describeHolder(en.Rec.Holder))
	}
}

// notFound explains an ID with no record. A broken lease leaves a tombstone for its holder, which
// this call delivered a moment ago. Any other ID leaves no trace, so nitipan cannot tell a released
// lease from a mistyped one and never claims "already released".
func (c *call) notFound(id string) error {
	if t := c.deliveredFor(id); t != nil {
		return failf(exitcode.ErrBroken, "%s", DescribeBroken(t))
	}

	return failf(exitcode.ErrNoSuchLease, "no such lease %s; %s", id, c.leaseList())
}

// resolveDomain matches the caller's leases whose canonical key set equals the domain. It works from
// the stored keys, so the lease of a deleted worktree can still be released.
func (c *call) resolveDomain(claimed keys.Domain) ([]*entry, error) {
	var targets []*entry

	for _, en := range c.mine {
		if ks, ok := en.keysOf(); ok && sameKeySet(ks, claimed.Keys) {
			targets = append(targets, en)
		}
	}

	if len(targets) > 0 {
		return targets, nil
	}

	fp := record.PathFootprint(claimed.Keys)

	var partial []string

	for _, en := range c.mine {
		if en.Rec.Footprint.Overlaps(fp, c.e.Finder) {
			partial = append(partial, fmt.Sprintf("%s (%s)", en.ID, en.pathList()))
		}
	}

	if len(partial) > 0 {
		return nil, failf(exitcode.ErrRefused, "no lease of yours has exactly those keys. These overlap them: %s. "+
			"Release one by its ID.", strings.Join(partial, "; "))
	}

	return nil, failf(exitcode.ErrNoSuchLease, "no lease of yours has those keys; %s", c.leaseList())
}
