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
	authpb "github.com/FPGSchiba/vcs-srs-server/vcsauthpb"
	"github.com/google/uuid"
	"github.com/sethvargo/go-diceware/diceware"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/peer"
)

type AuthServer struct {
	pb.UnimplementedAuthServiceServer
	logger                *slog.Logger
	serverState           *state.ServerState
	settingsState         *state.SettingsState
	distributionState     *state.DistributionState
	eventBus              *events.EventBus
	mu                    sync.RWMutex
	authenticatingClients map[uuid.UUID]*AuthenticatingClient
	pluginClients         map[string]*PluginClient
}

type AuthenticatingClient struct {
	Name           string
	Secret         string
	Expires        time.Time
	AvailableRoles []uint8
	AvailableUnits []*pb.UnitSelection
	SessionId      string
	PluginUsed     string
}

func NewAuthServer(serverState *state.ServerState, settingsState *state.SettingsState, logger *slog.Logger, distributionState *state.DistributionState, eventBus *events.EventBus) *AuthServer {
	return &AuthServer{
		serverState:           serverState,
		settingsState:         settingsState,
		eventBus:              eventBus,
		logger:                logger,
		mu:                    sync.RWMutex{},
		distributionState:     distributionState,
		pluginClients:         initializePluginClients(settingsState, logger),
		authenticatingClients: make(map[uuid.UUID]*AuthenticatingClient),
	}
}

func initializePluginClients(settingsState *state.SettingsState, logger *slog.Logger) map[string]*PluginClient {
	pluginClients := make(map[string]*PluginClient)
	for _, plugin := range settingsState.GetAllPluginNames() {
		var conf *state.FlowConfiguration
		var address string
		var ok bool
		if conf, ok = settingsState.GetPluginConfiguration(plugin); !ok || conf == nil {
			logger.Warn("Plugin configuration not found or empty", "name", plugin)
			continue
		}
		if address, ok = settingsState.GetPluginAddress(plugin); !ok || address == "" {
			logger.Warn("Plugin address not found or empty", "name", plugin)
			continue
		}
		client := NewPluginClient(logger, settingsState, plugin, address, conf)
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

func (s *AuthServer) InitAuth(ctx context.Context, request *pb.AuthInitRequest) (*pb.AuthInitResponse, error) {
	p, _ := peer.FromContext(ctx)
	s.logger.Debug("Initializing Auth", "IP", p.Addr.String(), "Version", request.Capabilities.Version)

	s.removeExpiredAuthenticatingClients()

	// TODO: Check if IP is banned

	// Check Version
	if !checkVersion(request.Capabilities.Version) {
		return &pb.AuthInitResponse{
			Success:    false,
			InitResult: &pb.AuthInitResponse_ErrorMessage{ErrorMessage: "Unsupported version"},
		}, nil
	}

	// Check Distribution Capabilities
	if !s.checkDistributionCapabilities(request.Capabilities.SupportedDistributionModes) {
		return &pb.AuthInitResponse{
			Success:    false,
			InitResult: &pb.AuthInitResponse_ErrorMessage{ErrorMessage: fmt.Sprintf("Unsupported distribution capabilities, currently running: %s", s.GetStringDistributionMode())},
		}, nil
	}

	clientGuid := uuid.New()
	s.mu.Lock()
	s.authenticatingClients[clientGuid] = &AuthenticatingClient{
		Expires: time.Now().Add(20 * time.Minute),
	}
	s.mu.Unlock()

	s.logger.Info("Client initialized", "ClientGuid", clientGuid, "IP", p.Addr.String(), "Version", request.Capabilities.Version)

	return &pb.AuthInitResponse{
		Success: true,
		InitResult: &pb.AuthInitResponse_Result{
			Result: &pb.AuthInitResult{
				DistributionMode: s.GetProtoDistributionMode(),
				AvailablePlugins: s.settingsState.GetAllPluginNames(),
				ClientGuid:       clientGuid.String(),
				HasGuestLogin:    s.settingsState.Security.EnableGuestAuth,
			},
		},
	}, nil
}

func (s *AuthServer) DiscoverAuthenticationFlows(ctx context.Context, request *pb.FlowDiscoveryRequest) (*pb.FlowDiscoveryResponse, error) {
	p, _ := peer.FromContext(ctx)
	s.logger.Debug("Getting Flow Discovery", "IP", p.Addr.String(), "Plugin", request.AuthenticationPlugin)

	// Check if This auth type is enabled
	s.settingsState.RLock()
	if !s.settingsState.Security.EnablePluginAuth {
		s.settingsState.RUnlock()
		return &pb.FlowDiscoveryResponse{
			Success:         false,
			DiscoveryResult: &pb.FlowDiscoveryResponse_ErrorMessage{ErrorMessage: "Plugin login is disabled"},
		}, nil
	}
	s.settingsState.RUnlock()

	s.removeExpiredAuthenticatingClients()

	// Check if plugin is available
	s.mu.RLock()
	pluginClient, ok := s.pluginClients[request.AuthenticationPlugin]
	if !ok {
		s.mu.RUnlock()
		s.logger.Warn("Plugin not found", "PluginName", request.AuthenticationPlugin)
		return &pb.FlowDiscoveryResponse{
			Success:         false,
			DiscoveryResult: &pb.FlowDiscoveryResponse_ErrorMessage{ErrorMessage: fmt.Sprintf("Plugin %s not found", request.AuthenticationPlugin)},
		}, nil
	}
	s.mu.RUnlock()

	// Call the plugin's flow discovery method
	flows, err := pluginClient.DiscoverPluginFlows()
	if err != nil {
		s.logger.Error("Plugin Flow Discovery failed", "plugin-name", request.AuthenticationPlugin, "Error", err)
		return &pb.FlowDiscoveryResponse{
			Success:         false,
			DiscoveryResult: &pb.FlowDiscoveryResponse_ErrorMessage{ErrorMessage: fmt.Sprintf("Flow discovery failed: %s", err.Error())},
		}, nil
	}

	return &pb.FlowDiscoveryResponse{
		Success: true,
		DiscoveryResult: &pb.FlowDiscoveryResponse_Result{
			Result: &pb.FlowDiscoveryResult{
				Flows: mapFlows(flows.Flows),
			},
		},
	}, nil
}

func mapFlows(in []*authpb.AuthFlowDefinition) []*pb.AuthFlowDefinition {
	outs := make([]*pb.AuthFlowDefinition, 0, len(in))
	for _, f := range in {
		outs = append(outs, &pb.AuthFlowDefinition{
			FlowId:      f.FlowId,
			Description: f.Description,
			Steps:       mapSteps(f.Steps),
			// ignore settings, as Client does not need them
		})
	}
	return outs
}

func mapSteps(in []*authpb.AuthStepDefinition) []*pb.AuthStepDefinition {
	outs := make([]*pb.AuthStepDefinition, 0, len(in))
	for _, s := range in {
		outs = append(outs, &pb.AuthStepDefinition{
			StepId:          s.StepId,
			StepName:        s.StepName,
			StepDescription: s.StepDescription,
			StepType:        s.StepType,
			RequiredFields:  mapFields(s.RequiredFields),
			Metadata:        s.Metadata, // map[string]string matches
		})
	}
	return outs
}

func mapFields(in []*authpb.FieldDefinition) []*pb.FieldDefinition {
	outs := make([]*pb.FieldDefinition, 0, len(in))
	for _, f := range in {
		outs = append(outs, &pb.FieldDefinition{
			FieldId:         f.Key,
			Label:           f.Label,
			Description:     f.Description,
			Type:            f.Type,
			Required:        f.Required,
			ValidationRegex: f.ValidationRegex,
			DefaultValue:    f.DefaultValue,
		})
	}
	return outs
}

func (s *AuthServer) GuestLogin(ctx context.Context, request *pb.GuestLoginRequest) (*pb.GuestLoginResponse, error) {
	p, _ := peer.FromContext(ctx)
	s.logger.Debug("Getting Guest Login", "IP", p.Addr.String(), "Name", request.Name, "UnitId", request.UnitId)

	// check if This auth type is enabled
	s.settingsState.RLock()
	if !s.settingsState.Security.EnableGuestAuth {
		s.settingsState.RUnlock()
		return &pb.GuestLoginResponse{
			Success:     false,
			LoginResult: &pb.GuestLoginResponse_ErrorMessage{ErrorMessage: "Guest login is disabled"},
		}, nil
	}
	s.settingsState.RUnlock()

	s.removeExpiredAuthenticatingClients()

	// Check if client is initialized
	clientGuid, err := uuid.Parse(request.ClientGuid)
	if err != nil {
		s.logger.Error("Failed to parse ClientGuid", "ClientGuid", request.ClientGuid, "Error", err)
		return &pb.GuestLoginResponse{
			Success:     false,
			LoginResult: &pb.GuestLoginResponse_ErrorMessage{ErrorMessage: "Invalid ClientGuid"},
		}, err
	}
	s.mu.RLock()
	if _, ok := s.authenticatingClients[clientGuid]; !ok {
		s.mu.RUnlock()
		return &pb.GuestLoginResponse{
			Success:     false,
			LoginResult: &pb.GuestLoginResponse_ErrorMessage{ErrorMessage: "ClientGuid not found, please initialize first"},
		}, nil
	}
	s.mu.RUnlock()

	// Check Username
	if !checkUsername(request.Name) {
		return &pb.GuestLoginResponse{
			Success:     false,
			LoginResult: &pb.GuestLoginResponse_ErrorMessage{ErrorMessage: "Invalid username"},
		}, nil
	}

	// Check Password > Select coalition
	s.mu.Lock()
	var selectedCoalition *state.Coalition
	for _, coalition := range s.settingsState.Coalitions {
		if utils.CheckPasswordHash(coalition.Password, request.Password) {
			s.mu.Unlock()
			selectedCoalition = &coalition
			break
		}
	}

	if selectedCoalition == nil {
		return &pb.GuestLoginResponse{
			Success:     false,
			LoginResult: &pb.GuestLoginResponse_ErrorMessage{ErrorMessage: "No Coalition found with that password"},
		}, nil
	}

	// Check Client UnitId
	if !checkUnitId(request.UnitId) {
		return &pb.GuestLoginResponse{
			Success:     false,
			LoginResult: &pb.GuestLoginResponse_ErrorMessage{ErrorMessage: "Invalid UnitId"},
		}, nil
	}

	// Add Client to State
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
		return &pb.GuestLoginResponse{
			Success:     false,
			LoginResult: &pb.GuestLoginResponse_ErrorMessage{ErrorMessage: "Failed to generate token"},
		}, err
	}

	s.logger.Info("guest login succeeded for ", "Guest Name", request.Name, "UnitId", request.UnitId, "Coalition", selectedCoalition.Name, "ClientGuid", clientGuid)
	s.eventBus.Publish(events.Event{
		Name: events.ClientsChanged,
		Data: s.serverState.Clients,
	})
	return &pb.GuestLoginResponse{
		Success: true,
		LoginResult: &pb.GuestLoginResponse_Result{
			Result: &pb.GuestLoginResult{
				Token:     token,
				Coalition: selectedCoalition.Name,
			},
		},
	}, nil
}

func (s *AuthServer) StartAuth(ctx context.Context, request *pb.StartAuthRequest) (*pb.AuthStepResponse, error) {
	p, _ := peer.FromContext(ctx)
	s.logger.Debug("Getting 3rd Party Plugin Login", "IP", p.Addr.String(), "plugin-name", request.AuthenticationPlugin)

	// Check if This auth type is enabled
	s.settingsState.RLock()
	if !s.settingsState.Security.EnablePluginAuth {
		s.settingsState.RUnlock()
		return &pb.AuthStepResponse{
			Success:   false,
			SessionId: "",
			Result:    &pb.AuthStepResponse_ErrorMessage{ErrorMessage: "Plugin login is disabled"},
		}, nil
	}
	s.settingsState.RUnlock()

	s.removeExpiredAuthenticatingClients()

	// Check if client is initialized
	clientGuid, err := uuid.Parse(request.ClientGuid)
	if err != nil {
		s.logger.Error("Failed to parse ClientGuid", "ClientGuid", request.ClientGuid, "Error", err)
		return &pb.AuthStepResponse{
			Success:   false,
			SessionId: "",
			Result:    &pb.AuthStepResponse_ErrorMessage{ErrorMessage: "Invalid ClientGuid"},
		}, err
	}
	s.mu.RLock()
	if _, ok := s.authenticatingClients[clientGuid]; !ok {
		s.mu.RUnlock()
		return &pb.AuthStepResponse{
			Success:   false,
			SessionId: "",
			Result:    &pb.AuthStepResponse_ErrorMessage{ErrorMessage: "ClientGuid not found, please initialize first"},
		}, nil
	}
	s.mu.RUnlock()

	// Check if the plugin is available
	s.mu.RLock()
	pluginClient, ok := s.pluginClients[request.AuthenticationPlugin]
	if !ok {
		s.mu.RUnlock()
		s.logger.Warn("Plugin not found", "PluginName", request.AuthenticationPlugin)
		return &pb.AuthStepResponse{
			Success:   false,
			SessionId: "",
			Result:    &pb.AuthStepResponse_ErrorMessage{ErrorMessage: fmt.Sprintf("Plugin %s not found", request.AuthenticationPlugin)},
		}, nil
	}
	s.mu.RUnlock()

	// Call the plugin's login method
	// This will return an error if the login fails
	loginResponse, err := pluginClient.StartAuth(request.FlowId, request.FirstStepInput)
	if err != nil {
		s.logger.Error("Plugin Login failed", "plugin-name", request.AuthenticationPlugin, "Error", err)
		return &pb.AuthStepResponse{
			Success:   false,
			SessionId: "",
			Result:    &pb.AuthStepResponse_ErrorMessage{ErrorMessage: fmt.Sprintf("Login failed: %s", err.Error())},
		}, nil
	}

	switch loginResponse.GetStatus() {
	case authpb.AuthStepStatus_AUTH_CONTINUE:
		s.mu.RLock()
		s.authenticatingClients[clientGuid].PluginUsed = request.AuthenticationPlugin
		s.authenticatingClients[clientGuid].SessionId = loginResponse.SessionId
		s.mu.RUnlock()
		return handleAuthContinue(loginResponse)
	case authpb.AuthStepStatus_AUTH_FAILED:
		errMsg := loginResponse.StepResult.(*authpb.AuthStepResponse_ErrorMessage)
		s.logger.Warn("Plugin Login failed", "plugin-name", request.AuthenticationPlugin, "Error", errMsg.ErrorMessage)
		return &pb.AuthStepResponse{
			Success:   false,
			SessionId: loginResponse.SessionId,
			Result:    &pb.AuthStepResponse_ErrorMessage{ErrorMessage: fmt.Sprintf("Login failed: %s", errMsg.ErrorMessage)},
		}, nil
	case authpb.AuthStepStatus_AUTH_COMPLETE:
		return s.handleAuthComplete(clientGuid, loginResponse)
	default:
		s.logger.Error("Plugin returned unknown status", "plugin-name", request.AuthenticationPlugin, "Status", loginResponse.GetStatus())
		return &pb.AuthStepResponse{
			Success:   false,
			SessionId: loginResponse.SessionId,
			Result:    &pb.AuthStepResponse_ErrorMessage{ErrorMessage: "Plugin returned unknown status"},
		}, nil
	}
}

func (s *AuthServer) handleAuthComplete(clientGuid uuid.UUID, response *authpb.AuthStepResponse) (*pb.AuthStepResponse, error) {
	clientSecret, err := diceware.Generate(5)
	if err != nil {
		s.logger.Error("Failed to generate client secret", "Error", err)
		return &pb.AuthStepResponse{
			Success: false,
			Result:  &pb.AuthStepResponse_ErrorMessage{ErrorMessage: "Failed to generate client secret"},
		}, nil
	}

	result := response.StepResult.(*authpb.AuthStepResponse_Complete).Complete
	var availableRoles []uint8
	for _, role := range result.AvailableRoles {
		availableRoles = append(availableRoles, uint8(role))
	}
	var availableUnits []*pb.UnitSelection
	for _, unit := range result.AvailableUnits {
		availableUnits = append(availableUnits, &pb.UnitSelection{
			UnitId:   unit.UnitId,
			UnitName: unit.UnitName,
		})
	}
	s.mu.Lock()
	s.authenticatingClients[clientGuid] = &AuthenticatingClient{
		Name:           result.PlayerName,
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
	for _, role := range result.AvailableRoles {
		builtRoles = append(builtRoles, &pb.RoleSelection{
			Id:   role,
			Name: utils.SrsRoleNameMap[uint8(role)],
		})
	}

	return &pb.AuthStepResponse{
		Success:   true,
		SessionId: response.SessionId,
		Result: &pb.AuthStepResponse_Complete{
			Complete: &pb.LoginResult{
				Secret:              strings.Join(clientSecret, "-"),
				AvailableRoles:      builtRoles,
				AvailableUnits:      availableUnits,
				AvailableCoalitions: availableCoalitions,
				PlayerName:          result.PlayerName,
			},
		},
	}, nil
}

func handleAuthContinue(response *authpb.AuthStepResponse) (*pb.AuthStepResponse, error) {
	return transformAuthStepResponse(response), nil
}

func transformAuthStepResponse(response *authpb.AuthStepResponse) *pb.AuthStepResponse {
	if response.GetStatus() == authpb.AuthStepStatus_AUTH_CONTINUE {
		return &pb.AuthStepResponse{
			Success:   true,
			SessionId: response.SessionId,
			Result: &pb.AuthStepResponse_NextStep{
				NextStep: &pb.NextStepRequired{
					StepId:          response.GetNextStep().GetStepId(),
					StepName:        response.GetNextStep().GetStepName(),
					StepDescription: response.GetNextStep().GetStepDescription(),
					RequiredFields:  transformFieldDefinitions(response.GetNextStep().GetRequiredFields()),
					Metadata:        response.GetNextStep().GetMetadata(),
				},
			},
		}
	} else {
		return nil // Ignore all other cases
	}
}

func transformFieldDefinitions(fields []*authpb.FieldDefinition) []*pb.FieldDefinition {
	var transformedFields []*pb.FieldDefinition
	for _, field := range fields {
		transformedFields = append(transformedFields, &pb.FieldDefinition{
			FieldId:         field.Key,
			Label:           field.Label,
			Description:     field.Description,
			Type:            field.Type,
			Required:        field.Required,
			DefaultValue:    field.DefaultValue,
			ValidationRegex: field.ValidationRegex,
		})
	}
	return transformedFields
}

func (s *AuthServer) ContinueAuth(ctx context.Context, request *pb.ContinueAuthRequest) (*pb.AuthStepResponse, error) {
	p, _ := peer.FromContext(ctx)
	s.logger.Debug("Continuing 3rd Party Plugin Login", "IP", p.Addr.String(), "SessionId", request.SessionId)

	// Check if This auth type is enabled
	s.settingsState.RLock()
	if !s.settingsState.Security.EnablePluginAuth {
		s.settingsState.RUnlock()
		return &pb.AuthStepResponse{
			Success:   false,
			SessionId: "",
			Result:    &pb.AuthStepResponse_ErrorMessage{ErrorMessage: "Plugin login is disabled"},
		}, nil
	}
	s.settingsState.RUnlock()

	s.removeExpiredAuthenticatingClients()
	// Check if client is initialized
	clientGuid, err := uuid.Parse(request.ClientGuid)
	if err != nil {
		s.logger.Error("Failed to parse ClientGuid", "ClientGuid", request.ClientGuid, "Error", err)
		return &pb.AuthStepResponse{
			Success:   false,
			SessionId: "",
			Result:    &pb.AuthStepResponse_ErrorMessage{ErrorMessage: "Invalid ClientGuid"},
		}, err
	}
	s.mu.RLock()
	authenticatedClient, ok := s.authenticatingClients[clientGuid]
	if !ok {
		s.mu.RUnlock()
		return &pb.AuthStepResponse{
			Success:   false,
			SessionId: "",
			Result:    &pb.AuthStepResponse_ErrorMessage{ErrorMessage: "ClientGuid not found, please initialize first"},
		}, nil
	}
	s.mu.RUnlock()
	if authenticatedClient.SessionId != request.SessionId {
		return &pb.AuthStepResponse{
			Success:   false,
			SessionId: "",
			Result:    &pb.AuthStepResponse_ErrorMessage{ErrorMessage: "SessionId does not match, please start authentication again"},
		}, nil
	}

	// Check if the plugin is available
	s.mu.RLock()
	pluginClient, ok := s.pluginClients[authenticatedClient.PluginUsed]
	if !ok {
		s.mu.RUnlock()
		s.logger.Warn("Plugin not found", "PluginName", authenticatedClient.PluginUsed)
		return &pb.AuthStepResponse{
			Success:   false,
			SessionId: "",
			Result:    &pb.AuthStepResponse_ErrorMessage{ErrorMessage: fmt.Sprintf("Plugin %s not found", authenticatedClient.PluginUsed)},
		}, nil
	}
	s.mu.RUnlock()

	// Call the plugin's continue method
	loginResponse, err := pluginClient.ContinueAuth(request.SessionId, request.StepData)
	if err != nil {
		s.logger.Error("Plugin ContinueAuth failed", "plugin-name", authenticatedClient.PluginUsed, "Error", err)
		return &pb.AuthStepResponse{
			Success:   false,
			SessionId: "",
			Result:    &pb.AuthStepResponse_ErrorMessage{ErrorMessage: fmt.Sprintf("ContinueAuth failed: %s", err.Error())},
		}, nil
	}

	switch loginResponse.GetStatus() {
	case authpb.AuthStepStatus_AUTH_CONTINUE:
		return handleAuthContinue(loginResponse)
	case authpb.AuthStepStatus_AUTH_FAILED:
		errMsg := loginResponse.StepResult.(*authpb.AuthStepResponse_ErrorMessage)
		s.logger.Warn("Plugin ContinueAuth failed", "plugin-name", authenticatedClient.PluginUsed, "Error", errMsg.ErrorMessage)
		return &pb.AuthStepResponse{
			Success:   false,
			SessionId: loginResponse.SessionId,
			Result:    &pb.AuthStepResponse_ErrorMessage{ErrorMessage: fmt.Sprintf("ContinueAuth failed: %s", errMsg.ErrorMessage)},
		}, nil
	case authpb.AuthStepStatus_AUTH_COMPLETE:
		return s.handleAuthComplete(clientGuid, loginResponse)
	default:
		s.logger.Error("Plugin returned unknown status", "plugin-name", authenticatedClient.PluginUsed, "Status", loginResponse.GetStatus())
		return &pb.AuthStepResponse{
			Success:   false,
			SessionId: loginResponse.SessionId,
			Result:    &pb.AuthStepResponse_ErrorMessage{ErrorMessage: "Plugin returned unknown status"},
		}, nil
	}
}

func (s *AuthServer) UnitSelect(ctx context.Context, request *pb.UnitSelectRequest) (*pb.UnitSelectResponse, error) {
	p, _ := peer.FromContext(ctx)
	s.logger.Debug("Processing Unit Select", "IP", p.Addr.String(), "ClientGuid", request.ClientGuid, "UnitId", request.UnitId)

	// Find the authenticating client
	clientGuid, err := uuid.Parse(request.ClientGuid)
	if err != nil {
		s.logger.Error("Failed to parse ClientGuid", "ClientGuid", request.ClientGuid, "Error", err)
		return &pb.UnitSelectResponse{
			Success: false,
			Result:  &pb.UnitSelectResponse_ErrorMessage{ErrorMessage: "Invalid ClientGuid"},
		}, err
	}
	s.mu.RLock()
	authClient, ok := s.authenticatingClients[clientGuid]
	if !ok {
		s.mu.RUnlock()
		s.logger.Warn("Client not found for Unit Select", "ClientGuid", request.ClientGuid)
		return &pb.UnitSelectResponse{
			Success: false,
			Result:  &pb.UnitSelectResponse_ErrorMessage{ErrorMessage: "ClientGuid not found, please initialize first"},
		}, nil
	}
	s.mu.RUnlock()

	// Check secret
	if authClient == nil || authClient.Secret != request.Secret {
		s.logger.Warn("Authentication failed for Unit Select", "ClientGuid", request.ClientGuid, "UnitId", request.UnitId)
		return &pb.UnitSelectResponse{
			Success: false,
			Result:  &pb.UnitSelectResponse_ErrorMessage{ErrorMessage: "Problem verifying client"},
		}, nil
	}

	// Check if the selected unit is available for the client
	selectedUnit := getSelectedUnit(authClient, request.UnitId)
	if selectedUnit == nil {
		// Other UnitIds should also be valid, so we check if the UnitId is valid
		if !checkUnitId(request.UnitId) {
			return &pb.UnitSelectResponse{
				Success: false,
				Result:  &pb.UnitSelectResponse_ErrorMessage{ErrorMessage: "Invalid UnitId"},
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
		return &pb.UnitSelectResponse{
			Success: false,
			Result:  &pb.UnitSelectResponse_ErrorMessage{ErrorMessage: "Invalid Role"},
		}, nil
	}

	// Check if the coalition is available
	if !s.isCoalitionAvailable(request.Coalition) {
		return &pb.UnitSelectResponse{
			Success: false,
			Result:  &pb.UnitSelectResponse_ErrorMessage{ErrorMessage: "Invalid Coalition"},
		}, nil
	}

	s.serverState.AddClient(clientGuid, &state.ClientState{
		Name:      authClient.Name,
		UnitId:    selectedUnit.UnitId,
		Coalition: request.Coalition,
		Role:      uint8(request.Role),
	})

	s.mu.Lock()
	delete(s.authenticatingClients, clientGuid)
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
		return &pb.UnitSelectResponse{
			Success: false,
			Result:  &pb.UnitSelectResponse_ErrorMessage{ErrorMessage: "Failed to generate token"},
		}, err
	}

	s.eventBus.Publish(events.Event{
		Name: events.ClientsChanged,
		Data: s.serverState.Clients,
	})

	return &pb.UnitSelectResponse{
		Success: true,
		Result:  &pb.UnitSelectResponse_Token{Token: token},
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
