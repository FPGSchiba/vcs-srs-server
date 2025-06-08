package state

import (
	"encoding/json"
	"os"
	"sync"
)

type ServerState struct {
	sync.RWMutex
	// State holds the current state of the server
	Clients      map[string]*ClientState
	RadioClients map[string]*RadioState
	BannedState  BannedState
}

type ClientState struct {
	Name      string
	UnitId    string
	Coalition string
}

type RadioState struct {
	Radios []Radio
	Muted  bool
}

type Radio struct {
	ID        int32
	Name      string
	Frequency float64
	Enabled   bool
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

func (s *ServerState) AddClient(clientGuid string, client *ClientState) {
	s.Lock()
	defer s.Unlock()
	if s.Clients == nil {
		s.Clients = make(map[string]*ClientState)
	}
	s.Clients[clientGuid] = client
	s.RadioClients[clientGuid] = &RadioState{
		Radios: []Radio{},
		Muted:  false,
	}
}
