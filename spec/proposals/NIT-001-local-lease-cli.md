# NIT-001: Local Lease CLI

**Status:** accepted
**Created:** 2026-10-18
**Updated:** 2026-10-18

## Dependencies

None.

## Impacts

- NIT-002 — "Domain keys and overlap", "Claiming", "Lease record fields", "Wider-reach lease names", and "Invocation
  surface". Resource names are not paths, and a counted slot lets several live holders share one name
- NIT-003 — "Domain keys and overlap", "Claiming", and "Breaking a live lease". A sub-lease sits inside a live holder's
  domain, which the overlap rule refuses today
- NIT-004 — "Lease record fields", "Liveness", and "Wider-reach lease names". NIT-004 settles the deferred name syntax
- NIT-005 — "Breaking a live lease" and "Lease record fields". Tombstones are the only signal a broken holder gets
  until fencing exists, and a fencing token joins the record
- NIT-006 — "Holder identity" and "Lease record fields", specifically `holder` and `acting_for`
- NIT-007 — "Session name and permission mode". A mailbox can key on the holder's handle, the stable address a person
  learns from `list`
- NIT-008 — "Breaking a live lease" and "Record evolution and unknown records". A recall deadline on a tombstone must
  follow the minor-version rule, so an old reader never deletes it unread
- NIT-002 through NIT-008 — "Record evolution and unknown records". Every field they add must pass its minor-version
  rule or take a new major

Outside nitipan, the guardian protocol in coding_agent_standards
(`claude/project-management/guardian.md`, "Discovery Today") expects a lease tool to claim domains and detect overlaps.
This proposal is that tool's first version. Guardians read leases through `list --json`. See "A supported read path
for guardians".

## Summary

A `nitipan` CLI lets a coding agent session claim a domain on one machine. A domain is a checkout, optionally narrowed
to path scopes. The CLI has six commands: `acquire`, `release`, `list`, `break`, `advertise`, and `setup`. There is
no daemon. State is a directory of lease records, one per lease, under `~/.local/state/nitipan/`. A claim against a
dead holder succeeds. An overlapping claim against a live holder is refused, and the refusal names the holder. Each
record also advertises the session: what it does, where it runs, and how to reach it.

## Motivation

Guardians are found today by session name alone. A session named `<domain>_guardian` owns that domain. Nothing stops
two guardians from claiming overlapping paths. The user checks for overlaps by hand.

Sessions also have no shared way to say what they are doing. ListAgents gives a name and an idle or busy state. It does
not say which checkouts a session is working in, or whether it can commit there.

A lease file solves both on one machine. The claim is exclusive, so overlaps are caught when they happen. The record
carries the advertisement, so `list` answers "who owns this path, and what are they doing."

Later versions extend the same record across machines through a self-hosted broker. That work is out of scope here. It
is sketched in `spec/reports/2026-10-18-lease-broker-research.md`. This proposal fixes the record's envelope and its
evolution rules now, so later fields can arrive without breaking V1 readers or callers.

## Design Decisions (Settled)

### State directory

Lease records live under `~/.local/state/nitipan/`. nitipan owns this layout. It does not depend on
coding_agent_standards being installed.

Sandboxed sessions need a write allowance for the directory. coding_agent_standards 5b0e716 adds one to
`setup_permissions.py`'s managed list. Machines without coding_agent_standards need another path. See "Granting the
sandbox allowance".

### Commands

Amended 2026-10-18; see Decision Log.

- `acquire` claims a domain. It fails when a live holder owns an overlapping domain.
- `release` gives up a lease the caller holds.
- `list` prints every lease, with each holder's liveness.
- `break` removes another holder's lease. See "Breaking a live lease".
- `advertise` updates the caller's name, permission mode, or a lease's description. See "Session name and permission
  mode".
- `setup` prints the sandbox allowance entries for an agent. See "Granting the sandbox allowance".

### Domain keys and overlap

A domain entry is a checkout plus an optional list of path scopes, matching the guardian protocol's domain. The
checkout is an absolute path. nitipan canonicalizes it with symlinks resolved before it compares or stores anything.
Two worktrees of one repo are two checkouts, because they are two directories.

Amended 2026-10-18; see Decision Log.

Each scope expands to a key: the checkout joined with the scope. An entry with no scopes has one key, the checkout
itself. Two keys overlap when one is equal to the other, or is its ancestor by whole path components. `/a/b` overlaps
`/a/b/c`. It does not overlap `/a/bc`. A nested checkout is the one exception. See "Nested checkouts". Every relation
between keys uses this one rule: overlap, "covered by your leases", and later sub-lease containment.

### Holder identity

Each supported agent gets an adapter. The adapter finds the holder's session ID and PID from what the agent exposes to
child commands. The record names the agent, so the same session ID cannot collide across tools.

- Claude Code exports `CLAUDE_CODE_SESSION_ID` and `CLAUDE_PID` to every command. Both were observed in a session on
  2026-10-18.
- opencode 2.0.32's bash tool exports `OPENCODE_SESSION_ID`, `OPENCODE=1`, and `AGENT=1`, by a source reading of
  `packages/core/src/tool/plugin/shell.ts` at commit 501b15a. It exposes no PID variable that was found. Amended
  2026-10-18; see Decision Log.
- Codex 0.163.0 exports `CODEX_THREAD_ID`, `CODEX_SESSION_ID` for the root session, and `CODEX_VERSION` to shell
  commands, by a source reading of `codex-rs/protocol/src/shell_environment.rs` and `codex-rs/core/src/exec_env.rs` at
  tag rust-v0.163.0. `CODEX_THREAD_ID` survives `include_only` environment policies. It exposes no PID variable that
  was found.

Both are source readings, not live tests. Milestone 4 verifies them from inside each tool.

The Codex adapter keys its session ID on `CODEX_SESSION_ID`, the root session, so Codex subagent threads are one holder.
That matches Claude Code, whose subagents share their parent's holder. Where an environment policy strips that variable
and only `CODEX_THREAD_ID` survives, each thread becomes its own holder. Milestone 4 records which one each tested
policy gives.

Where the agent exposes no PID, the adapter walks up the process tree from nitipan's parent and stops at the agent's
process. Where it exposes no session ID, the session ID is empty, and the holder is the PID and start time alone.

When no adapter matches, `acquire` fails. Its error names the agents nitipan recognizes. There is no cross-vendor
standard for any of this. `AGENT` and `AI_AGENT` are proposed conventions that name the tool only.

### Liveness

A holder is alive when a process with its PID exists and that process's start time equals the recorded one. The start
time guards against PID reuse. No heartbeat is needed, and an idle session cannot heartbeat anyway.

Amended 2026-10-18; see Decision Log.

A record from another machine has unknown liveness, which counts as alive. A synced home directory or a broker mirror
can put another machine's record in the local state directory. A local PID check would read that live holder as dead.
The comparison uses the machine ID when both sides have one, and the host name only as a fallback. A laptop whose host
name changes with the network still recognizes its own records.

On macOS, nitipan reads the start time with the `kern.proc.pid` sysctl. On Linux, it reads `starttime` from
`/proc/<pid>/stat`. It never uses `kill(pid, 0)`. Inside the Claude Code sandbox on macOS, `kill` returns EPERM for a
live process, so it cannot tell "alive" from "not permitted." A spike on 2026-10-18 confirmed that the sysctl works
inside that sandbox. It returns the start time for a live PID and zero bytes for a dead one.

V1 supports macOS and Linux.

### Claiming

Amended 2026-10-18; see Decision Log.

`acquire` takes an exclusive `flock` on a single lock file in the state directory. Under the lock, it reads every
record, checks the new keys for overlap with each one, and publishes the new record. Publishing follows "Record
encoding and file naming": a temp file, then `link(2)`, which fails with `EEXIST` the way `O_EXCL` does. Then it
releases the lock.

A per-key atomic create alone would leave a race. Claims on `/a` and `/a/b` use different keys. Both could scan, find
nothing, and create. The lock closes that race. It is held for milliseconds. The kernel drops it when its process dies.

An overlapping record whose holder is dead does not block the claim when its `clear_when_dead` is true. `acquire`
removes it under the same lock, then creates its own. A dead holder's record with `clear_when_dead` false stays, and
refuses the claim. Its error says the record needs a newer nitipan to clear, names the version from its envelope, and
says to upgrade or ask the user. An overlapping record whose holder is alive refuses the claim. The error names the
holder's agent, handle, agent name, session ID, and description.

### Breaking a live lease

`break` against a dead holder needs no flag when the record's `clear_when_dead` is true. The lease could be claimed
anyway. When it is false, `break` needs `--force`, like a record of a higher minor.

`break` against a live holder refuses unless `--force` is given. With `--force`, it removes the lease and writes a
tombstone. The tombstone names the broken lease, who broke it, and when. `break` runs under the same lock as `acquire`.

Amended 2026-10-18; see Decision Log.

Tombstones are keyed by the broken holder's identity, not by the domain. A tombstone never blocks a claim, so the
breaker can claim the domain at once, unless another live lease overlaps it. A broken NIT-003 sub-lease's keys return
to its live parent, for example. Every nitipan call, `acquire` included, reads the tombstones addressed to its
caller, reports them, and then removes them. A broken holder that re-acquires learns its lease was broken. This is the
only signal a broken holder gets until fencing exists. `list` also reports a tombstone whose holder is dead once, then
removes it.

nitipan cannot trust the holder identity in a record it cannot use, so it cannot address a tombstone to that holder.
`break --force` on such a record writes no tombstone, and says so.

A tombstone nitipan cannot use, for any cause in "Record evolution and unknown records", never blocks a claim. `list`
shows it as unknown, and `break --force` removes it by its ID.

### Lease record fields

Amended 2026-10-18; see Decision Log.

Every record carries these fields from the first version. A field a later version adds defines its absent value as the
behavior from before the field existed. See "Record evolution and unknown records".

- `version`: the record format's version, as `major.minor`
- `id`: the lease ID, which is also the record's file name. See "Record encoding and file naming"
- `footprint`: what the record blocks, as typed entries, or `none`, or `all`. A V1 path record lists its canonical
  keys
- `clear_when_dead`: whether any reader may clear the record once its holder is dead. V1 writes true
- `reach`: how far the name is meaningful. One of `session`, `machine`, `user`, or `shared`. V1 writes only `machine`
- `domain`: a `kind`, then the fields that kind defines. V1 writes only kind `path`: the checkout and its scopes, as
  given, and the canonical keys derived from them. See "Wider-reach lease names"
- `holder`: the agent, session ID, handle, session name, PID, process start, and the OS user running the process. The
  process start is the tagged value in "Process start-time encoding". The handle and the name are in "Session name and
  permission mode"
- `acting_for`: the user the holder acts for. In V1 it always equals the holder's user. It differs only for a shared
  agent serving several users, which needs the broker
- `machine`: the host name, plus a stable machine ID where the OS has one
- `permission_mode`: the holder's permission mode
- `description`: free text saying what the session does
- `acquired_at`: when the lease was claimed
- `absorbs`: lease IDs a merging `--replace` folded into this one. Empty except after a merge. See "Replacing a lease"

### Record encoding and file naming

The question was how a lease record is encoded and named on disk. The candidates were a JSON file named by a hash of
its first canonical key, a JSON file named by an escaped form of the key path, and one shared index file rewritten
under the lock.

Decision (autonomous): one JSON file per lease, named by a generated lease ID, a ULID. The file is
`leases/<id>.json` in the state directory.

- A new record is written to `leases/.tmp-<id>`, fsynced, and published with `link(2)` to its final name. The temp
  file is then removed. A reader that does not take the lock, such as a guardian running `jq`, never sees a partly
  written record
- An update in place writes a temp file the same way and publishes it with `rename(2)`, since `link(2)` refuses an
  existing name
- Every scan and every reader skips names that start with `.tmp-`. Every write happens under the lock. So a temp file
  seen by a lock holder belongs to a writer that crashed, and the lock holder removes it
- `release` and `break` can address a lease by its ID
- Tombstones use the same scheme in `tombstones/<id>.json`. The broken holder's identity is inside the record. A
  caller finds its own tombstones by scanning that directory

The fsync is there because `rename(2)` and `link(2)` can reach the disk before the file's data does. A power loss could
then leave a published record that does not decode. Under "Record evolution and unknown records", such a record blocks
every claim.

The key-hash name was rejected. Scopes are stored as given, so the same domain claimed with scopes in a different order
hashes to a different name. NIT-002's counted slots let several holders share one name, so they would collide on it.
And a "first canonical key" means nothing for a non-path kind. The escaped path breaks on paths longer than the
255-byte file-name limit. The shared index loses every lease to one bad write. The overlap decision already comes from
the scan under the lock, so the file name does not need to encode keys.

### Process start-time encoding

The question was how Linux's start time is stored. macOS's `kern.proc.pid` sysctl reports an absolute start timeval.
Linux's `/proc/<pid>/stat` reports `starttime` in clock ticks since boot, and ticks can repeat across a reboot. The
candidates were the raw ticks plus the boot ID, and ticks converted to absolute time through `btime` with a tolerance.

Decision (autonomous): the holder's process start is one tagged value. Its `platform` field names the shape. Liveness
compares it exactly.

- On Linux it holds the boot ID from `/proc/sys/kernel/random/boot_id`, the `starttime` ticks, and the PID-namespace
  inode from `/proc/self/ns/pid` of the process that wrote the record
- On macOS it holds the start timeval, as seconds and microseconds
- A reader in a different PID namespace from the writer cannot see the holder's `/proc/<pid>`. That holder's liveness
  is unknown, not dead
- An unreadable `boot_id` or `stat` file also makes liveness unknown
- Unknown counts as alive, so the claim is refused rather than stolen. A human outside the namespace can still `break`
- The `stat` parser splits at the last `)`, because the process name can contain spaces and parentheses
- No display time is stored. `list` derives one when it prints, and `acquired_at` already says when the lease began

The `btime` conversion was rejected. `btime` moves when the realtime clock is stepped, such as by NTP or a VM resuming.
Every live holder would then read as dead, and its lease would be stolen. A stored display time was rejected too. It
duplicates `acquired_at`, and a direct reader might compare it by mistake. Start times are only compared on the machine
that wrote them, even under the broker, so one encoding across platforms gains nothing.

### Wider-reach lease names

V1 names domains by local path, at `machine` reach. A `user` or `shared` lease needs a name that machines share, such
as `repo:<remote-url>#services/billing`. The syntax waits on the broker design. The `reach` field exists now, so a V1
record is never ambiguous about what its name means.

Decision (autonomous): defer the name syntax, and make the domain typed now. `domain.kind` says what the name is. V1
writes only kind `path` at reach `machine`.

**Revisit:** when NIT-002 settles machine-reach resource names such as `cpu:heavy`; when NIT-004 settles the syntax for
`user` and `shared` names

Amended 2026-10-18; see Decision Log.

A typed domain does not let a V1 reader understand a V2 kind. What it buys is the other direction. A V1 path record
stays valid and unambiguous for every later reader, with no migration. An absent kind is never read as `path`. How a V1
reader treats a record whose kind or version it does not know is in "Record evolution and unknown records".

Deciding the syntax now was rejected. It would fix a contract while the relation between `machine` and `user` names
is still open in the broker research.

### Invocation surface

V1 has no daemon either way. The question was whether the one binary also runs as an MCP server. The candidates were
CLI only, CLI plus a stdio MCP mode such as `nitipan mcp`, MCP for the agent's operations with the CLI for humans, and
MCP only.

Decision (autonomous): CLI only in V1. The MCP mode is a decision to defer. Agents call nitipan through their shell
tool. Holder derivation lives in one module that does not depend on how nitipan was invoked.

**Revisit:** when milestone 4 finds that a Codex or opencode sandbox blocks the liveness sysctl, or blocks the
state-directory writes and no allowance can be configured; when a later proposal needs an MCP surface

A stdio MCP server has one environment and one parent PID for its whole life. MCP requests carry no caller-session
identity. So an MCP transport gives at most one holder per process. A CLI call gets a fresh environment on every
command. That is the only place a per-session ID such as `OPENCODE_SESSION_ID` can arrive. If one opencode process hosts
two sessions, an MCP server would give both the same holder. The second session's claim would then succeed as a
re-acquire, and exclusivity breaks silently.

Two transports were rejected for a second reason. Their holders can differ for the same session. A lease acquired
through MCP could not be released from the shell. An MCP server is also not a strict improvement for identity. A
wrapper such as `npx` or a shell can sit between the agent and the server, so the process-tree walk stays. The MCP
sandbox benefit is verified only in Claude Code, where the allowance already exists. MCP only leaves humans no way to
run `list` or `break`.

The deferral leaves Codex and opencode calling nitipan inside their own sandboxes. Their fallback is the write allowance
in "Granting the sandbox allowance". Milestone 4 records the data the hook needs: whether each agent passes its
session-ID variable to MCP servers, and whether opencode hosts one session or many per process.

### Re-acquiring as the same holder

A session may call `acquire` for a domain it already holds. The candidates were to succeed idempotently and update the
advertisement fields in place, or to refuse and leave updates to a separate command.

Decision (autonomous): `acquire` by the same holder, as "Holder match rule" defines it, has three cases. The check
against other live holders always runs first. A key another live lease holds is refused even when the caller's leases
cover it, as a key delegated to a NIT-003 sub-lessee is.

- The canonical key set equals one of the caller's leases. The call succeeds. It keeps the lease ID and `acquired_at`,
  and updates the advertisement fields through `rename(2)`
- Every new key is covered by the caller's leases, one or several together. Nothing changes. The call reports
  "already covered by your leases <ids>" with its own exit status
- Any other overlap with the caller's own lease is a real scope change. It is refused by default. The error names the
  caller's lease, and names `--replace <id>` only when the replace drops no keys. See "Replacing a lease"

Amended 2026-10-18; see Decision Log.

`acquire --replace <id>` rewrites the lease in place and keeps its ID. "Replacing a lease" has the mechanism.

In every case, a flag left out keeps the stored value. Re-acquiring to change only the description does not clear the
name.

Plain refusal was rejected. A retry, or an agent that forgot its own lease, would get an error. Silently replacing the
caller's overlapping lease was rejected. A guardian holding `/repo` that claims `/repo/docs` would lose `/repo/src`
without knowing. Telling the caller to release first was rejected too. For a covered claim it drops the caller's lease
for nothing. For a scope change it opens a window in which another session can take the domain.

### Session name and permission mode

No supported agent exports a session's display name or its permission mode. Claude Code's `/rename` name is the address
SendMessage uses. That makes it the most useful field in the advertisement. The candidates were optional `--name` and
`--permission-mode` flags on `acquire`, and the same flags with `--name` required. A third candidate came later:
optional `--name`, with nitipan generating a random name when it is absent.

Amended 2026-10-18; see Decision Log.

Decision (autonomous): the holder carries two name fields. They do different jobs.

- `holder.handle` is assigned by nitipan. It is four words derived from a hash of the holder's identity under "Holder
  match rule" plus the machine ID. It is the same on every call by that holder, so it survives `/clear`, idleness, and
  releasing every lease. A new process gets a new handle. A dead holder's handle comes back only on a hash collision.
  `acquire` and `list` print it. It is the stable address a person can learn from `list`, and the one NIT-007 can key
  on
- The handle is four words from a 2048-word list, about 44 bits per machine, so a collision is negligible. A collision
  with a different live holder, found under the lock, fails the call. A suffix was rejected: with no record of past
  handles, a suffixed holder would take the base handle once the other holder released, and senders would reach the
  wrong session
- `holder.name` is optional and self-reported, such as the `/rename` name that SendMessage takes. It is never generated
  and never forced unique. `list` flags a name shared by more than one live holder
- `list` and every error label the two fields as "handle" and "agent name"

Both flags stay optional, and a flag left out keeps its stored value. The name and the permission mode belong to the
holder, not to one lease. When a caller supplies either one, nitipan rewrites it in every live lease the caller holds.
That happens under the lock, one `rename(2)` per record. The description stays per lease.

- `nitipan advertise` refreshes the name, the permission mode, or a lease's description without re-supplying keys. A
  session runs it after `/rename`. See "Commands"
- `advertise` with no live lease refuses: "no lease to advertise on; pass --name to acquire"
- The rewrite is not atomic across records. A crash mid-rewrite can leave one holder under two names. The holder's
  next locked call brings every record back in line
- `permission_mode` takes one of the values the holder's adapter lists. The value is self-reported and advisory, and
  can go stale when the agent changes mode. An unknown value is refused, and the error lists the accepted ones

A required `--name` was rejected for now. Whether each agent can see its own session name is unverified. An agent that
cannot see it would have to invent one, and a wrong address is worse than an empty one. Refreshing per lease was
rejected. A session that refreshes one of two leases after `/rename` would show under two names.

A generated fallback in the name field was rejected. It writes a made-up value into the field people use as the
SendMessage address, so a reader cannot tell a generated name from a reported one. Unique only among live holders, a
name passes to a new holder once the old one renames or dies, and a sender who learned it reaches the wrong session.
Stored only in lease records, it would also change after the holder releases everything. The derived handle has none of
these problems. One field doing both jobs was the root cause.

### Holder match rule

`release` removes only a lease the caller holds. Re-acquiring and tombstone delivery also ask whether a record's holder
is the caller. No decision said which holder fields that comparison uses. The candidates were every identity field,
process identity only, process identity plus a non-empty session ID, and a per-adapter declaration of session-ID
stability.

Decision (autonomous): matching keys on whether one agent process can host several sessions. Process identity means
the agent, the PID, and the process start. Each adapter declares whether its agent can run several concurrent sessions
in one process.

- For a single-session agent, process identity alone decides the match. The session ID is advertisement. A matching
  call rewrites a changed session ID in the record, so a rotated ID never strands a live lease
- For a multi-session agent, or one not yet known, process identity and a non-empty session ID must both match. Such an
  agent with no session ID fails `acquire` closed
- When process identity matches but the session ID does not, the error says the lease is held by another session in
  this same process. It names that session's handle, and says to coordinate with it or ask the user

Amended 2026-10-18; see Decision Log.

Claude Code is declared single-session. Its subagents already share the parent's `CLAUDE_PID` and
`CLAUDE_CODE_SESSION_ID`, and `/clear` appears to rotate the session ID inside a running process. Codex and opencode
are declared in milestone 4, from what their checks find, when their adapters are built. Both export a session ID by
source reading, so a strict declaration still works for them. If a live test finds no session ID for an agent that
hosts several sessions per process, that agent's `acquire` fails closed. Whether V1 then ships without it is the
user's call.

No error names `break --force` as the fix. An agent follows the command its error names. Errors name the holder and
say to coordinate with it or ask the user.

Two limits hold under every candidate. Parallel subagents of one Claude Code session are one holder, so `acquire` does
not keep them apart. Liveness is per process, so on a multi-session host a session that ended inside a live process
leaves a lease that reads as alive until `break --force`.

Process identity alone was rejected. It is the one-holder-per-process flaw that "Invocation surface" rejected MCP for.
Requiring the session ID everywhere was rejected. A rotated ID would leave a live session unable to release its own
lease. Keying on ID stability was rejected. It falls back to process identity while an agent is unknown, which is
exactly when a multi-session host breaks exclusivity.

### Granting the sandbox allowance

coding_agent_standards grants the allowance on machines that have it. Other machines need their own path. Codex and
opencode each have their own sandbox and their own configuration, so the allowance must cover them too. "Invocation
surface" depends on this. Agents call nitipan from inside their sandboxes. The candidates were README documentation,
and a `nitipan setup` command that prints or applies the entries.

Decision (autonomous): `nitipan setup` prints the entries for the detected or named agent, and never writes settings.
The README documents the same entries. When any nitipan write fails with `EPERM` or `EACCES`, the error names the
detected agent, says the sandbox likely blocked the state directory, and prints the same text `setup` prints.

- "Any write" covers the lock file, temp files, records, and tombstone removal. `list` removes tombstones too, so it can
  hit this error
- `setup` prints every entry an agent needs. That includes a command-approval rule where the agent has one, not only the
  write path
- The message says "likely", because a plain file-mode failure gives the same errno

Writing settings with `--apply` was rejected for reasons beyond effort. Claude Code's sandbox denies writes to its own
settings file, so `--apply` would fail from inside a session. Codex's config is TOML and opencode's is JSONC. A
round-trip would drop comments in files nitipan does not own. Claude Code's settings already have two writers,
coding_agent_standards and Claude Code itself.

### Canonicalizing scope keys

Only the checkout was resolved. A scope that is a symlink, a scope that escapes the checkout, a scope that does not
exist yet, and two spellings of one path on a case-insensitive volume all produced keys that miss a real overlap. The
candidates were lexical joining, full resolution of each joined key, full resolution plus canonical case, and
comparison by device and inode.

Decision (autonomous): every key, the checkout's included, is canonicalized the same way.

- Scopes that are absolute or contain a `..` component are rejected lexically, before anything is resolved
- The longest existing ancestor of the joined path is resolved fully, with symlinks followed. On a case-insensitive
  volume it takes the filesystem's canonical case. On macOS that comes from `F_GETPATH` on an open descriptor
- A key that resolves outside its checkout is rejected
- The part that does not exist yet is appended. On a case-insensitive or normalization-insensitive volume it is
  compared with case folded and Unicode normalized. Each key records its volume's sensitivity, so every reader compares
  it the same way
- One limit stays. A symlink created later inside a not-yet-existing tail changes what the key names

The canonical keys are stored in the record, so the rule is part of the format. Changing it later would make old and
new keys compare wrong.

Lexical joining was rejected: a symlinked scope or `Docs` against `docs` gives two successful claims on one directory.
Device-and-inode comparison was rejected. A scope could not be claimed before it exists. A directory that is removed
and recreated, as build output often is, would silently lose its claim. And a guardian reading with `jq` cannot
interpret an inode.

### Adapter precedence

Agent environment variables are inherited. When Claude Code runs Codex, which runs nitipan, both adapters can match.
The candidates were a fixed adapter order, the nearest agent process, and refusing with an `--agent` flag.

Decision (autonomous): the nearest agent process above nitipan wins.

- An ancestor is an agent when an adapter's PID variable equals its PID, or when it matches that adapter's process
  signature. A name match alone is not enough, since Claude Code installed from npm runs as `node`
- The first ancestor that matches wins. That adapter's variables, the session ID included, are trusted only because it
  won. Inherited variables from an outer agent are ignored
- The nearest match is found first. A PID variable that names no ancestor matters only when no ancestor matches at all,
  such as for a `nohup` or tmux job. The error then names the variable and says it is not an ancestor. nitipan does
  not fall back to it
- A caller has no adapter when no ancestor matches and no agent's variables are present
- When no ancestor matches but agent variables are present, such as a `nohup` job that inherited `CODEX_SESSION_ID`,
  `acquire`, `advertise`, and `release` fail closed and name the stray variables. `list` and `break` run as an
  anonymous caller
- When the walk cannot read an ancestor, such as across a PID namespace, it fails closed

A fixed order was rejected. With Claude Code first, a lease taken by Codex would follow the outer Claude Code process's
liveness and outlive Codex. `--agent` was rejected as a knob whose only correct value is the nearest agent, and one
that lets a caller tie its lease to the wrong process.

### Waiting for the lock

A nitipan process that holds the lock and is stopped, or a laptop that sleeps mid-call, blocks every other call. The
candidates were blocking on `flock`, a bounded retry, and a bounded retry with a `--wait` flag.

Decision (autonomous): nitipan tries the lock without blocking and retries for a few seconds.

- Right after taking the lock, the holder writes its PID, process start, and command into the lock file. This is
  advisory only. `flock` stays authoritative
- On timeout, the error names that holder and says whether it is stopped. It names the PID only after the PID passes
  the liveness check. Otherwise it says the holder is unknown
- A lock-busy timeout has its own exit status, distinct from a refusal by a live holder
- `list` reads without the lock when the lock is busy, since records are published atomically. It skips tombstone
  cleanup and prints a warning
- A command that asks the user anything asks before taking the lock, then checks the state again under it

Blocking was rejected. A stopped holder would hang every call until the agent's shell timeout killed it. A `--wait`
flag was rejected. No caller needs a longer lock wait, and NIT-002 plans waiting for a lease, which would want the name.

### Resolving the state directory

`~/.local/state/nitipan/` could resolve through `$HOME`, `$XDG_STATE_HOME`, or the user database. Two sessions that
resolve it differently scan two directories, and both claims succeed. The candidates were those three sources.

Decision (autonomous): the home directory comes from the user database. `HOME` and `XDG_STATE_HOME` are ignored.

- A failed lookup is an error. nitipan never falls back to `$HOME`
- There is no override flag or variable. An inherited override would reopen the split. Tests inject the directory at
  the command layer
- Linux builds use cgo and never the `osusergo` tag. A static build reads only `/etc/passwd`, and fails for LDAP or SSSD
  users
- nitipan creates a missing state directory with mode 0700, and refuses one the caller does not own. A sandbox may
  block the `mkdir`, so `setup` also prints the command that creates it
- `setup` and every sandbox error print the absolute resolved path, not `~`
- Leases are per OS user. Two users on one machine never see each other's leases, though records say reach `machine`

`$HOME` was rejected. A sandbox or wrapper that changes it silently splits state. `$XDG_STATE_HOME` was rejected too. A
user whose shell sets it would see no agent leases from the terminal, since an agent launched from the Dock does not
inherit it. It would also miss the sandbox allowance, which names `~/.local/state/nitipan`.

### Callers with no adapter

Humans run `list` and `break` from a terminal, where no adapter matches. A tombstone must name who broke a lease, and
every call looks up its caller's tombstones. The candidates were recording a terminal caller by OS user and parent
process, the same plus a confirmation prompt, and a terminal pseudo-adapter that can hold leases.

Decision (autonomous): every command that acts as a holder needs an adapter. In V1 those are `acquire`, `advertise`,
and `release`. A later proposal's holder commands, such as NIT-003's `request`, `grant`, `decline`, and `revoke`, join
the rule. Commands that only read or break, `list` and `break`, do not. A caller with no adapter is
recorded as an anonymous process, never as a session or as a person. Its identity is its OS user, plus its parent's
PID, process start, and process name, the same shape as a holder.

- The no-adapter path never runs the process-tree walk
- A caller with no adapter holds nothing, so it has no tombstones to receive
- `release` without an adapter refuses. Its error may name plain `break` for a dead holder's lease, never `--force`
- `break` takes lease IDs only, so a destructive action always names its exact target

The prompt was rejected. Lease IDs are ULIDs that are never reused, so a stale or mistyped ID matches nothing, and there
is no wrong target to catch. A TTY also does not reliably mean a person. The pseudo-adapter was rejected. Nobody needs
humans to hold leases, and a shell's lifetime is a poor proxy for intent.

### A supported read path for guardians

Guardians read records with `jq`, but liveness needs a sysctl or a `/proc` parse. A guardian reading files alone treats
a dead holder's record as a live claim. The candidates were files only, `list --json` as the supported interface, and
`list --json` with the files also documented for direct reads.

Decision (autonomous): `list --json` is the supported read interface. The record files are nitipan's own format.

- The output schema evolves like the record envelope. A minor bump adds fields or new `domain.kind` values. Consumers
  ignore fields they do not know, and treat a lease of a kind they do not know as opaque, judged only by its liveness
  and footprint. A major bump breaks compatibility. NIT-003's `parent`, `delegated`, ownership marks, and `requests`,
  and NIT-002's `resource` kind, are minor bumps. Amended 2026-10-18; see Decision Log
- The output has its own versioned schema, separate from the record format. Each lease carries its computed liveness:
  `alive`, `dead`, or `unknown`. The schema says `unknown` means treat as alive. Unusable records appear as unknown
  entries
- `list` takes the same domain arguments as `acquire`, and returns the leases that overlap them, using nitipan's own
  canonicalization and overlap rule
- Tombstones that `list` delivers or reaps appear in the output, and on stderr too. A guardian that discards them still
  leaves a trace
- `list` writes its read output first. A failed tombstone removal is reported, not fatal

Files only was rejected: a reader cannot apply liveness or the unknown-record rule. Documenting the files too was
rejected. It doubles the contract, and every reader would have to reimplement liveness and overlap. A guardian doing
its own prefix match in `jq` gets `/a/b` against `/a/bc` wrong.

### Releasing

`release` can take a lease ID. Nothing said what it does with no argument, or with a path. The candidates were IDs
only, IDs or domain arguments with a bare `release` acting on the caller's only lease, and IDs or domain arguments with
a bare `release` refusing.

Decision (autonomous): `release` takes lease IDs, or the same domain arguments as `acquire`. It never acts on a target
the caller did not name.

- Domain arguments match a lease whose canonical key set is equal. They work from the stored keys, so a deleted
  worktree can still be released
- Domain arguments that overlap a held lease without equaling it are refused, and the error names the candidate IDs
- With no argument, `release` refuses and lists the caller's leases
- `--all` releases every lease the caller holds. Subagents share their parent's holder, so `--all` from a subagent
  releases the parent's leases too. Its help text says so
- An ID that names another holder's lease reports "not yours"
- An ID with an undelivered tombstone for the caller reports "broken", with who broke it
- Any other ID reports "no such lease" and lists the caller's current lease IDs. A released ID leaves no trace, so
  nitipan cannot tell it from a mistyped one, and never claims "already released"
- Each of these has its own exit status

A bare `release` that acts on the only lease was rejected. A subagent that acquired nothing could clean up and release
its parent's lease without any error.

### Replacing a lease

`--replace` was suggested for any scope change, including one that drops keys. It checked only other holders. A crash
between publish and removal left two records that nothing reconciled. The candidates were keeping it, a `replaces`
link reaped by later calls, and the same with a flag for a replace that drops keys.

Decision (autonomous): `--replace` rewrites the lease in place and keeps its ID. See "Re-acquiring as the same holder".

- `--replace` takes one or more of the caller's lease IDs. The first is rewritten to the new keys, and its record names
  the other IDs in an `absorbs` field, written in the same `rename(2)`. The other records are then removed under the
  same lock, which merges them. A crash after the rewrite leaves them in place. Any locked call removes the records a
  live record's `absorbs` names, and nothing else. Leftovers are never inferred from coverage. A lease on a nested
  worktree can become covered when the worktree is removed, and it must survive that
- Under the lock, the new keys are checked against every other holder, and against the caller's leases not named in
  `--replace`
- The new record is written to a temp file and published with `rename(2)` over `<id>.json`, the same way an update in
  place works. A crash leaves the old record or the new one, never both
- `acquired_at` is kept
- The self-overlap refusal names `--replace` with every one of the caller's leases the claim overlaps, and only when a
  dry run of that replace would pass and drop no keys. When it would drop keys, the refusal lists them and names the
  claim that drops nothing, such as acquiring only the new keys. Otherwise it says to coordinate or ask the user

The `replaces` link was rejected. It needs a reap ordered before the overlap scan, transitive resolution of crash
chains, and a `break` that also removes what a record replaces. It would also strand NIT-003's parent links on a new
ID. Rewriting in place removes the crash window instead of cleaning up after it. A drop flag was rejected. An agent
would paste it from the error, which drops the keys anyway.

### Record evolution and unknown records

Downstream drafts add record fields: a slot count in NIT-002, a parent link in NIT-003, an expiry in NIT-004, a fencing
token in NIT-005, and a recall deadline on tombstones in NIT-008. Under the earlier rule, each one bumped `version`, and
every such record blocked every V1 claim until someone ran `break --force`, even after its holder died. The candidates
were keeping that rule, a stable envelope, the envelope with `major.minor` versioning, the envelope with exposed path
keys, and a single integer version whose readers ignore unknown fields.

Amended 2026-10-18; see Decision Log.

Decision (autonomous): records and tombstones share one envelope, versioned `major.minor`, with rules for old readers
and old writers. Every part of it ships in V1, because a V1 binary can never learn a forward-compatibility check later.

The envelope is the part every version keeps readable:

- `version`, `id`, `machine`, and the holder's agent, PID, and process start. The process start's platform shapes are
  frozen. A platform tag a reader does not know makes liveness unknown
- `footprint`: typed entries, `none`, or `all`. Path entries carry each key's volume sensitivity and use the path rule,
  "Nested checkouts" included. Name entries use exact match. A path entry and a name entry never overlap. A later
  entry type defines how it pairs with every older type when it is introduced. An entry type a reader does not know
  counts as `all`. An absent footprint counts as `all`. `none` is never inferred from the kind. Amended 2026-10-18;
  see Decision Log
- `clear_when_dead`: default true. A writer sets it false when a holder's death carries duties an old reader cannot
  perform, such as a record shared by several holders, which must outlive any one of them

Readers follow these rules:

- A reader accepts a higher minor of its own major and ignores fields it does not know. It treats a higher major, or a
  record whose envelope does not decode, as unusable
- A minor version may add a field only if leaving it out never makes an old reader more permissive. That covers every
  decision a reader makes: claiming, clearing a dead record, delivering a tombstone, releasing, and `break`. A field
  that fails this test needs a new major
- A reader that knows a record's kind checks that the footprint equals the canonical keys stored in the body. It never
  resolves paths again for this check, so a filesystem change cannot turn a record unusable. A mismatch
  makes the record unusable
- A record whose body is unknown is judged by its envelope. It blocks only claims its footprint overlaps. When its
  holder is dead and `clear_when_dead` is true, a lock holder clears it like any dead lease. Otherwise it stays

Writers follow these rules:

- An old binary never rewrites a record of a higher minor. Every rewrite path refuses: re-acquire, `--replace`,
  `advertise`, and the holder-wide name and mode rewrite. The holder-wide rewrite checks every record before its first
  `rename(2)`, so it never leaves a partial rewrite
- `break` on a record of a higher minor needs `--force`, even when the holder is dead
- `release` of a caller's own record of a higher minor removes it. Removing is not a rewrite. An old `release` skips
  any release-time duty a newer field carries. A field whose skipped duty would make an old reader more permissive
  fails the minor rule above, and needs a new major. A skipped informational duty, such as a notice to a parent, does
  not
- The state directory has a `format` marker naming the minimum writer version. An absent marker means the V1 format. A
  binary below the marker, or one that cannot decode it, refuses every write and still reads and lists. The marker is
  checked under the lock

An unusable record is handled the same way for every cause:

- `acquire` refuses the claim, since it cannot tell whether the record overlaps. The error names the file, and says to
  upgrade nitipan or ask the user
- `list` shows it as unknown, with its file name and the raw fields it could read
- `break --force` removes it by lease ID
- `release` cannot act on it. `release <id>` naming it reports "unusable record; upgrade nitipan or ask the user", with
  its own exit status

The earlier rule was rejected. A dead holder's NIT-002 `cpu:heavy` record would block every V1 path claim forever.
Ignoring unknown fields without the writer rules was rejected. A V1 binary on PATH that runs `advertise` would rewrite a
newer record and silently drop its parent link, expiry, or token. Clearing on the envelope alone was rejected. A
NIT-002 counted slot shared by several holders names one of them in its envelope. Clearing it when that one is dead
would wipe the slots of holders still alive. Inferring "blocks nothing" from the kind was rejected,
since NIT-004 has not settled whether a `user` lease overlaps local checkouts. A single integer version with no
envelope leaves a dead holder's newer record blocking forever.

### Nested checkouts

Claude Code puts worktrees under `.claude/worktrees/` inside the repo. A worktree there is its own checkout, but under
the ancestor rule a lease on the parent checkout overlapped it. The candidates were keeping the ancestor rule, a
checkout root stored with each key, the same for same-repo worktrees only, and comparing keys only within one
checkout.

Decision (autonomous): the ancestor rule stands, with one exemption computed at check time. Two keys where one is an
ancestor of the other do not overlap when a git checkout boundary lies strictly below the ancestor and at or above the
descendant.

- A boundary is a directory holding a `.git` file or directory. Worktrees and independent nested repos are boundaries
- A submodule of the enclosing repo is not a boundary. It is known from a gitlink in the repo's index or an entry in
  `.gitmodules`. The parent's `git submodule update` and `--recurse-submodules` checkouts write into it, so it stays
  part of the parent
- Boundaries are found from the filesystem under the lock, at check time. Nothing is stored, so nothing goes stale
- The same rule decides "covered by your leases". A guardian holding `<repo>` that claims a nested worktree gets a new
  lease, not "already covered"

Limits:

- A `.git` created at the same moment as a claim races with it, as a symlink in a not-yet-existing tail does
- A worktree removed and replaced by a plain directory while both leases are held leaves two leases on one directory.
  The check runs only at claim time
- A bare repo whose `core.worktree` points elsewhere, such as dotfiles in `$HOME`, has no `.git` at its working tree's
  root, so it is not seen as a boundary

The plain ancestor rule was rejected. It blocks the guardian protocol's own pattern of a separate session in its own
worktree. A guardian could not exclude one path except by listing every other directory as a scope. Storing a root
with each key was rejected. A stored root goes stale when a worktree is removed. B holds worktree `/repo/w` with root
`/repo/w`. The worktree is removed, and `w` becomes a plain directory. C then claims `/repo`. The stored roots differ,
so C succeeds and covers B's directory. The chosen rule finds no boundary there and refuses C. Comparing only within
one checkout misses a scope that crosses into a nested checkout.

## Design Decisions (Open)

None.


## Risks

- **The record envelope and evolution rules are a one-way door** — likelihood: high, impact: medium.
  Every V1 binary in the field applies the envelope, footprint, minor-version, and `format` marker rules forever, and
  cannot learn new ones. Mitigation: "Record evolution and unknown records" fails safe on every unknown: an unknown
  major, footprint entry type, or platform tag. A later breaking change raises the `format` marker, which locks old
  writers out while they still read. Guardians read through the versioned `list --json` schema, not the files.
- **A broken holder keeps working until its next nitipan call** — likelihood: medium, impact: high.
  `break --force` removes the lease, but nothing stops the old holder from committing in the meantime. Accepted
  (autonomous): V1 has no fencing, and a tombstone is the only signal until NIT-005 lands. `break --force` already needs
  an explicit flag and an exact ID.
- **Linux liveness inside an agent's sandbox is untested** — likelihood: medium, impact: high.
  If Claude Code's Linux sandbox unshares the PID namespace per command, every record reads as unknown, which counts as
  alive, and dead holders are never reclaimed without `break`. Mitigation: milestone 1 checks liveness inside the
  Claude Code sandbox on Linux, and milestone 4 inside the Codex and opencode sandboxes. The failure is safe: claims are
  refused, never stolen.
- **opencode and Codex support rests on source readings, not live tests** — likelihood: low, impact: medium.
  Both export a session ID by source reading. If a live test disagrees, and the agent hosts several sessions per
  process, its `acquire` fails closed. Mitigation: milestone 4 verifies the variables from inside each tool. Whether V1
  then ships without that agent's support is the user's call.
- **A session that ends inside a live multi-session process leaves a lease that reads as alive** — likelihood: medium,
  impact: medium. Liveness is per process. Accepted (autonomous): the lease is visible in `list` with its handle, and a
  human can `break --force` it. No per-session liveness signal exists to do better.
- **An unsupported agent between a supported one and nitipan is misattributed** — likelihood: low, impact: medium.
  The nearest recognized ancestor wins, so a lease from an unsupported agent follows its supported parent's liveness.
  Accepted (autonomous): nitipan cannot recognize an agent it has no adapter for, and the lease fails safe by outliving
  its real holder.
- **The process-tree walk finds the wrong holder** — likelihood: medium, impact: medium.
  A wrapper process or a renamed binary could stop the walk too early or too late. Mitigation: milestone 4 verifies
  each adapter inside its own tool. `list` shows the PID and process name, so a wrong holder is visible.
- **Another agent's sandbox blocks the liveness sysctl** — likelihood: low, impact: high.
  Codex and opencode have their own sandboxes. If one blocks the sysctl, nitipan cannot tell a live holder from a dead
  one. Mitigation: an unreadable liveness result counts as alive, so the claim is refused rather than stolen. Milestone
  4 tests the sysctl inside each tool.
- **The state directory is on a network filesystem** — likelihood: low, impact: high.
  `flock` and `O_EXCL` are unreliable on some network filesystems. Two claims could both succeed. Accepted: the
  directory is under the user's home on a local disk by default. The README says so. A home shared across machines no
  longer makes remote holders read as dead, since a record from another machine has unknown liveness.
- **Tombstones pile up for holders that never return** — likelihood: medium, impact: low.
  A broken holder that dies before its next call never reads its tombstone. Mitigation: `list` removes tombstones
  whose holder is dead, after reporting them once.

## Milestones

### M1: Record format and liveness

**Status:** NOT STARTED

- The repo has a Makefile per the Makefile standard, with `build`, `test`, `lint`, and `ci` targets
- Lease records encode and decode every field in "Lease record fields". A higher major, an undecodable envelope, or a
  footprint that disagrees with the body makes a record unusable
- A record of a higher minor is read with its unknown fields ignored, judged by its footprint, cleared only when its
  holder is dead and `clear_when_dead` is true, and never rewritten by an older binary
- An unknown footprint entry type, an absent footprint, and an unknown process-start platform each fail safe
- A path footprint entry never overlaps a name footprint entry
- `list --json` emits a lease of an unknown kind with its liveness and footprint
- A `format` marker above the binary's version refuses every write and still allows `list`
- A record from another machine has unknown liveness
- Overlap and coverage treat a nested worktree or nested repo as separate and a submodule as part of the parent, with
  the boundary found at check time
- Path canonicalization resolves symlinks, and the overlap check passes table tests for equal, ancestor, sibling, and
  shared-prefix keys
- Canonicalization rejects absolute and `..` scopes, and rejects a symlinked scope that resolves outside the checkout
- On a case-insensitive volume, two spellings of one existing path give one key, and two spellings of a not-yet-existing
  tail compare equal
- The adapter walk picks the nearest agent ancestor, ignores an inherited outer agent's variables, and fails closed with
  a named error when a PID variable names no ancestor and no ancestor matches
- With stray agent variables and no matching ancestor, `acquire`, `advertise`, and `release` fail closed, while `list`
  and `break` run as an anonymous caller
- A lock held past the retry budget gives its own exit status, and the error names the holder and whether it is stopped
- The liveness check reports alive, dead, and PID-reused correctly on macOS and on Linux
- On Linux, the liveness check reports unknown for an unreadable `boot_id` or `stat` file and for a PID-namespace
  mismatch, and parses a `stat` line whose process name contains spaces and parentheses
- The liveness check works inside the Claude Code sandbox on macOS and on Linux, and the Linux result is recorded in the
  As built block

### M2: Acquire, release, and list

**Status:** NOT STARTED

- `acquire` claims a domain under the lock, and publishes its record through a temp file and `link(2)`
- A reader never sees a partly written record, and a lock holder removes temp files left by a crashed writer
- `acquire` refuses a claim while any record cannot be used, and its error names the file
- `acquire` replaces an overlapping lease whose holder is dead
- `acquire` refuses an overlapping lease whose holder is alive, and the error names the holder
- Two concurrent `acquire` calls on overlapping keys never both succeed, in a test that races them
- `acquire` by the same holder is idempotent for an equal key set, reports a covered key set without changing it, and
  refuses any other self-overlap unless given `--replace <id>`
- `acquire --replace` keeps the lease ID and `acquired_at`, refuses overlap with the caller's other leases, and leaves
  exactly one record when killed mid-replace
- The self-overlap refusal names `--replace` only when the replace drops no keys
- `release` takes IDs or domain arguments, refuses with no argument, refuses a partial match and names the candidates,
  releases a deleted worktree's lease by its stored keys, and gives distinct results for an ID that is not found
- `list --json` emits its versioned schema with liveness per lease, filters by domain arguments, and includes the
  tombstones it delivered or reaped
- The state directory comes from the user database, ignores `HOME` and `XDG_STATE_HOME`, fails closed when the lookup
  fails, and is created with mode 0700
- A caller with no adapter can run `list` and `break`, is recorded as an anonymous process, and cannot `release`
- A flag left out keeps its stored value
- `advertise`, or a name or mode given to `acquire`, rewrites that field in every live lease the caller holds
- `advertise` with no live lease refuses, and says to pass `--name` to `acquire`
- A holder's handle is the same across calls, re-acquires, and a full release, and differs for a new process
- A handle collision with a different live holder fails the call, in a test that forces the collision
- A dead holder's record with `clear_when_dead` false refuses the claim, names the needed version, and needs
  `break --force`
- `--replace` with several IDs merges them, and a crashed merge's leftovers are removed by the next locked call through
  `absorbs`
- A lease on a nested worktree survives when the worktree is removed and the holder's other lease covers it
- The self-overlap refusal suggests `--replace` only when a dry run of it passes
- `release` reports "no such lease" with the caller's lease IDs for an unknown ID, and never claims "already released"
- A host-name change does not make a machine's own records read as remote
- `acquire` and `list` print the handle, and label it apart from the agent name
- `permission_mode` accepts only the adapter's listed values
- `release` removes only a lease the caller holds
- `list` prints every lease with its holder's liveness, and flags a name shared by more than one live holder
- The Claude Code adapter identifies the holder from `CLAUDE_PID` and `CLAUDE_CODE_SESSION_ID`
- The Claude Code adapter is declared single-session, and a call with a rotated session ID still matches its own live
  lease and rewrites the ID
- A multi-session or unknown adapter matches only when the session ID matches too, and fails `acquire` without one
- A write blocked with `EPERM` or `EACCES` prints the detected agent's entries, the same text `nitipan setup` prints

### M3: Break and tombstones

**Status:** NOT STARTED

- `break` removes a dead holder's lease without `--force`
- `break` refuses a live holder's lease without `--force`, and names the holder
- `break --force` leaves a tombstone keyed by the broken holder, naming the lease, the breaker, and the time
- The breaker can `acquire` the domain immediately after `break --force`, when no other live lease overlaps it
- `release` removes the caller's own record of a higher minor, and the other-holder check runs before the coverage
  check
- A broken holder's next nitipan call reports that its lease was broken, and by whom, then removes the tombstone
- `list` reports a tombstone whose holder is dead once, then removes it
- A broken holder that calls `acquire` next is told its lease was broken
- `break --force` removes a record nitipan cannot use, writes no tombstone, and says so
- `list` shows a record nitipan cannot use as unknown, and `release <id>` on one reports an unusable record
- An unusable tombstone never blocks a claim, shows as unknown in `list`, and is removable with `break --force`
- No error message names `break --force` as its fix

### M4: Codex and opencode adapters

**Status:** NOT STARTED

- The opencode adapter's session ID and PID are verified from inside an opencode bash tool call
- The Codex adapter's PID, `CODEX_THREAD_ID`, and `CODEX_SESSION_ID` are verified from inside a Codex shell command
- The As built block records which Codex session variable each tested environment policy leaves
- The liveness sysctl is verified inside each tool's sandbox, and the result is recorded in the As built block
- The As built block records whether each agent passes its session-ID variable to MCP servers, and whether opencode
  hosts one session or many per process
- The As built block records whether Codex hosts one session or many per process
- The Codex and opencode adapters' session declarations match what those checks found
- With the entries `nitipan setup` prints, `acquire` succeeds inside each agent. Without them, the sandbox error appears
- The Codex and opencode adapters each define their list of `permission_mode` values
- With no matching adapter, `acquire` fails and names the agents nitipan recognizes

## Decision Log

- 2026-10-18: Lease records live at `~/.local/state/nitipan/`, not under `~/.local/state/coding_agent_standards/`.
  nitipan must work without coding_agent_standards. coding_agent_standards 5b0e716 adds the sandbox allowance.
- 2026-10-18: Overlapping claims serialize on one global `flock`. Per-component locks were rejected as complex, with no
  contention to justify them.
- 2026-10-18: V1 supports Claude Code, Codex, and opencode through per-agent adapters.
- 2026-10-18: Liveness never uses `kill(pid, 0)`. It returns EPERM for live processes inside the Claude Code sandbox.
- 2026-10-18: ADR candidate (hard to reverse): the lease record's fields and versioning. Waits on the record encoding
  open question and on the broker research, since the broker may still add fields. Record encoding settled on
  2026-10-18. Written as ADR-02 on 2026-10-18, once "Record evolution and unknown records" let broker fields arrive as
  minor versions.
- 2026-10-18 (autonomous): Records are one JSON file per lease, named by a ULID lease ID, published through a fsynced
  temp file and `link(2)`, and updated with `rename(2)`. Tombstones use the same scheme. Key-derived names break on
  scope order, counted slots, and non-path kinds.
- 2026-10-18 (autonomous): The process start is one tagged value compared exactly. On Linux it holds the boot ID,
  ticks, and PID-namespace inode. On macOS it holds the start timeval. Unknown liveness counts as alive. `btime`
  conversion was rejected because clock steps would make live holders read as dead.
- 2026-10-18 (autonomous): Wider-reach name syntax is deferred to NIT-002 for resource names and NIT-004 for `user` and
  `shared` names. The domain carries a `kind` now. A record nitipan cannot use blocks `acquire`, shows as unknown in
  `list`, and is removable with `break --force`.
- 2026-10-18 (autonomous): Amended "Claiming". It said `acquire` "creates the new record with `O_EXCL`." It now
  publishes through a temp file and `link(2)`, so a reader without the lock never sees a partly written record.
- 2026-10-18 (autonomous): Amended "Lease record fields". It added `id`, typed `domain` by `kind`, and made the process
  start a tagged value. Before, `domain` held only the checkout and scopes, and the start time had no stated shape.
- 2026-10-18 (autonomous): V1 is CLI only, and the MCP mode is deferred. Revisit when milestone 4 finds that a Codex or
  opencode sandbox blocks the liveness sysctl, or blocks the state-directory writes and no allowance can be configured,
  or when a later proposal needs an MCP surface. A stdio MCP server gives at most one holder per process, and two
  transports can give one session two holders.
- 2026-10-18 (autonomous): Added the open question "Holder match rule", found while settling "Invocation surface".
  Widened "Granting the sandbox allowance" to Codex and opencode.
- 2026-10-18 (autonomous): Re-acquiring by the same holder is idempotent for an equal key set and a no-op for a covered
  one. A real scope change needs `--replace <id>`, which publishes before it removes. Telling the caller to release
  first would open a window for another session.
- 2026-10-18 (autonomous): `--name` and `--permission-mode` are optional. A new value applies to every live lease the
  holder has. A flag left out keeps its value. `permission_mode` takes adapter-listed values. A required name waits on
  evidence that agents can see their own names.
- 2026-10-18 (autonomous): Amended "Commands" and the Summary to add `advertise` as a fifth command. Before, the CLI had
  four commands, and an advertisement could change only through `acquire`.
- 2026-10-18 (autonomous): Holder matching keys on whether an agent can host several sessions in one process.
  Single-session agents match on process identity and refresh the session ID. Others also need a matching non-empty
  session ID. Claude Code and Codex are declared single-session. opencode stays strict until milestone 4.
- 2026-10-18 (autonomous): `nitipan setup` prints each agent's allowance entries and never writes settings. Every
  blocked write prints the same entries. `--apply` was rejected because the Claude Code sandbox denies writes to its own
  settings, and the other agents' formats carry comments a round-trip would drop.
- 2026-10-18 (autonomous): Reopened "Session name and permission mode" for a new candidate from the NIT-007 author: a
  random name generated when `--name` is absent. The relayed claim that the user accepts any stable name is not yet
  confirmed by the user.
- 2026-10-18 (autonomous): Amended "Session name and permission mode" and "Lease record fields". The holder gains
  `handle`, a nitipan-assigned name derived from holder identity and the machine ID. `name` stays self-reported. Before,
  one optional name field was the only address. A generated name in that field was rejected because it fakes a
  SendMessage address and can pass to another holder.
- 2026-10-18 (autonomous): Coherence fixes. Amended "Breaking a live lease": it said `acquire` "never reads or removes"
  tombstones and also that every call reads them. Now a tombstone never blocks a claim, and every call, `acquire`
  included, delivers it. Added `setup` to "Commands" as a sixth command. Covered re-acquires count the caller's leases
  together.
- 2026-10-18 (autonomous): Amended "Holder match rule". It declared Codex single-session because Codex exposes no
  session ID, which says nothing about how many sessions a process hosts. Codex and opencode are now declared in
  milestone 4 from evidence. Errors no longer name `break --force` as the fix, since an agent follows the command its
  error names.
- 2026-10-18 (autonomous): Every key, checkout included, is canonicalized: lexical rejection of absolute and `..`
  scopes, full resolution with canonical case of the longest existing ancestor, and a case-folded, normalized tail.
  Lexical joining let a symlink or a case variant give two claims on one directory.
- 2026-10-18 (autonomous): The nearest agent ancestor wins adapter precedence, recognized by PID variable or process
  signature. Inherited outer variables are ignored, and a PID variable that names no ancestor fails closed.
- 2026-10-18 (autonomous): The lock is taken without blocking, with a few seconds of retry. The holder writes an
  advisory note naming itself, and a timeout has its own exit status. `list` reads without the lock when it is busy.
- 2026-10-18 (autonomous): Added the open question "Nested checkouts", found while settling "Canonicalizing scope keys".
- 2026-10-18 (autonomous): The state directory's home comes from the user database, fails closed, and has no override.
  `$HOME` and `$XDG_STATE_HOME` can differ between sessions of one user and split state.
- 2026-10-18 (autonomous): A caller with no adapter is an anonymous process identified by its parent. It can `list` and
  `break`, but cannot `release` or receive tombstones. `break` takes lease IDs only.
- 2026-10-18 (autonomous): `list --json` is the supported read interface, with its own versioned schema, computed
  liveness, a domain filter, and tombstones in its output. Amended the Impacts note that guardians read the record
  format directly.
- 2026-10-18 (autonomous): `release` takes IDs or domain arguments and never acts on an unnamed target. A bare `release`
  that acts on the only lease would let a subagent release its parent's lease.
- 2026-10-18 (autonomous): Amended "Re-acquiring as the same holder". It said `--replace` publishes a new record with a
  new ID, then removes the old one. Now it rewrites in place with `rename(2)` and keeps the ID, so a crash never leaves
  two records and NIT-003 parent links stay valid.
- 2026-10-18 (autonomous): Records and tombstones share an envelope versioned `major.minor`, with a typed footprint, a
  `clear_when_dead` bit, a rule that old binaries never rewrite a higher minor, and a `format` marker on the state
  directory. Converged in two passes. Ignoring unknown fields without the writer rules would let a V1 binary drop newer
  fields on rewrite. This also covers the additive-field rule the NIT-007 author relayed as the user's wish, which the
  user has not yet confirmed.
- 2026-10-18 (autonomous): Amended "Wider-reach lease names" and "Lease record fields". The unknown-record rule moved to
  "Record evolution and unknown records". Before, any unknown version, kind, or reach blocked every claim until
  `break --force`, even after the holder died. The fields intro said every later field is present now, which the
  downstream drafts disproved.
- 2026-10-18 (autonomous): Amended "Liveness". A record from another machine has unknown liveness, so a synced home
  cannot make a live remote holder read as dead.
- 2026-10-18 (autonomous): Nested worktrees and nested repos are separate domains, found by a `.git` boundary computed
  at check time. Submodules stay part of the parent. Amended "Domain keys and overlap" so overlap and coverage use the
  same rule. A stored checkout root would go stale after a `git worktree add`.
- 2026-10-18 (autonomous): Risk review. Replaced the record-contract risk with the envelope one-way door and its
  fail-safe mitigation. Accepted that a broken holder keeps working until its next call, pending NIT-005. Added the
  untested Linux sandbox liveness, with a milestone 1 check. Added opencode and Codex support resting on milestone 4,
  and accepted per-process liveness on multi-session hosts and misattribution through unsupported agents.
- 2026-10-18: Correction of fact in "Holder identity", from source readings reported by the NIT-007 author. It said
  Codex exposes no session ID variable and that opencode's export was unverified. Codex exports `CODEX_THREAD_ID` and
  `CODEX_SESSION_ID`. opencode's bash tool exports `OPENCODE_SESSION_ID`. Milestone 4's live checks still stand.
- 2026-10-18 (autonomous): Re-check fixes. Amended "Claiming" and "Breaking a live lease": a dead holder's record is
  cleared only when `clear_when_dead` is true. Unusable tombstones never block. `release` reports "no such lease"
  instead of "already released", which it cannot know. `release` needs an adapter. `--replace` merges several IDs.
  Handles fail on collision instead of taking a suffix, which could reassign a handle. The footprint check uses stored
  keys. The nearest adapter match wins before the PID-variable error. Machine comparison prefers the machine ID. The
  Codex adapter keys on `CODEX_SESSION_ID`.
- 2026-10-18 (autonomous): Final-pass fixes, not re-checked because the review's three-pass budget was spent. A merge
  records the IDs it absorbs, and cleanup removes only those. Inferring leftovers from coverage would delete a live
  nested-worktree lease once its worktree was removed. Stray agent variables without a matching ancestor fail
  `acquire`, `advertise`, and `release` closed. `release` on an unusable record has its own result. The handle is four
  words. Restated the nested-checkouts rationale around worktree removal.
- 2026-10-18: The user confirmed both preferences the NIT-007 author relayed. Any stable name is acceptable, even a
  random one. Additive record fields should not be breaking changes. "Session name and permission mode" and "Record
  evolution and unknown records" already satisfy both.
- 2026-10-18: Review gate: kept all decisions, on the user's choice to follow the recommendation.
- 2026-10-18 (autonomous): Took eight amendments that NIT-003's review asked for. The other-holder check runs before
  the coverage check. `release` removes a caller's record of a higher minor, and a field with release-time duties needs
  a new major. The `clear_when_dead` example and a rejected-alternative example no longer cite a NIT-003 delegation
  list, which NIT-003 rejected. `list --json` has a minor and major rule. The adapter rule covers later holder
  commands. The breaker claims at once only when no other live lease overlaps.
- 2026-10-18: Review gate: the user kept all eight NIT-003 amendments.
- 2026-10-18 (autonomous): Narrowed the release-time duty sentence to match the minor rule. Only a skipped duty that
  would make an old reader more permissive needs a new major. A skipped informational notice does not.
- 2026-10-18: ADR candidate (hard to reverse): key canonicalization and the overlap rule, nested-checkout boundary
  included. Written as ADR-03.
- 2026-10-18: ADR candidate (departs from the past): `list --json` as the supported read interface, replacing direct
  reads of record files. Declined: the proposal records it, and its consumers are the user's own guardian scripts.
- 2026-10-18: ADR candidate (cuts across the system): the derived holder handle, which NIT-007 may key mailboxes on.
  Waits on NIT-007 settling how mailboxes are addressed.
- 2026-10-18: Accepted, on the user's choice at the review gate.
- 2026-10-18: Amended "Record evolution and unknown records", on the user's choice after acceptance, for a gap the
  NIT-002 author found. It did not say how a path footprint entry compares with a name entry. A V1 binary that guessed
  "overlap" would refuse every path claim while a NIT-002 resource lease lives. Now a path entry and a name entry never
  overlap, and each later type defines its pairing with older types. ADR-02 took this as a minor edit: its name entries
  already use exact match, which never matches a path.
- 2026-10-18: Amended "A supported read path for guardians", on the user's choice. It said a minor schema bump "only
  adds fields". A minor bump may now also add `domain.kind` values, and consumers treat an unknown kind as opaque.
  NIT-002's `resource` kind is a minor bump.

## References

- ADR-02: lease record envelope and evolution rules
- ADR-03: lease key canonicalization and the overlap rule
- `spec/reports/2026-10-18-lease-broker-research.md`: broker, fencing, auth, and messaging research
- coding_agent_standards `claude/project-management/guardian.md`: the guardian protocol this tool serves
