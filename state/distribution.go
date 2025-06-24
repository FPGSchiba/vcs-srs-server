package state

import "sync"

const (
	RuntimeModeHeadless int8 = iota // Headless mode, no GUI, distribution modes available: Standalone, Control, Voice
	RuntimeModeGUI                  // GUI mode, using Wails for GUI & Standalone server, only supports Standalone distribution mode
)

const (
	DistributionModeStandalone int8 = iota // Standalone mode, no distribution all in one Server
	DistributionModeControl                // Only Control Server, no Voice. Used as Control-Node for Voice Servers
	DistributionModeVoice                  // Only Voice Server, no Control. Used as Voice-Node for Control Servers
)

type DistributionState struct {
	sync.RWMutex
	DistributionMode int8
	RuntimeMode      int8
}
