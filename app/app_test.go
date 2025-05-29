package app

import (
	"testing"

	"github.com/FPGSchiba/vcs-srs-server/state"
)

func newTestApp() *VCSApplication {
	settings := &state.SettingsState{}
	admin := &state.AdminState{}
	server := &state.ServerState{
		Clients:      make(map[string]*state.ClientState),
		RadioClients: make(map[string]*state.RadioState),
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
