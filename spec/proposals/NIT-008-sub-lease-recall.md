# NIT-008: Sub-Lease Recall

**Status:** accepted
**Created:** 2026-10-18
**Updated:** 2026-11-08

## Dependencies

- NIT-003
- NIT-005, for M2 only
- NIT-007, for M3 only
- NIT-004, for M4 only

## Impacts

- NIT-005 — "Local fencing for NIT-001" and "Refused mid-task". The hook honors a recall past its deadline, warns on
  commits before it, and has a refusal kind for a recalled sub-lessee that names no command
- NIT-007 — "Wake paths ring, and only a read consumes" and "Agents without a wake path". Every wake path rings for
  pending notices and requests, not only mail. The ring ledger becomes a file beside each registration in the state
  directory, in a format this proposal sets. Its line that `hook` subcommands do not ring no longer covers the git
  hook's recall warning
- NIT-004 — "Expiring leases" and "Lease term and renewal". Recall no longer uses broker terms, so the Impacts entry and
  the line that revival never applies to a recall describe nothing this proposal does. "Source of truth for machine
  leases": the node applies a recall's deadline when it mirrors `leases/`

## Summary

A parent holder can recall a sub-lease with a deadline. The sub-lessee is told, and it has until the deadline to finish
and release. If it has not released by then, the sub-lease ends as if it were revoked. The deadline is stored beside the
sub-lease in the state directory, and every nitipan call enforces it when it reads.

## Motivation

NIT-003 revokes a sub-lease at once. That cuts off a sub-lessee in the middle of its work, possibly with uncommitted
changes. A guardian that wants its domain back usually wants it soon, not this instant.

A deadline does not need a timer. Ending a sub-lease matters only to a session that wants its keys, and that session's
own nitipan call can end it. Amended 2026-10-24; see Decision Log.

## Design Decisions (Settled)

### Split from NIT-003

Recall with a deadline was an option in NIT-003's revocation question. It moved here because it needs NIT-004's broker.
The user preferred a separate proposal that depends on the broker over a deferral inside NIT-003.

Amended 2026-10-24; see Decision Log. The separate proposal stands. "How the deadline is enforced" found that recall
needs no broker, so this proposal no longer depends on NIT-004.

### Sub-leases at machine reach

NIT-003's sub-leases live in NIT-001's state directory. Whether recall works for them through the node, or only for
leases the broker grants, is open.

Decision (autonomous): recall applies to NIT-003's local sub-leases, and only to them. Every sub-lease that exists is a
`machine` record in the state directory. NIT-004 says the broker never grants, renews, or expires a `machine` lease, and
NIT-005's "Token scope" says broker grants have no sub-leases. Recall for broker sub-leases belongs to the proposal that
first creates them, the same proposal NIT-005 gives the token reissue rule.

Recall for broker-granted sub-leases only was rejected. It reaches no sub-lease that exists, and it would have to design
broker sub-leases first. Both reaches at once was rejected for the same reason. It designs a broker sub-lease feature
that no caller has asked for.

### How the deadline is enforced

- A recall shortens the sub-lease's term to the deadline. The broker's normal expiry ends it. This needs no new timer.
- The broker keeps a separate recall timer per sub-lease.

Decision (autonomous): no timer. The deadline is stored beside the sub-lease, and every new-binary reader enforces it
when it reads. "Sub-leases at machine reach" left both listed candidates without a lease to act on, since a `machine`
sub-lease has no broker term. Amended 2026-11-13; see Decision Log.

The recall record:

- `nitipan recall <child-id> --deadline <duration>` runs under the lock. Only the holder of the child's parent lease may
  run it, as with `revoke`. It is refused for a lease with no `parent`, a child whose parent is gone, and a parent
  record the binary cannot read
- It writes `recalls/<child-id>.json`, a record with NIT-001's envelope and footprint `none`. The record holds the
  parent lease ID, the time of the recall, the deadline as an absolute UTC time, and a `state` of `announcing`,
  `announced`, or `cancelling`. V1 and NIT-003 binaries never scan `recalls/`. The child's own record does not change,
  so it keeps version 1.2
- The recall record, the `recalled` and `recall-cancelled` notices, and the `recalled` tombstone carry the next minor of
  NIT-001's major when M1 lands. That minor is the one that adds recall, whatever other proposal lands first. A recall
  record of a higher minor is read under the minor rule. `recall` and `--cancel` refuse to rewrite it and name an
  upgrade. Removing it follows NIT-001's writer rules for a record of a higher minor
- A recall is in force exactly while the child still names that parent, the parent is not gone by the test "Liveness and
  the parent's death" uses in NIT-003, the parent's record is usable, and the record is not `cancelling`. A recall whose
  parent record a reader cannot use ends no child for any reader, and each reader reports it. A parent that dies,
  releases, or is broken before the deadline cancels the recall. The parent-gone repair runs before any expiry. Ending a
  child with no live parent would free a live sub-lessee's keys to third parties
- Every locked call removes a recall record whose child is gone, whose child no longer names that parent, or whose
  parent is gone by NIT-003's test, after it drops that recall's notices. That covers a child released, broken, cleared
  as dead, revoked, or rewritten by an older binary. A `cancelling` record is finished, never reaped. A recall whose
  parent record is unusable stays in place and is reported, and it is enforced once the parent reads usable again

Expiry:

- Every locked call's repairs end each child whose recall is in force and past its deadline. The deadline is inclusive:
  a reader whose clock reads the deadline itself finds the child ended. A child whose own holder is dead is left for
  the clearing that removes it, as the parent-gone repair leaves it. The expiry first writes a tombstone with cause
  `recalled` and the "sub-lease ended" notice to the parent. The notice says the recall's deadline
  passed. It then removes the child, and removes the recall record last. The tombstone names the parent's holder as the
  breaker, taken from the parent's record, since the call that runs the expiry may be a third party's
- A retry after a crash reuses a tombstone for the same lease whose cause is `revoked` or `recalled`, so the sub-lessee
  is told once
- A locked call reports the expiries it ran. A parent whose own `recall` or `--cancel` comes just after the deadline
  gets the "sub-lease ended" notice in that same call, so its refusal is explained
- A recall of a child whose holder is already dead removes the child at once with no tombstone and no notice, as
  `revoke` does

Readers that take no lock:

- NIT-005's `hook` and an unlocked `list` apply the same in-force and deadline rule. They treat a child past its
  deadline as ended, with the parent as owner
- The hook refuses the ex-sub-lessee's commits there with its own refusal kind. It quotes the recall and names no
  command, as the tombstone kind does. It never names `request`
- A recall record a reader cannot use never ends a child. A locked call leaves the child alone and reports the file. The
  hook reports it and ignores it, as NIT-005 treats a fence mark it cannot read
- NIT-004's node applies the rule when it mirrors `leases/`, so discovery reports the parent as owner past the deadline

Changing a recall:

- A second `recall` on the same child replaces the deadline, earlier or later, and the notice under "Telling the
  sub-lessee"
- `recall --cancel <child-id>`, by the parent's holder only, first rewrites the record to `cancelling` through
  `rename(2)`. A `cancelling` recall is not in force, so no reader ends the child from then on. It then drops the
  `recalled` notice, writes a `recall-cancelled` notice to the sub-lessee, and removes the record. Every locked call
  finishes a `cancelling` record it finds. A crash then never revives a cancelled recall, and never leaves the
  sub-lessee unaware of a cancel. A mistaken recall has a recovery path short of waiting it out and granting again
- A new `recall` on a `cancelling` record finishes the cancel first. It removes a pending `recall-cancelled` notice, and
  delivery drops one while a recall is in force. The sub-lessee never reads "cancelled" after a newer recall

Taking the keys back:

- With `--wait`, an equal-key-set refresh waits while any delegation it exempts is under a recall in force. Each retry
  is a locked call, so the expiry runs once the deadline passes. That covers a full handoff, where the delegated keys
  are the parent's whole key set. Without `--wait`, the refresh exits 11 as NIT-003 says, and its output lists the
  recall's deadline
- `recall`'s output names the sub-lessee's handle and agent name and the absolute deadline. It says to tell the
  sub-lessee out of band. It also prints an `acquire --wait` command on the parent's whole key set, with a wait that
  covers the deadline. That claim is the refresh above, so it exits 0 once the child ends, or 11 if other delegations
  remain. A claim on the delegated keys alone would exit 5 as "already covered" once the child ends
- No error names `recall`. Its help text says subagents share their guardian's holder and can recall, as `revoke`'s does
- `list` shows a pending recall's deadline on the child. `list --json` gains a `recall` object on the child, with the
  parent lease ID and the deadline. That is an additive bump to the schema's next minor, under NIT-003's rule for `list
  --json`

The deadline is wall-clock time, since it crosses processes. A forward clock step fires a recall early, which ends the
sub-lease early the way `revoke` would. Among new binaries the keys only ever return to the parent.

Older binaries leave gaps, stated rather than closed:

- A NIT-003 binary never reads `recalls/`. Past the deadline it still answers "covered" to the sub-lessee's own claim,
  until a new binary's locked call ends the child. The parent's own `acquire --wait` is such a call. Meanwhile new
  readers tell the parent it owns the keys. Both could edit files there, since files are not fenced
- A hook binary built before M2 reads no recall. Past the deadline it passes the ex-sub-lessee's commits and refuses the
  parent's, until a new binary's locked call writes the tombstone. NIT-005's hook runs the stable `nitipan` on PATH, so
  this needs mixed binaries on PATH
- A NIT-003 binary can rewrite the child in its parent-dead repair. The recall then is no longer in force, and the next
  new-binary call removes it
- A NIT-003 binary's `revoke` of a recalled child leaves the `recalled` notice, and a NIT-003 binary may deliver it. A
  NIT-003 retry of `revoke` after a crash ignores a `recalled` tombstone, so the sub-lessee can be told twice
- A node built before M4 ignores `recalls/`. Discovery reports the ex-sub-lessee as owner past the deadline, until a
  locked call ends the child. It lets no claim through, since claims never consult the broker

A deadline field on the child record was rejected. A NIT-003 reader ignores it, and so answers "covered" past the
deadline. Leaving the field out would make an old reader more permissive on claiming, which ADR-04's minor rule forbids.
The new major it would need makes the child unusable to every older binary, so an older sub-lessee could not even
release it. A node timer beside the lazy rule was rejected. It closes the older-binary window only while a node runs,
adds a second writer of lease state and a contract across deploy units, and the parent's `acquire --wait` already closes
that window with no node.

### Telling the sub-lessee

- A tombstone-like record the sub-lessee reads on its next nitipan call. It may not call again before the deadline.
- A NIT-007 message, which can wake an idle session where the agent allows it. This would add a dependency on NIT-007.

Decision (autonomous): every path the sub-lessee already runs tells it. A notice carries the recall, the git hook and
NIT-007's rings point at it, and the parent is told to send word out of band. Amended 2026-10-24; see Decision Log.

- `recall` writes a NIT-003 notice with cause `recalled` and ID `<child-id>-recalled` in `notices/`. It carries the
  deadline and says to finish, commit, and release. It never goes in `tombstones/`. A V1 `release` reports an ID with an
  undelivered tombstone as "broken", and that would misreport a live sub-lease
- The recall record is written as `announcing`. Then the notice is written, replacing any older one in place. Then the
  record is rewritten as `announced`, through `rename(2)`. Every locked call finishes a record it finds `announcing`, as
  NIT-003 finishes a request in the `granting` state. A crash then never leaves a recall unannounced, or an undelivered
  notice showing an old deadline
- Notice delivery gains `recalled` and `recall-cancelled` cases. It drops a `recalled` notice when its recall is gone or
  not in force, so a cancelled recall is never announced. A NIT-003 binary delivers any cause it does not know, so it
  may deliver a notice whose recall was cancelled. That gap is stated, not closed
- The NIT-005 hook prints the recall's deadline on stderr when it passes a commit by a sub-lessee under recall. It only
  signals. It writes nothing and consumes nothing, which ADR-06 requires of a ring
- `recall`'s output tells the parent to notify the sub-lessee out of band, through SendMessage or NIT-007 mail, as
  NIT-003's `request` output does

NIT-007's rings cover pending notices and pending requests, not only mail:

- A ring fires for a notice addressed to the session that delivery would deliver now, judged read-only by delivery's own
  verdict. A notice delivery keeps, such as a "parent ended" notice waiting on its repair, never rings. NIT-006's
  notice that a coworker's `shared` claim was refused by the session's lease never rings either, turn hooks included.
  A refused claim costs the coworker nothing, so a ringing notice would let them wake the session's agent with no
  mail grant. Its cause is `shared-claim-refused`, and that cause is how a ring path tells it apart. Amended
  2026-11-10; see Decision Log
- A ring also fires for a pending request whose target lease the session holds. That is the idle parent in NIT-003's
  accepted risk, whose requester waits on it. The notices with a waiting reader are the `recalled` notice and NIT-003's
  request granted, declined, and lapsed
- Every NIT-007 wake path carries these rings: the turn hooks, the Claude Code waiter and socket ring, the opencode
  plugin, and `codex queue`. The ring says to run `nitipan list`, which delivers notices when it takes the lock
- A turn hook's ring does not wake, so it may repeat. A waking path rings once per notice or request, keyed by its ID
  and its write time, so a replaced `recalled` notice rings again. Those keys join NIT-007's per-identity ring ledger
- The ledger is one file per mailbox registration, beside it in the state directory, so a waiter with no node running
  still rings once. It carries a `major.minor` version and its registration's identity, is published through the
  temp-file scheme, and is removed with its registration. A ledger a path cannot use reads as empty, so the path may
  ring again, which ADR-06 allows. A session with no registration has no waking path, and gets only the turn hooks'
  rings. Amended 2026-11-08; see Decision Log
- NIT-007 M1 builds the ledger as this file, keyed by message ID, and every NIT-007 wake path rings through it. A node
  built before M3 therefore never rings for notices or requests, and never rings twice for one message. M3 adds their
  keys to the same file. Amended 2026-11-08; see Decision Log

A busy sub-lessee hears on its next tool call or commit, and an idle one is woken where its agent allows. With none of
NIT-005 and NIT-007 built, the notice and the parent's message still reach it.

The notice alone was rejected. A busy sub-lessee that neither commits nor calls nitipan would learn only from the
tombstone. A recall sent as NIT-007 mail was rejected. It routes a same-machine pair through the broker. `recall` could
not tell anyone while the broker or node is down, and a Claude Code record without `session_id` has no mail address.
Rings for the `recalled` cause only were rejected. A requester waiting on a grant, and a parent with a pending request,
have the same need.

### Deadline length

Whether the parent picks the deadline, a fixed default applies, or both is open.

Decision (autonomous): the parent must choose. `--deadline <duration>` is required, with no default. Its absence is an
error that says what the duration means: time for the sub-lessee to finish, commit, and release. A duration of zero or
less is refused, so `recall` can never be a `revoke` that escapes `revoke`'s help-only guard.

How soon a parent needs its domain back varies, from a quick fix to the end of the day, and the parent is the one who
knows. A default was rejected. No single value fits, and an agent following the help text would take it without
deciding. A fixed constant was rejected for the same variation. Bounds beyond refusing zero or less were rejected. No
failure a minimum or maximum prevents has been named, and a long recall leaves only a harmless pending record.

## Design Decisions (Open)

None.

## Risks

- **The sub-lessee misses the recall** — likelihood: medium, impact: medium.
  An idle sub-lessee whose agent has no wake path, and that makes no call before the deadline, learns of the recall only
  from its tombstone. Its uncommitted edits stay in the tree, which now belongs to the parent. Mitigation: the notice,
  the hook's warning, NIT-007's rings, and the parent's own message reach it on every path it runs. The parent picks a
  deadline that fits.
- **Older binaries honor a recalled sub-lease past its deadline** — likelihood: medium, impact: medium.
  A NIT-003 binary never reads `recalls/`, so it answers "covered" to the sub-lessee until a new binary's locked call
  ends the child. Meanwhile new readers tell the parent it owns the keys, and both could edit files there. A hook built
  before M2 passes the ex-sub-lessee's commits in the same window. Mitigation: the parent's `acquire --wait` ends the
  child at the deadline, and so does any new-binary locked call. Third parties are never let in. The gaps need mixed
  binaries on PATH, and "How the deadline is enforced" states each one.
- **A clock step fires a recall early** — likelihood: low, impact: low.
  The deadline is wall-clock time. Accepted (autonomous): an early end is what `revoke` already does, and the keys go
  only to the parent.
- **The recall record and the `recalled` cause are formats that outlive binaries** — likelihood: high, impact: medium.
  `recalls/`, the notice causes, the tombstone cause, the `list --json` field, and the ring ledger file are read by
  every later binary and node. The ledger is shared by the CLI's hooks and the node, which deploy separately.
  Mitigation: each rides NIT-001's envelope and minor-version rule. `recalls/` is a directory older binaries never scan,
  the child record keeps version 1.2, and old readers deliver a `recalled` tombstone as a break, as they do `revoked`.
  A ledger a path cannot use reads as empty, so skew costs at most a repeated ring.
- **M2 through M4 rest on NIT-005, NIT-007, and NIT-004, which are unbuilt** — likelihood: medium, impact: low.
  Their hook and ring designs could shift before they are built. Mitigation: M1 stands alone with the notice and the
  parent's message. The Impacts entries name the sections each relies on.

## Milestones

### M1: Recall with a deadline

**Status:** DONE

- `recall <child-id> --deadline <duration>` by the parent's holder writes the recall record and the `recalled` notice,
  and leaves the child's record unchanged
- `recall` is refused for another caller, a lease with no `parent`, a gone or unreadable parent, a missing or
  non-positive duration, and a recall record of a higher minor. `--cancel` is refused for anyone but the parent's holder
- A crash between the recall record and its notice, on a first and on a second `recall`, leaves the notice showing the
  current deadline after the next locked call
- A sub-lessee that releases before the deadline returns its keys to the parent, with the "sub-lease ended" notice
- The first locked call past the deadline, by any caller, writes a `recalled` tombstone naming the parent's holder and a
  "sub-lease ended" notice to the parent, then removes the child. A crash between the steps tells each side once,
  whatever the cause of the tombstone found
- A parent that dies, releases, or is broken before the deadline cancels the recall. The child stands alone as NIT-003
  says, and the undelivered `recalled` notice is dropped. An unreadable parent leaves the child alone and is reported
- A recall record that is no longer in force is removed by the next locked call, after any release, break, dead-holder
  clearing, revoke, or older-binary rewrite of its child
- A recall whose parent record is unusable ends no child for a locked call or an unlocked `list`, and each reports it.
  The recall survives the locked call, and is enforced once the parent reads usable again. Amended 2026-11-13; see
  Decision Log.
- A second `recall` replaces the deadline and the notice. `recall --cancel` removes both and leaves a `recall-cancelled`
  notice, and a later `recall` removes that notice. A crash at each step of `--cancel` leaves the recall cancelled after
  the next locked call, and the sub-lessee told once. A recall of a dead sub-lessee removes the child at once with no
  tombstone
- An unusable recall record never ends a child, and is reported. The recall's records carry the minor that adds recall
- An unlocked `list` shows a child past its deadline as ended. `list` and `list --json` show a pending recall, and the
  `list --json` schema takes its next minor
- `acquire --wait` on the parent's whole key set waits under a recall and returns the keys at the deadline, after a full
  handoff and after a partial delegation. Without `--wait` it exits 11 and lists the deadline
- `recall`'s output names the sub-lessee and the deadline, says to notify it, and prints an `acquire --wait` command
  whose wait covers the deadline
- A NIT-003 binary leaves the recall record alone and can still rewrite the child, and a new binary then removes the
  recall

**As built:**

Nothing here ran against a real agent session. Every check drives the CLI over a fake process table and a settable
clock.

- The record minor is 1.4. `CurrentMinor` moved to 4, so the tests that used 1.4 as a "newer minor" fixture now use 1.5.
  The child's own record keeps 1.2
- One exported function, `lease.JudgeRecall`, decides in force, past the deadline, not in force, or unusable. It reads
  plain data and takes no lock. M2's hook and M4's node mirror call it. A locked call, an unlocked `list`, and
  delivery's verdict on the two new notice causes all use it. The deadline is inclusive. See Decision Log
- The repair runs after the parent-gone repair, so a child whose parent died has already stood alone and its recall is
  reaped, never enforced. A `cancelling` record is finished before any verdict. An unusable parent leaves the recall
  and warns on every locked call. An unusable recall record warns the same way and ends nothing
- The crash order is hooked at each step of `recall`, `--cancel`, and the expiry, and a test crashes at each one. The
  expiry reuses a tombstone of cause `revoked` or `recalled` for the same lease. A revoke reuses either one too. No
  test reaches the revoke side, because every locked call's repairs end a recalled child before a revoke's body runs.
  The shared lookup serves both
- `recall` and `--cancel` refuse a recall record they cannot use, not only one of a higher minor, and exit 9. A cancel
  with no recall exits 8. Recall of a dead sub-lessee prints that no recall was written
- A recall record of a higher minor is never removed or finished by this binary. The reap, an expiry's last step, and a
  cancel leave it for a newer binary, since NIT-001 lets an older binary remove such a record only on its own holder's
  release. `recall` on a dead sub-lessee checks the record first and refuses the same way
- The cancelled notice is written only for a sub-lease that still stands and names the parent. A cancel that finishes
  after the sub-lease is gone writes none
- A locked call prints a line to stderr for each expiry it ran, naming the sub-lease and the deadline. A call that
  cannot use a record warns with the file's path
- A `list` that reads without the lock drops a sub-lease past its deadline from its leases and from the parent's
  `delegated`, so the parent reads as owner, and warns on stderr that the sub-lease counts as ended. The JSON has no
  marker for it
- A refresh under a recall in force returns the deadline in its result. `acquire --wait` retries on it, so an equal-key
  refresh waits for the expiry and exits 0, or 11 when other delegations remain. A wait that ends before the deadline
  exits 10. Without `--wait` the delegation line ends with `recalled until <deadline>`
- `recall` prints an `acquire --wait` command on the parent's whole key set. The wait is the time to the deadline
  rounded up to a second, plus 10 seconds, so the last attempt runs after the deadline
- `list` text and `list --json` show a pending recall on the sub-lease. The schema moved from 1.5 to 1.6
- The hook's recall warning, the recall refusal kind, the rings, and the node mirror are not built. They are M2, M3,
  and M4

### M2: Recall in the git hook

**Status:** NOT STARTED

- The hook refuses an ex-sub-lessee's commits on recalled keys past the deadline, with a refusal that quotes the recall
  and names no command, and passes the parent there
- Before the deadline, the hook passes the sub-lessee's commits and prints the deadline on stderr
- A recall record the hook cannot use is reported and ignored
- A recall whose parent record is unusable ends no child for the hook, which reports it. Amended 2026-11-13; see
  Decision Log.

### M3: Rings for pending notices and requests

**Status:** NOT STARTED

- Every NIT-007 wake path rings for a notice that delivery would deliver now, and for a pending request on a lease the
  session holds, naming `nitipan list`
- A notice that delivery keeps or drops never rings. Neither does a notice with the cause `shared-claim-refused`.
  Amended 2026-11-10; see Decision Log
- A waking path rings once per notice or request with no node running. A re-armed waiter does not ring again for an
  ignored one, and a replaced `recalled` notice rings again

### M4: Recall in the node's mirror

**Status:** NOT STARTED

- Past a recall's deadline, and before any locked call ends the child, "who holds X" reports the parent as owner of the
  recalled keys

## Decision Log

- 2026-10-18: Split from NIT-003's revocation question, since recall needs NIT-004's broker.
- 2026-10-24: Recall applies to NIT-003's local sub-leases only. Broker sub-leases do not exist yet. (autonomous)
- 2026-10-24: The deadline lives in a `recalls/` side record, and every new-binary reader enforces it. No timer and no
  node. (autonomous)
- 2026-10-24: A `recalled` notice tells the sub-lessee. The NIT-005 hook warns on commits, NIT-007 rings for every
  pending notice, and the parent is told to send word out of band. (autonomous)
- 2026-10-24: The parent must pass `--deadline`. There is no default, and zero or less is refused. (autonomous)
- 2026-10-24: Amended "Split from NIT-003" and the Motivation. Both said recall needs NIT-004's broker to enforce the
  deadline. "How the deadline is enforced" needs none, so the NIT-004 dependency is dropped. NIT-005, NIT-007, and
  NIT-004 join for M2, M3, and M4. The separate proposal the user chose stands. (autonomous)
- 2026-10-24: Risks rewritten after the design settled. The open mitigation for a missed recall now names the paths that
  tell the sub-lessee. (autonomous)
- 2026-10-24: Coherence review amended "How the deadline is enforced" and "Telling the sub-lessee". Before, the parent
  got no notice of an expiry, and `acquire --wait` returned at once after a full handoff. The notice was written after
  the record with no repair. Recall records had no reap rule, and the cancel notice had no ID. Rings fired for notices
  delivery keeps, the ring ledger missed a replaced notice, and the node's mirror ignored recalls. The ring rationale
  named NIT-003's idle parent, which a notice cannot reach, so rings now cover pending requests too. (autonomous)
- 2026-10-24: A re-check found that `--cancel` had no crash order and that readers disagreed on an unusable parent. The
  printed wait failed for a partial delegation, the ledger had no format or skew rule, and the version was fixed at
  1.3. Each now has a rule, and M1 through M4 check them. (autonomous)
- 2026-10-24: A last re-check found that the reap rule removed a `cancelling` recall and one with an unusable parent.
  The reap now names only a gone child, a child that no longer names the parent, and a gone parent. That fix was not
  re-checked, since the review's pass budget ran out. (autonomous)
- 2026-10-24: Risk review added the ring ledger to the formats that outlive binaries, with its skew mitigation.
  (autonomous)
- 2026-10-24: Review gate: kept all.
- 2026-10-24: ADR candidate (cuts across the system): a deadline on a local lease is enforced by every reader when it
  reads, from a record beside the lease, never by a timer process. Declined: the user chose not to write it.
  (autonomous)
- 2026-10-24: Accepted.
- 2026-10-24: NIT-006's review, which accepted NIT-006, amended "Telling the sub-lessee" in place. The ring rule said a
  ring fires for every notice delivery would deliver now. NIT-006 adds a notice to the holder of a lease that refused
  a coworker's `shared` claim. That notice never rings, since a refused claim is free to make and would otherwise
  wake the holder's agent with no mail grant.
- 2026-10-24: Scheduled as NIT-008 M1-M4 in queue.md.
- 2026-11-08 (autonomous): NIT-007 M1 built the ring ledger. The text of "Telling the sub-lessee" said "A node built
  before M3 keeps its own ledger for mail and never rings for notices. It may ring a second time for mail a new waiter
  already rang." That left the waiter and the node with two ledgers for one message, and M1's check that each message
  wakes a session at most once needs one shared file. M1 therefore writes the file in this proposal's format, keyed by
  message ID. M3 adds the notice and request keys. A ring repeated by a pre-M3 node is no longer possible. The same
  entry corrects the file's header. The text said the ledger "carries NIT-001's envelope with `major.minor`". That
  envelope is a lease record's footprint, liveness, and holder, which a ledger of rung keys has no use for. The ledger
  carries a `major.minor` version and its registration's identity, and ADR-04's reader rules apply to the version.
- 2026-11-10 (autonomous): NIT-006 M2 built the refused-claim notice. "Telling the sub-lessee" said the notice never
  rings, but it gave no way for a ring path to recognize it. It now names the notice's cause, `shared-claim-refused`,
  and M3's check names it too. Nothing else about the rule changed. The bullet's marker moved from the 2026-10-24 entry
  to this one.
- 2026-11-13 (autonomous): M1 built the CLI and the locked and unlocked readers, and no hook. M1's check said "A recall
  whose parent record is unusable ends no child for a locked call, an unlocked `list`, or the hook, and each reports
  it." The hook is M2, so M1 would have named a reader it does not build. M1's check now names the locked call and the
  unlocked `list`, and M2 gets a check for the hook.
- 2026-11-13 (autonomous): "How the deadline is enforced" said every locked call's repairs end each child "whose recall
  is in force and past its deadline", and gave no rule for the instant of the deadline or for a dead sub-lessee. The
  deadline is now inclusive, since a wait whose last attempt runs at its own timeout must see the child ended. A child
  whose own holder is dead is left for the clearing that removes it, as the parent-gone repair leaves it, because
  nobody can receive its tombstone. The marker on the decision moved from 2026-10-24 to this entry.

## References

- NIT-003: the sub-leases and immediate revocation this proposal extends
- NIT-005: the git hook that honors a recall
- NIT-007: the rings that point a session at its pending notices
- ADR-04: the envelope and minor-version rule the recall record and new causes follow
- ADR-06: rings only signal, and only a read consumes
- `spec/reports/2026-10-18-sub-lease-prior-art.md`: "Hierarchical and delegated leases", on NFSv4 recall
- NIT-006: the refused-claim notice that never rings
