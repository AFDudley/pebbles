#!/usr/bin/env python3
"""Acceptance oracle entrypoint for pebble so-a5d clause c5 (behavior oracle).

Proves: the fix reaches cmd/pb/log.go:810's parallel scanner too, so pb log
reads a bus with one events.jsonl line over the old 64 KiB bufio.Scanner
default, not just the readEventLog path the operator first hit.

The RUNNER (spec_oracle) owns judgment: this entrypoint only builds the real
`pb` binary, drives it, and EMITS observations (`log_exit_code`,
`token_too_long_in_output`, `printed_event_count`) as one JSON object on
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
        issue_id = create_issue(binary_path, project_dir, "Oversized Comment For Log Verb")

        oversized_line = build_padded_comment_line(issue_id, args.line_bytes)
        append_raw_lines(project_dir, [oversized_line])

        log_result = run_pb(binary_path, project_dir, "log", "--json", "--no-git")
        combined_output = log_result.stdout + log_result.stderr

        printed_event_count = 0
        if log_result.returncode == 0:
            for raw_line in log_result.stdout.splitlines():
                raw_line = raw_line.strip()
                if not raw_line:
                    continue
                json.loads(raw_line)
                printed_event_count += 1

        observation = {
            "log_exit_code": log_result.returncode,
            "token_too_long_in_output": "token too long" in combined_output,
            "printed_event_count": printed_event_count,
        }
    sys.stdout.write(json.dumps(observation))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
