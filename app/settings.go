package app

import "github.com/FPGSchiba/vcs-srs-server/state"

func (a *App) GetSettings() *state.SettingsState {
	a.SettingsState.Lock()
	defer a.SettingsState.Unlock()
	return a.SettingsState
}
