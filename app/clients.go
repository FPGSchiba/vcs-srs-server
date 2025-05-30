package app

import (
	"github.com/FPGSchiba/vcs-srs-server/events"
	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/FPGSchiba/vcs-srs-server/utils"
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

func (a *VCSApplication) GetRadioClients() RadioClients {
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

func (a *VCSApplication) GetClients() Clients {
	a.ServerState.Lock()
	defer a.ServerState.Unlock()
	clients := make(map[string]state.ClientState, len(a.ServerState.Clients))
	for k, v := range a.ServerState.Clients {
		clients[k] = *v
	}
	return Clients{clients}
}

func (a *VCSApplication) GetBannedClients() []state.BannedClient {
	a.ServerState.Lock()
	defer a.ServerState.Unlock()
	return a.ServerState.BannedState.BannedClients
}

func (a *VCSApplication) BanClient(clientId string, reason string) { // TODO: Implement the Backend Logic to ban a client
	a.ServerState.Lock()
	defer a.ServerState.Unlock()
	client, ok := a.ServerState.Clients[clientId]
	if !ok {
		a.Notify(events.NewNotification("Ban failed", "Client not found", "error"))
		a.Logger.Error("Failed to ban client", zap.String("clientId", clientId), zap.String("reason", reason))
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
		a.Logger.Error("Failed to save banned clients", "error", err)
		return
	}
	delete(a.ServerState.Clients, clientId)
	a.EmitEvent(events.Event{
		Name: events.ClientsChanged,
		Data: a.ServerState.Clients,
	})
	a.EmitEvent(events.Event{
		Name: events.BannedClientsChanged,
		Data: a.ServerState.BannedState.BannedClients,
	})
	a.Notify(events.NewNotification("Ban succeeded", "Client banned successfully", "success"))
	a.Logger.Info("Client banned", "clientId", clientId, "reason", reason)
}

func (a *VCSApplication) UnbanClient(clientId string) {
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
		a.Logger.Error("Failed to unban client", "clientId", clientId)
		return
	}
	err := a.ServerState.BannedState.Save()
	if err != nil {
		a.Notify(events.NewNotification("Unban failed", "Failed to save banned clients", "error"))
		a.Logger.Error("Failed to save banned clients", "error", err)
		return
	}
	a.EmitEvent(events.Event{
		Name: events.BannedClientsChanged,
		Data: a.ServerState.BannedState.BannedClients,
	})
	a.Notify(events.NewNotification("Unban succeeded", "Client successfully unbanned", "success"))
}

func (a *VCSApplication) KickClient(clientId string, reason string) { // TODO: Implement Backend Logic to kick a client
	a.ServerState.Lock()
	defer a.ServerState.Unlock()
	delete(a.ServerState.Clients, clientId)
	a.EmitEvent(events.Event{
		Name: events.ClientsChanged,
		Data: a.ServerState.Clients,
	})
	a.Notify(events.NewNotification("Kick succeeded", "Client kicked successfully", "success"))
	a.Logger.Info("Client kicked", "clientId", clientId, "reason", reason)
}

func (a *VCSApplication) MuteClient(clientId string) { // TODO: Implement Backend Logic to mute a client and notify the Client
	a.ServerState.Lock()
	defer a.ServerState.Unlock()
	client, ok := a.ServerState.RadioClients[clientId]
	if !ok {
		a.Notify(events.NewNotification("Mute failed", "Client not found", "error"))
		a.Logger.Error("Failed to mute client", "clientId", clientId)
		return
	}
	client.Muted = true
	a.ServerState.RadioClients[clientId] = client
	a.EmitEvent(events.Event{
		Name: events.RadioClientsChanged,
		Data: a.ServerState.RadioClients,
	})
	a.Notify(events.NewNotification("Mute succeeded", "Client muted successfully", "success"))
	a.Logger.Info("Client muted", "clientId", clientId)
}

func (a *VCSApplication) UnmuteClient(clientId string) { // TODO: Implement Backend Logic to unmute a client and notify the Client
	a.ServerState.Lock()
	defer a.ServerState.Unlock()
	client, ok := a.ServerState.RadioClients[clientId]
	if !ok {
		a.Notify(events.NewNotification("Unmute failed", "Client not found", "error"))
		a.Logger.Error("Failed to unmute client", "clientId", clientId)
		return
	}
	client.Muted = false
	a.ServerState.RadioClients[clientId] = client
	a.EmitEvent(events.Event{
		Name: events.RadioClientsChanged,
		Data: a.ServerState.RadioClients,
	})
	a.Notify(events.NewNotification("Unmute succeeded", "Client unmuted successfully", "success"))
	a.Logger.Info("Client unmuted", "clientId", clientId)
}

func (a *VCSApplication) IsClientMuted(clientId string) bool {
	a.ServerState.Lock()
	defer a.ServerState.Unlock()
	client, ok := a.ServerState.RadioClients[clientId]
	if !ok {
		a.Notify(events.NewNotification("Client not Found", "Could not check if client is muted or not", "warning"))
		a.Logger.Error("Failed to check if client is muted", "clientId", clientId)
		return false
	}
	return client.Muted
}
