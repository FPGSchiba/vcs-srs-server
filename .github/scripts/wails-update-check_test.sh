#!/usr/bin/env bash
# Offline test for wails-update-check.sh. Portable: shared verbatim between
# vcs-srs-server and vcs-srs-client, and bash 3.2-safe (the client's
# TestCIScripts runs it on the macOS leg).
#
# `curl` and `gh` are stubbed on PATH. Each case lives in its own directory
# ($FIX), which holds the go.mod / frontend/package.json under test and the
# stubs' inputs (proxy-list, npm.json, advisories.json, open-issue, and the
# curl-fail / curl-fail-npm / gh-api-fail switches) and outputs (gh-calls,
# gh-argv, curl-argv, body).
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
script="$here/wails-update-check.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fail=0

check() { # name, got, want
  if [ "$2" = "$3" ]; then
    echo "PASS $1"
  else
    printf 'FAIL %s\n--- got ---\n%s\n--- want ---\n%s\n' "$1" "$2" "$3"; fail=1
  fi
}
has() { # haystack, needle -> yes|no
  case "$1" in
    *"$2"*) echo yes ;;
    *) echo no ;;
  esac
}

mkdir -p "$tmp/bin"

# curl: dispatch on the URL (the last argument); every argv goes to curl-argv.
# curl-fail fails every fetch, curl-fail-npm only the npm one.
cat > "$tmp/bin/curl" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$FIX/curl-argv"
url=""
for a in "$@"; do url="$a"; done
fail503() { echo "curl: (22) The requested URL returned error: 503" >&2; exit 22; }
if [ -e "$FIX/curl-fail" ]; then fail503; fi
case "$url" in
  https://proxy.golang.org/github.com/wailsapp/wails/v3/@v/list) cat "$FIX/proxy-list" ;;
  https://registry.npmjs.org/@wailsio%2Fruntime)
    if [ -e "$FIX/curl-fail-npm" ]; then fail503; fi
    cat "$FIX/npm.json" ;;
  *) echo "curl stub: unexpected URL $url" >&2; exit 99 ;;
esac
STUB

# gh: `api` serves the advisories; `issue list` prints the open issue number;
# the writing subcommands are recorded in gh-calls, with the body copied out.
cat > "$tmp/bin/gh" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$FIX/gh-argv"
if [ "$1" = "api" ]; then
  if [ -e "$FIX/gh-api-fail" ]; then
    echo "gh: Resource not accessible by integration (HTTP 403)" >&2
    exit 1
  fi
  cat "$FIX/advisories.json"
  exit 0
fi
sub="$1 $2"
shift 2
title="" num="" bodyf=""
while [ $# -gt 0 ]; do
  case "$1" in
    --title) title="$2"; shift 2 ;;
    --body-file) bodyf="$2"; shift 2 ;;
    --repo|--label|--state|--json|--jq|--color|--description) shift 2 ;;
    --*) shift ;;
    *) if [ -z "$num" ]; then num="$1"; fi; shift ;;
  esac
done
case "$sub" in
  "issue list") cat "$FIX/open-issue" 2>/dev/null || true ;;
  "label create") echo "label create" >> "$FIX/gh-calls" ;;
  "issue create")
    echo "issue create title=$title" >> "$FIX/gh-calls"
    cp "$bodyf" "$FIX/body" ;;
  "issue edit")
    echo "issue edit $num title=$title" >> "$FIX/gh-calls"
    cp "$bodyf" "$FIX/body" ;;
  *) echo "gh stub: unexpected subcommand $sub" >&2; exit 99 ;;
esac
STUB
chmod +x "$tmp/bin/curl" "$tmp/bin/gh"

# The 9 published wailsapp/wails repository advisories, captured 2026-10-28
# (trimmed to the fields the script reads).
cat > "$tmp/advisories.json" <<'JSON'
[
{"ghsa_id":"GHSA-vfcr-9mmf-hc6m","html_url":"https://github.com/wailsapp/wails/security/advisories/GHSA-vfcr-9mmf-hc6m","severity":"medium","summary":"macOS child frames can enter the Wails framework control channel","vulnerabilities":[{"package":{"ecosystem":"go","name":"github.com/wailsapp/wails/v3"},"vulnerable_version_range":"< 3.0.0-beta.31","patched_versions":"3.0.0-beta.31"}]},
{"ghsa_id":"GHSA-c8qg-xhgr-798r","html_url":"https://github.com/wailsapp/wails/security/advisories/GHSA-c8qg-xhgr-798r","severity":"high","summary":"Windows notifications: `$(…)` in notification text runs as PowerShell when the toast falls back to go-toast's PowerShell path","vulnerabilities":[{"package":{"ecosystem":"go","name":"github.com/wailsapp/wails/v3"},"vulnerable_version_range":"< 3.0.0-beta.31","patched_versions":"3.0.0-beta.31"},{"package":{"ecosystem":"go","name":"github.com/wailsapp/wails/v2"},"vulnerable_version_range":"< 2.17.0","patched_versions":"2.17.0"}]},
{"ghsa_id":"GHSA-cjw4-96p8-36wh","html_url":"https://github.com/wailsapp/wails/security/advisories/GHSA-cjw4-96p8-36wh","severity":"low","summary":"wailsapp/mimetype fork vulnerabilty","vulnerabilities":[{"package":{"ecosystem":"go","name":"github.com/wailsapp/wails/v2"},"vulnerable_version_range":"< 2.17.0","patched_versions":"2.17.0"}]},
{"ghsa_id":"GHSA-3vcq-4frm-47ph","html_url":"https://github.com/wailsapp/wails/security/advisories/GHSA-3vcq-4frm-47ph","severity":"low","summary":" Malicious template's `frontend:build` command runs on `wails build` / `wails dev` without consent","vulnerabilities":[{"package":{"ecosystem":"go","name":"github.com/wailsapp/wails/v2"},"vulnerable_version_range":"< 2.17.0","patched_versions":"2.17.0"}]},
{"ghsa_id":"GHSA-vwx8-cwg4-xx23","html_url":"https://github.com/wailsapp/wails/security/advisories/GHSA-vwx8-cwg4-xx23","severity":"medium","summary":"`wails3 init -t` passes the template name to `git clone` unvalidated (argument injection; conditional `ext::` command exec)","vulnerabilities":[{"package":{"ecosystem":"go","name":"github.com/wailsapp/wails/v3"},"vulnerable_version_range":"< 3.0.0-beta.31","patched_versions":"3.0.0-beta.31"}]},
{"ghsa_id":"GHSA-r7hq-45w4-mfcx","html_url":"https://github.com/wailsapp/wails/security/advisories/GHSA-r7hq-45w4-mfcx","severity":"high","summary":"v3 server-mode HTTP IPC endpoint /wails/runtime allows CSRF via unauthenticated GET with query parameters","vulnerabilities":[{"package":{"ecosystem":"go","name":"github.com/wailsapp/wails/v3"},"vulnerable_version_range":"< 3.0.0-beta.31","patched_versions":"3.0.0-beta.31"}]},
{"ghsa_id":"GHSA-cv6g-2vcq-wcvx","html_url":"https://github.com/wailsapp/wails/security/advisories/GHSA-cv6g-2vcq-wcvx","severity":"medium","summary":"v3 server-mode WebSocket broadcaster accepts connections from any origin (InsecureSkipVerify: true)","vulnerabilities":[{"package":{"ecosystem":"go","name":"github.com/wailsapp/wails/v3"},"vulnerable_version_range":"< 3.0.0-beta.8","patched_versions":"3.0.0-beta.8"}]},
{"ghsa_id":"GHSA-qg59-r6hv-pvm6","html_url":"https://github.com/wailsapp/wails/security/advisories/GHSA-qg59-r6hv-pvm6","severity":"high","summary":"Dev server WebSocket IPC accepts connections from any origin (CWE-346)","vulnerabilities":[{"package":{"ecosystem":"go","name":"github.com/wailsapp/wails/v2"},"vulnerable_version_range":"< 2.17.0","patched_versions":"2.17.0"}]},
{"ghsa_id":"GHSA-47hv-j4px-h3c9","html_url":"https://github.com/wailsapp/wails/security/advisories/GHSA-47hv-j4px-h3c9","severity":"high","summary":"wildcardPatternToRegex allows Origin bypass via suffix match in BindingsAllowedOrigins","vulnerabilities":[{"package":{"ecosystem":"go","name":"github.com/wailsapp/wails/v2"},"vulnerable_version_range":">= 2.0.0, < 2.13.0","patched_versions":"2.13.0"}]}
]
JSON

v3_beta31_ids="GHSA-vfcr-9mmf-hc6m GHSA-c8qg-xhgr-798r GHSA-vwx8-cwg4-xx23 GHSA-r7hq-45w4-mfcx"
v2_only_ids="GHSA-cjw4-96p8-36wh GHSA-3vcq-4frm-47ph GHSA-qg59-r6hv-pvm6 GHSA-47hv-j4px-h3c9"

mkcase() { # name, go version, npm spec, proxy list (space-separated), npm latest
  local c="$tmp/$1" v
  mkdir -p "$c/frontend"
  printf 'module example.com/fixture\n\ngo 1.27.0\n\nrequire github.com/wailsapp/wails/v3 %s\n' "$2" > "$c/go.mod"
  printf '{\n  "name": "fixture",\n  "dependencies": {\n    "@wailsio/runtime": "%s"\n  }\n}\n' "$3" > "$c/frontend/package.json"
  : > "$c/proxy-list"
  for v in $4; do printf '%s\n' "$v" >> "$c/proxy-list"; done
  printf '{"name":"@wailsio/runtime","dist-tags":{"latest":"%s","nightly":"3.1.0-nightly.20261027"}}\n' "$5" > "$c/npm.json"
  cp "$tmp/advisories.json" "$c/advisories.json"
  : > "$c/gh-calls"
  : > "$c/gh-argv"
  : > "$c/curl-argv"
}

# run name [args...]: sets out, err, rc, calls, body.
run() {
  local c="$tmp/$1"
  shift
  set +e
  out="$(cd "$c" && FIX="$c" PATH="$tmp/bin:$PATH" GITHUB_REPOSITORY=o/r bash "$script" "$@" 2> "$c/stderr")"
  rc=$?
  set -e
  err="$(cat "$c/stderr")"
  calls="$(tr -d '\r' < "$c/gh-calls")"
  body=""
  if [ -f "$c/body" ]; then body="$(tr -d '\r' < "$c/body")"; fi
}

beta22_list="v3.0.0-beta.8 v3.0.0-beta.22 v3.0.0-beta.31 v3.0.0 v3.1.0-nightly.20261027"

# --- client-beta22-opens -----------------------------------------------------
mkcase client-beta22-opens v3.0.0-beta.22 '^3.0.0-beta.34' "$beta22_list" 3.0.0
run client-beta22-opens
check "client-beta22-opens: exit" "$rc" 0
check "client-beta22-opens: gh calls" "$calls" "$(printf 'label create\nissue create title=Wails v3.0.0 available')"
check "client-beta22-opens: marker first" "$(printf '%s\n' "$body" | head -n1)" "<!-- wails-update-check -->"
check "client-beta22-opens: release link" "$(has "$body" 'https://github.com/wailsapp/wails/releases/tag/v3.0.0')" yes
check "client-beta22-opens: compare link" "$(has "$body" 'https://github.com/wailsapp/wails/compare/v3.0.0-beta.22...v3.0.0')" yes
check "client-beta22-opens: Security: YES" "$(has "$body" 'Security: YES')" yes
for id in $v3_beta31_ids; do
  check "client-beta22-opens: lists $id" "$(has "$body" "$id")" yes
done
check "client-beta22-opens: omits GHSA-cv6g-2vcq-wcvx (fixed in beta.8)" "$(has "$body" GHSA-cv6g-2vcq-wcvx)" no
for id in $v2_only_ids; do
  check "client-beta22-opens: omits v2-only $id" "$(has "$body" "$id")" no
done
check "client-beta22-opens: go row" "$(has "$body" '| Go module `github.com/wailsapp/wails/v3` | `v3.0.0-beta.22` → `v3.0.0` |')" yes
check "client-beta22-opens: npm row" "$(has "$body" '| npm `@wailsio/runtime` | `3.0.0-beta.34` → `3.0.0` |')" yes
for code in '(U5)' 'Phase 6.9' 'UP design' TestWailsRuntimeMatchesGoModule; do
  check "client-beta22-opens: body is repo-neutral (no $code)" "$(has "$body" "$code")" no
done
argv="$(tr -d '\r' < "$tmp/client-beta22-opens/gh-argv")"
check "client-beta22-opens: lists open labelled issues of the repo" "$(has "$argv" 'issue list --repo o/r --label wails-update --state open')" yes
check "client-beta22-opens: label colour and description" "$(has "$argv" 'label create wails-update --repo o/r --force --color FBCA04 --description A newer Wails release is available')" yes
check "client-beta22-opens: issue carries the label" "$(has "$argv" 'issue create --repo o/r --label wails-update')" yes
curl_argv="$(tr -d '\r' < "$tmp/client-beta22-opens/curl-argv")"
check "client-beta22-opens: two fetches" "$(printf '%s\n' "$curl_argv" | grep -c .)" 2
check "client-beta22-opens: every fetch is https-only, TLS 1.2+"   "$(printf '%s\n' "$curl_argv" | grep -c -- '--proto =https --tlsv1.2')" 2

# --- server-alpha43-five -----------------------------------------------------
mkcase server-alpha43-five v3.0.0-alpha.43 '^3.0.0-beta.34' "v3.0.0-alpha.43 $beta22_list" 3.0.0
run server-alpha43-five
check "server-alpha43-five: exit" "$rc" 0
check "server-alpha43-five: Security: YES" "$(has "$body" 'Security: YES')" yes
for id in $v3_beta31_ids GHSA-cv6g-2vcq-wcvx; do
  check "server-alpha43-five: lists $id" "$(has "$body" "$id")" yes
done
for id in $v2_only_ids; do
  check "server-alpha43-five: omits v2-only $id" "$(has "$body" "$id")" no
done

# --- existing-issue-updated --------------------------------------------------
mkcase existing-issue-updated v3.0.0-beta.22 '^3.0.0-beta.34' "$beta22_list" 3.0.0
echo 7 > "$tmp/existing-issue-updated/open-issue"
run existing-issue-updated
check "existing-issue-updated: exit" "$rc" 0
check "existing-issue-updated: gh calls" "$calls" "issue edit 7 title=Wails v3.0.0 available"
check "existing-issue-updated: marker first" "$(printf '%s\n' "$body" | head -n1)" "<!-- wails-update-check -->"

# --- up-to-date-noop ---------------------------------------------------------
mkcase up-to-date-noop v3.0.0 3.0.0 "v3.0.0-beta.22 v3.0.0" 3.0.0
run up-to-date-noop
check "up-to-date-noop: exit" "$rc" 0
check "up-to-date-noop: gh calls" "$calls" ""
check "up-to-date-noop: message" "$(has "$out" 'Wails is up to date (go v3.0.0, npm 3.0.0).')" yes

# --- nightly-ignored ---------------------------------------------------------
mkcase nightly-ignored v3.0.0 3.0.0 "v3.0.0 v3.1.0-nightly.20261027" 3.0.0
run nightly-ignored
check "nightly-ignored: exit" "$rc" 0
check "nightly-ignored: gh calls" "$calls" ""
check "nightly-ignored: message" "$(has "$out" 'Wails is up to date (go v3.0.0, npm 3.0.0).')" yes

# --- semver-numeric ----------------------------------------------------------
mkcase semver-numeric v3.9.0 3.9.0 "v3.9.0 v3.10.0 v3.2.0" 3.10.0
run semver-numeric
check "semver-numeric: exit" "$rc" 0
check "semver-numeric: gh calls" "$calls" "$(printf 'label create\nissue create title=Wails v3.10.0 available')"
check "semver-numeric: Security: no" "$(has "$body" 'Security: no')" yes
check "semver-numeric: go row (numeric max of the proxy list)" "$(has "$body" '| Go module `github.com/wailsapp/wails/v3` | `v3.9.0` → `v3.10.0` |')" yes

# --- npm-only-behind ---------------------------------------------------------
mkcase npm-only-behind v3.1.0 3.0.0 "v3.0.0 v3.1.0" 3.1.0
run npm-only-behind
check "npm-only-behind: exit" "$rc" 0
check "npm-only-behind: gh calls" "$calls" "$(printf 'label create\nissue create title=Wails v3.1.0 available')"
check "npm-only-behind: npm row" "$(has "$body" '| npm `@wailsio/runtime` | `3.0.0` → `3.1.0` |')" yes

# --- advisory-lookup-fails-still-reports -------------------------------------
mkcase advisory-lookup-fails-still-reports v3.0.0-beta.22 '^3.0.0-beta.34' "$beta22_list" 3.0.0
touch "$tmp/advisory-lookup-fails-still-reports/gh-api-fail"
run advisory-lookup-fails-still-reports
check "advisory-lookup-fails-still-reports: exit" "$rc" 0
check "advisory-lookup-fails-still-reports: gh calls" "$calls" "$(printf 'label create\nissue create title=Wails v3.0.0 available')"
check "advisory-lookup-fails-still-reports: Security: unknown" "$(has "$body" 'Security: unknown')" yes
check "advisory-lookup-fails-still-reports: advisories link" "$(has "$body" 'https://github.com/wailsapp/wails/security/advisories')" yes

# --- proxy-fetch-fails -------------------------------------------------------
mkcase proxy-fetch-fails v3.0.0-beta.22 '^3.0.0-beta.34' "$beta22_list" 3.0.0
touch "$tmp/proxy-fetch-fails/curl-fail"
run proxy-fetch-fails
check "proxy-fetch-fails: non-zero exit" "$([ "$rc" -ne 0 ] && echo yes || echo no)" yes
check "proxy-fetch-fails: gh calls" "$calls" ""
check "proxy-fetch-fails: stderr" "$(has "$err" 'could not fetch the Go module proxy version list')" yes

# --- missing-require ---------------------------------------------------------
mkcase missing-require v3.0.0 3.0.0 "v3.0.0" 3.0.0
printf 'module example.com/fixture\n\ngo 1.27.0\n\nrequire example.com/other v1.0.0\n' > "$tmp/missing-require/go.mod"
run missing-require
check "missing-require: non-zero exit" "$([ "$rc" -ne 0 ] && echo yes || echo no)" yes
check "missing-require: stderr" "$(has "$err" 'github.com/wailsapp/wails/v3 not found in go.mod')" yes
check "missing-require: gh calls" "$calls" ""

# --- advisory-beyond-latest --------------------------------------------------
# Patched only above the latest release: the update does not fix it.
mkcase advisory-beyond-latest v3.0.0 3.0.0 "v3.0.0 v3.1.0" 3.1.0
cat > "$tmp/advisory-beyond-latest/advisories.json" <<'JSON'
[{"ghsa_id":"GHSA-test-beyo-ndla","html_url":"https://example.invalid/a","severity":"high","vulnerabilities":[{"package":{"ecosystem":"go","name":"github.com/wailsapp/wails/v3"},"vulnerable_version_range":"< 3.2.0","patched_versions":"3.2.0"}]}]
JSON
run advisory-beyond-latest
check "advisory-beyond-latest: exit" "$rc" 0
check "advisory-beyond-latest: Security: no" "$(has "$body" 'Security: no')" yes
check "advisory-beyond-latest: omits GHSA-test-beyo-ndla" "$(has "$body" GHSA-test-beyo-ndla)" no

# --- advisory-unparsable -----------------------------------------------------
# No version in patched_versions: the rule cannot be applied.
mkcase advisory-unparsable v3.0.0 3.0.0 "v3.0.0 v3.1.0" 3.1.0
cat > "$tmp/advisory-unparsable/advisories.json" <<'JSON'
[{"ghsa_id":"GHSA-test-unpa-rsed","html_url":"https://example.invalid/b","severity":"high","vulnerabilities":[{"package":{"ecosystem":"npm","name":"@wailsio/runtime"},"vulnerable_version_range":"< 3.1.0","patched_versions":null}]}]
JSON
run advisory-unparsable
check "advisory-unparsable: exit" "$rc" 0
check "advisory-unparsable: Security: unknown" "$(has "$body" 'Security: unknown')" yes
check "advisory-unparsable: could-not-be-evaluated line" "$(has "$body" 'Could not be evaluated (no version in patched_versions): GHSA-test-unpa-rsed')" yes

# --- npm-latest-prerelease-ignored -------------------------------------------
mkcase npm-latest-prerelease-ignored v3.0.0 3.0.0 "v3.0.0" 3.1.0-beta.1
run npm-latest-prerelease-ignored
check "npm-latest-prerelease-ignored: exit" "$rc" 0
check "npm-latest-prerelease-ignored: gh calls" "$calls" ""
check "npm-latest-prerelease-ignored: message" "$(has "$out" 'Wails is up to date (go v3.0.0, npm 3.0.0).')" yes

# --- npm-latest-not-plain ----------------------------------------------------
mkcase npm-latest-not-plain v3.0.0 3.0.0 "v3.0.0" '3.1.0+build.5'
run npm-latest-not-plain
check "npm-latest-not-plain: non-zero exit" "$([ "$rc" -ne 0 ] && echo yes || echo no)" yes
check "npm-latest-not-plain: stderr" "$(has "$err" 'npm latest dist-tag for @wailsio/runtime is not a plain version: 3.1.0+build.5')" yes
check "npm-latest-not-plain: gh calls" "$calls" ""

# --- npm-fetch-fails ---------------------------------------------------------
mkcase npm-fetch-fails v3.0.0-beta.22 '^3.0.0-beta.34' "$beta22_list" 3.0.0
touch "$tmp/npm-fetch-fails/curl-fail-npm"
run npm-fetch-fails
check "npm-fetch-fails: non-zero exit" "$([ "$rc" -ne 0 ] && echo yes || echo no)" yes
check "npm-fetch-fails: stderr" "$(has "$err" 'could not fetch @wailsio/runtime from the npm registry')" yes
check "npm-fetch-fails: gh calls" "$calls" ""

# --- npm-spec-not-plain ------------------------------------------------------
for spec in latest '>=3'; do
  mkcase npm-spec-not-plain v3.0.0 "$spec" "v3.0.0" 3.0.0
  run npm-spec-not-plain
  check "npm-spec-not-plain ($spec): non-zero exit" "$([ "$rc" -ne 0 ] && echo yes || echo no)" yes
  check "npm-spec-not-plain ($spec): stderr" "$(has "$err" "@wailsio/runtime in frontend/package.json is not a plain version: $spec")" yes
  check "npm-spec-not-plain ($spec): gh calls" "$calls" ""
  rm -rf "$tmp/npm-spec-not-plain"
done

# --- dry-run -----------------------------------------------------------------
mkcase dry-run v3.0.0-beta.22 '^3.0.0-beta.34' "$beta22_list" 3.0.0
run dry-run --dry-run
check "dry-run: exit" "$rc" 0
check "dry-run: gh calls" "$calls" ""
check "dry-run: title on stdout" "$(has "$out" 'Wails v3.0.0 available')" yes
check "dry-run: body on stdout" "$(has "$out" '<!-- wails-update-check -->')" yes

exit "$fail"
