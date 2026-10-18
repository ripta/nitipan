# NIT-007: Session Mailbox

**Status:** draft
**Created:** 2026-10-18
**Updated:** 2026-10-18

## Dependencies

- NIT-004

## Impacts

- NIT-001 — "Session name and permission mode". A mailbox is addressed by the session's local name, so every lease
  needs one
- NIT-004 — "Per-machine node". The node may become the delivery point for messages, not just a heartbeat

## Summary

The broker keeps a mailbox for each session. Any session can leave a message in another session's mailbox, on any
machine. That acts as a cross-machine SendMessage. A message reaches a machine over the stream that machine opened to
the broker. A local component then hands it to the session through a path the session's agent supports. Where the agent
allows it, that wakes an idle session.

## Motivation

SendMessage reaches sessions on the same machine, or through Remote Control. Remote Control is banned at the user's
work. A guardian on one machine has no way to reach a requestor on another. NIT-004 lets sessions find each other
across machines. Finding a session is not enough if it cannot be told anything.

Waking matters most. A requestor sends a request to a guardian, goes idle, and waits for the reply. A mailbox that only
delivers on the next tool call never reaches it.

## Design Decisions (Settled)

### Build it

The user decided on 2026-10-18 to build the mailbox. The broker will store and deliver messages, not just leases.

### Qualified addresses

A mailbox is addressed by the session's qualified name from NIT-004: a local name unique on its machine, plus a machine
part the broker adds. The local name is the one the local delivery path uses. Two sessions with the same local name on
different machines are still separately addressable.

### Delivery over the machine's own stream

The broker sends a message to a machine only over a connection that machine opened, per ADR-01. A message for a machine
whose node is not connected waits on the broker.

### Content is data

The receiving session treats message content as data from another session, never as instructions. SendMessage already
works this way.

## Design Decisions (Open)

The session wake path research found a documented path into an idle Claude Code session. It also found paths into
opencode and Codex that are less certain.

### Where messages enter a machine

- The NIT-004 node delivers. It reaches Claude Code through the session inbox socket, which the docs offer to scripts
  and hooks. It reaches opencode through its local HTTP service, and Codex through `codex queue` or its app-server. One
  broker connection serves the whole machine. The node is not a session's own child, so each Claude Code session needs
  `crossSessionInbound: accept` for unattended delivery.
- A per-session stdio MCP server delivers through Claude Code channels. It wakes an idle session in Claude Code only.
  It inherits every channel gate. An org admin must enable channels and allowlist the server, and `-p` sessions ignore
  the development flag. Each session holds its own broker connection.
- The node receives, and relays to a per-session MCP server. The MCP server, as the session's own child, posts to its
  session's inbox socket. That passes the own-child check without channels. Whether MCP server processes receive the
  socket's variable is unverified, so this needs a spike.

### Per-agent delivery adapters

Each agent takes messages differently. NIT-001 already has per-agent adapters for holder identity. Whether delivery
extends those adapters or stands apart is open.

The Claude Code socket's message body is undocumented. Only its auth line is documented. An adapter that writes the
body depends on a format that can change.

### Agents without a wake path

Codex's app-server is experimental. It is unverified whether `codex queue` or opencode's prompt endpoint wakes an idle
session. An agent without a wake path still needs a tier, such as delivery on its next tool call through a hook or a
nitipan call.

### Delivery guarantees

The research does not say whether messages expire, whether a sender learns of delivery, or what happens to a mailbox
whose session has died.

### Auth

A message across users needs to say who sent it. Whether the mailbox needs NIT-006 before it ships, or only before
cross-user delivery, is open.

## Risks

- **The work environment blocks every Claude Code wake path** — likelihood: medium, impact: high.
  An org can set `crossSessionInbound: refuse`, decline to enable channels, or run Claude Code on a provider without
  channels. Then no message reaches an idle session. Mitigation: before design review, check the work environment's
  managed settings and provider. The research lists which settings matter.
- **The socket body format changes** — likelihood: medium, impact: medium.
  Only the auth line is documented. Mitigation: the adapter tests delivery against each Claude Code release nitipan
  supports, and fails loudly on a rejected message.
- **Injected messages become a prompt-injection path** — likelihood: medium, impact: high.
  Any sender can put text in front of the receiving session. Mitigation: content is data, a settled decision. Sender
  identity depends on NIT-006.

## Milestones

### M1: Mailbox and Claude Code delivery

**Status:** NOT STARTED

- A session on one machine leaves a message that a Claude Code session on another machine receives, through the
  settled entry path
- An idle Claude Code session wakes and receives the message, under the settings the design names
- Messages are marked with their sender's qualified name, and the receiver sees them as data, not instructions

### M2: opencode and Codex delivery

**Status:** NOT STARTED

- opencode and Codex sessions receive messages through their adapters
- Whether each wakes an idle session is recorded in the As built block, along with the fallback tier it uses

## Decision Log

- 2026-10-18: Drafted from the lease broker research. The research framed the mailbox as optional, so nothing was
  settled at first.
- 2026-10-18: The user decided to build the mailbox. The open question "Whether to build it" was removed.
- 2026-10-18: Addressing settled on NIT-004's qualified names. Delivery settled on ADR-01's outbound streams. The wake
  questions were rewritten from the session wake path research. That research found a documented inbox socket for idle
  Claude Code sessions, replacing the draft's claim that third-party use of the socket was unknown.

## References

- `spec/reports/2026-10-18-lease-broker-research.md`: "Messaging"
- `spec/reports/2026-10-18-session-wake-paths.md`: how each agent takes messages, and which settings gate them
- ADR-01: machines connect out to the broker
- NIT-004: the broker that holds the mailboxes
