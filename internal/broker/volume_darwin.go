//go:build darwin

package broker

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// SystemVolume asks statfs whether a directory is on a local volume.
type SystemVolume struct{}

// Local refuses a network share, where fsync may not reach the disk.
func (SystemVolume) Local(dir string) error {
	var st unix.Statfs_t

	if err := unix.Statfs(dir, &st); err != nil {
		return fmt.Errorf("checking the volume of %s: %w", dir, err)
	}

	if st.Flags&unix.MNT_LOCAL == 0 {
		return fmt.Errorf("%s is on a network volume: %w", dir, ErrStoreVolume)
	}

	return nil
}
