package app

import (
	"context"
	"go.uber.org/zap"
	"net/http"
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
	// Check status without holding the lock for the entire operation
	a.AdminState.RLock()
	if a.AdminState.HTTPStatus.IsRunning || a.AdminState.VoiceStatus.IsRunning {
		a.AdminState.RUnlock()
		return "One or more servers are already running"
	}
	a.AdminState.RUnlock()

	// Start servers without holding the main lock
	resVoice := a.startVoiceServer()
	resHTTP := a.startHTTPServer()

	return resHTTP + "\n" + resVoice
}

// StopServer starts the HTTP and Voice servers
func (a *App) StopServer() string {
	// First check if servers are running
	a.AdminState.RLock()
	if !a.AdminState.HTTPStatus.IsRunning && !a.AdminState.VoiceStatus.IsRunning {
		a.AdminState.RUnlock()
		return "Both servers are not running"
	}
	a.AdminState.RUnlock()

	// Stop HTTP server first
	resHTTP := a.stopHTTPServer()

	// Then stop Voice server
	resVoice := a.stopVoiceServer()

	return resHTTP + "\n" + resVoice
}

// GetServerStatus returns the status of the HTTP and Voice servers
func (a *App) GetServerStatus() map[string]state.ServiceStatus {
	a.AdminState.RLock()
	defer a.AdminState.RUnlock()

	return map[string]state.ServiceStatus{
		"http":  a.AdminState.HTTPStatus,
		"voice": a.AdminState.VoiceStatus,
	}
}
