package graphql

// This file will not be regenerated automatically.
//
// It serves as dependency injection for your app, add any dependencies you require here.

import (
	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/FPGSchiba/vcs-srs-server/voiceontrol"
	"github.com/google/uuid"
)

// AppInterface is the contract the Resolver needs from *app.VCSApplication.
// Defined here (in graphql/) to avoid a circular import with app/.
type AppInterface interface {
	GetServerVersion() string
	GetServerStatus() state.AdminStateSnapshot
	GetClientMap() map[uuid.UUID]*state.ClientState
	GetBannedClients() []state.BannedClient
	GetRadioClientMap() map[uuid.UUID]*state.RadioState
	GetSettingsSnapshot() state.SettingsSnapshot
	// Client management
	KickClient(clientId string, reason string)
	BanClient(clientId string, reason string)
	UnbanClient(clientId string)
	MuteClient(clientId string)
	UnmuteClient(clientId string)
	// Server control
	StartServer()
	StopServer()
	// Settings (signatures match existing app/settings.go methods)
	SaveGeneralSettings(newSettings *state.GeneralSettings)
	SaveSecuritySettings(enableGuestAuth bool, enablePluginAuth bool)
	SaveVoiceControlSettings(input state.VoiceControlSettings)
	SaveFrequencySettings(newSettings *state.FrequencySettings)
	SaveCoalitions(coalitions []state.Coalition)
	SaveServerSettings(newSettings *state.ServerSettings)
	// Distribution
	GetDistributionStatus() voiceontrol.DistributionView
	GetDistributionMode() string
	GetClientVoiceLatencyMap() map[uuid.UUID]int64
}

// Resolver is the root resolver. It holds the application.
type Resolver struct {
	App AppInterface
}

// NewResolver creates a Resolver backed by the given app.
func NewResolver(app AppInterface) *Resolver {
	return &Resolver{App: app}
}
