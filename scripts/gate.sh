#!/usr/bin/env bash
# Commit-boundary acceptance gate for this Go repo.
#
# Exophial's integrator re-runs THIS on a completed worker's branch, in the
# worker's worktree, for any pebble with no linked spec (a linked spec is graded
# in-process by spec_oracle.run_spec instead). It is also the worker's own
# inner-loop self-check.
#
# `gate.command` in .exophial/config.yaml is an argv list, not a shell line, so
# the `build && vet && test` conjunction lives here rather than in the config.
# The go toolchain is installed at /usr/local/go on this host and is not on a
# non-interactive shell's default PATH; prepend it rather than assume it.
set -euo pipefail

export PATH="/usr/local/go/bin:${PATH}"

go build ./...
go vet ./...
go test ./...
