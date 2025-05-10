package voice

import (
	_ "encoding/binary"
	"net"
	"sync"
	"time"
	"vcs-server/state"

	"go.uber.org/zap"
)

const (
	MaxPacketSize = 1024 // Adjust based on your voice packet size
	BufferSize    = 4096 // UDP buffer size
)

type Client struct {
	ID       string
	Addr     *net.UDPAddr
	LastSeen time.Time
	Room     string // Optional: for room-based communication
}

type Server struct {
	sync.RWMutex
	conn      *net.UDPConn
	clients   map[string]*Client
	state     *state.ServerState
	logger    *zap.Logger
	isRunning bool
	stopChan  chan struct{}
}

func NewServer(state *state.ServerState, logger *zap.Logger) *Server {
	return &Server{
		clients:  make(map[string]*Client),
		state:    state,
		logger:   logger,
		stopChan: make(chan struct{}),
	}
}

func (v *Server) Listen(address string, stopChan chan struct{}) error {
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
	v.isRunning = true
	v.Unlock()

	v.logger.Info("Voice server started", zap.String("address", address))

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
			v.conn.SetReadDeadline(time.Now().Add(1 * time.Second))
			n, remoteAddr, err := v.conn.ReadFromUDP(buffer)

			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}
				v.logger.Error("Error reading UDP", zap.Error(err))
				continue
			}

			// Handle the received packet
			go v.handlePacket(buffer[:n], remoteAddr)
		}
	}
}

type VoicePacket struct {
	ClientID  string
	Timestamp int64
	VoiceData []byte
	// Add other necessary fields (e.g., sequence number, room ID, etc.)
}

func (v *Server) handlePacket(data []byte, addr *net.UDPAddr) {
	if len(data) < 12 { // Minimum packet size (adjust based on your protocol)
		v.logger.Warn("Received malformed packet", zap.String("addr", addr.String()))
		return
	}

	// Extract client ID from packet (implement based on your protocol)
	clientID := extractClientID(data) // You'll need to implement this

	v.Lock()
	client, exists := v.clients[clientID]
	if !exists {
		// New client
		client = &Client{
			ID:       clientID,
			Addr:     addr,
			LastSeen: time.Now(),
		}
		v.clients[clientID] = client
		v.logger.Info("New voice client connected",
			zap.String("id", clientID),
			zap.String("addr", addr.String()))
	}
	client.LastSeen = time.Now()
	v.Unlock()

	/* Check if client is banned or muted
	v.state.RLock()
	if v.state.Banned[clientID] {
		v.state.RUnlock()
		return
	}
	isMuted := v.state.Muted[clientID]
	v.state.RUnlock()


	if isMuted {
		// Optionally send mute notification to client
		return
	}
	*/

	// Broadcast to other clients
	v.broadcastVoice(data, clientID)
}

func (v *Server) broadcastVoice(data []byte, senderID string) {
	v.RLock()
	defer v.RUnlock()

	for id, client := range v.clients {
		if id == senderID {
			continue // Skip sender
		}

		// You might want to add room-based filtering here
		go func(addr *net.UDPAddr) {
			_, err := v.conn.WriteToUDP(data, addr)
			if err != nil {
				v.logger.Error("Failed to send voice packet",
					zap.String("to", addr.String()),
					zap.Error(err))
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
				zap.String("id", id),
				zap.String("addr", client.Addr.String()))
		}
	}
}

func (v *Server) Stop() error {
	v.Lock()
	defer v.Unlock()

	if !v.isRunning {
		return nil
	}

	close(v.stopChan)

	if v.conn != nil {
		err := v.conn.Close()
		if err != nil {
			return err
		}
	}

	v.isRunning = false
	v.logger.Info("Voice server stopped")
	return nil
}

// Helper methods for client management
func (v *Server) GetConnectedClients() []string {
	v.RLock()
	defer v.RUnlock()

	clients := make([]string, 0, len(v.clients))
	for id := range v.clients {
		clients = append(clients, id)
	}
	return clients
}

func (v *Server) DisconnectClient(clientID string) {
	v.Lock()
	defer v.Unlock()

	if client, exists := v.clients[clientID]; exists {
		// Optionally send disconnect message to client
		delete(v.clients, clientID)
		v.logger.Info("Disconnected voice client",
			zap.String("id", clientID),
			zap.String("addr", client.Addr.String()))
	}
}

// Additional helper methods you might need

func (v *Server) IsRunning() bool {
	v.RLock()
	defer v.RUnlock()
	return v.isRunning
}

func (v *Server) GetClientCount() int {
	v.RLock()
	defer v.RUnlock()
	return len(v.clients)
}

// You'll need to implement this based on your protocol
func extractClientID(data []byte) string {
	// Example implementation - adjust based on your packet format
	// This assumes the first 8 bytes are the client ID
	return string(data[:8])
}
