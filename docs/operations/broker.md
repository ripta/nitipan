# Running the broker

The broker coordinates leases and mail across machines. One broker serves every node. This guide covers how to run it
under launchd, its config file, its store, and how to back it up, restore it, and upgrade it.

For the first-time walkthrough, see [Going multi-machine](../getting-started/broker-and-nodes.md). For what the broker's
users, bindings, and grants mean, see [Broker authentication](../reference/broker-auth.md). The broker-host commands
are in [Users and access](users-and-access.md).

## How the broker runs

`nitipan broker` runs the broker in the foreground. It reads `~/.config/nitipan/broker.json`, opens its store, and
serves until it gets `SIGINT` or `SIGTERM`. It is meant to run under launchd, not by hand.

The broker runs on one host that every machine can reach. Run it on a host of your own, never a hosted relay.

### Network direction

Nodes dial out to the broker. The broker never opens a connection to a machine. Everything the broker sends a machine
travels over a stream that the machine's node opened. Only the broker's address has to be reachable. No machine needs
an inbound port or a firewall rule. A machine that moves between networks reconnects from wherever it is.
[ADR-01](../../spec/adrs/ADR-01-machines-connect-out-to-broker.md) records this decision.

### The LaunchAgent

`nitipan setup broker` prints a LaunchAgent and the commands that load it. It writes nothing.

| Item       | Value                                              |
| ---------- | -------------------------------------------------- |
| Label      | `dev.nitipan.broker`                               |
| Plist path | `~/Library/LaunchAgents/dev.nitipan.broker.plist`  |
| Log        | `~/Library/Logs/nitipan/broker.log`                |
| Program    | the `nitipan` on PATH, with the argument `broker`  |

The plist sets `RunAtLoad` and `KeepAlive`, so launchd starts the broker at login and restarts it whenever it exits.
Standard output and standard error both go to the log.

The plist names the `nitipan` that PATH finds, not the path a symlink resolves to. A versioned install path would not
survive an upgrade. Run `setup broker` from a shell where `nitipan` is on PATH.

Save the plist where the output says, then load it:

```sh
mkdir -p ~/Library/Logs/nitipan
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/dev.nitipan.broker.plist
```

To stop it:

```sh
launchctl bootout gui/$(id -u)/dev.nitipan.broker
```

The printed text fills in your numeric uid and your absolute home directory in place of `$(id -u)` and `~`.

### Which OS user runs the broker

Run the broker as an OS user, or on a host, where no agent session runs. This is a hard rule, not a hardening tip.

The broker-host commands in [Users and access](users-and-access.md) reach the broker through a unix socket, the admin
socket. The broker accepts a connection on it only from its own OS user. Approving a machine and admitting a user are
acts that hand out authority. They count as proof of the person's choice only because no agent session can run them.
An agent session running as the broker's OS user on the broker host could approve its own machine.
[ADR-08](../../spec/adrs/ADR-08-broker-authority-needs-a-proof-no-session-can-make.md) records this decision.

## The config file

The broker reads `~/.config/nitipan/broker.json`. The home directory comes from the user database, not from `HOME`,
because a LaunchAgent gets no shell environment.

### Ownership and permissions

Both the directory `~/.config/nitipan` and the file must be owned by the OS user the broker runs as. Neither may be
writable by its group or by others. The broker refuses to start otherwise:

```
nitipan: /Users/nitipan-broker/.config/nitipan/broker.json is writable by its group or others (mode 0664): config path is not safe to read
```

Fix it with `chmod 600 ~/.config/nitipan/broker.json` and `chmod 700 ~/.config/nitipan`. The file can hold the OIDC
client secret, so keep it at mode 0600 even though the check only refuses group and other write access.

When the file does not exist, the error names the path and says to run `nitipan setup broker`.

### Fields

| Field       | Required | Meaning                                                                                       |
| ----------- | -------- | --------------------------------------------------------------------------------------------- |
| `version`   | yes      | The config format version. It must be `1.x`, such as `"1.0"`. A bare `"1"` is refused.        |
| `listen`    | yes      | The TCP address to listen on, as `host:port`, such as `127.0.0.1:8443` or `0.0.0.0:8443`.     |
| `cert_file` | no       | The PEM certificate for TLS.                                                                  |
| `key_file`  | no       | The PEM private key for TLS.                                                                  |
| `store`     | no       | The absolute path of the store file. The default is described under "The store" below.        |
| `oidc`      | no       | The identity provider for coworkers' sign-ins. Without it, only the broker owner can use it.  |

Fields the broker does not know are ignored.

`cert_file` and `key_file` go together. With both set, the broker serves HTTPS. With neither set, it serves plain HTTP.
With only one set, the broker refuses to start with `cert_file and key_file go together`. It never falls back to plain
HTTP.

No path in the file is expanded. Write `/Users/nitipan-broker/...`, not `~/...`.

### Listen address rules

Without TLS, the broker listens only on a loopback or private address. It refuses a public address and a wildcard bind
such as `0.0.0.0:8443`, since a wildcard listens on every interface. A host name other than `localhost` counts as
public, because answering would need a DNS lookup that could change. The refusal reads:

```
listen address "0.0.0.0:8443" is public or a wildcard, and no certificate is configured. Set cert_file and key_file, or listen on a loopback or private address
```

With `cert_file` and `key_file` set, any listen address is allowed.

TLS may end at an ingress in front of the broker. The broker then listens on plain HTTP on a loopback or private
address. Each node verifies the ingress's certificate. Cross-user features have a stricter rule. They need the broker's
own listener on loopback, or serving TLS itself. See [Users and access](users-and-access.md).

### The oidc block

| Field           | Required | Meaning                                                                                   |
| --------------- | -------- | ----------------------------------------------------------------------------------------- |
| `issuer`        | yes      | The provider's issuer URL. It must be `https`, except on loopback.                        |
| `client_id`     | yes      | The OAuth client ID registered with the provider. The broker does not check it at start.  |
| `client_secret` | no       | The client secret. Leave it out for a public client.                                      |
| `redirect_url`  | yes      | Where the provider sends the browser back. It must be `https`, except on loopback.        |
| `ca_file`       | no       | A PEM file of certificate authorities for a self-hosted provider. Empty uses the system roots. |

The broker serves its sign-in page at `/signin` and the provider's callback at `/callback`, on its main listener. Set
`redirect_url` to the broker's public URL with the path `/callback`. Register the same URL with the provider.

The broker checks the `issuer` and `redirect_url` schemes and reads `ca_file` when it starts. A plain-HTTP issuer, a
missing or plain-HTTP `redirect_url` off loopback, or an unreadable `ca_file` stops the broker. The broker fetches the
provider's discovery document on the first sign-in, not at start. A provider that is down at start delays only
sign-ins. The sign-in page answers 503 until the provider can be reached. Leases, mail, and existing bindings keep
working.

The client secret lives only in this file on the broker host. No node and no agent sandbox reaches it.

### A complete example

This file loads, passes the listen check, and passes the OIDC checks. It serves TLS on every interface, keeps the store
at its default location, and lets coworkers sign in:

```json
{
  "version": "1.0",
  "listen": "0.0.0.0:8443",
  "cert_file": "/Users/nitipan-broker/.config/nitipan/tls/broker.example.com.pem",
  "key_file": "/Users/nitipan-broker/.config/nitipan/tls/broker.example.com-key.pem",
  "store": "/Users/nitipan-broker/Library/Application Support/nitipan/broker.json",
  "oidc": {
    "issuer": "https://idp.example.com",
    "client_id": "nitipan-broker",
    "client_secret": "replace-with-the-client-secret",
    "redirect_url": "https://broker.example.com:8443/callback"
  }
}
```

The smallest file that runs a broker for one user on a private network is:

```json
{"version": "1.0", "listen": "192.168.1.10:8443"}
```

A node that reaches the broker over plain HTTP gets leases but no mail. Mail needs HTTPS, from the broker itself or from
an ingress in front of it. See [Running nodes](nodes.md).

## The store

The store is one JSON file that holds all broker state. That covers live and lapsed leases, request outcomes, the
fencing token counter, the seen labels, each label's pinned key, queued mail, and the users, bindings, delegations, and
grants.

The default path is `~/Library/Application Support/nitipan/broker.json`. It shares a base name with the config file in
`~/.config/nitipan`, but the two are different files.

Each change is written through a temp file, `fsync`, `rename`, and a directory sync before the broker answers. A failed
write is fatal. The broker exits, and launchd restarts it from the file on disk. At start, the broker removes its own
leftover temp file.

Files beside the store:

| File                    | Purpose                                                                                     |
| ----------------------- | ------------------------------------------------------------------------------------------- |
| `broker.json.lock`      | The flock that keeps a second broker off this store.                                        |
| `admin.sock`            | The admin socket that the broker-host commands use.                                        |
| `broker.json.v1`        | The kept 1.x store, after the upgrade to 2.0, until `nitipan broker confirm-upgrade`.       |
| `.tmp-broker.json`      | A write in progress. The broker removes a stale one at start.                               |

### Where the store may live

The store path must be absolute. The broker creates its directory with mode 0700 when it is missing.

The store must be on a local volume. The broker refuses a network share, since `fsync` there may not reach the disk:

```
/Volumes/share/nitipan is on a network volume: store path is not on a local volume
```

It also refuses a folder that a sync service owns. A sync service can hold a file back or replace it, so a write is not
durable. The broker refuses any path under `~/Library/Mobile Documents` or `~/Library/CloudStorage`. Those cover iCloud
Drive, Dropbox, Google Drive, OneDrive, and Box on current macOS. It also refuses the legacy folders directly under the
home directory: `~/Dropbox`, `~/Google Drive`, `~/OneDrive*`, `~/iCloud Drive`, `~/Box`, and `~/Box Sync`.

The check matches by path only. A sync tool pointed at an arbitrary folder is not detected. Neither is iCloud's
Desktop and Documents sync. Do not put the store in `~/Desktop` or `~/Documents` when that sync is on.

### One broker per store

The broker takes an exclusive flock on `broker.json.lock`. A second broker on the same store fails at once:

```
/Users/nitipan-broker/Library/Application Support/nitipan/broker.json: another broker holds this store
```

This usually means a broker started by hand while the LaunchAgent's broker runs. Stop one of them.

### The admin socket

The admin socket is `admin.sock` in the store's directory. The broker creates it at start with mode 0600.

- The store's directory must be owned by the broker's OS user, with no access for group or others. Otherwise the broker
  stops with an error that says to fix it with `chmod 700`.
- A file at the socket path that is not a socket stops the broker. The broker removes a stale socket itself.
- A unix socket path holds at most 103 bytes on macOS. A store in a deeply nested directory is refused with an error
  that says to use a shorter directory.
- The broker checks the peer's uid on every connection. A connection from any other OS user is closed and logged.

The broker-host commands find the socket by reading the same `broker.json` config, so they must run as the broker's OS
user. When they cannot reach it, the error says to run on the broker host, as that OS user, with the broker running.

No node route and no network path reaches the admin socket.

## Upgrading the store to 2.0

A broker that supports users rewrites a 1.x store as version 2.0 the first time it opens it. The upgrade binds every
pinned label to the broker owner and keeps every lease, label, key, and queued message.

Before it changes the store, the broker writes the previous file beside it as `broker.json.v1`. An older broker can
start from that file after a rollback. A rollback loses every binding, admission, delegation, and mail grant made since
the upgrade, and turns cross-user features off.

Every `nitipan broker forget` and `nitipan broker revoke-user` after the upgrade also removes the forgotten keys from
`broker.json.v1`. A rollback therefore never brings back a key you forgot.

Once you are sure you will not roll back, run on the broker host:

```sh
nitipan broker confirm-upgrade
```

It deletes `broker.json.v1` and prints `confirmed the upgrade. A rollback is now impossible.`

An older broker refuses to start on a store with a higher major version. It names both versions. A broker also refuses a
store with the same major and a higher minor, since it cannot rewrite one. Its error says to run the newer broker, or to
restore the store file from before the upgrade.

## Backup and restore

### What to back up

Back up these files from the broker host:

- the store, `~/Library/Application Support/nitipan/broker.json` or the path in `store`
- the config, `~/.config/nitipan/broker.json`
- the certificate and key that `cert_file` and `key_file` name

Each store write replaces the file whole by `rename`, so a copy taken while the broker runs is a complete store as of
some write. The copy includes leases and grants that will be out of date by the time you restore it.

### Restoring

Stop the broker with `launchctl bootout`, put the store file back at its path, and start it again with
`launchctl bootstrap`. Restoring a 1.x file onto a 2.0 broker runs the upgrade again.

Everything written after the backup is gone. That covers leases, request outcomes, bindings, admissions, delegations,
grants, and queued mail. A `forget` or a `revoke-user` run after the backup is undone too. Run each one again after the
restore. The restored file can also turn cross-user features back on or off, as they were at the backup.

### What a restore does to fencing tokens

Every broker grant carries a fencing token. A push compares the holder's token with the fence refs on the remote. See
[Pushing](../reference/pushing.md). A restored store holds an older counter, which could reissue a token already on a
remote. The broker guards against that in two ways.

The first is the clock floor. Each new token is the larger of the counter plus one and the broker's wall clock in
microseconds. The broker puts no floor on the counter when it loads the store. Each grant floors its own token instead.
A restored broker whose clock has moved forward issues tokens above every token issued before the restore. The
residual risk is a restored broker whose clock is also behind.

The second is the fence report. A push refused by a fence token higher than its own reports that token to the broker.
The broker answers with one of three verdicts:

| Verdict    | When                                                         | What the broker does                       |
| ---------- | ------------------------------------------------------------ | ------------------------------------------ |
| `known`    | The token is at or below the counter.                        | Nothing. It issued that token or a later one. |
| `raised`   | The token is above the counter and at most now plus 24 hours. | Sets the counter to the token in one write. |
| `unusable` | The token is more than 24 hours past the broker's clock.     | Nothing. No honest counter issued it.      |

An `unusable` verdict is only the reply. The broker stores nothing for it. The pusher treats that fence ref as unusable,
and its refusal says the ref looks like the result of a broker restore or of damage. The bound keeps one garbage value
from pushing the counter to its top.

### Renewals after a restore

A restore also loses the lease table since the backup. A holder whose lease the restored broker does not know gets
`not_found` on renewal. It is lost. The broker never revives a lease it has no record of, because two sessions would
then each believe they hold it. A holder pushing to a remote that cannot take an atomic push sees
`the broker has no record of lease <id>`.

The broker revives only a lapsed lease it still holds and nobody else took. A revived lease keeps its token.

On load, every live lease in the restored file is given one full term from the broker's start. That is the restart
grace. Holders that are still running renew within it.

### Deleting a fence ref after a restore

`nitipan fence delete <remote> <key>` removes one fence ref from a remote. Use it when a push refusal names a fence ref
that cannot be used, such as one the `unusable` verdict marks. Run it from your own terminal, inside a clone of the
repo:

```sh
nitipan fence delete origin repo:<root-sha>#<path>
```

The key is a fence ref name, `<root-sha>/<encoded-path>`, or a repo name as `acquire` prints it,
`repo:<root-sha>#<path>`. The ref is deleted only if it still holds the value the command read.

The command refuses any caller with agent variables, or with an agent process above it. No error an agent reads names
it. Deleting a live fence ref lets a stale push land, so check first that no live holder depends on it. Fence refs are
never removed automatically.

## Upgrading the broker binary

The broker does not watch its own executable. Unlike the node, it keeps running the old binary after `make install`.
Restart it to run the new one:

```sh
make install
launchctl bootout gui/$(id -u)/dev.nitipan.broker
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/dev.nitipan.broker.plist
```

Live leases survive the restart. On load, each one is given a full term from the new start, and nodes reconnect on their
own. Check `~/Library/Logs/nitipan/broker.log` for the `node registered` lines.

A new broker that changes the store's major version writes the kept file first. See "Upgrading the store to 2.0" above.

The broker, each node, and each `nitipan` on PATH are separate deploy units, and no upgrade order is required. A node
newer than the broker reports "broker too old" for calls the broker does not serve. Upgrade the broker to clear it. See
[Running nodes](nodes.md) for the node side.

## Logs

The broker logs to `~/Library/Logs/nitipan/broker.log` in Go's `slog` text format. It logs each node registration with
the node's label, uid, version, and whether it used HTTPS. It also logs refused admin socket connections and store
errors.
