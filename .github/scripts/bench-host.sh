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

host_label_re='^(local-docker|local-linux|aws-[a-z0-9]+\.[a-z0-9]+)$'
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
[ -n "$budget" ] || die "--budget must not be empty"
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
if [ -e "$out_dir" ]; then
  [ -d "$out_dir" ] || die "--out $out_dir is not a directory"
  [ -z "$(ls -A "$out_dir")" ] || die "--out $out_dir must be empty"
fi

if [ "$dry_run" -eq 1 ]; then
  echo "bench-host: $mode arguments ok (dry-run)"
fi
