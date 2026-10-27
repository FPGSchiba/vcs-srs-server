**Status: approved**

# Phase 6.9 SV5 — Server CI & build — design

Repo: `vcs-srs-server`, branch `chore/cleanup-sv5`, cut from `origin/main` @ c3096ce (after #223).
Umbrella: `docs/superpowers/specs/2026-10-27-vcs-client-phase-6-9-cleanup-umbrella-design.md` (client repo), §3 SV5 and §9.

## Decisions

| # | Decision | Value | By |
|---|---|---|---|
| D1 | Go version | `go 1.27.0` + `toolchain go1.27.3` in `go.mod`, the same as CL5. Workflows use `go-version-file: go.mod`. Dockerfile uses `golang:1.27.3`. | user (1.27, umbrella §9). The patch and the directive form are the worker's call, approved in the plan. |
| D2 | Go dependency updates (CI-06, Q1) | A weekly (Mon) + `workflow_dispatch` workflow regenerates the code, bumps the **direct** deps (Wails excluded until UP), tidies, builds, runs the race tests, and opens a PR using a fine-grained PAT or GitHub App token stored as the secret **`DEPS_BOT_TOKEN`**. The user creates and rotates it. If the secret is absent, the workflow fails with a clear message. The `gomod` Dependabot entry is removed. The workflow is portable, so CL5 reuses it. | user decision |
| D3 | Codecov (SRV-18, Q2) | Tokenless OIDC upload (`use_oidc: true`, `id-token: write` on the Go test job). The upload is skipped on fork and Dependabot PRs. `CODECOV_TOKEN` is no longer referenced. The user rotates or deletes it. | user decision |
| D4 | Ordering with the client | CL5 pins the client CI's server checkout to a SHA and **merges before** SV5's go.mod bump. SV5's PR must not merge until that is true. | orchestrator (umbrella §9) |
| D5 | `go vet` in CI (SRV-19, CI half) | A separate follow-up task, **blocked on SV3**: `srs/srs_service.go:590` makes vet red until SV3 removes it. | umbrella §3 |
| D6 | Generator pins | protoc **v36.2** from the upstream zip, with sha256 checks for amd64 and arm64. `protoc-gen-go` is derived from `go.mod`. `protoc-gen-go-grpc@v1.6.2`. gqlgen is derived from `go.mod`. One composite action serves CI. The Dockerfile mirrors it. | worker's call (it matches the client's existing pins) |
| D7 | Least-privilege deps workflow | Two jobs plus a preflight. The job that runs third-party code (generate/get/test) gets **no** secret and a read token. The job that holds `DEPS_BOT_TOKEN` runs no Go code: it applies `go.mod`/`go.sum` from an artifact, commits, pushes a dated branch and runs `gh pr create`. | worker's call |

## 1. Intent

Before any other server track opens a PR, make the server CI green, safe and reproducible:

- no secrets in scope for PR-head code;
- tests run on every push to `main`, with `-race`;
- one Go version (1.27.3) everywhere;
- pinned code generators;
- a console-subsystem Windows headless binary;
- a Go dependency-update path that works with gitignored generated code (the same mechanism as CL5).

## 2. Re-verification (against origin/main c3096ce, 2026-10-27)

| ID | Status | Evidence |
|---|---|---|
| SRV-18 | holds | `test.yml:2-3` is `on: pull_request_target:`. `:11-13` checks out `github.event.pull_request.head.sha`. `:56` passes `token: ${{ secrets.CODECOV_TOKEN }}`. There is no `permissions:` key. **Worse than recorded:** the repo's default workflow token is `write` (`default_workflow_permissions: write`), so PR-head code ran with a write token *and* the Codecov secret. Dependabot PR #219 got a Codecov comment. `release.yml`'s `test` job also has no `permissions` key, so it inherits write. |
| CI-02 | holds | `test.yml:25-26` and `release.yml:22-23` run `go install .../wails3@latest`. Both set `go-version: '1.25'`. Run 40504855685 fails with `wails/v3@v3.0.0 requires go >= 1.26.0`. Nothing consumes wails3. The apt GTK4/WebKitGTK/libasound packages are unused too: the headless dependency graph has no cgo besides `net`/`runtime/cgo`. gcc (preinstalled) is needed only for `-race`. |
| CI-03 (server half) | holds | Only `pull_request_target`; there is no `push` trigger. |
| CI-10 | holds | `protoc-gen-go@latest`/`-grpc@latest` in `test.yml:30-31`, `release.yml:27-28` and `Dockerfile:5-6`. protoc comes from apt. `Dockerfile:7` hardcodes `gqlgen@v0.17.89`. Three different Go versions appear: `go.mod` 1.25.0, the workflows `'1.25'`, the Dockerfile `golang:1.27`. The precondition hints at `build/Taskfile.yml:98,100` say `@latest`. |
| SRV-19 (CI half) | holds | `go vet -tags headless ./...` reports `srs/srs_service.go:590:3: unreachable code`. The code fix belongs to SV3. |
| SRV-21 | holds, with a correction | `build/windows/Taskfile.yml:69` has production `-ldflags="-w -s -H windowsgui"`. Logs go to stdout, so a GUI-subsystem exe drops every log line and has no Ctrl+C. **Correction:** line 67 appends a second `-tags headless`. The last `-tags` wins, so `production` is silently dropped. This is fixed on the same line. |
| CI-06 (server half) | holds, with a correction | Dependabot run 40478974327 fails with `cannot find module providing package …/srspb`, and the same for `graphql/generated`, `voicecontrolpb` and `vcsauthpb`. |
| CI-11 | already fixed | Fixed by #223. |

Baseline: on the exported origin/main with generated code, `go build` and `go test -race -count=1 -tags headless ./...` are green, both as-is and after `go mod edit -go=1.27.0 -toolchain=go1.27.3` under `GOTOOLCHAIN=go1.27.3`. go1.27.3 is the newest patch on go.dev today.

## 3. Approach per finding

### Go 1.27 (D1)
- `go.mod`: `go 1.27.0` + `toolchain go1.27.3`, then `go mod tidy` (only those lines are expected to change).
- A shared script, `.github/scripts/assert-go-toolchain.sh`, fails a job when `go env GOVERSION` ≠ the `toolchain` directive in `go.mod`. It runs in every Go job, which guards against setup-go silently picking 1.27.0.
- `Dockerfile`: `golang:1.27.3`.

### SRV-18 + CI-03 + CI-02: `test.yml`
- Triggers are `pull_request` and `push: branches: [main]`. Top-level `permissions: contents: read`. Checkout has no `ref:` override.
- Delete "Install Wails" and the `ConorMacBride/install-package` step.
- Code generation goes through `./.github/actions/generate`.
- `go test -race -count=1 -tags headless -coverprofile=coverage.out ./...`.
- Codecov (D3): `use_oidc: true`, no `token:`. Job permissions are `contents: read`, `id-token: write`. The upload runs `if: github.event_name == 'push' || (github.event.pull_request.head.repo.full_name == github.repository && github.actor != 'dependabot[bot]')`. The action stays at its current SHA (v7.0.0, which supports `use_oidc`). `use_pypi: true` and its comment stay.
- A new step runs the deps script's test (`bash .github/scripts/go-deps-update_test.sh`).
- `frontend-test`: only the `ref:` override is removed.

### CI-02: `release.yml`
- Top-level `permissions: contents: read`. The `docker`/`release` jobs keep their explicit job permissions.
- `go-version-file: go.mod`, the toolchain assert, the generate action, and `go test -race -count=1 -tags headless ./...`. Wails and the apt step are removed.

### CI-10: pinned generators (D6)
- New composite action `.github/actions/generate/action.yml` (Linux X64 only; it fails clearly on any other runner):
  - protoc v36.2 zip with sha256 `121f6c7afe1d4d0e3ea6aab9432038599250134cbf4474cb1167d2c7decd4278`, and a check that the version is 36.2;
  - `protoc-gen-go@$(go list -m … google.golang.org/protobuf)` and `protoc-gen-go-grpc@v1.6.2`;
  - the three `protoc` calls, then `go run gqlgen@<go.mod version> generate`.
- `Dockerfile`: protoc v36.2 chosen by `TARGETARCH`:
  - amd64: `linux-x86_64`, sha256 `121f6c7a…4278`;
  - arm64: `linux-aarch_64`, sha256 `8b8f18bd2b30346efbc698dd5a73dd7c805f3ef8380f6dfc95c768f3f1852f6a` (computed 2026-10-27 from the upstream release; closes former risk R5);
  - any other arch fails.

  Plugins are installed after `COPY go.mod go.sum` and derived from `go.mod`. gqlgen is run via `go run …@$(go list -m …)`. A comment names the composite action as the other copy to keep in step.
- `build/Taskfile.yml:98,100` hints: text only, with the pinned/derived install commands.

### SRV-19 (CI half) (D5)
- Not in the main SV5 changes. A final task, **blocked on SV3**, adds `go vet -tags headless ./...` to `test.yml` and to `release.yml`'s test job, as a separate follow-up PR after SV3 merges.

### SRV-21: `build/windows/Taskfile.yml`
- `build:headless`:
  - production flags become `-tags production,headless -trimpath -buildvcs=false -ldflags="-w -s"`;
  - dev flags become `-tags headless -buildvcs=false -gcflags=all="-l"`;
  - the `cmds` line drops its trailing `-tags headless`.
- The GUI `build` target keeps `-H windowsgui`.

### CI-06 (server half) (D2, D7)
- `.github/dependabot.yml`: remove the `gomod` entry. A comment points to `go-deps.yml`. npm, github-actions and docker stay.
- New `.github/scripts/go-deps-update.sh`. It is portable and has no repo-specific values.
  - `--list` prints the direct requirements from `go mod edit -json`, minus the space-separated `GO_DEPS_EXCLUDE`.
  - With no argument, it runs `go get <each>@latest` in one call, then `go mod tidy`.
  - Test: `.github/scripts/go-deps-update_test.sh` (fixture go.mod, offline).
- New `.github/workflows/go-deps.yml`:
  - Triggers: `schedule` (cron `0 6 * * 1`) and `workflow_dispatch`. Top-level `permissions: contents: read`. `concurrency: go-deps`.
  - Repo-specific values live in the workflow `env`: `GO_DEPS_EXCLUDE: github.com/wailsapp/wails/v3` (until UP) and `GO_BUILD_TAGS: headless`. Only the generate step and those values differ in the client copy (CL5).
  - `preflight`: fails with `::error title=DEPS_BOT_TOKEN missing::…` if the secret is empty. The message names the required fine-grained permissions: Contents RW, Pull requests RW, Metadata R, on this repo only.
  - `update` (no secrets, read token): checkout → setup-go → assert toolchain → generate → script → if `git diff --quiet -- go.mod go.sum`, set `changed=false` → `go build`, then `go test -race -count=1 -tags $GO_BUILD_TAGS ./...` → upload `go.mod`, `go.sum` and `pr-body.md` (which holds the go.mod diff and the run URL) as an artifact.
  - `open-pr` (needs `update`, only if `changed`; uses `DEPS_BOT_TOKEN`, runs no Go):
    1. checkout with the PAT;
    2. skip, with a notice, if an open PR with a `deps/go-` head already exists;
    3. download the artifact;
    4. commit to `deps/go-YYYY-MM-DD` (no force-push);
    5. `gh pr create --base main`.

    A PR opened by the PAT triggers the normal `pull_request` CI.

## 4. Files touched

Modified: `go.mod`, `go.sum` (if tidy changes it), `Dockerfile`, `.github/workflows/test.yml`, `.github/workflows/release.yml`, `.github/dependabot.yml`, `build/Taskfile.yml` (hint text), `build/windows/Taskfile.yml`.

New: `.github/actions/generate/action.yml`, `.github/scripts/assert-go-toolchain.sh`, `.github/scripts/assert-go-toolchain_test.sh`, `.github/scripts/go-deps-update.sh`, `.github/scripts/go-deps-update_test.sh`, `.github/workflows/go-deps.yml`.

Docs: `docs/superpowers/specs/2026-10-27-phase-6-9-sv5-server-ci-build-design.md` and `docs/superpowers/plans/2026-10-27-phase-6-9-sv5-server-ci-build.md`.

No Go source changes. No file deletions. Removing the `ConorMacBride/install-package` action reference and the `gomod` Dependabot entry are config removals.

## 5. Tests first

1. **Toolchain assert:** the script is run against today's go.mod (no `toolchain` line) and fails. After the bump, under `GOTOOLCHAIN=go1.27.3`, it passes.
2. **Deps script:** the fixture test fails (no script), then passes. It checks direct-only selection, exclusions, and an empty require block.
3. **SRV-21:** `task --dry windows:build:headless PRODUCTION=true` must not print `windowsgui` and must contain exactly one `-tags`. A throwaway `debug/pe` checker reports Subsystem 2 (GUI) on main and 3 (console) after the edit. `go version -m` shows `-tags=production,headless`.
4. **Workflows:** pinned `actionlint` (`go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12`) is clean. The PR's own `pull_request` run is green and shows `Contents: read`. After merge, a `push` run on `main` is green with a Codecov OIDC upload.
5. **Dockerfile:** `docker build --platform linux/amd64 .` and `--platform linux/arm64 .` succeed locally (Docker Desktop is present).
6. **go-deps:** after merge and once `DEPS_BOT_TOKEN` exists, one `workflow_dispatch` run either opens a PR whose `Test` run starts, or ends with "no updates". Before the secret exists, the run fails at preflight with the clear message (user-observed).

## 6. Out of scope
- The code half of SRV-19 → SV3.
- The duplicate `-tags` in `build/linux|darwin/Taskfile.yml` (harmless, outside SV5's file list) → noted for SV7/DOC.
- SHA-pinning the server's other third-party actions (CI-14 is client-only).
- The Wails upgrade and the `wails3` CLI version → UP.
- Closing the stale Dependabot PRs (#221 needs a rebase onto the new `test.yml`).
- govulncheck.

## 7. Behaviour changes
- **Operators:** the Windows production headless exe is a console binary. It opens a console, logs to it, and Ctrl+C works.
- **Contributors:** fork and Dependabot PRs get neither secrets, a write token, nor a Codecov upload. Go Dependabot PRs stop and are replaced by weekly `deps/go-*` PRs.
- The Docker image is built with Go 1.27.3. There is no runtime change and no wire change.

## 8. Approval gates (each appears in the plan)
- **dependency:** `go 1.27.0`/`toolchain go1.27.3`; `golang:1.27.3`; protoc v36.2; protoc-gen-go-grpc v1.6.2.
- **ci-cd:**
  - the `test.yml` rewrite;
  - the `release.yml` changes;
  - the new composite action;
  - the new `go-deps.yml`;
  - removal of the `gomod` Dependabot entry and of `ConorMacBride/install-package`;
  - the follow-up vet step.
- **secret (user action):** create the `DEPS_BOT_TOKEN` secret (fine-grained PAT or App token); rotate or delete `CODECOV_TOKEN` after the first green OIDC upload.
- **repo setting (optional, user):** switch the default workflow permission to read.
- **cross-repo:** do not merge before CL5 (D4).

## 9. Risks
- **R1 (cross-repo):** client CI builds server `main` with the client's Go. SV5 must merge after CL5's SHA pin (D4).
- **R2:** with `pull_request_target`, the SV5 PR also runs the *base* branch's `test.yml`, which fails red (the Wails install). This is expected. The ruleset requires no checks.
- **R3:** setup-go may pick the `go` line instead of `toolchain`. The assert script catches this. Fallback: make the directive `go 1.27.3`.
- **R4:** Codecov OIDC needs the Codecov GitHub app to know the repo. The first push to `main` proves it (manual item). If it fails, the user decides; the token is not silently restored.
- **R5:** `DEPS_BOT_TOKEN` is a long-lived credential. It is used only by a job that runs no third-party code (D7) and is scoped to this repo.
- **R6 (found during implementation, 2026-10-27; resolved 2026-10-27, see §10):** `go run github.com/99designs/gqlgen@v0.17.89 generate` fails under Go 1.27.3 with `internal error: package "github.com/vektah/gqlparser/v2/ast" without types was imported from "github.com/99designs/gqlgen/graphql/introspection"`. It succeeds under go1.26.5. The same gqlgen v0.17.89 rebuilt against `golang.org/x/tools` v0.52.0 (gqlgen pins v0.42.0) succeeds under go1.27.3, and its output builds. §2's baseline did not catch this because it reused code generated under the older Go. Until a dependency decision lands, every generate step on this branch (composite action, Dockerfile, `task generate:graphql`) fails under Go 1.27.3.

## 10. Amendment, 2026-10-27: R6 resolution and review fixes

User-approved on 2026-10-27. This supersedes the gqlgen half of D6 and the gqlgen invocations in §3.

- **gqlgen runs in-module.** Every generate path (the composite action, the Dockerfile, `build/Taskfile.yml` `generate:graphql`) now runs `go run github.com/99designs/gqlgen generate` with no `@version`, so gqlgen is built against *this* `go.mod`. `go run pkg@version` used gqlgen's own `go.mod`, which pins `golang.org/x/tools` v0.42.0. The `GQLGEN_VERSION` Taskfile variable is removed. gqlgen itself stays at v0.17.89.
- **`golang.org/x/tools` v0.42.0 → v0.46.0** (indirect). This is the lowest version that works. Bisected under go1.27.3: v0.43.0, v0.44.0 and v0.45.0 fail with the R6 error; v0.46.0, v0.47.0, v0.50.0, v0.51.0 and v0.52.0 succeed. Through MVS the bump also raises two direct requirements: `golang.org/x/crypto` v0.48.0 → v0.53.0 and `golang.org/x/mod` v0.33.0 → v0.37.0. It raises these indirect ones too: `x/net` v0.51.0 → v0.56.0, `x/sync` v0.20.0 → v0.21.0, `x/sys` v0.42.0 → v0.46.0 and `x/text` v0.34.0 → v0.38.0. These raises are the floor that x/tools v0.46.0 requires; none was chosen separately. They are smaller than the approved v0.52.0 would have forced (x/crypto v0.58.0).
- **`tool github.com/99designs/gqlgen` directive in `go.mod`.** gqlgen's command-line dependencies (`github.com/urfave/cli/v3` v3.7.0) must stay in `go.mod`/`go.sum` for the in-module `go run`. A plain `go get` is not enough: the next `go mod tidy` removes them, because no package in the module imports gqlgen's `main`. That includes the tidy run by `go-deps-update.sh`. The Go 1.24+ `tool` directive is the supported way to keep them across a tidy. `go mod tidy` is now a no-op.
- **go-deps.yml regenerates after the bump.** The first generate pass stays, because `go get`/`go mod tidy` must resolve the module's own generated packages. A second pass runs the composite action again after `go-deps-update.sh`, so the build and tests compile code from the *bumped* generators (protoc-gen-go and gqlgen are both derived from `go.mod`).
- **Unique deps branch.** The branch is `deps/go-YYYY-MM-DD-<run_id>`. A second run on the same day cannot collide with a leftover branch, and the job never force-pushes. The existing-PR check still matches the `deps/go-` prefix. This is the same scheme as the client's CL5.
- **Artifact actions SHA-pinned:** `actions/upload-artifact@cf430e0… # v7.0.2` and `actions/download-artifact@9000827… # v8.0.2`. They were `@v4` tags. These are the same majors as the client's CL5.
- **`go-deps-update.sh` uses `@upgrade`, not `@latest`.** `@latest` would downgrade a requirement pinned to a newer pre-release or pseudo-version. The script and its test are byte-identical to the client's CL5 copy, including the `update-uses-upgrade` case.
