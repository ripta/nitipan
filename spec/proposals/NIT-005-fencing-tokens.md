# NIT-005: Fencing Tokens

**Status:** implemented
**Created:** 2026-10-18
**Updated:** 2026-11-08

## Dependencies

- NIT-004

## Impacts

- NIT-001 — "Breaking a live lease". Tombstones are a broken holder's only signal until fencing exists. This proposal
  is that fencing. "The fence mark" narrows its "a tombstone never blocks a claim" for one holder, so that section
  took a "Superseded in part by NIT-005" marker when M1 landed. Amended 2026-11-08; see Decision Log
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
  Without it, a refused commit survives only as a dangling object that `git gc` prunes. A refused deletion has no new
  SHA, so it prints no pin, and the ref stays where it was. Amended 2026-10-25; see Decision Log
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
- A transaction line whose new value is the value the ref already holds passes, and so does a symbolic update, whose
  new value is `ref:<name>`. `git pack-refs` and `git gc` send lines of both shapes. They also delete the loose copy of
  each packed ref. That deletion passes when `packed-refs` holds the ref at the same value and `packed-refs.lock` is
  absent. A real deletion of a packed ref holds the lock while the hook runs. Amended 2026-10-25; see Decision Log
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

Decision (autonomous): persist, floor at the clock, and raise on a bounded report. Amended 2026-10-29; see Decision
Log.

- Each new token is `max(last + 1, now)`, with `now` the broker's wall-clock time in microseconds. The counter and the
  grant land in one durable write before the reply leaves. The broker puts no floor on the counter at load, since
  every grant floors its own token
- A pusher refused by a fence token higher than any the broker has issued reports it through the request handoff. The
  broker answers with a verdict. A token at or below the counter is `known`, and nothing is written. A token above the
  counter and at most `now` plus 24 hours is `raised`, and the counter is set to the token in one durable write. A
  token above that bound is `unusable`, and nothing is written. The refusal names the cause as a broker restore
- The `unusable` verdict is the report's reply, not broker state. The reporter marks the fence ref unusable, under
  "Carrying the token". The user deletes that ref. A garbage value near `2^64` can then never push the counter to its
  top. The report grants no authority, since it only raises the counter within the bound
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
- A hooks directory outside the repository's git directory is never written. That covers one that holds files git
  tracks, such as husky's `.husky`, and one that other repositories share, such as a global `core.hooksPath`. The
  install puts nitipan's hooks in a directory under the git directory, sets the checkout's local `core.hooksPath` to
  it, and chains every hook name git documents to the directory it replaced. Nothing nitipan writes can then be
  committed or reach another repository, and a collaborator without nitipan never meets its hooks. The directory it
  chains to is the last `core.hooksPath` that any config scope sets apart from nitipan's own, with a leading `~`
  expanded the way git does. A relative value stays relative to each worktree. A re-install reads it from the scopes
  again, and the checkout's local value from before the first install stays recorded as written, so an uninstall
  restores it unchanged. A hook manager that later resets `core.hooksPath` is caught at the next `acquire`. A hook name
  git adds later is not chained until nitipan learns it. Amended 2026-10-25; see Decision Log
- `setup git --uninstall <checkout>` removes them and restores what they chained to
- The hooks bake the stable PATH location of `nitipan`, not the symlink-resolved path. A Homebrew upgrade deletes the
  versioned Cellar path. The script falls back to `command -v nitipan`
- With no `nitipan` found, the reference-transaction hook fails closed for `refs/heads/*` and HEAD updates and passes
  every other ref, such as fetches, tags, and salvage pins. The script tells them apart without the binary. The
  pre-push hook fails closed for every push. git lists no ref that a pruning push such as `--mirror` deletes, so no
  input shows that a push is harmless, and the script starts the binary for every push. Each error names reinstalling
  and `setup git --uninstall`. It never names `git -c core.hooksPath=/dev/null`, since an agent follows the command its
  error names. That escape stays in the documentation only. Amended 2026-11-08; see Decision Log
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

- `break --force` writes the fence mark for the broken lease's keys in the same locked call as the tombstone, and
  writes the mark first. A mark that cannot be written then leaves no tombstone, and the lease stays. A tombstone that
  cannot be written takes its mark back. An older
  binary that later delivers the tombstone never scans the marks directory, so it cannot lift the mark. A new binary
  that delivers a tombstone with no mark, because an older binary did the break, writes the mark then. A break that
  handles its mark marks its tombstone `fenced`, and delivery writes nothing for it, so a mark that a user removed or
  that lifted does not return
- The mark refuses every path that would create a record for the marked holder over its keys: `acquire`, `--replace`,
  and NIT-003 `request`. A parent's `grant` to the marked holder lifts it, since that is the owner's consent. The
  parent's live record covers the granted keys, so the cover rule below lifts the mark, and `grant` has no code of its
  own for it. Other holders are unaffected
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

Amended 2026-10-25; see Decision Log.

### Tokenless pushers and fence cleanup

`nitipan push` defined only holders of broker leases with tokens. A fence ref stays on the remote after its key's first
fenced push, so the user, agents with no covering broker lease, and a `git push --mirror` from a clone without the
fence refs each needed a rule. The candidates were treating a tokenless pusher as token 0, sending every tokenless
pusher through push rule 6, mirroring the local rule, and the same with automatic pruning of old fence refs.

Decision (autonomous): mirror the local rule, and guard the fence namespace structurally. Amended 2026-11-08; see
Decision Log

- A caller with no adapter and no agent variables passes the pre-push hook with no fence update, as it passes the local
  hook. The user holds no lease, so there is no staleness to fence. Refusing the user would also teach `--no-verify`,
  which turns off every other guard on that push. At `shared` reach a second human counts the same way, under the
  accepted risk "Hooks are bypassable"
- A caller with agent variables but no matching ancestor is refused when the push updates a branch or carries a fence
  ref. A push of tags alone passes, as NIT-001's local hook passes every ref but branch and HEAD updates
- An agent with no broker lease covering the touched paths goes through push rule 6, a lease check. Rule 6 fires only on
  paths with a fence ref at or above them, so a lease-less agent can push into a live broker lease's keys unchecked
  until that key's first fenced push. That extends the settled "A stale push that lands before the new holder's first
  push" to non-holders
- An update to a ref under the fence namespace must create it or fast-forward it, whoever makes it. That is exactly
  what `nitipan push` does, since each fence commit's parent is the previous value. A deletion, a rewind, and a
  force-update to a diverged fence commit are refused, which covers `--mirror` only in part. git does not list the
  refs a pruning push deletes, and a flag can be abbreviated or hidden in an alias, so the hook cannot detect a
  pruning push. It refuses every push to a remote that holds a fence ref this clone's fence namespace lacks, or holds
  at another value, as of before the hook's own fetch. The hook then fetches the namespace, so pushing again passes.
  The rule applies to every caller and does not depend on detecting pruning. A window stays, and is accepted: another
  machine can create a fence ref on the remote after the hook's `ls-remote` and before this clone's pruning push
  lands, and the push then deletes it. Only a server-side hook closes it
- An agent's push may carry only the fence refs its own leases call for, with the tokens the plan sets. Any other
  fence ref in an agent's push is refused, so an agent cannot lock out the other holders with a ref of a chosen
  token. The user's own terminal holds no lease and is not held to the plan
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
sessions on one machine can share a checkout while one holds a `user` lease over it depended on NIT-004's question
"Machine leases and user leases on the same path".

Decision: the local hook judges the anchor. NIT-004 settled that a `user` claim made from a checkout takes or reuses an
ordinary `machine` lease there. The local hook judges that anchor like any other `machine` lease. It never judges
broker leases.

Amended 2026-10-24; see Decision Log.

## Design Decisions (Open)

None.

## Risks

- **The user's remote hosts may not support atomic push** — likelihood: low, impact: medium.
  The fence needs `git push --atomic`. Azure DevOps does not advertise it. M3's probe found that github.com,
  gitlab.com, git.sr.ht, and a tangled knot all take atomic push and fence refs. huggingface.co was not probed. Where
  atomic push is missing, a push gets only the fail-closed lease check, whose window is bounded by one 5-minute term
  after a synchronous renewal. A host that refuses refs under the fence namespace fails the push outright. Mitigation:
  the lease check bounds the impact, and `make live-fence-hosts` probes a new host. Amended 2026-11-08; see Decision
  Log.
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

**Status:** DONE

Fences `machine` leases on one machine. Needs no broker.

- `setup git --install` and `--uninstall` write and remove chaining hooks in the directory git uses, submodules
  included, and keep any hook already there
- A path claim in a git checkout without the hooks in force is refused, and the error names the install command
- The `reference-transaction` hook refuses a ref update that touches a key another holder owns, a key in an
  undelivered tombstone addressed to the caller, or a key in a fence mark. It passes the user's own terminal
- A test per channel shows the touched set catches a commit, a finished rebase, `reset --hard`, `update-ref` on a branch
  checked out in a sibling worktree, `branch -f` off a branch's unique commits, `update-ref` and `reset --soft` in the
  same worktree, a detached-HEAD commit, a deletion, and a pinned salvage commit landed on a branch. Amended 2026-10-25;
  see Decision Log
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

**As built:**

- Probes of git 2.50.1 shaped the touched set. `git gc` and `git pack-refs --prune` run the hook with a transaction of
  packed creations and then a deletion per loose ref, which "Touched set for a ref update" now covers. A commit on an
  attached branch updates only the branch, and a detached commit updates HEAD. The hook runs from the worktree's root.
  In a linked worktree `GIT_DIR` is `<common>/worktrees/<name>`, and one install in the common hooks directory covers
  every worktree. Decision Log entries of 2026-10-25 record each guess.
- git refuses `branch -f` on a checked-out branch, so that channel is open only off a branch's unique commits. The M1
  channel check was amended to say so.
- A grant to the marked holder is lifted by the cover rule, and no `grant` code exists for it. A request over a marked
  key reaches the mark check only when a mark of a newer minor, which repairs never rewrite, is still in place.
- Cost on a long rebase is measured on one macOS machine, and the numbers are in the 2026-10-25 Decision Log entry on
  the hook's cost.
- After review: a mark-aware break marks its tombstone `fenced`, so delivery never brings back a removed or lifted mark.
  The install treats any hooks directory outside the git directory as shared and chains to it. The hook opens the state
  directory read-only, and the acquire check asks git before the lock.
- Left undone, as the settled text says: a refused `reset --hard` has already rewritten the tree, a hook name git adds
  later is not chained, and an older binary's break or `acquire` leaves the two mixed-binary gaps.

### M2: Token issue

**Status:** DONE

- Every broker grant returns a token of `max(last + 1, now)` in microseconds, persisted with the grant before the
  reply leaves
- A revived lease keeps its token, and a renewal for a lease the broker does not know returns lost
- A reported fence token within the bound raises the counter, and one above it gets the `unusable` verdict, which the
  reporter treats as an unusable ref. Amended 2026-10-29; see Decision Log
- The token reaches the CLI in the grant and renewal replies, and `list` shows it

**As built:**

- The report is a new broker path, `fence`, with a request kind of the same name on the handoff. Its body is the
  token, and its reply is the verdict and the counter after the report. The CLI has a function that sends it. No
  command calls it until `nitipan push`. Decision Log entries of 2026-10-29 record each guess.
- A restore test grants, restores an older store file, and shows the renewal answered `not_found` with nothing
  revived, and the next grant above every earlier token once the clock moved on.
- The sidecar is at file version 1.1 and carries the token. The node writes it on grant and on every renewal reply
  that differs. The CLI writes it from the grant and renew replies it reads, so a node from before this change
  cannot leave a sidecar without a token the broker returned. `list --json` is at schema 1.5, with `token` on each
  broker lease.
- Existing broker tests that asserted a counter of 1 now assert the counter equals the token, since tokens are clock
  microseconds.

### M3: Remote fencing

**Status:** DONE

- `nitipan push` runs one atomic push with the branch refs and the fence refs, each fence pushed by SHA with
  `--force-with-lease`
- The pre-push hook refuses a push whose fence set is missing, and every tokenless pusher follows the settled rule
- The push rule's nested, sibling, and orphan-branch cases each have a test, and a lost compare-and-swap is retried
- A test partitions a holder, grants its lease to another session that pushes, and shows the stale push refused
- Fence objects follow the versioned format, and a test runs older and newer readers and writers against one remote
- Hosts without atomic push get the labeled lease check, and the As built block records which of the user's hosts
  support atomic push and fence refs

**As built:**

- `nitipan push` and the pre-push hook run one plan, so they compute the same required fence set. The plan is a
  pure function of the fetched fence refs, the pusher's broker keys, and the touched paths. Tables cover the nested,
  sibling, orphan-branch, deletion, force-push, and rule 6 cases. The remote touched set is the commit rule against the
  advertised branches, and the root lookup now works from any commit. Decision Log entries of 2026-11-08 record each
  guess.
- git's pre-push hook never sees the refs that `--mirror` and `--prune` delete, and a flag can be abbreviated or
  aliased. The settled refusal of a fence deletion therefore could not cover `--mirror` by itself. The hook now refuses
  any push while the clone's fence refs differ from the remote's, and its own fetch lets the retry through. Tests run
  `--mir`, an alias, and `remote.<name>.mirror`. The reason is in the Decision Log.
- A lost compare-and-swap is recognized by asking the remote whether a fence it meant to update moved, since hosts word
  the loss differently. A test forces the race from a chained hook that moves the fence between nitipan's check and the
  push, and the retry lands.
- The partition test merges the two-machine broker fixture with a bare git remote. The holder is partitioned, its lease
  lapses, another session is granted it and pushes, and the stale push is refused with the higher token and no
  command that could delete the fence. The skew test plants fence objects of versions 1.1 and 2.0 beside this binary's
  1.0 on one remote. It shows the newer-minor ancestor refreshed with its tree and unknown field intact, and the newer
  major refusing with the upgrade message.
- A host without atomic push is simulated with `receive.advertiseAtomic=false`. Git's refusal of `--atomic` is the
  detection, and the clone records it. The lease check renews the covering leases and then pushes the branches, and a
  renewal the broker answers as lost refuses.
- `make live-fence-hosts` probes the remotes of a clone with the push shape of `nitipan push`. It was tested here only
  against local bare remotes, one of them without atomic push.
- Hosts probed on 2026-11-08 over SSH, from a throwaway clone of the user's scratch repo: github.com, gitlab.com,
  git.sr.ht, and a tangled knot each advertise atomic push. Each took a fence ref created in one atomic push with a
  branch, a fast-forward of it under `--force-with-lease`, and its deletion. The probe left no refs behind.
  huggingface.co, the user's one other host, was not probed, since the scratch repo has no remote there.
- Left undone, as the settled text says: a plain `git push --no-verify` and a clone with no hooks land unfenced, and a
  stale push that lands before the new holder's first push is not caught. A shallow clone cannot push as an agent once a
  repo has fence refs or the agent holds a broker lease, because its root commits are unknown.

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
  It is stored data on remotes the user may not control, and every binary reads it. Undecided on 2026-11-08: its waits
  are spent, since NIT-004 is `implemented` and the encoding and counter durability shipped. It still names a format,
  so it needs lifting to the invariant it serves before it is weighed.
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
  in force. Undecided on 2026-10-25: NIT-005 M1 landed, and its tracked hooks directory test exercises the fix the
  proposal loop did not re-check.
- 2026-10-24: ADR candidate (departs from the past): a fence mark blocks the broken holder's own claim, narrowing
  NIT-001's "a tombstone never blocks a claim". Waits on this proposal reaching `accepted`. Declined on 2026-10-24:
  on recheck it fails Architecture, since it shapes only the claim path, and NIT-001's "Superseded in part" marker
  records it where a reader of that rule looks.
- 2026-10-24: NIT-008's review settled that the hook honors a recall. A sub-lease past its in-force deadline is not live
  for the ownership rule, and the hook has a refusal kind for a recalled sub-lessee that quotes the recall and names no
  command. Before, the rule knew no recall. NIT-008 M2 builds it.
- 2026-10-24: Revisit-hook audit for "Local fencing for a `user` lease": act. NIT-004 settled "Machine leases and
  user leases on the same path": a `user` claim made from a checkout takes or reuses an ordinary `machine` anchor
  lease there. The local hook judges that anchor like any `machine` lease, and never judges broker leases. The
  decision said the question was deferred until NIT-004 answered it. The hook line was removed.
- 2026-10-24: Scheduled as NIT-005 M1, NIT-005 M2, and NIT-005 M3 in queue.md. M1 needs no broker and lands first.
- 2026-10-25 (run best guess): The git hook calls `nitipan hook reference-transaction <state>` and reads git's input.
  It exits 0 to pass, 3 to refuse, and 1 for any failure that fails closed. The script reads exit 2, which an older
  nitipan returns for an unknown `hook` subcommand, as "too old". The text said only "a read-only `hook` subcommand".
  If the name changes, the script and the subcommand change together.
- 2026-10-25 (run best guess): Fence marks are stamped 1.3, the lowest version that needs them, as 1.2 was for
  sub-leases. `CurrentMinor` moves to 3 so this binary may rewrite them, and `list --json` moves to schema 1.3 with a
  `marks` array. Test fixtures that stood for "a record of a newer minor" moved from 1.3 to 1.4.
- 2026-10-25 (run best guess): A mark has its own ULID and records the broken lease's ID. `break <mark-id>` needs no
  `--force`, since a mark is not a lease and lifting it is the user's act. The holder a mark guards against cannot
  remove it, and gets exit 6, so the fence cannot be undone by the session it guards against. A mark nitipan cannot use
  needs `--force`, as an unusable tombstone does.
- 2026-10-25 (run best guess): Marks cover path keys only, since a resource has no checkout for a ref update to collide
  in. A key that another holder's live record already covers is left out when the mark is written, and a mark with no
  key left is not written. A revoked sub-lease, and a sub-lease whose keys return to a live parent, therefore leave
  none.
- 2026-10-25 (run best guess): HEAD counts added commits only, and removed commits count for `refs/heads/*` only.
  Counting removed commits for HEAD would refuse a checkout of a branch from a detached commit. "Touched set for a ref
  update" says HEAD gets "the commit rule only".
- 2026-10-25 (run best guess): Touched paths map against the worktree that has the branch checked out, else the worktree
  the hook runs in. A bare repository has no worktree, so its updates pass.
- 2026-10-25 (run best guess): A mark refusing `acquire`, `--replace`, or `request` exits 7, "broken", and cannot be
  waited out. If it should exit 12 instead, the refusal and the exit table change.
- 2026-10-25 (run best guess): A mark written when a tombstone is delivered, because an older binary did the break,
  takes its keys from the tombstone's absolute `paths`, with default key flags. A tombstone with a `kind` or a `cause`,
  and a resource name, gives none. A `list` that delivers the tombstone writes the mark too.
- 2026-10-25 (run best guess): NIT-001's "Breaking a live lease" takes its "Superseded in part by NIT-005" marker with
  M1, though "The fence mark" says "when this proposal ships". The narrowing is live once M1 merges. That section
  said "a tombstone never blocks a claim". It still holds for every holder but the broken one. The mark now refuses the
  broken holder's own `acquire`, `--replace`, and `request` over the broken keys, until another holder's live record
  covers them, the holder dies, or a user removes the mark. NIT-001 is implemented and frozen, so this entry carries the
  account and the marker points here.
- 2026-10-25 (run best guess): A lease record nitipan cannot use covers a touched path by its raw footprint when that
  decodes, and covers every path otherwise. A live record of another holder wins over it in the message. An unusable
  mark whose keys decode refuses those keys for any agent caller. One whose keys do not decode is reported as a warning
  and ignored.
- 2026-10-25 (run best guess): M1 installs only `reference-transaction`. The pre-push hook belongs to M3, and the
  `acquire` check will then need both.
- 2026-10-25: Amended "Touched set for a ref update". Probing git 2.50.1 showed that `git pack-refs --prune` and
  `git gc` send a transaction that creates every packed ref, then delete each loose ref, and that a ref update can be
  symbolic. The text had no rule for any of them, so `gc` would have refused a branch with commits on another holder's
  keys. A line whose new value is the value the ref holds passes, as does a symbolic update. A deletion passes when
  `packed-refs` holds the ref at the same value and `packed-refs.lock` is absent, since a real deletion of a packed ref
  holds the lock. It looked at old values only through `rev-parse`, as before.
- 2026-10-25: Amended "Refused mid-task". The refusal printed "each refused ref's new SHA", and a deletion has none. It
  prints a pin only when the update has a new commit. The refused ref stays where it was.
- 2026-10-25: Amended "The fence mark". It said a parent's `grant` to the marked holder "may lift" the mark. The grant's
  parent holds a live record over the granted keys, so the rule that lifts a key when another holder's live record
  covers it already does it, on the grant's own locked call. `grant` has no code for it. Acquire lifts eagerly after
  it writes, and every locked call lifts and reaps in its repairs.
- 2026-10-25: Amended M1. The channel check said `branch -f` on a branch checked out in a sibling worktree. git refuses
  `branch -f` on a checked-out branch, so that channel never reaches the hook. The check now says `branch -f` off a
  branch's unique commits, which the removed-commit rule catches.
- 2026-10-25 (run best guess): `acquire` checks the hooks on every call, a refresh included, through a git runner that
  `Deps` injects. A failure is a plain error with exit 1 that names `nitipan setup git --install <checkout>`, not a
  session gate with the setup text.
- 2026-10-25 (run best guess): The hook script bakes the `nitipan` that `PATH` finds when it resolves to the running
  binary, and the resolved executable otherwise. The script falls back to `command -v nitipan`.
- 2026-10-25 (run best guess): An existing `reference-transaction` hook is kept beside nitipan's as
  `reference-transaction.nitipan-orig`. A tracked hooks directory gets nitipan's hooks under `<git-dir>/nitipan-hooks`,
  and the checkout's previous local `core.hooksPath` is recorded in `prior-hooks-path` there. `setup git` takes the
  checkout as the flag's value, and with neither flag it prints what the hook does.
- 2026-10-25: Amended "The fence mark" and its delivery rule, after review. Delivery wrote a mark for any tombstone with
  no mark beside it, so `break <mark-id>` before delivery, or a lift followed by a release or a death, brought the mark
  back. A tombstone that a mark-aware break writes now carries `fenced: true`, and delivery writes a mark only for a
  tombstone without it, which an older binary wrote. A reader that does not know the field delivers and removes the
  tombstone as before, so the field makes no old reader more permissive. The tombstone keeps version 1.0.
- 2026-10-25: Amended "Installing the git hooks", after review. A re-install over a tracked hooks directory chained
  every hook to `unset/<name>` when the local `core.hooksPath` had been unset and another scope, such as a global
  relative path, named the directory. The install now asks git for the `core.hooksPath` values with their scopes,
  drops nitipan's own, and uses the last one as written.
- 2026-10-25 (run best guess): A hooks directory outside the repository's git directory counts as shared or tracked,
  whether or not git tracks files in it. The install writes there nothing, uses the nitipan directory under the git
  directory, and chains. A global `core.hooksPath` such as `~/.config/git/hooks` would otherwise take nitipan's script
  for every repository, and one `--uninstall` would remove it for all. The tracked-files test is gone. An untracked
  directory inside the worktree, such as husky 9's generated `.husky/_`, is chained too.
- 2026-10-25 (run best guess): The hook opens the state directory for reading. An absent directory reads as empty and
  is not created, and a denied mkdir no longer fails closed on every judged update. The format marker gates writes only
  and the hook never takes the lock, so a newer marker never stopped it. A directory owned by another user, and
  records the hook cannot read, still fail closed.
- 2026-10-25 (run best guess): The acquire check asks git before the lock is taken, and judges the answer after begin,
  so a broken holder still hears its tombstone. It asks once for each directory a claim names. Only "not a git
  repository" and a missing git binary count as outside a checkout. Any other failure, such as an unsafe directory or a
  timeout, refuses the claim with exit 1 and says what failed. This replaces the earlier guess that a failure to ask git
  means outside.
- 2026-10-25: Cost of the hook on a long rebase, for the risk "The hook runs on every branch and HEAD update". A rebase
  of 60 commits took 0.37 s without the hook and 7.0 s with it, and 9.3 s with an opencode session lease that runs
  `sqlite3` on each update. The script's fast path, which starts no program for a ref nitipan does not judge, cut 10 s
  to 7 s. Per replayed commit that is about 110 ms. The rebase ran the hook about 800 times, and 61 of those reached
  nitipan at about 40 ms each, since the other runs end in the shell script. With an opencode session-kind lease
  whose host died mid-turn, which runs `sqlite3` on each judged update, the median was 9.3 s. A Claude Code lease costs
  nothing extra beyond reading the records. `NITIPAN_MEASURE_REBASE=1 go test ./internal/cli -run MeasureTheHook -v`
  repeats it. The risk's low-impact rating is left unchanged for the user to review.
- 2026-10-25: Amended "Installing the git hooks", after row review. The chain stubs quote the directory they chain to,
  and the shell does not expand a `~` inside quotes. A `core.hooksPath` of `~/.config/git/hooks` was read as written, so
  every hook in it was skipped silently, and a local `set ~/...` replayed from `prior-hooks-path` did the same. The
  install now reads the values with `--type=path`, and expands a replayed value the same way. The recorded local value
  stays raw, so an uninstall restores the user's own text. The section's re-install bullet text was added in the earlier
  fix and never stated, so it now says how the chained directory is found.
- 2026-10-25: Amended "The fence mark", after row review. The break published its `fenced` tombstone and then wrote the
  mark. A mark that failed to write left the tombstone with the lease standing, so the holder was told its lease broke,
  and a retry wrote a second tombstone. The text said only "in the same locked call as the tombstone". The break now
  writes the mark first and then the tombstone, and takes the mark back when the tombstone fails. Both exist before the
  lease is removed, so the lock-free reader, which reads records, then tombstones, then marks, still finds the broken
  holder refused by one of them at every point after the lease is gone.
- 2026-10-25 (run best guess): Every git call that nitipan classifies by stderr, or that runs from the hook, sets
  `LC_ALL=C` and `LANGUAGE=C`. "Not a git repository" is matched in English, and a translated git would turn a claim
  outside a checkout into a refusal. Whether this git ships translations could not be checked here, so the guard is
  unconditional. Nothing else about git's environment changes, and the hook runner keeps `GIT_DIR`.
- 2026-10-25: ADR candidate (departs from the past): a new kind of state-directory file that older binaries never scan
  may leave stated mixed-binary gaps instead of a `format` bump. ADR-04's minor rule says an old reader is never more
  permissive, and the fence mark's two gaps make older `acquire` and `break` more permissive. Undecided. Found in
  NIT-005 M1.
- 2026-10-29 (run best guess): Amended "Counter durability" and M2's check on a reported token, with the margin and
  the verdicts. The text named a margin without a value. It said the broker "raises its counter above that token",
  and the broker now sets the counter to the token, so the next grant issues above it. It said a token above the bound
  "marks its fence ref unusable", and M2's check said the same. The margin is 24 hours, a named
  broker constant. It is wide enough to cover clock skew between the broker and a pusher's remote, and narrow enough
  that a value past it cannot be a restored counter. If it is wrong, one constant changes, and no stored data does.
- 2026-10-29 (run best guess): "Marks the ref unusable" is the `unusable` verdict in the report's reply, not broker
  state. The broker knows no fence refs, and none exist before the push wrapper. The reporter acts on the verdict.
  If the broker should remember reported tokens, it needs a new field in its store, which a later proposal can add.
- 2026-10-29 (run best guess): The report has no user-facing command in M2. The push wrapper is its only caller, and a
  command with no caller would be surface nobody asked for. The CLI function that sends it is tested end to end through
  the node and the broker. If a command is wanted earlier, it is a thin wrapper over that function.
- 2026-10-29 (run best guess): The report keeps the current request version. A new request kind is refused by an
  older node through the presence record's kinds, as `KindSince` and NIT-004's "Version skew" already say. An older
  broker answers 404 and the CLI reports it as too old. No older binary could mishandle it silently.
- 2026-10-29 (run best guess): The sidecar's file version moves to 1.1 for its own writes only. The deadline, in-flight,
  and orphan files keep 1.0, since they gained no field. NIT-004 says a new field on a request that a node could drop
  takes a new request version. This field is not one: the token rides in the broker's reply, which the node forwards
  whole, and the CLI writes it into the sidecar from that reply. A node from before this change therefore cannot drop
  it on a grant or a renew that goes through the CLI. An older CLI that rewrites a sidecar, as it does to clear the
  anchor's created flag, drops the token, and the next renewal reply puts it back through the node. A missing token
  reads as no token. If this is wrong, the request version moves to 3 and `KindSince` gains an entry.
- 2026-10-29 (run best guess): The node also records the token from renewal replies, which the plan did not list. The
  CLI repair covers only renewals the CLI asks for, and an older CLI can drop the token at any time. Without the node
  repair, `nitipan push` would refuse a live lease for lack of a token until the holder ran `renew` by hand.
- 2026-10-29 (run best guess): The broker takes its wall clock once per claim. The reply token and the committed
  counter come from that one value, so the dry-run prediction the earlier code made is gone. A wall clock that steps
  back cannot lower a token, since the counter term wins.
- 2026-10-29 (run best guess): No floor at load, as the text of NIT-004's "Broker lifecycle and store" left open. Every
  grant floors at the clock, so a floor at load adds nothing a grant does not add. A broker restored from an older
  file and left idle issues nothing, so no stale token can pass in that time. If a floor at load is wanted, it is one
  `max` at start.
- 2026-10-29 (run best guess): A claim fails closed with a 503 when the stored counter is at its 64-bit maximum.
  Adding one would wrap to zero and issue a token below every earlier one. Only a damaged or hand-edited store holds
  such a counter, since the report path caps it at the clock plus the margin. The broker does not validate the counter
  at load, so a bad store still starts and still serves renewals.
- 2026-10-29: ADR candidate (cuts across the system): a fence token never falls below the broker's wall clock, and a
  restored or reinstalled broker never issues below a token already on a remote. The invariant is the floor and the
  bounded raise, not the margin value. M3 only reads tokens, so nothing the invariant rests on is still in design.
  Undecided. Found in NIT-005 M2.
- 2026-11-08 (run best guess): The fence ref name encoding, which "Carrying the token" freezes without stating. One
  component. A byte outside `[a-z0-9._-]` becomes `%XX` with uppercase hex, uppercase letters included, so two names
  never differ only in case on a filesystem that folds it. A dot git would reject is escaped too: a leading or
  trailing dot, the first dot of `..`, and the dot of a `.lock` suffix. The repo root is `%00`, which no path can
  contain. A name that does not round-trip through decode and encode does not decode. A path whose encoding passes 255
  bytes is refused, since a loose ref component cannot be longer. If the encoding is wrong, only the encoder changes
  before a remote holds a ref.
- 2026-11-08 (run best guess): Fence commits use the fixed identity `nitipan <nitipan@invalid>` and the clone's clock
  for their time. "Carrying the token" says the fence names no holder, machine, or user. The identity is set in the
  environment of `commit-tree`, so a user's git configuration never reaches a remote.
- 2026-11-08 (run best guess): A push that loses a compare-and-swap is tried at most 5 times, then reports contention
  with its own exit status. "Token scope" says "a bounded number of times". A loss is recognized by asking the remote
  whether a fence moved, not by git's wording, which differs between hosts and versions. If 5 is wrong, one constant
  changes.
- 2026-11-08 (run best guess): The pre-push hook may write handoff request files and fetch the fence refs into the
  clone's `refs/nitipan/fence/` namespace. "Local fencing for NIT-001" makes only the reference-transaction hook
  read-only. Rule 6 and the lease check ask the broker through the node, and the fetch gives the hook the fence objects
  to read. A fetch prunes local fence refs the remote lost, so a later `--mirror` cannot bring back a deleted fence. The
  namespace is shared by every remote of the clone. The hook and the wrapper read the objects by the SHA the remote
  advertised, so a fetch from another remote does not change what they decide.
- 2026-11-08 (run best guess): A remote that cannot take an atomic push is recorded in the clone's local git config as
  `nitipan.<remote>.atomic = false`. "What enforces the token" names a pre-push lease check and does not say how the
  hook knows the host lacks atomic push. Git's own refusal of `--atomic` is the detection, and the key is the remote's
  name. A remote named by URL cannot be recorded, so the push says to add it as a remote. A hand-set flag falls under
  the accepted risk "Hooks are bypassable". The hook accepts a fence-less push only to a marked remote, after its own
  synchronous renewal of the pusher's leases that cover the touched paths.
- 2026-11-08 (run best guess): An advertised commit that the clone does not hold is left out of the remote baseline, which
  only widens the touched set. "Touched set for a ref update" fails closed only for a missing old or new value, and the
  error says to fetch. A stale holder must therefore fetch before its push is judged. A shallow clone cannot derive
  root commits, so an agent in one is refused once fence refs or its broker leases make the roots matter.
- 2026-11-08 (run best guess): "Could not verify" is exit status 12 and contention is 13. Refusals keep status 3, as the
  local hook does. An unknown or stale answer in rule 6, an `outcome unknown`, and an unreachable broker are all "could
  not verify", per "Counter durability" and rule 6. Rule 6 counts a live broker grant and a mirrored machine lease of
  another holder as live, and skips the pusher's own by holder key.
- 2026-11-08 (run best guess): The pusher's token for a touched path is the highest token among its leases that cover the
  path, and the token written to a fence ref is the highest among its keys that the ref lies at or below. "Token scope"
  says "the pusher's token" and a holder can hold several leases. A lease that covers the path and has no token refuses.
  Equal tokens do not refuse, so a holder's second push goes through. A fence of a newer major in the check set refuses
  a pusher with no lease too, since it cannot be read.
- 2026-11-08 (run best guess): `nitipan push` takes branch names, `<src>:<dst>`, `:<dst>` to delete, and a leading `+` on a
  branch. It takes no pattern and no other git flag, and it defaults to the current branch. A destination without
  `refs/` is a branch. Tags and other refs go through unfenced. "Carrying the token" forbids `+` and `-f` on fence
  refs only.
- 2026-11-08 (run best guess): `nitipan fence delete` takes a fence ref name, `<root>/<encoded-path>`, or a repo name. It
  reaches the pre-push hook as the user's deletion through the environment variable `NITIPAN_FENCE_DELETE`, which names
  the one ref. The hook allows a deletion only for a caller with no agent variables, and only of that ref. The
  settled text says the hook refuses a fence deletion "whoever makes it" and that `fence delete` is the one way to
  delete, and does not say how the hook tells the two apart. A user who sets the variable by hand can delete a fence
  with `git push`, which is the user's own terminal and the accepted risk.
- 2026-11-08: Amended "Tokenless pushers and fence cleanup", after review of the first amendment. The settled text said
  a deletion of a fence ref is refused, "which covers `--mirror`". git does not hand the refs a `--mirror` or
  `--prune` push deletes to the pre-push hook. A probe of git 2.50.1 showed only the refs the push updates. The first
  amendment had the hook look for `--mirror` and `--prune` in the process tree. Review showed that git accepts
  `--mir`, and that an alias or `-c remote.<name>.mirror=true` hides the flag, so process matching cannot be
  exhaustive. The text now says the hook refuses any push to a remote whose fence refs the clone lacks or holds at
  another value, whoever pushes. Its own fetch then fills the namespace, so the retry passes. A user's first plain push
  to a remote with fence refs is therefore refused once. `nitipan push` records the fence refs it writes in the local
  namespace, and `fence delete` syncs first, so neither trips the guard. The script no longer has a fast path for
  pushes of tags or of nothing: it starts nitipan on every push, since no input shows that a push deletes nothing.
  A missing binary now fails closed for every push, which the plan's fast path did not. One window stays and is
  accepted. Another machine can create a fence ref after the hook's `ls-remote` and before the pruning push lands, and
  the push deletes it. The hook reads the remote once, and only a server-side hook closes the gap.
- 2026-11-08: Amended "Installing the git hooks". Its bullet on a missing `nitipan` said "the hook fails closed for
  `refs/heads/*` and HEAD updates and passes every other ref, such as fetches, tags, and salvage pins. The script tells
  them apart without the binary." That holds for the reference-transaction hook and still does. The pre-push hook can
  no longer tell a harmless push apart without the binary, because git does not list the refs a pruning push deletes
  and the hook's sync guard needs the remote's fence refs. Its script starts nitipan for every push, and with no
  binary it fails every push closed. The bullet now states both hooks. M1's check on a missing binary describes the
  reference-transaction hook and is unchanged. The risk "The hook runs on every branch and HEAD update" is about that
  hook too, and is unchanged.
- 2026-11-08: Amended "Tokenless pushers and fence cleanup" with the rule that an agent's push carries only the fence
  refs the plan calls for. The text let a caller create or fast-forward any well-formed fence ref, and the hook only
  checked that the planned writes were present. An agent could push `<root>/%00` with a huge token and lock out every
  other holder until the user deletes it. The hook now refuses a carried fence ref that is not in the plan, and one
  whose token differs from the plan's. It runs even when the push has no branch update. The user's own terminal passes
  with no fence update, as the settled text has it, so it may still create a well-formed ref by hand.
- 2026-11-08 (run best guess): A fence is never written at a lower token than the one it replaces. The plan wrote a ref at
  the token of the pusher's key that the ref lies at or below, while the check used the highest token over the touched
  path. A holder of `/repo` at 5 and `/repo/src` at 20, with a stored `/repo` fence at 15, passed the check and then
  lowered the fence to 5. A ref whose stored token is higher is refreshed and keeps it. The engine table has the case.
- 2026-11-08 (run best guess): A key with no valid fence ref name, such as an encoded path over 255 bytes, refuses the push
  with its own message. The plan had dropped the error, so the holder pushed unfenced.
- 2026-11-08 (run best guess): Key containment in the plan uses ADR-03's one rule, `keys.Contains`, with the entry's
  FoldCase and Normalize, which the sidecar entry carries and the plan key now copies. A fence ref records no
  volume rules, so a fence key is compared with the pusher's own rules, folded when any of the pusher's keys under the
  same repo folds. The more insensitive reading only adds names to the check set, as ADR-03 says of keys on different
  volumes. Two pushers on different volumes can therefore disagree on a case-variant path, and the fence errs toward
  refusing.
- 2026-11-08 (run best guess): `fence delete` maps the root of a `repo:` name to the first-parent root of the clone's HEAD
  when the typed root is one of HEAD's roots, since fence refs are named by the first-parent root. When the ref is not
  found, the error lists the fence refs of the same path under other roots.
- 2026-11-08: Amended Impacts for NIT-001. It said the "Superseded in part by NIT-005" marker is added "when this
  proposal ships". The marker landed with M1, per the 2026-10-25 entry on it.
- 2026-11-08 (run best guess): The host probe is `scripts/live-checks/fence-hosts.py`, run by `make live-fence-hosts`. It
  is never part of `ci`. It sends the push shape of `nitipan push`, a probe branch and a fence ref in one `--atomic`
  push with `--force-with-lease`. It then fast-forwards the fence and deletes both. On a host with no atomic push it
  still tries the fence ref without `--atomic`, to learn whether the host's ref policy accepts the namespace. The
  coordinator runs it against the user's remotes with their credentials, since this run tested it only against
  local bare remotes.
- 2026-11-08: ADR candidate (hard to reverse), update: the fence format shipped as the Decision Log of 2026-10-24
  described it, with the encoding in the 2026-11-08 entry above. A test round-trips and case-folds the encoding, and
  another runs versions 1.0, 1.1, and 2.0 against one remote. The candidate stays undecided until the end of the run.
  Found in NIT-005 M3.
- 2026-11-08: Amended the risk "The user's remote hosts may not support atomic push". It was rated likelihood high
  because github.com's behavior was unconfirmed. `make live-fence-hosts` ran over SSH against the user's scratch repo
  on github.com, gitlab.com, git.sr.ht, and a tangled knot. Each advertised atomic push, and each took a fence ref's
  create, fast-forward, and delete. The likelihood is now low. huggingface.co was not probed, and Azure DevOps still
  lacks atomic push, so the risk stays.
- 2026-11-08: Amended "Tokenless pushers and fence cleanup" on the agent with no matching ancestor. The text said such a
  caller "fails closed, as NIT-001 does locally". NIT-001's local hook fails closed for branch and HEAD updates only.
  The pre-push hook returns before the refusal when a push has no branch update and carries no fence ref, so a tag-only
  push by an agent that cannot be identified passes. The text now says so. The decision carries one marker, which
  covers this amendment and the three before it.
- 2026-11-08 (run best guess): `nitipan push` and `fence delete` run git with the hook's 2 minute bound, not the 10
  seconds of the cleaned gate. Cleaning the environment changed the bound too, so `ls-remote`, the fence `fetch`, and
  the touched-set `rev-list` and `diff-tree` failed as "could not verify" on a slow SSH round trip or a long first push,
  where the hook passed. No document fixes the value. The hook's bound keeps the wrapper and the hook agreeing on what
  a slow remote is. `ExecClean` keeps 10 seconds for setup and the acquire gate. The push itself has no bound.

## References

- `spec/reports/2026-10-18-lease-broker-research.md`: "Fencing"
- NIT-004: the broker that issues tokens
