//go:build darwin

package proc

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"

	"github.com/space-armada/nitipan/internal/record"
)

// sstop is SSTOP from <sys/proc.h>, the p_stat value of a stopped process.
const sstop = 4

// DarwinTable reads processes through the kern.proc.pid and kern.procargs2 sysctls.
//
// The sysctl returns zero bytes for a dead PID, where kill(pid, 0) returns EPERM for a live one
// inside the Claude Code sandbox.
type DarwinTable struct{}

// System returns the table for the running operating system.
func System() Table {
	return DarwinTable{}
}

// Platform implements Table.
func (DarwinTable) Platform() string {
	return record.PlatformDarwin
}

// Self implements Table.
func (DarwinTable) Self() int {
	return os.Getpid()
}

func (DarwinTable) kinfo(pid int) (*unix.KinfoProc, error) {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if err != nil {
		return nil, fmt.Errorf("reading kern.proc.pid for %d: %w", pid, err)
	}

	if len(procs) == 0 || int(procs[0].Proc.P_pid) != pid {
		return nil, fmt.Errorf("kern.proc.pid for %d: %w", pid, ErrNoProcess)
	}

	return &procs[0], nil
}

// Start implements Table.
func (d DarwinTable) Start(pid int) (record.ProcessStart, error) {
	kp, err := d.kinfo(pid)
	if err != nil {
		return record.ProcessStart{}, err
	}

	return record.ProcessStart{
		Platform: record.PlatformDarwin,
		Sec:      kp.Proc.P_starttime.Sec,
		Usec:     int64(kp.Proc.P_starttime.Usec),
	}, nil
}

// Parent implements Table.
func (d DarwinTable) Parent(pid int) (int, error) {
	kp, err := d.kinfo(pid)
	if err != nil {
		return 0, err
	}

	return int(kp.Eproc.Ppid), nil
}

// Name implements Table.
func (d DarwinTable) Name(pid int) (string, error) {
	kp, err := d.kinfo(pid)
	if err != nil {
		return "", err
	}

	comm := kp.Proc.P_comm[:]
	for i, b := range comm {
		if b == 0 {
			comm = comm[:i]

			break
		}
	}

	return string(comm), nil
}

// Stopped implements Table.
func (d DarwinTable) Stopped(pid int) (bool, error) {
	kp, err := d.kinfo(pid)
	if err != nil {
		return false, err
	}

	return kp.Proc.P_stat == sstop, nil
}

// Args implements Table.
func (d DarwinTable) Args(pid int) ([]string, error) {
	if _, err := d.kinfo(pid); err != nil {
		return nil, err
	}

	buf, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		if errors.Is(err, unix.ESRCH) {
			return nil, fmt.Errorf("kern.procargs2 for %d: %w", pid, ErrNoProcess)
		}

		return nil, fmt.Errorf("reading kern.procargs2 for %d: %w", pid, err)
	}

	args, err := ParseProcArgs2(buf)
	if err != nil {
		return nil, fmt.Errorf("parsing kern.procargs2 for %d: %w", pid, err)
	}

	return args, nil
}
