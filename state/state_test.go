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
