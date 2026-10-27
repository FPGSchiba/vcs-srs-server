package app

import (
	"net"

	"github.com/FPGSchiba/vcs-srs-server/events"
	"github.com/FPGSchiba/vcs-srs-server/srs"
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
	a.ServerState.RLock()
	defer a.ServerState.RUnlock()
	clients := make(map[string]state.RadioState, len(a.ServerState.RadioClients))
	for k, v := range a.ServerState.RadioClients {
		if v == nil {
			continue
		}
		clients[k.String()] = *v.Clone()
	}
	return RadioClients{RadioClients: clients}
}

func (a *VCSApplication) GetClients() Clients {
	a.ServerState.RLock()
	defer a.ServerState.RUnlock()
	clients := make(map[string]state.ClientState, len(a.ServerState.Clients))
	for k, v := range a.ServerState.Clients {
		if v == nil {
			continue
		}
		clients[k.String()] = *v.Clone()
	}
	return Clients{clients}
}

func (a *VCSApplication) GetBannedClients() []state.BannedClient {
	a.ServerState.Lock()
	defer a.ServerState.Unlock()
	return a.ServerState.BannedState.BannedClients
}

// voiceIP returns the client's address on the local voice server, or false
// when there is no local voice server or the client is not bound to it.
// It must be called without ServerState held: the voice server takes its
// own lock and then ServerState on its hot path (D17).
func (a *VCSApplication) voiceIP(id uuid.UUID) (net.IP, bool) {
	if a.voiceIPLookup != nil {
		return a.voiceIPLookup(id)
	}
	v := a.currentVoiceServer()
	if v == nil {
		return nil, false
	}
	return v.GetClientIPFromId(id)
}

// runningRemover returns the session remover if there is a running control
// server to end sessions through, else nil.
func (a *VCSApplication) runningRemover() sessionRemover {
	r := a.currentRemover()
	if r == nil || !r.IsRunning() {
		return nil
	}
	return r
}

// banPrecheck reports, under a read lock, whether clientGuid is a known
// client that is not banned yet; otherwise it notifies and returns false.
func (a *VCSApplication) banPrecheck(clientGuid uuid.UUID, clientId, reason string) bool {
	a.ServerState.RLock()
	_, known := a.ServerState.Clients[clientGuid]
	_, alreadyBanned := utils.FindByFunc(a.ServerState.BannedState.BannedClients, func(bc state.BannedClient) bool {
		return bc.ID == clientGuid
	})
	a.ServerState.RUnlock()
	if !known {
		a.Notify(events.NewNotification("Ban failed", "Client not found", "error"))
		a.Logger.Error("Failed to ban client", "clientId", clientId, "reason", reason)
		return false
	}
	if alreadyBanned {
		a.Notify(events.NewNotification("Ban failed", "Client is already banned", "error"))
		a.Logger.Error("Failed to ban client: already banned", "clientId", clientId)
		return false
	}
	return true
}

func (a *VCSApplication) BanClient(clientId string, reason string) {
	clientGuid, err := uuid.Parse(clientId)
	if err != nil {
		a.Notify(events.NewNotification("Ban failed", "Invalid client ID format", "error"))
		a.Logger.Error("Failed to parse client ID", "clientId", clientId, "error", err)
		return
	}
	remover := a.runningRemover()
	if remover == nil {
		a.Notify(events.NewNotification("Ban failed", "Control server is not running", "error"))
		a.Logger.Error("Failed to ban client: control server is not running", "clientId", clientId)
		return
	}
	// Same check order as before the removal helper: unknown, then already
	// banned, then not bound to voice. Both checks are repeated under the
	// write lock below, since the voice lookup must run without ServerState.
	if !a.banPrecheck(clientGuid, clientId, reason) {
		return
	}
	// Read the voice IP before taking ServerState (lock order, and no
	// local voice server must not panic).
	clientIp, ok := a.voiceIP(clientGuid)
	if !ok {
		a.Notify(events.NewNotification("Ban failed", "Client not found in voice server", "error"))
		a.Logger.Error("Failed to ban client: not found in voice server", "clientId", clientId)
		return
	}

	a.ServerState.Lock()
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
	bannedSnap := make([]state.BannedClient, len(a.ServerState.BannedState.BannedClients))
	copy(bannedSnap, a.ServerState.BannedState.BannedClients)
	a.ServerState.Unlock()

	// The removal helper ends the stream (BAN as its last update), drops the
	// voice binding everywhere and publishes ClientLeft.
	if _, running := remover.RemoveClient(clientGuid, srs.ReasonBanned, srs.ServerActionUpdate(events.ActionBan, clientGuid, reason)); !running {
		// The control server stopped between the check and here; the ban is
		// recorded, so a re-login is refused anyway.
		a.Logger.Warn("Ban recorded but the session could not be ended: control server stopped", "clientId", clientId)
	}
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
	clientGuid, err := uuid.Parse(clientId)
	if err != nil {
		a.Notify(events.NewNotification("Kick failed", "Invalid client ID format", "error"))
		a.Logger.Error("Failed to parse client ID", "clientId", clientId, "error", err)
		return
	}
	remover := a.currentRemover()
	if remover == nil {
		a.Notify(events.NewNotification("Kick failed", "Control server is not running", "error"))
		a.Logger.Error("Failed to kick client: control server is not running", "clientId", clientId)
		return
	}
	// The removal helper ends the stream (KICK as its last update), drops
	// the voice binding everywhere and publishes ClientLeft.
	existed, running := remover.RemoveClient(clientGuid, srs.ReasonKicked, srs.ServerActionUpdate(events.ActionKick, clientGuid, reason))
	if !running {
		a.Notify(events.NewNotification("Kick failed", "Control server is not running", "error"))
		a.Logger.Error("Failed to kick client: control server is not running", "clientId", clientId)
		return
	}
	if !existed {
		a.Notify(events.NewNotification("Kick failed", "Client not found", "error"))
		a.Logger.Error("Failed to kick client: not found", "clientId", clientId)
		return
	}
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
	radioSnap := a.ServerState.CloneRadiosLocked() // SRV-09
	a.ServerState.Unlock()
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
	radioSnap := a.ServerState.CloneRadiosLocked() // SRV-09
	a.ServerState.Unlock()
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
	return a.ServerState.CloneClientsLocked()
}

func (a *VCSApplication) GetRadioClientMap() map[uuid.UUID]*state.RadioState {
	a.ServerState.RLock()
	defer a.ServerState.RUnlock()
	return a.ServerState.CloneRadiosLocked()
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
