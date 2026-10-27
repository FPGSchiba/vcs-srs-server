package events

import (
	"sync"
	"sync/atomic"
)

const (
	// defaultQueueSize is the capacity of the bus's dispatch queue.
	defaultQueueSize = 1024
	// legacyBuffer is the channel capacity of a legacy Subscribe channel.
	legacyBuffer = 64
)

// Subscription is an overflow-aware subscription to a set of event names,
// created by SubscribeNames. Events arrive on C. If C is ever full when an
// event is dispatched, the subscription overflows: Overflow() is closed, and
// from then on it receives nothing more (each skipped event is counted in
// Dropped). A consumer that sees Overflow() has missed events and must resync.
type Subscription struct {
	C <-chan Event

	ch           chan Event
	bus          *EventBus
	overflow     chan struct{}
	overflowOnce sync.Once
	overflowed   atomic.Bool
	dropped      atomic.Uint64
	closeOnce    sync.Once
}

// Overflow returns a channel that is closed once, on the subscription's first drop.
func (s *Subscription) Overflow() <-chan struct{} { return s.overflow }

// Dropped reports how many events this subscription has missed.
func (s *Subscription) Dropped() uint64 { return s.dropped.Load() }

// Close unregisters the subscription and closes C. It is idempotent and safe
// to call after the bus has stopped.
func (s *Subscription) Close() {
	s.bus.mu.Lock()
	defer s.bus.mu.Unlock()
	s.bus.removeLocked(func(sub *subscriber) bool { return sub.sub == s })
	s.closeLocked()
}

// closeLocked closes C exactly once. The caller holds bus.mu (write), so no
// dispatch can be sending on it concurrently.
func (s *Subscription) closeLocked() {
	s.closeOnce.Do(func() { close(s.ch) })
}

// Stats are the bus's drop counters, for tests and diagnostics.
type Stats struct {
	// LogDropped counts LogEntry events dropped because the queue was full.
	LogDropped uint64
	// LegacyDropped counts events dropped because a legacy Subscribe channel was full.
	LegacyDropped uint64
	// Overflows counts SubscribeNames subscriptions that have overflowed.
	Overflows uint64
}

// subscriber is one registration. sub == nil marks a legacy Subscribe channel.
type subscriber struct {
	ch  chan Event
	sub *Subscription
}

// EventBus dispatches events to registered subscribers via a buffered queue.
//
// The bus never logs: a log line is itself a LogEntry event and would feed
// back into the bus. Drops are only counted (see Stats).
type EventBus struct {
	mu          sync.RWMutex
	subscribers map[string][]*subscriber
	queue       chan Event
	stop        chan struct{}
	once        sync.Once

	logDropped    atomic.Uint64
	legacyDropped atomic.Uint64
	overflows     atomic.Uint64
}

// NewEventBus creates a bus and starts its dispatcher.
func NewEventBus() *EventBus {
	eb := newEventBus(defaultQueueSize)
	go eb.startPublisher()
	return eb
}

// newEventBus creates a bus with the given queue size. It does NOT start the
// dispatcher, so tests can hold a full queue deterministically.
func newEventBus(queueSize int) *EventBus {
	return &EventBus{
		subscribers: make(map[string][]*subscriber),
		queue:       make(chan Event, queueSize),
		stop:        make(chan struct{}),
	}
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
		for _, s := range subs {
			if s.sub != nil {
				s.sub.closeLocked()
			} else {
				closeQuietly(s.ch)
			}
		}
		delete(eb.subscribers, name)
	}
}

// closeQuietly closes ch, tolerating a channel that is already closed.
func closeQuietly(ch chan Event) {
	defer func() { recover() }() //nolint:errcheck
	close(ch)
}

// dispatch sends the event to all matching and wildcard subscribers. It never
// blocks on a subscriber.
func (eb *EventBus) dispatch(event Event) {
	eb.mu.RLock()
	defer eb.mu.RUnlock()
	for _, s := range eb.subscribers[event.Name] {
		eb.deliver(s, event)
	}
	if event.Name != "*" {
		for _, s := range eb.subscribers["*"] {
			eb.deliver(s, event)
		}
	}
}

// deliver sends event to one subscriber without blocking, applying the drop
// rules: a legacy channel drops and counts; a filtered subscription overflows
// once and then skips (and counts) everything.
func (eb *EventBus) deliver(s *subscriber, event Event) {
	defer func() { recover() }() //nolint:errcheck // send on a channel closed in a race
	if s.sub == nil {
		select {
		case s.ch <- event:
		default:
			eb.legacyDropped.Add(1)
		}
		return
	}
	sub := s.sub
	if sub.overflowed.Load() {
		sub.dropped.Add(1)
		return
	}
	select {
	case s.ch <- event:
	default:
		sub.dropped.Add(1)
		sub.overflowOnce.Do(func() {
			sub.overflowed.Store(true)
			eb.overflows.Add(1)
			close(sub.overflow)
		})
	}
}

// Subscribe registers a channel to receive events with the given name.
// Use "*" to receive all events. The channel is lossy: if it is full when an
// event is dispatched, that event is dropped for this subscriber (counted in
// Stats().LegacyDropped). Use SubscribeNames when a drop must be noticed.
func (eb *EventBus) Subscribe(eventName string) chan Event {
	eb.mu.Lock()
	defer eb.mu.Unlock()
	ch := make(chan Event, legacyBuffer)
	eb.subscribers[eventName] = append(eb.subscribers[eventName], &subscriber{ch: ch})
	return ch
}

// SubscribeNames registers an overflow-aware subscription with a buffer of
// size slots for the given event names. Duplicate names are collapsed; if
// "*" is among them the subscription is registered only under "*", so no
// event is delivered twice.
func (eb *EventBus) SubscribeNames(size int, names ...string) *Subscription {
	ch := make(chan Event, size)
	sub := &Subscription{
		C:        ch,
		ch:       ch,
		bus:      eb,
		overflow: make(chan struct{}),
	}
	s := &subscriber{ch: ch, sub: sub}

	unique := make([]string, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, n := range names {
		if n == "*" {
			unique = []string{"*"}
			break
		}
		if !seen[n] {
			seen[n] = true
			unique = append(unique, n)
		}
	}

	eb.mu.Lock()
	defer eb.mu.Unlock()
	for _, n := range unique {
		eb.subscribers[n] = append(eb.subscribers[n], s)
	}
	return sub
}

// removeLocked drops every registration matching match and reports whether
// anything was removed. The caller holds eb.mu (write).
func (eb *EventBus) removeLocked(match func(*subscriber) bool) bool {
	found := false
	for name, subs := range eb.subscribers {
		kept := make([]*subscriber, 0, len(subs))
		for _, s := range subs {
			if match(s) {
				found = true
				continue
			}
			kept = append(kept, s)
		}
		if len(kept) == 0 {
			delete(eb.subscribers, name)
		} else {
			eb.subscribers[name] = kept
		}
	}
	return found
}

// Unsubscribe removes the channel from all subscriber lists and closes it
// so that range-based consumers exit cleanly.
func (eb *EventBus) Unsubscribe(ch chan Event) {
	eb.mu.Lock()
	defer eb.mu.Unlock()
	var owner *Subscription
	found := eb.removeLocked(func(s *subscriber) bool {
		if s.ch == ch {
			owner = s.sub
			return true
		}
		return false
	})
	if !found {
		return
	}
	if owner != nil {
		owner.closeLocked()
		return
	}
	// Tolerate a channel closeSubscribers already closed in a race.
	closeQuietly(ch)
}

// Publish enqueues an event for dispatching.
//
// A LogEntry is lossy: if the queue is full it is dropped and counted in
// Stats().LogDropped. Every other (state) event is never dropped at the
// queue: Publish waits for queue space, or returns once the bus is stopped.
// The wait is bounded by the dispatcher, which never blocks on a subscriber
// and never publishes, so a publisher holding its own lock cannot deadlock it.
func (eb *EventBus) Publish(event Event) {
	if event.Name == LogEntry {
		select {
		case eb.queue <- event:
		default:
			eb.logDropped.Add(1)
		}
		return
	}
	select {
	case eb.queue <- event:
	case <-eb.stop:
	}
}

// Stats returns the bus's drop counters.
func (eb *EventBus) Stats() Stats {
	return Stats{
		LogDropped:    eb.logDropped.Load(),
		LegacyDropped: eb.legacyDropped.Load(),
		Overflows:     eb.overflows.Load(),
	}
}

// Stop shuts down the publisher goroutine. Safe to call multiple times.
func (eb *EventBus) Stop() {
	eb.once.Do(func() { close(eb.stop) })
}
