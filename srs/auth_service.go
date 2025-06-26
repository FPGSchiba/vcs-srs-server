package srs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	pb "github.com/FPGSchiba/vcs-srs-server/srspb"
	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/FPGSchiba/vcs-srs-server/utils"
	"github.com/google/uuid"
	"github.com/sethvargo/go-diceware/diceware"
	"github.com/sony/gobreaker/v2"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/peer"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

type AuthServer struct {
	pb.UnimplementedAuthServiceServer
	logger                *slog.Logger
	mu                    sync.Mutex
	serverState           *state.ServerState
	settingsState         *state.SettingsState
	distributionState     *state.DistributionState
	wixCircuitBreaker     *gobreaker.CircuitBreaker[*WixLoginResponse]
	authenticatingClients []*AuthenticatingClient
}

type AuthenticatingClient struct {
	ClientId       string
	Secret         string
	Expires        time.Time
	AvailableRoles []uint8
	AvailableUnits []WixUnitResult
}

type WixLoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type WixLoginResponse struct {
	Success bool            `json:"success"`
	Message string          `json:"message"`
	Error   interface{}     `json:"error,omitempty"`
	Data    *WixLoginResult `json:"data,omitempty"`
}

type WixLoginResult struct {
	UserId         string          `json:"userId"`
	DisplayName    string          `json:"displayName"`
	AvailableUnits []WixUnitResult `json:"availableUnits"`
	AvailableRoles []uint8         `json:"availableRoles"`
}

type WixUnitResult struct {
	UnitId string `json:"unitId"`
	Name   string `json:"name"`
}

func NewAuthServer(serverState *state.ServerState, settingsState *state.SettingsState, logger *slog.Logger, distributionState *state.DistributionState) *AuthServer {
	return &AuthServer{
		serverState:       serverState,
		settingsState:     settingsState,
		logger:            logger,
		mu:                sync.Mutex{},
		distributionState: distributionState,
		wixCircuitBreaker: gobreaker.NewCircuitBreaker[*WixLoginResponse](gobreaker.Settings{
			Name: "WixLogin",
			ReadyToTrip: func(counts gobreaker.Counts) bool {
				failureRatio := float64(counts.TotalFailures) / float64(counts.Requests)
				return counts.Requests >= 3 && failureRatio >= 0.6
			},
		}),
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
	// check if This auth type is enabled
	s.settingsState.RLock()
	if !s.settingsState.Security.EnableGuestAuth {
		s.settingsState.RUnlock()
		return &pb.ServerGuestLoginResponse{
			Success:     false,
			LoginResult: &pb.ServerGuestLoginResponse_ErrorMessage{ErrorMessage: "Guest login is disabled"},
		}, nil
	}
	s.settingsState.RUnlock()

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
	s.settingsState.RLock()
	token, err := utils.GenerateToken(
		clientGuid.String(),
		utils.GuestRole,
		s.settingsState.Security.Token.Issuer,
		s.settingsState.Security.Token.Subject,
		time.Duration(s.settingsState.Security.Token.Expiration)*time.Second,
		s.settingsState.Security.Token.PrivateKeyFile,
		s.settingsState.Security.Token.PublicKeyFile)
	s.settingsState.RUnlock()
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

	// Check if This auth type is enabled
	s.settingsState.RLock()
	if !s.settingsState.Security.EnableVanguardAuth {
		s.settingsState.RUnlock()
		return &pb.ServerVanguardLoginResponse{
			Success:     false,
			LoginResult: &pb.ServerVanguardLoginResponse_ErrorMessage{ErrorMessage: "Vanguard login is disabled"},
		}, nil
	}
	s.settingsState.RUnlock()

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

	loginResponse, err := s.WixLogin(request.Email, request.Password)
	if err != nil {
		s.logger.Error("Wix login failed", "Email", request.Email, "Error", err)
		return &pb.ServerVanguardLoginResponse{
			Success:     false,
			LoginResult: &pb.ServerVanguardLoginResponse_ErrorMessage{ErrorMessage: fmt.Sprintf("Wix login failed: %s", err.Error())},
		}, nil
	}

	clientGuid := uuid.New()
	clientSecret, err := diceware.Generate(5)
	if err != nil {
		s.logger.Error("Failed to generate client secret", "Error", err)
		return &pb.ServerVanguardLoginResponse{
			Success:     false,
			LoginResult: &pb.ServerVanguardLoginResponse_ErrorMessage{ErrorMessage: "Failed to generate client secret"},
		}, nil
	}

	s.mu.Lock()
	// TODO: Remove old clients that are expired
	s.authenticatingClients = append(s.authenticatingClients, &AuthenticatingClient{
		ClientId:       clientGuid.String(),
		Secret:         strings.Join(clientSecret, "-"),
		Expires:        time.Now().Add(5 * time.Minute),
		AvailableRoles: loginResponse.Data.AvailableRoles,
		AvailableUnits: loginResponse.Data.AvailableUnits,
	})
	s.mu.Unlock()

	var availableRoles []*pb.RoleSelection
	for _, role := range loginResponse.Data.AvailableRoles {
		availableRoles = append(availableRoles, &pb.RoleSelection{
			Id:   uint32(role),
			Name: utils.SrsRoleNameMap[role],
		})
	}
	var availableUnits []*pb.UnitSelection
	for _, unit := range loginResponse.Data.AvailableUnits {
		availableUnits = append(availableUnits, &pb.UnitSelection{
			UnitId:   unit.UnitId,
			UnitName: unit.Name,
		})
	}

	var availableCoalitions []*pb.CoalitionSelection
	s.settingsState.RLock()
	for _, coalition := range s.settingsState.Coalitions {
		availableCoalitions = append(availableCoalitions, &pb.CoalitionSelection{
			Name:        coalition.Name,
			Color:       coalition.Color,
			Description: coalition.Description,
		})
	}
	s.settingsState.RUnlock()

	return &pb.ServerVanguardLoginResponse{
		Success: true,
		LoginResult: &pb.ServerVanguardLoginResponse_Result{
			Result: &pb.VanguardLoginResult{
				Secret:              strings.Join(clientSecret, "-"),
				ClientGuid:          clientGuid.String(),
				AvailableRoles:      availableRoles,
				AvailableUnits:      availableUnits,
				AvailableCoalitions: availableCoalitions,
			},
		},
	}, nil
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

func (s *AuthServer) WixLogin(email, password string) (*WixLoginResponse, error) {
	s.logger.Debug("Starting Wix login", "Email", email)

	result, err := s.wixCircuitBreaker.Execute(func() (*WixLoginResponse, error) {
		reqBody, err := json.Marshal(WixLoginRequest{Email: email, Password: password})
		if err != nil {
			return nil, err
		}
		s.settingsState.RLock()
		url := fmt.Sprintf("%svcs_login?key=%s&token=%s", s.settingsState.Security.VanguardApiBaseUrl, s.settingsState.Security.VanguardApiKey, s.settingsState.Security.VanguardToken)
		s.settingsState.RUnlock()
		s.logger.Info("Wix login URL", "URL", url, "Body", string(reqBody))
		req, err := http.NewRequest("POST", url, bytes.NewBuffer(reqBody))
		if err != nil {
			return nil, err
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Content-Length", fmt.Sprintf("%d", len(reqBody)))
		req.Header.Set("Host", "profile.vngd.net")
		req.Header.Set("User-Agent", "vcs-srs-server/1.0")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}

		var wixResp WixLoginResponse
		if err := json.NewDecoder(resp.Body).Decode(&wixResp); err != nil {
			return nil, err
		}

		return &wixResp, nil
	})
	if err != nil {
		return nil, err
	}

	if !result.Success {
		return result, fmt.Errorf("wix login failed: %s", result.Message)
	}

	if result.Data == nil {
		return result, fmt.Errorf("wix login failed: %s", result.Message)
	}

	return result, nil
}
