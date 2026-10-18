package jsc2go

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTimers(t *testing.T) {
	rt := New()
	var out bytes.Buffer
	rt.SetConsole(&out, &out)
	run(t, rt, `
		setTimeout(() => console.log("timeout 20"), 20);
		setTimeout((a, b) => console.log("timeout 5", a, b), 5, "x", 2);
		var n = 0, id = setInterval(() => { console.log("tick", ++n); if (n == 3) clearInterval(id); }, 1);
		var never = setTimeout(() => console.log("cleared"), 10); clearTimeout(never);
		queueMicrotask(() => console.log("microtask"));
		console.log("script");`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rt.RunPending(ctx); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"script\nmicrotask\n", "tick 3\n", "timeout 5 x 2\n", "timeout 20\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "cleared") || strings.Contains(got, "tick 4") {
		t.Fatalf("a cleared timer ran:\n%s", got)
	}
	if strings.Index(got, "timeout 5") > strings.Index(got, "timeout 20") {
		t.Fatalf("timers out of order:\n%s", got)
	}
}

// TestInvoke drives a Runtime from other goroutines while its loop runs,
// as a UI does: synchronous and async handlers, rejections, Do, Post.
func TestInvoke(t *testing.T) {
	rt := New()
	var errs []error
	var mu sync.Mutex
	rt.SetErrorHandler(func(err error) { mu.Lock(); errs = append(errs, err); mu.Unlock() })
	rt.Set("goDo", func() error { return rt.Do(context.Background(), func(*Runtime) error { return nil }) })
	run(t, rt, `
		var calls = 0;
		function sum(a, b) { calls++; return a + b; }
		async function slow(x) { await new Promise(r => setTimeout(r, 20)); return {doubled: x * 2, list: [x]}; }
		async function fails() { throw new RangeError("nope"); }
		function viaDo() { return goDo(); }
		setTimeout(() => { throw new Error("uncaught in a timer"); }, 1);`)
	ctx, cancel := context.WithCancel(context.Background())
	loopDone := make(chan error, 1)
	go func() { loopDone <- rt.RunLoop(ctx) }()

	bg := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v, err := rt.Invoke(bg, "sum", i, 1)
			if err != nil || v != int64(i+1) {
				t.Errorf("sum(%d, 1) = %v, %v", i, v, err)
			}
		}(i)
	}
	wg.Wait()
	v, err := rt.Invoke(bg, "slow", 21)
	if m, ok := v.(map[string]any); err != nil || !ok || m["doubled"] != int64(42) {
		t.Fatalf("slow: %v %v", v, err)
	}
	if _, err := rt.Invoke(bg, "fails"); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("fails: %v", err)
	}
	if _, err := rt.Invoke(bg, "missing"); err == nil {
		t.Fatal("missing function: no error")
	}
	if _, err := rt.Invoke(bg, "viaDo"); err == nil || !strings.Contains(err.Error(), "own goroutine") {
		t.Fatalf("Do from the loop goroutine: %v", err)
	}
	var calls int
	if err := rt.Do(bg, func(rt *Runtime) error {
		calls = int(rt.Get("calls").ToInteger())
		return nil
	}); err != nil || calls != 20 {
		t.Fatalf("Do: %v %v", calls, err)
	}
	posted := make(chan string, 1)
	rt.Post(func(rt *Runtime) { posted <- rt.Get("calls").String() })
	if got := <-posted; got != "20" {
		t.Fatalf("Post: %q", got)
	}
	short, cancelShort := context.WithTimeout(bg, 10*time.Millisecond)
	defer cancelShort()
	run2 := func() { rt.Invoke(bg, "eval", "var forever = new Promise(() => {})") }
	run2()
	if _, err := rt.Invoke(short, "eval", "forever"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Invoke with a deadline: %v", err)
	}
	cancel()
	if err := <-loopDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("RunLoop: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "uncaught in a timer") {
		t.Fatalf("error handler got %v", errs)
	}
}

// TestLoopInterrupt stops a loop whose timer runs forever.
func TestLoopInterrupt(t *testing.T) {
	rt := New()
	run(t, rt, `setTimeout(() => { for (;;) {} }, 1)`)
	time.AfterFunc(100*time.Millisecond, rt.Interrupt)
	if err := rt.RunLoop(context.Background()); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("got %v", err)
	}
}

func TestModules(t *testing.T) {
	rt := New()
	var out bytes.Buffer
	rt.SetConsole(&out, &out)
	err := rt.RegisterModule("app:math", map[string]any{
		"add": func(a, b float64) float64 { return a + b },
		"pi":  3.14,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.RegisterModule("app:bad", map[string]any{"not-ident": 1}); err == nil {
		t.Fatal("a bad export name was accepted")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "lib.mjs"), []byte(`export const twice = x => x * 2;`), 0o644)
	main := filepath.Join(dir, "main.mjs")
	src := `
		import { add, pi } from "app:math";
		import math from "app:math";
		import { twice } from "./lib.mjs";
		await new Promise(r => setTimeout(r, 5));
		console.log(add(1, 2), math.pi === pi, twice(21));`
	if err := rt.RunModule(main, src); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rt.RunPending(ctx); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "3 true 42\n" {
		t.Fatalf("got %q", got)
	}
	if err := rt.RunModule(filepath.Join(dir, "throws.mjs"), `throw new Error("module failed")`); err == nil || !strings.Contains(err.Error(), "module failed") {
		t.Fatalf("throwing module: %v", err)
	}
	if err := rt.RunModule(filepath.Join(dir, "missing.mjs"), `import "./nope.mjs"`); err == nil {
		t.Fatal("missing import: no error")
	}
}
