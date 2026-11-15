package lease

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/space-armada/nitipan/internal/adapter"
	"github.com/space-armada/nitipan/internal/listing"
	"github.com/space-armada/nitipan/internal/liveness"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/state"
)

const reasonBadTombstoneName record.Reason = "file name is not a tombstone ID"

// Notify receives what a call tells the person or agent behind it. Both functions may be nil.
//
// Delivered runs before the tombstone is removed, so a crash between the two repeats the notice
// instead of losing it. A tombstone is the only signal a broken holder gets.
type Notify struct {
	Delivered func(*record.Tombstone)
	Warn      func(error)
}

func (n Notify) delivered(t *record.Tombstone) {
	if n.Delivered != nil {
		n.Delivered(t)
	}
}

func (n Notify) warn(err error) {
	if n.Warn != nil {
		n.Warn(err)
	}
}

// tombEntry is one file in tombstones/. Tomb is nil when the file cannot be used, and Bad says why.
type tombEntry struct {
	ID   string
	Path string
	Tomb *record.Tombstone
	Bad  *record.UnusableError
	Live liveness.Result
}

// loadTombstones reads and classifies every file in tombstones/, in file-name order.
func (e *Engine) loadTombstones() ([]*tombEntry, error) {
	files, err := e.Dir.Scan(state.TombstonesDir)
	if err != nil {
		return nil, fmt.Errorf("reading the tombstones: %w", err)
	}

	out := make([]*tombEntry, 0, len(files))

	for _, f := range files {
		te := &tombEntry{ID: f.ID, Path: filepath.Join(e.Dir.Path(), state.TombstonesDir, f.ID+".json")}

		switch t, err := record.DecodeTombstone(f.Data); {
		case f.BadName:
			te.Bad = &record.UnusableError{Reason: reasonBadTombstoneName}
		case err != nil:
			te.Bad = asUnusable(err)
		case t.ID != f.ID:
			te.Bad = &record.UnusableError{Reason: reasonNameVsBody, Version: t.Version}
		default:
			te.Tomb = t
			te.Live = e.Checker.Check(t.Envelope)
		}

		out = append(out, te)
	}

	return out, nil
}

// addressedTo reports whether the tombstone was written for this holder. A tombstone from another
// machine is never ours, whatever its PID says.
func (e *Engine) addressedTo(h adapter.Holder, t *record.Tombstone) bool {
	return liveness.SameMachine(e.Machine, t.Machine) && matchHolder(h, t.Holder, t.Liveness) == matchSame
}

// deliver reports every tombstone addressed to the caller, then removes it. A failed removal is
// reported and does not fail the call, because the tombstone is only a notice.
func (c *call) deliver(h adapter.Holder) error {
	tombs, err := c.e.loadTombstones()
	if err != nil {
		return err
	}

	for _, te := range tombs {
		if te.Tomb == nil || !c.e.addressedTo(h, te.Tomb) {
			continue
		}

		c.delivered = append(c.delivered, te.Tomb)
		c.e.Notify.delivered(te.Tomb)

		c.removeTombstone(te)
	}

	return nil
}

func (c *call) removeTombstone(te *tombEntry) {
	err := c.l.Remove(state.TombstonesDir, te.ID)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		c.e.Notify.warn(fmt.Errorf("removing the tombstone %s: %w", te.Path, err))
	}
}

// deliveredFor finds a tombstone this call delivered that names the lease.
func (c *call) deliveredFor(id string) *record.Tombstone {
	for _, t := range c.delivered {
		if t.Lease == id {
			return t
		}
	}

	return nil
}

// tombstoneReport classifies the tombstones `list` shows. Delivered ones are addressed to the
// caller. Reaped ones belong to a dead holder that allows clearing. Unusable ones are shown as
// unknown and left alone, because only a person can say whose they are.
//
// Without the lock or with writes forbidden, only the unusable ones are reported. Delivering and
// reaping both end in a removal, which that state cannot do.
//
// The second result lists the files to remove once the output has been written.
func (c *call) tombstoneReport(caller adapter.Caller, canWrite bool) ([]listing.Tombstone, []*tombEntry, error) {
	tombs, err := c.e.loadTombstones()
	if err != nil {
		return nil, nil, err
	}

	var (
		shown  []listing.Tombstone
		remove []*tombEntry
	)

	for _, te := range tombs {
		switch {
		case te.Tomb == nil:
			shown = append(shown, listing.Tombstone{ID: te.ID, File: te.Path, Action: listing.TombstoneUnknown,
				Summary: fmt.Sprintf("tombstone %s cannot be used by this nitipan (%s)", te.Path, te.Bad.Reason)})
		case !canWrite:
			continue
		case caller.Holder != nil && c.e.addressedTo(*caller.Holder, te.Tomb):
			shown = append(shown, c.tombstoneItem(te.Tomb, listing.TombstoneDelivered))
			remove = append(remove, te)
		case te.Live.Status == liveness.Dead && te.Tomb.ClearWhenDead:
			shown = append(shown, c.tombstoneItem(te.Tomb, listing.TombstoneReaped))
			remove = append(remove, te)
		}
	}

	return shown, remove, nil
}

func (c *call) tombstoneItem(t *record.Tombstone, action string) listing.Tombstone {
	holder, by, at := t.Holder, t.BrokenBy, t.BrokenAt

	return listing.Tombstone{
		ID:        t.ID,
		Action:    action,
		Lease:     t.Lease,
		Holder:    &holder,
		BrokenBy:  &by,
		Anonymous: t.Anonymous,
		BrokenAt:  &at,
		Summary:   fmt.Sprintf("%s (holder: %s)", DescribeNotice(t), describeHolder(t.Holder)),
	}
}

// DescribeNotice says what a tombstone reports. A break names the lease, the breaker, and the time.
// Any other kind carries its own sentence.
func DescribeNotice(t *record.Tombstone) string {
	if t.Kind == "" {
		return DescribeBroken(t)
	}

	return strings.TrimSuffix(t.Detail, ".")
}

// DescribeBroken says which lease was broken, by whom, and when.
func DescribeBroken(t *record.Tombstone) string {
	msg := fmt.Sprintf("lease %s", t.Lease)
	if len(t.Paths) > 0 {
		msg += fmt.Sprintf(" on %s", strings.Join(t.Paths, ", "))
	}

	return fmt.Sprintf("%s was broken by %s at %s", msg, describeBreaker(t), t.BrokenAt.Format("2006-01-02 15:04:05 MST"))
}

// describeBreaker names the breaker. A caller with no adapter is an anonymous process, never a
// session or a person, so the text says what was recorded and no more.
func describeBreaker(t *record.Tombstone) string {
	if !t.Anonymous {
		return describeHolder(t.BrokenBy)
	}

	parts := []string{}

	if t.BrokenBy.Name != "" {
		parts = append(parts, t.BrokenBy.Name)
	}

	if t.BrokenBy.PID > 0 {
		parts = append(parts, fmt.Sprintf("pid %d", t.BrokenBy.PID))
	}

	if t.BrokenBy.User != "" {
		parts = append(parts, "user "+t.BrokenBy.User)
	}

	if len(parts) == 0 {
		return "an anonymous process"
	}

	return fmt.Sprintf("an anonymous process (%s)", strings.Join(parts, ", "))
}
