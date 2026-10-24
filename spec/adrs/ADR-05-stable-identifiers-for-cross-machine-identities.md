# ADR-05: Stable Identifiers for Cross-Machine Identities

**Status:** accepted
**Date:** 2026-10-24

## Context

NIT-004 adds a broker that coordinates leases across machines. Several things then need an identity that holds beyond
one machine: a repo's files at `user` and `shared` reach, a node, and a session that NIT-007 addresses mail to.

Callers store these identities. Guardians and scripts keep lease names, and senders keep mail addresses. A stored
identity that later names something else misroutes mail or lets two holders claim the same files. One that changes
for the same thing strands every stored copy.

The obvious candidates change on their own. A filesystem path differs between machines. A host name changes with the
network, and two machines can share one. A git remote URL differs between ssh aliases and forks. A name set in local
config is missing on any machine nobody configured.

## Decision

Every identity that crosses machines, or that callers store, is derived from a stable identifier.

A stable identifier is one that:

- Stays the same for the same thing across machines, networks, paths, and local configuration
- Is never reused for a different thing
- Can be derived the same way by every machine that must agree on it, or is derived in one place

An identity is never keyed on a filesystem path, a host name, a remote URL, or a value from local configuration. Such
values may be shown beside an identity as a label. They never decide whether two identities are equal.

The exact derivation and text form of each identity belong to the proposal that introduces it. A new kind of identity,
or a new format for an existing one, follows this decision without changing it.

A migration that changes an existing identity's identifier follows these rules:

- Every producer of an identifier states the derivation version it used. A reader refuses a version it does not know
  rather than guessing
- The old and new identifiers both resolve during the migration. The component that resolves them keeps a mapping
  from old to new until no stored copy of the old one can still arrive
- When old and new cannot be reconciled for some identity, the comparison fails closed: the identities are treated as
  conflicting, never as distinct
- No identifier is ever reassigned to a different thing, during the migration or after it

## Rationale

Two names for one thing fail open: two holders claim the same files, and both succeed silently. One name for two
things fails closed, as a visible conflict. A stable identifier keeps the first failure from arising out of ordinary
configuration.

Keeping the format out of the decision lets a proposal change a derivation without superseding the ADR. The migration
rules are what must not change, since a migration done wrong strands or misroutes stored identities.

## Consequences

- Every proposal that adds a cross-machine identity names its stable identifier and its derivation version
- Every resolver of a stored identity keeps enough state to map an old identifier to a new one during a migration
- Identifiers can be harder for people to read. A readable label may sit beside one for display only
- An identity whose stable identifier cannot be read is refused, rather than given a fallback that is not stable

## Alternatives Considered

- Recording each identity's syntax as its own ADR. Every format change, and every new kind of identity, would need a
  superseding ADR, though the principle behind them would not change
- Keying identities on paths, host names, remote URLs, or configured names. Each one gives one thing two names under
  ordinary setups, and two things one name in others

## References

- NIT-004 (originating proposal)
- NIT-007: addresses mail to sessions
