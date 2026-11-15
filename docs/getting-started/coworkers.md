# Adding a coworker

This walkthrough lets a second person use your broker. You are `alice`, the broker owner. Your coworker is `bob`. Bob
signs in through an OIDC identity provider at `idp.example.com`, binds his machine on the broker's sign-in page, and
then shares leases and mail with you.

It assumes your broker and nodes already work. See [Going multi-machine](broker-and-nodes.md). For the rules behind
each step, see [Broker authentication](../reference/broker-auth.md) and
[Users and access](../operations/users-and-access.md).

## 1. Check the broker's transport

Cross-user features need the broker's own listener on a loopback address, or serving TLS itself. A broker with
`cert_file` and `key_file` set passes. A plain HTTP listener on a private address does not, even behind a TLS ingress.

Every label's key must also have reported HTTPS. A key that ever reported plain HTTP, and a key from an older node that
never reported its scheme, blocks the switch in step 7. The error names the label and the fix.

## 2. Add an identity provider to the broker

1. Register a client for the broker at your identity provider. Its redirect URL is the broker's `/callback` path, such
   as `https://broker.example.com:8443/callback`.

2. As `nitipan-broker` on the broker host, add an `oidc` block to `~/.config/nitipan/broker.json`:

   ```json
   {
     "version": "1.0",
     "listen": "0.0.0.0:8443",
     "cert_file": "/Users/nitipan-broker/.config/nitipan/tls/cert.pem",
     "key_file": "/Users/nitipan-broker/.config/nitipan/tls/key.pem",
     "oidc": {
       "issuer": "https://idp.example.com",
       "client_id": "nitipan-broker",
       "client_secret": "<client secret>",
       "redirect_url": "https://broker.example.com:8443/callback"
     }
   }
   ```

   `issuer` and `redirect_url` must be `https`. Loopback addresses are exempt, for tests. Add `ca_file` inside the
   block when the identity provider uses a private CA. The client secret lives only in this file on the broker host.
   See [Running the broker](../operations/broker.md) for every field.

3. Restart the broker so it reads the new config:

   ```
   launchctl bootout gui/$(id -u)/dev.nitipan.broker
   launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/dev.nitipan.broker.plist
   ```

   The broker now serves its sign-in page at `https://broker.example.com:8443/signin`. It fetches the provider's
   discovery document on the first sign-in, not at start.

## 3. Link your own sign-in to the broker owner

You are already the broker owner, through the machines you approved. To use the sign-in page yourself, link your
identity to that user.

1. Open `https://broker.example.com:8443/signin`, choose any action, and sign in. The page says
   `Your sign-in was recorded. Ask the broker owner to admit you.`

2. On the broker host, find your issuer and `sub`:

   ```
   nitipan broker admissions
   ```

   Each person prints as `<email>  issuer <issuer>  sub <sub>  first signed in <time>`.

3. Find your user ID. The first line of each user in this list ends with `user <user-id>`:

   ```
   nitipan broker bindings
   ```

4. Link your sign-in to it:

   ```
   nitipan broker link https://idp.example.com <your sub> <your user-id>
   ```

   You should see `linked issuer https://idp.example.com sub <your sub> to user <your user-id>`.

## 4. Admit your coworker

1. Bob opens `https://broker.example.com:8443/signin`, chooses "Bind a Label", and signs in. The page says his sign-in
   was recorded and asks him to have you admit him.

2. On the broker host, list who waits:

   ```
   nitipan broker admissions
   ```

   ```
   bob@example.com  issuer https://idp.example.com  sub <bob's sub>  first signed in <time>

   Admit a person with: nitipan broker admit <issuer> <sub> <email>
   ```

   Your own entry from step 3 may still show here, since `link` does not remove it. Leave it. `admit` refuses an
   identity that already resolves to a user.

3. Admit him:

   ```
   nitipan broker admit https://idp.example.com <bob's sub> bob@example.com
   ```

   You should see `admitted bob@example.com as user <user-id>, with user label <user label>`. A user label is a
   four-word name. It is how leases, delegations, and grants name a user.

## 5. Bind your coworker's machine

1. Bob installs nitipan and starts a node on his machine, as in step 4 of
   [Going multi-machine](broker-and-nodes.md). His `node.json` names your broker.

2. Once his node has run once, Bob prints a pairing code:

   ```
   nitipan node pair
   ```

   ```
   Label: <bob's label>
   Fingerprint: <fingerprint>
   Pairing code: <four words>

   This code expires in 10 minutes. Bind the node on the broker's sign-in page with its label and this code.
   ```

3. Bob opens the sign-in page, chooses "Bind a Label", and signs in. He enters the label and the pairing code, then
   chooses "Confirm Bind". The binding pins the key that sent the code.

   Five wrong codes from one user for one label lock that user out of the label until the code expires. Run
   `nitipan node pair` again for a fresh code.

4. On the broker host, check the binding:

   ```
   nitipan broker bindings
   ```

   Bob's user prints with his label under it, with its host, OS user, last-seen time, and when it was bound.

## 6. Know what changes before you turn it on

While cross-user features are off, the broker serves only your labels and unbound labels. Bob's bound machine is
refused for leases, discovery, break, and mail, and the error names `nitipan broker cross-user true`. Binding still
works while the features are off, so you can do steps 4 and 5 first.

Once they are on, every unbound label is refused for leases and mail. Approve or bind each of your machines first.

## 7. Turn on cross-user features

On the broker host:

```
nitipan broker cross-user true
```

It first lists any label bound to nobody. Each one is refused from then on, until it is approved on the broker host or
bound on the sign-in page. Then it prints `cross-user features are on.`

Bob's machine is served from now on. On his machine, Bob checks his user label:

```
nitipan who am-i
```

You should see `User label: <bob's user label>`.

The command refuses when:

- The broker config has no `oidc` block.
- The broker's listener is neither on a loopback address nor serving TLS itself.
- A label's current or pinned key reported plain HTTP. Forget the label with `nitipan broker forget <label>`, run
  `nitipan node rekey` on that machine, and approve the new key.
- A label's current or pinned key never reported its scheme. Forget the label, upgrade that node's nitipan, run
  `nitipan node rekey`, and approve the new key.

`nitipan broker cross-user false` turns the features off again. Bob's bindings, leases, delegations, and grants stay as
they are for when you turn them back on.

## 8. Share a lease

Each user has a private `user` reach. Two users' `user` leases never conflict, and Bob does not see yours. Every user
shares one `shared` reach. A `shared` lease shows to every bound user, and it conflicts with any `user` lease that
overlaps it.

1. In one of your sessions, claim a directory at `shared` reach:

   ```
   nitipan acquire --reach shared --checkout . --scope docs
   ```

   You should see `acquired shared lease <lease-id>`, then the repo name, anchor, token, and expiry.

2. On Bob's machine, his session finds it:

   ```
   nitipan who holds repo:<root-sha>#docs
   ```

3. A claim by Bob on an overlapping name is refused, and the refusal names your lease.

A coworker's `user` lease that you cannot see can also refuse your `shared` claim. That refusal names no holder or
path. It says the name is held at `user` reach by another user.

Bob cannot `break --force` your lease. You can end any lease with `nitipan broker break <lease-id>` on the broker host.

## 9. Delegate to a shared agent

A shared agent serves several users. It claims in a user's reach only under that user's standing delegation to the
agent's label. Say the agent runs on a machine with the label `<agent label>`.

1. Bob opens the sign-in page, chooses "Grant Delegation", and signs in. He types the agent's label and chooses
   "Confirm Grant". No request through a node can grant a delegation.

2. Bob tells the agent his user label, which `nitipan who am-i` printed for him.

3. The agent claims for Bob:

   ```
   nitipan acquire --reach user --acting-for <bob's user label> --checkout . --scope src
   ```

   The lease counts in Bob's `user` reach only. `--acting-for` applies only with `--reach user` or `--reach shared`. A
   claim for a user without that user's delegation is refused, and the error names the sign-in page.

4. Bob can release that lease from any of his own labels with `nitipan release <lease-id>`. The agent's node writes the
   agent a notice.

To take the delegation back, Bob chooses "Revoke Delegation" on the sign-in page. That ends every lease the agent's
label holds for him.

## 10. Let a coworker mail you

Mail to another user's session needs that user's grant. To receive mail from Bob, you grant it.

1. Open the sign-in page, choose "Grant Mail From a User", and sign in. Type Bob's user label and choose
   "Confirm Grant".

2. Hold a `shared` lease in the session Bob should reach. Bob's first message reaches only a session of yours that
   holds a live or lapsed `shared` lease.

3. Bob finds the address with `nitipan who holds` and sends:

   ```
   nitipan mail send <mail name>@<label> "can I take docs/api after you?"
   ```

4. Your session reads it:

   ```
   nitipan mail read
   ```

   The frame marks it as another user's mail, outside the body:

   ```
   === message <message-id> ===
   from: <bob's mail name>@<bob's label> (another user's machine, NOT one of yours)
   from user: <bob's user label> (stamped by the broker)
   reply with: nitipan mail send --reply-to <message-id> <bob's mail name>@<bob's label>
   ```

5. Reply with the command the frame prints:

   ```
   nitipan mail send --reply-to <message-id> <bob's mail name>@<bob's label> "yes, after 3pm"
   ```

   A reply needs no lease on either side. It is accepted for 7 days after the message it answers was read. Every reply
   rides the grant that admitted the first message.

To stop Bob's mail, choose "Revoke Mail From a User" on the sign-in page. A message still in flight then bounces back
to Bob with `sender no longer allowed`. See [Mail](../reference/mail.md).

## Removing a coworker

`nitipan broker revoke-user <user-id>` on the broker host forgets every label of the user, removes their admissions,
and ends their leases. It prints what it removed.
