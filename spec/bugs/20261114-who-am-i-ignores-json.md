# `who am-i --json` accepts the flag and prints text

**Status:** open

## Observed

`nitipan who am-i --json` prints the same text as `nitipan who am-i`: `User label: <label>`, or `This node is not bound
to a user.` A script that parses the output as JSON fails.

## Cause

`internal/cli/who.go:91-94` registers `--json` on all three `who` subcommands in one loop. `runWhoAmI`, at
`internal/cli/who.go:301-328`, never reads the flag. `who holds` and `who is` do read it, and print schema 1.0 JSON.

## Repro

On a machine with a running node:

```
nitipan who am-i --json
```

Expected: a JSON object carrying the user label. Actual: the text line.

## Fix

Read the flag in `runWhoAmI`. Print the answer as JSON when it is set, with a `version` field like the other `who`
commands. A node that is not bound gets an empty or absent user label. `docs/reference/cli.md` and
`docs/reference/json-and-records.md` describe today's behavior, and change with the fix.

The other option is to stop registering `--json` on `am-i`. That breaks nothing that works today, but JSON is the
supported interface for scripts and guardians, so the JSON output is the better fix.
