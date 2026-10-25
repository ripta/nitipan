# NIT-005 M1: local fencing work-queue run

Date: 2026-10-25. Trigger: a `/work-queue` run with no arguments, which means `next 1`. It picked queue row
`NIT-005 M1` from `spec/proposals/queue.md`. NIT-005 is the proposal for fencing tokens
(`spec/proposals/NIT-005-fencing-tokens.md`). M1 fences `machine` leases on one machine, with no broker. Environment:
macOS, run from Claude Code's sandboxed Bash in `/Users/ripta/projects/nitipan`.

## Run state

- Code repo base commit for the run and for row `NIT-005 M1`: `247eba5`
- Specs live in the code repo, so the specs commits are the code commits listed below
- The row has no dependencies. No rows are held or blocked. `next` skipped no rows

| Milestone | Run status | Commit |
|-----------|------------|--------|
| NIT-005 M1 | DONE | `cabd387` |

Commits, in order:

1. `cabd387` nitipan: fence local ref updates against broken and foreign leases
2. `81a629c` nitipan: keep global hooks and fence marks intact on edge paths
3. The `spec:` commit that carries this amendment, which moves the finished rows to `history.md`

The first two were committed as `0dc89ee` and `46322f9`. Their bodies opened with a recap of the prior state, which
`commit-style.md` forbids. Neither was pushed, so both were reworded with `git commit-tree`. The trees, authors, and
dates are unchanged.

The row is finished. NIT-005 stays `accepted`, since M2 and M3 remain. `NIT-005 M1` moved from `queue.md` to the new
`history.md`. The three finished rows that earlier runs left in `queue.md` moved ahead of it, in build order: NIT-001
M1-M4, NIT-002 M1-M3, and NIT-003 M1-M3. `proposals.py check` then returned `[]`.

## Row and final review

The row has one milestone, so the row review and the run's final review cover the same range, `247eba5..0dc89ee`.
One reviewer pass served as both, and included the final ADR review. An `adr-impact` agent ran beside it.

Findings and dispositions:

1. Chain scripts baked a raw `~/...` from a global `core.hooksPath`, which the shell never expands. Every global hook
   was skipped. CONFIRMED with a scratch repo. Sent to fix.
2. "Outside a checkout" matches git's English stderr, and nothing sets `LC_ALL=C`. PLAUSIBLE. Sent to trace.
3. `break` publishes a fenced tombstone before the mark. If the mark write fails, a fenced tombstone with no mark
   remains. PLAUSIBLE. Sent to trace.
4. "The fence mark" carried two amendment markers. Sent to fix.
5. A mid-bullet marker in "Installing the git hooks", and a log entry claiming an amendment with no text change. Sent
   to fix.
6. Rebase cost numbers appeared in both the As built block and the Decision Log. Sent to fix.
7. ADR-04 against the mark directory's mixed-binary gaps. Left for the user. It is already an Undecided candidate.

Fix round results, from a fresh implementer:

1. Fixed. `core.hooksPath` is read with `--type=path`, and a replayed prior value is expanded through git. The raw
   value is still what uninstall restores. A regression test with a temp `HOME` failed before the fix.
2. Uncheckable here, since the sandbox denied the probe of git's translations. Fixed anyway: `gitfence` pins
   `LC_ALL=C` and `LANGUAGE=C` for every git call. A fake-git test failed before the fix. This is a run best guess.
3. Confirmed with an injected failure. `break` now writes the mark first and the tombstone second, and removes the
   mark if the tombstone write fails. The lease goes only after both exist. A regression test failed before the fix.
   The mark-rollback path has no test. "The fence mark" is amended to say the mark comes first.
4. Fixed. "The fence mark" has one marker.
5. Fixed. "Installing the git hooks" now states the scope-value and `~` behavior, with the marker at the end of the
   bullet.
6. Fixed. The cost numbers live only in the Decision Log, and the As built block points there.

Checks after the fix round: `make ci` passed. `proposals.py freeze --row-commit 0dc89ee` returned `[]`.
`proposals.py check` reported only the four finished queue rows. The coordinator's `go build ./...` passed.

The `adr-impact` pass found the `fenced` field consistent with ADR-04, since leaving it out changes no old reader's
decision. It found the same PLAUSIBLE ADR-04 point as before. It raised no new conflict.

## Plan-time facts

- Session reports go in `spec/reports/`. The preference file under `~/.config` is denied by the sandbox. Prior runs
  used `spec/reports/`.
- Local checks: `make ci`, which runs `fmt-check lint vet-linux test build`. Plus `proposals.py check` and
  `proposals.py freeze`.
- A hidden `hook` command already serves agent session hooks. Its subcommands go through `hookSub`, which needs JSON
  stdin and a session ID. The git hook needs its own subcommand beside them.
- `keys.ExecGit` strips `GIT_DIR` and `GIT_WORK_TREE`. The git hook runs inside git's hook environment.
- The NIT-005 Decision Log ADR candidate about the hook requirement "waits on NIT-005 M1". This run does not flip it.
  Once M1 lands, the next proposal review can act on it.

## Best guesses made at plan time

Each guess names the ambiguity, the choice, the document it rests on, and what changes if it is wrong. Each rests on
NIT-005 unless noted.

1. The git hook's subcommand is `nitipan hook reference-transaction <state>`, reading git's stdin. NIT-005 says only "a
   read-only `hook` subcommand". If wrong, the installed script and the subcommand name change together.
2. Marks are stamped 1.3, the next minor. `list --json` moves to schema 1.3 when it adds a `marks` array. This follows
   the "lowest version that needs the fields" pattern of `ParentVersion` 1.2. A different number changes constants
   and fixtures.
3. A mark has its own ULID and records the broken lease ID. `break <mark-id>` needs no `--force`, since a mark is not a
   lease and lifting it is the user's act. If wrong, `break` needs `--force` for a mark.
4. Marks cover path keys only. Resource leases have no git collision to fence. If wrong, a broken resource holder's
   re-claim is also refused.
5. HEAD counts added commits only. Removed commits count for `refs/heads/*`. NIT-005 says HEAD gets "the commit rule
   only". Counting removed commits for HEAD would refuse `checkout main` from a detached commit.
6. Repo-relative paths map against the worktree that has the ref checked out, else the worktree the hook runs in. The
   sibling-worktree checks depend on this.
7. A mark refusing `acquire` exits 7, "broken", and is not waitable. If wrong, it takes exit 12.
8. A mark written at delivery from an old-binary tombstone has only string paths. Its key flags take canonical
   defaults.
9. NIT-001's "Superseded in part by NIT-005" marker lands with M1. The mark's narrowing is live once M1 merges, though
   NIT-005 says "when this proposal ships".
10. An unusable record whose envelope does not decode covers every touched path, since it already refuses every
    `acquire`.
11. M1 installs only `reference-transaction` in the ordinary mode. The pre-push hook belongs to M3. The `acquire`
    in-force check catches an install from before M3.
12. Commit subjects follow history: `nitipan: <what>` for milestone commits, `spec: <what>` for spec-only commits.

## Milestones

### NIT-005 M1

Checks after the first implementation pass: `make ci` passed, with 0 lint issues. `proposals.py freeze` returned
`[]`. `proposals.py check` reported only finished queue rows still in `queue.md`: NIT-001 M1-M4, NIT-002 M1-M3,
NIT-003 M1-M3, and NIT-005 M1. Earlier runs left the first three behind. This run moves all four at row finish. The
`internal/cli` tests went from about 31s to about 237s under `-race`. Most of that is the end-to-end rebase and
worktree cases.

Empirical findings:

1. Hook cost: a 60-commit rebase took 0.37s without the hook and 7.0s with it, about 19 times as long. A built binary
   gave about 6.2s. With an opencode session-kind lease whose host died, the median was 9.3s. Codex was not measured.
   NIT-005 rates the risk "The hook runs on every branch and HEAD update" as low impact. That rating deserves the
   user's look.
2. `git gc` and `pack-refs --prune` fire the hook. They send packed refs as creations, then one deletion per loose
   ref. A deletion passes when `packed-refs` holds the ref at the same value and `packed-refs.lock` is absent. This
   reads git internals. It was verified on the files backend of git 2.50.1 only.
3. In a linked worktree, the hook's cwd is the worktree root and `GIT_DIR` is `<common>/worktrees/<name>`. One install
   in the common hooks directory covers every worktree.

Deviations from the plan, each as the implementer reported it:

1. NIT-001 has the "Superseded in part by NIT-005" marker but no Decision Log entry. `proposals.py freeze` rejects
   the entry, since it is neither a revisit audit nor a correction of fact. `proposals.md` allows the marker, but not
   the log entry the marker names. This looks like a defect in the standards.
2. `branch -f` on a branch checked out in a sibling worktree is refused by git before any hook runs. The M1 check was
   amended to "`branch -f` off a branch's unique commits".
3. `grant` has no separate lift path. The cover rule lifts the mark in the grant's own locked call. "The fence mark"
   was amended.
4. The acquire gate is a plain exit-1 error naming `nitipan setup git --install <checkout>`. The plan had named the
   `GateError` path.
5. Each test agent is a long-lived `sh` whose PID is its `CLAUDE_PID`, so two real holders can coexist.

Best guesses the implementer made beyond the plan's, each in NIT-005's Decision Log:

1. The `packed-refs.lock` test above decides when a loose-ref deletion is a prune. If it is wrong, `git gc` could
   refuse a branch whose commits touch another holder's keys.
2. The hook subcommand exits 0 to pass, 3 to refuse, and 1 to fail closed. The script reads exit 2 as "too old".
3. The guarded holder cannot remove its own mark: `break <mark-id>` exits 6. An unusable mark needs `--force`.
4. A key already covered by another holder's live record is left out of a new mark. A mark with no keys is not
   written. So a revoked sub-lease, or one whose keys return to a live parent, leaves no mark.
5. An unusable mark whose keys decode refuses those keys for any agent caller. One whose keys do not decode is
   reported and ignored.
6. An unusable lease record covers a path by its raw footprint when that decodes, and every path otherwise.
7. The script bakes `LookPath("nitipan")` when it resolves to the running binary, otherwise the resolved executable.
8. An existing hook is kept as `reference-transaction.nitipan-orig`. Tracked-directory mode uses
   `<git-dir>/nitipan-hooks` and records the prior `core.hooksPath` in `prior-hooks-path` there.
9. `acquire` checks the hooks on every call, a refresh included. A path git cannot be asked about counts as outside a
   checkout.

Left open by the implementer:

- The hook opens the state directory through `state.Open`, which creates it when it is missing. That is one `mkdir`
  in a read-only hook.
- With no `nitipan` binary, the script fails closed on the symbolic HEAD update that a plain `git checkout <branch>`
  sends.

Milestone review: findings and dispositions.

1. A mark removed by `break <mark-id>`, or lifted per key, came back when the holder's tombstone was delivered.
   CONFIRMED by a scratch test. Sent to fix.
2. Re-running `setup git --install` on a tracked hooks directory pointed every chain at `unset/<name>` when the prior
   local `core.hooksPath` was unset. CONFIRMED. Sent to fix.
3. `setup git --install` may write into a shared global hooks directory. PLAUSIBLE. Sent to trace.
4. The hook's `state.Open` creates the state directory, and may fail closed when that is denied or the format is
   newer. PLAUSIBLE. Sent to trace.
5. The acquire gate runs `git` under the state lock. Any git error counts as "outside a checkout", so the claim
   passes unfenced. PLAUSIBLE. Sent to trace, with a narrowing of the git-error rule.
6. The 19-times cost on a long rebase, against a risk rated low impact. Left for the user. Re-rating a risk is the
   user's call.
7. NIT-001's marker says "see Decision Log", but the entry is in NIT-005's log. Sent to fix. Deviation 1 stands: the
   plan's NIT-001 Decision Log entry was dropped, since `freeze` rejects it.
8. Four comment-craft findings. Sent to fix.
9. The reviewer asked for an e2e test of the `packed-refs.lock` rule with a real `git branch -D`. Sent to fix.

The reviewer accepted the five reported deviations.

Fix round results:

1. Fixed. A tombstone written by `break --force` now carries a `fenced` field. Delivery writes a mark only for a
   tombstone without it, which means an older binary wrote it. An old reader ignores the field and delivers the
   tombstone as before. Two regression tests failed before the fix.
2. Fixed. The install reads every `core.hooksPath` scope, drops nitipan's own value, and chains to the last remaining
   one. A regression test using `GIT_CONFIG_GLOBAL` failed before the fix.
3. Confirmed by reading the code, and fixed. Any hooks directory outside the repository's git dir is treated as shared,
   and gets the nitipan-owned directory with chaining. No regression test was run before the fix.
4. Confirmed for the mkdir, and fixed with a read-only open. A newer format marker never stopped the hook. A
   regression test failed before the fix for the absent directory. A denied mkdir was not tested directly.
5. Confirmed, and fixed. The acquire gate asks git before the lock, once per directory. Only "not a git repository"
   and a missing git binary count as outside a checkout. Any other git failure refuses the claim. No test shows git
   runs outside the lock.
7. Not changed. `freeze` rejects any marker wording but "see Decision Log", so NIT-001's marker still points at a log
   that has no entry. This is a gap in `proposals.md`: it allows the marker but not the entry the marker names.
8. Fixed.
9. Fixed with a real `git branch -D` of a loose and packed branch. It is refused.

The implementer's new best guesses are in NIT-005's Decision Log: the `fenced` tombstone field, the `core.hooksPath`
scope read, the shared-directory rule, the read-only open, and the gate's git-error rule. The coordinator changed the
cost entry's "stays rated low impact" to leave the rating for the user. It also added the Undecided ADR candidate below.

Checks after the fix round: `make ci` passed. `proposals.py freeze` returned `[]`. `proposals.py check` reported only
the four finished queue rows.

ADR impact: no confirmed conflict. One PLAUSIBLE conflict with ADR-04's minor rule: a fence mark lives in a directory
older binaries never scan, so an older binary's `acquire` and `break` ignore it. Dismissed as a conflict for this run.
The accepted proposal settled those two mixed-binary gaps before the run, and rejected a `format` bump on purpose.
ADR-04's minor rule governs fields on records, and a new directory is not a field. It is recorded as an ADR candidate
below.

ADR candidates:

- The existing candidate "a path claim inside a git checkout requires nitipan's git hooks in force" waited on M1. M1
  is done. Its log entry is left for the next proposal review.
- New, Undecided: a new kind of state-directory file that older binaries never scan may accept stated mixed-binary
  gaps instead of a `format` bump. It departs from ADR-04's rule that an old reader is never more permissive.
