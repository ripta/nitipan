# ADR-01: Machines Connect Out to the Broker

**Status:** proposed
**Date:** 2026-10-18

## Context

NIT-004 adds a self-hosted broker that coordinates leases across machines. Later work sends things from the broker to
machines: lease events, fencing state in NIT-005, and session messages in NIT-007.

The machines are a user's laptops and workstations. They sit behind NAT, firewalls, and VPNs. Remote Control and hosted
relays are banned at the user's work, so no outside service can bridge them.

## Decision

The broker has its own address, and every machine can reach it. Each machine's node opens its own connection to the
broker. The broker never opens a connection to a machine.

Anything the broker sends to a machine travels over a stream that machine opened.

## Rationale

Only one address has to be reachable, and it is the broker's. No machine needs an inbound port, a public address, or a
firewall rule. A machine that moves between networks keeps working, because it reconnects from wherever it is.

## Consequences

- The broker protocol must carry a long-lived stream from node to broker. A plain request and response is not enough.
- The broker reaches only machines whose node is connected. A message to a disconnected machine waits on the broker
  until the node reconnects.
- The broker's address is configuration every node needs.
- A direct connection between two machines is out of scope. Machine-to-machine traffic goes through the broker.

## Alternatives Considered

- The broker connects to each machine. Every machine would need an address the broker can reach, which NAT and
  firewalls rule out.
- Machines connect to each other directly. Every pair would need to reach each other, with the same problem, and there
  would be no single place to answer queries.

## References

- NIT-004 (originating proposal)
