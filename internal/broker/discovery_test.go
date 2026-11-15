package broker

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/space-armada/nitipan/internal/keys"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/repoid"
	"github.com/space-armada/nitipan/internal/wire"
)

// machineMirror builds the mirror of a machine lease with the given keys and derived entries.
func machineMirror(t *testing.T, id, handle string, derived *wire.Derived, paths ...string) wire.MirroredLease {
	t.Helper()

	ks := make([]keys.Key, len(paths))
	for i, p := range paths {
		ks[i] = keys.Key{Path: p}
	}

	r := &record.Record{
		Envelope: record.Envelope{
			Version: record.PathVersion, ID: id, Footprint: record.PathFootprint(ks), ClearWhenDead: true,
			Liveness: record.LivenessProcess, Machine: record.Machine{Host: "h", ID: "m"},
			Holder: record.Holder{Agent: "claude", SessionID: "sess-" + handle, Handle: handle, Name: "name-" + handle, PID: 7},
		},
		Reach: record.ReachMachine, Domain: record.Domain{Kind: record.KindPath, Keys: ks}, ActingFor: "u",
	}

	data, err := r.Encode()
	if err != nil {
		t.Fatal(err)
	}

	return wire.MirroredLease{ID: id, Record: data, Derived: derived}
}

func resourceMirror(t *testing.T, id, handle, resource string) wire.MirroredLease {
	t.Helper()

	r := &record.Record{
		Envelope: record.Envelope{
			Version: record.ResourceVersion, ID: id, Footprint: record.ResourceFootprint(resource), ClearWhenDead: true,
			Liveness: record.LivenessProcess, Machine: record.Machine{Host: "h", ID: "m"},
			Holder: record.Holder{Agent: "claude", Handle: handle, PID: 8},
		},
		Reach: record.ReachMachine, Domain: record.Domain{Kind: record.KindResource, Name: resource, Slots: 1}, ActingFor: "u",
	}

	data, err := r.Encode()
	if err != nil {
		t.Fatal(err)
	}

	return wire.MirroredLease{ID: id, Record: data}
}

func okEntries(path string) *wire.Derived {
	return &wire.Derived{State: wire.DerivedOK, Derivation: 1, Entries: name(path)}
}

func (h *harness) whoHolds(asker nodeID, q wire.WhoHolds) (out wire.WhoHoldsAnswer, status int) {
	h.t.Helper()

	q.Version = wire.Current

	status = h.call(asker, wire.PathWhoHolds, q, &out)

	return out, status
}

func (h *harness) whoIs(asker nodeID, who string) wire.WhoIsAnswer {
	h.t.Helper()

	var out wire.WhoIsAnswer

	if status := h.call(asker, wire.PathWhoIs, wire.WhoIs{Version: wire.Current, Name: who}, &out); status != http.StatusOK {
		h.t.Fatalf("who is %q: status %d", who, status)
	}

	return out
}

func found(t *testing.T, ans wire.WhoHoldsAnswer, lease string) wire.Found {
	t.Helper()

	for _, f := range ans.Found {
		if f.Lease == lease {
			return f
		}
	}

	t.Fatalf("lease %s is not in the answer: %+v", lease, ans.Found)

	return wire.Found{}
}

func leaseIDs(fs []wire.Found) []string {
	var out []string

	for _, f := range fs {
		out = append(out, f.Lease)
	}

	slices.Sort(out)

	return out
}

func TestWhoHoldsRepoNameReachesGrantsAndEveryMirror(t *testing.T) {
	h := newHarness(t, nil)
	h.twoNodes()
	h.claim(laptop, "G1", wire.ReachUser, name("svc"), "a")

	h.snapshot(studio,
		machineMirror(t, "M-overlap", "m1", okEntries("svc/api"), "/x/svc/api"),
		machineMirror(t, "M-other", "m2", okEntries("docs"), "/x/docs"),
		machineMirror(t, "M-failed", "m3", &wire.Derived{State: wire.DerivedFailed, Derivation: 1, Reason: "the clone is shallow"}, "/x/y"),
		machineMirror(t, "M-older-node", "m4", nil, "/x/z"),
		machineMirror(t, "M-newer-derivation", "m5", &wire.Derived{State: wire.DerivedOK, Derivation: MaxDerivation + 1}, "/x/w"),
		wire.MirroredLease{ID: "M-unusable", Unknown: &wire.UnknownLease{Reason: "higher major", Footprint: wire.FootprintAll}},
		resourceMirror(t, "R1", "r1", "cpu:heavy"),
	)

	ans, status := h.whoHolds(laptop, wire.WhoHolds{Names: name("svc/api")})
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}

	want := []string{"G1", "M-failed", "M-newer-derivation", "M-older-node", "M-overlap", "M-unusable"}
	if got := leaseIDs(ans.Found); !slices.Equal(got, want) {
		t.Fatalf("holders = %v, want %v", got, want)
	}

	grant := found(t, ans, "G1")

	mailName, err := DeriveMailName("claude", "s-a", h.data().Labels[grant.Node].MachineID)
	if err != nil {
		t.Fatal(err)
	}

	if grant.State != wire.StateLive || grant.Reach != wire.ReachUser || grant.Qualified != FormatAddress(mailName, grant.Node) || len(grant.Names) != 1 {
		t.Fatalf("grant = %+v", grant)
	}

	if m := found(t, ans, "M-overlap"); m.State != wire.StateMirrored || m.Reach != record.ReachMachine || m.Stale || len(m.Names) != 1 {
		t.Fatalf("mirror = %+v", m)
	}

	for _, id := range []string{"M-failed", "M-newer-derivation", "M-older-node", "M-unusable"} {
		if f := found(t, ans, id); f.State != wire.StateUnknown || f.Why == "" {
			t.Fatalf("%s must read as unknown with a reason: %+v", id, f)
		}
	}

	if len(ans.Notes) == 0 {
		t.Fatal("an answer with unjudged mirrors must say so")
	}
}

func TestWhoHoldsReadsAMirrorBelowTheDerivationFloorAsUnknown(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.DerivationFloor = 2 })
	h.twoNodes()

	h.snapshot(studio, machineMirror(t, "M-below-floor", "m1", okEntries("svc"), "/x/svc"))

	ans, status := h.whoHolds(laptop, wire.WhoHolds{Names: name("svc")})
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}

	f := found(t, ans, "M-below-floor")
	if f.State != wire.StateUnknown || !strings.Contains(f.Why, "derivation") {
		t.Fatalf("a mirror below the broker's derivation floor must read as unknown with a reason: %+v", f)
	}
}

func TestWhoHoldsReportsStaleAndLapsedStates(t *testing.T) {
	h := newHarness(t, nil)
	h.twoNodes()
	h.snapshot(laptop, rec("anchor-G1"))
	h.claim(laptop, "G1", wire.ReachUser, name("svc"), "a")
	h.snapshot(studio, machineMirror(t, "M1", "m1", okEntries("svc"), "/x/svc"))

	h.clock.Advance(testTerm / 2)
	h.b.Wake()
	h.clock.Advance(testTerm)

	h.b.Sweep()

	ans, _ := h.whoHolds(laptop, wire.WhoHolds{Names: name("svc")})

	if g := found(t, ans, "G1"); g.State != wire.StateLapsed || !g.Stale {
		t.Fatalf("a lapsed grant on a quiet node = %+v", g)
	}

	if m := found(t, ans, "M1"); m.State != wire.StateStale || !m.Stale {
		t.Fatalf("a mirror whose node went quiet = %+v", m)
	}
}

func TestWhoHoldsPathMatchesOnlyTheAskersMachine(t *testing.T) {
	h := newHarness(t, nil)
	h.twoNodes()
	h.snapshot(laptop,
		machineMirror(t, "L-same", "a", nil, "/work/repo/svc"),
		machineMirror(t, "L-elsewhere", "b", nil, "/work/other"),
		wire.MirroredLease{ID: "L-unusable", Unknown: &wire.UnknownLease{Reason: "bad", Footprint: wire.FootprintAll}},
	)
	h.snapshot(studio, machineMirror(t, "S-same-path", "c", nil, "/work/repo/svc"))

	ans, status := h.whoHolds(laptop, wire.WhoHolds{Path: &keys.Key{Path: "/work/repo/svc/api"}})
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}

	want := []string{"L-same", "L-unusable"}
	if got := leaseIDs(ans.Found); !slices.Equal(got, want) {
		t.Fatalf("a path on one machine = %v, want %v", got, want)
	}

	if f := found(t, ans, "L-same"); f.State != wire.StateMirrored {
		t.Fatalf("a path match needs no repo entries: %+v", f)
	}

	if f := found(t, ans, "L-unusable"); f.State != wire.StateUnknown {
		t.Fatalf("an all-footprint mirror on the asker's machine must be reported: %+v", f)
	}
}

func TestWhoHoldsResourceMatchesOnEveryMachine(t *testing.T) {
	h := newHarness(t, nil)
	h.twoNodes()
	h.snapshot(laptop, resourceMirror(t, "R-laptop", "a", "cpu:heavy"), resourceMirror(t, "R-other", "b", "gpu:big"))
	h.snapshot(studio, resourceMirror(t, "R-studio", "c", "cpu:heavy"))

	ans, _ := h.whoHolds(laptop, wire.WhoHolds{Resource: "cpu:heavy"})

	if got, want := leaseIDs(ans.Found), []string{"R-laptop", "R-studio"}; !slices.Equal(got, want) {
		t.Fatalf("resource holders = %v, want %v", got, want)
	}

	if laptopHolder, studioHolder := found(t, ans, "R-laptop"), found(t, ans, "R-studio"); laptopHolder.Node == studioHolder.Node {
		t.Fatal("each holder is shown with the label of its own machine")
	}
}

func TestWhoHoldsQualifiedNameListsOneHoldersLeases(t *testing.T) {
	h := newHarness(t, nil)
	h.twoNodes()

	reg := h.b.Nodes()
	label := ""

	for _, v := range reg {
		if v.Key == laptop.key() {
			label = v.Label
		}
	}

	h.claim(laptop, "G1", wire.ReachUser, name("svc"), "a")
	h.claim(studio, "G2", wire.ReachUser, name("docs"), "b")
	h.snapshot(laptop, machineMirror(t, "M1", "h-a", nil, "/x"), machineMirror(t, "M2", "h-other", nil, "/y"))

	ans, status := h.whoHolds(studio, wire.WhoHolds{Address: "h-a@" + label})
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}

	if got, want := leaseIDs(ans.Found), []string{"G1", "M1"}; !slices.Equal(got, want) {
		t.Fatalf("leases of h-a@%s = %v, want %v", label, got, want)
	}

	_, status = h.whoHolds(studio, wire.WhoHolds{Address: "h-a@never-seen-label"})
	if status != http.StatusBadRequest {
		t.Fatalf("an address with a label the broker never saw: status %d, want %d", status, http.StatusBadRequest)
	}
}

func TestWhoIsFindsHoldersOnEveryMachineAndFlagsDuplicates(t *testing.T) {
	h := newHarness(t, nil)
	h.twoNodes()

	var held wire.Result

	h.call(laptop, wire.PathClaim, wire.Claim{
		Version: wire.Current, ID: "G1", Reach: wire.ReachUser, Entries: name("svc"), Derivation: 1, Anchor: "a1",
		Holder: wire.Holder{Key: "k1", Agent: "claude", Handle: "guardian-1", Name: "guardian"},
	}, &held)

	h.snapshot(studio,
		machineMirror(t, "M1", "guardian", nil, "/x"),
		machineMirror(t, "M2", "solo", nil, "/y"),
	)

	one := h.whoIs(laptop, "solo")
	if one.Duplicate || one.Holders != 1 || len(one.Found) != 1 || one.Found[0].Lease != "M2" {
		t.Fatalf("a unique name = %+v", one)
	}

	dup := h.whoIs(laptop, "guardian")

	if !dup.Duplicate || dup.Holders != 2 || !slices.Equal(leaseIDs(dup.Found), []string{"G1", "M1"}) {
		t.Fatalf("a name shared by two holders on two machines = %+v", dup)
	}

	byHandle := h.whoIs(laptop, "guardian-1")
	if byHandle.Duplicate || len(byHandle.Found) != 1 || byHandle.Found[0].Lease != "G1" {
		t.Fatalf("lookup by handle = %+v", byHandle)
	}

	if none := h.whoIs(laptop, "nobody"); len(none.Found) != 0 || none.Duplicate {
		t.Fatalf("an unknown name = %+v", none)
	}
}

func TestClaimReplyReportsOverlapsWithMirroredMachineLeases(t *testing.T) {
	h := newHarness(t, nil)
	h.twoNodes()

	h.snapshot(studio,
		machineMirror(t, "M-overlap", "m1", okEntries("svc/api"), "/x/svc/api"),
		machineMirror(t, "M-other", "m2", okEntries("docs"), "/x/docs"),
		machineMirror(t, "M-unknown", "m3", nil, "/x/z"),
	)

	res := h.claim(laptop, "G1", wire.ReachUser, name("svc"), "a")
	if res.Code != wire.CodeGranted {
		t.Fatalf("a mirrored machine lease never refuses a user claim: %+v", res)
	}

	if len(res.Overlaps) != 1 || res.Overlaps[0].Mirror.Lease != "M-overlap" || res.Overlaps[0].Grant.Lease != "G1" ||
		res.Overlaps[0].ID != "G1/M-overlap" {
		t.Fatalf("overlaps = %+v", res.Overlaps)
	}

	again := h.claim(laptop, "G1", wire.ReachUser, name("svc"), "a")
	if len(again.Overlaps) != 1 {
		t.Fatalf("the replayed outcome keeps the overlap: %+v", again)
	}
}

func TestOverlapsSkipTheHoldersOwnAnchorAndOtherLeases(t *testing.T) {
	h := newHarness(t, nil)
	h.twoNodes()

	own := machineMirror(t, "anchor-G1", "h-a", okEntries("svc"), "/x/svc")

	h.snapshot(laptop, own, machineMirror(t, "L-sibling", "other", okEntries("svc"), "/x/wt/svc"))

	res := h.claim(laptop, "G1", wire.ReachUser, name("svc"), "a")

	if len(res.Overlaps) != 1 || res.Overlaps[0].Mirror.Lease != "L-sibling" {
		t.Fatalf("only the sibling session's lease overlaps: %+v", res.Overlaps)
	}
}

func TestSnapshotAnswerCarriesOverlapsForBothSides(t *testing.T) {
	h := newHarness(t, nil)
	h.twoNodes()
	h.claim(laptop, "G1", wire.ReachUser, name("svc"), "a")

	snap := func(n nodeID, ls ...wire.MirroredLease) wire.SnapshotAck {
		var ack wire.SnapshotAck

		status := h.call(n, wire.PathSnapshot, wire.Snapshot{Version: wire.Current, Leases: ls}, &ack)
		if status != http.StatusOK && status != http.StatusNoContent {
			t.Fatalf("snapshot: status %d", status)
		}

		return ack
	}

	mine := snap(studio, machineMirror(t, "M1", "m1", okEntries("svc"), "/x/svc"))
	if len(mine.Overlaps) != 1 || mine.Overlaps[0].Mirror.Lease != "M1" {
		t.Fatalf("the mirror's node learns of the overlap: %+v", mine)
	}

	theirs := snap(laptop, machineMirror(t, "anchor-G1", "h-a", okEntries("svc"), "/y/svc"))
	if len(theirs.Overlaps) != 1 || theirs.Overlaps[0].Grant.Lease != "G1" {
		t.Fatalf("the grant's node learns of the overlap: %+v", theirs)
	}

	if third := snap(nodeIDWith("m-third", "key-third", h)); len(third.Overlaps) != 0 {
		t.Fatalf("a node whose leases are not involved is told nothing: %+v", third)
	}
}

func nodeIDWith(machine, key string, h *harness) nodeID {
	n := nodeID{Machine: machine, UID: 501, Key: key}
	h.register(n)

	return n
}

func TestWhoHoldsRefusesAQueryThatNamesNothing(t *testing.T) {
	h := newHarness(t, nil)
	h.twoNodes()

	if _, status := h.whoHolds(laptop, wire.WhoHolds{}); status != http.StatusBadRequest {
		t.Fatalf("status %d, want %d", status, http.StatusBadRequest)
	}
}

func TestDiscoveryNeedsAnAuthenticatedNode(t *testing.T) {
	h := newHarness(t, nil)
	h.twoNodes()

	bad := nodeID{Machine: laptop.Machine, UID: laptop.UID, Key: "wrong"}

	if _, status := h.whoHolds(bad, wire.WhoHolds{Names: []repoid.Name{name("svc")[0]}}); status != http.StatusUnauthorized {
		t.Fatalf("status %d, want %d", status, http.StatusUnauthorized)
	}
}

func TestWhoIsListsAnUnreadableMirrorAsUnknownInsteadOfSkippingIt(t *testing.T) {
	h := newHarness(t, nil)
	h.twoNodes()
	h.snapshot(studio,
		machineMirror(t, "M1", "solo", nil, "/x"),
		wire.MirroredLease{ID: "M-unusable", Unknown: &wire.UnknownLease{Reason: "higher major", Footprint: wire.FootprintAll}},
	)

	ans := h.whoIs(laptop, "solo")

	if got, want := leaseIDs(ans.Found), []string{"M-unusable", "M1"}; !slices.Equal(got, want) {
		t.Fatalf("holders = %v, want %v", got, want)
	}

	for _, f := range ans.Found {
		if f.Lease == "M-unusable" && (f.State != wire.StateUnknown || f.Why == "") {
			t.Fatalf("the unreadable mirror = %+v", f)
		}
	}

	if ans.Holders != 1 || len(ans.Notes) == 0 {
		t.Fatalf("the answer must say a holder behind an unreadable record may be missed: %+v", ans)
	}
}

func TestDiscoveryPrintsMailAddressesAndAcceptsOneAndNeverFallsBackToTheHandle(t *testing.T) {
	h := newHarness(t, nil)
	h.twoNodes()

	h.claim(laptop, "G1", wire.ReachUser, name("svc"), "a")

	// A Claude Code holder whose record predates session_id has no mail address.
	noSession := machineMirror(t, "M1", "h-old", nil, "/x")

	var r record.Record
	if err := json.Unmarshal(noSession.Record, &r); err != nil {
		t.Fatal(err)
	}

	r.Holder.SessionID = ""

	data, err := r.Encode()
	if err != nil {
		t.Fatal(err)
	}

	noSession.Record = data
	h.snapshot(laptop, noSession)

	label := h.data().Nodes[laptop.key().String()].Label
	want, _ := DeriveMailName("claude", "s-a", laptop.Machine)
	addr := FormatAddress(want, label)

	ans, status := h.whoHolds(studio, wire.WhoHolds{Address: addr})
	if status != http.StatusOK || !slices.Contains(leaseIDs(ans.Found), "G1") {
		t.Fatalf("who holds %s: status %d, found %v", addr, status, leaseIDs(ans.Found))
	}

	if f := found(t, ans, "G1"); f.Qualified != addr || f.NotMailable != "" {
		t.Errorf("found = %+v, want the mail address", f)
	}

	byName := h.whoIs(studio, want)
	if byName.Holders != 1 || len(byName.Found) != 1 || byName.Found[0].Lease != "G1" {
		t.Errorf("who is %s = %+v", want, byName)
	}

	all, _ := h.whoHolds(studio, wire.WhoHolds{Address: "h-old@" + label})
	m := found(t, all, "M1")

	if m.Qualified != "" || m.NotMailable == "" {
		t.Errorf("a holder with no session ID = %+v, want not mailable and no fallback to the handle", m)
	}
}
