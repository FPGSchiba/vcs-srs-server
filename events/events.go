package events

import (
	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/google/uuid"
)

// SettingsEvent is an event triggered when the settings are changed
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

const (
	LogEntry     = "logs/entry"
	ServerAction = "admin/server-action"
)

type ClientChangeType int

const (
	ClientJoined ClientChangeType = iota
	ClientLeft
	ClientInfoUpdated
)

type RadioChangeType int

const (
	RadioUpdated RadioChangeType = iota
)

type ActionType int

const (
	ActionKick ActionType = iota
	ActionBan
	ActionMute
	ActionUnmute
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

type ClientChangeEvent struct {
	Type     ClientChangeType
	ClientID uuid.UUID
	Clients  map[uuid.UUID]*state.ClientState
}

type RadioChangeEvent struct {
	Type     RadioChangeType
	ClientID uuid.UUID
	Radios   map[uuid.UUID]*state.RadioState
}

type ServerActionEvent struct {
	ActionType     ActionType
	TargetClientID uuid.UUID
	Reason         string
}

func NewNotification(title, message, level string) Notification {
	return Notification{
		Title:   title,
		Message: message,
		Level:   level,
		Id:      uuid.New().String(),
	}
}

func NewEvent(name string, data interface{}) Event {
	return Event{
		Name: name,
		Data: data,
	}
}
