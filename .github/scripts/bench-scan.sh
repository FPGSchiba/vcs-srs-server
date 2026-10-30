#!/usr/bin/env bash
# Scan 2 of the fail-closed publish gate (shell + jq), independent of the Go
# scan. Usage: bench-scan.sh DIR [EXTRA_LITERAL...]. Exit 1 on any finding.
# Findings print as "file: rule" only; matched text is never echoed.
set -uo pipefail

dir="${1:?usage: bench-scan.sh DIR [EXTRA...]}"; shift
[ -d "$dir" ] || { echo "not a directory: $dir" >&2; exit 2; }
rc=0
flag() { echo "$1: $2" >&2; rc=1; }

H='[0-9A-Fa-f]'
B='(^|[^0-9A-Fa-f:])'
E='($|[^0-9A-Fa-f:])'
v6_full="($H{1,4}:){7}$H{1,4}"
v6_comp="($H{1,4}:){0,6}$H{0,4}::($H{1,4}:){0,6}$H{0,4}"
re_v6="${B}(${v6_full}|${v6_comp})${E}"
re_v4='(^|[^0-9.])([0-9]{1,3}\.){3}[0-9]{1,3}($|[^0-9.])'

while IFS= read -r -d '' f; do
  rel="${f#"$dir"/}"
  grep -Eq -e "$re_v4" "$f" && flag "$rel" ipv4-literal
  grep -Eq -e "$re_v6" "$f" && flag "$rel" ipv6-literal
  grep -Fq -e '.internal.cloudapp.net' "$f" && flag "$rel" cloudapp-host
  grep -Fq -e 'fv-az' "$f" && flag "$rel" runner-host
  grep -Fq -e '/home/runner' "$f" && flag "$rel" runner-path
  for x in "$@"; do
    [ -n "$x" ] && grep -Fq -e "$x" "$f" && flag "$rel" extra-literal
  done
done < <(find "$dir" -type f -print0)

# manifest.json, when present, must carry exactly the schema's top-level keys.
m="$dir/manifest.json"
if [ -f "$m" ]; then
  jq -e '(keys | sort) == ["client_go","client_sha","corpus","env","join_label","label","legs","pins","run_url","schema_version","server_dirty","server_go","server_sha"]' "$m" >/dev/null 2>&1 \
    || flag manifest.json manifest-keys
fi
exit "$rc"
