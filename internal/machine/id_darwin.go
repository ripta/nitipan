//go:build darwin && cgo

package machine

/*
#include <time.h>
#include <unistd.h>
*/
import "C"

const uuidLen = 16

// machineID reads the hardware UUID with gethostuuid(2). A failure returns no ID.
func machineID() string {
	var (
		uuid [uuidLen]C.uchar
		wait = C.struct_timespec{tv_sec: 1}
	)

	if C.gethostuuid(&uuid[0], &wait) != 0 {
		return ""
	}

	var raw [uuidLen]byte
	for i, b := range uuid {
		raw[i] = byte(b)
	}

	return FormatUUID(raw)
}
