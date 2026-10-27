**Status: approved** (pass 2 of 2; open questions Q1-Q5 resolved by user decision 2026-10-27; implemented on branch `chore/cleanup-sv1`)

# Phase 6.9 / SV1: Server session liveness & reaping

**Repo:** `vcs-srs-server`. Branch `chore/cleanup-sv1`, cut from `origin/main` (`553b6dd` at design time; contains SV3 #226, SV5 #225, #227).
**Umbrella:** client repo `docs/superpowers/specs/2026-10-27-vcs-client-phase-6-9-cleanup-umbrella-design.md`, §3 SV1, §5, §6, §8, §9, §10.
**Builds on SV3 (merged):** `(*control.Server).authenticate` / `authError` (`control/server.go:453-515`, the comment at `:452` reserves the SV1 hook), the interceptor code table, `TestAuthInterceptorCodes` (`control/server_test.go:271`), the srs `fakeClock` test helper (`srs/auth_service_test.go:214-235`), SRV-03's sender+receiver check in `IsListeningOnFrequency` (`state/server.go:201-228`).

## Decisions

| # | Decision | Value | By |
|---|---|---|---|
| D1 | Stream-end grace | **120 s** | user (umbrella §9) |
| D2 | Token revocation | SRV-06 unknown-GUID check inside `authenticate`, `codes.Unauthenticated`. No denylist. A kicked client may log in again (new GUID). | orchestrator (umbrella §3) |
| D3 | Distributed-mode kick | Existing `kick_client` `ControlMessage` (`control.proto:25`, `KickClientRequest :181-186`). No proto change. | orchestrator (umbrella §3) |
| D4 | Voice handle | `srs` reaches voice through an interface (`VoiceSessionDropper`) injected from `app`; `srs` gets no `voice` import. | orchestrator (umbrella §3) |
| D5 | Duplicate `SubscribeToUpdates` (CON-05, Q1) | **Last subscriber wins.** The displaced stream ends with `codes.Aborted` "superseded by a newer subscription" and starts **no** grace period. Per-subscription identity, so the displaced handler's defer cannot delete the newer entry. | **user decision** (2026-10-27) |
| D6 | `SERVER_ACTION` delivery (Q2) | **Target-only.** KICK/BAN/MUTE/UNMUTE are delivered only on the target client's own stream. Others learn outcomes via `CLIENT_LEFT` / `CLIENT_RADIO_UPDATE.Muted`. A bug note for the C# team (`HandleServerAction` never checks `TargetClientGuid`) is produced as `note-csharp-serveraction.md` and referenced in the PR body; no issue is filed in another repo. | **user decision** (2026-10-27) |
| D7 | Ban through the removal helper (Q3) | **Yes.** Ban is recorded and the client's IP read **before** the binding is dropped; BAN is delivered on the target's stream; the stream ends; later RPCs get `Unauthenticated`; the existing ban check refuses re-login. | **user decision** (2026-10-27) |
| D8 | Status a server-ended stream returns (Q4) | A live stream whose session is removed (stale, > 120 s lost, kicked, banned, disconnected) ends with **`codes.Unauthenticated` "session ended: <reason>"**. | **user decision** (2026-10-27) |
| D9 | Grace clock | A stream-less client is removed when `now - max(detachedAt, LastUpdate) > 120 s`. `LastUpdate` is refreshed by Ping / SyncClient / UpdateRadioInfo / UpdateClientInfo, so a client actively reconnecting resets its own clock. A client that never subscribed is measured from `LastUpdate` (set by `AddClient`). | worker's call |
| D10 | Grace enforcement | Swept by the existing 15 s reaper (no per-client timers): removal 120-135 s after the later of stream end / last RPC. Clock injectable (`now func() time.Time` field), sweep callable as `reapOnce(now)`. | worker's call |
| D11 | Stale rule for clients **with** a live stream | Kept: `now - LastUpdate > 10 min` → remove. Pinging clients (VCS every 5 s, C# peer every 15 s) never hit it. | umbrella / worker's call |
| D12 | Unknown-GUID check scope | Every non-exempt method (SRSService, Health, reflection); AuthService and VoiceControlService stay exempt. | worker's call |
| D13 | Remote voice drop | `kick_client` sent to **every** registered voice node with a stream, for every removal reason (a client can be bound on its coalition node and the global node; `clientNodes` records only one). Fire-and-forget (`EstablishStream` never `Recv`s after identification). | worker's call |
| D14 | In-process RPC tests | `google.golang.org/grpc/test/bufconn` (inside the already-required `google.golang.org/grpc` module; no `go.mod` change). | worker's call |
| D15 | Removal atomicity | `removeClient` takes `s.mu` then `serverState.Lock` (the one lock order used everywhere) and does test-and-delete of both maps, the subscription and `detachedAt` in **one** critical section, with an optional precondition evaluated under both locks. This makes the reaper's re-validation race-free against a concurrent resubscribe or Ping. Nothing may take `s.mu` while holding `serverState`. | worker's call |
| D16 | KICK/BAN path to the target | KICK/BAN reach the target only as the subscription's `final` update, sent by the target's own handler just before it returns `Unauthenticated`. The stream loop never forwards KICK/BAN from the bus (so no duplicate and no race with the stream end); MUTE/UNMUTE are forwarded from the bus to the target only. The bus `ServerAction` event is still emitted for the admin GUI and for voice nodes (`buildClientDelta` maps MUTE/UNMUTE). | worker's call (implements D6) |
| D17 | Ban IP lookup | Read the voice IP **before** taking `ServerState.Lock`, via a guarded `currentVoiceServer()` accessor. Removes today's `ServerState → voice` vs `voice → ServerState` (`GetListeningClients` → `IsListeningOnFrequency`) lock-order inversion, and today's nil-pointer panic when banning on a control-only server (no local voice server). | worker's call |
| D18 | Voice node drops the binding on `ClientDelta_LEFT` too | Same kick handler, called after `applyDelta` releases the state lock (closes R4 below). | worker's call |
| D19 | Kick/Ban while the control server is not running (Q5) | **Fail cleanly:** "Kick failed" / "Ban failed" with "Control server is not running"; nothing is changed. | **user decision** (2026-10-27) |

## 0. Baseline

`origin/main` @ `553b6dd`, Go 1.27.3 (`go.mod` `toolchain go1.27.3`). Server suite at `9a0c74b` (main minus the CI-only #227) run 2026-10-27: generate, `go build`, `go vet`, `go test -race -count=1 -tags headless ./...` all green across 10 packages. Every line anchor in §2 was re-checked on `553b6dd`.

## 1. Intent

A live client is never evicted while it is alive; a dead client is always removed, and every removal looks the same to everyone: both state maps cleared, voice binding dropped (locally and on every voice node), update stream ended with a typed status, `CLIENT_LEFT` published exactly once, token dead. A network blip shorter than the grace period reconnects with the stored token; anything longer gets `Unauthenticated` and logs in again.

## 2. Re-verification (on `origin/main` `553b6dd`; gopls not used)

| ID | Status | Evidence |
|---|---|---|
| SRV-01 | **holds** | `srs/srs_service.go:59` `StartCleanupRoutine(15s, 10min)`; reaper `:491-523` evicts `!exists \|\| now.Sub(client.LastUpdate) > staleAfter`. `LastUpdate` writers: `state/server.go:137` (`AddClient`) and `srs/srs_service.go:294` (`UpdateClientInfo`) only. `Ping` `:180-190` writes only `LatencyToControlMs`. The VCS client never calls `UpdateClientInfo`. On eviction the reaper deletes `s.streams[id]` but never tells the handler goroutine, which keeps forwarding events for a client that no longer exists. |
| SRV-02 | **holds-with-correction** | `SubscribeToUpdates` defer `:390-394` only deletes `s.streams[clientID]`; reaper iterates `s.streams` only (`:506`), so a stream-less client is never reaped; reaper publishes nothing. **Correction:** "only Disconnect publishes ClientLeft" is wrong: `app/clients.go:105-108` (Ban) and `:177-180` (Kick) publish it too; neither the reaper nor stream death does. |
| SRV-10 | **holds** | `app/clients.go:158-187` `KickClient` deletes only `Clients` (`:167`), leaks `RadioClients`, has no existence check (an unknown GUID reports success and publishes `CLIENT_LEFT`), never ends the stream, never drops the voice binding (`voice.Server.DisconnectClient` exists, `voice/server.go:543-558`); the JWT keeps passing `authenticate`. `kick_client` is defined but never sent or handled (`voiceontrol/client.go:242-255` has no case). **Same class:** `BanClient` (`:48-116`) deletes only `Clients` (`:93`) and leaves stream and binding open (→ D7); it also calls `a.voiceServer.GetClientIPFromId` (`:73`) while holding `ServerState.Lock` and without a nil check (→ D17). |
| CON-05 | **holds** | `srs_service.go:384-387` returns `fmt.Errorf` (`codes.Unknown`). Server notices a silently dead transport after keepalive `Time 60s / Timeout 10s` (`control/server.go:146-149`) ≈ 70 s; the VCS client after 5 s × 3 pings ≈ 15 s, so every Reconnect in that window collides. |
| SRV-06 (server half) | **holds** | `authenticate` (`control/server.go:453-498`) never consults `serverState.Clients`. `SyncClient` for an unknown GUID returns `Success:true`, coalition `""`, voice secret `""` (`:143-174`); the `voiceSecretFor` comment (`:72-78`) claiming the interceptor guarantees a known client is false. `AddClient` precedes `GenerateToken` in both login paths, so a freshly issued token always names a present GUID. |
| SRV-20 (SV1 share) | **holds** | `srs/srs_service_test.go` covers `buildServerUpdate` and `voiceSecretFor` only; nothing for Ping, reaper, stream end, Disconnect, Kick, duplicate subscribe. No bufconn harness in the repo. |

**New finding (not in the scan), C# peer:** `VcsClientSyncHandler.cs` `HandleServerAction` acts on every `SERVER_ACTION` without comparing `TargetClientGuid` to itself; the server broadcasts all four actions (`srs_service.go:620-647` + the unfiltered stream loop). Resolved server-side by D6; note for the C# team in `note-csharp-serveraction.md`.

## 3. Design

### 3.1 Subscriptions (CON-05, D5)

`s.streams` becomes `map[uuid.UUID]*subscription`; new `s.detachedAt map[uuid.UUID]time.Time`, `s.now func() time.Time` (default `time.Now`), `s.staleAfter time.Duration`, `s.dropper VoiceSessionDropper`.

```go
type subscription struct {
    done  chan struct{}
    once  sync.Once
    final *pb.ServerUpdate // sent by the handler before it returns, may be nil
    err   error            // what the handler returns
}
// end records final/err and closes done. First call wins; returns whether this call ended it.
func (sub *subscription) end(final *pb.ServerUpdate, err error) bool
```

**Invariant:** the server unregisters a subscription from `s.streams` (under `s.mu`) *before* calling `end`. Therefore, when a handler's defer finds `s.streams[id] == sub`, the stream ended from the client side (context done or `Send` failed) and the defer deletes the entry and sets `detachedAt[id] = s.now()` (grace starts). A displaced or removed subscription is already unregistered, so its defer does nothing.

`SubscribeToUpdates`:
1. GUID from context; client absent from `Clients` → `Unauthenticated` "session not found: log in again".
2. Subscribe to the bus; under `s.mu`: `old := s.streams[id]`; register the new `sub`; `delete(s.detachedAt, id)`; unlock. Then re-check `Clients[id]` (closes the race with a removal between step 1 and registration); absent → unregister by identity, return the same `Unauthenticated`.
3. `old != nil` → `old.end(nil, status.Error(codes.Aborted, "superseded by a newer subscription"))`.
4. Loop: `<-sub.done` → send `sub.final` if non-nil (error logged only), return `sub.err`; `<-ctx.Done()` → return nil; bus event → if `sub.done` is already closed handle it as above, else filter (§3.2) and forward as today (including the `CoalitionReassigned` redirect).

### 3.2 `SERVER_ACTION` target-only (D6, D16)

`forwardServerAction(event, clientID) bool`: KICK/BAN → never forwarded from the bus; MUTE/UNMUTE → forwarded only when `TargetClientID == clientID`. `buildServerUpdate`'s `ServerAction` case is refactored onto an exported `ServerActionUpdate(action events.ActionType, target uuid.UUID, reason string) *pb.ServerUpdate` (nil for an unknown action), which `app` uses to build the KICK/BAN `final`.

### 3.3 The removal helper (SRV-02, SRV-10, D15)

```go
type RemovalReason string
const (
    ReasonDisconnect RemovalReason = "disconnected"
    ReasonKicked     RemovalReason = "kicked"
    ReasonBanned     RemovalReason = "banned"
    ReasonStale      RemovalReason = "stale"       // live stream, no RPC for 10 min
    ReasonStreamLost RemovalReason = "stream lost" // no stream for 120 s
)
type VoiceSessionDropper interface { DropVoiceSession(clientID uuid.UUID, reason string) }
type VoiceSessionDroppers []VoiceSessionDropper // fans out, skips nil entries

func SessionEndedError(reason RemovalReason) error // status.Errorf(codes.Unauthenticated, "session ended: %s", reason)
func (s *SimpleRadioServer) RemoveClient(id uuid.UUID, reason RemovalReason, final *pb.ServerUpdate) bool
func (s *SimpleRadioServer) removeClientIf(id uuid.UUID, reason RemovalReason, final *pb.ServerUpdate, cond func() bool) bool
```

`removeClientIf`: lock `s.mu`, then `serverState`; if `cond != nil && !cond()` → unlock, return false. `existed := Clients[id] != nil || RadioClients[id] != nil`; delete both; if existed take the `ClientLeft` snapshot (value copies); unlock `serverState`; take and delete `s.streams[id]` and `s.detachedAt[id]`; unlock `s.mu`. Then, outside every lock: `sub.end(final, SessionEndedError(reason))` if there was a subscription; `dropper.DropVoiceSession(id, string(reason))` (always; drops are idempotent); publish `ClientsChanged{ClientLeft}` once if existed. Returns `existed`. `cleanupClientState` is deleted.

`Disconnect` RPC → `RemoveClient(id, ReasonDisconnect, nil)`; `Success:true` if it existed, else the existing `Success:false` body (unreachable through gRPC now that the interceptor rejects unknown GUIDs).

### 3.4 SRV-01: refresh `LastUpdate`

`client.LastUpdate = s.now()` under `serverState.Lock` in `Ping`, `SyncClient` (caller's entry), `UpdateRadioInfo` (`Clients[id]`, inside the existing write section), and `UpdateClientInfo` (switch `time.Now()` → `s.now()`). No event for a refresh. Side effect: roster `LastUpdate` values in `SyncClient` advance (nothing in the VCS client reads them).

### 3.5 The reaper (SRV-01, SRV-02)

`StartCleanupRoutine(interval, staleAfter)` keeps its signature, stores `staleAfter`, and each tick calls `reapOnce(s.now())`. `const streamGrace = 120 * time.Second`.

`reapReasonLocked(id, now) (RemovalReason, bool)` (caller holds `s.mu` and `serverState`): client in `Clients` with a subscription and `now-LastUpdate > staleAfter` → `ReasonStale`; client without a subscription and `now - max(detachedAt[id], LastUpdate) > streamGrace` → `ReasonStreamLost`; subscription whose GUID is not in `Clients` → `ReasonStale` (defensive orphan). `reapOnce(now) []uuid.UUID` collects candidates under both locks, then for each calls `removeClientIf(id, reason, nil, cond)` where `cond` re-evaluates `reapReasonLocked(id, now)` and requires the same reason. A client that resubscribes or pings between collection and removal is not removed.

### 3.6 SRV-06: unknown GUID in `authenticate`

After the `claims == nil` check: `uuid.Parse(claims.ClientGuid)` fails, or the GUID is absent from `serverState.Clients` (under `RLock`) → `status.Errorf(codes.Unauthenticated, "session not found for %s: log in again", fullMethod)`. The `voiceSecretFor` comment is rewritten to cite this check (the `""` branch now only covers a removal racing the RPC).

### 3.7 Kick and Ban (SRV-10, D7, D17, D19)

- `control.Server` gains `localVoice srs.VoiceSessionDropper` (new last parameter of `NewServer`; `nil` in tests) and `RemoveClient(id uuid.UUID, reason srs.RemovalReason, final *srspb.ServerUpdate) (existed, running bool)`, delegating under `s.mu.RLock` when `isRunning && srsServer != nil`. `Start` passes `srs.VoiceSessionDroppers{s.localVoice, controlServer}` to `srs.NewSimpleRadioServer` (new last parameter).
- `app`: `voiceMu sync.RWMutex` + `currentVoiceServer() *voice.Server` (all `a.voiceServer` reads/writes go through it); `localVoiceDropper{a}` → `currentVoiceServer().DisconnectClient(id)` if non-nil; `remover sessionRemover` field (interface with `control.Server.RemoveClient`'s signature), assigned in `startGrpcServer`.
- **KickClient:** bad GUID → existing failure. `remover == nil` or `!running` → "Kick failed" / "Control server is not running". `!existed` → "Kick failed" / "Client not found". Else emit the bus `ServerAction` (admin GUI, voice nodes), notify success. The removal is `RemoveClient(id, ReasonKicked, srs.ServerActionUpdate(events.ActionKick, id, reason))`; `ClientLeft` comes from the helper.
- **BanClient:** bad GUID → existing failure; no remover / not running → "Ban failed" / "Control server is not running"; voice IP read first via `currentVoiceServer()` (nil server or not bound → existing "Client not found in voice server"); then under `ServerState.Lock`: not in `Clients` → "Client not found", already banned → existing message, else append the ban and `Save()` (failure → existing message, nothing removed); unlock; `RemoveClient(id, ReasonBanned, banUpdate)`; emit `BannedClientsChanged` and `ServerAction`; notify success.

### 3.8 Voice side (D4, D13, D18)

- `voiceontrol.VoiceControlServer.DropVoiceSession(id, reason)`: under `s.mu.RLock` copy `(node, node.stream)` for every node with a non-nil stream; outside `s.mu`, for each send `ControlMessage{ServerId: node.serverID, EventId: uuid.NewString(), Command: &pb.ControlMessage_KickClient{KickClient: &pb.KickClientRequest{ServerId: node.serverID, ClientId: id.String(), Reason: reason}}}` under `node.sendMu`; a send error is logged only. No nodes → no-op.
- `VoiceControlClient.SetKickHandler(func(uuid.UUID))` (guarded by a mutex); `applyControlMessage` gets `case *pb.ControlMessage_KickClient` → parse (invalid → WARN, ignore) → handler; after `applyDelta` for a `ClientDelta_LEFT`, the handler is also called (outside the state lock). `voice.Server.Listen` calls `SetKickHandler(v.DisconnectClient)` before `ConnectControlServer`. Older nodes ignore `kick_client` (no default case) → degrade to today's behaviour.

## 4. Grace × the VCS client's Phase 6 reconnect

| When Reconnect happens (t = 0 at network loss) | Server state | Result |
|---|---|---|
| t < ~70 s (server has not noticed the dead transport) | old subscription registered | `SyncClient` OK (refreshes `LastUpdate`); subscribe **displaces** the old one (D5), which ends `Aborted` on a transport nobody reads. Session kept. |
| ~70 s < t < 120 s after the later of stream end / last RPC | `detachedAt` set, grace running | `SyncClient` OK, refreshes `LastUpdate` (D9); subscribe clears `detachedAt`. Same GUID, same voice secret, radios re-pushed. Session kept. |
| later than 120-135 s | removed (`CLIENT_LEFT` sent, binding dropped) | `SyncClient` → `Unauthenticated` "session not found for /srs.SRSService/SyncClient: log in again". CL1 → "session expired". |
| Client-initiated close that reaches the server | stream ends, grace from then | Reconnect within 120 s keeps the session. |

Voice during a blip: the voice server's own 60 s inactivity cleanup may drop the UDP binding; Phase 5 binding-loss recovery re-HELLOs with the stored secret, valid while the session is in `Clients`. After removal the HELLO is rejected (`rejectUnknownClient`).

## 5. Tests (TDD, all `-tags headless`)

`srs` (fake clock, fake `grpc.ServerStreamingServer[pb.ServerUpdate]` recording `Send`, fake dropper, real `events.EventBus` to count `ClientLeft`):
1. Ping/SyncClient/UpdateRadioInfo each set `LastUpdate` to the fake clock; a live-stream client pinging every 5 s survives `reapOnce` at +11 min.
2. Live-stream client, no RPC for > 10 min → removed: both maps empty, handler returns `Unauthenticated` "session ended: stale", dropper called with `"stale"`, exactly one `ClientLeft`.
3. Stream ends (client ctx cancelled) → present after `reapOnce` at +119 s, removed at +121 s with `"stream lost"`.
4. Stream ends; resubscribe at +60 s → not removed at +300 s.
5. Stream ends; Ping at +100 s → present at +200 s, removed at +221 s.
6. Never-subscribed client removed 121 s after its `LastUpdate`.
7. `RemoveClient` from 8 goroutines → one `true`, one `ClientLeft`, `-race` clean.
8. `Disconnect` RPC → stream ends `Unauthenticated` "session ended: disconnected", dropper called, one `ClientLeft`, `Success:true`.
9. Duplicate subscribe → old handler returns `Aborted` "superseded by a newer subscription"; new entry intact; no `detachedAt`.
10. Subscribe for a GUID not in `Clients` → `Unauthenticated`, nothing registered.
11. `RemoveClient(kicked, KICK update)` → the KICK is the target's last `Send`, handler returns `Unauthenticated` "session ended: kicked".
12. MUTE for X reaches only X's stream; a KICK/BAN bus event reaches no stream.
13. Reaper re-validation: a client that resubscribes after collection is not removed (drive via `removeClientIf` with the reaper's cond).

`control`: 14. `TestAuthInterceptorCodes`: unknown GUID → `Unauthenticated` (unary + stream); malformed GUID claim → `Unauthenticated`; present GUID → handler called; AuthService still exempt. 15. bufconn: subscribe; `RemoveClient(kicked)` → client `Recv` gets KICK then `Unauthenticated` "session ended: kicked"; next `Ping` → `Unauthenticated`. 16. bufconn: duplicate subscribe over the wire → first stream `Aborted`; second still receives events; same voice secret from `SyncClient`.

`voiceontrol` / `voice` / `app`: 17. `DropVoiceSession` sends one `kick_client` per node with a stream; none without. 18. `applyControlMessage(KickClient)` calls the handler with the parsed GUID; invalid GUID → not called; `ClientDelta_LEFT` → handler called. 19. `voice.Server` kick handler removes the binding. 20. `app.KickClient`/`BanClient` with a fake remover: success, unknown client, control not running, ban IP-before-lock, nil voice server no panic.

Cross-repo: the client's Go suite with `VCS_SERVER_BIN` pointing at the SV1 headless build.

## 6. Files touched

`srs/srs_service.go`, (new) `srs/session.go` (subscription, removal helper, reaper), `srs/srs_service_test.go`, (new) `srs/session_test.go`, `control/server.go`, `control/server_test.go`, (new) `control/session_e2e_test.go`, `app/app.go`, `app/clients.go`, `app/server_control.go`, (new) `app/clients_test.go`, `voice/server.go`, `voice/server_test.go`, `voiceontrol/service.go`, `voiceontrol/service_test.go`, `voiceontrol/client.go`, `voiceontrol/client_test.go`. `state/server.go`: no change (listed in the umbrella; nothing here needs it). Not touched: any `.proto`, `go.mod`/`go.sum`, CI.

## 7. Behaviour changes (wire- or user-visible) — all gated

- Live clients are no longer evicted at 10 min.
- Dead sessions are removed 120-135 s after the later of stream end / last RPC, with `CLIENT_LEFT` and the voice binding dropped (today: ghosts forever).
- Unknown-GUID tokens get `Unauthenticated` on every non-auth RPC, including after a server restart and after removal (today: `Success:true`, empty voice secret).
- Kick and Ban actually disconnect: target gets KICK/BAN then `Unauthenticated` "session ended: kicked|banned"; binding dropped locally and on every voice node; token dead. Kicking an unknown GUID reports failure.
- Duplicate subscribe: last wins; displaced stream ends `Aborted`.
- `SERVER_ACTION` only on the target's own stream.
- Roster `LastUpdate` advances with pings. Server-to-server: control sends `kick_client`; nodes also drop on `LEFT`.
- Admin UI: Kick/Ban with the control server stopped fail cleanly (Q5, decided A).

## 8. Out of scope

The VCS client's reaction (CL1). Token refresh (PROTO_GAPS #12). C# peer fixes (note only). Deep-copied event snapshots / slow-subscriber disconnect / nil-GUID info update (SV2). Voice fan-out and `VoiceControlClient` reconnection (SV4). A token denylist. Ending subscriptions on server shutdown (unchanged: they end with the transport). Remote-node IP lookup for bans in distributed mode (a ban still requires a local voice binding, as today).

## 9. Approval-gate items

- **server-behaviour / wire-visible:** every bullet in §7.
- **cross-repo:** the CL1 contract (§11); the C# team note.
- **None of:** dependency change, proto change, CI change, repo settings, secrets. File deletion: none (only the `cleanupClientState` function goes).

## 10. Risks

- R1 **C# peer** (untestable here; PR #253 cannot authenticate): on `Unauthenticated` stream end it backs off 1-30 s and retries forever, its ping loop warns every 15 s; it has no "session gone" path. On `Aborted` (displaced) it backs off and resubscribes. Manual checklist + C# note.
- R2 **Lock order.** Only `s.mu → serverState`. `-race` on tests 7 and 13.
- R3 **Kick ordering.** KICK is sent by the target's own handler before it returns (D16), so it cannot be lost to asynchronous bus delivery.
- R4 **Distributed kick ordering.** Mitigated by D18 (the node drops on both `kick_client` and `LEFT`).
- R5 **Server restart** forces every client to log in again on Reconnect. Correct; CL1 owns the message.
- R6 **Field:** a client staying routable past 10 min, a blip reconnect inside 120 s, and kick/ban against a real server are manual items.
- R7 **Bus overflow.** `EventBus.Publish` drops when its queue is full, so a `ClientLeft` can be lost under load (pre-existing; SV2's area).

## 11. Cross-track contracts

### SV1 → CL1 (CL1 codes against this table)

| # | Situation | What the VCS client sees | Code | Message (informational; branch on the code) |
|---|---|---|---|---|
| C1 | Any non-AuthService unary RPC (SyncClient, Ping, UpdateRadioInfo, UpdateClientInfo, Disconnect, GetServerSettings) after the session was removed (stale, stream lost, kicked, banned, disconnected, server restarted) | RPC error | `Unauthenticated` | `session not found for <full method>: log in again` |
| C2 | Same RPCs with a missing / malformed / expired token (SV3) | RPC error | `Unauthenticated` | `authentication error for <method>: ...` / `missing ...` |
| C3 | Token valid but role too low (SV3) | RPC error | `PermissionDenied` | `insufficient role for <method>: ...` |
| C4 | Token keys unreadable on the server (SV3) | RPC error | `Internal` | `token verification unavailable` — not a session loss; do **not** route to "session expired" |
| C5 | Live update stream, session removed by the server | optional final `ServerUpdate` (SERVER_ACTION KICK or BAN with this client's GUID, only for kick/ban), then stream status | `Unauthenticated` | `session ended: <reason>`, reason ∈ `disconnected`, `kicked`, `banned`, `stale`, `stream lost` |
| C6 | Live update stream displaced by a newer `SubscribeToUpdates` with the same token | stream status, no final update | `Aborted` | `superseded by a newer subscription` — not a session loss; the newer stream is live |
| C7 | `SubscribeToUpdates` with a token whose session no longer exists | stream status | `Unauthenticated` | `session not found ...` (interceptor) or `session not found: log in again` (race) |
| C8 | Reconnect (`SyncClient` + `SubscribeToUpdates` with the stored token) while the session lives | success; same GUID, same `voice_secret`; radios kept server-side | — | — |
| C9 | Grace timing | Session survives while the stream is down as long as `now - max(stream end, last unary RPC) ≤ 120 s`; removal happens 120-135 s after that point (15 s sweep). Any unary RPC (incl. Ping) restarts the 120 s. A live stream without any unary RPC for 10 min is removed as `stale` (VCS pings every 5 s, so never). | — | — |
| C10 | `SERVER_ACTION` | Only ever arrives for this client itself (KICK/BAN only as C5's final update; MUTE/UNMUTE from the bus). The VCS client may keep ignoring it. | — | — |
| C11 | AuthService (InitAuth, GuestLogin, ...) | Unchanged: failures are `Success:false` bodies (SV3); a banned client's re-login is refused by the existing ban check. | — | — |
| C12 | User-initiated `Disconnect` | The server ends the stream with C5 `session ended: disconnected`; the client has normally already cancelled its stream generation and must not show "session expired" for it. | — | — |

### Other tracks

- **SV3 → SV1:** `authenticate(ctx, fullMethod)` / `authError` unchanged; SV1 adds only the unknown-GUID branch.
- **SV1 → SV2:** SV2 rebases onto `subscription` and the new stream loop. SRV-08 (slow subscriber) must end only the subscription, keeping the session: under `s.mu`, unregister by identity **and set `detachedAt[id] = s.now()`**, then `sub.end(nil, <SV2's status>)` — never `RemoveClient`. SV2's deep copies apply to the `ClientLeft` snapshot `removeClientIf` takes.
- **SV1 → SV4:** SV1 adds `SetKickHandler` and the `kick_client` / `LEFT` handler calls to `voiceontrol/client.go`, and `DropVoiceSession` to `voiceontrol/service.go`. SV4's single-flight reconnection must keep the kick handler across reconnects (a field set once, not per connection).
- **SV1 → DOC / manual checklist:** R1, R5, R6; the C# note.

## Q5 (resolved: A) — Kick/Ban while the control server is stopped

Today `KickClient` with the control server stopped deletes the client from `Clients` and reports success; `BanClient` works if a local voice server is bound. With the helper living in the control server there is no stream or session plane to act on. **Recommended A:** fail with "Control server is not running" and change nothing (the plan assumes this). **B:** fall back to a state-only removal (both maps, local voice drop, `CLIENT_LEFT`) when stopped.

**Decision (user, 2026-10-27): A.** Kick and Ban with the control server stopped fail with "Control server is not running" and change nothing.

## Implementation notes (added at execution)

- `app.sessionRemover` carries `IsRunning()` as well as `RemoveClient`: Ban must know the control server is running *before* it records and saves a ban, which the `(existed, running)` result of `RemoveClient` cannot tell it in time.
- A ban whose session cannot be ended because the control server stopped between that check and the removal stays recorded (reported as success, WARN logged); re-login is refused by the ban check anyway.
- `TestBanClientControlNotRunning` was added beside the plan's Task 9 tests to pin the Ban half of D19.
- The C# team note is `note-csharp-serveraction.md`, handed to the maintainers separately; no issue was filed in `VNGD-SimpleRadioStandalone`.
- Review follow-ups: `control.Server.RemoveClient` no longer holds `s.mu.RLock` across the removal (it copies `srsServer`/`isRunning` and releases the lock first, since the voice drop may block on a send); `app.remover` is guarded by `removerMu`; `BanClient` checks "Client not found" / "Client is already banned" under a read lock **before** the voice-IP lookup, so those failures read as on `main`, then repeats both checks under the write lock (§3.7's order otherwise unchanged).
