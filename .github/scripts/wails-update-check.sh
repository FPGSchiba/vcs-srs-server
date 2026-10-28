#!/usr/bin/env bash
# Reports a newer stable Wails v3 release by opening (or updating) ONE issue
# labelled `wails-update`. Portable: shared verbatim between vcs-srs-server and
# vcs-srs-client; repo-specific values come from the environment only.
# bash 3.2-safe (no mapfile, no associative arrays, no sed -i).
#
#   GITHUB_REPOSITORY   owner/repo the issue goes to (required unless --dry-run)
#   GH_TOKEN            token `gh` uses (the workflow's GITHUB_TOKEN)
#   WAILS_GO_MOD        default go.mod
#   WAILS_PACKAGE_JSON  default frontend/package.json
#   GITHUB_SERVER_URL, GITHUB_RUN_ID   optional; add a link to the run
#
# Usage, from the repo root:
#   bash .github/scripts/wails-update-check.sh            # open/update the issue
#   bash .github/scripts/wails-update-check.sh --dry-run  # print title + body only
#
# It only reports; it never bumps anything. Wails moves by hand: the Go module
# and @wailsio/runtime are bumped together, in one PR.
#
# Latest versions: the Go module proxy's version list and npm's `latest`
# dist-tag, stable releases only (nightlies and other pre-releases are ignored).
# Security: the wailsapp/wails repository-advisory API. The Go vuln DB and the
# global advisory DB carry none of Wails' v3 advisories. An advisory counts as
# fixed by the update when the first version in its patched_versions, P, for
# our Go module (or npm package) satisfies current < P <= latest. If the
# lookup fails the issue still goes out, with "Security: unknown".
#
# Exit 0 when up to date, or when the issue was created or updated.
set -euo pipefail

dry_run=0
case "${1:-}" in
  "") ;;
  --dry-run) dry_run=1 ;;
  *) echo "usage: $0 [--dry-run]" >&2; exit 2 ;;
esac

die() { echo "wails-update-check: $*" >&2; exit 1; }

go_mod="${WAILS_GO_MOD:-go.mod}"
pkg_json="${WAILS_PACKAGE_JSON:-frontend/package.json}"
go_pkg="github.com/wailsapp/wails/v3"
npm_pkg="@wailsio/runtime"
label="wails-update"
marker="<!-- wails-update-check -->"
advisories_url="https://github.com/wailsapp/wails/security/advisories"

if [ "$dry_run" = 0 ] && [ -z "${GITHUB_REPOSITORY:-}" ]; then
  die "GITHUB_REPOSITORY is not set (use --dry-run to only print)"
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# Semver ordering key: numeric core, then a release sorts above any of its
# pre-releases, and pre-release identifiers compare numerically when numeric.
semkey='def semkey: ltrimstr("v") | (index("-")) as $i
  | (if $i then .[:$i] else . end) as $core | (if $i then .[$i+1:] else null end) as $pre
  | ($core | split(".") | map(tonumber))
    + (if $pre == null then [1] else [0] + ($pre | split(".") | map(if test("^[0-9]+$") then tonumber else . end)) end);'

# --- current versions --------------------------------------------------------
[ -f "$go_mod" ] || die "$go_mod not found"
go_current="$(sed -nE 's/^[[:space:]]*(require[[:space:]]+)?github\.com\/wailsapp\/wails\/v3[[:space:]]+(v[^[:space:]]+).*/\2/p' "$go_mod" | head -n1 | tr -d '\r')"
[ -n "$go_current" ] || die "$go_pkg not found in $go_mod"

[ -f "$pkg_json" ] || die "$pkg_json not found"
npm_spec="$(jq -r --arg p "$npm_pkg" '.dependencies[$p] // empty' < "$pkg_json" | tr -d '\r')"
[ -n "$npm_spec" ] || die "$npm_pkg not found in the dependencies of $pkg_json"
npm_current="${npm_spec#[~^=]}"
plain_re='^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$'
[[ "$npm_current" =~ $plain_re ]] || die "$npm_pkg in $pkg_json is not a plain version: $npm_spec"

# --- latest stable versions --------------------------------------------------
curl -fsSL --proto =https --tlsv1.2 --retry 3 --max-time 30 "https://proxy.golang.org/github.com/wailsapp/wails/v3/@v/list" > "$work/proxy-list" \
  || die "could not fetch the Go module proxy version list"
go_latest="$(tr -d '\r' < "$work/proxy-list" | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' \
  | jq -R . | jq -rs "$semkey"' max_by(semkey) // empty' | tr -d '\r' || true)"
[ -n "$go_latest" ] || die "the Go module proxy lists no stable $go_pkg version"

curl -fsSL --proto =https --tlsv1.2 --retry 3 --max-time 30 -H 'Accept: application/vnd.npm.install-v1+json' 'https://registry.npmjs.org/@wailsio%2Fruntime' > "$work/npm.json" \
  || die "could not fetch $npm_pkg from the npm registry"
npm_latest="$(jq -r '."dist-tags".latest // empty' < "$work/npm.json" | tr -d '\r')"
[ -n "$npm_latest" ] || die "the npm registry has no latest dist-tag for $npm_pkg"
case "$npm_latest" in
  *-*) npm_latest="$npm_current" ;; # a pre-release on `latest` is not a release
esac
# It ends up in the issue title, body and suggested commands: plain versions only.
[[ "$npm_latest" =~ $plain_re ]] || die "npm latest dist-tag for $npm_pkg is not a plain version: $npm_latest"

# --- advisories (best effort) ------------------------------------------------
if gh api "repos/wailsapp/wails/security-advisories?state=published&per_page=100" > "$work/advisories.json" 2> "$work/gh-api.err" \
   && jq -e 'type == "array"' < "$work/advisories.json" > /dev/null 2>&1; then
  :
else
  echo "wails-update-check: advisory lookup failed, reporting Security: unknown" >&2
  sed 's/^/  /' "$work/gh-api.err" >&2 || true
  echo null > "$work/advisories.json"
fi

# --- compare + evaluate, one jq program --------------------------------------
jq -n --arg gc "$go_current" --arg gl "$go_latest" --arg nc "$npm_current" --arg nl "$npm_latest" \
  --arg gp "$go_pkg" --arg np "$npm_pkg" "$semkey"'
  def newer($cur; $lat): ($lat | semkey) > ($cur | semkey);
  def firstver: if type == "string"
    then ([match("[0-9]+\\.[0-9]+\\.[0-9]+(-[0-9A-Za-z.]+)?")] | if length > 0 then .[0].string else null end)
    else null end;
  input as $adv
  | ([$gl, $nl] | max_by(semkey) | ltrimstr("v")) as $x
  | (if $adv == null then null else
      [ $adv[]
        | . as $a
        | [ (.vulnerabilities // [])[]
            | (.package.name // "") as $name
            | select($name == $gp or $name == $np)
            | (if $name == $gp then [$gc, $gl] else [$nc, $nl] end) as $cl
            | (.patched_versions | firstver) as $p
            | { range: (.vulnerable_version_range // ""), patched: $p,
                fixed: ($p != null and newer($cl[0]; $p) and (($p | semkey) <= ($cl[1] | semkey))) } ]
        | select(length > 0)
        | { id: $a.ghsa_id, url: $a.html_url, severity: ($a.severity // "unknown"),
            fix: (map(select(.fixed)) | first),
            unevaluated: any(.[]; .patched == null) } ]
     end) as $ev
  | { newer: (newer($gc; $gl) or newer($nc; $nl)),
      x: $x,
      title: "Wails v\($x) available",
      fixed: (if $ev == null then [] else [ $ev[] | select(.fix != null)
                | { id, url, severity, range: .fix.range, patched: .fix.patched } ] end),
      unevaluated: (if $ev == null then [] else [ $ev[] | select(.fix == null and .unevaluated) | .id ] end) }
  | .security = (if $ev == null then "unknown"
                 elif (.fixed | length) > 0 then "yes"
                 elif (.unevaluated | length) > 0 then "unknown"
                 else "no" end)
' < "$work/advisories.json" > "$work/result.json"

field() { jq -r "$1" < "$work/result.json" | tr -d '\r'; }

if [ "$(field .newer)" != "true" ]; then
  echo "Wails is up to date (go $go_current, npm $npm_current)."
  exit 0
fi

x="$(field .x)"
title="$(field .title)"
security="$(field .security)"

# --- body ----------------------------------------------------------------------
body="$work/body.md"
{
  echo "$marker"
  echo "A newer stable Wails release is available."
  echo
  echo "| Package | Current → latest |"
  echo "|---|---|"
  echo "| Go module \`$go_pkg\` | \`$go_current\` → \`$go_latest\` |"
  echo "| npm \`$npm_pkg\` | \`$npm_current\` → \`$npm_latest\` |"
  echo
  echo "- Release notes: https://github.com/wailsapp/wails/releases/tag/v$x"
  if [ "$go_current" != "v$x" ]; then
    echo "- Changes: https://github.com/wailsapp/wails/compare/$go_current...v$x"
  fi
  echo
  case "$security" in
    yes)
      echo "**Security: YES** - this release fixes published advisories that affect \`$go_current\` / \`$npm_current\`:"
      echo
      jq -r '.fixed[] | "- [\(.id)](\(.url)) (\(.severity)): \(.range), fixed in \(.patched)"' < "$work/result.json" | tr -d '\r'
      ;;
    no)
      echo "**Security: no** - no published Wails advisory is fixed between the current and the latest version ($advisories_url)."
      ;;
    *)
      echo "**Security: unknown** - the advisories could not be fully evaluated; check $advisories_url by hand."
      ;;
  esac
  unevaluated="$(jq -r '.unevaluated | join(", ")' < "$work/result.json" | tr -d '\r')"
  if [ -n "$unevaluated" ]; then
    echo
    echo "Could not be evaluated (no version in patched_versions): $unevaluated"
  fi
  echo
  echo "### How to bump"
  echo
  echo "- Run the repo's code generation first, so \`go mod tidy\` sees every package (client: \`buf generate\`; server: protoc + \`go run github.com/99designs/gqlgen generate\`)."
  echo "- \`go get $go_pkg@v$x && go mod tidy\`"
  echo "- \`cd frontend && npm install --save-exact $npm_pkg@$x\`"
  echo "- Re-run the checks a Wails bump needs: binding generation, the frontend build, the full test suite and govulncheck."
  echo "- Bump the Go module and \`$npm_pkg\` together in ONE PR; the PR closes this issue with \`Closes #N\`."
  if [ -n "${GITHUB_RUN_ID:-}" ]; then
    echo
    echo "Reported by ${GITHUB_SERVER_URL:-https://github.com}/${GITHUB_REPOSITORY:-}/actions/runs/$GITHUB_RUN_ID"
  fi
} > "$body"

if [ "$dry_run" = 1 ]; then
  echo "$title"
  echo
  cat "$body"
  exit 0
fi

# --- issue -------------------------------------------------------------------
num="$(gh issue list --repo "$GITHUB_REPOSITORY" --label "$label" --state open --json number --jq '.[0].number // empty' | tr -d '\r')"
if [ -n "$num" ]; then
  gh issue edit "$num" --repo "$GITHUB_REPOSITORY" --title "$title" --body-file "$body"
  echo "Updated #$num: $title"
else
  gh label create "$label" --repo "$GITHUB_REPOSITORY" --force --color FBCA04 --description "A newer Wails release is available"
  gh issue create --repo "$GITHUB_REPOSITORY" --label "$label" --title "$title" --body-file "$body"
  echo "Created: $title"
fi
