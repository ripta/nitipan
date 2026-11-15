# The node lease rig's renewal block lets an in-flight renewal through

**Status:** open

## Observed

`TestLostToAnotherHolderWritesANotice` in `internal/node/lease_test.go` failed once in a full `make ci` run under `-race`.
The second node's claim was refused: the first lease was still `live` at revision 3, with an expiry five minutes past the
advanced clock. The test passed five times in a row when run alone with `-race -count=5`.

## Cause

`blockRenewals(true)` stores an intercept that answers later `/v1/renew` requests with 503. A renewal that is already past
the intercept check when the block goes up still reaches the broker. Under load, that renewal lands after
`clock.Advance(7 * time.Minute)`, so the broker renews the lease on the advanced clock, and `Sweep` finds nothing to lapse.

This is a missing completion signal in the fixture, not a product bug. A real renewal racing a broker outage behaves the
same way, and that is correct.

## Repro

Run `make ci` on a loaded machine. Not reliable. The test alone does not reproduce it.

## Fix

Make `blockRenewals(true)` wait until no renewal is in flight before it returns. For example, count requests inside the
intercepted handler and wait for the count to drain. Other tests that call `blockRenewals(true)` and then advance the
clock have the same exposure, such as `TestNotFoundRenewalWritesALossNotice`.
