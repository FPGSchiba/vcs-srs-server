package app

import (
	"log/slog"
	"net/http"

	"github.com/FPGSchiba/vcs-srs-server/control"
	"github.com/FPGSchiba/vcs-srs-server/events"
	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/FPGSchiba/vcs-srs-server/voice"
	"github.com/google/uuid"
)

const Version = "v0.1.0"

// VCSApplication struct
type VCSApplication struct {
	guiApp            // GUI-only fields (App *application.App); empty struct in headless builds
	ServerState       *state.ServerState
	SettingsState     *state.SettingsState
	AdminState        *state.AdminState
	DistributionState *state.DistributionState
	autoStart         bool
	httpServer        *http.Server
	voiceServer       *voice.Server
	controlServer     *control.Server
	StopSignals       map[string]chan struct{}
	eventBus          *events.EventBus
	Logger            *slog.Logger
}

// New creates a new App application struct
func New() *VCSApplication {
	return &VCSApplication{
		ServerState:       &state.ServerState{},
		SettingsState:     &state.SettingsState{},
		AdminState:        &state.AdminState{},
		DistributionState: &state.DistributionState{},
		autoStart:         false,
		eventBus:          events.NewEventBus(),
		httpServer:        nil,
		voiceServer:       nil,
		controlServer:     nil,
		StopSignals:       make(map[string]chan struct{}),
	}
}

func (a *VCSApplication) HeadlessStartup(logger *slog.Logger, configFilePath, bannedFilePath string, distributionMode uint8, isGlobal bool) {
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
		Clients:      make(map[uuid.UUID]*state.ClientState),
		RadioClients: make(map[uuid.UUID]*state.RadioState),
		BannedState:  *bannedState,
	}

	distributionState := &state.DistributionState{
		DistributionMode: distributionMode,
		RuntimeMode:      state.RuntimeModeHeadless,
		IsGlobal:         isGlobal,
	}

	a.ServerState = serverState
	a.SettingsState = settingsState
	a.AdminState = adminState
	a.DistributionState = distributionState
	a.autoStart = true
	a.Logger = logger

	switch distributionMode {
	case state.DistributionModeStandalone:
		a.StartStandaloneServer()
	case state.DistributionModeControl:
		a.StartControlServer()
	case state.DistributionModeVoice:
		a.StartVoiceServer()
	}
}

// StartStandaloneServer starts the Control, HTTP and Voice servers
func (a *VCSApplication) StartStandaloneServer() {
	a.startGrpcServer()
	a.startVoiceServer()
	a.startHTTPServer()
}

func (a *VCSApplication) StartControlServer() {
	a.startGrpcServer()
	a.startHTTPServer()
}

func (a *VCSApplication) StartVoiceServer() {
	a.startVoiceServer()
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

	a.eventBus.Stop()
}

// GetServerStatus returns the status of the Control, HTTP and Voice servers
func (a *VCSApplication) GetServerStatus() state.AdminStateSnapshot {
	a.AdminState.RLock()
	defer a.AdminState.RUnlock()
	return state.AdminStateSnapshot{
		HTTPStatus:    a.AdminState.HTTPStatus,
		VoiceStatus:   a.AdminState.VoiceStatus,
		ControlStatus: a.AdminState.ControlStatus,
	}
}

func (a *VCSApplication) GetServerVersion() string {
	return Version
}

func (a *VCSApplication) GetEventBus() *events.EventBus {
	return a.eventBus
}

func (a *VCSApplication) GetSettingsSnapshot() state.SettingsSnapshot {
	return a.SettingsState.Snapshot()
}

func (a *VCSApplication) Notify(notification events.Notification) {
	a.EmitEvent(events.Event{
		Name: events.NotificationEvent,
		Data: notification,
	})
	a.Logger.Info("Notification", "title", notification.Title, "message", notification.Message, "level", notification.Level)
}

func (a *VCSApplication) EmitEvent(event events.Event) {
	a.eventBus.Publish(event)
}
