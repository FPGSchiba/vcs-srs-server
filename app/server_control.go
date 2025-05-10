package app

import (
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"net/http"
	"time"
	"vcs-server/voice"
)

func (a *App) startHTTPServer() string {
	a.AdminState.Lock()
	if a.AdminState.HTTPStatus.IsRunning {
		a.AdminState.Unlock()
		return "HTTP server is already running"
	}
	a.AdminState.Unlock()

	// Create stop channel
	stopChan := make(chan struct{})
	a.AdminState.Lock()
	a.AdminState.StopSignals["http"] = stopChan
	a.AdminState.Unlock()

	go func() {
		r := gin.Default()
		// Configure your gin routes and socket.io here

		a.httpServer = &http.Server{
			Addr:    ":8080", // TODO: Load this from SettingsState
			Handler: r,
			// Add timeouts to prevent hanging
			ReadTimeout:  15 * time.Second,
			WriteTimeout: 15 * time.Second,
			IdleTimeout:  60 * time.Second,
		}

		// Update status
		a.AdminState.Lock()
		a.AdminState.HTTPStatus.IsRunning = true
		a.AdminState.HTTPStatus.Error = ""
		a.AdminState.Unlock()

		a.logger.Info("HTTP server starting")

		// Start server
		if err := a.httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			a.logger.Error("HTTP server error", zap.Error(err))
			a.AdminState.Lock()
			a.AdminState.HTTPStatus.Error = err.Error()
			a.AdminState.HTTPStatus.IsRunning = false
			a.AdminState.Unlock()
		}

		a.logger.Info("HTTP server stopped listening")
	}()

	return "HTTP server started"
}

func (a *App) stopHTTPServer() string {
	// First mark the server as stopping
	a.AdminState.Lock()
	if !a.AdminState.HTTPStatus.IsRunning {
		a.AdminState.Unlock()
		return "HTTP server is not running"
	}

	// Signal stop
	if stopChan, exists := a.AdminState.StopSignals["http"]; exists {
		close(stopChan)
		delete(a.AdminState.StopSignals, "http")
	}
	a.AdminState.Unlock()

	// Create context with timeout for shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Shutdown the server without holding the lock
	if err := a.httpServer.Shutdown(ctx); err != nil {
		a.AdminState.Lock()
		a.AdminState.HTTPStatus.Error = err.Error()
		a.AdminState.Unlock()
		return "Error stopping HTTP server: " + err.Error()
	}

	// Update final status
	a.AdminState.Lock()
	a.AdminState.HTTPStatus.IsRunning = false
	a.AdminState.HTTPStatus.Error = ""
	a.AdminState.Unlock()

	return "HTTP server stopped"
}

func (a *App) startVoiceServer() string {
	a.AdminState.Lock()
	defer a.AdminState.Unlock()

	if a.AdminState.VoiceStatus.IsRunning {
		return "voice server is already running"
	}

	stopChan := make(chan struct{})
	a.AdminState.StopSignals["voice"] = stopChan

	go func() {
		voiceServer := voice.NewServer(a.ServerState, a.logger)
		a.voiceServer = voiceServer

		// Update status
		a.AdminState.Lock()
		a.AdminState.VoiceStatus.IsRunning = true
		a.AdminState.VoiceStatus.Error = ""
		a.AdminState.Unlock()

		// TODO: Load this from SettingsState
		if err := voiceServer.Listen(":9000", stopChan); err != nil {
			a.AdminState.Lock()
			a.AdminState.VoiceStatus.Error = err.Error()
			a.AdminState.VoiceStatus.IsRunning = false
			a.AdminState.Unlock()
			a.logger.Error("voice server error", zap.Error(err))
		}
	}()

	return "voice server started"
}

func (a *App) stopVoiceServer() string {
	// First mark the server as stopping
	a.AdminState.Lock()
	if !a.AdminState.VoiceStatus.IsRunning {
		a.AdminState.Unlock()
		return "Voice server is not running"
	}

	// Signal stop
	if stopChan, exists := a.AdminState.StopSignals["voice"]; exists {
		close(stopChan)
		delete(a.AdminState.StopSignals, "voice")
	}
	a.AdminState.Unlock()

	// Stop the server without holding the lock
	if err := a.voiceServer.Stop(); err != nil {
		a.AdminState.Lock()
		a.AdminState.VoiceStatus.Error = err.Error()
		a.AdminState.Unlock()
		return "Error stopping Voice server: " + err.Error()
	}

	// Update final status
	a.AdminState.Lock()
	a.AdminState.VoiceStatus.IsRunning = false
	a.AdminState.VoiceStatus.Error = ""
	a.AdminState.Unlock()

	return "Voice server stopped"
}
