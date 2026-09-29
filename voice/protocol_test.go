package voice

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestNewVCSHelloPacketCarriesSecret(t *testing.T) {
	id := uuid.New()
	secret := strings.Repeat("a", VoiceSecretLen)
	pkt := NewVCSHelloPacket(id, secret)

	if pkt.Type != PacketTypeHello {
		t.Fatalf("expected HELLO, got %v", pkt.Type)
	}
	if pkt.SenderID != id {
		t.Fatalf("expected sender %v, got %v", id, pkt.SenderID)
	}
	if len(pkt.Payload) != VoiceSecretLen {
		t.Fatalf("expected a %d-byte payload, got %d", VoiceSecretLen, len(pkt.Payload))
	}
}

func TestHelloSecretRoundTrip(t *testing.T) {
	id := uuid.New()
	secret := strings.Repeat("x", VoiceSecretLen)

	parsed, err := ParsePacket(NewVCSHelloPacket(id, secret).SerializePacket())
	if err != nil {
		t.Fatalf("ParsePacket failed: %v", err)
	}

	got, ok := parsed.HelloSecret()
	if !ok {
		t.Fatal("expected a secret in the parsed packet")
	}
	if got != secret {
		t.Fatalf("expected %q, got %q", secret, got)
	}
}

// Review Focus 4: base64.RawURLEncoding emits '-' and '_', which must survive
// serialization and parsing unaltered.
func TestHelloSecretRoundTripURLSafeChars(t *testing.T) {
	id := uuid.New()
	secret := "-_" + strings.Repeat("Zz09", 10) + "_"
	if len(secret) != VoiceSecretLen {
		t.Fatalf("test fixture must be %d chars, got %d", VoiceSecretLen, len(secret))
	}

	parsed, err := ParsePacket(NewVCSHelloPacket(id, secret).SerializePacket())
	if err != nil {
		t.Fatalf("ParsePacket failed: %v", err)
	}
	got, ok := parsed.HelloSecret()
	if !ok {
		t.Fatal("expected a secret")
	}
	if got != secret {
		t.Fatalf("expected %q, got %q", secret, got)
	}
}

// Review Focus 2: bytes past the secret are reserved for future protocol use
// and must be ignored rather than rejected.
func TestHelloSecretIgnoresTrailingBytes(t *testing.T) {
	secret := strings.Repeat("q", VoiceSecretLen)
	pkt := &VCSPacket{Payload: append([]byte(secret), 0xFF, 0xFE, 0xFD)}

	got, ok := pkt.HelloSecret()
	if !ok {
		t.Fatal("a longer payload must still yield a secret")
	}
	if got != secret {
		t.Fatalf("expected %q, got %q", secret, got)
	}
}

func TestHelloSecretEmptyPayload(t *testing.T) {
	pkt := &VCSPacket{Payload: nil}
	if _, ok := pkt.HelloSecret(); ok {
		t.Fatal("expected ok=false for an empty payload")
	}
}

func TestHelloSecretShortPayload(t *testing.T) {
	pkt := &VCSPacket{Payload: []byte(strings.Repeat("s", VoiceSecretLen-1))}
	if _, ok := pkt.HelloSecret(); ok {
		t.Fatal("expected ok=false for a payload shorter than VoiceSecretLen")
	}
}
