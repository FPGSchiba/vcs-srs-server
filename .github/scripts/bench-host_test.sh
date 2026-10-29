#!/usr/bin/env bash
# Offline dry-run test for bench-host.sh. Run: bash .github/scripts/bench-host_test.sh
# Needs bash and jq on PATH. Calls no docker, taskset or go.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
script="$here/bench-host.sh"
failures=0
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

pass() { echo "PASS $1"; }
fail() { echo "FAIL $1"; failures=$((failures + 1)); }

out=""; rc=0
# host <args...>: runs the script in dry-run with a fixed nproc; sets out (stdout+stderr) and rc.
host() {
  set +e
  out="$(BENCH_HOST_NPROC=24 bash "$script" "$@" 2>&1)"; rc=$?
  set -e
}
# base <mode> [extra...]: a valid command line; later duplicates override earlier ones.
fresh_out() { rm -rf "$tmp/out"; mkdir -p "$tmp/out"; printf '%s' "$tmp/out"; }
base() {
  local mode="$1"; shift
  host "$mode" --host-label local-docker --server-set 0 --gen-set 4-11 --out "$(fresh_out)" --dry-run "$@"
}
expect() { # <name> <rc> <substring or empty>
  if [ "$rc" -eq "$2" ] && { [ -z "$3" ] || [[ "$out" == *"$3"* ]]; }; then pass "$1"; else fail "$1 (rc=$rc want=$2 out=$out)"; fi
}

# help: exits 0, lists every flag and both modes, carries the EC2 commands.
host --help
ok=1
[ "$rc" -eq 0 ] || ok=0
for w in docker native --host-label --server-set --gen-set --arms --scenario --repeats --gogc \
  --client-ref --out --micro --predict --budget --leg-arg --note --privileged-sysctl --dry-run \
  'git archive HEAD | ssh' 'scp -r'; do
  [[ "$out" == *"$w"* ]] || { ok=0; echo "  help lacks: $w"; }
done
if [ "$ok" -eq 1 ]; then pass help; else fail "help (rc=$rc)"; fi

# missing-required: each required flag, one at a time.
for f in host-label server-set gen-set out; do
  args=(--host-label local-docker --server-set 0 --gen-set 4-11 --out "$(fresh_out)")
  keep=(); i=0
  while [ $i -lt ${#args[@]} ]; do
    if [ "${args[$i]}" = "--$f" ]; then i=$((i + 2)); continue; fi
    keep+=("${args[$i]}"); i=$((i + 1))
  done
  host native "${keep[@]}" --dry-run
  expect "missing-required-$f" 2 "bench-host: --$f is required"
done

# mode: missing or unknown.
host --dry-run;          expect mode-missing 2 "bench-host: mode must be docker or native"
host bogus --dry-run;    expect mode-unknown 2 "bench-host: mode must be docker or native"

# host-label-regex
for l in local-docker local-linux aws-c7i.4xlarge aws-c7i-flex.large; do
  base native --host-label "$l"; expect "host-label-ok-$l" 0 ""
done
for l in aws github aws-C7I.4XL "local docker"; do
  base native --host-label "$l"; expect "host-label-bad-$l" 2 "bench-host: --host-label"
done

# arms
base native --arms S;   expect arms-S 0 ""
base native --arms Seq; expect arms-Seq 2 "Seq and D2 arrive with kit S5"
base native --arms D2;  expect arms-D2 2 "Seq and D2 arrive with kit S5"

# scenario
base native --scenario one-big-net; expect scenario-ok 0 ""
base native --scenario other;       expect scenario-bad 2 "bench-host: --scenario"

# repeats
for r in 1 2 3 4 5; do base native --repeats "$r"; expect "repeats-$r" 0 ""; done
for r in 0 6 x; do base native --repeats "$r"; expect "repeats-bad-$r" 2 "bench-host: --repeats"; done

# gogc
for g in off default both; do base native --gogc "$g"; expect "gogc-$g" 0 ""; done
base native --gogc nope; expect gogc-bad 2 "bench-host: --gogc"

# client-ref
base native --client-ref b55823e195577506ba5c3fa2c6d16a48a5f9bd5b; expect client-ref-full 0 ""
base native --client-ref b55823e;                                    expect client-ref-short 2 "bench-host: --client-ref must be a full 40-hex SHA"

# micro
base native --micro run;                                  expect micro-run 0 ""
base native --micro maybe;                                expect micro-bad 2 "bench-host: --micro"
base native --micro skip;                                 expect micro-skip-bare 2 "bench-host: --micro skip needs"
echo '{}' > "$tmp/p.json"
base native --micro skip --predict "$tmp/p.json";         expect micro-skip-predict 0 ""
base native --micro skip --predict "$tmp/missing.json";   expect micro-skip-predict-missing 2 "bench-host: --predict"
base native --micro skip --leg-arg --predicted-knee=900;  expect micro-skip-knee 0 ""
base native --micro run --predict "$tmp/p.json";          expect micro-run-predict-ok 0 ""
base native --micro run --predict "$tmp/missing.json";    expect micro-run-predict-missing 2 "bench-host: --predict"

# budget and misc values
base native --budget 100m; expect budget-ok 0 ""
base native --budget "";   expect budget-empty 2 "bench-host: --budget"
for b in 1h30m 90s 250ms 100m; do base native --budget "$b"; expect "budget-ok-$b" 0 ""; done
for b in abc 10 m 1x; do base native --budget "$b"; expect "budget-bad-$b" 2 "bench-host: --budget"; done

# privileged-sysctl is docker-only
base docker --privileged-sysctl; expect privileged-docker 0 ""
base native --privileged-sysctl; expect privileged-native 2 "bench-host: --privileged-sysctl is for docker mode only"

# out-not-empty
mkdir -p "$tmp/full"; : > "$tmp/full/x"
host native --host-label local-docker --server-set 0 --gen-set 4-11 --out "$tmp/full" --dry-run
expect out-not-empty 2 "bench-host: --out"

# unknown-flag, positional, missing value
base native --nope;      expect unknown-flag 2 "bench-host: unknown flag --nope"
base native stray;       expect positional 2 "bench-host: unexpected argument stray"
host native --host-label; expect missing-value 2 "bench-host: --host-label needs a value"


# cpu sets (H-T5). Fixtures: BENCH_HOST_SYSFS is a root holding sys/devices/system/cpu/...
fx="$here/testdata/bench-host"
cpus() { # <server-set> <gen-set> [sysfs fixture]: native dry-run with those sets
  local sysfs="${3:-sysfs-no-smt}"
  set +e
  out="$(BENCH_HOST_NPROC=24 BENCH_HOST_SYSFS="$fx/$sysfs" bash "$script" native --host-label local-docker \
    --server-set "$1" --gen-set "$2" --out "$(fresh_out)" --dry-run 2>&1)"; rc=$?
  set -e
}
cpus 0 4-11;      expect cpus-ok 0 ""
cpus 0-1 1-3;     expect cpus-overlap 2 "bench-host: server and generator cpu sets overlap on 1"
cpus 0-1 1-3,5;   expect cpus-overlap-list 2 "overlap on 1"
cpus 0 20-30;     expect cpus-range 2 "cpu 24 is not online"
cpus 24 4-11;     expect cpus-range-server 2 "cpu 24 is not online"
for bad in a 3-1 1,,2 "" 1- -1 1-a; do
  cpus 0 "$bad";  expect "cpus-malformed-gen-[$bad]" 2 "bench-host: --gen-set"
done
cpus a 4-11;      expect cpus-malformed-server 2 "bench-host: --server-set"
cpus "" 4-11;     expect cpus-empty-server 2 "bench-host: --server-set"
cpus 0-2 4-11;    expect server-size 2 "server set must be 1 or 2 cpus"
cpus 0,1 4-11;    expect server-size-two-ok 0 ""
cpus 0 1,2,3 sysfs-smt
expect sibling-warning 0 "WARNING: server cpu 0 shares a physical core with generator cpu 1"
cpus 0 2,3 sysfs-smt
if [ "$rc" -eq 0 ] && [[ "$out" != *WARNING* ]]; then pass sibling-none; else fail "sibling-none (rc=$rc out=$out)"; fi
cpus 0 1,2,3 sysfs-no-smt
if [ "$rc" -eq 0 ] && [[ "$out" != *WARNING* ]]; then pass sibling-no-smt; else fail "sibling-no-smt (rc=$rc out=$out)"; fi

echo
echo "$failures failures"
[ "$failures" -eq 0 ]
