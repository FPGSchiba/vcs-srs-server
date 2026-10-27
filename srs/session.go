package srs

import (
	"sync"

	pb "github.com/FPGSchiba/vcs-srs-server/srspb"
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
