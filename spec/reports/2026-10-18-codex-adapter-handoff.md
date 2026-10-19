# Codex Adapter Handoff

**Date:** 2026-10-18

This is a handoff for a running Codex CLI session. It asks that session to verify, by experiment, what a source reading
found. Add your results to "Findings from a live session" at the end. Do not edit the sections above it.

## Why this matters

nitipan is a lease and coordination tool for coding agent sessions. A session runs `nitipan acquire` from its shell to
claim a checkout. nitipan must then work out which agent session called it, and later whether that session is still
alive. NIT-001 builds a Codex adapter for this, and its milestone 4 needs live evidence from inside Codex.

nitipan finds the holder in three steps. It reads the agent's environment variables for a session ID. It walks up the
process tree from its parent until it reaches the agent's process, since Codex exports no PID variable. It then records
that process's PID and start time. Liveness later compares the start time again, through the macOS `kern.proc.pid`
sysctl or Linux `/proc/<pid>/stat`. It never uses `kill(pid, 0)`, which returns EPERM for a live process inside the
Claude Code sandbox.

Leases live under `~/.local/state/nitipan/`. Codex runs shell commands in its own sandbox, so that directory may need an
allowance. nitipan will print the needed entries but never edit Codex's config.

## What the source says

A research agent read `openai/codex` at tag `rust-v0.163.0`, commit 83086188. Paths are relative to `codex-rs/`. The
earlier wake handoff, `2026-10-18-codex-wake-handoff.md`, covers delivery. This one covers identity and liveness.

SOURCE marks a claim read from code, DOCUMENTED marks one from the docs, and UNVERIFIED marks one nobody has checked.

### Session identity

- Shell commands get `CODEX_THREAD_ID`, `CODEX_SESSION_ID` for the root session, and `CODEX_VERSION`. SOURCE:
  `protocol/src/shell_environment.rs:6-7,166-167`, `core/src/exec_env.rs:30-50`.
- `CODEX_THREAD_ID` survives an `include_only` environment policy. Whether `CODEX_SESSION_ID` does is UNVERIFIED. SOURCE
  for the first claim: the same files.
- A subagent thread gets its own `CODEX_THREAD_ID` and shares the root's `CODEX_SESSION_ID`. Inferred from "for the
  root session". UNVERIFIED.
- No variable names the Codex process's PID. SOURCE, a negative result.

### Process model

- The TUI runs an app-server embedded in its own process unless a daemon answers on
  `$CODEX_HOME/app-server-control/app-server-control.sock`. SOURCE: `tui/src/lib.rs:523-553,1027-1056`.
- With the embedded app-server, shell commands are children of the TUI process. UNVERIFIED.
- With a daemon, shell commands are likely children of the daemon, which serves several sessions. UNVERIFIED.

### MCP servers

Whether Codex passes `CODEX_THREAD_ID` or `CODEX_SESSION_ID` to the MCP servers it starts is UNVERIFIED.

## Experiments

Run these in order. Every step is read-only except step 5's test file, which you remove afterward. Do not edit
`~/.codex/config.toml` or any other config. Do not start or stop an app-server daemon. Do not kill or signal any process
except as step 3 says. Run each command on its own, and record its exact output and any error.

If `ps` is blocked by the sandbox, record the exact error. Then use step 4's script for start times, and say that `ps`
was unavailable.

1. Run `echo "$CODEX_THREAD_ID $CODEX_SESSION_ID $CODEX_VERSION"`, then `env | grep -E '^(CODEX|AGENT|AI_AGENT)'`. Two
   UUIDs, with the thread ID matching `/status`, confirm session identity. Redact any value whose name contains TOKEN,
   SECRET, or KEY.
2. Walk the process tree. Run `echo $$ $PPID`, then `ps -o pid,ppid,lstart,comm,args -p <pid>` for the parent, and
   repeat up to PID 1. Mark which process is Codex. Record its command name and full command line exactly. nitipan
   recognizes the agent by this signature, so note whether it is a native `codex` binary or a `node` wrapper.
3. Run `kill -0 <codex pid>; echo $?`. Record whether it succeeds or fails with EPERM. This confirms or refutes the
   reason nitipan avoids `kill`.
4. Run this script with the Codex PID as its argument. It reads the start time through the same sysctl nitipan uses.
   It prints a nonzero byte count and a start time for a live PID. On Linux, run `cat /proc/<pid>/stat` instead, and
   record field 22.

   ```sh
   python3 - <pid> <<'EOF'
   import ctypes, ctypes.util, struct, sys
   libc = ctypes.CDLL(ctypes.util.find_library("c"), use_errno=True)
   mib = (ctypes.c_int * 4)(1, 14, 1, int(sys.argv[1]))
   buf = ctypes.create_string_buffer(1024)
   n = ctypes.c_size_t(1024)
   rc = libc.sysctl(mib, 4, buf, ctypes.byref(n), None, ctypes.c_size_t(0))
   start = struct.unpack_from("<qi", buf.raw, 0) if n.value else None
   print("rc", rc, "errno", ctypes.get_errno(), "bytes", n.value, "start", start)
   EOF
   ```

   Then run `sh -c 'echo $$'`, and run the script again on the PID it printed. Zero bytes for that dead PID confirm that
   nitipan can tell dead from alive inside the sandbox.
5. Run `/status` and record the sandbox mode and approval policy. Then run
   `mkdir -p ~/.local/state/nitipan/handoff-test`, then `touch ~/.local/state/nitipan/handoff-test/codex`. A sandbox
   error confirms that an allowance is needed. Record the exact error, and whether Codex asked for approval. If the
   write succeeded, remove the file and the directory. From Codex's documentation, record which config entry would allow
   the write, such as a `sandbox_workspace_write.writable_roots` entry. Do not apply it.
6. Show the `shell_environment_policy` section of `~/.codex/config.toml`, or say that it has none. Record which policy is
   in effect, and which of the variables from step 1 it left.
7. Run `codex app-server daemon version`. It reports whether a daemon is running without starting one. Combined with
   step 2, this says whether shell commands are children of the TUI or of a daemon.
8. If you can spawn a subagent, have it run step 1 and report both variables. A different thread ID with the same
   session ID confirms the subagent claim. If you cannot spawn one, say so.
9. From Codex's documentation or source, find whether MCP servers inherit `CODEX_THREAD_ID` or `CODEX_SESSION_ID`.
   Mark the answer DOCUMENTED or SOURCE, with the location.
10. Record the sandbox modes and approval policies Codex offers, from `codex --help` or the docs. nitipan's
    `--permission-mode` flag will accept these values for Codex.

The next two need sessions the user starts. The user points each one at this file and names the step.

11. In this same TUI, start a new session with `/new`, and repeat steps 1 and 2. The same Codex PID with a new session ID
    shows that session IDs rotate inside one process.
12. In another terminal, the user starts session C with
    `codex -c 'shell_environment_policy.include_only=["PATH","HOME"]'`. That flag changes only session C, not the
    config file. In C, repeat step 1. Record which Codex variables survive.

## Findings from a live session

Record the Codex version, the OS, and the date. For each experiment, give its number, what you ran, what happened, and
whether it confirms or refutes the claim. Note anything the source reading got wrong.

### 2026-10-18: Codex 0.163.0, macOS arm64, workspace sandbox

Experiments were run through this session's shell tool, without escalation. No config was changed and no daemon
was started or stopped. `/status` and `/new` are interactive UI commands unavailable to this agent; they were not
run as shell commands. The initial claim that this was necessarily an API-hosted rather than TUI session was not
established: the shell environment identifies Codex, but the UI/process topology remains unverified.

`uname -a` (exit 0):

```text
Darwin Riptas-French-Toast-Pro.local 25.6.0 Darwin Kernel Version 25.6.0: Fri Jul 31 19:18:53 PDT 2026; root:xnu-12377.161.14~5/RELEASE_ARM64_T6031 arm64
```

#### 1. Root session identity — confirmed, except the /status comparison

`echo "$CODEX_THREAD_ID $CODEX_SESSION_ID $CODEX_VERSION"` (exit 0):

```text
01a15245-b963-7900-844f-cbbd4aaaa364 01a15245-b963-7900-844f-cbbd4aaaa364 0.163.0
```

To redact before displaying, the second command was:

```sh
env | grep -E '^(CODEX|AGENT|AI_AGENT)' | sed -E '/^[^=]*(TOKEN|SECRET|KEY)[^=]*=/s/=.*/=[REDACTED]/'
```

Output (exit 0; no names required redaction):

```text
CODEX_APPLY_PATCH_PRESERVE_LINE_ENDINGS=1
CODEX_CI=1
CODEX_PERMISSION_PROFILE=:workspace
CODEX_SANDBOX=seatbelt
CODEX_SANDBOX_NETWORK_DISABLED=1
CODEX_SESSION_ID=01a15245-b963-7900-844f-cbbd4aaaa364
CODEX_THREAD_ID=01a15245-b963-7900-844f-cbbd4aaaa364
CODEX_TOOL_CALL_ID=exec-90fc25d5-3ef4-46f0-a4ce-f29144fec286
CODEX_VERSION=0.163.0
```

Both IDs exist and match in the root thread. No exported PID variable appeared. `/status` comparison remains pending.

#### 2. Process ancestry — blocked

`echo $$ $PPID` (exit 0):

```text
93382 3067
```

`ps -o pid,ppid,lstart,comm,args -p 3067` (exit 127):

```text
zsh:1: operation not permitted: ps
```

`ps` was unavailable. PID 3067 is the shell's observed parent, **not a verified Codex PID**. Neither its command name
nor its full command line nor ancestry to PID 1 was established. Native binary versus node wrapper is unresolved.

#### 3. Signal-zero probe — permission failure for the live parent

`kill -0 3067; echo $?` (overall exit 0 because of the final echo):

```text
zsh:kill:1: kill 3067 failed: operation not permitted
1
```

This probe was performed after step 4's live/dead probes. It supports avoiding `kill(pid, 0)` for liveness in this
sandbox: the sysctl probe succeeded for the same PID. The Codex-specific identity remains unverified as above.

#### 4. Start-time sysctl — confirmed for the live parent and exited child

Ran the exact Python script in step 4 with argument `3067` (exit 0):

```text
rc 0 errno 0 bytes 648 start (1792103390, 449120)
```

`sh -c 'echo $$'` (exit 0):

```text
93838
```

Ran the same script with argument `93838` after that command exited (exit 0):

```text
rc 0 errno 0 bytes 0 start None
```

The live/dead distinction works inside this sandbox. This did not test PID reuse or identify the parent executable.

#### 5. Lease-directory write — allowance needed

`/status` was unavailable. Session instructions identify `workspace-write`, restricted network access, and writable
roots limited to the checkout and temporary directories. They permit approval requests, but do not expose a definitive
effective `approval_policy` value; it is not inferred from whether a command failed.

`mkdir -p ~/.local/state/nitipan/handoff-test` (exit 1):

```text
mkdir: /Users/ripta/.local/state/nitipan/handoff-test: Operation not permitted
```

`touch ~/.local/state/nitipan/handoff-test/codex` (exit 1):

```text
touch: /Users/ripta/.local/state/nitipan/handoff-test/codex: No such file or directory
```

No approval prompt occurred for these sandbox probes. Neither the test directory nor file was created, so there was
nothing to remove. The mkdir denial establishes the missing allowance; the touch error alone does not.

DOCUMENTED: [OpenAI configuration reference](https://learn.chatgpt.com/docs/config-file/config-reference) describes
additional writable roots for workspace-write. An example entry, **not applied**, is:

```toml
[sandbox_workspace_write]
writable_roots = ["/Users/ripta/.local/state/nitipan"]
```

#### 6. Shell environment policy — no section in user config

Read only the relevant section and top-level mode/policy lines using:

```sh
python3 - <<'EOF'
import pathlib, re
p = pathlib.Path.home() / '.codex/config.toml'
s = p.read_text()
m = re.search(r'^\[shell_environment_policy\]\s*\n(?:(?!^\[).*(?:\n|$))*', s, re.M)
print(m.group(0).rstrip() if m else 'No [shell_environment_policy] section in ~/.codex/config.toml')
for line in s.splitlines():
    if re.match(r'^(approval_policy|sandbox_mode|permissions)\s*=', line): print(line)
EOF
```

Output (exit 0):

```text
No [shell_environment_policy] section in ~/.codex/config.toml
```

SOURCE: at commit `83086188ae0614bd882818ff1aa23f3194962012`,
`codex-rs/protocol/src/config_types.rs:261-272` defaults to `inherit = all`,
`ignore_default_excludes = true`, empty exclude/set/include_only, and `use_profile = false`.
This is the source default, not a complete inspection of the session's merged config or launch arguments.
All variables listed in step 1 survived the actual policy. No include-only experiment was performed here.

#### 7. Daemon version — blocked, daemon status unresolved

`codex app-server daemon version` (exit 1):

```text
WARNING: proceeding, even though we could not create PATH aliases: Operation not permitted (os error 1)
Error: failed to connect to /Users/ripta/.codex/app-server-control/app-server-control.sock

Caused by:
    Operation not permitted (os error 1)
```

The socket permission error does **not** show that no daemon is running. Combined with blocked `ps`, it leaves
embedded versus shared-daemon topology unresolved. The CLI also attempted PATH-alias setup despite this being a
nominally read-only command; that attempt was denied.

#### 8. Subagent identity — confirmed

A subagent ran step 1. Its echo command returned (exit 0):

```text
01a15246-e0df-73c3-b3a9-5e38c6d16de7 01a15245-b963-7900-844f-cbbd4aaaa364 0.163.0
```

Its filtered environment returned (exit 0; no names required redaction):

```text
CODEX_APPLY_PATCH_PRESERVE_LINE_ENDINGS=1
CODEX_CI=1
CODEX_PERMISSION_PROFILE=:workspace
CODEX_SANDBOX=seatbelt
CODEX_SANDBOX_NETWORK_DISABLED=1
CODEX_SESSION_ID=01a15245-b963-7900-844f-cbbd4aaaa364
CODEX_THREAD_ID=01a15246-e0df-73c3-b3a9-5e38c6d16de7
CODEX_TOOL_CALL_ID=exec-2bb78677-4dcc-4f2e-b8f6-f346c5ddca1f
CODEX_VERSION=0.163.0
```

The thread ID changes and the root session ID stays the same.

#### 9. MCP environment — source evidence, no live MCP probe

Inspected an existing local source checkout; `git rev-parse HEAD` returned
`83086188ae0614bd882818ff1aa23f3194962012` (exit 0), matching the handoff's revision.

SOURCE: `codex-rs/rmcp-client/src/utils.rs:16-59,163-175` constructs the local stdio MCP environment from a default
allowlist, explicitly configured `env_vars`, certificate variables, and explicit `env` values. The Unix default
allowlist contains neither `CODEX_THREAD_ID` nor `CODEX_SESSION_ID`.
`codex-rs/codex-mcp/src/rmcp_client.rs:1220-1266` passes the configured environment into the launcher;
`codex-rs/rmcp-client/src/stdio_server_launcher.rs:276-283` applies the constructed map.
No identity-variable injection was found in `codex-mcp` or `rmcp-client`.

Thus the inspected code does not explicitly supply either identity to local stdio MCP by default. This source checkout
does not include the `codex_utils_pty::Command` implementation needed to independently verify whether its environment
map replaces or supplements inheritance; complete default-inheritance behavior remains unverified. Explicitly adding
an `env_vars` name also requires that variable to exist in the launching process, which shell-tool injection alone
does not establish. Remote stdio and HTTP MCP identity were not verified.

DOCUMENTED: the [OpenAI configuration reference](https://learn.chatgpt.com/docs/config-file/config-reference)
provides `mcp_servers.<id>.env` and `.env_vars` for configuring a stdio server's environment; it does not establish
automatic per-thread identity injection.

#### 10. Modes accepted by the installed CLI

`codex --help` exited 0. Its stderr was:

```text
WARNING: proceeding, even though we could not create PATH aliases: Operation not permitted (os error 1)
```

Relevant stdout excerpts (unrelated commands/options omitted):

```text
  -s, --sandbox <SANDBOX_MODE>
          Select the sandbox policy to use when executing model-generated shell commands
          
          [possible values: read-only, workspace-write, danger-full-access]

      --approve-for-me
          Route approval requests through automatic review using the workspace-write sandbox
```

```text
  -a, --ask-for-approval <APPROVAL_POLICY>
          Configure when the model requires human approval before executing a command

          Possible values:
          - on-request: The model decides when to ask the user for approval
          - never:      Never ask for user approval Execution failures are immediately returned to
            the model
```

These are separate dimensions. For this version, the advertised sandbox values are `read-only`, `workspace-write`,
and `danger-full-access`; advertised approval values are `on-request` and `never`. Do not assume older approval values
such as `untrusted` or `on-failure` are offered by this CLI. The automatic-review flag is separate from the enum above.

#### 11–12. User-started sessions — pending

Neither `/new` nor a separate include-only session was started. These still need the user-started sessions described
above. This run does not establish ID rotation within one process or live survival of the IDs under include-only.

The strongest live findings are the shared root identity across subagents, working sysctl liveness despite signal-zero
denial, and the missing state-directory write allowance. Process recognition and shared-daemon liveness semantics
still need evidence before milestone 4 can be considered fully verified.

### 2026-10-18: Step 12, session C — Codex 0.163.0, macOS arm64

The user directed this session to run step 12. This treats the current session as session C launched with
`codex -c 'shell_environment_policy.include_only=["PATH","HOME"]'`, as specified above; the launch arguments and
merged configuration were not independently inspected. No configuration was changed. The earlier pending status
for step 12 is superseded by this result, subject to that launch assumption.

`echo "$CODEX_THREAD_ID $CODEX_SESSION_ID $CODEX_VERSION"` (exit 0):

```text
01a15251-52ea-7572-bb0c-afc1f89828e9 01a15251-52ea-7572-bb0c-afc1f89828e9 0.163.0
```

The second step 1 command included redaction before displaying output:

```sh
env | grep -E '^(CODEX|AGENT|AI_AGENT)' | sed -E '/^[^=]*(TOKEN|SECRET|KEY)[^=]*=/s/=.*/=[REDACTED]/'
```

Output (exit 0; no names required redaction):

```text
CODEX_APPLY_PATCH_PRESERVE_LINE_ENDINGS=1
CODEX_CI=1
CODEX_PERMISSION_PROFILE=:workspace
CODEX_SANDBOX=seatbelt
CODEX_SANDBOX_NETWORK_DISABLED=1
CODEX_SESSION_ID=01a15251-52ea-7572-bb0c-afc1f89828e9
CODEX_THREAD_ID=01a15251-52ea-7572-bb0c-afc1f89828e9
CODEX_TOOL_CALL_ID=exec-142527fc-e583-4033-8077-8ada22d2fba1
CODEX_VERSION=0.163.0
```

`uname -a` (exit 0):

```text
Darwin Riptas-French-Toast-Pro.local 25.6.0 Darwin Kernel Version 25.6.0: Fri Jul 31 19:18:53 PDT 2026; root:xnu-12377.161.14~5/RELEASE_ARM64_T6031 arm64
```

Both identity variables and `CODEX_VERSION` survived, along with the other Codex variables listed above. Assuming
the specified session C launch, this confirms the source claim for `CODEX_THREAD_ID` and resolves the previously
unverified survival of `CODEX_SESSION_ID` under this include-only policy. Both IDs match in this root thread.
The interactive `/status` comparison was unavailable to the agent and was not performed. These commands produced
no errors or approval prompts.

### 2026-10-18: Step 11 follow-up, Codex 0.163.0, macOS arm64

The user directed this session to run step 11. Repeated steps 1 and 2 without escalation, and used step 4's
sysctl fallback when `ps` was blocked. The agent did not invoke `/new` or independently observe the UI transition.
No config was changed and no daemon was started or stopped.

`echo "$CODEX_THREAD_ID $CODEX_SESSION_ID $CODEX_VERSION"` (exit 0):

```text
01a15251-917e-77f0-8150-63123c454cfa 01a15251-917e-77f0-8150-63123c454cfa 0.163.0
```

`env | grep -E '^(CODEX|AGENT|AI_AGENT)' | sed -E '/^[^=]*(TOKEN|SECRET|KEY)[^=]*=/s/=.*/=[REDACTED]/'`
(exit 0; no names required redaction):

```text
CODEX_APPLY_PATCH_PRESERVE_LINE_ENDINGS=1
CODEX_CI=1
CODEX_PERMISSION_PROFILE=:workspace
CODEX_SANDBOX=seatbelt
CODEX_SANDBOX_NETWORK_DISABLED=1
CODEX_SESSION_ID=01a15251-917e-77f0-8150-63123c454cfa
CODEX_THREAD_ID=01a15251-917e-77f0-8150-63123c454cfa
CODEX_TOOL_CALL_ID=exec-bac02583-202a-4c3a-b7fa-20ae3d30fe4c
CODEX_VERSION=0.163.0
```

`echo $$ $PPID` (exit 0):

```text
14160 3067
```

`ps -o pid,ppid,lstart,comm,args -p 3067` (exit 127):

```text
zsh:1: operation not permitted: ps
```

`ps` was unavailable, so the ancestry walk could not continue. Ran the exact Python script from step 4 with
argument `3067` (exit 0):

```text
rc 0 errno 0 bytes 648 start (1792103390, 449120)
```

**Finding: session identity rotation under the same live parent is confirmed.** Both IDs changed from
`01a15245-b963-7900-844f-cbbd4aaaa364` to `01a15251-917e-77f0-8150-63123c454cfa`. Parent PID `3067` and start time
`(1792103390, 449120)` match the earlier run exactly, establishing the same parent process rather than merely a
reused PID. Its executable, command line, and TUI-versus-daemon role remain unverified because `ps` was blocked.
Thus the narrower observed result supports step 11's claim, but does not independently establish the same-TUI
`/new` mechanism or identify PID 3067 as Codex. Step 12's results are recorded in the session C section above.
