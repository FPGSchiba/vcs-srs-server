package state

import (
	"encoding/json"
	"github.com/google/uuid"
	"os"
	"sync"
	"time"
)

type ServerState struct {
	sync.RWMutex
	// State holds the current state of the server
	Clients      map[uuid.UUID]*ClientState
	RadioClients map[uuid.UUID]*RadioState
	BannedState  BannedState
}

type ClientState struct {
	Name       string
	UnitId     string
	Coalition  string
	Role       uint8
	LastUpdate time.Time
}

type RadioState struct {
	Radios []Radio
	Muted  bool
}

type Radio struct {
	ID         uint32
	Name       string
	Frequency  float32
	Enabled    bool
	IsIntercom bool
}

type BannedState struct {
	BannedClients []BannedClient
	file          string
}

type BannedClient struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IPAddress string `json:"ip_address"`
	Reason    string `json:"reason"`
}

func ensureBanFileExists(bannedFile string) error {
	_, err := os.Stat(bannedFile)
	if os.IsNotExist(err) {
		f, createErr := os.Create(bannedFile)
		if createErr != nil {
			return createErr
		}
		defer f.Close()
	}
	return err
}

func getBanFile(bannedFile string) (string, error) {
	err := ensureBanFileExists(bannedFile)
	if err != nil {
		return bannedFile, err
	}
	return bannedFile, nil
}

func GetBannedState(bannedFile string) (*BannedState, error) {
	file, err := getBanFile(bannedFile)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var bannedState BannedState
	decoder := json.NewDecoder(f)
	err = decoder.Decode(&bannedState.BannedClients)
	if err != nil {
		return nil, err
	}
	bannedState.file = file
	return &bannedState, nil
}

func (b *BannedState) Save() error {
	file, err := getBanFile(b.file)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(file, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0777)
	if err != nil {
		return err
	}
	defer f.Close()
	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ")
	err = encoder.Encode(b.BannedClients)
	if err != nil {
		return err
	}
	return nil
}

func (s *ServerState) AddClient(clientGuid uuid.UUID, client *ClientState) {
	s.Lock()
	defer s.Unlock()
	if s.Clients == nil {
		s.Clients = make(map[uuid.UUID]*ClientState)
	}
	s.Clients[clientGuid] = client
	s.RadioClients[clientGuid] = &RadioState{
		Radios: []Radio{},
		Muted:  false,
	}
}

func (s *ServerState) GetAllClients() []struct {
	ID    uuid.UUID
	State *ClientState
} {
	s.RLock()
	defer s.RUnlock()
	clients := make([]struct {
		ID    uuid.UUID
		State *ClientState
	}, 0, len(s.Clients))
	for id, client := range s.Clients {
		clients = append(clients, struct {
			ID    uuid.UUID
			State *ClientState
		}{ID: id, State: client})
	}
	return clients
}

func (s *ServerState) GetAllRadios() []struct {
	ID    uuid.UUID
	State *RadioState
} {
	s.RLock()
	defer s.RUnlock()
	radios := make([]struct {
		ID    uuid.UUID
		State *RadioState
	}, 0, len(s.RadioClients))
	for id, radio := range s.RadioClients {
		radios = append(radios, struct {
			ID    uuid.UUID
			State *RadioState
		}{ID: id, State: radio})
	}
	return radios
}

func (s *ServerState) GetAllEnabledFrequencies(clientGuid uuid.UUID) []float32 {
	s.RLock()
	defer s.RUnlock()
	if clientState, exists := s.RadioClients[clientGuid]; exists {
		var enabledFrequencies []float32
		for _, radio := range clientState.Radios {
			if radio.Enabled {
				enabledFrequencies = append(enabledFrequencies, radio.Frequency)
			}
		}
		return enabledFrequencies
	}
	return nil
}

func (s *ServerState) IsListeningOnFrequency(clientGuid uuid.UUID, frequency float32) bool {
	if clientState, exists := s.RadioClients[clientGuid]; exists {
		for _, radio := range clientState.Radios {
			if radio.Frequency == frequency {
				return radio.Enabled
			}
		}
	}
	return false
}

func (s *ServerState) DoesClientExist(clientGuid uuid.UUID) bool {
	s.RLock()
	defer s.RUnlock()
	_, exists := s.Clients[clientGuid]
	if !exists {
		return false
	}
	return true
}
