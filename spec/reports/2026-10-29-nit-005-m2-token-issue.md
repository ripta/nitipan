# NIT-005 M2: token issue work-queue run

Date: 2026-10-29. Trigger: a `/work-queue` run with no arguments, which means `next 1`. It picked queue row
`NIT-005 M2` from `spec/proposals/queue.md`. NIT-005 is the fencing tokens proposal
(`spec/proposals/NIT-005-fencing-tokens.md`). M2 makes the broker issue tokens floored at its clock, raise its counter
on a bounded report, and carry each token to the CLI and `list`. Environment: macOS, run from Claude Code's sandboxed
Bash in `/Users/ripta/projects/nitipan`. The approved plan is `/Users/ripta/.claude/plans/zesty-humming-moore.md`.

The system `python3` is 3.9, which cannot run the plugin's `proposals.py`. The run uses `/opt/homebrew/bin/python3`.

## Run state

- Code repo base commit for the run and for row `NIT-005 M2`: `09bab3f`
- Specs live in the code repo, so the specs commits are the code commits listed below
- The row depends on NIT-004 M2, which is `DONE`. No rows are held or blocked. `next` skipped no rows

| Milestone | Run status | Commit |
|-----------|------------|--------|
| NIT-005 M2 | DONE | see Commits below |

## Best guesses from the plan

- The margin for the bounded report is 24 hours, a named broker constant. "Counter durability" names no value
- "Marks the ref unusable" is a verdict in the report's reply, not broker state. The broker knows no fence refs, and
  none exist before M3
- The report has no user-facing command in M2. M3's `nitipan push` is its caller
- No counter floor at load, since every grant floors at the clock
- The existing `not_found` reply and loss notice meet "a renewal for a lease the broker does not know returns lost".
  M2 adds a restore test
- The report through the handoff grants no authority under ADR-08, since it only raises the counter within a bound

## NIT-005 M2

### Implementation

One implementer built the milestone. It set M2 to `DONE` with an As built block.

- The broker takes its wall clock once per claim. It computes `max(counter + 1, now)` in microseconds, and the reply
  and the stored counter use that one value. The dry-run token prediction is gone. There is no floor at load
- A new `fence` wire path, broker handler, handoff kind, and node forwarder carry a reported fence token. The verdicts
  are `known` (at or below the counter, no write), `raised` (above the counter, at most now plus 24 hours, one
  durable write), and `unusable` (above the bound, no write). A zero token is a 400. A CLI-side function sends the
  report, and no command calls it yet
- The handoff sidecar gains a token, at file version 1.1. The node writes it on grant and records it from renewal
  replies. The CLI writes it from grant and renew replies. Grant and renew output print the token
- `list` shows the token in text and JSON, and from the sidecar fallback. The `list --json` schema is 1.5
- NIT-005 "Counter durability" was amended with the margin, the verdicts, and no floor at load. NIT-004's "Broker
  lifecycle and store", its NIT-005 risk text, and its M2 As built line were amended to match, with one Decision Log
  entry

Checks: `make ci` passed (fmt-check, lint with 0 issues, vet-linux, `go test -race ./...` with `internal/cli` at
280 s, build). An earlier `make test` failed on four stale `schema_version "1.4"` assertions, and an earlier `make ci`
failed lint on one `unnamedResult` in a new test. Both were fixed. `proposals.py check` reports only that the row is
finished, which the row's finish handles. `proposals.py freeze` returned `[]`.

The Bash sandbox blocks local port binding, so the implementer ran one focused `go test` with the sandbox disabled.
Later checks ran through `cg_run`.

Best guesses the implementer added, each in a `(run best guess)` Decision Log entry:

- The handoff request version stays 2. An older node refuses the new kind through its presence record's kinds, and
  an older broker's 404 reads as too old. If wrong, the request version goes to 3 and `KindSince` gains an entry
- The sidecar goes to 1.1 with no new request version. The token rides in the broker reply, which the node forwards
  whole, and the CLI writes it from that reply. An older CLI that rewrites a sidecar drops the token. The next renewal
  reply restores it through the node
- The node records tokens from renewal replies, which the plan did not name. It is the only path that restores a
  token an older CLI dropped, so M3's push would not refuse a live lease for lack of one

ADR candidate the implementer recorded as `Undecided` in NIT-005's Decision Log: a fence token never falls below the
broker's wall clock, and a restored or reinstalled broker never issues below a token already on a remote, with a
bounded raise.

Left open: a node from before this change does not know the `fence` kind. M3's push then sees "node too old" for the
report until the node is upgraded.

### Milestone review

A reviewer and an `adr-impact` agent ran on the working-tree diff. `adr-impact` found no conflicts and no
supersessions. It judged ADR-04 and ADR-08 consistent, and found the change touches no other ADR in a way that
matters. It named one uncovered decision, the token floor invariant, which NIT-005's log already holds as the
`Undecided` candidate above.

Reviewer findings and dispositions:

1. NIT-004's three amended texts carried no "Amended" marker. Fixed: each now carries one.
2. NIT-005's Decision Log entry for "Counter durability" left out the prior meaning, "raises its counter above that
   token". The M2 check bullet still said "marks the ref unusable". Fixed: the entry gives both prior meanings, and the
   bullet now says the reporter treats the `unusable` verdict as an unusable ref, with the marker.
3. The counter at its maximum value would wrap to 0, and the next token could fall below the counter. PLAUSIBLE. It
   was sent to trace. The trace confirmed it: a damaged or hand-edited store loads that counter unchecked. The report
   path cannot reach it. Fixed: a claim fails closed with 503 when the counter cannot advance, with a test. The store
   still loads and still serves renewals. A `(run best guess)` Decision Log entry records it.

Checks after fixes: `make ci` passed. `proposals.py check` reports only the row move. `proposals.py freeze` returned
`[]`.
