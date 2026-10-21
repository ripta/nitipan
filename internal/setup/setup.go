// Package setup writes the text that tells a user how to let an agent's sandbox reach the state
// directory.
//
// `nitipan setup` prints it, and every blocked write prints the same words. nitipan never edits an
// agent's settings: Claude Code's sandbox denies writes to its own settings file, and the other
// agents keep comments in theirs that a round-trip would drop.
package setup

import (
	"fmt"
	"strings"
)

// Agent names that setup knows.
const (
	Claude   = "claude"
	Codex    = "codex"
	Opencode = "opencode"
)

// Agents lists the agent names setup accepts.
func Agents() []string {
	return []string{Claude, Codex, Opencode}
}

// Text returns the entries an agent needs for the state directory at stateDir. The path is always
// the absolute resolved one, never `~`.
func Text(agent, stateDir string) (string, error) {
	switch agent {
	case Claude:
		return claude(stateDir), nil
	case Codex, Opencode:
		return pending(agent, stateDir), nil
	default:
		return "", fmt.Errorf("no setup entries for %q; nitipan knows %s", agent, strings.Join(Agents(), ", "))
	}
}

func claude(dir string) string {
	return fmt.Sprintf(`Claude Code needs write access to the nitipan state directory, %[1]s.

In your Claude Code settings, add to permissions.allow:

  "Edit(%[1]s/**)"
  "Bash(nitipan *)"

and add to sandbox.filesystem.allowWrite:

  "%[1]s"

If coding_agent_standards manages your settings, its setup_permissions.py adds the first and last
entries. The Bash rule only stops Claude Code from asking before each nitipan command.

If the sandbox also blocks creating the directory, create it first from a terminal outside the sandbox:

  mkdir -p -m 700 %[1]s
`, dir)
}

// pending is the text for agents whose session liveness has not landed. It names the one thing
// every agent needs, the writable state directory.
func pending(agent, dir string) string {
	return fmt.Sprintf(`nitipan cannot claim leases from %[1]s yet.

When it can, %[1]s will need write access to the state directory, %[2]s. Create the directory from a
terminal outside the sandbox with:

  mkdir -p -m 700 %[2]s
`, agent, dir)
}
