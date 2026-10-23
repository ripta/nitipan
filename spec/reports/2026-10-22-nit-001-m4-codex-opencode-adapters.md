# NIT-001 M4: Codex and opencode adapters work-queue run

Date: 2026-10-22. Trigger: a `/work-queue` run with no arguments, which means `next 1`. It picked queue row
`NIT-001 M1-M4` from `spec/proposals/queue.md`, whose only unfinished milestone is M4. NIT-001 is the proposal for
nitipan's local lease CLI (`spec/proposals/NIT-001-local-lease-cli.md`). Environment: macOS (darwin arm64), run from
Claude Code's sandboxed Bash in `/Users/ripta/projects/nitipan`. Codex 0.163.0 and opencode are installed under
`/opt/homebrew/bin`.

The previous run, which built M1 to M3 and held M4, is reported in
[2026-10-20-nit-001-m1-m4-local-lease-cli.md](2026-10-20-nit-001-m1-m4-local-lease-cli.md).

## TL;DR

In progress. The pre-gate work is reviewed and committed with M4 `IN PROGRESS`. The run waits at the gate for the
user to run the two handoffs in real Codex and opencode sessions.

## Run state

- Code repo base commit for the run and for this row's review: `d9dbcdc`. M1 to M3 (`4cde8a9..2b1f280`) were
  reviewed on 2026-10-20
- Specs live in the code repo, so the specs commits are the code commits listed below

| Milestone | Run status | Commits |
|-----------|------------|---------|
| NIT-001 M4 | IN PROGRESS | |

M4 lands as two commits. The pre-gate commit carries the code, tests, plugins, and handoffs, with M4 `IN PROGRESS`.
The run then pauses at one gate while the user runs the live checks in real Codex and opencode sessions. The
post-gate commit carries the live fixes and the As built block, and sets M4 `DONE`.

## Decisions the user made at plan time

- Live checks: build with automated tests, then pause at one gate for the user to run handoffs in real Codex and
  opencode sessions. Live failures are fixed before M4 is `DONE`.
- Plugins: embedded in the binary, and `setup` writes them to a directory the user names. Printing long plugin source
  to stdout and expecting a redirect is not acceptable. This amends "Granting the sandbox allowance". `setup` still
  never writes a settings file.

## Best guesses made at plan time

1. `time_suspended` is read by running `sqlite3 -readonly`, with no Go SQLite dependency. Any failure reads unknown.
   If wrong, a dependency is added.
2. The Codex hook config format, SessionStart payload, and hook trust come from Codex 0.163.0 docs and source, since
   no report covers them.
3. opencode cannot run inside this sandbox, which gives EPERM on its log file. The plugin API comes from source reading
   of opencode 2.0.32. The gate proves it.
4. The plugins are TypeScript, which opencode loads directly.
5. The plugin-writing flag is `nitipan setup opencode --install-plugins <dir>`. It refuses to overwrite a differing
   file without `--replace`.
6. Notices for a moved host or a cleared end mark reuse the tombstone record with a new optional kind field. That is a
   minor addition to a format nothing has released.
7. The `sessions/` layout extends the existing Undecided ADR candidate on the state directory layout.

## Milestones

### NIT-001 M4

#### Pre-gate implementation

The implementer built the `internal/session` package, `nitipan hook` (subcommands `session-start`, `session-end`,
`presence`, and `execution`), the Codex and opencode rules and `acquire` gates, the host move in holder sync, the
`setup` text, `setup opencode --install-plugins <dir> [--replace]`, the plugins under `plugins/opencode/nitipan/`,
and the two handoff files:

- [2026-10-22-nit-001-m4-codex-live-checks.md](2026-10-22-nit-001-m4-codex-live-checks.md)
- [2026-10-22-nit-001-m4-opencode-live-checks.md](2026-10-22-nit-001-m4-opencode-live-checks.md)

Checks: `make ci` passed for the implementer, with 0 lint issues. `proposals.py check` and `proposals.py freeze`
returned `[]`. The implementer ran no Codex or opencode session and no lease smoke test. `build/nitipan list` against
the real state directory printed "no leases".

Best guesses the implementer made. They are also in the proposal's Decision Log, marked autonomous:

1. The plugin sources live in `plugins/opencode/nitipan/`, with an embed package at `plugins/embed.go`. `go:embed`
   cannot reach above `internal/`. The plan's path changes only in depth.
2. The plugins export plain `{id, setup}` objects and import nothing from opencode. `index.ts` is the server plugin
   and `tui.ts` is the TUI plugin. Session IDs are read from `data.sessionID`, `sessionID`, or
   `properties.sessionID`. The TUI plugin polls the tab list and route every 2 seconds and also subscribes to events.
   If any of this is wrong, only the TypeScript files change.
3. It is unverified whether the global `plugins/` directory loads the server plugin. `setup` also prints the
   `plugins` config fallback. The opencode handoff's steps 0 and 13 settle it.
4. A session absent from opencode's `session_v2` reads clear, since there is no turn to resume.
5. On Linux, both Codex config checks record unknown, because only macOS process starts convert to wall-clock time.
   Codex `acquire` then fails closed on Linux.
6. The cleared-end-mark notice is a tombstone with new optional fields `kind` ("revived") and `detail`. A host move
   is reported by the call that moves it and is not stored. "Claimed meanwhile" means other holders' leases acquired
   after the end was recorded.
7. The session gates run after `begin()`, so tombstones still deliver.
8. Permission modes. Codex: `read-only`, `workspace-write`, `danger-full-access`, `on-request`, `never`, from
   `codex --help`. opencode: `build`, `plan`, `auto`.
9. The hook finds Codex's home from `CODEX_HOME`, then `$HOME/.codex` from the hook's environment, then the
   user-database home.
10. A registration or mark write refuses to overwrite or remove a session file that is unusable or of a higher minor.
11. Hook input is stdin JSON, `{"session_id", "present"?, "executing"?}`. Codex's extra fields are ignored.
12. A registration is written only while the session holds a lease. The sweep removes an unended daemon registration
    whose host is dead.
13. That the Codex hook payload's `session_id` equals `CODEX_SESSION_ID`, and the SessionEnd payload shape, rest on
    docs and source only. The handoff checks them.
14. `opencode service restart` in the opencode handoff is hedged. Only `opencode service` and `opencode reload` were
    confirmed to exist.

Amendments, each marked "Amended 2026-10-22; see Decision Log": "Commands", "Granting the sandbox allowance" (the
user's plugin decision), the M4 plugin bullet, and "Session liveness on shared hosts" (the Linux unknown, a session
absent from the database, and the notice shape).

ADR candidates the implementer named: the `sessions/` layout and its name grammar, logged as Undecided as an extension
of the state-directory candidate. The hook stdin contract is a possible candidate, not logged.

#### Pre-gate review

The reviewer ran `make ci`, which passed, and `proposals.py check` and `freeze`, which returned `[]`. adr-impact found
one conflict with ADR-04. Dispositions, before the fixes land:

1. Fix (CONFIRMED). The TUI plugin marked a session before its hook call ran. A failed call was never retried.
2. Fix (adr-impact CONFIRMED, ADR-04; reviewer PLAUSIBLE). The sweep and the end-mark clear removed decoded session
   files of a higher minor. ADR-04 says an old binary never removes a session file of a higher version.
3. Trace (PLAUSIBLE). Each dead execution mark ran its own `sqlite3` call, with a 3-second timeout, under the lock.
   That could exceed the Codex SessionStart hook's 3-second limit.
4. Trace (PLAUSIBLE). A Codex re-registration cleared end marks before publishing its notice, so a failed publish
   lost the notice.
5. Fix (CONFIRMED). Stray punctuation in the Codex `setup` text.
6. Trace (PLAUSIBLE). The opencode config fallback may name the wrong key, `plugins` instead of `plugin`.
7. Fix. The opencode `setup` text printed the executable path without shell quoting.
8. Trace (PLAUSIBLE, security). The plugins logged to a predictable file in the temp directory.
9. Fix. Three proposal lines ran past 120 columns.
10. Fix. A comment on session-file upkeep contradicted the sweep.
11. Fix. A one-line method only forwarded to another.
12. Fix. Two opencode handoff steps needed caveats: one makes the live database unreadable, and one kills the TUI of
    the session running it.
13. Deferred to the final review. The 2026-10-20 ADR candidate on tombstone delivery still reads "Waits on NIT-001
    M4". M4 now settles the `hook` exception and the notice kinds.

Outcomes of the fix round:

- 1, 2, 5, 7, 9, 10, 11, and 12 are fixed. For 1, the server plugin, which has no poll, retries each execution call 3
  times, 2 seconds apart. The TypeScript was not run. Only the embedded-source checks cover it.
- 3 was confirmed by trace and fixed. The sweep makes one `sqlite3` read for all dead execution marks, bounded at 800
  ms. A failed or slow read keeps every mark.
- 4 was confirmed by trace and fixed. The notice is published before anything is removed. A failed publish aborts the
  registration and leaves the end in place.
- 6 was partly confirmed. The opencode 2.0.32 TUI config source uses `plugins` (plural), so the key was right. The
  global `plugins/` directory autoloads subdirectories for the TUI, so `plugins/nitipan/` is right. Server-side
  discovery is unverified. The printed text now says where each plugin goes. Handoff steps 0.5 and 13 record what
  worked.
- 8 was confirmed and fixed. Both plugins log to `<state dir>/plugin.log`, opened with `O_NOFOLLOW` and mode 0600.

Checks after the fixes: the coordinator ran `make ci`, which passed with 0 lint issues. `proposals.py check` and
`freeze` returned `[]`.

adr-impact also named the per-lock upkeep run by every lock holder as a possible ADR candidate. Declined: the
proposal's "Session liveness on shared hosts" already says every lock holder runs it, and it shapes only nitipan's
internals.

## Status

In progress.
