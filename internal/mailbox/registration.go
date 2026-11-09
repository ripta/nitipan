package mailbox

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/space-armada/nitipan/internal/atomicfile"
	"github.com/space-armada/nitipan/internal/liveness"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/session"
	"github.com/space-armada/nitipan/internal/state"
)

// Endpoint is where a session takes a ring. It holds a path or an address, never a credential. The
// node never stores or presents a session's CLAUDE_CODE_MESSAGING_TOKEN.
type Endpoint struct {
	Socket string `json:"socket,omitempty"`
}

// Registration is one mail-capable process. It stores identity fields and every session ID the process
// has held, plus the delivery endpoint. It stores no name, since only the broker derives one.
type Registration struct {
	Version  record.Version `json:"version"`
	Identity string         `json:"identity"`

	Agent   string              `json:"agent"`
	PID     int                 `json:"pid"`
	Start   record.ProcessStart `json:"start"`
	Machine record.Machine      `json:"machine"`

	// SessionIDs is every session ID the process held, oldest first. Current is the one it holds now. A
	// /clear changes it, and the earlier ID stays reachable.
	SessionIDs []string `json:"session_ids"`
	Current    string   `json:"current"`

	Endpoint  Endpoint  `json:"endpoint"`
	UpdatedAt time.Time `json:"updated_at"`
}

// IdentityKey names a registration by the fields of the process that owns it.
func IdentityKey(agent string, pid int, start record.ProcessStart) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%s", agent, pid, start.String())))

	return hex.EncodeToString(sum[:])[:24]
}

// Envelope is the liveness envelope of the registration's process.
func (r Registration) Envelope() record.Envelope {
	return record.Envelope{
		Liveness: record.LivenessProcess, Machine: r.Machine, Holder: record.Holder{Agent: r.Agent, PID: r.PID, Start: r.Start},
	}
}

// Holds reports whether the registration ever held the session ID.
func (r Registration) Holds(sessionID string) bool { return slices.Contains(r.SessionIDs, sessionID) }

// Refresh writes a registration, merging the session IDs of an earlier one for the same identity. A
// later registration replaces the earlier one, which is how an endpoint is refreshed. It leaves a file
// of a newer minor alone and returns ErrNewer.
func Refresh(root string, r Registration, now time.Time) (Registration, error) {
	if !session.ValidSession(r.Current) {
		return r, fmt.Errorf("session ID %q: %w", r.Current, ErrBadName)
	}

	r.Identity = IdentityKey(r.Agent, r.PID, r.Start)
	r.Version = Version
	r.UpdatedAt = now.UTC()

	old, err := readRegistration(root, r.Identity+fileSuffix)

	switch {
	case err == nil:
		if old.Version.Minor > Version.Minor {
			return r, fmt.Errorf("registration %s is version %s: %w", r.Identity, old.Version, ErrNewer)
		}

		merged := slices.Clone(old.SessionIDs)
		for _, s := range r.SessionIDs {
			if !slices.Contains(merged, s) {
				merged = append(merged, s)
			}
		}

		r.SessionIDs = merged
	case errors.Is(err, fs.ErrNotExist):
	case errors.Is(err, ErrUnknownVersion):
		return r, fmt.Errorf("registration %s: %w", r.Identity, err)
	}

	if !slices.Contains(r.SessionIDs, r.Current) {
		r.SessionIDs = append(r.SessionIDs, r.Current)
	}

	data, err := json.Marshal(r)
	if err != nil {
		return r, fmt.Errorf("encoding the registration: %w", err)
	}

	if err := atomicfile.Write(filepath.Join(root, RegDir), r.Identity+fileSuffix, data); err != nil {
		return r, err
	}

	return r, nil
}

func readRegistration(root, name string) (Registration, error) {
	data, err := state.ReadName(root, RegDir, name)
	if err != nil {
		return Registration{}, err
	}

	var r Registration
	if err := json.Unmarshal(data, &r); err != nil {
		return Registration{}, fmt.Errorf("cannot be decoded: %w", err)
	}

	if err := checkVersion(r.Version); err != nil {
		return r, err
	}

	if r.Identity+fileSuffix != name || r.Current == "" || r.PID <= 0 {
		return r, errors.New("its name or fields do not match its contents")
	}

	return r, nil
}

// Registrations reads every registration. A file it cannot use is returned apart, with why.
func Registrations(root string) (regs []Registration, unknown []Unknown, err error) {
	names, err := state.ListNames(root, RegDir)
	if err != nil {
		return nil, nil, err
	}

	for _, n := range names {
		if !strings.HasSuffix(n, fileSuffix) {
			continue
		}

		r, err := readRegistration(root, n)

		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			unknown = append(unknown, Unknown{Name: n, Reason: err.Error()})
		default:
			regs = append(regs, r)
		}
	}

	return regs, unknown, nil
}

// Find reads the registration of an identity.
func Find(root, identity string) (Registration, error) {
	return readRegistration(root, identity+fileSuffix)
}

// Live is a registration whose process the checker does not read as dead.
func Live(r Registration, c *liveness.Checker) bool {
	if c == nil {
		return true
	}

	return c.Check(r.Envelope()).Status != liveness.Dead
}

// RemoveRegistration deletes a registration with its ring ledger and waiter presence.
func RemoveRegistration(root, identity string) error {
	for _, sub := range []string{RegDir, RingDir, WaitDir} {
		err := os.Remove(filepath.Join(root, sub, identity+fileSuffix))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("removing %s/%s: %w", sub, identity, err)
		}
	}

	return nil
}

// Resolution is how a stored message's session ID maps onto live registrations.
type Resolution struct {
	// Holder is the live registration whose current ID matches, or the live one that held the ID before a
	// /clear when PreClear is set. It is nil when nothing live holds the ID.
	Holder   *Registration
	PreClear bool

	// Ambiguous lists two or more live registrations that hold the same current ID.
	Ambiguous []Registration
}

// Resolve applies the address rules to a session ID: the live registration whose current ID matches,
// then a live registration that held the ID before a /clear, then nothing live.
func Resolve(regs []Registration, sessionID string, c *liveness.Checker) Resolution {
	var current, before []Registration

	for _, r := range regs {
		if !Live(r, c) {
			continue
		}

		switch {
		case r.Current == sessionID:
			current = append(current, r)
		case r.Holds(sessionID):
			before = append(before, r)
		}
	}

	switch {
	case len(current) > 1:
		return Resolution{Ambiguous: current}
	case len(current) == 1:
		return Resolution{Holder: &current[0]}
	case len(before) >= 1:
		return Resolution{Holder: &before[0], PreClear: true}
	}

	return Resolution{}
}
