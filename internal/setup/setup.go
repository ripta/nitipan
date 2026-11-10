// Package setup writes the text that tells a user how to let an agent's sandbox reach the state
// directory, and which hooks and plugins an agent needs for session liveness.
//
// `nitipan setup` prints it, and every blocked write prints the same words. nitipan never edits an
// agent's settings: Claude Code's sandbox denies writes to its own settings file, and the other
// agents keep comments in theirs that a round-trip would drop. It writes two things, each into a
// place the user names: the opencode plugin files, and the git hook that `setup git` installs into a
// checkout. Neither is a settings file.
package setup

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Agent names that setup knows.
const (
	Claude   = "claude"
	Codex    = "codex"
	Opencode = "opencode"

	// Git is not an agent. `setup git` installs the git hook that fences ref updates.
	Git = "git"
)

// Agents lists the agent names setup accepts.
func Agents() []string {
	return []string{Claude, Codex, Opencode}
}

// Input is what the text depends on. Both paths are absolute and resolved, never `~`.
type Input struct {
	// StateDir is the nitipan state directory.
	StateDir string

	// Executable is the absolute path of this nitipan. Hook entries run it by that path, because an
	// agent's hook environment may not have nitipan on PATH.
	Executable string
}

// Text returns the entries an agent needs for the state directory.
func Text(agent string, in Input) (string, error) {
	switch agent {
	case Claude:
		return claude(in), nil
	case Codex:
		return codex(in), nil
	case Opencode:
		return opencode(in), nil
	default:
		return "", fmt.Errorf("no setup entries for %q; nitipan knows %s", agent, strings.Join(Agents(), ", "))
	}
}

func claude(in Input) string {
	return claudeAccess(in.StateDir) + claudeMail(in)
}

func claudeAccess(dir string) string {
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

// MailWaitTimeoutSeconds is the timeout the printed waiter hooks carry. Claude Code documents no
// maximum for a hook timeout, so the waiter asks for a day. When it times out, the session has no
// waiter until its next Stop, and the node's socket ring covers the gap.
const MailWaitTimeoutSeconds = 86400

// hookTypeCommand is the type of a Claude Code command hook.
const hookTypeCommand = "command"

// hookEntry is one command hook of Claude Code's settings.
type hookEntry struct {
	Type        string `json:"type"`
	Command     string `json:"command"`
	AsyncRewake bool   `json:"asyncRewake,omitempty"`
	Timeout     int    `json:"timeout,omitempty"`
}

type hookGroup struct {
	Hooks []hookEntry `json:"hooks"`
}

type hookSettings struct {
	Hooks map[string][]hookGroup `json:"hooks"`
}

// ClaudeMailHooks returns the hooks mail needs, as the JSON for Claude Code's settings.
func ClaudeMailHooks(exe string) (string, error) {
	cmd := shellWord(exe)

	wait := hookEntry{Type: hookTypeCommand, Command: cmd + " hook mail-wait", AsyncRewake: true, Timeout: MailWaitTimeoutSeconds}

	settings := hookSettings{Hooks: map[string][]hookGroup{
		"SessionStart": {{Hooks: []hookEntry{{Type: hookTypeCommand, Command: cmd + " hook mail-register"}, wait}}},
		"Stop":         {{Hooks: []hookEntry{wait}}},
		"UserPromptSubmit": {{Hooks: []hookEntry{
			{Type: hookTypeCommand, Command: cmd + " hook mail-ring --event UserPromptSubmit"},
		}}},
		"PostToolUse": {{Hooks: []hookEntry{
			{Type: hookTypeCommand, Command: cmd + " hook mail-ring --event PostToolUse"},
		}}},
	}}

	out, err := json.MarshalIndent(settings, "  ", "  ")
	if err != nil {
		return "", fmt.Errorf("encoding the mail hooks: %w", err)
	}

	return string(out), nil
}

func claudeMail(in Input) string {
	hooks, err := ClaudeMailHooks(in.Executable)
	if err != nil {
		return ""
	}

	return fmt.Sprintf(`
Mail between sessions needs four hooks. Merge this into the "hooks" key of your Claude Code settings:

  %[1]s

SessionStart registers the session's mailbox, so mail sent to it reaches it, and arms a waiter. The waiter
and the Stop hook wake an idle session when mail arrives. The waiter asks for a %[2]d second timeout, since
Claude Code documents no maximum. When it expires, the node's ring to the session's inbox socket covers
the gap. UserPromptSubmit and PostToolUse tell a busy session that mail is pending. None of them reads or
removes mail. Only nitipan mail read does.

The waiter stands down when any Claude Code settings scope you can read sets crossSessionInbound to
refuse. Mail then waits until the session's next prompt, tool call, or nitipan call.
`, hooks, MailWaitTimeoutSeconds)
}

func codex(in Input) string {
	return fmt.Sprintf(`Codex needs three things before a session can acquire a lease.

1. Write access to the nitipan state directory, %[1]s.
   In ~/.codex/config.toml, add the directory to the workspace-write sandbox:

     [sandbox_workspace_write]
     writable_roots = ["%[1]s"]

   If the section exists, add the path to its writable_roots list. If the sandbox also blocks
   creating the directory, create it first from a terminal outside the sandbox:

     mkdir -p -m 700 %[1]s

2. A SessionStart hook and a SessionEnd hook that run nitipan. Add them to ~/.codex/config.toml:

     [[hooks.SessionStart]]
     [[hooks.SessionStart.hooks]]
     type = "command"
     command = %[3]s
     timeout = 3

     [[hooks.SessionEnd]]
     [[hooks.SessionEnd.hooks]]
     type = "command"
     command = %[4]s
     timeout = 3

   Codex runs a hook only after you trust it. Open /hooks in Codex and trust the entries.

   For mail reminders during turns, add both hooks and trust them too:

     [[hooks.UserPromptSubmit]]
     [[hooks.UserPromptSubmit.hooks]]
     type = "command"
     command = %[5]s
     timeout = 3

     [[hooks.PostToolUse]]
     [[hooks.PostToolUse.hooks]]
     type = "command"
     command = %[5]s
     timeout = 3

   SessionStart also registers the mailbox. Turn reminders never consume mail. The node wakes idle
   sessions through codex queue, using only the Codex path and home printed by nitipan setup node.
   Save that LaunchAgent outside the sandbox. A session under a different CODEX_HOME gets reminders
   but no queue push; nitipan mail status explains the mismatch.

3. A short unload delay. Put this line at the top of ~/.codex/config.toml, before any [table]:

     thread_unload_delay_secs = 60

   Codex fires SessionEnd only when it unloads a thread, which happens this many seconds after the
   last client leaves. The default is 1800, and nitipan refuses to acquire with more than 60. The cost
   is that a thread's background terminals and MCP servers stop 60 seconds after you switch away from
   it. The same applies after a Codex restart.

Codex reads these settings only when its host process starts. Restart the Codex daemon after you
change them. A restart interrupts every running Codex turn.

nitipan reads config.toml and hooks.json under %[2]s. It trusts a file only
when the file is older than the running host, so a change you have not restarted for reads as
unknown.
`, in.StateDir, "$CODEX_HOME (default ~/.codex)", tomlString(shellWord(in.Executable)+" hook session-start"),
		tomlString(shellWord(in.Executable)+" hook session-end"), tomlString(shellWord(in.Executable)+" hook mail-turn"))
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// shellWord quotes a path so a hook command survives a space or a metacharacter in it.
func shellWord(s string) string {
	if shellSafe.MatchString(s) {
		return s
	}

	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func opencode(in Input) string {
	return fmt.Sprintf(`opencode needs nitipan's two plugins before a session can acquire a lease. They mark the sessions
that a TUI has open and that the service is running. The server plugin also registers mailboxes,
watches for pending mail, publishes its presence, and rings with session.prompt. Only mail read
consumes a message. A missing or stale watcher is visible in nitipan mail status.

1. Install the plugins into your opencode plugins directory. This writes plugin files and nothing
   else, and it bakes nitipan's absolute path into them:

     %[2]s setup opencode --install-plugins ~/.config/opencode/plugins

   This puts the plugin in a nitipan directory under the one you name.

   The command refuses to overwrite a plugin file that differs from the one nitipan ships. Pass
   --replace to overwrite it after an upgrade.

2. Restart the opencode service so it loads the server plugin, and restart each TUI so it loads the
   TUI plugin. opencode loads a plugin directory found under the plugins directory of its config.
   If yours does not, list the absolute path of the installed nitipan directory under the "plugins"
   key. The server plugin goes in opencode.json or opencode.jsonc, and the TUI plugin in cli.json:

     "plugins": ["/absolute/path/to/plugins/nitipan"]

3. opencode runs shell commands with no OS sandbox, so the state directory, %[1]s, needs no write
   allowance. If you have tightened opencode's permissions, add these rules to opencode.jsonc:

     { "action": "shell", "resource": "nitipan *", "effect": "allow" },
     { "action": "external_directory", "resource": "%[1]s/*", "effect": "allow" },

A session started without a TUI, such as one from opencode run, the desktop app, or the API, cannot
acquire a lease, because nothing marks it as open.
`, in.StateDir, shellWord(in.Executable))
}

// tomlString quotes s as a TOML basic string.
func tomlString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)

	return `"` + r.Replace(s) + `"`
}
