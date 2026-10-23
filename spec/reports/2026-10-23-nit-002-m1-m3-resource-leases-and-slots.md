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
| NIT-002 M1 | DONE | see M2 |
| NIT-002 M2 | QUEUED | |
| NIT-002 M3 | QUEUED | |

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
