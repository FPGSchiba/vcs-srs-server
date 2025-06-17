package services

import (
	"github.com/FPGSchiba/vcs-srs-server/app"
	"github.com/FPGSchiba/vcs-srs-server/state"
)

type ControlService struct {
	App *app.VCSApplication
}

func NewControlService(app *app.VCSApplication) *ControlService {
	return &ControlService{
		App: app,
	}
}

// StartServer starts the HTTP and Voice servers
func (c *ControlService) StartServer() {
	c.App.StartStandaloneServer()
}

// StopServer starts the HTTP and Voice servers
func (c *ControlService) StopServer() {
	c.App.StopServer()
}

// GetServerStatus returns the status of the HTTP and Voice servers
func (c *ControlService) GetServerStatus() *state.AdminState {
	return c.App.GetServerStatus()
}

func (c *ControlService) GetServerVersion() string {
	return c.App.GetServerVersion()
}
