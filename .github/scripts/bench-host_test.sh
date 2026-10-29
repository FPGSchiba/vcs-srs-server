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
# docker mode with the hook still checks siblings (the hook-less skip reads no host /sys at all)
set +e
out="$(BENCH_HOST_NPROC=24 BENCH_HOST_SYSFS="$fx/sysfs-smt" bash "$script" docker --host-label local-docker   --server-set 0 --gen-set 1,2,3 --out "$(fresh_out)" --dry-run 2>&1)"; rc=$?
set -e
expect sibling-docker-hooked 0 "WARNING: server cpu 0 shares"

# ---------------------------------------------------------------------------
# native pipeline, dry-run (H-T6). Every command is one "+ ..." line, in execution order, with
# args single-quoted when they hold anything outside [A-Za-z0-9_@%+=:,./-].
repo="$(cd "$here/../.." && pwd)"
err=""
pipe() { # <extra args...>: native dry-run; stdout into $out, stderr into $err
  local o; o="$(fresh_out)"
  set +e
  out="$(env -u BENCH_HOST_IN_CONTAINER BENCH_HOST_NPROC=24 bash "$script" native --host-label local-docker \
    --server-set 0 --gen-set 4-11 --out "$o" --dry-run "$@" 2>"$tmp/err")"; rc=$?
  set -e
  err="$(cat "$tmp/err")"
}
plus() { printf '%s\n' "$out" | grep '^+ ' || true; }
# in_order <name> <pattern>...: the patterns (fixed strings) match "+ " lines at strictly rising line numbers.
in_order() {
  local name="$1"; shift
  local last=0 pat n ok=1
  for pat in "$@"; do
    n="$(plus | grep -nF -- "$pat" | awk -F: -v l="$last" '$1>l{print $1; exit}' || true)"
    if [ -z "$n" ]; then ok=0; echo "  $name: no line after $last containing: $pat"; break; fi
    last="$n"
  done
  if [ "$ok" -eq 1 ]; then pass "$name"; else fail "$name"; fi
}
has() { # <name> <fixed substring in the + lines>
  if plus | grep -qF -- "$2"; then pass "$1"; else fail "$1 (missing: $2)"; fi
}
# lacks <name> <command filter> <fixed substring>: no "+" line matching the filter holds the substring
hasnt() {
  if plus | grep -F -- "$2" | grep -qF -- "$3"; then fail "$1"; else pass "$1"; fi
}
holds() { # <name> <command filter> <fixed substring>: some "+" line matching the filter holds it
  if plus | grep -F -- "$2" | grep -qF -- "$3"; then pass "$1"; else fail "$1 (no '$2' line with '$3')"; fi
}

pipe
[ "$rc" -eq 0 ] || fail "pipe-default (rc=$rc err=$err)"
default_out="$out"

# pins-match-ci: every pin is EXTRACTED from action.yml / bench.yml, never retyped here.
act="$repo/.github/actions/generate/action.yml"
wf="$repo/.github/workflows/bench.yml"
pv="$(grep -o 'protoc-[0-9.]*-linux-x86_64' "$act" | head -1)"
ps="$(grep -oE '[0-9a-f]{64}  protoc.zip' "$act" | head -1 | cut -d' ' -f1)"
gg="$(grep -o 'protoc-gen-go-grpc@v[0-9.]*' "$act" | head -1)"
buf="$(grep -o 'bufbuild/buf/cmd/buf@v[0-9.]*' "$wf" | head -1)"
bs="$(grep -o 'benchstat@v0[0-9a-z.-]*' "$wf" | head -1)"
cr="$(awk '/client_ref:/{f=1} f&&/default:/{gsub(/[^0-9a-f]/,"",$2); print $2; exit}' "$wf")"
for pair in "protoc:$pv" "protoc-sha:$ps" "grpc-gen:$gg" "buf:$buf" "benchstat:$bs" "client-ref:$cr"; do
  n="${pair%%:*}"; v="${pair#*:}"
  if [ -z "$v" ]; then fail "pins-match-ci-$n (could not extract from ci files)"; continue; fi
  out="$default_out"; has "pins-match-ci-$n" "$v"
done
[ "${#cr}" -eq 40 ] || fail "pins-match-ci-client-ref-length ($cr)"
out="$default_out"

# build-order
in_order build-order \
  assert-go-toolchain.sh \
  "$pv" \
  "sha256sum -c -" \
  "unzip -o" \
  "go install google.golang.org/protobuf/cmd/protoc-gen-go@v" \
  "$gg" \
  "protoc --go_out=./srspb" \
  "protoc --go_out=./voicecontrolpb" \
  "protoc --go_out=./vcsauthpb" \
  "gqlgen generate" \
  "go build -tags headless -trimpath -o " \
  "go build -trimpath -o " \
  "git clone" \
  "checkout $cr" \
  "client/go.mod" \
  "$buf generate" \
  "go build -tags purego -trimpath -o "
for b in "go build -tags headless" "go build -trimpath" "go build -tags purego"; do
  holds "build-cgo-off-[$b]" "$b" 'env CGO_ENABLED=0'
done
holds build-server-target 'go build -tags headless' 'vcs-server .'
holds build-loadbot-target 'go build -tags purego' './cmd/vcs-loadbot'
holds build-kit-target 'go build -trimpath' './cmd/vcs-benchkit'
# the client steps use the client's own toolchain (GOTOOLCHAIN=auto)
holds build-client-toolchain 'buf@' 'GOTOOLCHAIN=auto'
holds build-client-assert 'client/go.mod' 'GOTOOLCHAIN=auto'

# probe
in_order probe "bench-probe_test.sh" "bench-probe.sh"
holds probe-out '/bench-probe.sh' '/env-probe.json'

# micro
in_order micro \
  "go test -c -tags headless -o " \
  "taskset -c 0 env GOMAXPROCS=1 " \
  "benchstat" \
  "vcs-benchkit predict"
holds micro-run-empty 'taskset' "-test.run '^\$'"
holds micro-bench-regex 'taskset' "-test.bench '^Benchmark(SocketSend|GetListeningClients|HandleVoicePacket)\$'"
holds micro-flags 'taskset' '-test.benchmem -test.count=10'
holds predict-logger 'vcs-benchkit predict' '--logger bus'
o2="$(fresh_out)"
set +e
out="$(env -u BENCH_HOST_IN_CONTAINER BENCH_HOST_NPROC=24 bash "$script" native --host-label local-docker --server-set 0,1 --gen-set 4-11 --out "$o2" --dry-run 2>&1)"; rc=$?
set -e
has micro-gomaxprocs-two "taskset -c 0,1 env GOMAXPROCS=2 "

# micro skip with --predict: none of the micro commands run, F is copied to OUT/micro/predict.json
echo '{}' > "$tmp/pred.json"
pipe --micro skip --predict "$tmp/pred.json"
if plus | grep -qE 'voice.test|benchstat|vcs-benchkit predict'; then fail micro-skip-none; else pass micro-skip-none; fi
has micro-skip-copy "cp $tmp/pred.json $tmp/out/micro/predict.json"
has micro-skip-leg-predict "--predict $tmp/out/micro/predict.json"
pipe --micro skip --leg-arg --predicted-knee=300
hasnt micro-skip-knee-nopredict 'vcs-benchkit leg' '--predict '
has micro-skip-knee-arg "--predicted-knee=300"

# leg argv is a superset of bench.yml's "Run leg" step, parsed from the workflow
want="$(awk '/bin\/vcs-benchkit leg/{f=1} f{print} f&&!/\\$/{exit}' "$wf" | grep -oE -- '--[a-z0-9-]+' | sort -u)"
[ -n "$want" ] || fail "leg-flags-extracted"
pipe
legs="$(plus | grep -F 'vcs-benchkit leg' || true)"
okl=1
[ -n "$legs" ] || okl=0
while IFS= read -r line; do
  [ -n "$line" ] || continue
  for f in $want --server-set --gen-set --topology --label --generator-gc --budget --sysctl=true; do
    [[ "$line" == *"$f"* ]] || { okl=0; echo "  leg line lacks $f"; }
  done
done <<< "$legs"
if [ "$okl" -eq 1 ]; then pass leg-argv-superset-of-bench-yml; else fail leg-argv-superset-of-bench-yml; fi
has leg-hosts "--server-set 0 --gen-set 4-11"
has leg-budget "--budget 100m"
has leg-q6 "--q6 'host run: server 0, generator 4-11 (user, 2026-10-29)'"
has leg-tee "| tee $tmp/out/gogc-off/leg-1.log"
has leg-out "--out $tmp/out/gogc-off/results/1-one-big-net"
has leg-gc-off "--generator-gc off"

# gogc both: seeds alternate off/default; aggregate once per variant, never pooled
pipe --repeats 2 --gogc both
seq_="$(plus | grep -F 'vcs-benchkit leg' | sed -E 's/.*--seed ([0-9]+).*--generator-gc ([a-z]+).*/\1 \2/' | tr '\n' ',')"
if [ "$seq_" = "1 off,1 default,2 off,2 default," ]; then pass gogc-both-order; else fail "gogc-both-order ($seq_)"; fi
has gogc-both-out-off "--out $tmp/out/gogc-off/results/2-one-big-net"
has gogc-both-out-default "--out $tmp/out/gogc-default/results/1-one-big-net"
nagg="$(plus | grep -cF 'vcs-benchkit aggregate' || true)"
if [ "$nagg" -eq 2 ]; then pass gogc-both-two-aggregates; else fail "gogc-both-two-aggregates ($nagg)"; fi
lbl='local-docker, server cpus 0, generator cpus 4-11, loopback, relative only. Not a capacity figure.'
has gogc-both-agg-off "--in $tmp/out/gogc-off/results --out $tmp/out/gogc-off/aggregate --label '$lbl'"
has gogc-both-agg-default "--in $tmp/out/gogc-default/results --out $tmp/out/gogc-default/aggregate --label '$lbl'"
pipe --gogc default
hasnt gogc-default-only 'vcs-benchkit leg' '--generator-gc off'
has gogc-default-only-out "--out $tmp/out/gogc-default/results/1-one-big-net"
if plus | grep -qF 'gogc-off'; then fail gogc-default-no-off-dir; else pass gogc-default-no-off-dir; fi

# labels
pipe
has label-exact "--label '$lbl'"
has topology-exact "--topology 'local-docker: server 0 (1 cpu), generator 4-11 (8 cpu), smt false'"
holds predict-label-exact 'vcs-benchkit predict' "--label '$lbl'"
pipe --leg-arg --n-max=200
has label-nondefault "local-docker, server cpus 0, generator cpus 4-11, non-default leg args, loopback, relative only. Not a capacity figure."
has leg-arg-passthrough "--n-max=200"

# native-sysctl: the kit raises the socket buffers itself outside a container, never inside one
pipe
has sysctl-outside "--sysctl=true"
set +e
out="$(BENCH_HOST_IN_CONTAINER=1 BENCH_HOST_NPROC=24 bash "$script" native --host-label local-docker --server-set 0 --gen-set 4-11 --out "$(fresh_out)" --dry-run 2>&1)"; rc=$?
set -e
has sysctl-inside "--sysctl=false"

# tools-missing: a missing tool stops the run before anything is built (BENCH_HOST_HIDE_TOOLS is the hook)
set +e
out="$(BENCH_HOST_HIDE_TOOLS=jq BENCH_HOST_NPROC=24 bash "$script" native --host-label local-docker --server-set 0 --gen-set 4-11 --out "$(fresh_out)" 2>&1)"; rc=$?
set -e
expect tools-missing 2 "bench-host: needs jq"
if [ -e "$tmp/out/.work" ]; then fail tools-missing-no-build; else pass tools-missing-no-build; fi

# dry-run is side-effect free except for the three run records (H-T7)
pipe
if [ "$(ls -A "$tmp/out" | tr '
' ' ')" = "build.json host.json summary.md " ]; then pass dry-run-writes-only-records; else fail "dry-run-writes-only-records ($(ls -A "$tmp/out"))"; fi
# the work dir (about 1 GB) is outside --out
has work-outside-out "mkdir -p /"
if plus | grep -qF "$tmp/out/.work"; then fail work-outside-out-path; else pass work-outside-out-path; fi

# ---------------------------------------------------------------------------
# run records (H-T7): dry-run writes host.json, build.json and summary.md from injected fake values.
recs() { # <extra args...>: native dry-run with fake facts and stub results; records stay in $tmp/out
  local o; o="$(fresh_out)"
  set +e
  out="$(env -u BENCH_HOST_IN_CONTAINER BENCH_HOST_NPROC=24 BENCH_HOST_FAKE_SHA=abc123def456 BENCH_HOST_FAKE_DIRTY=true     BENCH_HOST_FAKE_KERNEL=6.18.33.2-microsoft-standard-WSL2 BENCH_HOST_FAKE_RMEM=4194304 BENCH_HOST_FAKE_WMEM=4194304     BENCH_HOST_FAKE_GO_VERSION=go1.27.3 BENCH_HOST_FAKE_CLIENT_GO_VERSION=go1.26.1 BENCH_HOST_FAKE_RESULTS=1     BENCH_HOST_IMAGE=golang:1.27.3@sha256:abc BENCH_HOST_DOCKER_VERSION=29.7.2     bash "$script" native --host-label local-docker --server-set 0 --gen-set 4-11 --out "$o" --dry-run "$@" 2>"$tmp/err")"; rc=$?
  set -e
  err="$(cat "$tmp/err")"
}
recs --gogc both --repeats 2 --leg-arg --n-max=200 --note 'High performance | apps closed'
[ "$rc" -eq 0 ] || fail "records-run (rc=$rc err=$err)"
hj="$tmp/out/host.json"; bj="$tmp/out/build.json"; sm="$tmp/out/summary.md"
lbl2='local-docker, server cpus 0, generator cpus 4-11, non-default leg args, loopback, relative only. Not a capacity figure.'
if jq -e --arg l "$lbl2" '
    (["host_label","mode","image","docker_version","kernel","nproc","server_set","gen_set","gogc","client_ref",
      "server_sha","server_dirty","rmem_max","wmem_max","leg_args","note","started_utc","ended_utc"] - keys) == []
    and .relative_only == true and .label == $l and .host_label == "local-docker" and .mode == "native"
    and .image == "golang:1.27.3@sha256:abc" and .docker_version == "29.7.2"
    and .kernel == "6.18.33.2-microsoft-standard-WSL2" and .nproc == 24
    and .server_set == "0" and .gen_set == "4-11" and .gogc == "both"
    and .server_sha == "abc123def456" and .server_dirty == true
    and .rmem_max == 4194304 and .wmem_max == 4194304
    and .leg_args == ["--n-max=200"] and .note == "High performance | apps closed"
    and (.started_utc | test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:]{8}Z$"))
    and (.ended_utc | test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:]{8}Z$"))
    and (.bot_label_note | test("raw")) and (.bot_label_note | test("Actions label"))' "$hj" >/dev/null; then
  pass host-json
else fail "host-json ($(cat "$hj" 2>&1))"; fi
cr_ref="$(jq -r .client_ref "$hj")"
if [ "$cr_ref" = "$cr" ]; then pass host-json-client-ref; else fail "host-json-client-ref ($cr_ref)"; fi
hn="$(hostname 2>/dev/null || true)"
if [ "${#hn}" -ge 4 ]; then
  if grep -rqiF -- "$hn" "$hj" "$bj" "$sm"; then fail host-json-no-hostname; else pass host-json-no-hostname; fi
else pass host-json-no-hostname; fi
# unknown facts are null, not invented
set +e
env -u BENCH_HOST_IMAGE -u BENCH_HOST_DOCKER_VERSION BENCH_HOST_NPROC=24 BENCH_HOST_FAKE_DIRTY=false BENCH_HOST_FAKE_RMEM= BENCH_HOST_FAKE_WMEM=   bash "$script" native --host-label local-linux --server-set 0 --gen-set 4-11 --out "$(fresh_out)" --dry-run >/dev/null 2>&1
set -e
if jq -e '.image == null and .docker_version == null and .server_dirty == false and .leg_args == [] and .note == ""
          and .host_label == "local-linux"' "$tmp/out/host.json" >/dev/null; then pass host-json-nulls; else fail "host-json-nulls ($(cat "$tmp/out/host.json"))"; fi
# BENCH_HOST_SERVER_SHA/DIRTY are the real channel (container has no .git) and win over the FAKE aliases
set +e
env -u BENCH_HOST_IN_CONTAINER BENCH_HOST_NPROC=24 BENCH_HOST_SERVER_SHA=feed1234 BENCH_HOST_SERVER_DIRTY=true BENCH_HOST_FAKE_SHA=old BENCH_HOST_FAKE_DIRTY=false BENCH_HOST_FAKE_RMEM= BENCH_HOST_FAKE_WMEM=   bash "$script" native --host-label local-linux --server-set 0 --gen-set 4-11 --out "$(fresh_out)" --dry-run >/dev/null 2>&1
set -e
if jq -e '.server_sha == "feed1234" and .server_dirty == true' "$tmp/out/host.json" >/dev/null; then pass host-json-server-sha-names; else fail "host-json-server-sha-names ($(cat "$tmp/out/host.json"))"; fi

recs --gogc both --repeats 2 --leg-arg --n-max=200 --note 'High performance | apps closed'
if jq -e '(keys - ["server_sha","go_version","client_sha","client_go_version","server_dirty"]) == []
          and (["server_sha","go_version","client_sha","client_go_version","server_dirty"] - keys) == []
          and .server_sha == "abc123def456" and .go_version == "go1.27.3"
          and .client_sha == "'"$cr"'" and .client_go_version == "go1.26.1" and .server_dirty == true' "$bj" >/dev/null; then
  pass build-json
else fail "build-json ($(cat "$bj" 2>&1))"; fi

# summary-md: heading, label line, facts table, then per variant a "## GOGC" heading with each results.md and the aggregate
in_order_file() { # <name> <file> <pattern>...: fixed-string patterns on rising line numbers
  local name="$1" f="$2"; shift 2
  local last=0 pat n ok=1
  for pat in "$@"; do
    n="$(grep -nF -- "$pat" "$f" | awk -F: -v l="$last" '$1>l{print $1; exit}' || true)"
    if [ -z "$n" ]; then ok=0; echo "  $name: no line after $last containing: $pat"; break; fi
    last="$n"
  done
  if [ "$ok" -eq 1 ]; then pass "$name"; else fail "$name"; fi
}
in_order_file summary-md "$sm"   "# Bench host run: local-docker"   "$lbl2"   "| fact | value |"   "| host_label | local-docker |"   "| server_dirty | true |"   "## GOGC off"   "FAKE results GOGC off leg 1"   "FAKE results GOGC off leg 2"   "FAKE aggregate GOGC off"   "## GOGC default"   "FAKE results GOGC default leg 1"   "FAKE results GOGC default leg 2"   "FAKE aggregate GOGC default"
if [ "$(sed -n 1p "$sm")" = "# Bench host run: local-docker" ]; then pass summary-md-first-line; else fail summary-md-first-line; fi
if grep -qF 'High performance \| apps closed' "$sm"; then pass summary-md-escapes-pipes; else fail summary-md-escapes-pipes; fi
# the summary is also printed to stdout at the end
if printf '%s
' "$out" | grep -qF "FAKE aggregate GOGC default"; then pass summary-printed; else fail summary-printed; fi
# a leg that has not finished still gets a section
pipe --gogc default
if grep -qF '_no results.md' "$tmp/out/summary.md"; then pass summary-md-missing-results; else fail summary-md-missing-results; fi


# ---------------------------------------------------------------------------
# H-T8: docker launcher (dry-run; BENCH_HOST_FAKE_SHA/DIRTY/PWDW/UNAME are the hooks)
repo_root="$(cd "$here/../.." && pwd)"
dk() { # <flags...>: docker dry-run; sets out (stdout+stderr), dline (the docker run line), rc
  local o; o="$(fresh_out)"
  set +e
  out="$(env -u BENCH_HOST_IN_CONTAINER BENCH_HOST_NPROC=24 BENCH_HOST_FAKE_SHA=0123abcd BENCH_HOST_FAKE_DIRTY=false \
    BENCH_HOST_FAKE_UNAME=Linux BENCH_HOST_DOCKER_VERSION=29.7.2 bash "$script" docker --host-label local-docker --server-set 0 --gen-set 4-11 \
    --out "$o" --dry-run "$@" 2>&1)"; rc=$?
  set -e
  dline="$(printf '%s\n' "$out" | grep -F '+ docker run' || true)"
}
nocontain() { # <name> <word>: the docker run line must not contain the word
  if [[ "$dline" == *"$2"* ]]; then fail "$1 (found $2)"; else pass "$1"; fi
}
cmd_word() { # prints the single bash -c argument of the docker run line (one shell word, unquoted)
  local inner
  inner="$(printf '%s\n' "$dline" | sed -n 's/^.* bash -c \(.*\)$/\1/p')"
  eval "set -- $inner"
  printf '%s' "$1"
}

# image-tag-matches-dockerfile
dk
img="$(printf '%s\n' "$dline" | grep -o 'golang:[0-9][0-9.]*@sha256:[0-9a-f]\{64\}' | head -n 1 || true)"
df_tag="$(sed -n 's/^FROM golang:\([0-9][0-9.]*\)\( .*\)\{0,1\}$/\1/p' "$repo_root/Dockerfile" | head -n 1)"
if [ -n "$img" ] && [ -n "$df_tag" ] && [ "${img%%@*}" = "golang:$df_tag" ]; then pass image-tag-matches-dockerfile
else fail "image-tag-matches-dockerfile (image='$img' dockerfile tag='$df_tag')"; fi

# docker-default-unprivileged
expect docker-default-rc 0 "bench-host: docker arguments ok (dry-run)"
ok=1
for w in "docker run --rm --init" "--name vcs-bench-host-" ":/out" "vcs-bench-gomod:/go/pkg/mod" \
  "vcs-bench-gocache:/root/.cache/go-build" "$img" "bash -c" "bench-host.sh native" "--out /out"; do
  [[ "$dline" == *"$w"* ]] || { ok=0; echo "  docker line lacks: $w"; }
done
if [ "$ok" -eq 1 ]; then pass docker-default-line; else fail "docker-default-line ($dline)"; fi
for w in --privileged --cap-add --sysctl --network --pid /var/run/docker.sock --device; do
  nocontain "docker-default-no-${w#--}" "$w"
done
if [[ "$dline" == *"BENCH_HOST_IN_CONTAINER=1"* ]]; then pass docker-in-container-env; else fail "docker-in-container-env ($dline)"; fi
if [[ "$out" == *"WARNING: --privileged"* ]]; then fail docker-default-no-warning; else pass docker-default-no-warning; fi
if [[ "$dline" == *"BENCH_HOST_SERVER_SHA=0123abcd"* && "$dline" == *"BENCH_HOST_SERVER_DIRTY=false"* \
  && "$dline" == *"BENCH_HOST_IMAGE=$img"* && "$dline" == *"BENCH_HOST_DOCKER_VERSION=29.7.2"* ]]; then pass docker-env-facts
else fail "docker-env-facts ($dline)"; fi
if [[ "$dline" == *"apt-get install -y -qq --no-install-recommends jq unzip"* && "$dline" == *"BENCH_HOST_TOOL_VERSIONS"* ]]; then pass docker-installs-jq-unzip
else fail "docker-installs-jq-unzip ($dline)"; fi
if [[ "$dline" == *"8388608"* || "$dline" == *"trap"* ]]; then fail docker-default-no-sysctl-code; else pass docker-default-no-sysctl-code; fi

# docker-src-archive
a="$(printf '%s\n' "$out" | grep -n 'archive --format=tar HEAD -o ' | head -n 1 | cut -d: -f1 || true)"
d="$(printf '%s\n' "$out" | grep -n '^+ docker run' | head -n 1 | cut -d: -f1 || true)"
if [ -n "$a" ] && [ -n "$d" ] && [ "$a" -lt "$d" ] && printf '%s\n' "$out" | grep -qF '/.src.tar'; then pass docker-src-archive-before-run
else fail "docker-src-archive-before-run (archive line '$a', docker line '$d')"; fi
if printf '%s
' "$out" | grep -qF 'git -c core.autocrlf=false archive --format=tar HEAD -o '; then pass docker-src-archive-lf; else fail "docker-src-archive-lf (git archive would give CRLF scripts under core.autocrlf=true)"; fi
if [[ "$dline" == *"DEBIAN_FRONTEND=noninteractive"* ]]; then pass docker-apt-noninteractive; else fail docker-apt-noninteractive; fi
if [[ "$dline" == *"tar -x -C /work/src -f /out/.src.tar"* && "$dline" == *"cd /work/src"* && "$dline" == *"rm -f /out/.src.tar"* ]]; then pass docker-src-untar
else fail "docker-src-untar ($dline)"; fi
if [[ "$out" == *"WARNING: uncommitted changes are not benched"* ]]; then fail docker-clean-no-dirty-warning; else pass docker-clean-no-dirty-warning; fi
o="$(fresh_out)"
set +e
out="$(env -u BENCH_HOST_IN_CONTAINER BENCH_HOST_NPROC=24 BENCH_HOST_FAKE_SHA=0123abcd BENCH_HOST_FAKE_DIRTY=true bash "$script" docker \
  --host-label local-docker --server-set 0 --gen-set 4-11 --out "$o" --dry-run 2>&1)"; rc=$?
set -e
dline="$(printf '%s\n' "$out" | grep -F '+ docker run' || true)"
expect docker-dirty-warns 0 "WARNING: uncommitted changes are not benched"
if [[ "$dline" == *"BENCH_HOST_SERVER_DIRTY=true"* ]]; then pass docker-dirty-forwarded; else fail "docker-dirty-forwarded ($dline)"; fi
# without the hooks the host's own git is asked
if git -C "$repo_root" rev-parse HEAD >/dev/null 2>&1; then
  head_sha="$(git -C "$repo_root" rev-parse HEAD)"
  o="$(fresh_out)"
  set +e
  out="$(env -u BENCH_HOST_IN_CONTAINER -u BENCH_HOST_FAKE_SHA -u BENCH_HOST_FAKE_DIRTY BENCH_HOST_FAKE_UNAME=Linux BENCH_HOST_NPROC=24 bash "$script" docker \
    --host-label local-docker --server-set 0 --gen-set 4-11 --out "$o" --dry-run 2>&1)"; rc=$?
  set -e
  if [[ "$out" == *"BENCH_HOST_SERVER_SHA=$head_sha"* ]]; then pass docker-sha-from-git; else fail "docker-sha-from-git"; fi
  # Git Bash with MSYS_NO_PATHCONV=1 gives git.exe a /d/... path it cannot open, so `git -C <repo>` fails there
  # (found by the H-T9 smoke run: server_sha "unknown", server_dirty null). The script must cd instead.
  mkdir -p "$tmp/shim"
  real_git="$(command -v git)"
  printf '#!/usr/bin/env bash\nif [ "${1:-}" = "-C" ]; then echo "fatal: cannot change to $2" >&2; exit 128; fi\nexec "%s" "$@"\n' "$real_git" > "$tmp/shim/git"
  chmod +x "$tmp/shim/git"
  set +e
  out="$(env -u BENCH_HOST_IN_CONTAINER -u BENCH_HOST_FAKE_SHA -u BENCH_HOST_FAKE_DIRTY PATH="$tmp/shim:$PATH" BENCH_HOST_FAKE_UNAME=Linux BENCH_HOST_NPROC=24 bash "$script" docker \
    --host-label local-docker --server-set 0 --gen-set 4-11 --out "$(fresh_out)" --dry-run 2>&1)"; rc=$?
  set -e
  if [[ "$out" == *"BENCH_HOST_SERVER_SHA=$head_sha"* && ( "$out" == *"BENCH_HOST_SERVER_DIRTY=true"* || "$out" == *"BENCH_HOST_SERVER_DIRTY=false"* ) ]]; then
    pass docker-sha-without-git-C; else fail "docker-sha-without-git-C"; fi
fi

# docker-flags-forwarded: verbatim, except --out, --privileged-sysctl and --dry-run; --predict is staged
pf="$tmp/predict.json"; echo '{}' > "$pf"
dk --gogc both --repeats 2 --leg-arg --n-max=200 --leg-arg '--x=a b' --note "it's | odd" --micro skip --predict "$pf" --budget 30m \
  --client-ref 0123456789012345678901234567890123456789 --arms S --scenario one-big-net
word="$(cmd_word)"
ok=1
for w in "--host-label local-docker" "--server-set 0" "--gen-set 4-11" "--gogc both" "--repeats 2" "--leg-arg --n-max=200" \
  "--micro skip" "--budget 30m" "--client-ref 0123456789012345678901234567890123456789" "--arms S" "--scenario one-big-net" \
  "--predict /out/.predict.json" "--out /out"; do
  [[ "$word" == *"$w"* ]] || { ok=0; echo "  forward lacks: $w"; }
done
[[ "$word" == *"--leg-arg '--x=a b'"* ]] || { ok=0; echo "  value with a space was not kept as one word"; }
[[ "$word" == *"it'\\''s | odd"* ]] || { ok=0; echo "  value with a quote was not escaped"; }
[[ "$word" != *"--dry-run"* ]] || { ok=0; echo "  --dry-run was forwarded"; }
[[ "$word" != *"$pf"* ]] || { ok=0; echo "  host predict path leaked into the container"; }
if [ "$ok" -eq 1 ]; then pass docker-flags-forwarded; else fail "docker-flags-forwarded ($word)"; fi
if [ -f "$tmp/out/.predict.json" ]; then pass docker-predict-copied; else fail docker-predict-copied; fi
# the final argument is --out /out
case "$word" in *"--out /out") pass docker-out-last ;; *) fail "docker-out-last ($word)" ;; esac

# docker-privileged-optin-warns
dk --privileged-sysctl
expect docker-privileged-rc 0 "bench-host: docker arguments ok (dry-run)"
if [[ "$dline" == *"--privileged --network host"* ]]; then pass docker-privileged-flags; else fail "docker-privileged-flags ($dline)"; fi
nocontain docker-privileged-no-cap-add --cap-add
nocontain docker-privileged-no-sysctl-flag "--sysctl"
word="$(cmd_word)"
if [[ "$word" == *"--privileged-sysctl"* ]]; then fail docker-privileged-not-forwarded; else pass docker-privileged-not-forwarded; fi
warn="WARNING: --privileged-sysctl runs the container privileged and changes the host's net.core.rmem_max/wmem_max until reboot; the script restores the old values on exit"
if [[ "$out" == *"$warn"* ]]; then pass docker-privileged-warning-text; else fail "docker-privileged-warning-text ($out)"; fi
set +e
errtxt="$(env -u BENCH_HOST_IN_CONTAINER BENCH_HOST_NPROC=24 BENCH_HOST_FAKE_SHA=0123abcd BENCH_HOST_FAKE_DIRTY=false bash "$script" docker \
  --host-label local-docker --server-set 0 --gen-set 4-11 --out "$(fresh_out)" --dry-run --privileged-sysctl 2>&1 >/dev/null)"
set -e
if [[ "$errtxt" == *"$warn"* ]]; then pass docker-privileged-warning-on-stderr; else fail docker-privileged-warning-on-stderr; fi
if [[ "$word" == *"8388608"* && "$word" == *"trap"* && "$word" == *"EXIT"* && "$word" == *"refused: "* \
  && "$word" == *"BENCH_HOST_SYSCTL_RAISE"* && "$word" == *"rmem_max"* && "$word" == *"wmem_max"* ]]; then pass docker-privileged-raise-and-restore
else fail "docker-privileged-raise-and-restore ($word)"; fi
if [[ "$word" == *"exec bash"* ]]; then fail docker-privileged-no-exec; else pass docker-privileged-no-exec; fi

# the raise/restore code really runs: aim it at fake /proc files, with the real native run stubbed out
sandbox="$tmp/proc"; mkdir -p "$sandbox"
echo 4194304 > "$sandbox/rmem_max"; echo 4194304 > "$sandbox/wmem_max"
sc="${word//\/proc\/sys\/net\/core\//$sandbox/}"
case "$sc" in *"/proc/sys"*) fail "docker-privileged-sandbox (still touches /proc/sys)" ;; esac
stub='apt-get() { :; }; tar() { :; }; bash() { echo "SYSCTL_RAISE=$BENCH_HOST_SYSCTL_RAISE"; echo "RMEM_DURING=$(cat '"$sandbox"'/rmem_max)"; }; cd() { :; }; mkdir() { :; }; rm() { :; }; jq() { echo jq-stub; }; unzip() { echo unzip-stub; }; '
sout="$(env -u BENCH_HOST_TOOL_VERSIONS bash -c "$stub$sc" 2>&1 || true)"
if [[ "$sout" == *"SYSCTL_RAISE=raised"* && "$sout" == *"RMEM_DURING=8388608"* && "$(cat "$sandbox/rmem_max")" = "4194304" && "$(cat "$sandbox/wmem_max")" = "4194304" ]]; then
  pass docker-privileged-raise-restores
else fail "docker-privileged-raise-restores ($sout; rmem now $(cat "$sandbox/rmem_max"))"; fi
# a refused write is recorded and the run continues. A directory in place of the file refuses the write for any user, root included.
rm -f "$sandbox/rmem_max"; mkdir "$sandbox/rmem_max"
sout="$(env -u BENCH_HOST_TOOL_VERSIONS bash -c "$stub$sc" 2>&1 || true)"
if [[ "$sout" == *"SYSCTL_RAISE=refused: "* ]]; then pass docker-privileged-refusal-recorded; else fail "docker-privileged-refusal-recorded ($sout)"; fi

# docker-msys: Git Bash gets MSYS_NO_PATHCONV and pwd -W paths
o="$(fresh_out)"
set +e
out="$(env -u BENCH_HOST_IN_CONTAINER BENCH_HOST_NPROC=24 BENCH_HOST_FAKE_SHA=0123abcd BENCH_HOST_FAKE_DIRTY=false BENCH_HOST_FAKE_UNAME=MINGW64_NT-10.0 \
  BENCH_HOST_FAKE_PWDW='C:/Users/x/out' bash "$script" docker --host-label local-docker --server-set 0 --gen-set 4-11 --out "$o" --dry-run 2>&1)"; rc=$?
set -e
dline="$(printf '%s\n' "$out" | grep -F '+ docker run' || true)"
if [[ "$dline" == *"-v C:/Users/x/out:/out"* ]]; then pass docker-msys-pwdw; else fail "docker-msys-pwdw ($dline)"; fi
if [[ "$out" == *"MSYS_NO_PATHCONV=1"* ]]; then pass docker-msys-no-pathconv-noted; else fail "docker-msys-no-pathconv-noted ($out)"; fi
dk
if [[ "$dline" == *"C:/Users"* ]]; then fail docker-nonmsys-plain-path; else pass docker-nonmsys-plain-path; fi

# missing docker: a real docker mode refuses before it archives anything
set +e
out="$(env -u BENCH_HOST_IN_CONTAINER BENCH_HOST_HIDE_TOOLS=docker BENCH_HOST_NPROC=24 BENCH_HOST_FAKE_SHA=0123abcd BENCH_HOST_FAKE_DIRTY=false \
  bash "$script" docker --host-label local-docker --server-set 0 --gen-set 4-11 --out "$(fresh_out)" 2>&1)"; rc=$?
set -e
expect docker-missing 2 "bench-host: docker mode needs docker"
if [ ! -e "$tmp/out/.src.tar" ]; then pass docker-missing-no-archive; else fail docker-missing-no-archive; fi

# native inside the container tolerates the two staged dotfiles in --out, nothing else
o="$(fresh_out)"; : > "$o/.predict.json"; : > "$o/.src.tar"
set +e
out="$(BENCH_HOST_IN_CONTAINER=1 BENCH_HOST_NPROC=24 bash "$script" native --host-label local-docker --server-set 0 --gen-set 4-11 --out "$o" --dry-run 2>&1)"; rc=$?
set -e
expect native-container-tolerates-staging 0 ""
: > "$o/stray"
set +e
out="$(BENCH_HOST_IN_CONTAINER=1 BENCH_HOST_NPROC=24 bash "$script" native --host-label local-docker --server-set 0 --gen-set 4-11 --out "$o" --dry-run 2>&1)"; rc=$?
set -e
expect native-container-rejects-stray 2 "must be empty"
o="$(fresh_out)"; : > "$o/.src.tar"
set +e
out="$(env -u BENCH_HOST_IN_CONTAINER BENCH_HOST_NPROC=24 bash "$script" native --host-label local-docker --server-set 0 --gen-set 4-11 --out "$o" --dry-run 2>&1)"; rc=$?
set -e
expect native-host-rejects-staging 2 "must be empty"

# dry-run docker must not create --out when it does not exist
nx="$tmp/nx-out"; rm -rf "$nx"
set +e
out="$(env -u BENCH_HOST_IN_CONTAINER BENCH_HOST_NPROC=24 BENCH_HOST_FAKE_SHA=0123abcd BENCH_HOST_FAKE_DIRTY=false BENCH_HOST_FAKE_UNAME=Linux   bash "$script" docker --host-label local-docker --server-set 0 --gen-set 4-11 --out "$nx" --dry-run 2>&1)"; rc=$?
set -e
expect docker-dryrun-missing-out-ok 0 "-v $nx:/out"
if [ ! -e "$nx" ]; then pass docker-dryrun-no-mkdir; else fail docker-dryrun-no-mkdir; fi

# a failed git archive must not leave .src.tar behind in --out
stubbin="$tmp/stubbin"; mkdir -p "$stubbin"
cat > "$stubbin/git" <<'STUB'
#!/usr/bin/env bash
for a in "$@"; do if [ "${prev:-}" = "-o" ]; then echo partial > "$a"; fi; prev="$a"; done
exit 1
STUB
printf '#!/usr/bin/env bash
exit 0
' > "$stubbin/docker"
chmod +x "$stubbin/git" "$stubbin/docker"
o="$(fresh_out)"
set +e
out="$(env -u BENCH_HOST_IN_CONTAINER PATH="$stubbin:$PATH" BENCH_HOST_NPROC=24 BENCH_HOST_FAKE_SHA=0123abcd BENCH_HOST_FAKE_DIRTY=false BENCH_HOST_FAKE_UNAME=Linux   BENCH_HOST_DOCKER_VERSION=29.7.2 bash "$script" docker --host-label local-docker --server-set 0 --gen-set 4-11 --out "$o" 2>&1)"; rc=$?
set -e
if [ "$rc" -ne 0 ]; then pass docker-archive-failure-rc; else fail "docker-archive-failure-rc ($out)"; fi
if [ ! -e "$o/.src.tar" ]; then pass docker-archive-failure-cleans-src-tar; else fail docker-archive-failure-cleans-src-tar; fi

# host.json carries the sysctl outcome and the tool versions (null when unknown)
set +e
env -u BENCH_HOST_IN_CONTAINER BENCH_HOST_NPROC=24 BENCH_HOST_FAKE_DIRTY=false BENCH_HOST_SYSCTL_RAISE='refused: Permission denied' \
  BENCH_HOST_TOOL_VERSIONS='jq 1.7.1, unzip 6.0' bash "$script" native --host-label local-docker --server-set 0 --gen-set 4-11 --out "$(fresh_out)" --dry-run >/dev/null 2>&1
set -e
if jq -e '.sysctl_raise == "refused: Permission denied" and .tool_versions == "jq 1.7.1, unzip 6.0"' "$tmp/out/host.json" >/dev/null; then pass host-json-sysctl-tools
else fail "host-json-sysctl-tools ($(cat "$tmp/out/host.json"))"; fi
set +e
env -u BENCH_HOST_IN_CONTAINER -u BENCH_HOST_SYSCTL_RAISE -u BENCH_HOST_TOOL_VERSIONS BENCH_HOST_NPROC=24 BENCH_HOST_FAKE_DIRTY=false bash "$script" native --host-label local-docker --server-set 0 --gen-set 4-11 --out "$(fresh_out)" --dry-run >/dev/null 2>&1
set -e
if jq -e '.sysctl_raise == null and .tool_versions == null' "$tmp/out/host.json" >/dev/null; then pass host-json-sysctl-tools-null; else fail host-json-sysctl-tools-null; fi

echo
echo "$failures failures"
[ "$failures" -eq 0 ]
