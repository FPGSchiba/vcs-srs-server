package state

import "sync"

type SettingsState struct {
	sync.RWMutex
	// State holds the current state of the settings
}
