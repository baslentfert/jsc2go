package jsc2go

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/baslentfert/jsc2go/vm"
)

// SetConsole sends console.log, info and debug to stdout and console.warn
// and error to stderr (os.Stdout and os.Stderr by default; print, the jsc
// shell's, writes to stdout too).
func (rt *Runtime) SetConsole(stdout, stderr io.Writer) {
	rt.stdout, rt.stderr = stdout, stderr
}

// installConsole defines console with log, info, debug, warn and error,
// which format their arguments as console.log does in browsers and Node
// (strings as they are, other values inspected, separated by spaces).
func (rt *Runtime) installConsole() {
	r := rt.vm.Realm()
	c := r.NewObject()
	for _, m := range []struct {
		name string
		err  bool
	}{{"log", false}, {"info", false}, {"debug", false}, {"warn", true}, {"error", true}} {
		toErr := m.err
		r.DefineFunction(c, m.name, 0, func(call *vm.Call) vm.Value {
			parts := make([]string, len(call.Args))
			for i, a := range call.Args {
				if a.IsString() {
					parts[i] = a.StringRef().GoString()
				} else {
					parts[i] = inspect(call.VM, a, 0)
				}
			}
			w := rt.stdout
			if toErr {
				w = rt.stderr
			}
			fmt.Fprintln(w, strings.Join(parts, " "))
			return vm.Undefined
		})
	}
	r.GlobalObject.CreateDataPropertyOrThrow(vm.StrKey("console"), vm.ObjectValue(c))
}

// inspect formats v for the console: strings quoted inside objects,
// arrays and plain objects two levels deep, functions by name, errors by
// their stack or message.
func inspect(v *vm.VM, x vm.Value, depth int) string {
	switch {
	case x.IsString():
		if depth == 0 {
			return x.StringRef().GoString()
		}
		return strconv.Quote(x.StringRef().GoString())
	case !x.IsObject():
		return v.ToString(x).GoString()
	}
	o := x.AsObject()
	if x.IsCallable() {
		name := o.GetStr("name")
		if name.IsString() && name.StringRef().GoString() != "" {
			return "[Function: " + name.StringRef().GoString() + "]"
		}
		return "[Function (anonymous)]"
	}
	if o.Class() == vm.ClassError {
		if s := o.GetStr("stack"); s.IsString() {
			return s.StringRef().GoString()
		}
		return v.ToString(x).GoString()
	}
	if depth > 2 {
		if o.Class() == vm.ClassArray {
			return "[Array]"
		}
		return "[Object]"
	}
	if o.Class() == vm.ClassArray {
		list := v.CreateListFromArrayLike(x, false)
		parts := make([]string, len(list))
		for i, e := range list {
			parts[i] = inspect(v, e, depth+1)
		}
		return "[ " + strings.Join(parts, ", ") + " ]"
	}
	if o.Class() != vm.ClassObject {
		return v.ToString(x).GoString()
	}
	var parts []string
	keys := o.OwnPropertyKeys()
	names := make([]string, 0, len(keys))
	for _, k := range keys {
		if k.IsSymbol() {
			continue
		}
		if p, ok := o.GetOwnProperty(k); ok && p.Flags&vm.Enumerable != 0 {
			names = append(names, k.String())
		}
	}
	for _, n := range names {
		parts = append(parts, n+": "+inspect(v, o.GetStr(n), depth+1))
	}
	if len(parts) == 0 {
		return "{}"
	}
	return "{ " + strings.Join(parts, ", ") + " }"
}
