package keys

import (
	"errors"
	"syscall"
)

// isNotDir reports ENOTDIR, which Lstat returns when a prefix of the path is a file.
func isNotDir(err error) bool {
	return errors.Is(err, syscall.ENOTDIR)
}
