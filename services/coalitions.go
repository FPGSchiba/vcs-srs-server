package services

import (
	"fmt"
	"github.com/FPGSchiba/vcs-srs-server/app"
	"github.com/FPGSchiba/vcs-srs-server/events"
	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/FPGSchiba/vcs-srs-server/utils"
)

type CoalitionService struct {
	App *app.VCSApplication
}

func NewCoalitionService(app *app.VCSApplication) *CoalitionService {
	return &CoalitionService{
		App: app,
	}
}

func (c *CoalitionService) GetCoalitions() []state.Coalition {
	c.App.SettingsState.Lock()
	defer c.App.SettingsState.Unlock()
	return c.App.SettingsState.Coalitions
}

func (c *CoalitionService) GetCoalitionByName(name string) *state.Coalition {
	c.App.SettingsState.Lock()
	defer c.App.SettingsState.Unlock()
	for _, coalition := range c.App.SettingsState.Coalitions {
		if coalition.Name == name {
			return &coalition
		}
	}
	return nil
}

func (c *CoalitionService) AddCoalition(coalition state.Coalition) {
	c.App.SettingsState.Lock()
	defer c.App.SettingsState.Unlock()
	c.App.SettingsState.Coalitions = append(c.App.SettingsState.Coalitions, coalition)
	err := c.App.SettingsState.Save()
	if err != nil {
		c.App.Notify(events.NewNotification("Coalition failed to save", fmt.Sprintf("Coalition %s could not be saved!"), "error"))
		c.App.Logger.Error("Failed to save settings", "error", err)
		return
	}
	c.App.App.EmitEvent(events.CoalitionsChanged, c.App.SettingsState.Coalitions)
	c.App.Notify(events.NewNotification("Coalition added", fmt.Sprintf("Coalition %s added", coalition.Name), "info"))
}

func (c *CoalitionService) RemoveCoalition(coalition state.Coalition) {
	c.App.SettingsState.Lock()
	defer c.App.SettingsState.Unlock()
	c.App.SettingsState.Coalitions = utils.Remove(c.App.SettingsState.Coalitions, coalition)
	err := c.App.SettingsState.Save()
	if err != nil {
		c.App.Notify(events.NewNotification("Coalition failed to save", fmt.Sprintf("Coalition %s could not be saved!"), "error"))
		c.App.Logger.Error("Failed to save settings", "error", err)
		return
	}
	c.App.App.EmitEvent(events.CoalitionsChanged, c.App.SettingsState.Coalitions)
	c.App.Notify(events.NewNotification("Coalition removed", fmt.Sprintf("Coalition %s removed", coalition.Name), "info"))
}

func (c *CoalitionService) UpdateCoalition(coalition state.Coalition) {
	c.App.SettingsState.Lock()
	defer c.App.SettingsState.Unlock()
	for i, coal := range c.App.SettingsState.Coalitions {
		if coal.Name == coalition.Name {
			c.App.SettingsState.Coalitions[i] = coalition
			break
		}
	}
	err := c.App.SettingsState.Save()
	if err != nil {
		c.App.Notify(events.NewNotification("Coalition failed to save", fmt.Sprintf("Coalition %s could not be saved!", coalition.Name), "error"))
		c.App.Logger.Error("Failed to save settings", "error", err)
		return
	}
	c.App.App.EmitEvent(events.CoalitionsChanged, c.App.SettingsState.Coalitions)
	c.App.Notify(events.NewNotification("Coalition updated", fmt.Sprintf("Coalition %s updated", coalition.Name), "info"))
}
