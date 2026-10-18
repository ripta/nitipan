# NIT-004: Lease Broker

**Status:** draft
**Created:** 2026-10-18
**Updated:** 2026-10-18

## Dependencies

- NIT-001

## Impacts

- NIT-005 — "Expiring leases", since fencing tokens are issued with each grant
- NIT-006 — "Broker protocol", since auth wraps every broker call
- NIT-007 — "Per-machine node", "Machines connect out", and "Qualified names". A mailbox lives on the broker, reaches
  a machine over the stream the machine opened, and is addressed by qualified name
- NIT-008 — "Expiring leases". Recall with a deadline needs the broker's lease terms
- NIT-001 — "Lease record fields" and the open question "Wider-reach lease names". This proposal answers that question.
  A new record field here follows NIT-001's versioning rule

## Summary

A self-hosted broker extends nitipan leases across machines. It grants expiring leases. One node per machine
heartbeats to the broker for every session on that machine. The node reports a session's death from the local liveness
check that NIT-001 defines. Sessions find each other by query, such as "who holds X" or "who can do Y." The broker
never broadcasts.

The broker is the one address every machine can reach. Machines connect out to it, and it never connects in. Leases at
`user` and `shared` reach need names that machines share. This proposal defines them.

## Motivation

NIT-001 coordinates sessions on one machine. A user who runs sessions on several machines still checks for overlaps by
hand. Two sessions on two machines can claim the same repo path and both succeed, because each machine sees only its
own state directory.

A local path cannot name a cross-machine lease. `/Users/ripta/projects/nitipan` means nothing on another host. A
broker needs names that every machine resolves the same way.

## Design Decisions (Settled)

The first four come from the roadmap the user approved on 2026-10-18, recorded in the research write-up. The rest the
user settled the same day while the proposal was drafted.

### Expiring leases

The broker grants leases that expire. A holder that stops renewing loses its lease when the term ends. This replaces
NIT-001's "a dead holder's lease can be claimed" with a rule that also works when the broker cannot see the holder's
process.

### Per-machine node

One node per machine heartbeats to the broker on behalf of all of that machine's sessions. Sessions do not heartbeat.
An idle session cannot heartbeat anyway.

The node checks each local holder with NIT-001's liveness check. When a holder dies, the node reports it, and the broker
ends its leases without waiting for the term to run out.

### Discovery by query

Sessions find holders and capabilities by asking the broker. Examples are "who holds X" and "who can do Y." The broker
does not push announcements to sessions.

### Self-hosted

The broker and node run inside the user's own infrastructure. Remote Control is banned at the user's work, so no part
of this design may depend on it or on any hosted relay.

### Callers do not change

A V1 caller keeps working when the broker arrives. The research claims a V1 record already carries every field the
broker needs. A field this design adds follows NIT-001's versioning rule.

### Machines connect out

The broker has its own address, and every machine can reach it. Nodes open their connections to the broker. The broker
never opens a connection to a machine, so no machine needs to be reachable from outside.

Anything the broker sends to a machine travels over a stream that machine opened. That covers lease events and
NIT-007's messages. See ADR-01.

### Single broker

One broker process serves every machine. A replicated broker is too complex for nitipan's needs. If outages become a
problem, replication is its own proposal. `machine` leases keep working through NIT-001's state directory while the
broker is down.

### Qualified names

Each layer contributes its own part of a name. A session's local name only needs to be unique on its machine. That is
the name a local socket or MCP server exposes. The broker qualifies it with a machine part, roughly
`<session>@<machine>`. Two sessions with the same local name on different machines stay distinct.

## Design Decisions (Open)

### Name syntax for wider reach

A `user` or `shared` lease needs a name every machine shares. Filesystem paths do not qualify. The research sketches a
repo-relative name, `repo:<remote-url>#services/billing`.

- Repo-relative names keyed by remote URL. Two questions follow. SSH and HTTPS forms of one remote differ, so the URL
  needs a canonical form. A repo with several remotes needs a rule for which one names it.
- Names keyed by something other than the remote, such as the repo's root commit. Stable across remote renames. Two
  forks of one repo would share a root commit.

### The machine part of a qualified name

- The host name. Simple. Host names can collide across networks and change over time.
- A name the broker assigns when a node first registers, kept by the node. Unique by construction. The node must store
  it, and a reinstalled machine gets a new one.
- The stable machine ID NIT-001 already records. Unique. Unreadable to people.

### Source of truth for machine leases

A `machine` lease lives in NIT-001's state directory. The broker also needs to know about it to answer queries.

- The state directory stays authoritative. The node mirrors it to the broker, and the broker's view may lag.
- The broker becomes authoritative once a node is connected. The state directory is a cache. This leaves two sources of
  truth during an outage.

### Machine leases and user leases on the same path

A `machine` lease names a checkout. A `user` lease names a repo path. Both can cover the same files.

- One implies the other. A `machine` lease on a checkout also claims the `user` name of that path, so two machines
  cannot both hold the path.
- They are independent. A `machine` lease protects the working tree only. Cross-machine conflicts need an explicit
  `user` lease.

### Shared agents and acting_for

A shared agent serves several users and holds leases in the requesting user's scope. Its record's `acting_for` differs
from its `holder` user.

- The lease counts against the requesting user's overlaps.
- It counts against the agent's own overlaps.
- It counts against both.

### Broker protocol

The research does not name a transport. The transport must carry a long-lived stream from node to broker, since the
broker sends to machines only over connections they opened. Candidates include HTTP with server-sent events, gRPC, and
Connect. The choice affects NIT-006, which wraps every call with auth.

### Lease term and renewal

The research does not give a term length, a renewal interval, or a grace period. It also does not say what a node does
when it cannot reach the broker. Its local sessions might keep their leases until the term ends, or the node might
release them early.

### Discovery queries

The research names two query shapes. It does not define what "who can do Y" matches against. The record's
`description` field is free text, so a capability query may need a structured field.

## Risks

- **A partitioned holder keeps working after its lease expires** — likelihood: medium, impact: high.
  An expiring lease alone cannot stop a holder that lost contact with the broker. It may commit or push after another
  session takes the lease. Mitigation: NIT-005 adds fencing tokens. Until NIT-005 lands, this risk is open.
- **The broker is a single point of failure** — likelihood: medium, impact: medium.
  If the broker is down, no session can claim a cross-machine lease. Accepted: one broker is simple, and `machine`
  leases keep working locally. Replication can come later as its own proposal.
- **Wider-reach names are a contract** — likelihood: high, impact: medium.
  Once guardians and scripts store `repo:` names, changing the syntax breaks them. Mitigation: settle "Name syntax for
  wider reach" before any milestone ships. Record it as an ADR candidate.
- **The broker needs a field V1 records lack** — likelihood: medium, impact: low.
  The research's claim that V1 carries every field is unproven. Mitigation: NIT-001's versioning rule decides whether
  the field is additive or needs a version bump.

## Milestones

### M1: Broker and node with expiring leases

**Status:** NOT STARTED

- The broker grants, renews, and expires leases at `machine` reach for sessions on two machines
- The node heartbeats for every local session, and sessions make no heartbeat calls of their own
- When a local holder dies, the node reports it, and the broker ends its leases before the term runs out
- Every connection between a node and the broker is opened by the node
- A V1 lease record is accepted by the broker without changes, or the change is recorded in the As built block

### M2: Wider-reach and qualified names

**Status:** NOT STARTED

- `user` and `shared` leases use the settled name syntax, and two machines resolve one repo path to the same name
- Two sessions with the same local name on two machines get distinct qualified names
- Overlap checks between `machine` and `user` leases follow the settled rule

### M3: Discovery queries

**Status:** NOT STARTED

- "Who holds X" returns every live holder whose keys overlap X, across machines
- "Who can do Y" returns matching sessions under the settled capability rule

## Decision Log

- 2026-10-18: Drafted from the lease broker research. Settled decisions are limited to the roadmap the user approved
  that day.
- 2026-10-18: The user settled three decisions. Machines connect out to the broker, and the broker never connects in.
  One broker serves every machine. Names are qualified per layer, with a machine part added by the broker. The open
  question "Broker availability" was removed, since a single broker settles it. Systems like etcd and Consul are too
  complex for nitipan.
- 2026-10-18: ADR candidate (cuts across the system): machines connect out to the broker, and the broker never connects
  in. Written as ADR-01.
- 2026-10-18: Added the open questions "The machine part of a qualified name" and "Source of truth for machine leases".

## References

- `spec/reports/2026-10-18-lease-broker-research.md`: roadmap, reach levels, and open questions
- ADR-01: machines connect out to the broker
- NIT-001: the lease record and liveness check this proposal extends
