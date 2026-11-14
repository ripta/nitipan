# `broker cross-user` help says it lists unbound labels first, but it commits first

**Status:** open

## Observed

The `cross-user` help, at `internal/cli/broker_admin.go:91-93`, says: "Turning them on first lists every label nobody
bound. Each is refused from then on". A reader expects a chance to look at the list before the change lands. In fact
the command turns the features on, then prints the list. The labels it names are already refused.

## Cause

`Broker.SetCrossUserEnabled`, at `internal/broker/user.go:453-531`, collects the unbound labels and then commits
`CrossUserEnabled = true` in the same call. It returns the list with the commit. There is no step that shows the list
without committing.

## Repro

On a broker host with an approved label that no user is bound to, and every precondition met:

```
nitipan broker cross-user true
```

The output names the unbound label. `nitipan broker cross-user` is already on when it prints.

## Fix

The narrow fix rewords the help to match: turning the features on prints every label nobody bound, and each is
refused from then on. It tells the reader to run `nitipan broker pending` and `nitipan broker bindings` first.
`docs/operations/users-and-access.md` already says that.

The fuller fix adds a dry run, such as `broker cross-user true --check`, that runs every precondition and lists the
unbound labels without committing. That adds a flag, so it is a design choice.
