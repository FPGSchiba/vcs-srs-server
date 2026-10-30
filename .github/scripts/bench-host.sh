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
  --arms LIST           comma list of S, Seq, D2. Default S. Seq and D2 need a 2-cpu --server-set
  --scenario LIST       comma list of one-big-net, org-op, or "all" for both. Default one-big-net
  --repeats K           default 1. 1 to 5 legs per GOGC variant, seeds 1..K, run serially
  --gogc off|default|both
                        default default. default = Go defaults, off = GOGC=off GOMEMLIMIT=1GiB
                        (R2-9), both = alternate off/default for each seed
  --join-floor F        the bot join-rate floor in joins/s (a non-negative number), forwarded to
                        every leg as --join-rate-floor. Default: the kit's own default
  --client-ref SHA      default the pinned bench.yml ref. A full 40-hex SHA
  --out DIR             required. Results directory; must be empty or absent
  --micro run|skip      default run. skip runs the legs without a prediction unless --predict FILE
                        or --leg-arg --predicted-knee=N is given (the prediction is context only)
  --predict FILE        a predict.json from an earlier run (with --micro skip)
  --budget D            default 100m. The kit's leg budget
  --leg-arg ARG         repeatable pass-through to vcs-benchkit leg (smoke runs only)
  --note TEXT           free text recorded in host.json
  --privileged-sysctl   docker mode only, opt-in. Runs the container --privileged with
                        --network host to raise rmem_max/wmem_max; prints a warning
  --dry-run             print every command instead of running it
  --help                this text

CPU sets per arm (the kit pins each arm's processes inside --server-set):
  S    standalone server on the first server cpu
  Seq  standalone server on both server cpus
  D2   control and global on both, nodeA on the first, nodeB on the second
For Seq and D2 to measure two cores, pick a 2-cpu --server-set on two different physical cores
(check /sys/devices/system/cpu/cpu<N>/topology/thread_siblings_list; the script warns when the two
are hyperthreads of one core and records server_pair in host.json). Leave their SMT siblings out of
--gen-set and otherwise idle, and give the generator every remaining cpu. Example for a 16-vCPU,
8-core instance whose siblings are (0,8) (1,9) ...: --server-set 0,1 --gen-set 2-7,10-15
Full AWS matrix (all arms, both scenarios), Go-default GC, three seeds:
  --arms S,Seq,D2 --scenario all --repeats 3 --gogc default

EC2 (no extra tooling; the instance is yours to provide):
  git archive HEAD | ssh <host> 'mkdir -p vcs && tar -x -C vcs'
  ssh <host> 'cd vcs && bash .github/scripts/bench-host.sh native --host-label aws-<type> --server-set 0 --gen-set 4-11 --out ~/bench'
  scp -r <host>:bench .
Native mode needs an x86_64 Linux host (the protoc pin is linux-x86_64; pick an x86 instance type,
not Graviton), Go 1.21 or newer with GOTOOLCHAIN=auto, plus jq, unzip, curl, git, taskset and ss (iproute2).
USAGE
}

host_label=""; server_set=""; gen_set=""; out_dir=""
have_host_label=0; have_server_set=0; have_gen_set=0; have_out=0
arms="S"; scenario="one-big-net"; repeats="1"; gogc="default"; join_floor=""; have_join_floor=0
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
    --host-label|--server-set|--gen-set|--arms|--scenario|--repeats|--gogc|--join-floor|--client-ref|--out|--micro|--predict|--budget|--leg-arg|--note)
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
        --join-floor) join_floor="$val"; have_join_floor=1 ;;
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
# list_canon FLAG LIST ALLOWED...: validates a comma list (no empty or duplicate element, only ALLOWED
# names) and sets CANON to the comma list in ALLOWED order, so the same set always prints the same.
CANON=""
list_canon() {
  local flag="$1" list="$2" a el seen out="" found
  shift 2
  [ -n "$list" ] || die "$flag: empty list (allowed: $*)"
  case "$list" in ,*|*,|*,,*) die "$flag: '$list' has an empty element (allowed: $*)" ;; esac
  local parts; IFS=',' read -ra parts <<< "$list"
  for el in "${parts[@]}"; do
    found=0; for a in "$@"; do if [ "$el" = "$a" ]; then found=1; fi; done
    [ "$found" -eq 1 ] || die "$flag: unknown name '$el' in '$list' (allowed: $*)"
  done
  for a in "$@"; do
    seen=0
    for el in "${parts[@]}"; do if [ "$el" = "$a" ]; then seen=$((seen + 1)); fi; done
    [ "$seen" -le 1 ] || die "$flag: '$a' is listed twice in '$list'"
    if [ "$seen" -eq 1 ]; then out="${out:+$out,}$a"; fi
  done
  CANON="$out"
}
list_canon --arms "$arms" S Seq D2; arms="$CANON"
if [ "$scenario" = "all" ]; then
  scenario="one-big-net,org-op"
elif [[ ",$scenario," == *,all,* ]]; then
  die "--scenario: all cannot be combined with other names (got '$scenario')"
fi
list_canon --scenario "$scenario" one-big-net org-op; scenario="$CANON"
[[ "$repeats" =~ ^[1-5]$ ]] || die "--repeats must be 1 to 5 (got '$repeats')"
case "$gogc" in off|default|both) ;; *) die "--gogc must be off, default or both (got '$gogc')" ;; esac
join_floor_re='^[0-9]+(\.[0-9]+)?$'
if [ "$have_join_floor" -eq 1 ] && ! [[ "$join_floor" =~ $join_floor_re ]]; then die "--join-floor must be a non-negative number (got '$join_floor')"; fi
[[ "$client_ref" =~ ^[0-9a-f]{40}$ ]] || die "--client-ref must be a full 40-hex SHA"
case "$micro" in run|skip) ;; *) die "--micro must be run or skip (got '$micro')" ;; esac
budget_re='^([0-9]+(ns|us|ms|s|m|h))+$'
[[ "$budget" =~ $budget_re ]] || die "--budget must be a Go duration such as 100m or 1h30m (got '$budget')"
if [ -n "$predict" ] && [ ! -f "$predict" ]; then die "--predict $predict is not a file"; fi
if [ "$privileged_sysctl" -eq 1 ] && [ "$mode" != "docker" ]; then
  die "--privileged-sysctl is for docker mode only"
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
need2=""
case ",$arms," in *,Seq,*) need2="Seq" ;; esac
case ",$arms," in *,D2,*) need2="${need2:+$need2,}D2" ;; esac
if [ -n "$need2" ] && [ "${#server_cpus[@]}" -ne 2 ]; then die "--arms $need2 need a 2-cpu --server-set (got '$server_set')"; fi

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

# Server pair: with two server cpus, are they two cores or two hyperthreads of one? Seq and D2 only
# mean "two cores" in the first case, so a shared core is a warning (an S-only run uses one cpu and
# stays quiet). "unknown" when the topology is unreadable or, in docker mode, not looked at here.
if [ "${#server_cpus[@]}" -ne 2 ]; then
  server_pair="n/a (1 cpu)"
else
  server_pair="unknown"
  if [ "${#sib_scan[@]}" -gt 0 ]; then
    sib="$sysfs_root/sys/devices/system/cpu/cpu${server_cpus[0]}/topology/thread_siblings_list"
    if [ -r "$sib" ]; then
      sl="$(tr -d "[:space:]" < "$sib")"
      if ( cpu_expand "cpu${server_cpus[0]} siblings" "$sl" ) >/dev/null 2>&1; then
        cpu_expand "cpu${server_cpus[0]} siblings" "$sl"
        server_pair="2 cores"
        for t in "${CPUS[@]}"; do
          if [ "$t" -eq "${server_cpus[1]}" ]; then server_pair="2 hyperthreads of 1 core"; fi
        done
      fi
    fi
  fi
  if [ "$server_pair" = "2 hyperthreads of 1 core" ] && [ "$arms" != "S" ]; then
    echo "bench-host: WARNING: server cpus ${server_cpus[0]} and ${server_cpus[1]} are hyperthreads of one core: Seq and D2 measure SMT, not a second core" >&2
  fi
fi

if [ -e "$out_dir" ]; then
  [ -d "$out_dir" ] || die "--out $out_dir is not a directory"
  if [ -n "${BENCH_HOST_IN_CONTAINER:-}" ]; then
    # BENCH_HOST_IN_CONTAINER is internal: the docker launcher sets it, users must not (it only loosens this check)
    # the docker launcher stages the source archive and a --predict file in --out; nothing else may be there
    [ -z "$(ls -A "$out_dir" | grep -v -x -e .src.tar -e .predict.json || true)" ] || die "--out $out_dir must be empty"
  else
    [ -z "$(ls -A "$out_dir")" ] || die "--out $out_dir must be empty"
  fi
fi

# The protoc pin (and its sha256) is the linux-x86_64 build, so native mode refuses any other arch
# before building. BENCH_HOST_FAKE_ARCH is the test hook; a dry-run without it skips the check.
host_arch="${BENCH_HOST_FAKE_ARCH:-}"
if [ -z "$host_arch" ] && [ "$mode" = "native" ] && [ "$dry_run" -eq 0 ]; then host_arch="$(uname -m)"; fi
if [ "$mode" = "native" ] && [ -n "$host_arch" ] && [ "$host_arch" != "x86_64" ]; then
  die "native mode needs an x86_64 host (got $host_arch): the protoc pin is linux-x86_64"
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
  if [ "$dry_run" -eq 1 ]; then echo "+ $(qargs "$@") 2>&1 | tee $(q1 "$f")"; else "$@" 2>&1 | tee "$f"; fi
}
run_tee_out() { # FILE argv...: stdout only to the console and to FILE (bench.yml's "| tee"); stderr to the console
  local f="$1"; shift
  if [ "$dry_run" -eq 1 ]; then echo "+ $(qargs "$@") | tee $(q1 "$f")"; else "$@" | tee "$f"; fi
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
  local rec_mode="$mode"   # the container runs the native pipeline; record how it was launched
  if [ -n "${BENCH_HOST_IN_CONTAINER:-}" ]; then rec_mode=docker; fi
  jq -n --arg host_label "$host_label" --arg mode "$rec_mode" \
    --argjson image "$(str_or_null "${BENCH_HOST_IMAGE:-}")" \
    --argjson docker_version "$(str_or_null "${BENCH_HOST_DOCKER_VERSION:-}")" \
    --argjson tool_versions "$(str_or_null "${BENCH_HOST_TOOL_VERSIONS:-}")" \
    --argjson sysctl_raise "$(str_or_null "${BENCH_HOST_SYSCTL_RAISE:-}")" \
    --arg kernel "$kernel" --argjson nproc "$(num_or_null "$nproc_v")" \
    --arg server_set "$server_set" --arg gen_set "$gen_set" --arg gogc "$gogc" \
    --arg arms "$arms" --arg scenarios "$scenario" --arg server_pair "$server_pair" \
    --argjson arm_pins "$arm_pins_json" \
    --argjson join_floor "$(str_or_null "$join_floor")" \
    --arg client_ref "$client_ref" --arg server_sha "$server_sha" --argjson server_dirty "$dirty" \
    --argjson rmem_max "$(num_or_null "$rmem0")" --argjson wmem_max "$(num_or_null "$wmem0")" \
    --argjson leg_args "$legs_json" --arg note "$note" \
    --arg started_utc "$started" --arg ended_utc "$ended" \
    --arg label "$label" --arg bot_label_note "$BOT_LABEL_NOTE" \
    '{host_label: $host_label, mode: $mode, image: $image, docker_version: $docker_version,
      tool_versions: $tool_versions, sysctl_raise: $sysctl_raise,
      kernel: $kernel, nproc: $nproc, server_set: $server_set, gen_set: $gen_set, gogc: $gogc,
      arms: ($arms | split(",")), scenarios: ($scenarios | split(",")), server_pair: $server_pair,
      arm_pins: $arm_pins,
      join_floor: $join_floor,
      client_ref: $client_ref, server_sha: $server_sha, server_dirty: $server_dirty,
      rmem_max: $rmem_max, wmem_max: $wmem_max, leg_args: $leg_args, note: $note,
      started_utc: $started_utc, ended_utc: $ended_utc,
      relative_only: true, label: $label, bot_label_note: $bot_label_note}' > "$out_dir/host.json"

  local sum="$out_dir/summary.md" v k dir f sc scenarios_sp="${scenario//,/ }"
  {
    echo "# Bench host run: $host_label"
    echo
    echo "$label"
    echo
    echo "| fact | value |"
    echo "|---|---|"
    jq -r '. as $h | ["host_label","mode","image","docker_version","tool_versions","sysctl_raise","kernel","nproc","server_set","gen_set","arms","scenarios","server_pair","arm_pins","gogc","join_floor","client_ref","server_sha","server_dirty","rmem_max","wmem_max","leg_args","note","started_utc","ended_utc"][]
      | "| \(.) | \($h[.] | if type == "array" then join(" ") elif type == "object" then to_entries | map("\(.key): \(.value)") | join("; ") elif . == null then "-" else tostring end | gsub("\\|"; "\\|") | gsub("\n"; " ")) |"' "$out_dir/host.json"
    echo
    echo "$BOT_LABEL_NOTE"
    for v in $variants; do
      dir="$out_dir/gogc-$v"
      echo
      echo "## GOGC $v"
      k=1
      while [ "$k" -le "$repeats" ]; do
        for sc in ${scenarios_sp}; do
          f="$dir/results/$k-$sc/results.md"
          echo
          echo "### Leg $k, $sc results"
          echo
          if [ -f "$f" ]; then cat "$f"; else echo "_no results.md (the leg did not finish)_"; fi
        done
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
    for t in jq unzip ss curl git taskset go sha256sum; do
      have_tool "$t" || die "needs $t (native mode needs jq, unzip, curl, git, taskset, ss and Go; see --help)"
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
  # The per-arm CPU plan (design I-5): where each arm's processes run inside --server-set.
  local s0="${server_cpus[0]}" s1="${server_cpus[1]:-}" arm_pins_json='{}' a pin
  echo "== arm plan (server set $server_set, pair: $server_pair) =="
  for a in ${arms//,/ }; do
    case "$a" in
      S) pin="standalone cpu $s0" ;;
      Seq) pin="standalone cpus $s0,$s1" ;;
      D2) pin="control $s0,$s1 / nodeA $s0 / nodeB $s1 / global $s0,$s1" ;;
    esac
    echo "$a: $pin"
    arm_pins_json="$(printf '%s' "$arm_pins_json" | jq -c --arg a "$a" --arg p "$pin" '. + {($a): $p}')"
  done
  started="$(now_utc)"
  dirty="${BENCH_HOST_SERVER_DIRTY:-${BENCH_HOST_FAKE_DIRTY:-}}"
  if [ -z "$dirty" ]; then
    if ! (cd "$repo" && git rev-parse --git-dir) >/dev/null 2>&1; then dirty=null
    elif [ -n "$(cd "$repo" && git status --porcelain 2>/dev/null)" ]; then dirty=true
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
    server_sha="$(cd "$repo" && git rev-parse HEAD 2>/dev/null || echo unknown)"
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
  pb_ver="$(awk '$1=="google.golang.org/protobuf"{print $2; exit}' go.mod | tr -d '\r')"
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
  if [ "$nsrv" -eq 2 ]; then topology="$topology, server pair $server_pair"; fi

  local have_predict=0 predict_file="$out_dir/micro/predict.json"
  echo "== micro =="
  run mkdir -p "$out_dir/micro"
  if [ "$micro" = "run" ]; then
    have_predict=1
    run go test -c -tags headless -o "$bin/voice.test" ./voice/
    # GOMAXPROCS=$nsrv is deliberate: bench.yml fixes it at 2. With a 1-cpu --server-set the micro numbers are therefore NOT comparable with Actions micro results.
    run_tee_out "$out_dir/micro/micro.txt" taskset -c "$server_set" env "GOMAXPROCS=$nsrv" "$bin/voice.test" \
      -test.run '^$' -test.bench "$MICRO_RE" -test.benchmem -test.count=10
    run_tee_out "$out_dir/micro/micro-benchstat.txt" go run "$BENCHSTAT" "$out_dir/micro/micro.txt"
    run_to "$predict_file" "$bin/vcs-benchkit" predict --micro "$out_dir/micro/micro.txt" --logger bus \
      --csv "$out_dir/micro/micro.csv" --label "$label"
  elif [ -n "$predict" ]; then
    have_predict=1
    run cp "$predict" "$predict_file"
  fi

  echo "== legs =="
  local k v dir cmd sc
  k=1
  while [ "$k" -le "$repeats" ]; do
    for v in $variants; do
     for sc in ${scenario//,/ }; do
      dir="$out_dir/gogc-$v"
      run mkdir -p "$dir/results"
      cmd=("$bin/vcs-benchkit" leg
        --server-bin "$bin/vcs-server"
        --loadbot-bin "$bin/vcs-loadbot"
        --scenario "$sc"
        --arms "$arms"
        --seed "$k"
        --out "$dir/results/$k-$sc"
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
      if [ "$have_join_floor" -eq 1 ]; then cmd+=(--join-rate-floor "$join_floor"); fi
      if [ "${#leg_args[@]}" -gt 0 ]; then cmd+=("${leg_args[@]}"); fi
      echo "== leg $k, $sc, GOGC $v =="
      if [ "$dry_run" -eq 1 ] && [ -n "${BENCH_HOST_FAKE_RESULTS:-}" ]; then
        mkdir -p "$dir/results/$k-$sc"
        printf 'FAKE results GOGC %s leg %s %s\n' "$v" "$k" "$sc" > "$dir/results/$k-$sc/results.md"
      fi
      if ! run_tee "$dir/leg-$k-$sc.log" "${cmd[@]}"; then
        echo "bench-host: leg $k ($sc, GOGC $v) failed; continuing" >&2
        failed=1
        leg_diagnostics "$dir/results/$k-$sc"
      fi
     done
    done
    k=$((k + 1))
  done

  echo "== aggregate =="
  for v in $variants; do
    dir="$out_dir/gogc-$v"
    if [ "$dry_run" -eq 0 ]; then
      local legs_done
      local legs_want=$((repeats * (1 + $(printf '%s' "$scenario" | tr -cd ',' | wc -c | tr -d ' '))))
      legs_done="$(find "$dir/results" -name knees.csv 2>/dev/null | wc -l | tr -d ' ')"
      if [ "$legs_done" -ne "$legs_want" ]; then
        echo "PARTIAL: GOGC $v: $legs_done of $legs_want legs aggregated; the statistics cover the surviving legs only"
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
  [ -n "$sha" ] || sha="$(cd "$repo" && git rev-parse HEAD 2>/dev/null || echo unknown)"
  dirty="${BENCH_HOST_SERVER_DIRTY:-${BENCH_HOST_FAKE_DIRTY:-}}"
  if [ -z "$dirty" ]; then
    if ! (cd "$repo" && git rev-parse --git-dir) >/dev/null 2>&1; then dirty=null
    elif [ -n "$(cd "$repo" && git status --porcelain 2>/dev/null)" ]; then dirty=true
    else dirty=false; fi
  fi
  case "$dirty" in true|false|null) ;; *) die "BENCH_HOST_SERVER_DIRTY must be true, false or null" ;; esac
  if [ "$dirty" = "true" ]; then
    echo "bench-host: WARNING: uncommitted changes are not benched (the container gets git archive HEAD, commit $sha)" >&2
  fi

  # Only now: MSYS_NO_PATHCONV would stop git.exe from understanding the /d/... paths used above.
  # Git Bash rewrites /out and /work/src in docker arguments unless told not to, and wants Windows paths for -v.
  local uname_s="${BENCH_HOST_FAKE_UNAME:-$(uname -s 2>/dev/null || echo unknown)}"
  # A dry-run must leave nothing behind, so it does not create --out; a missing one is taken as given.
  if [ "$dry_run" -eq 1 ] && [ ! -d "$out_dir" ]; then
    case "$out_dir" in /*) out_abs="$out_dir" ;; *) out_abs="$PWD/$out_dir" ;; esac
  else
    mkdir -p "$out_dir"
    out_abs="$(cd "$out_dir" && pwd)"
  fi
  host_out="$out_abs"
  case "$uname_s" in
    MINGW*|MSYS*)
      export MSYS_NO_PATHCONV=1
      echo "bench-host: Git Bash detected, MSYS_NO_PATHCONV=1 and Windows paths for docker" >&2
      if [ -n "${BENCH_HOST_FAKE_PWDW:-}" ]; then host_out="$BENCH_HOST_FAKE_PWDW"
      elif [ -d "$out_dir" ]; then host_out="$(cd "$out_dir" && pwd -W)"; fi ;;
  esac

  dver="${BENCH_HOST_DOCKER_VERSION:-}"
  if [ -z "$dver" ]; then
    if [ "$dry_run" -eq 1 ]; then dver="dry-run"
    else dver="$(docker version --format '{{.Server.Version}}' 2>/dev/null || true)"; fi
  fi

  if [ -n "$predict" ] && [ -d "$out_abs" ]; then cp "$predict" "$out_abs/.predict.json"; fi

  if [ "$privileged_sysctl" -eq 1 ]; then
    echo "bench-host: WARNING: --privileged-sysctl runs the container privileged and changes the host's net.core.rmem_max/wmem_max until reboot; the script restores the old values on exit" >&2
  fi

  # The container command, one line. jq and unzip come from apt (the image lacks them) and their versions are recorded.
  local c="set -euo pipefail; export DEBIAN_FRONTEND=noninteractive; apt-get update -qq >/dev/null; apt-get install -y -qq --no-install-recommends jq unzip iproute2 >/dev/null"
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

  # a SIGINT or a failed git archive must not leave a partial .src.tar in --out (the next run would refuse it)
  trap "rm -f $(q1 "$host_out/.src.tar")" EXIT
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
