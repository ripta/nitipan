package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/lease"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/state"
)

func (w *world) recallPath(id string) string {
	return filepath.Join(w.state, state.RecallsDir, id+".json")
}

// recallOf reads the recall of a sub-lease, or nil when it has none.
func (w *world) recallOf(id string) *record.Recall {
	w.t.Helper()

	data, err := os.ReadFile(w.recallPath(id))
	if os.IsNotExist(err) {
		return nil
	}

	if err != nil {
		w.t.Fatal(err)
	}

	rc, err := record.DecodeRecall(data)
	if err != nil {
		w.t.Fatal(err)
	}

	return rc
}

// noticeOf reads the one published notice of a cause, or nil when there is none.
func (w *world) noticeOf(cause string) *record.Notice {
	w.t.Helper()

	names := w.noticesOf(cause)
	if len(names) == 0 {
		return nil
	}

	if len(names) > 1 {
		w.t.Fatalf("notices of %s = %v, want at most one", cause, names)
	}

	data, err := os.ReadFile(filepath.Join(w.state, state.NoticesDir, names[0]))
	if err != nil {
		w.t.Fatal(err)
	}

	n, err := record.DecodeNotice(data)
	if err != nil {
		w.t.Fatal(err)
	}

	return n
}

// recall runs the parent's recall of the tree's child.
func (tr *tree) recall(deadline string) result {
	tr.w.t.Helper()

	return tr.w.run(tr.parent, "recall", tr.childID, "--deadline", deadline)
}

func (tr *tree) recallOK(deadline string) {
	tr.w.t.Helper()

	wantStatus(tr.w.t, tr.recall(deadline), exitcode.OK)
}

// pass moves the clock forward.
func (w *world) pass(d time.Duration) { w.now = w.now.Add(d) }

func (tr *tree) wantChildEnded() {
	tr.w.t.Helper()

	if tr.w.leaseByID(tr.childID) != nil {
		tr.w.t.Error("the sub-lease still stands")
	}

	if tr.w.recallOf(tr.childID) != nil {
		tr.w.t.Error("the recall record still stands")
	}
}

func TestRecallWritesTheRecordAndTheNoticeAndLeavesTheSubLeaseUnchanged(t *testing.T) {
	tr := newTree(t)

	before, err := os.ReadFile(filepath.Join(tr.w.leasesDir(), tr.childID+".json"))
	if err != nil {
		t.Fatal(err)
	}

	res := tr.recall("30m")
	wantStatus(t, res, exitcode.OK)

	deadline := tr.w.now.Add(30 * time.Minute)

	wantContains(t, "stdout", res.Stdout, "recalled sub-lease "+tr.childID, "child-agent", deadline.Format(time.RFC3339),
		"out of band", "nitipan acquire --wait", "--checkout "+tr.repo)

	rc := tr.w.recallOf(tr.childID)

	switch {
	case rc == nil:
		t.Fatal("no recall record")
	case rc.State != record.RecallAnnounced || rc.Parent != tr.parentID || !rc.Deadline.Equal(deadline):
		t.Errorf("recall = %+v", rc)
	case rc.Version != record.RecallVersion || rc.Footprint.Kind != record.NoneFootprint().Kind:
		t.Errorf("recall version %s footprint %+v", rc.Version, rc.Footprint)
	case rc.Holder.Name != "parent-agent":
		t.Errorf("recall holder = %+v, want the parent's", rc.Holder)
	}

	n := tr.w.noticeOf(record.CauseRecalled)
	if n == nil || n.Version != record.RecallVersion || n.Target != tr.childID || n.Holder.Name != "child-agent" {
		t.Fatalf("notice = %+v", n)
	}

	wantContains(t, "notice", n.Detail, deadline.Format(time.RFC3339), "Finish, commit, and release")

	after, err := os.ReadFile(filepath.Join(tr.w.leasesDir(), tr.childID+".json"))
	if err != nil {
		t.Fatal(err)
	}

	if string(before) != string(after) {
		t.Errorf("the sub-lease's record changed:\n%s\n%s", before, after)
	}

	heard := tr.w.poke(tr.child)
	wantContains(t, "stderr", heard.Stderr, "your sub-lease "+tr.childID, "was recalled", deadline.Format(time.RFC3339))
	wantOnce(t, []result{heard, tr.w.poke(tr.child)}, "was recalled")
}

func TestRecallRefusals(t *testing.T) {
	tests := []struct {
		Name     string
		Args     func(tr *tree) []string
		Who      string
		Status   int
		Contains []string
	}{
		{
			Name: "a third party", Who: "third", Status: exitcode.NotYours, Contains: []string{"parent-agent"},
			Args: func(tr *tree) []string { return []string{"recall", tr.childID, "--deadline", "30m"} },
		},
		{
			Name: "the sub-lessee itself", Who: "child", Status: exitcode.NotYours, Contains: []string{"parent-agent"},
			Args: func(tr *tree) []string { return []string{"recall", tr.childID, "--deadline", "30m"} },
		},
		{
			Name: "a lease with no parent", Who: "parent", Status: exitcode.Refused, Contains: []string{"not a sub-lease"},
			Args: func(tr *tree) []string { return []string{"recall", tr.parentID, "--deadline", "30m"} },
		},
		{
			Name: "a lease that does not exist", Who: "parent", Status: exitcode.NoSuchLease,
			Args: func(*tree) []string { return []string{"recall", missingID, "--deadline", "30m"} },
		},
		{
			Name: "no deadline", Who: "parent", Status: exitcode.Usage,
			Contains: []string{"--deadline is required", "finish, commit, and release"},
			Args:     func(tr *tree) []string { return []string{"recall", tr.childID} },
		},
		{
			Name: "a zero deadline", Who: "parent", Status: exitcode.Usage, Contains: []string{"positive duration"},
			Args: func(tr *tree) []string { return []string{"recall", tr.childID, "--deadline", "0s"} },
		},
		{
			Name: "a negative deadline", Who: "parent", Status: exitcode.Usage, Contains: []string{"positive duration"},
			Args: func(tr *tree) []string { return []string{"recall", tr.childID, "--deadline", "-5m"} },
		},
		{
			Name: "a deadline that is not a duration", Who: "parent", Status: exitcode.Usage, Contains: []string{"duration"},
			Args: func(tr *tree) []string { return []string{"recall", tr.childID, "--deadline", "soon"} },
		},
		{
			Name: "a cancel with a deadline", Who: "parent", Status: exitcode.Usage, Contains: []string{"--cancel takes no --deadline"},
			Args: func(tr *tree) []string { return []string{"recall", tr.childID, "--cancel", "--deadline", "5m"} },
		},
		{
			Name: "a cancel by a third party", Who: "third", Status: exitcode.NotYours, Contains: []string{"parent-agent"},
			Args: func(tr *tree) []string { return []string{"recall", tr.childID, "--cancel"} },
		},
		{
			Name: "a cancel by the sub-lessee itself", Who: "child", Status: exitcode.NotYours, Contains: []string{"parent-agent"},
			Args: func(tr *tree) []string { return []string{"recall", tr.childID, "--cancel"} },
		},
		{
			Name: "a cancel with no recall", Who: "parent", Status: exitcode.NoSuchLease, Contains: []string{"no recall to cancel"},
			Args: func(tr *tree) []string { return []string{"recall", tr.childID, "--cancel"} },
		},
	}

	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			tr := newTree(t)

			res := tr.w.run(tr.actor(test.Who), test.Args(tr)...)
			wantStatus(t, res, test.Status)
			wantContains(t, "stderr", res.Stderr, test.Contains...)
			wantAbsent(t, "stderr", res.Stderr, "--force")

			if names := tr.w.fileNames(state.RecallsDir); len(names) != 0 {
				t.Errorf("a refused call wrote recalls %v", names)
			}
		})
	}
}

func TestRecallOfAGoneOrUnreadableParentIsRefused(t *testing.T) {
	t.Run("gone", func(t *testing.T) {
		w := newWorld(t)
		repo := w.dir("repo")
		w.dir("repo", "src")
		who := w.claude(200, "p")

		w.claude(300, "c")

		childID := w.plant(plantSpec{PID: 300, Paths: []string{filepath.Join(repo, "src")}, Parent: missingID})

		res := w.run(who, "recall", childID, "--deadline", "30m")
		wantStatus(t, res, exitcode.Refused)
		wantContains(t, "stderr", res.Stderr, "not a sub-lease")
	})

	t.Run("unreadable", func(t *testing.T) {
		tr := newTree(t)
		tr.w.editJSON(filepath.Join(tr.w.leasesDir(), tr.parentID+".json"), func(doc map[string]any) { doc["version"] = "2.0" })

		res := tr.recall("30m")
		wantStatus(t, res, exitcode.Refused)
		wantContains(t, "stderr", res.Stderr, "gone or cannot be read")
	})
}

func TestRecallRefusesToRewriteARecallOfAHigherMinor(t *testing.T) {
	tr := newTree(t)
	tr.recallOK("30m")

	tr.w.editJSON(tr.w.recallPath(tr.childID), func(doc map[string]any) { doc["version"] = "1.5" })

	for _, args := range [][]string{{"recall", tr.childID, "--deadline", "10m"}, {"recall", tr.childID, "--cancel"}} {
		res := tr.w.run(tr.parent, args...)
		wantStatus(t, res, exitcode.Unusable)
		wantContains(t, "stderr", res.Stderr, "upgrade nitipan")
	}

	if rc := tr.w.recallOf(tr.childID); rc == nil || rc.Version.String() != "1.5" {
		t.Errorf("the recall was rewritten: %+v", rc)
	}
}

func TestACrashBetweenTheRecallRecordAndItsNoticeLeavesTheCurrentDeadline(t *testing.T) {
	tests := []struct {
		Name   string
		Second bool
	}{
		{"a first recall", false},
		{"a second recall", true},
	}

	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			tr := newTree(t)

			if test.Second {
				tr.recallOK("2h")
			}

			tr.w.hooks = lease.Hooks{AfterRecallRecord: func() error { return errKilled }}
			wantStatus(t, tr.recall("30m"), exitcode.Error)
			tr.w.hooks = lease.Hooks{}

			if rc := tr.w.recallOf(tr.childID); rc == nil || rc.State != record.RecallAnnouncing {
				t.Fatalf("recall after the crash = %+v, want announcing", rc)
			}

			wantStatus(t, tr.w.run(tr.third, "acquire", "--checkout", tr.w.dir("other")), exitcode.OK)

			want := tr.w.now.Add(30 * time.Minute).Format(time.RFC3339)

			if rc := tr.w.recallOf(tr.childID); rc == nil || rc.State != record.RecallAnnounced {
				t.Errorf("recall after the next locked call = %+v, want announced", rc)
			}

			heard := tr.w.poke(tr.child)
			wantContains(t, "stderr", heard.Stderr, want)
			wantAbsent(t, "stderr", heard.Stderr, tr.w.now.Add(2*time.Hour).Format(time.RFC3339))
		})
	}
}

func TestACrashBetweenTheRecallNoticeAndTheAnnouncedMarkTellsOnce(t *testing.T) {
	tr := newTree(t)

	tr.w.hooks = lease.Hooks{AfterRecallNotice: func() error { return errKilled }}
	wantStatus(t, tr.recall("30m"), exitcode.Error)
	tr.w.hooks = lease.Hooks{}

	wantStatus(t, tr.w.run(tr.third, "acquire", "--checkout", tr.w.dir("other")), exitcode.OK)

	if rc := tr.w.recallOf(tr.childID); rc == nil || rc.State != record.RecallAnnounced {
		t.Errorf("recall = %+v, want announced", rc)
	}

	wantOnce(t, []result{tr.w.poke(tr.child), tr.w.poke(tr.child)}, "was recalled")
}

func TestASubLesseeThatReleasesBeforeTheDeadlineReturnsItsKeysToTheParent(t *testing.T) {
	tr := newTree(t)
	tr.recallOK("30m")

	tr.w.pass(10 * time.Minute)

	wantStatus(t, tr.w.run(tr.child, "release", tr.childID), exitcode.OK)

	heard := tr.w.poke(tr.parent)
	wantContains(t, "stderr", heard.Stderr, "your sub-lease "+tr.childID, "its sub-lessee released it", "Its keys are yours again")
	wantStatus(t, tr.w.run(tr.parent, "acquire", "--checkout", tr.repo, "--scope", "src"), exitcode.AlreadyCovered)

	tr.wantChildEnded()

	if names := tr.w.noticesOf(record.CauseRecalled); len(names) != 0 {
		t.Errorf("recalled notices = %v, want them dropped with the recall", names)
	}

	if tombs := tr.w.tombstones(); len(tombs) != 0 {
		t.Errorf("a release before the deadline left tombstones: %v", tombs)
	}
}

func TestTheFirstLockedCallPastTheDeadlineEndsTheSubLease(t *testing.T) {
	tr := newTree(t)
	tr.recallOK("30m")

	tr.w.pass(29 * time.Minute)
	wantStatus(t, tr.w.run(tr.third, "acquire", "--checkout", tr.w.dir("other")), exitcode.OK)

	if tr.w.leaseByID(tr.childID) == nil {
		t.Fatal("a call before the deadline ended the sub-lease")
	}

	tr.w.pass(2 * time.Minute)

	res := tr.w.run(tr.third, "list")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stderr", res.Stderr, "sub-lease "+tr.childID, "ended", "deadline of its recall")
	tr.wantChildEnded()

	tombs := tr.w.tombstones()
	if len(tombs) != 1 {
		t.Fatalf("tombstones = %d, want 1", len(tombs))
	}

	switch tomb := tombs[0]; {
	case tomb.Cause != record.CauseRecallExpired || tomb.Version != record.RecallVersion || tomb.Kind != "":
		t.Errorf("tombstone = %+v", tomb)
	case tomb.Lease != tr.childID || tomb.Holder.Name != "child-agent":
		t.Errorf("tombstone addressed to %+v for %s", tomb.Holder, tomb.Lease)
	case tomb.BrokenBy.Name != "parent-agent" || tomb.Anonymous:
		t.Errorf("breaker = %+v anonymous %v, want the parent's holder", tomb.BrokenBy, tomb.Anonymous)
	}

	heard := tr.w.run(tr.child, "release", tr.childID)
	wantStatus(t, heard, exitcode.Broken)
	wantContains(t, "stderr", heard.Stderr, "lease "+tr.childID, "was recalled by", "the holder of its parent lease")

	told := tr.w.poke(tr.parent)
	wantContains(t, "stderr", told.Stderr, "your sub-lease "+tr.childID, "the deadline of its recall passed", "Its keys are yours again")
	wantStatus(t, tr.w.run(tr.parent, "acquire", "--checkout", tr.repo, "--scope", "src"), exitcode.AlreadyCovered)
}

func TestAParentWhoseOwnCallComesJustAfterTheDeadlineIsToldWhy(t *testing.T) {
	tr := newTree(t)
	tr.recallOK("30m")

	tr.w.pass(31 * time.Minute)

	res := tr.w.run(tr.parent, "recall", tr.childID, "--cancel")
	wantStatus(t, res, exitcode.NoSuchLease)
	wantContains(t, "stderr", res.Stderr, "your sub-lease "+tr.childID, "the deadline of its recall passed")
}

func TestACrashDuringAnExpiryTellsEachSideOnce(t *testing.T) {
	tests := []struct {
		Name  string
		Hooks lease.Hooks
	}{
		{"after the tombstone", lease.Hooks{AfterExpiryTombstone: func() error { return errKilled }}},
		{"after the parent's notice", lease.Hooks{AfterLossNotice: func() error { return errKilled }}},
		{"after the sub-lease is removed", lease.Hooks{AfterExpiryRemoval: func() error { return errKilled }}},
	}

	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			tr := newTree(t)
			tr.recallOK("30m")
			tr.w.pass(time.Hour)

			tr.w.hooks = test.Hooks
			wantStatus(t, tr.w.poke(tr.third), exitcode.Error)
			tr.w.hooks = lease.Hooks{}

			wantStatus(t, tr.w.run(tr.third, "list"), exitcode.OK)
			tr.wantChildEnded()

			if tombs := tr.w.tombstones(); len(tombs) != 1 {
				t.Errorf("tombstones = %d, want 1", len(tombs))
			}

			wantOnce(t, []result{tr.w.poke(tr.child), tr.w.poke(tr.child)}, "was recalled by")
			wantOnce(t, []result{tr.w.poke(tr.parent), tr.w.poke(tr.parent)}, "your sub-lease "+tr.childID)
		})
	}
}

func TestAnExpiryReusesATombstoneACrashedRevokeLeft(t *testing.T) {
	tr := newTree(t)
	tr.recallOK("30m")

	tr.w.hooks = lease.Hooks{AfterRevokeTombstone: func() error { return errKilled }}
	wantStatus(t, tr.w.run(tr.parent, "revoke", tr.childID), exitcode.Error)
	tr.w.hooks = lease.Hooks{}

	tr.w.pass(time.Hour)
	wantStatus(t, tr.w.run(tr.third, "list"), exitcode.OK)
	tr.wantChildEnded()

	if tombs := tr.w.tombstones(); len(tombs) != 1 || tombs[0].Cause != record.CauseRevoked {
		t.Fatalf("tombstones = %+v, want the revoke's alone", tombs)
	}

	wantOnce(t, []result{tr.w.poke(tr.child), tr.w.poke(tr.child)}, "was revoked by")
}

func TestAParentThatEndsBeforeTheDeadlineCancelsTheRecall(t *testing.T) {
	tests := []struct {
		Name string
		End  func(tr *tree)
	}{
		{"dies", func(tr *tree) { tr.w.kill(200) }},
		{"releases", func(tr *tree) { wantStatus(tr.w.t, tr.w.run(tr.parent, "release", tr.parentID), exitcode.OK) }},
		{"is broken", func(tr *tree) {
			wantStatus(tr.w.t, tr.w.run(tr.third, "break", "--force", tr.parentID), exitcode.OK)
		}},
	}

	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			tr := newTree(t)
			tr.recallOK("30m")

			test.End(tr)
			tr.w.pass(time.Hour)

			wantStatus(t, tr.w.run(tr.third, "list"), exitcode.OK)

			rec := tr.w.leaseByID(tr.childID)
			if rec == nil || rec.Parent != "" {
				t.Fatalf("the sub-lease = %+v, want it standing alone", rec)
			}

			if tr.w.recallOf(tr.childID) != nil || len(tr.w.noticesOf(record.CauseRecalled)) != 0 {
				t.Error("the recall or its notice survived the parent")
			}

			for _, tomb := range tr.w.tombstones() {
				if tomb.Lease == tr.childID {
					t.Errorf("tombstone for the sub-lease = %+v, want none", tomb)
				}
			}

			heard := tr.w.poke(tr.child)
			wantContains(t, "stderr", heard.Stderr, "lost its parent")
			wantAbsent(t, "stderr", heard.Stderr, "was recalled")
		})
	}
}

func TestARecallWhoseParentRecordIsUnusableEndsNoChildAndIsEnforcedOnceItReadsAgain(t *testing.T) {
	tr := newTree(t)
	tr.recallOK("30m")
	tr.w.pass(time.Hour)

	path := filepath.Join(tr.w.leasesDir(), tr.parentID+".json")

	good, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	tr.w.editJSON(path, func(doc map[string]any) { doc["version"] = "2.0" })

	res := tr.w.poke(tr.third)
	wantContains(t, "stderr", res.Stderr, "recall of sub-lease "+tr.childID, "is not enforced", "parent lease record")

	if _, err := os.Stat(filepath.Join(tr.w.leasesDir(), tr.childID+".json")); err != nil || tr.w.recallOf(tr.childID) == nil {
		t.Fatalf("an unusable parent let a call end the sub-lease or reap its recall: %v", err)
	}

	if err := os.WriteFile(path, good, 0o600); err != nil {
		t.Fatal(err)
	}

	wantStatus(t, tr.w.run(tr.third, "list"), exitcode.OK)
	tr.wantChildEnded()
}

func TestARecallThatIsNoLongerInForceIsRemovedByTheNextLockedCall(t *testing.T) {
	tests := []struct {
		Name string
		End  func(tr *tree)
	}{
		{"the sub-lessee released", func(tr *tree) { wantStatus(tr.w.t, tr.w.run(tr.child, "release", tr.childID), exitcode.OK) }},
		{"the sub-lease was broken", func(tr *tree) {
			wantStatus(tr.w.t, tr.w.run(tr.third, "break", "--force", tr.childID), exitcode.OK)
		}},
		{"the sub-lease was revoked", func(tr *tree) { wantStatus(tr.w.t, tr.w.run(tr.parent, "revoke", tr.childID), exitcode.OK) }},
		{"a dead sub-lessee's lease was cleared", func(tr *tree) {
			tr.w.kill(300)
			wantStatus(tr.w.t, tr.w.run(tr.parent, "acquire", "--checkout", tr.repo), exitcode.Delegated)
		}},
		{"an older nitipan rewrote the sub-lease without its parent", func(tr *tree) {
			tr.w.editJSON(filepath.Join(tr.w.leasesDir(), tr.childID+".json"), func(doc map[string]any) {
				delete(doc, "parent")
				doc["version"] = "1.0"
			})
		}},
	}

	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			tr := newTree(t)
			tr.recallOK("30m")

			test.End(tr)

			wantStatus(t, tr.w.run(tr.third, "list"), exitcode.OK)

			if tr.w.recallOf(tr.childID) != nil {
				t.Error("the recall record survived")
			}

			if names := tr.w.noticesOf(record.CauseRecalled); len(names) != 0 {
				t.Errorf("recalled notices = %v, want them dropped with the recall", names)
			}
		})
	}
}

func TestASecondRecallReplacesTheDeadlineAndTheNotice(t *testing.T) {
	tr := newTree(t)
	tr.recallOK("2h")
	tr.recallOK("10m")

	want := tr.w.now.Add(10 * time.Minute)

	if rc := tr.w.recallOf(tr.childID); rc == nil || !rc.Deadline.Equal(want) {
		t.Fatalf("recall = %+v, want the earlier deadline", rc)
	}

	n := tr.w.noticeOf(record.CauseRecalled)
	if n == nil {
		t.Fatal("no recalled notice")
	}

	wantContains(t, "notice", n.Detail, want.Format(time.RFC3339))
	wantAbsent(t, "notice", n.Detail, tr.w.now.Add(2*time.Hour).Format(time.RFC3339))

	tr.recallOK("3h")

	if rc := tr.w.recallOf(tr.childID); rc == nil || !rc.Deadline.Equal(tr.w.now.Add(3*time.Hour)) {
		t.Errorf("recall = %+v, want the later deadline", rc)
	}
}

func TestCancelRemovesTheRecallAndItsNoticeAndTellsTheSubLesseeOnce(t *testing.T) {
	tr := newTree(t)
	tr.recallOK("30m")

	res := tr.w.run(tr.parent, "recall", tr.childID, "--cancel")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "cancelled the recall of sub-lease "+tr.childID, "child-agent")

	if tr.w.recallOf(tr.childID) != nil || len(tr.w.noticesOf(record.CauseRecalled)) != 0 {
		t.Error("the recall or its notice survived the cancel")
	}

	if tr.w.noticeOf(record.CauseRecallCancelled) == nil {
		t.Fatal("no cancelled notice")
	}

	heard := tr.w.poke(tr.child)
	wantContains(t, "stderr", heard.Stderr, "was cancelled")
	wantAbsent(t, "stderr", heard.Stderr, "was recalled")
	wantOnce(t, []result{heard, tr.w.poke(tr.child)}, "was cancelled")

	tr.w.pass(time.Hour)
	wantStatus(t, tr.w.run(tr.third, "list"), exitcode.OK)

	if tr.w.leaseByID(tr.childID) == nil {
		t.Error("a cancelled recall ended the sub-lease")
	}
}

func TestALaterRecallRemovesThePendingCancelledNotice(t *testing.T) {
	tr := newTree(t)
	tr.recallOK("30m")
	wantStatus(t, tr.w.run(tr.parent, "recall", tr.childID, "--cancel"), exitcode.OK)

	tr.recallOK("20m")

	if names := tr.w.noticesOf(record.CauseRecallCancelled); len(names) != 0 {
		t.Errorf("cancelled notices = %v, want the new recall to remove them", names)
	}

	heard := tr.w.poke(tr.child)
	wantContains(t, "stderr", heard.Stderr, "was recalled")
	wantAbsent(t, "stderr", heard.Stderr, "was cancelled")
}

func TestDeliveryDropsACancelledNoticeWhileARecallIsInForce(t *testing.T) {
	tr := newTree(t)
	tr.recallOK("30m")
	wantStatus(t, tr.w.run(tr.parent, "recall", tr.childID, "--cancel"), exitcode.OK)

	names := tr.w.noticesOf(record.CauseRecallCancelled)
	if len(names) != 1 {
		t.Fatalf("cancelled notices = %v", names)
	}

	path := filepath.Join(tr.w.state, state.NoticesDir, names[0])

	stale, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	tr.recallOK("20m")

	if err := os.WriteFile(path, stale, 0o600); err != nil {
		t.Fatal(err)
	}

	heard := tr.w.poke(tr.child)
	wantContains(t, "stderr", heard.Stderr, "was recalled")
	wantAbsent(t, "stderr", heard.Stderr, "was cancelled")

	if names := tr.w.noticesOf(record.CauseRecallCancelled); len(names) != 0 {
		t.Errorf("the stale notice was kept: %v", names)
	}
}

func TestACrashAtEachStepOfACancelLeavesTheRecallCancelledAndTheSubLesseeToldOnce(t *testing.T) {
	tests := []struct {
		Name  string
		Hooks lease.Hooks
	}{
		{"after the cancelling mark", lease.Hooks{AfterCancelMark: func() error { return errKilled }}},
		{"after the recalled notice is dropped", lease.Hooks{AfterCancelDrop: func() error { return errKilled }}},
		{"after the cancelled notice", lease.Hooks{AfterCancelNotice: func() error { return errKilled }}},
	}

	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			tr := newTree(t)
			tr.recallOK("30m")

			tr.w.hooks = test.Hooks
			wantStatus(t, tr.w.run(tr.parent, "recall", tr.childID, "--cancel"), exitcode.Error)
			tr.w.hooks = lease.Hooks{}

			if rc := tr.w.recallOf(tr.childID); rc == nil || rc.State != record.RecallCancelling {
				t.Fatalf("recall after the crash = %+v, want cancelling", rc)
			}

			tr.w.pass(time.Hour)
			wantStatus(t, tr.w.run(tr.third, "list"), exitcode.OK)

			if tr.w.recallOf(tr.childID) != nil || tr.w.leaseByID(tr.childID) == nil {
				t.Error("the cancel was not finished, or the cancelling recall ended the sub-lease")
			}

			first, second := tr.w.poke(tr.child), tr.w.poke(tr.child)
			wantOnce(t, []result{first, second}, "was cancelled")
			wantAbsent(t, "stderr", first.Stderr+second.Stderr, "was recalled")
		})
	}
}

func TestARecallOnACancellingRecallFinishesTheCancelFirst(t *testing.T) {
	tr := newTree(t)
	tr.recallOK("30m")

	tr.w.hooks = lease.Hooks{AfterCancelMark: func() error { return errKilled }}
	wantStatus(t, tr.w.run(tr.parent, "recall", tr.childID, "--cancel"), exitcode.Error)
	tr.w.hooks = lease.Hooks{}

	tr.recallOK("20m")

	if rc := tr.w.recallOf(tr.childID); rc == nil || rc.State != record.RecallAnnounced || !rc.Deadline.Equal(tr.w.now.Add(20*time.Minute)) {
		t.Errorf("recall = %+v, want the new one", rc)
	}

	if names := tr.w.noticesOf(record.CauseRecallCancelled); len(names) != 0 {
		t.Errorf("cancelled notices = %v", names)
	}

	heard := tr.w.poke(tr.child)
	wantContains(t, "stderr", heard.Stderr, "was recalled")
	wantAbsent(t, "stderr", heard.Stderr, "was cancelled")
}

func TestRecallOfADeadSubLesseeRemovesTheSubLeaseAtOnce(t *testing.T) {
	tr := newTree(t)
	tr.w.kill(300)

	res := tr.recall("30m")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stdout", res.Stdout, "no recall was written", "no tombstone")

	if tr.w.leaseByID(tr.childID) != nil || tr.w.recallOf(tr.childID) != nil || len(tr.w.fileNames(state.TombstonesDir)) != 0 {
		t.Error("recalling a dead sub-lessee left a lease, a recall, or a tombstone")
	}
}

func TestARecallRecordNitipanCannotUseEndsNoChildAndIsReported(t *testing.T) {
	tr := newTree(t)

	if err := os.MkdirAll(filepath.Join(tr.w.state, state.RecallsDir), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(tr.w.recallPath(tr.childID), []byte("not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tr.w.pass(24 * time.Hour)

	res := tr.w.poke(tr.third)
	wantContains(t, "stderr", res.Stderr, "recall record", tr.childID+".json", "ends no sub-lease")

	if tr.w.leaseByID(tr.childID) == nil {
		t.Error("an unusable recall record ended the sub-lease")
	}

	res = tr.w.run(tr.parent, "recall", tr.childID, "--deadline", "30m")
	wantStatus(t, res, exitcode.Unusable)
	wantContains(t, "stderr", res.Stderr, "cannot be replaced")
}

func TestAnOlderBinaryLeavesTheRecallAloneAndCanStillRewriteTheSubLease(t *testing.T) {
	tr := newTree(t)
	tr.recallOK("30m")

	rec := tr.w.leaseByID(tr.childID)
	if rec.Version != record.ParentVersion {
		t.Errorf("sub-lease version = %s, want it kept at %s so an older binary can rewrite it", rec.Version, record.ParentVersion)
	}

	if err := rec.Rewritable(); err != nil {
		t.Errorf("an older binary could not rewrite the sub-lease: %v", err)
	}
}

func TestListShowsAPendingRecallOnItsSubLease(t *testing.T) {
	tr := newTree(t)
	tr.recallOK("30m")

	deadline := tr.w.now.Add(30 * time.Minute)

	for _, locked := range []bool{true, false} {
		if !locked {
			defer tr.w.holdLock()()
		}

		doc := tr.w.listJSON(tr.third)

		if doc["schema_version"] != "1.6" {
			t.Errorf("schema_version = %v", doc["schema_version"])
		}

		rc, ok := leaseByID(t, doc, tr.childID)["recall"].(map[string]any)
		if !ok || rc["parent"] != tr.parentID || rc["deadline"] != deadline.Format(time.RFC3339) {
			t.Errorf("locked %v: recall = %v", locked, rc)
		}

		if _, has := leaseByID(t, doc, tr.siblingID)["recall"]; has {
			t.Error("a sub-lease with no recall shows one")
		}
	}

	res := tr.w.run(tr.third, "list")
	wantContains(t, "stdout", res.Stdout, "recalled by the holder of "+tr.parentID, deadline.Format(time.RFC3339))
}

func TestAnUnlockedListShowsAChildPastItsDeadlineAsEndedWithTheParentAsOwner(t *testing.T) {
	tr := newTree(t)
	tr.recallOK("30m")
	tr.w.pass(time.Hour)

	defer tr.w.holdLock()()

	res := tr.w.run(tr.third, "list", "--json", "--checkout", tr.repo, "--scope", "src")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stderr", res.Stderr, "counts as ended", "The next locked nitipan call removes it")

	doc := decodeStdout(t, res)

	for _, l := range leasesOf(t, doc) {
		if l["id"] == tr.childID {
			t.Errorf("the ended sub-lease is listed: %v", l)
		}
	}

	parent := leaseByID(t, doc, tr.parentID)
	if parent["owner"] != true {
		t.Errorf("owner = %v, want the parent to own the match", parent["owner"])
	}

	if grantor, _ := parent["grantor_of"].([]any); len(grantor) != 0 {
		t.Errorf("grantor_of = %v, want none", grantor)
	}

	if tr.w.leaseByID(tr.childID) == nil {
		t.Error("an unlocked list removed the sub-lease")
	}
}

func TestAnUnlockedListReportsAnUnusableParentAndEndsNothing(t *testing.T) {
	tr := newTree(t)
	tr.recallOK("30m")
	tr.w.pass(time.Hour)

	tr.w.editJSON(filepath.Join(tr.w.leasesDir(), tr.parentID+".json"), func(doc map[string]any) { doc["version"] = "2.0" })

	defer tr.w.holdLock()()

	res := tr.w.run(tr.third, "list", "--json")
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stderr", res.Stderr, "recall of sub-lease "+tr.childID, "not enforced")

	leaseByID(t, decodeStdout(t, res), tr.childID)
}

// handoff is a parent that delegated all of its keys.
func newHandoff(t *testing.T) *tree {
	t.Helper()

	w := newWorld(t)

	tr := &tree{
		w:      w,
		repo:   w.dir("repo"),
		parent: w.claude(200, "p"),
		child:  w.claude(300, "c"),
		third:  w.claude(400, "t"),
	}

	w.dir("repo", "src")

	tr.parentID = w.acquireOK(tr.parent, "--checkout", tr.repo, "--scope", "src", "--name", "parent-agent")
	tr.childID = w.delegate(tr.child, tr.parent, tr.parentID, "src", "--name", "child-agent")

	return tr
}

// printedWait pulls the acquire arguments out of the line recall prints for taking the keys back.
func printedWait(t *testing.T, out string) []string {
	t.Helper()

	for _, line := range strings.Split(out, "\n") {
		if args, found := strings.CutPrefix(line, "To take the keys back, run: nitipan "); found {
			return strings.Fields(args)
		}
	}

	t.Fatalf("recall printed no wait command:\n%s", out)

	return nil
}

func TestAcquireWaitUnderARecallReturnsTheKeysAtTheDeadlineAfterAFullHandoff(t *testing.T) {
	tr := newHandoff(t)

	res := tr.recall("30m")
	wantStatus(t, res, exitcode.OK)

	args := printedWait(t, res.Stdout)

	if args[0] != "acquire" || args[1] != "--wait" {
		t.Fatalf("printed command = %v", args)
	}

	waited, err := time.ParseDuration(args[2])
	if err != nil || waited <= 30*time.Minute {
		t.Fatalf("printed wait = %q, want a wait that covers the 30m deadline", args[2])
	}

	// Without --wait the refresh exits 11 and lists the deadline.
	plain := tr.w.run(tr.parent, "acquire", "--checkout", tr.repo, "--scope", "src")
	wantStatus(t, plain, exitcode.Delegated)
	wantContains(t, "stdout", plain.Stdout, "delegated: "+tr.childID, "recalled until", tr.w.now.Add(30*time.Minute).Format(time.RFC3339))

	wt := tr.w.fakeWait(func(int) { tr.w.pass(10 * time.Minute) })

	got := tr.w.run(tr.parent, args...)
	wantStatus(t, got, exitcode.OK)
	wantContains(t, "stdout", got.Stdout, "already held; nothing changed")
	wantContains(t, "stderr", got.Stderr, "ended: the deadline of its recall")

	if len(wt.slept) == 0 {
		t.Error("the wait never waited")
	}

	tr.wantChildEnded()
}

func TestAcquireWaitUnderARecallAfterAPartialDelegationExitsWithTheDelegationsThatRemain(t *testing.T) {
	tr := newTree(t)

	res := tr.recall("30m")
	wantStatus(t, res, exitcode.OK)

	args := printedWait(t, res.Stdout)

	tr.w.fakeWait(func(int) { tr.w.pass(10 * time.Minute) })

	got := tr.w.run(tr.parent, args...)
	wantStatus(t, got, exitcode.Delegated)
	wantContains(t, "stdout", got.Stdout, "delegated: "+tr.siblingID)
	wantAbsent(t, "stdout", got.Stdout, "delegated: "+tr.childID)

	tr.wantChildEnded()
}

func TestAcquireWaitThatEndsBeforeTheDeadlineTimesOut(t *testing.T) {
	tr := newHandoff(t)
	tr.recallOK("30m")

	tr.w.fakeWait(nil)

	got := tr.w.run(tr.parent, "acquire", "--wait", "1m", "--checkout", tr.repo, "--scope", "src")
	wantStatus(t, got, exitcode.WaitTimeout)
	wantContains(t, "stderr", got.Stderr, "recalled until", tr.childID)

	if tr.w.leaseByID(tr.childID) == nil {
		t.Error("a wait that stopped before the deadline ended the sub-lease")
	}
}

func TestAHigherMinorRecallIsNeverRemovedByAnOlderBinary(t *testing.T) {
	t.Run("recall of a dead sub-lessee is refused", func(t *testing.T) {
		tr := newTree(t)
		tr.recallOK("30m")
		tr.w.editJSON(tr.w.recallPath(tr.childID), func(doc map[string]any) { doc["version"] = "1.5" })
		tr.w.kill(300)

		res := tr.recall("10m")
		wantStatus(t, res, exitcode.Unusable)
		wantContains(t, "stderr", res.Stderr, "upgrade nitipan")

		if tr.w.leaseByID(tr.childID) == nil || tr.w.recallOf(tr.childID) == nil {
			t.Error("a refused recall removed the sub-lease or the recall")
		}
	})

	t.Run("the reap of a recall whose sub-lease is gone leaves it", func(t *testing.T) {
		tr := newTree(t)
		tr.recallOK("30m")
		tr.w.editJSON(tr.w.recallPath(tr.childID), func(doc map[string]any) { doc["version"] = "1.5" })

		wantStatus(t, tr.w.run(tr.child, "release", tr.childID), exitcode.OK)
		wantStatus(t, tr.w.run(tr.third, "list"), exitcode.OK)

		if rc := tr.w.recallOf(tr.childID); rc == nil || rc.Version.String() != "1.5" {
			t.Errorf("recall = %+v, want it left for a newer binary", rc)
		}
	})
}
