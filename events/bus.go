package events

import "sync"

// EventBus dispatches events to registered subscribers via a buffered channel.
type EventBus struct {
	mu          sync.RWMutex
	subscribers map[string][]chan Event
	queue       chan Event
	stop        chan struct{}
	once        sync.Once
}

func NewEventBus() *EventBus {
	eb := &EventBus{
		subscribers: make(map[string][]chan Event),
		queue:       make(chan Event, 256),
		stop:        make(chan struct{}),
	}
	go eb.startPublisher()
	return eb
}

// startPublisher dispatches events from the queue until Stop is called.
func (eb *EventBus) startPublisher() {
	for {
		select {
		case event := <-eb.queue:
			eb.dispatch(event)
		case <-eb.stop:
			eb.closeSubscribers()
			return
		}
	}
}

// closeSubscribers closes all subscriber channels so that range-based consumers exit cleanly.
// Called from startPublisher when the bus shuts down.
func (eb *EventBus) closeSubscribers() {
	eb.mu.Lock()
	defer eb.mu.Unlock()
	for name, subs := range eb.subscribers {
		for _, ch := range subs {
			close(ch)
		}
		delete(eb.subscribers, name)
	}
}

// dispatch sends the event to all matching and wildcard subscribers.
func (eb *EventBus) dispatch(event Event) {
	eb.mu.RLock()
	defer eb.mu.RUnlock()
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
				// channel full — drop to avoid blocking publisher
			}
		}
	}
}

// Subscribe registers a channel to receive events with the given name.
// Use "*" to receive all events.
func (eb *EventBus) Subscribe(eventName string) chan Event {
	eb.mu.Lock()
	defer eb.mu.Unlock()
	ch := make(chan Event, 10)
	eb.subscribers[eventName] = append(eb.subscribers[eventName], ch)
	return ch
}

// Unsubscribe removes the channel from all subscriber lists and closes it
// so that range-based consumers exit cleanly.
func (eb *EventBus) Unsubscribe(ch chan Event) {
	eb.mu.Lock()
	found := false
	for name, subs := range eb.subscribers {
		for i, sub := range subs {
			if sub == ch {
				eb.subscribers[name] = append(subs[:i], subs[i+1:]...)
				found = true
				break
			}
		}
	}
	eb.mu.Unlock()
	if found {
		// Close the channel so range-based consumers exit.
		// Use recover in case closeSubscribers already closed it in a race.
		func() {
			defer func() { recover() }() //nolint:errcheck
			close(ch)
		}()
	}
}

// Publish enqueues an event for dispatching. Non-blocking: drops if queue is full.
func (eb *EventBus) Publish(event Event) {
	select {
	case eb.queue <- event:
	default:
		// queue full — drop event
	}
}

// Stop shuts down the publisher goroutine. Safe to call multiple times.
func (eb *EventBus) Stop() {
	eb.once.Do(func() { close(eb.stop) })
}
