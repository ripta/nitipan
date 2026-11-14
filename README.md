# nitipan

nitipan is a lease and coordination tool for coding agent sessions. A session claims ownership of a domain, such as a
checkout or some paths inside one. The claim also advertises the session: what it does, where it runs, and how to reach
it. Other sessions use the leases to find each other and to avoid stepping on each other's paths.

It supports Claude Code, Codex, and opencode.

## Status

The first version is [NIT-001](spec/proposals/NIT-001-local-lease-cli.md). The commands, the Claude Code adapter, and
the Codex and opencode session rules are built. The Codex and opencode hooks and plugins pass automated live checks.
`make live-codex` and `make live-opencode` drive a private Codex app-server and a private opencode server against a
fake model server. They need no manual steps and cost no model tokens.

## First version

The first version runs on one machine, with no daemon. It has ten commands:

- `acquire` claims a domain
- `release` gives it up
- `list` shows every lease and whether its holder is alive
- `break` removes another holder's lease, and needs `--force` when that holder is alive
- `advertise` updates the caller's name, permission mode, or a lease's description
- `request` asks the holder of a lease for part of it
- `grant` hands a requested part to the requester as a sub-lease
- `decline` refuses a request, or withdraws one
- `revoke` ends a sub-lease at once, and only the holder of its parent can run it
- `recall` gives a sub-lease a deadline to finish, commit, and release, and only the holder of its parent can run it
- `setup` prints the sandbox allowance, hooks, and plugins an agent needs, and installs the git hooks with
  `setup git --install <checkout>`

Two more commands work with a remote. `push` pushes branches with the fence refs of the broker leases the caller holds,
and `fence delete` is the user's way to remove one fence ref. See "Pushing under a broker lease" below. A last command,
`hook`, is called only by those agent hooks and plugins, and by the git hooks that `setup git` installs.

A domain is keyed by absolute path, with optional path scopes. A claim against a dead holder succeeds. An overlapping
claim against a live holder is refused, and the refusal names the holder.

A Claude Code holder is alive while its process runs. nitipan checks the PID and the process start time, which guards
against PID reuse. Codex and opencode run every session under one long-lived host process. Their holders are judged by
session instead, through the hooks and plugins that `setup` prints.

Leases live under `~/.local/state/nitipan/`. That directory must be on a local disk. A sandboxed agent needs a write
allowance for it.

`acquire --wait <duration>` waits for a lease instead of being refused at once. See "Waiting" below. A later version
adds a self-hosted broker that works across machines. Callers will not change when the broker arrives. See the
[broker research](spec/reports/2026-10-18-lease-broker-research.md).

## Resource leases

A resource lease names a machine resource instead of a path. `nitipan acquire --resource cpu:heavy` claims "all cores"
for a full build or test run. A second live claim on the same name is refused, and the refusal names the holder.
`release --resource` and `list --resource` take the same name.

A name is `<namespace>:<name>`. Each part holds lowercase letters, digits, `-`, `_`, and `.`. The whole name is at most
64 bytes. A name outside that grammar is refused and never folded. When the only problem is case, the error suggests
the lowercase spelling. The `repo` namespace is reserved.

Names are flat. Two names overlap only when they are equal, so `cpu:heavy` and `cpu:light` never see each other. A
resource never overlaps a path lease. `--replace` does not apply to resource leases. Release the old one and acquire the
new one.

A resource belongs to its machine. A resource lease from another machine, as on a shared home directory, blocks nothing.
`list` marks it remote.

### Counted slots

A name admits one holder unless the user gives it a larger count. A count lets that many holders share the name at once.
The next claim is refused while the slots are full. The refusal names the holders and says to ask the user to raise the
count. A dead holder's slot is freed for the next claim.

The counts live in `slots.json`, in the state directory beside `leases/`. The user writes it, and every resource claim
reads it under the lock:

```json
{"version": 1, "slots": {"gpu:main": 2, "db:pg": 4}}
```

Each key is a resource name under the grammar above, and each count is an integer of at least 1. Any bad entry, an
unknown `version`, or an unknown top-level field makes the whole file unusable. Every call then warns with the file's
path and every error, and treats every name as exclusive. Path claims are unaffected.

Each lease records the count it was admitted under. A new claim is admitted only while the live holders number fewer than
the smallest of the configured count and every live holder's recorded count. Only this machine's leases count. Lowering
a count takes effect at once. Raising it takes effect once every holder admitted under a lower count has released.

A holder already on the name can claim it again at any time. That refreshes its lease and keeps its recorded count, even
when the slot is now over its count. A holder admitted under an old count therefore pins that count until it releases.
Ask the user to `break --force` it if it must go.

`--slots <N>` states the count you expect for the resource. A new claim is refused when `N` differs from the configured
count, and the error names the configured one. A repeat claim is compared with the count on the lease you already hold.

`list` shows, for each local resource lease, how many holders the name admits and which leases hold it. It also prints
the path of `slots.json`. `list --json` carries the same two values as `effective_slots` and `slot_holders` on each
local resource lease. Every lease stays a top-level entry, and a lease with the `remote` flag carries neither value.

`--wait` applies to resource claims, including a full slot. See "Waiting" below.

Resource leases are advisory. They coordinate cooperating sessions only. A process that never calls nitipan can still
use every core.

Acquire resources from the parent session only. Subagents share their parent's holder, so two subagents that both claim
`cpu:heavy` both succeed, and the first to release frees the name while the other still runs.

## Sub-leases

A holder can delegate part of its domain to another session. The other session then works in that part, and the holder
keeps the rest. The delegated part belongs to the sub-lessee alone while the sub-lease lives. Only path leases can
delegate. A resource lease cannot, and a sub-lease cannot delegate again.

The sub-lessee asks and the holder grants:

1. The other session runs `nitipan request <lease-id> --checkout <path> --scope <dir>`. The lease is the one that a
   refused `acquire` names. Request prints a request ID and the holder's handle.
2. The requester tells the holder out of band, for example through SendMessage.
3. The holder runs `nitipan grant <request-id>`. The output names the requester's handle and agent name. The holder can
   run `nitipan decline <request-id>` instead. The requester can run it to withdraw.

A grant binds the requester's exact identity, which is recorded in the request. The sub-lease takes the request's ID as
its lease ID, and its name, permission mode, and description from the request. The grant is refused when the requester
is the holder's own session, when the requester has died, when the keys are no longer inside the lease, and when
another live lease overlaps them.

The requester learns of a grant or a refusal from a notice on its next nitipan call. A notice goes to stderr as
`nitipan: notice: ...` and is removed once it is delivered. A `list` that takes the lock delivers the caller's notices
like any other command, and `list --json` also reports them in its `notices` array. A `list` that reads without the
lock delivers none. Requests wait in `requests/` and notices in `notices/`, both in the state directory. Older binaries
never read either one.

While a sub-lease lives:

- A claim by anyone else on the delegated part is refused, and the error names both holders.
- The holder's own claim on the delegated part is refused too. The error names the sub-lessee, and the refusal can be
  waited out with `--wait`.
- The sub-lessee's claim inside its sub-lease reports that it is already covered. A claim that reaches outside is
  refused by the holder's lease.
- `acquire --replace` is refused on a sub-lease. A holder's `--replace` that would drop the delegated keys, or absorb a
  lease that has sub-leases, is refused and lists them.
- The holder that re-runs `acquire` with exactly its own keys exits with status 11 and lists the delegations. Status 11
  means that the holder still holds the lease, but does not own all of it right now.

When a sub-lease or its parent ends:

- The parent's holder can run `nitipan revoke <lease-id>` to end a sub-lease at once. The sub-lessee gets a tombstone
  that names the parent's holder, with the cause `revoked`. Anyone else is refused, and the error names the parent.
  Subagents share their parent's holder, so any subagent of the holder can revoke.
- The parent's holder can run `nitipan recall <lease-id> --deadline <duration>` to give a sub-lease time to finish. The
  duration is required and has no default. The sub-lessee gets a notice with the deadline on its next nitipan call. The
  output names the sub-lessee's handle and agent name and the absolute deadline. Tell the sub-lessee out of band, for
  example through SendMessage or mail. The output also prints an `acquire --wait` command on the parent's whole key set.
  There is no timer. The first locked nitipan call by anyone after the deadline ends the sub-lease as a `revoke` would,
  with a tombstone of the cause `recalled` and a "sub-lease ended" notice to the parent. A sub-lessee that releases
  first returns its keys as usual. A second `recall` replaces the deadline. `nitipan recall <lease-id> --cancel` takes
  the recall back and tells the sub-lessee. A recall ends when its parent dies, is released, or is broken, and the
  sub-lease then stands alone. A recall whose parent record this nitipan cannot read ends nothing, and is reported.
  The holder's `acquire --wait` on exactly its own keys waits while a delegation is under a recall, and exits 11 without
  `--wait`, listing the deadline. An older nitipan never reads `recalls/`, so it keeps answering the sub-lessee as
  covered past the deadline until a newer binary takes the lock.
- A sub-lessee's `release`, a forced `break` of the sub-lease, and a dead sub-lessee that nitipan clears each return the
  keys to the parent. The parent's record does not change. The parent gets a notice with the sub-lease's ID, its keys,
  and how it ended. The output of a `break` says the keys went back.
- A parent that dies, is released, or is broken takes only itself. Each live sub-lease is rewritten without `parent` in
  the same call, at the lowest record version its fields need. Its sub-lessee gets a notice that says how the parent
  ended. The output of `release` and `break` lists the sub-leases that now stand alone. A parent that died is handled by
  the next nitipan call that takes the lock.
- Releasing the parent ends the grant for good. The former parent cannot revoke a former sub-lease, and cannot claim its
  old domain while the sub-lease stands.
- A request whose parent lease ended is declined with a notice. A notice whose holder is dead is removed by the next
  call that takes the lock.
- A binary older than a sub-lease's record version never rewrites it and writes no notice for it.

`list` shows the tree. The text output prints each sub-lease indented under its parent, and each pending request under
the lease it asks to share. A sub-lease whose parent is dead or gone prints at the top level, with its stored `parent`
link, until a locked call repairs it. A `list` that reads without the lock prints it that way too.

`list --json` has schema version 1.6, an additive change over 1.2 that adds the `marks` array. The sub-lease fields
below came in with 1.2, an additive change over 1.1. Consumers ignore fields they do not know. Every lease stays a
top-level entry in `leases`, so a consumer of the earlier schema still sees all of them.

- `parent` is on a sub-lease, and names its parent lease ID.
- `delegated` is on a parent that has live sub-leases. It lists each one as `{"id": ..., "keys": [...]}`. It is
  computed when `list` runs and is never stored. A lease with no live sub-leases carries no `delegated` field.
- `owner` and `grantor_of` appear only when `list` is filtered by a domain. An unfiltered `list` has no path to answer
  for, so it omits both. `owner` is true when the filter overlaps a part of the lease that no live sub-lease holds, and
  false when the match lies wholly inside the sub-leases. `grantor_of` lists the IDs of the live sub-leases whose keys
  the filter overlaps, and is absent when there are none. A parent that holds `/repo` and delegated `/repo/src` is the
  owner and the grantor when filtered by `/repo`. Filtered by `/repo/src`, it is only the grantor, and the sub-lease is
  the owner. A resource filter sets neither mark.
- `requests` lists the pending requests, each with its `id`, `parent` lease ID, `requester` holder (which carries the
  handle), `liveness`, `keys`, `description` when the requester gave one, and `requested_at`. A request that `grant`
  is already working on is not pending. A filtered `list` keeps the requests whose keys overlap the filter.
- `recall` is on a sub-lease whose parent recalled it and whose recall is still pending, as
  `{"parent": ..., "deadline": ...}`. Schema version 1.6 adds it. A `list` that reads without the lock treats a
  sub-lease past its deadline as ended, so the parent reads as the owner, and warns on stderr.
- `notices` lists the notices this call delivered, each with its `cause`, `target`, `paths`, `at`, and a `summary`
  sentence. Delivery removes a notice, so read this array when stderr is not captured.

`requests`, `notices`, and `tombstones` are always arrays, never null.

A sub-lease is its own record in `leases/`, with a `parent` field that names the lease it came from. The parent's record
does not change. Records that use `parent`, and requests and notices, carry record version 1.2. Fence marks carry 1.3.
Recalls, in `recalls/`, carry 1.4 with the `recalled` and `recall-cancelled` notices and the `recalled` tombstone cause.
A lease that uses none of them keeps version 1.0 and stays writable by older binaries.

## Git hook and fence marks

`break --force` removes a live holder's lease, and the holder learns of it only on its next nitipan call. Until then it
can keep committing. A git hook closes that gap on one machine. The state directory is the authority, so the hook never
leaves the machine and needs no network.

`nitipan setup git --install <checkout>` installs a `reference-transaction` hook. It runs for every ref update:
`commit`, `rebase`, `reset`, `update-ref`, `branch -f`, and `branch -D`. `--no-verify` does not skip it. It judges only
updates of branches and HEAD, and every other ref passes. For each update it works out which paths the update touches,
and refuses it when it touches a path that:

- another holder's live record owns, under the sub-lease rule that a parent does not own what it delegated
- a record covers whose holder's liveness is unknown, or that nitipan cannot use
- an undelivered tombstone addressed to the caller lists
- a fence mark addressed to the caller guards
- a sub-lease of the caller ended at the deadline of its recall, which gives the keys back to the parent

A path no live record covers passes, because a lease is not required to commit. A dead holder's record does not refuse.
A caller with no agent variables is the user's own terminal and is never refused. A caller with agent variables and no
matching ancestor fails closed.

The paths of an update come from the commits it adds and removes, compared with the branches of every worktree and the
remote-tracking refs. A pull, a rebase onto upstream, and a checkout of `origin/main` therefore touch nothing. A branch
checked out in a worktree also counts the tree difference between its old and new values, when that worktree's index
still matches the old value. HEAD counts added commits only. `git gc` and `git pack-refs` pass. A refusal prints the new
commit and a pin, `git update-ref refs/nitipan/salvage/<id> <sha>`, that keeps it from being collected. The pin writes
outside `refs/heads`, so the hook lets it through. A pinned commit landed on a branch later is still checked.

Each refusal kind has its own message. A refusal for a lease that ended quotes how it ended and names no command, and
says to report to the user. The recall refusal quotes the recall's deadline and the parent's holder in the same way.

The hook reads recalls without the lock. It applies the same rule as `list`. A sub-lease past its recall's deadline
counts as ended, so the parent owns its keys and the parent's commits pass. Before the deadline, the hook passes the sub-lessee's
commits and prints the deadline on stderr. That line writes nothing and consumes nothing. A recall record the hook cannot
use, and a recall whose parent record it cannot use, end no sub-lease. The hook reports each and ignores the recall.

The hook goes into the directory git uses for hooks, so a linked worktree shares it. It goes into every initialized
submodule too. An existing `reference-transaction` hook is kept and chained, and `setup git --uninstall <checkout>` puts
it back. When that directory holds files git tracks, such as husky's, nothing there is written. The hook goes under the
git directory instead, the checkout's local `core.hooksPath` points at it, and every hook git documents chains to the
tracked directory. Uninstall restores the old `core.hooksPath`.

The script bakes the path of `nitipan` on PATH, not the symlink-resolved path that an upgrade can delete. It falls back
to the `nitipan` that PATH finds when the baked path is gone. With none, it refuses updates of branches and HEAD only,
and the message says to reinstall nitipan. A `nitipan` that does not know the hook is reported as too old.

A path claim inside a git checkout is refused until the hook is in force there, and the error names the install
command. Resource claims and paths outside any checkout are not affected. A tool that rewires `core.hooksPath` later
is caught at the next `acquire`. `break --force` says whether the broken lease's checkout had the hook in force, so the
user knows whether the break is fenced.

The same install writes a second hook, `pre-push`. See "Pushing under a broker lease". A path claim in a git checkout
is refused until both hooks are in force, so a checkout that has only the `reference-transaction` hook from an
earlier version needs `setup git --install <checkout>` once more. An existing `pre-push` hook is kept and chained like
the first.

### Fence marks

A tombstone is delivered and removed on the holder's next call, so it cannot guard the keys for long. `break --force`
therefore also writes a fence mark in `marks/`, in the same locked call. A mark carries the broken lease's path keys and
the account of the break. While it stands:

- the hook refuses the holder's ref updates on those keys
- the holder's `acquire`, `acquire --replace`, and `request` over those keys are refused with status 7, and the refusal
  cannot be waited out

A mark lifts for a key once another holder's live record covers it, and it goes when its holder dies. `list` shows
marks, and `list --json` has them in the `marks` array of schema 1.3. `break <mark-id>` removes one with no `--force`.
A holder cannot remove a mark that guards against itself. A mark nitipan cannot use is shown as unusable and needs
`--force`. Resource leases and sub-leases that return to a live parent get no mark.

Two mixed-binary gaps remain. An older binary's `break` writes no mark, and the mark appears when a newer binary
delivers the tombstone. An older binary's `acquire` by the marked holder ignores the mark, though the hook still
refuses that holder's commits on those keys.

## Pushing under a broker lease

A broker lease guards a domain that several machines clone. A partitioned holder commits into its own clone and
collides with nobody. The collision is the push. Every broker grant carries a fencing token that only goes up, and a
push compares the holder's token with the fence refs on the remote.

`nitipan push <remote> [<branch> ...]` does one `git push --atomic` with the branch refs and the fence refs together. A
fence ref is `refs/nitipan/fence/<root-sha>/<encoded-path>`, and it holds the highest token pushed for that key. The
push fetches the fence refs, refuses when one holds a token higher than the holder's own, and otherwise writes its
token to the refs under its keys. The fence refs go by SHA with `--force-with-lease`, so git's compare-and-swap makes the
check and the write one step. A push that loses the compare-and-swap to a sibling push fetches again and retries, up to
five times, and then reports contention. A refusal says the lease was granted to another session, and says to report to
the user. With no refspec, the current branch is pushed. A refspec is a branch, `<src>:<dst>`, or `:<dst>` to delete,
with `+` to force. No pattern is accepted.

The `pre-push` hook is the authority. It recomputes the fence refs the push needs and refuses a plain `git push` that
lacks them, and the message names `nitipan push`. `--no-verify` skips the hook, which is the accepted risk that hooks
can be bypassed. A caller with no agent variables, such as the user's own terminal, passes with no fence. A caller with
agent variables and no matching ancestor is refused when the push updates a branch or carries a fence ref. A push of
tags alone passes, as it does at NIT-001's local hook. An agent whose own broker leases do not cover a path that has a
fence ref at or above it must be told by the broker that no live lease covers it. When the broker cannot say, the
push is refused as "could not verify", exit status 12, which is not a lost lease.

Nothing deletes a fence ref automatically. The hook refuses any update of a fence ref that is not a creation or a
fast-forward, for every caller. It also refuses any push to a remote that holds a fence ref this clone lacks, or holds
at another value, because a pruning push such as `git push --mirror` would delete it, and git does not tell the hook
about those deletions. The hook fetches the fence refs as it refuses, so pushing again passes. The refusal names the
fetch it ran:

    git fetch <remote> '+refs/nitipan/fence/*:refs/nitipan/fence/*'

One window stays. Another machine can create a fence ref after the hook reads the remote and before a pruning push
lands, and the push then deletes it. Only a server-side hook closes that window, so it is accepted.

An agent's push may carry only the fence refs its own leases call for. Any other fence ref is refused.

`nitipan fence delete <remote> <key>` removes one fence ref, for example one marked unusable after a broker restore. The
key is `<root-sha>/<encoded-path>` or a repo name, `repo:<root-sha>#<path>`. It refuses any caller with agent variables,
and no error an agent reads names it, because deleting a live fence lets a stale push land.

A host that cannot take an atomic push cannot hold the fence and the branch in one transaction. For such a remote,
`nitipan push` records `nitipan.<remote>.atomic = false` in the clone's config. The push then renews the holder's
covering leases with the broker and pushes the branches without fence refs. That is a lease check, and every message
says so. It is not fencing. An unreachable broker is "could not verify" and can be retried. A host that refuses refs under
`refs/nitipan/fence/` fails the whole push, and the message says it is the host's ref policy.

The pre-push hook trusts the flag whoever set it. An agent that runs `git config nitipan.origin.atomic false` in its
clone gets only the lease check on that remote, not the fence. This falls under the accepted risk that hooks are
bypassable, as `--no-verify` does. nitipan never clears the flag. A host that later gains atomic push stays on the lease
check until you run `git config --unset nitipan.<remote>.atomic`.

`make live-fence-hosts LIVE_FENCE_ARGS="<clone>"` probes each remote of a throwaway clone for atomic push and fence refs.
It pushes and then deletes a probe branch and a probe fence ref, and is never part of `make ci`.

## Waiting

`nitipan acquire --wait 5m` waits for a lease instead of being refused at once. It applies to path claims and resource
claims alike. The duration is required, and it is a Go duration such as `30s` or `5m`. A missing, unparseable, or
non-positive value is a usage error.

A wait polls with backoff, starting at 100ms and growing to a 2s pause. It takes the state lock only for each attempt
and releases it between attempts, so other commands run while a waiter polls. Only three outcomes are waited on: a live
holder overlaps the claim, the slot is full, or the lock is busy. A busy lock counts as a failed attempt, not as the
lock-busy exit. Every other refusal exits at once with its own status, such as an unusable record, a dead holder whose
record asks to be kept, an overlap with your own lease, or a `--slots` that differs from the count.

When the time runs out, `acquire` exits with status 10. The message names the holders it waited on. When the last
attempt found the lock busy, it names the process holding the lock too.

No file is created while waiting, so a waiter that is killed leaves nothing behind. A shell tool that kills a long
`acquire` may do so after the lease was published. Retry the same `acquire`. It reports the lease as already held. Or
run `list`.

There is no queue. A waiter can lose a freed slot to a claim that arrived later.

## Mail

A session can send a message to another session, on any of your machines, through the broker. It needs the broker and a
node on each machine, and the broker over HTTPS with a certificate the nodes verify. A node on plain HTTP gets leases
and no mail.

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

A new machine can neither send nor receive mail until you approve it on the broker host. The machine's node prints its
key fingerprint with `nitipan node fingerprint`. On the broker host, run `nitipan broker pending`, then `nitipan broker
approve <label> <fingerprint>`. After a machine is reinstalled, run `nitipan broker forget <label>` and approve its new
key. No agent session may run as the broker's OS user on the broker host.

For Claude Code, `nitipan setup claude` prints the hooks. A waiter hook wakes an idle session when mail arrives. The node
rings the session's inbox socket when no waiter is armed. Every ring only says that mail is pending. Only `nitipan mail
read` removes it. A Claude Code setting of `crossSessionInbound: refuse` stops the waiter. Codex and opencode also register their mailboxes at session start or through the server plugin, and on every mail
call. Codex's SessionStart and turn hooks are printed by `setup codex`. The node rings idle Codex sessions through
`codex queue`. Save the LaunchAgent printed by `setup node` with the Codex executable and `CODEX_HOME` it resolves.
The node executes only those trusted values. A session using another `CODEX_HOME` gets turn reminders and no queue
push; `mail status` explains the mismatch. Queueing also reaches an embedded Codex TUI without a daemon in the tested
Codex version.

The opencode server plugin watches for mail and rings through `session.prompt`. Its context hook repeats unread-mail
reminders during later turns and tool steps, even after an ignored wake. These reminders start no new turn. It
publishes presence while loaded,
and `mail status` reports missing or stale presence. Restart the service after replacing its installed plugin. Sessions
on one shared host keep separate mailboxes and ring ledgers. Every wake path only signals; the session runs
`nitipan mail read` to consume its mail.

## Broker authentication

The broker authenticates users whose machines call it. Each node keeps a pinned bearer key. The broker binds each key's
label to one user. The broker owner is the user approved on the broker host. Other users sign in through OIDC on a page
the broker serves.

### Broker configuration

The broker config names an OIDC identity provider in its `oidc` block:

```json
{
  "store": "...",
  "listen": "...",
  "oidc": {
    "issuer": "https://idp.example.com",
    "client_id": "nitipan-broker",
    "client_secret": "...",
    "redirect_url": "https://broker.example.com/callback",
    "ca_file": "/path/to/ca.pem"
  }
}
```

The `issuer` and `redirect_url` must be `https`. Loopback addresses are exempt for tests. The broker refuses to start
with a `redirect_url` that is missing or plain HTTP off loopback. The `client_secret` lives
only in the broker config on the broker host. No node or agent sandbox reaches it. The `ca_file` is optional. Use it
for a self-hosted issuer with a private CA.

The broker fetches the provider's discovery document on the first sign-in, not at start. A provider that is down when
the broker starts leaves leases and mail working, and every existing binding keeps working. The sign-in page answers
503 until the provider can be reached, so only new bindings, delegations, and grants wait.

### Upgrading to store 2.0

On upgrade, the broker store becomes version 2.0. Every pinned label is bound to the broker owner in one step. The
broker keeps the previous store file beside it for rollback. After you confirm the upgrade with
`nitipan broker confirm-upgrade` on the broker host, a rollback is impossible. The command deletes the kept file.

### Binding a coworker's machine

A coworker whose machine needs broker access is admitted on the broker host, then binds their label through the
broker's sign-in page.

On the broker host, list pending admissions with `nitipan broker admissions`, then admit the user with
`nitipan broker admit <issuer> <sub> <email>`. The issuer and `sub` come from the identity provider. The broker maps
them to a broker-assigned user ID.

On the coworker's machine, run `nitipan node pair`. It prints the machine's label, its key's fingerprint, and a
four-word pairing code. The code expires in 10 minutes. The coworker opens the broker's sign-in page, signs in with their identity provider,
and enters the label and the pairing code on the form. The binding pins the key that sent the code and discards the
label's other pending keys. Five wrong codes from one signed-in user for one label lock that user out of that label until the code
expires.

After a machine is forgotten with `nitipan broker forget <label>`, run `nitipan node rekey` on that machine to
generate a new key. The label stays the same. Then approve the new key on the broker host with `nitipan broker approve
<label> <fingerprint>`, or run `nitipan node pair` and bind the label it prints through the sign-in page with the
fresh code. The old key stays refused, even when it registers again.

### Broker-host commands

These commands run on the broker host only. No node or network path reaches them.

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

### Turning cross-user features on

Cross-user features stay off until the broker owner turns them on with `nitipan broker cross-user true` on the broker
host. The command refuses when:

- No OIDC provider is configured in the broker config. Configure an `oidc` block and retry.
- The broker's listener is neither on a loopback address nor serving TLS itself. Set `cert_file` and `key_file` in the
  broker config to serve TLS, or listen on `127.0.0.1` or `::1`. An ingress on another host may terminate TLS and
  re-encrypt to the broker. An ingress that speaks plain HTTP to the broker over a private hop is not enough.
- Any label's current key, or its pinned key, reported plain HTTP to the broker. Once a node sends its key over plain
  HTTP, that key may have been read on the LAN. The mark stays with the key, so a later HTTPS report does not clear it.
  Forget each such label with `nitipan broker forget <label>` on the broker host, then have the node run `nitipan
  node rekey` and approve its new key.
- Any label's current key, or its pinned key, never reported its scheme. Every key from store 1.x is one. Forget the
  label, upgrade the node's nitipan binary, run `nitipan node rekey`, and approve the new key.

A forgotten label has no current key, so it never blocks the command.

The broker always has an owner, so the owner never blocks the command. The broker assigns its owner when it first
opens its store, and an upgrade from store 1.x keeps the owner its migration assigned. A store whose owner record went
missing gets it back under the same user ID when the broker opens it, so every label bound to the owner stays bound.

Once cross-user features are on, an unbound label is refused for leases and mail. A node records the flag and never
sends its key over plain HTTP again, even when a later response omits the flag.

While cross-user features are off, the broker serves only the owner's labels and unbound labels. A label bound to any
other user is refused for leases, discovery, break, and mail, with an error that names `nitipan broker cross-user
true`. A coworker can still bind their machine on the sign-in page while the features are off, so setup can happen
first. `nitipan broker cross-user false` turns them off again. The coworker's machines are then refused, and their
bindings, leases, delegations, and grants stay as they are for when the features come back on.

### Checking your user label

`nitipan who am-i` prints the caller's user label through the node. The user label is what `acting_for`, cross-user
mail, delegations, and grants use to name users.

### Lease scopes across users

Each bound user has their own `user` scope. Two users' `user` leases on overlapping names do not conflict. Two labels
of one user do. Every bound user shares one `shared` scope. A `user` lease and a `shared` lease on overlapping names
conflict, whichever user holds each. An overlap report never shows another user's mirrored `machine` lease, at any
reach.

While cross-user features are off, the broker runs one scope, as before. A label nobody approved counts in the broker
owner's scope then, and its claims for another user are refused.

### Delegating to a shared agent

A shared agent serves several users. It claims in a user's scope only under that user's standing delegation to the
agent's label. Grant or revoke a delegation on the broker's sign-in page with "Grant Delegation" or "Revoke
Delegation". Each action needs its own sign-in. Type the agent's label on the form that follows. No request through a
node can grant or revoke a delegation.

The agent claims for a user with `nitipan acquire --acting-for <user label>`. The user tells the agent their user
label, which `nitipan who am-i` prints. The lease then counts in that user's `user` scope only. A claim for another
user without that user's delegation is refused, and the error names the sign-in page. A node from before this feature
answers such a claim with "node too old", and nitipan writes no request. A node whose broker is from before this
feature answers "broker too old", and forwards nothing.

A claim without `--acting-for`, or from an older nitipan, acts as the label's own bound user. The broker replaces any
other value it carries.

Revoking a delegation ends every lease the agent's label holds for that user. The agent's node writes the holder a
notice. An ended lease is never revived. `nitipan broker revoke-user` ends every lease held for that user, and every
lease the user's labels hold for others, the same way.

### Releasing and breaking across users

A lease is a user's own when its holder's label is bound to that user, or when its `acting_for` names that user. Any
of the user's labels may release or `break --force` it. That includes a lease a shared agent holds for the user.
Name the lease by its ID: `nitipan release <lease-id>`. A release by a label other than the holder's ends the lease,
and the holder's node writes its holder a notice.

A user may not `break --force` a coworker's lease. A coworker's live `shared` lease ends when its holder releases it,
when its term runs out, on a death report, or by a break from its own user. The broker owner may end any lease with
`nitipan broker break <lease-id>` on the broker host. The holder's node then writes a notice that names the broker
owner by user label, as in "broker owner <user label>". The refusal of a coworker's break names each of these ways forward.

### Discovery across users

`nitipan who holds`, `nitipan who is`, and `nitipan list` show every bound user's `shared` leases. A coworker's
`user` leases and mirrored `machine` leases show only to that coworker, and to labels holding their delegation. A
lookup by holder name across users matches only `shared` leases. A lease whose owner the broker cannot resolve shows
only when it is `shared`.

While cross-user features are off, discovery shows every lease.

### A claim refused by a lease you cannot see

A coworker's hidden `user` lease can refuse your `shared` claim. The refusal then names no holder, path, node, or
user. It says the name is held at `user` reach by another user. Wait for that lease to end, or ask the broker owner.
Leases you can see are still listed in full. The refusal is an ordinary message, so an older nitipan prints it the
same way.

The coworker's node writes them one notice per lease per refused user. It names only their own lease and your user
label, never the name you asked for. The notice is delivered on the holder's next nitipan call. It never rings, wakes
no session, and prints nothing from a turn hook. The broker records each notice in the lease, so a broker restart
sends no second one.

### Mail across users

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

### Running the broker safely

Run the broker as an OS user, or on a host, where no agent session runs. The broker-host commands act on the broker
directly as its OS user. An agent session running as that OS user on that host could run them and approve its own
machine.

## Codex and opencode

Run `nitipan setup codex` or `nitipan setup opencode` to print what each agent needs. Neither command edits a settings
file.

For Codex, `setup` prints the sandbox write path, a SessionStart hook and a SessionEnd hook that run `nitipan hook`,
and `thread_unload_delay_secs = 60`. Codex fires SessionEnd only when it unloads a thread. The short delay means a
thread's background terminals and MCP servers stop 60 seconds after you switch away from it. Restart the Codex daemon
after you change the config. `acquire` refuses until the hooks and the delay are in force.

For opencode, `nitipan setup opencode --install-plugins <dir>` writes the TUI and server plugins into `<dir>/nitipan`,
with nitipan's absolute path baked in. It writes plugin files and nothing else. A session needs a TUI with the plugin
loaded before it can `acquire`.

## Relationship to coding_agent_standards

[coding_agent_standards](https://github.com/ripta/coding_agent_standards) defines guardians. A guardian is a long-lived
session that owns the changes in a domain. Today a guardian is found only by its session name, `<domain>_guardian`.
Nothing stops two guardians from claiming overlapping paths.

nitipan is the lease tool that protocol expects. nitipan owns the lease record format and the state directory layout.
coding_agent_standards owns the guardian protocol and the requestor rules. Guardians use nitipan when it is installed.
They fall back to session names when it is not.

## License

See [LICENSE](LICENSE).
