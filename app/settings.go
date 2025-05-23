package app

import (
	"fmt"
	"github.com/FPGSchiba/vcs-srs-server/events"
	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

func (a *App) GetSettings() *state.SettingsState {
	a.SettingsState.Lock()
	defer a.SettingsState.Unlock()
	return a.SettingsState
}

func (a *App) SaveGeneralSettings(newSettings *state.GeneralSettings) {
	a.SettingsState.Lock()
	defer a.SettingsState.Unlock()
	a.SettingsState.General = *newSettings
	err := a.SettingsState.Save()
	if err != nil {
		a.logger.Error(fmt.Sprintf("Failed to save settings: %v", err))
		a.Notify(events.NewNotification("Failed to save settings", "Failed to save settings", "error"))
		return
	}
	runtime.EventsEmit(a.ctx, events.SettingsChanged, a.SettingsState)
	a.Notify(events.NewNotification("Settings saved", "General Settings were successfully saved", "info"))
}

func (a *App) SaveServerSettings(newSettings *state.ServerSettings) {
	a.SettingsState.Lock()
	defer a.SettingsState.Unlock()
	a.SettingsState.Servers = *newSettings
	err := a.SettingsState.Save()
	if err != nil {
		a.logger.Error(fmt.Sprintf("Failed to save settings: %v", err))
		a.Notify(events.NewNotification("Failed to save settings", "Failed to save settings", "error"))
		return
	}
	runtime.EventsEmit(a.ctx, events.SettingsChanged, a.SettingsState)
	a.Notify(events.NewNotification("Settings saved", "Server Settings were successfully saved", "info"))
}

func (a *App) SaveFrequencySettings(newSettings *state.FrequencySettings) {
	a.SettingsState.Lock()
	defer a.SettingsState.Unlock()
	a.SettingsState.Frequencies = *newSettings
	err := a.SettingsState.Save()
	if err != nil {
		a.logger.Error(fmt.Sprintf("Failed to save settings: %v", err))
		a.Notify(events.NewNotification("Failed to save settings", "Failed to save settings", "error"))
		return
	}
	runtime.EventsEmit(a.ctx, events.SettingsChanged, a.SettingsState)
	a.Notify(events.NewNotification("Settings saved", "Frequency Settings were successfully saved", "info"))
}
