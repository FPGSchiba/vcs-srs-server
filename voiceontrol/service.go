package voiceontrol

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/FPGSchiba/vcs-srs-server/events"
	"github.com/FPGSchiba/vcs-srs-server/state"
	pb "github.com/FPGSchiba/vcs-srs-server/voicecontrolpb"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

type registeredNode struct {
	serverID   string
	address    string // public "host:port" for UDP
	isGlobal   bool
	region     string
	coalitions []string
	stream     pb.VoiceControlService_EstablishStreamServer
	sendMu     sync.Mutex
}

type VoiceControlServer struct {
	pb.UnimplementedVoiceControlServiceServer
	logger         *slog.Logger
	mu             sync.RWMutex
	serverState    *state.ServerState
	settingsState  *state.SettingsState
	eventBus       *events.EventBus
	nodes          map[string]*registeredNode
	globalNodeAddr string
}

func NewVoiceControlServer(serverState *state.ServerState, settingsState *state.SettingsState, eventBus *events.EventBus, logger *slog.Logger) *VoiceControlServer {
	return &VoiceControlServer{
		serverState:   serverState,
		settingsState: settingsState,
		eventBus:      eventBus,
		logger:        logger,
		nodes:         make(map[string]*registeredNode),
	}
}

func (s *VoiceControlServer) GetServerState() healthpb.HealthCheckResponse_ServingStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.serverState == nil {
		return healthpb.HealthCheckResponse_SERVICE_UNKNOWN
	}
	return healthpb.HealthCheckResponse_SERVING
}

// GetVoiceAddressForCoalition returns the UDP address of the node owning the
// coalition and the address of the global voice node.
func (s *VoiceControlServer) GetVoiceAddressForCoalition(coalition string) (coalitionAddr, globalAddr string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, node := range s.nodes {
		if !node.isGlobal {
			for _, c := range node.coalitions {
				if c == coalition {
					coalitionAddr = node.address
					break
				}
			}
		}
	}
	globalAddr = s.globalNodeAddr
	return
}

func (s *VoiceControlServer) RegisterVoiceServer(ctx context.Context, req *pb.RegisterVoiceServerRequest) (*pb.RegisterVoiceServerResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	udpAddr := fmt.Sprintf("%s:%d", req.ServerAddress, req.UdpPort)
	node := &registeredNode{
		serverID: req.ServerId,
		address:  udpAddr,
		isGlobal: req.IsGlobal,
		region:   req.Region,
	}

	var assignedCoalitions []string
	if req.IsGlobal {
		s.globalNodeAddr = udpAddr
	} else {
		assignedCoalitions = s.assignCoalitions(req.PreferredCoalitions)
		node.coalitions = assignedCoalitions
	}
	s.nodes[req.ServerId] = node

	s.logger.Info("Voice server registered",
		"serverId", req.ServerId,
		"address", udpAddr,
		"isGlobal", req.IsGlobal,
		"coalitions", assignedCoalitions,
	)
	return &pb.RegisterVoiceServerResponse{
		Success:            true,
		Message:            "registered",
		AssignedCoalitions: assignedCoalitions,
		GlobalVoiceAddress: s.globalNodeAddr,
	}, nil
}

// assignCoalitions must be called with s.mu write-locked.
func (s *VoiceControlServer) assignCoalitions(preferred []string) []string {
	s.settingsState.RLock()
	all := make([]string, len(s.settingsState.Coalitions))
	for i, c := range s.settingsState.Coalitions {
		all[i] = c.Name
	}
	s.settingsState.RUnlock()

	taken := make(map[string]bool)
	for _, node := range s.nodes {
		if !node.isGlobal {
			for _, c := range node.coalitions {
				taken[c] = true
			}
		}
	}
	for _, p := range preferred {
		if !taken[p] {
			return []string{p}
		}
	}
	for _, c := range all {
		if !taken[c] {
			return []string{c}
		}
	}
	return []string{}
}

func (s *VoiceControlServer) EstablishStream(stream pb.VoiceControlService_EstablishStreamServer) error {
	// Voice node sends an initial ControlResponse with its server_id to identify itself.
	initMsg, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("failed to receive stream identification: %w", err)
	}
	serverID := initMsg.ServerId

	s.mu.Lock()
	node, exists := s.nodes[serverID]
	if !exists {
		s.mu.Unlock()
		return fmt.Errorf("voice node not registered: %s", serverID)
	}
	node.stream = stream
	s.mu.Unlock()

	s.logger.Info("Stream established with voice node", "serverId", serverID)

	snapshot := s.buildStateSnapshot(node)
	if err := stream.Send(snapshot); err != nil {
		return fmt.Errorf("failed to send state snapshot to %s: %w", serverID, err)
	}

	ch := s.eventBus.Subscribe("*")
	defer s.eventBus.Unsubscribe(ch)

	for {
		select {
		case <-stream.Context().Done():
			s.deregisterNode(serverID)
			return nil
		case event, ok := <-ch:
			if !ok {
				return nil
			}
			msg := s.buildClientDelta(node, event)
			if msg == nil {
				continue
			}
			node.sendMu.Lock()
			sendErr := stream.Send(msg)
			node.sendMu.Unlock()
			if sendErr != nil {
				s.deregisterNode(serverID)
				return sendErr
			}
		}
	}
}

func (s *VoiceControlServer) buildStateSnapshot(node *registeredNode) *pb.ControlMessage {
	s.serverState.RLock()
	defer s.serverState.RUnlock()

	clients := make(map[string]*pb.VoiceClientInfo)
	radios := make(map[string]*pb.VoiceRadioInfo)

	for id, client := range s.serverState.Clients {
		if node.isGlobal || containsStr(node.coalitions, client.Coalition) {
			clients[id.String()] = &pb.VoiceClientInfo{
				Name:      client.Name,
				Coalition: client.Coalition,
				UnitId:    client.UnitId,
				Role:      uint32(client.Role),
			}
		}
	}
	for id, radio := range s.serverState.RadioClients {
		c, ok := s.serverState.Clients[id]
		if !ok {
			continue
		}
		if node.isGlobal || containsStr(node.coalitions, c.Coalition) {
			radios[id.String()] = convertRadioStateToProto(radio)
		}
	}

	return &pb.ControlMessage{
		Command: &pb.ControlMessage_StateSnapshot{
			StateSnapshot: &pb.ClientStateSnapshot{Clients: clients, Radios: radios},
		},
	}
}

func (s *VoiceControlServer) buildClientDelta(node *registeredNode, event events.Event) *pb.ControlMessage {
	switch event.Name {
	case events.ClientsChanged:
		ce, ok := event.Data.(events.ClientChangeEvent)
		if !ok {
			return nil
		}
		guid := ce.ClientID.String()

		if ce.Type == events.ClientLeft {
			return &pb.ControlMessage{
				Command: &pb.ControlMessage_ClientDelta{
					ClientDelta: &pb.ClientDelta{Type: pb.ClientDelta_LEFT, ClientId: guid},
				},
			}
		}

		client, exists := ce.Clients[ce.ClientID]
		if !exists {
			return nil
		}
		if !node.isGlobal && !containsStr(node.coalitions, client.Coalition) {
			return nil
		}

		info := &pb.VoiceClientInfo{
			Name:      client.Name,
			Coalition: client.Coalition,
			UnitId:    client.UnitId,
			Role:      uint32(client.Role),
		}
		deltaType := pb.ClientDelta_INFO_UPDATED
		if ce.Type == events.ClientJoined {
			deltaType = pb.ClientDelta_JOINED
		}
		return &pb.ControlMessage{
			Command: &pb.ControlMessage_ClientDelta{
				ClientDelta: &pb.ClientDelta{Type: deltaType, ClientId: guid, ClientInfo: info},
			},
		}

	case events.RadioClientsChanged:
		re, ok := event.Data.(events.RadioChangeEvent)
		if !ok {
			return nil
		}
		radio, exists := re.Radios[re.ClientID]
		if !exists {
			return nil
		}
		s.serverState.RLock()
		client, clientExists := s.serverState.Clients[re.ClientID]
		s.serverState.RUnlock()
		if !clientExists || (!node.isGlobal && !containsStr(node.coalitions, client.Coalition)) {
			return nil
		}
		return &pb.ControlMessage{
			Command: &pb.ControlMessage_ClientDelta{
				ClientDelta: &pb.ClientDelta{
					Type:      pb.ClientDelta_RADIO_UPDATED,
					ClientId:  re.ClientID.String(),
					RadioInfo: convertRadioStateToProto(radio),
				},
			},
		}

	case events.ServerAction:
		ae, ok := event.Data.(events.ServerActionEvent)
		if !ok {
			return nil
		}
		var deltaType pb.ClientDelta_Type
		switch ae.ActionType {
		case events.ActionMute:
			deltaType = pb.ClientDelta_MUTED
		case events.ActionUnmute:
			deltaType = pb.ClientDelta_UNMUTED
		default:
			return nil
		}
		return &pb.ControlMessage{
			Command: &pb.ControlMessage_ClientDelta{
				ClientDelta: &pb.ClientDelta{Type: deltaType, ClientId: ae.TargetClientID.String()},
			},
		}
	}
	return nil
}

func (s *VoiceControlServer) deregisterNode(serverID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	node, exists := s.nodes[serverID]
	if !exists {
		return
	}
	if node.isGlobal {
		s.globalNodeAddr = ""
	}
	delete(s.nodes, serverID)
	s.logger.Info("Voice node deregistered", "serverId", serverID)
}

func containsStr(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

func convertRadioStateToProto(r *state.RadioState) *pb.VoiceRadioInfo {
	radios := make([]*pb.VoiceRadio, len(r.Radios))
	for i, radio := range r.Radios {
		radios[i] = &pb.VoiceRadio{
			Id:         radio.ID,
			Frequency:  radio.Frequency,
			Enabled:    radio.Enabled,
			IsIntercom: radio.IsIntercom,
		}
	}
	return &pb.VoiceRadioInfo{Radios: radios, Muted: r.Muted}
}
