package vm

import (
	"weak"

	"github.com/baslentfert/jsc2go/bytecode"
	"github.com/baslentfert/jsc2go/bytecompiler"
	"github.com/baslentfert/jsc2go/ident"
	"github.com/baslentfert/jsc2go/parser"
)

// Realm is a realm with its global object and intrinsics (JSGlobalObject).
type Realm struct {
	vm *VM

	GlobalObject  *Object
	GlobalThis    *Object
	globalScope   *Scope // the global object as a scope
	globalLexical *Scope // let/const/class bindings of scripts
	varNames      map[ident.Identifier]struct{}

	linked            map[*bytecode.UnlinkedCodeBlock]*CodeBlock
	templates         map[templateKey]weak.Pointer[Object]
	templatesPurged   int // len(templates) after dead entries were last removed
	linkTimeConstants map[bytecode.LinkTimeConstant]Value
	// argumentsShapes are the shapes of mapped and unmapped arguments objects.
	argumentsShapes [2]argumentsShape
	// stringObjectShape is the shape of String objects made with the
	// realm's String.prototype; stringToPrimitive caches the check of that
	// prototype chain (plainStringObject).
	stringObjectShape *shape
	stringToPrimitive stringToPrimitive

	regExpStatics regExpStatics
	modules       map[moduleKey]*moduleRecord
	// globalLexicalGen changes whenever globalLexical gains bindings, which
	// invalidates cached resolve_scope results.
	globalLexicalGen uint64

	ObjectPrototype   *Object
	FunctionPrototype *Object
	ArrayPrototype    *Object
	StringPrototype   *Object
	NumberPrototype   *Object
	BooleanPrototype  *Object
	SymbolPrototype   *Object
	BigIntPrototype   *Object
	ErrorPrototypes   [numberOfErrorKinds]*Object
	ErrorConstructors [numberOfErrorKinds]*Object

	IteratorPrototype               *Object
	AsyncIteratorPrototype          *Object
	ArrayIteratorPrototype          *Object
	StringIteratorPrototype         *Object
	MapIteratorPrototype            *Object
	SetIteratorPrototype            *Object
	RegExpStringIteratorPrototype   *Object
	IteratorHelperPrototype         *Object
	WrapForValidIteratorPrototype   *Object
	AsyncFromSyncIteratorPrototype  *Object
	GeneratorPrototype              *Object
	GeneratorFunctionPrototype      *Object
	AsyncFunctionPrototype          *Object
	AsyncGeneratorPrototype         *Object
	AsyncGeneratorFunctionPrototype *Object
	PromisePrototype                *Object
	RegExpPrototype                 *Object
	DatePrototype                   *Object
	MapPrototype                    *Object
	SetPrototype                    *Object
	WeakMapPrototype                *Object
	WeakSetPrototype                *Object
	WeakRefPrototype                *Object
	FinalizationRegistryPrototype   *Object
	ProxyConstructor                *Object
	ArrayBufferPrototype            *Object
	SharedArrayBufferPrototype      *Object
	DataViewPrototype               *Object
	TypedArrayPrototype             *Object
	TypedArrayConstructor           *Object
	TypedArrayPrototypes            [numberOfTypedArrayKinds]*Object
	TypedArrayConstructors          [numberOfTypedArrayKinds]*Object
	DisposableStackPrototype        *Object
	AsyncDisposableStackPrototype   *Object
	ShadowRealmPrototype            *Object

	ObjectConstructor   *Object
	FunctionConstructor *Object
	ArrayConstructor    *Object
	// arraySpecies is Array[Symbol.species]'s original accessor, and
	// speciesSlots where Array.prototype.constructor and it are
	// (arraySpeciesIsDefault).
	arraySpecies       Value
	speciesSlots       [2]slotCache
	PromiseConstructor *Object
	RegExpConstructor  *Object

	evalFunction      *Object
	ThrowTypeError    *Object
	arrayProtoValues  *Object
	arrayIteratorNext *Object
	regExpPrimordials regExpPrimordials
	// typedArrayLength is %TypedArray%.prototype's length getter, which
	// get_length reads in place of calling (JSC's IntrinsicGetterAccessCase).
	typedArrayLength *Object
	// dataViewByteLength is DataView.prototype's byteLength getter, which
	// generated code computes itself (dataViewByteLength).
	dataViewByteLength *Object
	promiseResolve     *Object
	promiseThen        *Object
	asyncFromSyncNext  *Object
	emptyEnumerator    Value
	sentinelString     Value
	// fastArraySentinel is the iterator register of a for-of over an
	// array in fast mode (fastArrayIterable): an object without
	// properties or prototype, so that closing it finds no return.
	fastArraySentinel *Object
}

// NewRealm creates a realm with a fresh global object (CreateRealm and
// SetRealmGlobalObject).
func (vm *VM) NewRealm() *Realm {
	r := &Realm{
		vm:                vm,
		varNames:          make(map[ident.Identifier]struct{}),
		linked:            make(map[*bytecode.UnlinkedCodeBlock]*CodeBlock),
		templates:         make(map[templateKey]weak.Pointer[Object]),
		linkTimeConstants: make(map[bytecode.LinkTimeConstant]Value),
	}
	saved := vm.realm
	vm.realm = r
	vm.rootShape.tree.buildingRealms++
	defer func() {
		vm.rootShape.tree.buildingRealms--
		if saved != nil {
			vm.realm = saved
		}
	}()

	// The two fundamental prototypes must exist before any function.
	r.ObjectPrototype = &Object{realm: r, extensible: true, props: propertyMap{shape: vm.rootShape}}
	r.ObjectPrototype.exotic = immutablePrototypeObject{}
	r.FunctionPrototype = r.newObject(r.ObjectPrototype)
	r.FunctionPrototype.class = ClassFunction
	r.FunctionPrototype.fn = &nativeFunction{fn: func(c *Call) Value { return Undefined }}

	g := r.newObject(r.ObjectPrototype)
	g.class = ClassGlobal
	r.GlobalObject = g
	r.GlobalThis = g
	r.globalScope = &Scope{kind: scopeGlobalObject, object: g}
	r.globalLexical = &Scope{kind: scopeGlobalLexical, parent: r.globalScope, table: bytecode.NewSymbolTable()}
	r.globalLexical.table.ScopeType = bytecode.GlobalLexicalScope

	r.emptyEnumerator = cellValue(&propertyNameEnumerator{})
	r.sentinelString = StringValue(NewString("<sentinel>"))
	r.fastArraySentinel = r.newObject(nil)

	r.initObject()
	r.initFunction()
	r.initErrors()
	r.initSymbol()
	r.initIterators()
	r.initArray()
	r.initString()
	r.initNumber()
	r.initBoolean()
	r.initBigInt()
	r.initMath()
	r.initJSON()
	r.initReflect()
	r.initProxy()
	r.initPromise()
	r.initGenerators()
	r.initMapSet()
	r.initWeak()
	r.initRegExp()
	r.initDate()
	r.initTypedArrays()
	r.initGlobalFunctions()
	r.initDisposableStack()
	r.initShadowRealm()
	r.installIntrinsics()
	return r
}

// immutablePrototypeObject is %Object.prototype%, an immutable prototype
// exotic object (10.4.7).
type immutablePrototypeObject struct{ ordinaryObject }

func (immutablePrototypeObject) setPrototypeOf(o *Object, p *Object) bool { return p == o.proto }

// --- Builtin definition helpers ---------------------------------------------------------

// method installs a built-in method on o.
func (r *Realm) method(o *Object, name string, length int, fn NativeFunc) *Object {
	f := r.newNativeFunction(name, length, fn)
	o.putDirect(StrKey(name), ObjectValue(f), BuiltinFlags)
	return f
}

// symbolMethod installs a built-in method keyed by a symbol.
func (r *Realm) symbolMethod(o *Object, sym *Symbol, length int, fn NativeFunc, flags PropertyFlags) *Object {
	f := r.newNativeFunctionKey(SymKey(sym), length, fn, false)
	o.putDirect(SymKey(sym), ObjectValue(f), flags)
	return f
}

// getter installs a built-in accessor with only a getter.
func (r *Realm) getter(o *Object, k PropertyKey, fn NativeFunc) *Object {
	g := r.newNativeFunctionKey(k, 0, fn, false)
	g.putDirect(StrKey("name"), StringValue(functionNameForKey(k, "get")), Configurable)
	o.putDirectAccessor(k, g, nil, Configurable)
	return g
}

// accessor installs a built-in getter/setter pair.
func (r *Realm) accessor(o *Object, k PropertyKey, get, set NativeFunc) {
	g := r.newNativeFunctionKey(k, 0, get, false)
	g.putDirect(StrKey("name"), StringValue(functionNameForKey(k, "get")), Configurable)
	s := r.newNativeFunctionKey(k, 1, set, false)
	s.putDirect(StrKey("name"), StringValue(functionNameForKey(k, "set")), Configurable)
	o.putDirectAccessor(k, g, s, Configurable)
}

// constructor creates a built-in constructor whose prototype property is
// proto, links proto.constructor back and installs it on the global object.
func (r *Realm) constructor(name string, length int, fn NativeFunc, proto *Object) *Object {
	c := r.newNativeConstructor(name, length, fn)
	if proto != nil {
		c.putDirect(StrKey("prototype"), ObjectValue(proto), 0)
		proto.putDirect(StrKey("constructor"), ObjectValue(c), BuiltinFlags)
	}
	r.GlobalObject.putDirect(StrKey(name), ObjectValue(c), BuiltinFlags)
	return c
}

func (r *Realm) speciesGetter(c *Object) {
	r.getter(c, SymKey(SymbolSpecies), func(call *Call) Value { return call.This })
}

func (r *Realm) toStringTag(o *Object, tag string) {
	o.putDirect(SymKey(SymbolToStringTag), Str(tag), Configurable)
}

// --- Primitive wrappers --------------------------------------------------------------

// primitiveWrapper holds [[BooleanData]], [[NumberData]], [[StringData]],
// [[SymbolData]] or [[BigIntData]].
type primitiveWrapper struct{ value Value }

func (r *Realm) wrapPrimitive(v Value) *Object {
	var o *Object
	switch v.Kind() {
	case KindBool:
		o = r.newObject(r.BooleanPrototype)
		o.class = ClassBoolean
	case KindNumber:
		o = r.newObject(r.NumberPrototype)
		o.class = ClassNumber
	case KindString:
		return r.newStringObject(v.StringRef(), r.StringPrototype)
	case KindSymbol:
		o = r.newObject(r.SymbolPrototype)
		o.class = ClassSymbol
	case KindBigInt:
		o = r.newObject(r.BigIntPrototype)
		o.class = ClassBigInt
	default:
		panic("vm: wrapPrimitive of a non-primitive")
	}
	o.internal = &primitiveWrapper{v}
	return o
}

// thisPrimitive extracts the primitive of class cls from this (a primitive
// or a wrapper), for thisBooleanValue and friends.
func (vm *VM) thisPrimitive(this Value, kind Kind, cls Class, method string) Value {
	if this.Kind() == kind {
		return this
	}
	if o := this.AsObject(); o != nil && o.class == cls {
		if w, ok := o.internal.(*primitiveWrapper); ok {
			return w.value
		}
	}
	vm.throwTypeError("%s called on incompatible receiver", method)
	return Undefined
}

// --- Link-time constants ---------------------------------------------------------------

func (r *Realm) linkTimeConstant(c bytecode.LinkTimeConstant) Value {
	if v, ok := r.linkTimeConstants[c]; ok {
		return v
	}
	v := r.createLinkTimeConstant(c)
	r.linkTimeConstants[c] = v
	return v
}

func (r *Realm) createLinkTimeConstant(c bytecode.LinkTimeConstant) Value {
	vm := r.vm
	fn := func(name string, length int, f NativeFunc) Value {
		return ObjectValue(r.newNativeFunction(name, length, f))
	}
	switch c {
	case bytecode.LinkTimeConstantCallFunction:
		return r.FunctionPrototype.GetStr("call")
	case bytecode.LinkTimeConstantApplyFunction:
		return r.FunctionPrototype.GetStr("apply")
	case bytecode.LinkTimeConstantHasOwnPropertyFunction:
		return r.ObjectPrototype.GetStr("hasOwnProperty")
	case bytecode.LinkTimeConstantReflectConstructFunction:
		return r.GlobalObject.GetStr("Reflect").ObjectRef().GetStr("construct")
	case bytecode.LinkTimeConstantEvalFunction:
		return ObjectValue(r.evalFunction)
	case bytecode.LinkTimeConstantArray:
		return ObjectValue(r.ArrayConstructor)
	case bytecode.LinkTimeConstantObject:
		return ObjectValue(r.ObjectConstructor)
	case bytecode.LinkTimeConstantThrowTypeErrorFunction:
		return ObjectValue(r.ThrowTypeError)
	case bytecode.LinkTimeConstantSuppressedError:
		return ObjectValue(r.ErrorConstructors[ErrorKindSuppressedError])
	case bytecode.LinkTimeConstantEmptyPropertyNameEnumerator:
		return r.emptyEnumerator
	case bytecode.LinkTimeConstantSentinelString:
		return r.sentinelString
	case bytecode.LinkTimeConstantCreatePrivateSymbol:
		return fn("createPrivateSymbol", 1, func(c *Call) Value {
			var desc *String
			if d := c.Arg(0); d.IsString() {
				desc = d.StringRef()
			}
			return SymbolValue(&Symbol{description: desc, private: true})
		})
	case bytecode.LinkTimeConstantSetPrototypeDirect:
		return fn("setPrototypeDirect", 1, func(c *Call) Value {
			if o := c.This.AsObject(); o != nil {
				p := c.Arg(0)
				if p.IsObject() || p.IsNull() {
					o.SetPrototypeOf(p.AsObject())
				}
			}
			return Undefined
		})
	case bytecode.LinkTimeConstantSetPrototypeDirectOrThrow:
		return fn("setPrototypeDirectOrThrow", 1, func(c *Call) Value {
			o := c.This.AsObject()
			p := c.Arg(0)
			if !p.IsObject() && !p.IsNull() {
				vm.throwTypeError("Class extends value does not have valid prototype property")
			}
			if !o.SetPrototypeOf(p.AsObject()) {
				vm.throwTypeError("Could not set prototype")
			}
			return Undefined
		})
	case bytecode.LinkTimeConstantCopyDataProperties:
		return fn("copyDataProperties", 2, func(c *Call) Value {
			var excluded []ident.Identifier
			if set := c.Arg(1); set.IsNumber() && vm.currentFrame != nil {
				excluded = vm.currentFrame.cb.u.ConstantIdentifierSets[int(set.Float())]
			}
			vm.copyDataProperties(c.This.ObjectRef(), c.Arg(0), excluded, c.Args[min(2, len(c.Args)):])
			return Undefined
		})
	case bytecode.LinkTimeConstantCloneObject:
		return fn("cloneObject", 0, func(c *Call) Value {
			o := r.NewObject()
			vm.copyDataProperties(o, c.This, nil, nil)
			return ObjectValue(o)
		})
	case bytecode.LinkTimeConstantGetDisposeMethod:
		return fn("getDisposeMethod", 1, func(c *Call) Value { return vm.getDisposeMethod(c.Arg(0), false) })
	case bytecode.LinkTimeConstantGetAsyncDisposeMethod:
		return fn("getAsyncDisposeMethod", 1, func(c *Call) Value { return vm.getDisposeMethod(c.Arg(0), true) })
	case bytecode.LinkTimeConstantNewResolvedPromise:
		return fn("newResolvedPromise", 1, func(c *Call) Value {
			p := r.newPromise()
			vm.resolvePromise(p, c.Arg(0))
			return ObjectValue(p)
		})
	case bytecode.LinkTimeConstantNewRejectedPromise:
		return fn("newRejectedPromise", 1, func(c *Call) Value {
			p := r.newPromise()
			vm.rejectPromise(p, c.Arg(0))
			return ObjectValue(p)
		})
	case bytecode.LinkTimeConstantResolvePromiseWithFirstResolvingFunctionCallCheck:
		return fn("resolvePromiseWithFirstResolvingFunctionCallCheck", 2, func(c *Call) Value {
			p := c.Arg(0).ObjectRef()
			if promiseOf(p).state == promisePending && !promiseOf(p).alreadyResolved {
				vm.resolvePromise(p, c.Arg(1))
			}
			return Undefined
		})
	case bytecode.LinkTimeConstantRejectPromiseWithFirstResolvingFunctionCallCheck:
		return fn("rejectPromiseWithFirstResolvingFunctionCallCheck", 2, func(c *Call) Value {
			p := c.Arg(0).ObjectRef()
			if promiseOf(p).state == promisePending && !promiseOf(p).alreadyResolved {
				vm.rejectPromise(p, c.Arg(1))
			}
			return Undefined
		})
	case bytecode.LinkTimeConstantAsyncFunctionDrive:
		return fn("asyncFunctionDrive", 2, func(c *Call) Value {
			vm.asyncFunctionAwait(c.Arg(1).ObjectRef(), c.Arg(0))
			return Undefined
		})
	case bytecode.LinkTimeConstantImportModule:
		return fn("importModule", 3, func(c *Call) Value {
			return vm.importModuleDynamically(c.Arg(0), c.Arg(1), c.Arg(2))
		})
	}
	panic("vm: unsupported link-time constant " + c.String())
}

// copyDataProperties is CopyDataProperties (7.3.25).
func (vm *VM) copyDataProperties(target *Object, source Value, excluded []ident.Identifier, excludedValues []Value) {
	if source.IsNullish() {
		return
	}
	from := vm.ToObject(source)
	isExcluded := func(k PropertyKey) bool {
		if !k.IsSymbol() {
			for _, e := range excluded {
				if e == k.Ident() {
					return true
				}
			}
		}
		for _, v := range excludedValues {
			if vm.ToPropertyKey(v) == k {
				return true
			}
		}
		return false
	}
	for _, k := range from.OwnPropertyKeys() {
		if isExcluded(k) {
			continue
		}
		p, ok := from.GetOwnProperty(k)
		if ok && p.Enumerable() {
			target.CreateDataPropertyOrThrow(k, from.Get(k, ObjectValue(from)))
		}
	}
}

// --- Running scripts -------------------------------------------------------------------

// Script is a compiled classic script.
type Script struct {
	code   *bytecode.UnlinkedCodeBlock
	source parser.SourceCode
}

// CompileScript parses and compiles a classic script. Syntax errors are
// returned as *Exception holding a SyntaxError of the current realm.
func (vm *VM) CompileScript(source, name string) (*Script, error) {
	src := parser.NewSourceCode(parser.NewSourceProvider(source, name, parser.SourceTypeProgram))
	ucb, err := bytecompiler.CompileProgram(src, parser.NoLexicallyScopedFeatures, 0)
	if err != nil {
		return nil, &Exception{Value: ObjectValue(vm.realm.newError(ErrorKindSyntaxError, err.Message))}
	}
	return &Script{code: ucb, source: src}, nil
}

// RunScript runs a compiled script in the current realm (ScriptEvaluation).
// It returns the exception the script throws as *Exception, or
// ErrInterrupted.
func (vm *VM) RunScript(s *Script) (result Value, err error) {
	if err := vm.guard(func() { result = vm.runScript(vm.realm, s) }); err != nil {
		return Undefined, err
	}
	return result, nil
}

// Eval compiles and runs source as a script.
func (vm *VM) Eval(source, name string) (Value, error) {
	s, err := vm.CompileScript(source, name)
	if err != nil {
		return Undefined, err
	}
	return vm.RunScript(s)
}

func (vm *VM) runScript(r *Realm, s *Script) Value {
	saved := vm.realm
	vm.realm = r
	defer func() { vm.realm = saved }()
	cb := r.link(s.code, s.source)
	vm.globalDeclarationInstantiation(r, s.code)
	return vm.execute(vm.newFrame(cb, nil, r.globalLexical, ObjectValue(r.GlobalThis), nil))
}

// globalDeclarationInstantiation (16.1.7).
func (vm *VM) globalDeclarationInstantiation(r *Realm, ucb *bytecode.UnlinkedCodeBlock) {
	g := r.GlobalObject
	lex := r.globalLexical
	for name := range ucb.LexicalDeclarations.All {
		if lex.table.Contains(name) {
			vm.throwSyntaxError("Can't create duplicate variable: '%s'", name)
		}
		if p, ok := g.GetOwnProperty(IdentKey(name)); ok && !p.Configurable() {
			vm.throwSyntaxError("Can't create duplicate variable that shadows a global property: '%s'", name)
		}
	}
	for name := range ucb.VarDeclarations.All {
		if lex.table.Contains(name) {
			vm.throwSyntaxError("Can't create duplicate variable: '%s'", name)
		}
	}
	var functions, vars []ident.Identifier
	for name, entry := range ucb.VarDeclarations.All {
		if entry.IsFunction() {
			functions = append(functions, name)
		} else {
			vars = append(vars, name)
		}
	}
	for _, name := range functions {
		if !vm.canDeclareGlobalFunction(g, IdentKey(name)) {
			vm.throwTypeError("Can't declare global function '%s'", name)
		}
	}
	for _, name := range vars {
		if !g.HasOwnProperty(IdentKey(name)) && !g.IsExtensible() {
			vm.throwTypeError("Can't declare global variable '%s'", name)
		}
	}
	for name, entry := range ucb.LexicalDeclarations.All {
		off := lex.table.TakeNextScopeOffset()
		lex.table.Add(name, bytecode.SymbolTableEntry{Offset: bytecode.VarOffsetForScope(off), ReadOnly: entry.IsConst()})
		r.globalLexicalGen++
		lex.slots = append(lex.slots, Empty)
	}
	for _, name := range functions {
		vm.createGlobalFunctionBinding(g, IdentKey(name), Undefined, false)
		r.varNames[name] = struct{}{}
	}
	for _, name := range vars {
		vm.createGlobalVarBinding(g, IdentKey(name), false)
		r.varNames[name] = struct{}{}
	}
}

func (vm *VM) canDeclareGlobalFunction(g *Object, k PropertyKey) bool {
	p, ok := g.GetOwnProperty(k)
	if !ok {
		return g.IsExtensible()
	}
	if p.Configurable() {
		return true
	}
	return !p.IsAccessor() && p.Writable() && p.Enumerable()
}

func (vm *VM) createGlobalFunctionBinding(g *Object, k PropertyKey, v Value, deletable bool) {
	p, ok := g.GetOwnProperty(k)
	var d PropertyDescriptor
	if !ok || p.Configurable() {
		flags := Writable | Enumerable
		if deletable {
			flags |= Configurable
		}
		d = DataDescriptor(v, flags)
	} else {
		d = PropertyDescriptor{Value: v, Has: HasValue}
	}
	g.DefinePropertyOrThrow(k, d)
	if !v.IsUndefined() {
		g.SetOrThrow(k, v)
	}
}

func (vm *VM) createGlobalVarBinding(g *Object, k PropertyKey, deletable bool) {
	if !g.HasOwnProperty(k) && g.IsExtensible() {
		flags := Writable | Enumerable
		if deletable {
			flags |= Configurable
		}
		g.DefinePropertyOrThrow(k, DataDescriptor(Undefined, flags))
	}
}
