# NIT-007 M1: mailbox and Claude Code delivery work-queue run

Date: 2026-11-08. Trigger: a `/work-queue` run with no arguments, which means `next 1`. It picked queue row
`NIT-007 M1` from `spec/proposals/queue.md`. NIT-007 is the session mailbox proposal
(`spec/proposals/NIT-007-session-mailbox.md`). M1 carries mail between one user's sessions across machines, and wakes
Claude Code sessions for it. Environment: macOS, run from Claude Code's sandboxed Bash in `/Users/ripta/projects/nitipan`.
The approved plan is `/Users/ripta/.claude/plans/iterative-baking-steele.md`.

## Run state

- Code repo base commit for the run and for row `NIT-007 M1`: `spec: mark NIT-004 implemented and move its last queue
  row to history`
- Specs live in the code repo, so the specs commits are the code commits listed below
- The row depends on NIT-004 M3, which is `DONE`. No rows are held or blocked. `next` skipped no rows

| Milestone | Run status | Commit |
|-----------|------------|--------|
| NIT-007 M1 | DONE | `nitipan: carry mail between one user's sessions across machines` |

## Best guesses from the plan

- Approve, forget, and pending reach the running broker through a local admin unix socket with a peer-UID check. No
  network path reaches them. The store's flock rules out a second process editing the file. Rests on ADR-08
  "Decision" and "Consequences"
- The ring ledger is built in M1 as a state-directory file in NIT-008's "Telling the sub-lessee" format. M1's own
  checks need it shared: the waiter runs with no node, and each message wakes at most once. NIT-008 M3 adds notice and
  request keys. Rests on `NIT-007-session-mailbox.md:248-256` and `NIT-008-sub-lease-recall.md:208-213`
- The mail name for Codex and opencode uses the NIT-001 identity string unchanged, so it equals the handle. The domain
  separator and derivation version apply to the Claude Code derivation. Rests on `NIT-007-session-mailbox.md:144-147`
- A sender must be a mailable identified holder, so every bounce has a mailbox to land in. Rests on "Delivery
  guarantees"
- Labels seen before mail shipped stay unpinned. They keep NIT-004's live-stream key rule until approved. Rests on
  `NIT-007-session-mailbox.md:199-200`
- Mail capability travels as a numeric field in the node's registration, persisted per node. The broker can then answer
  "recipient node too old" while the node is offline. Rests on NIT-004 "Version skew"
- The body cap is 64 KiB. The proposal fixes a cap without a number
- The waiter hook timeout is 86400 s. The proposal says only "long"
- The waiter reads `crossSessionInbound` from every Claude Code settings scope it can read: user, project, local, and
  managed. Rests on `NIT-007-session-mailbox.md:282`
- Stored mail lives under a new `mail/` state subdirectory beside `broker/`, which V1 binaries never scan. Rests on
  `NIT-007-session-mailbox.md:97-98`

## Left for the user

- A live check that a real idle Claude Code session wakes through the waiter, and through the socket ring with no
  waiter. Tests drive the hook contract and a fake inbox socket. The repo has no live Claude Code harness
- The Risk "The work environment blocks every Claude Code wake path" asks for a check of the work org's managed
  settings for `crossSessionInbound` before M1. Only the user can run it
- NIT-004 was marked `implemented` while the NIT-004 M3 run's ADR-07 block has no recorded ruling. See
  `2026-10-30-nit-004-m3-discovery-queries.md` "Block: ADR-07". This run does not touch it
- Two settled behaviors changed by run best guesses, each amended with a marker. "Recipient node too old" now refuses
  the send rather than keeping the message. An approval by a fingerprint no pending key has now waits for that key
  rather than erroring, so a LAN host cannot block approval
- NIT-006's text says a kept previous broker store holds NIT-007's keys in the clear. The broker stores only key hashes,
  so that text is stale. NIT-006 is not this row's work

## NIT-007 M1

### Implementation

One implementer built the milestone in eight slices, each with its tests. It set M1 to `DONE` with an As built block.
That block maps every M1 check to the tests that prove it, and names the two checks left to the user.

- The broker store is now 1.1. It holds label pins, every session ID each label reported, queued mail, and each
  node's last mail capability and HTTPS flag. A 1.0 store is stamped 1.1 on load, so an older broker then refuses it
- A pinned label refuses any other key, for leases as well as mail, whether or not its stream is live. `nitipan broker
  pending`, `approve`, and `forget` reach the broker only through a unix socket beside the store. That socket checks
  its directory mode and the peer UID. `nitipan node fingerprint` prints the node's key fingerprint. Approving a key
  closes any stream opened under another key
- New wire event kinds for mail, stage, and bounce, and new send, ack, report, and status calls. `Register` carries a
  numeric mail capability. The handoff request version is 4. Bodies over 64 KiB are refused at send
- The node stores each message under `mail/` in the state directory and syncs the directory before it acks. An
  accepted-ID ledger and read markers drop resends. Plain HTTP refuses mail with a reason
- Registrations are versioned and keep every session ID the process held. The broker alone derives mail names
  (`internal/broker/mailname.go`). Discovery, reply addresses, and `mail status` print `<mail name>@<label>`
- A shared ring ledger in `internal/mailbox/ring.go` gives each message at most one wake. `nitipan hook mail-wait` is
  the `asyncRewake` waiter. The node's socket ring checks the kernel peer PID and UID through a new
  `internal/peercred` package before it writes
- `nitipan mail send`, `mail read`, and `mail status`. `setup claude` prints the SessionStart, Stop,
  UserPromptSubmit, and PostToolUse hooks
- Every bounce reason in "Delivery guarantees" is implemented. Expiry applies only to mail the broker never sent

Claude Code facts the implementer checked against code.claude.com on 2026-11-08:

- `asyncRewake` "runs in the background and wakes Claude on exit code 2". No hook timeout maximum is documented
- `crossSessionInbound` is read from user, shared project, project local, managed, and `--settings` scopes, ordered
  accept < hold < refuse
- `CLAUDE_CODE_MESSAGING_SOCKET` is documented from v2.1.224
- The waiter cannot see MDM, server-managed, or `--settings` values. A refusal set only there stops the socket ring
  inside Claude Code but leaves the waiter armed. The As built block records this. The Linux managed-settings path
  `/etc/claude-code` came from memory, not the docs

Checks: `make ci` passed three times on the implementer's tree. The last run served every package from the Go test
cache. `proposals.py check` reported only the row-finish finding. `proposals.py freeze` returned `[]`.

Best guesses and deviations the implementer added, each in a Decision Log entry marked `(autonomous)`:

- NIT-007's Impacts line for NIT-008 said "NIT-008 M3 builds this". It now says M1 builds the ledger and NIT-008 M3
  adds notice and request keys. NIT-008 was amended in place to match
- NIT-008 said the ledger "carries NIT-001's envelope". It now says a `major.minor` version and the registration's
  identity, since the envelope's footprint and liveness fields mean nothing for a ledger. If wrong, the writer in
  `internal/mailbox/ring.go` changes
- "Recipient node too old" refuses the send, and bounces already-queued mail when a node later registers without mail
  capability. The settled text said the broker keeps the message. If wrong, `checkRecipientLocked` changes
- A `mail send` call does not print the non-waking ring. If wrong, `quietRing` is dropped
- Only Claude Code sessions register for mail in M1. Other agents get a "not supported yet" error until M2
- `Forget` resets the sent flag on mail still queued, so the next approved key gets it
- The Makefile gained `TEST_PKGS` and `TEST_ARGS` overrides on `make test`, so one package can run without calling
  `go` directly

Failures the work did not cause:

- `TestDeadHolderEndsItsLeasesAtOnce` in `internal/node` had a data race on the fake process table. It failed 1 of 3
  runs on an archive of the base commit, and at once under `-count=40`. The new mail code reads the table more often,
  so it failed 4 of 5 runs on the new tree. Fixed: `proctest.Table` now locks and gains `Remove`. The node package then
  passed `-count=3` and the full `make ci` passed
- `TestWhoHoldsReadsAV1RecordAndAnUnreadableSidecarAndAQuietNode` in `internal/cli` timed out in 3 of 6 full-package
  runs on the new tree. It passed alone 3 times. The base passed 25 of 25 under light load. The implementer's
  diagnosis is that the node's derivation rewrites the sidecar the test planted, so a slow derivation under load
  outlasts the 5 s wait. That diagnosis is unresolved. Whether the base fails the same way under the same load is
  not shown

Test cost, wall time per package from the latest uncached runs: `internal/cli` 296.8 s, `internal/node` 29.2 s,
`internal/broker` 22.7 s, `internal/mailbox` 1.27 s, `internal/handoff` 1.27 s, `internal/state` 1.5 s,
`internal/adapter` 1.0 s, `internal/liveness` 1.0 s. `internal/broker` and `internal/node` were not re-timed after the
final edits.

### Review

A reviewer and an ADR-impact agent ran on the working-tree diff. `proposals.py freeze` returned `[]`, and NIT-004 was
untouched. Findings and their triage:

1. CONFIRMED: `TestWhoHoldsReadsAV1RecordAndAnUnreadableSidecarAndAQuietNode` passes 30 of 30 on a worktree of the
   base commit and fails about 1 in 10 on the new tree. The failure reads "timed out waiting for the unreadable
   sidecar to read as unknown". The diff caused it. This replaces the implementer's unresolved diagnosis above. Fix
2. Plan: `mail send` suppressed the non-waking ring, against M1's "every identified-holder nitipan call ... ring".
   Fix: `send` rings, while `read` and `status` stay quiet since they show the mail
3. Proposal: only Claude Code sessions can send or register in M1, which narrows M1's "a session sends". Fix: record
   the narrowing in As built and the Decision Log
4. No finding: the "recipient node too old" amendment is well formed, and it matches M1's own check. It still changes a
   settled behavior without the user choosing it, so the final report surfaces it
5. PLAUSIBLE, security: `ServeAdmin` checks the admin directory's mode but not its owner. It removes whatever sits at
   `admin.sock` without checking that it is a socket. A long store path can pass the unix socket path limit with an
   opaque error. Trace
6. PLAUSIBLE, security: the pending-key set is capped at 8 and evicts the oldest. A flood of fresh keys might evict a
   real node's key before approval. Trace
7. PLAUSIBLE, ADR-04: four node paths drop unusable registrations, so a session whose only registration is unusable
   reads as dead. Its stored mail would then bounce after 7 days. Trace
8. PLAUSIBLE, ADR-04: the ring ledger, the accepted ledger, and presence files are rewritten after an unusable or
   higher-minor read. Trace

ADR impact found ADR-01, ADR-05, ADR-06, ADR-07, and ADR-08 consistent. ADR-03 is not touched. The ADR-07 note: the
socket ring's peer check compares the PID from the registration, with no process-start comparison. The node still only
sends one fixed ring line, so this is not a conflict. No uncovered decision clears the ADR bar. One weak candidate is
how ADR-07's "belongs to the session it claims to" applies to a node dialing a session socket. It fails the
Decision test as a new ADR, since it would only restate ADR-07 with an example. It is recorded here, not in the
Decision Log.

Outcomes, from the same implementer:

1. Fixed. `derivedFor` in `internal/node/derive.go` re-checks the derived file under the lock after its slow
   derivation. Any file it could not read fell through to `WriteDerived`. A higher-major file planted mid-derivation
   was overwritten, so the lease read as derived instead of unknown. That is NIT-004 code, so the bug predates this
   run. Now an unreadable file that appeared meanwhile is left alone. `TestADerivationNeverRewritesASidecarItCannotReadThatAppearedWhileItRan`
   forces the interleaving. It failed without the fix and passes with it. The CLI test failed within `-count=30`
   before and passed `-count=60` after. Unresolved: the reviewer saw 30 of 30 pass on the base, and why the new tree
   hits the window more often is not shown. Removing the new registrations watch did not change the outcome over 40
   runs
2. Fixed. `mail send` rings. `mail read` and `mail status` stay quiet, recorded in As built and a Decision Log entry
3. Fixed. As built and an `(autonomous)` Decision Log entry record that only Claude Code sessions send or register
   until M2
4. Left for the user. See "Left for the user"
5. Confirmed and fixed. The admin path check now verifies the directory owner, removes only a stale socket, and refuses
   a path over the platform's socket path limit with an error naming both. `TestAdminPathChecksOwnerFileTypeAndLength`
6. Confirmed and fixed, with an amendment to "Auth". With the real machine asleep, a host could evict the real key
   from the pending list, or hold a live stream so the real key never became pending. Now a well-formed fingerprint
   that no pending key has is recorded, and the label pins the first key that presents it. A host cannot present a
   matching key without the user's own key. A mistyped fingerprint now waits instead of erroring. `broker pending`
   shows it as approved in advance. `TestAFloodOfFreshKeysCannotEvictTheRealKeyFromAnApprovalItNeverGets`
7. Confirmed and fixed. An unusable registration now keeps its session's mail from expiring. The failing-first test is
   `TestAnUnusableRegistrationMakesItsSessionUnknownSoItsMailIsNeverExpired`. Sessions an unusable registration names
   are left out of the node's snapshot, recorded in As built
8. Confirmed by reading and fixed. The ring ledger, accepted entries, waiter presence, and sent notes are never
   rewritten when unusable or of a higher minor. An unusable ring ledger still allows a ring, at most once per 30 s per
   registration. An accepted entry of any version still dedupes a resend. The new tests use a new symbol, so they were
   not run on the unfixed code. They pin the fix, not the bug

Checks: `make ci TEST_ARGS=-count=1` passed uncached, with 0 lint issues. `proposals.py check` reported only the
row-finish finding, and `freeze` returned `[]`. Test cost from that run: `internal/cli` 300.6 s, `internal/node`
31.6 s, `internal/broker` 8.7 s, `internal/setup` 10.8 s, `internal/gitfence` 10.2 s, `internal/state` 2.1 s,
`internal/mailbox` 1.4 s, `internal/handoff` 1.3 s, `internal/adapter` 1.0 s.
