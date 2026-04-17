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
	Api          ApiSettings
}

// Snapshot returns a copy of SettingsState under a read lock.
func (s *SettingsState) Snapshot() SettingsSnapshot {
	s.RLock()
	defer s.RUnlock()
	coalitions := make([]Coalition, len(s.Coalitions))
	copy(coalitions, s.Coalitions)
	return SettingsSnapshot{
		Servers:      s.Servers,
		Coalitions:   coalitions,
		Frequencies:  s.Frequencies,
		General:      s.General,
		Security:     s.Security,
		VoiceControl: s.VoiceControl,
		Api:          s.Api,
	}
}

// AdminStateSnapshot is a lock-free copy of AdminState for read-only consumers.
type AdminStateSnapshot struct {
	HTTPStatus    ServiceStatus
	VoiceStatus   ServiceStatus
	ControlStatus ServiceStatus
}
