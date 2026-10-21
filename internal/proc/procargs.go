package proc

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// ParseProcArgs2 parses the buffer that the macOS kern.procargs2 sysctl returns.
//
// The layout is a native-endian int32 argument count, the executable path, NUL padding, and then
// the arguments, each NUL-terminated. The environment follows and is ignored.
func ParseProcArgs2(buf []byte) ([]string, error) {
	const header = 4

	if len(buf) < header {
		return nil, errors.New("procargs2 buffer is shorter than its header")
	}

	argc := int(binary.NativeEndian.Uint32(buf[:header]))
	rest := buf[header:]

	i := 0
	for i < len(rest) && rest[i] != 0 {
		i++
	}

	for i < len(rest) && rest[i] == 0 {
		i++
	}

	args := make([]string, 0, argc)

	for len(args) < argc {
		if i >= len(rest) {
			return nil, fmt.Errorf("procargs2 buffer ends after %d of %d arguments", len(args), argc)
		}

		j := i
		for j < len(rest) && rest[j] != 0 {
			j++
		}

		args = append(args, string(rest[i:j]))
		i = j + 1
	}

	return args, nil
}
