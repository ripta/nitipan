# ADR-07: The Node Runs Only Input Sandboxes Cannot Write

**Status:** accepted
**Date:** 2026-10-24

## Context

Coding agents run their sessions inside sandboxes. nitipan's state directory is the one path every agent's sandbox
already allows, so sessions write leases, requests, and registrations there. NIT-004's node reads that directory, and
it runs outside every sandbox.

That makes the node a bridge out of the sandbox. NIT-007 found the first case: a session could name a binary, or a
configuration directory, for the node to run on its behalf. A session can also start a process whose name and
environment it chooses, so a value read from a running process is no safer than one read from the state directory.

## Decision

The node never runs, loads, or configures anything from input an agent sandbox can write or control.

- What the node executes, and the configuration it executes with, come only from inputs no agent sandbox can write
- Input from inside a sandbox, such as a registration or a request, is data. The node may compare it, check it, or
  store it, but never runs it or follows it to code
- A value read from a process a session could have started counts as sandbox input
- When the node acts on a resource a session names, such as a socket, it first checks that the resource belongs to the
  session it claims to, and then only sends it data

A new feature that has the node act for a session follows this decision without changing it. Which inputs count as
outside every sandbox, and how each check works, belong to the proposal that adds the feature.

## Rationale

A sandbox limits what a session can do. The node holds no such limits. Any path by which a session chooses what the
node runs gives the session every permission the node has, which is the escape the sandbox exists to prevent.

Treating sandbox input as data keeps that line in one place. Each feature then asks only whether it runs something,
not how trustworthy each field is.

## Consequences

- Configuration the node runs with is set up by the user outside the state directory
- A feature that needs a per-session executable or configuration cannot take it from the session. It must use one the
  user set up, or do without

## Alternatives Considered

- Validating sandbox-written paths before running them. Any check on a path a session chose still lets the session
  choose a binary that passes the check
- Reading values from the session's live process. The session controls that process's name and environment

## References

- NIT-007 (originating proposal)
- NIT-004: the node and its state directory
