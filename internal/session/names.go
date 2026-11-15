// Package session holds what Codex hooks and opencode plugins report about their sessions, and the
// liveness rules that read it.
//
// Codex and opencode run every session's shell commands under one long-lived host process, so the
// host says nothing about whether a session is still open. A hook or plugin that the user installs
// reports it instead. The reports are small files under sessions/ in the state directory.
//
// The file names carry everything a reader needs to tell which session a file vouches for and when
// it can be swept, even when its body cannot be read.
package session

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/space-armada/nitipan/internal/record"
)

// Agent names the session rules cover.
const (
	AgentCodex    = "codex"
	AgentOpencode = "opencode"
)

// ErrBadName means a session file name does not follow the grammar.
var ErrBadName = errors.New("not a session file name")

var (
	sessionPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	startPattern   = regexp.MustCompile(`^[0-9a-f-]+$`)
	agentPattern   = regexp.MustCompile(`^[a-z][a-z0-9]*$`)
)

// ValidSession reports whether a session ID is safe to use in a file name. Any shell command in
// the session can supply the ID of a hook input, so anything outside this set is refused.
func ValidSession(id string) bool {
	return sessionPattern.MatchString(id)
}

// Key names the process a session file is tied to. For a registration it is the host. For a
// presence mark it is the TUI. For an execution mark it is the service.
type Key struct {
	Agent   string
	Session string
	PID     int

	// Start is the process start, spelled the way record.ProcessStart.String spells it.
	Start string
}

// Name is the file name without a suffix: <agent>.<session>.<pid>.<start>.
func (k Key) Name() string {
	return fmt.Sprintf("%s.%s.%d.%s", k.Agent, k.Session, k.PID, k.Start)
}

// FileName is the name of a JSON session file.
func (k Key) FileName() string {
	return k.Name() + ".json"
}

// Validate checks every field against the grammar.
func (k Key) Validate() error {
	switch {
	case !agentPattern.MatchString(k.Agent):
		return fmt.Errorf("%w: agent %q", ErrBadName, k.Agent)
	case !ValidSession(k.Session):
		return fmt.Errorf("%w: session %q", ErrBadName, k.Session)
	case k.PID <= 0:
		return fmt.Errorf("%w: pid %d", ErrBadName, k.PID)
	case !startPattern.MatchString(k.Start):
		return fmt.Errorf("%w: start %q", ErrBadName, k.Start)
	}

	return nil
}

// ParseKey reads a session file name, with or without its .json suffix. It parses the fields from
// the right, because the session is the only free-form field.
func ParseKey(name string) (Key, error) {
	name = strings.TrimSuffix(name, ".json")

	rest, start, ok := cutLast(name)
	if !ok {
		return Key{}, fmt.Errorf("%w: %q", ErrBadName, name)
	}

	rest, pidText, ok := cutLast(rest)
	if !ok {
		return Key{}, fmt.Errorf("%w: %q", ErrBadName, name)
	}

	agent, session, ok := cutLast(rest)
	if !ok {
		return Key{}, fmt.Errorf("%w: %q", ErrBadName, name)
	}

	pid, err := strconv.Atoi(pidText)
	if err != nil {
		return Key{}, fmt.Errorf("%w: pid %q", ErrBadName, pidText)
	}

	k := Key{Agent: agent, Session: session, PID: pid, Start: start}
	if err := k.Validate(); err != nil {
		return Key{}, err
	}

	return k, nil
}

// ParseStart reads a start spelled by record.ProcessStart.String for a platform. The Linux boot ID
// is stored with its dashes removed, so the three fields split cleanly at the dashes.
func ParseStart(platform, text string) (record.ProcessStart, bool) {
	parts := strings.Split(text, "-")

	switch platform {
	case record.PlatformDarwin:
		if len(parts) != 2 {
			return record.ProcessStart{}, false
		}

		sec, err1 := strconv.ParseInt(parts[0], 10, 64)
		usec, err2 := strconv.ParseInt(parts[1], 10, 64)

		if err1 != nil || err2 != nil {
			return record.ProcessStart{}, false
		}

		return record.ProcessStart{Platform: platform, Sec: sec, Usec: usec}, true
	case record.PlatformLinux:
		if len(parts) != 3 {
			return record.ProcessStart{}, false
		}

		ticks, err1 := strconv.ParseUint(parts[1], 10, 64)
		pidns, err2 := strconv.ParseUint(parts[2], 10, 64)

		if err1 != nil || err2 != nil || parts[0] == "" {
			return record.ProcessStart{}, false
		}

		return record.ProcessStart{Platform: platform, BootID: parts[0], Ticks: ticks, PIDNS: pidns}, true
	default:
		return record.ProcessStart{}, false
	}
}

// cutLast splits s at its last dot.
func cutLast(s string) (head, tail string, ok bool) {
	i := strings.LastIndexByte(s, '.')
	if i < 0 {
		return "", "", false
	}

	return s[:i], s[i+1:], true
}
