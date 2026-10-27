package srs

import (
	"context"
	"net"
	"testing"
	"time"

	pb "github.com/FPGSchiba/vcs-srs-server/srspb"
	"github.com/FPGSchiba/vcs-srs-server/utils"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
)

// testClientIDHeader carries the client GUID from the test client to the
// test interceptor, standing in for the production JWT interceptor.
const testClientIDHeader = "test-client-id"

// idStream overrides Context so the handler sees utils.ClientIDKey.
type idStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *idStream) Context() context.Context { return s.ctx }

// startGRPC serves s over an in-memory listener with a stream interceptor
// that copies the test client-id header into utils.ClientIDKey, and returns
// a connected client.
func startGRPC(t *testing.T, s *SimpleRadioServer) pb.SRSServiceClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer(grpc.StreamInterceptor(func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, h grpc.StreamHandler) error {
		ctx := ss.Context()
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			if v := md.Get(testClientIDHeader); len(v) == 1 {
				ctx = context.WithValue(ctx, utils.ClientIDKey, v[0])
			}
		}
		return h(srv, &idStream{ServerStream: ss, ctx: ctx})
	}))
	pb.RegisterSRSServiceServer(gs, s)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return pb.NewSRSServiceClient(conn)
}

// openUpdates opens SubscribeToUpdates for id and waits (bounded) for the
// response headers. It fails the test if they do not arrive in time.
func openUpdates(t *testing.T, c pb.SRSServiceClient, id uuid.UUID) grpc.ServerStreamingClient[pb.ServerUpdate] {
	t.Helper()
	ctx, cancel := context.WithCancel(metadata.AppendToOutgoingContext(context.Background(), testClientIDHeader, id.String()))
	t.Cleanup(cancel)
	stream, err := c.SubscribeToUpdates(ctx, &pb.Empty{})
	if err != nil {
		t.Fatalf("SubscribeToUpdates: %v", err)
	}
	got := make(chan error, 1)
	go func() {
		_, err := stream.Header()
		got <- err
	}()
	select {
	case err := <-got:
		if err != nil {
			t.Fatalf("Header: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SubscribeToUpdates sent no response headers before any update was published")
	}
	return stream
}

// C17: the headers arrive before any update is published, so a client can
// use them as "the subscription is live".
func TestSubscribeToUpdatesSendsHeadersOnceRegistered(t *testing.T) {
	s := newTestServer(t)
	id := addTestClient(s)
	c := startGRPC(t, s)

	openUpdates(t, c, id)

	if s.registeredSub(id) == nil {
		t.Fatal("headers observed but no subscription is registered")
	}
}

// C17: an event published after the client observed the headers is
// delivered on that stream (closes the resubscribe-then-SyncClient gap).
func TestSubscribeToUpdatesDeliversEventPublishedAfterHeaders(t *testing.T) {
	s := newTestServer(t)
	id := addTestClient(s)
	c := startGRPC(t, s)

	stream := openUpdates(t, c, id)
	publishRadio(s, id)

	got := make(chan error, 1)
	go func() {
		u, err := stream.Recv()
		if err == nil && u.GetType() != pb.ServerUpdate_CLIENT_RADIO_UPDATE {
			t.Errorf("first update type = %v, want CLIENT_RADIO_UPDATE", u.GetType())
		}
		got <- err
	}()
	select {
	case err := <-got:
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event published after the headers was not delivered on the stream")
	}
}
