// nitipan TUI plugin for opencode.
//
// It marks each session this TUI has open, by calling nitipan. A session counts as open while it is
// in the shared tab list, is the TUI's current route, or is running. Child sessions from the subagent
// tool follow their root. The mark ties to this TUI process, which the nitipan walk finds from this
// plugin's subprocess.
//
// Cleanup never removes marks, because opencode also runs it on a hot reload. A TUI that exits leaves
// its marks behind, and nitipan reads them as dead once the process is gone. A fresh setup
// republishes every mark.
//
// The two paths below are filled in when `nitipan setup opencode --install-plugins` writes this file.
import { spawn } from "node:child_process"
import { closeSync, constants, openSync, writeSync } from "node:fs"
import { join } from "node:path"

const NITIPAN: string = "@@NITIPAN@@"
const STATE_DIR: string = "@@STATEDIR@@"
const LOG = join(STATE_DIR, "plugin.log")
const POLL_MS = 2000

// The log sits in nitipan's state directory, which only the user can write. It is opened without
// following a symlink, with mode 0600.
function note(message: string) {
  try {
    const flags = constants.O_WRONLY | constants.O_APPEND | constants.O_CREAT | constants.O_NOFOLLOW
    const fd = openSync(LOG, flags, 0o600)
    try {
      writeSync(fd, `${new Date().toISOString()} tui ${message}\n`)
    } finally {
      closeSync(fd)
    }
  } catch {}
}

// Calls run one at a time, in order, so a mark on and a mark off for one session never swap. The
// promise says whether nitipan accepted the call.
let chain: Promise<unknown> = Promise.resolve()

function hook(subcommand: string, input: unknown): Promise<boolean> {
  const run = chain.then(
    () =>
      new Promise<boolean>((resolve) => {
        const child = spawn(NITIPAN, ["hook", subcommand], { stdio: ["pipe", "ignore", "pipe"] })
        let err = ""
        child.stderr.on("data", (chunk) => (err += String(chunk)))
        child.on("error", (e) => {
          note(`spawn failed: ${e}`)
          resolve(false)
        })
        child.on("close", (code) => {
          if (code !== 0) note(`${subcommand} exited ${code}: ${err.trim()}`)
          resolve(code === 0)
        })
        child.stdin.on("error", () => {})
        child.stdin.end(JSON.stringify(input))
      }),
  )

  chain = run

  return run
}

function attempt<T>(fn: () => T, fallback: T): T {
  try {
    return fn()
  } catch (e) {
    note(`read failed: ${e}`)
    return fallback
  }
}

export default {
  id: "nitipan.tui",
  setup(ctx: any) {
    // The sessions this plugin has told nitipan about.
    const marked = new Set<string>()
    const running = new Set<string>()

    const open = (): Set<string> => {
      const roots = new Set<string>(running)

      for (const tab of attempt<any[]>(() => [...ctx.ui.tabs.list()], [])) {
        if (tab?.sessionID) roots.add(tab.sessionID)
      }

      const route = attempt<any>(() => ctx.ui.router.current(), undefined)
      if (route?.type === "session" && route.sessionID) roots.add(route.sessionID)

      for (const info of attempt<any[]>(() => [...ctx.data.session.list()], [])) {
        const id = info?.id ?? info?.sessionID
        if (id && attempt(() => ctx.data.session.status(id), "idle") === "running") roots.add(id)
      }

      const all = new Set<string>(roots)
      for (const id of roots) {
        for (const member of attempt<string[]>(() => [...ctx.data.session.family(id)], [])) all.add(member)
      }

      return all
    }

    const sync = () => {
      const want = open()

      for (const id of want) {
        if (marked.has(id)) continue
        marked.add(id)

        // A call nitipan refused leaves the session unmarked, so the next poll tries again.
        hook("presence", { session_id: id, present: true }).then((ok) => {
          if (!ok) marked.delete(id)
        })
      }

      for (const id of [...marked]) {
        if (want.has(id)) continue
        marked.delete(id)

        hook("presence", { session_id: id, present: false }).then((ok) => {
          if (!ok) marked.add(id)
        })
      }
    }

    const idOf = (event: any): string | undefined =>
      event?.data?.sessionID ?? event?.sessionID ?? event?.properties?.sessionID

    const stops: Array<() => void> = []

    for (const type of ["session.execution.started", "session.created"]) {
      stops.push(
        ctx.data.on(type, (event: any) => {
          const id = idOf(event)
          if (id && type === "session.execution.started") running.add(id)
          sync()
        }),
      )
    }

    for (const type of [
      "session.execution.succeeded",
      "session.execution.failed",
      "session.execution.interrupted",
      "session.deleted",
    ]) {
      stops.push(
        ctx.data.on(type, (event: any) => {
          const id = idOf(event)
          if (id) running.delete(id)
          sync()
        }),
      )
    }

    // The tab list and the route are read on a timer. Whether they can be watched reactively from a
    // plugin is unconfirmed, and a poll does not depend on it.
    const timer = setInterval(sync, POLL_MS)
    sync()

    return () => {
      clearInterval(timer)
      for (const stop of stops) attempt(stop, undefined)
    }
  },
}
