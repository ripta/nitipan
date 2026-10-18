# NIT-008: Sub-Lease Recall

**Status:** draft
**Created:** 2026-10-18
**Updated:** 2026-10-18

## Dependencies

- NIT-003
- NIT-004

## Impacts

None.

## Summary

A parent holder can recall a sub-lease with a deadline. The sub-lessee is told, and it has until the deadline to finish
and release. If it has not released by then, the sub-lease ends as if it were revoked.

## Motivation

NIT-003 revokes a sub-lease at once. That cuts off a sub-lessee in the middle of its work, possibly with uncommitted
changes. A guardian that wants its domain back usually wants it soon, not this instant.

A deadline needs something to enforce it while every session is idle. NIT-001 has no daemon. NIT-004's broker already
grants leases that expire, so it can enforce the deadline.

## Design Decisions (Settled)

### Split from NIT-003

Recall with a deadline was an option in NIT-003's revocation question. It moved here because it needs NIT-004's broker.
The user preferred a separate proposal that depends on the broker over a deferral inside NIT-003.

## Design Decisions (Open)

### How the deadline is enforced

- A recall shortens the sub-lease's term to the deadline. The broker's normal expiry ends it. This needs no new timer.
- The broker keeps a separate recall timer per sub-lease.

### Telling the sub-lessee

- A tombstone-like record the sub-lessee reads on its next nitipan call. It may not call again before the deadline.
- A NIT-007 message, which can wake an idle session where the agent allows it. This would add a dependency on NIT-007.

### Deadline length

Whether the parent picks the deadline, a fixed default applies, or both is open.

### Sub-leases at machine reach

NIT-003's sub-leases live in NIT-001's state directory. Whether recall works for them through the node, or only for
leases the broker grants, is open.

## Risks

- **The sub-lessee misses the recall** — likelihood: medium, impact: medium.
  An idle sub-lessee that never makes another call learns of the recall only after its sub-lease has ended. Mitigation:
  open, under "Telling the sub-lessee".

## Milestones

### M1: Recall with a deadline

**Status:** NOT STARTED

- A parent recalls a sub-lease with a deadline, and the sub-lessee is told under the settled rule
- A sub-lessee that releases before the deadline returns its keys to the parent
- A sub-lessee that has not released by the deadline loses the sub-lease and gets a tombstone

## Decision Log

- 2026-10-18: Split from NIT-003's revocation question, since recall needs NIT-004's broker.

## References

- NIT-003: the sub-leases and immediate revocation this proposal extends
- NIT-004: the broker whose lease terms enforce the deadline
- `spec/reports/2026-10-18-sub-lease-prior-art.md`: "Hierarchical and delegated leases", on NFSv4 recall
