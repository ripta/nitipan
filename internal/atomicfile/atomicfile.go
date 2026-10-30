// Package atomicfile publishes a file all at once.
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// TempPrefix starts the name of every temp file Write leaves behind when it is interrupted.
const TempPrefix = ".tmp-"

// Write writes a file through a synced temp file and rename(2), so a reader never sees a partial
// file. It creates the directory with mode 0700 when it is missing.
func Write(dir, name string, data []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, TempPrefix+name+"-*")
	if err != nil {
		return fmt.Errorf("creating a temp file in %s: %w", dir, err)
	}

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())

		return fmt.Errorf("writing %s: %w", name, err)
	}

	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())

		return fmt.Errorf("syncing %s: %w", name, err)
	}

	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())

		return fmt.Errorf("closing %s: %w", name, err)
	}

	if err := os.Rename(tmp.Name(), filepath.Join(dir, name)); err != nil {
		_ = os.Remove(tmp.Name())

		return fmt.Errorf("publishing %s: %w", name, err)
	}

	return nil
}
