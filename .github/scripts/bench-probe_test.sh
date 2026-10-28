#!/usr/bin/env bash
# Offline fixture test for bench-probe.sh. Run: bash .github/scripts/bench-probe_test.sh
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
script="$here/bench-probe.sh"
fx="$here/testdata/bench-probe"
failures=0

pass() { echo "PASS $1"; }
fail() { echo "FAIL $1"; failures=$((failures + 1)); }

probe() { # <fixture>
  PROBE_ROOT="$fx/$1" PROBE_STEAL_SECONDS=0 ImageOS=ubuntu24 ImageVersion=20261020.1.0 bash "$script"
}

check() { # <fixture> <irq> <server> <generator> <smt> <label>
  local name="$1" out rc
  set +e; out="$(probe "$name" 2>&1)"; rc=$?; set -e
  if [ "$rc" -ne 0 ]; then fail "$name (rc=$rc out=$out)"; return; fi
  if echo "$out" | jq -e --arg irq "$2" --arg s "$3" --arg g "$4" --argjson smt "$5" --arg l "$6" '
      .schema=="env-probe/1" and .server_set==$s and .generator_set==$g and .smt==$smt
      and .topology_label==$l and .irq_time_accounting==$irq
      and .sysctl.ip_local_port_range=="32768 60999" and .steal_ticks==0
      and .image_os=="ubuntu24" and .vcpus==4' >/dev/null \
    && [[ "$out" != *"$(hostname)"* ]]; then
    pass "$name"
  else
    fail "$name (out=$out)"
  fi
}

check smt-commas y "0,2" "1,3" true "2 hyperthreads of 1 core"
check smt-range n "0,1" "2,3" true "2 hyperthreads of 1 core"
check no-smt unknown "0,1" "2,3" false "2 cores"

# Missing root: clean failure, no partial JSON.
set +e; out="$(PROBE_ROOT="$fx/does-not-exist" PROBE_STEAL_SECONDS=0 bash "$script" 2>&1)"; rc=$?; set -e
if [ "$rc" -ne 0 ] && [[ "$out" == *"bench-probe: "*" not found"* ]] && [[ "$out" != *"{"* ]]; then
  pass "missing-root"
else
  fail "missing-root (rc=$rc out=$out)"
fi

if [ "$failures" -ne 0 ]; then
  echo "$failures failure(s)"
  exit 1
fi
echo "bench-probe tests passed"
