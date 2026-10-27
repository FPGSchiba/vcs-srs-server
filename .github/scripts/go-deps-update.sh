#!/usr/bin/env bash
# Bumps the module's DIRECT requirements to @upgrade, then tidies. Portable:
# shared verbatim between vcs-srs-server and vcs-srs-client; repo-specific
# values come from the environment only.
#
#   GO_DEPS_EXCLUDE  whitespace-separated module paths to leave alone
#                    (exact match only, no prefix matching)
#
# Usage:
#   bash .github/scripts/go-deps-update.sh --list   # print the update set
#   bash .github/scripts/go-deps-update.sh          # go get …@upgrade + go mod tidy
#
# @upgrade, not @latest: @latest is the newest tagged *release* and would
# DOWNGRADE a requirement pinned to a newer pre-release or pseudo-version
# (e.g. the client's github.com/robotn/gohook v1.0.0-beta1, whose @latest is
# v0.50.0, without the purego backends). @upgrade never moves a module below
# its current version.
#
# It does not build, test, commit or touch git.
set -euo pipefail

list() {
  local excl=" ${GO_DEPS_EXCLUDE:-} " p
  excl="${excl//[$'\t\n']/ }"
  while IFS= read -r p; do
    p="${p%$'\r'}" # jq on Windows (Git Bash) emits CRLF
    if [ -z "$p" ]; then
      continue
    fi
    if [[ "$excl" == *" $p "* ]]; then
      continue
    fi
    printf '%s\n' "$p"
  done < <(go mod edit -json | jq -r '(.Require // [])[] | select(.Indirect != true) | .Path')
}

if [ "${1:-}" = "--list" ]; then
  list
  exit 0
fi

# Bash 3.2 compatible (macOS runners run this script's tests): no mapfile.
mods=()
while IFS= read -r m; do
  if [ -n "$m" ]; then mods+=("$m"); fi
done < <(list)
if [ "${#mods[@]}" -gt 0 ]; then
  args=()
  for m in "${mods[@]}"; do
    args+=("$m@upgrade")
  done
  go get "${args[@]}"
fi
go mod tidy
