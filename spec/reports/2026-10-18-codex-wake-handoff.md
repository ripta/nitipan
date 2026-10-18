# Codex Wake Handoff

**Date:** 2026-10-18

This is a handoff for a running Codex CLI session. It asks that session to verify, by experiment, what a source reading
found. Add your results to "Findings from a live session" at the end. Do not edit the sections above it.

## Why this matters

nitipan is a lease and coordination tool for coding agent sessions. Its NIT-007 proposal adds cross-machine messaging.
A per-machine daemon receives a message from a central broker and hands it to a local session. The open question is
whether that daemon can deliver into a running Codex session, and whether doing so starts a turn when the session is
idle.

The daemon runs as the same OS user, but it is not the session's child. Remote Control is banned at the user's work, so
nothing here may depend on `codex remote-control` or a hosted relay. Every test below should keep both points in mind.

## What the source says

A research agent read `openai/codex` at tag `rust-v0.163.0`, commit 83086188, along with the installed binary's
`--help` output. No app-server or session was touched. Paths are relative to `codex-rs/`.

SOURCE marks a claim read from code, DOCUMENTED marks one from the docs, and VERIFIED marks one seen in `--help`
output.

### `codex queue`

- It sends the experimental app-server method `thread/queue/add` with text input. It refuses `--no-daemon` without
  `--remote`. SOURCE: `tui/src/session_queue_commands.rs:25-50,111-126`.
- The daemon's socket is `$CODEX_HOME/app-server-control/app-server-control.sock`. Its directory must be mode 0700 and
  owned by the user. No token auth was found on the socket. SOURCE: `app-server-transport/src/transport/mod.rs:53-69`,
  `uds/src/lib.rs:116`.
- The TUI does not start a daemon. It uses one only if one answers on the socket. Otherwise it runs an app-server
  embedded in its own process. SOURCE: `tui/src/lib.rs:523-553,1027-1056`, `tui/src/startup_orchestration.rs:342-353`.
- `thread/queue/add` writes the message to a SQLite queue. Every app-server with a state database polls that queue
  every 10 seconds. SOURCE: `app-server/src/request_processors/thread_queue_processor.rs:77-95`,
  `ext/queue/src/service.rs:89-97`.
- On an idle thread, the poller starts a new turn. Mid-turn, the message waits and runs as the next turn. SOURCE:
  `ext/queue/src/service.rs:193-238,405-448,466-476,549-565`.
- An embedded TUI likely picks up queued messages too, since both processes share the SQLite file. UNVERIFIED.
- If the user interrupted the last turn with Esc, nothing is dispatched until the thread next goes idle normally.
  SOURCE: `ext/queue/src/service.rs:218-223,551`.
- Docs list `codex queue` as Stable. DOCUMENTED: https://learn.chatgpt.com/docs/developer-commands.
- Open issues report the queue starving during `/goal`, and `codex queue` exiting 0 once stored rather than once run.
  UNVERIFIED: https://github.com/openai/codex/issues/52705, https://github.com/openai/codex/issues/45976.

### App-server protocol

- `turn/start` begins a turn. `turn/steer` fails with no active turn. `thread/inject_items` adds model-visible items
  without starting a turn. DOCUMENTED: https://learn.chatgpt.com/docs/app-server.
- Transports are stdio, a WebSocket over the control socket, and `ws://`, which the docs call experimental and
  unsupported. DOCUMENTED.
- A second client can attach only to a daemon. An embedded TUI exposes no socket. SOURCE, inferred.
- `codex app-server` and the daemon are Experimental and "may change without notice." DOCUMENTED.
- Remote control is a separate daemon setting, toggled by `daemon enable-remote-control`. SOURCE:
  `app-server-daemon/src/lib.rs:740-810`.

### Session identity

Shell commands get `CODEX_THREAD_ID`, `CODEX_SESSION_ID` for the root session, and `CODEX_VERSION`. SOURCE:
`protocol/src/shell_environment.rs:6-7,166-167`, `core/src/exec_env.rs:30-50`. The docs do not mention them. This
corrects an earlier finding that Codex exports no session ID variable.

`codex queue` and `codex resume` accept a UUID or an exact session name. VERIFIED.

### Hooks, notify, and MCP

- `notify` is outbound only. It runs after each turn, and its output is discarded. SOURCE:
  `hooks/src/legacy_notify.rs:45-70`.
- SessionStart, UserPromptSubmit, PreToolUse, and PostToolUse hooks can add model context, but only during a turn.
  Async hooks do not start a turn. DOCUMENTED: https://learn.chatgpt.com/docs/hooks.
- MCP notifications are only logged, never given to the model. SOURCE:
  `rmcp-client/src/logging_client_handler.rs:52-130`.

## Experiments

Run these in order. The first four are read-only. Any test that queues a message targets only session B, a second,
throwaway session the user creates for the test. Never queue into the session running these experiments, or into any
other session. Do not start an app-server daemon unless the user approves step 8. Never enable remote control.

1. In a shell command, run `echo $CODEX_THREAD_ID $CODEX_SESSION_ID`. Two UUIDs, with the thread ID matching `/status`,
   confirm session identity.
2. Run `ls -ld ~/.codex/app-server-control`, then `ls -l ~/.codex/app-server-control/`. No socket means the TUI is
   embedded. A 0700 directory confirms the boundary.
3. Run `codex app-server daemon version`. It reports whether a daemon is running without starting one.
4. Run `codex features list`. Look for a feature flag that gates the queue.
5. Leave session B idle with its TUI embedded. From another shell, run
   `codex queue --thread <B's thread ID> --message "reply with the word PING"`. B starting a turn within about 10
   seconds confirms delivery to an embedded TUI. No turn refutes it.
6. Repeat step 5 while B is mid-turn. PING running as the next turn, after the current one ends, confirms the mid-turn
   behavior.
7. Interrupt a turn in B with Esc, then queue a message. It staying queued until B's next normal turn ends confirms the
   interrupt stall.
8. Only if the user approves starting a local daemon without remote control: run `codex app-server daemon start`.
   Relaunch B, and confirm `/status` reports a local daemon. Repeat step 5. Delivery confirms the daemon path. Stop the
   daemon afterward if the user asks.

## Findings from a live session

Record the Codex version, the OS, and the date. For each experiment, give its number, what you ran, what happened, and
whether it confirms or refutes the claim. Note anything the source reading got wrong.
