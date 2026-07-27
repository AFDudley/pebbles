#!/usr/bin/env bash
# Build pb from the working tree and install it over ~/.local/bin/pb, STAMPED.
#
# Why this exists: the version-stamp machinery already lives in cmd/pb/main.go
# (`buildVersion` / `buildCommit` / `buildDate`, injected with -ldflags -X) and
# scripts/release.sh already uses it for published artifacts — but a local
# `go build ./cmd/pb` leaves the defaults, so the installed binary reports a bare
# "dev" with no sha. That makes "is the installed pb the fixed build?"
# unanswerable by inspection, which is exactly the question that matters after a
# bug fix lands. This script closes that gap by stamping the local build the same
# way a release is stamped.
#
# The toolchain location comes from the single declaration in go-env.sh.
set -euo pipefail

. "$(dirname "$0")/go-env.sh"

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
INSTALL_DIR="${PB_INSTALL_DIR:-$HOME/.local/bin}"

cd "$REPO_ROOT"

COMMIT="$(git rev-parse --short HEAD)"
DATE="$(date -u +%Y-%m-%d)"
DIRTY=""
if ! git diff --quiet HEAD --; then
  DIRTY="-dirty"
fi
VERSION="local-${COMMIT}${DIRTY}"

mkdir -p "$INSTALL_DIR"
go build \
  -ldflags "-X main.buildVersion=${VERSION} -X main.buildCommit=${COMMIT} -X main.buildDate=${DATE}" \
  -o "$INSTALL_DIR/pb" ./cmd/pb

echo "installed $INSTALL_DIR/pb"
"$INSTALL_DIR/pb" --version
sha256sum "$INSTALL_DIR/pb"
