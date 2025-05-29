package services

import (
	"fmt"
	"github.com/FPGSchiba/vcs-srs-server/app"
	"github.com/FPGSchiba/vcs-srs-server/events"
	"github.com/FPGSchiba/vcs-srs-server/state"
)

type SettingsService struct {
	App *app.VCSApplication
}

func NewSettingsService(app *app.VCSApplication) *SettingsService {
	return &SettingsService{
		App: app,
	}
}

func (s *SettingsService) GetSettings() *state.SettingsState {
	s.App.SettingsState.Lock()
	defer s.App.SettingsState.Unlock()
	return s.App.SettingsState
}

func (s *SettingsService) SaveGeneralSettings(newSettings *state.GeneralSettings) {
	s.App.SettingsState.Lock()
	defer s.App.SettingsState.Unlock()
	s.App.SettingsState.General = *newSettings
	err := s.App.SettingsState.Save()
	if err != nil {
		s.App.App.Logger.Error(fmt.Sprintf("Failed to save settings: %v", err))
		s.App.Notify(events.NewNotification("Failed to save settings", "Failed to save settings", "error"))
		return
	}
	s.App.App.EmitEvent(events.SettingsChanged, s.App.SettingsState)
	s.App.Notify(events.NewNotification("Settings saved", "General Settings were successfully saved", "info"))
}

func (s *SettingsService) SaveServerSettings(newSettings *state.ServerSettings) {
	s.App.SettingsState.Lock()
	defer s.App.SettingsState.Unlock()
	s.App.SettingsState.Servers = *newSettings
	err := s.App.SettingsState.Save()
	if err != nil {
		s.App.App.Logger.Error(fmt.Sprintf("Failed to save settings: %v", err))
		s.App.Notify(events.NewNotification("Failed to save settings", "Failed to save settings", "error"))
		return
	}
	s.App.App.EmitEvent(events.SettingsChanged, s.App.SettingsState)
	s.App.Notify(events.NewNotification("Settings saved", "Server Settings were successfully saved", "info"))
}

func (s *SettingsService) SaveFrequencySettings(newSettings *state.FrequencySettings) {
	s.App.SettingsState.Lock()
	defer s.App.SettingsState.Unlock()
	s.App.SettingsState.Frequencies = *newSettings
	err := s.App.SettingsState.Save()
	if err != nil {
		s.App.App.Logger.Error(fmt.Sprintf("Failed to save settings: %v", err))
		s.App.Notify(events.NewNotification("Failed to save settings", "Failed to save settings", "error"))
		return
	}
	s.App.App.EmitEvent(events.SettingsChanged, s.App.SettingsState)
	s.App.Notify(events.NewNotification("Settings saved", "Frequency Settings were successfully saved", "info"))
}
