package jsc2go

// ES modules: running them, loading them from files or elsewhere, and
// modules made of Go values (as Bun's bun:sqlite or Node's node:fs).

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/baslentfert/jsc2go/vm"
)

// modulesKey is the hidden global the generated source of Go modules reads
// its exports from (not enumerable, writable or configurable).
const modulesKey = "__jsc2go_modules__"

// ModuleLoader returns the source of the module named by specifier,
// imported from referrer (a module name, "" for the main module), and the
// module's canonical name (a resolved path, say), which identifies it.
type ModuleLoader func(specifier, referrer string) (source, name string, err error)

// SetModuleLoader sets how imports that are not Go modules are loaded. By
// default they are files, relative to the importing module's file.
func (rt *Runtime) SetModuleLoader(l ModuleLoader) { rt.loader = l }

// FileLoader loads modules from files: specifier relative to the directory
// of referrer.
func FileLoader(specifier, referrer string) (string, string, error) {
	name := specifier
	if !filepath.IsAbs(name) {
		name = filepath.Join(filepath.Dir(referrer), specifier)
	}
	name = filepath.Clean(name)
	b, err := os.ReadFile(name)
	return string(b), name, err
}

var identRe = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)

// RegisterModule makes Go values importable from JavaScript under
// specifier ("app:db", say): each entry of exports, converted with
// ToValue, is a named export, and the default export is an object holding
// them all:
//
//	rt.RegisterModule("app:math", map[string]any{"add": func(a, b float64) float64 { return a + b }})
//	// import { add } from "app:math"; import m from "app:math"
//
// Export names must be identifiers.
func (rt *Runtime) RegisterModule(specifier string, exports map[string]any) error {
	names := make([]string, 0, len(exports))
	for name := range exports {
		if !identRe.MatchString(name) || name == "default" {
			return fmt.Errorf("jsc2go: RegisterModule %q: export name %q is not an identifier", specifier, name)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	var err error
	if exc := rt.vm.Try(func() {
		r := rt.vm.Realm()
		obj := r.NewObject()
		for _, name := range names {
			obj.CreateDataPropertyOrThrow(vm.StrKey(name), r.ToValue(exports[name]))
		}
		rt.modulesObject().CreateDataPropertyOrThrow(vm.StrKey(specifier), vm.ObjectValue(obj))
	}); exc != nil {
		err = rt.wrapErr(exc)
	}
	if err == nil {
		var src strings.Builder
		fmt.Fprintf(&src, "const m = globalThis.%s[%q];\nexport default m;\n", modulesKey, specifier)
		for _, name := range names {
			fmt.Fprintf(&src, "export const %s = m.%s;\n", name, name)
		}
		rt.goModules[specifier] = src.String()
	}
	return err
}

// modulesObject is the hidden global holding Go modules' exports.
func (rt *Runtime) modulesObject() *vm.Object {
	g := rt.vm.Realm().GlobalObject
	if p, ok := g.GetOwnProperty(vm.StrKey(modulesKey)); ok {
		return p.Value.AsObject()
	}
	o := rt.vm.Realm().NewObject()
	g.DefineOwnProperty(vm.StrKey(modulesKey), vm.PropertyDescriptor{Value: vm.ObjectValue(o), Has: vm.HasValue | vm.HasWritable | vm.HasEnumerable | vm.HasConfigurable})
	return o
}

// loadModule is the engine's module loading hook: Go modules first, then
// the loader.
func (rt *Runtime) loadModule(specifier, referrer string) (string, string, error) {
	if src, ok := rt.goModules[specifier]; ok {
		return src, specifier, nil
	}
	if rt.loader == nil {
		return "", "", errors.New("no module loader")
	}
	return rt.loader(specifier, referrer)
}

// RunModule runs src as the ES module named name (its imports are relative
// to it), then the promise jobs it queued. It returns an error if the
// module threw. A module whose top-level await waits for a timer or a
// task finishes in RunLoop or RunPending.
func (rt *Runtime) RunModule(name, src string) error {
	p, err := rt.vm.RunModule(src, name)
	if err == nil {
		err = rt.vm.RunJobs()
	}
	if err != nil {
		return rt.wrapErr(err)
	}
	if settled, rejected, v := vm.PromiseResult(p); settled && rejected {
		return rt.wrapErr(&vm.Exception{Value: v})
	}
	return nil
}

// RunModuleFile runs the module in file path.
func (rt *Runtime) RunModuleFile(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return rt.RunModule(filepath.Clean(path), string(b))
}
