#!/usr/bin/env python3
"""Acceptance oracle entrypoint for pebble so-a9f clause c1 (behavior oracle).

Proves: firing N `pb dep add --type parent-child` commands at the same
parent, at the same instant, as real concurrent OS processes, can never
assign the same child ID to two of them — the exact defect reproduced in
production on 2026-07-24, where two overlapping calls both renamed onto
exo-72d.9.

The RUNNER (spec_oracle) owns judgment: this entrypoint only builds the real
`pb` binary, drives it, and EMITS observations (``duplicate_child_ids`` and
``distinct_child_ids``) as one JSON object on stdout; the oracle's ``then``
predicates decide pass/fail. It judges nothing itself.
"""

from __future__ import annotations

import argparse
import json
import sys
import tempfile

from _pb_oracle_lib import build_pb_binary, create_issue, init_project, launch_concurrent, read_events


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

        rename_targets = [
            event["payload"]["new_id"]
            for event in read_events(project_dir)
            if event.get("type") == "rename"
        ]
        distinct = set(rename_targets)
        duplicate_count = len(rename_targets) - len(distinct)

        observation = {
            "duplicate_child_ids": duplicate_count,
            "distinct_child_ids": len(distinct),
        }
    sys.stdout.write(json.dumps(observation))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
