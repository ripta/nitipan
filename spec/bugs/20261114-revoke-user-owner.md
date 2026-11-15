# `broker revoke-user` accepts the broker owner's own user ID

**Status:** fixed 2026-11-15

**Fixed:** 2026-11-15

**Estimate:** small; no open design decisions

## Observed

`nitipan broker revoke-user <owner user ID>` runs on the broker owner like on any other user. It forgets every label
bound to the owner, ends the owner's leases, and removes the owner's issuer mappings. Every machine the owner approved
is then unpinned.

## Cause

`Broker.RevokeUser` at `internal/broker/user.go:342` checks only that the user exists. The admin handler at
`internal/broker/pin.go:592-611` adds no check. Nothing compares the ID with the store's `OwnerID`.

## Repro

In a broker test harness: open a store, approve a label so it binds to the owner, then call `RevokeUser` with the
owner's ID. It returns no error, and the label is forgotten.

On a broker host: `nitipan broker bindings` shows the owner's user ID. `nitipan broker revoke-user <that ID>` succeeds.

## Fix

Refuse the owner's ID in `RevokeUser` with an error that says the owner cannot be revoked. The error also says to use
`broker forget <label>` for one of the owner's machines. Add a test beside `internal/broker/revoke_test.go`.
`docs/operations/users-and-access.md` warns about this today, and that warning goes with the fix.

Whether the owner should ever be revocable is a design question. Today no flow hands ownership to another user, so a
revoked owner leaves a broker whose owner has no labels. The refusal is the narrow fix until such a flow exists.

## Fix note

`Broker.RevokeUser` in `internal/broker/user.go` now refuses the owner's ID with `ErrOwnerUser`. The error points at
`broker forget <label>`. The admin handler maps it to a bad request in `internal/broker/pin.go`. The regression test is
`TestRevokeUserRefusesBrokerOwner` in `internal/broker/revoke_test.go`. The warning in
`docs/operations/users-and-access.md` is replaced by the refusal.
