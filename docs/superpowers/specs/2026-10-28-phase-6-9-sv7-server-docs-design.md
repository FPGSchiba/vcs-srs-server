**Status: approved 2026-10-28** (user decisions at the end).

# Phase 6.9 / SV7: Server docs (Client connections, build commands)

**Repos:** `vcs-srs-server` (`origin/main` `df8d9f3`, with SV1–SV5 and UP merged as #225–#232). Read-only cross-checks against `vcs-srs-client` (`origin/main` `b80b0e6`). Suggested branch: `docs/cleanup-sv7` in the server, cut from `origin/main`.
**Umbrella:** `docs/superpowers/specs/2026-10-27-vcs-client-phase-6-9-cleanup-umbrella-design.md:141` (track SV7, finding DOC-15). Gate: **cross-repo**. Listed files: `README.md` only.
**Evidence basis:** one research pass over server `df8d9f3`, with every fact cited to code. One throwaway detached worktree ran codegen, vet, the Windows headless and GUI builds, the linux/darwin headless cross-compiles, the build-tag probe and a throwaway key-format test (§4). Line numbers for the README, the Taskfiles, `docker-compose.dev.yml` and `deploy/README.md` were re-checked with `git show origin/main:<file>` while writing this doc. gopls was not used.

## Decisions (D1–D7 proposed; design approval pending)

| # | Decision | Source |
|---|---|---|
| D1 | SV7 changes **no Go code, proto, dependency or workflow**. It changes docs, plus the Taskfile `-tags` fix that the task brief puts in scope (§3 item 12; still a §6 gate, because the file is outside the umbrella's file list). | brief; umbrella:141 |
| D2 | The new section heading is exactly **`## Client connections`** (anchor `#client-connections`), so the client's DOC track (DOC-11, client README and CLAUDE.md) can link to it. | cross-repo gate |
| D3 | Every sentence in the new section is cited to code in the PR description (file:line), not to design docs. The README itself links the SV1 §11 and SV2 §9 tables for the full session contract and does not copy them. | research |
| D4 | **Parallel with SEC, merge after it.** SEC's server file list (umbrella:316 plus `design-SEC.md` header and §3): `go.mod`, `go.sum`, `frontend/package.json` + `package-lock.json`, `.github/workflows/test.yml` (+ `release.yml` if SEC Q4 = A), `srs/auth_service{,_test}.go`, `utils/general{,_test}.go`, `build/linux/appimage/build.sh` (deleted), `dev/config/control.yaml` (S2068, `:16,:20`), optionally `Dockerfile:53` (SEC Q4 = B). SV7 edits `README.md`, `build/{linux,darwin}/Taskfile.yml` (Q4), and under Q1 `docker-compose.dev.yml:52` and `deploy/README.md`. **No file is edited by both, as SEC is designed today** — SEC's S2068 choice is a comment in `dev/config/control.yaml` only (`design-SEC.md` D8, D-f), and SV7 only *cites* that file (row 2) and the `Dockerfile` (row 8). The claim depends on SEC's S2068 choice: if the user instead takes the umbrella's "env variables" route for S2068, `docker-compose.dev.yml` must pass them in (SV7 Q1 edits `:52` of the same file) and the README run instructions gain the variables, so SV7 documents them after rebasing. The same holds if SEC Q4 = B moves the image to `:nonroot` (the `/certs` first-run advice in row 15 then needs the UID). Rebase checkpoint: §5 step 6. If SEC adds a server-side bcrypt cost cap, SV7 adds one line to the Login subsection (row 5). | umbrella:316, :347; `design-SEC.md` D8, Q2, Q4 |
| D5 | Defects found while documenting that need a **code** change are documented as they are today and recorded as follow-ups (§10). SV7 does not fix them. That covers the PKCS#1-only key loader and the cert overwrite (Q2). | brief |
| D6 | The client's bcrypt hashing of the password is documented as intentional (CLAUDE.md, "Password handling looks odd but is correct"). Only a SERVER-side cost cap, if SEC adds one, is mentioned. | CLAUDE.md |
| D7 | The README states, in one sentence, that **the VCS client cannot log into a control-mode server today** (InitAuth refuses a client whose distribution modes exclude the server's, and the client advertises STANDALONE only), pointing to client ROADMAP Phase 9. This was Q3; it is a verified fact operators need, so it is a decision, not a question. | `srs/auth_service.go:304-310, :967-986`; review point 16 |

## 1. Intent and scope

Give server operators and client developers one accurate README section on how a client reaches this server: ports, client-facing TLS, the version floor, login, and the session lifecycle that SV1/SV2 defined. Fix the README's build, run and flag instructions, which still describe Wails v2 and an older Taskfile. Remove statements that SV4 and UP made false, and mark the older architecture section as historical so the statements SV1/SV3 made false there (row 17) stop reading as current.

**In scope:** `README.md` (new `## Client connections` section; rewritten Development / Building / Running / Flags; stale lines at `:19-20`, `:79`, `:84`, `:88`, `:91-96`, `:273`; one "historical design" line on the architecture section, row 17); the duplicate `-tags` in `build/linux/Taskfile.yml:121,123` and `build/darwin/Taskfile.yml:75,79` (per the brief, gated in §6). Subject to Q1: `docker-compose.dev.yml:52` and `deploy/README.md:97-99, :113, :131, :178-180`.

**Out of scope:** see §10. In short: any code change (key loader, cert overwrite, `production` tag semantics, STANDALONE-only client), any CI change, and the client-repo docs (DOC track).

## 2. What changed since the umbrella was written

The umbrella (2026-10-27) scoped SV7 as "a *Client connections* section (`clientTLS`, the client version floor) and corrected build commands", `README.md` only. Since then:

1. **SV1–SV5 and UP merged** (#225–#232). There is now a full session contract (C1–C17), a pending-login cap, a semver version floor, a sharded voice path and Wails v3.0.0. The section has more to document than the umbrella assumed.
2. **UP already fixed the install block.** `README.md:9-15` now installs `wails3` derived from go.mod and names the GTK4/WebKitGTK 6.0 dev packages. SV7 keeps those lines.
3. **The version-floor landmine is gone.** The exact-match check was replaced by a semver floor on 2026-09-26 (`063f6c5`; `srs/utils.go:13-48`). The client's `CLAUDE.md:96` and `docs/ROADMAP.md:155` still call it a landmine. That is DOC-03's job in the client repo (§6.3), not SV7's.
4. **README:273 became false with SV4.** The voice server no longer starts a goroutine per datagram. It dispatches to a fixed number of sender-hashed shards with 256-deep queues and drops when a queue is full (`voice/server.go` `Listen` → `startShards(shardCount(), shardQueueDepth)`; `voice/shard.go`).
5. **New defects surfaced by probing, all outside a doc track:**
   - the TLS key loader accepts PKCS#1 RSA keys only (§4), which makes `deploy/README.md:178-180`'s "use a real-CA certificate" advice fail for typical CA-issued keys;
   - a missing key file with an existing cert silently overwrites the cert (§4);
   - the linux/darwin headless production build drops the `production` tag (§4);
   - two deploy-doc defects: `docker-compose.dev.yml:52` and the `publicAddr` service names in `deploy/README.md:113, :131`.
6. **The user asked to run SEC and SV7 in parallel** (2026-10-28). The umbrella put SEC first, then SV7. D4 keeps the merge order and runs the work in parallel.

## 3. Change list

| # | Item | Repo | Files:lines | Change | Evidence |
|---|---|---|---|---|---|
| 1 | New `## Client connections` section, placed after `## Running` (before `## Improvements`, `README.md:98`) | server | `README.md` | Subsections: Ports, Client-facing TLS, Version floor, Login, Session lifecycle. Draft text in §3.1. | see the rows below |
| 2 | Ports table | server | same | client gRPC `servers.control` TCP (default `0.0.0.0:5002`; Docker/deploy configs `14446`), standalone + control modes, serves `SRSService`, `AuthService`, `grpc.health.v1`, reflection; UDP voice `servers.voice.port` (default 5002), standalone + voice modes; admin HTTP `servers.http` (default `0.0.0.0:80`), standalone + control, GraphQL `POST /api/v1/graphql` behind `api.key` (an empty `api.key` disables GraphQL with HTTP 503); VoiceControl `0.0.0.0:14448`, control mode only, always TLS, server-to-server only. TCP 5002 and UDP 5002 do not clash. Client developers: the client port accepts keepalive pings no more often than every 60 s; a client that pings faster gets GOAWAY. | `state/settings.go:13-17, :145-156`; `control/server.go:93-97, :138, :143-151, :166-173, :197-208`; `rest/middleware.go:19-21`; `app/app.go:171-180`; `app/server_control.go:41-53`; `dev/config/control.yaml:7`; `deploy/docker-compose.central.yml:10-12` |
| 3 | Voice address derivation | server | same | Standalone sends **empty** `coalition_voice_addr`/`global_voice_addr`, because no voice node ever registers with the in-process registry (the registry is wired in every mode, `control/server.go:181, :193`; `GetVoiceAddressForCoalition` returns the registered nodes' addresses, `voiceontrol/service.go:122-136`). Clients derive the address themselves. The VCS client uses `[voice] port` **only together with** `[voice] host`; with no host it uses the server URL's host plus port 5002 and ignores `[voice] port`. So a standalone server that changes `servers.voice.port` needs every client to set **both `[voice] host` and `[voice] port`**. In distributed mode it is each voice node's `voiceControl.publicAddr`, normalised before registration: `host:port` (the port wins, e.g. a NAT mapping) or a bare host (port from `servers.voice.port`); empty, `0.0.0.0` / `::`, a missing host or a bad port refuse registration; the control node joins host and UDP port. Wording reused from `example.config.yaml:40`. A voice binding with no packets is removed by a 30 s sweep with a 1 min threshold, so after **60–90 s**. | `app/server_control.go:168-182`; `control/server.go:181, :193`; `voiceontrol/service.go:122-136, :192`; `voiceontrol/client.go:440-468`; `example.config.yaml:40`; client `internal/voice/endpoint.go:21, :55-76`, `internal/app/voice.go:712-731`; `voice/server.go:573, :587` |
| 4 | Client-facing TLS (`clientTLS`) | server | same | Top-level `clientTLS {certificateFile, privateKeyFile, serverName}`, separate from `voiceControl` so an admin settings save cannot erase it. Omitted → plaintext + startup WARN; the VCS client refuses plaintext for any non-loopback host. One field only → client port down, error names the missing field, process and admin HTTP stay up. Key file absent → self-signed RSA-2048, 365 days, not a CA, SANs `localhost`, `127.0.0.1`, `::1`, `serverName` (IP SAN if an IP), files 0600, reused, never renewed. Cert present + key absent → **both regenerated, cert overwritten**. Key present + cert absent → error. Own cert: key must be **PKCS#1 RSA** (`BEGIN RSA PRIVATE KEY`). Conversion: OpenSSL 3.x `openssl rsa -in key.pem -traditional -out key-pkcs1.pem` (probed, §4); OpenSSL 1.1.1 has no `-traditional` and already writes PKCS#1, so `openssl rsa -in key.pem -out key-pkcs1.pem` (from the 1.1.1 man page, not run; LibreSSL not checked) (Q2). Clients verify the dialled hostname; with a self-signed cert, copy only the cert to clients and set `tls_ca_file`. Failed handshakes are logged at WARN, with a plaintext hint. | `state/settings.go:29, :109-130`; `example.config.yaml:41-44`; `control/server.go:157-160, :244-284, :253, :299`; `app/server_control.go:271-279`; `voiceontrol/utils.go:19-54, :90-120`; client `internal/session/dial.go:89-110`; probe §4 |
| 5 | Version floor and login | server | same | `srs.MinClientVersion = "v0.1.0"`, a semver floor (leading `v` optional; empty or invalid refused) with the body `Unsupported client version %q; this server requires %s or newer`. Raise it only in a deliberate PR and tell the client repo (client `internal/version` `Client = "0.1.0"`). InitAuth also refuses a client whose distribution modes do not include the server's: the VCS client advertises STANDALONE only, so it **cannot log into a control-mode server today** (D7; one sentence pointing to client ROADMAP Phase 9). Defaults `enableGuestAuth: true`, `enablePluginAuth: false`. GuestLogin: name 1–32 **bytes** (`len()`, `srs/utils.go:53`), unitId `^[A-Z0-9]{2,4}$`, password = client-side bcrypt hash of the coalition password, compared to each coalition's plaintext password, first match wins. AuthService failures are `Success:false` bodies, not gRPC errors. Pending logins: 5 min TTL re-armed per step, 10,000 total, 32 per IP, 60 s sweep, busy body over the cap. *Placeholder for SEC's bcrypt cost cap, if SEC adds one (D4).* | `srs/utils.go:13-60`; `srs/auth_service.go:49-58, :292-329, :450-456, :486-511, :967-986`; `state/settings.go:166-169`; `example.config.yaml:25-27`; `utils/general.go:7-9`; client `internal/version/version.go:9`; `063f6c5` |
| 6 | Session lifecycle (condensed C1–C17) | server | same | Short table: `Unauthenticated "session not found for <method>: log in again"` (auth interceptor, any RPC) and `Unauthenticated "session not found: log in again"` (the update stream, `srs/session.go:57`); both mean "log in again"; `PermissionDenied` (role too low); `Internal "token verification unavailable"` (not a session loss); stream end `Unauthenticated "session ended: <disconnected\|kicked\|banned\|stale\|stream lost>"` with a final SERVER_ACTION before kick/ban; `Aborted "superseded by a newer subscription"` (do not resubscribe); `ResourceExhausted "update stream fell behind: resubscribe and call SyncClient"`; grace 120 s after the later of stream end and last unary RPC, removal at 120–135 s (15 s sweep), any RPC including Ping resets it; 10 min stale rule for a live stream with no RPC; C17 empty response headers once the subscription is registered; SERVER_ACTION only to its target. Links SV1 §11 and SV2 §9. | `srs/session.go:56-61, :79-93, :168`; `srs/srs_service.go:74, :378, :422`; `control/server.go:525` |
| 7 | Build commands: codegen | server | `README.md:74-80` | Replace the single-proto `protoc` line with `mkdir -p srspb voicecontrolpb vcsauthpb` (protoc fails when the output directory is missing), the three protoc invocations, and `go run github.com/99designs/gqlgen generate`, copied from the generate action. Prerequisites: protoc 36.2, protoc-gen-go at the `google.golang.org/protobuf` version in `go.mod:19` (v1.36.11 today; v1.36.12 after SEC, so the README names the rule, not the number), protoc-gen-go-grpc v1.6.2, Node 22 for the GUI. Note that every `wails3 task` build runs `common:go:mod:tidy`, which also runs `go mod tidy`. | `.github/actions/generate/action.yml:44-77` (mkdir `:73`); `Dockerfile:31`; `go.mod:3,5,15,19,74` |
| 8 | Build commands: GUI and headless | server | `README.md:22-66, :82-84` | `wails build` → `wails3 build` (builds `bin/vcs-server[.exe]`). `wails3 dev` uses `./build/config.yml`. Headless: `wails3 task headless-standalone\|headless-control\|headless-voice`, or `go build -tags headless -o bin/vcs-server-headless .` after codegen. Tests: `go vet -tags headless ./...`, `go test -race -count=1 -tags headless ./...`. Release: Docker image only (`release.yml`, `Dockerfile`); no GUI artifact. One line noting that `wails3 build` rewrites `build/windows/icons.ico`, `build/darwin/icons.icns` and `frontend/package-lock.json` (§4). | `Taskfile.yml:11, :25-28`; `build/Taskfile.yml:82-125`; `.github/workflows/test.yml:27-45, :71-83`; `.github/workflows/release.yml:13-81`; `Dockerfile:1, :26-51`; probe §4 |
| 9 | Running | server | `README.md:30-36, :86-88` | `wails3 task run` runs an **existing** binary and does not build. Output is `bin/`, not `build/bin`. Windows passes `--autostart`; linux/darwin do not. A missing config file is written with defaults (no coalitions, HTTP port 80, placeholder token-key paths). | `Taskfile.yml:11, :25-28`; `build/windows/Taskfile.yml:58-60`; `build/linux/Taskfile.yml:112-114`; `build/darwin/Taskfile.yml:68-70`; `state/settings.go:136-195` |
| 10 | Flags | server | `README.md:90-96` | Remove "via the `--` separator" (no Taskfile uses `CLI_ARGS`). Flags go to the binary: `--config`, `--banned`, `--log-folder`, `--file-log`, `--autostart` (GUI only; headless always starts), `--mode` and `--global` (headless only, now documented). Remove the stray `~~` at `:96`. | `shared.go:23-31`; `headless.go:14`; `app/app.go:157-167`; `build/Taskfile.yml` (no `CLI_ARGS`) |
| 11 | Stale lines | server | `README.md:17-20`, `:273` | Drop the dangling "Live development section" reference (`:20`; no such section). Rewrite `:273` "Flooding": per-sender sharded dispatch, 256-deep queues, drop on full; flood mitigation by rate limit is still not implemented. | `README.md:17-20, :273`; `voice/server.go` `Listen`; `voice/shard.go`; SV4 design §3 |
| 12 | Duplicate `-tags` in headless production builds | server | `build/linux/Taskfile.yml:121, :123`; `build/darwin/Taskfile.yml:75, :79` | Mirror `build/windows/Taskfile.yml:69`: drop the trailing `-tags headless` from the `cmds` line and put `-tags headless` in the non-production branch of `BUILD_FLAGS`. Effect: production linux/darwin headless builds get `[production headless]` instead of `[headless]`. No runtime change today (D1; §4). Edit **only** those four lines: the GUI `build` task's `BUILD_FLAGS` at `:16` of both files has the same `{{else}}` text and must not gain `-tags headless` (a blanket substitution in the probe hit it, §4). **Gate §6.2.** | probe §4; `build/windows/Taskfile.yml:67, :69` |
| 13 | (Q1) dev compose restart comment | server | comment `docker-compose.dev.yml:52` (`restart:` is `:53`) | Rewrite: `on-failure` covers crashes only. A voice node does not exit when control's cert is missing or control is unreachable: a missing cert makes `connectControl` fail, `Listen` returns an error that is logged and recorded in admin state, and the process lives on in `select {}`; an unreachable control node is retried forever. A voice node started before control wrote its cert therefore needs `docker compose restart voice-eu voice-global`. Comment only; `restart:` value unchanged. | `voiceontrol/client.go:183-205, :257-290`; `voice/server.go:157-178`; `app/server_control.go:172-189`; `headless.go:42-44`; `app/app.go:107-110` |
| 14 | (Q1) deploy doc `publicAddr` | server | `deploy/README.md:113, :131` | `voice-eu` / `voice-global` are Docker service names that clients outside the compose network cannot resolve. The voice node normalises `publicAddr` (row 3; same wording as `example.config.yaml:40`) but does not resolve or validate the name, so the control node hands it to clients as `<name>:<port>`. Change to a public `host[:port]` such as `vcs.example.com:5002`, with one sentence explaining why. | `voiceontrol/client.go:440-468`; `voiceontrol/service.go:192`; `srs/srs_service.go:165-176`; `dev/config/voice-eu.yaml:38`; `example.config.yaml:40` |
| 15 | (Q1) deploy doc cert mount | server | `deploy/README.md:97-99` vs `deploy/docker-compose.central.yml:15` | The doc already says to mount `./certs` read-write on the first run and then switch to `:ro`. The gap is that the **shipped** compose file is already `:ro` at `:15`, so following it literally means the first run cannot generate certs. Doc-only fix: tell the operator to edit the shipped compose for the first start (drop `:ro`, start once, restore it), or pre-create the files. Changing the compose file itself is Q1 option B. Note that `deploy/config/` does not ship and must be created. | `deploy/README.md:97-99`; `deploy/docker-compose.central.yml:15`; `git ls-tree` (no `deploy/config`) |
| 16 | (Q2) deploy doc real-CA advice | server | `deploy/README.md:178-180` | Add the PKCS#1 constraint and the conversion recipe, phrased per OpenSSL version as in row 4 (OpenSSL 3: `-traditional`; 1.1.1: plain `openssl rsa -in … -out …`); say ECDSA keys cannot be used today (`openssl rsa` on an EC key prints `Not an RSA key`, §4). | probe §4; `voiceontrol/utils.go:38-54` |
| 17 | Architecture section marked historical | server | `README.md` heading of the protocol/architecture section (inside `:108-459`) | One line under the heading: "Design notes from before the implementation; the *Client connections* section above is authoritative where they differ." Lines made false by SV1/SV3 (kick shown as a voice-server "Disconnect Notice" / admin "gRPC Kick" at `:325-330, :404-410`, actually a final SERVER_ACTION on the control stream; admin surface "gRPC" at `:290`, actually HTTP GraphQL; `:242` "short random token, not a JWT" vs `:238` "Client GUID UUIDv4") are listed as follow-ups in §10, not rewritten. | `app/clients.go:233-235`; `srs/srs_service.go:693`; `graphql/schema.resolvers.go:97-100`; `README.md:238, :242, :290, :325-330, :404-410` |

### 3.1 Draft text (for the plan; it can trim)

This design approves the **content** of `## Client connections`: exactly the facts in rows 2–6 (and the bcrypt line from SEC, if any). It does not contain the README prose, so the user is not asked to approve wording here. The prose is written in the plan and **approved at the plan stage**, sentence by sentence against rows 2–6; any fact in the prose that is not in rows 2–6 needs its own citation and is flagged in the plan for review. Every bullet keeps its code citation in the PR description (D3).

## 4. Probe results (verbatim)

Throwaway detached worktree `scratchpad/probe-sv7docs` at `df8d9f3`. `git worktree remove` failed with `Filename too long` under `node_modules`, so it was removed with `rm -rf` and then `git worktree prune`. `git worktree list` no longer shows it. The server main checkout was not touched.

| Command | Result | Output |
|---|---|---|
| 3× protoc (local libprotoc 36.0; CI pins 36.2) + `go run github.com/99designs/gqlgen generate` | pass | OK |
| `go vet -tags headless ./...` | pass | OK |
| Windows headless, dev flags `-tags headless -buildvcs=false -gcflags=all=-l` | pass | OK |
| Windows headless, prod flags `-tags production,headless -trimpath -ldflags=-w -s` | pass | OK |
| Linux/darwin headless, `CGO_ENABLED=0` cross-compile with the Taskfile's duplicated tags | pass | both OK |
| `go list -f {{context.BuildTags}} -tags production,headless -tags headless .` | `production` dropped | `[headless]`; without the duplicate: `[production headless]` |
| `go list -deps -tags headless . \| grep -c wailsapp` | Wails not in headless graph | `0` |
| `git grep '^//go:build'` | no `production` constraint in the repo | only `headless` / `!headless` |
| wails3 CLI v3.0.0 built into the scratchpad; `wails3 dev -help` | default config confirmed | default `-config ./build/config.yml` |
| Windows GUI `wails3 build` | pass, 2m33s | produced `bin/vcs-server.exe` (npm install, vite build:dev, syso, go build). Side effects: rewrote `icons.ico`/`icons.icns` and `package-lock.json`; `go.mod` and `schema.resolvers.go` changed only in line endings |
| Key formats (throwaway control test, deleted) | only PKCS#1 RSA accepted | `ecdsa-pkcs8` / `ecdsa-sec1` / `rsa-pkcs8` → `x509: failed to parse private key (use ParsePKCS8PrivateKey/ParseECPrivateKey instead...)`; `rsa-pkcs1` → nil |
| Key-only (cert file missing) | error | `open ...missing.pem` error |
| Cert-only (key file missing) | **cert overwritten** | `err=nil certOverwritten=true` |
| Generated cert | as documented | `DNS=[localhost vcs.example.net] IPs=[127.0.0.1 ::1] validity=8760h isCA=false` |

**Not run:**
- A live headless server, to see the plaintext WARN and the half-config error in its log. The auto-mode classifier denied killing the process by image name (`taskkill //IM`). Both behaviours rest on code reading (`control/server.go:244-250`) and on the existing `TestClientTransportCredentials` (`control/server_test.go:29`).
- Linux and macOS GUI builds (not possible on Windows). The README's GTK4/WebKitGTK 6.0 line (`README.md:15`, from UP) and `wails3 task linux:build` / `darwin:build` stay unverified here.
- The linux/darwin headless builds were cross-compiled with `CGO_ENABLED=0`, not with the Taskfile's `CGO_ENABLED=1`.

**Probe cleanup caveat (deviations from the cleanup rule, reported to the user):**
1. The first probe worktree (`probe-sv7docs`) was removed with `rm -rf` plus `git worktree prune`, not with `git worktree remove --force` as the rule requires, because `git worktree remove` failed with `Filename too long` under `node_modules`. The end state is the same (no directory, no worktree entry), but the rule was not followed.
2. The v3.0.0 `wails3` CLI was installed into the scratchpad's `wails3bin` directory and that directory was removed afterwards. If a `wails3bin` directory existed there before this session, it was removed too. (A scratchpad listing on 2026-10-28 shows no `wails3bin`, but also directories `wails`, `wails-cl`, `wails-up-server` from other tracks, which were not touched.)

### 4.1 Review probe (worktree `probe-sv7dry`, detached `df8d9f3`, removed with `git worktree remove --force` + `git worktree prune`)

| Command | Result | Output |
|---|---|---|
| `wails3 task -dry linux:build:headless PRODUCTION=true` (global `wails3` v3.0.0-beta.26; the repo pins v3.0.0, so re-check `-dry` with v3.0.0 in the plan) | `-dry` works: prints the rendered commands, runs nothing | `go build -tags production,headless -trimpath -buildvcs=false -ldflags="-w -s" -o bin/vcs-server-headless -tags headless` |
| rendered `-tags` of `{linux,darwin}:build:headless` piped into `go list -e -f '{{context.BuildTags}}' <tags> .` (RED, today) | production drops `production` | linux/darwin `PRODUCTION=true` → `[headless]`; `PRODUCTION=false` → `[headless]` |
| same after the item 12 edit (GREEN) | fixed | linux/darwin `PRODUCTION=true` → `[production headless]`; `false` → `[headless]`. Caveat: the probe's blanket `sed` also touched the GUI `BUILD_FLAGS` at `:16`; the headless results do not depend on it, and item 12 now says to edit only `:121/:123`, `:75/:79` |
| OpenSSL 3.5.7: `openssl genrsa -out key.pem 2048` → `-----BEGIN PRIVATE KEY-----` (PKCS#8); `openssl rsa -in key.pem -traditional -out key-pkcs1.pem` | pass | `writing RSA key`, output `-----BEGIN RSA PRIVATE KEY-----` |
| both keys + a self-signed cert fed to the real `voiceontrol.LoadOrGenerateKeyPair` (throwaway test, deleted with the worktree) | only the converted key loads | `key.pem -> err=x509: failed to parse private key (use ParsePKCS8PrivateKey instead for this key format)`; `key-pkcs1.pem -> key=true err=<nil>` |
| `openssl rsa -in ec.pem -traditional` on a P-256 key | refused | `Not an RSA key` |

Not run: OpenSSL 1.1.1 and LibreSSL. Per the 1.1.1 man page (https://docs.openssl.org/1.1.1/man1/rsa/) `openssl rsa` has no `-traditional` option and its PEM output is `BEGIN RSA PRIVATE KEY`; per the 3.0 page (https://docs.openssl.org/3.0/man1/openssl-rsa/) `-traditional` means "use the traditional PKCS#1 format instead of the PKCS#8 format". LibreSSL is unverified, so the README says "OpenSSL 3: …; OpenSSL 1.1.1: …" and nothing about LibreSSL beyond "check that the output starts with `BEGIN RSA PRIVATE KEY`".

## 5. Plan shape

The TDD obligation for a doc track is **executable docs**: every command the README gains is run in a fresh worktree before it is written down, and every claim is cited to a file:line in the PR description. The one non-doc change (item 12) gets a RED/GREEN check of its own.

1. **Branch** `docs/cleanup-sv7` from server `origin/main` in a worktree. Work in parallel with SEC (D4).
2. **RED, item 12 — the check reads the Taskfile.** A small script (kept in the PR description, not committed) runs, for `t` in linux, darwin and `PRODUCTION` in true, false: `wails3 task -dry $t:build:headless PRODUCTION=$p`, extracts the `go build` line, takes every `-tags X` from it, and runs `go list -e -f '{{context.BuildTags}}' <those flags> .`. First confirm `-dry` with the repo's wails3 v3.0.0 (probed only with v3.0.0-beta.26, §4.1). Expected RED: `PRODUCTION=true` → `[headless]` on both. (A build through the task on Windows is not possible: the linux/darwin tasks set `CGO_ENABLED: 1` with a foreign GOOS, `build/linux/Taskfile.yml:126-129`, `build/darwin/Taskfile.yml:82-85`, which needs a cross C compiler. Alternative if a Linux host or WSL is at hand: `wails3 task linux:build:headless PRODUCTION=true` and `go version -m bin/vcs-server-headless | grep -- -tags`.) **GREEN:** the same script gives `[production headless]` for `PRODUCTION=true` and `[headless]` for false, on both; `git diff` touches only `build/linux/Taskfile.yml:121,123` and `build/darwin/Taskfile.yml:75,79` (not `:16`). Also cross-compile linux and darwin headless (dev + production flags) with `CGO_ENABLED=0`; `go vet -tags headless ./...` and `go test -race -count=1 -tags headless ./...` stay green.
3. **Command check before writing (items 7–10, 4, 16).** Run the OpenSSL 3 conversion recipe and feed its output to `voiceontrol.LoadOrGenerateKeyPair` in a throwaway test (as in §4.1), so the README recipe is executed, not just quoted. In the worktree, run the codegen block (including `mkdir -p`), `go build -tags headless -o bin/vcs-server-headless .`, the vet/test lines, `wails3 task headless-standalone` (stop it by PID, not by image name), `wails3 build`, `wails3 task run`, and `--help` on the binary to confirm the flag list. Revert the `wails3 build` side effects before committing (§4).
4. **README rewrite**, in this order: stale lines (item 11), Development / Building / Running / Flags (items 7–10), then the new `## Client connections` section (items 1–6), then the historical-design line (item 17). One commit each.
5. **Q1 / Q2 doc items** (13–16), if approved. One commit.
6. **Rebase on SEC** once SEC merges. Checkpoints, each answered from SEC's merged code (cite file:line):
   - **bcrypt cap:** if SEC added one, fill the Login placeholder (value, `Success:false` message); otherwise delete the placeholder.
   - **S2068 resolution:** if SEC kept the comment-only fix (its current design), nothing changes. If it moved the dev passwords to environment variables, document the variables in the README run instructions and in the `docker-compose.dev.yml` comment, and re-check that SV7's `:52` edit does not conflict with SEC's compose change.
   - **SEC Q4 = B (`:nonroot`):** add the UID to row 15's first-run `/certs` advice.
   - **protoc-gen-go version:** row 7 states the rule (follow `go.mod`'s protobuf), so SEC's v1.36.12 needs no edit; confirm.
   - Re-run `go test -race -count=1 -tags headless ./...` (R3).
7. **Link check:** every relative link and anchor in the changed files resolves; the heading is exactly `## Client connections` (D2).
8. **Verification:** `go vet -tags headless ./...`, `go test -race -count=1 -tags headless ./...` (unchanged code, item 12 only), then `superpowers:requesting-code-review`. The orchestrator reports the PR as ready; the user merges.
9. **Hand-off to DOC:** report the final anchor URL to the client DOC track (§6.3).

## 6. Gates needing approval

### 6.1 Dependency changes

**None.** No go.mod, go.sum, package.json or lockfile change. The `wails3 build` side effect on `frontend/package-lock.json` (§4) is reverted, not committed.

### 6.2 CI and build changes

| Repo | File:line | Change | Approval |
|---|---|---|---|
| server | `build/linux/Taskfile.yml:121, :123` | drop the trailing `-tags headless`, move it into the dev branch of `BUILD_FLAGS` (as `build/windows/Taskfile.yml:69`) | Needed. The brief puts this in scope; the umbrella's SV7 file list does not, and the Taskfiles are SV5's ci-cd territory. Q4 records it. |
| server | `build/darwin/Taskfile.yml:75, :79` | same | same |

This is the only item that makes SV7 more than a docs track: the umbrella gives SV7 a **cross-repo** gate only (umbrella:141), so the Taskfile edit adds a build gate the umbrella did not foresee. That is why Q4 stays a question. No workflow, Dockerfile, generate action or protoc pin changes. CI does not invoke these Taskfile targets (`test.yml` and `release.yml` call `go build` directly), so CI behaviour is unchanged.

### 6.3 Cross-repo

- **SEC → SV7.** SV7 merges after SEC and rebases on it (D4). Content dependencies: the possible bcrypt cost-cap line, SEC's S2068 choice (only if it moves to environment variables), and SEC Q4 = B (non-root image). See §5 step 6.
- **SV7 → DOC (client).** DOC-11 links to `#client-connections` in the server README. DOC-03 must also mark the version-landmine text in client `CLAUDE.md:96` and `docs/ROADMAP.md:155` resolved (server `063f6c5`, 2026-09-26). The client's `CLAUDE.md:119` "Control-plane TLS" paragraph is accurate but lacks the PKCS#1-only and overwrite caveats. That is DOC's call; SV7 only reports it.

### 6.4 Server behaviour changes

**None at runtime.** Item 12 changes the build tags of the linux/darwin production headless Taskfile builds, which no repo file reads today (§4). If a future file uses `//go:build production`, those builds start honouring it, which is the intended behaviour and matches Windows and the Dockerfile.

## 7. Risks

- **R1 Docs describe defects that may be fixed soon.** Q2-B would make the PKCS#1-only and overwrite text wrong. Mitigation: word those lines as "as of `<SHA>`" and add a follow-up task to update the README when the loader changes.
- **R2 Unverified platforms.** The Linux/macOS GUI build lines and the GTK4 dev-package line were not run here (§4). The README must not claim they were verified. Mitigation: phrase them as instructions only; manual check §8.
- **R3 Drift after SEC.** If SEC changes anything the section documents (gRPC behaviour under the grpc bump, the bcrypt cap), the rebase must re-check rows 4–6. grpc ≥ v1.83.1 changes no status code the section names, but this has not been tested; re-run `go test -race -tags headless ./...` after the rebase.
- **R4 The section duplicates design-doc tables.** Mitigation (D3): condensed table plus links; the design docs stay authoritative.
- **R5 `-dry` verified only with wails3 v3.0.0-beta.26** (§4.1), not the repo's v3.0.0. If v3.0.0's `task -dry` behaves differently, fall back to a real build on a Linux host or WSL and `go version -m … | grep -- -tags` (§5 step 2). Typing the flags by hand into `go list` is **not** a fallback: it cannot fail.
- **R6 Live-log claims rest on code reading** (§4 "Not run"). Mitigation: manual §8 item 1.

## 8. Manual verification

Add to the server-relevant section of the Phase 6.9 manual-verification checklist (umbrella §8):

1. Start a headless standalone server with no `clientTLS`: the log shows the PLAINTEXT WARN. Set only `certificateFile`: the log shows the "configure both or neither" error, the gRPC port is down and the admin HTTP port answers.
2. With `clientTLS` set and both files absent: the files are created (0600), and the VCS client connects by hostname with `tls_ca_file` pointing at the copied cert.
3. Supply a real-CA certificate: confirm the PKCS#1 requirement and the conversion recipe for the operator's OpenSSL version (3.x `-traditional`; 1.1.1 plain `openssl rsa`) work as the README says.
3a. Standalone server with `servers.voice.port` changed from 5002: a client with only `[voice] port` set still dials 5002 and gets no voice; with both `[voice] host` and `[voice] port` set, voice works.
4. Linux and macOS: follow the README's GUI build steps literally on a clean machine (GTK4/WebKitGTK 6.0 dev packages on Linux).
5. If Q1 is approved: `docker compose -f docker-compose.dev.yml up` from scratch, and confirm the voice nodes need the documented restart; a client outside the compose network receives the public `publicAddr`.

## 9. Questions for the user

**Q1 – Widen SV7 to the two wrong deploy docs?** `docker-compose.dev.yml:52` claims `restart: on-failure` recovers a voice node that started before control's cert existed; it never fires (the process does not exit). `deploy/README.md:113, :131` tell operators to set `publicAddr` to Docker service names that clients cannot resolve. `deploy/README.md:97-99` correctly says to mount `./certs` read-write on first run, but the shipped `deploy/docker-compose.central.yml:15` is already `:ro`.
- **A (recommended):** yes, doc-only: rewrite the `:52` comment, change `publicAddr` to a public `host[:port]` example, and tell operators to edit the shipped compose for the first start. Two more files, no behaviour change.
- B: as A, and also change `deploy/docker-compose.central.yml:15` to drop `:ro`. That changes what the shipped compose file does, so it is a deploy behaviour change.
- C: leave both files as they are and record follow-ups.

**Q2 – PKCS#1-only TLS key loader and cert overwrite (new finding).** `voiceontrol/utils.go:38-54` refuses PKCS#8 RSA and all ECDSA keys, which most CAs issue; `:20-36` overwrites an existing cert when the key file is missing. The same loader serves the client port and VoiceControl.
- **A (recommended):** SV7 documents both as they are (README + `deploy/README.md:178-180`), with the conversion for RSA (OpenSSL 3: `openssl rsa -in key.pem -traditional -out key-pkcs1.pem`, probed against the real loader in §4.1; OpenSSL 1.1.1: `openssl rsa -in key.pem -out key-pkcs1.pem`, from the man page) and a note that ECDSA cannot be used. Record a code follow-up (accept any key `tls.X509KeyPair` accepts; never overwrite an existing cert) as a new server track or post-6.9 item.
- B: add the code fix to Phase 6.9 now as a new server track. It is a behaviour change and needs its own design, tests and manual check.

**Q3 – (now decision D7.)** The control-mode login refusal is a verified fact and costs one sentence; it is no longer put to the user. The numbering of Q1, Q2 and Q4 is kept so earlier references stay valid.

**Q4 – Approve the Taskfile `-tags` fix in SV7?** The brief puts it in scope, but `build/linux/Taskfile.yml` and `build/darwin/Taskfile.yml` are build files outside the umbrella's SV7 list (SV5 territory), and the umbrella's SV7 gates are cross-repo only, so this adds a build gate (§6.2). It is latent: no file uses the `production` tag today.
- **A (recommended):** yes, in SV7 as a separate commit, mirroring `build/windows/Taskfile.yml:69` (§3 item 12, §6.2).
- B: leave it as a follow-up and keep SV7 doc-only.

## 10. Out of scope

- **Code defects found while documenting** (follow-ups, see Q2): the PKCS#1-only key loader (`voiceontrol/utils.go:38-54`) and the cert overwrite (`:20-36`), both shared by the client port and VoiceControl.
- **Changing the shipped deploy compose files** (`deploy/docker-compose.central.yml:15` `:ro`; the `restart:` policy in `docker-compose.dev.yml`), unless Q1 = B.
- **The VCS client advertising DISTRIBUTED** (CON-11, client ROADMAP Phase 9).
- **Raising `MinClientVersion`**, or any change to the version check.
- **Any CI change**, including a GUI build leg (UP follow-up) and a README link checker in CI.
- **The `wails3 build` side effects** on icons and `package-lock.json` (a Taskfile behaviour; documented in one line, not fixed).
- **Client-repo docs:** `CLAUDE.md:96, :119`, `docs/ROADMAP.md:155`, the client README link. These are DOC-03 / DOC-11.
- **Rewriting the README's protocol and architecture sections** (`README.md:108-459`), apart from the `:273` correction and the one "historical design" line (row 17). Recorded follow-ups for a later rewrite: the kick flow in both sequence diagrams (`:325-330`, `:404-410`: kick is a final SERVER_ACTION on the control stream, `app/clients.go:233-235`, not a voice-server "Disconnect Notice" or an admin "gRPC Kick"); the admin surface (`:290` says gRPC; it is HTTP GraphQL, `graphql/schema.resolvers.go:97-100`); the Session ID description (`:242` "short random token, not a JWT" contradicts `:238` "Client GUID UUIDv4").
- **Server-side bcrypt cost cap**: SEC's decision. SV7 only documents it if SEC adds it.
- **Graceful listener shutdown on SIGINT/SIGTERM** (B21-01; UP out of scope).

## Review notes (2026-10-28)

A review against server `df8d9f3` and client `b80b0e6` raised 16 points. Each was judged and every changed claim re-read with `git show <sha>:<file>`. One new probe ran (§4.1, worktree `probe-sv7dry`, removed with `git worktree remove --force` + `git worktree prune`; `git worktree list` shows no probe entry afterwards).

| # | Point | Verdict | Change |
|---|---|---|---|
| 1 | `[voice] port` alone is ignored | **Accepted.** `endpoint.go:55-76`: `ConfigPort` is only read inside `if s.ConfigHost != ""`; else ServerURL host + `DefaultVoicePort`. `internal/app/voice.go:712-731` passes both through unchanged. | Row 3 now says "set both `[voice] host` and `[voice] port`"; manual check §8 3a. |
| 2 | D4 file list incomplete; S2068 overlap | **Accepted, with a narrowing.** SEC's list also has `dev/config/control.yaml`, `build/linux/appimage/build.sh`, `frontend/package*.json`, `srs/auth_service*`, optionally `release.yml`/`Dockerfile`. SEC's current design resolves S2068 with a **comment only** (`design-SEC.md` D8, D-f: the server has no env expansion), so today there is still no shared file; the risk is real only if the user overrides SEC toward env variables. | D4 rewritten with SEC's real list and the dependency on SEC's S2068 choice; §5 step 6 has explicit rebase checkpoints (bcrypt, S2068, SEC Q4 = B, protoc-gen-go); §6.3 updated. |
| 3 | Item 12 test cannot fail | **Accepted.** Hand-typed `go list` flags never read the Taskfile. Verified `wails3 task -dry` renders the command without running it (§4.1), and that the rendered flags give `[headless]` today and `[production headless]` after the edit. Confirmed `CGO_ENABLED: 1` + foreign GOOS at `build/linux/Taskfile.yml:126-129` and `build/darwin/Taskfile.yml:82-85`. | §5 step 2 rewritten around `-dry`; R5 rewritten. New finding from the probe: the GUI `BUILD_FLAGS` at `:16` matches the same text, so row 12 now restricts the edit to `:121/:123` and `:75/:79`. |
| 4 | `publicAddr` is not verbatim; standalone citation misleading | **Accepted.** `resolveAdvertisedAddr` (`voiceontrol/client.go:440-468`) normalises; `RegisterVoiceServer` joins host and UDP port (`service.go:192`); `controlServer` is built and passed to `NewSimpleRadioServer` in every mode (`control/server.go:181, :193`), so the empty standalone address comes from "no node registered", not the nil branch at `srs_service.go:78-83`. | Rows 3 and 14 reworded after `example.config.yaml:40`; the `srs_service.go` `getVoiceAddresses` citation dropped from row 3. |
| 5 | `-traditional` recipe never run | **Accepted and run.** OpenSSL 3.5.7: PKCS#8 key fails the real loader, the `-traditional` output loads (§4.1). OpenSSL 1.1.1 has no `-traditional` (man page URL in §4.1); LibreSSL not checked. | Rows 4, 16, Q2-A, §8 #3 phrase it per version; §5 step 3 runs the recipe against the loader. |
| 6 | README lines made false by SV1/SV3 | **Accepted.** Read `README.md:238, :242, :290, :325-330, :404-410` at `df8d9f3`; kick goes through `RemoveClient(... ServerActionUpdate(ActionKick ...))` (`app/clients.go:233-235`); admin kick is a GraphQL resolver (`graphql/schema.resolvers.go:97-100`). | New row 17 (one "historical design" line); the three defects listed as follow-ups in §10. |
| 7 | Binding timeout 60–90 s | **Accepted** (`voice/server.go:573` 30 s ticker, `:587` 1 min threshold). | Row 3. |
| 8 | Name limit in bytes | **Accepted** (`srs/utils.go:53`, `len()`). | Row 5. |
| 9 | Row 13 line citations | **Accepted.** `voice/server.go:157-178` is `Listen` with the `connectControl` failure; `headless.go:44` is `select {}`; comment `docker-compose.dev.yml:52`, `restart:` `:53`. | Row 13. |
| 10 | Codegen needs `mkdir -p`; protobuf at `go.mod:19` | **Accepted** (`.github/actions/generate/action.yml:73`, `Dockerfile:31`, `go.mod:19` = protobuf v1.36.11). The §4.1 probe failed nothing here because it ran `mkdir -p` first. | Row 7 adds `mkdir -p` and states the version as a rule, since SEC moves protobuf to v1.36.12. |
| 11 | Item 15 overstated | **Accepted** (`deploy/README.md:97-99` already says read-write then `:ro`). | Row 15 and Q1 reworded: the gap is only that the shipped compose `:15` is already `:ro`. |
| 12 | Optional Ports facts | **Accepted** (`rest/middleware.go:19-21` 503; `control/server.go:166-169` `MinTime: 60s`). The GOAWAY consequence is grpc's documented keepalive enforcement behaviour, not re-tested here. | Row 2. |
| 13 | Second "session not found" string | **Accepted** (`srs/session.go:57`). | Row 6. |
| 14 | §3.1 promises prose it does not contain | **Accepted.** | §3.1 now approves content only; the prose is approved at the plan stage. |
| 15 | Probe cleanup deviated | **Accepted.** | §4 cleanup caveat lists both deviations; surfaced to the user in the summary. |
| 16 | Q3 should be a decision; Q4 stays | **Accepted.** `srs/auth_service.go:304-310, :967-986` re-read. | New D7; Q3 retired (numbering kept); §6.2 and Q4 explain the extra build gate. |

Verified-correct items from the review were not re-opened.

## User decisions (2026-10-28)

All recorded design questions were answered by the user on 2026-10-28. Design approval is still pending.

- SEC Q1 = A: server grpc v1.85.0 and quic-go v0.64.1, plus the moves they force (protobuf v1.36.12, qpack v0.6.0, genproto rpc). This re-approves grpc above the old v1.83.1 floor.
- SEC Q2 = A: cap the accepted bcrypt cost at 12.
  - Over-cost or malformed hashes are rejected with a Success:false body ("Invalid password hash …") and a WARN log.
  - The compare runs outside the settings lock.
  - A client guard keeps its own cost at or below 12.
  - SV7's README documents the cap.
- SEC Q3 = A: remove the unused dev dependency typed-scss-modules, which closes esbuild alert #2.
- SEC Q4 = A: SHA-pin the 7 actions in the server release.yml (S7637). Mark S6471 (container runs as root) safe for now. The image does not move to nonroot.
- SEC Q5 = A: fix the Windows reserved-port flake in SEC. This is a test-only change.
- SEC Q6 = A: x/net v0.62.0 in both repos, plus the moves it forces on the server (x/crypto v0.58.0, x/sys v0.49.0, x/text v0.43.0, x/term v0.47.0).
- SV7 Q1 = A: fix the two wrong deploy docs, docs only. Drop no :ro mount.
- SV7 Q2 = A: document today's TLS key-loader behaviour (PKCS#1 RSA only, the OpenSSL conversion command, no ECDSA). The code fix is recorded as a follow-up.
- SV7 Q4 = A: fix the Taskfile build tags in their own commit. Change only the release-build lines: Linux :121/:123 and Darwin :75/:79.
