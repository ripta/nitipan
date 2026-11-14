# Sub-leases and recall

A holder can delegate part of its domain to another session. The other session then works in that part, and the holder
keeps the rest. The delegated part belongs to the sub-lessee alone while the sub-lease lives. Only path leases can
delegate. A resource lease cannot, and a sub-lease cannot delegate again.

The design is in [NIT-003](../../spec/proposals/NIT-003-sub-leases.md) and, for recall,
[NIT-008](../../spec/proposals/NIT-008-sub-lease-recall.md).

## Requesting and granting

The sub-lessee asks and the holder grants:

1. The other session runs `nitipan request <lease-id> --checkout <path> --scope <dir>`. The lease is the one that a
   refused `acquire` names. Request prints a request ID and the holder's handle.
2. The requester tells the holder out of band, for example through SendMessage.
3. The holder runs `nitipan grant <request-id>`. The output names the requester's handle and agent name. The holder can
   run `nitipan decline <request-id>` instead. The requester can run it to withdraw.

A grant binds the requester's exact identity, which is recorded in the request. The sub-lease takes the request's ID as
its lease ID, and its name, permission mode, and description from the request. The grant is refused when the requester
is the holder's own session, when the requester has died, when the keys are no longer inside the lease, and when
another live lease overlaps them.

The requester learns of a grant or a refusal from a notice on its next nitipan call. A notice goes to stderr as
`nitipan: notice: ...` and is removed once it is delivered. A `list` that takes the lock delivers the caller's notices
like any other command, and `list --json` also reports them in its `notices` array. A `list` that reads without the
lock delivers none. Requests wait in `requests/` and notices in `notices/`, both in the state directory. Older binaries
never read either one.

## While a sub-lease lives

- A claim by anyone else on the delegated part is refused, and the error names both holders.
- The holder's own claim on the delegated part is refused too. The error names the sub-lessee, and the refusal can be
  waited out with `--wait`.
- A claim inside your own lease or sub-lease exits 5, already covered. An exact re-claim of the same keys exits 0 with
  "already held; nothing changed". A claim that reaches outside the sub-lease is refused by the holder's lease.
- `acquire --replace` is refused on a sub-lease. A holder's `--replace` that would drop the delegated keys, or absorb a
  lease that has sub-leases, is refused and lists them.
- The holder that re-runs `acquire` with exactly its own keys exits with status 11 and lists the delegations. Status 11
  means that the holder still holds the lease, but does not own all of it right now.

## Revoking

The parent's holder can run `nitipan revoke <lease-id>` to end a sub-lease at once. The sub-lessee gets a tombstone
that names the parent's holder, with the cause `revoked`. Anyone else is refused, and the error names the parent.
Subagents share their parent's holder, so any subagent of the holder can revoke.

## Recall

The parent's holder can run `nitipan recall <lease-id> --deadline <duration>` to give a sub-lease time to finish. The
duration is required and has no default. The sub-lessee gets a notice with the deadline on its next nitipan call.

The output names the sub-lessee's handle and agent name and the absolute deadline. Tell the sub-lessee out of band, for
example through SendMessage or mail. The output also prints an `acquire --wait` command on the parent's whole key set.

There is no timer. The first locked nitipan call by anyone after the deadline ends the sub-lease as a `revoke` would.
It leaves a tombstone of the cause `recalled` and a "sub-lease ended" notice to the parent. A sub-lessee that releases
first returns its keys as usual.

- A second `recall` replaces the deadline.
- `nitipan recall <lease-id> --cancel` takes the recall back and tells the sub-lessee.
- A recall ends when its parent dies, is released, or is broken. The sub-lease then stands alone.
- A recall whose parent record this nitipan cannot read ends nothing, and is reported.
- The holder's `acquire --wait` on exactly its own keys waits while a delegation is under a recall. Without `--wait`,
  it exits 11 and lists the deadline.

An older nitipan never reads `recalls/`. It keeps answering the sub-lessee as covered past the deadline until a newer
binary takes the lock.

The node applies the same rule when it mirrors `leases/`. `nitipan who holds` therefore reports the parent as owner of
a recalled sub-lease's keys once the deadline passes, before any locked call removes the sub-lease. A node built before
recall reports the ex-sub-lessee as owner until then.

The git hook applies the same rule too. See [Git hooks and fence marks](git-hooks.md).

## When a sub-lease or its parent ends

- A sub-lessee's `release`, a forced `break` of the sub-lease, and a dead sub-lessee that nitipan clears each return the
  keys to the parent. The parent's record does not change. The parent gets a notice with the sub-lease's ID, its keys,
  and how it ended. The output of a `break` says the keys went back.
- A parent that dies, is released, or is broken takes only itself. Each live sub-lease is rewritten without `parent` in
  the same call, at the lowest record version its fields need. Its sub-lessee gets a notice that says how the parent
  ended. The output of `release` and `break` lists the sub-leases that now stand alone. A parent that died is handled by
  the next nitipan call that takes the lock.
- Releasing the parent ends the grant for good. The former parent cannot revoke a former sub-lease, and cannot claim its
  old domain while the sub-lease stands.
- A request whose parent lease ended is declined with a notice. A notice whose holder is dead is removed by the next
  call that takes the lock.
- A binary older than a sub-lease's record version never rewrites it and writes no notice for it.

## Seeing the tree

`list` shows the tree. The text output prints each sub-lease indented under its parent, and each pending request under
the lease it asks to share. A sub-lease whose parent is dead or gone prints at the top level, with its stored `parent`
link, until a locked call repairs it. A `list` that reads without the lock prints it that way too.

`list --json` carries the sub-lease fields `parent`, `delegated`, `owner`, `grantor_of`, and `recall`, and the
top-level `requests` and `notices` arrays. [JSON output and records](json-and-records.md) defines each one, and gives
the record versions that sub-leases, requests, notices, and recalls carry.
