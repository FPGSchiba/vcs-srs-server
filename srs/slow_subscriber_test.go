package srs

import (
	"context"
	"testing"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/events"
	pb "github.com/FPGSchiba/vcs-srs-server/srspb"
	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/FPGSchiba/vcs-srs-server/utils"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// stalledStream is a fakeUpdateStream whose Send blocks until release is
// closed. entered is signalled (without blocking) each time Send is entered.
type stalledStream struct {
	*fakeUpdateStream
	release chan struct{}
	entered chan struct{}
}

func (s *stalledStream) Send(u *pb.ServerUpdate) error {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	<-s.release
	return s.fakeUpdateStream.Send(u)
}

// startStalledSubscriber mirrors startSubscriber with a stalled stream.
func startStalledSubscriber(t *testing.T, s *SimpleRadioServer, id uuid.UUID) (*stalledStream, <-chan error) {
	t.Helper()
	prev := s.registeredSub(id)
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), utils.ClientIDKey, id.String()))
	stream := &stalledStream{
		fakeUpdateStream: newFakeUpdateStream(ctx),
		release:          make(chan struct{}),
		entered:          make(chan struct{}, 1),
	}
	result := make(chan error, 1)
	go func() { result <- s.SubscribeToUpdates(&pb.Empty{}, stream) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if cur := s.registeredSub(id); cur != nil && cur != prev {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("stalled subscriber for %s never registered", id)
		}
		time.Sleep(time.Millisecond)
	}
	t.Cleanup(cancel)
	return stream, result
}

// publishRadio publishes one RadioClientsChanged for id.
func publishRadio(s *SimpleRadioServer, id uuid.UUID) {
	s.eventBus.Publish(events.Event{
		Name: events.RadioClientsChanged,
		Data: events.RadioChangeEvent{
			Type:     events.RadioUpdated,
			ClientID: id,
			Radios:   map[uuid.UUID]*state.RadioState{id: {}},
		},
	})
}

func waitOverflows(t *testing.T, bus *events.EventBus, n uint64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for bus.Stats().Overflows < n {
		if time.Now().After(deadline) {
			t.Fatalf("Stats().Overflows = %d, want >= %d", bus.Stats().Overflows, n)
		}
		time.Sleep(time.Millisecond)
	}
}

func waitEntered(t *testing.T, st *stalledStream) {
	t.Helper()
	select {
	case <-st.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("stalled stream never entered Send")
	}
}

func waitSent(t *testing.T, st *fakeUpdateStream, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for len(st.sentCopy()) < n {
		if time.Now().After(deadline) {
			t.Fatalf("stream received %d updates, want %d", len(st.sentCopy()), n)
		}
		time.Sleep(time.Millisecond)
	}
}

func assertFellBehind(t *testing.T, err error) {
	t.Helper()
	if status.Code(err) != codes.ResourceExhausted ||
		status.Convert(err).Message() != "update stream fell behind: resubscribe and call SyncClient" {
		t.Fatalf("stream ended with %v, want ResourceExhausted fell-behind", err)
	}
}

// SRV-08: a subscriber that falls behind has its stream ended with
// ResourceExhausted; its session is kept and the SV1 grace starts.
func TestSlowSubscriberEndsWithResourceExhausted(t *testing.T) {
	s := newTestServer(t)
	d := withDropper(s)
	id := addTestClient(s)
	left := watchClientLeft(t, s.eventBus, id)
	st, result := startStalledSubscriber(t, s, id)

	publishRadio(s, id)
	waitEntered(t, st)
	for i := 0; i < 300; i++ {
		publishRadio(s, id)
	}
	waitOverflows(t, s.eventBus, 1)
	close(st.release)

	assertFellBehind(t, waitResult(t, result))
	assertPresent(t, s, id)
	if s.registeredSub(id) != nil {
		t.Fatal("subscription still registered after falling behind")
	}
	if _, ok := s.detachedSince(id); !ok {
		t.Fatal("detachedAt not set after falling behind")
	}
	if n := left(300 * time.Millisecond); n != 0 {
		t.Fatalf("%d ClientLeft events, want 0", n)
	}
	if calls := d.callsCopy(); len(calls) != 0 {
		t.Fatalf("dropper calls %v, want none", calls)
	}

	startSubscriber(t, s, id)
	if _, ok := s.detachedSince(id); ok {
		t.Fatal("detachedAt still set after resubscribe")
	}
}

// SRV-08: log entries are not stream events; a flood of them never ends a stream.
func TestLogFloodDoesNotEndStream(t *testing.T) {
	s := newTestServer(t)
	id := addTestClient(s)
	st, result := startStalledSubscriber(t, s, id)
	publishRadio(s, id)
	waitEntered(t, st)

	sentinel := s.eventBus.Subscribe(events.SettingsChanged)
	t.Cleanup(func() { s.eventBus.Unsubscribe(sentinel) })
	for i := 0; i < 500; i++ {
		s.eventBus.Publish(events.Event{Name: events.LogEntry})
	}
	s.eventBus.Publish(events.Event{Name: events.SettingsChanged})
	select {
	case <-sentinel:
	case <-time.After(2 * time.Second):
		t.Fatal("sentinel SettingsChanged never dispatched")
	}
	if n := s.eventBus.Stats().Overflows; n != 0 {
		t.Fatalf("Stats().Overflows = %d after a log flood, want 0", n)
	}
	close(st.release)

	select {
	case err := <-result:
		t.Fatalf("stream ended after a log flood: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	if s.registeredSub(id) == nil {
		t.Fatal("subscription unregistered after a log flood")
	}
}

// SRV-08: one slow subscriber does not cost another subscriber any event.
func TestSlowSubscriberDoesNotAffectOthers(t *testing.T) {
	s := newTestServer(t)
	a := addTestClient(s)
	b := addTestClient(s)
	stA, resultA := startStalledSubscriber(t, s, a)
	_, _, streamB := startSubscriber(t, s, b)

	total := 0
	for batch := 0; batch < 6; batch++ {
		for i := 0; i < 50; i++ {
			publishRadio(s, b)
		}
		total += 50
		// B's fakeUpdateStream.sentCh (cap 64) is non-blocking, so count via sentCopy.
		waitSent(t, streamB, total)
	}
	waitOverflows(t, s.eventBus, 1)
	close(stA.release)

	assertFellBehind(t, waitResult(t, resultA))
	waitSent(t, streamB, 300)
	if n := len(streamB.sentCopy()); n != 300 {
		t.Fatalf("B received %d updates, want 300", n)
	}
	if s.registeredSub(b) == nil {
		t.Fatal("B's subscription was unregistered")
	}
}

// SRV-08 x SV1: a kick landing on an overflowed stream still delivers the
// KICK last, exactly once, with "session ended: kicked".
func TestOverflowThenKickDeliversFinal(t *testing.T) {
	s := newTestServer(t)
	id := addTestClient(s)
	st, result := startStalledSubscriber(t, s, id)

	publishRadio(s, id)
	waitEntered(t, st)
	for i := 0; i < 300; i++ {
		publishRadio(s, id)
	}
	waitOverflows(t, s.eventBus, 1)

	final := ServerActionUpdate(events.ActionKick, id, "bye")
	s.RemoveClient(id, ReasonKicked, final)
	close(st.release)

	assertSessionEnded(t, waitResult(t, result), "kicked")
	sent := st.sentCopy()
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

// SRV-08 x SV1 (D10): the overflow is observed after a remover has
// unregistered the subscription but before it has ended it. The handler must
// not claim the ending itself; it waits for the remover's end and returns the
// remover's status, delivering its final update last.
func TestOverflowAfterUnregisterWaitsForRemover(t *testing.T) {
	s := newTestServer(t)
	id := addTestClient(s)
	st, result := startStalledSubscriber(t, s, id)

	publishRadio(s, id)
	waitEntered(t, st)
	for i := 0; i < 300; i++ {
		publishRadio(s, id)
	}
	waitOverflows(t, s.eventBus, 1)

	// First half of removeClientIf: unregister under s.mu, do not end yet.
	s.mu.Lock()
	sub := s.streams[id]
	delete(s.streams, id)
	delete(s.detachedAt, id)
	s.mu.Unlock()
	if sub == nil {
		t.Fatal("no registered subscription to unregister")
	}
	close(st.release)

	select {
	case err := <-result:
		t.Fatalf("handler returned %v before the remover ended the subscription", err)
	case <-time.After(200 * time.Millisecond):
	}
	if _, ok := s.detachedSince(id); ok {
		t.Fatal("handler set detachedAt for a subscription it no longer owns")
	}

	// Second half of removeClientIf: end with the final update.
	final := ServerActionUpdate(events.ActionKick, id, "bye")
	sub.end(final, SessionEndedError(ReasonKicked))

	assertSessionEnded(t, waitResult(t, result), "kicked")
	sent := st.sentCopy()
	if len(sent) == 0 || sent[len(sent)-1] != final {
		t.Fatal("final update was not the last update sent")
	}
	n := 0
	for _, u := range sent {
		if u == final {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("final update sent %d times, want 1", n)
	}
	if _, ok := s.detachedSince(id); ok {
		t.Fatal("detachedAt set after a remover ended the subscription")
	}
}
