# Your first leases on one machine

This walkthrough takes a lease in a checkout, shares part of it with a second session, and takes it back. Everything
here runs on one machine with no daemon. It assumes you finished [Installing nitipan](install.md) and
[Setting up your agents](agents.md).

The examples use a checkout at `/Users/alice/src/widget`. Lease IDs are ULIDs, such as `01JD3K8Q2V7X9M4N6P0R5T1W8Y`.
Output below is trimmed to the lines that matter.

## 1. Install the git hooks in the checkout

A path claim inside a git checkout is refused until nitipan's git hooks are in force there. Without them, a lease could
not stop a session that lost its lease from committing. Install them once per checkout:

```
nitipan setup git --install /Users/alice/src/widget
```

You should see one line per hook, then the path the hooks run:

```
wrote /Users/alice/src/widget/.git/hooks/reference-transaction
wrote /Users/alice/src/widget/.git/hooks/pre-push

The hook runs /Users/alice/.local/bin/nitipan, or the nitipan on PATH when that path is gone.
```

An existing hook of either name is kept and chained. Linked worktrees share the hooks, and every initialized submodule
gets them too. A caller with no agent above it, such as your own terminal, is never refused by the hooks.

If you skip this step, `acquire` refuses with a message that names the command to run:

```
nitipan: the claim is inside a git checkout where nitipan's git hooks are not in force, so a lease there would not fence a ref update:
  /Users/alice/src/widget (<why>). Run: nitipan setup git --install /Users/alice/src/widget
Then claim again
```

See [Git hooks](../reference/git-hooks.md) for what the hooks refuse.

## 2. Claim part of the checkout

In the first agent session, from inside the checkout, claim the `src` directory:

```
nitipan acquire --checkout . --scope src --description "refactor the parser"
```

You should see:

```
acquired
01JD3K8Q2V7X9M4N6P0R5T1W8Y  alive
  domain: /Users/alice/src/widget, scopes src
  handle: <four-word handle>
  ...
  description: refactor the parser
```

`--checkout` defaults to the working directory. Repeat `--scope` to claim several directories. Leave `--scope` out to
claim the whole checkout. Claiming the same domain again refreshes the advertisement and prints `already held`.

## 3. See who holds what

```
nitipan list
```

Every lease prints with its holder's liveness, handle, agent name, and description. Scripts should use the versioned
JSON form instead:

```
nitipan list --json
```

A liveness of `unknown` means treat the holder as alive. See [JSON and records](../reference/json-and-records.md).

## 4. Try a conflicting claim

In a second agent session, claim the same directory:

```
nitipan acquire --checkout /Users/alice/src/widget --scope src
```

It is refused with exit status 3. The message names the lease and its holder. A claim against a dead holder succeeds
instead, and nitipan prints that it cleared the dead holder's lease.

To wait for the lease instead of failing at once, give `--wait` a duration:

```
nitipan acquire --checkout /Users/alice/src/widget --scope src --wait 5m
```

The wait polls with backoff and holds the state lock only during each attempt. There is no queue, so a later claim can
win a freed lease. When the time runs out, `acquire` exits with status 10 and names the holders it waited on.

## 5. Release the lease

In the first session:

```
nitipan release --checkout . --scope src
```

You should see `released 01JD3K8Q2V7X9M4N6P0R5T1W8Y`. You can also name the lease by ID, as in
`nitipan release 01JD3K8Q2V7X9M4N6P0R5T1W8Y`, or release every lease you hold with `--all`. With no target, `release`
refuses and lists your leases.

## 6. Claim a machine resource

A resource lease names a machine resource instead of a path. Claim "all cores" before a full build:

```
nitipan acquire --resource cpu:heavy
```

A second live claim on `cpu:heavy` is refused, and the refusal names the holder. Release it when the build ends:

```
nitipan release --resource cpu:heavy
```

A name admits one holder unless you give it a larger count. Write the counts in
`/Users/alice/.local/state/nitipan/slots.json`:

```json
{"version": 1, "slots": {"gpu:main": 2, "db:pg": 4}}
```

`gpu:main` now admits two holders at once. Any bad entry makes nitipan ignore the whole file. Every call then warns
with the file's path, and every name stays exclusive. `nitipan list` prints the path of `slots.json` and how many
holders each local resource admits.

Resource leases are advisory. Claim them from the parent session only, since subagents share their parent's holder.
See [Leases](../reference/leases.md) for the name grammar and the slot rules.

## 7. Share part of a lease with another session

A holder can delegate part of its domain to another session as a sub-lease. In this example, session A holds the
whole checkout and session B wants `src`.

1. Session A claims the whole checkout:

   ```
   nitipan acquire --checkout /Users/alice/src/widget
   ```

2. Session B tries to claim `src`. The claim is refused with exit status 3, and the refusal names A's lease ID.

3. Session B asks for that part:

   ```
   nitipan request 01JD3K8Q2V7X9M4N6P0R5T1W8Y --checkout /Users/alice/src/widget --scope src \
     --description "fix the tokenizer"
   ```

   You should see:

   ```
   requested 01JD3M2A9C4E6G8J0L2N4Q6S8U
     from lease: 01JD3K8Q2V7X9M4N6P0R5T1W8Y
     parent handle: <A's handle>
     parent agent name: <A's name>
   Ask the parent to run: nitipan grant 01JD3M2A9C4E6G8J0L2N4Q6S8U
   ```

4. Session B tells session A about the request out of band, for example through SendMessage.

5. Session A grants it:

   ```
   nitipan grant 01JD3M2A9C4E6G8J0L2N4Q6S8U
   ```

   You should see `granted 01JD3M2A9C4E6G8J0L2N4Q6S8U to handle <B's handle>, agent name <B's name>`. The sub-lease
   takes the request's ID as its lease ID. To refuse instead, A runs `nitipan decline <request-id>`.

6. Session B learns of the grant from a `nitipan: notice:` line on its next nitipan call. Re-running its exact claim
   on `src` now prints `already held; nothing changed` and exits 0. A claim by B on a directory inside `src`, such as
   `--scope src/lexer`, exits with status 5, already covered. Session B works in `src`.

7. If session A re-runs `nitipan acquire --checkout /Users/alice/src/widget`, it exits with status 11. A still holds
   the lease, but part of it is delegated right now, and the output lists the delegation.

8. When A needs `src` back, it gives B a deadline:

   ```
   nitipan recall 01JD3M2A9C4E6G8J0L2N4Q6S8U --deadline 30m
   ```

   You should see:

   ```
   recalled sub-lease 01JD3M2A9C4E6G8J0L2N4Q6S8U held by handle <B's handle>, agent name <B's name>
     deadline: 2026-11-14T18:30:00Z
     if it has not released by then, its keys go back to lease 01JD3K8Q2V7X9M4N6P0R5T1W8Y
   Tell the sub-lessee out of band, for example through SendMessage or mail to its handle. It also learns on its next nitipan call.
   To take the keys back, run: nitipan acquire --wait 30m10s --checkout /Users/alice/src/widget
   ```

   `--deadline` is required and has no default. A second `recall` replaces the deadline. `--cancel` takes the recall
   back.

9. Session B finishes, commits, and releases:

   ```
   nitipan release 01JD3M2A9C4E6G8J0L2N4Q6S8U
   ```

   The keys go back to A's lease, and A gets a notice. If B does not release in time, the first nitipan call after the
   deadline ends the sub-lease. The `acquire --wait` that `recall` printed is such a call, so A can run it and wait.

To end a sub-lease at once instead, A runs `nitipan revoke <lease-id>`. See [Sub-leases](../reference/sub-leases.md)
for every rule.

## Exit statuses to check in scripts

| Status | Meaning |
| --- | --- |
| 3 | Refused. An overlapping lease blocks the claim, or a resource slot is full. |
| 5 | Already covered. Your own leases already cover the claim, and nothing changed. |
| 10 | Wait timeout. `acquire --wait` ran out of time. |
| 11 | Delegated. You hold exactly these keys, but part of them is delegated to a sub-lessee. |

The [command reference](../reference/cli.md) lists every exit status.

## Next steps

- [Going multi-machine](broker-and-nodes.md).
- Read the [lease reference](../reference/leases.md).
