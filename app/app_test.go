package app

import (
	"testing"

	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/google/uuid"
)

func newTestApp() *VCSApplication {
	settings := &state.SettingsState{}
	admin := &state.AdminState{}
	server := &state.ServerState{
		Clients:      make(map[uuid.UUID]*state.ClientState),
		RadioClients: make(map[uuid.UUID]*state.RadioState),
		BannedState:  state.BannedState{BannedClients: []state.BannedClient{}},
	}
	return &VCSApplication{
		ServerState:   server,
		SettingsState: settings,
		AdminState:    admin,
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

func TestGetServerStatus(t *testing.T) {
	app := newTestApp()
	status := app.GetServerStatus()
	if status == nil {
		t.Error("GetServerStatus() returned nil")
	}
}
