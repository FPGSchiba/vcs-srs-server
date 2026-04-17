package logging_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/events"
	"github.com/FPGSchiba/vcs-srs-server/logging"
)

func TestBusHandler_PublishesLogEntry(t *testing.T) {
	bus := events.NewEventBus()
	ch := bus.Subscribe(events.LogEntry)
	defer bus.Unsubscribe(ch)

	h := logging.NewBusHandler(bus)
	logger := slog.New(h)

	logger.Info("test message", "key", "value")

	select {
	case evt := <-ch:
		entry, ok := evt.Data.(logging.LogEntry)
		if !ok {
			t.Fatalf("expected LogEntry, got %T", evt.Data)
		}
		if entry.Message != "test message" {
			t.Fatalf("expected 'test message', got %q", entry.Message)
		}
		if entry.Level != "INFO" {
			t.Fatalf("expected 'INFO', got %q", entry.Level)
		}
		if entry.Attrs["key"] != "value" {
			t.Fatalf("expected attrs[key]=value, got %v", entry.Attrs["key"])
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for log event")
	}
}

func TestBusHandler_Enabled_AlwaysTrue(t *testing.T) {
	bus := events.NewEventBus()
	h := logging.NewBusHandler(bus)
	if !h.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("expected Enabled=true for DEBUG")
	}
}
