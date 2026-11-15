# Leases

A lease claims ownership of a domain for one session. The claim also advertises the session: what it does, where it
runs, and how to reach it. This page covers path leases, resource leases, reach, liveness, waiting, broker terms, and
discovery. [Sub-leases and recall](sub-leases.md) covers delegation. [The command reference](cli.md) lists every flag.

## Domains and path scopes

A domain is keyed by absolute path, with optional path scopes. `nitipan acquire` claims the working directory's
checkout. `--checkout <path>` names another one, and `--scope <dir>` narrows the claim to a path inside it. Repeat
`--scope` for several.

A claim against a dead holder succeeds. An overlapping claim against a live holder is refused, and the refusal names the
holder. Claiming a domain you already hold is a no-op that refreshes the advertisement. A flag you leave out keeps its
stored value. Keys are canonicalized and compared by the rule in
[ADR-03](../../spec/adrs/ADR-03-lease-key-canonicalization-and-overlap.md).

`acquire --replace <id>` rewrites your lease with that ID to the new domain. Repeat it to merge several of your leases
into one. `nitipan advertise` changes the session's name, permission mode, or a lease's description without naming keys
again.

`release` names a lease by ID, by the domain flags, by `--resource`, or with `--all`. The domain flags match a lease
whose stored canonical keys equal them, so the lease of a deleted worktree can still be released. With no target,
release refuses and lists your leases.

`break` removes another holder's lease by ID. A dead holder's lease goes without a flag when its record allows clearing.
A live holder's lease needs `--force`. So do a holder whose liveness is unknown, a dead holder's record that asks for a
newer nitipan, and a record or tombstone nitipan cannot use. Breaking a live holder's lease leaves a tombstone, and the
holder learns of it on its next nitipan call. It also leaves a fence mark. See [Git hooks and fence
marks](git-hooks.md).

A path claim inside a git checkout is refused until nitipan's git hooks are in force there. The error names the install
command. Resource claims and paths outside any checkout are not affected.

Leases live under `~/.local/state/nitipan/`. That directory must be on a local disk. A sandboxed agent needs a write
allowance for it, which `nitipan setup` prints. The home directory comes from the user database. `HOME` and
`XDG_STATE_HOME` are ignored, since two sessions that resolved the directory differently would both succeed in claiming
one path. [JSON output and records](json-and-records.md) describes the layout.

## Liveness

A Claude Code holder is alive while its process runs. nitipan checks the PID and the process start time, which guards
against PID reuse.

Codex and opencode run every session under one long-lived host process. Their holders are judged by session instead,
through the hooks and plugins that `setup` prints. A holder whose liveness reads unknown counts as alive. The design is
in [NIT-001 "Session liveness on shared hosts"](../../spec/proposals/NIT-001-local-lease-cli.md) and
[ADR-04](../../spec/adrs/ADR-04-lease-record-envelope-with-liveness-kind.md).

Run `nitipan setup codex` or `nitipan setup opencode` to print what each agent needs. Neither command edits a settings
file. [Setting up your agents](../getting-started/agents.md) walks through each agent.

For Codex, `setup` prints the sandbox write path, a SessionStart hook and a SessionEnd hook that run `nitipan hook`,
and `thread_unload_delay_secs = 60`. Codex fires SessionEnd only when it unloads a thread. The short delay means a
thread's background terminals and MCP servers stop 60 seconds after you switch away from it. Restart the Codex daemon
after you change the config. `acquire` refuses until the hooks and the delay are in force.

For opencode, `nitipan setup opencode --install-plugins <dir>` writes the TUI and server plugins into `<dir>/nitipan`,
with nitipan's absolute path baked in. It writes plugin files and nothing else. It refuses to overwrite a file that
differs from the one nitipan ships, unless `--replace` is given. A session needs a TUI with the plugin loaded before it
can `acquire`.

## Reach

`acquire --reach` says how far a lease reaches. It takes `machine`, `user`, or `shared`, and the default is `machine`.

A `machine` lease lives in the state directory, and the local lock decides between claims. The state directory is
authoritative at `machine` reach. When a node runs, it mirrors each local `machine` lease to the broker as a read-only
fact, so discovery on other machines can find it. The broker never grants, renews, or expires a `machine` lease. A
mirror whose node has gone quiet for longer than one term reads stale.

A `user` or `shared` lease is granted by the broker, through the node. A `user` lease covers all of one user's
machines. A `shared` lease reaches every bound user of the broker. [Broker authentication](broker-auth.md) covers how
the two reaches behave across users. A wider claim needs the node and a broker. See [Going multi-machine](../getting-started/broker-and-nodes.md).

A `user` or `shared` claim is made from a checkout. It first takes, or reuses, an ordinary `machine` lease on the
covered paths in that checkout, for the same holder. That lease is the anchor. The output of `acquire` names the
anchor, and says whether the claim took it new or reused it. When the anchor ends or narrows off the lease's paths,
the node ends the wider lease and writes its holder a notice. An ended lease is never revived. Releasing a `user` lease
releases an anchor its claim created and left unchanged. An anchor that was already held, widened, merged, or shared
with a second wider lease stays, and the release output says so.

A wider claim does not combine with `--resource`, `--replace`, `--wait`, or `--slots`. The broker never refuses a
`user` claim because of a mirrored `machine` lease. Overlaps between them appear in discovery, as a warning in the
output of the wider `acquire`, and as notices to the holders on both sides.

The design is in [NIT-004](../../spec/proposals/NIT-004-lease-broker.md), under "Source of truth for machine leases" and
"Machine leases and user leases on the same path". The [broker
research](../../spec/reports/2026-10-18-lease-broker-research.md) gives the background.

### Repo names

A `user` or `shared` lease needs a name every machine shares. Filesystem paths do not qualify. A repo is identified by
the set of root commits reachable from HEAD, so the name never depends on a remote URL.

The text form people type and read is `repo:<root-sha>#<repo-relative-path>`. Its root is the first-parent root, from
`git rev-list --max-parents=0 --first-parent HEAD`. `acquire` prints it for each name the lease covers. `list --json`
carries the whole root set. A typed name matches a holder whose stored set contains its root. Two names overlap when
their root sets intersect and their repo-relative paths overlap by whole components.

A claim must come from a checkout, which expands to the whole root set. A typed name with no checkout behind it serves
`who holds` and `release` only.

- The repo-relative path is made relative to the innermost checkout that contains the key
- A claim inside a submodule, or covering one, also emits an entry keyed by the submodule's own roots. A standalone
  clone of that submodule then meets it
- A claim covering a submodule that is not initialized on the claiming machine is refused, since its roots cannot be
  read
- A shallow clone, an unborn HEAD, and a repo with an `info/grafts` file are refused. Each error says what to do
- Every worktree of one repo shares its root set. A `user` lease on `repo:<root>#` therefore covers every worktree's
  files

Forks and repos cloned from one template share roots, so unrelated paths collide at `shared` reach. This fails closed:
the refusal names the holder. The accepted limits are listed in [NIT-004 "Name syntax for wider
reach"](../../spec/proposals/NIT-004-lease-broker.md).

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

Resource leases are advisory. They coordinate cooperating sessions only. A process that never calls nitipan can still
use every core.

Acquire resources from the parent session only. Subagents share their parent's holder, so two subagents that both claim
`cpu:heavy` both succeed. The first to release frees the name while the other still runs.

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

Each lease records the count it was admitted under. A new claim is admitted only while the live holders number fewer
than the smallest of the configured count and every live holder's recorded count. Only this machine's leases count.
Lowering a count takes effect at once. Raising it takes effect once every holder admitted under a lower count has
released.

A holder already on the name can claim it again at any time. That refreshes its lease and keeps its recorded count, even
when the slot is now over its count. A holder admitted under an old count therefore pins that count until it releases.
Ask the user to `break --force` it if it must go.

`--slots <N>` states the count you expect for the resource. A new claim is refused when `N` differs from the configured
count, and the error names the configured one. A repeat claim is compared with the count on the lease you already hold.

`list` shows, for each local resource lease, how many holders the name admits and which leases hold it. It also prints
the path of `slots.json`. `list --json` carries the same two values as `effective_slots` and `slot_holders` on each
local resource lease. See [JSON output and records](json-and-records.md).

`--wait` applies to resource claims, including a full slot.

## Waiting

`nitipan acquire --wait 5m` waits for a lease instead of being refused at once. It applies to path claims and resource
claims alike. The duration is required, and it is a Go duration such as `30s` or `5m`. A missing, unparseable, or
non-positive value is a usage error.

A wait polls with backoff, starting at 100ms and growing to a 2s pause. It takes the state lock only for each attempt
and releases it between attempts, so other commands run while a waiter polls. Only three outcomes are waited on: a live
holder overlaps the claim, the slot is full, or the lock is busy. A busy lock counts as a failed attempt, not as the
lock-busy exit. Every other refusal exits at once with its own status. Examples are an unusable record, a dead holder
whose record asks to be kept, an overlap with your own lease, and a `--slots` that differs from the count.

When the time runs out, `acquire` exits with status 10. The message names the holders it waited on. When the last
attempt found the lock busy, it names the process holding the lock too.

No file is created while waiting, so a waiter that is killed leaves nothing behind. A shell tool that kills a long
`acquire` may do so after the lease was published. Retry the same `acquire`. It reports the lease as already held. Or
run `list`.

There is no queue. A waiter can lose a freed slot to a claim that arrived later.

## Terms and renewal

Every broker lease has one fixed term of 5 minutes. The term is a constant in the broker, not a config field.
Sessions make no heartbeat calls. The node renews every local holder's broker leases in one heartbeat, at a third of
the term. It renews at once on wake and on reconnect. When a holder dies, the node reports it, and the broker ends its
leases before the term runs out. A holder whose liveness reads unknown counts as alive, so the node keeps renewing its
leases.

The broker reports a lease as `live` or `lapsed`. A lease lapses when its renewals stop, as on a laptop that went to
sleep. The broker keeps a lapsed lease rather than deleting it. The holder's next renewal revives it with the same
grant and the same fencing token, if nobody claimed its keys in the meantime. The node then writes the holder a notice
that says the lease was unprotected for a stated interval and nobody claimed it. When another holder claimed the keys,
the lease is lost, and the notice names the new holder. The holder's next nitipan call reports either notice.

Revival never applies to a lease that ended by release, `revoke`, `break`, or a reported death. A renewal for a lease
the broker no longer has returns "not found", and the node writes a loss notice that says the broker had no record of
it.

A lease the node wrote off whose renewal in fact reached the broker shows as live on the next snapshot. The node reports
it as restored, through the same kind of notice.

A command run while the node is disconnected says the holder's broker leases are at risk and when they lapse. Past that
time it says they lapsed. After the broker starts, or wakes from sleep, it expires nothing for one term, so every node
has a chance to renew.

`nitipan renew [lease-id ...]` renews broker leases through the node at once and prints each new expiry and token. With
no lease ID, it renews every broker lease you hold. A session never needs it, since the node already renews. It is for
a check that wants a fresh expiry before an action that must not outlive the lease. A lease the broker answers as lost
or ended exits with status 3. A lease the broker has no record of exits with status 8.

The design is in [NIT-004 "Lease term and renewal"](../../spec/proposals/NIT-004-lease-broker.md).

## Finding holders

The `who` commands ask the broker who holds leases. They need the node and a broker, and they find only sessions that
hold a lease.

`nitipan who holds <subject>` lists every holder whose lease overlaps the subject, on every machine. The subject is one
of:

- `repo:<root-sha>#<path>`, a repo name as `acquire` prints it
- a path on this machine. Inside a checkout it becomes the repo name plus this machine's path. Outside any checkout it
  searches only this machine's mirrored leases
- `<namespace>:<name>`, a resource name, matched on every machine
- `<handle>@<label>`, a qualified name, which lists that holder's leases

Each answer carries the holder's qualified name, reach, and state. A broker grant is live or lapsed. A mirrored machine
lease is mirrored, stale when its node has gone quiet, or unknown when its repo entries could not be derived. Unknown
never means no overlap.

`nitipan who is <name>` lists every holder whose agent name or handle is exactly the name, on every machine. A name
shared by more than one holder is flagged and never resolved to one. Stop and ask the user which one they mean.

`nitipan who holds` and `nitipan who is` print each holder's mail address. See [Mail](mail.md). Both take `--json`,
which [JSON output and records](json-and-records.md) describes.

`nitipan who am-i` prints the caller's user label through the node. The user label is what `acting_for`, cross-user
mail, delegations, and grants use to name users. See [Broker authentication](broker-auth.md).
