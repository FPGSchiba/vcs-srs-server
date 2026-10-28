package voiceontrol

import (
	"log/slog"
	"os"
	"sync"
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

// --- SV1: kick_client and LEFT drop the voice binding ---

type kickRecorder struct {
	mu  sync.Mutex
	ids []uuid.UUID
}

func (k *kickRecorder) handle(id uuid.UUID) {
	k.mu.Lock()
	k.ids = append(k.ids, id)
	k.mu.Unlock()
}

func (k *kickRecorder) got() []uuid.UUID {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]uuid.UUID(nil), k.ids...)
}

func emptyState() *state.ServerState {
	return &state.ServerState{
		Clients:      make(map[uuid.UUID]*state.ClientState),
		RadioClients: make(map[uuid.UUID]*state.RadioState),
	}
}

func kickMsg(clientID string) *pb.ControlMessage {
	return &pb.ControlMessage{Command: &pb.ControlMessage_KickClient{KickClient: &pb.KickClientRequest{ClientId: clientID, Reason: "kicked"}}}
}

func TestApplyKickClientCallsHandler(t *testing.T) {
	c := newTestClient(emptyState())
	rec := &kickRecorder{}
	c.SetKickHandler(rec.handle)
	id := uuid.New()

	c.applyControlMessage(kickMsg(id.String()))

	if got := rec.got(); len(got) != 1 || got[0] != id {
		t.Fatalf("handler got %v, want [%s]", got, id)
	}
}

func TestApplyKickClientInvalidGUID(t *testing.T) {
	c := newTestClient(emptyState())
	rec := &kickRecorder{}
	c.SetKickHandler(rec.handle)

	c.applyControlMessage(kickMsg("nope"))

	if got := rec.got(); len(got) != 0 {
		t.Fatalf("handler called with %v for an invalid GUID", got)
	}
}

func TestApplyDeltaLeftCallsHandler(t *testing.T) {
	id := uuid.New()
	ss := emptyState()
	ss.Clients[id] = &state.ClientState{Name: "Pilot"}
	ss.RadioClients[id] = &state.RadioState{}
	c := newTestClient(ss)
	rec := &kickRecorder{}
	// The handler takes the state lock, so it must run after applyDelta has
	// released it -- the real handler (voice.Server.DisconnectClient) may.
	c.SetKickHandler(func(got uuid.UUID) {
		ss.RLock()
		ss.RUnlock()
		rec.handle(got)
	})

	done := make(chan struct{})
	go func() {
		c.applyControlMessage(&pb.ControlMessage{Command: &pb.ControlMessage_ClientDelta{ClientDelta: &pb.ClientDelta{Type: pb.ClientDelta_LEFT, ClientId: id.String()}}})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("deadlock: the handler ran under the state lock")
	}

	ss.RLock()
	_, c1 := ss.Clients[id]
	_, r1 := ss.RadioClients[id]
	ss.RUnlock()
	if c1 || r1 {
		t.Fatal("LEFT must still clear the state maps")
	}
	if got := rec.got(); len(got) != 1 || got[0] != id {
		t.Fatalf("handler got %v, want [%s]", got, id)
	}
}

func TestApplyKickWithoutHandler(t *testing.T) {
	c := newTestClient(emptyState())
	c.applyControlMessage(kickMsg(uuid.NewString())) // must not panic
}
