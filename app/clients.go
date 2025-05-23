package app

import (
	"github.com/FPGSchiba/vcs-srs-server/events"
	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/FPGSchiba/vcs-srs-server/utils"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"go.uber.org/zap"
)

// Clients is a workaround struct for wails to generate the wanted bindings
type Clients struct {
	Clients map[string]state.ClientState
}

// RadioClients is a workaround struct for wails to generate the wanted bindings
type RadioClients struct {
	RadioClients map[string]state.RadioState
}

func (a *App) GetRadioClients() RadioClients {
	a.ServerState.Lock()
	defer a.ServerState.Unlock()
	clients := make(map[string]state.RadioState, len(a.ServerState.RadioClients))
	count := 0
	for k, v := range a.ServerState.RadioClients {
		clients[k] = *v
		count++
	}
	return RadioClients{RadioClients: clients}
}

func (a *App) GetClients() Clients {
	a.ServerState.Lock()
	defer a.ServerState.Unlock()
	clients := make(map[string]state.ClientState, len(a.ServerState.Clients))
	for k, v := range a.ServerState.Clients {
		clients[k] = *v
	}
	return Clients{clients}
}

func (a *App) GetBannedClients() []state.BannedClient {
	a.ServerState.Lock()
	defer a.ServerState.Unlock()
	return a.ServerState.BannedState.BannedClients
}

func (a *App) BanClient(clientId string, reason string) { // TODO: Implement the Backend Logic to ban a client
	a.ServerState.Lock()
	defer a.ServerState.Unlock()
	client, ok := a.ServerState.Clients[clientId]
	if !ok {
		a.Notify(events.NewNotification("Ban failed", "Client not found", "error"))
		a.logger.Error("Failed to ban client", zap.String("clientId", clientId), zap.String("reason", reason))
		return
	}
	a.ServerState.BannedState.BannedClients = append(a.ServerState.BannedState.BannedClients, state.BannedClient{
		Name:      client.Name,
		IPAddress: "0.0.0.0",
		Reason:    reason,
		ID:        clientId,
	})
	err := a.ServerState.BannedState.Save()
	if err != nil {
		a.Notify(events.NewNotification("Ban failed", "Failed to save banned clients", "error"))
		a.logger.Error("Failed to save banned clients", zap.Error(err))
		return
	}
	delete(a.ServerState.Clients, clientId)
	runtime.EventsEmit(a.ctx, events.ClientsChanged, a.ServerState.Clients)
	runtime.EventsEmit(a.ctx, events.BannedClientsChanged, a.ServerState.BannedState.BannedClients)
	a.Notify(events.NewNotification("Ban succeeded", "Client banned successfully", "success"))
	a.logger.Info("Client banned", zap.String("clientId", clientId), zap.String("reason", reason))
}

func (a *App) UnbanClient(clientId string) {
	a.ServerState.Lock()
	defer a.ServerState.Unlock()
	success := false
	for _, client := range a.ServerState.BannedState.BannedClients {
		if client.ID == clientId {
			a.ServerState.BannedState.BannedClients = utils.Remove(a.ServerState.BannedState.BannedClients, client)
			success = true
			break
		}
	}
	if !success {
		a.Notify(events.NewNotification("Unban failed", "Client not found", "error"))
		a.logger.Error("Failed to unban client", zap.String("clientId", clientId))
		return
	}
	err := a.ServerState.BannedState.Save()
	if err != nil {
		a.Notify(events.NewNotification("Unban failed", "Failed to save banned clients", "error"))
		a.logger.Error("Failed to save banned clients", zap.Error(err))
		return
	}
	runtime.EventsEmit(a.ctx, events.BannedClientsChanged, a.ServerState.BannedState.BannedClients)
	a.Notify(events.NewNotification("Unban succeeded", "Client successfully unbanned", "success"))
}

func (a *App) KickClient(clientId string, reason string) { // TODO: Implement Backend Logic to kick a client
	a.ServerState.Lock()
	defer a.ServerState.Unlock()
	delete(a.ServerState.Clients, clientId)
	runtime.EventsEmit(a.ctx, events.ClientsChanged, a.ServerState.Clients)
	a.Notify(events.NewNotification("Kick succeeded", "Client kicked successfully", "success"))
	a.logger.Info("Client kicked", zap.String("clientId", clientId), zap.String("reason", reason))
}

func (a *App) MuteClient(clientId string) { // TODO: Implement Backend Logic to mute a client and notify the Client
	a.ServerState.Lock()
	defer a.ServerState.Unlock()
	client, ok := a.ServerState.RadioClients[clientId]
	if !ok {
		a.Notify(events.NewNotification("Mute failed", "Client not found", "error"))
		a.logger.Error("Failed to mute client", zap.String("clientId", clientId))
		return
	}
	client.Muted = true
	a.ServerState.RadioClients[clientId] = client
	runtime.EventsEmit(a.ctx, events.RadioClientsChanged, a.ServerState.RadioClients)
	a.Notify(events.NewNotification("Mute succeeded", "Client muted successfully", "success"))
	a.logger.Info("Client muted", zap.String("clientId", clientId))
}

func (a *App) UnmuteClient(clientId string) { // TODO: Implement Backend Logic to unmute a client and notify the Client
	a.ServerState.Lock()
	defer a.ServerState.Unlock()
	client, ok := a.ServerState.RadioClients[clientId]
	if !ok {
		a.Notify(events.NewNotification("Unmute failed", "Client not found", "error"))
		a.logger.Error("Failed to unmute client", zap.String("clientId", clientId))
		return
	}
	client.Muted = false
	a.ServerState.RadioClients[clientId] = client
	runtime.EventsEmit(a.ctx, events.RadioClientsChanged, a.ServerState.RadioClients)
	a.Notify(events.NewNotification("Unmute succeeded", "Client unmuted successfully", "success"))
	a.logger.Info("Client unmuted", zap.String("clientId", clientId))
}

func (a *App) IsClientMuted(clientId string) bool {
	a.ServerState.Lock()
	defer a.ServerState.Unlock()
	client, ok := a.ServerState.RadioClients[clientId]
	if !ok {
		a.Notify(events.NewNotification("Client not Found", "Could not check if client is muted or not", "warning"))
		a.logger.Error("Failed to check if client is muted", zap.String("clientId", clientId))
		return false
	}
	return client.Muted
}
