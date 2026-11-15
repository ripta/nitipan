//go:build !darwin

package broker

import "fmt"

// SystemVolume cannot check a volume off macOS.
type SystemVolume struct{}

// Local always fails, since the broker runs as a macOS LaunchAgent and no other check is written.
func (SystemVolume) Local(dir string) error {
	return fmt.Errorf("cannot check whether %s is on a local volume on this platform: %w", dir, ErrStoreVolume)
}
