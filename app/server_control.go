package app

import (
	"context"
	"errors"
	"fmt"
	"github.com/FPGSchiba/vcs-srs-server/control"
	"github.com/FPGSchiba/vcs-srs-server/events"
	"github.com/FPGSchiba/vcs-srs-server/voice"
	"github.com/gin-gonic/gin"
	"net/http"
	"time"
)

func (a *VCSApplication) startHTTPServer() {
	a.AdminState.Lock()
	if a.AdminState.HTTPStatus.IsRunning {
		a.AdminState.Unlock()
		a.Notify(events.NewNotification("HTTP server already running", "HTTP server is already running", "warning"))
		return
	}
	a.AdminState.Unlock()

	// Create stop channel
	stopChan := make(chan struct{})
	a.AdminState.Lock()
	a.StopSignals["http"] = stopChan
	a.AdminState.Unlock()

	go func() {
		r := gin.Default()
		// Configure your gin routes and socket.io here

		a.SettingsState.Lock()

		a.httpServer = &http.Server{
			Addr:    fmt.Sprintf("%s:%d", a.SettingsState.Servers.HTTP.Host, a.SettingsState.Servers.HTTP.Port),
			Handler: r,
			// Add timeouts to prevent hanging
			ReadTimeout:  15 * time.Second,
			WriteTimeout: 15 * time.Second,
			IdleTimeout:  60 * time.Second,
		}

		a.SettingsState.Unlock()

		// Update status
		a.AdminState.Lock()
		a.AdminState.HTTPStatus.IsRunning = true
		a.AdminState.HTTPStatus.Error = ""
		a.AdminState.Unlock()

		a.Logger.Info("HTTP server starting")

		// Start server
		if err := a.httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			a.Notify(events.NewNotification("HTTP server error", "Could not start HTTP Server.", "error"))
			a.Logger.Error("HTTP server error", "error", err)
			a.AdminState.Lock()
			a.AdminState.HTTPStatus.Error = err.Error()
			a.AdminState.HTTPStatus.IsRunning = false
			a.AdminState.Unlock()
		}

		a.Logger.Info("HTTP server stopped listening")
	}()

	a.EmitEvent(events.Event{
		Name: events.AdminChanged,
		Data: a.AdminState,
	})
}

func (a *VCSApplication) stopHTTPServer() {
	// First mark the server as stopping
	a.AdminState.Lock()
	if !a.AdminState.HTTPStatus.IsRunning {
		a.AdminState.Unlock()
		a.Notify(events.NewNotification("HTTP server error", "HTTP server is not running", "warning"))
		return
	}

	// Signal stop
	if stopChan, exists := a.StopSignals["http"]; exists {
		close(stopChan)
		delete(a.StopSignals, "http")
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

		a.EmitEvent(events.Event{
			Name: events.AdminChanged,
			Data: a.AdminState,
		})
		a.Notify(events.NewNotification("HTTP server error", "Could not stop HTTP Server.", "error"))
		return
	}

	// Update final status
	a.AdminState.Lock()
	a.AdminState.HTTPStatus.IsRunning = false
	a.AdminState.HTTPStatus.Error = ""
	a.AdminState.Unlock()

	a.EmitEvent(events.Event{
		Name: events.AdminChanged,
		Data: a.AdminState,
	})
}

func (a *VCSApplication) startVoiceServer() {
	a.AdminState.Lock()
	defer a.AdminState.Unlock()

	if a.AdminState.VoiceStatus.IsRunning {
		a.Notify(events.NewNotification("Voice server error", "voice server is already running", "warning"))
		return
	}

	stopChan := make(chan struct{})
	a.StopSignals["voice"] = stopChan

	go func() {
		voiceServer := voice.NewServer(a.ServerState, a.Logger)
		a.voiceServer = voiceServer

		// Update status
		a.AdminState.Lock()
		a.AdminState.VoiceStatus.IsRunning = true
		a.AdminState.VoiceStatus.Error = ""
		a.AdminState.Unlock()

		a.SettingsState.Lock()
		serverHost := fmt.Sprintf("%s:%d", a.SettingsState.Servers.Voice.Host, a.SettingsState.Servers.Voice.Port)
		a.SettingsState.Unlock()
		if err := voiceServer.Listen(serverHost, stopChan); err != nil {
			a.AdminState.Lock()
			a.AdminState.VoiceStatus.Error = err.Error()
			a.AdminState.VoiceStatus.IsRunning = false
			a.AdminState.Unlock()

			a.Notify(events.NewNotification("voice server error", "Could not start Voice server", "error"))
			a.Logger.Error("voice server error", "error", err)
		}

	}()

	a.EmitEvent(events.Event{
		Name: events.AdminChanged,
		Data: a.AdminState,
	})
}

func (a *VCSApplication) stopVoiceServer() {
	// First mark the server as stopping
	a.AdminState.Lock()
	if !a.AdminState.VoiceStatus.IsRunning {
		a.AdminState.Unlock()
		a.Notify(events.NewNotification("Voice server error", "Voice server is not running", "warning"))
		return
	}

	// Signal stop
	if stopChan, exists := a.StopSignals["voice"]; exists {
		close(stopChan)
		delete(a.StopSignals, "voice")
	}
	a.AdminState.Unlock()

	// Stop the server without holding the lock
	if err := a.voiceServer.Stop(); err != nil {
		a.AdminState.Lock()
		a.AdminState.VoiceStatus.Error = err.Error()
		a.AdminState.Unlock()
		a.Notify(events.NewNotification("Voice server error", "Could stop start Voice server", "error"))
		return
	}

	// Update final status
	a.AdminState.Lock()
	a.AdminState.VoiceStatus.IsRunning = false
	a.AdminState.VoiceStatus.Error = ""
	a.AdminState.Unlock()

	a.EmitEvent(events.Event{
		Name: events.AdminChanged,
		Data: a.AdminState,
	})
}

func (a *VCSApplication) startControlServer() {
	a.AdminState.Lock()
	if a.AdminState.ControlStatus.IsRunning {
		a.AdminState.Unlock()
		a.Notify(events.NewNotification("Control server error", "Control server is already running", "warning"))
		return
	}
	a.AdminState.Unlock()

	// Create stop channel
	stopChan := make(chan struct{})
	a.AdminState.Lock()
	a.StopSignals["control"] = stopChan
	a.AdminState.Unlock()

	controlServer := control.NewServer(a.ServerState, a.SettingsState, a.Logger)
	a.controlServer = controlServer

	a.SettingsState.Lock()
	serverHost := fmt.Sprintf("%s:%d", a.SettingsState.Servers.Control.Host, a.SettingsState.Servers.Control.Port)
	a.SettingsState.Unlock()
	if err := controlServer.Start(serverHost, stopChan); err != nil {
		a.Logger.Error("Failed to start control server", "error", err)
		a.AdminState.Lock()
		a.AdminState.ControlStatus.Error = err.Error()
		a.AdminState.ControlStatus.IsRunning = false
		a.AdminState.Unlock()
		a.Notify(events.NewNotification("Control server error", "Could not start Control server", "error"))
		return
	}

	a.AdminState.Lock()
	a.AdminState.ControlStatus.IsRunning = true
	a.AdminState.ControlStatus.Error = ""
	a.AdminState.Unlock()

	a.EmitEvent(events.Event{
		Name: events.AdminChanged,
		Data: a.AdminState,
	})
}

func (a *VCSApplication) stopControlServer() {
	a.AdminState.Lock()
	if !a.AdminState.ControlStatus.IsRunning {
		a.AdminState.Unlock()
		a.Notify(events.NewNotification("Control server error", "Control server is not running", "warning"))
		return
	}

	// Signal stop
	if stopChan, exists := a.StopSignals["control"]; exists {
		close(stopChan)
		delete(a.StopSignals, "control")
	}
	a.AdminState.Unlock()

	if a.controlServer != nil {
		err := a.controlServer.Stop()
		if err != nil {
			a.Logger.Error("Failed to stop control server", "error", err)
			a.AdminState.Lock()
			a.AdminState.ControlStatus.Error = err.Error()
			a.AdminState.Unlock()
			a.Notify(events.NewNotification("Control server error", "Could not stop control server", "error"))
			return
		}
	}

	a.AdminState.Lock()
	a.AdminState.ControlStatus.IsRunning = false
	a.AdminState.ControlStatus.Error = ""
	a.AdminState.Unlock()

	a.EmitEvent(events.Event{
		Name: events.AdminChanged,
		Data: a.AdminState,
	})
}
