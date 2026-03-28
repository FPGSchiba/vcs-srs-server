# Go Code Review Fixes (Round 2) — Design Spec

**Date:** 2026-03-28
**Branch:** `develop`
**Scope:** Fix all 24 issues from go-review round 2 (8 CRITICAL, 7 HIGH, 9 MEDIUM)
**Strategy:** Fix by theme — 5 commits, each addressing a coherent category

---

## Commit Order

| Order | Theme | Issues | New files |
|-------|-------|--------|-----------|
| 1 | Voice server concurrency | CRIT-1, CRIT-2, CRIT-3, MED-8 | none |
| 2 | Event bus & streaming | HIGH-4, HIGH-5, HIGH-6 | none |
| 3 | Shared state safety | CRIT-4, CRIT-5, CRIT-6, HIGH-1, HIGH-2, HIGH-3, MED-5 | `state/snapshot.go` |
| 4 | Auth & security | CRIT-7, CRIT-8, HIGH-7 | `utils/tls.go` |
| 5 | Code quality | MED-1, MED-2, MED-3, MED-4, MED-6, MED-7, MED-9 | none |

---

## Theme 1: Voice Server Concurrency

**Commit scope:** CRIT-1, CRIT-2, CRIT-3, MED-8
**File:** `voice/server.go` only

### CRIT-1 — RLock→Lock upgrade race in packet handlers

**Affected methods:** `handleKeepalivePacket`, `handleVoicePacket`, `handleTestFrequencyPacket`

**Problem:** Each method reads a client pointer under `RLock`, releases it, then acquires `Lock` to write `client.LastSeen`. The `cleanup()` goroutine can delete the map entry between the two lock acquisitions, causing a write to a logically deleted client.

**Fix:** Combine read-check-write into a single `Lock` critical section:
```go
v.Lock()
client, exists := v.clients[packet.SenderID]
if exists {
    client.LastSeen = time.Now()
}
v.Unlock()
if !exists {
    v.logger.Warn("Received packet from unknown client", "sender_id", packet.SenderID)
    return
}
```
Apply this pattern to all three handlers. The addr-based log statements that follow can use the retained `client` pointer (already safe since we hold a copy of the pointer value).

### CRIT-2 — Check-then-act race in `DisconnectClient`

**Problem:** Existence check under `RLock`, lock released, then `Lock` acquired to delete. `cleanup()` or another concurrent `DisconnectClient` can delete the entry in the window.

**Fix:** Acquire `Lock` directly, read and delete atomically, log after unlock:
```go
func (v *Server) DisconnectClient(clientID uuid.UUID) {
    v.Lock()
    client, exists := v.clients[clientID]
    if exists {
        delete(v.clients, clientID)
    }
    v.Unlock()
    if exists {
        v.logger.Info("Disconnected voice client", "id", clientID, "addr", client.Addr.String())
    }
}
```

### CRIT-3 — `stopChan` double-close panic on second `Stop()` call

**Problem:** `Stop()` calls `close(v.stopChan)` with no guard. A second call panics.

**Fix:** Add `stopOnce sync.Once` field to `Server`. Wrap the close:
```go
type Server struct {
    // ...
    stopOnce sync.Once
}

func (v *Server) Stop() error {
    v.Lock()
    defer v.Unlock()
    if !v.running {
        return nil
    }
    v.stopOnce.Do(func() { close(v.stopChan) })
    if v.conn != nil {
        if err := v.conn.Close(); err != nil {
            return err
        }
    }
    v.running = false
    v.logger.Info("Voice server stopped")
    return nil
}
```

### MED-8 — UDP buffer reused before goroutine completes

**Problem:** `buffer[:n]` is a slice of the shared 1024-byte array. The next `ReadFromUDP` overwrites it before `handlePacket` finishes reading.

**Fix:** Copy before spawning:
```go
pkt := make([]byte, n)
copy(pkt, buffer[:n])
go v.handlePacket(pkt, remoteAddr)
```

---

## Theme 2: Event Bus & Streaming

**Commit scope:** HIGH-4, HIGH-5, HIGH-6
**Files:** `events/bus.go`, `srs/srs_service.go`

### HIGH-4 — Busy-poll loop and goroutine leak in `EventBus`

**Problem:** `startPublisher` holds `eb.Lock()` on every 10ms cycle even when the queue is empty, blocking all subscribers. The goroutine has no shutdown path — it runs forever.

**Fix:** Replace `publishQueue []Event` slice with a `queue chan Event` (buffered at 256). Replace the poll loop with a blocking `select`. Add `Stop()` method.

```go
type EventBus struct {
    mu          sync.RWMutex
    subscribers map[string][]chan Event
    queue       chan Event
    stop        chan struct{}
    once        sync.Once
}

func NewEventBus() *EventBus {
    eb := &EventBus{
        subscribers: make(map[string][]chan Event),
        queue:       make(chan Event, 256),
        stop:        make(chan struct{}),
    }
    go eb.startPublisher()
    return eb
}

func (eb *EventBus) startPublisher() {
    for {
        select {
        case event := <-eb.queue:
            eb.dispatch(event)
        case <-eb.stop:
            return
        }
    }
}

func (eb *EventBus) Publish(event Event) {
    select {
    case eb.queue <- event:
    default:
        // queue full — drop (non-blocking to avoid blocking callers)
    }
}

func (eb *EventBus) Stop() {
    eb.once.Do(func() { close(eb.stop) })
}
```

Also add `Unsubscribe(ch chan Event)` to remove a subscriber by channel reference — needed by the streaming implementation:
```go
func (eb *EventBus) Unsubscribe(ch chan Event) {
    eb.Lock()
    defer eb.Unlock()
    for name, subs := range eb.subscribers {
        for i, sub := range subs {
            if sub == ch {
                eb.subscribers[name] = append(subs[:i], subs[i+1:]...)
                break
            }
        }
    }
}
```

Call `eb.Stop()` from `VCSApplication.StopServer()` after stopping all servers.

### HIGH-5 — `SubscribeToUpdates` never sends data

**Problem:** The method stores the stream and returns nil immediately. Clients block indefinitely waiting for updates. No `stream.Send` call exists anywhere.

**Fix:** Rewrite to block until stream closes, forwarding relevant events as `pb.ServerUpdate` messages:

```go
func (s *SimpleRadioServer) SubscribeToUpdates(_ *pb.Empty, stream grpc.ServerStreamingServer[pb.ServerUpdate]) error {
    clientID, err := clientIDFromContext(stream.Context())
    if err != nil {
        s.logger.Error("SubscribeToUpdates failed: invalid client ID", "error", err)
        return err
    }

    ch := s.eventBus.Subscribe("*")
    defer s.eventBus.Unsubscribe(ch)

    s.mu.Lock()
    if _, exists := s.streams[clientID]; exists {
        s.mu.Unlock()
        return fmt.Errorf("client %s is already subscribed to updates", clientID)
    }
    s.streams[clientID] = stream
    s.mu.Unlock()

    defer func() {
        s.mu.Lock()
        delete(s.streams, clientID)
        s.mu.Unlock()
    }()

    s.logger.Info("Client subscribed to updates", "client_id", clientID)

    for {
        select {
        case <-stream.Context().Done():
            s.logger.Info("Client unsubscribed from updates", "client_id", clientID)
            return nil
        case event, ok := <-ch:
            if !ok {
                return nil
            }
            update := buildServerUpdate(event)
            if update == nil {
                continue
            }
            if err := stream.Send(update); err != nil {
                s.logger.Error("Failed to send update to client", "client_id", clientID, "error", err)
                return err
            }
        }
    }
}
```

Add helper `buildServerUpdate(event events.Event) *pb.ServerUpdate` that converts `ClientsChanged`, `RadioClientsChanged`, and `SettingsChanged` events into the appropriate `pb.ServerUpdate` oneof field. Returns `nil` for all other event types (skipped).

### HIGH-6 — No-op `stream.Context().Done()` in cleanup routine

**Problem:** `stream.Context().Done()` returns a channel; calling it as a statement discards the result. It does nothing.

**Fix:** Remove the call entirely from `StartCleanupRoutine`. The stream cleanup already happens via the `SubscribeToUpdates` goroutine's `defer delete(s.streams, clientID)` when the context cancels.

---

## Theme 3: Shared State Safety

**Commit scope:** CRIT-4, CRIT-5, CRIT-6, HIGH-1, HIGH-2, HIGH-3, MED-5
**New file:** `state/snapshot.go`

### CRIT-4 — Cleanup goroutine leak in `SimpleRadioServer`

**Problem:** `Stop()` closes `stopChan` and returns immediately. No join confirms the goroutine exited. Rapid stop/start cycles create multiple concurrent cleanup goroutines.

**Fix:** Add `wg sync.WaitGroup` field to `SimpleRadioServer`. In `StartCleanupRoutine`:
```go
s.wg.Add(1)
go func() {
    defer s.wg.Done()
    ticker := time.NewTicker(interval)
    defer ticker.Stop()
    for {
        select {
        case <-s.stopChan:
            return
        case <-ticker.C:
            // ... cleanup logic
        }
    }
}()
```

Update `Stop()`:
```go
func (s *SimpleRadioServer) Stop() {
    s.stopOnce.Do(func() { close(s.stopChan) })
    s.wg.Wait()
}
```

### CRIT-5 — `stopc` reused after `Close()` in `voiceontrol/client.go`

**Problem:** `v.stopc` is only initialized when nil. After `Close()` closes the channel, any subsequent `handleReconnection` → `establishStream` goroutine sees a closed channel and exits immediately.

**Fix:** Re-initialize `v.stopc` at the top of `ConnectControlServer()` on every call:
```go
func (v *VoiceControlClient) ConnectControlServer() error {
    v.stopc = make(chan struct{})
    // ... rest of existing logic
}
```

### CRIT-6 — Live map passed unprotected after unlock in `app/clients.go` and `srs/srs_service.go`

**Problem:** After `a.ServerState.Unlock()`, the live `Clients` or `RadioClients` map is passed directly as event data, racing with concurrent writes.

**Fix:** Take a shallow copy before emitting at every affected site:
```go
// Clients snapshot
clientsSnap := make(map[uuid.UUID]*state.ClientState, len(a.ServerState.Clients))
for k, v := range a.ServerState.Clients {
    clientsSnap[k] = v
}
a.EmitEvent(events.Event{Name: events.ClientsChanged, Data: clientsSnap})

// RadioClients snapshot
radioSnap := make(map[uuid.UUID]*state.RadioState, len(a.ServerState.RadioClients))
for k, v := range a.ServerState.RadioClients {
    radioSnap[k] = v
}
a.EmitEvent(events.Event{Name: events.RadioClientsChanged, Data: radioSnap})
```

Apply to: `BanClient`, `KickClient`, `MuteClient`, `UnmuteClient` in `app/clients.go`, and `SyncClient`, `Disconnect`, `UpdateClientInfo`, `UpdateRadioInfo` in `srs/srs_service.go`.

### HIGH-1 + MED-5 — `GetSettings` and `GetServerStatus` return live pointer after lock release

**Problem:** Both methods return `*state.SettingsState` / `*state.AdminState` — the live struct — after releasing the lock. Callers read fields with no synchronization. The embedded `sync.RWMutex` prevents returning a value copy.

**Fix:** Add dedicated snapshot structs to `state/snapshot.go` (no embedded mutex):
```go
// state/snapshot.go
package state

type SettingsSnapshot struct {
    General     GeneralSettings
    Servers     ServerSettings
    Frequencies FrequencySettings
    Coalitions  []Coalition
}

type AdminStateSnapshot struct {
    HTTPStatus    ServiceStatus
    VoiceStatus   ServiceStatus
    ControlStatus ServiceStatus
}
```

Update getters:
```go
func (a *VCSApplication) GetSettings() state.SettingsSnapshot {
    a.SettingsState.RLock()
    defer a.SettingsState.RUnlock()
    return state.SettingsSnapshot{
        General:     a.SettingsState.General,
        Servers:     a.SettingsState.Servers,
        Frequencies: a.SettingsState.Frequencies,
        Coalitions:  a.SettingsState.Coalitions,
    }
}

func (a *VCSApplication) GetServerStatus() state.AdminStateSnapshot {
    a.AdminState.RLock()
    defer a.AdminState.RUnlock()
    return state.AdminStateSnapshot{
        HTTPStatus:    a.AdminState.HTTPStatus,
        VoiceStatus:   a.AdminState.VoiceStatus,
        ControlStatus: a.AdminState.ControlStatus,
    }
}
```

**Risk:** Changing the return types updates Wails TypeScript bindings. Verify `frontend/` still compiles after this change.

### HIGH-2 — TOCTOU in `StartStandaloneServer` / `StartControlServer` / `StartVoiceServer`

**Problem:** `IsRunning` is checked under lock, lock released, then the inner `start*` functions called. Two concurrent callers can both pass the outer check.

**Fix:** Remove the outer `IsRunning` guard entirely from all three methods. The inner `start*` functions (`startGrpcServer`, `startVoiceServer`, `startHTTPServer`) already each check `IsRunning` under their own locks — those are the authoritative guards. The outer check is redundant and racy.

### HIGH-3 — `sync.Once` in `control.Server` not reset between Stop/Start cycles

**Problem:** `stopOnce sync.Once` fires once. After a stop+start cycle, the next `Stop()` is silently a no-op, leaving the gRPC server running.

**Fix:** Allocate a fresh `*control.Server` on every call to `startGrpcServer()`. Each cycle gets a new `Server` with a fresh `sync.Once`:
```go
func (a *VCSApplication) startGrpcServer() {
    // ... existing IsRunning check
    a.controlServer = control.NewServer(a.ServerState, a.SettingsState, a.Logger, a.eventBus)
    go func() {
        if err := a.controlServer.Start(...); err != nil {
            // ... error handling
        }
    }()
}
```

---

## Theme 4: Auth & Security

**Commit scope:** CRIT-7, CRIT-8, HIGH-7
**New file:** `utils/tls.go`

### CRIT-7 — Unguarded JWT claims type assertions in `utils/auth.go`

**Problem:** `claims["client_guid"].(string)` and `claims["role_id"].(float64)` panic if the claims are missing or the wrong type. A single malformed token from any client crashes the server goroutine.

**Fix:** Use comma-ok assertions in `getJWTClaims`:
```go
if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {
    guidRaw, ok := claims["client_guid"].(string)
    if !ok || guidRaw == "" {
        return nil, errors.New("missing or invalid client_guid claim")
    }
    roleRaw, ok := claims["role_id"].(float64)
    if !ok {
        return nil, errors.New("missing or invalid role_id claim")
    }
    return &TokenClaims{
        ClientGuid: guidRaw,
        RoleId:     uint8(roleRaw),
    }, nil
}
return nil, errors.New("invalid token")
```

### HIGH-7 — Unguarded plugin response type assertions in `srs/auth_service.go`

**Problem:** Two bare type assertions on plugin `oneof` fields panic if the field is nil or the wrong variant:
- Line 473: `response.StepResult.(*authpb.AuthStepResponse_Complete).Complete`
- Line 644: `loginResponse.StepResult.(*authpb.AuthStepResponse_ErrorMessage)`

**Fix:** Use comma-ok in both locations. Return a proper error response if assertion fails:
```go
// handleAuthComplete
completeResult, ok := response.StepResult.(*authpb.AuthStepResponse_Complete)
if !ok || completeResult.Complete == nil {
    return &pb.AuthStepResponse{
        Success: false,
        Result:  &pb.AuthStepResponse_ErrorMessage{ErrorMessage: "plugin returned AUTH_COMPLETE but result payload is missing"},
    }, nil
}
result := completeResult.Complete

// ContinueAuth AUTH_FAILED branch
errResult, ok := loginResponse.StepResult.(*authpb.AuthStepResponse_ErrorMessage)
errMsg := "unknown error"
if ok && errResult != nil {
    errMsg = errResult.ErrorMessage
}
```

### CRIT-8 — Plaintext auth plugin transport in `srs/plugin_client.go`

**Problem:** Plugin connections use `insecure.NewCredentials()`. Auth tokens and credentials flow in plaintext.

**Fix:** Generate a self-signed TLS CA + leaf cert at startup. Plugin clients use the CA cert for server verification.

**New file `utils/tls.go`:**
```go
package utils

// EnsureTLSCerts generates a local CA cert and a leaf cert signed by it if the
// files do not already exist. Uses ECDSA P-256, consistent with the JWT key infrastructure.
// Returns the leaf TLS certificate and a cert pool containing the CA cert.
func EnsureTLSCerts(caFile, certFile, keyFile string) (tls.Certificate, *x509.CertPool, error)
```

The function:
1. Checks if all three files exist; if so, loads and returns them.
2. If any are missing, generates a new ECDSA P-256 CA key + self-signed CA cert, then a leaf key + cert signed by the CA.
3. Writes all three files.

**`PluginClient` changes:**
- Add `caCertFile string` field (path to CA cert, passed from settings).
- In `establishConnection`, replace `insecure.NewCredentials()` with TLS credentials:
```go
creds, err := credentials.NewClientTLSFromFile(v.caCertFile, "")
if err != nil {
    v.logger.Warn("Failed to load TLS CA cert, falling back to insecure", "error", err)
    // fall back to insecure with warning — maintains backward compat
    opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
} else {
    opts = append(opts, grpc.WithTransportCredentials(creds))
}
```

**Settings change:** Add `TLSCACertFile string` to `ServerSettings` (or a new `SecuritySettings` struct), defaulting to `"ca-cert.pem"`.

**Startup:** Call `utils.EnsureTLSCerts(caFile, certFile, keyFile)` in `StartUp` and `HeadlessStartup` before starting servers.

---

## Theme 5: Code Quality

**Commit scope:** MED-1, MED-2, MED-3, MED-4, MED-6, MED-7, MED-9

### MED-1 — `keysOnce` global prevents runtime key rotation (`utils/auth.go`)

**Fix:** Add a comment documenting the limitation:
```go
// keysOnce ensures keys are loaded only once per process lifetime.
// Changing the key file paths in settings requires a server restart to take effect.
var (
    keysOnce   sync.Once
    cachedPriv *ecdsa.PrivateKey
    cachedPub  *ecdsa.PublicKey
    keysErr    error
)
```
No code change — the singleton pattern is load-bearing and a full refactor is out of scope.

### MED-2 — `authInterceptor` panics on malformed `FullMethod` (`control/server.go`)

**Fix:** Validate slice lengths before indexing:
```go
elements := strings.Split(info.FullMethod, "/")
if len(elements) < 3 {
    return nil, status.Errorf(codes.Unauthenticated, "malformed method path: %s", info.FullMethod)
}
fullServiceName := elements[1]
pathName := elements[2]
parts := strings.Split(fullServiceName, ".")
if len(parts) < 2 {
    return nil, status.Errorf(codes.Unauthenticated, "malformed service name: %s", fullServiceName)
}
serviceName := parts[len(parts)-1]
```

### MED-3 — `SrsServiceMinimumRoleMap` is a mutable exported global (`utils/auth.go`)

**Fix:** Unexport: rename to `srsServiceMinimumRoleMap`. Update the one read site in `control/server.go` to use the unexported name (both are in `utils` package — no, `control` is a different package so we need a getter function):
```go
// utils/auth.go
func GetMinimumRoleForMethod(methodName string) (uint8, bool) {
    role, ok := srsServiceMinimumRoleMap[methodName]
    return role, ok
}
```
Update `control/server.go` to call `utils.GetMinimumRoleForMethod(pathName)`.

### MED-4 — Redundant `break` in `HeadlessStartup` switch (`app/app.go`)

**Fix:** Remove the `break` statements from all three `switch` cases. Go `switch` does not fall through by default.

### MED-6 — Wildcard subscriber channel not drained in headless mode (`app/app.go`)

**Problem:** When `!isGUI`, `handleFrontendEmits` returns immediately but the wildcard subscriber channel remains registered. Events sent to it are silently dropped via the `default` branch in `dispatch`.

**Fix:** Drain the channel in a discard loop so the goroutine exits cleanly when the bus is stopped:
```go
if !isGUI {
    for range channel {} // drain until channel closed by EventBus.Stop()
    return
}
```

### MED-7 — `CheckPasswordHash` parameter names inverted vs. bcrypt convention (`utils/general.go`)

**Problem:** The function signature `CheckPasswordHash(password, hash string)` passes `hash` as first arg to `bcrypt.CompareHashAndPassword(hash, password)`. The parameter name `password` is the first arg at call sites, but it receives `coalition.Password` (the stored bcrypt hash). This is a naming inversion that makes the call site ambiguous.

**Fix:** Rename parameters to match the bcrypt convention:
```go
// CheckPasswordHash compares a bcrypt hash with a plaintext password.
func CheckPasswordHash(hash, plaintext string) bool {
    err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(plaintext))
    return err == nil
}
```
Update the one call site in `srs/auth_service.go`:
```go
if utils.CheckPasswordHash(coalition.Password, request.Password) {
```
(Same values — `coalition.Password` is the stored hash, `request.Password` is the plaintext. Now the intent is explicit.)

### MED-9 — Nil `stopc` check in `handleReconnection` (`voiceontrol/client.go`)

**Problem:** If `handleReconnection` is called before `establishStream` ever succeeded, `v.stopc` is nil. Receiving on a nil channel blocks forever.

**Fix:** Initialize `stopc` to a never-closing channel as the zero value in `NewVoiceControlClient`, so it is never nil:
```go
func NewVoiceControlClient(...) *VoiceControlClient {
    return &VoiceControlClient{
        // ...
        stopc: make(chan struct{}),
    }
}
```
`ConnectControlServer()` already re-creates it on each call (CRIT-5 fix), so this just ensures the initial state is safe.

---

## Files Modified

| File | Themes |
|------|--------|
| `voice/server.go` | 1 |
| `events/bus.go` | 2 |
| `srs/srs_service.go` | 2, 3 |
| `state/snapshot.go` | 3 (new) |
| `app/clients.go` | 3 |
| `app/settings.go` | 3 |
| `app/app.go` | 3, 5 |
| `app/server_control.go` | 3 |
| `control/server.go` | 3, 5 |
| `voiceontrol/client.go` | 3, 5 |
| `utils/tls.go` | 4 (new) |
| `utils/auth.go` | 4, 5 |
| `srs/auth_service.go` | 4 |
| `utils/general.go` | 5 |

## Verification

After all 5 commits:
- `go vet ./...` must pass
- `go build -race ./...` must pass
- Frontend must compile (Wails bindings updated for snapshot return types)
