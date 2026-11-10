package broker

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/wire"
)

// crossUserOn turns cross-user features on in the store, as the owner's command does once its checks
// pass.
func (h *harness) crossUserOn(t *testing.T) {
	t.Helper()

	h.b.mu.Lock()
	err := h.b.commit(func(d *Data) error {
		d.CrossUserEnabled = true

		return nil
	})
	h.b.mu.Unlock()

	if err != nil {
		t.Fatal(err)
	}
}

// unboundWorld is a broker with cross-user features off. The owner has one bound label, V is a second
// user, and one more label registered but nobody approved it.
type unboundWorld struct {
	h *harness

	owner, v UserRecord

	ownerNode, unbound nodeID
	unboundLabel       string
}

func newUnboundWorld(t *testing.T) *unboundWorld {
	t.Helper()

	h := newHarness(t, nil)
	now := time.Now()

	owner, err := h.b.CreateUser("owner@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	v, err := h.b.CreateUser("v@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	h.b.mu.Lock()
	err = h.b.commit(func(d *Data) error {
		d.OwnerID = owner.ID

		return nil
	})
	h.b.mu.Unlock()

	if err != nil {
		t.Fatal(err)
	}

	w := &unboundWorld{
		h: h, owner: owner, v: v,
		ownerNode: nodeID{Machine: "machine-owner", UID: 1000, Key: "key-owner"},
		unbound:   nodeID{Machine: "machine-unbound", UID: 1001, Key: "key-unbound"},
	}

	if err := h.b.BindLabelToUser(h.register(w.ownerNode).Label, owner.ID, now); err != nil {
		t.Fatal(err)
	}

	w.unboundLabel = h.register(w.unbound).Label

	return w
}

// claim sends a claim from node n and returns the status and the decoded result.
func (w *unboundWorld) claim(n nodeID, id, actingFor string, explicit bool) (int, wire.Result, wire.Error) {
	w.h.t.Helper()

	resp := w.h.do(http.MethodPost, wire.PathClaim, n, wire.Claim{
		Version: wire.Current, ID: id, Reach: wire.ReachUser, Entries: name("svc"), Derivation: 1, Anchor: "anchor-" + id,
		Holder: holder(id), ActingFor: actingFor, ActingForExplicit: explicit,
	})

	var (
		res wire.Result
		e   wire.Error
	)

	into := any(&res)
	if resp.StatusCode != http.StatusOK {
		into = &e
	}

	if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
		w.h.t.Fatal(err)
	}

	return resp.StatusCode, res, e
}

// TestWithCrossUserOffAnUnboundLabelsLeaseConflictsWithTheOwners proves an unbound label's `user`
// lease counts in the owner's scope while cross-user features are off, whatever OS user its claim names.
// It catches a broker that passes an unbound label's unauthenticated acting_for through, which would
// put the lease in a scope of its own and let it sit beside the owner's lease on the same files.
func TestWithCrossUserOffAnUnboundLabelsLeaseConflictsWithTheOwners(t *testing.T) {
	w := newUnboundWorld(t)

	if status, res, _ := w.claim(w.ownerNode, "L-owner", w.owner.UserLabel, false); status != http.StatusOK || res.Code != wire.CodeGranted {
		t.Fatalf("the owner's claim: status %d, %+v", status, res)
	}

	status, res, _ := w.claim(w.unbound, "L-unbound", "alice", false)
	if status != http.StatusOK || res.Code != wire.CodeRefused || len(res.Others) != 1 || res.Others[0].ID != "L-owner" {
		t.Fatalf("the unbound label's claim: status %d, %+v, want a refusal naming L-owner", status, res)
	}
}

// TestAnUnboundLabelsClaimForAnotherUserIsRefused proves an explicit acting_for from a label nobody
// approved is refused, and the error names the approve step. It catches a broker that lets an unbound
// label claim in any user's scope it names, since no binding or delegation is there to check.
func TestAnUnboundLabelsClaimForAnotherUserIsRefused(t *testing.T) {
	w := newUnboundWorld(t)

	status, _, e := w.claim(w.unbound, "L1", w.v.UserLabel, true)
	if status != http.StatusForbidden || e.Code != wire.CodeForbidden || !strings.Contains(e.Message, "nitipan broker approve") {
		t.Fatalf("the unbound label's claim for V: status %d, %+v", status, e)
	}

	w.h.b.mu.Lock()
	g := w.h.b.g.grants["L1"]
	w.h.b.mu.Unlock()

	if g != nil {
		t.Fatalf("the refused claim left a grant: %+v", g)
	}
}

// TestAnUnboundLabelsLeaseNeverLandsInAnotherUsersScope proves an old-version claim from an unbound
// label that names V in acting_for still counts in the owner's scope, and stays out of V's scope once
// the label is bound. It catches a broker that stores the unbound label's value, which a later binding
// keeps, since it already reads as a user label.
func TestAnUnboundLabelsLeaseNeverLandsInAnotherUsersScope(t *testing.T) {
	w := newUnboundWorld(t)

	status, res, _ := w.claim(w.unbound, "L1", w.v.UserLabel, false)
	if status != http.StatusOK || res.Code != wire.CodeGranted || res.Lease.ActingFor != w.owner.UserLabel {
		t.Fatalf("the unbound label's claim: status %d, %+v, want a grant for the owner", status, res)
	}

	u, err := w.h.b.CreateUser("u@example.com", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	if err := w.h.b.BindLabelToUser(w.unboundLabel, u.ID, time.Now()); err != nil {
		t.Fatal(err)
	}

	w.h.b.mu.Lock()
	defer w.h.b.mu.Unlock()

	for _, g := range w.h.b.g.grants {
		if g.Node == w.unboundLabel && g.ActingFor == w.v.UserLabel {
			t.Errorf("lease %s of the newly bound label sits in V's scope", g.ID)
		}
	}
}

// leaseActingFor returns a lease's acting_for as the broker holds it in memory and as it persisted it,
// since a renewal reads the first and a restart reloads the second.
func (h *harness) leaseActingFor(t *testing.T, id string) (held, stored string) {
	t.Helper()

	h.b.mu.Lock()
	defer h.b.mu.Unlock()

	if g := h.b.g.grants[id]; g != nil {
		held = g.ActingFor
	}

	if raw, ok := h.b.store.Data().Leases[id]; ok {
		var l wire.Lease
		if err := json.Unmarshal(raw, &l); err != nil {
			t.Fatal(err)
		}

		stored = l.ActingFor
	}

	return held, stored
}

// TestBindingALabelMovesItsLeasesToTheNewlyBoundUser proves an unbound label's lease, held in the
// owner's scope while cross-user features are off, moves to the user the label is then bound to. A lease
// the label holds for a user who delegated to it keeps that user, and binding the label again to the same
// user changes nothing. It catches a binding that rewrites only an acting_for naming no known user, which
// leaves the lease in the owner's scope, since renewals carry only the node credential. It also catches a
// rewrite of the store alone, which the broker's in-memory leases then overwrite.
func TestBindingALabelMovesItsLeasesToTheNewlyBoundUser(t *testing.T) {
	w := newUnboundWorld(t)

	status, res, _ := w.claim(w.unbound, "L-before", "alice", false)
	if status != http.StatusOK || res.Code != wire.CodeGranted || res.Lease.ActingFor != w.owner.UserLabel {
		t.Fatalf("the unbound label's claim: status %d, %+v, want a grant for the owner", status, res)
	}

	coworker, err := w.h.b.CreateUser("w@example.com", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	if err := w.h.b.BindLabelToUser(w.unboundLabel, coworker.ID, time.Now()); err != nil {
		t.Fatal(err)
	}

	if held, stored := w.h.leaseActingFor(t, "L-before"); held != coworker.UserLabel || stored != coworker.UserLabel {
		t.Fatalf("L-before after binding to W: held %q, stored %q, want %q", held, stored, coworker.UserLabel)
	}

	w.h.delegate(t, w.v, w.unboundLabel)

	status = w.h.call(w.unbound, wire.PathClaim, wire.Claim{
		Version: wire.Current, ID: "L-for-v", Reach: wire.ReachUser, Entries: name("for-v"), Derivation: 1,
		Anchor: "anchor-L-for-v", Holder: holder("L-for-v"), ActingFor: w.v.UserLabel, ActingForExplicit: true,
	}, &res)
	if status != http.StatusOK || res.Code != wire.CodeGranted || res.Lease.ActingFor != w.v.UserLabel {
		t.Fatalf("the label's claim for V: status %d, %+v, want a grant for V", status, res)
	}

	if err := w.h.b.Forget(w.unboundLabel); err != nil {
		t.Fatal(err)
	}

	w.h.register(nodeID{Machine: w.unbound.Machine, UID: w.unbound.UID, Key: "key-unbound-new"})

	if err := w.h.b.BindLabelToUser(w.unboundLabel, coworker.ID, time.Now()); err != nil {
		t.Fatal(err)
	}

	want := map[string]string{"L-before": coworker.UserLabel, "L-for-v": w.v.UserLabel}
	for id, label := range want {
		if held, stored := w.h.leaseActingFor(t, id); held != label || stored != label {
			t.Errorf("%s after binding again to W: held %q, stored %q, want %q", id, held, stored, label)
		}
	}
}

// TestWithCrossUserOffBoundUsersShareOneUserScope proves two bound users' `user` leases on one name
// conflict while cross-user features are off, since the broker then runs one scope. It catches a scope
// check that splits users before the owner turns the features on.
func TestWithCrossUserOffBoundUsersShareOneUserScope(t *testing.T) {
	w := newAuthzWorld(t, false)
	w.claimAs(t, w.uNode, "LU", wire.ReachUser, name("svc"), "u", w.u.UserLabel)

	res, _ := w.claimRaw(t, w.vNode, "LV", wire.ReachUser, name("svc"), "v", w.v.UserLabel)
	if res.Code != wire.CodeRefused || len(res.Others) != 1 || res.Others[0].ID != "LU" {
		t.Fatalf("V's user claim with cross-user off: %+v, want a refusal naming LU", res)
	}
}
