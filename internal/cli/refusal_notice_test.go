package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/space-armada/nitipan/internal/broker"
	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/state"
	"github.com/space-armada/nitipan/internal/wire"
)

// TestARefusedClaimNoticeNeverRingsAndArrivesOnTheNextCall proves the holder's notice about a
// coworker's refused claim reaches the holder on its next nitipan call, and that while it is pending the
// turn hooks print nothing for it. It catches a ring path that counts every pending notice, which would
// let a coworker wake the holder's agent with a free refused claim and no mail grant.
func TestARefusedClaimNoticeNeverRingsAndArrivesOnTheNextCall(t *testing.T) {
	f := newWideFixture(t)
	m := f.machine("m1")
	m.start()

	w := m.w
	repo := w.repoIn("repo")
	c := w.claude(100, "s1")
	w.installHooks(c, repo)

	res := w.run(c, "acquire", "--reach", "user", "--checkout", repo, "--scope", "svc")
	wantStatus(t, res, exitcode.OK)

	leases := f.b.Leases()
	if len(leases) != 1 {
		t.Fatalf("the broker holds %d leases, want 1", len(leases))
	}

	leaseID := leases[0].ID

	key := broker.NodeKey{MachineID: w.machine.ID, UID: os.Getuid()}
	if _, err := f.b.Enqueue(key, wire.EventClaimRefused, wire.ClaimRefused{Lease: leaseID, Coworker: "coworker-label-one-two"}); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "the refused-claim notice", func() bool {
		names, _ := state.ListNames(w.state, state.NoticesDir)

		return len(names) == 1
	})

	for _, event := range ringEvents {
		hook := w.run(c, "hook", "mail-ring", "--event", event)
		wantStatus(t, hook, exitcode.OK)

		if strings.TrimSpace(hook.Stdout) != "" {
			t.Errorf("the %s hook rang for the refused-claim notice: %q", event, hook.Stdout)
		}
	}

	list := w.run(c, "list")
	wantStatus(t, list, exitcode.OK)
	wantContains(t, "stderr", list.Stderr, "nitipan: notice:", leaseID, "coworker-label-one-two")

	if names, _ := state.ListNames(w.state, state.NoticesDir); len(names) != 0 {
		t.Errorf("the notice stayed after delivery: %v", names)
	}
}
