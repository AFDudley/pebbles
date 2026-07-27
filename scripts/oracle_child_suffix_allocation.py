#!/usr/bin/env python3
"""Acceptance oracle entrypoint for pebble so-a9f clause c2 (behavior oracle).

Proves: when N `pb dep add --type parent-child` commands land on the same
parent at the same instant, every one of them succeeds and the suffixes
handed out are contiguous with no gaps and no duplicates — i.e. allocating
the child ID and appending the event that consumes it happen as one
indivisible step, so overlapping writers are serialized rather than
colliding and needing to be patched up afterwards.

The RUNNER (spec_oracle) owns judgment: this entrypoint only builds the real
`pb` binary, drives it, and EMITS observations (``commands_succeeded``,
``commands_failed``, ``distinct_suffixes``, ``suffix_gaps``) as one JSON
object on stdout; the oracle's ``then`` predicates decide pass/fail. It
judges nothing itself.
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

        results = launch_concurrent(
            binary_path,
            project_dir,
            [
                ["dep", "add", "--type", "parent-child", child_id, parent_id]
                for child_id in child_ids
            ],
        )
        commands_succeeded = sum(1 for r in results if r.returncode == 0)
        commands_failed = len(results) - commands_succeeded

        prefix = parent_id + "."
        suffixes = set()
        for event in read_events(project_dir):
            if event.get("type") != "rename":
                continue
            new_id = event["payload"]["new_id"]
            if new_id.startswith(prefix):
                suffix_text = new_id[len(prefix):]
                if suffix_text.isdigit():
                    suffixes.add(int(suffix_text))

        full_range = set(range(1, max(suffixes) + 1)) if suffixes else set()
        suffix_gaps = len(full_range - suffixes)

        observation = {
            "commands_succeeded": commands_succeeded,
            "commands_failed": commands_failed,
            "distinct_suffixes": len(suffixes),
            "suffix_gaps": suffix_gaps,
        }
    sys.stdout.write(json.dumps(observation))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
