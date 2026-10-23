#!/usr/bin/env python3
"""Run the NIT-001 M4 Codex live checks unattended, in isolation, at no model cost.

Run it with `make live-codex` from a plain terminal. Nothing needs to be closed first.

Isolation. Every Codex process the harness starts runs with a private CODEX_HOME under $TMPDIR. The
harness runs its own `codex app-server --listen stdio://`, its own `codex exec`, and its own
`codex --no-daemon` TUI. It never touches ~/.codex, and it never starts, stops, or connects to the
user's Codex daemon. nitipan's state directory has no override by design, so the harness shares it.
It touches only what it creates there: leases it acquired, identified by recorded lease IDs, and
the session files of its own sessions and processes.

Zero model cost. Each private config points `model_provider` at a fake Responses API that the
harness serves on 127.0.0.1. A prompt carries a command. The fake answers it with one scripted
`exec_command` call, so Codex runs the command in its real Seatbelt sandbox with the CODEX_*
variables set and the hooks firing. The fake then ends the turn. No real model is called.

Hook trust. A fresh home trusts no hook. A throwaway app-server reads each hook's hash with
`hooks/list`, and `config/batchWrite` stores the trust, as the TUI does. The real host starts after
that, because nitipan trusts a config file only when it is older than the host.

Limits. A Codex "window" is a client subscription under a stdio host, so closing a window is
`thread/unsubscribe`. `/new` is `thread/start` plus an unsubscribe, and `/resume` is `thread/resume`
plus an unsubscribe. A daemon restart is a kill of the harness's own host.

Results go to spec/reports/2026-10-22-nit-001-m4-codex-harness-results.md, one row per check,
written as each check finishes. Raw events, host logs, hook logs, and TUI transcripts go to the work
directory. The exit status is 1 when any check fails or errors.

Several parts rest on reading codex-rs and have never run. `--probe` exercises just those parts,
prints what works, and exits. Run it first. `--selftest` checks the fake server's pure logic offline.
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import json
import os
import re
import shlex
import shutil
import signal
import subprocess
import sys
import threading
import time
import traceback
import uuid
from dataclasses import dataclass, field
from http.server import BaseHTTPRequestHandler
from pathlib import Path
from typing import Callable

import common
from common import (BIN, CLEAN_ENV, ENDED, HOME, OWN_PIDS, REGISTERED, ROOT, RUNID, SESSIONS, TUIS, WORK,
                    FakeServer, Pty, check, lease_by, leases, liveness, log, nitipan, note, own,
                    process_table, release, run, scratch, wait_for)

RESULTS_PATH = ROOT / "spec/reports/2026-10-22-nit-001-m4-codex-harness-results.md"

EXE = BIN.resolve()
PY = sys.executable
REAL_CODEX_HOME = HOME / ".codex"
HOMES = WORK / "homes"
SUPPORT = WORK / "support"
HOOK_LOG = SUPPORT / "hooks.jsonl"
MCP_DUMP_PATH = SUPPORT / "mcp-env.json"
HOOKWRAP_PATH = SUPPORT / "hookwrap.py"
MCP_SCRIPT_PATH = SUPPORT / "mcp_dump.py"

# The unload delay every home uses unless a variant overrides it. nitipan accepts 60 or less, and a
# short one keeps the dead-within-the-delay checks quick.
DELAY = 3

# How long the order variant's SessionEnd hook pauses before it runs nitipan. A resume inside this
# window lands while the hook is in flight.
END_PAUSE = 1.0

MODEL = "mock-model"
PROVIDER = "mock"
MARKER = re.compile(r"HARNESS-CMD:([A-Za-z0-9+/=]+):END")

# The As built rows the checks feed. The coordinator matches these strings against the proposal.
R_ID = "Codex PID and session variables in a shell command"
R_ENV = "Session variables left by each environment policy"
R_SYSCTL = "Liveness sysctl inside each tool"
R_MCP = "MCP variable passing, and opencode sessions per process"
R_MANY = "Codex sessions per process"
R_DECL = "Session declarations match the findings"
R_ENTRIES = "`acquire` with and without the printed entries"
R_MODES = "Permission mode lists"
R_HOST = "Host recognition in four modes, and `--no-daemon` ancestry"
R_GATE = "Codex `acquire` fails closed without a registration or delay"
R_CLOSE = "Codex reads dead after window close and `/new`"
R_RESTART = "Daemon restart, move, ended stays dead"
R_EMBED = "Embedded Codex session reads dead at TUI exit"
R_LOCK = "Non-ENOENT lock reads unknown, missing lock fails"
R_NOMOVE = "Move fails and leaves leases unknown without the lock file"
R_LATE = "Late SessionEnd after a resume, hook order"
R_HOOKENV = "Hook sees `CODEX_HOME`, and hook trust"
R_NOEND = "No SessionEnd entry fails, no registration names the timeout"
R_PIDERR = "Config-check error names the daemon's PID"
R_HINT = "`list` shows the hint and names an unreadable database"

CONFIG_FILES: set[str] = set()
HOSTS: list["Host"] = []
EXECS: list[subprocess.Popen] = []
THREADS: dict[str, "Ctx"] = {}
FAKE: "Fake | None" = None
BASELINE: list[str] = []
GUARD: dict = {}


# The fake Responses API. reply() is pure so it can be tested without a socket.


def encode_command(cmd: str, before: float = 0.0, after: float = 0.0, yield_ms: int = 60000) -> str:
    """A prompt that carries a command. The fake reads the encoded form. A real model would read
    the plain one."""
    payload = json.dumps({"cmd": cmd, "before": before, "after": after, "yield_ms": yield_ms})
    encoded = base64.b64encode(payload.encode()).decode()
    return f"Run this command exactly once with your shell tool, then reply ok. HARNESS-CMD:{encoded}:END"


def decode_marker(text: str) -> dict | None:
    found = MARKER.search(text or "")
    if not found:
        return None
    try:
        return json.loads(base64.b64decode(found.group(1)).decode())
    except ValueError:
        return None


def item_text(item: dict) -> str:
    content = item.get("content")
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        return "".join(p.get("text", "") for p in content if isinstance(p, dict))
    return ""


def tool_names(tools) -> list[str]:
    """Function names in a request's tool list, including those inside namespaces."""
    names = []
    for tool in tools or []:
        if not isinstance(tool, dict):
            continue
        if tool.get("name"):
            names.append(tool["name"])
        if isinstance(tool.get("function"), dict) and tool["function"].get("name"):
            names.append(tool["function"]["name"])
        names.extend(tool_names(tool.get("tools")))
    return names


def tool_call(names: list[str], payload: dict) -> tuple[str, dict] | None:
    cmd = payload["cmd"]
    if "exec_command" in names:
        return "exec_command", {"cmd": cmd, "login": False, "yield_time_ms": payload.get("yield_ms", 60000)}
    if "shell_command" in names:
        return "shell_command", {"command": cmd, "login": False, "timeout_ms": 120000}
    if "shell" in names:
        return "shell", {"command": ["bash", "-c", cmd], "timeout_ms": 120000}
    return None


def reply(request: dict) -> dict:
    """Decide the fake's answer: a scripted tool call, or a plain `ok`.

    The newest user message of the current turn decides. A turn ends at the last tool output or
    assistant message, so an old marker in the history never replays.
    """
    items = request.get("input") or []
    if not items:
        return {"kind": "text", "text": "ok"}
    last = items[-1] if isinstance(items[-1], dict) else {}
    if str(last.get("type", "")).endswith("_output"):
        marker = None
        for item in reversed(items):
            if isinstance(item, dict) and item.get("role") == "user":
                marker = decode_marker(item_text(item))
                if marker:
                    break
        return {"kind": "text", "text": "ok", "before": float((marker or {}).get("after", 0))}
    for item in reversed(items):
        if not isinstance(item, dict):
            continue
        if item.get("role") == "assistant" or str(item.get("type", "")).endswith("_output"):
            break
        if item.get("role") != "user":
            continue
        marker = decode_marker(item_text(item))
        if marker:
            call = tool_call(tool_names(request.get("tools")), marker)
            if call is None:
                return {"kind": "text", "text": "ok", "noTool": True}
            name, arguments = call
            return {"kind": "tool", "name": name, "arguments": json.dumps(arguments),
                    "before": float(marker.get("before", 0))}
    return {"kind": "text", "text": "ok"}


def sse_events(answer: dict, call_id: str) -> list[dict]:
    events = [{"type": "response.created", "response": {"id": "resp-1"}}]
    if answer["kind"] == "tool":
        events.append({"type": "response.output_item.done", "item": {
            "type": "function_call", "call_id": call_id, "name": answer["name"],
            "arguments": answer["arguments"]}})
    else:
        events.append({"type": "response.output_item.done", "item": {
            "type": "message", "role": "assistant", "id": "msg-" + call_id,
            "content": [{"type": "output_text", "text": answer["text"]}]}})
    events.append({"type": "response.completed", "response": {"id": "resp-1", "usage": {
        "input_tokens": 0, "input_tokens_details": None, "output_tokens": 0,
        "output_tokens_details": None, "total_tokens": 0}}})
    return events


def sse_body(events: list[dict]) -> bytes:
    return "".join(f"event: {e['type']}\ndata: {json.dumps(e)}\n\n" for e in events).encode()


class FakeHandler(BaseHTTPRequestHandler):
    def log_message(self, *args) -> None:
        pass

    def do_GET(self) -> None:
        body = json.dumps({"models": []}).encode() if self.path.split("?")[0].endswith("/models") else b"{}"
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self) -> None:
        raw = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        encoding = self.headers.get("Content-Encoding", "identity")
        try:
            request = json.loads(raw) if encoding == "identity" else {}
        except ValueError:
            request = {}
        answer = reply(request)
        HITS.append(self.path)
        log("fake_request", path=self.path, encoding=encoding, tools=tool_names(request.get("tools")),
            answer=answer["kind"], before=answer.get("before", 0), no_tool=answer.get("noTool", False))
        if answer.get("before"):
            time.sleep(answer["before"])
        body = sse_body(sse_events(answer, "call_" + uuid.uuid4().hex[:12]))
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        try:
            self.wfile.write(body)
        except OSError:
            pass


HITS: list[str] = []


class Fake(FakeServer):
    def __init__(self):
        super().__init__(FakeHandler)


# Support files: the hook wrapper and the MCP server that dumps its environment.

HOOKWRAP = r'''"""Test-only hook wrapper. It records the lock file around a hook, then runs nitipan."""
import json
import os
import subprocess
import sys
import time

log, pause, exe, *rest = sys.argv[1:]
data = sys.stdin.buffer.read()
try:
    payload = json.loads(data)
except ValueError:
    payload = {}
session = payload.get("session_id", "")
home = os.environ.get("CODEX_HOME", "")
lock = os.path.join(home, "thread-writer-locks", session + ".lock")


def snap():
    try:
        return {"exists": True, "mtime": os.stat(lock).st_mtime}
    except OSError:
        return {"exists": False, "mtime": None}


def emit(phase, **kw):
    line = {"hook": rest[-1], "phase": phase, "t": time.time(), "session": session, "lock": snap(), **kw}
    with open(log, "a") as f:
        f.write(json.dumps(line) + "\n")


emit("begin", codex_home=home, keys=sorted(payload), source=payload.get("source"))
if float(pause):
    time.sleep(float(pause))
    emit("after_pause")
r = subprocess.run([exe, *rest], input=data)
emit("end", exit=r.returncode)
sys.exit(r.returncode)
'''

MCP_DUMP = r'''"""Test-only MCP server. It writes its environment to a file, then answers the handshake."""
import json
import os
import sys

with open(sys.argv[1], "w") as f:
    json.dump(dict(os.environ), f)

for line in sys.stdin:
    try:
        msg = json.loads(line)
    except ValueError:
        continue
    if "id" not in msg:
        continue
    method = msg.get("method")
    if method == "initialize":
        result = {"protocolVersion": (msg.get("params") or {}).get("protocolVersion", "2024-11-05"),
                  "capabilities": {"tools": {}}, "serverInfo": {"name": "dump", "version": "0"}}
    elif method == "tools/list":
        result = {"tools": []}
    else:
        result = {}
    sys.stdout.write(json.dumps({"jsonrpc": "2.0", "id": msg["id"], "result": result}) + "\n")
    sys.stdout.flush()
'''


def write_support() -> None:
    SUPPORT.mkdir(parents=True, exist_ok=True)
    HOOKWRAP_PATH.write_text(HOOKWRAP)
    MCP_SCRIPT_PATH.write_text(MCP_DUMP)


# Private Codex homes.


@dataclass
class Spec:
    """One private CODEX_HOME. The defaults are the entries `nitipan setup codex` prints."""

    label: str
    delay: int | None = DELAY
    root: bool = True
    policy: str = ""
    start: bool = True
    end: bool = True
    start_command: str | None = None
    trusted: tuple[str, ...] = ("sessionStart", "sessionEnd")
    ordered: bool = False
    mcp: bool = False
    project: Path | None = None
    extra: str = ""


def toml_str(value: str) -> str:
    return json.dumps(value)


def hook_commands(spec: Spec) -> tuple[str, str]:
    if spec.ordered:
        pause = (0, END_PAUSE)
        return (f"{PY} {HOOKWRAP_PATH} {HOOK_LOG} {pause[0]} {EXE} hook session-start",
                f"{PY} {HOOKWRAP_PATH} {HOOK_LOG} {pause[1]} {EXE} hook session-end")
    return f"{EXE} hook session-start", f"{EXE} hook session-end"


def config_text(spec: Spec, port: int) -> str:
    """The complete config.toml. The unload delay comes first, because nitipan reads only the keys
    that precede the first table."""
    start, end = hook_commands(spec)
    if spec.start_command:
        start = spec.start_command
    lines = []
    if spec.delay is not None:
        lines.append(f"thread_unload_delay_secs = {spec.delay}")
    lines += [
        f"model = {toml_str(MODEL)}",
        f"model_provider = {toml_str(PROVIDER)}",
        'approval_policy = "never"',
        'sandbox_mode = "workspace-write"',
        "check_for_update_on_startup = false",
        "",
        "[analytics]",
        "enabled = false",
        "",
        f"[model_providers.{PROVIDER}]",
        'name = "mock"',
        f'base_url = "http://127.0.0.1:{port}/v1"',
        'wire_api = "responses"',
        "request_max_retries = 0",
        "stream_max_retries = 0",
        "supports_websockets = false",
        "",
    ]
    if spec.root:
        lines += ["[sandbox_workspace_write]", f"writable_roots = [{toml_str(str(common.STATE))}]", ""]
    if spec.policy:
        lines += ["[shell_environment_policy]", *spec.policy.splitlines(), ""]
    if spec.start:
        lines += ["[[hooks.SessionStart]]", "[[hooks.SessionStart.hooks]]", 'type = "command"',
                  f"command = {toml_str(start)}", "timeout = 3", ""]
    if spec.end:
        lines += ["[[hooks.SessionEnd]]", "[[hooks.SessionEnd.hooks]]", 'type = "command"',
                  f"command = {toml_str(end)}", "timeout = 3", ""]
    if spec.mcp:
        lines += ["[mcp_servers.dump]", f"command = {toml_str(PY)}",
                  f"args = [{toml_str(str(MCP_SCRIPT_PATH))}, {toml_str(str(MCP_DUMP_PATH))}]",
                  "startup_timeout_sec = 20", ""]
    if spec.project:
        lines += [f"[projects.{toml_str(str(spec.project))}]", 'trust_level = "trusted"', ""]
    if spec.extra:
        lines += [spec.extra, ""]
    return "\n".join(lines)


def delay_precedes_tables(text: str) -> bool:
    for line in text.splitlines():
        if line.lstrip().startswith("["):
            return False
        if line.startswith("thread_unload_delay_secs"):
            return True
    return False


def config_memory_name(home: str) -> str:
    """nitipan's per-home config memory file, sessions/config/<digest of the home>.json."""
    return hashlib.sha256(home.encode()).hexdigest()[:32] + ".json"


def codex_env(home: Path) -> dict[str, str]:
    private_home(home)
    for form in {str(home), str(home.resolve())}:
        CONFIG_FILES.add(config_memory_name(form))
    env = dict(CLEAN_ENV)
    env["CODEX_HOME"] = str(home)
    env["TERM"] = "xterm-256color"
    return env


def private_home(home: Path) -> None:
    """Every Codex process the harness starts must run in a home under the work directory."""
    resolved = home.resolve()
    if REAL_CODEX_HOME.resolve() in (resolved, *resolved.parents) or WORK.resolve() not in resolved.parents:
        raise RuntimeError(f"refusing to run Codex in {home}: it is not a private home")


# The app-server client.


class RpcError(Exception):
    pass


class Host:
    """A private `codex app-server --listen stdio://`. Its PID is the daemon as far as nitipan can tell."""

    def __init__(self, label: str, home: Path):
        self.label = label
        self.home = home
        self.next_id = 0
        self.pending: dict[int, dict] = {}
        self.notes: list[dict] = []
        self.cv = threading.Condition()
        log_file = (WORK / f"host-{label}-{len(HOSTS)}.log").open("w")
        self.proc = subprocess.Popen(
            ["codex", "app-server", "--listen", "stdio://"], cwd=WORK, env=codex_env(home),
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=log_file, text=True, bufsize=1,
            start_new_session=True)
        OWN_PIDS.add(self.proc.pid)
        HOSTS.append(self)
        threading.Thread(target=self._read, daemon=True).start()
        log("host_start", label=label, pid=self.proc.pid, home=str(home))

    @property
    def pid(self) -> int:
        return self.proc.pid

    def alive(self) -> bool:
        return self.proc.returncode is None and self._running()

    def _running(self) -> bool:
        """Poll without reaping, so the group kill in stop() still has an unreaped leader."""
        try:
            return os.waitid(os.P_PID, self.proc.pid, os.WEXITED | os.WNOHANG | os.WNOWAIT) is None
        except ChildProcessError:
            return False

    def _send(self, message: dict) -> None:
        self.proc.stdin.write(json.dumps(message) + "\n")
        self.proc.stdin.flush()

    def _read(self) -> None:
        for line in self.proc.stdout:
            try:
                msg = json.loads(line)
            except ValueError:
                continue
            log("rpc_in", host=self.label, message=json.dumps(msg)[:1500])
            if "method" in msg and "id" in msg:
                self._answer(msg)
            elif "id" in msg:
                with self.cv:
                    self.pending[msg["id"]] = msg
                    self.cv.notify_all()
            else:
                with self.cv:
                    self.notes.append(msg)
                    self.cv.notify_all()

        with self.cv:
            self.cv.notify_all()

    def _answer(self, msg: dict) -> None:
        """Answer a request from the server. The config never asks for approval, so any request is
        logged, and approvals are accepted so that a turn cannot hang on one."""
        method = msg["method"]
        log("server_request", host=self.label, method=method)
        try:
            if "requestApproval" in method:
                self._send({"id": msg["id"], "result": {"decision": "accept"}})
            else:
                self._send({"id": msg["id"], "error": {"code": -32601, "message": "unsupported"}})
        except (OSError, ValueError):
            pass

    def request(self, method: str, params: dict | None = None, timeout: float = 60) -> dict:
        with self.cv:
            self.next_id += 1
            rid = self.next_id
        message = {"id": rid, "method": method}
        if params is not None:
            message["params"] = params
        self._send(message)
        log("rpc_out", host=self.label, message=json.dumps(message)[:1500])
        with self.cv:
            self.cv.wait_for(lambda: rid in self.pending or not self.alive(), timeout)
            reply_msg = self.pending.pop(rid, None)
        if reply_msg is None:
            raise RpcError(f"{method}: no reply" + ("" if self.alive() else " (the host exited)"))
        if "error" in reply_msg:
            raise RpcError(f"{method}: {reply_msg['error']}")
        return reply_msg.get("result") or {}

    def initialize(self) -> dict:
        result = self.request("initialize", {
            "clientInfo": {"name": "nitipan-live-checks", "version": "0.0.0"},
            "capabilities": {"experimentalApi": True}})
        self._send({"method": "initialized"})
        return result

    def mark(self) -> int:
        with self.cv:
            return len(self.notes)

    def find(self, method: str, since: int = 0, **params) -> dict | None:
        with self.cv:
            for msg in self.notes[since:]:
                if msg.get("method") != method:
                    continue
                if all(lookup_path(msg.get("params"), k) == v for k, v in params.items()):
                    return msg
        return None

    def wait_note(self, method: str, timeout: float, since: int = 0, **params) -> dict | None:
        with self.cv:
            self.cv.wait_for(lambda: self.find_unlocked(method, since, params) is not None, timeout)
        return self.find(method, since, **params)

    def find_unlocked(self, method: str, since: int, params: dict) -> dict | None:
        for msg in self.notes[since:]:
            if msg.get("method") == method and all(
                    lookup_path(msg.get("params"), k) == v for k, v in params.items()):
                return msg
        return None

    def notes_since(self, since: int, method: str) -> list[dict]:
        with self.cv:
            return [m for m in self.notes[since:] if m.get("method") == method]

    def thread(self, cwd: Path, label: str) -> "Ctx":
        result = self.request("thread/start", {"cwd": str(cwd)})
        tid = result["thread"]["id"]
        SESSIONS.add(tid)
        ctx = Ctx(self, tid, cwd, label)
        THREADS[tid] = ctx
        log("thread", host=self.label, thread=tid, label=label, cwd=str(cwd))
        return ctx

    def stop(self, sig: int | None = None) -> None:
        """Stop the host. Closing stdin is the graceful path, as a client disconnect would be.

        The process group is killed while the host is still unreaped, because its zombie keeps the PID
        from being reused. Once the host is reaped, nothing is signaled.
        """
        if self.proc.returncode is None:
            if sig is not None:
                self.proc.send_signal(sig)
            else:
                try:
                    self.proc.stdin.close()
                except OSError:
                    pass
            if not self._exited(20):
                self.proc.terminate()
                if not self._exited(5):
                    self.proc.kill()
                    self._exited(10)
            self._reap()
            self.proc.wait(timeout=10)
            log("host_stop", label=self.label, pid=self.proc.pid)

    def _exited(self, timeout: float) -> bool:
        """True once the host has exited, without reaping it."""
        def gone() -> bool:
            try:
                return os.waitid(os.P_PID, self.proc.pid, os.WEXITED | os.WNOHANG | os.WNOWAIT) is not None
            except ChildProcessError:
                return True

        return bool(wait_for(gone, timeout, 0.1))

    def _reap(self) -> None:
        """Kill anything the host left in its own process group. Call it before the host is reaped."""
        try:
            os.killpg(self.proc.pid, signal.SIGKILL)
        except (ProcessLookupError, PermissionError):
            pass


def lookup_path(obj, dotted: str):
    for part in dotted.split("."):
        obj = obj.get(part) if isinstance(obj, dict) else None
    return obj


class TurnResult:
    def __init__(self, status: str, items: list[dict], error: str = "", out: common.Out | None = None):
        self.status = status
        self.items = items
        self.error = error
        last = items[-1] if items else {}
        self.exit = last.get("exitCode")
        self.text = (last.get("aggregatedOutput") or "").replace("\r", "").strip()
        if out is not None and out.exit is not None:
            self.exit, self.text = out.exit, out.text.strip()

    def __repr__(self) -> str:
        return f"{self.status} exit={self.exit} {self.text!r}" + (f" error={self.error}" if self.error else "")


class Ctx:
    """A thread on a host, with the commands that run in it."""

    def __init__(self, host: Host, tid: str, cwd: Path, label: str):
        self.host = host
        self.id = tid
        self.cwd = cwd
        self.label = label
        self.turns = 0

    def turn(self, cmd: str, before: float = 0.0, after: float = 0.0, timeout: float = 90) -> TurnResult:
        """Run cmd in the thread's sandbox through a scripted turn.

        Codex emits no commandExecution item for a command that exits at once, so the output goes to
        a file in the thread's working directory and the exit status is read from it.
        """
        self.turns += 1
        out = self.cwd / f".harness-{self.turns}.out"
        out.unlink(missing_ok=True)
        since = self.host.mark()
        prompt = encode_command(common.wrap(cmd, out), before, after)
        self.host.request("turn/start", {"threadId": self.id, "input": [
            {"type": "text", "text": prompt, "text_elements": []}]})
        done = self.host.wait_note("turn/completed", timeout, since, threadId=self.id)
        items = [m["params"]["item"] for m in self.host.notes_since(since, "item/completed")
                 if lookup_path(m.get("params"), "threadId") == self.id
                 and lookup_path(m.get("params"), "item.type") == "commandExecution"]
        wait_for(lambda: common.done(out), 5 if done else 0, 0.1)
        result = common.Out(out)
        if done is None:
            return TurnResult("timeout", items, "no turn/completed", result)
        turn = done["params"]["turn"]
        return TurnResult(turn.get("status", "?"), items, json.dumps(turn.get("error") or ""), result)

    def shell(self, cmd: str, timeout: float = 60) -> TurnResult:
        """An unsandboxed user shell command. It fires no SessionStart."""
        since = self.host.mark()
        self.host.request("thread/shellCommand", {"threadId": self.id, "command": cmd})
        found = wait_for(lambda: [m for m in self.host.notes_since(since, "item/completed")
                                  if lookup_path(m.get("params"), "threadId") == self.id
                                  and lookup_path(m.get("params"), "item.type") == "commandExecution"],
                         timeout, 0.3)
        return TurnResult("completed" if found else "timeout", [m["params"]["item"] for m in found or []])

    def unsubscribe(self) -> str:
        return self.host.request("thread/unsubscribe", {"threadId": self.id}).get("status", "?")

    def resume(self, host: Host | None = None) -> dict:
        if host is not None:
            self.host = host
        return self.host.request("thread/resume", {"threadId": self.id})

    def lock(self) -> Path:
        return self.host.home / "thread-writer-locks" / f"{self.id}.lock"

    def loaded(self) -> bool:
        return self.id in (self.host.request("thread/loaded/list", {}).get("data") or [])

    def registrations(self) -> list[Path]:
        return sorted(REGISTERED.glob(f"codex.{self.id}.*"))

    def registration(self) -> dict | None:
        for path in self.registrations():
            try:
                return json.loads(path.read_text())
            except (OSError, ValueError):
                continue
        return None

    def end_marks(self) -> list[Path]:
        return sorted(ENDED.glob(f"codex.{self.id}.*"))


def make_home(spec: Spec) -> Path:
    """Write a home's config, and trust its hooks through a throwaway host."""
    home = HOMES / spec.label
    home.mkdir(parents=True, exist_ok=True)
    (home / "config.toml").write_text(config_text(spec, FAKE.port))
    boot = Host("boot-" + spec.label, home)
    try:
        boot.initialize()
        cwd = scratch("boot-" + spec.label)
        listed = boot.request("hooks/list", {"cwds": [str(cwd)]})
        hooks = [h for entry in listed.get("data", []) for h in entry.get("hooks", [])
                 if h.get("source") == "user"]
        edits = {h["key"]: {"trusted_hash": h["currentHash"]} for h in hooks
                 if h.get("eventName") in spec.trusted and h.get("trustStatus") != "trusted"}
        if edits:
            boot.request("config/batchWrite", {
                "edits": [{"keyPath": "hooks.state", "value": edits, "mergeStrategy": "upsert"}],
                "reloadUserConfig": True})
        after = boot.request("hooks/list", {"cwds": [str(cwd)]})
        status = {h["eventName"]: h.get("trustStatus") for entry in after.get("data", [])
                  for h in entry.get("hooks", []) if h.get("source") == "user"}
        log("trust", home=spec.label, status=status)
    finally:
        boot.stop()
    TRUST[spec.label] = status
    text = (home / "config.toml").read_text()
    (WORK / f"config-{spec.label}.toml").write_text(text)
    if spec.delay is not None and not delay_precedes_tables(text):
        raise RuntimeError(f"home {spec.label}: the trust write moved thread_unload_delay_secs below a table")
    # The real host must start after the last config write, by more than the clock's resolution.
    time.sleep(1.1)
    return home


TRUST: dict[str, dict] = {}
HOME_CACHE: dict[str, Path] = {}


def home_for(spec: Spec) -> Path:
    if spec.label not in HOME_CACHE:
        HOME_CACHE[spec.label] = make_home(spec)
    return HOME_CACHE[spec.label]


PLAIN = Spec("plain")
PLAIN_HOST: Host | None = None


def plain_host() -> Host:
    """The shared host of the default home. A step that kills it gets a fresh one next time."""
    global PLAIN_HOST
    if PLAIN_HOST is None or not PLAIN_HOST.alive():
        PLAIN_HOST = Host("plain", home_for(PLAIN))
        PLAIN_HOST.initialize()
    return PLAIN_HOST


def fresh_host(spec: Spec) -> Host:
    host = Host(spec.label, home_for(spec))
    host.initialize()
    return host


# Leases, commands, and polling.


def json_in(text: str):
    start, end = text.find("{"), text.rfind("}")
    if start < 0 or end < start:
        return None
    try:
        return json.loads(text[start:end + 1])
    except ValueError:
        return None


def acquire(ctx: Ctx, label: str, extra: str = "", directory: Path | None = None):
    desc = f"harness {RUNID} {label}"
    where = directory or ctx.cwd
    res = ctx.turn(f"{BIN} acquire --checkout {shlex.quote(str(where))} --description {shlex.quote(desc)}{extra}")
    lease = lease_by(description=desc)
    if lease:
        own(lease, ctx.id, "turn")
    log("acquire", label=label, thread=ctx.id, result=repr(res), lease=lease)
    return res, lease


def release_in_turn(info: dict, lease_id: str) -> bool:
    """Release through the holder's own session, resuming it when its thread was unloaded. A dead
    holder's lease goes through a plain `break`, which leaves no tombstone."""
    ctx = THREADS.get(info["session"])
    if ctx is None or not ctx.host.alive() or liveness(lease_id)["status"] == "dead":
        return False
    command = f"{BIN} release {lease_id}"
    try:
        try:
            return ctx.turn(command, timeout=40).exit == 0
        except RpcError:
            ctx.resume()
            return ctx.turn(command, timeout=40).exit == 0
    except (RpcError, OSError):
        return False


common.TURN_RELEASE = release_in_turn


def await_status(lease_id: str, want: tuple[str, ...], timeout: float, since: float | None = None,
                 interval: float = 0.5) -> tuple[dict, float | None]:
    """Poll a lease until its liveness is one of `want`. The second value is the seconds since
    `since`, or None on a timeout."""
    start = since if since is not None else time.monotonic()
    while True:
        lv = liveness(lease_id)
        if lv["status"] in want:
            return lv, time.monotonic() - start
        if time.monotonic() - start >= timeout:
            return lv, None
        time.sleep(interval)


def read_hook_log() -> list[dict]:
    out = []
    try:
        for line in HOOK_LOG.read_text().splitlines():
            try:
                out.append(json.loads(line))
            except ValueError:
                pass
    except OSError:
        pass
    return out


def host_command(pid: int) -> str:
    return process_table().get(pid, (0, ""))[1]


# Steps. Each records its own checks. An exception becomes one ERROR row and the run goes on.


def step_install() -> None:
    global FAKE
    r = run(["make", "build"], timeout=600)
    check("0", R_ENTRIES, "make build", r.returncode == 0, r.stderr.strip()[-200:])
    if r.returncode != 0:
        raise RuntimeError("make build failed; nothing below would test the current code")
    FAKE = Fake()
    FAKE.start()
    write_support()
    check("0", R_ENTRIES, "fake Responses API listens on loopback", FAKE.port > 0, f"port {FAKE.port}")
    host = plain_host()
    status = TRUST.get("plain", {})
    check("0", R_HOOKENV, "both hooks are trusted in the private home after the trust write",
          status.get("sessionStart") == "trusted" and status.get("sessionEnd") == "trusted", json.dumps(status))
    check("0", R_ENTRIES, "private app-server host starts with the mock provider", host.alive(), f"pid {host.pid}")
    check("0", R_HOST, "the host's argument list is recognised as a daemon",
          "app-server" in host_command(host.pid), host_command(host.pid))


def step_identity() -> None:
    host = plain_host()
    a = host.thread(scratch("a"), "a")
    before_turn = a.lock().exists()
    res, lease = acquire(a, "a", extra="")
    env_res = a.turn("/usr/bin/env | /usr/bin/grep '^CODEX_' | /usr/bin/sort")
    env = dict(line.split("=", 1) for line in env_res.text.splitlines() if "=" in line)
    check("1", R_ID, "CODEX_THREAD_ID and CODEX_SESSION_ID are both the thread ID",
          env.get("CODEX_THREAD_ID") == a.id and env.get("CODEX_SESSION_ID") == a.id,
          f"thread {a.id}; {sorted(env)}")
    check("1", R_ID, "acquire succeeds in a brand-new session's first turn", res.exit == 0 and lease is not None,
          repr(res))
    if not lease:
        raise RuntimeError("acquire in the first turn failed; the rest of the identity step needs its lease")
    holder = lease["holder"]
    check("1", R_ID, "holder.agent is codex and holder.session_id is the thread ID",
          holder.get("agent") == "codex" and holder.get("session_id") == a.id, json.dumps(holder))
    check("1", R_ID, "holder.pid is the Codex host process", holder.get("pid") == host.pid,
          f"{holder.get('pid')} vs host {host.pid}")
    lv = lease["liveness"]
    check("1", R_ID, "liveness reads alive from the registration", lv["status"] == "alive"
          and f"registered under host {host.pid}" in lv.get("reason", ""), json.dumps(lv))
    check("8", R_LOCK, "the writer-lock file exists once thread/start returns", before_turn,
          "stat before the first turn")

    listed = a.turn(f"{BIN} list --json")
    doc = json_in(listed.text) or {}
    seen = next((x for x in doc.get("leases", []) if x.get("id") == lease["id"]), None)
    check("3a", R_SYSCTL, "list inside the sandbox reads the lease alive (the sysctl works there)",
          bool(seen) and seen["liveness"]["status"] == "alive", repr(listed)[:200])
    check("3a", R_ENTRIES, "acquire and list both work inside the workspace-write sandbox with the printed entries",
          res.exit == 0 and listed.exit == 0, "")

    reg = a.registration() or {}
    check("7", R_HOOKENV, "the registration names the private CODEX_HOME the host runs with",
          bool(reg) and Path(reg.get("codex_home", "")).resolve() == host.home.resolve(),
          f"{reg.get('codex_home')} vs {host.home}")
    check("7", R_HOOKENV, "the registration session equals the thread ID, host.pid is the host",
          reg.get("session") == a.id and lookup_path(reg, "host.pid") == host.pid, json.dumps(reg)[:200])
    check("7", R_GATE, "the registration records the unload delay and a present SessionEnd entry",
          reg.get("unload_delay_secs") == DELAY and reg.get("session_end") == "present",
          f"delay {reg.get('unload_delay_secs')}, session_end {reg.get('session_end')}")
    check("4a", R_HOST, "the registration records the app-server host as a daemon", reg.get("mode") == "daemon",
          f"mode {reg.get('mode')}; argv {host_command(host.pid)}")

    b = host.thread(scratch("b"), "b")
    _, lease_b = acquire(b, "b")
    ok = bool(lease_b) and lease_b["holder"]["pid"] == holder["pid"] and a.id != b.id
    check("5", R_MANY, "two sessions on one host share one holder PID", ok,
          f"{holder['pid']} / {lease_b['holder']['pid'] if lease_b else 'no lease'}")
    check("5", R_DECL, "the adapter's multi-session and CODEX_SESSION_ID declarations match what was seen",
          ok and bool(lease_b) and lease_b["holder"]["session_id"] == b.id,
          "one host, a holder per thread, keyed by CODEX_SESSION_ID")
    check("5", R_HOST, "the second session's holder.pid is the same daemon", bool(lease_b)
          and lease_b["holder"]["pid"] == host.pid, "")

    d3 = scratch("modes")
    res_ok, lease_m = acquire(a, "modes", " --permission-mode workspace-write", d3)
    check("15", R_MODES, "--permission-mode workspace-write succeeds and is stored",
          res_ok.exit == 0 and bool(lease_m) and lease_m.get("permission_mode") == "workspace-write",
          repr(res_ok))
    bad = a.turn(f"{BIN} acquire --checkout {shlex.quote(str(d3))}-x --permission-mode nonsense")
    wanted = ("read-only", "workspace-write", "danger-full-access", "on-request", "never")
    check("15", R_MODES, "--permission-mode nonsense fails and lists the five Codex values",
          bad.exit not in (0, None) and all(m in bad.text for m in wanted), repr(bad)[:300])
    help_text = subprocess.run(["codex", "--help"], capture_output=True, text=True, env=CLEAN_ENV).stdout
    note("15", R_MODES, "Codex's own --sandbox and --ask-for-approval values",
         ", ".join(f"{m}={'yes' if m in help_text else 'no'}" for m in wanted))

    stripped = a.turn(f"/usr/bin/env -u CODEX_SESSION_ID {BIN} acquire --checkout {shlex.quote(str(scratch('nosession')))} "
                      f"--description {shlex.quote(f'harness {RUNID} nosession')}")
    stray = lease_by(description=f"harness {RUNID} nosession")
    if stray:
        own(stray, a.id, "turn")
    check("2", R_ENV, "acquire with CODEX_SESSION_ID unset fails closed and says why",
          stripped.exit not in (0, None) and stray is None, repr(stripped)[:300])
    for held in (lease, lease_b, lease_m, stray):
        if held:
            release(held["id"])


def step_policies() -> None:
    cases = [("core", 'inherit = "core"'), ("include_only", 'include_only = ["PATH", "HOME"]'),
             ("none", 'inherit = "none"')]
    for label, policy in cases:
        host = fresh_host(Spec("policy-" + label, policy=policy))
        ctx = host.thread(scratch("policy-" + label), label)
        env_res = ctx.turn("/usr/bin/env | /usr/bin/grep '^CODEX_' | /usr/bin/sort")
        env = dict(line.split("=", 1) for line in env_res.text.splitlines() if "=" in line)
        res, lease = acquire(ctx, "policy-" + label)
        check("2", R_ENV, f"policy {label}: both session variables survive, and acquire works",
              "CODEX_THREAD_ID" in env and "CODEX_SESSION_ID" in env and res.exit == 0 and lease is not None,
              f"vars {sorted(env)}; {res!r}"[:300])
        if lease:
            release(lease["id"])
        host.stop()


def step_noroot() -> None:
    host = fresh_host(Spec("noroot", root=False))
    ctx = host.thread(scratch("noroot"), "noroot")
    res, lease = acquire(ctx, "noroot")
    check("3b", R_ENTRIES, "without the writable root, acquire fails and names the sandbox",
          res.exit not in (0, None) and lease is None and "sandbox likely blocked" in res.text, repr(res)[:300])
    check("3b", R_ENTRIES, "the error prints the setup entries, including the writable root",
          "writable_roots" in res.text and "hooks.SessionEnd" in res.text, "")
    if lease:
        release(lease["id"])
    host.stop()


def gate(label: str, spec: Spec, row: str, name: str, must: tuple[str, ...], pre=None, pids: bool = True) -> None:
    """Start a host for a gate variant, try acquire in a fresh thread, and check the refusal."""
    host = Host(spec.label, home_for(spec))
    host.initialize()
    try:
        if pre:
            pre(spec, host)
        ctx = host.thread(scratch("gate-" + label), label)
        res, lease = acquire(ctx, "gate-" + label)
        text = res.text
        ok = res.exit not in (0, None) and lease is None and all(m in text for m in must)
        if pids:
            ok = ok and str(host.pid) in text
        check("14", row, name, ok, repr(res)[:400])
        if lease:
            release(lease["id"])
    finally:
        host.stop()


def touch_after_start(spec: Spec, host: Host) -> None:
    path = host.home / "config.toml"
    now = time.time() + 2
    os.utime(path, (now, now))


def step_gates() -> None:
    host = plain_host()
    ctx = host.thread(scratch("noreg"), "noreg")
    res = ctx.shell(f"{BIN} acquire --checkout {shlex.quote(str(ctx.cwd))}")
    check("14", R_GATE, "acquire from a user shell command (no SessionStart ran) fails with no registration",
          res.exit not in (0, None) and "no registration" in res.text and "may have timed out" in res.text
          and str(host.pid) in res.text, repr(res)[:400])

    gate("high", Spec("high-delay", delay=1800), R_PIDERR, "unload delay 1800 refuses and names the daemon PID",
         ("1800", "60 or less", "restarts"))
    gate("touched", Spec("touched"), R_PIDERR, "a config changed after the host started reads unknown and names the PID",
         ("unknown", "restarts"), pre=touch_after_start)
    gate("noend", Spec("no-end", end=False), R_NOEND, "no SessionEnd entry refuses, names the PID, prints the entries",
         ("no SessionEnd entry", "hooks.SessionEnd"))
    gate("slow", Spec("slow-start", start_command=f"/bin/sh -c 'sleep 5; {EXE} hook session-start'",
                      trusted=("sessionStart", "sessionEnd")),
         R_NOEND, "a SessionStart hook that times out leaves no registration and the error says so",
         ("may have timed out",))


def step_locks() -> None:
    host = plain_host()
    gone = host.thread(scratch("lockgone"), "lockgone")
    gone.turn("true")
    removed = False
    try:
        gone.lock().unlink()
        removed = True
    except OSError as e:
        note("14", R_LOCK, "could not remove the private lock file", repr(e))
    res, lease = acquire(gone, "lockgone")
    check("14", R_LOCK, "acquire fails closed when the session's own lock file is missing",
          removed and res.exit not in (0, None) and lease is None and "no writer-lock file" in res.text, repr(res)[:300])
    if lease:
        release(lease["id"])

    held = host.thread(scratch("lockperm"), "lockperm")
    _, lease = acquire(held, "lockperm")
    locks = host.home / "thread-writer-locks"
    if not lease:
        check("14", R_LOCK, "a lock file that cannot be checked reads unknown", None, "no lease to read")
        return
    try:
        os.chmod(locks, 0)
        held.unsubscribe()
        wait_for(lambda: held.end_marks(), DELAY + 10, 0.3)
        lv, _ = await_status(lease["id"], ("unknown", "dead", "alive"), 1)
        check("14", R_LOCK, "an end mark with an unreadable lock file reads unknown, not dead",
              lv["status"] == "unknown" and bool(held.end_marks()), json.dumps(lv))
    finally:
        os.chmod(locks, 0o700)
    release(lease["id"])


def step_hint() -> None:
    host = fresh_host(Spec("start-only", trusted=("sessionStart",)))
    ctx = host.thread(scratch("startonly"), "start-only")
    status = TRUST.get("start-only", {})
    check("12", R_HOOKENV, "trusting one hook leaves the other untrusted",
          status.get("sessionStart") == "trusted" and status.get("sessionEnd") != "trusted", json.dumps(status))
    res, lease = acquire(ctx, "start-only")
    check("12", R_HOOKENV, "with only SessionStart trusted, the session registers", res.exit == 0
          and ctx.registration() is not None, repr(res)[:200])
    if lease:
        ctx.unsubscribe()
        time.sleep(DELAY + 4)
        check("12", R_HOOKENV, "with only SessionStart trusted, no SessionEnd runs", not ctx.end_marks(),
              str(ctx.end_marks()))
        listing = nitipan("list")
        check("14", R_HINT, "list shows the likely-ended hint when the lock file is gone but the host lives",
              "likely ended" in listing.stdout, listing.stdout[-300:])
        check("12", R_HOOKENV, "the lease still reads alive (no end was recorded)",
              liveness(lease["id"])["status"] == "alive", "")
        release(lease["id"])
    host.stop()

    host = fresh_host(Spec("end-only", trusted=("sessionEnd",)))
    ctx = host.thread(scratch("endonly"), "end-only")
    res, lease = acquire(ctx, "end-only")
    check("12", R_HOOKENV, "with only SessionEnd trusted, SessionStart does not run, so acquire is refused",
          res.exit not in (0, None) and ctx.registration() is None, repr(res)[:200])
    ctx.unsubscribe()
    check("12", R_HOOKENV, "with only SessionEnd trusted, the end hook runs at unload",
          bool(wait_for(lambda: ctx.end_marks(), DELAY + 8, 0.3)), str(ctx.end_marks()))
    if lease:
        release(lease["id"])
    host.stop()


def step_mcp() -> None:
    MCP_DUMP_PATH.unlink(missing_ok=True)
    host = fresh_host(Spec("mcp", mcp=True))
    ctx = host.thread(scratch("mcp"), "mcp")
    ctx.turn("true")
    appeared = bool(wait_for(MCP_DUMP_PATH.exists, 30, 0.5))
    if not appeared:
        check("6", R_MCP, "a stdio MCP server starts and reports its environment", False,
              "the dump file never appeared")
        host.stop()
        return
    env = json.loads(MCP_DUMP_PATH.read_text())
    got = sorted(k for k in env if k.startswith("CODEX_"))
    check("6", R_MCP, "a stdio MCP server started by Codex was observed", True,
          f"CODEX_THREAD_ID={'yes' if 'CODEX_THREAD_ID' in env else 'no'}, "
          f"CODEX_SESSION_ID={'yes' if 'CODEX_SESSION_ID' in env else 'no'}; CODEX_* keys {got}")
    host.stop()


def step_order() -> None:
    spec = Spec("order", delay=4, ordered=True)
    host = fresh_host(spec)
    ctx = host.thread(scratch("order"), "order")
    lock_at_start = ctx.lock().exists()
    res, lease = acquire(ctx, "order")
    check("8", R_LOCK, "acquire works in the first turn of a brand-new session", res.exit == 0 and lease is not None,
          repr(res)[:200])
    begin = next((r for r in read_hook_log() if r["session"] == ctx.id and r["hook"] == "session-start"
                  and r["phase"] == "begin"), None)
    check("8", R_LOCK, "SessionStart runs after the lock file exists",
          bool(begin) and begin["lock"]["exists"], f"lock at thread/start {lock_at_start}; at the hook "
          f"{lookup_path(begin, 'lock.exists')}; payload keys {begin and begin.get('keys')}")
    check("7", R_HOOKENV, "the hook payload carries session_id, and the hook sees the host's CODEX_HOME",
          bool(begin) and "session_id" in begin.get("keys", [])
          and Path(begin.get("codex_home", "")).resolve() == host.home.resolve(), json.dumps(begin)[:250])
    if not lease:
        host.stop()
        return

    ctx.unsubscribe()
    started = wait_for(lambda: next((r for r in read_hook_log() if r["session"] == ctx.id
                                     and r["hook"] == "session-end" and r["phase"] == "begin"), None),
                       spec.delay + 10, 0.05)
    check("10", R_LATE, "the SessionEnd hook fires at unload", bool(started),
          f"lock at the hook {lookup_path(started, 'lock.exists')}")
    resumed = None
    if started:
        try:
            resumed = ctx.resume()
        except RpcError as e:
            note("10", R_LATE, "thread/resume while the SessionEnd hook runs was refused", str(e))
    finished = wait_for(lambda: next((r for r in read_hook_log() if r["session"] == ctx.id
                                      and r["hook"] == "session-end" and r["phase"] == "end"), None), 10, 0.2)
    if started and resumed is not None and finished:
        time.sleep(1)
        lv = liveness(lease["id"])
        check("10", R_LATE, "a session resumed while its SessionEnd ran stays alive, with the lock file back",
              lv["status"] == "alive" and ctx.lock().exists(),
              f"{json.dumps(lv)}; end marks {len(ctx.end_marks())}; lock {ctx.lock().exists()}; "
              f"hook exit {finished.get('exit')}")
    else:
        check("10", R_LATE, "a session resumed while its SessionEnd ran stays alive", None,
              f"hook started {bool(started)}, resumed {resumed is not None}, finished {bool(finished)}. "
              "Codex refuses thread/resume while a thread is closing, so the host API cannot land a resume "
              "inside the SessionEnd window")
    release(lease["id"])
    host.stop()


def step_close() -> None:
    host = plain_host()
    limit = DELAY + 10

    a = host.thread(scratch("close-a"), "close-a")
    _, lease_a = acquire(a, "close-a")
    if lease_a:
        t0 = time.monotonic()
        status = a.unsubscribe()
        lv, secs = await_status(lease_a["id"], ("dead",), limit + 10, t0)
        check("9A", R_CLOSE, f"window close (thread/unsubscribe, {status}) reads dead within the delay plus 10 s",
              secs is not None and secs <= limit, f"{secs and round(secs, 1)} s; {json.dumps(lv)}")
        release(lease_a["id"])

    old = host.thread(scratch("close-b"), "close-b")
    _, lease_b = acquire(old, "close-b")
    if lease_b:
        t0 = time.monotonic()
        fresh = host.thread(scratch("close-b2"), "close-b2")
        old.unsubscribe()
        lv, secs = await_status(lease_b["id"], ("dead",), limit + 10, t0)
        check("9B", R_CLOSE, "/new (thread/start, then unsubscribe of the old thread) reads the old one dead in time",
              secs is not None and secs <= limit, f"{secs and round(secs, 1)} s; {json.dumps(lv)}")
        res, lease_new = acquire(fresh, "close-b2")
        check("9B", R_CLOSE, "the new thread acquires and reads alive",
              res.exit == 0 and bool(lease_new) and lease_new["liveness"]["status"] == "alive", repr(res)[:200])
        release(lease_b["id"])
        if lease_new:
            release(lease_new["id"])

    other = host.thread(scratch("close-c0"), "close-c0")
    other.turn("true")
    other.unsubscribe()
    wait_for(lambda: not other.loaded(), DELAY + 8, 0.5)
    cur = host.thread(scratch("close-c"), "close-c")
    _, lease_c = acquire(cur, "close-c")
    if lease_c:
        t0 = time.monotonic()
        other.resume()
        cur.unsubscribe()
        lv, secs = await_status(lease_c["id"], ("dead",), limit + 10, t0)
        check("9C", R_CLOSE, "/resume (thread/resume of another thread, unsubscribe of this one) reads this one dead",
              secs is not None and secs <= limit, f"{secs and round(secs, 1)} s; {json.dumps(lv)}")
        check("9C", R_CLOSE, "the resumed thread is loaded", other.loaded(), "")
        release(lease_c["id"])


def step_restart() -> None:
    h1 = fresh_host(PLAIN)
    s1 = h1.thread(scratch("rs-1"), "rs-1")
    _, lease1 = acquire(s1, "rs-1")
    s2 = h1.thread(scratch("rs-2"), "rs-2")
    _, lease2 = acquire(s2, "rs-2")
    s3 = h1.thread(scratch("rs-3"), "rs-3")
    _, lease3 = acquire(s3, "rs-3")
    s5 = h1.thread(scratch("rs-5"), "rs-5")
    _, lease5 = acquire(s5, "rs-5")
    if not (lease1 and lease2 and lease3 and lease5):
        raise RuntimeError("the restart step could not take its four leases")
    s2.unsubscribe()
    await_status(lease2["id"], ("dead",), DELAY + 12)

    # An end mark becomes an ended registration under a lock holder. A call from a live session
    # of the host is one, and it must land before the host dies.
    s1.turn(f"{BIN} advertise --name before-kill")
    broken = nitipan("break", "--force", lease3["id"])
    old_pid = h1.pid
    h1.stop(signal.SIGKILL)
    lv1, _ = await_status(lease1["id"], ("unknown", "alive", "dead"), 1)
    lv2, _ = await_status(lease2["id"], ("unknown", "alive", "dead"), 1)
    check("11", R_RESTART, "after a host kill, a live session's lease reads unknown", lv1["status"] == "unknown",
          json.dumps(lv1))
    check("11", R_RESTART, "after a host kill, an ended session's lease still reads dead", lv2["status"] == "dead",
          json.dumps(lv2))

    h2 = fresh_host(PLAIN)
    s1.resume(h2)
    res = s1.turn(f"{BIN} advertise --name moved-check")
    moved = lease_by(id=lease1["id"]) or {}
    check("11", R_RESTART, "a resumed session's next call moves its leases and reports the new host",
          "runs under a new" in res.text and f"pid {h2.pid}" in res.text and f"pid {old_pid}" in res.text,
          repr(res)[:400])
    check("11", R_RESTART, "the moved lease names the new host and reads alive",
          lookup_path(moved, "holder.pid") == h2.pid and lookup_path(moved, "liveness.status") == "alive",
          json.dumps(moved.get("holder")) + " " + json.dumps(moved.get("liveness")))
    still = liveness(lease2["id"])
    check("11", R_RESTART, "the ended session still reads dead under the new host", still["status"] == "dead",
          json.dumps(still))
    s3.resume(h2)
    res3 = s3.turn(f"{BIN} list")
    check("11", R_RESTART, "a tombstone addressed to a resumed session is delivered by its next call",
          "tombstone" in res3.text and broken.returncode == 0, repr(res3)[:300])

    s5.resume(h2)
    s5.lock().unlink(missing_ok=True)
    res5 = s5.turn(f"{BIN} advertise --name no-lock")
    stayed = lease_by(id=lease5["id"]) or {}
    check("11", R_NOMOVE, "with the session's lock file missing, the call reports that its leases stay on the old host",
          lookup_path(stayed, "holder.pid") == old_pid and "stay on the old host" in res5.text,
          f"holder.pid {lookup_path(stayed, 'holder.pid')} (old {old_pid}); {res5!r}"[:300])
    check("11", R_NOMOVE, "those leases then read unknown",
          lookup_path(stayed, "liveness.status") == "unknown",
          json.dumps(stayed.get("liveness")) + ". The session registered under the new host, and the "
          "liveness rule judges by the session's newest live registration, not by the lease's holder host")
    s5.lock().touch()
    release(lease5["id"])
    release(lease1["id"])
    h2.stop()

    graceful_stop_probe()


def graceful_stop_probe() -> None:
    """Closing the host's stdin is a graceful shutdown. Record what it does to a live session."""
    host = fresh_host(PLAIN)
    ctx = host.thread(scratch("graceful"), "graceful")
    _, lease = acquire(ctx, "graceful")
    if not lease:
        return
    host.stop()
    lv, _ = await_status(lease["id"], ("unknown", "alive", "dead"), 1)
    note("11", R_RESTART, "a graceful host stop with a live session loaded reads",
         f"{lv['status']}: {lv.get('reason', '')}; end marks {len(ctx.end_marks())}")
    revived = fresh_host(PLAIN)
    ctx.resume(revived)
    release(lease["id"])
    revived.stop()


def run_exec(label: str, cmd: str, after: float) -> tuple[subprocess.Popen, Path, str]:
    home = home_for(PLAIN)
    directory = scratch("exec-" + label)
    out = WORK / f"exec-{label}.jsonl"
    argv = ["codex", "exec", "--skip-git-repo-check", "--json", "-C", str(directory),
            encode_command(cmd, 0, after)]
    proc = subprocess.Popen(argv, cwd=directory, env=codex_env(home), stdin=subprocess.DEVNULL,
                            stdout=out.open("w"), stderr=(WORK / f"exec-{label}.err").open("w"),
                            start_new_session=True)
    OWN_PIDS.add(proc.pid)
    EXECS.append(proc)
    log("exec_start", label=label, pid=proc.pid, argv=argv[:6])
    return proc, out, str(directory)


def thread_id_from(out: Path) -> str:
    try:
        for line in out.read_text().splitlines():
            try:
                msg = json.loads(line)
            except ValueError:
                continue
            if msg.get("type") == "thread.started":
                return msg.get("thread_id", "")
    except OSError:
        pass
    return ""


def step_exec() -> None:
    desc = f"harness {RUNID} exec-exit"
    proc, out, directory = run_exec("exit", f"{BIN} acquire --checkout {shlex.quote(directory_of('exec-exit'))} "
                                    f"--description {shlex.quote(desc)}", 0)
    lease = wait_for(lambda: lease_by(description=desc), 60, 0.5)
    session = thread_id_from(out) or (lease or {}).get("holder", {}).get("session_id", "")
    if session:
        SESSIONS.add(session)
    if not lease:
        check("13", R_EMBED, "codex exec acquires a lease under the mock provider", False,
              (WORK / "exec-exit.err").read_text()[-300:])
        kill_group(proc)
        return
    own(lease, session, "break")
    reg = next(iter(sorted(REGISTERED.glob(f"codex.{session}.*"))), None)
    mode = json.loads(reg.read_text()).get("mode") if reg else None
    check("4c", R_HOST, "codex exec is recorded as an embedded host (mode tui)", mode == "tui",
          f"mode {mode}; pid {proc.pid}; argv {host_command(proc.pid) or 'exited'}")
    check("4b", R_HOST, "the lease's holder.pid is the codex exec process (the walk reaches it)",
          lease["holder"]["pid"] == proc.pid, f"{lease['holder']['pid']} vs {proc.pid}")
    proc.wait(timeout=60)
    t0 = time.monotonic()
    lv, secs = await_status(lease["id"], ("dead",), 15, t0)
    check("13", R_EMBED, "an embedded session reads dead within a few seconds of its host exiting",
          secs is not None and secs <= 5, f"{secs and round(secs, 1)} s; {json.dumps(lv)}")
    release(lease["id"])

    desc = f"harness {RUNID} exec-kill"
    proc, out, directory = run_exec("kill", f"{BIN} acquire --checkout {shlex.quote(directory_of('exec-kill'))} "
                                    f"--description {shlex.quote(desc)}", 60)
    lease = wait_for(lambda: lease_by(description=desc), 60, 0.5)
    if not lease:
        check("13", R_EMBED, "a blocked codex exec holds a lease", False, (WORK / "exec-kill.err").read_text()[-300:])
        kill_group(proc)
        return
    session = lease["holder"]["session_id"]
    SESSIONS.add(session)
    own(lease, session, "break")
    alive = liveness(lease["id"])
    t0 = time.monotonic()
    kill_group(proc)
    lv, secs = await_status(lease["id"], ("dead",), 15, t0)
    check("13", R_EMBED, "kill -9 of the embedded host mid-turn reads dead within a few seconds",
          alive["status"] == "alive" and secs is not None and secs <= 5,
          f"before {alive['status']}; after {secs and round(secs, 1)} s {json.dumps(lv)}")
    release(lease["id"])


def kill_group(proc: subprocess.Popen) -> None:
    """SIGKILL a child the harness started in its own session, then reap it. Nothing is signaled
    once it is reaped, because its PID may belong to another process by then."""
    if proc.returncode is None:
        try:
            os.killpg(proc.pid, signal.SIGKILL)
        except (ProcessLookupError, PermissionError):
            pass
        proc.wait(timeout=10)


def directory_of(label: str) -> str:
    return str(scratch(label))


def tui_spec(directory: Path) -> Spec:
    """The TUI rewrites config.toml at startup for its first-run notices, which makes the file newer
    than the host. The notices are written in advance so the file stays older."""
    notices = '[tui]\nscreen_reader_detection_done = true\n\n[tui.model_availability_nux]\n"gpt-6.1-sol" = 99'
    return Spec("tui", project=directory.resolve(), extra=notices)


def tui_send(tui: Pty, line: str) -> None:
    """Type a line once the TUI is idle. Enter is ignored while a turn runs, and the text only
    queues."""
    wait_for(lambda: "Worked for" in tui.text(), 60, 0.5)
    tui.type(line)
    time.sleep(0.7)
    tui.type("\r")


def tui_wait_lease(desc: str, tui: Pty, timeout: float = 90) -> dict | None:
    return wait_for(lambda: lease_by(description=desc) if tui.alive() else None, timeout, 1)


def step_tui() -> None:
    directory = scratch("tui")
    spec = tui_spec(directory)
    home = home_for(spec)
    desc = f"harness {RUNID} tui-exit"
    cmd = (f"{BIN} acquire --checkout {shlex.quote(str(scratch('tui-co1')))} --description {shlex.quote(desc)}; "
           "echo $$ $PPID; sleep 6")
    argv = ["codex", "--no-daemon", "--no-alt-screen", "-C", str(directory), encode_command(cmd)]
    tui = Pty("codex-tui-1", argv, directory, codex_env(home))
    lease = tui_wait_lease(desc, tui)
    if not lease:
        if "unload delay" in tui.text():
            check("4b", R_HOST, "an embedded TUI acquires with the printed entries", False,
                  "the TUI rewrote config.toml after it started, so the config reads newer than the host and "
                  "the unload delay is unknown: " + tui.tail())
        else:
            check("4b", R_HOST, "a codex --no-daemon TUI runs a scripted turn", None,
                  "SKIP: the TUI never acquired. Transcript tail: " + tui.tail())
        tui.stop()
        return
    own(lease, lease["holder"]["session_id"], "break")
    SESSIONS.add(lease["holder"]["session_id"])
    reg = next(iter(sorted(REGISTERED.glob(f"codex.{lease['holder']['session_id']}.*"))), None)
    mode = json.loads(reg.read_text()).get("mode") if reg else None
    check("4b", R_HOST, "under --no-daemon, holder.pid is the TUI process, so the shell's ancestry reaches it",
          lease["holder"]["pid"] == tui.pid, f"{lease['holder']['pid']} vs TUI {tui.pid}")
    check("4c", R_HOST, "the TUI host is recorded as embedded (mode tui) and its argv has no app-server",
          mode == "tui" and "app-server" not in host_command(tui.pid), f"mode {mode}; {host_command(tui.pid)}")
    tui_send(tui, "/exit")
    t0 = time.monotonic()
    if not wait_for(lambda: not tui.alive(), 15):
        tui.type("\x03")
        time.sleep(0.5)
        tui.type("\x03")
        wait_for(lambda: not tui.alive(), 10)
    lv, secs = await_status(lease["id"], ("dead",), 15, t0)
    check("13", R_EMBED, "a TUI that exits reads dead within a few seconds", secs is not None and secs <= 10,
          f"{secs and round(secs, 1)} s; {json.dumps(lv)}; TUI exited {not tui.alive()}")
    release(lease["id"])

    desc = f"harness {RUNID} tui-kill"
    cmd = f"{BIN} acquire --checkout {shlex.quote(str(scratch('tui-co2')))} --description {shlex.quote(desc)}"
    tui2 = Pty("codex-tui-2", ["codex", "--no-daemon", "--no-alt-screen", "-C", str(directory),
                               encode_command(cmd, 0, 60)], directory, codex_env(home))
    lease = tui_wait_lease(desc, tui2)
    if not lease:
        check("13", R_EMBED, "a second TUI holds a lease mid-turn", None, "SKIP: no lease. " + tui2.tail())
        tui2.stop()
        return
    own(lease, lease["holder"]["session_id"], "break")
    SESSIONS.add(lease["holder"]["session_id"])
    t0 = time.monotonic()
    tui2.kill()
    lv, secs = await_status(lease["id"], ("dead",), 15, t0)
    check("13", R_EMBED, "kill -9 of the TUI mid-turn reads dead within a few seconds",
          secs is not None and secs <= 5, f"{secs and round(secs, 1)} s; {json.dumps(lv)}")
    release(lease["id"])


# The plan. It is the single list the dry run prints and the driver walks.


@dataclass
class Step:
    name: str
    fn: Callable[[], None]
    what: str
    covers: list[tuple[str, str]] = field(default_factory=list)
    always: bool = False


STEPS = [
    Step("install", step_install, "make build, fake Responses API, trust bootstrap, start the plain host",
         [("0", R_ENTRIES), ("0", R_HOOKENV), ("0", R_HOST)], always=True),
    Step("identity", step_identity,
         "thread a: first-turn acquire, variables, holder, sandbox list, registration, second thread, modes",
         [("1", R_ID), ("3a", R_SYSCTL), ("3a", R_ENTRIES), ("7", R_HOOKENV), ("7", R_GATE), ("4a", R_HOST),
          ("5", R_MANY), ("5", R_DECL), ("15", R_MODES), ("2", R_ENV), ("8", R_LOCK)]),
    Step("policies", step_policies, "three shell_environment_policy homes: variables survive, acquire works",
         [("2", R_ENV)]),
    Step("noroot", step_noroot, "a home without the writable root: acquire names the sandbox",
         [("3b", R_ENTRIES)]),
    Step("gates", step_gates, "no registration, delay 1800, config newer than host, no SessionEnd, hook timeout",
         [("14", R_GATE), ("14", R_PIDERR), ("14", R_NOEND)]),
    Step("locks", step_locks, "missing lock file refuses acquire, unreadable lock directory reads unknown",
         [("14", R_LOCK)]),
    Step("trust", step_hint, "trust one hook only, in each direction: which runs, and the likely-ended hint",
         [("12", R_HOOKENV), ("14", R_HINT)]),
    Step("mcp", step_mcp, "a stdio MCP server dumps its environment", [("6", R_MCP)]),
    Step("order", step_order, "hook wrapper records the lock file: SessionStart order, late SessionEnd",
         [("8", R_LOCK), ("7", R_HOOKENV), ("10", R_LATE)]),
    Step("close", step_close, "window close, /new, /resume: seconds until dead", [("9A", R_CLOSE), ("9B", R_CLOSE),
                                                                                  ("9C", R_CLOSE)]),
    Step("restart", step_restart, "SIGKILL the host, new host, resume: unknown, dead, move, tombstone, graceful stop",
         [("11", R_RESTART)]),
    Step("exec", step_exec, "codex exec: embedded mode, exit reads dead, kill -9 reads dead",
         [("4b", R_HOST), ("4c", R_HOST), ("13", R_EMBED)]),
    Step("tui", step_tui, "codex --no-daemon under a pty: ancestry, mode, /exit, kill -9",
         [("4b", R_HOST), ("4c", R_HOST), ("13", R_EMBED)]),
]


def selected(only: set[str] | None) -> list[Step]:
    return [s for s in STEPS if s.always or not only or s.name in only]


def print_plan(opts: argparse.Namespace, only: set[str] | None) -> None:
    print("nitipan Codex live checks: plan (dry run, nothing touched)\n")
    print(f"results: {opts.report or RESULTS_PATH}\nwork directory: {WORK} (created when the run starts)\n")
    print(f"{'step':10}  what (handoff step -> As built row)")
    for s in selected(only):
        print(f"{s.name:10}  {s.what}")
        for handoff, row in s.covers:
            print(f"{'':12}  {handoff:4} -> {row}")
    print("\nnot automated:")
    print("  - the user's own Codex daemon and TUIs, which the harness never starts, stops, or reads")
    print("  - Codex model behavior, which the fake provider replaces")
    print("\nreal model calls: 0 planned. Estimated model cost: $0.00.")
    print("Every turn is answered by a fake Responses API that the harness serves on 127.0.0.1.")
    print("\nisolation:")
    print(f"  - every Codex process runs with a private CODEX_HOME under {HOMES}")
    print("  - the harness starts its own `codex app-server --listen stdio://`, `codex exec`, and")
    print("    `codex --no-daemon`. It never uses `codex app-server daemon`, and never edits ~/.codex")
    print("  - nitipan's state directory is the real one. Only leases acquired here are released, by recorded")
    print("    ID, and only session files named by this run's own sessions and processes are removed")
    print("  - the user does not need to close Codex")


# Driver.


def guard_snapshot() -> dict:
    """What the user's real Codex looks like, to show the run did not disturb it."""
    def mtime(path: Path):
        try:
            return path.stat().st_mtime
        except OSError:
            return None

    table = process_table()
    return {"config": mtime(REAL_CODEX_HOME / "config.toml"), "hooks": mtime(REAL_CODEX_HOME / "hooks.json"),
            "daemons": sorted(p for p, (_, cmd) in table.items() if "app-server" in cmd and "--managed-daemon" in cmd)}


def apply_cleanup() -> list[str]:
    """Undo everything the run created. Each part runs even when an earlier one fails."""
    common.CLEANING = True
    common.release_own(BASELINE)
    for tui in TUIS:
        try:
            tui.stop()
        except Exception as e:
            log("cleanup_error", tui=tui.label, error=repr(e))
    for proc in EXECS:
        kill_group(proc)
    for host in HOSTS:
        try:
            host.stop()
        except Exception as e:
            log("cleanup_error", host=host.label, error=repr(e))
    if FAKE:
        try:
            FAKE.stop()
        except Exception as e:
            log("cleanup_error", what="fake provider", error=repr(e))
    common.sweep_marks((REGISTERED, ENDED))
    sweep_config_memory()
    return [x["id"] for x in leases() if x["id"] in common.OWN]


def sweep_config_memory() -> None:
    """Remove only the config memory files that name this run's private homes."""
    for name in CONFIG_FILES:
        try:
            (common.STATE / "sessions/config" / name).unlink()
            log("config_removed", name=name)
        except FileNotFoundError:
            pass
        except OSError as e:
            log("cleanup_error", path=name, error=repr(e))


def preflight() -> list[str]:
    problems = []
    if any(k in os.environ for k in ("CODEX_THREAD_ID", "CODEX_SESSION_ID")):
        problems.append("run this from a plain terminal, not from inside Codex")
    for tool in ("codex", "make", "python3"):
        if shutil.which(tool) is None:
            problems.append(f"{tool} is not on PATH")
    return problems


def open_results() -> None:
    version = subprocess.run(["codex", "--version"], capture_output=True, text=True).stdout.strip()
    macos = subprocess.run(["sw_vers", "-productVersion"], capture_output=True, text=True).stdout.strip()
    common.open_sink(RESULTS_PATH, [
        "# NIT-001 M4: Codex live results (automated harness)",
        "",
        f"- Started {time.strftime('%Y-%m-%d %H:%M:%S')}; run `{RUNID}`; work directory `{WORK}`",
        f"- {version}; macOS {macos}; private CODEX_HOME per variant, own app-server host, fake Responses API,"
        " no real model",
        "- Rows are written as each check finishes. A run that stops early leaves the rows so far, and no totals.",
        "",
        "| Handoff step | As built row | Check | Result | Detail |",
        "|---|---|---|---|---|",
        "",
    ])


def close_results(leftover: list[str]) -> None:
    counts = common.totals()
    after = guard_snapshot()
    untouched = after == GUARD
    common.close_sink([
        "",
        f"Totals: {', '.join(f'{v} {k}' for k, v in counts.items())}.",
        f"Real model calls: 0. Fake Responses requests answered: {len(HITS)}.",
        f"The user's ~/.codex config and hooks mtimes and the managed daemon PIDs are "
        f"{'unchanged' if untouched else 'CHANGED: ' + json.dumps({'before': GUARD, 'after': after})}.",
        f"Leases at start: {BASELINE or 'none'}. Left behind by this run: {leftover or 'none'}.",
        f"Raw events: `{WORK / 'events.jsonl'}`. Host logs, hook logs, and TUI transcripts are beside it.",
        "",
    ])


# Probe: exercise only the parts that rest on source reading.


def probe() -> int:
    global FAKE
    results = []

    def step(name: str, ok: bool, detail: str = "") -> bool:
        results.append((name, ok, detail))
        print(f"  {'OK  ' if ok else 'FAIL'} {name}" + (f" -- {detail}" if detail else ""), flush=True)
        return ok

    print(f"probe in {WORK}", flush=True)
    common.open_events()
    before = guard_snapshot()
    try:
        r = run(["make", "build"], timeout=600)
        step("make build", r.returncode == 0, r.stderr.strip()[-200:])
        FAKE = Fake()
        FAKE.start()
        write_support()
        spec = Spec("probe", ordered=True)
        try:
            home = make_home(spec)
        except Exception as e:
            step("private home: config, hooks/list, trust write via config/batchWrite", False, repr(e))
            return 1
        step("private home: config, hooks/list, trust write via config/batchWrite",
             TRUST.get("probe", {}).get("sessionStart") == "trusted" and TRUST["probe"].get("sessionEnd") == "trusted",
             json.dumps(TRUST.get("probe")))
        step("thread_unload_delay_secs still precedes the first table after the trust write",
             delay_precedes_tables((home / "config.toml").read_text()))
        host = Host("probe", home)
        info = host.initialize()
        step("host handshake: initialize, then initialized, with no login", True, json.dumps(info)[:120])
        ctx = host.thread(scratch("probe"), "probe")
        step("thread/start creates the writer-lock file before any turn", ctx.lock().exists(), str(ctx.lock()))
        res = ctx.turn("/usr/bin/env | /usr/bin/grep '^CODEX_' | /usr/bin/sort; echo hi")
        step("the fake saw a Responses request and Codex ran the scripted command", res.exit == 0 and "hi" in res.text
             and bool(HITS), f"{res!r}; paths {sorted(set(HITS))}")
        tools = [e for e in open(WORK / "events.jsonl").read().splitlines() if '"fake_request"' in e][:1]
        step("the request's tool list names a shell tool", bool(tools) and any(
            n in tools[0] for n in ("exec_command", "shell_command", "shell")), tools[0][:200] if tools else "")
        step("both CODEX_THREAD_ID and CODEX_SESSION_ID reach the sandboxed command",
             "CODEX_THREAD_ID=" in res.text and "CODEX_SESSION_ID=" in res.text, res.text[:160])
        started = host.find("hook/started", 0, **{"run.eventName": "sessionStart"})
        step("hook/started for sessionStart arrived, and the hook log shows the lock file at that time",
             started is not None and any(r["hook"] == "session-start" and r["lock"]["exists"]
                                         for r in read_hook_log()), "")
        reg = ctx.registration()
        step("a registration names the private CODEX_HOME and mode daemon",
             bool(reg) and reg.get("mode") == "daemon"
             and Path(reg.get("codex_home", "")).resolve() == home.resolve(), json.dumps(reg)[:200])
        mark = host.mark()
        slow = threading.Thread(target=lambda: ctx.turn("true", before=3, timeout=30), daemon=True)
        slow.start()
        time.sleep(1.5)
        running = (host.find("turn/started", mark, threadId=ctx.id) is not None
                   and host.find("turn/completed", mark, threadId=ctx.id) is None)
        slow.join(30)
        step("a fake-side hold keeps the turn running, and the turn completes once released",
             running and host.find("turn/completed", mark, threadId=ctx.id) is not None)
        t0 = time.monotonic()
        step("thread/unsubscribe answers", ctx.unsubscribe() == "unsubscribed")
        closed = host.wait_note("thread/closed", DELAY + 8, 0, threadId=ctx.id)
        step("thread/closed arrives within the delay plus 5 s", closed is not None,
             f"{time.monotonic() - t0:.1f} s")
        step("the SessionEnd hook ran and left an end mark", bool(wait_for(lambda: ctx.end_marks(), 5, 0.3)),
             str(ctx.end_marks()))
        step("the lock file is gone after the unload", not ctx.lock().exists())
        host.stop()
        step("the host exits when its stdin closes", not host.alive())

        first = Host("probe-kill", home)
        first.initialize()
        survivor = first.thread(scratch("probe-kill"), "probe-kill")
        survivor.turn("true")
        first.stop(signal.SIGKILL)
        second = Host("probe-resume", home)
        second.initialize()
        try:
            survivor.resume(second)
            resumed = True
        except RpcError as e:
            resumed = False
            print(f"    {e}")
        step("a thread persisted before a host kill resumes under a new host", resumed)

        proc, out, _ = run_exec("probe", "echo exec-ok", 0)
        step("codex exec runs against the mock provider", proc.wait(timeout=90) == 0,
             (WORK / "exec-probe.err").read_text()[-200:])
        sid = thread_id_from(out)
        reg = next(iter(sorted(REGISTERED.glob(f"codex.{sid}.*"))), None) if sid else None
        step("codex exec registers as an embedded host (mode tui)",
             bool(reg) and json.loads(reg.read_text()).get("mode") == "tui", str(reg))
        SESSIONS.add(sid)

        flag = scratch("probe-tui") / "flag"
        cmd = f"echo started > {shlex.quote(str(flag))}; sleep 3"
        tui = Pty("probe-tui", ["codex", "--no-daemon", "--no-alt-screen", "-C", str(flag.parent),
                                encode_command(cmd)], flag.parent,
                  codex_env(home_for(tui_spec(flag.parent))))
        step("a codex --no-daemon TUI auto-submits its prompt argument and runs the command",
             bool(wait_for(flag.exists, 60, 1)), tui.tail())
        tui_send(tui, "/exit")
        step("/exit ends the TUI", bool(wait_for(lambda: not tui.alive(), 15)))
        tui.stop()

        after = guard_snapshot()
        step("the user's ~/.codex config, hooks, and managed daemon are untouched", after == before,
             json.dumps({"before": before, "after": after}) if after != before else "")
        print("\nprobe finished", flush=True)
        return 0 if all(ok for _, ok, _ in results) else 1
    finally:
        common.CLEANING = True
        for t in TUIS:
            t.stop()
        for p in EXECS:
            if p.poll() is None:
                kill_group(p)
        for h in HOSTS:
            h.stop()
        if FAKE:
            FAKE.stop()
        common.sweep_marks((REGISTERED, ENDED))
        sweep_config_memory()


# Selftest: the fake server's pure logic, with no sockets and no Codex.


def selftest() -> int:
    tools = [{"type": "function", "name": "exec_command"}, {"type": "namespace", "tools": [{"name": "inner"}]}]
    assert tool_names(tools) == ["exec_command", "inner"], tool_names(tools)

    prompt = encode_command("echo hi", before=1.5, after=2.5)
    assert decode_marker(prompt) == {"cmd": "echo hi", "before": 1.5, "after": 2.5, "yield_ms": 60000}
    assert decode_marker("no marker") is None
    assert decode_marker("HARNESS-CMD:!!!:END") is None

    user = {"type": "message", "role": "user", "content": [{"type": "input_text", "text": prompt}]}
    env = {"type": "message", "role": "user", "content": [{"type": "input_text", "text": "<environment>"}]}
    request = {"tools": tools, "input": [env, user]}
    answer = reply(request)
    assert answer["kind"] == "tool" and answer["name"] == "exec_command" and answer["before"] == 1.5, answer
    assert json.loads(answer["arguments"]) == {"cmd": "echo hi", "login": False, "yield_time_ms": 60000}

    output = {"type": "function_call_output", "call_id": "c", "output": "hi"}
    answer = reply({"tools": tools, "input": [env, user, {"type": "function_call", "name": "exec_command"}, output]})
    assert answer == {"kind": "text", "text": "ok", "before": 2.5}, answer

    old_turn = [env, user, {"type": "function_call", "name": "x"}, output,
                {"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "ok"}]},
                {"type": "message", "role": "user", "content": [{"type": "input_text", "text": "plain"}]}]
    assert reply({"tools": tools, "input": old_turn}) == {"kind": "text", "text": "ok"}

    for name, key in (("shell_command", "command"), ("shell", "command")):
        answer = reply({"tools": [{"name": name}], "input": [user]})
        assert answer["name"] == name and key in json.loads(answer["arguments"]), answer
    assert reply({"tools": [{"name": "other"}], "input": [user]}).get("noTool") is True
    assert reply({}) == {"kind": "text", "text": "ok"}

    body = sse_body(sse_events(reply(request), "call_1")).decode()
    kinds = [line.split(": ")[1] for line in body.splitlines() if line.startswith("event: ")]
    assert kinds == ["response.created", "response.output_item.done", "response.completed"], kinds
    data = [json.loads(line[6:]) for line in body.splitlines() if line.startswith("data: ")]
    assert data[1]["item"]["type"] == "function_call" and data[1]["item"]["call_id"] == "call_1"
    final = sse_body(sse_events({"kind": "text", "text": "ok"}, "x")).decode()
    assert '"output_text"' in final and '"message"' in final

    text = config_text(Spec("t"), 4321)
    assert delay_precedes_tables(text)
    assert f"{EXE} hook session-end" in text.replace('\\"', '"')
    assert "[sandbox_workspace_write]" in text and "[[hooks.SessionEnd]]" in text
    assert "SessionEnd" not in config_text(Spec("t", end=False), 1)
    assert "writable_roots" not in config_text(Spec("t", root=False), 1)
    assert not delay_precedes_tables("[a]\nthread_unload_delay_secs = 3\n")
    assert "thread_unload_delay_secs" not in config_text(Spec("t", delay=None), 1)
    ordered = config_text(Spec("t", ordered=True), 1)
    assert f"{EXE} hook session-end" in ordered and str(HOOKWRAP_PATH) in ordered

    assert lookup_path({"a": {"b": 3}}, "a.b") == 3 and lookup_path({}, "a.b") is None
    assert json_in('noise {"a": 1} tail') == {"a": 1} and json_in("none") is None
    assert TurnResult("completed", [{"exitCode": 2, "aggregatedOutput": "x\r\ny\r\n"}]).text == "x\ny"
    print("selftest ok")
    return 0


def main() -> int:
    global BASELINE, GUARD
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--only", help="comma-separated step names to run: " + ",".join(s.name for s in STEPS))
    parser.add_argument("--report", type=Path, help="also copy the results here when the run ends")
    parser.add_argument("--dry-run", action="store_true", help="print the plan and exit; touch nothing")
    parser.add_argument("--probe", action="store_true",
                        help="check the parts that rest on source reading (private home, trust, fake "
                             "Responses API, codex exec, a TUI under a pty) and exit")
    parser.add_argument("--selftest", action="store_true",
                        help="check the fake server's pure logic offline and exit; touch nothing")
    opts = parser.parse_args()
    only = set(opts.only.split(",")) if opts.only else None
    if only and only - {s.name for s in STEPS}:
        parser.error(f"unknown step in --only: {sorted(only - {s.name for s in STEPS})}")

    if opts.selftest:
        return selftest()
    if opts.dry_run:
        print_plan(opts, only)
        return 0

    problems = preflight()
    if problems:
        print("nitipan Codex live checks cannot start:\n  - " + "\n  - ".join(problems), file=sys.stderr)
        return 2
    common.install_signal_handlers()

    if opts.probe:
        try:
            return probe()
        except common.Interrupted as e:
            print(f"interrupted by {e}", file=sys.stderr)
            return 130

    common.open_events()
    open_results()
    print(f"work directory: {WORK}\nresults: {RESULTS_PATH}", flush=True)
    common.log("run_start", run=RUNID, argv=sys.argv)

    leftover: list[str] = []
    try:
        GUARD = guard_snapshot()
        BASELINE = [x["id"] for x in leases()]
        log("baseline", leases=BASELINE, guard=GUARD)
        for s in selected(only):
            print(f"== {s.name}", flush=True)
            try:
                s.fn()
            except Exception as e:
                common.record(s.name, "-", "step raised", "ERROR", repr(e))
                log("error", step=s.name, error=repr(e), trace=traceback.format_exc())
                if s.name == "install":
                    break
    except common.Interrupted as e:
        common.record("-", "-", "interrupted", "ERROR", f"signal {e}; cleaning up")
    finally:
        leftover = apply_cleanup()
        log("run_end", own={k: {"session": v["session"], "done": v["done"]} for k, v in common.OWN.items()},
            leftover=leftover)
        close_results(leftover)
        if opts.report:
            shutil.copyfile(RESULTS_PATH, opts.report)
        print(f"\nresults: {RESULTS_PATH}\nfake Responses requests: {len(HITS)}; real model calls: 0", flush=True)
    return 1 if any(r["status"] in ("FAIL", "ERROR") for r in common.RESULTS) else 0


if __name__ == "__main__":
    sys.exit(main())
