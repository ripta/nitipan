# NIT-001: Local Lease CLI

**Status:** draft
**Created:** 2026-10-18
**Updated:** 2026-10-18

## Dependencies

None.

## Impacts

None within nitipan. Outside it, the guardian protocol in coding_agent_standards
(`claude/project-management/guardian.md`, "Discovery Today") expects a lease tool to claim domains and detect overlaps.
This proposal is that tool's first version. The record format in "Lease Record" is what guardians will read.

## Summary

A `nitipan` CLI lets a coding agent session claim a domain on one machine. A domain is a checkout, optionally narrowed
to path scopes. The CLI has four commands: `acquire`, `release`, `list`, and `break`. There is no daemon. State is a
directory of lease records, one per lease, under `~/.local/state/nitipan/`. A claim against a dead holder succeeds. An
overlapping claim against a live holder is refused, and the refusal names the holder. Each record also advertises the
session: what it does, where it runs, and how to reach it.

## Motivation

Guardians are found today by session name alone. A session named `<domain>_guardian` owns that domain. Nothing stops
two guardians from claiming overlapping paths. The user checks for overlaps by hand.

Sessions also have no shared way to say what they are doing. ListAgents gives a name and an idle or busy state. It does
not say which checkouts a session is working in, or whether it can commit there.

A lease file solves both on one machine. The claim is exclusive, so overlaps are caught when they happen. The record
carries the advertisement, so `list` answers "who owns this path, and what are they doing."

Later versions extend the same record across machines through a self-hosted broker. That work is out of scope here. It
is sketched in `spec/reports/2026-10-18-lease-broker-research.md`. This proposal fixes the record's fields now, so
callers do not change when the broker arrives.

## Design Decisions (Settled)

### State directory

Lease records live under `~/.local/state/nitipan/`. nitipan owns this layout. It does not depend on
coding_agent_standards being installed.

Sandboxed sessions need a write allowance for the directory. coding_agent_standards 5b0e716 adds one to
`setup_permissions.py`'s managed list. Machines without coding_agent_standards need another path. See the open question
"Granting the sandbox allowance".

### Commands

- `acquire` claims a domain. It fails when a live holder owns an overlapping domain.
- `release` gives up a lease the caller holds.
- `list` prints every lease, with each holder's liveness.
- `break` removes another holder's lease. See "Breaking a live lease".

### Domain keys and overlap

A domain entry is a checkout plus an optional list of path scopes, matching the guardian protocol's domain. The
checkout is an absolute path. nitipan canonicalizes it with symlinks resolved before it compares or stores anything.
Two worktrees of one repo are two checkouts, because they are two directories.

Each scope expands to a key: the checkout joined with the scope. An entry with no scopes has one key, the checkout
itself. Two keys overlap when one is equal to the other, or is its ancestor by whole path components. `/a/b` overlaps
`/a/b/c`. It does not overlap `/a/bc`.

### Holder identity

Each supported agent gets an adapter. The adapter finds the holder's session ID and PID from what the agent exposes to
child commands. The record names the agent, so the same session ID cannot collide across tools.

- Claude Code exports `CLAUDE_CODE_SESSION_ID` and `CLAUDE_PID` to every command. Both were observed in a session on
  2026-10-18.
- opencode 2.0.32 contains `OPENCODE_SESSION_ID` in its binary. Whether its bash tool exports it is unverified. It
  exposes no PID variable that was found.
- Codex 0.163.0 exposes no session ID variable that was found. `CODEX_THREAD_ID`, cited by a third-party detection
  library, does not appear in its binary.

Where the agent exposes no PID, the adapter walks up the process tree from nitipan's parent and stops at the agent's
process. Where it exposes no session ID, the session ID is empty, and the holder is the PID and start time alone.

When no adapter matches, `acquire` fails. Its error names the agents nitipan recognizes. There is no cross-vendor
standard for any of this. `AGENT` and `AI_AGENT` are proposed conventions that name the tool only.

### Liveness

A holder is alive when a process with its PID exists and that process's start time equals the recorded one. The start
time guards against PID reuse. No heartbeat is needed, and an idle session cannot heartbeat anyway.

On macOS, nitipan reads the start time with the `kern.proc.pid` sysctl. On Linux, it reads `starttime` from
`/proc/<pid>/stat`. It never uses `kill(pid, 0)`. Inside the Claude Code sandbox on macOS, `kill` returns EPERM for a
live process, so it cannot tell "alive" from "not permitted." A spike on 2026-10-18 confirmed that the sysctl works
inside that sandbox. It returns the start time for a live PID and zero bytes for a dead one.

V1 supports macOS and Linux.

### Claiming

`acquire` takes an exclusive `flock` on a single lock file in the state directory. Under the lock, it reads every
record, checks the new keys for overlap with each one, and creates the new record with `O_EXCL`. Then it releases the
lock.

A per-key atomic create alone would leave a race. Claims on `/a` and `/a/b` use different keys. Both could scan, find
nothing, and create. The lock closes that race. It is held for milliseconds. The kernel drops it when its process dies.

An overlapping record whose holder is dead does not block the claim. `acquire` removes it under the same lock, then
creates its own. An overlapping record whose holder is alive refuses the claim. The error names the holder's agent,
session ID, session name, and description.

### Breaking a live lease

`break` against a dead holder needs no flag. The lease could be claimed anyway.

`break` against a live holder refuses unless `--force` is given. With `--force`, it removes the lease and writes a
tombstone. The tombstone names the broken lease, who broke it, and when. `break` runs under the same lock as `acquire`.

Tombstones are keyed by the broken holder's identity, not by the domain. `acquire` never reads or removes them, so the
breaker can claim the domain at once. Every nitipan call reads the tombstones addressed to its caller, reports them,
and then removes them. This is the only signal a broken holder gets until fencing exists.

### Lease record fields

Every record carries these fields from the first version. Fields a later version needs are present now, so a reader
never has to guess what an absent field means.

- `version`: the record format's version
- `reach`: how far the name is meaningful. One of `session`, `machine`, `user`, or `shared`. V1 writes only `machine`
- `domain`: the checkout and its scopes, as given, and the canonical keys derived from them
- `holder`: the agent, session ID, session name, PID, process start time, and the OS user running the process
- `acting_for`: the user the holder acts for. In V1 it always equals the holder's user. It differs only for a shared
  agent serving several users, which needs the broker
- `machine`: the host name, plus a stable machine ID where the OS has one
- `permission_mode`: the holder's permission mode
- `description`: free text saying what the session does
- `acquired_at`: when the lease was claimed

## Design Decisions (Open)

### Invocation surface

V1 has no daemon either way. The question is whether the one binary also runs as an MCP server.

- CLI only. Agents call it through their shell tool. That call runs inside the agent's sandbox, so it needs the write
  allowance. The holder is found through the adapters above.
- CLI plus a stdio MCP mode, such as `nitipan mcp`. Each agent session spawns its own MCP server process. It is not a
  shared daemon. All three supported agents speak MCP. The server's parent process is the agent session itself, so
  holder identity needs no environment variables or process-tree walk. That would make most of "Holder identity" and
  milestone 4 unnecessary. Claude Code runs MCP servers outside its sandbox, so no write allowance is needed there.
  That is unverified for Codex and opencode. Humans and scripts still use the CLI. It costs a second surface over the
  same operations.
- MCP only. It leaves humans without a way to inspect or break leases.

### Record encoding and file naming

- One JSON file per lease, named by a hash of its first canonical key. Readable with `jq`. A guardian can read it
  without nitipan. Recommended.
- One JSON file per lease, named by an escaped form of the key path. Readable in `ls`. Long paths can exceed the
  file-name limit.
- One shared index file rewritten under the lock. Fewer files. One corrupt write loses every lease.

### Re-acquiring as the same holder

A session may call `acquire` for a domain it already holds.

- Idempotent: succeed, and update the advertisement fields in place.
- Refuse: tell the caller it already holds the lease. Updating the description then needs a separate command.

### Session name and permission mode

No supported agent exports a session's display name or its permission mode. Claude Code's `/rename` name is the address
SendMessage uses. That makes it the most useful field in the advertisement.

- Flags on `acquire`, such as `--name` and `--permission-mode`. The calling agent knows both. Unset fields stay empty.
- Flags, with `--name` required. Every lease is then addressable. It costs one more argument on every call.

### Linux start-time encoding

macOS reports an absolute start time. Linux reports clock ticks since boot. Ticks alone can repeat across a reboot.

- Store the raw ticks plus the boot ID from `/proc/sys/kernel/random/boot_id`, and compare both. Exact.
- Convert ticks to absolute time using `btime` from `/proc/stat`. One encoding for both platforms. `btime` has
  one-second resolution, so the comparison needs a tolerance.

### Granting the sandbox allowance

coding_agent_standards grants the allowance on machines that have it. Other machines need their own path.

- Document the two settings entries in the README.
- Add a `nitipan setup` command that prints or applies them. Applying would write the user's settings file. That is a
  guarded action.

### Wider-reach lease names

V1 names domains by local path, at `machine` reach. A `user` or `shared` lease needs a name that machines share, such
as `repo:<remote-url>#services/billing`. The syntax waits on the broker design. The `reach` field exists now, so a V1
record is never ambiguous about what its name means.

## Risks

- **The record format becomes a contract before the broker exists** — likelihood: high, impact: medium.
  Guardians and scripts will read the files directly. Mitigation: the `version` field, and every field the broker
  needs is defined now. A reader rejects a version it does not know rather than guessing.
- **The process-tree walk finds the wrong holder** — likelihood: medium, impact: medium.
  A wrapper process or a renamed binary could stop the walk too early or too late. Mitigation: milestone 4 verifies
  each adapter inside its own tool. `list` shows the PID and process name, so a wrong holder is visible.
- **Another agent's sandbox blocks the liveness sysctl** — likelihood: low, impact: high.
  Codex and opencode have their own sandboxes. If one blocks the sysctl, nitipan cannot tell a live holder from a dead
  one. Mitigation: an unreadable liveness result counts as alive, so the claim is refused rather than stolen. Milestone
  4 tests the sysctl inside each tool.
- **The state directory is on a network filesystem** — likelihood: low, impact: high.
  `flock` and `O_EXCL` are unreliable on some network filesystems. Two claims could both succeed. Accepted: the
  directory is under the user's home on a local disk by default. The README says so.
- **Tombstones pile up for holders that never return** — likelihood: medium, impact: low.
  A broken holder that dies before its next call never reads its tombstone. Mitigation: `list` removes tombstones
  whose holder is dead, after reporting them once.

## Milestones

### M1: Record format and liveness

**Status:** NOT STARTED

- The repo has a Makefile per the Makefile standard, with `build`, `test`, `lint`, and `ci` targets
- Lease records encode and decode every field in "Lease record fields", and reject an unknown `version`
- Path canonicalization resolves symlinks, and the overlap check passes table tests for equal, ancestor, sibling, and
  shared-prefix keys
- The liveness check reports alive, dead, and PID-reused correctly on macOS and on Linux
- The liveness check works inside the Claude Code sandbox on macOS

### M2: Acquire, release, and list

**Status:** NOT STARTED

- `acquire` claims a domain under the lock with an `O_EXCL` create
- `acquire` replaces an overlapping lease whose holder is dead
- `acquire` refuses an overlapping lease whose holder is alive, and the error names the holder
- Two concurrent `acquire` calls on overlapping keys never both succeed, in a test that races them
- `release` removes only a lease the caller holds
- `list` prints every lease with its holder's liveness
- The Claude Code adapter identifies the holder from `CLAUDE_PID` and `CLAUDE_CODE_SESSION_ID`

### M3: Break and tombstones

**Status:** NOT STARTED

- `break` removes a dead holder's lease without `--force`
- `break` refuses a live holder's lease without `--force`, and names the holder
- `break --force` leaves a tombstone keyed by the broken holder, naming the lease, the breaker, and the time
- The breaker can `acquire` the domain immediately after `break --force`
- A broken holder's next nitipan call reports that its lease was broken, and by whom, then removes the tombstone

### M4: Codex and opencode adapters

**Status:** NOT STARTED

- The opencode adapter's session ID and PID are verified from inside an opencode bash tool call
- The Codex adapter's PID is verified from inside a Codex shell command
- The liveness sysctl is verified inside each tool's sandbox, and the result is recorded in the As built block
- With no matching adapter, `acquire` fails and names the agents nitipan recognizes

## Decision Log

- 2026-10-18: Lease records live at `~/.local/state/nitipan/`, not under `~/.local/state/coding_agent_standards/`.
  nitipan must work without coding_agent_standards. coding_agent_standards 5b0e716 adds the sandbox allowance.
- 2026-10-18: Overlapping claims serialize on one global `flock`. Per-component locks were rejected as complex, with no
  contention to justify them.
- 2026-10-18: V1 supports Claude Code, Codex, and opencode through per-agent adapters.
- 2026-10-18: Liveness never uses `kill(pid, 0)`. It returns EPERM for live processes inside the Claude Code sandbox.
- 2026-10-18: ADR candidate (hard to reverse): the lease record's fields and versioning. Waits on the record encoding
  open question and on the broker research, since the broker may still add fields.

## References

- `spec/reports/2026-10-18-lease-broker-research.md`: broker, fencing, auth, and messaging research
- coding_agent_standards `claude/project-management/guardian.md`: the guardian protocol this tool serves
