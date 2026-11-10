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
| NIT-006 M3 | QUEUED | |

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
