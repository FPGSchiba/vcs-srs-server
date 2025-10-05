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
	"github.com/google/uuid"
	"google.golang.org/grpc"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

type SimpleRadioServer struct {
	pb.UnimplementedSRSServiceServer
	logger        *slog.Logger
	mu            sync.Mutex
	serverState   *state.ServerState
	settingsState *state.SettingsState
	eventBus      *events.EventBus
	streams       map[uuid.UUID]grpc.ServerStreamingServer[pb.ServerUpdate]
}

func NewSimpleRadioServer(serverState *state.ServerState, settingsState *state.SettingsState, logger *slog.Logger, bus *events.EventBus) *SimpleRadioServer {
	server := SimpleRadioServer{
		serverState:   serverState,
		settingsState: settingsState,
		eventBus:      bus,
		logger:        logger,
		mu:            sync.Mutex{},
		streams:       make(map[uuid.UUID]grpc.ServerStreamingServer[pb.ServerUpdate]),
	}
	server.StartCleanupRoutine(time.Second*15, time.Minute*10)
	return &server
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

func (s *SimpleRadioServer) SyncClient(_ context.Context, _ *pb.Empty) (*pb.SyncResponse, error) {
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
		if srsRadios == nil {
			srsRadios = make(map[string]*pb.RadioInfo)
		}
		s.serverState.RLock()
		srsRadios[radio.ID.String()] = &pb.RadioInfo{
			Radios:     convertRadios(radio.State.Radios),
			Muted:      radio.State.Muted,
			LastUpdate: ptrInt64(s.serverState.Clients[radio.ID].LastUpdate.Unix()),
		}
		s.serverState.RUnlock()
	}

	s.eventBus.Publish(events.Event{
		Name: events.ClientsChanged,
		Data: s.serverState.Clients,
	})

	return &pb.SyncResponse{
		Success: true,
		SyncResult: &pb.SyncResponse_Data{
			Data: &pb.ServerSyncResult{
				Clients:  srsClients,
				Radios:   srsRadios,
				Settings: s.buildServerSettings(),
			},
		},
	}, nil
}

func (s *SimpleRadioServer) GetServerSettings(_ context.Context, _ *pb.Empty) (*pb.ServerSettings, error) {
	return s.buildServerSettings(), nil
}

func (s *SimpleRadioServer) Disconnect(ctx context.Context, _ *pb.Empty) (*pb.ServerResponse, error) {
	clientID, err := uuid.Parse(ctx.Value("client_id").(string))
	if err != nil {
		s.logger.Error("Disconnect failed: invalid client ID", "error", err)
		return &pb.ServerResponse{
			Success:      false,
			ErrorMessage: "Internal error, please try logging in again.",
		}, nil
	}
	s.serverState.RLock()
	client, exists := s.serverState.Clients[clientID]
	_, existsRadio := s.serverState.RadioClients[clientID]
	s.serverState.RUnlock()

	if !exists || !existsRadio {
		s.logger.Error("Disconnect failed: client not found", "client_id", clientID)
		s.cleanupClientState(clientID) // Make sure no single radio or client state is left dangling
		return &pb.ServerResponse{
			Success:      false,
			ErrorMessage: "Internal error: You may already have been disconnected.",
		}, nil
	}

	s.logger.Info("Disconnecting client", "client_id", clientID, "client_name", client.Name)
	s.cleanupClientState(clientID)
	s.eventBus.Publish(events.Event{
		Name: events.ClientsChanged,
		Data: s.serverState.Clients,
	})

	return &pb.ServerResponse{
		Success:      true,
		ErrorMessage: "",
	}, nil
}

func (s *SimpleRadioServer) UpdateClientInfo(ctx context.Context, req *pb.ClientInfo) (*pb.ServerResponse, error) {
	clientID, err := uuid.Parse(ctx.Value("client_id").(string))
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
	client.LastUpdate = time.Now()
	s.serverState.Unlock()

	s.logger.Info("Updated client info", "client_id", clientID, "client_name", client.Name)

	if len(errors) > 0 {
		s.logger.Warn("UpdateClientInfo completed with errors", "client_id", clientID, "errors", errors)
		return &pb.ServerResponse{
			Success:      false,
			ErrorMessage: "Errors occurred while updating client info: \n - " + strings.Join(errors, "\n - "),
		}, nil
	}

	s.eventBus.Publish(events.Event{
		Name: events.ClientsChanged,
		Data: s.serverState.Clients,
	})

	return &pb.ServerResponse{
		Success:      true,
		ErrorMessage: "",
	}, nil
}

func (s *SimpleRadioServer) UpdateRadioInfo(ctx context.Context, req *pb.RadioInfo) (*pb.ServerResponse, error) {
	clientID, err := uuid.Parse(ctx.Value("client_id").(string))
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

	// Client has control over their own radios, so we don't need to check if the radios are valid or not.
	s.serverState.Lock()
	s.serverState.RadioClients[clientID] = convertRadioInfo(req)
	s.serverState.Unlock()

	s.eventBus.Publish(events.Event{
		Name: events.RadioClientsChanged,
		Data: s.serverState.RadioClients,
	})

	return &pb.ServerResponse{
		Success:      true,
		ErrorMessage: "",
	}, nil
}

func (s *SimpleRadioServer) SubscribeToUpdates(_ *pb.Empty, stream grpc.ServerStreamingServer[pb.ServerUpdate]) error {
	clientID, err := uuid.Parse(stream.Context().Value("client_id").(string))
	if err != nil {
		s.logger.Error("SubscribeToUpdates failed: invalid client ID", "error", err)
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.streams[clientID]; exists {
		s.logger.Warn("SubscribeToUpdates: client already subscribed", "client_id", clientID)
		return fmt.Errorf("client %s is already subscribed to updates", clientID)
	}
	s.streams[clientID] = stream
	s.logger.Info("Client subscribed to updates", "client_id", clientID)
	return nil
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

func (s *SimpleRadioServer) cleanupClientState(clientID uuid.UUID) {
	s.serverState.Lock()
	defer s.serverState.Unlock()

	// Remove client from server state
	if _, exists := s.serverState.Clients[clientID]; exists {
		delete(s.serverState.Clients, clientID)
	}

	// Remove radio state if it exists
	if _, exists := s.serverState.RadioClients[clientID]; exists {
		delete(s.serverState.RadioClients, clientID)
	}
}

// StartCleanupRoutine launches a goroutine that periodically removes stale clients.
func (s *SimpleRadioServer) StartCleanupRoutine(interval time.Duration, staleAfter time.Duration) {
	go func() {
		for {
			time.Sleep(interval)
			now := time.Now()
			for clientID, stream := range s.streams {
				s.serverState.RLock()
				client, exists := s.serverState.Clients[clientID]
				s.serverState.RUnlock()
				if !exists || now.Sub(client.LastUpdate) > staleAfter {
					if stream != nil {
						stream.Context().Done()
					}
					s.cleanupClientState(clientID)
					s.mu.Lock()
					delete(s.streams, clientID)
					s.mu.Unlock()
					s.logger.Info("Cleaned up stale client", "client_id", clientID)
				}
			}
		}
	}()
}
