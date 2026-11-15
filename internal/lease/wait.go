package lease

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/space-armada/nitipan/internal/exitcode"
	"github.com/space-armada/nitipan/internal/state"
)

// Backoff between attempts of a wait. The delay starts at waitFirst, doubles after each attempt, and
// stops growing at waitCap. Each sleep is then jittered into its upper half, so waiters that started
// together drift apart.
const (
	waitFirst = 100 * time.Millisecond
	waitCap   = 2 * time.Second
)

// WaitTimeoutError reports that a wait ran out of time. It names what the last attempts met.
type WaitTimeoutError struct {
	Waited time.Duration

	// Refusal is the last refusal that named holders, or nil when every attempt met a busy lock.
	Refusal error

	// Busy is set only when the last attempt found the lock busy.
	Busy error
}

// Error implements error.
func (e *WaitTimeoutError) Error() string {
	var b strings.Builder

	fmt.Fprintf(&b, "no lease was granted within %s.", e.Waited.Round(time.Millisecond))

	if e.Refusal != nil {
		fmt.Fprintf(&b, " Waited on:\n%s", indent(e.Refusal.Error()))
	}

	if e.Busy != nil {
		fmt.Fprintf(&b, "\nThe last attempt could not take the state lock: %v", e.Busy)
	}

	return b.String()
}

// Is matches the timeout status.
func (e *WaitTimeoutError) Is(target error) bool { return target == exitcode.ErrWaitTimeout }

// warnOnce returns a copy of the engine that reports each distinct warning once. A wait runs an attempt
// per poll, and a standing problem such as a bad slot config would otherwise print on every one.
func (e *Engine) warnOnce() *Engine {
	seen := map[string]bool{}
	next := e.Notify

	next.Warn = func(err error) {
		if seen[err.Error()] {
			return
		}

		seen[err.Error()] = true

		e.Notify.warn(err)
	}

	cp := *e
	cp.Notify = next

	return &cp
}

func indent(s string) string {
	return "  " + strings.ReplaceAll(s, "\n", "\n  ")
}

// AcquireWait is Acquire with a bounded wait. It tries again after a refusal that a later attempt can
// see lifted, until the timeout passes.
//
// Only three outcomes are waited on: a live holder overlaps, a slot is full, or the lock is busy.
// The lock is held only inside each attempt. Every other error returns at once, with its own status.
// The last attempt runs at the deadline, so a holder that released just before it is still seen.
func (e *Engine) AcquireWait(req AcquireRequest, timeout time.Duration) (*AcquireResult, error) {
	e = e.warnOnce()

	start := e.Now()
	deadline := start.Add(timeout)
	delay := waitFirst

	var timedOut WaitTimeoutError

	for {
		// The lock retry budget never outruns the deadline, so a wedged lock cannot stretch the wait.
		// A budget of zero still makes one try.
		budget := min(max(deadline.Sub(e.Now()), 0), e.Dir.LockBudget())

		res, err := e.acquire(req, func() (*state.Locked, error) { return e.Dir.LockWithin(budget) })
		if err == nil {
			return res, nil
		}

		switch {
		case errors.Is(err, ErrWaitable):
			timedOut.Refusal, timedOut.Busy = err, nil
		case errors.Is(err, state.ErrLockBusy):
			timedOut.Busy = err
		default:
			return nil, err
		}

		remaining := deadline.Sub(e.Now())
		if remaining <= 0 {
			timedOut.Waited = timeout

			return nil, &timedOut
		}

		sleep := time.Duration(float64(delay) * (0.5 + 0.5*e.Jitter()))
		e.Sleep(min(sleep, remaining))

		delay = min(delay*2, waitCap)
	}
}
