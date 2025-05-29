package app

import (
	"fmt"
	"github.com/FPGSchiba/vcs-srs-server/events"
	"github.com/FPGSchiba/vcs-srs-server/state"
)

func (a *VCSApplication) GetSettings() *state.SettingsState {
	a.SettingsState.Lock()
	defer a.SettingsState.Unlock()
	return a.SettingsState
}

func (a *VCSApplication) SaveGeneralSettings(newSettings *state.GeneralSettings) {
	a.SettingsState.Lock()
	defer a.SettingsState.Unlock()
	a.SettingsState.General = *newSettings
	err := a.SettingsState.Save()
	if err != nil {
		a.App.Logger.Error(fmt.Sprintf("Failed to save settings: %v", err))
		a.Notify(events.NewNotification("Failed to save settings", "Failed to save settings", "error"))
		return
	}
	a.App.EmitEvent(events.SettingsChanged, a.SettingsState)
	a.Notify(events.NewNotification("Settings saved", "General Settings were successfully saved", "info"))
}

func (a *VCSApplication) SaveServerSettings(newSettings *state.ServerSettings) {
	a.SettingsState.Lock()
	defer a.SettingsState.Unlock()
	a.SettingsState.Servers = *newSettings
	err := a.SettingsState.Save()
	if err != nil {
		a.App.Logger.Error(fmt.Sprintf("Failed to save settings: %v", err))
		a.Notify(events.NewNotification("Failed to save settings", "Failed to save settings", "error"))
		return
	}
	a.App.EmitEvent(events.SettingsChanged, a.SettingsState)
	a.Notify(events.NewNotification("Settings saved", "Server Settings were successfully saved", "info"))
}

func (a *VCSApplication) SaveFrequencySettings(newSettings *state.FrequencySettings) {
	a.SettingsState.Lock()
	defer a.SettingsState.Unlock()
	a.SettingsState.Frequencies = *newSettings
	err := a.SettingsState.Save()
	if err != nil {
		a.App.Logger.Error(fmt.Sprintf("Failed to save settings: %v", err))
		a.Notify(events.NewNotification("Failed to save settings", "Failed to save settings", "error"))
		return
	}
	a.App.EmitEvent(events.SettingsChanged, a.SettingsState)
	a.Notify(events.NewNotification("Settings saved", "Frequency Settings were successfully saved", "info"))
}
