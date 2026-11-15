# nitipan

nitipan is a lease and coordination tool for coding agent sessions. A session claims ownership of a domain, such as a
checkout or some paths inside one. The claim also advertises the session: what it does, where it runs, and how to reach
it. Other sessions use the leases to find each other and to avoid stepping on each other's paths.

It supports Claude Code, Codex, and opencode.

## Status

The first version is [NIT-001](spec/proposals/NIT-001-local-lease-cli.md). The commands, the Claude Code adapter, and
the Codex and opencode session rules are built. The Codex and opencode hooks and plugins pass automated live checks.
`make live-codex` and `make live-opencode` drive a private Codex app-server and a private opencode server against a
fake model server. They need no manual steps and cost no model tokens.

## First version

The first version runs on one machine, with no daemon. It has ten commands:

- `acquire` claims a domain
- `release` gives it up
- `list` shows every lease and whether its holder is alive
- `break` removes another holder's lease, and needs `--force` when that holder is alive
- `advertise` updates the caller's name, permission mode, or a lease's description
- `request` asks the holder of a lease for part of it
- `grant` hands a requested part to the requester as a sub-lease
- `decline` refuses a request, or withdraws one
- `revoke` ends a sub-lease at once, and only the holder of its parent can run it
- `setup` prints the sandbox allowance, hooks, and plugins an agent needs

An eleventh command, `hook`, is called only by those agent hooks and plugins.

A domain is keyed by absolute path, with optional path scopes. A claim against a dead holder succeeds. An overlapping
claim against a live holder is refused, and the refusal names the holder.

A Claude Code holder is alive while its process runs. nitipan checks the PID and the process start time, which guards
against PID reuse. Codex and opencode run every session under one long-lived host process. Their holders are judged by
session instead, through the hooks and plugins that `setup` prints.

Leases live under `~/.local/state/nitipan/`. That directory must be on a local disk. A sandboxed agent needs a write
allowance for it.

`acquire --wait <duration>` waits for a lease instead of being refused at once. See "Waiting" below. A later version
adds a self-hosted broker that works across machines. Callers will not change when the broker arrives. See the
[broker research](spec/reports/2026-10-18-lease-broker-research.md).

## Resource leases

A resource lease names a machine resource instead of a path. `nitipan acquire --resource cpu:heavy` claims "all cores"
for a full build or test run. A second live claim on the same name is refused, and the refusal names the holder.
`release --resource` and `list --resource` take the same name.

A name is `<namespace>:<name>`. Each part holds lowercase letters, digits, `-`, `_`, and `.`. The whole name is at most
64 bytes. A name outside that grammar is refused and never folded. When the only problem is case, the error suggests
the lowercase spelling. The `repo` namespace is reserved.

Names are flat. Two names overlap only when they are equal, so `cpu:heavy` and `cpu:light` never see each other. A
resource never overlaps a path lease. `--replace` does not apply to resource leases. Release the old one and acquire the
new one.

A resource belongs to its machine. A resource lease from another machine, as on a shared home directory, blocks nothing.
`list` marks it remote.

### Counted slots

A name admits one holder unless the user gives it a larger count. A count lets that many holders share the name at once.
The next claim is refused while the slots are full. The refusal names the holders and says to ask the user to raise the
count. A dead holder's slot is freed for the next claim.

The counts live in `slots.json`, in the state directory beside `leases/`. The user writes it, and every resource claim
reads it under the lock:

```json
{"version": 1, "slots": {"gpu:main": 2, "db:pg": 4}}
```

Each key is a resource name under the grammar above, and each count is an integer of at least 1. Any bad entry, an
unknown `version`, or an unknown top-level field makes the whole file unusable. Every call then warns with the file's
path and every error, and treats every name as exclusive. Path claims are unaffected.

Each lease records the count it was admitted under. A new claim is admitted only while the live holders number fewer than
the smallest of the configured count and every live holder's recorded count. Only this machine's leases count. Lowering
a count takes effect at once. Raising it takes effect once every holder admitted under a lower count has released.

A holder already on the name can claim it again at any time. That refreshes its lease and keeps its recorded count, even
when the slot is now over its count. A holder admitted under an old count therefore pins that count until it releases.
Ask the user to `break --force` it if it must go.

`--slots <N>` states the count you expect for the resource. A new claim is refused when `N` differs from the configured
count, and the error names the configured one. A repeat claim is compared with the count on the lease you already hold.

`list` shows, for each local resource lease, how many holders the name admits and which leases hold it. It also prints
the path of `slots.json`. `list --json` carries the same two values as `effective_slots` and `slot_holders` on each
local resource lease. Every lease stays a top-level entry, and a lease with the `remote` flag carries neither value.

`--wait` applies to resource claims, including a full slot. See "Waiting" below.

Resource leases are advisory. They coordinate cooperating sessions only. A process that never calls nitipan can still
use every core.

Acquire resources from the parent session only. Subagents share their parent's holder, so two subagents that both claim
`cpu:heavy` both succeed, and the first to release frees the name while the other still runs.

## Sub-leases

A holder can delegate part of its domain to another session. The other session then works in that part, and the holder
keeps the rest. The delegated part belongs to the sub-lessee alone while the sub-lease lives. Only path leases can
delegate. A resource lease cannot, and a sub-lease cannot delegate again.

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

While a sub-lease lives:

- A claim by anyone else on the delegated part is refused, and the error names both holders.
- The holder's own claim on the delegated part is refused too. The error names the sub-lessee, and the refusal can be
  waited out with `--wait`.
- The sub-lessee's claim inside its sub-lease reports that it is already covered. A claim that reaches outside is
  refused by the holder's lease.
- `acquire --replace` is refused on a sub-lease. A holder's `--replace` that would drop the delegated keys, or absorb a
  lease that has sub-leases, is refused and lists them.
- The holder that re-runs `acquire` with exactly its own keys exits with status 11 and lists the delegations. Status 11
  means that the holder still holds the lease, but does not own all of it right now.

When a sub-lease or its parent ends:

- The parent's holder can run `nitipan revoke <lease-id>` to end a sub-lease at once. The sub-lessee gets a tombstone
  that names the parent's holder, with the cause `revoked`. Anyone else is refused, and the error names the parent.
  Subagents share their parent's holder, so any subagent of the holder can revoke.
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

`list` shows the tree. The text output prints each sub-lease indented under its parent, and each pending request under
the lease it asks to share. A sub-lease whose parent is dead or gone prints at the top level, with its stored `parent`
link, until a locked call repairs it. A `list` that reads without the lock prints it that way too.

`list --json` has schema version 1.2, an additive change over 1.1. Consumers ignore fields they do not know. Every
lease stays a top-level entry in `leases`, so a consumer of the earlier schema still sees all of them.

- `parent` is on a sub-lease, and names its parent lease ID.
- `delegated` is on a parent that has live sub-leases. It lists each one as `{"id": ..., "keys": [...]}`. It is
  computed when `list` runs and is never stored. A lease with no live sub-leases carries no `delegated` field.
- `owner` and `grantor_of` appear only when `list` is filtered by a domain. An unfiltered `list` has no path to answer
  for, so it omits both. `owner` is true when the filter overlaps a part of the lease that no live sub-lease holds, and
  false when the match lies wholly inside the sub-leases. `grantor_of` lists the IDs of the live sub-leases whose keys
  the filter overlaps, and is absent when there are none. A parent that holds `/repo` and delegated `/repo/src` is the
  owner and the grantor when filtered by `/repo`. Filtered by `/repo/src`, it is only the grantor, and the sub-lease is
  the owner. A resource filter sets neither mark.
- `requests` lists the pending requests, each with its `id`, `parent` lease ID, `requester` holder (which carries the
  handle), `liveness`, `keys`, `description` when the requester gave one, and `requested_at`. A request that `grant`
  is already working on is not pending. A filtered `list` keeps the requests whose keys overlap the filter.
- `notices` lists the notices this call delivered, each with its `cause`, `target`, `paths`, `at`, and a `summary`
  sentence. Delivery removes a notice, so read this array when stderr is not captured.

`requests`, `notices`, and `tombstones` are always arrays, never null.

A sub-lease is its own record in `leases/`, with a `parent` field that names the lease it came from. The parent's record
does not change. Records that use `parent`, and requests and notices, carry record version 1.2. A lease that uses none
of them keeps version 1.0 and stays writable by older binaries.

## Waiting

`nitipan acquire --wait 5m` waits for a lease instead of being refused at once. It applies to path claims and resource
claims alike. The duration is required, and it is a Go duration such as `30s` or `5m`. A missing, unparseable, or
non-positive value is a usage error.

A wait polls with backoff, starting at 100ms and growing to a 2s pause. It takes the state lock only for each attempt
and releases it between attempts, so other commands run while a waiter polls. Only three outcomes are waited on: a live
holder overlaps the claim, the slot is full, or the lock is busy. A busy lock counts as a failed attempt, not as the
lock-busy exit. Every other refusal exits at once with its own status, such as an unusable record, a dead holder whose
record asks to be kept, an overlap with your own lease, or a `--slots` that differs from the count.

When the time runs out, `acquire` exits with status 10. The message names the holders it waited on. When the last
attempt found the lock busy, it names the process holding the lock too.

No file is created while waiting, so a waiter that is killed leaves nothing behind. A shell tool that kills a long
`acquire` may do so after the lease was published. Retry the same `acquire`. It reports the lease as already held. Or
run `list`.

There is no queue. A waiter can lose a freed slot to a claim that arrived later.

## Codex and opencode

Run `nitipan setup codex` or `nitipan setup opencode` to print what each agent needs. Neither command edits a settings
file.

For Codex, `setup` prints the sandbox write path, a SessionStart hook and a SessionEnd hook that run `nitipan hook`,
and `thread_unload_delay_secs = 60`. Codex fires SessionEnd only when it unloads a thread. The short delay means a
thread's background terminals and MCP servers stop 60 seconds after you switch away from it. Restart the Codex daemon
after you change the config. `acquire` refuses until the hooks and the delay are in force.

For opencode, `nitipan setup opencode --install-plugins <dir>` writes the TUI and server plugins into `<dir>/nitipan`,
with nitipan's absolute path baked in. It writes plugin files and nothing else. A session needs a TUI with the plugin
loaded before it can `acquire`.

## Relationship to coding_agent_standards

[coding_agent_standards](https://github.com/ripta/coding_agent_standards) defines guardians. A guardian is a long-lived
session that owns the changes in a domain. Today a guardian is found only by its session name, `<domain>_guardian`.
Nothing stops two guardians from claiming overlapping paths.

nitipan is the lease tool that protocol expects. nitipan owns the lease record format and the state directory layout.
coding_agent_standards owns the guardian protocol and the requestor rules. Guardians use nitipan when it is installed.
They fall back to session names when it is not.

## License

See [LICENSE](LICENSE).
