# ADR-02: Lease Record Envelope and Evolution

**Status:** accepted
**Date:** 2026-10-18

## Context

nitipan stores each lease, and each tombstone, as a JSON file in the state directory. Several nitipan versions may run
on one machine at once. An agent can find an older binary on `PATH` while a newer one wrote the records.

Later proposals add fields: a slot count in NIT-002, a parent link in NIT-003, an expiry in NIT-004, a fencing token in
NIT-005, and a recall deadline on tombstones in NIT-008. The user wants additive fields not to break older readers.

A V1 binary can never learn a forward-compatibility rule after it ships. Every rule an old binary needs must ship in
V1.

## Decision

Records and tombstones share one envelope, versioned `major.minor`. Every version keeps the envelope readable:
`version`, `id`, `machine`, the holder's agent, PID, and process start, a `footprint`, and a `clear_when_dead` bit. The
process start's platform shapes are frozen.

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
  cleared after its holder dies only when `clear_when_dead` is true
- An unknown platform tag, or a record from another machine, has unknown liveness, which counts as alive

Writers:

- An old binary never rewrites a record of a higher minor. It may remove its own such record on `release`. `break` on
  one needs `--force`, even when its holder is dead
- The state directory carries a `format` marker naming the minimum writer version. A binary below it refuses every
  write and still reads

An unusable record refuses every claim, shows as unknown in `list`, and is removable only with `break --force`. An
unusable tombstone never blocks a claim.

A reader that knows a record's kind checks that the footprint equals the canonical keys stored in the body. It never
resolves paths again for that check. A mismatch makes the record unusable.

## Rationale

Ignoring unknown fields alone is safe only for readers. V1 rewrites whole records on re-acquire, `--replace`, and
`advertise`. An old binary would silently drop newer fields on every such rewrite. The no-rewrite rule closes that.

The minor rule covers every decision a reader makes, not only claims. A field whose omission made an old reader clear,
deliver, or release more freely would let an old binary undo a newer guarantee.

An explicit footprint lets a record of an unknown kind block only what it occupies. A dead holder's NIT-002
`cpu:heavy` record then never blocks every path claim on the machine. The kind alone does not say what a record
occupies, so "blocks nothing" is never inferred from it.

`clear_when_dead` lets a writer protect a record whose holder's death carries duties an old reader cannot perform,
such as a record shared by several holders. The `format` marker is the one lever for a change no per-record rule can
express. An example is a grant that must write a token: a V1 binary that grants a lease would write none.

## Consequences

- Every proposal that adds a record or tombstone field must show that the field passes the minor rule, or take a new
  major
- Every V1 binary carries the envelope, footprint, minor-version, no-rewrite, and `format` checks forever
- A record of an unknown kind can block claims until its holder dies, or forever when `clear_when_dead` is false. Only
  an upgrade or `break --force` clears it
- A breaking change raises the `format` marker, which locks older binaries out of writes on that machine
- Guardians read leases through the versioned `list --json` schema, not the record files

## Alternatives Considered

- Block every claim on any unknown version, kind, or reach. A dead holder's newer record would block the machine
  forever, and every additive field would break every V1 reader
- A single integer version whose readers ignore unknown fields, with no envelope. Old binaries drop fields on rewrite,
  and a dead holder's newer record still blocks forever
- An envelope with path keys inferred from the kind. A future kind that does overlap paths would be read as blocking
  nothing

## References

- NIT-001 (originating proposal): "Record evolution and unknown records", "Lease record fields"
- NIT-002, NIT-003, NIT-004, NIT-005, NIT-008: proposals that add fields under this rule
