# NIT-002: Resource Leases and Counted Slots

**Status:** accepted
**Created:** 2026-10-18
**Updated:** 2026-10-23

## Dependencies

- NIT-001. Its "Record evolution and unknown records" states that a path entry and a name entry never overlap. Its "A
  supported read path for guardians" lets a minor `list --json` version add a `domain.kind` value. See "Risks"

## Impacts

- NIT-001 — "Domain keys and overlap", "Claiming", "Lease record fields", "Re-acquiring as the same holder",
  "Releasing", "Replacing a lease", "A supported read path for guardians", "Record evolution and unknown records", and
  "Wider-reach lease names". A resource name is not a path, a counted slot lets several live holders share one name,
  and `release`, `list`, and `--replace` gain a resource form
- NIT-003 — whether a resource lease can be sub-leased. NIT-002 does not decide it
- NIT-004 — "Name syntax". The `repo` namespace is refused at `machine` reach, so wider-reach `repo:` names never
  collide with a machine resource

## Summary

A resource lease names a machine resource rather than a path. `cpu:heavy` means "all cores." A counted slot lets up to
N holders share one name at once. Both live at `machine` reach. They use NIT-001's claim, liveness, and break rules.
`acquire --wait` lets a caller wait for a lease instead of being refused at once.

## Motivation

Sessions on one machine compete for more than files. Two sessions each running a full build or test suite slow each
other down. A session about to use every core has no way to say so, and no way to wait for another session to finish.

Some resources take a few users at once, but not unlimited ones. A local database or a GPU might allow two jobs. A
plain exclusive lease is too strict for those, and no lease is too loose.

## Design Decisions (Settled)

The first three come from the roadmap the user approved on 2026-10-18, recorded in the research write-up.

### Resource names

A resource lease's name is a resource, not a path. `cpu:heavy` is the example. Resource names are `machine` reach and
never leave the machine.

A resource record whose `machine` differs from the reader's neither overlaps nor counts toward a slot. Another
machine's CPU or GPU is a different resource. This differs from path records, whose unknown liveness counts as alive
because a shared home can share the same files. `list` shows such a record as remote.

### Counted slots

A counted slot lets N holders hold one name at once. The N+1th claim is refused while N live holders remain.

### Invocation surface

NIT-001 settled the surface as CLI only. This proposal adds `--resource`, `--slots`, and `--wait` to that CLI. It does
not choose the surface.

### Related resources

`cpu:heavy` means all cores. The research did not say whether lighter CPU claims exist, or whether they would overlap
`cpu:heavy`. A name hierarchy would need an overlap rule beyond NIT-001's path rule. The candidates were a built-in
conflict table, weighted claims on one counted name, flat names with exact match, and hierarchical names with ancestor
overlap.

Decision (autonomous): resource names are flat. Two names overlap only when they are equal. A resource record's
footprint is a plain `name` entry, which NIT-001's envelope already defines as exact match for every V1 reader.

- `cpu:heavy` is one name. A lighter use is either an unrelated name or a counted slot of its own
- A light claim and a heavy claim cannot see each other. A session running one test is not told about a full build
- Adding weighted claims later fails NIT-001's minor-version rule. An older slot reader would count a heavy record as
  one holder and admit a light claim beside it. Weights would need a new major and a raised `format` marker, which is
  the recovery path

A built-in conflict table was rejected. A binary built before a pair was added allows a claim a newer binary refuses,
so a mixed install grants `cpu:heavy` beside `cpu:light` silently. The overlap rule would also live in the binary
instead of the record. Hierarchical names were rejected. They need a new footprint entry type, which every older reader
treats as `all`, so a live `cpu:*` holder would block every V1 path claim. Siblings such as `cpu:heavy` and `cpu:light`
also do not overlap under an ancestor rule. Weighted claims were rejected for now. No caller needs light claims, and a
heavy claim for every unit starves behind a stream of light ones unless waiting gains a reservation. Writing a constant
`weight: 1` now, to make weights a minor change later, was rejected as speculation for the same reason.

### Record shape for slots

The candidates were one record per holder, all naming the same resource, and one record per resource that lists its
holders.

Decision (autonomous): one record per holder. Each holder of a slot has its own ULID-named file under NIT-001's
"Record encoding and file naming", with its own holder, liveness, and `clear_when_dead`. A claim counts the live records
with that name under the lock. A dead holder's record is cleared the way any dead lease is.

- The footprint is a plain `name` entry. The slot count lives in the body. A new `slot` entry type would read as `all`
  to every V1 reader and block every V1 path claim
- A record with no slot count is exclusive. That is stricter than any count, so the field passes NIT-001's
  minor-version rule
- One holder holds at most one slot per name. A second claim on the same name by the same holder is a re-acquire under
  NIT-001's "Re-acquiring as the same holder", and succeeds without taking a second slot
- `list` and `list --json` show each slot name's effective count and its live holders, so "2 of 2" is never mistaken
  for two conflicting exclusive records. The effective count is the minimum the admission rule in "Who sets N" uses

How live records that disagree on the count are judged is part of "Who sets N".

One record per resource was rejected. NIT-001's envelope names one holder per record. A holder list cannot be expressed
without a new major, so a V1 binary would treat the record as unusable and refuse every claim on the machine, path
claims included, even after every holder died. Every claim and every dead-entry cleanup would also rewrite a shared
record.

### Record and output versions

Resource records and the slot count arrive in a minor version of NIT-001's major. Resource records are version 1.1, and
`list --json` is schema 1.1. Amended 2026-10-23; see Decision Log.

- The body of kind `resource` holds `name` and `slots`, the N the holder was admitted under. The footprint is exactly
  one `name` entry equal to `name`. A mismatch makes the record unusable, per NIT-001's footprint check
- A writer stamps the lowest minor whose fields the record uses. Path records keep NIT-001's version, so an older
  binary can still rewrite them
- The `format` marker stays at NIT-001's value. Every new field passes the minor-version rule, so no older writer needs
  to be locked out
- A record with a matching `name` entry whose body a reader does not know counts as exclusive, as NIT-001's envelope
  already judges it
- `list --json` takes a minor schema version. It adds kind `resource`, the remote flag, and the effective count as a
  computed field on each local resource lease. A remote resource lease carries no effective count, since this machine's
  config and holders say nothing about its machine. Every lease stays a top-level entry, so a consumer that iterates
  leases sees every slot holder. A consumer counting a slot's holders skips entries with the remote flag

### Waiting

NIT-001's `acquire` refuses at once. The candidates were no wait, a bounded polling wait, a wait with no timeout, and a
bounded wait with a FIFO queue of waiter records.

Decision (autonomous): `acquire --wait <duration>` waits for a lease. The duration is required. It applies to path and
resource claims alike. Amended 2026-10-18; see Decision Log.

- It polls with backoff. It takes NIT-001's lock only for each attempt and releases it between attempts
- Only three refusals are waited on: a live holder overlaps, the slot is full, or the lock is busy. A lock-busy attempt
  counts as a failed attempt, not as the lock-busy exit
- Every other refusal exits at once with its own status and message. That covers an unusable record, a dead holder's
  record with `clear_when_dead` false, a self-overlap that needs `--replace`, a mismatched `--slots`, the `format`
  marker, and a sandbox write error
- When the duration passes, `acquire` exits with its own wait-timeout status. The message names the holders it waited
  on. When the lock was busy at the deadline, it names the lock holder too
- Nothing persists while waiting. A waiter killed by its shell tool leaves nothing behind
- A retry after a kill is safe. If the lease was published before the kill, the retry is a re-acquire and succeeds.
  The help text and README tell callers to retry, or run `list`, after a killed `acquire`
- There is no queue. A waiter can lose a freed slot to a claim that arrived later

No wait was rejected. It pushes the polling loop into every caller's shell, with the same unfairness and worse
messages. A wait with no timeout was rejected. The shell tool kills it without a clean status or a named holder, and an
orphan outlives a dead agent. The FIFO queue was rejected. Its waiter records die with each invocation, so a wait
longer than the shell tool's limit re-enters at the back on every retry. A stopped waiter at the head also reads as
alive and blocks every other claim on the name. A reservation keyed on the agent's holder would fix both, but only
weighted claims need it.

A warning when the duration exceeds the agent's shell-tool limit was dropped in review. Claude Code's limit is a
per-call parameter, and background runs have none, so a constant in the binary misfires both ways.

### Name syntax

The research gives one example. It does not say whether resource names have a fixed set of namespaces, such as `cpu:`
and `gpu:`, or are free text after a colon. It also does not say how a resource name is told apart from a path in the
`domain` field. The candidates were a fixed set of namespaces built into the binary, a free-form namespace and name
under a strict grammar with an explicit flag, free text compared byte for byte, and the strict grammar told apart from
a path by its shape.

Decision (autonomous): a resource name is `<namespace>:<name>` under a strict grammar. The record's `domain.kind` is
`resource`. On the CLI, a resource is given with `--resource <name>`. Amended 2026-10-23; see Decision Log.

- Each part holds lowercase ASCII letters, digits, `-`, `_`, and `.`. There is exactly one colon, both parts are
  non-empty, and the whole name is at most 64 bytes
- A name outside the grammar is refused, never folded, so the stored name is the only spelling. When a name is refused
  for case, the error suggests the lowercase spelling without applying it
- The namespace `repo` is refused at `machine` reach. NIT-004 drafts `repo:` names for wider reach, and a machine
  resource with the same spelling would match its name entries. No other namespace is reserved
- One record names exactly one resource. A second `--resource` in one call is refused, since a record carries one slot
  count
- `--resource` cannot be combined with path arguments in one call
- `release --resource` and `list --resource` match a lease whose name is equal, the way domain arguments do for paths
- `--replace` is refused when the claim or the lease it names is a resource. Under flat names, a different name is a
  different lease, and a path lease never turns into a resource lease or back
- `--slots` is refused on a path claim

A fixed namespace set was rejected. An older binary reads `acquire gpu:main` with a namespace it does not know as a
relative path. NIT-001 lets a scope that does not exist yet be claimed, so it publishes a path lease, and two sessions
both believe they hold the GPU. A typo does the same on any version. Telling a resource from a path by shape was
rejected for the same failure: `db:pg/main` or `cpuheavy` silently become path claims. Free text was rejected.
`cpu:heavy`, `CPU:heavy`, and a trailing space give three names that never coordinate, and control characters reach
`list` output.

### Who sets N

The candidates were the first claimant setting N, every claim stating N, a per-machine config file, and the config file
with a stated N the claim expects. A fifth arose in review: the config file and stated N, with a rule that never admits
more holders than any live holder was admitted under.

Decision (autonomous): N comes from a per-machine config file in the state directory, and admission never exceeds the N
any live holder was admitted under. Amended 2026-10-18; see Decision Log.

- The file is `slots.json` in the state directory NIT-001 resolves. That is the only directory nitipan resolves, and
  `~/.config` is unreadable from inside the Claude Code sandbox. It holds a `version` and a map from names to N, and
  nothing else. It is read under the lock on every resource claim
- A name with no entry has N = 1, which is exclusive
- Each record copies the config's N at the moment it was admitted. That is the record's N
- A claim may state the N it expects with `--slots <N>`. A mismatch with the config is refused, and the error names the
  configured value
- A claim is admitted only while live holders number fewer than the minimum of the config's N and every live record's
  N. Only this machine's records count, for the holders and for the minimum. Lowering N takes effect at once. Raising
  N takes effect once every holder admitted under a lower N has released. The same rule judges live records that
  disagree
- N is not an advertisement field. A re-acquire by a holder that already has a record for the name always succeeds as
  a refresh, even when the slot is now over its count, and never waits on itself. It keeps the record's N. A stated
  `--slots` on a re-acquire is compared with the record's N, not the config's, and a different value is refused
- N must be an integer of at least 1. Each name must pass the grammar and must not use `repo`. Any invalid entry or an
  unknown `version` makes the whole file unparseable, and every error in it is reported together
- An unparseable config treats every name as N = 1. Every call warns with the config's absolute path and its errors,
  and says to ask the user to fix the file. Path claims are unaffected
- A refusal for a full slot names the holders and says to ask the user to raise N for that name. It names neither the
  config's path nor an edit to it, the same way NIT-001's errors name no bypass. `list` shows the config's path

A holder admitted under the old N pins the old count for as long as it holds, since a re-acquire keeps its record's N.
This is a known limit. That holder can release, or a user can `break --force` it.

The first claimant setting N was rejected. Any caller's N, or its omission, would set the count for everyone, and a
caller that left N out could join as holder 2 of 2 when it expected the GPU to itself. Under the config, the count is
one the user set for that name. Every claim stating N was rejected. N is a fact about the machine, such as one GPU on a
laptop and four on a workstation, but the callers follow instructions shared across machines. Those instructions would
hard-code one N and flap with refusals. A config without the minimum rule was rejected. Raising N from 2 to 3 would
admit a third job beside two that each sized their work to half the GPU.

## Design Decisions (Open)

None.

## Risks

- **Advisory leases on resources are ignored** — likelihood: high, impact: low.
  Nothing stops a process that never calls nitipan from using every core. Accepted: resource leases coordinate
  coöperating sessions only. The README says so.
- **A V1 binary compares a `name` entry with a `path` entry wrongly** — likelihood: low, impact: high.
  Every V1 binary applies the envelope forever. One that treats a mixed pair as overlapping would refuse every path
  claim while a resource is held. Mitigation: NIT-001 and ADR-02 state that a path entry and a name entry never
  overlap, while an unknown type still counts as `all`. NIT-001's milestone 1 and this proposal's milestone 1 both test
  that a resource lease never overlaps a path lease. Amended 2026-10-18; see Decision Log.
- **NIT-002 rests on NIT-001's envelope** — likelihood: low, impact: medium.
  A change to the envelope, the minor-version rule, or the re-acquire rule would change this design. NIT-001 is
  accepted, and ADR-02 records the envelope, so such a change now needs a superseding ADR. Mitigation: NIT-002's queue
  rows depend on NIT-001, so none is built first. NIT-002 is amended if NIT-001 changes those sections.
- **The name grammar, the resource body, and the config syntax are one-way doors** — likelihood: medium, impact:
  medium. Names end up in shared agent instructions and stored records. Mitigation: the grammar starts strict, and
  loosening it later breaks no stored name. The config carries a `version`, and an unknown one fails closed to N = 1. A
  breaking record change can raise NIT-001's `format` marker.
- **A wait killed after it publishes leaves a lease its agent does not know about** — likelihood: medium, impact:
  medium. The agent sees a killed command and moves on, and holds the resource for its process's life. Mitigation:
  the help text and README tell callers to retry or run `list` after a killed `acquire`. A retry is a re-acquire and
  reports the lease. `list` shows the holder by handle, and a human can `break --force` it.
- **Subagents of one session share one resource lease** — likelihood: medium, impact: medium.
  Under NIT-001's holder rule, two parallel subagents each claiming `cpu:heavy` both succeed, and the first to release
  frees the name while the second still builds. Accepted: no per-subagent identity exists. The README tells callers to
  acquire resources from the parent session only.
- **Agents can write the slot config** — likelihood: low, impact: medium.
  The sandbox allowance covers the whole state directory, so an agent could raise N to get past a refusal. Accepted:
  the same allowance already lets an agent delete records. The full-slot refusal names the user as the one to raise N
  and does not print the path. Raising N admits no one while the old holders remain.

## Milestones

### M1: Resource leases

**Status:** DONE

- `acquire --resource` claims a resource name, and a second live claim on it is refused with the holder named
- A resource lease never overlaps a path lease
- A name outside the grammar is refused, and a name refused for case suggests the lowercase spelling
- The `repo` namespace, a second `--resource`, `--resource` with path arguments, and `--slots` on a path claim are each
  refused
- `--replace` is refused for a resource claim naming a path lease, for a path claim naming a resource lease, and for a
  resource claim naming another resource lease
- `release --resource` and `list --resource` match a lease by equal name
- A resource record from another machine neither blocks a claim nor counts, and `list` shows it as remote
- `list` shows resource leases beside path leases, and `list --json` emits the new minor schema
- Path records written by this binary keep NIT-001's version, and resource records carry the minor that adds them
- A binary that knows only NIT-001's version, reading a resource record, does not block path claims with it, does not
  clear it during a path claim, needs `--force` to `break` it, and never rewrites it
- `list --json` keeps every resource lease as a top-level entry, and gives no effective count on a remote one

**As built:**

- A NIT-001-only binary was checked against a record the new binary wrote. The binary came from the run's base commit,
  built from a `git archive` copy, and its own test harness drove it over a temporary state directory. With the holder
  alive and with it dead, a path claim by another session and by the holder itself succeeded and left the record
  byte-identical. `break` without `--force` was refused in both states, and `break --force` removed it. `advertise` by
  the holder was refused as a newer minor and left the record unchanged. `list --json` showed it as kind `resource`
  with its footprint and no domain. That binary reads a dead holder's record as unclearable, as the envelope rules
  require. The check script was not kept.
- The effective count is not in the output yet, so no lease carries one and the remote lease check holds trivially. The
  count arrives with M2. The `remote` flag is set on resource leases only, since path leases from another machine keep
  counting as alive.
- A claim skips another machine's record only when its kind is `resource`. A remote record of an unknown kind with a
  footprint of `all` still blocks, as the envelope requires.
- The resource body carries `slots`, written as 1, so M2 needs no record change. M1 judges a re-acquire against the
  record's count and a new claim against a count of 1.
- A resource name given to `release` and `list` passes the same grammar, so a typo gets the lowercase suggestion there
  too.
- An empty `--resource ""` reads back from the flag library as no value, which would have claimed the working
  directory. It is refused as a usage error.
- A broken resource lease puts its name in the tombstone's `paths`, so the notice reads "lease ID on cpu:heavy".

### M2: Counted slots

**Status:** NOT STARTED

- N live holders can hold one slot name, and the N+1th claim is refused
- A dead holder's slot is freed for the next claim
- Concurrent claims never exceed N, in a test that races them
- N comes from `slots.json` in the state directory, and a name with no entry is exclusive
- Each record copies the config's N at admission
- A remote record counts toward neither the holders nor the minimum
- After a raise, a holder beyond the old N is admitted only once every holder under the lower N has released
- A retry with the same `--slots` after a kill that followed publication succeeds, including after the config changed
- A `--slots` value that differs from the config is refused, and the error names the configured value
- Admission uses the minimum of the config's N and every live record's N. Lowering N takes effect at once, and raising
  it waits for the old holders to drain
- A re-acquire by a holder already on the name succeeds as a refresh when the slot is over its count, keeps the
  record's N, and refuses a `--slots` that differs from the record's N
- An unparseable config, including an invalid entry or an unknown version, treats every name as exclusive and warns
  with the path and every error. Path claims are unaffected
- A full-slot refusal names the holders and says to ask the user to raise N, without printing the config's path
- `list` and `list --json` show each slot name's effective count and live holders, and `list` shows the config's path

### M3: Waiting

**Status:** NOT STARTED

- `acquire --wait <duration>` requires a duration, and is granted once the blocking holder releases or dies
- The lock is held only for each attempt, in a test where another call succeeds while a waiter polls
- A lock-busy attempt during a wait counts as a failed attempt
- Every refusal other than a live overlap, a full slot, or a busy lock exits at once with its own status
- The timeout exit has its own status, and names the holders waited on, and the lock holder when the lock was busy
- A waiter killed mid-wait leaves no file in the state directory
- A retry after a kill that followed publication succeeds as a re-acquire
- The help text says to retry, or run `list`, after a killed `acquire`

## Decision Log

- 2026-10-18: Drafted from the lease broker research. Settled decisions are limited to the roadmap the user approved
  that day.
- 2026-10-18: The MCP server the research lists as optional V2 work is left to NIT-001's "Invocation surface"
  question. NIT-001's reviewer asked that NIT-002 not carry a duplicate question.
- 2026-10-18 (autonomous): Resource names are flat, and overlap only when equal. The footprint is a plain `name`
  entry. A conflict table permits silently under version skew, and a hierarchy's new entry type blocks every V1 path
  claim. Converged in one pass.
- 2026-10-18 (autonomous): Slots use one record per holder, with the count in the body and a plain `name` footprint. A
  record with no count is exclusive. A holder list would break NIT-001's one-holder envelope. Converged in one pass.
- 2026-10-18 (autonomous): `acquire --wait <duration>` polls with the lock released between attempts, has its own
  timeout status, and leaves nothing behind when killed. A FIFO of per-invocation waiter records is unfair across
  retries, and a stopped waiter blocks the name. Converged in one pass.
- 2026-10-18 (autonomous): Resource names are `<namespace>:<name>` under a strict lowercase grammar, given with
  `--resource`, one per record, with `repo` refused at `machine` reach. A fixed namespace set or shape-based parsing
  turns an unknown name into a silent path claim. Converged in one pass.
- 2026-10-18 (autonomous): N comes from a config file in the state directory, defaults to 1, and is copied into each
  record. Admission stays below the minimum of the config's N and every live record's N. Per-claim N hard-codes a
  machine fact into shared instructions, and a config without the minimum rule over-admits when N is raised. Converged
  in one pass, on an option the critic added.
- 2026-10-18 (autonomous): Coherence fixes. A resource record from another machine neither overlaps nor counts. Before,
  "Who sets N" let another machine's record with unknown liveness count against local slots, which contradicted
  `machine` reach.
- 2026-10-18 (autonomous): Amended "Waiting". It waited on every refusal and warned when the duration passed the
  agent's shell-tool limit. Now only a live overlap, a full slot, and a busy lock are waited on, and the warning is
  dropped, since Claude Code's limit is per call. Callers are told to retry or `list` after a kill.
- 2026-10-18 (autonomous): Amended "Who sets N". It did not say how a re-acquire meets the count, named the config's
  path in the full-slot refusal, and left the file's name, syntax, and validation open. Now a re-acquire is always a
  refresh that keeps its N and checks `--slots` against it. The refusal names the user, not the path. The file is
  `slots.json`, versioned, read under the lock, and invalid as a whole on any bad entry.
- 2026-10-18 (autonomous): Amended "Name syntax". It refused only a kind change through `--replace`. Now `--replace`
  is refused on any resource lease, since a different flat name is a different lease. The slot flag is `--slots`.
- 2026-10-18 (autonomous): Added "Record and output versions". Resource records take a minor of NIT-001's major, path
  records keep NIT-001's version, the `format` marker does not move, and `list --json` takes a minor schema version.
- 2026-10-18 (autonomous): Fixed stale text. "Invocation surface" said NIT-001's surface was pending review, and NIT-001
  has settled it as CLI only. The slots risk said the count had to land in V1, and the settled shape makes it a minor
  field. Widened Impacts to the NIT-001 sections this changes, NIT-003, and NIT-004.
- 2026-10-18 (autonomous): NIT-001's "Wider-reach lease names" hook fires on NIT-002 settling resource names. Its audit
  belongs in NIT-001, and was not made from this review.
- 2026-10-18 (autonomous): Risk review. Added the undefined `name`-against-`path` comparison in NIT-001, the dependency
  on NIT-001's envelope, the name grammar and config as one-way doors, a lease left by a killed wait, subagents sharing
  a resource lease, and an agent-writable config. Each carries a mitigation or an acceptance.
- 2026-10-18 (autonomous): Re-check fixes. A record's N is the config's N at admission. Before, "the N it was admitted
  under" read either as the effective minimum, which refused a retry after a config raise, or as the config's N, which
  contradicted the known limit. The known limit now says an old-N holder pins the count until it releases. The minimum
  counts only this machine's records. `--replace` is refused whenever the claim or a named lease is a resource.
  `list --json` keeps every lease top-level, and gives no effective count on a remote lease. The config warning says to ask the user. The V1-compatibility check no longer expects a V1
  binary to clear a resource record, since NIT-001 clears dead records only during an overlapping claim.
- 2026-10-18 (autonomous): Final re-check fixes, not re-checked since the pass budget ran out. M1 also refuses
  `--replace` between two resource leases. The M2 raise check now admits a holder beyond the old N only after every
  lower-N holder releases. A remote resource lease in `list --json` carries no effective count.
- 2026-10-18: ADR candidate (hard to reverse): the resource name grammar and `domain.kind` `resource`. Names end up in
  stored records and shared agent instructions. Waits on NIT-001 reaching `accepted` with the cross-type overlap rule
  stated.
- 2026-10-18: ADR candidate (hard to reverse): one record per slot holder, with the count in the body and a plain
  `name` footprint. Waits on NIT-001 reaching `accepted`, since it extends NIT-001's own record-format candidate.
- 2026-10-18: ADR candidate (hard to reverse): the `slots.json` config and its minimum admission rule. Waits on
  NIT-001 reaching `accepted`.
- 2026-10-18: Review gate: no judge rulings, provisional decisions, or standing dissent. Nothing reopened. The user kept
  the proposal `designing`, since the Risks mitigation requires NIT-001 to be accepted first.
- 2026-10-18: Accepted. The two NIT-001 rules NIT-002 relies on, cross-type overlap and `list --json` schema
  evolution, have no plausible outcome that changes a NIT-002 decision. Amended the NIT-001 dependency risk. Its
  mitigation said NIT-002 is accepted only after NIT-001. Now NIT-002's queue rows depend on NIT-001 instead.
- 2026-10-18: Amended Dependencies and Risks after NIT-001 was accepted and amended in a27cd26. The Dependencies entry
  said NIT-001 "must also say" that different known types never overlap. NIT-001 now states the pairwise rule: a path
  entry and a name entry never overlap, and each later type defines its pairing. It also lets a minor `list --json`
  version add kind `resource`. The cross-type risk said the rule was undefined, and the envelope risk called NIT-001
  "still in design". The hooks on the three ADR candidates above have fired, since NIT-001 is accepted with the
  cross-type rule. Their outcomes are left for the user to choose.
- 2026-10-20: Scheduled as NIT-002 M1-M3 in queue.md.
- 2026-10-23 (run best guess): Amended "Record and output versions" and "Name syntax". The text said only "a minor" and
  "a length limit". It now says resource records are version 1.1, `list --json` is schema 1.1, and a name is at most 64
  bytes. Nothing stored changes if a later run picks other numbers, since each is one constant. Loosening the length
  limit later breaks no stored name. The same run settled exit statuses without changing the text. A bad name, a second
  `--resource`, `--resource` with path flags, and `--slots` on a path claim exit as usage. `--replace` on a resource
  claim or lease, and a `--slots` that differs from the count that applies, exit as refused.
- 2026-10-23: ADR candidate (departs from the past): another machine's `resource` record never overlaps a claim or
  counts toward a slot, although ADR-04 reads every other-machine record as alive. Later proposals that add kinds must
  say whether they follow it. Undecided. Found in NIT-002 M1.
- 2026-10-23: ADR candidate (cuts across the system): a writer stamps the lowest minor whose fields a record uses, so
  path records, tombstones, and session files stay 1.0 while the binary reads 1.1. Every later minor must follow it.
  Undecided. Found in NIT-002 M1.

## References

- `spec/reports/2026-10-18-lease-broker-research.md`: "Roadmap" and "Reach"
- NIT-001: the claim, liveness, and break rules this proposal reuses
- ADR-02: the record envelope and evolution rules resource records follow
- ADR-03: the key and overlap rule, which a resource name never meets
