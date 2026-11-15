# Fence fallback for hosts that refuse fence refs

**Date:** 2026-11-14

This report covers a `/work-queue` run of NIT-009 M1-M3 in the local macOS checkout at
`/Users/ripta/projects/nitipan`. NIT-009 is `spec/proposals/NIT-009-fence-fallback-for-refusing-hosts.md`. It gives a
remote's fence mode one key, `nitipan.<remote>.fence`, with three states. It classifies a failed fenced push from
`git push --porcelain`. It lets the pre-push hook accept the lease-check fallback that `nitipan push` sanctions.

## Run state

- Run: default `next 1`, the row NIT-009 M1-M3. Code and specs share this repository, on `main`. In progress.
- Run base and row base commit subject: `spec: schedule NIT-009 M1-M3`.
- Dependencies: none. NIT-005 is implemented. No `live_after` entries. No skipped, held, or blocked rows.
- Machine-local state tracks commit identifiers outside committed documentation.
- Installed git: 2.50.1 (Apple Git-155).

| Milestone | Run status | Commits |
|-----------|------------|---------|
| NIT-009 M1 | DONE | `nitipan: judge a failed fenced push from git's porcelain output` |
| NIT-009 M2 | DONE | `nitipan: give each remote a three-state fence mode with a sanctioned fallback` |
| NIT-009 M3 | QUEUED | |

## Approved plan and authorization

The user approved the run plan on 2026-11-14. It authorizes implementation, milestone, row, and final reviews,
proposal completion, and commits by explicit path on the checked-out branch. It authorizes best guesses the proposal
supports, and stashing a blocked milestone's work by path. It authorizes no push, pull request, branch change,
destructive git operation, or action on another session's edits.

Local checks for every milestone: `make fmt`, `make ci` (fmt-check, lint, vet-linux, race tests, build), then
`proposals.py check` and `proposals.py freeze`. M3 also runs the probe with `--dry-run` and against a local bare
remote. Running `make live-fence-hosts` against the user's real hosts is left to the user.

## Best guesses from the plan

- The collateral strings come from the installed git's own output, pinned by tests. Rule 3 says M1 pins them. A git
  version that words them otherwise shows up as a failing test.
- M1 classifies but keeps today's mode handling. M2 wires the outcomes to the three states.
- The old-hook skew test uses a hook script that runs the current binary with `NITIPAN_FENCE_FALLBACK` stripped. For the
  unset-key case that is the old hook's only difference, per "Version skew". A faithful old binary would need a build
  from history.
- The reinstall hint for a user's `fence=false` meeting an old hook is keyed on the hook refusing a fence-less push
  under `fence=false`. The pusher cannot see the hook's version.
- An invalid `atomic` value is an error naming the key and file, only when `fence` is unset and the old key is
  consulted. With `fence` set, `atomic` sets no mode, and only `atomic=false` triggers the notice.
- Docs beyond the three files M3 names are corrected too, since they describe the removed `atomic=false` write.

## Milestones

### NIT-009 M1: Porcelain classifier

DONE. Commit: `nitipan: judge a failed fenced push from git's porcelain output`, which carries the proposal and this
report. The bug it found is in its own commit, `spec: file the hook's misleading refusal of a behind branch`.

The fenced push runs with `--porcelain`, and `gitPush` tees stdout as it does stderr. A new classifier in
`internal/cli/pushclassify.go` judges a failure by the four rules in order. Mode handling is unchanged, as planned:
a no-atomic refusal still records `atomic=false` and takes the lease check, and a fence refusal is still an error, now
naming the ref and the host's reason.

Checks: `make fmt` and `make ci` passed, with one gocritic `argOrder` finding fixed on the first run. `proposals.py
check` and `freeze` were clean. The review fixes changed a test name and a comment, then `make fmt-check` and `make
vet-linux` passed. Test cost: `internal/cli` took 501 s wall in `make ci`, run in parallel with the other packages.
That is not an isolated measurement.

Implementer best guesses:

- `--porcelain` goes on the fenced push even when it carries no fence writes, so the output format does not change
  between pushes. The lease-check push and the user's passthrough push do not use it.
- The old `stale info` stderr match is dropped. Rule 1 asks the remote only, as the proposal says.
- Rule 2 runs before the malformed-output check, since it reads stderr, not porcelain.
- git's client-side `atomic push failed` is not on the collateral list. It always shows as `[rejected]`, which rule 3
  excludes. A plain-git test pins it.

Deviation: git leaves a locally rejected branch out of the pre-push hook's input. On a non-fast-forward branch, the
nitipan hook refuses the lone fence ref before the remote is asked. The outcome still matches rule 4: no fallback,
nothing recorded, and git's output is shown. The As built block records it.

Review findings:

- The hook's refusal of a behind branch is misleading. Confirmed. It needs a design call, so it is filed as
  `spec/bugs/20261114-nonff-branch-refused-as-extra-fence.md`, not fixed.
- The unreachable-remote test claimed to exercise the classifier. Plausible, then confirmed by trace: it fails at
  `pushOnce`'s `ls-remote`. The test is renamed and its comment says the classifier table owns that case. Fixed.
- The As built block claimed `--porcelain` on every leased push. Fixed to say the fenced push.
- adr-impact: no conflict. ADR-09 is consistent. No ADR candidate passes the bar.

### NIT-009 M2: Fence modes and the sanctioned fallback

DONE. Commit: `nitipan: give each remote a three-state fence mode with a sanctioned fallback`, which carries the
proposal and this report. The test-cost bug it found is in its own commit,
`spec: file the cli test package's closeness to go's test timeout`.

One reader in `internal/cli/fencemode.go` serves the pusher and the hook. It reads `fence`, then the legacy `atomic`
when `fence` is unset, with `--show-origin`. nitipan writes neither key. Unset falls back on a rule 2 or 3 refusal,
`true` fails naming the key, and `false` skips the fenced attempt. Only the fallback call carries
`NITIPAN_FENCE_FALLBACK`. The hook accepts the fallback under the three conditions and runs its own lease check.

Checks: `make fmt` and `make ci` passed after the review fixes, with 0 lint issues. `proposals.py check` and `freeze`
were clean. Test cost: `internal/cli` took 585 s before the review fixes and 568 s after, in `make ci`. One run in the
fix round hit go's 600 s package timeout with no failing test. That is filed as
`spec/bugs/20261114-cli-tests-near-go-timeout.md`.

Implementer best guesses:

- The fallback warning prints after the lease check passes. When the lease check fails or cannot run, the error leads
  with the host's reason and omits the `fence=false` and `fence=true` advice. The review accepted it.
- The reinstall hint keys on a shared refusal phrase, `missingFencesPhrase`. It is "and it lacks the fence refs", since
  the hook's mirror refusal also says "lacks the fence refs".
- The legacy notice prints once per push. `nitipan push` prints it and marks its git calls with
  `NITIPAN_FENCE_NOTICE=1`. The hook prints it for an agent's push without the mark. A hand-set mark only loses the
  line. The proposal's "Replacing `nitipan.<remote>.atomic`" and "Version skew" are amended, with a run-best-guess
  Decision Log entry.
- The old-hook skew tests run the real hook binary with `NITIPAN_FENCE_FALLBACK` stripped, in the wide fixture. The
  shell rig has no broker for the pusher's lease check. For a user's `fence=false`, the old hook also runs with
  `GIT_CONFIG_GLOBAL=/dev/null`, to model a hook that reads no `fence` key. Test-only variables in `runAsNitipan` let
  the hook binary see the fixture's agent.
- Under a user's `fence=false` meeting an old hook, the error names the reinstall step but does not repeat the key.

Review findings:

- The legacy notice printed only from `nitipan push`, so a plain agent push under `atomic=false` was silent. Plan gap.
  Fixed as above.
- The old-hook skew test used a stub printing copied text, a shortcut the plan did not approve. Plausible, then
  confirmed by trace: `verifyUncovered` needs no broker on that path. Fixed with the real hook binary.
- A remote named with `/`, `@`, `:`, or a space read as unset, so `fence=true` on `team/origin` silently fell back.
  Confirmed. Fixed: git answers every name but an empty one or one with a newline. Regression tests failed on the old
  rule first.
- The reinstall hint matched a hand-copied substring. Fixed with the shared constant.
- adr-impact: no conflict. It named the fence-mode contract and the `NITIPAN_FENCE_FALLBACK` handshake as possible ADR
  candidates. Dismissed: NIT-009's Decision Log already weighed and declined who may set the mode, under NIT-005's
  shipped trust model, and the handshake follows `NITIPAN_FENCE_DELETE`. Neither adds a new principle.
