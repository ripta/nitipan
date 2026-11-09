#!/usr/bin/env python3
"""Probe which git hosts take the push that `nitipan push` makes.

For each remote of a clone, the probe checks that the host advertises atomic push, then makes the exact
push shape of `nitipan push`: one `git push --atomic` with a probe branch and one fence ref under
refs/nitipan/fence/, the fence pushed by SHA with `--force-with-lease`. It then fast-forwards the fence
ref, as a second push under the same root does, and deletes both refs. Each step is reported per remote.

The probe pushes to real remotes. It creates only a branch named nitipan-probe-<timestamp> and a fence
ref of the same name, and deletes both before it moves on. Run it from a throwaway clone, with the
credentials of the user who owns the remotes. It is never part of `make ci`.

    python3 scripts/live-checks/fence-hosts.py [--dry-run] [--remote NAME ...] CLONE

With --dry-run it prints the commands each remote would get and runs none of them.
"""

from __future__ import annotations

import argparse
import os
import subprocess
import sys
import time
from dataclasses import dataclass, field

FENCE_NS = "refs/nitipan/fence"
IDENTITY = {
    "GIT_AUTHOR_NAME": "nitipan", "GIT_AUTHOR_EMAIL": "nitipan@invalid",
    "GIT_COMMITTER_NAME": "nitipan", "GIT_COMMITTER_EMAIL": "nitipan@invalid",
}
STEPS = ("atomic", "create", "fast-forward", "delete")


@dataclass
class Result:
    remote: str
    url: str = ""
    steps: dict[str, str] = field(default_factory=dict)
    notes: list[str] = field(default_factory=list)

    def ok(self) -> bool:
        return all(self.steps.get(s) == "yes" for s in STEPS)


def git(clone: str, *args: str, env: dict | None = None, stdin: str | None = None,
        check: bool = True) -> subprocess.CompletedProcess:
    full_env = dict(os.environ)
    full_env.update(IDENTITY)
    full_env.update(env or {})
    full_env["GIT_TERMINAL_PROMPT"] = "0"
    proc = subprocess.run(["git", "-C", clone, *args], input=stdin, capture_output=True, text=True,
                          env=full_env, timeout=120)
    if check and proc.returncode != 0:
        raise RuntimeError(f"git {' '.join(args)}: {proc.stderr.strip() or proc.stdout.strip()}")
    return proc


def make_fence(clone: str, token: int, parent: str | None) -> str:
    """Write a fence commit the way nitipan does: one fence.json blob, a fixed identity."""
    body = f'{{"version":"1.0","token":{token},"path":"%00","lease":"probe"}}\n'
    blob = git(clone, "hash-object", "-w", "--stdin", stdin=body).stdout.strip()
    tree = git(clone, "mktree", stdin=f"100644 blob {blob}\tfence.json\n").stdout.strip()
    args = ["commit-tree", tree, "-m", "nitipan fence probe"]
    if parent:
        args += ["-p", parent]
    return git(clone, *args).stdout.strip()


def advertises_atomic(clone: str, remote: str, probe: str) -> bool:
    """A dry-run push reads the receive-pack advertisement, which lists the atomic capability."""
    proc = git(clone, "push", "--dry-run", "--no-verify", remote, f"HEAD:refs/heads/{probe}",
               env={"GIT_TRACE_PACKET": "1"}, check=False)
    seen = [line for line in proc.stderr.splitlines() if "push<" in line and "report-status" in line]
    return any(" atomic" in line for line in seen)


def probe_remote(clone: str, remote: str, root: str, stamp: str) -> Result:
    res = Result(remote=remote)
    res.url = git(clone, "remote", "get-url", "--push", remote).stdout.strip()
    branch = f"nitipan-probe-{stamp}"
    branch_ref = f"refs/heads/{branch}"
    fence_ref = f"{FENCE_NS}/{root}/{branch}"
    created = False

    try:
        res.steps["atomic"] = "yes" if advertises_atomic(clone, remote, branch) else "no"
        if res.steps["atomic"] == "no":
            res.notes.append("the host does not advertise atomic push, so nitipan push falls back to the lease check")

        # Without atomic push the fence cannot ride with the branch. The probe still learns whether the
        # host accepts refs under the fence namespace at all.
        atomic = ["--atomic"] if res.steps["atomic"] == "yes" else []

        first = make_fence(clone, 1, None)
        push = git(clone, "push", "--no-verify", *atomic, f"--force-with-lease={fence_ref}:", remote,
                   f"HEAD:{branch_ref}", f"{first}:{fence_ref}", check=False)
        created = push.returncode == 0
        res.steps["create"] = "yes" if created else "no"
        if not created:
            res.notes.append("create: " + push.stderr.strip().replace("\n", " | ")[-300:])
            return res

        second = make_fence(clone, 2, first)
        push = git(clone, "push", "--no-verify", *atomic, f"--force-with-lease={fence_ref}:{first}", remote,
                   f"HEAD:{branch_ref}", f"{second}:{fence_ref}", check=False)
        res.steps["fast-forward"] = "yes" if push.returncode == 0 else "no"
        if push.returncode != 0:
            res.notes.append("fast-forward: " + push.stderr.strip().replace("\n", " | ")[-300:])
    except (RuntimeError, subprocess.TimeoutExpired) as err:
        res.notes.append(f"error: {err}")
    finally:
        if created:
            gone = git(clone, "push", "--no-verify", remote, f":{fence_ref}", f":{branch_ref}", check=False)
            res.steps["delete"] = "yes" if gone.returncode == 0 else "no"
            if gone.returncode != 0:
                res.notes.append(f"CLEAN UP BY HAND: {fence_ref} and {branch_ref} on {remote}: "
                                 + gone.stderr.strip().replace("\n", " | ")[-200:])
    return res


def dry_run(clone: str, remotes: list[str], root: str, stamp: str) -> None:
    for remote in remotes:
        branch = f"nitipan-probe-{stamp}"
        fence_ref = f"{FENCE_NS}/{root}/{branch}"
        print(f"{remote}:")
        print(f"  git push --dry-run {remote} HEAD:refs/heads/{branch}   (reads the advertisement for atomic)")
        print(f"  git push --atomic --force-with-lease={fence_ref}: {remote} HEAD:refs/heads/{branch} <fence1>:{fence_ref}")
        print(f"  git push --atomic --force-with-lease={fence_ref}:<fence1> {remote} HEAD:refs/heads/{branch} <fence2>:{fence_ref}")
        print(f"  git push {remote} :{fence_ref} :refs/heads/{branch}")


def table(results: list[Result]) -> str:
    rows = [("remote", "url", *STEPS)]
    for r in results:
        rows.append((r.remote, r.url, *(r.steps.get(s, "-") for s in STEPS)))
    widths = [max(len(row[i]) for row in rows) for i in range(len(rows[0]))]
    lines = ["  ".join(cell.ljust(widths[i]) for i, cell in enumerate(row)).rstrip() for row in rows]
    for r in results:
        for note in r.notes:
            lines.append(f"{r.remote}: {note}")
    return "\n".join(lines)


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("clone", help="a clone whose remotes are probed")
    ap.add_argument("--remote", action="append", help="probe only this remote. May repeat")
    ap.add_argument("--dry-run", action="store_true", help="print the commands and run none of them")
    args = ap.parse_args()

    clone = os.path.abspath(args.clone)
    remotes = args.remote or git(clone, "remote").stdout.split()
    if not remotes:
        print("the clone has no remotes", file=sys.stderr)
        return 2

    root = git(clone, "rev-list", "--max-parents=0", "--first-parent", "HEAD").stdout.split()[0]
    stamp = time.strftime("%Y%m%d%H%M%S", time.gmtime())

    if args.dry_run:
        dry_run(clone, remotes, root, stamp)
        return 0

    results = [probe_remote(clone, r, root, stamp) for r in remotes]
    print(table(results))
    return 0 if all(r.ok() for r in results) else 1


if __name__ == "__main__":
    sys.exit(main())
