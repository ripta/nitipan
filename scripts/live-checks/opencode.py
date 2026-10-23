#!/usr/bin/env python3
"""Run the NIT-001 M4 opencode live checks unattended, in isolation, at no model cost.

Run it with `make live-opencode` from a plain terminal. Nothing needs to be closed first.

Isolation. The harness starts its own `opencode serve` on a free loopback port. That server, and
every TUI and `opencode run` the harness starts, uses private XDG config, data, state, and cache
directories under $TMPDIR. The user's opencode service, database, config, and plugins are never
read or written. nitipan's own state directory has no override by design, so the harness shares
it. It touches only what it creates there: leases it acquired, identified by recorded lease IDs,
and the session mark files of its own sessions and processes.

Zero model cost. The private config points at a fake OpenAI-compatible provider that the harness
serves on 127.0.0.1. A prompt carries a command, and the fake answers it with one scripted `shell`
tool call. opencode then runs the command in its real shell tool, with OPENCODE_SESSION_ID set and
the execution events firing, and the fake ends the turn. No real model is called.

The freshly built plugins (`make build`, then `setup opencode --install-plugins`) go into the
private config only. A test-only TUI plugin beside them opens, focuses, and closes tabs on command
through the TUI plugin API, so no keystrokes are needed.

Limit. nitipan reads opencode's database from the user's real path, with no override. A private
database is therefore invisible to it. The `time_suspended` and unreadable-database checks are
reported as skipped, and the unit tests cover them.

Results go to spec/reports/2026-10-22-nit-001-m4-opencode-harness-results.md, one row per check,
written as each check finishes. Raw events, TUI transcripts, and command output go to the work
directory. The exit status is 1 when any check fails or errors.

Several parts rest on reading opencode's source and have never run. `--probe` exercises just those
parts, prints what works, and exits. Run it first.
"""

from __future__ import annotations

import argparse
import base64
import json
import os
import re
import shlex
import shutil
import signal
import socket
import sqlite3
import subprocess
import sys
import threading
import time
import traceback
import urllib.error
import urllib.request
import uuid
from dataclasses import dataclass, field
from http.server import BaseHTTPRequestHandler
from pathlib import Path
from typing import Callable

import common
from common import (BIN, CLEAN_ENV, EXECUTING, OWN, OWN_PIDS, PRESENCE, RESULTS, ROOT, RUNID, SESSIONS,
                    TUIS, WORK, FakeServer, Interrupted, Out, Pty, check, children_of, done, lease_by,
                    leases, liveness, log, marks, nitipan, note, own, process_table, record,
                    release, run, scratch, wait_for, wrap)

RESULTS_PATH = ROOT / "spec/reports/2026-10-22-nit-001-m4-opencode-harness-results.md"

XDG = {name: WORK / "xdg" / kind for name, kind in (
    ("XDG_CONFIG_HOME", "config"), ("XDG_DATA_HOME", "data"),
    ("XDG_STATE_HOME", "state"), ("XDG_CACHE_HOME", "cache"))}
CONFIG_DIR = XDG["XDG_CONFIG_HOME"] / "opencode"
PLUGINS = CONFIG_DIR / "plugins"
TUI_TS = PLUGINS / "nitipan/tui.ts"
DB = XDG["XDG_DATA_HOME"] / "opencode/opencode.db"
PASSWORD = uuid.uuid4().hex

FAKE_PROVIDER = "fake"
FAKE_MODEL = "m"
MARKER = re.compile(r"HARNESS-CMD:([A-Za-z0-9+/=]+):END")

# The As built rows the checks feed. The coordinator matches these strings against the proposal.
R_ID = "opencode session ID and PID in a bash tool call"
R_SYSCTL = "Liveness sysctl inside each tool"
R_MANY = "MCP variable passing, and opencode sessions per process"
R_ENTRIES = "`acquire` with and without the printed entries"
R_MODES = "Permission mode lists"
R_PLUGINS = "Plugins shipped, `--install-plugins`, and printed steps"
R_TABS = "TUI plugin marks follow its tabs and route, survive reload"
R_EVENTS = "Server plugin sees events, including a resumed turn"
R_DEAD = "TUI dies mid-turn, `time_suspended`, bad DB"
R_RUNACP = "Presence ties to the TUI, `run` and `acp` safe"
R_NOTUI = "`acquire` with no TUI fails and says why"
R_SWEEP = "Sweep keeps what it must and removes dead files"

SCRIPTED_TURNS = 0
REAL_TURNS = 0
MAX_REAL_TURNS = 0


# The fake provider. reply() is pure so it can be tested without a socket.


def message_text(message: dict) -> str:
    content = message.get("content")
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        return "".join(p.get("text", "") for p in content if isinstance(p, dict))
    return ""


def reply(request: dict) -> dict:
    """Decide the fake's answer: a scripted shell tool call, or a plain `ok`."""
    messages = request.get("messages") or []
    tools = request.get("tools") or []
    names = [t.get("function", {}).get("name", "") for t in tools if isinstance(t, dict)]
    if messages and messages[-1].get("role") == "tool":
        return {"kind": "text", "text": "ok"}
    if not names:
        return {"kind": "text", "text": "ok"}
    users = [message_text(m) for m in messages if m.get("role") == "user"]
    found = MARKER.search(users[-1]) if users else None
    # A turn resumed after a server restart ends with a notice and no command. Repeat the last
    # command so the resumed turn stays in flight long enough to observe.
    if not found and users and "server restarted" in users[-1]:
        found = next((f for f in (MARKER.search(u) for u in reversed(users)) if f), None)
    if not found:
        return {"kind": "text", "text": "ok"}
    tool = next((n for n in names if n in ("shell", "bash")), None) or next(
        (n for n in names if "shell" in n or "bash" in n), "shell")
    command = base64.b64decode(found.group(1)).decode()
    return {"kind": "tool", "name": tool, "arguments": json.dumps({"command": command})}


def chunk(delta: dict, finish: str | None, usage: bool = False) -> str:
    body = {"id": "chatcmpl-fake", "object": "chat.completion.chunk", "created": int(time.time()),
            "model": FAKE_MODEL, "choices": [{"index": 0, "delta": delta, "finish_reason": finish}]}
    if usage:
        body["usage"] = {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
    return "data: " + json.dumps(body) + "\n\n"


def stream_body(answer: dict) -> bytes:
    parts = []
    if answer["kind"] == "tool":
        call = {"index": 0, "id": "call_" + uuid.uuid4().hex[:12], "type": "function",
                "function": {"name": answer["name"], "arguments": answer["arguments"]}}
        parts.append(chunk({"role": "assistant", "content": None, "tool_calls": [call]}, None))
        parts.append(chunk({}, "tool_calls", usage=True))
    else:
        parts.append(chunk({"role": "assistant", "content": answer["text"]}, None))
        parts.append(chunk({}, "stop", usage=True))
    parts.append("data: [DONE]\n\n")
    return "".join(parts).encode()


def json_body(answer: dict) -> bytes:
    if answer["kind"] == "tool":
        message = {"role": "assistant", "content": None, "tool_calls": [
            {"id": "call_" + uuid.uuid4().hex[:12], "type": "function",
             "function": {"name": answer["name"], "arguments": answer["arguments"]}}]}
        finish = "tool_calls"
    else:
        message = {"role": "assistant", "content": answer["text"]}
        finish = "stop"
    return json.dumps({"id": "chatcmpl-fake", "object": "chat.completion", "created": int(time.time()),
                       "model": FAKE_MODEL, "choices": [{"index": 0, "message": message, "finish_reason": finish}],
                       "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}}).encode()


class FakeHandler(BaseHTTPRequestHandler):
    def log_message(self, *args) -> None:
        pass

    def do_GET(self) -> None:
        body = json.dumps({"object": "list", "data": [{"id": FAKE_MODEL, "object": "model"}]}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self) -> None:
        raw = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        try:
            request = json.loads(raw)
        except ValueError:
            request = {}
        answer = reply(request)
        log("fake_request", path=self.path, tools=len(request.get("tools") or []), answer=answer["kind"],
            stream=bool(request.get("stream")))
        stream = bool(request.get("stream"))
        body = stream_body(answer) if stream else json_body(answer)
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream" if stream else "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


class Fake(FakeServer):
    def __init__(self):
        super().__init__(FakeHandler)


FAKE: Fake | None = None


# The private opencode: environment, config, server, and HTTP.


def oc_env() -> dict[str, str]:
    env = dict(CLEAN_ENV)
    env.update({k: str(v) for k, v in XDG.items()})
    env.update(OPENCODE_DISABLE_MODELS_FETCH="1", OPENCODE_DISABLE_PROJECT_CONFIG="1",
               OPENCODE_DISABLE_CLAUDE_CODE="1", OPENCODE_SERVER_PASSWORD=PASSWORD)
    return env


def write_config() -> None:
    for path in XDG.values():
        path.mkdir(parents=True, exist_ok=True)
    PLUGINS.mkdir(parents=True, exist_ok=True)
    config = {
        "$schema": "https://opencode.ai/config.json",
        "model": f"{FAKE_PROVIDER}/{FAKE_MODEL}",
        "update": "disable",
        "share": "disabled",
        "snapshots": False,
        "permissions": [{"action": "*", "resource": "*", "effect": "allow"}],
        "providers": {FAKE_PROVIDER: {
            "name": "fake",
            "package": "@opencode/ai/providers/openai-compatible",
            "settings": {"baseURL": f"http://127.0.0.1:{FAKE.port}/v1", "apiKey": "fake"},
            "models": {FAKE_MODEL: {"name": FAKE_MODEL, "limit": {"context": 100000, "output": 4096}}},
        }},
    }
    (CONFIG_DIR / "opencode.json").write_text(json.dumps(config, indent=2))


DRIVER_TUI = """\
// Test-only TUI plugin. It runs a command file line by line and appends one result line per command.
import { appendFileSync, readFileSync } from "node:fs"

export default {
  id: "harness.driver",
  setup(ctx: any) {
    const dir = process.env.NITIPAN_HARNESS_DRIVER
    if (!dir) return
    const cmds = dir + "/cmd.jsonl"
    const acks = dir + "/ack.jsonl"
    let seen = 0

    const run = (c: any) => {
      switch (c.op) {
        case "open": return { ok: ctx.ui.tabs.open(c.sessionID) }
        case "focus": return { ok: ctx.ui.tabs.focus(c.sessionID) }
        case "close": return { ok: ctx.ui.tabs.close(c.sessionID) }
        case "home": ctx.ui.router.navigate({ type: "home" }); return { ok: true }
        case "state": return { ok: true, tabs: [...ctx.ui.tabs.list()].map((t: any) => t.sessionID),
                               route: ctx.ui.router.current(), enabled: ctx.ui.tabs.enabled() }
        default: return { ok: false, error: "unknown op " + c.op }
      }
    }

    appendFileSync(acks, JSON.stringify({ n: 0, ready: true }) + "\\n")

    const timer = setInterval(() => {
      let lines: string[] = []
      try { lines = readFileSync(cmds, "utf8").split("\\n").filter(Boolean) } catch { return }
      while (seen < lines.length) {
        const line = lines[seen++]
        let result: any
        try { result = run(JSON.parse(line)) } catch (e) { result = { ok: false, error: String(e) } }
        appendFileSync(acks, JSON.stringify({ n: seen, ...result }) + "\\n")
      }
    }, 300)

    return () => clearInterval(timer)
  },
}
"""

DRIVER_INDEX = """\
export default {
  id: "harness.driver.server",
  async setup() {},
}
"""

DRIVER_PACKAGE = {
    "name": "nitipan-harness-driver", "version": "0.0.0", "private": True, "type": "module",
    "exports": {".": "./index.ts", "./tui": "./tui.ts"},
}


def install_plugins() -> subprocess.CompletedProcess:
    r = run([str(BIN), "setup", "opencode", "--install-plugins", str(PLUGINS), "--replace"])
    driver = PLUGINS / "harness-driver"
    driver.mkdir(parents=True, exist_ok=True)
    (driver / "tui.ts").write_text(DRIVER_TUI)
    (driver / "index.ts").write_text(DRIVER_INDEX)
    (driver / "package.json").write_text(json.dumps(DRIVER_PACKAGE, indent=2))
    return r


class Server:
    """The private `opencode serve`. Its PID is the shared service as far as nitipan can tell."""

    def __init__(self):
        sock = socket.socket()
        sock.bind(("127.0.0.1", 0))
        self.port = sock.getsockname()[1]
        sock.close()
        self.proc: subprocess.Popen | None = None
        self.starts = 0
        self.why = ""

    @property
    def url(self) -> str:
        return f"http://127.0.0.1:{self.port}"

    @property
    def pid(self) -> int | None:
        return self.proc.pid if self.proc and self.proc.poll() is None else None

    def start(self, service: bool = False) -> bool:
        """Only a server started with --service is registered, and only a registered one resumes
        turns orphaned by a restart."""
        self.starts += 1
        self.why = ""
        out = (WORK / f"serve-{self.starts}.log").open("w")
        flags = ["--service"] if service else []
        if service:
            # A service-mode server ignores OPENCODE_SERVER_PASSWORD. It takes its password from the
            # service config, and writes a random one there when the file has none.
            (CONFIG_DIR / "service.json").write_text(json.dumps({"password": PASSWORD}))
        self.proc = subprocess.Popen(
            ["opencode", "serve", *flags, "--hostname", "127.0.0.1", "--port", str(self.port)],
            cwd=WORK, env=oc_env(), stdout=out, stderr=subprocess.STDOUT, start_new_session=True)
        OWN_PIDS.add(self.proc.pid)
        log("serve_start", pid=self.proc.pid, port=self.port)

        def ready() -> bool:
            if self.proc.poll() is not None:
                raise RuntimeError(f"opencode serve exited {self.proc.returncode}: "
                                   + (WORK / f"serve-{self.starts}.log").read_text()[-600:])
            status = http("GET", "/api/session")[0]
            if status in (401, 403):
                self.why = f"the server is listening but rejected the harness password ({status})"
            return status == 200

        try:
            return bool(wait_for(ready, 60, 1))
        except OSError:
            return False

    def stop(self, sig: int = signal.SIGTERM) -> None:
        if not self.proc or self.proc.poll() is not None:
            return
        self.proc.send_signal(sig)
        try:
            self.proc.wait(timeout=20)
        except subprocess.TimeoutExpired:
            self.proc.kill()
            self.proc.wait(timeout=10)
        log("serve_stop", pid=self.proc.pid)


SERVER: Server | None = None


def http(method: str, path: str, body: dict | None = None, timeout: float = 30) -> tuple[int, object]:
    token = base64.b64encode(f"opencode:{PASSWORD}".encode()).decode()
    req = urllib.request.Request(
        SERVER.url + path, method=method, data=None if body is None else json.dumps(body).encode(),
        headers={"Content-Type": "application/json", "Authorization": "Basic " + token})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            status, raw = r.status, r.read()
    except urllib.error.HTTPError as e:
        status, raw = e.code, e.read()
    except (urllib.error.URLError, OSError, TimeoutError):
        return 0, None
    try:
        return status, json.loads(raw) if raw.strip() else {}
    except ValueError:
        return status, raw.decode(errors="replace")


# The private database.


def db(sql: str, args: tuple = ()) -> list[tuple] | None:
    try:
        con = sqlite3.connect(f"file:{DB}?mode=ro", uri=True, timeout=5)
        try:
            return con.execute(sql, args).fetchall()
        finally:
            con.close()
    except sqlite3.Error as e:
        log("db_error", sql=sql, error=str(e))
        return None


def is_active(session: str) -> bool:
    status, body = http("GET", "/api/session/active")
    return status == 200 and session in json.dumps(body)


def wait_idle(session: str, timeout: float = 120) -> bool:
    """True once the session has run and stopped. A turn that finished before the first look counts."""
    wait_for(lambda: is_active(session), 10, 0.3)
    return bool(wait_for(lambda: not is_active(session), timeout, 1))


# Sessions and scripted turns.


def prompt_text(cmd: str) -> str:
    """The fake reads the encoded command. A real model would read the plain one."""
    encoded = base64.b64encode(cmd.encode()).decode()
    return (f"Run this command exactly once with your shell tool, then reply ok. Command: {cmd}\n"
            f"HARNESS-CMD:{encoded}:END")


def new_session(title: str, directory: Path, parent: str | None = None) -> str:
    body = {"title": f"{RUNID} {title}", "location": {"directory": str(directory)},
            "model": {"id": FAKE_MODEL, "providerID": FAKE_PROVIDER}}
    if parent:
        body["parentID"] = parent
    status, reply_body = http("POST", "/api/session", body)
    if status != 200 or not isinstance(reply_body, dict) or "data" not in reply_body:
        raise RuntimeError(f"session.create failed: {status} {reply_body}")
    sid = reply_body["data"]["id"]
    SESSIONS.add(sid)
    log("session", title=title, session=sid, directory=str(directory), parent=parent)
    return sid


def start_turn() -> None:
    global SCRIPTED_TURNS
    SCRIPTED_TURNS += 1
    log("scripted_turn", number=SCRIPTED_TURNS)


def send(session: str, cmd: str) -> None:
    start_turn()
    status, body = http("POST", f"/api/session/{session}/prompt", {"text": prompt_text(cmd)})
    if status != 200:
        raise RuntimeError(f"session.prompt failed: {status} {body}")


def sh(session: str, cmd: str, label: str, timeout: float = 90) -> Out:
    """Run cmd in the session's shell tool, through a scripted turn. Returns the captured output."""
    out = WORK / "out" / f"{label}.out"
    out.parent.mkdir(exist_ok=True)
    out.unlink(missing_ok=True)
    send(session, wrap(cmd, out))
    wait_for(lambda: done(out), timeout)
    result = Out(out)
    log("sh", session=session, label=label, cmd=cmd, exit=result.exit, output=result.text)
    return result


def hold(session: str, seconds: int) -> None:
    """A scripted turn that keeps the session executing for about `seconds`."""
    send(session, f"sleep {seconds}")


def pump_permissions(stop: threading.Event) -> None:
    """Answer any permission request the config rule did not already allow."""
    while not stop.is_set():
        for session in list(SESSIONS):
            status, body = http("GET", f"/api/session/{session}/permission", timeout=5)
            items = body.get("data") if status == 200 and isinstance(body, dict) else None
            for item in items or []:
                rid = item.get("id") if isinstance(item, dict) else None
                if rid:
                    http("POST", f"/api/session/{session}/permission/{rid}/reply", {"reply": "once"}, timeout=5)
                    log("permission_reply", session=session, request=rid)
        stop.wait(1.0)


# TUIs.


class TUI(Pty):
    """An opencode TUI in a pseudo-terminal, driven through the test-only driver plugin."""

    def __init__(self, label: str, args: list[str], cwd: Path):
        self.driver = WORK / "driver" / label
        self.driver.mkdir(parents=True, exist_ok=True)
        env = oc_env()
        env["NITIPAN_HARNESS_DRIVER"] = str(self.driver)
        env["TERM"] = "xterm-256color"
        self.commands = 0
        super().__init__(label, ["opencode", *args], cwd, env)

    def acks(self) -> list[dict]:
        try:
            lines = (self.driver / "ack.jsonl").read_text().splitlines()
        except OSError:
            return []
        out = []
        for line in lines:
            try:
                out.append(json.loads(line))
            except ValueError:
                pass
        return out

    def ready(self, timeout: float = 40) -> bool:
        return bool(wait_for(lambda: any(a.get("ready") for a in self.acks()), timeout))

    def drive(self, op: str, session: str = "", timeout: float = 15) -> dict:
        self.commands += 1
        n = self.commands
        with (self.driver / "cmd.jsonl").open("a") as f:
            f.write(json.dumps({"op": op, "sessionID": session}) + "\n")
        found = wait_for(lambda: next((a for a in self.acks() if a.get("n") == n), None), timeout)
        log("tui_drive", label=self.label, op=op, session=session, ack=found)
        return found or {"ok": False, "error": "no ack"}

    def session_marks(self) -> set[str]:
        found = set()
        try:
            names = os.listdir(PRESENCE)
        except OSError:
            return found
        for name in names:
            parts = name.split(".")
            if len(parts) >= 4 and parts[2] == str(self.pid):
                found.add(parts[1])
        return found


def open_tui(label: str, session: str, cwd: Path) -> TUI | None:
    tui = TUI(label, ["--server", SERVER.url, "-s", session], cwd)
    if not tui.ready():
        log("tui_not_ready", label=label, reason="driver plugin never answered", tail=tui.tail())
        return None
    if wait_for(lambda: tui.pid in marks(PRESENCE, session), 25):
        return tui
    log("tui_not_ready", label=label, reason="no presence mark", tail=tui.tail())
    return None


# Lease bookkeeping. Only leases recorded here are ever released or broken.


def acquire(step: str, row: str, session: str, directory: Path, label: str, extra: str = ""):
    desc = f"harness {RUNID} {label}"
    out = sh(session, f"{BIN} acquire --checkout {directory} --description {shlex.quote(desc)}{extra}", label)
    lease = lease_by(description=desc)
    if lease:
        own(lease, session, "turn")
    log("acquire", step=step, row=row, label=label, exit=out.exit, lease=lease)
    return out, lease


def release_in_turn(info: dict, lease_id: str) -> bool:
    """Release through the holder's own session while the shared server is up."""
    if not (SERVER and SERVER.pid and info["session"] in SESSIONS):
        return False
    out = sh(info["session"], f"{BIN} release {lease_id}", f"release-{lease_id}", timeout=40)
    return out.exit == 0


common.TURN_RELEASE = release_in_turn


# Steps. Each records its own checks. An exception becomes one ERROR row and the run goes on.


def step_install() -> None:
    r = run(["make", "build"], timeout=600)
    check("0", R_PLUGINS, "make build", r.returncode == 0, r.stderr.strip()[-200:])
    if r.returncode != 0:
        raise RuntimeError("make build failed; nothing below would test the current code")
    global FAKE, SERVER
    FAKE = Fake()
    FAKE.start()
    write_config()
    r = install_plugins()
    check("0", R_PLUGINS, "setup opencode --install-plugins (private config)", r.returncode == 0,
          (r.stdout + r.stderr).strip()[-300:])
    SERVER = Server()
    up = SERVER.start()
    check("0", R_PLUGINS, "private opencode serve starts", up, f"port {SERVER.port}")
    if not up:
        raise RuntimeError("the private server never came up; every later check needs it")
    OWN_PIDS.update(children_of(SERVER.pid))
    threading.Thread(target=pump_permissions, args=(PERMISSION_STOP,), daemon=True).start()


PERMISSION_STOP = threading.Event()


def step_plugins() -> None:
    files = [p.name for p in (PLUGINS / "nitipan").glob("*")]
    check("0", R_PLUGINS, "plugin files installed", {"index.ts", "tui.ts", "package.json"} <= set(files),
          str(sorted(files)))
    # `opencode plugin list` takes no --server flag, and run bare it manages the user's own service.
    # The API inventory is scoped to a location and came back empty, so it is only noted. The proof
    # that the server plugin loaded is its effect: an execution mark under the server's PID.
    status, body = http("GET", "/api/plugin")
    note("13", R_PLUGINS, "plugin inventory from the API", f"{status} {json.dumps(body)[:200]}")
    session = new_session("plugin-proof", scratch("plugin-proof"))
    hold(session, 4)
    seen = wait_for(lambda: SERVER.pid in marks(EXECUTING, session), 20)
    check("13", R_PLUGINS, "server plugin loaded: its execution mark appears under the server PID",
          bool(seen), f"mark pids {marks(EXECUTING, session)}, server {SERVER.pid}")
    wait_idle(session, 30)


def step_headless(step: str, label: str) -> None:
    """acquire from a session that no TUI has open must fail and name the TUI plugin."""
    directory = scratch(label)
    session = new_session(label, directory)
    out, lease = acquire(step, R_NOTUI, session, directory, label)
    ok = out.exit not in (0, None) and "TUI" in out.text and lease is None
    detail = out.text.strip().splitlines()[-1] if out.text.strip() else repr(out)
    if lease:
        detail = f"acquired; liveness {lease['liveness']}"
    check(step, R_NOTUI, "acquire with no TUI on the session fails and names the TUI plugin", ok, detail)
    if lease:
        release(lease["id"])


def step_run() -> None:
    directory = scratch("run")
    title = f"{RUNID} opencode run"
    out = WORK / "out" / "run-acquire.out"
    out.parent.mkdir(exist_ok=True)
    out.unlink(missing_ok=True)
    acquire_cmd = f"{BIN} acquire --checkout {directory} --description 'harness {RUNID} run'"
    cmd = wrap(f'{acquire_cmd}; echo "acquire-exit=$?"; sleep 12', out)
    start_turn()
    proc = subprocess.Popen(
        ["opencode", "run", "--server", SERVER.url, "--title", title, prompt_text(cmd)],
        cwd=directory, env=oc_env(), stdout=(WORK / "run.log").open("w"), stderr=subprocess.STDOUT)
    session = None
    presence, executing = set(), set()
    try:
        session = wait_for(lambda: (db("select id from session_v2 where title=?", (title,)) or [[None]])[0][0], 60)
        if session:
            SESSIONS.add(session)
        until = time.monotonic() + 120
        while proc.poll() is None and time.monotonic() < until:
            if session:
                presence.update(marks(PRESENCE, session))
                executing.update(marks(EXECUTING, session))
            time.sleep(0.5)
    finally:
        if proc.poll() is None:
            proc.terminate()
        proc.wait(timeout=30)
    check("10", R_RUNACP, "opencode run session gets no presence mark", bool(session) and not presence,
          f"presence pids {sorted(presence)}")
    check("10", R_RUNACP, "opencode run turn has an execution mark under the server",
          SERVER.pid in executing, f"mark pids {sorted(executing)}, server {SERVER.pid}")
    check("8", R_EVENTS, "execution mark removed when the opencode run turn ends",
          bool(session) and bool(wait_for(lambda: not marks(EXECUTING, session), 10)))
    result = Out(out)
    lease = lease_by(description=f"harness {RUNID} run")
    # The group's own exit status is the trailing sleep's, so acquire's is echoed separately.
    found = re.search(r"acquire-exit=(\d+)", result.text)
    refused = bool(found) and found.group(1) != "0"
    check("2", R_NOTUI, "acquire inside opencode run fails with no TUI",
          refused and lease is None and "TUI" in result.text, repr(result))
    if lease:
        own(lease, session or "", "turn")
        release(lease["id"])


def step_acp() -> None:
    directory = scratch("acp")
    proc = subprocess.Popen(
        ["opencode", "acp"], cwd=directory, env=oc_env(), text=True, bufsize=1,
        stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
    OWN_PIDS.add(proc.pid)
    replies: dict[int, dict] = {}
    cv = threading.Condition()

    def put(msg: dict) -> None:
        proc.stdin.write(json.dumps(msg) + "\n")
        proc.stdin.flush()

    def reader() -> None:
        for line in proc.stdout:
            try:
                msg = json.loads(line)
            except ValueError:
                continue
            if "method" in msg and "id" in msg:
                options = msg.get("params", {}).get("options", [])
                pick = next((o for o in options if o.get("kind", "").startswith("allow")), None)
                result = {"outcome": {"outcome": "selected", "optionId": pick["optionId"]}} if pick else {}
                put({"jsonrpc": "2.0", "id": msg["id"], "result": result})
            elif "id" in msg:
                with cv:
                    replies[msg["id"]] = msg
                    cv.notify_all()

    def call(i: int, method: str, params: dict, timeout: float = 120) -> dict:
        put({"jsonrpc": "2.0", "id": i, "method": method, "params": params})
        with cv:
            cv.wait_for(lambda: i in replies, timeout)
        return replies.get(i, {})

    threading.Thread(target=reader, daemon=True).start()
    try:
        call(1, "initialize", {"protocolVersion": 1, "clientCapabilities": {}}, 60)
        session = call(2, "session/new", {"cwd": str(directory), "mcpServers": []}, 60).get("result", {}).get("sessionId")
        if not session:
            check("10", R_RUNACP, "opencode acp session gets no presence mark", None, "acp gave no session")
            return
        SESSIONS.add(session)
        presence, executing = set(), set()
        finished = threading.Event()

        def watch() -> None:
            while not finished.is_set():
                presence.update(marks(PRESENCE, session))
                executing.update(marks(EXECUTING, session))
                time.sleep(0.5)

        threading.Thread(target=watch, daemon=True).start()
        start_turn()
        answer = call(3, "session/prompt", {"sessionId": session, "prompt": [{"type": "text", "text": prompt_text("sleep 8")}]})
        finished.set()
        log("acp", session=session, reply=answer, presence=sorted(presence), executing=sorted(executing))
        check("10", R_RUNACP, "opencode acp session gets no presence mark", not presence, f"presence pids {sorted(presence)}")
        check("10", R_RUNACP, "acp turn has an execution mark from its own server",
              bool(executing) and SERVER.pid not in executing, f"mark pids {sorted(executing)}, server {SERVER.pid}")
        check("8", R_SWEEP, "acp execution mark removed after the turn",
              bool(wait_for(lambda: not marks(EXECUTING, session), 10)))
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            proc.kill()


class Shared:
    """Sessions, TUIs, and leases the shared-server steps build on."""

    a = b = None
    tui_a = tui_b = None
    lease_a = lease_b = None
    dir_a = dir_b = None


S = Shared()


def step_identity() -> None:
    S.dir_a = scratch("a")
    S.a = new_session("a", S.dir_a)
    S.tui_a = open_tui("a", S.a, S.dir_a)
    check("13", R_TABS, "TUI renders under a pty and its plugin marks the session", S.tui_a is not None)
    if not S.tui_a:
        raise RuntimeError("TUI a never marked its session")
    env = sh(S.a, 'echo "$OPENCODE_SESSION_ID"', "a-env")
    check("1", R_ID, "OPENCODE_SESSION_ID in the shell tool is the session", env.text.strip() == S.a, env.text.strip())
    out, S.lease_a = acquire("1", R_ID, S.a, S.dir_a, "a")
    if not S.lease_a:
        check("1", R_ID, "acquire from a TUI session", False, repr(out))
        raise RuntimeError("acquire a failed")
    holder = S.lease_a["holder"]
    info = process_table().get(holder["pid"], (0, ""))
    check("1", R_ID, "holder.session_id is the session", holder["session_id"] == S.a, holder["session_id"])
    check("1", R_ID, "holder.pid is the server", holder["pid"] == SERVER.pid and "serve" in info[1],
          f"{holder['pid']}: {info[1]}")
    lv = S.lease_a["liveness"]
    check("1", R_SYSCTL, "liveness alive from this TUI's presence mark",
          lv["status"] == "alive" and str(S.tui_a.pid) in lv.get("reason", ""), json.dumps(lv))
    check("2", R_ENTRIES, "no permission prompt was needed", True, "the scripted turn ran unattended")


def step_headless_with_tui() -> None:
    step_headless("2", "headless-with-tui")


def step_permission_modes() -> None:
    directory = scratch("perm")
    before = lease_by(id=S.lease_a["id"]) or {}
    out, lease = acquire("12", R_MODES, S.a, directory, "perm-build", " --permission-mode build")
    check("12", R_MODES, "--permission-mode build succeeds", out.exit == 0 and lease is not None, repr(out))
    bad = sh(S.a, f"{BIN} acquire --checkout {directory}-x --permission-mode nonsense", "perm-nonsense")
    check("12", R_MODES, "--permission-mode nonsense fails listing build, plan, auto",
          bad.exit not in (0, None) and all(m in bad.text for m in ("build", "plan", "auto")), repr(bad))
    after = lease_by(id=S.lease_a["id"]) or {}
    check("12", R_MODES, "a mode on one acquire applies to the session's other leases as designed",
          after.get("permission_mode") == "build", f"{before.get('permission_mode')} -> {after.get('permission_mode')}")
    status, agents = http("GET", "/api/agent")
    names = sorted(a.get("id") for a in (agents.get("data", []) if isinstance(agents, dict) else [])
                   if a.get("mode") == "primary" and not a.get("hidden"))
    note("12", R_MODES, "opencode primary agents", f"{names}; plus the --auto flag")
    if lease:
        release(lease["id"])


def step_subagent() -> None:
    directory = scratch("sub")
    child = new_session("subagent", directory, parent=S.a)
    check("11", R_TABS, "child session's parent is the root",
          (db("select parent_id from session_v2 where id=?", (child,)) or [[None]])[0][0] == S.a)
    check("11", R_TABS, "child marked present under the root's TUI",
          bool(wait_for(lambda: S.tui_a.pid in marks(PRESENCE, child), 15)), f"marks for child: {marks(PRESENCE, child)}")
    out, lease = acquire("11", R_TABS, child, directory, "subagent")
    check("11", R_TABS, "child is its own holder", bool(lease) and lease["holder"]["session_id"] == child, repr(out))
    if lease:
        release(lease["id"])


def step_many_sessions() -> None:
    S.dir_b = scratch("b")
    S.b = new_session("b", S.dir_b)
    S.tui_b = open_tui("b", S.b, S.dir_b)
    if not S.tui_b:
        raise RuntimeError("TUI b never marked its session")
    out, S.lease_b = acquire("3", R_MANY, S.b, S.dir_b, "b")
    ok = bool(S.lease_b) and S.lease_b["holder"]["pid"] == S.lease_a["holder"]["pid"] and S.b != S.a
    check("3", R_MANY, "two sessions share one holder PID", ok,
          f"{S.lease_a['holder']['pid']} / {S.lease_b['holder']['pid'] if S.lease_b else repr(out)}")


def step_tabs() -> None:
    tui = S.tui_b
    extra = new_session("tab", scratch("tab"))
    ack = tui.drive("state")
    note("5", R_TABS, "tab API", f"tabs enabled: {ack.get('enabled')}; route {ack.get('route')}")
    tui.drive("open", extra)
    check("5", R_TABS, "a new tab's session is marked", bool(wait_for(lambda: extra in tui.session_marks(), 10)))
    time.sleep(3)
    check("5", R_TABS, "an idle tab still holds its mark", extra in tui.session_marks())
    tui.drive("close", extra)
    check("5", R_TABS, "closing the tab removes the mark", bool(wait_for(lambda: extra not in tui.session_marks(), 10)))
    check("5", R_TABS, "route without a tab", None, "the v2.1.0 TUI opens every session in a tab")

    running = new_session("running", scratch("running"))
    tui.drive("open", running)
    wait_for(lambda: running in tui.session_marks(), 10)
    tui.drive("home")
    hold(running, 20)
    check("8", R_EVENTS, "a TUI session's turn gets an execution mark under the server",
          bool(wait_for(lambda: SERVER.pid in marks(EXECUTING, running), 30)))
    tui.drive("close", running)
    time.sleep(4)
    others = {S.tui_a.pid} & set(marks(PRESENCE, running))
    check("5", R_TABS, "a running session loses its TUI mark when its tab closes",
          running not in tui.session_marks(), "running state is not a source")
    check("5", R_TABS, "the server's execution mark outlives the tab",
          SERVER.pid in marks(EXECUTING, running), str(marks(EXECUTING, running)))
    check("5", R_TABS, "TUIs without a tab for a session do not mark it", not others, f"marked by TUIs {sorted(others)}")
    wait_idle(running, 60)
    check("8", R_EVENTS, "the execution mark goes within 10 s of the turn ending",
          bool(wait_for(lambda: not marks(EXECUTING, running), 10)))


def step_hot_reload() -> None:
    original = TUI_TS.read_text()
    first, _, rest = original.partition("\n")
    TUI_TS.write_text(first + " reload\n" + rest)
    log("edit", path=str(TUI_TS), line1=first + " reload")
    bad = []
    for _ in range(12):
        time.sleep(1)
        if S.tui_a.pid not in marks(PRESENCE, S.a) or S.tui_b.pid not in marks(PRESENCE, S.b):
            bad.append("mark missing")
        for lease in (S.lease_a, S.lease_b):
            if liveness(lease["id"])["status"] != "alive":
                bad.append(f"{lease['id']} not alive")
    check("6", R_TABS, "marks and leases survive a hot reload", not bad, "; ".join(sorted(set(bad))))
    nitipan("setup", "opencode", "--install-plugins", str(PLUGINS), "--replace")
    check("6", R_PLUGINS, "setup --replace restores tui.ts", TUI_TS.read_text() == original)


def step_release_shared() -> None:
    for lease in (S.lease_a, S.lease_b):
        if lease:
            release(lease["id"])
    for tui in (S.tui_a, S.tui_b):
        if tui:
            tui.stop()


def step_standalone() -> None:
    directory = scratch("standalone")
    desc = f"harness {RUNID} standalone"
    out = WORK / "out" / "standalone.out"
    out.parent.mkdir(exist_ok=True)
    out.unlink(missing_ok=True)
    acq = wrap(f"{BIN} acquire --checkout {directory} --description {shlex.quote(desc)}", out)
    start_turn()
    tui = TUI("standalone", ["--standalone", "--prompt", prompt_text(f"{acq}; sleep 20")], directory)
    lease, session = None, ""
    try:
        if not wait_for(lambda: done(out), 90):
            check("7", R_RUNACP, "acquire from a standalone TUI", False, "the command never ran: " + tui.tail())
            return
        lease = lease_by(description=desc)
        if not lease:
            check("7", R_RUNACP, "acquire from a standalone TUI", False, repr(Out(out)))
            return
        session = lease["holder"]["session_id"]
        SESSIONS.add(session)
        own(lease, session, "break")
        child = lease["holder"]["pid"]
        OWN_PIDS.add(child)
        info = process_table().get(child, (0, ""))
        check("7", R_RUNACP, "holder.pid is the private serve child of the TUI",
              info[0] == tui.pid and "serve --stdio" in info[1], f"{child}: ppid {info[0]} {info[1]}")
        check("7", R_RUNACP, "presence mark names the TUI, not the child", marks(PRESENCE, session) == [tui.pid],
              f"{marks(PRESENCE, session)} vs TUI {tui.pid}")
        check("7", R_EVENTS, "execution mark names the serve child", child in marks(EXECUTING, session),
              str(marks(EXECUTING, session)))
        check("7", R_EVENTS, "execution mark goes when the turn ends",
              bool(wait_for(lambda: not marks(EXECUTING, session), 60)))
    finally:
        tui.stop()
    if lease:
        time.sleep(3)
        check("8", R_SWEEP, "no stale execution mark after a standalone TUI exits",
              not marks(EXECUTING, session), str(marks(EXECUTING, session)))
        release(lease["id"])


def held_session(label: str) -> tuple[str, TUI, dict]:
    """A session with a lease, acquired through its own TUI."""
    directory = scratch(label)
    session = new_session(label, directory)
    tui = open_tui(label, session, directory)
    if not tui:
        raise RuntimeError(f"TUI {label} never marked its session")
    out, lease = acquire("9", R_DEAD, session, directory, label)
    if not lease:
        raise RuntimeError(f"acquire {label} failed: {out!r}")
    return session, tui, lease


def step_dead_tui() -> None:
    session, tui, lease = held_session("killed")
    try:
        hold(session, 30)
        wait_for(lambda: marks(EXECUTING, session), 30)
        tui.kill()
        # An unreaped child stays in the process table as a zombie, and nitipan reads it as alive.
        wait_for(lambda: not tui.alive(), 5)
        samples = []
        ended_at = None
        while len(samples) < 40:
            lv = liveness(lease["id"])
            running = is_active(session)
            samples.append((running, lv))
            if not running and ended_at is None:
                ended_at = time.monotonic()
            if lv["status"] == "dead" or (ended_at and time.monotonic() - ended_at > 20):
                break
            time.sleep(2)
        log("samples", step="9", lease=lease["id"], samples=samples)
        during = [lv for running, lv in samples if running]
        check("9.2", R_DEAD, "reads alive while the turn runs after its TUI died",
              bool(during) and all(lv["status"] == "alive" for lv in during), json.dumps(during[-1:] or samples[:1]))
        note("9.2", R_DEAD, "liveness reason while the turn ran", "; ".join(sorted({lv.get("reason", "") for lv in during})))
        check("9.3", R_DEAD, "reads dead within 15 s of the turn ending",
              samples[-1][1]["status"] == "dead" and ended_at is not None and time.monotonic() - ended_at <= 15,
              json.dumps(samples[-1][1]))
    finally:
        tui.stop()
        release(lease["id"])


def step_restart() -> None:
    session = new_session("restart", scratch("restart"))
    hold(session, 40)
    wait_for(lambda: marks(EXECUTING, session), 30)
    time.sleep(3)
    old = SERVER.pid
    SERVER.stop(signal.SIGTERM)
    registered = SERVER.start(service=True)
    why = SERVER.why
    if not registered:
        SERVER.stop()
        up = SERVER.start()
    else:
        up = True
    OWN_PIDS.update(children_of(SERVER.pid) if SERVER.pid else [])
    check("8.3", R_EVENTS, "the server restarts on the same port and data", up and SERVER.pid != old,
          f"{old} -> {SERVER.pid}; started {'with' if registered else 'without'} --service")
    if not registered:
        check("8.3", R_EVENTS, "the turn resumes after the restart", None,
              "skipped: the registered (--service) server did not come up: " + (why or "see serve-N.log in the work directory")
              + ". Only a registered server resumes orphaned turns")
        return
    seen: set[int] = set()
    resumed = False
    deadline = time.monotonic() + 60
    while time.monotonic() < deadline:
        seen.update(marks(EXECUTING, session))
        rows = db("select count(*) from session_message where session_id=? and type='synthetic' "
                  "and data like '%server restarted%'", (session,))
        resumed = resumed or bool(rows and rows[0][0])
        if resumed and SERVER.pid in seen:
            break
        time.sleep(1)
    note("8.3", R_EVENTS, "execution marks seen after the restart", f"{sorted(seen)}; new server {SERVER.pid}")
    check("8.3", R_EVENTS, "the turn resumes after the restart", resumed or is_active(session))
    check("8.3", R_EVENTS, "the resumed turn has an execution mark under the new server", SERVER.pid in seen,
          f"mark pids seen {sorted(seen)}, new server {SERVER.pid}")
    wait_idle(session, 90)


def step_database() -> None:
    check("9.4", R_DEAD, "reads unknown while time_suspended is set", None,
          "skipped: nitipan reads the real opencode.db path, which a private run cannot reach; covered by unit tests")
    check("9.5", R_DEAD, "an unreadable database reads unknown with a hint naming it", None,
          "skipped: same reason, and the real database is never touched")


# The plan. It is the single list the dry run prints and the driver walks.


@dataclass
class Step:
    name: str
    fn: Callable[[], None]
    what: str
    covers: list[tuple[str, str]] = field(default_factory=list)
    always: bool = False


STEPS = [
    Step("install", step_install, "make build, private config and fake provider, install plugins, start the server",
         [("0", R_PLUGINS)], always=True),
    Step("plugins", step_plugins, "installed files and `opencode plugin list`", [("0", R_PLUGINS), ("13", R_PLUGINS)]),
    Step("headless", lambda: step_headless("2", "headless"), "acquire from a session no TUI has open fails",
         [("2", R_NOTUI)]),
    Step("run", step_run, "`opencode run` turn: no presence mark, execution mark",
         [("10", R_RUNACP), ("8", R_EVENTS), ("2", R_NOTUI)]),
    Step("acp", step_acp, "`opencode acp` turn: no presence mark, own server's mark", [("10", R_RUNACP), ("8", R_SWEEP)]),
    Step("identity", step_identity, "TUI a, session variable, acquire, holder, liveness",
         [("1", R_ID), ("1", R_SYSCTL), ("2", R_ENTRIES), ("13", R_TABS)]),
    Step("headless-tui", step_headless_with_tui, "a session without a TUI still fails while TUIs run", [("2", R_NOTUI)]),
    Step("permission", step_permission_modes, "permission mode build and nonsense", [("12", R_MODES)]),
    Step("subagent", step_subagent, "child session with a parent: its mark and holder", [("11", R_TABS)]),
    Step("sessions", step_many_sessions, "TUI b, a second session on the same server PID", [("3", R_MANY)]),
    Step("tabs", step_tabs, "open and close tabs through the driver, a running session's tab", [("5", R_TABS), ("8", R_EVENTS)]),
    Step("reload", step_hot_reload, "edit the private tui.ts line 1, watch marks, restore",
         [("6", R_TABS), ("6", R_PLUGINS)]),
    Step("release-shared", step_release_shared, "release leases a and b, stop TUIs a and b", [], always=True),
    Step("standalone", step_standalone, "`opencode --standalone` TUI turn: holder, marks",
         [("7", R_RUNACP), ("7", R_EVENTS), ("8", R_SWEEP)]),
    Step("dead-tui", step_dead_tui, "kill a TUI mid-turn, sample liveness", [("9.2", R_DEAD), ("9.3", R_DEAD)]),
    Step("restart", step_restart, "SIGTERM the server mid-turn, start it again, watch the resumed turn", [("8.3", R_EVENTS)]),
    Step("database", step_database, "time_suspended and unreadable database: skipped, with the reason",
         [("9.4", R_DEAD), ("9.5", R_DEAD)]),
]


def selected(only: set[str] | None) -> list[Step]:
    return [s for s in STEPS if s.always or not only or s.name in only]


def print_plan(opts: argparse.Namespace, only: set[str] | None) -> None:
    print("nitipan opencode live checks: plan (dry run, nothing touched)\n")
    print(f"results: {opts.report or RESULTS_PATH}\nwork directory: {WORK} (created when the run starts)\n")
    print(f"{'step':14}  what (handoff step -> As built row)")
    for s in selected(only):
        print(f"{s.name:14}  {s.what}")
        for handoff, row in s.covers:
            print(f"{'':16}  {handoff:5} -> {row}")
    print(f"\nreal model turns: 0 planned, cap {opts.max_model_turns}. Estimated model cost: $0.00.")
    print("Every turn is answered by a fake provider that the harness serves on 127.0.0.1.")
    print("\nisolation:")
    print("  - a private `opencode serve` on a free loopback port, with XDG config, data, state, and cache")
    print(f"    under {WORK / 'xdg'}, and a random server password")
    print("  - the user's opencode service, database, config, and plugins are never read or written")
    print("  - plugins from `make build` are installed into the private config only")
    print("  - nitipan's state directory is the real one. Only leases acquired here are released, by recorded")
    print("    ID, and only mark files named by this run's own sessions and processes are removed")
    print("  - the user does not need to close opencode")


# Driver.


def apply_cleanup() -> list[str]:
    """Undo everything the run created. Each part runs even when an earlier one fails."""
    common.CLEANING = True
    PERMISSION_STOP.set()
    common.release_own(BASELINE)
    for tui in TUIS:
        try:
            tui.stop()
        except Exception as e:
            log("cleanup_error", tui=tui.label, error=repr(e))
    if SERVER:
        try:
            SERVER.stop()
        except Exception as e:
            log("cleanup_error", what="server", error=repr(e))
    if FAKE:
        try:
            FAKE.stop()
        except Exception as e:
            log("cleanup_error", what="fake provider", error=repr(e))
    sweep_marks()
    return [x["id"] for x in leases() if x["id"] in OWN]


def sweep_marks() -> None:
    common.sweep_marks((PRESENCE, EXECUTING))


BASELINE: list[str] = []


def preflight() -> list[str]:
    problems = []
    if any(k in os.environ for k in ("OPENCODE_SESSION_ID", "OPENCODE")):
        problems.append("run this from a plain terminal, not from inside opencode")
    for tool in ("opencode", "make", "sqlite3"):
        if shutil.which(tool) is None:
            problems.append(f"{tool} is not on PATH")
    return problems


def open_results() -> None:
    version = subprocess.run(["opencode", "--version"], capture_output=True, text=True).stdout.strip()
    macos = subprocess.run(["sw_vers", "-productVersion"], capture_output=True, text=True).stdout.strip()
    common.open_sink(RESULTS_PATH, [
        "# NIT-001 M4: opencode live results (automated harness)",
        "",
        f"- Started {time.strftime('%Y-%m-%d %H:%M:%S')}; run `{RUNID}`; work directory `{WORK}`",
        f"- opencode {version}; macOS {macos}; private server, fake provider, no real model",
        "- Rows are written as each check finishes. A run that stops early leaves the rows so far, and no totals.",
        "",
        "| Handoff step | As built row | Check | Result | Detail |",
        "|---|---|---|---|---|",
        "",
    ])


def close_results(leftover: list[str]) -> None:
    counts = common.totals()
    common.close_sink([
        "",
        f"Totals: {', '.join(f'{v} {k}' for k, v in counts.items())}.",
        f"Real model turns: {REAL_TURNS}. Scripted turns answered by the fake provider: {SCRIPTED_TURNS}.",
        f"Leases at start: {BASELINE or 'none'}. Left behind by this run: {leftover or 'none'}.",
        f"Raw events: `{WORK / 'events.jsonl'}`. TUI transcripts and command output are beside it.",
        "",
    ])


# Probe: exercise only the parts that rest on source reading.


def probe() -> int:
    global SERVER, FAKE
    results = []

    def step(name: str, ok: bool, detail: str = "") -> bool:
        results.append((name, ok, detail))
        print(f"  {'OK  ' if ok else 'FAIL'} {name}" + (f" -- {detail}" if detail else ""), flush=True)
        return ok

    print(f"probe in {WORK}", flush=True)
    common.open_events()
    try:
        r = run(["make", "build"], timeout=600)
        step("make build", r.returncode == 0, r.stderr.strip()[-200:])
        FAKE = Fake()
        FAKE.start()
        write_config()
        r = install_plugins()
        step("plugins installed into the private config", r.returncode == 0, (r.stdout + r.stderr)[-200:])
        SERVER = Server()
        up = SERVER.start()
        step("private opencode serve starts with the fake provider config", up, f"port {SERVER.port}")
        if not up:
            return 1
        threading.Thread(target=pump_permissions, args=(PERMISSION_STOP,), daemon=True).start()
        directory = scratch("probe")
        try:
            session = new_session("probe", directory)
            step("session.create accepts location and model", True, session)
        except RuntimeError as e:
            step("session.create accepts location and model", False, str(e))
            return 1
        status, body = http("GET", "/api/session/active")
        step("session.active answers", status == 200, f"{status} {json.dumps(body)[:120]}")
        out = sh(session, 'echo "$OPENCODE_SESSION_ID $AGENT $OPENCODE_TERMINAL"', "probe-env", timeout=60)
        step("the scripted shell tool call runs with OPENCODE_SESSION_ID set", out.text.startswith(session), repr(out))
        status, body = http("GET", f"/api/session/{session}/permission")
        step("permission list answers", status == 200, f"{status} {json.dumps(body)[:120]}")
        hold(session, 6)
        step("a held turn shows the session as active", bool(wait_for(lambda: is_active(session), 10, 0.3)))
        step("an execution mark appears and goes",
             bool(wait_for(lambda: marks(EXECUTING, session), 10)) and wait_idle(session, 30)
             and bool(wait_for(lambda: not marks(EXECUTING, session), 10)))
        tui = TUI("probe", ["--server", SERVER.url, "-s", session], directory)
        step("the TUI starts under a pty and the driver plugin answers", tui.ready(40), tui.tail())
        step("the nitipan TUI plugin marks the session", bool(wait_for(lambda: tui.pid in marks(PRESENCE, session), 20)))
        if tui.ready(1):
            other = new_session("probe-tab", directory)
            tui.drive("open", other)
            step("tabs.open marks a second session",
                 bool(wait_for(lambda: other in tui.session_marks(), 10)), json.dumps(tui.drive("state")))
        tui.stop()
        print("\nprobe finished", flush=True)
        return 0 if all(ok for _, ok, _ in results) else 1
    finally:
        common.CLEANING = True
        PERMISSION_STOP.set()
        for t in TUIS:
            t.stop()
        if SERVER:
            SERVER.stop()
        if FAKE:
            FAKE.stop()
        sweep_marks()


def main() -> int:
    global MAX_REAL_TURNS, BASELINE
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--max-model-turns", type=int, default=0,
                        help="hard cap on real model turns. The harness uses none, so the default is 0")
    parser.add_argument("--only", help="comma-separated step names to run: " + ",".join(s.name for s in STEPS))
    parser.add_argument("--report", type=Path, help="also copy the results here when the run ends")
    parser.add_argument("--dry-run", action="store_true", help="print the plan and exit; touch nothing")
    parser.add_argument("--probe", action="store_true",
                        help="check the parts that rest on source reading (private server, fake provider, "
                             "TUI under a pty) and exit")
    opts = parser.parse_args()
    only = set(opts.only.split(",")) if opts.only else None
    if only and only - {s.name for s in STEPS}:
        parser.error(f"unknown step in --only: {sorted(only - {s.name for s in STEPS})}")

    if opts.dry_run:
        print_plan(opts, only)
        return 0

    problems = preflight()
    if problems:
        print("nitipan opencode live checks cannot start:\n  - " + "\n  - ".join(problems), file=sys.stderr)
        return 2
    MAX_REAL_TURNS = opts.max_model_turns
    common.install_signal_handlers()

    if opts.probe:
        try:
            return probe()
        except Interrupted as e:
            print(f"interrupted by {e}", file=sys.stderr)
            return 130

    common.open_events()
    open_results()
    print(f"work directory: {WORK}\nresults: {RESULTS_PATH}", flush=True)
    log("run_start", run=RUNID, argv=sys.argv)

    leftover: list[str] = []
    try:
        BASELINE = [x["id"] for x in leases()]
        log("baseline", leases=BASELINE)
        for s in selected(only):
            print(f"== {s.name}", flush=True)
            try:
                s.fn()
            except Exception as e:
                record(s.name, "-", "step raised", "ERROR", repr(e))
                log("error", step=s.name, error=repr(e), trace=traceback.format_exc())
                if s.name == "install":
                    break
    except Interrupted as e:
        record("-", "-", "interrupted", "ERROR", f"signal {e}; cleaning up")
    finally:
        leftover = apply_cleanup()
        log("run_end", own={k: {"session": v["session"], "done": v["done"]} for k, v in OWN.items()}, leftover=leftover)
        close_results(leftover)
        if opts.report:
            shutil.copyfile(RESULTS_PATH, opts.report)
        print(f"\nresults: {RESULTS_PATH}\nreal model turns: {REAL_TURNS}; scripted turns: {SCRIPTED_TURNS}", flush=True)
    return 1 if any(r["status"] in ("FAIL", "ERROR") for r in RESULTS) else 0


if __name__ == "__main__":
    sys.exit(main())
