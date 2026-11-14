# Running nodes

A node runs on each machine, once for each OS user. It mirrors the machine's leases to the broker, forwards the
requests that sessions hand it, and delivers mail. This guide covers the node's config file, its LaunchAgent, its state
files, and how to handle upgrades and reinstalls.

For the first-time walkthrough, see [Going multi-machine](../getting-started/broker-and-nodes.md). The broker side is in
[Running the broker](broker.md).

## The config file

The node reads `~/.config/nitipan/node.json`. The home directory comes from the user database, not from `HOME` or
`XDG_CONFIG_HOME`, because a LaunchAgent gets no shell environment.

### Fields

| Field     | Required | Meaning                                                                                        |
| --------- | -------- | ---------------------------------------------------------------------------------------------- |
| `version` | yes      | The config format version. It must be `1.x`, such as `"1.0"`. A bare `"1"` is refused.         |
| `broker`  | yes      | The broker's base URL, `https://host:port` or `http://host:port`.                              |
| `proxy`   | no       | The URL of an HTTP proxy, such as `http://proxy.example.com:3128`. Empty means a direct connection. |
| `ca_file` | no       | A PEM file of certificate authorities that replaces the system roots.                          |

Fields the node does not know are ignored. A file with no `broker` is refused with `names no broker`.

The node never reads the proxy from the environment. `HTTPS_PROXY`, `HTTP_PROXY`, and `NO_PROXY` have no effect on it.
A LaunchAgent does not inherit them, and a session could set them. Only `proxy` in this file counts.

`ca_file` replaces the system roots. It does not add to them. With `ca_file` set, the node trusts only the authorities
in that file. Use it for a broker whose certificate a private authority signed. A file with no PEM certificate stops the
node.

An example for a broker behind a corporate proxy, with a private authority:

```json
{
  "version": "1.0",
  "broker": "https://broker.example.com:8443",
  "proxy": "http://proxy.example.com:3128",
  "ca_file": "/Users/alice/.config/nitipan/example-ca.pem"
}
```

The smallest file is:

```json
{"version": "1.0", "broker": "https://broker.example.com:8443"}
```

### Ownership and permissions

The directory `~/.config/nitipan` and the file must be owned by you. Neither may be writable by its group or by others.
The node refuses the file otherwise, with an error that ends in `config path is not safe to read`. Fix it with
`chmod 700 ~/.config/nitipan` and `chmod 600 ~/.config/nitipan/node.json`.

When the file is missing, the error names the path and says to run `nitipan setup node`.

### Why the config is not in the state directory

Agent sandboxes can write the state directory, `~/.local/state/nitipan`. Sessions write their leases and requests
there. The node runs outside every sandbox. If what the node connects to came from the state directory, a session could
point it at another broker or proxy. So the broker address, the proxy, and the CA file come from `~/.config/nitipan`,
which no agent sandbox may write. The same holds for the Codex path in the LaunchAgent.
[ADR-07](../../spec/adrs/ADR-07-node-runs-only-input-sandboxes-cannot-write.md) records this decision.

The owner and mode checks guard against other OS users and loose permissions. They do not guard against a sandboxed
session, which runs as the same user. Keep `~/.config/nitipan` out of every agent's sandbox write list.

## HTTPS and plain HTTP

Use HTTPS. The node verifies the broker's certificate against the system roots, or against `ca_file`.

Plain HTTP is allowed only toward a loopback or private address:

- A literal public IP over plain HTTP is refused when the node starts. The error says the address `is a public
  address. Use https, or an address on a loopback or private network`.
- A host name over plain HTTP is allowed at start. The node checks the address the name resolves to just before each
  connection, and refuses a public one with `refusing plain HTTP to the public address ...`. This also covers a name
  whose answer changes later.
- With `proxy` set, the node judges the proxy's address instead, since that is what it dials.
- The node logs a warning on every start over plain HTTP. It says that anyone on the path can read and change leases.

The node follows no redirect from the broker. Go would send the node's key again to the new location, and a redirect
from `https` to `http` would send it in the clear. A redirect fails the call with `refusing to follow it`.

A node on plain HTTP gets leases but no mail. Mail needs a broker address that is HTTPS with a certificate the node
verifies. See [Mail](../reference/mail.md).

Once a broker reports cross-user features on, the node records that in its state directory. From then on it refuses
plain HTTP to the broker, even when a later answer omits the flag. Move that node's `broker` to an `https` URL. Nothing
in nitipan clears the record. See "State files" below.

## The LaunchAgent

`nitipan setup node` prints a LaunchAgent and the commands that load it. It writes nothing. It also prints how to
approve the node's key for mail.

| Item       | Value                                            |
| ---------- | ------------------------------------------------ |
| Label      | `dev.nitipan.node`                               |
| Plist path | `~/Library/LaunchAgents/dev.nitipan.node.plist`  |
| Log        | `~/Library/Logs/nitipan/node.log`                |
| Program    | the `nitipan` on PATH, with the argument `node`  |

The plist sets `RunAtLoad` and `KeepAlive`. launchd starts the node at login and restarts it whenever it exits. Standard
output and standard error both go to the log.

The plist names the `nitipan` that PATH finds, not the path a symlink resolves to. Run `setup node` from a shell where
`nitipan` is on PATH.

### Codex arguments

When `codex` is on PATH as an absolute path, `setup node` adds two arguments to the plist:

- `--codex-path` is the Codex executable that PATH found.
- `--codex-home` is `CODEX_HOME` when it is set, and `~/.codex` otherwise.

The node runs only that Codex, with only that home, to ring idle Codex sessions. It never takes either from a session.
Run `setup node` from the shell you start Codex from, so the two values match. A session under another `CODEX_HOME` gets
turn reminders but no queue push, and `nitipan mail status` says why. Both values must be absolute, or the node stops
with `codex path and home must both be absolute`.

If you install Codex after you load the plist, run `setup node` again and replace the plist.

### Loading and stopping

Save the plist where the output says, then load it:

```sh
mkdir -p ~/Library/Logs/nitipan
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/dev.nitipan.node.plist
```

To stop it:

```sh
launchctl bootout gui/$(id -u)/dev.nitipan.node
```

To restart it, run the `bootout` and then the `bootstrap`. The printed text fills in your numeric uid and your absolute
home directory.

Do not run `nitipan node` from a terminal or an agent's shell. A process started from an agent's shell inherits the
agent's sandbox, and closing a terminal tab stops its heartbeats.

## One node per user

Only one node runs for each machine and OS user. The node takes an exclusive flock on `node.lock` in the state
directory. A second node fails at once:

```
nitipan: /Users/alice/.local/state/nitipan/node.lock: another node already runs for this machine and user
```

Two nodes would keep replacing each other's snapshots. This error usually means a node started by hand while the
LaunchAgent's node runs.

Each OS user on a shared Mac runs a node of their own, with their own `node.json` and their own LaunchAgent.

## State files

The node keeps these files in `~/.local/state/nitipan`. Older binaries never read them.

| File                  | Purpose                                                                                  |
| --------------------- | ---------------------------------------------------------------------------------------- |
| `node.lock`           | The flock that keeps a second node off this machine and user.                            |
| `node-key`            | The node's instance key. The node creates a random one on its first run.                 |
| `cross-user-enabled`  | Present once the broker reported cross-user features on. It is never removed.            |
| `broker/presence.json`| The node's presence record. Commands that need the broker read it to find a live node.   |

`node-key` is the bearer key the broker pins to this machine's label. Its fingerprint is what you approve on the broker
host. Print it with `nitipan node fingerprint`. The key lives in your 0700 state directory, so taking over a node's
stream needs a file only you can read.

`cross-user-enabled` makes the node refuse plain HTTP to the broker. The node fails closed. When it cannot read the file
or its directory, it treats the flag as set.

The node removes `broker/presence.json` when it stops. A command that needs the broker and finds no live node fails at
once with "node not running". See [Troubleshooting](troubleshooting.md).

## Upgrades

### The node restarts itself

The node checks its own executable every 5 seconds. When the file changes, the node logs
`the executable changed. Exiting so launchd restarts it.` and exits with status 0. launchd then starts it again on the
new binary. After `make install`, the node is on the new version within a few seconds. No `launchctl` step is needed.

This works because the plist names the stable `nitipan` path on PATH. A plist that names a versioned path would keep
running an old binary, or fail once an upgrade deletes it. Run `setup node` again and replace such a plist.

### Version skew

The broker, each node, and each `nitipan` binary are separate deploy units. No upgrade order is required. Each side
answers a newer one with an explicit error, never by dropping a request.

- A "node too old" error means a CLI newer than the node sent a request kind or request version the node does not
  know. The error reads `the node does not know the "<kind>" request: node too old`, or names the request versions.
  The node restarts itself on a new binary, so the usual cause is a `nitipan` on PATH that differs from the one in the
  plist. Check that the plist's program is the `nitipan` that PATH finds.
- A "broker too old" error means the node or the CLI asked the broker for a call it does not serve. The error says
  the broker does not serve the call. Upgrade the broker. See [Running the broker](broker.md).

A request more than one day old by the machine's clock is refused by the node with "request expired". Run the command
again.

## Reinstalling a machine

A machine's label comes from its hardware UUID and its uid, so a reinstalled Mac keeps the same label. The broker still
has the old key pinned to that label, and it refuses the new key's registration. The node's log shows the broker's
refusal after `lost the broker:`:

```
the label is pinned to another instance key. If this machine was reinstalled, run nitipan broker forget on the broker host, then approve its new key
```

Retire the old key first.

1. On the broker host, forget the label:

   ```sh
   nitipan broker forget <label>
   ```

   Mail still queued for the label waits for its next approved key. Mail stored on the old machine and never read
   bounces to its senders without its body.

2. On the machine, generate a new key:

   ```sh
   nitipan node rekey
   ```

   It prints the new fingerprint. A fresh state directory already has a new key from the node's first run. Rekey anyway
   when the state directory came back from a backup, since it then holds the forgotten key.

3. Restart the node, so it registers the new key. The node reads `node-key` only when it starts:

   ```sh
   launchctl bootout gui/$(id -u)/dev.nitipan.node
   launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/dev.nitipan.node.plist
   ```

4. Approve the new key in one of two ways:

   - The broker owner runs `nitipan broker approve <label> <fingerprint>` on the broker host. This binds the label to
     the broker owner.
   - A coworker runs `nitipan node pair` on the machine and binds the label on the broker's sign-in page with the
     printed code. See [Users and access](users-and-access.md).

Do not approve the old key again. A forgotten key that registers again only waits as pending. It gets no mail, and it
is refused for leases while cross-user features are on.

## Logs

The node logs to `~/Library/Logs/nitipan/node.log`. Warnings start with `nitipan: node:`. They include plain-HTTP
warnings, `lost the broker: ...` when the connection drops, and the executable-change exit. An error that stops the
node starts with `nitipan:` alone. See
[Troubleshooting](troubleshooting.md).
