package srs

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/events"
	pb "github.com/FPGSchiba/vcs-srs-server/srspb"
	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/FPGSchiba/vcs-srs-server/utils"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// VoiceAddressProvider resolves UDP addresses for a given coalition.
// VoiceControlServer implements this; standalone mode passes nil.
type VoiceAddressProvider interface {
	GetVoiceAddressForCoalition(coalition string) (coalitionAddr, globalAddr string)
}

type SimpleRadioServer struct {
	pb.UnimplementedSRSServiceServer
	logger        *slog.Logger
	mu            sync.Mutex
	wg            sync.WaitGroup
	serverState   *state.ServerState
	settingsState *state.SettingsState
	eventBus      *events.EventBus
	voiceRegistry VoiceAddressProvider
	streams       map[uuid.UUID]*subscription
	// detachedAt records when a client's stream ended from the client side;
	// the 120s reconnect grace runs from here. Guarded by mu.
	detachedAt map[uuid.UUID]time.Time
	// dropper drops a removed client's voice binding (local server and
	// remote voice nodes). May be nil.
	dropper  VoiceSessionDropper
	stopChan chan struct{}
	stopOnce sync.Once
	// staleAfter: a client with a live stream but no RPC for this long is
	// removed. Set by StartCleanupRoutine. Guarded by mu.
	staleAfter time.Duration
	// now is the server's clock (time.Now outside tests).
	now func() time.Time
}

func clientIDFromContext(ctx context.Context) (uuid.UUID, error) {
	rawID, ok := ctx.Value(utils.ClientIDKey).(string)
	if !ok || rawID == "" {
		return uuid.Nil, fmt.Errorf("missing client id in context")
	}
	return uuid.Parse(rawID)
}

func NewSimpleRadioServer(serverState *state.ServerState, settingsState *state.SettingsState, logger *slog.Logger, bus *events.EventBus, voiceRegistry VoiceAddressProvider, dropper VoiceSessionDropper) *SimpleRadioServer {
	server := SimpleRadioServer{
		serverState:   serverState,
		settingsState: settingsState,
		eventBus:      bus,
		logger:        logger,
		voiceRegistry: voiceRegistry,
		dropper:       dropper,
		mu:            sync.Mutex{},
		streams:       make(map[uuid.UUID]*subscription),
		detachedAt:    make(map[uuid.UUID]time.Time),
		stopChan:      make(chan struct{}),
		now:           time.Now,
	}
	server.StartCleanupRoutine(time.Second*15, time.Minute*10)
	return &server
}

func (s *SimpleRadioServer) getVoiceAddresses(coalition string) (coalitionAddr, globalAddr string) {
	if s.voiceRegistry != nil {
		return s.voiceRegistry.GetVoiceAddressForCoalition(coalition)
	}
	return "", ""
}

// voiceSecretFor returns the client's voice secret, or "" if the client is
// unknown. The secret is delivered alongside the voice address so the client
// has everything it needs to send a HELLO.
//
// The auth interceptor rejects any token whose GUID is not in
// serverState.Clients (control.Server.authenticate, SRV-06), so an empty
// return is only reachable when the session is removed between that check
// and this call. A caller that received "" would silently get an unusable
// secret with Success: true, so warn here to keep that race diagnosable.
func (s *SimpleRadioServer) voiceSecretFor(clientID uuid.UUID) string {
	secret, ok := s.serverState.GetVoiceSecret(clientID)
	if secret == "" {
		s.logger.Warn("voiceSecretFor returned an empty secret", "ClientID", clientID, "known", ok)
	}
	return secret
}

func (s *SimpleRadioServer) GetServerState() healthpb.HealthCheckResponse_ServingStatus {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.serverState == nil {
		return healthpb.HealthCheckResponse_SERVICE_UNKNOWN
	}
	// TODO: Implement actual logic to determine server state
	return healthpb.HealthCheckResponse_SERVING
}

func (s *SimpleRadioServer) SyncClient(ctx context.Context, _ *pb.Empty) (*pb.SyncResponse, error) {
	clients := s.serverState.GetAllClients()
	radioClients := s.serverState.GetAllRadios()

	var srsClients map[string]*pb.ClientInfo
	var srsRadios map[string]*pb.RadioInfo
	for _, client := range clients {
		if client.State == nil {
			continue
		}
		if srsClients == nil {
			srsClients = make(map[string]*pb.ClientInfo)
		}
		srsClients[client.ID.String()] = &pb.ClientInfo{
			Name:       client.State.Name,
			Coalition:  client.State.Coalition,
			LastUpdate: ptrInt64(client.State.LastUpdate.Unix()),
		}
	}

	for _, radio := range radioClients {
		if radio.State == nil {
			continue
		}
		s.serverState.RLock()
		clientEntry, clientExists := s.serverState.Clients[radio.ID]
		s.serverState.RUnlock()
		if !clientExists || clientEntry == nil {
			continue
		}
		if srsRadios == nil {
			srsRadios = make(map[string]*pb.RadioInfo)
		}
		srsRadios[radio.ID.String()] = &pb.RadioInfo{
			Radios:     convertRadios(radio.State.Radios),
			Muted:      radio.State.Muted,
			LastUpdate: ptrInt64(clientEntry.LastUpdate.Unix()),
		}
	}

	s.serverState.RLock()
	clientsSnap := make(map[uuid.UUID]*state.ClientState, len(s.serverState.Clients))
	for k, v := range s.serverState.Clients {
		clientsSnap[k] = v
	}
	s.serverState.RUnlock()
	s.eventBus.Publish(events.Event{
		Name: events.ClientsChanged,
		Data: events.ClientChangeEvent{Type: events.ClientInfoUpdated, Clients: clientsSnap},
	})

	var coalition string
	var callerID uuid.UUID
	if clientID, err := clientIDFromContext(ctx); err == nil {
		callerID = clientID
		s.serverState.Lock()
		if client, exists := s.serverState.Clients[clientID]; exists {
			coalition = client.Coalition
			client.LastUpdate = s.now() // SRV-01: any RPC proves the client alive
		}
		s.serverState.Unlock()
	}
	coalitionVoiceAddr, globalVoiceAddr := s.getVoiceAddresses(coalition)

	return &pb.SyncResponse{
		Success: true,
		SyncResult: &pb.SyncResponse_Data{
			Data: &pb.ServerSyncResult{
				Clients:            srsClients,
				Radios:             srsRadios,
				Settings:           s.buildServerSettings(),
				CoalitionVoiceAddr: coalitionVoiceAddr,
				GlobalVoiceAddr:    globalVoiceAddr,
				VoiceSecret:        s.voiceSecretFor(callerID),
			},
		},
	}, nil
}

func (s *SimpleRadioServer) GetServerSettings(_ context.Context, _ *pb.Empty) (*pb.ServerSettings, error) {
	return s.buildServerSettings(), nil
}

func (s *SimpleRadioServer) Ping(ctx context.Context, req *pb.PingRequest) (*pb.PingResponse, error) {
	clientID, err := clientIDFromContext(ctx)
	if err == nil {
		s.serverState.Lock()
		if client, exists := s.serverState.Clients[clientID]; exists {
			client.LatencyToControlMs = req.LastRttMs
			client.LastUpdate = s.now() // SRV-01: a pinging client is alive
		}
		s.serverState.Unlock()
	}
	return &pb.PingResponse{ServerTimeMs: time.Now().UnixMilli()}, nil
}

func (s *SimpleRadioServer) Disconnect(ctx context.Context, _ *pb.Empty) (*pb.ServerResponse, error) {
	clientID, err := clientIDFromContext(ctx)
	if err != nil {
		s.logger.Error("Disconnect failed: invalid client ID", "error", err)
		return &pb.ServerResponse{
			Success:      false,
			ErrorMessage: "Internal error, please try logging in again.",
		}, nil
	}
	s.serverState.RLock()
	client, known := s.serverState.Clients[clientID]
	var clientName string
	if known {
		clientName = client.Name
	}
	s.serverState.RUnlock()
	if known {
		s.logger.Info("Disconnecting client", "client_id", clientID, "client_name", clientName)
	}
	if !s.RemoveClient(clientID, ReasonDisconnect, nil) {
		s.logger.Error("Disconnect failed: client not found", "client_id", clientID)
		return &pb.ServerResponse{
			Success:      false,
			ErrorMessage: "Internal error: You may already have been disconnected.",
		}, nil
	}

	return &pb.ServerResponse{
		Success:      true,
		ErrorMessage: "",
	}, nil
}

func (s *SimpleRadioServer) UpdateClientInfo(ctx context.Context, req *pb.ClientInfo) (*pb.ServerResponse, error) {
	clientID, err := clientIDFromContext(ctx)
	if err != nil {
		s.logger.Error("UpdateClientInfo failed: invalid client ID", "error", err)
		return &pb.ServerResponse{
			Success:      false,
			ErrorMessage: "Internal error, please try logging in again.",
		}, nil
	}

	s.serverState.RLock()
	client, exists := s.serverState.Clients[clientID]
	s.serverState.RUnlock()
	if !exists {
		s.logger.Error("UpdateClientInfo failed: client not found", "client_id", clientID)
		return &pb.ServerResponse{
			Success:      false,
			ErrorMessage: "Internal error: You may already have been disconnected.",
		}, nil
	}

	var errors []string

	if !checkUsername(req.Name) {
		s.logger.Warn("UpdateClientInfo failed: invalid client name", "client_id", clientID, "client_name", req.Name)
		errors = append(errors, "Invalid username. It must be between 1 and 32 characters long.")
	} else {
		s.serverState.Lock()
		client.Name = req.Name
		s.serverState.Unlock()
	}

	if !checkUnitId(req.UnitId) {
		s.logger.Warn("UpdateClientInfo failed: invalid unit ID", "client_id", clientID, "unit_id", req.UnitId)
		errors = append(errors, "Invalid unit ID. It must be 2 to 4 uppercase alphanumeric characters.")
	} else {
		s.serverState.Lock()
		client.UnitId = req.UnitId
		s.serverState.Unlock()
	}

	if !s.settingsState.DoesCoalitionExist(req.Coalition) {
		s.logger.Warn("UpdateClientInfo failed: invalid coalition", "client_id", clientID, "coalition", req.Coalition)
		errors = append(errors, "Coalition not found, please select an existing coalition.")
	} else {
		s.serverState.Lock()
		client.Coalition = req.Coalition
		s.serverState.Unlock()
	}

	if !canSwapRoles(client, uint8(req.RoleId)) {
		s.logger.Warn("UpdateClientInfo failed: Client cannot swap roles", "client_id", clientID, "role_id", req.RoleId)
		errors = append(errors, "You cannot swap roles. Please contact an administrator if you need a different role.")
	} else {
		s.serverState.Lock()
		client.Role = uint8(req.RoleId)
		s.serverState.Unlock()
	}

	s.serverState.Lock()
	client.LastUpdate = s.now()
	s.serverState.Unlock()

	s.logger.Info("Updated client info", "client_id", clientID, "client_name", client.Name)

	if len(errors) > 0 {
		s.logger.Warn("UpdateClientInfo completed with errors", "client_id", clientID, "errors", errors)
		return &pb.ServerResponse{
			Success:      false,
			ErrorMessage: "Errors occurred while updating client info: \n - " + strings.Join(errors, "\n - "),
		}, nil
	}

	s.serverState.RLock()
	clientsSnap := make(map[uuid.UUID]*state.ClientState, len(s.serverState.Clients))
	for k, v := range s.serverState.Clients {
		clientsSnap[k] = v
	}
	s.serverState.RUnlock()
	s.eventBus.Publish(events.Event{
		Name: events.ClientsChanged,
		Data: events.ClientChangeEvent{Type: events.ClientInfoUpdated, ClientID: clientID, Clients: clientsSnap},
	})

	return &pb.ServerResponse{
		Success:      true,
		ErrorMessage: "",
	}, nil
}

func (s *SimpleRadioServer) UpdateRadioInfo(ctx context.Context, req *pb.RadioInfo) (*pb.ServerResponse, error) {
	clientID, err := clientIDFromContext(ctx)
	if err != nil {
		s.logger.Error("UpdateRadioInfo failed: invalid client ID", "error", err)
		return &pb.ServerResponse{
			Success:      false,
			ErrorMessage: "Internal error, please try logging in again.",
		}, nil
	}

	s.serverState.RLock()
	_, exists := s.serverState.RadioClients[clientID]
	s.serverState.RUnlock()
	if !exists {
		s.logger.Error("UpdateRadioInfo failed: client not found", "client_id", clientID)
		return &pb.ServerResponse{
			Success:      false,
			ErrorMessage: "Internal error: We could not find your Radios...\n Please try logging in again.",
		}, nil
	}

	// Client has control over their own radios, but the server owns the mute status.
	s.serverState.Lock()
	existingMuted := false
	if existing, exists := s.serverState.RadioClients[clientID]; exists {
		existingMuted = existing.Muted
	}
	newState := convertRadioInfo(req)
	newState.Muted = existingMuted
	s.serverState.RadioClients[clientID] = newState
	if client, ok := s.serverState.Clients[clientID]; ok {
		client.LastUpdate = s.now() // SRV-01
	}
	s.serverState.Unlock()

	s.serverState.RLock()
	radioSnap := make(map[uuid.UUID]*state.RadioState, len(s.serverState.RadioClients))
	for k, v := range s.serverState.RadioClients {
		radioSnap[k] = v
	}
	s.serverState.RUnlock()
	s.eventBus.Publish(events.Event{
		Name: events.RadioClientsChanged,
		Data: events.RadioChangeEvent{Type: events.RadioUpdated, ClientID: clientID, Radios: radioSnap},
	})

	return &pb.ServerResponse{
		Success:      true,
		ErrorMessage: "",
	}, nil
}

func (s *SimpleRadioServer) clientExists(id uuid.UUID) bool {
	s.serverState.RLock()
	defer s.serverState.RUnlock()
	_, ok := s.serverState.Clients[id]
	return ok
}

func (s *SimpleRadioServer) SubscribeToUpdates(_ *pb.Empty, stream grpc.ServerStreamingServer[pb.ServerUpdate]) error {
	clientID, err := clientIDFromContext(stream.Context())
	if err != nil {
		s.logger.Error("SubscribeToUpdates failed: invalid client ID", "error", err)
		return err
	}
	if !s.clientExists(clientID) {
		return errNoSession
	}

	ch := s.eventBus.Subscribe("*")
	defer s.eventBus.Unsubscribe(ch)

	// CON-05: the last subscriber wins. A reconnecting client whose old
	// transport the server has not yet noticed as dead must not be refused.
	sub := newSubscription()
	s.mu.Lock()
	old := s.streams[clientID]
	s.streams[clientID] = sub
	delete(s.detachedAt, clientID)
	s.mu.Unlock()

	// A removal may have run between the presence check and registration;
	// it would not have seen this subscription, so re-check now.
	if !s.clientExists(clientID) {
		s.mu.Lock()
		if s.streams[clientID] == sub {
			delete(s.streams, clientID)
		}
		s.mu.Unlock()
		if old != nil {
			old.end(nil, errSuperseded)
		}
		return errNoSession
	}
	if old != nil {
		old.end(nil, errSuperseded)
	}

	defer func() {
		// Only a subscription ended by the client side is still registered
		// here (see the subscription invariant); that starts the grace.
		s.mu.Lock()
		if s.streams[clientID] == sub {
			delete(s.streams, clientID)
			s.detachedAt[clientID] = s.now()
		}
		s.mu.Unlock()
	}()

	s.logger.Info("Client subscribed to updates", "client_id", clientID)

	finish := func() error {
		if sub.final != nil {
			if err := stream.Send(sub.final); err != nil {
				s.logger.Warn("Failed to send final update", "client_id", clientID, "error", err)
			}
		}
		s.logger.Info("Update stream ended by server", "client_id", clientID, "status", sub.err)
		return sub.err
	}

	for {
		select {
		case <-sub.done:
			return finish()
		case <-stream.Context().Done():
			s.logger.Info("Client unsubscribed from updates", "client_id", clientID)
			return nil
		case event, ok := <-ch:
			if !ok {
				return nil
			}
			if sub.ended() {
				return finish()
			}

			// Per-client voice address redirect after coalition rebalancing.
			if event.Name == events.CoalitionReassigned {
				if ce, ok2 := event.Data.(events.CoalitionReassignedEvent); ok2 {
					s.serverState.RLock()
					client, exists := s.serverState.Clients[clientID]
					coalition := ""
					if exists {
						coalition = client.Coalition
					}
					s.serverState.RUnlock()
					if exists && coalition == ce.Coalition {
						addr := &pb.ServerUpdate{
							Type: pb.ServerUpdate_VOICE_ADDRESS_UPDATE,
							Update: &pb.ServerUpdate_VoiceAddressUpdate{
								VoiceAddressUpdate: &pb.VoiceAddressUpdate{
									CoalitionVoiceAddr: ce.NewAddr,
									GlobalVoiceAddr:    ce.GlobalAddr,
									VoiceSecret:        s.voiceSecretFor(clientID),
								},
							},
						}
						if err := stream.Send(addr); err != nil {
							s.logger.Error("Failed to send voice address update", "client_id", clientID, "error", err)
							return err
						}
					}
				}
				continue
			}

			if !forwardServerAction(event, clientID) {
				continue
			}
			update := s.buildServerUpdate(event)
			if update == nil {
				continue
			}
			if err := stream.Send(update); err != nil {
				s.logger.Error("Failed to send update to client", "client_id", clientID, "error", err)
				return err
			}
		}
	}
}

func (s *SimpleRadioServer) buildServerSettings() *pb.ServerSettings {
	s.settingsState.RLock()
	defer s.settingsState.RUnlock()

	var coalitions []*pb.Coalition
	for _, coalition := range s.settingsState.Coalitions {
		coalitions = append(coalitions, &pb.Coalition{
			Name:        coalition.Name,
			Description: coalition.Description,
			Color:       coalition.Color,
		})
	}

	settings := &pb.ServerSettings{
		Coalitions:        coalitions,
		TestFrequencies:   s.settingsState.Frequencies.TestFrequencies,
		GlobalFrequencies: s.settingsState.Frequencies.GlobalFrequencies,
		GeneralSettings: &pb.GeneralServerSettings{
			MaxRadiosPerClient: int32(s.settingsState.General.MaxRadiosPerUser),
		},
	}

	return settings
}

// StartCleanupRoutine launches a goroutine that periodically removes dead
// sessions (see reapOnce).
func (s *SimpleRadioServer) StartCleanupRoutine(interval time.Duration, staleAfter time.Duration) {
	s.mu.Lock()
	s.staleAfter = staleAfter
	s.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-s.stopChan:
				return
			case <-ticker.C:
				s.reapOnce(s.now())
			}
		}
	}()
}

// Stop signals the cleanup goroutine to exit and waits for it to finish.
func (s *SimpleRadioServer) Stop() {
	s.stopOnce.Do(func() { close(s.stopChan) })
	s.wg.Wait()
}

// buildServerUpdate converts a bus event into a pb.ServerUpdate to send to
// subscribed clients. Returns nil for event types that should not be forwarded.
func (s *SimpleRadioServer) buildServerUpdate(event events.Event) *pb.ServerUpdate {
	switch event.Name {
	case events.ClientsChanged:
		ce, ok := event.Data.(events.ClientChangeEvent)
		if !ok {
			return nil
		}
		guid := ce.ClientID.String()
		switch ce.Type {
		case events.ClientJoined:
			var clientInfo *pb.ClientInfo
			if c, exists := ce.Clients[ce.ClientID]; exists {
				clientInfo = &pb.ClientInfo{
					Name:      c.Name,
					Coalition: c.Coalition,
					UnitId:    c.UnitId,
					RoleId:    uint32(c.Role),
				}
			}
			return &pb.ServerUpdate{
				Type: pb.ServerUpdate_CLIENT_JOINED,
				Update: &pb.ServerUpdate_ClientUpdate{
					ClientUpdate: &pb.ClientUpdate{
						ClientGuid: &guid,
						ClientInfo: clientInfo,
					},
				},
			}
		case events.ClientLeft:
			return &pb.ServerUpdate{
				Type: pb.ServerUpdate_CLIENT_LEFT,
				Update: &pb.ServerUpdate_ClientUpdate{
					ClientUpdate: &pb.ClientUpdate{
						ClientGuid: &guid,
					},
				},
			}
		default: // ClientInfoUpdated
			var clientInfo *pb.ClientInfo
			if c, exists := ce.Clients[ce.ClientID]; exists {
				clientInfo = &pb.ClientInfo{
					Name:      c.Name,
					Coalition: c.Coalition,
					UnitId:    c.UnitId,
					RoleId:    uint32(c.Role),
				}
			}
			return &pb.ServerUpdate{
				Type: pb.ServerUpdate_CLIENT_INFO_UPDATE,
				Update: &pb.ServerUpdate_ClientUpdate{
					ClientUpdate: &pb.ClientUpdate{
						ClientGuid: &guid,
						ClientInfo: clientInfo,
					},
				},
			}
		}

	case events.RadioClientsChanged:
		re, ok := event.Data.(events.RadioChangeEvent)
		if !ok {
			return nil
		}
		guid := re.ClientID.String()
		var radioInfo *pb.RadioInfo
		if r, exists := re.Radios[re.ClientID]; exists {
			radioInfo = &pb.RadioInfo{
				Radios: convertRadios(r.Radios),
				Muted:  r.Muted,
			}
		}
		return &pb.ServerUpdate{
			Type: pb.ServerUpdate_CLIENT_RADIO_UPDATE,
			Update: &pb.ServerUpdate_ClientUpdate{
				ClientUpdate: &pb.ClientUpdate{
					ClientGuid: &guid,
					RadioInfo:  radioInfo,
				},
			},
		}

	case events.SettingsChanged, events.CoalitionsChanged:
		return &pb.ServerUpdate{
			Type:   pb.ServerUpdate_SERVER_SETTINGS_CHANGED,
			Update: &pb.ServerUpdate_SettingsUpdate{SettingsUpdate: s.buildServerSettings()},
		}

	case events.ServerAction:
		ae, ok := event.Data.(events.ServerActionEvent)
		if !ok {
			return nil
		}
		return ServerActionUpdate(ae.ActionType, ae.TargetClientID, ae.Reason)

	default:
		return nil
	}
}

// ServerActionUpdate builds the SERVER_ACTION update for action against
// target, or nil for an unknown action.
func ServerActionUpdate(action events.ActionType, target uuid.UUID, reason string) *pb.ServerUpdate {
	var actionType pb.ServerAction_ActionType
	switch action {
	case events.ActionKick:
		actionType = pb.ServerAction_KICK
	case events.ActionBan:
		actionType = pb.ServerAction_BAN
	case events.ActionMute:
		actionType = pb.ServerAction_MUTE
	case events.ActionUnmute:
		actionType = pb.ServerAction_UNMUTE
	default:
		return nil
	}
	return &pb.ServerUpdate{
		Type: pb.ServerUpdate_SERVER_ACTION,
		Update: &pb.ServerUpdate_ServerAction{
			ServerAction: &pb.ServerAction{
				Type:             actionType,
				TargetClientGuid: target.String(),
				Reason:           reason,
			},
		},
	}
}

// forwardServerAction reports whether a bus event may be forwarded on
// clientID's update stream. SERVER_ACTION is delivered only to its target:
// MUTE/UNMUTE from the bus, and KICK/BAN never from the bus -- they reach
// the target only as its subscription's final update (see RemoveClient), so
// they arrive exactly once and immediately before the stream ends. Every
// other event is forwarded.
func forwardServerAction(event events.Event, clientID uuid.UUID) bool {
	if event.Name != events.ServerAction {
		return true
	}
	ae, ok := event.Data.(events.ServerActionEvent)
	if !ok {
		return false
	}
	switch ae.ActionType {
	case events.ActionMute, events.ActionUnmute:
		return ae.TargetClientID == clientID
	default:
		return false
	}
}
