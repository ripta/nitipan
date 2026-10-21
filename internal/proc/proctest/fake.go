// Package proctest provides a fake process table for tests in other packages.
package proctest

import (
	"fmt"

	"github.com/space-armada/nitipan/internal/proc"
	"github.com/space-armada/nitipan/internal/record"
)

// Process is one entry in a fake table.
type Process struct {
	Start   record.ProcessStart
	PPID    int
	Name    string
	Args    []string
	Stopped bool

	// StartErr, ArgsErr, ParentErr, and NameErr make the matching read fail with something other than
	// "no such process", which is how an unreadable process looks.
	StartErr  error
	ArgsErr   error
	ParentErr error
	NameErr   error
}

// Table is a fake proc.Table. A PID with no entry does not exist.
type Table struct {
	PlatformName string
	SelfPID      int
	Procs        map[int]*Process

	// StartCalls counts the Start reads per PID.
	StartCalls map[int]int
}

// New returns a fake table for the given platform with the calling process at selfPID.
func New(platform string, selfPID int) *Table {
	return &Table{
		PlatformName: platform,
		SelfPID:      selfPID,
		Procs:        map[int]*Process{},
		StartCalls:   map[int]int{},
	}
}

// Add puts a process in the table and returns it for further changes.
func (t *Table) Add(pid int, p Process) *Process {
	t.Procs[pid] = &p

	return &p
}

func (t *Table) get(pid int) (*Process, error) {
	p, ok := t.Procs[pid]
	if !ok {
		return nil, fmt.Errorf("pid %d: %w", pid, proc.ErrNoProcess)
	}

	return p, nil
}

// Platform implements proc.Table.
func (t *Table) Platform() string { return t.PlatformName }

// Self implements proc.Table.
func (t *Table) Self() int { return t.SelfPID }

// Start implements proc.Table.
func (t *Table) Start(pid int) (record.ProcessStart, error) {
	t.StartCalls[pid]++

	p, err := t.get(pid)
	if err != nil {
		return record.ProcessStart{}, err
	}

	return p.Start, p.StartErr
}

// Parent implements proc.Table.
func (t *Table) Parent(pid int) (int, error) {
	p, err := t.get(pid)
	if err != nil {
		return 0, err
	}

	return p.PPID, p.ParentErr
}

// Name implements proc.Table.
func (t *Table) Name(pid int) (string, error) {
	p, err := t.get(pid)
	if err != nil {
		return "", err
	}

	return p.Name, p.NameErr
}

// Args implements proc.Table.
func (t *Table) Args(pid int) ([]string, error) {
	p, err := t.get(pid)
	if err != nil {
		return nil, err
	}

	return p.Args, p.ArgsErr
}

// Stopped implements proc.Table.
func (t *Table) Stopped(pid int) (bool, error) {
	p, err := t.get(pid)
	if err != nil {
		return false, err
	}

	return p.Stopped, nil
}
