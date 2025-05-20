package main

import (
	"embed"
	"github.com/FPGSchiba/vcs-srs-server/app"
	"github.com/FPGSchiba/vcs-srs-server/utils"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
	"go.uber.org/zap"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	logger := utils.CreateLogger()

	defer func(logger *zap.Logger) {
		err := logger.Sync()
		if err != nil {
			println(err.Error())
		}
	}(logger)
	// Create an instance of the app structure
	gui := app.NewApp(logger)

	// Create application with options
	err := wails.Run(&options.App{
		Title:  "vcs-server",
		Width:  1080,
		Height: 800,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 0, G: 0, B: 0, A: 0},
		OnStartup:        gui.Startup,
		Bind: []interface{}{
			gui,
		},
		Frameless:     true,
		DisableResize: true,
		Debug: options.Debug{
			OpenInspectorOnStartup: true,
		},
		Windows: &windows.Options{
			WebviewIsTransparent: true,
			WindowIsTranslucent:  false,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
