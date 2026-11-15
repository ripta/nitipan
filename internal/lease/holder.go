package lease

import (
	"fmt"
	"strings"

	"github.com/space-armada/nitipan/internal/adapter"
	"github.com/space-armada/nitipan/internal/handle"
	"github.com/space-armada/nitipan/internal/record"
)

type matchKind int

const (
	matchNone matchKind = iota

	// matchSame means the record's holder is the caller.
	matchSame

	// matchSameProcess means the record belongs to another session in the caller's own agent process.
	matchSameProcess
)

// matchHolder applies the holder match rule.
//
// For liveness kind "session" the host process is not part of the match, so a host restart keeps the
// session's leases. Otherwise a single-session agent matches on process identity alone and a
// multi-session agent also needs a non-empty, equal session ID.
func matchHolder(h adapter.Holder, rec record.Holder, kind string) matchKind {
	if rec.Agent != h.Adapter.Name {
		return matchNone
	}

	if kind == record.LivenessSession {
		if h.SessionID != "" && rec.SessionID == h.SessionID {
			return matchSame
		}

		return matchNone
	}

	if rec.PID != h.PID || !rec.Start.Equal(h.Start) {
		return matchNone
	}

	if !h.Adapter.MultiSession {
		return matchSame
	}

	if h.SessionID != "" && rec.SessionID == h.SessionID {
		return matchSame
	}

	return matchSameProcess
}

// identity is the string a handle hashes. It is the same set of fields the match rule compares, so a
// holder keeps its handle across every call that the match rule says is the same holder.
func identity(h adapter.Holder) string {
	parts := []string{h.Adapter.Name}

	if h.Adapter.Liveness == record.LivenessSession {
		return strings.Join(append(parts, h.SessionID), "\x00")
	}

	parts = append(parts, fmt.Sprint(h.PID), h.Start.String())

	if h.Adapter.MultiSession {
		parts = append(parts, h.SessionID)
	}

	return strings.Join(parts, "\x00")
}

// machineKey prefers the machine ID. A host name that follows the network must not change handles.
func machineKey(m record.Machine) string {
	if m.ID != "" {
		return m.ID
	}

	return m.Host
}

func (e *Engine) handleFor(h adapter.Holder) string {
	if e.HandleOf != nil {
		return e.HandleOf(h)
	}

	return handle.Derive(identity(h), machineKey(e.Machine))
}

// DescribeHolder labels a record's holder for a person.
func DescribeHolder(h record.Holder) string { return describeHolder(h) }

// describeHolder labels the handle apart from the agent name, since only the handle is assigned by
// nitipan.
func describeHolder(h record.Holder) string {
	var parts []string

	if h.Handle != "" {
		parts = append(parts, fmt.Sprintf("handle %q", h.Handle))
	}

	if h.Name != "" {
		parts = append(parts, fmt.Sprintf("agent name %q", h.Name))
	}

	agent := h.Agent
	if agent == "" {
		agent = "an unidentified process"
	}

	if h.SessionID != "" {
		agent = fmt.Sprintf("%s session %s", agent, h.SessionID)
	}

	parts = append(parts, agent)

	if h.PID > 0 {
		parts = append(parts, fmt.Sprintf("pid %d", h.PID))
	}

	return strings.Join(parts, ", ")
}
