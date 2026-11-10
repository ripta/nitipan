package broker

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/space-armada/nitipan/internal/repoid"
	"github.com/space-armada/nitipan/internal/wire"
)

// claimRaw claims from node n and returns the answer as decoded and as the bytes the broker sent.
func (w *authzWorld) claimRaw(t *testing.T, n nodeID, id, reach string, entries []repoid.Name, hk, actingFor string) (res wire.Result, raw []byte) {
	t.Helper()

	resp := w.h.do(http.MethodPost, wire.PathClaim, n, wire.Claim{
		Version: wire.Current, ID: id, Reach: reach, Entries: entries, Derivation: 1, Anchor: "anchor-" + id,
		Holder: holder(hk), ActingFor: actingFor, ActingForExplicit: true,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("claim %s: status %d", id, resp.StatusCode)
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}

	return res, raw
}

// refusalNotices returns the claim-refused events queued for node n.
func (w *authzWorld) refusalNotices(t *testing.T, n nodeID) []wire.ClaimRefused {
	t.Helper()

	var out []wire.ClaimRefused

	for _, ev := range w.h.b.Unacked(n.key()) {
		if ev.Kind != wire.EventClaimRefused {
			continue
		}

		var cr wire.ClaimRefused
		if err := json.Unmarshal(ev.Data, &cr); err != nil {
			t.Fatal(err)
		}

		out = append(out, cr)
	}

	return out
}

func svcName(path string) []repoid.Name {
	return []repoid.Name{repoid.New([]string{rootA}, path, false, false)}
}

// hiddenWorld is a broker with cross-user features on, where U holds the user lease LU on svc/a.
func hiddenWorld(t *testing.T) *authzWorld {
	t.Helper()

	w := newAuthzWorld(t, true)
	w.claimAs(t, w.uNode, "LU", wire.ReachUser, svcName("svc/a"), "hu", w.u.UserLabel)

	return w
}

// wantNothingOfLU fails when the text names U's lease, holder, node, user, or the lease's path.
func wantNothingOfLU(t *testing.T, w *authzWorld, what string, text []byte) {
	t.Helper()

	for _, leak := range []string{"LU", "h-hu", "s-hu", w.uLabel, w.u.UserLabel, w.u.Email, "machine-u", "svc/a"} {
		if bytes.Contains(text, []byte(leak)) {
			t.Errorf("%s names %q of U's hidden lease: %s", what, leak, text)
		}
	}
}

// TestASharedClaimRefusedByAHiddenUserLeaseNamesNothingOfIt proves V's `shared` claim refused by U's
// `user` lease, which V's discovery hides, names no holder, path, node, or user, carries the lease in
// neither Others nor Overlaps, and tells V what to do next. It catches a refusal that lists every
// conflicting lease at full detail, which reopens what discovery hides.
func TestASharedClaimRefusedByAHiddenUserLeaseNamesNothingOfIt(t *testing.T) {
	w := hiddenWorld(t)

	res, raw := w.claimRaw(t, w.vNode, "LV", wire.ReachShared, svcName("svc"), "hv", w.v.UserLabel)
	if res.Code != wire.CodeRefused || res.Message != hiddenRefusal || len(res.Others) != 0 || len(res.Overlaps) != 0 {
		t.Fatalf("V's claim got %+v, want a hidden refusal with no others or overlaps", res)
	}

	for _, want := range []string{"held at user reach by another user", "Wait for that lease to end", "ask the broker owner"} {
		if !strings.Contains(res.Message, want) {
			t.Errorf("refusal %q lacks %q", res.Message, want)
		}
	}

	wantNothingOfLU(t, w, "the refusal", raw)
}

// TestADelegatedLabelAndAVisibleSharedLeaseGetTheFullRefusal proves the conflicts the asker can see
// are refused at full detail: a label holding U's delegation asking against U's `user` lease, and U
// asking against V's `shared` lease. It catches a redaction that hides every lease of another user, or
// that ignores delegations.
func TestADelegatedLabelAndAVisibleSharedLeaseGetTheFullRefusal(t *testing.T) {
	w := hiddenWorld(t)

	res, _ := w.claimRaw(t, w.agent, "LA", wire.ReachShared, svcName("svc"), "ha", w.v.UserLabel)
	if res.Code != wire.CodeRefused || strings.Contains(res.Message, hiddenRefusal) || len(res.Others) != 1 || res.Others[0].ID != "LU" ||
		!strings.Contains(res.Message, "lease LU held by") {
		t.Errorf("the delegated label got %+v, want the full refusal naming LU", res)
	}

	w.claimAs(t, w.vNode, "LV", wire.ReachShared, svcName("svc/b"), "hv", w.v.UserLabel)

	res, _ = w.claimRaw(t, w.uNode, "LU2", wire.ReachUser, svcName("svc/b"), "hu2", w.u.UserLabel)
	if res.Code != wire.CodeRefused || strings.Contains(res.Message, hiddenRefusal) || len(res.Others) != 1 || res.Others[0].ID != "LV" ||
		!strings.Contains(res.Message, "lease LV held by") {
		t.Errorf("U against V's shared lease got %+v, want the full refusal naming LV", res)
	}
}

// TestAMixedRefusalDetailsOnlyTheVisibleLease proves a refusal by one visible and one hidden lease
// lists only the visible one, and still says another user holds the name. It catches a refusal that
// drops the hidden blocker's existence, or one that details it along with the visible one.
func TestAMixedRefusalDetailsOnlyTheVisibleLease(t *testing.T) {
	w := hiddenWorld(t)

	w.claimAs(t, w.agent, "LA", wire.ReachShared, svcName("svc/b"), "ha", w.v.UserLabel)

	res, raw := w.claimRaw(t, w.vNode, "LV", wire.ReachShared, svcName("svc"), "hv", w.v.UserLabel)
	if res.Code != wire.CodeRefused || !strings.Contains(res.Message, hiddenRefusal) || len(res.Others) != 1 || res.Others[0].ID != "LA" {
		t.Fatalf("V's claim got %+v, want a hidden refusal listing only LA", res)
	}

	if !strings.Contains(res.Message, "lease LA held by") || !strings.Contains(res.Message, "held at user reach by another user") {
		t.Errorf("refusal %q should detail LA and say another user holds the name", res.Message)
	}

	wantNothingOfLU(t, w, "the mixed refusal", raw)
}

// TestAHiddenRefusalIsStoredFilteredAndReplaysFiltered proves the outcome the broker stores for a
// hidden refusal is the filtered one, and a retry of the request ID gets those same bytes. It catches a
// broker that stores the full refusal and filters only the first reply, so a replay leaks the lease.
func TestAHiddenRefusalIsStoredFilteredAndReplaysFiltered(t *testing.T) {
	w := hiddenWorld(t)

	_, first := w.claimRaw(t, w.vNode, "LV", wire.ReachShared, svcName("svc"), "hv", w.v.UserLabel)

	w.h.b.mu.Lock()
	stored := bytes.Clone(w.h.b.g.outcomes["LV"].Result)
	w.h.b.mu.Unlock()

	wantNothingOfLU(t, w, "the stored outcome", stored)

	_, again := w.claimRaw(t, w.vNode, "LV", wire.ReachShared, svcName("svc"), "hv", w.v.UserLabel)
	if !bytes.Equal(bytes.TrimSpace(first), bytes.TrimSpace(again)) || !bytes.Equal(bytes.TrimSpace(stored), bytes.TrimSpace(again)) {
		t.Errorf("replay %s differs from the first reply %s or the stored outcome %s", again, first, stored)
	}
}

// TestTheHiddenHolderIsToldOncePerCoworkerAcrossARestart proves U's node gets one claim-refused event
// per lease per coworker, naming only U's lease and V's user label. A second refused claim and a broker
// restart send no second one. A delegated label's full refusal sends none. It catches a notice sent on
// every refused claim, a dedup kept only in memory, and a notice that carries the names V asked for.
func TestTheHiddenHolderIsToldOncePerCoworkerAcrossARestart(t *testing.T) {
	w := hiddenWorld(t)

	w.claimRaw(t, w.agent, "LA", wire.ReachShared, svcName("svc"), "ha", w.v.UserLabel)

	if got := w.refusalNotices(t, w.uNode); len(got) != 0 {
		t.Fatalf("a full refusal to a delegated label told U: %+v", got)
	}

	w.claimRaw(t, w.vNode, "LV1", wire.ReachShared, svcName("svc"), "hv", w.v.UserLabel)
	w.claimRaw(t, w.vNode, "LV2", wire.ReachShared, svcName("svc/a/deeper"), "hv", w.v.UserLabel)

	got := w.refusalNotices(t, w.uNode)
	if len(got) != 1 || got[0] != (wire.ClaimRefused{Lease: "LU", Coworker: w.v.UserLabel}) {
		t.Fatalf("U's node got %+v, want one notice naming LU and %s", got, w.v.UserLabel)
	}

	for _, n := range []nodeID{w.vNode, w.agent} {
		if other := w.refusalNotices(t, n); len(other) != 0 {
			t.Errorf("node %s got a refusal notice: %+v", n.Machine, other)
		}
	}

	w.h.restart(nil)

	w.claimRaw(t, w.vNode, "LV3", wire.ReachShared, svcName("svc"), "hv", w.v.UserLabel)

	if again := w.refusalNotices(t, w.uNode); len(again) != 0 {
		t.Errorf("the restarted broker told U again: %+v", again)
	}
}

// TestAnOverlapReportShowsNoMirrorTheAskerCannotDiscover proves V's `shared` claim on a name U's
// mirrored `machine` lease overlaps carries no overlap naming that mirror, while U's own claim on the
// name still reports it. It catches an overlap report that skips the discovery rule for `shared` grants,
// which would hand V the local paths and names discovery hides.
func TestAnOverlapReportShowsNoMirrorTheAskerCannotDiscover(t *testing.T) {
	w := newAuthzWorld(t, true)
	entries := svcName("svc/a")

	w.h.snapshot(w.uNode, machineMirror(t, "MU", "hu", &wire.Derived{State: wire.DerivedOK, Derivation: 1, Entries: entries}, "/src/svc/a"))

	res, raw := w.claimRaw(t, w.vNode, "LV", wire.ReachShared, entries, "v", w.v.UserLabel)
	if res.Code != wire.CodeGranted || len(res.Overlaps) != 0 || bytes.Contains(raw, []byte("MU")) {
		t.Fatalf("V's shared claim reports U's mirror: %s", raw)
	}

	if res := w.releaseAs(t, w.vNode, "release-v", "LV", "v"); res.Code != wire.CodeReleased {
		t.Fatalf("V's release: %+v", res)
	}

	res, raw = w.claimRaw(t, w.uNode, "LU", wire.ReachShared, entries, "u", w.u.UserLabel)
	if res.Code != wire.CodeGranted || len(res.Overlaps) != 1 || res.Overlaps[0].Mirror.Lease != "MU" {
		t.Fatalf("U's shared claim does not report U's own mirror: %s", raw)
	}
}
