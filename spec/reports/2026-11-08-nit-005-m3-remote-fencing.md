# NIT-005 M3: remote fencing work-queue run

Date: 2026-11-08. Trigger: a `/work-queue` run with no arguments, which means `next 1`. It picked queue row
`NIT-005 M3` from `spec/proposals/queue.md`. NIT-005 is the fencing tokens proposal
(`spec/proposals/NIT-005-fencing-tokens.md`). M3 fences broker leases at the push. A `nitipan push` wrapper sends the
branch refs and fence-ref compare-and-swaps in one atomic push. A pre-push hook refuses a push that lacks its fence
set. Environment: macOS, git 2.50.1, run from Claude Code's sandboxed Bash in `/Users/ripta/projects/nitipan`. The
approved plan is `/Users/ripta/.claude/plans/tidy-baking-wozniak.md`.

## Run state

- Code repo base commit for the run and for row `NIT-005 M3`: the commit "spec: finish the NIT-007 M1 work-queue
  session report"
- Specs live in the code repo, so the specs commits are the code commits listed below
- The row depends on NIT-005 M2, which is `DONE`. No rows are held or blocked. `next` skipped no rows

| Milestone | Run status | Commit |
|-----------|------------|--------|
| NIT-005 M3 | DONE | "nitipan: fence broker leases at the push" |

## Best guesses from the plan

- The fence ref name encoding is one reversible, canonical ref component. Bytes outside `[a-z0-9._-]` are
  percent-escaped with uppercase hex, uppercase letters included, so names never collide on a case-insensitive
  filesystem. The repo root encodes as `%00`. "Carrying the token" freezes an encoding but names none
- Fence commits use a fixed `nitipan <nitipan@invalid>` identity. "Carrying the token" says the fence names no holder,
  machine, or user
- A lost compare-and-swap is retried at most 5 times. "Token scope" says "a bounded number of times"
- The pre-push hook may write handoff request files for push rule 6 and the lease check. Only the
  `reference-transaction` hook is read-only under "Local fencing for NIT-001"
- A remote without atomic push is recorded in the clone's local config per remote. The pre-push hook accepts a
  fence-less push only to a remote so marked, after its own synchronous renewal
- Advertised baseline commits missing locally are dropped from the remote baseline, which only widens the touched set
- "Could not verify" and contention take new exit codes. Refusals stay exit 3, as for the local hook
- `unknown` and `stale` answers in rule 6 fail closed as "could not verify"
- The host probe runs on `~/projects/playground`'s four remotes (github.com, gitlab.com, git.sr.ht, a tangled knot),
  per the user's answer. huggingface.co stays unprobed

## NIT-005 M3

### Implementation

One implementer built the milestone, in about 41 minutes. It set M3 to `DONE` with an As built block.

- A new fence format package names refs `refs/nitipan/fence/<root>/<encoded-path>` and reads and writes the
  `fence.json` commit. A new pure engine applies the push rule: check sets under old and new roots, owned writes,
  ancestor refreshes, and rule 6
- The touched set takes its baseline as a parameter, and a remote variant runs the commit rule only, in repo-relative
  paths. The root set can be read from any commit, not only `HEAD`
- `nitipan push` runs one atomic push with the branch refs and fence refs, each fence by SHA with
  `--force-with-lease`. A lost compare-and-swap is retried up to 5 times. A higher token refuses and is reported to the
  broker. A host without atomic push is recorded in local config and gets the lease check
- A pre-push hook is installed beside the `reference-transaction` hook, chained and uninstalled the same way.
  `acquire` requires both. An M1-only checkout is told to re-install
- `nitipan fence delete` is the user's alone. New exit codes are 12 (could not verify) and 13 (contention)
- `make live-fence-hosts` probes a clone's remotes with the push shape of `nitipan push`. It is never part of `ci`

Checks: `make ci` passed (fmt-check, lint with 0 issues, vet-linux, `go test -race ./...`, build). `proposals.py
check` reports only the two row-finish findings the coordinator handles. `proposals.py freeze` returned `[]`.

Best guesses the implementer added, each in a `(run best guess)` Decision Log entry:

- The pusher's token for a path is the highest among its covering leases. Equal tokens pass. A newer-major fence
  refuses even a lease-less pusher
- `nitipan push` takes branches, `<src>:<dst>`, `:<dst>`, and a leading `+`. It takes no patterns and no other git
  flags, and defaults to the current branch
- `fence delete` passes the ref it deletes to the hook in `NITIPAN_FENCE_DELETE`. The hook allows the deletion only for
  a caller with no agent variables, and only of that ref
- A lost compare-and-swap is detected by listing the remote's fence refs again, not by git's wording
- A remote named by URL cannot be recorded as non-atomic, so the push says to add it as a remote
- An agent in a shallow clone is refused once fence refs exist or it holds a broker lease, since its roots are unknown

Deviation: git does not give the pre-push hook the refs that a `--mirror` or `--prune` push deletes. The settled
refusal of a fence deletion could not see them. The implementer amended "Tokenless pushers and fence cleanup" so a
pruning push, detected from the parent `git push` arguments, is refused when the remote holds a fence ref the clone
lacks. Review then showed that detection can be evaded. See the review below.

Test cost, under `-race`: `internal/cli` 431.9 s for the whole package, of which the new push tests take about
60-75 s. `internal/setup` 49.8 s, `internal/gitfence` 13.5 s, `internal/rootset` 9.2 s, `internal/fenceref` 1.8 s,
`internal/fenceplan` 1.0 s. The `internal/cli` package was 280 s in NIT-005 M2. The project has no `make test-cost`
target.

### Host probe

The coordinator ran `scripts/live-checks/fence-hosts.py` with the sandbox disabled, for the SSH agent. It ran from a
throwaway clone of `~/projects/playground` in the session scratchpad, with that repo's four remotes. A dry run first
printed the plan.

| Remote | Host | atomic | create | fast-forward | delete |
|--------|------|--------|--------|--------------|--------|
| github | github.com | yes | yes | yes | yes |
| gitlab | gitlab.com | yes | yes | yes | yes |
| srht | git.sr.ht | yes | yes | yes | yes |
| tngl | a tangled knot | yes | yes | yes | yes |

A follow-up `git ls-remote` on each remote found no `refs/nitipan/*` and no probe branch left behind. huggingface.co
has no remote in the scratch repo and was not probed. The As built block records the result, and the risk "The user's
remote hosts may not support atomic push" moved from likelihood high to low, with an amendment and a Decision Log entry.

### Review

A reviewer and an `adr-impact` agent ran on the working tree.

| Finding | Verdict | Disposition |
|---------|---------|-------------|
| The pruning guard misses abbreviated `--mir`/`--prun`, aliases, and `-c remote.X.mirror` | CONFIRMED | fix: make the guard unconditional on a fence ref the clone lacks |
| `want()` drops a ref-name error, so a long key path pushes unfenced | CONFIRMED | fix: refuse with its own kind |
| A fence write can lower a stored token with nested keys | PLAUSIBLE | trace |
| The hook does not check carried fence refs outside the plan, so an agent can plant a high token | PLAUSIBLE | trace |
| Fence key containment is a second rule beside ADR-03's and drops `Normalize` | PLAUSIBLE (ADR-03) | trace |
| `fence delete` picks `Roots[0]`, not the first-parent root | plan gap | fix |
| Two stale doc comments in the hook install code | comment | fix |
| An agent can set `nitipan.<remote>.atomic=false` and get only the lease check | accepted risk | left for the user |

The last row falls under the accepted risk "Hooks are bypassable". It is cheaper for an agent than `--no-verify`.
The user may want the README to say the flag is trusted.

The `adr-impact` agent found ADR-04, ADR-05, ADR-07, and ADR-08 consistent. It listed two uncovered decisions: the
fence format on remotes, already an undecided ADR candidate, and the user-versus-agent split for fence deletion, which
it rated low priority.

### Fixes and traces

The same implementer took the fix and trace findings. All three traces confirmed the finding.

- The pruning guard is unconditional. On every push, the pre-push hook lists the remote's refs. It refuses when the
  remote holds a fence ref that the clone's namespace lacks or holds at another value, and names the fetch. Its own
  fetch then fills the namespace, so a second push passes. The `ps` detection is gone from the Go code and the script.
  Tests cover `git push --mir`, an aliased mirror push, and `-c remote.origin.mirror=true`. `nitipan push` records the
  fence refs it wrote, and `fence delete` syncs first, so ordinary flows never meet the guard
- Because no input shows that a push is harmless, the pre-push script now starts `nitipan` for every push. A missing
  binary fails every push closed. "Installing the git hooks" was amended to say so. The `reference-transaction` hook
  keeps its fast path
- A key whose fence ref name cannot be built, such as a path over 255 encoded bytes, refuses with its own kind
- A fence ref whose stored token is above the pusher's token for that ref is refreshed at the stored token, never
  lowered. The diagnosis was confirmed by reading the code. The regression test was not run on the unfixed code, so
  it pins the fix, not the bug
- An agent's push that carries any fence ref outside the plan is refused, also on a remote marked non-atomic. The
  user's own terminal still passes with no fence update, as the settled text has it, so a user may write a fence ref by
  hand
- Fence key containment calls the shared ADR-03 rule with each entry's FoldCase and Normalize. A fence key is compared
  under the pusher's rules, folded when any of its keys in that repo folds. Two pushers on different volumes can
  disagree on a case-variant path, and the fence then errs toward refusing. This is a `(run best guess)` entry
- `fence delete` maps a typed `repo:` root to the clone's first-parent root
- The two stale comments in the hook install code were reworded

A follow-up amendment finding: "Installing the git hooks" still said a missing binary passes every ref but branch and
HEAD updates. The implementer amended it to state both hooks' rules, with a Decision Log entry quoting the prior text.

Checks after the fixes: `make ci` passed, with lint at 0 issues, `internal/cli` at 416.7 s, and `internal/setup` at
12.2 s. `proposals.py check` reports only the two row-finish findings. `proposals.py freeze` returned `[]`.

ADR candidates: none new. The unconditional prune guard, the non-atomic flag, and the user-only fence deletion each
shape only the pre-push hook. They fail the Architecture test in `design.md`. The fence format candidate is already in
the Decision Log, undecided.

## Row review

A fresh reviewer and a fresh `adr-impact` agent ran on the row's range, the commit "nitipan: fence broker leases at the
push". `proposals.py freeze` with that commit as the row's specs commit returned `[]`. The reviewer found that every
milestone-review fix holds. It found no divergence between the push wrapper and the hook, which share one plan. It also
found that M1's local hook keeps the same baseline arguments after the refactor. The `adr-impact` agent found no
conflicts, and confirmed the ADR-03 containment fix.

| Finding | Verdict | Disposition |
|---------|---------|-------------|
| `nitipan push` and `fence delete` give network and history calls the 10 s clean-runner timeout | PLAUSIBLE | trace |
| The amended fence-namespace bullet carries no amendment marker | proposal | fix |
| A fence ref created on the remote between the hook's listing and a pruning push can still be deleted | PLAUSIBLE, low | trace: state the window as accepted |
| The README says an unidentifiable agent is always refused, but a tag-only push passes | doc | fix |
| huggingface.co was not probed for M3's host check | plan | left for the user |

The As built block says plainly that huggingface.co was not probed. The risk entry stays open.

### Row review fixes

A fresh implementer took the row-review findings. Both traces confirmed the finding.

- The push wrapper's git calls now get the hook's 2-minute bound. The clean runner keeps only its environment
  cleaning, and setup and the `acquire` check keep 10 s. A runner seam test failed on the unfixed code with
  `timeout = 10s, want 2m0s`, and passes with the fix. The value is a `(run best guess)` entry
- "Tokenless pushers and fence cleanup" carries one marker, on its decision line, covering every bullet the run
  changed. The Decision Log gives each bullet's prior meaning
- The window between the hook's listing and a pruning push is stated as accepted in that decision, its Decision Log
  entry, and the README. It cannot be closed without a server hook
- The README and the proposal now say an unidentifiable agent is refused when its push updates a branch or carries a
  fence ref. A tags-only push passes, as at NIT-001's local hook

Checks: `make ci` passed, with lint at 0 issues, `internal/cli` at 433.4 s, and `internal/gitfence` at 10.3 s.
`proposals.py check` reports only the two row-finish findings. `proposals.py freeze` returned `[]`.
