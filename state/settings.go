package state

import (
	"gopkg.in/yaml.v3"
	"os"
	"sync"
)

type SettingsState struct {
	sync.RWMutex
	// State holds the current state of the settings
	Servers    ServerSettings `yaml:"servers"`
	Coalitions []Coalition    `yaml:"coalitions"`
}

type ServerSettings struct {
	sync.RWMutex
	// ServerSettings holds the current settings of the server
	HTTP    ServerSetting `yaml:"http"`
	Voice   ServerSetting `yaml:"voice"`
	Control ServerSetting `yaml:"control"`
}

type ServerSetting struct {
	sync.RWMutex
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
}

type Coalition struct {
	sync.RWMutex
	// The Coalition holds the current settings of the coalition
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Color       string `yaml:"color"`
	Password    string `yaml:"password"`
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
			err = settings.Save(file)
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
	// Return the settings
	return settings, nil
}

func (settings *SettingsState) Save(file string) error {
	// Save the settings to the file
	yamlData, err := yaml.Marshal(settings)
	if err != nil {
		return err
	}

	err = os.WriteFile(file, yamlData, 0644)
	if err != nil {
		return err
	}
	return nil
}
