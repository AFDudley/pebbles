#!/usr/bin/env python3
"""Acceptance oracle entrypoint for pebble so-a9f clause c3 (behavior oracle).

Proves: a `pb dep add --type parent-child` call the tool refuses (here,
linking under a parent ID that does not exist) writes NOTHING to the events
log — the collision/validity check must precede any append, so a rejected
call never leaves a rename or dep_add behind that needs manual event-log
surgery.

The RUNNER (spec_oracle) owns judgment: this entrypoint only builds the real
`pb` binary, drives it, and EMITS observations (``cli_exit_code`` and
``events_appended``) as one JSON object on stdout; the oracle's ``then``
predicates decide pass/fail. It judges nothing itself.
"""

from __future__ import annotations

import json
import sys
import tempfile

from _pb_oracle_lib import build_pb_binary, create_issue, init_project, read_events, run_pb


def main() -> int:
    with tempfile.TemporaryDirectory() as tmp:
        binary_path = build_pb_binary(f"{tmp}/pb")
        project_dir = f"{tmp}/project"
        init_project(binary_path, project_dir)
        child_id = create_issue(binary_path, project_dir, "Child")

        events_before = len(read_events(project_dir))
        result = run_pb(
            binary_path,
            project_dir,
            "dep",
            "add",
            "--type",
            "parent-child",
            child_id,
            "does-not-exist-999",
        )
        events_after = len(read_events(project_dir))

        observation = {
            "cli_exit_code": result.returncode,
            "events_appended": events_after - events_before,
        }
    sys.stdout.write(json.dumps(observation))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
