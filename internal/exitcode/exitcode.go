// Package exitcode defines nitipan's exit statuses in one table.
//
// Scripts and agents test these values, so a value never changes once released. A new result takes
// the next free number.
package exitcode

import (
	"errors"

	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/state"
)

// Exit statuses.
const (
	OK             = 0
	Error          = 1
	Usage          = 2
	Refused        = 3
	LockBusy       = 4
	AlreadyCovered = 5
	NotYours       = 6
	Broken         = 7
	NoSuchLease    = 8
	Unusable       = 9
	WaitTimeout    = 10
)

// Entry documents one exit status.
type Entry struct {
	Code    int
	Name    string
	Meaning string
}

// Table lists every exit status, in numeric order.
var Table = []Entry{
	{OK, "ok", "the command did what was asked"},
	{Error, "error", "any failure with no status of its own"},
	{Usage, "usage", "the command line was wrong"},
	{Refused, "refused", "an overlapping lease blocks the claim: another holder's live, unknown, or unclearable lease, or one of the caller's own that the claim would change. Also a full resource slot, a --slots value that differs from the count that applies, and --replace on a resource lease"},
	{LockBusy, "lock busy", "the state lock stayed busy past the retry budget"},
	{AlreadyCovered, "already covered", "the claim is already covered by the caller's own leases, and nothing changed"},
	{NotYours, "not yours", "the lease belongs to another holder"},
	{Broken, "broken", "the lease was broken, and the tombstone says by whom"},
	{NoSuchLease, "no such lease", "no lease has that ID"},
	{Unusable, "unusable record", "a record this nitipan cannot use blocks the request"},
	{WaitTimeout, "wait timeout", "acquire --wait ran out of time before the lease could be granted"},
}

// Results that map to a status of their own. Commands wrap these with detail for the message.
var (
	ErrUsage          = errors.New("usage error")
	ErrRefused        = errors.New("refused by an overlapping lease")
	ErrAlreadyCovered = errors.New("already covered by your leases")
	ErrNotYours       = errors.New("lease is held by another holder")
	ErrBroken         = errors.New("lease was broken")
	ErrNoSuchLease    = errors.New("no such lease")
	ErrWaitTimeout    = errors.New("timed out waiting for a lease")
)

// For maps an error to its exit status.
func For(err error) int {
	switch {
	case err == nil:
		return OK
	case errors.Is(err, ErrUsage):
		return Usage
	case errors.Is(err, ErrRefused):
		return Refused
	case errors.Is(err, state.ErrLockBusy):
		return LockBusy
	case errors.Is(err, ErrAlreadyCovered):
		return AlreadyCovered
	case errors.Is(err, ErrNotYours):
		return NotYours
	case errors.Is(err, ErrBroken):
		return Broken
	case errors.Is(err, ErrNoSuchLease):
		return NoSuchLease
	case errors.Is(err, record.ErrUnusable):
		return Unusable
	case errors.Is(err, ErrWaitTimeout):
		return WaitTimeout
	default:
		return Error
	}
}
