// Package proc reads facts about other processes: when one started, who its parent is, and how it
// was invoked.
//
// Nothing here uses kill(pid, 0). Inside the Claude Code sandbox on macOS it returns EPERM for a
// live process, so it cannot tell "alive" from "not permitted". Every read goes through a Table, so
// tests inject a fake process table.
package proc

import (
	"errors"

	"github.com/space-armada/nitipan/internal/record"
)

// ErrNoProcess means the process does not exist. Any other error from a Table means the answer is
// unknown.
var ErrNoProcess = errors.New("no such process")

// Table reads processes.
type Table interface {
	// Platform names the shape of the ProcessStart values this table produces.
	Platform() string

	// Self is the PID of the calling process.
	Self() int

	// Start returns the process start value for pid.
	//
	// On Linux the value also carries the reader's PID-namespace inode and the current boot ID, so
	// liveness can tell a namespace mismatch and a reboot from a reused PID.
	Start(pid int) (record.ProcessStart, error)

	// Parent returns the parent PID of pid.
	Parent(pid int) (int, error)

	// Name returns the command name of pid, which the operating system may truncate.
	Name(pid int) (string, error)

	// Args returns the full argument list of pid.
	Args(pid int) ([]string, error)

	// Stopped reports whether pid is stopped, as by SIGSTOP.
	Stopped(pid int) (bool, error)
}
