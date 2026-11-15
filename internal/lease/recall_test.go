package lease

import (
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/liveness"
	"github.com/space-armada/nitipan/internal/record"
)

type judgeTest struct {
	Name   string
	Recall func(rc *record.Recall)
	Child  RecallLease
	Parent RecallLease
	Now    time.Duration
	Want   RecallStatus
}

var judgeNow = time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

func judgeDeadline() time.Time { return judgeNow.Add(30 * time.Minute) }

func judgeChild() RecallLease {
	return RecallLease{Exists: true, Rec: &record.Record{Parent: "P"}, Live: liveness.Result{Status: liveness.Alive}}
}

func judgeParent() RecallLease {
	return RecallLease{Exists: true, Rec: &record.Record{}, Live: liveness.Result{Status: liveness.Alive}}
}

var judgeTests = []judgeTest{
	{Name: "before the deadline", Child: judgeChild(), Parent: judgeParent(), Now: 29 * time.Minute, Want: RecallInForce},
	{Name: "at the deadline, which is inclusive", Child: judgeChild(), Parent: judgeParent(), Now: 30 * time.Minute, Want: RecallPastDeadline},
	{Name: "after the deadline", Child: judgeChild(), Parent: judgeParent(), Now: time.Hour, Want: RecallPastDeadline},
	{
		Name: "a cancelling recall is never in force, even past the deadline", Child: judgeChild(), Parent: judgeParent(), Now: time.Hour,
		Recall: func(rc *record.Recall) { rc.State = record.RecallCancelling }, Want: RecallNotInForce,
	},
	{Name: "a sub-lease that is gone", Parent: judgeParent(), Now: time.Hour, Want: RecallNotInForce},
	{
		Name: "a sub-lease that names another parent", Parent: judgeParent(), Now: time.Hour, Want: RecallNotInForce,
		Child: RecallLease{Exists: true, Rec: &record.Record{Parent: "OTHER"}},
	},
	{
		Name: "a sub-lease that stands alone", Parent: judgeParent(), Now: time.Hour, Want: RecallNotInForce,
		Child: RecallLease{Exists: true, Rec: &record.Record{}},
	},
	{Name: "a parent that is gone", Child: judgeChild(), Now: time.Hour, Want: RecallNotInForce},
	{
		Name: "a parent whose holder died", Child: judgeChild(), Now: time.Hour, Want: RecallNotInForce,
		Parent: RecallLease{Exists: true, Rec: &record.Record{}, Live: liveness.Result{Status: liveness.Dead}},
	},
	{
		Name: "a parent record that cannot be used ends nothing", Child: judgeChild(), Now: time.Hour, Want: RecallUnusable,
		Parent: RecallLease{Exists: true},
	},
	{
		Name: "a sub-lease record that cannot be used ends nothing", Parent: judgeParent(), Now: time.Hour, Want: RecallUnusable,
		Child: RecallLease{Exists: true},
	},
}

func TestJudgeRecall(t *testing.T) {
	for _, test := range judgeTests {
		t.Run(test.Name, func(t *testing.T) {
			rc := &record.Recall{Parent: "P", Deadline: judgeDeadline(), State: record.RecallAnnounced}
			if test.Recall != nil {
				test.Recall(rc)
			}

			got := JudgeRecall(rc, test.Child, test.Parent, judgeNow.Add(test.Now))
			if got.Status != test.Want {
				t.Errorf("status = %d (%s), want %d", got.Status, got.Why, test.Want)
			}
		})
	}
}
