package control

import (
	"context"
	"fmt"
	"github.com/FPGSchiba/vcs-srs-server/state"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"log/slog"
	"net"
	"sync"
)

type Server struct {
	mu          sync.RWMutex
	grpcServer  *grpc.Server
	listener    net.Listener
	logger      *slog.Logger
	serverState *state.ServerState
	isRunning   bool
	stopOnce    sync.Once // Add this to ensure we only stop once
}

func NewServer(serverState *state.ServerState, logger *slog.Logger) *Server {
	return &Server{
		serverState: serverState,
		logger:      logger,
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

	// Register your services here

	// pb.RegisterYourServiceServer(s.grpcServer, NewYourServiceServer(s.serverState))

	// Register health service
	healthServer := health.NewServer()
	healthpb.RegisterHealthServer(s.grpcServer, healthServer)

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
