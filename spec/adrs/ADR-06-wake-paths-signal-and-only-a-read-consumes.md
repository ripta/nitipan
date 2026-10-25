# ADR-06: Wake Paths Signal, and Only a Read Consumes

**Status:** accepted
**Date:** 2026-10-24

## Context

NIT-007 carries messages between sessions on different machines. A message reaches a session's machine, and then the
session's agent must learn of it. Each agent offers different paths for that: a hook, a plugin, a local socket, a
queue command. More agents, and more paths, will come.

nitipan cannot see what most of these paths do with what it hands them. A path can accept a message and then drop it,
hold it for an approval nobody gives, or report success once the message is stored rather than once it is read. A
message handed over on such a path and then removed from nitipan's own copy is lost with no trace.

## Decision

A message is consumed only by the recipient's explicit read, and only after the message is durable on the recipient's
machine. Every other delivery path only signals that mail is waiting.

- A signal never carries the message's content and never removes the message
- A signal may be lost, repeated, or ignored without losing any message
- A machine acknowledges a message to the sender's side only once the message is durable on that machine
- The read is the last stage of delivery that nitipan reports to the sender

A new agent, or a new path into an existing agent, follows this decision without changing it. Which paths exist, and
how each one signals, belong to the proposal that adds them.

## Rationale

The read is the one step nitipan both performs and observes. Making it the only consuming step means no path whose
outcome nitipan cannot see can lose a message. It also makes delivery a stage the sender can be told about truthfully.

Content that arrives through the read comes back as the output of a command the session ran, which frames it as data.
A pushed message lands wherever the agent chooses to put it.

## Consequences

- Every message costs the recipient a read after the signal, so one more step per delivery
- Signals can repeat, so each path needs a rule that keeps a signal from waking a session over and over
- A recipient that never reads keeps its mail until the proposal's delivery rules dispose of it
- Every adapter for a new agent must be built as a signal, never as a delivery of content

## Alternatives Considered

- Letting each path consume the message it hands over. A path that drops or holds what it accepted loses the message,
  and nitipan cannot tell
- Pushing content on every path and keeping a copy until some path confirms. Most paths cannot confirm, so the copy
  would never be released, and the session would see each message several times

## References

- NIT-007 (originating proposal)
