# Go Code Review Round 2 — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix all 24 issues from the go-review round 2 audit — 8 CRITICAL, 7 HIGH, 9 MEDIUM — in 5 themed commits.

**Architecture:** Fixes are grouped by theme (voice concurrency → event bus → shared state → auth/security → code quality). Each theme produces one self-contained commit. All tests use the standard `testing` package with the race detector (`go test -race ./...`).

**Tech Stack:** Go 1.25, `google.golang.org/grpc`, `github.com/golang-jwt/jwt/v5`, `golang.org/x/crypto/bcrypt`, standard `crypto/tls`, `crypto/x509`

---

## File Map

| File | Action | Theme |
|------|--------|-------|
| `voice/server.go` | Modify | 1 |
| `voice/server_test.go` | Modify | 1 |
| `events/bus.go` | Modify | 2 |
| `events/bus_test.go` | Create | 2 |
| `srs/srs_service.go` | Modify | 2, 3 |
| `state/snapshot.go` | Create | 3 |
| `app/clients.go` | Modify | 3 |
| `app/settings.go` | Modify | 3 |
| `app/app.go` | Modify | 3, 5 |
| `app/server_control.go` | Modify | 3 |
| `voiceontrol/client.go` | Modify | 3, 5 |
| `utils/auth.go` | Modify | 4, 5 |
| `utils/auth_test.go` | Create | 4 |
| `utils/general.go` | Modify | 5 |
| `srs/auth_service.go` | Modify | 4 |
| `srs/plugin_client.go` | Modify | 4 |
| `state/settings.go` | Modify | 4 |
| `control/server.go` | Modify | 5 |

---

## Task 1: Voice Server Concurrency

**Issues:** CRIT-1, CRIT-2, CRIT-3, MED-8
**Files:**
- Modify: `voice/server.go`
- Modify: `voice/server_test.go`

---

- [ ] **Step 1.1: Write failing tests for DisconnectClient and Stop idempotency**

Open `voice/server_test.go` and replace the empty file with:

```go
package voice

import (
	"net"
	"testing"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/google/uuid"
	"log/slog"
	"os"
)

func newTestServer() *Server {
	return NewServer(
		&state.ServerState{},
		slog.New(slog.NewTextHandler(os.Stderr, nil)),
		&state.DistributionState{},
		&state.SettingsState{},
	)
}

// TestDisconnectClientIdempotent verifies that calling DisconnectClient twice
// on the same ID does not panic and the client is removed after the first call.
func TestDisconnectClientIdempotent(t *testing.T) {
	s := newTestServer()
	id := uuid.New()
	s.clients[id] = &Client{
		Addr:     &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5002},
		LastSeen: time.Now(),
	}

	// First disconnect should remove the client.
	s.DisconnectClient(id)
	if _, exists := s.clients[id]; exists {
		t.Fatal("client should have been removed after first disconnect")
	}

	// Second disconnect should not panic.
	s.DisconnectClient(id)
}

// TestStopIdempotent verifies that calling Stop twice does not panic.
func TestStopIdempotent(t *testing.T) {
	s := newTestServer()
	s.running = true

	if err := s.Stop(); err != nil {
		t.Fatalf("first Stop returned error: %v", err)
	}
	// Second call must not panic (covers CRIT-3 stopOnce fix).
	if err := s.Stop(); err != nil {
		t.Fatalf("second Stop returned error: %v", err)
	}
}

// TestHandleKeepaliveRace verifies no data race when a keepalive arrives for a
// client that is simultaneously removed by cleanup. Run with -race.
func TestHandleKeepaliveRace(t *testing.T) {
	s := newTestServer()
	id := uuid.New()
	s.clients[id] = &Client{
		Addr:     &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5002},
		LastSeen: time.Now().Add(-2 * time.Minute), // stale
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Simulate cleanup removing the client concurrently.
		s.Lock()
		delete(s.clients, id)
		s.Unlock()
	}()

	pkt := &VCSPacket{SenderID: id}
	s.handleKeepalivePacket(pkt, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5002})
	<-done
}
```

- [ ] **Step 1.2: Run tests to confirm they fail (or race)**

```bash
cd D:/Projects/Vanguard/vngd-srs-server && go test -race ./voice/... -v -run "TestDisconnectClientIdempotent|TestStopIdempotent|TestHandleKeepaliveRace"
```

Expected: `TestDisconnectClientIdempotent` passes (CRIT-2 is benign for this specific test, but confirms behaviour), `TestStopIdempotent` panics on second `Stop` (CRIT-3), and `TestHandleKeepaliveRace` reports a DATA RACE with `-race`.

- [ ] **Step 1.3: Fix CRIT-1 — replace RLock→Lock upgrades in all three packet handlers**

In `voice/server.go`, replace `handleKeepalivePacket`:

```go
func (v *Server) handleKeepalivePacket(packet *VCSPacket, addr *net.UDPAddr) {
	v.Lock()
	client, exists := v.clients[packet.SenderID]
	if exists {
		client.LastSeen = time.Now()
	}
	v.Unlock()
	if !exists {
		v.logger.Warn("Received keepalive from unknown client", "sender_id", packet.SenderID)
		return
	}
	v.logger.Debug("Updated last seen for client", "sender_id", packet.SenderID, "addr", addr.String())
	ackPacket := NewVCSKeepalivePacket(packet.SenderID)
	ackData := ackPacket.SerializePacket()
	_, err := v.conn.WriteToUDP(ackData, addr)
	if err != nil {
		v.logger.Error("Failed to send keepalive acknowledgment",
			"to", addr.String(),
			"error", err)
	}
}
```

Replace `handleVoicePacket` (the read+update portion):

```go
func (v *Server) handleVoicePacket(packet *VCSPacket) {
	if v.settingsState.IsFrequencyTest(packet.FrequencyAsFloat32()) {
		v.handleTestFrequencyPacket(packet)
		return
	}

	v.Lock()
	client, exists := v.clients[packet.SenderID]
	if exists {
		client.LastSeen = time.Now()
	}
	v.Unlock()
	if !exists {
		v.logger.Warn("Received voice packet from unknown client", "sender_id", packet.SenderID)
		return
	}

	if len(packet.Payload) > 5 {
		v.broadcastVoice(packet, packet.SenderID)
	}

	v.logger.Debug("Received voice packet",
		"sender_id", packet.SenderID,
		"frequency", packet.FrequencyAsFloat32(),
		"size", len(packet.Payload))
}
```

Replace `handleTestFrequencyPacket` (the read+use portion):

```go
func (v *Server) handleTestFrequencyPacket(packet *VCSPacket) {
	v.Lock()
	client, exists := v.clients[packet.SenderID]
	if exists {
		client.LastSeen = time.Now()
	}
	v.Unlock()
	if !exists || client == nil {
		v.logger.Warn("Test frequency from unknown client", "sender_id", packet.SenderID)
		return
	}

	if v.conn == nil {
		v.logger.Warn("No UDP connection available to echo test packet")
		return
	}

	// Capture addr while we still have the pointer (safe — Client.Addr is never mutated after creation).
	addr := client.Addr
	_, err := v.conn.WriteToUDP(packet.SerializePacket(), addr)
	if err != nil {
		v.logger.Error("Failed to echo test frequency packet to client", "to", addr.String(), "error", err)
		return
	}
	v.logger.Debug("Echoed test frequency packet to client", "to", addr.String(), "sender_id", packet.SenderID)
}
```

- [ ] **Step 1.4: Fix CRIT-2 — DisconnectClient acquires Lock directly**

Replace `DisconnectClient` in `voice/server.go`:

```go
func (v *Server) DisconnectClient(clientID uuid.UUID) {
	v.Lock()
	client, exists := v.clients[clientID]
	if exists {
		delete(v.clients, clientID)
	}
	v.Unlock()
	if exists {
		v.logger.Info("Disconnected voice client",
			"id", clientID,
			"addr", client.Addr.String())
	}
}
```

- [ ] **Step 1.5: Fix CRIT-3 — guard stopChan close with sync.Once**

Add `stopOnce sync.Once` to the `Server` struct:

```go
type Server struct {
	sync.RWMutex
	conn              *net.UDPConn
	clients           map[uuid.UUID]*Client
	serverState       *state.ServerState
	settingsState     *state.SettingsState
	distributionState *state.DistributionState
	logger            *slog.Logger
	running           bool
	stopChan          chan struct{}
	stopOnce          sync.Once
	controlClient     *voiceontrol.VoiceControlClient
	serverId          string
}
```

Update `Stop()` to use `stopOnce`:

```go
func (v *Server) Stop() error {
	v.Lock()
	defer v.Unlock()

	if !v.running {
		return nil
	}

	v.stopOnce.Do(func() { close(v.stopChan) })

	if v.conn != nil {
		err := v.conn.Close()
		if err != nil {
			return err
		}
	}

	v.running = false
	v.logger.Info("Voice server stopped")
	return nil
}
```

- [ ] **Step 1.6: Fix MED-8 — copy UDP buffer before goroutine dispatch**

In the `Listen` main receive loop, replace:

```go
go v.handlePacket(buffer[:n], remoteAddr)
```

With:

```go
pkt := make([]byte, n)
copy(pkt, buffer[:n])
go v.handlePacket(pkt, remoteAddr)
```

- [ ] **Step 1.7: Run tests to confirm they pass**

```bash
cd D:/Projects/Vanguard/vngd-srs-server && go test -race ./voice/... -v -run "TestDisconnectClientIdempotent|TestStopIdempotent|TestHandleKeepaliveRace"
```

Expected: all three tests PASS with no race reports.

- [ ] **Step 1.8: Verify build is clean**

```bash
cd D:/Projects/Vanguard/vngd-srs-server && go vet ./... && go build -race ./...
```

Expected: no errors.

- [ ] **Step 1.9: Commit**

```bash
cd D:/Projects/Vanguard/vngd-srs-server && git add voice/server.go voice/server_test.go && git commit -m "fix: voice server concurrency — RLock/Lock upgrade, DisconnectClient, stopOnce, buffer copy"
```

---

## Task 2: Event Bus & Streaming

**Issues:** HIGH-4, HIGH-5, HIGH-6
**Files:**
- Modify: `events/bus.go`
- Create: `events/bus_test.go`
- Modify: `srs/srs_service.go`

---

- [ ] **Step 2.1: Write failing tests for EventBus**

Create `events/bus_test.go`:

```go
package events

import (
	"testing"
	"time"
)

// TestEventBusDelivers verifies that a published event reaches a subscriber.
func TestEventBusDelivers(t *testing.T) {
	eb := NewEventBus()
	defer eb.Stop()

	ch := eb.Subscribe("test/event")

	eb.Publish(Event{Name: "test/event", Data: "hello"})

	select {
	case evt := <-ch:
		if evt.Name != "test/event" {
			t.Fatalf("expected test/event, got %s", evt.Name)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for event")
	}
}

// TestEventBusStop verifies that Stop shuts down the publisher goroutine.
// If Stop leaks, this test would hang because the subscriber channel would
// never be closed. We verify by confirming no goroutine blocks after Stop.
func TestEventBusStop(t *testing.T) {
	eb := NewEventBus()
	eb.Stop()
	// Second Stop must not panic (once.Do protection).
	eb.Stop()
}

// TestEventBusUnsubscribe verifies that Unsubscribe removes the channel from dispatch.
func TestEventBusUnsubscribe(t *testing.T) {
	eb := NewEventBus()
	defer eb.Stop()

	ch := eb.Subscribe("test/unsub")
	eb.Unsubscribe(ch)

	eb.Publish(Event{Name: "test/unsub", Data: "ignored"})

	select {
	case <-ch:
		t.Fatal("received event on unsubscribed channel")
	case <-time.After(100 * time.Millisecond):
		// Expected: no event delivered to the removed channel.
	}
}

// TestEventBusWildcard verifies that "*" subscribers receive all events.
func TestEventBusWildcard(t *testing.T) {
	eb := NewEventBus()
	defer eb.Stop()

	ch := eb.Subscribe("*")
	eb.Publish(Event{Name: "anything", Data: nil})

	select {
	case evt := <-ch:
		if evt.Name != "anything" {
			t.Fatalf("expected anything, got %s", evt.Name)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for wildcard event")
	}
}
```

- [ ] **Step 2.2: Run tests to confirm they fail**

```bash
cd D:/Projects/Vanguard/vngd-srs-server && go test -race ./events/... -v
```

Expected: `TestEventBusUnsubscribe` and `TestEventBusStop` FAIL because `Unsubscribe` and `Stop` don't exist yet.

- [ ] **Step 2.3: Rewrite events/bus.go**

Replace the entire file:

```go
package events

import "sync"

// EventBus dispatches events to registered subscribers via a buffered channel.
// It replaces the old slice-based queue with a channel so the publisher goroutine
// blocks instead of busy-polling, and supports clean shutdown via Stop.
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

// startPublisher dispatches events from the queue until Stop is called.
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

// dispatch sends the event to all matching and wildcard subscribers.
func (eb *EventBus) dispatch(event Event) {
	eb.mu.RLock()
	defer eb.mu.RUnlock()
	var subsList [][]chan Event
	if subs, ok := eb.subscribers[event.Name]; ok {
		subsList = append(subsList, subs)
	}
	if subs, ok := eb.subscribers["*"]; ok {
		subsList = append(subsList, subs)
	}
	for _, subs := range subsList {
		for _, sub := range subs {
			select {
			case sub <- event:
			default:
				// channel full — drop to avoid blocking publisher
			}
		}
	}
}

// Subscribe registers a channel to receive events with the given name.
// Use "*" to receive all events.
func (eb *EventBus) Subscribe(eventName string) chan Event {
	eb.mu.Lock()
	defer eb.mu.Unlock()
	ch := make(chan Event, 10)
	eb.subscribers[eventName] = append(eb.subscribers[eventName], ch)
	return ch
}

// Unsubscribe removes the channel from all subscriber lists.
func (eb *EventBus) Unsubscribe(ch chan Event) {
	eb.mu.Lock()
	defer eb.mu.Unlock()
	for name, subs := range eb.subscribers {
		for i, sub := range subs {
			if sub == ch {
				eb.subscribers[name] = append(subs[:i], subs[i+1:]...)
				break
			}
		}
	}
}

// Publish enqueues an event for dispatching. Non-blocking: drops if queue is full.
func (eb *EventBus) Publish(event Event) {
	select {
	case eb.queue <- event:
	default:
		// queue full — drop event
	}
}

// Stop shuts down the publisher goroutine. Safe to call multiple times.
func (eb *EventBus) Stop() {
	eb.once.Do(func() { close(eb.stop) })
}
```

- [ ] **Step 2.4: Run tests to confirm they pass**

```bash
cd D:/Projects/Vanguard/vngd-srs-server && go test -race ./events/... -v
```

Expected: all 4 tests PASS.

- [ ] **Step 2.5: Add EventBus.Stop() call to VCSApplication.StopServer()**

In `app/app.go`, update `StopServer()` — add `a.eventBus.Stop()` after the server stop calls:

```go
func (a *VCSApplication) StopServer() {
	a.AdminState.RLock()
	if !a.AdminState.HTTPStatus.IsRunning &&
		!a.AdminState.VoiceStatus.IsRunning &&
		!a.AdminState.ControlStatus.IsRunning {
		a.AdminState.RUnlock()
		return
	}
	a.AdminState.RUnlock()

	a.DistributionState.RLock()
	if a.DistributionState.DistributionMode == state.DistributionModeVoice || a.DistributionState.DistributionMode == state.DistributionModeStandalone {
		a.stopVoiceServer()
	}
	if a.DistributionState.DistributionMode == state.DistributionModeControl || a.DistributionState.DistributionMode == state.DistributionModeStandalone {
		a.stopHTTPServer()
		a.stopControlServer()
	}
	a.DistributionState.RUnlock()

	a.eventBus.Stop()
}
```

- [ ] **Step 2.6: Fix HIGH-6 — remove no-op stream.Context().Done() in cleanup routine**

In `srs/srs_service.go`, inside `StartCleanupRoutine`, replace the stale-client block:

```go
// Before (no-op call):
if stream != nil {
    stream.Context().Done()
}
s.cleanupClientState(clientID)
delete(s.streams, clientID)
s.logger.Info("Cleaned up stale client", "client_id", clientID)

// After (no-op removed):
s.cleanupClientState(clientID)
delete(s.streams, clientID)
s.logger.Info("Cleaned up stale client", "client_id", clientID)
```

The full cleanup loop in `StartCleanupRoutine` after the fix:

```go
case <-ticker.C:
    now := time.Now()
    s.mu.Lock()
    for clientID, stream := range s.streams {
        s.serverState.RLock()
        client, exists := s.serverState.Clients[clientID]
        s.serverState.RUnlock()
        if !exists || now.Sub(client.LastUpdate) > staleAfter {
            _ = stream
            s.cleanupClientState(clientID)
            delete(s.streams, clientID)
            s.logger.Info("Cleaned up stale client", "client_id", clientID)
        }
    }
    s.mu.Unlock()
```

- [ ] **Step 2.7: Implement HIGH-5 — SubscribeToUpdates send path**

Add the `buildServerUpdate` method and rewrite `SubscribeToUpdates` in `srs/srs_service.go`.

First, add the import `pb "github.com/FPGSchiba/vcs-srs-server/srspb"` is already present. Add `"github.com/FPGSchiba/vcs-srs-server/events"` if not already imported (it is).

Add `buildServerUpdate` at the end of `srs_service.go`:

```go
// buildServerUpdate converts a bus event into a pb.ServerUpdate to send to
// subscribed clients. Returns nil for event types that should not be forwarded.
func (s *SimpleRadioServer) buildServerUpdate(event events.Event) *pb.ServerUpdate {
	switch event.Name {
	case events.ClientsChanged:
		return &pb.ServerUpdate{
			Type: pb.ServerUpdate_CLIENT_INFO_UPDATE,
		}
	case events.RadioClientsChanged:
		return &pb.ServerUpdate{
			Type: pb.ServerUpdate_CLIENT_RADIO_UPDATE,
		}
	case events.SettingsChanged, events.CoalitionsChanged:
		return &pb.ServerUpdate{
			Type:   pb.ServerUpdate_SERVER_SETTINGS_CHANGED,
			Update: &pb.ServerUpdate_SettingsUpdate{SettingsUpdate: s.buildServerSettings()},
		}
	default:
		return nil
	}
}
```

Replace `SubscribeToUpdates`:

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
			update := s.buildServerUpdate(event)
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

- [ ] **Step 2.8: Verify build and run event bus tests**

```bash
cd D:/Projects/Vanguard/vngd-srs-server && go vet ./... && go build -race ./... && go test -race ./events/... -v
```

Expected: build clean, all event bus tests pass.

- [ ] **Step 2.9: Commit**

```bash
cd D:/Projects/Vanguard/vngd-srs-server && git add events/bus.go events/bus_test.go srs/srs_service.go app/app.go && git commit -m "fix: event bus rewrite (channel-based, shutdown) + implement SubscribeToUpdates send path"
```

---

## Task 3: Shared State Safety

**Issues:** CRIT-4, CRIT-5, CRIT-6, HIGH-1, HIGH-2, HIGH-3 (already resolved), MED-5
**Files:**
- Create: `state/snapshot.go`
- Modify: `srs/srs_service.go`
- Modify: `voiceontrol/client.go`
- Modify: `app/clients.go`
- Modify: `app/settings.go`
- Modify: `app/app.go`
- Modify: `app/server_control.go`

---

- [ ] **Step 3.1: Verify HIGH-3 is already resolved**

HIGH-3 was about reusing the same `control.Server` instance. Check `app/server_control.go:startGrpcServer` — it already calls `control.NewServer(...)` on every invocation, giving each cycle a fresh `sync.Once`. No code change needed. Proceed.

- [ ] **Step 3.2: Create state/snapshot.go with snapshot structs**

Create `state/snapshot.go`:

```go
package state

// SettingsSnapshot is a lock-free copy of SettingsState for read-only consumers.
// Use this instead of returning the live *SettingsState pointer to avoid
// data races after the lock is released.
type SettingsSnapshot struct {
	Servers      ServerSettings
	Coalitions   []Coalition
	Frequencies  FrequencySettings
	General      GeneralSettings
	Security     SecuritySettings
	VoiceControl VoiceControlSettings
}

// AdminStateSnapshot is a lock-free copy of AdminState for read-only consumers.
type AdminStateSnapshot struct {
	HTTPStatus    ServiceStatus
	VoiceStatus   ServiceStatus
	ControlStatus ServiceStatus
}
```

- [ ] **Step 3.3: Fix HIGH-1 — GetSettings returns SettingsSnapshot**

In `app/settings.go`, change `GetSettings`:

```go
func (a *VCSApplication) GetSettings() state.SettingsSnapshot {
	a.SettingsState.RLock()
	defer a.SettingsState.RUnlock()
	return state.SettingsSnapshot{
		Servers:      a.SettingsState.Servers,
		Coalitions:   a.SettingsState.Coalitions,
		Frequencies:  a.SettingsState.Frequencies,
		General:      a.SettingsState.General,
		Security:     a.SettingsState.Security,
		VoiceControl: a.SettingsState.VoiceControl,
	}
}
```

Also snapshot settings events in `SaveGeneralSettings`, `SaveServerSettings`, and `SaveFrequencySettings` (all still hold the lock when calling EmitEvent via defer, but the event is dispatched after unlock — snapshot while lock is held):

In all three Save* methods, replace `Data: a.SettingsState` with a snapshot built while the lock is still held. Example for `SaveGeneralSettings` (apply same pattern to the other two):

```go
func (a *VCSApplication) SaveGeneralSettings(newSettings *state.GeneralSettings) {
	a.SettingsState.Lock()
	defer a.SettingsState.Unlock()
	a.SettingsState.General = *newSettings
	err := a.SettingsState.Save()
	if err != nil {
		a.Logger.Error(fmt.Sprintf("Failed to save settings: %v", err))
		a.Notify(events.NewNotification("Failed to save settings", "Failed to save settings", "error"))
		return
	}
	snap := state.SettingsSnapshot{
		Servers:      a.SettingsState.Servers,
		Coalitions:   a.SettingsState.Coalitions,
		Frequencies:  a.SettingsState.Frequencies,
		General:      a.SettingsState.General,
		Security:     a.SettingsState.Security,
		VoiceControl: a.SettingsState.VoiceControl,
	}
	a.EmitEvent(events.Event{Name: events.SettingsChanged, Data: snap})
	a.Notify(events.NewNotification("Settings saved", "General Settings were successfully saved", "info"))
}
```

Apply the same `snap` pattern to `SaveServerSettings` and `SaveFrequencySettings`.

**Risk:** `GetSettings()` return type changes from `*state.SettingsState` to `state.SettingsSnapshot`. Wails generates TypeScript bindings from Go return types. After this change, regenerate bindings (`wails generate bindings`) and update any frontend code that calls `GetSettings()` to use the new flat struct shape.

- [ ] **Step 3.4: Fix MED-5 — GetServerStatus returns AdminStateSnapshot**

In `app/app.go`, change `GetServerStatus`:

```go
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

Also snapshot AdminState events in `app/server_control.go`. The `startHTTPServer`, `stopHTTPServer`, `startGrpcServer`, `stopVoiceServer`, `stopControlServer` methods all call `a.EmitEvent(events.Event{Name: events.AdminChanged, Data: a.AdminState})` without the lock. Replace each with a snapshot taken while the lock is held (move the EmitEvent call inside the lock, or snapshot before releasing).

For `startHTTPServer`, the emit happens after lock is released (line 69). Fix by taking a snapshot before the lock is released:

```go
// At the end of startHTTPServer, replace:
a.EmitEvent(events.Event{
    Name: events.AdminChanged,
    Data: a.AdminState,
})

// With (call AFTER the final Unlock, using a pre-captured snapshot):
// Capture snapshot while lock is held:
a.AdminState.Lock()
adminSnap := state.AdminStateSnapshot{
    HTTPStatus:    a.AdminState.HTTPStatus,
    VoiceStatus:   a.AdminState.VoiceStatus,
    ControlStatus: a.AdminState.ControlStatus,
}
a.AdminState.Unlock()
a.EmitEvent(events.Event{Name: events.AdminChanged, Data: adminSnap})
```

Apply the same pattern to `stopHTTPServer`, `startGrpcServer`, `stopVoiceServer`, `stopControlServer`.

- [ ] **Step 3.5: Fix CRIT-6 — snapshot maps before emitting events in app/clients.go**

In `app/clients.go`, update every `EmitEvent` call that passes a live map. Here are all six replacements:

**BanClient** (two events after the unlock):
```go
// After: delete(a.ServerState.Clients, clientGuid) + a.ServerState.Unlock()

clientsSnap := make(map[uuid.UUID]*state.ClientState, len(a.ServerState.Clients))
for k, v := range a.ServerState.Clients {
    clientsSnap[k] = v
}
bannedSnap := make([]state.BannedClient, len(a.ServerState.BannedState.BannedClients))
copy(bannedSnap, a.ServerState.BannedState.BannedClients)

a.EmitEvent(events.Event{Name: events.ClientsChanged, Data: clientsSnap})
a.EmitEvent(events.Event{Name: events.BannedClientsChanged, Data: bannedSnap})
```

**UnbanClient** (one event after the unlock):
```go
bannedSnap := make([]state.BannedClient, len(a.ServerState.BannedState.BannedClients))
copy(bannedSnap, a.ServerState.BannedState.BannedClients)
a.EmitEvent(events.Event{Name: events.BannedClientsChanged, Data: bannedSnap})
```

**KickClient** (one event after the unlock):
```go
clientsSnap := make(map[uuid.UUID]*state.ClientState, len(a.ServerState.Clients))
for k, v := range a.ServerState.Clients {
    clientsSnap[k] = v
}
a.EmitEvent(events.Event{Name: events.ClientsChanged, Data: clientsSnap})
```

**MuteClient** (one event after the unlock):
```go
radioSnap := make(map[uuid.UUID]*state.RadioState, len(a.ServerState.RadioClients))
for k, v := range a.ServerState.RadioClients {
    radioSnap[k] = v
}
a.EmitEvent(events.Event{Name: events.RadioClientsChanged, Data: radioSnap})
```

**UnmuteClient** (one event after the unlock):
```go
radioSnap := make(map[uuid.UUID]*state.RadioState, len(a.ServerState.RadioClients))
for k, v := range a.ServerState.RadioClients {
    radioSnap[k] = v
}
a.EmitEvent(events.Event{Name: events.RadioClientsChanged, Data: radioSnap})
```

- [ ] **Step 3.6: Fix CRIT-6 continued — snapshot in srs/srs_service.go**

In `srs/srs_service.go`, replace every live-map publish with a snapshot.

**SyncClient** (line 101):
```go
clientsSnap := make(map[uuid.UUID]*state.ClientState, len(s.serverState.Clients))
s.serverState.RLock()
for k, v := range s.serverState.Clients {
    clientsSnap[k] = v
}
s.serverState.RUnlock()
s.eventBus.Publish(events.Event{Name: events.ClientsChanged, Data: clientsSnap})
```

**Disconnect** (line 147):
```go
s.serverState.RLock()
clientsSnap := make(map[uuid.UUID]*state.ClientState, len(s.serverState.Clients))
for k, v := range s.serverState.Clients {
    clientsSnap[k] = v
}
s.serverState.RUnlock()
s.eventBus.Publish(events.Event{Name: events.ClientsChanged, Data: clientsSnap})
```

**UpdateClientInfo** (line 231):
```go
s.serverState.RLock()
clientsSnap := make(map[uuid.UUID]*state.ClientState, len(s.serverState.Clients))
for k, v := range s.serverState.Clients {
    clientsSnap[k] = v
}
s.serverState.RUnlock()
s.eventBus.Publish(events.Event{Name: events.ClientsChanged, Data: clientsSnap})
```

**UpdateRadioInfo** (line 268):
```go
s.serverState.RLock()
radioSnap := make(map[uuid.UUID]*state.RadioState, len(s.serverState.RadioClients))
for k, v := range s.serverState.RadioClients {
    radioSnap[k] = v
}
s.serverState.RUnlock()
s.eventBus.Publish(events.Event{Name: events.RadioClientsChanged, Data: radioSnap})
```

- [ ] **Step 3.7: Fix CRIT-4 — add WaitGroup to SimpleRadioServer cleanup goroutine**

In `srs/srs_service.go`, add `wg sync.WaitGroup` to the struct:

```go
type SimpleRadioServer struct {
	pb.UnimplementedSRSServiceServer
	logger        *slog.Logger
	mu            sync.Mutex
	wg            sync.WaitGroup
	serverState   *state.ServerState
	settingsState *state.SettingsState
	eventBus      *events.EventBus
	streams       map[uuid.UUID]grpc.ServerStreamingServer[pb.ServerUpdate]
	stopChan      chan struct{}
	stopOnce      sync.Once
}
```

Update `StartCleanupRoutine` to track the goroutine:

```go
func (s *SimpleRadioServer) StartCleanupRoutine(interval time.Duration, staleAfter time.Duration) {
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
				now := time.Now()
				s.mu.Lock()
				for clientID := range s.streams {
					s.serverState.RLock()
					client, exists := s.serverState.Clients[clientID]
					s.serverState.RUnlock()
					if !exists || now.Sub(client.LastUpdate) > staleAfter {
						s.cleanupClientState(clientID)
						delete(s.streams, clientID)
						s.logger.Info("Cleaned up stale client", "client_id", clientID)
					}
				}
				s.mu.Unlock()
			}
		}
	}()
}
```

Update `Stop()` to wait for the goroutine:

```go
func (s *SimpleRadioServer) Stop() {
	s.stopOnce.Do(func() { close(s.stopChan) })
	s.wg.Wait()
}
```

- [ ] **Step 3.8: Fix CRIT-5 + MED-9 — reinitialize stopc in voiceontrol/client.go**

In `voiceontrol/client.go`, update `NewVoiceControlClient` to initialize `stopc`:

```go
func NewVoiceControlClient(serverId string, settingsState *state.SettingsState, logger *slog.Logger) *VoiceControlClient {
	return &VoiceControlClient{
		serverId:      serverId,
		logger:        logger,
		settingsState: settingsState,
		stopc:         make(chan struct{}), // never nil — MED-9 fix
	}
}
```

Update `ConnectControlServer` to always create a fresh `stopc` at the start (CRIT-5):

```go
func (v *VoiceControlClient) ConnectControlServer() error {
	// Re-initialize stopc on every connect so that a previous Close() doesn't
	// leave a closed channel that causes the next stream goroutine to exit immediately.
	v.stopc = make(chan struct{})

	v.settingsState.RLock()
	address := fmt.Sprintf("%s:%d", v.settingsState.VoiceControl.RemoteHost, v.settingsState.VoiceControl.Port)
	v.settingsState.RUnlock()
	// ... rest of function unchanged
```

- [ ] **Step 3.9: Fix HIGH-2 — remove outer TOCTOU guards**

In `app/app.go`, remove the redundant outer `IsRunning` check from `StartStandaloneServer`:

```go
func (a *VCSApplication) StartStandaloneServer() {
	a.startGrpcServer()
	a.startVoiceServer()
	a.startHTTPServer()
}
```

Remove from `StartControlServer`:

```go
func (a *VCSApplication) StartControlServer() {
	a.startGrpcServer()
	a.startHTTPServer()
}
```

Remove from `StartVoiceServer`:

```go
func (a *VCSApplication) StartVoiceServer() {
	a.startVoiceServer()
}
```

The inner `start*` functions each have their own authoritative guard under lock.

- [ ] **Step 3.10: Verify build is clean**

```bash
cd D:/Projects/Vanguard/vngd-srs-server && go vet ./... && go build -race ./...
```

Expected: no errors.

- [ ] **Step 3.11: Commit**

```bash
cd D:/Projects/Vanguard/vngd-srs-server && git add state/snapshot.go srs/srs_service.go voiceontrol/client.go app/clients.go app/settings.go app/app.go app/server_control.go && git commit -m "fix: shared state safety — map snapshots, WaitGroup, stopc reinit, snapshot return types"
```

---

## Task 4: Auth & Security

**Issues:** CRIT-7, CRIT-8, HIGH-7
**Files:**
- Create: `utils/auth_test.go`
- Modify: `utils/auth.go`
- Modify: `srs/auth_service.go`
- Modify: `srs/plugin_client.go`
- Modify: `state/settings.go`

---

- [ ] **Step 4.1: Write failing tests for JWT panic (CRIT-7)**

Create `utils/auth_test.go`:

```go
package utils

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// TestGetJWTClaimsMissingClientGuid verifies that a token with missing
// client_guid returns an error instead of panicking.
func TestGetJWTClaimsMissingClientGuid(t *testing.T) {
	// Generate a key pair in a temp dir.
	tmp := t.TempDir()
	priv, pub := tmp+"/priv.pem", tmp+"/pub.pem"

	// Generate a valid token but with no client_guid claim.
	key, _, err := getKeys(priv, pub)
	if err != nil {
		t.Fatalf("getKeys: %v", err)
	}
	claims := jwt.MapClaims{
		"role_id": float64(0),
		"exp":     float64(time.Now().Add(time.Hour).Unix()),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	tokenStr, err := token.SignedString(key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	result, err := getJWTClaims(tokenStr, priv, pub)
	if err == nil {
		t.Fatal("expected error for missing client_guid, got nil")
	}
	if result != nil {
		t.Fatal("expected nil result for missing client_guid")
	}
}

// TestGetJWTClaimsWrongType verifies that a token with wrong type for role_id
// returns an error instead of panicking.
func TestGetJWTClaimsWrongType(t *testing.T) {
	tmp := t.TempDir()
	priv, pub := tmp+"/priv.pem", tmp+"/pub.pem"

	key, _, err := getKeys(priv, pub)
	if err != nil {
		t.Fatalf("getKeys: %v", err)
	}
	claims := jwt.MapClaims{
		"client_guid": "some-guid",
		"role_id":     "not-a-number", // wrong type: string instead of float64
		"exp":         float64(time.Now().Add(time.Hour).Unix()),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	tokenStr, err := token.SignedString(key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	result, err := getJWTClaims(tokenStr, priv, pub)
	if err == nil {
		t.Fatal("expected error for wrong role_id type, got nil")
	}
	if result != nil {
		t.Fatal("expected nil result for wrong role_id type")
	}
}

// TestGetJWTClaimsValid verifies a well-formed token is parsed correctly.
func TestGetJWTClaimsValid(t *testing.T) {
	tmp := t.TempDir()
	priv, pub := tmp+"/priv.pem", tmp+"/pub.pem"

	_, _, err := getKeys(priv, pub) // initialise once
	if err != nil {
		t.Fatalf("getKeys: %v", err)
	}

	// Reset the once so a different key path can be used in this test.
	// (Since keysOnce is package-level, we call GenerateToken which uses getKeys.)
	tokenStr, err := GenerateToken("test-guid", GuestRole, "issuer", "subject", time.Hour, priv, pub)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	result, err := getJWTClaims(tokenStr, priv, pub)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ClientGuid != "test-guid" {
		t.Fatalf("expected client_guid=test-guid, got %s", result.ClientGuid)
	}
}
```

**Note:** Because `keysOnce` is a package-level `sync.Once`, the three tests must be run in the same process — the first call to `getKeys` caches the keys. `TestGetJWTClaimsValid` reuses the key from the first test via `GenerateToken`. This is expected behaviour (and documents MED-1).

- [ ] **Step 4.2: Run tests to confirm panic**

```bash
cd D:/Projects/Vanguard/vngd-srs-server && go test -race ./utils/... -v -run "TestGetJWT"
```

Expected: `TestGetJWTClaimsMissingClientGuid` and `TestGetJWTClaimsWrongType` PANIC (index out of range or type assertion failure).

- [ ] **Step 4.3: Fix CRIT-7 — guard JWT claims assertions in utils/auth.go**

Replace the body of `getJWTClaims` from the claims extraction point:

```go
func getJWTClaims(tokenString, privateKeyFile, publicKeyFile string) (*TokenClaims, error) {
	_, publicKey, err := getKeys(privateKeyFile, publicKeyFile)
	if err != nil {
		return nil, err
	}
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodECDSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return publicKey, nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok || !token.Valid {
		return nil, errors.New("invalid token")
	}

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
```

- [ ] **Step 4.4: Run tests to confirm they pass**

```bash
cd D:/Projects/Vanguard/vngd-srs-server && go test -race ./utils/... -v -run "TestGetJWT"
```

Expected: all three JWT tests PASS.

- [ ] **Step 4.5: Fix HIGH-7 — guard plugin response type assertions in srs/auth_service.go**

**In `handleAuthComplete` (line ~473)**, replace the bare assertion:

```go
// Before:
result := response.StepResult.(*authpb.AuthStepResponse_Complete).Complete

// After:
completeResult, ok := response.StepResult.(*authpb.AuthStepResponse_Complete)
if !ok || completeResult == nil || completeResult.Complete == nil {
    s.logger.Error("handleAuthComplete: plugin returned AUTH_COMPLETE but result payload is missing")
    return &pb.AuthStepResponse{
        Success: false,
        Result:  &pb.AuthStepResponse_ErrorMessage{ErrorMessage: "plugin returned AUTH_COMPLETE but result payload is missing"},
    }, nil
}
result := completeResult.Complete
```

**In `StartAuth` AUTH_FAILED branch (line ~444)**, replace:

```go
// Before:
errMsg := loginResponse.StepResult.(*authpb.AuthStepResponse_ErrorMessage)
s.logger.Warn(...)

// After:
errResult, ok := loginResponse.StepResult.(*authpb.AuthStepResponse_ErrorMessage)
errMsgText := "unknown error"
if ok && errResult != nil {
    errMsgText = errResult.ErrorMessage
}
s.logger.Warn("Plugin Login failed", "plugin-name", request.AuthenticationPlugin, "Error", errMsgText)
return &pb.AuthStepResponse{
    Success:   false,
    SessionId: loginResponse.SessionId,
    Result:    &pb.AuthStepResponse_ErrorMessage{ErrorMessage: fmt.Sprintf("Login failed: %s", errMsgText)},
}, nil
```

**In `ContinueAuth` AUTH_FAILED branch (line ~644)**, replace:

```go
// Before:
errMsg := loginResponse.StepResult.(*authpb.AuthStepResponse_ErrorMessage)

// After:
errResult, ok := loginResponse.StepResult.(*authpb.AuthStepResponse_ErrorMessage)
errMsgText := "unknown error"
if ok && errResult != nil {
    errMsgText = errResult.ErrorMessage
}
s.logger.Warn("Plugin ContinueAuth failed", "plugin-name", authenticatedClient.PluginUsed, "Error", errMsgText)
return &pb.AuthStepResponse{
    Success:   false,
    SessionId: loginResponse.SessionId,
    Result:    &pb.AuthStepResponse_ErrorMessage{ErrorMessage: fmt.Sprintf("ContinueAuth failed: %s", errMsgText)},
}, nil
```

- [ ] **Step 4.6: Fix CRIT-8 — TLS for plugin client connections**

**Add `CertificateFile string` to `PluginSettings` in `state/settings.go`:**

```go
type PluginSettings struct {
	Name           string            `yaml:"name"`
	Enabled        bool              `yaml:"enabled"`
	Address        string            `yaml:"address"`
	CertificateFile string           `yaml:"certificateFile"` // path to plugin's TLS cert (PEM); empty = insecure
	Configurations FlowConfiguration `yaml:"configurations"`
}
```

**Add a TLS cert loader helper to `srs/plugin_client.go`** (new unexported function at the bottom of the file):

```go
// loadPluginTLSConfig loads the plugin server's certificate from certFile and
// returns a tls.Config that trusts it. Returns nil, nil if certFile is empty,
// indicating that the caller should fall back to insecure transport.
func loadPluginTLSConfig(certFile string) (*tls.Config, error) {
	if certFile == "" {
		return nil, nil
	}
	certData, err := os.ReadFile(certFile)
	if err != nil {
		return nil, fmt.Errorf("loadPluginTLSConfig: read cert %s: %w", certFile, err)
	}
	block, _ := pem.Decode(certData)
	if block == nil {
		return nil, fmt.Errorf("loadPluginTLSConfig: failed to decode PEM from %s", certFile)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("loadPluginTLSConfig: parse certificate: %w", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &tls.Config{RootCAs: pool, InsecureSkipVerify: false}, nil
}
```

Add required imports to `srs/plugin_client.go` (add to import block):

```go
import (
    // ... existing imports ...
    "crypto/tls"
    "crypto/x509"
    "encoding/pem"
    "os"
    "google.golang.org/grpc/credentials"
)
```

**Add `caCertFile string` field to `PluginClient`:**

```go
type PluginClient struct {
	client           pb.AuthPluginServiceClient
	conn             *grpc.ClientConn
	logger           *slog.Logger
	settingsState    *state.SettingsState
	connectionFailed bool
	address          string
	pluginName       string
	caCertFile       string // path to plugin server's TLS cert; empty = insecure
	stopc            chan struct{}
	cancelMonitor    context.CancelFunc
	configuredFlows  []string
	config           *state.FlowConfiguration
}
```

**Update `NewPluginClient` to accept cert file:**

```go
func NewPluginClient(logger *slog.Logger, settingsState *state.SettingsState, name, address, caCertFile string, configuration *state.FlowConfiguration) *PluginClient {
	return &PluginClient{
		logger:        logger,
		settingsState: settingsState,
		pluginName:    name,
		address:       address,
		caCertFile:    caCertFile,
		config:        configuration,
	}
}
```

**Update `ConnectPlugin` to use TLS credentials:**

```go
func (v *PluginClient) ConnectPlugin() error {
	v.logger.Info("Connecting to plugin", "plugin-name", v.pluginName, "address", v.address)

	tlsCfg, err := loadPluginTLSConfig(v.caCertFile)
	if err != nil {
		v.logger.Warn("Failed to load plugin TLS cert, falling back to insecure", "plugin", v.pluginName, "error", err)
	}

	var transportCreds grpc.DialOption
	if tlsCfg != nil {
		transportCreds = grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg))
		v.logger.Info("Plugin connection using TLS", "plugin", v.pluginName)
	} else {
		transportCreds = grpc.WithTransportCredentials(insecure.NewCredentials())
		v.logger.Warn("Plugin connection is NOT encrypted (no certificateFile configured)", "plugin", v.pluginName)
	}

	opts := []grpc.DialOption{
		transportCreds,
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff: backoff.Config{
				BaseDelay:  1 * time.Second,
				Multiplier: 1.6,
				MaxDelay:   10 * time.Second,
				Jitter:     0.2,
			},
			MinConnectTimeout: 5 * time.Second,
		}),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                15 * time.Second,
			Timeout:             10 * time.Second,
			PermitWithoutStream: true,
		}),
	}

	conn, err := grpc.NewClient(v.address, opts...)
	if err != nil || conn == nil {
		v.logger.Error("Failed to connect to Plugin", "plugin-name", v.pluginName, "error", err)
		_ = v.settingsState.SetPluginEnabled(v.pluginName, false)
		return err
	}
	v.conn = conn
	client := pb.NewAuthPluginServiceClient(v.conn)
	v.client = client
	if err := v.establishConnection(); err != nil {
		v.logger.Error("Failed to establish connection to Plugin", "plugin-name", v.pluginName, "error", err)
		v.Close()
		return err
	}
	return nil
}
```

**Update the call site in `srs/auth_service.go`** where `NewPluginClient` is called (search for `NewPluginClient` calls and add the `plugin.CertificateFile` argument):

```go
// Find the call site (typically in NewAuthServer or plugin initialization):
// Before:
client := NewPluginClient(s.logger, s.settingsState, plugin.Name, plugin.Address, plugin.Configurations)

// After:
client := NewPluginClient(s.logger, s.settingsState, plugin.Name, plugin.Address, plugin.CertificateFile, &plugin.Configurations)
```

Search for all `NewPluginClient` calls with:
```bash
grep -rn "NewPluginClient" D:/Projects/Vanguard/vngd-srs-server/srs/
```

Update each call to pass `plugin.CertificateFile` as the 5th argument.

- [ ] **Step 4.7: Verify build is clean**

```bash
cd D:/Projects/Vanguard/vngd-srs-server && go vet ./... && go build -race ./...
```

Expected: no errors.

- [ ] **Step 4.8: Run auth tests**

```bash
cd D:/Projects/Vanguard/vngd-srs-server && go test -race ./utils/... -v
```

Expected: all tests PASS.

- [ ] **Step 4.9: Commit**

```bash
cd D:/Projects/Vanguard/vngd-srs-server && git add utils/auth.go utils/auth_test.go srs/auth_service.go srs/plugin_client.go state/settings.go && git commit -m "fix: auth safety — JWT claim guards, plugin response guards, TLS cert support for plugin connections"
```

---

## Task 5: Code Quality

**Issues:** MED-1, MED-2, MED-3, MED-4, MED-6, MED-7, MED-9
**Files:**
- Modify: `utils/auth.go`
- Modify: `utils/general.go`
- Modify: `control/server.go`
- Modify: `app/app.go`
- Modify: `voiceontrol/client.go`

---

- [ ] **Step 5.1: Fix MED-1 — document keysOnce limitation**

In `utils/auth.go`, update the comment above `keysOnce`:

```go
// keysOnce ensures keys are loaded from disk exactly once per process lifetime.
// Changing the key file paths in settings requires a server restart to take effect.
// A future improvement would replace this package-level singleton with a per-instance
// key cache to allow runtime key rotation without a restart.
var (
	keysOnce   sync.Once
	cachedPriv *ecdsa.PrivateKey
	cachedPub  *ecdsa.PublicKey
	keysErr    error
)
```

- [ ] **Step 5.2: Fix MED-3 — unexport SrsServiceMinimumRoleMap and add getter**

In `utils/auth.go`, rename `SrsServiceMinimumRoleMap` to `srsServiceMinimumRoleMap`:

```go
var (
	srsServiceMinimumRoleMap = map[string]uint8{
		"UpdateClientInfo":   GuestRole,
		"UpdateRadioInfo":    GuestRole,
		"SyncClient":         GuestRole,
		"Disconnect":         GuestRole,
		"GetServerSettings":  GuestRole,
		"SubscribeToUpdates": GuestRole,
	}
	SrsRoleNameMap = map[uint8]string{
		GuestRole:   "Guest",
		MemberRole:  "Member",
		OfficerRole: "Officer",
		AdminRole:   "Admin",
	}
)

// GetMinimumRoleForMethod returns the minimum required role for the given gRPC
// method name, and whether the method is in the role map at all.
func GetMinimumRoleForMethod(methodName string) (uint8, bool) {
	role, ok := srsServiceMinimumRoleMap[methodName]
	return role, ok
}
```

- [ ] **Step 5.3: Update control/server.go to use GetMinimumRoleForMethod**

In `control/server.go:authInterceptor`, replace the direct map access with the getter. Also add the bounds check (MED-2) at the same time:

```go
func (s *Server) authInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
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

	if serviceName == "AuthService" || serviceName == "VoiceControlService" {
		return handler(ctx, req)
	}

	s.logger.Debug("Authentication required for service", "service", serviceName, "method", info.FullMethod)

	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, fmt.Errorf("unauthenticated request to %s: missing metadata", info.FullMethod)
	}

	tokens := md.Get("authorization")
	if len(tokens) == 0 {
		return nil, fmt.Errorf("unauthenticated request to %s: missing authorization token", info.FullMethod)
	}
	token := strings.TrimPrefix(tokens[0], "Bearer ")

	minRole, _ := utils.GetMinimumRoleForMethod(pathName)

	s.settingsState.RLock()
	claims, err := utils.GetTokenClaims(token, minRole, s.settingsState.Security.Token.PrivateKeyFile, s.settingsState.Security.Token.PublicKeyFile)
	s.settingsState.RUnlock()
	if err != nil {
		s.logger.Error("Authentication error", "method", info.FullMethod, "error", err)
		return nil, fmt.Errorf("authentication error for %s: %v", info.FullMethod, err)
	}

	if claims == nil {
		return nil, fmt.Errorf("unauthenticated request to %s", info.FullMethod)
	}

	ctx = context.WithValue(ctx, utils.ClientIDKey, claims.ClientGuid)
	return handler(ctx, req)
}
```

Add `"google.golang.org/grpc/codes"` and `"google.golang.org/grpc/status"` to the imports in `control/server.go` if not already present.

- [ ] **Step 5.4: Fix MED-4 — remove redundant break statements in HeadlessStartup**

In `app/app.go`, update the switch in `HeadlessStartup`:

```go
switch distributionMode {
case state.DistributionModeStandalone:
	a.StartStandaloneServer()
case state.DistributionModeControl:
	a.StartControlServer()
case state.DistributionModeVoice:
	a.StartVoiceServer()
}
```

- [ ] **Step 5.5: Fix MED-6 — drain wildcard subscriber channel in headless mode**

In `app/app.go`, update `handleFrontendEmits`:

```go
func (a *VCSApplication) handleFrontendEmits(channel chan events.Event) {
	a.DistributionState.RLock()
	isGUI := a.DistributionState.RuntimeMode == state.RuntimeModeGUI
	a.DistributionState.RUnlock()

	if !isGUI {
		// Drain the channel so events don't silently back up when the bus is running.
		// The loop exits when the channel is closed by EventBus.Stop().
		for range channel {}
		return
	}
	for event := range channel {
		a.Logger.Debug("Received event from event bus", "name", event.Name)
		a.App.Event.EmitEvent(&application.CustomEvent{Name: event.Name, Data: event.Data})
	}
}
```

- [ ] **Step 5.6: Fix MED-7 — rename CheckPasswordHash parameters to match bcrypt convention**

In `utils/general.go`, rename parameters:

```go
// CheckPasswordHash compares a bcrypt hash with a plaintext password.
// The first argument is the stored hash; the second is the plaintext to verify.
func CheckPasswordHash(hash, plaintext string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(plaintext))
	return err == nil
}
```

Verify the call site in `srs/auth_service.go` (search for `CheckPasswordHash`):

```bash
grep -n "CheckPasswordHash" D:/Projects/Vanguard/vngd-srs-server/srs/auth_service.go
```

The call is:
```go
if utils.CheckPasswordHash(coalition.Password, request.Password) {
```

`coalition.Password` is the stored bcrypt hash; `request.Password` is the plaintext from the client. With the renamed parameters `(hash, plaintext)`, this call is now semantically explicit and correct. No value change needed.

- [ ] **Step 5.7: Fix MED-9 — stopc already initialized in NewVoiceControlClient (done in Task 3)**

MED-9 was fixed in Step 3.8 when `NewVoiceControlClient` was updated to initialize `stopc: make(chan struct{})`. Verify:

```bash
grep -n "stopc" D:/Projects/Vanguard/vngd-srs-server/voiceontrol/client.go
```

Expected: `stopc: make(chan struct{})` present in `NewVoiceControlClient`.

- [ ] **Step 5.8: Verify full build and tests**

```bash
cd D:/Projects/Vanguard/vngd-srs-server && go vet ./... && go build -race ./... && go test -race ./...
```

Expected: no errors, all tests pass.

- [ ] **Step 5.9: Commit**

```bash
cd D:/Projects/Vanguard/vngd-srs-server && git add utils/auth.go utils/general.go control/server.go app/app.go voiceontrol/client.go && git commit -m "fix: code quality — authInterceptor bounds, unexport role map, bcrypt naming, headless drain, remove break"
```

---

## Post-Implementation Verification

- [ ] **Run full test suite with race detector**

```bash
cd D:/Projects/Vanguard/vngd-srs-server && go test -race ./... -v
```

- [ ] **Run go vet**

```bash
cd D:/Projects/Vanguard/vngd-srs-server && go vet ./...
```

- [ ] **Run race build**

```bash
cd D:/Projects/Vanguard/vngd-srs-server && go build -race ./...
```

- [ ] **Check Wails bindings compile** (if frontend is present)

```bash
# If frontend/ directory exists and uses the changed GetSettings/GetServerStatus return types:
cd D:/Projects/Vanguard/vngd-srs-server && wails generate bindings 2>/dev/null || echo "wails not in path — check frontend manually"
```

- [ ] **Push to origin**

```bash
cd D:/Projects/Vanguard/vngd-srs-server && git push
```
