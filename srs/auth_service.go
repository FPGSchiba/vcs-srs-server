package srs

import (
	"context"
	"fmt"
	pb "github.com/FPGSchiba/vcs-srs-server/srspb"
	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/FPGSchiba/vcs-srs-server/utils"
	"github.com/google/uuid"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/peer"
	"log/slog"
	"sync"
)

type AuthServer struct {
	pb.UnimplementedAuthServiceServer
	logger            *slog.Logger
	mu                sync.Mutex
	serverState       *state.ServerState
	settingsState     *state.SettingsState
	distributionState *state.DistributionState
}

func NewAuthServer(serverState *state.ServerState, settingsState *state.SettingsState, logger *slog.Logger, distributionState *state.DistributionState) *AuthServer {
	return &AuthServer{
		serverState:       serverState,
		settingsState:     settingsState,
		logger:            logger,
		mu:                sync.Mutex{},
		distributionState: distributionState,
	}
}

func (s *AuthServer) GetServerState() healthpb.HealthCheckResponse_ServingStatus {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.serverState == nil {
		return healthpb.HealthCheckResponse_SERVICE_UNKNOWN
	}
	// TODO: Implement actual logic to determine server state
	return healthpb.HealthCheckResponse_SERVING
}

func (s *AuthServer) isControlServer() bool {
	s.distributionState.RLock()
	defer s.distributionState.RUnlock()
	return s.distributionState.DistributionMode == state.DistributionModeControl
}

func (s *AuthServer) GuestLogin(ctx context.Context, request *pb.ClientGuestLoginRequest) (*pb.ServerGuestLoginResponse, error) {
	p, _ := peer.FromContext(ctx)
	s.logger.Debug("Getting Guest Login", "IP", p.Addr.String(), "Name", request.Name, "UnitId", request.UnitId)
	// Check Version
	if !checkVersion(request.Capabilities.Version) {
		return &pb.ServerGuestLoginResponse{
			Success:     false,
			LoginResult: &pb.ServerGuestLoginResponse_ErrorMessage{ErrorMessage: "Unsupported version"},
		}, nil
	}

	// Check Distribution Capabilities
	if !s.checkDistributionCapabilities(request.Capabilities.SupportedFeatures) {
		return &pb.ServerGuestLoginResponse{
			Success:     false,
			LoginResult: &pb.ServerGuestLoginResponse_ErrorMessage{ErrorMessage: fmt.Sprintf("Unsupported distribution capabilities, currently running: %s", s.GetStringDistributionMode())},
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

	s.logger.Info("guest login succeeded for ", "Guest Name", request.Name, "UnitId", request.UnitId, "Coalition", selectedCoalition.Name, "ClientGuid", clientGuid.String())
	response := &pb.ServerGuestLoginResponse{
		Success: true,
		LoginResult: &pb.ServerGuestLoginResponse_Result{
			Result: &pb.GuestLoginResult{
				Token:      token,
				ClientGuid: clientGuid.String(),
				Coalition:  selectedCoalition.Name,
			},
		},
	}
	return response, nil
}

func (s *AuthServer) VanguardLogin(ctx context.Context, request *pb.ClientVanguardLoginRequest) (*pb.ServerVanguardLoginResponse, error) {
	p, _ := peer.FromContext(ctx)
	s.logger.Debug("Getting Vanguard Login", "IP", p.Addr.String(), "Name", request.Email)

	// Check Version
	if !checkVersion(request.Capabilities.Version) {
		return &pb.ServerVanguardLoginResponse{
			Success:     false,
			LoginResult: &pb.ServerVanguardLoginResponse_ErrorMessage{ErrorMessage: "Unsupported version"},
		}, nil
	}

	// Check Distribution Capabilities
	if !s.checkDistributionCapabilities(request.Capabilities.SupportedFeatures) {
		return &pb.ServerVanguardLoginResponse{
			Success:     false,
			LoginResult: &pb.ServerVanguardLoginResponse_ErrorMessage{ErrorMessage: fmt.Sprintf("Unsupported distribution capabilities, currently running: %s", s.GetStringDistributionMode())},
		}, nil
	}

	return nil, nil
}

func (s *AuthServer) checkDistributionCapabilities(features []pb.ClientFeature) bool {
	s.distributionState.RLock()
	currentDistributionMode := s.distributionState.DistributionMode
	s.distributionState.RUnlock()
	for _, feature := range features {
		switch feature {
		case pb.ClientFeature_DISTRIBUTED:
			if currentDistributionMode == state.DistributionModeControl {
				return true
			}
			continue
		case pb.ClientFeature_STANDALONE:
			if currentDistributionMode == state.DistributionModeStandalone {
				return true
			}
			continue
		}
	}
	return false
}

func (s *AuthServer) GetStringDistributionMode() string {
	s.distributionState.RLock()

	defer s.distributionState.RUnlock()
	switch s.distributionState.DistributionMode {
	case state.DistributionModeControl:
		return "Control"
	case state.DistributionModeStandalone:
		return "Standalone"
	case state.DistributionModeVoice:
		return "Voice"
	default:
		return "Unknown"
	}
}
