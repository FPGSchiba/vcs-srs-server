package events

import (
	"sync"
	"testing"
	"time"
)

// TestEventBusDelivers verifies that a published event reaches a subscriber.
func TestEventBusDelivers(t *testing.T) {
	eb := NewEventBus()
	defer eb.Stop()

	ch := eb.Subscribe("test/event")

	eb.Publish(Event{Name: "test/event", Data: "hello"})

	select {
	case evt := <-ch:
		if evt.Name != "test/event" {
			t.Fatalf("expected test/event, got %s", evt.Name)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for event")
	}
}

// TestEventBusStop verifies that Stop shuts down the publisher goroutine.
func TestEventBusStop(t *testing.T) {
	eb := NewEventBus()
	eb.Stop()
	// Second Stop must not panic (once.Do protection).
	eb.Stop()
}

// TestEventBusUnsubscribe verifies that Unsubscribe removes the channel from dispatch
// and closes it so range-based consumers exit cleanly.
func TestEventBusUnsubscribe(t *testing.T) {
	eb := NewEventBus()
	defer eb.Stop()

	ch := eb.Subscribe("test/unsub")
	eb.Unsubscribe(ch)

	// Channel must be closed after Unsubscribe — drain until closed.
	// No real event should be present; the channel was closed with no data.
	for evt := range ch {
		// If we receive an event it must not be the one published below,
		// because Unsubscribe already removed us from dispatch.
		if evt.Name == "test/unsub" {
			t.Fatalf("received dispatched event on unsubscribed channel: %v", evt)
		}
	}
	// Reaching here means the channel is closed — expected behaviour.

	eb.Publish(Event{Name: "test/unsub", Data: "ignored"})
}

// TestEventBusWildcard verifies that "*" subscribers receive all events.
func TestEventBusWildcard(t *testing.T) {
	eb := NewEventBus()
	defer eb.Stop()

	ch := eb.Subscribe("*")
	eb.Publish(Event{Name: "anything", Data: nil})

	select {
	case evt := <-ch:
		if evt.Name != "anything" {
			t.Fatalf("expected anything, got %s", evt.Name)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for wildcard event")
	}
}

// TestEventBusMultipleSubscribers verifies that multiple subscribers on the same
// event name all receive the event.
func TestEventBusMultipleSubscribers(t *testing.T) {
	eb := NewEventBus()
	defer eb.Stop()

	ch1 := eb.Subscribe("multi")
	ch2 := eb.Subscribe("multi")

	eb.Publish(Event{Name: "multi", Data: "both"})

	for i, ch := range []chan Event{ch1, ch2} {
		select {
		case evt := <-ch:
			if evt.Name != "multi" {
				t.Fatalf("subscriber %d: expected multi, got %s", i, evt.Name)
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("subscriber %d timed out", i)
		}
	}
}

// TestEventBusConcurrentSubscribePublish exercises concurrent Subscribe/Unsubscribe
// while publishing to catch races detected by -race.
func TestEventBusConcurrentSubscribePublish(t *testing.T) {
	eb := NewEventBus()
	defer eb.Stop()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch := eb.Subscribe("race")
			eb.Publish(Event{Name: "race", Data: nil})
			eb.Unsubscribe(ch)
		}()
	}
	wg.Wait()
}

// waitFor polls cond every 5ms until it holds or d elapses.
func waitFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting for %s", d, what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestSubscribeNamesFilters verifies a filtered subscription receives only its names.
func TestSubscribeNamesFilters(t *testing.T) {
	eb := NewEventBus()
	defer eb.Stop()

	sub := eb.SubscribeNames(8, "a", "b")
	defer sub.Close()
	eb.Publish(Event{Name: "a"})
	eb.Publish(Event{Name: "c"})
	eb.Publish(Event{Name: "b"})

	for _, want := range []string{"a", "b"} {
		select {
		case evt := <-sub.C:
			if evt.Name != want {
				t.Fatalf("expected %s, got %s", want, evt.Name)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s", want)
		}
	}
	select {
	case evt := <-sub.C:
		t.Fatalf("unexpected extra event %q", evt.Name)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestSubscribeNamesWildcardNoDuplicates verifies "*" mixed with names never delivers twice.
func TestSubscribeNamesWildcardNoDuplicates(t *testing.T) {
	eb := NewEventBus()
	defer eb.Stop()

	sub := eb.SubscribeNames(8, "*", "a", "a")
	defer sub.Close()
	eb.Publish(Event{Name: "a"})

	select {
	case <-sub.C:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
	select {
	case evt := <-sub.C:
		t.Fatalf("event delivered twice: %q", evt.Name)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestSubscriptionOverflow verifies overflow fires once, counts drops and delivers nothing afterwards.
func TestSubscriptionOverflow(t *testing.T) {
	eb := NewEventBus()
	defer eb.Stop()

	sub := eb.SubscribeNames(2, "a")
	defer sub.Close()
	for i := 0; i < 5; i++ {
		eb.Publish(Event{Name: "a"})
	}
	select {
	case <-sub.Overflow():
	case <-time.After(time.Second):
		t.Fatal("Overflow() not closed")
	}
	waitFor(t, time.Second, "Dropped()==3", func() bool { return sub.Dropped() == 3 })
	if n := len(sub.C); n != 2 {
		t.Fatalf("len(C) = %d, want 2", n)
	}
	if got := eb.Stats().Overflows; got != 1 {
		t.Fatalf("Stats().Overflows = %d, want 1", got)
	}
	eb.Publish(Event{Name: "a"})
	waitFor(t, time.Second, "Dropped()==4", func() bool { return sub.Dropped() == 4 })
	if n := len(sub.C); n != 2 {
		t.Fatalf("len(C) after overflow = %d, want 2", n)
	}
	if got := eb.Stats().Overflows; got != 1 {
		t.Fatalf("Stats().Overflows after more drops = %d, want 1", got)
	}
}

// TestLegacySubscribeDropsAndCounts verifies legacy subscriptions drop and count.
func TestLegacySubscribeDropsAndCounts(t *testing.T) {
	eb := NewEventBus()
	defer eb.Stop()

	ch := eb.Subscribe("x")
	for i := 0; i < 70; i++ {
		eb.Publish(Event{Name: "x"})
	}
	waitFor(t, time.Second, "LegacyDropped==6", func() bool { return eb.Stats().LegacyDropped == 6 })
	if n := len(ch); n != 64 {
		t.Fatalf("len(ch) = %d, want 64", n)
	}
}

// TestPublishWaitsForQueueSpace verifies a state event waits on a full queue, then is delivered.
func TestPublishWaitsForQueueSpace(t *testing.T) {
	eb := newEventBus(4)
	t.Cleanup(eb.Stop)

	ch := eb.Subscribe("last")
	for i := 0; i < 4; i++ {
		eb.Publish(Event{Name: "fill"})
	}
	done := make(chan struct{})
	go func() {
		eb.Publish(Event{Name: "last"})
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("Publish returned on a full queue; want it to wait")
	case <-time.After(100 * time.Millisecond):
	}
	go eb.startPublisher()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Publish still waiting after the dispatcher started")
	}
	select {
	case evt := <-ch:
		if evt.Name != "last" {
			t.Fatalf("expected last, got %s", evt.Name)
		}
	case <-time.After(time.Second):
		t.Fatal("waiting event was never delivered")
	}
}

// TestPublishLogEntryDropsWhenFull verifies a LogEntry on a full queue is dropped and counted.
func TestPublishLogEntryDropsWhenFull(t *testing.T) {
	eb := newEventBus(1)
	t.Cleanup(eb.Stop)

	eb.Publish(Event{Name: "fill"})
	done := make(chan struct{})
	go func() {
		eb.Publish(Event{Name: LogEntry})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("LogEntry Publish blocked on a full queue")
	}
	if got := eb.Stats().LogDropped; got != 1 {
		t.Fatalf("Stats().LogDropped = %d, want 1", got)
	}
}

// TestPublishWaitingReturnsOnStop verifies a publisher waiting for queue space returns on Stop.
func TestPublishWaitingReturnsOnStop(t *testing.T) {
	eb := newEventBus(1)
	eb.Publish(Event{Name: "fill"})
	done := make(chan struct{})
	go func() {
		eb.Publish(Event{Name: "fill"})
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("Publish returned on a full queue; want it to wait")
	case <-time.After(50 * time.Millisecond):
	}
	eb.Stop()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("waiting Publish did not return after Stop")
	}
}

// TestSubscriptionCloseIdempotent verifies Close is idempotent, including after Stop.
func TestSubscriptionCloseIdempotent(t *testing.T) {
	eb := NewEventBus()
	defer eb.Stop()

	sub := eb.SubscribeNames(4, "a")
	sub.Close()
	sub.Close()
	select {
	case _, ok := <-sub.C:
		if ok {
			t.Fatal("C delivered an event after Close")
		}
	case <-time.After(time.Second):
		t.Fatal("C not closed after Close")
	}

	sub2 := eb.SubscribeNames(4, "a")
	eb.Stop()
	select {
	case _, ok := <-sub2.C:
		if ok {
			t.Fatal("C delivered an event after Stop")
		}
	case <-time.After(time.Second):
		t.Fatal("C not closed after Stop")
	}
	sub2.Close()
}
