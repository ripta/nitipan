# NIT-003: Sub-Leases

**Status:** draft
**Created:** 2026-10-18
**Updated:** 2026-10-18

## Dependencies

- NIT-001

## Impacts

- NIT-001 — "Domain keys and overlap", "Claiming", and "Breaking a live lease". A sub-lease is a second lease inside a
  live holder's domain, which NIT-001's overlap rule refuses today
- NIT-005 — "Token per grant". A sub-lease is a grant, and its token's relation to the parent's is open here
- NIT-008 — "Revocation" and "Record shape". Recall with a deadline extends this proposal's revocation and its tombstone

## Summary

A guardian holds a domain. It can delegate part of that domain to another session through a sub-lease. The other
session then works in that part while the guardian keeps the rest.

## Motivation

A guardian that owns a whole checkout may want another session to take on one directory. Under NIT-001, that session's
claim overlaps the guardian's lease and is refused. The guardian would have to release its whole domain and claim the
rest piece by piece. Overlaps would go unchecked between those steps.

## Design Decisions (Settled)

These come from the roadmap the user approved on 2026-10-18, recorded in the research write-up.

### Delegation by the holder

A sub-lease is granted by the session that holds the parent lease. No other session can carve a sub-lease out of a
domain it does not hold.

### No nesting

A sub-lessee cannot delegate again. Nobody needs it, and it would make cascading revocation and tombstone fan-out
required. A sub-lease record that names its parent leaves room for a later proposal to allow nesting.

### Revocation is immediate

When a sub-lease is revoked, it ends at once and its holder gets a tombstone. A gentler recall with a deadline needs a
timer, and nitipan has no process to run one until NIT-004's broker. NIT-008 adds recall on top of that broker.

## Design Decisions (Open)

The broker research gives sub-leases one sentence. The prior-art survey found no system that hands a child range to a
different holder under one shared overlap check. The options below come from analogues for behavior only. nitipan
adopts none of those systems. The survey lists them with sources.

### Record shape

- A separate lease record that names its parent. It follows Chubby, ZooKeeper, and UCAN. It reuses NIT-001's scan and
  gives each holder its own record. Cross-record consistency is the cost.
- A field on the parent record that lists its delegations. It follows etcd and multiple-granularity locking. Cascading
  is atomic. Every grant rewrites the parent's record.

### Overlap with the parent

A sub-lease's keys sit inside the parent's keys. The overlap check must allow that pair and still refuse every other
overlap.

Multiple-granularity locking suggests a shape for this. The parent's record changes mode, from exclusive on its whole
domain to exclusive minus the delegated parts. The overlap check becomes a small table keyed by mode and parent link,
rather than a special case.

- Strictly inside the parent. The parent always keeps a remainder.
- Equal to the parent allowed. That is a full handoff, as in NFS delegation.

Either way, whether the parent may still work in the delegated part is open. NFS says no until the parent recalls it.

### Liveness and the parent's death

- The sub-lease ends with the parent, as in etcd and SPIFFE. Simple and safe. It cuts off in-flight work.
- The sub-lease survives and becomes an ordinary lease, as with orphan reparenting and Consul's `release` behavior. Work
  continues. The parent's remaining keys become claimable around it, and the change needs a re-check under the lock.

### Breaks across the tree

- Whether the parent can end its own sub-lease without `--force`, or whether that is just revocation.
- Whether a sub-lessee may break its parent.
- Whether a third party's forced break of the parent cascades to its sub-leases, and who gets tombstones.

### Release across the tree

- Whether the parent may release while sub-leases exist. ZooKeeper refuses to delete a node with children. The other
  choices are to cascade or to orphan.
- Whether a sub-lessee's early release returns its keys to the parent's record, or leaves them unclaimed.

### Invocation and consent

- The guardian runs a command that names the sub-lessee. Identity is bound, as a UCAN audience is. The sub-lessee may
  not exist yet when the guardian acts.
- The guardian mints a token, and the sub-lessee presents it on `acquire`, as with macaroons. It works while the
  guardian is idle. It is a bearer secret, so anyone holding it can claim.
- The sub-lessee asks, and the guardian approves. The guardian may be idle and unable to answer.

### Display

How `list` shows a parent and its sub-leases is open.

### Fencing

How NIT-005's fencing tokens relate between a parent and its sub-leases is open.

## Risks

- **The overlap exception weakens exclusivity** — likelihood: medium, impact: high.
  A bug in the parent-child exception could let an unrelated session claim inside a live domain. Mitigation: table
  tests cover every overlap case with and without a parent link, and the race test from NIT-001 includes sub-leases.
- **The parent dies and the sub-lessee does not notice** — likelihood: medium, impact: medium.
  Neither macOS nor nitipan has a push signal on another process's death. Mitigation: the sub-lessee learns on its next
  nitipan call, as a broken holder does from a tombstone.

## Milestones

### M1: Grant and release

**Status:** NOT STARTED

- A holder grants a sub-lease on part of its domain, and the sub-lessee's claim succeeds
- A claim by any other session on the sub-leased part is still refused, and the error names both holders
- Releasing the sub-lease follows the settled release rule

### M2: Parent lifecycle

**Status:** NOT STARTED

- The parent holder's death and release follow the settled rules, and the sub-lessee is told
- Revoking a sub-lease ends it at once and leaves a tombstone for its holder
- A sub-lessee's attempt to grant a sub-lease is refused
- Breaks across the tree follow the settled rules

## Decision Log

- 2026-10-18: Drafted from the lease broker research. Settled decisions are limited to the roadmap the user approved
  that day.
- 2026-10-18: Added options and open questions from the sub-lease prior-art survey.
- 2026-10-18: The user ruled that systems like etcd, Consul, and ZooKeeper are far too complex for nitipan. Three
  options were removed. A per-grant choice of parent-death behavior was dropped, since review picks one rule. Nesting
  is forbidden, since nobody needs it, and a later proposal can allow it. Recall with a deadline moved to NIT-008,
  since it needs NIT-004's broker. The user preferred a separate proposal over a deferral with a revisit hook.

## References

- `spec/reports/2026-10-18-lease-broker-research.md`: "Roadmap"
- `spec/reports/2026-10-18-sub-lease-prior-art.md`: prior art for each open question
- NIT-001: the lease and overlap rules this proposal extends
