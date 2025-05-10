package state

import "sync"

type ServerState struct {
	sync.RWMutex
	// State holds the current state of the server
	Clients map[string]*ClientState
}

type ClientState struct {
	ID string
}
