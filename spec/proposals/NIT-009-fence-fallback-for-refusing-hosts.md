# NIT-009: Fence Fallback for Hosts That Refuse Fence Refs

**Status:** accepted
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

The pusher classifies a failed push from `git push --porcelain`, not from the wording of a host's stderr. The one
stderr line it matches is git's own client message for a remote without atomic push. Only a host
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

### Fencing is coordination, not correctness

Git guarantees the repo's data. A non-fast-forward push is rejected, and an atomic push applies all its refs or none.
The broker, leases, and fence refs sit on top of that as coordination. The broker orders grants across machines. A
lease says which session should work in a domain, and it expires when its holder goes quiet. The fence makes the
remote enforce that lease against a holder that lost it without knowing. Each layer makes collisions between agents
rarer. None of them guards against data loss.

A run with no broker has no fencing at all, and that is a supported setup. Sessions on different machines then meet
only at git's fast-forward rule. So a lease check in place of fencing is graceful degradation. It is strictly better
than the no-broker baseline, and it needs no extra machinery to be acceptable. A gap in fencing weakens coordination.
It is never a correctness defect, and it never justifies a correctness-grade fix.

The fence exists so the remote enforces the lease and the holder never needs the broker at push time. That is
NIT-005's "Receiver enforces" and its broker-free fenced push. A change here is weighed against that purpose before
any edge case is patched. A change that puts the broker in every fenced push defeats the fence. It does not harden it.

A threat that shapes the design must match how nitipan is used. One user's agents push to that user's remotes as that
user. A host that refuses fence refs refuses them for every pusher. A threat no probe has shown, and that assumes
otherwise, does not drive a design change.

A change that reverses a settled principle of an implemented proposal goes to the user as a question. No review
decides it on its own.

### nitipan honors its user's configuration

Every app must honor its user's configuration, and nitipan does too. A value the user sets, in any scope git reads, is
the user's decision. nitipan never ignores it, overrides it, or treats it as an accident to guard against. No agent
second-guesses it either.

If a value is unsafe, nitipan does not offer it. Every value nitipan does offer is legitimate, and setting it needs no
justification. A short, non-intrusive warning about a legitimate value is acceptable, such as one stderr line saying
what the value does.

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

An invalid value, such as `fence = maybe`, is an error that names the key and its file. An invalid value is not one
the user could have meant, so nitipan does not guess at it.

A value in any scope is the user's choice, and nitipan honors it the same way. A `false` in `~/.gitconfig` applies to
every clone with a remote of that name, as the user set it. Under `false`, a lease-check message says the push got a
lease check because `nitipan.<remote>.fence` is `false`. A successful push stays silent, as it does under
`atomic=false` today.

### Replacing `nitipan.<remote>.atomic`

The old key named one cause, and the new key covers two. The name changes.

When `fence` is unset, `nitipan push` and the hook both read `nitipan.<remote>.atomic=false`, in any scope, as
`fence=false`. nitipan never rewrites or unsets it. An `atomic=true` means nothing today, since no code reads it, and
stays ignored.

Who wrote the old key cannot be told. Today's pusher writes it with `--local`, and a user's hand-set `git config` writes
the same scope. Leaving it in place also keeps every mix of binaries working, since old and new binaries read it the
same way. See "Version skew".

Every push that the old key affects prints one stderr line. It says the old key acts as `fence=false`, names its file,
and says it can be renamed to `fence=false` or unset after `nitipan setup git --install`. It gives no advice on which
to pick. An older hook binary reads only the old key, which is why the reinstall comes first. The line also prints
when `fence` is set beside a leftover `atomic`, because an older hook still obeys the old key.

nitipan does not clear a record it wrote earlier. The clone stays on the lease check until the user changes it. The
legacy read also stays, and removing it later needs its own skew analysis.

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
   ref in the atomic push failed, such as `atomic push failure`, is not its own. Nor is `atomic transaction failed`, which
git puts on every line when the transaction fails to commit, whichever ref caused it. Such a push gets rule 4. M1 pins the exact strings git's client
   and receive-pack use for this, with a test per side. This is a host refusal of the fence ref. It holds when the
   host refused only the fence ref, as with a ruleset or an update hook. It also holds when the host refused every ref
   with the same reason of its own, as with a pre-receive hook or a read-only repository. An update hook that refuses
   every ref one by one leaves a collateral reason on the fence line, so that push falls to rule 4
4. Anything else: a branch line rejected, a local `[rejected]` such as a non-fast-forward, a fatal error with no ref
   lines such as a network or auth failure, or output the pusher cannot parse. The push fails with git's output and no
   fallback

A refusal by token is not on this list, because it never reaches git. In the unset and `true` states, the pusher
refuses before its fenced attempt when a fence ref holds a higher token than its own. A fallback push follows only that
check. Under `false` there is no fenced attempt, and the push relies on the lease check alone, as a remote marked
`atomic=false` does today.

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
- New pusher, old hook, with `atomic=false`: both binaries read the old key as the lease-check mode. The new pusher's
  fence-less push passes the old hook's lease-check branch, as it does today
- New pusher, old hook, with `fence=false` set by the user: the old hook does not read it, takes its fenced branch, and
  refuses the fence-less push. It fails closed. The notice says to reinstall the hook before renaming
- Old pusher, new hook: the old pusher behaves as shipped. On a host without atomic push it writes `atomic=false`, and
  the new hook honors it. On a host that refuses fence refs it fails, as shipped. Under a user's `fence=false`, the old
  pusher's fenced push meets a new hook that refuses fence refs, and fails closed
- Old binary after new: the new binary writes nothing, so the old binary sees the config as it left it

No direction lets a fence-less push land without a lease check.

## Design Decisions (Open)

None.

## Risks

- **A fallback push leaves the fence unraised** — likelihood: low, impact: medium.
  A lease-check push does not write the new holder's token to the fence refs. Suppose a transient refusal sends the new
  holder's push to the fallback, and the host then accepts fence refs again. A stale holder's fenced push from another
  clone is checked against the older token and can land. NIT-005 already accepts this shape: "A stale push that lands
  before the new holder's first push succeeds". Here it means before the new holder's first fenced push. The unset
  state retries the fence on the next push, so after a transient refusal the window closes at the new holder's next
  push. A host that fast-forwards existing fence refs but refuses new ones keeps the window open. Every push that also
  needs a new fence falls back, so an existing key's fence stays at its old token for as long as the rule stands.
  Accepted: fencing is coordination, and a lease check in place of fencing is graceful degradation. The warning names
  `fence=true` for a user who wants a refusal to stop the push.
- **A host that refuses fence refs pays two pushes** — likelihood: medium, impact: low.
  In the unset state, every push to such a host uploads its pack, is refused, and uploads again without fence refs. A
  large push pays twice. Accepted: the warning names `fence=false`, which skips the fenced attempt.
- **Agents can opt out of fencing** — likelihood: low, impact: medium.
  An agent can set `fence=false` in its clone, or set `NITIPAN_FENCE_FALLBACK` on a plain `git push`. It then gets a
  lease check instead of fencing. Accepted: this is the trust model NIT-005 shipped for `atomic=false`, under the risk
  "Hooks are bypassable". The hook's own lease check still runs on every such push.
- **Renaming the key is a one-way door** — likelihood: low, impact: low.
  Clones carry `nitipan.<remote>.atomic=false` today. Mitigation: the new binary reads the old key and never rewrites
  it, and
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
- A refusal by a higher token is still refused before git runs, with the key unset or `true`. Under `false` the push
  gets the lease check alone
- `atomic push failure` and `atomic transaction failed` both count as collateral reasons. A test holds a lock on the
  fence ref and requires rule 4
- When rule 1 cannot re-read the remote, the push fails by rule 4, not rule 3
- The classifier reads porcelain from stdout, and the user still sees git's per-ref result
- The fenced push sends branch refspecs before fence refspecs. A test with an update hook that refuses every ref
  reaches rule 4
- A fence ref that fails at transaction commit, such as on a directory/file conflict, gets rule 4, and the user sees
  the remote's `error:` line

### M2: Fence modes and the sanctioned fallback

**Status:** NOT STARTED

- `nitipan push` and the pre-push hook read `nitipan.<remote>.fence` with the three states in the table, and nitipan
  never writes the key
- With the key unset, a host refusal of `--atomic` or of a fence ref gets the lease-check push and the warning, and the
  next push tries the fence again
- With `true`, the same refusals fail with an error that names the key
- An invalid value fails with an error that names the key and its file
- A `false` in any scope, `~/.gitconfig` included, is honored the same way. Lease-check messages under `false` name the
  key, and a successful push prints nothing about it
- With `fence` unset, an `atomic=false` in any scope is read as `fence=false` by both the pusher and the hook, and is
  never rewritten or unset. Its notice names its file and says to run `nitipan setup git --install` before renaming
  or unsetting it
- With `fence` set beside a leftover `atomic`, `fence` wins and the notice still fires
- The hook accepts a fence-less push into fenced paths only under the three conditions in "The hook accepts a sanctioned
  fallback", and runs its own lease check on it
- The hook refuses a fence-less agent push when `NITIPAN_FENCE_FALLBACK` names another remote, or when the mode is
  `true`
- A test runs a new pusher against an old hook script and shows the fallback push fails closed
- An invalid value exits non-zero in both the pusher and the hook, and the message names the file
- Lease-check messages and `push --help` name the actual cause, not only "cannot take an atomic push"
- When a whole-push refusal is followed by a lease check that cannot run, the error leads with the host's reason
- Only the fallback git call carries `NITIPAN_FENCE_FALLBACK`, even when the caller's environment already exports it
- When the hook refuses the fallback push, the pusher's error names `nitipan setup git --install` as a likely cause
- The e2e tests that assert today's `atomic=false` write assert that nothing is written
- A test pins what an invalid `atomic` value does, in both the pusher and the hook
- When a user's `fence=false` meets an old hook that refuses the fence-less push, the pusher's error names
  `nitipan setup git --install`

### M3: Docs, probe, and NIT-005 markers

**Status:** NOT STARTED

- `docs/reference/pushing.md` describes the three states in place of "Hosts without atomic push"
- `docs/operations/troubleshooting.md` replaces "The host refuses fence refs" and "The remote is marked as unable to
  take an atomic push" with entries for the fallback warning, the `true` error, the invalid value, and the legacy `atomic` key
- `docs/architecture.md` says that git guarantees the repo's data, and that the broker, leases, and fence refs are
  coordination layers on top of it. It says a run with no broker has no fencing, and that a lease check in place of
  fencing is graceful degradation
- `docs/architecture.md` says that nitipan honors its user's configuration in every scope, that it does not offer an
  unsafe value, and that any warning about a legitimate value is short and non-intrusive
- `make live-fence-hosts` pushes with `--porcelain` and reports each host's porcelain lines for a refused fence ref
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
  Wrong, and superseded on 2026-11-14. The drafting session decided without asking that a `false` names its source
  file, so a value in `~/.gitconfig` could not "quietly switch off fencing". That treated the user's own configuration
  as a hazard. nitipan honors its user's configuration in every scope. See "nitipan honors its user's configuration".
- 2026-11-14 (autonomous): ADR candidate (cuts across the system): who may set a remote's fence mode, which every
  `nitipan push` and pre-push hook obeys. Declined: the user kept NIT-005's shipped trust model, so the choice adds no
  new principle. The adr-impact check found no conflict. ADR-07 holds, since neither the key nor the variable reaches
  the node. ADR-05 holds, since the key stays clone-local and is never an identity.
- 2026-11-14: The user rejected an autonomous review of this proposal. The commit "spec: record the rejected
  autonomous review of NIT-009" keeps it, and its revert gives the reasons. The review had every fenced push renew its
  leases with the broker. It did that to close a hole on hosts that refuse fence refs from some pushers only. That
  reversed NIT-005's broker-free fenced push, without asking, for a threat no probe showed. The review filed a
  coordination gap under correctness. It patched edge cases before weighing each component's purpose. It grew the
  design instead of settling it. The user set "Fencing is coordination, not correctness" as a settled decision, and
  asked for it in nitipan's own docs, so other agents learn it. M3 carries that to `docs/architecture.md`.
- 2026-11-14: Review start: 3 milestones, 7 settled decisions, 5 risks, no `**Live after:**` lines or row gates; no
  out-of-scope statements. Baseline commit "spec: settle that fencing in NIT-009 is coordination, not correctness".
  The user asked for a new autonomous review after rejecting the last one, with no open questions left.
- 2026-11-14 (autonomous): Corrected text the coherence review found overclaimed, and added no mechanism. The higher-token
  refusal before git runs holds in the unset and `true` states. Under `false` the push relies on the lease check alone,
  as `atomic=false` does today. The Summary now says rule 2 matches git's own client message on stderr. Rule 3 names
  `atomic transaction failed` as collateral, and says when an every-ref refusal counts as the fence's own. The risk "A
  fallback push leaves the fence unraised" now covers a host that refuses only new fence refs, and stays accepted.
- 2026-11-14 (autonomous): Routed the coherence review's implementation findings to milestone checks. M1 got the
  collateral strings, rule 1 failing to rule 4 when it cannot re-read the remote, porcelain read from stdout, and the
  narrowed higher-token check. M2 got the invalid-value exit, lease-check messages that name their cause, a host's
  reason leading the error when the lease check cannot run, the variable on the fallback call only, a reinstall hint
  when the hook refuses the fallback, and the e2e tests that assert the old write. M3 got porcelain in the probe.
- 2026-11-14 (autonomous): Reopened the local migration of `atomic=false` as a weak decision. An `atomic=false` in any
  scope is now read as `fence=false`, by the pusher and the hook, and never rewritten. The migration locked out a clone
  that works today when its hook ran an older binary. It also contradicted "nitipan never writes the key". Its premise,
  that nitipan wrote every local value, was false. Ignoring the old key or dropping a local one breaks old hooks too. A
  rename at `setup git --install` was weighed and not taken: it adds a config write to setup, and read-only honoring
  already works with every mix of binaries. Converged in 1 pass. Rewrote "Version skew" to match.
- 2026-11-14 (autonomous): The proposal loop converged on its second pass, with no design-level finding. Rule 3 now says
  `atomic transaction failed` covers a commit failure of any ref, the fence ref included, and gets rule 4. Routed to M1:
  branch refspecs before fence refspecs, and a fence ref failing at commit. Routed to M2: an invalid `atomic` value, and
  the reinstall hint when a user's `fence=false` meets an old hook.
- 2026-11-14 (autonomous): Risk review. Every risk carries a likelihood, an impact, and a mitigation or an acceptance.
  None is high likelihood and high impact. The one-way door is the key rename, listed and defused by read-only
  honoring. NIT-005 is implemented, so no in-flight design can shift under a mitigation. Its marker edits fall under
  the frozen-proposal allowances.
- 2026-11-14: Review gate: reopened naming the origin of a `false` value. Kept everything else, including the earlier
  run's autonomous entries.
- 2026-11-14: The user set that nitipan must respect a value in `~/.gitconfig`, like a value in any other scope. Messages
  under `false` name the key, not the file, and no scope is treated as suspect. This replaces the autonomous decision
  that a `false` names its source file so a global value "cannot quietly switch off fencing". A global value is the
  user's choice, not an accident to guard against. A notice on every push under `false` was weighed and not taken.
- 2026-11-14: The user set "nitipan honors its user's configuration" as a settled decision. Every app must honor its
  user's configuration, and no agent may treat the user as unable to choose. An unsafe value is not offered. A
  non-intrusive warning about a legitimate value is acceptable. The drafting session's autonomous entry that a `false`
  names its source file is marked wrong. M3 carries the principle to `docs/architecture.md`.
- 2026-11-14: The user approved three rewordings from an audit of the drafting session's decisions. The invalid-value
  rationale no longer warns of a weaker mode. The legacy notice is one stderr line with no advice on whether to rename
  or unset. The leftover record is stated as a fact, not a complaint. The audit found no distrust of the user's
  configuration in the key name, the four fallback rules, or `NITIPAN_FENCE_FALLBACK`.
- 2026-11-14: ADR candidate (cuts across the system): the broker, leases, and fences are coordination, not correctness,
  and the fenced push stays receiver-enforced and broker-free. Written as ADR-09.
- 2026-11-14: ADR candidate (cuts across the system): nitipan honors its user's configuration in every scope. Declined:
  honoring a user's configuration is the default for every app, not something particular to nitipan.
- 2026-11-14: The user accepted the proposal. ADR-09 stays proposed, since the user accepted only the proposal.

## References

- NIT-005: fencing tokens, fence refs, and the lease check this proposal reuses
- ADR-05, ADR-07, ADR-08: checked by adr-impact. No conflict
- ADR-09: fencing is coordination, not correctness
- `git-push(1)`, "OUTPUT", for the porcelain format
