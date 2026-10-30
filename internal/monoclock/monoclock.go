// Package monoclock reads CLOCK_MONOTONIC.
//
// On macOS that clock keeps counting while the machine sleeps and never steps with NTP. Go's own
// monotonic reading stops during sleep, and the wall clock can step backward, so neither can pace a
// lease term.
package monoclock

import (
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// Clock reads elapsed time since an arbitrary fixed point. Only differences mean anything.
type Clock interface {
	Now() time.Duration
}

// System reads CLOCK_MONOTONIC.
type System struct{}

// Now returns the clock reading. A failed read returns zero, which every caller sees as no time
// passing, so a lease stays alive and is never ended early by a broken clock.
func (System) Now() time.Duration {
	var ts unix.Timespec

	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts); err != nil {
		return 0
	}

	return time.Duration(ts.Nano())
}

// Fake is a clock a test moves by hand. It counts the time slept separately from the time that
// passed, so a test can tell a machine that slept from one that ran.
type Fake struct {
	mu     sync.Mutex
	now    time.Duration
	slept  time.Duration
	sleeps int
}

// NewFake starts a fake clock at start.
func NewFake(start time.Duration) *Fake {
	return &Fake{now: start}
}

// Now returns the fake reading.
func (f *Fake) Now() time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.now
}

// Advance moves the clock forward by d, as running time.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.now += d
}

// Sleep moves the clock forward by d and counts it as time the machine slept.
func (f *Fake) Sleep(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.now += d
	f.slept += d
	f.sleeps++
}

// Slept returns the total time and the number of sleeps the clock counted.
func (f *Fake) Slept() (total time.Duration, count int) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.slept, f.sleeps
}
