# NIT-005: Fencing Tokens

**Status:** accepted
**Created:** 2026-10-18
**Updated:** 2026-10-24

## Dependencies

- NIT-004

## Impacts

- NIT-001 — "Breaking a live lease". Tombstones are a broken holder's only signal until fencing exists. This proposal
  is that fencing. "The fence mark" narrows its "a tombstone never blocks a claim" for one holder, so that section
  takes a "Superseded in part by NIT-005" marker when this proposal ships
- NIT-003 — "Fencing" there fixes the outcomes this proposal's "Token scope" and "Local fencing for NIT-001" must keep
  for sub-leases. A single high-water mark per name or per repo cannot keep them. Its revisit hook was audited and
  closed on 2026-10-24, when this proposal was accepted
- NIT-004 — this proposal relies on "Lease term and renewal" (revival keeps the token, and the synchronous renew),
  "Name syntax for wider reach" (root sets and repo-relative paths name fence keys), "How sessions reach the broker"
  (the token arrives in the grant reply), and "Discovery queries" (refusals and push rule 6 ask the broker who holds a
  key). A change to any of them reopens the matching part of this proposal. "Counter durability" also needs NIT-004's
  renewal of a lease the broker does not know, as after a restore, to return lost and never revive it. "Tokenless
  pushers and fence cleanup" changes NIT-004's risk "The broker is a single point of failure": during an outage, an
  agent's push to a fenced path outside its broker leases is refused, not only its claims
- NIT-008 — "Token scope" leaves the token reissue on a sub-lease's end to the first proposal that creates broker
  sub-leases. If NIT-008's "Sub-leases at machine reach" brings sub-leases to the broker, it owns that rule under
  NIT-003's outcomes
- NIT-008 — "Local fencing for NIT-001" and "Refused mid-task". A recall past its deadline ends the child for the hook,
  which then refuses the ex-sub-lessee with its own refusal kind and passes the parent. Before the deadline the hook
  warns on stderr. NIT-008 M2 builds this. Amended 2026-10-24; see Decision Log

## Summary

The broker issues a fencing token with every lease grant. A token is a number that increases with each grant. A push
that touches a broker lease's keys compares the holder's token with fence refs on the remote, and updates them in the
same atomic push. A higher token there refuses the push, and git's compare-and-swap on the fence refs makes the check
and the write one step.

On one machine, a local git hook refuses a ref update that touches keys the caller no longer owns, such as after
`break --force`. The state directory is the authority there, so no token is needed.

## Motivation

An expiring lease cannot stop a partitioned holder. The holder may not know its lease expired. It can still commit or
push after another session takes the lease, and the two sessions' work collides.

NIT-001 has the same gap on one machine. `break --force` removes a live holder's lease, and the holder learns of it
only on its next nitipan call. Anything it does before that call is unchecked.

## Design Decisions (Settled)

These come from the roadmap the user approved on 2026-10-18, recorded in the research write-up.

### Token per grant

The broker increases the token with every grant. A newer holder of a name always carries a higher token than any
earlier holder of that name.

### Receiver enforces

The receiving side of a protected action checks the token. The holder does not police itself. A holder that has lost
contact cannot know it is stale, so only the receiver can refuse it.

### What enforces the token

The question was which component refuses a stale commit or push. The candidates were a local git hook before a commit,
a pre-receive hook on the remote, and both where each is available.

Decision (autonomous): enforce at the real receiver for each reach. What collides differs by reach, so the receiver
differs too.

- A `machine` lease guards a checkout that sessions on one machine share. The collision is a ref update in that
  checkout. A local `reference-transaction` hook checks the update against the state directory in its `prepared` state
  and aborts the transaction when the caller no longer owns what it touches. See "Local fencing for NIT-001". The state
  directory is the authority, so the hook never calls the broker. It filters to `refs/heads/*` of the leased
  checkout, works out which worktree it runs in, and stays off the network
- A broker lease guards a domain that several machines clone. A partitioned machine commits into its own clone, which
  collides with nobody. The collision is the push. Commits are not checked for broker leases, so offline work keeps
  going
- A protected push carries a compare-and-swap on a fence ref in the same `git push --atomic`. The fence ref holds the
  highest token pushed for its key. The pusher fetches it and refuses when the stored token is higher than its own. It
  then pushes the fence with its own token and the fetched value as the expected old value. receive-pack verifies every
  ref's old value on the server and refuses the whole push on a mismatch. No hook runs on the remote, so this works on
  remotes the user does not control
- Fence ref names cannot be ADR-03 keys. Those are absolute local paths that differ between machines, and many are not
  valid ref names, such as `.github`. Fence refs live on the repo's own remote, so a fence ref is named by a root
  commit and an encoding of the key's repo-relative path, `refs/nitipan/fence/<root-sha>/<encoded-path>`. The root
  keeps apart histories that NIT-004 never lets meet, such as `main` and an orphan `gh-pages`. The encoding flattens
  nested keys into one component, since git cannot create `refs/x/src/a` while `refs/x/src` exists. Mapping a broker
  lease's key to a repo-relative path depends on NIT-004's "Name syntax for wider reach". Amended 2026-10-24; see
  Decision Log
- A host that does not support atomic push cannot hold the fence and the branch update in one transaction. There, a
  fail-closed pre-push check renews the lease with the broker before the push. That is a lease check, not fencing. Its
  window is bounded by the lease term, which NIT-004 has not settled. It is labeled as such wherever it applies

What this does not fence:

- Only pushers that cooperate are fenced. A plain `git push` that bypasses nitipan never touches the fence ref, so
  receive-pack has nothing to check. A pre-push hook refuses a push of a fenced branch that carries no fence update.
  `--no-verify` still skips it, under the accepted risk "Hooks are bypassable"
- A stale push that lands before the new holder's first push succeeds, since the receiver has not yet seen the higher
  token. The new holder then integrates that work under git's fast-forward rule. This is standard fencing. Bumping the
  fence eagerly at grant time would need `acquire` to reach the remote
- Refs are fenced, not files. A stale holder on one machine can still write into the new holder's working tree and
  index. That is out of scope
- A small window remains on one machine between the hook's check and the ref update, against a `break --force` that
  runs at the same moment. It is accepted

This squares with "Receiver enforces". The remote's receive-pack does the old-value check, and the CAS makes the
pusher's fetch, compare, and write atomic. On one machine the local repo receives the ref update, and its hook reads the
authoritative state directory. The pre-push lease check on hosts without atomic push is the one holder-side check. It
is labeled as a lease check for that reason.

A local pre-commit hook was rejected. It misses rebase, cherry-pick, `update-ref`, and reset, and `--no-verify` skips
it. It also cannot see a broker grant from a partitioned machine. A pre-receive hook alone was rejected. github.com
offers none, the user cannot add one to remotes they do not control, and it sees refs, not lease paths. Fail-closed
local hooks that ask the broker before every commit and push were rejected. Their check-then-act window is unbounded: a
large upload or a laptop that sleeps outlasts the term, and the stale push lands. They also check commits for broker
leases, which turns a broker outage into a lockout of local commits. That breaks NIT-004's acceptance of the broker as a
single point of failure. Adding a pre-receive layer to those hooks was rejected. Once the CAS is in place, the extra
layer only adds coverage against the bypass the proposal already accepts.

### Local fencing for NIT-001

`break --force` on one machine could also bump a token, with the local git hook checking it. That would give V1 leases
fencing without a broker. Whether that belongs here or in a separate proposal is open.

The question became how the local hook from "What enforces the token" decides, and which local acts it covers. The
candidates were a local token counter with per-key high-water marks, an ownership check against the state directory,
and a separate proposal. Where the counter lives was folded in, since records are removed on release, break, and
dead-holder clearing, and the state directory has no counter today.

Decision (autonomous): the local hook checks ownership against the state directory. No local token exists. The state
directory is the authority on one machine, and the hook reads it directly.

The hook runs as a read-only `hook` subcommand in the `prepared` state. It takes no lock, and it never delivers or
removes tombstones or notices. It resolves the caller with the adapter walk every nitipan call uses.

The paths a ref update touches are defined in "Touched set for a ref update". That mapping is how a hook that sees refs
reconciles with keys that name paths, as NIT-003 "Fencing" requires. Amended 2026-10-24; see Decision Log.

Each touched key is judged on its own. Ownership follows NIT-003's `owner` rule: a parent does not own a key inside
one of its live sub-leases, and the sub-lessee does. A sub-lease past an in-force NIT-008 recall deadline is not live
for this rule. Amended 2026-10-24; see Decision Log.

- A key owned by another holder's live record, or covered by a record whose holder's liveness is unknown or by an
  unusable record, is refused
- A key listed in an undelivered tombstone addressed to the caller is refused. The refusal quotes the tombstone and
  leaves it in place
- A key listed in a fence mark addressed to the caller is refused. See "The fence mark"
- A key no live record covers, with no such tombstone or fence mark, passes. A lease is not required to commit, as in
  NIT-001
- A dead holder's record does not refuse. The hook is read-only, so it cannot clear it

A caller with no adapter and no agent variables is not an agent, and passes. That covers the user's own terminal.
`--no-verify` does not skip `reference-transaction`, so refusing the user would leave no way past it. A caller with
agent variables but no matching ancestor fails closed, as NIT-001 "Callers with no adapter" already splits the cases.

NIT-003's fencing outcomes hold through ownership:

- A sub-lease and its parent never share a pass. Each is judged by the keys its own record owns
- A parent's commit on keys it delegated is refused while the sub-lease lives, since the child's live record covers them
- A revoked or broken sub-lessee is refused, since the parent's live record owns the returned keys again. The parent
  passes there
- A child that survives its parent keeps its keys. Survival is not an ending
- Grant, revoke, and the return of keys to a parent need no extra write. An older binary that ends a child leaves the
  parent's record owning the keys, so the ex-child is refused and the parent passes. No fail-closed rule is needed
- A top-level `break --force` needs a fence mark, which "The fence mark" writes at break time

Local tokens were rejected. The token was read from the caller's current record, so it repeated what the record already
said, and no scenario was found where a counter refused something the ownership check allows. The counter also created
the problem it then solved: a child's higher mark refused the parent's older token on returned keys, which forced a
reissue that rewrites the parent's record. NIT-003 "Record shape" keeps that record unchanged. Older binaries would also
skip every bump, and closing that needed a `format` bump, which locks every older binary out of every write, the Codex
`session-start` hook included. The fence mark has a narrower older-binary gap, which "The fence mark" states rather than
closing with a `format` bump. A separate proposal was rejected. It leaves the Motivation's `break --force` gap open and
ships a hook that does nothing. Amended 2026-10-24; see Decision Log.

### Refused mid-task

The research leaves open what a holder does when its token is refused. It might stop and report, re-acquire and retry,
or hand its uncommitted work to the new holder.

The candidates became re-acquire and retry, stop and report, stop and preserve locally through a hook write, stop and
preserve on both reaches through salvage pushes, and hand-off.

Decision (autonomous): stop and report, with the refused commit's SHA and a pin command on stderr. The refusal itself
preserves the work, so the hook stays read-only.

- Locally, the refusal prints each refused ref's new SHA and the command that pins it,
  `git update-ref refs/nitipan/salvage/<id> <sha>`. The pin writes outside `refs/heads/*`, so the hook lets it through.
  A salvage ref is never part of the baseline in "Touched set for a ref update", so a pinned commit landed later on a
  branch is still checked.
  Without it, a refused commit survives only as a dangling object that `git gc` prunes
- On the remote path nothing extra happens. The unlanded commits already sit on the stale clone's branch
- The holder stops protected actions in that domain and reports to the user. It does not run `rebase --abort`, `reset`,
  or `checkout` in a tree it no longer owns

Each refusal kind has its own message and its own next step:

- Another holder's live record over the keys: names that holder. It may name `request` for the refused keys only, as
  NIT-003 does for a refused `acquire`
- A tombstone or fence mark addressed to the caller: quotes how the lease ended. It names no nitipan command and says to
  report to the user. Naming `acquire` would let the agent retake the lease in the gap between a `break --force` and the
  breaker's own `acquire`, since a tombstone never blocks a claim
- A record whose holder's liveness is unknown, or an unusable record: names the record, and says to ask the user
- Agent variables with no matching ancestor: says the caller could not be identified, and to ask the user
- A newer token at the fence: says the lease was granted to another session, and to report to the user. What it can
  name depends on what "Carrying the token" puts behind the fence ref
- An unreachable broker in the pre-push lease check: says the lease could not be verified, which is not a lost lease.
  The push can be retried once the broker answers

A rebase refused at its final branch update leaves the shared tree mid-rebase, with a detached HEAD and
`.git/rebase-merge`. The new owner's next git operation fails with "rebase in progress". Stray uncommitted edits stay in
the tree too. Nothing tells the new owner why beyond the stale holder's report to the user. This stays undone, since
refs are fenced and files are not.

Re-acquire and retry was rejected. It defeats `break --force` by design, since the stale holder can win the gap before
the breaker's `acquire`. A hook that records the refused SHA in the state directory was rejected. It breaks the
read-only, lock-free hook, and stderr carries the same value. Salvage pushes to the remote were rejected. The commits
are already safe on the stale clone, and a namespace exempt from the fence is an unchecked push channel. Hand-off was
rejected. No current failure needs it, and locally the edits already sit in the new owner's tree.

### Token scope

The research does not say whether one counter covers all names or each name has its own. Per-name counters keep tokens
small. One counter is simpler for a receiver that guards several names.

Tokens exist only for broker grants, since `machine` leases are fenced by ownership. The candidates were a counter per
name with a fence ref per name, a counter per repo, one counter with one fence ref per repo, one counter with a fence
ref per key, and a counter per key.

Decision (autonomous): one broker-wide counter, with a fence ref per lease key. Tokens are comparable everywhere, so
nested and sibling keys order correctly. A 64-bit counter never runs out, so small tokens buy nothing.

A protected push follows this rule:

1. The pusher fetches the repo's whole fence namespace. A fence ref appears only on its key's first push, so the
   pusher cannot assume which exist
2. For each path the push touches, under "Touched set for a ref update", the check set is every existing fence ref whose
   key equals the path or is an ancestor of it, up to the repo root. Only fence refs under a root of the pushed ref's
   old or new value count. The roots come from the pushed and removed commits, never from the pusher's lease, so a
   holder of `main` that also pushes `gh-pages` meets gh-pages' fences. A deletion, and a force-push that replaces a
   ref's history, are checked under the old value's roots. A fence the pusher writes goes under the first-parent root of
   the pushed ref's new value, the root NIT-004's text form uses, or of its old value for a deletion. An orphan branch
   such as `gh-pages` then never shares a fence ref with `main`
3. The push is refused when any ref in the check set holds a token higher than the pusher's
4. The pusher writes its own token only to refs whose key lies at or below one of its own keys. It creates the ref for
   each of its keys that covers a touched path
5. A ref whose key lies strictly above every key the pusher holds keeps its stored token. It still gets a fresh object
   carrying that token, so the compare-and-swap runs
6. A push that touches a path outside every key the pusher holds, where the check set for that path is not empty, is
   refused unless the broker confirms that no live lease covers it. A path with no fence ref above it needs no broker
   call, so a repo nobody has fenced never depends on the broker. An unreachable broker reads as "could not verify".
   Fence refs record tokens, not liveness, so only the broker can answer for keys the pusher does not hold. This is a
   lease check, not fencing: a lease granted and pushed between the broker's answer and this push creates a fence ref
   this push never sees

Every protected push writes a new fence object, a commit whose parent is the previous fence value and which carries the
token. git does not send a ref whose new value equals the advertised one, and receive-pack neither locks nor checks a
ref it is not sent.

A push that loses a compare-and-swap is not yet a refusal. Two sibling pushes both refresh a shared ancestor ref, and
one loses with "stale info". The pusher fetches again, runs the rule again, and pushes again, a bounded number of
times. Only a higher token in the check set is a refusal. Running out of attempts reports contention, not a lost lease.

The fenced remote is the one being pushed to. A claim inside a submodule is fenced by pushes to the submodule's own
remote, under the submodule's own root set. A host that refuses to create refs under the fence namespace fails the whole
atomic push. The error says the host's ref policy refuses the fence, which is not a lost lease.

The pusher's token comes from the grant and renewal replies that NIT-004's request handoff delivers to the CLI. `list`
shows it wherever it shows the holder's broker leases.

Sibling holders of `/repo/src` and `/repo/docs` never raise the shared `/repo` ref under rule 5, so neither blocks the
other. A new holder of `/repo` writing into `src` raises `/repo`, and a stale holder of `/repo/src` is then refused.

There is no reissue rule. Broker grants have no sub-leases today, and local sub-leases are fenced by ownership. The
proposal that first creates broker sub-leases owns the reissue under NIT-003's outcomes. A fence ref per key leaves room
for it.

A counter per name was rejected. Two counters cannot order a stale holder of `/repo/src` against a new holder of
`/repo`, so the stale push lands after the new holder's. A counter per key fails the same nested case, with larger
tokens. One fence ref per repo was rejected. Two live, non-overlapping leases lock each other out in turn. A counter per
repo was rejected. It ties counter identity to NIT-004's repo name, a set of root commits that grows when unrelated
history is merged. Two counters would then guard one repo, and their tokens would not order. A single fence ref holding
a map of keys was rejected. Every push in the repo would contend on that one ref from the start, including pushes to
unrelated keys. With a ref per key, pushes contend only under an ancestor that has a fence. Once a root fence exists,
that contention is the same, and the retry above covers it.

### Carrying the token

The research does not say how a commit or push carries the token. Candidates include a commit trailer, a push option,
and an environment variable the hook reads.

After "What enforces the token", nothing local carries a token. The local hook resolves its caller and reads the state
directory. What remained was how a push gets its fence updates into the same atomic push, and the format of the fence
object, which every nitipan binary that pushes to a remote reads and writes. The candidates were a wrapper with a
trailer-message fence, a wrapper with a JSON fence, a pre-push hook that pushes the fence in its own transaction, and a
push option.

Decision (autonomous): a `nitipan push` wrapper, guarded by a pre-push hook, with a JSON fence object. The pusher's
token is the one in its broker lease record.

- `nitipan push` computes the fence updates under "Token scope" and runs one `git push --atomic` with the branch refs
  and the fence refs together
- Each fence ref is pushed by SHA with `--force-with-lease=<fence>:<fetched-sha>`. A ref being created expects it to be
  absent. Nothing is pushed with `+` or `-f`
- The pre-push hook is the authority, not only a guard against bypass. It recomputes the required fence set from the ref
  list git gives it on stdin, and refuses a push that lacks it. It refuses a plain `git push` that touches a fenced path
  and names `nitipan push`. It also runs for `nitipan push`, which never passes `--no-verify`, so a wrapper that
  resolves refspecs differently from git is caught
- The fence commit has a tree with one `fence.json` blob. Its envelope is versioned `major.minor`, as NIT-001's records
  are. It carries the token, the key's repo-relative path in the ref-name encoding, and the lease ID. It names no
  holder, machine, or user, since a fence ref may sit on a public remote. A refusal asks the broker who holds the key
  when the broker answers
- The ref-name encoding and the namespace path are frozen with the object format. A reader checks the key in the object
  against the decoded ref name, and a mismatch makes the ref unusable
- A ref name in the fence namespace that does not decode refuses every push to that remote and says to upgrade nitipan.
  A decodable ref of a higher major refuses only a push whose check set holds it, and names only the upgrade. A
  decodable ref with an unusable object refuses the same way, and says to ask the user. The user's recovery for an
  unusable ref is `nitipan fence delete`, under "Tokenless pushers and fence cleanup". No error an agent reads names
  that command, since an agent follows the command its error names, and deleting a live fence lets a stale push land
- The refresh of an ancestor ref under rule 5 of "Token scope" reuses the prior tree. It changes only the parent and the
  commit time, so a binary that writes it never drops fields it does not know
- A minor version may add a fence field only if an old reader ignoring it, or an old writer dropping it from a ref it
  owns, never lets through a push that a new reader would refuse. Any other field needs a new major
- A broker lease whose record has no token refuses `nitipan push` and names the cause. That covers a lease granted
  before tokens existed
- The hook script reports a `nitipan` on PATH that lacks the hook subcommand as too old for this clone's hook, and says
  to upgrade. git would otherwise say only that the hook declined

Version skew, across three deploy units: each machine's binary with the hooks in its clones, the fence refs on the
remote as shared stored data, and the broker as the source of tokens.

- Same major, newer writer to older reader: the reader takes the token and ignores the rest. The minor rule makes that
  safe
- Same major, older writer: under rule 5 it copies the tree, so nothing is lost. Under rule 4 it writes its own grant's
  fence at its own minor, which the minor rule makes safe
- A higher major in the check set refuses the older binary's push and says to upgrade. This fails closed. For that
  remote it is a one-way door until every machine that pushes there upgrades
- A binary from before this proposal knows nothing of the fence. A plain `git push` from a clone without the hook lands
  unfenced. No direction can be made tolerant without a server hook. This is the settled "Only pushers that cooperate
  are fenced"
- A hook script newer than the `nitipan` on PATH fails closed with the upgrade message above
- No deploy order is needed. Every direction either works or refuses with a message

A trailer-message fence was rejected. git paths may contain a newline, which could forge a trailer, and duplicate
trailers have no rule. A second serialization would also sit beside the JSON that NIT-001's records and NIT-004's
protocol use. A pre-push hook that pushes the fence in its own transaction was rejected. The fence and the branch land
separately, so a stale branch update can land after the new holder's fence. A push option was rejected. Only server
hooks read push options, and none exists. A remote helper set as `pushurl` was rejected. Every push would run through
nitipan's reimplementation of git's helper protocol, so a nitipan bug would block every push.

### Touched set for a ref update

Reopened from "Local fencing for NIT-001" and "Token scope" on 2026-10-24. `rev-list <new> --not --all` sees no new
commits for a finished rebase, `reset`, a detached-HEAD commit landed by `update-ref`, or a commit pinned under the
salvage ref, so each passes the hook. A force-push rewind or a branch deletion carries no fence update. Which paths a
ref update touches, locally and at the remote, is open.

The candidates were a tree diff of each updated ref, the commits added and removed against a trusted baseline, and the
commit rule plus a tree diff for a ref checked out in a leased worktree. The deeper choice was what the hook protects.

Decision (autonomous): authorship governs every ref, and the owned tree also governs a ref that is checked out. The
same rule runs locally and at the remote, which has no worktrees, so the remote runs only the commit rule.

- The old value of each ref comes from `git rev-parse <ref>` in the `prepared` state, never from the hook's stdin. git
  passes the caller's expected old value there, so `update-ref refs/heads/main HEAD~1` and `branch -f` arrive as
  creations with an all-zero old value
- The baseline is the refs a rule may trust. Locally it is `--branches` across every worktree and `--remotes`, so a
  pull, a rebase onto upstream, or a checkout of `origin/main` does not count fetched commits as the caller's own. HEAD,
  tags, and salvage refs are never baseline. A hand-written remote-tracking ref can launder a commit locally, under the
  accepted risk "Hooks are bypassable". At the remote the baseline is the branches in the advertisement fetched for this
  push, never `refs/remotes`, since an unpruned remote-tracking ref would make unpushed commits look pushed
- Added commits are `rev-list <new> --not <baseline>`. Each is diffed against its parents. A merge counts only the paths
  that differ from every parent
- Removed commits are `rev-list <old> --not <new> --exclude=<this ref> <baseline>`. Their paths count as touched. That
  covers a rewind, a force-push, and a deletion, and a deletion touches only what no other branch still holds
- A ref checked out in a worktree of a leased checkout also counts the tree diff between its old and new values, when
  that worktree's index still matches the old value's tree. Then the ref moves under a tree git did not rewrite, as with
  `update-ref` or `reset --soft` from any worktree. The diff resolves against the worktree that has the ref checked
  out. Moving `main` onto commits another branch or `origin/*` already holds is then checked against the owner of
  `main`'s tree. An update after which the index already matches the new tree, such as a `pull` or `merge` there, gets
  no tree diff. git has already rewritten that tree, so a refusal would only leave it out of step with HEAD
- Each worktree's HEAD is checked with the commit rule only. A commit on a detached HEAD, such as after a refused
  rebase, is refused at once in keys the caller does not own. HEAD gets no tree diff, since `checkout` and `reset`
  rewrite the tree before the hook runs, and a tree diff there would refuse read-only inspection
- When an object a rule needs is missing, such as the old value of a force-push over commits this clone never fetched,
  the check fails closed and says to fetch

What this leaves undone:

- The tree diff cannot protect a tree that `merge`, `reset --hard`, or `pull` inside the leased worktree already
  rewrote. git changes the tree and index before the hook runs, so the refusal leaves HEAD in place and the tree
  changed. It protects the tree only when the ref moves without a checkout. This is the settled "Refs are fenced, not
  files"
- A merge that discards the other side's change, such as `-s ours`, counts nothing for the discarded paths. Counting
  paths that differ from any parent would refuse ordinary merges in a shared checkout. It is accepted

A tree diff for every ref was rejected. It refuses every rebase onto, and every pull of, work others hold, even with no
tree involved, and agents would learn to route around it. The commit rule alone was rejected. A branch checked out in
another worktree could be moved onto baseline commits unchecked, under a tree its owner never changed, and that owner's
next commit would revert the move.

### Counter durability

The broker-wide counter must never reissue a value or fall below a token already on a remote. How it survives a
restart, a reinstall, and a restore from backup is open.

The candidates were persisting the counter before each reply, flooring each token at the wall clock, learning from
refused pushes, and an epoch prefix.

Decision (autonomous): persist, floor at the clock, and raise on a bounded report.

- Each new token is `max(last + 1, now)`, with `now` the broker's wall-clock time in microseconds. The counter and the
  grant land in one durable write before the reply leaves
- A pusher refused by a fence token higher than any the broker has issued reports it through the request handoff. The
  broker raises its counter above that token only when the token is at most `now` plus a margin. The refusal names the
  cause as a broker restore
- A reported token above that bound marks its fence ref unusable, under "Carrying the token". The user deletes that ref.
  A garbage value near `2^64` can then never push the counter to its top
- Microsecond tokens stay below `2^53` until about the year 2255, so JSON readers such as `jq` read them exactly. The
  fence object and `list --json` carry tokens as JSON numbers under that bound

A restore also loses the broker's lease table. A renewal for a lease the broker does not know must return lost, never
revive it, or two sessions would each believe they hold it. That rule belongs to NIT-004 "Lease term and renewal" and
is listed under Impacts.

Persisting alone was rejected. After a restore, a stale holder's higher token passes and the new holder is refused, and
deleting fence refs to recover removes the fence against the stale holder. An epoch prefix was rejected, since a restore
also restores the epoch. Learning from refused pushes with no bound was rejected. One garbage value would push the
counter to its top for good.

### Installing the git hooks

Nothing installs the `reference-transaction` and pre-push hooks today. How they are installed, how they coexist with
other hooks, and what a hook does when `nitipan` is missing is open.

The candidates were a global `core.hooksPath`, a per-checkout install with a warning at `acquire`, and a per-checkout
install that `acquire` requires.

Decision (autonomous): `nitipan setup git --install <checkout>` installs the hooks, and a path claim in a git checkout
fails closed until they are in force. Codex `acquire` already fails closed the same way when its hooks are missing.

- The hooks go into the directory git actually uses, `git rev-parse --git-path hooks`, which honors a local
  `core.hooksPath` such as husky's. They chain to any hook already there and keep it. They also go into every
  initialized submodule's git directory, since a claim inside a submodule is fenced through the submodule's own remote
- A hooks directory that holds files git tracks, such as husky's `.husky`, is never written. The install puts
  nitipan's hooks in a directory under the git directory, sets the checkout's local `core.hooksPath` to it, and chains
  every hook name git documents to the tracked directory. Nothing nitipan writes can then be committed, and a
  collaborator without nitipan never meets its hooks. A hook manager that later resets `core.hooksPath` is caught at the
  next `acquire`. A hook name git adds later is not chained until nitipan learns it
- `setup git --uninstall <checkout>` removes them and restores what they chained to
- The hooks bake the stable PATH location of `nitipan`, not the symlink-resolved path. A Homebrew upgrade deletes the
  versioned Cellar path. The script falls back to `command -v nitipan`
- With no `nitipan` found, the hook fails closed for `refs/heads/*` and HEAD updates and passes every other ref, such as
  fetches, tags, and salvage pins. The script tells them apart without the binary. The error names reinstalling and
  `setup git --uninstall`. It never names `git -c core.hooksPath=/dev/null`, since an agent follows the command its
  error names. That escape stays in the documentation only
- `acquire` of a path inside a git checkout refuses when the hooks are not in force there, and names the install
  command. A checkout that a tool rewires later is caught at the next `acquire`. Resource claims and paths outside a git
  checkout are unaffected
- `break --force` says whether the broken lease's checkout had the hooks in force, so the user knows whether the break
  is fenced

This changes NIT-001's behavior for existing callers. A path claim that succeeded before needs the install once per
checkout, and the error says how.

A global `core.hooksPath` was rejected. It replaces `.git/hooks` for every hook name, so any name nitipan does not
chain stops running the repo's own hook. A missing binary would also break git in every repo the user has. A warning at
`acquire` was rejected. An agent ignores a warning, and the user would then break a lease believing it fenced.

### The fence mark

A fence mark is lifted when its holder claims the keys again, and a tombstone never blocks a claim. So the broken
holder's own `acquire` before the breaker's clears it. Whether the mark guards that claim, how it is lifted, how it is
stored, and what an older binary that delivers a tombstone does are open.

The candidates were a mark that also guards the marked holder's claims, and a mark that does not, with the gap
accepted.

Decision (autonomous): the mark guards the marked holder's claims, and is written at break time.

- `break --force` writes the fence mark for the broken lease's keys in the same locked call as the tombstone. An older
  binary that later delivers the tombstone never scans the marks directory, so it cannot lift the mark. A new binary
  that delivers a tombstone with no mark, because an older binary did the break, writes the mark then
- The mark refuses every path that would create a record for the marked holder over its keys: `acquire`, `--replace`,
  and NIT-003 `request`. A parent's `grant` to the marked holder may lift it, since that is the owner's consent. Other
  holders are unaffected
- The mark lifts per key. A key lifts when another holder's live record covers it, or when a grant covers it. The whole
  mark lifts when the user runs `break <mark-id>`, or when a lock holder finds the marked holder dead
- Marks live in their own directory, which V1 binaries never scan. Each mark carries NIT-001's envelope with
  `major.minor` and footprint `none`. `list` shows them, `break` removes one by its ID, and a lock holder reaps a mark
  whose holder is dead. The hook reads them, and a mark it cannot use refuses the keys it names when the keys can be
  read, and otherwise is reported and ignored

This narrows NIT-001's "a tombstone never blocks a claim" for one holder. NIT-001 is implemented, so its "Breaking a
live lease" takes a "Superseded in part by NIT-005" marker when this proposal ships.

Older binaries leave two gaps, stated rather than closed:

- An older binary that does the break writes no mark. The mark appears only when a new binary delivers the tombstone,
  so a broken holder that runs an older binary until then is unguarded
- An older binary's `acquire` by the marked holder ignores the mark and succeeds. The hook still refuses that holder's
  commits on the marked keys, but its new record blocks the breaker's `acquire` until the user breaks it

A `format` bump would close both. It was rejected, since it locks every older binary out of every write, the Codex
`session-start` hook included, which is out of proportion to two mixed-binary gaps. A mark that does not guard claims
was rejected. NIT-001 says a broken holder learns of the break by re-acquiring, so its next `acquire` delivers the
tombstone, succeeds, and lifts the mark in one call. That is the main path, not a corner case.

### Tokenless pushers and fence cleanup

`nitipan push` defined only holders of broker leases with tokens. A fence ref stays on the remote after its key's first
fenced push, so the user, agents with no covering broker lease, and a `git push --mirror` from a clone without the
fence refs each needed a rule. The candidates were treating a tokenless pusher as token 0, sending every tokenless
pusher through push rule 6, mirroring the local rule, and the same with automatic pruning of old fence refs.

Decision (autonomous): mirror the local rule, and guard the fence namespace structurally.

- A caller with no adapter and no agent variables passes the pre-push hook with no fence update, as it passes the local
  hook. The user holds no lease, so there is no staleness to fence. Refusing the user would also teach `--no-verify`,
  which turns off every other guard on that push. At `shared` reach a second human counts the same way, under the
  accepted risk "Hooks are bypassable"
- A caller with agent variables but no matching ancestor fails closed, as NIT-001 does locally
- An agent with no broker lease covering the touched paths goes through push rule 6, a lease check. Rule 6 fires only on
  paths with a fence ref at or above them, so a lease-less agent can push into a live broker lease's keys unchecked
  until that key's first fenced push. That extends the settled "A stale push that lands before the new holder's first
  push" to non-holders
- An update to a ref under the fence namespace must create it or fast-forward it, whoever makes it. That is exactly
  what `nitipan push` does, since each fence commit's parent is the previous value. A deletion, a rewind, and a
  force-update to a diverged fence commit are refused, which covers `--mirror`
- A refused `--mirror` names the fetch to run first,
  `git fetch <remote> '+refs/nitipan/fence/*:refs/nitipan/fence/*'`. The mirror then sends equal values, which git does
  not send
- `nitipan fence delete <remote> <key>` is the one way to delete a fence ref, and it is the user's alone. It refuses a
  caller with agent variables. It keeps the recovery in "Carrying the token" and "Counter durability" reachable without
  `--no-verify`. The fence errors an agent reads say to ask the user, and never name it
- Fence refs are never removed automatically

While the broker is down, a tokenless agent's push to a fenced path is refused as "could not verify". That is a push
lockout for agents, not the commit lockout the design rejected. It changes NIT-004's risk entry "The broker is a single
point of failure", which says only claims stop, and is listed under Impacts.

Token 0 for tokenless pushers was rejected. Once a root fence exists, the user could never push in that repo without a
broker lease, and never during an outage. Rule 6 for every pusher was rejected. Every user push to a fenced path would
depend on the broker, and each refusal pushes the user toward `--no-verify`. A guard that trusts only pushes
`nitipan push` made was rejected. It has no mechanism to tell, and it blocks the user's own deletion of a bad fence.
Pruning was rejected. A holder whose lease expired while its laptop was closed would recreate a pruned fence ref with
its old token and land a stale push, which the newer holder's fence would have refused.

### Local fencing for a `user` lease

The local hook judges only records in the state directory, and NIT-004 never writes broker leases there. Whether two
sessions on one machine can share a checkout while one holds a `user` lease over it depends on NIT-004's open question
"Machine leases and user leases on the same path".

Decision (autonomous): defer. The local hook does not judge broker leases. When NIT-004 settles that question, its
answer says which hook judges such an overlap, or that the case cannot arise.

**Revisit:** when NIT-004 settles "Machine leases and user leases on the same path"

## Design Decisions (Open)

None.

## Risks

- **The user's remote hosts may not support atomic push** — likelihood: high, impact: medium.
  The fence needs `git push --atomic`. Azure DevOps does not advertise it, and github.com's current behavior is
  unconfirmed. The likelihood is rated high because the evidence does not settle it. Where atomic push is missing, a
  push gets only the fail-closed lease check, whose window is bounded by one 5-minute term after a synchronous renewal.
  A host that refuses refs under the fence namespace fails the push outright. Mitigation: the lease check bounds the
  impact, and M3 records which of the user's hosts support atomic push and fence refs before the fence ships.
- **Hooks are bypassable** — likelihood: medium, impact: medium.
  `git push --no-verify`, a local `core.hooksPath` set after `acquire`, a clone with no hooks, and a binary from before
  this proposal each skip the fence. `--no-verify` does not skip `reference-transaction`. Accepted: the environment is
  high-trust, and fencing guards against accidents, not hostile sessions. `acquire` catches a missing local hook at the
  next claim.
- **The fence format is a one-way door on remotes** — likelihood: medium, impact: high.
  The fence namespace, its name encoding, and the object's major version sit on remotes the user may not control, and
  every binary reads them. A higher major refuses older binaries until every machine that pushes there upgrades. One
  ref name that does not decode refuses every push to that remote. Mitigation: the format is versioned `major.minor`
  with writer and reader rules, every refusal names the upgrade or says to ask the user, `nitipan fence delete` gives
  the user a recovery, and the format is recorded as an ADR candidate that waits until it is checked.
- **The token counter regresses after a broker restore** — likelihood: low, impact: high.
  A restored broker below the tokens on remotes would let a stale holder pass and refuse the new holder. Mitigation:
  tokens are floored at the broker's clock and raised on a bounded report, under "Counter durability". The residual is
  a restored broker whose clock is also behind, before any refused push reports a higher fence.
- **NIT-004 is still in design** — likelihood: medium, impact: medium.
  This proposal relies on five NIT-004 sections, listed under Impacts. NIT-004 is `designing` and changed while this
  proposal was reviewed. Mitigation: a change to any listed section reopens the matching part of this proposal. M2 and
  M3 need NIT-004 built, and M1 does not.
- **A `user` lease on a shared checkout is not fenced locally** — likelihood: medium, impact: medium.
  The local hook judges only records in the state directory, and broker leases are never written there. Two sessions on
  one machine sharing a checkout, one of them holding a `user` lease, are then fenced only at the push. NIT-004's open
  question "Machine leases and user leases on the same path" decides whether that case exists. Mitigation: deferred
  under the settled "Local fencing for a `user` lease"
- **The hook requirement changes existing callers** — likelihood: high, impact: low.
  A path claim in a git checkout that succeeded under NIT-001 fails until the hooks are installed. Accepted: the error
  names the install command, and it is needed once per checkout.
- **The hook runs on every branch and HEAD update** — likelihood: medium, impact: low.
  A rebase updates HEAD once per commit, and each update runs `nitipan`. Mitigation: the script passes other refs
  without the binary, and M1 records the hook's cost on a long rebase.
- **Fence refs accumulate on remotes** — likelihood: high, impact: low.
  Fence refs are never removed automatically, and every push under a root fence contends on it. Accepted: they are
  small, deleting one reopens the fence for a stale holder still offline, and a lost compare-and-swap is retried.

## Milestones

### M1: Local fencing

**Status:** NOT STARTED

Fences `machine` leases on one machine. Needs no broker.

- `setup git --install` and `--uninstall` write and remove chaining hooks in the directory git uses, submodules
  included, and keep any hook already there
- A path claim in a git checkout without the hooks in force is refused, and the error names the install command
- The `reference-transaction` hook refuses a ref update that touches a key another holder owns, a key in an
  undelivered tombstone addressed to the caller, or a key in a fence mark. It passes the user's own terminal
- A test per channel shows the touched set catches a commit, a finished rebase, `reset --hard`, `update-ref` and
  `branch -f` on a branch checked out in a sibling worktree, `update-ref` and `reset --soft` in the same worktree, a
  detached-HEAD commit, a deletion, and a pinned salvage commit landed on a branch
- A pull, a rebase onto upstream, and a checkout of `origin/main` pass when the upstream commits touch others' keys
- A checkout whose hooks directory is tracked gets nitipan's hooks through a local `core.hooksPath` under the git
  directory, and no tracked file changes
- NIT-003's outcomes hold: a parent on delegated keys, a revoked sub-lessee, and a child ended by an older binary are
  each refused, and the parent passes on returned keys
- `break --force` writes a fence mark, which refuses the marked holder's `acquire`, `--replace`, and `request`, lifts
  per key, and is shown by `list` and removable by `break`
- Each refusal kind prints its own message, and a local refusal prints the refused SHA and a pin command that works
- A missing `nitipan` binary fails closed for branch and HEAD updates only
- The hook's cost on a long rebase is recorded in the As built block

### M2: Token issue

**Status:** NOT STARTED

- Every broker grant returns a token of `max(last + 1, now)` in microseconds, persisted with the grant before the
  reply leaves
- A revived lease keeps its token, and a renewal for a lease the broker does not know returns lost
- A reported fence token within the bound raises the counter, and one above it marks the ref unusable
- The token reaches the CLI in the grant and renewal replies, and `list` shows it

### M3: Remote fencing

**Status:** NOT STARTED

- `nitipan push` runs one atomic push with the branch refs and the fence refs, each fence pushed by SHA with
  `--force-with-lease`
- The pre-push hook refuses a push whose fence set is missing, and every tokenless pusher follows the settled rule
- The push rule's nested, sibling, and orphan-branch cases each have a test, and a lost compare-and-swap is retried
- A test partitions a holder, grants its lease to another session that pushes, and shows the stale push refused
- Fence objects follow the versioned format, and a test runs older and newer readers and writers against one remote
- Hosts without atomic push get the labeled lease check, and the As built block records which of the user's hosts
  support atomic push and fence refs

## Decision Log

- 2026-10-18: Drafted from the lease broker research. Settled decisions are limited to the roadmap the user approved
  that day.
- 2026-10-24 (autonomous): "What enforces the token" settled per reach. A local `reference-transaction` hook checks
  `machine` leases against the state directory. A fence-ref compare-and-swap in an atomic push checks broker leases,
  and commits are not checked for them. Hosts without atomic push get a fail-closed lease check, labeled as not
  fencing. Converged in one pass on an option the critic added.
- 2026-10-24 (autonomous): "Local fencing for NIT-001" settled in this proposal as an ownership check against the
  state directory, with no local token. Touched keys come from the commits new to the repo, and undelivered tombstones
  refuse the broken holder. NIT-003's outcomes hold with no extra write. Converged in one pass.
- 2026-10-24 (autonomous): "Refused mid-task" settled as stop and report, with the refused SHA and a pin command on
  stderr. Each refusal kind has its own message. A refusal for an ended lease names no nitipan command. Converged in two
  passes; the second added the critic's variant that preserves the commit without a hook write.
- 2026-10-24 (autonomous): Amended "Local fencing for NIT-001". The refusal rested on an undelivered tombstone, and the
  holder's own next nitipan call delivers and removes it, so the refusal lapsed before the breaker acquired. It said
  "a key listed in an undelivered tombstone addressed to the caller is refused" as the only post-break refusal. It now
  also refuses keys in a fence mark, written when the tombstone is delivered and removed when the caller holds those
  keys again or dies.
- 2026-10-24 (autonomous): "Token scope" settled as one broker-wide counter with a fence ref per lease key, and a push
  rule that keeps ancestor refs at their stored token. No reissue rule until broker sub-leases exist. Converged in one
  pass.
- 2026-10-24 (autonomous): Amended "What enforces the token". It said a fence ref names an encoding of the key "which
  depends on NIT-004's 'Name syntax for wider reach'". Fence refs live on the repo's own remote, so the name needs only
  the repo-relative path, flattened for nesting. Only mapping a broker key to that path depends on NIT-004.
- 2026-10-24 (autonomous): "Carrying the token" settled as a `nitipan push` wrapper guarded by an authoritative pre-push
  hook, with a versioned JSON fence object carrying the token, key, and lease ID. The fence names no holder, so
  nothing identifying reaches a public remote. The critic dissented on that point. It had listed the holder in the
  fence, with disclosure flagged as the user's to decide. Answer: a refusal asks the broker for the holder, so naming
  survives whenever the broker answers. Converged in one pass on the critic's hardened option.
- 2026-10-24: ADR candidate (hard to reverse): the fence ref namespace, its name encoding, and the fence object format.
  It is stored data on remotes the user may not control, and every binary reads it. Waits on the proposal loop's
  check of the encoding and counter durability, and on NIT-004 reaching `accepted`, since the key mapping relies on its
  name syntax.
- 2026-10-24 (autonomous): The whole-proposal review reopened the touched-path rule from "Local fencing for NIT-001"
  and "Token scope". A finished rebase, `reset`, a detached-HEAD commit landed by `update-ref`, and a salvage pin each
  passed the hook. A force-push rewind or a deletion carried no fence update. Added the open questions "Touched set for
  a ref update", "Counter durability", "Installing the git hooks", and "The fence mark".
- 2026-10-24 (autonomous): Fixes from the same review. Fence refs carry the lease's root set, and a check set holds only
  refs whose root set meets the pusher's, so an orphan branch never blocks `main`. A lost compare-and-swap is retried
  before it counts as a refusal. Push rule 6 is labeled a lease check. A host that refuses fence refs, submodule
  remotes, and where the token arrives are stated. The Summary and Impacts now match the settled design.
- 2026-10-24 (autonomous): "Touched set for a ref update" settled. Authorship governs every ref against a trusted
  baseline of branches, with added and removed commits both counted. A ref checked out in a leased worktree also counts
  its tree diff, and old values come from `rev-parse`. Converged in one pass on the critic's hardened option.
- 2026-10-24 (autonomous): "Counter durability" settled. The counter is persisted with the grant, floored at the
  broker's clock in microseconds, and raised on a bounded report of a higher fence. Converged in one pass.
- 2026-10-24 (autonomous): "Installing the git hooks" settled. `setup git --install` writes chaining hooks where git
  looks for them, and a path claim in a git checkout fails closed until they are in force. A missing binary fails
  closed for branch and HEAD updates only. Converged in one pass.
- 2026-10-24 (autonomous): "The fence mark" settled. `break --force` writes it, it refuses the marked holder's own
  claims, and it lifts per key. Two mixed-binary gaps are stated rather than closed with a `format` bump. Converged in
  one pass.
- 2026-10-24 (autonomous): Amended "Local fencing for NIT-001". Its rule said a key "covered by another holder's live
  record" is refused, which refused a live sub-lessee through its parent's record. It now uses NIT-003's `owner` rule.
  It said an older binary ending a child "leaves the keys with no live record over them". The parent's record still
  covers them, so the ex-child is refused through it. The rejection of local tokens cited a `format` bump as its own
  cost. It now says the fence mark's narrower gap is stated instead. The touched-path paragraph moved to "Touched set
  for a ref update".
- 2026-10-24 (autonomous): Added and settled "Tokenless pushers and fence cleanup". The user passes the pre-push hook
  as it passes the local hook. Agents with no covering broker lease go through push rule 6. A fence ref may only be
  created or fast-forwarded, which refuses `--mirror` deletes. `nitipan fence delete` is the recovery the fence errors
  name. Converged in one pass on the critic's hardened option.
- 2026-10-24 (autonomous): Fixes from the proposal loop's second pass. Fence refs are named by root commit, and a check
  set uses the pushed ref's own roots, since one ref per path let leases on disjoint histories overwrite each other's
  fence. The local baseline counts remote-tracking refs, since excluding them refused every pull of upstream work. The
  tree diff applies only to an update from another worktree. `fence delete` refuses agent callers, and no agent-facing
  error names it, since a stale agent would delete a live fence. Push rule 6 fires only where a fence ref sits at or
  above the path. The hook install refuses a tracked hooks directory.
- 2026-10-24 (autonomous): Fixes from the proposal loop's third and last pass, not re-checked. A deletion or a
  root-replacing force-push is checked under its old value's roots, since an empty new value gave it no check set. The
  tree diff applies whenever the worktree's index still matches the old tree, since `update-ref` and `reset --soft` move
  a ref in its own worktree without rewriting the tree. A tracked hooks directory gets a nitipan-owned local
  `core.hooksPath` under the git directory that chains to it. Refusing the install there blocked every path claim in a
  husky repo.
- 2026-10-24 (autonomous): Risk review. "Remotes without hooks stay unfenced" is replaced by "The user's remote hosts
  may not support atomic push", mitigated by the fallback lease check and an M3 record of the user's hosts. Its
  likelihood is rated high because the evidence does not settle it, and its impact medium because the lease check bounds
  the window. Added the fence format as a one-way door, counter regression, NIT-004 in design, the hook requirement for
  existing callers, the hook's cost, and fence-ref accumulation. Deferred mitigating the unfenced local `user` lease
  until NIT-004 settles "Machine leases and user leases on the same path". No risk is rated high likelihood and high
  impact.
- 2026-10-24: Review gate: kept all. Contested at the gate were "Carrying the token" with the critic's dissent on
  naming the holder, the unchecked third-pass fixes, the hook requirement at `acquire`, and the fence mark blocking the
  broken holder's re-claim. NIT-003's "Fencing" revisit hook was extended to fire when this proposal is accepted.
- 2026-10-24: ADR candidate (cuts across the system): a path claim inside a git checkout requires nitipan's git hooks
  in force. Waits on NIT-005 M1, whose check for a tracked hooks directory exercises the fix the proposal loop did not
  re-check.
- 2026-10-24: ADR candidate (departs from the past): a fence mark blocks the broken holder's own claim, narrowing
  NIT-001's "a tombstone never blocks a claim". Waits on this proposal reaching `accepted`. Declined on 2026-10-24:
  on recheck it fails Architecture, since it shapes only the claim path, and NIT-001's "Superseded in part" marker
  records it where a reader of that rule looks.
- 2026-10-24: NIT-008's review settled that the hook honors a recall. A sub-lease past its in-force deadline is not live
  for the ownership rule, and the hook has a refusal kind for a recalled sub-lessee that quotes the recall and names no
  command. Before, the rule knew no recall. NIT-008 M2 builds it.

## References

- `spec/reports/2026-10-18-lease-broker-research.md`: "Fencing"
- NIT-004: the broker that issues tokens
