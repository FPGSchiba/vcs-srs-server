package app

import (
	"log/slog"
	"net"
	"net/http"
	"sync"

	"github.com/FPGSchiba/vcs-srs-server/control"
	"github.com/FPGSchiba/vcs-srs-server/events"
	"github.com/FPGSchiba/vcs-srs-server/srs"
	"github.com/FPGSchiba/vcs-srs-server/srspb"
	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/FPGSchiba/vcs-srs-server/voice"
	"github.com/FPGSchiba/vcs-srs-server/voiceontrol"
	"github.com/google/uuid"
)

const Version = "v0.1.0"

// VCSApplication struct
type VCSApplication struct {
	guiApp            // GUI-only fields (App *application.App); empty struct in headless builds
	ServerState       *state.ServerState
	SettingsState     *state.SettingsState
	AdminState        *state.AdminState
	DistributionState *state.DistributionState
	autoStart         bool
	httpServer        *http.Server
	voiceMu           sync.RWMutex  // guards voiceServer
	voiceServer       *voice.Server // read through currentVoiceServer
	controlServer     *control.Server
	// remover ends a client's session (control.Server once started; nil
	// while the control server has never been started). Read through
	// currentRemover; removerMu guards it.
	removerMu sync.RWMutex
	remover   sessionRemover
	// voiceIPLookup is a test seam; nil means the local voice server's
	// GetClientIPFromId (and "not bound" when there is no voice server).
	voiceIPLookup func(uuid.UUID) (net.IP, bool)
	StopSignals   map[string]chan struct{}
	eventBus      *events.EventBus
	Logger        *slog.Logger
}

// New creates a new App application struct
// sessionRemover is the part of control.Server that Kick and Ban use.
type sessionRemover interface {
	IsRunning() bool
	RemoveClient(id uuid.UUID, reason srs.RemovalReason, final *srspb.ServerUpdate) (existed, running bool)
}

// currentVoiceServer returns the local voice server, or nil if none was
// started in this process.
func (a *VCSApplication) currentVoiceServer() *voice.Server {
	a.voiceMu.RLock()
	defer a.voiceMu.RUnlock()
	return a.voiceServer
}

// currentRemover returns the session remover, or nil if the control
// server was never started.
func (a *VCSApplication) currentRemover() sessionRemover {
	a.removerMu.RLock()
	defer a.removerMu.RUnlock()
	return a.remover
}

func (a *VCSApplication) setRemover(r sessionRemover) {
	a.removerMu.Lock()
	a.remover = r
	a.removerMu.Unlock()
}

func (a *VCSApplication) setVoiceServer(v *voice.Server) {
	a.voiceMu.Lock()
	a.voiceServer = v
	a.voiceMu.Unlock()
}

// localVoiceDropper drops a removed client's binding on this process's own
// voice server, if one is running. It satisfies srs.VoiceSessionDropper.
type localVoiceDropper struct{ a *VCSApplication }

func (d localVoiceDropper) DropVoiceSession(id uuid.UUID, _ string) {
	if v := d.a.currentVoiceServer(); v != nil {
		v.DisconnectClient(id)
	}
}

func New() *VCSApplication {
	return &VCSApplication{
		ServerState:       &state.ServerState{},
		SettingsState:     &state.SettingsState{},
		AdminState:        &state.AdminState{},
		DistributionState: &state.DistributionState{},
		autoStart:         false,
		eventBus:          events.NewEventBus(),
		httpServer:        nil,
		voiceServer:       nil,
		controlServer:     nil,
		StopSignals:       make(map[string]chan struct{}),
	}
}

func (a *VCSApplication) HeadlessStartup(logger *slog.Logger, configFilePath, bannedFilePath string, distributionMode uint8, isGlobal bool) {
	settingsState, err := state.GetSettingsState(configFilePath)
	if err != nil {
		panic(err) // Without settings, we can't run
	}

	adminState := &state.AdminState{
		HTTPStatus: state.ServiceStatus{
			IsRunning: false,
			IsNeeded:  distributionMode == state.DistributionModeStandalone || distributionMode == state.DistributionModeControl,
			Error:     "",
		},
		VoiceStatus: state.ServiceStatus{
			IsRunning: false,
			IsNeeded:  distributionMode == state.DistributionModeStandalone || distributionMode == state.DistributionModeVoice,
			Error:     "",
		},
		ControlStatus: state.ServiceStatus{
			IsRunning: false,
			IsNeeded:  distributionMode == state.DistributionModeStandalone || distributionMode == state.DistributionModeControl,
			Error:     "",
		},
	}

	bannedState, err := state.GetBannedState(bannedFilePath)
	if err != nil {
		bannedState = &state.BannedState{
			BannedClients: make([]state.BannedClient, 0),
		}
		err = bannedState.Save()
		if err != nil {
			panic(err)
		}
	}

	serverState := &state.ServerState{
		Clients:      make(map[uuid.UUID]*state.ClientState),
		RadioClients: make(map[uuid.UUID]*state.RadioState),
		BannedState:  *bannedState,
	}

	distributionState := &state.DistributionState{
		DistributionMode: distributionMode,
		RuntimeMode:      state.RuntimeModeHeadless,
		IsGlobal:         isGlobal,
	}

	a.ServerState = serverState
	a.SettingsState = settingsState
	a.AdminState = adminState
	a.DistributionState = distributionState
	a.autoStart = true
	a.Logger = logger

	switch distributionMode {
	case state.DistributionModeStandalone:
		a.StartStandaloneServer()
	case state.DistributionModeControl:
		a.StartControlServer()
	case state.DistributionModeVoice:
		a.StartVoiceServer()
	}
}

// StartStandaloneServer starts the Control, HTTP and Voice servers
func (a *VCSApplication) StartStandaloneServer() {
	a.startGrpcServer()
	a.startVoiceServer()
	a.startHTTPServer()
}

func (a *VCSApplication) StartControlServer() {
	a.startGrpcServer()
	a.startHTTPServer()
}

func (a *VCSApplication) StartVoiceServer() {
	a.startVoiceServer()
}

// StopServer stops the Control, HTTP and Voice servers
func (a *VCSApplication) StopServer() {
	a.AdminState.RLock()
	if !a.AdminState.HTTPStatus.IsRunning &&
		!a.AdminState.VoiceStatus.IsRunning &&
		!a.AdminState.ControlStatus.IsRunning {
		a.AdminState.RUnlock()
		return
	}
	a.AdminState.RUnlock()

	a.DistributionState.RLock()
	if a.DistributionState.DistributionMode == state.DistributionModeVoice || a.DistributionState.DistributionMode == state.DistributionModeStandalone {
		a.stopVoiceServer()
	}
	if a.DistributionState.DistributionMode == state.DistributionModeControl || a.DistributionState.DistributionMode == state.DistributionModeStandalone {
		a.stopHTTPServer()
		a.stopControlServer()
	}
	a.DistributionState.RUnlock()

	a.eventBus.Stop()
}

// GetServerStatus returns the status of the Control, HTTP and Voice servers
func (a *VCSApplication) GetServerStatus() state.AdminStateSnapshot {
	a.AdminState.RLock()
	defer a.AdminState.RUnlock()
	return state.AdminStateSnapshot{
		HTTPStatus:    a.AdminState.HTTPStatus,
		VoiceStatus:   a.AdminState.VoiceStatus,
		ControlStatus: a.AdminState.ControlStatus,
	}
}

func (a *VCSApplication) GetServerVersion() string {
	return Version
}

func (a *VCSApplication) GetEventBus() *events.EventBus {
	return a.eventBus
}

func (a *VCSApplication) GetSettingsSnapshot() state.SettingsSnapshot {
	return a.SettingsState.Snapshot()
}

func (a *VCSApplication) Notify(notification events.Notification) {
	a.EmitEvent(events.Event{
		Name: events.NotificationEvent,
		Data: notification,
	})
	a.Logger.Info("Notification", "title", notification.Title, "message", notification.Message, "level", notification.Level)
}

func (a *VCSApplication) EmitEvent(event events.Event) {
	a.eventBus.Publish(event)
}

// GetDistributionStatus returns the current voice node registry and client assignments.
func (a *VCSApplication) GetDistributionStatus() voiceontrol.DistributionView {
	if a.controlServer == nil {
		return voiceontrol.DistributionView{}
	}
	return a.controlServer.GetDistributionStatus()
}

// GetDistributionMode returns the current distribution mode as a string for GraphQL.
func (a *VCSApplication) GetDistributionMode() string {
	a.DistributionState.RLock()
	mode := a.DistributionState.DistributionMode
	a.DistributionState.RUnlock()
	switch mode {
	case state.DistributionModeControl:
		return "CONTROL"
	case state.DistributionModeVoice:
		return "VOICE"
	default:
		return "STANDALONE"
	}
}

// GetClientVoiceLatencyMap returns a map of clientID → measured UDP RTT to the local voice server.
// In distributed voice-only deployments the voice server is not in this process, so this returns nil.
func (a *VCSApplication) GetClientVoiceLatencyMap() map[uuid.UUID]int64 {
	v := a.currentVoiceServer()
	if v == nil {
		return nil
	}
	return v.GetClientLatencyMap()
}
