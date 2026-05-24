package app

import (
	"github.com/FPGSchiba/vcs-srs-server/events"
	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/FPGSchiba/vcs-srs-server/utils"
	"github.com/google/uuid"
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
		clients[k.String()] = *v
		count++
	}
	return RadioClients{RadioClients: clients}
}

func (a *VCSApplication) GetClients() Clients {
	a.ServerState.Lock()
	defer a.ServerState.Unlock()
	clients := make(map[string]state.ClientState, len(a.ServerState.Clients))
	for k, v := range a.ServerState.Clients {
		clients[k.String()] = *v
	}
	return Clients{clients}
}

func (a *VCSApplication) GetBannedClients() []state.BannedClient {
	a.ServerState.Lock()
	defer a.ServerState.Unlock()
	return a.ServerState.BannedState.BannedClients
}

func (a *VCSApplication) BanClient(clientId string, reason string) {
	a.ServerState.Lock()
	clientGuid, err := uuid.Parse(clientId)
	if err != nil {
		a.ServerState.Unlock()
		a.Notify(events.NewNotification("Ban failed", "Invalid client ID format", "error"))
		a.Logger.Error("Failed to parse client ID", "clientId", clientId, "error", err)
		return
	}
	client, ok := a.ServerState.Clients[clientGuid]
	if !ok {
		a.ServerState.Unlock()
		a.Notify(events.NewNotification("Ban failed", "Client not found", "error"))
		a.Logger.Error("Failed to ban client", "clientId", clientId, "reason", reason)
		return
	}
	_, alreadyBanned := utils.FindByFunc(a.ServerState.BannedState.BannedClients, func(bc state.BannedClient) bool {
		return bc.ID == clientGuid
	})
	if alreadyBanned {
		a.ServerState.Unlock()
		a.Notify(events.NewNotification("Ban failed", "Client is already banned", "error"))
		a.Logger.Error("Failed to ban client: already banned", "clientId", clientId)
		return
	}
	clientIp, ok := a.voiceServer.GetClientIPFromId(clientGuid)
	if !ok {
		a.ServerState.Unlock()
		a.Notify(events.NewNotification("Ban failed", "Client not found in voice server", "error"))
		a.Logger.Error("Failed to ban client: not found in voice server", "clientId", clientId)
		return
	}
	a.ServerState.BannedState.BannedClients = append(a.ServerState.BannedState.BannedClients, state.BannedClient{
		Name:      client.Name,
		IPAddress: clientIp.String(),
		Reason:    reason,
		ID:        clientGuid,
	})
	err = a.ServerState.BannedState.Save()
	if err != nil {
		a.ServerState.Unlock()
		a.Notify(events.NewNotification("Ban failed", "Failed to save banned clients", "error"))
		a.Logger.Error("Failed to save banned clients", "error", err)
		return
	}
	delete(a.ServerState.Clients, clientGuid)
	a.ServerState.Unlock()

	a.ServerState.RLock()
	clientsSnap := make(map[uuid.UUID]*state.ClientState, len(a.ServerState.Clients))
	for k, v := range a.ServerState.Clients {
		cp := *v
		clientsSnap[k] = &cp
	}
	bannedSnap := make([]state.BannedClient, len(a.ServerState.BannedState.BannedClients))
	copy(bannedSnap, a.ServerState.BannedState.BannedClients)
	a.ServerState.RUnlock()
	a.EmitEvent(events.Event{
		Name: events.ClientsChanged,
		Data: events.ClientChangeEvent{Type: events.ClientLeft, ClientID: clientGuid, Clients: clientsSnap},
	})
	a.EmitEvent(events.Event{Name: events.BannedClientsChanged, Data: bannedSnap})
	a.EmitEvent(events.Event{
		Name: events.ServerAction,
		Data: events.ServerActionEvent{ActionType: events.ActionBan, TargetClientID: clientGuid, Reason: reason},
	})
	a.Notify(events.NewNotification("Ban succeeded", "Client banned successfully", "success"))
	a.Logger.Info("Client banned", "clientId", clientId, "reason", reason)
}

func (a *VCSApplication) UnbanClient(clientId string) {
	a.ServerState.Lock()
	clientGuid, err := uuid.Parse(clientId)
	if err != nil {
		a.ServerState.Unlock()
		a.Notify(events.NewNotification("Unban failed", "Invalid client ID format", "error"))
		a.Logger.Error("Failed to parse client ID", "clientId", clientId, "error", err)
		return
	}
	success := false
	for _, client := range a.ServerState.BannedState.BannedClients {
		if client.ID == clientGuid {
			a.ServerState.BannedState.BannedClients = utils.Remove(a.ServerState.BannedState.BannedClients, client)
			success = true
			break
		}
	}
	if !success {
		a.ServerState.Unlock()
		a.Notify(events.NewNotification("Unban failed", "Client not found", "error"))
		a.Logger.Error("Failed to unban client", "clientId", clientId)
		return
	}
	err = a.ServerState.BannedState.Save()
	if err != nil {
		a.ServerState.Unlock()
		a.Notify(events.NewNotification("Unban failed", "Failed to save banned clients", "error"))
		a.Logger.Error("Failed to save banned clients", "error", err)
		return
	}
	a.ServerState.Unlock()

	a.ServerState.RLock()
	bannedSnap := make([]state.BannedClient, len(a.ServerState.BannedState.BannedClients))
	copy(bannedSnap, a.ServerState.BannedState.BannedClients)
	a.ServerState.RUnlock()
	a.EmitEvent(events.Event{Name: events.BannedClientsChanged, Data: bannedSnap})
	a.Notify(events.NewNotification("Unban succeeded", "Client successfully unbanned", "success"))
}

func (a *VCSApplication) KickClient(clientId string, reason string) {
	a.ServerState.Lock()
	clientGuid, err := uuid.Parse(clientId)
	if err != nil {
		a.ServerState.Unlock()
		a.Notify(events.NewNotification("Kick failed", "Invalid client ID format", "error"))
		a.Logger.Error("Failed to parse client ID", "clientId", clientId, "error", err)
		return
	}
	delete(a.ServerState.Clients, clientGuid)
	a.ServerState.Unlock()

	a.ServerState.RLock()
	clientsSnap := make(map[uuid.UUID]*state.ClientState, len(a.ServerState.Clients))
	for k, v := range a.ServerState.Clients {
		cp := *v
		clientsSnap[k] = &cp
	}
	a.ServerState.RUnlock()
	a.EmitEvent(events.Event{
		Name: events.ClientsChanged,
		Data: events.ClientChangeEvent{Type: events.ClientLeft, ClientID: clientGuid, Clients: clientsSnap},
	})
	a.EmitEvent(events.Event{
		Name: events.ServerAction,
		Data: events.ServerActionEvent{ActionType: events.ActionKick, TargetClientID: clientGuid, Reason: reason},
	})
	a.Notify(events.NewNotification("Kick succeeded", "Client kicked successfully", "success"))
	a.Logger.Info("Client kicked", "clientId", clientId, "reason", reason)
}

func (a *VCSApplication) MuteClient(clientId string) {
	a.ServerState.Lock()
	clientGuid, err := uuid.Parse(clientId)
	if err != nil {
		a.ServerState.Unlock()
		a.Notify(events.NewNotification("Mute failed", "Invalid client ID format", "error"))
		a.Logger.Error("Failed to parse client ID", "clientId", clientId, "error", err)
		return
	}
	client, ok := a.ServerState.RadioClients[clientGuid]
	if !ok {
		a.ServerState.Unlock()
		a.Notify(events.NewNotification("Mute failed", "Client not found", "error"))
		a.Logger.Error("Failed to mute client", "clientId", clientId)
		return
	}
	client.Muted = true
	a.ServerState.RadioClients[clientGuid] = client
	a.ServerState.Unlock()

	a.ServerState.RLock()
	radioSnap := make(map[uuid.UUID]*state.RadioState, len(a.ServerState.RadioClients))
	for k, v := range a.ServerState.RadioClients {
		cp := *v
		radioSnap[k] = &cp
	}
	a.ServerState.RUnlock()
	a.EmitEvent(events.Event{
		Name: events.RadioClientsChanged,
		Data: events.RadioChangeEvent{Type: events.RadioUpdated, ClientID: clientGuid, Radios: radioSnap},
	})
	a.EmitEvent(events.Event{
		Name: events.ServerAction,
		Data: events.ServerActionEvent{ActionType: events.ActionMute, TargetClientID: clientGuid},
	})
	a.Notify(events.NewNotification("Mute succeeded", "Client muted successfully", "success"))
	a.Logger.Info("Client muted", "clientId", clientId)
}

func (a *VCSApplication) UnmuteClient(clientId string) {
	a.ServerState.Lock()
	clientGuid, err := uuid.Parse(clientId)
	if err != nil {
		a.ServerState.Unlock()
		a.Notify(events.NewNotification("Unmute failed", "Invalid client ID format", "error"))
		a.Logger.Error("Failed to parse client ID", "clientId", clientId, "error", err)
		return
	}
	client, ok := a.ServerState.RadioClients[clientGuid]
	if !ok {
		a.ServerState.Unlock()
		a.Notify(events.NewNotification("Unmute failed", "Client not found", "error"))
		a.Logger.Error("Failed to unmute client", "clientId", clientId)
		return
	}
	client.Muted = false
	a.ServerState.RadioClients[clientGuid] = client
	a.ServerState.Unlock()

	a.ServerState.RLock()
	radioSnap := make(map[uuid.UUID]*state.RadioState, len(a.ServerState.RadioClients))
	for k, v := range a.ServerState.RadioClients {
		cp := *v
		radioSnap[k] = &cp
	}
	a.ServerState.RUnlock()
	a.EmitEvent(events.Event{
		Name: events.RadioClientsChanged,
		Data: events.RadioChangeEvent{Type: events.RadioUpdated, ClientID: clientGuid, Radios: radioSnap},
	})
	a.EmitEvent(events.Event{
		Name: events.ServerAction,
		Data: events.ServerActionEvent{ActionType: events.ActionUnmute, TargetClientID: clientGuid},
	})
	a.Notify(events.NewNotification("Unmute succeeded", "Client unmuted successfully", "success"))
	a.Logger.Info("Client unmuted", "clientId", clientId)
}

func (a *VCSApplication) GetClientMap() map[uuid.UUID]*state.ClientState {
	a.ServerState.RLock()
	defer a.ServerState.RUnlock()
	snap := make(map[uuid.UUID]*state.ClientState, len(a.ServerState.Clients))
	for k, v := range a.ServerState.Clients {
		cp := *v
		snap[k] = &cp
	}
	return snap
}

func (a *VCSApplication) GetRadioClientMap() map[uuid.UUID]*state.RadioState {
	a.ServerState.RLock()
	defer a.ServerState.RUnlock()
	snap := make(map[uuid.UUID]*state.RadioState, len(a.ServerState.RadioClients))
	for k, v := range a.ServerState.RadioClients {
		cp := *v
		snap[k] = &cp
	}
	return snap
}

func (a *VCSApplication) IsClientMuted(clientId string) bool {
	failedEvent := events.NewNotification("Check Mute Status Failed", "Failed to check if client is muted or not", "error")
	clientGuid, err := uuid.Parse(clientId)
	if err != nil {
		a.Notify(failedEvent)
		a.Logger.Error("Failed to parse client ID", "clientId", clientId, "error", err)
		return false
	}
	a.ServerState.RLock()
	client, ok := a.ServerState.RadioClients[clientGuid]
	if !ok {
		a.ServerState.RUnlock()
		a.Notify(failedEvent)
		a.Logger.Error("Failed to check if client is muted", "clientId", clientId)
		return false
	}
	muted := client.Muted
	a.ServerState.RUnlock()
	return muted
}
