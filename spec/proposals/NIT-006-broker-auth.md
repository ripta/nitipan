# NIT-006: Broker Auth

**Status:** draft
**Created:** 2026-10-18
**Updated:** 2026-10-24

## Dependencies

- NIT-004

## Impacts

- NIT-001 — "Holder identity" and "Lease record fields". Auth must prove the `holder` user and `acting_for` user those
  fields name
- NIT-007 — "Node credential" and M1. NIT-007's "Auth" already fills the auth slot with a per-label node key that the
  user approves on the broker host and the broker pins. Mail across users waits on this proposal. Whatever this
  proposal settles for the node and for M1's refusal rule decides how that key ends and how cross-user mail starts

## Summary

The broker authenticates the users and sessions that call it. Users sign in through an identity provider the user's
infrastructure already runs. Headless sessions sign in without a browser. Each session then calls the broker with a
short-lived credential of its own.

## Motivation

Early broker versions run without auth, because the environment is high-trust. That stops working once leases cross
users at `shared` reach. A shared agent acts for several users. The broker must know which user each request acts for,
or one user's session can claim or break another user's lease.

## Design Decisions (Settled)

These come from the roadmap the user approved on 2026-10-18, recorded in the research write-up.

### No auth at first

NIT-004 ships without auth. The record fields auth needs, `holder` and `acting_for`, exist from NIT-001 on. Adding auth
does not change the record.

### Self-hostable

Every piece runs inside the user's own infrastructure and auth. Remote Control is banned at the user's work. No piece
may depend on a hosted identity service the user does not run.

## Design Decisions (Open)

### Identity mechanism

- OIDC for user identity, self-hosted through Keycloak, Dex, or corporate SSO. Device flow covers headless sessions.
  The research recommends this path.
- SSH signing through `ssh-keygen -Y sign` and SSH CA certificates. This fits where the user's infrastructure already
  runs an SSH CA.
- Both, with the broker accepting either.

### Session credential

- A short-lived per-session key that the user's identity signs.
- A token the broker issues after the user signs in.

The research lists both and picks neither. Either one needs a lifetime and a renewal rule.

### Node credential

NIT-004's node heartbeats for every session on its machine. The research does not say whether the node holds its own
credential or forwards each session's.

NIT-007 settles a node credential ahead of this proposal, for mail only between one user's machines. Each node keeps
a random bearer key, the user approves each label on the broker host, and the broker pins the key and checks it on
every request, including the stream, sends, and acks. The answer here must take these into account:

- This proposal's node credential replaces or wraps the pinned key. It never runs beside it, since two node
  credentials would give two ways to act as one machine
- Machines already enrolled under NIT-007 need a migration path that does not strand their labels or their queued
  mail. ADR-05's migration rules apply to the label
- NIT-007 marks a message `sender_verified` when it comes from an enrolled node. A node credential that names a user
  extends that meaning to cross-user mail. It must not weaken it for single-user mail
- The node attests each session's identity, under NIT-004's "How sessions reach the broker". Mail and heartbeats are
  node calls, not session calls. M1's rule that "every broker call carries a session credential" cannot hold for them
  as written. Settling this question settles M1's wording too

### What auth authorizes

The research covers identity only. It does not say who may break whose lease, or whether a shared agent may claim in a
user's scope without that user's approval.

### Shared agents and acting_for

Moved from NIT-004, which deferred it here. Settle it before NIT-006 is accepted.

A shared agent serves several users and holds leases in the requesting user's scope. Its record's `acting_for` differs
from its `holder` user. Which scope's overlaps such a lease counts against is open:

- The requesting user's scope, named by `acting_for`
- The agent's own scope, named by its holder's user
- Both

The answer depends on "What auth authorizes": whether an agent may claim in a user's scope without that user's
approval. M1's refusal of a record whose `acting_for` does not match the authenticated identity also depends on it.
Read literally, that check refuses every shared agent's record.

Every CLI before this proposal writes `acting_for` as the OS user name. That value is not an authenticated identity,
and OS user names differ across one person's machines. Whatever this proposal picks must treat a legacy `acting_for`
as unauthenticated, for example by mapping it through the node's authenticated user.

Until this lands, NIT-004 runs one user scope per broker, and `acting_for` is carried unchanged and ignored for scope.

## Risks

- **Auth arrives after callers depend on no-auth behavior** — likelihood: medium, impact: medium.
  Scripts written against an open broker break when auth turns on. Mitigation: the broker protocol in NIT-004 carries
  an auth slot from its first version. Before this proposal lands, only NIT-007's pinned node key uses it.
- **Replacing NIT-007's node key strands enrolled machines** — likelihood: medium, impact: medium.
  Machines approved under NIT-007 hold pinned keys and may have mail queued on the broker. A cutover that refuses the
  old key before each machine holds the new credential locks them out and holds their mail. Mitigation: open under
  "Node credential", which must give the migration path.
- **Device flow stalls an unattended session** — likelihood: medium, impact: low.
  A headless session waiting on a device code has no user watching it. Mitigation: open under "Session credential".
  A credential obtained once per user, then delegated to sessions, would avoid a prompt per session.

## Milestones

### M1: User and session identity

**Status:** NOT STARTED

- A user signs in through the settled identity mechanism, including from a headless session
- Every broker call carries a session credential, and the broker refuses calls without one
- The broker's view of `holder` and `acting_for` matches the authenticated identity, and a mismatched record is refused

### M2: Authorization rules

**Status:** NOT STARTED

- Claims and breaks follow the settled authorization rules, in tests that cover two users and one shared agent

## Decision Log

- 2026-10-18: Drafted from the lease broker research. Settled decisions are limited to the roadmap the user approved
  that day.
- 2026-10-24: Added the open question "Shared agents and acting_for", moved from NIT-004's design review at the user's
  request. It blocks acceptance.
- 2026-10-24: NIT-007's design review settled a pinned per-label node key for single-user mail. "Node credential"
  gained the constraints that follow from it, including a note that M1's session-credential rule cannot hold for node
  calls as written. A risk was added for the cutover from that key. Nothing was settled here.

## References

- `spec/reports/2026-10-18-lease-broker-research.md`: "Auth" and "Holder and acting_for"
- NIT-004: the broker this proposal secures
- NIT-007: the node key this proposal's node credential replaces or wraps
- ADR-05: migration rules for the machine label
