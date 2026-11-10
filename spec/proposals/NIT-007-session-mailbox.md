# NIT-007: Session Mailbox

**Status:** implemented
**Created:** 2026-10-18
**Updated:** 2026-11-09

## Dependencies

- NIT-004

## Impacts

- NIT-001 — "Holder identity" and handles. "Mailbox registration" derives a mail name from the session ID through
  NIT-001's word list. It equals the handle for Codex and opencode, and differs for Claude Code. NIT-001 is frozen, so
  nothing there changes
- NIT-004 — "Per-machine node": the node stores mail and rings sessions, not just heartbeats. "Broker lifecycle and
  store": the store must also hold mail queued for a disconnected node until it is acked, and the stage and bounce
  state "Delivery guarantees" needs. "Broker protocol" and "The machine part of a qualified name": "Auth" requires a
  per-label bearer key pinned at enrollment the user approves on the broker host, checked on every request, a
  mismatch refused whether or not the old stream is live, a "forget label" command, and verified HTTPS for mail. That
  changes "Losing the key never changes the label". "Version skew": mail, stage updates, and bounces are new event
  kinds gated on the node's registered version. "Node lifecycle": the LaunchAgent plist `setup` prints also carries
  the codex path and `CODEX_HOME`, since the plist is the one node input no agent sandbox can write
- NIT-008 — "Wake paths ring, and only a read consumes" and "Agents without a wake path". Rings also cover pending
  NIT-003 notices and requests. The ring ledger holds their keys and lives in the state directory. NIT-007 M1 builds
  the ledger with the mail keys. NIT-008 M3 adds the notice and request keys. Amended 2026-11-08; see Decision Log
- NIT-006 — "Node credential": NIT-006, now accepted, keeps the pinned bearer key "Auth" sets and binds its label to
  one user, and never runs a second credential beside it. "Cross-user mail authorization" opens mail across users
  under the recipient user's grant. Amended 2026-10-24; see Decision Log

## Summary

The broker carries mail between sessions on a user's machines. Any session can leave a message for another session's
mail address on any machine. The address is keyed by the session ID, so it survives a resume. That acts as a
cross-machine SendMessage. A message reaches a machine over the stream that machine's node opened to the broker. The
node stores it in a local mailbox, then rings the session through a path its agent supports, which wakes an idle
session where the agent allows it. The session reads its mail with `nitipan mail read`. Mail flows only between nodes
the user enrolled, over verified HTTPS. Under NIT-006, mail also crosses users, only under the recipient user's grant.

## Motivation

SendMessage reaches sessions on the same machine, or through Remote Control. Remote Control is banned at the user's
work. A guardian on one machine has no way to reach a requestor on another. NIT-004 lets sessions find each other
across machines. Finding a session is not enough if it cannot be told anything.

Waking matters most. A requestor sends a request to a guardian, goes idle, and waits for the reply. A mailbox that only
delivers on the next tool call never reaches it.

## Design Decisions (Settled)

### Build it

The user decided on 2026-10-18 to build the mailbox. The broker will store and deliver messages, not just leases.

### Qualified addresses

A mailbox is addressed by the session's qualified name from NIT-004: a local name unique on its machine, plus a machine
part the broker adds. The local name is the one the local delivery path uses. Two sessions with the same local name on
different machines are still separately addressable.

Amended 2026-10-24; see Decision Log. The local name in a mail address is the session's mail name from "Mailbox
registration", keyed by its session ID. For Codex and opencode it equals the NIT-001 handle. For Claude Code it does
not, since a Claude Code handle changes on every resume and ADR-05 requires a stored identity to stay the same.

### Delivery over the machine's own stream

The broker sends a message to a machine only over a connection that machine opened, per ADR-01. A message for a machine
whose node is not connected waits on the broker, for up to 7 days, then bounces to its sender. Amended 2026-10-24; see
Decision Log and "Delivery guarantees".

### Content is data

The receiving session treats message content as data from another session, never as instructions. SendMessage already
works this way.

### Where messages enter a machine

- The NIT-004 node delivers. It reaches Claude Code through the session inbox socket, which the docs offer to scripts
  and hooks. It reaches opencode through its local HTTP service, and Codex through `codex queue` or its app-server. One
  broker connection serves the whole machine. The node is not a session's own child. The current Claude Code docs say
  a message from a non-child is delivered to a session that prompts for permissions, auto mode included, when no
  `crossSessionInbound` value is set. A session that bypasses permissions holds it for approval, and a `-p` session
  holds it for `dialogExpiry`, five minutes by default, then drops it.
- A per-session stdio MCP server delivers through Claude Code channels. It wakes an idle session in Claude Code only.
  It inherits every channel gate. An org admin must enable channels and allowlist the server, and `-p` sessions ignore
  the development flag. Each session holds its own broker connection.
- The node receives, and relays to a per-session MCP server. The MCP server, as the session's own child, posts to its
  session's inbox socket. That passes the own-child check without channels. Whether MCP server processes receive the
  socket's variable is unverified, so this needs a spike.

Decision (autonomous): the node stores, then delivers. It writes each message it receives into a per-recipient
mailbox under the state directory, and acks the broker only after the write. Every local delivery path reads from
that mailbox.

- A send leaves the sender's machine through NIT-004's request handoff, like any broker request. The sender's node
  forwards it over its own broker connection, and the request ID is the message ID
- The mailbox is a new directory under the state directory that V1 binaries never scan, as NIT-004's request handoff
  is. Each message is one file named by its message ID, published with NIT-001's temp-file scheme
- The node never sees a session's environment, since launchd starts it. A process inside the session writes the
  session's delivery endpoint, such as the Claude Code inbox socket path, into the session's mailbox registration. See
  "Mailbox registration"
- The node keeps a ledger of message IDs it has accepted, so a resend after a crash between the write and the ack is
  dropped, even when the first copy was already consumed. An entry is kept as long as the broker could still resend
  the message
- The broker sends mail only to a node whose registered version supports it. For an older node it refuses the send with
  "recipient node too old", and bounces mail already queued for a node that later registers without mail, rather than
  letting the node ack an event it does not know. Amended 2026-11-08; see Decision Log
- Mail queued on the broker for a disconnected node is part of the broker's persisted state until the node acks it.
  NIT-004's "Broker lifecycle and store" persists no general event queue, so its store must also hold queued mail.
  See Risks
- The node never stores or presents a session's `CLAUDE_CODE_MESSAGING_TOKEN`. Posting with it would pass the node off
  as the session's own child, which gets around the receiver's inbound controls, an org's included
- How each agent is woken from the mailbox is settled under "Per-agent delivery adapters" and "Agents without a wake
  path"

A local copy is what lets one message reach a session by more than one path: a wake, and the session's next nitipan
call when no wake path works. The ack also then means the message is on the receiving machine's disk.

Direct push from the node with no local copy was rejected. The inbox socket accepts a message that a `-p` or
bypassing session later drops, so the node would ack a message that is then lost with no copy anywhere. A per-session
MCP channel was rejected. It needs per-launch flags, which nitipan cannot assume, and it gives each session a network
connection and a broker credential, which NIT-004's "How sessions reach the broker" avoids. A relay to a per-session
MCP server was rejected as the entry point. It rests on an unverified variable, still needs the node for Codex and
opencode, and serves only as a Claude Code wake path, which "Per-agent delivery adapters" weighs.

### Mailbox registration

A handle is derived on demand and stored only in lease, request, and notice records. A session that holds no lease has
no address the broker knows. A Claude Code handle is keyed by PID and process start, so a resumed Claude Code session
gets a new handle. NIT-001, which defines handles, is implemented and frozen. Who can be addressed, how a session's
delivery endpoint reaches the node, and whether an address survives a resume are open.

Decision (autonomous): a session registers its mailbox at start and again on each mail call. The mail address is
keyed by the session's stable identifier, its session ID, so it survives a resume. Amended 2026-10-24; see Decision
Log.

- Each agent's start path writes a registration into the state directory: a new Claude Code `SessionStart` hook that
  `setup` prints, Codex's existing `SessionStart` hook, and the opencode plugin's session events. Every nitipan call
  that sends or reads mail refreshes it from its own environment. That covers a session started before the hook was
  installed, and gives every sender a reply address. A send is refused unless its sender is a registered session that its
  node reported as live, so every bounce has a mailbox to land in. Amended 2026-11-08; see Decision Log
- A registration stores the holder's identity fields and the delivery endpoint. Claude Code registrations are keyed by
  agent, PID, and process start, and retain every session ID the process held across `/clear`. Codex and opencode
  registrations also key on the session ID, since their shared hosts run several conversations at once. Each keeps
  only its own session ID. It stores no name. The node checks the process and NIT-001's per-session liveness rule before
  it rings. A later registration for the same identity replaces the earlier one, which is how an endpoint is refreshed.
  Amended 2026-11-08; see Decision Log
- The mail address is `<mail name>@<label>`. The mail name is derived from the agent and the session ID through
  NIT-001's word list. A Claude Code mail name has its own domain separator. A Codex or opencode mail name uses
  NIT-001's identity string unchanged, so it equals their handle, since both handles are already keyed by session ID.
  The broker stores a derivation version with each session it learns. A Claude Code session has two names: its lease
  handle, keyed by PID, and its mail name, keyed by `CLAUDE_CODE_SESSION_ID`. Amended 2026-11-08; see Decision Log
- The broker is the only place a mail name is derived, as it is for labels. Nodes report raw session IDs from
  registrations and from lease records' `session_id`. Discovery answers, reply addresses, and `mail status` all print
  the mail address, so no sender ever derives one. "Who holds X" and "who is named N" accept a mail address too
- A Claude Code session with no session ID, such as one whose lease record predates `session_id`, has no mail
  address. Discovery shows it as not mailable and never falls back to its handle, as ADR-05 requires
- An address resolves in this order: the live registration whose current session ID matches; then a live
  registration that held that ID before a `/clear`, with the message marked as addressed to its pre-clear session;
  then nothing live, in which case the mail waits for a resume. `CLAUDE_CODE_SESSION_ID` keeps the resumed ID on
  `--resume` in hooks and Bash, per the Claude Code docs, so a resumed session reads mail sent to it before
- When two live registrations hold the same current session ID, the node refuses the mail as ambiguous, naming both
  holders. The sender learns of it through "Delivery guarantees"
- The state directory is writable from inside agent sandboxes, so the node never runs anything a registration names.
  It connects to a Claude Code socket only after the kernel reports that its peer is the registered Claude Code PID,
  and it only writes a ring there. A same-user process can still write a registration that names another live
  session's identity, within the same-user trust NIT-001 already has
- The node reports each live registration's session IDs to the broker, and the broker keeps every session ID each
  label ever reported. Mail for an address no node ever reported is refused at send with "no such session", as NIT-004
  refuses an unknown label. An address once reported stays valid, so a stored copy never strands. The broker reads
  reports only from a label pinned to the reporting node's key, since a pending label can neither send nor receive
  mail. It reads the first 256 sessions of a snapshot, and refuses an agent it has no mail name for and an ID unsafe in
  a file name. It stores at most 10,000 session IDs per label and refuses a new one past that. Each refusal is logged
  with its reason and never dropped quietly. A label that reaches the bound needs a new design, since an address once
  reported stays valid. Amended 2026-11-08; see Decision Log
- Registrations, presence files, and message files carry `major.minor` versions under ADR-04's reader rules, as
  NIT-004's sidecars do. One the node or CLI cannot use reads as unknown: it is never rung or read, and `mail status`
  says why. A registration missing a field its adapter's descriptor needs is refused the same way

An address names a conversation, which is what a sender means. A guardian's reply to a requestor the user quit and
resumed still arrives, on day 8 as on day 1. A `/clear` keeps the process reachable at its earlier address.

Handle addresses with a 7-day resume alias were rejected. A Claude Code handle changes on every resume, which breaks
ADR-05's rule that a stored identity stays the same for the same thing, and a stored address arriving after 7 days
gets "no such session" for a live session. Handle addresses with permanent aliases were rejected. The address still
changes on each resume, and a `/clear` followed by two separate resumes leaves one old handle ambiguous forever. An
ADR exempting restart-changing addresses from ADR-05 was rejected as a waiver of a known failure. Lease holders only
was rejected. A requestor holding no lease could not get a reply, and lease records carry no delivery endpoint.
Extending Codex's existing session registration was rejected. It is a liveness artifact that V1 binaries read, and
it covers Codex only. Registration at start alone was rejected. It misses sessions started before the hook existed.
Registration on use alone was rejected. A resumed idle session keeps a stale endpoint until it calls nitipan. Changing
NIT-001's handle derivation was rejected. V1 binaries on PATH would keep writing the PID-based handle.

### Auth

A message across users needs to say who sent it. Whether the mailbox needs NIT-006 before it ships, or only before
cross-user delivery, is open.

Decision (autonomous, judge): mail ships before NIT-006, but only between enrolled nodes. Each label is pinned to a
node key that the user approves on the broker host. The key is checked on every request, and mail runs only over
verified HTTPS. Mail across users waits on NIT-006.

- The node's credential is a random bearer key kept in its 0700 state directory. It rides in NIT-004's
  `Authorization` slot on every request: the stream's `GET`, every send, and every ack
- The broker pins the key to the label when the user approves the label on the broker host. A new label stays pending
  until then and can neither send nor receive mail. Its leases follow NIT-004 unchanged. The approve command takes a
  fingerprint, which the node prints locally, and pins the pending key that matches it. A fingerprint no pending key has
  is recorded, and the label pins the first key that presents it. Every other pending key for the label is discarded, so
  a host that submits a key of its own cannot block the approval. Amended 2026-11-08; see Decision Log
- A different key for a pinned label is refused whether or not the old stream is live, for leases as well as mail.
  Labels the broker saw before mail shipped are not pinned automatically. Each needs the same approval. Until then the
  label keeps NIT-004's live-stream key rule for leases, and its stream receives no mail. Approval closes a stream that
  opened under another key. Amended 2026-11-08; see Decision Log
- A reinstalled or wiped machine is recovered with a broker-side "forget label" command. Mail queued for a forgotten
  label is kept for the label's next approved key, never handed to an unapproved one. Mail the broker had sent and
  saw no ack for is queued again, since an ack lost before a wipe cannot be told from a message that never arrived.
  Mail already stored on the forgotten machine and never read bounces to its senders without its body, since the
  broker no longer holds it. Under NIT-006, when the next key binds the label to a different user, the queued mail
  bounces instead. Amended 2026-11-08; see Decision Log
- A node sends and accepts mail only when its broker address is HTTPS with a certificate it verifies. A node on plain
  HTTP gets leases but no mail, and its error says why
- The broker stamps each message's sender from the authenticated connection and ignores any sender field the client
  sets. It sets `sender_verified` to true for an enrolled node. The field's absence means unverified, under ADR-04's
  minor rule, so an older node or broker never passes an unverified message off as verified
- The receiver frames the sender and its verification outside the message body, so the body cannot forge them
- Under NIT-006, approval also binds the label to a user. Approval on the broker host binds it to the broker owner,
  and a coworker binds through NIT-006's sign-in page. `sender_verified` keeps the meaning above: the sender is one of
  the recipient user's own enrolled nodes. Mail from another user carries the sender's user label in a new field and
  no `sender_verified`. The broker stores a hash of each pinned key. Amended 2026-10-24; see Decision Log

NIT-007 places these as requirements on NIT-004, whose "Broker protocol" and "The machine part of a qualified name" are
under their own review. Pinning changes NIT-004's "Losing the key never changes the label": losing the key then locks
the label until the user forgets it. It also replaces NIT-004's rule that a different key is refused only while the
old stream is live. NIT-007 M1 builds the pinning, approval, and forget commands in the broker, since NIT-004's leases
do not need them. NIT-006 keeps this key and wraps it in a user binding, and never runs a second credential beside
it. See Impacts and Risks.

The requirement is on the hop the node makes. The node sends mail only to an HTTPS broker address whose certificate it
verifies, from the system roots or a CA file in the node config. TLS may terminate at an ingress in front of the
broker, which then listens on plain HTTP on a private address, as NIT-004's "Broker protocol" allows. The user
confirmed at the review gate that deployments will have certificates, most likely at an ingress. Tests generate a
temporary CA.

Within one user's machines, the guardian-reply case needs only proof that a message came from one of the user's own
nodes. User identity adds nothing there. NIT-006, accepted later, keeps this key as the node credential.

Shipping with only a marker was rejected. On NIT-004's open broker, a host on the LAN can register as a sleeping
laptop, or `POST` a send as a label it has seen. An idle Codex or opencode session, which has no approval hold, then
runs an unattended turn on its text. The impostor can also ack the laptop's queued mail, so that mail is lost and its
contents disclosed. A marker changes nothing an unattended model can do. A key checked only when the stream opens was
rejected, since sends and acks are separate `POST`s. A keypair was rejected as adding nothing over a bearer key on
verified HTTPS. A local sender allowlist was rejected, since labels can be spoofed. Waiting for all of NIT-006 was
rejected. It delays the motivating case for no security gain within one user.

### Wake paths ring, and only a read consumes

The four decisions below share one rule, settled with them. A wake path never carries a message's content, and never
removes it from the mailbox. It only rings: it tells the session that N messages are pending and to run
`nitipan mail read`. Only `nitipan mail read` consumes mail. It prints each message to stdout, framed with its sender
and verification, then removes it, the way NIT-001 delivers notices.

A wake rings once per message. A ledger per registered identity holds the message IDs some path already rang for.
Amended 2026-10-24; see Decision Log. A refreshed registration for the same identity keeps that ledger. A waiter
exits to wake, and the node pushes, only for an ID not yet rung. Unread mail that was already rung keeps showing in the
non-waking rings of "Agents without a wake path", but never wakes the session again. A session that ignores its ring
cannot wake forever.

The ledger is a file beside the registration in the state directory, so a waiter with no node running reads it too.
NIT-008's "Telling the sub-lessee" sets its format, and adds keys for pending notices and requests, which every wake
path rings for.

A message body is capped at 64 KiB, refused at send. The cap keeps queued and bounced mail small in NIT-004's
whole-file store, and keeps one message within what an agent shows as tool output. Amended 2026-11-08; see Decision
Log.

nitipan cannot see whether a push path delivered. The inbox socket reports no outcome, and `codex queue` exits once
the message is stored, not once it runs. A path that took the message out of the mailbox before the session read it
would bring back the loss "Where messages enter a machine" exists to prevent. A ring that is lost or repeated loses
nothing. A read is also a stage nitipan can observe, and the content arrives as tool output, which frames it as data.

### Claude Code wake path

The node can post to the session's inbox socket as a process that is not the session's child. A hook the session runs
can instead wait on the session's mailbox and wake it with `asyncRewake`. Which path wakes an idle Claude Code session
is open. Added from "Per-agent delivery adapters", since the choice turns on Claude Code's inbound controls.

Decision (autonomous): a waiter hook rings first. The node rings through the inbox socket when no waiter is armed.

- `setup` prints a Claude Code `SessionStart` hook and a `Stop` hook, each `asyncRewake`, that run
  `nitipan hook mail-wait` with a long timeout. The waiter watches its session's mailbox with kqueue. When mail is
  pending, it exits 2 with the ring on stderr, which wakes an idle session
- Each armed waiter publishes a presence file with its PID and process start. A newer waiter for the same session
  replaces the older one, which exits, so waiters do not pile up across turns
- When the node finds mail for a Claude Code session with no live waiter, it posts the ring to the session's inbox
  socket. The receiver's inbound controls apply to it as to any peer message. A waiter that exited by ringing has
  already rung for that message, so the node does not ring it again
- The waiter stands down when any Claude Code settings scope it can read sets `crossSessionInbound: refuse`. nitipan
  honors a user's or an org's refusal on both paths. Mail then waits for "Agents without a wake path"
- The waiter's timeout has no documented maximum. When it expires with no mail, the session has no waiter until its
  next `Stop`, and an idle session never reaches one. The socket ring covers that gap under the inbound controls, and
  the mail waits otherwise. It is delayed, never lost

The waiter is the session's own process, so it reaches a session that bypasses permissions, which holds a peer
message for approval. The socket ring reaches a session started before the hooks were installed.

The socket alone was rejected. A bypassing session holds its ring for approval and drops it unanswered, and a `-p`
session drops it after five minutes. The waiter alone was rejected. It leaves an idle session with no wake once its
waiter times out. Socket first with the waiter as backup was rejected. The waiter would ring a second time for every
message the socket already rang.

### Per-agent delivery adapters

Each agent takes messages differently. NIT-001 already has per-agent adapters for holder identity. Whether delivery
extends those adapters or stands apart is open.

The Claude Code socket's message body is undocumented. Only its auth line is documented. An adapter that writes the
body depends on a format that can change.

Decision (autonomous): delivery extends NIT-001's adapters. Each adapter gains a data-only delivery descriptor, and the
node holds the code for each mechanism.

- The descriptor names the wake mechanism and the registration fields its endpoint needs. `internal/adapter` stays
  free of node code
- There are two mechanism shapes. A watcher runs inside the session and rings from there: the Claude Code waiter and
  the opencode plugin. A push is sent by the node: the Claude Code socket ring and `codex queue`
- opencode: the existing server plugin watches its sessions' mailboxes and rings through the plugin API's
  `session.prompt`, which starts a turn on an idle session. The plugin publishes presence, so the node can tell it
  failed to load
- Codex: the node runs `codex queue --thread <id>` with the ring. The codex executable and `CODEX_HOME` come from the
  node's LaunchAgent plist, which `setup` prints with the values it resolves. No agent sandbox can write the plist.
  The node never takes either from a registration or from a host process, since a sandboxed session can write the
  first and spoof the second's argv. A config under an attacker's `CODEX_HOME` could otherwise run commands outside the
  sandbox. With no codex path in the plist, Codex sessions get only the non-waking rings, and the node logs why
- A Codex registration's `CODEX_HOME` is compared with the plist's, never run. On a mismatch the node skips the push
  and does not mark the message rung. It logs the reason, and `mail status` shows it. The session still gets the
  non-waking rings
- An embedded Codex TUI picks up `codex queue` without a daemon in Codex 0.167.0. M2 records the scripted observation.
  Amended 2026-11-08; see Decision Log
- The Claude Code socket ring writes the undocumented body. Its format risk is accepted. A refused post is reported

One per-agent list keeps an agent's identity and delivery in one place. A new agent cannot gain an adapter and silently
lack a wake mechanism.

A separate delivery registry was rejected. An agent added to one list and not the other would have no wake path and no
error. opencode's local HTTP service was rejected. It needs the password opencode strips from session environments on
purpose, on undocumented routes. Codex's app-server `turn/start` was rejected. It is experimental, and a node
connection changes the daemon's state: its client name overwrites the daemon's originator, and the connection holds
subagent threads loaded.

### Agents without a wake path

Source readings found idle wake paths in both other agents. opencode's local service starts a turn when a prompt is
posted to an idle session. `codex queue` is documented as stable, and a 10-second poller starts a turn on an idle
thread. Both await live tests, which the opencode and Codex wake handoffs describe.

A future agent, or a setting that blocks these paths, would still need a fallback tier. One example is delivery on the
session's next tool call through a hook or a nitipan call.

Decision (autonomous): every path the session already runs rings, and `nitipan mail read` reads.

- Every nitipan call on the identified-holder path, the one that delivers NIT-001 notices, rings when mail is pending.
  The agent `hook` subcommands do not, since nobody reads their output. NIT-008's git hook warning is not a mail ring
- Turn hooks ring through `additionalContext`: Claude Code `UserPromptSubmit` and `PostToolUse` hooks that `setup`
  prints, the matching Codex hooks, and the opencode plugin. A ring is one directory read
- An agent with no wake path and no hooks still gets the ring on its next nitipan call, and can always run
  `nitipan mail read`

A session that refuses peer messages, or whose waiter timed out, still hears of its mail on the next prompt the user
types, the next tool call, or the next nitipan call.

The next nitipan call alone was rejected. A busy session that never calls nitipan would never hear of its mail. Turn
hooks alone were rejected. An agent with no hooks would have no path. Relying on each agent's own holding was rejected.
`refuse` drops the message, `-p` drops it after five minutes, and an Esc interrupt stalls `codex queue`.

### Delivery guarantees

The research does not say whether messages expire, whether a sender learns of delivery, or what happens to a mailbox
whose session has died.

Decision (autonomous): the sender can see each stage of its message, and only a bounce wakes it. Mail that cannot be
read is returned to the sender with the reason.

- A message's stages are: queued on the broker, stored on the recipient's machine, and read by the recipient through
  `nitipan mail read`. A read is the last stage nitipan can observe. The sender sees each stage through
  `nitipan mail status <id>`, and on its next nitipan call. A stage never wakes the sender
- A bounce returns the message, body included, to the sender's mailbox with its reason, so it rings like mail. The
  reasons are: the broker held it 7 days without sending it, the recipient was not running and no resume read it within
  7 days, two live registrations made the address ambiguous, the recipient node is too old, the recipient's label is
  pending or forgotten past 7 days, or the recipient's label was forgotten with the mail stored but unread. NIT-006
  adds "sender no longer allowed", "recipient holds no `shared` lease", and a label rebound to a different user, for
  cross-user mail. Amended 2026-10-24; see Decision Log
- 7 days matches NIT-004's retention of request outcomes, and outlasts a laptop shut over a weekend. It is a constant,
  not a field
- A bounce or a stage update never produces a stage update or a bounce of its own. A bounce that cannot reach its
  sender is kept for 7 days, then dropped
- The broker decides expiry under the same lock it sends under. The 7-day expiry applies only to a message the broker
  never sent. A message it sent and saw no ack for is never bounced for expiry. When the node reconnects, it first
  reports the IDs it stored and read, and the broker resends only the ones it did not store. A node that never
  reconnects leaves such a message as "sent, not acknowledged" in `mail status` until the user forgets its label
- Mail for a session that is not running stays stored for 7 days, waiting for a resume, and `mail status` says the
  recipient is not running. The address itself stays valid after a bounce, so later mail reaches a later resume
- Stage updates and bounces are new event kinds between broker and node. The broker sends them only to a node whose
  registered version supports them. A newer node facing an older broker reports "broker too old" for mail, per NIT-004's
  "Version skew"
- The mail request kinds raise the handoff request version to 4, which is global to the CLI and node. A new CLI facing
  an older node gets "node too old" for every request, mail or not, until the node restarts on the new binary, which
  it does by itself when its executable changes. An older CLI facing a new node sends versions the node still answers,
  and has no mail commands. The broker never sees the handoff version. Its direction is the node's registration: an
  older node sends no mail capability, so the broker refuses a send to it with "recipient node too old", and a newer
  node facing an older broker reports "broker too old". The broker store is 1.1. A 1.0 store is stamped 1.1 as it
  loads, so a 1.0 broker refuses the file once a newer broker has run on it. Rolling the broker back below 1.1
  therefore needs a copy of the store made before the upgrade, and loses every approval, reported session, and queued
  message since. A minor stamp keeps no previous file, since NIT-004 keeps one only before a major migration.
  Amended 2026-11-08; see Decision Log

A sender can tell mail in flight from mail stuck, and a reply is never silently lost. An idle requestor wakes for its
reply or its bounce, and for nothing else.

Receipts that arrive as mail were rejected. One message would wake an idle requestor three times. Bounces only were
rejected. The sender could not tell mail in flight from mail stuck. Silent expiry was rejected as silent loss. A read
receipt the recipient sends was rejected. It depends on the model remembering to send it, and a read already is the
observable stage.

## Design Decisions (Open)

None.

## Risks

- **The work environment blocks every Claude Code wake path** — likelihood: medium, impact: high.
  Amended 2026-10-24; see Decision Log. An org can set `crossSessionInbound: refuse`. The waiter then stands down and
  the socket ring is dropped, so no message wakes an idle session. Mail still arrives on the next prompt, tool call, or
  nitipan call. Channels are no longer a path. Mitigation: check the work environment's managed settings for
  `crossSessionInbound` before M1. That check is still pending. The user planned to test the Enterprise org's wake
  paths before talking to its admins.
- **The socket body format changes** — likelihood: medium, impact: low.
  Amended 2026-10-24; see Decision Log. Only the auth line is documented. Accepted by the user on 2026-10-18. The
  socket carries only a ring, used when no waiter is armed, so a format change loses a wake and never a message. The
  adapter still fails loudly on a rejected post.
- **Injected messages become a prompt-injection path** — likelihood: medium, impact: high.
  Amended 2026-10-24; see Decision Log. A sender puts text in front of the receiving session, and a woken Codex or
  opencode session runs a turn with nobody watching. Mitigation: mail flows only between nodes the user enrolled, with
  the sender stamped by the broker and framed outside the body. Content arrives as tool output and is treated as data.
  A compromised enrolled machine, or a hostile session on one, can still send. Within one user that stays as is.
  Across users, NIT-006 lets mail through only under the recipient user's grant, sends a conversation's first message
  only to a session holding a `shared` lease, and re-checks the grant before delivery. A granted coworker's
  compromised session can still reach those sessions. Amended 2026-10-24; see Decision Log.
- **NIT-004 drifts from what NIT-007 requires of it** — likelihood: low, impact: high.
  NIT-007 needs NIT-004's store to persist queued mail, its key rule to refuse a second key for a pinned label, and
  its plist to carry the codex path. Amended 2026-10-24; see Decision Log. Mitigation: NIT-004 was amended in place
  for each, with markers and a Decision Log entry, and its Impacts entry for NIT-007 names every amended decision.
  A later change to them goes through those entries.
- **A broker is deployed without TLS in front of it** — likelihood: low, impact: medium.
  Mail needs verified HTTPS on the node's hop. Mitigation: the user confirmed deployments will have certificates,
  most likely terminated at an ingress. A node on plain HTTP refuses mail with an error that says why, so the gap is
  visible.
- **Mail formats become a contract** — likelihood: high, impact: medium.
  The mailbox layout, message IDs, registrations, and the meaning of `sender_verified` are read by every CLI and node
  on PATH once mail ships. Mitigation: each file carries a `major.minor` version under ADR-04's reader rules. An
  unknown version reads as unknown, never as absent. `sender_verified` absent means unverified.
- **The node runs code a sandboxed session chose** — likelihood: medium, impact: high.
  The node runs outside every sandbox, and the state directory is writable from inside them. A registration or a
  spoofed host process could name a binary or a `CODEX_HOME` for the node to run. Mitigation: the node runs only what
  its LaunchAgent plist names, and only writes rings to a socket whose peer PID it checked.
- **Pinning locks out a machine that loses its key** — likelihood: low, impact: medium.
  A wiped state directory or a reinstall leaves the label pinned to a key nobody holds. Accepted: the user runs
  "forget label" on the broker host and approves the new key, and queued mail waits for it.

## Milestones

Amended 2026-10-24; see Decision Log.

### M1: Mailbox and Claude Code delivery

**Status:** DONE

- A Claude Code session on one machine sends a message that a Claude Code session on another machine reads with
  `nitipan mail read`, and the message is stored on the recipient's machine before the broker drops it. Amended
  2026-11-08; see Decision Log
- Mail queued for a disconnected node survives a broker restart. A body over the cap is refused at send
- A node crash between storing a message and acking it, followed by a resend, leaves one copy, including after the
  first copy was read
- A new label can neither send nor receive mail until the user approves it on the broker host. A second key for an
  approved label is refused while its machine sleeps. A send or ack without the label's key is refused. A node on
  plain HTTP gets no mail, and says why. Approval by the fingerprint the node prints pins that key and discards any
  other pending key for the label
- After "forget label", queued mail waits for the next approved key, and stored mail never read bounces without its
  body
- The broker stamps the sender from the connection, and a client-set sender or `sender_verified` is ignored. The
  reader shows the sender and its verification outside the message body
- A Claude Code session registers at start and on each mail call. A resumed Claude Code session keeps its mail
  address and reads mail sent before the resume, a stored address still reaches it after 7 days, and mail to a
  pre-`/clear` address reaches the live process marked as such. Two live sessions with one session ID make the address
  ambiguous. A dead registration is never rung, mail for an address no node reported is refused at send, and a Claude
  Code holder with no session ID shows as not mailable. Amended 2026-11-08; see Decision Log
- The broker alone derives mail names. Discovery, reply addresses, and `mail status` print mail addresses, and "who
  holds X" and "who is named N" accept one
- The node rings a Claude Code socket only after the kernel names the registered PID as its peer
- An idle Claude Code session wakes through the waiter. With no live waiter, an idle session that prompts for
  permissions wakes through the socket ring. A waiter facing `crossSessionInbound: refuse` stands down. A newer waiter
  replaces an older one, and a day of turns leaves one waiter
- No wake path removes mail. A ring that is lost or repeated loses no message. Each message wakes a session at most
  once, even when the session ignores the ring
- A registration, presence file, or message file of an unknown version is never rung or read, and `mail status` says
  why
- Every identified-holder nitipan call, and Claude Code `UserPromptSubmit` and `PostToolUse` hooks, ring when mail is
  pending
- `nitipan mail status` shows queued, stored, and read, and says when a recipient is not running. A message bounces
  with its body for each reason "Delivery guarantees" lists. A bounce wakes the sender, and no stage does. Bounces and
  stage updates produce none of their own
- A message the broker sent without seeing an ack is never bounced for expiry. A node back after 7 days reports what
  it stored and read, and only unstored messages are resent
- An old node with a new broker gets no mail, and the sender gets "recipient node too old". A new node with an old
  broker reports "broker too old" for mail. Both in tests

**As built:**

Left to the user. No live Claude Code harness exists in the repo, and no check here ran against a real session:

- A live check that a real idle Claude Code session wakes through the `asyncRewake` waiter, and through the node's
  socket ring with no waiter armed. Tests drive the hook contract (exit status 2 with the ring on standard error) and a
  fake inbox socket, never Claude Code itself
- The Risks item "The work environment blocks every Claude Code wake path": check the work org's managed settings for
  `crossSessionInbound`. Only the user can do that. It does not hold the milestone

What was built, and what was found:

- The mailbox is a `mail/` tree in the state directory: inbox, accepted-ID ledger, read markers, registrations, ring
  ledger, waiter presence, and the sender's stage notes. A reader files a read marker and the node owns the ledger, so
  neither rewrites the other's file. The accepted-ID check also counts a message still in the inbox or already marked
  read, which closes the window between the write and the ledger entry
- The ring ledger is built here, in NIT-008's format as amended, keyed `mail:<message ID>`. A waiter and the node claim
  keys under one lock, so each message wakes a session at most once. NIT-008 M3 adds the notice and request keys. See
  Decision Log
- The broker store is 1.1: pins, every session ID each label reported, and mail. A 1.0 store is stamped 1.1 as it loads,
  so an older broker refuses the file once a newer one has run on it. Each node's last mail capability and HTTPS flag are
  stored, so "recipient node too old" and the plain HTTP refusal work while the node is offline
- Approval is a broker-local admin unix socket beside the store, with a peer UID check and a 0700 directory. `nitipan
  broker pending|approve|forget` call it. No network route and no handoff kind reaches it, and tests assert both. The
  approve output and `setup broker` carry ADR-08's warning. The node prints its key fingerprint with `nitipan node
  fingerprint` and in `setup node`
- Claude Code facts checked against its docs on 2026-11-08. The hooks page documents `asyncRewake` as "runs in the
  background and wakes Claude on exit code 2", with stderr shown as a system reminder, and documents no maximum for a
  hook `timeout`. `setup claude` therefore prints 86400 seconds. The settings page lists `crossSessionInbound` scopes as
  user, shared project, project local, managed, and `--settings`, on an `accept` < `hold` < `refuse` ladder, where a
  stricter project or local value beats a managed one. The environment variable page names
  `CLAUDE_CODE_MESSAGING_SOCKET` (v2.1.224 or later) and `CLAUDE_CODE_MESSAGING_TOKEN`, exported to hooks and Bash
  commands. The token is never read, stored, or presented. The waiter reads the user, project, local, and file-based
  managed scopes. It cannot see MDM, server-managed, or `--settings` values of the session. A refusal set only there
  stops the socket ring inside Claude Code and leaves the waiter armed. The macOS managed path comes from the sandbox's
  own deny list, and the Linux path from memory, since the managed settings page was not fetched
- The mail name for Codex and opencode is already derived and equals the lease handle. Their delivery is M2
- `NIT-006` said a kept previous store holds this proposal's keys in the clear. Keys are stored hashed, so that line was
  stale. The row review corrected it in NIT-006 with an amendment
- A `Makefile` variable pair, `TEST_PKGS` and `TEST_ARGS`, lets `make test` run one package. Nothing else changed in
  the build
- `TestDeadHolderEndsItsLeasesAtOnce` already raced on the base commit. It deleted from the fake process table while the
  node read it, and failed about one run in three there. The fake table now locks, and the test calls `Remove`
- `TestWhoHoldsReadsAV1RecordAndAnUnreadableSidecarAndAQuietNode` failed about one run in thirty on this milestone's
  tree and never on the base. The cause was in the product. While the node derived a lease's repo entries, which can
  take a while, an unreadable derived file of a higher major could appear. The node then wrote its own file over it, and
  the lease read as derived. A node never rewrites a file it cannot read, so it now leaves that file alone and the lease
  reads as unknown. A node test holds the derivation open, plants the file, and releases it, so no timing is involved.
  The CLI test passes 60 of 60 runs after the fix, and failed within 30 before it
- Review found and the milestone fixed five more, each pinned by a test:
  - An unusable registration may name any session, so while one exists no stored mail expires and none bounces for want
    of a live holder. It is never reaped, and `mail status` lists it. The sessions it names are left out of the node's
    snapshot, since the node cannot read them
  - A ring ledger, accepted-ID entry, waiter presence file, or sent note that is unusable or of a higher minor is never
    rewritten. A ledger read that way may let a ring repeat, and the node then rings that registration at most once
    every 30 seconds. An accepted-ID entry of any version still counts as the message taken, so a resend never becomes a
    second copy. A waiter that cannot replace a newer presence file does not arm, and `mail status` says why
  - The admin socket's directory must belong to the broker's user. A file at the socket path is removed only when it is a
    stale socket. A path over the platform's limit is refused with the path and the limit
  - Approval no longer depends on the bounded list of pending keys. A host that registers many fresh keys while the real
    machine sleeps could evict the real key before the user approved it, and could hold the label's stream so the real
    key was never recorded. A well-formed fingerprint no pending key has is recorded, and the label pins the first key
    that presents it. A malformed fingerprint, or an approval for a label already pinned to another key, is refused
  - `mail send` rings pending mail like any identified-holder call. `mail read` and `mail status` stay quiet, since they
    show the mail
- The row review found and fixed these, each pinned by a test where one could be written:
  - The broker stored every session a node reported, with no check of the agent or ID, no bound, and from a pending
    label. It now reads reports only from a pinned label, first 256 per snapshot, refuses an unknown agent and an
    unsafe ID, and stores at most 10,000 IDs per label. Each refusal is logged. A mail to a label that was never
    approved is refused with "no such session", since the broker holds no address for it. A forgotten label keeps its
    addresses, so mail queued for it waits
  - A socket ring held the node's mail lock across the dial and the write. A socket that never answers could stall
    every other mail call for the dial's two seconds. The node now judges the registrations under the lock and dials
    after releasing it. The claim on the ring ledger is still made before the dial and undone when it fails. A reap
    can land between the pick and the claim, so the claim and the undo write no ledger for an identity with no
    registration, and removing a registration holds the ledger lock
  - A registration refresh over a file that failed to decode, or whose name and fields did not match, rewrote it and
    dropped its session IDs. A refresh now refuses on any read error other than a missing file
  - Reaping a dead process's registration removed files this binary may not remove under ADR-04. It now removes only a
    file it can decode, of its own major and no higher minor. A registration it must leave keeps its ledger and
    presence beside it. A leftover registration still reads as dead, so nothing rings it, and `mail status` lists it
  - The version skew and store rollback consequences, and the narrowed checks and behaviors the Decision Log held
    alone, are now in the settled text with markers
- The Claude-only gate narrows two checks. A session sends mail and registers its mailbox only when its agent is Claude
  Code. Codex and opencode sessions get a clear refusal until M2 lifts the gate. See Decision Log

Check to test, by the bullet order above:

1. Two machines, stored before the broker drops it: node `TestMailIsStoredOnTheRecipientsMachineBeforeTheBrokerDropsItAndTheSenderSeesTheStage`, cli
   `TestASessionSendsMailAcrossMachinesAndTheRecipientReadsItFramedOutsideTheBody`
2. Survives a restart, cap refused: broker `TestQueuedMailSurvivesABrokerRestartAndIsStoredThenAcked`,
   `TestABodyOverTheCapIsRefusedAtSend`, cli `TestMailRefusesABodyOverTheCapBeforeTheBrokerSeesIt`
3. Crash between store and ack: node `TestACrashBetweenStoringAndAckingLeavesOneCopyEvenAfterTheFirstWasRead`, mailbox
   `TestTheAcceptedLedgerOutlivesTheReadAndPrunesAfterTheRetention`
4. Approval, pinning, keys, HTTPS: broker `TestANewLabelCanNeitherSendNorReceiveMailUntilApproved`,
   `TestAPendingLabelStillGetsLeaseServiceButNoMailEvent`, `TestASecondKeyForAnApprovedLabelIsRefusedWhileItsMachineSleeps`,
   `TestAnUnpinnedLabelKeepsTheLiveStreamKeyRule`, `TestApproveByFingerprintPinsThatKeyAndDiscardsTheOthers`,
   `TestApprovingTheRealKeyClosesAnImpostorsOpenStreamAndSendsItNoMail`, `TestAFloodOfFreshKeysCannotEvictTheRealKeyFromAnApprovalItNeverGets`,
   `TestAdminPathChecksOwnerFileTypeAndLength`,
   `TestASendOrAckWithoutTheLabelsKeyIsRefused`, `TestANodeOnPlainHTTPGetsNoMailAndTheErrorSaysWhy`, `TestDefaultKeyPolicy`;
   node `TestANodeOnPlainHTTPSendsAndAcceptsNoMailAndSaysWhy`; cli `TestANodeOnPlainHTTPGetsNoMailAndTheErrorSaysWhy`,
   `TestTheApprovalCommandsReachTheBrokerOnlyThroughItsAdminSocket`; the admin socket, peer UID, and no network route:
   broker `TestAdminSocketListsApprovesAndForgetsForTheBrokersOwnUser`, `TestAdminSocketRefusesAPeerThatIsAnotherOSUser`,
   `TestAdminSocketNeedsAPrivateDirectory`, `TestTheNetworkHandlerServesNoApprovalRoute`
5. Forget: broker `TestForgetKeepsQueuedMailForTheNextKeyAndBouncesStoredUnreadMailWithoutItsBody`
6. Sender stamped from the connection, reader framing: broker `TestMailReachesTheRecipientsStreamWithTheSenderStampedByTheBroker`;
   cli `TestASessionSendsMailAcrossMachinesAndTheRecipientReadsItFramedOutsideTheBody`
7. Registration, resume, 7 days, pre-clear, ambiguity, dead, unreported, no session ID: cli
   `TestAResumedSessionReadsMailSentBeforeTheResumeAndAnAddressOutlivesItsProcess`,
   `TestMailToAPreClearAddressReachesTheLiveProcessAndSaysSo`,
   `TestTwoLiveSessionsWithOneSessionIDBounceTheMailToItsSender`, `TestAClaudeHolderWithNoSessionIDIsNotMailable`; broker
   `TestAnAddressOnceReportedStaysValidAndMailQueuedForAnUnapprovedLabelBouncesWithTheLabelReason`,
   `TestSendRefusesAnUnknownLabelAnUnreportedNameAndAnUnreportedSender`; node `TestTwoLiveRegistrationsWithOneSessionIDMakeTheAddressAmbiguousAndTheSenderIsBounced`,
   `TestTheNodeSkipsTheSocketRingWhileALiveWaiterIsArmedAndForADeadRegistration`; mailbox
   `TestResolveFollowsTheAddressOrderLiveCurrentThenLivePreClearThenNothing`, `TestRefreshKeepsEverySessionIDAndRefusesAFileOfANewerMinor`
8. The broker alone derives names; discovery and `who`: broker `TestAMailNameIsDerivedFromTheSessionIDAndSurvivesAResume`,
   `TestDiscoveryPrintsMailAddressesAndAcceptsOneAndNeverFallsBackToTheHandle`
9. Socket ring only after the kernel names the PID: node `TestTheNodeNeverRingsASocketWhoseKernelPeerIsNotTheRegisteredPID`,
   `TestTheNodeRingsAClaudeSocketOnceWithNoContentAndOnlyForThePeerItNamed`
10. Waiter, socket ring with no waiter, refuse stand-down, replacement: cli `TestTheWaiterWakesAnIdleSessionWithStatusTwoAndLeavesTheMailInPlace`,
    `TestAWaiterFacingCrossSessionInboundRefuseStandsDownInEveryScope`, `TestANewerWaiterReplacesTheOlderAndADayOfTurnsLeavesOneWaiter`; node
    `TestTheNodeRingsAClaudeSocketOnceWithNoContentAndOnlyForThePeerItNamed`
11. No wake path removes mail, each message wakes at most once: cli `TestTheWaiterWakesAnIdleSessionWithStatusTwoAndLeavesTheMailInPlace`,
    `TestTurnHooksAndIdentifiedHolderCallsRingWithoutRecordingOrRemoving`; mailbox
    `TestClaimReturnsEachKeyToExactlyOneCallerEvenWhenTheyRace`; node the socket ring test
12. Unknown versions: cli `TestMailOfAnUnknownVersionIsNeverRungOrReadAndStatusSaysWhy`; node
    `TestAnUnusableRegistrationMakesItsSessionUnknownSoItsMailIsNeverExpired`; mailbox `TestClaimNeverRewritesARingLedgerItCannotUseOrOfAHigherMinor`,
    `TestAnAcceptedEntryOfAnyVersionStillMeansTheMessageWasTakenAndIsNeverRewritten`,
    `TestAWaiterPresenceOfAnotherVersionIsNotReplacedAndStatusCanSayWhy`, `TestASentNoteOfAHigherMinorIsNeitherRewrittenNorListed`;
    node `TestADerivationNeverRewritesASidecarItCannotReadThatAppearedWhileItRan`; mailbox
    `TestAMessageOfAnUnknownMajorOrWithAMismatchedNameIsNeverReadAsMail`, `TestAnUnusableLedgerReadsAsEmptyAndOldKeysArePruned`
13. Identified-holder calls and turn hooks ring: cli `TestTurnHooksAndIdentifiedHolderCallsRingWithoutRecordingOrRemoving`,
    `TestSetupClaudePrintsTheMailHooksWithTheWaiterAsAsyncRewake`
14. Status, stages, bounce reasons, bounces wake and stages do not: broker `TestAReadReportAdvancesTheStageAndTellsTheSenderWithoutWakingIt`,
    `TestMailTheBrokerNeverSentBouncesToItsSenderAfterSevenDaysWithItsBody`, `TestABounceThatCannotReachItsSenderIsDroppedAfterSevenDays`,
    `TestQueuedMailBouncesAtOnceWhenItsNodeLaterReportsNoMailCapability`; node
    `TestMailForASessionNobodyResumesBouncesToItsSenderWithTheBodyAfterSevenDays`; cli the first test's stage lines
15. A sent message never bounces for expiry; a returning node reports first: broker
    `TestASentMessageNeverBouncesForExpiryAndAReportedStoreStopsTheResend`
16. Old node with new broker, new node with old broker: broker `TestAnOldNodeGetsNoMailAndTheSenderLearnsTheRecipientNodeIsTooOld`; cli
    `TestANewNodeWithAnOldBrokerReportsBrokerTooOldForMail`, `TestAnOldNodeIsToldToUpgradeBeforeMailIsHandedToIt`

### M2: opencode and Codex delivery

**Status:** DONE

- The opencode plugin rings its sessions through `session.prompt`, publishes presence, and a scripted test shows an
  idle opencode session waking and reading its mail
- The node rings Codex sessions with `codex queue`, using the codex path and `CODEX_HOME` from its LaunchAgent plist,
  and a scripted test shows an idle Codex session waking and reading its mail. A registration or a host process
  naming another path or `CODEX_HOME` does not change what the node runs. A session whose `CODEX_HOME` differs from the
  plist's gets no push, its message stays unrung, and `mail status` says why
- Codex turn hooks and the opencode plugin ring when mail is pending
- Whether `codex queue` reaches an embedded TUI with no daemon is recorded in the As built block

**As built:**

- Codex and opencode mail is enabled. Their mailbox registration is per conversation on a shared host; Claude Code
  keeps its pre-clear aliases. Process death and the existing per-session liveness rules prevent rings to dead
  registrations. See the run best guess in the Decision Log.
- Codex execution uses the executable and configuration home passed in the node's printed LaunchAgent arguments.
  Its environment uses fixed system paths and the home from the OS account database. Registration data supplies only
  the home comparison and session identity. Missing trusted configuration or a home mismatch leaves mail unrung,
  with the reason in status output. Failed queue commands return their ring claims for a later attempt.
- The opencode server plugin watches the inbox and publishes presence while polling its known sessions. The Go helper
  owns version checks and ring claims; the plugin sends only the ring through the installed API's text field. A
  refused prompt returns the claim. Status reports missing or stale plugin presence. Codex turn hooks and the
  opencode plugin provide reminders without consuming message content. The opencode context hook repeats unread
  reminders in later model steps, including after a tool result, without touching the waking ledger or adding a turn.
- Scripted trials on macOS 26.6.2 with Codex 0.167.0 and opencode 2.2.6 woke idle sessions and ran explicit mail reads,
  using private agent homes and fake model providers. The node's durable mailbox output is the live fixtures' input;
  process-boundary tests cover the node's trusted execution arguments and ledger behavior. No production broker or
  real model was used. The same Codex trial reached an embedded TUI with no daemon: queue exited 0, the session read
  its mail, and no control socket existed before or after the command.
- Milestone review found that a local PID absence could classify a foreign-machine registration as dead. Local process
  death is now checked only for the same machine; a foreign registration stays unknown and cannot be reaped or expire
  its unread mail on that basis. A regression failed before the repair and preserves genuine local-host death too.
- Review also found that the opencode watcher stopped reminding once a waking key was claimed. A scripted trial
  reproduced it by ignoring the wake and inspecting the subsequent turn and post-tool model requests. Both lacked a
  reminder. The installed context hook now adds a text object to the model's system context through a separate
  non-claiming helper. The repaired trial confirms both requests contain the reminder, no new wake is queued, and only
  explicit read consumes the message.
- Row review found that one failed initial session lookup, or an idle recipient beyond the first page, could leave
  that recipient unwatched indefinitely. Discovery now advances one page on each periodic tick, restarts after a
  failed lookup, and starts again after the last page. Known sessions keep being polled during lookup failures. An
  injected-clock regression reproduces both failures before the fix and reaches the idle recipient without a session
  event afterward.
- The Codex live harness needed a macOS Python fallback for non-reaping child exit observation. It uses the operating
  system's waitid through libc when Python exposes no os.waitid, so cleanup still owns each process group until reap.
- The opencode probe's tabs check failed with tabs disabled on the installed CLI. The committed harness and committed
  plugin reproduced it. The focused mailbox trial passed independently. The
  [run report](../reports/20261108-nit-007-m2-opencode-codex-delivery.md) records the attempts and remaining state.

## Decision Log

- 2026-10-18: Drafted from the lease broker research. The research framed the mailbox as optional, so nothing was
  settled at first.
- 2026-10-18: The user decided to build the mailbox. The open question "Whether to build it" was removed.
- 2026-10-18: Addressing settled on NIT-004's qualified names. Delivery settled on ADR-01's outbound streams. The wake
  questions were rewritten from the session wake path research. That research found a documented inbox socket for idle
  Claude Code sessions, replacing the draft's claim that third-party use of the socket was unknown.
- 2026-10-18: The user accepted the risk that the socket's undocumented body format changes. The user will test the
  Enterprise org's wake paths before talking to its admins.
- 2026-10-18: Source readings of opencode 2.0.32 and Codex 0.163.0 found idle wake paths in both. "Agents without a
  wake path" was rewritten to match. It had said both were unverified.
- 2026-10-24 (autonomous): "Where messages enter a machine" settled on the node storing each message in a local
  mailbox before it acks the broker, with a dedup ledger, version-gated sends, and broker persistence of queued mail.
  Converged in 1 pass. The option text that said every Claude Code session needs `crossSessionInbound: accept` was
  corrected from the current Claude Code docs.
- 2026-10-24 (autonomous): Added the open question "Mailbox registration". Handles are stored only in lease, request,
  and notice records, and a Claude Code handle changes when its process restarts.
- 2026-10-24 (autonomous): "Mailbox registration" settled on registration at start and on each mail call, keyed by
  identity fields, with the handle as the address and resume aliases matched by Claude Code session ID. Converged in 2
  passes. The second pass added the alias tombstone and identity-field storage. The Claude Code env-var docs confirmed
  that a resume keeps the session ID.
- 2026-10-24 (autonomous, judge): "Auth" settled on mail between enrolled nodes before NIT-006. Each label is pinned
  to a bearer key the user approves on the broker host, the key is checked on every request, and mail needs verified
  HTTPS. Judge after 3 passes, on the pass budget. The critic's top moved from a pulled-forward node credential, to a
  pinned keypair, to pinning plus per-request binding, HTTPS, and enrollment approval. Each move came from a new
  confirmed failure. The judge narrowed the keypair to a bearer key. It left one check for the user: whether the
  broker will run with a certificate the nodes trust. If not, the ruling falls back to waiting for NIT-006.
- 2026-10-24 (autonomous): Added the open question "Claude Code wake path", split from "Per-agent delivery adapters".
- 2026-10-24 (autonomous): Settled "Claude Code wake path", "Per-agent delivery adapters", "Agents without a wake
  path", and "Delivery guarantees", each converged in 1 pass. They share one rule: wake paths only ring, and only
  `nitipan mail read` consumes. The critic found that consuming on a push path loses mail that the session then drops.
  The Claude Code waiter rings first, with a socket ring when no waiter is armed, and stands down on any
  `crossSessionInbound: refuse`. Delivery extends NIT-001's adapters with a data-only descriptor. Every nitipan call
  and turn hook rings. Senders see stages, and only bounces wake them, with a 7-day constant.
- 2026-10-24 (autonomous): Milestones rewritten to match the settled design, with a check for each settled behavior.
  No queue row names them. Impacts and the Summary were brought in line. The NIT-001 Impacts entry had said every lease
  needs a session name. That was stale, since NIT-001 is frozen and mail registration does not depend on leases.
- 2026-10-24 (autonomous): Coherence fixes from the whole-proposal critic pass. The node takes the Codex executable and
  `CODEX_HOME` from the live host process, never from a registration a sandboxed session can write, and checks a
  socket's peer PID. Each message wakes a session at most once. Approval shows a key fingerprint and refuses a label
  with two pending keys. A reconnecting node drops mail the broker bounced meanwhile. "Forget label" bounces stored,
  unread mail. Mail for an unreported handle is refused at send. Registrations keep every session ID the process held,
  and a later one for the same identity replaces the earlier. Mail files carry versions. Bodies have a size cap.
  Extending Codex's session registration was rejected. The milestones gained a check for each.
- 2026-10-24 (autonomous): Risks reviewed. "Blocks every Claude Code wake path" was rewritten: channels are no longer
  a path, and mail still arrives through non-waking rings. Its pre-review check of managed settings has not happened,
  so it now says "before M1". The socket format risk dropped to low impact, since the socket carries only a ring. The
  prompt-injection risk now cites enrollment, not NIT-006. Added risks for NIT-004 changing what NIT-007 requires,
  a broker without a trusted certificate, mail formats as a contract, and lockout after key loss. The lockout risk is
  accepted, since "forget label" recovers it.
- 2026-10-24 (autonomous): Re-check fixes. Taking the codex executable and `CODEX_HOME` from the host process still
  let a sandboxed session choose them, since argv is spoofable. They now come from the LaunchAgent plist, and a risk
  records the exposure. A message could still be both bounced and read when an ack was lost near the 7-day expiry.
  Expiry now applies only to mail the broker never sent, and a reconnecting node reports what it stored and read
  first. That replaced the rule that the node drops mail bounced while it was away. Refusing approval for a label with
  two pending keys let a LAN host block approval. Approval by fingerprint now discards the other pending keys. The
  rung ledger is keyed by identity.
- 2026-10-24 (autonomous): The final re-check found that a Codex session under a non-default `CODEX_HOME` would be
  rung through the wrong daemon with no log. The node now compares the registration's `CODEX_HOME` with the plist's,
  and skips the push on a mismatch without marking the message rung. The proposal loop ran its 3 passes, so this fix
  was not re-checked.
- 2026-10-24: Review gate: kept all. The user kept "Auth" and the 7-day bounce, which narrows the settled "Delivery
  over the machine's own stream". The user confirmed that deployments will have certificates, most likely with TLS
  terminated at an ingress, and that tests may use a temporary CA. "Auth" now states the HTTPS rule for the node's
  hop. The user directed that NIT-004, now accepted, be amended directly for NIT-007's requirements.
- 2026-10-24 (autonomous): "Mailbox registration" reopened on new evidence. ADR-05, accepted with NIT-004 that day,
  requires a stored identity to stay the same for the same thing. The settled text addressed mail by the NIT-001
  handle, which changes on every Claude Code resume, and kept a 7-day alias that ADR-05's migration rule does not
  allow. It now keys the mail address on the session ID, derived only by the broker. A `/clear` forwards the
  pre-clear address to the live process, and a Claude Code holder with no session ID is not mailable. Converged in 1
  pass. "Qualified addresses" was amended to match, and NIT-004's "Qualified names" and "Discovery queries" were
  amended for the mail address.

- 2026-10-24: ADR candidate (cuts across the system): a delivery side channel only signals, and a message is consumed
  only by the recipient's explicit read once it is durable on the recipient's machine. Written as ADR-06.
- 2026-10-24: ADR candidate (cuts across the system): the unsandboxed node never runs or configures anything an agent
  sandbox can write. Written as ADR-07.
- 2026-10-24: ADR candidate (cuts across the system): nitipan honors each agent's inbound controls and never presents
  a credential that passes it off as part of a session. Declined: the user did not pick it.
- 2026-10-24: ADR candidate (cuts across the system): text from another machine reaches a model only from a machine
  the user enrolled. Declined: the user did not pick it.
- 2026-10-24: ADR candidate (hard to reverse): mail addresses keyed on the session ID. Declined: ADR-05 already states
  the principle, and the derivation is implementation.
- 2026-10-24: Accepted.
- 2026-10-24: NIT-008's review extended the rings to pending NIT-003 notices and requests. The ledger was the node's,
  and is now a state-directory file every waking path shares, so it holds with no node. The rule that `hook`
  subcommands do not ring now names the agent hooks, since NIT-005's git hook prints a recall's deadline.
- 2026-10-24: NIT-006's review settled broker auth, and NIT-006 was accepted. Amended in place, with markers: Impacts
  and "Auth" said NIT-006's credential replaces or wraps the pinned key. NIT-006 keeps it and binds its label to a
  user. "Auth" said NIT-006 had not designed a node credential, which is now stale. It gains how approval binds a user,
  that `sender_verified` keeps its same-user meaning, and that a label rebound to a different user bounces its queued
  mail. "Delivery guarantees" gains NIT-006's bounce reasons. The Summary and the prompt-injection risk said mail
  stays within one user until NIT-006. They now cite NIT-006's grant and reach rule.
- 2026-10-24: Scheduled as NIT-007 M1 and NIT-007 M2 in queue.md. NIT-005 M3 goes between them.
- 2026-11-08 (autonomous): M1 built the ring ledger. Impacts said "NIT-008 M3 builds this", for the ledger of rung
  keys. M1's own checks need it: a waiter with no node must ring once per message, and the node's socket ring must skip
  what a waiter rang. Two ledgers cannot give that. The ledger is the state-directory file in NIT-008's format, keyed by
  message ID. NIT-008 M3 adds the notice and request keys. NIT-008's sentence about a pre-M3 node keeping its own mail
  ledger was amended to match. The same amendment corrects the ledger's header: NIT-008 said it carries "NIT-001's
  envelope with `major.minor`", and it carries a version and its registration's identity.
- 2026-11-08 (autonomous): The mail name text said every mail name is derived "with its own domain separator", and that
  Codex and opencode names are the same four words as their handle. Both cannot hold, since a domain separator changes
  the words. The domain separator now applies to Claude Code names. Codex and opencode names use NIT-001's identity
  string unchanged, so they equal their handle. M2 inherits that. Each session the broker learns carries a derivation
  version.
- 2026-11-08 (autonomous): The text said that for an older node the broker "keeps the message and tells the sender
  'recipient node too old'". The broker now refuses the send with that error and queues nothing. It also bounces mail
  already queued for a node that later registers without mail capability, with the reason "the recipient node is too
  old". Keeping a message the node can never take would leave it to expire after 7 days with the wrong reason. The
  capability is a number in the node's registration, stored per node, so the answer holds while the node is offline.
- 2026-11-08 (autonomous): The body cap was "a fixed size". It is 64 KiB. The text fixed a cap and gave no number.
- 2026-11-08 (autonomous): Approve, pending, and forget reach the running broker through a unix socket beside its store,
  checked by the peer UID, never through a network route or a handoff kind. The store's flock rules out a second
  process editing the file, and ADR-08 requires a channel for proofs that no node carries. The settled "approves on the
  broker host" text already says this.
- 2026-11-08 (autonomous): A sender must be a mailable identified holder: a registered session that its node reported as
  live, with a mail name. Every bounce then has a mailbox to land in. A holder with no session ID cannot send.
- 2026-11-08 (autonomous): Labels the broker saw before mail shipped stay unpinned and keep NIT-004's live-stream key
  rule until approved. Mail needs the pin. "Forget label" also resets the sent flag on mail still queued, since an ack
  lost before a wipe can no longer be told from a message that never arrived. The next approved key receives it again.
  A stream that opened under another key also receives no mail, and approval closes it. Otherwise a host that took
  an unpinned label's stream while the machine slept would be sent the label's mail the moment the real key was
  approved.
- 2026-11-08 (autonomous): A store of an older minor is stamped with this broker's version as it loads. Otherwise a
  1.0 broker could open a file that already holds mail and drop it on its next rewrite.
- 2026-11-08 (autonomous): The waiter hook timeout is 86400 seconds. Claude Code documents no maximum for a hook
  timeout. The waiter reads `crossSessionInbound` from the user, project, local, and file-based managed scopes, which the
  settings page lists, plus the managed drop-in directory. See the As built block for what it cannot see.
- 2026-11-08 (autonomous): Only Claude Code sessions can send mail or register a mailbox in M1. The milestone's checks
  say "a session sends" and "a session registers", and Codex and opencode have no wake path until M2. Their sessions
  get a refusal that names the supported agents. M2 lifts the gate with their delivery. `mail read` and `mail status`
  ring nothing, because they show the mail. `mail send` rings pending mail like any identified-holder call.
- 2026-11-08 (autonomous): "Auth" said approval "pins the pending key that matches" the fingerprint. That depends on the
  bounded list of pending keys. A host on the LAN that registers fresh keys while the real machine sleeps can evict the
  real key from the list, and can keep a stream open so the real key's registration is refused and never recorded.
  Approval now also accepts a well-formed fingerprint no pending key has, and the label pins the first key that
  presents it. The fingerprint is a 64-bit prefix of the key hash, so a host cannot present a matching key without the
  user's. A malformed fingerprint, and an approval for a label pinned to another key, are refused. The cost is that a
  mistyped fingerprint is recorded and waits. `broker pending` shows it, and the approve output says nothing is pinned.
- 2026-11-08 (autonomous): Files under `mail/` that this binary cannot use, or that carry a higher minor, are never
  rewritten, and an unusable registration makes the sessions it may name unknown rather than dead. ADR-04's reader rules
  say so for lease records. M1 applies them to every mail file. A ledger that cannot be written may repeat a ring, which
  the settled design allows. The accepted-ID ledger instead treats any entry as the message taken.
- 2026-11-08 (autonomous): A node's request handoff version is 4 for the mail kinds, as the M2 and M3 precedents of
  NIT-004 bumped it for their kinds.
- 2026-11-08 (autonomous): The row review amended the text in place for decisions the log held alone. "A session on
  one machine sends" and "A session registers at start" said any session, and now say a Claude Code session, since
  M1 gates the other agents until M2. "Mailbox registration" gained that a send is refused unless its sender is a
  registered session that its node reported as live. "Auth" gained that an unpinned label's stream receives no mail
  until approval, which closes a stream opened under another key. "Auth"'s forget text said queued mail waits for
  the next approved key, and now also says mail the broker had sent without an ack is queued again.
- 2026-11-08 (autonomous): The broker read every session a node reported. "Mailbox registration" said the broker keeps
  every session ID each label reported, with no condition and no bound. It now reads reports only from a label pinned
  to the node's key, the first 256 per snapshot, only for an agent with a mail name and an ID safe in a file name,
  and it stores at most 10,000 per label. Each refusal is logged. The first condition follows "Auth", which says a
  pending label can neither send nor receive mail. The others bound what a node, or a host holding a pending key,
  can make the whole-file store and the per-send name derivation grow by. A mail to a label never approved is now
  "no such session" instead of waiting. A label forgotten after it reported keeps its addresses, so its mail still
  waits. The 10,000 bound is about 1 MiB per label and about 2.7 years at 10 new sessions a day. A label that
  reaches it needs a new design: no eviction exists, since an address once reported stays valid.
- 2026-11-08 (autonomous): "Delivery guarantees" covered mail event kinds between broker and node only. It said nothing
  of the handoff request version 4 between CLI and node, or of the store stamp. It now states each direction of the
  CLI, node, and broker trio, and that rolling the broker back below store 1.1 needs a pre-upgrade copy of the store
  and loses the approvals, sessions, and mail since.
- 2026-11-08 (autonomous): Two node rules from the row review. A file under `mail/` that this binary cannot decode, or
  that carries a higher minor, is never rewritten, and a registration refresh over one now fails. A reaped dead
  registration is removed only when it and each sibling file is decodable, of the binary's major, and no higher
  minor. Otherwise the file stays. ADR-04 says an old binary never removes a session file of a higher version or one
  it cannot decode. NIT-008's "removed with its registration" holds for files this binary can use. A leftover
  registration reads as dead because its process is dead, so nothing rings it.

- 2026-11-08 (run best guess): "Mailbox registration" described one process-keyed registration retaining every session
  ID. That merges concurrent Codex and opencode sessions on a shared host into one current conversation, which lets
  one session read another's mail. Their mailbox registrations now also key on the session ID, with one ID each.
  Claude Code retains process identity and pre-clear aliases. The registration and endpoint remain version 1.0:
  older CLIs refuse mail for these agents, and an older node never pushes to them. The optional Codex home is comparison
  data only. A pooled shared-host registration is unusable rather than guessed at or rewritten. Both node versions
  continue to understand Claude Code registrations. Only one node version runs at a time, under its existing flock.
- 2026-11-08 (run best guess): M2 extends the existing private fake-provider harnesses. Node process-boundary tests prove
  the trusted executable, home, target, and ring-only arguments. Live harnesses publish synthetic mail at the durable
  local mailbox boundary and prove the installed agent's wake path followed by explicit mail read. They do not need
  an enrolled production broker or a real model provider.
- 2026-11-08: "Per-agent delivery adapters" said embedded Codex queue delivery was unverified. The M2 scripted trial on
  Codex 0.167.0 observed queue exit 0, a ring-triggered mail read, and no control socket before or after the command.
- 2026-11-09 (run best guess): The opencode watcher discovers one page of sessions every five seconds, following the
  installed SDK's cursor.next token. A traversal starts again after its last page or a lookup failure. This replaces
  the one-time recent-session snapshot, which could never recover a failed lookup or reach an idle recipient beyond
  its first 200 entries. Existing sessions still poll while discovery is unavailable; a page limits lookup work rather
  than the set of recipients the plugin can watch.

## References

- `spec/reports/2026-10-18-lease-broker-research.md`: "Messaging"
- `spec/reports/2026-10-18-session-wake-paths.md`: how each agent takes messages, and which settings gate them
- `spec/reports/2026-10-18-opencode-wake-handoff.md`: opencode source findings and live tests
- `spec/reports/2026-10-18-codex-wake-handoff.md`: Codex source findings and live tests
- ADR-01: machines connect out to the broker
- ADR-05: mail addresses are stored identities, keyed on the session ID
- ADR-06: wake paths signal, and only a read consumes
- ADR-07: the node runs only input sandboxes cannot write
- NIT-004: the broker that holds the mailboxes
- NIT-006: broker auth, which binds each label to a user and opens mail across users
- ADR-08: broker authority needs a proof no session can make
