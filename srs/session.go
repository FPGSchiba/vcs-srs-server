package srs

import (
	"sync"
	"time"

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

// streamGrace is how long a session survives without an update stream,
// measured from the later of the stream's end and the client's last RPC.
const streamGrace = 120 * time.Second

// reapReasonLocked decides whether the reaper should remove id at now.
// The caller holds s.mu and serverState (read or write).
//   - live stream, no RPC for staleAfter            -> ReasonStale
//   - no stream for streamGrace (since the later of
//     the stream's end and the last RPC)             -> ReasonStreamLost
//   - a subscription whose client is gone (orphan)  -> ReasonStale
func (s *SimpleRadioServer) reapReasonLocked(id uuid.UUID, now time.Time) (RemovalReason, bool) {
	_, subscribed := s.streams[id]
	client, exists := s.serverState.Clients[id]
	switch {
	case !exists:
		if subscribed {
			return ReasonStale, true
		}
		return "", false
	case client == nil:
		return ReasonStale, true
	case subscribed:
		if now.Sub(client.LastUpdate) > s.staleAfter {
			return ReasonStale, true
		}
		return "", false
	default:
		since := client.LastUpdate
		if d, ok := s.detachedAt[id]; ok && d.After(since) {
			since = d
		}
		if now.Sub(since) > streamGrace {
			return ReasonStreamLost, true
		}
		return "", false
	}
}

// reapCond re-validates a reaper decision inside removeClientIf: the removal
// goes ahead only if the same reason still holds under both locks, so a
// client that resubscribed or sent an RPC after collection is kept.
func (s *SimpleRadioServer) reapCond(id uuid.UUID, now time.Time, reason RemovalReason) func() bool {
	return func() bool {
		r, ok := s.reapReasonLocked(id, now)
		return ok && r == reason
	}
}

// reapOnce removes every session reapReasonLocked condemns at now and
// returns the removed ids.
func (s *SimpleRadioServer) reapOnce(now time.Time) []uuid.UUID {
	type candidate struct {
		id     uuid.UUID
		reason RemovalReason
	}
	var candidates []candidate
	s.mu.Lock()
	s.serverState.RLock()
	for id := range s.serverState.Clients {
		if r, ok := s.reapReasonLocked(id, now); ok {
			candidates = append(candidates, candidate{id, r})
		}
	}
	for id := range s.streams {
		if _, known := s.serverState.Clients[id]; known {
			continue
		}
		if r, ok := s.reapReasonLocked(id, now); ok {
			candidates = append(candidates, candidate{id, r})
		}
	}
	s.serverState.RUnlock()
	s.mu.Unlock()

	var removed []uuid.UUID
	for _, c := range candidates {
		// An orphan subscription (client already gone) is ended here too;
		// removeClientIf reports false for it because nothing existed.
		if s.removeClientIf(c.id, c.reason, nil, s.reapCond(c.id, now, c.reason)) {
			removed = append(removed, c.id)
		}
	}
	return removed
}
