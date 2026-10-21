# NIT-001 M1-M4: local lease CLI work-queue run

Date: 2026-10-20. Trigger: a `/work-queue` run with no arguments, which means `next 1`. It picked queue row
`NIT-001 M1-M4` from `spec/proposals/queue.md`. NIT-001 is the proposal for nitipan's local lease CLI
(`spec/proposals/NIT-001-local-lease-cli.md`). Environment: macOS (darwin arm64), Go 1.27.1, run from Claude Code's
sandboxed Bash in `/Users/ripta/projects/nitipan`.

## TL;DR

The run builds M1 through M3. M4 (the Codex and opencode adapters) is held because it needs live interactive TUI
sessions. In progress.

## Run state

- Code repo base commit for the run and for row `NIT-001 M1-M4`: `4cde8a9`
- Specs live in the code repo, so the specs commits are the code commits listed per milestone below

| Milestone | Run status | Commit |
|-----------|------------|--------|
| NIT-001 M1 | DONE | `90d5349` |
| NIT-001 M2 | DONE | see M3 section |
| NIT-001 M3 | QUEUED | |
| NIT-001 M4 | HELD | |

The user held M4 at plan time. Its checks need interactive Codex and opencode sessions: closing a window, `/new`, a
daemon restart, a plugin hot reload, and the tab list.

Because M4 is held, the row stays unfinished. NIT-001 stays `accepted`, and the row does not move in `queue.md`. One
review after M3 serves as both the row review and the final review.

## Decisions the user made at plan time

- M4 is held.
- M1's check "the liveness check works inside the Claude Code sandbox on Linux" is skipped. The user said: "I didn't
  allow you to add a dependency on linux. I don't run linux." The user also said not to run docker commands.
- These dependencies are allowed: `golang.org/x/sys`, `golang.org/x/text`, `github.com/oklog/ulid/v2`, and
  `github.com/spf13/cobra`.
- Session reports go in `spec/reports/`.

## Best guesses made at plan time

Each guess names the ambiguity, the choice, the document it rests on, and what changes if it is wrong.

1. Linux support. NIT-001 "Liveness" says V1 supports macOS and Linux, so the Linux code is written. It is verified
   only by fixture tests and `GOOS=linux go vet`. If the user meant to drop Linux, the proposal needs an amendment, and
   the Linux code comes out.
2. The Codex and opencode adapters recognize their hosts in M1, so the walk tests have real signatures. Their session
   rules are M4 work. Until M4 lands, their records read unknown and their `acquire` fails closed. This rests on
   "Record evolution and unknown records": a reader with no session rule reads unknown. If that is wrong, the
   recognition moves to M4.
3. The lock retry budget is 5 seconds. "Waiting for the lock" says "a few seconds". A different budget is a constant.
4. Distinct exit statuses live in one documented table. "Releasing" and "Waiting for the lock" require distinct
   statuses but give no values. Changing the values later breaks scripts that test them.
5. The handle wordlist is BIP-39 English, 2048 words. The hash is SHA-256 over the holder identity and machine ID.
   "Session name and permission mode" fixes only the size. A different list changes every handle.
6. The macOS machine ID comes from `gethostuuid(2)`. Linux uses `/etc/machine-id`. "Lease record fields" says "a
   stable machine ID where the OS has one".
7. Submodule detection reads `.gitmodules` and index gitlinks through `git ls-files -s`. "Nested checkouts" names both
   sources but no mechanism.
8. The Claude Code `setup` text matches the entry shape that coding_agent_standards'
   `claude/scripts/setup_permissions.py` writes.

## Milestones

### NIT-001 M1

The M1 commit is named under M2's section, since a report cannot hold the SHA of the commit that carries it.

Checks: `make fmt-check lint vet-linux test build` passed. `make ci` passed for the implementer. Lint reported 0
issues. `proposals.py check` and `proposals.py freeze` both returned `[]`. Inside this sandbox the checks need
`GOCACHE=/tmp/claude-501/gocache GOPROXY=off GOSUMDB=off`, because the sandbox blocks `~/Library/Caches` and TLS to
the module proxy. Outside the sandbox no variables are needed.

The modules were taken from the local module cache with `GOSUMDB=off`, so the hashes in `go.sum` were never checked
against sum.golang.org. Run `go mod verify` outside the sandbox to confirm them. The versions are cobra v1.10.2,
x/sys v0.49.0, x/text v0.43.0, and ulid v2.1.0. golangci-lint is pinned at v2.15.0.

Best guesses the implementer made:

- On macOS, the machine ID comes from cgo `gethostuuid`. The `x/sys` raw syscall is deprecated. A darwin build with
  cgo off has no machine ID and falls back to the host name.
- Every macOS volume counts as normalization-insensitive. Two keys fold when either key's volume does. A failed
  sensitivity read counts as insensitive. Each choice only adds overlaps.
- An empty footprint entry list counts as `all`. A body whose domain is not an object is judged by its envelope. An
  absent `clear_when_dead` reads as true.
- Any git failure during boundary detection counts as "not a boundary", which adds overlaps. A submodule is found from
  `.gitmodules` first, then from `git --literal-pathspecs ls-files -s -z`. The `GIT_*` variables that locate a repo
  are cleared before git runs.
- The walk runs only when an agent variable is set. PID 1's unreadable arguments are ignored. Codex and opencode
  match when the base name of `argv[0]` is `codex` or `opencode`. Live hosts have not confirmed this, and M4 checks it.
- The `format` marker is a decimal integer. An absent marker means format 1.
- The lock note is one JSON line. The lock poll is 25 ms within the 5 s budget.
- Exit statuses: 0 ok, 1 error, 2 usage, 3 refused, 4 lock busy, 5 already covered, 6 not yours, 7 broken, 8 no such
  lease, 9 unusable record.
- The commands are stubs until M2 and M3. Their stub error does not name `hook`.

Review findings and dispositions:

1. Fixed (CONFIRMED). A PID that is absent locally read as dead whatever the record held. It now reads dead only when
   the record's platform matches the reader's and, on Linux, its PID namespace matches too. Otherwise it reads unknown.
2. Fixed (PLAUSIBLE, confirmed by trace). Submodule lookup broke on paths with glob characters or non-ASCII names, and
   inherited `GIT_DIR`. It now uses literal pathspecs and NUL-separated output, and clears those variables.
3. Fixed (PLAUSIBLE). The walk's unreadable-ancestor error now also names the agent variables it could not match. It
   still fails closed. In this sandbox every ancestor's arguments were readable except launchd's. A root-owned
   `login` case is covered only by an injected fault.
4. Fixed (adr-impact PLAUSIBLE, ADR-03). Linux treated every volume as case-sensitive. It now reads the casefold
   flag on the deepest existing directory. ENOTTY, EOPNOTSUPP, and EINVAL mean sensitive. Any other error means
   insensitive. Only fixture tests on macOS cover this.
5. Fixed. The `hook` stub's error no longer names `hook`.
6. Fixed. M1 has one amendment marker, and the As built block lists no symbols. It now notes that two checks are
   tested below command level until M2 and M3.
7. Fixed. An ambiguous comment in the state store was reworded.

ADR candidates added to the Decision Log as Undecided:

- The state directory's location and layout (hard to reverse)
- The exit-status table (hard to reverse)

Declined: the `format` marker's on-disk spelling, a decimal integer. It fills a gap under ADR-04's marker rule and is
not a decision of its own.

Deviations: the Linux amendment the user chose. Nothing else is open.

### NIT-001 M2

Commits so far: `90d5349` nitipan: add lease record format, key overlap, and liveness. That is M1's commit.

The first M2 implementer was restarted on 2026-10-21 at about 04:58 UTC. It had stalled for about 8 minutes on two
`mdfind` calls, which waited on a permission prompt because `mdfind` is not on the repo's cg allow list. It had
changed no code. The stopped agent's result read "Use find via Bash". The fresh agent's brief says to use `find`
through Bash for file searches.

Checks: `make fmt-check lint vet-linux test build` passed. Lint reported 0 issues. `go test -race` passed, including a
race of 8 processes over 6 rounds. `proposals.py check` and `proposals.py freeze --row-commit 90d5349` returned
`[]`. The implementer also ran `acquire`, `list`, and `release` once inside this sandbox, against the real state
directory, and released the lease.

Best guesses the implementer made:

- Domain arguments are `--checkout` plus a repeatable `--scope`. The checkout defaults to the working directory for
  `acquire` and `list`. `release` takes IDs as positional arguments, and the flags as a domain. The proposal does not
  spell these flags, and scripts will depend on them.
- An explicit `--replace` may drop keys. Only the self-overlap refusal stops suggesting it when it would drop keys.
  After review, a replace that drops keys prints the keys it released. The reviewer found the proposal's wording
  allows the flag to drop keys, but that is not settled. If `--replace` must refuse to drop keys, it needs a change.
- `list` takes the lock so it can finish crashed merges. When the lock is busy or blocked by the sandbox, `list` reads
  without the lock, warns, and exits 0.
- The Claude Code permission modes are `default`, `acceptEdits`, `plan`, `auto`, `dontAsk`, and
  `bypassPermissions`. Nobody verified the list. A stale list refuses a valid new mode.
- The Claude Code `setup` text adds a `Bash(nitipan *)` allow rule, which `setup_permissions.py` does not manage. The
  proposal says `setup` prints a command-approval rule where the agent has one. That rule also pre-approves
  `break --force`.
- The BIP-39 English list was written from memory, because no local copy was found. The reviewer generated the
  official test-vector mnemonics at 128, 192, and 256 bits, and all 12 matched. That covers about 25 words plus the
  ordering, but it is not a full comparison. A correction later changes every handle.
- A handle collision, a missing session ID, and an unsupported agent exit with status 1. A claim that overlaps the
  caller's own leases exits with status 3, and the table's description of status 3 was widened to say so.
- Codex and opencode `setup` print a "cannot claim leases yet" notice and the mkdir command.

Deviations:

- Two M2 checks moved to M3, and M3 gained the matching checks. One is "`list --json` includes the tombstones it
  delivered or reaped". The other is "an anonymous `break` caller is recorded". Both depend on `break` and
  tombstones, which M3 builds. The proposal carries one amendment marker per milestone and a Decision Log entry. This
  moved planned checks between milestones without asking, as the run's ambiguity rule allows. Review it.
- `list --json` schema 1.0 gained `tombstones` and `name_shared` without a version bump, since no schema had shipped.

Review findings and dispositions:

1. Fixed. M3's amendment marker sat in the middle of its check list.
2. Fixed. The M2/M3 amendment's log entry now gives the real reason, which is the plan inconsistency above.
3. Fixed. `list` now resolves its caller through the path that M3's tombstone delivery will use. A command-level test
   shows that a caller with no agent variables, or with stray ones, resolves as an anonymous process.
4. Fixed. Command-level tests now cover `list` reading without the lock, for a busy lock and for a sandbox-blocked
   lock.
5. Fixed (PLAUSIBLE). A `--replace` that drops keys now names the keys it released.
6. Fixed (CONFIRMED). The suggested `acquire` command in the uncovered-claim error now shell-quotes its paths.
   `release --all` with no leases now says so.
7. Fixed. The duplicate `plural` helper was merged.
8. Fixed. A duplicate ADR candidate for the handle derivation was removed. The 2026-10-18 entry already covers it.
9. Dismissed. adr-impact asked whether `absorbs` passes the minor-version rule. `absorbs` is a field the first
   version already defines, so the rule does not apply.

ADR candidates: none new. The handle candidate is already in the Decision Log, dated 2026-10-18. The `list --json`
schema's evolution falls under the 2026-10-18 candidate for `list --json`.

### NIT-001 M3

Commits so far: the M2 commit is named here once M3's first amendment lands.

## Status

M1 and M2 are done. M3 is next.
