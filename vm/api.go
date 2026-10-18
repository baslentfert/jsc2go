package vm

// Embedding API helpers.

import "time"

// DefineFunction installs a native method on o.
func (r *Realm) DefineFunction(o *Object, name string, length int, fn NativeFunc) *Object {
	return r.method(o, name, length, fn)
}

// Enter runs f with r as the current realm.
func (r *Realm) Enter(f func()) {
	vm := r.vm
	saved := vm.realm
	vm.realm = r
	defer func() { vm.realm = saved }()
	f()
}

// RunScript runs a compiled script in r; exceptions propagate as panics
// (use it from native functions, or wrap it in VM.Try).
func (r *Realm) RunScript(s *Script) Value { return r.vm.runScript(r, s) }

// DetachArrayBuffer is the host hook $262.detachArrayBuffer.
func (vm *VM) DetachArrayBuffer(v Value) Value {
	vm.detachArrayBuffer(v, Undefined)
	return Null
}

// SetTimeout schedules a call of callback after ms milliseconds as a host
// task (the jsc shell's setTimeout).
func (vm *VM) SetTimeout(callback Value, ms float64) {
	if !callback.IsCallable() {
		vm.throwTypeError("setTimeout requires a function")
	}
	vm.addTimer(time.Duration(max(ms, 0)*float64(time.Millisecond)), func() { vm.Call(callback, Undefined) })
}

// NewHTMLDDA creates an [[IsHTMLDDA]] object like document.all: typeof
// "undefined", falsy and == null; calling it returns null.
func (r *Realm) NewHTMLDDA() *Object {
	o := r.newNativeFunction("", 0, func(*Call) Value { return Null })
	o.htmlDDA = true
	return o
}
