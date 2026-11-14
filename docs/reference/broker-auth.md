# Broker authentication

The broker authenticates users whose machines call it. Each node keeps a pinned bearer key. The broker binds each key's
label to one user. The broker owner is the user approved on the broker host. Other users sign in through OIDC on a page
the broker serves.

This page covers what that means for leases, discovery, and mail across users. Setting up the broker, its config, and
its users is in [Running the broker](../operations/broker.md) and
[Users and access](../operations/users-and-access.md). [Adding a coworker](../getting-started/coworkers.md) walks through
a first setup. The design is in [NIT-006](../../spec/proposals/NIT-006-broker-auth.md) and
[ADR-08](../../spec/adrs/ADR-08-broker-authority-needs-a-proof-no-session-can-make.md).

## Checking your user label

`nitipan who am-i` prints the caller's user label through the node. The user label is what `acting_for`, cross-user
mail, delegations, and grants use to name users.

## Lease reach across users

`user` reach is per user. Each bound user's `user` leases meet only that user's other `user` leases. Two users' `user`
leases on overlapping names do not conflict. Two labels of one user do. The sign-in page and some CLI help call this the user's "scope".

`shared` reach is one space that every bound user shares. A `user` lease and a `shared` lease on overlapping names
conflict, whichever user holds each. An overlap report never shows another user's mirrored `machine` lease, at any
reach.

While cross-user features are off, the broker treats every lease as one user's, as before. A label nobody approved
counts as the broker owner's then, and its claims for another user are refused.

## Delegating to a shared agent

A shared agent serves several users. It claims at a user's `user` reach only under that user's standing delegation to
the agent's label. Grant or revoke a delegation on the broker's sign-in page with "Grant Delegation" or "Revoke
Delegation". Each action needs its own sign-in. Type the agent's label on the form that follows. No request through a
node can grant or revoke a delegation.

The agent claims for a user with `nitipan acquire --acting-for <user label>`. The user tells the agent their user
label, which `nitipan who am-i` prints. The lease then counts among that user's `user` leases only. A claim for another
user without that user's delegation is refused, and the error names the sign-in page. A node from before this feature
answers such a claim with "node too old", and nitipan writes no request. A node whose broker is from before this
feature answers "broker too old", and forwards nothing.

A claim without `--acting-for`, or from an older nitipan, acts as the label's own bound user. The broker replaces any
other value it carries.

Revoking a delegation ends every lease the agent's label holds for that user. The agent's node writes the holder a
notice. An ended lease is never revived. `nitipan broker revoke-user` ends every lease held for that user, and every
lease the user's labels hold for others, the same way.

## Releasing and breaking across users

A lease is a user's own when its holder's label is bound to that user, or when its `acting_for` names that user. Any
of the user's labels may release or `break --force` it. That includes a lease a shared agent holds for the user.
Name the lease by its ID: `nitipan release <lease-id>`. A release by a label other than the holder's ends the lease,
and the holder's node writes its holder a notice.

A user may not `break --force` a coworker's lease. A coworker's live `shared` lease ends when its holder releases it,
when its term runs out, on a death report, or by a break from its own user. The broker owner may end any lease with
`nitipan broker break <lease-id>` on the broker host. The holder's node then writes a notice that names the broker
owner by user label, as in "broker owner <user label>". The refusal of a coworker's break names each of these ways
forward.

## Discovery across users

`nitipan who holds`, `nitipan who is`, and `nitipan list` show every bound user's `shared` leases. A coworker's
`user` leases and mirrored `machine` leases show only to that coworker, and to labels holding their delegation. A
lookup by holder name across users matches only `shared` leases. A lease whose owner the broker cannot resolve shows
only when it is `shared`.

While cross-user features are off, discovery shows every lease.

## A claim refused by a lease you cannot see

A coworker's hidden `user` lease can refuse your `shared` claim. The refusal then names no holder, path, node, or
user. It says the name is held at `user` reach by another user. Wait for that lease to end, or ask the broker owner.
Leases you can see are still listed in full. The refusal is an ordinary message, so an older nitipan prints it the
same way.

The coworker's node writes them one notice per lease per refused user. It names only their own lease and your user
label, never the name you asked for. The notice is delivered on the holder's next nitipan call. It never rings, wakes
no session, and prints nothing from a turn hook. The broker records each notice in the lease, so a broker restart
sends no second one.

## Mail across users

Mail between your own machines works as before. Mail to another user's session needs that user's grant, and cross-user
features on. Grant or revoke mail from a coworker on the broker's sign-in page with "Grant Mail From a User" or "Revoke
Mail From a User". Each action needs its own sign-in. Type the coworker's user label on the form that follows. No
request through a node can grant or revoke one. Mail to a shared agent follows the grants of the agent's own user, not
the users it acts for.

Even under a grant, a coworker's first message reaches only a session of yours that holds a live or lapsed `shared`
lease. A session you never made discoverable cannot be reached. Otherwise the send is refused, and the error says why.

The recipient answers with `nitipan mail send --reply-to <message-id> <sender address> [message]`. A reply goes from
the address that received the message to that message's sender. It needs no lease on either side, so a requestor
holding no lease still gets its answer. A reply is accepted for 7 days after the message it answers was read, and
refused after that. Every reply rides the grant that admitted the conversation's first message. An older nitipan
cannot send a reply. A node from before replies answers "node too old", and a broker from before them gets "broker too
old".

The broker checks the grant and the reach again before it delivers. A message bounces back to its sender with "sender
no longer allowed" once the grant was revoked. That covers every later message of the conversation, replies included.
A first message bounces with "recipient holds no `shared` lease" once the lease ended. A reconnecting node reports
what it stored and read before the broker re-checks anything, so no message is both read and bounced. A recipient node
too old for mail across users gets none. The sender is told "recipient node too old", and mail already queued for it
bounces. A sender node that old is refused at send.

`nitipan mail read` frames such mail with the sender's user outside the body:

```
=== message <id> ===
from: <mail name>@<label> (another user's machine, NOT one of yours)
from user: <user label> (stamped by the broker)
reply with: nitipan mail send --reply-to <id> <mail name>@<label>
```

It never carries the verified mark, which means one of your own machines sent the mail. An older nitipan shows it as
not verified, never as yours.
