// nitipan server plugin for opencode.
//
// It marks a session while the service holds an execution claim for it, by calling nitipan. The
// mark ties to the service process, which the nitipan walk finds from this plugin's subprocess.
// nitipan reads the marks to tell whether an opencode session still runs.
//
// The two paths below are filled in when `nitipan setup opencode --install-plugins` writes this file.
import { spawn } from "node:child_process"
import { closeSync, constants, openSync, writeSync } from "node:fs"
import { join } from "node:path"

const NITIPAN: string = "@@NITIPAN@@"
const STATE_DIR: string = "@@STATEDIR@@"
const LOG = join(STATE_DIR, "plugin.log")
const RETRIES = 3
const RETRY_MS = 2000

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
  return event?.data?.sessionID ?? event?.sessionID ?? event?.properties?.sessionID
}

export default {
  id: "nitipan.server",
  async setup(ctx: any) {
    const controller = new AbortController()

    const watch = async () => {
      for await (const event of ctx.event.subscribe({ signal: controller.signal })) {
        const type: string = event?.type ?? ""
        if (!type.startsWith("session.execution.")) continue

        const id = sessionID(event)
        if (!id) continue

        hook("execution", { session_id: id, executing: type === "session.execution.started" })
      }
    }

    watch().catch((e) => {
      if (!controller.signal.aborted) note(`event stream ended: ${e}`)
    })

    return () => controller.abort()
  },
}
