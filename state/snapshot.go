package state

// SettingsSnapshot is a lock-free copy of SettingsState for read-only consumers.
// Use this instead of returning the live *SettingsState pointer to avoid
// data races after the lock is released.
type SettingsSnapshot struct {
	Servers      ServerSettings
	Coalitions   []Coalition
	Frequencies  FrequencySettings
	General      GeneralSettings
	Security     SecuritySettings
	VoiceControl VoiceControlSettings
}

// AdminStateSnapshot is a lock-free copy of AdminState for read-only consumers.
type AdminStateSnapshot struct {
	HTTPStatus    ServiceStatus
	VoiceStatus   ServiceStatus
	ControlStatus ServiceStatus
}
