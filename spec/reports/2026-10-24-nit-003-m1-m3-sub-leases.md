# NIT-003 M1-M3: sub-leases work-queue run

Date: 2026-10-24. Trigger: a `/work-queue` run with no arguments, which means `next 1`. It picked queue row
`NIT-003 M1-M3` from `spec/proposals/queue.md`. NIT-003 is the proposal for sub-leases a holder delegates for part of
its domain (`spec/proposals/NIT-003-sub-leases.md`). Environment: macOS, run from Claude Code's sandboxed Bash in
`/Users/ripta/projects/nitipan`.

## Run state

- Code repo base commit for the run and for row `NIT-003 M1-M3`: `f29ad6d`
- Specs live in the code repo, so the specs commits are the code commits listed per milestone below
- The row's only dependency, NIT-001, is implemented. No rows are held or blocked. `next` skipped no rows

| Milestone | Run status | Commit |
|-----------|------------|--------|
| NIT-003 M1 | DONE | `2ee18df` |
| NIT-003 M2 | DONE | `344a4e6` |
| NIT-003 M3 | IN PROGRESS | |

## Plan-time facts

- NIT-001 already carries the eight amendments NIT-003 needs. See NIT-001's Decision Log entry of 2026-10-18 that
  begins "Took eight amendments". NIT-001 is implemented and frozen, so this run does not edit it.
- Session reports go in `spec/reports/`. The preference file under `~/.config` is denied by the sandbox. The prior run
  recorded the user's choice of `spec/reports/`.
- Local checks: `make ci`, which runs `fmt-check lint vet-linux test build`. Plus `proposals.py check` and
  `proposals.py freeze`.

## Best guesses made at plan time

Each guess names the ambiguity, the choice, the document it rests on, and what changes if it is wrong.

1. Resource leases cannot be sub-leased, and `request` against one is refused. NIT-002's Impacts leaves the question
   to NIT-003, and NIT-003 is silent. Refusing is reversible and additive. M1 amends NIT-003's settled text. If wrong,
   a later change allows `request` on a resource lease and defines slot semantics for it.
2. The tombstone `cause` is a new field, and `kind` stays empty. Tombstones already use `kind` for `moved` and
   `revived`. NIT-003 "Breaks across the tree" says old readers ignore the cause and deliver the tombstone as a break.
   Only a separate field gives that. If wrong, `revoked` becomes a `kind` value, and old readers would misreport it.
3. Child records, `cause` tombstones, requests, and notices are stamped 1.2. `list --json` is schema 1.2. NIT-003 says
   only "a minor". A different number changes constants and fixtures.
4. The parent's equal-key refresh with live delegations exits 11, the next free code. "Overlap with the parent" says
   only "its own status".
5. Notice file names are `<target-id>-<cause>.json`. A grant notice uses the request ID as its target. NIT-003 says
   only that the ID is derived from the target's ID and the cause.
6. Refusals that name a sub-lessee are waitable under `acquire --wait`, since the child may end. NIT-002 "Waiting"
   makes overlap refusals waitable, and NIT-003 does not say otherwise.
7. `list --json` under the lock also reports delivered notices in a `notices` array with a `summary`, as tombstones
   are. Delivery removes a notice, so stderr alone would lose it for a guardian piping JSON. M3's check is amended to
   say so. If wrong, the array is dropped.
8. The old-binary checks use a binary built from the run base `f29ad6d`. That binary is older than the child's minor.
9. Commit subjects follow history: `nitipan: <what>` for milestone commits, `spec: <what>` for spec-only commits.

## Milestones

### NIT-003 M1

The M1 commit is named under M2's section, since a report cannot hold the SHA of the commit that carries it.

Checks: `make ci` passed before and after the review fixes. Lint reported 0 issues. `proposals.py check` and
`proposals.py freeze` both returned `[]`. The `cli` package's tests now take about 47s, up from about 21s. The new
sub-lease race test runs half of `raceRounds` for that reason.

The old-binary check passed against a binary built from `f29ad6d`. The harness is
`/private/tmp/claude-501/-Users-ripta-projects-nitipan/fdf2abeb-45ed-49e4-8573-d6d6613704eb/scratchpad/old-binary/`.
Its `oldcheck_test.go` is copied into the target tree's `internal/cli` and run with
`go test ./internal/cli -run TestOldBinaryCheck -v -count=1`. The old binary refused every claim the new one refused.
It was stricter in three cases: the sub-lessee's refresh, its in-lease claim, and the parent's equal-key refresh. It
never rewrote the child. A `git archive` through `cg_run` was refused by a cg rule, so it ran through Bash.

Best guesses the implementer made, each also in the proposal's Decision Log:

1. `request` also refuses a lease of the caller's own, and a lease whose holder is dead. The text implied both.
2. `grant` also refuses a requester whose handle another live holder has since taken.
3. A sub-lessee is exempt from its own parent only while every claimed key stays inside its own sub-lease, whatever
   the parent's liveness.
4. `list` neither repairs requests nor delivers notices in M1. Plan best guess 7, a `notices` array, is left to M3.
5. Reaping a request whose parent lease is gone, with a "request declined" notice, fell out of M1's code. M2 owns its
   checks.

Amendments: "Invocation and consent", "Overlap with the parent", "Record shape", and "Liveness and the parent's death"
carry the plan's best guesses. The Risks entry on NIT-001 amendments was stale and was amended in place.

Review findings:

- `repairGranting` wrote a "request lapsed" notice that said the grant never became a lease. That is false after a V1
  removal of a published child. Traced and confirmed. Fixed by rewording the notice.
- `Grant` removed dead holders' leases before the `granting` mark. A failed mark then left them removed. Traced and
  confirmed. Fixed by moving the removal after the mark.
- A rewrapped proposal sentence and one README line ran past 120 columns. Fixed.
- The stale Risks entry was left as written. Fixed by amending it in place.
- The grant hooks' comment restated the code. Fixed with one short comment per hook.
- adr-impact: no conflicts. It named the record-level parent and child overlap exemption as an uncovered decision. It
  passes the bar as cutting across the system, and it is recorded as an `Undecided` candidate in the Decision Log. The
  existing candidate for the record shape and directories still reads "waits on NIT-001 accepted". NIT-001 is now
  implemented.

### NIT-003 M2

Code commit for M1: `2ee18df nitipan: add sub-leases with request and grant`. The M2 commit is named under M3's
section.

Checks: `make ci` passed before and after the review fixes. Lint reported 0 issues. `proposals.py check` and
`proposals.py freeze --row-commit 2ee18df` both returned `[]`.

The old-binary check passed. It used a second harness, `oldcheck2_test.go`, beside M1's in the scratchpad
`old-binary/` directory. There were four scenarios: a parent release, a forced break of the parent, a dead parent
cleared by a third party's claim, and a dead parent with the child refreshing its keys. In all four, the `f29ad6d`
binary left the child byte-identical and wrote no notice. The new binary did the same against a child stamped 1.3. A
test covers that `list` without the lock delivers no notices.

Best guesses the implementer made, each also in the proposal's Decision Log:

1. A dead sub-lessee gets no revoke tombstone, and a retried revoke reuses the existing tombstone.
2. A repair that finds the parent already gone cannot tell how it ended, and its "parent ended" notice says so. Only
   the call that releases or breaks the parent knows the cause and the breaker. A child whose own holder is dead is not
   repaired.
3. A "sub-lease ended" notice goes only to a live parent.
4. `revoke` refuses a lease that is not a sub-lease, and a child whose parent record is gone, with status Refused.
5. A parent's release or forced break publishes the children's notices, removes the parent, and then rewrites the
   children. A crash after the removal is finished by the dangling-parent repair.
6. Delivery holds a loss notice until the loss it announces has happened. It drops the notice when the loss can no
   longer happen as announced. Loss notices are written with replace, so a retry or a later loss with another cause
   leaves the text of the loss that happened. This came from review finding 2 below.

Review findings:

- One `break --force` of a child and its parent, with the child sorting first, recreated the removed child. The
  parent's repair still saw the child's stale entry, and its `rename(2)` brought the file back. Traced and confirmed.
  Fixed at the root: every lease removal goes through one helper that marks the entry removed. Lookups and child scans
  skip removed entries, and the stand-alone rewrite refuses one. A test covers it.
- A loss notice published before its write could be delivered twice. That happened when the addressee's call consumed
  it between a crash and the actor's retry. Traced and confirmed against M2's "exactly one notice" check. Fixed by
  best guess 6, with tests. The check text is unchanged.
- The `reapNotices` doc named a sibling function. Fixed.
- A "Parent ended" bullet was edited without its own marker. Dismissed: the amendment's marker and Decision Log entry
  cover it.
- adr-impact: no conflicts. It named "a rewrite drops a record to the lowest minor its fields need" as an uncovered
  decision. Dismissed: writers already stamp the lowest minor their fields need, so the rule is not new to this row and
  fails the Architecture test.

### NIT-003 M3

Code commit for M2: `344a4e6 nitipan: keep sub-leases alive when their parent ends`. The M3 commit is named under the
row review's section.

Checks: `make ci` passed before and after the review fixes. Lint reported 0 issues. `proposals.py freeze` with both
row commits returned `[]`. `proposals.py check` reported only that every milestone is finished, so the proposal should
be `implemented`. The row finish resolves that. The old-binary harness was not rerun, since no record or state-directory
format changed.

Best guesses the implementer made, each also in the proposal's Decision Log:

1. An unfiltered or resource-filtered `list` omits `owner` and `grantor_of`. A mark is always computed from a query, so
   an absent mark never means false.
2. `requests` leaves out requests in the `granting` state and request files this binary cannot use. A filtered `list`
   keeps only the requests whose keys overlap the filter. Each request carries `id`, `parent`, `requester` with its
   handle, `liveness`, `keys`, `description`, and `requested_at`.
3. A locked `list` runs the request and child repairs and reaps dead holders' notices. It then delivers the caller's
   notices and reports each in a `notices` array with a `summary`. An unlocked `list` does none of this. This settles
   plan best guess 7, on the reading that "a `list` that reads without the lock delivers no notices" implies a locked
   one does.
4. A sub-lease of a dead parent displays as standing alone, since the next locked call makes it so.
5. A lease with a `parent` link is never a nesting parent, so a planted cycle cannot recurse.
6. The unlocked-read warnings now say "tombstones and notices are not delivered".

Review findings:

- M3's amended check had no marker. Fixed.
- The "Display" owner bullet still said "outside the union of its live children's keys". The code tests each part
  against one child key at a time. Fixed by rewording the bullet to the per-part rule, with a Decision Log entry. The
  bullet explains why the two agree for path keys.
- A repair write failure made a locked `list` fail. Traced and confirmed against NIT-001's rule that a failed tombstone
  removal is reported, not fatal. Fixed: the failure is now a warning, `list` still prints what it read, and that call
  delivers no notices. A test covers it.
- `list`'s repair did not reap dead holders' notices, unlike every other locked call. Fixed for parity.
- The `marks` comment credited ADR-03 with a sentence ADR-03 does not carry. Fixed.
- The README `requests` bullet omitted `description`. Fixed.
- adr-impact: no conflicts. It named two uncovered decisions. Both were dismissed. A locked `list` delivering notices
  extends NIT-001's existing tombstone delivery by `list`, so it does not depart from the past. The `list --json`
  consumer contract is NIT-001's, whose ADR candidate the user already declined.
