# Troubleshooting

This guide is organized by symptom. Each section quotes the error, says what causes it, and says what to do. Every
error a command prints starts with `nitipan:` on standard error. Warnings start with `nitipan: warning:`.

## Exit statuses

Scripts and agents test these values. A value never changes once released.

| Status | Name             | Meaning                                                                                        |
| ------ | ---------------- | ---------------------------------------------------------------------------------------------- |
| 0      | ok               | The command did what was asked.                                                                |
| 1      | error            | Any failure with no status of its own.                                                         |
| 2      | usage            | The command line was wrong.                                                                    |
| 3      | refused          | An overlapping lease blocks the claim. Also a full resource slot, a `--slots` value that differs from the count that applies, and `--replace` on a resource lease. |
| 4      | lock busy        | The state lock stayed busy past the retry budget.                                              |
| 5      | already covered  | The caller's own leases already cover the claim, and nothing changed.                          |
| 6      | not yours        | The lease belongs to another holder.                                                           |
| 7      | broken           | The lease was broken, and the tombstone says by whom.                                          |
| 8      | no such lease    | No lease has that ID.                                                                          |
| 9      | unusable record  | A record this nitipan cannot use blocks the request.                                           |
| 10     | wait timeout     | `acquire --wait` ran out of time before the lease could be granted.                            |
| 11     | delegated        | The caller already holds exactly these keys, and part of them is delegated to a sub-lessee.    |
| 12     | could not verify | A push needed the broker's answer and could not get it. It is neither allowed nor a lost lease. |
| 13     | contention       | A push kept losing the compare-and-swap on the fence refs to other pushes and gave up.          |

The git hooks' refusals of paths, pushes, and fence refs also exit 3, and git aborts the update or the push.

Several errors in this guide have no status of their own and exit 1. They include "node not running", "node too old",
"broker too old", the Codex and opencode acquire gates, the git-hooks-not-in-force refusal, and sandbox write failures.

## Where the logs are

| Source            | Where                                                                                 |
| ----------------- | ------------------------------------------------------------------------------------- |
| The broker        | `~/Library/Logs/nitipan/broker.log` on the broker host, in `slog` text format.        |
| The node          | `~/Library/Logs/nitipan/node.log` on each machine. Warnings start with `nitipan: node:`. |
| A command         | Its own standard error.                                                               |
| A git hook        | The standard error of the git command that ran it.                                    |

The LaunchAgents write these log paths. Their directory must exist before you load a LaunchAgent, so run
`mkdir -p ~/Library/Logs/nitipan` first. See [Running the broker](broker.md) and [Running nodes](nodes.md).

## The state lock stays busy

```
nitipan: the state lock stayed busy for 5s; held by process 4242 (nitipan acquire --wait 5m ...), which is running
```

Every command that writes takes an exclusive lock on the `lock` file in the state directory. It retries every 25
milliseconds for up to 5 seconds, then exits with status 4.

The error names the holder when it can. The holder writes its PID, process start time, and command line into the lock
file. The error trusts that note only when the process is still alive with the same start time. It then says whether
the holder is running or stopped. When the note cannot be trusted, the error says `the process holding it is unknown`.

What to do:

- When the holder is running, wait for it, then run the command again.
- When the holder is stopped, as by `SIGSTOP` or a suspended terminal job, resume it with `kill -CONT <pid>` or end it.
- When the holder is unknown, run the command again. The lock is a flock, so it goes away when the holder exits.

`acquire --wait` does not exit 4 on a busy lock. It counts a busy lock as a failed attempt and keeps waiting. When its
time runs out, it exits 10, and the message names the lock holder if the last attempt found the lock busy.

## "node not running"

```
nitipan: node not running (no presence record).
The node mirrors this machine's leases to the broker, ...
```

A command that needs the broker found no live node. It fails at once, before it writes any request, and prints the
node's install text. The reason in parentheses is one of:

- `no presence record`. The node is not running, or never ran. Its presence record is
  `~/.local/state/nitipan/broker/presence.json`.
- `the process in the presence record has exited`. The node died without removing its record.
- A read or decode error for the presence record.

What to do:

1. Check that the LaunchAgent is loaded: `launchctl print gui/$(id -u)/dev.nitipan.node`.
2. Read `~/Library/Logs/nitipan/node.log` for why it exited. A bad `node.json`, a second node, and a refused key are the
   usual causes.
3. Load it with the `launchctl bootstrap` command that `nitipan setup node` prints.

Commands on `machine` leases never need the node. Only commands that reach the broker do.

## The node will not start or stays disconnected

Read `~/Library/Logs/nitipan/node.log`. Common lines:

| Log text                                                               | Cause and fix                                                     |
| ---------------------------------------------------------------------- | ----------------------------------------------------------------- |
| `... node.lock: another node already runs for this machine and user`   | A second node. Stop the one you started by hand.                  |
| `... is writable by its group or others ...: config path is not safe to read` | Fix the mode of `~/.config/nitipan` or `node.json`.        |
| `version "1" is not 1.x`                                               | Set `"version": "1.0"` in `node.json`.                            |
| `refusing plain HTTP to the public address ...`                        | Use an `https` broker URL. See [Running nodes](nodes.md).         |
| `refusing plain HTTP to the broker: cross-user features were enabled ...` | The node recorded that cross-user features were on. Use `https`. |
| `the label is pinned to another instance key ...`                      | The machine was reinstalled. Follow "Reinstalling a machine" in [Running nodes](nodes.md). |
| `... refusing to follow it: the broker redirected the call`            | Point `broker` at the URL the broker serves, without a redirect.  |
| `lost the broker: ...`                                                 | The connection dropped. The node reconnects on its own.           |

## A sandbox blocks the state directory

```
nitipan: opening the lock file: open /Users/alice/.local/state/nitipan/lock: operation not permitted
The agent's sandbox likely blocked access to the state directory.

Detected agent: claude.
...
```

The agent's sandbox refused a write to the state directory, `~/.local/state/nitipan`. The error appends the setup text
for the agent nitipan detected, which lists the sandbox entries it needs. With no agent detected, it says to run
`nitipan setup` with the agent's name instead.

A blocked write gives the same errno as a plain file-mode problem, so the message says "likely". When no sandbox is in
play, check that you own the state directory.

Add the printed write allowance to the agent's sandbox config and restart the agent. Never add `~/.config/nitipan` to
the allowance. See [Setting up your agents](../getting-started/agents.md).

## Codex or opencode refuses acquire

`acquire` checks that the agent's hooks or plugins are in force before it claims anything. A failed check appends the
output of `nitipan setup codex` or `nitipan setup opencode`.

For Codex, a failed check names one of these:

| Error starts with                                              | Fix                                                                 |
| -------------------------------------------------------------- | ------------------------------------------------------------------- |
| `Codex session ... has no registration under ...`              | Install and trust the SessionStart hook. It may also have timed out. |
| `the unload delay of ... is unknown`                           | Restart the Codex host after you change its config.                 |
| `thread_unload_delay_secs is N for ... and must be 60 or less` | Set `thread_unload_delay_secs = 60` and restart the Codex host.     |
| `the recorded Codex config for ... has no SessionEnd entry`    | Add the SessionEnd hook that runs nitipan, and restart the host.    |
| `Codex has no writer-lock file for session ...`                | This Codex version is not supported.                                |

Codex reads these settings only when its host starts, so each change applies after that host restarts.

For opencode, the claim needs a presence mark from a live TUI with nitipan's plugin loaded:

```
opencode session <id> has no presence mark from a live TUI. It needs a TUI with nitipan's plugin loaded. A session from opencode run, the desktop app, or the API cannot acquire.
```

Install the plugins with `nitipan setup opencode --install-plugins <dir>`, and restart the service after you replace an
installed plugin.

`session file ... cannot be used (...); upgrade nitipan or ask the user` means nitipan could not decode a session file.
A newer nitipan may have written it. Put the same nitipan on PATH everywhere, or ask the user.

## A claim in a git checkout needs the hooks

```
nitipan: the claim is inside a git checkout where nitipan's git hooks are not in force, so a lease there would not fence a ref update:
  /path/to/checkout (there is no pre-push hook in /path/to/checkout/.git/hooks). Run: nitipan setup git --install /path/to/checkout
Then claim again
```

A path claim inside a git checkout needs both the `reference-transaction` and `pre-push` hooks in force. The reason can
be a missing hook, a hook that is not executable, or a hook that is not nitipan's. A checkout set up by an earlier
version may have only the `reference-transaction` hook. A tool that rewired `core.hooksPath` is caught here too.

Run the printed `nitipan setup git --install <checkout>` and claim again. See [Git hooks](../reference/git-hooks.md).

## Unusable records

```
nitipan: leases/<id>.json cannot be used by this nitipan (higher major version); upgrade nitipan or ask the user
```

A record this nitipan cannot read blocks the request, and the command exits 9. The reasons include
`higher major version`, `envelope does not decode`, `footprint disagrees with the body`, and a file name that is not an
ID. `list` shows such a record
as `unusable record (...); upgrade nitipan or ask the user`.

What to do:

1. When a newer nitipan wrote it, upgrade this nitipan. Every binary on the machine should be the same version.
2. When the record is damaged, the user can remove it with `nitipan break <id> --force`. It removes the file and writes
   no tombstone, because nitipan cannot tell whose it was. An unusable tombstone or fence mark also needs `--force`.
3. A file whose name is not an ID cannot be addressed by any command. After checking what it is, delete it by hand.

An agent that hits status 9 should stop and report to the user.

## slots.json problems

```
nitipan: warning: the slot config /Users/alice/.local/state/nitipan/slots.json cannot be used, so every resource name is exclusive. Ask the user to fix it. Problems:
  slots entry "gpu:main": the count 0 must be an integer of at least 1
```

`slots.json` in the state directory sets how many holders a resource name admits. Any problem makes the whole file
unusable. Every call then warns with the path and every problem, and treats every resource name as exclusive. Path
claims are unaffected.

The problems it reports include:

- the file is not a JSON object
- an unknown top-level field
- a `version` that is not the integer 1
- a key that is not a valid resource name
- a count that is not an integer of at least 1

Fix every listed problem. The next call reads the file again. The format is in
[Leases](../reference/leases.md).

## Mail does not arrive

Start with the sender's and the recipient's `mail status`. Both are diagnostics and change nothing.

### `nitipan mail status`

Run it in the recipient's session. It prints:

- the session ID, the agent, and the registered PID
- `mail waiting: N`
- whether a waiter is armed, and by which PID
- for Codex, whether the node can push to this session
- for opencode, whether the server plugin is watching
- each registration nitipan cannot use, which is never rung

Lines that point at a problem:

| Line                                                                                   | Fix                                             |
| -------------------------------------------------------------------------------------- | ----------------------------------------------- |
| `waiter: the presence file cannot be replaced (...), so no waiter arms for this session` | Fix the file named in the reason.            |
| `Codex push unavailable: no usable node presence`                                       | Start the node.                                 |
| `Codex push unavailable: the node LaunchAgent has no Codex path and home; rerun nitipan setup node` | Replace the plist with a new `setup node`. |
| `Codex push unavailable: this session's CODEX_HOME differs from the node LaunchAgent's; mail stays unrung` | Start Codex with the plist's `CODEX_HOME`, or rerun `setup node` from that shell. |
| `opencode mail plugin: missing or stale presence; only turn and command reminders are available` | Install the server plugin and restart the opencode service. |
| `socket ring: unavailable. This session reported no ...`                                | The session started without the inbox socket variable. Restart it. |

### `nitipan mail status <id>`

Run it in the sender's session with the message ID that `mail send` printed. It prints the recipient and the stage:
`queued`, `stored`, or `read`. A bounced message shows `bounced:` and the reason. The message came back to the sender's
mailbox with its body. `sent, not acknowledged` means the broker sent it and has not heard that the recipient's machine
stored it.

Bounce reasons include:

- `the broker held it for 7 days without sending it`
- `the recipient was not running, and no resume read it within 7 days`
- `the recipient node is too old`
- `the recipient's machine stayed unapproved or forgotten for 7 days`
- `the recipient's machine was forgotten with the message stored but unread`
- `the recipient's machine was bound to a different user`
- `sender no longer allowed`
- ``recipient holds no `shared` lease``

### Mail needs HTTPS and approval

A node on plain HTTP sends and accepts no mail. Its error says the broker is reached over plain HTTP, and that mail
needs a broker address that is HTTPS with a certificate the node verifies. A machine whose key nobody approved can
neither send nor receive mail. See [Running nodes](nodes.md) and [Users and access](users-and-access.md). Mail's own
rules are in [Mail](../reference/mail.md).

## "node too old" and "broker too old"

A newer side sent something an older side does not know. The older side answers with an error instead of dropping it.

- `node too old`. The CLI is newer than the node. The node restarts itself within 5 seconds of a change to its
  executable. When the error stays, the plist names a different `nitipan` than the one on PATH. Replace the plist with
  the output of `nitipan setup node`.
- `broker too old`. The node or the CLI is newer than the broker. Upgrade the broker and restart it. The broker does
  not restart itself.

See "Version skew" in [Running nodes](nodes.md).

## A git hook refused a commit, rebase, or reset

```
nitipan: git refused to update refs/heads/main. It touches paths that are not yours:
  /path/to/checkout: lease <id> on src is held by <holder>. These paths are theirs while they hold it.
To keep the commit this update would have pointed at, run: git update-ref refs/nitipan/salvage/<id> <sha>
```

The `reference-transaction` hook refuses a branch or HEAD update that touches paths it may not touch. It runs for every
ref update, and `--no-verify` does not skip it. Each cause has its own line:

- another holder's live lease owns the paths
- the holder's liveness is unknown, or the record cannot be used
- your lease was broken or revoked, and the tombstone is not yet delivered
- a fence mark guards the paths against you
- your sub-lease was recalled and its deadline passed

The commit is not lost. Run the printed `git update-ref refs/nitipan/salvage/<id> <sha>` to keep it from being
collected. The salvage ref is outside `refs/heads`, so the hook lets it through. A commit landed on a branch later is
still checked.

The hook fails closed in two other cases. A caller with agent variables and no matching agent process is refused with
`could not identify the calling agent`. A failure to read the leases is refused with `could not read the leases`. Your
own terminal, with no agent variables, is never refused by this hook.

An agent that sees a refusal should stop and report to the user. See [Git hooks](../reference/git-hooks.md).

## A git hook refuses every update after an upgrade

```
nitipan: git refused to update a branch or HEAD, because /usr/local/bin/nitipan is too old for the hook installed in this clone.
nitipan: git refused the push, because the nitipan binary that checks leases is missing.
```

The hook scripts bake in the path of `nitipan` that was on PATH at install time. When that path is gone, they fall back
to the `nitipan` PATH finds. With none, they refuse branch and HEAD updates and every push. A `nitipan` that does not
know the hook is reported as too old.

What to do:

1. Put a current `nitipan` on PATH.
2. Run `nitipan setup git --install <checkout>` again in each checkout. A new version can add a hook, and a claim is
   refused until every hook is in force.
3. To remove the hooks instead, run `nitipan setup git --uninstall <checkout>`. It restores any hook they chained to.

## A push is refused

Pushes under a broker lease go through `nitipan push`. The `pre-push` hook checks every push. See
[Pushing](../reference/pushing.md).

### The push lacks fence refs

```
git refused to push to origin. The push touches paths that a broker lease fences, and it lacks the fence refs: ... Push with: nitipan push origin <branch>
```

A plain `git push` into fenced paths is refused. Push with `nitipan push <remote> <branch>`.

### The fence refs differ from the remote's

```
git refused to push to origin. This clone's fence refs differ from the remote's: ... A push from a clone that lacks them could delete them, for example with --mirror. nitipan fetched them just now, so push again.
```

The hook refuses any push to a remote that holds a fence ref this clone lacks, or holds at another value. A pruning
push from such a clone could delete it. The hook fetches the fence refs as it refuses, so push again. When its fetch
failed, the message says so. Fetch by hand:

```sh
git fetch origin '+refs/nitipan/fence/*:refs/nitipan/fence/*'
```

### A fence ref would be deleted or rewound

```
git refused to push refs/nitipan/fence/..., because the push deletes it. A fence ref may only be created or fast-forwarded.
```

A fence ref may only be created or fast-forwarded, by any caller. A deletion, a rewind, or a diverged update is refused.
A `--mirror` push from a clone without the fence refs hits this. Run the fetch the message names, then push again. To
remove a fence ref on purpose, the user runs `nitipan fence delete <remote> <key>`. See
[Deleting a fence ref](../reference/pushing.md#deleting-a-fence-ref) for the command and
[Running the broker](broker.md) for restoring a broker.

### The lease went to another session

```
git refused to push refs/heads/main. The lease that covered src was granted to another session: origin holds the fence token ... and yours is ....
```

A fence ref on the remote holds a higher token than yours. Another session holds the lease now. The message names the
holder when the broker answers. Your commits are still on your branch. Stop work in that domain and report to the user.

When the message says the token is above any the broker could have issued, the fence ref looks like the result of a
broker restore or of damage. The user deletes it with `nitipan fence delete`. See "Backup and restore" in
[Running the broker](broker.md).

### Could not verify

```
nitipan: could not verify: the broker could not say whether .... This is not a lost lease. Push again once it can
```

The push needed the broker's answer and could not get it. It exits 12. Causes include an unreachable broker, a node
that is not running, and a remote whose fence refs could not be listed. The push is neither allowed nor a lost lease.
Fix the broker or the node, then push again.

### Contention

```
nitipan: fence contention: other pushes kept updating the fence refs on origin, and this push lost the compare-and-swap 5 times. This is contention and not a lost lease. Push again
```

Other pushes kept moving the fence refs while this push ran. It exits 13. Push again.

### The host refuses fence refs

```
origin refused the fence ref. This is the host's ref policy about refs under refs/nitipan/fence/ and not a lost lease. Report this to the user
```

The git host does not accept refs under `refs/nitipan/fence/`. The whole push fails. Change the host's ref policy, or
push that repo somewhere else.

## The remote is marked as unable to take an atomic push

```
nitipan: origin cannot take an atomic push, so this push gets a lease check and not fencing. Recorded in nitipan.origin.atomic
```

A host that cannot take `git push --atomic` cannot hold the fence and the branch in one transaction. `nitipan push`
records `nitipan.<remote>.atomic = false` in the clone's git config. From then on, pushes to that remote renew the
holder's leases with the broker and push without fence refs. That is a lease check, not fencing.

nitipan never clears the flag. When the host later gains atomic push, clear it yourself:

```sh
git config --unset nitipan.origin.atomic
```

The `pre-push` hook trusts the flag whoever set it. Check for it with `git config --get nitipan.<remote>.atomic` when a
remote seems to skip fencing.

## A lease was broken

```
nitipan: lease <id> on src was broken by <holder> at 2026-11-14 10:00:00 PST
```

Exit status 7 means someone broke your lease. The tombstone says who and when. A revoked sub-lease says it was revoked
by the holder of its parent. A recalled one says the deadline passed.

`break --force` also writes a fence mark. While it stands, the broken holder's commits on those paths are refused, and
its `acquire`, `acquire --replace`, and `request` over them exit 7:

```
nitipan: lease <id> on src was broken by <holder> at .... A fence mark keeps src from being claimed again, so this claim is refused. Report this to the user and stop work on those paths.
```

Waiting does not help. A mark lifts for a key once another holder's live lease covers it, and it goes when its holder
dies. Anyone but the guarded holder can remove it with `nitipan break <mark-id>`. `list` shows marks. See
[Leases](../reference/leases.md).
