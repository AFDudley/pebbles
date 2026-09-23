#!/usr/bin/env python3
"""Acceptance oracle entrypoint for pebble so-a5d clause c2 (behavior oracle).

Proves: pb list and pb ready still finish and name every ordinary pebble on
a bus that also carries one events.jsonl line over the old 64 KiB
bufio.Scanner default, instead of the whole bus reporting empty
(internal/pebbles/events.go's decodeEvents feeds both commands via the
SQLite cache rebuild).

The RUNNER (spec_oracle) owns judgment: this entrypoint only builds the real
`pb` binary, drives it, and EMITS observations (`list_exit_code`,
`ready_exit_code`, `listed_pebble_count`, `ready_pebble_count`) as one JSON
object on stdout; the oracle's `then` predicates decide pass/fail. It judges
nothing itself.
"""

from __future__ import annotations

import argparse
import json
import sys
import tempfile

from _pb_oracle_lib import (
    append_raw_lines,
    build_padded_comment_line,
    build_pb_binary,
    create_issue,
    init_project,
    run_pb,
)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--line-bytes", type=int, required=True)
    parser.add_argument("--pebble-count", type=int, required=True)
    args = parser.parse_args()

    with tempfile.TemporaryDirectory() as tmp:
        binary_path = build_pb_binary(f"{tmp}/pb")
        project_dir = f"{tmp}/project"
        init_project(binary_path, project_dir)
        issue_ids = [
            create_issue(binary_path, project_dir, f"Ordinary Pebble {i}")
            for i in range(args.pebble_count)
        ]

        oversized_line = build_padded_comment_line(issue_ids[0], args.line_bytes)
        append_raw_lines(project_dir, [oversized_line])

        list_result = run_pb(binary_path, project_dir, "list", "--json")
        ready_result = run_pb(binary_path, project_dir, "ready", "--json")

        listed_pebble_count = 0
        if list_result.returncode == 0:
            listed_pebble_count = len(json.loads(list_result.stdout))
        ready_pebble_count = 0
        if ready_result.returncode == 0:
            ready_pebble_count = len(json.loads(ready_result.stdout))

        observation = {
            "list_exit_code": list_result.returncode,
            "ready_exit_code": ready_result.returncode,
            "listed_pebble_count": listed_pebble_count,
            "ready_pebble_count": ready_pebble_count,
        }
    sys.stdout.write(json.dumps(observation))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
