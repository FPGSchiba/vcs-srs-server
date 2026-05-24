package voiceontrol

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/state"
	pb "github.com/FPGSchiba/vcs-srs-server/voicecontrolpb"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"
)

type VoiceControlClient struct {
	client             pb.VoiceControlServiceClient
	conn               *grpc.ClientConn
	serverId           string
	isGlobal           bool
	assignedCoalitions []string
	logger             *slog.Logger
	stream             grpc.BidiStreamingClient[pb.ControlResponse, pb.ControlMessage]
	stopc              chan struct{}
	closeOnce          sync.Once
	cancelMonitor      context.CancelFunc
	connectionFailed   bool
	settingsState      *state.SettingsState
	serverState        *state.ServerState
}

func NewVoiceControlClient(serverId string, settingsState *state.SettingsState, serverState *state.ServerState, isGlobal bool, logger *slog.Logger) *VoiceControlClient {
	return &VoiceControlClient{
		serverId:      serverId,
		isGlobal:      isGlobal,
		logger:        logger,
		settingsState: settingsState,
		serverState:   serverState,
		stopc:         make(chan struct{}),
	}
}

func (v *VoiceControlClient) ConnectControlServer() error {
	v.stopc = make(chan struct{})

	v.settingsState.RLock()
	address := fmt.Sprintf("%s:%d", v.settingsState.VoiceControl.RemoteHost, v.settingsState.VoiceControl.Port)
	certFileName := v.settingsState.VoiceControl.CertificateFile
	serverName := v.settingsState.VoiceControl.ServerName
	v.settingsState.RUnlock()

	v.logger.Info("Connecting to Control node", "address", address)

	cert, err := LoadCertificateOnly(certFileName)
	if err != nil {
		return err
	}
	clientTLSConfig, err := CreateClientTLSConfig(cert, serverName)
	if err != nil {
		return err
	}

	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(credentials.NewTLS(clientTLSConfig)),
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff: backoff.Config{
				BaseDelay:  1 * time.Second,
				Multiplier: 1.6,
				MaxDelay:   10 * time.Second,
				Jitter:     0.2,
			},
			MinConnectTimeout: 5 * time.Second,
		}),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                15 * time.Second,
			Timeout:             10 * time.Second,
			PermitWithoutStream: true,
		}),
	}
	conn, err := grpc.NewClient(address, opts...)
	if err != nil {
		return err
	}

	v.conn = conn
	v.client = pb.NewVoiceControlServiceClient(v.conn)
	if err := v.establishConnection(); err != nil {
		v.logger.Error("Failed to establish connection to Control Server", "error", err)
		if v.conn != nil {
			v.conn.Close()
		}
	}
	return nil
}

func (v *VoiceControlClient) establishConnection() error {
	if v.cancelMonitor != nil {
		v.cancelMonitor()
	}
	ctx, cancel := context.WithCancel(context.Background())
	v.cancelMonitor = cancel

	go func() {
		lastState := v.conn.GetState()
		for {
			if !v.conn.WaitForStateChange(ctx, lastState) {
				return
			}
			newState := v.conn.GetState()
			if newState == connectivity.Idle {
				v.logger.Warn("VoiceControl connection idle, attempting reconnection")
				go v.handleReconnection()
			}
			lastState = newState
		}
	}()

	if err := v.registerSelf(); err != nil {
		return err
	}
	return v.establishStream()
}

func (v *VoiceControlClient) registerSelf() error {
	v.settingsState.RLock()
	publicAddr := v.settingsState.VoiceControl.PublicAddr
	region := v.settingsState.VoiceControl.Region
	v.settingsState.RUnlock()

	host := publicAddr
	if host == "" {
		host = "0.0.0.0"
	}

	resp, err := v.client.RegisterVoiceServer(context.Background(), &pb.RegisterVoiceServerRequest{
		ServerId:      v.serverId,
		Capabilities:  &pb.ServerCapabilities{Version: "0.1.0"},
		ServerAddress: host,
		UdpPort:       5002,
		IsGlobal:      v.isGlobal,
		Region:        region,
	})
	if err != nil {
		return fmt.Errorf("failed to register voice server: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("registration rejected: %s", resp.Message)
	}
	v.assignedCoalitions = resp.AssignedCoalitions
	v.logger.Info("Registered with Control Server",
		"coalitions", v.assignedCoalitions,
		"globalAddr", resp.GlobalVoiceAddress,
	)
	return nil
}

func (v *VoiceControlClient) establishStream() error {
	stream, err := v.client.EstablishStream(context.Background())
	if err != nil {
		st, ok := status.FromError(err)
		if ok && (st.Code() == codes.Unavailable || st.Code() == codes.DeadlineExceeded) {
			go v.handleReconnection()
			return fmt.Errorf("temporary connection issue: %w", err)
		}
		return fmt.Errorf("failed to establish stream: %w", err)
	}

	// Identify ourselves so the server can find our registration record.
	if err := stream.Send(&pb.ControlResponse{
		ServerId: v.serverId,
		EventId:  "init",
		Success:  true,
		Message:  "stream established",
	}); err != nil {
		return fmt.Errorf("failed to send stream identification: %w", err)
	}

	v.stream = stream
	go v.receiveMessages()

	v.connectionFailed = false
	v.logger.Info("Stream established with Control Server")
	return nil
}

func (v *VoiceControlClient) receiveMessages() {
	for {
		select {
		case <-v.stopc:
			return
		default:
			msg, err := v.stream.Recv()
			if err == io.EOF {
				return
			}
			if err != nil {
				go v.handleReconnection()
				return
			}
			v.applyControlMessage(msg)
		}
	}
}

func (v *VoiceControlClient) applyControlMessage(msg *pb.ControlMessage) {
	switch cmd := msg.Command.(type) {
	case *pb.ControlMessage_StateSnapshot:
		v.applySnapshot(cmd.StateSnapshot)
	case *pb.ControlMessage_ClientDelta:
		v.applyDelta(cmd.ClientDelta)
	case *pb.ControlMessage_AssignCoalitions:
		v.assignedCoalitions = cmd.AssignCoalitions.Coalitions
		v.logger.Info("Coalition assignment updated", "coalitions", v.assignedCoalitions)
	case *pb.ControlMessage_AssignFrequencies:
		ranges := cmd.AssignFrequencies.FrequencyRanges
		v.logger.Info("Frequency band assignment received", "ranges", len(ranges))
	}
}

func (v *VoiceControlClient) applySnapshot(snap *pb.ClientStateSnapshot) {
	v.serverState.Lock()
	defer v.serverState.Unlock()

	v.serverState.Clients = make(map[uuid.UUID]*state.ClientState, len(snap.Clients))
	v.serverState.RadioClients = make(map[uuid.UUID]*state.RadioState, len(snap.Radios))

	for idStr, info := range snap.Clients {
		id, err := uuid.Parse(idStr)
		if err != nil {
			v.logger.Warn("invalid client ID in snapshot", "id", idStr)
			continue
		}
		v.serverState.Clients[id] = &state.ClientState{
			Name:      info.Name,
			Coalition: info.Coalition,
			UnitId:    info.UnitId,
			Role:      uint8(info.Role),
		}
	}
	for idStr, radio := range snap.Radios {
		id, err := uuid.Parse(idStr)
		if err != nil {
			v.logger.Warn("invalid radio ID in snapshot", "id", idStr)
			continue
		}
		v.serverState.RadioClients[id] = convertVoiceRadioInfo(radio)
	}
	v.logger.Info("Applied state snapshot", "clients", len(snap.Clients))
}

func (v *VoiceControlClient) applyDelta(delta *pb.ClientDelta) {
	id, err := uuid.Parse(delta.ClientId)
	if err != nil {
		v.logger.Warn("invalid client ID in delta", "id", delta.ClientId)
		return
	}

	v.serverState.Lock()
	defer v.serverState.Unlock()

	switch delta.Type {
	case pb.ClientDelta_JOINED, pb.ClientDelta_INFO_UPDATED:
		if delta.ClientInfo != nil {
			v.serverState.Clients[id] = &state.ClientState{
				Name:      delta.ClientInfo.Name,
				Coalition: delta.ClientInfo.Coalition,
				UnitId:    delta.ClientInfo.UnitId,
				Role:      uint8(delta.ClientInfo.Role),
			}
			if _, exists := v.serverState.RadioClients[id]; !exists {
				v.serverState.RadioClients[id] = &state.RadioState{Radios: []state.Radio{}}
			}
		}
	case pb.ClientDelta_LEFT:
		delete(v.serverState.Clients, id)
		delete(v.serverState.RadioClients, id)
	case pb.ClientDelta_RADIO_UPDATED:
		if delta.RadioInfo != nil {
			newRadio := convertVoiceRadioInfo(delta.RadioInfo)
			// Preserve server-owned mute status across radio updates.
			if existing, exists := v.serverState.RadioClients[id]; exists {
				newRadio.Muted = existing.Muted
			}
			v.serverState.RadioClients[id] = newRadio
		}
	case pb.ClientDelta_MUTED:
		if radio, exists := v.serverState.RadioClients[id]; exists {
			radio.Muted = true
		}
	case pb.ClientDelta_UNMUTED:
		if radio, exists := v.serverState.RadioClients[id]; exists {
			radio.Muted = false
		}
	}
}

func convertVoiceRadioInfo(r *pb.VoiceRadioInfo) *state.RadioState {
	radios := make([]state.Radio, len(r.Radios))
	for i, radio := range r.Radios {
		radios[i] = state.Radio{
			ID:         radio.Id,
			Frequency:  radio.Frequency,
			Enabled:    radio.Enabled,
			IsIntercom: radio.IsIntercom,
		}
	}
	return &state.RadioState{Radios: radios, Muted: r.Muted}
}

func (v *VoiceControlClient) handleReconnection() {
	currentBackoff := 1
	reconnectionAttempts := 0
	maxBackoff := 128
	maxReconnectionAttempts := 20
	v.logger.Warn("Attempting to reconnect to Voice Control Server")

	for {
		select {
		case <-v.stopc:
			return
		default:
			if reconnectionAttempts >= maxReconnectionAttempts {
				v.logger.Error("Max reconnection attempts reached, giving up")
				v.Close()
				return
			}
			if err := v.establishConnection(); err == nil {
				return
			}
			time.Sleep(time.Duration(currentBackoff) * time.Second)
			if currentBackoff < maxBackoff {
				currentBackoff *= 2
			}
			reconnectionAttempts++
		}
	}
}

func (v *VoiceControlClient) ReportClientConnected(clientID uuid.UUID, addr *net.UDPAddr) {
	if v.client == nil {
		return
	}
	_, err := v.client.ReportClientConnected(context.Background(), &pb.ClientConnectedRequest{
		ServerId:      v.serverId,
		ClientId:      clientID.String(),
		ClientAddress: addr.IP.String(),
		ClientPort:    int32(addr.Port),
		ConnectedAt:   time.Now().Unix(),
	})
	if err != nil {
		v.logger.Warn("ReportClientConnected failed", "client", clientID, "error", err)
	}
}

func (v *VoiceControlClient) ReportClientDisconnected(clientID uuid.UUID) {
	if v.client == nil {
		return
	}
	_, err := v.client.ReportClientDisconnected(context.Background(), &pb.ClientDisconnectedRequest{
		ServerId:        v.serverId,
		ClientId:        clientID.String(),
		Reason:          pb.DisconnectReason_CLIENT_DISCONNECT,
		DisconnectedAt:  time.Now().Unix(),
	})
	if err != nil {
		v.logger.Warn("ReportClientDisconnected failed", "client", clientID, "error", err)
	}
}

func (v *VoiceControlClient) Close() error {
	if v.cancelMonitor != nil {
		v.cancelMonitor()
	}
	v.closeOnce.Do(func() { close(v.stopc) })
	if v.conn != nil {
		return v.conn.Close()
	}
	return nil
}
