# Sub-Lease Prior Art

**Date:** 2026-10-18

This is a research write-up for NIT-003. Its claims are unproven. A research agent wrote the survey from memory and did
not fetch most sources, so spot-check each link before relying on it.

No surveyed system has a parent lease that hands a child range to a different holder under one shared overlap check.
Every option below is built from an analogue.

## Multiple-granularity locking

Gray, Lorie, Putzolu, and Traiger, "Granularity of Locks and Degrees of Consistency in a Shared Data Base" (1976). A
lock on a node covers its subtree. Intention modes on ancestors, IS, IX, and SIX, let a fine lock coexist with a coarse
one. Conflicts come from a compatibility matrix, not from equality.

The mapping to delegation is partial. IX is compatible with any other IX, but NIT-003 lets only the holder delegate.
The useful idea is that the parent's record changes mode, from exclusive on the whole subtree to exclusive minus the
delegated children. The overlap check then becomes a small matrix keyed by mode and parent link. That replaces an ad
hoc exception, which is what NIT-003's exclusivity risk warns about.

## Hierarchical and delegated leases

- Chubby has no sub-locks. A lock is per node. A session's death frees all its locks after a grace period. This points
  toward flat records with independent lifetimes.
- ZooKeeper ephemeral nodes cannot have children, and a node with children cannot be deleted. That supports forbidding
  nesting under a session-bound node, and refusing a parent's release while children exist.
- etcd attaches many keys to one lease, and they die with it. Leases do not nest. This is the model for a sub-lease
  that ends with its parent.
- Consul sessions carry a `behavior` field. On invalidation, `release` keeps the key without a lock, and `delete`
  removes it. A `lock-delay` follows. This is precedent for choosing parent-death behavior per grant.
- NFSv4 delegations, in RFC 8881 sections 10.4 and 10.9, are recalled with CB_RECALL. A client that does not return a
  delegation within the lease period has it revoked. The client learns through status bits on later calls, which is
  close to NIT-001's tombstones. This suggests revocation in two steps: recall, then force.

## Capability attenuation

- Macaroons let any holder add caveats, and caveats only narrow rights. Revocation needs the verifier to check state. A
  bearer token suggests a shape where the parent mints a token and the sub-lessee presents it on `acquire`.
- UCAN delegations name an audience. Scope and expiry must be at most the parent's. Revoking one link voids every link
  below it. This supports strict nesting and cascading revocation. The delegator's death does not void the chain, but
  expiry bounds it.
- SPIFFE has no delegation chain. Short-lived identities expire when the issuer stops renewing them. In nitipan terms,
  the child dies with the parent, with no explicit revocation.

## Process trees

- Orphans survive and are reparented to init or to a subreaper set with `PR_SET_CHILD_SUBREAPER`. This is precedent for
  a sub-lease that survives and becomes ordinary.
- `PR_SET_PDEATHSIG` signals a child on its parent's death. It fires on thread exit, a known pitfall, and macOS has no
  equivalent. Without a push signal, the child learns on its next call, as with tombstones.
- `cgroup.kill` kills a whole subtree at once. This is the model for cascading a break.

## Agent tooling

mcp_agent_mail has advisory file and glob reservations, exclusive or shared, with a TTL and an optional pre-commit
guard. It has no delegation. Claude Code agent teams assign directory ownership in the lead's spawn prompt, by
convention only. No surveyed tool grants sub-ownership.

## Options per open question

- Record shape. A separate record with a parent link follows Chubby, ZooKeeper, and UCAN. It reuses NIT-001's scan and
  gives each holder a stable identity. It costs consistency across records. A delegation list on the parent follows
  etcd and Gray. Cascade becomes atomic, but every grant rewrites the parent.
- Overlap. A sub-lease strictly inside its parent, as in UCAN and Gray, always leaves the parent a remainder. One equal
  to the parent is a full handoff, as in NFS. Either way, the parent's right to write in the delegated part is open.
  NFS says no until recall.
- Parent death. Ending with the parent, as in etcd, SPIFFE, and `cgroup.kill`, is simple and safe. It kills in-flight
  work. Surviving as an ordinary lease, as with orphans and Consul `release`, keeps work going. It needs a re-check
  under the lock, and the parent's leftover keys become claimable around it. A per-grant choice, as in Consul, adds
  surface.
- Revocation. An immediate force with a tombstone, as in NIT-001's `break`, is fast and abrupt. A recall with a
  deadline, as in NFS, is gentler. It needs a timer, and NIT-001 has none.
- Nesting. Forbid it, as ZooKeeper does for ephemerals, or allow it with attenuation, as UCAN does. Allowing it makes
  cascading revocation and tombstone fan-out mandatory.
- Invocation. A guardian that names the sub-lessee binds identity, as a UCAN audience does. The sub-lessee may not exist
  yet. A token the sub-lessee presents, as with macaroons, works while the parent is idle. It is a bearer secret, so
  anyone holding it can claim.

## Questions NIT-003 lacked

- Whether the parent can break its own sub-lease without `--force`, or whether that is just revocation
- Whether a sub-lessee may break its parent
- Whether a third party's forced break of the parent cascades, and who gets tombstones
- Whether the parent may release while sub-leases exist
- Whether a sub-lessee's early release merges its keys back into the parent
- How `list` shows the tree
- How consent works when the parent is idle and cannot answer
- How NIT-005's fencing tokens nest

## Sources

- [Granularity of Locks and Degrees of Consistency in a Shared Data Base](https://web.stanford.edu/class/cs240/readings/granularity-of-locks.pdf)
- [The Chubby lock service](https://research.google/pubs/the-chubby-lock-service-for-loosely-coupled-distributed-systems/)
- [ZooKeeper Programmer's Guide](https://zookeeper.apache.org/doc/current/zookeeperProgrammers.html)
- [etcd lease API](https://etcd.io/docs/v3.5/learning/api/#lease-api)
- [Consul sessions](https://developer.hashicorp.com/consul/docs/dynamic-app-config/sessions)
- [RFC 8881, NFSv4.1](https://www.rfc-editor.org/rfc/rfc8881)
- [Macaroons](https://research.google/pubs/macaroons-cookies-with-contextual-caveats-for-decentralized-authorization-in-the-cloud/)
- [UCAN spec](https://github.com/ucan-wg/spec)
- [SPIFFE concepts](https://spiffe.io/docs/latest/spiffe-about/spiffe-concepts/)
- [prctl(2)](https://man7.org/linux/man-pages/man2/prctl.2.html)
- [cgroup v2](https://docs.kernel.org/admin-guide/cgroup-v2.html)
- [mcp_agent_mail](https://github.com/Dicklesworthstone/mcp_agent_mail)
