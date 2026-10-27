#!/usr/bin/env bash
# Offline test for go-deps-update.sh (--list mode only; never touches the network).
# Run: bash .github/scripts/go-deps-update_test.sh
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
script="$here/go-deps-update.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

failures=0
pass() { echo "PASS $1"; }
fail() { echo "FAIL $1"; failures=$((failures + 1)); }

mkdir -p "$tmp/full" "$tmp/empty"
cat > "$tmp/full/go.mod" <<'MOD'
module example.com/fixture

go 1.27.0

require (
	example.com/direct/a v1.0.0
	example.com/direct/b v1.0.0
	example.com/excluded v1.0.0
	example.com/excluded/v2 v2.0.0
)

require example.com/indirect v1.0.0 // indirect
MOD
printf 'module example.com/empty\n\ngo 1.27.0\n' > "$tmp/empty/go.mod"

run_list() { # <dir> <exclude>
  (cd "$1" && GO_DEPS_EXCLUDE="$2" bash "$script" --list)
}

# Case 1: exact-match exclusion; /v2 must survive, indirect must not appear.
set +e
out="$(run_list "$tmp/full" "example.com/excluded")"; rc=$?
set -e
want=$'example.com/direct/a\nexample.com/direct/b\nexample.com/excluded/v2'
if [ "$rc" -eq 0 ] && [ "$out" == "$want" ]; then
  pass "exclude-exact"
else
  fail "exclude-exact (rc=$rc out=$out)"
fi

# Case 2: no exclusions -> 4 direct requires, no indirect.
set +e
out="$(run_list "$tmp/full" "")"; rc=$?
set -e
lines="$(printf '%s\n' "$out" | grep -c . || true)"
if [ "$rc" -eq 0 ] && [ "$lines" -eq 4 ] && [[ "$out" != *"example.com/indirect"* ]]; then
  pass "no-exclude"
else
  fail "no-exclude (rc=$rc lines=$lines out=$out)"
fi

# Case 3: no require block -> empty output, exit 0.
set +e
out="$(run_list "$tmp/empty" "")"; rc=$?
set -e
if [ "$rc" -eq 0 ] && [ -z "$out" ]; then
  pass "no-requires"
else
  fail "no-requires (rc=$rc out=$out)"
fi

if [ "$failures" -ne 0 ]; then
  exit 1
fi
