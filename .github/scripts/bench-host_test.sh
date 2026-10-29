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

echo
echo "$failures failures"
[ "$failures" -eq 0 ]
