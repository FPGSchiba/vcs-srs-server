package main

import (
	"embed"
	"go.uber.org/zap"
	"vcs-server/app"
	"vcs-server/utils"

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
		Width:  1024,
		Height: 768,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 27, G: 38, B: 54, A: 1},
		OnStartup:        gui.Startup,
		Bind: []interface{}{
			gui,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
