//go:build linux

package keys

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// SystemVolumes reads volume facts from Linux.
type SystemVolumes struct{}

// Sensitivity reads the casefold flag of the deepest existing directory.
func (SystemVolumes) Sensitivity(resolved string) Sensitivity {
	return casefoldSensitivity(readInodeFlags, resolved)
}

// CanonicalCase returns the path unchanged. A casefold directory keeps the case it was created
// with, but every comparison of its keys folds, so two spellings still compare equal.
func (SystemVolumes) CanonicalCase(resolved string) (string, error) {
	return resolved, nil
}

// readInodeFlags runs FS_IOC_GETFLAGS on path, or on its directory when path is a file.
func readInodeFlags(path string) (uint32, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("checking %s: %w", path, err)
	}

	if !info.IsDir() {
		path = filepath.Dir(path)
	}

	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()

	flags, err := unix.IoctlGetUint32(int(f.Fd()), unix.FS_IOC_GETFLAGS)
	if err != nil {
		return 0, fmt.Errorf("FS_IOC_GETFLAGS on %s: %w", path, err)
	}

	return flags, nil
}
