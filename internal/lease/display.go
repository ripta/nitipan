package lease

import (
	"github.com/space-armada/nitipan/internal/keys"
	"github.com/space-armada/nitipan/internal/listing"
	"github.com/space-armada/nitipan/internal/liveness"
	"github.com/space-armada/nitipan/internal/record"
)

// subLease is a live sub-lease as a parent's display sees it. Keys is empty when the sub-lease's
// footprint is not made of path entries this binary understands.
type subLease struct {
	ID   string
	Keys []keys.Key
}

// pathKeys returns the keys of a footprint that lists only path entries.
func pathKeys(fp record.Footprint) ([]keys.Key, bool) {
	if fp.Kind != record.FootprintEntries {
		return nil, false
	}

	out := make([]keys.Key, len(fp.Entries))

	for i, e := range fp.Entries {
		if e.Type != record.EntryPath {
			return nil, false
		}

		out[i] = e.Key()
	}

	return out, true
}

// delegations finds the live sub-leases a lease has for display. A lease that is a sub-lease itself
// has none, since sub-leases do not nest. A dead parent has none either: its record is about to be
// cleared, and its children stand alone.
func (c *call) delegations(en *entry) []subLease {
	if en.Rec == nil || en.Rec.Parent != "" || en.Live.Status == liveness.Dead {
		return nil
	}

	var out []subLease

	for _, kid := range c.liveChildren(en.ID) {
		ks, _ := pathKeys(kid.Rec.Footprint)
		out = append(out, subLease{ID: kid.ID, Keys: ks})
	}

	return out
}

// delegated turns a lease's sub-leases into the computed list of its entry.
func delegated(subs []subLease) []listing.Delegation {
	out := make([]listing.Delegation, len(subs))

	for i, s := range subs {
		out[i] = listing.Delegation{ID: s.ID, Keys: pathsOf(s.Keys)}
	}

	return out
}

func pathsOf(ks []keys.Key) []string {
	out := make([]string, len(ks))
	for i, k := range ks {
		out[i] = k.Path
	}

	return out
}

// marks answers "who owns this path" for one lease that a filtered query matched.
//
// The lease is the owner when the query overlaps some part of its keys that no sub-lease holds. The
// part of a query and a key that overlap is the deeper of the two, so a sub-lease holds it when one
// of its keys contains that part. Containment is ADR-03's one rule for comparing keys. A part lies
// inside the sub-leases' union only when one sub-lease key contains it, since a part is a single path
// and sibling subtrees are disjoint. So testing one sub-lease key at a time is the union test.
//
// The lease is the grantor of every sub-lease whose keys the query overlaps.
func marks(query, own []keys.Key, subs []subLease, finder keys.BoundaryFinder) (owner bool, grantorOf []string) {
	for _, s := range subs {
		if anyOverlap(query, s.Keys, finder) {
			grantorOf = append(grantorOf, s.ID)
		}
	}

	if len(subs) == 0 {
		return true, grantorOf
	}

	for _, q := range query {
		for _, p := range own {
			if !keys.Overlap(q, p, finder) {
				continue
			}

			part := q
			if keys.Contains(q, p, finder) {
				part = p
			}

			if !heldBySubLease(part, subs, finder) {
				return true, grantorOf
			}
		}
	}

	return false, grantorOf
}

func anyOverlap(a, b []keys.Key, finder keys.BoundaryFinder) bool {
	for _, x := range a {
		for _, y := range b {
			if keys.Overlap(x, y, finder) {
				return true
			}
		}
	}

	return false
}

func heldBySubLease(part keys.Key, subs []subLease, finder keys.BoundaryFinder) bool {
	for _, s := range subs {
		if keys.Covered(part, s.Keys, finder) {
			return true
		}
	}

	return false
}

// annotate fills the fields that only a read of every record can compute: the delegated list of a
// parent, and for a path query the marks of each match.
//
// A lease whose footprint is not made of path entries is the owner of what it matches, because
// nothing here can say what part of it a sub-lease holds.
func (c *call) annotate(lease *listing.Lease, en *entry, query []keys.Key, haveQuery bool) {
	subs := c.delegations(en)

	if len(subs) > 0 {
		lease.Delegated = delegated(subs)
	}

	if !haveQuery {
		return
	}

	own, ok := pathKeys(en.Rec.Footprint)
	if !ok {
		subs = nil
	}

	owner, grantorOf := marks(query, own, subs, c.e.Finder)
	lease.Owner = &owner
	lease.GrantorOf = grantorOf
}

// pendingRequests lists the requests a grant has not started on. A request in the granting state is
// never granted again, and its repair finishes it, so it does not read as pending. A file that cannot
// be used is left out.
//
// A path query keeps the requests whose keys overlap it. A resource query keeps none, since a
// request names paths.
func (c *call) pendingRequests(query []keys.Key, haveQuery, resource bool) ([]listing.Request, error) {
	reqs, err := c.e.loadRequests()
	if err != nil {
		return nil, err
	}

	out := []listing.Request{}

	for _, re := range reqs {
		if re.Req == nil || re.Req.State == record.RequestGranting {
			continue
		}

		ks := re.Req.Domain.Keys

		if resource || (haveQuery && !anyOverlap(query, ks, c.e.Finder)) {
			continue
		}

		out = append(out, listing.Request{
			ID:          re.ID,
			Parent:      re.Req.Parent,
			Requester:   re.Req.Holder,
			Liveness:    listing.Liveness{Status: re.Live.Status.String(), Reason: re.Live.Reason},
			Keys:        keyPaths(ks),
			Description: re.Req.Description,
			RequestedAt: re.Req.RequestedAt,
		})
	}

	return out, nil
}
