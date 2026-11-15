# Command reference

Every nitipan command, its arguments, and its flags. `nitipan <command> --help` prints the same flags with a longer
description. The topic pages explain what each command does in context:

- [Leases](leases.md) covers `acquire`, `release`, `list`, `break`, `advertise`, `renew`, and `who`
- [Sub-leases and recall](sub-leases.md) covers `request`, `grant`, `decline`, `revoke`, and `recall`
- [Git hooks and fence marks](git-hooks.md) covers `setup git`
- [Pushing under a broker lease](pushing.md) covers `push` and `fence delete`
- [Mail](mail.md) covers `mail`
- [Broker authentication](broker-auth.md) covers the cross-user parts of each command

The root command takes `-v` or `--version` to print the version. A flag mistake or a missing argument exits with the
usage status, 2.

## Leases

### `nitipan acquire`

Claims a domain: a checkout, optionally narrowed to path scopes. `--resource` claims a machine resource instead.

| Flag | Meaning |
|------|---------|
| `--checkout <path>` | checkout to claim. The default is the working directory |
| `--scope <dir>` | path scope inside the checkout. Repeat it for several |
| `--resource <name>` | resource name to claim, such as `cpu:heavy`. One per call, and not with `--checkout` or `--scope` |
| `--slots <N>` | the count you expect for the resource. The claim is refused when it differs from the count that applies |
| `--replace <id>` | rewrite your lease with this ID to the new domain. Repeat it to merge several |
| `--wait <duration>` | wait up to this long, such as `30s` or `5m`, for a live holder or a full slot to clear |
| `--reach <reach>` | `machine`, the default, `user`, or `shared`. `user` and `shared` go through the node to the broker |
| `--acting-for <user label>` | claim for this user under their standing delegation to your label |
| `--name <name>` | the session name others can reach you by, such as the `/rename` name |
| `--permission-mode <mode>` | the session's permission mode, as the agent names it |
| `--description <text>` | what this session does with the domain |

`--reach user` and `--reach shared` claim a checkout. They do not combine with `--resource`, `--replace`, `--wait`, or
`--slots`.

### `nitipan release [lease-id ...]`

Gives up leases you hold. Name them by lease ID, by the domain flags, by `--resource`, or with `--all`. With no target,
release refuses and lists your leases. A broker lease can also be named by its repo name, `repo:<root-sha>#<path>`.

| Flag | Meaning |
|------|---------|
| `--checkout <path>` | checkout whose lease to release. The default is the working directory |
| `--scope <dir>` | path scope inside the checkout. Repeat it for several |
| `--resource <name>` | name of the resource whose lease to release |
| `--all` | release every lease you hold. Subagents share their parent's holder, so this releases the parent's leases too |

### `nitipan list`

Prints every lease with its holder's liveness. With the domain flags, it prints only the leases that overlap that
domain. `list --json` is the supported interface for scripts and guardians. See [JSON output and
records](json-and-records.md).

| Flag | Meaning |
|------|---------|
| `--checkout <path>` | checkout to filter by. The default is the working directory |
| `--scope <dir>` | path scope inside the checkout. Repeat it for several |
| `--resource <name>` | resource name to filter by |
| `--json` | print the versioned JSON document |

### `nitipan break <lease-id> ...`

Removes other holders' leases by lease ID, as `list` prints them. It also takes a fence mark's ID.

| Flag | Meaning |
|------|---------|
| `--force` | break a live holder's lease, or remove a record nitipan cannot use |

### `nitipan renew [lease-id ...]`

Renews broker leases through the node at once and prints their new expiry. With no lease ID, it renews every broker
lease you hold. The node already renews them on its own. This is for a check that wants a fresh expiry before an action
that must not outlive the lease. It takes no flags.

### `nitipan advertise`

Updates what your leases say about the session, without naming keys again. Run it after `/rename`. A flag you leave out
keeps its stored value.

| Flag | Meaning |
|------|---------|
| `--name <name>` | the session name others can reach you by. It changes in every lease you hold |
| `--permission-mode <mode>` | the session's permission mode. It changes in every lease you hold |
| `--description <text>` | what this session does with the lease named by `--lease` |
| `--lease <id>` | the lease a description applies to, when you hold several |

### `nitipan who`

Asks the broker who holds leases. Each subcommand needs the node and a broker.

| Command | Meaning |
|---------|---------|
| `who holds <subject>` | list every holder whose lease overlaps a repo name, path, resource, or qualified name |
| `who is <name>` | list every holder whose agent name or handle is exactly the name |
| `who am-i` | print this node's user label |

Each `who` command takes `--json`.

## Sub-leases

### `nitipan request <lease-id>`

Asks the holder of a lease to delegate part of it to you as a sub-lease. It prints the request ID and the holder's
handle.

| Flag | Meaning |
|------|---------|
| `--checkout <path>` | checkout to ask for. The default is the working directory |
| `--scope <dir>` | path scope inside the checkout. Repeat it for several |
| `--name <name>` | the session name others can reach you by |
| `--permission-mode <mode>` | the session's permission mode |
| `--description <text>` | what this session will do with the part it is granted |

### `nitipan grant <request-id>`

Turns a request into a sub-lease of a lease you hold. Only the holder of the parent lease can grant. It takes no flags.

### `nitipan decline <request-id>`

Removes a request. The holder of the parent lease runs it to refuse, and the requester runs it to withdraw. It takes no
flags.

### `nitipan revoke <lease-id>`

Ends a sub-lease of a lease you hold, at once. Only the holder of the sub-lease's parent can run it. It takes no flags.

### `nitipan recall <lease-id> --deadline <duration>`

Gives a sub-lease of a lease you hold a deadline to finish, commit, and release.

| Flag | Meaning |
|------|---------|
| `--deadline <duration>` | time the sub-lessee has, such as `30m` or `2h`. It is required and has no default |
| `--cancel` | take the recall back instead of making one |

## Setup

### `nitipan setup [agent]`

Prints the entries an agent needs before it can claim leases: the sandbox allowance for the state directory, and the
hooks or plugins that report when a session ends. The agent is the argument, or the agent that runs this command. The
agents are `claude`, `codex`, and `opencode`. `setup` never writes settings.

Three more arguments do other jobs:

- `setup git --install <checkout>` installs the git hooks, and `setup git --uninstall <checkout>` removes them. See
  [Git hooks and fence marks](git-hooks.md)
- `setup node` prints the LaunchAgent that runs the per-user node, with the `launchctl` command that loads it
- `setup broker` prints the LaunchAgent that runs the broker, with the `launchctl` command that loads it

`setup node` and `setup broker` write nothing.

| Flag | Meaning |
|------|---------|
| `--install-plugins <dir>` | with `opencode`: write the opencode plugins into `<dir>/nitipan` |
| `--replace` | with `--install-plugins`: overwrite a plugin file that differs from the one nitipan ships |
| `--install <checkout>` | with `git`: install the git hooks in this checkout |
| `--uninstall <checkout>` | with `git`: remove the git hooks from this checkout |

## Remotes

### `nitipan push <remote> [[+]<src>[:<dst>] ...]`

Pushes branches in one atomic git push that also updates the fence refs of the broker leases you hold. A host that
refuses the fence gets a lease check, unless `nitipan.<remote>.fence` says otherwise. With no refspec, it pushes the
current branch. Patterns are not supported. It takes no flags. See [Pushing under a broker
lease](pushing.md).

### `nitipan fence delete <remote> <key>`

Deletes one fence ref from a remote. The key is `<root-sha>/<encoded-path>` or a repo name, `repo:<root-sha>#<path>`.
It is for the user's own terminal only, and refuses any caller with agent variables or an agent above it. It takes no
flags.

## Mail

| Command | Meaning |
|---------|---------|
| `mail send <address> [message]` | send a message. Without a message argument, it reads standard input |
| `mail read` | print this session's mail, then remove it |
| `mail status [id]` | show this session's mailbox, or the stage of a message it sent |

`mail send` takes `--reply-to <message-id>`, the ID of the message this one answers. See [Mail](mail.md).

## Broker host

`nitipan broker` with no subcommand runs the broker. launchd runs it, from the LaunchAgent that `setup broker` prints.
It reads `~/.config/nitipan/broker.json`. The subcommands run on the broker host only. No node or network path reaches
them. See [Running the broker](../operations/broker.md) and [Users and access](../operations/users-and-access.md).

```
nitipan broker pending                         # list unapproved labels with fingerprints
nitipan broker approve <label> <fingerprint>   # pin the key, bind the label to the broker owner
nitipan broker forget <label>                  # unpin a label whose machine was reinstalled
nitipan broker confirm-upgrade                 # delete the kept v1 file, making rollback impossible
nitipan broker admissions                      # list people who signed in but are not admitted
nitipan broker admit <issuer> <sub> <email>    # admit a user by their issuer plus sub, and print their user ID
nitipan broker link <issuer> <sub> <user-id>   # map a new issuer plus sub to an existing user
nitipan broker bindings                        # list each user's bound labels with last-seen times
nitipan broker revoke-user <user-id>           # forget a user's labels, end their leases, and print what it removed
nitipan broker cross-user <true|false>         # turn cross-user features on or off
nitipan broker break <lease-id>                # end any broker lease as the broker owner
```

The `approve` command binds the label to the broker owner when run on the broker host. A coworker binds through the
sign-in page instead.

## Hidden commands

These commands do not appear in `nitipan --help`.

`nitipan node` runs the node that mirrors this machine's leases to the broker. launchd runs it, from the LaunchAgent
that `setup node` prints. It takes `--codex-path` and `--codex-home`, the trusted Codex executable and Codex home that
the LaunchAgent names. Its three subcommands are for people to run:

| Command | Meaning |
|---------|---------|
| `node fingerprint` | print the fingerprint of this node's key, for the broker's `approve` command |
| `node pair` | print this node's label, its key's fingerprint, and a fresh four-word pairing code |
| `node rekey` | generate a new instance key for a forgotten label |

`nitipan hook` is called only by agent hooks and plugins, and by the git hooks that `setup git` installs. Its
subcommands are `session-start`, `session-end`, `presence`, `execution`, `mail-turn`, `mail-poll`, `mail-retry`,
`mail-reminder`, `mail-stop`, `mail-register`, `mail-wait`, `mail-ring`, `reference-transaction`, and `pre-push`. Do
not run them by hand. `setup` prints the entries that call them.

## Exit statuses

Scripts and agents test these values, so a value never changes once released. A new result takes the next free number.

| Status | Name | Meaning |
|--------|------|---------|
| 0 | ok | the command did what was asked |
| 1 | error | any failure with no status of its own |
| 2 | usage | the command line was wrong |
| 3 | refused | an overlapping lease blocks the claim. That covers another holder's live, unknown, or unclearable lease, and one of the caller's own that the claim would change. It also covers a full resource slot, a `--slots` value that differs from the count that applies, and `--replace` on a resource lease |
| 4 | lock busy | the state lock stayed busy past the retry budget |
| 5 | already covered | the claim is already covered by the caller's own leases, and nothing changed |
| 6 | not yours | the lease belongs to another holder |
| 7 | broken | the lease was broken, and the tombstone says by whom |
| 8 | no such lease | no lease has that ID |
| 9 | unusable record | a record this nitipan cannot use blocks the request |
| 10 | wait timeout | `acquire --wait` ran out of time before the lease could be granted |
| 11 | delegated | the caller already holds exactly these keys, and part of them is delegated to a sub-lessee. The output lists the delegations |
| 12 | could not verify | a push needed the broker's answer and could not get it. It is neither allowed nor a lost lease. Try again once the broker answers |
| 13 | contention | a push kept losing the compare-and-swap on the fence refs to other pushes and gave up. Try again |

The table lives in `internal/exitcode/exitcode.go`.
