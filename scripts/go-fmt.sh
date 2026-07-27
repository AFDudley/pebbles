#!/usr/bin/env bash
# The go-fmt pre-commit/pre-push hook's entry point.
#
# Same defect and same fix as scripts/go-vet.sh: the stock
# dnephin/pre-commit-golang go-fmt hook resolves `gofmt` against whatever PATH
# pre-commit inherited, and on this host the toolchain is not on a
# non-interactive shell's PATH — so `git push` failed with "gofmt not installed
# or available in the PATH". Sourcing the single toolchain-location declaration
# makes the hook independent of the caller's environment.
#
# Reformats in place and FAILS when it had to, so the offending files are staged
# by the author rather than silently rewritten under a passing hook.
set -euo pipefail

. "$(dirname "$0")/go-env.sh"

unformatted="$(gofmt -l "$@")"
if [ -n "$unformatted" ]; then
  echo "gofmt reformatted the following files; review and stage them:" >&2
  echo "$unformatted" >&2
  gofmt -w $unformatted
  exit 1
fi
