//go:build !headless

package app

import (
	"github.com/FPGSchiba/vcs-srs-server/events"
	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/google/uuid"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// guiApp holds the Wails application handle. It is embedded in VCSApplication
// and only compiled in non-headless (GUI) builds to keep Wails out of the
// headless binary, which builds on Linux with CGO_ENABLED=0.
type guiApp struct {
	App *application.App
}

func (a *VCSApplication) StartUp(wailsApp *application.App, configFilePath, bannedFilePath string, autoStartServers bool) {
	settingsState, err := state.GetSettingsState(configFilePath)
	if err != nil {
		wailsApp.Logger.Error("Failed to load settings", "error", err)
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
		wailsApp.Logger.Error("Failed to load banned clients", "error", err)
		bannedState = &state.BannedState{
			BannedClients: make([]state.BannedClient, 0),
		}
		err = bannedState.Save()
		if err != nil {
			wailsApp.Logger.Error("Failed to initialize Banned Clients file", "error", err)
			panic(err)
		}
	}

	serverState := &state.ServerState{
		Clients:      make(map[uuid.UUID]*state.ClientState),
		RadioClients: make(map[uuid.UUID]*state.RadioState),
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
	a.Logger = wailsApp.Logger
	a.App = wailsApp

	if autoStartServers {
		a.StartStandaloneServer()
	}

	// Subscribe to the notification event to log notifications
	notChan := a.eventBus.Subscribe(events.NotificationEvent)
	allChan := a.eventBus.Subscribe("*")
	go a.handleFrontendEmits(allChan)
	go a.handleNotificationEvent(notChan)
}

func (a *VCSApplication) handleNotificationEvent(channel chan events.Event) {
	var guiMode bool
	a.DistributionState.RLock()
	guiMode = a.DistributionState.RuntimeMode == state.RuntimeModeGUI
	a.DistributionState.RUnlock()
	for event := range channel {
		if event.Name == events.NotificationEvent {
			notification, ok := event.Data.(events.Notification)
			if !ok {
				a.Logger.Error("Received non-notification event", "event", event)
				continue
			}
			a.Logger.Info("Notification received", "title", notification.Title, "message", notification.Message, "level", notification.Level)
			if guiMode {
				a.App.Event.EmitEvent(&application.CustomEvent{Name: event.Name, Data: notification})
				continue
			}
		}
	}
}

func (a *VCSApplication) handleFrontendEmits(channel chan events.Event) {
	a.DistributionState.RLock()
	isGUI := a.DistributionState.RuntimeMode == state.RuntimeModeGUI
	a.DistributionState.RUnlock()

	if !isGUI {
		// Drain the channel so events don't silently back up when the bus is running.
		// The loop exits when the channel is closed by EventBus.Stop().
		for range channel {
		}
		return
	}
	for event := range channel {
		a.App.Event.EmitEvent(&application.CustomEvent{Name: event.Name, Data: event.Data})
	}
}
