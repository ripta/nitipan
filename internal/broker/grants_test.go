package broker

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/repoid"
	"github.com/space-armada/nitipan/internal/wire"
)

const (
	rootA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	rootB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

var (
	laptop = nodeID{Machine: "m-laptop", UID: 501, Key: "key-laptop"}
	studio = nodeID{Machine: "m-studio", UID: 501, Key: "key-studio"}
)

func name(path string, roots ...string) []repoid.Name {
	if len(roots) == 0 {
		roots = []string{rootA}
	}

	return []repoid.Name{repoid.New(roots, path, false, false)}
}

func holder(key string) wire.Holder {
	return wire.Holder{Key: key, Agent: "claude", SessionID: "s-" + key, Handle: "h-" + key}
}

func (h *harness) call(n nodeID, path string, body, out any) int {
	h.t.Helper()

	resp := h.do(http.MethodPost, path, n, body)

	if out != nil && resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			h.t.Fatalf("decoding the answer from %s: %v", path, err)
		}
	}

	return resp.StatusCode
}

func (h *harness) claim(n nodeID, id, reach string, entries []repoid.Name, hk string) wire.Result {
	h.t.Helper()

	var res wire.Result

	status := h.call(n, wire.PathClaim, wire.Claim{
		Version: wire.Current, ID: id, Reach: reach, Entries: entries, Derivation: 1, Anchor: "anchor-" + id,
		Holder: holder(hk), ActingFor: "someone",
	}, &res)
	if status != http.StatusOK {
		h.t.Fatalf("claim %s: status %d", id, status)
	}

	return res
}

func (h *harness) renew(n nodeID, ids ...string) []wire.RenewResult {
	h.t.Helper()

	items := make([]wire.RenewItem, len(ids))
	for i, id := range ids {
		items[i] = wire.RenewItem{ID: id, Liveness: wire.LiveAlive}
	}

	var out wire.Renewed
	if status := h.call(n, wire.PathRenew, wire.Renew{Version: wire.Current, Items: items}, &out); status != http.StatusOK {
		h.t.Fatalf("renew: status %d", status)
	}

	return out.Results
}

func (h *harness) twoNodes() {
	h.t.Helper()

	h.register(laptop)
	h.register(studio)
}

func TestUserClaimsFromTwoMachinesConflict(t *testing.T) {
	h := newHarness(t, nil)
	h.twoNodes()

	first := h.claim(laptop, "L1", wire.ReachUser, name("svc", rootA, rootB), "a")
	if first.Code != wire.CodeGranted || first.Lease.Revision != 1 || first.Lease.Token == 0 || first.Lease.TermMillis != testTerm.Milliseconds() {
		t.Fatalf("first claim = %+v", first)
	}

	if first.Lease.ExpiresMillis == 0 {
		t.Fatal("a grant carries its absolute expiry")
	}

	second := h.claim(studio, "L2", wire.ReachUser, name("svc/api", rootB), "b")
	if second.Code != wire.CodeRefused || len(second.Others) != 1 || second.Others[0].ID != "L1" {
		t.Fatalf("overlapping root sets must conflict: %+v", second)
	}

	if third := h.claim(studio, "L3", wire.ReachUser, name("other", rootB), "b"); third.Code != wire.CodeGranted {
		t.Fatalf("a disjoint path must not conflict: %+v", third)
	}
}

func TestUserAndSharedLeasesConflict(t *testing.T) {
	h := newHarness(t, nil)
	h.twoNodes()

	h.claim(laptop, "L1", wire.ReachUser, name("svc"), "a")

	if res := h.claim(studio, "L2", wire.ReachShared, name("svc"), "b"); res.Code != wire.CodeRefused {
		t.Fatalf("shared against user: %+v", res)
	}
}

func TestClaimRetryNeverGrantsTwice(t *testing.T) {
	h := newHarness(t, nil)
	h.twoNodes()

	a := h.claim(laptop, "L1", wire.ReachUser, name("svc"), "a")
	b := h.claim(laptop, "L1", wire.ReachUser, name("svc"), "a")

	if a.Lease.Token != b.Lease.Token || h.store.Data().Counter != a.Lease.Token || len(h.b.Leases()) != 1 {
		t.Fatalf("a retry must replay the outcome: %+v vs %+v, counter %d", a, b, h.store.Data().Counter)
	}
}

func TestRefusalsOfBadClaims(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.DerivationFloor = 2 })
	h.register(laptop)

	body := wire.Claim{Version: wire.Current, ID: "L1", Reach: wire.ReachUser, Entries: name("svc"), Derivation: 1, Anchor: "a", Holder: holder("a")}

	if status := h.call(laptop, wire.PathClaim, body, nil); status != http.StatusBadRequest {
		t.Fatalf("a derivation below the floor: status %d", status)
	}

	body.Derivation, body.Anchor, body.ID = 2, "", "L2"

	if status := h.call(laptop, wire.PathClaim, body, nil); status != http.StatusBadRequest {
		t.Fatalf("a claim with no anchor ID: status %d", status)
	}

	body.Anchor, body.Reach, body.ID = "a", "machine", "L3"

	if status := h.call(laptop, wire.PathClaim, body, nil); status != http.StatusBadRequest {
		t.Fatalf("machine reach: status %d", status)
	}
}

func TestLapseDuringSleepIsRevivedWithTheSameGrant(t *testing.T) {
	h := newHarness(t, nil)
	h.twoNodes()
	h.snapshot(laptop, rec("anchor-L1"))

	granted := h.claim(laptop, "L1", wire.ReachUser, name("svc"), "a")

	h.clock.Sleep(testTerm + time.Minute)
	h.b.Wake()
	h.clock.Advance(testTerm)

	h.b.Sweep()

	view := h.b.Leases()
	if len(view) != 1 || view[0].State != wire.StateLapsed || view[0].Revision != granted.Lease.Revision+1 {
		t.Fatalf("view after the sleep = %+v", view)
	}

	got := h.renew(laptop, "L1")
	if got[0].Code != wire.CodeRevived || got[0].Lease.Token != granted.Lease.Token ||
		got[0].Lease.Revision != granted.Lease.Revision+2 || got[0].LapsedMillis < 0 {
		t.Fatalf("revival = %+v", got[0])
	}
}

func TestLongLapseIsStillRevived(t *testing.T) {
	h := newHarness(t, nil)
	h.twoNodes()
	h.snapshot(laptop, rec("anchor-L1"))
	h.claim(laptop, "L1", wire.ReachUser, name("svc"), "a")

	h.clock.Advance(13 * testTerm)
	h.b.Wake()
	h.clock.Advance(testTerm)
	h.b.Sweep()

	if got := h.renew(laptop, "L1"); got[0].Code != wire.CodeRevived {
		t.Fatalf("a lease lapsed for 14 terms must revive when nobody claimed: %+v", got[0])
	}
}

func TestOverlappingGrantTakesALapsedLease(t *testing.T) {
	h := newHarness(t, nil)
	h.twoNodes()
	h.snapshot(laptop, rec("anchor-L1"))
	h.claim(laptop, "L1", wire.ReachUser, name("svc"), "a")

	h.clock.Sleep(2 * testTerm)
	h.clock.Advance(testTerm)
	h.b.Sweep()

	if res := h.claim(studio, "L2", wire.ReachUser, name("svc"), "b"); res.Code != wire.CodeGranted {
		t.Fatalf("a lapsed lease does not block: %+v", res)
	}

	got := h.renew(laptop, "L1")
	if got[0].Code != wire.CodeTaken || got[0].TakenBy == "" {
		t.Fatalf("renewal after a grant = %+v", got[0])
	}

	if len(h.b.Facts()) != 1 {
		t.Fatalf("the taken marker is an unacked end fact: %+v", h.b.Facts())
	}

	var ack wire.SnapshotAck

	status := h.call(laptop, wire.PathSnapshot, wire.Snapshot{Version: wire.Current, Leases: []wire.MirroredLease{rec("anchor-L1")}}, &ack)
	if status != http.StatusOK || len(ack.Leases) != 1 || ack.Leases[0].State != wire.StateTaken {
		t.Fatalf("the snapshot answer carries the fact: status %d, %+v", status, ack)
	}

	h.snapshotAcked(laptop, []string{"L1"}, rec("anchor-L1"))

	if len(h.b.Facts()) != 0 {
		t.Fatalf("an ack drops the fact: %+v", h.b.Facts())
	}
}

func (h *harness) snapshotAcked(n nodeID, acked []string, leases ...wire.MirroredLease) {
	h.t.Helper()

	h.call(n, wire.PathSnapshot, wire.Snapshot{Version: wire.Current, Leases: leases, Acked: acked}, nil)
}

func TestRenewOfUnknownLeaseIsNotFound(t *testing.T) {
	h := newHarness(t, nil)
	h.twoNodes()

	if got := h.renew(laptop, "nope"); got[0].Code != wire.CodeNotFound {
		t.Fatalf("renew = %+v", got[0])
	}

	h.claim(laptop, "L1", wire.ReachUser, name("svc"), "a")

	if got := h.renew(studio, "L1"); got[0].Code != wire.CodeNotFound {
		t.Fatalf("another node's lease is not found: %+v", got[0])
	}
}

func TestNothingExpiresForOneTermAfterStartOrWake(t *testing.T) {
	h := newHarness(t, nil)
	h.twoNodes()
	h.claim(laptop, "L1", wire.ReachUser, name("svc"), "a")

	h.clock.Advance(testTerm - time.Second)
	h.b.Wake()
	h.clock.Advance(2 * time.Second)
	h.b.Sweep()

	if v := h.b.Leases(); v[0].State != wire.StateLive {
		t.Fatalf("a wake starts a new grace: %+v", v[0])
	}

	h.clock.Advance(testTerm)
	h.b.Sweep()

	if v := h.b.Leases(); v[0].State != wire.StateLapsed {
		t.Fatalf("a lease expires once the grace is over: %+v", v[0])
	}
}

func TestRestartKeepsLeasesAndOutcomesAndExpiresNothingForOneTerm(t *testing.T) {
	h := newHarness(t, nil)
	h.twoNodes()

	first := h.claim(laptop, "L1", wire.ReachUser, name("svc"), "a")

	if err := h.store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := OpenStore(h.path, StoreOptions{Volume: localVolume{}})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = store.Close() })

	h.clock.Advance(10 * testTerm)

	b := New(Config{Store: store, Term: testTerm, Clock: h.clock, Version: "test"})
	b.Sweep()

	v := b.Leases()
	if len(v) != 1 || v[0].State != wire.StateLive || v[0].Token != first.Lease.Token {
		t.Fatalf("after a restart = %+v", v)
	}

	h.clock.Advance(testTerm / 2)
	b.Sweep()

	if b.Leases()[0].State != wire.StateLive {
		t.Fatal("nothing expires for one term after start")
	}

	if _, ok := store.Data().Outcomes["L1"]; !ok || store.Data().Counter != first.Lease.Token {
		t.Fatal("the request outcome and the counter survive")
	}
}

func TestBreakRefusesWithoutForceAndForceLeavesAFact(t *testing.T) {
	h := newHarness(t, nil)
	h.twoNodes()
	h.claim(laptop, "L1", wire.ReachUser, name("svc"), "a")

	var res wire.Result

	h.call(studio, wire.PathBreak, wire.Break{Version: wire.Current, RequestID: "B1", ID: "L1", By: holder("b")}, &res)

	if res.Code != wire.CodeRefused || len(h.b.Leases()) != 1 {
		t.Fatalf("break without --force: %+v", res)
	}

	h.call(studio, wire.PathBreak, wire.Break{Version: wire.Current, RequestID: "B2", ID: "L1", Force: true, By: holder("b")}, &res)

	if res.Code != wire.CodeBroken || len(h.b.Leases()) != 0 {
		t.Fatalf("break --force: %+v", res)
	}

	facts := h.b.Facts()
	if len(facts) != 1 || facts[0].State != wire.StateEnded || facts[0].EndedBy == "" {
		t.Fatalf("facts = %+v", facts)
	}

	if got := h.renew(laptop, "L1"); got[0].Code != wire.CodeEnded || got[0].EndedBy == "" {
		t.Fatalf("a broken lease is never revived: %+v", got[0])
	}
}

func TestBreakFromAnotherNodeLeavesAnUnackedEndFactForItsOwner(t *testing.T) {
	h := newHarness(t, nil)
	h.twoNodes()
	h.claim(laptop, "L1", wire.ReachUser, name("svc"), "a")

	var res wire.Result

	h.call(studio, wire.PathBreak, wire.Break{Version: wire.Current, RequestID: "B1", ID: "L1", Force: true, By: holder("b")}, &res)

	if res.Code != wire.CodeBroken || len(h.b.Facts()) != 1 {
		t.Fatalf("break --force: %+v", res)
	}

	h.call(studio, wire.PathBreak, wire.Break{Version: wire.Current, RequestID: "B2", ID: "L1", By: holder("c")}, &res)

	if res.Code != wire.CodeEnded || res.Lease == nil || res.Lease.EndedBy == "" {
		t.Fatalf("a second break answers = %+v, want the lease already ended", res)
	}

	if len(h.b.Facts()) != 1 {
		t.Fatalf("another node's break dropped the unacked fact: %+v", h.b.Facts())
	}

	h.call(laptop, wire.PathBreak, wire.Break{Version: wire.Current, RequestID: "B3", ID: "L1", By: holder("a")}, &res)

	if res.Code != wire.CodeBroken || len(h.b.Facts()) != 0 {
		t.Fatalf("the owner's own break clears the fact: %+v, facts %+v", res, h.b.Facts())
	}
}

func TestNoRevivalAfterReleaseOrReportedDeath(t *testing.T) {
	h := newHarness(t, nil)
	h.twoNodes()
	h.snapshot(laptop, rec("anchor-L1"), rec("anchor-L2"))
	h.claim(laptop, "L1", wire.ReachUser, name("a"), "a")
	h.claim(laptop, "L2", wire.ReachUser, name("b"), "a")

	var res wire.Result

	h.call(laptop, wire.PathRelease, wire.Release{Version: wire.Current, RequestID: "R1", ID: "L1", Holder: holder("a"), Cause: wire.CauseReleased}, &res)
	h.call(laptop, wire.PathRelease, wire.Release{Version: wire.Current, RequestID: "R2", ID: "L2", Holder: holder("a"), Cause: wire.CauseHolderDead}, &res)

	h.clock.Advance(3 * testTerm)
	h.b.Sweep()

	for _, id := range []string{"L1", "L2"} {
		if got := h.renew(laptop, id); got[0].Code != wire.CodeNotFound {
			t.Fatalf("renew %s after the end = %+v", id, got[0])
		}
	}
}

func TestReleaseChecksTheHolderButDeathReportDoesNot(t *testing.T) {
	h := newHarness(t, nil)
	h.twoNodes()
	h.claim(laptop, "L1", wire.ReachUser, name("svc"), "a")

	var res wire.Result

	h.call(laptop, wire.PathRelease, wire.Release{Version: wire.Current, RequestID: "R1", ID: "L1", Holder: holder("other"), Cause: wire.CauseReleased}, &res)

	if res.Code != wire.CodeNotFound {
		t.Fatalf("another session's release: %+v", res)
	}

	n := repoid.New([]string{rootA}, "svc", false, false)
	h.call(laptop, wire.PathRelease, wire.Release{Version: wire.Current, RequestID: "R2", Name: &n, Holder: holder("a"), Cause: wire.CauseReleased}, &res)

	if res.Code != wire.CodeReleased || len(h.b.Leases()) != 0 {
		t.Fatalf("release by name: %+v", res)
	}
}

func TestRevivalNeedsTheAnchorInTheLatestSnapshot(t *testing.T) {
	h := newHarness(t, nil)
	h.twoNodes()
	h.snapshot(laptop, rec("anchor-L1"))
	h.claim(laptop, "L1", wire.ReachUser, name("svc"), "a")

	h.clock.Advance(2 * testTerm)
	h.b.Sweep()

	h.snapshot(laptop)

	if got := h.renew(laptop, "L1"); got[0].Code == wire.CodeRevived {
		t.Fatalf("revival with the anchor gone: %+v", got[0])
	}

	if len(h.b.Leases()) != 0 {
		t.Fatalf("a snapshot that omits the anchor removes the lapsed lease: %+v", h.b.Leases())
	}
}

func TestLapsedLeaseOutlivesUnseenLabelAfter30Days(t *testing.T) {
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	h := newHarness(t, func(c *Config) { c.Now = func() time.Time { return now } })
	h.twoNodes()
	h.snapshot(laptop, rec("anchor-L1"))
	h.claim(laptop, "L1", wire.ReachUser, name("svc"), "a")

	h.clock.Advance(2 * testTerm)
	h.b.Sweep()

	now = now.Add(29 * 24 * time.Hour)
	h.b.Sweep()

	if len(h.b.Leases()) != 1 {
		t.Fatal("a lapsed lease stays within 30 days")
	}

	now = now.Add(2 * 24 * time.Hour)
	h.b.Sweep()

	if len(h.b.Leases()) != 0 {
		t.Fatal("a lapsed lease goes once its label is unseen for 30 days")
	}
}

func TestOutcomesAreKeptSevenDays(t *testing.T) {
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	h := newHarness(t, func(c *Config) { c.Now = func() time.Time { return now } })
	h.twoNodes()
	h.claim(laptop, "L1", wire.ReachUser, name("svc"), "a")

	h.clock.Advance(2 * testTerm)
	now = now.Add(6 * 24 * time.Hour)
	h.b.Sweep()

	if _, ok := h.store.Data().Outcomes["L1"]; !ok {
		t.Fatal("an outcome stays for 7 days")
	}

	now = now.Add(2 * 24 * time.Hour)
	h.b.Sweep()

	if _, ok := h.store.Data().Outcomes["L1"]; ok {
		t.Fatal("an outcome goes after 7 days")
	}
}

func TestUnknownLivenessIsShownAndKeepsRenewing(t *testing.T) {
	h := newHarness(t, nil)
	h.twoNodes()
	h.claim(laptop, "L1", wire.ReachUser, name("svc"), "a")

	var out wire.Renewed

	h.call(laptop, wire.PathRenew, wire.Renew{Version: wire.Current, Items: []wire.RenewItem{{ID: "L1", Liveness: wire.LiveUnknown}}}, &out)

	if out.Results[0].Code != wire.CodeRenewed || h.b.Leases()[0].Liveness != wire.LiveUnknown {
		t.Fatalf("renew with unknown liveness: %+v / %+v", out, h.b.Leases())
	}

	res := h.claim(studio, "L2", wire.ReachUser, name("svc"), "b")
	if res.Code != wire.CodeRefused {
		t.Fatalf("an unknown holder still blocks: %+v", res)
	}
}

func TestLapseEventsCarryRevisions(t *testing.T) {
	h := newHarness(t, nil)
	h.twoNodes()
	h.snapshot(laptop, rec("anchor-L1"))
	h.claim(laptop, "L1", wire.ReachUser, name("svc"), "a")

	h.clock.Advance(2 * testTerm)
	h.b.Sweep()

	events := h.b.Unacked(laptop.key())
	if len(events) != 1 || events[0].Kind != wire.EventLease {
		t.Fatalf("events = %+v", events)
	}

	var l wire.Lease
	if err := json.Unmarshal(events[0].Data, &l); err != nil || l.State != wire.StateLapsed || l.Revision != 2 {
		t.Fatalf("event lease = %+v, %v", l, err)
	}
}

func TestAnotherNodeCannotReplayAnOutcomeItDidNotMake(t *testing.T) {
	h := newHarness(t, nil)
	h.twoNodes()
	h.claim(laptop, "L1", wire.ReachUser, name("svc"), "a")

	status := h.call(studio, wire.PathClaim, wire.Claim{
		Version: wire.Current, ID: "L1", Reach: wire.ReachUser, Entries: name("svc"), Derivation: 1, Anchor: "x", Holder: holder("b"),
	}, nil)

	if status == http.StatusOK {
		t.Fatal("a node must not receive another node's stored outcome")
	}

	if len(h.b.Leases()) != 1 || h.b.Leases()[0].Node != h.store.Data().Nodes[laptop.key().String()].Label {
		t.Fatalf("leases = %+v", h.b.Leases())
	}
}

func TestDerivationAboveWhatTheBrokerKnowsIsRefused(t *testing.T) {
	h := newHarness(t, nil)
	h.register(laptop)

	body := wire.Claim{Version: wire.Current, ID: "L1", Reach: wire.ReachUser, Entries: name("svc"), Derivation: MaxDerivation + 1, Anchor: "a", Holder: holder("a")}

	resp := h.do(http.MethodPost, wire.PathClaim, laptop, body)

	var e wire.Error
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil || resp.StatusCode != http.StatusBadRequest || e.Code != wire.CodeVersion {
		t.Fatalf("status %d, error %+v, %v", resp.StatusCode, e, err)
	}
}

func TestOldNodeSnapshotWithNoAckedFieldStillWorks(t *testing.T) {
	h := newHarness(t, nil)
	h.twoNodes()
	h.claim(laptop, "L1", wire.ReachUser, name("svc"), "a")

	// An M1 node posts {"version","leases"} and ignores whatever the answer holds.
	raw := map[string]any{"version": "1.0", "leases": []any{map[string]any{"id": "anchor-L1", "record": map[string]any{"id": "anchor-L1"}}}}

	resp := h.do(http.MethodPost, wire.PathSnapshot, laptop, raw)
	if resp.StatusCode/100 != 2 {
		t.Fatalf("status %d", resp.StatusCode)
	}

	if len(h.b.Leases()) != 1 || len(h.b.Facts()) != 0 {
		t.Fatalf("the snapshot must not disturb the lease: %+v", h.b.Leases())
	}
}

// TestSubstituteActingForPreventsCrossUserClaiming tests that a label bound to U1 cannot claim
// in U2's scope by naming U2's label in acting_for. No user delegated to the label, so substitute
// everything except the label's own bound user's label.
func TestSubstituteActingForPreventsCrossUserClaiming(t *testing.T) {
	h := newHarness(t, nil)
	h.twoNodes()

	u1ID := "user1"
	u1Label := DeriveUserLabel(u1ID)
	u2ID := "user2"
	u2Label := DeriveUserLabel(u2ID)

	d := h.store.Data()
	next := d.clone()
	next.OwnerID = u1ID
	next.Users[u1ID] = UserRecord{
		ID:              u1ID,
		UserLabel:       u1Label,
		LabelDerivation: UserLabelDerivation,
	}
	next.Users[u2ID] = UserRecord{
		ID:              u2ID,
		UserLabel:       u2Label,
		LabelDerivation: UserLabelDerivation,
	}

	laptopLabel := next.Nodes[laptop.key().String()].Label
	next.Bindings[laptopLabel] = BindingRecord{
		UserID:            u1ID,
		LastBoundUser:     u1ID,
		DerivationVersion: UserLabelDerivation,
	}

	if err := h.store.Persist(next); err != nil {
		t.Fatal(err)
	}
	h.store.Swap(next)

	var res wire.Result
	req := wire.Claim{
		Version: wire.Current, ID: "L1", Reach: wire.ReachUser, Derivation: 1, Anchor: "anchor-L1",
		Entries: name("svc"), Holder: holder("a"), ActingFor: u2Label,
	}

	if h.call(laptop, wire.PathClaim, req, &res) != http.StatusOK || res.Code != wire.CodeGranted {
		t.Fatalf("claim failed: %+v", res)
	}

	leases := h.b.Leases()
	if len(leases) != 1 {
		t.Fatalf("lease count = %d, want 1", len(leases))
	}

	if leases[0].ActingFor != u1Label {
		t.Errorf("stored lease ActingFor = %q, want %q (the label's bound user, not the claimed user)", leases[0].ActingFor, u1Label)
	}
}
