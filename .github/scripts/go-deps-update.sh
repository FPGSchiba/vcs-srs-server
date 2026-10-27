#!/usr/bin/env bash
# Bumps the module's DIRECT requirements to @latest, then tidies.
# Does not build, test, commit or touch git — the calling workflow does that.
#
# Usage (run from the module root):
#   bash .github/scripts/go-deps-update.sh --list   # print the module paths that would be bumped
#   bash .github/scripts/go-deps-update.sh          # go get <each>@latest, then go mod tidy
#
# Env:
#   GO_DEPS_EXCLUDE  whitespace-separated module paths to skip (exact match only).
#
# Portable: holds no repo-specific values; those live in the workflow env.
set -euo pipefail

list_direct() {
  local excludes=" ${GO_DEPS_EXCLUDE:-} "
  excludes="${excludes//$'\n'/ }"
  excludes="${excludes//$'\t'/ }"
  local path
  while IFS= read -r path; do
    if [ -z "$path" ]; then
      continue
    fi
    if [[ "$excludes" == *" $path "* ]]; then
      continue
    fi
    printf '%s\n' "$path"
  done < <(go mod edit -json | jq -r '(.Require // [])[] | select(.Indirect != true) | .Path' | tr -d '\r')
}

case "${1:-}" in
  --list)
    list_direct
    ;;
  "")
    mapfile -t mods < <(list_direct)
    if [ "${#mods[@]}" -gt 0 ]; then
      args=()
      for m in "${mods[@]}"; do
        args+=("$m@latest")
      done
      echo "go get ${args[*]}"
      go get "${args[@]}"
    else
      echo "No direct requirements to update."
    fi
    go mod tidy
    ;;
  *)
    echo "usage: $0 [--list]" >&2
    exit 2
    ;;
esac
