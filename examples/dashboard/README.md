# System Pulse: a Wails desktop app scripted with jsc2go

A live system dashboard whose logic is JavaScript running in jsc2go, inside
the Go program, the way a Bun or Node backend drives a Tauri or Electron
window. The window is [Wails](https://wails.io) (a WebView2 / WebKit webview
with a Go backend); the data comes from `backend/app.js`.

```
 React UI (webview)  ── invoke("disks") ──▶  Go (Wails)  ── Invoke ──▶  app.js in jsc2go
                     ◀── "stats" events ───              ◀── emit ────       │
                                                                             ├─ import os   from "sys:os"
                                                                             ├─ import disk from "sys:disk"
                                                                             ├─ import proc from "sys:proc"
                                                                             ├─ import db   from "sys:db"     (SQLite)
                                                                             └─ import shell from "sys:shell"
```

- `backend/app.js` samples CPU and memory every second into an in-memory
  SQLite table, emits them to the UI, and answers the UI's calls (`handle`):
  system information, disks, processes, the last minute in SQL, and a speed
  test of plain JavaScript (SHA-256, recursion, allocation, JSON).
- `engine.go` makes the jsc2go Runtime: the system as Go modules
  (`RegisterModule`, with gopsutil and modernc.org/sqlite), `app:window`
  for `handle` and `emit`, and the event loop on its own goroutine
  (`RunLoop`); `Engine.Call` hands the UI's calls to it (`Invoke`, which
  also waits for an async handler's promise).
- `app.go` binds `Invoke` for the UI, sends `emit`'s events to it and lets
  app.js set the window's title, size and position (`setTitle`, `setSize`,
  `center` in `app:window`).
- `frontend/` is the React UI (Vite); `frontend/src/main.tsx` reaches the
  backend through `invoke` and `listen`.

## Run

Needs Go, Node.js (npm) and the Wails CLI (`go install
github.com/wailsapp/wails/v2/cmd/wails@latest`).

    wails dev        # development, with live reload of the UI
    wails build      # build/bin/SystemPulse.exe: UI, app.js and engine in one executable

## Change it without a rebuild

The executable is a fixed core. It reads `backend/app.js` and
`frontend/dist` from disk when they are there (next to the executable, two
levels up from it as `wails build` leaves it in `build/bin`, or in the
working directory); the copies built into the executable are the fallback.
While the app runs it watches `app.js`: save a change and the script runs
again in a fresh Runtime and the window reloads, without compiling
anything. A script that fails is logged (and sent to the UI as an `error`
event); the next save runs it again. (A changed UI needs `npm run build` in `frontend/`, still no
Go build.)

`go test` runs `backend/app.js` without a window and checks every handler
and the stats it emits (`engine_test.go`).
