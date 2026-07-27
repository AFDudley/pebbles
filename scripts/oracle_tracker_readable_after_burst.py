#!/usr/bin/env python3
"""Acceptance oracle entrypoint for pebble so-a9f clause c4 (behavior oracle).

Proves: after a burst of N concurrent `pb dep add --type parent-child` calls
land on the same parent, the tracker is still fully readable — deleting the
SQLite cache and letting a read verb rebuild it from scratch must not abort
with "issue already exists" (the exact production outage: a single duplicate
child ID denied every subsequent `pb show`/`list`/`ready`).

The RUNNER (spec_oracle) owns judgment: this entrypoint only builds the real
`pb` binary, drives it, and EMITS observations (``rebuild_exit_code``,
``duplicate_id_error_seen``, ``read_commands_succeeded``) as one JSON object
on stdout; the oracle's ``then`` predicates decide pass/fail. It judges
nothing itself.
"""

from __future__ import annotations

import argparse
import json
import sys
import tempfile

from _pb_oracle_lib import (
    build_pb_binary,
    create_issue,
    init_project,
    launch_concurrent,
    remove_cache_files,
    run_pb,
)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--concurrency", type=int, default=8)
    args = parser.parse_args()

    with tempfile.TemporaryDirectory() as tmp:
        binary_path = build_pb_binary(f"{tmp}/pb")
        project_dir = f"{tmp}/project"
        init_project(binary_path, project_dir)
        parent_id = create_issue(binary_path, project_dir, "Parent")
        child_ids = [
            create_issue(binary_path, project_dir, f"Child {i}")
            for i in range(args.concurrency)
        ]

        launch_concurrent(
            binary_path,
            project_dir,
            [
                ["dep", "add", "--type", "parent-child", child_id, parent_id]
                for child_id in child_ids
            ],
        )

        # Force every read below to rebuild the cache from the raw log.
        remove_cache_files(project_dir)

        list_result = run_pb(binary_path, project_dir, "list", "--all")
        show_result = run_pb(binary_path, project_dir, "show", parent_id)
        ready_result = run_pb(binary_path, project_dir, "ready")
        read_results = [list_result, show_result, ready_result]

        duplicate_id_error_seen = any(
            "already exists" in r.stderr for r in read_results
        )
        read_commands_succeeded = sum(1 for r in read_results if r.returncode == 0)

        observation = {
            "rebuild_exit_code": list_result.returncode,
            "duplicate_id_error_seen": duplicate_id_error_seen,
            "read_commands_succeeded": read_commands_succeeded,
        }
    sys.stdout.write(json.dumps(observation))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
