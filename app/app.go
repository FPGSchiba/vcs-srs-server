package app

import (
	"github.com/FPGSchiba/vcs-srs-server/control"
	"github.com/FPGSchiba/vcs-srs-server/events"
	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/FPGSchiba/vcs-srs-server/voice"
	"github.com/wailsapp/wails/v3/pkg/application"
	"log/slog"
	"net/http"
)

const Version = "v0.1.0"

// VCSApplication struct
type VCSApplication struct {
	ServerState       *state.ServerState
	SettingsState     *state.SettingsState
	AdminState        *state.AdminState
	DistributionState *state.DistributionState
	autoStart         bool
	httpServer        *http.Server
	voiceServer       *voice.Server
	controlServer     *control.Server // Add this
	StopSignals       map[string]chan struct{}
	App               *application.App
	Logger            *slog.Logger // Optional Logger, only used in headless mode
}

// New creates a new App application struct
func New() *VCSApplication {
	return &VCSApplication{
		ServerState:       &state.ServerState{},
		SettingsState:     &state.SettingsState{},
		AdminState:        &state.AdminState{},
		DistributionState: &state.DistributionState{},
		autoStart:         false,
		httpServer:        nil,
		voiceServer:       nil,
		controlServer:     nil, // Initialize control server
		StopSignals:       make(map[string]chan struct{}),
		App:               nil,
	}
}

func (a *VCSApplication) StartUp(app *application.App, configFilePath, bannedFilePath string, autoStartServers bool) {
	settingsState, err := state.GetSettingsState(configFilePath)
	if err != nil {
		app.Logger.Error("Failed to load settings", "error", err)
		panic(err) // Without settings, we can't run
	}

	adminState := &state.AdminState{
		HTTPStatus: state.ServiceStatus{
			IsRunning: false,
			IsNeeded:  true,
			Error:     "",
		},
		VoiceStatus: state.ServiceStatus{
			IsRunning: false,
			IsNeeded:  true,
			Error:     "",
		},
		ControlStatus: state.ServiceStatus{
			IsRunning: false,
			IsNeeded:  true,
			Error:     "",
		},
	}

	bannedState, err := state.GetBannedState(bannedFilePath)
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

	distributionState := &state.DistributionState{
		DistributionMode: state.DistributionModeStandalone, // Default to Standalone
		RuntimeMode:      state.RuntimeModeGUI,
	}

	a.ServerState = serverState
	a.SettingsState = settingsState
	a.AdminState = adminState
	a.DistributionState = distributionState
	a.autoStart = autoStartServers
	a.Logger = app.Logger
	a.App = app

	if autoStartServers {
		a.StartStandaloneServer()
	}
}

func (a *VCSApplication) HeadlessStartup(logger *slog.Logger, configFilePath, bannedFilePath string, distributionMode int8) {
	settingsState, err := state.GetSettingsState(configFilePath)
	if err != nil {
		panic(err) // Without settings, we can't run
	}

	adminState := &state.AdminState{
		HTTPStatus: state.ServiceStatus{
			IsRunning: false,
			IsNeeded:  distributionMode == state.DistributionModeStandalone || distributionMode == state.DistributionModeControl,
			Error:     "",
		},
		VoiceStatus: state.ServiceStatus{
			IsRunning: false,
			IsNeeded:  distributionMode == state.DistributionModeStandalone || distributionMode == state.DistributionModeVoice,
			Error:     "",
		},
		ControlStatus: state.ServiceStatus{
			IsRunning: false,
			IsNeeded:  distributionMode == state.DistributionModeStandalone || distributionMode == state.DistributionModeControl,
			Error:     "",
		},
	}

	bannedState, err := state.GetBannedState(bannedFilePath)
	if err != nil {
		bannedState = &state.BannedState{
			BannedClients: make([]state.BannedClient, 0),
		}
		err = bannedState.Save()
		if err != nil {
			panic(err)
		}
	}

	serverState := &state.ServerState{
		Clients:      make(map[string]*state.ClientState),
		RadioClients: make(map[string]*state.RadioState),
		BannedState:  *bannedState,
	}

	distributionState := &state.DistributionState{
		DistributionMode: distributionMode,
		RuntimeMode:      state.RuntimeModeHeadless,
	}

	a.ServerState = serverState
	a.SettingsState = settingsState
	a.AdminState = adminState
	a.DistributionState = distributionState
	a.autoStart = true
	a.Logger = logger
	a.App = nil // No application context in headless mode

	switch distributionMode {
	case state.DistributionModeStandalone:
		a.StartStandaloneServer()
		break
	case state.DistributionModeControl:
		a.StartControlServer()
		break
	case state.DistributionModeVoice:
		a.StartVoiceServer()
		break
	}
}

// StartStandaloneServer starts the Control, HTTP and Voice servers
func (a *VCSApplication) StartStandaloneServer() {
	a.AdminState.Lock()
	if a.AdminState.HTTPStatus.IsRunning ||
		a.AdminState.VoiceStatus.IsRunning ||
		a.AdminState.ControlStatus.IsRunning {
		a.AdminState.Unlock()
		return
	}
	a.AdminState.Unlock()

	a.startGrpcServer()
	a.startVoiceServer()
	a.startHTTPServer()

	return
}

func (a *VCSApplication) StartControlServer() {
	a.AdminState.Lock()
	if a.AdminState.ControlStatus.IsRunning {
		a.AdminState.Unlock()
		return
	}
	a.AdminState.Unlock()

	a.startGrpcServer()
	a.startHTTPServer()

	return
}

func (a *VCSApplication) StartVoiceServer() {
	a.AdminState.Lock()
	if a.AdminState.VoiceStatus.IsRunning {
		a.AdminState.Unlock()
		return
	}
	a.AdminState.Unlock()

	a.startVoiceServer()

	return
}

// StopServer stops the Control, HTTP and Voice servers
func (a *VCSApplication) StopServer() {
	a.AdminState.RLock()
	if !a.AdminState.HTTPStatus.IsRunning &&
		!a.AdminState.VoiceStatus.IsRunning &&
		!a.AdminState.ControlStatus.IsRunning {
		a.AdminState.RUnlock()
		return
	}
	a.AdminState.RUnlock()

	a.DistributionState.RLock()
	if a.DistributionState.DistributionMode == state.DistributionModeVoice || a.DistributionState.DistributionMode == state.DistributionModeStandalone {
		a.stopVoiceServer()
	}
	if a.DistributionState.DistributionMode == state.DistributionModeControl || a.DistributionState.DistributionMode == state.DistributionModeStandalone {
		a.stopHTTPServer()
		a.stopControlServer()
	}
	a.DistributionState.RUnlock()

	return
}

// GetServerStatus returns the status of the Control, HTTP and Voice servers
func (a *VCSApplication) GetServerStatus() *state.AdminState {
	a.AdminState.RLock()
	defer a.AdminState.RUnlock()
	return a.AdminState
}

func (a *VCSApplication) GetServerVersion() string {
	return Version
}

func (a *VCSApplication) Notify(notification events.Notification) {
	a.EmitEvent(events.Event{
		Name: events.NotificationEvent,
		Data: notification,
	})
	a.Logger.Info("Notification", "title", notification.Title, "message", notification.Message, "level", notification.Level)
}

func (a *VCSApplication) EmitEvent(event events.Event) {
	a.DistributionState.RLock()
	if a.DistributionState.RuntimeMode == state.RuntimeModeGUI { // Only emit events if in GUI mode
		a.DistributionState.RUnlock()
		a.App.EmitEvent(event.Name, event.Data)
	}
}
