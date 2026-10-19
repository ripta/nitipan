# NIT-005: Fencing Tokens

**Status:** draft
**Created:** 2026-10-18
**Updated:** 2026-10-18

## Dependencies

- NIT-004

## Impacts

- NIT-001 — "Breaking a live lease". Tombstones are a broken holder's only signal until fencing exists. This proposal
  is that fencing
- NIT-003 — "Fencing" there fixes the outcomes this proposal's "Token scope" and "Local fencing for NIT-001" must keep
  for sub-leases. A single high-water mark per name or per repo cannot keep them

## Summary

The broker issues a fencing token with every lease grant. A token is a number that increases with each grant. A
protected action, such as a commit or a push, carries the holder's token. The side that receives the action refuses a
token lower than the highest one it has seen.

## Motivation

An expiring lease cannot stop a partitioned holder. The holder may not know its lease expired. It can still commit or
push after another session takes the lease, and the two sessions' work collides.

NIT-001 has the same gap on one machine. `break --force` removes a live holder's lease, and the holder learns of it
only on its next nitipan call. Anything it does before that call is unchecked.

## Design Decisions (Settled)

These come from the roadmap the user approved on 2026-10-18, recorded in the research write-up.

### Token per grant

The broker increases the token with every grant. A newer holder of a name always carries a higher token than any
earlier holder of that name.

### Receiver enforces

The receiving side of a protected action checks the token. The holder does not police itself. A holder that has lost
contact cannot know it is stale, so only the receiver can refuse it.

## Design Decisions (Open)

### What enforces the token

- A local git hook checks the token before a commit. It runs on the holder's machine, so it needs the highest token the
  broker has issued. A partitioned holder cannot fetch that.
- A pre-receive hook on the remote checks the token before a push. This catches a stale push even from a partitioned
  machine. The user cannot add hooks to every remote.
- Both, where each is available.

### Token scope

The research does not say whether one counter covers all names or each name has its own. Per-name counters keep tokens
small. One counter is simpler for a receiver that guards several names.

### Carrying the token

The research does not say how a commit or push carries the token. Candidates include a commit trailer, a push option,
and an environment variable the hook reads.

### Refused mid-task

The research leaves open what a holder does when its token is refused. It might stop and report, re-acquire and retry,
or hand its uncommitted work to the new holder.

### Local fencing for NIT-001

`break --force` on one machine could also bump a token, with the local git hook checking it. That would give V1 leases
fencing without a broker. Whether that belongs here or in a separate proposal is open.

## Risks

- **Remotes without hooks stay unfenced** — likelihood: high, impact: high.
  The user cannot install pre-receive hooks on remotes they do not control. A stale push to such a remote goes through.
  Mitigation: none yet. This blocks acceptance until "What enforces the token" settles it or the user accepts it.
- **Hooks are bypassable** — likelihood: medium, impact: medium.
  `git commit --no-verify` skips a local hook. Accepted for now: the environment is high-trust, and fencing guards
  against accidents, not hostile sessions.
- **Token format becomes a contract** — likelihood: medium, impact: medium.
  Hooks on remotes read the token, and they update slower than nitipan does. Mitigation: settle "Carrying the token"
  with a versioned format before any hook ships.

## Milestones

### M1: Token issue

**Status:** NOT STARTED

- Every broker grant returns a token higher than every earlier token for the same name
- The token is stored in the lease record and shown by `list`

### M2: Enforcement

**Status:** NOT STARTED

- A protected action carrying a stale token is refused at each enforcement point the design settles
- The holder's response to a refusal follows the settled rule, in a test that partitions a holder and grants its lease
  to another session

## Decision Log

- 2026-10-18: Drafted from the lease broker research. Settled decisions are limited to the roadmap the user approved
  that day.

## References

- `spec/reports/2026-10-18-lease-broker-research.md`: "Fencing"
- NIT-004: the broker that issues tokens
