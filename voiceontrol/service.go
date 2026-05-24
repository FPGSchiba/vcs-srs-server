package voiceontrol

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"

	"github.com/FPGSchiba/vcs-srs-server/events"
	"github.com/FPGSchiba/vcs-srs-server/state"
	pb "github.com/FPGSchiba/vcs-srs-server/voicecontrolpb"
	"github.com/google/uuid"
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

type pendingMove struct {
	fromNodeID string
	toNodeID   string
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
	pendingMoves   map[string]pendingMove  // coalition name → queued move (sticky rebalance)
	clientNodes    map[uuid.UUID]string    // clientID → nodeServerID (from ReportClientConnected)
}

func NewVoiceControlServer(serverState *state.ServerState, settingsState *state.SettingsState, eventBus *events.EventBus, logger *slog.Logger) *VoiceControlServer {
	return &VoiceControlServer{
		serverState:   serverState,
		settingsState: settingsState,
		eventBus:      eventBus,
		logger:        logger,
		nodes:         make(map[string]*registeredNode),
		pendingMoves:  make(map[string]pendingMove),
		clientNodes:   make(map[uuid.UUID]string),
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

	udpAddr := fmt.Sprintf("%s:%d", req.ServerAddress, req.UdpPort)
	node := &registeredNode{
		serverID: req.ServerId,
		address:  udpAddr,
		isGlobal: req.IsGlobal,
		region:   req.Region,
	}

	if req.IsGlobal {
		s.globalNodeAddr = udpAddr
	} else {
		node.coalitions = s.assignCoalitions(req.PreferredCoalitions)
	}
	s.nodes[req.ServerId] = node

	if !req.IsGlobal {
		// Redistribute non-sticky excess from overloaded nodes to this new node.
		s.rebalance(true)
	}

	assignedCoalitions := node.coalitions
	globalAddr := s.globalNodeAddr
	s.mu.Unlock()

	s.logger.Info("Voice server registered",
		"serverId", req.ServerId,
		"address", udpAddr,
		"isGlobal", req.IsGlobal,
		"coalitions", assignedCoalitions,
	)
	s.eventBus.Publish(events.Event{Name: events.VoiceNodeRegistered, Data: req.ServerId})

	return &pb.RegisterVoiceServerResponse{
		Success:            true,
		Message:            "registered",
		AssignedCoalitions: assignedCoalitions,
		GlobalVoiceAddress: globalAddr,
	}, nil
}

// assignCoalitions must be called with s.mu write-locked.
// When preferred is non-empty all non-taken preferred coalitions are assigned.
// When preferred is empty one free coalition is auto-assigned for balanced distribution.
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

	if len(preferred) > 0 {
		var assigned []string
		for _, p := range preferred {
			if !taken[p] {
				assigned = append(assigned, p)
			}
		}
		return assigned
	}

	for _, c := range all {
		if !taken[c] {
			return []string{c}
		}
	}
	return []string{}
}

// rebalance distributes coalitions across registered non-global nodes.
// Pass redistribute=true (new node joined) to also move non-sticky excess from overloaded nodes.
// Must be called with s.mu write-locked.
func (s *VoiceControlServer) rebalance(redistribute bool) {
	var nonGlobal []*registeredNode
	for _, n := range s.nodes {
		if !n.isGlobal {
			nonGlobal = append(nonGlobal, n)
		}
	}
	if len(nonGlobal) == 0 {
		return
	}
	sort.Slice(nonGlobal, func(i, j int) bool { return nonGlobal[i].serverID < nonGlobal[j].serverID })

	s.settingsState.RLock()
	allCoalitions := make([]string, len(s.settingsState.Coalitions))
	for i, c := range s.settingsState.Coalitions {
		allCoalitions[i] = c.Name
	}
	s.settingsState.RUnlock()

	if len(allCoalitions) == 0 {
		return
	}

	target := (len(allCoalitions) + len(nonGlobal) - 1) / len(nonGlobal) // ceil

	// Step 1: assign unowned coalitions to the least-loaded node.
	assigned := make(map[string]bool)
	for _, n := range nonGlobal {
		for _, c := range n.coalitions {
			assigned[c] = true
		}
	}
	for _, c := range allCoalitions {
		if assigned[c] {
			continue
		}
		n := leastLoaded(nonGlobal)
		n.coalitions = append(n.coalitions, c)
		assigned[c] = true
		s.sendCoalitionAssignment(n)
		s.logger.Info("Assigned unowned coalition", "coalition", c, "node", n.serverID)
	}

	if !redistribute {
		return
	}

	// Step 2: move non-sticky excess from overloaded nodes to underloaded nodes.
	counts := s.countClientsPerCoalition()
	for _, over := range nonGlobal {
		for len(over.coalitions) > target {
			moved := false
			for i, c := range over.coalitions {
				under := leastLoaded(nonGlobal)
				if under == over || len(under.coalitions) >= len(over.coalitions)-1 {
					break
				}
				if counts[c] > 0 {
					// Sticky: queue the move for when the coalition empties.
					if _, pending := s.pendingMoves[c]; !pending {
						s.pendingMoves[c] = pendingMove{fromNodeID: over.serverID, toNodeID: under.serverID}
						s.logger.Info("Coalition sticky, queued for rebalance", "coalition", c, "from", over.serverID, "to", under.serverID)
					}
					continue
				}
				// Non-sticky: move immediately.
				over.coalitions = removeAt(over.coalitions, i)
				under.coalitions = append(under.coalitions, c)
				s.sendCoalitionAssignment(over)
				s.sendCoalitionAssignment(under)
				s.eventBus.Publish(events.Event{
					Name: events.CoalitionReassigned,
					Data: events.CoalitionReassignedEvent{
						Coalition:  c,
						OldNodeID:  over.serverID,
						NewNodeID:  under.serverID,
						NewAddr:    under.address,
						GlobalAddr: s.globalNodeAddr,
					},
				})
				s.logger.Info("Moved coalition", "coalition", c, "from", over.serverID, "to", under.serverID)
				moved = true
				break
			}
			if !moved {
				break // remaining excess is all sticky; stop to avoid infinite loop
			}
		}
	}

	// Warn if any node ended up with no coalitions; assign global frequencies as fallback.
	for _, n := range nonGlobal {
		if len(n.coalitions) == 0 {
			s.logger.Warn("Voice node has no coalitions after rebalance, assigning global frequencies", "node", n.serverID)
			s.sendGlobalFrequencies(n)
		}
	}
}

// tryFlushPendingMoves executes queued sticky moves whose coalition has since emptied.
// Must be called with s.mu write-locked.
func (s *VoiceControlServer) tryFlushPendingMoves() {
	if len(s.pendingMoves) == 0 {
		return
	}
	counts := s.countClientsPerCoalition()
	for coalition, move := range s.pendingMoves {
		if counts[coalition] > 0 {
			continue // still sticky
		}
		from, fromOK := s.nodes[move.fromNodeID]
		to, toOK := s.nodes[move.toNodeID]
		if !fromOK || !toOK {
			delete(s.pendingMoves, coalition)
			continue
		}
		idx := indexStr(from.coalitions, coalition)
		if idx < 0 {
			delete(s.pendingMoves, coalition)
			continue
		}
		from.coalitions = removeAt(from.coalitions, idx)
		to.coalitions = append(to.coalitions, coalition)
		s.sendCoalitionAssignment(from)
		s.sendCoalitionAssignment(to)
		s.eventBus.Publish(events.Event{
			Name: events.CoalitionReassigned,
			Data: events.CoalitionReassignedEvent{
				Coalition:  coalition,
				OldNodeID:  from.serverID,
				NewNodeID:  to.serverID,
				NewAddr:    to.address,
				GlobalAddr: s.globalNodeAddr,
			},
		})
		delete(s.pendingMoves, coalition)
		s.logger.Info("Flushed pending coalition move", "coalition", coalition, "from", move.fromNodeID, "to", move.toNodeID)
	}
}

// countClientsPerCoalition returns a map of coalition → number of connected voice clients.
// Uses the authoritative clientNodes map when populated; falls back to ServerState.Clients.
// Must be called with s.mu read or write locked (acquires serverState.RLock internally).
func (s *VoiceControlServer) countClientsPerCoalition() map[string]int {
	counts := make(map[string]int)
	s.serverState.RLock()
	defer s.serverState.RUnlock()
	if len(s.clientNodes) > 0 {
		for clientID := range s.clientNodes {
			if client, ok := s.serverState.Clients[clientID]; ok && client.Coalition != "" {
				counts[client.Coalition]++
			}
		}
	} else {
		for _, client := range s.serverState.Clients {
			if client.Coalition != "" {
				counts[client.Coalition]++
			}
		}
	}
	return counts
}

// sendCoalitionAssignment streams an AssignCoalitionsRequest to the node.
// Must be called with s.mu write-locked. Skips silently if the node has no stream yet.
func (s *VoiceControlServer) sendCoalitionAssignment(node *registeredNode) {
	if node.stream == nil {
		return
	}
	node.sendMu.Lock()
	defer node.sendMu.Unlock()
	msg := &pb.ControlMessage{
		Command: &pb.ControlMessage_AssignCoalitions{
			AssignCoalitions: &pb.AssignCoalitionsRequest{Coalitions: node.coalitions},
		},
	}
	if err := node.stream.Send(msg); err != nil {
		s.logger.Error("Failed to send coalition assignment", "node", node.serverID, "error", err)
	}
}

// sendGlobalFrequencies sends the configured global frequencies to an idle node as fallback.
// Must be called with s.mu write-locked.
func (s *VoiceControlServer) sendGlobalFrequencies(node *registeredNode) {
	if node.stream == nil {
		return
	}
	s.settingsState.RLock()
	freqs := s.settingsState.Frequencies.GlobalFrequencies
	s.settingsState.RUnlock()

	ranges := make([]*pb.FrequencyRange, len(freqs))
	for i, f := range freqs {
		ranges[i] = &pb.FrequencyRange{StartFrequency: float64(f), EndFrequency: float64(f)}
	}
	node.sendMu.Lock()
	defer node.sendMu.Unlock()
	msg := &pb.ControlMessage{
		Command: &pb.ControlMessage_AssignFrequencies{
			AssignFrequencies: &pb.AssignFrequenciesRequest{FrequencyRanges: ranges},
		},
	}
	if err := node.stream.Send(msg); err != nil {
		s.logger.Error("Failed to send frequency assignment", "node", node.serverID, "error", err)
	}
}

func leastLoaded(nodes []*registeredNode) *registeredNode {
	least := nodes[0]
	for _, n := range nodes[1:] {
		if len(n.coalitions) < len(least.coalitions) {
			least = n
		}
	}
	return least
}

func removeAt(s []string, i int) []string {
	out := make([]string, 0, len(s)-1)
	out = append(out, s[:i]...)
	out = append(out, s[i+1:]...)
	return out
}

func indexStr(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

func (s *VoiceControlServer) ReportClientConnected(ctx context.Context, req *pb.ClientConnectedRequest) (*pb.ClientConnectedResponse, error) {
	clientID, err := uuid.Parse(req.ClientId)
	if err != nil {
		return &pb.ClientConnectedResponse{Success: false, Message: "invalid client id"}, nil
	}
	s.mu.Lock()
	s.clientNodes[clientID] = req.ServerId
	s.mu.Unlock()
	return &pb.ClientConnectedResponse{Success: true, Message: "ok"}, nil
}

func (s *VoiceControlServer) ReportClientDisconnected(ctx context.Context, req *pb.ClientDisconnectedRequest) (*pb.ClientDisconnectedResponse, error) {
	clientID, err := uuid.Parse(req.ClientId)
	if err != nil {
		return &pb.ClientDisconnectedResponse{Acknowledged: false}, nil
	}
	s.mu.Lock()
	delete(s.clientNodes, clientID)
	s.tryFlushPendingMoves()
	s.mu.Unlock()
	return &pb.ClientDisconnectedResponse{Acknowledged: true}, nil
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

			// Rebalance when the coalition list changes.
			if event.Name == events.CoalitionsChanged {
				s.mu.Lock()
				s.rebalance(false)
				s.mu.Unlock()
				continue
			}

			// Flush pending sticky moves when a client leaves.
			if event.Name == events.ClientsChanged {
				if ce, ok2 := event.Data.(events.ClientChangeEvent); ok2 && ce.Type == events.ClientLeft {
					s.mu.Lock()
					s.tryFlushPendingMoves()
					s.mu.Unlock()
				}
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
