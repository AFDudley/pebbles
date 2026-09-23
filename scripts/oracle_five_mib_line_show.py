#!/usr/bin/env python3
"""Acceptance oracle entrypoint for pebble so-a5d clause c3 (behavior oracle).

Proves: pb accepts a line up to the 5 MiB buffer internal/pebbles/beads_import.go
already sets for the Beads importer (scanner.Buffer(make([]byte, 0, 64*1024),
5*1024*1024)), not merely a line a little over the old 64 KiB default.

The RUNNER (spec_oracle) owns judgment: this entrypoint only builds the real
`pb` binary, drives it, and EMITS observations (`show_exit_code`,
`token_too_long_in_output`) as one JSON object on stdout; the oracle's
`then` predicates decide pass/fail. It judges nothing itself.
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
    args = parser.parse_args()

    with tempfile.TemporaryDirectory() as tmp:
        binary_path = build_pb_binary(f"{tmp}/pb")
        project_dir = f"{tmp}/project"
        init_project(binary_path, project_dir)
        issue_id = create_issue(binary_path, project_dir, "Near Five MiB Comment Target")

        near_limit_line = build_padded_comment_line(issue_id, args.line_bytes)
        append_raw_lines(project_dir, [near_limit_line])

        show_result = run_pb(binary_path, project_dir, "show", issue_id)
        combined_output = show_result.stdout + show_result.stderr

        observation = {
            "show_exit_code": show_result.returncode,
            "token_too_long_in_output": "token too long" in combined_output,
        }
    sys.stdout.write(json.dumps(observation))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
