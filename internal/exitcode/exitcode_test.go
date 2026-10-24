package exitcode

import (
	"errors"
	"fmt"
	"testing"

	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/state"
)

type forTest struct {
	Name string
	Err  error
	Want int
}

var forTests = []forTest{
	{Name: "nil", Err: nil, Want: OK},
	{Name: "generic", Err: errors.New("boom"), Want: Error},
	{Name: "usage", Err: fmt.Errorf("bad flag: %w", ErrUsage), Want: Usage},
	{Name: "refused", Err: fmt.Errorf("held by amber: %w", ErrRefused), Want: Refused},
	{Name: "lock busy", Err: &state.LockBusyError{}, Want: LockBusy},
	{Name: "lock busy wrapped", Err: fmt.Errorf("acquire: %w", &state.LockBusyError{}), Want: LockBusy},
	{Name: "already covered", Err: ErrAlreadyCovered, Want: AlreadyCovered},
	{Name: "not yours", Err: ErrNotYours, Want: NotYours},
	{Name: "broken", Err: ErrBroken, Want: Broken},
	{Name: "no such lease", Err: ErrNoSuchLease, Want: NoSuchLease},
	{Name: "unusable record", Err: &record.UnusableError{Reason: record.ReasonHigherMajor}, Want: Unusable},
	{Name: "wait timeout", Err: fmt.Errorf("gave up: %w", ErrWaitTimeout), Want: WaitTimeout},
	{Name: "delegated", Err: fmt.Errorf("part is delegated: %w", ErrDelegated), Want: Delegated},
	{Name: "format refusal has no status of its own", Err: &state.FormatError{Need: 2}, Want: Error},
}

func TestFor(t *testing.T) {
	for _, test := range forTests {
		t.Run(test.Name, func(t *testing.T) {
			if got := For(test.Err); got != test.Want {
				t.Errorf("For(%v) = %d, want %d", test.Err, got, test.Want)
			}
		})
	}
}

func TestTableIsDocumentedAndUnique(t *testing.T) {
	seen := map[int]bool{}
	names := map[string]bool{}

	for i, e := range Table {
		if e.Code != i {
			t.Errorf("entry %q has code %d at index %d, want the table in numeric order", e.Name, e.Code, i)
		}

		if seen[e.Code] || names[e.Name] {
			t.Errorf("duplicate entry %+v", e)
		}

		if e.Name == "" || e.Meaning == "" {
			t.Errorf("entry %d is undocumented", e.Code)
		}

		seen[e.Code], names[e.Name] = true, true
	}

	for _, code := range []int{OK, Error, Usage, Refused, LockBusy, AlreadyCovered, NotYours, Broken, NoSuchLease, Unusable, WaitTimeout, Delegated} {
		if !seen[code] {
			t.Errorf("status %d is missing from the table", code)
		}
	}
}

func TestLockBusyHasItsOwnStatusDistinctFromRefusal(t *testing.T) {
	if For(&state.LockBusyError{}) == For(ErrRefused) {
		t.Error("a busy lock and a refusal share an exit status")
	}
}
