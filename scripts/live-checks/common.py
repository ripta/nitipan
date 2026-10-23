"""Helpers the live-check harnesses share.

Each harness is its own process, so the run state here is module state: one run ID, one work
directory, one results file, and the lease and process bookkeeping that cleanup reads. A harness
sets the few scalars it owns through open_events and open_sink, and reads the containers directly.
"""

from __future__ import annotations

import fcntl
import json
import os
import pty
import re
import shlex
import signal
import struct
import subprocess
import tempfile
import termios
import threading
import time
import uuid
from http.server import ThreadingHTTPServer
from pathlib import Path
from typing import Callable

ROOT = Path(__file__).resolve().parents[2]
BIN = ROOT / "build" / "nitipan"
HOME = Path.home()
STATE = HOME / ".local/state/nitipan"
PRESENCE = STATE / "sessions/presence"
EXECUTING = STATE / "sessions/executing"
REGISTERED = STATE / "sessions/registered"
ENDED = STATE / "sessions/ended"

RUNID = uuid.uuid4().hex[:8]
START_TS = time.time()
WORK = Path(tempfile.gettempdir()) / f"nitipan-live-{RUNID}"

# Agent identity leaks into nitipan's process walk, so no command the harness runs may inherit it.
AGENT_VARS = ("OPENCODE_SESSION_ID", "OPENCODE", "AGENT", "AI_AGENT", "OPENCODE_TERMINAL")
CLEAN_ENV = {k: v for k, v in os.environ.items()
             if k not in AGENT_VARS and not k.startswith(("XDG_", "CODEX_", "CLAUDE"))}

EVENTS = None
SINK = None
RESULTS: list[dict] = []
OWN: dict[str, dict] = {}
SESSIONS: set[str] = set()
OWN_PIDS: set[int] = set()
TUIS: list["Pty"] = []
CLEANING = False

# Set by a harness that can release a lease from inside the holder's own session. It returns True
# once the lease is gone. Without it, or when it fails, release falls back to `break`.
TURN_RELEASE: Callable[[dict, str], bool] | None = None


class Interrupted(BaseException):
    """A termination signal. It is a BaseException so no step's `except Exception` swallows it."""


# Logging and results.


def open_events() -> None:
    global EVENTS
    WORK.mkdir(parents=True, exist_ok=True)
    EVENTS = (WORK / "events.jsonl").open("a")


def log(kind: str, **kw) -> None:
    if EVENTS is None:
        return
    EVENTS.write(json.dumps({"time": time.strftime("%H:%M:%S"), "kind": kind, **kw}, default=str) + "\n")
    EVENTS.flush()


def open_sink(path: Path, header: list[str]) -> None:
    global SINK
    path.parent.mkdir(parents=True, exist_ok=True)
    SINK = path.open("w")
    SINK.write("\n".join(header))
    SINK.flush()


def close_sink(footer: list[str]) -> None:
    SINK.write("\n".join(footer))
    SINK.close()


def record(step: str, row: str, name: str, status: str, detail: str) -> None:
    RESULTS.append({"step": step, "row": row, "check": name, "status": status, "detail": detail})
    log("check", step=step, row=row, check=name, status=status, detail=detail)
    if SINK is not None:
        cells = [step, row, name, status, detail.replace("|", "\\|").replace("\n", " ")[:300]]
        SINK.write("| " + " | ".join(cells) + " |\n")
        SINK.flush()
        os.fsync(SINK.fileno())
    shown = f" -- {detail}" if detail and status not in ("PASS", "INFO") else ""
    print(f"  {status:5} {step:5} {name}{shown}", flush=True)


def check(step: str, row: str, name: str, ok: bool | None, detail: str = "") -> None:
    record(step, row, name, {True: "PASS", False: "FAIL", None: "SKIP"}[ok], detail)


def note(step: str, row: str, name: str, detail: str) -> None:
    record(step, row, name, "INFO", detail)


def totals() -> dict[str, int]:
    return {s: sum(r["status"] == s for r in RESULTS) for s in ("PASS", "FAIL", "ERROR", "SKIP", "INFO")}


def wait_for(fn, timeout: float, interval: float = 0.5):
    until = time.monotonic() + timeout
    while True:
        value = fn()
        if value or time.monotonic() >= until:
            return value
        time.sleep(interval)


# Processes and nitipan.


def run(args: list[str], timeout: float = 60, env: dict | None = None, cwd: Path | None = None):
    r = subprocess.run(args, capture_output=True, text=True, timeout=timeout, env=env or CLEAN_ENV,
                       cwd=cwd or ROOT)
    log("command", argv=args, exit=r.returncode, stdout=r.stdout[-4000:], stderr=r.stderr[-2000:])
    return r


def nitipan(*args: str) -> subprocess.CompletedProcess:
    return run([str(BIN), *args])


def process_table() -> dict[int, tuple[int, str]]:
    r = subprocess.run(["ps", "-axo", "pid=,ppid=,command="], capture_output=True, text=True)
    table = {}
    for line in r.stdout.splitlines():
        parts = line.split(None, 2)
        if len(parts) == 3 and parts[0].isdigit() and parts[1].isdigit():
            table[int(parts[0])] = (int(parts[1]), parts[2])
    return table


def children_of(pid: int) -> list[int]:
    return [p for p, (ppid, _) in process_table().items() if ppid == pid]


def leases() -> list[dict]:
    r = nitipan("list", "--json")
    try:
        return json.loads(r.stdout)["leases"]
    except (ValueError, KeyError, TypeError):
        return []


def lookup(entry: dict, dotted: str):
    for part in dotted.split("."):
        entry = entry.get(part) if isinstance(entry, dict) else None
    return entry


def lease_by(**match) -> dict | None:
    for entry in leases():
        if all(lookup(entry, k) == v for k, v in match.items()):
            return entry
    return None


def liveness(lease_id: str) -> dict:
    entry = lease_by(id=lease_id)
    return entry["liveness"] if entry else {"status": "absent"}


def marks(directory: Path, session: str) -> list[int]:
    """PIDs named by the session's mark files in a sessions/ subdirectory."""
    pids = []
    try:
        for name in os.listdir(directory):
            parts = name.split(".")
            if len(parts) >= 4 and parts[1] == session:
                pids.append(int(parts[2]))
    except (OSError, ValueError):
        pass
    return pids


# Captured command output.


class Out:
    def __init__(self, path: Path):
        text = path.read_text() if path.exists() else ""
        lines = text.rstrip("\n").split("\n")
        self.exit = None
        if lines and lines[-1].startswith("exit="):
            self.exit = int(lines[-1][5:])
            lines = lines[:-1]
        self.text = "\n".join(lines)

    def __repr__(self) -> str:
        return f"exit={self.exit} {self.text!r}"


def wrap(cmd: str, out: Path) -> str:
    q = shlex.quote(str(out))
    return f'{{ {cmd} ; }} > {q} 2>&1; echo "exit=$?" >> {q}'


def done(out: Path) -> bool:
    return out.exists() and "exit=" in out.read_text()


def scratch(label: str) -> Path:
    path = WORK / "checkouts" / label
    path.mkdir(parents=True, exist_ok=True)
    return path


# Lease bookkeeping. Only leases recorded here are ever released or broken.


def own(lease: dict, session: str, via: str) -> None:
    OWN[lease["id"]] = {"session": session, "via": via, "done": False}


def release(lease_id: str) -> None:
    info = OWN.get(lease_id)
    if not info or info["done"]:
        return
    if lease_by(id=lease_id) is None:
        info["done"] = True
        return
    if info["via"] == "turn" and TURN_RELEASE is not None and TURN_RELEASE(info, lease_id):
        info["done"] = True
        log("released", lease=lease_id)
        return
    entry = lease_by(id=lease_id)
    if entry is None:
        info["done"] = True
        return
    args = ["break", lease_id] + ([] if entry["liveness"]["status"] == "dead" else ["--force"])
    r = nitipan(*args)
    info["done"] = r.returncode == 0
    log("broken", lease=lease_id, args=args, exit=r.returncode, output=r.stdout + r.stderr)


def release_own(baseline: list[str], runid: str = RUNID) -> None:
    """Release every recorded lease, then any lease carrying this run's description prefix."""
    baseline_ids = set(baseline)
    try:
        for lease_id in list(OWN):
            release(lease_id)
        # A lease this run created but lost track of still carries the unique description prefix.
        for entry in leases():
            description = lookup(entry, "description") or ""
            if description.startswith(f"harness {runid}") and entry["id"] not in OWN \
                    and entry["id"] not in baseline_ids:
                own(entry, "", "break")
                release(entry["id"])
    except Exception as e:
        log("cleanup_error", what="leases", error=repr(e))


def sweep_marks(directories: tuple[Path, ...]) -> None:
    """Remove only the mark files that name this run's sessions or processes."""
    for directory in directories:
        try:
            names = os.listdir(directory)
        except OSError:
            continue
        for name in names:
            parts = name.split(".")
            if len(parts) < 4:
                continue
            try:
                pid = int(parts[2])
            except ValueError:
                continue
            # A PID can be recycled, so a match on the process alone also needs a file written during
            # this run.
            mine = parts[1] in SESSIONS
            if not mine and pid in OWN_PIDS:
                try:
                    mine = (directory / name).stat().st_mtime >= START_TS
                except OSError:
                    mine = False
            if mine:
                try:
                    (directory / name).unlink()
                    log("mark_removed", path=str(directory / name))
                except OSError as e:
                    log("cleanup_error", path=str(directory / name), error=repr(e))


def on_signal(signum, frame) -> None:
    if CLEANING:
        return
    raise Interrupted(signal.Signals(signum).name)


def install_signal_handlers() -> None:
    for sig in (signal.SIGINT, signal.SIGTERM, signal.SIGHUP):
        signal.signal(sig, on_signal)


# A child process in a pseudo-terminal.


class Pty:
    """A program under a pty. The drain thread keeps its output flowing and answers cursor queries."""

    def __init__(self, label: str, argv: list[str], cwd: Path, env: dict[str, str]):
        self.label = label
        self.transcript = WORK / f"tui-{label}.log"
        pid, fd = pty.fork()
        if pid == 0:
            os.chdir(cwd)
            os.execvpe(argv[0], argv, env)
        self.pid, self.fd = pid, fd
        self.reaped = False
        OWN_PIDS.add(pid)
        fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", 40, 160, 0, 0))
        TUIS.append(self)
        threading.Thread(target=self._drain, daemon=True).start()
        log("tui_start", label=label, pid=pid, argv=argv, cwd=str(cwd), transcript=str(self.transcript))

    def _drain(self) -> None:
        with self.transcript.open("ab", buffering=0) as f:
            while True:
                try:
                    data = os.read(self.fd, 65536)
                except OSError:
                    break
                if not data:
                    break
                f.write(data)
                if b"\x1b[6n" in data:
                    try:
                        os.write(self.fd, b"\x1b[1;1R")
                    except OSError:
                        break

    def type(self, text: str) -> None:
        os.write(self.fd, text.encode())

    def alive(self) -> bool:
        if self.reaped:
            return False
        try:
            if os.waitpid(self.pid, os.WNOHANG) == (0, 0):
                return True
        except ChildProcessError:
            pass
        self.reaped = True
        return False

    def kill(self, sig: int = signal.SIGKILL) -> None:
        """Signal the child's process group. A SIGKILL is reaped before it returns, because a zombie
        still reads as a live process to nitipan. Nothing is signaled once the child is reaped, since
        its PID may belong to another process by then."""
        if self.reaped:
            return
        try:
            os.killpg(self.pid, sig)
            log("tui_signal", label=self.label, pid=self.pid, signal=sig)
        except ProcessLookupError:
            return
        except PermissionError:
            return
        if sig == signal.SIGKILL:
            wait_for(lambda: not self.alive(), 5, 0.05)

    def text(self) -> str:
        """The transcript so far, without terminal escape sequences."""
        try:
            raw = self.transcript.read_bytes().decode("utf8", "replace")
        except OSError:
            return ""
        return re.sub(r"\x1b\[[0-9;?]*[a-zA-Z]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[()][A-Z0-9]", "", raw)

    def stop(self) -> None:
        if not self.alive():
            return
        self.kill(signal.SIGTERM)
        if not wait_for(lambda: not self.alive(), 5):
            self.kill(signal.SIGKILL)
            wait_for(lambda: not self.alive(), 5)

    def tail(self) -> str:
        try:
            raw = self.transcript.read_bytes()[-1500:].decode("utf8", "replace")
        except OSError:
            return ""
        return re.sub(r"\x1b\[[0-9;?]*[a-zA-Z]", "", raw).strip()[-300:]


# A loopback HTTP server that stands in for a model provider.


class FakeServer:
    def __init__(self, handler):
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), handler)
        self.port = self.server.server_address[1]
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)

    def start(self) -> None:
        self.thread.start()

    def stop(self) -> None:
        self.server.shutdown()
        self.server.server_close()
