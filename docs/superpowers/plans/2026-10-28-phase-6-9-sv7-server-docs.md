# Phase 6.9 / SV7: Server Docs (Client connections, build commands) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give the server README one accurate `## Client connections` section (ports, voice address, client-facing TLS, version floor, login, session lifecycle). Correct its stale build, run and flag instructions. Fix two wrong deploy docs. Fix the dropped `production` tag in the linux/darwin headless release builds.

**Architecture:** There is one server-only lane, `sv7`, on branch `docs/cleanup-sv7`. Almost everything is Markdown. The TDD obligation is **executable docs**: every command the README gains is run before it is written down (Task 2), and every fact is cited to `file:line` in the PR description (D3). The one non-doc change, the Taskfile `-tags` fix, gets its own RED/GREEN check. That check renders the real Taskfile through `wails3 task -dry`; a hand-typed flag list could never fail, so it does not count. SV7 runs in parallel with SEC, **merges after SEC and rebases onto it** (D4). See Task 9, the rebase checkpoint.

**Tech Stack:** Markdown, Taskfile v3 via the wails3 CLI v3.0.0 (derived from go.mod), Go 1.27.3 (`go list`, `go build`, `go vet`, `go test`), protoc 36.x + gqlgen (codegen only), OpenSSL 3.x (recipe check), bash (Git Bash on Windows).

**Spec:** the approved design, scratchpad `design-SV7.md`, approved 2026-10-28 with the user decisions Q1 = A, Q2 = A, Q4 = A ("Execute SV7 and SEC both as workflows"). Executors read §3 (change list, rows 1–17), §5 (plan shape), §6 (gates), §8 (manual list) and "User decisions". Task 8 commits it as server `docs/superpowers/specs/2026-10-28-phase-6-9-sv7-server-docs-design.md`. Umbrella: client `docs/superpowers/specs/2026-10-27-vcs-client-phase-6-9-cleanup-umbrella-design.md:141` (track SV7, DOC-15). SEC design (merge-order dependency): scratchpad `design-SEC.md`, "User decisions".

**Planning bases:** server `origin/main` `df8d9f3`, client `origin/main` `b80b0e6`. All `file:line` citations are against these SHAs and were re-read with `git show origin/main:<file>` while writing this plan.

`$S` is the scratchpad directory `C:/Users/janne/AppData/Local/Temp/claude/D--Projects-vcs-srs-client/61e6be5b-33d5-417e-8854-ccbdd02e4132/scratchpad`. `$WT` is the worktree `D:/Projects/vcs-srs-server-sv7`. All commands run from `$WT` in Git Bash, with `export MSYS_NO_PATHCONV=1` (otherwise Git Bash rewrites `origin/main:path` arguments).

## Global Constraints

- **Files SV7 may change, and nothing else:**
  - `README.md`;
  - `build/linux/Taskfile.yml` lines `:121` and `:123` only, and `build/darwin/Taskfile.yml` lines `:75` and `:79` only (Q4 = A). The GUI `build` task's `BUILD_FLAGS` at `:16` of both files has the same `{{else}}` text and must **not** change (design row 12; the §4.1 probe's blanket `sed` hit it);
  - `docker-compose.dev.yml`: the comment at `:52` only. The `restart:` value at `:53` is unchanged (Q1 = A);
  - `deploy/README.md` (`:97-99`, `:113`, `:131`, `:178-180`);
  - the two new docs files from Task 8.
- **Not changed (design D1, D5, §10; Q1 = A "Drop no :ro mount"):** any Go code, `srs.proto` or other proto, `go.mod`/`go.sum`, `frontend/package*.json`, any workflow or action, the `Dockerfile`, `deploy/docker-compose.*.yml`, `dev/config/*` (`dev/config/control.yaml` is SEC's file, S2068), `example.config.yaml`, and the README protocol/architecture body (`:108-459`) apart from `:273` and the one historical line.
- **Throwaway probes are never committed.** Task 2's probe test file is deleted before any commit, and `git status --porcelain` must be empty at the end of Task 2.
- **`wails3 build` side effects are reverted** (design §4): `build/windows/icons.ico`, `build/darwin/icons.icns`, `frontend/package-lock.json`, plus line-ending-only changes to `go.mod` and `graphql/schema.resolvers.go`. Use `git checkout -- <file>` for each.
- **wails3 CLI = go.mod's version.** Taskfile commands call `wails3` by bare name, so put the derived CLI first on PATH:
  ```bash
  WAILS_V="$(go list -m -f '{{.Version}}' github.com/wailsapp/wails/v3)"
  GOBIN="$S/sv7-bin" go install "github.com/wailsapp/wails/v3/cmd/wails3@${WAILS_V}"
  export PATH="$S/sv7-bin:$PATH"; wails3 version   # expect v3.0.0
  ```
  The global `wails3` on this machine is v3.0.0-beta.26 (design §4.1). Never use it.
- **Stopping a server you started:** use `timeout <s> <cmd>` (kills by PID) or `taskkill //PID <pid> //F`. **Never** kill by image name (`taskkill //IM`), which the auto-mode classifier refuses (design §4 "Not run").
- **Exact heading `## Client connections`** (anchor `#client-connections`, D2). The client DOC track links to it.
- **Wording rules for the README:**
  - every fact in `## Client connections` is one of design rows 2–6, the SEC bcrypt line, or one of the flagged extras F1–F8 below;
  - TLS-loader defects are worded "as of commit `df8d9f3`" (R1);
  - Linux/macOS GUI steps are phrased as instructions, never as "verified" (R2);
  - the client's bcrypt hashing is described as intended (D6).
- **gopls is unreliable here.** Trust only `go build`/`go vet`.
- **Commits** end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Push and PR are separate and not in this plan. The user merges.

### Facts in the README prose beyond design rows 2–6 (flagged for review, each cited)

| # | Fact | Citation |
|---|---|---|
| F1 | GraphQL's API key travels in the `X-API-Key` header; a wrong key gets 401 | `rest/middleware.go:24-27` |
| F2 | A bad, malformed or expired token gets `Unauthenticated "authentication error for <method>: …"` | `control/server.go:556-557` |
| F3 | `wails3 task <os>:build:headless [PRODUCTION=true]` builds the headless binary without running it | `Taskfile.yml:5-7`; `build/windows/Taskfile.yml:62-76`; `build/linux/Taskfile.yml:116-130`; `build/darwin/Taskfile.yml:72-89` |
| F4 | `--file-log` writes `<log-folder>/vcs-server-log.jsonl` in addition to stdout | `shared.go:34-54` |
| F5 | The headless binary always starts its servers (no `--autostart` needed) | `app/app.go:157`; `headless.go:14` (autostart flag discarded) |
| F6 | `deploy/config/` is not shipped and must be created | `git ls-tree origin/main deploy` (only `README.md` and two compose files) |
| F7 | The `:ro` first-start problem also applies to a `clientTLS` pair the server generates under `/certs` | `voiceontrol/utils.go:29-34` (writes both files); `deploy/README.md:165-166` (`/certs/...` paths) |
| F8 | The Go client hashes at `bcrypt.DefaultCost` (10), below the cap | client `internal/session/session.go:744` |

### Server verification suite (from `$WT`; protoc 36.x on PATH; wails3 v3.0.0 first on PATH)

```bash
export MSYS_NO_PATHCONV=1
go install google.golang.org/protobuf/cmd/protoc-gen-go@$(go list -m -f '{{.Version}}' google.golang.org/protobuf)
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2
mkdir -p srspb voicecontrolpb vcsauthpb
protoc --go_out=./srspb --go_opt=paths=source_relative --go-grpc_out=./srspb --go-grpc_opt=paths=source_relative srs.proto
protoc --go_out=./voicecontrolpb --go_opt=paths=source_relative --go-grpc_out=./voicecontrolpb --go-grpc_opt=paths=source_relative control.proto
protoc --go_out=./vcsauthpb --go_opt=paths=source_relative --go-grpc_out=./vcsauthpb --go-grpc_opt=paths=source_relative vcs-auth-plugin.proto
go run github.com/99designs/gqlgen generate
go build -tags headless ./... && go vet -tags headless ./...
for os in linux darwin; do
  GOOS=$os CGO_ENABLED=0 go build -tags headless -buildvcs=false -o /dev/null . &&
  GOOS=$os CGO_ENABLED=0 go build -tags production,headless -trimpath -buildvcs=false -ldflags="-w -s" -o /dev/null . || echo "CROSS FAIL $os"
done
go test -race -count=1 -tags headless ./...
bash "$S/sv7-tags.sh"            # Task 1's script
git status --porcelain           # only intended files
```

Pass means:
- generate exits 0; build and vet print nothing; no `CROSS FAIL`;
- every package is `ok` or `[no test files]`;
- `sv7-tags.sh` prints exactly the GREEN table of Task 1;
- `git status --porcelain` lists nothing outside the allowed files (generated code is gitignored: `*.pb.go`, `graphql/generated/`).

The generate block mirrors `.github/actions/generate/action.yml:62-77`.

## Gates

Every gate below was approved by the user with the design on **2026-10-28** ("Execute SV7 and SEC both as workflows as discussed before.").

| Gate | Where | Approval it rests on |
|---|---|---|
| build: linux/darwin headless release-build `-tags` (`:121/:123`, `:75/:79` only) | Task 1 | Q4 = A; design §6.2 |
| docs widened to `docker-compose.dev.yml:52` (comment) and `deploy/README.md` | Task 7 | Q1 = A ("docs only. Drop no :ro mount") |
| TLS key-loader documented as-is; code fix is a follow-up | Tasks 5, 7 | Q2 = A |
| content of `## Client connections` = rows 2–6 + bcrypt cap line | Task 5 | design §3.1; SEC Q2 = A ("SV7's README documents the cap"). **The prose itself is approved at the plan stage** (§3.1): the reviewer approves Task 5's text and F1–F8. |
| cross-repo: SV7 merges after SEC, rebased on it | Task 9 | D4; §6.3 |
| dependency / proto / workflow / runtime behaviour | none | none are changed (§6.1, §6.4) |
| outward-facing: push, PR; a force-push after rebasing an already-pushed branch | not in this plan | The orchestrator handles them. A `--force-with-lease` after the rebase is a destructive git operation and needs the user's go-ahead at that time. |

## Out of scope (design §10)

- Fixing the PKCS#1-only key loader or the cert overwrite (`voiceontrol/utils.go:20-56`). These are recorded follow-ups (Q2 = A).
- Changing `deploy/docker-compose.central.yml:15` `:ro`, or any `restart:` policy.
- The client advertising DISTRIBUTED (client ROADMAP Phase 9). Raising `MinClientVersion`.
- Any CI change, including a README link checker in CI.
- Rewriting README `:108-459` beyond `:273` and the historical line. Follow-ups: the kick flow at `:325-330` and `:404-410`; the "gRPC" admin surface at `:290`; the `:242` vs `:238` Session ID contradiction.
- The client repo's docs (`CLAUDE.md:96, :119`, `docs/ROADMAP.md:155`, the README link): DOC-03 / DOC-11.
- Graceful listener shutdown (B21-01).

## Review Focus

1. **An operator follows the linux/darwin headless release build and assumes `production` is set.**
   - *Expected:* `PRODUCTION=true` renders `[production headless]`, the dev build `[headless]`, and the GUI builds keep `[production]` / `[]`.
   - *Test:* Task 1, `sv7-tags.sh`. Its GUI rows pin the `:16` trap.
2. **An operator brings a CA-issued PKCS#8 or ECDSA key.**
   - *Expected:* the README's recipe turns a PKCS#8 RSA key into one the real loader accepts, and the README says plainly that ECDSA cannot be used.
   - *Test:* Task 2 Step 4, the throwaway `TestSV7KeyProbe` against `voiceontrol.LoadOrGenerateKeyPair`.
3. **An operator follows the shipped central compose literally on a first start.**
   - *Expected:* `deploy/README.md` tells them that the shipped `:ro` mount blocks cert generation and what to do.
   - *Test:* Task 7 Step 1, grep assertions. Live check: manual item 5.
4. **A client developer copies a link or anchor from the README** (`#client-connections`, SV1 §11, SV2 §9, `deploy/README.md` → `README.md`).
   - *Expected:* every link resolves.
   - *Test:* Task 10 Step 2, `sv7-links.py`.
5. **SEC changes something this section describes** (the bcrypt cap message, the compose file, the key loader, the image user).
   - *Expected:* the rebased README matches SEC's merged code.
   - *Test:* Task 9's checkpoints, each answered from `origin/main` with `file:line`.

---

# Part 1: Lane `sv7` (`vcs-srs-server`, branch `docs/cleanup-sv7`)

### Task 0: Worktree and baseline

- [ ] **Step 1: Create the worktree.**
  ```bash
  git -C D:/Projects/vcs-srs-server fetch origin && git -C D:/Projects/vcs-srs-server worktree add -b docs/cleanup-sv7 D:/Projects/vcs-srs-server-sv7 origin/main
  ```
  Record `git -C "$WT" log -1 --format=%h`. Expected: `df8d9f3` or later.
  - If `origin/main` has moved (for example because SEC already merged), run `git diff --name-only df8d9f3 origin/main`. Re-check by content, not by line number, every `file:line` this plan edits or cites in a listed file.
  - If SEC has already merged, do Task 9's checkpoints now as well as in Task 9.
- [ ] **Step 2: Install the derived wails3 CLI** into `$S/sv7-bin` (Global Constraints) and put it first on PATH. `wails3 version` must print `v3.0.0`. Record `protoc --version` (local 36.0 is fine; CI pins 36.2) and `openssl version` (3.x needed for Task 2 Step 4; if it is missing or 1.1.1, stop and report).
- [ ] **Step 3: Run the server verification suite at baseline**, without the `sv7-tags.sh` line, which does not exist yet. Everything must be green. If anything is red, stop and report it verbatim. Save the `go test` output to `$S/sv7-test-before.txt`.

### Task 1: Taskfile production tag fix (GATE: build, Q4 = A)

**Files:**
- Modify: `build/linux/Taskfile.yml:121, :123`; `build/darwin/Taskfile.yml:75, :79`
- Test (not committed): `$S/sv7-tags.sh`

**Interfaces:** Produces `$S/sv7-tags.sh`. Task 10 re-runs it.

- [ ] **Step 1: Write `$S/sv7-tags.sh`.** It reads the Taskfile through `wails3 task -dry`. For each `task` in `linux:build:headless darwin:build:headless linux:build darwin:build`, and each `p` in `true false`:
  - `out=$(wails3 task -dry "$task" PRODUCTION=$p 2>&1)`;
  - take the last line matching `go build `;
  - collect every `-tags <value>` pair from it with `grep -oE -- '-tags [^ ]+'`;
  - print `"$task PRODUCTION=$p $(GOOS=${task%%:*} go list -e -f '{{context.BuildTags}}' <those pairs> .)"`.

  If no `go build` line is found, print `NO GO BUILD LINE for $task` and exit 1. That is the R5 trip-wire: if v3.0.0's `-dry` behaves unlike beta.26, stop. Do **not** fall back to hand-typed flags. The only fallback is a real build on Linux/WSL with `go version -m bin/vcs-server-headless | grep -- -tags`.
- [ ] **Step 2: Run it (RED).** Expected, exactly:
  ```
  linux:build:headless PRODUCTION=true [headless]
  linux:build:headless PRODUCTION=false [headless]
  darwin:build:headless PRODUCTION=true [headless]
  darwin:build:headless PRODUCTION=false [headless]
  linux:build PRODUCTION=true [production]
  linux:build PRODUCTION=false []
  darwin:build PRODUCTION=true [production]
  darwin:build PRODUCTION=false []
  ```
  The first and third lines are the bug: `production` is dropped because the trailing `-tags headless` at `:121`/`:75` overrides the earlier `-tags production,headless`.
- [ ] **Step 3: Edit the four lines.** Mirror `build/windows/Taskfile.yml:67, :69`:
  - linux `:121` and darwin `:75` become `- go build {{.BUILD_FLAGS}} -o {{.OUTPUT}}` (drop the trailing ` -tags headless`; keep the indentation);
  - linux `:123` and darwin `:79` become, character for character, the windows `:69` value: `BUILD_FLAGS: '{{if eq .PRODUCTION "true"}}-tags production,headless -trimpath -buildvcs=false -ldflags="-w -s"{{else}}-tags headless -buildvcs=false -gcflags=all="-l"{{end}}'`.

  Edit with the Edit tool, anchored on the `build:headless` block. Never use a blanket substitution.
- [ ] **Step 4: Run it (GREEN).** Expected: the same table except `linux:build:headless PRODUCTION=true [production headless]` and `darwin:build:headless PRODUCTION=true [production headless]`. The GUI rows must be unchanged.
- [ ] **Step 5: Scope check.** `git diff --stat` shows exactly `build/darwin/Taskfile.yml | 4 ++--` and `build/linux/Taskfile.yml | 4 ++--`. `git diff -U0 | grep '^@@'` shows hunks only at linux 121 and 123, and darwin 75 and 79. Then run the suite's cross-build loop: no `CROSS FAIL`.
- [ ] **Step 6: Commit.** The message is `build: keep the production tag in linux/darwin headless release builds (SV7 Q4)`. The body says:
  - the trailing `-tags headless` overrode `-tags production,headless`;
  - it now mirrors `build/windows/Taskfile.yml:69`;
  - there is no runtime change today, because no file uses a `production` constraint (`git grep '^//go:build'`);
  - CI does not call these targets.

### Task 2: Execute every command the README will gain (no commit)

**Files:** none committed. Evidence goes to `$S/sv7-evidence.md`, which the PR description quotes.

**Interfaces:** Produces the observed outputs that Tasks 4, 5 and 7 cite. If any observation contradicts the text this plan gives those tasks, **stop and report**; do not reword silently.

- [ ] **Step 1: Codegen from scratch.** Run `rm -rf srspb voicecontrolpb vcsauthpb graphql/generated`, then the suite's codegen block, `mkdir -p` included. Expected: exit 0. Then run the three `protoc` lines **without** `mkdir -p` in a fresh `rm -rf` state, and record the error text (the README says protoc fails when the output directory is missing; design row 7). Restore with the full block.
- [ ] **Step 2: Headless by hand.** Run `go build -tags headless -o bin/vcs-server-headless.exe .` (the README shows `bin/vcs-server-headless`; Windows adds `.exe`). Then `./bin/vcs-server-headless.exe -h 2>&1 | tee "$S/sv7-flags.txt"`. Expected flags: `-autostart`, `-banned`, `-config`, `-file-log`, `-global`, `-log-folder`, `-mode`, with the defaults of `shared.go:23-30`.
- [ ] **Step 3: Live headless runs in a scratch dir** (`D=$S/sv7-run`, created empty). Each run is bounded by `timeout` and logs to a file:
  - (a) **Missing config.** `timeout 8 ./bin/vcs-server-headless.exe --config "$D/a.yaml" --banned "$D/b.json" --log-folder "$D/log" > "$D/a.log" 2>&1`. Expect exit 124. Then:
    - `$D/a.yaml` exists, with `coalitions: []`, `port: 80` under `http`, and `/path/to/ecdsa_key.pem`;
    - `a.log` contains `client-facing gRPC port is PLAINTEXT` (`control/server.go:245`). This upgrades design §8 item 1's first half from code reading to an observation.
  - (b) **Half config.** Copy `a.yaml` to `c.yaml`. Set `servers.http.port: 18080` and `servers.control.port: 15002`, and add `clientTLS: {certificateFile: "$D/srs-cert.pem"}`. Run it the same way. Expect:
    - `c.log` contains `clientTLS.certificateFile is set but clientTLS.privateKeyFile is empty: configure both or neither` (`control/server.go:248`);
    - the process stays up until `timeout` (exit 124).
  - (c) **Generation.** In `c.yaml`, add `privateKeyFile: "$D/srs-key.pem"` and `serverName: vcs.example.net`, then run again. Expect `c.log` to contain `client-facing gRPC port is TLS`, and both `.pem` files to exist. Then `openssl x509 -in "$D/srs-cert.pem" -noout -text | grep -A1 'Subject Alternative Name'` lists `localhost`, `vcs.example.net`, `127.0.0.1` and `0:0:0:0:0:0:0:1`.

  If port 80 is taken in (a), the log shows an HTTP bind error. That does not affect the asserted lines; record it.
- [ ] **Step 4: TLS key recipe against the real loader (Review Focus 2).** In `$D/keys`:
  ```bash
  openssl genrsa -out key.pem 2048                                   # BEGIN PRIVATE KEY (PKCS#8)
  openssl rsa -in key.pem -traditional -out key-pkcs1.pem            # the README recipe
  # (an `openssl req -x509 -subj` line is NOT part of the README recipe: it fails on some OpenSSL configs)
  openssl ecparam -name prime256v1 -genkey -noout -out ec.pem
  openssl rsa -in ec.pem -traditional -out ec-out.pem; echo "exit=$?"  # expect "Not an RSA key", non-zero
  head -1 key.pem key-pkcs1.pem
  ```
  Then write the throwaway `voiceontrol/zz_sv7_probe_test.go` (package `voiceontrol`, build tag-free). `TestSV7KeyProbe` reads `SV7_KEYS` from the environment and `t.Skip`s when it is empty. It then logs:
  - `LoadOrGenerateKeyPair(key.pem, cert.pem)` → `err` non-nil, containing `failed to parse private key`;
  - `LoadOrGenerateKeyPair(key-pkcs1.pem, cert.pem)` → `err == nil`;
  - **cert overwrite:** copy `cert.pem` to `only-cert.pem`, call it with a missing key path, and `t.Logf("overwritten=%v", !bytes.Equal(before, after))` → `overwritten=true`.

  Run `SV7_KEYS="$D/keys" go test -tags headless ./voiceontrol/ -run TestSV7KeyProbe -v`. Then **delete the file** (`rm voiceontrol/zz_sv7_probe_test.go`).
- [ ] **Step 5: Taskfile commands.**
  - `timeout 300 wails3 task headless-standalone > "$D/task.log" 2>&1`. Expect a build, then the PLAINTEXT WARN, then exit 124.
  - Afterwards, `tasklist //FI "IMAGENAME eq vcs-server-headless.exe"`. If a child survived, kill it by PID with `taskkill //PID <pid> //F`.
  - Then `wails3 task -dry run`. Expect `bin\\vcs-server.exe --autostart`, with **no** build step, which confirms "run does not build".
- [ ] **Step 6: GUI build and run (Windows).**
  - `time wails3 build`. Expect `bin/vcs-server.exe`, about 2–3 min.
  - `git status --porcelain` should show the §4 side effects (`icons.ico`, `icons.icns`, `frontend/package-lock.json`, and possibly `go.mod` and `graphql/schema.resolvers.go` line endings). Record the list for the README sentence, then `git checkout --` each one.
  - `timeout 20 wails3 task run`. Expect webview2 process events (do not claim a window was seen unless observed) and exit 124. Then clean up by PID as in Step 5.
  - Confirm `wails3 dev -help` shows default `-config ./build/config.yml`.
- [ ] **Step 7: Clean state.** `git status --porcelain` is empty (Task 1's commit is already in). `bin/`, `log/`, `config.yaml` and `.task/` are gitignored (`.gitignore`). Remove `$D`.

### Task 3: README stale lines (design row 11)

**Files:** Modify `README.md:17-20`, `:273`

- [ ] **Step 1: Failing check.** `grep -c 'Live development section' README.md` → `1`. `grep -c 'spawns a goroutine per received datagram' README.md` → `1`.
- [ ] **Step 2: Edit.**
  - Replace `:19-20` (the paragraph under `### Note about Wails3`) with:
    > Wails3 is used for the GUI and for the Taskfile that builds the different server modes. The headless server needs neither: after code generation it builds with plain `go build` (see [Building](#building)).
  - Replace the `:273` bullet with:
    > - **Flooding.** The read loop hands each datagram to one of a fixed set of shard workers, chosen by a hash of the sender ID, each with a 256-packet queue. When a shard's queue is full the packet is dropped and a rate-limited WARN is logged. There is no per-sender rate limit, so UDP flood mitigation is still not implemented in-process.

    (`voice/shard.go:12-27, :83-99`; `voice/server.go:190`.)
- [ ] **Step 3: Check.** Both greps → `0`. `grep -c 'shard' README.md` → at least `1`.
- [ ] **Step 4: Commit** `docs(readme): drop the dangling live-development reference; describe sharded voice dispatch (SV7)`.

### Task 4: README Development / Building / Running / Flags (design rows 7–10)

**Files:** Modify `README.md:28-36` (GUI mode), `:45-48` (headless), `:68-72` (config), `:74-96` (codegen, Building, Running, Flags)

- [ ] **Step 1: Failing check.** Each of these greps returns ≥ 1 today and must return `0` afterwards: `'wails build\`'`, `'build/bin'`, `'by using the \`--\` separator'`, `'Default is \`true\`~~'`. And `grep -c 'go run github.com/99designs/gqlgen generate' README.md` → `0` today, `1` afterwards.
- [ ] **Step 2: Edit `### Standalone GUI Mode`.** Delete `:33-36` ("Or" and `wails3 task run`). After the `wails3 dev` block, add: `` `wails3 dev` builds and runs the GUI with live reload, using `./build/config.yml`. `` (`Taskfile.yml:33`).
- [ ] **Step 3: Edit `### Standalone Headless Mode`.** After `:48`, add:
  > Each `headless-*` task first builds `bin/vcs-server-headless` (`bin/vcs-server-headless.exe` on Windows), then starts it with `--mode=standalone`, `--mode=control` or `--mode=voice`. The process runs until you stop it.

  (`build/*/Taskfile.yml` `headless-*` with `deps: build:headless`.)
- [ ] **Step 4: Edit `### Config File`.** Append:
  > If the file named by `--config` does not exist, the server writes one with defaults: no coalitions, the admin HTTP port on 80, the client gRPC and voice ports on 5002, and placeholder token-key paths (`/path/to/ecdsa_key.pem`) that you must change.

  (`state/settings.go:136-195`; Task 2 Step 3a.)
- [ ] **Step 5: Replace `### Building Protobuf files` (`:74-80`) with `### Code generation`:**
  ````markdown
  ### Code generation

  The protobuf packages (`srspb/`, `voicecontrolpb/`, `vcsauthpb/`) and the GraphQL code (`graphql/generated/`) are generated, not checked in. Every `wails3 task` build generates them first; that step also runs `go mod tidy`. To generate them by hand, as CI does (`.github/actions/generate/action.yml`), install:

  - `protoc` 36.2 (the version CI and the `Dockerfile` pin);
  - `protoc-gen-go` at the `google.golang.org/protobuf` version in `go.mod`, and `protoc-gen-go-grpc` v1.6.2;
  - Node 22, for the GUI frontend only.

  ```bash
  go install google.golang.org/protobuf/cmd/protoc-gen-go@$(go list -m -f '{{.Version}}' google.golang.org/protobuf)
  go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2
  ```

  Then, from the repository root (protoc fails if an output directory is missing, hence `mkdir -p`):

  ```bash
  mkdir -p srspb voicecontrolpb vcsauthpb
  protoc --go_out=./srspb --go_opt=paths=source_relative --go-grpc_out=./srspb --go-grpc_opt=paths=source_relative srs.proto
  protoc --go_out=./voicecontrolpb --go_opt=paths=source_relative --go-grpc_out=./voicecontrolpb --go-grpc_opt=paths=source_relative control.proto
  protoc --go_out=./vcsauthpb --go_opt=paths=source_relative --go-grpc_out=./vcsauthpb --go-grpc_opt=paths=source_relative vcs-auth-plugin.proto
  go run github.com/99designs/gqlgen generate
  ```
  ````
  Citations: `.github/actions/generate/action.yml:62-77`; `Dockerfile:26-27, :31`; `build/Taskfile.yml:4-11`; `go.mod:19`; `.github/workflows/test.yml:73`.
- [ ] **Step 6: Replace `## Building` (`:82-84`):**
  ```markdown
  ## Building

  - **GUI:** `wails3 build` builds `bin/vcs-server` (`bin/vcs-server.exe` on Windows). It also rewrites `build/windows/icons.ico`, `build/darwin/icons.icns` and `frontend/package-lock.json`; do not commit those changes by accident. Linux and macOS GUI builds need the packages from [Installation](#installation).
  - **Headless, via the Taskfile:** `wails3 task <os>:build:headless`, where `<os>` is `windows`, `linux` or `darwin`. Add `PRODUCTION=true` for a release build. The output is `bin/vcs-server-headless`.
  - **Headless, without Wails:** after [code generation](#code-generation), `go build -tags headless -o bin/vcs-server-headless .`
  - **Checks CI runs:** `go vet -tags headless ./...` and `go test -race -count=1 -tags headless ./...`.

  Releases ship only the Docker image (`Dockerfile`, built with `-tags production,headless`). There is no GUI release artifact.
  ```
  The side-effect list must equal what Task 2 Step 6 recorded. Citations: `Taskfile.yml:11, :15-18`; `build/*/Taskfile.yml`; `.github/workflows/test.yml:39, :45`; `.github/workflows/release.yml`; `Dockerfile:46-51`.
- [ ] **Step 7: Replace `## Running` and `### Flags` (`:86-96`):**
  ```markdown
  ## Running

  Binaries are written to `bin/`. `wails3 task run` starts the binary already built for your OS; it does not build. On Windows it passes `--autostart`; on Linux and macOS it does not. You can also start a binary directly with the flags below.

  ### Flags

  Flags go straight to the binary, for example `bin/vcs-server-headless --mode control --config /etc/vcs/control.yaml`:

  - `--config /path/to/config.yaml`: the config file. Default `config.yaml`.
  - `--banned /path/to/banned.json`: the banned clients file. Default `banned_clients.json`.
  - `--log-folder /path/to/logs`: the log folder. Default `log`.
  - `--file-log`: also write logs to `<log-folder>/vcs-server-log.jsonl`. Default `true`; turn it off with `--file-log=false`.
  - `--autostart`: GUI only. Start the servers when the app starts. Default `false`. The headless binary always starts them.
  - `--mode standalone|control|voice`: headless only. Default `standalone`.
  - `--global`: headless only, and only with `--mode voice`. Run as the global-frequency voice node. Default `false`.
  ```
  The flag list must equal `$S/sv7-flags.txt` (Task 2 Step 2). Citations: `shared.go:23-31, :34-54`; `headless.go:14, :30-33`; `app/app.go:157`; `build/windows/Taskfile.yml:58-60`; `build/linux/Taskfile.yml:112-114`; `build/darwin/Taskfile.yml:68-70`.
- [ ] **Step 8: Check.** Re-run the Step 1 greps; they give the "afterwards" values. `grep -n '^## \|^### ' README.md` shows `### Code generation`, `## Building`, `## Running` and `### Flags`, in that order, before `## Improvements`.
- [ ] **Step 9: Commit** `docs(readme): Wails v3 build, codegen, run and flag instructions (SV7)`.

### Task 5: README `## Client connections` (design rows 1–6; prose approved here)

**Files:** Modify `README.md`: insert after `### Flags`, before `## Improvements` (`:98` at base)

**Interfaces:** Produces the anchor `#client-connections`, which Task 7 (`deploy/README.md`) and the client DOC track link to. Also `#client-facing-tls` (Task 7 links it).

- [ ] **Step 1: Failing check.** `grep -c '^## Client connections$' README.md` → `0`.
- [ ] **Step 2: Insert this text verbatim** (the inline citations in brackets after each paragraph are for the reviewer; they do **not** go into the README, they go into the PR description per D3):

  ````markdown
  ## Client connections

  What a client (the VCS client or another SRS client) needs to reach this server.

  ### Ports

  | Listener | Config key | Default | Modes | Serves |
  |---|---|---|---|---|
  | Client gRPC (TCP) | `servers.control` | `0.0.0.0:5002` (the Docker and deploy configs use `14446`) | standalone, control | `SRSService`, `AuthService`, `grpc.health.v1`, gRPC reflection |
  | Voice (UDP) | `servers.voice` | `0.0.0.0:5002` | standalone, voice | voice packets |
  | Admin HTTP (TCP) | `servers.http` | `0.0.0.0:80` | standalone, control | GraphQL at `POST /api/v1/graphql`, protected by `api.key` in the `X-API-Key` header; an empty `api.key` disables it with HTTP 503 |
  | VoiceControl gRPC (TCP) | `voiceControl.listenHost` / `voiceControl.port` | `0.0.0.0:14448` | control | voice nodes only, always TLS; never clients |

  TCP 5002 and UDP 5002 do not clash. The client gRPC port accepts keepalive pings no more often than every 60 s; a client that pings more often is sent GOAWAY.

  ### Voice address

  `SyncClient` returns `coalition_voice_addr` and `global_voice_addr`.

  - **Standalone:** both are empty, because no voice node registers, so clients derive the address themselves. The VCS client uses `[voice] port` only together with `[voice] host`; with no host it uses the server URL's host and port 5002. If you change `servers.voice.port` on a standalone server, every VCS client must set both `[voice] host` and `[voice] port`.
  - **Distributed:** each voice node registers its `voiceControl.publicAddr`, either `host:port` (the port wins, e.g. a NAT mapping) or a bare host (the port comes from `servers.voice.port`). An empty value, `0.0.0.0` / `::`, a missing host or a bad port refuses registration. The control node hands `host:port` to clients, so it must be a name or address clients can reach, not a Docker service name.

  A voice binding that receives no packets is removed after 60–90 s.

  ### Client-facing TLS

  ```yaml
  clientTLS:
    certificateFile: /certs/srs-cert.pem
    privateKeyFile:  /certs/srs-private-key.pem
    serverName:      vcs.example.net
  ```

  `clientTLS` is a top-level block, separate from `voiceControl`, so an admin settings save cannot erase it.

  - **Block omitted:** the client port serves plaintext and the server logs a WARN at startup. The VCS client refuses plaintext for any host that is not loopback.
  - **Only one of the two files set:** the client port stays down, and the error names the missing field. The process and the admin HTTP port stay up.
  - **Key file absent:** the server generates a self-signed RSA-2048 certificate, valid for 365 days and not a CA, with the SANs `localhost`, `127.0.0.1`, `::1` and `serverName` (an IP SAN if `serverName` is an IP). Both files are written with mode 0600, reused on later starts, and never renewed.
  - **Certificate present, key absent:** both are regenerated and the existing certificate is **overwritten**.
  - **Key present, certificate absent:** the client port stays down with an error.
  - **Your own certificate:** as of commit `df8d9f3` the key must be a PKCS#1 RSA key (`-----BEGIN RSA PRIVATE KEY-----`). PKCS#8 keys (`-----BEGIN PRIVATE KEY-----`) are refused, and ECDSA keys cannot be used at all. Convert an RSA key with OpenSSL 3: `openssl rsa -in key.pem -traditional -out key-pkcs1.pem`. OpenSSL 1.1.1 has no `-traditional` and already writes PKCS#1: `openssl rsa -in key.pem -out key-pkcs1.pem`. With any other tool, check that the output starts with `-----BEGIN RSA PRIVATE KEY-----`.

  Clients verify the hostname they dial against the certificate. With a self-signed certificate, copy only the certificate (never the key) to each client and point the VCS client's `tls_ca_file` at it. A failed handshake is logged at WARN; when the client spoke plaintext, the log line says so.

  ### Client version floor

  `InitAuth` refuses a client whose reported version is below `srs.MinClientVersion` (`v0.1.0`), compared as semver. The leading `v` is optional, and an empty or invalid version is refused with `Unsupported client version "<version>"; this server requires v0.1.0 or newer`. Raise the floor only in a deliberate PR, and tell the client repository (the VCS client reports `0.1.0`).

  ### Login

  - `InitAuth` also refuses a client whose supported distribution modes do not include the server's. The VCS client advertises STANDALONE only, so **it cannot log into a control-mode server today** (client ROADMAP, Phase 9).
  - Defaults: `security.enableGuestAuth: true`, `security.enablePluginAuth: false`.
  - `GuestLogin`: the name is 1–32 bytes and the unit ID matches `^[A-Z0-9]{2,4}$`. The password field carries a bcrypt hash of the coalition password, made by the client; the server compares it with each coalition's plaintext password and the first match wins.
  - The server accepts a bcrypt cost of at most 12. A hash with a higher cost, or a value that is not a bcrypt hash, is refused with a message starting `Invalid password hash` and logged at WARN. Clients must hash at cost 12 or lower (Go's `bcrypt.DefaultCost` is 10).
  - AuthService refusals are `Success: false` responses, not gRPC errors.
  - Pending logins (an `InitAuth` not yet redeemed) live 5 min, re-armed by each login step, and are swept every 60 s. At most 10,000 exist in total and 32 per client IP; over either cap, `InitAuth` answers with a "busy" / "too many pending logins" message.

  ### Session lifecycle

  | The client sees | Meaning |
  |---|---|
  | `Unauthenticated "session not found for <method>: log in again"` (any RPC) or `Unauthenticated "session not found: log in again"` (`SubscribeToUpdates`) | The session is gone: log in again. |
  | `Unauthenticated "authentication error for <method>: …"` | The token is bad, malformed or expired: log in again. |
  | `PermissionDenied` | The token's role is too low for this method. |
  | `Internal "token verification unavailable"` | The server cannot verify tokens right now. The session is not lost. |
  | Update stream ends with `Unauthenticated "session ended: <reason>"`, where the reason is `disconnected`, `kicked`, `banned`, `stale` or `stream lost` | The server removed the session. A kick or ban first sends a final `SERVER_ACTION` on the stream. |
  | Update stream ends with `Aborted "superseded by a newer subscription"` | A newer `SubscribeToUpdates` for the same session replaced this stream. Do not resubscribe. |
  | Update stream ends with `ResourceExhausted "update stream fell behind: resubscribe and call SyncClient"` | The stream overflowed; the session is intact. Resubscribe, then call `SyncClient`. |

  - **Grace:** a session without an update stream survives 120 s, counted from the later of the stream's end and its last unary RPC. The reaper runs every 15 s, so removal happens 120–135 s after that. Any RPC, including `Ping`, resets the clock.
  - **Stale:** a session with a live stream but no RPC for 10 min is removed (`session ended: stale`).
  - **Subscription ack:** once a subscription is registered, the server sends empty response headers. A client that resubscribes waits for them before calling `SyncClient`.
  - A `SERVER_ACTION` is delivered only to the client it targets.

  The full contract (C1–C17) is in [SV1 §11](docs/superpowers/specs/2026-10-27-phase-6-9-sv1-session-liveness-design.md#11-cross-track-contracts) and [SV2 §9](docs/superpowers/specs/2026-10-27-phase-6-9-sv2-state-event-correctness-design.md#9-cross-track-contracts).
  ````

  Reviewer citation map (for the PR description, D3):
  - **Ports:** `state/settings.go:13-17, :144-157`; `control/server.go:138-151, :166-169, :197-208`; `rest/middleware.go:19-27`; `app/app.go:170-184`; `app/server_control.go:41-53`; `dev/config/control.yaml:7`; `deploy/docker-compose.central.yml:10-12`.
  - **Voice address:** `srs/srs_service.go:165-176`; `control/server.go:181, :193`; `voiceontrol/service.go:122-136, :192`; `voiceontrol/client.go:440-468`; `example.config.yaml:40`; client `internal/voice/endpoint.go:21, :55-76`; `voice/server.go:572-587`.
  - **TLS:** `state/settings.go:109-130`; `control/server.go:153-160, :242-284`; `voiceontrol/utils.go:19-56, :90-120`; client `internal/session/dial.go:89-112`; Task 2 Steps 3–4.
  - **0600 and GUI (PR description):** cite `voiceontrol/utils.go:29,32` as code reading; the file mode and the GUI window were not observed (Windows, no screen). Write 'webview2 started', not 'a window opens'.
  - **Version / Login:** `srs/utils.go:13-60`; `srs/auth_service.go:48-58, :292-329, :448-501, :967-986`; client `internal/auth/auth.go:34`, `internal/version/version.go:9`, `internal/session/session.go:744`; SEC's merged cap (Task 9).
  - **Lifecycle:** `control/server.go:524-525, :548-557`; `srs/session.go:55-61, :78-94, :166-201`; `srs/srs_service.go:74, :161, :186-197, :418-422, :722-732`; `app/clients.go:233-235`.
- [ ] **Step 3: Check.**
  - `grep -c '^## Client connections$' README.md` → `1`;
  - `grep -c '^### Client-facing TLS$' README.md` → `1`;
  - `grep -n '^## ' README.md` shows `## Running`, then `## Client connections`, then `## Improvements`, in that order.
- [ ] **Step 4: Commit** `docs(readme): Client connections section: ports, voice address, clientTLS, version floor, login, session lifecycle (SV7)`.

### Task 6: README historical-design line (design row 17)

**Files:** Modify `README.md`, directly under `## Distributed VOIP System Architecture` (`:276` at base)

The false statements row 17 names (`:290`, `:325-330`, `:404-410`) are all in this section, so the line goes here. `## Voice Communication Protocol` (`:171`) carries the SV-era trust model, which is accurate, and is not marked. The `:238`/`:242` contradiction stays a follow-up (§10).

- [ ] **Step 1: Insert** a blank line, then:
  > *Design notes from before the implementation. Where they differ from [Client connections](#client-connections), that section is authoritative.*
- [ ] **Step 2: Check.** `grep -A2 '^## Distributed VOIP System Architecture' README.md` shows the line. `git diff --stat` shows 2 insertions.
- [ ] **Step 3: Commit** `docs(readme): mark the distributed-architecture notes as historical (SV7)`.

### Task 7: Deploy docs (design rows 13–16; Q1 = A, Q2 = A)

**Files:**
- Modify: the `docker-compose.dev.yml:52` comment (one line becomes a short comment block; `:53` `restart: on-failure` is unchanged)
- Modify: `deploy/README.md:97-99`, `:113`, `:131`, `:178-180`

- [ ] **Step 1: Failing checks.**
  - `grep -c 'retries if control hasn.t written its cert' docker-compose.dev.yml` → `1`;
  - `grep -cE 'publicAddr: voice-(eu|global)' deploy/README.md` → `2`;
  - `grep -c 'BEGIN RSA PRIVATE KEY' deploy/README.md` → `0`;
  - `grep -c 'docker-compose.central.yml. already mounts' deploy/README.md` → `0`.
- [ ] **Step 2: `docker-compose.dev.yml:52`.** Replace the comment line with (4-space indent, as `:52`):
  ```yaml
      # restart: on-failure only covers crashes. A voice node does not exit when
      # control's cert is missing (it logs the error and runs without voice) or
      # when control is unreachable (it retries forever). If this node started
      # before control wrote /certs/voicecontrol-cert.pem, run:
      #   docker compose -f docker-compose.dev.yml restart voice-eu voice-global
  ```
  Citations: `voice/server.go:170-175`; `app/server_control.go:182-189`; `headless.go:44`; `voiceontrol/client.go:186-205, :255-287`.
- [ ] **Step 3: `deploy/README.md:97-99`.** After the existing paragraph, add:
  > The shipped `docker-compose.central.yml` already mounts `./certs:/certs:ro`, so a first start cannot write these files. For the first start, remove `:ro` from the `control` service's `./certs` line, start once, then restore it. Alternatively, create the files beforehand. The same applies to a `clientTLS` pair you let the server generate (see [Client-facing TLS](#client-facing-tls)). `deploy/config/` is not shipped; create it with the files below.

  (`deploy/docker-compose.central.yml:15`; F6, F7.)
- [ ] **Step 4: `deploy/README.md:113, :131`.**
  - `:113` becomes `  publicAddr: vcs.example.com:5002  # Public host[:port] clients use to reach this node`.
  - `:131` becomes `  publicAddr: vcs.example.com:5003`.
  - After the `:101-115` block, add:
    > `publicAddr` is handed to clients as `host:port`. Clients outside the compose network cannot resolve the service names `voice-eu` / `voice-global`, so use the host's public name or IP (see [Voice address](../README.md#voice-address)).

  Citations: `voiceontrol/client.go:440-468`; `voiceontrol/service.go:192`; `srs/srs_service.go:165-176`.
- [ ] **Step 5: `deploy/README.md:178-180`.** After "…with no configuration at all.", add:
  > As of commit `df8d9f3` the server loads only **PKCS#1 RSA** private keys (`-----BEGIN RSA PRIVATE KEY-----`). Most CAs and `openssl genrsa` on OpenSSL 3 produce PKCS#8 (`-----BEGIN PRIVATE KEY-----`). Convert with OpenSSL 3: `openssl rsa -in key.pem -traditional -out key-pkcs1.pem`; on OpenSSL 1.1.1: `openssl rsa -in key.pem -out key-pkcs1.pem`. ECDSA keys cannot be used today (`openssl rsa` answers `Not an RSA key`). Details: [Client-facing TLS](../README.md#client-facing-tls).

  (`voiceontrol/utils.go:47-54`; Task 2 Step 4.)
- [ ] **Step 6: Checks.**
  - The Step 1 greps now give `0`, `0`, `≥1` and `1`.
  - **Comment-only compose diff:** `git diff -U0 docker-compose.dev.yml | grep -E '^[+-][^+-]' | grep -vE '^[+-]\s*#'` prints nothing.
  - If `docker` is installed: `docker compose -f docker-compose.dev.yml config -q` exits 0. Otherwise record "docker not available".
  - `git diff --stat` lists only `docker-compose.dev.yml` and `deploy/README.md`.
- [ ] **Step 7: Commit** `docs(deploy): voice restart comment, public publicAddr, first-start :ro note, PKCS#1 key requirement (SV7 Q1, Q2)`.

### Task 8: Commit the SV7 design and plan to the server repo

**Files:**
- Create: `docs/superpowers/specs/2026-10-28-phase-6-9-sv7-server-docs-design.md` (copy of `$S/design-SV7.md`)
- Create: `docs/superpowers/plans/2026-10-28-phase-6-9-sv7-server-docs.md` (copy of this plan, with checkboxes as executed)

These follow SV1–SV5 and UP, which keep their specs and plans in the server repo.

- [ ] **Step 1:** Copy both files, then fix the first line of the spec copy to read `**Status: approved 2026-10-28** (user decisions at the end).` The file is otherwise unchanged.
- [ ] **Step 2: Commit** `docs(phase-6.9): SV7 design and plan`.

### Task 9: Rebase checkpoint onto SEC (after SEC merges)

SV7 merges **after** SEC (D4). Run this task once SEC's server PR is merged to `origin/main`.
- If SEC has not merged when the lane reaches here, run Task 10 on the un-rebased branch, report the lane as **"ready for review; blocked on SEC for merge"**, and return to this task when SEC merges.
- After rebasing, run Task 10 again in full.

- [ ] **Step 1: Rebase.** `git fetch origin && git rebase origin/main`. Expected: no conflicts, because SEC's file list (design D4) does not overlap SV7's. On any conflict, stop and report the conflicting files; do not resolve README facts from memory.
- [ ] **Step 2: Overlap check.** `git diff --name-only df8d9f3 origin/main` ∩ {`README.md`, `build/linux/Taskfile.yml`, `build/darwin/Taskfile.yml`, `docker-compose.dev.yml`, `deploy/README.md`} must be empty. If it is not, re-verify every SV7 line in the overlapping file against `origin/main`.
- [ ] **Step 3: Checkpoints.** Answer each one from `origin/main` and cite `file:line` in `$S/sv7-evidence.md`:
  - **bcrypt cap (SEC Q2 = A):** run `git grep -n 'Invalid password hash' origin/main -- srs utils` and find the cap constant (`git grep -nE 'bcrypt.*12|12.*bcrypt|MaxCost' origin/main -- srs utils`).
    - If the value is 12 and the message starts `Invalid password hash`: keep the Login bullet as written.
    - If the value or the prefix differs: edit the bullet to match the code.
    - If SEC shipped no cap: delete the bullet.
    - Also confirm the WARN log call exists next to the refusal.
  - **S2068:** decided comment-only (SEC D8, D-f). `git diff df8d9f3 origin/main -- docker-compose.dev.yml` is empty, and `dev/config/control.yaml:16,20` still hold the literal dev passwords. Otherwise, stop and report: env variables would be undesigned scope for SV7.
  - **Image user (SEC Q4 = A, no nonroot):** `Dockerfile` still ends `FROM gcr.io/distroless/static-debian12` with no `:nonroot`, so no UID advice is needed. If it changed, stop and report.
  - **Key loader:** `git diff df8d9f3 origin/main -- voiceontrol/utils.go control/server.go state/settings.go` is empty, so the PKCS#1, overwrite and plaintext-WARN text and the "as of `df8d9f3`" wording stay true. Otherwise re-check each TLS bullet against the new code.
  - **Session/auth strings:** `git diff df8d9f3 origin/main -- srs/session.go srs/utils.go` is empty, and `srs/auth_service.go` changes only in GuestLogin's password path. Otherwise re-check the Version/Login/Lifecycle text.
  - **protoc-gen-go:** `go.mod` now pins `google.golang.org/protobuf v1.36.12` (SEC Q1). The README states the rule, not the number, so no edit is needed. Confirm with `grep -c 'v1.36.1' README.md` → `0`.
- [ ] **Step 4:** If Step 3 changed the README, commit `docs(readme): align Client connections with SEC's merged bcrypt cap (SV7)`. Then run Task 10.

### Task 10: Link check and full-suite verification

- [ ] **Step 1: Run the server verification suite** (full, with `sv7-tags.sh`). Expected: as "Pass means". `sv7-tags.sh` prints the Task 1 GREEN table. Compare the `go test` package list with `$S/sv7-test-before.txt`: the same packages are `ok`.
- [ ] **Step 2: Link check.** Write `$S/sv7-links.py`, run with `python -I`, taking `$WT` as its argument. For `README.md` and `deploy/README.md`, it extracts every Markdown link target that is relative (no scheme), splits each into path and `#anchor`, and checks:
  - the path exists in the worktree;
  - the anchor is among the GitHub slugs of the target file's headings. Slug rule: lowercase; drop every character that is not a letter, digit, space or `-`; spaces become `-`.

  It prints `OK <n> links` or one `BROKEN <file>: <target>` line per failure, and exits 1 on any failure. Expected: `OK`, including `#client-connections`, `#client-facing-tls`, `#voice-address`, `#building`, `#code-generation`, `#installation`, `../README.md#client-facing-tls`, `../README.md#voice-address`, and both `docs/superpowers/specs/...#11-cross-track-contracts` / `#9-cross-track-contracts`.
- [ ] **Step 3: Scope check.** `git diff --name-only origin/main...HEAD` lists exactly:
  - `README.md`, `build/linux/Taskfile.yml`, `build/darwin/Taskfile.yml`, `docker-compose.dev.yml` and `deploy/README.md`;
  - the two Task 8 docs.

  `git diff origin/main...HEAD -- '*.go' go.mod go.sum .github Dockerfile '*.proto'` is empty. `git log --oneline origin/main..HEAD` shows the commits of Tasks 1, 3, 4, 5, 6, 7 and 8 (and 9, if any).
- [ ] **Step 4: Fact pass.** For each README bullet under `## Client connections`, the PR description's citation map (Task 5 Step 2, plus the Task 9 evidence) has a `file:line` entry. List F1–F8 explicitly as "beyond design rows 2–6".
- [ ] **Step 5: Clean up.**
  - Remove `$S/sv7-bin` and `$S/sv7-run`. Keep `sv7-tags.sh`, `sv7-links.py` and `sv7-evidence.md` for the PR description.
  - `git status --porcelain` is empty.
- [ ] **Step 6:** Run superpowers:verification-before-completion, then superpowers:requesting-code-review on `origin/main..HEAD`. The brief:
  - "docs + 4 Taskfile lines only";
  - "every Client-connections fact cited";
  - "F1–F8 flagged";
  - "GUI `:16` lines untouched".

  Fix findings in new commits and re-run Steps 1–3.
- [ ] **Step 7: Report.**
  - The lane is ready for push/PR (done separately) and merges after SEC.
  - **Hand-off to the client DOC track (design §6.3):** the anchor is `https://github.com/FPGSchiba/vcs-srs-server/blob/main/README.md#client-connections`. DOC-03 should also mark client `CLAUDE.md:96` and `docs/ROADMAP.md:155` (the version landmine) resolved by server `063f6c5`. Client `CLAUDE.md:119` lacks the PKCS#1-only and overwrite caveats; that is DOC's call.
  - Report the follow-ups (design §10): the key-loader fix (accept any key `tls.X509KeyPair` accepts; never overwrite an existing cert), and the README `:238/:242`, `:290`, `:325-330` and `:404-410` rewrites.

---

## Manual verification (for the orchestrator to merge into the Phase 6.9 manual-verification checklist; design §8)

1. Headless standalone, no `clientTLS`: the log shows the PLAINTEXT WARN. With only `certificateFile` set: the log shows "configure both or neither", the gRPC port is down, and the admin HTTP port answers. (Task 2 Step 3 observes the log lines; the port probes stay manual.)
2. `clientTLS` set with both files absent: the files are created with mode 0600 (on Linux), and the VCS client connects by hostname with `tls_ca_file` pointing at the copied cert.
3. A real-CA certificate: the PKCS#1 requirement and the operator's OpenSSL recipe (3.x `-traditional`; 1.1.1 plain `openssl rsa`) work as the README says.
3a. A standalone server with `servers.voice.port` changed from 5002: a client with only `[voice] port` set still dials 5002 and gets no voice. With both `[voice] host` and `[voice] port` set, voice works.
4. Linux and macOS: follow the README's GUI build steps literally on a clean machine (GTK4/WebKitGTK 6.0 dev packages on Linux).
5. `docker compose -f docker-compose.dev.yml up` from scratch: the voice nodes need the documented restart, and a client outside the compose network receives the public `publicAddr`.
