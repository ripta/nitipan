package lease

import (
	"errors"
	"fmt"
	"io/fs"
	"slices"

	"github.com/space-armada/nitipan/internal/adapter"
	"github.com/space-armada/nitipan/internal/keys"
	"github.com/space-armada/nitipan/internal/listing"
	"github.com/space-armada/nitipan/internal/liveness"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/state"
)

// ListRequest filters the output. With no Domain or Resource it lists every lease. Otherwise it lists
// the leases that overlap the domain, by nitipan's own canonicalization and overlap rule, or the ones
// that hold the resource name.
type ListRequest struct {
	Domain *Domain

	// Resource lists the leases whose footprint overlaps the resource name. It excludes Domain.
	Resource string

	// Caller is who runs the command: the holder when an agent matched, an anonymous process
	// otherwise. Tombstones are delivered to it. A caller with no adapter holds nothing, so it has
	// none to receive.
	Caller adapter.Caller

	// Emit writes the output. List calls it before it removes any tombstone it reported, so a
	// failed removal never costs the reader its output.
	Emit func(listing.Output) error
}

// ListResult is the output with the warnings that go to stderr.
type ListResult struct {
	Warnings []string

	// LockErr is a permission failure on the lock file. The command layer turns it into a warning
	// with the sandbox entries, because list still prints what it could read.
	LockErr error
}

// List reads every lease with its holder's liveness.
//
// It takes the lock so it can finish a crashed merge, run the repairs of requests and sub-leases that
// every locked call runs, and deliver the caller's notices and tombstones. It removes what it reported.
// When the lock is busy, or the sandbox blocks it, list reads without it and warns, because records
// are published atomically. It then still shows the tombstones it cannot use. It repairs nothing and
// neither delivers nor reaps any notice or tombstone, since it could not remove them.
func (e *Engine) List(req ListRequest) (*ListResult, error) {
	res := &ListResult{}

	switch {
	case req.Domain != nil && req.Resource != "":
		return nil, usagef("list takes a domain or a resource, not both")
	case req.Domain != nil:
		d, err := e.Canon.Domain(req.Domain.Checkout, req.Domain.Scopes)
		if err != nil {
			return nil, usagef("%v", err)
		}

		return e.list(req, res, record.PathFootprint(d.Keys), true)
	case req.Resource != "":
		if err := keys.ValidateResource(req.Resource); err != nil {
			return nil, usagef("%v", err)
		}

		return e.list(req, res, record.ResourceFootprint(req.Resource), true)
	}

	return e.list(req, res, record.Footprint{}, false)
}

func (e *Engine) list(req ListRequest, res *ListResult, filter record.Footprint, filtered bool) (*ListResult, error) {
	l, err := e.Dir.Lock()

	var c *call

	// repaired is false when the repairs did not run or did not finish. Notices are judged against the
	// leases the repairs leave, so none is delivered from a half-repaired state.
	repaired := false

	switch {
	case err == nil:
		c = &call{e: e, l: l, sameProcess: map[*entry]bool{}}
		defer c.close()

		if ferr := l.FormatErr(); ferr != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("%v; tombstones and notices are not delivered", ferr))
		}

		if err := c.load(); err != nil {
			return nil, err
		}

		if l.FormatErr() == nil {
			repaired = true

			if err := c.repair(req.Caller); err != nil {
				repaired = false

				res.Warnings = append(res.Warnings, fmt.Sprintf("repairing requests and sub-leases failed: %v; notices are "+
					"not delivered", err))
			}
		}
	case errors.Is(err, state.ErrLockBusy):
		res.Warnings = append(res.Warnings, fmt.Sprintf("%v; listing without the lock, so tombstones and notices are "+
			"not delivered", err))
	case errors.Is(err, fs.ErrPermission):
		res.LockErr = err
	default:
		return nil, err
	}

	if c == nil {
		entries, err := e.load()
		if err != nil {
			return nil, err
		}

		c = &call{e: e, entries: entries}
		res.Warnings = append(res.Warnings, c.hideEndedRecalls()...)
	}

	if req.Emit == nil {
		return nil, errors.New("listing needs an Emit function to write its output")
	}

	cfg := LoadSlotConfig(e.Dir.Path())
	if warning := cfg.Warning(); warning != "" {
		res.Warnings = append(res.Warnings, warning)
	}

	out := c.output(filter, filtered, cfg)

	query := queryKeys(filter, filtered)

	out.Requests, err = c.pendingRequests(query, haveQuery(query), req.Resource != "")
	if err != nil {
		return nil, err
	}

	out.Marks, err = c.markReport(query, haveQuery(query), req.Resource != "")
	if err != nil {
		return nil, err
	}

	canWrite := c.l != nil && c.l.FormatErr() == nil

	var remove []*tombEntry

	out.Tombstones, remove, err = c.tombstoneReport(req.Caller, canWrite)
	if err != nil {
		return nil, err
	}

	var due []dueNotice

	if canWrite && repaired && req.Caller.Holder != nil {
		due = c.dueNotices(*req.Caller.Holder)
		out.Notices = noticeReport(due)
	}

	if err := req.Emit(out); err != nil {
		return nil, err
	}

	for _, te := range remove {
		c.markDeliveredBy(te, req.Caller)
		c.removeTombstone(te)
	}

	for _, d := range due {
		c.removeNotice(d.Name)
	}

	return res, nil
}

// repair runs the repairs that every locked call runs before it delivers anything, then classifies
// the caller's holder. A caller that holds nothing still repairs, as any other locked call does.
//
// A failure is the caller's to report as a warning. list is the supported way to read leases, so a
// repair that cannot write must not stop the read.
func (c *call) repair(caller adapter.Caller) error {
	if err := c.runRepairs(); err != nil {
		return err
	}

	if caller.Holder != nil {
		c.classify(*caller.Holder)
	}

	return nil
}

// noticeReport turns the delivered notices into the document's array. A dropped notice says nothing
// true any more, so it is removed and never reported.
func noticeReport(due []dueNotice) []listing.Notice {
	out := []listing.Notice{}

	for _, d := range due {
		if !d.Report {
			continue
		}

		out = append(out, listing.Notice{
			ID:      d.Notice.ID,
			Cause:   d.Notice.Cause,
			Target:  d.Notice.Target,
			Paths:   d.Notice.Paths,
			At:      d.Notice.At,
			Summary: d.Notice.Detail,
		})
	}

	return out
}

// queryKeys returns the keys of a path filter. A resource filter, or no filter, has none.
func queryKeys(filter record.Footprint, filtered bool) []keys.Key {
	if !filtered {
		return nil
	}

	ks, _ := pathKeys(filter)

	return ks
}

// output builds the document. A record this binary cannot use always appears, as an unknown entry,
// because it may overlap the filter.
//
// Each local resource lease carries its name's effective count and live holders, computed over every
// record, so a filter never changes the count. A remote lease carries neither.
func (c *call) output(filter record.Footprint, filtered bool, cfg *SlotConfig) listing.Output {
	out := listing.Output{SlotConfigPath: cfg.Path}

	shared := c.sharedNames()
	views := c.slotViews(cfg)
	recalls := c.pendingRecalls()
	query := queryKeys(filter, filtered)
	haveQuery := len(query) > 0

	for _, en := range c.entries {
		if en.Rec == nil {
			out.Leases = append(out.Leases, listing.FromUnusable(en.file(), en.Bad))

			continue
		}

		if filtered && !en.Rec.Footprint.Overlaps(filter, c.e.Finder) {
			continue
		}

		lease := listing.FromRecord(en.Rec, en.Live)
		c.annotate(&lease, en, query, haveQuery)
		lease.Hint = en.Hint

		if rc := recalls[en.ID]; rc != nil {
			lease.Recall = &listing.Recall{Parent: rc.Parent, Deadline: rc.Deadline.UTC()}
		}

		lease.Remote = en.Rec.Domain.Kind == record.KindResource && !liveness.SameMachine(c.e.Machine, en.Rec.Machine)
		lease.NameShared = shared[en.Rec.Holder.Name] && en.Rec.Holder.Name != ""

		if name, ok := en.resource(); ok && !lease.Remote {
			lease.EffectiveSlots, lease.SlotHolders = slotFields(views[name])
		}

		out.Leases = append(out.Leases, lease)
	}

	return out
}

// sharedNames finds agent names that more than one live holder reports. The name is self-reported
// and never forced unique, so list flags the clash.
func (c *call) sharedNames() map[string]bool {
	holders := map[string][]string{}

	for _, en := range c.entries {
		if en.Rec == nil || en.Rec.Holder.Name == "" || en.Live.Status == liveness.Dead {
			continue
		}

		who := en.Rec.Holder.Handle
		if who == "" {
			who = en.ID
		}

		name := en.Rec.Holder.Name
		if !slices.Contains(holders[name], who) {
			holders[name] = append(holders[name], who)
		}
	}

	shared := map[string]bool{}

	for name, who := range holders {
		if len(who) > 1 {
			shared[name] = true
		}
	}

	return shared
}

// slotFields turns a name's view into the computed fields of a lease.
func slotFields(view *slotView) (limit *int, holders []string) {
	if view == nil {
		return nil, nil
	}

	count := view.Limit

	holders = make([]string, len(view.Holders))
	for i, en := range view.Holders {
		holders[i] = en.ID
	}

	return &count, holders
}

func haveQuery(query []keys.Key) bool {
	return len(query) > 0
}

// markDeliveredBy writes the fence mark of a break that an older binary did without one, when list
// delivers its tombstone to the holder it was addressed to. The listing is already written, so a
// failure here is a warning.
func (c *call) markDeliveredBy(te *tombEntry, caller adapter.Caller) {
	if te.Tomb == nil || caller.Holder == nil || !c.e.addressedTo(*caller.Holder, te.Tomb) || c.l == nil {
		return
	}

	marks, err := c.e.loadMarks()
	if err == nil {
		err = c.markDelivered(te.Tomb, marks)
	}

	if err != nil {
		c.e.Notify.warn(err)
	}
}

// markReport lists the fence marks. A path query keeps the marks whose keys overlap it, and a
// resource query keeps none, since a mark guards paths. A mark that cannot be used is always shown.
func (c *call) markReport(query []keys.Key, filtered, resource bool) ([]listing.Mark, error) {
	marks, err := c.e.loadMarks()
	if err != nil {
		return nil, err
	}

	out := []listing.Mark{}

	for _, me := range marks {
		if me.Mark == nil {
			out = append(out, listing.Mark{File: me.Path, Summary: fmt.Sprintf("fence mark %s cannot be used by this "+
				"nitipan (%s)", me.Path, me.Bad.Reason)})

			continue
		}

		if resource || (filtered && !anyOverlap(query, me.Mark.Keys, c.e.Finder)) {
			continue
		}

		m := me.Mark
		holder, by, at := m.Holder, m.BrokenBy, m.BrokenAt

		out = append(out, listing.Mark{
			ID:        me.ID,
			Usable:    true,
			Lease:     m.Lease,
			Holder:    &holder,
			Liveness:  &listing.Liveness{Status: me.Live.Status.String(), Reason: me.Live.Reason},
			Keys:      keys.Paths(m.Keys),
			BrokenBy:  &by,
			Anonymous: m.Anonymous,
			BrokenAt:  &at,
			Summary:   fmt.Sprintf("%s (holder: %s)", DescribeBroken(m.AsTombstone()), describeHolder(m.Holder)),
		})
	}

	return out, nil
}
