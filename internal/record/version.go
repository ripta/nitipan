// Package record defines the lease record envelope, its footprint, and the rules for reading and
// writing a record this binary may not fully understand.
package record

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// The record format this binary writes. A record of a higher minor is readable. A record of a higher
// major is not.
const (
	CurrentMajor = 1
	CurrentMinor = 0
)

// Version is a record format version, written as "major.minor".
type Version struct {
	Major int
	Minor int
}

// Current is the version this binary writes.
var Current = Version{Major: CurrentMajor, Minor: CurrentMinor}

// ParseVersion parses "major.minor".
func ParseVersion(s string) (Version, error) {
	major, minor, found := strings.Cut(s, ".")
	if !found {
		return Version{}, fmt.Errorf("version %q: want major.minor", s)
	}

	ma, err := strconv.Atoi(major)
	if err != nil || ma < 0 {
		return Version{}, fmt.Errorf("version %q: bad major", s)
	}

	mi, err := strconv.Atoi(minor)
	if err != nil || mi < 0 {
		return Version{}, fmt.Errorf("version %q: bad minor", s)
	}

	return Version{Major: ma, Minor: mi}, nil
}

// String formats the version as "major.minor".
func (v Version) String() string {
	return fmt.Sprintf("%d.%d", v.Major, v.Minor)
}

// MarshalJSON implements json.Marshaler.
func (v Version) MarshalJSON() ([]byte, error) {
	return json.Marshal(v.String())
}

// UnmarshalJSON implements json.Unmarshaler.
func (v *Version) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("decoding version: %w", err)
	}

	parsed, err := ParseVersion(s)
	if err != nil {
		return err
	}

	*v = parsed

	return nil
}

// ReadableBy reports whether a binary at Current may read a record of version v.
func (v Version) ReadableBy() bool {
	return v.Major == CurrentMajor
}

// Newer reports whether v carries a higher minor than this binary writes, within a readable major.
func (v Version) Newer() bool {
	return v.Major == CurrentMajor && v.Minor > CurrentMinor
}
