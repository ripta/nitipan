package record

import (
	"encoding/json"
	"fmt"
)

// Platform tags for ProcessStart. The shapes behind them are frozen.
const (
	PlatformDarwin = "darwin"
	PlatformLinux  = "linux"
)

// ProcessStart is the tagged value that identifies when a process began.
//
// macOS holds the start timeval. Linux holds the boot ID, the `starttime` ticks, and the inode of
// the PID namespace of the process that wrote the record. Ticks repeat across a reboot, and a
// reader in another PID namespace cannot see the holder, so those two extras are part of the value.
// A platform tag a reader does not know makes liveness unknown.
type ProcessStart struct {
	Platform string

	Sec  int64
	Usec int64

	BootID string
	Ticks  uint64
	PIDNS  uint64

	// raw keeps an unknown platform's fields, so rewriting a record never drops them.
	raw json.RawMessage
}

// wireStart is the JSON shape of a ProcessStart.
type wireStart struct {
	Platform string `json:"platform"`
	Sec      int64  `json:"sec,omitempty"`
	Usec     int64  `json:"usec,omitempty"`
	BootID   string `json:"boot_id,omitempty"`
	Ticks    uint64 `json:"ticks,omitempty"`
	PIDNS    uint64 `json:"pidns,omitempty"`
}

// Known reports whether this binary understands the platform tag.
func (s ProcessStart) Known() bool {
	return s.Platform == PlatformDarwin || s.Platform == PlatformLinux
}

// Equal compares two start values exactly.
func (s ProcessStart) Equal(o ProcessStart) bool {
	return s.Platform == o.Platform && s.Sec == o.Sec && s.Usec == o.Usec &&
		s.BootID == o.BootID && s.Ticks == o.Ticks && s.PIDNS == o.PIDNS
}

// String formats the value the way the name grammar for session files spells it: seconds and
// microseconds on macOS, boot ID, ticks, and PID-namespace inode on Linux.
func (s ProcessStart) String() string {
	switch s.Platform {
	case PlatformDarwin:
		return fmt.Sprintf("%d-%d", s.Sec, s.Usec)
	case PlatformLinux:
		return fmt.Sprintf("%s-%d-%d", s.BootID, s.Ticks, s.PIDNS)
	default:
		return s.Platform
	}
}

// MarshalJSON implements json.Marshaler.
func (s ProcessStart) MarshalJSON() ([]byte, error) {
	if !s.Known() && len(s.raw) > 0 {
		return s.raw, nil
	}

	data, err := json.Marshal(wireStart{
		Platform: s.Platform,
		Sec:      s.Sec,
		Usec:     s.Usec,
		BootID:   s.BootID,
		Ticks:    s.Ticks,
		PIDNS:    s.PIDNS,
	})
	if err != nil {
		return nil, fmt.Errorf("encoding process start: %w", err)
	}

	return data, nil
}

// UnmarshalJSON implements json.Unmarshaler.
func (s *ProcessStart) UnmarshalJSON(data []byte) error {
	var w wireStart
	if err := json.Unmarshal(data, &w); err != nil {
		return fmt.Errorf("decoding process start: %w", err)
	}

	*s = ProcessStart{
		Platform: w.Platform,
		Sec:      w.Sec,
		Usec:     w.Usec,
		BootID:   w.BootID,
		Ticks:    w.Ticks,
		PIDNS:    w.PIDNS,
	}

	if !s.Known() {
		s.raw = append(json.RawMessage(nil), data...)
	}

	return nil
}
