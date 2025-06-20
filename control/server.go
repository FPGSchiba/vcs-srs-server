package control

import (
	"context"
	"errors"
	"fmt"
	"github.com/FPGSchiba/vcs-srs-server/srs"
	"github.com/FPGSchiba/vcs-srs-server/srspb"
	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/FPGSchiba/vcs-srs-server/utils"
	"github.com/FPGSchiba/vcs-srs-server/voicecontrolpb"
	"github.com/FPGSchiba/vcs-srs-server/voiceontrol"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/reflection"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"
)

var (
	sleep = time.Second * 5
)

const (
	healthServiceSystem  = ""        // empty string represents the health of the healthServiceSystem
	healthServiceSRS     = "srs"     // service name for SRS
	healthServiceControl = "control" // service name for Control Server
	healthServiceAuth    = "auth"    // service name for Auth Server
)

const (
	controlServerListeningIpAddress = "0.0.0.0" // Default address for control server
)

type Server struct {
	mu                sync.RWMutex
	clientGrpcServer  *grpc.Server
	controlGrpcServer *grpc.Server
	clientListener    net.Listener
	controlListener   net.Listener
	logger            *slog.Logger
	serverState       *state.ServerState
	settingsState     *state.SettingsState
	distributionState *state.DistributionState
	isRunning         bool
	stopOnce          sync.Once // Add this to ensure we only stop once
}

func NewServer(serverState *state.ServerState, settingsState *state.SettingsState, logger *slog.Logger, distributionState *state.DistributionState) *Server {
	return &Server{
		serverState:       serverState,
		settingsState:     settingsState,
		logger:            logger,
		distributionState: distributionState,
	}
}

func (s *Server) isControlServer() bool {
	s.distributionState.RLock()
	defer s.distributionState.RUnlock()
	return s.distributionState.DistributionMode == state.DistributionModeControl
}

func (s *Server) Start(address string, stopChan chan struct{}) error {
	if s.IsRunning() {
		return fmt.Errorf("server is already running")
	}

	var err error

	s.mu.Lock()
	s.clientListener, err = net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("failed to listen: %v", err)
	}

	if s.isControlServer() {
		s.controlListener, err = net.Listen("tcp", fmt.Sprintf("%s:%d", controlServerListeningIpAddress, voiceontrol.DefaultVoiceControlPort))
		if err != nil {
			return fmt.Errorf("failed to listen on control server address: %v", err)
		}
	}

	s.clientGrpcServer = grpc.NewServer(
		grpc.ChainUnaryInterceptor(s.loggingInterceptor, s.authInterceptor),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             60 * time.Second, // allow pings every 60s
			PermitWithoutStream: true,
		}),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    60 * time.Second, // server sends pings every 30s if idle
			Timeout: 10 * time.Second, // wait 10s for ping ack
		}),
	)

	srsServer := srs.NewSimpleRadioServer(s.serverState, s.settingsState, s.logger)
	authServer := srs.NewAuthServer(s.serverState, s.settingsState, s.logger, s.distributionState)
	srspb.RegisterSRSServiceServer(s.clientGrpcServer, srsServer)
	srspb.RegisterAuthServiceServer(s.clientGrpcServer, authServer)

	controlServer := voiceontrol.NewVoiceControlServer(s.serverState, s.settingsState, s.logger)

	if s.isControlServer() {
		s.initControlServer(controlServer)
	}

	healthServer := NewFullHealthServer()
	healthpb.RegisterHealthServer(s.clientGrpcServer, healthServer)
	if s.isControlServer() {
		healthpb.RegisterHealthServer(s.controlGrpcServer, healthServer)
	}

	go s.monitorHealth(srsServer, authServer, controlServer, healthServer)

	reflection.Register(s.clientGrpcServer)

	s.isRunning = true
	s.mu.Unlock()

	go s.serveClient(address)
	if s.isControlServer() {
		go s.serveControl()
	}

	go s.handleShutdown(stopChan)

	return nil
}

func (s *Server) initControlServer(controlServer voicecontrolpb.VoiceControlServiceServer) {
	cert, _, err := voiceontrol.LoadOrGenerateKeyPair()
	if err != nil {
		s.logger.Error("Failed to load TLS certificate for control server", "error", err)
		return
	}
	s.controlGrpcServer = grpc.NewServer(
		grpc.Creds(credentials.NewServerTLSFromCert(cert)),
		grpc.UnaryInterceptor(s.loggingInterceptor),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             10 * time.Second, // allow pings every 10s
			PermitWithoutStream: true,
		}),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    20 * time.Second, // server sends pings every 20s if idle
			Timeout: 10 * time.Second, // wait 10s for ping ack
		}),
	)
	voicecontrolpb.RegisterVoiceControlServiceServer(s.controlGrpcServer, controlServer)
	reflection.Register(s.controlGrpcServer)
}

func (s *Server) monitorHealth(
	srsServer *srs.SimpleRadioServer,
	authServer *srs.AuthServer,
	controlServer *voiceontrol.VoiceControlServer,
	healthServer *FullHealthServer,
) {
	for s.IsRunning() {
		srsStatus := srsServer.GetServerState()
		authStatus := authServer.GetServerState()
		healthServer.SetServingStatus(healthServiceSRS, srsStatus)
		healthServer.SetServingStatus(healthServiceAuth, authStatus)

		controlStatus := controlServer.GetServerState()
		if s.isControlServer() {
			healthServer.SetServingStatus(healthServiceControl, controlStatus)
		}

		if srsStatus == healthpb.HealthCheckResponse_SERVING &&
			controlStatus == healthpb.HealthCheckResponse_SERVING &&
			authStatus == healthpb.HealthCheckResponse_SERVING {
			healthServer.SetServingStatus(healthServiceSystem, healthpb.HealthCheckResponse_SERVING)
		} else {
			healthServer.SetServingStatus(healthServiceSystem, healthpb.HealthCheckResponse_NOT_SERVING)
		}

		time.Sleep(sleep)
	}
}

func (s *Server) serveClient(address string) {
	s.logger.Info("Starting VCS gRPC server", "address", address)
	if err := s.clientGrpcServer.Serve(s.clientListener); err != nil {
		if !errors.Is(err, grpc.ErrServerStopped) {
			s.logger.Error("gRPC server error", "error", err)
		}
		s.mu.Lock()
		s.isRunning = false
		s.mu.Unlock()
	}
}

func (s *Server) serveControl() {
	s.logger.Info("Starting Control gRPC server", "address", fmt.Sprintf("%s:%d", controlServerListeningIpAddress, voiceontrol.DefaultVoiceControlPort))
	if err := s.controlGrpcServer.Serve(s.controlListener); err != nil {
		if !errors.Is(err, grpc.ErrServerStopped) {
			s.logger.Error("gRPC server error", "error", err)
		}
		s.mu.Lock()
		s.isRunning = false
		s.mu.Unlock()
	}
}

func (s *Server) handleShutdown(stopChan chan struct{}) {
	<-stopChan
	s.logger.Info("Received stop signal for gRPC server")
	err := s.Stop()
	if err != nil {
		s.logger.Error("failed to stop gRPC server", "error", err)
	}
}

func (s *Server) Stop() error {
	var stopErr error

	s.stopOnce.Do(func() {
		if !s.IsRunning() {
			return
		}
		s.mu.Lock()
		s.isRunning = false
		s.mu.Unlock()

		s.logger.Info("Stopping gRPC server")

		// GracefulStop will automatically close the clientListener
		if s.clientGrpcServer != nil {
			s.clientGrpcServer.GracefulStop()
			s.clientGrpcServer = nil
		}

		if s.controlGrpcServer != nil {
			s.controlGrpcServer.GracefulStop()
			s.controlGrpcServer = nil
		}

		s.clientListener = nil // Just clear the reference, GracefulStop closes it
		s.controlListener = nil
	})

	return stopErr
}

func (s *Server) IsRunning() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.isRunning
}

// Logging interceptor for debugging
func (s *Server) loggingInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	s.logger.Debug("gRPC request", "method", info.FullMethod, "request", req)

	resp, err := handler(ctx, req)

	if err != nil {
		s.logger.Error("gRPC error",
			"method", info.FullMethod,
			"error", err)
	}

	return resp, err
}

func (s *Server) authInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	elements := strings.Split(info.FullMethod, "/")
	fullServiceName := elements[1] // Get the service name from the method path
	pathName := elements[2]        // Get the method name from the method path
	serviceName := strings.Split(fullServiceName, ".")[1]

	if serviceName == "AuthService" || serviceName == "VoiceControlService" {
		// Skip authentication for AuthService and VoiceControlService
		return handler(ctx, req)
	} else {
		// For other services, perform authentication
		s.logger.Debug("Authentication required for service", "service", serviceName, "method", info.FullMethod)
	}

	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, fmt.Errorf("unauthenticated request to %s: missing metadata", info.FullMethod)
	}

	tokens := md.Get("authorization") // Check for an "authorization" header
	if len(tokens) == 0 {
		return nil, fmt.Errorf("unauthenticated request to %s: missing authorization token", info.FullMethod)
	}
	token := strings.TrimPrefix(tokens[0], "Bearer ") // Remove "Bearer " prefix if present

	// Placeholder for authentication logic
	claims, err := utils.GetTokenClaims(token, utils.SrsServiceRoleMap[pathName]) // Replace with actual authentication check
	if err != nil {
		s.logger.Error("Authentication error", "method", info.FullMethod, "error", err)
		return nil, fmt.Errorf("authentication error for %s: %v", info.FullMethod, err)
	}

	if claims == nil {
		return nil, fmt.Errorf("unauthenticated request to %s", info.FullMethod)
	}

	md.Append("client_id", claims.ClientGuid)
	md.Append("role_id", fmt.Sprintf("%d", claims.RoleId))
	err = grpc.SetTrailer(ctx, md)
	if err != nil {
		return nil, fmt.Errorf("failed to set trailer: %v", err)
	}

	return handler(ctx, req)
}
