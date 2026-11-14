# The plain-HTTP refusal offers `node rekey` as a way out, but nothing clears the flag

**Status:** open

## Observed

A node that once saw the broker report cross-user features on records it in `<state>/cross-user-enabled`. From then
on, it refuses plain HTTP to the broker. The refusal, at `internal/node/client.go:164-174`, says: "Use https, or forget
this label on the broker host with 'nitipan broker forget', then run 'nitipan node rekey' and approve the new key".

Following the second route changes nothing. After `broker forget`, `node rekey`, and a new approval, the node still
refuses plain HTTP with the same message.

## Cause

`RecordCrossUserEnabled`, at `internal/node/files.go:190`, creates the file. No code removes it. `Rekey`, at
`internal/node/files.go:176-186`, removes only `node-key`. The function's doc comment says the record is permanent on
purpose: "Once set, the node refuses to send its key over plain HTTP even when a later registration omits the flag."

## Repro

1. Point a node at a broker over plain HTTP on a private address, then turn cross-user features on at the broker.
   Under cross-user, the broker refuses the plain-HTTP key. To reach the recorded state, move the node to https, let
   it register once, and move it back. The test harness can write the file directly instead.
2. Run `nitipan node rekey`, then `broker forget <label>` and `broker approve` the new key.
3. The node still refuses plain HTTP to the broker.

## Fix

The record being permanent is the intended security property. A key the node sends over plain HTTP may be read on
the LAN. So the narrow fix drops the rekey route from the message: "Use https. This node recorded that cross-user
features were on, and will not use plain HTTP to the broker again." `docs/operations/nodes.md` and
`docs/operations/troubleshooting.md` already say the record never clears.

When a deliberate way back to plain HTTP is wanted, it needs its own command from the user's terminal, with agent
variables refused, like `fence delete`. That is a design choice.
