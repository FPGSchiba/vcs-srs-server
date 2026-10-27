# Phase 6.9 / SV3: Server Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Where this runs:** repo `vcs-srs-server` (`D:\Projects\vcs-srs-server`), in a **fresh git worktree** on branch `chore/cleanup-sv3`, branched from `origin/main` (`c3096ce` at planning time). Create it with superpowers:using-git-worktrees. All paths below are relative to that worktree root. The worktree needs the generated `*.pb.go` and `graphql/generated` (gitignored): run the repo's generation step (`buf generate` / `go generate` as in `test.yml`) or copy them from the main checkout before Task 1.

**Goal:** Make the server crash-proof and deadlock-proof on the scanned paths, enforce coalition isolation on voice, give the non-auth auth interceptor typed gRPC codes, make every AuthService RPC body-only, bound pending logins, and get `go vet` green.

**Architecture:** Small, independent fixes in `state`, `srs`, `control`, `voice`, `utils`. The two control-plane interceptors are refactored onto one `authenticate`/`authError` pair (SV1's extension point). Pending logins get a TTL'd, capped map with a background sweeper owned by `AuthServer`. A hand-rolled recovery interceptor sits outermost on both gRPC servers.

**Tech Stack:** Go (headless build tag), grpc-go (`codes`, `status`, `metadata`, `peer`), `log/slog`, `golang-jwt/jwt/v5`. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-10-27-phase-6-9-sv3-server-hardening-design.md` (this repo, committed in Task 10; draft at the orchestrator's scratchpad `design-SV3.md`). Umbrella: client repo `docs/superpowers/specs/2026-10-27-vcs-client-phase-6-9-cleanup-umbrella-design.md` §3 SV3, §9.

## Global Constraints

- Verification suite (every task ends green on it, from the worktree root): `go build -tags headless ./...`, `go vet -tags headless ./...`, `go test -race -count=1 -tags headless ./...`. Before Task 1 `go vet` fails only on `srs\srs_service.go:590:3: unreachable code`; from Task 1 on it must be fully green.
- **No dependency changes** (no `go.mod`/`go.sum` edits). Recovery is hand-rolled. No goleak.
- **No proto change, no CI/workflow change, no repo settings, no secrets.**
- AuthService business failures return `Success:false` + `ErrorMessage` with a **nil** error. Never return a non-nil error from an AuthService RPC except a recovered panic (interceptor-level).
- Interceptor codes: `Unauthenticated` (missing metadata/token, invalid/malformed/expired token), `PermissionDenied` (insufficient role), `Internal` (token key load failure, recovered panic).
- Pending-login values: `pendingLoginTTL = 5 * time.Minute`, `maxPendingLogins = 10000`, `maxPendingLoginsPerIP = 32`, `pendingSweepInterval = 60 * time.Second`. Over cap → refuse the **new** InitAuth with a body; never evict.
- Existing AuthService message strings stay byte-for-byte. New strings (exact): `Missing client capabilities`, `Server is busy: too many pending logins, please try again shortly`, `Too many pending logins from your address, please try again shortly`, `internal server error`, `token verification unavailable`.
- gopls is unreliable in the client repo; treat any editor diagnostic as unverified until `go build`/`go vet` confirms it.
- Commit after each task; messages end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Review Focus

1. **Expired token on a unary SRS RPC (the CL1 session-expired signal)** — a client 8 h into a session calls `SyncClient`; it must get `codes.Unauthenticated`, not `Unknown`. Pinned in Task 4 (`expired token` row, unary and stream).
2. **A plugin login whose pending entry was swept mid-flow** — `StartAuth` `AUTH_CONTINUE` or `handleAuthComplete` after the sweeper removed the GUID must return the "please initialize first" body, never panic. Pinned in Task 7 (`TestSetPluginSessionMissingGUID`, `TestCompletePendingMissingGUID`).
3. **Per-IP count drifting after plugin completion** — `handleAuthComplete` used to replace the entry; if `IP` is lost, `pendingPerIP` never decrements and the address is eventually locked out. Pinned in Task 7 (`TestCompletePendingPreservesIP` + decrement assertion after UnitSelect).
4. **`Start` called again after a failed `Start`** — the admin UI retries after a port clash; the retry must not hang and must be able to bind. Pinned in Task 8 (`TestStartRetryAfterFailure`).
5. **A malformed-packet flood not hiding a later, different failure** — parse and banned limiters are separate from each other and from HELLO's. Pinned in Task 9 (`TestParseAndBannedLimitersIndependent`).

---

### Task 1: SRV-19 — `go vet` green, keepalive comment

**Files:**
- Modify: `srs/srs_service.go:590` (delete the unreachable `return nil` after the exhaustive switch in `case events.ClientsChanged`)
- Modify: `control/server.go:129` (comment)

- [ ] **Step 1: Confirm the failing check**

Run: `go vet -tags headless ./...`
Expected: FAIL, exactly `srs\srs_service.go:590:3: unreachable code` (path separator may vary).

- [ ] **Step 2: Delete `return nil` at `srs/srs_service.go:590`; change the comment on `control/server.go:129` to `// server pings an idle connection every 60s`.**

- [ ] **Step 3: Verify**

Run: `go vet -tags headless ./... && go build -tags headless ./... && go test -race -count=1 -tags headless ./...`
Expected: all green, no vet output.

- [ ] **Step 4: Commit** — `fix(srs): drop unreachable return so go vet passes (SRV-19)`

---

### Task 2: SRV-03 — coalition isolation in `IsListeningOnFrequency`

**GATE — needs user approval before this task** (wire-visible to every client, including the C# peer: cross-coalition voice on non-global frequencies stops being delivered).

**Files:**
- Modify: `state/server.go:201-219`
- Test: `state/state_test.go`

**Interfaces:** Produces no new API; `func (s *ServerState) IsListeningOnFrequency(clientGuid, senderId uuid.UUID, frequency float32, globalFreq bool) bool` keeps its signature.

- [ ] **Step 1: Write the failing test** `TestIsListeningOnFrequency` — table-driven, using `newTestServerState()`. Fixture: sender `S` (Blue), receivers `RB` (Blue) and `RR` (Red); `RadioClients[RB]` and `RadioClients[RR]` each hold `Radios: []Radio{{Frequency: 251, Enabled: true}, {Frequency: 252, Enabled: false}}`; receiver `RN` (Blue) has a `Clients` entry but no `RadioClients` entry.

| name | receiver | sender | freq | global | want |
|---|---|---|---|---|---|
| same coalition enabled | RB | S | 251 | false | true |
| cross coalition non-global | RR | S | 251 | false | **false** |
| cross coalition global | RR | S | 251 | true | true |
| radio disabled | RB | S | 252 | false | false |
| frequency not tuned | RB | S | 300 | false | false |
| unknown sender | RB | `uuid.New()` | 251 | false | false |
| unknown receiver | `uuid.New()` | S | 251 | false | false |
| receiver without radio state | RN | S | 251 | false | false |

- [ ] **Step 2: Run** `go test -race -count=1 -tags headless ./state/ -run TestIsListeningOnFrequency -v` — Expected: FAIL on `cross coalition non-global` (got true).

- [ ] **Step 3: Implement.** Sender from `s.Clients[senderId]`, receiver from `s.Clients[clientGuid]`, radios from `s.RadioClients[clientGuid]`; any missing → `false`; `!globalFreq && sender.Coalition != receiver.Coalition` → `false`; then the existing first-match loop returning `radio.Enabled`.

- [ ] **Step 4: Run** the test again — Expected: PASS. Then the full suite.

- [ ] **Step 5: Commit** — `fix(state): look up the receiver, not the sender, for coalition isolation (SRV-03)`

---

### Task 3: Token-error sentinels in `utils/auth.go`

**Files:**
- Modify: `utils/auth.go` (`getJWTClaims`, `GetTokenClaims`)
- Test: `utils/auth_test.go`

**Interfaces:**
- Produces: `var ErrInsufficientRole = errors.New("insufficient role")`, `var ErrTokenKeys = errors.New("token signing keys unavailable")` in package `utils`.
  - `GetTokenClaims` returns `fmt.Errorf("%w: %d, required: %d", ErrInsufficientRole, claims.RoleId, minRole)`.
  - `getJWTClaims` returns `fmt.Errorf("%w: %v", ErrTokenKeys, err)` when `getKeys` fails.

- [ ] **Step 1: Write the failing test** `TestGetTokenClaimsInsufficientRoleIsSentinel`: `tok, _ := GenerateToken(uuid.NewString(), GuestRole, "i", "s", time.Hour, testPrivKeyFile, testPubKeyFile)`; `_, err := GetTokenClaims(tok, AdminRole, testPrivKeyFile, testPubKeyFile)`; assert `errors.Is(err, ErrInsufficientRole)`. Add `TestGetTokenClaimsExpiredIsNotRoleError`: token with expiration `-time.Hour` → `err != nil`, `!errors.Is(err, ErrInsufficientRole)`, `!errors.Is(err, ErrTokenKeys)`. (`ErrTokenKeys` cannot be triggered in-process because `getKeys` is a `sync.Once` primed by `TestMain`; it is covered by Task 4's `authError` table.)

- [ ] **Step 2: Run** `go test -race -count=1 -tags headless ./utils/ -run TestGetTokenClaims -v` — Expected: FAIL to compile (`undefined: ErrInsufficientRole`).

- [ ] **Step 3: Implement** the two sentinels and wraps above.

- [ ] **Step 4: Run** the tests — PASS; full suite green.

- [ ] **Step 5: Commit** — `feat(utils): sentinel errors for insufficient role and key load failure`

---

### Task 4: SRV-07 — typed codes from the auth interceptors

**GATE — needs user approval before this task** (wire-visible: unary non-auth RPCs change from `codes.Unknown` to `Unauthenticated`/`PermissionDenied`/`Internal`; stream insufficient-role changes from `Unauthenticated` to `PermissionDenied`; error message text changes).

**Files:**
- Modify: `control/server.go` (`authInterceptor` `:398-446`, `authStreamInterceptor` `:454-501`)
- Test: `control/server_test.go`

**Interfaces:**
- Consumes: `utils.ErrInsufficientRole`, `utils.ErrTokenKeys` (Task 3).
- Produces (SV1 extends `authenticate` with its unknown-GUID check; keep these names):
  - `func (s *Server) authenticate(ctx context.Context, fullMethod string) (context.Context, error)` — exempts `AuthService` and `VoiceControlService`; returns `ctx` with `utils.ClientIDKey` set, or a status error per the table below.
  - `func authError(fullMethod string, err error) error`
  - `authInterceptor` / `authStreamInterceptor` become thin wrappers over `authenticate` (stream wraps the returned ctx in `wrappedStream`).

| Condition | Code | Message |
|---|---|---|
| malformed method path / service name | `Unauthenticated` | unchanged |
| no metadata | `Unauthenticated` | `missing metadata for <method>` |
| no `authorization` value | `Unauthenticated` | `missing authorization token for <method>` |
| `errors.Is(err, utils.ErrTokenKeys)` | `Internal` | `token verification unavailable` (log the cause at ERROR) |
| `errors.Is(err, utils.ErrInsufficientRole)` | `PermissionDenied` | `insufficient role for <method>: <err>` |
| any other token error / nil claims | `Unauthenticated` | `authentication error for <method>: <err>` |

- [ ] **Step 1: Write the failing tests.** Helper `newInterceptorTestServer(t) *Server` (NewServer with a `SettingsState` whose `Security.Token` key files point into `t.TempDir()`, discard logger) and `mintToken(t, s, role uint8, ttl time.Duration) string` via `utils.GenerateToken`.
  - `TestAuthInterceptorCodes` — table over **both** `authInterceptor` (method `/srs.SRSService/SyncClient`) and `authStreamInterceptor` (method `/srs.SRSService/SubscribeToUpdates`, with a fake `grpc.ServerStream` whose `Context()` returns the test ctx): rows `no metadata` (ctx without metadata), `no token` (metadata without `authorization`), `garbage token` (`Bearer x.y.z`), `expired token` (ttl `-time.Hour`) → `status.Code(err) == codes.Unauthenticated` and handler **not** called; `valid token` → handler called and `ctx.Value(utils.ClientIDKey)` equals the token's GUID; `/srs.AuthService/InitAuth` with no metadata → handler called.
  - `TestAuthErrorMapping` — `authError("/m", fmt.Errorf("%w: 0, required: 3", utils.ErrInsufficientRole))` → `PermissionDenied`; `authError("/m", fmt.Errorf("%w: boom", utils.ErrTokenKeys))` → `Internal` with message `token verification unavailable`; `authError("/m", errors.New("token is expired"))` → `Unauthenticated`.

- [ ] **Step 2: Run** `go test -race -count=1 -tags headless ./control/ -run 'TestAuthInterceptorCodes|TestAuthErrorMapping' -v` — Expected: FAIL (`undefined: authError`; unary rows report `Unknown`).

- [ ] **Step 3: Implement** `authenticate` and `authError` per the table; rewrite both interceptors to call `authenticate`. Remove the now-unused `fmt` usages only if nothing else uses them.

- [ ] **Step 4: Run** the tests — PASS; full suite green.

- [ ] **Step 5: Commit** — `fix(control): typed gRPC codes from the auth interceptors (SRV-07)`

---

### Task 5: SRV-04 (part 1) — panic-recovery interceptors

**GATE — needs user approval before this task** (wire-visible: a handler panic now returns `codes.Internal` instead of killing the server process).

**Files:**
- Create: `control/recovery.go`
- Create: `control/recovery_test.go`
- Modify: `control/server.go` (`clientOpts` `:121-123`; `initControlServer` options `:248-259`)

**Interfaces:**
- Produces:
  - `func (s *Server) recoveryUnaryInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp interface{}, err error)`
  - `func (s *Server) recoveryStreamInterceptor(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error)`
  - Both: deferred `recover()`; on panic log `s.logger.Error("gRPC handler panic", "method", info.FullMethod, "panic", r, "stack", string(debug.Stack()))` and set `err = status.Error(codes.Internal, "internal server error")`, `resp = nil`.

- [ ] **Step 1: Write the failing tests** in `control/recovery_test.go`: `TestRecoveryUnaryPanics` (handler `panic("boom")` → `codes.Internal`, message `internal server error`, `resp == nil`, and the message does not contain `boom`); `TestRecoveryStreamPanics` (same for stream); `TestRecoveryPassesThrough` (unary handler returns `("ok", nil)` → unchanged; handler returns `status.Error(codes.NotFound,"x")` → unchanged).

- [ ] **Step 2: Run** `go test -race -count=1 -tags headless ./control/ -run TestRecovery -v` — Expected: FAIL to compile.

- [ ] **Step 3: Implement** `recovery.go`, then wire: client server `grpc.ChainUnaryInterceptor(s.recoveryUnaryInterceptor, s.loggingInterceptor, s.authInterceptor)`, `grpc.ChainStreamInterceptor(s.recoveryStreamInterceptor, s.authStreamInterceptor)`; control server replaces `grpc.UnaryInterceptor(s.loggingInterceptor)` with `grpc.ChainUnaryInterceptor(s.recoveryUnaryInterceptor, s.loggingInterceptor)` and adds `grpc.ChainStreamInterceptor(s.recoveryStreamInterceptor)`. Recovery must be first in each chain (outermost).

- [ ] **Step 4: Run** the tests — PASS; full suite green.

- [ ] **Step 5: Commit** — `fix(control): recover handler panics as codes.Internal (SRV-04)`

---

### Task 6: SRV-04 (part 2) + SRV-07 (AuthService) — nil capabilities, peer helper, body-only outliers

**GATE — needs user approval before this task** (wire-visible: `InitAuth` without capabilities gets a new `Success:false` body `Missing client capabilities`; the 6 outlier sites now deliver their existing `Success:false` body instead of a bodiless `codes.Unknown` error).

**Files:**
- Modify: `srs/auth_service.go` (`InitAuth` `:104-159`; the `}, err` returns at `:284,360,411,620,704,788`; every `p, _ := peer.FromContext(ctx)` / `p.Addr.String()` in the six RPCs)
- Test: `srs/auth_service_test.go`

**Interfaces:**
- Produces: `func peerAddr(ctx context.Context) (addr, host string)` — `addr` is `p.Addr.String()` or `"unknown"`; `host` is `net.SplitHostPort` host or `""`. Task 7 uses `host` as the per-IP key.

- [ ] **Step 1: Write the failing tests.**
  - `TestInitAuthNilCapabilities`: `resp, err := s.InitAuth(testPeerContext(), &pb.AuthInitRequest{})` → `err == nil`, `!resp.GetSuccess()`, `resp.GetErrorMessage() == "Missing client capabilities"`.
  - `TestInitAuthWithoutPeer`: same call with `context.Background()` → no panic, same body.
  - `TestInitAuthNilDistributionModes`: `Capabilities{Version: MinClientVersion}` with no modes → `err == nil`, `!Success`, message has prefix `Unsupported distribution capabilities`.
  - `TestInvalidClientGuidReturnsBody`: with guest and plugin auth enabled on the test settings, call `GuestLogin`, `StartAuth`, `ContinueAuth`, `UnitSelect` with `ClientGuid: "not-a-uuid"` → each `err == nil`, `!Success`, error message `Invalid ClientGuid`.
  - (Token-generation failure sites `:360,788` cannot be triggered in-process because `utils.getKeys` is a `sync.Once`; Step 4's grep pins them.)

- [ ] **Step 2: Run** `go test -race -count=1 -tags headless ./srs/ -run 'TestInitAuth|TestInvalidClientGuid' -v` — Expected: FAIL (nil-pointer panic in InitAuth; invalid-GUID calls return non-nil err).

- [ ] **Step 3: Implement.** `InitAuth` first checks `request.GetCapabilities() == nil` and returns the body; all reads use getters. Add `peerAddr` and use it in all six RPCs. Change the six `}, err` returns to `}, nil` (keep the existing log lines).

- [ ] **Step 4: Verify**

Run: the tests (PASS), then `grep -nE '\}, err$' srs/auth_service.go`
Expected: no output. Full suite green.

- [ ] **Step 5: Commit** — `fix(srs): InitAuth rejects missing capabilities; auth RPCs never return body and error (SRV-04, SRV-07)`

---

### Task 7: SRV-17 — bounded, swept pending logins

**GATE — needs user approval before this task** (user-visible: pending logins now expire after 5 idle minutes instead of 20 fixed; `InitAuth` can return two new `Success:false` messages under load).

**Files:**
- Modify: `srs/auth_service.go` (struct, `NewAuthServer`, `InitAuth`, `GuestLogin`, `StartAuth`, `ContinueAuth`, `UnitSelect`, `handleAuthComplete`; delete `removeExpiredAuthenticatingClients`)
- Test: `srs/auth_service_test.go` (also add `t.Cleanup(authServer.Stop)` to `newGuestLoginTestServer`)

**Interfaces:**
- Consumes: `peerAddr` (Task 6).
- Produces (Task 8 calls `Stop`):
  - consts `pendingLoginTTL = 5 * time.Minute`, `maxPendingLogins = 10000`, `maxPendingLoginsPerIP = 32`, `pendingSweepInterval = 60 * time.Second`
  - `AuthenticatingClient.IP string`
  - `AuthServer` fields `pendingPerIP map[string]int`, `now func() time.Time`, `maxPending int`, `maxPendingPerIP int`, `stopSweep chan struct{}`, `sweepDone chan struct{}`, `stopOnce sync.Once`
  - `func (s *AuthServer) Stop()` — idempotent; closes `stopSweep`, waits for `sweepDone`.
  - `func (s *AuthServer) sweepExpired()`
  - `func (s *AuthServer) deletePendingLocked(guid uuid.UUID)` (caller holds `s.mu`)
  - `func (s *AuthServer) touchPending(guid uuid.UUID) (AuthenticatingClient, bool)` — missing/expired → `false` (expired is deleted); live → re-arm `Expires = s.now().Add(pendingLoginTTL)`, return a value copy.
  - `func (s *AuthServer) setPluginSession(guid uuid.UUID, plugin, sessionID string) bool`
  - `func (s *AuthServer) completePending(guid uuid.UUID, name, secret string, roles []uint8, units []*pb.UnitSelection) bool` — in-place update, re-arms TTL, keeps `IP`.

- [ ] **Step 1: Write the failing tests** (inject `s.now` with a mutable fake clock; helper `peerCtx(ip string) context.Context`; helper `validInitReq()` with `Version: MinClientVersion` and `SupportedDistributionModes: {STANDALONE}`):
  - `TestPendingLoginConstants`: the four constants equal 5m, 10000, 32, 60s; a fresh server has `maxPending == 10000`, `maxPendingPerIP == 32`.
  - `TestInitAuthPerIPCap`: 32 InitAuths from `10.0.0.1` succeed; the 33rd → `err == nil`, `!Success`, message `Too many pending logins from your address, please try again shortly`; one from `10.0.0.2` succeeds; `len(authenticatingClients) == 33`.
  - `TestInitAuthGlobalCap`: `s.maxPending = 3`; 3 InitAuths from distinct IPs succeed; the 4th → message `Server is busy: too many pending logins, please try again shortly`; the original 3 GUIDs are all still present (no eviction).
  - `TestExpiredPendingRejectedWithoutSweep`: InitAuth, advance clock by `pendingLoginTTL + time.Second`, GuestLogin with that GUID → body `ClientGuid not found, please initialize first`; entry and `pendingPerIP` count gone.
  - `TestLoginStepRearmsTTL`: InitAuth; advance 4m; GuestLogin with a wrong password (body `No coalition found with that password`); advance 4m; GuestLogin with the right password → `Success`.
  - `TestSweepExpiredRemovesAndDecrements`: 2 entries from `10.0.0.1`, advance past TTL, `s.sweepExpired()` → map empty, `pendingPerIP` has no `10.0.0.1` key.
  - `TestSuccessfulLoginDecrementsPerIP`: InitAuth + successful GuestLogin → `pendingPerIP["127.0.0.1"] == 0`/absent.
  - `TestSetPluginSessionMissingGUID`: `s.setPluginSession(uuid.New(), "p", "sid")` → `false`, no panic.
  - `TestCompletePendingMissingGUID`: `s.completePending(uuid.New(), …)` → `false`, no panic.
  - `TestCompletePendingPreservesIP`: InitAuth from `10.0.0.9`; `completePending(guid, "Pilot", "a-b-c", []uint8{0}, nil)` → `true`; entry `IP == "10.0.0.9"`, `Secret == "a-b-c"`; `pendingPerIP["10.0.0.9"] == 1`.
  - `TestAuthServerStopEndsSweeper`: `s.Stop()` returns within 1 s; `<-s.sweepDone` is closed; a second `s.Stop()` does not panic.

- [ ] **Step 2: Run** `go test -race -count=1 -tags headless ./srs/ -run 'TestPending|TestInitAuth.*Cap|TestExpiredPending|TestLoginStep|TestSweep|TestSuccessfulLogin|TestSetPluginSession|TestCompletePending|TestAuthServerStop' -v` — Expected: FAIL to compile.

- [ ] **Step 3: Implement.**
  - `NewAuthServer` initialises the new fields and starts the sweeper (`time.NewTicker(pendingSweepInterval)`; on tick `sweepExpired()`; on `stopSweep` stop the ticker and `close(sweepDone)`).
  - `InitAuth`: after ban/version/distribution checks, under `s.mu.Lock()` check global cap, then per-IP cap (messages above), else insert `{IP: host, Expires: s.now().Add(pendingLoginTTL)}` and `pendingPerIP[host]++`.
  - GuestLogin, StartAuth, ContinueAuth, UnitSelect: replace the `removeExpiredAuthenticatingClients()` call and the existence check with `touchPending`; ContinueAuth/UnitSelect use the returned copy. Success paths delete via `deletePendingLocked`.
  - StartAuth `AUTH_CONTINUE`: `setPluginSession`; on `false` return the `ClientGuid not found, please initialize first` body.
  - `handleAuthComplete`: `completePending` instead of replacing the entry; on `false` return the same body.
  - Delete `removeExpiredAuthenticatingClients`.

- [ ] **Step 4: Run** the tests — PASS; then full suite with `-race` green (the existing `TestGuestLoginConsumesAuthenticatingClient` must still pass).

- [ ] **Step 5: Commit** — `fix(srs): cap, TTL and sweep pending logins (SRV-17)`

---

### Task 8: SRV-05 — `control.Server.Start` fails cleanly; stop the auth sweeper

**Files:**
- Modify: `control/server.go` (`Server` struct, `Start` `:74-170`, `initControlServer` `:237-262`, `Stop` `:321-360`)
- Test: `control/server_test.go`

**Interfaces:**
- Consumes: `(*srs.AuthServer).Stop()` (Task 7); the interceptor chains from Task 5.
- Produces: `func (s *Server) initControlServer(controlServer voicecontrolpb.VoiceControlServiceServer) error`; `Server.authServer *srs.AuthServer`.

- [ ] **Step 1: Write the failing tests.** Helper `newStartTestServer(t, mode state.DistributionMode, vcPort int, keyDir string) *Server` (fresh `ServerState`, `SettingsState` with `VoiceControl{ListenHost: "127.0.0.1", Port: vcPort, PrivateKeyFile/CertificateFile under keyDir, ServerName: "localhost"}` and token keys in `t.TempDir()`, `DistributionState{DistributionMode: mode}`, `events.NewEventBus()` with `t.Cleanup(bus.Stop)`). Helper `occupy(t) (addr string, port int)` opens a TCP listener on `127.0.0.1:0` and closes it in cleanup. Helper `freeAddr(t) string`.
  - `TestStartClientPortOccupied`: standalone; `Start(occupiedAddr, stop)` → error; `IsRunning()` returns `false` within 1 s (run it in a goroutine, fail on timeout).
  - `TestStartControlPortOccupiedReleasesClientPort`: control mode, VoiceControl port occupied, client addr free → error; `net.Listen("tcp", clientAddr)` then succeeds; `IsRunning()` returns within 1 s.
  - `TestStartControlKeypairFailure`: control mode, free VoiceControl port, `keyDir` = a path *under a regular file* (`filepath.Join(fileInTemp, "sub")`) → `Start` returns an error containing `control server TLS`, no panic, client addr rebindable.
  - `TestStartRetryAfterFailure`: occupy an address, `Start` on it → error; close the occupier; `Start` the **same** `*Server` on that address → `nil`, `IsRunning()` true; `Stop()`; the address is rebindable.
  - `TestStartStopReleasesPort`: standalone happy path `Start` → `nil`, `IsRunning()` true; `Stop()` → address rebindable.

- [ ] **Step 2: Run** `go test -race -count=1 -tags headless ./control/ -run TestStart -v -timeout 60s` — Expected: FAIL (`IsRunning` timeout; keypair case panics on nil `controlGrpcServer`).

- [ ] **Step 3: Implement.** In `Start`, after `s.mu.Lock()` define `fail := func(err error) error` (close+nil both listeners, nil `controlGrpcServer`, `s.mu.Unlock()`, return err) and route every error through it, including the existing TLS-creds error (drop its inline cleanup). Order: client listener → control listener → client creds → build `clientGrpcServer` → `voiceontrol.NewVoiceControlServer` → (control mode) `initControlServer` (error → `fail(fmt.Errorf("control server TLS: %w", err))`) → only then `srs.NewSimpleRadioServer` and `srs.NewAuthServer` (store in `s.authServer`), registrations, health, `isRunning = true`. `initControlServer` returns the keypair error instead of logging and returning. `Stop` calls `s.authServer.Stop()` (nil-checked) next to `s.srsServer.Stop()`.

- [ ] **Step 4: Run** the tests — PASS; full suite green.

- [ ] **Step 5: Commit** — `fix(control): release lock and listeners on every Start failure (SRV-05)`

---

### Task 9: SRV-14 — rate-limit UDP parse and banned-IP logs

**Files:**
- Modify: `voice/server.go` (`Server` struct `:93-107`; `Listen` ban block `:190-198`; `handlePacket` parse branch `:214-219`)
- Test: `voice/server_test.go`

**Interfaces:**
- Produces: `Server.parseRejects rejectLimiter`, `Server.bannedRejects rejectLimiter`; `func (v *Server) dropIfBanned(addr *net.UDPAddr) bool` (true = drop; logs through `bannedRejects`).

- [ ] **Step 1: Write the failing tests.** Extend `capturingHandler` to also keep `records []capturedRecord` (`level slog.Level`, `msg string`, `attrs map[string]slog.Value`) without changing `reasons`/`snapshot()`; add `recordsWithMsg(msg string) []capturedRecord`.
  - `TestParseFailureLogRateLimited`: `newCapturingTestServer`; call `s.handlePacket([]byte{0xFF}, addr)` 50 times → exactly one record with msg `Failed to parse voice packet`, level `slog.LevelWarn`; no record at ERROR. Then set `s.parseRejects.lastLogged = time.Now().Add(-rejectLogWindow)` and call once more → a second record whose `suppressed_since_last` is `49`.
  - `TestBannedLogRateLimited`: `ServerState.BannedState.BannedClients` contains `127.0.0.1`; 50 × `s.dropIfBanned(addr)` → all `true`; exactly one `Banned client attempted to connect` WARN; after rewinding `bannedRejects.lastLogged`, next record has `suppressed_since_last == 49`. A non-banned address returns `false` and logs nothing.
  - `TestParseAndBannedLimitersIndependent`: 10 parse failures then one `dropIfBanned` on a banned address → one record of each message.

- [ ] **Step 2: Run** `go test -race -count=1 -tags headless ./voice/ -run 'TestParseFailure|TestBannedLog|TestParseAndBanned' -v` — Expected: FAIL to compile (`dropIfBanned` undefined).

- [ ] **Step 3: Implement.** Move the ban check into `dropIfBanned` (same `FindByFunc` lookup under `serverState.RLock`), log via `bannedRejects.shouldLog(time.Now())` with `"IP"` and `"suppressed_since_last"`; `Listen` calls it. In `handlePacket`, replace the ERROR with a `parseRejects`-gated `Warn("Failed to parse voice packet", "IP", addr.IP.String(), "error", err, "suppressed_since_last", n)`.

- [ ] **Step 4: Run** the tests — PASS; full suite green.

- [ ] **Step 5: Commit** — `fix(voice): rate-limit parse-failure and banned-IP logs (SRV-14)`

---

### Task 10: Final verification, commit design + plan, open PR

**GATE — needs user approval before this task** (opening the PR publishes the wire-visible changes from Tasks 4–7 for merge; the PR body must list them).

**Files:**
- Create: `docs/superpowers/specs/2026-10-27-phase-6-9-sv3-server-hardening-design.md` (copy of the approved `design-SV3.md`)
- Create: `docs/superpowers/plans/2026-10-27-phase-6-9-sv3-server-hardening.md` (this plan)

- [ ] **Step 1: Full verification** (superpowers:verification-before-completion)

Run: `go build -tags headless ./... && go vet -tags headless ./... && go test -race -count=1 -tags headless ./...`
Expected: build and vet produce no output; every package `ok`. Report failures verbatim.

- [ ] **Step 2: Confirm no dependency drift** — `git diff origin/main -- go.mod go.sum` is empty.

- [ ] **Step 3: Commit the docs** — `docs(phase-6.9): SV3 server-hardening design and plan`

- [ ] **Step 4: Code review** — superpowers:requesting-code-review on the branch diff against `origin/main`; fix findings in new commits; re-run Step 1.

- [ ] **Step 5: Push and open the PR** against `main` with `gh pr create`. Title: `Phase 6.9 / SV3: server hardening`. Body sections: Summary (one line per SRV id); **Behaviour changes** (design §6 verbatim, including "SRV-03: cross-coalition non-global voice stops"); **Contract for CL1** (the interceptor code table from Task 4); **Token-refresh text for DOC** (design §3 SRV-07 block); Out of scope (design §7); Verification (Step 1 output). End the body with `🤖 Generated with [Claude Code](https://claude.com/claude-code)`.

- [ ] **Step 6: Hand back** the PR URL and the manual-verification items below to the orchestrator.

## Manual-verification items (for `docs/superpowers/plans/2026-10-27-phase-6-9-manual-verification.md`)

- [ ] **SV3 / SRV-03:** two VCS clients logged into different coalitions (Blue, Red) on the same non-global frequency cannot hear each other; on a global frequency they can; two clients in the same coalition can.
- [ ] **SV3 / SRV-07:** a VCS client whose token has expired (set `security.token.expiration` to e.g. 120 s on a test server) gets `Unauthenticated` on its next `SyncClient`/`Ping`, and the server log shows no `Unknown` gRPC error for it.
- [ ] **SV3 / SRV-07:** a wrong coalition password still shows the existing `No coalition found with that password` message in the VCS client (body path unchanged).
- [ ] **SV3 / SRV-05:** start the server with the client gRPC port already in use: the admin UI reports the error and does not freeze; free the port and start again from the admin UI: it starts.
- [ ] **SV3 / SRV-14:** spray garbage UDP at the voice port (e.g. `head -c 64 /dev/urandom | nc -u <host> <voice-port>` in a loop): the log shows at most one `Failed to parse voice packet` WARN per 30 s with a `suppressed_since_last` count.
- [ ] **SV3 / C# peer (blocked on PR #253):** once the C# peer can authenticate, confirm it handles `Unauthenticated` on non-auth RPCs and the `Success:false` bodies from the 6 former outlier sites.

## Execution notes (2026-10-27)

Tasks 1–9 were executed in order, test-first, each ending green on the full suite. Deviations from the steps above:

- **Extra commit after Task 9: `fix(control): close the Start/Stop races the SRV-05 tests exposed`.** The new `TestStartStopReleasesPort` / `TestStartRetryAfterFailure` (Task 8) call `Stop` immediately after a successful `Start`, which exposed two races that were already on `main`. (1) `serveClient`/`serveControl` read `s.clientGrpcServer`/`s.clientListener` without the lock while `Stop` nils them. The race detector flagged it, and `Serve` was called on a nil `*grpc.Server` and panicked. (2) `GracefulStop` only closes a listener that `Serve` has already registered. When `Stop` won the race, the port stayed bound until after `Stop` returned, so an immediate rebind failed intermittently. Fix: `Start` hands each serve goroutine its server and listener while it holds the lock, and `Stop` also closes both listeners itself. This is internal only: nothing changes on the wire. The control package was then stress-run 40× under `-race` and passed every time.
- `mintToken` (Task 4 helper) returns the token **and** its GUID so the valid-token row can assert `utils.ClientIDKey`.
- `capturingHandler` (Task 9) gained a `recordsAtLevel` helper alongside `recordsWithMsg` for the "no ERROR record" assertion.
- The panic-recovery interceptors log through a small `logPanic` helper. The fields are the ones the plan specifies (`method`, `panic`, `stack`).
- `gofmt` realigned the `control.Server` struct, which was not gofmt-clean on `main`.
- Task 10 Step 4 (code review) and Steps 5–6 (push, PR) were not run by the implementing agent. A separate reviewer runs before the PR.
- **Review follow-up commit: `fix(control): Stop tears down even after a Serve failure`.** The independent review (verdict: approve, three minors) found that `Stop` returned early when `isRunning` was already false. That happens after a `Serve` failure, and the early return used up `stopOnce` without stopping the AuthServer's new pending-login sweeper. `Stop` now has no early return; it copies out and clears the server, listener and service fields under `s.mu` (which also fixes the second minor, unlocked writes that were already on `main`), and then stops each one outside the lock. Covered by `TestStopAfterServeFailureStillTearsDown`, which failed first (port still bound). The third minor (a single user's wrong-password retries can trip the per-IP cap) is documented under design R3, with no code change.
