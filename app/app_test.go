package app

import (
	"context"
	"testing"

	"github.com/FPGSchiba/vcs-srs-server/events"
	"github.com/FPGSchiba/vcs-srs-server/state"
	"go.uber.org/zap"
)

func newTestApp() *App {
	logger := zap.NewNop()
	settings := &state.SettingsState{}
	admin := &state.AdminState{}
	server := &state.ServerState{
		Clients:      make(map[string]*state.ClientState),
		RadioClients: make(map[string]*state.RadioState),
		BannedState:  state.BannedState{BannedClients: []state.BannedClient{}},
	}
	return &App{
		ServerState:   server,
		SettingsState: settings,
		AdminState:    admin,
		logger:        logger,
		StopSignals:   make(map[string]chan struct{}),
	}
}

func TestGetServerVersion(t *testing.T) {
	app := newTestApp()
	got := app.GetServerVersion()
	want := Version
	if got != want {
		t.Errorf("GetServerVersion() = %v, want %v", got, want)
	}
}

func TestNotify(t *testing.T) {
	app := newTestApp()
	app.ctx = context.Background()
	notification := events.Notification{
		Title:   "Test",
		Message: "Test message",
		Level:   "info",
	}
	// Should not panic or error
	app.Notify(notification)
}

func TestGetServerStatus(t *testing.T) {
	app := newTestApp()
	status := app.GetServerStatus()
	if status == nil {
		t.Error("GetServerStatus() returned nil")
	}
}
