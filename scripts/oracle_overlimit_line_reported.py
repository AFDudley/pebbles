#!/usr/bin/env python3
"""Acceptance oracle entrypoint for pebble so-a5d clause c4 (behavior oracle).

Proves: when one events.jsonl line is over the 5 MiB buffer pb now reads up
to, pb names that line by its 1-based line number instead of aborting with
no location, and the rest of the bus stays readable.

The RUNNER (spec_oracle) owns judgment: this entrypoint only builds the real
`pb` binary, drives it, and EMITS observations (`reported_line_number`,
`listed_pebble_count`) as one JSON object on stdout; the oracle's `then`
predicates decide pass/fail. It judges nothing itself.
"""

from __future__ import annotations

import argparse
import json
import re
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

REPORTED_LINE_RE = re.compile(r"skipping events log line (\d+)")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--line-bytes", type=int, required=True)
    parser.add_argument("--line-number", type=int, required=True)
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

        # One line per created pebble is already in the log; pad with small
        # filler comment lines up to line_number - 1, then place the
        # oversized line exactly at line_number.
        filler_needed = args.line_number - 1 - args.pebble_count
        if filler_needed < 0:
            raise ValueError(
                f"line_number {args.line_number} leaves no room for {args.pebble_count} pebbles"
            )
        filler_lines = [build_padded_comment_line(issue_ids[0], 120) for _ in range(filler_needed)]
        oversized_line = build_padded_comment_line(issue_ids[0], args.line_bytes)
        append_raw_lines(project_dir, [*filler_lines, oversized_line])

        log_result = run_pb(binary_path, project_dir, "log", "--limit", "100", "--no-git")
        list_result = run_pb(binary_path, project_dir, "list", "--json")

        match = REPORTED_LINE_RE.search(log_result.stderr)
        reported_line_number = int(match.group(1)) if match else -1

        listed_pebble_count = 0
        if list_result.returncode == 0:
            listed_pebble_count = len(json.loads(list_result.stdout))

        observation = {
            "reported_line_number": reported_line_number,
            "listed_pebble_count": listed_pebble_count,
        }
    sys.stdout.write(json.dumps(observation))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
