package srs

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
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
	s := newTestServer(t)
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
	s := newTestServer(t)
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
	s := newTestServer(t)
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
	s := newTestServer(t)
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

// --- SRV-02: the removal helper ---

type dropCall struct {
	id     uuid.UUID
	reason string
}

type fakeDropper struct {
	mu    sync.Mutex
	calls []dropCall
}

func (f *fakeDropper) DropVoiceSession(id uuid.UUID, reason string) {
	f.mu.Lock()
	f.calls = append(f.calls, dropCall{id, reason})
	f.mu.Unlock()
}

func (f *fakeDropper) callsCopy() []dropCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]dropCall(nil), f.calls...)
}

func withDropper(s *SimpleRadioServer) *fakeDropper {
	d := &fakeDropper{}
	s.dropper = d
	return d
}

// watchClientLeft subscribes to ClientsChanged now (before the action under
// test) and returns a function that counts the ClientLeft events for id
// that arrive within wait.
func watchClientLeft(t *testing.T, bus *events.EventBus, id uuid.UUID) func(wait time.Duration) int {
	t.Helper()
	ch := bus.Subscribe(events.ClientsChanged)
	t.Cleanup(func() { bus.Unsubscribe(ch) })
	return func(wait time.Duration) int {
		n := 0
		timeout := time.After(wait)
		for {
			select {
			case ev := <-ch:
				if ce, ok := ev.Data.(events.ClientChangeEvent); ok && ce.Type == events.ClientLeft && ce.ClientID == id {
					n++
				}
			case <-timeout:
				return n
			}
		}
	}
}

func assertGone(t *testing.T, s *SimpleRadioServer, id uuid.UUID) {
	t.Helper()
	s.serverState.RLock()
	_, c := s.serverState.Clients[id]
	_, r := s.serverState.RadioClients[id]
	s.serverState.RUnlock()
	if c || r {
		t.Fatalf("client %s still in state (Clients %v, RadioClients %v)", id, c, r)
	}
	if s.registeredSub(id) != nil {
		t.Fatal("subscription still registered")
	}
	if _, ok := s.detachedSince(id); ok {
		t.Fatal("detachedAt still set")
	}
}

func assertPresent(t *testing.T, s *SimpleRadioServer, id uuid.UUID) {
	t.Helper()
	if !s.clientExists(id) {
		t.Fatalf("client %s was removed", id)
	}
}

func assertSessionEnded(t *testing.T, err error, reason string) {
	t.Helper()
	if status.Code(err) != codes.Unauthenticated || status.Convert(err).Message() != "session ended: "+reason {
		t.Fatalf("stream ended with %v, want Unauthenticated \"session ended: %s\"", err, reason)
	}
}

func TestRemoveClientEndsEverything(t *testing.T) {
	s := newTestServer(t)
	d := withDropper(s)
	id := addTestClient(s)
	left := watchClientLeft(t, s.eventBus, id)
	_, result, _ := startSubscriber(t, s, id)

	if !s.RemoveClient(id, ReasonStale, nil) {
		t.Fatal("RemoveClient returned false for a present client")
	}
	assertSessionEnded(t, waitResult(t, result), "stale")
	assertGone(t, s, id)
	if calls := d.callsCopy(); len(calls) != 1 || calls[0] != (dropCall{id, "stale"}) {
		t.Fatalf("dropper calls %v, want one (%s, stale)", calls, id)
	}
	if n := left(300 * time.Millisecond); n != 1 {
		t.Fatalf("%d ClientLeft events, want 1", n)
	}
}

func TestRemoveClientDeliversFinalLast(t *testing.T) {
	s := newTestServer(t)
	id := addTestClient(s)
	_, result, stream := startSubscriber(t, s, id)

	final := ServerActionUpdate(events.ActionKick, id, "bye")
	s.RemoveClient(id, ReasonKicked, final)
	assertSessionEnded(t, waitResult(t, result), "kicked")
	sent := stream.sentCopy()
	if len(sent) == 0 || sent[len(sent)-1] != final {
		t.Fatalf("last sent update is not the final one: %v", sent)
	}
}

// TestRemoveClientFinalLastUnderBusTraffic pins the final-update ordering
// while the target's own handler is busy forwarding bus events: the final
// update must be sent exactly once and be the last thing on the stream.
func TestRemoveClientFinalLastUnderBusTraffic(t *testing.T) {
	for i := 0; i < 20; i++ {
		s := newTestServer(t)
		id := addTestClient(s)
		_, result, stream := startSubscriber(t, s, id)

		stop := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(1)
		// Paced (SRV-08): one event in flight at a time, so the handler's
		// subscription can never overflow and end the stream with
		// ResourceExhausted instead of the kick under test.
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				s.eventBus.Publish(events.Event{
					Name: events.RadioClientsChanged,
					Data: events.RadioChangeEvent{
						Type:     events.RadioUpdated,
						ClientID: id,
						Radios:   map[uuid.UUID]*state.RadioState{id: {}},
					},
				})
				select {
				case <-stream.sentCh:
				case <-stop:
					return
				}
			}
		}()
		// Let the handler pick up radio updates before the removal. The
		// publisher owns stream.sentCh, so wait on the recorded sends.
		deadline := time.Now().Add(2 * time.Second)
		for len(stream.sentCopy()) == 0 {
			if time.Now().After(deadline) {
				t.Fatal("subscriber never received bus traffic")
			}
			time.Sleep(time.Millisecond)
		}

		final := ServerActionUpdate(events.ActionKick, id, "bye")
		s.RemoveClient(id, ReasonKicked, final)
		err := waitResult(t, result)
		close(stop)
		wg.Wait()
		assertSessionEnded(t, err, "kicked")

		sent := stream.sentCopy()
		n := 0
		for _, u := range sent {
			if u == final {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("final update sent %d times, want 1", n)
		}
		if sent[len(sent)-1] != final {
			t.Fatalf("last sent update is %v, not the final one", sent[len(sent)-1])
		}
	}
}

func TestDisconnectLogsClientName(t *testing.T) {
	s := newTestServer(t)
	var buf syncBuffer
	s.logger = slog.New(slog.NewTextHandler(&buf, nil))
	id := uuid.New()
	s.serverState.AddClient(id, &state.ClientState{Name: "Maverick", Coalition: "Blue"})

	if resp, err := s.Disconnect(clientCtx(id), &pb.Empty{}); err != nil || !resp.GetSuccess() {
		t.Fatalf("Disconnect: %+v, %v", resp, err)
	}
	if !strings.Contains(buf.String(), "client_name=Maverick") {
		t.Fatalf("Disconnect log does not name the client: %s", buf.String())
	}
}

func TestDisconnectUnknownLogsNoInfo(t *testing.T) {
	s := newTestServer(t)
	var buf syncBuffer
	s.logger = slog.New(slog.NewTextHandler(&buf, nil))

	if resp, _ := s.Disconnect(clientCtx(uuid.New()), &pb.Empty{}); resp.GetSuccess() {
		t.Fatal("Disconnect of an unknown client succeeded")
	}
	if strings.Contains(buf.String(), "level=INFO") {
		t.Fatalf("an unknown client must not log \"Disconnecting client\" at INFO: %s", buf.String())
	}
}

// syncBuffer is a bytes.Buffer safe for concurrent log writes.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func TestRemoveClientConcurrent(t *testing.T) {
	s := newTestServer(t)
	id := addTestClient(s)
	left := watchClientLeft(t, s.eventBus, id)

	var wg sync.WaitGroup
	var mu sync.Mutex
	trues := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s.RemoveClient(id, ReasonDisconnect, nil) {
				mu.Lock()
				trues++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if trues != 1 {
		t.Fatalf("%d calls returned true, want 1", trues)
	}
	if n := left(300 * time.Millisecond); n != 1 {
		t.Fatalf("%d ClientLeft events, want 1", n)
	}
}

func TestRemoveUnknownClient(t *testing.T) {
	s := newTestServer(t)
	d := withDropper(s)
	id := uuid.New()
	left := watchClientLeft(t, s.eventBus, id)
	if s.RemoveClient(id, ReasonKicked, nil) {
		t.Fatal("RemoveClient returned true for an unknown client")
	}
	if n := left(200 * time.Millisecond); n != 0 {
		t.Fatalf("%d ClientLeft events, want 0", n)
	}
	if len(d.callsCopy()) != 1 {
		t.Fatal("the voice drop is idempotent and must still run")
	}
}

func TestDisconnectRemoves(t *testing.T) {
	s := newTestServer(t)
	d := withDropper(s)
	id := addTestClient(s)
	left := watchClientLeft(t, s.eventBus, id)
	_, result, _ := startSubscriber(t, s, id)

	resp, err := s.Disconnect(clientCtx(id), &pb.Empty{})
	if err != nil || !resp.GetSuccess() {
		t.Fatalf("Disconnect: %+v, %v", resp, err)
	}
	assertSessionEnded(t, waitResult(t, result), "disconnected")
	assertGone(t, s, id)
	if calls := d.callsCopy(); len(calls) != 1 || calls[0] != (dropCall{id, "disconnected"}) {
		t.Fatalf("dropper calls %v", calls)
	}
	if n := left(300 * time.Millisecond); n != 1 {
		t.Fatalf("%d ClientLeft events, want 1", n)
	}
}

// --- SRV-01 / SRV-02: the reaper ---

func setLastUpdate(s *SimpleRadioServer, id uuid.UUID, t time.Time) {
	s.serverState.Lock()
	s.serverState.Clients[id].LastUpdate = t
	s.serverState.Unlock()
}

func TestReapPingingClientSurvives(t *testing.T) {
	s := newTestServer(t)
	clock := withSRSClock(s)
	id := addTestClient(s)
	setLastUpdate(s, id, clock.Now())
	startSubscriber(t, s, id)

	for i := 1; i <= 132; i++ { // 11 minutes in 5s steps
		clock.Advance(5 * time.Second)
		if _, err := s.Ping(clientCtx(id), &pb.PingRequest{}); err != nil {
			t.Fatal(err)
		}
		if i%12 == 0 {
			s.reapOnce(clock.Now())
			assertPresent(t, s, id)
		}
	}
}

func TestReapStaleLiveStream(t *testing.T) {
	s := newTestServer(t)
	clock := withSRSClock(s)
	id := addTestClient(s)
	T := clock.Now()
	setLastUpdate(s, id, T)
	left := watchClientLeft(t, s.eventBus, id)
	_, result, _ := startSubscriber(t, s, id)

	s.reapOnce(T.Add(10 * time.Minute))
	assertPresent(t, s, id)
	s.reapOnce(T.Add(10*time.Minute + time.Second))
	assertSessionEnded(t, waitResult(t, result), "stale")
	assertGone(t, s, id)
	if n := left(300 * time.Millisecond); n != 1 {
		t.Fatalf("%d ClientLeft events, want 1", n)
	}
}

// detach subscribes id and ends the stream from the client side at the
// fake clock's current time, which it returns.
func detach(t *testing.T, s *SimpleRadioServer, clock *fakeClock, id uuid.UUID) time.Time {
	t.Helper()
	T := clock.Now()
	setLastUpdate(s, id, T)
	cancel, result, _ := startSubscriber(t, s, id)
	cancel()
	if err := waitResult(t, result); err != nil {
		t.Fatalf("client-side end returned %v", err)
	}
	return T
}

func TestReapStreamLostAfterGrace(t *testing.T) {
	s := newTestServer(t)
	clock := withSRSClock(s)
	d := withDropper(s)
	id := addTestClient(s)
	T := detach(t, s, clock, id)

	s.reapOnce(T.Add(119 * time.Second))
	assertPresent(t, s, id)
	removed := s.reapOnce(T.Add(121 * time.Second))
	assertGone(t, s, id)
	if len(removed) != 1 || removed[0] != id {
		t.Fatalf("reapOnce returned %v", removed)
	}
	if calls := d.callsCopy(); len(calls) != 1 || calls[0].reason != "stream lost" {
		t.Fatalf("dropper calls %v, want one with reason \"stream lost\"", calls)
	}
}

func TestReapResubscribeKeepsSession(t *testing.T) {
	s := newTestServer(t)
	clock := withSRSClock(s)
	id := addTestClient(s)
	T := detach(t, s, clock, id)
	clock.Advance(60 * time.Second)
	startSubscriber(t, s, id)

	s.reapOnce(T.Add(300 * time.Second))
	assertPresent(t, s, id)
}

func TestReapPingExtendsGrace(t *testing.T) {
	s := newTestServer(t)
	clock := withSRSClock(s)
	id := addTestClient(s)
	T := detach(t, s, clock, id)
	clock.Advance(100 * time.Second)
	if _, err := s.Ping(clientCtx(id), &pb.PingRequest{}); err != nil {
		t.Fatal(err)
	}

	s.reapOnce(T.Add(200 * time.Second))
	assertPresent(t, s, id)
	s.reapOnce(T.Add(221 * time.Second))
	assertGone(t, s, id)
}

func TestReapNeverSubscribed(t *testing.T) {
	s := newTestServer(t)
	clock := withSRSClock(s)
	id := addTestClient(s)
	T := clock.Now()
	setLastUpdate(s, id, T)

	s.reapOnce(T.Add(119 * time.Second))
	assertPresent(t, s, id)
	s.reapOnce(T.Add(121 * time.Second))
	assertGone(t, s, id)
}

func TestReapRevalidatesUnderLock(t *testing.T) {
	s := newTestServer(t)
	clock := withSRSClock(s)
	id := addTestClient(s)
	T := detach(t, s, clock, id)
	now := T.Add(121 * time.Second)

	// Collection, as reapOnce does it.
	s.mu.Lock()
	s.serverState.RLock()
	reason, ok := s.reapReasonLocked(id, now)
	s.serverState.RUnlock()
	s.mu.Unlock()
	if !ok || reason != ReasonStreamLost {
		t.Fatalf("reapReasonLocked = %q, %v; want stream lost, true", reason, ok)
	}

	// The client resubscribes between collection and removal.
	_, result, _ := startSubscriber(t, s, id)

	if s.removeClientIf(id, reason, nil, s.reapCond(id, now, reason)) {
		t.Fatal("removal went ahead although the client resubscribed")
	}
	assertPresent(t, s, id)
	select {
	case err := <-result:
		t.Fatalf("new stream ended: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
}

// --- SERVER_ACTION target-only (D6, D16) ---

func TestForwardServerActionTargetOnly(t *testing.T) {
	x, y := uuid.New(), uuid.New()
	action := func(a events.ActionType) events.Event {
		return events.Event{Name: events.ServerAction, Data: events.ServerActionEvent{ActionType: a, TargetClientID: x}}
	}
	cases := []struct {
		name  string
		event events.Event
		to    uuid.UUID
		want  bool
	}{
		{"mute target", action(events.ActionMute), x, true},
		{"mute other", action(events.ActionMute), y, false},
		{"unmute target", action(events.ActionUnmute), x, true},
		{"unmute other", action(events.ActionUnmute), y, false},
		{"kick target", action(events.ActionKick), x, false},
		{"kick other", action(events.ActionKick), y, false},
		{"ban target", action(events.ActionBan), x, false},
		{"ban other", action(events.ActionBan), y, false},
		{"not a server action", events.Event{Name: events.SettingsChanged}, y, true},
	}
	for _, c := range cases {
		if got := forwardServerAction(c.event, c.to); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestMuteReachesOnlyTarget(t *testing.T) {
	s := newTestServer(t)
	x, y := addTestClient(s), addTestClient(s)
	_, _, streamX := startSubscriber(t, s, x)
	_, _, streamY := startSubscriber(t, s, y)

	s.eventBus.Publish(events.Event{Name: events.ServerAction, Data: events.ServerActionEvent{ActionType: events.ActionMute, TargetClientID: x}})

	select {
	case u := <-streamX.sentCh:
		if u.GetType() != pb.ServerUpdate_SERVER_ACTION || u.GetServerAction().GetType() != pb.ServerAction_MUTE {
			t.Fatalf("X got %v", u)
		}
	case <-time.After(time.Second):
		t.Fatal("X never received its MUTE")
	}
	select {
	case u := <-streamY.sentCh:
		t.Fatalf("Y received %v", u)
	case <-time.After(200 * time.Millisecond):
	}
}
