# JSON output and records

`list --json` is the supported interface for scripts and guardians. Its schema is versioned apart from the lease
records, which are nitipan's own format. `who holds --json` and `who is --json` print a second, smaller document. This
page defines both, then describes the record versions and the state directory for readers who need to know what is on
disk.

## The compatibility contract

Every minor version of a JSON schema or a record format is an additive change. Consumers ignore fields they do not
know. A `list --json` consumer treats a lease of a kind it does not know as opaque, and judges it by its liveness and
footprint alone. A major bump breaks compatibility.

Records follow the same rule from the reader's side, which
[ADR-04](../../spec/adrs/ADR-04-lease-record-envelope-with-liveness-kind.md) sets:

- A reader accepts a higher minor of its own major and ignores fields it does not know. A higher major, or an envelope
  that does not decode, makes the record unusable
- A minor version may add a field only if leaving it out never makes an old reader more permissive
- An old binary never rewrites a record of a higher minor. It may remove its own such record on `release`. `break` on
  one needs `--force`, even when its holder is dead
- A record of an unknown domain kind is judged by its envelope. It blocks only the claims its footprint overlaps
- A reader that does not know a record's liveness kind, or finds it absent, reads the holder's liveness as unknown.
  Unknown counts as alive

An unusable record refuses every claim, shows as unknown in `list`, and is removable only with `break --force`. An
unusable tombstone never blocks a claim.

## `list --json`

The current schema version is 1.6. Every lease stays a top-level entry in `leases`, so a consumer of an earlier schema
still sees all of them. A liveness of `unknown` means treat the holder as alive.

### Schema history

| Version | What it added |
|---------|---------------|
| 1.0 | the document: `schema_version`, `leases`, and `tombstones` |
| 1.1 | the domain kind `resource`, the `remote` flag, and `effective_slots` and `slot_holders` on a local resource lease |
| 1.2 | sub-leases: `parent`, `delegated`, `owner`, and `grantor_of` on a lease, and the top-level `requests` and `notices` arrays |
| 1.3 | the top-level `marks` array of fence marks |
| 1.4 | the top-level `broker_leases` array of `user` and `shared` leases |
| 1.5 | `token` on each broker lease |
| 1.6 | `recall` on a sub-lease whose parent recalled it with a deadline |

The schema version lives in `internal/listing/listing.go`.

### The document

- `schema_version` is the schema version, as a string.
- `leases` lists every lease the call can read, local and remote.
- `tombstones` lists the tombstones this call delivered or reaped, plus any it found unusable and left in place. Each
  carries an `action` of `delivered`, `reaped`, or `unknown`.
- `requests` lists the pending sub-lease requests.
- `notices` lists the notices this call delivered.
- `marks` lists the fence marks.
- `broker_leases` lists the `user` and `shared` leases the broker grants.

`requests`, `notices`, `marks`, and `tombstones` are always arrays, never null. `broker_leases` is present only when the
call could reach the node, or when this holder has broker leases on file.

A filtered `list` keeps the requests and marks whose keys overlap the filter.

### Lease entries

A usable lease carries its `id`, `version`, `kind`, `liveness`, `footprint`, `clear_when_dead`, `machine`, and
`holder`. A lease of a known kind also carries `reach`, `domain`, `acting_for`, `permission_mode`, `description`,
`acquired_at`, and `absorbs`. `liveness` is an object with a `status` and an optional `reason`.

A record nitipan cannot use is an entry with `usable` false. It carries its `file`, the `version` it claims, an
`unusable_reason`, and the `raw_fields` that could be read. Its liveness is `unknown`.

### Resource fields

- `remote` is true on a resource lease written on another machine. Another machine's resource is a different resource,
  so a consumer that counts the holders of a name skips these.
- `effective_slots` is the number of holders this machine admits on a local resource lease's name.
- `slot_holders` lists the IDs of the live local leases on that name.

Both slot values are computed when `list` runs and are never stored. A lease with the `remote` flag carries neither.
[Resource leases](leases.md#resource-leases) explains the counts.

### Sub-lease fields

- `parent` is on a sub-lease, and names its parent lease ID. It stays set while the parent is dead or gone, until a
  locked call repairs the record.
- `delegated` is on a parent that has live sub-leases. It lists each one as `{"id": ..., "keys": [...]}`. It is
  computed when `list` runs and is never stored. A lease with no live sub-leases carries no `delegated` field.
- `owner` and `grantor_of` appear only when `list` is filtered by a domain. An unfiltered `list` has no path to answer
  for, so it omits both. `owner` is true when the filter overlaps a part of the lease that no live sub-lease holds, and
  false when the match lies wholly inside the sub-leases. `grantor_of` lists the IDs of the live sub-leases whose keys
  the filter overlaps, and is absent when there are none. A parent that holds `/repo` and delegated `/repo/src` is the
  owner and the grantor when filtered by `/repo`. Filtered by `/repo/src`, it is only the grantor, and the sub-lease is
  the owner. A resource filter sets neither mark.
- `recall` is on a sub-lease whose parent recalled it and whose recall is still pending, as
  `{"parent": ..., "deadline": ...}`. A `list` that reads without the lock treats a sub-lease past its deadline as
  ended, so the parent reads as the owner, and warns on stderr.

Each entry in `requests` has its `id`, `parent` lease ID, `requester` holder, `liveness`, `keys`, `description` when
the requester gave one, and `requested_at`. The `requester` holder carries the requester's handle. A request that
`grant` is already working on is not pending.

Each entry in `notices` has its `cause`, `target`, `paths`, `at`, and a `summary` sentence. Delivery removes a notice,
so read this array when stderr is not captured. A `list` that reads without the lock delivers none.

[Sub-leases and recall](sub-leases.md) explains how these fit together.

### Marks

Each entry in `marks` has its `id`, `usable`, the broken `lease`, the `holder` it guards against with that holder's
`liveness`, the `keys`, `broken_by`, `broken_at`, and a `summary`. A mark nitipan cannot use carries its `file` only,
with `usable` false. [Fence marks](git-hooks.md#fence-marks) explains when a mark stands.

### Broker leases

Each entry in `broker_leases` has these fields:

- `id`, the lease ID
- `reach`, `user` or `shared`
- `state`, which is `live`, `lapsed`, or `unknown`. A lease reads `unknown` when no live node answered and the entry
  came from the holder's own files
- `names`, the repo names the lease covers
- `anchor`, the ID of the `machine` lease it is anchored to
- `holder`, `node`, and `liveness`, which describe the holder
- `expires`, when the lease lapses without renewal
- `mine`, true when the caller holds it
- `token`, the lease's fencing token. See [Fencing tokens](pushing.md#fencing-tokens)
- `at_risk` and `risk`, set when the holder's node is disconnected from the broker. `risk` says when the lease lapses

Tokens are JSON numbers. They stay below 2^53 until about the year 2255, so readers such as `jq` read them exactly.

## `who --json`

`nitipan who holds <subject> --json` and `nitipan who is <name> --json` print one document at schema version 1.0. It
has these fields:

- `schema_version`, `"1.0"`
- `query`, `holds` or `is`
- `subject`, the subject or name asked about
- `found`, the leases that answer the query
- `notes`, what the query could not search or judge, when there is any
- `holders` and `duplicate`, from `who is` only. `duplicate` is true when more than one holder carries the name

Each entry in `found` has these fields:

- `lease`, the lease ID
- `qualified`, the holder's mail address `<mail name>@<label>`. It is empty when the holder is not mailable, and
  `not_mailable` says why. It never falls back to the handle
- `reach` and `state`. A broker grant is `live` or `lapsed`. A mirrored machine lease is `mirrored`, `stale`, or
  `unknown`. `stale` is also a flag of its own, set when the holder's node has gone quiet
- `holder`, `node`, and `host`, which describe the holder and its machine
- `description`, `liveness`, and `anchor`
- `names`, a grant's stored root set or a mirror's derived repo entries
- `paths` and `resource`, which describe a mirror's own keys
- `why`, what the broker could not judge for a mirror in the `unknown` state
- `expires_ms`, the expiry in milliseconds since the epoch

`nitipan who am-i --json` prints one document at schema version 1.0. It has these fields:

- `schema_version`, `"1.0"`
- `user_label`, the label of the user this node is bound to. It is an empty string when the node is bound to nobody

## Record versions

Each record carries a `version` of the form `major.minor`. A writer stamps the lowest minor whose fields the record
uses, so an older binary can still rewrite what it fully understands. The versions live in
`internal/record/version.go`.

| Record | Version |
|--------|---------|
| path lease | 1.0 |
| tombstone | 1.0 |
| session file | 1.0 |
| resource lease | 1.1 |
| sub-lease, with a `parent` field | 1.2 |
| request and notice | 1.2 |
| fence mark | 1.3 |
| recall, the `recalled` and `recall-cancelled` notices, and the `recalled` tombstone cause | 1.4 |

A sub-lease is its own record in `leases/`, with a `parent` field that names the lease it came from. The parent's record
does not change. A sub-lease's own record keeps version 1.2 while it is under a recall. A sub-lease that loses its
parent is rewritten at the lowest version its fields still need, so a path sub-lease drops to 1.0. A path lease that
uses none of the later fields keeps version 1.0 and stays writable by older binaries.

Other files carry versions of their own. Broker lease sidecars are at 1.1, which adds the token. Deadline, in-flight,
derived-entry, and mail files are at 1.0. Fence objects on a remote are at 1.0. The broker store is at 2.0.

## The state directory

The state directory is `~/.local/state/nitipan/`, with the home directory taken from the user database. `HOME` and
`XDG_STATE_HOME` are ignored. The directory must be on a local disk, and it must be owned by the calling user. nitipan
owns its layout. Every write happens under the lock, and every record is published through a synced temp file, so a
reader without the lock never sees a partly written one.

| Path | Contents |
|------|----------|
| `lock` | the state lock. It names the process holding it |
| `format` | the minimum writer version. A binary below it refuses every write and still reads |
| `leases/` | one record per lease, local and sub-lease alike |
| `tombstones/` | tombstones addressed to holders whose leases were broken, revoked, or recalled |
| `requests/` | pending sub-lease requests |
| `notices/` | notices waiting for their holder's next call |
| `recalls/` | the deadlines of recalled sub-leases, one record per sub-lease |
| `marks/` | the fence marks that `break --force` leaves for a broken holder |
| `slots.json` | the resource slot counts, which the user writes |
| `sessions/registered/` | Codex session registrations under their host |
| `sessions/ended/` | Codex end marks |
| `sessions/presence/` | opencode presence marks from a TUI |
| `sessions/executing/` | opencode execution marks from the server plugin |
| `sessions/config/` | the remembered Codex config checks, one file per `CODEX_HOME` |
| `broker/pending/`, `broker/claimed/`, `broker/answered/` | the request handoff between the CLI and the node |
| `broker/presence.json` | the node's presence record |
| `broker/held/` | one sidecar per broker lease the holder holds, with its anchor and token |
| `broker/deadlines/` | each holder's deadline anchor, which the CLI reads to report leases at risk |
| `broker/inflight/` | commands whose reply the CLI did not see, so a retry reuses the request ID |
| `broker/orphans/` | anchors of claims whose refusal reached the CLI too late |
| `broker/derived/` | the repo entries of machine leases, one file per lease ID |
| `broker/rootsets.json` | the root-set cache, keyed by HEAD commit |
| `mail/inbox/` | messages stored for this machine's sessions |
| `mail/accepted/`, `mail/read/` | the node's ledger of messages it took in, and reads it has not yet reported |
| `mail/sent/` | the sender's note on each message it sent, with the stage the broker last reported |
| `mail/reg/`, `mail/ring/`, `mail/wait/` | mailbox registrations, ring ledgers, and armed waiters |
| `node.lock` | the node's lock, so a second node for the same machine and user fails |
| `node-key` | the node's instance key, readable by its owner only |
| `cross-user-enabled` | the node's record that the broker turned cross-user features on |

Binaries that predate a directory never scan it, and writers create the newer directories on demand. Nothing the
broker holds is ever written into a directory that the first version scans.

The node and broker config files live outside the state directory, in `~/.config/nitipan/`, since a sandboxed session
can write the state directory. See [Running nodes](../operations/nodes.md) and
[Running the broker](../operations/broker.md).
