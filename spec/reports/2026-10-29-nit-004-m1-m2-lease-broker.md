# NIT-004 M1-M2: lease broker work-queue run

Date: 2026-10-29. Trigger: a `/work-queue` run with no arguments, which means `next 1`. It picked queue row
`NIT-004 M1-M2` from `spec/proposals/queue.md`. NIT-004 is the lease broker proposal
(`spec/proposals/NIT-004-lease-broker.md`). M1 builds the node, the broker, and mirrors of `machine` leases. M2 adds
`user` and `shared` leases. Environment: macOS, run from Claude Code's sandboxed Bash in `/Users/ripta/projects/nitipan`.
The approved plan is `/Users/ripta/.claude/plans/virtual-weaving-cupcake.md`.

## Run state

- Code repo base commit for the run and for row `NIT-004 M1-M2`: `7ad4d68`
- Specs live in the code repo, so the specs commits are the code commits listed below
- The row has no dependencies. No rows are held or blocked. `next` skipped no rows

| Milestone | Run status | Commit |
|-----------|------------|--------|
| NIT-004 M1 | DONE | `f81c349` |
| NIT-004 M2 | DONE | see commit 2 |

Commits, in order:

1. `f81c349` nitipan: add the per-user node, the broker, and machine lease mirrors

## Plan-time decisions

- The user chose the session report directory `spec/reports` for this run. The preference file
  `~/.config/coding_agent_standards/session-report.json` could not be read from the sandbox, so it was left unchanged.
- NIT-004 "Node lifecycle" put the node config in the state directory, which ADR-07 forbids. The user suggested
  `~/.config`. The node reads `~/.config/nitipan/node.json`, and the broker reads `~/.config/nitipan/broker.json`. The
  proposal is amended to match.

## Best guesses from the plan

1. The instance key is the node key later proposals pin. It rides in `Authorization: Bearer` on every request. The
   broker checks it on every `POST`. Rests on NIT-004 "Broker protocol", NIT-007 "Auth", and NIT-006 "Node credential".
2. The seen-label table stores a label derivation version, per ADR-05.
3. The broker store reserves NIT-005's token and counter fields in M2's grant write.
4. The unseen-label address refusal is a broker API with package tests. No CLI exposes addresses until M3.
5. "Discovery shows it as unknown" in M2 is met by `list` and the broker's lease view. M3 owns queries.
6. The broker config lives at `~/.config/nitipan/broker.json`. NIT-004 names no location.
7. The read of NIT-008's `recalls/` is a guarded no-op until NIT-008 exists.
8. Package names, and the handoff directory name `broker/`, follow the one-word style in `internal/`.
9. A renewal of an unknown lease returns "not found", NIT-004's wording. NIT-005 says "lost".

## Check left to the user

Running the printed LaunchAgents under real launchd, with `launchctl bootstrap`. Tests cover everything else.

## NIT-004 M1

### Implementation

A fresh implementer built M1 in about 24 minutes. New packages: `internal/wire`, `internal/monoclock`,
`internal/config`, `internal/broker`, `internal/node`, `internal/handoff`, `internal/tlstest`. It added
`internal/setup/launchd.go` and `internal/cli/node.go`, wired the `node` and `broker` commands, and added a handoff
reaping step to `runRepairs`. `make ci` passed. `proposals.py check` and `freeze` returned `[]`. Each M1 check bullet
has a named test.

The proposal was amended for the config location. "Node lifecycle" and "Broker lifecycle and store" now name
`~/.config/nitipan/node.json` and `~/.config/nitipan/broker.json`. An untagged Decision Log entry dated 2026-10-29
records the change.

Implementer best guesses:

1. On non-register calls, a node is identified by `X-Nitipan-Machine` and `X-Nitipan-Uid` headers. The bearer key is
   checked against the key registered for that pair. The stream GET has no body. If wrong, a header rename.
2. The store holds a SHA-256 of the instance key, not the key itself, as NIT-006 "Node credential" does.
3. `DefaultKeyPolicy` is the one swap point for key rules. A new node is accepted. The same key is accepted. A
   different key is refused while the old stream is live, and accepted after a term with no heartbeat.
4. Stream liveness and mirror staleness share one rule: a heartbeat, snapshot, register, or stream open within one
   term.
5. Event IDs are `<boot>.<seq>`, with a random boot part per broker run. The node keeps a 1024-entry dedupe window.
   The event queue is not persisted.
6. The wire carries `term_ms` and `keepalive_ms`. The node heartbeats at a third of the term. It cuts a stream that
   is silent for three keepalives.
7. The order is register, then the stream, then the snapshot. Each heartbeat tick sends a heartbeat and then a
   snapshot.
8. A `ca_file` replaces the system roots rather than adding to them.
9. The previous store file is `<store>.v<major>`. M1 has no real major migration, so the test injects one.
10. The default store is `~/Library/Application Support/nitipan/broker.json`, and its lock is `<store>.lock`. Off
    macOS, the volume check always refuses.
11. Presence is `broker/presence.json`. The node lock is `node.lock` and the key is `node-key`, all in the state
    directory. Presence lists the request kinds the node knows, which gives the CLI "node too old".
12. M1's only request kind is `ping`, forwarded to an authed `POST /v1/ping`. It exercises the handoff, expiry, and
    "node too old" paths end to end. M2 adds the real kinds.
13. "Node not running" exits with status 1. No new exit code was added.
14. Mirrored record bytes are compacted JSON. They are equal in meaning to the file on disk, not byte-identical.
15. `broker` is a visible command and `node` is hidden.

### Review

The reviewer could not run `make test`. Its sandbox denies local port binding, so `httptest` panics. The
implementer's `make ci` ran through `cg_run`. The `adr-impact` pass found no conflicts with any ADR.

Findings and dispositions:

1. Plain HTTP to any host name other than `localhost` was refused, which is stricter than "refuses plain HTTP to a
   public address". Sent to fix: check the dialed IP at connect time.
2. The synced-folder refusal matched only iCloud. Sent to fix: add `~/Library/CloudStorage` and the legacy Dropbox,
   Google Drive, and OneDrive folders.
3. The amendment marker in "Node lifecycle" sat on the plist bullet, not on the changed config bullet. Sent to fix.
4. Comment craft: a garbled `OnEvent` doc, a comment that referred to future auth, and empty group labels. Sent to fix.
5. Non-stream calls have no deadline, so a stalled broker could block the session loop. PLAUSIBLE. Sent to trace.
6. A key replacement does not cancel the old, non-live stream. PLAUSIBLE. Sent to trace.
7. Heartbeat and snapshot POSTs with no stream attached keep a node fresh. PLAUSIBLE. Sent to trace.
8. A higher-minor store makes the broker exit on its first write. PLAUSIBLE. Sent to trace, suggesting a refusal at
   start.
9. The previous session's stream goroutine is not awaited before the next session. PLAUSIBLE. Sent to trace.
10. Undecodable claimed or answered handoff files are never reaped. PLAUSIBLE. Sent to trace.
11. An unauthenticated `register` allows store growth and, after a quiet term, identity takeover. Dismissed: the Risks
    entry "The broker is open before NIT-006" accepts it.

Fix round results, from the same implementer:

1. Fixed. The node refuses only a literal public IP at start. For plain HTTP, a dialer `Control` hook refuses a
   connection to a public IP after resolution, so `broker.lan` works and rebinding is covered. With a proxy, it judges
   the proxy's address. No amendment was needed. The As built block records the dial-time check.
2. Fixed. The store also refuses `~/Library/CloudStorage`, and the home-level Dropbox, Google Drive, OneDrive, iCloud
   Drive, and Box folders, after resolving symlinks. Matching is by path only. A sync tool on an arbitrary folder, and
   iCloud's Desktop and Documents sync, are not detected. The As built block says so.
3. Fixed. The 2026-10-29 marker is on the config bullet, and the plist bullet has its 2026-10-24 marker back.
4. Fixed.
5. Confirmed with a stalling fake broker. Every non-stream call now has a 30-second deadline, and the transport has a
   `ResponseHeaderTimeout`.
6. Confirmed. A key replacement cancels the old stream.
7. Confirmed. Heartbeat and snapshot count as beats only while a stream is attached.
8. Confirmed. A higher-minor store is now refused at start, naming both versions.
9. Confirmed. Five handlers ran at once before the fix. `session` now waits for its stream goroutine.
10. Confirmed. `Reap` removes an undecodable file once it is older than the one-day window.

Each trace's regression test failed on the unfixed code before its fix. After the round, `make fmt` and `make ci`
passed, and so did `-race -count=5` on `internal/node` and `internal/broker`. `proposals.py check` returned `[]`.

ADR candidate from `adr-impact`: the node-broker channel never carries plaintext over a public path. It passes all
three tests. Architecture: it cuts across the system, since NIT-005, NIT-006, and NIT-007 traffic rides this channel.
Decision: TLS or a loopback or private address, over plaintext anywhere. Record: "Broker protocol" settles it, and
accepted NIT-006 builds on it. It is recorded as Undecided in NIT-004's Decision Log.

## NIT-004 M2

### Implementation

A fresh implementer built M2 in about 50 minutes.

- New packages: `internal/repoid` holds the `repo:` names and their overlap. `internal/rootset` derives root sets.
- New files: `internal/broker/grants.go`, `internal/handoff/held.go`, `internal/node/lease.go`,
  `internal/node/forward.go`, `internal/cli/wider.go`, and `internal/wire/lease.go`.
- Changed: `acquire`, `release`, `break`, and `list`. A new `renew` command was added.
- `make ci` passed. `proposals.py freeze --row-commit f81c349` returned `[]`.
- `proposals.py check` reported only that the finished row must move to `history.md`.

The implementer amended "Machine leases and user leases on the same path". A late refusal now becomes a notice that
names the release command. The text had said the next call that receives the late refusal releases the anchor. The
Decision Log entry is marked as a run best guess.

Implementer best guesses:

1. One ULID serves as both the request ID and the lease ID. A timed-out command leaves `broker/inflight/<key>.json`, and
   a retry reuses its ID.
2. `RequestVersion` is 2. An M2 CLI gets "node too old" from an M1 node.
3. The broker store stays at 1.0 and uses M1's reserved fields. The token is the counter value at grant time, and
   revival keeps it. NIT-005 M2 may change these semantics.
4. The snapshot answer carries the node's leases and end facts. Acks ride in `Snapshot.Acked`. The answer is 204 when
   there are none, which keeps M1 clients working.
5. Overlapping leases from the same holder do not conflict at the broker. Otherwise a second `user` lease could not
   share an anchor.
6. An exact repeat of a name by the same holder returns the existing lease.
7. Deadline files are `broker/deadlines/<holder-key>.json`, and each carries a connected flag.
8. `renew` is a subcommand over a `renew` request kind.
9. `list` asks the node. With no live node, it falls back to the holder's sidecars with state `unknown`. JSON gains
   `broker_leases`, and the listing schema goes to 1.4.
10. A remote `break` goes to the broker when the ID matches no local lease, tombstone, or mark file.
11. No new exit codes. "taken", refused, and ended exit 3. "not found" exits 8. Node and broker version errors exit 1.
12. A gap of more than 5 seconds between node loop ticks, on the monotonic clock, counts as a wake.
13. The broker removes a lapsed lease whose anchor the node's snapshot omits.
14. A claim with several covering leases from the same holder is refused, since only one can be the anchor.
15. The root-set cache is `broker/rootsets.json`. A claim inside a submodule emits the submodule's entry first.
16. Releasing a typed `repo:` name releases the caller's own live or lapsed leases that overlap it.

Gaps the implementer named: the "restored" notice has no test, and no test is dedicated to an old node against a new
broker. A node restart within the 90-second reply window drops the late-reply notice, though a retry still returns
the same answer.

### Review

The reviewer's sandbox denied local port binding, so it ran only the rootset, repoid, and handoff tests. They passed.

Reviewer findings and dispositions:

1. A release of another session's lease ID deleted that session's sidecar on `not_found`, so its lease stopped
   renewing. CONFIRMED. Sent to fix.
2. An exact repeat claim wrote a ghost sidecar under the new request ID. The holder then got a false loss notice.
   CONFIRMED. Sent to fix.
3. A hard round-trip failure, such as "broker too old" or a withdrawn request, leaked an anchor the claim had created.
   CONFIRMED. Sent to fix.
4. Dropping "or the next call that receives the late refusal" releases the anchor was a shortcut the plan did not
   approve. Sent to fix the way the proposal says, reverting the amendment. A retried release after a lost reply also
   skipped the created-anchor release. That went with it.
5. The same-holder rule appeared only in the Decision Log, not in the settled text. Sent to fix by an amendment in
   place.
6. Only some skew directions had tests: there was no old-node test, and the old-CLI test used only ping. The
   "restored" notice was nearly unreachable. Sent to fix.
7. Request outcomes were keyed by request ID with no node check, so another node could replay a "granted" outcome.
   PLAUSIBLE. Sent to trace.
8. The root-set cache was written non-atomically. Sent to fix.
9. Comment craft: a "mirror of" comment, a doc comment bound to the wrong name, and a no-op assignment. Sent to fix.

Fix round results, from the same implementer:

1. Fixed. A release removes a sidecar only when its holder key matches the requester's. A regression test failed
   first.
2. Fixed. The sidecar is keyed on the returned lease ID, and an existing sidecar keeps its created flag. Node and CLI
   regression tests failed first.
3. Fixed. A created anchor is released on every definite non-grant, and kept only when the outcome is unknown.
4. Fixed, and the amendment reverted. The node writes `broker/orphans/<claimID>.json` beside the late-refusal notice.
   The orphan holds the anchor's ID, keys, and created flag. The next command that delivers the notice releases the
   anchor under the lock, by the created-anchor rules. A release keeps its sidecars in the in-flight record, so a
   retry after a lost reply still releases a created anchor. Both regression tests failed first. Run best guess: an
   orphan file rather than a new `record.Notice` field, since a field needs a record minor bump.
5. Fixed. "Which user a user-reach lease belongs to" and M2's check now say one holder never conflicts with its own
   broker leases. Each has one marker. A single Decision Log entry, marked as a run best guess, gives the prior text.
6. Fixed. An old-node snapshot with no `Acked` field works. A v1 request for a kind added in v2 gets "node too old".
   The "restored" notice now fires once, after the node's own deadline passes with no acked renewal. Each regression
   test failed first.
7. Confirmed and fixed. A stored outcome carries the node that made it, so another node's replay gets 409. An outcome
   stored with no node still replays, which keeps old stores working.
8. Fixed. The root-set cache is written through a temp file and rename. No test can fail first, since a torn write
   cannot be reproduced. The test pins only that no temp file is left.
9. Fixed.
10. ADR-05 ceiling, fixed. The broker refuses a derivation above `MaxDerivation`, which is 1, as "broker too old". A
    regression test failed first.

After the round, `make fmt` and `make ci` passed. `proposals.py freeze --row-commit f81c349` returned `[]`.
`proposals.py check` reported only the row move. The `internal/cli` tests now take about 340 seconds under `-race`.

`adr-impact` findings and dispositions:

1. ADR-05: the broker refused a derivation below its floor but accepted an unknown higher one. Treated as a fix: ADR-05
   says a reader refuses a version it does not know.
2. ADR-03: repo-name overlap ignores the checkout-boundary exemption. Dismissed as a conflict. A `repo` entry is its
   own type and never overlaps a `path` entry, and "Name syntax for wider reach" settles that it covers every
   worktree. The invariant behind it is recorded as an ADR candidate below.
3. Token equals the counter, and a holder never conflicts with itself. Not recorded as candidates. The token meaning
   is NIT-005 M2's to settle, and the same-holder rule is a run best guess the user has not confirmed.

ADR candidate: at `user` and `shared` reach, a lease names a repo's files across every checkout and worktree,
unlike the checkout-boundary exemption at `machine` reach. Architecture: it departs from ADR-03's pattern, and
NIT-005, NIT-006, and NIT-007 inherit it. Decision: repo-wide coverage, over per-checkout coverage. Record: "Name
syntax for wider reach" is settled and accepted. It is recorded as Undecided in NIT-004's Decision Log.
