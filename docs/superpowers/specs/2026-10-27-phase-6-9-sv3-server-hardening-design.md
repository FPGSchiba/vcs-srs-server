**Status: approved 2026-10-27**

# Phase 6.9 / SV3: Server hardening

**Repo:** `vcs-srs-server`. Branch `chore/cleanup-sv3` from `origin/main` @ `c3096ce` (after #223).
**Umbrella:** `docs/superpowers/specs/2026-10-27-vcs-client-phase-6-9-cleanup-umbrella-design.md` (client repo), §3 SV3, §5, §9, §10.
**Plan:** `docs/superpowers/plans/2026-10-27-phase-6-9-sv3-server-hardening.md`.

## Decisions

| # | Decision | Value | By |
|---|---|---|---|
| D1 | SRV-07: AuthService failure shape | `Success:false` + `ErrorMessage` body with a **nil** error. Only the 6 outlier sites (`srs/auth_service.go:284,360,411,620,704,788`) change, from `}, err` to `}, nil`. The ~30 body-only sites stay as they are. No gRPC status code is ever returned for an AuthService *business* failure. | user decision (umbrella §9, revised) |
| D2 | SRV-07: auth interceptor on non-auth RPCs | `codes.Unauthenticated` for missing metadata / missing token / invalid, malformed or expired token; `codes.PermissionDenied` for insufficient role. Replaces `fmt.Errorf` (`codes.Unknown`) on the unary path. | user decision |
| D3 | SRV-04: nil `Capabilities` on `InitAuth` | `Success:false` body, message `Missing client capabilities`, nil error. | user decision |
| D4 | SRV-04: recovered handler panic | `codes.Internal`, message `internal server error`. | user decision |
| D5 | SRV-17: pending-login limits | TTL **5 min**, re-armed on each login step; global cap **10,000**; per-IP cap **32**; sweep every **60 s**. | user decision (Q1 = "Moderate") |
| D6 | SRV-17: over-cap behaviour | The **new** `InitAuth` is refused with a `Success:false` body (not `ResourceExhausted`, reconciling Q1 with D1: AuthService failures are bodies). An existing pending entry is never evicted. | user decision (reconciled) |
| D7 | Recovery interceptor | Hand-rolled (~30 lines), no `go-grpc-middleware` dependency. | worker's call |
| D8 | Interceptor code-path sentinels | `utils.ErrInsufficientRole`, `utils.ErrTokenKeys`, matched with `errors.Is`. | worker's call |
| D9 | SRV-03 semantics | Coalition isolation only; first-radio-match-on-frequency semantics unchanged. | worker's call |
| D10 | SV1 extension point | The two interceptors share one `authenticate` helper and one `authError` mapping. SV1 adds its unknown-GUID check (SRV-06, `Unauthenticated`) inside `authenticate`. | worker's call |

## 0. Baseline (origin/main `c3096ce`, run 2026-10-27)

Run from a `git archive` export with the gitignored `*.pb.go` and `graphql/generated` copied in (proto and graphql inputs identical to origin/main). Go 1.26.5.

- `go build -tags headless ./...`: **green**.
- `go vet -tags headless ./...`: **fails**, single diagnostic `srs\srs_service.go:590:3: unreachable code`.
- `go test -race -count=1 -tags headless ./...`: **green**, 11 packages ok.

## 1. Intent

Make the server impossible to crash or deadlock through the paths found in the scan. Enforce coalition isolation on voice. Give the non-auth interceptor typed status codes so the VCS client (CL1) can detect "session expired". Make every AuthService RPC return a body and never a body-plus-error. Get `go vet` green so SV5 can gate on it. SRV-03, SRV-07 (interceptor codes), SRV-04 (InitAuth body) and SRV-17 (over-cap body) are visible on the wire; everything else is internal or operator-visible.

## 2. Re-verification

| ID | Status | Evidence |
|---|---|---|
| SRV-03 | **holds** | `state/server.go:201-219`: `receiver := s.Clients[senderId]`. A probe showed a Red receiver hearing a Blue sender on non-global 251 MHz. Only caller: `voice/server.go:556`. Distributed voice nodes fill `Coalition` from control sync (`voiceontrol/client.go:272,304`), so the fix also works there. |
| SRV-04 | **holds** | `srs/auth_service.go:106,127,132,137` read `request.Capabilities.*` directly; `InitAuth(ctx, &pb.AuthInitRequest{})` panics. `control/server.go:122-123` chains no recovery interceptor; grpc-go does not recover handler panics, so the process dies. Adjacent: every AuthService RPC does `p, _ := peer.FromContext(ctx); p.Addr.String()`, which panics on a context without a peer (never in real gRPC; matters for tests and is fixed with a helper). |
| SRV-05 | **holds-with-correction** | `control/server.go:81-95`: both `net.Listen` failures return with `s.mu` locked; `IsRunning()` then blocks forever. The control-listen failure also leaks `clientListener`. **Addition:** `initControlServer` (`:237-247`) only logs on a VoiceControl keypair failure, leaving `controlGrpcServer` nil, and `:155` plus `serveControl` then dereference it. Same function, same failure class, folded in. |
| SRV-17 | **holds** | `auth_service.go:144-149` inserts `{Expires: now+20m}` with no cap; every auth RPC runs a full O(n) `removeExpiredAuthenticatingClients()` under the write lock (`:108,181,275,401,611`). **Adjacent:** `:452-454` writes `authenticatingClients[clientGuid].PluginUsed` after re-taking the lock without re-checking existence (nil dereference if swept); `handleAuthComplete` (`:511-518`) replaces the entry wholesale. Both fixed here. |
| SRV-14 | **holds** | `voice/server.go:195-197` WARNs per packet from a banned IP; `:214-216` ERRORs per malformed datagram. Neither is rate-limited; `rejectLimiter` (`:37-85`) only covers HELLO. |
| SRV-19 | **holds** | `go vet` reproduces `srs_service.go:590:3: unreachable code`. `control/server.go:129` comment says 30s for a 60s value. |
| SRV-07 | **holds-with-correction** | Unary `authInterceptor` returns plain `fmt.Errorf` at `control/server.go:419,424,435,439` (`codes.Unknown`). Stream interceptor (`:454-501`) already uses `codes.Unauthenticated`, including for insufficient role. Only 6 AuthService sites return body **and** error (`auth_service.go:284,360,411,620,704,788`); grpc-go drops the body when the error is non-nil, so those clients see `Unknown` with no body. The ~30 other failure sites return a body with a nil error, and clients (the VCS client's `GetSuccess()` branches in `internal/auth/{auth,guest}.go`, and the C# peer) read them. Token expiry is 28800 s (`state/settings.go:171`, `example.config.yaml:29`). |
| SRV-20 | **holds** (SV3's share) | No tests for `IsListeningOnFrequency`, for `InitAuth` with nil capabilities, for `control.Server.Start` failure paths, or for interceptor codes. All written test-first below. |

## 3. Per-finding approach

### SRV-03: coalition isolation (`state/server.go`)
Look up sender by `senderId` and receiver by `clientGuid`; either missing → `false`. Receiver with no `RadioClients` entry → `false`. `!globalFreq && sender.Coalition != receiver.Coalition` → `false`. Then the existing first-match-on-frequency loop returns `radio.Enabled` (D9).

### SRV-04: nil capabilities + panic recovery
- `InitAuth`: `request.GetCapabilities() == nil` → `&pb.AuthInitResponse{Success:false, InitResult: ErrorMessage{"Missing client capabilities"}}, nil` (D3). All other reads use getters.
- New helper `peerAddr(ctx) (addr, host string)` in `srs/auth_service.go` returning `"unknown"`/`""` when there is no peer; every AuthService RPC uses it instead of `p.Addr.String()`.
- New `control/recovery.go`: `recoveryUnaryInterceptor` and `recoveryStreamInterceptor` (methods on `*Server` so they use `s.logger`). They `recover()`, log ERROR with `method`, `panic` and `stack` (`debug.Stack()`), and return `status.Error(codes.Internal, "internal server error")` (D4); no panic text reaches the client. They go **outermost**: `ChainUnaryInterceptor(recovery, logging, auth)`, `ChainStreamInterceptor(recovery, auth)`, and on `controlGrpcServer` `ChainUnaryInterceptor(recovery, logging)` + `ChainStreamInterceptor(recovery)`. Hand-rolled, no dependency (D7).
- Limitation (stated, not fixed): recovery covers the handler goroutine only. Panics in goroutines that handlers spawn (event-bus forwarding, `go v.handlePacket`) still crash the process.

### SRV-05: `control.Server.Start` failure cleanup
- Every error return after `s.mu.Lock()` goes through one local `fail := func(err error) error` that closes and nils both listeners, nils `controlGrpcServer`, unlocks, and returns `err`. The existing TLS-error cleanup is folded into it.
- All fallible setup runs **before** anything that starts a goroutine: listeners → client TLS creds → (control mode) `initControlServer(controlServer) error` → only then `NewSimpleRadioServer` (starts its cleanup goroutine) and `NewAuthServer` (starts the SRV-17 sweeper). So `fail` never has to stop a goroutine.
- `initControlServer` returns `error` (keypair load failure wrapped as `control server TLS: %w`).
- `Server` gains `authServer *srs.AuthServer`; `Stop()` calls `authServer.Stop()` next to `srsServer.Stop()`.

### SRV-17: bound `authenticatingClients`
Constants in `srs/auth_service.go` (D5): `pendingLoginTTL = 5 * time.Minute`, `maxPendingLogins = 10000`, `maxPendingLoginsPerIP = 32`, `pendingSweepInterval = 60 * time.Second`.

Mechanism:
- `AuthenticatingClient` gains `IP string` (peer host).
- `AuthServer` gains `pendingPerIP map[string]int`, `now func() time.Time` (default `time.Now`; tests inject), `maxPending`, `maxPendingPerIP` (initialised from the constants; tests may lower them), and `stopSweep chan struct{}`, `sweepDone chan struct{}`, `stopOnce sync.Once`.
- All deletes go through `deletePendingLocked(guid)`, which also decrements `pendingPerIP` (removing the key at 0). Callers: GuestLogin success, UnitSelect success, sweep, expired lookup.
- `InitAuth`, after the ban, version and distribution checks: under `s.mu.Lock()`, if `len(authenticatingClients) >= maxPending` → body `Server is busy: too many pending logins, please try again shortly`; else if `pendingPerIP[host] >= maxPendingPerIP` → body `Too many pending logins from your address, please try again shortly` (D6). Otherwise insert `{IP: host, Expires: now()+pendingLoginTTL}` and increment. Existing entries are never evicted.
- `touchPending(guid) (AuthenticatingClient, bool)`: under the write lock, missing → `false`; expired (`now().After(Expires)`) → `deletePendingLocked`, `false`; else re-arm `Expires = now()+pendingLoginTTL` and return a **value copy** (so callers never read shared fields outside the lock). Every login step uses it: GuestLogin, StartAuth, ContinueAuth, UnitSelect. "Re-armed on each login step" (D5) means every one of these RPCs that finds a live pending entry.
- `setPluginSession(guid, plugin, sessionID string) bool` (StartAuth `AUTH_CONTINUE`, replacing `:452-454`) and `completePending(guid, name, secret string, roles []uint8, units []*pb.UnitSelection) bool` (`handleAuthComplete`, replacing the wholesale replacement at `:511-518`) update the existing entry in place under the lock with comma-ok, re-arm the TTL, and **preserve `IP`** so the per-IP count stays correct. A missing entry yields the existing body `ClientGuid not found, please initialize first`.
- `sweepExpired()` deletes every expired entry (one O(n) pass every 60 s instead of on every RPC). `NewAuthServer` starts the sweeper goroutine; `Stop()` (idempotent) closes `stopSweep` and waits on `sweepDone`.
- The per-RPC `removeExpiredAuthenticatingClients()` calls and the function itself are removed.
- An entry is not deleted on a failed GuestLogin (unchanged): our client re-runs InitAuth on every Connect.

### SRV-14: UDP log flood (`voice/server.go`)
- `Server` gains `parseRejects rejectLimiter` and `bannedRejects rejectLimiter`, keyed by fixed reason, never by address (same rationale as `:26-36`).
- `handlePacket` parse failure: `Warn("Failed to parse voice packet", "IP", addr.IP.String(), "error", err, "suppressed_since_last", n)` through `parseRejects` (demoted from ERROR).
- The banned check moves out of the `Listen` loop into `func (v *Server) dropIfBanned(addr *net.UDPAddr) bool`, which logs `Warn("Banned client attempted to connect", "IP", …, "suppressed_since_last", n)` through `bannedRejects`.

### SRV-19: vet + comment
- Delete `srs/srs_service.go:590` `return nil`.
- `control/server.go:129` comment becomes `// server pings an idle connection every 60s`.
- `go vet` in `test.yml` is SV5's.

### SRV-07: typed interceptor errors, body-only AuthService
**AuthService (D1).** The six `}, err` returns become `}, nil` (the error is still logged). Message strings unchanged. After the change `grep -nE '\}, err$' srs/auth_service.go` returns nothing.

**Interceptor (D2).** Both interceptors call one helper:

```go
// authenticate validates the bearer token for fullMethod and returns a context
// carrying the client GUID, or a gRPC status error. AuthService and
// VoiceControlService are exempt. SV1 adds its unknown-GUID check here.
func (s *Server) authenticate(ctx context.Context, fullMethod string) (context.Context, error)
// authError maps a utils.GetTokenClaims error to a status error.
func authError(fullMethod string, err error) error
```

| Condition | Code | Message |
|---|---|---|
| malformed method path / service name | `Unauthenticated` | unchanged |
| no metadata | `Unauthenticated` | `missing metadata for <method>` |
| no `authorization` value | `Unauthenticated` | `missing authorization token for <method>` |
| bad signature, malformed, **expired**, missing claims | `Unauthenticated` | `authentication error for <method>: <err>` |
| `errors.Is(err, utils.ErrInsufficientRole)` | `PermissionDenied` *(stream changes from `Unauthenticated`)* | `insufficient role for <method>: <err>` |
| `errors.Is(err, utils.ErrTokenKeys)` (server key load failure) | `Internal` | `token verification unavailable` |
| recovered panic (any service) | `Internal` | `internal server error` |

`utils/auth.go` gains `ErrInsufficientRole` (wrapped by `GetTokenClaims`: `fmt.Errorf("%w: %d, required: %d", ErrInsufficientRole, …)`) and `ErrTokenKeys` (wrapped by `getJWTClaims` around a `getKeys` failure). `utils/auth.go` is not in the umbrella's SV3 file list: worker's-call addition (D8), internal.

CL1 contract: on a **non-Auth** RPC, `Unauthenticated` or `PermissionDenied` = session expired / not allowed. AuthService RPCs never return a status code for a business failure; they return `Success:false` bodies (CL1 keeps its `GetSuccess()` handling).

**Token-refresh text handed to DOC** (new PROTO_GAPS entry, next number #12):
> **12. Token refresh.** *Today:* guest/plugin JWTs expire after `security.token.expiration` (default 28800 s / 8 h) and there is no RPC to renew one; after expiry every SRS RPC fails with `codes.Unauthenticated` (from Phase 6.9 / SV3; previously `codes.Unknown` on unary RPCs) and the client must run `InitAuth` → `GuestLogin` again. *Ask:* `AuthService.RefreshToken(RefreshTokenRequest{}) returns (RefreshTokenResponse{token})`, authenticated with the current, still-valid token, re-checking that the GUID is still in `serverState.Clients` (SV1's SRV-06 rule) and not banned. *Client use:* refresh at ~80 % of lifetime so a long session never hits the expiry path.

## 4. Tests first (TDD, all `-tags headless`)
1. `state/state_test.go` `TestIsListeningOnFrequency` table: same coalition + enabled (true); cross coalition non-global (**false**, fails today); cross coalition global (true); radio disabled (false); frequency not tuned (false); unknown sender (false); unknown receiver (false); receiver with no radio state (false).
2. `srs/auth_service_test.go` SRV-04/SRV-07: `InitAuth` with an empty request and with a context without a peer → `err == nil`, `Success == false`, message `Missing client capabilities`, no panic. The four `Invalid ClientGuid` sites → `err == nil`, body `Invalid ClientGuid`. A source-level check (`grep`) that no `}, err` remains.
3. `srs/auth_service_test.go` SRV-17: constants equal D5's values; per-IP cap (32 from one IP, 33rd refused with the per-IP body, another IP still accepted); global cap (lowered `maxPending`); refusal never evicts; expired entry rejected by GuestLogin without a sweep (injected clock); a login step re-arms the TTL; `sweepExpired` removes expired entries and decrements `pendingPerIP`; `Stop()` ends the sweeper and is idempotent; `completePending` preserves `IP`; `setPluginSession` on a missing GUID returns `false` with no panic.
4. `control/recovery_test.go`: a panicking unary and stream handler each return `Internal` with message `internal server error`; a non-panicking handler's result passes through.
5. `control/server_test.go` interceptor table, unary and stream: no metadata, no token, garbage token, expired token → `Unauthenticated`; valid token → handler called with `utils.ClientIDKey` set; AuthService method → handler called without a token. `authError` table: wrapped `ErrInsufficientRole` → `PermissionDenied`, wrapped `ErrTokenKeys` → `Internal`, other → `Unauthenticated`.
6. `utils/auth_test.go`: `GetTokenClaims` on a guest token with `minRole = AdminRole` → `errors.Is(err, ErrInsufficientRole)`.
7. `control/server_test.go` SRV-05: client port occupied → `Start` errors and `IsRunning()` returns within 1 s; control mode with the VoiceControl port occupied → error and the client port is rebindable; VoiceControl keypair at an unwritable path → error, no panic, client port rebindable; a successful `Start` + `Stop` leaves the port rebindable.
8. `voice/server_test.go` SRV-14: 50 malformed datagrams → exactly one WARN `Failed to parse voice packet` (no ERROR); after the window, the next record has `suppressed_since_last == 49`. Same for `dropIfBanned`.
9. `go vet -tags headless ./...` green (SRV-19).

## 5. Files touched
`state/server.go`, `state/state_test.go`, `srs/auth_service.go`, `srs/auth_service_test.go`, `srs/srs_service.go` (one line), `control/server.go`, `control/server_test.go`, `control/recovery.go` (new), `control/recovery_test.go` (new), `voice/server.go`, `voice/server_test.go`, `utils/auth.go` (sentinels, D8), `utils/auth_test.go`.

## 6. Behaviour changes (wire- or user-visible)
- **SRV-03:** non-global voice no longer crosses coalitions, for every client including the C# peer.
- **SRV-07 interceptor:** unary non-auth RPCs with a bad/missing/expired token return `Unauthenticated` instead of `Unknown`; insufficient role returns `PermissionDenied` on both unary and stream (stream was `Unauthenticated`). Messages reworded (nothing string-matches them; checked client `internal/` and `frontend/src`).
- **SRV-07 AuthService:** the 6 outlier failures (invalid ClientGuid ×4, token generation ×2) now deliver their `Success:false` body instead of a bodiless `Unknown` error. Every other AuthService response is unchanged.
- **SRV-04:** `InitAuth` without capabilities gets a `Success:false` body instead of crashing the server; any handler panic becomes `Internal`.
- **SRV-17:** pending logins expire after 5 idle minutes (was 20 fixed); under load or abuse `InitAuth` returns one of two new `Success:false` messages.
- **SRV-14, SRV-05, SRV-19:** operator-visible only (log volume/level; `Start` failures reported instead of hanging).

## 7. Out of scope
- The client's reaction to the codes: CL1.
- `go vet` in CI: SV5.
- SRV-06 unknown-GUID rejection and the stream-end grace period: SV1 (it extends `authenticate`).
- Panic recovery for handler-spawned goroutines.
- The linear ban-list scan per UDP packet.
- GuestLogin/UnitSelect token-generation failure happening *after* `AddClient` (leaves a tokenless client in state; SV1's reaper removes it). Pre-existing ordering, not changed here.
- `handleAuthComplete` overwriting `PluginUsed`/`SessionId` semantics beyond the in-place update above.
- The PROTO_GAPS edit itself: DOC, using the text above.

## 8. Approval gates
- **wire / user-visible (server-behaviour):** SRV-03, SRV-07 (interceptor codes; 6 outlier sites), SRV-04 (InitAuth body), SRV-17 (TTL change, two new refusal messages).
- **cross-repo:** the interceptor code table in §3 SRV-07 is the contract CL1 consumes; CL1's design must cite it.
- **None of:** dependency change (recovery hand-rolled), proto change, CI change, file deletion, repo setting, secret.

## 9. Risks
- R1: the C# peer's UX on the 6 outlier sites and interceptor failures changes (SRV-07). Untestable here (PR #253). Manual checklist.
- R2: SV3 merges in wave 1, CL1 in wave 3. In between, the VCS client shows `rpc error: code = Unauthenticated desc = …` on an expired token instead of `code = Unknown`. Cosmetic.
- R3: a per-IP cap of 32 behind a shared NAT could refuse logins only if 32 users from one address are simultaneously mid-login; entries are removed on successful login.
  A single user can also reach the cap alone (added after code review): a failed GuestLogin keeps its pending entry, and the VCS client calls InitAuth again on every Connect, so 32 wrong-password retries from one IP within the 5-minute TTL refuse the 33rd InitAuth until the oldest entries expire. Accepted as-is (it is also a crude brute-force brake); it is listed on the PR's manual-verification checklist.
- R4: removing the per-RPC sweep makes expiry correctness depend on `touchPending`. Test 3 covers it.
- R5: cross-coalition isolation can only be heard with two real clients. Manual checklist (umbrella §8).
- R6: SV1 rebases onto `authenticate`/`authError`; the plan names them exactly so SV1's plan can target them.
