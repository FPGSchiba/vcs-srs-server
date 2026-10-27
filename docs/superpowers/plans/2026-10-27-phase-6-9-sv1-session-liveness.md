# Phase 6.9 / SV1: Server Session Liveness & Reaping Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Where this runs:** repo `vcs-srs-server` (`D:\Projects\vcs-srs-server`), in a **fresh git worktree** on branch `chore/cleanup-sv1`, branched from `origin/main` (`553b6dd` at planning time; contains SV3 #226, SV5 #225, #227). Create it with superpowers:using-git-worktrees. All paths are relative to the worktree root.

**Goal:** A live client is never evicted, a dead client is always removed through one helper (both maps, voice binding locally and on every voice node, stream ended with a typed status, one `CLIENT_LEFT`, token dead), with a 120 s reconnect grace, last-subscriber-wins duplicate subscribe, and target-only `SERVER_ACTION`.

**Architecture:** `srs` gets a per-subscription identity (`subscription`), a single atomic removal helper (`removeClientIf`, lock order `s.mu → serverState`), an injectable clock and a `reapOnce(now)` sweep. `control.authenticate` rejects GUIDs no longer in `serverState.Clients`. Voice bindings are dropped through a `VoiceSessionDropper` interface injected from `app` (local `voice.Server`) and `voiceontrol` (`kick_client` to remote nodes). `app` Kick/Ban go through `control.Server.RemoveClient`.

**Tech Stack:** Go 1.27.3, `-tags headless`, grpc-go (`codes`, `status`, `metadata`, `test/bufconn`), `log/slog`, `github.com/google/uuid`. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-10-27-phase-6-9-sv1-session-liveness-design.md` (this repo). Umbrella: client repo `docs/superpowers/specs/2026-10-27-vcs-client-phase-6-9-cleanup-umbrella-design.md` §3 SV1, §8, §9.

## Global Constraints

- **Code generation before anything builds** (gitignored `srspb/`, `voicecontrolpb/`, `vcsauthpb/`, `graphql/generated/`), exactly as `.github/actions/generate/action.yml`, with `protoc` 36.2 and `protoc-gen-go-grpc@v1.6.2` on PATH:
  ```bash
  go install google.golang.org/protobuf/cmd/protoc-gen-go@$(go list -m -f '{{.Version}}' google.golang.org/protobuf)
  go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2
  mkdir -p srspb voicecontrolpb vcsauthpb
  protoc --go_out=./srspb --go_opt=paths=source_relative --go-grpc_out=./srspb --go-grpc_opt=paths=source_relative srs.proto
  protoc --go_out=./voicecontrolpb --go_opt=paths=source_relative --go-grpc_out=./voicecontrolpb --go-grpc_opt=paths=source_relative control.proto
  protoc --go_out=./vcsauthpb --go_opt=paths=source_relative --go-grpc_out=./vcsauthpb --go-grpc_opt=paths=source_relative vcs-auth-plugin.proto
  go run github.com/99designs/gqlgen generate
  ```
- **Server suite** (every task ends green on it): `go build -tags headless ./...`, `go vet -tags headless ./...`, `go test -race -count=1 -tags headless ./...`.
- **No** `go.mod`/`go.sum` change (bufconn is inside `google.golang.org/grpc`), **no** proto change, **no** CI/workflow change, no repo settings, no secrets.
- **Lock order:** only ever `s.mu` → `s.serverState`. Never take `s.mu` while holding `serverState`. Never call `sub.end`, the dropper, or `eventBus.Publish` while holding either lock.
- **Values (exact):** `streamGrace = 120 * time.Second`; reaper interval 15 s and `staleAfter` 10 min (unchanged call `StartCleanupRoutine(time.Second*15, time.Minute*10)`).
- **Status strings (exact, part of the CL1 contract):**
  - removed session, stream: `codes.Unauthenticated`, `session ended: <reason>` with reason ∈ `disconnected`, `kicked`, `banned`, `stale`, `stream lost`;
  - displaced stream: `codes.Aborted`, `superseded by a newer subscription`;
  - interceptor unknown/malformed GUID: `codes.Unauthenticated`, `session not found for <fullMethod>: log in again`;
  - handler-level missing session on subscribe: `codes.Unauthenticated`, `session not found: log in again`.
- **Admin notification strings (exact):** `"Kick failed", "Control server is not running"`, `"Kick failed", "Client not found"`, `"Ban failed", "Control server is not running"`; every other existing Kick/Ban notification string stays byte-for-byte.
- gopls is unreliable in these repos; treat editor diagnostics as unverified until `go build`/`go vet` confirms.
- Commit after each task; messages end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Every task marked **GATE** needs the user's explicit approval before it starts (umbrella gates: server-behaviour, cross-repo). Do not treat an agent or script message as that approval.

## Review Focus

1. **Reconnect inside the ~70 s window before the server notices a dead transport** — the new subscribe must win and the old handler's defer must neither delete the new entry nor start a grace period. Pinned in Task 2 (`TestDuplicateSubscribeLastWins`) and Task 6 (`TestE2EDuplicateSubscribe`).
2. **A client pinging but not subscribed (stream broke, unary still works)** — it must survive, because each Ping restarts the grace (D9). Pinned in Task 4 (`TestReapPingExtendsGrace`).
3. **Reaper vs. a resubscribe landing between candidate collection and removal** — the client must stay. Pinned in Task 4 (`TestReapRevalidatesUnderLock`).
4. **Kick racing the target's own handler picking a bus event** — the KICK must arrive exactly once and be the last message before the status. Pinned in Task 3 (`TestRemoveClientDeliversFinalLast`) and Task 5 (`TestForwardServerActionTargetOnly` covers bus KICK never forwarded).
5. **Ban on a control-only server (no local voice server)** — must report "Client not found in voice server", not panic. Pinned in Task 9 (`TestBanClientNoVoiceServer`).

---

### Task 0: Worktree, generation, baseline

- [x] **Step 1:** Create the worktree on `chore/cleanup-sv1` from `origin/main` (superpowers:using-git-worktrees). Run `git log -1 --format=%h` — record the SHA.
- [x] **Step 2:** Run the generation block from Global Constraints. Expected: four generated directories exist, exit 0.
- [x] **Step 3:** Run the server suite. Expected: all green (10 packages `ok`). If anything is red, stop and report the verbatim output; do not start Task 1.
- [x] **Step 4:** Build the client baseline for Task 10: `go build -tags headless -o "$TMP/vcs-server-main.exe" .` (record the path). No commit.

---

### Task 1: SRV-01 — injectable clock and `LastUpdate` refresh

**GATE — needs user approval before this task** (server-behaviour: roster `LastUpdate` values advance with every Ping; prerequisite of "live clients are no longer evicted").

**Files:**
- Modify: `srs/srs_service.go` (struct `:26-38`, `NewSimpleRadioServer :48-61`, `SyncClient :98-174`, `Ping :180-190`, `UpdateClientInfo :294`, `UpdateRadioInfo :346-353`)
- Test: `srs/srs_service_test.go`

**Interfaces:**
- Produces: field `now func() time.Time` on `SimpleRadioServer` (default `time.Now`); test helper `withSRSClock(s *SimpleRadioServer) *fakeClock` (reuses `fakeClock` from `srs/auth_service_test.go`); `newTestServer()` updated to set `now: time.Now`, `eventBus: events.NewEventBus()`, `serverState` with non-nil `Clients`/`RadioClients`.

- [x] **Step 1: Write the failing test** `TestRPCsRefreshLastUpdate` — table over `Ping`, `SyncClient`, `UpdateRadioInfo`, `UpdateClientInfo`: `AddClient(id)`; set `Clients[id].LastUpdate = clock.Now().Add(-time.Hour)`; `clock.Advance(time.Minute)`; call the RPC with `context.WithValue(ctx, utils.ClientIDKey, id.String())`; assert `Clients[id].LastUpdate.Equal(clock.Now())`.
- [x] **Step 2: Run** `go test -race -count=1 -tags headless ./srs/ -run TestRPCsRefreshLastUpdate -v` — Expected: FAIL for Ping, SyncClient, UpdateRadioInfo (UpdateClientInfo fails too: it uses `time.Now()`).
- [x] **Step 3: Implement.** Add `now`; in each RPC set `client.LastUpdate = s.now()` under `serverState.Lock` (SyncClient: switch the caller lookup at `:143-150` to `Lock`; UpdateRadioInfo: inside the existing write section at `:346-353`). No event for a refresh.
- [x] **Step 4: Run** the test, then the server suite — Expected: PASS / green.
- [x] **Step 5: Commit** — `fix(srs): refresh LastUpdate on Ping, SyncClient and UpdateRadioInfo (SRV-01)`

---

### Task 2: CON-05 — subscriptions with identity, last subscriber wins

**GATE — needs user approval before this task** (wire-visible: a duplicate subscribe now displaces the old stream with `Aborted` instead of failing with `Unknown`; a subscribe for an unknown session returns `Unauthenticated`).

**Files:**
- Create: `srs/session.go`
- Modify: `srs/srs_service.go` (struct, constructor, `SubscribeToUpdates :373-449`; the existing reaper at `:491-523` is adapted minimally to the new map type here and replaced in Task 4)
- Test: create `srs/session_test.go`

**Interfaces:**
- Produces (in `srs/session.go`):
  ```go
  type subscription struct { done chan struct{}; once sync.Once; final *pb.ServerUpdate; err error }
  func newSubscription() *subscription
  func (sub *subscription) end(final *pb.ServerUpdate, err error) bool // first call wins
  func (sub *subscription) ended() bool                                 // non-blocking check of done
  var errSuperseded = status.Error(codes.Aborted, "superseded by a newer subscription")
  var errNoSession  = status.Error(codes.Unauthenticated, "session not found: log in again")
  ```
  Fields on `SimpleRadioServer`: `streams map[uuid.UUID]*subscription`, `detachedAt map[uuid.UUID]time.Time`.
- Test helpers (in `session_test.go`): `fakeUpdateStream` implementing `grpc.ServerStreamingServer[pb.ServerUpdate]` (ctx carrying `utils.ClientIDKey`, mutex-guarded `sent []*pb.ServerUpdate`, `sentCh chan *pb.ServerUpdate` buffered 64); `startSubscriber(t, s, id) (cancel func(), result <-chan error, stream *fakeUpdateStream)` which runs `SubscribeToUpdates` in a goroutine and waits until `s.streams[id]` is set.

- [x] **Step 1: Write the failing tests:**
  - `TestDuplicateSubscribeLastWins`: subscriber A, then B for the same id; A's result is an error with `status.Code == codes.Aborted` and message `superseded by a newer subscription`; `s.streams[id]` is B's subscription; `s.detachedAt` has no entry for id; publishing a `RadioClientsChanged` event reaches B's stream.
  - `TestSubscribeUnknownClient`: id not in `Clients` → returns `codes.Unauthenticated` immediately; `len(s.streams) == 0`.
  - `TestStreamEndStartsGrace`: subscriber A; cancel its ctx; result `nil`; `s.streams[id]` absent; `s.detachedAt[id] == clock.Now()`.
  - `TestResubscribeClearsGrace`: after the above, subscribe again → `s.detachedAt[id]` absent.
- [x] **Step 2: Run** `go test -race -count=1 -tags headless ./srs/ -run 'TestDuplicateSubscribe|TestSubscribeUnknown|TestStreamEndStartsGrace|TestResubscribeClearsGrace' -v` — Expected: FAIL (compile error on `subscription` first, then behaviour).
- [x] **Step 3: Implement `SubscribeToUpdates`** per spec §3.1: presence check → bus subscribe → under `s.mu` swap in the new sub, `delete(s.detachedAt, id)` → re-check presence (absent → unregister **by identity**, return `errNoSession`) → `old.end(nil, errSuperseded)` outside the lock → loop selecting `sub.done` (send `final` if non-nil, log a send error, return `sub.err`), `ctx.Done()` (return nil), bus event (if `sub.ended()` handle as done; otherwise forward exactly as today). Defer: under `s.mu`, only if `s.streams[id] == sub`: delete it and set `s.detachedAt[id] = s.now()`. Write the invariant as a comment on `subscription`: *the server unregisters a subscription before ending it; a handler that finds itself still registered was ended by the client.*
- [x] **Step 4: Run** the tests, then the server suite — Expected: PASS / green.
- [x] **Step 5: Commit** — `fix(srs): last subscriber wins on duplicate SubscribeToUpdates (CON-05)`

---

### Task 3: SRV-02 — the removal helper and Disconnect

**GATE — needs user approval before this task** (wire-visible: Disconnect ends the caller's stream with `Unauthenticated` "session ended: disconnected" and drops its voice binding).

**Files:**
- Modify: `srs/session.go`, `srs/srs_service.go` (`NewSimpleRadioServer` signature, `Disconnect :192-232`, delete `cleanupClientState :476-489` and repoint the old reaper's call to `RemoveClient(id, ReasonStale, nil)`), `control/server.go:169` (pass `nil` dropper for now)
- Test: `srs/session_test.go`

**Interfaces:**
- Consumes: Task 2's `subscription`, `streams`, `detachedAt`.
- Produces:
  ```go
  type RemovalReason string // ReasonDisconnect "disconnected", ReasonKicked "kicked", ReasonBanned "banned", ReasonStale "stale", ReasonStreamLost "stream lost"
  type VoiceSessionDropper interface{ DropVoiceSession(clientID uuid.UUID, reason string) }
  type VoiceSessionDroppers []VoiceSessionDropper // DropVoiceSession fans out, skips nil entries
  func SessionEndedError(reason RemovalReason) error
  func (s *SimpleRadioServer) RemoveClient(id uuid.UUID, reason RemovalReason, final *pb.ServerUpdate) bool
  func (s *SimpleRadioServer) removeClientIf(id uuid.UUID, reason RemovalReason, final *pb.ServerUpdate, cond func() bool) bool
  func NewSimpleRadioServer(serverState *state.ServerState, settingsState *state.SettingsState, logger *slog.Logger, bus *events.EventBus, voiceRegistry VoiceAddressProvider, dropper VoiceSessionDropper) *SimpleRadioServer
  ```
  Field `dropper VoiceSessionDropper` (nil-safe). Test helpers: `fakeDropper` (mutex-guarded `calls []dropCall{id uuid.UUID; reason string}`); `countClientLeft(t, bus, id, wait time.Duration) int` (subscribes to `events.ClientsChanged` **before** the action, counts `ClientLeft` events for id until `wait` elapses).

- [x] **Step 1: Write the failing tests:**
  - `TestRemoveClientEndsEverything`: subscribed client; `RemoveClient(id, ReasonStale, nil)` returns true; `Clients[id]` and `RadioClients[id]` absent; handler result has code `Unauthenticated`, message `session ended: stale`; dropper called once with `(id, "stale")`; `countClientLeft == 1`; `s.streams`/`s.detachedAt` have no id.
  - `TestRemoveClientDeliversFinalLast`: subscribed client; `final := ServerActionUpdate(events.ActionKick, id, "bye")` (Task 5 adds it — until then build the `pb.ServerUpdate` inline in the test); `RemoveClient(id, ReasonKicked, final)`; the last element of `stream.sent` is `final`; result message `session ended: kicked`.
  - `TestRemoveClientConcurrent`: 8 goroutines call `RemoveClient` → exactly one `true`; `countClientLeft == 1`.
  - `TestRemoveUnknownClient`: returns false, no `ClientLeft`, dropper still called (idempotent drop).
  - `TestDisconnectRemoves`: subscribed client; `Disconnect` → `Success:true`; stream ends with `session ended: disconnected`; dropper `(id, "disconnected")`; one `ClientLeft`.
- [x] **Step 2: Run** `go test -race -count=1 -tags headless ./srs/ -run 'TestRemove|TestDisconnectRemoves' -v` — Expected: FAIL (undefined symbols).
- [x] **Step 3: Implement `removeClientIf`** per spec §3.3: one critical section (`s.mu` then `serverState`), optional `cond` under both locks, test-and-delete both maps, `ClientLeft` snapshot with value copies taken while `serverState` is held, take+delete `streams[id]` and `detachedAt[id]`; after unlocking: `sub.end(final, SessionEndedError(reason))`, `dropper.DropVoiceSession`, publish once if it existed. `RemoveClient = removeClientIf(..., nil)`. `Disconnect` → `RemoveClient(id, ReasonDisconnect, nil)`; `false` → the existing `Success:false` body. Update the `control/server.go:169` call with a trailing `nil`.
- [x] **Step 4: Run** the tests, then the server suite — Expected: PASS / green.
- [x] **Step 5: Commit** — `fix(srs): one removal helper for every session end (SRV-02)`

---

### Task 4: SRV-01/SRV-02 — the reaper: 120 s grace, 10 min stale, re-validation

**GATE — needs user approval before this task** (server-behaviour: live clients are no longer evicted at 10 min; stream-less sessions are removed after 120 s with `CLIENT_LEFT`).

**Files:**
- Modify: `srs/session.go`, `srs/srs_service.go` (`StartCleanupRoutine :491-523` body)
- Test: `srs/session_test.go`

**Interfaces:**
- Consumes: Task 3's `removeClientIf`.
- Produces: `const streamGrace = 120 * time.Second`; field `staleAfter time.Duration` (set by `StartCleanupRoutine`, and to `10*time.Minute` in `newTestServer`); `func (s *SimpleRadioServer) reapReasonLocked(id uuid.UUID, now time.Time) (RemovalReason, bool)`; `func (s *SimpleRadioServer) reapOnce(now time.Time) []uuid.UUID`.

- [x] **Step 1: Write the failing tests** (fake clock; `LastUpdate` set explicitly):
  - `TestReapPingingClientSurvives`: subscribed; every 5 s of fake time call `Ping`, up to +11 min, calling `reapOnce(clock.Now())` each minute → never removed.
  - `TestReapStaleLiveStream`: subscribed, no RPC; `reapOnce` at +10 min +1 s → removed with `session ended: stale`, one `ClientLeft`.
  - `TestReapStreamLostAfterGrace`: subscribe then cancel at T; `reapOnce(T+119s)` → present; `reapOnce(T+121s)` → removed, dropper reason `"stream lost"`.
  - `TestReapResubscribeKeepsSession`: cancel at T, resubscribe at T+60 s; `reapOnce(T+300s)` → present.
  - `TestReapPingExtendsGrace`: cancel at T; Ping at T+100 s; `reapOnce(T+200s)` → present; `reapOnce(T+221s)` → removed.
  - `TestReapNeverSubscribed`: `LastUpdate = T`, no stream; `reapOnce(T+121s)` → removed.
  - `TestReapRevalidatesUnderLock`: cancel at T; at T+121 s compute `reason, ok := reapReasonLocked` (with locks) → `ReasonStreamLost, true`; then resubscribe; then call `removeClientIf(id, reason, nil, cond)` with the reaper's cond → returns false; client present, new stream live.
- [x] **Step 2: Run** `go test -race -count=1 -tags headless ./srs/ -run TestReap -v` — Expected: FAIL.
- [x] **Step 3: Implement** per spec §3.5. `reapOnce` collects `(id, reason)` under `s.mu` + `serverState.RLock`, then for each calls `removeClientIf` with `cond = func() bool { r, ok := s.reapReasonLocked(id, now); return ok && r == reason }`, logs `"Removed client session"` with `client_id` and `reason`, returns the removed ids. The ticker calls `reapOnce(s.now())`.
- [x] **Step 4: Run** the tests, then the server suite — Expected: PASS / green.
- [x] **Step 5: Commit** — `fix(srs): reap stream-less sessions after a 120s grace, never live ones (SRV-01, SRV-02)`

---

### Task 5: `SERVER_ACTION` target-only

**GATE — needs user approval before this task** (wire-visible to every client incl. the C# peer: other clients stop receiving KICK/BAN/MUTE/UNMUTE for someone else).

**Files:**
- Modify: `srs/srs_service.go` (`buildServerUpdate` `ServerAction` case `:620-647`, stream loop), `srs/session.go`
- Test: `srs/session_test.go`, `srs/srs_service_test.go` (existing `TestBuildServerUpdate_ServerAction_Kick` must stay green)

**Interfaces:**
- Produces: `func ServerActionUpdate(action events.ActionType, target uuid.UUID, reason string) *pb.ServerUpdate` (nil for an unknown action); `func forwardServerAction(event events.Event, clientID uuid.UUID) bool`.

- [x] **Step 1: Write the failing tests:**
  - `TestForwardServerActionTargetOnly` (table, pure): MUTE target X → true for X, false for Y; UNMUTE same; KICK target X → false for X and Y; BAN same; non-ServerAction event → true.
  - `TestMuteReachesOnlyTarget`: subscribers X and Y; publish `ServerAction{ActionMute, X}`; X's stream receives a `SERVER_ACTION` within 1 s; Y's receives none within 200 ms.
  - Switch `TestRemoveClientDeliversFinalLast` to build `final` with `ServerActionUpdate`.
- [x] **Step 2: Run** `go test -race -count=1 -tags headless ./srs/ -run 'TestForwardServerAction|TestMuteReachesOnlyTarget|TestRemoveClientDeliversFinalLast' -v` — Expected: FAIL.
- [x] **Step 3: Implement** — `buildServerUpdate` delegates its `ServerAction` case to `ServerActionUpdate`; the stream loop skips an event when `!forwardServerAction(event, clientID)`.
- [x] **Step 4: Run** the tests, then the server suite — Expected: PASS / green.
- [x] **Step 5: Commit** — `fix(srs): deliver SERVER_ACTION only on the target's own stream`

---

### Task 6: SRV-06 — unknown GUID in `authenticate`; control-side `RemoveClient`; bufconn end-to-end

**GATE — needs user approval before this task** (wire-visible: a valid token whose session is gone gets `Unauthenticated` on every non-auth RPC, including after a server restart; today `Success:true` with an empty voice secret).

**Files:**
- Modify: `control/server.go` (`Server` struct `:40-56`, `NewServer :59-67`, `Start :169`, `authenticate :453-498`), `srs/srs_service.go` (`voiceSecretFor` comment `:69-78`), `app/server_control.go:262` (pass `nil` for now; Task 9 wires it)
- Test: `control/server_test.go` (`newInterceptorTestServer :219-229`, `newStartTestServer :387`, `TestAuthInterceptorCodes :271-333`), create `control/session_e2e_test.go`

**Interfaces:**
- Consumes: Task 3's `srs.RemovalReason`, `srs.VoiceSessionDropper`, `srs.VoiceSessionDroppers`; Task 5's `srs.ServerActionUpdate`.
- Produces:
  ```go
  func NewServer(serverState *state.ServerState, settingsState *state.SettingsState, logger *slog.Logger, distributionState *state.DistributionState, eventBus *events.EventBus, localVoice srs.VoiceSessionDropper) *Server
  func (s *Server) RemoveClient(id uuid.UUID, reason srs.RemovalReason, final *srspb.ServerUpdate) (existed, running bool)
  ```
  `Start` passes `srs.VoiceSessionDroppers{s.localVoice, controlServer}` as the new last argument of `srs.NewSimpleRadioServer` (Task 8 makes `*voiceontrol.VoiceControlServer` satisfy the interface; until then pass `srs.VoiceSessionDroppers{s.localVoice}`).
  Test helper `newE2E(t) (*Server, *srs.SimpleRadioServer, srspb.SRSServiceClient, func(guid string) context.Context)`: bufconn listener (`bufconn.Listen(1 << 20)`), `grpc.NewServer` with `s.authInterceptor` / `s.authStreamInterceptor`, `srs.NewSimpleRadioServer(..., nil)` registered and assigned to `s.srsServer` with `s.isRunning = true` under `s.mu`; client via `grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(...), grpc.WithTransportCredentials(insecure.NewCredentials()))`; `t.Cleanup` stops everything. Sessions are created with `serverState.AddClient(uuid.MustParse(guid), &state.ClientState{Name: "t"})` + `mintToken`.

- [x] **Step 1: Write the failing tests:**
  - Extend `TestAuthInterceptorCodes`: the `valid token` case first `AddClient(validGUID)` (it must stay green); new reject rows `unknown GUID` (valid token, GUID not added) and `malformed GUID claim` (token minted with GUID `"not-a-uuid"` via `utils.GenerateToken`) → `codes.Unauthenticated`, message contains `session not found for`, handler not called. AuthService row unchanged.
  - `TestE2EKickEndsStreamAndToken` (`session_e2e_test.go`): subscribe; `s.RemoveClient(id, srs.ReasonKicked, srs.ServerActionUpdate(events.ActionKick, id, "r"))` → `(true, true)`; client `Recv` returns the KICK update, next `Recv` error has code `Unauthenticated`, message `session ended: kicked`; then `Ping` → `codes.Unauthenticated`.
  - `TestE2EDuplicateSubscribe`: subscribe A, then B with the same token; A's next `Recv` → `codes.Aborted`; after `UpdateRadioInfo`, B receives a `CLIENT_RADIO_UPDATE`; `SyncClient` returns the same `VoiceSecret` as before.
  - `TestRemoveClientNotRunning`: `NewServer(...)` without start → `RemoveClient` returns `(false, false)`.
- [x] **Step 2: Run** `go test -race -count=1 -tags headless ./control/ -run 'TestAuthInterceptorCodes|TestE2E|TestRemoveClientNotRunning' -v` — Expected: FAIL.
- [x] **Step 3: Implement** per spec §3.6/§3.7: the unknown-GUID branch after the `claims == nil` check (parse failure or absent under `serverState.RLock` → `status.Errorf(codes.Unauthenticated, "session not found for %s: log in again", fullMethod)`); `localVoice` field; `RemoveClient` under `s.mu.RLock`; rewrite the `voiceSecretFor` comment to cite this check; update the three `NewServer` call sites with a trailing `nil`.
- [x] **Step 4: Run** the tests, then the server suite — Expected: PASS / green.
- [x] **Step 5: Commit** — `fix(control): reject tokens whose session is gone with Unauthenticated (SRV-06)`

---

### Task 7: Voice node — `kick_client` and `LEFT` drop the binding

**GATE — needs user approval before this task** (server-to-server behaviour: voice nodes act on `kick_client` and drop bindings on `ClientDelta_LEFT`).

**Files:**
- Modify: `voiceontrol/client.go` (struct `:24-38`, `applyControlMessage :242-255`), `voice/server.go` (`Listen :133-150`)
- Test: `voiceontrol/client_test.go`, `voice/server_test.go`

**Interfaces:**
- Produces: `func (v *VoiceControlClient) SetKickHandler(h func(uuid.UUID))` (mutex-guarded field `kickHandler`); unexported `kick(id uuid.UUID)` that calls it if set.

- [x] **Step 1: Write the failing tests:**
  - `TestApplyKickClientCallsHandler`: handler records ids; `applyControlMessage(&pb.ControlMessage{Command: &pb.ControlMessage_KickClient{KickClient: &pb.KickClientRequest{ClientId: id.String(), Reason: "kicked"}}})` → handler got `[id]`.
  - `TestApplyKickClientInvalidGUID`: `ClientId: "nope"` → handler not called.
  - `TestApplyDeltaLeftCallsHandler`: `ClientDelta{Type: LEFT, ClientId: id}` → state maps cleared **and** handler got `[id]`; handler runs after the state lock is released (handler calls `ss.RLock(); ss.RUnlock()` and must not deadlock — run with a 2 s timeout).
  - `TestApplyKickWithoutHandler`: no handler set → no panic.
  - `voice/server_test.go` `TestKickHandlerDropsBinding`: `newBoundTestServer` with a bound client; `v.DisconnectClient` passed as the handler and invoked → `GetClientIPFromId(id)` returns `false`.
- [x] **Step 2: Run** `go test -race -count=1 -tags headless ./voiceontrol/ ./voice/ -run 'Kick|DeltaLeftCallsHandler' -v` — Expected: FAIL.
- [x] **Step 3: Implement** per spec §3.8; in `Listen`, `v.controlClient.SetKickHandler(v.DisconnectClient)` right after `NewVoiceControlClient`, before `ConnectControlServer`.
- [x] **Step 4: Run** the tests, then the server suite — Expected: PASS / green.
- [x] **Step 5: Commit** — `feat(voiceontrol): voice nodes drop a binding on kick_client and LEFT`

---

### Task 8: Control — `DropVoiceSession` sends `kick_client` to every node

**GATE — needs user approval before this task** (server-to-server: control now sends `kick_client` on every session removal).

**Files:**
- Modify: `voiceontrol/service.go`, `control/server.go:169` (pass `srs.VoiceSessionDroppers{s.localVoice, controlServer}`)
- Test: `voiceontrol/service_test.go`

**Interfaces:**
- Produces: `func (s *VoiceControlServer) DropVoiceSession(clientID uuid.UUID, reason string)` (so `*VoiceControlServer` satisfies `srs.VoiceSessionDropper`; add `var _ srs.VoiceSessionDropper = (*voiceontrol.VoiceControlServer)(nil)` in `control/server.go` — not in `voiceontrol`, to avoid an import of `srs` there).
- Test helper: `fakeNodeStream` implementing `pb.VoiceControlService_EstablishStreamServer` with a mutex-guarded `sent []*pb.ControlMessage`.

- [x] **Step 1: Write the failing tests:**
  - `TestDropVoiceSessionKicksEveryNode`: register nodes `n1` (coalition) and `g` (global) via `RegisterVoiceServer`, set each `node.stream` to a `fakeNodeStream`, plus `n3` with no stream; `DropVoiceSession(id, "kicked")` → `n1` and `g` each got exactly one message whose `KickClient.ClientId == id.String()`, `Reason == "kicked"`, `ServerId` equal to the node's id, non-empty `EventId`; `n3` nothing, no panic.
  - `TestDropVoiceSessionNoNodes`: no nodes → returns, no panic.
- [x] **Step 2: Run** `go test -race -count=1 -tags headless ./voiceontrol/ -run TestDropVoiceSession -v` — Expected: FAIL.
- [x] **Step 3: Implement** per spec §3.8 (copy `(node, stream)` pairs under `s.mu.RLock`, send outside `s.mu` under `node.sendMu`, log a send error with `node` and `error`). Wire it into `control.Start`.
- [x] **Step 4: Run** the tests, then the server suite — Expected: PASS / green.
- [x] **Step 5: Commit** — `feat(voiceontrol): send kick_client to every voice node on session removal (SRV-10)`

---

### Task 9: SRV-10 — Kick and Ban through the helper; local voice drop; voice accessor

**GATE — needs user approval before this task** (user-visible: Kick/Ban actually disconnect the target; kicking an unknown GUID reports failure; Kick/Ban with the control server stopped fail — **this last point assumes the answer to Q5 is A; if the user picks B, stop and re-plan this step**).

**Files:**
- Modify: `app/app.go` (struct `:18-31`, `GetClientLatencyMap :213-218`), `app/server_control.go` (`startVoiceServer :172-180`, `stopVoiceServer :220`, `startGrpcServer :262-263`), `app/clients.go` (`BanClient :48-116`, `KickClient :158-187`)
- Test: create `app/clients_test.go`

**Interfaces:**
- Consumes: `control.Server.RemoveClient` (Task 6), `srs.ReasonKicked`, `srs.ReasonBanned`, `srs.ServerActionUpdate`.
- Produces:
  ```go
  type sessionRemover interface {
      RemoveClient(id uuid.UUID, reason srs.RemovalReason, final *srspb.ServerUpdate) (existed, running bool)
  }
  // VCSApplication fields: voiceMu sync.RWMutex; remover sessionRemover;
  // voiceIPLookup func(uuid.UUID) (net.IP, bool) — test seam; nil means
  // currentVoiceServer().GetClientIPFromId, and (nil, false) when there is no voice server
  func (a *VCSApplication) currentVoiceServer() *voice.Server
  func (a *VCSApplication) setVoiceServer(v *voice.Server)
  type localVoiceDropper struct{ a *VCSApplication } // DropVoiceSession(id, reason) → currentVoiceServer().DisconnectClient(id) if non-nil
  ```
  `startGrpcServer`: `control.NewServer(..., localVoiceDropper{a})`, then `a.remover = controlServer`.
- Test helpers: `fakeRemover` recording `(id, reason, final)` and returning configured `(existed, running)`; `newTestApp()` gains `eventBus: events.NewEventBus()` and a `Logger`; `collectEvents(t, a, name)` subscribes to the bus.

- [x] **Step 1: Write the failing tests:**
  - `TestKickClientRemovesThroughHelper`: client in state; fake remover `(true, true)` → remover got `(id, srs.ReasonKicked, final)` with `final.GetServerAction().GetType() == srspb.ServerAction_KICK`, `TargetClientGuid == id.String()`, reason passed through; a `ServerAction` bus event emitted; **no** `ClientsChanged` emitted by the app; notification `"Kick succeeded"`.
  - `TestKickClientUnknown`: remover `(false, true)` → notification `"Kick failed"` / `"Client not found"`; no `ServerAction` event.
  - `TestKickClientControlNotRunning`: `remover == nil`, and separately remover `(false, false)` → `"Kick failed"` / `"Control server is not running"`; state untouched.
  - `TestBanClientNoVoiceServer`: remover `(true, true)`, no voice server → `"Ban failed"` / `"Client not found in voice server"`, no panic, ban list unchanged.
  - `TestBanClientRemovesThroughHelper`: set `a.voiceIPLookup` to return `(net.ParseIP("203.0.113.7"), true)`; `BannedState` with a temp file path so `Save()` succeeds → ban recorded with IP `203.0.113.7`; remover got `(id, srs.ReasonBanned, final)` with `final.GetServerAction().GetType() == srspb.ServerAction_BAN`; `BannedClientsChanged` and `ServerAction` emitted. Also assert the lookup ran while `ServerState` was **not** locked (the fake calls `a.ServerState.TryLock()`, which must succeed, then unlocks).
  - `TestLocalVoiceDropperNilSafe`: `localVoiceDropper{a}.DropVoiceSession(id, "x")` with no voice server → no panic.
- [x] **Step 2: Run** `go test -race -count=1 -tags headless ./app/ -run 'TestKick|TestBan|TestLocalVoiceDropper' -v` — Expected: FAIL.
- [x] **Step 3: Implement** per spec §3.7: every `a.voiceServer` read/write goes through `currentVoiceServer`/`setVoiceServer`; Ban reads the IP **before** `ServerState.Lock`; Kick and Ban no longer touch `Clients` or emit `ClientsChanged` (the helper does).
- [x] **Step 4: Run** the tests, then the server suite — Expected: PASS / green.
- [x] **Step 5: Commit** — `fix(app): Kick and Ban end the session through the removal helper (SRV-10)`

---

### Task 10: Cross-repo check — client suite against the SV1 server

**GATE — needs user approval before this task** (cross-repo: runs the VCS client's real-server integration tests against the SV1 build; read-only on the client repo).

No code change. Client repo `D:\Projects\vcs-srs-client` at `origin/main` (`5a888e2` or later), used read-only.

- [x] **Step 1:** Build the SV1 server: `go build -tags headless -o "$TMP/vcs-server-sv1.exe" .` (worktree root).
- [x] **Step 2:** In the client repo with `VCS_SERVER_BIN="$TMP/vcs-server-sv1.exe"`:
  `go build -tags purego ./...`, `go vet -tags purego ./...`, `go test -race -count=1 -tags purego ./...`
  Expected: all green, and `internal/voice` integration tests **ran** (not skipped: `go test -run Integration -v ./internal/voice/` shows `--- PASS`, no `SKIP`).
- [x] **Step 3:** In `frontend/`: `npx vitest run`, `npx tsc --noEmit`, `npm run build` — Expected: green (sanity only; no frontend change, so a failure here is pre-existing and must be reproduced against Task 0's `vcs-server-main.exe`/`main` before being reported).
- [x] **Step 4:** If any Go test is red, rerun it with `VCS_SERVER_BIN` = the Task 0 baseline binary. Red on SV1 only → an SV1 regression or a CL1 dependency: stop and report verbatim, with which §11 contract row it touches. No commit.

---

### Task 11: Docs, PR, manual-verification list

**GATE — needs user approval before this task** (opens a PR; the PR body carries the CL1 contract and references the C# note).

**Files:**
- Create: `docs/superpowers/specs/2026-10-27-phase-6-9-sv1-session-liveness-design.md` (copy of the approved `design-SV1.md`, status line changed to `approved` only if the user approved it)
- Create: `docs/superpowers/plans/2026-10-27-phase-6-9-sv1-session-liveness.md` (this plan, checkboxes as executed)

- [x] **Step 1:** Final server suite (generation + build + vet + test -race) — Expected: green; paste the per-package `ok` lines into the PR body verbatim.
- [x] **Step 2:** Commit the two docs — `docs(phase-6.9): SV1 session liveness design and plan`.
- [x] **Step 3:** Run superpowers:requesting-code-review on the branch; fix findings in new commits. *(Executed as a self-review: no reviewer subagent was available; no Critical/Important findings.)*
- [ ] **Step 4:** *(Not done by the executor: the PR is opened separately.)* Push and open the PR against `main`, title `Phase 6.9 SV1: server session liveness & reaping`. Body: summary; findings closed (SRV-01, SRV-02, SRV-06 server half, SRV-10, CON-05, SRV-20 share); user decisions D5-D8 and Q5; §7 behaviour changes; the full §11 SV1→CL1 contract table; "Note for the C# team: see `note-csharp-serveraction.md` (handed to the maintainers separately; no issue filed in VNGD-SimpleRadioStandalone)"; cross-repo result from Task 10; the manual-verification list below; ending with `🤖 Generated with [Claude Code](https://claude.com/claude-code)`.
- [ ] **Step 5:** Report the PR URL and the CI result to the orchestrator. Do not merge.

---

## Manual verification (append to the client repo's `docs/superpowers/plans/2026-10-27-phase-6-9-manual-verification.md` via DOC)

Nothing below can be run in the environment that implements this plan.

1. **Long session:** a VCS client stays connected and routable (voice heard both ways) against a real server for > 15 min; the admin roster shows its `LastUpdate` advancing.
2. **Blip inside grace:** pull the network for ~30 s, restore, click Reconnect → same session (no re-login), voice works, other clients saw no `CLIENT_LEFT`.
3. **Blip inside the ~70 s window:** pull for ~20 s, restore, Reconnect immediately → succeeds at once (last-subscriber-wins), no stuck "disconnected".
4. **Blip past grace:** pull for > 150 s → other clients see the client leave within ~135 s of loss; Reconnect gets `Unauthenticated` (until CL1: raw error text; after CL1: "session expired").
5. **Kick:** admin kicks client A → A's stream ends, A's voice stops reaching others and A hears nothing, B sees A leave, B's UI is **not** affected; A can log in again.
6. **Ban:** as kick, and A's re-login is refused.
7. **Mute:** admin mutes A → only A gets SERVER_ACTION; B sees A's mute via the roster.
8. **Server restart:** clients clicking Reconnect get `Unauthenticated` and must log in again.
9. **Distributed mode:** kick a client bound on a coalition node **and** the global node → both nodes log "Disconnected voice client"; an older voice node build ignores `kick_client` without errors.
10. **C# peer** (blocked on PR #253): kick/mute of another client no longer drops/mutes every C# client; behaviour of a kicked C# client on `Unauthenticated` (expected: backoff retry loop, R1).

---

## Execution record

Executed inline (superpowers:executing-plans) on 2026-10-27 from `origin/main` `2a65c4d` (`553b6dd` + #228, CI timeouts only). Each task's tests were watched failing before the implementation. Deviations, recorded as rulings:

- Task 0: local `protoc` is 36.0 (no Windows 36.2 build at hand); generated code is gitignored. gqlgen's line-ending-only rewrites of `go.mod` and `graphql/schema.resolvers.go` were restored.
- Task 2: `TestSubscribeUnknownClient` runs the handler in a goroutine with a 2 s bound (the pre-SV1 handler blocks forever there).
- Task 3: `countClientLeft` became `watchClientLeft(t, bus, id) func(wait) int`, so it can subscribe before the action and count after it.
- Task 6: the bufconn tests prove a stream is live by an `UpdateRadioInfo` round trip, since `control` cannot see `srs` internals.
- Task 7: `TestKickHandlerDropsBinding` is a pin rather than a RED test; the `Listen` wiring needs a live control node (manual item 9).
- Task 9: `sessionRemover` has `IsRunning()`; `TestBanClientControlNotRunning` added; a failed ban `Save()` keeps its pre-existing behaviour.
- Task 10: one environmental failure in a verbose rerun (`TestIntegrationReHelloAfterSourceAddressChange`: the client fixture picked UDP 60004, inside a Windows excluded port range); two reruns 7/7 PASS, and the full `-race` client suite was green.
