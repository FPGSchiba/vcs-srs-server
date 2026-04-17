package srs

import (
	"testing"

	"github.com/FPGSchiba/vcs-srs-server/events"
	pb "github.com/FPGSchiba/vcs-srs-server/srspb"
	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/google/uuid"
	"google.golang.org/grpc"
)

func newTestServer() *SimpleRadioServer {
	return &SimpleRadioServer{
		serverState:   &state.ServerState{},
		settingsState: &state.SettingsState{},
		streams:       make(map[uuid.UUID]grpc.ServerStreamingServer[pb.ServerUpdate]),
		stopChan:      make(chan struct{}),
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
	cu := update.GetClientUpdate()
	if cu == nil || cu.GetClientGuid() != id.String() {
		t.Fatal("expected ClientUpdate with correct guid")
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
	if sa.GetTargetClientGuid() != id.String() {
		t.Fatalf("expected %s, got %s", id.String(), sa.GetTargetClientGuid())
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

func TestBuildServerUpdate_BadPayload_ReturnsNil(t *testing.T) {
	s := newTestServer()
	evt := events.Event{Name: events.ClientsChanged, Data: "not a ClientChangeEvent"}
	if s.buildServerUpdate(evt) != nil {
		t.Fatal("expected nil when payload is wrong type")
	}
}
