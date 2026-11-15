# Installing nitipan

This page builds nitipan from source and puts it on your PATH. It takes about five minutes. nitipan runs on macOS.

## Before you start

You need:

- A Go toolchain at version 1.27.1 or later. `go.mod` names `go 1.27.1`. Check yours with `go version`.
- `git` and `make`.
- A clone of the nitipan repository.

## Steps

1. Build and install the binary from the root of the clone:

   ```
   make install
   ```

   The target builds nitipan with its version injected, then installs it to `~/.local/bin/nitipan`. Make sure
   `~/.local/bin` is on your PATH.

2. To install somewhere else, set `PREFIX` or `BINDIR`:

   ```
   make install PREFIX=/usr/local
   make install BINDIR=/opt/tools/bin
   ```

   `PREFIX` defaults to `~/.local`, and `BINDIR` defaults to `$(PREFIX)/bin`.

3. Check the install:

   ```
   nitipan --version
   ```

   You should see `nitipan version` followed by the output of `git describe` for your clone. A binary built with plain
   `go build` prints `nitipan version dev` instead.

4. Check that the shell finds the binary you just installed:

   ```
   command -v nitipan
   ```

   You should see the path under `BINDIR`, such as `/Users/alice/.local/bin/nitipan`.

## Why the install location must stay put

Several things nitipan sets up record the path of the binary:

- The git hooks from `nitipan setup git --install` and the LaunchAgents from `nitipan setup node` and
  `nitipan setup broker` record where `nitipan` sits on your PATH. They keep that path even when it is a symlink. A
  symlink-resolved path can name a versioned directory that an upgrade deletes.
- The Claude Code and Codex hooks that `nitipan setup` prints, and the opencode plugins it installs, record the path of
  the running binary with symlinks resolved.

`make install` writes a real file, not a symlink, so both kinds of path agree. Run every `nitipan setup` command from
the installed binary. Do not run it from `build/` or through `go run`, because the printed hooks would then name that
temporary path.

The git hook falls back to the `nitipan` that PATH finds when its recorded path is gone. With neither, it refuses
branch and HEAD updates, and its message says to reinstall nitipan.

## Upgrading

Upgrade by pulling the clone and running `make install` again, with the same `PREFIX` or `BINDIR` as before.

The install copies the new binary to a temporary file in `BINDIR` and then renames it over the old one. The rename is
atomic, so a git hook or a node that runs during the upgrade never sees a partial binary.

A running node watches its own executable. When the file changes, the node logs
`the executable changed. Exiting so launchd restarts it.` and exits. launchd starts it again on the new binary. You do
not need to restart it by hand.

## The state directory

nitipan keeps its leases in `~/.local/state/nitipan`.

- The home directory comes from the user database. nitipan ignores `HOME` and `XDG_STATE_HOME`. Two sessions that
  resolved the directory differently would scan two directories, and both of their claims would succeed.
- The directory must be on a local disk. Do not put your home directory on a network share.
- nitipan creates the directory with mode `0700` on first use. It refuses a directory that another user owns.
- A sandboxed agent needs write access to this directory. `nitipan setup <agent>` prints the exact allowance. See
  [Setting up your agents](agents.md).

## Next steps

- [Setting up your agents](agents.md).
- [Your first leases on one machine](local-leases.md).
- Read the [command reference](../reference/cli.md), or go back to the [README](../../README.md).
