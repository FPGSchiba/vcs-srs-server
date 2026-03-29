package voice

import (
	"net"
	"testing"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/google/uuid"
	"log/slog"
	"os"
)

func newTestServer() *Server {
	return NewServer(
		&state.ServerState{},
		slog.New(slog.NewTextHandler(os.Stderr, nil)),
		&state.DistributionState{},
		&state.SettingsState{},
	)
}

// TestDisconnectClientIdempotent verifies that calling DisconnectClient twice
// on the same ID does not panic and the client is removed after the first call.
func TestDisconnectClientIdempotent(t *testing.T) {
	s := newTestServer()
	id := uuid.New()
	s.clients[id] = &Client{
		Addr:     &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5002},
		LastSeen: time.Now(),
	}

	// First disconnect should remove the client.
	s.DisconnectClient(id)
	if _, exists := s.clients[id]; exists {
		t.Fatal("client should have been removed after first disconnect")
	}

	// Second disconnect should not panic.
	s.DisconnectClient(id)
}

// TestStopIdempotent verifies that calling Stop twice does not panic.
func TestStopIdempotent(t *testing.T) {
	s := newTestServer()
	s.running = true

	if err := s.Stop(); err != nil {
		t.Fatalf("first Stop returned error: %v", err)
	}
	// Second call must not panic (covers CRIT-3 stopOnce fix).
	if err := s.Stop(); err != nil {
		t.Fatalf("second Stop returned error: %v", err)
	}
}

// TestHandleKeepaliveRace verifies no data race when a keepalive arrives for a
// client that is simultaneously removed by cleanup. Run with -race.
func TestHandleKeepaliveRace(t *testing.T) {
	s := newTestServer()
	id := uuid.New()
	s.clients[id] = &Client{
		Addr:     &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5002},
		LastSeen: time.Now().Add(-2 * time.Minute), // stale
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Simulate cleanup removing the client concurrently.
		s.Lock()
		delete(s.clients, id)
		s.Unlock()
	}()

	pkt := &VCSPacket{SenderID: id}
	s.handleKeepalivePacket(pkt, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5002})
	<-done
}
