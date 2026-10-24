# NIT-003: Sub-Leases

**Status:** accepted
**Created:** 2026-10-18
**Updated:** 2026-10-20

## Dependencies

- NIT-001

## Impacts

- NIT-001 — "Domain keys and overlap", "Claiming", and "Breaking a live lease". A sub-lease is a second lease inside a
  live holder's domain, which NIT-001's overlap rule refuses today
- NIT-001 — "Re-acquiring as the same holder", "Record evolution and unknown records", "A supported read path for
  guardians", "Callers with no adapter", and milestone 3. This proposal needs amendments to each. The Decision Log
  lists them
- NIT-005 — "Token scope" and "Local fencing for NIT-001". "Fencing" here fixes the outcomes they must keep for
  sub-leases
- NIT-008 — "Revocation is immediate", "Breaks across the tree", and "Record shape". Recall with a deadline extends
  `revoke` and its tombstone

## Summary

A guardian holds a domain. It can delegate part of that domain to another session through a sub-lease. The other
session then works in that part while the guardian keeps the rest.

## Motivation

A guardian that owns a whole checkout may want another session to take on one directory. Under NIT-001, that session's
claim overlaps the guardian's lease and is refused. The guardian would have to release its whole domain and claim the
rest piece by piece. Overlaps would go unchecked between those steps.

## Design Decisions (Settled)

These come from the roadmap the user approved on 2026-10-18, recorded in the research write-up.

### Delegation by the holder

A sub-lease is granted by the session that holds the parent lease. No other session can carve a sub-lease out of a
domain it does not hold.

### No nesting

A sub-lessee cannot delegate again. Nobody needs it, and it would make cascading revocation and tombstone fan-out
required. A sub-lease record that names its parent leaves room for a later proposal to allow nesting.

### Revocation is immediate

When a sub-lease is revoked, it ends at once and its holder gets a tombstone. A gentler recall with a deadline needs a
timer, and nitipan has no process to run one until NIT-004's broker. NIT-008 adds recall on top of that broker.

### Record shape

The question was where a sub-lease lives on disk. The candidates were a separate lease record that names its parent, a
delegation list on the parent's record, both together, and a separate record with the parent's footprint carved down
to exclude the delegated keys.

Decision (autonomous): a sub-lease is its own lease record in `leases/`, with its own holder and a footprint of its own
keys. It adds one field, `parent`, naming the parent lease ID. The parent's record does not change. Its footprint still
covers all its keys.

- The overlap check exempts a sub-lease from its own parent's record, and from no other record
- The children of a lease are found by scanning for records that name it, in the scan `acquire` already does under the
  lock
- A child whose `parent` names no record has a dangling parent. Lease IDs are never reused, so this is detected
  soundly. A new reader treats it the way "Liveness and the parent's death" treats a dead parent, tombstone included
- A V1 binary can release, break, or clear a parent record, since the parent's record stays V1-writable. Its children
  then dangle. The duty to end or reparent them is delayed until the next new-binary call, and it is not lost, because
  each child still holds its sub-lessee's identity
- A sub-lease cannot change its own keys. `acquire --replace` on a record that has `parent` is refused, and so is any
  other scope change. Otherwise the parent exemption would let a sub-lessee widen itself anywhere inside the parent
  without a grant
- The parent is exempt from its own children in `--replace` only while the new keys still cover every child's keys. A
  replace that would drop delegated keys is refused, and the refusal lists the sub-leases in the way
- A `--replace` that would absorb or remove a lease with live children is refused, and the refusal lists the children.
  Otherwise the absorbed parent's children would dangle and stand alone inside the rewritten lease
- A V1 binary, or any binary older than a child's minor, never rewrites that child, under NIT-001's writer rules. It
  still treats the child's dead or dangling parent as absent when it checks overlap
- `parent` passes NIT-001's minor-version rule. A reader that ignores it sees two overlapping live records, so it
  refuses at least as much as a new reader does. A sub-lease, a request, and a notice carry record version 1.2. A lease
  without `parent` keeps version 1.0. Amended 2026-10-24; see Decision Log

NIT-001's per-record mechanisms work unchanged on a child: holder match, tombstones, handle collision, release and
break by ID, the holder-wide rewrite, and `list`. NIT-001's `--replace` keeps the lease ID so that parent links like
this one stay valid.

The delegation list was rejected. NIT-001 does not list `release` among the rewrites an old binary refuses on a higher
minor. A V1 parent that runs `release` would delete live delegations with no tombstones, which makes an old reader more
permissive. The field would need a new major, and then every delegating parent would block every V1 claim. The carved
footprint was rejected. A `path` body cannot express "checkout minus subtree", so V1 readers would find the footprint
and body disagreeing and refuse every claim. Keeping both a link and a child list was rejected. The child record must
be authoritative, since it holds the sub-lessee's identity. The parent's list would then be a cache that can lie after
a crash, and it guards against no named failure.

### Overlap with the parent

The question was whether a sub-lease may equal its parent, and whether the parent keeps any right in the delegated part.
The candidates were strictly inside or equal allowed, each with the delegated part exclusive to the sub-lessee or
shared with the parent.

Decision (autonomous): every sub-lease key must be covered by the parent's keys, under NIT-001's one overlap rule with
nested checkouts included. A sub-lease equal to its parent is allowed. That is a full handoff with no window. While a
sub-lease lives, its keys belong to the sub-lessee alone.

- A parent key that overlaps a live child's key, in either direction, is not "covered by your leases". The parent's
  `acquire` on such a key is refused. The error names the sub-lessee and says to coordinate with it or ask the user. A
  parent holding `src` and `docs` that delegated `src/api` and then claims `src` is refused
- The one exception is the parent's equal-key-set refresh. It is exempt from the parent's own children. When live
  delegations exist, it exits with status 11, distinct from plain success, and its output lists them. A caller
  that checks only the exit status is not told it owns everything. Amended 2026-10-24; see Decision Log
- A sub-lessee's claim whose keys are all covered by its own live sub-lease is "covered by your leases". The parent's
  record does not block it, since those keys belong to the sub-lessee alone. A sub-lessee holding `/repo/src` that
  claims `/repo/src/handlers` gets "covered", not a refusal naming the parent
- `list --json` filtered by a delegated path reports the sub-lessee as the owner and the parent as the grantor
- A third party's claim on a delegated key overlaps both records, so it is refused, and the error names both holders
- Two sub-leases of one parent follow the ordinary overlap rule between each other

NIT-001's "Re-acquiring as the same holder" does not say whether the check against other live holders runs before the
"covered by your leases" check. Under NIT-001 alone the order cannot matter. With an exclusive sub-lease it does. A V1
binary that checks coverage first tells the parent a delegated key is covered. NIT-001 must fix the order now, since V1
binaries cannot learn it later. See the Decision Log.

Shared rights were rejected. Subagents share their parent's holder, so a guardian's subagent would get "covered" in the
delegated part and edit the sub-lessee's files. `list` would also name two owners, which breaks "who owns this path"
for guardians. Equal plus shared is NIT-002's counted slot under another name. Strictly inside was rejected as a rule
that buys nothing. Sibling sub-leases could cover the whole parent together unless a check over their union were added,
and no failure that check prevents can be named.

### Invocation and consent

The question was how a grant reaches the sub-lessee. The candidates were a grant bound to the sub-lessee's handle, a
single-use secret the sub-lessee presents, a request the parent approves, and an offer ID any session can redeem.

Decision (autonomous): the sub-lessee requests, and the parent grants. The grant binds the requester's exact holder
identity.

- `nitipan request <lease-id>` takes the same domain arguments and advertisement flags as `acquire`. It writes a
  request record holding the requester's full holder identity, the parent lease ID, the requested keys, and the
  advertisement fields. The granted sub-lease takes its description, name, and permission mode from the request. It
  prints the request ID and the parent's handle. The requester then asks the parent out of band, such as through
  SendMessage
- A refused `acquire` names `nitipan request` as the way to ask for part of a blocking lease only for the refused keys
  that one blocking lease covers, when that lease has no `parent` and none of the blockers is a live child of it. That is a command the caller itself runs. A sub-lease
  cannot delegate, so a `request` against one is refused, and an error never suggests it. Neither does an error to the
  blocker's own holder
- `request` refuses keys that the named lease does not cover, and keys that a live child of it already covers or
  overlaps, since `grant` would always refuse them. The error names the sub-lessee where there is one
- `request` also refuses a resource lease, since a slot has no part to hand over, a lease of the caller's own, and a
  lease whose holder is dead. Amended 2026-10-24; see Decision Log
- `nitipan grant <request-id>` turns the request into a sub-lease record under the lock. The sub-lease's lease ID is
  the request ID. Its output names the requester's handle and agent name, so the parent sees whom it granted to
- A grant first rewrites the request to a `granting` state through `rename(2)`. It then publishes the sub-lease, writes
  the "request granted" notice with an ID derived from the request ID, and removes the request. A positive notice never
  precedes the fact it announces
- A request in the `granting` state is never granted again, so a lease ID is never reused. A lock holder that finds
  one handles it before any other reaping of requests. When a lease carries its ID, the lock holder writes the granted
  notice if it is missing, then removes the request. When none does, the grant was interrupted, or a V1 binary removed
  the lease before the repair. The lock holder removes the request and leaves a "request lapsed" notice saying to
  request again. It never says "declined" for such a request
- Under the lock, `grant` rechecks that the caller still holds the parent, that the keys are still covered by the
  parent, that no other live lease overlaps them, that the requester is alive, and that the requester is not the
  parent's own holder. Subagents share their parent's holder, so a grant to self is refused
- `nitipan decline <request-id>` removes a request. The parent runs it to refuse, and the requester runs it to withdraw
- The requester learns of a grant or a refusal on its next nitipan call, through a notice. See "Liveness and the
  parent's death". It can also re-run `acquire` on the same keys, which succeeds as an equal-key-set refresh once
  granted
- Requests live in `requests/<id>.json`, published the way tombstones are. They use the shared envelope and its
  versioning, with footprint `none`. V1 binaries never scan that directory, so a request cannot block a V1 claim. A lock holder reaps a request whose requester is dead. It also reaps a request whose
  parent lease is gone, and leaves the requester a "request declined" notice

The handle binding was rejected. A session that holds no lease has no record, so its handle carries no collision
suffix, and two such sessions can compute the same handle. It also needs a new `whoami` command. The secret was
rejected. The parent would deliver it by agent name, which NIT-001 never forces unique, so it can reach the wrong
session. Under a request, a misrouted message carries only a request ID, and the grant stays bound to the recorded
identity. The offer ID was rejected, since `list` shows it to every session.

The parent must act on each request. A Claude Code parent is woken by SendMessage. A Codex or opencode parent may not
be wakeable until NIT-007. See Risks.

### Liveness and the parent's death

The question was what happens to a live sub-lease when its parent's holder dies, or its parent record is gone. The
candidates were ending the sub-lease with its parent, and keeping it as an ordinary lease, either rewritten without its
link or left with the link forever.

Decision (autonomous): the sub-lease survives and becomes an ordinary lease. A dead parent and a dangling parent are
handled the same way.

- The next new-binary lock holder that sees a child whose parent is dead or dangling first writes the "parent ended"
  notice, then rewrites the child without `parent`, through `rename(2)`. The notice's ID is derived from the child's
  lease ID and the cause, so a retry after a crash finds it already published. The rewritten record's `version` drops
  to the lowest minor its remaining fields need, so a V1 binary can rewrite it again
- The notice says how the parent ended when the call that ended it knows. A repair that finds the parent already gone
  cannot tell a release from a break or from an older binary's removal, and the notice says so. A sub-lease whose own
  holder is dead is left alone, since clearing it removes it. Amended 2026-10-24; see Decision Log
- A binary older than the child's minor does not rewrite it, and does not write its notice. Only a binary that can
  rewrite the child writes the notice, so the notice is written once, by the call that repairs the child
- The parent's dead record is cleared as NIT-001 already clears dead leases. Its remaining keys become claimable around
  the child. The child's keys stay blocked, since the child was already exclusive
- The parent's record keeps `clear_when_dead` true. A V1 reader that clears a dead parent reaches the same end state,
  minus the rewrite and the notice

Notices live in `notices/<id>.json`, published the way tombstones are, with the shared envelope and footprint `none`.
A notice that reports a loss, such as a parent or sub-lease ending, is published before the write that causes it. The
overlap check still guards the keys, so an early loss notice is harmless. A grant notice comes after the grant. Every
notice's ID is derived from its target's ID and its cause, as `<target-id>-<cause>`. A retry after a crash finds it
already published, and the holder is told once. Amended 2026-10-24; see Decision Log. A notice is addressed to a
holder. It carries a cause, and how that cause came about:

- Parent ended, sent to a sub-lessee: the parent's holder died, released, or was broken by a named caller, or the parent
  is gone with no record of how
- Sub-lease ended, sent to a parent: its sub-lessee released, died, or was broken by a named caller. A revoke sends none,
  since the parent did it
- Request granted, sent to a requester
- Request declined, sent to a requester: the parent declined, or the parent lease ended and the request was reaped
- Request lapsed, sent to a requester: a grant was interrupted and did not leave a lease. The requester may request
  again

The holder's next nitipan call reports a notice and removes it. Delivery happens only under the lock, after that call's
repairs of dangling children and granted requests. A `list` that reads without the lock delivers no notices, as it
skips tombstone cleanup. A lock holder reaps a notice whose holder is dead.
A loss notice that is published before its write waits for that write. Delivery leaves a "parent ended" notice in place
while its sub-lease still names a live parent, and a "sub-lease ended" notice while the sub-lease still exists. A retry
after a crash then replaces the notice with its own account, and the addressee is told once. Delivery drops a "parent
ended" notice whose sub-lease is gone, and a "sub-lease ended" notice whose addressee holds no lease covering the keys.
Amended 2026-10-24; see Decision Log.
Subagents share their parent's holder, so a subagent's call can consume a notice before the main session sees it. The
same is true of tombstones. Notices do not go in `tombstones/`. NIT-001's `release` reports an ID
with an undelivered tombstone as "broken", so a V1 `release` would misreport a live lease. V1 binaries never scan
`notices/`.

Ending the sub-lease with its parent was rejected. A guardian that exits would free a live sub-lessee's keys while it
is still editing them, and a third party could then claim them. No automatic path in NIT-001 removes a live lease. It
would also turn the settled full handoff into a loan that ends when the guardian leaves, which is the usual reason to
hand off. And it is a duty a V1 reader cannot perform, which NIT-001 says needs `clear_when_dead` false on the parent,
against "Record shape". Leaving the link forever was rejected. It makes a child that names a record that will never
exist again, and such a child can never `--replace`.

NIT-001's "Record evolution and unknown records" gives "ending sub-leases" as its example of a duty an old reader cannot
perform. That example is now stale. See the Decision Log.

### Display

The question was how `list` and `list --json` show a parent, its sub-leases, and pending requests. The candidates were
sub-leases nested inside their parent's entry, flat entries with computed annotations, and flat entries with only the
stored `parent` link.

Decision (autonomous): every lease stays a top-level entry, with computed annotations. The `list --json` change is an
additive minor bump of its schema, from 1.1 to 1.2. Amended 2026-10-24; see Decision Log.

- A child carries `parent`, its parent's lease ID
- A parent carries a computed `delegated` list of its live children's IDs and keys. It is computed at read time, never
  stored in the parent's record
- A filtered query marks each match. A lease is `owner` when some part of the query that overlaps its keys is held by
  none of its live children. The part is the deeper of the query key and the lease key, and a child holds it when one of
  the child's keys contains it. Testing one child key at a time is the same as testing the union, since a part is one
  path and sibling subtrees are disjoint. A parent is also `grantor_of` the children whose keys match. A parent holding `/repo` with
  `/repo/src` delegated, filtered by `/repo`, is `owner` and `grantor_of` the child. Filtered by `/repo/src`, it is only
  `grantor_of`, and the child is `owner`
- An unfiltered `list` sets neither mark, since there is no path to answer for. A resource filter sets neither. The
  parent link and the `delegated` list carry the structure there. A mark that is present is always computed from a
  query, and an absent one never means false
- Pending requests appear in a top-level `requests` array, each with its parent lease ID, requester holder and handle,
  liveness, and keys. A request in the `granting` state is not pending and is left out. A filtered query keeps the
  requests whose keys overlap the filter
- `list` that takes the lock runs the same repairs of requests and sub-leases as any locked call, then delivers the
  caller's notices. It reports each delivered notice in a top-level `notices` array with a `summary`, as tombstones are.
  The array is always present, and empty for a `list` that reads without the lock, which delivers none
- A sub-lease counts as live, and so as delegated, only while its holder and its parent are alive. A child whose parent
  is dead is displayed like one whose parent is dangling
- The human table prints children indented under their parent, and pending requests under the lease they target. A
  request whose lease is not listed prints in a group of its own
- A child whose parent is dangling prints at the top level with no grantor mark, showing its stored `parent` link. That
  includes a `list` that reads without the lock

Requests are visible to every session. That is harmless. Only the parent can grant, only the parent or the requester
can decline, and a grant binds the recorded identity. Seeing a request lets a third party do nothing.

Nesting was rejected. A consumer of the earlier schema that iterates top-level leases would never see the children. It
would report the parent as owner of a delegated path. A V1 binary on PATH also prints flat output, so the shape would
depend on which binary ran. Flat entries with only the link were rejected. "Overlap with the parent" requires the
filter to report owner and grantor, and NIT-001's "A supported read path for guardians" rejected making readers
reimplement overlap in `jq`.

NIT-001 calls the `list --json` schema "versioned", but it does not say what a version bump means to a consumer. It
needs a rule that mirrors the record envelope's minor rule. See the Decision Log.

### Fencing

The question was how NIT-005's fencing tokens relate between a parent and its sub-leases. NIT-003 is local, with no
broker, so it cannot issue a token. NIT-005 has not settled "Token scope" or "Local fencing for NIT-001".

Decision (autonomous): defer the token mechanism to NIT-005. NIT-003 fixes the outcomes NIT-005 must keep.

**Revisit:** when NIT-005 settles "Token scope" and "Local fencing for NIT-001"

- A sub-lease is its own grant. A parent and its child never share a token
- While a sub-lease lives, the parent's token does not pass for the delegated keys
- After a sub-lease ends, the sub-lessee's token is refused for its keys, and some token the parent holds passes there
- Survival under "Liveness and the parent's death" is not an ending. A surviving child keeps its token
- NIT-005's "Local fencing for NIT-001" must cover sub-lease grant and revocation, or say they stay unfenced
- The outcomes must hold when an older binary ends a child, since that binary reissues no token. Otherwise NIT-005
  must state that the parent fails closed, locked out of the returned keys until its next call

These outcomes constrain NIT-005's "Token scope". NIT-005's receiver refuses a token lower than the highest it has seen.
A single high-water mark per name or per repo would refuse the parent's older token on the keys it kept, once the child
commits with a newer one. So the outcomes hold only with per-key high-water marks and a reissue above the child's token,
or with another model that keeps them. A pre-receive hook sees refs, not paths, which NIT-005 must also reconcile.

Sharing the parent's token was rejected. A guardian's subagent would commit in the delegated part with a token the
receiver accepts, and a revoked sub-lessee's token would still pass. Deciding a token scheme now was rejected. It would
settle NIT-005's open "Token scope" from a proposal that cannot issue tokens. Its reissue would also rewrite the
parent's record, which "Record shape" leaves unchanged. Moving the question to NIT-005 with no stated outcomes was
rejected, since nothing would then stop NIT-005 from sharing tokens.

### Breaks across the tree

The question had three parts: how the parent ends its own sub-lease, whether a sub-lessee may break its parent, and
whether a third party's forced break of the parent cascades. The candidates were a `break` exemption for the parent,
plain `break --force`, and a new `revoke` command; a special refusal for a sub-lessee or none; and a cascade or orphaning
the children.

Decision (autonomous): the parent revokes with its own command. Every other break follows NIT-001's rules, and nothing
cascades.

- `nitipan revoke <child-id>` ends a live sub-lease at once. Only the holder of the child's parent lease may run it, and
  it needs no `--force`. Under the lock, it removes the child and writes a tombstone in `tombstones/`, keyed by the
  sub-lessee and naming the parent as the breaker. The tombstone carries a new `cause` field set to `revoked`, and is
  stamped 1.2. Its `kind` field stays empty, since `kind` already means `moved` or `revived`. Old readers ignore the
  cause and deliver it as a break, which passes the minor-version rule. A sub-lessee that is already dead gets no
  tombstone, and a retry after a crash reuses the tombstone it finds. NIT-008 extends this act. Amended 2026-10-24; see
  Decision Log
- Another caller's `revoke` is refused, and the error names the parent. `break --force` by anyone still works as in
  NIT-001
- No error names `revoke`. A refused `acquire` on a delegated key names the sub-lessee and says to coordinate with it or
  ask the user. `revoke` appears in help text only. An agent follows the command its error names, and a guardian's
  subagent that followed `revoke` would cut off a live sub-lessee to edit its files
- Subagents share their parent's holder, so any subagent of the guardian can revoke. `revoke`'s help text says so, as
  `release --all`'s does
- A sub-lessee follows NIT-001's break rules toward its parent like any third party. It needs `--force` for a live
  parent
- A forced break of a parent removes only the parent. Its holder gets a tombstone. The children dangle, and "Liveness
  and the parent's death" handles them in the same call: each is rewritten without `parent`, and each sub-lessee gets a
  "parent ended" notice naming the breaker. The break output lists the children that now stand alone
- A forced break of a child returns its keys to the parent, since the parent's footprint still covers them. The breaker
  cannot then claim those keys. The break output says the keys went back to the parent, and the parent gets a
  "sub-lease ended" notice
- A `revoke` writes the tombstone before it removes the child. Every removal that triggers a loss notice writes the
  notice first

Plain `break --force` was rejected. A routine revoke would look like a hostile break, and NIT-008 would have no distinct
act to extend. The parent would also need `--force` for an act it alone is entitled to. The `break` exemption was
rejected. A V1 binary refuses `break` without `--force` on a higher-minor child, so an agent retrying would add
`--force`. An unknown `revoke` command points at an upgrade instead.
Refusing a sub-lessee's break of its parent was rejected. The sub-lessee can release its own sub-lease and then break
the parent as a third party, and a V1 binary ignores `parent` anyway. A cascade was rejected. NIT-001 says a
destructive action always names its exact target, and "Liveness and the parent's death" already rejected freeing a live
sub-lessee's keys. A V1 break would not cascade, so the result would depend on the binary.

### Release across the tree

The question had two parts: whether the parent may release while sub-leases exist, and what a sub-lessee's early
release does with its keys. The candidates were refusing, cascading, or orphaning the children; and returning the keys
to the parent with or without a notice, or leaving them claimable by anyone.

Decision (autonomous): a parent's release orphans its children. A child's end returns its keys to the parent, and the
parent is told.

- A parent's `release` removes its record. Its undelegated keys become claimable. The children dangle, and "Liveness
  and the parent's death" handles them in the same call. The release output lists the children that now stand alone
- Release ends the grant for good. The parent can no longer revoke the children, and re-acquiring its old domain is
  refused, naming them
- When a child ends by any act other than its parent's `revoke`, its keys return to the parent with no rewrite of the
  parent's record. That covers the sub-lessee's `release`, a forced break of the child, and clearing a dead child. The
  parent gets a "sub-lease ended" notice carrying the child's ID, its keys, and how it ended. A parent that is dead or
  gone gets none, since nobody can receive it. Amended 2026-10-24; see Decision Log
- A V1 `release` of a child reaches the same end state, minus the notice. That rests on NIT-001 saying what `release`
  does on a record of a higher minor

Refusing the parent's release was rejected. A guardian that handed off its whole domain could not release, while
exiting the process or running a V1 `release` reaches the orphan end state anyway. The guard pushes an agent to `revoke`
live sub-lessees just to finish its own release, and `release --all` at session end would fail. A cascade was rejected
for the reasons in "Breaks across the tree", and because `--all` from a subagent would then end every sub-lessee. Leaving
the keys claimable was rejected. It needs the parent's record carved, which "Record shape" rejected, and it would let a
third party gain ground in a live domain without the holder's consent. A silent return was rejected. The parent would
think the area is still delegated and learn otherwise only by retrying `acquire`.

## Design Decisions (Open)

None.

## Risks

- **The overlap exception weakens exclusivity** — likelihood: medium, impact: high.
  A bug in the parent-child exception could let an unrelated session claim inside a live domain. Mitigation: table
  tests cover every overlap case with and without a parent link, and the race test from NIT-001 includes sub-leases.
- **The parent dies and the sub-lessee does not notice** — likelihood: medium, impact: low.
  Neither macOS nor nitipan has a push signal on another process's death. The sub-lease survives, so the sub-lessee's
  work is not cut off. Mitigation: the sub-lessee learns from a "parent ended" notice on its next nitipan call.
- **NIT-003 rests on NIT-001 amendments that NIT-001 now carries** — likelihood: low, impact: high.
  The amendments have landed in NIT-001. Amended 2026-10-24; see Decision Log. The Decision Log lists the eight it
  needed: the order of the other-holders and coverage checks, `release` on a higher minor, the stale `clear_when_dead`
  example, a `list --json` evolution rule, the commands that need an adapter, the milestone 3 carve-out, the "breaker
  can claim at once" text in "Breaking a live lease", and the rationale in "Record evolution and unknown records" about
  a NIT-003 delegation list. The check order and the `release` rule are one-way doors, since V1 binaries cannot learn
  them later. Mitigation: the amendments went to NIT-001's owner on 2026-10-18, and NIT-001 took all eight.
- **Subagents act as their guardian** — likelihood: medium, impact: medium.
  Subagents share the parent's holder. Any subagent of a guardian can grant or revoke, and can consume its notices and
  tombstones. Accepted (autonomous): NIT-001's holder model already accepts that subagents are one holder, and no
  per-subagent identity exists. `revoke`'s help text says so, and no error names `revoke`.
- **The new on-disk and output formats are one-way doors** — likelihood: high, impact: medium.
  The `parent` field, the `requests/` and `notices/` layout, the tombstone `cause`, and the new `list --json` fields
  outlive any binary that writes them. Mitigation: each rides NIT-001's envelope and minor-version rule. `parent` and
  `cause` leave old readers no more permissive. Requests and notices live where V1 never scans, with footprint `none`.
  The `list --json` additions are an additive minor bump.
- **NIT-005 may not keep the fencing outcomes** — likelihood: medium, impact: medium.
  NIT-005 is a draft that depends on NIT-004, and a pre-receive hook sees refs, not paths. Mitigation: "Fencing" lets
  NIT-005 state that sub-leases stay unfenced, so the gap is recorded rather than silent. The revisit hook reopens the
  question when NIT-005 settles.
- **An idle Codex or opencode parent never sees a request** — likelihood: medium, impact: low.
  Only Claude Code is known to wake on SendMessage. Accepted (autonomous): the request waits in `list`, the requester
  can ask the user, and nothing is lost. NIT-007 may add a wake path.

## Milestones

### M1: Records, overlap, and grants

**Status:** DONE

- NIT-001's text carries the amendments this proposal's Decision Log lists
- Table tests cover every overlap case with and without a parent link: parent and child, child and third party,
  siblings, a dead parent, and a dangling parent. NIT-001's race test includes sub-leases
- `request`, `grant`, and `decline` work as settled. `grant` refuses a self-grant, a dead requester, keys outside the
  parent, and keys another live lease overlaps. A `request` against a sub-lease is refused
- A crash after a grant publishes the sub-lease, before or after its notice, leaves one "request granted" notice and no
  pending request once the next lock holder runs, even if the parent released in between. A crash after the `granting`
  rewrite and before the publish, or a V1 removal of the lease before the repair, leaves a "request lapsed" notice, no
  pending request, and no reused lease ID
- `request` refuses keys the named lease does not cover, and keys a live child covers or overlaps
- A claim by any other session on the sub-leased part is refused, and the error names both holders
- The parent's claim on a delegated key is refused and names the sub-lessee, never `revoke`. Its equal-key refresh
  succeeds with its own exit status and lists the delegations
- A sub-lessee's claim inside its own sub-lease reports "covered"
- `--replace` on a sub-lease is refused. A parent `--replace` that drops delegated keys, or absorbs a lease with live
  children, is refused and lists the children
- A V1 binary refuses at least as much as a new one against a parent and child, and never rewrites a child

**As built:**

- NIT-001 already carried the eight amendments. Its Decision Log entry of 2026-10-18, "Took eight amendments", lists
  them, so the first check needed no edit. The Risks entry about NIT-001 not carrying them was stale, and was amended.
  See the Decision Log entry of 2026-10-24.
- The old-binary check used a binary from the run's base commit `f29ad6d`, extracted with `git archive`. Its own test
  harness drove it over a temporary state directory, with a parent, a planted version 1.2 child, and a third party.
  Every claim by the parent, the sub-lessee, and the third party on the delegated or the parent's keys was refused.
  `advertise` by the sub-lessee was refused, and `break` of the child without `--force` was refused. A dead parent's
  keys outside the child were claimable and the child's were not. The child record stayed byte-identical in every case.
  The new binary gave the same refusal in each case but three, where the old binary was stricter.
  The new one reports "covered" or exits 11 instead, and lets the sub-lessee refresh its own keys. The old binary
  refused all three. The harness is kept outside the repo for M2 to reuse.
- Notices are delivered by every locked call that has a holder, after that call's repairs. `list` neither repairs nor
  delivers them. M3 decides how `list` reports them.
- The repair of requests also reaps a request whose parent lease no longer exists, with a "request declined" notice. It
  fell out of the same code, and M2 owns its checks. One test covers it here.
- `grant` also refuses a requester whose handle another live holder has taken since the request, and any other overlap
  with the caller's own leases or the parent's other sub-leases.
- Fixtures that stood for a newer minor moved from 1.2 to 1.3.
- Decisions amended during the build: resource leases cannot be sub-leased, the exit status of the refresh with
  delegations is 11, notice IDs are `<target-id>-<cause>`, and the sub-lease, request, and notice records carry version
  1.2. See the Decision Log entry of 2026-10-24.

### M2: Parent lifecycle

**Status:** DONE

- A child whose parent died, was released, or was broken survives as an ordinary lease. It is rewritten without
  `parent` at the lowest minor its fields need, and its sub-lessee gets a "parent ended" notice saying how
- A crash between any loss notice and its write yields exactly one notice on retry. A binary older than the child's
  minor writes no "parent ended" notice, and `list` without the lock delivers none
- `revoke` by the parent ends a sub-lease at once, with a tombstone of cause `revoked`. `revoke` by anyone else is
  refused
- A sub-lessee's release, a forced break of a child, and a dead child each return the keys to the parent, with a
  "sub-lease ended" notice
- A forced break or release of a parent lists the children that now stand alone
- A request whose parent lease ends is reaped with a "request declined" notice
- Notices never appear in `tombstones/`, and a lock holder reaps a dead holder's notices

**As built:**

- The repair of a dead or dangling parent runs in the same phase as the request repair, after it and before any
  delivery, on every locked call that opens the lock for a write, with or without a holder. `list` does not run it.
- A parent's `release` and a forced `break` publish every sub-lessee's notice, then remove the parent, then rewrite the
  sub-leases. A crash after the removal is finished by the dangling-parent repair, and its notice reads "cannot tell".
  Crash hooks cover a loss notice and a revoke tombstone. Each test runs the call twice and counts one report.
- A request whose parent ended is reaped by the next locked call, not by the call that ended the parent.
- A call that removes a lease marks it removed for the rest of the call. A break of a parent and its child in one call
  once could have rewritten the child after removing it, which recreates the file. The mark and a test close it.
- A loss notice is held back until its loss happens, so the addressee is not told early and not told twice. A notice
  left pending by a crash that never retries stays on disk until the sub-lease ends another way or the addressee's
  holder dies. See the Decision Log entry of 2026-10-24 for M2.
- Old-binary check, using the harness kept beside M1's, with a binary from `f29ad6d` and the new binary against a
  child stamped 1.3. A parent release, a forced break of the parent, a dead parent cleared by a third party's claim,
  and a dead parent with the child refreshing its keys all left the child byte-identical. No `notices/` file appeared,
  and the only tombstone was the break's own for the parent. With the child at 1.2 the new binary rewrote it and left
  one notice in the three cases where another holder made the call. `list` without the lock delivers none, which a
  test covers.
- Decisions amended during the build: see the Decision Log entries of 2026-10-24 for M2.

### M3: Display

**Status:** DONE

- `list --json` carries `parent`, a computed `delegated` list, `owner` and `grantor_of` marks, a `requests` array, and a
  `notices` array of what that call delivered, as an additive minor bump of its schema. A `list` that takes the lock
  repairs and delivers, and one that reads without it delivers none. Amended 2026-10-24; see Decision Log
- A filter on an ancestor of a delegated key marks the parent `owner` and `grantor_of`. A filter on a delegated key
  marks the child `owner` and the parent only `grantor_of`
- The human table indents children and pending requests under their parent, and prints a dangling child at the top
  level, including when `list` reads without the lock

**As built:**

- `list` under the lock now runs the request and sub-lease repairs and delivers the caller's notices, which M1 and M2
  left undone. A test of M2 that expected a locked `list` to leave a notice for the sub-lessee's next call was changed
  to match. See the Decision Log entries of 2026-10-24 for M3.
- The owner check takes the deeper of each overlapping query key and lease key, and asks whether one sub-lease key
  contains it. Tests cover each example in "Display", a full handoff, siblings, a parent whose keys are all delegated,
  and a shared prefix.
- The unlocked `list` reads a dead parent's children as standing alone, since that is what the next locked call makes
  them. A test plants a dangling child and lists with the lock held by another process, as JSON and as text.
- A request file this binary cannot use is not shown in `requests`.
- `list --json` goldens moved to schema 1.2. The old-binary harness was not rerun, since no record or state-directory
  change landed.

## Decision Log

- 2026-10-18: Drafted from the lease broker research. Settled decisions are limited to the roadmap the user approved
  that day.
- 2026-10-18: Added options and open questions from the sub-lease prior-art survey.
- 2026-10-18: The user ruled that systems like etcd, Consul, and ZooKeeper are far too complex for nitipan. Three
  options were removed. A per-grant choice of parent-death behavior was dropped, since review picks one rule. Nesting
  is forbidden, since nobody needs it, and a later proposal can allow it. Recall with a deadline moved to NIT-008,
  since it needs NIT-004's broker. The user preferred a separate proposal over a deferral with a revisit hook.
- 2026-10-18 (autonomous): A sub-lease is its own lease record with a `parent` field naming the parent lease ID. The
  parent's record is unchanged. A delegation list on the parent would need a new major, since a V1 `release` would
  delete live delegations. Converged in one pass.
- 2026-10-18 (autonomous): A sub-lease's keys must be covered by its parent's, and equal is allowed. The delegated keys
  belong to the sub-lessee alone while it lives. Shared rights would let the guardian's subagents edit the sub-lessee's
  files. Converged in one pass.
- 2026-10-18 (autonomous): The sub-lessee runs `nitipan request`, and the parent runs `nitipan grant <request-id>` or
  `nitipan decline`. The grant binds the requester's exact identity. A secret or handle could reach the wrong session.
  Converged in one pass.
- 2026-10-18 (autonomous): NIT-001 needs two amendments. "Re-acquiring as the same holder" must run the check against
  other live holders before the "covered by your leases" check. "Record evolution and unknown records" must say what
  `release` does on a record of a higher minor. NIT-001 is owned elsewhere, so this review does not edit it.
- 2026-10-18 (autonomous): A sub-lease survives its parent's death or a dangling parent, and becomes an ordinary lease.
  The next new-binary lock holder rewrites it without `parent` and leaves a notice in `notices/`. Ending it would free a
  live sub-lessee's keys to a third party. Converged in one pass.
- 2026-10-18 (autonomous): `list` keeps every lease top-level, with a computed `delegated` list on parents, `owner` and
  `grantor_of` marks on filtered matches, and a `requests` array. Nesting would hide sub-leases from consumers of the
  earlier schema. Converged in one pass.
- 2026-10-18 (autonomous): Deferred the fencing mechanism to NIT-005, with the outcomes it must keep. Revisit when
  NIT-005 settles "Token scope" and "Local fencing for NIT-001". Sharing the parent's token would let a revoked
  sub-lessee's token pass. Converged in one pass.
- 2026-10-18 (autonomous): NIT-001 needs two more amendments. Its `clear_when_dead` example, "such as ending
  sub-leases", is stale, since sub-leases survive their parent. Its `list --json` schema needs an evolution rule that
  says what a minor bump means to consumers.
- 2026-10-18 (autonomous): Amended "Invocation and consent". It said a requester learns of a grant the way a broken
  holder learns from a tombstone. It now learns through a notice, since tombstones would mislead a V1 `release`.
- 2026-10-18 (autonomous): The parent ends a sub-lease with a new `nitipan revoke`, whose tombstone carries cause
  `revoked`. A sub-lessee breaks its parent under NIT-001's rules. A forced break of a parent orphans its children.
  Plain `break --force` was rejected because no error may name it. Converged in one pass.
- 2026-10-18 (autonomous): A parent's release orphans its children. A child's end by any act but `revoke` returns its
  keys to the parent with a "sub-lease ended" notice. Refusing the release would block a full handoff that exiting
  completes anyway. Converged in one pass.
- 2026-10-18 (autonomous): Widened notices. They now carry how a cause came about, a parent gets one when a child ends,
  and a reaped request leaves its requester a "request declined" notice.
- 2026-10-18 (autonomous): NIT-001 needs two more amendments. "Callers with no adapter" must add `request`, `grant`,
  `decline`, and `revoke` to the commands that need an adapter. Milestone 3's check that the breaker can claim at once
  needs a carve-out for a broken sub-lease, whose keys return to its parent.
- 2026-10-18 (autonomous): Coherence fixes. No error names `revoke`, since a guardian's subagent would follow it and cut
  off a live sub-lessee. Before, a refused parent `acquire` named `revoke`, which was given as a reason for the command.
  A sub-lessee's claim inside its own sub-lease is "covered". The parent's refresh with live delegations gets its own
  exit status. A `--replace` that absorbs a lease with live children is refused. The `owner` mark now excludes the union
  of live children's keys, which its own example required. A granted sub-lease takes the request's ID, and every write
  that triggers a notice publishes it first, so a crash leaves no stuck request or lost notice. Requests and notices
  use the shared envelope with footprint `none`. Errors suggest `request` only against a lease that can delegate.
- 2026-10-18 (autonomous): NIT-001 needs two more amendments. "Breaking a live lease" says the breaker can claim the
  domain at once, which fails for a broken sub-lease. "Record evolution and unknown records" justifies a rule by "a
  NIT-003 delegation list", a design this proposal rejected.
- 2026-10-18 (autonomous): Re-check fixes. A grant now publishes the sub-lease before its notice, since a "granted"
  notice with no lease would let the requester edit inside the parent's live domain. The granted-request repair runs
  before other request reaping. Only a binary that can rewrite a dangling child writes its notice, which stops a repeat
  loop in older binaries. Notices are delivered only under the lock, after repairs. `request` refuses keys a live child
  covers, and errors suggest it only when no live child blocks.
- 2026-10-18 (autonomous): Final re-check fixes, not re-checked, since the pass budget was spent. A grant first marks
  the request `granting`, so a V1 removal between publish and repair can no longer lead to a reused lease ID or a
  "declined" grant. An interrupted grant leaves a new "request lapsed" notice. `request` also refuses keys the named
  lease does not cover. Every notice's ID is derived from its target and cause.
- 2026-10-18 (autonomous): ADR candidate (hard to reverse): a sub-lease is its own record with a `parent` link, and
  requests and notices live in `requests/` and `notices/`. Waits on NIT-001 carrying the amendments this log lists,
  since the envelope rules it rests on are still being edited. Revisit when NIT-001 is accepted.
- 2026-10-18 (autonomous): Amended "Fencing" to require the outcomes when an older binary ends a child, or a stated
  fail-closed lockout. Amended Impacts, which cited a missing "Revocation" section and too few NIT-001 sections.
- 2026-10-18 (autonomous): Milestones rewritten to check what the settled decisions require, and split display into
  M3. Before, M1 and M2 had seven checks and none for requests, notices, `list --json`, or V1 coexistence.
- 2026-10-18 (autonomous): Risk review. Added the dependence on NIT-001 amendments, rated high and high and mitigated by
  sending them to NIT-001's owner and gating milestone 1 on them. Accepted that subagents act as their guardian.
  Mitigated the new formats as one-way doors through NIT-001's envelope. Added the fencing outcomes NIT-005 may not
  keep. Accepted that an idle Codex or opencode parent may not see a request. Lowered the parent-death risk's impact to
  low, since the sub-lease survives.
- 2026-10-18: Review gate: kept all. The user chose to mitigate the NIT-001 amendments risk by sending the eight
  amendments to the nitipan guardian session. They were sent the same day.
- 2026-10-18: Accepted by the user. Not scheduled. Milestone 1 waits on NIT-001 carrying the amendments.
- 2026-10-18: The nitipan guardian took all eight NIT-001 amendments into NIT-001's working text. They are not
  committed yet. Two changed in wording. A V1 `release` of a caller's own higher-minor record removes it, as NIT-003
  needs. The rule on release-time duties was narrowed at this proposal's request: only a skipped duty that would make
  an old reader more permissive needs a new major, and a skipped notice to a parent does not. So `parent` stays a minor
  addition. The user kept all eight amendments at NIT-001's review gate.
- 2026-10-20: Scheduled as NIT-003 M1-M3 in queue.md.
- 2026-10-24 (run best guess, NIT-003 M1): Resource leases cannot be sub-leased. NIT-002's Impacts left the question to
  this proposal, and the text was silent. `request` against one is refused. A slot has no part to hand over, and the
  refusal is additive and reversible. NIT-002 is untouched. Amended "Invocation and consent". `request` also refuses a
  lease of the caller's own and a lease whose holder is dead, which the text implied.
- 2026-10-24 (run best guess, NIT-003 M1): The equal-key refresh with live delegations exits 11, the next free status.
  The text said only "its own status". Amended "Overlap with the parent".
- 2026-10-24 (run best guess, NIT-003 M1): A sub-lease, a request, and a notice are stamped record version 1.2, and the
  current minor is 2. A lease without `parent` keeps 1.0, and the parent's record never changes. The text said only "a
  minor". Amended "Record shape".
- 2026-10-24 (run best guess, NIT-003 M1): A notice's ID and file name is `<target-id>-<cause>`, with the request ID as
  the target of a request notice. The text said only that the ID is derived from the target and the cause. Amended
  "Liveness and the parent's death".
- 2026-10-24 (correction of a stale risk, NIT-003 M1): Amended the Risks entry "NIT-003 rests on NIT-001 amendments
  that NIT-001 does not carry yet". It was rated likelihood high, impact high, and said NIT-001 was in flight, that
  none was requested yet, and that milestone 1 starts only once NIT-001 carries them. NIT-001 took all eight on
  2026-10-18, so the entry now says the amendments landed, with likelihood low.
- 2026-10-24 (run best guess, NIT-003 M1): A refusal that names a sub-lessee is waitable under `acquire --wait`, since
  the sub-lease may end. No text changed.
- 2026-10-24 (run best guess, NIT-003 M2): The tombstone of a revoke carries a new `cause` field set to `revoked`, is
  stamped 1.2, and leaves `kind` empty. The text said only "cause `revoked`". `kind` already means `moved` or `revived`,
  and only a separate field lets an old reader deliver it as a plain break. A sub-lessee that is already dead gets no
  tombstone, and a retried revoke reuses its tombstone. Amended "Breaks across the tree".
- 2026-10-24 (run best guess, NIT-003 M2): A repair that finds a parent already gone writes a "parent ended" notice that
  says nitipan cannot tell how it ended. The text said the notice names how. Only the call that releases or breaks the
  parent knows. A sub-lease whose own holder is dead is not repaired. Amended "Liveness and the parent's death".
- 2026-10-24 (run best guess, NIT-003 M2): A "sub-lease ended" notice goes only to a live parent. A dead or gone parent
  has nobody to receive it, and the sub-lease repair has already made the child ordinary. Amended "Release across the
  tree".
- 2026-10-24 (run best guess, NIT-003 M2): Delivery holds a loss notice back until its loss has happened, and a retry
  replaces the pending notice instead of keeping it. Before, a notice published ahead of a crashed write could be
  delivered, and then published again by the retry, so the addressee heard twice. A "parent ended" notice is held while
  its sub-lease names a live parent, and dropped when the sub-lease is gone. A "sub-lease ended" notice is held while
  the sub-lease exists, and dropped when the addressee holds no lease covering its keys. No new field is stored. Amended
  "Liveness and the parent's death".
- 2026-10-24 (run best guess, NIT-003 M3): `list --json` is schema 1.2, and a lease with sub-leases is described by
  computed fields only. The text said only "an additive minor bump". Amended "Display".
- 2026-10-24 (run best guess, NIT-003 M3): An unfiltered `list` omits `owner` and `grantor_of`, and a resource filter
  omits them too. The text did not say. A mark answers a question about a path, so an unfiltered list has none to
  answer, and setting `owner` true on every lease would read as a claim nobody checked. If wrong, an unfiltered `list`
  would set `owner` true everywhere and `grantor_of` from `delegated`. Amended "Display".
- 2026-10-24 (run best guess, NIT-003 M3): `requests` omits a request in the `granting` state, and a filtered `list`
  keeps only the requests whose keys overlap the filter. A granting request is never granted again and its repair
  finishes it, so showing it as pending would invite a second grant. Amended "Display".
- 2026-10-24 (run best guess, NIT-003 M3): `list` that takes the lock runs the repairs of requests and sub-leases, and
  delivers the caller's notices, reported in a `notices` array with a `summary`. M1 and M2 left `list` neither
  repairing nor delivering. "Liveness and the parent's death" says a `list` that reads without the lock delivers no
  notices, as it skips tombstone cleanup, which implies a locked one does. Delivery removes a notice, so stderr alone
  would lose it for a guardian that pipes JSON. A `list` that reads without the lock repairs and delivers nothing. A
  test of M2 that expected a locked `list` to leave the notice was changed to expect delivery. If wrong, drop the
  repairs and the `notices` array. Amended "Display" and M3.
- 2026-10-24 (run best guess, NIT-003 M3): A failed repair in a locked `list` is a warning, not an error. `list` still
  prints what it read, and delivers no notice that call. `list` is the guardians' supported read path, and NIT-001
  already makes a failed tombstone removal non-fatal. A locked `list` also reaps the notices of dead holders, as every
  locked call does. Amended "Display".
- 2026-10-24 (run best guess, NIT-003 M3): Reworded the `owner` rule in "Display". It read "when the query overlaps its
  keys outside the union of its live children's keys". It now reads "when some part of the query that overlaps its keys
  is held by none of its live children", with the part and the holding spelled out. The two agree for path keys, since
  a part is one path and sibling subtrees are disjoint. The old wording hid how a part is found. Amended M3's first
  check to name the `notices` array, with the earlier entry above.
- 2026-10-24 (run best guess, NIT-003 M3): A sub-lease whose parent is dead is displayed as standing alone, and a dead
  parent lists no `delegated`. The text covered a dangling parent only. "Liveness and the parent's death" treats the two
  alike, and the next locked call makes that so. Amended "Display".
- 2026-10-24: ADR candidate (cuts across the system): a parent and its sub-lessees are exempt from each other at
  overlap, at the record level and outside ADR-03's key rule. NIT-005, NIT-008, and every later claim check must apply
  it. Undecided. Found in NIT-003 M1.

## References

- `spec/reports/2026-10-18-lease-broker-research.md`: "Roadmap"
- `spec/reports/2026-10-18-sub-lease-prior-art.md`: prior art for each open question
- NIT-001: the lease and overlap rules this proposal extends
