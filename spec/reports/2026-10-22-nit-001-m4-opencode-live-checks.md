# NIT-001 M4: opencode Live Checks

**Date:** 2026-10-22

This is a handoff for a running opencode session. It asks that session to run the live checks of NIT-001 milestone 4
for opencode, using the `nitipan` binary the milestone built. Write what you find to
`spec/reports/2026-10-22-nit-001-m4-opencode-live-results.md`, one section per step number below. Do not edit this
file.

Steps marked **USER** need a person at the keyboard. Do the steps that do not, then ask the user for each marked one in
turn. Record the exact command, the exact output, and PASS, FAIL, or NOT RUN for each step. When a step fails, record
what happened. Do not fix nitipan in this session.

## Read this first

- The binary is `/Users/ripta/projects/nitipan/build/nitipan`. Run it by that absolute path. Do not use a `nitipan` from
  `PATH`.
- nitipan has no state-directory override, by design. Every command here reads and writes the real directory,
  `/Users/ripta/.local/state/nitipan`. Other sessions may hold leases there.
- Run `nitipan list` first. Write down every lease that exists. Leave those leases alone. Never run `break` or `release`
  on a lease you did not create in this run. Release every lease you create before you finish.
- Use a scratch checkout for every `acquire`, such as a new directory under `$TMPDIR`. Never claim a real checkout.
- Do not edit `~/.config/opencode`. The user installs the plugins in step 0 with nitipan's own command.
- Run each command on its own. Do not chain commands.
- The plugins log their failures to `/Users/ripta/.local/state/nitipan/plugin.log`. Read it when a step needs it.
- Steps 9.2 and 9.5 kill a TUI and change a file mode. The session that runs this handoff must not be the one that is
  killed, and other opencode sessions must be stopped before 9.5. Each step says how.

## What the user does first (step 0, USER)

1. Run `/Users/ripta/projects/nitipan/build/nitipan setup opencode` in a terminal and read it.
2. Install the plugins:
   `/Users/ripta/projects/nitipan/build/nitipan setup opencode --install-plugins ~/.config/opencode/plugins`. The
   command prints one line per file it wrote and never prints plugin source. Record what it printed.
3. If you have tightened opencode's permissions, add the two allow rules `setup` prints.
4. Restart the opencode service so it loads the server plugin: `opencode service restart`, or the command
   `opencode service --help` names for it. Restart each TUI.
5. Record whether opencode loaded the plugins. Run `opencode plugin list` and record its output. If the plugins are not
   listed or not loaded, add the absolute path of the installed `nitipan` directory to the `plugins` array. The server
   plugin goes in `opencode.json` or `opencode.jsonc`, and the TUI plugin in `cli.json`, both under
   `~/.config/opencode`. The source reading says the key is `plugins` (plural) and an entry is a string or
   `{ "package": ..., "options": ... }`. It does not confirm that a bare directory path loads. Restart again. Record the
   exact key, the file, and the path form that worked, and the ones that did not (for example `plugin` singular, or a
   `~` path, or a `file://` path).
6. Start a TUI session in `/Users/ripta/projects/nitipan` and point it at this file. The results file is written
   there. Scratch checkouts are only for `acquire --checkout`.

Record in the results file: the opencode version (`opencode --version`), the macOS version, and the exact way the
plugins were loaded.

## Steps

### 1. Session ID and PID from inside a bash tool call (check: adapter verified in opencode)

1. Run `echo "$OPENCODE_SESSION_ID $OPENCODE $AGENT $AI_AGENT $OPENCODE_TERMINAL"`.
2. Run `/Users/ripta/projects/nitipan/build/nitipan acquire --checkout <scratch> --description "m4 live check"`.
3. Run `/Users/ripta/projects/nitipan/build/nitipan list --json`. In your lease, read `holder.agent`,
   `holder.session_id`, `holder.pid`, and `liveness`.
4. Confirm `holder.session_id` equals `OPENCODE_SESSION_ID`. Record `holder.pid`. **USER** In a terminal, run
   `ps -o pid,ppid,command -p <pid>` and record whether it is `opencode serve --service` (shared service) or
   `opencode serve --stdio ...` (standalone child).

### 2. No sandbox, the sysctl, and the allowance (checks: sysctl in the tool, `acquire` with and without entries)

1. Step 1 shows `acquire` and `list` work in the tool, so the liveness reads work. Record that `liveness` is `alive`.
2. Record whether opencode asked for permission at any step, and whether you needed the `shell` and
   `external_directory` rules from step 0.3.
3. Without a presence mark, `acquire` must fail. Use a session that has none: **USER** run
   `opencode run "run /Users/ripta/projects/nitipan/build/nitipan acquire --checkout <scratch2>"` in a terminal. Record
   the exact error. It must say the session needs a TUI with nitipan's plugin loaded (check: no-TUI session fails).
   Release nothing, because nothing was created.

### 3. One session or many per process (record in the As built block)

**USER** Open a second TUI tab or a second TUI window on another session. In each, run `acquire` with a different
scratch checkout. Run `list --json`. Record whether both leases name the same `holder.pid` and different
`holder.session_id` values. That confirms opencode hosts many sessions per process.

### 4. Do MCP servers get the session variable (record in the As built block)

From opencode's source or a test MCP server, find whether opencode passes `OPENCODE_SESSION_ID` to the MCP servers it
starts. A quick test: configure a local stdio MCP server that writes its environment to a file. Mark the answer OBSERVED
or SOURCE. Do not leave the test server configured.

### 5. Presence marks follow the tab list, the route, and running state (checks: TUI plugin marks)

Presence files live in `/Users/ripta/.local/state/nitipan/sessions/presence`. A file is named
`opencode.<session>.<tui-pid>.<tui-start>.json`. Run `ls` on the directory before and after each action. The plugin polls
every 2 seconds, so wait 5 seconds after each action.

- **USER** Open a new tab on a new session. Record the new file.
- **USER** Switch to the session's route without a tab, if the TUI allows it. Record whether a file exists.
- **USER** Close the tab. Record that the file goes. If you close the active route's tab, the route still marks the
  session until you leave it.
- **USER** Start a long turn in a session, then switch the TUI to another tab. Record whether the running session keeps
  its file while it runs, and loses it when the turn ends and no tab or route holds it.
- Record the tab-list and route reads: does the plugin see changes through the poll? Read
  `/Users/ripta/.local/state/nitipan/plugin.log` for `read failed` lines. The milestone asks whether the plugin can read the tab
  list and route as they change. Record the answer, and whether any read threw.

Record whether the `tui-pid` in each file is the TUI's PID (`ps` from a terminal).

### 6. Hot reload keeps the marks (check: survive a hot reload)

**USER** With two sessions marked, edit the harmless comment at the top of
`~/.config/opencode/plugins/nitipan/tui.ts` to force a hot reload. Do not change any other line. Wait 10 seconds. Record
that both files in `sessions/presence` are still there, and that `list --json` shows no lease of those sessions going
`dead` during the reload. Then run the install command with `--replace` to restore the file:
`/Users/ripta/projects/nitipan/build/nitipan setup opencode --install-plugins ~/.config/opencode/plugins --replace`.

### 7. Shared service and `--standalone` (check: presence write ties to the TUI in both modes)

1. In the shared-service TUI, read the presence file's `tui-pid` and compare it with the TUI process. Record it.
2. **USER** Start `opencode --standalone` in a scratch checkout. Run `acquire` in a session there. Record `holder.pid`
   from `list --json`. It must be the private `opencode serve --stdio` child, not the TUI.
3. Read the presence file for the standalone session. Its `tui-pid` must be the standalone TUI's PID, not the child's.
   Record both PIDs.
4. Read `sessions/executing` while a turn runs in the standalone session. Record the PID in the file name. It must be the
   serve child's.

### 8. The server plugin sees execution events (checks: execution events; resumed after a restart)

1. Start a long turn in a session. Run `ls /Users/ripta/.local/state/nitipan/sessions/executing` while it runs. Record
   the file. It must disappear when the turn ends (within 5 seconds).
2. Record the event names the plugin saw: there is no event log, so record only the file's appearance and removal.
3. **USER** Start a long turn, then restart the service with `opencode service restart` while the turn runs. When the
   service comes back, check whether the turn resumes and whether `sessions/executing` has a file for it under the new
   service PID. Record both. This is the "turn resumed after a service restart" check.

### 9. A session whose TUI died mid-turn (checks: reads alive until the turn ends; `time_suspended`)

1. Hold a lease in session S, and start a long turn in S.
2. **USER** Use a second TUI for this: open a second TUI window on a separate session S, in a different scratch
   checkout, and let the session that runs this handoff watch from its own TUI. Kill the second TUI, never the one that
   runs the handoff, with `kill -9 <tui pid>` while S's turn runs. The service keeps running.
3. From another session, run `list --json` every 10 seconds. Record that S's lease reads `alive` while the turn runs
   (execution mark under a live service). Record when it flips to `dead` after the turn ends.
4. **USER** Repeat, but restart the service after the kill, while the turn is still running, so the turn is suspended.
   From a terminal, run
   `sqlite3 -readonly ~/.local/share/opencode/opencode.db "select id, time_suspended from session_v2 where id='<S>'"`.
   Record the value. Record that `list --json` shows S as `unknown` while `time_suspended` is set, and what it shows
   after the turn resumes and ends.
5. **USER** Stop every other opencode session and TUI first, including the one that runs this handoff, because the
   service reads this database. Run the check from a plain terminal and write what you saw into the results file by
   hand, or hand the observation to the handoff session after it restarts. Then make the database unreadable for one
   check, without changing the real file: run
   `chmod 000 ~/.local/share/opencode/opencode.db`, then `list --json` from another session. Record the verdict and the
   `hint` field. It must be `unknown`, with a hint naming the database. Restore the mode with
   `chmod 600 ~/.local/share/opencode/opencode.db` straight after. If opencode is mid-write, skip this step and say so.

### 10. TUI signature and `opencode run` / `opencode acp` (check: presence only from the TUI plugin)

1. **USER** Run `opencode run "say hi"` and, separately, start `opencode acp`. In each, check
   `ls /Users/ripta/.local/state/nitipan/sessions/presence`. Record that neither writes a presence file, because the TUI
   plugin never loads there. The signature matches those processes, so only the plugin keeps them from writing.
2. Confirm that the server plugin may still write an execution mark for their sessions. Record what you see in
   `sessions/executing`.

### 11. A subagent session (check: child sessions follow their root)

In a TUI session that holds a lease, start a subagent. While it runs, `ls` the presence directory. Record whether the
child's session ID has a presence file under the same TUI. Run `acquire` from inside the subagent with a different
scratch checkout, if the subagent can run shell commands. It is its own holder. Record its `holder.session_id`.

### 12. Permission modes

Run `acquire` with `--permission-mode build` and with `--permission-mode nonsense`. The first must succeed. The second
must fail and list `build`, `plan`, and `auto`. Record the agents and modes opencode itself offers
(`opencode --help`, the `--auto` flag, and the agent list in the TUI), and say whether the list needs changes.

### 13. How the plugins load (record in the As built block)

Record: whether `~/.config/opencode/plugins/nitipan` loaded without an `opencode.json` entry; whether the server plugin
and the TUI plugin each loaded from it; the output of `opencode plugin list`; and any log line about the plugins. Record
whether the TUI plugin's `context.data.on` accepted the event names `session.execution.started`, `session.created`,
and the others in `tui.ts`. A failed subscription shows in the plugin log.

## Checks and the steps that cover them

| M4 check (opencode) | Step |
|---|---|
| Session ID and PID verified from inside a bash tool call | 1 |
| Liveness sysctl verified in the tool, and the allowance entries | 2 |
| Whether opencode hosts one or many sessions per process | 3 |
| Whether opencode passes its session variable to MCP servers | 4 |
| TUI plugin marks follow tabs, route, and running state; whether the plugin can read the tab list and route as they change | 5 |
| Marks survive a hot reload | 6 |
| Presence write ties to the TUI in shared-service and standalone modes | 7 |
| Server plugin sees execution events, including for a turn resumed after a restart | 8 |
| A session whose TUI died mid-turn reads alive until the turn ends; set `time_suspended` and an unreadable database read unknown | 9 |
| The TUI signature also matches `opencode run` and `opencode acp` | 10 |
| `acquire` from a session with no TUI fails and says it needs the plugin | 2 |
| Child sessions follow their root | 11 |
| Permission mode list | 12 |
| How the plugins load; the install steps work | 0, 13 |

## Results file

Write `spec/reports/2026-10-22-nit-001-m4-opencode-live-results.md` with this shape.

```markdown
# NIT-001 M4: opencode Live Results

opencode version, macOS version, date, how the plugins were loaded.

## Leases found at the start

## Step 0 ... Step 13

For each: PASS, FAIL, or NOT RUN. The commands, the output, and the findings.

## Leases left behind

None, or the IDs.
```

Release every lease you created before you finish. Run `/Users/ripta/projects/nitipan/build/nitipan list` last, and
record that only the leases from the start remain.
