package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/repoid"
	"github.com/space-armada/nitipan/internal/wire"
)

// authzWorld is a broker with two users. U has one label of their own. V has one label of their own and
// a shared agent's label, which holds U's delegation.
//
// With cross-user features off, the broker serves only its owner's labels and unbound labels. U is then the
// owner, and V's own label stays unbound, so the world is the one scope the broker runs.
type authzWorld struct {
	h *harness

	u, v UserRecord

	uNode, vNode, agent nodeID

	uLabel, vLabel, agentLabel string
}

func newAuthzWorld(t *testing.T, crossUser bool) *authzWorld {
	t.Helper()

	h := newCrossUserHarness(t)
	now := time.Now()

	u := h.data().Users[h.data().OwnerID]

	if crossUser {
		var err error

		if u, err = h.b.AdmitUser(createIssuer, "u@example.com", "u@example.com", now); err != nil {
			t.Fatal(err)
		}
	}

	v, err := h.b.AdmitUser(createIssuer, "v@example.com", "v@example.com", now)
	if err != nil {
		t.Fatal(err)
	}

	w := &authzWorld{
		h: h, u: u, v: v,
		uNode: nodeID{Machine: "machine-u", UID: 1000, Key: "key-u"},
		vNode: nodeID{Machine: "machine-v", UID: 1001, Key: "key-v"},
		agent: nodeID{Machine: "machine-agent", UID: 1002, Key: "key-agent"},
	}

	w.uLabel = w.bind(t, w.uNode, u.ID)

	if crossUser {
		w.vLabel = w.bind(t, w.vNode, v.ID)
	} else {
		w.vLabel = h.register(w.vNode).Label
	}

	w.agentLabel = w.bind(t, w.agent, v.ID)

	h.delegate(t, u, w.agentLabel)

	if crossUser {
		h.crossUserOn(t)
	}

	return w
}

// testDelegationIssuer is the issuer the tests sign users in under for the page's delegation actions.
const testDelegationIssuer = "https://issuer.test"

// signIn links an issuer plus sub to u, once, so u can act on the page as an admitted user would.
func (h *harness) signIn(t *testing.T, u UserRecord) {
	t.Helper()

	h.b.mu.Lock()
	_, linked := h.b.store.Data().IssuerSubs[issuerSubKey(testDelegationIssuer, u.ID)]
	h.b.mu.Unlock()

	if linked {
		return
	}

	if err := h.b.LinkIssuerSub(testDelegationIssuer, u.ID, u.ID); err != nil {
		t.Fatal(err)
	}
}

// delegate grants u's delegation to label, as u's page action does.
func (h *harness) delegate(t *testing.T, u UserRecord, label string) {
	t.Helper()

	h.signIn(t, u)

	if _, err := h.b.GrantDelegation(testDelegationIssuer, u.ID, label, time.Now()); err != nil {
		t.Fatal(err)
	}
}

// grantMail lets granter receive mail from sender, as granter's page action does.
func (h *harness) grantMail(t *testing.T, granter, sender UserRecord) {
	t.Helper()

	h.signIn(t, granter)

	if _, _, err := h.b.GrantMail(testDelegationIssuer, granter.ID, sender.UserLabel, h.wall.Now()); err != nil {
		t.Fatal(err)
	}
}

// undelegate revokes u's delegation to label, as u's page action does.
func (h *harness) undelegate(t *testing.T, u UserRecord, label string) {
	t.Helper()

	h.signIn(t, u)

	if _, err := h.b.RevokeDelegation(testDelegationIssuer, u.ID, label); err != nil {
		t.Fatal(err)
	}
}

func (w *authzWorld) bind(t *testing.T, n nodeID, userID string) string {
	t.Helper()

	w.h.register(n)

	return w.h.bindNode(t, n, userID)
}

// claimAs claims a lease from node n, with acting_for set to the given user label, and requires a grant.
func (w *authzWorld) claimAs(t *testing.T, n nodeID, id, reach string, entries []repoid.Name, hk, actingFor string) {
	t.Helper()

	var res wire.Result

	status := w.h.call(n, wire.PathClaim, wire.Claim{
		Version: wire.Current, ID: id, Reach: reach, Entries: entries, Derivation: 1, Anchor: "anchor-" + id,
		Holder: holder(hk), ActingFor: actingFor, ActingForExplicit: true,
	}, &res)
	if status != http.StatusOK || res.Code != wire.CodeGranted {
		t.Fatalf("claim %s: status %d, %+v", id, status, res)
	}
}

func (w *authzWorld) breakAs(t *testing.T, n nodeID, reqID, id string) wire.Result {
	t.Helper()

	var res wire.Result

	status := w.h.call(n, wire.PathBreak, wire.Break{
		Version: wire.Current, RequestID: reqID, ID: id, Force: true, By: holder("breaker"),
	}, &res)
	if status != http.StatusOK {
		t.Fatalf("break %s: status %d", id, status)
	}

	return res
}

func (w *authzWorld) releaseAs(t *testing.T, n nodeID, reqID, id, hk string) wire.Result {
	t.Helper()

	var res wire.Result

	status := w.h.call(n, wire.PathRelease, wire.Release{
		Version: wire.Current, RequestID: reqID, ID: id, Holder: holder(hk), Cause: wire.CauseReleased,
	}, &res)
	if status != http.StatusOK {
		t.Fatalf("release %s: status %d", id, status)
	}

	return res
}

func (w *authzWorld) grant(id string) *grant {
	w.h.b.mu.Lock()
	defer w.h.b.mu.Unlock()

	g := w.h.b.g.grants[id]
	if g == nil {
		return nil
	}

	c := *g

	return &c
}

// announced reports whether the node has an unacked lease event saying the lease ended.
func (w *authzWorld) announced(t *testing.T, n nodeID, id string) bool {
	t.Helper()

	for _, ev := range w.h.b.Unacked(n.key()) {
		if ev.Kind != wire.EventLease {
			continue
		}

		var l wire.Lease
		if err := json.Unmarshal(ev.Data, &l); err != nil {
			t.Fatal(err)
		}

		if l.ID == id && l.State == wire.StateEnded {
			return true
		}
	}

	return false
}

// TestAUserMayForceBreakTheirOwnLeases proves a user's other label may force-break a lease that is
// the user's own: one U's label holds for another user, and one a shared agent holds with acting_for
// set to U. It catches an ownership check that looks only at the holder's node, only at acting_for, or
// only at the holder's bound user.
func TestAUserMayForceBreakTheirOwnLeases(t *testing.T) {
	w := newAuthzWorld(t, true)
	second := nodeID{Machine: "machine-u2", UID: 1003, Key: "key-u2"}
	w.bind(t, second, w.u.ID)

	w.h.delegate(t, w.v, w.uLabel)

	w.claimAs(t, w.uNode, "L-for-v", wire.ReachShared, name("for-v"), "a", w.v.UserLabel)
	w.claimAs(t, w.agent, "L-agent", wire.ReachShared, name("agent"), "g", w.u.UserLabel)

	for _, id := range []string{"L-for-v", "L-agent"} {
		if res := w.breakAs(t, second, "break-"+id, id); res.Code != wire.CodeBroken {
			t.Errorf("U's second label breaking %s: %+v", id, res)
		}

		if g := w.grant(id); g == nil || g.State != wire.StateEnded {
			t.Errorf("%s after U's break: %+v", id, g)
		}
	}
}

// TestACoworkerCannotForceBreakAnotherUsersSharedLease proves V's force-break of U's live shared lease
// is refused, names the ways forward, and leaves the lease live. It catches a break check that lets
// any bound user end a coworker's lease.
func TestACoworkerCannotForceBreakAnotherUsersSharedLease(t *testing.T) {
	w := newAuthzWorld(t, true)
	w.claimAs(t, w.uNode, "L1", wire.ReachShared, name("svc"), "a", w.u.UserLabel)

	res := w.breakAs(t, w.vNode, "break-1", "L1")
	if res.Code != wire.CodeRefused {
		t.Fatalf("V's force-break of U's shared lease: %+v", res)
	}

	for _, want := range []string{"releases it", "term", "death report", "its user", "broker owner", "nitipan broker break L1"} {
		if !strings.Contains(res.Message, want) {
			t.Errorf("refusal %q does not name %q", res.Message, want)
		}
	}

	if g := w.grant("L1"); g == nil || g.State != wire.StateLive {
		t.Fatalf("L1 after the refused break: %+v", g)
	}
}

// TestWithCrossUserOffAnyNodeMayForceBreak proves the single-scope break rule holds while cross-user
// features are off: an unbound label may still force-break the owner's lease. It catches a cross-user
// break check that runs before the owner turns the features on.
func TestWithCrossUserOffAnyNodeMayForceBreak(t *testing.T) {
	w := newAuthzWorld(t, false)
	w.claimAs(t, w.uNode, "L1", wire.ReachShared, name("svc"), "a", w.u.UserLabel)

	if res := w.breakAs(t, w.vNode, "break-1", "L1"); res.Code != wire.CodeBroken {
		t.Fatalf("force-break with cross-user off: %+v", res)
	}

	if g := w.grant("L1"); g == nil || g.State != wire.StateEnded {
		t.Fatalf("L1 after the break: %+v", g)
	}
}

// TestUsersOwnLabelReleasesTheAgentsLeaseForThem proves U's own label may release a lease the shared
// agent holds for U, and that the release ends the lease and tells the agent's node, so its anchor
// does not outlive the lease. It catches a release that matches only the holder's node or holder key,
// and one that drops the grant without an end fact.
func TestUsersOwnLabelReleasesTheAgentsLeaseForThem(t *testing.T) {
	w := newAuthzWorld(t, true)
	w.claimAs(t, w.agent, "L1", wire.ReachUser, name("svc"), "g", w.u.UserLabel)

	res := w.releaseAs(t, w.uNode, "release-1", "L1", "u-session")
	if res.Code != wire.CodeReleased {
		t.Fatalf("U's release of the agent's lease for U: %+v", res)
	}

	g := w.grant("L1")
	if g == nil || g.State != wire.StateEnded || !strings.Contains(g.EndedBy, "u-session") {
		t.Fatalf("L1 after U's release: %+v", g)
	}

	if !w.announced(t, w.agent, "L1") {
		t.Fatal("the agent's node was not told that U released L1")
	}
}

// TestTheHolderStillReleasesAndAnUnrelatedUserCannot proves the holder's own node still releases its
// lease, and a user the lease does not belong to cannot release it. It catches a release check that
// shuts out the holder, and one that lets any bound user release.
func TestTheHolderStillReleasesAndAnUnrelatedUserCannot(t *testing.T) {
	w := newAuthzWorld(t, true)
	w.claimAs(t, w.agent, "L1", wire.ReachUser, name("svc"), "g", w.u.UserLabel)
	w.claimAs(t, w.uNode, "L2", wire.ReachShared, name("other"), "a", w.u.UserLabel)

	if res := w.releaseAs(t, w.vNode, "release-v", "L2", "a"); res.Code != wire.CodeNotFound {
		t.Fatalf("V's release of U's lease: %+v", res)
	}

	if g := w.grant("L2"); g == nil || g.State != wire.StateLive {
		t.Fatalf("L2 after V's release: %+v", g)
	}

	if res := w.releaseAs(t, w.agent, "release-g", "L1", "g"); res.Code != wire.CodeReleased {
		t.Fatalf("the holder's release of its own lease: %+v", res)
	}

	if g := w.grant("L1"); g != nil {
		t.Fatalf("L1 after the holder's release: %+v", g)
	}
}

// TestUsersBreakOfTheAgentsLeaseTellsTheAgentsNode proves a break by U of the lease an agent holds for
// U reaches the agent's node as an end fact. It catches a break that ends the lease without
// announcing it to the holder.
func TestUsersBreakOfTheAgentsLeaseTellsTheAgentsNode(t *testing.T) {
	w := newAuthzWorld(t, true)
	w.claimAs(t, w.agent, "L1", wire.ReachUser, name("svc"), "g", w.u.UserLabel)

	if res := w.breakAs(t, w.uNode, "break-1", "L1"); res.Code != wire.CodeBroken {
		t.Fatalf("U's break of the agent's lease for U: %+v", res)
	}

	if !w.announced(t, w.agent, "L1") {
		t.Fatal("the agent's node was not told that U broke L1")
	}
}

// TestAnAgentsLeaseForUCountsOnlyInUsUserScope proves a `user` lease a shared agent holds for U
// conflicts with U's own `user` claim and not with one from the agent's own user. It catches a scope
// taken from the holder's bound user rather than from acting_for, which would let U's sessions and the
// agent's work for U overlap unseen and block the agent's own user instead.
func TestAnAgentsLeaseForUCountsOnlyInUsUserScope(t *testing.T) {
	w := newAuthzWorld(t, true)
	w.claimAs(t, w.agent, "L1", wire.ReachUser, name("svc"), "g", w.u.UserLabel)

	w.claimAs(t, w.vNode, "LV", wire.ReachUser, name("svc"), "v", w.v.UserLabel)

	res, _ := w.claimRaw(t, w.uNode, "LU", wire.ReachUser, name("svc"), "u", w.u.UserLabel)
	if res.Code != wire.CodeRefused || len(res.Others) != 1 || res.Others[0].ID != "L1" {
		t.Fatalf("U's user claim against the agent's lease for U: %+v, want a refusal naming L1", res)
	}
}

// TestRevokingADelegationTellsTheAgentsNode proves U's revoke of the agent's delegation ends the
// agent's lease for U as an end fact queued for the agent's node, which writes the holder its notice,
// and leaves the agent's lease for its own user live. A renew of the ended lease answers ended and does
// not revive it. It catches a revoke that drops leases silently, one that ends every lease the label
// holds, and one a renewal undoes.
func TestRevokingADelegationTellsTheAgentsNode(t *testing.T) {
	w := newAuthzWorld(t, true)
	w.claimAs(t, w.agent, "L-for-u", wire.ReachUser, name("svc"), "g", w.u.UserLabel)
	w.claimAs(t, w.agent, "L-own", wire.ReachUser, name("own"), "g2", w.v.UserLabel)

	w.h.undelegate(t, w.u, w.agentLabel)

	if g := w.grant("L-for-u"); g == nil || g.State != wire.StateEnded || g.EndedBy != "delegation revoked" {
		t.Fatalf("L-for-u after the revoke: %+v", g)
	}

	if !w.announced(t, w.agent, "L-for-u") {
		t.Fatal("the agent's node was not told that the revoke ended L-for-u")
	}

	if got := w.h.renew(w.agent, "L-for-u"); len(got) != 1 || got[0].Code != wire.CodeEnded || got[0].EndedBy != "delegation revoked" {
		t.Fatalf("renewing L-for-u after the revoke: %+v, want ended by the revoke", got)
	}

	if g := w.grant("L-for-u"); g == nil || g.State != wire.StateEnded {
		t.Fatalf("L-for-u after the renew: %+v, want it still ended", g)
	}

	if g := w.grant("L-own"); g == nil || g.State != wire.StateLive {
		t.Fatalf("the agent's own lease after the revoke: %+v", g)
	}
}

func (w *authzWorld) serveAdmin(t *testing.T) *http.Client {
	t.Helper()

	path := filepath.Join(shortDir(t), "admin.sock")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() { done <- w.h.b.ServeAdmin(ctx, path) }()

	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Log(err)
		}
	})

	waitUntil(t, "the admin socket", func() bool {
		_, err := os.Stat(path)

		return err == nil
	})

	return adminClient(path)
}

// TestTheOwnersAdminBreakEndsACoworkersSharedLease proves the broker owner's admin break ends a lease
// no node may break: it sets the lease ended with EndedBy naming the owner by user label, tells the
// holder's node, and a later renew answers ended. It catches an admin break that drops the grant
// silently, that leaves it renewable, or that names the owner by an email the owner may not have.
func TestTheOwnersAdminBreakEndsACoworkersSharedLease(t *testing.T) {
	w := newAuthzWorld(t, true)

	w.h.b.mu.Lock()
	err := w.h.b.commit(func(d *Data) error {
		d.OwnerID = w.v.ID

		return nil
	})
	w.h.b.mu.Unlock()

	if err != nil {
		t.Fatal(err)
	}

	w.claimAs(t, w.uNode, "L1", wire.ReachShared, name("svc"), "a", w.u.UserLabel)

	cl := w.serveAdmin(t)
	body, _ := json.Marshal(map[string]string{"lease_id": "L1"})

	resp, err := cl.Post("http://admin"+AdminPathBreak, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}

	resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("admin break: status %d", resp.StatusCode)
	}

	g := w.grant("L1")
	if g == nil || g.State != wire.StateEnded || g.EndedBy != "broker owner "+w.v.UserLabel {
		t.Fatalf("L1 after the admin break: %+v", g)
	}

	if !w.announced(t, w.uNode, "L1") {
		t.Fatal("the holder's node was not told about the admin break")
	}

	var out wire.Renewed

	w.h.call(w.uNode, wire.PathRenew, wire.Renew{Version: wire.Current, Items: []wire.RenewItem{{ID: "L1", Liveness: wire.LiveAlive}}}, &out)

	if len(out.Results) != 1 || out.Results[0].Code != wire.CodeEnded {
		t.Fatalf("renew after the admin break: %+v", out)
	}
}

// discovered collects the lease IDs a node sees through who-holds by name, who-is by holder name, and
// list.
type discovered struct {
	holds, is, listed map[string]bool
}

func (w *authzWorld) discover(t *testing.T, n nodeID, names []repoid.Name, holderName string) discovered {
	t.Helper()

	d := discovered{holds: map[string]bool{}, is: map[string]bool{}, listed: map[string]bool{}}

	var holds wire.WhoHoldsAnswer
	if status := w.h.call(n, wire.PathWhoHolds, wire.WhoHolds{Version: wire.Current, Names: names}, &holds); status != http.StatusOK {
		t.Fatalf("who-holds: status %d", status)
	}

	for _, f := range holds.Found {
		d.holds[f.Lease] = true
	}

	var is wire.WhoIsAnswer
	if status := w.h.call(n, wire.PathWhoIs, wire.WhoIs{Version: wire.Current, Name: holderName}, &is); status != http.StatusOK {
		t.Fatalf("who-is: status %d", status)
	}

	for _, f := range is.Found {
		d.is[f.Lease] = true
	}

	var listed wire.Listed
	if status := w.h.call(n, wire.PathList, wire.ListRequest{Version: wire.Current}, &listed); status != http.StatusOK {
		t.Fatalf("list: status %d", status)
	}

	for _, l := range listed.Leases {
		d.listed[l.ID] = true
	}

	return d
}

// discoveryWorld gives U a shared lease, a user lease, and a mirrored machine lease, all on one repo
// name, with every holder carrying the name "name-u". The holders are U's own sessions.
func discoveryWorld(t *testing.T, crossUser bool) (*authzWorld, []repoid.Name) {
	t.Helper()

	w := newAuthzWorld(t, crossUser)
	entries := name("svc")

	w.h.snapshot(w.uNode, machineMirror(t, "M1", "u", &wire.Derived{State: wire.DerivedOK, Derivation: 1, Entries: entries}, "/src/svc"))

	for _, c := range []struct{ id, reach string }{{"L-shared", wire.ReachShared}, {"L-user", wire.ReachUser}} {
		var res wire.Result

		h := holder(c.id)
		h.Name = "name-u"

		status := w.h.call(w.uNode, wire.PathClaim, wire.Claim{
			Version: wire.Current, ID: c.id, Reach: c.reach, Entries: name(c.id), Derivation: 1, Anchor: "anchor-" + c.id,
			Holder: h, ActingFor: w.u.UserLabel,
		}, &res)
		if status != http.StatusOK || res.Code != wire.CodeGranted {
			t.Fatalf("claim %s: status %d, %+v", c.id, status, res)
		}
	}

	return w, append(append(entries, name("L-shared")...), name("L-user")...)
}

// TestACoworkerDiscoversOnlyAnotherUsersSharedLeases proves V's who-holds, who-is, and list show U's
// shared lease, and hide U's user lease and U's mirrored machine lease, so a lookup by holder name
// across users matches only the shared lease. It catches discovery that leaks a coworker's local paths
// and names.
func TestACoworkerDiscoversOnlyAnotherUsersSharedLeases(t *testing.T) {
	w, names := discoveryWorld(t, true)
	d := w.discover(t, w.vNode, names, "name-u")

	if !d.holds["L-shared"] || !d.is["L-shared"] || !d.listed["L-shared"] {
		t.Errorf("V does not see U's shared lease: %+v", d)
	}

	for _, id := range []string{"L-user", "M1"} {
		if d.holds[id] || d.is[id] || d.listed[id] {
			t.Errorf("V sees U's %s: %+v", id, d)
		}
	}
}

// TestADelegatedLabelDiscoversTheDelegatingUsersLeases proves a label holding U's delegation sees U's
// shared lease, user lease, and mirrored machine lease, by repo name and by holder name. It catches
// a discovery filter that ignores delegations.
func TestADelegatedLabelDiscoversTheDelegatingUsersLeases(t *testing.T) {
	w, names := discoveryWorld(t, true)
	d := w.discover(t, w.agent, names, "name-u")

	for _, id := range []string{"L-shared", "L-user", "M1"} {
		if !d.holds[id] || !d.is[id] {
			t.Errorf("the delegated label does not find U's %s: %+v", id, d)
		}
	}

	if !d.listed["L-shared"] || !d.listed["L-user"] {
		t.Errorf("the delegated label's list misses U's leases: %+v", d)
	}
}

// TestWithCrossUserOffDiscoveryFiltersNothing proves discovery from an unbound label shows every lease of
// the owner's while cross-user features are off, as the single scope it then is. It catches a filter that
// runs before the owner turns the features on.
func TestWithCrossUserOffDiscoveryFiltersNothing(t *testing.T) {
	w, names := discoveryWorld(t, false)
	d := w.discover(t, w.vNode, names, "name-u")

	for _, id := range []string{"L-shared", "L-user", "M1"} {
		if !d.holds[id] || !d.is[id] {
			t.Errorf("V does not find U's %s with cross-user off: %+v", id, d)
		}
	}

	if !d.listed["L-shared"] || !d.listed["L-user"] {
		t.Errorf("V's list misses U's leases with cross-user off: %+v", d)
	}
}

// TestAnOldVersionClaimNamingADelegatorIsSubstituted proves a claim from before explicit acting_for
// acts as the label's own bound user, whether it names a user who delegated to the label or one who did
// not. It catches a broker that honors a delegation on an unauthenticated value, which an older CLI fills
// with an OS user name that can collide with a user label, and one that passes such a value through.
func TestAnOldVersionClaimNamingADelegatorIsSubstituted(t *testing.T) {
	w := newAuthzWorld(t, true)

	var res wire.Result

	status := w.h.call(w.agent, wire.PathClaim, wire.Claim{
		Version: wire.Current, ID: "L1", Reach: wire.ReachUser, Entries: name("svc"), Derivation: 1, Anchor: "anchor-L1",
		Holder: holder("g"), ActingFor: w.u.UserLabel,
	}, &res)
	if status != http.StatusOK || res.Code != wire.CodeGranted || res.Lease.ActingFor != w.v.UserLabel {
		t.Fatalf("old-version claim naming U: status %d, %+v, want a grant for the agent's own user", status, res)
	}

	status = w.h.call(w.vNode, wire.PathClaim, wire.Claim{
		Version: wire.Current, ID: "L2", Reach: wire.ReachUser, Entries: name("other"), Derivation: 1, Anchor: "anchor-L2",
		Holder: holder("v"), ActingFor: w.u.UserLabel,
	}, &res)
	if status != http.StatusOK || res.Code != wire.CodeGranted || res.Lease.ActingFor != w.v.UserLabel {
		t.Fatalf("old-version claim naming U from a label U never delegated to: status %d, %+v, want a grant for V", status, res)
	}
}

// endedForU has the agent claim L1 for U, then has U break it, so L1 is an end fact the agent's node has
// not acked yet.
func endedForU(t *testing.T) *authzWorld {
	t.Helper()

	w := newAuthzWorld(t, true)
	w.claimAs(t, w.agent, "L1", wire.ReachUser, name("svc"), "g", w.u.UserLabel)

	if res := w.breakAs(t, w.uNode, "break-1", "L1"); res.Code != wire.CodeBroken {
		t.Fatalf("U's break of L1: %+v", res)
	}

	return w
}

// TestRevokingLeavesEndFactsAsTheyWere proves "revoke user" and a delegation's revoke end only live and
// lapsed leases. An end fact keeps who ended it and its revision. It catches a revoke that rewrites
// every lease held for the user, which would tell the holder the wrong cause and send a second end.
func TestRevokingLeavesEndFactsAsTheyWere(t *testing.T) {
	revokes := map[string]func(w *authzWorld) error{
		"revoke user": func(w *authzWorld) error { _, err := w.h.b.RevokeUser(w.u.ID); return err },
		"revoke delegation": func(w *authzWorld) error {
			_, err := w.h.b.RevokeDelegation(testDelegationIssuer, w.u.ID, w.agentLabel)

			return err
		},
	}

	for what, revoke := range revokes {
		t.Run(what, func(t *testing.T) {
			w := endedForU(t)
			before := w.grant("L1")

			if err := revoke(w); err != nil {
				t.Fatal(err)
			}

			after := w.grant("L1")
			if after == nil || after.State != wire.StateEnded || after.EndedBy != before.EndedBy || after.Revision != before.Revision {
				t.Fatalf("L1 after %s: %+v, want it unchanged from %+v", what, after, before)
			}
		})
	}
}

// TestABreakOfAnEndFactShowsItOnlyToWhoCanSeeIt proves a break naming U's ended `user` lease answers
// ended to every node, and carries the lease only to a node whose discovery shows it, here a label
// holding U's delegation. It catches a break that
// hands any node the full view of a coworker's hidden lease once it ended.
func TestABreakOfAnEndFactShowsItOnlyToWhoCanSeeIt(t *testing.T) {
	w := newAuthzWorld(t, true)
	second := nodeID{Machine: "machine-u2", UID: 1003, Key: "key-u2"}
	w.bind(t, second, w.u.ID)

	w.claimAs(t, w.uNode, "L1", wire.ReachUser, name("svc"), "u", w.u.UserLabel)

	if res := w.breakAs(t, second, "break-1", "L1"); res.Code != wire.CodeBroken {
		t.Fatalf("U's break of L1: %+v", res)
	}

	resp := w.h.do(http.MethodPost, wire.PathBreak, w.vNode, wire.Break{
		Version: wire.Current, RequestID: "break-v", ID: "L1", Force: true, By: holder("breaker"),
	})

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	var res wire.Result
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}

	if res.Code != wire.CodeEnded || res.Lease != nil || bytes.Contains(raw, []byte("svc")) || bytes.Contains(raw, []byte(w.u.UserLabel)) {
		t.Fatalf("V's break of U's ended lease: %s", raw)
	}

	if res := w.breakAs(t, w.agent, "break-g", "L1"); res.Code != wire.CodeEnded || res.Lease == nil || res.Lease.ID != "L1" {
		t.Fatalf("the delegated label's break of U's ended lease: %+v", res)
	}
}

// TestARevokedUserCannotGrantOrRevokeADelegation proves a grant or revoke from a user whose admission
// was just removed is refused, and changes nothing. It catches a page action that checks admission
// before taking the broker's lock, so a "revoke user" that lands in between lets it through.
func TestARevokedUserCannotGrantOrRevokeADelegation(t *testing.T) {
	w := newAuthzWorld(t, true)
	w.h.signIn(t, w.v)

	if _, err := w.h.b.RevokeUser(w.v.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := w.h.b.GrantDelegation(testDelegationIssuer, w.v.ID, w.uLabel, time.Now()); !errors.Is(err, ErrNotAdmitted) {
		t.Errorf("grant by a revoked user: %v, want %v", err, ErrNotAdmitted)
	}

	if _, err := w.h.b.RevokeDelegation(testDelegationIssuer, w.v.ID, w.uLabel); !errors.Is(err, ErrNotAdmitted) {
		t.Errorf("revoke by a revoked user: %v, want %v", err, ErrNotAdmitted)
	}

	w.h.b.mu.Lock()
	_, granted := w.h.b.store.Data().Delegations[w.v.ID]
	w.h.b.mu.Unlock()

	if granted {
		t.Error("the revoked user's grant took effect")
	}
}

// TestRevokingAUserEndsEveryLeaseADelegationOfTheirsAuthorized proves revoking U ends both kinds of lease a
// delegation authorized: the shared agent's lease for U, and the lease U's own label holds for V under V's
// delegation. Each end tells the holder's node. Revoking U drops the delegations naming U in both
// directions. It catches a revoke that drops V's delegation to U's label but leaves the lease it
// authorized live, so U's forgotten machine keeps holding a name for V.
func TestRevokingAUserEndsEveryLeaseADelegationOfTheirsAuthorized(t *testing.T) {
	w := newAuthzWorld(t, true)
	w.h.delegate(t, w.v, w.uLabel)

	w.claimAs(t, w.agent, "L-agent-for-u", wire.ReachUser, name("agent"), "g", w.u.UserLabel)
	w.claimAs(t, w.uNode, "L-u-for-v", wire.ReachUser, name("for-v"), "u", w.v.UserLabel)
	w.claimAs(t, w.vNode, "L-v", wire.ReachUser, name("v"), "v", w.v.UserLabel)

	rec := w.h.admin(http.MethodPost, AdminPathRevokeUser, map[string]string{"user_id": w.u.ID})
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke-user: %d %s", rec.Code, rec.Body)
	}

	var revoked RevokedUser
	if err := json.NewDecoder(rec.Body).Decode(&revoked); err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(revoked.Labels, []string{w.uLabel}) || !slices.Equal(revoked.Leases, []string{"L-agent-for-u", "L-u-for-v"}) ||
		revoked.Delegations != 2 {
		t.Errorf("revoke-user answered %+v, want U's label, both leases, and both delegations", revoked)
	}

	for _, id := range []string{"L-agent-for-u", "L-u-for-v"} {
		if g := w.grant(id); g == nil || g.State != wire.StateEnded || g.EndedBy != "user revoked" {
			t.Errorf("%s after revoking U: %+v, want it ended by the revoke", id, g)
		}
	}

	if !w.announced(t, w.agent, "L-agent-for-u") {
		t.Error("the agent's node was not told its lease for U ended")
	}

	if g := w.grant("L-v"); g == nil || g.State != wire.StateLive {
		t.Errorf("V's own lease after revoking U: %+v, want it live", g)
	}

	d := w.h.data()

	if _, ok := d.Delegations[w.u.ID]; ok {
		t.Errorf("U's delegations survive the revoke: %v", d.Delegations[w.u.ID])
	}

	if _, ok := d.Delegations[w.v.ID][w.uLabel]; ok {
		t.Error("V's delegation to U's label survives the revoke")
	}
}

// TestARevokeWhoseStoreWriteFailsLeavesTheUserWhole proves revoking a user is one store write. When it
// fails, the user keeps every label, admission, delegation, and lease, and the broker stops. It cannot
// catch a revoke split over several writes whose first one fails, since that one also changes nothing.
// It pins the single write, so no failure leaves a user half revoked.
func TestARevokeWhoseStoreWriteFailsLeavesTheUserWhole(t *testing.T) {
	w := newAuthzWorld(t, true)
	w.claimAs(t, w.agent, "L-agent-for-u", wire.ReachUser, name("agent"), "g", w.u.UserLabel)

	before, err := json.Marshal(w.h.data())
	if err != nil {
		t.Fatal(err)
	}

	if err := os.RemoveAll(filepath.Dir(w.h.path)); err != nil {
		t.Fatal(err)
	}

	if rec := w.h.admin(http.MethodPost, AdminPathRevokeUser, map[string]string{"user_id": w.u.ID}); rec.Code/100 == 2 {
		t.Fatalf("revoke-user with a failing store write: %d %s", rec.Code, rec.Body)
	}

	after, err := json.Marshal(w.h.data())
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(before, after) {
		t.Errorf("a failed revoke changed the store:\nbefore %s\nafter  %s", before, after)
	}

	if g := w.grant("L-agent-for-u"); g == nil || g.State != wire.StateLive {
		t.Errorf("the agent's lease after a failed revoke: %+v, want it live", g)
	}

	select {
	case <-w.h.b.Done():
	default:
		t.Error("a failed store write did not stop the broker")
	}
}
