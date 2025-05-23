package state

import (
	"encoding/json"
	"os"
	"sync"
)

const (
	banFileName = "banned_clients.json"
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
}

type BannedClient struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IPAddress string `json:"ip_address"`
	Reason    string `json:"reason"`
}

func ensureBanFileExists() error {
	_, err := os.Stat(banFileName)
	if os.IsNotExist(err) {
		f, createErr := os.Create(banFileName)
		if createErr != nil {
			return createErr
		}
		defer f.Close()
	}
	return err
}

func getBanFile() (string, error) {
	err := ensureBanFileExists()
	if err != nil {
		return banFileName, err
	}
	return banFileName, nil
}

func GetBannedState() (*BannedState, error) {
	file, err := getBanFile()
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
	return &bannedState, nil
}

func (b *BannedState) Save() error {
	file, err := getBanFile()
	if err != nil {
		return err
	}
	f, err := os.OpenFile(file, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
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
