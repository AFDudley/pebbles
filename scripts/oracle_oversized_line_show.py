#!/usr/bin/env python3
"""Acceptance oracle entrypoint for pebble so-a5d clause c1 (behavior oracle).

Proves: pb show reads a well-formed events.jsonl line over the old 64 KiB
bufio.Scanner default (internal/pebbles/log.go before the fix) instead of
failing the whole log with "bufio.Scanner: token too long".

The RUNNER (spec_oracle) owns judgment: this entrypoint only builds the real
`pb` binary, drives it, and EMITS observations (`show_exit_code`,
`token_too_long_in_output`, `pebble_title_printed`) as one JSON object on
stdout; the oracle's `then` predicates decide pass/fail. It judges nothing
itself.
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
        issue_id = create_issue(binary_path, project_dir, "Oversized Comment Target")

        oversized_line = build_padded_comment_line(issue_id, args.line_bytes)
        append_raw_lines(project_dir, [oversized_line])

        show_result = run_pb(binary_path, project_dir, "show", issue_id)
        combined_output = show_result.stdout + show_result.stderr

        observation = {
            "show_exit_code": show_result.returncode,
            "token_too_long_in_output": "token too long" in combined_output,
            "pebble_title_printed": "Oversized Comment Target" in show_result.stdout,
        }
    sys.stdout.write(json.dumps(observation))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
