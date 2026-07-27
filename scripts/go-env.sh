# shellcheck shell=bash
# The ONE declaration of where the go toolchain lives on this host.
#
# go is installed at /usr/local/go (the canonical location go.dev's tarball
# install uses) and is NOT on a non-interactive shell's default PATH here — so
# anything git, pre-commit, or exophial invokes gets a bare `go: command not
# found` even though an interactive shell finds it. Every script that needs the
# toolchain sources THIS file rather than repeating the path, so there is one
# place to change if the toolchain moves.
#
# Sourced by: scripts/gate.sh, scripts/go-vet.sh, scripts/install-local.sh.
export PATH="/usr/local/go/bin:${PATH}"
