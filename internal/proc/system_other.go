//go:build !darwin && !linux

package proc

import (
	"errors"
	"os"

	"github.com/space-armada/nitipan/internal/record"
)

// ErrUnsupported means the platform has no process reader.
var ErrUnsupported = errors.New("process inspection is not supported on this platform")

// UnsupportedTable answers every question with ErrUnsupported, so liveness reads unknown.
type UnsupportedTable struct{}

// System returns the table for the running operating system.
func System() Table {
	return UnsupportedTable{}
}

// Platform implements Table.
func (UnsupportedTable) Platform() string { return "unsupported" }

// Self implements Table.
func (UnsupportedTable) Self() int { return os.Getpid() }

// Start implements Table.
func (UnsupportedTable) Start(int) (record.ProcessStart, error) {
	return record.ProcessStart{}, ErrUnsupported
}

// Parent implements Table.
func (UnsupportedTable) Parent(int) (int, error) { return 0, ErrUnsupported }

// Name implements Table.
func (UnsupportedTable) Name(int) (string, error) { return "", ErrUnsupported }

// Args implements Table.
func (UnsupportedTable) Args(int) ([]string, error) { return nil, ErrUnsupported }

// Stopped implements Table.
func (UnsupportedTable) Stopped(int) (bool, error) { return false, ErrUnsupported }
