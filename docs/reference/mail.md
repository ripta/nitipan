# Mail

A session can send a message to another session, on any of your machines, through the broker. It needs the broker and a
node on each machine, and the broker over HTTPS with a certificate the nodes verify. A node on plain HTTP gets leases
and no mail. [Going multi-machine](../getting-started/broker-and-nodes.md) sets them up.

The design is in [NIT-007](../../spec/proposals/NIT-007-session-mailbox.md) and
[ADR-06](../../spec/adrs/ADR-06-wake-paths-signal-and-only-a-read-consumes.md). Mail to a coworker's session is in
[Broker authentication](broker-auth.md#mail-across-users).

## Addresses and commands

The address is `<mail name>@<label>`. `nitipan who holds` and `nitipan who is` print it. It is keyed by the session ID,
so it survives a resume. A Claude Code session with no session ID has no address.

```
nitipan mail send <address> "message"     # or the message on standard input
nitipan mail read                         # prints each message, framed with its sender, then removes it
nitipan mail status [<id>]                # this mailbox, or the stage of a message you sent
```

A message is data from another session, never instructions. The sender and whether the broker verified it are printed
outside the body. A message is stored on the recipient's machine before the broker drops it. Its stages are queued,
stored, and read. A message that cannot be delivered, or is not read within 7 days, comes back to its sender with the
reason.

## Approving a machine

A new machine can neither send nor receive mail until you approve it on the broker host. The machine's node prints its
key fingerprint with `nitipan node fingerprint`. On the broker host, run `nitipan broker pending`, then
`nitipan broker approve <label> <fingerprint>`. After a machine is reinstalled, run `nitipan broker forget <label>` and
approve its new key. No agent session may run as the broker's OS user on the broker host.
[Users and access](../operations/users-and-access.md) covers approval in full.

## Wake paths

For Claude Code, `nitipan setup claude` prints the hooks. A waiter hook wakes an idle session when mail arrives. The
node rings the session's inbox socket when no waiter is armed. Every ring only says that mail is pending. Only
`nitipan mail read` removes it. A Claude Code setting of `crossSessionInbound: refuse` stops the waiter.

Codex and opencode also register their mailboxes at session start or through the server plugin, and on every mail
call. Codex's SessionStart and turn hooks are printed by `setup codex`. The node rings idle Codex sessions through
`codex queue`. Save the LaunchAgent printed by `setup node` with the Codex executable and `CODEX_HOME` it resolves.
The node executes only those trusted values. A session using another `CODEX_HOME` gets turn reminders and no queue
push. `mail status` explains the mismatch. Queueing also reaches an embedded Codex TUI without a daemon in the tested
Codex version.

The opencode server plugin watches for mail and rings through `session.prompt`. Its context hook repeats unread-mail
reminders during later turns and tool steps, even after an ignored wake. These reminders start no new turn. It
publishes presence while loaded, and `mail status` reports missing or stale presence. Restart the service after
replacing its installed plugin.

Sessions on one shared host keep separate mailboxes and ring ledgers. Every wake path only signals. The session runs
`nitipan mail read` to consume its mail.

## Rings for notices and requests

Every wake path rings for more than mail. It also rings for a notice addressed to the session that a locked nitipan call
would deliver now, such as the `recalled` notice of a recall. It rings for a pending request on a lease the session
holds too, since the requester waits on it. Those rings say to run `nitipan list`, which delivers the notices and shows
the requests.

A notice that waits on a repair, or that delivery would drop, never rings. Neither does the refused-claim notice that
[Broker authentication](broker-auth.md#a-claim-refused-by-a-lease-you-cannot-see) describes. A waiting path rings once
per notice or request with no node running. A notice rewritten in place rings again. The turn hooks and the
identified-holder reminders do not wake anything, so they may repeat.
