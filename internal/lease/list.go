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
// It takes the lock so it can finish a crashed merge and remove the tombstones it reports. When the
// lock is busy, or the sandbox blocks it, list reads without it and warns, because records are
// published atomically. It then still shows the tombstones it cannot use. It neither delivers nor
// reaps any, since it could not remove them.
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

	switch {
	case err == nil:
		c = &call{e: e, l: l, sameProcess: map[*entry]bool{}}
		defer c.close()

		if ferr := l.FormatErr(); ferr != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("%v; tombstones are not delivered", ferr))
		}

		if err := c.load(); err != nil {
			return nil, err
		}
	case errors.Is(err, state.ErrLockBusy):
		res.Warnings = append(res.Warnings, fmt.Sprintf("%v; listing without the lock, so tombstones are not "+
			"delivered", err))
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
	}

	if req.Emit == nil {
		return nil, errors.New("listing needs an Emit function to write its output")
	}

	cfg := LoadSlotConfig(e.Dir.Path())
	if warning := cfg.Warning(); warning != "" {
		res.Warnings = append(res.Warnings, warning)
	}

	out := c.output(filter, filtered, cfg)

	canWrite := c.l != nil && c.l.FormatErr() == nil

	var remove []*tombEntry

	out.Tombstones, remove, err = c.tombstoneReport(req.Caller, canWrite)
	if err != nil {
		return nil, err
	}

	if err := req.Emit(out); err != nil {
		return nil, err
	}

	for _, te := range remove {
		c.removeTombstone(te)
	}

	return res, nil
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

	for _, en := range c.entries {
		if en.Rec == nil {
			out.Leases = append(out.Leases, listing.FromUnusable(en.file(), en.Bad))

			continue
		}

		if filtered && !en.Rec.Footprint.Overlaps(filter, c.e.Finder) {
			continue
		}

		lease := listing.FromRecord(en.Rec, en.Live)
		lease.Hint = en.Hint
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
