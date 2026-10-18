package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/baslentfert/jsc2go"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// The copy of app.js built into the executable, used when there is none on
// disk.
//
//go:embed backend/app.js
var backend embed.FS

// App is what the UI binds to: Invoke calls the app's JavaScript handlers.
// The executable is a fixed core: app.js is read from disk when there is
// one (findFile) and runs again when it changes, without a rebuild.
type App struct {
	ctx context.Context

	mu       sync.Mutex
	engine   *Engine
	finished chan struct{} // closed when engine's script and loop have ended
	stop     chan struct{}
}

func NewApp() *App { return &App{stop: make(chan struct{})} }

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	path := findFile("backend/app.js")
	if path == "" {
		src, _ := backend.ReadFile("backend/app.js")
		a.run("app.js", string(src))
		return
	}
	wruntime.LogInfof(ctx, "app.js: %s (reloads on change)", path)
	a.load(path)
	go a.watch(path)
}

// load runs the app.js at path in a new engine, after stopping the old one.
func (a *App) load(path string) {
	src, err := os.ReadFile(path)
	if err != nil {
		wruntime.LogErrorf(a.ctx, "app.js: %v", err)
		return
	}
	a.run(filepath.Base(path), string(src))
}

func (a *App) run(name, src string) {
	ctx := a.ctx
	e, err := NewEngine(Window{
		Emit:     func(name string, payload any) { wruntime.EventsEmit(ctx, name, payload) },
		SetTitle: func(title string) { wruntime.WindowSetTitle(ctx, title) },
		SetSize:  func(w, h int) { wruntime.WindowSetSize(ctx, w, h) },
		Center:   func() { wruntime.WindowCenter(ctx) },
	})
	if err != nil {
		wruntime.LogErrorf(ctx, "engine: %v", err)
		return
	}
	finished := make(chan struct{})
	a.mu.Lock()
	old, oldFinished := a.engine, a.finished
	a.engine, a.finished = e, finished
	a.mu.Unlock()
	if old != nil {
		old.Stop()
		<-oldFinished
	}
	done := e.Start(name, src)
	go func() {
		defer close(finished)
		// (Stop ends the loop with context.Canceled or ErrInterrupted.)
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, jsc2go.ErrInterrupted) {
			wruntime.LogErrorf(ctx, "app.js: %v", err)
			wruntime.EventsEmit(ctx, "error", err.Error())
		}
	}()
}

// watch reloads app.js when it changes, and the window with it, so that
// the UI asks the new handlers again. (Polling: no watcher dependency for
// one file.)
func (a *App) watch(path string) {
	modTime := func() time.Time {
		if fi, err := os.Stat(path); err == nil {
			return fi.ModTime()
		}
		return time.Time{}
	}
	last := modTime()
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-a.stop:
			return
		case <-t.C:
		}
		if m := modTime(); !m.IsZero() && !m.Equal(last) {
			last = m
			wruntime.LogInfof(a.ctx, "app.js changed: reloading")
			a.load(path)
			wruntime.WindowReloadApp(a.ctx)
		}
	}
}

func (a *App) shutdown(context.Context) {
	close(a.stop)
	a.mu.Lock()
	e := a.engine
	a.mu.Unlock()
	if e != nil {
		e.Stop()
	}
}

// Invoke calls the handler app.js registered as cmd with args (as the
// window's invoke in Tauri). (A list: Wails cannot pass a
// JSON null to an any parameter.)
func (a *App) Invoke(cmd string, args []any) (any, error) {
	a.mu.Lock()
	e := a.engine
	a.mu.Unlock()
	if e == nil {
		return nil, fmt.Errorf("the engine did not start")
	}
	v, err := e.Call(a.ctx, cmd, args)
	if err != nil {
		wruntime.LogErrorf(a.ctx, "invoke %s: %v", cmd, err)
	}
	return v, err
}

// findFile looks for rel next to the executable, in the project two
// levels up from it (wails build puts the exe in build/bin) and in the
// working directory (wails dev, go run); "" if there is none.
func findFile(rel string) string {
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		dirs = append(dirs, dir, filepath.Join(dir, "..", ".."))
	}
	if wd, err := os.Getwd(); err == nil {
		dirs = append(dirs, wd)
	}
	for _, d := range dirs {
		p := filepath.Join(d, filepath.FromSlash(rel))
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			p, _ = filepath.Abs(p)
			return p
		}
	}
	return ""
}
