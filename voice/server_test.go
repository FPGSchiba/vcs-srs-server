package voice

import (
	"context"
	"net"
	"strings"
	"sync"
	"sync/atomic"
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

// capturingHandler is a minimal slog.Handler that records the "reason"
// attribute of every log record, so a test can assert WHICH guard refused a
// HELLO rather than only that it was refused.
type capturingHandler struct {
	mu      sync.Mutex
	reasons []string
}

func (h *capturingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *capturingHandler) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h *capturingHandler) WithGroup(string) slog.Handler            { return h }

func (h *capturingHandler) Handle(_ context.Context, r slog.Record) error {
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "reason" {
			h.mu.Lock()
			h.reasons = append(h.reasons, a.Value.String())
			h.mu.Unlock()
		}
		return true
	})
	return nil
}

func (h *capturingHandler) snapshot() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.reasons...)
}

// newCapturingTestServer is newBoundTestServer with a handler that records
// the "reason" attribute of every rejection, so a test can assert WHICH
// guard refused a HELLO rather than only that it was refused.
func newCapturingTestServer(t *testing.T, ss *state.ServerState) (*Server, *capturingHandler) {
	t.Helper()
	h := &capturingHandler{}
	s := NewServer(ss, slog.New(h), &state.DistributionState{}, &state.SettingsState{})
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
	return s, h
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
	s, h := newCapturingTestServer(t, ss)
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
	assertRejectReason(t, h, "invalid secret")
}

// The hijack case: a bound victim must not be moved by an unauthenticated HELLO.
func TestHelloWrongSecretDoesNotOverwriteBinding(t *testing.T) {
	ss := &state.ServerState{}
	s, h := newCapturingTestServer(t, ss)
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
	assertRejectReason(t, h, "invalid secret")
}

// assertRejectReason fails unless exactly one rejection with the given reason
// was captured. Asserting the reason (not just "was rejected") is the only
// way to tell a guard actually fired from it being silently shadowed by a
// later guard that happens to reject the same input for a different cause.
func assertRejectReason(t *testing.T, h *capturingHandler, want string) {
	t.Helper()
	got := h.snapshot()
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 captured rejection, got %d: %v", len(got), got)
	}
	if got[0] != want {
		t.Fatalf("expected rejection reason %q, got %q", want, got[0])
	}
}

func TestHelloMissingSecretRejected(t *testing.T) {
	ss := &state.ServerState{}
	s, h := newCapturingTestServer(t, ss)
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
	assertRejectReason(t, h, "missing or short secret")
}

func TestHelloShortSecretRejected(t *testing.T) {
	ss := &state.ServerState{}
	s, h := newCapturingTestServer(t, ss)
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
	assertRejectReason(t, h, "missing or short secret")
}

// The pre-existing DoesClientExist rejection must still hold.
func TestHelloUnknownClientRejected(t *testing.T) {
	ss := &state.ServerState{}
	s, h := newCapturingTestServer(t, ss)
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
	assertRejectReason(t, h, "unknown client")
}

// Review Focus 1: a ClientState built from an old control server's delta has an
// empty secret. It must match nothing, not match a full-length presented secret.
// This is the dangerous path: a full-length presented secret is what an attacker
// would actually send, so this is the input that must be pinned against the
// stored-empty-secret case.
func TestHelloEmptyStoredSecretRejected(t *testing.T) {
	id := uuid.New()
	ss := &state.ServerState{
		Clients:      map[uuid.UUID]*state.ClientState{id: {Name: "Pilot", VoiceSecret: ""}},
		RadioClients: map[uuid.UUID]*state.RadioState{},
	}
	s, h := newCapturingTestServer(t, ss)
	peer := newTestPeer(t)

	s.handleHelloPacket(NewVCSHelloPacket(id, strings.Repeat("a", VoiceSecretLen)), peer.LocalAddr().(*net.UDPAddr))

	s.RLock()
	_, bound := s.clients[id]
	s.RUnlock()
	if bound {
		t.Fatal("a client with no secret on record must never bind")
	}
	expectNoAck(t, peer)
	assertRejectReason(t, h, "no secret on record")
}

// A client with no secret on record must reject regardless of the presented
// payload's length: this exercises the same guard as its sibling above, just
// with a short (empty) payload instead of a full-length one.
func TestHelloEmptyStoredSecretRejectsAnyPayloadLength(t *testing.T) {
	id := uuid.New()
	ss := &state.ServerState{
		Clients:      map[uuid.UUID]*state.ClientState{id: {Name: "Pilot", VoiceSecret: ""}},
		RadioClients: map[uuid.UUID]*state.RadioState{},
	}
	s, h := newCapturingTestServer(t, ss)
	peer := newTestPeer(t)

	s.handleHelloPacket(NewVCSHelloPacket(id, ""), peer.LocalAddr().(*net.UDPAddr))

	s.RLock()
	_, bound := s.clients[id]
	s.RUnlock()
	if bound {
		t.Fatal("a client with no secret on record must never bind")
	}
	expectNoAck(t, peer)
	assertRejectReason(t, h, "no secret on record")
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

	// The first rejection always logs immediately.
	if ok, suppressed := lim.shouldLog(base); !ok || suppressed != 0 {
		t.Fatalf("the first rejection must log: ok=%v suppressed=%d", ok, suppressed)
	}

	// Five rejections inside the window are suppressed.
	for i := 0; i < 5; i++ {
		if ok, _ := lim.shouldLog(base.Add(time.Second)); ok {
			t.Fatal("rejections inside the window must be suppressed")
		}
	}

	// A rejection exactly at the window boundary must log: the guard is
	// ">=", not ">". Using the boundary value itself (not boundary+something)
	// pins that exact comparison.
	boundary := base.Add(rejectLogWindow)
	ok, suppressed := lim.shouldLog(boundary)
	if !ok {
		t.Fatal("a rejection exactly at the window boundary must log")
	}
	if suppressed != 5 {
		t.Fatalf("expected 5 suppressed, got %d", suppressed)
	}

	// The suppressed counter must reset after logging, not accumulate: two
	// further suppressed rejections in the next window, then a log at the
	// following boundary must report exactly 2, not 5+2.
	for i := 0; i < 2; i++ {
		if ok, _ := lim.shouldLog(boundary.Add(time.Second)); ok {
			t.Fatal("rejections inside the third window must be suppressed")
		}
	}
	ok, suppressed = lim.shouldLog(boundary.Add(rejectLogWindow + time.Second))
	if !ok {
		t.Fatal("a rejection after the third window must log")
	}
	if suppressed != 2 {
		t.Fatalf("expected the suppressed counter to reset: got %d, want 2", suppressed)
	}
}

// TestRejectLimiterConcurrentShouldLog verifies that shouldLog, called
// concurrently from many packet-handling goroutines as it is in production
// (handlePacket spawns one goroutine per datagram), lets exactly one caller
// log per window. Run with -race.
func TestRejectLimiterConcurrentShouldLog(t *testing.T) {
	var lim rejectLimiter
	const n = 50
	now := time.Now()

	var wg sync.WaitGroup
	var loggedCount int32
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, _ := lim.shouldLog(now); ok {
				atomic.AddInt32(&loggedCount, 1)
			}
		}()
	}
	wg.Wait()

	if loggedCount != 1 {
		t.Fatalf("expected exactly 1 goroutine to log, got %d", loggedCount)
	}
}

// TestRejectHelloPerReasonLimitersAreIndependent proves that flooding one
// rejection reason does not arm the window for, and so does not suppress,
// a different reason. Before the fix all four reasons shared one
// rejectLimiter, so an attacker spraying random UUIDs ("unknown client")
// could suppress logging of "invalid secret" against a real session -- the
// highest-value signal the server can emit, since it means someone holds a
// valid GUID and is attacking a live session.
func TestRejectHelloPerReasonLimitersAreIndependent(t *testing.T) {
	h := &capturingHandler{}
	s := &Server{logger: slog.New(h)}
	addr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5002}

	// Flood "unknown client": the first call logs and arms that reason's
	// window, every subsequent call within the window is suppressed.
	for i := 0; i < 10; i++ {
		s.rejectHello(rejectUnknownClient, uuid.New(), addr)
	}

	// A different reason must still log immediately: it has its own,
	// unarmed limiter.
	s.rejectHello(rejectInvalidSecret, uuid.New(), addr)

	got := h.snapshot()
	unknownCount, invalidCount := 0, 0
	for _, reason := range got {
		switch reason {
		case "unknown client":
			unknownCount++
		case "invalid secret":
			invalidCount++
		}
	}
	if unknownCount != 1 {
		t.Fatalf("expected exactly 1 logged \"unknown client\" rejection (the rest suppressed within the window), got %d", unknownCount)
	}
	if invalidCount != 1 {
		t.Fatalf("expected \"invalid secret\" to log despite the \"unknown client\" flood, got %d occurrences (reasons: %v)", invalidCount, got)
	}
}

// Pins that an IPv4-mapped IPv6 form of the bound address is accepted, and
// that a different IP or port is not. Note this does NOT distinguish IP.Equal
// from an addr.String() comparison — Go normalizes ::ffff:127.0.0.1 to
// 127.0.0.1 in String() — so it is a behavior test, not an implementation test.
// IP.Equal is used anyway: it does not depend on String()'s normalization
// remaining stable across Go versions and platforms. Trade-off: IP.Equal
// ignores the IPv6 zone, which makes it marginally more permissive than a
// String() compare would be — a cost of this choice, not a reason for it.
func TestIsBoundAddrMatchesIPv4MappedIPv6(t *testing.T) {
	s := newTestServer()
	id := uuid.New()
	s.clients[id] = &Client{
		Addr:     &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5002},
		LastSeen: time.Now(),
	}

	mapped := &net.UDPAddr{IP: net.ParseIP("::ffff:127.0.0.1"), Port: 5002}
	if !s.isBoundAddr(id, mapped) {
		t.Fatal("an IPv4-mapped IPv6 address must match the same IPv4 binding")
	}

	wrongPort := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5003}
	if s.isBoundAddr(id, wrongPort) {
		t.Fatal("a different port must not match")
	}

	wrongIP := &net.UDPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 5002}
	if s.isBoundAddr(id, wrongIP) {
		t.Fatal("a different IP must not match")
	}
}

func TestIsBoundAddrUnknownClient(t *testing.T) {
	s := newTestServer()
	addr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5002}
	if s.isBoundAddr(uuid.New(), addr) {
		t.Fatal("an unbound client must not match any address")
	}
}

// Impersonation: an attacker who sniffed the UUID must not be able to transmit
// as the victim from a different address.
func TestVoiceFromUnboundAddressDropped(t *testing.T) {
	ss := &state.ServerState{}
	s := newBoundTestServer(t, ss)
	victim := newTestPeer(t)
	attacker := newTestPeer(t)
	id := uuid.New()
	secret := addClientWithSecret(t, ss, id)

	s.handleHelloPacket(NewVCSHelloPacket(id, secret), victim.LocalAddr().(*net.UDPAddr))
	expectAck(t, victim, PacketTypeHelloAck)

	s.RLock()
	before := s.clients[id].LastSeen
	s.RUnlock()

	voicePkt := NewVCSVoicePacket(id, 1, 243000, make([]byte, 40))
	s.handleVoicePacket(voicePkt, attacker.LocalAddr().(*net.UDPAddr))

	s.RLock()
	after := s.clients[id].LastSeen
	s.RUnlock()
	if !after.Equal(before) {
		t.Fatal("a voice packet from an unbound address must not refresh the session")
	}
}

// A spoofed BYE must not be able to disconnect an arbitrary player.
func TestByeFromUnboundAddressDoesNotDisconnect(t *testing.T) {
	ss := &state.ServerState{}
	s := newBoundTestServer(t, ss)
	victim := newTestPeer(t)
	attacker := newTestPeer(t)
	id := uuid.New()
	secret := addClientWithSecret(t, ss, id)

	s.handleHelloPacket(NewVCSHelloPacket(id, secret), victim.LocalAddr().(*net.UDPAddr))
	expectAck(t, victim, PacketTypeHelloAck)

	s.handleGoodbyePacket(&VCSPacket{SenderID: id}, attacker.LocalAddr().(*net.UDPAddr))

	s.RLock()
	_, stillBound := s.clients[id]
	s.RUnlock()
	if !stillBound {
		t.Fatal("a BYE from an unbound address must not disconnect the client")
	}
}

func TestByeFromBoundAddressDisconnects(t *testing.T) {
	ss := &state.ServerState{}
	s := newBoundTestServer(t, ss)
	peer := newTestPeer(t)
	id := uuid.New()
	secret := addClientWithSecret(t, ss, id)
	addr := peer.LocalAddr().(*net.UDPAddr)

	s.handleHelloPacket(NewVCSHelloPacket(id, secret), addr)
	expectAck(t, peer, PacketTypeHelloAck)

	s.handleGoodbyePacket(&VCSPacket{SenderID: id}, addr)

	s.RLock()
	_, stillBound := s.clients[id]
	s.RUnlock()
	if stillBound {
		t.Fatal("a BYE from the bound address must disconnect the client")
	}
}

func TestKeepaliveFromUnboundAddressIgnored(t *testing.T) {
	ss := &state.ServerState{}
	s := newBoundTestServer(t, ss)
	victim := newTestPeer(t)
	attacker := newTestPeer(t)
	id := uuid.New()
	secret := addClientWithSecret(t, ss, id)

	s.handleHelloPacket(NewVCSHelloPacket(id, secret), victim.LocalAddr().(*net.UDPAddr))
	expectAck(t, victim, PacketTypeHelloAck)

	s.RLock()
	before := s.clients[id].LastSeen
	s.RUnlock()

	// Sleep so a refresh would be observably different from `before`.
	time.Sleep(5 * time.Millisecond)

	s.handleKeepalivePacket(NewVCSKeepalivePacket(id), attacker.LocalAddr().(*net.UDPAddr))
	expectNoAck(t, attacker)

	// The ACK-to-bound-address behavior alone would let expectNoAck above pass
	// even if the binding check were removed, since the ACK still would not
	// reach the attacker. Pin the guard's real effect directly: a keepalive
	// from an unbound address must not refresh session liveness.
	s.RLock()
	after := s.clients[id].LastSeen
	s.RUnlock()
	if !after.Equal(before) {
		t.Fatal("a keepalive from an unbound address must not refresh the session")
	}
}

// KEEPALIVE must never become a rebind path — a rebind is the whole attack.
//
// This currently passes vacuously: v.clients has exactly one write site
// (handleHelloPacket), so nothing in handleKeepalivePacket could rebind the
// address regardless of this test. It is a forward-looking regression guard,
// not evidence that handleKeepalivePacket actively defends against rebinding
// today — it exists to fail if a future change adds a second write site to
// v.clients.
func TestKeepaliveNeverRebindsAddress(t *testing.T) {
	ss := &state.ServerState{}
	s := newBoundTestServer(t, ss)
	victim := newTestPeer(t)
	attacker := newTestPeer(t)
	id := uuid.New()
	secret := addClientWithSecret(t, ss, id)

	victimAddr := victim.LocalAddr().(*net.UDPAddr)
	s.handleHelloPacket(NewVCSHelloPacket(id, secret), victimAddr)
	expectAck(t, victim, PacketTypeHelloAck)

	s.handleKeepalivePacket(NewVCSKeepalivePacket(id), attacker.LocalAddr().(*net.UDPAddr))

	s.RLock()
	bound := s.clients[id].Addr
	s.RUnlock()
	if bound.Port != victimAddr.Port {
		t.Fatalf("keepalive rebound the session: expected port %d, got %d", victimAddr.Port, bound.Port)
	}
}

// The ACK must go to the bound address, not the packet source, so nobody can
// elicit a reply for a sniffed UUID.
//
// This test always sends the keepalive from the already-bound address, so
// addr and boundAddr are equal by construction while the binding guard
// holds — it cannot observe the ACK-destination change on its own. It is
// TestKeepaliveFromUnboundAddressIgnored, sending from an unbound address,
// that actually distinguishes the two destinations.
func TestKeepaliveAckGoesToBoundAddress(t *testing.T) {
	ss := &state.ServerState{}
	s := newBoundTestServer(t, ss)
	peer := newTestPeer(t)
	id := uuid.New()
	secret := addClientWithSecret(t, ss, id)
	addr := peer.LocalAddr().(*net.UDPAddr)

	s.handleHelloPacket(NewVCSHelloPacket(id, secret), addr)
	expectAck(t, peer, PacketTypeHelloAck)

	s.handleKeepalivePacket(NewVCSKeepalivePacket(id), addr)
	expectAck(t, peer, PacketTypeKeepalive)
}
