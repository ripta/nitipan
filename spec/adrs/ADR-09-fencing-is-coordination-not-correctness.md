# ADR-09: Fencing Is Coordination, Not Correctness

**Status:** proposed
**Date:** 2026-11-14

## Context

nitipan has three layers that keep agent sessions from stepping on each other. The broker orders lease grants across
machines. A lease says which session should work in a domain, and it expires when its holder goes quiet. A fence ref
on the remote makes the receiver enforce the lease against a holder that lost it without knowing.

Git already guarantees the repo's data. A non-fast-forward push is rejected, and an atomic push applies all its refs
or none. A run with no broker has no fencing at all, and that is a supported setup. Sessions on different machines
then meet only at git's fast-forward rule.

Correctness outranks every other criterion when design options are weighed. Filing a fencing gap under correctness
therefore lets any guard outweigh robustness and simplicity, including one that moves enforcement off the receiver.

## Decision

The broker, leases, and fence refs are coordination layers. They make collisions between sessions rarer. They do not
guard the repo's data, which git guarantees.

- A gap in fencing weakens coordination. It is never a correctness defect, and it never justifies a correctness-grade
  fix
- When fencing cannot run, nitipan degrades to a lease check, or to git's own rules, without extra machinery
- The fence is enforced by the receiver. A fenced push does not call the broker. A change that puts the broker on the
  fenced push defeats the fence, and it goes to the user as a question
- A threat that shapes a fencing design must match how nitipan is used: one user's agents push to that user's remotes
  as that user

## Rationale

The fence exists so that the remote, not the holder, refuses a stale push. A holder that has lost contact cannot know
it is stale, and it may not reach the broker either. Keeping the broker off the fenced push is what makes the fence
worth having.

Ranking a coordination gap as a correctness failure inverts the cost of every fix. Each added check pays robustness
and simplicity for a guarantee git already gives. The no-broker setup shows what the floor is. Anything above it is
coordination, and a weaker coordination mode is acceptable.

## Consequences

- A fencing gap can let a stale push land. Git keeps the data intact
- Design reviews weigh a fencing change against the fence's purpose before patching an edge case
- Future work cannot add a broker call to the fenced push, or treat a fencing gap as a correctness defect, without
  superseding this ADR

## Alternatives Considered

- Treating fencing as part of correctness. Every fencing gap then outranks robustness and simplicity, and the design
  grows a guard for each edge case
- Renewing leases with the broker on every fenced push. It closes rare stale-push windows, but it moves enforcement to
  the holder's side and makes every fenced push depend on the broker

## References

- NIT-009: the proposal whose review set this principle
- NIT-005: fencing tokens, "Receiver enforces", and the broker-free fenced push
- NIT-004: the broker
