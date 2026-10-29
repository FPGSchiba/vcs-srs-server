#!/usr/bin/env bash
# Runs the Wave 6 bench on a host we control (local Docker, or any Linux host such as EC2).
# Results are relative only and are never published. See --help.
set -euo pipefail

DEFAULT_CLIENT_REF='b55823e195577506ba5c3fa2c6d16a48a5f9bd5b'

die() { echo "bench-host: $*" >&2; exit 2; }

usage() {
  cat <<'USAGE'
Usage:
  bench-host.sh docker [flags]   run inside a Docker container (any Docker host)
  bench-host.sh native [flags]   run on this Linux host (inside the container, or on EC2)

Runs build, probe, micro, leg(s) and aggregate exactly like the Actions bench, with CPU sets you
choose. Results are labelled with the host type and "relative only". Nothing is published.

Flags:
  --host-label L        required. local-docker, local-linux or aws-<type> (e.g. aws-c7i.4xlarge)
  --server-set CPUS     required. taskset list for the server (1 or 2 cpus)
  --gen-set CPUS        required. taskset list for the load bot
  --arms S              default S. Seq and D2 arrive with kit S5
  --scenario NAME       default one-big-net (the only scenario)
  --repeats K           default 1. 1 to 5 legs per GOGC variant, seeds 1..K, run serially
  --gogc off|default|both
                        default off. off = GOGC=off GOMEMLIMIT=1GiB (the kit), default = Go
                        defaults, both = alternate off/default for each seed
  --client-ref SHA      default the pinned bench.yml ref. A full 40-hex SHA
  --out DIR             required. Results directory; must be empty or absent
  --micro run|skip      default run. skip needs --predict FILE or --leg-arg --predicted-knee=N
  --predict FILE        a predict.json from an earlier run (with --micro skip)
  --budget D            default 100m. The kit's leg budget
  --leg-arg ARG         repeatable pass-through to vcs-benchkit leg (smoke runs only)
  --note TEXT           free text recorded in host.json
  --privileged-sysctl   docker mode only, opt-in. Runs the container --privileged with
                        --network host to raise rmem_max/wmem_max; prints a warning
  --dry-run             print every command instead of running it
  --help                this text

EC2 (no extra tooling; the instance is yours to provide):
  git archive HEAD | ssh <host> 'mkdir -p vcs && tar -x -C vcs'
  ssh <host> 'cd vcs && bash .github/scripts/bench-host.sh native --host-label aws-<type> --server-set 0 --gen-set 4-11 --out ~/bench'
  scp -r <host>:bench .
Native mode needs Go 1.21 or newer with GOTOOLCHAIN=auto, plus jq, unzip, curl, git and taskset.
USAGE
}

host_label=""; server_set=""; gen_set=""; out_dir=""
have_host_label=0; have_server_set=0; have_gen_set=0; have_out=0
arms="S"; scenario="one-big-net"; repeats="1"; gogc="off"
client_ref="$DEFAULT_CLIENT_REF"; micro="run"; predict=""; budget="100m"; note=""
privileged_sysctl=0; dry_run=0
leg_args=()
fwd=()   # flags forwarded verbatim into the container (docker mode); --out and --predict are rewritten there

mode=""
case "${1:-}" in
  -h|--help) usage; exit 0 ;;
  docker|native) mode="$1"; shift ;;
  *) die "mode must be docker or native (see --help)" ;;
esac

while [ $# -gt 0 ]; do
  flag="$1"
  case "$flag" in
    -h|--help) usage; exit 0 ;;
    --privileged-sysctl) privileged_sysctl=1; shift; continue ;;
    --dry-run) dry_run=1; shift; continue ;;
    --host-label|--server-set|--gen-set|--arms|--scenario|--repeats|--gogc|--client-ref|--out|--micro|--predict|--budget|--leg-arg|--note)
      [ $# -ge 2 ] || die "$flag needs a value"
      val="$2"; shift 2
      case "$flag" in --out|--predict) ;; *) fwd+=("$flag" "$val") ;; esac
      case "$flag" in
        --host-label) host_label="$val"; have_host_label=1 ;;
        --server-set) server_set="$val"; have_server_set=1 ;;
        --gen-set) gen_set="$val"; have_gen_set=1 ;;
        --arms) arms="$val" ;;
        --scenario) scenario="$val" ;;
        --repeats) repeats="$val" ;;
        --gogc) gogc="$val" ;;
        --client-ref) client_ref="$val" ;;
        --out) out_dir="$val"; have_out=1 ;;
        --micro) micro="$val" ;;
        --predict) predict="$val" ;;
        --budget) budget="$val" ;;
        --leg-arg) leg_args+=("$val") ;;
        --note) note="$val" ;;
      esac ;;
    -*) die "unknown flag $flag" ;;
    *) die "unexpected argument $flag" ;;
  esac
done

[ "$have_host_label" -eq 1 ] || die "--host-label is required"
[ "$have_server_set" -eq 1 ] || die "--server-set is required"
[ "$have_gen_set" -eq 1 ] || die "--gen-set is required"
[ "$have_out" -eq 1 ] || die "--out is required"

host_label_re='^(local-docker|local-linux|aws-[a-z0-9-]+\.[a-z0-9]+)$'
[[ "$host_label" =~ $host_label_re ]] || die "--host-label must be local-docker, local-linux or aws-<type> (got '$host_label')"
case "$arms" in
  S) ;;
  Seq|D2) die "--arms $arms: Seq and D2 arrive with kit S5; only S exists" ;;
  *) die "--arms must be S (got '$arms')" ;;
esac
[ "$scenario" = "one-big-net" ] || die "--scenario must be one-big-net (got '$scenario')"
[[ "$repeats" =~ ^[1-5]$ ]] || die "--repeats must be 1 to 5 (got '$repeats')"
case "$gogc" in off|default|both) ;; *) die "--gogc must be off, default or both (got '$gogc')" ;; esac
[[ "$client_ref" =~ ^[0-9a-f]{40}$ ]] || die "--client-ref must be a full 40-hex SHA"
case "$micro" in run|skip) ;; *) die "--micro must be run or skip (got '$micro')" ;; esac
budget_re='^([0-9]+(ns|us|ms|s|m|h))+$'
[[ "$budget" =~ $budget_re ]] || die "--budget must be a Go duration such as 100m or 1h30m (got '$budget')"
if [ -n "$predict" ] && [ ! -f "$predict" ]; then die "--predict $predict is not a file"; fi
if [ "$privileged_sysctl" -eq 1 ] && [ "$mode" != "docker" ]; then
  die "--privileged-sysctl is for docker mode only"
fi
if [ "$micro" = "skip" ]; then
  knee=0
  for a in ${leg_args[@]+"${leg_args[@]}"}; do
    case "$a" in --predicted-knee=*) knee=1 ;; esac
  done
  if [ -z "$predict" ] && [ "$knee" -eq 0 ]; then
    die "--micro skip needs --predict FILE or --leg-arg --predicted-knee=N"
  fi
fi

# cpu_expand FLAG LIST: expands a taskset list ("0-1,4") into the global array CPUS. Same rules as
# leg.CPUList: no empty list or element, no negative number, no descending range. Bash 3.2 safe.
CPUS=()
cpu_expand() {
  local flag="$1" list="$2" el lo hi c
  CPUS=()
  [ -n "$list" ] || die "$flag: empty cpu list"
  local parts
  IFS=',' read -ra parts <<< "$list"
  # a trailing comma leaves no empty last element in read -a, so check the raw string too
  case "$list" in ,*|*,|*,,*) die "$flag: cpu list '$list' has an empty element" ;; esac
  for el in "${parts[@]}"; do
    if [[ "$el" =~ ^[0-9]{1,5}$ ]]; then
      CPUS+=("$((10#$el))")
    elif [[ "$el" =~ ^([0-9]{1,5})-([0-9]{1,5})$ ]]; then
      lo=$((10#${BASH_REMATCH[1]})); hi=$((10#${BASH_REMATCH[2]}))
      [ "$hi" -ge "$lo" ] || die "$flag: cpu list '$list' has a bad range '$el'"
      c="$lo"
      while [ "$c" -le "$hi" ]; do CPUS+=("$c"); c=$((c + 1)); done
    else
      die "$flag: cpu list '$list' has a bad element '$el'"
    fi
  done
}

cpu_expand --server-set "$server_set"; server_cpus=("${CPUS[@]}")
cpu_expand --gen-set "$gen_set";       gen_cpus=("${CPUS[@]}")
[ "${#server_cpus[@]}" -ge 1 ] && [ "${#server_cpus[@]}" -le 2 ] || die "server set must be 1 or 2 cpus (got ${#server_cpus[@]} in '$server_set')"

overlap=""
for s in "${server_cpus[@]}"; do
  for g in "${gen_cpus[@]}"; do
    if [ "$s" -eq "$g" ]; then overlap="${overlap:+$overlap,}$s"; fi
  done
done
[ -z "$overlap" ] || die "server and generator cpu sets overlap on $overlap"

# Range check against the online cpu count. BENCH_HOST_NPROC is the test hook. In docker mode the
# host's own count is not the container VM's, so without the hook the check is left to native mode
# inside the container.
host_nproc="${BENCH_HOST_NPROC:-}"
if [ -z "$host_nproc" ] && [ "$mode" = "native" ]; then host_nproc="$(nproc)"; fi
if [ -n "$host_nproc" ]; then
  for c in "${server_cpus[@]}" "${gen_cpus[@]}"; do
    [ "$c" -lt "$host_nproc" ] || die "cpu $c is not online (host has $host_nproc cpus, valid 0-$((host_nproc - 1)))"
  done
fi

# Sibling warning: a shared physical core skews both sides. A warning, not an error. In docker mode
# the Docker host's /sys is not the container VM's topology, so without the BENCH_HOST_SYSFS test
# hook the check is skipped here and native mode inside the container does the real one.
sysfs_root="${BENCH_HOST_SYSFS:-}"
sib_scan=("${server_cpus[@]}")
if [ "$mode" = "docker" ] && [ -z "$sysfs_root" ]; then sib_scan=(); fi
for s in ${sib_scan[@]+"${sib_scan[@]}"}; do
  sib="$sysfs_root/sys/devices/system/cpu/cpu$s/topology/thread_siblings_list"
  [ -r "$sib" ] || continue
  sl="$(tr -d "[:space:]" < "$sib")"
  ( cpu_expand "cpu$s siblings" "$sl" ) >/dev/null 2>&1 || continue
  cpu_expand "cpu$s siblings" "$sl"
  for t in "${CPUS[@]}"; do
    for g in "${gen_cpus[@]}"; do
      if [ "$t" -eq "$g" ] && [ "$t" -ne "$s" ]; then
        echo "bench-host: WARNING: server cpu $s shares a physical core with generator cpu $g" >&2
      fi
    done
  done
done

if [ -e "$out_dir" ]; then
  [ -d "$out_dir" ] || die "--out $out_dir is not a directory"
  if [ -n "${BENCH_HOST_IN_CONTAINER:-}" ]; then
    # the docker launcher stages the source archive and a --predict file in --out; nothing else may be there
    [ -z "$(ls -A "$out_dir" | grep -v -x -e .src.tar -e .predict.json || true)" ] || die "--out $out_dir must be empty"
  else
    [ -z "$(ls -A "$out_dir")" ] || die "--out $out_dir must be empty"
  fi
fi

if [ "$dry_run" -eq 1 ]; then
  echo "bench-host: $mode arguments ok (dry-run)"
fi

# ---------------------------------------------------------------------------
# Command helpers. In dry-run every command is printed as one "+ ..." line and nothing runs. Args
# are single-quoted when they hold anything outside [A-Za-z0-9_@%+=:,./-], so a printed line can
# be pasted into a shell.
q1() {
  case "$1" in
    "") printf "''" ;;
    *[!A-Za-z0-9_@%+=:,./-]*) printf "'%s'" "$(printf '%s' "$1" | sed "s/'/'\\\\''/g")" ;;
    *) printf '%s' "$1" ;;
  esac
}
qargs() {
  local a first=1
  for a in "$@"; do
    if [ "$first" -eq 1 ]; then first=0; else printf ' '; fi
    q1 "$a"
  done
}
run() { if [ "$dry_run" -eq 1 ]; then echo "+ $(qargs "$@")"; else "$@"; fi; }
run_in() { # DIR argv...
  local d="$1"; shift
  if [ "$dry_run" -eq 1 ]; then echo "+ cd $(q1 "$d") && $(qargs "$@")"; else ( cd "$d" && "$@" ); fi
}
run_to() { # FILE argv...: stdout to FILE
  local f="$1"; shift
  if [ "$dry_run" -eq 1 ]; then echo "+ $(qargs "$@") > $(q1 "$f")"; else "$@" > "$f"; fi
}
run_tee() { # FILE argv...: stdout and stderr to the console and to FILE; fails when argv fails (pipefail)
  local f="$1"; shift
  if [ "$dry_run" -eq 1 ]; then echo "+ $(qargs "$@") | tee $(q1 "$f")"; else "$@" 2>&1 | tee "$f"; fi
}
sha_check() { # FILE SHA256
  if [ "$dry_run" -eq 1 ]; then
    echo "+ echo '$2  $1' | sha256sum -c -"
  else
    echo "$2  $1" | sha256sum -c -
  fi
}
have_tool() { # BENCH_HOST_HIDE_TOOLS (space list) is the test hook for a missing tool
  case " ${BENCH_HOST_HIDE_TOOLS:-} " in *" $1 "*) return 1 ;; esac
  command -v "$1" >/dev/null 2>&1
}

# Pins. Third copy of .github/actions/generate/action.yml and bench.yml; bench-host_test.sh fails
# when they drift.
PROTOC_VERSION='36.2'
PROTOC_SHA256='121f6c7afe1d4d0e3ea6aab9432038599250134cbf4474cb1167d2c7decd4278'
PROTOC_GEN_GO_GRPC='google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2'
BUF='github.com/bufbuild/buf/cmd/buf@v1.47.2'
BENCHSTAT='golang.org/x/perf/cmd/benchstat@v0.0.0-20261009192801-be2c69fb417e'
CLIENT_URL='https://github.com/FPGSchiba/vcs-srs-client.git'
MICRO_RE='^Benchmark(SocketSend|GetListeningClients|HandleVoicePacket)$'

now_utc() { date -u +%Y-%m-%dT%H:%M:%SZ; }
num_or_null() { case "$1" in ''|*[!0-9]*) echo null ;; *) echo "$1" ;; esac; }
str_or_null() { if [ -n "$1" ]; then jq -n --arg v "$1" '$v'; else echo null; fi; }

# write_build_json and write_records read pipeline_native's locals through bash dynamic scope
# (server_sha, client_sha, dirty, go_version, label, started, variants, ...), so they are only called
# from there. All JSON is built with jq --arg/--argjson, never by string splicing.
write_build_json() {
  mkdir -p "$out_dir"
  jq -n --arg server_sha "$server_sha" --arg go_version "$go_version" \
    --arg client_sha "$client_sha" --arg client_go_version "$client_go_version" \
    --argjson server_dirty "$dirty" \
    '{server_sha: $server_sha, go_version: $go_version, client_sha: $client_sha,
      client_go_version: $client_go_version, server_dirty: $server_dirty}' > "$out_dir/build.json"
}

BOT_LABEL_NOTE="The pinned load bot (client b55823e) writes its own raw NDJSON lines in loadbot-*.log with the Actions label (Shared GitHub runner). That is the client's string and stays in the raw logs only; the kit's tables, CSVs and manifests carry the label above."

write_records() {
  mkdir -p "$out_dir"
  local ended legs_json a nproc_v kernel
  ended="$(now_utc)"
  legs_json='[]'
  for a in ${leg_args[@]+"${leg_args[@]}"}; do
    legs_json="$(printf '%s' "$legs_json" | jq -c --arg a "$a" '. + [$a]')"
  done
  nproc_v="${BENCH_HOST_NPROC:-$(nproc 2>/dev/null || true)}"
  kernel="${BENCH_HOST_FAKE_KERNEL:-$(uname -r 2>/dev/null || echo unknown)}"
  jq -n --arg host_label "$host_label" --arg mode "$mode" \
    --argjson image "$(str_or_null "${BENCH_HOST_IMAGE:-}")" \
    --argjson docker_version "$(str_or_null "${BENCH_HOST_DOCKER_VERSION:-}")" \
    --argjson tool_versions "$(str_or_null "${BENCH_HOST_TOOL_VERSIONS:-}")" \
    --argjson sysctl_raise "$(str_or_null "${BENCH_HOST_SYSCTL_RAISE:-}")" \
    --arg kernel "$kernel" --argjson nproc "$(num_or_null "$nproc_v")" \
    --arg server_set "$server_set" --arg gen_set "$gen_set" --arg gogc "$gogc" \
    --arg client_ref "$client_ref" --arg server_sha "$server_sha" --argjson server_dirty "$dirty" \
    --argjson rmem_max "$(num_or_null "$rmem0")" --argjson wmem_max "$(num_or_null "$wmem0")" \
    --argjson leg_args "$legs_json" --arg note "$note" \
    --arg started_utc "$started" --arg ended_utc "$ended" \
    --arg label "$label" --arg bot_label_note "$BOT_LABEL_NOTE" \
    '{host_label: $host_label, mode: $mode, image: $image, docker_version: $docker_version,
      tool_versions: $tool_versions, sysctl_raise: $sysctl_raise,
      kernel: $kernel, nproc: $nproc, server_set: $server_set, gen_set: $gen_set, gogc: $gogc,
      client_ref: $client_ref, server_sha: $server_sha, server_dirty: $server_dirty,
      rmem_max: $rmem_max, wmem_max: $wmem_max, leg_args: $leg_args, note: $note,
      started_utc: $started_utc, ended_utc: $ended_utc,
      relative_only: true, label: $label, bot_label_note: $bot_label_note}' > "$out_dir/host.json"

  local sum="$out_dir/summary.md" v k dir f
  {
    echo "# Bench host run: $host_label"
    echo
    echo "$label"
    echo
    echo "| fact | value |"
    echo "|---|---|"
    jq -r '. as $h | ["host_label","mode","image","docker_version","tool_versions","sysctl_raise","kernel","nproc","server_set","gen_set","gogc","client_ref","server_sha","server_dirty","rmem_max","wmem_max","leg_args","note","started_utc","ended_utc"][]
      | "| \(.) | \($h[.] | if type == "array" then join(" ") elif . == null then "-" else tostring end | gsub("\\|"; "\\|") | gsub("\n"; " ")) |"' "$out_dir/host.json"
    echo
    echo "$BOT_LABEL_NOTE"
    for v in $variants; do
      dir="$out_dir/gogc-$v"
      echo
      echo "## GOGC $v"
      k=1
      while [ "$k" -le "$repeats" ]; do
        f="$dir/results/$k-$scenario/results.md"
        echo
        echo "### Leg $k results"
        echo
        if [ -f "$f" ]; then cat "$f"; else echo "_no results.md (the leg did not finish)_"; fi
        k=$((k + 1))
      done
      f="$dir/aggregate/aggregate.md"
      echo
      echo "### Aggregate"
      echo
      if [ -f "$f" ]; then cat "$f"; else echo "_no aggregate.md_"; fi
    done
  } > "$sum"
  echo "== summary =="
  cat "$sum"
}

# The same backstop as bench.yml's "Diagnostics (failed leg)" step, with plain headers.
leg_diagnostics() { # LEG_DIR
  local d="$1" f srv
  [ "$dry_run" -eq 0 ] || return 0
  for f in "$d"/loadbot-*.log "$d"/calibrate.log "$d"/reflect.log; do
    [ -e "$f" ] || continue
    echo "== $f (last 50 lines) =="
    tail -n 50 "$f" 2>&1 || true
  done
  srv="$(ls "$d"/server-*.log.gz 2>/dev/null | sort -V | tail -n 1 || true)"
  echo "== ${srv:-server log} (last 200 lines) =="
  if [ -n "$srv" ]; then zcat "$srv" 2>&1 | tail -n 200 || true; else echo "no server log kept"; fi
  echo "== $d/manifest.json =="
  cat "$d/manifest.json" 2>&1 || true
}

pipeline_native() {
  local here repo work bin
  here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  repo="$(cd "$here/../.." && pwd)"

  if [ "$dry_run" -eq 0 ]; then
    local t
    for t in jq unzip curl git taskset go sha256sum; do
      have_tool "$t" || die "needs $t (native mode needs jq, unzip, curl, git, taskset and Go; see --help)"
    done
  fi

  # The work dir (protoc, binaries, the client clone, about 1 GB) lives outside --out so it is never
  # packaged and --out stays empty-checkable on a rerun. BENCH_HOST_WORK overrides it and is kept.
  if [ -n "${BENCH_HOST_WORK:-}" ]; then
    work="$BENCH_HOST_WORK"
  elif [ "$dry_run" -eq 1 ]; then
    work="${TMPDIR:-/tmp}/vcs-bench-host.work"
  else
    work="$(mktemp -d "${TMPDIR:-/tmp}/vcs-bench-host.XXXXXX")"
    WORK_TO_REMOVE="$work"
    trap 'rm -rf "$WORK_TO_REMOVE"' EXIT
  fi
  bin="$work/bin"
  [ "$dry_run" -eq 1 ] || mkdir -p "$out_dir"

  local started dirty rmem0 wmem0
  started="$(now_utc)"
  dirty="${BENCH_HOST_SERVER_DIRTY:-${BENCH_HOST_FAKE_DIRTY:-}}"
  if [ -z "$dirty" ]; then
    if ! git -C "$repo" rev-parse --git-dir >/dev/null 2>&1; then dirty=null
    elif [ -n "$(git -C "$repo" status --porcelain 2>/dev/null)" ]; then dirty=true
    else dirty=false; fi
  fi
  case "$dirty" in true|false|null) ;; *) die "BENCH_HOST_SERVER_DIRTY must be true, false or null" ;; esac
  rmem0="${BENCH_HOST_FAKE_RMEM:-$(cat /proc/sys/net/core/rmem_max 2>/dev/null || true)}"
  wmem0="${BENCH_HOST_FAKE_WMEM:-$(cat /proc/sys/net/core/wmem_max 2>/dev/null || true)}"
  local go_version="${BENCH_HOST_FAKE_GO_VERSION:-dry-run}" client_go_version="${BENCH_HOST_FAKE_CLIENT_GO_VERSION:-dry-run}"

  local nsrv ngen label topology smt extra=""
  nsrv="${#server_cpus[@]}"; ngen="${#gen_cpus[@]}"
  if [ "${#leg_args[@]}" -gt 0 ]; then extra=", non-default leg args"; fi
  label="$host_label, server cpus $server_set, generator cpus $gen_set$extra, loopback, relative only. Not a capacity figure."
  # smt comes from the probe's own reading on a real run; dry-run has no probe.
  smt="${BENCH_HOST_FAKE_SMT:-false}"

  local sysctl_flag="--sysctl=true"
  if [ -n "${BENCH_HOST_IN_CONTAINER:-}" ]; then sysctl_flag="--sysctl=false"; fi

  # BENCH_HOST_SERVER_SHA / BENCH_HOST_SERVER_DIRTY carry the host commit into a container (no .git there); the FAKE_ names are kept as aliases.
  local server_sha="${BENCH_HOST_SERVER_SHA:-${BENCH_HOST_FAKE_SHA:-}}"
  if [ -z "$server_sha" ]; then
    server_sha="$(git -C "$repo" rev-parse HEAD 2>/dev/null || echo unknown)"
  fi
  local client_sha="$client_ref"

  local variants
  case "$gogc" in both) variants="off default" ;; *) variants="$gogc" ;; esac

  local failed=0

  echo "== build =="
  cd "$repo"
  run mkdir -p "$bin"
  run bash "$here/assert-go-toolchain.sh"
  local protoc_zip="$work/protoc.zip"
  run curl -fsSL --proto '=https' --tlsv1.2 -o "$protoc_zip" \
    "https://github.com/protocolbuffers/protobuf/releases/download/v$PROTOC_VERSION/protoc-$PROTOC_VERSION-linux-x86_64.zip"
  sha_check "$protoc_zip" "$PROTOC_SHA256"
  run unzip -o "$protoc_zip" -d "$work/protoc" bin/protoc 'include/*'
  run rm "$protoc_zip"
  export PATH="$work/protoc/bin:$bin:$PATH"
  if [ "$dry_run" -eq 0 ]; then
    local v; v="$(protoc --version)"
    echo "$v"
    case "$v" in *" $PROTOC_VERSION"|*" $PROTOC_VERSION.0") ;; *) die "expected protoc $PROTOC_VERSION, got: $v" ;; esac
  fi
  local pb_ver
  pb_ver="$(awk '$1=="google.golang.org/protobuf"{print $2; exit}' go.mod)"
  [ -n "$pb_ver" ] || die "google.golang.org/protobuf not found in go.mod"
  run env "GOBIN=$bin" go install "google.golang.org/protobuf/cmd/protoc-gen-go@$pb_ver"
  run env "GOBIN=$bin" go install "$PROTOC_GEN_GO_GRPC"
  run mkdir -p srspb voicecontrolpb vcsauthpb
  run protoc --go_out=./srspb --go_opt=paths=source_relative --go-grpc_out=./srspb --go-grpc_opt=paths=source_relative srs.proto
  run protoc --go_out=./voicecontrolpb --go_opt=paths=source_relative --go-grpc_out=./voicecontrolpb --go-grpc_opt=paths=source_relative control.proto
  run protoc --go_out=./vcsauthpb --go_opt=paths=source_relative --go-grpc_out=./vcsauthpb --go-grpc_opt=paths=source_relative vcs-auth-plugin.proto
  run go run github.com/99designs/gqlgen generate
  run env CGO_ENABLED=0 go build -tags headless -trimpath -o "$bin/vcs-server" .
  run env CGO_ENABLED=0 go build -trimpath -o "$bin/vcs-benchkit" ./cmd/vcs-benchkit
  # The client is public, so no token. Its toolchain is its own: GOTOOLCHAIN=auto reads its go.mod.
  run git clone "$CLIENT_URL" "$work/client"
  run git -C "$work/client" checkout "$client_ref"
  run_in "$work/client" env GOTOOLCHAIN=auto bash "$here/assert-go-toolchain.sh" "$work/client/go.mod"
  run_in "$work/client" env GOTOOLCHAIN=auto go run "$BUF" generate
  run_in "$work/client" env CGO_ENABLED=0 GOTOOLCHAIN=auto go build -tags purego -trimpath -o "$bin/vcs-loadbot" ./cmd/vcs-loadbot
  if [ "$dry_run" -eq 0 ]; then
    client_sha="$(git -C "$work/client" rev-parse HEAD)"
    go_version="$(go env GOVERSION)"
    client_go_version="$(cd "$work/client" && GOTOOLCHAIN=auto go env GOVERSION)"
  fi
  write_build_json

  echo "== probe =="
  run bash "$here/bench-probe_test.sh"
  run_to "$out_dir/env-probe.json" bash "$here/bench-probe.sh"
  if [ "$dry_run" -eq 0 ]; then
    smt="$(jq -r .smt "$out_dir/env-probe.json")"
  fi
  topology="$host_label: server $server_set ($nsrv cpu), generator $gen_set ($ngen cpu), smt $smt"

  local have_predict=0 predict_file="$out_dir/micro/predict.json"
  echo "== micro =="
  run mkdir -p "$out_dir/micro"
  if [ "$micro" = "run" ]; then
    have_predict=1
    run go test -c -tags headless -o "$bin/voice.test" ./voice/
    # GOMAXPROCS=$nsrv is deliberate: bench.yml fixes it at 2. With a 1-cpu --server-set the micro numbers are therefore NOT comparable with Actions micro results.
    run_tee "$out_dir/micro/micro.txt" taskset -c "$server_set" env "GOMAXPROCS=$nsrv" "$bin/voice.test" \
      -test.run '^$' -test.bench "$MICRO_RE" -test.benchmem -test.count=10
    run_tee "$out_dir/micro/micro-benchstat.txt" go run "$BENCHSTAT" "$out_dir/micro/micro.txt"
    run_to "$predict_file" "$bin/vcs-benchkit" predict --micro "$out_dir/micro/micro.txt" --logger bus \
      --csv "$out_dir/micro/micro.csv" --label "$label"
  elif [ -n "$predict" ]; then
    have_predict=1
    run cp "$predict" "$predict_file"
  fi

  echo "== legs =="
  local k v dir cmd
  k=1
  while [ "$k" -le "$repeats" ]; do
    for v in $variants; do
      dir="$out_dir/gogc-$v"
      run mkdir -p "$dir/results"
      cmd=("$bin/vcs-benchkit" leg
        --server-bin "$bin/vcs-server"
        --loadbot-bin "$bin/vcs-loadbot"
        --scenario "$scenario"
        --arms "$arms"
        --seed "$k"
        --out "$dir/results/$k-$scenario"
        --probe "$out_dir/env-probe.json")
      if [ "$have_predict" -eq 1 ]; then cmd+=(--predict "$predict_file"); fi
      cmd+=(--server-sha "$server_sha"
        --client-sha "$client_sha"
        --q6 "host run: server $server_set, generator $gen_set (user, 2026-10-29)"
        --server-set "$server_set"
        --gen-set "$gen_set"
        --topology "$topology"
        --label "$label"
        --generator-gc "$v"
        --budget "$budget"
        "$sysctl_flag")
      if [ "${#leg_args[@]}" -gt 0 ]; then cmd+=("${leg_args[@]}"); fi
      echo "== leg $k, GOGC $v =="
      if [ "$dry_run" -eq 1 ] && [ -n "${BENCH_HOST_FAKE_RESULTS:-}" ]; then
        mkdir -p "$dir/results/$k-$scenario"
        printf 'FAKE results GOGC %s leg %s\n' "$v" "$k" > "$dir/results/$k-$scenario/results.md"
      fi
      if ! run_tee "$dir/leg-$k.log" "${cmd[@]}"; then
        echo "bench-host: leg $k (GOGC $v) failed; continuing" >&2
        failed=1
        leg_diagnostics "$dir/results/$k-$scenario"
      fi
    done
    k=$((k + 1))
  done

  echo "== aggregate =="
  for v in $variants; do
    dir="$out_dir/gogc-$v"
    if [ "$dry_run" -eq 0 ]; then
      local legs_done
      legs_done="$(find "$dir/results" -name knees.csv 2>/dev/null | wc -l | tr -d ' ')"
      if [ "$legs_done" -ne "$repeats" ]; then
        echo "PARTIAL: GOGC $v: $legs_done of $repeats legs aggregated; the statistics cover the surviving legs only"
      fi
    fi
    if [ "$dry_run" -eq 1 ] && [ -n "${BENCH_HOST_FAKE_RESULTS:-}" ]; then
      mkdir -p "$dir/aggregate"
      printf 'FAKE aggregate GOGC %s\n' "$v" > "$dir/aggregate/aggregate.md"
    fi
    if ! run "$bin/vcs-benchkit" aggregate --in "$dir/results" --out "$dir/aggregate" --label "$label"; then
      echo "bench-host: aggregate (GOGC $v) failed" >&2
      failed=1
    fi
  done

  write_records
  if [ "$failed" -ne 0 ]; then
    echo "bench-host: finished with failures (see above)" >&2
    exit 1
  fi
}

# The pinned image: the exact tag of the repo Dockerfile's builder stage, by index digest (resolved
# 2026-10-29). bench-host_test.sh fails when the tag drifts from the Dockerfile.
IMAGE='golang:1.27.3@sha256:2920bb124f1a2e12a1d39f0cab756ae0068441ee1da7655dec7f5e374944095b'

pipeline_docker() {
  local here repo ts out_abs host_out sha dirty dver a
  here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  repo="$(cd "$here/../.." && pwd)"

  if [ "$dry_run" -eq 0 ]; then
    have_tool docker || die "docker mode needs docker on PATH (see --help; native mode runs without it)"
    have_tool git || die "docker mode needs git (it archives HEAD for the container)"
  fi

  # The commit under test comes from the host: a worktree's .git file points at a host path that does not exist in the container.
  sha="${BENCH_HOST_SERVER_SHA:-${BENCH_HOST_FAKE_SHA:-}}"
  [ -n "$sha" ] || sha="$(git -C "$repo" rev-parse HEAD 2>/dev/null || echo unknown)"
  dirty="${BENCH_HOST_SERVER_DIRTY:-${BENCH_HOST_FAKE_DIRTY:-}}"
  if [ -z "$dirty" ]; then
    if ! git -C "$repo" rev-parse --git-dir >/dev/null 2>&1; then dirty=null
    elif [ -n "$(git -C "$repo" status --porcelain 2>/dev/null)" ]; then dirty=true
    else dirty=false; fi
  fi
  case "$dirty" in true|false|null) ;; *) die "BENCH_HOST_SERVER_DIRTY must be true, false or null" ;; esac
  if [ "$dirty" = "true" ]; then
    echo "bench-host: WARNING: uncommitted changes are not benched (the container gets git archive HEAD, commit $sha)" >&2
  fi

  # Only now: MSYS_NO_PATHCONV would stop git.exe from understanding the /d/... paths used above.
  # Git Bash rewrites /out and /work/src in docker arguments unless told not to, and wants Windows paths for -v.
  local uname_s="${BENCH_HOST_FAKE_UNAME:-$(uname -s 2>/dev/null || echo unknown)}"
  mkdir -p "$out_dir"
  out_abs="$(cd "$out_dir" && pwd)"
  host_out="$out_abs"
  case "$uname_s" in
    MINGW*|MSYS*)
      export MSYS_NO_PATHCONV=1
      echo "bench-host: Git Bash detected, MSYS_NO_PATHCONV=1 and Windows paths for docker" >&2
      host_out="${BENCH_HOST_FAKE_PWDW:-$(cd "$out_dir" && pwd -W)}" ;;
  esac

  dver="${BENCH_HOST_DOCKER_VERSION:-}"
  if [ -z "$dver" ]; then
    if [ "$dry_run" -eq 1 ]; then dver="dry-run"
    else dver="$(docker version --format '{{.Server.Version}}' 2>/dev/null || true)"; fi
  fi

  if [ -n "$predict" ]; then cp "$predict" "$out_abs/.predict.json"; fi

  if [ "$privileged_sysctl" -eq 1 ]; then
    echo "bench-host: WARNING: --privileged-sysctl runs the container privileged and changes the host's net.core.rmem_max/wmem_max until reboot; the script restores the old values on exit" >&2
  fi

  # The container command, one line. jq and unzip come from apt (the image lacks them) and their versions are recorded.
  local c="set -euo pipefail; export DEBIAN_FRONTEND=noninteractive; apt-get update -qq >/dev/null; apt-get install -y -qq --no-install-recommends jq unzip >/dev/null"
  c="$c; BENCH_HOST_TOOL_VERSIONS=\"jq \$(jq --version | head -n 1), \$(unzip -v | head -n 1 | cut -d, -f1)\"; export BENCH_HOST_TOOL_VERSIONS"
  c="$c; mkdir -p /work/src; tar -x -C /work/src -f /out/.src.tar; rm -f /out/.src.tar"
  if [ "$privileged_sysctl" -eq 1 ]; then
    c="$c; old_r=\"\$(cat /proc/sys/net/core/rmem_max 2>/dev/null || true)\"; old_w=\"\$(cat /proc/sys/net/core/wmem_max 2>/dev/null || true)\""
    c="$c; restore_sysctl() { [ -z \"\$old_r\" ] || echo \"\$old_r\" > /proc/sys/net/core/rmem_max 2>/dev/null || true; [ -z \"\$old_w\" ] || echo \"\$old_w\" > /proc/sys/net/core/wmem_max 2>/dev/null || true; }; trap restore_sysctl EXIT"
    c="$c; if sysctl_err=\"\$( ( echo 8388608 > /proc/sys/net/core/rmem_max && echo 8388608 > /proc/sys/net/core/wmem_max ) 2>&1 )\"; then BENCH_HOST_SYSCTL_RAISE=raised; else BENCH_HOST_SYSCTL_RAISE=\"refused: \$(printf '%s' \"\$sysctl_err\" | tr '\n' ' ')\"; fi; export BENCH_HOST_SYSCTL_RAISE"
  fi
  c="$c; cd /work/src; bash .github/scripts/bench-host.sh native"
  for a in ${fwd[@]+"${fwd[@]}"}; do c="$c $(q1 "$a")"; done
  if [ -n "$predict" ]; then c="$c --predict /out/.predict.json"; fi
  c="$c --out /out"
  # no exec: with --privileged-sysctl the EXIT trap has to run after the native run

  ts="$(date -u +%Y%m%d%H%M%S)"
  local dargs=(docker run --rm --init --name "vcs-bench-host-$ts")
  if [ "$privileged_sysctl" -eq 1 ]; then dargs+=(--privileged --network host); fi
  dargs+=(-e BENCH_HOST_IN_CONTAINER=1 -e "BENCH_HOST_SERVER_SHA=$sha" -e "BENCH_HOST_SERVER_DIRTY=$dirty"
    -e "BENCH_HOST_IMAGE=$IMAGE" -e "BENCH_HOST_DOCKER_VERSION=$dver"
    -v "$host_out:/out" -v vcs-bench-gomod:/go/pkg/mod -v vcs-bench-gocache:/root/.cache/go-build
    "$IMAGE" bash -c "$c")

  # core.autocrlf=false: git archive converts to the working-tree form, and CRLF scripts do not run in the container
  run_in "$repo" git -c core.autocrlf=false archive --format=tar HEAD -o "$host_out/.src.tar"
  local rc=0
  run "${dargs[@]}" || rc=$?
  run rm -f "$host_out/.src.tar"
  if [ "$rc" -ne 0 ]; then
    echo "bench-host: the container exited with status $rc" >&2
    exit "$rc"
  fi
}

case "$mode" in
  native) pipeline_native ;;
  docker) pipeline_docker ;;
esac
