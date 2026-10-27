package srs

import (
	"sync"

	"github.com/FPGSchiba/vcs-srs-server/events"
	pb "github.com/FPGSchiba/vcs-srs-server/srspb"
	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// subscription is the identity of one SubscribeToUpdates call.
//
// Invariant: the server unregisters a subscription from s.streams (under
// s.mu) before ending it; a handler that finds itself still registered when
// it exits was ended by the client (context done or Send failed), and only
// then does a grace period start.
type subscription struct {
	done  chan struct{}
	once  sync.Once
	final *pb.ServerUpdate // sent by the handler before it returns; may be nil
	err   error            // what the handler returns
}

func newSubscription() *subscription {
	return &subscription{done: make(chan struct{})}
}

// end records final and err and wakes the handler. The first call wins; it
// reports whether this call was the one that ended the subscription.
func (sub *subscription) end(final *pb.ServerUpdate, err error) bool {
	ended := false
	sub.once.Do(func() {
		sub.final = final
		sub.err = err
		close(sub.done)
		ended = true
	})
	return ended
}

// ended reports, without blocking, whether end has been called.
func (sub *subscription) ended() bool {
	select {
	case <-sub.done:
		return true
	default:
		return false
	}
}

var (
	errSuperseded = status.Error(codes.Aborted, "superseded by a newer subscription")
	errNoSession  = status.Error(codes.Unauthenticated, "session not found: log in again")
)

// RemovalReason says why a session was removed. It is part of the status
// message a live update stream ends with ("session ended: <reason>").
type RemovalReason string

const (
	ReasonDisconnect RemovalReason = "disconnected"
	ReasonKicked     RemovalReason = "kicked"
	ReasonBanned     RemovalReason = "banned"
	ReasonStale      RemovalReason = "stale"       // live stream, no RPC for staleAfter
	ReasonStreamLost RemovalReason = "stream lost" // no stream for streamGrace
)

// SessionEndedError is the status a live update stream ends with when the
// server removes its session.
func SessionEndedError(reason RemovalReason) error {
	return status.Errorf(codes.Unauthenticated, "session ended: %s", reason)
}

// VoiceSessionDropper drops a client's voice binding. Drops are idempotent:
// dropping a client that is not bound is a no-op.
type VoiceSessionDropper interface {
	DropVoiceSession(clientID uuid.UUID, reason string)
}

// VoiceSessionDroppers fans a drop out to several droppers, skipping nil
// entries (a nil interface or a typed nil pointer is never called).
type VoiceSessionDroppers []VoiceSessionDropper

func (ds VoiceSessionDroppers) DropVoiceSession(clientID uuid.UUID, reason string) {
	for _, d := range ds {
		if d != nil {
			d.DropVoiceSession(clientID, reason)
		}
	}
}

// RemoveClient removes a session for good: both state maps, the update
// stream (which sends final, if non-nil, then ends with
// SessionEndedError(reason)), the voice binding, and exactly one ClientLeft
// event. It reports whether the client existed. Safe to call concurrently;
// only one caller observes true.
func (s *SimpleRadioServer) RemoveClient(id uuid.UUID, reason RemovalReason, final *pb.ServerUpdate) bool {
	return s.removeClientIf(id, reason, final, nil)
}

// removeClientIf is RemoveClient with a precondition evaluated while both
// s.mu and serverState are held; when cond returns false nothing changes.
// Lock order: s.mu, then serverState. The subscription end, the voice drop
// and the event are done after both locks are released.
func (s *SimpleRadioServer) removeClientIf(id uuid.UUID, reason RemovalReason, final *pb.ServerUpdate, cond func() bool) bool {
	s.mu.Lock()
	s.serverState.Lock()
	if cond != nil && !cond() {
		s.serverState.Unlock()
		s.mu.Unlock()
		return false
	}
	_, inClients := s.serverState.Clients[id]
	_, inRadios := s.serverState.RadioClients[id]
	existed := inClients || inRadios
	delete(s.serverState.Clients, id)
	delete(s.serverState.RadioClients, id)
	var snap map[uuid.UUID]*state.ClientState
	if existed {
		snap = make(map[uuid.UUID]*state.ClientState, len(s.serverState.Clients))
		for k, v := range s.serverState.Clients {
			if v == nil {
				continue
			}
			c := *v
			snap[k] = &c
		}
	}
	s.serverState.Unlock()
	sub := s.streams[id]
	delete(s.streams, id)
	delete(s.detachedAt, id)
	s.mu.Unlock()

	if sub != nil {
		sub.end(final, SessionEndedError(reason))
	}
	if s.dropper != nil {
		s.dropper.DropVoiceSession(id, string(reason))
	}
	if existed {
		s.logger.Info("Removed client session", "client_id", id, "reason", string(reason))
		s.eventBus.Publish(events.Event{
			Name: events.ClientsChanged,
			Data: events.ClientChangeEvent{Type: events.ClientLeft, ClientID: id, Clients: snap},
		})
	}
	return existed
}
