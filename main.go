//go:build !headless

package main

import (
	"embed"
	"github.com/FPGSchiba/vcs-srs-server/app"
	"github.com/FPGSchiba/vcs-srs-server/services"
	"github.com/wailsapp/wails/v3/pkg/application"
	"log/slog"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	configFilepath, bannedFilePath, autoStartServers, logger := parseFlags()

	vcs := app.New()

	// Create application with options
	appOptions := application.Options{
		Name:        "vcs-server",
		Description: "A Voice Communication Server for Vanguard",
		Logger:      logger,
		LogLevel:    slog.LevelInfo,
		Services: []application.Service{
			application.NewService(services.NewNotificationService(vcs)),
			application.NewService(services.NewClientService(vcs)),
			application.NewService(services.NewControlService(vcs)),
			application.NewService(services.NewCoalitionService(vcs)),
			application.NewService(services.NewSettingsService(vcs)),
		},
	}

	// Normal mode with UI
	appOptions.Assets = application.AssetOptions{
		Handler: application.AssetFileServerFS(assets),
	}

	wails := application.New(appOptions)
	vcs.StartUp(wails, configFilepath, bannedFilePath, autoStartServers)

	wails.NewWebviewWindowWithOptions(application.WebviewWindowOptions{
		Title:          "VCS Server",
		Width:          1080,
		Height:         800,
		MaxHeight:      800,
		MaxWidth:       1080,
		MinHeight:      800,
		MinWidth:       1080,
		BackgroundType: application.BackgroundTypeTransparent,
		Frameless:      true,
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 50,
			Backdrop:                application.MacBackdropTransparent,
			TitleBar:                application.MacTitleBarHiddenInset,
		},
		Windows: application.WindowsWindow{
			DisableIcon:                       false,
			DisableFramelessWindowDecorations: true,
			WindowMaskDraggable:               true,
		},
		Linux: application.LinuxWindow{
			WindowIsTranslucent: true,
		},
		BackgroundColour: application.NewRGBA(0, 0, 0, 0),
		URL:              "/",
	})

	// Run the application
	err := wails.Run()

	if err != nil {
		println("Error:", err.Error())
	}
}
