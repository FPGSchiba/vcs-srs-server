package state

import (
	"encoding/base64"
	"testing"

	"github.com/google/uuid"
)

func newTestServerState() *ServerState {
	return &ServerState{
		Clients:      make(map[uuid.UUID]*ClientState),
		RadioClients: make(map[uuid.UUID]*RadioState),
	}
}

func TestAddClientGeneratesVoiceSecret(t *testing.T) {
	s := newTestServerState()
	id := uuid.New()
	s.AddClient(id, &ClientState{Name: "Alice", Coalition: "Blue"})

	s.RLock()
	defer s.RUnlock()
	client, exists := s.Clients[id]
	if !exists {
		t.Fatal("client not added")
	}
	if client.VoiceSecret == "" {
		t.Fatal("AddClient must generate a voice secret")
	}
	if len(client.VoiceSecret) != 43 {
		t.Fatalf("expected a 43-char secret, got %d chars: %q", len(client.VoiceSecret), client.VoiceSecret)
	}
	raw, err := base64.RawURLEncoding.DecodeString(client.VoiceSecret)
	if err != nil {
		t.Fatalf("secret is not valid base64url: %v", err)
	}
	if len(raw) != VoiceSecretBytes {
		t.Fatalf("expected %d decoded bytes, got %d", VoiceSecretBytes, len(raw))
	}
}

func TestAddClientGeneratesUniqueVoiceSecrets(t *testing.T) {
	s := newTestServerState()
	seen := make(map[string]struct{})
	for i := 0; i < 100; i++ {
		id := uuid.New()
		s.AddClient(id, &ClientState{Name: "Pilot"})
		s.RLock()
		secret := s.Clients[id].VoiceSecret
		s.RUnlock()
		if _, dup := seen[secret]; dup {
			t.Fatalf("duplicate voice secret generated: %q", secret)
		}
		seen[secret] = struct{}{}
	}
}

func TestGetVoiceSecretReturnsStoredSecret(t *testing.T) {
	s := newTestServerState()
	id := uuid.New()
	s.AddClient(id, &ClientState{Name: "Alice"})

	s.RLock()
	want := s.Clients[id].VoiceSecret
	s.RUnlock()

	got, ok := s.GetVoiceSecret(id)
	if !ok {
		t.Fatal("expected ok for a known client")
	}
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestGetVoiceSecretUnknownClient(t *testing.T) {
	s := newTestServerState()
	if _, ok := s.GetVoiceSecret(uuid.New()); ok {
		t.Fatal("expected ok=false for an unknown client")
	}
}

func TestAddClientOnNilMapsDoesNotPanic(t *testing.T) {
	s := &ServerState{}
	s.AddClient(uuid.New(), &ClientState{Name: "Alice"})
	s.RLock()
	defer s.RUnlock()
	if len(s.Clients) != 1 {
		t.Fatalf("expected 1 client, got %d", len(s.Clients))
	}
	if len(s.RadioClients) != 1 {
		t.Fatalf("expected 1 radio client, got %d", len(s.RadioClients))
	}
}

func TestIsListeningOnFrequency(t *testing.T) {
	s := newTestServerState()
	sender := uuid.New()
	rb := uuid.New()
	rr := uuid.New()
	rn := uuid.New()
	s.Clients[sender] = &ClientState{Name: "S", Coalition: "Blue"}
	s.Clients[rb] = &ClientState{Name: "RB", Coalition: "Blue"}
	s.Clients[rr] = &ClientState{Name: "RR", Coalition: "Red"}
	s.Clients[rn] = &ClientState{Name: "RN", Coalition: "Blue"}
	radios := func() *RadioState {
		return &RadioState{Radios: []Radio{
			{Frequency: 251, Enabled: true},
			{Frequency: 252, Enabled: false},
		}}
	}
	s.RadioClients[rb] = radios()
	s.RadioClients[rr] = radios()

	cases := []struct {
		name     string
		receiver uuid.UUID
		sender   uuid.UUID
		freq     float32
		global   bool
		want     bool
	}{
		{"same coalition enabled", rb, sender, 251, false, true},
		{"cross coalition non-global", rr, sender, 251, false, false},
		{"cross coalition global", rr, sender, 251, true, true},
		{"radio disabled", rb, sender, 252, false, false},
		{"frequency not tuned", rb, sender, 300, false, false},
		{"unknown sender", rb, uuid.New(), 251, false, false},
		{"unknown receiver", uuid.New(), sender, 251, false, false},
		{"receiver without radio state", rn, sender, 251, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := s.IsListeningOnFrequency(tc.receiver, tc.sender, tc.freq, tc.global)
			if got != tc.want {
				t.Fatalf("IsListeningOnFrequency(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestRadioStateCloneIndependent(t *testing.T) {
	src := &RadioState{Radios: []Radio{{ID: 1, Frequency: 100}}, Muted: false}
	cl := src.Clone()
	src.Radios[0].Frequency = 200
	src.Muted = true
	if cl.Radios[0].Frequency != 100 || cl.Muted {
		t.Fatalf("clone changed with its source: %+v", cl)
	}

	if got := (&RadioState{}).Clone(); got.Radios != nil {
		t.Fatalf("nil Radios cloned to %#v, want nil", got.Radios)
	}
	if got := (&RadioState{Radios: []Radio{}}).Clone(); got.Radios == nil {
		t.Fatal("empty Radios cloned to nil, want empty")
	}
	if got := (*RadioState)(nil).Clone(); got != nil {
		t.Fatalf("nil Clone = %+v, want nil", got)
	}
	if got := (*ClientState)(nil).Clone(); got != nil {
		t.Fatalf("nil ClientState Clone = %+v, want nil", got)
	}
}

func TestCloneClientsLockedSkipsNilAndCopies(t *testing.T) {
	s := newTestServerState()
	a, b, n := uuid.New(), uuid.New(), uuid.New()
	s.Clients[a] = &ClientState{Name: "A"}
	s.Clients[b] = &ClientState{Name: "B"}
	s.Clients[n] = nil
	s.RadioClients[a] = &RadioState{Radios: []Radio{{ID: 1}}}
	s.RadioClients[n] = nil

	s.RLock()
	clients := s.CloneClientsLocked()
	radios := s.CloneRadiosLocked()
	s.RUnlock()

	if len(clients) != 2 {
		t.Fatalf("len(clients) = %d, want 2", len(clients))
	}
	for id, c := range clients {
		if c == s.Clients[id] {
			t.Fatalf("client %s shares its pointer with live state", id)
		}
	}
	s.Clients[a].Name = "changed"
	if clients[a].Name != "A" {
		t.Fatalf("clone Name = %q, want A", clients[a].Name)
	}

	if len(radios) != 1 {
		t.Fatalf("len(radios) = %d, want 1", len(radios))
	}
	s.RadioClients[a].Radios[0].ID = 9
	if radios[a].Radios[0].ID != 1 {
		t.Fatal("radio clone shares its Radios slice with live state")
	}
}
