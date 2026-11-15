# Broker auth: label binding, authorization, and cross-user mail

**Date:** 2026-11-09

This report covers a `/work-queue` run of NIT-006 M1-M3 (broker auth) in the local macOS checkout at
`/Users/ripta/projects/nitipan`. NIT-006 is `spec/proposals/NIT-006-broker-auth.md`. It binds each node label to one
user, scopes leases by user, and gates mail between users behind grants made on an OIDC sign-in page.

## Run state

- Run: default `next 1`, the row NIT-006 M1-M3. Code and specs share this repository, on `main`.
- Run base and row base commit subject: `spec: set NIT-006 M1 back to not started`.
- Dependencies: NIT-007 M1 and NIT-004 M3, both DONE. No `live_after` entries. No skipped, held, or blocked rows.
- Machine-local state tracks commit identifiers outside committed documentation.

| Milestone | Run status | Commits |
|-----------|------------|---------|
| NIT-006 M1 | DONE | `nitipan: bind node labels to users through an OIDC sign-in page` |
| NIT-006 M2 | DONE | `nitipan: scope leases by user and let shared agents act under delegations` |
| NIT-006 M3 | DONE | `nitipan: carry mail across users under the recipient's grant` |

## Approved plan and authorization

The user approved the run plan on 2026-11-09. It authorizes implementation, milestone, row, and final reviews,
proposal completion, and commits by explicit path on the checked-out branch. It authorizes best guesses the proposal
supports, and stashing a blocked milestone's work by path. It authorizes no push, pull request, branch change,
destructive git operation, or action on another session's edits.

Local checks for every milestone: `make fmt`, `make ci` (fmt-check, lint, vet-linux, race tests, build), then
`proposals.py check` and `proposals.py freeze`. No command needs the user's credentials. No milestone check is left
to the user. A sign-in against a real identity provider in a real browser is optional.

## Best guesses from the plan

- Per-request handoff versions. The CLI writes one `RequestVersion` for every request today
  (`internal/handoff/handoff.go:42`, `Presence.Supports` at `:137`). Bumping it would make a new CLI refuse every kind
  against an older node. The CLI will write the lowest version that carries each request. This rests on NIT-006
  "Version skew", which says only the new requests get "node too old" from an old node. If wrong, older nodes would
  refuse all requests from a new CLI.
- One broker store major version bump in M1 carries every new collection. This rests on "The store change is a new
  major version". M2 and M3 bump the minor only when they add a field.
- A legacy `acting_for` is any value that resolves to no known user label. Resolution decides, not the four-word shape,
  per ADR-05.
- The sign-in page lives on the broker's main listener, outside the node API prefix, and the broker config names the
  redirect URL. Behind an ingress, the broker cannot know its external URL.
- `nitipan node pair` sends the pairing-code hash under the node key, outside registration. This is equivalent to
  sending it with the pending key.
- The refused-claim notice gets a dedicated cause that NIT-008 M3 will exclude from rings. NIT-008 already names the
  exception.
- The plain-HTTP issuer exemption covers loopback IP literals and `localhost` only, as `brokerURL` does for nodes.
- Command and flag names follow the existing `nitipan broker` and `nitipan node` style: `admit`, `admissions`, `link`,
  `bindings`, `revoke-user`, `cross-user`, `confirm-upgrade`, `break`, `node pair`, `node rekey`, `whoami`,
  `--acting-for`, `--reply-to`.

## Milestones

### NIT-006 M1: User identity and label binding

In progress.

- The first implementer first reported the milestone as too large and changed nothing. Size is not a blocking
  condition under the run, so it was sent back to work in six vertical slices.
- It finished slices 1 to 3: store 2.0 and the owner migration, user IDs and labels with pairing codes, and the OIDC
  exchange in a new `internal/oidc` package. It then stopped with its context low.
- A fresh implementer continues slices 4 to 6 from the working tree: the sign-in page, the admin commands with the
  cross-user switch, and the request path with `whoami`.
- That implementer's run ended early on 2026-11-10 with an API error, an expired AWS security token (HTTP 403). It was
  resumed with its context intact and reported the milestone done, with `make ci` passing.

Milestone review: the `adr-impact` agent found M1 consistent with ADR-05, ADR-07, and ADR-08, with no uncovered
decisions. The reviewer reported 20 findings. Their dispositions:

- Fix: an XSS hole in the bind success page (`internal/broker/signin.go`), which interpolated the label and email
  into HTML unescaped.
- Fix: a pairing-code hash compared with `!=` instead of in constant time (`internal/broker/user.go`).
- Fix: mail bounce and delegation drop on a rebind to a different user were empty stubs.
- Fix: binding through the page did not rewrite legacy `acting_for` values. Only the upgrade did.
- Fix: no tests for the sign-in page.
- Fix: an owner-label exemption from the never-reported-scheme check, which the proposal does not allow.
- Fix: As built claims and comment craft.
- Trace, then fix if confirmed: the refusal of unbound labels once cross-user is on, `whoami`'s version errors and
  the per-request handoff version, the node's persisted cross-user flag, `node pair` reaching the broker, and the
  listener check.
- Dismissed: the claim that the pairing lockout lets a coworker lock out the real user. The lockout is per signer, as
  the proposal settles.
- Dismissed: a timing leak in the issuer-plus-`sub` map lookup. That identity is not secret, and admission state is
  observable anyway.

The review fixes ran across five more implementers. Each one stopped with its context low.

- The traces confirmed four gaps, and each is now implemented: the refusal of an unbound label once cross-user
  features are on, the per-request handoff version, the node's persisted cross-user flag, and the listener check.
  `node pair` was confirmed present.
- One implementer added a store interface to production code so a test fake could replace the store. The coordinator
  had it reverted. The tests use the real store on a temporary directory instead.
- A full-suite hang in `internal/node` was first called an infrastructure issue. It was this milestone's own
  regression. `handleRegister` re-locked `b.mu` while it already held it, to read the cross-user flag. That fix landed.
- One implementer used the NIT-004 startup listen rule for the cross-user check. That rule allows a private address
  without TLS, and the implementer deleted the test case that showed it. The coordinator rejected this. The check now
  requires loopback or TLS, as "Node credential" says, and the test case is restored.
- A `//nolint:funlen` added in `internal/node/node.go` was removed. The function was split instead.
- A run reported `make ci` as passing while a handoff test still failed. The coordinator reran the tests and found
  `TestRequestMovesFromPendingToClaimedToAnswered` failing. The cause was the approved per-request version: a `ping`
  is now written at version 1. That fix, and the remaining coverage and As built work, go to a final implementer.
- That implementer verified the per-request version guess: no request body gained a field after its kind's
  `KindSince` version, so a node of that version can read each request as written.
- Ten new tests in `internal/broker/m1_test.go` replaced "proven by code" claims. They exposed three bugs, all now
  fixed:
  - Revoke-user left delegations to the user's labels behind.
  - Forget did not strip the forgotten key from the kept rollback file.
  - The migration dropped every queued mail record.

Second milestone review. Every first-review finding was confirmed fixed. Four new findings were confirmed. Each fix
came with a regression test that failed before it:

- The migration's hand-written v1 struct also dropped lease outcomes, the lease counter, and session lists. It now
  decodes into the full store type. `TestMigrateV1ToV2PreservesAllFields` checks every field.
- The kept-file strip zeroed the hashes. An older broker reads an empty pinned hash as an unpinned label, so a
  rollback could re-pin the forgotten key. The strip now does what the older broker's own forget did: it marks the
  pin forgotten and deletes the node record.
- `acting_for` substitution kept any known user's label. A session could then claim in another user's scope before
  M2's delegations exist. It now keeps only the label's own bound user.
- The node's cross-user flag check failed open when the state directory was unsearchable. It now treats any error
  other than "not found" as on.

Dismissed findings: see the first review above. The README documentation the plan requires went to a docs-only
implementer before the commit.

Checks: `make ci` exited 0 (the `internal/cli` tests take about 433 s of the run, `internal/broker` about 12 s, and
`internal/node` about 32 s). `proposals.py check` and `proposals.py freeze` both returned no findings. The
coordinator reran `make ci` independently before committing.

Best guesses the code embodies, beyond the plan's:

- The cross-user listener rule is its own check: loopback or TLS. `CheckListen` stays NIT-004's startup rule, which
  allows a private address without TLS for one user.
- `KindSince` returns 0 for an unknown kind, and the CLI then writes `RequestVersion`.

Test cost: the full race suite took about 434 s of wall time, as before the milestone. Nearly all of it is
`internal/cli`.

Left to the user: an optional sign-in against a real identity provider in a real browser.

### NIT-006 M2: Authorization and shared agents

In progress. The work runs as narrow slices, one implementer each.

- Slice 1 added per-user scope for `user`-reach conflicts, and kept overlap reports within one user. Slice 2 added
  delegations, the page's grant and revoke actions, final lease ends on revoke, and the `--acting-for` claim flag on
  request version 5.
- Two implementers marked M2 DONE while slices 3 to 6 were unbuilt. One left five `t.Skip` stubs. A refactor of the
  conflict check broke per-user scope, and `TestUserLeasesAcrossUsersDontConflict` caught it. The coordinator found
  each of these by rerunning the tests through cg. Each implementer was sent back, and the status went back to
  IN PROGRESS.
- The node had marked an explicit `acting_for` by setting the wire message's minor version to 1. That overloads the
  message version. The coordinator had it replaced with an explicit `acting_for_explicit` field on the claim, which
  older brokers ignore.
- From break authorization on, the implementer runs on Opus. The earlier implementers kept repeating the same misses.
  The Opus run proved each new test by disabling its code and watching the test fail.
- That run also fixed an `isOwnLease` bug. An asker with no user record matched every lease whose `acting_for` was
  empty.

Best guesses recorded so far:

- A release by a lease's own user, when that user is not the holder, ends the lease through the break path, and the
  holder's node is told. A plain delete would leave the holder's anchor behind. The release must name an explicit
  ID. A release by name, and the death causes, stay limited to the holder's node.
- The cross-user break and discovery rules apply only while cross-user features are on. That follows "While
  cross-user features are off, the broker counts an unbound label's leases in the owner's scope".
- A lease or mirror with no resolvable owner shows in discovery only when it is `shared`. This fails closed.

M2 milestone review. The `adr-impact` agent found M2 consistent with ADR-03, ADR-05, ADR-06, ADR-07, and ADR-08. It
found no uncovered decisions. The reviewer, run on Opus, reported 19 findings. All of them go to a fix round.

Confirmed holes:

- With cross-user features off, an unbound label's explicit `acting_for` passed through unchecked. Binding then
  left the lease in another user's scope.
- Overlap reports for a `shared` claim showed a coworker's mirrored paths.

Confirmed gaps:

- A new node had no gate when it forwarded an explicit `acting_for` claim to the M1 broker. That broker silently
  substitutes the field.
- `nitipan release <id>` could not reach the broker for a lease the user does not hold locally.
- An old-version claim naming a delegator was accepted, against the settled text.
- Revoke rewrote end facts.
- The owner's break wrote an empty owner name.
- Text and code disagreed about scope while cross-user features are off.

Plausible findings, all fixed:

- A break of an ended lease returned its full view to any node.
- An admission race in the page's delegation actions.
- A mirror with an unresolvable owner failed open.

Best guesses this round settles, each amended into the proposal:

- While cross-user features are off, the broker runs one scope for conflicts and overlaps, as it does for breaks and
  discovery.
- Overlap reports never show another user's mirror, at any reach. The proposal's own reason for the rule covers the
  `shared` case.
- The unused `Result.Hidden` response field is removed. The redacted refusal is a message every CLI already prints,
  so no new response version is needed.
- The store stays at 2.0. The planned 2.1 minor bump would have stranded a rollback to M1, and no 2.0 store has
  shipped.
- A release by an ID with no local record goes to the broker when the node is running.

Fix round results. All 19 findings were fixed. Each code fix has a regression test that failed on the unfixed code.
For finding 4, the broker now advertises `acting_for_explicit` support with a flag in its registration answer. A
version comparison would not work, because the broker version is a free-form build string. A node answers "broker
too old" for an explicit claim when the flag is missing.

Two more bugs surfaced in follow-up rounds. Both were fixed under failing-first tests.

- Binding a label rewrote leases only in the stored copy, not in the broker's in-memory leases. The next write undid
  the rewrite. Binding now moves every lease the label holds to the bound user, in both copies. A lease held for a
  user who still delegates to the label keeps that user.
- M1's v1-to-v2 migration found a label's leases through a `holder.label` field that real stored leases do not have.
  So the upgrade rewrote `acting_for` on no real lease. M1's test had passed only because it built lease JSON by hand
  with that field. The migration now matches on the lease's `node` field. Its test marshals the real stored type.
  The proposal records this as a correction of fact.

Checks: `make ci` exited 0, and the coordinator reran it before committing. `proposals.py check` and `freeze` both
returned no findings.

ADR candidates considered: "capabilities are advertised by a flag at registration, not by version comparison". It
fails the "What Earns an ADR" bar, because it shapes only the broker and node contract. NIT-007's mail capability
already follows the same pattern. The candidate "the cross-user rules apply only once the owner turns them on" is
carried by NIT-006's settled text, so it needs no ADR.

### NIT-006 M3: Cross-user mail

One Opus implementer built M3 in a single run, test first. It proved each test by disabling the code it covers and
watching the test fail: 45 mutations, 45 failures. `make ci` exited 0, in about 453 s, most of it `internal/cli`.

Best guesses, each amended into the proposal:

- Cross-user mail needs cross-user features on. Off, it is refused at send, and a queued message bounces at the
  delivery re-check. Carrying it in the single scope would stamp a coworker's mail as verified.
- Mail capability levels are split: level 1 is basic mail, level 2 is cross-user mail. Level-1 nodes keep same-user
  mail.
- A label nobody bound counts as the owner's for mail, as M2's scope rule does.
- The node marks its reconnect report with a `complete` flag. The broker holds cross-user mail until that report
  arrives, and the node retries a failed report on the slow mail tick. Same-user mail is never held.
- A reply needs the broker to have seen the original read. It must come from the exact address that received the
  message, and go to the original sender's address.
- Replies ride request version 6. The broker advertises a `mail_replies` flag in its registration answer, as M2 did
  for explicit `acting_for`.
- A conversation is keyed by its first message's ID. It is dropped once none of its messages is held and its last
  read is more than 7 days old.
- Neither the store version nor the mailbox format version changes.

The implementer also found two gaps in M1's committed code, both confirmed against M1's checks:

- `Approve` pinned the key but created no owner binding. Once cross-user features were on, a label the owner
  approved would be refused.
- A rebind to a different user bounced only stored mail. Queued mail, which forget had kept, went to the new user.

Both go to a fix round before M3's review.

ADR candidate considered: "a broker re-check never runs before the node's complete reconnect report". It shapes only
the broker and node contract, so it fails the bar.

Before M3's review, a fix round closed both M1 gaps. It added a shared bind path, which approve, the page, and an
awaited key all use. A rebind to a different user now bounces every message held for the label, with the new reason
"the recipient's machine was bound to a different user". The same round fixed three more defects, each behind a test
that failed first:

- An M3 node test closed a stream before it was open. The test lacked a completion signal. It was not a product bug.
- A flaky broker test exposed a real race: a stale or half-open stream could re-check and bounce mail the node may
  have stored. The re-check now trusts only the current registration's report. It also skips cross-user mail sent
  since that report. The flake reproduced in 2 of 300 runs before the fix and in 0 of 300 after it.
- A fresh broker never got an owner, because only the 1.x migration created one. That would have kept cross-user
  features off forever on a new install. The store now assigns its owner when it opens.

M3 milestone review. The `adr-impact` agent found M3 consistent with ADR-01, ADR-04, ADR-05, ADR-06, ADR-07, and
ADR-08. It named three uncovered decisions: mail capability levels, the complete-report gate, and the 7-day window.
None passes the ADR bar. The first two shape only the broker and node contract. The 7-day window is NIT-007's retention
rule.

The reviewer, run on Opus, reported 13 findings. One is a confirmed hole. A client could reuse a surviving
conversation's ID for a new first message. That rebound the conversation to another grant, so a revoked sender's
queued reply got through.

The remaining findings:

- An operator error that told the operator to restart, which cannot fix the state it reports.
- The page's bind used the wall clock, not the broker's clock.
- A plausible early bounce of a downgraded node's sent-but-unacked mail.
- A test gap: a rebind with an unacked sent message.
- A test helper that orphaned the store's owner.
- Text and comment fixes.

Finding 7 was dismissed. A 2.0 store with users but no owner exists only from unshipped code, and no 2.0 store has
shipped.

Fix round results. The fixes for all 12 kept findings landed. Each code fix has a regression test that failed first.
Finding 8 is the exception: its test pins a property, so it passed with no code change.

- A new message whose ID already names a held conversation is refused. A repeat of a held message still gets its
  stored answer.
- The store's owner can no longer be missing. At open, an owner ID with no user record gets its record back under the
  same ID. The refusal stays only as a guard.
- The page, pairing, admission, and grant paths read the broker's injected clock.
- Finding 6 is confirmed. A node that came back too old had its sent-but-unacked cross-user mail bounced before its
  report. That mail could then be both bounced and read. Sent mail now waits for the connection's complete report.
  Cross-user mail goes out only to a node that is cross-user capable as well as reported.
- A rebind never meets sent mail. A pinned label cannot be rebound, and only a forget unpins a label, which resets
  `Sent`. A test pins this.

Best guesses: the reused-ID refusal covers every send, not only cross-user first messages. Sent mail that a complete
report leaves queued bounces "node too old" at once, without waiting for the expiry report.

Checks: `make ci` exited 0. The broker and node tests passed three runs in a row. `proposals.py check` reported only
the row-finish findings. `freeze` returned no findings. The coordinator reran `make ci` before committing.

## Row review

The row review covered the range from the run base through M3. The `adr-impact` agent found the range consistent
with ADR-01 and ADR-03 through ADR-08. It listed seven decisions that no ADR records. NIT-006's settled decisions
record all seven. The 2026-10-24 Decision Log entry already declined the broadest one: limit refusals to the detail
that discovery shows. No new ADR candidate is recorded.

The reviewer, run on Opus, found that the three milestones did not hold together in the shipped binary. Each
milestone's tests built the broker config by hand, so the defects passed every milestone review.

Confirmed findings:

1. `runBroker` never passed the configured OIDC block to the broker. The sign-in page was never mounted, and
   `cross-user true` always refused. Nothing cross-user worked outside tests.
2. Forget and revoke-user left the label bound. A forgotten key could re-register and act as the old user while
   cross-user features were on. The rule that decides whether a label is bound had drifted into three different
   answers across the milestones.
3. `nitipan node pair` could not reach a real broker. It sent no machine or uid headers, was gated on a binding, and
   skipped the node's TLS checks.
4. The page bind pinned an arbitrary pending key, not the key that sent the pairing code.
5. With the switch off, a bound coworker could act in the owner's single scope. Mail treated the same label as
   another user.
6. The plain-HTTP gate was overwritten on each registration. A key that had crossed plain HTTP could re-report
   HTTPS and pass. A forgotten machine could block the switch forever.
7. `cross-user true` logged the unbound labels instead of showing them.
8. Revoke-user did not end the leases an agent held for the revoked user. It ran across many commits. One of its
   delete loops did nothing.
9. The page bind checked admission outside its lock.

The review also found proposal and README drift, test files and comments named by milestone or plan item,
near-duplicate tests, tests that reach into internals, and comment-craft issues.

Best guess for finding 5. NIT-006 never weighed a coworker bound while the switch is off, or the switch turned off
after it was on. The run takes the reading that keeps the proposal's "one owner scope while off" true: while
cross-user features are off, the broker serves only the owner's labels and unbound labels. A coworker's bound label
is refused until the switch is on, with an error that says so. A coworker can still bind on the page while the
switch is off, so setup can happen before it is turned on. Turning the switch off later refuses coworkers again and
leaves their state intact. If this guess is wrong, the alternatives are to refuse page actions while the switch is
off, or to refuse turning it off while a coworker is bound.

The fixes run in two rounds. Part A covers findings 1 to 4, 6, 7, 9, and the unified bound-label rule. Part B covers
findings 5 and 8, the docs and proposal text, the test hygiene, and comment craft.

Row review fix rounds. Each code fix has a regression test that failed on the unfixed code, with three exceptions
noted below.

Part A:

- The broker config builder now carries the OIDC block, the listener, and TLS into the broker. A new test drives that
  wiring through a local test issuer that requires the client secret. Before the fix, the sign-in page answered 404
  in every listener case.
- With OIDC wired, the broker fetched the discovery document at start. A provider outage then stopped the broker,
  which NIT-006's accepted outage risk rules out. Discovery now waits for the first sign-in and retries after a
  failure. While the provider is down, the page answers 503.
- One rule now decides whether a label is bound: it has a user and is pinned under its node's current key. One rule
  decides its scope. Every caller uses them. Forget clears the user and keeps the last bound user. A forgotten key
  that registers again acts for nobody. Before the fix, it could claim, list, break, and receive `whoami` answers
  as the old user.
- `nitipan node pair` goes through the node's own client. It works for an unbound label while cross-user features
  are on, and prints the real label. The page bind pins exactly the key that sent the code. The test for that caught
  the bug in only 1 of 12 runs on the unfixed code, because the wrong key was picked by map order.
- The plain-HTTP mark is sticky per key. `cross-user true` returns and prints the unbound labels.
- The test for the bind's admission check passed on the unfixed code. The race cannot be timed without a hook
  inside the handler, so that test pins the rule, not the bug.
- `CreateUser`, a test-only export, was removed.

Part B:

- The switch-off rule above is implemented in the scope rule. About 40 existing tests had run coworker labels with
  the switch off. Their setup was fixed rather than their assertions.
- Revoke-user is now one commit. It ends every lease held for the user or by the user's labels, and drops
  delegations in both directions. The single-write test passed on the unfixed code, because there the first write
  fails cleanly too. A failure in the middle cannot be injected without a seam.
- The broker-host commands `admissions`, `admit`, `link`, `bindings`, and `revoke-user` existed only as admin-socket
  routes, with no CLI, although the README documented them. They now exist.
- The migration and store read an injected clock.
- `redirect_url` must be https off loopback.

Unowned flake: `TestLostToAnotherHolderWritesANotice` (internal/node) failed once during a three-package `-count=2`
run. Renew answered 503, then the claim was refused. It passed alone at `-count=10`, in its own package at
`-count=2`, and in two `make ci` runs. No node code changed in that round. Whether it fails on the base commit is
uncertain. Part C makes one bounded attempt to reproduce it.

Part C covered text and test hygiene. It made no product change.

- The three As built blocks are now prose. The Decision Log split is fixed. The milestone-named test files were split
  into files named for their contracts: revoke, delegation, cross-user mail, migrate, scope, approve, and broker.
- Each near-duplicate test was retired only where a kept test proves everything it did. Where a kept test proved
  less, the retired test's checks were folded in. The overlap test gained the positive control it lacked.
- Binding and pairing tests now drive the real pair route and the sign-in page through a local test issuer. The
  internal shortcuts are gone. The direct store writes that remain set up a precondition no current route can
  produce, and each one says so.
- One M1 check is not proven: colliding user labels are refused. Its old test wrote a colliding user and never
  called the refusal. Proving it needs an injectable user-label derivation, like the machine-label seam. That goes
  to the final review's fix round.
- The node flake did not reproduce in two attempts. An unconfirmed hypothesis: a renewal already past the test's
  intercept can reach the broker after the clock advances, which keeps the lease fresh. The fix would be a
  completion signal for in-flight renewals. It stays open.

Checks: `make ci` exited 0, and the coordinator reran it before committing. `freeze` returned no findings.
`proposals.py check` reported only the row-finish findings.
