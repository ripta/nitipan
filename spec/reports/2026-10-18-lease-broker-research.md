# Lease Broker Research

**Date:** 2026-10-18

This is a research write-up. Its claims are unproven. It records the roadmap the user approved on 2026-10-18 and the
questions that roadmap leaves open. It feeds future proposals after NIT-001. Nothing here is decided until a proposal
settles it.

## Roadmap

V1 is one machine with no daemon. NIT-001 covers it.

V2 adds resource leases, counted slots, and sub-leases. A resource lease names a machine resource rather than a path,
such as `cpu:heavy` for "all cores." A counted slot lets N holders share one name. A sub-lease is one a guardian
delegates to another session for part of its domain. An MCP server exposing the lease operations is optional at this
stage. NIT-001 may pull it forward. See its "Invocation surface" question.

V3 adds a self-hosted broker. It hands out expiring leases with fencing tokens. One node per machine heartbeats for
all of that machine's sessions. The node reports a session's death from the local liveness check. Discovery is by
query, such as "who holds X" or "who can do Y." It is never by broadcast.

Callers should not change between versions. A V1 record already carries every field the broker needs.

## Reach

A lease name says how far it is meaningful. The levels, narrowest first:

- `session`: one session
- `machine`: one machine. Paths and `cpu:heavy` live here, and never leave the machine
- `user`: all of one user's machines. This is what "cluster-wide" usually means
- `shared`: across users

A wider name must use something the machines share. Filesystem paths do not qualify. A repo-relative name such as
`repo:<remote-url>#services/billing` does.

Open: the exact name syntax. Open: how a `machine` lease on a checkout relates to a `user` lease on the same repo
path. One may imply the other, or they may be independent.

## Holder and acting_for

A record names its holder, which is a session plus the user running it. It also names `acting_for`, a user. The two
differ for a shared agent that serves several separately-authenticated users. That agent holds leases in the
requesting user's scope. The agent itself is a `shared`-reach resource.

Open: whether a shared agent's lease counts against the requesting user's overlaps, the agent's, or both.

## Fencing

An expiring lease alone cannot stop a partitioned holder. The holder may not know its lease expired, and may still
commit or push. A fencing token is a number the broker increases with every grant. A protected action carries the
token. The receiving side refuses a token lower than the highest it has seen.

Open: what enforces the token. A local git hook can check it before a commit. A push needs the remote to check it,
such as a pre-receive hook. The user cannot add hooks to every remote. Open: what a holder does when its token is
refused mid-task.

## Auth

The environment is high-trust, so early versions have no auth. The fields are designed in now.

The recommended later path:

- OIDC for user identity, self-hostable through Keycloak, Dex, or corporate SSO
- Device flow for headless sessions
- A short-lived per-session key, or a broker-issued token

SSH signing is an alternative where the user's infrastructure runs an SSH CA. That covers `ssh-keygen -Y sign` and SSH
CA certificates.

Constraint: Remote Control is banned at the user's work. Every cross-machine piece must be self-hostable inside their
infrastructure and auth.

## Messaging

The broker could keep a mailbox per session. That would act as a cross-machine SendMessage.

Delivery to a busy session may work. A plugin hook can fetch queued messages on each tool call and inject them as
context. Plugin hooks run outside the sandbox.

Waking an idle session needs a push path into it. That path is unverified. Claude Code exposes
`CLAUDE_CODE_MESSAGING_SOCKET` to child commands. Whether a third party may use it is unknown, and it is local only.

## Agent identity

As of 2026-10-18, no cross-vendor standard identifies an agent session. `AGENT` (proposed in the agents.md project) and
`AI_AGENT` (promoted by Vercel's detect-agent) name the tool only. NIT-001's "Holder identity" records what Claude
Code, Codex, and opencode each expose. A broker that spans tools inherits the same gap.

## Sources

- coding_agent_standards `claude/project-management/guardian.md`, commit 05204b7
- Design handoff from the coding_agent_standards guardian session, 2026-10-18
- [agent detector (Go)](https://pkg.go.dev/github.com/gechr/x/agent)
- [detect-agent](https://www.pypi.org/project/detect-agent)
