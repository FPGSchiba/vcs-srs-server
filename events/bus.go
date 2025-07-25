package events

import (
	"sync"
	"time"
)

type EventBus struct {
	// Subscribe subscribes to an event with a given name and returns a channel to receive events.
	sync.RWMutex
	subscribers  map[string][]chan Event
	publishQueue []Event
}

func NewEventBus() *EventBus {
	eb := &EventBus{
		subscribers:  make(map[string][]chan Event),
		publishQueue: make([]Event, 0),
	}
	go eb.startPublisher()
	return eb
}

// startPublisher runs in a goroutine and dispatches events from the publishQueue.
func (eb *EventBus) startPublisher() {
	for {
		eb.Lock()
		if len(eb.publishQueue) == 0 {
			eb.Unlock()
			// Sleep briefly to avoid busy waiting
			time.Sleep(10 * time.Millisecond)
			continue
		}
		event := eb.publishQueue[0]
		eb.publishQueue = eb.publishQueue[1:]
		eb.Unlock()

		eb.dispatch(event)
	}
}

// dispatch sends the event to all subscribers.
func (eb *EventBus) dispatch(event Event) {
	eb.RLock()
	defer eb.RUnlock()
	var subsList [][]chan Event
	if subs, ok := eb.subscribers[event.Name]; ok {
		subsList = append(subsList, subs)
	}
	if subs, ok := eb.subscribers["*"]; ok {
		subsList = append(subsList, subs)
	}
	for _, subs := range subsList {
		for _, sub := range subs {
			select {
			case sub <- event:
			default:
			}
		}
	}
}

// Subscribe adds a new subscriber for a specific event type.
func (eb *EventBus) Subscribe(eventName string) chan Event {
	eb.Lock()
	defer eb.Unlock()

	ch := make(chan Event, 10) // Buffered channel to avoid blocking
	eb.subscribers[eventName] = append(eb.subscribers[eventName], ch)
	return ch
}

// Publish enqueues an event for dispatching.
func (eb *EventBus) Publish(event Event) {
	eb.Lock()
	defer eb.Unlock()
	eb.publishQueue = append(eb.publishQueue, event)
}
