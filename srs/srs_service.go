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
	"google.golang.org/grpc/metadata"
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
	// SRV-15: the whole roster and radio set are built under one read lock,
	// and the roster carries UnitId/RoleId as buildServerUpdate's do.
	var srsClients map[string]*pb.ClientInfo
	var srsRadios map[string]*pb.RadioInfo
	s.serverState.RLock()
	for id, client := range s.serverState.Clients {
		if client == nil {
			continue
		}
		if srsClients == nil {
			srsClients = make(map[string]*pb.ClientInfo)
		}
		srsClients[id.String()] = &pb.ClientInfo{
			Name:       client.Name,
			Coalition:  client.Coalition,
			UnitId:     client.UnitId,
			RoleId:     uint32(client.Role),
			LastUpdate: ptrInt64(client.LastUpdate.Unix()),
		}
	}
	for id, radio := range s.serverState.RadioClients {
		if radio == nil {
			continue
		}
		clientEntry, clientExists := s.serverState.Clients[id]
		if !clientExists || clientEntry == nil {
			continue
		}
		if srsRadios == nil {
			srsRadios = make(map[string]*pb.RadioInfo)
		}
		srsRadios[id.String()] = &pb.RadioInfo{
			Radios:     convertRadios(radio.Radios),
			Muted:      radio.Muted,
			LastUpdate: ptrInt64(clientEntry.LastUpdate.Unix()),
		}
	}
	s.serverState.RUnlock()
	// SRV-15: no ClientsChanged publish; SyncClient changes no roster field.

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

	// SRV-16: settings are read before serverState is locked, so the two
	// locks never nest.
	coalitionExists := s.settingsState.DoesCoalitionExist(req.Coalition)

	// SRV-16: validate every field, then apply all of them, in one critical
	// section; a failed call changes nothing but LastUpdate (liveness).
	s.serverState.Lock()
	client, exists := s.serverState.Clients[clientID]
	if !exists || client == nil {
		s.serverState.Unlock()
		s.logger.Error("UpdateClientInfo failed: client not found", "client_id", clientID)
		return &pb.ServerResponse{
			Success:      false,
			ErrorMessage: "Internal error: You may already have been disconnected.",
		}, nil
	}
	client.LastUpdate = s.now() // SRV-01

	var errors []string
	if !checkUsername(req.Name) {
		s.logger.Warn("UpdateClientInfo failed: invalid client name", "client_id", clientID, "client_name", req.Name)
		errors = append(errors, "Invalid username. It must be between 1 and 32 characters long.")
	}
	if !checkUnitId(req.UnitId) {
		s.logger.Warn("UpdateClientInfo failed: invalid unit ID", "client_id", clientID, "unit_id", req.UnitId)
		errors = append(errors, "Invalid unit ID. It must be 2 to 4 uppercase alphanumeric characters.")
	}
	switch {
	case req.Coalition == client.Coalition:
		// Unchanged: always accepted, even if an admin has since removed it.
	case client.Role == utils.GuestRole:
		// A guest's coalition is the one whose password it logged in with.
		s.logger.Warn("UpdateClientInfo failed: guest cannot change coalition", "client_id", clientID, "coalition", req.Coalition)
		errors = append(errors, "Guests cannot change coalition here: log in again with that coalition's password.")
	case !coalitionExists:
		s.logger.Warn("UpdateClientInfo failed: invalid coalition", "client_id", clientID, "coalition", req.Coalition)
		errors = append(errors, "Coalition not found, please select an existing coalition.")
	}
	if !canSwapRoles(client, uint8(req.RoleId)) {
		s.logger.Warn("UpdateClientInfo failed: Client cannot swap roles", "client_id", clientID, "role_id", req.RoleId)
		errors = append(errors, "You cannot swap roles. Please contact an administrator if you need a different role.")
	}

	if len(errors) > 0 {
		s.serverState.Unlock()
		s.logger.Warn("UpdateClientInfo completed with errors", "client_id", clientID, "errors", errors)
		return &pb.ServerResponse{
			Success:      false,
			ErrorMessage: "Errors occurred while updating client info: \n - " + strings.Join(errors, "\n - "),
		}, nil
	}

	client.Name = req.Name
	client.UnitId = req.UnitId
	client.Coalition = req.Coalition
	client.Role = uint8(req.RoleId)
	clientsSnap := s.serverState.CloneClientsLocked() // SRV-09
	s.serverState.Unlock()

	s.logger.Info("Updated client info", "client_id", clientID, "client_name", req.Name)
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
	radioSnap := s.serverState.CloneRadiosLocked() // SRV-09
	s.serverState.Unlock()
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

	// SRV-08: only stream-relevant events, and a subscription that reports
	// overflow instead of silently dropping (see fellBehind below).
	evs := s.eventBus.SubscribeNames(updateStreamBuffer, streamEvents...)
	defer evs.Close()

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

	// C17: the response headers mean "this subscription is live". evs and
	// sub are both registered and any older subscriber is superseded, so an
	// event published after the client observes the headers is delivered
	// here. A client that resubscribes waits for them before SyncClient.
	if err := stream.SendHeader(metadata.MD{}); err != nil {
		s.logger.Warn("Failed to send update stream headers", "client_id", clientID, "error", err)
		return err
	}

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

	// fellBehind ends this stream with ResourceExhausted after its
	// subscription overflowed (SRV-08). The session is kept: the
	// subscription is unregistered by identity and the SV1 grace starts.
	// It never calls RemoveClient.
	fellBehind := func() error {
		if sub.ended() {
			return finish()
		}
		s.mu.Lock()
		mine := s.streams[clientID] == sub
		if mine {
			delete(s.streams, clientID)
			s.detachedAt[clientID] = s.now()
		}
		s.mu.Unlock()
		if !mine {
			// A removal or a newer subscriber owns the ending; it ends sub
			// after unregistering it, so a KICK/BAN final is still delivered.
			<-sub.done
			return finish()
		}
		sub.end(nil, errFellBehind)
		s.logger.Warn("Update stream fell behind; ending it so the client resyncs",
			"client_id", clientID, "dropped", evs.Dropped())
		return finish()
	}

	for {
		select {
		case <-sub.done:
			return finish()
		case <-stream.Context().Done():
			s.logger.Info("Client unsubscribed from updates", "client_id", clientID)
			return nil
		case <-evs.Overflow():
			return fellBehind()
		case event, ok := <-evs.C:
			if !ok {
				return nil
			}
			if sub.ended() {
				return finish()
			}
			// Do not drain a buffer to a client that has already fallen behind.
			select {
			case <-evs.Overflow():
				return fellBehind()
			default:
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
