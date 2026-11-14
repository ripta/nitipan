package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/gitfence"
	"github.com/space-armada/nitipan/internal/keys"
)

// commitTouching makes the hook see a branch update that touches one path under the tree's checkout.
func (tr *tree) commitTouching(rels ...string) {
	tr.w.stdin = branchTx
	tr.w.touched = func([]gitfence.Update) ([]gitfence.RefTouch, error) {
		return []gitfence.RefTouch{{
			Ref:      "refs/heads/main",
			New:      someID,
			Checkout: keys.Key{Path: tr.repo},
			Keys:     recallKeys(tr.repo, rels),
		}}, nil
	}
}

func recallKeys(repo string, rels []string) []keys.Key {
	out := make([]keys.Key, len(rels))
	for i, rel := range rels {
		out[i] = keys.Key{Path: filepath.Join(repo, rel)}
	}

	return out
}

func (tr *tree) hook(c caller) result {
	tr.w.t.Helper()

	tr.w.stdin = branchTx

	return tr.w.run(c, "hook", "reference-transaction", "prepared")
}

// These tests drive the hook over a fake process table and a settable clock, with the touched paths
// fixed. They cannot show that git calls the hook at the right moment, which the end-to-end rig owns.

func TestTheHookRefusesAnExSubLesseeOnRecalledKeysPastTheDeadlineAndPassesTheParent(t *testing.T) {
	tr := newTree(t)
	tr.commitTouching("src/api/x.go")
	tr.recallOK("30m")
	tr.w.pass(time.Hour)

	deadline := tr.w.recallOf(tr.childID).Deadline.UTC().Format(time.RFC3339)

	refused := tr.hook(tr.child)
	wantStatus(t, refused, exitcode.Refused)
	wantContains(t, "stderr", refused.Stderr, "your sub-lease "+tr.childID+" was recalled", "parent-agent", deadline,
		"no longer exists", "src/api/x.go")
	wantAbsent(t, "stderr", refused.Stderr, "nitipan request", "nitipan acquire", "revoke", "--force")

	wantStatus(t, tr.hook(tr.parent), exitcode.OK)

	if tr.w.recallOf(tr.childID) == nil || tr.w.leaseByID(tr.childID) == nil {
		t.Error("the hook wrote to the state directory")
	}
}

func TestTheHookPassesASubLesseeBeforeTheDeadlineAndPrintsTheDeadline(t *testing.T) {
	tr := newTree(t)
	tr.commitTouching("src/api/x.go")
	tr.recallOK("30m")

	recallBefore, err := os.ReadFile(tr.w.recallPath(tr.childID))
	if err != nil {
		t.Fatal(err)
	}

	tr.w.pass(10 * time.Minute)

	res := tr.hook(tr.child)
	wantStatus(t, res, exitcode.OK)

	deadline := tr.w.recallOf(tr.childID).Deadline.UTC().Format(time.RFC3339)
	wantContains(t, "stderr", res.Stderr, "your sub-lease "+tr.childID, "was recalled by the holder of lease "+tr.parentID, deadline)

	recallAfter, err := os.ReadFile(tr.w.recallPath(tr.childID))
	if err != nil {
		t.Fatal(err)
	}

	if string(recallBefore) != string(recallAfter) || tr.w.noticeOf("recalled") == nil {
		t.Error("the hook rewrote the recall or consumed its notice")
	}

	tr.commitTouching("docs/x.md")
	wantAbsent(t, "stderr", tr.hook(tr.sibling).Stderr, "was recalled")
}

func TestTheHookReportsAndIgnoresARecallRecordItCannotUse(t *testing.T) {
	tr := newTree(t)
	tr.commitTouching("src/api/x.go")
	tr.recallOK("30m")

	if err := os.WriteFile(tr.w.recallPath(tr.childID), []byte("not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tr.w.pass(time.Hour)

	res := tr.hook(tr.child)
	wantStatus(t, res, exitcode.OK)
	wantContains(t, "stderr", res.Stderr, "recall record", tr.childID+".json", "ends no sub-lease")
	wantAbsent(t, "stderr", res.Stderr, "no longer exists")

	wantStatus(t, tr.hook(tr.parent), exitcode.Refused)
}

func TestTheHookEndsNoSubLeaseWhoseRecallHasAnUnusableParentRecord(t *testing.T) {
	tr := newTree(t)
	tr.commitTouching("src/api/x.go")
	tr.recallOK("30m")
	tr.w.pass(time.Hour)

	tr.w.editJSON(filepath.Join(tr.w.leasesDir(), tr.parentID+".json"), func(doc map[string]any) { doc["version"] = "2.0" })

	res := tr.hook(tr.child)
	wantStatus(t, res, exitcode.Refused)
	wantContains(t, "stderr", res.Stderr, "recall of sub-lease "+tr.childID, "is not enforced", "parent lease record")
	wantAbsent(t, "stderr", res.Stderr, "was recalled by")
}

type recallEdgeTest struct {
	Name   string
	Setup  func(tr *tree)
	Who    string
	Paths  []string
	Status int

	// Contains and Absent are checked against stderr.
	Contains []string
	Absent   []string
}

// recalledPastDeadline recalls the child and moves the clock beyond the deadline.
func recalledPastDeadline(tr *tree) {
	tr.recallOK("30m")
	tr.w.pass(time.Hour)
}

var recallEdgeTests = []recallEdgeTest{
	{
		Name: "a caller that holds the sub-lease and its parent is not refused by the recall",
		Setup: func(tr *tree) {
			var parentHolder any

			tr.w.editJSON(filepath.Join(tr.w.leasesDir(), tr.parentID+".json"), func(doc map[string]any) {
				parentHolder = doc["holder"]
			})
			tr.w.editJSON(filepath.Join(tr.w.leasesDir(), tr.childID+".json"), func(doc map[string]any) {
				doc["holder"] = parentHolder
			})
			recalledPastDeadline(tr)
		},
		Who: "parent", Paths: []string{"src/api/x.go"}, Status: exitcode.OK,
		Absent: []string{"no longer exists", "was recalled by"},
	},
	{
		Name: "a refused update prints no deadline line",
		Setup: func(tr *tree) {
			tr.recallOK("30m")
		},
		Who: "child", Paths: []string{"src/api/x.go", "docs/x.md"}, Status: exitcode.Refused,
		Contains: []string{"sibling-agent"}, Absent: []string{"was recalled by"},
	},
	{
		Name: "a dead sub-lessee past the deadline refuses nobody",
		Setup: func(tr *tree) {
			recalledPastDeadline(tr)
			tr.w.kill(300)
		},
		Who: "parent", Paths: []string{"src/api/x.go"}, Status: exitcode.OK,
		Absent: []string{"was recalled by"},
	},
	{
		Name: "a fence mark on the same path is reported before the recall",
		Setup: func(tr *tree) {
			recalledPastDeadline(tr)
			tr.w.plantMark(markSpec{PID: 300, Keys: []string{filepath.Join(tr.repo, "src", "api")}})
		},
		Who: "child", Paths: []string{"src/api/x.go"}, Status: exitcode.Refused,
		Contains: []string{"fence mark still guards"}, Absent: []string{"was recalled by"},
	},
	{
		Name: "a tombstone on the same path is reported before the recall",
		Setup: func(tr *tree) {
			elsewhere := tr.w.acquireOK(tr.child, "--checkout", tr.w.dir("other"), "--name", "child-agent")
			wantStatus(tr.w.t, tr.w.run(tr.third, "break", "--force", elsewhere), exitcode.OK)

			for _, id := range tr.w.tombIDs() {
				tr.w.editJSON(filepath.Join(tr.w.tombDir(), id+".json"), func(doc map[string]any) {
					doc["paths"] = []any{filepath.Join(tr.repo, "src", "api")}
				})
			}

			recalledPastDeadline(tr)
		},
		Who: "child", Paths: []string{"src/api/x.go"}, Status: exitcode.Refused,
		Contains: []string{"Your lease no longer exists"}, Absent: []string{"was recalled by"},
	},
}

func TestTheHookSettlesRecallEdgesTheSameWayEveryTime(t *testing.T) {
	for _, test := range recallEdgeTests {
		t.Run(test.Name, func(t *testing.T) {
			tr := newTree(t)
			test.Setup(tr)
			tr.commitTouching(test.Paths...)

			res := tr.hook(tr.actor(test.Who))
			wantStatus(t, res, test.Status)
			wantContains(t, "stderr", res.Stderr, test.Contains...)
			wantAbsent(t, "stderr", res.Stderr, test.Absent...)
		})
	}
}

func TestTheHookPrintsOneDeadlineLinePerRecallWhateverNumberOfPathsFallUnderIt(t *testing.T) {
	tr := newTree(t)
	tr.commitTouching("src/api/x.go", "src/handlers/y.go")
	tr.recallOK("30m")

	res := tr.hook(tr.child)
	wantStatus(t, res, exitcode.OK)

	if n := strings.Count(res.Stderr, "was recalled by"); n != 1 {
		t.Errorf("the hook printed %d deadline lines, want 1\n%s", n, res.Stderr)
	}
}
