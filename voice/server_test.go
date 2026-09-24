package voice

import (
	"net"
	"strings"
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

// newBoundTestServer returns a voice server listening on a real loopback UDP
// socket, so ACK behaviour can be asserted for real.
func newBoundTestServer(t *testing.T, ss *state.ServerState) *Server {
	t.Helper()
	s := NewServer(ss, slog.New(slog.NewTextHandler(os.Stderr, nil)), &state.DistributionState{}, &state.SettingsState{})
	udpAddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	s.conn = conn
	s.running = true
	return s
}

// newTestPeer returns a loopback UDP socket standing in for a client.
func newTestPeer(t *testing.T) *net.UDPConn {
	t.Helper()
	udpAddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// expectAck fails unless a packet of the given type arrives promptly.
func expectAck(t *testing.T, peer *net.UDPConn, want PacketType) {
	t.Helper()
	_ = peer.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	buf := make([]byte, BufferSize)
	n, _, err := peer.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("expected a %v, got read error: %v", want, err)
	}
	pkt, err := ParsePacket(buf[:n])
	if err != nil {
		t.Fatalf("expected a %v, got unparseable packet: %v", want, err)
	}
	if pkt.Type != want {
		t.Fatalf("expected %v, got %v", want, pkt.Type)
	}
}

// expectNoAck fails if any packet arrives within the window.
func expectNoAck(t *testing.T, peer *net.UDPConn) {
	t.Helper()
	_ = peer.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	buf := make([]byte, BufferSize)
	n, _, err := peer.ReadFromUDP(buf)
	if err == nil {
		t.Fatalf("expected no reply, but received %d bytes", n)
	}
	if netErr, ok := err.(net.Error); !ok || !netErr.Timeout() {
		t.Fatalf("expected a read timeout, got: %v", err)
	}
}

// addClientWithSecret registers a client and returns its generated secret.
func addClientWithSecret(t *testing.T, ss *state.ServerState, id uuid.UUID) string {
	t.Helper()
	ss.AddClient(id, &state.ClientState{Name: "Pilot", Coalition: "Blue"})
	secret, ok := ss.GetVoiceSecret(id)
	if !ok || secret == "" {
		t.Fatal("expected AddClient to generate a secret")
	}
	return secret
}

func TestHelloValidSecretBinds(t *testing.T) {
	ss := &state.ServerState{}
	s := newBoundTestServer(t, ss)
	peer := newTestPeer(t)
	id := uuid.New()
	secret := addClientWithSecret(t, ss, id)

	s.handleHelloPacket(NewVCSHelloPacket(id, secret), peer.LocalAddr().(*net.UDPAddr))

	s.RLock()
	client, bound := s.clients[id]
	s.RUnlock()
	if !bound {
		t.Fatal("a valid HELLO must bind the client")
	}
	if client.Addr.Port != peer.LocalAddr().(*net.UDPAddr).Port {
		t.Fatalf("bound to the wrong port: %v", client.Addr)
	}
	expectAck(t, peer, PacketTypeHelloAck)
}

func TestHelloWrongSecretDoesNotBind(t *testing.T) {
	ss := &state.ServerState{}
	s := newBoundTestServer(t, ss)
	peer := newTestPeer(t)
	id := uuid.New()
	addClientWithSecret(t, ss, id)

	wrong := strings.Repeat("w", VoiceSecretLen)
	s.handleHelloPacket(NewVCSHelloPacket(id, wrong), peer.LocalAddr().(*net.UDPAddr))

	s.RLock()
	_, bound := s.clients[id]
	s.RUnlock()
	if bound {
		t.Fatal("a HELLO with a wrong secret must not bind")
	}
	expectNoAck(t, peer)
}

// The hijack case: a bound victim must not be moved by an unauthenticated HELLO.
func TestHelloWrongSecretDoesNotOverwriteBinding(t *testing.T) {
	ss := &state.ServerState{}
	s := newBoundTestServer(t, ss)
	victim := newTestPeer(t)
	attacker := newTestPeer(t)
	id := uuid.New()
	secret := addClientWithSecret(t, ss, id)

	victimAddr := victim.LocalAddr().(*net.UDPAddr)
	s.handleHelloPacket(NewVCSHelloPacket(id, secret), victimAddr)
	expectAck(t, victim, PacketTypeHelloAck)

	wrong := strings.Repeat("w", VoiceSecretLen)
	s.handleHelloPacket(NewVCSHelloPacket(id, wrong), attacker.LocalAddr().(*net.UDPAddr))

	s.RLock()
	client := s.clients[id]
	s.RUnlock()
	if client == nil {
		t.Fatal("the victim's binding was removed")
	}
	if client.Addr.Port != victimAddr.Port {
		t.Fatalf("the binding was hijacked: expected port %d, got %d", victimAddr.Port, client.Addr.Port)
	}
	expectNoAck(t, attacker)
}

func TestHelloMissingSecretRejected(t *testing.T) {
	ss := &state.ServerState{}
	s := newBoundTestServer(t, ss)
	peer := newTestPeer(t)
	id := uuid.New()
	addClientWithSecret(t, ss, id)

	s.handleHelloPacket(NewVCSHelloPacket(id, ""), peer.LocalAddr().(*net.UDPAddr))

	s.RLock()
	_, bound := s.clients[id]
	s.RUnlock()
	if bound {
		t.Fatal("a HELLO with no secret must not bind")
	}
	expectNoAck(t, peer)
}

func TestHelloShortSecretRejected(t *testing.T) {
	ss := &state.ServerState{}
	s := newBoundTestServer(t, ss)
	peer := newTestPeer(t)
	id := uuid.New()
	secret := addClientWithSecret(t, ss, id)

	s.handleHelloPacket(NewVCSHelloPacket(id, secret[:VoiceSecretLen-1]), peer.LocalAddr().(*net.UDPAddr))

	s.RLock()
	_, bound := s.clients[id]
	s.RUnlock()
	if bound {
		t.Fatal("a truncated secret must not bind")
	}
	expectNoAck(t, peer)
}

// The pre-existing DoesClientExist rejection must still hold.
func TestHelloUnknownClientRejected(t *testing.T) {
	ss := &state.ServerState{}
	s := newBoundTestServer(t, ss)
	peer := newTestPeer(t)
	id := uuid.New() // never added to state

	s.handleHelloPacket(NewVCSHelloPacket(id, strings.Repeat("a", VoiceSecretLen)), peer.LocalAddr().(*net.UDPAddr))

	s.RLock()
	_, bound := s.clients[id]
	s.RUnlock()
	if bound {
		t.Fatal("an unknown client must not bind")
	}
	expectNoAck(t, peer)
}

// Review Focus 1: a ClientState built from an old control server's delta has an
// empty secret. It must match nothing, not match an empty presented secret.
func TestHelloEmptyStoredSecretRejected(t *testing.T) {
	id := uuid.New()
	ss := &state.ServerState{
		Clients:      map[uuid.UUID]*state.ClientState{id: {Name: "Pilot", VoiceSecret: ""}},
		RadioClients: map[uuid.UUID]*state.RadioState{},
	}
	s := newBoundTestServer(t, ss)
	peer := newTestPeer(t)

	s.handleHelloPacket(NewVCSHelloPacket(id, ""), peer.LocalAddr().(*net.UDPAddr))

	s.RLock()
	_, bound := s.clients[id]
	s.RUnlock()
	if bound {
		t.Fatal("a client with no secret on record must never bind")
	}
	expectNoAck(t, peer)
}

// Review Focus 5: run with -race.
func TestHelloRaceWithCleanup(t *testing.T) {
	ss := &state.ServerState{}
	s := newBoundTestServer(t, ss)
	peer := newTestPeer(t)
	id := uuid.New()
	secret := addClientWithSecret(t, ss, id)
	addr := peer.LocalAddr().(*net.UDPAddr)

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Lock()
		delete(s.clients, id)
		s.Unlock()
	}()
	s.handleHelloPacket(NewVCSHelloPacket(id, secret), addr)
	<-done
}

func TestRejectLimiterSuppressesWithinWindow(t *testing.T) {
	var lim rejectLimiter
	base := time.Now()

	if ok, suppressed := lim.shouldLog(base); !ok || suppressed != 0 {
		t.Fatalf("the first rejection must log: ok=%v suppressed=%d", ok, suppressed)
	}
	for i := 0; i < 5; i++ {
		if ok, _ := lim.shouldLog(base.Add(time.Second)); ok {
			t.Fatal("rejections inside the window must be suppressed")
		}
	}
	ok, suppressed := lim.shouldLog(base.Add(rejectLogWindow + time.Second))
	if !ok {
		t.Fatal("a rejection after the window must log")
	}
	if suppressed != 5 {
		t.Fatalf("expected 5 suppressed, got %d", suppressed)
	}
}
