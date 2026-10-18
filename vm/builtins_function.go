package vm

import (
	"strings"
	"unicode"

	"github.com/baslentfert/jsc2go/bytecode"
	"github.com/baslentfert/jsc2go/bytecompiler"
	"github.com/baslentfert/jsc2go/parser"
)

// Function constructor and Function.prototype (FunctionConstructor.cpp,
// FunctionPrototype.cpp).

func (r *Realm) initFunction() {
	vm := r.vm
	proto := r.FunctionPrototype
	proto.putDirect(StrKey("length"), Int(0), Configurable)
	proto.putDirect(StrKey("name"), StringValue(emptyString), Configurable)

	c := r.constructor("Function", 1, func(c *Call) Value {
		return ObjectValue(vm.createDynamicFunction(c, dynamicNormal))
	}, proto)
	r.FunctionConstructor = c

	r.method(proto, "apply", 2, func(c *Call) Value {
		if !c.This.IsCallable() {
			vm.throwTypeError("Function.prototype.apply was called on %s, which is not a function", vm.describeForError(c.This))
		}
		args := c.Arg(1)
		if args.IsNullish() {
			return vm.Call(c.This, c.Arg(0))
		}
		return vm.Call(c.This, c.Arg(0), vm.CreateListFromArrayLike(args, false)...)
	})
	r.method(proto, "bind", 1, func(c *Call) Value {
		target := c.This.AsObject()
		if target == nil || !target.IsCallable() {
			vm.throwTypeError("Cannot bind non-function")
		}
		var boundArgs []Value
		if len(c.Args) > 1 {
			boundArgs = copyArgs(c.Args[1:])
		}
		f := c.Realm().newObject(target.GetPrototypeOf())
		f.class = ClassFunction
		f.fn = &boundFunction{target: target, boundThis: c.Arg(0), boundArgs: boundArgs}
		length := 0.0
		if target.HasOwnProperty(StrKey("length")) {
			if l := target.GetStr("length"); l.IsNumber() {
				switch n := l.Float(); {
				case n == posInf:
					length = posInf
				case n == negInf:
					length = 0
				default:
					length = max(0, integerOrInfinity(n)-float64(len(boundArgs)))
				}
			}
		}
		f.putDirect(StrKey("length"), Number(length), Configurable)
		name := target.GetStr("name")
		nameStr := emptyString
		if name.IsString() {
			nameStr = name.StringRef()
		}
		f.putDirect(StrKey("name"), StringValue(Concat(NewString("bound "), nameStr)), Configurable)
		return ObjectValue(f)
	})
	r.method(proto, "call", 1, func(c *Call) Value {
		if !c.This.IsCallable() {
			vm.throwTypeError("Function.prototype.call was called on %s, which is not a function", vm.describeForError(c.This))
		}
		var args []Value
		if len(c.Args) > 1 {
			args = c.Args[1:]
		}
		return vm.Call(c.This, c.Arg(0), args...)
	})
	r.method(proto, "toString", 0, func(c *Call) Value {
		o := c.This.AsObject()
		if o == nil || !o.IsCallable() {
			vm.throwTypeError("Function.prototype.toString requires that 'this' be a Function")
		}
		return StringValue(vm.functionSource(o))
	})
	r.symbolMethod(proto, SymbolHasInstance, 1, func(c *Call) Value {
		return Bool(vm.OrdinaryHasInstance(c.This, c.Arg(0)))
	}, 0)

	// %ThrowTypeError% and the poisoned caller/arguments accessors.
	thrower := r.newNativeFunction("", 0, func(c *Call) Value {
		vm.throwTypeError("'arguments', 'callee', and 'caller' cannot be accessed in this context.")
		return Undefined
	})
	thrower.putDirect(StrKey("length"), Int(0), 0)
	thrower.putDirect(StrKey("name"), StringValue(emptyString), 0)
	thrower.PreventExtensions()
	r.ThrowTypeError = thrower
	// Like JSC, caller and arguments are the legacy reflection accessors
	// (es-legacy-function-reflection) rather than %ThrowTypeError%.
	r.accessor(proto, StrKey("caller"), func(c *Call) Value { return vm.legacyCaller(c.This) }, func(c *Call) Value {
		vm.legacyReflectionReceiver(c.This)
		return Undefined
	})
	r.accessor(proto, StrKey("arguments"), func(c *Call) Value { return vm.legacyArguments(c.This) }, func(c *Call) Value {
		vm.legacyReflectionReceiver(c.This)
		return Undefined
	})
}

// functionSource is Function.prototype.toString's result for f.
func (vm *VM) functionSource(f *Object) *String {
	if j, ok := f.fn.(*jsFunction); ok {
		exec := j.exec
		if exec.IsClass() {
			src := exec.ClassSource
			return NewStringUTF16(append([]uint16(nil), src.Provider.Range(src.StartOffset, src.EndOffset)...))
		}
		src := exec.Source
		if src.Provider != nil {
			start := exec.UnlinkedFunctionStart
			end := exec.UnlinkedFunctionEnd + 1
			if p := src.Provider.Source(); start >= 0 && end <= len(p) && start < end {
				if dyn, ok := f.internal.(*dynamicFunctionSource); ok {
					return dyn.text
				}
				return NewStringUTF16(append([]uint16(nil), p[start:end]...))
			}
		}
	}
	name := ""
	if n, ok := lookupDataProperty(f, StrKey("name")); ok && n.IsString() {
		name = n.StringRef().String()
	}
	return NewString("function " + nativeFunctionName(name) + "() {\n    [native code]\n}")
}

// nativeFunctionName keeps a name only where the NativeFunction syntax allows
// it: an optional get/set prefix followed by an IdentifierName or a computed
// name ("[Symbol.iterator]"). Names like "bound f" are dropped.
func nativeFunctionName(name string) string {
	prefix, rest := "", name
	for _, p := range []string{"get ", "set "} {
		if r, ok := strings.CutPrefix(name, p); ok {
			prefix, rest = p, r
		}
	}
	if rest == "" || strings.HasPrefix(rest, "[") && strings.HasSuffix(rest, "]") {
		return name
	}
	for i, r := range rest {
		if !(r == '$' || r == '_' || unicode.IsLetter(r) || i > 0 && (unicode.IsDigit(r) || unicode.In(r, unicode.Mn, unicode.Mc, unicode.Pc))) {
			return prefix
		}
	}
	return name
}

type dynamicKind uint8

const (
	dynamicNormal dynamicKind = iota
	dynamicGenerator
	dynamicAsync
	dynamicAsyncGenerator
)

// dynamicFunctionSource remembers the synthesized source text of a
// function created by the Function constructor.
type dynamicFunctionSource struct{ text *String }

// createDynamicFunction is CreateDynamicFunction (20.2.1.1.1).
func (vm *VM) createDynamicFunction(c *Call, kind dynamicKind) *Object {
	var params, body string
	if n := len(c.Args); n > 0 {
		parts := make([]string, n-1)
		for i := 0; i < n-1; i++ {
			parts[i] = vm.toGoString(c.Args[i])
		}
		params = strings.Join(parts, ",")
		body = vm.toGoString(c.Args[n-1])
	}
	prefix := map[dynamicKind]string{dynamicNormal: "function", dynamicGenerator: "function*",
		dynamicAsync: "async function", dynamicAsyncGenerator: "async function*"}[kind]
	text := prefix + " anonymous(" + params + "\n) {\n" + body + "\n}"

	// Parse the parameters and the body on their own first so that neither
	// can close the other early ("/*" in the parameters, "}){" in the body).
	check := func(src string) {
		s := parser.NewSourceCode(parser.NewSourceProvider("("+src+")", "", parser.SourceTypeProgram))
		if _, err := bytecompiler.CompileProgram(s, parser.NoLexicallyScopedFeatures, 0); err != nil {
			vm.throwSyntaxError("%s", err.Message)
		}
	}
	check(prefix + " anonymous(" + params + "\n) {\n}")
	check(prefix + " anonymous(\n) {\n" + body + "\n}")

	src := parser.NewSourceCode(parser.NewSourceProvider("("+text+")", "", parser.SourceTypeProgram))
	src.Provider.Origin = vm.activeOrigin()
	ucb, err := bytecompiler.CompileProgram(src, parser.NoLexicallyScopedFeatures, 0)
	if err != nil {
		vm.throwSyntaxError("%s", err.Message)
	}
	if len(ucb.FunctionExprs) != 1 {
		vm.throwSyntaxError("Invalid function source")
	}
	exec := ucb.FunctionExprs[0]
	// Like JSC's fromGlobalCode, the function is not a named function
	// expression: "anonymous" is not bound inside it.
	exec.FunctionMode = parser.FunctionModeFunctionDeclaration
	realm := vm.functionRealm(c.Callee)
	f := realm.newJSFunction(exec, realm.globalLexical)
	f.internal = &dynamicFunctionSource{NewString(text)}

	newTarget := c.NewTarget
	if newTarget == nil {
		newTarget = c.Callee
	}
	fallback := map[dynamicKind]func(*Realm) *Object{
		dynamicNormal:         func(r *Realm) *Object { return r.FunctionPrototype },
		dynamicGenerator:      func(r *Realm) *Object { return r.GeneratorFunctionPrototype },
		dynamicAsync:          func(r *Realm) *Object { return r.AsyncFunctionPrototype },
		dynamicAsyncGenerator: func(r *Realm) *Object { return r.AsyncGeneratorFunctionPrototype },
	}[kind]
	f.proto = vm.GetPrototypeFromConstructor(newTarget, fallback)
	_ = bytecode.CanConstruct
	return f
}

// legacyReflectionReceiver is IsAllowedReceiverFunctionForCallerAndArguments:
// a sloppy ordinary function (not an arrow, method, generator, async
// function or class constructor).
func (vm *VM) legacyReflectionReceiver(this Value) *Object {
	if o := this.AsObject(); o != nil {
		if j, ok := o.fn.(*jsFunction); ok && !j.exec.IsInStrictContext() &&
			j.exec.ParseMode == parser.NormalFunctionMode && !j.exec.IsClassConstructorFunction() {
			return o
		}
	}
	vm.throwTypeError("'arguments', 'callee', and 'caller' cannot be accessed in this context.")
	return nil
}

// legacyArguments is get Function.prototype.arguments: a fresh arguments
// object for the innermost active call of the function, or null.
func (vm *VM) legacyArguments(this Value) Value {
	o := vm.legacyReflectionReceiver(this)
	for f := vm.currentFrame; f != nil; f = f.caller {
		if f.callee == o {
			return ObjectValue(vm.newArgumentsObject(f, false))
		}
	}
	return Null
}

// legacyCaller is get Function.prototype.caller: the function that called
// the innermost active call of the function, or null when that is not a
// sloppy ordinary function.
func (vm *VM) legacyCaller(this Value) Value {
	o := vm.legacyReflectionReceiver(this)
	f := vm.currentFrame
	for f != nil && f.callee != o {
		f = f.caller
	}
	if f == nil {
		return Null
	}
	for f = f.caller; f != nil; f = f.caller {
		if f.callee == nil {
			return Null // global or eval code
		}
		j, ok := f.callee.fn.(*jsFunction)
		if !ok {
			continue // bound functions and proxies are skipped
		}
		mode := j.exec.ParseMode
		if j.exec.IsInStrictContext() || mode.IsGenerator() || mode.IsAsyncFunction() || mode.IsAsyncGenerator() {
			return Null
		}
		return ObjectValue(f.callee)
	}
	return Null
}
