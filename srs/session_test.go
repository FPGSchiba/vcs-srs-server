package srs

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/events"
	pb "github.com/FPGSchiba/vcs-srs-server/srspb"
	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/FPGSchiba/vcs-srs-server/utils"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeUpdateStream is a grpc.ServerStreamingServer[pb.ServerUpdate] that
// records every Send. Only Context and Send are used by SubscribeToUpdates.
type fakeUpdateStream struct {
	grpc.ServerStream
	ctx    context.Context
	mu     sync.Mutex
	sent   []*pb.ServerUpdate
	sentCh chan *pb.ServerUpdate
}

func newFakeUpdateStream(ctx context.Context) *fakeUpdateStream {
	return &fakeUpdateStream{ctx: ctx, sentCh: make(chan *pb.ServerUpdate, 64)}
}

func (f *fakeUpdateStream) Context() context.Context { return f.ctx }

func (f *fakeUpdateStream) Send(u *pb.ServerUpdate) error {
	f.mu.Lock()
	f.sent = append(f.sent, u)
	f.mu.Unlock()
	select {
	case f.sentCh <- u:
	default:
	}
	return nil
}

func (f *fakeUpdateStream) sentCopy() []*pb.ServerUpdate {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*pb.ServerUpdate(nil), f.sent...)
}

func (s *SimpleRadioServer) registeredSub(id uuid.UUID) *subscription {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.streams[id]
}

func (s *SimpleRadioServer) detachedSince(id uuid.UUID) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.detachedAt[id]
	return t, ok
}

// startSubscriber runs SubscribeToUpdates for id in a goroutine and waits
// until its subscription is registered (i.e. differs from whatever was
// registered before).
func startSubscriber(t *testing.T, s *SimpleRadioServer, id uuid.UUID) (context.CancelFunc, <-chan error, *fakeUpdateStream) {
	t.Helper()
	prev := s.registeredSub(id)
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), utils.ClientIDKey, id.String()))
	stream := newFakeUpdateStream(ctx)
	result := make(chan error, 1)
	go func() { result <- s.SubscribeToUpdates(&pb.Empty{}, stream) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if cur := s.registeredSub(id); cur != nil && cur != prev {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("subscriber for %s never registered", id)
		}
		time.Sleep(time.Millisecond)
	}
	t.Cleanup(cancel)
	return cancel, result, stream
}

func waitResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("SubscribeToUpdates did not return")
		return nil
	}
}

func addTestClient(s *SimpleRadioServer) uuid.UUID {
	id := uuid.New()
	s.serverState.AddClient(id, &state.ClientState{Name: "t", Coalition: "Blue"})
	return id
}

func TestDuplicateSubscribeLastWins(t *testing.T) {
	s := newTestServer()
	id := addTestClient(s)

	_, resultA, _ := startSubscriber(t, s, id)
	_, _, streamB := startSubscriber(t, s, id)
	subB := s.registeredSub(id)

	err := waitResult(t, resultA)
	if status.Code(err) != codes.Aborted || status.Convert(err).Message() != "superseded by a newer subscription" {
		t.Fatalf("displaced subscriber returned %v, want Aborted superseded", err)
	}
	if got := s.registeredSub(id); got != subB {
		t.Fatal("the newer subscription must stay registered after the old handler exits")
	}
	if _, ok := s.detachedSince(id); ok {
		t.Fatal("a displaced subscription must not start a grace period")
	}

	s.eventBus.Publish(events.Event{
		Name: events.RadioClientsChanged,
		Data: events.RadioChangeEvent{Type: events.RadioUpdated, ClientID: id, Radios: map[uuid.UUID]*state.RadioState{}},
	})
	select {
	case u := <-streamB.sentCh:
		if u.GetType() != pb.ServerUpdate_CLIENT_RADIO_UPDATE {
			t.Fatalf("B received %v", u.GetType())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("B never received the event")
	}
}

func TestSubscribeUnknownClient(t *testing.T) {
	s := newTestServer()
	id := uuid.New()
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), utils.ClientIDKey, id.String()))
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- s.SubscribeToUpdates(&pb.Empty{}, newFakeUpdateStream(ctx)) }()
	err := waitResult(t, result)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("got %v, want Unauthenticated", err)
	}
	s.mu.Lock()
	n := len(s.streams)
	s.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d streams registered, want 0", n)
	}
}

func TestStreamEndStartsGrace(t *testing.T) {
	s := newTestServer()
	clock := withSRSClock(s)
	id := addTestClient(s)

	cancel, result, _ := startSubscriber(t, s, id)
	cancel()
	if err := waitResult(t, result); err != nil {
		t.Fatalf("client-side end returned %v, want nil", err)
	}
	if s.registeredSub(id) != nil {
		t.Fatal("subscription still registered after the client ended it")
	}
	at, ok := s.detachedSince(id)
	if !ok || !at.Equal(clock.Now()) {
		t.Fatalf("detachedAt = %v (%v), want %v", at, ok, clock.Now())
	}
}

func TestResubscribeClearsGrace(t *testing.T) {
	s := newTestServer()
	withSRSClock(s)
	id := addTestClient(s)

	cancel, result, _ := startSubscriber(t, s, id)
	cancel()
	waitResult(t, result)
	if _, ok := s.detachedSince(id); !ok {
		t.Fatal("precondition: grace not started")
	}
	startSubscriber(t, s, id)
	if _, ok := s.detachedSince(id); ok {
		t.Fatal("resubscribe must clear detachedAt")
	}
}
