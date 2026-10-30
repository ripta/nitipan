# NIT-004: Lease Broker

**Status:** accepted
**Created:** 2026-10-18
**Updated:** 2026-10-30

## Dependencies

- NIT-001

## Impacts

- NIT-005 — "Expiring leases", since fencing tokens are issued with each grant. "Lease term and renewal": a revived
  lease keeps its token, and the CLI can ask for a synchronous renew for NIT-005's pre-push lease check. "Source of
  truth for machine leases": `machine` leases get no broker grant, so no broker token
- NIT-006 — "Broker protocol", since auth wraps every broker call. "How sessions reach the broker", since the node
  attests each session's identity. "The machine part of a qualified name", since NIT-006 wraps the pinned key in a
  user binding. "Which user a user-reach lease belongs to" and "Shared agents and acting_for", which moved the scope
  and `acting_for` questions to NIT-006. Amended 2026-10-24; see Decision Log. NIT-006, now accepted, amends eight
  decisions here: "Broker protocol", "The machine part of a qualified name", "Lease term and renewal", "Which user a
  user-reach lease belongs to", "Machine leases and user leases on the same path", "Discovery queries", "Broker
  lifecycle and store", and "Shared agents and acting_for", plus "Version skew" and two risks
- NIT-007 — "Per-machine node", "Machines connect out", and "Qualified names". A mailbox lives on the broker, reaches
  a machine over the stream the machine opened, and is addressed by qualified name. "The machine part of a qualified
  name" fixes the address as `<handle>@<label>` and refuses a label the broker never saw. "Broker protocol" acks each
  event, which mail delivery can reuse. NIT-007 amends seven decisions here: "The machine part of a qualified name"
  pins a label's key once the user approves it, "Broker protocol" carries that key in the auth slot before NIT-006,
  "Broker lifecycle and store" persists queued mail and its stage and bounce state, "Node lifecycle" puts the codex
  path and `CODEX_HOME` in the plist, and "Version skew" gates mail events on the node's version. "Qualified names"
  and "Discovery queries" gain a mail address keyed by session ID
- NIT-008 — "Source of truth for machine leases". Recall applies only to NIT-003's local sub-leases, which have no
  broker term, and needs no broker. The node applies a recall's deadline when it mirrors `leases/`. Amended
  2026-10-24; see Decision Log
- NIT-002 — "Name syntax", which reserved the `repo` namespace at `machine` reach. "Name syntax for wider reach" uses
  it
- NIT-001 — "Lease record fields" and the open question "Wider-reach lease names". This proposal answers that question.
  A new record field here follows NIT-001's versioning rule

## Summary

A self-hosted broker extends nitipan leases across machines. It grants expiring leases at `user` and `shared` reach.
`machine` leases stay in NIT-001's state directory, and the broker sees them as mirrors. One node per machine and OS
user heartbeats to the broker for every session it serves, and relays their requests. The node reports a session's
death from the local liveness check that NIT-001 defines. Sessions find each other by query, such as "who holds X" or
"who is named N." The broker never broadcasts.

The broker is the one address every machine can reach. Machines connect out to it, and it never connects in. Leases at
`user` and `shared` reach need names that machines share. This proposal defines them, keyed by a repo's root commits.

## Motivation

NIT-001 coordinates sessions on one machine. A user who runs sessions on several machines still checks for overlaps by
hand. Two sessions on two machines can claim the same repo path and both succeed, because each machine sees only its
own state directory.

A local path cannot name a cross-machine lease. `/Users/ripta/projects/nitipan` means nothing on another host. A
broker needs names that every machine resolves the same way.

## Design Decisions (Settled)

The first four come from the roadmap the user approved on 2026-10-18, recorded in the research write-up. The rest the
user settled the same day while the proposal was drafted.

### Expiring leases

Amended 2026-10-24; see Decision Log. The rule applies to leases the broker grants, at `user` and `shared` reach.
`machine` leases keep NIT-001's rule. See "Source of truth for machine leases" and "Lease term and renewal".

The broker grants leases that expire. A holder that stops renewing loses its lease when the term ends. For the leases
the broker grants, this replaces NIT-001's "a dead holder's lease can be claimed" with a rule that also works when the
broker cannot see the holder's process.

### Per-machine node

Amended 2026-10-24; see Decision Log. The node is per machine and OS user. See "Node scope".

One node per machine heartbeats to the broker on behalf of all of that machine's sessions. Sessions do not heartbeat.
An idle session cannot heartbeat anyway.

The node checks each local holder with NIT-001's liveness check. When a holder dies, the node reports it, and the broker
ends its leases without waiting for the term to run out.

### Discovery by query

Sessions find holders and capabilities by asking the broker. Examples are "who holds X" and "who can do Y." The broker
does not push announcements to sessions.

### Self-hosted

The broker and node run inside the user's own infrastructure. Remote Control is banned at the user's work, so no part
of this design may depend on it or on any hosted relay.

### Callers do not change

A V1 caller keeps working when the broker arrives. The research claims a V1 record already carries every field the
broker needs. A field this design adds follows NIT-001's versioning rule.

### Machines connect out

The broker has its own address, and every machine can reach it. Nodes open their connections to the broker. The broker
never opens a connection to a machine, so no machine needs to be reachable from outside.

Anything the broker sends to a machine travels over a stream that machine opened. That covers lease events and
NIT-007's messages. See ADR-01.

### Single broker

One broker process serves every machine. A replicated broker is too complex for nitipan's needs. If outages become a
problem, replication is its own proposal. `machine` leases keep working through NIT-001's state directory while the
broker is down.

### Qualified names

Each layer contributes its own part of a name. A session's local name only needs to be unique on its machine. That is
the name a local socket or MCP server exposes. The broker qualifies it with a machine part, roughly
`<session>@<machine>`. Two sessions with the same local name on different machines stay distinct.

The local part is NIT-001's handle, and the machine part is the label in "The machine part of a qualified name". A
handle is unique within one state directory, and the label covers the machine and OS user.

Amended 2026-10-24; see Decision Log. A mail address under NIT-007 uses a mail name as its local part, keyed by the
session ID and derived only by the broker. It equals the handle for Codex and opencode. A Claude Code handle changes
on every resume, so a Claude Code session's mail name differs from its handle, as ADR-05 requires for stored
identities.

### Source of truth for machine leases

A `machine` lease lives in NIT-001's state directory. The broker also needs to know about it to answer queries.

- The state directory stays authoritative. The node mirrors it to the broker, and the broker's view may lag.
- The broker becomes authoritative once a node is connected. The state directory is a cache. This leaves two sources of
  truth during an outage.

Decision (autonomous): the state directory is authoritative at `machine` reach, and the broker is authoritative at
`user` and `shared` reach. The broker never grants, renews, or expires a `machine` lease.

- The node mirrors each local `machine` lease to the broker as a read-only fact. Each mirror is a full snapshot of the
  machine's leases, which replaces the last one. A node sends one on every connect, so leases a V1 binary took while
  the node was down appear on reconnect. It also sends one whenever `leases/` changes, which it watches with kqueue,
  and at least once per heartbeat
- A record the node cannot use, such as one of a higher major, is mirrored as an unknown holder with an `all`
  footprint on its machine. Every local claim refuses it, so discovery must not answer "nobody" for its keys
- Mirrored facts are scoped by machine identity. The broker matches a mirrored path only against queries about the same
  machine. Two machines that both have `/Users/ripta/projects/nitipan` never meet through their mirrors
- The node reads NIT-008's `recalls/` when it mirrors, and reports a child past its recall deadline as ended, with the
  parent as owner. Amended 2026-10-24; see Decision Log
- A node whose stream has had no heartbeat for longer than a grace period has its facts marked stale in every query
  answer, whether or not the connection has closed. They are replaced when it reconnects with a new snapshot
- Nothing the broker holds is ever written into a state directory that V1 scans. Amended 2026-10-24; see Decision Log.
  A V1 reader treats an unknown footprint entry type as
  `all`, so one cached `user` lease would block every V1 claim on that machine. A mirrored other-machine path record
  would read alive under ADR-04 and block unrelated claims
- "Expiring leases" applies to leases the broker grants. A `machine` lease keeps NIT-001's liveness rule, which the
  node can check because it runs on the same machine

Only sessions on one machine can contend for a `machine` key, and the local lock already decides between them. A mirror
serves "Discovery by query" without adding a second decider.

Broker authority with the state directory as a cache was rejected. A V1 binary on PATH keeps deciding locally with no
broker call, so two deciders exist whenever one runs. The broker would expire a live holder's lease when the node lost
contact, and the node would then delete a record whose holder is still working. No other machine could have claimed
that key in the meantime, so the deletion protects nothing. Keeping `machine` leases off the broker entirely was
rejected. It gives up the settled "Discovery by query" and M3's cross-machine "who holds X".

### Broker protocol

The research does not name a transport. The transport must carry a long-lived stream from node to broker, since the
broker sends to machines only over connections they opened. Candidates include HTTP with server-sent events, gRPC, and
Connect. The choice affects NIT-006, which wraps every call with auth.

Decision (autonomous): HTTP/1.1 with JSON bodies. The node opens one long-lived `GET` on which the broker streams
server-sent events. Requests go as separate `POST`s. The standard library covers both sides.

- Delivery is acknowledged at the application level. A successful write to a half-open connection is not delivery. An
  event the broker sends stays queued until the node acks it by `POST`
- A request's response and a stream event travel on different connections, so either can arrive first. Each lease
  carries a revision number, and the node keeps the state with the higher revision
- Go has no SSE client. The node carries its own parser and reconnect loop. On reconnect it resyncs from a snapshot
  rather than replaying from `Last-Event-ID`. Event IDs serve only for ordering and deduplication
- The stream sends periodic keepalive comments and `Cache-Control: no-cache`, so an HTTP/1.1 proxy neither buffers it
  nor drops it as idle
- The auth slot is the `Authorization` header on every request, the stream's `GET` included. NIT-006 fills it. Before
  NIT-006, it carries the node key NIT-007 pins. NIT-006 keeps that key and binds its label to a user, so the slot
  carries the same key after NIT-006. A credential checked when the stream opens is enforced for the stream's
  lifetime: the broker closes the stream when the credential expires, and when NIT-006's "forget label" or "revoke
  user" revokes it. Amended 2026-10-24; see Decision Log
- Under NIT-006, features that cross users need the broker's own listener on loopback or serving TLS itself, and a
  node refuses plain HTTP to a broker that reported them on. See NIT-006's "Node credential"
- The schema is JSON under a versioned path prefix. Skew between nodes and the broker follows "Version skew"
- The broker serves HTTPS when its config names a certificate and key, through the standard library. A node verifies
  it against the system roots, or a CA file named in the node config. Without a certificate, the broker refuses to
  listen on any address that is not loopback or private, a wildcard bind included. The node also refuses plain HTTP to
  a public address, and warns when it uses plain HTTP at all. Amended 2026-10-24; see Decision Log
- TLS may terminate at an ingress in front of the broker. The broker then listens on plain HTTP on a loopback or
  private address, and the node verifies the ingress's certificate at the address in its config. NIT-007 sends mail
  only when that address is HTTPS and verified

JSON lets the protocol follow the same evolution rule ADR-04 sets for records, so the project keeps one schema
discipline. go.mod has no networking dependency today, and this adds none. It also works through ordinary HTTP/1.1
proxies.

Connect was rejected. It can take the same shape over HTTP/1.1, but it adds protobuf and a codegen step, and protobuf's
field rules duplicate ADR-04's. gRPC was rejected for the heaviest dependencies and for requiring HTTP/2 end to end,
which some corporate proxies break. A WebSocket was rejected. It needs a third-party module, hand-written framing and
request IDs, and some proxies block the upgrade.

### Node scope

"Per-machine node" says one node per machine. NIT-001's state directory is per OS user, created mode 0700, and the
node reads it to mirror leases and check liveness. A node per machine cannot read a second OS user's directory.

- One node per machine and OS user, running as that user
- One privileged node per machine, reading every user's directory
- One node per machine, serving one OS user only

Decision (autonomous): one node per machine and OS user, running as that user and reading only that user's state
directory. "Per-machine node" is amended to match. Amended 2026-10-24; see Decision Log.

- At the broker, a node is identified by its machine identity and its numeric OS uid together. The user name travels
  with it for display only
- Snapshots, the grace period, and staleness under "Source of truth for machine leases" are all keyed by that pair. One
  user's snapshot never replaces another user's on the same machine
- The uid travels in the node's registration and its mirror messages. Lease records already carry the holder's user,
  and gain no field

Two OS users on one Mac already never coordinate locally, since each has its own state directory and lock. A per-user
node keeps that scope and needs no privilege. Each node's liveness checks already resolve the right user's home, its
opencode database, and its Codex files.

A privileged node was rejected. Its liveness checks resolve root's home, so an opencode session would read dead from
the wrong database and the broker would end a live holder's leases. A root-owned lock file it created would lock the
user out of their own claims. It also needs root to install. A node serving one user only was rejected. Which user gets
the broker would follow restart order, and the second user's leases would be missing from discovery.

### How sessions reach the broker

A session's `user` or `shared` claim must reach the broker. Sessions run inside agent sandboxes that may block network
access and Unix sockets. Whether the CLI calls the broker itself, calls the node, or hands the request to the node
through the state directory is open.

Decision (autonomous): the CLI hands each broker request to the node through files in a new directory under the state
directory. V1 binaries never scan it. The node forwards the request over its own broker connection and writes the reply
beside it.

- The CLI publishes a request atomically, with NIT-001's temp-file scheme, and waits for the reply without holding
  NIT-001's lock. The node never holds the lock across a broker round trip
- The node publishes a presence record with its PID and process start. The CLI checks it with NIT-001's liveness rule
  before writing a request. With no live node, the command fails at once with "node not running" and the install
  command from "Node lifecycle"
- A request moves through pending, claimed by the node, and answered, each step a `rename(2)`. The CLI may withdraw a
  request only while it is pending. The request ID is the broker's idempotency key, so a retry never grants twice
- A CLI that times out after the node claimed its request does not know the outcome. The answer is delivered later as a
  notice, the way NIT-003 reports a request's outcome, and a retry with the same request returns the same answer
- A lock holder reaps requests and replies whose requester is dead, as NIT-003 reaps sub-lease requests
- Requests and replies carry a version, and the node publishes its own. A CLI newer than its node gets "node too old"
  for a request kind the node does not know, rather than a timeout. See "Version skew"
- The node watches the directory with kqueue, through the `golang.org/x/sys` module go.mod already has

The state directory is the one path every agent's sandbox already allows under NIT-001. The node holds the machine's
broker connection and credential, so no session needs network access or a broker credential. That bears on NIT-006:
the node attests each session's identity to the broker, which is no weaker than V1, where any process of the same user
can write records.

"Source of truth for machine leases" says nothing the broker holds is written into a state directory. That rule is
amended to directories V1 scans. Its reason is that a V1 scanner would read an unknown footprint as `all`, and a
directory V1 never scans cannot cause that. Amended 2026-10-24; see Decision Log.

A direct HTTP call from the CLI was rejected. It widens every sandbox, and Codex's network switch opens all egress at
once. It puts a credential in every session, and still needs the node for heartbeats. A Unix socket to the node was
rejected. Codex's default sandbox denies Unix socket connections with network off. A socket with a file fallback was
rejected. The fallback still needs everything above, and the socket adds a second transport to test for lower latency
alone.

### Node lifecycle

NIT-001 has no daemon. The node is one. Who starts it, what keeps it running, and what a session sees when it is not
running are open.

Decision (autonomous): the node runs as a per-user launchd LaunchAgent that restarts it when it exits. This is
macOS-only, which matches the platforms the user runs.

- `setup` prints the plist and the `launchctl bootstrap gui/<uid>` command, following NIT-001's rule that `setup`
  prints rather than writes. The plist names the stable executable path on PATH, not a versioned install path that an
  upgrade deletes
- The "node not running" error prints the same install text
- The node takes a flock on a node lock file in the state directory. A second node for the same machine and user fails
  loudly, since two would keep replacing each other's snapshots
- The node exits when its executable changes, so launchd restarts it on the upgraded binary
- The broker address and proxy settings come from a node config file at `~/.config/nitipan/node.json`, outside the state
  directory. The node finds the home directory in the user database, like the state directory, and ignores `HOME` and
  `XDG_CONFIG_HOME`. A LaunchAgent does not inherit the user's shell environment, and Go's default proxy lookup reads
  only the environment, so the node sets its own proxy function from the file. The node refuses a file or directory
  that another user owns or that others can write. That check guards against other users and loose permissions, not
  against a sandboxed session, which runs as the same user. ADR-07 keeps what the node connects to out of input a
  sandbox can write. Amended 2026-10-29; see Decision Log
- The plist also carries the codex path and `CODEX_HOME` that `setup` resolves, for NIT-007's Codex rings. No agent
  sandbox can write the plist, while the state directory is writable from inside them, so anything the node runs comes
  from the plist. Amended 2026-10-24; see Decision Log

`machine` leases keep working with no node. Commands that need the broker fail at once when it is absent, as "How
sessions reach the broker" says.

Starting the node on demand from the CLI was rejected. A process started from an agent's shell inherits that agent's
sandbox. Under Codex it would run with network denied, and it would die when the thread that spawned it unloads. Two
CLIs that both found no node would start two nodes. Starting it from a session hook was rejected. It escapes the
sandbox, but nothing restarts it after a crash, and its lifetime under each agent's host is unverified. Running it by
hand was rejected. Closing the terminal tab would stop heartbeats, and the broker would expire every local `user` lease
while its session kept working.

### Lease term and renewal

The research does not give a term length, a renewal interval, or a grace period. It also does not say what a node does
when it cannot reach the broker. Its local sessions might keep their leases until the term ends, or the node might
release them early.

Decision (autonomous): one fixed term of 5 minutes for every broker lease. A lease that lapses only because renewals
stopped can be revived by its holder while nobody else has claimed its keys, and the holder is told either way.

- The term is a constant in the broker, not a config field. Nobody has been named who would set another value. Tests
  inject it through a constructor
- Every grant and renewal ack carries the lease's absolute expiry and the term. The node paces its renewals from those
  values, at a third of the term, so a node never runs on a term the broker does not use
- The node renews every local holder's leases in one heartbeat. It renews at once on wake and on reconnect
- A holder whose liveness reads unknown under NIT-001 counts as alive, as it does for local claims, so the node keeps
  renewing its leases. Discovery shows it as unknown, and a `break` from any machine can end it. Under NIT-006 that
  break must come from the lease's own user or the broker owner. Amended 2026-10-24; see Decision Log
- The node counts a lease's remaining life from when it sent the renewal the broker acked, not from when the ack
  arrived. The broker started the term on receipt, so the node's deadline is never later than the broker's
- Time is measured with `CLOCK_MONOTONIC` through `golang.org/x/sys`. On macOS it keeps counting while the machine
  sleeps and never steps with NTP. Go's own monotonic reading stops during sleep, and the wall clock can step backward
- The node publishes each holder's deadline anchor in a file the CLI reads. A command run while the node is
  disconnected says the holder's broker leases are at risk and when they lapse. Past that time it says they lapsed
- The broker keeps a lapsed lease as lapsed rather than deleting it. The holder's next renewal revives it with the same
  grant and, under NIT-005, the same token, if no grant overlapping its keys happened since it lapsed. Otherwise the
  renewal returns "taken", naming the new holder. The revision rises on every state change, the lapse and the revival
  included, so the node can order a late stream event against a reply
- The node writes a notice to the holder in both cases. One says the lease was unprotected for a stated interval and
  nobody claimed it. The other says it was lost to a named holder. The holder's next nitipan call reports it. A notice
  ID carries the revision it reports, so two lapses before the holder's next call give two notices
- Revival never applies to a lease that ended by release, `revoke`, `break`, or a reported death
- A lapsed lease stays until the holder's node sends a snapshot that omits its holder or its anchor, or until its
  label has not been seen for 30 days. An overlapping grant turns it into a "taken" marker that names the new holder.
  The marker is an unacked end fact, so the next snapshot carries it and it is dropped when the holder's node acks
  it. "Who holds X" reports a lapsed lease as lapsed. Amended 2026-10-24; see
  Decision Log
- A renewal for a lease the broker no longer has returns "not found". The node writes a loss notice that says the
  broker had no record of it, unless it already holds that lease as ended or taken
- After the broker starts, or wakes from sleep, it expires nothing for one term, so every node has a chance to renew
- The grace period after which a disconnected node's mirrored facts read stale, under "Source of truth for machine
  leases", is one term
- A lease the node wrote off whose renewal in fact reached the broker shows as live on the next snapshot. It is
  reported as restored through the same notice
- The CLI can ask for a synchronous renew through the request handoff. NIT-005's pre-push lease check uses it

The term mostly matters for a machine that went silent, since the node reports a dead session at once. On laptops,
silent usually means asleep. Revival lets a short term unblock other machines quickly without dropping every lease over
a lunch break. Keeping the token on revival matters: a new token after another holder's grant would let a stale holder
pass NIT-005's fence.

This leaves one gap open. An idle holder learns of a lapse only on its next nitipan call, and nothing stops its work
until NIT-005 lands. The Risks entry on a partitioned holder covers it.

Releasing every lease as soon as the node loses the broker was rejected. A disconnected node cannot release anything at
the broker, so other machines gain nothing, and every Wi-Fi change would drop every lease. A term chosen per claim was
rejected as a per-grant knob. A session that asks for a long term would make its sleeping laptop block others for that
long. A silent re-claim on reconnect was rejected. After another holder's grant and release, the re-claim would carry a
higher token, and the stale holder's push would pass the fence. A plain lapse with no revival was rejected. Every sleep
longer than the term would drop every lease and need the user to recover each one.

### Name syntax for wider reach

A `user` or `shared` lease needs a name every machine shares. Filesystem paths do not qualify. The research sketches a
repo-relative name, `repo:<remote-url>#services/billing`.

- Repo-relative names keyed by remote URL. Two questions follow. SSH and HTTPS forms of one remote differ, so the URL
  needs a canonical form. A repo with several remotes needs a rule for which one names it.
- Names keyed by something other than the remote, such as the repo's root commit. Stable across remote renames. Two
  forks of one repo would share a root commit.

Decision (autonomous, judge): a repo is identified by the set of root commits reachable from HEAD. Two wider-reach
entries overlap when their root sets intersect and their repo-relative paths overlap by whole components.

- The text form people type and read is `repo:<root-sha>#<repo-relative-path>`. Its root is the first-parent root,
  from `git rev-list --max-parents=0 --first-parent HEAD`, which prints one line. `list` shows the canonical remote
  beside it as a label only. The remote is never part of identity
- The stored form, in `list --json` and anything a guardian stores, carries the whole root set. A typed name matches a
  holder whose stored set contains its root
- A claim must come from a checkout, which expands to the whole root set. A typed name with no checkout behind it serves
  queries and `release` only. A claim from a typed name alone would store one root, and a checkout elsewhere on the
  other side of an unrelated-history merge would not meet it
- The repo-relative path is made relative to the innermost checkout that contains the key, after ADR-03 canonicalizes
  it on the claiming machine. A claim on a nested worktree's path made from the parent repo then gets the same name as
  one made from inside the worktree. Paths compare with ADR-03's case folding and Unicode normalization
- A claim inside a submodule, or covering one, also emits an entry keyed by the submodule's own roots and its
  submodule-relative path. A standalone clone of that submodule then meets it. A claim covering a submodule that is not
  initialized on the claiming machine is refused, since its roots cannot be read
- A shallow clone, an unborn HEAD, and a repo with an `info/grafts` file in its common directory are refused. Each
  error says what to do. Git runs with `--no-replace-objects`, and `cleanGitEnv` also strips `GIT_GRAFT_FILE`
- The root walk has its own timeout, apart from the 5-second limit on other git calls. The set is cached by HEAD
  commit. A new HEAD descended from a cached one walks only the new commits and joins the result with the cached set. A
  timeout refuses the claim. It never falls back to one root, since a partial set can miss a holder's
- The CLI derives the entries, since it already canonicalizes keys and stands in the checkout. The request carries the
  derivation's version. The broker refuses a request whose version is below the lowest it accepts, so an older CLI
  that emits no submodule entry cannot claim. The broker applies the comparison. See "Version skew"
- A `repo` entry never overlaps a `path` or `name` entry, apart from what "Machine leases and user leases on the same
  path" settles
- Every worktree of one repo shares its root set. A `user` lease on `repo:<root>#` therefore covers every worktree's
  files at `user` reach. That is the opposite of ADR-03's boundary exemption at `machine` reach, where two worktrees are
  two checkouts. It is deliberate, since a `user` lease names the repo's files, not a directory

Accepted limits:

- Forks and repos cloned from one template share roots, so unrelated paths collide at `shared` reach. This fails
  closed: the refusal names the holder and its remote label
- An orphan branch such as `gh-pages` has a root set disjoint from `main`'s, so claims on the two never meet. They hold
  different content
- A history rewrite that changes the root commit leaves older clones with a disjoint set until they fetch. That fails
  open, and so does a graft or replace ref the user adds on purpose
- A repo that subtree-merges another repo's history at a prefix, such as `vendor/lib/`, holds that repo's root in its
  set. A standalone clone's `#src/foo` then never meets the monorepo's `#vendor/lib/src/foo`, which holds the same
  content. That fails open. The clone's `#src/foo` does meet the monorepo's unrelated `#src/foo`, which fails closed

A canonical remote URL was rejected. An ssh config host alias such as `gh-work:org/repo` cannot be folded to
`github.com` without reading `~/.ssh/config`, so one repo gets two names and both claims succeed. In a fork workflow,
`origin` also differs between machines. One root commit was rejected. A shallow clone reports its boundary commit as
the root, so each clone depth gives a different name. A root set with no text form was rejected, since guardians and
scripts store names. User-assigned names were rejected. A missing config entry on any machine gives two names.

### The machine part of a qualified name

- The host name. Simple. Host names can collide across networks and change over time.
- A name the broker assigns when a node first registers, kept by the node. Unique by construction. The node must store
  it, and a reinstalled machine gets a new one.
- The stable machine ID NIT-001 already records. Unique. Unreadable to people.

Decision (autonomous, judge): a four-word label derived from the node's machine ID and numeric OS uid. The address
people type is `<handle>@<label>`.

- The label is a hash of the machine ID and uid through NIT-001's handle word list, with a domain separator in the hash
  input. Four words carry about 44 bits, the same as a handle. The `@` position tells a label from a handle
- The broker derives the label from the raw machine ID and uid the node registers. The derivation runs in one place, so
  nodes on different versions cannot derive different labels. It is a stored contract once addresses are stored
- Two OS users on one Mac get two labels, since "Node scope" keys a node by machine and uid
- The broker keeps a persistent table of labels it has seen, with each one's machine ID, uid, LocalHostName for
  display, and last-seen time. The table validates addresses only. It never allocates a label. An address whose label
  was never seen is refused with an error, rather than queued as mail for an offline machine
- The node keeps a random instance key in its state directory and presents it on connect. A connection with the same
  machine ID, uid, and key replaces the broker's old stream for that node, which covers a half-open stream after sleep.
  A different key while the old stream is live is refused and reported. A stream counts as live while the broker has
  had a heartbeat on it within one term. Losing the key never changes the label
- Once the user approves a label for mail under NIT-007's "Auth", its key is pinned. A different key is then refused
  whether or not the old stream is live, for leases as well as mail. Losing a pinned key locks the label until the
  user runs "forget label" on the broker host and approves the new key. The label itself still never changes. Amended
  2026-10-24; see Decision Log
- A collision between two labels the broker has seen is refused, and the error names both machines
- When the OS gives no machine ID, the node refuses to start. Its error says the hardware UUID could not be read, and to
  retry or report it
- A logic-board replacement or a recreated account changes the label. Stored addresses for the old label then fail
  with the unknown-label error

A label derived from the identity can never pass to another machine, and needs no registry to allocate. The instance
key narrows takeover before NIT-006 lands: taking a node's stream also needs a file in the user's 0700 state
directory. It is not auth, and it does not catch a disk clone made after the node first ran, since that copies the key
too. NIT-006's "Node credential" keeps the key and binds its label to one user, and never replaces it. A label
rebound to a different user starts clean. Amended 2026-10-24; see Decision Log.

The host name was rejected. Host names collide, and a laptop's changes with the network. A broker-assigned name was
rejected. A reinstall or a wiped store gets a new name and strands stored addresses. The raw machine ID was rejected as
unreadable to people typing an address. A broker-assigned label seeded from LocalHostName was rejected. A broker wipe
loses its record of retired labels, and a new machine can take an old machine's label before it reconnects. A
three-word label of the machine ID alone was rejected. It gives two users on one Mac the same label, and it refused a
routine reconnect while the broker still held the old stream.

### Which user a user-reach lease belongs to

`user` reach covers all of one user's machines. A record's `holder.user` is the OS user name, which can differ across
one person's machines. Which identity scopes a `user` lease before NIT-006 is open.

Decision (autonomous): until NIT-006, each broker is one user scope. Every `user` lease on a broker conflicts with
every `user` lease of another holder on overlapping names, whatever its `holder.user`. One holder never conflicts with
its own broker leases. Amended 2026-10-29; see Decision Log.

- No name configures the scope. Nobody has been named who would set one
- `user` and `shared` leases of different holders on overlapping names conflict
- `acting_for` is carried unchanged on every broker lease, and is ignored for scope until NIT-006 defines its meaning.
  NIT-006 M1 then has a defined input
- When NIT-006 splits the scope by authenticated user, leases only get narrower, which matches what callers asked for.
  Stored `repo:` names and `<handle>@<label>` addresses carry no user, so nothing stored changes meaning

NIT-006 is accepted and scopes `user` leases by authenticated identity. Each
bound user is one user scope, named by a lease's `acting_for`, under NIT-006's "Session credential" and "Shared
agents and acting_for". A `user` lease and another user's `shared` lease on overlapping names still conflict, with
the refusal detail NIT-006's "`user` and `shared` leases across users" sets. The one-scope rule above holds until a
broker turns NIT-006's cross-user features on.

Merging two people into one scope gives a visible refusal that names the holder and its OS user. Splitting one person
into two scopes gives silent non-coordination, which is the bug in Motivation. A broker per user scope fails the first
way, and before auth every option shares the exposure of a coworker's node joining.

The OS user name was rejected. One person whose OS user names differ between machines would get two scopes, and those
machines would never coordinate. A scope name set in the node config was rejected. A machine nobody configured falls
back to the OS name and splits the scope silently, and an older node sends no name at all. Deferring `user` reach to
NIT-006 was rejected. Callers who meant "my machines" would use `shared` now and change reach later, against "Callers
do not change".

### Machine leases and user leases on the same path

A `machine` lease names a checkout. A `user` lease names a repo path. Both can cover the same files.

- One implies the other. A `machine` lease on a checkout also claims the `user` name of that path, so two machines
  cannot both hold the path.
- They are independent. A `machine` lease protects the working tree only. Cross-machine conflicts need an explicit
  `user` lease.

Decision (autonomous, judge): a `user` lease is anchored to an ordinary `machine` lease in the claimant's checkout.
Neither kind refuses the other through the broker. Overlaps between them are reported.

- A `user` or `shared` claim made from a checkout first takes, or reuses, a NIT-001 `machine` lease on the covered
  paths in that checkout, for the same holder, under the local lock. A V1 binary in that checkout then meets an
  ordinary path record. "Already covered by your leases" counts as success, and the covering lease becomes the anchor.
  Partial overlap with the holder's own leases follows NIT-001's own-lease rules unchanged
- The broker request carries the anchor's lease ID, and the broker stores it. The link also lives in a node sidecar
  keyed by lease ID, in a directory V1 never scans. It never goes in the record body, so the anchor keeps its V1
  version and older binaries can still rewrite it
- The sidecar stores the anchor's keys and the CLI's derivation of them at claim time. On each snapshot, the node
  checks that the anchor still exists, following `absorbs` across merges, and that its keys still cover the `user`
  lease's paths. Unchanged keys need no new derivation. When the keys changed and the node cannot derive them again,
  the check fails
- When the anchor ends, narrows off the lease's paths, or fails the check, the node sends an explicit end for the
  `user` lease and writes a notice to its holder. An ended lease is never revived. The broker also refuses to revive a
  lapsed lease whose anchor the node's latest snapshot omits
- Ending the anchor covers release, `break`, `revoke` of a child that held it, death, and `--replace`. A sub-lease
  delegated from the anchor leaves the anchor's keys in place, so the `user` lease survives it
- A `user` claim that the broker refuses, or answers "refused" after a timeout, leaves the anchor as an ordinary lease
  held by a live holder. If the claim took the anchor new, the CLI releases it, or the next call that receives the
  late refusal does. Crash cleanup follows NIT-001 liveness and the node's death report
- The sidecar records whether the `user` claim created its anchor. Releasing the `user` lease releases an anchor the
  claim created only while the anchor's keys still equal the keys the sidecar stored, its `absorbs` is empty, and no
  other live `user` lease names it as anchor. Any later call that reports the anchor as "already covered" clears its
  "created" flag. An anchor that was already held, widened by `--replace`, merged, shared, or reported as covering
  stays, and the release output says so
- A `user` or `shared` lease is broken the way NIT-001 breaks a lease. `break` from any machine in the scope refuses
  without `--force` while the holder is alive or unknown. With `--force`, the broker ends the lease, and the holder's
  node writes it a tombstone-like notice that names the breaker. The anchor stays as an ordinary lease, and the notice
  says so. Under NIT-006's "What auth authorizes", a user may break only their own leases, and the broker owner any.
  A `taken` or `ended` fact stays until its owner's node acks it, so a `break` from any other node answers that the
  lease already ended and changes nothing. Overlap reports between `user` leases and mirrored `machine` leases go only
  between leases of one user. Amended 2026-10-29; see Decision Log
- The broker never refuses a `user` claim because of a mirrored `machine` lease. No `machine` claim consults the
  broker. Mirrored machine leases carry derived entries in a sidecar keyed by lease ID in a directory V1 never scans.
  The CLI writes it after a `machine` claim whenever the user has a node config file, even if the node is down. The
  node derives for a lease with no sidecar for its current keys, a V1 record included, and removes the sidecars of
  ended leases. A derivation that fails shows as unknown in discovery, never as no overlap. Amended 2026-10-30; see
  Decision Log
- Overlaps between `user` leases and mirrored `machine` leases appear in discovery, as a warning in the output of a
  `user` or `shared` `acquire`, and as notices to new-binary holders on both sides. A `machine` holder learns of an
  overlap only by notice, since a `machine` claim never waits on the broker. A machine lease's holder is a new-binary
  holder when the CLI wrote its derived-entries sidecar. A record a V1 binary wrote gets no notice. Amended
  2026-10-30; see Decision Log

A `user` lease then protects against other `user` claimants everywhere, and against V1 sessions in the holder's own
checkout. A V1 session in a sibling worktree, and any `machine` claim on another machine, are warned about, never
refused. Unchanged callers get no cross-machine protection until they use `user` reach.

The paired lease without the anchor rule was rejected. A `--replace` or a V1 binary could narrow it while the `user`
lease lived on, and a second local session could then claim the uncovered files. Cross-refusal through the broker was
rejected. A guardian's scope-less lease on the main checkout claims one working tree, but its derived `repo:<roots>#`
covers the repo's files. Refusing on it would block every worker in a sibling worktree, the pattern ADR-03 exists to
allow. A refusal of `machine` claims on broker data also adds the second decider "Source of truth for machine leases"
rejected, and it fails open on stale data. Paired leases in every sibling worktree were rejected. The main checkout's
paired lease is refused against the guardian's own lease. Implication was rejected, since every `machine` claim would
need the broker. Independence with or without reporting was rejected. A V1 session in the `user` holder's own checkout
could claim the same files.

### Discovery queries

The research names two query shapes. It does not define what "who can do Y" matches against. The record's
`description` field is free text, so a capability query may need a structured field.

Decision (autonomous): "who can do Y" is answered by two exact queries over fields records already carry. Capabilities
with no path behind them are deferred. That narrows the settled "Discovery by query", and waits for the user at the
review gate.

**Revisit:** when a named caller needs a capability with no path behind it, such as "can review Go"

- "Who holds X" takes a repo name, a local path, a resource name, or a qualified name. It returns every holder whose
  keys overlap X, with its qualified name, reach, and whether it is live, lapsed, mirrored, stale, or unknown. A
  mirror is unknown when the broker cannot judge it: its record is unusable, or its repo entries are missing or
  failed. Such a mirror is listed for every repo-name and resource query, never dropped. A local path in a checkout
  becomes a repo name and a machine-scoped path. A local path with no checkout behind it, such as an installed plugin
  copy, searches only this machine's mirrored leases, and the answer says so and suggests the name lookup. The
  commands are `nitipan who holds <X>` and `nitipan who is <N>`, each with `--json`. Amended 2026-10-30; see Decision
  Log
- "Who is named N" matches `holder.name` or `holder.handle` exactly, across every live, stale, and lapsed lease on
  every machine. A name lookup is not a path query, so it crosses the per-machine scoping of mirrored paths
- Both queries also accept a NIT-007 mail address, and every answer carries each holder's mail address beside its
  qualified name. A Claude Code holder whose record has no `session_id` shows as not mailable. Amended 2026-10-24; see
  Decision Log
- Names are never forced unique. The lookup returns every match and flags a name shared by more than one holder. It
  never picks one. The guardian protocol already says to stop and ask the user then
- A session that holds no lease cannot be found by either query
- Under NIT-006, another user's `user` leases and mirrored `machine` leases are hidden, unless the asker's label holds
  that user's delegation, and a name lookup across users matches only `shared` leases. See NIT-006's "What auth
  authorizes". Amended 2026-10-24; see Decision Log

The guardian protocol finds a guardian by domain and by name. These two queries serve both routes. Its leading example,
a defect in an installed plugin copy, has no checkout to derive a repo name from, so only the name route reaches it.

A name lookup works only for a guardian that advertises its name and holds a lease on its domain. Neither is in the
guardian protocol today. Those changes belong to coding_agent_standards, through its guardian.

A structured capability field was rejected. No current caller asks for tags, and an older node mirrors a record
without the field it does not know, so the query misses the holder silently. A substring search over descriptions was
rejected. It misroutes requests to sessions whose descriptions happen to match. Answering capability only through
"who holds X" was rejected. It returns nobody for the guardian protocol's leading case. Moving all of "who can do Y" to
a later proposal was rejected. It also drops the name route, which has a current caller.

### Broker lifecycle and store

Nothing says how the broker runs or what survives its restart. Live leases, request outcomes, queued events, the
seen-label table, and NIT-005's counter must not be lost, or a retried request can grant twice.

Decision (autonomous): the broker is `nitipan broker`, run on a host the user picks under a launchd LaunchAgent that
`setup` prints. Its listen address, certificate, and store path are in a broker config file at
`~/.config/nitipan/broker.json`, outside the state directory and read like the node config. All broker state lives in
one versioned JSON file, rewritten whole through a temp file, fsync, and `rename(2)` for each change. Amended
2026-10-29; see Decision Log.

- The file holds live and lapsed leases with their grants, revisions, tokens, and anchors; the "taken" markers; request
  outcomes; end facts the holder's node has not acked; the seen-label table; and NIT-005's counter
- A change is built as a new state, persisted, then swapped into memory, then acknowledged. A failed write or
  directory sync is fatal. The broker exits, and launchd restarts it from the file on disk
- A grant, the "taken" mark on any lapsed lease it overlaps, its request outcome, and its counter bump go in one write
- The broker takes a flock on its store. A second broker on the same store fails loudly. At start it removes its own
  leftover temp files
- The store path must be on a local volume. The default is under `~/Library/Application Support/nitipan`. A path on a
  network share or in an iCloud-synced folder is refused, since fsync there may not reach the disk
- Expiries are persisted as wall-clock times for display. On load, every live lease is rebased to the start time plus
  one term, which is the settled restart grace. In process, deadlines run on `CLOCK_MONOTONIC`
- Request outcomes are kept for 7 days. The node refuses to forward a request more than one day old by its own clock,
  which is the same machine's clock the CLI used, with a "request expired" error. A retry within the window always
  finds its outcome
- No general event queue is persisted. A reconnecting node resyncs from a snapshot, and the snapshot carries the
  unacked end facts, so "lost to X" and "ended by break" survive a restart
- NIT-007's mail is the exception. The file also holds each message queued for a node until that node acks it, the
  stage and bounce state of each message, each label's pinned key and approval, and the handle list each node last
  reported. A message body has a fixed size cap, so the whole-file rewrite stays small. Amended 2026-10-24; see
  Decision Log
- Before migrating the store to a new major, the broker keeps the previous file beside it, so an older broker can start
  from it after a rollback
- NIT-006 adds its state to the file as a new major: a hash of each pinned key in place of the key, the owner ID,
  admissions, user bindings, delegations, mail grants, and cross-user conversations. Its "Node credential" sets how
  the kept previous file is treated after forget and revoke. Amended 2026-10-24; see Decision Log

One file means one atomic write covers a grant, its request outcome, and its counter bump together. It reuses the
temp-file pattern `internal/state` already uses, and ADR-04's evolution rule for JSON. The scale is a handful of
machines in one user scope, renewing every 100 seconds, so rewriting the whole file costs nothing that matters.

Restoring an older file, or reinstalling with a fresh store, restarts the counter below tokens already seen. NIT-005's
"Counter durability" settles that. The broker puts no floor on the counter at load. Every new token is floored at the
wall clock instead, and a bounded report raises the counter. Amended 2026-10-29; see Decision Log.

An append-only log with snapshots was rejected. It adds torn-tail detection, a crash-safe order for compaction, and
idempotent replay, each a new place to be wrong, to save write volume at a scale nobody named. SQLite was rejected. It
adds a large dependency and a schema outside ADR-04's rule, and on macOS it needs extra pragmas to reach the disk.
Rebuilding leases from nodes after a restart was rejected. A machine asleep through the grace loses its lease, another
machine is granted the keys, and the sleeper's retried request then replays "granted" for keys someone else holds.

### Version skew

The broker, each node, and each nitipan binary on PATH are separate deploy units. V1 binaries stay on PATH forever.
Every contract between them tolerates any version of the other side. No decision here relies on a deploy order.

The rule for every contract is ADR-04's minor rule, applied to messages: a field may be added only if its absence
never makes the reader more permissive. A field that fails that test takes a new message version, which an older
reader refuses.

Node and broker, over the protocol in "Broker protocol":

- Old node to new broker: the node reports its version at registration. The broker keeps serving every path prefix a
  registered node still uses. A field an older node never sends reads as its strictest meaning. Missing derived entries
  in a mirror read as unknown, never as no overlap. A `user` request with no anchor ID, or with a derivation version
  below the broker's floor, is refused
- New node to old broker: the node learns the broker's version at registration. A request kind the broker does not
  know is answered with an error the CLI reports as "broker too old", never dropped silently. Unknown fields are
  ignored only when the minor rule allows it

CLI and node, over the request handoff in "How sessions reach the broker":

- New CLI to old node: the node publishes its version. A request kind, or a request version, the node does not know
  gets "node too old" at once. A new field on an existing kind that a node could drop takes a new request version, so
  an old node refuses it rather than forwarding a request without it
- Old CLI to new node: the node keeps answering every request version it ever accepted
- V1 binaries never talk to the node. Their records are mirrored, and the node derives their wider-reach entries
- Sidecars, presence records, and deadline anchor files carry `major.minor` versions under ADR-04's reader rules. One
  the node or CLI cannot use reads as unknown, which means unchecked, never as absent

Derivation:

- Wider-reach entries are derived by the CLI, or by the node for a V1 record, and compared by the broker. The request
  carries the derivation version, and the broker refuses one below its floor. Raising the floor locks older CLIs out
  of `user` claims. That fails closed, and an upgrade fixes it
- Machine labels are derived only by the broker, so nodes on different versions cannot disagree

Mail, under NIT-007:

- Mail, stage updates, and bounces are event kinds the broker sends only to a node whose registered version supports
  them. For an older node it keeps the message and tells the sender "recipient node too old". A newer node facing an
  older broker reports "broker too old" for mail. Amended 2026-10-24; see Decision Log

Auth, under NIT-006:

- NIT-006's "Version skew" covers its contracts: bindings, claims for another user, cross-user mail and replies, and
  its store major. Amended 2026-10-24; see Decision Log

A node's own versions never overlap. One node runs per machine and OS user under its flock, and it exits when its
executable changes, so launchd restarts it on the new binary.

The broker's store under "Broker lifecycle and store" carries a `major.minor` version. An older
broker refuses to start on a higher major, so a rollback across a breaking change needs the matching state backup. A
minor change is additive, and an older broker ignores what it does not know only when the minor rule allows it.

### Shared agents and acting_for

A shared agent serves several users and holds leases in the requesting user's scope. Its record's `acting_for` differs
from its `holder` user.

- The lease counts against the requesting user's overlaps.
- It counts against the agent's own overlaps.
- It counts against both.

Decision (autonomous): a decision to defer. Before NIT-006, every option gives the same answer, since one broker is one
user scope. The question moves to NIT-006, beside "What auth authorizes", as an open question that blocks NIT-006's
acceptance.

Amended 2026-10-24; see Decision Log. NIT-006 settled the deferred question: a shared agent's lease counts in the
requesting user's scope, named by `acting_for`, and only under that user's standing delegation. A legacy `acting_for`
is replaced by the node's bound user. See NIT-006's "What auth authorizes" and "Shared agents and acting_for".

- The real choice needs an authorization rule: whether an agent may claim in a user's scope without that user's
  approval. NIT-006 owns that rule
- NIT-006 M1 refuses a record whose `acting_for` does not match the authenticated identity. Read literally, that
  refuses every shared agent's record. The moved question must settle what "match" means before M1 is built
- Every CLI before NIT-006 writes `acting_for` as the OS user name. That value is not an authenticated identity, and
  OS user names differ across one person's machines. Whatever NIT-006 picks must treat a legacy `acting_for` as
  unauthenticated, for example by mapping it through the node's authenticated user

This leaves the scope semantics and the wording of NIT-006 M1 undone in NIT-004.

Counting against the requesting user's scope, the agent's, or both were each rejected for now. Each gives a meaning to a
field nobody can authenticate yet, and commits NIT-006 to it before its threat model exists. The agent's own scope also
inverts the research's model, under which the agent holds leases in the requesting user's scope. Refusing every
mismatched `acting_for` until NIT-006 was rejected. No code path writes one, so the refusal guards against no failure.

## Design Decisions (Open)

None.

## Risks

- **A partitioned holder keeps working after its lease expires** — likelihood: medium, impact: high.
  An expiring lease alone cannot stop a holder that lost contact with the broker. It may commit or push after another
  session takes the lease. An idle holder learns of a lapse only on its next nitipan call. Mitigation: commands say
  when broker leases are at risk or lapsed, every lapse and loss writes a notice, and revival keeps a sleeping
  laptop's leases when nobody claimed them. NIT-005's fencing tokens stop the stale action itself. Until NIT-005
  lands, nothing refuses it.
  **Revisit:** when NIT-005 is accepted
- **The broker is a single point of failure** — likelihood: medium, impact: medium.
  If the broker is down, no session can claim a cross-machine lease. Accepted: one broker is simple, and `machine`
  leases keep working locally. Replication can come later as its own proposal.
- **The broker is open before NIT-006** — likelihood: medium, impact: high.
  With no auth, any host that reaches the broker can claim, break, or read every `user` and `shared` lease. It can also
  register as a sleeping laptop's machine and uid once that laptop's stream is no longer live, and send an empty
  snapshot. Mitigation: the broker serves HTTPS when given a certificate, plain HTTP only on a loopback or private
  address, and the node warns on plain HTTP. The instance key makes a takeover need a file in the user's 0700 state
  directory. The environment is high-trust, which the research and NIT-006's "No auth at first" accept. NIT-006, now
  accepted, closes it once built: labels bind to users, unbound labels are refused once cross-user features are on,
  and authority needs a proof no session can make, under ADR-08. Amended 2026-10-24; see Decision Log.
- **Wider-reach names are a contract** — likelihood: high, impact: medium.
  Once guardians and scripts store `repo:` names, changing the identity breaks them. "Name syntax for wider reach" is
  settled. Mitigation: the stored form carries the whole root set, the request carries a derivation version that the
  broker can floor, and ADR-05 governs any migration of the identity.
- **Machine labels are a contract** — likelihood: high, impact: medium.
  Once NIT-007 addresses are stored, changing the label derivation strands every one of them. Mitigation: only the
  broker derives labels, and the seen-label table maps each label to its machine ID and uid, so a later derivation can
  keep old labels valid. ADR-05 governs any migration of the label.
- **A broker store rollback across a major loses data** — likelihood: low, impact: high.
  An older broker refuses a higher-major store, so rolling back an upgrade needs the old file. Mitigation: before a
  major migration, the broker keeps the previous file beside the new one, and an older broker can start from it.
  Grants made after the migration are lost on rollback, and the restart grace lets every node renew.
- **The design depends on NIT-005 and NIT-006, which are still in design** — likelihood: medium, impact: medium.
  NIT-005 relies on revival keeping the token, on the synchronous renew, and on the store keeping its counter. Its
  counter durability after a restore is settled there: tokens floor at the clock, and a bounded report raises the
  counter. Amended 2026-10-29; see Decision Log. NIT-006, accepted on 2026-10-24, settled the `acting_for` scope
  question and the node credential, and amended the decisions its Impacts entry here names. A change there can reopen
  a decision here. Mitigation: each reliance is written in this proposal's
  Impacts and in the decision it rests on. NIT-005's working copy still says NIT-004 has not settled the term, which
  needs a fix in NIT-005.
- **Unchanged callers get no cross-machine protection** — likelihood: high, impact: medium.
  A V1 caller keeps working, but only `user` claims coordinate across machines. A guardian found by name must also
  advertise its name and hold a lease on its domain. The guardian protocol does neither today. Mitigation: the needed
  changes go to coding_agent_standards through its guardian. Discovery and warnings show cross-machine overlaps in the
  meantime.
- **macOS privacy controls block the node's git runs** — likelihood: medium, impact: low.
  A LaunchAgent running git in a checkout under `~/Documents` or `~/Desktop` may be refused. Then a V1 record's derived
  entries cannot be computed. Mitigation: new binaries derive in the CLI, which runs in the agent's shell, and a failed
  derivation shows as unknown in discovery, never as no overlap.
- **The broker needs a field V1 records lack** — likelihood: low, impact: low.
  The research's claim that V1 carries every field is unproven. Mitigation: everything new lives at the broker or in
  sidecars in directories V1 never scans, so path records keep their V1 version. An anchor keeps its V1 version too.

## Milestones

Amended 2026-10-24; see Decision Log.

### M1: Node, broker, and mirrors

**Status:** DONE

- `setup` prints a per-user LaunchAgent that runs `nitipan node`, and a second node for the same machine and user fails
  on its flock
- Every connection between a node and the broker is opened by the node, and broker events reach the node over its
  stream and stay queued until acked
- A node registers with its machine ID, uid, and instance key, and gets its four-word label. A reconnect with the same
  key replaces a half-open stream, and a different key while the old stream is live is refused
- Two OS users on one Mac run two nodes with two labels, and neither user's snapshot replaces the other's
- The node mirrors every local `machine` lease, V1-written ones included, as a snapshot on each connect. Two machines
  with the same checkout path never meet through their mirrors, and a node's mirrors read stale one term after its
  stream closes
- A record written by a V1 binary is mirrored with no change to the record. A record the node cannot use is mirrored as
  an unknown holder with an `all` footprint
- The node sends a new snapshot when `leases/` changes, and at least once per heartbeat
- A command that needs the broker fails at once with "node not running" and the install text when no live node exists
- The node reads the broker address and proxy from its config file, and exits when its executable changes
- The node refuses to start when the machine ID cannot be read. An address with a label the broker never saw is refused,
  and two seen labels that collide are refused with both machines named
- A lock holder reaps requests and replies whose requester is dead
- `setup` prints a LaunchAgent for `nitipan broker`. A second broker on the same store fails on its flock, and a store
  path on a network share or synced folder is refused
- Without a certificate, the broker refuses a public or wildcard listen address, and the node refuses plain HTTP to a
  public address. With one, the node verifies the broker's certificate
- A node's mirrors read stale when its stream has had no heartbeat for one term, even while the connection stays open
- A node refuses to forward a request more than one day old with "request expired"
- Before a major migration of its store, the broker keeps the previous file, and an older broker starts from it

**As built:**

- The node config moved out of the state directory. See the 2026-10-29 Decision Log entry.
- Running the printed LaunchAgents under real launchd, with `launchctl bootstrap`, is left to the user. Tests cover
  the node and broker in-process, the flocks, and the printed plist text.
- The broker and node tests run over `httptest` with fake clocks. Two OS users on one Mac are tested as two uids
  against one broker. Two real node processes cannot be tested that way, since the state directory must be owned by the
  calling user.
- The node mirrors a record unchanged on disk and in meaning. JSON transport compacts its whitespace.
- The store keeps a hash of each node key, never the key, and the derivation version beside each label. The kept
  previous file is named after the store with the major it held, such as `broker.json.v1`.
- The only request kind the node forwards is `ping`, which echoes the node's label. M2 adds the real kinds. No
  command calls the "node not running" check yet, and its error exits with status 1.
- The node reads NIT-008's `recalls/` only when the directory exists, and changes nothing yet.
- Plain HTTP to a literal public IP is refused when the node starts. A host name is allowed, and the node's dialer
  checks the address the name resolves to just before it connects, refusing a public one. This also covers a name whose
  answer changes later. With a proxy configured, the dialer judges the proxy's address. The node warns on any plain
  HTTP.
- The broker refuses a store file with a higher minor at start, naming both versions, since it cannot rewrite one and
  could not register a node against it.
- The store path check refuses `~/Library/Mobile Documents`, `~/Library/CloudStorage`, which covers iCloud Drive,
  Dropbox, Google Drive, OneDrive, and Box on current macOS, and the legacy `~/Dropbox`, `~/Google Drive`,
  `~/OneDrive*`, `~/iCloud Drive`, `~/Box`, and `~/Box Sync` folders. It matches by path only. A sync tool pointed at
  an arbitrary folder, and iCloud's Desktop and Documents sync, are not detected.
- A node call other than the stream has a 30-second deadline, so a stalled broker ends the session and the node
  reconnects. A claimed, answered, or pending handoff file that names no requester is reaped once it is over a day old.
- The volume check off macOS always fails, since the broker runs as a macOS LaunchAgent.
- The node follows no broker redirect, since Go would re-send the credential over plain HTTP. A handoff file whose
  body ID differs from its file name is refused and reaped after a day.

### M2: Wider-reach leases

**Status:** DONE

- `user` claims from checkouts of one repo on two machines derive overlapping root-set names and conflict. Shallow
  clones, an unborn HEAD, a graft file, and an uninitialized submodule are refused with their errors
- A claim travels through the request handoff and back. A CLI that timed out gets the outcome as a notice, and a retry
  of the same request never grants twice
- A `user` claim takes or reuses a local anchor lease. Ending or narrowing the anchor, `--replace` from a V1 binary
  included, ends the `user` lease within one term, and the lease is never revived
- The node renews every local holder's broker leases in one heartbeat, and sessions make no heartbeat calls. When a
  holder dies, the broker ends its leases before the term runs out
- In tests with a sleep-counting fake clock, a lease that lapsed during sleep is revived with a notice when nobody
  claimed it, and reports "lost" with the new holder when someone did
- A lease that stayed lapsed for longer than 12 terms is still revived when nobody claimed its keys. A renewal of a
  lease the broker no longer has returns "not found" and writes a loss notice
- A holder whose liveness reads unknown keeps its broker leases renewed, and discovery shows it as unknown
- A lapse event that arrives after the revival's reply does not override it, and two lapses before the holder's next
  call give two notices
- Releasing a `user` lease releases an anchor its claim created and left unchanged. It leaves an anchor that was
  already held, widened, merged, or shared with a second `user` lease, and says so, and the second lease survives
- `break --force` of a `user` lease from another machine ends it, and the holder gets a notice naming the breaker
- A `user` lease and a `shared` lease of different holders on overlapping names conflict. Amended 2026-10-29; see
  Decision Log
- A command run while the node is disconnected says the holder's broker leases are at risk and when they lapse, and
  past that time says they lapsed
- A synchronous renew through the request handoff returns the lease's new expiry
- After the broker restarts, it expires nothing for one term, and every lease and request outcome it acked survives
- Old CLI with new node, new CLI with old node, old node with new broker, and new node with old broker each give the
  error "Version skew" states, in tests. A derivation version below the broker's floor is refused. A sidecar or anchor
  file the reader cannot use reads as unknown

**As built:**

- The CLI generates one ID that is both the request ID and the lease ID. A command whose reply the CLI did not see
  leaves an in-flight record under `broker/inflight/`, so a retry of the same command reuses the ID and the broker
  replays its outcome. The handoff request version is now 2, so a CLI of this build gets "node too old" from an M1
  node at once.
- The node delivers a reply nobody read within 90 seconds as a notice and removes the reply file. Its memory of whom to
  notify is per process, so a node restart in that window drops the notice. The retry still returns the same answer.
- Synchronous renew is a `renew` subcommand over a `renew` request kind. Plain `list` also shows the broker leases,
  through a `list` request kind, and falls back to the holder's own sidecars with an `unknown` state when no live node
  answers. `list --json` gained `broker_leases`, so its schema moved to 1.4.
- The store keeps the 1.0 version. The grant, the outcome, and the counter ride in the `leases`, `outcomes`, and
  `counter` fields M1 reserved. A taken or broken lease stays in `leases` with state `taken` or `ended` until its node
  acks it in a snapshot. The counter bumps with each grant, and the grant's token was the counter value. Revival keeps
  it. NIT-005 M2 then floored each token at the wall clock and sets the counter to the token. Amended 2026-10-29;
  see Decision Log.
- The snapshot answer carries the node's leases, end facts included, and the snapshot request carries the acks. An
  older node ignores the answer, and an older broker answers with no body, which a node accepts.
- A holder never conflicts with its own leases at the broker. See the 2026-10-29 Decision Log entry on M2.
- A break from a node other than the owner leaves a `taken` or `ended` fact for its owner to ack. See the row review
  entry in the Decision Log.
- The node renews at a third of the term, checks anchors and holder liveness in the same pass, and treats a gap
  between loop ticks larger than 5 seconds as a wake. The broker detects its own wake by comparing the monotonic clock
  with Go's clock, which stops during sleep.
- A lapsed lease whose anchor was merged into another lease by `--replace` has an anchor ID the snapshot no longer
  lists, so the broker removes the lapsed lease. A live one survives, since the node follows `absorbs`.
- A late refusal leaves an orphan record under `broker/orphans/`, keyed by the claim ID that its notice
  targets. The next command that delivers the notice releases the anchor under the local lock, by the rules a release
  uses. A refusal the CLI sees at once, and any definite failure, releases a new anchor on the spot. Only an unknown
  outcome keeps it.
- A release the CLI did not see answered keeps the sidecars it asked to remove in its in-flight record. A retry reads
  them, so it still decides about a created anchor even though the node already removed the sidecar.
- The node removes a sidecar for a release only when the holder key matches the requester's. A repeat claim that the
  broker answers with the held lease writes no second sidecar, and an existing sidecar keeps its created flag.
- A stored outcome is bound to the node that made the request, and another node's retry of the ID is refused. The
  broker refuses a claim whose derivation is above the highest it knows (1), as "broker too old", and below its floor.
- A request kind introduced after request version 1 is refused from a version 1 request as "node too old".
- The "restored" notice fires when the node's own deadline passed with no acked renewal and a later renewal or snapshot
  shows the lease live.
- The root-set cache is a JSON file at `broker/rootsets.json`. A submodule entry covers the whole submodule, and a claim
  inside a submodule emits its own entry first, then each superproject's.

### M3: Discovery queries

**Status:** DONE

- "Who holds X" for a repo name, a checkout path, a path outside any checkout, and a resource name returns every
  overlapping holder across machines, with its qualified name, reach, and whether it is live, lapsed, mirrored, stale, or
  unknown. Amended 2026-10-30; see Decision Log
- "Who is named N" returns every holder with that name or handle on every machine, and flags a duplicate
- Overlaps between `user` leases and mirrored `machine` leases appear in discovery, as warnings on `user` and `shared`
  claims, and as notices to new-binary holders on both sides

**As built:**

- The commands are `nitipan who holds <subject>` and `nitipan who is <name>`, each with `--json`. The JSON carries
  `schema_version` 1.0 and the stored root set of each holder. `list --json` is unchanged, so its schema stays 1.5.
- The handoff request version is now 3. Two request kinds, `who-holds` and `who-is`, need it. A CLI of this build gets
  "node too old" from a node of version 2 for every request, since the version is global. A node restarts on the new
  binary by itself. An older broker answers 404 to the two new paths, which reads as "broker too old".
- Mirrors carry their derived entries, with a state, the derivation version, the entries, and a reason. A mirror
  without them reads as unknown. The node keeps them in a directory V1 never scans, keyed by lease ID, with the keys
  they were derived from. A file for other keys than the record's current ones counts as absent. A file of a higher
  major is left alone and reads as unknown. A mirror derived below the broker's derivation floor reads as unknown too,
  as one above the highest derivation it knows does.
- A key outside any checkout derives to no entries, which overlaps no repo. Every other refusal is a failed derivation.
  A failure the node recorded is tried again after an hour. A failure the CLI recorded is not.
- A snapshot spends at most 5 seconds starting derivations, since it runs on the loop that heartbeats and renews. A
  lease left over reads as unknown and is derived by a later snapshot. A single walk can still take its own timeout.
- `who is` lists a mirror with an unreadable record as unknown and notes that the holder count may be low. Such a record
  names no holder, so it cannot be matched to the name.
- A file the CLI wrote while the node was deriving wins over the node's. The notice rule below depends on it.
- Unknown mirrors are listed for every repo-name and resource query on every machine. Their number is stated in the
  answer's notes. A path query judges the asker's machine by keys, with no checkout-boundary exemption, so a sibling
  worktree's lease is listed.
- The overlap pair ID is the grant's lease ID and the mirror's lease ID. The broker leaves out the holder's own anchor,
  the holder's other leases on the same node, and sub-leases delegated from the anchor. It reports an overlap with a
  stale mirror, marked stale.
- The notice reaches a node in the snapshot answer, which now carries `overlaps` and is sent when it has either leases
  or overlaps. The node marks each pair and side once per process, so a node restart can repeat one notice that its
  holder has not yet read. The claim reply carries the same overlaps. The node marks the grant side as seen once the CLI
  has read the reply, so the claimant gets a warning and no notice for it. A reply left unread past the grace means no
  warning was printed. The snapshot answer then writes the notice, so the grant's holder still hears of the overlap.
- Not covered: a refused claimant's transient anchor is a machine lease that overlaps the live grant it is refused for.
  A snapshot that lands before the broker answers the claim can report that pair, and the grant's holder gets a notice
  about a lease released moments later. The window is wider when the outcome is unknown and the anchor stays. The CLI
  writes no derived file for the anchor, so no mirror-side notice goes out. The broker cannot tell the anchor from any
  other machine lease before the claim reaches it, so excluding it after a refusal would leave the main window open.
- The node's own derivation is not retried on a V1 record's behalf more than once an hour after a failure. A machine
  lease whose derived file the node wrote gets no overlap notice. That is how a V1 holder is told from a new-binary one.
- Not covered: a mail address in either query, which NIT-007 adds, and the cross-user hiding NIT-006 adds. Running the
  commands against a real launchd node and broker is left to the user.

## Decision Log

- 2026-10-18: Drafted from the lease broker research. Settled decisions are limited to the roadmap the user approved
  that day.
- 2026-10-18: The user settled three decisions. Machines connect out to the broker, and the broker never connects in.
  One broker serves every machine. Names are qualified per layer, with a machine part added by the broker. The open
  question "Broker availability" was removed, since a single broker settles it. Systems like etcd and Consul are too
  complex for nitipan.
- 2026-10-18: ADR candidate (cuts across the system): machines connect out to the broker, and the broker never connects
  in. Written as ADR-01.
- 2026-10-18: Added the open questions "The machine part of a qualified name" and "Source of truth for machine leases".
- 2026-10-24 (autonomous): "Source of truth for machine leases" settled. The state directory decides `machine` leases,
  and the node mirrors them to the broker as per-machine snapshots. The broker decides `user` and `shared` leases.
  Converged in 1 pass.
- 2026-10-24 (autonomous): "Broker protocol" settled on HTTP/1.1 with JSON and a server-sent events stream, with
  application-level acks and per-lease revisions. Converged in 1 pass.
- 2026-10-24 (autonomous): Added "Node scope", "How sessions reach the broker", and "Node lifecycle" as open questions.
  The critic found that the state directory is per OS user while the node was per machine.
- 2026-10-24 (autonomous): "Node scope" settled on one node per machine and OS user. Converged in 1 pass. This amends
  the user-settled "Per-machine node", which said one node per machine. That text now says per machine and OS user,
  and points to "Node scope".
- 2026-10-24 (autonomous): "How sessions reach the broker" settled on a file handoff to the node through a new
  directory under the state directory. Converged in 1 pass. "Source of truth for machine leases" said nothing the
  broker holds is ever written into a state directory. It now says a state directory that V1 scans, since a reply file
  carries a grant.
- 2026-10-24 (autonomous): "Node lifecycle" settled on a per-user launchd LaunchAgent. Converged in 1 pass.
- 2026-10-24 (autonomous): "Lease term and renewal" settled on a fixed 5-minute term, a sleep-counting monotonic clock,
  and broker-side revival of a lapsed lease with a notice to the holder. Converged in 1 pass. The 5-minute value is the
  reviewer's pick, not the critic's.
- 2026-10-24 (autonomous, judge): "The machine part of a qualified name" settled on a four-word label derived from
  machine ID and uid, with a validation-only table of seen labels and a node instance key. Judge after 3 passes, on
  the pass budget. Each pass added a refinement that answered a new failure. The judge refuted one supporting claim,
  that the instance key detects cloned VMs.
- 2026-10-24 (autonomous, judge): "Name syntax for wider reach" settled on root-commit-set identity, a
  `repo:<root-sha>#<path>` text form, and the whole set in the stored form. Judge after 3 passes, on the pass budget.
  The critic's top moved from a root set, to the set with a text form, to the set in the stored form. The rule that a
  claim must come from a checkout is the reviewer's, on one of the judge's required caveats.
- 2026-10-24 (autonomous): Added the open question "Which user a user-reach lease belongs to". OS user names can
  differ across one person's machines.
- 2026-10-24 (autonomous): "Which user a user-reach lease belongs to" settled on one user scope per broker until
  NIT-006, with `user` and `shared` leases conflicting on overlapping names. Converged in 1 pass. Revisit when NIT-006
  M1 lands.
- 2026-10-24 (autonomous): "Shared agents and acting_for" settled as a decision to defer to NIT-006, beside "What
  auth authorizes". Converged in 1 pass. The revisit hook on "Which user a user-reach lease belongs to" moved from
  "when NIT-006 M1 lands" to "when NIT-006 is accepted". M1's refusal of a mismatched record depends on the answer, so
  the answer must come first. Adding the question to NIT-006 itself waits for the user.
- 2026-10-24 (autonomous, judge): "Machine leases and user leases on the same path" settled on a `user` lease anchored
  to a paired local `machine` lease, with no cross-refusal and overlaps reported. Judge after 3 passes, on the pass
  budget. The critic's top moved from cross-checks, to paired leases in every worktree, to the anchor rule. The judge
  left two caveats: how revival is blocked once the anchor ends, and what renewal does when it cannot compute the
  anchor's coverage. The reviewer settled both: an explicit end plus a broker-side anchor check, and ending the lease
  with a notice.
- 2026-10-24 (autonomous): "Discovery queries" settled on "who holds X" plus an exact name lookup across machines.
  Converged in 1 pass. Path-less capabilities are deferred with a revisit hook. That narrows the user-settled
  "Discovery by query", and waits for the user at the review gate.
- 2026-10-24 (autonomous): "Expiring leases" amended to apply to broker-granted leases. It had said the rule replaces
  NIT-001's dead-holder rule outright, which "Source of truth for machine leases" contradicts. "Qualified names" gained
  a pointer to the handle and the label. The Summary and Impacts were brought in line with the settled decisions. A
  "Version skew" section was added, since several decisions cite it.
- 2026-10-24 (autonomous): Milestones rewritten to match the settled design. M1 had said the broker grants, renews,
  and expires `machine` leases, and M3 had named a capability rule. No queue row names them, so they were renumbered
  freely. M1 is now the node, broker, and mirrors; M2 the wider-reach leases; M3 discovery.
- 2026-10-24 (autonomous): Coherence fixes from the whole-proposal critic pass. Mirrors are sent on every change to
  `leases/`, and an unusable record mirrors as an unknown holder. Unknown liveness keeps renewing. The revision now
  rises on every state change, and revival keeps the grant and token instead. Notice IDs carry the revision. A lapsed
  lease had been dropped after 12 terms, which an overnight sleep would always hit. It now stays until the holder's
  snapshot omits it, or its label is unseen for 30 days. A renewal of a lease the broker lost returns "not found".
  Stream liveness is a heartbeat within one term. Releasing a `user` lease releases an anchor its claim created.
  Remote `break` follows NIT-001. `acquire` warnings apply only to `user` and `shared` claims. Subtree merges were
  added to the accepted limits. The milestones gained checks for every settled behavior that had none.
- 2026-10-24 (autonomous): Added the open question "Broker lifecycle and store". Nothing said what survives a broker
  restart.
- 2026-10-24 (autonomous): "Broker lifecycle and store" settled on one versioned JSON file, rewritten atomically
  under a flock, with persist-then-ack. The broker runs as a LaunchAgent. Converged in 1 pass. NIT-005's counter floor
  after a restore or reinstall stays with NIT-005.
- 2026-10-24 (autonomous): Risk review. Stale entries were updated: the name syntax is settled, and sidecars answer
  the missing-field risk. The partitioned-holder risk is mitigated by at-risk output, notices, and revival until NIT-005
  lands, with a revisit hook. Added risks for the open broker before NIT-006, the label contract, a store rollback
  across a major, the NIT-005 and NIT-006 dependencies, unchanged callers, and macOS privacy controls. For the open
  broker, "Broker protocol" gained HTTPS support and a rule that plain HTTP is only for loopback or private addresses.
  For the rollback, the broker keeps its previous store file before a major migration. No risk is rated high
  likelihood and high impact.
- 2026-10-24 (autonomous): Re-check fixes. Releasing a `user` lease releases a created anchor only while its keys are
  unchanged and it absorbed nothing. Before, a widened or merged anchor would have been released whole. The broker
  itself refuses plain HTTP on a public or wildcard listen address. Before, only the node checked the address it
  dialed. Mirror staleness now follows the heartbeat rule, not the connection closing. A "taken" marker is an unacked
  end fact, dropped on ack. Milestones gained the checks the re-check found missing.
- 2026-10-24 (autonomous): Third proposal-loop pass fixes, not re-checked since the loop's pass budget ran out. A
  created anchor shared by a second `user` lease, or reported as covering by a later call, is no longer released with
  the first `user` lease. Before, releasing one `user` lease could end another. A "not found" renewal for a lease the
  node already holds as ended or taken writes no second notice.
- 2026-10-24: Review gate: kept all. Nothing reopened. The user kept the amendments to "Per-machine node" and
  "Expiring leases", the narrowing of "Discovery by query", the deferral of "Shared agents and acting_for" to NIT-006,
  and the milestone rewrite. The user asked to add the deferred question to NIT-006, and to send the guardian protocol
  changes to the coding_agent_standards guardian.
- 2026-10-24: NIT-001's revisit hook on "Wider-reach lease names" fires now that "Name syntax for wider reach" is
  settled. NIT-001 is implemented and frozen, so its text is left as is.
- 2026-10-24: ADR candidate (hard to reverse): wider-reach `repo:` names and their root-set identity. Declined on
  2026-10-24: a syntax and mapping are implementation, not architecture. The principle behind it is ADR-05.
- 2026-10-24: ADR candidate (hard to reverse): the machine label derivation, a four-word hash of machine ID and uid.
  Declined on 2026-10-24 for the same reason. The principle behind it is ADR-05.
- 2026-10-24: ADR candidate (cuts across the system): every identity that crosses machines, or that callers store, is
  derived from a stable identifier, and an identifier-breaking migration follows fixed rules. Written as ADR-05.
- 2026-10-24: ADR candidate (cuts across the system): the state directory decides `machine` leases, and nothing the
  broker holds lands in a directory V1 scans. Declined: the user did not pick it, and it is an implementation rule.
- 2026-10-24: Accepted. ADR-01 moved from proposed to accepted with it.
- 2026-10-24: Amended for NIT-007's design review, at the user's direction. "The machine part of a qualified name"
  gained key pinning for labels approved for mail. It had refused a different key only while the old stream was live,
  and that rule still holds for unapproved labels. "Broker protocol" now says the auth slot carries the pinned node key
  before NIT-006, and that TLS may terminate at an ingress. It had left the slot unused until NIT-006. "Broker
  lifecycle and store" now persists mail queued for a node and its stage, bounce, key, and handle-list state. It had
  persisted no event queue. "Node lifecycle" puts the codex path and `CODEX_HOME` in the plist. It had named only the
  executable. "Version skew" gained the mail event kinds. "Qualified names" and "Discovery queries" gained NIT-007's
  mail address, keyed by session ID. They had named only the handle, which changes on a Claude Code resume. Each
  change is why the NIT-007 text gives it.
- 2026-10-24: NIT-008's review settled recall for local sub-leases only, enforced by every reader when it reads, with no
  broker term. The NIT-008 Impacts entry said recall lowers a broker lease's expiry and needs the node for local
  sub-leases. It now names only the node's mirror, which applies a recall's deadline. "Lease term and renewal" said
  revival never applies to a NIT-008 recall. No broker lease is recalled, so that clause is dropped.
- 2026-10-24: NIT-006's review settled broker auth, and NIT-006 was accepted. Amended in place, with markers:
  "Broker protocol" said NIT-006's credential replaces or wraps the pinned key. NIT-006 wraps it in a user binding,
  forget and revoke close the stream, and cross-user features need a loopback or TLS listener. "The machine part of a
  qualified name" said NIT-006 decides whether its credential replaces the key. It never does, and a rebound label
  starts clean. "Lease term and renewal" let a `break` from any machine end an unknown holder's lease. Under NIT-006
  it must come from the lease's user or the broker owner. "Machine leases and user leases on the same path" left who
  may break whom to NIT-006, which settled it, and overlap reports now stay within one user. "Discovery queries" gains
  NIT-006's cross-user visibility. "Broker lifecycle and store" gains NIT-006's state as a new major. "Version skew"
  points at NIT-006's own analysis. The Impacts entry and the dependency risk were brought in line.
- 2026-10-24: Revisit audits for three hooks that NIT-006's acceptance fired. The user picked Act for each. "Which
  user a user-reach lease belongs to": the scope is now each bound user, under NIT-006, and the hook line is removed.
  The one-scope rule still holds until a broker turns cross-user features on. "Shared agents and acting_for": NIT-006
  settled the requesting user's scope under a standing delegation, and the hook line is removed. The risk "The broker
  is open before NIT-006": its mitigation now cites NIT-006 and ADR-08, and the hook line is removed. The risk stays
  until NIT-006 is built.
- 2026-10-24: Scheduled as NIT-004 M1-M2 and NIT-004 M3 in queue.md. The split lets NIT-005 M2 land on the new
  grant path first.
- 2026-10-29: "Node lifecycle" said the node config file sits in the state directory, beside NIT-002's `slots.json`.
  ADR-07 forbids that, because an agent sandbox can write the state directory, and the broker address, proxy, and CA
  decide where the node connects. It now says `~/.config/nitipan/node.json`, outside the state directory, found from
  the home directory in the user database. The node refuses a file or directory that another user owns or that others
  can write, which guards against other users and loose permissions and not against a sandbox. "Broker lifecycle and
  store" now names `~/.config/nitipan/broker.json` for the broker config, which it left unplaced. "Broker protocol"
  says only "the node config", so its text stands. The user chose `~/.config` during the NIT-004 M1-M2 work-queue plan,
  per ADR-07.
- 2026-10-29: ADR candidate (cuts across the system): the node-broker channel never carries plaintext over a public
  path. Undecided. Found in NIT-004 M1.
- 2026-10-29 (autonomous): Work-queue run best guess on M2, amending "Which user a user-reach lease belongs to" and
  M2's check on `user` and `shared` leases: one holder never conflicts with its own broker leases. The text said every `user` lease
  conflicts with every other `user` lease on overlapping names, and that `user` and `shared` leases on overlapping names
  conflict, with no word on a holder against itself. As built, the same names again from the same holder return the
  lease it holds, and overlapping names from the same holder are granted as another lease. A second `user` lease under
  one anchor, which M2's shared-anchor check needs, cannot exist otherwise. If this is wrong, one comparison in the
  broker's claim handler changes, and a second overlapping claim from the same holder is refused. The rule trusts the
  client-supplied holder key, which another session of the same OS user could forge. That is NIT-001's threat model.
- 2026-10-29 (autonomous): Row review fixes for NIT-004 M1-M2. "Machine leases and user leases on the same path" let `break` from
  any node delete a `taken` or `ended` fact. The holder then lost the notice that says who took or ended its lease,
  against "Lease term and renewal", which drops the marker on the owner's ack. A break from any node but the fact's
  owner now leaves the fact and answers that the lease already ended. The owner's own break still clears it.
  Separately, the node refuses a request file whose body ID differs from its file name, and refuses to follow a broker
  redirect, which would re-send the credential. Neither changes proposal text.
- 2026-10-29: ADR candidate (departs from the past): at `user` and `shared` reach, a lease names a repo's files across
  every checkout and worktree, unlike the checkout-boundary exemption ADR-03 sets for `machine` reach. Undecided.
  Found in NIT-004 M2. M3's path query also lists across worktree boundaries, found in NIT-004 M3.
- 2026-10-29 (run best guess): Amended "Broker lifecycle and store" and the risk on NIT-005, which both left the
  counter's floor on load to NIT-005. The text said the floor is NIT-005's to decide. NIT-005 M2 settled it: the
  broker applies no floor at load, since every grant floors its token at the wall clock. If a floor at load is wanted
  later, it is one `max` at start, and tokens already issued stay valid.
- 2026-10-30 (run best guess): Amended "Discovery queries" and M3's first check. The text said an answer is mirrored,
  stale, or lapsed. As built, a grant reads live or lapsed, a mirror reads mirrored or stale, and a mirror the broker
  cannot judge reads unknown, which is listed for every repo-name and resource query. A stale node's grants and mirrors
  also carry a stale flag. The commands are `who holds` and `who is`. A qualified name is accepted by `who holds` only.
  If this is wrong, the unknown rows are dropped, and discovery answers "nobody" for a lease it could not judge.
- 2026-10-30 (run best guess): Amended "Machine leases and user leases on the same path". The text said derived entries
  live in the sidecar and name no writer rule. As built, the CLI writes it after a `machine` claim when the user has a
  node config file, the node derives for a lease with no sidecar for its current keys, and the node removes the
  sidecars of ended leases. The same bullet's notice rule is now narrowed: a machine lease's holder gets a notice only
  when the CLI wrote its sidecar, since that marks a binary that can read one. If this is wrong, a V1 holder's notice
  would sit unread in `notices/`, and a notice would be sent for every mirrored lease.
- 2026-10-30 (run best guess): The overlap notice rides the snapshot answer, with the same pair marked once per node
  process, and the claim reply marks the grant side seen. A version-gated stream event was the other candidate. It needs
  a node version check and an ack, and the snapshot answer already reaches a node every third of a term. An older node
  ignores the field. If a notice must survive a node restart exactly once, the mark moves to a file.
- 2026-10-30: ADR candidate considered and not listed: a mirror the broker cannot judge reads as unknown, never as no
  overlap. It restates ADR-04's minor rule for a new reader, so it fails the Architecture test.
- 2026-10-30 (run best guess): Milestone review fixes for NIT-004 M3. A snapshot derived every underived lease before it
  returned, on the loop that heartbeats and renews, so many V1 leases in large repos could have kept the node quiet
  past a term. It now stops starting derivations after 5 seconds. `who is` skipped unreadable mirrors, and now lists
  them as unknown. Neither changes proposal text.
- 2026-10-30 (run best guess): Row-review fixes for NIT-004 M3. The claim reply marked the grant-side overlap notice
  as seen before the CLI read the reply, so a CLI that timed out and never retried left the grant's holder with no
  report. The node now marks the pair when the reply is read and holds the snapshot notice back until then. A reply
  unread past the grace lets the snapshot write the notice. A mirror below the broker's derivation floor now reads as
  unknown, as the claim path already refuses it. The refused claimant's transient anchor can still produce a brief
  overlap notice, which M3's As built block records. The As built text for the derived entries lost its symbol and
  path names.

## References

- `spec/reports/2026-10-18-lease-broker-research.md`: roadmap, reach levels, and open questions
- ADR-01: machines connect out to the broker
- ADR-05: cross-machine identities use stable identifiers
- NIT-001: the lease record and liveness check this proposal extends
- ADR-08: broker authority needs a proof no session can make, from NIT-006
