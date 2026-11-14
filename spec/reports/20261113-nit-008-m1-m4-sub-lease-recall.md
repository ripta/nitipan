# Sub-lease recall: deadline, hook, rings, and the node's mirror

**Date:** 2026-11-13

This report covers a `/work-queue` run of NIT-008 M1-M4 (sub-lease recall) in the local macOS checkout at
`/Users/ripta/projects/nitipan`. NIT-008 is `spec/proposals/NIT-008-sub-lease-recall.md`. It lets a parent holder
recall a sub-lease with a deadline. Every reader enforces the deadline from a `recalls/` side record.

## Run state

- Run: default `next 1`, the row NIT-008 M1-M4. Code and specs share this repository, on `main`.
- Run base and row base commit subject: `spec: mark broker auth implemented and move its queue row to history`.
- Dependencies: NIT-005 M1, NIT-007 M2, and NIT-004 M3, all DONE. No `live_after` entries. No skipped, held, or
  blocked rows.
- Machine-local state tracks commit identifiers outside committed documentation.

| Milestone | Run status | Commits |
|-----------|------------|---------|
| NIT-008 M1 | DONE | `nitipan: recall a sub-lease with a deadline` |
| NIT-008 M2 | QUEUED | |
| NIT-008 M3 | QUEUED | |
| NIT-008 M4 | QUEUED | |

## Approved plan and authorization

The user approved the run plan on 2026-11-13. It authorizes implementation, milestone, row, and final reviews,
proposal completion, and commits by explicit path on the checked-out branch. It authorizes best guesses the proposal
supports, and stashing a blocked milestone's work by path. It authorizes no push, pull request, branch change,
destructive git operation, or action on another session's edits.

Local checks for every milestone: `make fmt`, `make ci` (fmt-check, lint, vet-linux, race tests, build), then
`proposals.py check` and `proposals.py freeze`. M3 runs the live Codex and opencode wake-path checks with the sandbox
disabled. No milestone check is left to the user.

## Best guesses from the plan

- Recall's records take minor 1.4, and the current record minor moves to 4. "How the deadline is enforced" says the
  next minor when M1 lands, and the current minor is 1.3. The child record keeps 1.2.
- The recall repair runs after the parent-gone repair. The proposal says the parent-gone repair runs before any expiry.
- Tombstone reuse after a crash accepts cause `revoked` or `recalled`, in both `revoke` and expiry.
- Under `--wait`, an equal-key refresh whose exempted delegation is under a recall in force is waitable. Without that,
  exit 11 returns as success inside the wait loop and never retries.
- The help text keeps `revoke`'s wording, "Subagents share their parent's holder". The proposal says "as `revoke`'s
  does".
- M1 builds one shared read-only in-force evaluator and checks it for the locked call and the unlocked `list`. M2 wires
  the hook to it. M1's bullet naming the hook is amended to point at M2.
- M3 adds key prefixes to the ring ledger with no version bump. Its keys are opaque strings.
- A request's write time for its ring key is `RequestedAt`.
- M4 drops the expired child from the mirror rather than adding owner data to the wire. The broker computes no owner.
  The node schedules a resend at the earliest pending deadline. That only re-reads and re-sends, and writes no lease
  state.
- Text in NIT-004, NIT-005, and NIT-007 that this work changes is amended as a correction of fact with its own Decision
  Log entry.

## Milestones

### NIT-008 M1: Recall with a deadline

DONE. Commit: `nitipan: recall a sub-lease with a deadline`, which carries the proposal and this report.

Checks: `make fmt` clean, `make ci` passes (lint 0 issues; `internal/cli` takes about 438s under `-race`).
`proposals.py check` and `proposals.py freeze` both return `[]`. No unowned failures.

Test cost: the new `internal/cli/recall_test.go` tests plus the package's existing `TestACrash*` tests run in 16.2s
alone (`-count=1 -p 1 -parallel 1`, with `-race`). The new `internal/lease` and `internal/record` tests run in about 1s
each. No real sleeps. Waits use a settable clock.

Implementer best guesses, each recorded in the proposal's As built block or Decision Log:

- The deadline is inclusive. A recall is past its deadline at or after it, so the last attempt of an `acquire --wait`
  that runs exactly at its timeout sees the end. If wrong, flip one comparison in `JudgeRecall`.
- A dead sub-lessee past its deadline is left for normal dead-holder clearing, with no tombstone. Nobody can receive
  one. Amended in "Expiry".
- The recall record's envelope holder is the parent's holder. No decision reads it.
- `recall` and `--cancel` refuse any recall record they cannot use, not only one of a higher minor, with exit 9. A
  cancel with no recall exits 8.
- A recall `state` this binary does not know makes the record unusable. It ends nothing and warns.
- An unlocked `list` drops a sub-lease past its deadline from `leases` and from the parent's `delegated`, and warns on
  stderr. The JSON carries no marker for it.
- `recall` prints a wait of the time to the deadline, rounded up to a second, plus 10 seconds.
- A refresh result carries the recalled children's deadlines. `AcquireWait` treats a non-empty set as waitable. A wait
  that ends before the deadline exits 10. Without `--wait`, the delegation line ends with `recalled until <deadline>`.
- A locked call prints a stderr line per expiry it ran. Unusable recall or parent records warn on every locked call.
- The cancelled notice is written only while the sub-lease still stands and names the parent.
- Nine tests that used `"1.4"` as a newer-minor fixture now use `"1.5"`. Six schema assertions moved to `"1.6"`.
- `revocable` takes the act's name so a recall's refusal reads right. `newTombstone` is split so the expiry can name
  the parent's holder as breaker.

Deviations:

- The hook part of M1's unusable-parent check moved to M2, as planned. M1, M2, and the Decision Log are amended.
- No test covers `revoke` reusing a `recalled` tombstone. A recalled child is always ended by the repairs before a
  revoke body runs, so the CLI cannot reach it. The shared lookup that accepts both causes is exercised by the expiry
  crash test.
- The implementer overwrote `internal/lease/engine.go` by mistake and rebuilt it from `HEAD` plus the new hooks. The
  reviewer confirmed its diff holds only the added hook fields.

Review findings:

- `recall` on a dead sub-lessee skipped the rewritable check and removed a higher-minor recall record (PLAUSIBLE).
  Traced and confirmed: NIT-001 lets an older binary remove a higher-minor record only on its own holder's release. A
  regression test failed on the unfixed code, with exit 0 for 9 and the record gone. Fixed: `recall` checks the record
  first, and the reap, expiry, and cancel leave a higher-minor record in place. Recorded in As built.
- An unlocked `list` shows a pending recall on a dead sub-lessee past its deadline (PLAUSIBLE). Not confirmed. The
  recall still binds until dead-holder clearing removes the sub-lease, and the wait never counts dead children. No
  change.
- No test pins `revoke`'s reuse of a `recalled` tombstone (note). Dismissed: unreachable from the CLI, and the shared
  lookup is tested from the expiry side.
- `adr-impact`: ADR-04 and ADR-06 consistent. No conflict, no supersession.

ADR candidates:

- "A deadline on a local lease is enforced by every reader when it reads." The Decision Log already holds it, declined
  by the user on 2026-10-24.
- "Restrictive new state goes in a side directory older binaries never scan." Fails the Architecture test. It is a
  storage mechanism serving ADR-04's minor rule, not an invariant of its own.

Open: the README still says "ten commands", which was already out of date before this run.
