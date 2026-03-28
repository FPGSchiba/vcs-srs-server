# Go Code Review Fixes — Design Spec

**Date:** 2026-03-28
**Branch:** `develop`
**Scope:** Fix all 20 issues from go-review (4 CRITICAL, 7 HIGH, 9 MEDIUM)
**Strategy:** Fix by theme — 4 commits, each addressing a coherent category

---

## Theme 1: Concurrency Safety

**Commit scope:** 7 issues (C-2, C-3, C-4, H-2, H-3, H-7, M-9)

### C-2 — Write under RLock in `StartAuth`

**File:** `srs/auth_service.go:436-439`
**Fix:** Change `s.mu.RLock()` to `s.mu.Lock()` and `s.mu.RUnlock()` to `s.mu.Unlock()` for the two field assignments to `authenticatingClients`.

### C-3 — Goroutine leak in connection monitors

**Files:** `srs/plugin_client.go:80-98`, `voiceontrol/client.go:96-111`
**Fix for both structs:**
- Add `cancelMonitor context.CancelFunc` field
- In `establishConnection`, create `ctx, cancel := context.WithCancel(context.Background())`, store `cancel` on the struct, pass `ctx` to `WaitForStateChange`
- In `Close()`, call `v.cancelMonitor()` before closing the connection
- Guard against nil `cancelMonitor` in `Close()` for safety

### C-4 — Unsynchronized key cache

**File:** `utils/auth.go:17-20, 52-61`
**Fix:** Replace bare package-level `privateKey`/`publicKey` variables and nil-check pattern with `sync.Once`:
```go
var (
    keysOnce   sync.Once
    cachedPriv *ecdsa.PrivateKey
    cachedPub  *ecdsa.PublicKey
    keysErr    error
)

func getKeys(privateKeyFile, publicKeyFile string) (*ecdsa.PrivateKey, *ecdsa.PublicKey, error) {
    keysOnce.Do(func() {
        cachedPriv, cachedPub, keysErr = generateKey(privateKeyFile, publicKeyFile)
    })
    return cachedPriv, cachedPub, keysErr
}
```

### H-2 — Cleanup goroutine never stops

**File:** `srs/srs_service.go:324-347`, `control/server.go`
**Fix:**
- Add `stopChan chan struct{}` field to `SimpleRadioServer`
- Initialize in `NewSimpleRadioServer`
- Refactor `StartCleanupRoutine` to use `time.NewTicker` + `select` on `stopChan` (matches existing pattern in `voice/server.go:cleanupRoutine`)
- Add `Stop()` method that closes `stopChan`
- Store `srsServer` as a field on `control.Server` (currently a local variable in `Start()`) so it can be accessed during shutdown
- Call `srsServer.Stop()` from `control.Server.Stop()`

### H-3 — Fragile RLock/RUnlock in `handleFrontendEmits`

**File:** `app/app.go:299-310`
**Fix:** Read `RuntimeMode` into a local variable under lock, release lock immediately, then use the local to decide behavior:
```go
func (a *VCSApplication) handleFrontendEmits(channel chan events.Event) {
    a.DistributionState.RLock()
    isGUI := a.DistributionState.RuntimeMode == state.RuntimeModeGUI
    a.DistributionState.RUnlock()

    if !isGUI {
        return // headless mode: nothing to emit
    }
    for event := range channel {
        a.Logger.Debug("Received event from event bus", "name", event.Name)
        a.App.Event.EmitEvent(&application.CustomEvent{Name: event.Name, Data: event.Data})
    }
}
```

### H-7 — Lock held across Notify/EmitEvent

**File:** `app/clients.go`
**Affected methods:** `BanClient`, `UnbanClient`, `KickClient`, `MuteClient`, `UnmuteClient`, `IsClientMuted`
**Fix pattern for each method:**
1. Acquire `ServerState.Lock()`
2. Perform all state mutations, collect data needed for events into local variables
3. Release `ServerState.Unlock()`
4. Call `Notify` and `EmitEvent` after unlock

Remove `defer a.ServerState.Unlock()` and use explicit unlock before the notification calls.

### M-9 — Re-entrant mutex in `startVoiceServer`

**File:** `app/server_control.go:121-155`
**Fix:** Read settings values needed by the goroutine before spawning it:
```go
func (a *VCSApplication) startVoiceServer() {
    a.AdminState.Lock()
    if a.AdminState.VoiceStatus.IsRunning {
        a.AdminState.Unlock()
        a.Notify(events.NewNotification("Voice server error", "voice server is already running", "warning"))
        return
    }
    stopChan := make(chan struct{})
    a.StopSignals["voice"] = stopChan
    a.AdminState.Unlock()

    a.SettingsState.RLock()
    serverHost := fmt.Sprintf("%s:%d", a.SettingsState.Servers.Voice.Host, a.SettingsState.Servers.Voice.Port)
    a.SettingsState.RUnlock()

    go func() {
        a.voiceServer = voice.NewServer(a.ServerState, a.Logger, a.DistributionState, a.SettingsState)

        a.AdminState.Lock()
        a.AdminState.VoiceStatus.IsRunning = true
        a.AdminState.VoiceStatus.Error = ""
        a.AdminState.Unlock()

        if err := a.voiceServer.Listen(serverHost, stopChan); err != nil {
            a.AdminState.Lock()
            a.AdminState.VoiceStatus.Error = err.Error()
            a.AdminState.VoiceStatus.IsRunning = false
            a.AdminState.Unlock()
            a.Notify(events.NewNotification("voice server error", "Could not start Voice server", "error"))
            a.Logger.Error("voice server error", "error", err)
        }
    }()

    a.EmitEvent(events.Event{
        Name: events.AdminChanged,
        Data: a.AdminState,
    })
}
```

---

## Theme 2: Error Handling & Safety

**Commit scope:** 5 issues (C-1, H-4, M-4, M-5, M-8)

### C-1 — Unguarded type assertion on context value

**File:** `srs/srs_service.go:111, 147, 231, 268`
**Fix:** Introduce a helper function to DRY the four identical extraction + parse blocks:
```go
func clientIDFromContext(ctx context.Context) (uuid.UUID, error) {
    rawID, ok := ctx.Value(utils.ClientIDKey).(string)
    if !ok || rawID == "" {
        return uuid.Nil, fmt.Errorf("missing client id in context")
    }
    return uuid.Parse(rawID)
}
```
Replace all four call sites with `clientIDFromContext(ctx)`. Note: depends on `utils.ClientIDKey` from Theme 3 (H-1). Implementation order: Theme 3 before Theme 2, or define the key type early.

**Implementation order adjustment:** Theme 3 (API correctness) should be committed before Theme 2, since C-1's fix depends on H-1's context key. Revised commit order:
1. Concurrency safety
2. API correctness
3. Error handling & safety
4. Code quality

### H-4 — `GuestLogin` lock/pointer issues

**File:** `srs/auth_service.go:300-316`
**Three bugs, one restructured block:**
1. Use `s.settingsState.RLock()` instead of `s.mu.Lock()` (wrong lock)
2. Copy matched coalition value instead of taking `&coalition` (pointer to range var)
3. Ensure lock is always released (use defer or structured early-return)

```go
// Search coalitions under the correct lock
s.settingsState.RLock()
var selectedCoalition state.Coalition
var found bool
for _, coalition := range s.settingsState.Coalitions {
    if utils.CheckPasswordHash(coalition.Password, request.Password) {
        selectedCoalition = coalition  // value copy, not pointer
        found = true
        break
    }
}
s.settingsState.RUnlock()

if !found {
    return &pb.GuestLoginResponse{
        Success:     false,
        LoginResult: &pb.GuestLoginResponse_ErrorMessage{ErrorMessage: "No coalition found with that password"},
    }, nil
}
```

### M-4 — `pem.Decode` nil block dereference

**File:** `utils/auth.go:130-137`
**Fix:** Add nil checks after both `pem.Decode` calls:
```go
block, _ := pem.Decode([]byte(pemEncoded))
if block == nil {
    return nil, nil, fmt.Errorf("failed to decode private key PEM")
}

blockPub, _ := pem.Decode([]byte(pemEncodedPub))
if blockPub == nil {
    return nil, nil, fmt.Errorf("failed to decode public key PEM")
}
```

### M-5 — `ensureBanFileExists` error handling

**File:** `state/server.go:53-63`
**Fix:** Handle non-ErrNotExist errors explicitly:
```go
func ensureBanFileExists(bannedFile string) error {
    _, err := os.Stat(bannedFile)
    if err != nil && !os.IsNotExist(err) {
        return err
    }
    if os.IsNotExist(err) {
        f, createErr := os.Create(bannedFile)
        if createErr != nil {
            return createErr
        }
        f.Close()
    }
    return nil
}
```

### M-8 — Stream assigned before error check

**File:** `voiceontrol/client.go:139-140`
**Fix:** Move assignment after nil check:
```go
stream, err := v.client.EstablishStream(context.Background())
if err != nil {
    // handle error...
    return ...
}
v.stream = stream
```

---

## Theme 3: API Correctness

**Commit scope:** 5 issues (H-1, H-5, H-6, M-6, M-7)

### H-1 — Bare string context key

**Files:** `control/server.go:320`, `srs/srs_service.go` (read sites)
**Fix:** Create `utils/context.go` (new file):
```go
package utils

type ContextKey string

const ClientIDKey ContextKey = "client_id"
```
Update `control/server.go:320`:
```go
ctx = context.WithValue(ctx, utils.ClientIDKey, claims.ClientGuid)
```
Update all read sites in `srs/srs_service.go` to use `utils.ClientIDKey`.

### H-5 — `Remove` mutates caller's backing array

**File:** `utils/slice.go:12-22`
**Fix:** Replace swap-and-shrink with non-mutating filter:
```go
func Remove[K comparable](s []K, e K) []K {
    result := make([]K, 0, len(s))
    for _, v := range s {
        if v != e {
            result = append(result, v)
        }
    }
    return result
}
```
Remove the now-unused `indexOf` helper.

### H-6 — Wrong lock in `initControlServer`

**File:** `control/server.go:140-143`
**Fix:** Change `s.serverState.RLock()` / `s.serverState.RUnlock()` to `s.settingsState.RLock()` / `s.settingsState.RUnlock()`.

### M-6 — `GetSettings` returns pointer after releasing lock

**File:** `app/settings.go:9-13`
**Fix:** Return a value copy and use `RLock` (read-only operation):
```go
func (a *VCSApplication) GetSettings() state.SettingsState {
    a.SettingsState.RLock()
    defer a.SettingsState.RUnlock()
    return *a.SettingsState
}
```
**Risk note:** Changing the return type from `*state.SettingsState` to `state.SettingsState` may affect Wails-generated TypeScript bindings. Verify the frontend still compiles after this change. If bindings break, an alternative is to keep the pointer return but return a newly allocated copy: `s := *a.SettingsState; return &s`.

### M-7 — UDP ban check uses wrong address

**File:** `voice/server.go:110-111`
**Fix:** Change `conn.RemoteAddr().String()` to `remoteAddr.IP.String()` (where `remoteAddr` is the `*net.UDPAddr` from `ReadFromUDP`). Also fix the log line at 118 to use `remoteAddr`.

---

## Theme 4: Code Quality

**Commit scope:** 3 issues (M-1, M-2, M-3)

### M-1 — `fmt.Println` in production code

**File:** `app/app.go:301, 306`
**Fix:** Replace with structured logger:
- Line 301: `a.Logger.Debug("Current runtime mode", "mode", a.DistributionState.RuntimeMode)`
- Line 306: `a.Logger.Debug("Received event from event bus", "name", event.Name)`
- Remove `"fmt"` from imports if no longer needed.

### M-2 — Capitalised error strings

**Scope:** Only fix in files already being modified. Internal-facing errors only (not user-facing gRPC response messages).
- Logger calls using `"Failed to..."` as error keys: leave as-is (these are log messages, not error strings)
- `fmt.Errorf` / `errors.New` with capitalised strings: lowercase the first letter

### M-3 — Regex recompiled on every call

**File:** `srs/utils.go:22-25`
**Fix:** Compile once at package level:
```go
var unitIDRegex = regexp.MustCompile(`^[A-Z0-9]{2,4}$`)

func checkUnitId(unitId string) bool {
    return unitIDRegex.MatchString(unitId)
}
```

---

## Commit Order

| Order | Theme | Issues | New files |
|-------|-------|--------|-----------|
| 1 | Concurrency safety | C-2, C-3, C-4, H-2, H-3, H-7, M-9 | none |
| 2 | API correctness | H-1, H-5, H-6, M-6, M-7 | `utils/context.go` |
| 3 | Error handling & safety | C-1, H-4, M-4, M-5, M-8 | none |
| 4 | Code quality | M-1, M-2, M-3 | none |

API correctness is committed before error handling because C-1's fix depends on H-1's context key type.

## Files Modified

| File | Themes |
|------|--------|
| `srs/auth_service.go` | 1, 3 |
| `srs/plugin_client.go` | 1 |
| `voiceontrol/client.go` | 1, 3 |
| `utils/auth.go` | 1, 3 |
| `srs/srs_service.go` | 1, 2, 3 |
| `app/app.go` | 1, 4 |
| `app/clients.go` | 1 |
| `app/server_control.go` | 1 |
| `control/server.go` | 1, 2, 3 |
| `utils/context.go` | 2 (new) |
| `utils/slice.go` | 2 |
| `app/settings.go` | 2 |
| `voice/server.go` | 2 |
| `srs/utils.go` | 4 |
| `state/server.go` | 3 |

## Verification

After all 4 commits:
- `go vet ./...` must pass
- `go build -race ./...` must pass
- Existing tests must pass
