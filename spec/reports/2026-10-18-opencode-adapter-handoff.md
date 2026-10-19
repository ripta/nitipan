# opencode Adapter Handoff

**Date:** 2026-10-18

This is a handoff for a running opencode session. It asks that session to verify, by experiment, what a source reading
found. Add your results to "Findings from a live session" at the end. Do not edit the sections above it.

## Why this matters

nitipan is a lease and coordination tool for coding agent sessions. A session runs `nitipan acquire` from its shell to
claim a checkout. nitipan must then work out which agent session called it, and later whether that session is still
alive. NIT-001 builds an opencode adapter for this, and its milestone 4 needs live evidence from inside opencode.

nitipan finds the holder in three steps. It reads the agent's environment variables for a session ID. It walks up the
process tree from its parent until it reaches the agent's process, since opencode exports no PID variable. It then
records that process's PID and start time. Liveness later compares the start time again, through the macOS
`kern.proc.pid` sysctl or Linux `/proc/<pid>/stat`. It never uses `kill(pid, 0)`, which returns EPERM for a live
process inside the Claude Code sandbox.

The adapter must also declare whether one opencode process can host several sessions at once. If it can, nitipan
matches a holder on its session ID as well as its process, and liveness stays per process. That is the open question
here.

Leases live under `~/.local/state/nitipan/`. If opencode sandboxes shell commands, that directory may need an allowance.
nitipan will print the needed entries but never edit opencode's config.

## What the source says

A research agent read `anomalyco/opencode` at commit 501b15a, the 2.0.32 release. Paths are relative to `packages/`.
The earlier wake handoff, `2026-10-18-opencode-wake-handoff.md`, covers delivery. This one covers identity and
liveness.

SOURCE marks a claim read from code, VERIFIED marks one seen in `--help` output, and UNVERIFIED marks one nobody has
checked.

### Session identity

- The bash tool exports `OPENCODE_SESSION_ID`, along with `OPENCODE=1` and `AGENT=1`. SOURCE:
  `core/src/tool/plugin/shell.ts:213-216`.
- No variable names the opencode process's PID. SOURCE, a negative result.
- A subagent started through the task tool runs as a child session with its own session ID. UNVERIFIED.

### Process model

- The TUI attaches to a shared background service by default, and `--standalone` opts out. VERIFIED.
- `<state>/service.json` records the service's `pid`, among other fields. SOURCE:
  `cli/src/services/service-registration.ts:24-39`.
- Under the shared service, bash tool commands are children of the service, not the TUI. One service process then hosts
  every attached session. UNVERIFIED.
- Under `--standalone`, bash tool commands are children of the TUI process. UNVERIFIED.

### Sandbox and MCP servers

- Whether opencode runs bash tool commands under an OS sandbox is UNVERIFIED.
- Whether opencode passes `OPENCODE_SESSION_ID` to the MCP servers it starts is UNVERIFIED.

## Experiments

Run these in order. Every step is read-only except step 5's test file, which you remove afterward. Do not edit any
opencode config. Do not start, stop, or restart the background service. Do not kill or signal any process except as
step 3 says. Do not write the service password into your findings. Run each command on its own, and record its exact
output and any error.

If `ps` is blocked by a sandbox, record the exact error. Then use step 4's script for start times, and say that `ps`
was unavailable.

1. Run `env | grep -E '^(OPENCODE|AGENT|AI_AGENT|CODEX|CLAUDE)'`. `OPENCODE_SESSION_ID` being set confirms session
   identity. Redact any value whose name contains TOKEN, SECRET, KEY, or PASSWORD. Note any variable that looks like a
   PID.
2. Walk the process tree. Run `echo $$ $PPID`, then `ps -o pid,ppid,lstart,comm,args -p <pid>` for the parent, and
   repeat up to PID 1. Mark which process is opencode. Record its command name and full command line exactly. nitipan
   recognizes the agent by this signature. Then run `opencode service status`, and compare the service's PID with the
   process you marked. A match confirms that commands run under the shared service.
3. Run `kill -0 <opencode pid>; echo $?`. Record whether it succeeds or fails with EPERM.
4. Run this script with the opencode PID as its argument. It reads the start time through the same sysctl nitipan
   uses. It prints a nonzero byte count and a start time for a live PID. On Linux, run `cat /proc/<pid>/stat` instead,
   and record field 22.

   ```sh
   python3 - <pid> <<'EOF'
   import ctypes, ctypes.util, struct, sys
   libc = ctypes.CDLL(ctypes.util.find_library("c"), use_errno=True)
   mib = (ctypes.c_int * 4)(1, 14, 1, int(sys.argv[1]))
   buf = ctypes.create_string_buffer(1024)
   n = ctypes.c_size_t(1024)
   rc = libc.sysctl(mib, 4, buf, ctypes.byref(n), None, ctypes.c_size_t(0))
   start = struct.unpack_from("<qi", buf.raw, 0) if n.value else None
   print("rc", rc, "errno", ctypes.get_errno(), "bytes", n.value, "start", start)
   EOF
   ```

   Then run `sh -c 'echo $$'`, and run the script again on the PID it printed. Zero bytes for that dead PID confirm that
   nitipan can tell dead from alive.
5. Record whether opencode sandboxes bash tool commands, and how that is configured. Then run
   `mkdir -p ~/.local/state/nitipan/handoff-test`, then `touch ~/.local/state/nitipan/handoff-test/opencode`. Record
   success or the exact error, and whether opencode asked for permission. If the write succeeded, remove the file and
   the directory. If it failed, record from opencode's documentation which config entry would allow it. Do not apply it.
6. If you can spawn a subagent through the task tool, have it run step 1 and report `OPENCODE_SESSION_ID`. Record
   whether it differs from yours.
7. From opencode's documentation or source, find whether MCP servers inherit `OPENCODE_SESSION_ID`. Mark the answer
   DOCUMENTED or SOURCE, with the location.
8. Record the agent modes and permission settings opencode offers, such as `build` and `plan`, and which one is active.
   nitipan's `--permission-mode` flag will accept these values for opencode.

The next two need sessions the user starts. The user points each one at this file and names the step.

9. In a second terminal, the user starts another `opencode` TUI that attaches to the same service. In it, repeat steps
   1 and 2. The same opencode PID with a different session ID confirms that one process hosts several sessions at once.
10. In a third terminal, the user starts `opencode --standalone`. In it, repeat steps 1 and 2. Record whether the marked
    process is now the TUI itself.

## Findings from a live session

Record the opencode version, the OS, and the date. For each experiment, give its number, what you ran, what happened,
and whether it confirms or refutes the claim. Note anything the source reading got wrong.

### Step 9: a second TUI attached to the shared service

opencode v2.0.32 on macOS 26.6.2 (25G83), 2026-10-18. The user started this session with a plain `opencode` in a new
WezTerm tab, and it attached to the shared service. Only step 9 was run here, which repeats steps 1 and 2.

**Step 1 repeated.** `env | grep -E '^(OPENCODE|AGENT|AI_AGENT|CODEX|CLAUDE)'` printed:

```
OPENCODE_TERMINAL=1
AGENT=1
OPENCODE=1
AI_AGENT=opencode
OPENCODE_SESSION_ID=ses_eada61d4affeMBjRmAzGJt5ypK
```

Nothing needed redacting, and no variable looks like a PID. The session ID differs from every other session in this
report:

- the first session, which ran steps 1–8: `ses_eadabbb6…`
- the step 6 subagent: `ses_eadaac77…`
- the step 10 `--standalone` session: `ses_eada777b…`

**Step 2 repeated.** `echo $$ $PPID` printed `28332 17556`. `ps` was not blocked. Each
`ps -o pid,ppid,lstart,comm,args -p <pid>` call printed:

| PID   | PPID  | Started                  | COMM (truncated by `ps`) | ARGS                                                                   |
|-------|-------|--------------------------|--------------------------|------------------------------------------------------------------------|
| 17556 | 17555 | Sun Oct 18 21:03:15 2026 | `/opt/homebrew/Ce`       | `/opt/homebrew/Cellar/opencode/2.0.32/bin/opencode serve --service`    |
| 17555 | 93982 | Sun Oct 18 21:03:14 2026 | `opencode`               | `opencode`                                                             |
| 93982 | 1222  | Sun Oct 18 19:26:44 2026 | `-zsh`                   | `-zsh`                                                                 |
| 1222  | 1     | Tue Sep  8 19:33:38 2026 | `/Applications/We`       | `/Applications/WezTerm.app/Contents/MacOS/wezterm-gui`                 |
| 1     | 0     | Tue Sep  8 19:30:52 2026 | `/sbin/launchd`          | `/sbin/launchd`                                                        |

The marked opencode process is **PID 17556**, with the command line
`/opt/homebrew/Cellar/opencode/2.0.32/bin/opencode serve --service`, and the same start time as in step 2.

`opencode service status` printed only `http://127.0.0.1:49374`. As in step 10, I compared PIDs without reading
`service.json`: `lsof -nP -iTCP:49374 -sTCP:LISTEN` showed PID 17556 listening. **That matches the marked process.**

The walk up from this session never reaches this session's own TUI. It goes through the *first* session's TUI (17555)
and that TUI's terminal shell (93982). To find this session's TUI, I ran `ps -ax -o pid,ppid,lstart,args | grep -E '[o]pencode'`:

```
17556 17555 Sun Oct 18 21:03:15 2026     /opt/homebrew/Cellar/opencode/2.0.32/bin/opencode serve --service
25230 25229 Sun Oct 18 21:07:37 2026     /opt/homebrew/Cellar/opencode/2.0.32/bin/opencode serve --stdio --port 0
17555 93982 Sun Oct 18 21:03:14 2026     opencode
27953 27793 Sun Oct 18 21:09:37 2026     opencode
25229 78272 Sun Oct 18 21:07:37 2026     opencode --standalone
```

PID 27953 is the only TUI not otherwise accounted for. Its parent, 27793, is a `-zsh` started at 21:09:35 under
WezTerm (1222). `lsof -nP -a -p 27953 -iTCP` showed three connections from 27953 to `127.0.0.1:49374`, the service
port. I am inferring, not proving, that 27953 is this session's TUI, since no variable links a session to its TUI.

**Result: confirms the claim.** Two independent top-level sessions (`ses_eadabbb6…` and `ses_eada61d4…`) run
commands under the same process, PID 17556, at the same time. With step 6's subagent, that makes at least three
sessions in one process. The opencode adapter must declare that one process hosts several sessions. nitipan must
match a holder on `OPENCODE_SESSION_ID` as well as the PID and start time.

What this means for nitipan:

- The process tree says nothing about which TUI or terminal a session belongs to. Every ancestor above the service
  belongs to whichever TUI launched it. That supports step 2's advice to stop the walk at the first `opencode serve`
  ancestor.
- If the launching TUI (17555) exits while the service lives on, the service is reparented. Its PID and start time
  stay the same, so the lease's identity holds. This was not tested here.
- Liveness by PID only shows that the service is alive. A session that is closed or idle while the service runs will
  still look alive. Detecting a dead session needs something beyond the process, such as asking the service about the
  session. Neither the source reading nor these steps cover how to do that.

**Run by:** session `ses_eada61d4affeMBjRmAzGJt5ypK`, on 2026-10-18 around 21:10 PDT.

### Step 10: `--standalone` session

opencode v2.0.32 on macOS 26.6.2 (25G83), 2026-10-18. This session was started with `opencode --standalone` in a
WezTerm terminal. Only step 10 was run here, which repeats steps 1 and 2.

**Step 1 repeated.** `env | grep -E '^(OPENCODE|AGENT|AI_AGENT|CODEX|CLAUDE)'` printed:

```
OPENCODE_TERMINAL=1
AGENT=1
OPENCODE=1
AI_AGENT=opencode
OPENCODE_SESSION_ID=ses_eada777b6ffe49cmOWqBb0F4Nr
```

Nothing needed redacting, and no variable looks like a PID. This confirms that `OPENCODE_SESSION_ID` is set under
`--standalone`. The source reading missed two variables: `OPENCODE_TERMINAL=1` and `AI_AGENT=opencode`.

**Step 2 repeated.** `echo $$ $PPID` printed `26035 25230`. Each bash tool call runs in a new shell, so a later
`ps -p 26035` printed only the header and exited 1. `ps` was not blocked. Each `ps -o pid,ppid,lstart,comm,args -p <pid>`
call printed:

| PID   | PPID  | Started                  | COMM (truncated by `ps`) | ARGS                                                                          |
|-------|-------|--------------------------|--------------------------|-------------------------------------------------------------------------------|
| 25230 | 25229 | Sun Oct 18 21:07:37 2026 | `/opt/homebrew/Ce`       | `/opt/homebrew/Cellar/opencode/2.0.32/bin/opencode serve --stdio --port 0`    |
| 25229 | 78272 | Sun Oct 18 21:07:37 2026 | `opencode`               | `opencode --standalone`                                                       |
| 78272 | 1222  | Sun Oct 18 15:19:38 2026 | `-zsh`                   | `-zsh`                                                                        |
| 1222  | 1     | Tue Sep  8 19:33:38 2026 | `/Applications/We`       | `/Applications/WezTerm.app/Contents/MacOS/wezterm-gui`                        |
| 1     | 0     | Tue Sep  8 19:30:52 2026 | `/sbin/launchd`          | `/sbin/launchd`                                                               |

The marked opencode process is **PID 25230**, with the command line
`/opt/homebrew/Cellar/opencode/2.0.32/bin/opencode serve --stdio --port 0`. It is the direct parent of the bash tool
shell.

`opencode service status` printed only `http://127.0.0.1:49374` and no PID. To compare PIDs without reading
`service.json`, which holds the password, I ran `lsof -nP -iTCP:49374 -sTCP:LISTEN`. It showed PID 17556 listening.
`ps` showed PID 17556 as `/opt/homebrew/Cellar/opencode/2.0.32/bin/opencode serve --service`, with PPID 17555, started
Sun Oct 18 21:03:15 2026. That PID does not match 25230, so this session's commands do not run under the shared
service.

**Result: refutes the claim as written.** The marked process is **not** the TUI itself. Under `--standalone`, the TUI
(`opencode --standalone`, PID 25229) starts its own private server child (`opencode serve --stdio --port 0`, PID
25230), and bash tool commands are children of that server. The two processes started in the same second. The source
reading's claim that "under `--standalone`, bash tool commands are children of the TUI process" is wrong: they are
grandchildren of the TUI.

What this means for nitipan:

- In both modes, the process that runs commands is an `opencode serve ...` process: `serve --service` under the
  shared service, and `serve --stdio --port 0` under `--standalone`. The walk up the tree should stop at the first
  ancestor whose args contain `opencode serve`, not at a process named `opencode`.
- `ps` truncates `comm` to `/opt/homebrew/Ce` for the serve process, so matching on the command name alone would miss
  it. Match on the full executable path or argv, using the `kern.procargs2` sysctl on macOS and `/proc/<pid>/cmdline`
  on Linux.
- A standalone serve process should host only its own TUI's sessions. Whether it can host more than one session at a
  time, for example a session plus task-tool subagents, was not tested here.
- The standalone server was still running alongside the shared service (PID 17556). Both kinds can exist on one machine
  at once, so nitipan cannot assume the service PID in `service.json` is the holder.

### Steps 1–8: the first session

**Run by:** session `ses_eadabbb6bffeBfoGH0tEZz87e4`, on 2026-10-18 around 21:04 PDT.
**opencode:** v2.0.32 from Homebrew (`/opt/homebrew/Cellar/opencode/2.0.32/bin/opencode`), running in the TUI attached to
the shared service. By elimination against steps 9 and 10, this session's TUI is PID 17555, the one that launched the
service.
**OS:** macOS 26.6.2 (25G83), Darwin 25.6.0, arm64.

Steps 9 and 10 were run by other sessions. Their results are above.

### Corrections to the source reading

- In v2 the tools are named `shell` and `subagent`. `bash` and `task` are their v1 names, which
  `core/src/v1/config/migrate.ts:28-32` maps over.
- Under `--standalone`, shell commands are children of a private `opencode serve --stdio --port 0` process, not of the
  TUI. See step 10.
- Steps 1 and 4 turned up two variables the source reading did not list: `OPENCODE_TERMINAL=1` and
  `AI_AGENT=opencode`.
- The service process is a child of the TUI that launched it, not a detached daemon. Its parent is the TUI. See step 2.

### 1. Environment

```
$ env | grep -E '^(OPENCODE|AGENT|AI_AGENT|CODEX|CLAUDE)'
OPENCODE_TERMINAL=1
AGENT=1
OPENCODE=1
AI_AGENT=opencode
OPENCODE_SESSION_ID=ses_eadabbb6bffeBfoGH0tEZz87e4
```

**Confirms** that session identity comes from `OPENCODE_SESSION_ID`. No variable looks like a PID, which **confirms** the
negative result. No value needed redacting.

Where the variables come from:

- `OPENCODE_TERMINAL=1` is set in the shell base environment (SOURCE: `core/src/shell.ts:276-280`).
- `AI_AGENT` uses `||=`, so a value inherited from the parent environment wins over `opencode` (SOURCE:
  `core/src/tool/plugin/shell.ts:215`).
- `AGENT=1` is a fixed flag. It does not name the active agent.

### 2. Process tree

Each shell tool call runs in a new shell, so `$$` changes from call to call. `$PPID` stayed the same.

```
$ echo $$ $PPID
18251 17556

$ ps -o pid,ppid,lstart,comm,args -p 17556
  PID  PPID STARTED                      COMM             ARGS
17556 17555 Sun Oct 18 21:03:15 2026     /opt/homebrew/Ce /opt/homebrew/Cellar/opencode/2.0.32/bin/opencode serve --service

$ ps -o pid,ppid,lstart,comm,args -p 17555
  PID  PPID STARTED                      COMM     ARGS
17555 93982 Sun Oct 18 21:03:14 2026     opencode opencode

$ ps -o pid,ppid,lstart,comm,args -p 93982
  PID  PPID STARTED                      COMM ARGS
93982  1222 Sun Oct 18 19:26:44 2026     -zsh -zsh

$ ps -o pid,ppid,lstart,comm,args -p 1222
  PID  PPID STARTED                      COMM             ARGS
 1222     1 Tue Sep  8 19:33:38 2026     /Applications/We /Applications/WezTerm.app/Contents/MacOS/wezterm-gui
```

`ps` truncates the `comm` column. With a wide header, `comm` for 17556 is the full path
`/opt/homebrew/Cellar/opencode/2.0.32/bin/opencode`.

The chain runs: shell (18251) → **17556, the opencode service, marked as the agent process** → 17555, the opencode
TUI → 93982 `-zsh` → 1222 WezTerm → 1.

- **Agent signature:** the executable's basename is `opencode` and its arguments are `serve --service`. The TUI that
  launched it has the bare command line `opencode`.
- There is no intermediate shell. The service is the direct parent of the tool's shell.

`opencode service status` printed only `http://127.0.0.1:49374`. It does not report a PID, and its `--help` lists no
flag that would. I read the PID from `~/.local/state/opencode/service.json` instead, with the password redacted:

```
{'id': 'e8e7ecde-3191-40a9-a11d-bba1a1c92e94', 'version': '2.0.32', 'url': 'http://127.0.0.1:49374', 'pid': 17556, 'password': '<redacted>'}
```

**Confirms** that under the shared service, shell tool commands are children of the service (PID 17556), not the TUI.
Step 6 also found the same service PID for a subagent's session.

**Implications for nitipan:**

- Stop the walk at the first `opencode serve` ancestor (see step 10 for why `serve`, not just `opencode`). Walking
  further would reach the TUI, and the PID would then name whichever TUI happened to launch the service.
- The service is a child of its launching TUI. It may survive that TUI exiting, and if so it gets reparented. Steps 9
  and 10 did not test this.
- `service.json` gives a second way to find the PID without walking the tree. The state directory is
  `~/.local/state/opencode/`.

### 3. kill -0

```
$ kill -0 17556; echo $?
0
```

`kill -0` succeeded with no EPERM, which fits step 5's finding that there is no OS sandbox. nitipan's choice not to rely
on `kill(pid, 0)` is still correct for Claude Code. opencode doesn't need it either way.

### 4. Start time via sysctl

```
$ python3 - 17556 <<'EOF' ... EOF
rc 0 errno 0 bytes 648 start (1792382595, 301245)

$ date -r 1792382595
Sun Oct 18 21:03:15 PDT 2026

$ sh -c 'echo $$'
18882

$ python3 - 18882 <<'EOF' ... EOF
rc 0 errno 0 bytes 0 start None
```

**Confirms** both cases:

- For the live service, `kern.proc.pid` returns 648 bytes, and the start time agrees with `ps` `lstart` (21:03:15).
- For the dead PID it returns 0 bytes with `rc 0`, so nitipan can tell dead from alive.

`ps` was available throughout.

### 5. Sandbox and the state-directory write

There is no OS sandbox:

- Running `sandbox-exec -n no-network true` from the tool shell exited 0. macOS refuses to apply a Seatbelt profile
  inside a process that already has one, so this suggests the shell is not sandboxed.
- No sandbox setting exists in `~/.config/opencode/opencode.json`, which sets only `skills`. There is no project
  `opencode.json` or `.opencode/`, and `cli.json` sets only a theme.
- SOURCE: no `sandbox-exec`, `bwrap`, `landlock` or `seatbelt` appears anywhere in the core, server, cli, desktop or tui
  packages. Shell commands are spawned directly as the host user (`core/src/shell.ts:307-316`).
- DOCUMENTED: "`shell` runs with the host user's filesystem, process, and network authority"
  (https://opencode.ai/v2/docs/permissions/#directories).

```
$ mkdir -p ~/.local/state/nitipan/handoff-test
(no output, success)
$ touch ~/.local/state/nitipan/handoff-test/opencode
(no output, success)
```

opencode did not ask for permission at either step. Cleanup:

- I removed the test file and `handoff-test/`.
- I kept `~/.local/state/nitipan/` itself. It already existed, with a birth time of 15:37:55 that afternoon.

**No allowance is needed** under the default config. opencode's only controls are its permission rules, which are not
OS enforcement.

- **Shell commands:** `external_directory` is checked only for the working directory and `cd`/`pushd` targets
  (`core/src/tool/plugin/shell.ts:123-136`). Paths in redirects or arguments, such as `nitipan acquire` writing its
  lease, fall under the `shell` rule for that command text.
- **Edit and write tools:** `external_directory`, which defaults to allow in v2, is checked first and `edit` after it
  (`core/src/file-access.ts:93-142`).
- **When a user has tightened `external_directory` or `shell`:** the entries nitipan should print are a `shell` allow
  rule for `nitipan *`, plus an `external_directory` allow rule for `~/.local/state/nitipan/*`. Neither was applied.
  Docs: https://opencode.ai/v2/docs/permissions/.

### 6. Subagent session ID

The subagent tool spawned a `general` subagent, which ran step 1 and step 2's first two commands:

```
OPENCODE_TERMINAL=1
AGENT=1
OPENCODE=1
AI_AGENT=opencode
OPENCODE_SESSION_ID=ses_eadaac775ffeJ0KB3YZJdWqj6f

$ echo $$ $PPID
19616 17556
```

**Confirms** that a subagent is a child session with its own ID: `ses_eadaac77…` against the parent's `ses_eadabbb6…`.
It runs under the **same process**, the service at PID 17556.

- One opencode process already hosts two concurrent sessions here, a parent and its child. This bears directly on the
  multi-session question, independent of step 9.
- The parent's session ID is not exported. It exists only as the child session's `parentID` (SOURCE:
  `core/src/tool/plugin/subagent.ts:185-198`).

### 7. Do MCP servers inherit OPENCODE_SESSION_ID?

**No.** SOURCE, checked by reading `core/src/mcp/client.ts` at 501b15a:

- A local (stdio) server's environment is opencode's own `process.env` plus the config's `environment`
  (`core/src/mcp/client.ts:247-258`, `core/src/mcp/stdio.ts:84-88`).
- Nothing in that path adds `OPENCODE_SESSION_ID`.
- MCP connections are scoped to a location (a working directory) and shared across sessions (`core/src/mcp/index.ts:91-95`).
  A per-session environment variable is therefore impossible.

DOCUMENTED: `environment` is "String variables added to OpenCode's inherited process environment"
(https://opencode.ai/v2/docs/mcp-servers/).

**The session ID does reach MCP servers another way.** Every `tools/call` request carries
`_meta: { "ai.opencode/sessionID": <id> }` (SOURCE: `core/src/mcp/client.ts:375`, fed from `core/src/tool/mcp.ts:64-70`).

- This works for stdio and remote servers alike.
- It is sent only on tool calls, not on resource reads or prompts.
- It is not documented.

An nitipan MCP server would read the session from `_meta`, not from the environment.

### 8. Agents and permission modes

opencode has no global permission mode like Claude Code's `--permission-mode`. The closest equivalents are its agents
and an auto-approve flag.

Primary agents, either of which can be the active "mode" (SOURCE: `core/src/plugin/agent.ts:79-140`,
`core/src/plugin/plan.ts:33-41`):

- `build` is the default.
- `plan` denies `edit` except under `~/.opencode/plan/*`.

Other agents:

- The subagents are `general` and `explore`.
- `compaction`, `title` and `summary` are hidden.
- User-defined agents can also be primary. An agent's `mode` field is `primary`, `subagent` or `all`.

Permissions:

- Rules are an ordered list of `{action, resource, effect}`, where `effect` is `allow`, `ask` or `deny`. The last
  matching rule wins, and the default is `ask` (`core/src/permission.ts:87-94`).
- Actions are `read`, `edit`, `glob`, `grep`, `shell`, `subagent`, `skill`, `question`, `webfetch`, `websearch`,
  `external_directory`, `execute`, and `<server>_<tool>` for MCP tools.
- `--auto` auto-approves anything not explicitly denied. Its hidden aliases are `--yolo` and
  `--dangerously-skip-permissions` (`cli/src/commands/commands.ts:26-36`).
- The TUI also has an `autoaccept` mode.

**Active agent:** `build`, inferred because edits are allowed and no plan-mode reminder is present. **The active agent
cannot be seen from a shell command.**

- `AGENT=1` is a fixed flag.
- No `OPENCODE_AGENT` variable or similar exists.

nitipan cannot detect the mode for opencode. `--permission-mode` would have to be supplied by the caller, with suggested
values `build`, `plan`, a custom agent name, and possibly `auto`.

### 9 and 10

Done in separate sessions. See "Step 9" and "Step 10" above.
