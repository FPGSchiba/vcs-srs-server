package events

// SettingsEvent is an event that is triggered when the settings are changed
const (
	SettingsChanged = "settings/changed" // TODO: use this in front and backend for listening to settings changes
	SettingsSaved   = "settings/saved"
)
