// Package machine identifies the host nitipan runs on.
package machine

import (
	"fmt"
	"os"
	"strings"

	"github.com/space-armada/nitipan/internal/record"
)

// Local returns the host name and, where the operating system has one, the stable machine ID.
//
// A machine ID that cannot be read comes back empty. Records then fall back to comparing host
// names, which is weaker but never wrong in the unsafe direction: a mismatch reads unknown.
func Local() (record.Machine, error) {
	host, err := os.Hostname()
	if err != nil {
		return record.Machine{}, fmt.Errorf("reading host name: %w", err)
	}

	return record.Machine{Host: host, ID: machineID()}, nil
}

// FormatUUID spells 16 bytes the usual 8-4-4-4-12 way.
func FormatUUID(u [16]byte) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
}

// CleanID normalizes a raw machine ID for storage and comparison.
func CleanID(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}
