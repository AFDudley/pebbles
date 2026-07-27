#!/usr/bin/env bash
# The go-vet pre-commit/pre-push hook's entry point.
#
# The stock dnephin/pre-commit-golang go-vet hook runs `pkg=$(go list)` from the
# repo root, which fails on this repo's root-package-less cmd/+internal/ layout;
# the hook was already overridden to `go vet ./...` for that reason. But a bare
# `go` resolves against whatever PATH pre-commit inherited, and on this host the
# toolchain is not on a non-interactive shell's PATH — so `git push` failed with
# `go: command not found` (exit 127) even though the same hook passed from an
# interactive shell. Routing the hook through this script makes it depend on the
# single toolchain-location declaration instead of on the caller's environment.
set -euo pipefail

. "$(dirname "$0")/go-env.sh"

go vet ./...
