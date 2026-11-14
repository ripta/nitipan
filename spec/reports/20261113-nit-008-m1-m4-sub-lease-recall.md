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
| NIT-008 M2 | DONE | `nitipan: refuse a recalled sub-lessee's commits in the git hook` |
| NIT-008 M3 | DONE | `nitipan: ring wake paths for pending lease notices and requests` |
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

### NIT-008 M2: Recall in the git hook

DONE. Commit: `nitipan: refuse a recalled sub-lessee's commits in the git hook`, which carries the proposal and this
report.

Checks: `make fmt` clean, `make ci` passes (lint 0 issues; `internal/cli` about 435s under `-race`). The first `make ci`
of the fix round failed on an errcheck in a new test. The fix is in, and the rerun passed. `proposals.py check` and
`proposals.py freeze --row-commit` for M1's commit both return `[]`. No unowned failures.

Test cost: the first round's four hook recall tests ran in 1.9s alone under `-race`. The fix round's run used a filter
that also matched the older hook tests. That run took 143s and is not a figure for the new tests alone.

Implementer best guesses:

- A `Deps.Touched` seam in the CLI, following `Deps.Git`, so hook tests feed touched paths without real git. The plan
  named no seam. The alternative is real-git end-to-end tests with on-disk deadline edits.
- With an unusable parent record, the hook warns that the recall is not enforced. NIT-005's unusable kind still refuses
  the sub-lessee, because that record's footprint covers every path. The M2 check says only "ends no child and reports
  it".
- The pre-deadline line prints only when the whole update passes, once per recall, and never for the parent's holder.
- A caller holding both the parent and the sub-lease is never refused by the recall kind and gets no notice.
- A dead sub-lessee is left as is, matching the unlocked `list`.

Deviations: none from the proposal or the plan. NIT-005 and NIT-007 already describe the recall refusal and the hook's
deadline line, so neither needed a correction. The reviewer confirmed both.

Review findings:

- Several As built claims had no test (plan gap). Fixed with one table-driven test and one count test. They cover a
  caller holding both leases, a refused update printing no line, one line per recall across two paths, a dead
  sub-lessee, and a recall against a mark and against a tombstone. The unusable-parent test now asserts `Refused`. No
  claim turned out false. The dead-sub-lessee claim is barely observable, so As built was reworded to say the record
  never refuses either way.
- The unlocked read order let an expiry slip between reads (PLAUSIBLE). Confirmed by reading. Entries and tombstones
  read before the tombstone was published, plus recalls read after the drop, showed a live sub-lease with no recall.
  The ex-sub-lessee would pass once. Fixed: the hook and the unlocked `list` now read recalls first, then entries,
  tombstones, and marks. A test cannot interleave a read with an expiry. The test pins the fix, not the bug, and As
  built says so. Residue: a recall read just before a cancel that lands exactly at the deadline could show a child the
  cancel saved as ended, to one unlocked reader. Unlocked readers write nothing, so the next read corrects it.
- Nested recalls would mislabel a notice (PLAUSIBLE). Not confirmed: sub-leases do not nest, since a record with a
  `parent` has no delegations. Noted in As built.
- `recalledCause` ignores a grandchild's ownership (PLAUSIBLE). Not confirmed, for the same reason. No change.
- Doc comments on two hook helpers packed several ideas, and one README sentence stacked clauses. Both fixed.
- `adr-impact`: ADR-03, ADR-04, ADR-06, and ADR-07 consistent. No conflict.

ADR candidates: none new. The lock-free recall rule is the candidate the user declined on 2026-10-24.

### NIT-008 M3: Rings for pending notices and requests

DONE. Commit: `nitipan: ring wake paths for pending lease notices and requests`, which carries both proposals and this
report.

Checks: `make fmt` clean, `make ci` passes (lint 0 issues; `internal/cli` about 444s under `-race`). `proposals.py
check` and `proposals.py freeze` with both earlier row commits return `[]`. No unowned failures.

Live checks, run with the sandbox disabled through the permission prompt:

- `make live-codex LIVE_CODEX_ARGS=--selftest`: ok.
- `env -u CODEX_THREAD_ID -u CODEX_SESSION_ID make live-codex 'LIVE_CODEX_ARGS=--only mail,mail-embedded,notice-ring
  --results <scratchpad>/codex-notice-ring.md'`: all PASS, with no real model calls.
- `make live-opencode 'LIVE_OPENCODE_ARGS=--only mail,mail-reminder,notice-ring --results
  <scratchpad>/opencode-notice-ring.md'`: all PASS, with no real model turns.

The new `notice-ring` scenario plants a notice for the session. It checks that the wake path rings and names `nitipan
list`, and that `nitipan list` then delivers and removes the notice. The review round changed no live-check script, so
the live runs were not repeated.

Test cost: the first round's new CLI tests ran in 3.8s alone under `-race`. The node notice test ran in 1.06s. The
review round replaced the waiter test's real 300ms and 5s waits with a direct call and a single waiter run.

Implementer best guesses:

- The node maps a registration to a holder through its current session ID, not its raw envelope. Notices are addressed
  to the session the holder resolves to now. If wrong, the node would ring for notices `nitipan list` cannot deliver.
- A `recalled` notice for a sub-lease past its deadline does not ring. The unlocked read counts the sub-lease as ended,
  and the sub-lessee learns from the tombstone, which NIT-008 gives no ring.
- A request whose requester is dead does not ring, since a locked call removes it. The proposal is silent.
- A node built with no liveness checker rings no notices or requests. The real node always has a checker.
- The live harnesses plant notices only. A pending request needs a lease. The CLI and node tests cover requests.
- The Claude waiter has no live check. A CLI test covers it, and its socket ring shares the node path.
- `leaseRings` skips a multi-session holder with no session ID and an invalid ID. A single-session adapter with an
  empty ID still rings, because `pendingFor`'s blanket skip would drop its notice rings.

Deviations: none from NIT-008. The implementer first edited four NIT-007 passages as a correction of fact. Review
reverted that edit. NIT-007 now carries only a References line linking NIT-008.

Review findings:

- The NIT-007 edit was not a correction of fact (plan best guess, reviewer finding). The old text described what NIT-007
  shipped correctly. NIT-008 is the proposal that extends it, and its Impacts already records the change. The four
  hunks and their Decision Log entry were reverted, and NIT-007 gained one References line to NIT-008. The plan's best
  guess about amending frozen proposals was too broad here. Extensions stay in the extending proposal.
- The waiter test used real waits where a direct call proves the contract (CONFIRMED). Fixed: `ringUnrung` is called
  directly for the once-per-key and replaced-notice contract, and one waiter run checks its exit status.
- The node's idle early return was gone, and leases loaded once per registration (PLAUSIBLE). Confirmed. Fixed: the node
  returns early unless the inbox, `notices/`, or `requests/` holds a file. It reads leases once per tick and judges each
  registration against that view.
- The turn hook built the engine before its cheap empty check (PLAUSIBLE). Fixed: the check runs first, and holders that
  cannot hold a lease return early.
- The refused-claim control matched on the test name, and a PID offset was unexplained (CONFIRMED). Fixed with a table
  field and a named helper.
- Test gaps. Added a row for a past-deadline `recalled` notice and a node test for the request ring. The socket ring and
  a no-checker node stay untested, as recorded in As built.
- The live fake model could run `nitipan list` in another scenario (PLAUSIBLE). Not confirmed: notices are addressed to
  each scenario's own fresh session, and the sweep removes planted files. No change.
- Two doc comments repeated or packed ideas. Fixed.
- `adr-impact`: ADR-04, ADR-06, and ADR-07 consistent. No conflict.

ADR candidates, recorded as `Undecided` in NIT-008's Decision Log:

- No wake path lets a party wake a session's agent through an act that costs it nothing and that the session never
  granted. Cuts across the system: every future ring path must honor it, and it is a security boundary.
- Every wake signal, for mail, lease notices, or requests, only signals, and only a read consumes. ADR-06 states this
  for mail alone. Cuts across the system: a later path could read ADR-06 as mail-only.

One candidate was dismissed. `adr-impact` named wake paths judging from an unlocked, best-effort read, and that is an
implementation tradeoff, not an invariant.
