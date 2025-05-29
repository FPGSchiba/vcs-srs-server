package app

import (
	"github.com/FPGSchiba/vcs-srs-server/control"
	"github.com/FPGSchiba/vcs-srs-server/events"
	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/FPGSchiba/vcs-srs-server/voice"
	"github.com/wailsapp/wails/v3/pkg/application"
	"net/http"
)

const Version = "v0.1.0"

// VCSApplication struct
type VCSApplication struct {
	ServerState   *state.ServerState
	SettingsState *state.SettingsState
	AdminState    *state.AdminState
	autoStart     bool
	httpServer    *http.Server
	voiceServer   *voice.Server
	controlServer *control.Server // Add this
	StopSignals   map[string]chan struct{}
	App           *application.App
}

// New creates a new App application struct
func New() *VCSApplication {
	return &VCSApplication{
		ServerState:   &state.ServerState{},
		SettingsState: &state.SettingsState{},
		AdminState:    &state.AdminState{},
		autoStart:     false,
		httpServer:    nil,
		voiceServer:   nil,
		controlServer: nil, // Initialize control server
		StopSignals:   make(map[string]chan struct{}),
		App:           nil,
	}
}

func (a *VCSApplication) StartUp(app *application.App, configFilePath string, autoStartServers bool) {
	settingsState, err := state.GetSettingsState(configFilePath)
	if err != nil {
		app.Logger.Error("Failed to load settings", "error", err)
		panic(err) // Without settings, we can't run
	}

	adminState := &state.AdminState{
		HTTPStatus: state.ServiceStatus{
			IsRunning: false,
			Error:     "",
		},
		VoiceStatus: state.ServiceStatus{
			IsRunning: false,
			Error:     "",
		},
		ControlStatus: state.ServiceStatus{
			IsRunning: false,
			Error:     "",
		},
	}

	bannedState, err := state.GetBannedState()
	if err != nil {
		app.Logger.Error("Failed to load banned clients", "error", err)
		bannedState = &state.BannedState{
			BannedClients: make([]state.BannedClient, 0),
		}
		err = bannedState.Save()
		if err != nil {
			app.Logger.Error("Failed to initialize Banned Clients file", "error", err)
			panic(err)
		}
	}

	serverState := &state.ServerState{
		Clients:      make(map[string]*state.ClientState),
		RadioClients: make(map[string]*state.RadioState),
		BannedState:  *bannedState,
	}

	a.ServerState = serverState
	a.SettingsState = settingsState
	a.AdminState = adminState
	a.autoStart = autoStartServers
	a.App = app

	if autoStartServers {
		a.StartServer()
	}
}

// StartServer starts the HTTP and Voice servers
func (a *VCSApplication) StartServer() {
	a.AdminState.Lock()
	if a.AdminState.HTTPStatus.IsRunning ||
		a.AdminState.VoiceStatus.IsRunning ||
		a.AdminState.ControlStatus.IsRunning {
		a.AdminState.Unlock()
		return
	}
	a.AdminState.Unlock()

	a.startControlServer()
	a.startVoiceServer()
	a.startHTTPServer()

	return
}

// StopServer starts the HTTP and Voice servers
func (a *VCSApplication) StopServer() {
	a.AdminState.RLock()
	if !a.AdminState.HTTPStatus.IsRunning &&
		!a.AdminState.VoiceStatus.IsRunning &&
		!a.AdminState.ControlStatus.IsRunning {
		a.AdminState.RUnlock()
		return
	}
	a.AdminState.RUnlock()

	a.stopHTTPServer()
	a.stopVoiceServer()
	a.stopControlServer()

	return
}

// GetServerStatus returns the status of the HTTP and Voice servers
func (a *VCSApplication) GetServerStatus() *state.AdminState {
	a.AdminState.RLock()
	defer a.AdminState.RUnlock()
	return a.AdminState
}

func (a *VCSApplication) GetServerVersion() string {
	return Version
}

func (a *VCSApplication) Notify(notification events.Notification) {
	a.App.EmitEvent(events.NotificationEvent, notification)
	a.App.Logger.Info("Notification", "title", notification.Title, "message", notification.Message, "level", notification.Level)
}
