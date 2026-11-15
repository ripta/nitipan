# NIT-002 M1-M3: resource leases and counted slots work-queue run

Date: 2026-10-23. Trigger: a `/work-queue` run with no arguments, which means `next 1`. It picked queue row
`NIT-002 M1-M3` from `spec/proposals/queue.md`. NIT-002 is the proposal for resource leases, counted slots, and
`acquire --wait` (`spec/proposals/NIT-002-resource-leases-and-slots.md`). Environment: macOS, run from Claude Code's
sandboxed Bash in `/Users/ripta/projects/nitipan`.

## Run state

- Code repo base commit for the run and for row `NIT-002 M1-M3`: `dff4a6d`
- Specs live in the code repo, so the specs commits are the code commits listed per milestone below
- The row's only dependency, NIT-001, is implemented. No rows are held or blocked

| Milestone | Run status | Commit |
|-----------|------------|--------|
| NIT-002 M1 | DONE | `e53b4e3` |
| NIT-002 M2 | DONE | `bd17947` |
| NIT-002 M3 | DONE | `44f8042` |

## Decisions the user made at plan time

- Session reports go in `spec/reports/`.

## Best guesses made at plan time

Each guess names the ambiguity, the choice, the document it rests on, and what changes if it is wrong.

1. Resource records are version `1.1`, and `list --json` is schema `1.1`. "Record and output versions" says a minor,
   not which. A different number changes one constant and the fixtures.
2. `slots.json` is `{"version": 1, "slots": {"<name>": N}}`. "Who sets N" fixes only a `version` and a name-to-N map.
   A different shape changes the loader and the README.
3. A resource name is at most 64 bytes. "Name syntax" says only "a length limit". Loosening it later breaks no stored
   name.
4. The wait-timeout status is a new exit code 10. "Waiting" requires its own status. A mismatched `--slots` exits as
   Refused (3), since "Waiting" and M2 call it a refusal. The wait loop tells waitable refusals apart by a sentinel
   error, not by status.
5. The wait backoff starts at 100ms and doubles to a 2s cap, with jitter. "Waiting" says only "polls with backoff".
6. The M1 check that a NIT-001-only binary handles resource records is run once with the binary built from `dff4a6d`
   through `git archive`, against a temp state directory. The result goes in M1's As built block.
7. An unparseable `slots.json` warns on every command that takes the lock or lists. "Who sets N" says "every call".
   `hook` and `setup` never read leases, so they do not warn.
8. Before M2 adds `slots.json`, every name has N = 1. `--slots 1` succeeds, and any other value is refused naming the
   configured value 1.
9. Commit subjects follow the history: `nitipan: <what>` for milestone commits, `spec: <what>` for spec-only commits.

## Milestones

### NIT-002 M1

The M1 commit is named under M2's section, since a report cannot hold the SHA of the commit that carries it.

Checks: `make fmt-check lint vet-linux test build` passed, before and after the review fixes. Lint reported 0 issues.
`proposals.py check` and `proposals.py freeze` both returned `[]`.

The V1-reader check passed against a binary built from `dff4a6d`. The binary has no flag for another state
directory, so the implementer drove it through its own test harness over a temporary directory. The details are in
M1's As built block.

Best guesses the implementer made:

1. Exit statuses. A bad name, a second `--resource`, `--resource` with `--checkout` or `--scope`, `--slots` on a path
   claim, `--slots` below 1, and an empty `--resource` exit Usage (2). `--replace` involving a resource and a
   mismatched `--slots` exit Refused (3). If `--replace` should be Usage, it is a one-line change in two places.
2. Another machine's record is skipped only when its kind is `resource`. A remote record of unknown kind with
   footprint `all` still blocks, per ADR-04.
3. The resource body writes `slots: 1` now, so M2 needs no record change.
4. The `remote` flag in `list --json` is set on resource leases only. Path leases from another machine keep the
   unknown-counts-as-alive rule.
5. A broken resource lease puts its name in the tombstone's `paths` field. A V1 reader ignores it.
6. `repo` is refused case-insensitively. `REPO:x` gets no lowercase suggestion. `release --resource` and
   `list --resource` run the same grammar check.

Bug found and fixed: `--resource ""` reads back from cobra as no value. It would have become a path claim on the
working directory. It is now refused as a usage error.

Fixtures: forward-compatibility fixtures that planted kind `resource` now use kind `future`. Newer-minor fixtures at
1.1 moved to 1.2, since 1.1 is no longer newer.

Review findings:

- Reviewer, proposal: "Name syntax" carried two amendment markers. Fixed: one 2026-10-23 marker remains.
- Reviewer, comment: `ReleaseRequest` grouping and an over-wide struct comment. Fixed.
- adr-impact, PLAUSIBLE conflict with ADR-04: skipping another machine's resource record. Dismissed. ADR-04's
  sentence governs liveness ("has unknown liveness, which counts as alive"). NIT-002's settled "Resource names"
  decision governs overlap at `machine` reach, and an older binary still blocks on such a record, which fails safe.
  Recorded as an ADR candidate instead.
- adr-impact, uncovered decisions: two ADR candidates were added to NIT-002's Decision Log as `Undecided`. One covers
  remote resource records never overlapping. The other covers writers stamping the lowest minor they use. The three
  older NIT-002 candidates still wait on the user's choice. Their hooks fired when NIT-001 was accepted.

### NIT-002 M2

Commit for M1: `e53b4e3 nitipan: add resource leases`. The M2 commit is named under M3's section.

Checks: `make ci` passed, before and after the review fixes. Lint reported 0 issues. `proposals.py check` and
`proposals.py freeze --row-commit e53b4e3` both returned `[]`.

Best guesses the implementer made:

1. `list --json` stays at schema 1.1. M1 introduced 1.1 in this same unreleased row, so the new computed fields
   `effective_slots` and `slot_holders` join it. Recorded in the Decision Log. If 1.2 is wanted, it is one constant
   and the test assertions.
2. `slot_holders` lists lease IDs. Every lease stays a top-level entry, so a consumer joins on the ID.
3. The config path is in text `list` only, never in `--json`.
4. A read error on `slots.json`, such as a permission failure, makes it unparseable and falls back to N = 1. A file
   that is not one JSON value reports that single error, since nothing past it can be read.
5. Counts must be JSON integers. `2.0`, `"2"`, and `1.5` are rejected.
6. A live same-name record whose body this binary cannot read blocks as exclusive. It is not counted as a slot holder.
7. The full-slot refusal reads "gpu:main has 2 of 2 slots taken." and names the holders through the existing holder
   lines. When the limit is below the configured N, it adds that a holder admitted under a lower count keeps it there.

Review findings:

- Reviewer, proposal: "Who sets N" said every call warns on an unparseable config. Fixed: amended in place to every
  command that takes the lock or lists, which is the plan's best guess 7, with a Decision Log entry.
- Reviewer, plan: `list` printed the config path only when a resource lease was listed, though the proposal says
  `list` shows it. Fixed in code: text `list` always prints it.
- Reviewer, tests: three As built claims had no test. Fixed: an unknown-liveness holder takes a slot, a dead
  unclearable holder blocks, and an unreadable live same-name record blocks as exclusive.
- Reviewer, PLAUSIBLE: under an unparseable config, stderr on a full-slot refusal carries the config path through the
  warning. Dismissed. The proposal requires that warning on every call, and the refusal text itself never names the
  path.
- adr-impact: no conflicts and no new candidates. The `slots.json` candidate from 2026-10-18 now has a settled shape.

### NIT-002 M3

Commit for M2: `bd17947 nitipan: add counted slots for resource leases`. The M3 commit is named in the row review
section.

Checks: `make ci` passed, before and after the review fixes. Lint reported 0 issues. `proposals.py freeze` returned
`[]`. `proposals.py check` reported only that every milestone is finished, so the proposal should be implemented. That
is written after the row review.

Restart: the first M3 implementer hung for about 25 minutes during the review fixes. Its `cg_run` of `awk` never
started a run. `awk` is not in `.cg.yaml`'s allow list, so the call was most likely waiting on a permission prompt.
One nudge got no response, so it was stopped. A fresh implementer continued from the partial work in the tree and
finished the fixes without `awk`.

Best guesses the implementers made:

1. The wait loop lives in the lease package as `Engine.AcquireWait`. `Engine.Acquire` keeps its single-attempt
   behavior.
2. Jitter puts each sleep in the upper half of its delay, 50 to 100 percent.
3. The timeout message repeats the last waitable refusal under a "Waited on:" heading. It adds the lock holder only
   when the last attempt was lock busy.
4. `--wait` parses with `time.ParseDuration`, so a bare number such as `5` is a usage error.
5. A refusal that includes a dead holder whose record asks to be kept is not waited on, since that holder never frees
   itself. Its status stays Refused (3).
6. Each wait attempt's lock budget is the time left, capped at the default budget, with a floor of one try.

Review findings:

- Reviewer, proposal and plan: an attempt could overrun the deadline by the full 5s lock budget, and the message
  still named the requested duration. Fixed: the state lock takes a per-call budget, and each wait attempt's lock wait
  is cut to the time left. A test asserts lock waiting plus sleeps equals the `--wait` duration.
- Reviewer, docs: a README line ran 174 characters. Fixed, along with three 121-character lines.
- Reviewer, PLAUSIBLE: the kill test killed the child after a fixed delay and could pass without the child attempting.
  Fixed: the child signals after its first refused attempt, and the parent kills it only then.
- Reviewer, docs: "nothing is written while waiting" was imprecise, since each attempt rewrites the lock file's holder
  note. Fixed: the help text, README, and proposal now say no file is created.
- adr-impact: no conflicts. Two candidates added to the Decision Log. Exit status 10 is `Undecided`. The wait semantics
  wait on NIT-004, since a broker may queue waiters.
- The sandbox write error takes the non-waitable path but has no test. No test can plant it.

## Row review

Commit for M3: `44f8042 nitipan: let acquire wait for a lease`. Range reviewed: `dff4a6d..44f8042`. The fix commit is
named in the final review section.

Checks after the fixes: `make ci` passed, and lint reported 0 issues. `proposals.py freeze` with the three row commits
returned `[]`.

Findings:

- Reviewer, CONFIRMED: a bad `slots.json` warned on every `--wait` attempt, about 150 times in a 5-minute wait.
  Fixed: a wait passes each distinct warning on once. Other commands are unchanged. A test asserts one warning over
  several polls.
- Reviewer, proposal: M1's As built block still said the effective count arrives later and that `slots` is written as
  1. Fixed as a correction of As built facts, before the proposal is marked implemented.
- adr-impact: NIT-002 cited the superseded ADR-02 as the envelope ADR in References and two risks. Fixed to ADR-04.
  Historical Decision Log entries are unchanged.
- adr-impact: the ADR-04 remote-record point is still rated PLAUSIBLE across the range. It stays dismissed and
  recorded as an `Undecided` candidate. If it becomes an ADR, that ADR should amend or supersede ADR-04 explicitly.
- Reviewer, not a finding: the remote and same-machine check repeats in four places. They agree today. A shared helper
  would be a tidy-up.

After the fixes, NIT-002 was marked `implemented` in the proposal and the index. The row already sat above every
unfinished row in `queue.md`, so the queue did not change. `proposals.py check` returned `[]`.

## Final review

Range: `dff4a6d..9a08f83`. Commits, in order:

1. `e53b4e3 nitipan: add resource leases`
2. `bd17947 nitipan: add counted slots for resource leases`
3. `44f8042 nitipan: let acquire wait for a lease`
4. `6125488 nitipan: warn once about a bad slot config during a wait`
5. `9a08f83 spec: mark NIT-002 implemented`

The reviewer reported no findings, including on the ADR review. The range touches no ADR file, and the proposal's ADR
references are correct. adr-impact reported no conflicts and no new candidates. `proposals.py freeze` with all five
commits returned `[]`. `make ci` passed on `9a08f83`, run by the coordinator, since the reviewers' sandboxes could not
build.

## ADR candidates for the user

All are in NIT-002's Decision Log. None was written.

- Remote resource records never overlap or count (departs from the past). This narrows ADR-04's other-machine rule
  for one kind. If written, it should amend or supersede ADR-04 explicitly. `Undecided`.
- Writers stamp the lowest minor a record's fields use (cuts across the system). `Undecided`.
- Exit status 10 means a `--wait` timed out (hard to reverse). `Undecided`.
- Waiting is bounded polling with no queue (cuts across the system). Waits on NIT-004 reaching `accepted`.
- The three 2026-10-18 candidates: the name grammar and kind `resource`, one record per slot holder, and `slots.json`
  with the minimum rule. Their hooks fired when NIT-001 was accepted, and their outcomes are still open.

## Left open

- No row is held or blocked. No stash was made.
- The run found no freeze hunks from other sessions, and no findings were left for the user.
- Four lines in NIT-002 that predate this run exceed 120 columns. The run did not reflow them.
