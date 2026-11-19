// Command pims is the PIMS desktop app: it provisions a Confluent Cloud
// Tableflow + Debezium demo, simulates a clinic's patient data and charts
// Tableflow's freshness metrics.
package main

import (
	"embed"
	"log/slog"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"

	"pims/internal/config"
	"pims/internal/hostenv"
	"pims/internal/logging"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	// Before anything can look for docker: a Finder launch has no shell PATH.
	addedToPath, pathErr := hostenv.EnsureToolPath()

	ws, err := config.DefaultWorkspace()
	if err != nil {
		slog.Error("Could not resolve workspace", "error", err)
		os.Exit(1)
	}

	app := NewApp(ws)
	base := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})
	log := slog.New(logging.NewTeeHandler(base, app.onLog))
	slog.SetDefault(log)
	app.attachLogger(log)
	if pathErr != nil {
		log.Warn("Could not add tool directories to PATH", "error", pathErr)
	} else if len(addedToPath) > 0 {
		log.Info("Added tool directories to PATH", "dirs", addedToPath)
	}

	err = wails.Run(&options.App{
		Title:     "PIMS · Tableflow CDC demo",
		Width:     1280,
		Height:    860,
		MinWidth:  960,
		MinHeight: 640,
		// Width and Height still apply when the window is un-maximised.
		WindowStartState: options.Maximised,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 248, G: 250, B: 252, A: 1},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		Bind:             []any{app},
	})
	if err != nil {
		log.Error("PIMS exited with an error", "error", err)
		os.Exit(1)
	}
}
