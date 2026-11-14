# NIT-009: Fence Fallback for Hosts That Refuse Fence Refs

**Status:** designing
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
- NIT-005 — the M3 bullet that has the clone record a host without atomic push. M3 marks it
- NIT-005 — "What enforces the token", in the bullet that a protected push carries a compare-and-swap, and in "What this
  does not fence", the window before the new holder's first push. Fenced pushes now also renew their leases with the
  broker, which narrows that window. M3 marks both
- NIT-004 — the risk "The broker is a single point of failure". During an outage, a lease holder's fenced push now
  exits 12, where it landed before. M3 adds a "Superseded in part by NIT-009" marker to it

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

Every lease-check push still reads the remote's fence refs and refuses a higher token. Every fenced push also renews
its covering leases with the broker. A fallback leaves the fence unraised, so the token compare alone could let a
stale holder through.

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

When `fence` is unset, the new binary reads `nitipan.<remote>.atomic=false` in any scope as `fence=false`. It never
rewrites or unsets the old key. An `atomic=true` means nothing today, since no code reads it, and stays ignored.

The old key cannot be told apart by who wrote it. Today's pusher writes it with `--local` after a refused `--atomic`.
Users also set it by hand, since it is the only way out on a host that refuses fence refs, and a plain `git config`
writes the same scope. Rewriting it would turn a stale automatic record into what reads as the user's own `false`.

Leaving it in place also keeps every mix of binaries working. Old and new binaries read the old key the same way, so
a clone that pushes today keeps pushing. See "Version skew".

Every push that the old key puts on the lease check says so, and names the file it came from. The message says that a
record nitipan wrote after one refused push is best unset, which returns the clone to the default. It also says to
rename it to `fence=false` to keep the lease check. Both steps tell the user to run `nitipan setup git --install` first,
since an older hook binary reads only the old key.

A clone with a stale automatic record stays on the lease check until the user acts on that message. This is the
defect the Motivation names, made loud. The design does not clear it on its own.

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
   and receive-pack use for this, with a test per side. This is a host refusal of the fence ref. It holds whether the
   host refused only the fence ref, as with a ruleset or an update hook, or refused every ref, as with a pre-receive
   hook or a read-only repository
4. Anything else: a branch line rejected, a local `[rejected]` such as a non-fast-forward, a fatal error with no ref
   lines such as a network or auth failure, or output the pusher cannot parse. The push fails with git's output and no
   fallback

A refusal by token is not on this list, because it never reaches git. The pusher refuses before it pushes when a fence
ref holds a higher token than its own. That stays a refusal in every state of the key. See "The lease-check push reads
the fences".

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

### The lease-check push reads the fences

Today's lease-check push drops the remote's fence refs before it plans. That was safe under NIT-005, since a remote
marked `atomic=false` could not hold fence refs. Under this proposal the lease check also runs on hosts that do hold
them: after a transient refusal, on a host that refuses only some pushers, and under a `false` the user set on a host
with history. Dropping the fences there would skip the higher-token refusal and the broker query for a fenced path the
pusher holds no lease on. A push from a session holding no lease could then land in another session's fenced path.

So every lease-check push reads the advertised fence refs, in `nitipan push` and in the hook alike. It applies every
check the fenced push applies before git runs: an undecodable fence name, a higher token, an unusable fence, and the
broker query for a fenced path no lease of the pusher covers. Only the fence write is skipped. The hook still refuses a
lease-check push that carries fence refs. Reading a token needs the fence commit, so the lease-check push fetches the
fence refs first, as the fenced push does. The hook's fence sync already requires the clone to match the remote.

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

New pusher, old hook:

- No record, and the host refuses the fence: the old hook does not know `NITIPAN_FENCE_FALLBACK`. It refuses the
  fence-less fallback push into fenced paths. The push fails closed, with the old hook's message. Reinstalling the hook
  fixes it
- `atomic=false`: both binaries read it as the lease-check mode. The new pusher sends a lease-check push, and the old
  hook takes its lease-check branch. The push works as it does today
- `fence=false` set by the user: the old hook does not read it and takes its fenced branch, which refuses the
  fence-less push. It fails closed. The message that suggests `fence=false` says to reinstall the hook first
- A fenced push: the new pusher renews its leases, and the old hook checks the fence refs as shipped. It works

Old pusher, new hook:

- No record, on a host without atomic push: the old pusher's fenced attempt passes the new hook's fenced path. Git
  then refuses `--atomic`, and the old pusher writes `atomic=false` and sends its lease-check push. The new hook reads
  the old key as `false` and takes the lease-check path. It works
- `atomic=false`: the old pusher sends a lease-check push, and the new hook takes the lease-check path. It works
- `fence=false` set by the user: the old pusher does not read it and sends a fenced push. The new hook refuses fence
  refs under `false`. It fails closed
- A host that refuses fence refs: the old pusher fails, as shipped
- A fenced push: the new hook renews the leases, which the old pusher did not. A stale holder is refused at the hook

Stale holder on old binaries: a clone whose pusher and hook both predate this proposal does not renew on a fenced
push. Its stale push still passes on the token compare alone, as it does today. The renewal protects only clones that
run the new binary.

No direction lets a fence-less push land without a lease check. Every direction that fails, fails closed with a
message.

## Design Decisions (Open)

### Should anything close the unraised-fence hole on hosts that refuse fence refs per pusher?

Reopened after the review gate. The text below is the autonomous answer, kept so the options can be weighed against it.
It is not settled.

The hole predates this proposal. Today a pusher that a host refuses hand-sets `atomic=false` and gets a lease-check
push, which leaves the fence unraised. No probed host refuses fence refs per pusher. Candidates:

- No addition: keep the fallback, and restate the risk "A fallback push leaves the fence unraised" as unbounded on
  hosts that refuse fence refs per pusher. File the NIT-005 gap as a bug
- Refuse the fallback with a rule-3 refusal when a touched path's fence ref already exists. This narrows the user's
  choice of a fallback on any fence rejection. It misses a remote with no fence yet, and its error leads to
  `fence=false`
- The autonomous answer, below: every fenced push also renews its leases with the broker. This is an addition. It
  moves a check to the holder's side, against NIT-005's "Receiver enforces", and puts the broker on every fenced push

#### Autonomous answer: fenced pushes renew the leases they rely on

A fallback leaves the fence where it was. When the refusal is transient, the new holder's next fenced push raises it.
Some hosts refuse refs per pusher, such as gitolite refex rules or a host ruleset with bypass actors. There, the new
holder may never raise the fence. Suppose pusher A wrote the fence at token 5 and now holds a stale lease at 6. Pusher
B holds the lease at 7, but B's credentials cannot write fence refs. Every push B makes falls back. A's fenced push
compares 6 with 5 and lands, on every attempt. Today's fenced push never asks the broker about the pusher's own keys,
so nothing stops it.

So every fenced push also renews the pusher's covering broker leases before git runs, as the lease-check push does. A
lost or ended lease is a refusal. The hook's fenced path renews them too, so a plain `git push` that carries fence refs
at a stale token is refused there as well. The token compare and the compare-and-swap stay as they are. The renewal
adds a check beside them and replaces none of them.

This adds a broker call to every fenced push into a broker-leased path. NIT-005 kept that path free of the broker.
While the broker is unreachable, such a push now exits 12, "could not verify", and can be retried. It lands today.
This widens NIT-004's risk "The broker is a single point of failure" from tokenless agents to every holder's push.

The renewal also narrows NIT-005's accepted window, "A stale push that lands before the new holder's first push
succeeds". A stale holder running this binary is refused by the broker even before the new holder's first push. What
remains is the window between the renewal and the push landing, and a stale clone that runs an older binary or
`--no-verify`. That window is the push's own duration, since a `break` right after the renewal lets another session
take the lease while the push uploads.


## Risks

- **A fallback push leaves the fence unraised** — likelihood: low, impact: medium.
  A lease-check push does not write the new holder's token to the fence refs. The fence can stay low for one push after
  a transient refusal. It can stay low for good on a host that refuses fence refs from some pushers only. A stale
  holder's fenced push is then checked against the older token, and the token compare alone would let it land.
  Mitigation: every fenced push renews its covering leases with the broker, so a stale holder that runs this binary is
  refused whatever the fence holds. See "Fenced pushes renew the leases they rely on". What remains is a stale clone on
  an older binary or using `--no-verify`, and the window between the renewal and the push landing, which lasts as long as
  the push. The warning names `fence=true` for a user who wants a refusal to stop the push.
- **Fenced pushes need the broker** — likelihood: medium, impact: medium.
  The renewal makes every fenced push into a broker-leased path depend on reaching the broker. During an outage such a
  push exits 12 where it lands today. So does a lease-check push from a session with no lease into a path the remote
  holds a fence ref for, which now runs the broker query. This widens NIT-004's risk "The broker is a single point of
  failure". Accepted:
  the exit is "could not verify", it can be retried, and the lease-check push already has the same dependency. A push
  the broker cannot vouch for is the push fencing exists to stop.
- **A host that refuses fence refs pays two pushes** — likelihood: medium, impact: low.
  In the unset state, every push to such a host uploads its pack, is refused, and uploads again without fence refs. A
  large push pays twice. The pre-push hook also runs twice, with its broker query each time. Git runs the hook before it
  checks for atomic push, so a host without atomic push pays the second hook run but not the second upload. Accepted:
  the warning names `fence=false`, which skips the fenced attempt.
- **Agents can opt out of fencing** — likelihood: low, impact: medium.
  An agent can set `fence=false` in its clone, or set `NITIPAN_FENCE_FALLBACK` on a plain `git push`. It then gets a
  lease check instead of fencing. Accepted: this is the trust model NIT-005 shipped for `atomic=false`, under the risk
  "Hooks are bypassable". The hook's own lease check still runs on every such push.
- **Renaming the key is a one-way door** — likelihood: low, impact: low.
  Clones carry `nitipan.<remote>.atomic=false` today. Mitigation: the new binary reads the old key and never rewrites
  it, so old and new binaries agree on it. "Version skew" shows every mix of binaries either works or fails closed
  with a message.
- **A host words a fence refusal as a branch refusal** — likelihood: low, impact: low.
  Rule 3 needs the fence ref's own porcelain line to carry a reason of its own. A host that blames the branch instead
  falls to rule 4 and fails the push, which is today's behavior. Mitigation: `make live-fence-hosts`
  reports the porcelain lines each host gives, so a new host's wording is seen before it matters.
- **A host words a collateral failure as the fence ref's own** — likelihood: low, impact: medium.
  This is the unsafe direction of the risk above. In an atomic push, a ref that fails only because another ref failed
  carries a collateral reason. A host that gives the fence line a reason of its own for a branch failure or for
  contention matches rule 3, and the push falls back to the lease check when it should have failed. The lease check
  still runs, with the fences read, so no unchecked push lands. Mitigation: M1 pins the collateral strings git uses,
  and `make live-fence-hosts` records what each host says for an atomic push whose branch ref fails.

## Milestones

### M1: Porcelain classifier

**Status:** NOT STARTED

- The fenced push runs with `--porcelain`, and its failure is judged by the four rules in "What can cause a fallback",
  in order
- Tests against local bare remotes produce each kind: an update hook that refuses `refs/nitipan/fence/*`, a pre-receive
  hook that declines every ref, `receive.advertiseAtomic=false`, a non-fast-forward branch, a lost compare-and-swap, and
  an unreachable remote
- Each kind maps to the outcome the rules name, and an unparseable output fails with git's output and no fallback
- A branch ref locked on the remote, which makes every line read `[remote rejected] (atomic transaction failed)`, is
  judged by rule 4 and never falls back
- When rule 1 cannot ask the remote, the push fails by rule 4 and never falls back
- Rule 2 matches only git's own `fatal:` line with no ref lines on stdout. A `remote:` sideband line that says the
  same words does not match
- The pusher captures porcelain stdout, never reads `Done` as success, and shows the user a readable result and not
  raw porcelain
- A refusal by a higher token is still refused before git runs, with the key in each of its three states, on the
  fenced push and on every lease-check push, in `nitipan push` and in the hook
- A lease-check push from a session holding no lease, into a path whose fence ref the remote holds, runs the broker
  query and is refused when another session holds the lease
- A lease-check push refuses only for an undecodable fence name, a higher token, an unusable or higher-major fence, and
  the broker query. A key the plan cannot name, or a lease with no token, refuses it no more than it does today

### M2: Fence modes, the sanctioned fallback, and lease renewal

**Status:** NOT STARTED

- `nitipan push` and the pre-push hook read `nitipan.<remote>.fence` with the three states in the table, and nitipan
  never writes the key
- With the key unset, a host refusal of `--atomic` or of a fence ref gets the lease-check push and the warning, and the
  next push tries the fence again
- With `true`, the same refusals fail with an error that names the key
- An invalid value fails with an error that names the key and its file
- A `false` from outside the clone's own config is named by file in the message. A value from `git -c` or the
  environment, which `--show-origin` reports as `command line:`, is named as such
- With `fence` set to either value, the old key is ignored, so `fence=true` wins over a leftover `atomic=false`
- With `fence` unset, an `atomic=false` in any scope is read as `fence=false` and never rewritten or unset. Its message
  names its file,
  says to unset a record nitipan wrote, and says to run `nitipan setup git --install` before renaming or unsetting it
- Every fenced push into a broker-leased path renews the covering leases before git runs, in `nitipan push` and in the
  hook's fenced path. A stale holder whose token still beats the stored fence is refused, and an unreachable broker
  exits 12
- The hook accepts a fence-less push into fenced paths only under the three conditions in "The hook accepts a sanctioned
  fallback", and runs its own lease check on it
- The hook refuses a fence-less agent push when `NITIPAN_FENCE_FALLBACK` names another remote, or when the mode is
  `true`
- A push to a remote given by URL, or by a name git config cannot key, reads no mode and acts as unset. The fallback
  warning says the mode binds by remote name
- Every lease-check message names its real cause: no atomic push, a refused fence ref, or `false` and its file. No
  message or `push --help` text still says only "cannot take an atomic push"
- A test runs a hook binary built from the commit before this proposal against the new pusher. The fallback push with
  no record fails closed, and a push under `atomic=false` works
- With `fence=true` beside a leftover `atomic=false`, a new pusher against an old hook binary and an old pusher against
  a new hook binary each fail closed with a message

### M3: Docs, probe, and NIT-004 and NIT-005 markers

**Status:** NOT STARTED

- `docs/reference/pushing.md` describes the three states in place of "Hosts without atomic push"
- `docs/operations/troubleshooting.md` replaces "The host refuses fence refs" and "The remote is marked as unable to
  take an atomic push" with entries for the fallback warning, the `true` error, the invalid value, and the migration
- `docs/reference/cli.md` and `docs/operations/broker.md` describe the `fence` key. They mention the `atomic` key only
  as a legacy record, and point to the troubleshooting entry for it
- `make live-fence-hosts` reports each host's porcelain lines for a refused fence ref, and for an atomic push whose
  branch ref fails
- NIT-005's "What enforces the token", "Token scope", its atomic-push risk, and the M3 bullet that says the clone
  records a host without atomic push carry "Superseded in part by NIT-009; see Decision Log" markers. So do its
  compare-and-swap bullet and the window in "What this does not fence". Its Decision Log records the supersession of
  the `atomic` key and of the broker-free fenced push
- NIT-004's risk "The broker is a single point of failure" carries the same marker, and its Decision Log records it
- `docs/reference/pushing.md` says a fenced push renews its leases and exits 12 when the broker is unreachable

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
- 2026-11-14: Review start: 3 milestones, 6 settled decisions, 5 risks, no `**Live after:**` lines or row gates; no
  out-of-scope statements. Baseline commit "spec: propose a three-state fence mode for hosts that refuse fence refs".
  The user asked for an autonomous review that resumes the earlier one, with no open questions left.
- 2026-11-14 (autonomous): Every lease-check push reads the remote's fence refs and applies the higher-token refusal
  and the broker query, skipping only the fence write. The coherence review found that today's lease-check path drops
  the fences, so the claim that a token refusal holds in every state of the key was false once the lease check runs on
  hosts that hold fence refs. Added "The lease-check push reads the fences".
- 2026-11-14 (autonomous): Routed the coherence review's implementation findings to milestone checks. M1 got the
  collateral `atomic transaction failed` string, rule 1 failing closed when the remote cannot be asked, rule 2 anchored
  on git's own `fatal:` line, and porcelain stdout capture. M2 got pushes by URL, lease-check messages that name their
  cause, and a skew test against a hook binary from before this proposal. M3 got the remaining docs and NIT-005's M3
  bullet. Added the risk "A host words a collateral failure as the fence ref's own", and the double hook run to "A host
  that refuses fence refs pays two pushes".
- 2026-11-14 (autonomous): Reopened the migration of a local `atomic=false`. An `atomic=false` in any scope is now
  read as `fence=false` and never rewritten or unset. The coherence review found the migration broke two settled
  claims. A clone working today locks out under an older hook binary, since that binary reads only the old key. The
  rewrite also contradicted "nitipan never writes the key". Its premise that nitipan wrote every local value was false,
  since a hand-set `git config` writes the same scope. Ignoring the old key, or dropping a local one, also locks out
  some mixes of binaries. A setup-time migration was weighed and not taken: it adds a step to `setup git --install`,
  and read-only honoring already solves the problem. Converged in 1 pass. Rewrote "Version skew" for every mix.
- 2026-11-14 (autonomous, addition): Every fenced push into a broker-leased path renews its covering leases with the
  broker, in `nitipan push` and in the hook. This brings in a broker dependency on the fenced push, which NIT-005 kept
  broker-free. It widens NIT-004's single-point-of-failure risk, adds NIT-004 and NIT-005 markers to M3, and adds the
  risk "Fenced pushes need the broker". The coherence review found that a host refusing fence refs per pusher leaves
  the fence unraised for good, so a stale holder's fenced push lands on every attempt. Refusing the fallback when a
  fence exists was weighed and rejected. It misses a remote with no fence yet, and its error leads to `fence=false`,
  which reopens the hole. It also narrows the user's choice of a fallback on any fence rejection. Accepting the risk
  would leave fencing's core guarantee failing without bound. Converged in 1 pass. My first pick was the
  refuse-on-existing-fence option. The critic's counterexamples changed it.
- 2026-11-14 (autonomous): The proposal loop converged on its second pass. The re-check found no design-level defect.
  It corrected the renewal's residual window to the push's own duration, since a `break` right after the renewal can
  hand the lease on mid-upload. It also found that reading the fences gives a lease-check push from a session with no
  lease a broker query. "Fenced pushes need the broker" now names that case. Routed to M1: the lease-check push refuses
  no new plan kinds. Routed to M2: a set `fence` wins over the old key, a `git -c` origin is named, and `fence=true`
  beside `atomic=false` fails closed in both skew directions. Routed to M3: the docs keep the old key as a legacy
  record.
- 2026-11-14 (autonomous): Risk review. Every risk carries a likelihood, an impact, and a mitigation or an acceptance.
  None is high likelihood and high impact. The one-way door is the key rename, which is listed and defused by
  read-only honoring. Both dependencies, NIT-004 and NIT-005, are implemented, so no in-flight design can shift under
  a mitigation. Their marker edits fall under the frozen-proposal allowances.
- 2026-11-14: Review gate: kept all. This covers this run's decisions and the earlier run's autonomous entries.
- 2026-11-14: Reopened the renewal on every fenced push after the gate. The user rejected it. The hole it closes
  predates this proposal, no probed host shows it, and the renewal undercuts NIT-005's "Receiver enforces".

## References

- NIT-005: fencing tokens, fence refs, and the lease check this proposal reuses
- ADR-05, ADR-07, ADR-08: checked by adr-impact. No conflict
- `git-push(1)`, "OUTPUT", for the porcelain format
