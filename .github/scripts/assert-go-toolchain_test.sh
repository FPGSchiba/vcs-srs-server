#!/usr/bin/env bash
# Offline test for assert-go-toolchain.sh. Run: bash .github/scripts/assert-go-toolchain_test.sh
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
script="$here/assert-go-toolchain.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

have="$(go env GOVERSION)"
failures=0

pass() { echo "PASS $1"; }
fail() { echo "FAIL $1"; failures=$((failures + 1)); }

write_mod() { # <file> [toolchain]
  printf 'module example.com/fixture\n\ngo 1.21.0\n' > "$1"
  if [ -n "${2:-}" ]; then
    printf 'toolchain %s\n' "$2" >> "$1"
  fi
}

# Case 1: no toolchain directive.
write_mod "$tmp/none.mod"
set +e
out="$(bash "$script" "$tmp/none.mod" 2>&1)"; rc=$?
set -e
if [ "$rc" -ne 0 ] && [[ "$out" == *"No toolchain directive"* ]]; then
  pass "no-toolchain"
else
  fail "no-toolchain (rc=$rc out=$out)"
fi

# Case 2: mismatching toolchain.
write_mod "$tmp/mismatch.mod" go1.0.1
set +e
out="$(bash "$script" "$tmp/mismatch.mod" 2>&1)"; rc=$?
set -e
if [ "$rc" -ne 0 ] && [[ "$out" == *"want go1.0.1, got $have"* ]]; then
  pass "mismatch"
else
  fail "mismatch (rc=$rc out=$out)"
fi

# Case 3: matching toolchain.
write_mod "$tmp/match.mod" "$have"
set +e
out="$(bash "$script" "$tmp/match.mod" 2>&1)"; rc=$?
set -e
if [ "$rc" -eq 0 ]; then
  pass "match"
else
  fail "match (rc=$rc out=$out)"
fi

if [ "$failures" -ne 0 ]; then
  exit 1
fi
