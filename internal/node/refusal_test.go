package node

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/broker"
	"github.com/space-armada/nitipan/internal/handoff"
	"github.com/space-armada/nitipan/internal/keys"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/repoid"
	"github.com/space-armada/nitipan/internal/state"
	"github.com/space-armada/nitipan/internal/wire"
)

func (lr *leaseRig) refusalNotices() []*record.Notice {
	lr.t.Helper()

	var out []*record.Notice

	for _, n := range lr.notices() {
		if n.Cause == record.CauseSharedClaimRefused {
			out = append(out, n)
		}
	}

	return out
}

// TestAClaimRefusedEventWritesTheHolderOneNotice proves the node turns the broker's claim-refused
// event into one notice to the lease's holder, under the dedicated cause, naming only the lease and the
// coworker's user label. A repeated event writes no second notice, and an event for a lease with no
// sidecar here writes none. It catches a node that drops the event, that writes a notice per event, or
// that addresses it to anyone but the holder.
func TestAClaimRefusedEventWritesTheHolderOneNotice(t *testing.T) {
	lr := newLeaseRig(t, nil)

	l := grantedLease(t, mustReply(lr.claim(anchorA, "/work/a", record.LivenessProcess)))
	key := broker.NodeKey{MachineID: testMachine.ID, UID: os.Getuid()}

	for range 2 {
		if _, err := lr.b.Enqueue(key, wire.EventClaimRefused, wire.ClaimRefused{Lease: l.ID, Coworker: "coworker-label-one-two"}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := lr.b.Enqueue(key, wire.EventClaimRefused, wire.ClaimRefused{Lease: "01H00000000000000000000NONE", Coworker: "coworker-label-one-two"}); err != nil {
		t.Fatal(err)
	}

	eventually(t, "the node to ack every event", func() bool { return len(lr.b.Unacked(key)) == 0 })

	got := lr.refusalNotices()
	if len(got) != 1 {
		t.Fatalf("got %d refusal notices, want 1: %+v", len(got), got)
	}

	n := got[0]
	if n.Target != l.ID || n.ID != record.NoticeID(l.ID, record.CauseSharedClaimRefused)+"-coworker-label-one-two" ||
		handoff.HolderKey(n.Holder, n.Liveness) != handoff.HolderKey(holderEnv(record.LivenessProcess).Holder, record.LivenessProcess) {
		t.Errorf("notice %+v is not the holder's notice about %s", n, l.ID)
	}

	if !strings.Contains(n.Detail, l.ID) || !strings.Contains(n.Detail, "coworker-label-one-two") || strings.Contains(n.Detail, "/work") {
		t.Errorf("notice detail %q should name the lease and coworker only", n.Detail)
	}
}

// TestAClaimForAnotherUserGetsBrokerTooOldFromABrokerThatPredatesIt proves a node whose broker did not
// advertise explicit acting_for at registration answers a claim for another user with "broker too old",
// and never forwards it. It catches a node that forwards the claim anyway, where a broker that ignores
// the field substitutes the label's own user and the lease silently counts in the wrong scope.
func TestAClaimForAnotherUserGetsBrokerTooOldFromABrokerThatPredatesIt(t *testing.T) {
	r := newRig(t)

	var claims atomic.Int32

	f := func(w http.ResponseWriter, req *http.Request) bool {
		switch req.URL.Path {
		case wire.PathClaim:
			claims.Add(1)

			return false
		case wire.PathRegister:
		default:
			return false
		}

		rec := httptest.NewRecorder()
		r.b.Handler().ServeHTTP(rec, req)

		var reg map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &reg); err != nil {
			t.Errorf("decoding the registration answer: %v", err)
		}

		delete(reg, "acting_for_explicit")

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rec.Code)
		_ = json.NewEncoder(w).Encode(reg)

		return true
	}
	r.intercept.Store(&f)

	lr := newLeaseRigOn(t, r, nil)

	plantAnchor(t, lr.state, anchorA, "/work/a")

	body, err := json.Marshal(handoff.ClaimBody{
		Reach: wire.ReachUser, Entries: []repoid.Name{repoid.New([]string{testRoot}, "work/a", false, false)},
		Derivation: 1, Anchor: handoff.Anchor{ID: anchorA, Keys: []keys.Key{{Path: "/work/a"}}, Covered: []keys.Key{{Path: "/work/a"}}, Created: true},
		Holder: holderEnv(record.LivenessProcess), ActingFor: "another-user-label-here",
	})
	if err != nil {
		t.Fatal(err)
	}

	eventually(t, "the node to register", func() bool { return lr.run.n.Label() != "" })

	req := handoff.Request{ID: state.NewID(), Kind: handoff.KindClaim, CreatedAt: time.Now(), Body: body}
	if err := handoff.SubmitVersion(lr.state, req, handoff.ActingForSince); err != nil {
		t.Fatal(err)
	}

	var rep handoff.Reply

	eventually(t, "the node to answer the claim", func() bool {
		got, err := handoff.ReadReply(lr.state, req.ID)
		rep = got

		return err == nil
	})

	if rep.Error == nil || rep.Error.Code != handoff.CodeBrokerTooOld {
		t.Fatalf("reply = %+v, want broker too old", rep)
	}

	if n := claims.Load(); n != 0 {
		t.Errorf("the node forwarded the claim %d times", n)
	}
}
