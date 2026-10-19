# ADR-04: Lease Record Envelope with a Liveness Kind

**Status:** proposed
**Date:** 2026-10-19

## Context

ADR-02 fixed the lease record envelope and its evolution rules. Its envelope judges every holder by its process: a
holder is alive while its PID runs with the recorded start time.

Live tests on 2026-10-18 found that Codex and opencode run every session's shell commands under one long-lived host
process by default. A session can end while its host runs on for days. NIT-001 now judges those sessions by hooks and
plugins instead. Such a lease can outlive its host process. It reads unknown after a Codex host restart, and alive while
an opencode turn keeps running after its TUI dies.

An ADR-02 reader judges an unknown record by its envelope. It would read such a lease as dead once its host process
died, and clear it. Session liveness also adds state files beside the records, which old readers must judge safely. A V1
binary can never learn a forward-compatibility rule after it ships.

This ADR restates ADR-02's envelope in full, so the live rules sit in one accepted document.

## Decision

Records and tombstones share one envelope, versioned `major.minor`. Every version keeps the envelope readable:
`version`, `id`, `machine`, the holder's agent, PID, and process start, a `footprint`, a `clear_when_dead` bit, and a
`liveness` kind. The process start's platform shapes are frozen.

The liveness kind says how to judge the holder:

- `process` judges the holder by its PID and process start
- `session` judges the holder by its agent's session rule in NIT-001 "Session liveness on shared hosts". The holder's
  PID and process start name its host process
- A reader that does not know the kind, finds it absent, or has no session rule for the record's agent, reads the
  holder's liveness as unknown. Unknown counts as alive

The footprint says what the record blocks: typed entries, `none`, or `all`. An absent footprint, or an entry type a
reader does not know, counts as `all`. `none` is never inferred from a record's kind. Entries compare by type: path
entries use the path rule, and name entries use exact match. A path entry and a name entry never overlap. A later entry
type defines how it pairs with every older type when it is introduced.

Readers:

- A reader accepts a higher minor of its own major and ignores fields it does not know. A higher major, or an
  envelope that does not decode, makes the record unusable
- A minor version may add a field only if leaving it out never makes an old reader more permissive. That covers
  claiming, clearing a dead record, delivering a tombstone, releasing, and `break`. Any other field needs a new major
- A record with an unknown body is judged by its envelope. It blocks only the claims its footprint overlaps. It is
  cleared after its holder dies only when `clear_when_dead` is true and its liveness kind reads dead
- An unknown platform tag, or a record from another machine, has unknown liveness, which counts as alive

Writers:

- An old binary never rewrites a record of a higher minor. It may remove its own such record on `release`. `break` on
  one needs `--force`, even when its holder is dead
- The state directory carries a `format` marker naming the minimum writer version. A binary below it refuses every
  write and still reads

Session files, which session liveness keeps under `sessions/`, follow the same version rules. A session file a reader
cannot use, or one of a higher major, makes the session it names unknown, never dead. An old binary never rewrites or
removes a session file of a higher version, or one it cannot decode. Codex end marks are empty files whose name format
is fixed. A change to that format takes a new directory under the `format` marker.

An unusable record refuses every claim, shows as unknown in `list`, and is removable only with `break --force`. An
unusable tombstone never blocks a claim.

A reader that knows a record's kind checks that the footprint equals the canonical keys stored in the body. It never
resolves paths again for that check. A mismatch makes the record unusable.

## Rationale

ADR-02's rationale stands for every part this ADR keeps. Ignoring unknown fields alone is safe only for readers, so the
no-rewrite rule stays. The minor rule covers every reader decision, so an old binary cannot undo a newer guarantee. The
explicit footprint keeps a record of an unknown kind from blocking every claim.

The liveness kind is the one envelope fact an old reader needs to avoid clearing a live lease. With it, a later rule
that lets a lease outlive its process is safe for every older reader. Without it, each such rule would need a new major,
and every older binary would treat those records as unusable.

Reading an unknown kind, or an agent with no session rule, as unknown follows ADR-02's rule for an unknown platform
tag. It fails safe: a claim is refused rather than stolen. Session files take the same direction. An old reader that
skipped a session file it could not read would find no evidence of life, and read a live session as dead.

## Consequences

- Every proposal that adds a record, tombstone, or session-file field must show that the field passes the minor rule,
  or take a new major
- Every V1 binary carries the envelope, footprint, liveness-kind, minor-version, no-rewrite, and `format` checks forever
- A record of a kind or agent a reader does not know blocks the claims its footprint overlaps until an upgrade or
  `break --force`
- A later liveness rule adds a kind instead of a major
- A breaking change raises the `format` marker, which locks older binaries out of writes on that machine
- Guardians read the computed liveness through the versioned `list --json` schema, never the files

## Alternatives Considered

- Keep ADR-02 as is, with one TUI per host process. Every lease's liveness stays tied to its process. NIT-001 rejected
  it: nitipan cannot assume how a tool is launched
- Add the session rules as a new major. Every ADR-02 reader would treat every Codex and opencode lease as unusable
- ADR-02's own alternatives: blocking every claim on any unknown version, an integer version with no envelope, and path
  keys inferred from the kind

## References

- NIT-001 (originating proposal): "Session liveness on shared hosts", "Record evolution and unknown records", "Lease
  record fields", "Record encoding and file naming"
- Supersedes ADR-02
