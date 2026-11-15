# Proposals

| Proposal | Description | Status |
|----------|-------------|--------|
| [NIT-001](NIT-001-local-lease-cli.md) | Local lease CLI: acquire, release, list, break, advertise, and setup on one machine | implemented |
| [NIT-002](NIT-002-resource-leases-and-slots.md) | Resource leases such as `cpu:heavy`, and counted slots shared by N holders | implemented |
| [NIT-003](NIT-003-sub-leases.md) | Sub-leases a holder delegates for part of its domain | implemented |
| [NIT-004](NIT-004-lease-broker.md) | Self-hosted broker with expiring leases, per-machine nodes, and query discovery | implemented |
| [NIT-005](NIT-005-fencing-tokens.md) | Fencing tokens issued per grant and checked by the receiving side | implemented |
| [NIT-006](NIT-006-broker-auth.md) | Self-hosted user auth for the broker: label bindings, delegations, and cross-user mail grants | implemented |
| [NIT-007](NIT-007-session-mailbox.md) | Per-session mailboxes on the broker for cross-machine messaging | implemented |
| [NIT-008](NIT-008-sub-lease-recall.md) | Recall a sub-lease with a deadline every reader enforces | implemented |
| [NIT-009](NIT-009-fence-fallback-for-refusing-hosts.md) | Lease-check fallback for hosts that refuse fence refs, with a three-state fence mode per remote | implemented |
