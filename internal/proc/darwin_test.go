//go:build darwin

package proc

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestDarwinTableReadsOwnProcess(t *testing.T) {
	table := System()
	self := os.Getpid()

	start, err := table.Start(self)
	if err != nil {
		t.Fatalf("Start(self): %v", err)
	}

	if !start.Known() || start.Sec == 0 {
		t.Errorf("start = %+v, want a darwin start with seconds", start)
	}

	again, err := table.Start(self)
	if err != nil || !again.Equal(start) {
		t.Errorf("a second read changed the start: %+v, %v", again, err)
	}

	ppid, err := table.Parent(self)
	if err != nil || ppid != os.Getppid() {
		t.Errorf("Parent = %d, %v, want %d", ppid, err, os.Getppid())
	}

	args, err := table.Args(self)
	if err != nil {
		t.Fatalf("Args: %v", err)
	}

	if len(args) == 0 || !strings.Contains(args[0], "proc.test") {
		t.Errorf("args = %q, want the test binary first", args)
	}

	name, err := table.Name(self)
	if err != nil || name == "" {
		t.Errorf("Name = %q, %v", name, err)
	}

	stopped, err := table.Stopped(self)
	if err != nil || stopped {
		t.Errorf("Stopped(self) = %v, %v", stopped, err)
	}
}

func TestDarwinTableReportsExitedProcessAsMissing(t *testing.T) {
	cmd := exec.Command("/usr/bin/true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("running true: %v", err)
	}

	_, err := System().Start(cmd.Process.Pid)
	if !errors.Is(err, ErrNoProcess) {
		t.Errorf("Start of an exited process = %v, want ErrNoProcess", err)
	}
}

func TestDarwinTableDistinguishesRunningChildStart(t *testing.T) {
	cmd := exec.Command("/bin/sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting sleep: %v", err)
	}

	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	child, err := System().Start(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("Start(child): %v", err)
	}

	self, err := System().Start(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}

	if child.Equal(self) {
		t.Error("two processes reported the same start")
	}
}
