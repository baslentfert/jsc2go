package jsc2go

// The event loop: timers, tasks posted from other goroutines, and calls
// into JavaScript from them.
//
// A Runtime belongs to one goroutine. RunLoop (or RunPending) makes that
// goroutine an event loop: it runs the timers scripts set and the tasks
// other goroutines post (Post, Do, Invoke), each followed by the promise
// jobs it queued. A desktop or server program runs RunLoop on its own
// goroutine and talks to it through Do and Invoke.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/baslentfert/jsc2go/vm"
)

// loop is a Runtime's event loop state.
type loop struct {
	mu     sync.Mutex
	tasks  []func()
	signal chan struct{} // a task was posted
	// timers, by id; touched only on the loop goroutine.
	timers map[int]*jsTimer
	nextID int
	// owner is the goroutine running the loop (0: none), for Do's check.
	owner atomic.Int64
	// OnError receives exceptions no JavaScript code caught in a timer or
	// task (they do not stop the loop).
	onError func(error)
}

type jsTimer struct {
	id       int
	fn       vm.Value
	args     []vm.Value
	interval time.Duration // 0: a timeout
	t        *time.Timer
}

// goid is the current goroutine's id (from its stack header), to catch Do
// called from the loop's own goroutine, which would wait for itself.
func goid() int64 {
	var buf [64]byte
	b := buf[:runtime.Stack(buf[:], false)]
	b = bytes.TrimPrefix(b, []byte("goroutine "))
	if i := bytes.IndexByte(b, ' '); i > 0 {
		b = b[:i]
	}
	n, _ := strconv.ParseInt(string(b), 10, 64)
	return n
}

// ErrLoopGoroutine is returned by Do and Invoke when called from the
// goroutine running the loop (where the code can use the Runtime
// directly; waiting for the loop there would never end).
var ErrLoopGoroutine = errors.New("jsc2go: Do or Invoke called from the event loop's own goroutine")

func (rt *Runtime) initLoop() {
	rt.loop = &loop{signal: make(chan struct{}, 1), timers: map[int]*jsTimer{}}
	rt.loop.onError = func(err error) { fmt.Fprintln(rt.stderr, err) }
	r := rt.vm.Realm()
	g := r.GlobalObject
	add := func(name string, length int, fn vm.NativeFunc) { r.DefineFunction(g, name, length, fn) }
	add("setTimeout", 2, func(c *vm.Call) vm.Value { return rt.setTimer(c, false) })
	add("setInterval", 2, func(c *vm.Call) vm.Value { return rt.setTimer(c, true) })
	clearFn := func(c *vm.Call) vm.Value {
		if id := c.Arg(0); id.IsNumber() {
			rt.clearTimer(int(id.Float()))
		}
		return vm.Undefined
	}
	add("clearTimeout", 1, clearFn)
	add("clearInterval", 1, clearFn)
	add("queueMicrotask", 1, func(c *vm.Call) vm.Value {
		fn := c.Arg(0)
		if !fn.IsCallable() {
			c.VM.Throw(vm.ObjectValue(c.VM.NewError(vm.ErrorKindTypeError, "queueMicrotask requires a function")))
		}
		c.VM.EnqueueJob(func() { c.VM.Call(fn, vm.Undefined) })
		return vm.Undefined
	})
}

// SetErrorHandler sets what receives exceptions no JavaScript code caught
// in timers and tasks (printed to the console's stderr by default).
func (rt *Runtime) SetErrorHandler(f func(error)) { rt.loop.onError = f }

func (rt *Runtime) setTimer(c *vm.Call, repeat bool) vm.Value {
	fn := c.Arg(0)
	if !fn.IsCallable() {
		c.VM.Throw(vm.ObjectValue(c.VM.NewError(vm.ErrorKindTypeError, "the timer callback is not a function")))
	}
	ms := 0.0
	if d := c.Arg(1); !d.IsUndefined() {
		ms = c.VM.ToNumber(d)
	}
	if ms != ms || ms < 0 {
		ms = 0
	}
	d := time.Duration(ms * float64(time.Millisecond))
	l := rt.loop
	l.nextID++
	t := &jsTimer{id: l.nextID, fn: fn}
	if len(c.Args) > 2 {
		t.args = append([]vm.Value(nil), c.Args[2:]...)
	}
	if repeat {
		t.interval = max(d, time.Millisecond)
	}
	l.timers[t.id] = t
	t.t = time.AfterFunc(d, func() { rt.post(func() { rt.fire(t) }) })
	return vm.Int(t.id)
}

func (rt *Runtime) clearTimer(id int) {
	if t, ok := rt.loop.timers[id]; ok {
		t.t.Stop()
		delete(rt.loop.timers, id)
	}
}

// fire runs timer t (on the loop goroutine), unless it was cleared.
func (rt *Runtime) fire(t *jsTimer) {
	if rt.loop.timers[t.id] != t {
		return
	}
	if t.interval > 0 {
		t.t.Reset(t.interval)
	} else {
		delete(rt.loop.timers, t.id)
	}
	rt.vm.Call(t.fn, vm.Undefined, t.args...)
}

// post queues f for the loop goroutine.
func (rt *Runtime) post(f func()) {
	l := rt.loop
	l.mu.Lock()
	l.tasks = append(l.tasks, f)
	l.mu.Unlock()
	select {
	case l.signal <- struct{}{}:
	default:
	}
}

// Post runs f on the loop goroutine, after the work queued before it. It
// may be called from any goroutine. An exception f throws goes to the
// error handler.
func (rt *Runtime) Post(f func(rt *Runtime)) { rt.post(func() { f(rt) }) }

// Do runs f on the loop goroutine and waits for it: the way other
// goroutines use the Runtime while RunLoop runs. It returns f's error, an
// *Exception f threw, ctx's error if ctx ends first (f still runs), or
// ErrLoopGoroutine if called from the loop goroutine.
func (rt *Runtime) Do(ctx context.Context, f func(rt *Runtime) error) error {
	if rt.loop.owner.Load() == goid() {
		return ErrLoopGoroutine
	}
	done := make(chan error, 1)
	rt.post(func() {
		var err error
		if exc := rt.vm.Try(func() { err = f(rt) }); exc != nil {
			err = rt.wrapErr(exc)
		}
		done <- err
	})
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Invoke calls JavaScript function fn (a Value, or the name of a global
// function) with args converted by ToValue, on the loop goroutine, and
// waits for the result exported to Go (Value.Export). If the function
// returns a promise, Invoke waits for it to settle; a rejection or an
// exception is returned as an error. It may be called from any goroutine
// but the loop's: the way a UI or a server hands calls to scripts.
func (rt *Runtime) Invoke(ctx context.Context, fn any, args ...any) (any, error) {
	if rt.loop.owner.Load() == goid() {
		return nil, ErrLoopGoroutine
	}
	type result struct {
		v   any
		err error
	}
	done := make(chan result, 2) // (a reply, then perhaps an exception)
	rt.post(func() {
		// An exception of the call (not of a later job) goes back to the
		// caller, not to the error handler.
		if exc := rt.vm.Try(func() { rt.invoke(fn, args, func(v any, err error) { done <- result{v, err} }) }); exc != nil {
			done <- result{err: rt.wrapErr(exc)}
		}
	})
	select {
	case r := <-done:
		return r.v, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// invoke is Invoke's work on the loop goroutine; reply gets the result,
// now or when the returned promise settles.
func (rt *Runtime) invoke(fn any, args []any, reply func(any, error)) {
	var f vm.Value
	switch x := fn.(type) {
	case string:
		f = rt.vm.Realm().GlobalObject.GetStr(x)
	case Value:
		f = x.raw()
	}
	if !f.IsCallable() {
		reply(nil, fmt.Errorf("jsc2go: Invoke: %v is not a function", fn))
		return
	}
	r := rt.vm.Realm()
	vals := make([]vm.Value, len(args))
	for i, a := range args {
		vals[i] = r.ToValue(a)
	}
	res := rt.vm.Call(f, vm.Undefined, vals...)
	then := thenOf(res)
	if !then.IsCallable() {
		reply(rt.vm.Export(res), nil)
		return
	}
	ok := r.NewFunction("", 1, func(c *vm.Call) vm.Value {
		reply(rt.vm.Export(c.Arg(0)), nil)
		return vm.Undefined
	})
	fail := r.NewFunction("", 1, func(c *vm.Call) vm.Value {
		reply(nil, rt.wrapErr(&vm.Exception{Value: c.Arg(0)}))
		return vm.Undefined
	})
	rt.vm.Call(then, res, vm.ObjectValue(ok), vm.ObjectValue(fail))
}

// thenOf is v's then method if v is an object (a promise or thenable).
func thenOf(v vm.Value) vm.Value {
	if o := v.AsObject(); o != nil {
		return o.GetStr("then")
	}
	return vm.Undefined
}

// RunLoop runs the event loop on the calling goroutine until ctx ends:
// timers, posted tasks, Do and Invoke calls, each followed by the promise
// jobs it queued. Uncaught exceptions go to the error handler. It returns
// ctx's error, or ErrInterrupted if Interrupt stopped a task.
func (rt *Runtime) RunLoop(ctx context.Context) error { return rt.runLoop(ctx, false) }

// RunPending runs the event loop until no timer is set and no task is
// queued (as a script's process ends in Node or Bun), or ctx ends.
func (rt *Runtime) RunPending(ctx context.Context) error { return rt.runLoop(ctx, true) }

func (rt *Runtime) runLoop(ctx context.Context, untilIdle bool) error {
	l := rt.loop
	l.owner.Store(goid())
	defer l.owner.Store(0)
	stop := context.AfterFunc(ctx, func() {
		select {
		case l.signal <- struct{}{}:
		default:
		}
	})
	defer stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		l.mu.Lock()
		tasks := l.tasks
		l.tasks = nil
		l.mu.Unlock()
		for _, task := range tasks {
			err := rt.vm.RunTask(task)
			if errors.Is(err, vm.ErrInterrupted) {
				return ErrInterrupted
			}
			if err != nil {
				l.onError(rt.wrapErr(err))
			}
		}
		if len(tasks) > 0 {
			continue
		}
		if untilIdle && len(l.timers) == 0 {
			return nil
		}
		<-l.signal
	}
}
