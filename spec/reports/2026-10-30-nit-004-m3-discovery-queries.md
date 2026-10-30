# NIT-004 M3: discovery queries work-queue run

Date: 2026-10-30. Trigger: a `/work-queue` run with no arguments, which means `next 1`. It picked queue row
`NIT-004 M3` from `spec/proposals/queue.md`. NIT-004 is the lease broker proposal
(`spec/proposals/NIT-004-lease-broker.md`). M3 adds "who holds X" and "who is named N" across machines. It also reports
overlaps between `user` or `shared` leases and mirrored `machine` leases. Environment: macOS, run from Claude Code's
sandboxed Bash in `/Users/ripta/projects/nitipan`. The approved plan is
`/Users/ripta/.claude/plans/encapsulated-sauteeing-snail.md`.

The system `python3` is 3.9, which cannot run the plugin's `proposals.py`. The run uses `/opt/homebrew/bin/python3`.

## Run state

- Code repo base commit for the run and for row `NIT-004 M3`: `28ec274`
- Specs live in the code repo, so the specs commits are the code commits listed below
- The row depends on NIT-004 M2, which is `DONE`. No rows are held or blocked. `next` skipped no rows

| Milestone | Run status | Commit |
|-----------|------------|--------|
| NIT-004 M3 | IN PROGRESS | |

## Best guesses from the plan

- Mail addresses in discovery are left to NIT-007 M1. Its milestone text puts them there
  (`NIT-007-session-mailbox.md:469-470`). M3's checks do not name them
- NIT-006's cross-user hiding is left to NIT-006 M2. The queue note says "M2 filters NIT-004 M3's discovery". Before
  NIT-006 one broker is one user scope
- The CLI surface is `nitipan who holds <X>` and `nitipan who is <N>`, echoing the proposal's wording. The proposal
  names no command
- The CLI writes the derived-entries sidecar for a `machine` lease whenever a node is configured, even while the node
  is down. The node derives only for records with no sidecar, which covers V1 records. This rests on "Machine leases
  and user leases on the same path" and the macOS-privacy risk's mitigation
- A resource-name query matches holders on every machine. The M3 check says "across machines". The per-machine scoping
  in "Source of truth for machine leases" covers mirrored paths only
- "New-binary holders" means records written by a binary that delivers notices. A V1 holder gets no notice
- Overlap notices are keyed per lease pair and fire once per pair

## Left for the user

- The risk "A partitioned holder keeps working after its lease expires" carries `**Revisit:** when NIT-005 is
  accepted`. NIT-005 is accepted, so the hook's audit is due. Only the user picks its outcome, so the run left it alone

## NIT-004 M3

### Implementation

One implementer built the milestone. It set M3 to `DONE` with an As built block.

- A new sidecar under `broker/derived/<lease-id>.json` carries a `machine` lease's derived `repo:` entries and the keys
  they came from. The CLI writes it after a `machine` acquire whenever `~/.config/nitipan/node.json` exists. The node
  derives for any lease with no usable sidecar, which covers V1 records, and prunes sidecars of ended leases.
  `wire.MirroredLease` gains `Derived`. Its absence reads as unknown
- The broker answers `who-holds` and `who-is` on new paths. The node forwards them as new handoff kinds, and
  `RequestVersion` is 3. The CLI adds `nitipan who holds <X>` and `nitipan who is <N>`, each with `--json` at its own
  schema 1.0. `list --json` stays at 1.5
- Answer states are live and lapsed for grants, mirrored and stale for mirrors, and unknown for a mirror the broker
  cannot judge. Unknown mirrors are listed, never dropped
- A `user` or `shared` claim's reply carries overlaps with mirrored `machine` leases, and `acquire` prints warnings.
  The snapshot answer carries overlaps to each node, which writes one notice per lease pair per node process

Checks: `make ci` passed (fmt-check, lint with 0 issues, vet-linux, `go test -race ./...` with `internal/cli` at
372 s, build). `proposals.py check` reported only the row-finish findings. `proposals.py freeze` returned `[]`. The
implementer ran checks through `cg_run`, since the Bash sandbox blocks the local port binding `httptest` needs.

Best guesses the implementer added, each in a `(run best guess)` Decision Log entry:

- A `machine` lease's holder gets an overlap notice only when the CLI wrote its sidecar. A V1 record, or one the node
  derived, gets none. The grant's holder always gets one. A CLI-written sidecar wins over a node-written one. If wrong,
  V1 holders' notices sit unread, or every mirror gets one
- Overlap notices ride the snapshot answer rather than a version-gated stream event. The pair is marked once per node
  process, so a node restart can repeat an unread notice. The claim reply marks the grant side seen, so the claimant
  gets a warning and no notice. If exactly-once must survive restarts, the mark moves to a file
- The broker leaves out the grant's own anchor, the holder's other leases on the same node, and sub-leases of the
  anchor from overlap reports. Overlaps with a stale mirror are reported and marked stale. Unknown mirrors appear only
  in discovery rows, never as overlap reports
- `RequestVersion` 3 is global, so a new CLI gets "node too old" from a version-2 node for every request, `list`
  included. This follows the M2 precedent
- `who holds` accepts a qualified name, and `who is` does not. Path queries apply no checkout-boundary exemption, so a
  sibling worktree's lease is listed
- A derivation failure the node recorded is retried after an hour. One the CLI recorded is not retried

Amendments, each with an "Amended 2026-10-30" marker and a Decision Log entry: "Discovery queries", the derived-entries
and notice bullets of "Machine leases and user leases on the same path", and M3's first check.

### Review

ADR impact: no conflicts and no supersessions. ADR-01, ADR-03, ADR-04, ADR-05, ADR-07, and ADR-08 are consistent.
ADR-06 is not touched. No uncovered decision passes the ADR bar.

Reviewer findings, all PLAUSIBLE and sent to the implementer to trace:

1. `internal/node/derive.go`: a snapshot ran git derivation synchronously for every underived lease. It runs on the
   goroutine that heartbeats and renews. Fixed: the trace confirmed it. A snapshot now stops starting derivations after
   a 5-second budget, and later snapshots finish the rest. Leftover leases read as unknown in the meantime
2. `internal/broker/discovery.go` `handleWhoIs`: unreadable mirrors were skipped, so a name behind one went missing and
   a duplicate could be missed. Fixed: the trace confirmed it. They are now listed as unknown rows, and the answer
   carries a note that the count may be low
3. Each snapshot recomputes overlaps for every grant against every mirror under the broker lock. Dismissed: at the
   stated scale of a handful of machines it costs milliseconds at most. "Broker lifecycle and store" already accepts a
   whole-file rewrite per change at that scale. Worth revisiting only with hundreds of nodes
4. Minor: the node's in-process once-map for overlap notices is never trimmed. Left: trimming against an older answer
   could drop a fresh key and send a duplicate notice. Growth is one short string per pair, reset on restart

The regression tests for findings 1 and 2 were not run against the unfixed code first. They pin the fixes. The
implementer's reasoning says each would fail without its fix, but that is unverified.

`make ci` passed again after the fixes, with `internal/cli` at 398 s.
