#!/usr/bin/env bash
# Fails when the running Go toolchain differs from the `toolchain` directive
# in go.mod. Guards CI against setup-go silently picking the `go` line
# (e.g. 1.27.0) instead of the pinned toolchain, and against a later
# `go get` raising the toolchain line without anyone noticing.
#
# Usage: bash .github/scripts/assert-go-toolchain.sh [path/to/go.mod]
# Portable: holds no repo-specific values.
set -euo pipefail

modfile="${1:-./go.mod}"

want="$(go mod edit -json "$modfile" | jq -r '.Toolchain // empty')"
if [ -z "$want" ]; then
  echo "::error title=No toolchain directive::$modfile has no 'toolchain' directive; add one (go mod edit -toolchain=goX.Y.Z) so CI pins an exact Go patch."
  exit 1
fi

have="$(go env GOVERSION)"
if [ "$want" != "$have" ]; then
  echo "::error title=Go toolchain mismatch::want $want, got $have"
  exit 1
fi

echo "Go toolchain $have matches $modfile."
