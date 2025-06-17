package control

import (
	"context"
	"errors"
	"fmt"
	"github.com/FPGSchiba/vcs-srs-server/srs"
	"github.com/FPGSchiba/vcs-srs-server/srspb"
	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/FPGSchiba/vcs-srs-server/voicecontrolpb"
	"github.com/FPGSchiba/vcs-srs-server/voiceontrol"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"
	"log/slog"
	"net"
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
	isRunning         bool
	isControlServer   bool      // Add this to indicate if this is a control server
	stopOnce          sync.Once // Add this to ensure we only stop once
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

	var err error

	s.mu.Lock()
	s.clientListener, err = net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("failed to listen: %v", err)
	}

	if s.isControlServer {
		s.controlListener, err = net.Listen("tcp", fmt.Sprintf("%s:%d", controlServerListeningIpAddress, voiceontrol.DefaultVoiceControlPort))
		if err != nil {
			return fmt.Errorf("failed to listen on control server address: %v", err)
		}
	}

	s.clientGrpcServer = grpc.NewServer(
		grpc.UnaryInterceptor(s.loggingInterceptor),
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
	srspb.RegisterSRSServiceServer(s.clientGrpcServer, srsServer)

	controlServer := voiceontrol.NewVoiceControlServer(s.serverState, s.settingsState, s.logger)

	if s.isControlServer {
		s.initControlServer(controlServer)
	}

	healthServer := NewFullHealthServer()
	healthpb.RegisterHealthServer(s.clientGrpcServer, healthServer)
	if s.isControlServer {
		healthpb.RegisterHealthServer(s.controlGrpcServer, healthServer)
	}

	go s.monitorHealth(srsServer, controlServer, healthServer)

	reflection.Register(s.clientGrpcServer)

	s.isRunning = true
	s.mu.Unlock()

	go s.serveClient(address)
	if s.isControlServer {
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
	controlServer *voiceontrol.VoiceControlServer,
	healthServer *FullHealthServer,
) {
	for s.IsRunning() {
		srsStatus := srsServer.GetServerState()
		healthServer.SetServingStatus(healthServiceSRS, srsStatus)

		controlStatus := controlServer.GetServerState()
		if s.isControlServer {
			healthServer.SetServingStatus(healthServiceControl, controlStatus)
		}

		if srsStatus == healthpb.HealthCheckResponse_SERVING && controlStatus == healthpb.HealthCheckResponse_SERVING {
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
