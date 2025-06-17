package voiceontrol

import (
	"github.com/FPGSchiba/vcs-srs-server/state"
	pb "github.com/FPGSchiba/vcs-srs-server/voicecontrolpb"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"log/slog"
	"sync"
)

type VoiceControlServer struct {
	pb.UnimplementedVoiceControlServiceServer
	logger        *slog.Logger
	mu            sync.Mutex
	serverState   *state.ServerState
	settingsState *state.SettingsState
}

func NewVoiceControlServer(serverState *state.ServerState, settingsState *state.SettingsState, logger *slog.Logger) *VoiceControlServer {
	return &VoiceControlServer{
		serverState:   serverState,
		settingsState: settingsState,
		logger:        logger,
		mu:            sync.Mutex{},
	}
}

func (s *VoiceControlServer) GetServerState() healthpb.HealthCheckResponse_ServingStatus {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.serverState == nil {
		return healthpb.HealthCheckResponse_SERVICE_UNKNOWN
	}
	// TODO: Implement actual logic to determine server state
	return healthpb.HealthCheckResponse_SERVING
}

func (s *VoiceControlServer) EstablishStream(stream pb.VoiceControlService_EstablishStreamServer) error {
	s.logger.Info("Establishing stream for voice control server")
	// Handle the stream here
	return nil
}
