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

func TestApplySnapshotCarriesVoiceSecret(t *testing.T) {
	ss := &state.ServerState{
		Clients:      make(map[uuid.UUID]*state.ClientState),
		RadioClients: make(map[uuid.UUID]*state.RadioState),
	}
	c := newTestClient(ss)
	id := uuid.New()

	c.applySnapshot(&pb.ClientStateSnapshot{
		Clients: map[string]*pb.VoiceClientInfo{
			id.String(): {Name: "Pilot", Coalition: "Blue", VoiceSecret: "snapshot-secret"},
		},
	})

	ss.RLock()
	defer ss.RUnlock()
	if got := ss.Clients[id].VoiceSecret; got != "snapshot-secret" {
		t.Fatalf("expected the snapshot secret, got %q", got)
	}
}

func TestApplyDeltaJoinedCarriesVoiceSecret(t *testing.T) {
	ss := &state.ServerState{
		Clients:      make(map[uuid.UUID]*state.ClientState),
		RadioClients: make(map[uuid.UUID]*state.RadioState),
	}
	c := newTestClient(ss)
	id := uuid.New()

	c.applyDelta(&pb.ClientDelta{
		Type:       pb.ClientDelta_JOINED,
		ClientId:   id.String(),
		ClientInfo: &pb.VoiceClientInfo{Name: "Pilot", Coalition: "Blue", VoiceSecret: "joined-secret"},
	})

	ss.RLock()
	defer ss.RUnlock()
	if got := ss.Clients[id].VoiceSecret; got != "joined-secret" {
		t.Fatalf("expected the delta secret, got %q", got)
	}
}

// An INFO_UPDATED delta replaces the whole ClientState. If the secret is not
// carried on it, a client changing unit silently loses the ability to send a
// valid HELLO.
func TestApplyDeltaInfoUpdatedPreservesVoiceSecret(t *testing.T) {
	ss := &state.ServerState{
		Clients:      make(map[uuid.UUID]*state.ClientState),
		RadioClients: make(map[uuid.UUID]*state.RadioState),
	}
	c := newTestClient(ss)
	id := uuid.New()

	c.applyDelta(&pb.ClientDelta{
		Type:       pb.ClientDelta_JOINED,
		ClientId:   id.String(),
		ClientInfo: &pb.VoiceClientInfo{Name: "Pilot", Coalition: "Blue", UnitId: "A1", VoiceSecret: "stable-secret"},
	})
	c.applyDelta(&pb.ClientDelta{
		Type:       pb.ClientDelta_INFO_UPDATED,
		ClientId:   id.String(),
		ClientInfo: &pb.VoiceClientInfo{Name: "Pilot", Coalition: "Blue", UnitId: "B2", VoiceSecret: "stable-secret"},
	})

	ss.RLock()
	defer ss.RUnlock()
	client := ss.Clients[id]
	if client.UnitId != "B2" {
		t.Fatalf("expected the unit to update to B2, got %q", client.UnitId)
	}
	if client.VoiceSecret != "stable-secret" {
		t.Fatalf("the secret must survive an INFO_UPDATED delta, got %q", client.VoiceSecret)
	}
}
