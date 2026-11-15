package broker

import (
	"net/http"
	"testing"

	"github.com/space-armada/nitipan/internal/wire"
)

// TestWhoAmIReturnsTheBoundUser proves whoami names the user a label is bound to, and names nobody for a
// label nobody bound, which the CLI prints as not bound. It catches a whoami that reads a binding record
// without checking the label is still pinned under its key, or answers with the owner's scope for an
// unbound label.
func TestWhoAmIReturnsTheBoundUser(t *testing.T) {
	h := newHarness(t, nil)

	label := h.register(alice).Label
	approveKey(h, label, alice.Key)
	h.register(bob)

	owner := h.data().Users[h.data().OwnerID]

	for n, want := range map[nodeID]string{alice: owner.UserLabel, bob: ""} {
		var who wire.WhoAmIAnswer
		if status := h.call(n, wire.PathWhoAmI, wire.WhoAmI{Version: wire.Current}, &who); status != http.StatusOK {
			t.Fatalf("whoami from %s: status %d", n.Machine, status)
		}

		if who.UserLabel != want {
			t.Errorf("whoami from %s = %q, want %q", n.Machine, who.UserLabel, want)
		}
	}
}
