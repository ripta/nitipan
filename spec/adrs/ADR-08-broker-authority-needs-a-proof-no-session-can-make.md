# ADR-08: Broker Authority Needs a Proof No Session Can Make

**Status:** accepted
**Date:** 2026-10-24

## Context

NIT-006 adds users to the broker. Several acts then hand authority to someone. Binding a machine's label to a user
lets that machine act as the user. A delegation lets a shared agent claim in a user's scope. A mail grant lets a
coworker put text in front of a user's agents, which may run a turn on it with nobody watching.

Sessions reach the broker only through the node, by the request handoff in the state directory. Every agent sandbox
can write that directory, under ADR-07. A prompt-injected session can therefore send any request the user's own
sessions can. A credential kept where sessions can read it proves only the OS user, not that the person chose the act.

## Decision

The broker grants authority only on a proof that no session can produce alone.

- Every act that gives a user, a machine, or an agent authority it did not have takes such a proof. That covers
  binding a label, granting or revoking a delegation, and granting or revoking mail across users
- The request handoff never grants authority. A request through it acts within authority already granted, or is
  refused
- A proof needs a human step outside every agent sandbox, such as a sign-in and a confirming submit in a browser. A
  command run on the broker host counts only as an OS user no agent session runs as. Signing with a key a session can
  reach is not such a proof
- Revoking authority takes such a proof too, so a session can neither widen nor strip its user's grants

A new kind of grant follows this decision without changing it. Which proofs qualify, and how each one is checked,
belong to the proposal that adds the grant.

## Rationale

A sandbox limits what a session can do on its machine. A grant made through the handoff would let a session widen its
own reach, or hand its user's scope to an agent it chose. Under ADR-07 nothing in the state directory can tell the
user's choice from a session's. Only a step a session cannot take can.

Keeping the rule in one place means each new grant asks one question: can a session make this proof alone? It does not
need to weigh each request field.

## Consequences

- Granting authority always involves the user outside the agent, so no agent can set up its own delegations or mail
  grants. That holds on the broker host only while no agent session runs as the broker's OS user
- The broker needs a channel for proofs that does not run through any node, such as a page or a broker-host command
- A key that the user signs with, but that a session can reach, cannot back a grant. That rules out signing through
  the user's SSH agent
- Acts within granted authority, such as claims, releases, and sends, stay on the handoff and need no new proof

## Alternatives Considered

- Grants through the request handoff, checked against the node's credential. The credential proves only the machine
  and its user, which every session of that user shares
- Grants signed with a user key, such as an SSH key. A session running as the user can sign with any key in
  `~/.ssh` or a loaded agent
- Audit after the fact, with notices naming who granted what. A notice cannot stop a prompt-injected session from
  acting first

## References

- NIT-006 (originating proposal)
- ADR-07: the state directory is sandbox input
- NIT-004: the request handoff
