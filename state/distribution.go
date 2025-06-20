package state

import "sync"

const (
	RuntimeModeHeadless int8 = 0 // Headless mode, no GUI, distribution modes available: Standalone, Control, Voice
	RuntimeModeGUI      int8 = 1 // GUI mode, using Wails for GUI & Standalone server, only supports Standalone distribution mode
)

const (
	DistributionModeStandalone int8 = 0 // Standalone mode, no distribution all in one Server
	DistributionModeControl    int8 = 1 // Only Control Server, no Voice. Used as Control-Node for Voice Servers
	DistributionModeVoice      int8 = 2 // Only Voice Server, no Control. Used as Voice-Node for Control Servers
)

type DistributionState struct {
	sync.RWMutex
	DistributionMode int8
	RuntimeMode      int8
}
