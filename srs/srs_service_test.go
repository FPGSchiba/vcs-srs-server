package srs

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/events"
	pb "github.com/FPGSchiba/vcs-srs-server/srspb"
	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/FPGSchiba/vcs-srs-server/utils"
	"github.com/google/uuid"
)

// newTestServer builds a SimpleRadioServer with its own event bus, which
// is stopped when the test ends.
func newTestServer(t *testing.T) *SimpleRadioServer {
	t.Helper()
	bus := events.NewEventBus()
	t.Cleanup(bus.Stop)
	return &SimpleRadioServer{
		serverState: &state.ServerState{
			Clients:      make(map[uuid.UUID]*state.ClientState),
			RadioClients: make(map[uuid.UUID]*state.RadioState),
		},
		settingsState: &state.SettingsState{},
		logger:        slog.New(slog.NewTextHandler(os.Stderr, nil)),
		eventBus:      bus,
		now:           time.Now,
		staleAfter:    10 * time.Minute,
		streams:       make(map[uuid.UUID]*subscription),
		detachedAt:    make(map[uuid.UUID]time.Time),
		stopChan:      make(chan struct{}),
	}
}

// withSRSClock swaps the server's clock for a fake one and returns it.
func withSRSClock(s *SimpleRadioServer) *fakeClock {
	c := &fakeClock{t: time.Now()}
	s.mu.Lock()
	s.now = c.Now
	s.mu.Unlock()
	return c
}

func clientCtx(id uuid.UUID) context.Context {
	return context.WithValue(context.Background(), utils.ClientIDKey, id.String())
}

// SRV-01: every unary RPC a live client makes refreshes its LastUpdate, so
// a client that only pings is never mistaken for a stale one.
func TestRPCsRefreshLastUpdate(t *testing.T) {
	calls := map[string]func(s *SimpleRadioServer, ctx context.Context){
		"Ping": func(s *SimpleRadioServer, ctx context.Context) {
			_, _ = s.Ping(ctx, &pb.PingRequest{})
		},
		"SyncClient": func(s *SimpleRadioServer, ctx context.Context) {
			_, _ = s.SyncClient(ctx, &pb.Empty{})
		},
		"UpdateRadioInfo": func(s *SimpleRadioServer, ctx context.Context) {
			_, _ = s.UpdateRadioInfo(ctx, &pb.RadioInfo{})
		},
		"UpdateClientInfo": func(s *SimpleRadioServer, ctx context.Context) {
			_, _ = s.UpdateClientInfo(ctx, &pb.ClientInfo{Name: "Alice", UnitId: "AB"})
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			s := newTestServer(t)
			clock := withSRSClock(s)
			id := uuid.New()
			s.serverState.AddClient(id, &state.ClientState{Name: "Alice"})
			s.serverState.Lock()
			s.serverState.Clients[id].LastUpdate = clock.Now().Add(-time.Hour)
			s.serverState.Unlock()
			clock.Advance(time.Minute)

			call(s, clientCtx(id))

			s.serverState.RLock()
			got := s.serverState.Clients[id].LastUpdate
			s.serverState.RUnlock()
			if !got.Equal(clock.Now()) {
				t.Fatalf("LastUpdate = %v, want %v", got, clock.Now())
			}
		})
	}
}

func TestBuildServerUpdate_ClientJoined(t *testing.T) {
	s := newTestServer(t)
	id := uuid.New()
	evt := events.Event{
		Name: events.ClientsChanged,
		Data: events.ClientChangeEvent{
			Type:     events.ClientJoined,
			ClientID: id,
			Clients:  map[uuid.UUID]*state.ClientState{id: {Name: "Alice", Coalition: "Blue"}},
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
	if cu.GetClientInfo() == nil || cu.GetClientInfo().Name != "Alice" {
		t.Fatal("expected ClientInfo with Name=Alice")
	}
}

func TestBuildServerUpdate_ClientLeft(t *testing.T) {
	s := newTestServer(t)
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
	cu := update.GetClientUpdate()
	if cu == nil || cu.GetClientGuid() != id.String() {
		t.Fatal("expected ClientUpdate with correct guid")
	}
}

func TestBuildServerUpdate_RadioUpdate(t *testing.T) {
	s := newTestServer(t)
	id := uuid.New()
	evt := events.Event{
		Name: events.RadioClientsChanged,
		Data: events.RadioChangeEvent{
			Type:     events.RadioUpdated,
			ClientID: id,
			Radios:   map[uuid.UUID]*state.RadioState{id: {Muted: true}},
		},
	}
	update := s.buildServerUpdate(evt)
	if update == nil || update.Type != pb.ServerUpdate_CLIENT_RADIO_UPDATE {
		t.Fatalf("expected CLIENT_RADIO_UPDATE, got %v", update)
	}
	cu := update.GetClientUpdate()
	if cu == nil {
		t.Fatal("expected ClientUpdate")
	}
	if cu.GetClientGuid() != id.String() {
		t.Fatalf("expected %s, got %s", id.String(), cu.GetClientGuid())
	}
	if !cu.GetRadioInfo().GetMuted() {
		t.Fatal("expected Muted=true")
	}
}

func TestBuildServerUpdate_ServerAction_Kick(t *testing.T) {
	s := newTestServer(t)
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
	if sa.GetTargetClientGuid() != id.String() {
		t.Fatalf("expected %s, got %s", id.String(), sa.GetTargetClientGuid())
	}
	if sa.GetReason() != "rule violation" {
		t.Fatalf("expected 'rule violation', got %q", sa.GetReason())
	}
}

func TestBuildServerUpdate_UnknownEvent_ReturnsNil(t *testing.T) {
	s := newTestServer(t)
	evt := events.Event{Name: "unknown/event", Data: nil}
	if s.buildServerUpdate(evt) != nil {
		t.Fatal("expected nil for unknown event")
	}
}

func TestBuildServerUpdate_BadPayload_ReturnsNil(t *testing.T) {
	s := newTestServer(t)
	evt := events.Event{Name: events.ClientsChanged, Data: "not a ClientChangeEvent"}
	if s.buildServerUpdate(evt) != nil {
		t.Fatal("expected nil when payload is wrong type")
	}
}

func TestBuildServerUpdate_ClientInfoUpdated(t *testing.T) {
	s := newTestServer(t)
	id := uuid.New()
	evt := events.Event{
		Name: events.ClientsChanged,
		Data: events.ClientChangeEvent{
			Type:     events.ClientInfoUpdated,
			ClientID: id,
			Clients:  map[uuid.UUID]*state.ClientState{id: {Name: "Bob", Coalition: "Red"}},
		},
	}
	update := s.buildServerUpdate(evt)
	if update == nil || update.Type != pb.ServerUpdate_CLIENT_INFO_UPDATE {
		t.Fatalf("expected CLIENT_INFO_UPDATE, got %v", update)
	}
	cu := update.GetClientUpdate()
	if cu == nil || cu.GetClientGuid() != id.String() {
		t.Fatal("expected ClientUpdate with correct guid")
	}
	if cu.GetClientInfo() == nil || cu.GetClientInfo().Name != "Bob" {
		t.Fatal("expected ClientInfo with Name=Bob")
	}
}

func TestBuildServerUpdate_SettingsChanged(t *testing.T) {
	s := newTestServer(t)
	evt := events.Event{Name: events.SettingsChanged, Data: nil}
	update := s.buildServerUpdate(evt)
	if update == nil || update.Type != pb.ServerUpdate_SERVER_SETTINGS_CHANGED {
		t.Fatalf("expected SERVER_SETTINGS_CHANGED, got %v", update)
	}
}

func TestVoiceSecretForKnownClient(t *testing.T) {
	s := newTestServer(t)
	id := uuid.New()
	s.serverState.AddClient(id, &state.ClientState{Name: "Alice", Coalition: "Blue"})

	s.serverState.RLock()
	want := s.serverState.Clients[id].VoiceSecret
	s.serverState.RUnlock()

	if want == "" {
		t.Fatal("AddClient should have generated a secret")
	}
	if got := s.voiceSecretFor(id); got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestVoiceSecretForUnknownClientIsEmpty(t *testing.T) {
	s := newTestServer(t)
	if got := s.voiceSecretFor(uuid.New()); got != "" {
		t.Fatalf("expected an empty secret for an unknown client, got %q", got)
	}
}

// SRV-09: the RadioClientsChanged snapshot never shares memory with live state.
func TestUpdateRadioInfoSnapshotIndependent(t *testing.T) {
	s := newTestServer(t)
	id := uuid.New()
	s.serverState.AddClient(id, &state.ClientState{Name: "Alice"})
	ch := s.eventBus.Subscribe(events.RadioClientsChanged)

	if _, err := s.UpdateRadioInfo(clientCtx(id), &pb.RadioInfo{Radios: []*pb.Radio{{Id: 1, Frequency: 251}}}); err != nil {
		t.Fatalf("UpdateRadioInfo: %v", err)
	}
	var ev events.RadioChangeEvent
	select {
	case e := <-ch:
		ev = e.Data.(events.RadioChangeEvent)
	case <-time.After(time.Second):
		t.Fatal("no RadioClientsChanged event")
	}

	s.serverState.Lock()
	s.serverState.RadioClients[id].Muted = true
	s.serverState.RadioClients[id].Radios[0].Frequency = 1
	s.serverState.Unlock()

	got := ev.Radios[id]
	if got.Muted || got.Radios[0].Frequency != 251 {
		t.Fatalf("event snapshot changed with live state: muted=%v freq=%v", got.Muted, got.Radios[0].Frequency)
	}
}
