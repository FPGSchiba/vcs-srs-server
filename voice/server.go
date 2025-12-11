package voice

import (
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

type Client struct {
	Addr     *net.UDPAddr
	LastSeen time.Time
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
	controlClient     *voiceontrol.VoiceControlClient
	serverId          string
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
		v.controlClient = voiceontrol.NewVoiceControlClient(v.serverId, v.settingsState, v.logger)
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
				if bc.IPAddress == conn.RemoteAddr().String() {
					return true
				}
				return false
			})
			v.serverState.RUnlock()
			if banned {
				v.logger.Warn("Banned client attempted to initialize", "IP", conn.RemoteAddr().String())
				continue
			}

			// Handle the received packet
			go v.handlePacket(buffer[:n], remoteAddr)
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
		v.handleVoicePacket(packet)
	case PacketTypeBye:
		v.handleGoodbyePacket(packet)
	case PacketTypeKeepalive:
		v.handleKeepalivePacket(packet, addr)
	default:
		v.logger.Warn("Unknown packet type received", "type", packet.Type)
	}
}

func (v *Server) handleHelloPacket(packet *VCSPacket, addr *net.UDPAddr) {
	v.logger.Info("Received hello packet", "sender_id", packet.SenderID, "addr", addr.String())
	if !v.serverState.DoesClientExist(packet.SenderID) {
		v.logger.Warn("Client with hello, that does not exist", "sender_id", packet.SenderID)
		// Ignore hello from unknown client
		return
	}

	v.Lock()
	v.clients[packet.SenderID] = &Client{
		Addr:     addr,
		LastSeen: time.Now(),
	}
	v.Unlock()
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
	v.RLock()
	client, exists := v.clients[packet.SenderID]
	v.RUnlock()
	if !exists {
		v.logger.Warn("Received keepalive from unknown client", "sender_id", packet.SenderID)
		return
	}
	v.Lock()
	client.LastSeen = time.Now()
	v.Unlock()
	v.logger.Debug("Updated last seen for client", "sender_id", packet.SenderID, "addr", addr.String())
	ackPacket := NewVCSKeepalivePacket(packet.SenderID)
	ackData := ackPacket.SerializePacket()
	_, err := v.conn.WriteToUDP(ackData, addr)
	if err != nil {
		v.logger.Error("Failed to send keepalive acknowledgment",
			"to", addr.String(),
			"error", err)
		return
	}
}

func (v *Server) handleVoicePacket(packet *VCSPacket) {
	// If this is a test frequency, handle it separately and don't broadcast
	if v.settingsState.IsFrequencyTest(packet.FrequencyAsFloat32()) {
		v.handleTestFrequencyPacket(packet)
		return
	}

	v.RLock()
	client, exists := v.clients[packet.SenderID]
	v.RUnlock()
	if !exists {
		v.logger.Warn("Received voice packet from unknown client", "sender_id", packet.SenderID)
		return
	}

	// Update last seen time
	v.Lock()
	client.LastSeen = time.Now()
	v.Unlock()

	// Broadcast the voice data to other clients
	if len(packet.Payload) > 5 {
		// Ignore very small packets (could be keepalive or empty)
		v.broadcastVoice(packet, packet.SenderID)
	}

	// Optionally, you can log the received voice packet
	v.logger.Debug("Received voice packet",
		"sender_id", packet.SenderID,
		"frequency", packet.FrequencyAsFloat32(),
		"size", len(packet.Payload))
}

// handleTestFrequencyPacket echoes the received packet back to the sender so the client hears it.
func (v *Server) handleTestFrequencyPacket(packet *VCSPacket) {
	v.RLock()
	client, exists := v.clients[packet.SenderID]
	v.RUnlock()
	if !exists || client == nil {
		v.logger.Warn("Test frequency from unknown client", "sender_id", packet.SenderID)
		return
	}

	if v.conn == nil {
		v.logger.Warn("No UDP connection available to echo test packet")
		return
	}

	_, err := v.conn.WriteToUDP(packet.SerializePacket(), client.Addr)
	if err != nil {
		v.logger.Error("Failed to echo test frequency packet to client", "to", client.Addr.String(), "error", err)
		return
	}
	v.logger.Debug("Echoed test frequency packet to client", "to", client.Addr.String(), "sender_id", packet.SenderID)
}

func (v *Server) handleGoodbyePacket(packet *VCSPacket) {
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

	close(v.stopChan)

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
	v.RLock()
	if client, exists := v.clients[clientID]; exists {
		v.RUnlock()
		v.Lock()
		delete(v.clients, clientID)
		v.Unlock()
		v.logger.Info("Disconnected voice client",
			"id", clientID,
			"addr", client.Addr.String())
		return
	}
	v.RUnlock()
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
