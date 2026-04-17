package logging

import (
	"context"
	"log/slog"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/events"
)

// LogEntry is the payload published to the event bus for each log record.
type LogEntry struct {
	Time    time.Time      `json:"time"`
	Level   string         `json:"level"`
	Message string         `json:"msg"`
	Attrs   map[string]any `json:"attrs,omitempty"`
}

// BusHandler is a slog.Handler that publishes log records to an events.EventBus.
type BusHandler struct {
	bus   *events.EventBus
	attrs []slog.Attr
	group string
}

// NewBusHandler returns a BusHandler that publishes to bus.
func NewBusHandler(bus *events.EventBus) *BusHandler {
	return &BusHandler{bus: bus}
}

// Enabled always returns true so all levels reach the event bus.
func (h *BusHandler) Enabled(_ context.Context, _ slog.Level) bool {
	return true
}

// Handle converts the record to LogEntry and publishes it.
func (h *BusHandler) Handle(_ context.Context, r slog.Record) error {
	attrs := make(map[string]any, len(h.attrs)+r.NumAttrs())

	for _, a := range h.attrs {
		attrs[a.Key] = a.Value.Any()
	}

	r.Attrs(func(a slog.Attr) bool {
		key := a.Key
		if h.group != "" {
			key = h.group + "." + key
		}
		attrs[key] = a.Value.Any()
		return true
	})

	entry := LogEntry{
		Time:    r.Time,
		Level:   r.Level.String(),
		Message: r.Message,
		Attrs:   attrs,
	}
	h.bus.Publish(events.Event{Name: events.LogEntry, Data: entry})
	return nil
}

// WithAttrs returns a new BusHandler with the given attrs accumulated.
func (h *BusHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	combined := make([]slog.Attr, len(h.attrs)+len(attrs))
	copy(combined, h.attrs)
	copy(combined[len(h.attrs):], attrs)
	return &BusHandler{bus: h.bus, attrs: combined, group: h.group}
}

// WithGroup returns a new BusHandler with the given group prefix.
func (h *BusHandler) WithGroup(name string) slog.Handler {
	g := name
	if h.group != "" {
		g = h.group + "." + name
	}
	return &BusHandler{bus: h.bus, attrs: h.attrs, group: g}
}
