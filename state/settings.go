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
			}
			err = settings.Save()
			if err != nil {
				return settings, err
			}
			return settings, nil
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

	err = os.WriteFile(settings.file, yamlData, 0644)
	if err != nil {
		return err
	}
	return nil
}
