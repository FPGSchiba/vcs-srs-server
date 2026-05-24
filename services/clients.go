package services

import (
	"github.com/FPGSchiba/vcs-srs-server/app"
	"github.com/FPGSchiba/vcs-srs-server/state"
)

type ClientService struct {
	App *app.VCSApplication
}

func NewClientService(app *app.VCSApplication) *ClientService {
	return &ClientService{
		App: app,
	}
}

func (c *ClientService) GetRadioClients() app.RadioClients {
	return c.App.GetRadioClients()
}

func (c *ClientService) GetClients() app.Clients {
	return c.App.GetClients()
}

func (c *ClientService) GetBannedClients() []state.BannedClient {
	return c.App.GetBannedClients()
}

func (c *ClientService) BanClient(clientId string, reason string) {
	c.App.BanClient(clientId, reason)
}

func (c *ClientService) UnbanClient(clientId string) {
	c.App.UnbanClient(clientId)
}

func (c *ClientService) KickClient(clientId string, reason string) {
	c.App.KickClient(clientId, reason)
}

func (c *ClientService) MuteClient(clientId string) {
	c.App.MuteClient(clientId)
}

func (c *ClientService) UnmuteClient(clientId string) {
	c.App.UnmuteClient(clientId)
}

func (c *ClientService) IsClientMuted(clientId string) bool {
	return c.App.IsClientMuted(clientId)
}
