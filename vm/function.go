package vm

import (
	"github.com/baslentfert/jsc2go/bytecode"
	"github.com/baslentfert/jsc2go/bytecompiler"
	"github.com/baslentfert/jsc2go/ident"
	"github.com/baslentfert/jsc2go/parser"
	"slices"
)

// Function objects (JSFunction, InternalFunction, JSBoundFunction) and the
// Call/Construct abstract operations.

// functionImpl makes an object callable.
type functionImpl interface {
	call(f *Object, this Value, args []Value) Value
	// construct returns the new object; newTarget is the constructor
	// [[Construct]] was invoked with.
	construct(f *Object, args []Value, newTarget *Object) Value
	isConstructor() bool
}

// Call holds the arguments of a native function call.
type Call struct {
	VM     *VM
	Callee *Object
	This   Value
	Args   []Value
	// NewTarget is non-nil when the function is invoked by [[Construct]].
	NewTarget *Object
}

// Arg returns argument i, or undefined.
func (c *Call) Arg(i int) Value {
	if i < len(c.Args) {
		return c.Args[i]
	}
	return Undefined
}

// Realm is the realm of the function being called.
func (c *Call) Realm() *Realm { return c.Callee.realm }

// NativeFunc implements a built-in function.
type NativeFunc func(c *Call) Value

type nativeFunction struct {
	fn   NativeFunc
	ctor bool
	// fast, if set, handles common cases without allocating a Call (like
	// JSC's intrinsics); it must not keep args and reports false to fall
	// back to fn.
	fast fastNative
	// intrinsic is what JSC's property tables say the function is
	// (builtinIntrinsics), for the JIT's thunks.
	intrinsic Intrinsic
}

// fastNative is a builtin's allocation-free fast path.
type fastNative func(vm *VM, this Value, args []Value) (Value, bool)

func (n *nativeFunction) call(f *Object, this Value, args []Value) Value {
	return n.invoke(f, this, args, nil)
}

func (n *nativeFunction) construct(f *Object, args []Value, newTarget *Object) Value {
	return n.invoke(f, Undefined, args, newTarget)
}

// nativeCall is a Call together with room for a few arguments. The VM keeps
// one per depth of native calls (VM.nativeCalls) and reuses it: allocating
// one per call was the largest source of garbage in GC-heavy benchmarks
// (17% of the bytes allocated). So natives must not keep c or c.Args after
// they return (TestNativesKeepNoCall checks the sources); the arguments are
// copied out of the caller's frame, which the native may outlive within the
// call (it can run other code).
type nativeCall struct {
	c   Call
	buf [3]Value
}

func (n *nativeFunction) invoke(f *Object, this Value, args []Value, newTarget *Object) Value {
	vm := f.vm()
	// Fast paths run in the current realm, without the realm switch.
	if n.fast != nil && newTarget == nil && f.realm == vm.realm {
		if v, ok := n.fast(vm, this, args); ok {
			return v
		}
	}
	vm.enterCall()
	saved := vm.realm
	vm.realm = f.realm
	d := vm.nativeDepth
	if d == len(vm.nativeCalls) {
		vm.nativeCalls = append(vm.nativeCalls, new(nativeCall))
	}
	nc := vm.nativeCalls[d]
	vm.nativeDepth = d + 1
	defer func() {
		vm.leaveCall()
		vm.realm = saved
		vm.nativeDepth = d
		*nc = nativeCall{} // keeps nothing alive
	}()
	nc.c = Call{VM: vm, Callee: f, This: this, NewTarget: newTarget}
	if len(args) <= len(nc.buf) {
		nc.c.Args = nc.buf[:len(args):len(args)]
		copy(nc.c.Args, args)
	} else {
		nc.c.Args = copyArgs(args)
	}
	return n.fn(&nc.c)
}

func (n *nativeFunction) isConstructor() bool { return n.ctor }

// newNativeFunction creates a built-in function object.
func (r *Realm) newNativeFunction(name string, length int, fn NativeFunc) *Object {
	return r.newNativeFunctionKey(StrKey(name), length, fn, false)
}

// newNativeConstructor creates a built-in constructor.
func (r *Realm) newNativeConstructor(name string, length int, fn NativeFunc) *Object {
	return r.newNativeFunctionKey(StrKey(name), length, fn, true)
}

func (r *Realm) newNativeFunctionKey(name PropertyKey, length int, fn NativeFunc, ctor bool) *Object {
	f := r.newObject(r.FunctionPrototype)
	f.class = ClassFunction
	f.fn = &nativeFunction{fn: fn, ctor: ctor}
	f.putDirect(StrKey("length"), Int(length), Configurable)
	f.putDirect(StrKey("name"), StringValue(functionNameForKey(name, "")), Configurable)
	return f
}

// functionNameForKey is the name SetFunctionName gives for key (with an
// optional "get"/"set" prefix).
func functionNameForKey(k PropertyKey, prefix string) *String {
	var name *String
	if k.IsSymbol() {
		if d := k.sym.description; d != nil {
			name = Concat(NewString("["), Concat(d, NewString("]")))
		} else {
			name = emptyString
		}
	} else {
		name = NewString(k.String())
	}
	if prefix != "" {
		name = Concat(NewString(prefix+" "), name)
	}
	return name
}

// --- JavaScript functions ------------------------------------------------------------

// jsFunction is a function defined in JavaScript (JSFunction with a
// FunctionExecutable).
type jsFunction struct {
	exec  *bytecode.UnlinkedFunctionExecutable
	scope *Scope
	// code caches the linked code blocks for call and construct.
	code [2]*CodeBlock
	// insideOrdinary is ScriptExecutable::isInsideOrdinaryFunction.
	insideOrdinary bool
	// classConstructor caches exec.IsClassConstructorFunction (checked on
	// every call).
	classConstructor bool
}

func (j *jsFunction) isConstructor() bool {
	return j.exec.ConstructAbility == bytecode.CanConstruct
}

func (j *jsFunction) codeBlock(f *Object, kind bytecode.CodeSpecializationKind) *CodeBlock {
	if cb := j.code[kind]; cb != nil {
		return cb
	}
	vm := f.vm()
	ucb, err := bytecompiler.CodeBlockForFunction(j.exec, kind, 0)
	if err != nil {
		vm.throwSyntaxError("%s", err.Error())
	}
	cb := f.realm.link(ucb, j.exec.Source)
	j.code[kind] = cb
	return cb
}

func (j *jsFunction) call(f *Object, this Value, args []Value) Value {
	if j.classConstructor {
		// The error belongs to the callee's realm (10.2.1 step 2).
		vm := f.vm()
		saved := vm.realm
		vm.realm = f.realm
		defer func() { vm.realm = saved }()
		vm.throwTypeError("Cannot call a class constructor without |new|")
	}
	cb := j.codeBlock(f, bytecode.CodeForCall)
	return f.vm().runFunction(cb, f, j.scope, this, args)
}

func (j *jsFunction) construct(f *Object, args []Value, newTarget *Object) Value {
	cb := j.codeBlock(f, bytecode.CodeForConstruct)
	return f.vm().runFunction(cb, f, j.scope, ObjectValue(newTarget), args)
}

// newJSFunction is op_new_func and friends: it creates the closure for
// exec in scope.
// closureBlock is a closure in one allocation: the object, its jsFunction
// and its length, name and prototype (programs make closures all the
// time).
type closureBlock struct {
	o Object
	j jsFunction
	v [3]Value
}

// newClosureBlock makes a closure without its executable, scope,
// prototype and properties (generated code's pool fills them too).
func (r *Realm) newClosureBlock() *closureBlock {
	b := new(closureBlock)
	b.o = Object{realm: r, extensible: true, props: propertyMap{shape: r.vm.rootShape, vals: b.v[:0]},
		class: ClassFunction, fn: &b.j}
	return b
}

func (r *Realm) newJSFunction(exec *bytecode.UnlinkedFunctionExecutable, scope *Scope) *Object {
	mode := exec.ParseMode
	proto := r.FunctionPrototype
	switch {
	case mode.IsAsyncGeneratorWrapper():
		proto = r.AsyncGeneratorFunctionPrototype
	case mode.IsGeneratorWrapper():
		proto = r.GeneratorFunctionPrototype
	case mode.IsAsyncFunctionWrapper():
		proto = r.AsyncFunctionPrototype
	}
	b := r.newClosureBlock()
	b.j.exec, b.j.scope, b.j.classConstructor = exec, scope, exec.IsClassConstructorFunction()
	b.o.proto = proto
	f := &b.o

	f.putDirect(lengthKey, Int(exec.ParameterCount), Configurable)
	nameStr, ok := exec.NameString.(*String)
	if !ok {
		nameStr = functionNameString(exec)
		exec.NameString = nameStr
	}
	f.putDirect(nameKey, StringValue(nameStr), Configurable)

	switch {
	case mode.IsGeneratorWrapper():
		p := r.newObject(r.GeneratorPrototype)
		f.putDirect(prototypeKey, ObjectValue(p), Writable)
	case mode.IsAsyncGeneratorWrapper():
		p := r.newObject(r.AsyncGeneratorPrototype)
		f.putDirect(prototypeKey, ObjectValue(p), Writable)
	case mode == parser.NormalFunctionMode && exec.ConstructorKind == parser.ConstructorKindNone &&
		exec.ConstructAbility == bytecode.CanConstruct:
		// The prototype object is made when it is first looked at
		// (reifyPrototype), as JSC's JSFunction does: most functions
		// are never constructed, and it was 200 of a closure's 483
		// bytes. Its place in the layout is kept, so that the order of
		// keys does not change.
		f.putDirect(prototypeKey, Empty, Writable|lazyFlag)
	}
	return f
}

// reifyPrototype makes the prototype object of function o if newJSFunction
// left it lazy (JSFunction::reifyLazyPrototypeIfNeeded). Get, Set, define
// and getOwnPropertyDescriptor reach it through ordinaryGetOwnProperty;
// deleting it fails without (it is not configurable). The object moves to
// the shape it would have had with the prototype made at once.
func (o *Object) reifyPrototype() {
	s := o.props.shape
	i := s.lookup(prototypeKey)
	if i < 0 || s.flags[i]&lazyFlag == 0 {
		return
	}
	p := o.realm.NewObject()
	p.putDirect(constructorKey, ObjectValue(o), Writable|Configurable)
	o.props.vals[i] = ObjectValue(p)
	if s.dictionary {
		s.flags[i] &^= lazyFlag
		s.lazy = slices.ContainsFunc(s.flags, func(f PropertyFlags) bool { return f&lazyFlag != 0 })
		s.version++
		o.layoutChanged()
		return
	}
	n := o.realm.vm.rootShape
	for j, k := range s.keys {
		f := s.flags[j]
		if j == i {
			f &^= lazyFlag
		}
		n = n.withKey(k, f)
	}
	o.props.shape = n
	o.layoutChanged()
}

// functionNameString is the initial value of the name property of
// functions made from exec.
func functionNameString(exec *bytecode.UnlinkedFunctionExecutable) *String {
	name := exec.EcmaName()
	var nameStr *String
	switch {
	case name == ident.StarDefaultPrivateName:
		nameStr = NewString("default")
	case name.IsSymbol():
		nameStr = emptyString
	default:
		nameStr = NewString(name.String())
	}
	if prefix := accessorPrefix(exec.ParseMode); prefix != "" {
		nameStr = Concat(NewString(prefix+" "), nameStr)
	}
	return nameStr
}

// setFunctionName is op_set_function_name (JSFunction::setFunctionName).
func (vm *VM) setFunctionName(f *Object, name Value) {
	var k PropertyKey
	if name.IsSymbol() {
		k = SymKey(name.SymbolRef())
	} else {
		k = vm.ToPropertyKey(name)
	}
	prefix := ""
	if j, ok := f.fn.(*jsFunction); ok {
		prefix = accessorPrefix(j.exec.ParseMode)
	}
	f.DefineOwnProperty(StrKey("name"), DataDescriptor(StringValue(functionNameForKey(k, prefix)), Configurable))
}

// --- Bound functions -----------------------------------------------------------------

type boundFunction struct {
	target    *Object
	boundThis Value
	boundArgs []Value
}

func (b *boundFunction) isConstructor() bool { return b.target.IsConstructor() }

func (b *boundFunction) args(args []Value) []Value {
	if len(b.boundArgs) == 0 {
		return args
	}
	all := make([]Value, 0, len(b.boundArgs)+len(args))
	return append(append(all, b.boundArgs...), args...)
}

func (b *boundFunction) call(f *Object, this Value, args []Value) Value {
	return b.target.fn.call(b.target, b.boundThis, b.args(args))
}

func (b *boundFunction) construct(f *Object, args []Value, newTarget *Object) Value {
	if newTarget == f {
		newTarget = b.target
	}
	return b.target.vm().Construct(b.target, b.args(args), newTarget)
}

// --- Call and Construct --------------------------------------------------------------

func (vm *VM) enterCall() {
	if vm.interrupted.Load() {
		panic(ErrInterrupted) // (recursion without loops)
	}
	vm.depth++
	if vm.depth > vm.MaxDepth {
		vm.depth -= 10 // leave room to construct the error
		defer func() { vm.depth += 10 }()
		vm.throwRangeError("Maximum call stack size exceeded.")
	}
}

func (vm *VM) leaveCall() { vm.depth-- }

// Call is Call(F, V, argumentsList) (7.3.14).
func (vm *VM) Call(f Value, this Value, args ...Value) Value {
	o := f.AsObject()
	if o == nil || o.fn == nil {
		vm.throwTypeError("%s is not a function", vm.describeForError(f))
	}
	// Callees copy the arguments they keep (JavaScript frames, native
	// calls), so args need not escape: callers can keep it on the stack.
	if j, ok := o.fn.(*jsFunction); ok {
		return j.call(o, this, args)
	}
	return o.fn.call(o, this, args)
}

// callObject calls f, which must be callable.
func (vm *VM) callObject(f *Object, this Value, args []Value) Value {
	return f.fn.call(f, this, args)
}

// Construct is Construct(F, argumentsList, newTarget) (7.3.15).
func (vm *VM) Construct(f *Object, args []Value, newTarget *Object) Value {
	if newTarget == nil {
		newTarget = f
	}
	return f.fn.construct(f, args, newTarget)
}

// constructValue checks that f is a constructor and constructs it.
func (vm *VM) constructValue(f Value, args []Value, newTarget *Object) Value {
	o := f.AsObject()
	if o == nil || !o.IsConstructor() {
		vm.throwTypeError("%s is not a constructor", vm.describeForError(f))
	}
	return vm.Construct(o, args, newTarget)
}

// describeForError names a value in error messages without running user
// code.
func (vm *VM) describeForError(v Value) string {
	switch v.Kind() {
	case KindString:
		return "\"" + v.StringRef().GoString() + "\""
	case KindObject:
		o := v.ObjectRef()
		if o.IsCallable() {
			if name, ok := lookupDataProperty(o, StrKey("name")); ok && name.IsString() && name.StringRef().Length() > 0 {
				return "function " + name.StringRef().GoString()
			}
			return "function"
		}
		if o.class == ClassArray {
			return "array"
		}
		return "[object Object]"
	}
	return safeToString(v)
}

// GetMethod (7.3.11) returns the callable at v[k], or undefined.
func (vm *VM) GetMethod(v Value, k PropertyKey) Value {
	f := vm.GetV(v, k)
	if f.IsNullish() {
		return Undefined
	}
	if !f.IsCallable() {
		vm.throwTypeError("%s is not a function", k)
	}
	return f
}

// Invoke (7.3.21).
func (vm *VM) Invoke(v Value, k PropertyKey, args ...Value) Value {
	return vm.Call(vm.GetV(v, k), v, args...)
}

// GetV (7.3.3) reads a property of any value.
func (vm *VM) GetV(v Value, k PropertyKey) Value {
	if o := v.AsObject(); o != nil {
		return o.Get(k, v)
	}
	return vm.getPrimitiveProperty(v, k)
}

func (vm *VM) getPrimitiveProperty(v Value, k PropertyKey) Value {
	var proto *Object
	r := vm.realm
	switch v.Kind() {
	case KindString:
		s := v.StringRef()
		if !k.IsSymbol() {
			if k.name == lengthIdent {
				return Int(s.Length())
			}
			if i, ok := k.Index(); ok && int64(i) < int64(s.Length()) {
				return StringValue(s.Substring(int(i), int(i)+1))
			}
		}
		proto = r.StringPrototype
	case KindNumber:
		proto = r.NumberPrototype
	case KindBool:
		proto = r.BooleanPrototype
	case KindSymbol:
		proto = r.SymbolPrototype
	case KindBigInt:
		proto = r.BigIntPrototype
	default:
		vm.throwTypeError("Cannot read property '%s' of %s", k, v.TypeOfForMessage())
	}
	return proto.Get(k, v)
}

var lengthIdent = ident.FromString("length")

// Keys that built-ins use on every call, interned once.
var (
	lengthKey      = PropertyKey{name: lengthIdent}
	nameKey        = StrKey("name")
	constructorKey = StrKey("constructor")
)

// GetPrototypeFromConstructor (10.1.14): the "prototype" of newTarget, or
// the intrinsic picked by fallback from newTarget's realm.
func (vm *VM) GetPrototypeFromConstructor(newTarget *Object, fallback func(*Realm) *Object) *Object {
	proto := newTarget.Get(prototypeKey, ObjectValue(newTarget))
	if p := proto.AsObject(); p != nil {
		return p
	}
	return fallback(vm.functionRealm(newTarget))
}

// functionRealm is GetFunctionRealm (7.3.24).
func (vm *VM) functionRealm(f *Object) *Realm {
	for {
		switch impl := f.fn.(type) {
		case *boundFunction:
			f = impl.target
			continue
		case *proxyCall:
			p := f.internal.(*proxyData)
			if p.handler == nil {
				vm.throwTypeError("Proxy has been revoked")
			}
			f = p.target
			continue
		}
		return f.realm
	}
}

// OrdinaryCreateFromConstructor (10.1.13).
func (vm *VM) OrdinaryCreateFromConstructor(newTarget *Object, fallback func(*Realm) *Object) *Object {
	proto := vm.GetPrototypeFromConstructor(newTarget, fallback)
	return vm.realm.newObject(proto)
}

// SpeciesConstructor (7.3.22).
func (vm *VM) SpeciesConstructor(o *Object, defaultConstructor *Object) *Object {
	c := o.GetStr("constructor")
	if c.IsUndefined() {
		return defaultConstructor
	}
	co := c.AsObject()
	if co == nil {
		vm.throwTypeError("constructor is not an object")
	}
	s := co.Get(SymKey(SymbolSpecies), c)
	if s.IsNullish() {
		return defaultConstructor
	}
	if !s.IsConstructor() {
		vm.throwTypeError("species is not a constructor")
	}
	return s.ObjectRef()
}

// OrdinaryHasInstance (7.3.20).
func (vm *VM) OrdinaryHasInstance(c Value, o Value) bool {
	co := c.AsObject()
	if co == nil || !co.IsCallable() {
		return false
	}
	if b, ok := co.fn.(*boundFunction); ok {
		return vm.InstanceOf(o, ObjectValue(b.target))
	}
	obj := o.AsObject()
	if obj == nil {
		return false
	}
	p := co.GetKey(prototypeKey)
	proto := p.AsObject()
	if proto == nil {
		vm.throwTypeError("instanceof called on an object with an invalid prototype property")
	}
	for {
		obj = obj.GetPrototypeOf()
		if obj == nil {
			return false
		}
		if obj == proto {
			return true
		}
	}
}

// InstanceOf is InstanceofOperator (13.10.2).
func (vm *VM) InstanceOf(v Value, target Value) bool {
	if !target.IsObject() {
		vm.throwTypeError("Right hand side of instanceof is not an object")
	}
	h := vm.GetMethod(target, SymKey(SymbolHasInstance))
	if !h.IsUndefined() {
		return vm.Call(h, target, v).ToBoolean()
	}
	if !target.IsCallable() {
		vm.throwTypeError("Right hand side of instanceof is not callable")
	}
	return vm.OrdinaryHasInstance(target, v)
}

// LengthOfArrayLike (7.3.19).
func (vm *VM) LengthOfArrayLike(o *Object) int64 {
	if o.class == ClassArray && o.exotic != nil {
		if a, ok := o.exotic.(*arrayObject); ok {
			return int64(a.length)
		}
	}
	return vm.ToLength(o.GetKey(lengthKey))
}

// CreateListFromArrayLike (7.3.20).
func (vm *VM) CreateListFromArrayLike(v Value, propertyKeysOnly bool) []Value {
	o := v.AsObject()
	if o == nil {
		vm.throwTypeError("CreateListFromArrayLike called on non-object")
	}
	n := vm.LengthOfArrayLike(o)
	if n > 1<<24 {
		vm.throwRangeError("Too many arguments")
	}
	list := make([]Value, n)
	for i := range list {
		e := o.getIndex(uint32(i))
		if propertyKeysOnly && !e.IsString() && !e.IsSymbol() {
			vm.throwTypeError("Property key must be a string or symbol")
		}
		list[i] = e
	}
	return list
}

// accessorPrefix is "get" or "set" for accessor functions.
func accessorPrefix(mode parser.SourceParseMode) string {
	switch mode {
	case parser.GetterMode:
		return "get"
	case parser.SetterMode:
		return "set"
	}
	return ""
}
