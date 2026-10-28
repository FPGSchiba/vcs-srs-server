#!/usr/bin/env bash
# Gate-0 runner probe: prints one JSON object (env-probe/1) describing the runner.
# Reads only files under ${PROBE_ROOT:-}, plus ImageOS/ImageVersion and `ulimit -n`.
# PROBE_STAT2 (test hook) overrides the file for the second steal read.
# Never prints the hostname. Fails cleanly (no partial JSON) if a file is missing.
# The CPU split is PROVISIONAL (Q6-A) until the user decides Q6.
set -euo pipefail

root="${PROBE_ROOT:-}"
steal_seconds="${PROBE_STEAL_SECONDS:-20}"
label='Shared GitHub runner (4 vCPU), loopback, relative only. Not a capacity figure.'

need() { # <path>: print the path or fail
  if [ ! -e "$1" ]; then
    echo "bench-probe: $1 not found" >&2
    exit 1
  fi
  printf '%s' "$1"
}
slurp() { local p; p="$(need "$1")" || exit 1; tr -d "\r" < "$p"; }

# expand "0-1,4" into "0 1 4"
expand() {
  local part lo hi
  IFS=',' read -ra parts <<< "$1"
  for part in "${parts[@]}"; do
    if [[ "$part" == *-* ]]; then
      lo="${part%-*}"; hi="${part#*-}"
      seq "$lo" "$hi"
    else
      echo "$part"
    fi
  done
}
csv() { paste -sd, -; } # lines to comma list

steal() { # <stat file>: 8th value after the label (awk $9) of the aggregate cpu line; 0 if absent
  local f; f="$(need "$1")"
  local v; v="$(awk '$1=="cpu"{print $9; exit}' "$f")"
  printf '%s' "${v:-0}"
}

kernel="$(slurp "$root/proc/sys/kernel/osrelease")"
kernel="${kernel//[$'\n ']/}"
cpuinfo="$(need "$root/proc/cpuinfo")"
cpu_model="$(awk -F: '/^model name/{sub(/^[ \t]+/,"",$2); print $2; exit}' "$cpuinfo")"

cpudir="$root/sys/devices/system/cpu"
need "$cpudir" >/dev/null
mapfile -t cpus < <(find "$cpudir" -maxdepth 1 -name 'cpu[0-9]*' | sed 's#.*/cpu##' | sort -n)
vcpus="${#cpus[@]}"
[ "$vcpus" -gt 0 ] || { echo "bench-probe: $cpudir/cpu* not found" >&2; exit 1; }

groups=()
for c in "${cpus[@]}"; do
  raw="$(slurp "$cpudir/cpu$c/topology/thread_siblings_list")"
  groups+=("$(expand "$raw" | sort -n | csv)")
done
mapfile -t siblings < <(printf '%s\n' "${groups[@]}" | sort -u | awk -F, '{print $1"\t"$0}' | sort -n -k1,1 | cut -f2)

[ "$vcpus" -ge 2 ] || { echo "bench-probe: need >= 2 vcpus, found $vcpus" >&2; exit 1; }

first="${siblings[0]}"
IFS=',' read -ra first_cpus <<< "$first"
if [ "${#first_cpus[@]}" -ge 2 ]; then
  smt=true; topology="2 hyperthreads of 1 core"
  server=("${first_cpus[@]}")
else
  smt=false; topology="2 cores"
  server=("${cpus[0]}" "${cpus[1]}")
fi
server_set="$(printf '%s\n' "${server[@]}" | sort -n | csv)"
generator_set="$(printf '%s\n' "${cpus[@]}" | grep -vxFf <(printf '%s\n' "${server[@]}") | sort -n | csv || true)"

irq=unknown
cfg="$root/boot/config-$kernel"
if [ -e "$cfg" ]; then
  if grep -q '^CONFIG_IRQ_TIME_ACCOUNTING=y' "$cfg"; then irq=y
  elif grep -q '^# CONFIG_IRQ_TIME_ACCOUNTING is not set' "$cfg"; then irq=n; fi
fi

sc() { local v; v="$(slurp "$root/proc/sys/net/core/$1")"; printf '%s' "${v//[$'\n']/}"; }
port_range="$(slurp "$root/proc/sys/net/ipv4/ip_local_port_range" | tr -s ' \t' ' ' | tr -d '\n' | sed 's/ $//')"
rmem_default="$(sc rmem_default)"; rmem_max="$(sc rmem_max)"
wmem_default="$(sc wmem_default)"; wmem_max="$(sc wmem_max)"

s0="$(steal "$root/proc/stat")"
sleep "$steal_seconds"
s1="$(steal "${PROBE_STAT2:-$root/proc/stat}")"
steal_ticks=$((s1 - s0))

siblings_json="$(printf '%s\n' "${siblings[@]}" | jq -R . | jq -s .)"
jq -n \
  --arg label "$label" --arg kernel "$kernel" --arg cpu_model "$cpu_model" \
  --argjson vcpus "$vcpus" --argjson siblings "$siblings_json" --argjson smt "$smt" \
  --arg server_set "$server_set" --arg generator_set "$generator_set" --arg topology "$topology" \
  --arg irq "$irq" --arg rd "$rmem_default" --arg rm "$rmem_max" \
  --arg wd "$wmem_default" --arg wm "$wmem_max" --arg pr "$port_range" \
  --arg ulimit_n "$(ulimit -n)" --argjson steal_ticks "$steal_ticks" \
  --argjson steal_seconds "$steal_seconds" \
  --arg image_os "${ImageOS:-}" --arg image_version "${ImageVersion:-}" \
  '{schema:"env-probe/1", label:$label, kernel:$kernel, cpu_model:$cpu_model, vcpus:$vcpus,
    siblings:$siblings, smt:$smt, server_set:$server_set, generator_set:$generator_set,
    topology_label:$topology, irq_time_accounting:$irq,
    sysctl:{rmem_default:$rd, rmem_max:$rm, wmem_default:$wd, wmem_max:$wm, ip_local_port_range:$pr},
    ulimit_n:$ulimit_n, steal_ticks:$steal_ticks, steal_seconds:$steal_seconds,
    image_os:$image_os, image_version:$image_version}'
