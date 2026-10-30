package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/space-armada/nitipan/internal/config"
	"github.com/space-armada/nitipan/internal/handoff"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/state"
	"github.com/space-armada/nitipan/internal/wire"
)

// nodeConfigured reports whether this user set up a node, which is when the broker can see their
// machine leases. The node may be down. Its config file is what says discovery is in use.
func (a *app) nodeConfigured() bool {
	home, err := a.deps.UserHome()
	if err != nil {
		return false
	}

	_, err = os.Stat(filepath.Join(config.Dir(home), config.NodeFile))

	return err == nil
}

// recordDerived writes the repo entries of a machine lease the command just took, so the broker can tell
// which repos it names. It runs where the agent's shell does, which can read the checkout when the node
// cannot. A derivation that fails is recorded as failed, which discovery reads as unknown. Nothing here
// fails the claim.
func (a *app) recordDerived(rec *record.Record) {
	if rec == nil || rec.Domain.Kind != record.KindPath || rec.Reach != record.ReachMachine || !a.nodeConfigured() {
		return
	}

	stateDir, err := a.deps.StateDir()
	if err != nil {
		return
	}

	have, err := handoff.ReadDerived(stateDir, rec.ID)
	if err == nil && have.By == handoff.DerivedByCLI && have.State == wire.DerivedOK && have.Matches(rec.Domain.Keys) {
		return
	}

	d := handoff.Derive(a.deriver(stateDir), rec.ID, handoff.DerivedByCLI, rec.Domain.Keys, a.deps.Now())

	dir, err := state.Open(stateDir, a.deps.StateOptions)
	if err != nil {
		a.warn(fmt.Errorf("recording the repo entries of lease %s: %w", rec.ID, err))

		return
	}

	l, err := dir.Lock()
	if err != nil {
		a.warn(fmt.Errorf("recording the repo entries of lease %s: %w", rec.ID, err))

		return
	}

	defer func() { _ = l.Unlock() }()

	if err := handoff.WriteDerived(l, d); err != nil {
		a.warn(err)
	}
}

// printOverlaps warns about the mirrored machine leases a granted claim overlaps. The claim stands.
func (a *app) printOverlaps(overlaps []wire.Overlap) {
	for _, o := range overlaps {
		m := o.Mirror
		who := whoName(m)

		if m.Stale {
			who += " (its node has gone quiet)"
		}

		fmt.Fprintf(a.deps.Stderr, "nitipan: warning: your %s lease %s names repo files that the machine lease %s of %s also covers. "+
			"Nothing refuses either one. Ask them before you both change the same files\n", o.Grant.Reach, o.Grant.Lease, m.Lease, who)
	}
}
