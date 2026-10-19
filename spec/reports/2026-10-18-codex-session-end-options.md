# Codex Session-End Options

**Date:** 2026-10-18

This report asks whether anything removes the unload lag in Codex 0.163.0. It follows
`2026-10-18-shared-host-session-liveness.md` and `2026-10-18-codex-adapter-handoff.md`. It lists facts and trade-offs
only. It does not recommend a design.

## Why this matters

Every Codex 0.163.0 session runs its shell commands under one shared managed app-server daemon. The daemon learns that a
session ended only when the thread unloads. That happens `thread_unload_delay_secs` after the last client unsubscribes.
The default is 1800 seconds. Closing a TUI or running `/new` only unsubscribes. So every known signal lags by up to 30
minutes. The signals are `thread/read` status, `thread/loaded/list`, the writer lock, and the SessionEnd hook.

A lease held by a closed Codex session therefore reads as live for up to 30 minutes. The user does not accept that lag.
This report looks for anything that shortens it.

## Method and tags

Sources are `openai/codex` at tag `rust-v0.163.0` (commit `83086188ae0614bd882818ff1aa23f3194962012`) and at tag
`rust-v0.164.0-alpha.3`. Both came from GitHub tarballs. The checkout at `~/projects/codex` was not used. Paths are
relative to `codex-rs/` at `rust-v0.163.0` unless stated. Issue data came from the GitHub REST API.

Local checks were read-only. They used `sqlite3 -readonly` on `~/.codex/logs_2.sqlite`, `cat` of
`~/.codex/app-server-daemon/daemon.stderr.log`, `ls`, and `lsof -U` filtered to `codex` processes. Nothing was started,
stopped, or signalled. Nothing under `~/.codex/thread-writer-locks/` was touched. The control socket was not connected
to. No log message bodies are quoted below, because the TUI writes conversation deltas into `logs_2.sqlite`.

- SOURCE: read from code, with path and line.
- DOCUMENTED: from vendor docs, with URL.
- VERIFIED: observed locally, with the command.
- UNVERIFIED: not checked, or inferred.

## Question 1: has anyone proposed or shipped a fix

The newest stable tag is `rust-v0.163.0`, published 2026-10-15. The newest tag overall is `rust-v0.164.0-alpha.3`,
published 2026-10-17. VERIFIED: `curl https://api.github.com/repos/openai/codex/releases?per_page=6`.

No fix has shipped. Several open requests ask for one. Issue states are as of 2026-10-18.

| Number | State | Opened | What it asks |
|---|---|---|---|
| #35676 | open | 2026-07-27 | A read-only subscriber-presence signal separate from loaded state. Three later comments (the last 2026-10-09) report the same need on 0.157.1 and 0.161.0 |
| #21889 | open | 2026-05-09 | An explicit `thread/unload` RPC. A 2026-10-16 comment reports the 30-minute lag on 0.163.0 |
| #52907 | open | 2026-10-10 | Tell integrations which threads have a live client subscribed, or fire SessionStart on open |
| #35542 | open | 2026-07-27 | TUI thread-owner registration with the same-user IPC router |
| #45977 | open | 2026-09-16 | A SessionIdle hook event and other idle signals for orchestrators |
| #44902 | open | 2026-09-11 | Pass the originating client's environment to hooks launched by app-server |
| #51360 | open | 2026-10-06 | SessionStart on resume fires late. The replaced startup session gets a SessionEnd without a SessionStart |

Changes that did land touch the delay but not presence.

- #42320, closed 2026-09-02, added the `thread_unload_delay_secs` key with a 60-second default. It allows zero.
- #52081, closed 2026-10-08, raised the default from 60 to 1800 seconds. Its reason was multi-agent v2 children.
- #33895, closed 2026-07-17, added SessionEnd hooks for thread teardown.
- #50380 and #51420, both closed in October, fixed threads that failed to unload after a disconnect or a slow shutdown.

The PRs above were opened by `copyberry[bot]` and closed within minutes. They mirror internal commits. Their code is in
the 0.163.0 source, so they landed. Their merge status as PRs is UNVERIFIED.

Nothing newer than 0.163.0 changes this. SOURCE: a file diff of `rust-v0.163.0` against `rust-v0.164.0-alpha.3` shows the
following files are identical: `app-server/src/request_processors/thread_lifecycle.rs`, `app-server/src/thread_state.rs`,
`app-server-protocol/src/protocol/v2/thread.rs`, `app-server-protocol/src/protocol/v2/thread_data.rs`, and
`hooks/src/events/session_end.rs`. The `thread_unload_delay_secs` key and its comment are unchanged. A scan of the 154
commit subjects between the tags found no commit about unload, presence, or SessionEnd.

A user measured the delay on the Windows Desktop backend, version 0.163.0-alpha.10.2. With the key set to 1, the context
was removed 1.125 seconds after unsubscribe. With 5, it took 5.031 seconds. Unset, it took 1,800.016 seconds. Source:
comment by K0enna on #21889, 2026-10-18. This is a third-party measurement. It is not VERIFIED locally.

## Question 2: does anything run in the TUI process

### Hooks and notify

- Hooks run in the process that hosts the thread. SOURCE: `core/src/session/handlers.rs:290-337`. Under the daemon, that
  is the daemon.
- The TUI does not depend on `codex-hooks`. SOURCE, a negative result: `tui/Cargo.toml`.
- The TUI config has no user-defined command field. `status_line` is a list of item names. SOURCE:
  `config/src/types.rs:925`.
- Legacy `notify` and the Stop hook fire per turn, in the thread host. SOURCE: see the liveness report.
- No TUI-side extension, plugin, or exit hook was found. SOURCE, a negative result: `tui/src` and `tui/Cargo.toml`.

### MCP servers that the daemon starts

- Each thread owns its MCP connections in `SessionServices.mcp_runtime`. SOURCE: `core/src/state/service.rs:52-53`.
- Thread shutdown stops that runtime. It also terminates unified-exec processes first. SOURCE:
  `core/src/session/handlers.rs:313,330`. SessionEnd hooks run after both. SOURCE: `handlers.rs:335`.
- Stdio MCP servers launch from the process that hosts the thread. SOURCE: `rmcp-client/src/stdio_server_launcher.rs:280-315`.
- So stdin EOF reaches such a server at unload. That is the same moment as every other signal. A user measured the MCP
  process exiting before backend closure on unload. Source: the K0enna comment on #21889.
- The daemon's own stderr log shows it acting as an MCP client. VERIFIED: `cat ~/.codex/app-server-daemon/daemon.stderr.log`
  shows three `rmcp::transport::streamable_http_client` errors, "fail to delete session", each for a `127.0.0.1` URL.

### An MCP server that the TUI hosts

This is the one TUI-side component that exists.

- A TUI started against the daemon, or embedded with worktrees on, binds `127.0.0.1:0` and serves a streamable-HTTP MCP
  server. SOURCE: `tui/src/dynamic_tools_mcp.rs:140-210`, `tui/src/app/startup.rs:450-460`.
- It uses a random bearer token. The tools are task tools such as `create_thread`, `read_thread`, and
  `send_message_to_thread`. SOURCE: `dynamic_tools_mcp.rs:150-160`.
- The TUI passes the URL and token to the daemon in `thread/start` config as `mcp_servers.<namespace>`. SOURCE:
  `dynamic_tools_mcp.rs:62-82`. The daemon is therefore the MCP client.
- The listener is a tokio task. It is aborted on `Drop`. SOURCE: `dynamic_tools_mcp.rs:211-215`. Process death closes the
  socket regardless.
- One server serves every thread that this TUI starts. It outlives `/new` and `/resume` inside one TUI. SOURCE:
  `tui/src/app_server_session.rs:540-553`, reading `dynamic_tool_mcp` as a per-session field.
- VERIFIED consistency: the daemon's "fail to delete session" errors at 2026-10-19 04:30:39, 04:33:13, and 05:31:52 UTC
  fall in the same second as the idle-unload INFO lines. Command: `sqlite3 -readonly ~/.codex/logs_2.sqlite` on the
  `logs` table, filtered by target. The target URLs name ports 54021 and 56354. The TUI was gone by then.
- The DELETE failure is logged at unload. No earlier daemon WARN or ERROR for the TUI's death was found in that window.
  VERIFIED: same database, `process_uuid like 'pid:3067:%'`, window 04:30:30 to 04:30:42.
- Whether the daemon notices a dead listener sooner, for example through a dropped SSE stream, is UNVERIFIED.
- The TUI's PID is in the `process_uuid` column of its own `logs` rows, as `pid:<PID>:<hash>`. VERIFIED: a TUI process
  `pid:76905:…` wrote rows from 04:28:05 to 04:33:05 UTC. Those rows carry no `thread_id`.
- Mapping a thread to its TUI's port is not exposed. No environment variable, file, or protocol field was found that
  names the port for a thread. The port is inside the thread's start config. Whether that config is persisted in the
  rollout is UNVERIFIED.
- In a Codex workspace-write sandbox, loopback TCP is denied. SOURCE: see the liveness report. Listing listeners with
  `lsof` from that sandbox is UNVERIFIED.

### The TUI can run embedded instead

- `AppServerTarget` has `Embedded`, `LocalDaemon`, and `Remote`. SOURCE: `tui/src/lib.rs:329-338`.
- `--no-daemon` runs the TUI "without the shared background server, even if it is already running." SOURCE:
  `tui/src/cli.rs:88-89`.
- These also force the embedded path: `--oss`, `--profile`, `--strict-config`, `--dangerously-bypass-hook-trust`,
  `CODEX_EXEC_SERVER_URL`, and any `-c` override outside a short allowlist. SOURCE: `tui/src/daemon_startup.rs:70-125`.
- On Windows, an elevated launch sets `no_daemon`. SOURCE: `tui/src/startup_orchestration.rs:25-33`.
- Embedded mode uses `InProcessAppServerClient`. SOURCE: `tui/src/lib.rs:323`. The thread host is then the TUI process.
  Shell commands are children of the TUI. That the shell's parent is the TUI is UNVERIFIED locally.
- Under embedded mode the thread's SessionEnd and MCP stdin EOF would run in the TUI process at exit. That they run on
  a normal exit is UNVERIFIED. They cannot run on SIGKILL.

## Question 3: can the daemon's unsubscribe be observed without the socket

No path was found for the unsubscribe itself.

- `thread/unsubscribe` removes the connection from the thread's set and updates a watch channel. It emits no hook and no
  notification to other clients. SOURCE: `app-server/src/request_processors/thread_processor.rs:1034-1059`,
  `app-server/src/thread_state.rs:557-585`.
- `connection_closed` removes the connection the same way. It logs nothing on this path. SOURCE:
  `app-server/src/message_processor.rs:866-900`, `thread_state.rs:651-675`, `thread_processor.rs:3746-3759`.
- No state-db write happens at that moment. The `threads` table has no attach column. SOURCE: the liveness report.
- The first observable event is the unload. The daemon logs `thread <id> has no subscribers and is idle; shutting down`
  at INFO. SOURCE: `thread_lifecycle.rs:568`. The default filter keeps it in `logs_2.sqlite`. SOURCE:
  `state/src/log_db.rs:61-86`.
- VERIFIED: five such rows exist. Command: `sqlite3 -readonly ~/.codex/logs_2.sqlite "select … from logs where
  target='codex_app_server::request_processors::thread_lifecycle'"`. The thread ID is in the message text. The
  `thread_id` column is null.
- `logs_2.sqlite` is readable from the Claude sandbox with plain `-readonly`. VERIFIED. This differs from `state_5.sqlite`
  in the liveness report. Reading it from a Codex workspace-write sandbox is UNVERIFIED.
- The daemon's stderr log keeps only ERROR lines of the kind seen above. It does not hold the INFO unload line.
  VERIFIED: the file has five lines.
- The same timestamp carries a SessionEnd hook and the writer-lock release. They are not earlier signals.

## Question 4: setting `thread_unload_delay_secs` small

- The key is a top-level `config.toml` setting. A value of zero is accepted. SOURCE: `config/src/config_toml.rs:349-352`,
  `core/src/config/mod.rs:4031-4040`, `core/config.schema.json:8070-8075`.
- The doc comment says: "Defaults to 1800 (30 minutes); zero unloads immediately. Changes require a server restart."
- The public app-server docs page describes the 30-minute window and the "inactivity grace period." It does not mention
  the key. DOCUMENTED: https://learn.chatgpt.com/docs/app-server. Only the first 100,000 of 143,384 characters were read.
  A config-reference URL tried returned 404.
- With the value set, the timer starts when the last subscriber leaves and no turn is active. SOURCE:
  `thread_lifecycle.rs:33-60`. A running turn or an active status resets it. SOURCE: same file, `UnloadingState`.
  Pending approval counts as active status, per `matches!(…, ThreadStatus::Active { .. })`.
- A thread that unloads can be resumed from its persisted rollout. SOURCE: #42320 describes tests for "persisted-thread
  resume". The resume path itself was not read.
- What unload tears down is the cost of a short value. Each item is SOURCE unless marked.
  - Background terminal processes die. `terminate_all_processes` runs at thread shutdown. `handlers.rs:313`.
  - MCP connections close, and stdio MCP servers exit. `handlers.rs:330`.
  - In-memory context is dropped. #42320 says ephemeral threads lose their in-memory state on unload.
  - The first reconnect after unload pays a resume and MCP restart cost. UNVERIFIED as to duration.
- Multi-agent children were the reason the default went from 60 to 1800. That is the stated purpose of #52081. A short
  value may unload idle ephemeral children and lose their earlier replies. #52081 says ephemeral children are kept loaded
  so follow-ups keep their context. Whether a short value defeats that is UNVERIFIED.
- An open TUI that is idle keeps its subscription. It does not unload at any value. SOURCE: `unloading_target` returns
  `None` while `has_subscribers` is true.
- A TUI switching threads unsubscribes from the old one. With a short value, the old thread unloads soon after. SOURCE:
  `tui/src/app/session_lifecycle.rs:1020-1047` per the liveness report.
- A restart is needed to apply it. Users report that a daemon restart interrupts every running turn. Source: comments on
  #21889 and the title of #51763, "Background daemon auto-update drains active session." Neither is VERIFIED locally.
  `LifecycleCommand::Restart` exists. SOURCE: `tui/src/app/event_dispatch.rs:169`.
- The setting is global to the daemon. It cannot differ per thread or per client. SOURCE: one `thread_unload_delay` in
  `Config`.
- This machine has no setting. VERIFIED earlier: `grep -n thread_unload_delay ~/.codex/config.toml` found nothing, per
  the liveness report.
- A crashed or killed TUI closes its socket. That removes its subscription the same way. So the lag equals the value in
  that case too. SOURCE as above. Observed behavior is UNVERIFIED.
- Nitipan would not write this key. The user would.

## Question 5: is the TUI discoverable from a shell's environment

No variable names the client, its PID, or a client ID.

- The set injected into shell commands is `CODEX_THREAD_ID`, `CODEX_SESSION_ID`, `CODEX_TOOL_CALL_ID`, `CODEX_VERSION`,
  `CODEX_PERMISSION_PROFILE`, `CODEX_SANDBOX`, `CODEX_SANDBOX_NETWORK_DISABLED`, and `CODEX_CI`. SOURCE:
  `protocol/src/shell_environment.rs:6-8`, `core/src/exec_env.rs:16-20,41-70`, `core/src/spawn.rs:21-26`,
  `core/src/unified_exec/process_manager.rs:104`. The live set matches the one the task named.
- The rest of the environment follows the shell environment policy applied to the thread host's own environment. That is
  the daemon's environment, not the TUI's. SOURCE: `core/src/exec_env.rs:30-37`.
- `thread/start` carries no environment map from the client. Its only environment field is `environments`, which selects
  an environment and is marked experimental. SOURCE: `app-server-protocol/src/protocol/v2/thread.rs:136-144`.
- Issue #44902, open, asks for a client environment allowlist for hooks. That confirms the gap. The issue was read by
  title only.
- Process ancestry leads to the daemon, not a TUI. VERIFIED in `2026-10-18-codex-adapter-handoff.md`, step 13: PID 3067
  is the daemon, PID 63172 its updater loop, PID 1 launchd.
- The `sysctl kern.proc.pid` start-time probe works from inside the Codex workspace-write sandbox. VERIFIED in the same
  handoff, step 4. Signal zero returns a permission error there, which is ambiguous.
- Under `--no-daemon`, the shell's ancestry should reach the TUI. UNVERIFIED.
- The TUI's connection to the daemon is a Unix-socket WebSocket. `lsof -U` lists the daemon's socket file descriptors
  and their peer addresses. VERIFIED: `lsof -a -U -c codex -F pcn` returned the daemon (PID 3067) with a listener on
  `/private/tmp/codex-daemon-501/…` and several peer entries. No TUI was connected at that moment. Joining a peer entry
  to a thread is not possible without the protocol. A peer entry gives presence of a client process, not which thread.

## Candidate mechanisms

Columns: lag is time from the last client leaving to the signal. "Crash" is a killed TUI. "kill -9" is SIGKILL to the
TUI. "Terminal" is a closed terminal, meaning SIGHUP. Reachability is from a Codex workspace-write sandbox.

| Mechanism | Lag | Misses | Reachable from Codex workspace-write | Stability |
|---|---|---|---|---|
| Existing signals at unload: `thread/read`, `thread/loaded/list`, writer lock, SessionEnd, INFO unload log line, MCP DELETE failure | Up to the delay, 30 min by default | Nothing at unload. Everything before it | Socket: no. Lock file: likely. Logs DB: UNVERIFIED | Internal files. App-server documented experimental |
| `thread_unload_delay_secs` set to a small value, user-set | About the value, plus teardown time | Nothing beyond the value. Crash, kill -9, and terminal all reach it through socket close, UNVERIFIED | Same as the row above | Key is in the source and schema, absent from the docs page. Needs a daemon restart. Unloads kill background terminals and MCP servers |
| Run the TUI embedded with `--no-daemon`, user-chosen per launch | None. Holder PID is the TUI | None for process death. A Windows elevation or `-c` override can switch modes silently | Start-time sysctl works. Ancestry reaching the TUI is UNVERIFIED | CLI flag in the source help text. Loses the shared daemon and its features |
| TUI-hosted MCP listener on loopback, found with `lsof` | None for TUI process death | `/new` or `/resume` in a live TUI: the TUI is alive but the old thread is not attached. The thread-to-port map is not exposed. Embedded mode has no listener unless worktrees are on | Loopback TCP denied. `lsof` from the sandbox UNVERIFIED | Internal. Newly added. Random port per TUI launch |
| Daemon-socket peers via `lsof -U` | None for TUI process death | Same thread mapping gap. A probe holding a connection is itself a peer | `lsof` UNVERIFIED there | Internal |
| Upstream presence or unload API, if #35676 or #21889 lands | Prompt, per the proposals | Not available. Not in 0.164.0-alpha.3 | Socket denied, so a host-side process must ask | Would be new protocol surface |

## Trade-offs

- No mechanism removes the lag for every client without a user choice. The two with no lag need either a launch flag
  or a TUI-hosted listener that does not map to a thread.
- The shortest user-set delay still leaves a window equal to its value. It also changes what the user loses on unload.
  Background terminals and MCP servers stop when a thread is switched away from.
- The embedded mode ties the thread to one TUI process. That matches Claude Code's model. It costs the shared daemon.
  Several entry points drop to it without the user asking.
- The TUI-hosted MCP listener tells a TUI is alive. It does not tell a given thread is attached. A TUI that ran `/new`
  keeps the listener for the abandoned thread.
- Every approach that asks the daemon for state needs the socket. The sandbox denies it on purpose.
- Upstream interest is real. Several independent reporters describe the same need in #35676 and #21889. No maintainer reply
  was seen on either. The comments were read through 2026-10-18.
