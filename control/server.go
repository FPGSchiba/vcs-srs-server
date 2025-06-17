package control

import (
	"context"
	"fmt"
	"github.com/FPGSchiba/vcs-srs-server/srs"
	"github.com/FPGSchiba/vcs-srs-server/srspb"
	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/FPGSchiba/vcs-srs-server/voicecontrolpb"
	"github.com/FPGSchiba/vcs-srs-server/voiceontrol"
	"google.golang.org/grpc"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
	"log/slog"
	"net"
	"sync"
	"time"
)

var (
	sleep = time.Second * 5

	system         = ""        // empty string represents the health of the system
	srsService     = "srs"     // service name for SRS
	controlService = "control" // service name for Control Server
)

type Server struct {
	mu              sync.RWMutex
	grpcServer      *grpc.Server
	listener        net.Listener
	logger          *slog.Logger
	serverState     *state.ServerState
	settingsState   *state.SettingsState
	isRunning       bool
	isControlServer bool      // Add this to indicate if this is a control server
	stopOnce        sync.Once // Add this to ensure we only stop once
}

func NewServer(serverState *state.ServerState, settingsState *state.SettingsState, logger *slog.Logger, isControlServer bool) *Server {
	return &Server{
		serverState:     serverState,
		settingsState:   settingsState,
		logger:          logger,
		isControlServer: isControlServer,
	}
}

func (s *Server) Start(address string, stopChan chan struct{}) error {
	if s.IsRunning() {
		return fmt.Errorf("server is already running")
	}

	// Create listener
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("failed to listen: %v", err)
	}
	s.mu.Lock()
	s.listener = listener

	// Create gRPC server with interceptors
	s.grpcServer = grpc.NewServer(
		grpc.UnaryInterceptor(s.loggingInterceptor),
	)

	// Register services
	srsServer := srs.NewSimpleRadioServer(s.serverState, s.settingsState, s.logger)
	srspb.RegisterSRSServiceServer(s.grpcServer, srsServer)

	controlServer := voiceontrol.NewVoiceControlServer(s.serverState, s.settingsState, s.logger)
	if s.isControlServer {
		voicecontrolpb.RegisterVoiceControlServiceServer(s.grpcServer, controlServer)
	}

	// Register health service
	healthServer := NewFullHealthServer()
	healthpb.RegisterHealthServer(s.grpcServer, healthServer)

	go func() {
		for s.IsRunning() {
			// Check the health of each Service
			srsStatus := srsServer.GetServerState()
			healthServer.SetServingStatus(srsService, srsStatus)

			controlStatus := controlServer.GetServerState()
			if s.isControlServer {
				healthServer.SetServingStatus(controlService, controlStatus)
			}

			if srsStatus == healthpb.HealthCheckResponse_SERVING && controlStatus == healthpb.HealthCheckResponse_SERVING { // Add more services as needed
				healthServer.SetServingStatus(system, healthpb.HealthCheckResponse_SERVING)
			} else {
				healthServer.SetServingStatus(system, healthpb.HealthCheckResponse_NOT_SERVING)
			}

			time.Sleep(sleep)
		}
	}()

	reflection.Register(s.grpcServer)

	s.isRunning = true
	s.mu.Unlock()

	// Start server
	go func() {
		s.logger.Info("Starting gRPC server", "address", address)
		if err := s.grpcServer.Serve(s.listener); err != nil {
			if err != grpc.ErrServerStopped {
				s.logger.Error("gRPC server error", "error", err)
			}
			s.mu.Lock()
			s.isRunning = false
			s.mu.Unlock()
		}
	}()

	// Handle shutdown
	go func() {
		<-stopChan
		s.logger.Info("Received stop signal for gRPC server")
		s.Stop()
	}()

	return nil
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

		// GracefulStop will automatically close the listener
		if s.grpcServer != nil {
			s.grpcServer.GracefulStop()
			s.grpcServer = nil
		}

		s.listener = nil // Just clear the reference, GracefulStop closes it
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
	s.logger.Debug("gRPC request",
		"method", info.FullMethod,
		"request", req)

	resp, err := handler(ctx, req)

	if err != nil {
		s.logger.Error("gRPC error",
			"method", info.FullMethod,
			"error", err)
	}

	return resp, err
}
