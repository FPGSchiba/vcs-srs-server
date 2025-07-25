package state

import (
	"fmt"
	"gopkg.in/yaml.v3"
	"os"
	"slices"
	"sync"
)

// SettingsState holds the current state of the settings
type SettingsState struct {
	sync.RWMutex `yaml:"-"`
	Servers      ServerSettings       `yaml:"servers"`
	Coalitions   []Coalition          `yaml:"coalitions"`
	Frequencies  FrequencySettings    `yaml:"frequencies"`
	General      GeneralSettings      `yaml:"general"`
	Security     SecuritySettings     `yaml:"security"`
	VoiceControl VoiceControlSettings `yaml:"voiceControl"`
	file         string               `yaml:"-"`
}

type ServerSettings struct {
	// ServerSettings holds the current settings of the server
	HTTP    ServerSetting `yaml:"http"`
	Voice   ServerSetting `yaml:"voice"`
	Control ServerSetting `yaml:"control"`
}

type ServerSetting struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
}

type Coalition struct {
	// The Coalition holds the current settings of the coalition
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Color       string `yaml:"color"`
	Password    string `yaml:"password"`
}

type FrequencySettings struct {
	// FrequencySettings holds the current settings of the frequency
	TestFrequencies   []float32 `yaml:"testFrequencies"`
	GlobalFrequencies []float32 `yaml:"globalFrequencies"`
}

type GeneralSettings struct {
	// GeneralSettings holds the current settings of the general settings
	MaxRadiosPerUser int `yaml:"maxRadiosPerUser"`
}

type SecuritySettings struct {
	Plugins          []PluginSettings `yaml:"plugins"`
	EnablePluginAuth bool             `yaml:"enablePluginAuth"`
	EnableGuestAuth  bool             `yaml:"enableGuestAuth"`
	Token            TokenSettings    `yaml:"token"`
}

type PluginSettings struct {
	Name          string            `yaml:"name"`
	Enabled       bool              `yaml:"enabled"`
	Address       string            `yaml:"address"`
	Configuration map[string]string `yaml:"configuration"` // Generic configuration for the plugin
}

type TokenSettings struct {
	Expiration     int64  `yaml:"expiration"`
	PrivateKeyFile string `yaml:"privateKeyFile"`
	PublicKeyFile  string `yaml:"publicKeyFile"`
	Issuer         string `yaml:"issuer"`
	Subject        string `yaml:"subject"`
}

type VoiceControlSettings struct {
	Port            int    `yaml:"port"`
	RemoteHost      string `yaml:"remoteHost"`
	ListenHost      string `yaml:"listenHost"`
	CertificateFile string `yaml:"certificateFile"`
	PrivateKeyFile  string `yaml:"privateKeyFile"`
}

func GetSettingsState(file string) (*SettingsState, error) {
	// Load values from file if it exists
	yamlFile, err := os.ReadFile(file)
	if err != nil {
		// If the file doesn't exist, create a new one with default values
		if os.IsNotExist(err) {
			const defaultHost = "0.0.0.0"
			const defaultPort = 5002

			settings := &SettingsState{
				file: file,
				Servers: ServerSettings{
					HTTP: ServerSetting{
						Host: defaultHost,
						Port: 80,
					},
					Voice: ServerSetting{
						Host: defaultHost,
						Port: defaultPort,
					},
					Control: ServerSetting{
						Host: defaultHost,
						Port: defaultPort,
					},
				},
				Coalitions: make([]Coalition, 0),
				Frequencies: FrequencySettings{
					TestFrequencies:   make([]float32, 0),
					GlobalFrequencies: make([]float32, 0),
				},
				General: GeneralSettings{
					MaxRadiosPerUser: 20,
				},
				Security: SecuritySettings{
					Plugins:          make([]PluginSettings, 0),
					EnablePluginAuth: false,
					EnableGuestAuth:  true,
					Token: TokenSettings{
						Expiration:     28800, // 8 hours
						PrivateKeyFile: "/path/to/ecdsa_key.pem",
						PublicKeyFile:  "/path/to/ecdsa_pubkey.pem",
						Issuer:         "https://vcs.vngd.net",
						Subject:        "vcs.vngd.net",
					},
				},
				VoiceControl: VoiceControlSettings{
					Port:            14448,
					RemoteHost:      "localhost", // Default remote host is empty
					ListenHost:      defaultHost,
					CertificateFile: "/path/to/voicecontrol-cert.pem",
					PrivateKeyFile:  "/path/to/voicecontrol-private-key.pem",
				},
			}
			err = settings.Save()
			if err != nil {
				return settings, err
			}
			return settings, nil
		} else {
			return nil, err
		}
	}
	// If the file exists, unmarshal it into the SettingsState struct
	settings := &SettingsState{}
	err = yaml.Unmarshal(yamlFile, settings)
	if err != nil {
		return nil, err
	}
	settings.file = file
	// Return the settings
	return settings, nil
}

func (s *SettingsState) Save() error {
	// Save the settings to the file
	yamlData, err := yaml.Marshal(s)
	if err != nil {
		return err
	}

	err = os.WriteFile(s.file, yamlData, 0777)
	if err != nil {
		return err
	}
	return nil
}

func (s *SettingsState) GetAllPluginNames() []string {
	var pluginNames []string
	s.RLock()
	defer s.RUnlock()
	for _, plugin := range s.Security.Plugins {
		if plugin.Enabled {
			pluginNames = append(pluginNames, plugin.Name)
		}
	}
	return pluginNames
}

func (s *SettingsState) GetPluginConfiguration(pluginName string) (map[string]string, bool) {
	s.RLock()
	defer s.RUnlock()
	for _, plugin := range s.Security.Plugins {
		if plugin.Name == pluginName {
			return plugin.Configuration, true
		}
	}
	return nil, false
}

func (s *SettingsState) GetPluginAddress(pluginName string) (string, bool) {
	s.RLock()
	defer s.RUnlock()
	for _, plugin := range s.Security.Plugins {
		if plugin.Name == pluginName {
			return plugin.Address, true
		}
	}
	return "", false
}

func (s *SettingsState) IsPluginEnabled(pluginName string) bool {
	s.RLock()
	defer s.RUnlock()
	for _, plugin := range s.Security.Plugins {
		if plugin.Name == pluginName {
			return plugin.Enabled
		}
	}
	return false
}

func (s *SettingsState) SetPluginEnabled(pluginName string, enabled bool) error {
	s.Lock()
	defer s.Unlock()
	for i, plugin := range s.Security.Plugins {
		if plugin.Name == pluginName {
			s.Security.Plugins[i].Enabled = enabled
			return nil
		}
	}
	return fmt.Errorf("plugin '%s' does not exists", pluginName)
}

func (s *SettingsState) DoesCoalitionExist(coalitionName string) bool {
	s.RLock()
	defer s.RUnlock()
	for _, coalition := range s.Coalitions {
		if coalition.Name == coalitionName {
			return true
		}
	}
	return false
}

func (s *SettingsState) IsFrequencyGlobal(freq float32) bool {
	s.RLock()
	defer s.RUnlock()
	return slices.Contains(s.Frequencies.GlobalFrequencies, freq)
}

func (s *SettingsState) IsFrequencyTest(freq float32) bool {
	s.RLock()
	defer s.RUnlock()
	return slices.Contains(s.Frequencies.TestFrequencies, freq)
}
