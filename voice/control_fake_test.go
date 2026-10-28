package voice

import (
	"context"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/state"
	pb "github.com/FPGSchiba/vcs-srs-server/voicecontrolpb"
	"github.com/FPGSchiba/vcs-srs-server/voiceontrol"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// recordingControl is a minimal fake control node for voice-server tests:
// it records registrations, holds each stream open, and can push control
// messages onto the newest one.
type recordingControl struct {
	pb.UnimplementedVoiceControlServiceServer

	mu        sync.Mutex
	registers []*pb.RegisterVoiceServerRequest
	active    int
	out       chan *pb.ControlMessage
}

func (f *recordingControl) RegisterVoiceServer(_ context.Context, req *pb.RegisterVoiceServerRequest) (*pb.RegisterVoiceServerResponse, error) {
	f.mu.Lock()
	f.registers = append(f.registers, req)
	f.mu.Unlock()
	return &pb.RegisterVoiceServerResponse{Success: true}, nil
}

func (f *recordingControl) SendHeartbeat(context.Context, *pb.HeartbeatRequest) (*pb.HeartbeatResponse, error) {
	return &pb.HeartbeatResponse{Acknowledged: true}, nil
}

func (f *recordingControl) ReportClientConnected(context.Context, *pb.ClientConnectedRequest) (*pb.ClientConnectedResponse, error) {
	return &pb.ClientConnectedResponse{}, nil
}

func (f *recordingControl) ReportClientDisconnected(context.Context, *pb.ClientDisconnectedRequest) (*pb.ClientDisconnectedResponse, error) {
	return &pb.ClientDisconnectedResponse{}, nil
}

func (f *recordingControl) EstablishStream(stream pb.VoiceControlService_EstablishStreamServer) error {
	if _, err := stream.Recv(); err != nil {
		return err
	}
	f.mu.Lock()
	f.active++
	out := f.out
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
	}()
	for {
		select {
		case msg := <-out:
			if err := stream.Send(msg); err != nil {
				return err
			}
		case <-stream.Context().Done():
			return nil
		}
	}
}

func (f *recordingControl) counts() (registers, active int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.registers), f.active
}

func (f *recordingControl) lastRegister() *pb.RegisterVoiceServerRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.registers) == 0 {
		return nil
	}
	return f.registers[len(f.registers)-1]
}

// serveRecordingControl serves f over bufconn and returns a hook that points
// a voice server's control client at it.
func serveRecordingControl(t *testing.T, f *recordingControl) func(*voiceontrol.VoiceControlClient) {
	t.Helper()
	f.out = make(chan *pb.ControlMessage, 16)
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	pb.RegisterVoiceControlServiceServer(srv, f)
	go srv.Serve(lis) //nolint:errcheck
	t.Cleanup(srv.Stop)
	return func(c *voiceontrol.VoiceControlClient) {
		c.UseDialer("passthrough:///bufnet",
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
	}
}

// newDistributedServer returns a voice server in voice-distribution mode with
// the given publicAddr, whose control client dials the fake via hook.
func newDistributedServer(publicAddr string, hook func(*voiceontrol.VoiceControlClient)) *Server {
	ss := &state.ServerState{
		Clients:      map[uuid.UUID]*state.ClientState{},
		RadioClients: map[uuid.UUID]*state.RadioState{},
	}
	settings := &state.SettingsState{VoiceControl: state.VoiceControlSettings{PublicAddr: publicAddr}}
	dist := &state.DistributionState{DistributionMode: state.DistributionModeVoice}
	s := NewServer(ss, slog.New(slog.DiscardHandler), dist, settings)
	s.controlClientHook = hook
	return s
}

func waitFor(t *testing.T, within time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}
