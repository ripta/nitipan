# Users and access

This guide covers the commands you run on the broker host to approve machines, admit coworkers, turn cross-user features
on and off, offboard a user, and end a lease. For what users, bindings, reach, delegations, and mail grants mean, see
[Broker authentication](../reference/broker-auth.md). For a coworker's own steps, see
[Adding a coworker](../getting-started/coworkers.md).

## Where these commands run

Every command here is a subcommand of `nitipan broker`. Run them on the broker host, as the OS user the broker runs as,
while the broker runs. They talk to the broker over its admin socket, `admin.sock` in the store's directory. The broker
accepts a connection there only from its own OS user. No node and no network path reaches them.

The commands find the socket by reading `~/.config/nitipan/broker.json`, so that file must load for them too. When the
socket cannot be reached, the error says to run the command on the broker host, as the broker's OS user, with the broker
running.

No agent session may run as the broker's OS user on the broker host. These commands hand out authority. They prove the
person's choice only because no agent session can run them. See
[ADR-08](../../spec/adrs/ADR-08-broker-authority-needs-a-proof-no-session-can-make.md). `approve` prints this warning
with every approval.

When the broker refuses a request, the command prints `nitipan: the broker refused: <reason>` and exits with status 1.

## The broker owner

The broker always has an owner. The broker creates the owner's user record the first time it opens its store. An
upgrade from store 1.x keeps the owner its migration assigned. A machine you approve on the broker host is bound to the
owner.

Other users are coworkers. A coworker is admitted on the broker host, then binds their own machines through the
broker's sign-in page. The page needs an `oidc` block in the broker config. See [Running the broker](broker.md).

## Command reference

### pending

```sh
nitipan broker pending
```

Lists every label that is not approved, with the fingerprints of the keys that wait under it. Each label line shows
the label, the host, the OS user, and one of these states:

| State                                       | Meaning                                                              |
| ------------------------------------------- | -------------------------------------------------------------------- |
| `pending approval`                          | A key arrived and nobody approved it.                                |
| `approved in advance, waiting for key <fp>` | You approved a fingerprint before its key reached the broker.        |
| `approved, with another key waiting`        | The label is approved, and a different key also arrived.             |
| `forgotten, waiting for a new key`          | You forgot the label. A key listed under it arrived since, and waits. |

Each key line shows its fingerprint and when the broker first saw it. With nothing waiting, it prints
`every label is approved`.

Use it when a new machine's node has run once and you want to approve it. Compare the fingerprint with the one the
machine prints with `nitipan node fingerprint` before you approve.

### approve

```sh
nitipan broker approve <label> <fingerprint>
```

Pins the key with that fingerprint to the label, binds the label to the broker owner, and discards the label's other
pending keys. A fingerprint is four groups of four hex digits, such as `0123-4567-89ab-cdef`.

When the broker knows the label but no key with that fingerprint has reached it yet, the approval is recorded and
waits. The label pins the first key that presents that fingerprint, and binds to the owner then.

The broker refuses when:

- it has never seen the label
- the label is already approved under another key. Forget it first if its machine was reinstalled
- a coworker bound the label through the sign-in page. Forget it first if you mean to take it over

Use it for your own machines. A coworker binds their machine on the sign-in page instead.

### forget

```sh
nitipan broker forget <label>
```

Unpins the label and drops its node's key. The label's binding loses its user. The broker keeps the last bound user, so
a later binding to a different user starts clean.

Mail still queued for the label waits for its next approved key and is never handed to an unapproved one. Mail already
stored on the machine and never read bounces to its senders without its body. The command prints the `approve` command
to run once the new key arrives.

Use it after a machine is wiped or reinstalled, after its key may have leaked, or to clear a label that blocks
cross-user features. Then follow "Reinstalling a machine" in [Running nodes](nodes.md).

### confirm-upgrade

```sh
nitipan broker confirm-upgrade
```

Deletes `broker.json.v1`, the 1.x store the broker kept beside the store when it upgraded to 2.0. After this, a
rollback to a broker that reads only store 1.x is impossible. Use it once the 2.0 broker has run long enough that you
will not roll back. See [Running the broker](broker.md).

### admissions

```sh
nitipan broker admissions
```

Lists the people who signed in on the broker's sign-in page and are not admitted. Each line shows the email, the issuer,
the `sub`, and when they first signed in. With nobody waiting, it prints `no one is waiting for admission`.

Use it to read the exact issuer and `sub` to pass to `admit`.

### admit

```sh
nitipan broker admit <issuer> <sub> <email>
```

Admits a person as a new user, by the issuer plus `sub` their identity provider names them with. It prints the new user
ID and the user's label:

```
admitted dev@example.com as user <user-id>, with user label <user-label>
```

The user label is four words derived from the user ID. It is how other users name this user. The broker refuses an
issuer plus `sub` that already resolves to a user.

### link

```sh
nitipan broker link <issuer> <sub> <user-id>
```

Maps another issuer plus `sub` to an existing user. Use it when a user signs in through a second identity provider, or
their provider changed. The broker refuses an issuer plus `sub` that already resolves to any user.

### bindings

```sh
nitipan broker bindings
```

Lists each user with a bound label: their user label, email, and user ID. Under each user, it lists each label with its
host, its OS user, when its node was last seen, and when it was bound. Only labels bound now are listed. With none, it
prints `no label is bound to a user`.

Use it to find a user's ID before `revoke-user`, or to audit which machines act for whom.

### revoke-user

```sh
nitipan broker revoke-user <user-id>
```

Offboards a user in one store write. It:

- forgets every label bound to the user
- removes every issuer plus `sub` that resolves to them
- drops every delegation and mail grant that names them, in either direction
- ends every lease held for the user, and every lease their labels hold

Each ended lease's holder gets a notice from its node. An ended lease is never revived. The command prints the labels
it forgot, the leases it ended, and how many admissions, delegations, and mail grants it removed.

The command refuses the broker owner's own user ID. Use `broker forget <label>` to unpin one of the owner's machines.

### cross-user

```sh
nitipan broker cross-user true
nitipan broker cross-user false
```

Turns cross-user features on or off. See the procedures below.

### break

```sh
nitipan broker break <lease-id>
```

Ends any broker lease as the broker owner. That includes another user's live `shared` lease, which no other user may
break. The holder's node writes the holder a notice that names `broker owner <user label>`. A lease that already ended
is removed from the broker's table.

Use it when a coworker's lease blocks work and its holder cannot release it.

## Procedures

### Approve a new machine

1. On the new machine, set up `node.json` and load the node's LaunchAgent. See [Running nodes](nodes.md).
2. On the new machine, print the key's fingerprint:

   ```sh
   nitipan node fingerprint
   ```

3. On the broker host, list what waits:

   ```sh
   nitipan broker pending
   ```

4. Find the machine's label and check that its key's fingerprint matches step 2. Then approve it:

   ```sh
   nitipan broker approve <label> <fingerprint>
   ```

The broker must have seen the label, so the node must run once first. For a label the broker already knows, such as a
forgotten one, you may approve a fingerprint before its key arrives. The approval then waits for the key.

Until you approve it, the machine can hold leases while cross-user features are off. It can neither send nor receive
mail. Once cross-user features are on, an unbound label is refused for leases and mail alike.

### Admit and bind a coworker

1. Configure the `oidc` block in the broker config and restart the broker. See [Running the broker](broker.md).
2. The coworker opens `https://broker.example.com:8443/signin`, chooses "Bind a Label", and signs in. The broker records
   a pending admission and shows "Your sign-in was recorded. Ask the broker owner to admit you."
3. On the broker host, find the coworker and admit them:

   ```sh
   nitipan broker admissions
   nitipan broker admit <issuer> <sub> <email>
   ```

   Tell the coworker their user label from the output.

4. The coworker sets up a node on their machine with `node.json` pointing at the broker, and loads its LaunchAgent. The
   node must run once, so the broker has its key.
5. On their machine, the coworker runs:

   ```sh
   nitipan node pair
   ```

   It prints the label, the key's fingerprint, and a four-word pairing code. The code expires in 10 minutes.

6. The coworker opens the sign-in page again, chooses "Bind a Label", signs in, and enters the label and the code. The
   binding pins the key that sent the code and discards the label's other pending keys. Five wrong codes from one
   signed-in user for one label lock that user out of that label until the code expires.
7. Check the result on the broker host with `nitipan broker bindings`.

A coworker can bind while cross-user features are off, so setup can come first. Their machines stay refused until you
turn the features on.

### Turn cross-user features on

Run on the broker host:

```sh
nitipan broker cross-user true
```

The change takes effect at once. The command then lists every label that can reach the broker and is bound to nobody.
Each is refused from now on, until it is approved on the broker host or bound on the sign-in page. Run
`nitipan broker pending` and approve the labels you need before you turn the features on. Approve or bind any label
still on the list afterward.

The command refuses while any of these hold. Clear each one and run it again.

| Refusal                                                    | How to clear it                                                    |
| ---------------------------------------------------------- | ------------------------------------------------------------------ |
| `no identity provider configured`                          | Add an `oidc` block to the broker config and restart the broker.    |
| `listen address "..." requires TLS or a loopback address`  | Set both `cert_file` and `key_file`, or listen on `127.0.0.1` or `::1`, and restart. |
| `label "..." reported plain HTTP under its current key`     | Forget the label, rekey its node, and approve the new key.          |
| `label "..." never reported scheme`                        | Forget the label, upgrade the node's nitipan, rekey it, and approve the new key. |
| `the store's owner ID "..." names no user record`          | Restart the broker. It restores the owner's record when it opens the store. |

The listener rule is about the broker's own listener. A private address is not enough without TLS. An ingress on another
host may end TLS and re-encrypt to the broker. An ingress that speaks plain HTTP to the broker over a private hop does
not count.

A key that once reported plain HTTP keeps that mark for good. A later HTTPS report under the same key does not clear
it, and neither does approving the same key again. Only a new key clears it:

1. On the broker host, run `nitipan broker forget <label>`.
2. On the machine, move `node.json` to an `https` broker URL. For a "never reported scheme" label, upgrade nitipan
   first. Every key from store 1.x is one of these.
3. On the machine, run `nitipan node rekey`, then restart the node so it registers the new key. See
   "Reinstalling a machine" in [Running nodes](nodes.md).
4. Approve the new key with `nitipan broker approve <label> <fingerprint>`, or bind it on the sign-in page.

A forgotten label has no current key, so it never blocks the command.

Once the features are on, each node records it. From then on the node never sends its key over plain HTTP again.

### Turn cross-user features off

```sh
nitipan broker cross-user false
```

The broker then serves only the owner's labels and unbound labels. A label bound to any other user is refused for
leases, discovery, break, and mail. The error names `nitipan broker cross-user true`. Coworkers' bindings, leases,
delegations, and grants stay as they are, and come back when you turn the features on again.

Each node keeps its record that the features were once on. It still refuses plain HTTP.

### Offboard a user

1. On the broker host, find the user's ID:

   ```sh
   nitipan broker bindings
   ```

2. Revoke them:

   ```sh
   nitipan broker revoke-user <user-id>
   ```

3. Read the output. It lists the labels it forgot and the leases it ended.

A forgotten key that registers again only waits as pending. It is refused for leases while cross-user features are on,
and it gets no mail. Do not approve it. Remove the user from your identity provider too, so they cannot sign in and
record a new admission.

### End a lease as the broker owner

1. Find the lease's ID. From any machine with a node, `nitipan who holds <subject>` names each holder and its lease.
   See [CLI reference](../reference/cli.md).
2. On the broker host, run:

   ```sh
   nitipan broker break <lease-id>
   ```

The holder's node writes the holder a notice that names the broker owner. A user may release or `break --force` their
own leases from any of their labels. This command is for a lease that belongs to someone else.
