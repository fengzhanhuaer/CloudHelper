package main

import (
	"embed"
	"log"

	"github.com/cloudhelper/probe_manager/backend"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	backend.InitManagerLogger()

	// Create an instance of the app structure
	app := NewApp()
	tray := newTrayController(app)
	tray.Start()
	defer tray.Stop()

	// Create application with options
	err := wails.Run(&options.App{
		Title:             "Probe Manager",
		Width:             1024,
		Height:            768,
		HideWindowOnClose: true,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 27, G: 38, B: 54, A: 1},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		log.Printf("failed to run probe manager: %v", err)
	}
}
