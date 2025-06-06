package srs

import (
	pb "github.com/FPGSchiba/vcs-srs-server/srspb"
	"github.com/FPGSchiba/vcs-srs-server/state"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
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
