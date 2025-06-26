package state

import (
	"gopkg.in/yaml.v3"
	"os"
	"sync"
)

// SettingsState holds the current state of the settings
type SettingsState struct {
	sync.RWMutex `yaml:"-"`
	Servers      ServerSettings    `yaml:"servers"`
	Coalitions   []Coalition       `yaml:"coalitions"`
	Frequencies  FrequencySettings `yaml:"frequencies"`
	General      GeneralSettings   `yaml:"general"`
	Security     SecuritySettings  `yaml:"security"`
	file         string            `yaml:"-"`
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
	TestFrequencies   []float64 `yaml:"testFrequencies"`
	GlobalFrequencies []float64 `yaml:"globalFrequencies"`
}

type GeneralSettings struct {
	// GeneralSettings holds the current settings of the general settings
	MaxRadiosPerUser int `yaml:"maxRadiosPerUser"`
}

type SecuritySettings struct {
	VanguardToken      string               `yaml:"vanguardToken"`
	VanguardApiKey     string               `yaml:"vanguardApiKey"`
	VanguardApiBaseUrl string               `yaml:"vanguardBaseUrl"`
	EnableVanguardAuth bool                 `yaml:"enableVanguardAuth"`
	EnableGuestAuth    bool                 `yaml:"enableGuestAuth"`
	Token              TokenSettings        `yaml:"token"`
	VoiceControl       VoiceControlSettings `yaml:"voiceControl"`
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
					TestFrequencies:   make([]float64, 0),
					GlobalFrequencies: make([]float64, 0),
				},
				General: GeneralSettings{
					MaxRadiosPerUser: 20,
				},
				Security: SecuritySettings{
					VanguardToken:      "super-secret-token",
					VanguardApiKey:     "super-secret-api-key",
					VanguardApiBaseUrl: "https://profile.vngd.net/_functions/",
					EnableVanguardAuth: false,
					EnableGuestAuth:    true,
					Token: TokenSettings{
						Expiration:     28800, // 8 hours
						PrivateKeyFile: "/path/to/ecdsa_key.pem",
						PublicKeyFile:  "/path/to/ecdsa_pubkey.pem",
						Issuer:         "https://vcs.vngd.net",
						Subject:        "vcs.vngd.net",
					},
					VoiceControl: VoiceControlSettings{
						Port:            14448,
						RemoteHost:      "localhost", // Default remote host is empty
						ListenHost:      defaultHost,
						CertificateFile: "/path/to/voicecontrol-cert.pem",
						PrivateKeyFile:  "/path/to/voicecontrol-private-key.pem",
					},
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

func (settings *SettingsState) Save() error {
	// Save the settings to the file
	yamlData, err := yaml.Marshal(settings)
	if err != nil {
		return err
	}

	err = os.WriteFile(settings.file, yamlData, 0777)
	if err != nil {
		return err
	}
	return nil
}
