"""Shared plumbing for the pebbles oracle probes under scripts/oracle_*.py.

Not itself an acceptance oracle: it only builds the real `pb` binary and
drives it as a subprocess so each probe can fire genuine concurrent OS
processes against a scratch .pebbles project. Every oracle script imports
this, builds the binary once, and does its own driving + observing; judgment
of the observations stays entirely in the acceptance spec's `then` clauses.
"""

from __future__ import annotations

import json
import os
import shutil
import subprocess

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def _go_binary() -> str:
    found = shutil.which("go")
    if found:
        return found
    fallback = "/usr/local/go/bin/go"
    if os.path.exists(fallback):
        return fallback
    raise RuntimeError("go toolchain not found on PATH or at /usr/local/go/bin/go")


def build_pb_binary(dest_path: str) -> str:
    """Builds the real cmd/pb binary from this checkout to dest_path."""
    result = subprocess.run(
        [_go_binary(), "build", "-o", dest_path, "./cmd/pb"],
        cwd=REPO_ROOT,
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        raise RuntimeError(f"go build ./cmd/pb failed: {result.stderr}")
    return dest_path


def run_pb(binary_path: str, project_dir: str, *args: str) -> subprocess.CompletedProcess:
    return subprocess.run(
        [binary_path, *args],
        cwd=project_dir,
        capture_output=True,
        text=True,
    )


def init_project(binary_path: str, project_dir: str) -> None:
    os.makedirs(project_dir, exist_ok=True)
    result = run_pb(binary_path, project_dir, "init")
    if result.returncode != 0:
        raise RuntimeError(f"pb init failed: {result.stderr}")


def create_issue(binary_path: str, project_dir: str, title: str) -> str:
    result = run_pb(binary_path, project_dir, "create", "--title", title)
    if result.returncode != 0:
        raise RuntimeError(f"pb create failed: {result.stderr}")
    issue_id = result.stdout.strip()
    if not issue_id:
        raise RuntimeError(f"pb create printed no issue id: {result!r}")
    return issue_id


def events_path(project_dir: str) -> str:
    return os.path.join(project_dir, ".pebbles", "events.jsonl")


def read_events(project_dir: str) -> list[dict]:
    path = events_path(project_dir)
    if not os.path.exists(path):
        return []
    with open(path) as f:
        return [json.loads(line) for line in f if line.strip()]


def remove_cache_files(project_dir: str) -> None:
    db_path = os.path.join(project_dir, ".pebbles", "pebbles.db")
    for suffix in ("", "-wal", "-shm", "-journal"):
        path = db_path + suffix
        if os.path.exists(path):
            os.remove(path)


def launch_concurrent(binary_path: str, project_dir: str, argv_list: list[list[str]]) -> list[subprocess.CompletedProcess]:
    """Starts every command in argv_list before waiting on any of them, so
    they run as genuinely concurrent OS processes rather than one at a time.
    """
    procs = [
        subprocess.Popen(
            [binary_path, *argv],
            cwd=project_dir,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
        )
        for argv in argv_list
    ]
    results = []
    for argv, proc in zip(argv_list, procs):
        stdout, stderr = proc.communicate()
        results.append(
            subprocess.CompletedProcess(
                args=[binary_path, *argv],
                returncode=proc.returncode,
                stdout=stdout,
                stderr=stderr,
            )
        )
    return results
