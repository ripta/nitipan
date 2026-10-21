//go:build linux

package proc

import "os"

// System returns the table for the running operating system.
func System() Table {
	return NewLinuxTable(os.DirFS("/proc"), os.Getpid())
}
