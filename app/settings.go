package app

import (
	"fmt"
	"github.com/FPGSchiba/vcs-srs-server/state"
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
		return
	}
}

func (a *App) SaveServerSettings(newSettings *state.ServerSettings) {
	a.SettingsState.Lock()
	defer a.SettingsState.Unlock()
	a.SettingsState.Servers = *newSettings
	err := a.SettingsState.Save()
	if err != nil {
		a.logger.Error(fmt.Sprintf("Failed to save settings: %v", err))
		return
	}
}
