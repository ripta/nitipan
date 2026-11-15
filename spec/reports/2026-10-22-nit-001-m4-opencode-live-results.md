# NIT-001 M4: opencode Live Results

- **opencode:** `opencode --version` → `opencode v2.1.0` (Homebrew, `/opt/homebrew/Cellar/opencode/2.1.0/bin/opencode`)
- **macOS:** `sw_vers` → ProductVersion 26.6.2, BuildVersion 25G83
- **Date:** 2026-10-22 (local, PDT)
- **How the plugins loaded:** autoloaded from `~/.config/opencode/plugins/nitipan/` with **no config entry**. Neither
  `~/.config/opencode/opencode.json` nor `~/.config/opencode/cli.json` mentions plugins (grep for `plugin` finds
  nothing; `opencode debug config` shows only `$schema` and `skills`). The server plugin (`index.ts`) loads in the
  service; the TUI plugin (`tui.ts`, via `package.json` `exports["./tui"]`) loads in the TUI. No `plugins` key, path
  form, or second restart was needed.
- **Handoff session:** `ses_e98f0b1feffeG5l2IOcxOonSAo`, TUI PID 30555 (started 21:39:11, after the service restart),
  service PID 2163 `opencode serve --service`, parent PID 1.

Scratch directories: `$TMPDIR/opencode/nit-m4/{scratch1,scratch2,scratch11,scratch12,mcp4}`, where
`$TMPDIR/opencode` = `/private/var/folders/k2/l3f2x3cn67n_qz_805l81zph0000gn/T/opencode`.

**How headless runs were done:** steps marked USER that only needed "a session with no inherited opencode
environment" were run from this session's shell with the variables stripped:
`env -u OPENCODE_SESSION_ID -u OPENCODE -u AGENT -u AI_AGENT -u OPENCODE_TERMINAL opencode run ...`. They connect to
the shared service like a terminal would.

## Leases found at the start

```
$ /Users/ripta/projects/nitipan/build/nitipan list
no leases
```

## Step 0. Install (USER, done before this session)

**PASS.** Reported by the user: `nitipan setup opencode --install-plugins ~/.config/opencode/plugins` wrote
`nitipan/index.ts`, `nitipan/package.json`, `nitipan/tui.ts`. `opencode service restart` was run; the new service is PID
2163. Before the restart, `opencode plugin list` showed only `nitipan.server`.

```
$ ls -la ~/.config/opencode/plugins/nitipan
-rw-r--r--  index.ts      3238  Oct 22 21:32
-rw-r--r--  package.json   161  Oct 22 21:32
-rw-r--r--  tui.ts        5354  Oct 22 21:32

$ opencode plugin list
ID              VERSION  SOURCE
nitipan.server  local    /Users/ripta/.config/opencode/plugins/nitipan/index.ts
```

`opencode plugin list` lists only the server plugin even though the TUI plugin is loaded (see step 13). It does not
list TUI plugins.

## Step 1. Session ID and PID from a bash tool call

**PASS.**

```
$ echo "$OPENCODE_SESSION_ID $OPENCODE $AGENT $AI_AGENT $OPENCODE_TERMINAL"
ses_e98f0b1feffeG5l2IOcxOonSAo 1 1 opencode 1

$ nitipan acquire --checkout $TMPDIR/opencode/nit-m4/scratch1 --description "m4 live check"
acquired
01M5KGZFPDK3C9JNXW1F4BMMKH  alive
  holder: opencode session ses_e98f0b1feffeG5l2IOcxOonSAo, pid 2163
  description: m4 live check
```

`list --json`: `holder.agent` = `opencode`, `holder.session_id` = `ses_e98f0b1feffeG5l2IOcxOonSAo` (equals
`OPENCODE_SESSION_ID`), `holder.pid` = 2163, `holder.start.sec` = 1792730030, `liveness` =
`{"status":"alive","reason":"presence mark from TUI 30555"}`.

```
$ ps -o pid,ppid,lstart,command -p 30555,2163
  PID  PPID STARTED                      COMMAND
 2163     1 Thu Oct 22 21:33:50 2026     /opt/homebrew/Cellar/opencode/2.1.0/bin/opencode serve --service
30555 78272 Thu Oct 22 21:39:11 2026     opencode
```

The holder is the **shared service** (`opencode serve --service`). Its start time matches `holder.start.sec`
(1792730030 = 21:33:50 PDT). The `ps` was run from the tool shell, not a separate terminal; it reads the same process
table.

## Step 2. No sandbox, the sysctl, and the allowance

1. **PASS.** `acquire` and `list` work in the tool, and `liveness` is `alive`.
2. **PASS.** opencode asked for no permission at any step. The `build` agent's rules are `* * allow` (from
   `opencode debug agents`). The `shell` and `external_directory` rules from step 0.3 were not needed and were not added.
3. **FAIL.** A session with no TUI acquired a lease while a TUI was connected to the shared service.

```
$ env -u OPENCODE_SESSION_ID -u OPENCODE -u AGENT -u AI_AGENT -u OPENCODE_TERMINAL \
    opencode run --title "nit-m4 step2.3 no tui" "Run exactly this shell command once ...: \
    /Users/ripta/projects/nitipan/build/nitipan acquire --checkout $TMPDIR/opencode/nit-m4/scratch2"
$ /Users/ripta/projects/nitipan/build/nitipan acquire --checkout .../nit-m4/scratch2
acquired
01M5KH95D6TH8HAA18K2V6D25K  alive
  holder: opencode session ses_e98ecc711ffeY8d75yuKF7bF9u, pid 2163
```

Cause: the installed `tui.ts` counts a session as open when it is running anywhere on the service. `open()` adds every
session from `ctx.data.session.list()` whose status is `running` (tui.ts:91-93) and every session named by a
`session.execution.started` event (tui.ts:132-137). A TUI on the shared service sees every session's status and
events, so TUI 30555 marked the headless `opencode run` session as present while its turn ran. Step 8 shows that mark
directly. The acquire's presence check then passed.

After the turn ended, the lease read `dead`
(`liveness: no presence mark from a live TUI and no execution mark`). `release` from this session was refused, which is
correct (`lease ... is not yours ... exit 6`). It was released from its own session with
`opencode run --session ses_e98ecc711ffeY8d75yuKF7bF9u "... nitipan release 01M5KH95D6TH8HAA18K2V6D25K"` →
`released 01M5KH95D6TH8HAA18K2V6D25K`.

Not tested: the same run with no TUI connected to the service. The design probably meets the check then, but the
check as written fails whenever any TUI is open.

## Step 3. One session or many per process

**PASS. opencode hosts many sessions per process.**

A first attempt, in a tab of TUI 30555, created `scratch3` but left no lease, and its terminal was closed before the
output could be read. Its result is unknown.

The second attempt used a second TUI window, started from a new terminal:

```
$ opencode --prompt 'Run these two shell commands separately and show their output verbatim: echo "$OPENCODE_SESSION_ID" and then /Users/ripta/projects/nitipan/build/nitipan acquire --checkout .../nit-m4/scratch3 --description "m4 step 3"' .../nit-m4/scratch3
(agent reported session ses_e98e0641cffe5XbyJQzR4gxKCS)
```

`list --json` from the handoff session:

| lease | holder.session_id | holder.pid | liveness |
|---|---|---|---|
| `01M5KGZFPDK3C9JNXW1F4BMMKH` (scratch1) | `ses_e98f0b1feffeG5l2IOcxOonSAo` | 2163 | alive, presence mark from TUI 30555 |
| `01M5KHZA2WH2YZTJMVKWY6H0KQ` (scratch3) | `ses_e98e0641cffe5XbyJQzR4gxKCS` | 2163 | alive, presence mark from TUI 36442 |

```
$ pgrep -alf opencode
2163 /opt/homebrew/Cellar/opencode/2.1.0/bin/opencode serve --service
30555 opencode
36442 opencode --prompt Run these two shell commands ...
```

Same `holder.pid` and `holder.start` (2163, 1792730030), different session IDs, two TUIs. Earlier sessions in this
run add to it: the headless `ses_e98ecc71…` and the subagent `ses_e98e9586…` were also held by PID 2163.

Also seen: as soon as TUI 36442 started, it wrote
`opencode.ses_e98f0b1feffeG5l2IOcxOonSAo.36442.1792731486-992335.json`, a presence mark for the **handoff** session,
which has no tab or route in that TUI. It did so only because that session was running. This is the same cause as the
step 2.3 failure. TUI 36442 did not mark TUI 30555's idle tab sessions (`ses_eada…`), so the tab list is per TUI.

## Step 4. Do MCP servers get the session variable

**PASS — OBSERVED: no.** opencode does not pass `OPENCODE_SESSION_ID` (or any `OPENCODE*`/`AGENT` variable) to a local
stdio MCP server.

Method: a project-local `opencode.json` in `$TMPDIR/opencode/nit-m4/mcp4` (not `~/.config/opencode`) configured
`mcp.envdump = {type: "local", command: [".../mcp4/envdump.sh"]}`. The script writes `env` to a file and exits 1. It
ran under a standalone server so the shared service never loaded it, with the inherited variables stripped:

```
$ env -u OPENCODE_SESSION_ID -u OPENCODE -u AGENT -u AI_AGENT -u OPENCODE_TERMINAL \
    opencode run --standalone --format json --title "nit-m4 mcp env test" "Reply with the single word hi. ..."
(session ses_e98ef33beffez2jc2fmAF576BR, replied "hi")

$ grep -i "opencode\|session\|agent" .../mcp4/env.45828.txt
SSH_AGENT_PID=1455
FPATH=...
SSH_AUTH_SOCK=...
PWD=/private/var/folders/.../nit-m4/mcp4
ATUIN_SESSION=...
SECURITYSESSIONID=186b0
```

The MCP server is started per instance, not per session, so it has no session to name. The test config was removed
(`rm .../mcp4/opencode.json`).

## Step 5. Presence marks follow tabs, route, and running state

**PASS** (route-only case not possible in v2.1.0).

Actions were done in the second TUI window, PID 36442, started 1792731486 (see step 3). Each `ls` ran 5 s after the
action.

- **Open a new tab on a new session: PASS, with a note.** A new, empty tab creates no session: no new presence file,
  and no new row in `session_v2`. opencode v2.1.0 creates the session on the first message. After `say hi` was sent in
  the tab, `opencode.ses_e98dea37fffewkvwcTq3Z2JhYV.36442.1792731486-992335.json` appeared (22:00, session "Friendly
  Greeting"). It stayed after the turn ended, so the tab held it. TUI 30555 did not mark it.
- **Route without a tab: NOT POSSIBLE.** The v2.1.0 TUI opens every session in a tab; there is no way to reach a
  session's route without one (user). The route read is never the only thing holding a session.
- **Close the tab: PASS.** After the "Friendly Greeting" tab was closed, its presence file was gone at the next `ls`.
  The other tab's file (`ses_e98e0641….36442…`) stayed. No new `plugin.log` lines.
- **Long turn, tab closed while it runs: PASS.** Switching tabs tests nothing in v2.1.0, because the tab still holds
  the session. So the tab was closed mid-turn instead. A new tab was asked to `sleep 90` (session
  `ses_e98dad97fffe5Me7aMcgwlaAP6`, created 1792731850 = 22:04:10), then closed while it ran. A 10 s loop of
  `ls presence executing | grep e98dad97`:

  ```
  22:04:33 … 22:05:33   opencode.ses_e98dad97….2163.1792730030-983643.json     (executing)
                        opencode.ses_e98dad97….30555.1792730351-745038.json    (presence, TUI 30555)
                        opencode.ses_e98dad97….36442.1792731486-992335.json    (presence, TUI 36442)
  22:05:43 … 22:06:23   (none)
  ```

  The running session kept its marks while it ran and lost all three within 10 s of the turn ending. **TUI 30555,
  which never had a tab for this session, also marked it while it ran.** That is the cross-TUI marking behind the
  step 2.3 failure.
- **Tab-list and route reads: PASS.** The plugin sees tab changes through the 2 s poll: opening a tab (once it has a
  session) and closing one each showed up within one 5 s wait. No read threw: `plugin.log` holds no `read failed`
  line.
- **`tui-pid` is the TUI:** files carry 30555 and 36442, and `ps` shows both as `opencode` TUI processes (36442's
  command is the `opencode --prompt …` from step 3). Neither is the service 2163.

Earlier observations:

- The TUI plugin loaded. At the start, `sessions/presence` held 7 files under TUI 30555 (this session plus 6 older
  sessions, presumably tabs). PID 30555 is the TUI (`ps`: `opencode`, started 21:39:11; the file's `start.sec`
  1792730351 = 21:39:11 PDT).
- Running state: a headless session running on the service got a presence file from TUI 30555 while it ran, and lost
  it within the poll once its turn ended (see step 8). The plugin adds running sessions **whether or not this TUI has a
  tab or route for them**. That is the cause of the step 2.3 failure.
- Reads: no `read failed` lines. `plugin.log` did not exist until 04:42, and its only line is the server-plugin line in
  step 10.

## Step 6. Hot reload keeps the marks

**PASS.**

Deviation: at the user's direction, the handoff session made the edit itself, though the handoff says not to edit
`~/.config/opencode`. Only the comment on line 1 changed:

```
$ sed -i '' '1s/$/ reload/' ~/.config/opencode/plugins/nitipan/tui.ts      (22:15:53)
```

Two marked sessions held leases: `ses_e98f0b1f…` (scratch1 lease `01M5KGZF…`, tab in TUI 30555) and `ses_e98e0641…`
(scratch3 lease `01M5KHZA…`, tab in TUI 36442). A watcher sampled every ~2 s from 22:10:25 to 22:16:13, about 173
samples. Each sample ran `ls presence | grep -E "e98e0641|e98f0b1f"` and `nitipan list | grep ^01M`.

```
$ grep -v "^==" step6b.log | sort | uniq -c
 173 01M5KGZFPDK3C9JNXW1F4BMMKH  alive
 173 01M5KHZA2WH2YZTJMVKWY6H0KQ  alive
   2 01M5KJSQ8NB5FG9R52ZCX2F70D  alive      <- another session's lease, not this run's (see below)
 173 opencode.ses_e98e0641….36442.1792731486-992335.json
 173 opencode.ses_e98f0b1f….30555.1792730351-745038.json
  29 opencode.ses_e98f0b1f….36442.1792731486-992335.json
```

Both presence files were present and both leases read `alive` in every sample, including the 20 s after the edit. At
22:15:53, the second of the edit, **both** TUIs (30555 and 36442) rewrote every one of their presence files: the
reload re-ran the plugin's setup, which republished its marks, and no cleanup removed any. Ten seconds later all files
were still present. No `plugin.log` lines. The opencode log has no line naming `tui.ts` or a reload.

The 29-count file is TUI 36442's mark for the running handoff session. It comes and goes with whether this session's
turn is running, and is unrelated to the reload.

Restore:

```
$ nitipan setup opencode --install-plugins ~/.config/opencode/plugins --replace
unchanged /Users/ripta/.config/opencode/plugins/nitipan/index.ts
unchanged /Users/ripta/.config/opencode/plugins/nitipan/package.json
replaced /Users/ripta/.config/opencode/plugins/nitipan/tui.ts

Restart the opencode service and each TUI so they load the plugins.
nitipan setup opencode prints the rest of the steps.
$ head -1 ~/.config/opencode/plugins/nitipan/tui.ts
// nitipan TUI plugin for opencode.
```

The restore caused a second hot reload at 22:16:42. Every presence file was republished again, and both leases still
read `alive` afterwards.

Another session's lease, `01M5KJSQ8NB5FG9R52ZCX2F70D`, appeared alive in two samples around 22:12 and was gone by
22:16. It was not created by this run and was left alone.

## Step 7. Shared service and `--standalone`

1. **PASS.** Shared-service presence file `opencode.ses_e98f0b1f….30555.1792730351-745038.json` has `pid` 30555 (the
   TUI), not 2163 (the service).
2. **PASS.** The second TUI window was quit, and in the same terminal:

   ```
   $ opencode --standalone --prompt 'Run these shell commands one at a time ...: echo "$OPENCODE_SESSION_ID", then
       /Users/ripta/projects/nitipan/build/nitipan acquire --checkout .../nit-m4/scratch7 --description "m4 step 7
       standalone", then sleep 60.' .../nit-m4/scratch7

   $ ps -o pid,ppid,lstart,command -A -ww | grep -i "[o]pencode"
    2163     1 Thu Oct 22 21:33:50 2026  /opt/homebrew/Cellar/opencode/2.1.0/bin/opencode serve --service
   56109 56100 Thu Oct 22 22:18:34 2026  /opt/homebrew/Cellar/opencode/2.1.0/bin/opencode serve --stdio --port 0
   56100 24969 Thu Oct 22 22:18:34 2026  opencode --standalone --prompt Run these shell commands ...
   30555 78272 Thu Oct 22 21:39:11 2026  opencode
   ```

   In `list --json`, lease `01M5KK4TAVWKGPTDCKTFDNVE3R` (scratch7) has `holder.session_id` =
   `ses_e98cda7fdffeOSprdhGZ9874gZ`, **`holder.pid` = 56109** (the private `opencode serve --stdio` child),
   `holder.start.sec` = 1792732714, and `liveness` = `alive, presence mark from TUI 56100`.
3. **PASS.** The presence file is `opencode.ses_e98cda7f….56100.1792732714-543622.json`. Its `tui-pid` is **56100**, the
   standalone TUI; the serve child is **56109**. TUI 30555 did not mark this session, because it is on a different
   server.
4. **PASS.** While the `sleep 60` turn ran, `sessions/executing` held
   `opencode.ses_e98cda7f….56109.1792732714-614722.json`, with the **serve child's** PID in the name. An 8 s loop
   showed it present through 22:19:39 and gone at 22:19:47, once the turn ended. The presence file stayed, because the
   tab is open.

Earlier data point (step 4): the `opencode run --standalone` session wrote its execution mark under its private child
too (PID 45805), not under 2163.

Also seen: when TUI 36442 was quit, its presence files (`ses_e98e0641….36442…` and `ses_e98f0b1f….36442…`) were gone
by the next `ls`. `tui.ts` says cleanup never removes marks, so something else removed them. It was either a clean
TUI exit path or nitipan pruning marks of dead TUIs; this run cannot tell which.

Releasing the scratch7 lease: it is held by the standalone session, and its private server cannot be reached from
the handoff session. So it was broken, which is allowed for a lease this run created:

```
$ nitipan break --force 01M5KK4TAVWKGPTDCKTFDNVE3R
broken lease 01M5KK4TAVWKGPTDCKTFDNVE3R; a tombstone waits for its holder
```

This left `tombstones/01M5KKBDBFTTEYTHXK2JABGR89.json`, addressed to `ses_e98cda7f…` / PID 56109. **`list --json`
reports `"tombstones": []` while that file exists.** The standalone TUI was then quit, so the holder will never make
another call and collect the tombstone. `nitipan break 01M5KKBDBFTTEYTHXK2JABGR89` refuses:
`... is a tombstone, not a lease. The broken holder receives it on its next call` (exit 8). There is no command to
clear a tombstone whose holder is dead.

Before the second window was quit, the scratch3 lease was released from its own session:
`opencode run --session ses_e98e0641cffe5XbyJQzR4gxKCS "... nitipan release 01M5KHZA2WH2YZTJMVKWY6H0KQ"` →
`released 01M5KHZA2WH2YZTJMVKWY6H0KQ`.

## Step 8. The server plugin sees execution events

1. **PASS.** A headless `opencode run` on the shared service (`ses_e98ee7b2affevjP5Mc6oicVvpt`, asked to run
   `sleep 20`). About 10 s in:

```
$ ls -l sessions/executing sessions/presence
executing: opencode.ses_e98ee7b2affevjP5Mc6oicVvpt.2163.1792730030-983643.json   (21:42)
presence:  opencode.ses_e98ee7b2affevjP5Mc6oicVvpt.30555.1792730351-745038.json  (21:42)
```

   After the run finished (last event `text "done"` at 1792730642), both files were gone at the next `ls`. The handoff
   session's own execution mark (`….2163….json`) has been present for this whole turn.
2. Event names are not logged. The file appeared on execution start and disappeared on execution end.
3. **Turn resumed: PASS. Execution mark under the new service PID: FAIL.**

   This step and step 9.4 were run together by a detached script, `nit-m4/step89.py`. It used `setsid` so the restart
   could not kill it, and logged to `nit-m4/step89.log`. The script:
   - started a headless `opencode run` in `nit-m4/scratch8` that acquires a lease, then runs `sleep 120`;
   - ran `opencode service restart` mid-turn;
   - polled every 5 s: the service PID, `session_v2` for the session, `list --json` liveness, and the session's marks.

   ```
   [22:34:07] lease 01M5KM13Y995TDJ0J12BWM0JP0 session ses_e98bf860effeR4iEI6F6fsBvQF holder pid 2163
              alive (presence mark from TUI 30555)
              files: presence/…e98bf860….30555…, executing/…e98bf860….2163.1792730030-983643.json
   [22:35:16] service pid before restart: 2163
   [22:35:16] $ opencode service restart -> rc 0: http://127.0.0.1:49374
   [22:35:16 … 22:37:33] svc='50404'  executing/…e98bf860….2163.1792730030-983643.json   (old PID, never rewritten)
   [22:37:38 … 22:37:58] no files; lease dead
   ```

   `session_message` for the session shows the resume:

   ```
   15 assistant  created 1792733646852  completed 1792733716580   (the turn running `sleep 120`, cut by the restart)
   27 synthetic  created 1792733716718  {"notice":"restart"} "The server restarted while you were working. Continue
                                        from where you left off without repeating completed work."
   29 assistant  created 1792733717132  completed 1792733852834   (re-ran `sleep 120`)
   40 assistant  … "done"
   47 idle       created 1792733853763  outcome "succeeded"
   ```

   The turn resumed on the new service, 50404. The headless `opencode run` client did not survive the restart: it
   exited 1 with `Transport: ECONNRESET`. **The server plugin did not write an execution mark for the resumed turn
   under 50404.** The mark under the dead 2163 stayed for the whole resumed turn and was removed only when the turn
   ended. The resumed turn apparently raises no `session.execution.started` the plugin handles. New turns after the
   restart do get marks under 50404 (the handoff session's mark is `…e98f0b1f….50404.1792733716-602188.json`).
   Without TUI 30555's cross-marking, the resumed session would have had only a mark naming a dead PID.

   Also seen:
   - The new service `50404 opencode serve --service` has **PPID 30555, the handoff TUI**. 2163's parent was 1. No
     LaunchAgent for opencode exists (`~/Library/LaunchAgents/*opencode*` matches nothing). The service is not under
     launchd, and the restarted one now dies with this TUI.
   - The handoff session's own lease (`01M5KGZF…`) still names `holder.pid` 2163, which no longer exists. It reads
     `alive` only because of TUI 30555's presence mark. The lease record is not updated to the new service PID.

Also seen (standalone): the `opencode run --standalone` session left a **stale execution mark**,
`opencode.ses_e98ef33beffez2jc2fmAF576BR.45805.1792730516-357507.json`. PID 45805 is gone (`ps -p 45805` prints
nothing). `plugin.log` holds `2026-10-23T04:42:04.563Z server execution exited null:`. The server plugin's `hook
execution` child was killed by a signal as the private server shut down, so the mark-off never landed. nitipan should
read it dead because the PID is gone. The file was left in place.

## Step 9. A session whose TUI died mid-turn

1. and 2. A second shared-service TUI was started by the user from a new terminal:

   ```
   $ mkdir -p .../nit-m4/scratch9 && opencode --prompt 'Run these shell commands one at a time ...:
       /Users/ripta/projects/nitipan/build/nitipan acquire --checkout .../nit-m4/scratch9 --description "m4 step 9",
       then sleep 150.' .../nit-m4/scratch9
   ```

   Session S = `ses_e98c4e328ffesBTYMfSB1NkyDr`, lease `01M5KKPA2NJDWSP5RF71RNABW3` (seen 22:28:26), TUI PID 15875
   (`pgrep -alf "opencode --prompt"`). Before the kill, S had presence files from **both** TUIs (15875 and 30555; 30555
   because S was running) and an execution mark under 2163. The handoff session killed the second TUI, never its own:

   ```
   $ kill -9 15875          (22:28:37)
   ```

3. **PASS, with a caveat.** `list --json` was polled every 10 s, printing S's liveness and its files:

   ```
   22:28:41 … 22:30:42   alive | presence mark from TUI 30555
                         files: …e98c4e32….2163… (executing), …e98c4e32….30555… (presence)
   22:30:52 … 22:32:13   dead  | no presence mark from a live TUI and no execution mark
                         files: none
   ```

   S read `alive` for the whole turn after its TUI died, and flipped to `dead` within 10 s of the turn ending
   (`sleep 150` from about 22:28:20). The caveat: the reason was never the execution mark. It was **TUI 30555's
   presence mark**, which that TUI writes for any running session on the service (the step 2.3 cause). The
   execution-mark path ("alive by execution mark under a live service") was therefore not exercised. To exercise it,
   no other TUI may be on the service. The killed TUI 15875's own presence file was gone by the first poll after the
   kill.

   Cleanup: `nitipan break 01M5KKPA2NJDWSP5RF71RNABW3` → `cleared lease 01M5KKPA2NJDWSP5RF71RNABW3 of a dead holder`.

   Also seen: by 22:32 the step 7 tombstone `01M5KKBDBFTTEYTHXK2JABGR89` had gone from `tombstones/`. nitipan prunes a
   tombstone whose holder is dead, even though `break` refuses to remove it.

4. **FAIL: `unknown` was never shown, and `time_suspended` does not mean what the design assumes.** This ran in the
   same script as step 8.3. The headless session stands in for S, since a session with no TUI is what S becomes after
   its TUI dies.

   ```
   $ sqlite3 -readonly ~/.local/share/opencode/opencode.db "select id, time_suspended, time_updated from session_v2 where id='<S>'"
   [22:35:16 … 22:37:33] ses_e98bf860effeR4iEI6F6fsBvQF|1792733641256|…    liveness alive (presence mark from TUI 30555)
   [22:37:38 … 22:37:58] ses_e98bf860effeR4iEI6F6fsBvQF||1792733853761     liveness dead
   ```

   - `time_suspended` was 1792733641256 = 22:34:01.256, **the turn's start**, not the restart time (22:35:16). It was
     cleared when the turn went idle.
   - **The handoff session, never restarted, has `time_suspended` set while its turn runs:**
     `ses_e98f0b1feffeG5l2IOcxOonSAo|1792734099421|resume_attempts 0`. In v2.1.0 the column marks every turn in flight
     so it can be resumed after a restart. It does not mark only suspended turns.
   - While it was set, `list --json` showed S as `alive` from TUI 30555's presence mark, never `unknown`. So the
     `time_suspended` path was not exercised. If nitipan reads `time_suspended` as "suspended", every running session
     with no live mark would read `unknown`.
   - After the turn resumed and ended, S read `dead` within 5 s. The script then ran
     `nitipan break 01M5KM13Y995TDJ0J12BWM0JP0` → `cleared lease 01M5KM13Y995TDJ0J12BWM0JP0 of a dead holder`.

5. **NOT RUN.** The step needs every opencode TUI stopped, including the handoff session's, before
   `chmod 000 ~/.local/share/opencode/opencode.db`. The user declined to continue the run. Running it with the service
   up would have broken the handoff's instruction, so it was not done. `opencode.db`'s mode was never changed.

   Another session's leases appeared during this step: `01M5KKN3ECF8AZ5TGAMS9FCENJ` and `01M5KKN8VTPQNB7B08GV5Q7PDD`,
   codex sessions `01a1673a-…`, pid 87537, descriptions `m4-auto-a` and `m4-auto-b`. They were left alone.

## Step 10. TUI signature and `opencode run` / `opencode acp`

1. **`opencode run` on the shared service: FAIL.** `opencode run` does not load the TUI plugin, but a presence file for
   its session appeared anyway, written by the unrelated TUI 30555 (step 8). With `--standalone`, no presence file
   appeared, because no TUI watches that private server.

   **`opencode acp`: PASS.** It was driven from this session's shell by a small JSON-RPC client
   (`$TMPDIR/opencode/nit-m4/acp_client.py`), with the inherited variables stripped, in `nit-m4/scratch10`. The client
   sent `initialize`, then `session/new`, then `session/prompt` ("Run the shell command 'sleep 20' ..."), and snapshot
   `presence` and `executing` every 5 s.

   ```
   acp pid 92507
   initialize: {"result": {"protocolVersion": 1, ..., "agentInfo": {"name": "OpenCode", "version": "2.1.0"}}}
   session/new: {"result": {"sessionId": "ses_e98c9959dffeYmxj1lxoYmEKx9", ... "mode" options: build, plan}}
   [22:23:01] during turn: executing = [ses_e98f0b1f….2163…]                        (no acp mark yet)
   [22:23:06 … 22:24:51] during turn: executing += opencode.ses_e98c9959….92508.1792732981-460484.json
                         presence: no file for ses_e98c9959 in any sample
   session/prompt: {"error": {"code": -32603, "message": "Internal error: Upstream request failed: Endpoint is unavailable.", ...}}
   [22:24:58] after turn: no ses_e98c9959 file in presence or executing
   [22:25:01] after acp exit: same
   ```

   `opencode acp` never produced a presence file. It runs on its **own private server child**: the mark's PID 92508 is
   not the service 2163, and is the PID after the acp process 92507. So no shared-service TUI saw this session either.
   The model call failed upstream, but the execution-failed path still removed the mark.
2. **PASS.** The server plugin writes execution marks for these sessions:
   - `opencode run` on the shared service: under 2163, removed at turn end.
   - `opencode run --standalone`: under the private child 45805, left stale (see step 8).
   - `opencode acp`: under its private child 92508, removed when the turn failed.

## Step 11. A subagent session

**PASS.** A `general` subagent (`ses_e98e9586effeNkvWc95dYHeCVq`) spawned from the handoff session:

```
$ echo "$OPENCODE_SESSION_ID"
ses_e98e9586effeNkvWc95dYHeCVq
$ ls sessions/presence
opencode.ses_e98e9586effeNkvWc95dYHeCVq.30555.1792730351-745038.json   <- child, same TUI
opencode.ses_e98f0b1feffeG5l2IOcxOonSAo.30555.1792730351-745038.json   <- root
...
$ ls sessions/executing
opencode.ses_e98e9586effeNkvWc95dYHeCVq.2163.1792730030-983643.json
opencode.ses_e98f0b1feffeG5l2IOcxOonSAo.2163.1792730030-983643.json
$ nitipan acquire --checkout .../nit-m4/scratch11 --description "m4 step 11 subagent"
acquired
01M5KHDG3TP8CRB997JR0XFH7T  alive
  holder: opencode session ses_e98e9586effeNkvWc95dYHeCVq, pid 2163
```

`holder.session_id` is the child's own ID, and its liveness is `presence mark from TUI 30555`. The subagent released it
(`released 01M5KHDG3TP8CRB997JR0XFH7T`). After the subagent finished, its execution mark cleared and its presence file
remained, because the root session is still running and the child follows it.

## Step 12. Permission modes

**PASS.**

```
$ nitipan acquire --checkout .../nit-m4/scratch12 --description "m4 perm build" --permission-mode build
acquired
01M5KGZZHTG9W56PB4KPS5TWZJ  alive
  permission mode: build
$ nitipan acquire --checkout .../nit-m4/scratch12 --description "m4 perm nonsense" --permission-mode nonsense
nitipan: permission mode "nonsense" is not one opencode offers; accepted values: build, plan, auto
(exit 2)
$ nitipan release 01M5KGZZHTG9W56PB4KPS5TWZJ
released 01M5KGZZHTG9W56PB4KPS5TWZJ
```

What opencode offers: `opencode debug agents` lists primary visible agents `build` and `plan` (hidden primaries:
`compaction`, `summary`, `title`; subagents: `general`, `explore`, and the `coding-standards/*` agents).
`opencode --help` and `opencode run --help` have `--auto` ("Auto-approve permissions that are not explicitly
denied"). v2.1.0 has no `opencode agent list` subcommand (`Unexpected positional argument: "list"`). The list
`build, plan, auto` matches and needs no change.

**Anomaly:** after the `--permission-mode build` acquire on scratch12, the earlier scratch1 lease
(`01M5KGZFPDK3C9JNXW1F4BMMKH`, acquired with no mode) shows `"permission_mode": "build"` in `list --json`, and still
does after scratch12 was released. Its first `list --json` had no `permission_mode` field. A mode passed on one
acquire seems to be stamped onto the session's other leases. That may be by design (mode is per session), but it is
not stated.

## Step 13. How the plugins load

**PASS.**

- `~/.config/opencode/plugins/nitipan` loaded **without** any `opencode.json` or `cli.json` entry.
- Server plugin: service log (`~/.local/share/opencode/log/opencode.log`, `role=server`, run `aa7c7574` = PID 2163):

```
04:34:14.965Z msg="loading plugin" id=/Users/ripta/.config/opencode/plugins/nitipan entrypoint=file:///Users/ripta/.config/opencode/plugins/nitipan/index.ts
04:34:14.966Z message="watcher started" path=/Users/ripta/.config/opencode/plugins/nitipan/index.ts type=file backend=node
```

  It loaded on first instance use (04:34:14), not at service start (04:33:50). The previous service (run `e50dd5ba`)
  had also loaded it at 04:32:51, without a restart.
- TUI plugin: loaded. It writes presence files as TUI 30555. The CLI-side log (`role=cli`) shows
  `plugin reconciliation completed ... plugins=13` at 04:23 (before install) and `plugins=14` at 04:34:15 and at
  04:39:11 (TUI 30555 start). The log names no plugin file for the TUI.
- `opencode plugin list` output: only `nitipan.server  local  .../nitipan/index.ts`. It does not list the TUI plugin.
- `context.data.on` subscriptions: no failure in `plugin.log`. The only line there is the server-plugin line in step 8.
  `session.execution.started` works in practice: a headless session's presence file appeared within seconds of its
  turn starting. Whether that came from the event or from the 2 s poll of `session.status` cannot be told apart.

## Summary of findings

| Step | Result |
|---|---|
| 0 | PASS. Plugins autoload from `~/.config/opencode/plugins/nitipan` with no config entry |
| 1 | PASS |
| 2 | 2.1, 2.2 PASS. **2.3 FAIL**: a no-TUI session acquired while any TUI was on the service |
| 3 | PASS. Many sessions per process (2163) |
| 4 | PASS. OBSERVED: MCP servers get no `OPENCODE_SESSION_ID` |
| 5 | PASS. A route without a tab is not possible in v2.1.0 |
| 6 | PASS. Both TUIs republish every mark on reload; no lease went dead |
| 7 | PASS |
| 8 | 8.1, 8.2 PASS. 8.3: turn resumed PASS; **no execution mark under the new service PID: FAIL** |
| 9 | 9.3 PASS with caveat (held by the cross-TUI mark). **9.4 FAIL** (`time_suspended` marks every in-flight turn; `unknown` never seen). 9.5 NOT RUN |
| 10 | **`opencode run` on the shared service FAIL** (marked by another TUI). `opencode acp` PASS. 10.2 PASS |
| 11 | PASS |
| 12 | PASS. Anomaly: `--permission-mode` stamps the session's other leases |
| 13 | PASS. `opencode plugin list` does not show the TUI plugin |

Issues for NIT-001, most important first:

1. **Cross-TUI marking.** `tui.ts` marks every running session on the service (tui.ts:91-93, 132-137), not only its
   own tabs and route. Consequences:
   - Any open TUI makes headless `opencode run` sessions look present while they run (steps 2.3, 8.1, 10.1).
   - It hides the execution-mark and `time_suspended` paths (steps 9.3, 9.4).
2. **`time_suspended` is set for every in-flight turn in v2.1.0,** cleared at idle (step 9.4). It cannot be read as
   "suspended".
3. **A resumed turn gets no new execution mark.** After `opencode service restart`, the server plugin does not mark the
   resumed turn under the new service PID. The old mark names a dead PID until the turn ends (step 8.3).
4. **Standalone shutdown leaves a stale execution mark.** The server plugin's `hook execution` child is killed when a
   `--standalone` private server exits (`plugin.log`: `server execution exited null`). nitipan later pruned the mark
   (step 8).
5. **Lease `holder.pid` goes stale across a service restart** (still 2163). Liveness survives only through marks
   (step 8.3).
6. **`--permission-mode` on one acquire set `permission_mode` on the session's other lease** (step 12).
7. **Tombstones are hidden and stuck.** `list --json` shows `"tombstones": []` while a tombstone file exists, and
   `break` cannot remove a tombstone whose holder is dead. It was pruned later on its own (steps 7, 9).
8. **The restarted service runs as a child of the TUI** (PPID 30555), and no LaunchAgent exists (step 8.3). This is
   opencode behavior, but it affects which PID nitipan records.

Deviations from the handoff:
- At the user's direction, the handoff session edited line 1 of `~/.config/opencode/plugins/nitipan/tui.ts` for
  step 6. It was restored with `--replace`.
- Several USER steps were run from the handoff session's shell instead of a terminal:
  - `opencode run` with `env -u` stripping the opencode variables (steps 2.3, 4, 8, 10);
  - a JSON-RPC client for `opencode acp`;
  - `kill -9` of the second TUI (9.2);
  - a detached script for the service restart (8.3, 9.4).
- One lease was cleared with `break --force` instead of being released by its holder: `01M5KK4T…`, from step 7.

## Leases left behind

None from this run. Every lease this run created was released or broken:

| lease | step | how |
|---|---|---|
| `01M5KGZFPDK3C9JNXW1F4BMMKH` | 1 | `release` |
| `01M5KGZZHTG9W56PB4KPS5TWZJ` | 12 | `release` |
| `01M5KH95D6TH8HAA18K2V6D25K` | 2.3 | `release` from its session |
| `01M5KHDG3TP8CRB997JR0XFH7T` | 11 | `release` by the subagent |
| `01M5KHZA2WH2YZTJMVKWY6H0KQ` | 3 | `release` from its session |
| `01M5KK4TAVWKGPTDCKTFDNVE3R` | 7 | `break --force`; its tombstone was later pruned |
| `01M5KKPA2NJDWSP5RF71RNABW3` | 9 | `break`, dead holder |
| `01M5KM13Y995TDJ0J12BWM0JP0` | 8.3/9.4 | `break`, dead holder |

```
$ /Users/ripta/projects/nitipan/build/nitipan list
01M5KKN8VTPQNB7B08GV5Q7PDD  dead
  liveness: the session ended and Codex's writer-lock file is gone
  domain: /private/tmp/nitipan-m4-auto-p5nj19ym/checkout-m4-auto-b-0wczojru
  handle: pole-frame-summer-olive
  agent name: (none)
  holder: codex session 01a1673a-915d-73e0-908b-5cef457ba4d8, pid 87537
  description: m4-auto-b
```

The run started with no leases. The one lease left belongs to another session's codex run, which created it during
this run (`m4-auto-b`). It was left alone. `tombstones/` is empty. `tui.ts` is back to its installed text.
`opencode.db`'s mode was never changed.
