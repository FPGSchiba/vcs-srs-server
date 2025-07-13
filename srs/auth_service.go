package srs

import (
	"context"
	"fmt"
	pb "github.com/FPGSchiba/vcs-srs-server/srspb"
	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/FPGSchiba/vcs-srs-server/utils"
	authpb "github.com/FPGSchiba/vcs-srs-server/vcsauthpb"
	"github.com/google/uuid"
	"github.com/sethvargo/go-diceware/diceware"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/peer"
	"log/slog"
	"strings"
	"sync"
	"time"
)

type AuthServer struct {
	pb.UnimplementedAuthServiceServer
	logger                *slog.Logger
	serverState           *state.ServerState
	settingsState         *state.SettingsState
	distributionState     *state.DistributionState
	mu                    sync.RWMutex
	authenticatingClients map[string]*AuthenticatingClient
	pluginClients         map[string]*PluginClient
}

type AuthenticatingClient struct {
	Secret         string
	Expires        time.Time
	AvailableRoles []uint8
	AvailableUnits []*pb.UnitSelection
}

func NewAuthServer(serverState *state.ServerState, settingsState *state.SettingsState, logger *slog.Logger, distributionState *state.DistributionState) *AuthServer {
	return &AuthServer{
		serverState:           serverState,
		settingsState:         settingsState,
		logger:                logger,
		mu:                    sync.RWMutex{},
		distributionState:     distributionState,
		pluginClients:         initializePluginClients(settingsState, logger),
		authenticatingClients: make(map[string]*AuthenticatingClient),
	}
}

func initializePluginClients(settingsState *state.SettingsState, logger *slog.Logger) map[string]*PluginClient {
	pluginClients := make(map[string]*PluginClient)
	for _, plugin := range settingsState.GetAllPluginNames() {
		var configuration map[string]string
		var address string
		var ok bool
		if configuration, ok = settingsState.GetPluginConfiguration(plugin); !ok || configuration == nil {
			logger.Warn("Plugin configuration not found or empty", "name", plugin)
			continue
		}
		if address, ok = settingsState.GetPluginAddress(plugin); !ok || address == "" {
			logger.Warn("Plugin address not found or empty", "name", plugin)
			continue
		}
		client := NewPluginClient(logger, settingsState, plugin, address, configuration)
		if err := client.ConnectPlugin(); err != nil {
			logger.Error("Failed to connect to plugin", "name", plugin, "error", err)
			err := settingsState.SetPluginEnabled(plugin, false)
			if err != nil {
				logger.Error("Failed to disable plugin after connection failure", "name", plugin, "error", err)
			}
			continue
		}
		pluginClients[plugin] = client
	}
	return pluginClients
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

func (s *AuthServer) InitAuth(ctx context.Context, request *pb.ClientAuthInitRequest) (*pb.ServerAuthInitResponse, error) {
	p, _ := peer.FromContext(ctx)
	s.logger.Debug("Initializing Auth", "IP", p.Addr.String(), "Version", request.Capabilities.Version)

	s.removeExpiredAuthenticatingClients()

	// Check Version
	if !checkVersion(request.Capabilities.Version) {
		return &pb.ServerAuthInitResponse{
			Success:    false,
			InitResult: &pb.ServerAuthInitResponse_ErrorMessage{ErrorMessage: "Unsupported version"},
		}, nil
	}

	// Check Distribution Capabilities
	if !s.checkDistributionCapabilities(request.Capabilities.SupportedDistributionModes) {
		return &pb.ServerAuthInitResponse{
			Success:    false,
			InitResult: &pb.ServerAuthInitResponse_ErrorMessage{ErrorMessage: fmt.Sprintf("Unsupported distribution capabilities, currently running: %s", s.GetStringDistributionMode())},
		}, nil
	}

	clientGuid := uuid.New().String()
	s.mu.Lock()
	s.authenticatingClients[clientGuid] = &AuthenticatingClient{
		Expires: time.Now().Add(20 * time.Minute),
	}
	s.mu.Unlock()

	s.logger.Info("Client initialized", "ClientGuid", clientGuid, "IP", p.Addr.String(), "Version", request.Capabilities.Version)

	return &pb.ServerAuthInitResponse{
		Success: true,
		InitResult: &pb.ServerAuthInitResponse_Result{
			Result: &pb.AuthInitResult{
				DistributionMode: s.GetProtoDistributionMode(),
				AvailablePlugins: s.settingsState.GetAllPluginNames(),
				ClientGuid:       clientGuid,
				HasGuestLogin:    s.settingsState.Security.EnableGuestAuth,
			},
		},
	}, nil
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

	s.removeExpiredAuthenticatingClients()

	// Check if client is initialized
	s.mu.RLock()
	if _, ok := s.authenticatingClients[request.ClientGuid]; !ok {
		s.mu.RUnlock()
		return &pb.ServerGuestLoginResponse{
			Success:     false,
			LoginResult: &pb.ServerGuestLoginResponse_ErrorMessage{ErrorMessage: "ClientGuid not found, please initialize first"},
		}, nil
	}
	s.mu.RUnlock()

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
	s.serverState.AddClient(clientGuid, &state.ClientState{
		Name:      request.Name,
		UnitId:    request.UnitId,
		Coalition: selectedCoalition.Name,
		Role:      utils.GuestRole,
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
				Token:     token,
				Coalition: selectedCoalition.Name,
			},
		},
	}
	return response, nil
}

func (s *AuthServer) Login(ctx context.Context, request *pb.ClientLoginRequest) (*pb.ServerLoginResponse, error) {
	p, _ := peer.FromContext(ctx)
	s.logger.Debug("Getting 3rd Party Plugin Login", "IP", p.Addr.String(), "plugin-name", request.AuthenticationPlugin)

	// Check if This auth type is enabled
	s.settingsState.RLock()
	if !s.settingsState.Security.EnablePluginAuth {
		s.settingsState.RUnlock()
		return &pb.ServerLoginResponse{
			Success:     false,
			LoginResult: &pb.ServerLoginResponse_ErrorMessage{ErrorMessage: "Plugin login is disabled"},
		}, nil
	}
	s.settingsState.RUnlock()

	s.removeExpiredAuthenticatingClients()

	// Check if client is initialized
	s.mu.RLock()
	if _, ok := s.authenticatingClients[request.ClientGuid]; !ok {
		s.mu.RUnlock()
		return &pb.ServerLoginResponse{
			Success:     false,
			LoginResult: &pb.ServerLoginResponse_ErrorMessage{ErrorMessage: "ClientGuid not found, please initialize first"},
		}, nil
	}
	s.mu.RUnlock()

	// Check if the plugin is available
	s.mu.RLock()
	pluginClient, ok := s.pluginClients[request.AuthenticationPlugin]
	if !ok {
		s.mu.RUnlock()
		s.logger.Warn("Plugin not found", "PluginName", request.AuthenticationPlugin)
		return &pb.ServerLoginResponse{
			Success:     false,
			LoginResult: &pb.ServerLoginResponse_ErrorMessage{ErrorMessage: fmt.Sprintf("Plugin %s not found", request.AuthenticationPlugin)},
		}, nil
	}
	s.mu.RUnlock()

	// Call the plugin's login method
	// This will return an error if the login fails
	loginResponse, err := pluginClient.Login(request.Credentials)
	if err != nil {
		s.logger.Error("Plugin Login failed", "plugin-name", request.AuthenticationPlugin, "Error", err)
		return &pb.ServerLoginResponse{
			Success:     false,
			LoginResult: &pb.ServerLoginResponse_ErrorMessage{ErrorMessage: fmt.Sprintf("Login failed: %s", err.Error())},
		}, nil
	}

	clientSecret, err := diceware.Generate(5)
	if err != nil {
		s.logger.Error("Failed to generate client secret", "Error", err)
		return &pb.ServerLoginResponse{
			Success:     false,
			LoginResult: &pb.ServerLoginResponse_ErrorMessage{ErrorMessage: "Failed to generate client secret"},
		}, nil
	}
	result := loginResponse.LoginResult.(*authpb.ServerLoginResponse_Result)
	var availableRoles []uint8
	for _, role := range result.Result.AvailableRoles {
		availableRoles = append(availableRoles, uint8(role))
	}
	var availableUnits []*pb.UnitSelection
	for _, unit := range result.Result.AvailableUnits {
		availableUnits = append(availableUnits, &pb.UnitSelection{
			UnitId:   unit.UnitId,
			UnitName: unit.UnitName,
		})
	}
	s.mu.Lock()
	s.authenticatingClients[request.ClientGuid] = &AuthenticatingClient{
		Secret:         strings.Join(clientSecret, "-"),
		Expires:        time.Now().Add(5 * time.Minute),
		AvailableRoles: availableRoles,
		AvailableUnits: availableUnits,
	}
	s.mu.Unlock()

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

	var builtRoles []*pb.RoleSelection
	for _, role := range result.Result.AvailableRoles {
		builtRoles = append(builtRoles, &pb.RoleSelection{
			Id:   role,
			Name: utils.SrsRoleNameMap[uint8(role)],
		})
	}

	return &pb.ServerLoginResponse{
		Success: true,
		LoginResult: &pb.ServerLoginResponse_Result{
			Result: &pb.LoginResult{
				Secret:              strings.Join(clientSecret, "-"),
				AvailableRoles:      builtRoles,
				AvailableUnits:      availableUnits,
				AvailableCoalitions: availableCoalitions,
				PlayerName:          result.Result.PlayerName,
			},
		},
	}, nil
}

func (s *AuthServer) UnitSelect(ctx context.Context, request *pb.ClientUnitSelectRequest) (*pb.ServerUnitSelectResponse, error) {
	p, _ := peer.FromContext(ctx)
	s.logger.Debug("Processing Unit Select", "IP", p.Addr.String(), "ClientGuid", request.ClientGuid, "UnitId", request.UnitId)

	// Find the authenticating client
	s.mu.RLock()
	authClient, ok := s.authenticatingClients[request.ClientGuid]
	if !ok {
		s.mu.RUnlock()
		s.logger.Warn("Client not found for Unit Select", "ClientGuid", request.ClientGuid)
		return &pb.ServerUnitSelectResponse{
			Success: false,
			Result:  &pb.ServerUnitSelectResponse_ErrorMessage{ErrorMessage: "ClientGuid not found, please initialize first"},
		}, nil
	}
	s.mu.RUnlock()

	// Check secret
	if authClient == nil || authClient.Secret != request.Secret {
		s.logger.Warn("Authentication failed for Unit Select", "ClientGuid", request.ClientGuid, "UnitId", request.UnitId)
		return &pb.ServerUnitSelectResponse{
			Success: false,
			Result:  &pb.ServerUnitSelectResponse_ErrorMessage{ErrorMessage: "Problem verifying client"},
		}, nil
	}

	// Check if the selected unit is available for the client
	selectedUnit := getSelectedUnit(authClient, request.UnitId)
	if selectedUnit == nil {
		// Other UnitIds should also be valid, so we check if the UnitId is valid
		if !checkUnitId(request.UnitId) {
			return &pb.ServerUnitSelectResponse{
				Success: false,
				Result:  &pb.ServerUnitSelectResponse_ErrorMessage{ErrorMessage: "Invalid UnitId"},
			}, nil
		}
		// If the UnitId is not available, we create a new selection as only the UnitId is required
		selectedUnit = &pb.UnitSelection{
			UnitId:   request.UnitId,
			UnitName: fmt.Sprintf("Unit %s", request.UnitId),
		}
	}

	// Check if the role is available
	if !isRoleAvailable(authClient, uint8(request.Role)) {
		return &pb.ServerUnitSelectResponse{
			Success: false,
			Result:  &pb.ServerUnitSelectResponse_ErrorMessage{ErrorMessage: "Invalid Role"},
		}, nil
	}

	// Check if the coalition is available
	if !s.isCoalitionAvailable(request.Coalition) {
		return &pb.ServerUnitSelectResponse{
			Success: false,
			Result:  &pb.ServerUnitSelectResponse_ErrorMessage{ErrorMessage: "Invalid Coalition"},
		}, nil
	}

	clientGuid, err := uuid.Parse(request.ClientGuid)
	if err != nil {
		s.logger.Error("Failed to parse ClientGuid", "ClientGuid", request.ClientGuid, "Error", err)
		return &pb.ServerUnitSelectResponse{
			Success: false,
			Result:  &pb.ServerUnitSelectResponse_ErrorMessage{ErrorMessage: "Invalid ClientGuid"},
		}, err
	}

	s.serverState.AddClient(clientGuid, &state.ClientState{
		Name:      authClient.Secret,
		UnitId:    selectedUnit.UnitId,
		Coalition: request.Coalition,
		Role:      uint8(request.Role),
	})

	s.mu.Lock()
	delete(s.authenticatingClients, request.ClientGuid)
	s.mu.Unlock()

	// Generate token for the client
	s.settingsState.RLock()
	token, err := utils.GenerateToken(
		request.ClientGuid,
		uint8(request.Role),
		s.settingsState.Security.Token.Issuer,
		s.settingsState.Security.Token.Subject,
		time.Duration(s.settingsState.Security.Token.Expiration)*time.Second,
		s.settingsState.Security.Token.PrivateKeyFile,
		s.settingsState.Security.Token.PublicKeyFile)
	s.settingsState.RUnlock()

	if err != nil {
		s.logger.Error("Failed to generate token for guest login", "error", err)
		return &pb.ServerUnitSelectResponse{
			Success: false,
			Result:  &pb.ServerUnitSelectResponse_ErrorMessage{ErrorMessage: "Failed to generate token"},
		}, err
	}

	return &pb.ServerUnitSelectResponse{
		Success: true,
		Result:  &pb.ServerUnitSelectResponse_Token{Token: token},
	}, nil
}

func (s *AuthServer) checkDistributionCapabilities(features []pb.DistributionMode) bool {
	s.distributionState.RLock()
	currentDistributionMode := s.distributionState.DistributionMode
	s.distributionState.RUnlock()
	for _, feature := range features {
		switch feature {
		case pb.DistributionMode_DISTRIBUTED:
			if currentDistributionMode == state.DistributionModeControl {
				return true
			}
			continue
		case pb.DistributionMode_STANDALONE:
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

func (s *AuthServer) GetProtoDistributionMode() pb.DistributionMode {
	s.distributionState.RLock()
	defer s.distributionState.RUnlock()
	// This has to be a control server, so it either is distributed or standalone
	if s.distributionState.DistributionMode == state.DistributionModeControl {
		return pb.DistributionMode_DISTRIBUTED
	} else {
		return pb.DistributionMode_STANDALONE
	}
}

func (s *AuthServer) isCoalitionAvailable(selectedCoalition string) bool {
	var coalitionAvailable bool
	s.settingsState.RLock()
	defer s.settingsState.RUnlock()
	for _, coalition := range s.settingsState.Coalitions {
		if coalition.Name == selectedCoalition {
			coalitionAvailable = true
			break
		}
	}
	return coalitionAvailable
}

func (s *AuthServer) removeExpiredAuthenticatingClients() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for clientGuid, authClient := range s.authenticatingClients {
		if time.Now().After(authClient.Expires) {
			delete(s.authenticatingClients, clientGuid)
			s.logger.Info("Removed expired authenticating client", "ClientGuid", clientGuid)
		}
	}
}
