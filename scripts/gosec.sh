#!/usr/bin/env bash
# scripts/gosec.sh
# Runs the gosec security scanner on the backend module. CI (pull-request.yml,
# job security-scan) and `make gosec` both use this script, so they report the
# same findings.
#
# Why a build from source instead of the securego/gosec Docker action:
# - The action runs its own Go (1.26 in v2.29.0) with GOTOOLCHAIN=local, so it
#   refuses `go 1.27` in backend/go.mod.
# - gosec v2.29.0 vendors golang.org/x/tools v0.49.0, which cannot read the
#   export data of Go 1.27 ("export data version 5 is greater than maximum
#   supported version 4"). This script builds the pinned gosec release against
#   a newer x/tools (GOSEC_XTOOLS_VERSION) with the Go on PATH.
# - gosec must run inside backend/: the module root. Run from the repository
#   root, `gosec ./backend/...` finds no packages and reports 0 files.
#
# Remove the x/tools override when a gosec release supports Go 1.27.
#
# Usage: scripts/gosec.sh [extra gosec args]   (exit status is gosec's)
set -euo pipefail

GOSEC_VERSION="${GOSEC_VERSION:-v2.29.0}"
GOSEC_XTOOLS_VERSION="${GOSEC_XTOOLS_VERSION:-v0.52.0}"

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
build_dir="$(mktemp -d)"
trap 'rm -rf "$build_dir"' EXIT

echo "Building gosec ${GOSEC_VERSION} (golang.org/x/tools ${GOSEC_XTOOLS_VERSION}) with $(go version)..."
(
  cd "$build_dir"
  go mod init gosecbuild >/dev/null 2>&1
  go get "github.com/securego/gosec/v2@${GOSEC_VERSION}" "golang.org/x/tools@${GOSEC_XTOOLS_VERSION}" >/dev/null
  go build -mod=mod -o "$build_dir/gosec" github.com/securego/gosec/v2/cmd/gosec
)

cd "$repo_root/backend"
"$build_dir/gosec" "$@" ./...
