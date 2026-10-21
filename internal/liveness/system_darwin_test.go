//go:build darwin

package liveness

import (
	"os"
	"os/exec"
	"testing"

	"github.com/space-armada/nitipan/internal/machine"
	"github.com/space-armada/nitipan/internal/proc"
	"github.com/space-armada/nitipan/internal/record"
)

// These tests read the real process table, so they also show that the sysctl works inside the
// sandbox the tests run in.
func realChecker(t *testing.T) *Checker {
	t.Helper()

	local, err := machine.Local()
	if err != nil {
		t.Fatalf("machine.Local: %v", err)
	}

	return &Checker{Procs: proc.System(), Local: local}
}

func holderEnvelope(t *testing.T, c *Checker, pid int, start record.ProcessStart) record.Envelope {
	t.Helper()

	return record.Envelope{
		Version:       record.Current,
		ID:            "01JAAAAAAAAAAAAAAAAAAAAAAA",
		ClearWhenDead: true,
		Liveness:      record.LivenessProcess,
		Machine:       c.Local,
		Holder:        record.Holder{Agent: "claude", PID: pid, Start: start},
	}
}

func TestRealProcessAliveThenDeadThenReused(t *testing.T) {
	c := realChecker(t)

	cmd := exec.Command("/bin/sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting sleep: %v", err)
	}

	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	pid := cmd.Process.Pid

	start, err := c.Procs.Start(pid)
	if err != nil {
		t.Fatalf("Start(%d): %v", pid, err)
	}

	if got := c.Check(holderEnvelope(t, c, pid, start)); got.Status != Alive {
		t.Errorf("running child = %v (%s), want alive", got.Status, got.Reason)
	}

	other := start
	other.Usec++

	if got := c.Check(holderEnvelope(t, c, pid, other)); got.Status != Dead {
		t.Errorf("child with a different recorded start = %v (%s), want dead as a reused PID", got.Status, got.Reason)
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}

	_ = cmd.Wait()

	if got := c.Check(holderEnvelope(t, c, pid, start)); got.Status != Dead {
		t.Errorf("killed child = %v (%s), want dead", got.Status, got.Reason)
	}
}

func TestRealOwnProcessIsAlive(t *testing.T) {
	c := realChecker(t)

	start, err := c.Procs.Start(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}

	if got := c.Check(holderEnvelope(t, c, os.Getpid(), start)); got.Status != Alive {
		t.Errorf("own process = %v (%s), want alive", got.Status, got.Reason)
	}
}
