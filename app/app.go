package app

import (
	"context"
	"github.com/FPGSchiba/vcs-srs-server/control"
	"github.com/FPGSchiba/vcs-srs-server/events"
	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/FPGSchiba/vcs-srs-server/voice"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"go.uber.org/zap"
	"net/http"
)

const Version = "v0.1.0"

// App struct
type App struct {
	ctx           context.Context
	ServerState   *state.ServerState
	SettingsState *state.SettingsState
	AdminState    *state.AdminState
	logger        *zap.Logger
	autoStart     bool
	httpServer    *http.Server
	voiceServer   *voice.Server
	controlServer *control.Server // Add this
	StopSignals   map[string]chan struct{}
}

// NewApp creates a new App application struct
func NewApp(logger *zap.Logger, configFilePath string, autoStartServers bool) *App {
	settingsState, err := state.GetSettingsState(configFilePath)
	if err != nil {
		logger.Error("Failed to load settings", zap.Error(err))
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
		logger.Error("Failed to load banned clients", zap.Error(err))
		bannedState = &state.BannedState{
			BannedClients: make([]state.BannedClient, 0),
		}
		err = bannedState.Save()
		if err != nil {
			logger.Error("Failed to initialize Banned Clients file", zap.Error(err))
			panic(err)
		}
	}

	serverState := &state.ServerState{
		Clients:      make(map[string]*state.ClientState),
		RadioClients: make(map[string]*state.RadioState),
		BannedState:  *bannedState,
	}

	return &App{
		ServerState:   serverState,
		SettingsState: settingsState,
		AdminState:    adminState,
		logger:        logger,
		StopSignals:   make(map[string]chan struct{}),
		autoStart:     autoStartServers,
	}
}

// Startup is called when the app starts. The context is saved
// so we can call the runtime methods
func (a *App) Startup(ctx context.Context) {
	a.ctx = ctx
	a.setupWindow()
	if a.autoStart {
		a.StartServer()
	}
}

// StartServer starts the HTTP and Voice servers
func (a *App) StartServer() {
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
func (a *App) StopServer() {
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
func (a *App) GetServerStatus() *state.AdminState {
	a.AdminState.RLock()
	defer a.AdminState.RUnlock()
	return a.AdminState
}

func (a *App) GetServerVersion() string {
	return Version
}

func (a *App) Notify(notification events.Notification) {
	runtime.EventsEmit(a.ctx, events.NotificationEvent, notification)
	a.logger.Info("Notification", zap.String("title", notification.Title), zap.String("message", notification.Message), zap.String("level", notification.Level))
}
