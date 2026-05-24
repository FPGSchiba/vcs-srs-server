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
