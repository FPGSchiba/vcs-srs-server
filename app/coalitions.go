package app

import (
	"fmt"
	"github.com/FPGSchiba/vcs-srs-server/events"
	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"go.uber.org/zap"
)

func (a *App) GetCoalitions() []state.Coalition {
	a.SettingsState.Lock()
	defer a.SettingsState.Unlock()
	return a.SettingsState.Coalitions
}

func (a *App) AddCoalition(coalition state.Coalition) {
	a.SettingsState.Lock()
	defer a.SettingsState.Unlock()
	a.SettingsState.Coalitions = append(a.SettingsState.Coalitions, coalition)
	err := a.SettingsState.Save()
	if err != nil {
		a.Notify(events.NewNotification("Coalition failed to save", fmt.Sprintf("Coalition %s could not be saved!"), "error"))
		a.logger.Error("Failed to save settings", zap.Error(err))
		return
	}
	runtime.EventsEmit(a.ctx, events.CoalitionsChanged, a.SettingsState.Coalitions)
	a.Notify(events.NewNotification("Coalition added", fmt.Sprintf("Coalition %s added", coalition.Name), "info"))
}
