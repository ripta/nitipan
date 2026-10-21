package keys

import (
	"errors"
	"syscall"
)

// fsCasefoldFlag is FS_CASEFOLD_FL from <linux/fs.h>: the directory compares names without case.
const fsCasefoldFlag = 0x40000000

// FlagsReader reads the inode flags of a directory, as the FS_IOC_GETFLAGS ioctl does on Linux.
type FlagsReader func(dir string) (uint32, error)

// casefoldSensitivity decides a Linux volume's sensitivity from the flags of the deepest existing
// directory.
//
// A filesystem with no casefold support answers the ioctl with ENOTTY, EOPNOTSUPP, or EINVAL, and
// its names are case sensitive. Any other error reads insensitive. Folding only adds overlaps, so
// an unreadable answer refuses a claim instead of letting two spellings of one directory both
// succeed. That matches the macOS policy for a failed pathconf.
func casefoldSensitivity(read FlagsReader, dir string) Sensitivity {
	flags, err := read(dir)

	switch {
	case err == nil && flags&fsCasefoldFlag == 0:
		return Sensitivity{}
	case err == nil:
		return Sensitivity{FoldCase: true, Normalize: true}
	case errors.Is(err, syscall.ENOTTY), errors.Is(err, syscall.EOPNOTSUPP), errors.Is(err, syscall.EINVAL):
		return Sensitivity{}
	default:
		return Sensitivity{FoldCase: true, Normalize: true}
	}
}
