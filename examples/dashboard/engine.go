package main

// The engine: a jsc2go Runtime running backend/app.js on its own goroutine,
// with the system as Go modules it imports (as Bun's node:os, bun:sqlite
// and Bun.$), and a window module whose handlers the UI calls and whose
// events go to the UI.

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strings"
	"sync"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
	_ "modernc.org/sqlite"

	"github.com/baslentfert/jsc2go"
)

// Engine runs the app's JavaScript.
type Engine struct {
	rt       *jsc2go.Runtime
	mu       sync.Mutex
	handlers map[string]jsc2go.Value // touched on the loop goroutine (handle), read by Call through Invoke
	cancel   context.CancelFunc
}

// Window is what app.js controls of the window (app:window).
type Window struct {
	Emit     func(name string, payload any)
	SetTitle func(title string)
	SetSize  func(width, height int)
	Center   func()
}

// NewEngine makes the Runtime and its modules; win is the window app.js
// controls (its functions may be nil without a window, as in tests).
func NewEngine(win Window) (*Engine, error) {
	e := &Engine{rt: jsc2go.New(), handlers: map[string]jsc2go.Value{}}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // (one in-memory database)
	self, _ := process.NewProcess(int32(os.Getpid()))
	exe, _ := os.Executable()

	modules := map[string]map[string]any{
		"sys:os": {
			"platform": func() string { return runtime.GOOS },
			"arch":     func() string { return runtime.GOARCH },
			"hostname": func() string { h, _ := os.Hostname(); return h },
			"user": func() string {
				if u, err := user.Current(); err == nil {
					return u.Username
				}
				return ""
			},
			"version": func() string {
				if i, err := host.Info(); err == nil {
					return strings.TrimSpace(i.Platform + " " + i.PlatformVersion)
				}
				return runtime.GOOS
			},
			"uptime": func() uint64 { u, _ := host.Uptime(); return u },
			// cpus: per logical CPU its cumulative times (seconds), as Node's
			// os.cpus().times.
			"cpus": func() ([]map[string]any, error) {
				times, err := cpu.Times(true)
				if err != nil {
					return nil, err
				}
				model, speed := "", 0.0
				if info, err := cpu.Info(); err == nil && len(info) > 0 {
					model, speed = info[0].ModelName, info[0].Mhz
				}
				out := make([]map[string]any, len(times))
				for i, t := range times {
					out[i] = map[string]any{"model": model, "speed": speed, "times": map[string]float64{
						"user": t.User, "nice": t.Nice, "sys": t.System, "idle": t.Idle, "irq": t.Irq,
					}}
				}
				return out, nil
			},
			"memory": func() (map[string]uint64, error) {
				m, err := mem.VirtualMemory()
				if err != nil {
					return nil, err
				}
				return map[string]uint64{"total": m.Total, "free": m.Available}, nil
			},
			"network": func() ([]map[string]string, error) {
				ifs, err := net.Interfaces()
				if err != nil {
					return nil, err
				}
				var out []map[string]string
				for _, i := range ifs {
					internal := false
					for _, f := range i.Flags {
						internal = internal || f == "loopback"
					}
					if internal || !contains(i.Flags, "up") {
						continue
					}
					for _, a := range i.Addrs {
						addr, _, _ := strings.Cut(a.Addr, "/")
						family := "IPv4"
						if strings.Contains(addr, ":") {
							family = "IPv6"
						}
						out = append(out, map[string]string{"name": i.Name, "address": addr, "family": family, "mac": i.HardwareAddr})
					}
				}
				return out, nil
			},
		},
		"sys:disk": {
			"list": func() []map[string]any {
				parts, _ := disk.Partitions(false)
				var out []map[string]any
				for _, p := range parts {
					if u, err := disk.Usage(p.Mountpoint); err == nil && u.Total > 0 {
						out = append(out, map[string]any{"name": p.Mountpoint, "total": u.Total, "free": u.Free})
					}
				}
				return out
			},
		},
		"sys:proc": {
			// list: every process's name, pid and resident memory in KB.
			"list": func() []map[string]any {
				ps, _ := process.Processes()
				out := make([]map[string]any, 0, len(ps))
				for _, p := range ps {
					name, err := p.Name()
					if err != nil || name == "" {
						continue
					}
					var kb uint64
					if m, err := p.MemoryInfo(); err == nil {
						kb = m.RSS / 1024
					}
					out = append(out, map[string]any{"name": name, "pid": p.Pid, "memKB": kb})
				}
				return out
			},
			"self": func() map[string]any {
				var rss uint64
				if self != nil {
					if m, err := self.MemoryInfo(); err == nil {
						rss = m.RSS
					}
				}
				return map[string]any{"pid": os.Getpid(), "rss": rss, "exe": exe}
			},
		},
		"sys:shell": {
			// run: a command's output (as Bun.$`...`.text()).
			"run": func(name string, args ...string) (string, error) {
				c := exec.Command(name, args...)
				hideWindow(c)
				out, err := c.Output()
				return string(out), err
			},
		},
		"sys:db": {
			"exec": func(query string, args ...any) error {
				_, err := db.Exec(query, sqlArgs(args)...)
				return err
			},
			"query": func(query string, args ...any) ([]map[string]any, error) { return rows(db, query, sqlArgs(args)) },
			"get": func(query string, args ...any) (map[string]any, error) {
				r, err := rows(db, query, sqlArgs(args))
				if err != nil || len(r) == 0 {
					return nil, err
				}
				return r[0], nil
			},
		},
		"app:window": {
			"emit":     orNop2(win.Emit),
			"setTitle": orNop1(win.SetTitle),
			"setSize":  orNopSize(win.SetSize),
			"center":   orNop0(win.Center),
			// handle: the function the UI calls by name.
			"handle": func(name string, fn jsc2go.Value) error {
				if !fn.IsFunction() {
					return fmt.Errorf("handle(%q): not a function", name)
				}
				e.mu.Lock()
				e.handlers[name] = fn
				e.mu.Unlock()
				return nil
			},
		},
		"app:info": {
			"engine": "jsc2go " + jsc2go.Version,
			"go":     runtime.Version(),
		},
	}
	for spec, exports := range modules {
		if err := e.rt.RegisterModule(spec, exports); err != nil {
			return nil, err
		}
	}
	return e, nil
}

func orNop0(f func()) func() {
	if f == nil {
		return func() {}
	}
	return f
}
func orNop1(f func(string)) func(string) {
	if f == nil {
		return func(string) {}
	}
	return f
}
func orNop2(f func(string, any)) func(string, any) {
	if f == nil {
		return func(string, any) {}
	}
	return f
}
func orNopSize(f func(int, int)) func(int, int) {
	if f == nil {
		return func(int, int) {}
	}
	return f
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// sqlArgs passes JavaScript's integral numbers (float64 in Go) as integers,
// as SQLite's INTEGER columns want them.
func sqlArgs(args []any) []any {
	for i, a := range args {
		if f, ok := a.(float64); ok && f == math.Trunc(f) && math.Abs(f) < 1<<53 {
			args[i] = int64(f)
		}
	}
	return args
}

// rows runs a query and returns its rows as maps.
func rows(db *sql.DB, query string, args []any) ([]map[string]any, error) {
	rs, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	cols, _ := rs.Columns()
	var out []map[string]any
	for rs.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(cols))
		for i, c := range cols {
			row[c] = vals[i]
		}
		out = append(out, row)
	}
	return out, rs.Err()
}

// Start runs the app's module, then the event loop, on a goroutine of its
// own until Stop.
func (e *Engine) Start(name, src string) <-chan error {
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	done := make(chan error, 1)
	go func() {
		if err := e.rt.RunModule(name, src); err != nil {
			done <- err
			return
		}
		done <- e.rt.RunLoop(ctx)
	}()
	return done
}

// Call calls the handler the app registered as name, from any goroutine,
// and waits for its result (also for an async handler's promise).
func (e *Engine) Call(ctx context.Context, name string, args []any) (any, error) {
	e.mu.Lock()
	fn, ok := e.handlers[name]
	e.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("no handler %q", name)
	}
	return e.rt.Invoke(ctx, fn, args...)
}

// Stop ends the event loop and any script still running.
func (e *Engine) Stop() {
	if e.cancel != nil {
		e.cancel()
	}
	e.rt.Interrupt()
}
