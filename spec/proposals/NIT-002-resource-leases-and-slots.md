# NIT-002: Resource Leases and Counted Slots

**Status:** draft
**Created:** 2026-10-18
**Updated:** 2026-10-18

## Dependencies

- NIT-001

## Impacts

- NIT-001 — "Domain keys and overlap", "Claiming", and "Lease record fields". A resource name is not a path, and a
  counted slot lets several live holders share one name

## Summary

A resource lease names a machine resource rather than a path. `cpu:heavy` means "all cores." A counted slot lets up to
N holders share one name at once. Both live at `machine` reach. They use NIT-001's claim, liveness, and break rules.

## Motivation

Sessions on one machine compete for more than files. Two sessions each running a full build or test suite slow each
other down. A session about to use every core has no way to say so, and no way to wait for another session to finish.

Some resources take a few users at once, but not unlimited ones. A local database or a GPU might allow two jobs. A
plain exclusive lease is too strict for those, and no lease is too loose.

## Design Decisions (Settled)

These come from the roadmap the user approved on 2026-10-18, recorded in the research write-up.

### Resource names

A resource lease's name is a resource, not a path. `cpu:heavy` is the example. Resource names are `machine` reach and
never leave the machine.

### Counted slots

A counted slot lets N holders hold one name at once. The N+1th claim is refused while N live holders remain.

### Invocation surface

The invocation surface is settled in NIT-001, pending NIT-001's review. This proposal adds operations to that surface.
It does not choose the surface.

## Design Decisions (Open)

### Name syntax

The research gives one example. It does not say whether resource names have a fixed set of namespaces, such as `cpu:`
and `gpu:`, or are free text after a colon. It also does not say how a resource name is told apart from a path in the
`domain` field.

### Who sets N

- The first claimant sets N, and later claims must agree.
- Every claim states N, and a mismatch is refused.
- N comes from a per-machine config file.

### Related resources

`cpu:heavy` means all cores. The research does not say whether lighter CPU claims exist, or whether they would overlap
`cpu:heavy`. A name hierarchy would need an overlap rule beyond NIT-001's path rule.

### Record shape for slots

- One record per holder, all naming the same resource. Matches NIT-001's one-record-per-lease layout.
- One record per resource that lists its holders. Needs a rewrite under the lock on every claim.

### Waiting

NIT-001's `acquire` refuses at once. A session that wants all cores would usually rather wait. Whether `acquire` gains
a wait mode with a timeout is open.

## Risks

- **Advisory leases on resources are ignored** — likelihood: high, impact: low.
  Nothing stops a process that never calls nitipan from using every core. Accepted: resource leases coordinate
  coöperating sessions only. The README says so.
- **A record-format change for slots** — likelihood: medium, impact: medium.
  If slots need a field NIT-001's record lacks, NIT-001's versioning rule decides whether V1 readers can still read
  new records. Mitigation: settle "Record shape for slots" while NIT-001 is still in review, so the field can land in
  V1.

## Milestones

### M1: Resource leases

**Status:** NOT STARTED

- `acquire` claims a resource name, and a second live claim on it is refused with the holder named
- A resource lease never overlaps a path lease
- `list` shows resource leases beside path leases

### M2: Counted slots

**Status:** NOT STARTED

- N live holders can hold one slot name, and the N+1th claim is refused
- A dead holder's slot is freed for the next claim
- Concurrent claims never exceed N, in a test that races them

## Decision Log

- 2026-10-18: Drafted from the lease broker research. Settled decisions are limited to the roadmap the user approved
  that day.
- 2026-10-18: The MCP server the research lists as optional V2 work is left to NIT-001's "Invocation surface"
  question. NIT-001's reviewer asked that NIT-002 not carry a duplicate question.

## References

- `spec/reports/2026-10-18-lease-broker-research.md`: "Roadmap" and "Reach"
- NIT-001: the claim, liveness, and break rules this proposal reuses
