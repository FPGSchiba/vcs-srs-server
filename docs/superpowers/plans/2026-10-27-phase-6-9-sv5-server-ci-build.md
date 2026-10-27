# Phase 6.9 SV5 — Server CI & build Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Repo / branch:** `vcs-srs-server` (`D:\Projects\vcs-srs-server`). Execute in a **fresh git worktree** on branch **`chore/cleanup-sv5`**, branched from **`origin/main`** (c3096ce or later). Do not touch `main` directly and do not force-push.

**Goal:** Make server CI green, safe (no secrets or write token reachable from PR-head code) and reproducible (Go 1.27.3 everywhere, pinned generators). Make the Windows headless binary a console app. Replace the broken Go Dependabot with a weekly PR-opening workflow.

**Architecture:** One composite action (`.github/actions/generate`) owns pinned code generation for every workflow. The Dockerfile mirrors it. Two small portable bash scripts (`assert-go-toolchain.sh`, `go-deps-update.sh`) carry the logic that can be tested offline. The deps workflow splits code execution (no secret) from PR creation (secret, no Go code).

**Tech Stack:** GitHub Actions, bash, jq, Go 1.27.3, protoc v36.2, Taskfile v3, Docker buildx.

**Spec:** `docs/superpowers/specs/2026-10-27-phase-6-9-sv5-server-ci-build-design.md` (committed by Task 9 from the approved scratch draft `design-SV5.md`). The umbrella is the client repo's `docs/superpowers/specs/2026-10-27-vcs-client-phase-6-9-cleanup-umbrella-design.md`, §3 SV5 and §9.

## Global Constraints

- Go: `go 1.27.0` + `toolchain go1.27.3` in `go.mod`. Docker `golang:1.27.3`. Workflows use `go-version-file: go.mod`, never a literal version.
- protoc **v36.2**:
  - `linux-x86_64` sha256 `121f6c7afe1d4d0e3ea6aab9432038599250134cbf4474cb1167d2c7decd4278`;
  - `linux-aarch_64` sha256 `8b8f18bd2b30346efbc698dd5a73dd7c805f3ef8380f6dfc95c768f3f1852f6a`.
- `protoc-gen-go@$(go list -m -f '{{.Version}}' google.golang.org/protobuf)`; `protoc-gen-go-grpc@v1.6.2`; gqlgen via `go run github.com/99designs/gqlgen@$(go list -m -f '{{.Version}}' github.com/99designs/gqlgen) generate`.
- The secret name is **`DEPS_BOT_TOKEN`**, shared verbatim with client track CL5. The scripts in `.github/scripts/` are portable: no repo-specific values. Repo-specific values live only in the workflow `env` (`GO_DEPS_EXCLUDE`, `GO_BUILD_TAGS`).
- Wails (`github.com/wailsapp/wails/v3`) is excluded from automated bumps until track UP.
- The Codecov upload uses OIDC (`use_oidc: true`). No `token:` and no `secrets.CODECOV_TOKEN` reference may remain anywhere.
- Every workflow and job ends up with explicit least-privilege `permissions`.
- Workflow lint: `go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12` must report nothing (a tool invocation, not a module dependency).
- Server suite:
  - `go build -tags headless ./...`
  - `go vet -tags headless ./...`
  - `go test -race -count=1 -tags headless ./...`

  **Until SV3 merges, `go vet` fails with exactly one finding, `srs/srs_service.go:590:3: unreachable code`. Any other vet output is a failure.**
- Generated code (`srspb/`, `voicecontrolpb/`, `vcsauthpb/`, `graphql/generated/`) is gitignored. Never commit it.
- **ORDERING (D4):** client track CL5 pins the client CI's server checkout to a SHA and **merges before** this PR's go.mod bump to Go 1.27. SV5's PR may be opened and reviewed earlier, but **must not merge until CL5 is merged** (checked in Task 9).

## Review Focus

1. **A fork PR or Dependabot PR** must run the Go tests with a read-only token, no `id-token`, no secret, and no Codecov step. It must not fail because the OIDC token is missing (Task 4: the `if:` condition, checked by grep).
2. **`DEPS_BOT_TOKEN` is not set:** the deps workflow fails at `preflight` with a message that names the secret and the PAT permissions it needs. No partial work happens and no PR is opened (Task 6: preflight step plus actionlint; manual item M5).
3. **No dependency updates available:** the deps workflow ends green and opens no empty PR. **An open `deps/go-*` PR already exists:** it skips with a notice instead of stacking duplicates (Task 6).
4. **setup-go picks 1.27.0, or a later `go get` silently raises the `toolchain` line:** the assert script fails the job and names both versions (Task 1 tests the mismatch message).
5. **A multi-arch Docker build on arm64:** the protoc checksum is per-arch, and an unknown `TARGETARCH` fails loudly instead of installing a wrong binary (Task 5: both platform builds).

---

### Task 0: Worktree and baseline

**Files:** none.

- [ ] **Step 1:** `git -C D:\Projects\vcs-srs-server fetch origin && git -C D:\Projects\vcs-srs-server worktree add ..\vcs-srs-server-sv5 -b chore/cleanup-sv5 origin/main`. Run every later command from that worktree.
- [ ] **Step 2: Generate code locally.**
  - Run the three `protoc` commands from `build/Taskfile.yml:106-108`, then `go run github.com/99designs/gqlgen@$(go list -m -f '{{.Version}}' github.com/99designs/gqlgen) generate`.
  - Use the local protoc and `protoc-gen-go-grpc@v1.6.2`.
- [ ] **Step 3: Baseline.**
  - Run `go build -tags headless ./...` and `go test -race -count=1 -tags headless ./...`. Expected: all `ok`.
  - Run `go vet -tags headless ./...`. Expected: the single `srs_service.go:590` finding.
  - Record the output verbatim in the task report.

### Task 1: Toolchain assert script + Go 1.27.3

**GATE — needs user approval before this task (dependency):** `go.mod` `go 1.25.0` → `go 1.27.0` + `toolchain go1.27.3`.

**Files:**
- Create: `.github/scripts/assert-go-toolchain.sh`, `.github/scripts/assert-go-toolchain_test.sh`
- Modify: `go.mod:3` (and `go.sum` if tidy changes it)

**Interfaces:**
- Produces: `bash .github/scripts/assert-go-toolchain.sh [path/to/go.mod]` (default `./go.mod`).
  - Exits 0 when `go env GOVERSION` equals the `go.mod` `Toolchain` (from `go mod edit -json <file> | jq -r '.Toolchain // empty'`).
  - Exit 1, emitting `::error title=Go toolchain mismatch::want <X>, got <Y>`, when they differ.
  - Exit 1, emitting `::error title=No toolchain directive::…`, when the directive is absent.
  - Tasks 4, 5 and 6 call it.

- [ ] **Step 1: Write the failing test** `.github/scripts/assert-go-toolchain_test.sh` (bash, `set -euo pipefail`, temp dir via `mktemp -d`, fixture go.mod files written inline). Cases:
  - Fixture with no `toolchain` line → exit ≠ 0, and stdout contains `No toolchain directive`.
  - Fixture with `toolchain go0.0.1` → exit ≠ 0, and stdout contains `want go0.0.1, got $(go env GOVERSION)`.
  - Fixture with `toolchain $(go env GOVERSION)` → exit 0.

  Print `PASS`/`FAIL <case>` per case, and exit non-zero on any FAIL.
- [ ] **Step 2:** `bash .github/scripts/assert-go-toolchain_test.sh`. Expected: FAIL (script missing).
- [ ] **Step 3:** Implement `assert-go-toolchain.sh` per the Interfaces block.
- [ ] **Step 4:** Re-run the test. Expected: all PASS. Then run `bash .github/scripts/assert-go-toolchain.sh` against the **unchanged** repo go.mod. Expected: exit 1, `No toolchain directive`. This is the red state the bump fixes.
- [ ] **Step 5: Bump.** `go mod edit -go=1.27.0 -toolchain=go1.27.3`, then `GOTOOLCHAIN=go1.27.3 go mod tidy`. `git diff go.mod` must show only the `go`/`toolchain` lines. Report any `go.sum` churn verbatim.
- [ ] **Step 6: Verify under 1.27.3.** With `GOTOOLCHAIN=go1.27.3`:
  - `bash .github/scripts/assert-go-toolchain.sh` → exit 0;
  - `go build -tags headless ./...` → ok;
  - `go vet -tags headless ./...` → only the `srs_service.go:590` finding;
  - `go test -race -count=1 -tags headless ./...` → all ok.
- [ ] **Step 7: Commit.** `git add .github/scripts/assert-go-toolchain.sh .github/scripts/assert-go-toolchain_test.sh go.mod go.sum && git commit -m "build: move to Go 1.27.0 / toolchain go1.27.3, add toolchain assert script"`

### Task 2: Portable dependency-update script

**Files:**
- Create: `.github/scripts/go-deps-update.sh`, `.github/scripts/go-deps-update_test.sh`

**Interfaces:**
- Produces: `bash .github/scripts/go-deps-update.sh --list`.
  - Prints one module path per line: the `Require[]` entries with `Indirect != true` from `go mod edit -json`, minus any path in the whitespace-separated env `GO_DEPS_EXCLUDE`.
  - Exact match only; no prefix matching.
  - Prints nothing when there are no requires.
- Produces: `bash .github/scripts/go-deps-update.sh` with no argument runs a single `go get <p1>@latest <p2>@latest …` over the `--list` set (it skips `go get` when the set is empty), then `go mod tidy`. It does not build, test, commit or touch git. Task 6 calls it.

- [ ] **Step 1: Write the failing test** `go-deps-update_test.sh`. It is offline: `--list` only, in a `mktemp -d` dir. Write this fixture go.mod:

```
module example.com/fixture

go 1.27.0

require (
	example.com/direct/a v1.0.0
	example.com/direct/b v1.0.0
	example.com/excluded v1.0.0
	example.com/excluded/v2 v2.0.0
)

require example.com/indirect v1.0.0 // indirect
```

Cases (run with `cd` into the fixture dir and the script path absolute):
- `GO_DEPS_EXCLUDE="example.com/excluded"` → output is exactly `example.com/direct/a`, `example.com/direct/b`, `example.com/excluded/v2`, in that order. The `/v2` line proves exact match only.
- `GO_DEPS_EXCLUDE=""` → 4 lines, and no `example.com/indirect`.
- A go.mod with no `require` block → empty output, exit 0.

- [ ] **Step 2:** `bash .github/scripts/go-deps-update_test.sh`. Expected: FAIL (script missing).
- [ ] **Step 3:** Implement `go-deps-update.sh` (`set -euo pipefail`; use `if … then … fi`, not `&& echo`, inside loops so `set -e` does not trip on the last line; use `mapfile` for the list).
- [ ] **Step 4:** Re-run. Expected: all PASS. Also run `GO_DEPS_EXCLUDE=github.com/wailsapp/wails/v3 bash .github/scripts/go-deps-update.sh --list` in the repo. Expected: the 12 direct requires of `go.mod`, minus wails.
- [ ] **Step 5: Commit.** `git add .github/scripts/go-deps-update.sh .github/scripts/go-deps-update_test.sh && git commit -m "ci: portable direct-dependency update script with offline test"`

### Task 3: Composite generate action (pinned generators)

**GATE — needs user approval before this task (ci-cd + dependency pins):**
- new `.github/actions/generate/action.yml`;
- pins protoc v36.2 (sha256 as in Global Constraints) and protoc-gen-go-grpc v1.6.2;
- protoc-gen-go and gqlgen derived from `go.mod`.

**Files:**
- Create: `.github/actions/generate/action.yml`
- Modify: `build/Taskfile.yml:98,100` (hint text only)

**Interfaces:**
- Produces: `uses: ./.github/actions/generate` (no inputs). Precondition: the repo is checked out and setup-go has run. Postcondition: `srspb/`, `voicecontrolpb/`, `vcsauthpb/` and `graphql/generated/` exist. Tasks 4, 5 and 6 use it.

- [ ] **Step 1: Red check.** Run `grep -rn '@latest' .github build/Taskfile.yml Dockerfile`. Expected: matches in `test.yml`, `release.yml`, `build/Taskfile.yml:98,100` and `Dockerfile`. These are the drift CI-10 removes. Keep the output; Task 9 re-runs it and expects nothing.
- [ ] **Step 2: Write `action.yml`** (`runs.using: composite`, every step `shell: bash`). Steps:
  1. A guard that fails with `::error::generate action supports Linux X64 runners only` unless `runner.os == 'Linux' && runner.arch == 'X64'`.
  2. Install protoc. Copy the client's `test.yml` "Install protoc · Linux" commands (`curl -fsSL --proto '=https' --tlsv1.2`, `sha256sum -c -`, `sudo unzip -o protoc.zip -d /usr/local bin/protoc 'include/*'`). Then `protoc --version` must match `grep -Eq ' 36\.2(\.0)?$'`; otherwise fail.
  3. Install the plugins. Carry over the client's comment explaining why `protoc-gen-go-grpc` cannot be derived from `go.mod`.
  4. Generate: `mkdir -p` the three dirs, run the three `protoc` lines exactly as `test.yml:36-38` today, then the gqlgen `go run`.

  Header comment: "Second copy: Dockerfile. Keep pins in step."
- [ ] **Step 3: Taskfile hints.** At `build/Taskfile.yml:98`, change the msg to `'go install google.golang.org/protobuf/cmd/protoc-gen-go@<version of google.golang.org/protobuf in go.mod>'`. At `:100`, change it to `'go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2'`.
- [ ] **Step 4: Lint.** `go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12` → no output. (Composite actions are not linted by actionlint. Also run `python -c "import yaml,sys;yaml.safe_load(open('.github/actions/generate/action.yml'))"`, or an equivalent YAML parse, and expect no error.)
- [ ] **Step 5: Commit.** `git add .github/actions/generate/action.yml build/Taskfile.yml && git commit -m "ci: pinned protoc/plugin/gqlgen generation as a composite action"`

### Task 4: `test.yml`: safe triggers, least privilege, `-race`, OIDC Codecov

**GATE — needs user approval before this task (ci-cd + secret retirement):**
- `pull_request_target` → `pull_request` + `push: [main]`;
- `permissions: contents: read`;
- removal of the Wails install and of the third-party `ConorMacBride/install-package` action;
- Codecov switches to OIDC, so `CODECOV_TOKEN` stops being used. The user rotates or deletes it after M2.

**Files:**
- Modify: `.github/workflows/test.yml` (full rewrite of the `test` job; `frontend-test` only loses `with: ref:`)

**Interfaces:**
- Consumes: `./.github/actions/generate` (Task 3); `assert-go-toolchain.sh` (Task 1); `go-deps-update_test.sh`, `assert-go-toolchain_test.sh` (Tasks 1–2).

- [ ] **Step 1: Red check.** Each of these greps against the current file must **match** (they are the defects):
  - `grep -n 'pull_request_target' .github/workflows/test.yml`
  - `grep -n 'secrets.CODECOV_TOKEN' .github/workflows/test.yml`
  - `grep -n 'head.sha' .github/workflows/test.yml`
  - `grep -n 'wails3' .github/workflows/test.yml`
  - `grep -L 'permissions:' .github/workflows/test.yml`
  - `grep -L -- '-race' .github/workflows/test.yml`
- [ ] **Step 2: Rewrite.**
  - `on: { pull_request: {}, push: { branches: [main] } }`; top-level `permissions: contents: read`.
  - Job `test` has `permissions: { contents: read, id-token: write }`.
  - Steps:
    1. `actions/checkout@v7`, no `with.ref`.
    2. `actions/setup-go@v7` with `go-version-file: go.mod`.
    3. `bash .github/scripts/assert-go-toolchain.sh`.
    4. `./.github/actions/generate`.
    5. "Test CI scripts": `bash .github/scripts/assert-go-toolchain_test.sh && bash .github/scripts/go-deps-update_test.sh`.
    6. `go test -race -count=1 -tags headless -coverprofile=coverage.out ./...`.
    7. Codecov with `if: github.event_name == 'push' || (github.event.pull_request.head.repo.full_name == github.repository && github.actor != 'dependabot[bot]')`, the same action SHA (`fb8b3582…` with a `# v7.0.0` comment), `files: coverage.out`, `fail_ci_if_error: true`, `use_pypi: true` (keep its comment) and `use_oidc: true`. There is **no** `token:`.

      Comment: fork/Dependabot runs get no OIDC token; the upload is skipped for them, not failed.
  - `frontend-test`: drop `with: ref:`; add job `permissions: contents: read`.
- [ ] **Step 3: Green check.**
  - Every Step 1 grep now returns nothing. For the two `-L` greps, the file is no longer listed.
  - `grep -c 'use_oidc: true' .github/workflows/test.yml` → `1`.
  - `go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12` → no output.
- [ ] **Step 4: Commit.** `git commit -am "ci(test): pull_request+push triggers, read-only token, -race, OIDC Codecov, drop unused Wails/GTK install"`

### Task 5: `release.yml` test job + Dockerfile

**GATE — needs user approval before this task (ci-cd + dependency):**
- `release.yml` top-level `permissions: contents: read`, with Wails and apt removed;
- Dockerfile `golang:1.27` → `golang:1.27.3`;
- protoc changes from apt to a pinned v36.2 zip with per-arch sha256;
- `unzip` is added via apt in the builder stage.

**Files:**
- Modify: `.github/workflows/release.yml:1-41`, `Dockerfile:1-26`

**Interfaces:**
- Consumes: Task 1's script and Task 3's action.

- [ ] **Step 1: Red check.**
  - `grep -n "go-version: '1.25'\|wails3\|libgtk" .github/workflows/release.yml` matches.
  - `grep -n '@latest\|protobuf-compiler\|gqlgen@v0' Dockerfile` matches.
- [ ] **Step 2: Edit `release.yml`.**
  - Add top-level `permissions: contents: read`.
  - The `test` job becomes: checkout → setup-go (`go-version-file: go.mod`) → assert script → `./.github/actions/generate` → `go test -race -count=1 -tags headless ./...`.
  - `docker` and `release` jobs: unchanged.
- [ ] **Step 3: Edit `Dockerfile` (builder stage).**
  1. `FROM golang:1.27.3 AS builder`, `ARG TARGETARCH`.
  2. One `RUN` installs `unzip` (apt, `--no-install-recommends`, then clean the lists). A `case "$TARGETARCH"` selects `amd64` → `linux-x86_64` + its sha or `arm64` → `linux-aarch_64` + its sha. `*)` → `echo "unsupported TARGETARCH=$TARGETARCH" >&2; exit 1`. Then curl, `sha256sum -c -`, unzip to `/usr/local` and remove the zip.
  3. `WORKDIR /app`, `COPY go.mod go.sum ./`, `RUN go mod download`.
  4. `RUN go install …protoc-gen-go@$(go list -m -f '{{.Version}}' google.golang.org/protobuf) && go install …protoc-gen-go-grpc@v1.6.2`.
  5. `COPY . .`, then the existing protoc `RUN` (unchanged).
  6. `RUN go run github.com/99designs/gqlgen@$(go list -m -f '{{.Version}}' github.com/99designs/gqlgen) generate`.
  7. The final `go build` and runtime stage stay unchanged.

  Comment at the protoc step: "Keep in step with .github/actions/generate/action.yml".
- [ ] **Step 4: Verify.**
  - The Step 1 greps return nothing.
  - actionlint → no output.
  - `docker build --platform linux/amd64 -t sv5-amd64 .` → success.
  - `docker build --platform linux/arm64 -t sv5-arm64 .` → success. This needs Docker Desktop's QEMU binfmt. If it is unavailable, report that verbatim and leave the arm64 build to manual item M4. Do not mark it passed.
  - `docker run --rm sv5-amd64 --help` (or check the image entrypoint exists via `docker image inspect`). It must not error at exec.
- [ ] **Step 5: Commit.** `git add .github/workflows/release.yml Dockerfile && git commit -m "build: pin Go 1.27.3, protoc v36.2 and plugins in release CI and Dockerfile"`

### Task 6: `go-deps.yml` workflow + Dependabot `gomod` removal

**GATE — needs user approval before this task (ci-cd + secret):**
- new scheduled workflow `.github/workflows/go-deps.yml` that pushes branches and opens PRs;
- removes the `gomod` entry from `.github/dependabot.yml`;
- needs the user to create secret **`DEPS_BOT_TOKEN`**: a fine-grained PAT (or GitHub App installation token), on `FPGSchiba/vcs-srs-server` only, with permissions Contents: Read and write, Pull requests: Read and write, Metadata: Read. The user creates and rotates it; the worker never does.

**Files:**
- Create: `.github/workflows/go-deps.yml`
- Modify: `.github/dependabot.yml:1-28`

**Interfaces:**
- Consumes: `go-deps-update.sh` (Task 2), `assert-go-toolchain.sh` (Task 1), `./.github/actions/generate` (Task 3).
- Produces: a workflow whose only repo-specific parts are `env.GO_DEPS_EXCLUDE`, `env.GO_BUILD_TAGS` and the generate step. CL5 copies the file and changes those three.

- [ ] **Step 1: Red check.**
  - `grep -n 'package-ecosystem: "gomod"' .github/dependabot.yml` → matches.
  - `test -f .github/workflows/go-deps.yml` → fails.
- [ ] **Step 2: Write `go-deps.yml`.**
  - `name: Go dependency updates`; `on: schedule: [{cron: '0 6 * * 1'}]`, `workflow_dispatch:`.
  - Top-level `permissions: contents: read`; `concurrency: { group: go-deps, cancel-in-progress: false }`.
  - `env: GO_DEPS_EXCLUDE: github.com/wailsapp/wails/v3`, with the comment "until track UP lands the Wails GA upgrade", and `GO_BUILD_TAGS: headless`.
  - Jobs:
    - `preflight`:
      - Its single step has `env: DEPS_BOT_TOKEN: ${{ secrets.DEPS_BOT_TOKEN }}`.
      - If the value is empty, it emits `::error title=DEPS_BOT_TOKEN missing::Create a fine-grained PAT or GitHub App token for this repository with Contents: read/write and Pull requests: read/write, and store it as the Actions secret DEPS_BOT_TOKEN. PRs opened with GITHUB_TOKEN would not run CI.` and exits 1.
    - `update` (`needs: preflight`, `permissions: contents: read`, **no secrets referenced**, `outputs.changed`):
      - checkout (`persist-credentials: false`) → setup-go (`go-version-file`) → assert script → generate action → `bash .github/scripts/go-deps-update.sh`.
      - If `git diff --quiet -- go.mod go.sum`, set `changed=false` and echo `No Go dependency updates.`.
      - Otherwise set `changed=true`, then run `go build -tags "$GO_BUILD_TAGS" ./...` and `go test -race -count=1 -tags "$GO_BUILD_TAGS" ./...`.
      - Write `pr-body.md` containing the `git diff -- go.mod` in a fenced block and the line `Tested in ${{ github.server_url }}/${{ github.repository }}/actions/runs/${{ github.run_id }}`.
      - `actions/upload-artifact@v4`: `go.mod`, `go.sum`, `pr-body.md`.
    - `open-pr`:
      - `needs: update`, `if: needs.update.outputs.changed == 'true'`, `permissions: contents: read`; env `GH_TOKEN: ${{ secrets.DEPS_BOT_TOKEN }}`.
      - It runs **no `go` command**, and its comment says why (D7).
      - Steps:
        1. checkout with `token: ${{ secrets.DEPS_BOT_TOKEN }}`.
        2. If `gh pr list --state open --json headRefName --jq '[.[]|select(.headRefName|startswith("deps/go-"))]|length'` is > 0, echo `::notice::An open deps/go-* PR already exists; skipping.` and stop the job successfully (step `id` + `if:` on the later steps).
        3. `actions/download-artifact@v4` into the workspace root, overwriting `go.mod`, `go.sum`.
        4. `git switch -c "deps/go-$(date -u +%F)"`, commit as `github-actions[bot]` with the message `chore(deps): weekly Go dependency update`, then `git push origin HEAD` (no `--force`).
        5. `gh pr create --base main --title "chore(deps): weekly Go dependency update ($(date -u +%F))" --body-file pr-body.md`.
- [ ] **Step 3: Edit `dependabot.yml`.**
  - Delete the `gomod` block (lines 15-28).
  - Replace the header text "backend (Go)" with a comment that Go modules are updated by `.github/workflows/go-deps.yml`, because Dependabot cannot resolve the gitignored generated packages (`srspb`, `voicecontrolpb`, `vcsauthpb`, `graphql/generated`).
  - npm, github-actions and docker stay unchanged.
- [ ] **Step 4: Verify.**
  - The Step 1 checks invert (no gomod match; the file exists).
  - actionlint → no output.
  - `grep -n 'secrets\.' .github/workflows/go-deps.yml` shows `DEPS_BOT_TOKEN` only, in `preflight` and `open-pr`, **never** under `update`. Check the line ranges by eye and paste them in the report.
  - `grep -n '\bgo \(build\|test\|get\|run\|mod\)' .github/workflows/go-deps.yml` shows no line inside `open-pr`.
- [ ] **Step 5: Commit.** `git add .github/workflows/go-deps.yml .github/dependabot.yml && git commit -m "ci: weekly Go dependency PR workflow (DEPS_BOT_TOKEN), drop broken gomod Dependabot"`

### Task 7: SRV-21: console-subsystem Windows headless build

**GATE — needs user approval before this task (user-visible: operators):** the Windows production headless exe changes from the GUI to the console subsystem. It opens a console, logs to it, and Ctrl+C works.

**Files:**
- Modify: `build/windows/Taskfile.yml:67,69`
- Test (throwaway, **not committed**): `<scratch>/pecheck/main.go`, where `<scratch>` is a temp dir outside the worktree.

- [ ] **Step 1: Write the throwaway checker.** `pecheck/main.go` (its own `go mod init pecheck`). It opens `os.Args[1]` with `debug/pe`, reads `OptionalHeader.(*pe.OptionalHeader64).Subsystem`, prints `subsystem=<n>`, and exits 1 unless n == 3 (`IMAGE_SUBSYSTEM_WINDOWS_CUI`).
- [ ] **Step 2: Red run.** With generated code present (Task 0):
  - `go run github.com/go-task/task/v3/cmd/task@v3.54.0 --dry windows:build:headless PRODUCTION=true` prints `windowsgui` and two `-tags` flags.
  - Then build for real: `go run github.com/go-task/task/v3/cmd/task@v3.54.0 windows:build:headless PRODUCTION=true` → `bin/vcs-server-headless.exe`.
  - `go run <scratch>/pecheck bin/vcs-server-headless.exe` → `subsystem=2`, exit 1.
  - `go version -m bin/vcs-server-headless.exe | grep -- -tags` → `-tags=headless` (production dropped).
- [ ] **Step 3: Edit.**
  - Line 67 → `go build {{.BUILD_FLAGS}} -o {{.OUTPUT}}`.
  - Line 69 → `'{{if eq .PRODUCTION "true"}}-tags production,headless -trimpath -buildvcs=false -ldflags="-w -s"{{else}}-tags headless -buildvcs=false -gcflags=all="-l"{{end}}'`.
  - Line 23 (the GUI `build`) is untouched.
- [ ] **Step 4: Green run.**
  - `--dry` shows no `windowsgui` and exactly one `-tags`.
  - Rebuild; pecheck → `subsystem=3`, exit 0.
  - `go version -m` → `-tags=production,headless`.
  - Also `--dry windows:build:headless` (dev) → contains `-tags headless`.
  - `--dry windows:build PRODUCTION=true` → still contains `-H windowsgui`.
- [ ] **Step 5: Commit.** `git add build/windows/Taskfile.yml && git commit -m "build(windows): headless target is a console binary; state build tags once"` (`bin/` is gitignored; confirm with `git status`.)

### Task 8: Full verification

**Files:** none.

- [ ] **Step 1:** In the worktree, with `GOTOOLCHAIN=go1.27.3` and freshly regenerated code:
  - `go build -tags headless ./...` → ok;
  - `go vet -tags headless ./...` → exactly the one `srs/srs_service.go:590:3: unreachable code` line (blocked on SV3; see Task 10);
  - `go test -race -count=1 -tags headless ./...` → all `ok`.

  Paste the output verbatim.
- [ ] **Step 2:**
  - `bash .github/scripts/assert-go-toolchain_test.sh && bash .github/scripts/go-deps-update_test.sh` → all PASS.
  - actionlint → clean.
  - `grep -rn '@latest\|CODECOV_TOKEN\|pull_request_target\|wails3@' .github Dockerfile build/Taskfile.yml` → no output.
- [ ] **Step 3:** Invoke `superpowers:verification-before-completion` and `superpowers:requesting-code-review` on the branch diff `origin/main...HEAD`. Fix confirmed findings in new commits.

### Task 9: Commit docs, open the PR, merge gate on CL5

**GATE — needs user approval before this task (cross-repo merge ordering):** the PR may be opened, but **merging waits until client track CL5 is merged** (D4). Merging is the user's/orchestrator's action, not the worker's.

**Files:**
- Create: `docs/superpowers/specs/2026-10-27-phase-6-9-sv5-server-ci-build-design.md` (a copy of the approved `design-SV5.md`, with Status changed to `approved`)
- Create: `docs/superpowers/plans/2026-10-27-phase-6-9-sv5-server-ci-build.md` (this plan)

- [ ] **Step 1:** Copy both docs in, then `git add docs/superpowers && git commit -m "docs(phase-6.9): SV5 server CI & build design and plan"`.
- [ ] **Step 2:** `git push -u origin chore/cleanup-sv5`.
- [ ] **Step 3:** `gh pr create --base main --title "Phase 6.9 SV5: server CI & build — safe triggers, Go 1.27.3, pinned generators, console headless exe, Go deps workflow"`. The body includes:
  - the findings closed (SRV-18, CI-02, CI-03 server half, CI-10, SRV-21, CI-06 server half, Go 1.27);
  - the **Behaviour changes** section (console exe; fork/Dependabot PRs get no Codecov upload; Go Dependabot replaced);
  - **User actions after merge**: create `DEPS_BOT_TOKEN`; rotate/delete `CODECOV_TOKEN` after M2; optionally set the default workflow permission to read;
  - **Do not merge before CL5** (with a link to the CL5 PR);
  - a note that the red check from the base branch's `pull_request_target` workflow is expected (risk R2);
  - the line `🤖 Generated with [Claude Code](https://claude.com/claude-code)`.

  End commit messages with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- [ ] **Step 4: Check the PR's CI.**
  - `gh pr checks` → the new `Test` (event `pull_request`) is green.
  - In its log, the "GITHUB_TOKEN Permissions" block shows `Contents: read`.
  - The setup-go and assert steps print `go1.27.3`.
  - The Codecov step ran (in-repo branch) and succeeded.

  Report the legacy `pull_request_target` failure verbatim, not as passing.
- [ ] **Step 5: Merge gate.** Before reporting "ready to merge":
  - `gh pr list -R FPGSchiba/vcs-srs-client --state merged --search "head:chore/cleanup-cl5"` returns the CL5 PR;
  - `git -C D:\Projects\vcs-srs-client show origin/main:.github/workflows/test.yml | grep -n 'repository: FPGSchiba/vcs-srs-server' -A3` shows a 40-hex `ref:`, not `main`.

  If either check fails, report "blocked on CL5".

### Task 10 (follow-up, **blocked on SV3**): `go vet` in CI

**Blocked on SV3:** start only after SV3's PR (which removes `srs/srs_service.go:590`) has merged to server `main`. Use a separate branch `chore/cleanup-sv5-vet` from the then-current `origin/main`, and open a separate PR.

**GATE — needs user approval before this task (ci-cd):** adds a required-to-pass `go vet` step to `test.yml` and to the `release.yml` test job.

**Files:**
- Modify: `.github/workflows/test.yml`, `.github/workflows/release.yml`

- [ ] **Step 1: Red/green precondition.** On the fresh branch, regenerate the code and run `go vet -tags headless ./...` → exit 0 with no output. If it still reports anything, stop: SV3 has not landed.
- [ ] **Step 2: Prove the step would catch a regression.** In a temp file `srs/vetprobe_test.go` containing an unreachable statement, `go vet -tags headless ./srs/` → exit 1. Delete the probe; it is never committed.
- [ ] **Step 3:** Add a step `go vet -tags headless ./...` right after the generate step, before the tests, in both workflows. actionlint → clean.
- [ ] **Step 4:** Run the full server suite (`go build`/`go vet`/`go test -race -count=1 -tags headless ./...`) → all green. Commit `ci: run go vet in test and release workflows (SRV-19 CI half)`, push, open the PR, and confirm its `Test` run is green and includes the vet step.

---

## Manual-verification items (for the orchestrator to merge into `docs/superpowers/plans/2026-10-27-phase-6-9-manual-verification.md`)

- **M1 (SV5):** after merge, a `push` run of `Test` appears on server `main` and is green; the setup-go step prints `go1.27.3`.
- **M2 (SV5):** that push run's Codecov OIDC upload succeeds and the Codecov dashboard shows the commit. Only then does the user rotate or delete the `CODECOV_TOKEN` secret. An in-repo PR run also uploads, and a Dependabot PR run shows the Codecov step as *skipped*, not failed.
- **M3 (SV5):** on Windows, run `bin/vcs-server-headless.exe --mode=standalone` from a production Taskfile build by double-click and from a terminal. A console window shows the log lines, and Ctrl+C shuts down cleanly.
- **M4 (SV5):** the next pre-release tag's `Release` workflow builds and pushes both `linux/amd64` and `linux/arm64` images. The tag push is confirmed by the user at that moment. Pull and start the arm64 image on an arm64 host (or under emulation) and confirm the server starts.
- **M5 (SV5):** before `DEPS_BOT_TOKEN` exists, a `workflow_dispatch` run of `Go dependency updates` fails at `preflight` with the "DEPS_BOT_TOKEN missing" message. After the user creates the secret, a second dispatch either ends green with "No Go dependency updates." or opens a `deps/go-YYYY-MM-DD` PR whose own `Test` run starts automatically. A third dispatch while that PR is open logs the "already exists; skipping" notice.
- **M6 (SV5 follow-up, after SV3):** the `Test` run on the vet follow-up PR shows the `go vet` step green.
