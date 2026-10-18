// System Pulse: a Wails desktop app whose data all comes from JavaScript
// running in jsc2go, inside the Go program, with the system as Go modules.
//
//	wails dev      (development, with the frontend's live reload)
//	wails build    (build/bin/SystemPulse.exe: frontend, backend script and engine in one exe)
//
// The executable is a fixed core: backend/app.js and frontend/dist are read
// from disk when they are there (next to the exe, or in the project), so
// changing them needs no rebuild; the copies built in are the fallback.
package main

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	var ui fs.FS = assets
	if index := findFile("frontend/dist/index.html"); index != "" {
		ui = os.DirFS(filepath.Dir(index))
	}
	app := NewApp()
	err := wails.Run(&options.App{
		Title:            "System Pulse",
		Width:            1280,
		Height:           860,
		MinWidth:         860,
		MinHeight:        600,
		AssetServer:      &assetserver.Options{Assets: ui},
		BackgroundColour: &options.RGBA{R: 7, G: 8, B: 13, A: 1},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		Bind:             []any{app},
	})
	if err != nil {
		println("Error:", err.Error())
	}
}
