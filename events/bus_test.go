package events

import (
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

// TestEventBusUnsubscribe verifies that Unsubscribe removes the channel from dispatch.
func TestEventBusUnsubscribe(t *testing.T) {
	eb := NewEventBus()
	defer eb.Stop()

	ch := eb.Subscribe("test/unsub")
	eb.Unsubscribe(ch)

	eb.Publish(Event{Name: "test/unsub", Data: "ignored"})

	select {
	case <-ch:
		t.Fatal("received event on unsubscribed channel")
	case <-time.After(100 * time.Millisecond):
		// Expected: no event delivered to the removed channel.
	}
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
