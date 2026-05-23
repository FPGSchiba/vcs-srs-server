package voiceontrol

import (
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/state"
	pb "github.com/FPGSchiba/vcs-srs-server/voicecontrolpb"
	"github.com/google/uuid"
)

func newTestClient(serverState *state.ServerState) *VoiceControlClient {
	return &VoiceControlClient{
		serverId:      "test-node",
		logger:        slog.New(slog.NewTextHandler(os.Stderr, nil)),
		serverState:   serverState,
		stopc:         make(chan struct{}),
		settingsState: &state.SettingsState{},
	}
}

func TestApplySnapshot_PopulatesServerState(t *testing.T) {
	ss := &state.ServerState{
		Clients:      make(map[uuid.UUID]*state.ClientState),
		RadioClients: make(map[uuid.UUID]*state.RadioState),
	}
	c := newTestClient(ss)
	id := uuid.New()

	c.applySnapshot(&pb.ClientStateSnapshot{
		Clients: map[string]*pb.VoiceClientInfo{
			id.String(): {Name: "Pilot", Coalition: "Blue", UnitId: "A1", Role: 1},
		},
		Radios: map[string]*pb.VoiceRadioInfo{
			id.String(): {Radios: []*pb.VoiceRadio{{Id: 1, Frequency: 243.0, Enabled: true}}, Muted: false},
		},
	})

	ss.RLock()
	defer ss.RUnlock()
	client, exists := ss.Clients[id]
	if !exists {
		t.Fatal("expected client in server state after snapshot")
	}
	if client.Name != "Pilot" || client.Coalition != "Blue" {
		t.Fatalf("unexpected client data: %+v", client)
	}
	radio, rExists := ss.RadioClients[id]
	if !rExists || len(radio.Radios) != 1 {
		t.Fatal("expected radio state after snapshot")
	}
}

func TestApplyDelta_Joined(t *testing.T) {
	ss := &state.ServerState{
		Clients:      make(map[uuid.UUID]*state.ClientState),
		RadioClients: make(map[uuid.UUID]*state.RadioState),
	}
	c := newTestClient(ss)
	id := uuid.New()

	c.applyDelta(&pb.ClientDelta{
		Type:       pb.ClientDelta_JOINED,
		ClientId:   id.String(),
		ClientInfo: &pb.VoiceClientInfo{Name: "Pilot", Coalition: "Blue"},
	})

	ss.RLock()
	defer ss.RUnlock()
	if _, exists := ss.Clients[id]; !exists {
		t.Fatal("expected client to be added on JOINED delta")
	}
}

func TestApplyDelta_Left(t *testing.T) {
	id := uuid.New()
	ss := &state.ServerState{
		Clients:      map[uuid.UUID]*state.ClientState{id: {Name: "Pilot"}},
		RadioClients: map[uuid.UUID]*state.RadioState{id: {Radios: []state.Radio{}}},
	}
	c := newTestClient(ss)

	c.applyDelta(&pb.ClientDelta{Type: pb.ClientDelta_LEFT, ClientId: id.String()})

	ss.RLock()
	defer ss.RUnlock()
	if _, exists := ss.Clients[id]; exists {
		t.Fatal("expected client to be removed on LEFT delta")
	}
}

func TestApplyDelta_Muted(t *testing.T) {
	id := uuid.New()
	ss := &state.ServerState{
		Clients:      map[uuid.UUID]*state.ClientState{id: {Name: "Pilot", Coalition: "Blue"}},
		RadioClients: map[uuid.UUID]*state.RadioState{id: {Radios: []state.Radio{}, Muted: false}},
	}
	c := newTestClient(ss)

	c.applyDelta(&pb.ClientDelta{Type: pb.ClientDelta_MUTED, ClientId: id.String()})

	ss.RLock()
	defer ss.RUnlock()
	if !ss.RadioClients[id].Muted {
		t.Fatal("expected client to be muted after MUTED delta")
	}
}

func TestApplyDelta_RadioUpdated_PreservesMute(t *testing.T) {
	id := uuid.New()
	ss := &state.ServerState{
		Clients:      map[uuid.UUID]*state.ClientState{id: {Name: "Pilot", Coalition: "Blue"}},
		RadioClients: map[uuid.UUID]*state.RadioState{id: {Muted: true, Radios: []state.Radio{}}},
	}
	c := newTestClient(ss)

	c.applyDelta(&pb.ClientDelta{
		Type:     pb.ClientDelta_RADIO_UPDATED,
		ClientId: id.String(),
		RadioInfo: &pb.VoiceRadioInfo{
			Radios: []*pb.VoiceRadio{{Id: 1, Frequency: 243.0, Enabled: true}},
			Muted:  false,
		},
	})

	ss.RLock()
	defer ss.RUnlock()
	if !ss.RadioClients[id].Muted {
		t.Fatal("mute status should be preserved across RADIO_UPDATED delta")
	}
}

func TestApplySnapshot_Race(t *testing.T) {
	ss := &state.ServerState{
		Clients:      make(map[uuid.UUID]*state.ClientState),
		RadioClients: make(map[uuid.UUID]*state.RadioState),
	}
	c := newTestClient(ss)
	id := uuid.New()
	snap := &pb.ClientStateSnapshot{
		Clients: map[string]*pb.VoiceClientInfo{id.String(): {Name: "X", Coalition: "Blue"}},
		Radios:  map[string]*pb.VoiceRadioInfo{},
	}
	delta := &pb.ClientDelta{Type: pb.ClientDelta_LEFT, ClientId: id.String()}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			c.applyDelta(delta)
		}
	}()
	for i := 0; i < 100; i++ {
		c.applySnapshot(snap)
	}
	<-done
	_ = time.Now()
}
