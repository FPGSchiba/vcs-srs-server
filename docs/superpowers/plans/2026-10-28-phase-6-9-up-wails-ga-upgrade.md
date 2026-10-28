# Phase 6.9 / UP: Wails v3 GA Upgrade Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move the client from Wails `v3.0.0-beta.22` and the server from `v3.0.0-alpha.43` to `v3.0.0` GA, with `@wailsio/runtime` pinned to exactly `3.0.0`. Derive the wails3 CLI version from go.mod instead of a literal pin. Fix the server's one typecheck break. Adopt the `0409` info.json key (U6). Add a weekly check in each repo that opens or updates one issue when a newer Wails exists (U7).

**Architecture:** There are two independent PRs, client first (D5).
- **Client:** go.mod/go.sum, package.json and the lock change, plus CI and `info.json`. There is zero Go or TS source change (D2). Buildcheck tests pin the pairing rule, the derived CLI, the `0409` key and the shape of the new workflow.
- **Server:** the same bump. Its only source change is `frontend/src/pages/ClientList.tsx`, and the rest is housekeeping that UP itself makes necessary (D3), plus U2 and U6.
- **U7, both repos:** a portable script, `.github/scripts/wails-update-check.sh`, shared verbatim between the repos like `go-deps-update.sh`. It has an offline bash test that stubs `curl` and `gh` on `PATH`, and a workflow, `.github/workflows/wails-check.yml`, that only calls the script.

**Tech Stack:** Go 1.27.3, Wails v3.0.0, `@wailsio/runtime` 3.0.0, buf v1.47.2 (client), protoc 36.2 and gqlgen in-module (server), React 19, TypeScript 7, Vitest, bash (3.2-safe), jq, `gh`, GitHub Actions.

**Spec:** the approved design, scratchpad `design-UP.md` (status: approved 2026-10-28, with D1–D7 and U1–U7). Client Task 6 commits it as `docs/superpowers/specs/2026-10-28-phase-6-9-up-wails-ga-upgrade-design.md`. Executors read §3 (the change table), §5 (plan shape), §6 (gates) and §9 (manual list). Umbrella: client `docs/superpowers/specs/2026-10-27-vcs-client-phase-6-9-cleanup-umbrella-design.md:240`.

**Planning bases:** client `origin/main` `e7c7c30`, server `origin/main` `09f9180`. All `file:line` citations are against these SHAs.

## Execution record (server copy)

This is the server repo's copy of the plan. The design spec lives in the **client** repo at `docs/superpowers/specs/2026-10-28-phase-6-9-up-wails-ga-upgrade-design.md`; the scratchpad `design-UP.md` references below are the draft it was committed from. Part 1 (client) is executed and recorded in the client repo; its checkboxes are left as written here. Part 2 checkboxes are ticked as executed on branch `chore/cleanup-up` (base `origin/main` `09f9180`).

- **Approvals at execution time (the user, 2026-10-28):** the design ("Looks good to me."), execution through a subagent-driven workflow, the conditional Dependabot `ignore` for `@wailsio/runtime` in both repos ("Yes, ignore it", so S1b ran), and one branch dispatch each of the client release build and `wails-check` during the PR step. The "Needs the user" items 1 and 2 below are therefore resolved; the Gates table rows marked "not approved" / "not covered" are superseded by these answers.
- **S2** gained a follow-up refactor commit (the undefined-entry filter hoisted out of JSX), **S3** a review-nit commit, and **S1b** a comment fix dropping the client-only test name.
- **S5 Step 1 result (2026-10-28, Windows, local):**
  - generate (3× protoc, gqlgen), headless build/vet, Linux cross-build: exit 0; `go test -race -tags headless`: 10 packages `ok`;
  - bindings: `Wails v3.0.0`, `Processed: 512 Packages, 5 Services, 25 Methods, 0 Enums, 24 Models, 0 Events`;
  - frontend: `Test Files 16 passed (16)`, `Tests 76 passed (76)`; tsc **112**, empty diff against the baseline set;
  - GUI build/vet: exit 0;
  - govulncheck (headless and GUI): `Your code is affected by 6 vulnerabilities from 2 modules.` (grpc v1.77.0, quic-go v0.54.0; SEC), govulncheck exit 3, down from 10 / 21;
  - script tests (`assert-go-toolchain`, `go-deps-update`, `wails-update-check`): pass;
  - `diff -r` of the bindings against the baseline: only the map types (`{ [_ in string]?: T }`), `LastUpdate: string` (default `"0001-01-01T00:00:00.000Z"`), `bindings/time` gone and `bindings/events/index.ts` added.
  - Local deviation: protoc on the executing machine was `libprotoc 36.0`, not 36.2 (CI pins 36.2). Generation and every Go step succeeded with it.
- Nothing here is hardware-verified. The manual list at the end of Part 2 is unrun.

## Global Constraints

- **Versions (D1):** `github.com/wailsapp/wails/v3 v3.0.0`, and `@wailsio/runtime` exactly `"3.0.0"` (no `^`; U1) in both `frontend/package.json` files. Never take `v3.1.0-nightly.*` or any pre-release.
- **R1 re-check:** before the dependency task in each part, list the versions (`curl -fsS https://proxy.golang.org/github.com/wailsapp/wails/v3/@v/list`).
  - If a stable `v3.0.x` above `v3.0.0` exists, **stop and report**. The user decides whether to re-probe and retarget.
  - If a stable `v3.1.0` or later exists, carry on with `v3.0.0` as approved. The U7 check is what surfaces it.
- **Codegen before `go mod tidy` (D7):** client `buf generate`; server 3× protoc + `go run github.com/99designs/gqlgen generate`.
- **Derived CLI (D4), exact form.** Use it in workflows:
  ```yaml
  - name: Install wails3 CLI (version from go.mod)
    shell: bash
    run: go install "github.com/wailsapp/wails/v3/cmd/wails3@$(go list -m -f '{{.Version}}' github.com/wailsapp/wails/v3)"
  ```
  Locally, every bindings run uses `WAILS_V="$(go list -m -f '{{.Version}}' github.com/wailsapp/wails/v3)"` and `go run "github.com/wailsapp/wails/v3/cmd/wails3@${WAILS_V}" …`, so a stray `wails3` on PATH can never be used by mistake.
- **No source change** in client Go/TS (D2). The server source change is limited to `ClientList.tsx` (D3). No Go change in the server.
- **Not regenerated:** `wails3 update build-assets` is **never** run, in either repo (design R7/A.3). `info.json` and `nfpm.yaml` are hand-edited.
- **Go-deps (U5):** keep `GO_DEPS_EXCLUDE: github.com/wailsapp/wails/v3` in both repos and reword only its comment. `TestGoDepsWorkflowShape` (`internal/buildcheck/buildcheck_test.go:487-506`) stays byte-identical.
- **Not adopted:** SingleInstance (U3), `private_mac_apis` (U4), a server GUI CI job (U2), or anything else in D6.
- **U7 workflow constraints:**
  - It runs on `schedule` with cron `'0 7 * * 1'` (Monday 07:00 UTC, one hour after go-deps) and on `workflow_dispatch`.
  - Top level: `permissions: contents: read`. Job level: `contents: read` and `issues: write`.
  - The only token is `GH_TOKEN: ${{ github.token }}`. There is no `secrets.` reference anywhere in the file.
  - The only action is `actions/checkout@v7` with `persist-credentials: false`.
  - `timeout-minutes: 5` on the job; `concurrency: group: wails-check`.
- **U7 issue contract (exact):**
  - label `wails-update` (colour `FBCA04`, description `A newer Wails release is available`);
  - title `Wails v<X> available`, where `<X>` is the higher of the latest stable Go and npm versions, without a `v`;
  - the body starts with the marker line `<!-- wails-update-check -->`;
  - only an **open** issue carrying the label is updated. When none exists, a new one is created.
  - When everything is up to date, the script makes no issue call and prints `Wails is up to date (go v<cur>, npm <cur>).`
- **U7 security source: the GitHub repository-advisory API for `wailsapp/wails`,** `gh api "repos/wailsapp/wails/security-advisories?state=published&per_page=100"`. Justification, verified 2026-10-28:
  - It needs no new dependency: `gh` and `jq` are preinstalled on the runners, and the token is `GITHUB_TOKEN`.
  - The **Go vuln DB has none of Wails' v3 advisories**: govulncheck shows none (design §3 #24, §7).
  - The **global** advisory DB (`/advisories?ecosystem=go&affects=github.com/wailsapp/wails/v3`) returns `0`: the v3 GHSAs are not reviewed there.
  - The repository endpoint returns all 9 published advisories, with `vulnerabilities[].package.name` and `patched_versions` (for example `GHSA-r7hq-45w4-mfcx`, `github.com/wailsapp/wails/v3`, `< 3.0.0-beta.31`, patched `3.0.0-beta.31`). It answers HTTP 200 unauthenticated, so it is public data.
  - If the lookup fails, for example because the token is refused, the issue is still written with `Security: unknown`. A failed advisory call never fails the run.
- **U7 security rule:** an advisory counts as "fixed by this release" when one of its `vulnerabilities[]` meets all of these:
  - `package.name` is `github.com/wailsapp/wails/v3` (compared against the Go versions) or `@wailsio/runtime` (compared against the npm versions);
  - the first `X.Y.Z[-pre]` token in `patched_versions`, P, satisfies `current < P <= latest`.
  - An entry for our package whose `patched_versions` has no such token is listed as "could not be evaluated" and makes the flag `unknown`, unless another advisory already made it `yes`.
- **Portable shell:**
  - The script and its test must run under bash 3.2 (the client's `TestCIScripts` runs on the macOS leg, `buildcheck_test.go:112-130`). That rules out `mapfile`/`readarray`, `${x,,}`, associative arrays, `local -n` and `sed -i`.
  - Use `sed -E`, not `sed -r`.
  - Strip `\r` from every jq output (`tr -d '\r'`), because jq in Git Bash emits CRLF (cf. `go-deps-update.sh:26`).
  - `curl` and `gh` are called by bare name, so the test's PATH stubs take effect.
- **gopls is unreliable here.** Trust only `go build`/`go vet`.
- **Commits** end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. **PR bodies** end with `🤖 Generated with [Claude Code](https://claude.com/claude-code)`. Never merge: the user merges (`gh pr merge` is blocked).

### Client verification suite (worktree root)

```bash
buf generate
mkdir -p frontend/dist && [ -f frontend/dist/index.html ] || echo '<!doctype html><title>ci</title>' > frontend/dist/index.html
WAILS_V="$(go list -m -f '{{.Version}}' github.com/wailsapp/wails/v3)"
go run "github.com/wailsapp/wails/v3/cmd/wails3@${WAILS_V}" generate bindings -ts -f "-tags purego" -clean=true
(cd frontend && npm ci && npm run build && npx vitest run && npx tsc --noEmit)
go build -tags purego ./...
go vet -tags purego ./...
go test -race -count=1 -tags purego ./...
go build -tags production,purego .
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 -tags purego ./...
bash .github/scripts/assert-go-toolchain_test.sh && bash .github/scripts/go-deps-update_test.sh   # Git Bash: TestCIScripts skips on Windows
```

Pass means:
- bindings print `Wails v3.0.0 › Generate Bindings. Processed: … 1 Service, 59 Methods, 1 Enum, 36 Models, 0 Events`;
- `npm run build` ends with `check-dist: dist/ contains index.html, main.html, comms.html, notifications.html`;
- `Test Files 51 passed`, plus whatever the branch adds;
- tsc prints nothing; build and vet print nothing; every package is `ok`;
- govulncheck prints `Your code is affected by 0 vulnerabilities.`, with the same 3 module-level findings as the baseline (GO-2026-6866, GO-2026-6860, GO-2026-5932).

From Task 4 on, also run `bash .github/scripts/wails-update-check_test.sh`.

### Server verification suite (worktree root; protoc 36.2 on PATH)

```bash
go install google.golang.org/protobuf/cmd/protoc-gen-go@$(go list -m -f '{{.Version}}' google.golang.org/protobuf)
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2
mkdir -p srspb voicecontrolpb vcsauthpb
protoc --go_out=./srspb --go_opt=paths=source_relative --go-grpc_out=./srspb --go-grpc_opt=paths=source_relative srs.proto
protoc --go_out=./voicecontrolpb --go_opt=paths=source_relative --go-grpc_out=./voicecontrolpb --go-grpc_opt=paths=source_relative control.proto
protoc --go_out=./vcsauthpb --go_opt=paths=source_relative --go-grpc_out=./vcsauthpb --go-grpc_opt=paths=source_relative vcs-auth-plugin.proto
go run github.com/99designs/gqlgen generate
go build -tags headless ./... && go vet -tags headless ./...
GOOS=linux CGO_ENABLED=0 go build -tags headless -o /dev/null .
go test -race -count=1 -tags headless ./...
WAILS_V="$(go list -m -f '{{.Version}}' github.com/wailsapp/wails/v3)"
go run "github.com/wailsapp/wails/v3/cmd/wails3@${WAILS_V}" generate bindings -clean=true -ts
(cd frontend && npm ci --legacy-peer-deps && npm run build && npm test)
(cd frontend && npx tsc --noEmit -p . 2>&1 | grep 'error TS' | sort > "$S/up-srv-tsc-<stage>.txt"; wc -l < "$S/up-srv-tsc-<stage>.txt")
go build ./... && go vet ./...                       # GUI tag set; needs the frontend/dist just built
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 -tags headless ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
bash .github/scripts/assert-go-toolchain_test.sh && bash .github/scripts/go-deps-update_test.sh
```

`$S` is the scratchpad directory. The generate block is `.github/actions/generate/action.yml:57-77`.

Pass means:
- generate exits 0; build and vet print nothing; the cross-build exits 0; 10 packages are `ok`;
- vitest reports `Test Files 16 passed`, plus the new case;
- the tsc count and set are exactly as the task states;
- **after the bump**, govulncheck reports `Your code is affected by 6 vulnerabilities from 2 modules` (grpc v1.77.0 and quic-go v0.54.0, which belong to SEC) for both tag sets, with exit code 3. That is expected and is not a failure.

From Task S4 on, also run `bash .github/scripts/wails-update-check_test.sh`.

## Gates

Every gate below was approved by the user with the design on **2026-10-28** ("Looks good to me."). The design status records the approval, and U1–U7 record the user's answers. Execution itself has **not** been authorised yet: no task starts until the user says to execute the plan.

| Gate | Where | Approval it rests on |
|---|---|---|
| dependency: client wails/v3 → v3.0.0, `@wailsio/runtime` → `3.0.0` | Task 1 | design §6.1 (approved 2026-10-28), U1 |
| dependency: server wails/v3 → v3.0.0 with the tidy deltas of §6.1, `@wailsio/runtime` → `3.0.0` | Task S1 | design §6.1, D7, U1 |
| ci-cd: derived wails3 CLI in client `test.yml`/`release.yml`, plus comment rewording | Task 2 | design §6.2, D4 |
| ci-cd: go-deps comment only, exclusion kept (both repos) | Tasks 2, S3 | U5 |
| ci-cd: server `action.yml:20` comment | Task S3 | design §6.2, D3 |
| ci-cd: new `wails-check.yml` + script in both repos; client `TestCIScripts` entry; server `test.yml:42` test step | Tasks 4, 5, S4 | U7 ("a CI change, approved with the design"). The `test.yml:42` addition is the server's existing way of running script tests, so it is part of making U7 testable. |
| packaging: server `nfpm.yaml` GTK4 names | Task S3 | U2 |
| build metadata: `info.json` `0000` → `0409` (both repos) and `buildcheck_test.go:206` | Tasks 3, S3 | U6 |
| behaviour accepted, no change: Linux second instance; opaque macOS window | none | U3, U4 |
| cross-repo: two independent PRs, client first | Parts 1 → 2 | D5, §6.3 |
| outward-facing: push, open PR | Tasks 7, S6 | Needs the user's go-ahead to execute. The user merges. |
| outward-facing (optional): branch `workflow_dispatch` of client `release.yml`; first `wails-check` dispatch after merge | Task 7 Step 6, manual list | **Not covered.** Ask the user at that moment. |
| **NOT approved:** a Dependabot `ignore` for `@wailsio/runtime` | Conditional Tasks 1b / S1b | Needs the user. See "Needs the user before execution". |

## Out of scope (D6, design §1)

- Adopting any new GA feature or template hardening:
  - GlobalShortcut, SingleInstance-as-feature, `exit_policy`, `dev_mode`;
  - the macOS `wails3 tool prepare-macos-output` / `rm -rf` / `xattr -cr` / `codesign --verify --deep --strict` Taskfile steps (#6225, #6237);
  - `wails3 update build-assets` regeneration.
- The post-GA nightly `v3.1.0-nightly.20261027` (#6275, #6284).
- A server `ServiceShutdown` that stops gRPC and voice listeners on SIGINT/SIGTERM (B21-01).
- The server frontend's ~112 pre-existing tsc errors, and any tsc gate in server CI.
- grpc and quic-go on the server, and x/net v0.62.0 on the client (SEC / weekly go-deps).
- A server Linux GUI CI job (U2 = A). "Server GUI never built in CI" is recorded as a follow-up.
- Automating the Wails bump itself (U5/U7): the issue only reports, and the bump stays manual.

## Review Focus

1. **The advisory lookup is refused for `GITHUB_TOKEN`, or rate-limited.**
   - *Expected:* the issue is still opened or updated, with `Security: unknown` and a link to the advisories page, and the run stays green.
   - *Test:* Task 4, `advisory-lookup-fails-still-reports`.
2. **The only version above the current one is the weekly nightly** (`v3.1.0-nightly.*`, npm `nightly` dist-tag).
   - *Expected:* no issue.
   - *Test:* Task 4, `nightly-ignored` (the proxy list contains `v3.1.0-nightly.20261027`).
3. **Numeric versus lexical ordering** (`v3.10.0` vs `v3.9.0`, `beta.8` vs `beta.22`).
   - *Expected:* semver order.
   - *Tests:* Task 4, `semver-numeric` and the alpha.43 security case (which needs `beta.8 < beta.31` and `alpha.43 < beta.8`).
4. **A second weekly run while the issue is still open, after a newer release.**
   - *Expected:* the same issue is edited (new title and body); there is no duplicate.
   - *Test:* Task 4, `existing-issue-updated`.
5. **A ClientList map value that is `undefined`** (type-level after GA, §3 #1).
   - *Expected:* the defined entries render and nothing crashes.
   - *Test:* Task S2, `skips undefined entries in the Clients map`.

Not covered by any test, and called out for the user: a weekly Dependabot npm group PR bumping `@wailsio/runtime` alone. See "Needs the user".

---

# Part 1: Client PR (`vcs-srs-client`, branch `chore/cleanup-up`)

### Task 0: Worktree and baseline

- [ ] **Step 1:** Create a fresh worktree on `chore/cleanup-up` from `origin/main` (superpowers:using-git-worktrees). Record `git log -1 --format=%h`, expected `e7c7c30` or later. If `origin/main` has moved, re-check that each `file:line` this part edits still matches; a mismatch is fixed by content, not by line number.
- [ ] **Step 2: R1 re-check** (Global Constraints). `curl -fsS https://proxy.golang.org/github.com/wailsapp/wails/v3/@v/list | grep -vE -- '-' | sort` and `npm view @wailsio/runtime dist-tags`. Expected: the stable Go versions top out at `v3.0.0`, and npm shows `latest: '3.0.0'`. A `v3.0.x` above `v3.0.0` means stop and report.
- [ ] **Step 3:** Run the client verification suite at baseline. The bindings line reads `Wails v3.0.0-beta.22 › … 59 Methods … 36 Models`. Save:
  - `cp -r frontend/bindings "$S/up-cl-bindings-before"`;
  - the govulncheck output, to `$S/up-cl-vuln-before.txt`.

  Everything must be green. If anything is red, stop and report it verbatim.

### Task 1: Pairing test + dependency bump (GATE: dependency, U1)

**Files:**
- Test: `internal/buildcheck/buildcheck_test.go` (new test after `TestDependabotHasNoGomod`, `:472-484`)
- Modify: `go.mod:13`, `go.sum:47-48`, `frontend/package.json:17`, `frontend/package-lock.json` (`:11`, `:1177-1181`)

**Interfaces:**
- Produces:
  - `func wailsGoVersion(t *testing.T) string`: it reads `go.mod`, matches `(?m)^\s*(?:require\s+)?github\.com/wailsapp/wails/v3\s+(v\S+)`, and calls `t.Fatal` when there is no match. It returns the version with its `v`, for example `v3.0.0`.
  - Task 2 reuses it.

- [ ] **Step 1: Write `TestWailsRuntimeMatchesGoModule`.** It sets `want := strings.TrimPrefix(wailsGoVersion(t), "v")`, then:
  - `frontend/package.json`: `dependencies["@wailsio/runtime"] == want`, exactly, with no `^` or `~` (U1). On failure: `package.json @wailsio/runtime = %q, want exactly %q (go.mod's Wails version; bump both by hand, U5)`.
  - `frontend/package-lock.json`: `packages[""].dependencies["@wailsio/runtime"] == want` and `packages["node_modules/@wailsio/runtime"].version == want`.

  Decode both files with `encoding/json` into minimal structs. Add a comment citing the v3.0.0 release-notes pairing rule (design §2).
- [ ] **Step 2: Run** `go test -tags purego ./internal/buildcheck/ -run TestWailsRuntimeMatchesGoModule -v`. Expected: FAIL with `"^3.0.0-beta.34"`, want exactly `"3.0.0-beta.22"`.
- [ ] **Step 3: Bump.** `buf generate` (D7) has already run in Task 0. Then:
  ```bash
  go get github.com/wailsapp/wails/v3@v3.0.0 && go mod tidy
  (cd frontend && npm install --save-exact @wailsio/runtime@3.0.0)
  ```
  The expected diff:
  - `go.mod:13` → `v3.0.0`, and no other require changes;
  - `go.sum`: `v3.0.0 h1:lBe89kFu23bVz1dc1H9v0srkHQgWsy7BAKpH4sLQuDw=` and `v3.0.0/go.mod h1:U3p3/QfSer2XxXM3XWhwis9O7LvvuNKN/AcGoEFD+K8=`;
  - `package.json:17` → `"3.0.0"`;
  - the lock entry → `3.0.0` with integrity `sha512-NkFJXYWD/4Y3TcHAqfIeST4oybtwv9dVWTmriZTMFbINYiQ2FB3Wr4MjsQ7o0BNynEj2GslmUisNejnixEmLVQ==`.

  Any other transitive change means stop and report.
- [ ] **Step 4: Run** the same test. Expected: PASS. Then `go mod tidy -diff` exits 0 (the release Taskfile's CI check).
- [ ] **Step 5: Regenerate the bindings** with the derived CLI (suite line 3–4). Expected: `Wails v3.0.0 › … 59 Methods, 1 Enum, 36 Models`. `diff -r "$S/up-cl-bindings-before" frontend/bindings` shows only `Only in frontend/bindings: events` (gitignored; §3 #3). Then `go test -tags purego ./internal/app/ -run TestBoundSurfaceIsTheFrontendAPI -v` passes.
- [ ] **Step 6: Commit** `chore(deps): Wails v3.0.0 GA and @wailsio/runtime 3.0.0 (exact pin, paired by test)`.

### Task 1b (CONDITIONAL, skip unless the user approved it): Dependabot npm `ignore` for `@wailsio/runtime`

Only if the user approves "Needs the user" item 1.
- Edit `.github/dependabot.yml:7-14`: add an `ignore: - dependency-name: "@wailsio/runtime"` entry to the npm block, with a comment: `moves only with go.mod's Wails (TestWailsRuntimeMatchesGoModule); wails-check.yml reports new releases`.
- Test first: extend `TestDependabotHasNoGomod` (`:472`) to require `dependency-name: "@wailsio/runtime"`. Watch it fail, apply the edit, watch it pass.
- Commit `ci(dependabot): leave @wailsio/runtime to the paired manual Wails bump`.

### Task 2: Derived wails3 CLI + comment rewording (GATE: ci-cd, D4, U5)

**Files:**
- Test: `internal/buildcheck/buildcheck_test.go` (new test next to Task 1's)
- Modify:
  - `.github/workflows/test.yml:324-325`;
  - `.github/workflows/release.yml:117-123` (comment), `:151-152` (install), `:154-159` (comment);
  - `.github/workflows/go-deps.yml:28` (comment only);
  - `docs/superpowers/specs/2026-10-27-phase-6-9-cl5-build-ci-release-design.md:156`.

**Interfaces:** Consumes `wailsGoVersion` (Task 1). It produces nothing new; the derived-install string is the Global Constraints snippet.

- [ ] **Step 1: Write `TestWailsCLIVersionIsDerivedFromGoMod`.** It scans every file from `workflowFiles(t)` (`:54`) plus `.github/actions/*/action.yml` (via `filepath.Glob(repoPath(t, ".github/actions/*/action.yml"))`) and checks:
  - no file matches `wails3@v[0-9]`. The message says `literal wails3 CLI pin; derive it from go.mod (D4)`;
  - every line containing `cmd/wails3@` contains `wails3@$(go list -m -f '{{.Version}}' github.com/wailsapp/wails/v3)`;
  - `.github/workflows/test.yml` and `.github/workflows/release.yml` each contain that derived form exactly once.
- [ ] **Step 2: Run** `go test -tags purego ./internal/buildcheck/ -run TestWailsCLIVersionIsDerivedFromGoMod -v`. Expected: FAIL, naming `test.yml` and `release.yml`.
- [ ] **Step 3: Edit the workflows.**
  - `test.yml:324-325` and `release.yml:151-152` become the Global Constraints snippet, keeping `shell: bash` explicit.
  - `release.yml:117-123`: replace "which Wails v3 beta.22 does not link against -- it wants GTK 4 and WebKitGTK 6.0" with "which Wails v3 does not link against (GTK 4 / WebKitGTK 6.0 is the GA default)". The rest of the paragraph stays.
  - `release.yml:154-159` becomes:
    ```
    # Wails downloads linuxdeploy from its rolling `continuous` release
    # unless the file already exists in the AppImage build dir (v3.0.0
    # internal/commands/appimage.go:116-119), so pre-seed a pinned, checksummed
    # copy. Since v3.0.0 Wails writes its own AppRun (appimage.go:122).
    # Still unpinnable (design R2, re-checked in UP): the AppImage runtime
    # linuxdeploy fetches itself, and Microsoft's WebView2 bootstrapper on Windows.
    ```
  - `go-deps.yml:28` becomes `# Wails moves only together with @wailsio/runtime (exact pin, TestWailsRuntimeMatchesGoModule): bump both by hand; wails-check.yml opens an issue when a newer release exists.` Line 29 is unchanged.
  - CL5 spec `:156`: drop "`AppRun` (AppImageKit `continuous`)," and replace "re-check in UP" with "re-checked in UP (2026-10-28): Wails v3.0.0 writes its own AppRun".
- [ ] **Step 4: Run** `go test -tags purego ./internal/buildcheck/ -v`. Expected: all PASS, including `TestReleaseWorkflowShape` (`:451`; its banned list is untouched), `TestGoDepsWorkflowShape` (unchanged) and `TestEveryJobHasTimeout`.
- [ ] **Step 5: Commit** `ci: derive the wails3 CLI from go.mod and drop stale beta.22 notes (D4, U5)`.

### Task 3: Windows info.json `0409` key (U6)

**Files:**
- Test: `internal/buildcheck/buildcheck_test.go:200-207` (`readWindowsInfo`)
- Modify: `build/windows/info.json:6`

- [ ] **Step 1: Change `readWindowsInfo`.**
  - It returns `wi.Info["0409"]`.
  - It calls `t.Fatalf("info.json has no \"0409\" (en-US) block: Explorer's Details tab shows nothing for \"0000\" (Wails #6210, U6)")` when that key is absent.
  - It calls `t.Errorf` if a `"0000"` key is still present.
- [ ] **Step 2: Run** `go test -tags purego ./internal/buildcheck/ -run 'TestWindowsAssetsRendered|TestVersionsAgree' -v`. Expected: FAIL, with the 0409 message.
- [ ] **Step 3: Edit** `build/windows/info.json:6`, changing `"0000"` to `"0409"`. No other change.
- [ ] **Step 4: Run** the same command. Expected: PASS. Optional evidence: `go run "github.com/wailsapp/wails/v3/cmd/wails3@${WAILS_V}" generate syso -arch amd64 -icon build/windows/icon.ico -manifest build/windows/wails.exe.manifest -info build/windows/info.json -out "$S/up.syso"` exits 0 (`build/Taskfile.yml:96`).
- [ ] **Step 5: Commit** `build(windows): use the en-US (0409) version-info block (U6)`.

### Task 4: U7 check script, test first

**Files:**
- Create: `.github/scripts/wails-update-check_test.sh`, `.github/scripts/wails-update-check.sh`

**Interfaces:**
- Produces the CLI: `bash .github/scripts/wails-update-check.sh [--dry-run]`, run from the repo root.
  - Env: `GITHUB_REPOSITORY` (required unless `--dry-run`), `GH_TOKEN` (used by `gh`), `WAILS_GO_MOD` (default `go.mod`), `WAILS_PACKAGE_JSON` (default `frontend/package.json`), and the optional `GITHUB_SERVER_URL`/`GITHUB_RUN_ID` for the run link.
  - `--dry-run` prints the title and body to stdout and makes no `gh issue`/`gh label` call. It still calls `gh api` for the advisories.
  - Exit 0 on no-op, created or updated. Non-zero if the proxy or npm fetch fails, if the Wails require is missing from go.mod, or if the `@wailsio/runtime` dependency is missing or is not a plain version (an optional leading `^`/`~`/`=` is accepted).
- External calls (stubbed in the test):
  - `curl -fsSL --proto '=https' --tlsv1.2 --retry 3 --max-time 30 https://proxy.golang.org/github.com/wailsapp/wails/v3/@v/list`;
  - `curl … -H 'Accept: application/vnd.npm.install-v1+json' 'https://registry.npmjs.org/@wailsio%2Fruntime'`, reading `."dist-tags".latest`;
  - `gh api "repos/wailsapp/wails/security-advisories?state=published&per_page=100"`;
  - `gh issue list --repo "$GITHUB_REPOSITORY" --label wails-update --state open --json number --jq '.[0].number // empty'`;
  - `gh label create wails-update --repo … --force --color FBCA04 --description "A newer Wails release is available"`;
  - `gh issue create --repo … --label wails-update --title … --body-file …`, or `gh issue edit <n> --repo … --title … --body-file …`.

- [ ] **Step 1: Write the test** in the `go-deps-update_test.sh` style: `set -euo pipefail`, a `check name got want` helper, `mktemp -d`, `trap`, and `exit "$fail"`. Build the fixtures with heredocs:
  - A `bin/curl` stub. It dispatches on the URL (its last argument): it serves `$fix/proxy-list` or `$fix/npm.json`, and it `exit 22`s when `$fix/curl-fail` exists.
  - A `bin/gh` stub:
    - `api` serves `$fix/advisories.json`, or `exit 1`s when `$fix/gh-api-fail` exists;
    - `issue list` prints `$fix/open-issue`, which is empty or a number;
    - `label create`, `issue create` and `issue edit` append their subcommand, `--title` value and target number to `$fix/gh-calls`, and copy the `--body-file` argument to `$fix/body`.
  - An `advisories.json` with the 9 real entries captured on 2026-10-28, keeping only `ghsa_id`, `html_url`, `severity`, `summary` and `vulnerabilities[]` (`package{ecosystem,name}`, `vulnerable_version_range`, `patched_versions`):
    - v3: `GHSA-vfcr-9mmf-hc6m`, `GHSA-c8qg-xhgr-798r`, `GHSA-vwx8-cwg4-xx23`, `GHSA-r7hq-45w4-mfcx` (all patched `3.0.0-beta.31`) and `GHSA-cv6g-2vcq-wcvx` (patched `3.0.0-beta.8`);
    - v2 only: `GHSA-cjw4-96p8-36wh`, `GHSA-3vcq-4frm-47ph`, `GHSA-qg59-r6hv-pvm6`, `GHSA-47hv-j4px-h3c9`.

  Each case writes a `go.mod` line `require github.com/wailsapp/wails/v3 <v>` and a `package.json` with the `@wailsio/runtime` spec into a case directory, then runs the script there with `PATH="$tmp/bin:$PATH" GITHUB_REPOSITORY=o/r`. Cases and assertions:
  - `client-beta22-opens`: go `v3.0.0-beta.22`, npm `^3.0.0-beta.34`; proxy list `v3.0.0-beta.8 v3.0.0-beta.22 v3.0.0-beta.31 v3.0.0 v3.1.0-nightly.20261027`; npm latest `3.0.0`; no open issue. Expected:
    - `gh-calls` is exactly `label create` then `issue create title=Wails v3.0.0 available`;
    - the body's first line is `<!-- wails-update-check -->`;
    - the body contains `https://github.com/wailsapp/wails/releases/tag/v3.0.0`, `https://github.com/wailsapp/wails/compare/v3.0.0-beta.22...v3.0.0` and `Security: YES`;
    - the body contains the four beta.31 GHSA ids, and contains neither `GHSA-cv6g-2vcq-wcvx` nor any v2-only id.
  - `server-alpha43-five`: go `v3.0.0-alpha.43`, npm `^3.0.0-beta.34`. Expected: the body contains all five v3 ids, including `GHSA-cv6g-2vcq-wcvx`.
  - `existing-issue-updated`: as the first case, with `open-issue` = `7`. Expected: `gh-calls` is exactly `issue edit 7 title=Wails v3.0.0 available`, with no `create`.
  - `up-to-date-noop`: go `v3.0.0`, npm `3.0.0`, latest `v3.0.0` / `3.0.0`. Expected: `gh-calls` is empty and stdout contains `Wails is up to date (go v3.0.0, npm 3.0.0).`
  - `nightly-ignored`: as the no-op case, with the proxy list `v3.0.0 v3.1.0-nightly.20261027` and npm latest `3.0.0`. Expected: no `gh-calls`.
  - `semver-numeric`: go `v3.9.0`, npm `3.9.0`; list `v3.9.0 v3.10.0 v3.2.0`; npm latest `3.10.0`. Expected: the title is `Wails v3.10.0 available`, and the body says `Security: no`.
  - `npm-only-behind`: go `v3.1.0`, npm `3.0.0`; latest `v3.1.0` / `3.1.0`. Expected: an issue is created, titled `Wails v3.1.0 available`, and the npm row shows `3.0.0` → `3.1.0`.
  - `advisory-lookup-fails-still-reports`: the first case plus `gh-api-fail`. Expected:
    - exit 0;
    - the issue is created;
    - the body contains `Security: unknown` and `https://github.com/wailsapp/wails/security/advisories`.
  - `proxy-fetch-fails`: `curl-fail` present. Expected: a non-zero exit, and `gh-calls` is empty.
  - `missing-require`: a go.mod without Wails. Expected: a non-zero exit, and stderr contains `github.com/wailsapp/wails/v3 not found in go.mod`.
  - `dry-run`: the first case with `--dry-run`. Expected: no `gh-calls`, and stdout contains `Wails v3.0.0 available`.
- [ ] **Step 2: Run** `bash .github/scripts/wails-update-check_test.sh` in Git Bash. Expected: FAIL (the script does not exist).
- [ ] **Step 3: Implement the script.** `set -euo pipefail`. Use a header comment in the `go-deps-update.sh` style: portable, shared verbatim with the server repo, and env-only configuration. Structure:
  - `go_current`: `sed -nE 's/^[[:space:]]*(require[[:space:]]+)?github\.com\/wailsapp\/wails\/v3[[:space:]]+(v[^[:space:]]+).*/\2/p' "$WAILS_GO_MOD" | head -n1`.
  - `npm_current`: `jq -r '.dependencies["@wailsio/runtime"] // empty'`, with a leading `^`, `~` or `=` stripped. The result must match `^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$`.
  - `go_latest`: the proxy list, kept only where it matches `^v[0-9]+\.[0-9]+\.[0-9]+$`, then the jq max by `semkey`.
  - `npm_latest`: `dist-tags.latest`. It is ignored (treated as `npm_current`) when it contains `-`.
  - Comparison and the security evaluation run in **one jq program**, fed `--arg gc … --arg gl … --arg nc … --arg nl …` and the advisories JSON (or `null` if `gh api` failed). It returns `{newer: bool, title, security: "yes"|"no"|"unknown", fixed: [{id,url,severity,range,patched}], unevaluated: [ids]}`. Its ordering key (verified on jq 1.8.1, and it sorts `alpha.43 < beta.8 < beta.22 < beta.31 < 3.0.0 < 3.1.0-nightly < 3.9.0 < 3.10.0`):
    ```jq
    def semkey: ltrimstr("v") | (index("-")) as $i
      | (if $i then .[:$i] else . end) as $core | (if $i then .[$i+1:] else null end) as $pre
      | ($core | split(".") | map(tonumber))
        + (if $pre == null then [1] else [0] + ($pre | split(".") | map(if test("^[0-9]+$") then tonumber else . end)) end);
    ```
  - The body is written to a temp file:
    1. the marker;
    2. a two-row table (`Go module github.com/wailsapp/wails/v3` and `npm @wailsio/runtime`, current → latest);
    3. the release-notes link and the compare link;
    4. the `Security: YES|no|unknown` line, with the fixed-advisory bullets (`[GHSA-…](html_url) (severity): range, fixed in P`) or the advisories link;
    5. the "How to bump" bullets: run codegen first, then `go get github.com/wailsapp/wails/v3@v<X> && go mod tidy`, then `cd frontend && npm install --save-exact @wailsio/runtime@<X>`; re-run the UP trial-bump checks (design §4); bump Go and npm in one PR (U5);
    6. the run link, when `GITHUB_RUN_ID` is set.
  - Issue handling follows the contract in Global Constraints. There is no auto-close: the bump PR says `Closes #N`.
- [ ] **Step 4: Run** the test. Expected: every case `PASS` and exit 0. Also run `bash .github/scripts/wails-update-check.sh --dry-run` against the real network from the worktree. Expected: after Task 1, `Wails is up to date (go v3.0.0, npm 3.0.0).`, unless a newer stable release exists, in which case it prints that title.
- [ ] **Step 5: Commit** `ci(wails-check): script that reports a newer Wails release, with offline tests (U7)`.

### Task 5: U7 workflow + shape test (GATE: ci-cd, U7)

**Files:**
- Create: `.github/workflows/wails-check.yml`
- Test: `internal/buildcheck/buildcheck_test.go`: add the new `_test.sh` to the `TestCIScripts` list (`:121`); add `TestWailsCheckWorkflowShape`.

- [ ] **Step 1: Write the tests.**
  - `TestCIScripts`: its slice gains `".github/scripts/wails-update-check_test.sh"`.
  - `TestWailsCheckWorkflowShape` reads `.github/workflows/wails-check.yml`.
    - It requires: `cron: '0 7 * * 1'`, `workflow_dispatch:`, `issues: write`, `GH_TOKEN: ${{ github.token }}`, `persist-credentials: false`, `bash .github/scripts/wails-update-check.sh`, `group: wails-check`.
    - It forbids `secrets.`, `pull_request` and `contents: write`.
    - Every `uses:` target (via `usesRe`, `:420`) must start with `actions/checkout@`.
- [ ] **Step 2: Run** `go test -tags purego ./internal/buildcheck/ -run 'TestWailsCheckWorkflowShape|TestEveryJobHasTimeout|TestWorkflowsLeastPrivilege' -v`. Expected: `TestWailsCheckWorkflowShape` FAILs (the file is missing).
- [ ] **Step 3: Create the workflow** per Global Constraints. It has one job, `check`, with `runs-on: ubuntu-latest`, `timeout-minutes: 5`, the job-level permissions, the checkout step and one script step. The header comment says:
  - it reports only and never bumps (U5);
  - it uses only `GITHUB_TOKEN`;
  - the advisory source and why (one line each).
- [ ] **Step 4: Run** `go test -tags purego ./internal/buildcheck/ -v`. Expected: all PASS. `TestWorkflowsLeastPrivilege` (`:426`) and `TestEveryJobHasTimeout` (`ci_hardening_test.go:48`) now cover the new file too. `TestCIScripts` skips on Windows; the CI Linux and macOS legs run it under bash 5 and bash 3.2.
- [ ] **Step 5: Commit** `ci: weekly Wails release check that opens or updates one wails-update issue (U7)`.

### Task 6: Full verification, checklist, docs

**Files:**
- Modify: `docs/superpowers/plans/2026-09-15-phase-3-manual-verification.md`: a new `### 14. Wails v3.0.0 GA (Phase 6.9 UP)` section inserted before `## Known, already-documented gaps…` (`:402`), holding the client manual items 1–6 below.
- Create: `docs/superpowers/specs/2026-10-28-phase-6-9-up-wails-ga-upgrade-design.md` (a copy of the approved `design-UP.md`) and `docs/superpowers/plans/2026-10-28-phase-6-9-up-wails-ga-upgrade.md` (this plan, with its checkboxes as executed).

- [ ] **Step 1:** Run the client verification suite (full). Expected: the pass criteria above. `diff "$S/up-cl-vuln-before.txt"` against the new govulncheck output shows the same findings. Also run `go test -tags purego -run 'TestWindowPagesMatchSources|TestViteBundleShapeIsServable|TestEmbeddedAssetsServeWindowPages' -v .`, which gives `--- PASS` ×3.
- [ ] **Step 2:** Run `bash .github/scripts/wails-update-check_test.sh` in Git Bash. Every case should `PASS`.
- [ ] **Step 3:** Write the checklist section and the two docs. Commit `docs(phase-6.9): UP Wails GA design, plan and manual checks`.

### Task 7: Review, push, PR, CI

- [ ] **Step 1:** Invoke superpowers:verification-before-completion, then superpowers:requesting-code-review on `origin/main..HEAD`. The review brief includes:
  - the Review Focus list;
  - "zero Go/TS source change (D2)";
  - "script must stay bash-3.2-safe and is shared verbatim with the server".

  Fix the findings in new commits, then re-run Task 6 Steps 1–2.
- [ ] **Step 2:** `git push -u origin chore/cleanup-up`.
- [ ] **Step 3:** `gh pr create --base main --title "Phase 6.9 UP (client): Wails v3.0.0 GA" --body-file "$S/up-client-pr-body.md"`. The body contains:
  - a summary with D1, D2 and D4;
  - the Decisions table (D1–D7, U1–U7);
  - the dependency delta (§6.1 client) and the CI delta (§6.2 client rows, plus `wails-check.yml`);
  - behaviour changes users will see: the Windows tray right-click menu now opens (§3 #4); Linux right-click no longer toggles the window (#5); the AppImage crash outside Debian/Ubuntu is fixed (#12); four Wails GHSAs are closed (#24);
  - "Security effect: none in govulncheck" (§7);
  - the U7 security-source choice and its justification;
  - R1, R2 and R4 (Linux/macOS cgo and packaging are covered only by CI legs and a release run);
  - the verification output (the suite, the bindings line, govulncheck);
  - the manual-verification items below;
  - follow-ups: the Dependabot `@wailsio/runtime` question (if still open); DOC hand-off for `docs/ROADMAP.md`/CLAUDE.md Wails mentions;
  - the attribution line.

  Do not merge.
- [ ] **Step 4: CI.** `gh pr checks --watch --interval 30`. Expected green:
  - `Backend (Go) · ubuntu-latest`, `· macos-latest`, `· windows-latest`. These legs are the first Linux and darwin cgo compiles against GA, and the first bash 3.2 run of the new script test.
  - `Frontend (React)`, which uses the derived CLI.

  On red: superpowers:systematic-debugging. Fix in a new commit and push. Never weaken a test.
- [ ] **Step 5:** Report the PR URL and the CI result to the user. The user merges. After the merge, re-run the suite on `main`.
- [ ] **Step 6 (optional; ask the user at that moment):** `gh workflow run release.yml --ref chore/cleanup-up`, then `gh run watch`. It builds NSIS, AppImage and the `.app` and publishes nothing, so it covers R4 before a tag.

### Manual verification: client (design §9; goes into the checklist in Task 6)

1. **Windows tray:**
   - right-clicking the tray icon opens Show VCS / Settings / Quit, and it works again with the icon in the overflow (`^`) flyout;
   - Show and Settings work (`internal/app/tray.go:52-58`);
   - **Quit** runs `ServiceShutdown` (`internal/app/app.go:301`), with no hang and no race with the signal handler.
2. **Linux tray:**
   - right-click opens only the menu, and the main window does not toggle;
   - left-click still toggles (`tray.go:37,62-73`);
   - the tooltip still does nothing (expected at 3.0.0).
3. **Close-to-tray:** closing the main window hides it and does not quit (`main.go:379-384`).
4. **Multi-window:** main, comms and notifications open, focus, resize and move, and restore their saved geometry (`windows_wails.go:39-52`).
5. **Release artefacts** from a tagged or dispatched run:
   - the NSIS installer installs and launches;
   - the AppImage launches on **Fedora or Arch** as well as Ubuntu;
   - the arm64 `.app` zip launches;
   - Explorer → Properties → Details on the `.exe` shows the product name and version (U6).
6. **U7, after merge (ask the user first, it may create an issue):** `gh workflow run wails-check.yml`. Then check:
   - the run is green and takes under 5 min;
   - if a newer stable Wails exists, exactly one open `wails-update` issue appears, and its `Security:` line is `YES`/`no`, not `unknown`. `unknown` means the `GITHUB_TOKEN` advisory read was refused: record it as a follow-up;
   - a second dispatch edits the same issue.

---

# Part 2: Server PR (`vcs-srs-server`, branch `chore/cleanup-up`)

Start only after the client PR is open, so the script can be copied in its reviewed form. The PRs are independent (D5): the server PR does not wait for the client merge.

### Task S0: Worktree and baseline

- [x] **Step 1:** Create a fresh worktree on `chore/cleanup-up` from `origin/main`, expected `09f9180` or later. Re-check the R1 condition (Global Constraints).
- [x] **Step 2:** Run the server verification suite at baseline. Save:
  - the tsc set as `$S/up-srv-tsc-before.txt`. Expected count: **112**; for example `ServerControl.tsx(8,26): error TS2307 … '@wailsio/runtime/types/events'`;
  - govulncheck: headless **10** in 3 modules; GUI **21** in 7 modules;
  - `cp -r frontend/bindings "$S/up-srv-bindings-before"`.

  Everything else must be green. If anything differs, stop and report it verbatim.

### Task S1: Dependency bump (GATE: dependency, D7, U1)

**Files:** `go.mod`, `go.sum`, `frontend/package.json:21`, `frontend/package-lock.json` (`:2800-2803` and the root `packages[""]` entry)

- [x] **Step 1:** The codegen has already run (S0). Run `go get github.com/wailsapp/wails/v3@v3.0.0 && go mod tidy`. Expected go.mod delta (design §6.1):
  - direct: wails/v3 → `v3.0.0`, x/crypto `v0.53.0` → `v0.57.0`, x/mod `v0.37.0` → `v0.41.0`;
  - indirect: x/net → `v0.60.0`, x/sys → `v0.48.0`, x/text → `v0.42.0`, x/tools → `v0.49.0`, x/sync → `v0.23.0`, godbus/dbus/v5 → `v5.2.2`;
  - 28 indirect modules removed, including go-git/go-billy/go-webview2/go-winloader/circl;
  - added: coder/websocket v1.8.14, kr/pretty, rogpeppe/go-internal, gopkg.in/check.v1;
  - grpc, protobuf, gqlgen, gqlparser and quic-go unchanged, and `tool github.com/99designs/gqlgen` kept.

  `go mod tidy -diff` then exits 0. Any other delta means stop and report.
- [x] **Step 2:** `(cd frontend && npm install --legacy-peer-deps --save-exact @wailsio/runtime@3.0.0)`. Expected: `package.json:21` → `"3.0.0"`, and `npm ls @wailsio/runtime` shows `3.0.0`.
- [x] **Step 3:** Re-run generate plus the headless build, vet and test, and the bindings. Expected bindings: `Processed: … 5 Services, 25 Methods, 0 Enums, 24 Models`. Then run tsc into `up-srv-tsc-bumped.txt`. Expected: **114**, and `diff` against `-before` shows exactly two added lines, `src/pages/ClientList.tsx(21,20): error TS2345` and `(39,24)` (§3 #1). vitest is still green.
- [x] **Step 4: Commit** `chore(deps): Wails v3.0.0 GA and @wailsio/runtime 3.0.0 (exact pin)`. The ClientList fix follows in S2. The branch is never pushed in between.

### Task S1b (CONDITIONAL, skip unless the user approved it): Dependabot npm `ignore` for `@wailsio/runtime`

Only if the user approves "Needs the user" item 1. Add `ignore: - dependency-name: "@wailsio/runtime"` to the npm block at `.github/dependabot.yml:21-33`, with the same comment as Task 1b. There is no test (the server has no workflow-shape tests). Verify with `grep -n '@wailsio/runtime' .github/dependabot.yml`. Commit `ci(dependabot): leave @wailsio/runtime to the paired manual Wails bump`.

### Task S2: ClientList typecheck fix, test first (D3, §3 #1)

**Files:**
- Test: `frontend/src/pages/__tests__/ClientList.test.tsx`: a new `it` **appended after `:63`**, so the line numbers of the pre-existing tsc errors in this file do not move.
- Modify: `frontend/src/pages/ClientList.tsx:11`, `:47`

- [x] **Step 1: Write the test.** `it('skips undefined entries in the Clients map', …)`:
  - It mocks `vi.mocked(GetClients).mockResolvedValue({ Clients: { ...mockClients.Clients, 'client-3': undefined } } as never)`. The `as never` keeps the test from adding a tsc error.
  - `render(<ClientListPage />)`.
  - `await waitFor(() => expect(screen.getByText(/Pilot One/)).toBeTruthy())`, then `expect(screen.getByText(/Pilot Two/)).toBeTruthy()` and `expect(screen.getAllByRole('button', { name: 'Ban' })).toHaveLength(2)`.
  - Use only vitest-native matchers (`toBeTruthy`, `toHaveLength`). `toBeInTheDocument` is one of the baseline's 101 test-file tsc errors and would grow the set.
- [x] **Step 2: Run** `cd frontend && npx vitest run src/pages/__tests__/ClientList.test.tsx`. Expected: the new case FAILs, because `ClientEntry` reads `client.Coalition` on `undefined` (`components/ClientEntry.tsx:21`). The other 5 still pass.
- [x] **Step 3: Fix.**
  - `:11` becomes `React.useState<Partial<Record<string, ClientState>> | null>(null)`.
  - `:47` becomes `Object.entries(clients).filter((e): e is [string, ClientState] => e[1] !== undefined).map(([key, client]) => (`.
  - `:21` and `:39` are unchanged: they typecheck once `:11` is widened.
- [x] **Step 4: Run** the vitest file. Expected: 6 PASS. Then `npm test`: 16 files pass. Then tsc into `up-srv-tsc-after.txt`. Expected: **112**, and `diff "$S/up-srv-tsc-before.txt" "$S/up-srv-tsc-after.txt"` is **empty**. Leave the existing mocks' `LastUpdate: null` alone: changing them would move the baseline set (§3 #2; vitest does not typecheck).
- [x] **Step 5: Commit** `fix(frontend): tolerate optional map values from Wails GA bindings in ClientList (§3 #1)`.

### Task S3: Housekeeping that UP makes necessary (D3, U2, U5, U6)

There is no automated test in the server for these files. Each one gets a grep or parse check.

**Files and exact edits:**
- `build/Taskfile.yml:74`: `npm run dev -- --port {{.VITE_PORT}} --strictPort --host 127.0.0.1` (§3 #20).
- `.github/actions/generate/action.yml:20-21` becomes `#   x/tools must be at least v0.46.0 (the lowest that works); go.mod currently has v0.49.0, the version Wails v3.0.0 requires. The` followed by the unchanged remainder ("`tool` directive keeps gqlgen's own deps in go.sum across `go mod tidy`."). Rewrap to the file's comment width.
- `README.md:9-14`:
  - drop "currently in alpha" and both `v3alpha.wails.io` links;
  - the new text says the project uses Wails v3 (link: `https://github.com/wailsapp/wails`), and that the wails3 CLI must match the version in go.mod;
  - the code block becomes `go install "github.com/wailsapp/wails/v3/cmd/wails3@$(go list -m -f '{{.Version}}' github.com/wailsapp/wails/v3)"`, with no `@latest` (§5.2 step 5).
  - Add one line: Linux GUI builds need `libgtk-4-dev` and `libwebkitgtk-6.0-dev` (GA default, U2).
- `build/linux/nfpm/nfpm.yaml:27-29` (U2) becomes:
  ```yaml
  # GTK4 + WebKitGTK 6.0, the Wails v3 GA default (v3.0.0 nfpm.yaml.tmpl).
  depends:
    - libgtk-4-1
    - libwebkitgtk-6.0-4

  overrides:
    rpm:
      depends:
        - gtk4
        - webkitgtk6.0
    archlinux:
      depends:
        - gtk4
        - webkitgtk-6.0
  ```
  The commented example block at `:35-37` and everything else are unchanged. Template placeholders stay (A.3).
- `build/windows/info.json:6`: `"0000"` → `"0409"` (U6). Placeholders stay.
- `.github/workflows/go-deps.yml:32` becomes `# Wails moves only together with @wailsio/runtime (exact pin in frontend/package.json): bump both by hand; wails-check.yml opens an issue when a newer release exists.` Line 33 is unchanged (U5).

- [x] **Step 1: Apply** the edits.
- [x] **Step 2: Check.**
  - `jq -e '.info["0409"].ProductVersion' build/windows/info.json` → `"0.1.0"`, and `jq -e '.info["0000"]'` → exit 1.
  - `python -c "import yaml,sys;d=yaml.safe_load(open('build/linux/nfpm/nfpm.yaml'));assert d['depends']==['libgtk-4-1','libwebkitgtk-6.0-4'] and d['overrides']['rpm']['depends']==['gtk4','webkitgtk6.0'] and d['overrides']['archlinux']['depends']==['gtk4','webkitgtk-6.0']"`. If PyYAML is not installed, use `grep -n` on the six names instead.
  - `grep -n -- '--host 127.0.0.1' build/Taskfile.yml`.
  - `grep -c 'GO_DEPS_EXCLUDE: github.com/wailsapp/wails/v3' .github/workflows/go-deps.yml` → `1`.
  - `git grep -n -E 'wails3@latest|v3alpha|v0\.46\.0 \(the lowest' -- README.md .github` → only the reworded "at least v0.46.0" line.
- [x] **Step 3:** Run the generate block, then `go vet -tags headless ./...`. This confirms the action comment edit did not break the YAML shape the step relies on. Nothing should be printed.
- [x] **Step 4: Commit** `chore: GA housekeeping: dev-loop --host, GTK4 nfpm deps, 0409 info key, derived wails3 install, stale comments (D3, U2, U5, U6)`.

### Task S4: U7 check in the server (GATE: ci-cd, U7)

**Files:**
- Create: `.github/scripts/wails-update-check_test.sh` and `.github/scripts/wails-update-check.sh`, **byte-identical** to the client PR's final versions (Task 4 after review); `.github/workflows/wails-check.yml`, identical to the client's.
- Modify: `.github/workflows/test.yml:41-42`. This is the server's existing way of testing workflow scripts: there are no Go workflow-shape tests here.

- [x] **Step 1: Test first.** Copy only the `_test.sh`, and change `test.yml:42` to `run: bash .github/scripts/assert-go-toolchain_test.sh && bash .github/scripts/go-deps-update_test.sh && bash .github/scripts/wails-update-check_test.sh`. Run `bash .github/scripts/wails-update-check_test.sh`. Expected: FAIL (the script is missing).
- [x] **Step 2:** Copy the script and the workflow.
  - `cmp` against the client worktree's copies must exit 0 for all three files.
  - The workflow header comment must not cite client-only names (`TestWailsRuntimeMatchesGoModule`). If the client's copy does, generalise the comment **in both repos** (amend the client PR in a new commit) so the files stay identical.
- [x] **Step 3:** Run the three script tests. Expected: all PASS. `bash .github/scripts/wails-update-check.sh --dry-run` against the network gives `Wails is up to date (go v3.0.0, npm 3.0.0).`, or the newer-release title. Then a shape check by grep, mirroring the client's `TestWailsCheckWorkflowShape`:
  - `grep -c` finds `issues: write`, `workflow_dispatch:`, `cron: '0 7 * * 1'`, `timeout-minutes: 5`, `GH_TOKEN: ${{ github.token }}` and `persist-credentials: false` once each;
  - `grep -c 'secrets\.'` returns 0.
- [x] **Step 4: Commit** `ci: weekly Wails release check that opens or updates one wails-update issue (U7)`.

### Task S5: Full verification and docs

**Files:** Create `docs/superpowers/plans/2026-10-28-phase-6-9-up-wails-ga-upgrade.md` (this plan, with its checkboxes as executed; it points to the client spec path). The server has no manual-verification checklist file, so the server manual items live in this plan copy and in the PR body.

- [x] **Step 1:** Run the server verification suite (full). Expected:
  - all green;
  - tsc **112**, with an empty diff against the baseline;
  - govulncheck **6 in 2 modules** for both tag sets (it was 10 headless / 21 GUI);
  - `diff -r "$S/up-srv-bindings-before" frontend/bindings` shows the expected shape changes only: the map types (`{ [_ in string]?: T }`), `LastUpdate` as `string`, `bindings/time` gone, and `bindings/events/index.ts` added.
- [x] **Step 2:** Commit `docs(phase-6.9): UP Wails GA plan (server)`.

### Task S6: Review, push, PR, CI

- [ ] **Step 1:** superpowers:verification-before-completion, then superpowers:requesting-code-review on `origin/main..HEAD`. The brief includes:
  - "Go source untouched";
  - "tsc set identical to baseline";
  - "script/workflow byte-identical to the client".

  Fix the findings in new commits and re-run S5 Step 1.
- [ ] **Step 2:** `git push -u origin chore/cleanup-up`, then `gh pr create --base main --title "Phase 6.9 UP (server): Wails v3.0.0 GA" --body-file "$S/up-server-pr-body.md"`. The body contains:
  - a summary;
  - the Decisions table;
  - the §6.1 server dependency delta;
  - the security effect (govulncheck 10/21 → 6, x/net and the GUI-only go-git/go-billy/x/text/circl findings cleared; grpc and quic-go stay with SEC; x/net overlap note R6; five Wails GHSAs closed);
  - the ClientList fix, with the tsc 112 → 114 → 112 evidence;
  - the semantic binding change (§3 #2, `LastUpdate` now a string);
  - the behaviour changes and the user's choices: #6 log stream now ordered; #7 SIGINT goes through Wails Quit; #8/U3 a second Linux instance fails on port bind; #9/U2 GTK4 packaging; #10/U4 an opaque macOS window; #20 dev-loop `--host`; #21 latent; #22 closing the window ends `wails3 dev`;
  - the CI delta (go-deps comment, `action.yml` comment, `wails-check.yml`, the `test.yml:42` step);
  - R2/R3: the server GUI is compiled by no CI job (follow-up);
  - the verification output;
  - the manual list below;
  - a link to the client PR;
  - the attribution line.

  Do not merge.
- [ ] **Step 3: CI.** `gh pr checks --watch --interval 30`. Expected `Go Test` (including the new script test step) and `Frontend Test` green. On red: superpowers:systematic-debugging, fix in a new commit. Report the PR URL and the result to the user.

### Manual verification: server GUI (design §9; local builds only)

7. **Linux:**
   - the GUI builds with `libgtk-4-dev`/`libwebkitgtk-6.0-dev` and launches (U2);
   - a second launch fails on port bind with an error, as accepted (U3);
   - the `.deb`, if built, installs with the new dependencies.
8. **macOS:** the admin window renders opaque (U4), with no traffic lights, and the header's own minimise and quit work (`Header.tsx:21-22`).
9. **Windows:**
   - dragging by the masked region works;
   - mixed-DPI monitors behave;
   - there is no click-through on the transparent frameless window;
   - Explorer → Details shows the version info (U6).
10. **Ctrl-C / SIGTERM** to the GUI prints "Quitting application..." and exits.
11. **Logs page** receives `logs/entry` in order under load.
12. **Dev loop (Windows):** `task dev` serves the admin UI with `--host 127.0.0.1` (§3 #20). Closing the window ends the dev session (§3 #22).
13. **U7, after merge (ask the user first):** as client item 6, run in the server repo.

---

## Needs the user before execution

1. **Dependabot vs the exact runtime pin (not in the approved gates).** Both repos' Dependabot npm config groups `"*"` weekly (client `.github/dependabot.yml:7-14`, server `:21-33`). Once Wails ships a stable 3.1.0, Dependabot will bump the exact `"3.0.0"` pin on its own. The design proposed an `ignore` only for Q5 = B, but under U1 = A with U5 = A:
   - in the **client**, every `frontend-deps` group PR goes red on `TestWailsRuntimeMatchesGoModule`, which blocks all other npm updates in that group until someone drops the runtime bump;
   - in the **server**, the group PR would silently pair runtime 3.1.0 with Go v3.0.0.

   Conditional Tasks 1b / S1b add `ignore: @wailsio/runtime` (a CI-config change). Approve them or decline them. If declined, the group PRs need a manual revert of that line each time.
2. **Go-ahead to execute** and to push and open both PRs. Execution is not authorised yet.
3. **The optional release `workflow_dispatch`** on the branch (Task 7 Step 6) and the **first `wails-check` dispatch** after each merge (manual items 6 and 13). Both are outward-facing; the dispatch may create a real issue.
4. **If a `v3.0.x` appears before execution** (R1), the user decides whether to retarget.
