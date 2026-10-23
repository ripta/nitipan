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

The first version runs on one machine, with no daemon. It has six commands:

- `acquire` claims a domain
- `release` gives it up
- `list` shows every lease and whether its holder is alive
- `break` removes another holder's lease, and needs `--force` when that holder is alive
- `advertise` updates the caller's name, permission mode, or a lease's description
- `setup` prints the sandbox allowance, hooks, and plugins an agent needs

A seventh command, `hook`, is called only by those agent hooks and plugins.

A domain is keyed by absolute path, with optional path scopes. A claim against a dead holder succeeds. An overlapping
claim against a live holder is refused, and the refusal names the holder.

A Claude Code holder is alive while its process runs. nitipan checks the PID and the process start time, which guards
against PID reuse. Codex and opencode run every session under one long-lived host process. Their holders are judged by
session instead, through the hooks and plugins that `setup` prints.

Leases live under `~/.local/state/nitipan/`. That directory must be on a local disk. A sandboxed agent needs a write
allowance for it.

Later versions add waiting for a lease, and a self-hosted broker that works across machines. Callers will not change
when the broker arrives. See the [broker research](spec/reports/2026-10-18-lease-broker-research.md).

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

Resource leases are advisory. They coordinate cooperating sessions only. A process that never calls nitipan can still
use every core.

Acquire resources from the parent session only. Subagents share their parent's holder, so two subagents that both claim
`cpu:heavy` both succeed, and the first to release frees the name while the other still runs.

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
