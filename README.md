# nitipan

nitipan is a lease and coordination tool for coding agent sessions. A session claims ownership of a domain, such as a
checkout or some paths inside one. The claim also advertises the session: what it does, where it runs, and how to reach
it. Other sessions use the leases to find each other and to avoid stepping on each other's paths.

On one machine, nitipan needs no daemon. Leases live in a state directory under `~/.local/state/nitipan/`, and a lock
decides between claims. Across machines, a self-hosted broker grants wider leases, and a node on each machine connects
out to it. A local claim works the same with or without a broker. [Architecture](docs/architecture.md) shows how the
CLI, the agent harnesses, git, the node, and the broker talk to each other.

## Supported agents

nitipan supports Claude Code, Codex, and opencode. `nitipan setup <agent>` prints the sandbox allowance, hooks, and
plugins each one needs. It never edits a settings file.

- A Claude Code holder is alive while its process runs.
- Codex and opencode run every session under one long-lived host process. Their holders are judged by session instead,
  through the hooks and plugins that `setup` prints.

The Codex and opencode hooks and plugins pass automated live checks. `make live-codex` and `make live-opencode` drive a
private Codex app-server and a private opencode server against a fake model server. They need no manual steps and cost
no model tokens. [Setting up your agents](docs/getting-started/agents.md) covers each agent.

## Status

Every proposal listed below is implemented:

- [NIT-001](spec/proposals/NIT-001-local-lease-cli.md): local leases on one machine with `acquire`, `release`, `list`,
  `break`, `advertise`, and `setup`, and per-session liveness for Codex and opencode
- [NIT-002](spec/proposals/NIT-002-resource-leases-and-slots.md): resource leases such as `cpu:heavy`, counted slots
  shared by several holders, and `acquire --wait`
- [NIT-003](spec/proposals/NIT-003-sub-leases.md): sub-leases a holder delegates for part of its domain, with
  `request`, `grant`, `decline`, and `revoke`
- [NIT-004](spec/proposals/NIT-004-lease-broker.md): a self-hosted broker with expiring `user` and `shared` leases,
  per-machine nodes, `renew`, and discovery with `who holds` and `who is`
- [NIT-005](spec/proposals/NIT-005-fencing-tokens.md): a local git hook and fence marks against broken holders, and
  fencing tokens checked on push with `nitipan push`
- [NIT-006](spec/proposals/NIT-006-broker-auth.md): user auth for the broker, with label bindings, delegations to
  shared agents, and cross-user mail grants
- [NIT-007](spec/proposals/NIT-007-session-mailbox.md): per-session mailboxes for mail between sessions across
  machines, with wake paths for each agent
- [NIT-008](spec/proposals/NIT-008-sub-lease-recall.md): `recall`, which gives a sub-lease a deadline that every reader
  enforces

## Quick start

1. Build and install the binary with `make install`. It installs into `~/.local/bin` by default. Override the location
   with `PREFIX=` or `BINDIR=`, as in `make install PREFIX=/usr/local`.
2. Run `nitipan setup claude`, or `setup codex` or `setup opencode`. Apply what it prints to the agent's settings, then
   restart the agent.
3. Run `nitipan setup git --install <checkout>` for each checkout an agent works in. A path claim inside a git checkout
   is refused until its hooks are in force.
4. Inside an agent session, claim the checkout, look at the leases, and give the claim back:

   ```
   nitipan acquire --description "fixing the parser"
   nitipan list
   nitipan release <lease-id>
   ```

`acquire` prints the lease ID that `release` takes. [Installing nitipan](docs/getting-started/install.md) and
[Your first leases on one machine](docs/getting-started/local-leases.md) walk through each step.

## Documentation

[Architecture](docs/architecture.md) has a diagram of every component and how each one connects.

Getting started:

- [Installing nitipan](docs/getting-started/install.md)
- [Setting up your agents](docs/getting-started/agents.md)
- [Your first leases on one machine](docs/getting-started/local-leases.md)
- [Going multi-machine](docs/getting-started/broker-and-nodes.md)
- [Adding a coworker](docs/getting-started/coworkers.md)

Operations:

- [Running the broker](docs/operations/broker.md)
- [Running nodes](docs/operations/nodes.md)
- [Users and access](docs/operations/users-and-access.md)
- [Troubleshooting](docs/operations/troubleshooting.md)

Reference:

- [Command reference](docs/reference/cli.md), with every command, flag, and exit status
- [Leases](docs/reference/leases.md): domains, reach, repo names, liveness, resources, waiting, terms, and discovery
- [Sub-leases and recall](docs/reference/sub-leases.md)
- [Git hooks and fence marks](docs/reference/git-hooks.md)
- [Pushing under a broker lease](docs/reference/pushing.md), with fencing tokens
- [Mail](docs/reference/mail.md)
- [Broker authentication](docs/reference/broker-auth.md): leases, discovery, and mail across users
- [JSON output and records](docs/reference/json-and-records.md): `list --json`, `who --json`, record versions, and
  the state directory

Design lives in [spec/proposals/](spec/proposals/index.md), [spec/adrs/](spec/adrs/), and
[spec/reports/](spec/reports/).

## Relationship to coding_agent_standards

[coding_agent_standards](https://github.com/ripta/coding_agent_standards) defines guardians. A guardian is a long-lived
session that owns the changes in a domain. Today a guardian is found only by its session name, `<domain>_guardian`.
Nothing stops two guardians from claiming overlapping paths.

nitipan is the lease tool that protocol expects. nitipan owns the lease record format and the state directory layout.
coding_agent_standards owns the guardian protocol and the requestor rules. Guardians use nitipan when it is installed.
They fall back to session names when it is not.

## License

See [LICENSE](LICENSE).
