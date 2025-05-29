package main

import (
	"embed"
	"flag"
	"github.com/FPGSchiba/vcs-srs-server/app"
	"github.com/FPGSchiba/vcs-srs-server/services"
	"github.com/samber/slog-multi"
	"github.com/wailsapp/wails/v3/pkg/application"
	"log"
	"log/slog"
	"os"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	var configFilepath string
	var autoStartServers bool
	var headlessMode bool
	flag.StringVar(&configFilepath, "config", "config.yaml", "The Path to the config file")
	flag.BoolVar(&autoStartServers, "autostart", false, "Automatically start the servers on startup") // For console only applications
	flag.BoolVar(&headlessMode, "headless", false, "Run in headless mode")
	flag.Parse()

	if _, err := os.Stat("log"); os.IsNotExist(err) {
		err := os.Mkdir("log", 0666)
		if err != nil {
			log.Fatalf("error creating log directory: %v", err)
		}
	}

	f, err := os.OpenFile("log/vcs-server-log.json", os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		log.Fatalf("error opening log file: %v", err)
	}

	logger := slog.New(slogmulti.Fanout(
		slog.NewTextHandler(os.Stdout, nil),
		slog.NewJSONHandler(f, nil),
	))

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

	if !headlessMode {
		// Normal mode with UI
		appOptions.Assets = application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		}
	}

	wails := application.New(appOptions)
	vcs.StartUp(wails, configFilepath, autoStartServers)

	if !headlessMode {
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
	}

	// Run the application
	err = wails.Run()

	if err != nil {
		println("Error:", err.Error())
	}
}
