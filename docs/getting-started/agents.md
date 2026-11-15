# Setting up your agents

nitipan works with Claude Code, Codex, and opencode. Each agent needs a few entries before its sessions can claim
leases. `nitipan setup <agent>` prints those entries. It never edits a settings file. You paste the entries in yourself.

Run every `setup` command from the binary that `make install` put on your PATH. The printed hooks name the path of the
binary that printed them. See [Installing nitipan](install.md).

The examples below use `/Users/alice` as the home directory. Your output names your own.

## Claude Code

1. Print the entries:

   ```
   nitipan setup claude
   ```

2. Add the two permission rules it prints to `permissions.allow` in your Claude Code settings:

   ```
   "Edit(/Users/alice/.local/state/nitipan/**)"
   "Bash(nitipan *)"
   ```

   The `Bash` rule only stops Claude Code from asking before each nitipan command.

3. Add the state directory to `sandbox.filesystem.allowWrite`:

   ```
   "/Users/alice/.local/state/nitipan"
   ```

4. If the sandbox also blocks creating the directory, create it once from a terminal outside the sandbox:

   ```
   mkdir -p -m 700 /Users/alice/.local/state/nitipan
   ```

5. For mail between sessions, merge the `hooks` block that `setup claude` prints into the `hooks` key of your settings.
   It adds four hooks: `SessionStart`, `Stop`, `UserPromptSubmit`, and `PostToolUse`. Each runs `nitipan hook ...`
   with the full path of your binary. You can skip this step until you set up a broker.

6. Start a new Claude Code session so it reads the new settings.

7. Check that it works. Ask the session to run:

   ```
   nitipan acquire --resource demo:check
   nitipan release --resource demo:check
   ```

   The first command prints `acquired`, followed by the lease. The second prints `released` and the lease ID. A
   resource claim needs no git hooks, so it is a clean first test.

A Claude Code holder counts as alive while its process runs. No hook is needed for leases. The hooks only deliver
mail. The `SessionStart` waiter stands down when a Claude Code settings scope sets `crossSessionInbound` to `refuse`.

## Codex

Codex runs every session under one long-lived host process. nitipan judges a Codex holder by its session, through
hooks that report when a session starts and ends.

1. Print the entries:

   ```
   nitipan setup codex
   ```

2. Give the sandbox write access to the state directory. In `~/.codex/config.toml`:

   ```toml
   [sandbox_workspace_write]
   writable_roots = ["/Users/alice/.local/state/nitipan"]
   ```

   If the section exists, add the path to its `writable_roots` list.

3. Add the `SessionStart` and `SessionEnd` hooks that `setup codex` prints to `~/.codex/config.toml`. Each runs
   `nitipan hook session-start` or `nitipan hook session-end` with a 3 second timeout.

4. For mail reminders during turns, also add the `UserPromptSubmit` and `PostToolUse` hooks it prints. Both run
   `nitipan hook mail-turn`.

5. Put this line at the top of `~/.codex/config.toml`, before any `[table]`:

   ```toml
   thread_unload_delay_secs = 60
   ```

   Codex fires `SessionEnd` only when it unloads a thread. It unloads a thread this many seconds after the last client
   leaves. The default is 1800. nitipan refuses to acquire with more than 60. The cost is that a thread's background
   terminals and MCP servers stop 60 seconds after you switch away from it.

6. Restart the Codex daemon. Codex reads these settings only when its host process starts. A restart interrupts every
   running Codex turn.

7. Open `/hooks` in Codex and trust the new entries. Codex runs a hook only after you trust it.

8. Check that it works. Start a new Codex session and ask it to run:

   ```
   nitipan acquire --resource demo:check
   nitipan release --resource demo:check
   ```

   Until every entry is in force, `acquire` refuses and says which one is missing. The common refusals are:

   - `Codex session <id> has no registration under the Codex daemon (pid <pid>).` The `SessionStart` hook is missing,
     untrusted, or timed out.
   - `thread_unload_delay_secs is 1800 for the Codex daemon (pid <pid>) and must be 60 or less.` The delay is not set,
     or the daemon has not restarted since you set it.
   - `the recorded Codex config for the Codex daemon (pid <pid>) has no SessionEnd entry that runs nitipan.` The
     `SessionEnd` hook is missing.

   nitipan trusts `config.toml` and `hooks.json` under `$CODEX_HOME` only when the file is older than the running
   host. A change you have not restarted for reads as unknown. `CODEX_HOME` defaults to `~/.codex`.

The node wakes idle Codex sessions for mail through `codex queue`. It uses only the Codex path and home that
`nitipan setup node` prints into its LaunchAgent. See [Going multi-machine](broker-and-nodes.md).

## opencode

opencode also runs sessions under one host. nitipan judges an opencode holder through two plugins: a TUI plugin that
marks the sessions a TUI has open, and a server plugin that marks the service as running.

1. Print the steps:

   ```
   nitipan setup opencode
   ```

2. Install the plugins into your opencode plugins directory:

   ```
   nitipan setup opencode --install-plugins ~/.config/opencode/plugins
   ```

   It writes the plugin files into `~/.config/opencode/plugins/nitipan`, with nitipan's absolute path baked in. It
   writes nothing else. You should see one `wrote <path>` line for each file, then:

   ```
   Restart the opencode service and each TUI so they load the plugins.
   nitipan setup opencode prints the rest of the steps.
   ```

   The command refuses to overwrite a plugin file that differs from the one nitipan ships. After an upgrade, run it
   again with `--replace`.

3. Restart the opencode service so it loads the server plugin. Restart each TUI so it loads the TUI plugin.

4. If your opencode config does not load plugin directories from its plugins directory, list the absolute path of the
   installed `nitipan` directory under the `"plugins"` key. The server plugin goes in `opencode.json` or
   `opencode.jsonc`. The TUI plugin goes in `cli.json`.

5. opencode runs shell commands with no OS sandbox, so the state directory needs no write allowance. If you have
   tightened opencode's permissions, add the two rules that `setup opencode` prints to `opencode.jsonc`.

6. Check that it works. In a TUI session, ask the agent to run:

   ```
   nitipan acquire --resource demo:check
   nitipan release --resource demo:check
   ```

   Without the TUI plugin, `acquire` refuses with `opencode session <id> has no presence mark from a live TUI.`

A session started without a TUI cannot acquire a lease, because nothing marks it as open. That covers `opencode run`,
the desktop app, and the API.

## Next steps

- [Your first leases on one machine](local-leases.md).
- Read about [mail](../reference/mail.md) once you have a broker.
- See [troubleshooting](../operations/troubleshooting.md) when a refusal does not go away.
