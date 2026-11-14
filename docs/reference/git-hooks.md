# Git hooks and fence marks

`break --force` removes a live holder's lease, and the holder learns of it only on its next nitipan call. Until then it
can keep committing. A git hook closes that gap on one machine. The state directory is the authority, so the hook never
leaves the machine and needs no network.

The design is in [NIT-005 "Local fencing for NIT-001"](../../spec/proposals/NIT-005-fencing-tokens.md).

## The reference-transaction hook

`nitipan setup git --install <checkout>` installs a `reference-transaction` hook. It runs for every ref update:
`commit`, `rebase`, `reset`, `update-ref`, `branch -f`, and `branch -D`. `--no-verify` does not skip it. It judges only
updates of branches and HEAD, and every other ref passes. For each update it works out which paths the update touches,
and refuses it when it touches a path that:

- another holder's live record owns, under the sub-lease rule that a parent does not own what it delegated
- a record covers whose holder's liveness is unknown, or that nitipan cannot use
- an undelivered tombstone addressed to the caller lists
- a fence mark addressed to the caller guards
- a sub-lease of the caller ended at the deadline of its recall, which gives the keys back to the parent

A path no live record covers passes, because a lease is not required to commit. A dead holder's record does not refuse.
A caller with no agent variables is the user's own terminal and is never refused. A caller with agent variables and no
matching ancestor fails closed.

The paths of an update come from the commits it adds and removes, compared with the branches of every worktree and the
remote-tracking refs. A pull, a rebase onto upstream, and a checkout of `origin/main` therefore touch nothing. A branch
checked out in a worktree also counts the tree difference between its old and new values, when that worktree's index
still matches the old value. HEAD counts added commits only. `git gc` and `git pack-refs` pass.

A refusal prints the new commit and a pin, `git update-ref refs/nitipan/salvage/<id> <sha>`, that keeps it from being
collected. The pin writes outside `refs/heads`, so the hook lets it through. A pinned commit landed on a branch later is
still checked.

Each refusal kind has its own message. A refusal for a lease that ended quotes how it ended and names no command, and
says to report to the user. The recall refusal quotes the recall's deadline and the parent's holder in the same way.

The hook reads recalls without the lock. It applies the same rule as `list`. A sub-lease past its recall's deadline
counts as ended, so the parent owns its keys and the parent's commits pass. Before the deadline, the hook passes the
sub-lessee's commits and prints the deadline on stderr. That line writes nothing and consumes nothing. A recall record
the hook cannot use, and a recall whose parent record it cannot use, end no sub-lease. The hook reports each and
ignores the recall.

## Where the hooks go

The hook goes into the directory git uses for hooks, so a linked worktree shares it. It goes into every initialized
submodule too. An existing `reference-transaction` hook is kept and chained, and `setup git --uninstall <checkout>` puts
it back.

When that directory holds files git tracks, such as husky's, nothing there is written. The hook goes under the git
directory instead, the checkout's local `core.hooksPath` points at it, and every hook git documents chains to the
tracked directory. Uninstall restores the old `core.hooksPath`.

The script bakes the path of `nitipan` on PATH, not the symlink-resolved path that an upgrade can delete. It falls back
to the `nitipan` that PATH finds when the baked path is gone. With none, it refuses updates of branches and HEAD only,
and the message says to reinstall nitipan. A `nitipan` that does not know the hook is reported as too old.

A path claim inside a git checkout is refused until the hook is in force there, and the error names the install
command. Resource claims and paths outside any checkout are not affected. A tool that rewires `core.hooksPath` later
is caught at the next `acquire`. `break --force` says whether the broken lease's checkout had the hook in force, so the
user knows whether the break is fenced.

## The pre-push hook

The same install writes a second hook, `pre-push`. See [Pushing under a broker lease](pushing.md). A path claim in a
git checkout is refused until both hooks are in force. A checkout that has only the `reference-transaction` hook from
an earlier version therefore needs `setup git --install <checkout>` once more. An existing `pre-push` hook is kept and
chained like the first.

## Fence marks

A tombstone is delivered and removed on the holder's next call, so it cannot guard the keys for long. `break --force`
therefore also writes a fence mark in `marks/`, in the same locked call. A mark carries the broken lease's path keys and
the account of the break. While it stands:

- the hook refuses the holder's ref updates on those keys
- the holder's `acquire`, `acquire --replace`, and `request` over those keys are refused with status 7, and the refusal
  cannot be waited out

A mark lifts for a key once another holder's live record covers it, and it goes when its holder dies. `list` shows
marks, and `list --json` has them in the `marks` array. See [JSON output and records](json-and-records.md).

`break <mark-id>` removes a mark with no `--force`. A holder cannot remove a mark that guards against itself. A mark
nitipan cannot use is shown as unusable and needs `--force`. Resource leases and sub-leases that return to a live parent
get no mark.

Two mixed-binary gaps remain. An older binary's `break` writes no mark, and the mark appears when a newer binary
delivers the tombstone. An older binary's `acquire` by the marked holder ignores the mark. The hook still refuses that
holder's commits on those keys.
