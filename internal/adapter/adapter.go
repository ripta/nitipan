// Package adapter finds the holder of a call: the agent session that runs nitipan.
//
// Each supported agent gets an adapter that says how to recognize the agent's process and which
// environment variables it exports. The nearest agent process above nitipan wins, and the
// variables of an outer agent that merely passed them down are ignored.
package adapter

import (
	"path/filepath"
	"strings"

	"github.com/space-armada/nitipan/internal/record"
)

// Adapter describes one supported agent.
type Adapter struct {
	// Name is the agent name recorded in a lease.
	Name string

	// MultiSession is true when one agent process can host several concurrent sessions. Holder
	// matching then needs the session ID as well as the process identity.
	MultiSession bool

	// Liveness is the liveness kind a lease by this agent carries. Kind "session" needs a session rule
	// for the agent, and an agent without one cannot acquire.
	Liveness string

	// PermissionModes lists the values `--permission-mode` accepts. The value is self-reported and
	// advisory, so the list only guards against typos.
	PermissionModes []string

	// PIDVar names the variable that carries the agent's PID. Empty when the agent exports none.
	PIDVar string

	// SessionVar names the variable that carries the session ID. Empty when the agent exports none.
	SessionVar string

	// Markers are the variables whose presence says this agent is around, beyond PIDVar and
	// SessionVar.
	Markers []string

	// Signature recognizes the agent's process from its command name and full argument list. It is
	// nil when only the PID variable can identify the process.
	//
	// A command name alone is never enough. Claude Code installed from npm runs as `node`.
	Signature func(name string, args []string) bool
}

// vars lists every variable that signals this agent.
func (a Adapter) vars() []string {
	var out []string

	if a.PIDVar != "" {
		out = append(out, a.PIDVar)
	}

	if a.SessionVar != "" {
		out = append(out, a.SessionVar)
	}

	return append(out, a.Markers...)
}

// Claude is the Claude Code adapter. Claude Code exports its PID and session ID to every command,
// and its subagents share its holder, so it is single-session.
func Claude() Adapter {
	return Adapter{
		Name:            "claude",
		MultiSession:    false,
		Liveness:        record.LivenessProcess,
		PermissionModes: []string{"default", "acceptEdits", "plan", "auto", "dontAsk", "bypassPermissions"},
		PIDVar:          "CLAUDE_PID",
		SessionVar:      "CLAUDE_CODE_SESSION_ID",
	}
}

// Codex is the Codex adapter. Codex runs many sessions under one host process and exports no PID,
// so the walk recognizes the host by its argument list. The root session ID keys the holder, so a
// subagent thread shares its root's holder.
func Codex() Adapter {
	return Adapter{
		Name:         "codex",
		MultiSession: true,
		Liveness:     record.LivenessSession,
		SessionVar:   "CODEX_SESSION_ID",
		Markers:      []string{"CODEX_THREAD_ID", "CODEX_VERSION"},
		Signature:    codexSignature,
	}
}

// Opencode is the opencode adapter. Its shell commands run under a shared host, and each session,
// subagents included, has its own ID.
func Opencode() Adapter {
	return Adapter{
		Name:         "opencode",
		MultiSession: true,
		Liveness:     record.LivenessSession,
		SessionVar:   "OPENCODE_SESSION_ID",
		Markers:      []string{"OPENCODE_TERMINAL"},
		Signature:    opencodeSignature,
	}
}

// All returns the supported adapters in precedence order. Precedence only breaks a tie between two
// adapters that recognize the same process.
func All() []Adapter {
	return []Adapter{Claude(), Codex(), Opencode()}
}

// Names lists the agents nitipan recognizes.
func Names(adapters []Adapter) []string {
	names := make([]string, len(adapters))
	for i, a := range adapters {
		names[i] = a.Name
	}

	return names
}

// Codex host modes.
const (
	CodexDaemon = "daemon"
	CodexTUI    = "tui"
)

func codexSignature(_ string, args []string) bool {
	return len(args) > 0 && filepath.Base(args[0]) == "codex"
}

// CodexHostMode says whether a Codex process is the app-server daemon or the TUI itself. Only a
// positive match on the TUI counts as embedded, so anything unrecognized reads as a daemon.
func CodexHostMode(args []string) string {
	if !codexSignature("", args) {
		return CodexDaemon
	}

	for _, arg := range args[1:] {
		if arg == "app-server" || strings.HasPrefix(arg, "--managed-daemon") {
			return CodexDaemon
		}
	}

	return CodexTUI
}

func opencodeSignature(_ string, args []string) bool {
	return len(args) > 0 && filepath.Base(args[0]) == "opencode"
}

// OpencodeIsServe reports whether an opencode process is the shared service, as against a TUI.
// The TUI signature is any opencode process whose arguments do not start with `serve`.
func OpencodeIsServe(args []string) bool {
	return opencodeSignature("", args) && len(args) > 1 && args[1] == "serve"
}
