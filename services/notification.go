package services

import (
	"github.com/FPGSchiba/vcs-srs-server/app"
	"github.com/FPGSchiba/vcs-srs-server/events"
)

type NotificationService struct {
	app *app.VCSApplication
}

func NewNotificationService(app *app.VCSApplication) *NotificationService {
	return &NotificationService{
		app: app,
	}
}

func (n *NotificationService) Notify(notification events.Notification) {
	n.app.Notify(notification)
}
