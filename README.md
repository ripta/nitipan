# nitipan

nitipan is a lease and coordination tool for coding agent sessions. A session claims ownership of a domain, such as a
checkout or some paths inside one. The claim also advertises the session: what it does, where it runs, and how to reach
it. Other sessions use the leases to find each other and to avoid stepping on each other's paths.

It supports Claude Code, Codex, and opencode.

## Status

The first version is [NIT-001](spec/proposals/NIT-001-local-lease-cli.md). The commands, the Claude Code adapter, and
the Codex and opencode session rules are built. The Codex and opencode hooks and plugins still await live checks inside
real sessions.

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

Later versions add resource leases, such as `cpu:heavy`, and a self-hosted broker that works across machines. Callers
will not change when the broker arrives. See the
[broker research](spec/reports/2026-10-18-lease-broker-research.md).

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
