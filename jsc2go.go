// Package jsc2go runs JavaScript in Go programs: a port of WebKit's
// JavaScriptCore with a JIT for amd64 and arm64.
//
//	rt := jsc2go.New()
//	rt.Set("add", func(a, b float64) float64 { return a + b })
//	v, err := rt.RunString(`add(1, 2)`)
//	fmt.Println(v.Export()) // 3
//
// Go values passed in (Set, ToValue, function results) are copied into
// plain JavaScript values: structs, maps and slices become ordinary objects
// and arrays, which generated code reads as fast as any. Go functions are
// called with their arguments converted to their parameter types. Results
// come back with Value.Export or Runtime.ExportTo.
//
// A Runtime is not safe for concurrent use: use one per goroutine (they are
// independent), or hand work to the goroutine that owns it. Interrupt is
// the exception: it may be called from any goroutine.
package jsc2go

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"

	"github.com/baslentfert/jsc2go/vm"
)

// Version is jsc2go's version.
const Version = "0.1.0"

// Runtime is a JavaScript engine instance: a global object, its built-ins
// and everything scripts create.
type Runtime struct {
	vm *vm.VM
	// console output (SetConsole)
	stdout, stderr io.Writer
	loop           *loop
	// modules: Go modules' generated sources by specifier, and the loader
	// for the rest
	goModules map[string]string
	loader    ModuleLoader
}

// New makes a Runtime with the standard built-ins and a console.
func New() *Runtime {
	rt := &Runtime{vm: vm.New(), stdout: os.Stdout, stderr: os.Stderr, goModules: map[string]string{}, loader: FileLoader}
	rt.vm.Print = func(s string) { fmt.Fprintln(rt.stdout, s) }
	rt.vm.HostHooks.ToJS = rt.toJS
	rt.vm.HostHooks.FromJS = rt.fromJS
	rt.vm.HostHooks.LoadModule = rt.loadModule
	rt.installConsole()
	rt.initLoop()
	return rt
}

var (
	valueType = reflect.TypeOf(Value{})
	rawFnType = reflect.TypeOf((func(FunctionCall) Value)(nil))
)

// toJS converts this package's types for vm.Realm.ToValue, also inside
// structs, maps and slices: a Value as it is, a raw function
// (func(FunctionCall) Value) without conversions.
func (rt *Runtime) toJS(r *vm.Realm, x reflect.Value) (vm.Value, bool) {
	switch x.Type() {
	case valueType:
		v := x.Interface().(Value)
		if v.rt != nil && v.rt != rt && v.v.IsObject() {
			rt.vm.Throw(vm.ObjectValue(rt.vm.NewError(vm.ErrorKindTypeError, "jsc2go: an object of another Runtime")))
		}
		return v.raw(), true
	case rawFnType:
		if x.IsNil() {
			return vm.Null, true
		}
		fn := x.Interface().(func(FunctionCall) Value)
		return vm.ObjectValue(r.NewFunction("", 0, func(c *vm.Call) vm.Value {
			args := make([]Value, len(c.Args))
			for i, a := range c.Args {
				args[i] = Value{rt, a}
			}
			return fn(FunctionCall{Runtime: rt, This: Value{rt, c.This}, Arguments: args}).raw()
		})), true
	}
	return vm.Value{}, false
}

// fromJS converts to this package's types for vm.ExportTo and the
// parameters of Go functions: a Value as it is.
func (rt *Runtime) fromJS(v vm.Value, t reflect.Type) (reflect.Value, bool) {
	if t == valueType {
		return reflect.ValueOf(Value{rt, v}), true
	}
	return reflect.Value{}, false
}

// VM is the engine underneath, for what this package does not cover.
func (rt *Runtime) VM() *vm.VM { return rt.vm }

// ErrInterrupted is returned by the Run methods when Interrupt or a
// context stopped the code they ran.
var ErrInterrupted = vm.ErrInterrupted

// Exception is a JavaScript exception that reached Go: the thrown value and
// the stack where it was thrown.
type Exception struct {
	rt  *Runtime
	exc *vm.Exception
}

// Value is the thrown value.
func (e *Exception) Value() Value { return Value{e.rt, e.exc.Value} }

// Error is the exception's message, as an uncaught exception shows it.
func (e *Exception) Error() string { return e.exc.Error() }

// Stack is the stack trace of where it was thrown.
func (e *Exception) Stack() string { return e.exc.Stack }

// wrapErr turns the engine's errors into this package's.
func (rt *Runtime) wrapErr(err error) error {
	var exc *vm.Exception
	if errors.As(err, &exc) {
		return &Exception{rt, exc}
	}
	return err
}

// RunString runs a script.
func (rt *Runtime) RunString(src string) (Value, error) { return rt.RunScript("", src) }

// RunScript runs a script named name (for stack traces), then the promise
// jobs it queued (timers run in RunLoop or RunPending). It returns the
// script's completion value, or an *Exception, a syntax error (an
// *Exception holding a SyntaxError) or ErrInterrupted.
func (rt *Runtime) RunScript(name, src string) (Value, error) {
	p, err := rt.Compile(name, src)
	if err != nil {
		return Undefined(), err
	}
	return rt.RunProgram(p)
}

// Program is a compiled script.
type Program struct{ s *vm.Script }

// Compile compiles a script without running it.
func (rt *Runtime) Compile(name, src string) (*Program, error) {
	s, err := rt.vm.CompileScript(src, name)
	if err != nil {
		return nil, rt.wrapErr(err)
	}
	return &Program{s}, nil
}

// RunProgram runs a compiled script, then the promise jobs it queued.
func (rt *Runtime) RunProgram(p *Program) (Value, error) {
	v, err := rt.vm.RunScript(p.s)
	if err == nil {
		err = rt.vm.RunJobs()
	}
	if err != nil {
		return Undefined(), rt.wrapErr(err)
	}
	return Value{rt, v}, nil
}

// RunContext runs a script like RunScript, stopping it with
// ErrInterrupted when ctx is done (a deadline, a cancel). The Runtime
// can run code again afterwards.
func (rt *Runtime) RunContext(ctx context.Context, name, src string) (Value, error) {
	if err := ctx.Err(); err != nil {
		return Undefined(), err
	}
	stop := context.AfterFunc(ctx, rt.vm.Interrupt)
	v, err := rt.RunScript(name, src)
	if !stop() {
		// The context ended: the interrupt may have come after the
		// script; clear it either way.
		rt.vm.ClearInterrupt()
	}
	return v, err
}

// Interrupt stops the code running in rt, which then returns
// ErrInterrupted; code run later stops too, until ClearInterrupt. It may
// be called from any goroutine. Long operations of built-ins (a JSON of
// a huge object) finish first; loops, calls and regular expressions stop.
func (rt *Runtime) Interrupt() { rt.vm.Interrupt() }

// ClearInterrupt lets code run again after Interrupt.
func (rt *Runtime) ClearInterrupt() { rt.vm.ClearInterrupt() }

// Set sets a global variable to x converted with ToValue.
func (rt *Runtime) Set(name string, x any) error {
	var err error
	if exc := rt.vm.Try(func() {
		v := rt.vm.Realm().ToValue(x)
		rt.vm.Realm().GlobalObject.SetOrThrow(vm.StrKey(name), v)
	}); exc != nil {
		err = rt.wrapErr(exc)
	}
	return err
}

// Get returns a global variable (undefined if there is none).
func (rt *Runtime) Get(name string) Value {
	var v vm.Value
	rt.vm.Try(func() { v = rt.vm.Realm().GlobalObject.GetStr(name) })
	return Value{rt, v}
}

// ToValue converts a Go value to JavaScript (see the package doc):
// structs, maps and slices are copied into plain objects and arrays,
// functions become JavaScript functions, time.Time a Date, error an
// Error, []byte a Uint8Array.
func (rt *Runtime) ToValue(x any) Value {
	var v vm.Value
	if exc := rt.vm.Try(func() { v = rt.vm.Realm().ToValue(x) }); exc != nil {
		panic(rt.wrapErr(exc))
	}
	return Value{rt, v}
}

// ExportTo converts v into what target points to: a struct, map, slice,
// number, string, time.Time, func type or any. It returns an error when the
// value does not fit (a fraction into an int, a string into a struct).
func (rt *Runtime) ExportTo(v Value, target any) error {
	return rt.wrapErr(rt.vm.ExportTo(v.raw(), target))
}

// Callable is a JavaScript function as Go calls it.
type Callable func(this Value, args ...Value) (Value, error)

// AssertFunction returns v as a Callable if it is a function.
func AssertFunction(v Value) (Callable, bool) {
	if !v.v.IsCallable() || v.rt == nil {
		return nil, false
	}
	rt := v.rt
	return func(this Value, args ...Value) (Value, error) {
		vals := make([]vm.Value, len(args))
		for i, a := range args {
			vals[i] = a.raw()
		}
		var res vm.Value
		if exc := rt.vm.Try(func() { res = rt.vm.Call(v.v, this.raw(), vals...) }); exc != nil {
			return Undefined(), rt.wrapErr(exc)
		}
		if err := rt.vm.RunJobs(); err != nil {
			return Undefined(), rt.wrapErr(err)
		}
		return Value{rt, res}, nil
	}, true
}

// FunctionCall is how a raw Go function (func(FunctionCall) Value) sees
// a call: no conversions, no reflection.
type FunctionCall struct {
	Runtime   *Runtime
	This      Value
	Arguments []Value
}

// Argument returns argument i, or undefined.
func (c FunctionCall) Argument(i int) Value {
	if i < len(c.Arguments) {
		return c.Arguments[i]
	}
	return Undefined()
}

// Throw throws a JavaScript Error with message msg from a raw Go function.
func (c FunctionCall) Throw(msg string) {
	c.Runtime.vm.Throw(vm.ObjectValue(c.Runtime.vm.NewError(vm.ErrorKindError, msg)))
}
