package voiceontrol

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/FPGSchiba/vcs-srs-server/events"
	"github.com/FPGSchiba/vcs-srs-server/state"
	pb "github.com/FPGSchiba/vcs-srs-server/voicecontrolpb"
	"github.com/google/uuid"
)

func newTestVoiceControlServer(coalitions []string) *VoiceControlServer {
	settingsState := &state.SettingsState{}
	settingsState.Coalitions = make([]state.Coalition, len(coalitions))
	for i, name := range coalitions {
		settingsState.Coalitions[i] = state.Coalition{Name: name}
	}
	return NewVoiceControlServer(
		&state.ServerState{
			Clients:      make(map[uuid.UUID]*state.ClientState),
			RadioClients: make(map[uuid.UUID]*state.RadioState),
		},
		settingsState,
		events.NewEventBus(),
		slog.New(slog.NewTextHandler(os.Stderr, nil)),
	)
}

func TestRegisterVoiceServer_SingleNode_GetsAllCoalitions(t *testing.T) {
	s := newTestVoiceControlServer([]string{"Blue", "Red"})
	resp, err := s.RegisterVoiceServer(context.Background(), &pb.RegisterVoiceServerRequest{
		ServerId:      "node-1",
		ServerAddress: "10.0.0.1",
		UdpPort:       5002,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got: %s", resp.Message)
	}
	if len(resp.AssignedCoalitions) == 0 {
		t.Fatal("expected at least one coalition assigned")
	}
}

func TestRegisterVoiceServer_TwoNodes_DifferentCoalitions(t *testing.T) {
	s := newTestVoiceControlServer([]string{"Blue", "Red"})

	resp1, _ := s.RegisterVoiceServer(context.Background(), &pb.RegisterVoiceServerRequest{
		ServerId: "node-1", ServerAddress: "10.0.0.1", UdpPort: 5002,
	})
	resp2, _ := s.RegisterVoiceServer(context.Background(), &pb.RegisterVoiceServerRequest{
		ServerId: "node-2", ServerAddress: "10.0.0.2", UdpPort: 5002,
	})

	if resp1.AssignedCoalitions[0] == resp2.AssignedCoalitions[0] {
		t.Fatal("two nodes should not receive the same coalition")
	}
}

func TestRegisterVoiceServer_GlobalNode_SetsGlobalAddr(t *testing.T) {
	s := newTestVoiceControlServer([]string{"Blue"})
	resp, err := s.RegisterVoiceServer(context.Background(), &pb.RegisterVoiceServerRequest{
		ServerId: "global-1", ServerAddress: "10.0.0.10", UdpPort: 5002, IsGlobal: true,
	})
	if err != nil || !resp.Success {
		t.Fatalf("registration failed: %v", err)
	}
	if len(resp.AssignedCoalitions) != 0 {
		t.Fatal("global node should not receive coalition assignments")
	}
	s.mu.RLock()
	addr := s.globalNodeAddr
	s.mu.RUnlock()
	if addr != "10.0.0.10:5002" {
		t.Fatalf("expected global node addr 10.0.0.10:5002, got %s", addr)
	}
}

func TestRegisterVoiceServer_GlobalAddrReturnedToSubsequentNodes(t *testing.T) {
	s := newTestVoiceControlServer([]string{"Blue", "Red"})
	s.RegisterVoiceServer(context.Background(), &pb.RegisterVoiceServerRequest{
		ServerId: "global-1", ServerAddress: "10.0.0.10", UdpPort: 5002, IsGlobal: true,
	})
	resp, _ := s.RegisterVoiceServer(context.Background(), &pb.RegisterVoiceServerRequest{
		ServerId: "node-1", ServerAddress: "10.0.0.1", UdpPort: 5002,
	})
	if resp.GlobalVoiceAddress != "10.0.0.10:5002" {
		t.Fatalf("expected global voice address to be returned, got %q", resp.GlobalVoiceAddress)
	}
}

func TestGetVoiceAddressForCoalition(t *testing.T) {
	s := newTestVoiceControlServer([]string{"Blue", "Red"})
	s.RegisterVoiceServer(context.Background(), &pb.RegisterVoiceServerRequest{
		ServerId: "node-eu", ServerAddress: "10.0.1.1", UdpPort: 5002,
		PreferredCoalitions: []string{"Blue"},
	})
	s.RegisterVoiceServer(context.Background(), &pb.RegisterVoiceServerRequest{
		ServerId: "global-1", ServerAddress: "10.0.0.10", UdpPort: 5002, IsGlobal: true,
	})

	coalAddr, globalAddr := s.GetVoiceAddressForCoalition("Blue")
	if coalAddr != "10.0.1.1:5002" {
		t.Fatalf("expected coalition addr 10.0.1.1:5002, got %q", coalAddr)
	}
	if globalAddr != "10.0.0.10:5002" {
		t.Fatalf("expected global addr 10.0.0.10:5002, got %q", globalAddr)
	}
}
