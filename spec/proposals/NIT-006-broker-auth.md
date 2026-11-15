# NIT-006: Broker Auth

**Status:** implemented
**Created:** 2026-10-18
**Updated:** 2026-11-10

## Dependencies

- NIT-004

## Impacts

- NIT-001 — "Holder identity" and "Lease record fields". Auth must prove the `holder` user and `acting_for` user those
  fields name
- NIT-004 — "Which user a user-reach lease belongs to": "Session credential" and "Shared agents and acting_for" give
  the authenticated scope its revisit hook waits for. "Machine leases and user leases on the same path": "What auth
  authorizes" narrows who may `break --force` a `user` or `shared` lease. "Discovery queries": other users see only
  `shared` leases. "Broker protocol": cross-user features need a loopback or TLS listener. "Broker lifecycle and
  store": the store also holds key hashes, user bindings, admissions, delegations, and mail grants
- NIT-007 — "Auth" and the prompt-injection risk. "Node credential" wraps NIT-007's pinned key in a user binding,
  never replacing it, and the store keeps a hash of the key. `sender_verified` keeps its same-user meaning, and
  cross-user mail carries the sender's user in a new field. An OIDC binding on the broker page is the NIT-007
  approval. "Cross-user mail authorization" decides who may mail across users
- NIT-008 — "Telling the sub-lessee" and its ring rule. "`user` and `shared` leases across users" adds a notice kind
  that never rings, turn hooks included, as an exception to the rule that a pending notice rings

## Summary

The broker authenticates the users whose machines call it. Each node keeps the pinned key NIT-007 gives it, and the
broker binds the key's label to one user. The broker owner is vouched on the broker host. Other users sign in through
OIDC on a page the broker serves, against an identity provider their infrastructure already runs. No session signs
in. The node attests each session it serves, and the session acts as its label's user.

Users act on their own leases. A shared agent acts for a user only under that user's standing delegation, and its
lease counts in that user's scope. Mail crosses users only under a grant from the recipient's user.

## Motivation

Early broker versions run without auth, because the environment is high-trust. That stops working once leases cross
users at `shared` reach. A shared agent acts for several users. The broker must know which user each request acts for,
or one user's session can claim or break another user's lease.

## Design Decisions (Settled)

The first two come from the roadmap the user approved on 2026-10-18, recorded in the research write-up. The rest were
settled in design review on 2026-10-24.

### No auth at first

NIT-004 ships without auth. The record fields auth needs, `holder` and `acting_for`, exist from NIT-001 on. Adding auth
does not change the record.

### Self-hostable

Every piece runs inside the user's own infrastructure and auth. Remote Control is banned at the user's work. No piece
may depend on a hosted identity service the user does not run.

### Node credential

NIT-004's node heartbeats for every session on its machine. The research does not say whether the node holds its own
credential or forwards each session's.

NIT-007 settles a node credential ahead of this proposal, for mail only between one user's machines. Each node keeps
a random bearer key, the user approves each label on the broker host, and the broker pins the key and checks it on
every request, including the stream, sends, and acks. The answer here must take these into account:

- This proposal's node credential replaces or wraps the pinned key. It never runs beside it, since two node
  credentials would give two ways to act as one machine
- Machines already enrolled under NIT-007 need a migration path that does not strand their labels or their queued
  mail. ADR-05's migration rules apply to the label
- NIT-007 marks a message `sender_verified` when it comes from an enrolled node. A node credential that names a user
  extends that meaning to cross-user mail. It must not weaken it for single-user mail
- The node attests each session's identity, under NIT-004's "How sessions reach the broker". Mail and heartbeats are
  node calls, not session calls. M1's rule that "every broker call carries a session credential" cannot hold for them
  as written. Settling this question settles M1's wording too

Decision (autonomous): the node keeps NIT-007's pinned bearer key as its one broker credential, and the key's label
is bound to one user identity. The key is wrapped by that binding, never replaced, so no second node credential
exists.

- Binding a label to a user takes an identity proof and proof that the user can see the node's output, through the
  settled identity mechanism. Nobody can bind a key they do not hold. Amended 2026-10-24; see Decision Log
- The broker resolves each request's key to its label, uid, and bound user. A forwarded session request carries the
  node's attestation of the session's holder identity fields. The session's user is always the label's bound user,
  which the broker takes from the key, never from the request. One node serves one label, so no request can name
  another user
- The broker stores a hash of each pinned key and compares it in constant time. Once the owner confirms the upgrade
  and the kept previous file is gone, a copy of the store or a backup holds no usable credential. The key is random
  and high in entropy, so a plain hash is enough
- A broker-side "revoke user" command forgets every label bound to a user at once, for a coworker who leaves.
  "Forget label" still revokes one machine. Both close the label's live stream at once and refuse its key from then
  on, since a binding never expires and NIT-004 closes a stream only when its credential does
- Features that cross users need the broker's own listener on loopback, or serving TLS itself. An ingress on another
  host may terminate TLS and re-encrypt to the broker. An ingress that speaks plain HTTP over a private hop is enough
  for one user under NIT-004 and NIT-007, but not across users. The bearer key and the stream's mail cross that hop in
  the clear, and a host on the LAN can read or inject them. The broker checks its own listen address. A proxy on the
  broker host could still serve it as plain HTTP on the LAN, so nodes check their own hop too
- Each new node reports at registration whether it reached the broker over HTTPS. Once a broker reports cross-user
  features on, the node records that in its state directory and never sends its key over plain HTTP again, so an
  attacker who strips the flag from a later response gains nothing. The node's error says why
- The command that turns cross-user features on refuses while any label has reported plain HTTP, or is too old to
  report, unless that label has since been forgotten and approved again under a new key. A key that once crossed a
  plain hop may have been read there, and keys never rotate on their own. `nitipan node rekey` makes a new key for a
  forgotten label. The mark belongs to the key, and a later report or a forget does not clear it. The command judges
  each label by its current key, so a forgotten label no longer blocks, and approving it again under the same key
  blocks again. Amended 2026-11-10; see Decision Log
- On upgrade, every label NIT-007 pinned is bound in one step to the broker owner's identity. Those labels were
  approved on the owner's broker host, so they are the owner's by construction. Cross-user features stay off
  broker-wide until that step runs, so no unbound label ever meets a second user's scope. The label and its key do not
  change, so ADR-05's migration rules are never triggered, and queued mail is untouched
- A label the broker saw but nobody approved, such as a lease-only node on plain HTTP, keeps NIT-004's behavior while
  cross-user features are off. The broker-host command that turns them on answers with every label that can still
  reach the broker and is bound to nobody, and prints them before its result. Once they are on, an unbound label is
  refused for leases and mail, with an error that names the approve step. Otherwise a coworker's host could register a
  fresh label, or take over a sleeping laptop's unpinned one, and act in the owner's scope. Amended 2026-11-10; see
  Decision Log
- While cross-user features are off, the broker serves only the owner's labels and unbound labels. A label bound to
  any other user is refused for every node-API call but register and pair. The error says cross-user features are off
  on this broker, and that the broker owner turns them on with `nitipan broker cross-user true`. A coworker can still
  bind on the page while they are off, so setup happens before they are turned on. Turning them off later refuses the
  coworker again, and leaves the coworker's binding, leases, and grants as they are. Amended 2026-11-10; see Decision
  Log
- Approving a label on the broker host binds it to the broker owner, and the approve output says so. A coworker's
  machine is bound through the page instead. Approve refuses a label a coworker bound until it is forgotten, so a
  repeat approval never takes the coworker's label and bounces their mail. Amended 2026-11-10; see Decision Log
- A label bound to a different user than it was before, such as a reissued Mac whose hardware UUID and uid repeat,
  starts clean. Mail queued for the label bounces to its senders, and every delegation to the label is dropped. The
  new user never receives the old user's mail or acts under the old user's delegations. "Forget label" and "revoke
  user" clear the label's user and keep its last bound user, so the next binding has something to compare against. A
  label counts as bound only while it is pinned under its node's current key. A forgotten key that registers again
  then acts for nobody once cross-user features are on, and in the owner's one scope while they are off. Delegations
  to a forgotten label stay and act on nothing until it is bound again. A binding to the same user keeps them, as for
  a reinstalled machine. Amended 2026-11-10; see Decision Log
- The store change is a new major version of NIT-004's broker store. NIT-004 keeps the previous file beside it, so a
  rollback starts an older broker from the pre-upgrade state. A rollback loses every binding, admission, delegation,
  and grant made since, and turns cross-user features off with them. An older broker can never read a coworker's
  binding as one of the owner's enrolled labels, or compare a presented key against a hash
- The kept previous file holds the hashes of NIT-007's keys. Every "forget label" and "revoke user" after the upgrade
  also removes those hashes from it, so a rollback never brings back a key forgotten since. The broker deletes the file
  only when the owner confirms on the broker host that no rollback is wanted, and the confirm output says a rollback
  is then impossible. Amended 2026-11-08; see Decision Log
- Binding a label rewrites every lease it holds to the newly bound user. A lease held for a user who still delegates
  to the label keeps that user, since the delegation authorized it. A rebind to a different user drops those
  delegations first, so all of the label's leases move. The upgrade is one case: it binds the pinned labels to the
  owner, so their leases move to the owner's scope. A label nobody approved keeps a legacy `acting_for` until it is
  bound. While cross-user features are off, the broker counts an unbound label's leases in the owner's scope, the only
  one there is, and the binding moves them out of it. Renewals carry only the node credential, so without the rewrite
  a stale value would outlive the binding, and a new `user` claim from the same person would land in a different
  scope and be granted over it. Amended 2026-11-10; see Decision Log
- `sender_verified` keeps its NIT-007 meaning: the sender is one of the recipient user's own enrolled nodes. Mail from
  another user's node carries the sender's bound user in a new field, and leaves `sender_verified` absent. Setting
  `sender_verified` on a coworker's mail would make an older reader show it as the user's own mail, against ADR-04's
  minor rule. The broker sends cross-user mail only to a node whose registered version supports the new field, and
  tells the sender "recipient node too old" otherwise, as NIT-004 gates mail
- M1's rule is reworded. Every call on the node API carries the node credential. A forwarded session request also
  carries the node's attestation of the session. The stream, heartbeats, mirrors, mail, and acks are node calls and
  carry only the node credential. The user proves identity once per label, at binding, not once per session. The
  broker's sign-in page is not part of the node API. It takes browser requests, under "Identity mechanism"

The key lives in the node's 0700 state directory, which every agent sandbox can read and write under ADR-07. A
session, including a prompt-injected one, can read the key and act as its label from elsewhere until the label is
forgotten. Every option shares this exposure. See Risks.

A short-lived node token was rejected. The pinned key that refreshes it sits on the same disk and crosses the same hop,
so the short life protects only against a leak of the token alone. It also adds a broker signing secret and an hourly
stream reopen. A node keypair was rejected. The private key is as readable by a session as the bearer key, and stream
events from the broker stay unsigned across the private hop, so a signature adds little once cross-user features need
TLS on that hop. Both a token and a keypair also need the broker to keep accepting the bearer key from older nodes,
since NIT-004 serves every path prefix a registered node still uses. The new credential would then run beside the old
one, or a rolled-back node would be locked out. Forwarding each session's own credential was rejected. It reverses
NIT-004's rule that no session holds a broker credential, the node still needs its own, and a credential kept where
sessions can reach it proves only the user, which the binding already proves.

### Session credential

- A short-lived per-session key that the user's identity signs.
- A token the broker issues after the user signs in.

The research lists both and picks neither. Either one needs a lifetime and a renewal rule.

Decision (autonomous): no session credential. A session's identity is the node's attestation of it, under NIT-004's
"How sessions reach the broker", and its user is its label's bound user. A binding lasts until "forget label" or
"revoke user", and never expires on its own.

- The user proves identity once per label, at binding. No session signs in, so no device flow waits on an unattended
  session
- The bound user is the authenticated identity that scopes `user` leases, which NIT-004's revisit hook on "Which user
  a user-reach lease belongs to" waits for
- A broker-side "list bindings" command shows each bound user, their labels, and each label's last-seen time, so the
  broker owner can find labels to revoke
- A user removed from the identity provider keeps the broker until someone runs "revoke user". See Risks

Every OS user's session can read the whole state directory under ADR-07. A credential the node mints per session, or
a key per session, can then be read by every other session of that user, so it isolates nothing. It proves only the
user, which the binding already proves.

A node-issued credential per session was rejected for that reason. It adds per-session state, a lifetime, and renewal
for no protection. A per-session key signed by the user's identity was rejected. It reverses NIT-004's rule that no
session holds a broker credential, and it needs a sign-in per session. A binding that expires and renews through the
identity provider was rejected. Unattended renewal needs a long-lived identity provider credential, such as a refresh
token, kept where every session can read it, which reaches more than this broker. A renewal that needs the user would
come due during sleep and drop the label's `shared` leases on wake, the drop NIT-004's revival exists to prevent. It
would also add a bound-then-unbound state in which a label's scope is undefined.

### What auth authorizes

The research covers identity only. It does not say who may break whose lease, or whether a shared agent may claim in a
user's scope without that user's approval.

Decision (autonomous): owners act on their own leases, the broker owner can act on any, and a shared agent acts for a
user only under that user's standing delegation. Amended 2026-11-10; see Decision Log.

- A user may claim in their own `user` scope and at `shared` reach. A lease is a user's own when its holder's bound
  user is that user, or when its `acting_for` is that user. So a user can release or break a lease a shared agent
  holds for them. A release from a label other than the holder's names the lease's ID. It ends the lease as a break
  does, so the holder's node is told and drops its anchor. A release by name, and a death report, stay with the
  holder's own node
- A user may `break --force` only their own leases. A coworker's live `shared` lease ends by release, its term, a death
  report, or a break by its own user or the broker owner
- The broker owner may break any lease, through a command on the broker host. Like approve, forget, "revoke user", and
  "list bindings", it acts on the broker directly and is never reachable through the request handoff. An agent
  session on the broker host is covered under "Identity mechanism"
- A shared agent may set `acting_for` to user U only while U holds a standing delegation to the agent's label. U
  grants and revokes it with an identity proof through the settled identity mechanism, the same way a binding is made.
  A request through the handoff alone cannot grant one, so a prompt-injected session of U cannot hand U's scope to a
  label
- M1's "match" is defined by this rule: a record's `acting_for` matches when it is the node's bound user, or a user
  who delegated to this label
- A request version from before this proposal carries an OS user name in `acting_for`. The broker treats it as
  unauthenticated and substitutes the node's bound user, even when the value names a user who delegated to the
  label. An older CLI's claims then keep working in its own user's scope, and none can claim for another user. A
  label nobody approved counts in the broker owner's scope, under M1's unbound-label rule, and its claim for another
  user is refused with an error that names the approve step. Amended 2026-11-10; see Decision Log
- A claim for another user rides a new request version, which carries that user's user label. An older node answers
  it with "node too old" rather than forwarding it under the old version, where the broker would substitute the
  agent's own user and the lease would silently count in the wrong scope. A node whose broker predates the field
  answers "broker too old" for the same reason. Amended 2026-11-10; see Decision Log
- Discovery shows `shared` leases to every bound user. `user` leases and mirrored `machine` leases show only to their
  owner and to labels holding the owner's delegation. A name lookup across users matches only `shared` leases.
  Mirrored leases carry local paths and names, which coworkers have no need to read. A lease or mirror whose owner
  the broker cannot resolve shows only when it is `shared`, so an unknown owner fails closed
- The scope, break, discovery, and overlap rules here apply only while cross-user features are on. While they are
  off, the broker runs one scope, as M1's unbound-label rule says: `user` leases conflict whoever holds them, any
  label may break any lease, and discovery hides nothing. Amended 2026-11-10; see Decision Log
- Overlap reports between wider-reach leases and mirrored `machine` leases, under NIT-004's "Machine leases and user
  leases on the same path", never show another user's mirror, at any reach. An overlap shows to the asker only when
  discovery shows the asker both leases. Across users they would leak the mirrored paths discovery hides. Amended
  2026-11-10; see Decision Log

This leaves one residual. The node keeps renewing a holder whose liveness reads unknown, under NIT-004's "Lease term
and renewal". A coworker's `shared` lease on such a holder then never ends by term or death report. Only that coworker
or the broker owner can end it. A coworker blocked by it asks either one.

Audit only was rejected. It accepts any `acting_for` unproven, and lets any bound user claim in or break another
user's scope, which Motivation exists to prevent. A notice after the fact cannot stop a prompt-injected session.
Letting any bound user break a stale or unknown `shared` lease was rejected. A disconnected node's leases already lapse
within one term, and an overlapping claim then takes them, so no break is needed. What remains is the unknown case,
where a coworker's session would break on a reading it cannot check while the holder may still work. Per-lease access
lists were rejected as a per-grant knob, which V1 callers cannot set, so a default rule is still needed.

### Identity mechanism

- OIDC for user identity, self-hosted through Keycloak, Dex, or corporate SSO. Device flow covers headless sessions.
  The research recommends this path.
- SSH signing through `ssh-keygen -Y sign` and SSH CA certificates. This fits where the user's infrastructure already
  runs an SSH CA.
- Both, with the broker accepting either.

Decision (autonomous): the broker owner is vouched on the broker host, with no identity provider. Every other user
proves identity by signing in through OIDC on a page the broker serves. A user proves identity only to bind a label or
to grant or revoke a delegation, since no session signs in.

- The broker owner's identity is a stable ID the broker assigns when it is first configured. The owner's labels are
  approved on the broker host, as NIT-007 approves them now, and bound to that ID. The upgrade step under "Node
  credential" binds to it. A broker with no identity provider configured serves the owner only, and cross-user
  features stay off
- Other users sign in on the broker's approval page with OIDC authorization code flow and PKCE, against an issuer the
  broker config names. To bind a label, the user enters the label and the one-time pairing code their node printed
  beside its fingerprint. The node made the code at random, four words from NIT-001's word list, and sent only its
  hash, under its pending key. The broker ties the code to that key, and a bind by the code pins that key and no
  other. A pending key may send a code while its label is bound to nobody, with cross-user features on or off. The
  broker answers with the label, which `nitipan node pair` prints. The code expires after 10 minutes. Five wrong
  codes from one signed-in user for one label lock that user out of that label until the code expires, so a coworker
  cannot discard someone else's pending key.
  `nitipan node pair` prints a fresh code and sends its hash, for a code that expired. The page never lists pending
  labels, so a user can bind only a machine whose output they can see. The page also grants and revokes delegations
  and mail grants. No token ever reaches a node. Amended 2026-11-10; see Decision Log
- Each page action runs its own OIDC round trip, with a fresh `state`, nonce, and PKCE verifier, and the page keeps
  no session cookie. The page accepts an action's parameters only on a form it renders after the callback. That form
  starts empty: the user types the label, the code, or the user label the action names, and the page shows the
  action in words before the confirming submit. It carries a single-use token tied to that round trip. Parameters
  sent before the sign-in are never acted on, shown, or prefilled. The page sends `frame-ancestors 'none'`, so no
  other site can frame it and trick a click. A cross-site request can start a round trip, and corporate SSO may
  finish it silently, but it cannot fill or submit the form. Typing the parameters and confirming is the human step
- Each user has a stable user ID the broker assigns at admission, never reused. The owner's ID is one of these. An
  OIDC sign-in resolves to a user ID through the issuer plus `sub` it was admitted by, which ADR-05 requires, since an
  email or a user name can be reassigned. The email is shown beside it as a label only. Each binding records its
  derivation version. Amended 2026-10-24; see Decision Log
- Each user has a user label: four words the broker derives from the user's stable ID through NIT-001's word list,
  with its own domain separator and a derivation version, the way machine labels are derived. `acting_for` in a broker
  request, the sender-user field on cross-user mail, delegations, and grants all name users by it. A collision
  between two users' labels is refused, naming both. `nitipan whoami` prints the caller's own, through the node. A
  shared agent learns a requesting user's label from the sender-user field of the mail that asked it. The local anchor
  record keeps NIT-001's `acting_for`, the OS user, so the record does not change
- An OIDC binding is the NIT-007 approval. It pins the label's key and discards every other pending key for the
  label, as approval by fingerprint does
- The broker owner admits each other user on the broker host, by issuer plus `sub`. A sign-in by a user who is not
  admitted is recorded as a pending admission with its email, which the owner lists and admits. Signing in alone
  binds nothing. With corporate SSO, any employee can sign in, and admission keeps them out. "Revoke user" also
  removes the admission, so signing in again restores nothing
- The owner can link an issuer plus `sub` to an existing user ID on the broker host. Linking the owner's own OIDC
  identity lets a label the owner binds through the page, such as from a work laptop, join the owner's scope rather
  than splitting the owner into two users. Linking a coworker's new issuer plus `sub` carries them across a change of
  provider or client, which can change every `sub`, with their bindings, delegations, and grants intact. The old
  issuer plus `sub` keeps resolving until the owner removes it, as ADR-05's migration rules require. Link refuses an
  issuer plus `sub` that already resolves to a user, and names that user, so no identity resolves to two users or
  moves silently
- Broker-host commands, such as approve, forget, admit, link, "revoke user", "list bindings", and the owner's break,
  act on the broker directly, as its OS user. They are not reachable through any node. An agent session running as
  that OS user on the broker host could run them. `setup` says to run the broker as an OS user, or on a host, where no
  agent session runs. See Risks
- The page checks ID tokens it receives from the token endpoint, and fetches only from the issuer the config names.
  See "OIDC token verification"

Each proof needs a browser sign-in and a human step on the page, so no session can produce one alone.

SSH signatures were rejected. `ssh-keygen -Y sign` signs with any key in `~/.ssh` or loaded in an agent, so a session
running as the user can produce the user's proof. Without an SSH CA, the allowed-signers file is operator vouching
with a signature on top, and a corporate CA's principals are often reused names, against ADR-05. OIDC device flow at
the node was rejected. Nothing ties the ID token to the label's fingerprint, so an attacker who starts a flow and sends
the user its code gets their own key bound to the user. Operator vouching for every user was rejected. Only the owner
could revoke a delegation, so a misbehaving shared agent keeps claiming in a user's scope until the owner is reached,
and the proof is the owner's judgment of an out-of-band message. OIDC for every user, the owner included, was
rejected. The upgrade step binds pinned labels to the owner before any identity provider exists, and a single-user
broker would need one to name its owner. Owner-admitted users who prove identity with a passkey on a broker page was
rejected for now. It works without an identity provider, but needs CBOR and COSE parsing the standard library lacks
and a passkey enrollment per user, while the research and the Summary expect a provider the user's infrastructure
already runs.

### Shared agents and acting_for

Moved from NIT-004, which deferred it here. Settle it before NIT-006 is accepted.

A shared agent serves several users and holds leases in the requesting user's scope. Its record's `acting_for` differs
from its `holder` user. Which scope's overlaps such a lease counts against is open:

- The requesting user's scope, named by `acting_for`
- The agent's own scope, named by its holder's user
- Both

The answer depends on "What auth authorizes": whether an agent may claim in a user's scope without that user's
approval. M1's refusal of a record whose `acting_for` does not match the authenticated identity also depends on it.
Read literally, that check refuses every shared agent's record.

Every CLI before this proposal writes `acting_for` as the OS user name. That value is not an authenticated identity,
and OS user names differ across one person's machines. Whatever this proposal picks must treat a legacy `acting_for`
as unauthenticated, for example by mapping it through the node's authenticated user.

Until this lands, NIT-004 runs one user scope per broker, and `acting_for` is carried unchanged and ignored for scope.

Decision (autonomous): the requesting user's scope. Every broker lease has exactly one scope, named by its
`acting_for`. That is the label's bound user, or a user who delegated to the label.

- A lease an agent takes for itself carries its own bound user in `acting_for`. A legacy value is replaced by the
  bound user, under "What auth authorizes"
- `acting_for` decides overlap only at `user` reach. At `shared` reach every bound user shares one scope, and
  `acting_for` decides only whose lease it is
- When U revokes a delegation, the broker ends every lease that label holds with `acting_for` U, and the holder's node
  writes a notice. "Revoke user" on U ends every lease held for U the same way. An ended lease is never revived. The
  node renews leases with only the node credential, so without this a revoked agent's leases would keep renewing in
  U's scope
- The agent's own working tree stays protected by NIT-004's anchor rule, which takes a `machine` lease in the agent's
  checkout whatever the scope

Counting the lease in the agent's own scope was rejected. U's own sessions and the agent's work for U would never
conflict, which is the bug in Motivation. Counting it in both scopes was rejected. An agent serving U and V on one
`repo:` name at `user` reach would refuse V because of U's lease, though V's work and U's are in separate scopes.

### Cross-user mail authorization

Added 2026-10-24 by this review. NIT-007's mitigation for prompt injection is that mail stays within one user's
machines until NIT-006. A woken idle Codex or opencode session runs an unattended turn on the sender's text. Once
cross-user mail opens, any bound coworker, or a prompt-injected session on a coworker's machine, could drive the
user's unattended agents. "What auth authorizes" covers leases and discovery. Who may send mail to whom across users
is open. A rule must exist before cross-user mail opens.

Decision (autonomous): a sender grant plus recipient reach. U accepts mail from V only after U grants V. Even then,
V's first message in a conversation reaches only a session of U's that holds a `shared` lease. Replies within that
conversation ride on the same grant.

- U grants and revokes V with an identity proof through the settled identity mechanism, as delegations are granted. A
  request through the handoff alone cannot grant one
- A conversation's first message goes only to an address whose session holds a live or lapsed `shared` lease, which
  is the reach cross-user discovery already has. A "taken" marker is not reach. A session U never made discoverable
  cannot be reached by a coworker at all
- A reply is mail from the address that received a cross-user message, to that message's sender. It needs no `shared`
  lease on either side, so a requestor holding no lease still gets its guardian's answer. Every reply is admitted by
  the grant that admitted the conversation's first message, never on its own
- A reply names the message it answers, in a new send request version. The broker keeps each cross-user
  conversation's grant in its store while it holds any of the conversation's messages, and until 7 days after its last
  read. An older CLI cannot send a cross-user reply. Amended 2026-11-10; see Decision Log
- A reconnecting node's stored-and-read report comes first, as NIT-007's "Delivery guarantees" requires. The node marks
  that report complete and sends it on every reconnect, even when it holds nothing. The broker holds mail from another
  user until the connection's complete report arrives, and the node sends a failed one again. The broker then re-checks
  only messages the node never stored, so no message is both read and bounced. Amended 2026-11-10; see Decision
  Log
- A reply is accepted for 7 days after the message it answers was read, the stage NIT-007's "Delivery guarantees"
  records. A guardian that resumes on day 6 and replies on day 8 still reaches its requestor. A reply to a message the
  broker has not seen read is refused, so the node reports reads before it forwards a reply. Amended 2026-11-10; see
  Decision Log
- The broker checks the grant and the reach rule at send, and again before delivery to the recipient's node. It
  bounces with "sender no longer allowed" when the grant was revoked, and "recipient holds no `shared` lease" when
  the reach lapsed. Revoking a grant bounces every later message of its conversations, replies included
- The recipient's user is the bound user of the label the address belongs to. Mail to a shared agent is governed by
  the agent's own user's grants. `acting_for` is a lease field and plays no part in mail
- "Revoke user" on V drops every grant and delegation naming V, in both directions. If V binds again under the same
  identity, V starts with none
- The broker accepts a cross-user send only from a node whose registered version knows cross-user mail and its bounce
  reasons. An older sender node is refused at send, with an error it reports as a broker refusal. Delivery to an older
  recipient node follows "Node credential"
- Mail across users needs cross-user features on, as every other cross-user rule does. While they are off, it is
  refused at send, and a queued message of a conversation bounces with "sender no longer allowed". Amended
  2026-11-10; see Decision Log

This leaves two residuals. A granted coworker's injected session can still reach every session U made discoverable,
and wake it. A revocation after a message is stored on U's machine, but before it is read, does not stop it, since the
node does not track grants and a ring follows the store at once under ADR-06. The second window is short, and U can
discard the message unread.

Per-address opt-in was rejected. The mark would live in a registration any same-user process can write, so one
injected session could open every one of U's addresses to every bound coworker. A grant alone was rejected. Every one
of U's sessions could be woken by any of V's, and a guardian's reply would need V to grant U too. A grant plus a reply
rule that waives the grant was rejected. A revoked sender keeps talking while a conversation runs. Any bound user with
no wake was rejected. Turn hooks still ring pending mail, so a busy unattended session reads it with nobody watching,
and an idle requestor never wakes for its reply. Any bound user with reach and replies, but no grant, was rejected. A
`shared` lease is taken to coordinate work on a repo, not as an identity-proven consent to mail from every coworker.

### Version skew

Added 2026-10-24 by this review. The broker, each node, and each nitipan binary are separate deploy units under
NIT-004's "Version skew". This proposal changes contracts between them. Each direction follows ADR-04's minor rule, and
no decision here relies on a deploy order.

Node and broker:

- Old node to new broker: an old node sends its key and its attestation as before. The broker takes the session's
  user from the key's binding, so nothing new is missing. An old node cannot show a pairing code, so its label is
  bound only by approval on the broker host, which binds it to the owner. An old node gets no cross-user mail, and its
  cross-user sends are refused at send
- New node to old broker: the node learns the broker's version at registration. `whoami`, pairing codes, and a
  cross-user reply report "broker too old". So does a claim for another user, since an old broker ignores the field
  that marks `acting_for` explicit and would substitute the label's own user. The broker says at registration that it
  reads that field, and that it reads a reply's field. The node forwards no such claim or reply to a broker that did not
  say so. A first message needs no such check. A node cannot tell that it crosses users, and a broker from before this
  proposal binds no label to a second user. Leases and single-user mail keep working under NIT-004 and NIT-007. Amended
  2026-11-10; see Decision Log

CLI and node:

- New CLI to old node: a claim for another user and a cross-user reply are new request versions, and `whoami` is a new
  request kind at version 4, so an old node answers "node too old". None is forwarded under an old version, where the
  broker would read it differently. Amended 2026-11-10; see Decision Log
- Old CLI to new node: the node keeps answering every request version it ever accepted. A claim from an old CLI carries
  an OS user name in `acting_for`, which the broker replaces with the label's bound user
- Old CLI reading mail from a new node: cross-user mail carries no `sender_verified`, which an old reader shows as
  unverified. An old reader does not show the sender-user field. It never shows a coworker's mail as the user's own

Broker store: the change is a new major version, under "Node credential". An older broker refuses the new file and
starts from the previous one NIT-004 keeps. The refused-claim notice record rides in that major version, with no
minor version of its own. Amended 2026-11-10; see Decision Log.

Raising a user label's derivation version follows ADR-05's migration rules, as machine labels do.

### `user` and `shared` leases across users

Added 2026-10-24 by this review. NIT-004 says `user` and `shared` leases on overlapping names conflict. Under this
proposal each bound user has their own `user` scope, every bound user shares one `shared` scope, and discovery hides
other users' `user` leases. Whether U's `user` lease and V's `shared` lease on overlapping names conflict is open. If
they conflict, the refusal can name a lease discovery hides from V. If they do not, V's `shared` lease does not keep
U's `user`-reach work off the same files.

Decision (autonomous): they conflict in both directions, as NIT-004 says. A refusal shows each conflicting lease only
at the detail discovery would show the asker. Amended 2026-11-10; see Decision Log.

- V's `shared` claim refused by U's `user` lease says only that the name V asked about is held at `user` reach by
  another user. It names no holder, path, or user. It tells V the ways forward: wait for the lease to end, or ask the
  broker owner
- U's node writes U a notice that a coworker's `shared` claim was refused by U's lease. The notice names only U's own
  lease key and V's user label, never the name V asked for, so V puts no text of its choosing in front of U's agent.
  It is delivered on U's next nitipan call and never rings, turn hooks included. Under NIT-008 a pending notice rings,
  and a refused claim costs V nothing, so a ringing notice would let V wake U's idle agents with no mail grant. This
  notice kind is an exception to NIT-008's ring rule. At most one such notice goes out per lease per coworker. The
  broker records the coworker in the lease before it sends the notice, so a restart sends no second one, and a
  notice lost on the way is not sent again. U can then release or reach out, so V is not left only with the broker
  owner
- A label holding U's delegation, and U asking against V's visible `shared` lease, get the full refusal NIT-004 gives
- A refusal by a hidden lease is a message in the shape every CLI already prints, with only the leases the asker can
  see. A CLI from before this proposal never prints an empty holder, so no new response version is needed. Amended
  2026-11-10; see Decision Log

This leaves one residual. V can probe `shared` claims across a repo to learn where coworkers hold hidden `user`
leases, one yes or no per claim. V must already have the repo, and each probe that succeeds takes a real lease. A
coworker's unrelated fork and real work on the same repo also look alike to V, since the refusal hides the remote
label. U's notice covers both.

No conflict across users was rejected. A `shared` caller asked to coordinate with everyone, and a `user` claim granted
silently over it is the silent non-coordination NIT-004 ranks below a visible refusal. A full refusal was rejected. It
reopens through the conflict path what discovery and overlap reports hide. Either one-directional rule was rejected.
One leaves V's `shared` lease unprotected. The other grants a later `shared` claim silently over `user` work in
progress.

### OIDC token verification

Added 2026-10-24 by this review. The sign-in page must verify ID tokens: discovery, the issuer's key set and its
caching, algorithm pinning, and the `kid`, `iss`, `aud`, `exp`, and `nonce` checks. go.mod has no networking or crypto
module today. Whether nitipan writes these checks over the standard library, or takes a module for them, is open.

Decision (autonomous): the code flow over the standard library, with claim checks and no signature check. OIDC Core
1.0 §3.1.3.7 step 6 says that when the ID token comes by direct communication between the client and the token
endpoint, TLS server validation may stand in for checking the token's signature. The settled flow is that case.

- The broker fetches the discovery document over TLS from the configured issuer, and refuses it unless its `issuer`
  equals the configured issuer exactly. It POSTs the code and PKCE verifier to the token endpoint the document names.
  It fetches the document on the first sign-in, and again after a failed fetch, so a provider that is down when the
  broker starts delays only sign-ins. Amended 2026-11-10; see Decision Log
- It checks `iss`, `aud` as a string or an array, `azp` when there are several audiences, `exp` and `iat` with a small
  leeway, and `nonce` against the value stored for that page action
- It asks only for `response_type=code`, and never reads an ID token from a redirect. The code exposes one exchange
  that takes a code, a verifier, and a nonce, and returns claims. No function verifies a raw token from elsewhere. A
  later feature that needs one, such as back-channel logout, needs signature checks and its own decision
- The HTTP client refuses redirects on the token POST, so the code and verifier never go to a host TLS did not
  validate
- The issuer and the discovery document's `token_endpoint` must both be `https`, except on loopback for tests, and both
  are verified against the same roots or CA file. The step 6 argument rests on TLS to the token endpoint, so a
  plain-HTTP endpoint would let a network attacker return a forged ID token. The broker config can name a CA file for
  a self-hosted issuer's private CA, as NIT-004's node config can. No option skips verification
- The broker is a public client with PKCE when the provider allows one. When it requires a client secret, the secret
  lives in the broker config file on the broker host, which no node or agent sandbox reaches
- Tests run against a local test issuer, including a wrong `iss`, `aud`, `nonce`, expired token, and redirecting
  token endpoint

Signature verification adds nothing in this flow. The key set would arrive over the same TLS path as the token, so an
attacker on that path could swap both. NIT-004's "go.mod has no networking dependency, and this adds none" still holds.

Hand-written signature verification was rejected. JWS checks are where OIDC clients fail, through `alg: none`, key
confusion, signature encodings, and key rotation gaps, and this flow does not need them. go-oidc with
`golang.org/x/oauth2` was rejected. It is correct and well used, but adds three modules for work this flow does not
need, and still leaves the nonce, state, PKCE store, and HTTP client to nitipan. `golang.org/x/oauth2` with
hand-written verification was rejected. It takes a module for one form POST and hand-writes the risky part.
go-jose alone was rejected for the same reason as signature checks.

## Design Decisions (Open)

None.

## Risks

Amended 2026-10-24; see Decision Log.

- **Auth arrives after callers depend on no-auth behavior** — likelihood: low, impact: medium.
  Scripts written against an open broker break when auth turns on. Mitigation: the broker protocol in NIT-004 carries
  an auth slot from its first version, and NIT-006 wraps the key NIT-007 already puts there. No session or script
  presents a new credential. Leases of unapproved labels change only when the owner turns cross-user features on,
  and that command lists them first.
- **Binding at upgrade strands enrolled machines** — likelihood: low, impact: medium.
  Machines approved under NIT-007 hold pinned keys and may have mail queued on the broker. Mitigation: the key and
  label never change. The upgrade binds every pinned label to the owner in one step, and cross-user features stay off
  until it runs, so no label is refused and no mail is held.
- **A session reads the node key** — likelihood: medium, impact: high.
  The key lives in the 0700 state directory, which every agent sandbox can read under ADR-07. A prompt-injected
  session can copy it and act as its label from another host: in its user's scope, toward coworkers who granted that
  user, and under any delegation the label holds. Every node credential option shares this. Mitigation: `nitipan
  broker forget` and `nitipan broker revoke-user` close the label's stream at once, and `nitipan broker bindings` shows
  each label's last-seen time. nitipan never moves the key into the Secure Enclave or the Keychain. Amended 2026-11-10;
  see Decision Log.
- **A user removed from the identity provider keeps the broker** — likelihood: medium, impact: medium.
  A binding never expires, so a departed coworker keeps their labels, delegations, and grants until the owner runs
  "revoke user". Mitigation: "revoke user" closes every stream, removes the admission, and drops every grant and
  delegation naming them, and "list bindings" shows last-seen times. The lag that remains is accepted, since
  expiring bindings would keep a refresh token where every session can read it.
- **An agent session on the broker host runs broker-host commands** — likelihood: low, impact: high.
  Approve, admit, link, revoke, and the owner's break act on the broker as its OS user. A session running as that
  user on that host could run them. Mitigation: none is reachable through a node, and `setup` says to run the broker
  as an OS user, or on a host, where no agent session runs.
- **A delegated label acts in the delegator's scope** — likelihood: medium, impact: medium.
  Any session on a label U delegated to, a prompt-injected one included, can claim in U's scope and read U's `user`
  and mirrored leases. It breaks only the leases it holds and the leases of its own user, never U's other leases.
  Accepted: a delegation takes U's identity proof on the broker page, and revoking it ends the label's leases for U
  at once. A narrower grant per action is a per-grant knob nobody named. Amended 2026-11-10; see Decision Log.
- **A granted coworker's session drives the user's agents through mail** — likelihood: medium, impact: high.
  A woken idle Codex or opencode session runs an unattended turn on the sender's text. Mitigation: mail crosses users
  only under the recipient user's grant, first messages reach only sessions holding a `shared` lease, replies ride on
  the first message's grant, and the broker re-checks before delivery. Content arrives as tool output, framed with the
  sender's user outside the body, and is treated as data.
- **Changing the identity provider or its client strands every coworker** — likelihood: low, impact: high.
  A provider may issue `sub` values per client, so a new provider or client gives every coworker a new issuer plus
  `sub`. Their bindings, delegations, and grants would no longer match. Mitigation: users are keyed on a
  broker-assigned user ID, and the owner's link command maps a new issuer plus `sub` to an existing user ID, as
  ADR-05's migration rules require.
- **Cross-user features stay off in a common deployment** — likelihood: medium, impact: low.
  NIT-007 expects TLS at an ingress. An ingress on another host that speaks plain HTTP to the broker leaves cross-user
  features off. Mitigation: the error says why, and an ingress that re-encrypts, or a broker serving TLS itself,
  turns them on. Single-user leases and mail are unaffected.
- **A rollback across the store change loses cross-user state** — likelihood: low, impact: medium.
  Mitigation: NIT-004 keeps the previous file, so an older broker starts from it. Every binding, admission,
  delegation, and grant made since is lost, and must be made again after the upgrade is retried. The kept file holds
  the hashes of NIT-007's keys, minus every key forgotten since, until the owner confirms no rollback is wanted.
  Amended 2026-11-08; see Decision Log
- **The design depends on accepted proposals not yet built** — likelihood: medium, impact: medium.
  NIT-006 changes decisions in NIT-004, NIT-007, and NIT-008, all accepted and not started. A later change there can
  undo what NIT-006 relies on, such as NIT-008 ringing the refused-claim notice. Mitigation: each was amended in place
  for each reliance, with markers, Decision Log entries, and an Impacts entry naming the decisions.
- **A key read on a plain-HTTP hop before cross-user features open** — likelihood: low, impact: high.
  A node on plain HTTP sends its key in the clear on every request, and keys never rotate on their own. A coworker who
  read one on the LAN would hold it once cross-user features open. Mitigation: turning them on is refused until every
  label that reported plain HTTP, or is too old to report, is forgotten and approved again under a new key. Afterwards
  a node never sends its key over plain HTTP.
- **A coworker probes for hidden `user` leases** — likelihood: low, impact: low.
  Each `shared` claim refused by a hidden lease tells V something overlaps the name. Accepted: V must already have the
  repo, each successful probe takes a real lease, and the holder gets a notice.
- **An identity provider outage blocks new bindings and grants** — likelihood: low, impact: low.
  Accepted: existing bindings, delegations, and grants keep working, and only new proofs wait.

## Milestones

Amended 2026-10-24; see Decision Log.

### M1: User identity and label binding

**Status:** DONE

- Every call on the node API carries the node credential, and the broker refuses a call without one. A forwarded
  session request acts as the label's bound user, whatever user its attestation or record names
- The broker stores only a hash of each pinned key, and compares keys in constant time
- On upgrade, every label NIT-007 pinned is bound to the broker owner's ID, its key and label unchanged and its queued
  mail delivered. Cross-user features stay off until that step runs, and while no identity provider is configured
- An unapproved label keeps NIT-004's lease behavior while cross-user features are off. Turning them on lists every
  such label first, and afterwards an unbound label is refused with an error that names the approve step
- While cross-user features are off, a label bound to a user other than the owner is refused for claim, list,
  who-holds, break, and mail send, naming `nitipan broker cross-user true`, and the owner's labels work. Turning them on
  serves the coworker, and turning them off again refuses the coworker and keeps their leases and grants. Amended
  2026-11-10; see Decision Log
- The store change is a new major version. An older broker refuses the new file and starts from the kept previous one.
  A key forgotten or revoked after the upgrade is refused after a rollback too. The kept file survives until the
  owner's confirm, whose output says a rollback is then impossible
- Binding a label rewrites every lease it holds to the bound user, except a lease held for a user who still delegates
  to it, the upgrade included. A new `user` claim from that user then conflicts with it. While cross-user features are
  off, an unbound label's lease conflicts with the owner's. Amended 2026-11-10; see Decision Log
- A second user, admitted by the owner on the broker host, signs in through OIDC on the broker page and binds a label
  by its label and the pairing code its node printed. The binding pins the key and discards the label's other pending
  keys. A wrong or expired code binds nothing, five wrong codes lock that signer out of that label without touching
  its pending key, `nitipan node pair` gives a fresh code, and the page never lists pending labels. A user who signs
  in without admission binds nothing, and appears as a pending admission. The binding pins the key that sent the
  code, and a signer revoked after the sign-in binds nothing. Amended 2026-11-10; see Decision Log
- Each page action needs its own OIDC round trip and a submit on the form rendered after the callback. The form starts
  empty of any parameter sent before the sign-in, and the page refuses to be framed. A cross-site request carrying an
  action's parameters, under a silent sign-in, changes nothing
- A sign-in resolves to the user ID it was admitted under. A changed email leaves the binding unchanged, and a linked
  new issuer plus `sub` reaches the same user ID. Each user gets a broker-derived user label, two colliding user
  labels are refused, and `nitipan whoami` prints the caller's own
- The owner links an OIDC identity to the owner ID, and a label the owner binds through the page joins the owner's
  scope. Approve on the broker host says it binds to the owner
- "Revoke user" forgets every label bound to that user, removes the admission, and drops every grant and delegation
  naming them in either direction. It ends every lease held for the user and every lease their labels hold, and each
  holder's node writes a notice. It is one store write, so a failed write leaves the user whole. Signing in again
  restores nothing. "List bindings" shows each user's labels and last-seen times. Each of `nitipan broker admissions`,
  `admit`, `link`, `bindings`, and `revoke-user` reaches the broker through its admin socket and prints what it did.
  Amended 2026-11-10; see Decision Log
- "Forget label" and "revoke user" close the label's live stream at once, and the key is refused on reconnect
- A label forgotten, then bound to a different user, bounces its queued mail and loses every delegation to it
- Once the broker reports cross-user features on, a node records it and refuses plain HTTP to the broker from then on,
  even when a later response omits the flag, and says why
- Turning cross-user features on is refused while a label has reported plain HTTP, or never reported its scheme, until
  it is forgotten and approved again under a key from `nitipan node rekey`
- Link refuses an issuer plus `sub` that already resolves to a user, and names that user
- No broker-host command is reachable through a node
- With the broker's listener on a private address in plain HTTP, cross-user features stay off and say why. With the
  broker serving TLS itself behind a re-encrypting ingress, they turn on
- Token exchange refuses a wrong `iss`, `aud`, or `nonce`, an expired token, a discovery document whose `issuer`
  differs from the config, a redirecting token endpoint, and a plain-HTTP issuer or token endpoint off loopback, in
  tests with a local test issuer. The broker refuses a config whose `redirect_url` is missing or plain HTTP off
  loopback. Amended 2026-11-10; see Decision Log

**As built:**

Store 2.0 adds users, issuer plus `sub` mappings, pending admissions, bindings, delegations, mail grants, conversations,
pairing codes, per-key scheme reports, the cross-user switch, and the owner's ID. The upgrade marks every key the 1.x
store held as one that never reported its scheme. It binds every pinned label to the owner and copies every 1.x field,
sessions, outcomes, and the counter included. It rewrites `acting_for` to the owner's user label on every lease a pinned
label holds. It finds those leases by the lease's `node` field, which names the holder's label, as the running broker's
binding does. A lease with no `acting_for` moves too, and no other field of the lease changes. The upgrade test stores
its leases in the 1.x grant shape and reads them back through the broker's lease decoder. An unpinned label's lease is
left alone. The upgrade keeps each label, its key, and its queued mail.

Admission is the only way a user record other than the owner's is made. Each user label is four words derived from the
user ID. A pairing code is four words from the word list. The broker stores its SHA-256 hash with the hash of the
pending key that sent it. A code expires in 10 minutes, and five wrong codes lock out one signer for one label.

The OIDC client runs the authorization code flow with PKCE over the standard library. It fetches the discovery document
on the first sign-in and keeps it once fetched, so a provider that is down when the broker starts delays only sign-ins.
Discovery requires an exact issuer match, and the token request refuses redirects. The claim checks cover `iss`, `aud`,
`azp`, `exp` and `iat` with 60 seconds of leeway, and `nonce`. HTTPS is required except on loopback, and a CA file is
supported. The client refuses a missing redirect URL, or one in plain HTTP off loopback.

The sign-in page is served on the broker's main listener, outside the node API's path prefix. Each action runs its own
OIDC round trip with a fresh state, nonce, and PKCE verifier. No session cookie exists. After the callback, an empty
form carries a single-use token, and parameters sent before the sign-in are never acted on. Every page response sends a
`frame-ancestors 'none'` CSP header. The bind action takes a label and a pairing code. The signer's admission, the code,
and the bind are judged under one lock, as the page's delegation and mail grant actions are. A revoke that lands inside
the bind cannot be timed without a hook in the handler, so its test pins the rule and does not reproduce the race. The
bind pins the key that sent the code and discards the label's other pending keys. A sign-in without admission records a
pending admission. The broker run passes the config file's `oidc` block to the broker, client secret and CA file
included, with its listen address and TLS setting. A provider that cannot be reached when the broker starts leaves the
broker serving, and the page answers that the provider is unavailable. A broker built from a config file was driven
through a sign-in at a local test issuer that requires the client secret, and turned cross-user features on against its
listen address and TLS setting.

The admin socket serves admissions, admit, link, bindings, revoke-user, and cross-user. Approve binds the label to the
owner's user ID through the same path the page's bind takes. The label's leases move to the owner, and a later rebind
from another user starts clean. An awaited fingerprint binds when its key arrives and pins, in the registration's own
commit. Approving a pinned, unbound label again under its key binds it. A label a coworker bound through the page is
refused until it is forgotten. The approve answer carries the owner's user label, and the approve output names the owner
it binds to. Link refuses an issuer plus `sub` that already resolves to a user. No admin command is reachable through
any node route.

The store assigns the owner when it opens. A new store, and a 2.0 store with no owner, get one in the same load, under
the store lock, persisted once. An upgraded store keeps the owner its migration assigned, and the open and the migration
create the owner the same way. A store whose owner ID names no user record gets the owner's record back in the same
load, under that same ID, since bindings point at it. A restart neither replaces nor rewrites the owner. So the broker
has an owner whenever its store is open. Approve with no owner record would pin the key alone, and a running broker
never reaches that state. The store stamps the owner's record, and each binding a migration makes, with its own clock.

One rule decides whether a label is bound. Its binding names a user, and the label is pinned under its node's current
key. Every scope, delegation, discovery, mail, whoami, approve, cross-user, and bindings decision reads that rule.
Forget clears the label's user and keeps its last bound user. A forgotten key that registers again is refused while
cross-user features are on. Delegations to the label then act on nothing until it is bound again. Bindings shows each
user, their labels, and last-seen times, for labels bound now.

While cross-user features are off, the broker serves only the owner's labels and unbound labels. The one scope decision
refuses a label bound to any other user. Every node-API call from it but register and pair answers with the step that
turns the features on. The page still binds such a label, and turning the features off keeps the coworker's state. An
unbound label is served while the features are off, and refused once they are on.

Turning cross-user features on is refused when no OIDC provider is configured, when the listener is neither loopback nor
TLS, or when any label's current key reported plain HTTP or never reported its scheme. A key's plain-HTTP mark is
sticky. A later HTTPS report under the same key, or a forget and a fresh approval of that key, still blocks. A forgotten
label has no current key and no longer blocks. Turning the features on answers with the labels that can still reach the
broker and are bound to nobody, and `nitipan broker cross-user` prints them before its result. The success case runs on
a fresh broker with the owner its store assigned. The refusal for an owner ID with no user record stays as a guard,
names that state, and is never reached.

Revoking a user is one store write. It forgets the user's labels, removes their admissions, and drops every delegation
and mail grant naming them in either direction, delegations to their labels included. It ends every lease held for them
or by their labels, and each holder's node writes a notice. Its answer lists what it removed. The broker-host commands
admissions, admit, link, bindings, and revoke-user reach their admin-socket routes and print what they did.

`nitipan node pair` prints the label the broker names, the key's fingerprint, and the pairing code. It sends the code's
hash with the machine headers through the node's own client, with its address rules, its CA file, and its refusal of
plain HTTP once cross-user features were on. The broker takes the code from a key waiting for approval under an unbound
label, with cross-user features on or off, and answers with the label. `nitipan node rekey` generates a new key. Node
pair was run against a TLS broker with cross-user features on and the label unbound, and the printed code then bound the
label on the page.

A binding to a different user than the label's last bounces every message the broker holds for the label to its senders
with its body. That covers mail queued and mail sent but unacked, under "the recipient's machine was bound to a
different user". The binding drops every bounce waiting for the label and every delegation to it. A binding to the same
user bounces nothing. The bounce and the binding read the broker's injected clock, on the page as on the broker host.
The page's pairing code expiry and sign-in action age read that clock too. A rebind needs an unpinned label, and only a
forget unpins one. The forget marks each queued message unsent, and the broker sends nothing to an unpinned label, so a
rebind never meets a message marked sent. The page refuses to bind a pinned label straight to another user, and leaves
its sent, unacked mail alone.

A node reports its scheme in each registration, and the broker records the report per key. The registration answer
carries the cross-user flag. A node records the flag and refuses plain HTTP from then on, even when a later answer omits
it. It fails closed when it cannot read the flag's file or directory. A claim's `acting_for` is rewritten to the label's
bound user unless it already names that user, so a label bound to one user cannot claim in another's scope. A forwarded
session request acts as the label's bound user.

Whoami is a new request kind at request version 4. The broker resolves the label to the user it is bound to, and to
nobody for an unbound label. `nitipan who am-i` prints the caller's user label.

Fingerprint matches, the awaited fingerprint match, and the per-request key check all compare in constant time.

Forgetting a label or revoking a user also deletes the label's node record from the kept 1.x file and marks it forgotten
there, as 1.x's forget did. So a forgotten key is refused after a rollback.

A sign-in against a real identity provider in a real browser was not run. It is optional and left to the user.

The divergences are in the Decision Log entries of 2026-11-10: the correction of fact in M1's As built, M1's milestone
review, and the row review.

### M2: Authorization and shared agents

**Status:** DONE

- Once cross-user features are on, `user` leases of two bound users on overlapping names do not conflict. Two labels
  of one user do. While they are off, both conflict. Amended 2026-11-10; see Decision Log
- U's `user` lease and V's `shared` lease on overlapping names conflict both ways. V's refusal names no holder, path,
  or user, and says what V can do next. U gets one notice per lease per coworker, naming only U's lease key and V's
  user label, and it never rings, through a wake path or a turn hook. A label holding U's delegation gets the full
  refusal. The refusal is a message every CLI already prints, so a CLI from before this proposal never prints an
  empty holder. Amended 2026-11-10; see Decision Log
- An overlap report never shows another user's mirrored `machine` lease, at any reach. Amended 2026-11-10; see
  Decision Log
- A user can break only their own leases, and the broker owner can break any from the broker host. A coworker's
  `break --force` of another user's live `shared` lease is refused
- A shared agent's claim with `acting_for` U is refused without U's delegation, and granted with it. The lease counts
  against U's `user` scope only, and U can release or break it
- A delegation granted through the request handoff alone is refused. One granted with an identity proof on the broker
  page takes effect, and revoking it ends the agent's leases for U with a notice. Those leases are never revived
- A request version from before this proposal has its `acting_for` replaced by the node's bound user, and still claims
  in that user's scope. A claim for another user from a new CLI to an old node gets "node too old", and one through a
  new node to an old broker gets "broker too old". Amended 2026-11-10; see Decision Log
- "Revoke user" on U ends every lease held for U
- Discovery shows another user's `shared` leases, and hides their `user` leases and mirrored leases unless the asker's
  label holds their delegation

**As built:**

Scope follows the cross-user switch. Once it is on, a `user` claim conflicts only within the user its `acting_for`
names. While it is off, the broker runs one scope. A label nobody approved counts in the broker owner's scope, which
meets M1's check for unbound labels. Binding the label later moves its leases to the bound user, except a lease held for
a user who still delegates to it. The rewrite runs on the broker's in-memory leases and persists with them.

A refusal across users is filtered before the broker stores the outcome, so a replay returns the same bytes. The
redacted refusal is a plain message, which every CLI prints as it is. The holder's notice is recorded in the lease by
the write that stores the outcome, and the event goes out only after that write, so a broker restart sends no second
one. No test drives a Codex or opencode wake for the notice, since the node's wake path rings only stored mail. Overlap
reports follow the discovery rule for the asker, so no reach shows another user's mirror.

A break of a lease that already ended answers ended to any node, and carries the lease only to a node that can discover
it. The owner's break on the broker host names the owner by user label, since the owner may have no email. `nitipan
release <lease-id>` sends an ID with no record on the machine to the broker while a node runs, which is how a user
releases a lease a shared agent holds for them.

A claim for another user reaches the broker only when the broker said at registration that it reads explicit
`acting_for`. Otherwise the node answers "broker too old". A claim without the mark acts as the label's bound user, even
when it names a user who delegated to the label.

The page's grant and revoke check the signer's admission under the same lock as the change, so a "revoke user" that
lands after the sign-in refuses them. Revoking a delegation or a user ends only live and lapsed leases, and leaves an
end fact as it was.

The notice record rides in store 2.0, and no store minor version was added. The divergences are in the Decision Log
entries of 2026-11-10.

### M3: Cross-user mail

**Status:** DONE

- Mail from V to U is refused at send without U's grant. With it, a first message reaches only a session of U's that
  holds a live or lapsed `shared` lease
- A mail grant made through the request handoff alone is refused, and one made on the broker page takes effect. Mail
  to a shared agent follows the grants of the agent's own user. "Revoke user" on V drops V's grants both ways
- A reply names the message it answers. A reconnecting node's stored-and-read report comes before any re-check, so no
  message is both read and bounced
- A reply from the addressed session reaches a sender holding no lease, within 7 days of the read. A reply to a
  message read 8 days earlier is refused
- Revoking the grant bounces later messages of the conversation, replies included, with "sender no longer allowed".
  A recipient whose `shared` lease ended bounces new first messages with "recipient holds no `shared` lease"
- Cross-user mail carries the sender's user in its new field and no `sender_verified`. Mail between one user's own
  nodes keeps `sender_verified` as NIT-007 sets it
- An older recipient node gets no cross-user mail, and the sender is told "recipient node too old". An older sender
  node's cross-user send is refused at send. Both in tests

**As built:**

The page grants and revokes mail. Each action runs its own sign-in round trip, and the user types the sender's user
label on the empty form that follows. The signer's admission is checked under the same lock as the change. A label no
user has, and a grant to oneself, are refused. No node route grants or revokes one. M1's "revoke user" already dropped
the grants naming the user in both directions. A user admitted again gets a new user ID, so V starts with no grants.

Mail between two users carries the sender's user label in its own field, from the broker's record through the stream
event to the stored message, and leaves the verified mark out. Mail between one user's labels keeps the mark. A label
nobody bound counts as the broker owner's, as M1's scope rule does. `nitipan mail read` prints the sender's user on its
own line, says the mail is not from one of the user's machines, and shows the reply command. A forged file that also
carries the verified mark is framed as another user's. Neither the store nor the mailbox format changed version, since
the new fields are additive and an older reader drops them. A stored message is never rewritten.

The mail capability rose to 2. A node at level 1 still gets mail from its own user's machines. A cross-user send from
a level-1 node is refused with its own code. A level-1 recipient makes the send fail with "recipient node too old",
and mail queued for it bounces with the same reason when it comes back that old. Mail the broker never sent it bounces
at once. Mail sent to it as a newer binary and never acked waits for that connection's complete report, which advances
what the node stored, and bounces only if it is still queued then. So the broker never bounces a message the old
binary may show its reader.

A first message needs cross-user features on, the recipient user's grant to the sender's user, and a live or lapsed
`shared` lease held by the session at the address. A `user` lease and a taken marker are not reach. The recipient's
user comes from the address's label alone, so a shared agent's mail follows its own user's grants.

A reply names the message it answers on request version 6, through `--reply-to`. An older node answers it "node too
old", and a node whose broker did not say at registration that it reads replies answers "broker too old". The node
reports pending reads before it forwards a reply. The broker accepts a reply only from the address the message was
sent to, to that message's sender, after its read and within 7 days of it, while the conversation's grant stands. The
conversation record is keyed by its first message. The broker keeps it while it holds any of its messages, and until
7 days after its last read. The first message can be swept while the conversation's replies are held. A new message
under that ID would rebind the conversation to another grant, and let a revoked sender's queued reply through. So a
message whose ID already names a conversation the broker holds is refused at send. A repeat of a message the broker
still holds gets its stored answer, as before.

Before delivery, the broker re-checks the conversation's grant, and a first message's reach. It bounces with "sender
no longer allowed" or "recipient holds no `shared` lease". It holds mail from another user, and re-checks none of it,
until the connection's complete report arrives. The node sends that report on every reconnect, even when it holds
nothing, and sends it again on the mail tick when it failed. The divergences are in the Decision Log entry of
2026-11-10 for M3.

A complete report vouches only for its own connection, which is one registration. A stream left over from an earlier
registration never re-checks, even after its successor's report. The report also says nothing of mail sent after it.
So the re-check skips mail from another user sent since the node last registered, until the node acks it or reports
again on its next connection. A stream whose connection dropped without the broker noticing then cannot bounce a
message the node stored. Both were proved with the node's first stream still open on the broker throughout.

## Decision Log

- 2026-10-18: Drafted from the lease broker research. Settled decisions are limited to the roadmap the user approved
  that day.
- 2026-10-24: Added the open question "Shared agents and acting_for", moved from NIT-004's design review at the user's
  request. It blocks acceptance.
- 2026-10-24: NIT-007's design review settled a pinned per-label node key for single-user mail. "Node credential"
  gained the constraints that follow from it, including a note that M1's session-credential rule cannot hold for node
  calls as written. A risk was added for the cutover from that key. Nothing was settled here.
- 2026-10-24 (autonomous): "Node credential" settled on NIT-007's pinned bearer key, wrapped by a binding to one user
  identity made over the key's fingerprint. The broker stores key hashes, gains "revoke user", and needs a loopback or
  TLS listener for cross-user features. Pinned labels are bound to the broker owner on upgrade. `sender_verified`
  keeps its same-user meaning, and cross-user mail carries the sender's user in a new, version-gated field. M1's
  session-credential rule becomes node credential plus node attestation. Converged in 1 pass. The critic's spike
  option, a non-exportable Secure Enclave or Keychain key, goes to the risk review.
- 2026-10-24 (autonomous): "Session credential" settled on no session credential. The node attests each session, and
  a label's binding lasts until "forget label" or "revoke user". A "list bindings" command shows last-seen times.
  Converged in 1 pass. The recommendation moved from an expiring binding, since unattended renewal keeps an identity
  provider credential where every session can read it and drops leases across a sleep.
- 2026-10-24 (autonomous): "What auth authorizes" settled on owner rules, a broker-owner break on the broker host, and
  standing delegations granted with an identity proof. A pre-NIT-006 `acting_for` is replaced by the node's bound
  user. Discovery hides `user` and mirrored leases from other users. Converged in 1 pass. The recommendation moved from
  also letting coworkers break a stale or unknown lease, since lapse already covers a disconnected node.
- 2026-10-24 (autonomous): Added the open question "Cross-user mail authorization". No option for "What auth
  authorizes" said who may send mail across users, and NIT-007's prompt-injection mitigation rests on mail staying
  within one user until NIT-006.
- 2026-10-24 (autonomous): "Identity mechanism" settled on the broker owner vouched on the broker host, and every other
  user signing in through OIDC on a broker-served page, keyed on issuer plus `sub`. An OIDC binding is the NIT-007
  approval. The owner admits each user and can link their own OIDC identity. Broker-host commands are not reachable
  through any node. Converged in 1 pass. The critic's passkey option ranked second. It needs no identity provider,
  but adds CBOR and COSE parsing and departs from the research's provider path.
- 2026-10-24 (autonomous): "Shared agents and acting_for" settled on the requesting user's scope, named by
  `acting_for`, at `user` reach only. Revoking a delegation, or the user, ends the leases held for that user.
  Converged in 1 pass.
- 2026-10-24 (autonomous): "Cross-user mail authorization" settled on a sender grant made with an identity proof, plus
  recipient reach: a conversation's first message goes only to a session holding a `shared` lease, and replies ride
  on the first message's grant. The broker checks at send and before delivery. "Revoke user" drops grants and
  delegations naming the user. Converged in 2 passes. The second pass found that a reply rule waiving the grant lets
  a revoked sender keep talking, and that a reply window counted from send bounces a guardian that resumed late.
- 2026-10-24 (autonomous): Milestones rewritten to match the settled design. M1 had required a session credential on
  every call and a sign-in from headless sessions, and M2 had one check for all authorization. They became M1 for
  identity and binding, M2 for authorization and shared agents, and a new M3 for cross-user mail. No queue row named
  them. The Summary, Impacts, and References were brought in line. The Summary had said each session calls the
  broker with a short-lived credential of its own.
- 2026-10-24 (autonomous): Coherence fixes from the whole-proposal critic pass, which rated the draft unsound on four
  holes. The page had listed every pending label, so any admitted user could bind another person's machine. A label
  is now bound only by a one-time pairing code the node prints. Labels nobody approved had no rule once cross-user
  features opened. They keep NIT-004's behavior while those are off and are refused once they are on. A rollback
  across the store change would open auth or lock out every node. The change is now a store major version. Other
  fixes: the node attestation no longer checks a user the broker already takes from the key. Forget and revoke close
  the live stream. A label rebound to a new user bounces its queued mail and drops its delegations. "Revoke user"
  removes the admission, and admission by group claim was dropped, since nobody named needs it. Each page action runs
  its own OIDC round trip with no session cookie. Users get broker-derived four-word user labels. A claim for another
  user and a cross-user reply ride new request versions. The listener rule allows a re-encrypting ingress. Overlap
  reports stay within one user. "Version skew" was added. The owner's approve output says it binds to the owner.
- 2026-10-24 (autonomous): Added the open questions "`user` and `shared` leases across users" and "OIDC token
  verification", from the same pass. Before, NIT-006 said the page verifies tokens through the standard library's
  crypto, which the review had not weighed against a module.
- 2026-10-24 (autonomous): "`user` and `shared` leases across users" settled on a conflict in both directions, with
  refusal detail limited to what discovery shows the asker and a notice to the hidden holder. Converged in 1 pass.
  The "both scopes" rejection under "Shared agents and acting_for" no longer cites a refusal that names a hidden
  lease, since redaction removes that.
- 2026-10-24 (autonomous): "OIDC token verification" settled on the code flow over the standard library with claim
  checks and no signature check, under OIDC Core 1.0 §3.1.3.7 step 6. No module is added. Converged in 1 pass. The
  recommendation moved from go-oidc, since signature checks add nothing for a token taken from the token endpoint
  over TLS. Token-endpoint redirects are refused, the issuer must be `https`, a CA file is allowed, and a client
  secret, when required, stays in the broker config.
- 2026-10-24 (autonomous): Risks reviewed. The cutover risk is mitigated, since the key and label never change. The
  device-flow risk was removed, since no session signs in. Added risks for a session reading the node key, offboarding
  lag, an agent on the broker host, a delegated label, mail driving a user's agents, a provider change, cross-user
  features off behind a plain-HTTP ingress, a store rollback, the dependency on NIT-004 and NIT-007, probing for
  hidden leases, and a provider outage. The key-reading risk could not be rated lower, so it takes the higher rating,
  medium likelihood and high impact. Its mitigation waits on a spike before M1, with a revisit hook. The offboarding
  lag, delegated label, probing, and outage risks are accepted.
- 2026-10-24 (autonomous): "Identity mechanism" amended from the risk review. Users were keyed on issuer plus `sub`
  directly, so a new provider or client, which can issue new `sub` values, would strand every coworker. Each user now
  has a broker-assigned user ID, an issuer plus `sub` resolves to it, and the owner's link command can map a new one
  to any user.
- 2026-10-24 (autonomous): Re-check fixes from the proposal loop's second pass. A pairing code alone was matched
  against every pending key and could be guessed, so binding now takes the label and a four-word code, with five
  wrong tries allowed. Fresh round trips alone did not stop a cross-site bind under silent SSO, so actions now need a
  form the page renders after the callback. Forget and revoke keep the last bound user, so a rebind can be detected.
  A rollback could revive a forgotten key, so forgetting also strips the key from the kept file, and the file is
  deleted only on the owner's confirm. Stored leases kept a legacy `acting_for`, so the migration rewrites it. A
  same-host proxy could serve a loopback broker over plain HTTP, so nodes refuse plain HTTP once cross-user features
  are on. A refused claim's notice would ring under NIT-008 and carry V's text, so it names only U's key and V's user
  label and never rings. The token endpoint must be `https` too.
- 2026-10-24 (autonomous): Fixes from the proposal loop's third and last pass, which did not converge. These fixes
  were not re-checked. A node learned that cross-user features were on only after sending its key over plain HTTP,
  so nodes now report their scheme and persist the flag. Turning cross-user on is refused until every plain-HTTP or
  unreported label is re-keyed, and a risk records the earlier exposure. The `acting_for` rewrite assumed every label
  was the owner's. Binding any label now rewrites its unauthenticated `acting_for`, and while cross-user features are
  off an unbound label's leases count in the owner's scope. The refused-claim notice is an exception to NIT-008's ring
  rule, and NIT-008 joined Impacts and the dependency risk. Wrong pairing codes now lock out the signer, not the
  key. `nitipan node pair` and `nitipan node rekey` were added. The page form starts empty and refuses framing. Link
  refuses an identity that already resolves. The no-usable-credential claim now holds only after the owner's
  confirm.
- 2026-10-24: Review gate: kept all. The user kept the drop of the old M1 checks for a headless-session sign-in and a
  session credential on every call, and the removal of the device-flow risk. The user reopened nothing, including
  the pass-3 fixes that were not re-checked.
- 2026-10-24: ADR candidate (cuts across the system): broker authority is granted only by a proof no session can
  produce alone, never through the request handoff. Written as ADR-08.
- 2026-10-24: ADR candidate (cuts across the system): text a coworker chooses reaches a user's agent only under that
  user's grant. Declined: the user did not pick it.
- 2026-10-24: ADR candidate (cuts across the system): refusals, notices, and overlap reports show another user's leases
  only at the detail discovery shows. Declined: the user did not pick it.
- 2026-10-24: ADR candidate (cuts across the system): no piece depends on a hosted service the user does not run.
  Declined: NIT-004's and NIT-006's settled "Self-hosted" decisions already carry it.
- 2026-10-24: ADR candidate (cuts across the system): a machine has exactly one broker credential. Declined: it shapes
  only the broker and node contract, so it fails the Architecture test.
- 2026-10-24: Accepted. NIT-004, NIT-007, and NIT-008 were amended in place for its decisions.
- 2026-10-24: Scheduled as NIT-006 M1-M3 in queue.md.
- 2026-11-08 (autonomous): The kept previous store was said to hold NIT-007's keys "in the clear". NIT-007 stores only
  a hash of each pinned key, as its "Auth" says. The kept-file bullet and the rollback risk now say the
  file holds the hashes. The rule that "forget label" and "revoke user" also remove them from the kept file stays,
  since a retained hash would still let a forgotten key authenticate after a rollback. NIT-007 M1's row review found
  the stale wording.
- 2026-11-09: The user dropped the Secure Enclave and Keychain idea from the "A session reads the node key" risk.
  nitipan never reaches for either. The risk's revisit hook, which waited on a spike, is removed. Its mitigation
  stays "forget label", "revoke user", and "list bindings". A spike ran against the user's login keychain that day
  and was stopped. Its findings were never reviewed and are not recorded.
- 2026-11-10 (autonomous): M2's implementation narrowed four decisions. "What auth authorizes" had said a user can
  release a lease a shared agent holds for them, with no mechanism. A release from a label other than the holder's
  now names the lease's ID and ends it through the break path, since a plain delete would leave the holder's anchor
  behind. A release by name and a death report stay with the holder's node. The break and discovery rules had no
  stated gate. They now apply only once cross-user features are on, following M1's single-scope rule for that state.
  Discovery had no rule for a lease whose owner the broker cannot resolve. It now shows only when `shared`, failing
  closed. "`user` and `shared` leases across users" had said a refusal with no holder takes a new response version.
  The mark now rides only replies to claim requests at the request version this proposal adds, and the notice record
  is kept in the lease and sent at most once. "Version skew" gains the store minor version that record needs. The
  old-node bullet's 2026-10-24 marker moved to the decision's lead, which now carries this entry's marker.
- 2026-11-10 (autonomous, run best guess): M2's milestone review changed eight texts.
  1. "What auth authorizes" said nothing of a label nobody approved, and the broker passed its `acting_for` through.
     M1's check that such a label's lease conflicts with the owner's while cross-user features are off was therefore
     not met. The label now counts in the owner's scope, and its claim for another user is refused, naming the
     approve step. The old-version bullet also says now that a value naming a delegator is substituted too. The
     broker had honored the delegation on an unauthenticated value.
  2. The switch bullet covered only break and discovery, and M2's first check said two users' `user` leases never
     conflict. While cross-user features are off, the broker runs one scope, as the README says and as break and
     discovery do. The bullet and the check now say the scope rule waits on the switch too.
  3. The overlap bullet and its M2 check said overlap reports go only between leases of one user. The broker still
     reported a `shared` grant's overlaps across users, and failed open on an owner it could not resolve. They now say
     an overlap report never shows another user's mirror, at any reach, and reports follow the discovery rule. The
     bullet's own reason carries it: across users the report would leak the paths discovery hides.
  4. "Version skew" listed `whoami`, pairing codes, and cross-user mail as what a new node refuses against an old
     broker. An old broker ignores the explicit `acting_for` mark and would substitute the label's own user, so a
     claim for another user now gets "broker too old" too. The broker says it reads the mark with a flag in its
     registration answer. A comparison of broker versions was not used, since the version is a free-form build string
     that a development build does not order. The `acting_for` bullet and M2's old-node check name it too.
  5. "`user` and `shared` leases across users" said a refusal with no holder is marked only in replies to the new
     claim request version. No CLI read the mark, and the redacted refusal is a message every CLI already prints. The
     mark is gone, and the bullet and its M2 check say no new response version is needed.
  6. "Version skew" said the refused-claim notice record adds a store minor version, which an earlier minor refuses.
     No 2.0 store has shipped, and nothing was pushed. A 2.0 store without the record reads with no notices recorded,
     and a broker of this major version that predates the record drops it on a rewrite. The record now rides in store
     2.0 with no minor version. A rollback across the record could send a holder a second notice, which costs
     nothing.
  7. The risk "A delegated label acts in the delegator's scope" said the label can break in U's scope. The break
     rule lets it break only the leases it holds and the leases of its own user, never U's other leases. The risk
     now says so.
  8. "Node credential" said binding rewrites only a lease whose `acting_for` is not a user label. An unbound label's
     lease now names the owner's user label, so binding that label to a coworker left it in the owner's scope. Binding
     now moves every lease the label holds to the bound user, except one held for a user who still delegates to the
     label. The rewrite also goes through the broker's in-memory leases. It had changed only the stored copy, which
     the next lease write overwrote. The upgrade's rewrite is unchanged, since it binds only to the owner.
- 2026-11-10 (autonomous, run best guess): Correction of fact in M1's As built. The upgrade's `acting_for` rewrite
  found a label's leases by a `holder.label` field. A stored lease has no such field. It names its holder's label in
  `node`. So the upgrade rewrote no real lease, and its test passed only on hand-built lease JSON. The rewrite now
  matches on `node`, as the running broker's binding does, and the test stores leases in the 1.x grant shape. A
  pinned label's lease with no `acting_for` now moves to the owner too, since "Node credential" moves every lease the
  label holds. The M1 check and the decisions are unchanged.
  Approve on the broker host pinned the key and bound nothing, and its output named no owner. The As built said it
  bound to the owner, and cited a test that checks neither. Once cross-user features were on, a label the owner
  approved was refused on every call. Approve now binds the label to the owner through the page's bind path, an
  awaited key binds when it pins, and the output names the owner. A rebind to a different user bounced only stored
  mail. Forget had already reset queued mail to unsent, so the new user's node received it. The rebind now bounces
  every message held for the label to its senders with its body. The As built also placed the admin socket tests in
  internal/cli/node_pair_test.go, which holds none of them. The M1 checks are unchanged.
  Only the 1.x migration created an owner, so a fresh broker pinned on approve, bound nothing, and could never turn
  cross-user features on. "Identity mechanism" says the broker assigns the owner's ID when it is first configured. The
  store now assigns it at open, for a new store and a 2.0 store with none, and the refusal no longer names a
  nonexistent `nitipan broker upgrade-bindings` command.
  A store whose owner ID named no user record also left the broker with no owner. The cross-user refusal told the
  operator to restart, and a restart changed nothing. The store now writes the owner's record back under that same ID
  at open, since bindings point at it. The refusal stays for a state a running broker never reaches, and names it.
  The page's bind stamped its bounces and the binding with the process clock, not the broker's. The page now reads
  the broker's clock for the bind, the pairing code's expiry, and the sign-in action's age.
- 2026-11-10 (autonomous, run best guess): M1's milestone review amended "Node credential". The bullet said:
  "Approving a label on the broker host binds it to the broker owner, and the approve output says so. A coworker's
  machine is bound through the page instead." Approving a pinned label again under its key had done nothing. Once
  approve binds, a repeat approval of a label a coworker bound through the page would take that label for the owner,
  and bounce the coworker's mail. The bullet now adds that approve refuses such a label until it is forgotten.
- 2026-11-10 (autonomous, run best guess): M3's implementation narrowed four texts.
  1. "Cross-user mail authorization" said the reconnect report comes first, and nothing more. A failed report had only
     logged a warning, and a node holding nothing sent none. The node now marks the report complete and sends it on
     every reconnect, and again on its mail tick when it failed. The broker holds mail from another user until the
     connection's complete report arrives, and re-checks it only then.
  2. The reply window bullet did not say what happens before the read. A reply now needs the broker to have seen the
     message read, and the node reports reads before it forwards one. The conversation bullet also says now that the
     record stays while the broker holds any of its messages, since a conversation not yet read has no last read.
  3. Nothing said whether mail crosses users while cross-user features are off, when the broker runs one scope for
     leases. Carrying it then would stamp a coworker's mail as the user's own. It is now refused at send, and a queued
     message of a conversation bounces with "sender no longer allowed".
  4. "Version skew" said a new node reports "broker too old" for cross-user mail to an old broker. A node cannot tell
     that a first message crosses users, and a broker from before this proposal binds no label to a second user. Only a
     reply gets "broker too old" now. The broker says it reads replies with a flag at registration, as it does for
     explicit `acting_for`.
- 2026-11-10 (autonomous, run best guess): row review. The row review found that the milestones did not hold
  together in the shipped binary. These fixes changed eleven texts.
  1. "Node credential" said forget and revoke-user keep the label's last bound user. They kept its user as well, so a
     forgotten key that registered again acted as that user once its node record was gone. Forget now clears the user
     and keeps the last bound user, and a rebind compares against the last bound user. A label counts as bound only
     while it is also pinned under its node's current key, and one rule answers that for every decision. The bullet
     now says so. It also says delegations to a forgotten label stay and act on nothing until the label is bound
     again. The bullet drops delegations only on a binding to a different user, so a reinstalled machine bound to the
     same user keeps them, and forget alone does not drop them.
  2. "Node credential" said the switch refuses while a label reported plain HTTP, until it is forgotten and approved
     again under a new key. The broker kept one report per label and overwrote it on each registration, so a read key
     passed by reporting HTTPS once. It never cleared the report, so a forgotten machine blocked the switch for good.
     The mark now belongs to the key and is sticky, and the switch judges each label's current key. The bullet now says
     so. Keys from a 1.x store are marked as never reported, so an upgraded node's old key keeps blocking until it is
     rekeyed. The marks are never pruned, since a forgotten key's mark must outlive it. A host that registers fresh keys
     grows them, as it already grows the label table.
  3. The unbound-label bullet said the switch lists every unbound label first. It wrote them only to the broker's log,
     and the command's answer carried nothing. The answer now lists them, and `nitipan broker cross-user` prints them
     before its result. The bullet now says the list holds labels that can still reach the broker, so a forgotten label
     with no node is left out. The command did not exist, though the README listed it, and it was added.
  4. "Identity mechanism" said the node sends the code's hash with its pending key. The broker took it only from a
     bound label, through a route that refused unbound labels once cross-user features were on, and bound any pending
     key it found. `nitipan node pair` sent no machine headers, skipped the node's CA file, and printed no label. The
     broker now ties the code to the pending key that sent it, takes it from an unbound label, and answers with the
     label. A bind pins that key alone. The command goes through the node's own client. The bullet and M1's binding
     check now say so.
  5. M1's binding check did not say when the signer's admission is judged. The page checked it outside the lock that
     made the binding. It is now judged under that lock, as delegations and mail grants are, and the check says a
     signer revoked after the sign-in binds nothing. A test pins the rule. The race itself cannot be timed without a
     hook in the handler.
  6. "OIDC token verification" did not say when the discovery document is fetched. The broker run never passed its
     config file's oidc block on, so no sign-in route was mounted and the switch always refused. Passing it on made the
     broker fetch the document at start, and fail when the provider was down. That would take leases and mail down
     with the provider, against the risk "An identity provider outage blocks new bindings and grants", which accepts
     only that new proofs wait. The document is now fetched on the first sign-in and again after a failed fetch, and
     the bullet says so.
  7. "Node credential" did not say what the broker does with a coworker's label while cross-user features are off.
     The broker served it in its own scope, so a broker meant to run one user ran two. The run's best guess is that
     the broker then serves only the owner's labels and unbound labels. A label bound to any other user is refused for
     every node-API call but register and pair, with an error that names `nitipan broker cross-user true`. Binding on
     the page still works, so a coworker can set up before the switch. Turning it off again refuses the coworker and
     keeps their state. A new bullet and a new M1 check say so. The rule sits in the one scope decision, so leases,
     discovery, break, notices, and mail agree.
  8. M1's revoke-user check did not say what happens to the leases a delegation authorized. Revoking a user dropped
     the delegations to their labels, but the leases those labels held for other users stayed live. The revoke also
     ran as several store writes, so a failed write could leave a user half revoked. Its loop meant to drop
     delegations to the user's labels did nothing, since the inner maps are keyed by label. Revoke now ends every lease
     held for the user or by their labels, drops delegations naming the user in both directions, and does it all in
     one store write. The check now says so. A test pins the single write. It cannot reproduce a split write, since a
     failed first write also changes nothing.
  9. `nitipan broker admissions`, `admit`, `link`, `bindings`, and `revoke-user` had admin-socket routes, and the
     README listed them, but no command reached them. They were added, and M1's revoke-user check names them.
     `revoke-user` prints what it removed, so its admin answer now carries that list instead of no content.
  10. M1's token check did not cover `redirect_url`, which the README says must be https. Nothing checked it. The
      client now refuses one that is missing or plain HTTP off loopback, as it refuses such an issuer, and the check
      says so.
  11. "Version skew" called `whoami` a new request version. It is a new request kind at version 4. The bullet now
      says so. The "A session reads the node key" mitigation named "list bindings", which had no command. It now
      names the broker-host commands that exist.

## References

- `spec/reports/2026-10-18-lease-broker-research.md`: "Auth" and "Holder and acting_for"
- NIT-004: the broker this proposal secures
- NIT-007: the node key this proposal's node credential wraps
- ADR-01: every proof goes to the broker's address, and the broker never connects to a machine
- ADR-04: the minor rule behind keeping `sender_verified` same-user
- ADR-05: stable identifiers for users, and migration rules for the machine label
- ADR-07: why no credential kept in the state directory can isolate one session from another
- ADR-08: broker authority needs a proof no session can make
- NIT-008: the ring rule the refused-claim notice is exempt from
