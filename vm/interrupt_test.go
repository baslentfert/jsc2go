package vm

import (
	"errors"
	"testing"
	"time"
)

// TestInterrupt stops scripts from another goroutine: a loop, recursion
// without loops (which only function calls check, also direct calls between
// generated code), a promise job, proper tail calls (which reuse the frame)
// and a regular expression that backtracks for minutes. Eval and RunJobs return
// ErrInterrupted, the VM stays interrupted until ClearInterrupt, and then
// runs code again with its state intact.
func TestInterrupt(t *testing.T) {
	cases := []struct{ name, src string }{
		{"loop", `for (;;) {}`},
		{"recursion", `function t(d) { return d == 0 ? 1 : t(d - 1) + t(d - 1); } t(60);`},
		{"job", `Promise.resolve().then(function spin() { for (;;) {} });`},
		{"tailcall", `"use strict"; function f(n) { return f(n + 1); } f(0);`},
		{"mutual-tail", `"use strict"; function a(n) { return b(n + 1); } function b(n) { return a(n + 1); } a(0);`},
		{"regex", `/^(a+)+$/.test("a".repeat(40) + "b");`},
	}
	for _, threshold := range []int{0, 1} {
		for _, c := range cases {
			v := New()
			if threshold > 0 {
				saved := jitThreshold
				jitThreshold = threshold
				defer func() { jitThreshold = saved }()
			}
			// Some state before, to see it intact after.
			if _, err := v.Eval(`var kept = [1, 2, 3]; function sum(a) { var s = 0; for (var i = 0; i < a.length; i++) s += a[i]; return s; }`, "setup.js"); err != nil {
				t.Fatal(err)
			}
			timer := time.AfterFunc(100*time.Millisecond, v.Interrupt)
			_, err := v.Eval(c.src, c.name+".js")
			if err == nil {
				err = v.RunJobs()
			}
			timer.Stop()
			if !errors.Is(err, ErrInterrupted) {
				t.Fatalf("%s (threshold %d): got %v, want ErrInterrupted", c.name, threshold, err)
			}
			// Still interrupted: the next run stops too.
			if _, err := v.Eval(`for (var i = 0; i < 1e9; i++) {}`, "again.js"); !errors.Is(err, ErrInterrupted) {
				t.Fatalf("%s: before ClearInterrupt got %v, want ErrInterrupted", c.name, err)
			}
			v.ClearInterrupt()
			got, err := v.Eval(`sum(kept) + (function f(n) { return n ? f(n - 1) + 1 : 0; })(50)`, "after.js")
			if err != nil {
				t.Fatalf("%s: after ClearInterrupt: %v", c.name, err)
			}
			if got.num() != 56 {
				t.Fatalf("%s: after ClearInterrupt got %v, want 56", c.name, got.num())
			}
			if err := v.RunJobs(); err != nil {
				t.Fatalf("%s: jobs after ClearInterrupt: %v", c.name, err)
			}
		}
	}
}
