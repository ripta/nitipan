# Pushing under a broker lease

A broker lease guards a domain that several machines clone. A partitioned holder commits into its own clone and
collides with nobody. The collision is the push. Every broker grant carries a fencing token that only goes up, and a
push compares the holder's token with the fence refs on the remote.

The design is in [NIT-005](../../spec/proposals/NIT-005-fencing-tokens.md).

## Fencing tokens

The broker keeps one counter for every lease it grants. Each new grant's token is the larger of the counter plus one
and the broker's wall-clock time in microseconds. The counter and the grant land in one durable write before the reply
leaves. A newer holder of a name therefore always carries a higher token than any earlier holder of that name. Tokens
are comparable across every key, so nested and sibling keys order correctly.

Only `user` and `shared` leases carry a token. A `machine` lease is fenced by ownership, through the local
[git hooks](git-hooks.md).

The token reaches the CLI in the grant and renewal replies that the node delivers. `acquire` and `renew` print it, and
`list` shows it on each broker lease. `list --json` carries it as `token`. See [JSON output and
records](json-and-records.md). The holder's copy lives in the lease's sidecar under `broker/held/` in the state
directory.

A lease keeps its token for as long as it lives. A lapsed lease that its holder revives keeps the same grant and the
same token, since nobody claimed its keys in the meantime. A lease lost to another holder is never revived, and a
silent re-claim with a fresh token never happens. A new token after another holder's grant would let a stale holder
pass the fence. See [Terms and renewal](leases.md#terms-and-renewal).

A broker restored from an older store file loses its lease table. A renewal for a lease the broker does not know
returns "not found", and nothing is revived. The clock floor keeps the next grant above every token issued before the
restore, as long as the clock has moved forward.

A push refused by a fence token higher than any the broker has issued reports that token to the broker. A token at or
below the counter is known, and nothing changes. A token above the counter and at most 24 hours past the broker's clock
raises the counter to it. A token past that bound is unusable. The pusher then treats that fence ref as unusable, and
the refusal names a broker restore as the cause. The user deletes such a ref with `nitipan fence delete`.

A broker lease with no token refuses `nitipan push` and names the cause. That covers a lease granted before tokens
existed.

## `nitipan push`

`nitipan push <remote> [<branch> ...]` does one `git push --atomic` with the branch refs and the fence refs together. A
fence ref is `refs/nitipan/fence/<root-sha>/<encoded-path>`, and it holds the highest token pushed for that key.

The push fetches the fence refs, refuses when one holds a token higher than the holder's own, and otherwise writes its
token to the refs under its keys. The fence refs go by SHA with `--force-with-lease`, so git's compare-and-swap makes
the check and the write one step. A push that loses the compare-and-swap to a sibling push fetches again and retries, up
to five times. It then reports contention, exit status 13.

A refusal says the lease was granted to another session, and says to report to the user. With no refspec, the current
branch is pushed. A refspec is a branch, `<src>:<dst>`, or `:<dst>` to delete, with `+` to force. No pattern is
accepted. Run as your own terminal, with no agent above it, `nitipan push` is a plain git push.

## The pre-push hook

The `pre-push` hook is the authority. `nitipan setup git --install <checkout>` installs it. It recomputes the fence
refs the push needs and refuses a plain `git push` that lacks them, and the message names `nitipan push`.
`--no-verify` skips the hook, which is the accepted risk that hooks can be bypassed.

A caller with no agent variables, such as the user's own terminal, passes with no fence. A caller with agent variables
and no matching ancestor is refused when the push updates a branch or carries a fence ref. A push of tags alone passes,
as it does at the local `reference-transaction` hook.

An agent's own broker leases may not cover a path that has a fence ref at or above it. The broker must then say that no
live lease covers that path. When the broker cannot say, the push is refused as "could not verify", exit status 12,
which is not a lost lease.

Nothing deletes a fence ref automatically. The hook refuses any update of a fence ref that is not a creation or a
fast-forward, for every caller. It also refuses any push to a remote that holds a fence ref this clone lacks, or holds
at another value. A pruning push such as `git push --mirror` would delete it, and git does not tell the hook about
those deletions. The hook fetches the fence refs as it refuses, so pushing again passes. The refusal names the fetch it
ran:

    git fetch <remote> '+refs/nitipan/fence/*:refs/nitipan/fence/*'

One window stays. Another machine can create a fence ref after the hook reads the remote and before a pruning push
lands, and the push then deletes it. Only a server-side hook closes that window, so it is accepted.

An agent's push may carry only the fence refs its own leases call for. Any other fence ref is refused.

## Deleting a fence ref

`nitipan fence delete <remote> <key>` removes one fence ref, for example one marked unusable after a broker restore. The
key is `<root-sha>/<encoded-path>` or a repo name, `repo:<root-sha>#<path>`. The ref is deleted only if it still holds
the value the command read. It refuses any caller with agent variables, and no error an agent reads names it, because
deleting a live fence lets a stale push land.

## Hosts without atomic push

A host that cannot take an atomic push cannot hold the fence and the branch in one transaction. For such a remote,
`nitipan push` records `nitipan.<remote>.atomic = false` in the clone's config. The push then renews the holder's
covering leases with the broker and pushes the branches without fence refs. That is a lease check, and every message
says so. It is not fencing. An unreachable broker is "could not verify" and can be retried. A host that refuses refs
under `refs/nitipan/fence/` fails the whole push, and the message says it is the host's ref policy.

The pre-push hook trusts the flag whoever set it. An agent that runs `git config nitipan.origin.atomic false` in its
clone gets only the lease check on that remote, not the fence. This falls under the accepted risk that hooks are
bypassable, as `--no-verify` does. nitipan never clears the flag. A host that later gains atomic push stays on the lease
check until you run `git config --unset nitipan.<remote>.atomic`.

`make live-fence-hosts LIVE_FENCE_ARGS="<clone>"` probes each remote of a throwaway clone for atomic push and fence
refs. It pushes and then deletes a probe branch and a probe fence ref, and is never part of `make ci`.
