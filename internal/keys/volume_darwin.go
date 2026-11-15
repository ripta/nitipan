//go:build darwin

package keys

import (
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// pcCaseSensitive is _PC_CASE_SENSITIVE from <unistd.h>. It is absent from x/sys/unix.
const pcCaseSensitive = 11

// SystemVolumes reads volume facts from macOS.
type SystemVolumes struct{}

// Sensitivity asks pathconf whether the volume is case sensitive.
//
// Every macOS volume ignores Unicode normalization form, case sensitive APFS included, so Normalize
// is always set. A failed pathconf reads as case insensitive, because folding only adds overlaps.
func (SystemVolumes) Sensitivity(resolved string) Sensitivity {
	sensitive, err := unix.Pathconf(resolved, pcCaseSensitive)
	if err != nil {
		return Sensitivity{FoldCase: true, Normalize: true}
	}

	return Sensitivity{FoldCase: sensitive == 0, Normalize: true}
}

// CanonicalCase returns the path as the filesystem stores it, through F_GETPATH on an open
// descriptor.
func (SystemVolumes) CanonicalCase(resolved string) (string, error) {
	f, err := os.Open(resolved)
	if err != nil {
		return "", fmt.Errorf("opening %s: %w", resolved, err)
	}
	defer f.Close()

	buf := make([]byte, unix.PathMax)

	_, err = unix.FcntlInt(f.Fd(), unix.F_GETPATH, int(uintptr(unsafe.Pointer(&buf[0]))))
	runtime.KeepAlive(buf)

	if err != nil {
		return "", fmt.Errorf("F_GETPATH on %s: %w", resolved, err)
	}

	for i, b := range buf {
		if b == 0 {
			return string(buf[:i]), nil
		}
	}

	return "", fmt.Errorf("F_GETPATH on %s: unterminated path", resolved)
}
