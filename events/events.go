package events

import (
	"github.com/google/uuid"
)

// SettingsEvent is an event that is triggered when the settings are changed
const (
	SettingsChanged   = "settings/changed"
	CoalitionsChanged = "settings/coalitions/changed"
)

const (
	AdminChanged = "admin/changed"
)

const (
	RadioClientsChanged  = "clients/radio/changed"
	ClientsChanged       = "clients/changed"
	BannedClientsChanged = "clients/banned/changed"
)

const (
	NotificationEvent = "notification"
)

type Notification struct {
	Title   string `json:"title"`
	Message string `json:"message"`
	Level   string `json:"level"` // info, warning, error
	Id      string `json:"id"`    // unique id for the notification
}

type Event struct {
	Name string // Name of the event
	Data interface{}
}

func NewNotification(title, message, level string) Notification {
	return Notification{
		Title:   title,
		Message: message,
		Level:   level,
		Id:      uuid.New().String(),
	}
}
