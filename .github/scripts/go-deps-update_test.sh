#!/usr/bin/env bash
# Offline test for go-deps-update.sh --list. Portable: shared verbatim between
# vcs-srs-server and vcs-srs-client.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
script="$here/go-deps-update.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
export GOTOOLCHAIN=local
fail=0

check() { # name, got, want
  if [ "$2" = "$3" ]; then
    echo "PASS $1"
  else
    printf 'FAIL %s\n--- got ---\n%s\n--- want ---\n%s\n' "$1" "$2" "$3"; fail=1
  fi
}

mkdir -p "$tmp/full" "$tmp/empty"
cat > "$tmp/full/go.mod" <<'MOD'
module example.com/fixture

go 1.27.0

require (
	example.com/direct/a v1.0.0
	example.com/direct/b v1.0.0
	example.com/excluded v1.0.0
	example.com/excluded/v2 v2.0.0
)

require example.com/indirect v1.0.0 // indirect
MOD
printf 'module example.com/empty\n\ngo 1.27.0\n' > "$tmp/empty/go.mod"

got="$(cd "$tmp/full" && GO_DEPS_EXCLUDE="example.com/excluded" bash "$script" --list)"
check exclude-exact "$got" "$(printf 'example.com/direct/a\nexample.com/direct/b\nexample.com/excluded/v2')"

got="$(cd "$tmp/full" && GO_DEPS_EXCLUDE="" bash "$script" --list)"
check no-exclude "$got" "$(printf 'example.com/direct/a\nexample.com/direct/b\nexample.com/excluded\nexample.com/excluded/v2')"

set +e
got="$(cd "$tmp/empty" && GO_DEPS_EXCLUDE="" bash "$script" --list)"
rc=$?
set -e
check no-requires "$got|$rc" "|0"

# Update mode must request @upgrade, never @latest: @latest resolves to the
# newest *release* and would downgrade a direct requirement pinned to a newer
# pre-release (the client pins github.com/robotn/gohook v1.0.0-beta1 while
# @latest is v0.50.0). A stub `go` records the `go get` arguments; every other
# subcommand is delegated to the real go, except `mod tidy`, which is a no-op.
real_go="$(command -v go)"
mkdir -p "$tmp/bin"
cat > "$tmp/bin/go" <<STUB
#!/usr/bin/env bash
case "\$1 \${2:-}" in
  "get "*) shift; printf '%s\n' "\$@" > "$tmp/get-args"; exit 0 ;;
  "mod tidy") exit 0 ;;
esac
exec "$real_go" "\$@"
STUB
chmod +x "$tmp/bin/go"
rm -f "$tmp/get-args"
(cd "$tmp/full" && PATH="$tmp/bin:$PATH" GO_DEPS_EXCLUDE="example.com/excluded" bash "$script")
got="$(tr -d '\r' < "$tmp/get-args" 2>/dev/null || true)"
check update-uses-upgrade "$got" "$(printf 'example.com/direct/a@upgrade\nexample.com/direct/b@upgrade\nexample.com/excluded/v2@upgrade')"

exit "$fail"
