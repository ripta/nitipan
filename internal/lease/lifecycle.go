package lease

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/space-armada/nitipan/internal/liveness"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/state"
)

// repairChildren finishes what a parent's death, or an older binary's removal of a parent, left
// behind. It runs under the lock with the other repairs, before notices are delivered.
//
// A sub-lease whose parent is dead or dangling becomes an ordinary lease. Its sub-lessee holds its
// keys either way. A sub-lease whose own holder is dead is left for the clearing that removes it.
func (c *call) repairChildren() error {
	for _, kid := range c.entries {
		if kid.Rec == nil || kid.Rec.Parent == "" || kid.Live.Status == liveness.Dead {
			continue
		}

		how, ended := c.parentGone(kid)
		if !ended {
			continue
		}

		announced, err := c.announceParentEnded(kid, how)
		if err != nil {
			return err
		}

		if announced {
			if err := c.standAlone(kid); err != nil {
				return err
			}
		}
	}

	return nil
}

// parentGone says how a sub-lease's parent ended, when it did. A parent record that cannot be used
// has not ended as far as nitipan can tell.
func (c *call) parentGone(kid *entry) (string, bool) {
	parent := c.entryByID(kid.Rec.Parent)

	switch {
	case parent == nil:
		return "is gone, and nitipan cannot tell whether its holder released it, someone broke it, or an older " +
			"nitipan removed it", true
	case parent.Rec != nil && parent.Live.Status == liveness.Dead:
		return "ended because its holder died", true
	}

	return "", false
}

// announceParentEnded tells a sub-lessee that its parent ended. It reports whether the caller may go
// on to rewrite the sub-lease. A binary that must not rewrite the record writes no notice, so the
// notice is written once, by the call that repairs the record.
//
// The notice is published before the rewrite it announces, and its ID is derived from the sub-lease
// and the cause. A crash between the two repeats the call, which finds the notice already there.
func (c *call) announceParentEnded(kid *entry, how string) (bool, error) {
	if kid.Rec.Rewritable() != nil {
		return false, nil
	}

	paths := keyPaths(kid.Rec.Domain.Keys)

	n := c.e.noticeFor(kid.Rec.Envelope, kid.ID, record.CauseParentEnded, fmt.Sprintf("your sub-lease %s on %s lost its "+
		"parent: lease %s %s. It is now an ordinary lease of yours with the same keys", kid.ID, strings.Join(paths, ", "),
		kid.Rec.Parent, how), paths)

	if err := c.writeLossNotice(n); err != nil {
		return false, err
	}

	return true, runHook(c.e.Hooks.AfterLossNotice)
}

// standAlone rewrites a sub-lease without its parent, through rename(2). The version drops to the
// lowest minor the remaining fields need, so a V1 binary can rewrite the record again.
func (c *call) standAlone(kid *entry) error {
	if kid.removed {
		return nil
	}

	rec := *kid.Rec
	rec.Parent = ""
	rec.Version = rec.MinimalVersion()

	data, err := rec.Encode()
	if err != nil {
		return fmt.Errorf("encoding %s: %w", kid.file(), err)
	}

	if err := c.l.Replace(state.LeasesDir, kid.ID, data); err != nil {
		return fmt.Errorf("rewriting %s without its parent: %w", kid.file(), err)
	}

	kid.Rec = &rec

	return nil
}

// beforeRemoval does what the removal of a lease owes before it happens, so that every loss notice is
// published first. A sub-lease owes its parent a "sub-lease ended" notice. A parent owes each live
// sub-lease a "parent ended" notice. The sub-leases it returns are rewritten by afterRemoval.
//
// parentHow completes "lease P ..." for the sub-lessees. childHow completes "it ..." for the parent.
func (c *call) beforeRemoval(en *entry, parentHow, childHow string) ([]*entry, error) {
	if err := c.returnToParent(en, childHow); err != nil {
		return nil, err
	}

	var kids []*entry

	for _, kid := range c.liveChildren(en.ID) {
		announced, err := c.announceParentEnded(kid, parentHow)
		if err != nil {
			return nil, err
		}

		if announced {
			kids = append(kids, kid)
		}
	}

	return kids, nil
}

// afterRemoval rewrites the sub-leases of a lease that was just removed, and returns every live
// sub-lease that now stands alone. A sub-lease this binary must not rewrite stands alone too, and its
// repair waits for a newer binary.
func (c *call) afterRemoval(en *entry, announced []*entry) ([]*record.Record, error) {
	standing := c.liveChildren(en.ID)
	out := make([]*record.Record, 0, len(standing))

	for _, kid := range standing {
		if containsEntry(announced, kid) {
			if err := c.standAlone(kid); err != nil {
				return out, err
			}
		}

		out = append(out, kid.Rec)
	}

	return out, nil
}

func containsEntry(list []*entry, en *entry) bool {
	for _, x := range list {
		if x == en {
			return true
		}
	}

	return false
}

// returnToParent tells a sub-lease's parent that the sub-lease is ending. The keys go back to the
// parent without a rewrite, since its footprint never stopped covering them. A parent that is gone
// or dead has nobody to tell.
func (c *call) returnToParent(kid *entry, how string) error {
	parent := c.liveParent(kid)
	if parent == nil {
		return nil
	}

	paths := keyPaths(kid.Rec.Domain.Keys)

	n := c.e.noticeFor(parent.Rec.Envelope, kid.ID, record.CauseSubLeaseEnded, fmt.Sprintf("your sub-lease %s on %s ended: %s. "+
		"Its keys are yours again, and your lease %s is unchanged", kid.ID, strings.Join(paths, ", "), how, parent.ID), paths)

	if err := c.writeLossNotice(n); err != nil {
		return err
	}

	return runHook(c.e.Hooks.AfterLossNotice)
}

// liveParent returns the usable record of a sub-lease's parent unless its holder is dead. It is nil
// for a lease that is no sub-lease, and for a parent that is gone.
func (c *call) liveParent(kid *entry) *entry {
	if kid.Rec == nil || kid.Rec.Parent == "" {
		return nil
	}

	parent := c.entryByID(kid.Rec.Parent)
	if parent == nil || parent.Rec == nil || parent.Live.Status == liveness.Dead {
		return nil
	}

	return parent
}

// clearDead removes the lease of a dead holder that a claim displaced. A sub-lease that goes this
// way returns its keys to a live parent with a notice.
func (c *call) clearDead(en *entry) error {
	if err := c.returnToParent(en, "its holder died, and nitipan cleared it"); err != nil {
		return err
	}

	return c.removeLease(en)
}

// removeLease removes a lease file and marks the entry, so the rest of the call no longer sees it.
// The mark is what keeps a later rewrite from recreating a record this call removed.
func (c *call) removeLease(en *entry) error {
	err := c.l.Remove(state.LeasesDir, en.ID)
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		en.removed = true
	}

	return err
}
