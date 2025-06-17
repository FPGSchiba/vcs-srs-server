package state

import "sync"

type AdminState struct {
	sync.RWMutex
	HTTPStatus    ServiceStatus
	VoiceStatus   ServiceStatus
	ControlStatus ServiceStatus
}

type ServiceStatus struct {
	IsRunning bool
	IsNeeded  bool
	Error     string
}
