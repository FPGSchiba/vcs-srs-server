package srs

import (
	"context"
	"fmt"
	"log/slog"
	"net"
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

	// Pending-login bookkeeping (SRV-17). All guarded by mu.
	pendingPerIP    map[string]int
	now             func() time.Time
	maxPending      int
	maxPendingPerIP int

	stopSweep chan struct{}
	sweepDone chan struct{}
	stopOnce  sync.Once
}

// Pending-login limits. A pending login is an InitAuth whose GUID has not yet
// been redeemed by GuestLogin or UnitSelect. Without a bound, anyone who can
// reach the port can grow authenticatingClients without limit.
const (
	// pendingLoginTTL is how long a pending login lives without activity.
	// Every login step that finds a live entry re-arms it.
	pendingLoginTTL = 5 * time.Minute
	// maxPendingLogins caps the whole map. Over it, a new InitAuth is refused;
	// existing entries are never evicted.
	maxPendingLogins = 10000
	// maxPendingLoginsPerIP caps the entries one peer host may hold.
	maxPendingLoginsPerIP = 32
	// pendingSweepInterval is how often expired entries are removed in bulk.
	pendingSweepInterval = 60 * time.Second
)

type AuthenticatingClient struct {
	Name           string
	Secret         string
	Expires        time.Time
	AvailableRoles []uint8
	AvailableUnits []*pb.UnitSelection
	SessionId      string
	PluginUsed     string
	// IP is the peer host that ran InitAuth; it keys pendingPerIP and must be
	// preserved across in-place updates or the per-IP count drifts.
	IP string
}

func NewAuthServer(serverState *state.ServerState, settingsState *state.SettingsState, logger *slog.Logger, distributionState *state.DistributionState, eventBus *events.EventBus) *AuthServer {
	s := &AuthServer{
		serverState:           serverState,
		settingsState:         settingsState,
		eventBus:              eventBus,
		logger:                logger,
		mu:                    sync.RWMutex{},
		distributionState:     distributionState,
		pluginClients:         initializePluginClients(settingsState, logger),
		authenticatingClients: make(map[uuid.UUID]*AuthenticatingClient),
		pendingPerIP:          make(map[string]int),
		now:                   time.Now,
		maxPending:            maxPendingLogins,
		maxPendingPerIP:       maxPendingLoginsPerIP,
		stopSweep:             make(chan struct{}),
		sweepDone:             make(chan struct{}),
	}
	go s.runSweeper()
	return s
}

// runSweeper removes expired pending logins every pendingSweepInterval until
// Stop is called.
func (s *AuthServer) runSweeper() {
	ticker := time.NewTicker(pendingSweepInterval)
	defer func() {
		ticker.Stop()
		close(s.sweepDone)
	}()
	for {
		select {
		case <-ticker.C:
			s.sweepExpired()
		case <-s.stopSweep:
			return
		}
	}
}

// Stop ends the pending-login sweeper and waits for it to exit. It is safe to
// call more than once.
func (s *AuthServer) Stop() {
	s.stopOnce.Do(func() {
		close(s.stopSweep)
	})
	<-s.sweepDone
}

// sweepExpired deletes every expired pending login in one pass.
func (s *AuthServer) sweepExpired() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for guid, c := range s.authenticatingClients {
		if now.After(c.Expires) {
			s.deletePendingLocked(guid)
			s.logger.Info("Removed expired authenticating client", "ClientGuid", guid)
		}
	}
}

// deletePendingLocked removes a pending login and releases its per-IP slot.
// The caller holds s.mu for writing.
func (s *AuthServer) deletePendingLocked(guid uuid.UUID) {
	c, ok := s.authenticatingClients[guid]
	if !ok {
		return
	}
	delete(s.authenticatingClients, guid)
	if n := s.pendingPerIP[c.IP] - 1; n > 0 {
		s.pendingPerIP[c.IP] = n
	} else {
		delete(s.pendingPerIP, c.IP)
	}
}

// touchPending looks up a pending login for a login step. A missing or expired
// entry yields false (an expired one is deleted). A live one has its TTL
// re-armed and is returned as a value copy, so callers never read shared
// fields outside the lock.
func (s *AuthServer) touchPending(guid uuid.UUID) (AuthenticatingClient, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.authenticatingClients[guid]
	if !ok {
		return AuthenticatingClient{}, false
	}
	now := s.now()
	if now.After(c.Expires) {
		s.deletePendingLocked(guid)
		s.logger.Info("Removed expired authenticating client", "ClientGuid", guid)
		return AuthenticatingClient{}, false
	}
	c.Expires = now.Add(pendingLoginTTL)
	return *c, true
}

// setPluginSession records the plugin flow on a pending login in place. It
// returns false if the entry is gone (swept or consumed meanwhile).
func (s *AuthServer) setPluginSession(guid uuid.UUID, plugin, sessionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.authenticatingClients[guid]
	if !ok {
		return false
	}
	c.PluginUsed = plugin
	c.SessionId = sessionID
	c.Expires = s.now().Add(pendingLoginTTL)
	return true
}

// completePending stores a completed plugin login on the pending entry in
// place, keeping IP so the per-IP count stays correct. It returns false if the
// entry is gone.
func (s *AuthServer) completePending(guid uuid.UUID, name, secret string, roles []uint8, units []*pb.UnitSelection) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.authenticatingClients[guid]
	if !ok {
		return false
	}
	c.Name = name
	c.Secret = secret
	c.AvailableRoles = roles
	c.AvailableUnits = units
	c.Expires = s.now().Add(pendingLoginTTL)
	return true
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
		certFile, _ := settingsState.GetPluginCertificateFile(plugin)
		client := NewPluginClient(logger, settingsState, plugin, address, certFile, conf)
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

// peerAddr returns the caller's address and host for logging and per-address
// checks. A context without a peer (never the case for a real gRPC call, but
// possible from tests or in-process callers) yields ("unknown", "") instead of
// a nil-pointer panic.
func peerAddr(ctx context.Context) (addr, host string) {
	p, ok := peer.FromContext(ctx)
	if !ok || p == nil || p.Addr == nil {
		return "unknown", ""
	}
	addr = p.Addr.String()
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr, ""
	}
	return addr, host
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
	peerAddress, peerHost := peerAddr(ctx)
	capabilities := request.GetCapabilities()
	if capabilities == nil {
		s.logger.Warn("InitAuth without client capabilities", "IP", peerAddress)
		return &pb.AuthInitResponse{
			Success:    false,
			InitResult: &pb.AuthInitResponse_ErrorMessage{ErrorMessage: "Missing client capabilities"},
		}, nil
	}
	s.logger.Debug("Initializing Auth", "IP", peerAddress, "Version", capabilities.GetVersion())

	s.serverState.RLock()
	_, banned := utils.FindByFunc(s.serverState.BannedState.BannedClients, func(bc state.BannedClient) bool {
		return bc.IPAddress == peerHost
	})
	s.serverState.RUnlock()
	if banned {
		s.logger.Warn("Banned client attempted to initialize", "IP", peerAddress)
		return &pb.AuthInitResponse{
			Success:    false,
			InitResult: &pb.AuthInitResponse_ErrorMessage{ErrorMessage: "You are banned from this server"},
		}, nil
	}

	// Check Version. The message names both the version we got and the floor
	// we require -- "Unsupported version" alone gives a user nothing to act
	// on, and this is the first thing a mismatched client ever sees.
	if !checkVersion(capabilities.GetVersion()) {
		return &pb.AuthInitResponse{
			Success: false,
			InitResult: &pb.AuthInitResponse_ErrorMessage{ErrorMessage: fmt.Sprintf(
				"Unsupported client version %q; this server requires %s or newer",
				capabilities.GetVersion(), MinClientVersion)},
		}, nil
	}

	// Check Distribution Capabilities
	if !s.checkDistributionCapabilities(capabilities.GetSupportedDistributionModes()) {
		return &pb.AuthInitResponse{
			Success:    false,
			InitResult: &pb.AuthInitResponse_ErrorMessage{ErrorMessage: fmt.Sprintf("Unsupported distribution capabilities, currently running: %s", s.GetStringDistributionMode())},
		}, nil
	}

	clientGuid := uuid.New()
	s.mu.Lock()
	if len(s.authenticatingClients) >= s.maxPending {
		s.mu.Unlock()
		s.logger.Warn("Refusing InitAuth: pending-login limit reached", "IP", peerAddress, "limit", s.maxPending)
		return &pb.AuthInitResponse{
			Success:    false,
			InitResult: &pb.AuthInitResponse_ErrorMessage{ErrorMessage: "Server is busy: too many pending logins, please try again shortly"},
		}, nil
	}
	if s.pendingPerIP[peerHost] >= s.maxPendingPerIP {
		s.mu.Unlock()
		s.logger.Warn("Refusing InitAuth: per-address pending-login limit reached", "IP", peerAddress, "limit", s.maxPendingPerIP)
		return &pb.AuthInitResponse{
			Success:    false,
			InitResult: &pb.AuthInitResponse_ErrorMessage{ErrorMessage: "Too many pending logins from your address, please try again shortly"},
		}, nil
	}
	s.authenticatingClients[clientGuid] = &AuthenticatingClient{
		IP:      peerHost,
		Expires: s.now().Add(pendingLoginTTL),
	}
	s.pendingPerIP[peerHost]++
	s.mu.Unlock()

	s.logger.Info("Client initialized", "ClientGuid", clientGuid, "IP", peerAddress, "Version", capabilities.GetVersion())

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
	peerAddress, _ := peerAddr(ctx)
	s.logger.Debug("Getting Flow Discovery", "IP", peerAddress, "Plugin", request.AuthenticationPlugin)

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
	peerAddress, _ := peerAddr(ctx)
	s.logger.Debug("Getting Guest Login", "IP", peerAddress, "Name", request.Name, "UnitId", request.UnitId)

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

	// Check if client is initialized
	clientGuid, err := uuid.Parse(request.ClientGuid)
	if err != nil {
		s.logger.Error("Failed to parse ClientGuid", "ClientGuid", request.ClientGuid, "Error", err)
		return &pb.GuestLoginResponse{
			Success:     false,
			LoginResult: &pb.GuestLoginResponse_ErrorMessage{ErrorMessage: "Invalid ClientGuid"},
		}, nil
	}
	if _, ok := s.touchPending(clientGuid); !ok {
		return &pb.GuestLoginResponse{
			Success:     false,
			LoginResult: &pb.GuestLoginResponse_ErrorMessage{ErrorMessage: "ClientGuid not found, please initialize first"},
		}, nil
	}

	// Check Username
	if !checkUsername(request.Name) {
		return &pb.GuestLoginResponse{
			Success:     false,
			LoginResult: &pb.GuestLoginResponse_ErrorMessage{ErrorMessage: "Invalid username"},
		}, nil
	}

	// Check Password > Select coalition
	s.settingsState.RLock()
	var selectedCoalition state.Coalition
	var coalitionFound bool
	for _, coalition := range s.settingsState.Coalitions {
		if utils.CheckPasswordHash(request.Password, coalition.Password) { // Password comes in hashed and we have the plaintext
			selectedCoalition = coalition
			coalitionFound = true
			break
		}
	}
	s.settingsState.RUnlock()

	if !coalitionFound {
		return &pb.GuestLoginResponse{
			Success:     false,
			LoginResult: &pb.GuestLoginResponse_ErrorMessage{ErrorMessage: "No coalition found with that password"},
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

	s.mu.Lock()
	s.deletePendingLocked(clientGuid)
	s.mu.Unlock()

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
		}, nil
	}

	s.logger.Info("guest login succeeded for ", "Guest Name", request.Name, "UnitId", request.UnitId, "Coalition", selectedCoalition.Name, "ClientGuid", clientGuid)
	s.serverState.RLock()
	clientsSnap := make(map[uuid.UUID]*state.ClientState, len(s.serverState.Clients))
	for k, v := range s.serverState.Clients {
		clientsSnap[k] = v
	}
	s.serverState.RUnlock()
	s.eventBus.Publish(events.Event{
		Name: events.ClientsChanged,
		Data: events.ClientChangeEvent{Type: events.ClientJoined, ClientID: clientGuid, Clients: clientsSnap},
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
	peerAddress, _ := peerAddr(ctx)
	s.logger.Debug("Getting 3rd Party Plugin Login", "IP", peerAddress, "plugin-name", request.AuthenticationPlugin)

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

	// Check if client is initialized
	clientGuid, err := uuid.Parse(request.ClientGuid)
	if err != nil {
		s.logger.Error("Failed to parse ClientGuid", "ClientGuid", request.ClientGuid, "Error", err)
		return &pb.AuthStepResponse{
			Success:   false,
			SessionId: "",
			Result:    &pb.AuthStepResponse_ErrorMessage{ErrorMessage: "Invalid ClientGuid"},
		}, nil
	}
	if _, ok := s.touchPending(clientGuid); !ok {
		return pendingNotFoundStepResponse(), nil
	}

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
		// The plugin call ran without the lock; the entry may have been swept
		// or consumed meanwhile.
		if !s.setPluginSession(clientGuid, request.AuthenticationPlugin, loginResponse.SessionId) {
			return pendingNotFoundStepResponse(), nil
		}
		return handleAuthContinue(loginResponse)
	case authpb.AuthStepStatus_AUTH_FAILED:
		errResult, ok := loginResponse.StepResult.(*authpb.AuthStepResponse_ErrorMessage)
		errMsgText := "unknown error"
		if ok && errResult != nil {
			errMsgText = errResult.ErrorMessage
		}
		s.logger.Warn("Plugin Login failed", "plugin-name", request.AuthenticationPlugin, "Error", errMsgText)
		return &pb.AuthStepResponse{
			Success:   false,
			SessionId: loginResponse.SessionId,
			Result:    &pb.AuthStepResponse_ErrorMessage{ErrorMessage: fmt.Sprintf("Login failed: %s", errMsgText)},
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

	completeResult, ok := response.StepResult.(*authpb.AuthStepResponse_Complete)
	if !ok || completeResult == nil || completeResult.Complete == nil {
		s.logger.Error("handleAuthComplete: plugin returned AUTH_COMPLETE but result payload is missing")
		return &pb.AuthStepResponse{
			Success: false,
			Result:  &pb.AuthStepResponse_ErrorMessage{ErrorMessage: "plugin returned AUTH_COMPLETE but result payload is missing"},
		}, nil
	}
	result := completeResult.Complete
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
	if !s.completePending(clientGuid, result.PlayerName, strings.Join(clientSecret, "-"), availableRoles, availableUnits) {
		return pendingNotFoundStepResponse(), nil
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

// pendingNotFoundStepResponse is the plugin-flow body for a GUID with no live
// pending login.
func pendingNotFoundStepResponse() *pb.AuthStepResponse {
	return &pb.AuthStepResponse{
		Success:   false,
		SessionId: "",
		Result:    &pb.AuthStepResponse_ErrorMessage{ErrorMessage: "ClientGuid not found, please initialize first"},
	}
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
	peerAddress, _ := peerAddr(ctx)
	s.logger.Debug("Continuing 3rd Party Plugin Login", "IP", peerAddress, "SessionId", request.SessionId)

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

	// Check if client is initialized
	clientGuid, err := uuid.Parse(request.ClientGuid)
	if err != nil {
		s.logger.Error("Failed to parse ClientGuid", "ClientGuid", request.ClientGuid, "Error", err)
		return &pb.AuthStepResponse{
			Success:   false,
			SessionId: "",
			Result:    &pb.AuthStepResponse_ErrorMessage{ErrorMessage: "Invalid ClientGuid"},
		}, nil
	}
	authenticatedClient, ok := s.touchPending(clientGuid)
	if !ok {
		return pendingNotFoundStepResponse(), nil
	}
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
		errResult, ok := loginResponse.StepResult.(*authpb.AuthStepResponse_ErrorMessage)
		errMsgText := "unknown error"
		if ok && errResult != nil {
			errMsgText = errResult.ErrorMessage
		}
		s.logger.Warn("Plugin ContinueAuth failed", "plugin-name", authenticatedClient.PluginUsed, "Error", errMsgText)
		return &pb.AuthStepResponse{
			Success:   false,
			SessionId: loginResponse.SessionId,
			Result:    &pb.AuthStepResponse_ErrorMessage{ErrorMessage: fmt.Sprintf("ContinueAuth failed: %s", errMsgText)},
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
	peerAddress, _ := peerAddr(ctx)
	s.logger.Debug("Processing Unit Select", "IP", peerAddress, "ClientGuid", request.ClientGuid, "UnitId", request.UnitId)

	// Find the authenticating client
	clientGuid, err := uuid.Parse(request.ClientGuid)
	if err != nil {
		s.logger.Error("Failed to parse ClientGuid", "ClientGuid", request.ClientGuid, "Error", err)
		return &pb.UnitSelectResponse{
			Success: false,
			Result:  &pb.UnitSelectResponse_ErrorMessage{ErrorMessage: "Invalid ClientGuid"},
		}, nil
	}
	pending, ok := s.touchPending(clientGuid)
	if !ok {
		s.logger.Warn("Client not found for Unit Select", "ClientGuid", request.ClientGuid)
		return &pb.UnitSelectResponse{
			Success: false,
			Result:  &pb.UnitSelectResponse_ErrorMessage{ErrorMessage: "ClientGuid not found, please initialize first"},
		}, nil
	}
	authClient := &pending

	// Check secret
	if authClient.Secret != request.Secret {
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
	s.deletePendingLocked(clientGuid)
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
		Data: events.ClientChangeEvent{Type: events.ClientJoined, ClientID: clientGuid, Clients: clientsSnap},
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
