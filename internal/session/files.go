package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/space-armada/nitipan/internal/record"
)

// Modes of a Codex host. Only a positive match on the TUI is embedded. Anything else, such as an
// app-server an IDE extension started, is a daemon, whose death reads unknown.
const (
	ModeDaemon = "daemon"
	ModeTUI    = "tui"
)

// Tri-state results of a recorded config check.
const (
	CheckPresent = "present"
	CheckAbsent  = "absent"
	CheckUnknown = "unknown"
)

// Host names the process a registration is tied to.
type Host struct {
	PID   int                 `json:"pid"`
	Start record.ProcessStart `json:"start"`
}

// Registration is a Codex SessionStart report. It is the file registered/<key>.json, where the key
// names the host.
//
// The two config checks belong to the host, not to the session. The first registration under a host
// records them, and later ones copy them.
type Registration struct {
	Version record.Version `json:"version"`
	Agent   string         `json:"agent"`
	Session string         `json:"session"`
	Host    Host           `json:"host"`

	// Mode is ModeDaemon or ModeTUI.
	Mode string `json:"mode"`

	// CodexHome locates the writer-lock directory.
	CodexHome string `json:"codex_home"`

	// UnloadDelaySecs is the thread_unload_delay_secs in force, or nil when it could not be read.
	UnloadDelaySecs *int `json:"unload_delay_secs"`

	// SessionEnd says whether a SessionEnd entry runs this nitipan: CheckPresent, CheckAbsent, or
	// CheckUnknown.
	SessionEnd string `json:"session_end"`

	RegisteredAt time.Time `json:"registered_at"`

	// Ended is true once a lock holder saw this session's end mark and the writer-lock file was
	// absent. An ended registration outlives its host and stays while any lease names the session.
	Ended   bool       `json:"ended,omitempty"`
	EndedAt *time.Time `json:"ended_at,omitempty"`
}

// Mark is an opencode presence or execution report. The key names the process it is tied to.
type Mark struct {
	Version   record.Version      `json:"version"`
	Agent     string              `json:"agent"`
	Session   string              `json:"session"`
	PID       int                 `json:"pid"`
	Start     record.ProcessStart `json:"start"`
	WrittenAt time.Time           `json:"written_at"`
}

// Key returns the key the registration's file is named by.
func (r *Registration) Key() Key {
	return Key{Agent: r.Agent, Session: r.Session, PID: r.Host.PID, Start: r.Host.Start.String()}
}

// Key returns the key the mark's file is named by.
func (m *Mark) Key() Key {
	return Key{Agent: m.Agent, Session: m.Session, PID: m.PID, Start: m.Start.String()}
}

// errUnusable marks a session file this binary must not act on. Its session reads unknown.
var errUnusable = errors.New("unusable session file")

// readable returns why a session file of this version cannot be used, or nil.
func readable(v record.Version) error {
	if v.Major == 0 && v.Minor == 0 {
		return fmt.Errorf("%w: no version", errUnusable)
	}

	if !v.ReadableBy() {
		return fmt.Errorf("%w: version %s has a higher major than this nitipan reads", errUnusable, v)
	}

	return nil
}

// writable reports whether this binary may rewrite a file of this version. An old binary never
// rewrites a session file of a higher minor.
func writable(v record.Version) error {
	if err := readable(v); err != nil {
		return err
	}

	if v.Newer() {
		return fmt.Errorf("%w: version %s is newer than this nitipan writes", errUnusable, v)
	}

	return nil
}

// decodeRegistration reads a registration and checks it against the key its file name gives.
func decodeRegistration(data []byte, key Key) (*Registration, error) {
	var r Registration
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("%w: %w", errUnusable, err)
	}

	if err := readable(r.Version); err != nil {
		return nil, err
	}

	if r.Key() != key {
		return nil, fmt.Errorf("%w: the body does not match the file name", errUnusable)
	}

	return &r, nil
}

// decodeMark reads a presence or execution mark and checks it against its file name.
func decodeMark(data []byte, key Key) (*Mark, error) {
	var m Mark
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%w: %w", errUnusable, err)
	}

	if err := readable(m.Version); err != nil {
		return nil, err
	}

	if m.Key() != key {
		return nil, fmt.Errorf("%w: the body does not match the file name", errUnusable)
	}

	return &m, nil
}

func encode(v any) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encoding a session file: %w", err)
	}

	return append(data, '\n'), nil
}
