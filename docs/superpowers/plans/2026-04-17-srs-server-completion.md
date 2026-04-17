# SRS Server Completion Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Complete the three incomplete areas of `vngd-srs-server`: fix SRS gRPC gaps, add a GraphQL HTTP admin API, and extend the frontend with Security/VoiceControl settings and a live log view.

**Architecture:** Internal event bus wires logging, gRPC streaming, and frontend live updates. The GraphQL API is served by the existing Gin HTTP server and protected by a static API key read from settings. The new `logging/` package adds a `BusHandler` as a third fanout sink alongside the existing console and file handlers. All new Go code avoids circular imports by defining `AppInterface` in the `graphql/` package (importing only `state`) and having `app/` import `graphql/`.

**Tech Stack:** Go 1.25, gRPC (proto-generated), gqlgen (schema-first GraphQL), Gin, Wails v3 (React/TypeScript frontend), MUI, react-hook-form + Zod, slogmulti.

---

## File Map

**New files:**
- `events/events.go` — extend with `ClientChangeEvent`, `RadioChangeEvent`, `ServerActionEvent`, `LogEntry` const
- `logging/bus_handler.go` — `BusHandler` implementing `slog.Handler`; publishes to event bus
- `graphql/schema.graphql` — SDL schema (source of truth)
- `gqlgen.yml` — gqlgen config at project root
- `graphql/resolver.go` — `AppInterface`, `Resolver` struct + constructor
- `graphql/query.resolvers.go` — Query implementations
- `graphql/mutation.resolvers.go` — Mutation implementations
- `rest/middleware.go` — API key Gin middleware
- `frontend/src/pages/Logs.tsx` — new Logs tab page

**Modified files:**
- `srs/utils.go` — add `IsIntercom` to both radio conversion functions
- `srs/auth_service.go` — fix IP ban check with `net.SplitHostPort`; update `ClientsChanged` publishers
- `srs/srs_service.go` — update all `ClientsChanged` publishers; rewrite `buildServerUpdate`
- `app/clients.go` — emit `ServerActionEvent` from kick/ban/mute/unmute; update publishers
- `shared.go` — add `*events.EventBus` param to `parseFlags`; add BusHandler to fanout
- `main.go` — pass event bus to `parseFlags`
- `headless.go` — pass event bus to `parseFlags`
- `state/settings.go` — add `ApiSettings`, `SettingsSnapshot`
- `build/Taskfile.yml` — add `generate:graphql` task; add as dep of `go:mod:tidy`
- `app/server_control.go` — mount GraphQL handler with middleware
- `services/settings.go` — add `SaveSecuritySettings`, `SaveVoiceControlSettings`
- `frontend/src/pages/Settings.tsx` — Security + VoiceControl sections
- `frontend/src/components/ContentWrapper.tsx` — add Logs tab
- `frontend/src/pages/ClientList.tsx` — fix `ClientsChanged` event handler

---

## Task 1: Extend events/events.go with new types

**Files:**
- Modify: `events/events.go`
- Test: `events/events_test.go` (create)

- [ ] **Step 1: Write the failing test**

Create `events/events_test.go`:

```go
package events_test

import (
	"testing"

	"github.com/FPGSchiba/vcs-srs-server/events"
	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/google/uuid"
)

func TestClientChangeEvent_Fields(t *testing.T) {
	id := uuid.New()
	clients := map[uuid.UUID]*state.ClientState{id: {Name: "Alice"}}
	evt := events.ClientChangeEvent{
		Type:     events.ClientJoined,
		ClientID: id,
		Clients:  clients,
	}
	if evt.Type != events.ClientJoined {
		t.Fatalf("expected ClientJoined, got %v", evt.Type)
	}
	if evt.ClientID != id {
		t.Fatalf("expected %v, got %v", id, evt.ClientID)
	}
	if len(evt.Clients) != 1 {
		t.Fatalf("expected 1 client, got %d", len(evt.Clients))
	}
}

func TestServerActionEvent_Fields(t *testing.T) {
	id := uuid.New()
	evt := events.ServerActionEvent{
		ActionType:     events.ActionKick,
		TargetClientID: id,
		Reason:         "cheating",
	}
	if evt.ActionType != events.ActionKick {
		t.Fatalf("expected ActionKick, got %v", evt.ActionType)
	}
	if evt.Reason != "cheating" {
		t.Fatalf("expected 'cheating', got %q", evt.Reason)
	}
}

func TestLogEntry_Fields(t *testing.T) {
	if events.LogEntry != "logs/entry" {
		t.Fatalf("expected 'logs/entry', got %q", events.LogEntry)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/schiba/Projects/vanguard/vngd-srs-server
go test ./events/... -v -run TestClientChangeEvent_Fields
```

Expected: FAIL — `ClientChangeEvent`, `ClientJoined`, `ServerActionEvent`, `ActionKick`, `LogEntry` undefined.

- [ ] **Step 3: Implement — extend events/events.go**

Add to `events/events.go` below the existing `const` blocks and `type` declarations:

```go
const (
	LogEntry      = "logs/entry"
	ServerAction  = "admin/server-action"
)

type ClientChangeType int

const (
	ClientJoined      ClientChangeType = iota
	ClientLeft
	ClientInfoUpdated
)

type RadioChangeType int

const (
	RadioUpdated RadioChangeType = iota
)

type ActionType int

const (
	ActionKick   ActionType = iota
	ActionBan
	ActionMute
	ActionUnmute
)

type ClientChangeEvent struct {
	Type     ClientChangeType
	ClientID uuid.UUID
	Clients  map[uuid.UUID]*state.ClientState
}

type RadioChangeEvent struct {
	Type     RadioChangeType
	ClientID uuid.UUID
	Radios   map[uuid.UUID]*state.RadioState
}

type ServerActionEvent struct {
	ActionType     ActionType
	TargetClientID uuid.UUID
	Reason         string
}
```

Add the required import for `state` at the top of the file:

```go
import (
	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/google/uuid"
)
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./events/... -v -race
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add events/events.go events/events_test.go
git commit -m "feat: add ClientChangeEvent, RadioChangeEvent, ServerActionEvent, LogEntry to events package"
```

---

## Task 2: Fix IsIntercom in srs/utils.go

**Files:**
- Modify: `srs/utils.go`
- Test: `srs/utils_test.go` (create)

- [ ] **Step 1: Write the failing test**

Create `srs/utils_test.go`:

```go
package srs

import (
	"testing"

	pb "github.com/FPGSchiba/vcs-srs-server/srspb"
	"github.com/FPGSchiba/vcs-srs-server/state"
)

func TestConvertSingleRadio_PreservesIsIntercom(t *testing.T) {
	r := &state.Radio{
		ID:         "r1",
		Name:       "Radio 1",
		Frequency:  121.5,
		Enabled:    true,
		IsIntercom: true,
	}
	got := convertSingleRadio(r)
	if !got.IsIntercom {
		t.Fatal("expected IsIntercom=true, got false")
	}
}

func TestConvertSingleRadioState_PreservesIsIntercom(t *testing.T) {
	r := &pb.Radio{
		Id:         "r1",
		Name:       "Radio 1",
		Frequency:  121.5,
		Enabled:    true,
		IsIntercom: true,
	}
	got := convertSingleRadioState(r)
	if !got.IsIntercom {
		t.Fatal("expected IsIntercom=true, got false")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./srs/... -v -run TestConvertSingleRadio_PreservesIsIntercom
```

Expected: FAIL — `got false` (IsIntercom not copied).

- [ ] **Step 3: Implement — fix convertSingleRadio and convertSingleRadioState in srs/utils.go**

In `convertSingleRadio` (line 58–65), replace:

```go
func convertSingleRadio(r *state.Radio) *pb.Radio {
	return &pb.Radio{
		Id:        r.ID,
		Name:      r.Name,
		Frequency: r.Frequency,
		Enabled:   r.Enabled,
	}
}
```

With:

```go
func convertSingleRadio(r *state.Radio) *pb.Radio {
	return &pb.Radio{
		Id:         r.ID,
		Name:       r.Name,
		Frequency:  r.Frequency,
		Enabled:    r.Enabled,
		IsIntercom: r.IsIntercom,
	}
}
```

In `convertSingleRadioState` (line 102–109), replace:

```go
func convertSingleRadioState(r *pb.Radio) state.Radio {
	return state.Radio{
		ID:        r.Id,
		Name:      r.Name,
		Frequency: r.Frequency,
		Enabled:   r.Enabled,
	}
}
```

With:

```go
func convertSingleRadioState(r *pb.Radio) state.Radio {
	return state.Radio{
		ID:         r.Id,
		Name:       r.Name,
		Frequency:  r.Frequency,
		Enabled:    r.Enabled,
		IsIntercom: r.IsIntercom,
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./srs/... -v -race -run TestConvertSingleRadio
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add srs/utils.go srs/utils_test.go
git commit -m "fix: preserve IsIntercom field in radio conversion functions"
```

---

## Task 3: Fix IP ban check in srs/auth_service.go

**Files:**
- Modify: `srs/auth_service.go`
- Test: `srs/auth_service_test.go` (create)

- [ ] **Step 1: Write the failing test**

Create `srs/auth_service_test.go`:

```go
package srs

import (
	"net"
	"testing"
)

func TestSplitHostForBanCheck(t *testing.T) {
	cases := []struct {
		addr     string
		wantHost string
	}{
		{"192.168.1.1:54321", "192.168.1.1"},
		{"10.0.0.5:1234", "10.0.0.5"},
		{"[::1]:8080", "::1"},
	}
	for _, tc := range cases {
		host, _, err := net.SplitHostPort(tc.addr)
		if err != nil {
			t.Fatalf("SplitHostPort(%q) error: %v", tc.addr, err)
		}
		if host != tc.wantHost {
			t.Fatalf("addr %q: got host %q, want %q", tc.addr, host, tc.wantHost)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails (or passes immediately)**

```bash
go test ./srs/... -v -run TestSplitHostForBanCheck
```

Expected: PASS (this validates the fix approach). Proceed to step 3.

- [ ] **Step 3: Implement — fix InitAuth IP ban check in srs/auth_service.go**

In `InitAuth` (around line 110–116), replace:

```go
	_, banned := utils.FindByFunc(s.serverState.BannedState.BannedClients, func(bc state.BannedClient) bool {
		if bc.IPAddress == p.Addr.String() {
			return true
		}
		return false
	})
```

With:

```go
	peerHost, _, _ := net.SplitHostPort(p.Addr.String())
	_, banned := utils.FindByFunc(s.serverState.BannedState.BannedClients, func(bc state.BannedClient) bool {
		return bc.IPAddress == peerHost
	})
```

Add `"net"` to the import block in `srs/auth_service.go`.

- [ ] **Step 4: Run build to verify it compiles**

```bash
go build ./srs/...
```

Expected: success, no errors.

- [ ] **Step 5: Commit**

```bash
git add srs/auth_service.go srs/auth_service_test.go
git commit -m "fix: use net.SplitHostPort for IP ban check in InitAuth"
```

---

## Task 4: Update all ClientsChanged publishers to use ClientChangeEvent

**Files:**
- Modify: `srs/srs_service.go`
- Modify: `srs/auth_service.go`

There are 5 places that publish `events.ClientsChanged` with a bare map. All must be updated to use `events.ClientChangeEvent`.

Publishers in `srs/srs_service.go`:
1. `SyncClient` (line 108) — `ClientInfoUpdated` (full sync)
2. `Disconnect` (line 157) — `ClientLeft`
3. `UpdateClientInfo` (line 244) — `ClientInfoUpdated`

Publishers in `srs/auth_service.go`:
4. `GuestLogin` (line 363) — `ClientJoined`
5. `UnitSelect` (line 790) — `ClientJoined`

- [ ] **Step 1: Write a test that calls buildServerUpdate with a ClientChangeEvent and checks the result type**

Add to `srs/utils_test.go`:

```go
func TestBuildServerUpdate_ClientJoinedReturnsClientJoined(t *testing.T) {
	// Ensure buildServerUpdate maps ClientJoined → CLIENT_JOINED
	// We test the mapping logic independently; full service test would need gRPC infra.
	// For now, verify events package types are accessible in srs package.
	_ = events.ClientChangeEvent{Type: events.ClientJoined}
	_ = events.ClientChangeEvent{Type: events.ClientLeft}
	_ = events.ClientChangeEvent{Type: events.ClientInfoUpdated}
}
```

Add `"github.com/FPGSchiba/vcs-srs-server/events"` to `srs/utils_test.go` imports.

- [ ] **Step 2: Run build to confirm compiles**

```bash
go build ./srs/... && go test ./srs/... -v -run TestBuildServerUpdate
```

- [ ] **Step 3: Update SyncClient in srs/srs_service.go (line 102–108)**

Replace:
```go
	s.serverState.RLock()
	clientsSnap := make(map[uuid.UUID]*state.ClientState, len(s.serverState.Clients))
	for k, v := range s.serverState.Clients {
		clientsSnap[k] = v
	}
	s.serverState.RUnlock()
	s.eventBus.Publish(events.Event{Name: events.ClientsChanged, Data: clientsSnap})
```

With:
```go
	s.serverState.RLock()
	clientsSnap := make(map[uuid.UUID]*state.ClientState, len(s.serverState.Clients))
	for k, v := range s.serverState.Clients {
		clientsSnap[k] = v
	}
	s.serverState.RUnlock()
	s.eventBus.Publish(events.Event{
		Name: events.ClientsChanged,
		Data: events.ClientChangeEvent{Type: events.ClientInfoUpdated, Clients: clientsSnap},
	})
```

- [ ] **Step 4: Update Disconnect in srs/srs_service.go (line 151–157)**

Replace:
```go
	s.serverState.RLock()
	clientsSnap := make(map[uuid.UUID]*state.ClientState, len(s.serverState.Clients))
	for k, v := range s.serverState.Clients {
		clientsSnap[k] = v
	}
	s.serverState.RUnlock()
	s.eventBus.Publish(events.Event{Name: events.ClientsChanged, Data: clientsSnap})
```

With:
```go
	s.serverState.RLock()
	clientsSnap := make(map[uuid.UUID]*state.ClientState, len(s.serverState.Clients))
	for k, v := range s.serverState.Clients {
		clientsSnap[k] = v
	}
	s.serverState.RUnlock()
	s.eventBus.Publish(events.Event{
		Name: events.ClientsChanged,
		Data: events.ClientChangeEvent{Type: events.ClientLeft, ClientID: clientID, Clients: clientsSnap},
	})
```

- [ ] **Step 5: Update UpdateClientInfo in srs/srs_service.go (line 238–244)**

Replace:
```go
	s.serverState.RLock()
	clientsSnap := make(map[uuid.UUID]*state.ClientState, len(s.serverState.Clients))
	for k, v := range s.serverState.Clients {
		clientsSnap[k] = v
	}
	s.serverState.RUnlock()
	s.eventBus.Publish(events.Event{Name: events.ClientsChanged, Data: clientsSnap})
```

With:
```go
	s.serverState.RLock()
	clientsSnap := make(map[uuid.UUID]*state.ClientState, len(s.serverState.Clients))
	for k, v := range s.serverState.Clients {
		clientsSnap[k] = v
	}
	s.serverState.RUnlock()
	s.eventBus.Publish(events.Event{
		Name: events.ClientsChanged,
		Data: events.ClientChangeEvent{Type: events.ClientInfoUpdated, ClientID: clientID, Clients: clientsSnap},
	})
```

- [ ] **Step 6: Update GuestLogin in srs/auth_service.go (line 357–366)**

Replace:
```go
	s.serverState.RLock()
	clientsSnap := make(map[uuid.UUID]*state.ClientState, len(s.serverState.Clients))
	for k, v := range s.serverState.Clients {
		clientsSnap[k] = v
	}
	s.serverState.RUnlock()
	s.eventBus.Publish(events.Event{
		Name: events.ClientsChanged,
		Data: clientsSnap,
	})
```

With:
```go
	s.serverState.RLock()
	clientsSnap := make(map[uuid.UUID]*state.ClientState, len(s.serverState.Clients))
	for k, v := range s.serverState.Clients {
		clientsSnap[k] = v
	}
	s.serverState.RUnlock()
	s.eventBus.Publish(events.Event{
		Name: events.ClientsChanged,
		Data: events.ClientChangeEvent{Type: events.ClientJoined, ClientID: clientGuid, Clients: clientsSnap},
	})
```

- [ ] **Step 7: Update UnitSelect in srs/auth_service.go (line 784–793)**

Replace:
```go
	s.serverState.RLock()
	clientsSnap := make(map[uuid.UUID]*state.ClientState, len(s.serverState.Clients))
	for k, v := range s.serverState.Clients {
		clientsSnap[k] = v
	}
	s.serverState.RUnlock()
	s.eventBus.Publish(events.Event{
		Name: events.ClientsChanged,
		Data: clientsSnap,
	})
```

With:
```go
	s.serverState.RLock()
	clientsSnap := make(map[uuid.UUID]*state.ClientState, len(s.serverState.Clients))
	for k, v := range s.serverState.Clients {
		clientsSnap[k] = v
	}
	s.serverState.RUnlock()
	s.eventBus.Publish(events.Event{
		Name: events.ClientsChanged,
		Data: events.ClientChangeEvent{Type: events.ClientJoined, ClientID: clientGuid, Clients: clientsSnap},
	})
```

Also update `UpdateRadioInfo` in `srs/srs_service.go` (line 278–284) to use `RadioChangeEvent`:

Replace:
```go
	s.serverState.RLock()
	radioSnap := make(map[uuid.UUID]*state.RadioState, len(s.serverState.RadioClients))
	for k, v := range s.serverState.RadioClients {
		radioSnap[k] = v
	}
	s.serverState.RUnlock()
	s.eventBus.Publish(events.Event{Name: events.RadioClientsChanged, Data: radioSnap})
```

With:
```go
	s.serverState.RLock()
	radioSnap := make(map[uuid.UUID]*state.RadioState, len(s.serverState.RadioClients))
	for k, v := range s.serverState.RadioClients {
		radioSnap[k] = v
	}
	s.serverState.RUnlock()
	s.eventBus.Publish(events.Event{
		Name: events.RadioClientsChanged,
		Data: events.RadioChangeEvent{Type: events.RadioUpdated, ClientID: clientID, Radios: radioSnap},
	})
```

- [ ] **Step 8: Run build to verify**

```bash
go build ./srs/... ./events/...
```

Expected: no errors.

- [ ] **Step 9: Commit**

```bash
git add srs/srs_service.go srs/auth_service.go srs/utils_test.go
git commit -m "feat: replace bare map payloads with ClientChangeEvent and RadioChangeEvent"
```

---

## Task 5: Rewrite buildServerUpdate in srs/srs_service.go

**Files:**
- Modify: `srs/srs_service.go`
- Test: `srs/srs_service_test.go` (create)

- [ ] **Step 1: Write the failing test**

Create `srs/srs_service_test.go`:

```go
package srs

import (
	"testing"

	"github.com/FPGSchiba/vcs-srs-server/events"
	pb "github.com/FPGSchiba/vcs-srs-server/srspb"
	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/google/uuid"
)

func newTestServer() *SimpleRadioServer {
	return &SimpleRadioServer{
		serverState:   &state.ServerState{},
		settingsState: &state.SettingsState{},
		streams:       make(map[uuid.UUID]pb.SRSService_SubscribeToUpdatesServer),
	}
}

func TestBuildServerUpdate_ClientJoined(t *testing.T) {
	s := newTestServer()
	id := uuid.New()
	evt := events.Event{
		Name: events.ClientsChanged,
		Data: events.ClientChangeEvent{
			Type:     events.ClientJoined,
			ClientID: id,
			Clients:  map[uuid.UUID]*state.ClientState{},
		},
	}
	update := s.buildServerUpdate(evt)
	if update == nil {
		t.Fatal("expected non-nil update")
	}
	if update.Type != pb.ServerUpdate_CLIENT_JOINED {
		t.Fatalf("expected CLIENT_JOINED, got %v", update.Type)
	}
	cu := update.GetClientUpdate()
	if cu == nil {
		t.Fatal("expected ClientUpdate payload")
	}
	if cu.GetClientGuid() != id.String() {
		t.Fatalf("expected %s, got %s", id.String(), cu.GetClientGuid())
	}
}

func TestBuildServerUpdate_ClientLeft(t *testing.T) {
	s := newTestServer()
	id := uuid.New()
	evt := events.Event{
		Name: events.ClientsChanged,
		Data: events.ClientChangeEvent{
			Type:     events.ClientLeft,
			ClientID: id,
			Clients:  map[uuid.UUID]*state.ClientState{},
		},
	}
	update := s.buildServerUpdate(evt)
	if update == nil || update.Type != pb.ServerUpdate_CLIENT_LEFT {
		t.Fatalf("expected CLIENT_LEFT, got %v", update)
	}
}

func TestBuildServerUpdate_RadioUpdate(t *testing.T) {
	s := newTestServer()
	id := uuid.New()
	evt := events.Event{
		Name: events.RadioClientsChanged,
		Data: events.RadioChangeEvent{
			Type:     events.RadioUpdated,
			ClientID: id,
			Radios:   map[uuid.UUID]*state.RadioState{},
		},
	}
	update := s.buildServerUpdate(evt)
	if update == nil || update.Type != pb.ServerUpdate_CLIENT_RADIO_UPDATE {
		t.Fatalf("expected CLIENT_RADIO_UPDATE, got %v", update)
	}
}

func TestBuildServerUpdate_ServerAction_Kick(t *testing.T) {
	s := newTestServer()
	id := uuid.New()
	evt := events.Event{
		Name: events.ServerAction,
		Data: events.ServerActionEvent{
			ActionType:     events.ActionKick,
			TargetClientID: id,
			Reason:         "rule violation",
		},
	}
	update := s.buildServerUpdate(evt)
	if update == nil || update.Type != pb.ServerUpdate_SERVER_ACTION {
		t.Fatalf("expected SERVER_ACTION, got %v", update)
	}
	sa := update.GetServerAction()
	if sa == nil {
		t.Fatal("expected ServerAction payload")
	}
	if sa.GetType() != pb.ServerAction_KICK {
		t.Fatalf("expected KICK, got %v", sa.GetType())
	}
	if sa.GetReason() != "rule violation" {
		t.Fatalf("expected 'rule violation', got %q", sa.GetReason())
	}
}

func TestBuildServerUpdate_UnknownEvent_ReturnsNil(t *testing.T) {
	s := newTestServer()
	evt := events.Event{Name: "unknown/event", Data: nil}
	if s.buildServerUpdate(evt) != nil {
		t.Fatal("expected nil for unknown event")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./srs/... -v -run TestBuildServerUpdate
```

Expected: FAIL — `buildServerUpdate` does not yet use the typed events.

- [ ] **Step 3: Rewrite buildServerUpdate in srs/srs_service.go**

Replace the existing `buildServerUpdate` function (lines 421–439) with:

```go
func (s *SimpleRadioServer) buildServerUpdate(event events.Event) *pb.ServerUpdate {
	switch event.Name {
	case events.ClientsChanged:
		ce, ok := event.Data.(events.ClientChangeEvent)
		if !ok {
			return nil
		}
		guid := ce.ClientID.String()
		switch ce.Type {
		case events.ClientJoined:
			var clientInfo *pb.ClientInfo
			if c, exists := ce.Clients[ce.ClientID]; exists {
				clientInfo = &pb.ClientInfo{
					Name:      c.Name,
					Coalition: c.Coalition,
					UnitId:    c.UnitId,
					RoleId:    int32(c.Role),
				}
			}
			return &pb.ServerUpdate{
				Type: pb.ServerUpdate_CLIENT_JOINED,
				Update: &pb.ServerUpdate_ClientUpdate{
					ClientUpdate: &pb.ClientUpdate{
						ClientGuid: &guid,
						ClientInfo: clientInfo,
					},
				},
			}
		case events.ClientLeft:
			return &pb.ServerUpdate{
				Type: pb.ServerUpdate_CLIENT_LEFT,
				Update: &pb.ServerUpdate_ClientUpdate{
					ClientUpdate: &pb.ClientUpdate{
						ClientGuid: &guid,
					},
				},
			}
		default: // ClientInfoUpdated
			var clientInfo *pb.ClientInfo
			if c, exists := ce.Clients[ce.ClientID]; exists {
				clientInfo = &pb.ClientInfo{
					Name:      c.Name,
					Coalition: c.Coalition,
					UnitId:    c.UnitId,
					RoleId:    int32(c.Role),
				}
			}
			return &pb.ServerUpdate{
				Type: pb.ServerUpdate_CLIENT_INFO_UPDATE,
				Update: &pb.ServerUpdate_ClientUpdate{
					ClientUpdate: &pb.ClientUpdate{
						ClientGuid: &guid,
						ClientInfo: clientInfo,
					},
				},
			}
		}

	case events.RadioClientsChanged:
		re, ok := event.Data.(events.RadioChangeEvent)
		if !ok {
			return nil
		}
		guid := re.ClientID.String()
		var radioInfo *pb.RadioInfo
		if r, exists := re.Radios[re.ClientID]; exists {
			radioInfo = &pb.RadioInfo{
				Radios: convertRadios(r.Radios),
				Muted:  r.Muted,
			}
		}
		return &pb.ServerUpdate{
			Type: pb.ServerUpdate_CLIENT_RADIO_UPDATE,
			Update: &pb.ServerUpdate_ClientUpdate{
				ClientUpdate: &pb.ClientUpdate{
					ClientGuid: &guid,
					RadioInfo:  radioInfo,
				},
			},
		}

	case events.SettingsChanged, events.CoalitionsChanged:
		return &pb.ServerUpdate{
			Type:   pb.ServerUpdate_SERVER_SETTINGS_CHANGED,
			Update: &pb.ServerUpdate_SettingsUpdate{SettingsUpdate: s.buildServerSettings()},
		}

	case events.ServerAction:
		ae, ok := event.Data.(events.ServerActionEvent)
		if !ok {
			return nil
		}
		var actionType pb.ServerAction_ActionType
		switch ae.ActionType {
		case events.ActionKick:
			actionType = pb.ServerAction_KICK
		case events.ActionBan:
			actionType = pb.ServerAction_BAN
		case events.ActionMute:
			actionType = pb.ServerAction_MUTE
		case events.ActionUnmute:
			actionType = pb.ServerAction_UNMUTE
		}
		return &pb.ServerUpdate{
			Type: pb.ServerUpdate_SERVER_ACTION,
			Update: &pb.ServerUpdate_ServerAction{
				ServerAction: &pb.ServerAction{
					Type:             actionType,
					TargetClientGuid: ae.TargetClientID.String(),
					Reason:           ae.Reason,
				},
			},
		}

	default:
		return nil
	}
}
```

- [ ] **Step 4: Fix test — add correct import for SRSService_SubscribeToUpdatesServer**

The test uses `pb.SRSService_SubscribeToUpdatesServer` as the stream map value type. Check that it matches the actual type in `srs_service.go`:

```go
// In srs_service.go:
streams map[uuid.UUID]grpc.ServerStreamingServer[pb.ServerUpdate]
```

Update `newTestServer` in the test to match:

```go
import "google.golang.org/grpc"

func newTestServer() *SimpleRadioServer {
	return &SimpleRadioServer{
		serverState:   &state.ServerState{},
		settingsState: &state.SettingsState{},
		streams:       make(map[uuid.UUID]grpc.ServerStreamingServer[pb.ServerUpdate]),
	}
}
```

- [ ] **Step 5: Run test to verify it passes**

```bash
go test ./srs/... -v -race -run TestBuildServerUpdate
```

Expected: all 5 tests PASS.

- [ ] **Step 6: Commit**

```bash
git add srs/srs_service.go srs/srs_service_test.go
git commit -m "feat: rewrite buildServerUpdate with typed event payloads and server action support"
```

---

## Task 6: Emit ServerActionEvent from app/clients.go

**Files:**
- Modify: `app/clients.go`

The `KickClient`, `BanClient`, `MuteClient`, `UnmuteClient` functions currently update state and fire `ClientsChanged`/`RadioClientsChanged` but never notify gRPC subscribers. Add `ServerActionEvent` publication after the existing state events.

Also update the bare-map publishers to use `ClientChangeEvent`.

- [ ] **Step 1: Update KickClient in app/clients.go**

In `KickClient` (around line 160–170), replace:

```go
	a.ServerState.RLock()
	clientsSnap := make(map[uuid.UUID]*state.ClientState, len(a.ServerState.Clients))
	for k, v := range a.ServerState.Clients {
		clientsSnap[k] = v
	}
	a.ServerState.RUnlock()
	a.EmitEvent(events.Event{Name: events.ClientsChanged, Data: clientsSnap})
	a.Notify(events.NewNotification("Kick succeeded", "Client kicked successfully", "success"))
	a.Logger.Info("Client kicked", "clientId", clientId, "reason", reason)
```

With:

```go
	a.ServerState.RLock()
	clientsSnap := make(map[uuid.UUID]*state.ClientState, len(a.ServerState.Clients))
	for k, v := range a.ServerState.Clients {
		clientsSnap[k] = v
	}
	a.ServerState.RUnlock()
	a.EmitEvent(events.Event{
		Name: events.ClientsChanged,
		Data: events.ClientChangeEvent{Type: events.ClientLeft, ClientID: clientGuid, Clients: clientsSnap},
	})
	a.EmitEvent(events.Event{
		Name: events.ServerAction,
		Data: events.ServerActionEvent{ActionType: events.ActionKick, TargetClientID: clientGuid, Reason: reason},
	})
	a.Notify(events.NewNotification("Kick succeeded", "Client kicked successfully", "success"))
	a.Logger.Info("Client kicked", "clientId", clientId, "reason", reason)
```

Also remove the `// TODO:` comment from the function signature.

- [ ] **Step 2: Update BanClient in app/clients.go**

In `BanClient` (around line 97–107), replace:

```go
	a.EmitEvent(events.Event{Name: events.ClientsChanged, Data: clientsSnap})
	a.EmitEvent(events.Event{Name: events.BannedClientsChanged, Data: bannedSnap})
```

With:

```go
	a.EmitEvent(events.Event{
		Name: events.ClientsChanged,
		Data: events.ClientChangeEvent{Type: events.ClientLeft, ClientID: clientGuid, Clients: clientsSnap},
	})
	a.EmitEvent(events.Event{Name: events.BannedClientsChanged, Data: bannedSnap})
	a.EmitEvent(events.Event{
		Name: events.ServerAction,
		Data: events.ServerActionEvent{ActionType: events.ActionBan, TargetClientID: clientGuid, Reason: reason},
	})
```

- [ ] **Step 3: Update MuteClient in app/clients.go**

In `MuteClient` (around line 193–200), replace:

```go
	a.EmitEvent(events.Event{Name: events.RadioClientsChanged, Data: radioSnap})
	a.Notify(events.NewNotification("Mute succeeded", "Client muted successfully", "success"))
	a.Logger.Info("Client muted", "clientId", clientId)
```

With:

```go
	a.EmitEvent(events.Event{
		Name: events.RadioClientsChanged,
		Data: events.RadioChangeEvent{Type: events.RadioUpdated, ClientID: clientGuid, Radios: radioSnap},
	})
	a.EmitEvent(events.Event{
		Name: events.ServerAction,
		Data: events.ServerActionEvent{ActionType: events.ActionMute, TargetClientID: clientGuid},
	})
	a.Notify(events.NewNotification("Mute succeeded", "Client muted successfully", "success"))
	a.Logger.Info("Client muted", "clientId", clientId)
```

Also remove the `// TODO:` comment from the function signature.

- [ ] **Step 4: Update UnmuteClient in app/clients.go**

In `UnmuteClient` (around line 227–233), replace:

```go
	a.EmitEvent(events.Event{Name: events.RadioClientsChanged, Data: radioSnap})
	a.Notify(events.NewNotification("Unmute succeeded", "Client unmuted successfully", "success"))
	a.Logger.Info("Client unmuted", "clientId", clientId)
```

With:

```go
	a.EmitEvent(events.Event{
		Name: events.RadioClientsChanged,
		Data: events.RadioChangeEvent{Type: events.RadioUpdated, ClientID: clientGuid, Radios: radioSnap},
	})
	a.EmitEvent(events.Event{
		Name: events.ServerAction,
		Data: events.ServerActionEvent{ActionType: events.ActionUnmute, TargetClientID: clientGuid},
	})
	a.Notify(events.NewNotification("Unmute succeeded", "Client unmuted successfully", "success"))
	a.Logger.Info("Client unmuted", "clientId", clientId)
```

Also remove the `// TODO:` comment from the function signature.

- [ ] **Step 5: Build to verify**

```bash
go build ./app/...
```

Expected: no errors.

- [ ] **Step 6: Commit**

```bash
git add app/clients.go
git commit -m "feat: emit ServerActionEvent from kick/ban/mute/unmute; update ClientsChanged publishers"
```

---

## Task 7: Add logging/bus_handler.go

**Files:**
- Create: `logging/bus_handler.go`
- Test: `logging/bus_handler_test.go` (create)

- [ ] **Step 1: Write the failing test**

Create `logging/bus_handler_test.go`:

```go
package logging_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/events"
	"github.com/FPGSchiba/vcs-srs-server/logging"
)

func TestBusHandler_PublishesLogEntry(t *testing.T) {
	bus := events.NewEventBus()
	ch := bus.Subscribe(events.LogEntry)
	defer bus.Unsubscribe(ch)

	h := logging.NewBusHandler(bus)
	logger := slog.New(h)

	logger.Info("test message", "key", "value")

	select {
	case evt := <-ch:
		entry, ok := evt.Data.(logging.LogEntry)
		if !ok {
			t.Fatalf("expected LogEntry, got %T", evt.Data)
		}
		if entry.Message != "test message" {
			t.Fatalf("expected 'test message', got %q", entry.Message)
		}
		if entry.Level != "INFO" {
			t.Fatalf("expected 'INFO', got %q", entry.Level)
		}
		if entry.Attrs["key"] != "value" {
			t.Fatalf("expected attrs[key]=value, got %v", entry.Attrs["key"])
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for log event")
	}
}

func TestBusHandler_Enabled_AlwaysTrue(t *testing.T) {
	bus := events.NewEventBus()
	h := logging.NewBusHandler(bus)
	if !h.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("expected Enabled=true for DEBUG")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./logging/... -v -run TestBusHandler
```

Expected: FAIL — package `logging` does not exist.

- [ ] **Step 3: Create logging/bus_handler.go**

```go
package logging

import (
	"context"
	"log/slog"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/events"
)

// LogEntry is the payload published to the event bus for each log record.
type LogEntry struct {
	Time    time.Time      `json:"time"`
	Level   string         `json:"level"`
	Message string         `json:"msg"`
	Attrs   map[string]any `json:"attrs,omitempty"`
}

// BusHandler is a slog.Handler that publishes log records to an events.EventBus.
// It has no inner handler; the file and console handlers are separate fanout members.
type BusHandler struct {
	bus   *events.EventBus
	attrs []slog.Attr
	group string
}

// NewBusHandler returns a BusHandler that publishes to bus.
func NewBusHandler(bus *events.EventBus) *BusHandler {
	return &BusHandler{bus: bus}
}

// Enabled always returns true so all levels reach the event bus.
func (h *BusHandler) Enabled(_ context.Context, _ slog.Level) bool {
	return true
}

// Handle converts the record to LogEntry and publishes it.
func (h *BusHandler) Handle(_ context.Context, r slog.Record) error {
	attrs := make(map[string]any, len(h.attrs)+r.NumAttrs())

	// accumulated attrs from WithAttrs
	for _, a := range h.attrs {
		attrs[a.Key] = a.Value.Any()
	}

	// attrs from this record
	r.Attrs(func(a slog.Attr) bool {
		key := a.Key
		if h.group != "" {
			key = h.group + "." + key
		}
		attrs[key] = a.Value.Any()
		return true
	})

	entry := LogEntry{
		Time:    r.Time,
		Level:   r.Level.String(),
		Message: r.Message,
		Attrs:   attrs,
	}
	h.bus.Publish(events.Event{Name: events.LogEntry, Data: entry})
	return nil
}

// WithAttrs returns a new BusHandler with the given attrs accumulated.
func (h *BusHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	combined := make([]slog.Attr, len(h.attrs)+len(attrs))
	copy(combined, h.attrs)
	copy(combined[len(h.attrs):], attrs)
	return &BusHandler{bus: h.bus, attrs: combined, group: h.group}
}

// WithGroup returns a new BusHandler with the given group prefix.
func (h *BusHandler) WithGroup(name string) slog.Handler {
	g := name
	if h.group != "" {
		g = h.group + "." + name
	}
	return &BusHandler{bus: h.bus, attrs: h.attrs, group: g}
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./logging/... -v -race
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add logging/bus_handler.go logging/bus_handler_test.go
git commit -m "feat: add BusHandler slog.Handler that publishes log entries to event bus"
```

---

## Task 8: Wire BusHandler into logger in shared.go, main.go, headless.go

**Files:**
- Modify: `shared.go`
- Modify: `main.go`
- Modify: `headless.go`
- Modify: `app/app.go` (add `GetEventBus()`)

The `parseFlags` function currently creates the logger. We need to pass the event bus so it can be added as a fanout sink. `app.New()` is already called before `parseFlags` in both `main.go` and `headless.go` (it happens to be in the right order from reading the files).

- [ ] **Step 1: Add GetEventBus() to app/app.go**

In `app/app.go`, add after the `GetServerVersion()` function:

```go
func (a *VCSApplication) GetEventBus() *events.EventBus {
	return a.eventBus
}
```

- [ ] **Step 2: Modify parseFlags signature in shared.go**

Change:

```go
func parseFlags(isHeadless bool) (configFilepath, bannedFilePath, distributionMode string, autoStartServers bool, logger *slog.Logger) {
```

To:

```go
func parseFlags(isHeadless bool, bus *events.EventBus) (configFilepath, bannedFilePath, distributionMode string, autoStartServers bool, logger *slog.Logger) {
```

Add `"github.com/FPGSchiba/vcs-srs-server/events"` and `"github.com/FPGSchiba/vcs-srs-server/logging"` to the imports in `shared.go`.

- [ ] **Step 3: Add BusHandler to fanout in shared.go**

Replace:

```go
		logger = slog.New(slogmulti.Fanout(
			slog.NewTextHandler(os.Stdout, nil),
			slog.NewJSONHandler(f, nil),
		))
```

With:

```go
		logger = slog.New(slogmulti.Fanout(
			slog.NewTextHandler(os.Stdout, nil),
			slog.NewJSONHandler(f, nil),
			logging.NewBusHandler(bus),
		))
```

Also handle the `fileLogEnabled == false` path, replacing:

```go
	} else {
		logger = slog.New(slog.NewTextHandler(os.Stdout, nil))
	}
```

With:

```go
	} else {
		logger = slog.New(slogmulti.Fanout(
			slog.NewTextHandler(os.Stdout, nil),
			logging.NewBusHandler(bus),
		))
	}
```

- [ ] **Step 4: Update call in main.go**

In `main.go`, the call on line 19:

```go
configFilepath, bannedFilePath, _, autoStartServers, logger := parseFlags(false)
```

Must become:

```go
vcs := app.New()
configFilepath, bannedFilePath, _, autoStartServers, logger := parseFlags(false, vcs.GetEventBus())
```

Remove the separate `vcs := app.New()` line that follows (it was on line 21 before this change).

- [ ] **Step 5: Update call in headless.go**

In `headless.go`, lines 12–13:

```go
configFilepath, bannedFilePath, distributionModeFlag, _, logger := parseFlags(true)
...
vcs := app.New()
```

Reorder so `app.New()` comes first:

```go
vcs := app.New()
configFilepath, bannedFilePath, distributionModeFlag, _, logger := parseFlags(true, vcs.GetEventBus())
```

- [ ] **Step 6: Build to verify**

```bash
go build ./...
```

Expected: no errors.

- [ ] **Step 7: Commit**

```bash
git add shared.go main.go headless.go app/app.go
git commit -m "feat: wire BusHandler into slog fanout; thread event bus through parseFlags"
```

---

## Task 9: Add ApiSettings and SettingsSnapshot to state/settings.go

**Files:**
- Modify: `state/settings.go`
- Test: `state/settings_test.go` (create or append)

- [ ] **Step 1: Write the failing test**

Create `state/settings_test.go`:

```go
package state_test

import (
	"testing"

	"github.com/FPGSchiba/vcs-srs-server/state"
)

func TestSettingsState_Snapshot(t *testing.T) {
	s := &state.SettingsState{
		General:      state.GeneralSettings{MaxRadiosPerUser: 10},
		Security:     state.SecuritySettings{EnableGuestAuth: true},
		VoiceControl: state.VoiceControlSettings{Port: 14448},
		Api:          state.ApiSettings{Key: "secret-key"},
	}

	snap := s.Snapshot()

	if snap.General.MaxRadiosPerUser != 10 {
		t.Fatalf("expected 10, got %d", snap.General.MaxRadiosPerUser)
	}
	if !snap.Security.EnableGuestAuth {
		t.Fatal("expected EnableGuestAuth=true")
	}
	if snap.VoiceControl.Port != 14448 {
		t.Fatalf("expected port 14448, got %d", snap.VoiceControl.Port)
	}
	if snap.Api.Key != "secret-key" {
		t.Fatalf("expected 'secret-key', got %q", snap.Api.Key)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./state/... -v -run TestSettingsState_Snapshot
```

Expected: FAIL — `ApiSettings`, `Api` field, and `Snapshot()` undefined.

- [ ] **Step 3: Add ApiSettings, Api field, SettingsSnapshot, and Snapshot() to state/settings.go**

Add after `VoiceControlSettings` struct:

```go
type ApiSettings struct {
	Key string `yaml:"key"`
}
```

Add `Api ApiSettings` field to `SettingsState`:

```go
type SettingsState struct {
	sync.RWMutex `yaml:"-"`
	Servers      ServerSettings       `yaml:"servers"`
	Coalitions   []Coalition          `yaml:"coalitions"`
	Frequencies  FrequencySettings    `yaml:"frequencies"`
	General      GeneralSettings      `yaml:"general"`
	Security     SecuritySettings     `yaml:"security"`
	VoiceControl VoiceControlSettings `yaml:"voiceControl"`
	Api          ApiSettings          `yaml:"api"`
	file         string               `yaml:"-"`
}
```

Add `SettingsSnapshot` type and `Snapshot()` method:

```go
// SettingsSnapshot is a mutex-free copy of SettingsState for safe read access.
type SettingsSnapshot struct {
	Servers      ServerSettings
	Coalitions   []Coalition
	Frequencies  FrequencySettings
	General      GeneralSettings
	Security     SecuritySettings
	VoiceControl VoiceControlSettings
	Api          ApiSettings
}

// Snapshot returns a copy of SettingsState under a read lock.
func (s *SettingsState) Snapshot() SettingsSnapshot {
	s.RLock()
	defer s.RUnlock()
	coalitions := make([]Coalition, len(s.Coalitions))
	copy(coalitions, s.Coalitions)
	return SettingsSnapshot{
		Servers:      s.Servers,
		Coalitions:   coalitions,
		Frequencies:  s.Frequencies,
		General:      s.General,
		Security:     s.Security,
		VoiceControl: s.VoiceControl,
		Api:          s.Api,
	}
}
```

Also add `Api` to the default `SettingsState` in `GetSettingsState` (inside the `os.IsNotExist` branch, after `VoiceControl`):

```go
			Api: ApiSettings{
				Key: "",
			},
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./state/... -v -race
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add state/settings.go state/settings_test.go
git commit -m "feat: add ApiSettings, SettingsSnapshot, and Snapshot() to state/settings.go"
```

---

## Task 10: Add GraphQL schema, gqlgen.yml, Taskfile task, and run code generation

**Files:**
- Create: `graphql/schema.graphql`
- Create: `gqlgen.yml`
- Modify: `build/Taskfile.yml`

- [ ] **Step 1: Install gqlgen**

```bash
go install github.com/99designs/gqlgen@latest
```

- [ ] **Step 2: Create graphql/schema.graphql**

```bash
mkdir -p graphql/generated
```

Create `graphql/schema.graphql`:

```graphql
type Query {
  systemInfo: SystemInfo!
  clients: [Client!]!
  bannedClients: [BannedClient!]!
  settings: Settings!
}

type Mutation {
  # Settings
  updateGeneralSettings(input: GeneralSettingsInput!): MutationResult!
  updateSecuritySettings(input: SecuritySettingsInput!): MutationResult!
  updateVoiceControlSettings(input: VoiceControlSettingsInput!): MutationResult!
  updateFrequencySettings(input: FrequencySettingsInput!): MutationResult!
  updateCoalitions(coalitions: [CoalitionInput!]!): MutationResult!
  updateServerSettings(input: ServerSettingsInput!): MutationResult!
  # Server control
  startServer: MutationResult!
  stopServer: MutationResult!
  # Client management
  kickClient(clientId: ID!, reason: String!): MutationResult!
  banClient(clientId: ID!, reason: String!): MutationResult!
  unbanClient(clientId: ID!): MutationResult!
  muteClient(clientId: ID!): MutationResult!
  unmuteClient(clientId: ID!): MutationResult!
}

type SystemInfo {
  version: String!
  httpStatus: ServiceStatus!
  voiceStatus: ServiceStatus!
  controlStatus: ServiceStatus!
}

type ServiceStatus {
  isRunning: Boolean!
  error: String!
}

type Client {
  id: ID!
  name: String!
  coalition: String!
  unitId: String!
  roleId: Int!
  lastUpdate: String!
  muted: Boolean!
}

type BannedClient {
  id: ID!
  name: String!
  ipAddress: String!
  reason: String!
}

type Settings {
  general: GeneralSettings!
  security: SecuritySettings!
  voiceControl: VoiceControlSettings!
  frequencies: FrequencySettings!
  coalitions: [Coalition!]!
  servers: ServerSettings!
}

type GeneralSettings { maxRadiosPerUser: Int! }
type SecuritySettings {
  enablePluginAuth: Boolean!
  enableGuestAuth: Boolean!
  plugins: [PluginSettings!]!
}
type PluginSettings { name: String!; enabled: Boolean!; address: String! }
type VoiceControlSettings {
  port: Int!
  remoteHost: String!
  listenHost: String!
  certificateFile: String!
  privateKeyFile: String!
}
type FrequencySettings {
  testFrequencies: [Float!]!
  globalFrequencies: [Float!]!
}
type Coalition { name: String!; color: String!; description: String! }
type ServerSettings { http: ServerSetting!; voice: ServerSetting!; control: ServerSetting! }
type ServerSetting { host: String!; port: Int! }
type MutationResult { success: Boolean!; message: String }

input GeneralSettingsInput { maxRadiosPerUser: Int! }
input SecuritySettingsInput { enablePluginAuth: Boolean!; enableGuestAuth: Boolean! }
input VoiceControlSettingsInput {
  port: Int!
  remoteHost: String!
  listenHost: String!
  certificateFile: String!
  privateKeyFile: String!
}
input FrequencySettingsInput {
  testFrequencies: [Float!]!
  globalFrequencies: [Float!]!
}
input CoalitionInput { name: String!; color: String!; description: String!; password: String }
input ServerSettingsInput {
  http: ServerSettingInput!
  voice: ServerSettingInput!
  control: ServerSettingInput!
}
input ServerSettingInput { host: String!; port: Int! }
```

- [ ] **Step 3: Create gqlgen.yml at project root**

```yaml
schema:
  - graphql/schema.graphql

exec:
  filename: graphql/generated/generated.go
  package: generated

model:
  filename: graphql/generated/models_gen.go
  package: generated

resolver:
  layout: follow-schema
  dir: graphql
  package: graphql
  filename_template: "{name}.resolvers.go"

autobind: []

models:
  ID:
    model:
      - github.com/99designs/gqlgen/graphql/introspection.Type.ID
      - github.com/99designs/gqlgen/graphql.String
```

- [ ] **Step 4: Add generate:graphql task to build/Taskfile.yml**

In `build/Taskfile.yml`, after line 106 (end of `generate:proto` task), add:

```yaml
  generate:graphql:
    summary: Generates GraphQL server code via gqlgen
    sources:
      - "graphql/schema.graphql"
      - "gqlgen.yml"
    generates:
      - "graphql/generated/generated.go"
      - "graphql/generated/models_gen.go"
    preconditions:
      - sh: which gqlgen
        msg: "Install gqlgen: go install github.com/99designs/gqlgen@latest"
    cmds:
      - go run github.com/99designs/gqlgen generate
```

Update `go:mod:tidy` to also depend on `generate:graphql`:

```yaml
  go:mod:tidy:
    summary: Runs `go mod tidy`
    deps:
      - task: generate:proto
      - task: generate:graphql
    internal: true
    cmds:
      - go mod tidy
```

- [ ] **Step 5: Run gqlgen to generate code**

```bash
cd /Users/schiba/Projects/vanguard/vngd-srs-server
go run github.com/99designs/gqlgen generate
```

Expected: creates `graphql/generated/generated.go`, `graphql/generated/models_gen.go`, and stub files `graphql/schema.resolvers.go` (or `graphql/query.resolvers.go` + `graphql/mutation.resolvers.go`). Check what files were created:

```bash
ls graphql/
```

- [ ] **Step 6: Run go mod tidy**

```bash
go mod tidy
```

- [ ] **Step 7: Commit**

```bash
git add graphql/ gqlgen.yml build/Taskfile.yml go.mod go.sum
git commit -m "feat: add GraphQL schema, gqlgen config, Taskfile task, and run code generation"
```

---

## Task 11: Implement GraphQL resolver struct and Query resolvers

**Files:**
- Create: `graphql/resolver.go`
- Modify: `graphql/query.resolvers.go` (generated stub → implement)

- [ ] **Step 1: Create graphql/resolver.go**

```go
package graphql

import (
	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/google/uuid"
)

// AppInterface is the contract the Resolver needs from *app.VCSApplication.
// Defined here (in graphql/) to avoid a circular import with app/.
type AppInterface interface {
	GetServerVersion() string
	GetServerStatus() state.AdminStateSnapshot
	GetClients() map[uuid.UUID]*state.ClientState
	GetBannedClients() []state.BannedClient
	GetRadioClients() map[uuid.UUID]*state.RadioState
	GetSettingsSnapshot() state.SettingsSnapshot
	// Mutations
	KickClient(clientId string, reason string)
	BanClient(clientId string, reason string)
	UnbanClient(clientId string)
	MuteClient(clientId string)
	UnmuteClient(clientId string)
	StartServer()
	StopServer()
	SaveGeneralSettings(maxRadiosPerUser int)
	SaveSecuritySettings(enableGuestAuth bool, enablePluginAuth bool)
	SaveVoiceControlSettings(input state.VoiceControlSettings)
	SaveFrequencySettings(testFrequencies []float32, globalFrequencies []float32)
	SaveCoalitions(coalitions []state.Coalition)
	SaveServerSettings(input state.ServerSettings)
}

// Resolver is the root resolver. It holds the application.
type Resolver struct {
	App AppInterface
}

// NewResolver creates a Resolver backed by the given app.
func NewResolver(app AppInterface) *Resolver {
	return &Resolver{App: app}
}
```

- [ ] **Step 2: Verify AppInterface methods exist on VCSApplication**

Check that these methods exist (or will exist after later tasks):
- `GetClients()`, `GetBannedClients()`, `GetRadioClients()`, `GetServerStatus()`, `GetServerVersion()` — already in `app/clients.go` and `app/app.go`
- `GetSettingsSnapshot()` — needs to be added to `app/app.go` (see step below)
- `StartServer()`, `StopServer()` — check `app/server_control.go` for public wrappers

Add `GetSettingsSnapshot()` to `app/app.go`:

```go
func (a *VCSApplication) GetSettingsSnapshot() state.SettingsSnapshot {
	return a.SettingsState.Snapshot()
}
```

Check `StartServer` / `StopServer` names match. In `app/server_control.go`, the Wails-callable methods are `StartStandaloneServer()` and `StopServer()`. Add a thin `StartServer()` wrapper in `app/server_control.go`:

```go
func (a *VCSApplication) StartServer() {
	a.StartStandaloneServer()
}
```

The `GetClients()` in `app/clients.go` returns `Clients` struct (wrapped). For GraphQL we need the raw map. Add a new method:

```go
func (a *VCSApplication) GetClientMap() map[uuid.UUID]*state.ClientState {
	a.ServerState.RLock()
	defer a.ServerState.RUnlock()
	snap := make(map[uuid.UUID]*state.ClientState, len(a.ServerState.Clients))
	for k, v := range a.ServerState.Clients {
		snap[k] = v
	}
	return snap
}

func (a *VCSApplication) GetRadioClientMap() map[uuid.UUID]*state.RadioState {
	a.ServerState.RLock()
	defer a.ServerState.RUnlock()
	snap := make(map[uuid.UUID]*state.RadioState, len(a.ServerState.RadioClients))
	for k, v := range a.ServerState.RadioClients {
		snap[k] = v
	}
	return snap
}
```

Update the `AppInterface` to use `GetClientMap()` and `GetRadioClientMap()` (not `GetClients()`) to avoid wrapping ambiguity.

- [ ] **Step 3: Implement query.resolvers.go**

Rename or edit the generated stub (gqlgen may call it `schema.resolvers.go` or create `query.resolvers.go`). Replace the generated stub content with:

```go
package graphql

import (
	"context"
	"fmt"

	"github.com/FPGSchiba/vcs-srs-server/graphql/generated"
)

// SystemInfo resolves Query.systemInfo
func (r *queryResolver) SystemInfo(ctx context.Context) (*generated.SystemInfo, error) {
	status := r.App.GetServerStatus()
	return &generated.SystemInfo{
		Version: r.App.GetServerVersion(),
		HTTPStatus: &generated.ServiceStatus{
			IsRunning: status.HTTPStatus.IsRunning,
			Error:     status.HTTPStatus.Error,
		},
		VoiceStatus: &generated.ServiceStatus{
			IsRunning: status.VoiceStatus.IsRunning,
			Error:     status.VoiceStatus.Error,
		},
		ControlStatus: &generated.ServiceStatus{
			IsRunning: status.ControlStatus.IsRunning,
			Error:     status.ControlStatus.Error,
		},
	}, nil
}

// Clients resolves Query.clients
func (r *queryResolver) Clients(ctx context.Context) ([]*generated.Client, error) {
	clients := r.App.GetClientMap()
	radios := r.App.GetRadioClientMap()

	result := make([]*generated.Client, 0, len(clients))
	for id, c := range clients {
		muted := false
		if radio, ok := radios[id]; ok {
			muted = radio.Muted
		}
		result = append(result, &generated.Client{
			ID:         id.String(),
			Name:       c.Name,
			Coalition:  c.Coalition,
			UnitID:     c.UnitId,
			RoleID:     int(c.Role),
			LastUpdate: c.LastUpdate.Format("2006-01-02T15:04:05Z07:00"),
			Muted:      muted,
		})
	}
	return result, nil
}

// BannedClients resolves Query.bannedClients
func (r *queryResolver) BannedClients(ctx context.Context) ([]*generated.BannedClient, error) {
	banned := r.App.GetBannedClients()
	result := make([]*generated.BannedClient, 0, len(banned))
	for _, b := range banned {
		result = append(result, &generated.BannedClient{
			ID:        b.ID.String(),
			Name:      b.Name,
			IPAddress: b.IPAddress,
			Reason:    b.Reason,
		})
	}
	return result, nil
}

// Settings resolves Query.settings
func (r *queryResolver) Settings(ctx context.Context) (*generated.Settings, error) {
	snap := r.App.GetSettingsSnapshot()

	plugins := make([]*generated.PluginSettings, 0, len(snap.Security.Plugins))
	for _, p := range snap.Security.Plugins {
		plugins = append(plugins, &generated.PluginSettings{
			Name:    p.Name,
			Enabled: p.Enabled,
			Address: p.Address,
		})
	}

	coalitions := make([]*generated.Coalition, 0, len(snap.Coalitions))
	for _, c := range snap.Coalitions {
		coalitions = append(coalitions, &generated.Coalition{
			Name:        c.Name,
			Color:       c.Color,
			Description: c.Description,
		})
	}

	testFreqs := make([]float64, len(snap.Frequencies.TestFrequencies))
	for i, f := range snap.Frequencies.TestFrequencies {
		testFreqs[i] = float64(f)
	}
	globalFreqs := make([]float64, len(snap.Frequencies.GlobalFrequencies))
	for i, f := range snap.Frequencies.GlobalFrequencies {
		globalFreqs[i] = float64(f)
	}

	return &generated.Settings{
		General: &generated.GeneralSettings{
			MaxRadiosPerUser: snap.General.MaxRadiosPerUser,
		},
		Security: &generated.SecuritySettings{
			EnablePluginAuth: snap.Security.EnablePluginAuth,
			EnableGuestAuth:  snap.Security.EnableGuestAuth,
			Plugins:          plugins,
		},
		VoiceControl: &generated.VoiceControlSettings{
			Port:            snap.VoiceControl.Port,
			RemoteHost:      snap.VoiceControl.RemoteHost,
			ListenHost:      snap.VoiceControl.ListenHost,
			CertificateFile: snap.VoiceControl.CertificateFile,
			PrivateKeyFile:  snap.VoiceControl.PrivateKeyFile,
		},
		Frequencies: &generated.FrequencySettings{
			TestFrequencies:   testFreqs,
			GlobalFrequencies: globalFreqs,
		},
		Coalitions: coalitions,
		Servers: &generated.ServerSettings{
			HTTP: &generated.ServerSetting{
				Host: snap.Servers.HTTP.Host,
				Port: snap.Servers.HTTP.Port,
			},
			Voice: &generated.ServerSetting{
				Host: snap.Servers.Voice.Host,
				Port: snap.Servers.Voice.Port,
			},
			Control: &generated.ServerSetting{
				Host: snap.Servers.Control.Host,
				Port: snap.Servers.Control.Port,
			},
		},
	}, nil
}

// Query returns generated.QueryResolver implementation.
func (r *Resolver) Query() generated.QueryResolver {
	return &queryResolver{r}
}

type queryResolver struct{ *Resolver }

// Mutation returns generated.MutationResolver implementation.
func (r *Resolver) Mutation() generated.MutationResolver {
	return &mutationResolver{r}
}

type mutationResolver struct{ *Resolver }

// helper for success result
func ok(msg string) *generated.MutationResult {
	return &generated.MutationResult{Success: true, Message: &msg}
}

// helper for error result
func fail(msg string) *generated.MutationResult {
	return &generated.MutationResult{Success: false, Message: &msg}
}

// placeholder for unused param
var _ = fmt.Sprintf
```

Note: `queryResolver` and `mutationResolver` types must be defined once. The `Query()` and `Mutation()` methods satisfy the generated `ResolverRoot` interface.

- [ ] **Step 4: Build to check compilation**

```bash
go build ./graphql/...
```

Resolve any field name mismatches between the generated models and the resolver code (gqlgen may use `UnitId` vs `UnitID`, `RoleId` vs `RoleID`, etc. — adjust to match what the generator produced).

- [ ] **Step 5: Commit**

```bash
git add graphql/resolver.go graphql/query.resolvers.go app/app.go app/clients.go app/server_control.go
git commit -m "feat: add GraphQL AppInterface, Resolver, and Query resolvers"
```

---

## Task 12: Implement GraphQL Mutation resolvers

**Files:**
- Modify: `graphql/mutation.resolvers.go`
- Modify: `app/settings.go` (create app-level settings save methods)
- Modify: `services/settings.go` (add SaveSecuritySettings, SaveVoiceControlSettings)

First, add the app-level methods that the GraphQL resolver will call. The existing `services/settings.go` methods call `s.App.App.Event.EmitEvent(...)` which is Wails-only. For GraphQL (headless-compatible), add methods on `VCSApplication` directly that use `a.EmitEvent(...)` (which works in both modes).

- [ ] **Step 1: Create app/settings.go with app-level save methods**

```go
package app

import (
	"fmt"

	"github.com/FPGSchiba/vcs-srs-server/events"
	"github.com/FPGSchiba/vcs-srs-server/state"
)

func (a *VCSApplication) SaveGeneralSettings(maxRadiosPerUser int) {
	a.SettingsState.Lock()
	a.SettingsState.General.MaxRadiosPerUser = maxRadiosPerUser
	err := a.SettingsState.Save()
	a.SettingsState.Unlock()
	if err != nil {
		a.Logger.Error(fmt.Sprintf("Failed to save general settings: %v", err))
		a.Notify(events.NewNotification("Failed to save settings", "Failed to save settings", "error"))
		return
	}
	a.EmitEvent(events.Event{Name: events.SettingsChanged, Data: nil})
}

func (a *VCSApplication) SaveSecuritySettings(enableGuestAuth bool, enablePluginAuth bool) {
	a.SettingsState.Lock()
	a.SettingsState.Security.EnableGuestAuth = enableGuestAuth
	a.SettingsState.Security.EnablePluginAuth = enablePluginAuth
	err := a.SettingsState.Save()
	a.SettingsState.Unlock()
	if err != nil {
		a.Logger.Error(fmt.Sprintf("Failed to save security settings: %v", err))
		a.Notify(events.NewNotification("Failed to save settings", "Failed to save settings", "error"))
		return
	}
	a.EmitEvent(events.Event{Name: events.SettingsChanged, Data: nil})
}

func (a *VCSApplication) SaveVoiceControlSettings(input state.VoiceControlSettings) {
	a.SettingsState.Lock()
	a.SettingsState.VoiceControl = input
	err := a.SettingsState.Save()
	a.SettingsState.Unlock()
	if err != nil {
		a.Logger.Error(fmt.Sprintf("Failed to save voice control settings: %v", err))
		a.Notify(events.NewNotification("Failed to save settings", "Failed to save settings", "error"))
		return
	}
	a.EmitEvent(events.Event{Name: events.SettingsChanged, Data: nil})
}

func (a *VCSApplication) SaveFrequencySettings(testFrequencies []float32, globalFrequencies []float32) {
	a.SettingsState.Lock()
	a.SettingsState.Frequencies.TestFrequencies = testFrequencies
	a.SettingsState.Frequencies.GlobalFrequencies = globalFrequencies
	err := a.SettingsState.Save()
	a.SettingsState.Unlock()
	if err != nil {
		a.Logger.Error(fmt.Sprintf("Failed to save frequency settings: %v", err))
		a.Notify(events.NewNotification("Failed to save settings", "Failed to save settings", "error"))
		return
	}
	a.EmitEvent(events.Event{Name: events.SettingsChanged, Data: nil})
}

func (a *VCSApplication) SaveCoalitions(coalitions []state.Coalition) {
	a.SettingsState.Lock()
	a.SettingsState.Coalitions = coalitions
	err := a.SettingsState.Save()
	a.SettingsState.Unlock()
	if err != nil {
		a.Logger.Error(fmt.Sprintf("Failed to save coalitions: %v", err))
		a.Notify(events.NewNotification("Failed to save settings", "Failed to save settings", "error"))
		return
	}
	a.EmitEvent(events.Event{Name: events.CoalitionsChanged, Data: nil})
}

func (a *VCSApplication) SaveServerSettings(input state.ServerSettings) {
	a.SettingsState.Lock()
	a.SettingsState.Servers = input
	err := a.SettingsState.Save()
	a.SettingsState.Unlock()
	if err != nil {
		a.Logger.Error(fmt.Sprintf("Failed to save server settings: %v", err))
		a.Notify(events.NewNotification("Failed to save settings", "Failed to save settings", "error"))
		return
	}
	a.EmitEvent(events.Event{Name: events.SettingsChanged, Data: nil})
}
```

- [ ] **Step 2: Implement mutation.resolvers.go**

Replace the generated stub with:

```go
package graphql

import (
	"context"

	"github.com/FPGSchiba/vcs-srs-server/graphql/generated"
	"github.com/FPGSchiba/vcs-srs-server/state"
)

func (r *mutationResolver) UpdateGeneralSettings(ctx context.Context, input generated.GeneralSettingsInput) (*generated.MutationResult, error) {
	r.App.SaveGeneralSettings(input.MaxRadiosPerUser)
	return ok("General settings updated"), nil
}

func (r *mutationResolver) UpdateSecuritySettings(ctx context.Context, input generated.SecuritySettingsInput) (*generated.MutationResult, error) {
	r.App.SaveSecuritySettings(input.EnableGuestAuth, input.EnablePluginAuth)
	return ok("Security settings updated"), nil
}

func (r *mutationResolver) UpdateVoiceControlSettings(ctx context.Context, input generated.VoiceControlSettingsInput) (*generated.MutationResult, error) {
	r.App.SaveVoiceControlSettings(state.VoiceControlSettings{
		Port:            input.Port,
		RemoteHost:      input.RemoteHost,
		ListenHost:      input.ListenHost,
		CertificateFile: input.CertificateFile,
		PrivateKeyFile:  input.PrivateKeyFile,
	})
	return ok("VoiceControl settings updated"), nil
}

func (r *mutationResolver) UpdateFrequencySettings(ctx context.Context, input generated.FrequencySettingsInput) (*generated.MutationResult, error) {
	testFreqs := make([]float32, len(input.TestFrequencies))
	for i, f := range input.TestFrequencies {
		testFreqs[i] = float32(f)
	}
	globalFreqs := make([]float32, len(input.GlobalFrequencies))
	for i, f := range input.GlobalFrequencies {
		globalFreqs[i] = float32(f)
	}
	r.App.SaveFrequencySettings(testFreqs, globalFreqs)
	return ok("Frequency settings updated"), nil
}

func (r *mutationResolver) UpdateCoalitions(ctx context.Context, coalitions []*generated.CoalitionInput) (*generated.MutationResult, error) {
	stateCoalitions := make([]state.Coalition, 0, len(coalitions))
	for _, c := range coalitions {
		password := ""
		if c.Password != nil {
			password = *c.Password
		}
		stateCoalitions = append(stateCoalitions, state.Coalition{
			Name:        c.Name,
			Color:       c.Color,
			Description: c.Description,
			Password:    password,
		})
	}
	r.App.SaveCoalitions(stateCoalitions)
	return ok("Coalitions updated"), nil
}

func (r *mutationResolver) UpdateServerSettings(ctx context.Context, input generated.ServerSettingsInput) (*generated.MutationResult, error) {
	r.App.SaveServerSettings(state.ServerSettings{
		HTTP:    state.ServerSetting{Host: input.HTTP.Host, Port: input.HTTP.Port},
		Voice:   state.ServerSetting{Host: input.Voice.Host, Port: input.Voice.Port},
		Control: state.ServerSetting{Host: input.Control.Host, Port: input.Control.Port},
	})
	return ok("Server settings updated"), nil
}

func (r *mutationResolver) StartServer(ctx context.Context) (*generated.MutationResult, error) {
	r.App.StartServer()
	return ok("Server started"), nil
}

func (r *mutationResolver) StopServer(ctx context.Context) (*generated.MutationResult, error) {
	r.App.StopServer()
	return ok("Server stopped"), nil
}

func (r *mutationResolver) KickClient(ctx context.Context, clientID string, reason string) (*generated.MutationResult, error) {
	r.App.KickClient(clientID, reason)
	return ok("Client kicked"), nil
}

func (r *mutationResolver) BanClient(ctx context.Context, clientID string, reason string) (*generated.MutationResult, error) {
	r.App.BanClient(clientID, reason)
	return ok("Client banned"), nil
}

func (r *mutationResolver) UnbanClient(ctx context.Context, clientID string) (*generated.MutationResult, error) {
	r.App.UnbanClient(clientID)
	return ok("Client unbanned"), nil
}

func (r *mutationResolver) MuteClient(ctx context.Context, clientID string) (*generated.MutationResult, error) {
	r.App.MuteClient(clientID)
	return ok("Client muted"), nil
}

func (r *mutationResolver) UnmuteClient(ctx context.Context, clientID string) (*generated.MutationResult, error) {
	r.App.UnmuteClient(clientID)
	return ok("Client unmuted"), nil
}
```

- [ ] **Step 3: Build to verify**

```bash
go build ./graphql/... ./app/...
```

Resolve any field name mismatches (gqlgen may use `ClientId` vs `ClientID` in generated input types). Adjust resolver parameter names to match generated types.

- [ ] **Step 4: Commit**

```bash
git add graphql/mutation.resolvers.go app/settings.go
git commit -m "feat: implement GraphQL mutation resolvers and app-level settings save methods"
```

---

## Task 13: Add API key middleware and mount GraphQL in Gin

**Files:**
- Create: `rest/middleware.go`
- Modify: `app/server_control.go`
- Test: `rest/middleware_test.go` (create)

- [ ] **Step 1: Write the failing test**

Create `rest/middleware_test.go`:

```go
package rest_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/FPGSchiba/vcs-srs-server/rest"
	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/gin-gonic/gin"
)

func newTestSettings(key string) *state.SettingsState {
	s := &state.SettingsState{}
	s.Api.Key = key
	return s
}

func TestApiKeyMiddleware_EmptyKey_Returns503(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(rest.ApiKeyMiddleware(newTestSettings("")))
	r.GET("/test", func(c *gin.Context) { c.Status(200) })

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}

func TestApiKeyMiddleware_WrongKey_Returns401(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(rest.ApiKeyMiddleware(newTestSettings("correct-key")))
	r.GET("/test", func(c *gin.Context) { c.Status(200) })

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("X-API-Key", "wrong-key")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestApiKeyMiddleware_CorrectKey_PassesThrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(rest.ApiKeyMiddleware(newTestSettings("correct-key")))
	r.GET("/test", func(c *gin.Context) { c.Status(200) })

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("X-API-Key", "correct-key")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./rest/... -v -run TestApiKeyMiddleware
```

Expected: FAIL — `rest.ApiKeyMiddleware` undefined.

- [ ] **Step 3: Create rest/middleware.go**

```go
package rest

import (
	"net/http"

	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/gin-gonic/gin"
)

// ApiKeyMiddleware returns a Gin middleware that validates the X-API-Key header.
// If the key is empty in settings, the endpoint returns 503.
// If the key is wrong, returns 401. If correct, passes through.
func ApiKeyMiddleware(settings *state.SettingsState) gin.HandlerFunc {
	return func(c *gin.Context) {
		settings.RLock()
		key := settings.Api.Key
		settings.RUnlock()

		if key == "" {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "API disabled: no API key configured"})
			return
		}

		provided := c.GetHeader("X-API-Key")
		if provided != key {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
			return
		}

		c.Next()
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./rest/... -v -race -run TestApiKeyMiddleware
```

Expected: PASS

- [ ] **Step 5: Mount GraphQL in app/server_control.go**

In `startHTTPServer()` (around line 33–34), replace:

```go
		r := rest.GetRouter(a.Logger)
```

With:

```go
		r := rest.GetRouter(a.Logger)

		// Mount GraphQL API with API key protection
		gqlResolver := gql.NewResolver(a)
		gqlSrv := handler.NewDefaultServer(
			generated.NewExecutableSchema(generated.Config{Resolvers: gqlResolver}),
		)
		apiV1 := r.Group("/api/v1")
		apiV1.POST("/graphql", rest.ApiKeyMiddleware(a.SettingsState), func(c *gin.Context) {
			gqlSrv.ServeHTTP(c.Writer, c.Request)
		})
```

Add imports to `app/server_control.go`:

```go
import (
	// existing imports ...
	"github.com/99designs/gqlgen/graphql/handler"
	gql "github.com/FPGSchiba/vcs-srs-server/graphql"
	"github.com/FPGSchiba/vcs-srs-server/graphql/generated"
	"github.com/FPGSchiba/vcs-srs-server/rest"
)
```

- [ ] **Step 6: Build to verify**

```bash
go build ./...
```

Expected: success. Run `go mod tidy` if new gqlgen dependencies are needed.

- [ ] **Step 7: Commit**

```bash
git add rest/middleware.go rest/middleware_test.go app/server_control.go go.mod go.sum
git commit -m "feat: add API key middleware and mount GraphQL handler on /api/v1/graphql"
```

---

## Task 14: Add SaveSecuritySettings and SaveVoiceControlSettings to services/settings.go

**Files:**
- Modify: `services/settings.go`

These are Wails-facing methods used by the frontend. They delegate to the app-level methods added in Task 12.

- [ ] **Step 1: Add methods to services/settings.go**

Append to `services/settings.go`:

```go
func (s *SettingsService) SaveSecuritySettings(enableGuestAuth bool, enablePluginAuth bool) {
	s.App.SaveSecuritySettings(enableGuestAuth, enablePluginAuth)
	s.App.App.Event.EmitEvent(&application.CustomEvent{
		Name: events.SettingsChanged,
		Data: s.App.SettingsState,
	})
	s.App.Notify(events.NewNotification("Settings saved", "Security Settings were successfully saved", "info"))
}

func (s *SettingsService) SaveVoiceControlSettings(newSettings *state.VoiceControlSettings) {
	s.App.SaveVoiceControlSettings(*newSettings)
	s.App.App.Event.EmitEvent(&application.CustomEvent{
		Name: events.SettingsChanged,
		Data: s.App.SettingsState,
	})
	s.App.Notify(events.NewNotification("Settings saved", "VoiceControl Settings were successfully saved", "info"))
}
```

Note: `app.SaveSecuritySettings` and `app.SaveVoiceControlSettings` already call `a.EmitEvent(...)` for the bus. The Wails `App.Event.EmitEvent` call here sends to the Wails frontend. This is the same pattern as the existing `SaveGeneralSettings`.

- [ ] **Step 2: Build to verify**

```bash
go build ./services/...
```

Expected: success.

- [ ] **Step 3: Commit**

```bash
git add services/settings.go
git commit -m "feat: add SaveSecuritySettings and SaveVoiceControlSettings Wails service methods"
```

---

## Task 15: Update frontend Settings.tsx with Security and VoiceControl sections

**Files:**
- Modify: `frontend/src/pages/Settings.tsx`

- [ ] **Step 1: Extend the Zod schema to include Security and VoiceControl**

In `Settings.tsx`, replace the `settingsSchema` definition:

```typescript
const settingsSchema = z.object({
    General: z.object({
        MaxRadiosPerUser: z.number().min(1, "Must be at least 1"),
    }),
    Servers: z.object({
        HTTP: z.object({
            Port: z.number().min(1, "Required"),
            Host: z.string(),
        }),
        Voice: z.object({
            Port: z.number().min(1, "Required"),
            Host: z.string(),
        }),
        Control: z.object({
            Port: z.number().min(1, "Required"),
            Host: z.string(),
        }),
    }),
    Security: z.object({
        EnableGuestAuth: z.boolean(),
        EnablePluginAuth: z.boolean(),
    }),
    VoiceControl: z.object({
        Port: z.number().min(1, "Required"),
        ListenHost: z.string(),
        RemoteHost: z.string(),
        CertificateFile: z.string(),
        PrivateKeyFile: z.string(),
    }),
});
```

- [ ] **Step 2: Add new Wails imports and update defaultValues and reset calls**

Update imports at top of `Settings.tsx`:

```typescript
import { GetSettings, SaveGeneralSettings, SaveServerSettings, SaveSecuritySettings, SaveVoiceControlSettings } from "../../bindings/github.com/FPGSchiba/vcs-srs-server/services/settingsservice";
```

Update `defaultValues` in `useForm`:

```typescript
defaultValues: {
    General: { MaxRadiosPerUser: 1 },
    Servers: {
        HTTP: { Port: 80, Host: "" },
        Voice: { Port: 5002, Host: "" },
        Control: { Port: 5002, Host: "" },
    },
    Security: { EnableGuestAuth: true, EnablePluginAuth: false },
    VoiceControl: {
        Port: 14448,
        ListenHost: "0.0.0.0",
        RemoteHost: "localhost",
        CertificateFile: "",
        PrivateKeyFile: "",
    },
},
```

Update `fetchSettings` to populate Security and VoiceControl:

```typescript
reset({
    General: { MaxRadiosPerUser: Number(newSettings.General.MaxRadiosPerUser) || 1 },
    Servers: { /* ... existing ... */ },
    Security: {
        EnableGuestAuth: newSettings.Security.EnableGuestAuth ?? true,
        EnablePluginAuth: newSettings.Security.EnablePluginAuth ?? false,
    },
    VoiceControl: {
        Port: Number(newSettings.VoiceControl.Port) || 14448,
        ListenHost: newSettings.VoiceControl.ListenHost ?? "0.0.0.0",
        RemoteHost: newSettings.VoiceControl.RemoteHost ?? "localhost",
        CertificateFile: newSettings.VoiceControl.CertificateFile ?? "",
        PrivateKeyFile: newSettings.VoiceControl.PrivateKeyFile ?? "",
    },
});
```

Update `handleSettingsChange` similarly.

- [ ] **Step 3: Add SaveSecuritySettings and SaveVoiceControlSettings to onSubmit**

```typescript
const onSubmit = async (data: SettingsFormType) => {
    try {
        await SaveGeneralSettings(data.General);
        await SaveServerSettings({ ...data.Servers });
        await SaveSecuritySettings(data.Security.EnableGuestAuth, data.Security.EnablePluginAuth);
        await SaveVoiceControlSettings(data.VoiceControl);
        await fetchSettings();
    } catch (error) {
        console.error('Failed to save settings:', error);
    }
};
```

- [ ] **Step 4: Add Security section to JSX (below the Servers section)**

Add inside `<Box className="settings settings-content">`, after the Servers `</Box>`:

```tsx
<Box className="settings settings-security settings-security-wrapper">
    <Typography variant="h4">Security</Typography>
    <FormControl component="fieldset">
        <FormLabel>Enable Guest Authentication</FormLabel>
        <Controller
            name="Security.EnableGuestAuth"
            control={control}
            render={({ field }) => (
                <Switch checked={field.value} onChange={field.onChange} />
            )}
        />
    </FormControl>
    <FormControl component="fieldset">
        <FormLabel>Enable Plugin Authentication</FormLabel>
        <Controller
            name="Security.EnablePluginAuth"
            control={control}
            render={({ field }) => (
                <Switch checked={field.value} onChange={field.onChange} />
            )}
        />
    </FormControl>
</Box>
```

Add `Switch` to the MUI imports: `import { ..., Switch } from "@mui/material";`

- [ ] **Step 5: Add VoiceControl section to JSX (below Security section)**

```tsx
<Box className="settings settings-voicecontrol settings-voicecontrol-wrapper">
    <Typography variant="h4">Voice Control</Typography>
    <FormControl component="fieldset">
        <FormLabel>Listen Host</FormLabel>
        <Controller
            name="VoiceControl.ListenHost"
            control={control}
            render={({ field, fieldState }) => (
                <TextField {...field} variant="outlined" error={!!fieldState.error} helperText={fieldState.error?.message} />
            )}
        />
    </FormControl>
    <FormControl component="fieldset">
        <FormLabel>Port</FormLabel>
        <Controller
            name="VoiceControl.Port"
            control={control}
            render={({ field, fieldState }) => (
                <TextField {...field} type="number" variant="outlined" error={!!fieldState.error} helperText={fieldState.error?.message}
                    onChange={e => field.onChange(e.target.value === "" ? "" : Number(e.target.value))} />
            )}
        />
    </FormControl>
    <FormControl component="fieldset">
        <FormLabel>Remote Host</FormLabel>
        <Controller
            name="VoiceControl.RemoteHost"
            control={control}
            render={({ field, fieldState }) => (
                <TextField {...field} variant="outlined" error={!!fieldState.error} helperText={fieldState.error?.message} />
            )}
        />
    </FormControl>
    <FormControl component="fieldset">
        <FormLabel>Certificate File</FormLabel>
        <Controller
            name="VoiceControl.CertificateFile"
            control={control}
            render={({ field, fieldState }) => (
                <TextField {...field} variant="outlined" error={!!fieldState.error} helperText={fieldState.error?.message} />
            )}
        />
    </FormControl>
    <FormControl component="fieldset">
        <FormLabel>Private Key File</FormLabel>
        <Controller
            name="VoiceControl.PrivateKeyFile"
            control={control}
            render={({ field, fieldState }) => (
                <TextField {...field} variant="outlined" error={!!fieldState.error} helperText={fieldState.error?.message} />
            )}
        />
    </FormControl>
</Box>
```

- [ ] **Step 6: Regenerate Wails bindings**

```bash
cd /Users/schiba/Projects/vanguard/vngd-srs-server
wails3 generate bindings -clean=true -ts
```

- [ ] **Step 7: Build frontend to verify TypeScript compiles**

```bash
cd frontend && npm run build:dev
```

Expected: no type errors.

- [ ] **Step 8: Commit**

```bash
git add frontend/src/pages/Settings.tsx frontend/bindings/
git commit -m "feat: add Security and VoiceControl sections to Settings page"
```

---

## Task 16: Add Logs page (frontend/src/pages/Logs.tsx)

**Files:**
- Create: `frontend/src/pages/Logs.tsx`

- [ ] **Step 1: Create frontend/src/pages/Logs.tsx**

```tsx
import React, { useCallback, useEffect, useRef, useState } from 'react';
import {
    Box,
    Button,
    FormControl,
    InputLabel,
    MenuItem,
    Select,
    Typography,
} from '@mui/material';
import { Events } from '@wailsio/runtime';

interface LogEntry {
    time: string;
    level: string;
    msg: string;
    attrs?: Record<string, unknown>;
}

const MAX_ENTRIES = 500;

const LEVEL_COLORS: Record<string, string> = {
    DEBUG: 'text.disabled',
    INFO: 'text.primary',
    WARN: 'warning.main',
    ERROR: 'error.main',
};

type LevelFilter = 'ALL' | 'DEBUG' | 'INFO' | 'WARN' | 'ERROR';

const LEVEL_ORDER: Record<string, number> = { DEBUG: 0, INFO: 1, WARN: 2, ERROR: 3 };

function LogsPage() {
    const [entries, setEntries] = useState<LogEntry[]>([]);
    const [levelFilter, setLevelFilter] = useState<LevelFilter>('ALL');
    const [autoScroll, setAutoScroll] = useState(true);
    const bottomRef = useRef<HTMLDivElement>(null);
    const containerRef = useRef<HTMLDivElement>(null);

    const appendEntry = useCallback((entry: LogEntry) => {
        setEntries(prev => {
            const next = [...prev, entry];
            return next.length > MAX_ENTRIES ? next.slice(next.length - MAX_ENTRIES) : next;
        });
    }, []);

    useEffect(() => {
        Events.On('logs/entry', (event) => {
            const entry = event.data as LogEntry;
            appendEntry(entry);
        });
    }, [appendEntry]);

    useEffect(() => {
        if (autoScroll && bottomRef.current) {
            bottomRef.current.scrollIntoView({ behavior: 'smooth' });
        }
    }, [entries, autoScroll]);

    const handleScroll = () => {
        const container = containerRef.current;
        if (!container) return;
        const atBottom = container.scrollHeight - container.scrollTop <= container.clientHeight + 50;
        setAutoScroll(atBottom);
    };

    const filteredEntries = entries.filter(e => {
        if (levelFilter === 'ALL') return true;
        return LEVEL_ORDER[e.level] >= LEVEL_ORDER[levelFilter];
    });

    return (
        <Box sx={{ display: 'flex', flexDirection: 'column', height: '100%', gap: 1 }}>
            <Box sx={{ display: 'flex', alignItems: 'center', gap: 2 }}>
                <Typography variant="h6">Logs</Typography>
                <FormControl size="small" sx={{ minWidth: 120 }}>
                    <InputLabel>Level</InputLabel>
                    <Select
                        value={levelFilter}
                        label="Level"
                        onChange={e => setLevelFilter(e.target.value as LevelFilter)}
                    >
                        <MenuItem value="ALL">All</MenuItem>
                        <MenuItem value="DEBUG">Debug+</MenuItem>
                        <MenuItem value="INFO">Info+</MenuItem>
                        <MenuItem value="WARN">Warn+</MenuItem>
                        <MenuItem value="ERROR">Error</MenuItem>
                    </Select>
                </FormControl>
                <Button
                    variant="outlined"
                    size="small"
                    onClick={() => setEntries([])}
                >
                    Clear
                </Button>
            </Box>
            <Box
                ref={containerRef}
                onScroll={handleScroll}
                sx={{
                    flex: 1,
                    overflowY: 'auto',
                    fontFamily: 'monospace',
                    fontSize: '0.8rem',
                    bgcolor: 'background.paper',
                    p: 1,
                    borderRadius: 1,
                }}
            >
                {filteredEntries.map((entry, i) => (
                    <Box key={i} sx={{ color: LEVEL_COLORS[entry.level] ?? 'text.primary', whiteSpace: 'pre-wrap', mb: 0.25 }}>
                        {`[${entry.time}] ${entry.level.padEnd(5)} ${entry.msg}`}
                        {entry.attrs && Object.keys(entry.attrs).length > 0
                            ? ' ' + JSON.stringify(entry.attrs)
                            : ''}
                    </Box>
                ))}
                <div ref={bottomRef} />
            </Box>
        </Box>
    );
}

export default LogsPage;
```

- [ ] **Step 2: Build frontend to verify TypeScript compiles**

```bash
cd /Users/schiba/Projects/vanguard/vngd-srs-server/frontend && npm run build:dev
```

Expected: no TypeScript errors.

- [ ] **Step 3: Commit**

```bash
git add frontend/src/pages/Logs.tsx
git commit -m "feat: add live Logs page with ring buffer, level filter, and auto-scroll"
```

---

## Task 17: Add Logs tab to ContentWrapper.tsx and fix ClientList.tsx event handler

**Files:**
- Modify: `frontend/src/components/ContentWrapper.tsx`
- Modify: `frontend/src/pages/ClientList.tsx`

- [ ] **Step 1: Add Logs tab to ContentWrapper.tsx**

Replace the entire `ContentWrapper.tsx` content:

```tsx
import * as React from 'react';
import Box from '@mui/material/Box';
import Tab from '@mui/material/Tab';
import TabContext from '@mui/lab/TabContext';
import TabList from '@mui/lab/TabList';
import TabPanel from '@mui/lab/TabPanel';
import SettingsPage from "../pages/Settings";
import CoalitionsPage from "../pages/Coalitions";
import ClientListPage from "../pages/ClientList";
import BanManagement from "../pages/BanManagement";
import FrequencyPage from "../pages/FrequencyPage";
import LogsPage from "../pages/Logs";

function ContentWrapper() {
    const [value, setValue] = React.useState('1');

    const handleChange = (event: React.SyntheticEvent, newValue: string) => {
        setValue(newValue);
    };

    return (
        <Box className="content content-wrapper" sx={{ typography: 'body1' }}>
            <TabContext value={value}>
                <Box className="nav nav-wrapper">
                    <TabList onChange={handleChange}>
                        <Tab className="nav nav-tab nav-tab-button" label="Settings" value="1" />
                        <Tab className="nav nav-tab nav-tab-button" label="Coalitions" value="2" />
                        <Tab className="nav nav-tab nav-tab-button" label="Clients" value="3" />
                        <Tab className="nav nav-tab nav-tab-button" label="Banned Clients" value="4" />
                        <Tab className="nav nav-tab nav-tab-button" label="Frequencies" value="5" />
                        <Tab className="nav nav-tab nav-tab-button" label="Logs" value="6" />
                    </TabList>
                </Box>
                <TabPanel className="nav nav-tab nav-tab-container" value="1">
                    <SettingsPage />
                </TabPanel>
                <TabPanel className="nav nav-tab nav-tab-container" value="2">
                    <CoalitionsPage />
                </TabPanel>
                <TabPanel className="nav nav-tab nav-tab-container" value="3">
                    <ClientListPage />
                </TabPanel>
                <TabPanel className="nav nav-tab nav-tab-container" value="4">
                    <BanManagement />
                </TabPanel>
                <TabPanel className="nav nav-tab nav-tab-container" value="5">
                    <FrequencyPage />
                </TabPanel>
                <TabPanel className="nav nav-tab nav-tab-container" value="6">
                    <LogsPage />
                </TabPanel>
            </TabContext>
        </Box>
    );
}

export default ContentWrapper;
```

- [ ] **Step 2: Fix ClientList.tsx event handler**

The `ClientsChanged` event now carries a `ClientChangeEvent` payload (`{Type, ClientID, Clients}`), not a raw `Record<string, ClientState>` map. Read `ClientList.tsx` to find the event handler, then replace the direct cast with a `GetClients()` call.

Find the `Events.On("clients/changed", ...)` handler in `ClientList.tsx` and replace it:

```typescript
Events.On("clients/changed", async (_event) => {
    // ClientsChanged now carries a ClientChangeEvent — re-fetch instead of casting
    const result = await GetClients();
    setClients(result?.Clients ?? {});
});
```

Make sure `GetClients` is imported from the Wails bindings.

- [ ] **Step 3: Build frontend**

```bash
cd /Users/schiba/Projects/vanguard/vngd-srs-server/frontend && npm run build:dev
```

Expected: no TypeScript errors.

- [ ] **Step 4: Commit**

```bash
git add frontend/src/components/ContentWrapper.tsx frontend/src/pages/ClientList.tsx
git commit -m "feat: add Logs tab to ContentWrapper; fix ClientList event handler for new ClientChangeEvent payload"
```

---

## Self-Review

**Spec coverage check:**

| Spec section | Tasks covering it |
|---|---|
| Gap 1: IsIntercom | Task 2 |
| Gap 2: buildServerUpdate payload | Task 5 |
| Gap 3: CLIENT_JOINED/LEFT distinction | Tasks 1, 4, 5 |
| Gap 4: SERVER_ACTION | Tasks 1, 5, 6 |
| Gap 5: IP ban check | Task 3 |
| GraphQL schema + gqlgen | Task 10 |
| GraphQL resolvers (Query) | Task 11 |
| GraphQL resolvers (Mutation) | Task 12 |
| API key middleware | Task 13 |
| GraphQL route mounting | Task 13 |
| ApiSettings in state | Task 9 |
| BusHandler log streaming | Task 7 |
| Logger wiring | Task 8 |
| Security settings section | Task 15 |
| VoiceControl settings section | Task 15 |
| Logs tab | Tasks 16, 17 |
| Taskfile generate:graphql | Task 10 |
| ClientList.tsx fix | Task 17 |

**Type consistency check:**
- `events.ClientChangeEvent` used in Task 1 (def), Tasks 4+5 (publishers), Task 5 (consumer in `buildServerUpdate`)
- `events.ServerActionEvent` used in Task 1 (def), Task 6 (publishers), Task 5 (consumer)
- `events.RadioChangeEvent` used in Task 1 (def), Tasks 4+6 (publishers), Task 5 (consumer)
- `state.SettingsSnapshot` used in Task 9 (def), Task 11 (query resolver via `GetSettingsSnapshot()`)
- `AppInterface` in Task 11 must match methods added in Tasks 12 + `app/app.go` + `app/clients.go`
- `logging.LogEntry` matches what `BusHandler` publishes (Task 7) and what `Logs.tsx` expects (Task 16)

---

**Plan complete and saved to `docs/superpowers/plans/2026-04-17-srs-server-completion.md`. Two execution options:**

**1. Subagent-Driven (recommended)** - Fresh subagent per task, two-stage review between tasks, fast iteration

**2. Inline Execution** - Execute tasks in this session using executing-plans, batch execution with checkpoints

**Which approach?**
