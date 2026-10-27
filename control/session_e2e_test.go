package control

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/events"
	"github.com/FPGSchiba/vcs-srs-server/srs"
	"github.com/FPGSchiba/vcs-srs-server/srspb"
	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/FPGSchiba/vcs-srs-server/utils"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// newE2E serves the SRS service over an in-memory bufconn listener behind
// the real auth interceptors, and returns the control Server (marked
// running), the SRS server, a client, and a helper that creates a session
// and returns its id and an outgoing context carrying its token.
func newE2E(t *testing.T) (*Server, *srs.SimpleRadioServer, srspb.SRSServiceClient, func() (uuid.UUID, context.Context)) {
	t.Helper()
	s := newInterceptorTestServer(t)
	bus := events.NewEventBus()
	t.Cleanup(bus.Stop)
	s.eventBus = bus

	srsServer := srs.NewSimpleRadioServer(s.serverState, s.settingsState, slog.New(slog.NewTextHandler(io.Discard, nil)), bus, nil, nil)
	t.Cleanup(srsServer.Stop)

	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer(
		grpc.ChainUnaryInterceptor(s.authInterceptor),
		grpc.ChainStreamInterceptor(s.authStreamInterceptor),
	)
	srspb.RegisterSRSServiceServer(gs, srsServer)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)

	s.mu.Lock()
	s.srsServer = srsServer
	s.isRunning = true
	s.mu.Unlock()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	login := func() (uuid.UUID, context.Context) {
		tok, guid := mintToken(t, s, utils.GuestRole, time.Hour)
		id := uuid.MustParse(guid)
		s.serverState.AddClient(id, &state.ClientState{Name: "t"})
		ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+tok)
		return id, ctx
	}
	return s, srsServer, srspb.NewSRSServiceClient(conn), login
}

type recvResult struct {
	u   *srspb.ServerUpdate
	err error
}

// pump reads stream into a channel until it ends.
func pump(stream grpc.ServerStreamingClient[srspb.ServerUpdate]) <-chan recvResult {
	ch := make(chan recvResult, 64)
	go func() {
		for {
			u, err := stream.Recv()
			ch <- recvResult{u, err}
			if err != nil {
				return
			}
		}
	}()
	return ch
}

func next(t *testing.T, ch <-chan recvResult) recvResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(3 * time.Second):
		t.Fatal("Recv timed out")
		return recvResult{}
	}
}

// awaitLive proves the stream's server-side loop is running: it triggers a
// radio update for the caller until one arrives on the stream.
func awaitLive(t *testing.T, client srspb.SRSServiceClient, ctx context.Context, ch <-chan recvResult) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := client.UpdateRadioInfo(ctx, &srspb.RadioInfo{}); err != nil {
			t.Fatalf("UpdateRadioInfo: %v", err)
		}
		select {
		case r := <-ch:
			if r.err != nil {
				t.Fatalf("stream ended while waiting for it to go live: %v", r.err)
			}
			if r.u.GetType() == srspb.ServerUpdate_CLIENT_RADIO_UPDATE {
				return
			}
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatal("stream never went live")
}

// skipRadioUpdates returns the first result that is not a radio update
// (awaitLive may leave a few of those in flight).
func skipRadioUpdates(t *testing.T, ch <-chan recvResult) recvResult {
	t.Helper()
	for {
		r := next(t, ch)
		if r.err == nil && r.u.GetType() == srspb.ServerUpdate_CLIENT_RADIO_UPDATE {
			continue
		}
		return r
	}
}

func TestE2EKickEndsStreamAndToken(t *testing.T) {
	s, _, client, login := newE2E(t)
	id, ctx := login()
	stream, err := client.SubscribeToUpdates(ctx, &srspb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	ch := pump(stream)
	awaitLive(t, client, ctx, ch)

	existed, running := s.RemoveClient(id, srs.ReasonKicked, srs.ServerActionUpdate(events.ActionKick, id, "r"))
	if !existed || !running {
		t.Fatalf("RemoveClient = (%v, %v), want (true, true)", existed, running)
	}

	r := skipRadioUpdates(t, ch)
	if r.err != nil {
		t.Fatalf("expected the KICK update first, got error %v", r.err)
	}
	if r.u.GetServerAction().GetType() != srspb.ServerAction_KICK || r.u.GetServerAction().GetTargetClientGuid() != id.String() {
		t.Fatalf("got %v, want KICK for %s", r.u, id)
	}
	r = next(t, ch)
	if status.Code(r.err) != codes.Unauthenticated || status.Convert(r.err).Message() != "session ended: kicked" {
		t.Fatalf("stream ended with %v (update %v), want Unauthenticated \"session ended: kicked\"", r.err, r.u)
	}

	if _, err := client.Ping(ctx, &srspb.PingRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("Ping after kick: %v, want Unauthenticated", err)
	}
}

func TestE2EDuplicateSubscribe(t *testing.T) {
	_, _, client, login := newE2E(t)
	_, ctx := login()

	sync1, err := client.SyncClient(ctx, &srspb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	secret := sync1.GetData().GetVoiceSecret()
	if secret == "" {
		t.Fatal("no voice secret")
	}

	a, err := client.SubscribeToUpdates(ctx, &srspb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	chA := pump(a)
	awaitLive(t, client, ctx, chA)

	b, err := client.SubscribeToUpdates(ctx, &srspb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	chB := pump(b)

	if r := skipRadioUpdates(t, chA); status.Code(r.err) != codes.Aborted {
		t.Fatalf("first stream: %v, %v; want Aborted", r.u, r.err)
	}
	awaitLive(t, client, ctx, chB)

	sync2, err := client.SyncClient(ctx, &srspb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	if got := sync2.GetData().GetVoiceSecret(); got != secret {
		t.Fatalf("voice secret changed across the resubscribe: %q -> %q", secret, got)
	}
}

func TestRemoveClientNotRunning(t *testing.T) {
	s := newInterceptorTestServer(t)
	existed, running := s.RemoveClient(uuid.New(), srs.ReasonKicked, nil)
	if existed || running {
		t.Fatalf("RemoveClient = (%v, %v), want (false, false)", existed, running)
	}
}

// blockingDropper blocks every drop until release is closed.
type blockingDropper struct {
	entered chan struct{}
	release chan struct{}
}

func (d *blockingDropper) DropVoiceSession(uuid.UUID, string) {
	d.entered <- struct{}{}
	<-d.release
}

// TestRemoveClientDoesNotHoldMuAcrossDrop: the voice drop sends to every
// voice node and may block on flow control; Stop (s.mu.Lock) must not wait
// behind it.
func TestRemoveClientDoesNotHoldMuAcrossDrop(t *testing.T) {
	s := newInterceptorTestServer(t)
	bus := events.NewEventBus()
	t.Cleanup(bus.Stop)
	d := &blockingDropper{entered: make(chan struct{}, 1), release: make(chan struct{})}
	srsServer := srs.NewSimpleRadioServer(s.serverState, s.settingsState, slog.New(slog.NewTextHandler(io.Discard, nil)), bus, nil, d)
	t.Cleanup(srsServer.Stop)
	s.mu.Lock()
	s.srsServer = srsServer
	s.isRunning = true
	s.mu.Unlock()
	id := uuid.New()
	s.serverState.AddClient(id, &state.ClientState{Name: "t"})

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.RemoveClient(id, srs.ReasonKicked, nil)
	}()
	select {
	case <-d.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("the drop never ran")
	}
	locked := s.mu.TryLock()
	if locked {
		s.mu.Unlock()
	}
	close(d.release)
	<-done
	if !locked {
		t.Fatal("control.Server.mu is held while the voice drop runs")
	}
}
