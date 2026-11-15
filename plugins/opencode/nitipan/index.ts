// nitipan server plugin for opencode.
//
// It marks a session while the service holds an execution claim for it, by calling nitipan. The
// mark ties to the service process, which the nitipan walk finds from this plugin's subprocess.
// nitipan reads the marks to tell whether an opencode session still runs.
//
// A turn that a restarted service resumes raises no execution event this plugin sees. So on load,
// and again a few times just after, the plugin asks which recent sessions are running and marks
// each one. A running session is one whose wait call does not return at once. The same call
// returns when the turn ends, and that marks the session off.
//
// The two paths below are filled in when `nitipan setup opencode --install-plugins` writes this file.
import { spawn } from "node:child_process"
import { closeSync, constants, openSync, readdirSync, writeSync, watch, mkdirSync } from "node:fs"
import { join } from "node:path"

const NITIPAN: string = "@@NITIPAN@@"
const STATE_DIR: string = "@@STATEDIR@@"
const LOG = join(STATE_DIR, "plugin.log")
const EXECUTING = join(STATE_DIR, "sessions", "executing")
const RETRIES = 3
const RETRY_MS = 2000
const SCAN_LIMIT = 200
const PROBE_MS = 300
const REPROBES = 5
const REPROBE_MS = 3000
const RESCAN_MS = [2000, 5000, 15000, 45000]

// The log sits in nitipan's state directory, which only the user can write. It is opened without
// following a symlink, with mode 0600.
function note(message: string) {
  try {
    const flags = constants.O_WRONLY | constants.O_APPEND | constants.O_CREAT | constants.O_NOFOLLOW
    const fd = openSync(LOG, flags, 0o600)
    try {
      writeSync(fd, `${new Date().toISOString()} server ${message}\n`)
    } finally {
      closeSync(fd)
    }
  } catch {}
}

function once(subcommand: string, input: unknown): Promise<boolean> {
  return new Promise<boolean>((resolve) => {
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
  })
}

// Calls run one at a time, in order, so a mark on and a mark off for one session never swap. This
// plugin has no poll, so a call nitipan refuses is tried again a few times before it is given up.
let chain: Promise<unknown> = Promise.resolve()

function hook(subcommand: string, input: unknown) {
  chain = chain.then(async () => {
    for (let attempt = 0; attempt < RETRIES; attempt++) {
      if (await once(subcommand, input)) return
      await new Promise((resolve) => setTimeout(resolve, RETRY_MS))
    }

    note(`gave up on ${subcommand} after ${RETRIES} attempts`)
  })
}

function sessionID(event: any): string | undefined {
  return event?.data?.sessionID ?? event?.sessionID ?? event?.properties?.sessionID ?? event?.data?.info?.id ?? event?.properties?.info?.id ?? event?.data?.id
}

// Who last marked a session on. An event owns the session until its next end event. A scan owns
// it until the wait call returns, unless an event took it over first. Only the current owner may
// mark the session off, so a late wait never removes the mark of a newer turn.
const EVENT = Symbol("event")
const owner = new Map<string, symbol>()

function markOn(id: string, who: symbol) {
  owner.set(id, who)
  hook("execution", { session_id: id, executing: true })
}

function markOff(id: string, who?: symbol) {
  if (who !== undefined && owner.get(id) !== who) return

  owner.delete(id)
  hook("execution", { session_id: id, executing: false })
}

const sleep = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms))

// One wait call. It returns true when opencode answered, which means the session is idle or its turn
// just ended, and false when the call failed. A failure says nothing about the turn.
function waitOnce(ctx: any, id: string): Promise<boolean> {
  return Promise.resolve()
    .then(() => ctx.session.wait({ sessionID: id }))
    .then(
      () => true,
      (e) => {
        note(`wait for ${id} failed: ${e}`)
        return false
      },
    )
}

// Waits for the end of a turn this scan marked. A failed wait keeps the mark and probes again, a few
// times. Past that the mark stays until an end event, because an unknown state counts as running.
// Nothing runs once the instance is torn down, since the module-level state belongs to its successor.
async function settle(ctx: any, id: string, who: symbol, first: Promise<boolean>, signal: AbortSignal) {
  let returned = await first

  for (let attempt = 0; !returned && attempt < REPROBES; attempt++) {
    await sleep(REPROBE_MS)
    if (signal.aborted) return

    returned = await waitOnce(ctx, id)
  }

  if (signal.aborted) return

  if (returned) markOff(id, who)
  else note(`gave up probing ${id}; its mark stays until an end event`)
}

// The sessions that already have an execution mark under this process. A turn that ended while an
// earlier instance of this plugin was torn down leaves its mark behind, and no end event reaches
// this instance. File names are `opencode.<session>.<pid>.<start>.json`.
function markedHere(): Set<string> {
  const found = new Set<string>()

  try {
    for (const name of readdirSync(EXECUTING)) {
      const parts = name.split(".")
      if (parts.length >= 4 && parts[0] === "opencode" && parts[2] === String(process.pid)) found.add(parts[1])
    }
  } catch (e) {
    note(`reading marks failed: ${e}`)
  }

  return found
}

// A session is running when its wait call has not returned after the probe delay. The wait call
// never starts work. A wait that fails before then leaves the state unknown, so it is never marked,
// and a mark it leaves behind stays. An idle session that still has a mark here, with no owner, lost
// its end event, so the mark goes.
async function scan(ctx: any, signal: AbortSignal) {
  let ids: string[] = []

  try {
    const page = await ctx.session.list({ limit: SCAN_LIMIT, order: "desc" })
    ids = (page?.data ?? []).map((session: any) => session?.id).filter((id: unknown) => typeof id === "string")
  } catch (e) {
    note(`scan list failed: ${e}`)
    return
  }

  const left = markedHere()
  const probe = new Set<string>([...ids, ...left])

  await Promise.all(
    [...probe].map(async (id) => {
      if (owner.has(id) || signal.aborted) return

      const first = waitOnce(ctx, id)
      const outcome = await Promise.race([first, sleep(PROBE_MS).then(() => "pending" as const)])
      if (owner.has(id) || signal.aborted) return

      if (outcome === true) {
        if (left.has(id)) markOff(id)
        return
      }

      if (outcome !== "pending") return

      const who = Symbol(id)
      markOn(id, who)
      settle(ctx, id, who, first, signal)
    }),
  )
}

export default {
  id: "nitipan.server",
  async setup(ctx: any) {
    const controller = new AbortController()
    const timers: ReturnType<typeof setTimeout>[] = []

    const mailSessions = new Set<string>()
    let mailBusy = false
    let mailAgain = false
    const mailCall = (command: string, input: unknown): Promise<any> => new Promise((resolve, reject) => {
      const child = spawn(NITIPAN, ["hook", command], { stdio: ["pipe", "pipe", "pipe"], timeout: 5000 })
      let out = "", err = ""
      child.stdout.on("data", (chunk) => out += String(chunk))
      child.stderr.on("data", (chunk) => err += String(chunk))
      child.on("error", reject)
      child.on("close", (code) => {
        if (code !== 0) reject(new Error(`${command}: ${code}: ${err.trim()}`))
        else {
          if (err.trim()) note(err.trim())
          try { resolve(out.trim() ? JSON.parse(out) : null) } catch (e) { reject(e) }
        }
      })
      child.stdin.on("error", () => {})
      child.stdin.end(JSON.stringify(input))
    })
    // Context hooks run when the agent is already taking a step. They repeat unread reminders without
    // claiming waking rings or adding a new turn to the session's queue.
    await ctx.session.hook("context", async (context: any) => {
      if (controller.signal.aborted) return
      try {
        const reminders = await mailCall("mail-reminder", { session_id: context.sessionID })
        for (const reminder of reminders ?? []) context.system.push({ text: reminder.ring })
      } catch (e) { note(`mail context reminder failed: ${e}`) }
    })

    const pendingRetries = new Map<string, Set<string>>()
    const retryMail = async (id: string) => {
      const keys = pendingRetries.get(id)
      if (!keys) return
      try {
        await mailCall("mail-retry", { session_id: id, keys: [...keys] })
        pendingRetries.delete(id)
      } catch (e) {
        pendingRetries.delete(id)
        pendingRetries.set(id, keys)
        note(`mail ring rollback for ${id} failed: ${e}`)
      }
    }
    const pollMail = async () => {
      if (controller.signal.aborted || !mailSessions.size) return
      if (mailBusy) { mailAgain = true; return }
      mailBusy = true
      try {
        do {
          mailAgain = false
          const sessions = [...mailSessions]
          for (let offset = 0; offset < sessions.length; offset += SCAN_LIMIT) {
            const ids = sessions.slice(offset, offset + SCAN_LIMIT)
            let rings: any[]
            try {
              rings = await mailCall("mail-poll", { session_id: ids[0], sessions: ids })
            } catch (e) { note(`mail batch failed: ${e}`); continue }
            for (const ring of rings ?? []) {
              try {
                if (controller.signal.aborted) throw new Error("plugin stopped before prompt")
                await ctx.session.prompt({ sessionID: ring.session_id, delivery: "queue", text: ring.ring })
              } catch (e) {
                const keys = pendingRetries.get(ring.session_id) ?? new Set<string>()
                for (const key of ring.keys) keys.add(key)
                pendingRetries.set(ring.session_id, keys)
                note(`mail ring for ${ring.session_id} failed: ${e}`)
              }
            }
          }
        } while (mailAgain && !controller.signal.aborted)
        // Rollbacks follow healthy delivery, one per poll. A failure rotates to the end so another
        // broken recipient cannot monopolize retries or delay every new ring by a helper timeout.
        const retryID = pendingRetries.keys().next().value
        if (retryID !== undefined) await retryMail(retryID)
      } catch (e) { note(`mail watcher failed: ${e}`) }
      finally { mailBusy = false }
    }
    let discoveryBusy = false
    let mailCursor: string | undefined
    // Advance one page per tick so old idle sessions are eventually watched without an unbounded
    // startup scan. Restarting a failed traversal also recovers an expired cursor.
    const discoverMail = async () => {
      if (controller.signal.aborted || discoveryBusy) return
      discoveryBusy = true
      try {
        const page = await ctx.session.list({ limit: SCAN_LIMIT, order: "desc", cursor: mailCursor })
        if (controller.signal.aborted) return
        for (const s of page?.data ?? []) if (typeof s?.id === "string") mailSessions.add(s.id)
        mailCursor = page?.cursor?.next
      } catch (e) {
        mailCursor = undefined
        note(`mail session discovery failed: ${e}`)
      } finally { discoveryBusy = false }
      await pollMail()
    }
    const inbox = join(STATE_DIR, "mail", "inbox")
    mkdirSync(inbox, { recursive: true, mode: 0o700 })
    const mailWatcher = watch(inbox, () => { void pollMail() })
    mailWatcher.on("error", (e) => note(`mail watch failed: ${e}`))
    const mailTimer = setInterval(() => { void discoverMail() }, 5000)
    void discoverMail()

    const watchEvents = async () => {
      for await (const event of ctx.event.subscribe({ signal: controller.signal })) {
        if (controller.signal.aborted) break

        const type: string = event?.type ?? ""
        if (!type.startsWith("session.")) continue
        const id = sessionID(event)
        if (!id) continue
        if (type === "session.deleted") { mailSessions.delete(id); continue }
        mailSessions.add(id)
        void pollMail()
        if (!type.startsWith("session.execution.")) continue

        if (type === "session.execution.started") markOn(id, EVENT)
        else markOff(id)
      }
    }

    watchEvents().catch((e) => {
      if (!controller.signal.aborted) note(`event stream ended: ${e}`)
    })

    const rescan = () => scan(ctx, controller.signal).catch((e) => note(`scan failed: ${e}`))

    rescan()
    for (const ms of RESCAN_MS) timers.push(setTimeout(rescan, ms))

    return () => {
      controller.abort()
      mailWatcher.close()
      clearInterval(mailTimer)
      const ids = [...mailSessions]
      if (ids.length) void mailCall("mail-stop", { session_id: ids[0], sessions: ids.slice(-SCAN_LIMIT) }).catch((e) => note(`mail teardown failed: ${e}`))
      for (const timer of timers) clearTimeout(timer)
    }
  },
}
