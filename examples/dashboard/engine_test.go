package main

import (
	"context"
	"os"
	"runtime"
	"testing"
	"time"
)

// TestEngine runs backend/app.js without a window: the stats it emits
// every second, and every handler the UI calls.
func TestEngine(t *testing.T) {
	events := make(chan map[string]any, 10)
	e, err := NewEngine(Window{Emit: func(name string, payload any) {
		if name == "stats" {
			events <- payload.(map[string]any)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile("backend/app.js")
	if err != nil {
		t.Fatal(err)
	}
	done := e.Start("app.js", string(src))
	defer func() {
		e.Stop()
		<-done
	}()

	select {
	case s := <-events:
		for _, k := range []string{"cpu", "cores", "mem", "memUsed", "memTotal", "uptime", "appRss"} {
			if _, ok := s[k]; !ok {
				t.Errorf("stats: no %s in %v", k, s)
			}
		}
		if cores, _ := s["cores"].([]any); len(cores) == 0 {
			t.Errorf("stats: no cores")
		}
	case err := <-done:
		t.Fatalf("app.js ended: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("no stats event")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	call := func(name string) any {
		t.Helper()
		v, err := e.Call(ctx, name, nil)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return v
	}
	sys := call("system").(map[string]any)
	for _, k := range []string{"hostname", "os", "arch", "cpu", "cores", "memTotal", "gpus", "network", "engine", "pid", "exe"} {
		if _, ok := sys[k]; !ok {
			t.Errorf("system: no %s", k)
		}
	}
	if disks, _ := call("disks").([]any); len(disks) == 0 {
		t.Error("disks: none")
	}
	procs := call("processes").(map[string]any)
	if top, _ := procs["top"].([]any); len(top) == 0 || procs["total"].(int64) == 0 {
		t.Errorf("processes: %v", procs)
	}
	// Let a second sample reach the database.
	<-events
	hist := call("history").(map[string]any)
	if pts, _ := hist["points"].([]any); len(pts) == 0 {
		t.Errorf("history: %v", hist)
	}
	bench := call("benchmark").(map[string]any)
	sha := bench["sha256"].(map[string]any)
	if sha["digest"] == "" || sha["ms"].(int64) <= 0 {
		t.Errorf("benchmark: %v", bench)
	}
	t.Logf("system: %s, %v cores, %s; engine %s", sys["os"], sys["cores"], sys["cpu"], sys["engine"])
	if gpus, _ := sys["gpus"].([]any); len(gpus) == 0 && runtime.GOOS == "windows" {
		t.Error("system: no GPUs (PowerShell did not run?)")
	}
	t.Logf("benchmark: %v", bench)
	if _, err := e.Call(ctx, "nope", nil); err == nil {
		t.Error("an unknown handler: no error")
	}
}
