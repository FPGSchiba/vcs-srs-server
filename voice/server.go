package voice

import (
	_ "encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/FPGSchiba/vcs-srs-server/voiceontrol"
	"github.com/faiface/beep"
	"github.com/faiface/beep/speaker"
	"github.com/google/uuid"
	"github.com/pion/opus"
)

const (
	BufferSize       = 1024                  // UDP buffer size
	JitterBufferSize = 10                    // Number of packets to buffer
	PlayoutDelay     = 60 * time.Millisecond // Initial playout delay
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

	// Playback/decoder state
	playOnce   sync.Once
	opusDec    opus.Decoder
	pipeW      *io.PipeWriter
	playFormat beep.Format
	playErr    error

	// Optional: gate concurrent writes to the pipe if PlayVoiceData is called from multiple goroutines
	playMu sync.Mutex
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
		v.controlClient = voiceontrol.NewVoiceControlClient(v.serverId, v.logger)
		// TODO: Make Server domain / IP configurable
		if err := v.controlClient.ConnectControlServer("localhost"); err != nil {
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
	v.broadcastVoice(packet, packet.SenderID)

	// Optionally, you can log the received voice packet
	v.logger.Debug("Received voice packet",
		"sender_id", packet.SenderID,
		"frequency", packet.FrequencyAsFloat32(),
		"size", len(packet.Payload))
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
	if v.settingsState.IsFrequencyTest(packet.FrequencyAsFloat32()) {
		v.serverState.RLock()
		defer v.serverState.RUnlock()
		if _, exists := v.clients[packet.SenderID]; exists {
			// Instead of echoing, play the voice data locally on the server
			go v.PlayVoiceData(packet.Payload)
			return []*Client{} // Do not send to any clients
		}
		v.logger.Warn("Received test frequency packet from unknown client", "sender_id", packet.SenderID)
		return []*Client{} // No clients to return if sender is unknown
	}

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

// Simple, direct PlayVoiceData without dejitter buffer - for clean local test playback
func (v *Server) PlayVoiceData(payload []byte) {
	if len(payload) == 0 {
		return
	}
	toc := payload[0]
	config := toc >> 3
	if config < 16 {
		// Not CELT-only (likely SILK/hybrid). Drop to avoid Pion error.
		v.logger.Warn("Dropping non-CELT Opus packet", "config", config, "len_payload", len(payload))
		return
	}

	// Lazy-initialize decoder and speaker once
	v.playOnce.Do(func() {
		var err error
		v.opusDec = opus.NewDecoder() // mono 48kHz

		v.playFormat = beep.Format{
			SampleRate:  beep.SampleRate(48000),
			NumChannels: 1,
			Precision:   2,
		}

		// Larger buffer for smoother playback
		err = speaker.Init(v.playFormat.SampleRate, v.playFormat.SampleRate.N(300*time.Millisecond))
		if err != nil {
			v.playErr = err
			v.logger.Error("Failed to init Speaker", "error", err)
			return
		}

		pr, pw := io.Pipe()
		v.pipeW = pw

		stream := &pcmStream{
			r:   pr,
			f:   v.playFormat,
			buf: make([]byte, 8192*v.playFormat.Width()),
		}
		speaker.Play(stream)
	})

	if v.playErr != nil || v.pipeW == nil {
		v.logger.Error("Audio playback not initialized", "error", v.playErr)
		return
	}

	// Decode directly - buffer for up to 60ms stereo @ 48kHz
	out := make([]byte, 11520)

	// Serialize opus decoder access and pipe writes; opus.Decoder is not goroutine-safe
	v.playMu.Lock()
	// Protect against library panics on corrupt/non-Opus payloads
	var (
		bw       opus.Bandwidth
		isStereo bool
		err      error
	)
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("opus decode panic: %v", r)
			}
		}()
		bw, isStereo, err = v.opusDec.Decode(payload, out)
	}()
	if err != nil {
		v.playMu.Unlock()
		v.logger.Error("Failed to decode Opus data", "error", err)
		return
	}

	// Get sample rate and assume 20ms frame
	sr := bw.SampleRate()
	if sr == 0 {
		sr = 48000
	}
	samplesPerCh := sr / 50 // 20ms
	if samplesPerCh <= 0 {
		samplesPerCh = 960
	}

	// Calculate actual bytes to use
	channels := 1
	if isStereo {
		channels = 2
	}
	bytesToUse := samplesPerCh * 2 * channels
	if bytesToUse > len(out) {
		bytesToUse = len(out)
	}

	var pcmData []byte
	if isStereo {
		// Downmix to mono
		pcmData = downmixStereoS16LEToMono(out[:bytesToUse], samplesPerCh)
	} else {
		pcmData = out[:bytesToUse]
	}

	// Write directly to pipe while still holding the lock (serialize with decoder)
	_, err = v.pipeW.Write(pcmData)
	v.playMu.Unlock()

	if err != nil {
		v.logger.Error("Failed to write PCM to speaker pipe", "error", err)
	}
}

// downmixStereoS16LEToMono averages L/R channels into mono S16LE.
func downmixStereoS16LEToMono(in []byte, samplesPerCh int) []byte {
	out := make([]byte, samplesPerCh*2)
	for i := 0; i < samplesPerCh; i++ {
		li := 4 * i
		ri := li + 2
		l := int16(uint16(in[li]) | uint16(in[li+1])<<8)
		r := int16(uint16(in[ri]) | uint16(in[ri+1])<<8)
		m := int16((int32(l) + int32(r)) / 2)
		oi := 2 * i
		out[oi] = byte(uint16(m))
		out[oi+1] = byte(uint16(m) >> 8)
	}
	return out
}

// pcmStream allows faiface to play raw S16LE PCM directly.
// This is adapted from the example you pasted.
type pcmStream struct {
	r   io.Reader
	f   beep.Format
	buf []byte
	len int
	pos int
	err error
}

func (s *pcmStream) Err() error { return s.err }

func (s *pcmStream) Stream(samples [][2]float64) (n int, ok bool) {
	width := s.f.Width()

	// If there's not enough data for a full sample, get more
	if size := s.len - s.pos; size < width {
		// If there's a partial sample, move it to the beginning of the buffer
		if size != 0 {
			copy(s.buf, s.buf[s.pos:s.len])
		}
		s.len = size
		s.pos = 0

		// Refill the buffer
		nbytes, err := s.r.Read(s.buf[s.len:])
		if err != nil {
			if err != io.EOF {
				s.err = err
			}
			return n, false
		}
		s.len += nbytes
	}

	// Decode as many samples as we can
	for n < len(samples) && s.len-s.pos >= width {
		samples[n], _ = s.f.DecodeSigned(s.buf[s.pos:])
		n++
		s.pos += width
	}
	return n, true
}
