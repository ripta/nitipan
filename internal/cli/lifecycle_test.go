package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/lease"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/state"
)

// tombstones decodes every published tombstone, in file-name order.
func (w *world) tombstones() []*record.Tombstone {
	w.t.Helper()

	var out []*record.Tombstone

	for _, name := range w.fileNames(state.TombstonesDir) {
		data, err := os.ReadFile(filepath.Join(w.state, state.TombstonesDir, name))
		if err != nil {
			w.t.Fatal(err)
		}

		tomb, err := record.DecodeTombstone(data)
		if err != nil {
			w.t.Fatal(err)
		}

		out = append(out, tomb)
	}

	return out
}

// wantStandsAlone checks that a former sub-lease is an ordinary lease at the lowest minor.
func (w *world) wantStandsAlone(id string) {
	w.t.Helper()

	rec := w.leaseByID(id)
	if rec == nil {
		w.t.Fatalf("lease %s is gone", id)
	}

	if rec.Parent != "" {
		w.t.Errorf("lease %s still names parent %s", id, rec.Parent)
	}

	if rec.Version != record.PathVersion {
		w.t.Errorf("lease %s is stamped %s, want %s", id, rec.Version, record.PathVersion)
	}
}

func TestADeadParentLeavesItsSubLeasesAsOrdinaryLeasesWithOneNoticeEach(t *testing.T) {
	tr := newTree(t)
	tr.w.kill(200)

	res := tr.w.run(tr.child, "acquire", "--checkout", tr.repo, "--scope", "src")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stderr", res.Stderr, "your sub-lease "+tr.childID, "lost its parent", "lease "+tr.parentID,
		"its holder died", "ordinary lease")
	wantAbsent(t, "stderr", res.Stderr, tr.siblingID)

	tr.w.wantStandsAlone(tr.childID)
	tr.w.wantStandsAlone(tr.siblingID)

	if names := tr.w.noticesOf(record.CauseParentEnded); len(names) != 1 || names[0] != tr.siblingID+"-parent-ended.json" {
		t.Errorf("notices = %v, want only the sibling's still waiting", names)
	}

	res = tr.w.run(tr.sibling, "list")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stderr", res.Stderr, "your sub-lease "+tr.siblingID, "lost its parent")

	res = tr.w.run(tr.sibling, "release", "--all")
	wantStatus(t, res, exitcode.OK)
	wantAbsent(t, "stderr", res.Stderr, "lost its parent")

	if names := tr.w.fileNames(state.TombstonesDir); len(names) != 0 {
		t.Errorf("tombstones = %v, want none: a notice is not a tombstone", names)
	}
}

func TestADeadParentsOwnPartIsClaimableAndTheSubLeasesKeysStayBlocked(t *testing.T) {
	tr := newTree(t)
	tr.w.kill(200)

	res := tr.w.run(tr.third, "acquire", "--checkout", tr.repo, "--scope", "src")
	wantStatus(t, res, exitcode.Refused)
	wantContains(t, "stderr", res.Stderr, "child-agent")
	wantAbsent(t, "stderr", res.Stderr, "parent-agent")

	wantStatus(t, tr.w.run(tr.third, "acquire", "--checkout", tr.repo, "--scope", "lib"), exitcode.OK)
}

func TestADanglingParentIsRepairedByTheNextLockHolder(t *testing.T) {
	w := newWorld(t)
	repo := w.dir("repo")
	w.dir("repo", "src")

	child := w.claude(300, "c")
	id := w.plant(plantSpec{PID: 300, Paths: []string{filepath.Join(repo, "src")}, Name: "child-agent", Parent: missingID})

	wantStatus(t, w.run(w.claude(400, "t"), "acquire", "--checkout", repo, "--scope", "lib"), exitcode.OK)

	w.wantStandsAlone(id)

	res := w.run(child, "release", "--all")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stderr", res.Stderr, "lost its parent", "lease "+missingID, "nitipan cannot tell")
}

func TestASubLeaseOfANewerMinorIsNeitherRewrittenNorAnnounced(t *testing.T) {
	w := newWorld(t)
	repo := w.dir("repo")
	w.dir("repo", "src")

	id := w.plant(plantSpec{PID: 300, Paths: []string{filepath.Join(repo, "src")}, Parent: missingID, Version: "1.5"})
	before, err := os.ReadFile(filepath.Join(w.leasesDir(), id+".json"))
	if err != nil {
		t.Fatal(err)
	}

	w.claude(300, "c")
	wantStatus(t, w.run(w.claude(400, "t"), "acquire", "--checkout", repo, "--scope", "lib"), exitcode.OK)

	after, err := os.ReadFile(filepath.Join(w.leasesDir(), id+".json"))
	if err != nil {
		t.Fatal(err)
	}

	if string(before) != string(after) {
		t.Errorf("a record of a newer minor was rewritten:\n%s\n%s", before, after)
	}

	if names := w.fileNames(state.NoticesDir); len(names) != 0 {
		t.Errorf("notices = %v, want none from a binary that cannot rewrite the record", names)
	}
}

func TestReleasingAParentOrphansItsChildrenAndListsThem(t *testing.T) {
	tr := newTree(t)

	res := tr.w.run(tr.parent, "release", tr.parentID)
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "released "+tr.parentID, "sub-lease "+tr.childID, "sub-lease "+tr.siblingID,
		"now stands alone", "child-agent", "sibling-agent")

	if tr.w.leaseByID(tr.parentID) != nil {
		t.Error("the parent survived its release")
	}

	tr.w.wantStandsAlone(tr.childID)
	tr.w.wantStandsAlone(tr.siblingID)

	res = tr.w.run(tr.child, "release", "--all")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stderr", res.Stderr, "lost its parent", "was released by its holder")

	if names := tr.w.fileNames(state.TombstonesDir); len(names) != 0 {
		t.Errorf("tombstones = %v", names)
	}
}

func TestReleaseEndsTheGrantForGood(t *testing.T) {
	tr := newTree(t)

	wantStatus(t, tr.w.run(tr.parent, "release", tr.parentID), exitcode.OK)

	res := tr.w.run(tr.parent, "revoke", tr.childID)
	wantStatus(t, res, exitcode.Refused)
	wantContains(t, "stderr", res.Stderr, "not a sub-lease")

	res = tr.w.run(tr.parent, "acquire", "--checkout", tr.repo)
	wantStatus(t, res, exitcode.Refused)
	wantContains(t, "stderr", res.Stderr, "child-agent", "sibling-agent")
}

func TestAForcedBreakOfAParentOrphansItsChildrenAndNamesTheBreaker(t *testing.T) {
	tr := newTree(t)

	res := tr.w.run(tr.third, "break", "--force", tr.parentID)
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "broken lease "+tr.parentID, "sub-lease "+tr.childID, "sub-lease "+tr.siblingID,
		"now stands alone")

	tr.w.wantStandsAlone(tr.childID)
	tr.w.wantStandsAlone(tr.siblingID)

	res = tr.w.run(tr.child, "release", "--all")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stderr", res.Stderr, "lost its parent", "was broken by")

	res = tr.w.poke(tr.parent)
	wantContains(t, "stderr", res.Stderr, "lease "+tr.parentID+" on "+tr.repo+" was broken by")
	wantAbsent(t, "stderr", res.Stderr, "revoked")

	if names := tr.w.fileNames(state.TombstonesDir); len(names) != 0 {
		t.Errorf("tombstones = %v, want the parent's delivered and none for the children", names)
	}
}

func TestAForcedBreakOfAChildReturnsItsKeysToTheParent(t *testing.T) {
	tr := newTree(t)

	res := tr.w.run(tr.third, "break", "--force", tr.childID)
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "broken lease "+tr.childID, "its keys went back to lease "+tr.parentID)

	// The breaker cannot claim the keys, because the parent's footprint covers them.
	wantStatus(t, tr.w.run(tr.third, "acquire", "--checkout", tr.repo, "--scope", "src"), exitcode.Refused)

	res = tr.w.poke(tr.parent)
	wantContains(t, "stderr", res.Stderr, "your sub-lease "+tr.childID, "was broken by", "Its keys are yours again")

	res = tr.w.run(tr.child, "release", tr.childID)
	wantStatus(t, res, exitcode.Broken)
	wantContains(t, "stderr", res.Stderr, "lease "+tr.childID, "was broken by")

	parentRec := tr.w.leaseByID(tr.parentID)
	if parentRec == nil || parentRec.Domain.Keys[0].Path != tr.repo || parentRec.Version != record.PathVersion {
		t.Errorf("the parent's record changed: %+v", parentRec)
	}
}

func TestASubLesseesReleaseReturnsItsKeysToTheParentWithANotice(t *testing.T) {
	tr := newTree(t)

	wantStatus(t, tr.w.run(tr.child, "release", tr.childID), exitcode.OK)

	res := tr.w.poke(tr.parent)
	wantContains(t, "stderr", res.Stderr, "your sub-lease "+tr.childID, "its sub-lessee released it", "Its keys are yours again")
	wantAbsent(t, "stderr", res.Stderr, tr.siblingID)

	res = tr.w.run(tr.parent, "acquire", "--checkout", tr.repo, "--scope", "src")
	wantStatus(t, res, exitcode.AlreadyCovered)
}

func TestADeadSubLesseeIsClearedWithANoticeToTheParent(t *testing.T) {
	t.Run("by a break", func(t *testing.T) {
		tr := newTree(t)
		tr.w.kill(300)

		res := tr.w.run(tr.third, "break", tr.childID)
		wantStatus(t, res, exitcode.OK)
		wantContains(t, "stdout", res.Stdout, "cleared lease "+tr.childID, "its keys went back to lease "+tr.parentID)

		res = tr.w.poke(tr.parent)
		wantContains(t, "stderr", res.Stderr, "your sub-lease "+tr.childID, "its holder died")
	})

	t.Run("by the parent's own refresh", func(t *testing.T) {
		tr := newTree(t)
		tr.w.kill(300)

		res := tr.w.run(tr.parent, "acquire", "--checkout", tr.repo)
		wantStatus(t, res, exitcode.Delegated)
		wantContains(t, "stderr", res.Stderr, "cleared", tr.childID)

		res = tr.w.poke(tr.parent)
		wantContains(t, "stderr", res.Stderr, "your sub-lease "+tr.childID, "its holder died")
	})
}

func TestAnUnrelatedGrantClearingADeadSubLeaseNotifiesItsParent(t *testing.T) {
	tr := newTree(t)

	// The sibling's lease is dead and sits under the keys the grant will carve.
	tr.w.kill(500)

	id := tr.w.requestOK(tr.third, tr.parentID, "--checkout", tr.repo, "--scope", "docs")
	wantStatus(t, tr.w.run(tr.parent, "grant", id), exitcode.OK)

	res := tr.w.poke(tr.parent)
	wantContains(t, "stderr", res.Stderr, "your sub-lease "+tr.siblingID, "its holder died")
}

func TestRevokeEndsASubLeaseAtOnceWithATombstone(t *testing.T) {
	tr := newTree(t)

	res := tr.w.run(tr.parent, "revoke", tr.childID)
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "ended sub-lease "+tr.childID, "child-agent", "its keys went back to lease "+tr.parentID)

	if tr.w.leaseByID(tr.childID) != nil {
		t.Error("the sub-lease survived its revoke")
	}

	tombs := tr.w.tombstones()
	if len(tombs) != 1 {
		t.Fatalf("tombstones = %d, want 1", len(tombs))
	}

	tomb := tombs[0]

	switch {
	case tomb.Cause != record.CauseRevoked:
		t.Errorf("cause = %q", tomb.Cause)
	case tomb.Kind != "":
		t.Errorf("kind = %q, want it left empty", tomb.Kind)
	case tomb.Version != record.ParentVersion:
		t.Errorf("version = %s", tomb.Version)
	case tomb.Lease != tr.childID || tomb.Holder.Name != "child-agent":
		t.Errorf("tombstone addressed to %+v for %s", tomb.Holder, tomb.Lease)
	case tomb.BrokenBy.Name != "parent-agent":
		t.Errorf("breaker = %+v, want the parent", tomb.BrokenBy)
	}

	res = tr.w.run(tr.child, "release", tr.childID)
	wantStatus(t, res, exitcode.Broken)
	wantContains(t, "stderr", res.Stderr, "lease "+tr.childID, "was revoked by", "the holder of its parent lease")

	res = tr.w.poke(tr.parent)
	wantAbsent(t, "stderr", res.Stderr, "sub-lease ended", "your sub-lease")

	if names := tr.w.noticesOf(record.CauseSubLeaseEnded); len(names) != 0 {
		t.Errorf("a revoke left notices: %v", names)
	}

	wantStatus(t, tr.w.run(tr.parent, "acquire", "--checkout", tr.repo, "--scope", "src"), exitcode.AlreadyCovered)
}

func TestRevokeDiscardsASubLeaseEndedNoticeLeftByACrashedRelease(t *testing.T) {
	tr := newTree(t)

	tr.w.crashAtLossNotice(t, tr.child, "release", tr.childID)

	if tr.w.leaseByID(tr.childID) == nil {
		t.Fatal("the crash happened after the removal")
	}

	wantStatus(t, tr.w.run(tr.parent, "revoke", tr.childID), exitcode.OK)

	res := tr.w.poke(tr.parent)
	wantAbsent(t, "stderr", res.Stderr, "your sub-lease", "released")

	if names := tr.w.noticesOf(record.CauseSubLeaseEnded); len(names) != 0 {
		t.Errorf("the revoke left notices: %v", names)
	}
}

func TestRevokeByAnyoneButTheParentsHolderIsRefused(t *testing.T) {
	tests := []struct {
		Name string
		Who  string
	}{
		{"a third party", "third"},
		{"the sub-lessee itself", "child"},
		{"a sibling", "sibling"},
	}

	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			tr := newTree(t)

			res := tr.w.run(tr.actor(test.Who), "revoke", tr.childID)
			wantStatus(t, res, exitcode.NotYours)
			wantContains(t, "stderr", res.Stderr, tr.parentID, "parent-agent")
			wantAbsent(t, "stderr", res.Stderr, "revoke", "--force")

			if tr.w.leaseByID(tr.childID) == nil || len(tr.w.fileNames(state.TombstonesDir)) != 0 {
				t.Error("a refused revoke changed something")
			}
		})
	}
}

func TestRevokeRefusals(t *testing.T) {
	tr := newTree(t)

	res := tr.w.run(tr.parent, "revoke", tr.parentID)
	wantStatus(t, res, exitcode.Refused)
	wantContains(t, "stderr", res.Stderr, "not a sub-lease")
	wantAbsent(t, "stderr", res.Stderr, "revoke")

	res = tr.w.run(tr.parent, "revoke", missingID)
	wantStatus(t, res, exitcode.NoSuchLease)
	wantAbsent(t, "stderr", res.Stderr, "revoke")

	wantStatus(t, tr.w.run(tr.parent, "revoke", "not-an-id"), exitcode.Usage)
}

func TestRevokingADeadSubLesseeWritesNoTombstone(t *testing.T) {
	tr := newTree(t)
	tr.w.kill(300)

	res := tr.w.run(tr.parent, "revoke", tr.childID)
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "no tombstone was written")

	if tr.w.leaseByID(tr.childID) != nil || len(tr.w.fileNames(state.TombstonesDir)) != 0 {
		t.Error("revoking a dead sub-lessee left a lease or a tombstone")
	}
}

func TestARequestWhoseParentIsReleasedIsDeclinedWithANotice(t *testing.T) {
	tr := newTree(t)

	id := tr.w.requestOK(tr.third, tr.parentID, "--checkout", tr.repo, "--scope", "lib")

	wantStatus(t, tr.w.run(tr.parent, "release", tr.parentID), exitcode.OK)

	res := tr.w.run(tr.third, "release", "--all")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stderr", res.Stderr, "your request "+id, "declined", "ended")

	if names := tr.w.fileNames(state.RequestsDir); len(names) != 0 {
		t.Errorf("requests = %v", names)
	}

	if names := tr.w.fileNames(state.TombstonesDir); len(names) != 0 {
		t.Errorf("tombstones = %v, want none: a notice is not a tombstone", names)
	}
}

func TestALockHolderReapsADeadHoldersNotices(t *testing.T) {
	tr := newTree(t)

	id := tr.w.requestOK(tr.third, tr.parentID, "--checkout", tr.repo, "--scope", "lib")
	wantStatus(t, tr.w.run(tr.parent, "decline", id), exitcode.OK)

	if names := tr.w.noticesOf(record.CauseRequestDeclined); len(names) != 1 {
		t.Fatalf("notices = %v, want the declined one waiting", names)
	}

	tr.w.kill(400)

	wantStatus(t, tr.w.run(tr.child, "acquire", "--checkout", tr.repo, "--scope", "src"), exitcode.OK)

	if names := tr.w.fileNames(state.NoticesDir); len(names) != 0 {
		t.Errorf("notices = %v, want a dead holder's reaped", names)
	}
}

func TestABinaryThatCannotRewriteTheChildWritesNoNotice(t *testing.T) {
	tr := newTree(t)

	tr.w.editJSON(filepath.Join(tr.w.leasesDir(), tr.childID+".json"), func(doc map[string]any) { doc["version"] = "1.5" })
	tr.w.kill(200)

	wantStatus(t, tr.w.run(tr.third, "acquire", "--checkout", tr.w.dir("other")), exitcode.OK)

	if names := tr.w.noticesOf(record.CauseParentEnded); len(names) != 1 || names[0] != tr.siblingID+"-parent-ended.json" {
		t.Errorf("notices = %v, want only the sibling's", names)
	}

	if rec := tr.w.leaseByID(tr.childID); rec.Parent != tr.parentID || rec.Version.String() != "1.5" {
		t.Errorf("the child was rewritten: %+v", rec)
	}
}

// The crash tests stop a call at a hook, then run it again. The sub-lessee or parent must hear of the
// loss exactly once across both runs.

func (w *world) crashAtLossNotice(t *testing.T, who caller, args ...string) {
	t.Helper()

	w.hooks = lease.Hooks{AfterLossNotice: func() error { return errKilled }}
	wantStatus(t, w.run(who, args...), exitcode.Error)
	w.hooks = lease.Hooks{}
}

// poke is a locked call that changes nothing and fails. It delivers the caller's tombstones and
// notices, which `list` does not.
func (w *world) poke(c caller) result {
	w.t.Helper()

	return w.run(c, "release", missingID)
}

func wantOnce(t *testing.T, res []result, sub string) {
	t.Helper()

	got := 0
	for _, r := range res {
		got += strings.Count(r.Stderr, sub)
	}

	if got != 1 {
		t.Errorf("%q was reported %d times, want once", sub, got)
	}
}

func TestACrashBetweenTheParentEndedNoticeAndTheRewriteYieldsOneNotice(t *testing.T) {
	tr := newTree(t)
	tr.w.kill(200)

	tr.w.crashAtLossNotice(t, tr.child, "acquire", "--checkout", tr.repo, "--scope", "src")

	if rec := tr.w.leaseByID(tr.childID); rec.Parent != tr.parentID {
		t.Fatalf("the crash happened after the rewrite: %+v", rec)
	}

	first := tr.w.run(tr.child, "acquire", "--checkout", tr.repo, "--scope", "src")
	wantStatus(t, first, exitcode.OK)

	second := tr.w.poke(tr.child)

	wantOnce(t, []result{first, second}, "your sub-lease "+tr.childID)
	tr.w.wantStandsAlone(tr.childID)
}

func TestACrashBetweenAParentsReleaseNoticesAndItsRemovalYieldsOneNoticePerChild(t *testing.T) {
	tr := newTree(t)

	tr.w.crashAtLossNotice(t, tr.parent, "release", tr.parentID)

	if tr.w.leaseByID(tr.parentID) == nil {
		t.Fatal("the crash happened after the removal")
	}

	wantStatus(t, tr.w.run(tr.parent, "release", tr.parentID), exitcode.OK)

	childRun := tr.w.poke(tr.child)
	childAgain := tr.w.poke(tr.child)
	siblingRun := tr.w.poke(tr.sibling)

	wantOnce(t, []result{childRun, childAgain}, "your sub-lease "+tr.childID)
	wantOnce(t, []result{siblingRun}, "your sub-lease "+tr.siblingID)
	tr.w.wantStandsAlone(tr.childID)
	tr.w.wantStandsAlone(tr.siblingID)
}

func TestACrashBetweenAParentsRemovalAndTheRewriteIsRepairedByTheDanglingRule(t *testing.T) {
	tr := newTree(t)

	wantStatus(t, tr.w.run(tr.parent, "release", tr.parentID), exitcode.OK)

	// Put a child back as a crash after the removal would have left it.
	tr.w.editJSON(filepath.Join(tr.w.leasesDir(), tr.childID+".json"), func(doc map[string]any) {
		doc["parent"] = tr.parentID
		doc["version"] = "1.2"
	})

	again := tr.w.poke(tr.child)

	res := tr.w.run(tr.child, "acquire", "--checkout", tr.repo, "--scope", "src")
	wantStatus(t, res, exitcode.OK)

	wantOnce(t, []result{again, res}, "your sub-lease "+tr.childID)
	tr.w.wantStandsAlone(tr.childID)
}

func TestACrashBetweenASubLeaseEndedNoticeAndTheRemovalYieldsOneNotice(t *testing.T) {
	for _, test := range []struct {
		Name string
		Args func(tr *tree) (caller, []string)
	}{
		{"a release", func(tr *tree) (caller, []string) { return tr.child, []string{"release", tr.childID} }},
		{"a forced break", func(tr *tree) (caller, []string) { return tr.third, []string{"break", "--force", tr.childID} }},
	} {
		t.Run(test.Name, func(t *testing.T) {
			tr := newTree(t)
			who, args := test.Args(tr)

			tr.w.crashAtLossNotice(t, who, args...)

			if tr.w.leaseByID(tr.childID) == nil {
				t.Fatal("the crash happened after the removal")
			}

			wantStatus(t, tr.w.run(who, args...), exitcode.OK)

			first := tr.w.poke(tr.parent)
			second := tr.w.poke(tr.parent)

			wantOnce(t, []result{first, second}, "your sub-lease "+tr.childID)
		})
	}
}

func TestACrashBetweenARevokeTombstoneAndTheRemovalYieldsOneTombstone(t *testing.T) {
	tr := newTree(t)

	tr.w.hooks = lease.Hooks{AfterRevokeTombstone: func() error { return errKilled }}
	wantStatus(t, tr.w.run(tr.parent, "revoke", tr.childID), exitcode.Error)
	tr.w.hooks = lease.Hooks{}

	if tr.w.leaseByID(tr.childID) == nil {
		t.Fatal("the crash happened after the removal")
	}

	wantStatus(t, tr.w.run(tr.parent, "revoke", tr.childID), exitcode.OK)

	if tombs := tr.w.tombstones(); len(tombs) != 1 {
		t.Fatalf("tombstones = %d, want 1", len(tombs))
	}

	first := tr.w.poke(tr.child)
	second := tr.w.poke(tr.child)

	wantOnce(t, []result{first, second}, "was revoked by")
}

func TestListWithoutTheLockDeliversNoParentEndedNotice(t *testing.T) {
	tr := newTree(t)
	tr.w.kill(200)

	wantStatus(t, tr.w.run(tr.third, "acquire", "--checkout", tr.w.dir("other")), exitcode.OK)

	release := tr.w.holdLock()
	defer release()

	res := tr.w.run(tr.child, "list")
	wantStatus(t, res, exitcode.OK)
	wantAbsent(t, "stderr", res.Stderr, "lost its parent")

	if names := tr.w.noticesOf(record.CauseParentEnded); len(names) != 2 {
		t.Errorf("notices = %v, want both kept for the next locked call", names)
	}
}

func TestBreakingAParentAndItsChildInOneCallLeavesNeitherBehind(t *testing.T) {
	w := newWorld(t)
	repo := w.dir("repo")
	w.dir("repo", "src")

	w.claude(200, "p")
	w.claude(300, "c")

	// The child's ID sorts first, so it is removed before its parent.
	childID := "01JZZZZZZZZZZZZZZZZZZZZZZA"
	parentID := "01JZZZZZZZZZZZZZZZZZZZZZZY"

	w.plant(plantSpec{ID: parentID, PID: 200, Paths: []string{repo}, Name: "parent-agent"})
	w.plant(plantSpec{ID: childID, PID: 300, Paths: []string{filepath.Join(repo, "src")}, Name: "child-agent", Parent: parentID})

	res := w.run(w.claude(400, "t"), "break", "--force", childID, parentID)
	wantStatus(t, res, exitcode.OK)
	wantAbsent(t, "stdout", res.Stdout, "now stands alone")

	if ids := w.leaseIDs(); len(ids) != 0 {
		t.Errorf("leases = %v, want a removed lease to stay removed", ids)
	}

	if names := w.noticesOf(record.CauseParentEnded); len(names) != 0 {
		t.Errorf("notices = %v, want none for a sub-lease that was removed first", names)
	}
}

func TestALossNoticeWaitsForTheLossItAnnounces(t *testing.T) {
	t.Run("a parent ended notice", func(t *testing.T) {
		tr := newTree(t)

		tr.w.crashAtLossNotice(t, tr.parent, "release", tr.parentID)

		early := tr.w.poke(tr.child)
		wantAbsent(t, "stderr", early.Stderr, "lost its parent")

		wantStatus(t, tr.w.run(tr.parent, "release", tr.parentID), exitcode.OK)

		first := tr.w.poke(tr.child)
		second := tr.w.poke(tr.child)

		wantOnce(t, []result{early, first, second}, "your sub-lease "+tr.childID)
	})

	t.Run("a sub-lease ended notice", func(t *testing.T) {
		tr := newTree(t)

		tr.w.crashAtLossNotice(t, tr.child, "release", tr.childID)

		early := tr.w.poke(tr.parent)
		wantAbsent(t, "stderr", early.Stderr, "your sub-lease")

		wantStatus(t, tr.w.run(tr.child, "release", tr.childID), exitcode.OK)

		first := tr.w.poke(tr.parent)
		second := tr.w.poke(tr.parent)

		wantOnce(t, []result{early, first, second}, "your sub-lease "+tr.childID)
	})
}

func TestAPendingLossNoticeIsReplacedByTheLossThatHappened(t *testing.T) {
	tr := newTree(t)

	tr.w.crashAtLossNotice(t, tr.parent, "release", tr.parentID)

	wantStatus(t, tr.w.run(tr.third, "break", "--force", tr.parentID), exitcode.OK)

	res := tr.w.poke(tr.child)
	wantContains(t, "stderr", res.Stderr, "was broken by")
	wantAbsent(t, "stderr", res.Stderr, "was released by its holder")
}

func TestAStalePendingLossNoticeIsDroppedNotDelivered(t *testing.T) {
	t.Run("a sub-lease ended notice after the parent was released", func(t *testing.T) {
		tr := newTree(t)

		tr.w.crashAtLossNotice(t, tr.child, "release", tr.childID)
		wantStatus(t, tr.w.run(tr.parent, "release", tr.parentID), exitcode.OK)
		wantStatus(t, tr.w.run(tr.child, "release", tr.childID), exitcode.OK)

		res := tr.w.poke(tr.parent)
		wantAbsent(t, "stderr", res.Stderr, "your sub-lease", "Its keys are yours again")

		if names := tr.w.noticesOf(record.CauseSubLeaseEnded); len(names) != 0 {
			t.Errorf("notices = %v, want the stale one dropped", names)
		}
	})

	t.Run("a parent ended notice after the sub-lessee released", func(t *testing.T) {
		tr := newTree(t)

		tr.w.crashAtLossNotice(t, tr.parent, "release", tr.parentID)
		wantStatus(t, tr.w.run(tr.child, "release", tr.childID), exitcode.OK)

		res := tr.w.poke(tr.child)
		wantAbsent(t, "stderr", res.Stderr, "lost its parent")

		for _, name := range tr.w.noticesOf(record.CauseParentEnded) {
			if name == tr.childID+"-parent-ended.json" {
				t.Errorf("notices = %v, want the stale one dropped", name)
			}
		}
	})
}
