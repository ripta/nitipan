# ADR-03: Lease Key Canonicalization and Overlap

**Status:** accepted
**Date:** 2026-10-18

## Context

A lease's domain is a checkout plus optional path scopes. nitipan turns each one into a key and stores the canonical
keys in the lease record. Two claims conflict when their keys overlap.

Paths have many spellings. A scope can be a symlink, can escape its checkout, or can name a directory that does not
exist yet. macOS volumes are case-insensitive by default. Claude Code puts worktrees under `.claude/worktrees/` inside
the repo they belong to, and repos can hold submodules and nested repos.

Several relations compare keys: overlap between holders, "covered by your leases" on re-acquire, the domain filter on
`list`, and containment for NIT-003 sub-leases.

## Decision

Every key, the checkout's included, is canonicalized one way:

- A scope that is absolute or has a `..` component is rejected before anything is resolved
- The longest existing ancestor of the joined path is resolved fully, with symlinks followed, and takes the
  filesystem's canonical case on a case-insensitive volume
- A key that resolves outside its checkout is rejected
- The part that does not exist yet is appended. On a case-insensitive or normalization-insensitive volume it is
  compared with case folded and Unicode normalized. Each key records its volume's sensitivity

Two keys overlap when one equals the other, or is its ancestor by whole path components. The one exemption: a git
checkout boundary lies strictly below the ancestor and at or above the descendant. A boundary is a directory holding a
`.git` file or directory. A submodule of the enclosing repo is not a boundary. Boundaries are found from the filesystem
at check time and never stored.

Every relation between keys uses this one rule.

The canonical keys are stored in the record, and a path record's footprint under ADR-02 lists exactly those keys.
Readers compare stored keys and never resolve a stored key's path again.

## Rationale

Lexical joining let a symlinked scope, or `Docs` against `docs`, give two successful claims on one directory. Resolving
only the existing prefix lets a scope be claimed before its directory exists. Recording volume sensitivity makes every
reader compare a stored key the same way.

Without the boundary exemption, a guardian on a repo blocks every session in a nested worktree. That defeats the
guardian protocol's own pattern of separate sessions in their own worktrees. A stored checkout root goes stale when a
worktree is removed, and then lets a claim on the repo cover a directory another session holds. Computing the boundary
at check time follows the filesystem as it is now. Submodules stay part of the parent, because the parent's git
commands write into them.

One rule for every relation keeps a guardian from being told a nested worktree is "already covered" by a lease that
does not actually protect it.

## Consequences

- Canonical keys are stored in records, so this rule is part of the record format. Changing it makes old and new keys
  compare wrong, and needs a new record major under ADR-02
- NIT-003 containment, `list` filtering, and every later key relation must use this rule
- A symlink or `.git` created later inside a not-yet-existing tail, or at the same moment as a claim, can change what a
  key names
- A worktree removed and replaced by a plain directory while two leases are held leaves both leases on one directory.
  The check runs only at claim time
- A bare repo whose `core.worktree` points elsewhere is not seen as a boundary

## Alternatives Considered

- Lexical joining after resolving the checkout. Misses symlinked, escaping, and case-variant scopes
- Comparing keys by device and inode. A scope cannot be claimed before it exists, a recreated directory loses its
  claim, and a guardian reading `list --json` cannot interpret an inode
- The plain ancestor rule across checkouts. A repo lease blocks every nested worktree
- A checkout root stored with each key. It goes stale when a worktree is removed
- Comparing keys only within one checkout. Misses a scope that crosses into a nested checkout

## References

- NIT-001 (originating proposal): "Domain keys and overlap", "Canonicalizing scope keys", "Nested checkouts"
- NIT-003: sub-lease containment uses this rule
- ADR-02: canonical keys are part of the record format it versions
