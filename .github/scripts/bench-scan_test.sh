#!/usr/bin/env bash
# Offline fixture test for bench-scan.sh. Run: bash .github/scripts/bench-scan_test.sh
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
script="$here/bench-scan.sh"
fx="$here/testdata/bench-scan"
failures=0
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

pass() { echo "PASS $1"; }
fail() { echo "FAIL $1"; failures=$((failures + 1)); }

# stage <file> -> a one-file directory, printed
stage() { local d; d="$(mktemp -d "$tmp/s.XXXXXX")"; cp "$1" "$d/"; echo "$d"; }

for f in "$fx"/fail/*; do
  d="$(stage "$f")"
  if bash "$script" "$d" >/dev/null 2>&1; then fail "fail-fixture accepted: $(basename "$f")"; else pass "rejects $(basename "$f")"; fi
done
for f in "$fx"/pass/*; do
  d="$(stage "$f")"
  if bash "$script" "$d" >/dev/null 2>&1; then pass "accepts $(basename "$f")"; else fail "pass-fixture rejected: $(basename "$f")"; fi
done

# extra literals
d="$tmp/extra"; mkdir "$d"; echo "built on buildbox-17" > "$d/a.txt"
if bash "$script" "$d" buildbox-17 >/dev/null 2>&1; then fail "extra literal accepted"; else pass "rejects extra literal"; fi
if bash "$script" "$d" "" >/dev/null 2>&1; then pass "ignores empty extra"; else fail "empty extra rejected"; fi

# the real schema publish set
runok="$here/../../tools/benchsite/schema/testdata/run-ok"
if bash "$script" "$runok" >/dev/null 2>&1; then pass "run-ok clean"; else fail "run-ok flagged"; fi

# manifest top-level keys
d="$tmp/mf"; mkdir "$d"; cp "$runok/manifest.json" "$d/"
if bash "$script" "$d" >/dev/null 2>&1; then pass "manifest keys ok"; else fail "manifest keys rejected"; fi
jq '. + {extra_key: 1}' "$runok/manifest.json" > "$d/manifest.json"
if bash "$script" "$d" >/dev/null 2>&1; then fail "extra manifest key accepted"; else pass "rejects extra manifest key"; fi
jq 'del(.pins)' "$runok/manifest.json" > "$d/manifest.json"
if bash "$script" "$d" >/dev/null 2>&1; then fail "missing manifest key accepted"; else pass "rejects missing manifest key"; fi

# no output may echo a secret extra
d="$tmp/sec"; mkdir "$d"; echo "tok s3cr3t-value" > "$d/a.txt"
out="$(bash "$script" "$d" s3cr3t-value 2>&1 || true)"
if [[ "$out" == *s3cr3t-value* ]]; then fail "scan output leaks extra"; else pass "output does not echo extra"; fi

if [ "$failures" -ne 0 ]; then echo "$failures failure(s)"; exit 1; fi
echo "all passed"
