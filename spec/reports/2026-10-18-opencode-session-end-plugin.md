# opencode Session End Through a Plugin

**Date:** 2026-10-18

This report asks whether a user-installed opencode plugin can tell nitipan when a session ended. It follows
`2026-10-18-shared-host-session-liveness.md`, which found no server-side end signal in opencode 2.0.32. It lists facts
and trade-offs only. It does not recommend a design.

## Why this matters

The shared `opencode serve --service` process runs every session's shell commands. It outlives every TUI. So process
liveness of the shell's ancestors says nothing about the session. A lease needs some other proof that a person still has
the session open.

The one end signal found so far runs in the TUI process. A TUI plugin runs there too. This report checks what such a
plugin can learn, what it can write, and which endings it survives.

## Method and tags

The source is `anomalyco/opencode` at commit 501b15af26915ba52682e20e220e7e137e89e92f, fetched as a GitHub tarball.
Paths are relative to `packages/`. The terminal library is `@opentui/core` 0.5.17, from tag `v0.5.17` of the opentui
repo. Its paths are relative to `packages/core/src/`.

The release tag `v2.0.32` points at 843a6c5, "release: v2.0.32", dated 2026-10-16. Commit 501b15a is on `dev`, 50
commits after that tag's merge base. A GitHub compare between the two lists no change to `tui/src/plugin`,
`tui/src/context/session-tabs.tsx`, `tui/src/context/storage.tsx`, `tui/src/app.tsx`, `plugin/src`, or
`schema/src/event-manifest.ts`. It does list `core/src/shell.ts`. The environment block there behaves the same at the
tag (lines 259-271). So the findings below hold for 2.0.32.

Upstream state was read through the unauthenticated GitHub REST API on 2026-10-18. The `gh` CLI could not read its
config inside the sandbox, so it was not used. The search endpoint rate-limited some queries. GitHub Discussions were
not searched, since they need an authenticated GraphQL call. The `sst/opencode` name redirects to the same repo and was
not searched separately.

No process was started, stopped, or signalled. No plugin was installed or run. No file under `~/.config` was read.

- SOURCE: read from code, with path and line at the commit above.
- DOCUMENTED: from the vendor docs, with URL.
- VERIFIED: observed on this machine on 2026-10-18, with the command.
- UNVERIFIED: not checked, or inferred.

## 1. Upstream: server-side fix proposed or built

Nothing was found that adds a server-side session end, client disconnect, or presence signal. These are the closest
items.

| Item | State | Dates | What it is |
|---|---|---|---|
| #55358 | open | created 2026-10-14 | `session.idle` and `session.status` are declared in the V2 schema and never emitted. |
| PR #55380 | open | created 2026-10-14 | Publishes `session.status` busy at execution start and idle at settle. Closes #55358. |
| #16626 | open | created 2026-03-08, updated 2026-10-16 | Asks for a `session.stopping` plugin hook. It is about re-entering the loop. |
| #56115 | open | created 2026-10-16 | Asks to expose inbox cancel and a busy query to plugins. |
| #55214 | open | created 2026-10-13 | Reports that agents keep running after the app is closed. Reported on 2.0.28. |
| #52203 | open | created 2026-09-30 | Windows TUI survives closing its terminal. Cites closed #10563, #12767, #12913 for SIGHUP on macOS and Linux. |
| #56963 | open | created 2026-10-19 | On 2.0.32 on Windows, a bun process stays alive after the TUI closes. |
| #50363 | open | created 2026-09-21 | Local MCP servers outlive the sessions that spawned them. |
| PR #53759 | merged | merged 2026-10-19T03:46Z | Restores session archiving: `PATCH archived`, and `session.archived` and `session.unarchived` events. |

- VERIFIED for all rows: `curl https://api.github.com/repos/anomalyco/opencode/issues/<n>` and `.../pulls/53759`, read
  2026-10-18.
- A search for `session.closed` returned 0 results. VERIFIED: the GitHub search endpoint.
- #55380 would add busy and idle per turn. It would not say a client left. Even merged, `session.status` marks a turn
  end.
- #55214 describes the intended model. The server owns completion after the client goes away. This matches the comment
  at `core/src/session/session.ts:184` quoted in the earlier report.
- PR #53759 landed after 501b15a. It is an explicit archive action by a user or client. Nothing in it ties archiving to
  a TUI closing. Whether the TUI calls it on tab close is UNVERIFIED, since the TUI changes sit in the unread part of
  that diff.
- Dev HEAD at read time was ba06ff9, 10 commits after 501b15a. VERIFIED: the compare API. None of the 10 touch the
  plugin, tabs, storage, or event files named above.
- The newest `v2.0.x` tag found was `v2.0.32`. A check for `v2.0.33`, `v2.1`, and `v3` hit the rate limit and was not
  completed. The GitHub "latest release" is `v1.19.3`, which is the v1 line.

## 2. TUI plugin mechanics at 2.0.32

### Installing and configuring

- A TUI plugin is a module whose default export is `Plugin.define({ id, setup })`. SOURCE:
  `plugin/src/tui/plugin.ts:7-14`, `tui/src/plugin/context.tsx:741-751`.
- `setup(context)` may be async. It may return a cleanup function. SOURCE: `plugin/src/tui/plugin.ts:5-9`.
- The loader looks for a `tui` export, by package subpath or by a `tui` file in a directory. SOURCE:
  `plugin/src/host.ts:17-44`.
- Three sources feed the loader. SOURCE: `tui/src/plugin/context.tsx:290-309`.
  1. Subdirectories of `<config dir>/plugins` and of each project `.opencode/plugins`. SOURCE:
     `tui/src/plugin/discovery.ts:8-22,24-53`. Only directories and symlinks to directories are taken, at line 35.
  2. TUI-capable plugins that the server inventory reports as active. SOURCE: `tui/src/plugin/context.tsx:96-103`.
  3. The `plugins` array of the CLI config, as a string or `{ package, options }`. SOURCE:
     `tui/src/config/index.tsx:79-81`, `tui/src/plugin/context.tsx:306`.
- The CLI config file is `cli.json` in the global config directory. SOURCE: `cli/src/config/config.ts:31`. The directory
  itself comes from `Global`. UNVERIFIED as `~/.config/opencode`, since that path is outside this session's read access.
- The docs say a TUI plugin exports `./tui` beside the main plugin. They say a CLI-only package goes in `cli.json` under
  `plugins`. DOCUMENTED: https://opencode.ai/v2/docs/build/plugins/cli.
- The docs list install commands for package plugins: `opencode plugin add <package>`, and `list`, `check`, `update`,
  `remove`. DOCUMENTED: https://opencode.ai/v2/docs/plugins.
- Local directory plugins hot reload. A file change under a watched directory tears down and re-sets up the plugin.
  SOURCE: `tui/src/plugin/context.tsx:252-268,428-457`.

### What the plugin receives

- `Context` gives `options`, `location`, `app`, `renderer`, `client`, `data`, `attention`, `theme`, `keymap`, `storage`,
  and `ui`. SOURCE: `plugin/src/tui/context.ts:544-560`.
- `context.data.on(type, handler)` subscribes to one typed server event. `context.data.listen(handler)` takes all of
  them. Both return an unsubscribe function. SOURCE: `plugin/src/tui/context.ts:63-68`. DOCUMENTED:
  https://opencode.ai/v2/docs/build/plugins/cli.
- `context.data.session.get`, `root`, `family`, and `status` exist. `status` returns `"idle"` or `"running"`. SOURCE:
  `plugin/src/tui/context.ts:69-75`.
- `context.ui.tabs.list()` returns the open root-session tabs with `sessionID`, `active`, and `busy`. It is reactive. It
  also has `open`, `focus`, `move`, and `close`. SOURCE: `plugin/src/tui/context.ts:509-529`.
- `context.ui.router.current()` returns the route. A session route carries `sessionID`. SOURCE:
  `plugin/src/tui/context.ts:142-145,496-500`.
- There is no "session opened", "tab closed", or "TUI exiting" event. The plugin sees them only by watching
  `ui.tabs.list()` and `ui.router.current()` change, or by `session.created` and `session.deleted` events. SOURCE: a
  negative result over `plugin/src/tui/context.ts`.
- Reading those two reactive values inside a Solid effect in `setup` is UNVERIFIED. The plugin owner is captured at
  `tui/src/plugin/api.tsx:66`. Built-in plugins subscribe through `data.on`, as in
  `tui/src/feature-plugins/system/notifications.ts:36-60`.
- `context.storage.store(key, { initial })` is durable JSON. It is written under `<state>/<channel>/tui/<key>.json`,
  under a file lock. SOURCE: `tui/src/context/storage.tsx:47-53,71-89`. DOCUMENTED: "persists JSON across restarts and
  synchronizes across TUI instances".
- `context.storage.memory` is in-process and dies with the TUI. SOURCE: `plugin/src/tui/context.ts:42-52`.

### Learning the TUI PID and start time

- The plugin module is imported into the TUI process. SOURCE: `plugin/src/host.ts:46-48`,
  `tui/src/plugin/context.tsx:670-696`.
- So `process.pid` and `process.ppid` in the plugin are the TUI's. This follows from the import. UNVERIFIED by running a
  plugin.
- No sandbox or permission gate sits on the load path. SOURCE: `host.ts:46-48`, `context.tsx:652-698`. A server plugin
  in an upstream issue uses `node:fs` directly. SOURCE: the body of issue #55358.
- Start time has two routes. One is `Date.now() - process.uptime() * 1000`. The other is `ps -o lstart= -p <pid>`. Both
  UNVERIFIED.
- Running a subprocess or writing a file from the plugin is therefore possible by the same argument. UNVERIFIED.
- The plugin knows the session IDs of its tabs and its current route. Tabs are shared state, covered next.

### Tabs are shared, not per TUI

- Tab state is `storage.store("tabs")`. That is the single file `<state>/<channel>/tui/tabs.json`. SOURCE:
  `tui/src/context/session-tabs.tsx:72-78`.
- It holds one list per working directory by default. SOURCE: `tui/src/config/index.tsx:334`,
  `session-tabs.tsx:104-107`.
- Every TUI watches that directory and reloads on change. SOURCE: `tui/src/context/storage.tsx:116-121`.
- Tabs are on by default, unless `HERDR_ENV` is `1`. SOURCE: `tui/src/config/index.tsx:292,333`.
- So `ui.tabs.list()` is the shared persisted list. It is not a list of what this TUI process has open.
- A tab survives a TUI crash. The next TUI in that directory shows it again. SOURCE: persistence in `storage.tsx:56-93`
  plus the restore path. The restore itself is UNVERIFIED.
- VERIFIED: `ls -la ~/.local/state/opencode/latest/tui` shows `tabs.json`, 2384 bytes, modified Oct 18 21:09. Its
  contents were not read.
- Closing a tab in any TUI removes it from the shared list. Every other TUI in that scope sees it vanish. SOURCE:
  `session-tabs.tsx:325-350`.
- The route is per TUI and in memory. A TUI adds a tab for its routed session on route change. SOURCE:
  `session-tabs.tsx:180-203`.

### What runs on each ending

The TUI creates its renderer with `exitOnCtrlC: false` and no `exitSignals` override. SOURCE: `tui/src/app.tsx:243`. The
renderer then uses its default list of exit signals. SOURCE: opentui `renderer.ts:1185-1194`. That list is SIGINT,
SIGTERM, SIGQUIT, SIGABRT, SIGHUP, SIGPIPE, SIGBREAK, and SIGBUS.

Each of those calls `renderer.destroy()`. SOURCE: opentui `renderer.ts:1004-1006,1336-1342`. `destroy` sets
`isDestroyed` first, then emits `destroy`. SOURCE: opentui `renderer.ts:4411-4413,4541`. The TUI also registers its own
SIGHUP handler that destroys the renderer. SOURCE: `tui/src/app.tsx:289-293`.

The `destroy` event opens the shutdown latch. SOURCE: `tui/src/app.tsx:294,448`. The scope then closes. Its finalizers
run every registered `dispose`, which deactivates each plugin and awaits its cleanup. SOURCE: `app.tsx:279-287`,
`tui/src/plugin/context.tsx:571-594,208-231,633-637`. The CLI exits only after the TUI run finishes. SOURCE:
`cli/src/index.ts:139`. So a cleanup that is slow delays exit. No timeout was found.

| Ending | Plugin cleanup runs | Basis |
|---|---|---|
| Normal quit | Yes | SOURCE: path above. |
| SIGHUP, closed terminal on POSIX | Yes | SOURCE: `app.tsx:289-293` and the default list. |
| SIGTERM | Yes | SOURCE: opentui `renderer.ts:1187`. The earlier report listed only SIGHUP and normal exit. |
| SIGINT | Yes | SOURCE: opentui `renderer.ts:1186`. |
| Crash, uncaught exception | UNVERIFIED | The renderer adds `uncaughtException` and `unhandledRejection` handlers at `renderer.ts:1247-1248`. Whether they reach the finalizers was not traced. |
| SIGKILL | No | Inferred. No process can handle it. UNVERIFIED by test. |
| Windows terminal close | Not reliable | Open issue #52203. |

- The same cleanup runs when a plugin is disabled, hot reloaded, or replaced. SOURCE: `context.tsx:208-231,436-440`. A
  cleanup cannot tell that apart from TUI exit by itself.
- On TUI exit the renderer is already destroyed when cleanup runs. So `context.renderer.isDestroyed` is true there. This
  follows from the order above. UNVERIFIED by test.
- Cleanup is not called when a single tab closes. Tab close is visible only as a change in `ui.tabs.list()`.
- The server keeps running the session's turn after the TUI goes. SOURCE: `core/src/session/session.ts:184`. Issue
  #55214 reports it.

## 3. Server plugins

- A server plugin runs in the serve process. It is loaded per directory, not per session. SOURCE:
  `core/src/plugin/module.ts:98-124`, `core/src/plugin.ts:238-248`, from the earlier report.
- It can subscribe to the public event stream with `ctx.event.subscribe({ signal })`. SOURCE:
  `plugin/src/promise/event.ts:1-3`. DOCUMENTED: https://opencode.ai/v2/docs/build/plugins.
- The stream carries the events in `schema/src/event-manifest.ts:71-103`. Per session, that is `session.created`,
  `session.deleted`, `session.moved`, `session.viewed`, and `session.execution.started|succeeded|failed|interrupted`.
  SOURCE: `schema/src/session-event.ts:49,91,132,178,240-254`.
- `session.status` and `session.idle` are in the manifest and never published. SOURCE: earlier report. Upstream issue
  #55358 confirms it for 2.0.28, and PR #55380 is the open fix.
- Hooks a server plugin can register: session `prompt`, `context`, `compaction`, `generate`, `title`, model and http
  hooks, `retry`; permission `evaluate`; shell `create.before`; tool `execute.before` and `execute.after`. DOCUMENTED:
  https://opencode.ai/v2/docs/build/plugins.
- Nothing in that list carries a client ID, a subscriber count, or a disconnect.
- Each SSE subscriber is an anonymous queue. Disconnect removes it and publishes nothing. SOURCE:
  `server/src/event-feed.ts:72-80`, from the earlier report.
- Instance disposal exists. `location.shutdown` is published when a directory's location services close, and
  `global.disposed` is declared. SOURCE: `schema/src/location-event.ts:6`, `schema/src/server-event.ts:6`,
  `core/src/location-lifecycle.ts:46-56`, `core/src/instance.ts:174`.
- A location is evicted after 60 minutes with no session events for that directory and no running turn. SOURCE:
  `core/src/location-activity.ts:25,47-81`. That is activity-based and per directory. It says nothing about a client.
- Whether a server plugin sees `location.shutdown` on its own stream is UNVERIFIED. Its setup cleanup does run when the
  location closes. SOURCE: `core/src/plugin.ts:245`, from the earlier report.
- `session.viewed` is durable. A focused TUI posts it when it shows a session with an unread idle transition. It carries
  the idle watermark and no client ID. SOURCE: `schema/src/session-event.ts:132-141`,
  `tui/src/context/session-tabs.tsx:205-239`. It fires only on that condition. It is not presence.

## 4. Design inputs for a nitipan plugin

### 4a. Marker naming the TUI process and the session

- The TUI PID is reachable from a TUI plugin, by the import argument in section 2. UNVERIFIED by test.
- The plugin can learn a session ID at three moments. The route changes to a session. A tab appears in the shared list.
  A `session.created` event arrives. SOURCE: section 2.
- At TUI start, the plugin's setup runs after the first route may already be a session. So it must read the current
  route when it sets up, as well as watch for changes. SOURCE: `context.tsx:150-206` runs `setup` during activation, and
  `router.current()` is readable at any time.
- Nitipan holds the session ID already. The shell tool exports `OPENCODE_SESSION_ID`. SOURCE:
  `core/src/tool/plugin/shell.ts:216`. So a marker keyed by session ID can be found from inside a shell.
- A marker with PID plus start time stays truthful after the TUI dies, even if cleanup never ran. Nitipan checks that
  process, not the file. So it covers crash and SIGKILL. This is a property of the check, not of opencode.
- Gaps in that check:
  - A TUI whose process lingers after the terminal closes still reads alive. Issues #52203 and #56963 are Windows
    reports. The POSIX path is handled in source.
  - A frozen TUI reads alive.
  - A marker names a TUI, not a tab. A session whose tab was closed while the TUI runs still reads alive unless the
    plugin edits or removes the marker.
  - A session driven by something other than a TUI has no marker. That includes `opencode run`, the desktop app, ACP,
    and direct API calls. The plugin is never loaded in those.
  - A TUI started without the plugin has no marker. A plugin disabled mid-run leaves a marker with a live PID.

### 4b. Subagent child sessions

- A subagent is a child session with its own ID and a `parentID`. VERIFIED in the adapter handoff, steps 1 and 6.
- `ui.tabs.list()` holds root sessions only. SOURCE: `plugin/src/tui/context.ts:511`, `session-tabs.tsx:117,131-137`.
- `data.session.root(id)` and `family(id)` walk `parentID` through loaded session info. SOURCE:
  `client/src/solid/data.ts:536-549,1401-1404`. An unloaded child may resolve to itself. UNVERIFIED.
- The TUI loads children for open tabs. SOURCE: `session-tabs.tsx:269` passes `children: true`.
- So the TUI that holds the root session in a tab or route is the one that can name a child's owner. A plugin can write
  one marker per root and map children by `root()`.
- A child's shell commands run in the service, not the TUI. A background child keeps running with no TUI. SOURCE:
  `core/src/session/session.ts:184`.
- Several TUIs can load the same family. See 4c.

### 4c. Moved between TUIs, or open in two at once

- Two TUIs on one session is possible. The route is per TUI and in memory. The server keeps no per-client claim. SOURCE:
  a negative result over `server/src` and `core/src/session` in the earlier report. `time_suspended` is an execution
claim, not a client claim.
- Two TUIs in the same directory share one tab list. SOURCE: `session-tabs.tsx:104-107`, `storage.tsx:116-121`.
- `session.moved` is a durable event with an inbox move payload. SOURCE: `schema/src/session-event.ts:91-98`. The tabs
  code reads it to refresh location and VCS info. SOURCE: `session-tabs.tsx:303-308`. Nothing found uses it as a
  TUI-to-TUI handoff. UNVERIFIED as a negative result beyond `tui/src` and `schema/src`.
- A session can be "moved" between TUIs in practice by closing a tab in one and opening it in another. Each TUI would
  write or remove its own marker. Order of those writes is not guaranteed.
- A marker design must allow more than one live marker per session. Which TUI owns the lease is a nitipan choice.

## 5. Does any mechanism export the TUI PID or a client ID to a shell tool

No mechanism exports the TUI's PID or a client ID. These are the pieces that do reach a shell's environment.

- The shell tool sets `AGENT=1`, `OPENCODE=1`, `AI_AGENT` if unset, and `OPENCODE_SESSION_ID`. SOURCE:
  `core/src/tool/plugin/shell.ts:213-216`. It also sets `OPENCODE_TERMINAL=1` and `TERM`. SOURCE:
`core/src/shell.ts:271-280`.
- The base environment is the session's stored environment if one exists. Otherwise it is the service's `process.env`.
  SOURCE: `core/src/shell.ts:267-270`.
- The TUI sends its own `process.env` to the service as the session environment. It removes `OPENCODE_PASSWORD` and
  `OPENCODE_SERVER_PASSWORD`. SOURCE: `cli/src/env.ts:15-22`, `cli/src/commands/handlers/default.ts:143`. It is sent
only when the TUI is not given `--server`.
- The TUI re-sends it for the routed session whenever the route, session info, or connection changes. SOURCE:
  `tui/src/app.tsx:497-504`. It also sends it for a newly created session. SOURCE:
`tui/src/component/prompt/index.tsx:1248-1249`.
- So whatever is in the TUI's launch environment reaches that session's shell commands. The TUI does not add its own
  PID. A launcher script could add one. That would need a wrapper around `opencode`. UNVERIFIED.
- The endpoint is `PUT /api/session/:sessionID/environment`. It "replace[s] the process environment used by local shell
  commands for this session". SOURCE: `protocol/src/groups/session.ts:795-806`, `core/src/session/environment.ts:17-31`.
- A TUI plugin could call it through `context.client`. The payload replaces the whole map, so the plugin would have to
  send the full environment. The TUI's own effect would overwrite it on the next trigger. Both are SOURCE, the
  combination is UNVERIFIED.
- The stored environment is an in-memory map. It is lost when the service restarts. SOURCE:
  `core/src/session/environment.ts:18`.
- Only the routed session gets the push. A child session that nobody routes to has no stored environment. Its shell
  takes the service's `process.env`. SOURCE: `shell.ts:267-270`. The only callers of the environment endpoint are
  `tui/src/app.tsx:503`, `tui/src/component/prompt/index.tsx:1249`, and `cli/src/session-target.ts:78`. UNVERIFIED by a
  live test.
- A server plugin hook `shell create.before` can add variables to one invocation. SOURCE: `core/src/shell.ts:282`.
  DOCUMENTED: https://opencode.ai/v2/docs/build/plugins. A server plugin does not know which TUI owns the session.

## Candidate mechanisms

"Detects" means nitipan can tell the session ended. Marker liveness means nitipan checks that PID and start time itself.

| Mechanism | Normal exit | Closed terminal | Crash | kill -9 | Tab closed, TUI runs | Stability |
|---|---|---|---|---|---|---|
| TUI plugin writes marker (PID plus start time, session ID). Nitipan checks the process | Yes | Yes on POSIX | Yes | Yes | No | Plugin API is documented. Tabs and router are typed in the public package. Process check is nitipan's |
| Same marker, plus plugin cleanup removes it | Yes, sooner | Yes, sooner | No, falls back to the process check | No, same | No | Same. Cleanup also runs on plugin reload, so it can remove a marker early |
| Same marker, plus plugin watches `ui.tabs.list()` and `ui.router.current()` and updates it | Yes | Yes | Yes | Yes | Yes for this TUI. A tab closed in another TUI also vanishes | Reactive reads in `setup` UNVERIFIED. Tab list is shared state |
| Plugin PUTs `NITIPAN_*` into the session environment | None. Identity only | None | None | None | None | Replace semantics. Overwritten by the TUI. Routed session only |
| Launcher wrapper exports a variable that carries its PID | None. Identity only | None | None | None | None | Not an opencode feature. Reaches only routed sessions, and only without `--server` |
| Server plugin on the event stream | No | No | No | No | No | No end event. `location.shutdown` is a 60-minute directory eviction |
| Service HTTP API (`session.get`, `session.active`) | No | No | No | No | No | Row persists. Undocumented |
| `session.viewed` event | No | No | No | No | No | Fires only on unread idle. No client ID |
| `session.archived` event | No | No | No | No | No | New after 501b15a. User action only |
| `--standalone` serve child liveness | Yes | Yes | Yes | Yes | No | Internal. Only root and its subagents share that process |

## Trade-offs

- The marker-plus-process check gets crash and SIGKILL for free. It does not need cleanup to run.
- The marker design depends on the plugin being loaded in every TUI. Any session opened elsewhere reads as never marked.
- A marker names the TUI, and the TUI can hold many tabs. Marking a tab as closed needs the plugin to watch the shared
  tab list. That list also changes when another TUI closes a tab.
- The route is the sharper signal of "someone is looking at this session". It is also the weaker one, since a person can
  leave a tab open for days. Tabs are the wider one. Neither is a signal of a person present.
- Cleanup now covers SIGTERM as well as SIGHUP. The earlier report listed SIGHUP and normal exit only.
- A TUI that does not exit on terminal close would hold its markers alive. That is reported on Windows. It is handled in
  source on POSIX.
- A session's shell commands keep running after the TUI goes. A lease that releases on TUI death still leaves the shell
  command running. That is a nitipan policy question, not an opencode one.
- No upstream work targets a server-side end signal. PR #55380 would add per-turn status only.
