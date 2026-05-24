# Go Code Review Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix all 20 issues (4 CRITICAL, 7 HIGH, 9 MEDIUM) identified by the go-review, organized into 4 themed commits.

**Architecture:** Fixes are grouped by theme (concurrency, API correctness, error handling, code quality) with each theme producing one atomic commit. API correctness is committed before error handling because C-1's context key fix depends on H-1's key type definition.

**Tech Stack:** Go 1.25, gRPC, sync primitives, Wails v3

**Spec:** `docs/superpowers/specs/2026-03-28-go-review-fixes-design.md`

---

## File Structure

| File | Action | Theme(s) | Purpose |
|------|--------|----------|---------|
| `utils/context.go` | Create | 2 | Typed context key for `client_id` |
| `srs/auth_service.go` | Modify | 1, 3 | Fix RLock→Lock (C-2), GuestLogin lock/pointer (H-4) |
| `srs/plugin_client.go` | Modify | 1 | Cancel monitor goroutine (C-3) |
| `voiceontrol/client.go` | Modify | 1, 3 | Cancel monitor goroutine (C-3), stream assign order (M-8) |
| `utils/auth.go` | Modify | 1, 3 | sync.Once for keys (C-4), PEM nil check (M-4) |
| `srs/srs_service.go` | Modify | 1, 2, 3 | Stop cleanup goroutine (H-2), context key (H-1/C-1) |
| `app/app.go` | Modify | 1, 4 | Fix handleFrontendEmits (H-3), replace fmt.Println (M-1) |
| `app/clients.go` | Modify | 1 | Unlock before emit (H-7) |
| `app/server_control.go` | Modify | 1 | Remove re-entrant lock (M-9) |
| `control/server.go` | Modify | 1, 2 | Store srsServer (H-2), fix lock (H-6), context key (H-1) |
| `utils/slice.go` | Modify | 2 | Immutable Remove (H-5) |
| `app/settings.go` | Modify | 2 | Return value copy (M-6) |
| `voice/server.go` | Modify | 2 | Fix ban check address (M-7) |
| `srs/utils.go` | Modify | 4 | Precompile regex (M-3) |
| `state/server.go` | Modify | 3 | Fix ensureBanFileExists error handling (M-5) |

---

## Theme 1: Concurrency Safety

### Task 1: Fix C-2 — Write under RLock in `StartAuth`

**Files:**
- Modify: `srs/auth_service.go:436-439`

- [ ] **Step 1: Fix the lock type**

In `srs/auth_service.go`, find the `StartAuth` method's `AUTH_CONTINUE` case (around line 436). Change the read lock to a write lock:

```go
// BEFORE (line 436-439):
	case authpb.AuthStepStatus_AUTH_CONTINUE:
		s.mu.RLock()
		s.authenticatingClients[clientGuid].PluginUsed = request.AuthenticationPlugin
		s.authenticatingClients[clientGuid].SessionId = loginResponse.SessionId
		s.mu.RUnlock()

// AFTER:
	case authpb.AuthStepStatus_AUTH_CONTINUE:
		s.mu.Lock()
		s.authenticatingClients[clientGuid].PluginUsed = request.AuthenticationPlugin
		s.authenticatingClients[clientGuid].SessionId = loginResponse.SessionId
		s.mu.Unlock()
```

- [ ] **Step 2: Verify build**

Run: `cd D:/Projects/Vanguard/vngd-srs-server && go build ./...`
Expected: BUILD SUCCESS

---

### Task 2: Fix C-3 — Goroutine leak in `PluginClient` connection monitor

**Files:**
- Modify: `srs/plugin_client.go`

- [ ] **Step 1: Add `cancelMonitor` field to `PluginClient`**

In `srs/plugin_client.go`, add the field and update the constructor:

```go
type PluginClient struct {
	client pb.AuthPluginServiceClient
	// GetServerState returns the current state of the voice control server.
	conn             *grpc.ClientConn
	logger           *slog.Logger
	settingsState    *state.SettingsState
	connectionFailed bool
	address          string
	pluginName       string
	stopc            chan struct{}
	cancelMonitor    context.CancelFunc
	configuredFlows  []string
	config           *state.FlowConfiguration
}
```

- [ ] **Step 2: Use cancellable context in `establishConnection`**

Replace the monitor goroutine in `establishConnection`:

```go
func (v *PluginClient) establishConnection() error {
	// Cancel any previous monitor goroutine
	if v.cancelMonitor != nil {
		v.cancelMonitor()
	}
	ctx, cancel := context.WithCancel(context.Background())
	v.cancelMonitor = cancel

	go func() {
		lastState := v.conn.GetState()
		for {
			if !v.conn.WaitForStateChange(ctx, lastState) {
				return
			}
			newState := v.conn.GetState()
			if newState == connectivity.Idle {
				v.logger.Warn(fmt.Sprintf("Plugin: '%s' connection idle...", v.pluginName))
				err := v.settingsState.SetPluginEnabled(v.pluginName, false)
				if err != nil {
					return
				}
				go v.handleReconnection()
			}
			lastState = newState
		}
	}()

	err := v.configurePlugin()
	if err != nil {
		return err
	}
	configurableFlows, err := v.discoverPluginFlows()
	if err != nil {
		return err
	}
	return v.configureFlows(configurableFlows)
}
```

- [ ] **Step 3: Cancel monitor in `Close`**

Update the `Close` method:

```go
func (v *PluginClient) Close() error {
	if v.cancelMonitor != nil {
		v.cancelMonitor()
	}
	if v.stopc != nil {
		close(v.stopc)
	}
	if v.conn != nil {
		return v.conn.Close()
	}
	return nil
}
```

- [ ] **Step 4: Verify build**

Run: `cd D:/Projects/Vanguard/vngd-srs-server && go build ./...`
Expected: BUILD SUCCESS

---

### Task 3: Fix C-3 — Goroutine leak in `VoiceControlClient` connection monitor

**Files:**
- Modify: `voiceontrol/client.go`

- [ ] **Step 1: Add `cancelMonitor` field to `VoiceControlClient`**

```go
type VoiceControlClient struct {
	client pb.VoiceControlServiceClient
	// GetServerState returns the current state of the voice control server.
	conn                *grpc.ClientConn
	serverId            string
	assignedFrequencies []*pb.FrequencyRange
	logger              *slog.Logger
	stream              grpc.BidiStreamingClient[pb.ControlResponse, pb.ControlMessage]
	stopc               chan struct{}
	cancelMonitor       context.CancelFunc
	connectionFailed    bool
	settingsState       *state.SettingsState
}
```

- [ ] **Step 2: Use cancellable context in `establishConnection`**

```go
func (v *VoiceControlClient) establishConnection() error {
	// Cancel any previous monitor goroutine
	if v.cancelMonitor != nil {
		v.cancelMonitor()
	}
	ctx, cancel := context.WithCancel(context.Background())
	v.cancelMonitor = cancel

	go func() {
		lastState := v.conn.GetState()
		for {
			if !v.conn.WaitForStateChange(ctx, lastState) {
				return
			}
			newState := v.conn.GetState()
			if newState == connectivity.Idle {
				v.logger.Warn("Voicecontrol connection idle, stopping voice services")
				// TODO: Implement logic to stop voice services
				go v.handleReconnection()
			}
			lastState = newState
		}
	}()

	if err := v.registerSelf(); err != nil {
		return err
	}
	return v.establishStream()
}
```

- [ ] **Step 3: Cancel monitor in `Close`**

```go
func (v *VoiceControlClient) Close() error {
	if v.cancelMonitor != nil {
		v.cancelMonitor()
	}
	if v.stopc != nil {
		close(v.stopc)
	}
	if v.conn != nil {
		return v.conn.Close()
	}
	return nil
}
```

- [ ] **Step 4: Verify build**

Run: `cd D:/Projects/Vanguard/vngd-srs-server && go build ./...`
Expected: BUILD SUCCESS

---

### Task 4: Fix C-4 — Unsynchronized key cache

**Files:**
- Modify: `utils/auth.go`

- [ ] **Step 1: Add `sync` import and replace key cache with `sync.Once`**

Replace the package-level variable block and `getKeys` function in `utils/auth.go`:

```go
// BEFORE (lines 17-20):
var (
	privateKey *ecdsa.PrivateKey
	publicKey  *ecdsa.PublicKey
)

// AFTER:
var (
	keysOnce   sync.Once
	cachedPriv *ecdsa.PrivateKey
	cachedPub  *ecdsa.PublicKey
	keysErr    error
)
```

Note: `"sync"` is already not in the imports — add it to the import block.

- [ ] **Step 2: Rewrite `getKeys` to use `sync.Once`**

```go
func getKeys(privateKeyFile, publicKeyFile string) (*ecdsa.PrivateKey, *ecdsa.PublicKey, error) {
	keysOnce.Do(func() {
		cachedPriv, cachedPub, keysErr = generateKey(privateKeyFile, publicKeyFile)
	})
	return cachedPriv, cachedPub, keysErr
}
```

- [ ] **Step 3: Verify build**

Run: `cd D:/Projects/Vanguard/vngd-srs-server && go build ./...`
Expected: BUILD SUCCESS

---

### Task 5: Fix H-2 — Cleanup goroutine never stops

**Files:**
- Modify: `srs/srs_service.go`
- Modify: `control/server.go`

- [ ] **Step 1: Add `stopChan` field and `Stop` method to `SimpleRadioServer`**

In `srs/srs_service.go`, update the struct and constructor:

```go
type SimpleRadioServer struct {
	pb.UnimplementedSRSServiceServer
	logger        *slog.Logger
	mu            sync.Mutex
	serverState   *state.ServerState
	settingsState *state.SettingsState
	eventBus      *events.EventBus
	streams       map[uuid.UUID]grpc.ServerStreamingServer[pb.ServerUpdate]
	stopChan      chan struct{}
}

func NewSimpleRadioServer(serverState *state.ServerState, settingsState *state.SettingsState, logger *slog.Logger, bus *events.EventBus) *SimpleRadioServer {
	server := SimpleRadioServer{
		serverState:   serverState,
		settingsState: settingsState,
		eventBus:      bus,
		logger:        logger,
		mu:            sync.Mutex{},
		streams:       make(map[uuid.UUID]grpc.ServerStreamingServer[pb.ServerUpdate]),
		stopChan:      make(chan struct{}),
	}
	server.StartCleanupRoutine(time.Second*15, time.Minute*10)
	return &server
}
```

- [ ] **Step 2: Refactor `StartCleanupRoutine` to use ticker + stop channel**

Replace the existing `StartCleanupRoutine`:

```go
// StartCleanupRoutine launches a goroutine that periodically removes stale clients.
func (s *SimpleRadioServer) StartCleanupRoutine(interval time.Duration, staleAfter time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-s.stopChan:
				return
			case <-ticker.C:
				now := time.Now()
				for clientID, stream := range s.streams {
					s.serverState.RLock()
					client, exists := s.serverState.Clients[clientID]
					s.serverState.RUnlock()
					if !exists || now.Sub(client.LastUpdate) > staleAfter {
						if stream != nil {
							stream.Context().Done()
						}
						s.cleanupClientState(clientID)
						s.mu.Lock()
						delete(s.streams, clientID)
						s.mu.Unlock()
						s.logger.Info("Cleaned up stale client", "client_id", clientID)
					}
				}
			}
		}
	}()
}
```

- [ ] **Step 3: Add `Stop` method**

Add after `StartCleanupRoutine`:

```go
// Stop signals the cleanup goroutine to exit.
func (s *SimpleRadioServer) Stop() {
	close(s.stopChan)
}
```

- [ ] **Step 4: Store `srsServer` on `control.Server` and call `Stop` during shutdown**

In `control/server.go`, add the field to the `Server` struct:

```go
type Server struct {
	mu                sync.RWMutex
	clientGrpcServer  *grpc.Server
	controlGrpcServer *grpc.Server
	clientListener    net.Listener
	controlListener   net.Listener
	logger            *slog.Logger
	serverState       *state.ServerState
	settingsState     *state.SettingsState
	distributionState *state.DistributionState
	eventBus          *events.EventBus
	isRunning         bool
	stopOnce          sync.Once
	srsServer         *srs.SimpleRadioServer
}
```

In the `Start` method, store the reference (around line 105):

```go
	srsServer := srs.NewSimpleRadioServer(s.serverState, s.settingsState, s.logger, s.eventBus)
	s.srsServer = srsServer
```

In the `Stop` method, call `srsServer.Stop()` before `GracefulStop`:

```go
	s.stopOnce.Do(func() {
		if !s.IsRunning() {
			return
		}
		s.mu.Lock()
		s.isRunning = false
		s.mu.Unlock()

		s.logger.Info("Stopping gRPC server")

		if s.srsServer != nil {
			s.srsServer.Stop()
		}

		// GracefulStop will automatically close the clientListener
		if s.clientGrpcServer != nil {
			s.clientGrpcServer.GracefulStop()
			s.clientGrpcServer = nil
		}

		if s.controlGrpcServer != nil {
			s.controlGrpcServer.GracefulStop()
			s.controlGrpcServer = nil
		}

		s.clientListener = nil
		s.controlListener = nil
	})
```

- [ ] **Step 5: Verify build**

Run: `cd D:/Projects/Vanguard/vngd-srs-server && go build ./...`
Expected: BUILD SUCCESS

---

### Task 6: Fix H-3 — Fragile RLock/RUnlock in `handleFrontendEmits`

**Files:**
- Modify: `app/app.go:299-310`

- [ ] **Step 1: Rewrite `handleFrontendEmits`**

Replace the entire function:

```go
func (a *VCSApplication) handleFrontendEmits(channel chan events.Event) {
	a.DistributionState.RLock()
	isGUI := a.DistributionState.RuntimeMode == state.RuntimeModeGUI
	a.DistributionState.RUnlock()

	if !isGUI {
		return
	}
	for event := range channel {
		a.Logger.Debug("Received event from event bus", "name", event.Name)
		a.App.Event.EmitEvent(&application.CustomEvent{Name: event.Name, Data: event.Data})
	}
}
```

Remove `"fmt"` from the import block if it's no longer used anywhere else in the file. Check all usages — `fmt` is used by `fmt.Sprintf` in `server_control.go` but that's a different file. In `app.go`, search for other `fmt.` calls. If none remain after removing lines 301 and 306, remove the import.

- [ ] **Step 2: Verify build**

Run: `cd D:/Projects/Vanguard/vngd-srs-server && go build ./...`
Expected: BUILD SUCCESS

---

### Task 7: Fix H-7 — Lock held across Notify/EmitEvent in `clients.go`

**Files:**
- Modify: `app/clients.go`

- [ ] **Step 1: Rewrite `BanClient` to unlock before emitting**

Replace the entire `BanClient` method:

```go
func (a *VCSApplication) BanClient(clientId string, reason string) {
	a.ServerState.Lock()
	clientGuid, err := uuid.Parse(clientId)
	if err != nil {
		a.ServerState.Unlock()
		a.Notify(events.NewNotification("Ban failed", "Invalid client ID format", "error"))
		a.Logger.Error("Failed to parse client ID", "clientId", clientId, "error", err)
		return
	}
	client, ok := a.ServerState.Clients[clientGuid]
	if !ok {
		a.ServerState.Unlock()
		a.Notify(events.NewNotification("Ban failed", "Client not found", "error"))
		a.Logger.Error("Failed to ban client", "clientId", clientId, "reason", reason)
		return
	}
	_, alreadyBanned := utils.FindByFunc(a.ServerState.BannedState.BannedClients, func(bc state.BannedClient) bool {
		return bc.ID == clientGuid
	})
	if alreadyBanned {
		a.ServerState.Unlock()
		a.Notify(events.NewNotification("Ban failed", "Client is already banned", "error"))
		a.Logger.Error("Failed to ban client: already banned", "clientId", clientId)
		return
	}
	clientIp, ok := a.voiceServer.GetClientIPFromId(clientGuid)
	if !ok {
		a.ServerState.Unlock()
		a.Notify(events.NewNotification("Ban failed", "Client not found in voice server", "error"))
		a.Logger.Error("Failed to ban client: not found in voice server", "clientId", clientId)
		return
	}
	a.ServerState.BannedState.BannedClients = append(a.ServerState.BannedState.BannedClients, state.BannedClient{
		Name:      client.Name,
		IPAddress: clientIp.String(),
		Reason:    reason,
		ID:        clientGuid,
	})
	err = a.ServerState.BannedState.Save()
	if err != nil {
		a.ServerState.Unlock()
		a.Notify(events.NewNotification("Ban failed", "Failed to save banned clients", "error"))
		a.Logger.Error("Failed to save banned clients", "error", err)
		return
	}
	delete(a.ServerState.Clients, clientGuid)
	a.ServerState.Unlock()

	a.EmitEvent(events.Event{
		Name: events.ClientsChanged,
		Data: a.ServerState.Clients,
	})
	a.EmitEvent(events.Event{
		Name: events.BannedClientsChanged,
		Data: a.ServerState.BannedState.BannedClients,
	})
	a.Notify(events.NewNotification("Ban succeeded", "Client banned successfully", "success"))
	a.Logger.Info("Client banned", "clientId", clientId, "reason", reason)
}
```

- [ ] **Step 2: Rewrite `UnbanClient`**

```go
func (a *VCSApplication) UnbanClient(clientId string) {
	a.ServerState.Lock()
	clientGuid, err := uuid.Parse(clientId)
	if err != nil {
		a.ServerState.Unlock()
		a.Notify(events.NewNotification("Unban failed", "Invalid client ID format", "error"))
		a.Logger.Error("Failed to parse client ID", "clientId", clientId, "error", err)
		return
	}
	success := false
	for _, client := range a.ServerState.BannedState.BannedClients {
		if client.ID == clientGuid {
			a.ServerState.BannedState.BannedClients = utils.Remove(a.ServerState.BannedState.BannedClients, client)
			success = true
			break
		}
	}
	if !success {
		a.ServerState.Unlock()
		a.Notify(events.NewNotification("Unban failed", "Client not found", "error"))
		a.Logger.Error("Failed to unban client", "clientId", clientId)
		return
	}
	err = a.ServerState.BannedState.Save()
	if err != nil {
		a.ServerState.Unlock()
		a.Notify(events.NewNotification("Unban failed", "Failed to save banned clients", "error"))
		a.Logger.Error("Failed to save banned clients", "error", err)
		return
	}
	a.ServerState.Unlock()

	a.EmitEvent(events.Event{
		Name: events.BannedClientsChanged,
		Data: a.ServerState.BannedState.BannedClients,
	})
	a.Notify(events.NewNotification("Unban succeeded", "Client successfully unbanned", "success"))
}
```

- [ ] **Step 3: Rewrite `KickClient`**

```go
func (a *VCSApplication) KickClient(clientId string, reason string) { // TODO: Implement Backend Logic to kick a client
	a.ServerState.Lock()
	clientGuid, err := uuid.Parse(clientId)
	if err != nil {
		a.ServerState.Unlock()
		a.Notify(events.NewNotification("Kick failed", "Invalid client ID format", "error"))
		a.Logger.Error("Failed to parse client ID", "clientId", clientId, "error", err)
		return
	}
	delete(a.ServerState.Clients, clientGuid)
	a.ServerState.Unlock()

	a.EmitEvent(events.Event{
		Name: events.ClientsChanged,
		Data: a.ServerState.Clients,
	})
	a.Notify(events.NewNotification("Kick succeeded", "Client kicked successfully", "success"))
	a.Logger.Info("Client kicked", "clientId", clientId, "reason", reason)
}
```

- [ ] **Step 4: Rewrite `MuteClient`**

```go
func (a *VCSApplication) MuteClient(clientId string) { // TODO: Implement Backend Logic to mute a client and notify the Client
	a.ServerState.Lock()
	clientGuid, err := uuid.Parse(clientId)
	if err != nil {
		a.ServerState.Unlock()
		a.Notify(events.NewNotification("Mute failed", "Invalid client ID format", "error"))
		a.Logger.Error("Failed to parse client ID", "clientId", clientId, "error", err)
		return
	}
	client, ok := a.ServerState.RadioClients[clientGuid]
	if !ok {
		a.ServerState.Unlock()
		a.Notify(events.NewNotification("Mute failed", "Client not found", "error"))
		a.Logger.Error("Failed to mute client", "clientId", clientId)
		return
	}
	client.Muted = true
	a.ServerState.RadioClients[clientGuid] = client
	a.ServerState.Unlock()

	a.EmitEvent(events.Event{
		Name: events.RadioClientsChanged,
		Data: a.ServerState.RadioClients,
	})
	a.Notify(events.NewNotification("Mute succeeded", "Client muted successfully", "success"))
	a.Logger.Info("Client muted", "clientId", clientId)
}
```

- [ ] **Step 5: Rewrite `UnmuteClient`**

```go
func (a *VCSApplication) UnmuteClient(clientId string) { // TODO: Implement Backend Logic to unmute a client and notify the Client
	a.ServerState.Lock()
	clientGuid, err := uuid.Parse(clientId)
	if err != nil {
		a.ServerState.Unlock()
		a.Notify(events.NewNotification("Unmute failed", "Invalid client ID format", "error"))
		a.Logger.Error("Failed to parse client ID", "clientId", clientId, "error", err)
		return
	}
	client, ok := a.ServerState.RadioClients[clientGuid]
	if !ok {
		a.ServerState.Unlock()
		a.Notify(events.NewNotification("Unmute failed", "Client not found", "error"))
		a.Logger.Error("Failed to unmute client", "clientId", clientId)
		return
	}
	client.Muted = false
	a.ServerState.RadioClients[clientGuid] = client
	a.ServerState.Unlock()

	a.EmitEvent(events.Event{
		Name: events.RadioClientsChanged,
		Data: a.ServerState.RadioClients,
	})
	a.Notify(events.NewNotification("Unmute succeeded", "Client unmuted successfully", "success"))
	a.Logger.Info("Client unmuted", "clientId", clientId)
}
```

- [ ] **Step 6: Rewrite `IsClientMuted`**

```go
func (a *VCSApplication) IsClientMuted(clientId string) bool {
	failedEvent := events.NewNotification("Check Mute Status Failed", "Failed to check if client is muted or not", "error")
	a.ServerState.RLock()
	clientGuid, err := uuid.Parse(clientId)
	if err != nil {
		a.ServerState.RUnlock()
		a.Notify(failedEvent)
		a.Logger.Error("Failed to parse client ID", "clientId", clientId, "error", err)
		return false
	}
	client, ok := a.ServerState.RadioClients[clientGuid]
	if !ok {
		a.ServerState.RUnlock()
		a.Notify(failedEvent)
		a.Logger.Error("Failed to check if client is muted", "clientId", clientId)
		return false
	}
	muted := client.Muted
	a.ServerState.RUnlock()

	return muted
}
```

Note: `IsClientMuted` is read-only so it uses `RLock`/`RUnlock`. The original used `Lock` which was unnecessarily exclusive.

- [ ] **Step 7: Verify build**

Run: `cd D:/Projects/Vanguard/vngd-srs-server && go build ./...`
Expected: BUILD SUCCESS

---

### Task 8: Fix M-9 — Re-entrant mutex in `startVoiceServer`

**Files:**
- Modify: `app/server_control.go:121-155`

- [ ] **Step 1: Rewrite `startVoiceServer` to read settings before goroutine**

Replace the entire `startVoiceServer` method:

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

- [ ] **Step 2: Verify build**

Run: `cd D:/Projects/Vanguard/vngd-srs-server && go build ./...`
Expected: BUILD SUCCESS

---

### Task 9: Commit Theme 1

- [ ] **Step 1: Run tests and vet**

Run: `cd D:/Projects/Vanguard/vngd-srs-server && go vet ./... && go build -race ./... && go test ./...`
Expected: All pass

- [ ] **Step 2: Commit**

```bash
cd D:/Projects/Vanguard/vngd-srs-server
git add srs/auth_service.go srs/plugin_client.go voiceontrol/client.go utils/auth.go srs/srs_service.go app/app.go app/clients.go app/server_control.go control/server.go
git commit -m "fix: concurrency safety — data races, goroutine leaks, lock ordering

- C-2: Change RLock to Lock for map writes in StartAuth
- C-3: Add cancellable context to connection monitor goroutines
- C-4: Use sync.Once for key cache initialization
- H-2: Add stop channel to cleanup goroutine, store srsServer ref
- H-3: Fix handleFrontendEmits lock/unlock symmetry
- H-7: Release ServerState lock before calling Notify/EmitEvent
- M-9: Remove re-entrant mutex in startVoiceServer"
```

---

## Theme 2: API Correctness

### Task 10: Fix H-1 — Bare string context key

**Files:**
- Create: `utils/context.go`
- Modify: `control/server.go:320`

- [ ] **Step 1: Create `utils/context.go`**

```go
package utils

// ContextKey is the type used for context value keys to avoid collisions.
type ContextKey string

// ClientIDKey is the context key for the authenticated client's GUID.
const ClientIDKey ContextKey = "client_id"
```

- [ ] **Step 2: Update `authInterceptor` in `control/server.go`**

At line 320, change:

```go
// BEFORE:
	ctx = context.WithValue(ctx, "client_id", claims.ClientGuid)

// AFTER:
	ctx = context.WithValue(ctx, utils.ClientIDKey, claims.ClientGuid)
```

- [ ] **Step 3: Verify build**

Run: `cd D:/Projects/Vanguard/vngd-srs-server && go build ./...`
Expected: BUILD SUCCESS

---

### Task 11: Fix H-5 — `Remove` mutates caller's backing array

**Files:**
- Modify: `utils/slice.go`

- [ ] **Step 1: Replace `Remove` with immutable version and delete `indexOf`**

Replace the entire file content:

```go
package utils

func Remove[K comparable](s []K, e K) []K {
	result := make([]K, 0, len(s))
	for _, v := range s {
		if v != e {
			result = append(result, v)
		}
	}
	return result
}

func FindByFunc[K any](s []K, f func(K) bool) (K, bool) {
	var zero K
	for _, v := range s {
		if f(v) {
			return v, true
		}
	}
	return zero, false
}
```

- [ ] **Step 2: Verify build**

Run: `cd D:/Projects/Vanguard/vngd-srs-server && go build ./...`
Expected: BUILD SUCCESS

---

### Task 12: Fix H-6 — Wrong lock in `initControlServer`

**Files:**
- Modify: `control/server.go:140-143`

- [ ] **Step 1: Fix the lock**

```go
// BEFORE (lines 140-143):
func (s *Server) initControlServer(controlServer voicecontrolpb.VoiceControlServiceServer) {
	s.serverState.RLock()
	privateKeyFileName := s.settingsState.VoiceControl.PrivateKeyFile
	certificateFileName := s.settingsState.VoiceControl.CertificateFile
	s.serverState.RUnlock()

// AFTER:
func (s *Server) initControlServer(controlServer voicecontrolpb.VoiceControlServiceServer) {
	s.settingsState.RLock()
	privateKeyFileName := s.settingsState.VoiceControl.PrivateKeyFile
	certificateFileName := s.settingsState.VoiceControl.CertificateFile
	s.settingsState.RUnlock()
```

- [ ] **Step 2: Verify build**

Run: `cd D:/Projects/Vanguard/vngd-srs-server && go build ./...`
Expected: BUILD SUCCESS

---

### Task 13: Fix M-6 — `GetSettings` returns pointer after releasing lock

**Files:**
- Modify: `app/settings.go:9-13`

- [ ] **Step 1: Return a value copy with RLock**

```go
// BEFORE:
func (a *VCSApplication) GetSettings() *state.SettingsState {
	a.SettingsState.Lock()
	defer a.SettingsState.Unlock()
	return a.SettingsState
}

// AFTER:
func (a *VCSApplication) GetSettings() *state.SettingsState {
	a.SettingsState.RLock()
	defer a.SettingsState.RUnlock()
	copy := *a.SettingsState
	return &copy
}
```

Note: We keep the pointer return type to avoid breaking Wails-generated TypeScript bindings. Instead we return a pointer to a new copy allocated on the stack (escapes to heap due to return).

- [ ] **Step 2: Verify build**

Run: `cd D:/Projects/Vanguard/vngd-srs-server && go build ./...`
Expected: BUILD SUCCESS

---

### Task 14: Fix M-7 — UDP ban check uses wrong address

**Files:**
- Modify: `voice/server.go:109-119`

- [ ] **Step 1: Fix the ban check to use `remoteAddr`**

In the `Listen` method, replace the ban check block:

```go
// BEFORE (lines 109-119):
			v.serverState.RLock()
			_, banned := utils.FindByFunc(v.serverState.BannedState.BannedClients, func(bc state.BannedClient) bool {
				if bc.IPAddress == conn.RemoteAddr().String() {
					return true
				}
				return false
			})
			v.serverState.RUnlock()
			if banned {
				v.logger.Warn("Banned client attempted to initialize", "IP", conn.RemoteAddr().String())
				continue
			}

// AFTER:
			v.serverState.RLock()
			_, banned := utils.FindByFunc(v.serverState.BannedState.BannedClients, func(bc state.BannedClient) bool {
				return bc.IPAddress == remoteAddr.IP.String()
			})
			v.serverState.RUnlock()
			if banned {
				v.logger.Warn("Banned client attempted to connect", "IP", remoteAddr.IP.String())
				continue
			}
```

- [ ] **Step 2: Verify build**

Run: `cd D:/Projects/Vanguard/vngd-srs-server && go build ./...`
Expected: BUILD SUCCESS

---

### Task 15: Commit Theme 2

- [ ] **Step 1: Run tests and vet**

Run: `cd D:/Projects/Vanguard/vngd-srs-server && go vet ./... && go build -race ./... && go test ./...`
Expected: All pass

- [ ] **Step 2: Commit**

```bash
cd D:/Projects/Vanguard/vngd-srs-server
git add utils/context.go utils/slice.go control/server.go app/settings.go voice/server.go
git commit -m "fix: API correctness — context key type, immutable slice, lock targets

- H-1: Use typed ContextKey instead of bare string for context values
- H-5: Make Remove() non-mutating (new slice allocation)
- H-6: Fix initControlServer to lock settingsState, not serverState
- M-6: GetSettings returns a copy instead of live pointer
- M-7: UDP ban check uses packet remoteAddr instead of conn local addr"
```

---

## Theme 3: Error Handling & Safety

### Task 16: Fix C-1 — Unguarded type assertion on context value

**Files:**
- Modify: `srs/srs_service.go`

- [ ] **Step 1: Add `clientIDFromContext` helper**

Add this function near the top of `srs/srs_service.go` (after the imports):

```go
func clientIDFromContext(ctx context.Context) (uuid.UUID, error) {
	rawID, ok := ctx.Value(utils.ClientIDKey).(string)
	if !ok || rawID == "" {
		return uuid.Nil, fmt.Errorf("missing client id in context")
	}
	return uuid.Parse(rawID)
}
```

Add `"github.com/FPGSchiba/vcs-srs-server/utils"` to the imports if not already present.

- [ ] **Step 2: Replace all four call sites**

**Line 111** (`Disconnect`):
```go
// BEFORE:
	clientID, err := uuid.Parse(ctx.Value("client_id").(string))

// AFTER:
	clientID, err := clientIDFromContext(ctx)
```

**Line 147** (`UpdateClientInfo`):
```go
// BEFORE:
	clientID, err := uuid.Parse(ctx.Value("client_id").(string))

// AFTER:
	clientID, err := clientIDFromContext(ctx)
```

**Line 231** (`UpdateRadioInfo`):
```go
// BEFORE:
	clientID, err := uuid.Parse(ctx.Value("client_id").(string))

// AFTER:
	clientID, err := clientIDFromContext(ctx)
```

**Line 268** (`SubscribeToUpdates`):
```go
// BEFORE:
	clientID, err := uuid.Parse(stream.Context().Value("client_id").(string))

// AFTER:
	clientID, err := clientIDFromContext(stream.Context())
```

- [ ] **Step 3: Verify build**

Run: `cd D:/Projects/Vanguard/vngd-srs-server && go build ./...`
Expected: BUILD SUCCESS

---

### Task 17: Fix H-4 — `GuestLogin` lock/pointer issues

**Files:**
- Modify: `srs/auth_service.go:300-316`

- [ ] **Step 1: Replace the coalition search block**

Find the block starting at line 300 (`// Check Password > Select coalition`) and replace through line 316:

```go
// BEFORE (lines 300-316):
	// Check Password > Select coalition
	s.mu.Lock()
	var selectedCoalition *state.Coalition
	for _, coalition := range s.settingsState.Coalitions {
		if utils.CheckPasswordHash(coalition.Password, request.Password) {
			s.mu.Unlock()
			selectedCoalition = &coalition
			break
		}
	}

	if selectedCoalition == nil {
		return &pb.GuestLoginResponse{
			Success:     false,
			LoginResult: &pb.GuestLoginResponse_ErrorMessage{ErrorMessage: "No Coalition found with that password"},
		}, nil
	}

// AFTER:
	// Check Password > Select coalition
	s.settingsState.RLock()
	var selectedCoalition state.Coalition
	var coalitionFound bool
	for _, coalition := range s.settingsState.Coalitions {
		if utils.CheckPasswordHash(coalition.Password, request.Password) {
			selectedCoalition = coalition
			coalitionFound = true
			break
		}
	}
	s.settingsState.RUnlock()

	if !coalitionFound {
		return &pb.GuestLoginResponse{
			Success:     false,
			LoginResult: &pb.GuestLoginResponse_ErrorMessage{ErrorMessage: "No coalition found with that password"},
		}, nil
	}
```

- [ ] **Step 2: Update all references from pointer to value**

After the change above, `selectedCoalition` is now a `state.Coalition` value, not `*state.Coalition`. Update the remaining references in `GuestLogin`:

Line ~330 (AddClient): `selectedCoalition.Name` — no change needed (field access is the same for value and pointer).

Line ~353 (log message): `selectedCoalition.Name` — no change needed.

Line ~362 (response): `selectedCoalition.Name` — no change needed.

All field accesses work the same way for values and pointers, so no further changes are needed.

- [ ] **Step 3: Verify build**

Run: `cd D:/Projects/Vanguard/vngd-srs-server && go build ./...`
Expected: BUILD SUCCESS

---

### Task 18: Fix M-4 — `pem.Decode` nil block dereference

**Files:**
- Modify: `utils/auth.go:129-145`

- [ ] **Step 1: Add nil checks in `decode`**

Replace the `decode` function:

```go
func decode(pemEncoded string, pemEncodedPub string) (*ecdsa.PrivateKey, *ecdsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(pemEncoded))
	if block == nil {
		return nil, nil, fmt.Errorf("failed to decode private key PEM")
	}
	x509Encoded := block.Bytes
	privateKey, err := x509.ParseECPrivateKey(x509Encoded)
	if err != nil {
		return nil, nil, err
	}

	blockPub, _ := pem.Decode([]byte(pemEncodedPub))
	if blockPub == nil {
		return nil, nil, fmt.Errorf("failed to decode public key PEM")
	}
	x509EncodedPub := blockPub.Bytes
	genericPublicKey, err := x509.ParsePKIXPublicKey(x509EncodedPub)
	if err != nil {
		return nil, nil, err
	}
	publicKey := genericPublicKey.(*ecdsa.PublicKey)

	return privateKey, publicKey, nil
}
```

Add `"fmt"` to imports if not already present (it should be from `GenerateToken`).

- [ ] **Step 2: Verify build**

Run: `cd D:/Projects/Vanguard/vngd-srs-server && go build ./...`
Expected: BUILD SUCCESS

---

### Task 19: Fix M-5 — `ensureBanFileExists` error handling

**Files:**
- Modify: `state/server.go:53-63`

- [ ] **Step 1: Fix the error handling**

Replace the `ensureBanFileExists` function:

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

- [ ] **Step 2: Verify build**

Run: `cd D:/Projects/Vanguard/vngd-srs-server && go build ./...`
Expected: BUILD SUCCESS

---

### Task 20: Fix M-8 — Stream assigned before error check

**Files:**
- Modify: `voiceontrol/client.go:138-148`

- [ ] **Step 1: Move stream assignment after error check**

In `establishStream`, reorder the assignment:

```go
// BEFORE (lines 139-140):
	stream, err := v.client.EstablishStream(context.Background())
	v.stream = stream
	if err != nil {

// AFTER:
	stream, err := v.client.EstablishStream(context.Background())
	if err != nil {
		st, ok := status.FromError(err)
		if ok && (st.Code() == codes.Unavailable || st.Code() == codes.DeadlineExceeded) {
			go v.handleReconnection()
			return fmt.Errorf("temporary connection issue: %v", err)
		}
		return fmt.Errorf("failed to establish stream: %v", err)
	}
	v.stream = stream
```

Remove the duplicate error handling block that was after `v.stream = stream` (lines 141-147 in the original). The full replacement of the function's top portion:

```go
func (v *VoiceControlClient) establishStream() error {
	stream, err := v.client.EstablishStream(context.Background())
	if err != nil {
		st, ok := status.FromError(err)
		if ok && (st.Code() == codes.Unavailable || st.Code() == codes.DeadlineExceeded) {
			go v.handleReconnection()
			return fmt.Errorf("temporary connection issue: %v", err)
		}
		return fmt.Errorf("failed to establish stream: %v", err)
	}
	v.stream = stream
	if v.stopc == nil {
		v.stopc = make(chan struct{})
	}
```

The rest of the function (the goroutine and return) stays the same.

- [ ] **Step 2: Verify build**

Run: `cd D:/Projects/Vanguard/vngd-srs-server && go build ./...`
Expected: BUILD SUCCESS

---

### Task 21: Commit Theme 3

- [ ] **Step 1: Run tests and vet**

Run: `cd D:/Projects/Vanguard/vngd-srs-server && go vet ./... && go build -race ./... && go test ./...`
Expected: All pass

- [ ] **Step 2: Commit**

```bash
cd D:/Projects/Vanguard/vngd-srs-server
git add srs/srs_service.go srs/auth_service.go utils/auth.go state/server.go voiceontrol/client.go
git commit -m "fix: error handling — type assertion safety, PEM nil checks, lock correctness

- C-1: Safe context value extraction with clientIDFromContext helper
- H-4: GuestLogin uses correct lock (settingsState) and value copy
- M-4: Nil check after pem.Decode before dereferencing
- M-5: ensureBanFileExists handles non-ErrNotExist errors
- M-8: Assign stream after error check in establishStream"
```

---

## Theme 4: Code Quality

### Task 22: Fix M-1 — `fmt.Println` in production code

**Files:**
- Modify: `app/app.go`

- [ ] **Step 1: This was already fixed in Task 6 (H-3)**

The `handleFrontendEmits` rewrite in Task 6 already replaced both `fmt.Println` calls with `a.Logger.Debug(...)`. Verify `fmt` import is still needed in `app/app.go` — if not, remove it.

Check: search for `fmt.` in `app/app.go`. If no other usages exist after the H-3 fix, remove `"fmt"` from imports.

---

### Task 23: Fix M-3 — Regex recompiled on every call

**Files:**
- Modify: `srs/utils.go:22-26`

- [ ] **Step 1: Precompile the regex at package level**

```go
// BEFORE (lines 22-26):
func checkUnitId(unitId string) bool {
	re := `^[A-Z0-9]{2,4}$`
	matched, _ := regexp.MatchString(re, unitId)
	return matched
}

// AFTER:
var unitIDRegex = regexp.MustCompile(`^[A-Z0-9]{2,4}$`)

func checkUnitId(unitId string) bool {
	return unitIDRegex.MatchString(unitId)
}
```

- [ ] **Step 2: Verify build**

Run: `cd D:/Projects/Vanguard/vngd-srs-server && go build ./...`
Expected: BUILD SUCCESS

---

### Task 24: Fix M-2 — Capitalised error strings (light touch)

**Files:**
- Only files already modified in previous themes

- [ ] **Step 1: Scan and fix capitalised error strings**

Search all modified files for `fmt.Errorf` and `errors.New` with capitalised first letters. Only fix internal-facing error strings (not user-facing gRPC response messages).

Known instances to fix:

In `utils/auth.go`:
```go
// BEFORE:
return nil, nil, errors.New("public key is nil")
// AFTER (already lowercase, OK)

// BEFORE:
return nil, nil, errors.New("private key or public key could not be written to file")
// AFTER (already lowercase, OK)
```

In `srs/plugin_client.go`:
```go
// BEFORE:
return fmt.Errorf("client is not initialized")
// AFTER (already lowercase, OK)
```

Most error strings in the codebase are already lowercase. If you find any capitalised `fmt.Errorf`/`errors.New` in the modified files, lowercase the first letter.

- [ ] **Step 2: Verify build**

Run: `cd D:/Projects/Vanguard/vngd-srs-server && go build ./...`
Expected: BUILD SUCCESS

---

### Task 25: Commit Theme 4

- [ ] **Step 1: Run tests and vet**

Run: `cd D:/Projects/Vanguard/vngd-srs-server && go vet ./... && go build -race ./... && go test ./...`
Expected: All pass

- [ ] **Step 2: Commit**

```bash
cd D:/Projects/Vanguard/vngd-srs-server
git add app/app.go srs/utils.go
git commit -m "fix: code quality — precompile regex, replace fmt.Println with logger

- M-1: Replace fmt.Println with structured logger calls
- M-2: Lowercase internal error strings where needed
- M-3: Precompile unitID regex at package level"
```

---

## Final Verification

### Task 26: Full verification pass

- [ ] **Step 1: Run complete verification suite**

```bash
cd D:/Projects/Vanguard/vngd-srs-server
go vet ./...
go build -race ./...
go test -race ./...
```

Expected: All pass with no warnings.

- [ ] **Step 2: Verify git log shows 4 clean commits**

```bash
git log --oneline -4
```

Expected output (most recent first):
```
<hash> fix: code quality — precompile regex, replace fmt.Println with logger
<hash> fix: error handling — type assertion safety, PEM nil checks, lock correctness
<hash> fix: API correctness — context key type, immutable slice, lock targets
<hash> fix: concurrency safety — data races, goroutine leaks, lock ordering
```
