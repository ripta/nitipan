package cli

import (
	"testing"
	"time"

	"github.com/space-armada/nitipan/internal/exitcode"
)

type deadlineReaderTest struct {
	Name string

	// Ended runs one reader that takes no lock, and says whether it treated the sub-lease as ended.
	Ended func(tr *tree) bool
}

var deadlineReaderTests = []deadlineReaderTest{
	{
		Name: "the git hook",
		Ended: func(tr *tree) bool {
			tr.commitTouching("src/api/x.go")

			return tr.hook(tr.child).Status == exitcode.Refused
		},
	},
	{
		Name: "an unlocked list",
		Ended: func(tr *tree) bool {
			defer tr.w.holdLock()()

			res := tr.w.run(tr.third, "list", "--json")
			wantStatus(tr.w.t, res, exitcode.OK)

			for _, l := range leasesOf(tr.w.t, decodeStdout(tr.w.t, res)) {
				if l["id"] == tr.childID {
					return false
				}
			}

			return true
		},
	},
	{
		Name: "the ring verdict for the recalled notice",
		Ended: func(tr *tree) bool {
			return tr.w.ringContext(tr.child) == ""
		},
	},
}

type deadlineInstantTest struct {
	Name string

	// Offset is the reader's clock relative to the recall's deadline.
	Offset time.Duration
	Ended  bool
}

var deadlineInstantTests = []deadlineInstantTest{
	{Name: "one nanosecond before the deadline", Offset: -time.Nanosecond, Ended: false},
	{Name: "exactly at the deadline", Offset: 0, Ended: true},
}

// The deadline is inclusive, as lease.JudgeRecall defines it. A wait whose last attempt runs at its own
// timeout must find the sub-lease ended, so every reader that takes no lock must agree at that instant.
// The control one nanosecond earlier proves each reader tells the two instants apart. The test cannot
// show that a locked call agrees, which the expiry tests own.
func TestEveryReaderThatTakesNoLockEndsTheSubLeaseAtTheExactDeadline(t *testing.T) {
	for _, reader := range deadlineReaderTests {
		for _, instant := range deadlineInstantTests {
			t.Run(reader.Name+", "+instant.Name, func(t *testing.T) {
				tr := newTree(t)
				tr.recallOK("30m")

				tr.w.now = tr.w.recallOf(tr.childID).Deadline.Add(instant.Offset)

				if got := reader.Ended(tr); got != instant.Ended {
					t.Errorf("ended = %v, want %v", got, instant.Ended)
				}
			})
		}
	}
}
