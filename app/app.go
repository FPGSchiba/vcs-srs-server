package app

import (
	"context"
	"fmt"
	"go.uber.org/zap"
	"net/http"
	"vcs-server/control"
	"vcs-server/state"
	"vcs-server/utils"
	"vcs-server/voice"
)

// App struct
type App struct {
	ctx           context.Context
	ServerState   *state.ServerState
	SettingsState *state.SettingsState
	AdminState    *state.AdminState
	logger        *zap.Logger
	httpServer    *http.Server
	voiceServer   *voice.Server
	controlServer *control.Server // Add this
}

// NewApp creates a new App application struct
func NewApp(logger *zap.Logger) *App {
	serverState := &state.ServerState{
		Clients: make(map[string]*state.ClientState),
	}

	settingsState := &state.SettingsState{
		// Initialize settings state if needed
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
		StopSignals: make(map[string]chan struct{}),
	}

	return &App{
		ServerState:   serverState,
		SettingsState: settingsState,
		AdminState:    adminState,
		logger:        logger,
	}
}

// Startup is called when the app starts. The context is saved
// so we can call the runtime methods
func (a *App) Startup(ctx context.Context) {
	a.ctx = ctx
}

// TODO: Delete this function
func (a *App) Greet(name string) string {
	logger := utils.GetLogger()
	logger.Info("Greet called", zap.String("name", name))
	return "Hello " + name
}

// StartServer starts the HTTP and Voice servers
func (a *App) StartServer() string {
	a.AdminState.Lock()
	if a.AdminState.HTTPStatus.IsRunning ||
		a.AdminState.VoiceStatus.IsRunning ||
		a.AdminState.ControlStatus.IsRunning {
		a.AdminState.Unlock()
		return "One or more servers are already running"
	}
	a.AdminState.Unlock()

	resControl := a.startControlServer()
	resVoice := a.startVoiceServer()
	resHTTP := a.startHTTPServer()

	return fmt.Sprintf("%s\n%s\n%s", resHTTP, resVoice, resControl)
}

// StopServer starts the HTTP and Voice servers
func (a *App) StopServer() string {
	a.AdminState.RLock()
	if !a.AdminState.HTTPStatus.IsRunning &&
		!a.AdminState.VoiceStatus.IsRunning &&
		!a.AdminState.ControlStatus.IsRunning {
		a.AdminState.RUnlock()
		return "All servers are already stopped"
	}
	a.AdminState.RUnlock()

	resHTTP := a.stopHTTPServer()
	resVoice := a.stopVoiceServer()
	resControl := a.stopControlServer()

	return fmt.Sprintf("%s\n%s\n%s", resHTTP, resVoice, resControl)
}

// GetServerStatus returns the status of the HTTP and Voice servers
func (a *App) GetServerStatus() map[string]state.ServiceStatus {
	a.AdminState.RLock()
	defer a.AdminState.RUnlock()

	return map[string]state.ServiceStatus{
		"http":    a.AdminState.HTTPStatus,
		"voice":   a.AdminState.VoiceStatus,
		"control": a.AdminState.ControlStatus,
	}
}
