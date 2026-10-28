package srs

import (
	"context"
	"sync"
	"testing"
	"time"

	pb "github.com/FPGSchiba/vcs-srs-server/srspb"
	"github.com/FPGSchiba/vcs-srs-server/state"
	vcpb "github.com/FPGSchiba/vcs-srs-server/voicecontrolpb"
	"github.com/FPGSchiba/vcs-srs-server/voiceontrol"
	"github.com/google/uuid"
	"google.golang.org/grpc"
)

// newRegistryServer builds a test server wired to a real voice registry,
// mirroring control/server.go (settings coalitions, registry, then the
// registry set on the SRS service).
func newRegistryServer(t *testing.T) (*SimpleRadioServer, *voiceontrol.VoiceControlServer) {
	t.Helper()
	s := newTestServer(t)
	s.settingsState.Coalitions = []state.Coalition{{Name: "Blue"}, {Name: "Red"}}
	vc := voiceontrol.NewVoiceControlServer(s.serverState, s.settingsState, s.eventBus, s.logger)
	s.voiceRegistry = vc
	return s, vc
}

// registerNode registers a voice node on 127.0.0.1:port and requires success.
func registerNode(t *testing.T, vc *voiceontrol.VoiceControlServer, id string, port int32, global bool) {
	t.Helper()
	resp, err := vc.RegisterVoiceServer(context.Background(), &vcpb.RegisterVoiceServerRequest{
		ServerId:      id,
		ServerAddress: "127.0.0.1",
		UdpPort:       port,
		IsGlobal:      global,
	})
	if err != nil {
		t.Fatalf("RegisterVoiceServer(%s): %v", id, err)
	}
	if !resp.Success {
		t.Fatalf("RegisterVoiceServer(%s) not successful: %s", id, resp.Message)
	}
}

// addClient adds a client of the given coalition to the server state.
func addClient(s *SimpleRadioServer, coalition string) uuid.UUID {
	id := uuid.New()
	s.serverState.AddClient(id, &state.ClientState{Name: "c-" + id.String()[:8], Coalition: coalition})
	return id
}

// syncAs calls SyncClient as the given client and returns the sync data.
func syncAs(t *testing.T, s *SimpleRadioServer, id uuid.UUID) *pb.ServerSyncResult {
	t.Helper()
	resp, err := s.SyncClient(clientCtx(id), &pb.Empty{})
	if err != nil {
		t.Fatalf("SyncClient: %v", err)
	}
	if !resp.Success {
		t.Fatalf("SyncClient not successful")
	}
	data := resp.GetData()
	if data == nil {
		t.Fatalf("SyncClient returned no data")
	}
	return data
}

// Case 1: with no voice node registered, addresses are empty but the
// client still receives its 43-char voice secret.
func TestSyncClientWithoutVoiceNodes(t *testing.T) {
	s, _ := newRegistryServer(t)
	blue := addClient(s, "Blue")

	got := syncAs(t, s, blue)
	if got.CoalitionVoiceAddr != "" || got.GlobalVoiceAddr != "" {
		t.Fatalf("addresses = %q / %q, want both empty", got.CoalitionVoiceAddr, got.GlobalVoiceAddr)
	}
	if len(got.VoiceSecret) != 43 {
		t.Fatalf("len(VoiceSecret) = %d, want 43", len(got.VoiceSecret))
	}
}

// Case 2: with one coalition node and one global node, both coalitions
// resolve to the single owning node (rebalance step 1 gives A both).
func TestSyncClientReturnsOwningAndGlobalNode(t *testing.T) {
	s, vc := newRegistryServer(t)
	registerNode(t, vc, "A", 5002, false)
	registerNode(t, vc, "G", 5003, true)

	for _, coalition := range []string{"Blue", "Red"} {
		id := addClient(s, coalition)
		got := syncAs(t, s, id)
		if got.CoalitionVoiceAddr != "127.0.0.1:5002" {
			t.Errorf("%s: CoalitionVoiceAddr = %q, want 127.0.0.1:5002", coalition, got.CoalitionVoiceAddr)
		}
		if got.GlobalVoiceAddr != "127.0.0.1:5003" {
			t.Errorf("%s: GlobalVoiceAddr = %q, want 127.0.0.1:5003", coalition, got.GlobalVoiceAddr)
		}
		if len(got.VoiceSecret) != 43 {
			t.Errorf("%s: len(VoiceSecret) = %d, want 43", coalition, len(got.VoiceSecret))
		}
	}
}

// streamPumps holds one reader goroutine per update stream, so a timed-out
// wait never leaves a second concurrent Recv on the same stream.
var streamPumps sync.Map // grpc.ServerStreamingClient[pb.ServerUpdate] -> chan recvResult

type recvResult struct {
	u   *pb.ServerUpdate
	err error
}

func pumpOf(t *testing.T, stream grpc.ServerStreamingClient[pb.ServerUpdate]) chan recvResult {
	t.Helper()
	fresh := make(chan recvResult, 16)
	if got, loaded := streamPumps.LoadOrStore(stream, fresh); loaded {
		return got.(chan recvResult)
	}
	t.Cleanup(func() { streamPumps.Delete(stream) })
	go func() {
		for {
			u, err := stream.Recv()
			fresh <- recvResult{u, err}
			if err != nil {
				return
			}
		}
	}()
	return fresh
}

// recvVoiceUpdate reads one update within the deadline and fails on timeout
// or on any update type other than VOICE_ADDRESS_UPDATE.
func recvVoiceUpdate(t *testing.T, stream grpc.ServerStreamingClient[pb.ServerUpdate], within time.Duration) *pb.VoiceAddressUpdate {
	t.Helper()
	select {
	case r := <-pumpOf(t, stream):
		if r.err != nil {
			t.Fatalf("Recv: %v", r.err)
		}
		if r.u.Type != pb.ServerUpdate_VOICE_ADDRESS_UPDATE {
			t.Fatalf("update type = %v, want VOICE_ADDRESS_UPDATE", r.u.Type)
		}
		return r.u.GetVoiceAddressUpdate()
	case <-time.After(within):
		t.Fatalf("no voice address update within %v", within)
		return nil
	}
}

// expectNoUpdate fails if the stream delivers anything within the window.
func expectNoUpdate(t *testing.T, stream grpc.ServerStreamingClient[pb.ServerUpdate], within time.Duration) {
	t.Helper()
	select {
	case r := <-pumpOf(t, stream):
		t.Fatalf("unexpected update or end of stream: %v, %v", r.u, r.err)
	case <-time.After(within):
	}
}

func secretOf(t *testing.T, s *SimpleRadioServer, id uuid.UUID) string {
	t.Helper()
	secret, ok := s.serverState.GetVoiceSecret(id)
	if !ok || len(secret) != 43 {
		t.Fatalf("voice secret for %s: ok=%v len=%d", id, ok, len(secret))
	}
	return secret
}

func reportConnected(t *testing.T, vc *voiceontrol.VoiceControlServer, client uuid.UUID, node string) {
	t.Helper()
	resp, err := vc.ReportClientConnected(context.Background(), &vcpb.ClientConnectedRequest{ClientId: client.String(), ServerId: node})
	if err != nil || !resp.Success {
		t.Fatalf("ReportClientConnected: %v %+v", err, resp)
	}
}

// Case 3: when a new node takes over a coalition, only subscribed clients of
// the moved coalition get a redirect. Red is connected to A, so the count
// path is used and Blue (count 0) is non-sticky and moves immediately.
// RegisterVoiceServer publishes only VoiceNodeRegistered, which is not a
// stream event, so any update here is the CoalitionReassigned branch.
func TestRebalanceRedirectsSubscribedCoalition(t *testing.T) {
	s, vc := newRegistryServer(t)
	registerNode(t, vc, "A", 5002, false)
	registerNode(t, vc, "G", 5003, true)
	blue := addClient(s, "Blue")
	red := addClient(s, "Red")
	c := startGRPC(t, s)
	blueStream := openUpdates(t, c, blue)
	redStream := openUpdates(t, c, red)

	reportConnected(t, vc, red, "A")
	registerNode(t, vc, "B", 5004, false)

	got := recvVoiceUpdate(t, blueStream, 2*time.Second)
	if got.CoalitionVoiceAddr != "127.0.0.1:5004" || got.GlobalVoiceAddr != "127.0.0.1:5003" {
		t.Errorf("addrs = %q / %q, want 127.0.0.1:5004 / 127.0.0.1:5003", got.CoalitionVoiceAddr, got.GlobalVoiceAddr)
	}
	if got.VoiceSecret != secretOf(t, s, blue) {
		t.Errorf("VoiceSecret does not match Blue's secret")
	}
	expectNoUpdate(t, blueStream, 300*time.Millisecond)
	expectNoUpdate(t, redStream, 300*time.Millisecond)
}

// Case 4: a sticky coalition (clients connected) is queued and flushed when
// its last client disconnects. Red stays connected so clientNodes is not
// empty after Blue disconnects; otherwise the fallback count over
// serverState.Clients would keep Blue sticky.
func TestStickyMoveFlushesOnDisconnect(t *testing.T) {
	s, vc := newRegistryServer(t)
	registerNode(t, vc, "A", 5002, false)
	registerNode(t, vc, "G", 5003, true)
	blue := addClient(s, "Blue")
	red := addClient(s, "Red")
	c := startGRPC(t, s)
	blueStream := openUpdates(t, c, blue)
	_ = openUpdates(t, c, red)

	reportConnected(t, vc, blue, "A")
	reportConnected(t, vc, red, "A")
	registerNode(t, vc, "B", 5004, false)
	expectNoUpdate(t, blueStream, 300*time.Millisecond)

	resp, err := vc.ReportClientDisconnected(context.Background(), &vcpb.ClientDisconnectedRequest{ClientId: blue.String(), ServerId: "A"})
	if err != nil || !resp.Acknowledged {
		t.Fatalf("ReportClientDisconnected: %v %+v", err, resp)
	}
	got := recvVoiceUpdate(t, blueStream, 2*time.Second)
	if got.CoalitionVoiceAddr != "127.0.0.1:5004" || got.GlobalVoiceAddr != "127.0.0.1:5003" {
		t.Errorf("addrs = %q / %q, want 127.0.0.1:5004 / 127.0.0.1:5003", got.CoalitionVoiceAddr, got.GlobalVoiceAddr)
	}
	if got.VoiceSecret != secretOf(t, s, blue) {
		t.Errorf("VoiceSecret does not match Blue's secret")
	}
}
