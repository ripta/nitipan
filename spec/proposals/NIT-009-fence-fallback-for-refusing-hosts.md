# NIT-009: Fence Fallback for Hosts That Refuse Fence Refs

**Status:** draft
**Created:** 2026-11-14
**Updated:** 2026-11-14

## Dependencies

- NIT-005

## Impacts

- NIT-005 — "What enforces the token", in the bullet on hosts without atomic push. This proposal replaces how that
  fallback is chosen and recorded. M3 puts a "Superseded in part by NIT-009" marker on it
- NIT-005 — "Token scope", in the sentence that a host refusing the fence namespace fails the whole push. Under this
  proposal such a host gets the lease check unless the user asks for fencing to be required. M3 marks it
- NIT-005 — the risk "The user's remote hosts may not support atomic push", whose text says a host that refuses fence
  refs fails the push outright. M3 marks it
- NIT-005 — the 2026-11-08 Decision Log entry that records `nitipan.<remote>.atomic`. This proposal replaces that key.
  M3 records the supersession in NIT-005's Decision Log

## Summary

A remote's fence mode becomes one git config key, `nitipan.<remote>.fence`, with three states:

- unset, the default: try the fenced push. When the host refuses `--atomic` or refuses the fence ref, fall back to the
  lease check for that one push, record nothing, and say so on stderr
- `true`: always fence. A host refusal is an error
- `false`: never try the fence. Every push gets the lease check

The pusher classifies a failed push from `git push --porcelain`, not from the wording of git's stderr. Only a host
refusal of `--atomic` or of a fence ref can cause a fallback. A lost lease, contention, a branch rejection, and a network
or auth failure never do. The pre-push hook learns to accept the fallback push that `nitipan push` sanctions, and still
runs its own lease check on it.

## Motivation

A host that refuses refs under `refs/nitipan/fence/` fails every push into a fenced path. Ref-creation rules, naming
rules that forbid deep ref names, and pre-receive hooks can each cause it. Nothing is recorded, so the next push fails
the same way. The only way out is to hand-set `nitipan.<remote>.atomic=false`, a key whose name says something else.

A host without atomic push gets the opposite treatment. Its first refused push writes `nitipan.<remote>.atomic=false`,
and nothing ever clears it. A host that gains atomic push later, or a refusal that was really a one-off, leaves the
clone on the lease check until the user notices and unsets the key.

The classifier that tells these cases apart matches strings in git's human-readable stderr. A host that words its
rejection without the namespace or the word "rejected" falls through to a bare "git push failed with status N".

## Design Decisions (Settled)

### Three states on one key

`nitipan.<remote>.fence` is read with `git config --bool --get`, in every scope git reads. Its states:

| State | Fenced attempt | Host refuses `--atomic` or the fence ref | Records anything |
|-------|----------------|-------------------------------------------|------------------|
| unset | yes | lease-check push, with a warning | no |
| `true` | yes | error, naming the key | no |
| `false` | no | not applicable | no |

nitipan never writes the key. Unset is the default for every clone, and only the user sets the other two states. The
unset state retries the fence on every push. So a one-off refusal costs one push, and a host that gains atomic push or
starts accepting fence refs is fenced again on its next push, with nothing to clear.

An invalid value, such as `fence = maybe`, is an error that names the key and its file. Treating it as unset would
guess at the user's intent, and the guess could pick the weaker mode.

When `false` is in effect, the lease-check message names the file the value came from, read with `--show-origin`. A
`false` in `~/.gitconfig` then cannot quietly switch off fencing for every clone with a remote of that name.

### Replacing `nitipan.<remote>.atomic`

The old key named one cause, and the new key covers two. The name changes.

A clone's own `nitipan.<remote>.atomic=false` was written by nitipan. The new binary reads it when `fence` is unset,
treats it as `fence=false`, and migrates it in place: it writes `nitipan.<remote>.fence=false` with `--local` and unsets
the old key. An `atomic` entry in any other scope was written by the user. It is honored the same way, never rewritten,
and every push that uses it says to rename it. An `atomic=true` means nothing today, since no code reads it, and stays
ignored.

The migration preserves behavior. A clone on the lease check stays on the lease check under the new name. The user
can then unset the new key to get the default.

### What can cause a fallback

A fallback weakens the push from fencing to a lease check. It happens only on positive evidence that the host refused
the fence itself. Everything else keeps today's outcome.

The fenced push runs with `--porcelain`. Git's porcelain output gives one tab-separated line per ref, with a flag, the
refspec, and a summary that carries the remote's reason. The pusher judges a failed push in this order. The first match
decides:

1. A fence ref the push meant to update holds a value other than the expected one. This is a lost compare-and-swap, and
   the push retries fenced, as today. It is checked by asking the remote, as today, since hosts word a stale-info
   rejection in their own ways
2. Git refuses `--atomic` before sending any ref, because the remote did not advertise it. This is git's own
   client-side message, not a host's. It is a host refusal of atomic push
3. A fence ref's line is `!` with `[remote rejected]` and a reason of its own. A reason git gives only because another
   ref in the atomic push failed, such as `atomic push failure`, is not its own. M1 pins the exact strings git's client
   and receive-pack use for this, with a test per side. This is a host refusal of the fence ref. It holds whether the host refused only the fence ref, as with a ruleset or an update hook,
   or refused every ref, as with a pre-receive hook or a read-only repository
4. Anything else: a branch line rejected, a local `[rejected]` such as a non-fast-forward, a fatal error with no ref
   lines such as a network or auth failure, or output the pusher cannot parse. The push fails with git's output and no
   fallback

A refusal by token is not on this list, because it never reaches git. The pusher refuses before it pushes when a fence
ref holds a higher token than its own. That stays a refusal in every state of the key.

Rule 3 includes refusals of the whole push by the user's choice. Such a refusal usually refuses the fallback push too,
so it fails anyway. The cases where the fallback lands are a host that refuses only the fence ref, and a transient
refusal that clears between the two pushes. See the risk "A fallback push leaves the fence unraised".

### The fallback push

The fallback is today's lease-check push. The pusher renews every broker lease that covers a touched path and waits for
the broker. It then pushes the branches without fence refs. A lost or ended lease is a refusal. An unreachable broker is
"could not verify", exit 12, and can be retried.

The warning on stderr names the remote, the cause, and the host's reason from porcelain. It says the push got a lease
check and not fencing. It also names both keys: `fence=false` to skip the fenced attempt, and `fence=true` to make the
refusal an error.

When the fallback push also fails, the error carries both failures: the host's reason for refusing the fence, and git's
output for the fallback.

### The hook accepts a sanctioned fallback

The pre-push hook refuses an agent's push into fenced paths that carries no fence refs, unless the remote's mode is
`false`. With the key unset, it would refuse nitipan's own fallback push. The hook has to know about the fallback.

`nitipan push` sets `NITIPAN_FENCE_FALLBACK=<remote>` on the one git call that pushes the fallback. This follows
`NITIPAN_FENCE_DELETE`, which sanctions a `fence delete` push the same way. The hook accepts a push with no fence refs
into fenced paths when all of these hold:

- the variable names the remote being pushed to
- the remote's mode is unset, since `true` forbids the fallback and `false` already takes the lease-check path
- the push carries no fence refs

The hook then runs its own lease check, exactly as it does for a `false` remote today. The variable only picks the
lease-check path. It never skips the lease check.

An agent can set the variable on a plain `git push`. That gives it what one `git config` of `fence=false` gives it
today: a lease check in place of fencing. It falls under the accepted risk "Hooks are bypassable".

### Version skew

The units are two nitipan binaries that serve one clone. The hook script runs the binary path that
`setup git --install` recorded, while an agent may run a different `nitipan` from its PATH.

- New pusher, old hook: the old hook does not know `NITIPAN_FENCE_FALLBACK`. It refuses the fence-less fallback push
  into fenced paths on a remote with no record. The push fails closed, with the old hook's message. Reinstalling the
  hook fixes it
- Old pusher, new hook: the old pusher behaves as shipped. On a host without atomic push it writes `atomic=false`, which
  the new binary migrates on its next read. On a host that refuses fence refs it fails, as shipped
- New binary migrates, old binary reads: the old binary no longer sees `atomic=false`. It tries the fence, and on a
  host without atomic push it writes `atomic=false` again. On a host that refuses fence refs it fails. Both fail closed
  or loud, and neither lets an unchecked push through

No direction lets a fence-less push land without a lease check.

## Design Decisions (Open)

None.

## Risks

- **A fallback push leaves the fence unraised** — likelihood: low, impact: medium.
  A lease-check push does not write the new holder's token to the fence refs. Suppose a transient refusal sends the new
  holder's push to the fallback, and the host then accepts fence refs again. A stale holder's fenced push from another
  clone is checked against the older token and can land. NIT-005 already accepts this shape: "A stale push that lands
  before the new holder's first push succeeds". Here it means before the new holder's first fenced push. Accepted: the
  unset state retries the fence on the next push, so the window closes at the new holder's next push. The warning names
  `fence=true` for a user who wants a refusal to stop the push.
- **A host that refuses fence refs pays two pushes** — likelihood: medium, impact: low.
  In the unset state, every push to such a host uploads its pack, is refused, and uploads again without fence refs. A
  large push pays twice. Accepted: the warning names `fence=false`, which skips the fenced attempt.
- **Agents can opt out of fencing** — likelihood: low, impact: medium.
  An agent can set `fence=false` in its clone, or set `NITIPAN_FENCE_FALLBACK` on a plain `git push`. It then gets a
  lease check instead of fencing. Accepted: this is the trust model NIT-005 shipped for `atomic=false`, under the risk
  "Hooks are bypassable". The hook's own lease check still runs on every such push.
- **Renaming the key is a one-way door** — likelihood: low, impact: low.
  Clones carry `nitipan.<remote>.atomic=false` today. Mitigation: the new binary reads and migrates the old key, and
  "Version skew" shows every mix of old and new binaries fails closed or loud.
- **A host words a fence refusal as a branch refusal** — likelihood: low, impact: low.
  Rule 3 needs the fence ref's own porcelain line to carry a reason of its own. A host that blames
  the branch instead falls to rule 4 and fails the push, which is today's behavior. Mitigation: `make live-fence-hosts`
  reports the porcelain lines each host gives, so a new host's wording is seen before it matters.

## Milestones

### M1: Porcelain classifier

**Status:** NOT STARTED

- The fenced push runs with `--porcelain`, and its failure is judged by the four rules in "What can cause a fallback",
  in order
- Tests against local bare remotes produce each kind: an update hook that refuses `refs/nitipan/fence/*`, a pre-receive
  hook that declines every ref, `receive.advertiseAtomic=false`, a non-fast-forward branch, a lost compare-and-swap, and
  an unreachable remote
- Each kind maps to the outcome the rules name, and an unparseable output fails with git's output and no fallback
- A refusal by a higher token is still refused before git runs, with the key in each of its three states

### M2: Fence modes and the sanctioned fallback

**Status:** NOT STARTED

- `nitipan push` and the pre-push hook read `nitipan.<remote>.fence` with the three states in the table, and nitipan
  never writes the key
- With the key unset, a host refusal of `--atomic` or of a fence ref gets the lease-check push and the warning, and the
  next push tries the fence again
- With `true`, the same refusals fail with an error that names the key
- An invalid value fails with an error that names the key and its file
- A `false` from outside the clone's own config is named by file in the message
- A local `atomic=false` is migrated to `fence=false` on first read, and one in another scope is honored and named
- The hook accepts a fence-less push into fenced paths only under the three conditions in "The hook accepts a sanctioned
  fallback", and runs its own lease check on it
- The hook refuses a fence-less agent push when `NITIPAN_FENCE_FALLBACK` names another remote, or when the mode is
  `true`
- A test runs a new pusher against an old hook script and shows the fallback push fails closed

### M3: Docs, probe, and NIT-005 markers

**Status:** NOT STARTED

- `docs/reference/pushing.md` describes the three states in place of "Hosts without atomic push"
- `docs/operations/troubleshooting.md` replaces "The host refuses fence refs" and "The remote is marked as unable to
  take an atomic push" with entries for the fallback warning, the `true` error, the invalid value, and the migration
- `make live-fence-hosts` reports each host's porcelain lines for a refused fence ref
- NIT-005's "What enforces the token", "Token scope", and its atomic-push risk carry "Superseded in part by NIT-009; see
  Decision Log" markers, and its Decision Log records the supersession of the `atomic` key

## Decision Log

- 2026-11-14: The user chose a fallback to the lease check over failing every push on a host that refuses fence refs.
  Of the candidates, they chose a fallback on any fence rejection, including refusals of the whole push, over one
  limited to rejections attributable to the fence ref.
- 2026-11-14: The user chose to classify failures from `git push --porcelain` over widening the stderr string match, and
  over porcelain plus a probe push.
- 2026-11-14: The user chose to unify the no-atomic and refused-fence records into one, and to trust the record as
  today under "Hooks are bypassable" rather than verify it against the remote.
- 2026-11-14: The user set the key's three states: unset tries the fence and falls back, `true` always fences and fails
  hard, `false` never fences. This replaces the automatic `atomic=false` write, so nothing is recorded and recovery
  needs no rule. The user also asked for the key to be renamed, since `atomic` no longer says what it covers.
- 2026-11-14 (autonomous): The key is `nitipan.<remote>.fence`, a boolean, so its states read as the user stated them.
- 2026-11-14 (autonomous): Only a host refusal of `--atomic` or of a fence ref causes a fallback, with the four ordered
  rules in "What can cause a fallback". The user directed that correctness failures never fall back. These rules are
  the mechanism.
- 2026-11-14 (autonomous): The hook learns of the fallback through `NITIPAN_FENCE_FALLBACK`, after
  `NITIPAN_FENCE_DELETE`, and still runs its own lease check. Without a signal, the hook refuses the fallback, so the
  hook must change. The variable grants no more than `fence=false` does.
- 2026-11-14 (autonomous): An invalid value is an error, not unset. `false` names its source file. A local `atomic=false`
  is migrated, and one from another scope is honored but not rewritten, since nitipan did not write it.
- 2026-11-14 (autonomous): ADR candidate (cuts across the system): who may set a remote's fence mode, which every
  `nitipan push` and pre-push hook obeys. Declined: the user kept NIT-005's shipped trust model, so the choice adds no
  new principle. The adr-impact check found no conflict. ADR-07 holds, since neither the key nor the variable reaches
  the node. ADR-05 holds, since the key stays clone-local and is never an identity.

## References

- NIT-005: fencing tokens, fence refs, and the lease check this proposal reuses
- ADR-05, ADR-07, ADR-08: checked by adr-impact. No conflict
- `git-push(1)`, "OUTPUT", for the porcelain format
