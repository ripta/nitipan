# opencode Wake Handoff

**Date:** 2026-10-18

This is a handoff for a running opencode session. It asks that session to verify, by experiment, what a source reading
found. Add your results to "Findings from a live session" at the end. Do not edit the sections above it.

## Why this matters

nitipan is a lease and coordination tool for coding agent sessions. Its NIT-007 proposal adds cross-machine messaging.
A per-machine daemon receives a message from a central broker and hands it to a local session. The open question is
whether that daemon can deliver into a running opencode session, and whether doing so starts a turn when the session is
idle.

The daemon runs as the same OS user, but it is not the session's child. Every test below should keep that in mind.

## What the source says

A research agent read `anomalyco/opencode` at commit 501b15a, the 2.0.32 release. `sst/opencode` redirects there. No
live server was touched. Paths are relative to `packages/`.

SOURCE marks a claim read from code, DOCUMENTED marks one from the docs, and VERIFIED marks one seen in `--help`
output.

### Discovery and auth

- The TUI attaches to a shared background service by default, and `--standalone` opts out. VERIFIED.
- The service listens on 127.0.0.1, port 49374 by default. SOURCE: `cli/src/services/service-config.ts:46-56`,
  `cli/src/server-process.ts:71-72`.
- A 0600 file at `<state>/service.json` holds the service's `id`, `version`, `url`, `pid`, and `password`. `<state>` is
  `$XDG_STATE_HOME` or `~/.local/state`. Whether an `opencode` subdirectory sits in between is unverified. The client
  calls this file "the complete discovery contract." SOURCE: `cli/src/services/service-registration.ts:24-39`,
  `client/src/effect/service.ts:17-20`.
- Auth is HTTP Basic with the fixed username `opencode`. The service ignores `OPENCODE_SERVER_PASSWORD`. Passwords are
  stripped from the environment that sessions inherit. SOURCE: `server/src/auth.ts:20`, `server-process.ts:90-95`,
  `cli/src/env.ts:15-21`.
- The server docs describe port 4096, `--mdns`, and an `OPENCODE_SERVER_USERNAME` override. These look stale for 2.0.32.
  DOCUMENTED.

### Delivering input

- `POST /api/session/:sessionID/prompt` takes `delivery`, either `steer` or `queue`, and an optional `resume`. The
  default is `steer`. Unless `resume` is false, it wakes the session. On an idle session, waking starts a turn. SOURCE:
  `protocol/src/groups/session.ts:317-336`, `core/src/session/session.ts:155,174`,
  `core/src/session/run-coordinator.ts:14-15`.
- During a running turn, `steer` is taken at the next step boundary. `queue` waits for the turn to end and becomes the
  next turn. Nothing is rejected. A reused message `id` returns 409. SOURCE: `core/src/session/inbox.ts:43-48`,
  `server/src/handlers/session.ts:292-298`.
- `POST /api/session/:sessionID/synthetic` behaves the same way. The TUI shows a synthetic item only when it has a
  non-empty `description`. SOURCE: `core/src/session/session.ts:296,310`, `tui/src/routes/session/rows.ts:253-259,383`.
- `GET /api/session/:id/inbox` lists pending items. SOURCE.
- `/session/:id/prompt_async` appears only in the docs. It is absent from the 2.0.32 server. DOCUMENTED, stale.
- `opencode api <operationId>` calls the running server. VERIFIED.

### Session identity

The bash tool exports `OPENCODE_SESSION_ID`, along with `OPENCODE=1` and `AGENT=1`. SOURCE:
`core/src/tool/plugin/shell.ts:213-216`.

### Other paths

- Plugins can call `session.prompt` and `session.synthetic`, so an in-process plugin could relay messages. SOURCE:
  `plugin/src/promise/session.ts:153-171`.
- MCP server notifications do not reach the model. The client handles only `list_changed` and elicitation completion.
  SOURCE, a negative result: `core/src/mcp/client.ts:157-211`.

### Stability

The `/api/...` routes are not in the official docs. A generated `protocol/openapi.json` is committed. Some routes carry
an `/api/experimental/` prefix, which suggests the others are meant to be stable. No written stability promise was
found. UNVERIFIED.

## Experiments

Run these in order. The first five are read-only. Any test that posts a message posts only into session B, a second,
throwaway session the user creates for the test. Never post into the session running these experiments, or into any
other session.

Do not write the service password into your findings. Say whether it worked instead.

1. In the bash tool, run `env | grep -E '^(OPENCODE|AGENT)'`. `OPENCODE_SESSION_ID` being set confirms session
   identity.
2. Run `opencode service status`. Then find `service.json` under `~/.local/state`, and run `ls -l` on it. A 0600 file
   whose `url` matches the status output confirms discovery. Record the full path and the port.
3. Run `opencode debug paths`. Its state path should match step 2.
4. Request `<url>/api/session/$OPENCODE_SESSION_ID/inbox` with `curl`, first without credentials, then with
   `-u opencode:<password>`. A 401 then a 200 confirms Basic auth with a fixed username.
5. Fetch `<url>/openapi.json`, or run `opencode api` with no operation. Confirm that `session.prompt` and
   `session.synthetic` exist and `prompt_async` does not. Record the request body schema for `session.prompt`.
6. Leave session B idle. From a shell that is not B, post to B with `opencode api session.prompt` or `curl`, using the
   body schema from step 5. Ask B to reply with the word PONG. B starting a turn and replying confirms idle wake. A
   message that sits in B's inbox with no turn refutes it.
7. Start a long turn in B, such as `sleep 60` in its bash tool. Post with `delivery` set to `queue`. It should appear in
   the inbox and run after the turn ends. Repeat with `steer`. It should be taken after the sleep step.
8. Post to B's `/synthetic` once without a `description` and once with one. Only the second should show in the TUI.
   Both should wake B.
9. Post to B with `resume` set to false. The item should stay pending, with no turn, until the user types something.

## Findings from a live session

Record the opencode version, the OS, and the date. For each experiment, give its number, what you ran, what happened,
and whether it confirms or refutes the claim. Note anything the source reading got wrong.
