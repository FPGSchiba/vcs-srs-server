package srs

import (
	"context"
	pb "github.com/FPGSchiba/vcs-srs-server/srspb"
	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/FPGSchiba/vcs-srs-server/utils"
	"github.com/google/uuid"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/peer"
	"log/slog"
	"sync"
)

type SimpleRadioServer struct {
	pb.UnimplementedSRSServiceServer
	logger        *slog.Logger
	mu            sync.Mutex
	serverState   *state.ServerState
	settingsState *state.SettingsState
}

func NewSimpleRadioServer(serverState *state.ServerState, settingsState *state.SettingsState, logger *slog.Logger) *SimpleRadioServer {
	return &SimpleRadioServer{
		serverState:   serverState,
		settingsState: settingsState,
		logger:        logger,
		mu:            sync.Mutex{},
	}
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

func (s *SimpleRadioServer) GuestLogin(ctx context.Context, request *pb.ClientGuestLoginRequest) (*pb.ServerGuestLoginResponse, error) {
	p, _ := peer.FromContext(ctx)
	s.logger.Info("Getting Guest Login", "IP", p.Addr.String(), "Name", request.Name, "UnitId", request.UnitId)
	// Check Version
	if !checkVersion(request.Version) {
		return &pb.ServerGuestLoginResponse{
			Success:     false,
			LoginResult: &pb.ServerGuestLoginResponse_ErrorMessage{ErrorMessage: "Unsupported version"},
		}, nil
	}

	// Check Username
	if !checkUsername(request.Name) {
		return &pb.ServerGuestLoginResponse{
			Success:     false,
			LoginResult: &pb.ServerGuestLoginResponse_ErrorMessage{ErrorMessage: "Invalid username"},
		}, nil
	}

	// Check Password > Select coalition
	s.mu.Lock()
	var selectedCoalition *state.Coalition
	for _, coalition := range s.settingsState.Coalitions {
		if utils.HashPassword(coalition.Password) == request.Password {
			s.mu.Unlock()
			selectedCoalition = &coalition
		}
	}

	if selectedCoalition == nil {
		return &pb.ServerGuestLoginResponse{
			Success:     false,
			LoginResult: &pb.ServerGuestLoginResponse_ErrorMessage{ErrorMessage: "No Coalition found with that password"},
		}, nil
	}

	// Check Client UnitId
	if !checkUnitId(request.UnitId) {
		return &pb.ServerGuestLoginResponse{
			Success:     false,
			LoginResult: &pb.ServerGuestLoginResponse_ErrorMessage{ErrorMessage: "Invalid UnitId"},
		}, nil
	}
	// Add Client to State
	clientGuid := uuid.New()
	s.serverState.AddClient(clientGuid.String(), &state.ClientState{
		Name:      request.Name,
		UnitId:    request.UnitId,
		Coalition: selectedCoalition.Name,
	})

	// Return Response
	token, err := utils.GenerateToken(clientGuid.String(), GuestRole)
	if err != nil {
		s.logger.Error("Failed to generate token for guest login", "error", err)
		return &pb.ServerGuestLoginResponse{
			Success:     false,
			LoginResult: &pb.ServerGuestLoginResponse_ErrorMessage{ErrorMessage: "Failed to generate token"},
		}, err
	}
	return &pb.ServerGuestLoginResponse{
		Success: true,
		LoginResult: &pb.ServerGuestLoginResponse_Result{
			Result: &pb.GuestLoginResult{
				Token:      token,
				ClientGuid: clientGuid.String(),
				Coalition:  selectedCoalition.Name,
			},
		},
	}, nil
}
