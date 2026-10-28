package srs

import (
	"context"
	"testing"

	pb "github.com/FPGSchiba/vcs-srs-server/srspb"
	"github.com/FPGSchiba/vcs-srs-server/state"
	vcpb "github.com/FPGSchiba/vcs-srs-server/voicecontrolpb"
	"github.com/FPGSchiba/vcs-srs-server/voiceontrol"
	"github.com/google/uuid"
)

// newRegistryServer builds a test server wired to a real voice registry,
// mirroring control/server.go (settings coalitions, registry, then the
// registry set on the SRS service).
func newRegistryServer(t *testing.T) (*SimpleRadioServer, *voiceontrol.VoiceControlServer) {
	t.Helper()
	s := newTestServer(t)
	s.settingsState.Coalitions = []state.Coalition{{Name: "Blue"}, {Name: "Red"}}
	vc := voiceontrol.NewVoiceControlServer(s.serverState, s.settingsState, s.eventBus, s.logger)
	s.voiceRegistry = vc
	return s, vc
}

// registerNode registers a voice node on 127.0.0.1:port and requires success.
func registerNode(t *testing.T, vc *voiceontrol.VoiceControlServer, id string, port int32, global bool) {
	t.Helper()
	resp, err := vc.RegisterVoiceServer(context.Background(), &vcpb.RegisterVoiceServerRequest{
		ServerId:      id,
		ServerAddress: "127.0.0.1",
		UdpPort:       port,
		IsGlobal:      global,
	})
	if err != nil {
		t.Fatalf("RegisterVoiceServer(%s): %v", id, err)
	}
	if !resp.Success {
		t.Fatalf("RegisterVoiceServer(%s) not successful: %s", id, resp.Message)
	}
}

// addClient adds a client of the given coalition to the server state.
func addClient(s *SimpleRadioServer, coalition string) uuid.UUID {
	id := uuid.New()
	s.serverState.AddClient(id, &state.ClientState{Name: "c-" + id.String()[:8], Coalition: coalition})
	return id
}

// syncAs calls SyncClient as the given client and returns the sync data.
func syncAs(t *testing.T, s *SimpleRadioServer, id uuid.UUID) *pb.ServerSyncResult {
	t.Helper()
	resp, err := s.SyncClient(clientCtx(id), &pb.Empty{})
	if err != nil {
		t.Fatalf("SyncClient: %v", err)
	}
	if !resp.Success {
		t.Fatalf("SyncClient not successful")
	}
	data := resp.GetData()
	if data == nil {
		t.Fatalf("SyncClient returned no data")
	}
	return data
}

// Case 1: with no voice node registered, addresses are empty but the
// client still receives its 43-char voice secret.
func TestSyncClientWithoutVoiceNodes(t *testing.T) {
	s, _ := newRegistryServer(t)
	blue := addClient(s, "Blue")

	got := syncAs(t, s, blue)
	if got.CoalitionVoiceAddr != "" || got.GlobalVoiceAddr != "" {
		t.Fatalf("addresses = %q / %q, want both empty", got.CoalitionVoiceAddr, got.GlobalVoiceAddr)
	}
	if len(got.VoiceSecret) != 43 {
		t.Fatalf("len(VoiceSecret) = %d, want 43", len(got.VoiceSecret))
	}
}

// Case 2: with one coalition node and one global node, both coalitions
// resolve to the single owning node (rebalance step 1 gives A both).
func TestSyncClientReturnsOwningAndGlobalNode(t *testing.T) {
	s, vc := newRegistryServer(t)
	registerNode(t, vc, "A", 5002, false)
	registerNode(t, vc, "G", 5003, true)

	for _, coalition := range []string{"Blue", "Red"} {
		id := addClient(s, coalition)
		got := syncAs(t, s, id)
		if got.CoalitionVoiceAddr != "127.0.0.1:5002" {
			t.Errorf("%s: CoalitionVoiceAddr = %q, want 127.0.0.1:5002", coalition, got.CoalitionVoiceAddr)
		}
		if got.GlobalVoiceAddr != "127.0.0.1:5003" {
			t.Errorf("%s: GlobalVoiceAddr = %q, want 127.0.0.1:5003", coalition, got.GlobalVoiceAddr)
		}
		if len(got.VoiceSecret) != 43 {
			t.Errorf("%s: len(VoiceSecret) = %d, want 43", coalition, len(got.VoiceSecret))
		}
	}
}
