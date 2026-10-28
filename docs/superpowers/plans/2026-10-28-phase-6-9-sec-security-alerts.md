# Phase 6.9 / SEC: GitHub Security Alerts Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Bring both repos to 0 reachable govulncheck findings and close every open Dependabot alert. Keep it that way with a blocking govulncheck CI step. Cap the bcrypt cost the server accepts (12) and compare outside the settings lock. Fix the Windows reserved-port flake in the client's real-server fixture. Make the code-level SonarCloud fixes.

**Architecture:** There are two lanes, one PR each. The server goes first (D10).
- **sec-server:**
  - Bcrypt cap in `utils` and `GuestLogin`, plus the compare moved outside the lock (B1, Q2 = A).
  - Go bumps: grpc, quic-go, x/net and the moves they force (Q1 = A, Q6 = A).
  - Remove `typed-scss-modules` (Q3 = A).
  - CI: the govulncheck step, `--ignore-scripts`, and SHA pins for the 7 actions in `release.yml` (Q4 = A).
  - Sonar housekeeping.
- **sec-client:**
  - The port-flake fix (Q5 = A).
  - S4036.
  - A cost guard test and a `PROTO_GAPS.md` note.
  - x/net v0.62.0.
  - CI: the govulncheck step on all three legs, and `--ignore-scripts`, both pinned by a buildcheck test.
  - The client server-pin bump is **not** in this lane. It is a post-merge follow-up (see the end).

**Tech Stack:** Go 1.27.3, grpc v1.85.0, quic-go v0.64.1, x/net v0.62.0, `golang.org/x/crypto/bcrypt`, govulncheck v1.9.0 (through `go run`), protoc 36.2 + gqlgen in-module (server), buf v1.47.2 (client), npm, GitHub Actions.

**Spec:** the approved design `design-SEC.md` (executor scratchpad; committed to the client repo as below), revision 2 plus "User decisions (2026-10-28)". Client Task C6 commits it as `docs/superpowers/specs/2026-10-28-phase-6-9-sec-security-alerts-design.md`. Executors read §3 (change list), §5 (plan shape), §6 (gates), §8 (manual list) and the User decisions. Umbrella: client `docs/superpowers/specs/2026-10-27-vcs-client-phase-6-9-cleanup-umbrella-design.md:269`.

**Planning bases:** client `origin/main` `b80b0e6`, server `origin/main` `df8d9f3`. All `file:line` citations are against these SHAs and were read with `git show origin/main:<path>`. If `origin/main` has moved at T0, fix each edit by its content, not by its line number.

## Global Constraints

- **Server versions (Q1 = A, Q6 = A):**
  - `google.golang.org/grpc v1.85.0`; `github.com/quic-go/quic-go v0.64.1`; `golang.org/x/net v0.62.0`.
  - Forced and accepted: protobuf `v1.36.12`, qpack `v0.6.0`, `google.golang.org/genproto/googleapis/rpc v0.0.0-20260817212433-ac3dfec99bb1`, x/crypto `v0.58.0`, x/sys `v0.49.0`, x/text `v0.43.0`, x/term `v0.47.0` (go.sum, or `// indirect`). The go.sum-only moves of §6.1 are also accepted.
  - **Any other go.mod delta means stop and report.** It would be an unapproved dependency.
- **Client versions:** only `golang.org/x/net v0.60.0 → v0.62.0`. A `go.sum` line for `golang.org/x/term v0.47.0/go.mod` may appear. Anything else means stop and report.
- **npm (Q3 = A):** remove the server dev dependency `typed-scss-modules` only. `vite-plugin-typed-css-modules` stays.
- **Codegen before `go mod tidy` (D3):**
  - server: 3× protoc + `go run github.com/99designs/gqlgen generate`, with protoc-gen-go re-installed at the go.mod protobuf version after the bump;
  - client: `buf generate`.
- **Bcrypt cap (Q2 = A), exact contract:**
  - `utils.MaxBcryptCost = 12`.
  - An unparseable hash, or a cost above 12, gets `GuestLoginResponse{Success:false, ErrorMessage: "Invalid password hash: " + err.Error()}` with a nil error.
  - `err.Error()` is exactly `malformed` or `bcrypt cost N exceeds the server maximum of 12`.
  - One `WARN` log carries `"IP", peerAddress`. The pending entry is **not** consumed.
  - The check runs after `checkUsername` (`srs/auth_service.go:476-481`) and before the coalition RLock (`:484`).
  - `utils.CheckPasswordHash` also returns false above the cap.
  - The coalition slice is copied under `settingsState.RLock` and compared outside it, through `var comparePassword = utils.CheckPasswordHash`.
  - No concurrency limit (Q2c deferred).
- **Client hashing is untouched:** `internal/session/session.go:744` stays `bcrypt.DefaultCost` (CLAUDE.md, "Password handling … do not fix").
- **govulncheck step, exact command:**
  - `go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 -tags headless ./...` (server);
  - `… -tags purego ./...` (client).
  - The step is blocking and named `govulncheck`. Through `go run`, a finding exits 1.
  - Client: in the `backend` job with no `if:`, after "Provide embed placeholder for frontend/dist" (`test.yml:253-256`) and before "Go vet" (`:269`).
  - Server: in the `test` job after "Vet" (`test.yml:38-39`).
  - The server's `timeout-minutes: 10` (`:17`) is **not** changed.
- **`--ignore-scripts`:**
  - It goes only in the two Test-workflow `npm ci` lines: client `test.yml:344`, server `test.yml:79`.
  - The build Taskfiles are not changed (D-d).
- **SHA pins (Q4 = A):** the 7 actions in the server `release.yml` become `@<40-hex> # vX.Y.Z`. S6471 is marked safe by the user, and `Dockerfile:53` is unchanged.
- **Not done by the lanes (user actions, design §8 #2-3):** Sonar mark-safe (S8545 ×4 client + ×2 server + the new govulncheck lines, S2068, S6471), the `design/**` exclusion, and secret scanning.
- **No proto change.** Do not edit the umbrella design. Its §6.5 corrections are a DOC hand-off.
- **gopls is unreliable here.** Trust only `go build` and `go vet`.
- **Commits** end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Push and PR are done separately, outside this plan. Never merge: the user merges.

`$S` = the executor's scratchpad directory (local evidence files; not committed). Run every shell block in Git Bash with `export MSYS_NO_PATHCONV=1`.

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
(cd frontend && npm ci --ignore-scripts --legacy-peer-deps && npm test && npm run build && npm audit)
go build ./... && go vet ./...                      # GUI tag set; needs the frontend/dist just built
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 -tags headless ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
bash .github/scripts/assert-go-toolchain_test.sh && bash .github/scripts/go-deps-update_test.sh && bash .github/scripts/wails-update-check_test.sh
```

The generate block mirrors `.github/actions/generate/action.yml:59-77`. Pass means:
- build and vet print nothing, the cross-build exits 0, and 10 packages are `ok`;
- vitest reports the T0 file count, all passing, and `npm run build` exits 0;
- `npm audit` at T0 reports the esbuild finding; **from S4 on** it reports `found 0 vulnerabilities`;
- govulncheck at T0 reports `Your code is affected by 6 vulnerabilities from 2 modules.` (exit 1 through `go run`) for both tag sets; **from S3 on** it reports `Your code is affected by 0 vulnerabilities.`, and the only module-level line left is x/crypto GO-2026-5932;
- the three script tests pass.

### Client verification suite (worktree root)

```bash
buf generate
mkdir -p frontend/dist && [ -f frontend/dist/index.html ] || echo '<!doctype html><title>ci</title>' > frontend/dist/index.html
WAILS_V="$(go list -m -f '{{.Version}}' github.com/wailsapp/wails/v3)"
go run "github.com/wailsapp/wails/v3/cmd/wails3@${WAILS_V}" generate bindings -ts -f "-tags purego" -clean=true
(cd frontend && npm ci --ignore-scripts && npm run test && npm run build && npx tsc --noEmit)
go build -tags purego ./...
go vet -tags purego ./...
go test -race -count=1 -tags purego ./...
VCS_SERVER_BIN="$SRV_BIN" go test -race -count=1 -tags purego ./internal/voice/ ./internal/session/
go build -tags production,purego .
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 -tags purego ./...
bash .github/scripts/assert-go-toolchain_test.sh && bash .github/scripts/go-deps-update_test.sh && bash .github/scripts/wails-update-check_test.sh   # TestCIScripts skips on Windows
```

`$SRV_BIN` is the headless server built at the client's **pinned** SHA `850d984e186099d13acd6066f40ceb22ed2f7760` (`test.yml:49`). Use `$S/vcs-server-850d984.exe` if it exists. Otherwise build it in a detached scratch worktree:
- `git -C D:/Projects/vcs-srs-server worktree add --detach "$S/sec-pin-srv" 850d984e186099d13acd6066f40ceb22ed2f7760`;
- run the generate block;
- `go build -tags headless -o "$S/vcs-server-850d984.exe" .`;
- then `git worktree remove --force` the worktree and `git worktree prune`.

Pass means:
- `npm run build` ends with `check-dist: dist/ contains index.html, main.html, comms.html, notifications.html`;
- vitest reports `Tests 594 passed` (design §4.5 #1), plus nothing the lane adds;
- tsc, build and vet print nothing; every package is `ok`;
- the integration run is `ok` for both packages and **not** skipped (`-v` shows `TestIntegrationRelayBetweenTwoClients` PASS);
- govulncheck prints `Your code is affected by 0 vulnerabilities.`. Its module-level lines are GO-2026-6866, GO-2026-6860 and GO-2026-5932 at T0, and **from C4 on** only GO-2026-5932.

## Gates

Every gate below was decided by the user on 2026-10-28 (design "User decisions"). The design was approved by "Execute SV7 and SEC both as workflows as discussed before." Anything outside this table is a hard stop.

| Gate | Where | Approval it rests on |
|---|---|---|
| server behaviour: bcrypt cap 12, `Invalid password hash …` body, WARN log, compare outside the lock | S1, S2 | Q2 = A, design §6.3 |
| dependency: server grpc v1.85.0, quic-go v0.64.1, x/net v0.62.0 and the forced moves | S3 | Q1 = A, Q6 = A, design §6.1 |
| dependency: remove server dev dependency `typed-scss-modules` | S4 | Q3 = A |
| ci-cd: server govulncheck step; `npm ci --ignore-scripts --legacy-peer-deps` | S5 | D5, D-c, design §6.2 |
| ci-cd: SHA-pin the 7 actions in server `release.yml` | S5 | Q4 = A |
| housekeeping: delete `build/linux/appimage/build.sh`; comment in `dev/config/control.yaml` | S6 | D8 (D-e, D-f) |
| test-only: Windows port-flake fixture fix | C1 | Q5 = A |
| client: S4036 `/usr/bin/open` | C2 | D8 (D-a) |
| docs and test: client cost guard, `PROTO_GAPS.md` note | C3 | Q2 = A |
| dependency: client x/net v0.62.0 | C4 | Q6 = A |
| ci-cd: client govulncheck step on all three legs; `npm ci --ignore-scripts` | C5 | D5, D-b, design §6.2 |
| cross-repo: server PR first; client server-pin bump after the server merge | post-merge follow-up | D10 |

## Out of scope (design §10)

- A bcrypt concurrency limit (Q2c).
- The distroless `:nonroot` switch (Q4).
- A gin bump.
- Any change to client hashing.
- Config env expansion for the dev passwords.
- `--ignore-scripts` in the build Taskfiles.
- A server `:0` voice bind.
- Code smells.
- Removing `vite-plugin-typed-css-modules`.
- Secret-scanning remediation.
- Server `README.md`: that is SV7, which runs in parallel and cites the cap value 12.
- The umbrella row corrections (§6.5): a DOC hand-off.

## Review Focus

1. **A legitimate hash in another bcrypt variant** (`$2b$`, `$2y$`) at cost ≤ 12, for example from the C# peer's BCrypt library.
   - *Expected:* accepted exactly as `$2a$` is.
   - *Test:* S1, `TestCheckHashCost` rows `2b-cost10` and `2y-cost10`.
2. **An admin saves settings while a login's compare is running.**
   - *Expected:* the save does not stall behind the login.
   - *Test:* S2, `TestGuestLoginComparesOutsideSettingsLock`.
3. **The C# peer logs in with exactly cost 12** (`VcsClientSyncHandler.cs:140`, PR #253 head).
   - *Expected:* success.
   - *Test:* S1, `TestGuestLoginAcceptsMaxCost`.
4. **The Windows integration leg fails on a server error that is not a bind error** (bad config, key load).
   - *Expected:* it still fails fast with the log and is not retried.
   - *Test:* C1, the `IsBindError` false rows, and Start's retry being gated on `IsBindError`.
5. **An empty or garbage password field** (a hand-crafted client).
   - *Expected:* `Invalid password hash: malformed`, no compare, no crash.
   - *Test:* S1, `TestGuestLoginRejectsMalformedHash` rows `""` and `not-a-hash`.

Not testable here, recorded for the user: a new advisory published after the merge turns `main` red in both repos (R1). The step is separately named so the cause is visible.

---

# Part 1: Lane sec-server (`vcs-srs-server`, branch `chore/cleanup-sec`)

### Task S0: Worktree and baseline

- [x] **Step 1:** `git -C D:/Projects/vcs-srs-server fetch origin && git -C D:/Projects/vcs-srs-server worktree add -b chore/cleanup-sec D:/Projects/vcs-srs-server-wt/sec origin/main`. Run every later command from `D:/Projects/vcs-srs-server-wt/sec`. Record `git log -1 --format=%h`, expected `df8d9f3` or later. (The local `main` checkout is at `850d984`; never branch from it.)
- [x] **Step 2:** Run the server verification suite. Save:
  - `cp go.mod "$S/sec-srv-go.mod.before"` and `cp go.sum "$S/sec-srv-go.sum.before"`;
  - each govulncheck output to `$S/sec-srv-vuln-{headless,gui}-before.txt`, expected 6 reachable (grpc ×4, quic-go ×2), identical to `$S/secsrv-vuln-before-{headless,gui}.txt`;
  - the `npm audit` output to `$S/sec-srv-npm-audit-before.txt` (esbuild via `typed-scss-modules`);
  - the vitest file count.

  Everything except govulncheck and `npm audit` must be green. If anything else is red, stop and report it verbatim.

### Task S1: Bcrypt cost cap (GATE: server behaviour, Q2 = A)

**Files:**
- Create: `utils/general_test.go` (package `utils`; no test file covers `general.go` today)
- Modify: `utils/general.go:1-12`, `srs/auth_service.go` (insert between `:481` and `:483`)
- Test: `srs/auth_service_test.go` (append after the last test, `:502`ff.)

**Interfaces:**
- Produces in `utils`:
  - `const MaxBcryptCost = 12`;
  - `var ErrMalformedHash = errors.New("malformed")`;
  - `func CheckHashCost(hash string) error`. It returns nil when `bcrypt.Cost` parses the hash and the cost is ≤ `MaxBcryptCost`. It returns `ErrMalformedHash` when `bcrypt.Cost` errors. Otherwise it returns `fmt.Errorf("bcrypt cost %d exceeds the server maximum of %d", cost, MaxBcryptCost)`.
  - `CheckPasswordHash(hash, plaintext string) bool` keeps its signature and returns false when `CheckHashCost(hash) != nil`, before any compare.
- Produces a test helper in `srs/auth_service_test.go`: `func forgeCost(t *testing.T, hash string, cost int) string`. It requires the `$2a$04$` prefix (output of `hashPassword`, `:262-269`) and returns the hash with that prefix replaced by `fmt.Sprintf("$2a$%02d$", cost)`. Forged hashes are accepted by `bcrypt.Cost` (design §4.3 #1).

- [x] **Step 1: Write the utils tests** in `utils/general_test.go`:
  - `TestCheckHashCost`, a table built from `bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)` with the cost field rewritten:
    - `cost4` → nil;
    - `cost12` → nil;
    - `2b-cost10` and `2y-cost10` (prefix `$2b$10$` / `$2y$10$`) → nil;
    - `cost13` → error text exactly `bcrypt cost 13 exceeds the server maximum of 12`;
    - `""` and `not-a-hash` → `errors.Is(err, ErrMalformedHash)`.
  - `TestCheckPasswordHashRefusesOverCostQuickly`: a forged cost-14 hash of `"pw"`. `start := time.Now(); ok := CheckPasswordHash(h, "pw")`. Assert `!ok` and `time.Since(start) < 50*time.Millisecond`. Today this takes about 560 ms (design §4.3 #1).
  - `TestCheckPasswordHashStillMatches`: a cost-4 hash of `"pw"` → true, and `"other"` → false.
- [x] **Step 2: Write the GuestLogin tests** in `srs/auth_service_test.go`. Each uses `newGuestLoginTestServer` (`:47`), `mustInit` (`:250`), `testPeerContext()` (`:82`), name `"Victim"` and unit `"AB1"`. Before the call, replace `s.logger` with `slog.New(slog.NewTextHandler(&buf, nil))`.
  - `TestGuestLoginRejectsOverCostHash`:
    - the password is `forgeCost(t, hashPassword(t, guestTestCoalitionPassword), 13)`;
    - assert `err == nil`, `!resp.GetSuccess()`, and that `resp.GetErrorMessage()` equals `Invalid password hash: bcrypt cost 13 exceeds the server maximum of 12`;
    - assert `pendingCount(s) == 1`, so the entry is kept;
    - assert `buf` contains `level=WARN` and `127.0.0.1`.
  - `TestGuestLoginRejectsMalformedHash`: a table over `""` and `"not-a-hash"`, with a fresh `mustInit` per row. Message: `Invalid password hash: malformed`; `pendingCount(s) == 1`.
  - `TestGuestLoginAcceptsMaxCost` (regression guard, green before and after): `bcrypt.GenerateFromPassword([]byte(guestTestCoalitionPassword), 12)` → `resp.GetSuccess()`.
- [x] **Step 3: Run** `go test -count=1 -tags headless ./utils/ ./srs/ -run 'TestCheckHashCost|TestCheckPasswordHash|TestGuestLoginRejects|TestGuestLoginAcceptsMaxCost' -v`. Expected: a compile FAIL on `undefined: CheckHashCost` / `ErrMalformedHash`. Temporarily move `TestCheckHashCost` aside, then run again. Expected:
  - `TestCheckPasswordHashRefusesOverCostQuickly` FAILs on time (~560 ms);
  - both `TestGuestLoginRejects…` FAIL with message `No coalition found with that password` (`auth_service.go:496-500`);
  - the guard and `StillMatches` PASS.

  Restore the moved test and save the output to `$S/sec-s1-red.txt`.
- [x] **Step 4: Implement.**
  - `utils/general.go`: the Interfaces above.
  - `srs/auth_service.go`, after the username check (`:476-481`) and before `:483`:
    ```go
    if err := utils.CheckHashCost(request.Password); err != nil {
        s.logger.Warn("Rejected guest login: password hash refused", "IP", peerAddress, "ClientGuid", clientGuid, "error", err)
        return &pb.GuestLoginResponse{Success: false,
            LoginResult: &pb.GuestLoginResponse_ErrorMessage{ErrorMessage: "Invalid password hash: " + err.Error()}}, nil
    }
    ```
  - Add a one-line comment above it citing the remote CPU-exhaustion reason (design §3 B1/B2) and the C# peer at cost 12.
- [x] **Step 5: Run** the Step 3 command. Expected: all PASS. Then `go test -race -count=1 -tags headless ./srs/ ./utils/`, which must be `ok`.
- [x] **Step 6: After-fix-only test.** Add `TestGuestLoginRejectsCost31Quickly`: forged cost 31, return in `< 1*time.Second`, with the cost-31 message. Add a comment: never run against pre-cap code, because it would take about 21 h (design §5.1 #1). Run it. Expected: PASS.
- [x] **Step 7: Commit** `fix(auth): cap the client-supplied bcrypt cost at 12 and reject malformed hashes (SEC B1)`.

### Task S2: Compare outside the settings lock (GATE: server behaviour, Q2 = A "(b)")

**Files:**
- Modify: `srs/auth_service.go:483-494` (as moved by S1)
- Test: `srs/auth_service_test.go`

**Interfaces:**
- Consumes: S1's `CheckHashCost` gate, which still runs first.
- Produces: the package-level seam `var comparePassword = utils.CheckPasswordHash` in `srs/auth_service.go`. Only tests reassign it.

- [x] **Step 1: Behaviour-neutral seam.** Add `comparePassword` and call it in place of `utils.CheckPasswordHash` at `:488`. Run `go test -count=1 -tags headless ./srs/`. Expected: `ok`.
- [x] **Step 2: Write `TestGuestLoginComparesOutsideSettingsLock`.**
  - Swap `comparePassword` for a stub that closes `entered` once and then blocks on `<-release` before returning `utils.CheckPasswordHash(hash, plain)`. Restore it in `t.Cleanup`.
  - Start `GuestLogin` (a valid cost-4 hash) in a goroutine that closes `done` when it returns. Wait for `entered` (2 s guard).
  - In a second goroutine, call `s.settingsState.Lock()` and then close `locked`.
  - `select` on `locked` against `time.After(100*time.Millisecond)`:
    - **on timeout:** `close(release)`, wait for `locked`, `Unlock()`, wait for `done`, then `t.Fatal("settingsState.Lock() blocked behind the password compare; compare must run outside the RLock")`;
    - **on success:** `Unlock()`, `close(release)`, wait for `done`, and assert the response is `Success`.
- [x] **Step 3: Run** `go test -race -count=1 -tags headless ./srs/ -run TestGuestLoginComparesOutsideSettingsLock -v`. Expected: FAIL with the message above, after about 100 ms, with no hang.
- [x] **Step 4: Implement.** Under the RLock, set `coalitions := append([]state.Coalition(nil), s.settingsState.Coalitions...)`, then `RUnlock`. Loop over `coalitions` with `comparePassword` outside the lock. The `selectedCoalition`/`coalitionFound` semantics are unchanged.
- [x] **Step 5: Run** the same test (PASS), then `go test -race -count=1 -tags headless ./srs/ ./control/` (`ok`). `TestAuthInterceptorCodes` and the session E2E tests stay green.
- [x] **Step 6: Commit** `fix(auth): compare guest password hashes outside the settings read lock (SEC B2)`.

### Task S3: Go dependency bumps (GATE: dependency, Q1 = A, Q6 = A)

**Files:** `go.mod`, `go.sum`

- [x] **Step 1: Red evidence.** It is `$S/sec-srv-vuln-{headless,gui}-before.txt` from S0: 6 reachable. No new run.
- [x] **Step 2: Bump.**
  ```bash
  go get google.golang.org/grpc@v1.85.0 github.com/quic-go/quic-go@v0.64.1 golang.org/x/net@v0.62.0
  ```
  Then re-run the whole generate block (protoc-gen-go is now derived as v1.36.12), then `go mod tidy`, then `go mod tidy -diff` (exit 0).
- [x] **Step 3: Check the delta.** `diff "$S/secsrv-go.mod.after" go.mod`: this is the probe result without x/net, so only the x/net-driven lines may differ. `diff "$S/sec-srv-go.mod.before" go.mod` must show exactly:
  - grpc `v1.77.0→v1.85.0`, protobuf `v1.36.11→v1.36.12`;
  - quic-go `v0.54.0→v0.64.1`, qpack `v0.5.1→v0.6.0`;
  - genproto rpc `→ v0.0.0-20260817212433-ac3dfec99bb1`;
  - x/net `v0.60.0→v0.62.0`, x/crypto `v0.57.0→v0.58.0`, x/sys `v0.48.0→v0.49.0`, x/text `v0.42.0→v0.43.0`;
  - optionally `golang.org/x/term v0.47.0 // indirect`.

  gin `v1.11.0`, gqlgen `v0.17.89`, x/tools `v0.49.0` and wails `v3.0.0` are unchanged. **Any other line means stop and report.** `git diff --ignore-cr-at-eol --stat -- graph/` must be empty: gqlgen may only touch CRLF. *(Executed: x/tools `v0.49.0 → v0.51.0` and x/sync `v0.23.0 → v0.24.0` also moved. `go mod graph` shows both are required by `golang.org/x/text@v0.43.0`, an approved forced move; no new module. Recorded in the commit body and the PR.)*
- [x] **Step 4: Verify.**
  - `go build -tags headless ./... && go vet -tags headless ./...`;
  - `go test -race -count=1 -tags headless ./...`: 10 packages `ok`, including `TestAuthInterceptorCodes` (`control/server_test.go:271`), `TestAuthErrorMapping` (`:349`) and the session E2E tests (`control/session_e2e_test.go:138,170`);
  - govulncheck headless and GUI (the GUI run needs the S0 `frontend/dist`). Expected for both: `Your code is affected by 0 vulnerabilities.`, with module-level x/crypto GO-2026-5932 only and no x/net line. Save to `$S/sec-srv-vuln-{headless,gui}-after.txt`.
- [x] **Step 5: Commit** `chore(deps): grpc v1.85.0, quic-go v0.64.1, x/net v0.62.0 (SEC A1, A2, A6b)`. The body lists the forced moves and the eight Dependabot alerts closed (#38, #93, #118, #120, #121, #15, #23, #67).

### Task S4: Remove `typed-scss-modules` (GATE: dependency, Q3 = A)

**Files:** `frontend/package.json:42`, `frontend/package-lock.json` (the `node_modules/typed-scss-modules` subtree from `:5465`, including its nested `@esbuild/*` and `esbuild 0.17.19`)

- [x] **Step 1: Red evidence.** `(cd frontend && npm ls esbuild)` lists `esbuild@0.17.19` under `typed-scss-modules`; `npm audit` reports GHSA-67mh-4wv8-2f99.
- [x] **Step 2:** `git grep -n "typed-scss-modules" -- ':!frontend/package*.json'` must print nothing. If anything still references it, stop and report. Then `(cd frontend && npm uninstall --legacy-peer-deps typed-scss-modules)`.
- [x] **Step 3: Verify.**
  - `npm ls esbuild` shows only `esbuild@0.28.2`;
  - `npm audit` prints `found 0 vulnerabilities` (any other finding: stop and report);
  - `npm ci --ignore-scripts --legacy-peer-deps && npm test`: the S0 file count, all passing;
  - `git diff --stat` touches only the two package files.
- [x] **Step 4: Commit** `chore(frontend): drop unused typed-scss-modules, clearing esbuild alert #2 (SEC A5)`.

### Task S5: CI: govulncheck, `--ignore-scripts`, SHA pins (GATE: ci-cd, D5, D-c, Q4 = A)

**Files:** `.github/workflows/test.yml` (insert after `:39`; `:79`), `.github/workflows/release.yml:49,52,55,63,73,97,102`

The server has no workflow-shape tests (Go or otherwise), so each check is a grep or parse check.

- [x] **Step 1: Edit `test.yml`.**
  - After the "Vet" step (`:38-39`), insert:
    ```yaml
      # Blocking (Phase 6.9 SEC D5): any reachable advisory fails the job.
      # A new advisory can turn main red with no code change; that is the
      # point of the gate. Covers linux + headless only (design R13).
      # go install/run checks the module against sum.golang.org (S8545 n/a).
      - name: govulncheck
        run: go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 -tags headless ./...
    ```
    Indent it to match the sibling steps.
  - `:79` becomes `run: npm ci --ignore-scripts --legacy-peer-deps`.
- [x] **Step 2: Edit `release.yml`.** Pin each of the 7 actions as `uses: <action>@<sha> # <tag>`. Re-resolve each one at execution time:
  ```bash
  sha=$(gh api repos/<owner>/<repo>/commits/<major> --jq .sha)
  gh api "repos/<owner>/<repo>/tags?per_page=100" --jq ".[]|select(.commit.sha==\"$sha\")|.name"
  ```
  Use the exact `vX.Y.Z` tag the second command prints. Expected values on 2026-10-28:
  - `docker/setup-qemu-action` `99012661954931238ded8c8b007157a8430204e1` v4.4.0;
  - `docker/setup-buildx-action` `f87e5991a6d7451dcb8d9637bfbc97413f497069` v4.4.1;
  - `docker/login-action` `81c6f93d4bc991e510b342a00b419b3bb4efc5c6` v4.7.0;
  - `docker/metadata-action` `dc802804100637a589fabce1cb79ff13a1411302` v6.2.0;
  - `docker/build-push-action` `c3c9e263c25d99ce0380d002d59b67737d91b0dc` v7.4.0;
  - `mikepenz/release-changelog-builder-action` `aa013739bbc8e7067dbbecd4525ff11aeb51a84b` v6.4.0;
  - `softprops/action-gh-release` `efb35369e0ad2afab669f228072c1b0d510eae64` v3.0.3.

  If a major tag's SHA now differs, use the new SHA and its tag and record that in the commit body. `actions/*` lines stay as they are: Sonar flags only these 7, and that matches the client's practice.
- [x] **Step 3: Check.**
  - `grep -c 'govulncheck@v1.9.0 -tags headless ./...' .github/workflows/test.yml` → `1`;
  - `grep -n 'npm ci' .github/workflows/test.yml` shows only the `--ignore-scripts --legacy-peer-deps` line;
  - `grep -nE 'uses: (docker|mikepenz|softprops)/' .github/workflows/release.yml | grep -vE '@[0-9a-f]{40} # v[0-9]+\.[0-9]+\.[0-9]+$'` prints nothing;
  - `grep -cE '@[0-9a-f]{40} # v' .github/workflows/release.yml` → `7`;
  - `python -c "import yaml;[yaml.safe_load(open(f)) for f in ('.github/workflows/test.yml','.github/workflows/release.yml')]"` exits 0 (if PyYAML is missing, use `npx --yes yaml-lint` or skip with a note);
  - step order: `grep -n 'name: Vet\|name: govulncheck\|name: Test CI scripts' .github/workflows/test.yml` lists them in that order.
- [x] **Step 4: Run the step locally**, exactly as CI does: `go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 -tags headless ./...; echo "exit=$?"`. Expected: `exit=0`. Re-run it against the S0 red output for evidence: S0 showed exit 1 with 6 findings.
- [x] **Step 5: Commit** `ci: blocking govulncheck, npm ci --ignore-scripts, SHA-pinned release actions (SEC D5, D-c, S7637)`.

### Task S6: Sonar housekeeping (D-e, D-f)

**Files:** delete `build/linux/appimage/build.sh`; modify `dev/config/control.yaml:12` (insert above `coalitions:`)

- [x] **Step 1: Check that nothing calls the script.** `git grep -n 'appimage/build.sh' -- ':!docs'` must print nothing. The Taskfile uses `wails3 generate appimage` (`build/linux/Taskfile.yml:46`). Then `git rm build/linux/appimage/build.sh`.
- [x] **Step 2:** Insert above `coalitions:` (`:12`): `# Dev-only docker-compose fixture (docker-compose.dev.yml mounts it read-only): throwaway passwords, never used in a deployment.` Then `python -c "import yaml;d=yaml.safe_load(open('dev/config/control.yaml'));assert [c['password'] for c in d['coalitions']]==['blue123','red123']"` exits 0.
- [x] **Step 3: Commit** `chore: drop the unused Wails-alpha AppImage script; mark the dev passwords as fixtures (SEC S6506, S2068)`.

### Task S7: Full-suite verification and plan copy

**Files:** Create `docs/superpowers/plans/2026-10-28-phase-6-9-sec-security-alerts.md` (this plan, with its checkboxes as executed). It points to the client spec path `docs/superpowers/specs/2026-10-28-phase-6-9-sec-security-alerts-design.md`.

- [x] **Step 1:** Run the server verification suite (full) on a clean `frontend/node_modules`. Expected:
  - every pass criterion "from S3/S4 on";
  - govulncheck 0 for both tag sets (outputs pasted in the PR);
  - `npm audit` 0;
  - 10 Go packages `ok`;
  - the three script tests PASS.
- [x] **Step 2:** Build the bumped headless binary for the client lane: `go build -tags headless -o "$S/vcs-server-sec.exe" .`.
- [x] **Step 3:** `git status --short` is clean apart from the plan copy. `git log --oneline origin/main..HEAD` shows S1–S6. Then commit `docs(phase-6.9): SEC plan (server)`.

---

# Part 2: Lane sec-client (`vcs-srs-client`, branch `chore/cleanup-sec`)

Independent of Part 1 and runnable in parallel. Only the post-merge follow-up waits for the server merge.

### Task C0: Worktree and baseline

- [ ] **Step 1:** `git -C D:/Projects/vcs-srs-client fetch origin && git -C D:/Projects/vcs-srs-client worktree add -b chore/cleanup-sec D:/Projects/vcs-srs-client-wt/sec origin/main`. Run every later command from `D:/Projects/vcs-srs-client-wt/sec`. Record `git log -1 --format=%h`, expected `b80b0e6` or later.
- [ ] **Step 2:** Prepare `$SRV_BIN` (see the client suite). Run the client verification suite. Save the govulncheck output to `$S/sec-cli-vuln-before.txt`: 0 reachable, module-level GO-2026-6866, GO-2026-6860 and GO-2026-5932. Save `cp go.mod "$S/sec-cli-go.mod.before"`. Everything must be green. If anything is red, stop and report it verbatim.

### Task C1: Windows reserved-port flake (Q5 = A; test-only)

**Files:**
- Create: `internal/voice/fixture_ports_test.go` (package `voice`). It must **not** go under `testdata`: `./...` never runs it there (`server_fixture.go:4-7`).
- Modify: `internal/voice/testdata/server_fixture.go`: `:94-99` (doc), `:100-166` (`Start`), `:181-195` (port helpers), `:298-314` (`waitReady`)

**Interfaces:**
- Produces in `testdata`:
  - `func FreeUDPPort(t *testing.T) int`: binds `net.ListenPacket("udp", "127.0.0.1:0")`, closes it, and returns the port.
  - `func IsBindError(log string) bool`: true iff `log` contains any of `forbidden by its access permissions`, `Only one usage of each socket address`, `address already in use`.
  - `const startAttempts = 3`.
  - `waitReady(logPath string) (out string, ready bool)`: polls as today. It returns `(log, false)` as soon as `level=ERROR` appears, or at `readyTimeout`, instead of calling `t.Fatalf`.
- `Start(t) *Server` keeps its signature (integration_test.go has 8 call sites).

- [ ] **Step 1: Write the tests** in `internal/voice/fixture_ports_test.go`:
  - `TestFreeUDPPortIsUDPBindable`: 20 iterations of `p := testdata.FreeUDPPort(t)`. Assert `0 < p && p < 65536`, then that `net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127,0,0,1), Port: p})` succeeds, then close it.
  - `TestIsBindError`, a table:
    - **true:**
      - `level=ERROR msg="Failed to start voice server" error="listen udp 127.0.0.1:54600: bind: An attempt was made to access a socket in a way forbidden by its access permissions."`;
      - `…bind: Only one usage of each socket address (protocol/network address/port) is normally permitted.`;
      - `…bind: address already in use`.
    - **false:**
      - `level=ERROR msg="Failed to load config" error="yaml: line 3: mapping values are not allowed"`;
      - `""`.
- [ ] **Step 2: Run** `go test -count=1 -tags purego ./internal/voice/ -run 'TestFreeUDPPortIsUDPBindable|TestIsBindError' -v`. Expected: a compile FAIL, `undefined: testdata.FreeUDPPort` / `testdata.IsBindError`.
- [ ] **Step 3: Implement.**
  - Add `FreeUDPPort` and `IsBindError` next to `freePort` (http and control stay TCP).
  - Change `waitReady` as in Interfaces.
  - Restructure `Start` to try up to `startAttempts` times. Each attempt uses its own `filepath.Join(dir, fmt.Sprintf("attempt-%d", n))`:
    1. allocate fresh ports, with `voicePort := FreeUDPPort(t)`;
    2. write the config and the banned file, then launch;
    3. register the existing kill/wait/dump cleanup;
    4. call `waitReady`.
  - On `ready`, return the `Server`.
  - On `!ready && IsBindError(out) && n < startAttempts`: kill and wait for the process, `t.Logf("vcs-srs-server bind error on attempt %d/%d; retrying with fresh ports:\n%s", …)`, then continue.
  - Otherwise `t.Fatalf` with today's two messages (`:308`, `:313`).
  - Update the doc comment at `:94-99`: the voice port is UDP-probed because Windows keeps separate UDP and TCP excluded ranges (design §4.3 #4), and the retry covers the release-to-bind race.
- [ ] **Step 4: Run** the Step 2 command (PASS). Then `VCS_SERVER_BIN="$SRV_BIN" go test -race -count=3 -tags purego ./internal/voice/ -run TestIntegration -v`. Expected: every `TestIntegration…` PASSes three times, with no skip. Also run `go vet -tags purego ./internal/voice/...`; vet skips `testdata`, so also run `go vet ./internal/voice/testdata/` explicitly. Both print nothing.
- [ ] **Step 5: Commit** `test(voice): UDP-probe the fixture's voice port and retry server bind errors (SEC C1)`.

### Task C2: S4036 absolute `open` path (D-a)

**Files:** `internal/hotkeys/permission_darwin_pane.go:73`; test `internal/hotkeys/permission_darwin_test.go` (`//go:build darwin && cgo`, `:1`; add after `TestAccessibilityPaneURL`, `:74`)

**Interfaces:** Produces `const openBinary = "/usr/bin/open"` in `permission_darwin_pane.go`. `openPane` becomes `exec.Command(openBinary, url).Run()`.

- [ ] **Step 1: Write `TestOpenPaneUsesAbsoluteBinary`.**
  - It asserts `openBinary == "/usr/bin/open"`.
  - It asserts that `readSource(t, "permission_darwin_pane.go")` (`:97`) contains `exec.Command(openBinary, url)` and does not contain `exec.Command("open"`.
  - Add a comment: this is a compile-level guard only, and it runs on the macOS CI leg; the `nocgo` test file is `darwin && !cgo` and does not run there.
- [ ] **Step 2: Run** `GOOS=darwin CGO_ENABLED=1 go vet -tags purego ./internal/hotkeys/`. Expected: FAIL, `undefined: openBinary`. (The test cannot execute on Windows. vet type-checks the darwin test file. If cgo cross-vet is impossible on this host, use `GOOS=darwin go vet ./internal/hotkeys/` for the nocgo half and record that the test itself runs only on the macOS leg.)
- [ ] **Step 3: Implement.** Add the constant with a one-line comment (Sonar S4036: an absolute path avoids PATH lookup). Use it in `openPane`.
- [ ] **Step 4: Run** the Step 2 vet (it prints nothing), plus `GOOS=darwin CGO_ENABLED=0 go build -tags purego ./internal/hotkeys/` and `go build -tags purego ./...`.
- [ ] **Step 5: Commit** `fix(hotkeys): open the Accessibility pane via /usr/bin/open (Sonar S4036)`.

### Task C3: Client bcrypt cost guard + `PROTO_GAPS.md` note (Q2 = A)

**Files:** Create `internal/session/bcrypt_cost_test.go` (package `session`). Modify `docs/PROTO_GAPS.md`: a new `## 12. Server-side cap on the guest password hash cost` inserted before `## How to use this document`, after the `---` that follows §11.

- [ ] **Step 1: Write `TestGuestHashCostWithinServerCap`.**
  - Set `const serverMaxBcryptCost = 12`, with a comment: it mirrors the server's `utils.MaxBcryptCost`, and a hash above it is answered `Invalid password hash: bcrypt cost N exceeds the server maximum of 12`.
  - Assert `bcrypt.DefaultCost <= serverMaxBcryptCost`.
  - Read `session.go` and assert it contains `bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)` (`:744`), so a change to the cost there fails here. Read it from the package directory: `os.ReadFile("session.go")`, with CRLF normalised.
  - Generate one hash at `bcrypt.DefaultCost` and assert `bcrypt.Cost(h) <= 12`.
- [ ] **Step 2: Run** `go test -count=1 -tags purego ./internal/session/ -run TestGuestHashCostWithinServerCap -v`. Expected: PASS. This is a guard; it is green on arrival. **Mutation check, not committed:** change `bcrypt.DefaultCost` at `session.go:744` to `14`, re-run (FAIL on the source assertion), then `git checkout -- internal/session/session.go`. `git diff -- internal/session/session.go` must be empty.
- [ ] **Step 3: Write the `PROTO_GAPS.md` §12.** It is wording only, with no proto change:
  - **Where it shows up:** `GuestLogin`.
  - **Today:** the server (Phase 6.9 SEC) rejects a password hash whose bcrypt cost exceeds 12, or that does not parse, with `Success:false` and `Invalid password hash …`. It does this to bound a remote CPU-exhaustion vector.
  - **Client requirement:** the Go client hashes at `bcrypt.DefaultCost` (10; guarded by `TestGuestHashCostWithinServerCap`). The C# peer uses 12 (PR #253 head `VcsClientSyncHandler.cs:140`, unmerged).
  - **Suggested proto change:** none. A `max_bcrypt_cost` in the auth-init response would let clients adapt. Record it as an option only.
- [ ] **Step 4: Commit** `test(session): guard the guest hash cost against the server's cap of 12; document it (SEC Q2)`.

### Task C4: x/net v0.62.0 (GATE: dependency, Q6 = A)

**Files:** `go.mod:31`, `go.sum`

- [ ] **Step 1: Red evidence.** `$S/sec-cli-vuln-before.txt` lists GO-2026-6866 and GO-2026-6860 (x/net v0.60.0, module-level).
- [ ] **Step 2:** `buf generate` has already run in C0. Run `go get golang.org/x/net@v0.62.0 && go mod tidy && go mod tidy -diff`. `diff "$S/sec-cli-go.mod.before" go.mod` must show only `golang.org/x/net v0.60.0 → v0.62.0`. `git diff go.sum` must show only x/net lines, plus an optional `golang.org/x/term v0.47.0/go.mod` line. **Anything else means stop and report.**
- [ ] **Step 3: Verify.**
  - `go build -tags purego ./... && go vet -tags purego ./...`;
  - `go test -race -count=1 -tags purego ./...`;
  - the integration run against `$SRV_BIN`;
  - govulncheck: `Your code is affected by 0 vulnerabilities.`, module-level GO-2026-5932 only. Save to `$S/sec-cli-vuln-after.txt`.
- [ ] **Step 4: Commit** `chore(deps): golang.org/x/net v0.62.0 (GO-2026-6866, GO-2026-6860; SEC Q6)`.

### Task C5: CI: govulncheck on every backend leg, `npm ci --ignore-scripts` (GATE: ci-cd, D5, D-b)

**Files:**
- Create: `internal/buildcheck/security_ci_test.go` (package `buildcheck`; reuses `readRepoFile` `:32`, `hasLine` `:71`, `workflowFiles` `:55`, `workflowJobs` `ci_hardening_test.go:20`)
- Modify: `.github/workflows/test.yml` (insert between `:256` and `:258`; `:344`)

**Interfaces:** Produces `const govulncheckRun = "go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 -tags purego ./..."`.

- [ ] **Step 1: Write the tests.**
  - `TestGovulncheckGatesEveryBackendLeg`, on `job := workflowJobs(t, wf, text)["backend"]` of `test.yml`:
    - `strings.Count(job, govulncheckRun) == 1`;
    - the job still has `os: [ubuntu-latest, macos-latest, windows-latest]` (via `hasLine`);
    - the step block that starts at `- name: govulncheck` and runs to the next `\n      - ` has no `if:` line, so it runs on all three legs;
    - `idx("name: Provide embed placeholder for frontend/dist") < idx("name: govulncheck") < idx("name: Go vet")`;
    - `idx("name: Generate proto bindings") < idx("name: govulncheck")`.

    Each failure names the rule (design D5: govulncheck analyses only the host GOOS).
  - `TestTestWorkflowNpmCiIgnoresScripts`:
    - the `frontend` job `hasLine(job, "run: npm ci --ignore-scripts")`;
    - for every file in `workflowFiles(t)`, no line trimmed equals `run: npm ci`.
- [ ] **Step 2: Run** `go test -count=1 -tags purego ./internal/buildcheck/ -run 'TestGovulncheckGatesEveryBackendLeg|TestTestWorkflowNpmCiIgnoresScripts' -v`. Expected: both FAIL (count 0, and the bare `npm ci` at `test.yml:344`).
- [ ] **Step 3: Edit `test.yml`.**
  - Between the placeholder step (`:253-256`) and the `-tags purego` comment (`:258`), insert:
    ```yaml
      # Blocking (Phase 6.9 SEC D5): any reachable advisory fails the leg.
      # On all three legs on purpose: govulncheck analyses only the host GOOS,
      # so one leg would silently skip the darwin- and windows-tagged code.
      # After buf generate and the dist placeholder, which the packages need
      # to load. Sonar S8545 does not apply: go run checks the module against
      # sum.golang.org (see the protoc-gen-go-grpc note above).
      - name: govulncheck
        run: go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 -tags purego ./...
    ```
  - `:344` becomes `run: npm ci --ignore-scripts`.
- [ ] **Step 4: Run** `go test -count=1 -tags purego ./internal/buildcheck/ -v`. Expected: all PASS, including `TestBackendJobBuildsServerWithServerGo` (`:381`), `TestServerCheckoutPinnedToSHA` (`:363`, still `850d984`), `TestWorkflowsLeastPrivilege` and `TestEveryJobHasTimeout`. Then run the frontend step as CI does: `(cd frontend && rm -rf node_modules && npm ci --ignore-scripts && npm run test && npm run build)`, which is green with `check-dist` passing.
- [ ] **Step 5: Commit** `ci: blocking govulncheck on all backend legs; npm ci --ignore-scripts (SEC D5, Sonar S6505)`.

### Task C6: Full-suite verification and docs

**Files:**
- Create: `docs/superpowers/specs/2026-10-28-phase-6-9-sec-security-alerts-design.md` (a copy of the approved `design-SEC.md`; change its first-line status to `**Status: approved 2026-10-28**`); `docs/superpowers/plans/2026-10-28-phase-6-9-sec-security-alerts.md` (this plan, with its checkboxes as executed).
- Modify: `docs/superpowers/plans/2026-09-15-phase-3-manual-verification.md`: a new `### 15. Security alerts — Phase 6.9 SEC design §8`, inserted before `## Known, already-documented gaps…` (`:452`) and after the `---` that ends §14. It holds design §8 items 1–8 verbatim in substance, each marked **user** or **after merge**.

- [ ] **Step 1:** Run the client verification suite (full) from a clean `frontend/node_modules`. Expected: every pass criterion "from C4 on"; vitest `594 passed`; govulncheck 0 with GO-2026-5932 only.
- [ ] **Step 2 (cross-lane evidence; only if `$S/vcs-server-sec.exe` exists from S7 Step 2):** `VCS_SERVER_BIN="$S/vcs-server-sec.exe" go test -race -count=1 -tags purego ./internal/voice/ ./internal/session/ -v`. Expected: `ok` for both, `TestIntegrationRelayBetweenTwoClients` PASS. This shows that the cost-10 Go client logs in against the capped, bumped server. Save the output to `$S/sec-cli-integ-vs-sec-server.txt`. If the file is absent, record "deferred to the pin-bump follow-up".
- [ ] **Step 3:** Write the three docs. Then `go test -count=1 -tags purego ./internal/buildcheck/` is `ok`. Commit `docs(phase-6.9): SEC design, plan and manual checks`.
- [ ] **Step 4:** `git log --oneline origin/main..HEAD` shows C1–C6. `git status --short` is empty.

---

## Post-merge follow-up (NOT in either lane): client server-pin bump

It waits for the server SEC PR to merge (D10). Do it as a new last commit on the client `chore/cleanup-sec` branch, or as its own PR if the client PR has already merged:
1. Edit `TestServerCheckoutPinnedToSHA` (`internal/buildcheck/buildcheck_test.go:369`): `want` = the merged server SEC commit (full 40 hex). Run it; it FAILs.
2. `.github/workflows/test.yml:49` `ref:` gets that SHA.
3. Rewrite the comment at `:43-48` to describe the whole range it now covers: `850d984` → the SEC merge, which includes UP's server changes at `df8d9f3` (Wails GA) plus SEC (grpc v1.85.0, quic-go v0.64.1, x/net v0.62.0, bcrypt cap 12). Note that client CI's derived protoc-gen-go moves to v1.36.12 (design A4).
4. Re-run the buildcheck tests and the integration suite against a server built at the merged SHA. Commit `ci: pin the integration server to the SEC merge (<sha>)`.

## Other follow-ups (record in the PR bodies)

- DOC: the umbrella §6.5 corrections (SEC row: dev credentials kept, the wire-visible cap, the port flake moved to SEC, the grpc floor `≥ v1.83.2`).
- SV7: the server README *Client connections* section states the cap of 12.
- User (design §8): Sonar mark-safe and exclusion; secret scanning; Dependabot closure check; the real-network TLS login; the C# peer at cost 12; five consecutive green Windows integration runs.
