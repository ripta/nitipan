package monoclock

import (
	"testing"
	"time"
)

func TestSystemClockNeverGoesBackward(t *testing.T) {
	var c Clock = System{}

	a := c.Now()
	time.Sleep(2 * time.Millisecond)

	b := c.Now()
	if a <= 0 || b <= a {
		t.Errorf("readings %v then %v, want positive and increasing", a, b)
	}
}

func TestFakeCountsSleepSeparatelyFromRunning(t *testing.T) {
	f := NewFake(time.Second)

	f.Advance(time.Minute)
	f.Sleep(8 * time.Hour)

	total, count := f.Slept()
	if total != 8*time.Hour || count != 1 {
		t.Errorf("Slept = %v, %d, want 8h, 1", total, count)
	}

	if want := time.Second + time.Minute + 8*time.Hour; f.Now() != want {
		t.Errorf("Now = %v, want %v", f.Now(), want)
	}
}
