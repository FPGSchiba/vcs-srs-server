package events_test

import (
	"testing"

	"github.com/FPGSchiba/vcs-srs-server/events"
	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/google/uuid"
)

func TestClientChangeEvent_Fields(t *testing.T) {
	id := uuid.New()
	clients := map[uuid.UUID]*state.ClientState{id: {Name: "Alice"}}
	evt := events.ClientChangeEvent{
		Type:     events.ClientJoined,
		ClientID: id,
		Clients:  clients,
	}
	if evt.Type != events.ClientJoined {
		t.Fatalf("expected ClientJoined, got %v", evt.Type)
	}
	if evt.ClientID != id {
		t.Fatalf("expected %v, got %v", id, evt.ClientID)
	}
	if len(evt.Clients) != 1 {
		t.Fatalf("expected 1 client, got %d", len(evt.Clients))
	}
}

func TestServerActionEvent_Fields(t *testing.T) {
	id := uuid.New()
	evt := events.ServerActionEvent{
		ActionType:     events.ActionKick,
		TargetClientID: id,
		Reason:         "cheating",
	}
	if evt.ActionType != events.ActionKick {
		t.Fatalf("expected ActionKick, got %v", evt.ActionType)
	}
	if evt.Reason != "cheating" {
		t.Fatalf("expected 'cheating', got %q", evt.Reason)
	}
}

func TestLogEntry_Fields(t *testing.T) {
	if events.LogEntry != "logs/entry" {
		t.Fatalf("expected 'logs/entry', got %q", events.LogEntry)
	}
}
