# `acquire --acting-for` help says "scope" where nitipan means reach

**Status:** fixed 2026-11-14

**Fixed:** 2026-11-14

## Observed

`nitipan acquire --help` describes `--acting-for` as "act in this user's scope under their standing delegation to your
label". The same command has a `--scope <dir>` flag, which names a path inside the checkout. A reader cannot tell the
two apart. The docs call the user and shared lease levels "reach", after `--reach machine|user|shared`.

## Cause

The help string at `internal/cli/acquire.go:58`. The broker's sign-in page also says "scope" for the same concept, in
`internal/broker/signin.go`.

## Repro

```
nitipan acquire --help
```

## Fix

Reword the flag help to use reach, for example "claim in this user's `user` reach under their standing delegation to
your label; names a user label". Decide whether the sign-in page text follows. `docs/reference/broker-auth.md` notes
the mismatch today, and that sentence goes once both say reach.

## Fix note

The `--acting-for` help in `internal/cli/acquire.go` now says "claim in this user's user reach". The grant and
revoke forms in `internal/broker/signin.go` say "claim in your user reach". The sentence in
`docs/reference/broker-auth.md` that admitted the mismatch is gone. Regression tests pin the help line and both
forms: `TestAcquireHelpDescribesActingForAsReach` and `TestTheGrantAndRevokeFormsSpeakOfReachNotScope`.
