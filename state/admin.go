package state

import "sync"

type AdminState struct {
	sync.RWMutex
	HTTPStatus    ServiceStatus
	VoiceStatus   ServiceStatus
	ControlStatus ServiceStatus
	StopSignals   map[string]chan struct{}
}

type ServiceStatus struct {
	IsRunning bool
	Error     string
}
