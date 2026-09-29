package voice

import (
	"crypto/subtle"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/FPGSchiba/vcs-srs-server/utils"
	"github.com/FPGSchiba/vcs-srs-server/voiceontrol"
	"github.com/google/uuid"
)

const (
	BufferSize = 1024 // UDP buffer size
)

// rejectLogWindow is the minimum interval between logged HELLO rejections.
const rejectLogWindow = 30 * time.Second

// rejectLimiter rate-limits rejection logging so repeated attempts stay visible
// without letting an attacker flood the log.
//
// Deliberately keyed by rejection reason (a fixed array of four), never by
// source address: UDP source addresses are trivially spoofable, so a
// per-address map would itself be a memory-exhaustion vector and its
// attribution would be unreliable anyway. Keying by reason instead has fixed,
// attacker-independent cardinality — there are exactly four reasons a HELLO
// can be rejected, and nothing about the packet chooses which limiter is
// used beyond that fixed set — so it carries none of that risk, while
// stopping a flood of one reason (e.g. "unknown client" from sprayed random
// UUIDs) from arming the shared window and suppressing a different reason
// (e.g. "invalid secret" against a real session, the highest-value signal
// the server can emit).
type rejectLimiter struct {
	mu         sync.Mutex
	lastLogged time.Time
	suppressed int
}

// rejectReason identifies which guard refused a HELLO. It exists so each
// reason can be rate-limited independently — see rejectLimiter.
type rejectReason int

const (
	rejectUnknownClient rejectReason = iota
	rejectNoSecretOnRecord
	rejectMissingOrShortSecret
	rejectInvalidSecret
	numRejectReasons
)

// String returns the log-facing reason text. Existing tests assert on these
// exact strings, so they must not change when the limiter keying does.
func (r rejectReason) String() string {
	switch r {
	case rejectUnknownClient:
		return "unknown client"
	case rejectNoSecretOnRecord:
		return "no secret on record"
	case rejectMissingOrShortSecret:
		return "missing or short secret"
	case rejectInvalidSecret:
		return "invalid secret"
	default:
		return "unknown reason"
	}
}

// shouldLog reports whether this rejection should be logged now and, if so, how
// many rejections were suppressed since the last logged one.
func (r *rejectLimiter) shouldLog(now time.Time) (bool, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lastLogged.IsZero() || now.Sub(r.lastLogged) >= rejectLogWindow {
		suppressed := r.suppressed
		r.suppressed = 0
		r.lastLogged = now
		return true, suppressed
	}
	r.suppressed++
	return false, 0
}

type Client struct {
	Addr             *net.UDPAddr
	LastSeen         time.Time
	LatencyToVoiceMs int64 // measured RTT to this voice node (from keepalive echo)
}

type Server struct {
	sync.RWMutex
	conn              *net.UDPConn
	clients           map[uuid.UUID]*Client
	serverState       *state.ServerState
	settingsState     *state.SettingsState
	distributionState *state.DistributionState
	logger            *slog.Logger
	running           bool
	stopChan          chan struct{}
	stopOnce          sync.Once
	controlClient     *voiceontrol.VoiceControlClient
	serverId          string
	helloRejects      [numRejectReasons]rejectLimiter
}

func NewServer(state *state.ServerState, logger *slog.Logger, distributionState *state.DistributionState, settingsState *state.SettingsState) *Server {
	return &Server{
		clients:           make(map[uuid.UUID]*Client),
		serverState:       state,
		logger:            logger,
		settingsState:     settingsState,
		stopChan:          make(chan struct{}),
		distributionState: distributionState,
		serverId:          uuid.New().String(),
	}
}

func (v *Server) isDistributedServer() bool {
	v.distributionState.RLock()
	defer v.distributionState.RUnlock()
	return v.distributionState.DistributionMode == state.DistributionModeVoice
}

func (v *Server) Listen(address string, stopChan chan struct{}) error {
	if v.isDistributedServer() {
		// Initialize control client if this is a distributed server
		v.distributionState.RLock()
		isGlobal := v.distributionState.IsGlobal
		v.distributionState.RUnlock()

		v.controlClient = voiceontrol.NewVoiceControlClient(
			v.serverId,
			v.settingsState,
			v.serverState,
			isGlobal,
			v.logger,
		)
		if err := v.controlClient.ConnectControlServer(); err != nil {
			v.logger.Error("Failed to connect to control server", "error", err)
			return err
		}
	}

	addr, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		return err
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return err
	}

	v.Lock()
	v.conn = conn
	v.running = true
	v.Unlock()

	v.logger.Info("Voice server started", "address", address)

	// Start the cleanup routine
	go v.cleanupRoutine()

	// Main receive loop
	buffer := make([]byte, BufferSize)
	for {
		select {
		case <-stopChan:
			v.logger.Info("Stopping voice server...")
			return nil
		default:
			// Set read deadline to allow checking stop channel
			err := v.conn.SetReadDeadline(time.Now().Add(1 * time.Second))
			if err != nil {
				return err
			}
			n, remoteAddr, err := v.conn.ReadFromUDP(buffer)

			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}
				v.logger.Error("Error reading UDP", "error", err)
				continue
			}

			v.serverState.RLock()
			_, banned := utils.FindByFunc(v.serverState.BannedState.BannedClients, func(bc state.BannedClient) bool {
				return bc.IPAddress == remoteAddr.IP.String()
			})
			v.serverState.RUnlock()
			if banned {
				v.logger.Warn("Banned client attempted to connect", "IP", remoteAddr.IP.String())
				continue
			}

			// Handle the received packet
			pkt := make([]byte, n)
			copy(pkt, buffer[:n])
			go v.handlePacket(pkt, remoteAddr)
		}
	}
}

func (v *Server) handlePacket(data []byte, addr *net.UDPAddr) {
	if !v.isRunning() {
		v.logger.Warn("Voice server is not running, ignoring packet")
		return
	}

	packet, err := ParsePacket(data)
	if err != nil {
		v.logger.Error("Failed to parse voice packet", "error", err)
		// Optionally send an error response back to the client
		return
	}

	switch packet.Type {
	case PacketTypeHello:
		v.handleHelloPacket(packet, addr)
	case PacketTypeVoice:
		v.handleVoicePacket(packet, addr)
	case PacketTypeBye:
		v.handleGoodbyePacket(packet, addr)
	case PacketTypeKeepalive:
		v.handleKeepalivePacket(packet, addr)
	default:
		v.logger.Debug("Unknown packet type received", "type", packet.Type)
	}
}

// rejectHello records a refused HELLO without binding anything, at a rate
// that keeps repeated attempts visible without letting an attacker flood the
// log. Each reason has its own limiter — see rejectLimiter — so a flood
// under one reason cannot suppress a rejection logged for another.
func (v *Server) rejectHello(reason rejectReason, senderID uuid.UUID, addr *net.UDPAddr) {
	if ok, suppressed := v.helloRejects[reason].shouldLog(time.Now()); ok {
		v.logger.Warn("Rejected voice HELLO",
			"reason", reason.String(),
			"sender_id", senderID,
			"addr", addr.String(),
			"suppressed_since_last", suppressed)
	}
}

// isBoundAddr reports whether addr is the address currently bound to clientID.
//
// A verified HELLO is the only way to create or change a binding, so this is
// what authenticates every other packet type. IP.Equal is used rather than an
// addr.String() comparison. Not because a string compare would reject the
// IPv4-mapped form — Go normalizes ::ffff:127.0.0.1 to 127.0.0.1 in String(),
// so it would not — but because IP.Equal states the intent directly and does
// not depend on that normalization remaining stable across Go versions and
// platforms. Trade-off: IP.Equal ignores the IPv6 zone, which makes this
// marginally more permissive than a String() compare would be.
func (v *Server) isBoundAddr(clientID uuid.UUID, addr *net.UDPAddr) bool {
	if addr == nil {
		return false
	}
	v.RLock()
	defer v.RUnlock()
	client, exists := v.clients[clientID]
	if !exists || client.Addr == nil {
		return false
	}
	return client.Addr.IP.Equal(addr.IP) && client.Addr.Port == addr.Port
}

func (v *Server) handleHelloPacket(packet *VCSPacket, addr *net.UDPAddr) {
	expected, known := v.serverState.GetVoiceSecret(packet.SenderID)
	if !known {
		v.rejectHello(rejectUnknownClient, packet.SenderID, addr)
		return
	}
	// Defensive: a voice node fed by an older control server may hold a client
	// with no secret on record. No input reaches this today — a short payload is
	// rejected by HelloSecret and a full-length one fails the length check inside
	// ConstantTimeCompare — but relying on that is fragile, so reject explicitly
	// rather than depending on the comparison's length behaviour.
	if expected == "" {
		v.rejectHello(rejectNoSecretOnRecord, packet.SenderID, addr)
		return
	}
	presented, ok := packet.HelloSecret()
	if !ok {
		v.rejectHello(rejectMissingOrShortSecret, packet.SenderID, addr)
		return
	}
	if subtle.ConstantTimeCompare([]byte(presented), []byte(expected)) != 1 {
		v.rejectHello(rejectInvalidSecret, packet.SenderID, addr)
		return
	}

	v.logger.Info("Accepted voice HELLO", "sender_id", packet.SenderID, "addr", addr.String())

	v.Lock()
	v.clients[packet.SenderID] = &Client{
		Addr:     addr,
		LastSeen: time.Now(),
	}
	v.Unlock()

	if v.controlClient != nil {
		go v.controlClient.ReportClientConnected(packet.SenderID, addr)
	}

	if v.conn == nil {
		v.logger.Warn("No UDP connection available to send hello acknowledgment")
		return
	}

	ackPacket := NewVCSHelloAckPacket(packet.SenderID)
	ackData := ackPacket.SerializePacket()
	_, err := v.conn.WriteToUDP(ackData, addr)
	if err != nil {
		v.logger.Error("Failed to send hello acknowledgment",
			"to", addr.String(),
			"error", err)
		return
	}
}

func (v *Server) handleKeepalivePacket(packet *VCSPacket, addr *net.UDPAddr) {
	if !v.isBoundAddr(packet.SenderID, addr) {
		v.logger.Debug("Ignoring keepalive from an unbound address",
			"sender_id", packet.SenderID, "addr", addr.String())
		return
	}

	v.Lock()
	client, exists := v.clients[packet.SenderID]
	var boundAddr *net.UDPAddr
	if exists {
		client.LastSeen = time.Now()
		boundAddr = client.Addr
		// If the client echoed our timestamp, compute the round-trip latency.
		if ts := ExtractKeepaliveTimestamp(packet.Payload); ts > 0 {
			rtt := time.Now().UnixMilli() - ts
			if rtt > 0 {
				client.LatencyToVoiceMs = rtt
			}
		}
	}
	v.Unlock()
	if !exists {
		return
	}
	v.logger.Debug("Updated last seen for client", "sender_id", packet.SenderID, "addr", addr.String())

	if v.conn == nil {
		v.logger.Warn("No UDP connection available to send keepalive acknowledgment")
		return
	}

	// Reply to the bound address rather than the packet source, so a spoofed
	// keepalive cannot elicit a reply for a sniffed session id.
	ackPacket := NewVCSKeepaliveAckPacket(packet.SenderID)
	ackData := ackPacket.SerializePacket()
	_, err := v.conn.WriteToUDP(ackData, boundAddr)
	if err != nil {
		v.logger.Error("Failed to send keepalive acknowledgment",
			"to", boundAddr.String(),
			"error", err)
	}
}

func (v *Server) handleVoicePacket(packet *VCSPacket, addr *net.UDPAddr) {
	if !v.isBoundAddr(packet.SenderID, addr) {
		v.logger.Debug("Dropping voice packet from an unbound address",
			"sender_id", packet.SenderID, "addr", addr.String())
		return
	}

	if v.settingsState.IsFrequencyTest(packet.FrequencyAsFloat32()) {
		v.handleTestFrequencyPacket(packet)
		return
	}

	v.Lock()
	client, exists := v.clients[packet.SenderID]
	if exists {
		client.LastSeen = time.Now()
	}
	v.Unlock()
	if !exists {
		v.logger.Warn("Received voice packet from unknown client", "sender_id", packet.SenderID)
		return
	}

	if v.serverState.IsClientMuted(packet.SenderID) {
		v.logger.Debug("Dropping voice packet from muted client", "sender_id", packet.SenderID)
		return
	}

	if len(packet.Payload) > 5 {
		v.broadcastVoice(packet, packet.SenderID)
	}

	v.logger.Debug("Received voice packet",
		"sender_id", packet.SenderID,
		"frequency", packet.FrequencyAsFloat32(),
		"size", len(packet.Payload))
}

// handleTestFrequencyPacket echoes the received packet back to the sender so the client hears it.
func (v *Server) handleTestFrequencyPacket(packet *VCSPacket) {
	v.Lock()
	client, exists := v.clients[packet.SenderID]
	var addr *net.UDPAddr
	if exists && client != nil {
		client.LastSeen = time.Now()
		addr = client.Addr
	}
	v.Unlock()
	if !exists || addr == nil {
		v.logger.Warn("Test frequency from unknown client", "sender_id", packet.SenderID)
		return
	}

	if v.conn == nil {
		v.logger.Warn("No UDP connection available to echo test packet")
		return
	}
	_, err := v.conn.WriteToUDP(packet.SerializePacket(), addr)
	if err != nil {
		v.logger.Error("Failed to echo test frequency packet to client", "to", addr.String(), "error", err)
		return
	}
	v.logger.Debug("Echoed test frequency packet to client", "to", addr.String(), "sender_id", packet.SenderID)
}

func (v *Server) handleGoodbyePacket(packet *VCSPacket, addr *net.UDPAddr) {
	if !v.isBoundAddr(packet.SenderID, addr) {
		v.logger.Debug("Ignoring bye from an unbound address",
			"sender_id", packet.SenderID, "addr", addr.String())
		return
	}
	v.DisconnectClient(packet.SenderID)
}

func (v *Server) broadcastVoice(packet *VCSPacket, senderID uuid.UUID) {
	for _, client := range v.GetListeningClients(packet, senderID) { // Already a lot of logic is done in GetListeningClients
		go func(addr *net.UDPAddr) {
			_, err := v.conn.WriteToUDP(packet.SerializePacket(), addr)
			v.logger.Debug("Sent packet to client", "sender_id", packet.SenderID, "receiver_addr", addr.String())
			if err != nil {
				v.logger.Error("Failed to send voice packet",
					"to", addr.String(),
					"error", err)
			}
		}(client.Addr)
	}
}

func (v *Server) cleanupRoutine() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-v.stopChan:
			return
		case <-ticker.C:
			v.cleanup()
		}
	}
}

func (v *Server) cleanup() {
	threshold := time.Now().Add(-1 * time.Minute)

	v.Lock()
	defer v.Unlock()

	for id, client := range v.clients {
		if client.LastSeen.Before(threshold) {
			delete(v.clients, id)
			v.logger.Info("Removed inactive voice client",
				"id", id,
				"addr", client.Addr.String())
		}
	}
}

func (v *Server) Stop() error {
	v.Lock()
	defer v.Unlock()

	if !v.running {
		return nil
	}

	v.stopOnce.Do(func() { close(v.stopChan) })

	if v.conn != nil {
		err := v.conn.Close()
		if err != nil {
			return err
		}
	}

	v.running = false
	v.logger.Info("Voice server stopped")
	return nil
}

func (v *Server) GetConnectedClients() []uuid.UUID {
	v.RLock()
	defer v.RUnlock()

	clients := make([]uuid.UUID, 0, len(v.clients))
	for id := range v.clients {
		clients = append(clients, id)
	}
	return clients
}

func (v *Server) DisconnectClient(clientID uuid.UUID) {
	v.Lock()
	client, exists := v.clients[clientID]
	if exists {
		delete(v.clients, clientID)
	}
	v.Unlock()
	if exists {
		v.logger.Info("Disconnected voice client",
			"id", clientID,
			"addr", client.Addr.String())
		if v.controlClient != nil {
			go v.controlClient.ReportClientDisconnected(clientID)
		}
	}
}

func (v *Server) isRunning() bool {
	v.RLock()
	defer v.RUnlock()
	return v.running
}

func (v *Server) GetClientCount() int {
	v.RLock()
	defer v.RUnlock()
	return len(v.clients)
}

func (v *Server) GetListeningClients(packet *VCSPacket, senderId uuid.UUID) []*Client {
	var listeningClients []*Client
	for _, client := range v.serverState.GetAllClients() {
		if client.ID == senderId {
			continue // Skip the sender
		}
		if v.serverState.IsListeningOnFrequency(client.ID, senderId, packet.FrequencyAsFloat32(), v.settingsState.IsFrequencyGlobal(packet.FrequencyAsFloat32())) {
			v.RLock()
			clientData, exists := v.clients[client.ID]
			v.RUnlock()
			if exists {
				listeningClients = append(listeningClients, clientData)
			}
		}
	}
	return listeningClients
}

func (v *Server) GetClientIPFromId(clientId uuid.UUID) (net.IP, bool) {
	v.RLock()
	defer v.RUnlock()
	if client, exists := v.clients[clientId]; exists {
		return client.Addr.IP, true
	}
	return nil, false
}

// GetClientLatencyMap returns a snapshot of measured voice RTT per connected client.
func (v *Server) GetClientLatencyMap() map[uuid.UUID]int64 {
	v.RLock()
	defer v.RUnlock()
	result := make(map[uuid.UUID]int64, len(v.clients))
	for id, client := range v.clients {
		result[id] = client.LatencyToVoiceMs
	}
	return result
}
