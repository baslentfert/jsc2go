package vm

import (
	"math"

	"github.com/baslentfert/jsc2go/bytecompiler"
	"github.com/baslentfert/jsc2go/parser"
)

// ShadowRealm (proposal-shadowrealm): evaluation in a fresh realm whose
// objects never cross the boundary; functions are passed as wrapped
// function exotic objects and everything else must be a primitive.

func (r *Realm) initShadowRealm() {
	vm := r.vm
	proto := r.NewObject()
	r.ShadowRealmPrototype = proto
	r.constructor("ShadowRealm", 0, func(c *Call) Value {
		if c.NewTarget == nil {
			vm.throwTypeError("ShadowRealm constructor requires 'new'")
		}
		o := vm.OrdinaryCreateFromConstructor(c.NewTarget, func(r *Realm) *Object { return r.ShadowRealmPrototype })
		o.class = ClassShadowRealm
		o.internal = vm.NewRealm()
		return ObjectValue(o)
	}, proto)

	this := func(c *Call, name string) *Realm {
		if o := c.This.AsObject(); o != nil && o.class == ClassShadowRealm {
			return o.internal.(*Realm)
		}
		vm.throwTypeError("ShadowRealm.prototype.%s requires that |this| be a ShadowRealm object", name)
		return nil
	}
	r.method(proto, "evaluate", 1, func(c *Call) Value {
		evalRealm := this(c, "evaluate")
		src := c.Arg(0)
		if !src.IsString() {
			vm.throwTypeError("ShadowRealm.prototype.evaluate requires that the source be a string")
		}
		return vm.performShadowRealmEval(src.StringRef(), c.Realm(), evalRealm)
	})
	r.method(proto, "importValue", 2, func(c *Call) Value {
		evalRealm := this(c, "importValue")
		specifier := vm.ToString(c.Arg(0))
		exportName := c.Arg(1)
		if !exportName.IsString() {
			vm.throwTypeError("ShadowRealm.prototype.importValue requires that the export name be a string")
		}
		return vm.shadowRealmImportValue(specifier, exportName.StringRef(), c.Realm(), evalRealm)
	})
	r.toStringTag(proto, "ShadowRealm")
}

// performShadowRealmEval is PerformShadowRealmEval: syntax errors are
// reported as such, any other abrupt completion becomes a TypeError of
// the caller's realm.
func (vm *VM) performShadowRealmEval(src *String, callerRealm, evalRealm *Realm) Value {
	provider := parser.NewSourceProviderUTF16(append([]uint16(nil), src.UTF16()...), "eval", parser.SourceTypeProgram)
	if _, err := bytecompiler.CompileEval(parser.NewSourceCode(provider), bytecompiler.EvalConfig{}, 0); err != nil {
		vm.throwSyntaxError("%s", err.Message)
	}
	var result Value
	if exc := vm.Try(func() { result = vm.indirectEval(evalRealm, StringValue(src)) }); exc != nil {
		vm.throwShadowRealmTypeError(callerRealm, "ShadowRealm.prototype.evaluate: ", exc)
	}
	return vm.getWrappedValue(callerRealm, result)
}

// throwShadowRealmTypeError throws a TypeError of realm r describing exc.
func (vm *VM) throwShadowRealmTypeError(r *Realm, prefix string, exc *Exception) {
	saved := vm.realm
	vm.realm = r
	defer func() { vm.realm = saved }()
	msg := prefix + "the call threw an error"
	if exc != nil {
		var s string
		if vm.Try(func() { s = vm.toGoString(exc.Value) }) == nil {
			msg = prefix + s
		}
	}
	vm.throwTypeError("%s", msg)
}

// getWrappedValue is GetWrappedValue(callerRealm, value).
func (vm *VM) getWrappedValue(callerRealm *Realm, v Value) Value {
	o := v.AsObject()
	if o == nil {
		return v
	}
	if !o.IsCallable() {
		// The error belongs to the running (caller) realm, not to callerRealm.
		vm.throwTypeError("value passing between realms must be callable or primitive")
	}
	return ObjectValue(vm.wrappedFunctionCreate(callerRealm, o))
}

// wrappedFunction is a Wrapped Function Exotic Object.
type wrappedFunction struct{ target *Object }

func (w *wrappedFunction) isConstructor() bool { return false }

func (w *wrappedFunction) construct(f *Object, args []Value, newTarget *Object) Value {
	f.vm().throwTypeError("wrapped function is not a constructor")
	return Undefined
}

func (w *wrappedFunction) call(f *Object, this Value, args []Value) Value {
	vm := f.vm()
	vm.enterCall()
	defer vm.leaveCall()
	callerRealm := f.realm
	saved := vm.realm
	vm.realm = callerRealm
	defer func() { vm.realm = saved }()

	targetRealm := vm.functionRealm(w.target)
	wrappedArgs := make([]Value, len(args))
	for i, a := range args {
		wrappedArgs[i] = vm.getWrappedValue(targetRealm, a)
	}
	wrappedThis := vm.getWrappedValue(targetRealm, this)
	var result Value
	if exc := vm.Try(func() { result = vm.Call(ObjectValue(w.target), wrappedThis, wrappedArgs...) }); exc != nil {
		vm.throwShadowRealmTypeError(callerRealm, "", exc)
	}
	return vm.getWrappedValue(callerRealm, result)
}

// wrappedFunctionCreate is WrappedFunctionCreate, including CopyNameAndLength.
func (vm *VM) wrappedFunctionCreate(callerRealm *Realm, target *Object) *Object {
	f := callerRealm.newObject(callerRealm.FunctionPrototype)
	f.class = ClassFunction
	f.fn = &wrappedFunction{target: target}
	exc := vm.Try(func() {
		length := 0.0
		if target.HasOwnProperty(StrKey("length")) {
			if l := target.GetStr("length"); l.IsNumber() {
				switch n := l.Float(); {
				case math.IsInf(n, 1):
					length = n
				case math.IsInf(n, -1):
					length = 0
				default:
					length = max(vm.ToIntegerOrInfinity(l), 0)
				}
			}
		}
		f.putDirect(StrKey("length"), Number(length), Configurable)
		name := target.GetStr("name")
		if !name.IsString() {
			name = Str("")
		}
		f.putDirect(StrKey("name"), name, Configurable)
	})
	if exc != nil {
		vm.throwShadowRealmTypeError(callerRealm, "", exc)
	}
	return f
}

// shadowRealmImportValue is ShadowRealmImportValue: a dynamic import in the
// shadow realm whose export is wrapped for the caller; any rejection becomes
// a TypeError of the caller realm (%ThrowTypeError%).
func (vm *VM) shadowRealmImportValue(specifier, exportName *String, callerRealm, evalRealm *Realm) Value {
	inner := evalRealm.newPromise()
	vm.continueDynamicImport(evalRealm, vm.activeScriptOrModule(), parser.ModuleRequest{Specifier: specifier.String()}, inner)
	result := callerRealm.newPromise()
	name := exportName.Key()
	vm.promiseThenNative(inner, func(v Value, rejected bool) {
		saved := vm.realm
		vm.realm = callerRealm
		defer func() { vm.realm = saved }()
		if exc := vm.Try(func() {
			if rejected {
				vm.throwTypeError("ShadowRealm.prototype.importValue: the module could not be imported")
			}
			exports := v.ObjectRef()
			if !exports.HasOwnProperty(name) {
				vm.throwTypeError("ShadowRealm.prototype.importValue: '%s' is not exported", exportName.String())
			}
			vm.resolvePromise(result, vm.getWrappedValue(callerRealm, exports.GetKey(name)))
		}); exc != nil {
			vm.rejectPromise(result, exc.Value)
		}
	})
	return ObjectValue(result)
}
