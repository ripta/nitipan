# Shared-Host Session Liveness

**Date:** 2026-10-18

This report lists every way found for a separate process, running as the same user, to tell whether a Codex or
opencode session has ended. It informs the open question "Session liveness on shared hosts" in NIT-001. It lists facts
and trade-offs only. It does not recommend a design.

## Why this matters

A nitipan lease records its holder's agent process and session ID. The lease is live while that process is alive. Live
tests found that Codex 0.163.0 and opencode 2.0.32 each run every session's shell commands under one long-lived shared
process. Under Codex, that is the managed app-server daemon. Under opencode, it is `opencode serve --service`. See
`2026-10-18-codex-adapter-handoff.md` and `2026-10-18-opencode-adapter-handoff.md`.

So a lease from a session that has ended still reads as alive while the shared process runs. Something other than the
process must say when the session ended. This report asks what each host knows, where it records it, and who can reach
it from inside which sandbox.

## Method and tags

The sources are `openai/codex` at tag `rust-v0.163.0` and `anomalyco/opencode` at commit 501b15a. Both were fetched as
GitHub tarballs. `git get-tar-commit-id` confirmed commits `83086188ae0614bd882818ff1aa23f3194962012` and
`501b15af26915ba52682e20e220e7e137e89e92f`. The local checkout at `~/projects/codex` is at an older commit, 7880414, and
was not used. Codex paths are relative to `codex-rs/`. opencode paths are relative to `packages/`.

Local observations were read-only. They used `sqlite3 -readonly` with `.schema` only, `ls`, `lsof`, `pgrep`, and a
non-blocking `flock` probe. No process was started, stopped, or signalled. Two connect attempts, one per host, failed
before any request was sent. No secret or conversation content was read into this report.

- SOURCE: read from code, with path and line.
- DOCUMENTED: from vendor docs, with URL.
- VERIFIED: observed on this machine on 2026-10-18, with the command.
- UNVERIFIED: not checked, or inferred.

"Claude sandbox" below means the Bash sandbox of the Claude Code session that wrote this report. Its network and socket
settings are per-user configuration, so other Claude Code sessions may differ.

## Codex 0.163.0

### Identity

- The session ID is the root thread's ID. SOURCE: `core/src/session/session.rs:946-952`.
- `Thread.sessionId` is shared across the session tree. Subagent threads have their own IDs. SOURCE:
  `app-server-protocol/src/protocol/v2/thread_data.rs:216-217`.

### What "ended" means in the daemon

The daemon has no notion of a TUI closing. It has a notion of a thread being loaded or unloaded.

- The daemon tracks which connections subscribe to which thread. SOURCE: `app-server/src/thread_state.rs:322-350`.
- No protocol field exposes a subscriber or attached-client count. SOURCE, a negative result: searched the `Thread` and
  `ThreadStatus` definitions in `app-server-protocol/src/protocol/v2/`.
- A graceful TUI exit sends only `thread/unsubscribe` for its threads. SOURCE:
  `tui/src/app/event_dispatch.rs:3689-3706`, `tui/src/app/thread_routing.rs:46-50`,
  `tui/src/app/voice_owner.rs:123-147`.
- `ExitMode::Immediate` does not unsubscribe. SOURCE: `tui/src/app/event_dispatch.rs:3714-3717`. The closed socket then
  removes the subscription through `connection_closed`. SOURCE:
  `app-server/src/request_processors/thread_processor.rs:3746-3759`, `app-server/src/thread_state.rs:651-675`.
  That this covers a crashed or killed TUI is UNVERIFIED.
- `/new` starts a new thread, then only unsubscribes from the old one. SOURCE:
  `tui/src/app/session_lifecycle.rs:1020-1047`.
- A thread unloads once it has had no subscribers and no activity for a delay. SOURCE:
  `app-server/src/request_processors/thread_lifecycle.rs:59-66`. A running turn resets the timer. SOURCE: same file,
  405-408.
- The delay is `thread_unload_delay_secs`. Its doc comment says "Defaults to 1800 (30 minutes); zero unloads
  immediately. Changes require a server restart." SOURCE: `config/src/config_toml.rs:349-352`.
- `~/.codex/config.toml` on this machine does not set it. VERIFIED: `grep -n thread_unload_delay ~/.codex/config.toml`
  found nothing.
- Unload shuts the thread down, sets its status to `notLoaded`, and broadcasts `thread/closed` to all connections.
  SOURCE: `app-server/src/request_processors/thread_lifecycle.rs:559-624`, `app-server/src/outgoing_message.rs:747-787`.
- DOCUMENTED, consistent with the source: switching away or calling `thread/unsubscribe` "doesn't end the session right
  away." https://learn.chatgpt.com/docs/hooks.

So every Codex signal below lags a closed TUI by up to 30 minutes by default. A TUI that stays open, even idle for days,
keeps its thread subscribed and loaded.

### Signal: app-server API over the control socket

- `thread/read` takes `{threadId, includeTurns?}`. It returns `thread.status`. SOURCE:
  `app-server-protocol/src/protocol/v2/thread.rs:1705-1723`, `app-server-protocol/src/protocol/common.rs:858-862`.
- `ThreadStatus` is `notLoaded`, `idle`, `systemError`, or `active` with flags. SOURCE: `v2/thread.rs:1679-1700`,
  `app-server/src/thread_status.rs:447-469`. An unknown thread reads `notLoaded`. SOURCE: `thread_status.rs:384-387`.
- `thread/read` reads persisted metadata for an unloaded thread through `load_persisted_thread_for_read`. The handler
  makes no resume, subscribe, or recency call. SOURCE:
  `app-server/src/request_processors/thread_processor.rs:2934-3047`.
  Whether that helper updates `recency_at` is UNVERIFIED. A thread that is neither loaded nor persisted
  errors with "thread not loaded". SOURCE: same file, 2999-3001.
- `thread/loaded/list` returns the IDs of threads "currently loaded in memory". SOURCE: `v2/thread.rs:1659-1677`,
  `thread_processor.rs:2887-2932`.
- `thread/list` returns persisted threads with live status. It filters on `archived`. SOURCE: `v2/thread.rs:1403-1480`,
  `thread_processor.rs:2749-2755`.
- `thread/status/changed` and `thread/closed` notifications are broadcast on unload. SOURCE:
  `app-server/src/thread_status.rs:242-247,333-351`, `thread_lifecycle.rs:610-615`.
- None of these methods carries the `#[experimental]` marker. Experimental methods need `capabilities.experimentalApi`.
  SOURCE: `common.rs:772-777,853-862`, `app-server/src/message_processor.rs:977-981`.
- The app-server and its daemon are documented as Experimental and "may change without notice." DOCUMENTED:
  https://learn.chatgpt.com/docs/app-server, per `2026-10-18-codex-wake-handoff.md`.
- `thread/unsubscribe` and `thread/archive` are not probes. Unsubscribe can tear down state. Archive shuts the thread
  down. SOURCE: `thread_processor.rs:1034-1095`.

Connecting:

- The transport is a WebSocket over the Unix socket. SOURCE:
  `app-server-transport/src/transport/unix_socket.rs:176-211`.
- The request path `/daemon/shutdown` triggers a daemon shutdown handshake. SOURCE: `unix_socket.rs:180-187,217-233`.
- There is no token. Protection is a 0600 socket in a 0700 per-user directory under `/private/tmp/codex-daemon-<uid>/`.
  SOURCE: `unix_socket.rs:31,111,290-304`, `uds/src/daemon_directory.rs:13-38`.
- `initialize` must come first. SOURCE: `message_processor.rs:973-975`.
- A `clientInfo.name` other than `codex_app_server_daemon` or `codex-backend` overwrites the daemon's process-global
  originator and user-agent suffix. SOURCE: `app-server/src/request_processors/initialize_processor.rs:19,132,166-194`.
- Every initialized connection is auto-subscribed to newly spawned subagent threads. SOURCE:
  `app-server/src/lib.rs:1298-1316`. A long-lived probe connection would therefore hold those threads loaded.
  UNVERIFIED beyond that source reading.
- No CLI subcommand prints thread status or the loaded-thread list. SOURCE, a negative result: every subcommand enum in
  `cli/src/main.rs`. `codex app-server proxy` relays raw bytes, so the caller still speaks WebSocket. SOURCE:
  `cli/src/main.rs:632-633,1400-1408`.

### Signal: on-disk state

- The `threads` table records `created_at`, `updated_at`, `recency_at`, `archived`, and `archived_at`. Nothing records
  loaded, unloaded, closed, or detached. SOURCE: `state/migrations/0001_threads.sql:1-19` and later migrations.
  VERIFIED: `sqlite3 -readonly ~/.codex/state_5.sqlite .schema`.
- `thread_spawn_edges.status` is `open` or `closed`, for subagent edges only. SOURCE: `state/src/model/graph.rs:8-11`,
  `core/src/agent/control/spawn_guard.rs:90`.
- `thread_history_1.sqlite` records per-turn `status`, `started_at`, and `completed_at`. It has no session-end row.
  SOURCE: `state/thread_history_migrations/0001_thread_history.sql:1-38`. VERIFIED: `.schema` on that file.
- Rollout files get no end marker. `ShutdownComplete` is not persisted. SOURCE: `rollout/src/policy.rs:160,210,224`,
  `thread-store/src/local/live_writer.rs:169-213`.
- `session_index.jsonl` holds thread names only. SOURCE: `rollout/src/session_index.rs:20-71`.
- `tui-thread-reference-capabilities/` holds empty marker files that the TUI writes. SOURCE:
  `tui/src/app_server_session.rs:603-619`. No code that removes them was found. SOURCE, a negative result.
  VERIFIED: `ls -la ~/.codex/tui-thread-reference-capabilities` showed seven files, including ended sessions.

### Signal: the thread writer lock

- `thread-writer-locks/<threadId>.lock` is created when a thread's live writer starts. The writer takes a non-blocking
  exclusive `File::try_lock` on it. SOURCE: `rollout/src/writer_lock.rs:43-86`.
- The guard lives in the live recorder entry. Its `Drop` unlocks and deletes the file. Thread shutdown removes the
  recorder. SOURCE: `writer_lock.rs:174-196`, `thread-store/src/local/live_writer.rs:33-46,132,207`.
- Stale lock files are cleaned on the next acquire. SOURCE: `writer_lock.rs:124-171`.
- Ephemeral threads likely have no lock. UNVERIFIED.
- The daemon holds the lock. VERIFIED: `lsof` on `01a1526a-….lock` showed `codex` PID 3067, fd 37u.
- The lock is a real flock. VERIFIED: from the Claude sandbox, `open(O_RDONLY)` then `flock(LOCK_EX|LOCK_NB)` returned
  errno 35, EAGAIN.
- Only one lock file existed. It was for the thread created at 21:28. The two sessions from the adapter handoff,
  `01a15245…` and `01a15251…`, had none. VERIFIED: `ls -la ~/.codex/thread-writer-locks`. That fits unload after the
  delay. The unload itself was not observed.

### Signal: hooks

- Hook events are PreToolUse, PermissionRequest, PostToolUse, PreCompact, PostCompact, SessionStart, SessionEnd,
  UserPromptSubmit, SubagentStart, SubagentStop, Stop, and Interrupt. SOURCE: `protocol/src/protocol.rs:1688-1701`.
  DOCUMENTED: https://learn.chatgpt.com/docs/hooks.
- The `hooks` feature is Stable and on by default. SOURCE: `features/src/lib.rs:1346-1350`. The docs page gives no
  stability label per event. DOCUMENTED.
- SessionEnd runs at the end of thread runtime shutdown, for root threads only. SOURCE:
  `core/src/session/handlers.rs:290-337`, `core/src/hook_runtime.rs:480-484`.
- It fires on idle unload, archive or delete, and daemon shutdown. SOURCE: `thread_lifecycle.rs:580`,
  `thread_processor.rs:1074-1081,1297-1300`. DOCUMENTED: it runs "when Codex closes normally, or after a conversation
  has been idle and isn't open in any connected client for 30 minutes."
- It does not fire on TUI exit or `/new` under the daemon. Those only unsubscribe. SOURCE: see above.
- Its input carries `session_id`, `transcript_path`, `cwd`, and `reason`. The reason is always `other`. SOURCE:
  `hooks/src/events/session_end.rs:24,63-69`.
- Its timeout defaults to 1 second, with a 3-second maximum. SOURCE: `session_end.rs:20-23`.
- Hooks run in the process that hosts the thread, which is the daemon here. They spawn `shell -lc` with no seatbelt
  wrapping. SOURCE: `hooks/src/engine/command_runner.rs:225-234,384-413`. That the daemon is the host is UNVERIFIED
  beyond the source reading.
- A daemon that crashes or is killed runs no SessionEnd. UNVERIFIED. Its process liveness already covers that case.
- Stop and legacy `notify` fire per turn, not per session. SOURCE: `core/src/session/turn.rs:720,768`,
  `hooks/src/legacy_notify.rs:45-92`.

### Sandbox reachability

- Under workspace-write, the seatbelt policy grants full-disk read. SOURCE: `protocol/src/permissions.rs:819-829`,
  `sandboxing/src/seatbelt.rs:981-985`.
- With network disabled, no network rules are emitted under `(deny default)`. Loopback TCP and Unix-socket connects are
  denied. SOURCE: `sandboxing/src/seatbelt_base_policy.sbpl:8`, `seatbelt.rs:368-379`.
- Unix-socket allowlists exist. SOURCE: `config/src/permissions_toml.rs:300,338,342,498-519`. But any policy without
  full-disk write appends an explicit deny on the daemon socket directory, after all grants. SOURCE:
  `seatbelt.rs:1058-1064`, `sandboxing/src/seatbelt_daemon.rs:7-13`. So the API is unreachable from workspace-write.
  This matches the adapter handoff's step 7 error.
- From the Claude sandbox, connecting to the socket failed with EPERM. VERIFIED: python3
  `socket(AF_UNIX).connect(~/.codex/app-server-control/app-server-control.sock)`.
- From the Claude sandbox, `sqlite3 -readonly` and `?mode=ro` on `~/.codex/state_5.sqlite` failed with "unable to open
  database file". `?immutable=1` worked. VERIFIED. That points at the WAL `-shm` file, but the cause is UNVERIFIED.
  An immutable read can miss recent WAL writes. The plain `-readonly` command on opencode's database worked.
- Reading and flocking the writer lock from inside a Codex workspace-write sandbox is UNVERIFIED. Full-disk read
  suggests it works.
- opencode runs shell commands with no OS sandbox, so it can reach the socket. SOURCE and DOCUMENTED: see
  `2026-10-18-opencode-adapter-handoff.md` step 5.

## opencode 2.0.32

### Identity and process model

- The shell tool exports `OPENCODE_SESSION_ID`. A subagent is a child session with its own ID and a `parentID`. VERIFIED
  in `2026-10-18-opencode-adapter-handoff.md`, steps 1 and 6.
- The shared service is spawned detached and unref'd. SOURCE: `client/src/service-contender.ts:20,43`.
- It never exits on idle. It stops only on a signal, or when `service.json` is replaced or unreadable, which it polls
  every 5 seconds. SOURCE: `cli/src/server-process.ts:208-209`, `cli/src/services/service-registration.ts:40-67`.
- It outlives its TUIs. VERIFIED: `pgrep -lf opencode` showed only `opencode serve --service`, PID 17556, with no TUI.
  `lsof -p 17556` showed the listener on 49374 and no client connections.
- Under `--standalone`, the private `serve --stdio --port 0` child treats EOF on its stdin pipe as "the end of its
  ownership lease." The comment says the OS closes the pipe "even when the TUI is killed." It gets SIGTERM, then a force
  kill after 3 seconds. SOURCE: `cli/src/services/standalone.ts:17-32`, `cli/src/server-process.ts:210-211,231-243`.
  So per-process liveness tracks the TUI there. Several sessions in one standalone process, such as subagents or tabs,
  still share it.

### What "ended" means in the service

The service has no concept of a session ending. A session is a durable row until someone deletes it.

- The TUI's only long-lived connection is the global `/api/event` SSE stream. SOURCE:
  `client/src/solid/connection.ts:97`.
- Each SSE subscriber is an anonymous queue. Disconnect removes the queue and nothing else. SOURCE:
  `server/src/event-feed.ts:72-80`.
- No per-session client registry exists. SOURCE, a negative result: searched `server/src` and `core/src/session` for
  attach, clients, and disconnect.
- A client disconnect does not stop server-side work. A comment on the shell path says "The server owns completion
  recording even if the submitting client disconnects." SOURCE: `core/src/session/session.ts:184`.
- After 60 minutes with no session events for a directory, the service interrupts that directory's running sessions
  with reason `inactivity`. That is per directory, not per client. SOURCE: `core/src/location-activity.ts:25-30,60-68`.
- `time.archived` exists but only import writes it. SOURCE: `core/src/session/info.ts:61`,
  `core/src/session/transfer.ts:136`. No archive route exists. SOURCE, a negative result:
  `protocol/src/groups/session.ts`.

### Signal: service HTTP API

- `GET /api/session/:sessionID` returns `Session.Info`, or a not-found error after deletion. SOURCE:
  `protocol/src/groups/session.ts:208-219`, `schema/src/session.ts:32-60`.
- `Session.Info` carries `outcome` and `time{created, updated, idle, viewed, archived}`. SOURCE:
  `schema/src/session.ts:32-60`.
- `GET /api/session/active` lists "foreground Session drains currently owned by this OpenCode process. Sessions absent
  from the result are inactive." Here active means a turn is running. SOURCE: `protocol/src/groups/session.ts:196-206`,
  `server/src/handlers/session.ts:148-156`.
- No idle/busy status route exists. SOURCE, a negative result: searched `protocol/src/groups` and `server/src`.
- `GET /api/event` is a global SSE stream with a 15-second heartbeat. It takes no session filter. Its group carries an
  "Experimental event stream routes" annotation. SOURCE: `protocol/src/groups/event.ts:44-55`,
  `server/src/handlers/event.ts:13-23`.
- Other routes sit under `/api/experimental/`, such as `session/:id/wait` and `session/:id/log`. SOURCE:
  `protocol/src/groups/session.ts:441-454,647-665`.
- No written stability promise for `/api/...` routes was found. UNVERIFIED, per `2026-10-18-opencode-wake-handoff.md`.

Connecting:

- Discovery is `~/.local/state/opencode/service.json`, mode 0600, with `id`, `version`, `url`, `pid`, and `password`.
  SOURCE: `cli/src/services/service-registration.ts:24-39`. VERIFIED: `ls -la ~/.local/state/opencode`.
- Auth is HTTP Basic with username `opencode`. Every route needs it. SOURCE: `server/src/auth.ts:20`,
  `server/src/process.ts:188-197`.
- The default bind is 127.0.0.1. SOURCE: `cli/src/server-process.ts:71`.
- `opencode api` and `opencode session list` call `Service.ensure`, which spawns a service if none runs. SOURCE:
  `cli/src/services/server-connection.ts:41-50`, `client/src/effect/service.ts:49-127`. `--server URL` with
  `OPENCODE_PASSWORD` targets an existing server without spawning. SOURCE: `server-connection.ts:24-39`.
- A standalone server listens on an ephemeral 127.0.0.1 port. Only its TUI learns the URL and password. SOURCE:
  `server/src/process.ts:142-157`, `cli/src/services/standalone.ts:24`, `cli/src/server-process.ts:86-89,206`.

### Signal: on-disk state

- Sessions live in SQLite at `~/.local/share/opencode/opencode.db`, table `session_v2`. SOURCE:
  `cli/src/database-path.ts:4-13`, `core/src/session/sql.ts:22-77`. VERIFIED:
  `sqlite3 -readonly ~/.local/share/opencode/opencode.db ".schema session_v2"`.
- `time_idle` and `idle_outcome` are written when a turn ends, except on shutdown. SOURCE:
  `core/src/session/projector.ts:412-441`.
- `time_suspended` is the execution claim. It is set at turn start and cleared at turn end. SOURCE:
  `core/src/session/store.ts:205-251`.
- A turn killed by shutdown keeps its claim. The next managed service boot resumes root sessions that still hold one.
  SOURCE: `core/src/session/execution.ts:76-80,128-135`, `server/src/process.ts:235-242`.
- Deletion removes the row. SOURCE: `core/src/session/projector.ts:552-553`.
- Nothing records a session closing. Only activity timestamps, the claim, and deletion exist. SOURCE, a negative
  result: the `session_v2` schema.
- The `event` table is likely empty under the CLI, since the bus persists only when asked. UNVERIFIED. SOURCE for the
  default: `core/src/bus.ts:203,416-418`.

### Signal: plugins

- Server plugins run in the serve process, loaded per directory, not per session. SOURCE:
  `core/src/plugin/module.ts:98-124`, `core/src/plugin.ts:238-248`.
- Their session hooks are prompt, context, compaction, generate, title, model and HTTP hooks, and retry. None marks a
  session end. SOURCE: `plugin/src/promise/session.ts:138-151`.
- Public events include `session.created`, `session.deleted`, and
  `session.execution.started|succeeded|failed|interrupted`. SOURCE: `schema/src/event-manifest.ts:71-81`,
  `schema/src/session-event.ts:49-258`.
- `session.status` and `session.idle` are defined but never emitted. SOURCE: `schema/src/session-status-event.ts:35-51`.
  A grep for `SessionStatusEvent` found it only in the manifest.
- TUI plugins run in the TUI process. A plugin package can export a `tui` entry. SOURCE: `plugin/src/host.ts:11-13,43`,
  `plugin/src/tui/plugin.ts:5-10`.
- A TUI plugin's context lists the TUI's open root-session tabs, with `sessionID`, `active`, and `busy`. SOURCE:
  `plugin/src/tui/context.ts:509-519`. Subagent sessions are not tabs. Behavior with tabs disabled is UNVERIFIED.
- TUI plugin cleanup runs in the TUI's finalizers. Those run on normal exit, and on SIGHUP, which destroys the renderer.
  SOURCE: `tui/src/plugin/context.tsx:571-593`, `tui/src/app.tsx:279-294`. Cleanup on SIGKILL or a crash does not run.
  UNVERIFIED, inferred.
- Plugins load from opencode config, so the user must install one. UNVERIFIED for the exact config key.

### Sandbox reachability

- opencode runs shell commands with no OS sandbox. VERIFIED in the adapter handoff, step 5.
- From the Claude sandbox, `service.json` passes `test -r`. Its contents were not read. VERIFIED: `test -r`.
- From the Claude sandbox, a TCP connect to 127.0.0.1:49374 was refused while PID 17556 listened there. VERIFIED: `curl
  http://127.0.0.1:49374/api/info` exited 7, "Couldn't connect", and `lsof -p 17556` showed the listener.
- From the Claude sandbox, `sqlite3 -readonly` on `opencode.db` worked. VERIFIED: the `.schema` commands above.
- From a Codex workspace-write sandbox, loopback TCP is denied. SOURCE: see the Codex sandbox section. Reading
  `opencode.db` there is UNVERIFIED. Full-disk read suggests it works.

## Claude Code, for comparison

Claude Code runs one `claude` process per session. Its subagents share that process and session ID. VERIFIED on
2026-10-18, per NIT-001 "Holder identity" and "Holder match rule". When the session ends, the process exits, so
per-process liveness is correct. UNVERIFIED as a separate test. The one gap is `/clear`, which appears to rotate the
session ID inside a running process. UNVERIFIED, per NIT-001 "Holder match rule", whose match rule already covers it.

Claude Code also has a SessionEnd hook, which fires "when a session terminates." Its matcher values are `clear`,
`resume`, `logout`, `prompt_input_exit`, and `other`. It cannot block. SessionEnd hooks share a 1.5-second budget. A
longer per-hook timeout raises it, up to 60 seconds. DOCUMENTED: https://code.claude.com/docs/en/hooks.

## Comparison

Columns: normal exit means the user quits the TUI. Crash means the TUI is killed. A crash of the shared process itself
is already caught by process liveness. Closed terminal means SIGHUP to the TUI. Idle means an open TUI with no turn
running. "Correct" for idle means it still reads as alive.

| Signal | Normal exit | TUI crash | Closed terminal | Idle, open | From Codex workspace-write | From opencode | From Claude sandbox | Stability |
|---|---|---|---|---|---|---|---|---|
| Codex: process liveness of daemon | No | No | No | Correct | Yes | Yes | Yes | Internal |
| Codex: `thread/read` status or `thread/loaded/list` | After unload delay, 30 min default | After unload delay, UNVERIFIED | After unload delay, UNVERIFIED | Correct | No, explicit deny | Yes | No, EPERM | Not marked experimental. App-server documented experimental |
| Codex: `thread/closed` notification | After unload delay | After unload delay, UNVERIFIED | After unload delay, UNVERIFIED | Correct | No | Yes, needs a long-lived connection | No | Same as above |
| Codex: writer lock held | After unload delay | After unload delay, UNVERIFIED | After unload delay, UNVERIFIED | Correct | UNVERIFIED, likely | Yes | Yes, flock probe | Internal |
| Codex: state DB `threads` | No | No | No | No end record | UNVERIFIED, likely | Yes | Yes, only with `immutable=1` | Internal |
| Codex: SessionEnd hook | After unload delay | After unload delay, UNVERIFIED | After unload delay, UNVERIFIED | Correct | N/A, runs in daemon unsandboxed | N/A | N/A | Hooks stable. Per-event label none |
| opencode shared: process liveness | No | No | No | Correct | Yes | Yes | Yes | Internal |
| opencode shared: `session.get` | No, row persists | No | No | Correct | No, loopback denied | Yes | No, refused | Undocumented |
| opencode shared: `session.active` or `time_suspended` | No, turn state only | No | No | Reads inactive | No / UNVERIFIED | Yes | No / Yes | Undocumented / internal |
| opencode shared: `/api/event` stream | No | No | No | N/A | No | Yes | No | Annotated experimental |
| opencode shared: server plugin | No | No | No | N/A | N/A, runs in service | N/A | N/A | Plugin API, no end hook |
| opencode: TUI plugin cleanup | Yes | No, UNVERIFIED | Yes | Correct | N/A, runs in TUI | N/A | N/A | Plugin API |
| opencode standalone: serve child liveness | Yes | Yes, pipe EOF | Yes | Correct | Yes | Yes | Yes | Internal |
| Claude Code: process liveness | Yes | Yes | Yes | Correct | Yes | Yes | Yes | Internal |
| Claude Code: SessionEnd hook | Yes | UNVERIFIED | UNVERIFIED | Correct | N/A | N/A | N/A | Documented |

## Trade-offs

- Every Codex signal comes from the same unload event. They lag a closed TUI by `thread_unload_delay_secs`, 30 minutes
  by default. The user can shorten it in config, and a restart is needed.
- A Codex probe must speak WebSocket over a Unix socket, run `initialize`, and pick a client name. A wrong name mutates
  daemon-global state. A long-lived connection holds new subagent threads loaded.
- Codex's sandbox denies the daemon socket on purpose, and no allowlist reopens it. Only a process outside the
  workspace-write sandbox can ask the API. A SessionEnd hook already runs outside it, in the daemon.
- The Codex writer lock answers without a socket. But a probe that takes the flock, even briefly, can make the daemon's
  own `try_lock` fail with `WouldBlock` if both race. SOURCE for the failure mode: `rollout/src/writer_lock.rs:65-72`.
  File existence alone needs no lock, but a crash leaves the file until the next acquire.
- The lock file layout and the state schema are internal. They changed names across versions, such as `state_5`.
- opencode's shared service has no server-side signal for a closed TUI at all. The only end signal found runs inside
  the TUI process, through a user-installed TUI plugin, and it misses SIGKILL.
- opencode's `--standalone` mode does not have this problem for its root session. Its serve child dies with the TUI.
- opencode probes from the shell can spawn a service as a side effect unless they pass `--server`.
- A session row in opencode, and a thread row in Codex, persists after the session ends. "Not found" means deleted, not
  ended.
