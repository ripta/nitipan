import assert from "node:assert/strict"
import { EventEmitter } from "node:events"
import * as fs from "node:fs"
import * as module from "node:module"
import * as path from "node:path"
import vm from "node:vm"

if (typeof module.stripTypeScriptTypes !== "function") {
  console.log("SKIP: TypeScript stripping unavailable")
  process.exit(0)
}

const source = module.stripTypeScriptTypes(fs.readFileSync(process.argv[2], "utf8"))

async function trial(failInitially) {
  const intervals = []
  const prompted = []
  const calls = []
  let failures = failInitially ? 2 : 0
  const first = Array.from({ length: 200 }, (_, n) => ({ id: `recent-${n}` }))
  const target = "idle-recipient-on-later-page"
  const context = vm.createContext({
    AbortController, process: { pid: 1234 },
    setTimeout: () => ({}), clearTimeout: () => {},
    setInterval: (fn) => { intervals.push(fn); return fn }, clearInterval: () => {},
  })
  const spawn = (_exe, args) => {
    const child = new EventEmitter()
    child.stdout = new EventEmitter()
    child.stderr = new EventEmitter()
    child.stdin = new EventEmitter()
    child.stdin.end = (input) => {
      const request = JSON.parse(input)
      queueMicrotask(() => {
        if (args[1] === "mail-poll") {
          const rings = request.sessions.includes(target) ? [{ session_id: target, keys: ["mail:fixture"], ring: "mail waiting" }] : []
          child.stdout.emit("data", JSON.stringify(rings))
        }
        child.emit("close", 0)
      })
    }
    return child
  }
  const watcher = new EventEmitter()
  watcher.close = () => {}
  const imports = {
    "node:child_process": { spawn },
    "node:fs": { ...fs, watch: () => watcher },
    "node:path": path,
  }
  const plugin = new vm.SourceTextModule(source, { context })
  await plugin.link((name) => {
    const values = imports[name]
    assert.ok(values, `unexpected import ${name}`)
    return new vm.SyntheticModule(Object.keys(values), function () {
      for (const [key, value] of Object.entries(values)) this.setExport(key, value)
    }, { context })
  })
  await plugin.evaluate()
  const cleanup = await plugin.namespace.default.setup({
    session: {
      hook: async () => {},
      wait: async () => {},
      list: async (input) => {
        calls.push(input)
        if (failures > 0) { failures--; throw new Error("fixture initial list unavailable") }
        if (input.cursor === "older-page") return { data: [{ id: target }], cursor: {} }
        return { data: first, cursor: { next: "older-page" } }
      },
      prompt: async (input) => prompted.push(input.sessionID),
    },
    event: { subscribe: async function* () {} },
  })
  const flush = async () => { for (let n = 0; n < 100; n++) await Promise.resolve() }
  try {
    await flush()
    assert.ok(!prompted.includes(target), "later-page recipient must not be in the initial snapshot")
    for (let tick = 0; tick < 3; tick++) {
      const before = calls.length
      for (const callback of intervals) { callback(); callback() }
      await flush()
      assert.ok(calls.length - before <= 1, "one discovery page per timer tick bounds work")
    }
    assert.ok(prompted.includes(target), `idle recipient was never discovered after ${failInitially ? "initial failure" : "first page"}; cursors=${JSON.stringify(calls.map(c => c.cursor ?? null))}`)
    const lastPage = calls.findIndex(call => call.cursor === "older-page")
    assert.ok(calls.slice(lastPage + 1).some(call => call.cursor === undefined), "discovery restarts after the last page")
    console.log(`PASS: ${failInitially ? "failed discovery recovers" : "later page is reached"} without session events`)
  } finally {
    cleanup()
    await flush()
    const before = calls.length
    for (const callback of intervals) callback()
    await flush()
    assert.equal(calls.length, before, "teardown stops discovery even if a timer callback was already queued")
  }
}

await trial(process.argv[3] === "initial-failure")
