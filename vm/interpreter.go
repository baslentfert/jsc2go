package vm

import (
	"math"
	"slices"
	"unsafe"

	"github.com/baslentfert/jsc2go/bytecode"
	"github.com/baslentfert/jsc2go/ident"
	"github.com/baslentfert/jsc2go/parser"
)

// The bytecode interpreter (JSC's LLInt plus its slow paths, in Go). Each
// JavaScript call runs a Go call of execute; exceptions are Go panics
// carrying an *Exception, recovered per frame to dispatch to handlers.

// Frame is an activation of a code block.
type Frame struct {
	cb *CodeBlock
	// regs holds the locals (indices [0, nl)), then the call frame header
	// and the arguments: virtual register r lives at regs[nl+r].
	regs   []Value
	nl     int
	argc   int // including this
	callee *Object
	scope  *Scope
	pc     int
	caller *Frame

	exception *Exception

	// insideOrdinary is set for eval code inside a non-arrow function.
	insideOrdinary bool

	// stackBase is where regs start in the VM register stack (-1: heap).
	stackBase int

	// base points at virtual register 0 (regs[nl]) and consts at the code
	// block's first constant: get and set index them without bounds checks,
	// which is safe because link verified every register operand of the
	// code block (verifyRegisters) and regs never move.
	base, consts unsafe.Pointer
	// jitFuel counts down the loop iterations generated code may still
	// run before returning to the interpreter.
	jitFuel int64
	// ret is where generated code leaves the result of a return.
	ret Value
	// vm is the frame's VM (for generated code, which makes frames of its
	// own: jit_calls.go); jitExit is the exit code generated code that ran
	// in such a frame stopped with.
	vm      *VM
	jitExit uintptr
	// jitSuspends counts the times generated code called directly in this
	// frame suspended in this call (jit_suspend.go).
	jitSuspends int64
	// inlineRoom is how many registers above sp the calls generated code
	// inlines may use in this activation, as checked against the call
	// depth, the chain of generated-code frames and the register stack
	// (jit_inline.go); 0: not checked.
	inlineRoom int64
}

const valueSize = int(unsafe.Sizeof(Value{}))

func (f *Frame) get(r bytecode.VirtualRegister) Value {
	if r >= bytecode.FirstConstantRegisterIndex {
		return *(*Value)(unsafe.Add(f.consts, (int(r)-bytecode.FirstConstantRegisterIndex)*valueSize))
	}
	return *(*Value)(unsafe.Add(f.base, int(r)*valueSize))
}

// ref returns the address of register r. It is get for the interpreter
// loop: run is so big that Go only inlines callees of cost up to 20 into
// it, which ref is and get is not.
func (f *Frame) ref(r bytecode.VirtualRegister) *Value {
	if r >= bytecode.FirstConstantRegisterIndex {
		return (*Value)(unsafe.Add(f.consts, (r-bytecode.FirstConstantRegisterIndex)*16))
	}
	return (*Value)(unsafe.Add(f.base, r*16))
}

// ref's 16 is valueSize (untyped, for the int32 arithmetic).
var _ = [1]struct{}{}[valueSize-16]

func (f *Frame) set(r bytecode.VirtualRegister, v Value) {
	*(*Value)(unsafe.Add(f.base, int(r)*valueSize)) = v
}

// getChecked is get for computed registers (ranges such as argv - i),
// which verifyRegisters cannot vouch for.
func (f *Frame) getChecked(r bytecode.VirtualRegister) Value {
	if r >= bytecode.FirstConstantRegisterIndex {
		return f.cb.constants[int(r)-bytecode.FirstConstantRegisterIndex]
	}
	return f.regs[f.nl+int(r)]
}

// orZero reports whether ins starts with bitor dst, src, 0 (asm.js's x|0,
// ToInt32 of a number) and returns its dst.
func (f *Frame) orZero(ins []uint32, src bytecode.VirtualRegister) (bytecode.VirtualRegister, bool) {
	if bytecode.OpcodeID(ins[0]) != bytecode.OpBitorID {
		return 0, false
	}
	o := bytecode.DecodeBitor(ins)
	r := f.get(o.Rhs)
	return o.Dst, o.Lhs == src && r.IsNumber() && r.num() == 0
}

func (f *Frame) thisValue() Value { return f.regs[f.nl+bytecode.CallFrameSlotThisArgument] }

// argument returns argument i (excluding this) or undefined.
func (f *Frame) argument(i int) Value {
	if i+1 < f.argc {
		return f.regs[f.nl+bytecode.CallFrameSlotFirstArgument+i]
	}
	return Undefined
}

// arguments returns the passed arguments (excluding this).
func (f *Frame) arguments() []Value {
	start := f.nl + bytecode.CallFrameSlotFirstArgument
	return f.regs[start : start+f.argc-1]
}

// forwardedArguments is bytecode.ForwardedArguments as a value: an
// operand of call_varargs that stands for the frame's arguments.
var forwardedArguments = cellValue(&forwardedArgumentsCell{})

type forwardedArgumentsCell struct{ _ byte }

// newFrame sets up a frame for cb. this is the this argument (or
// new.target for construct calls).
func (vm *VM) newFrame(cb *CodeBlock, callee *Object, scope *Scope, this Value, args []Value) *Frame {
	nl := cb.u.NumCalleeLocals
	nparams := cb.u.NumParameters // including this
	n := len(args) + 1
	if n < nparams {
		n = nparams
	}
	regs, base0 := vm.allocRegs(nl + bytecode.CallFrameSlotThisArgument + n)
	base := nl + bytecode.CallFrameSlotThisArgument
	regs[base] = this
	copy(regs[base+1:], args)
	for i := base + 1 + len(args); i < len(regs); i++ {
		regs[i] = Undefined
	}
	if callee != nil {
		regs[nl+bytecode.CallFrameSlotCallee] = ObjectValue(callee)
	}
	var f *Frame
	if n := len(vm.freeFrames); n > 0 {
		f = vm.freeFrames[n-1]
		vm.freeFrames = vm.freeFrames[:n-1]
	} else {
		f = new(Frame)
	}
	// Pooled frames are zero (releaseFrame): set only what is not.
	f.cb, f.regs, f.nl, f.argc = cb, regs, nl, len(args)+1
	f.callee, f.scope, f.stackBase = callee, scope, base0
	f.base, f.consts = unsafe.Pointer(&regs[nl]), unsafe.Pointer(unsafe.SliceData(cb.constants))
	f.vm = vm
	return f
}

// allocRegs takes n registers from the VM's register stack (frames are
// strictly LIFO); stackBase is -1 when they had to be heap allocated.
func (vm *VM) allocRegs(n int) ([]Value, int) {
	if vm.sp+n > len(vm.regStack) {
		if vm.sp+n > maxRegStack {
			return make([]Value, n), -1
		}
		// Grow: frames below keep their slices into the old array.
		// (Frames below keep their slices into the old array, which must
		// not keep anything else alive.)
		vm.clearDirtyRegs()
		vm.regStack = make([]Value, max(2*len(vm.regStack), vm.sp+n, 4096))
	}
	base := vm.sp
	vm.sp += n
	if base < vm.spDirty {
		clear(vm.regStack[base:min(vm.sp, vm.spDirty)])
	}
	return vm.regStack[base:vm.sp:vm.sp], base
}

// releaseFrame returns the registers and the Frame of a finished frame;
// nothing refers to a frame once it has returned.
func (vm *VM) releaseFrame(f *Frame) {
	if f.stackBase >= 0 {
		clear(f.regs)
		vm.sp = f.stackBase
	}
	*f = Frame{}
	vm.freeFrames = append(vm.freeFrames, f)
}

const maxRegStack = 1 << 22

// runFunction runs a JavaScript function's code block.
func (vm *VM) runFunction(cb *CodeBlock, callee *Object, scope *Scope, this Value, args []Value) Value {
	vm.enterCall()
	saved, caller := vm.realm, vm.currentFrame
	vm.realm = cb.realm
	f := vm.newFrame(cb, callee, scope, this, args)
	f.caller = caller
	vm.currentFrame = f
	// One deferred cleanup per call (execute's and this one merged).
	defer func() {
		vm.currentFrame = caller
		vm.releaseFrame(f)
		vm.leaveCall()
		vm.realm = saved
	}()
	for {
		result := vm.runFrame(f)
		tc := vm.pendingTailCall
		if tc == nil {
			return result
		}
		// A proper tail call: reuse this Go frame for the callee.
		vm.pendingTailCall = nil
		j := tc.callee.fn.(*jsFunction)
		cb = j.codeBlock(tc.callee, bytecode.CodeForCall)
		vm.releaseFrame(f)
		vm.realm = cb.realm
		f = vm.newFrame(cb, tc.callee, j.scope, tc.this, tc.args)
		f.caller = caller
		vm.currentFrame = f
	}
}

type tailCallRequest struct {
	callee *Object
	this   Value
	args   []Value
}

// tailCall is op_tail_call: calls to JavaScript functions unwind the
// caller first (runFunction then runs the callee), others are ordinary
// calls.
func (vm *VM) tailCall(callee Value, this Value, args []Value) Value {
	if o := callee.AsObject(); o != nil {
		if j, ok := o.fn.(*jsFunction); ok && !j.exec.IsClassConstructorFunction() {
			vm.pendingTailCall = &tailCallRequest{o, this, copyArgs(args)} // the caller's registers are released first
			return Undefined
		}
	}
	return vm.callValue(callee, this, args)
}

// execute runs f to completion and returns its result.
func (vm *VM) execute(f *Frame) Value {
	f.caller = vm.currentFrame
	vm.currentFrame = f
	defer func() { vm.currentFrame = f.caller; vm.releaseFrame(f) }()
	return vm.runFrame(f)
}

// runFrame runs f until it returns, resuming at exception handlers.
func (vm *VM) runFrame(f *Frame) Value {
	if len(f.cb.u.ExceptionHandlers) == 0 {
		// Nothing to catch here: exceptions simply unwind to the caller.
		if result, done := vm.enterJIT(f); done {
			return result
		}
		return vm.run(f)
	}
	for {
		if result, done := vm.runProtected(f); done {
			return result
		}
	}
}

// runProtected runs f until it returns or throws. A thrown exception that
// f handles moves f to the handler and returns done == false.
func (vm *VM) runProtected(f *Frame) (result Value, done bool) {
	mark := vm.jitMark()
	defer func() {
		if r := recover(); r != nil {
			vm.jitUnwindTo(mark)
			exc, ok := r.(*Exception)
			if r == errStringTooLong {
				exc, ok = vm.outOfMemory(), true
				r = exc
			}
			if !ok {
				panic(r)
			}
			h := f.cb.u.HandlerForIndex(uint32(f.pc), bytecode.AnyHandler)
			if h == nil {
				panic(r)
			}
			f.pc = int(h.Target)
			f.exception = exc
			vm.currentFrame = f
			done = false
		}
	}()
	if result, done := vm.enterJIT(f); done {
		return result, true
	}
	return vm.run(f), true
}

// enterJIT runs the generated code for the whole function from the start
// of f, once the function is called often. If that code ends at a return,
// it returns the result (done) without the interpreter loop; otherwise f
// continues where the generated code stopped.
func (vm *VM) enterJIT(f *Frame) (Value, bool) {
	if !jitEnabled || f.pc != 0 {
		return Undefined, false
	}
	resume, ok := vm.tryJIT(f, 0)
	if !ok {
		return Undefined, false
	}
	in := f.cb.u.Instructions[resume:]
	if bytecode.OpcodeID(in[0]) == bytecode.OpRetID {
		return *f.ref(bytecode.DecodeRet(in).Value), true
	}
	f.pc = resume
	return Undefined, false
}

// thisForCall converts a scope passed as |this| (op_resolve_scope result
// used as the base of a call) to the this value it stands for: the binding
// object of a with scope, otherwise undefined.
func thisForCall(v Value) Value {
	if v.Kind() == KindCell {
		if s, ok := v.cell().(*Scope); ok {
			if s.kind == scopeWith {
				return ObjectValue(s.object)
			}
			return Undefined
		}
	}
	return v
}

// callArgs returns the callee frame's this and arguments that an op_call
// with argv and argc set up in f's registers.
func (f *Frame) callArgs(argv uint32, argc uint32) (this Value, args []Value) {
	base := f.nl - int(argv) + bytecode.CallFrameSlotThisArgument
	return thisForCall(f.regs[base]), f.regs[base+1 : base+int(argc)]
}

// copyArgs copies arguments so that callees may keep them.
func copyArgs(args []Value) []Value { return append([]Value(nil), args...) }

func (vm *VM) callValue(callee Value, this Value, args []Value) Value {
	o := callee.AsObject()
	if o == nil || o.fn == nil {
		vm.throwTypeError("%s is not a function", vm.describeForError(callee))
	}
	return o.fn.call(o, this, args) // callees copy what they keep
}

func (vm *VM) varargs(f *Frame, v Value, skip int32) []Value {
	if v.IsNullish() {
		return nil
	}
	if v == forwardedArguments {
		// x.apply(y, arguments) without an arguments object
		// (bytecode.ForwardedArguments): the frame's own arguments, which
		// the callee copies.
		args := f.arguments()
		if int(skip) >= len(args) {
			return nil
		}
		return args[skip:]
	}
	if s, ok := v.cell().(*spreadList); ok && v.Kind() == KindCell {
		if int(skip) >= len(s.values) {
			return nil
		}
		return s.values[skip:]
	}
	if !v.IsObject() {
		vm.throwTypeError("Arguments list must be an object")
	}
	if list, ok := vm.argumentsList(v.ObjectRef()); ok {
		if int(skip) >= len(list) {
			return nil
		}
		return list[skip:]
	}
	list := vm.CreateListFromArrayLike(v, false)
	if int(skip) >= len(list) {
		return nil
	}
	return list[skip:]
}

// spreadList is the result of op_spread (JSC's JSImmutableButterfly).
type spreadList struct{ values []Value }

func (f *Frame) jump(target bytecode.BoundLabel) { f.pc += int(target) }

func (f *Frame) key(i uint32) PropertyKey { return f.cb.keys[i] }

// run is the interpreter loop.
func (vm *VM) run(f *Frame) Value {
	cb := f.cb
	ins := cb.u.Instructions
	r := cb.realm
	for {
		pc := f.pc
		op := bytecode.OpcodeID(ins[pc])
		in := ins[pc:]
		next := pc + op.Length()

		switch op {
		case bytecode.OpEnterID:
			// The locals are already undefined: registers come zeroed from
			// the register stack and the zero Value is undefined.
			if cb.u.ScopeRegister.IsValid() {
				f.set(cb.u.ScopeRegister, scopeValue(f.scope))
			}

		case bytecode.OpLoopHintID, bytecode.OpCheckTrapsID:
			if vm.interrupted.Load() {
				panic(ErrInterrupted)
			}
			if jitEnabled && op == bytecode.OpLoopHintID {
				if resume, ok := vm.tryJIT(f, pc); ok {
					f.pc = resume
					continue
				}
			}

		case bytecode.OpGetScopeID:
			b := bytecode.DecodeGetScope(in)
			f.set(b.Dst, scopeValue(f.scope))

		case bytecode.OpMovID:
			b := bytecode.DecodeMov(in)
			f.set(b.Dst, (*f.ref(b.Src)))

		case bytecode.OpRetID:
			b := bytecode.DecodeRet(in)
			return (*f.ref(b.Value))

		case bytecode.OpNewObjectID:
			b := bytecode.DecodeNewObject(in)
			f.set(b.Dst, ObjectValue(r.newObjectWithCapacity(r.ObjectPrototype, b.InlineCapacity)))

		case bytecode.OpNewArrayID:
			b := bytecode.DecodeNewArray(in)
			values := make([]Value, b.Argc)
			for i := range values {
				values[i] = f.getChecked(b.Argv - bytecode.VirtualRegister(i))
			}
			f.set(b.Dst, ObjectValue(r.NewArray(values...)))

		case bytecode.OpNewArrayBufferID:
			b := bytecode.DecodeNewArrayBuffer(in)
			buf := (*f.ref(b.ImmutableButterfly)).cell().(*arrayBufferConstant)
			f.set(b.Dst, ObjectValue(r.NewArray(copyArgs(buf.values)...)))

		case bytecode.OpNewArrayWithSizeID:
			b := bytecode.DecodeNewArrayWithSize(in)
			f.set(b.Dst, ObjectValue(vm.arrayWithLength((*f.ref(b.Length)))))

		case bytecode.OpNewFuncID, bytecode.OpNewGeneratorFuncID, bytecode.OpNewAsyncFuncID, bytecode.OpNewAsyncGeneratorFuncID:
			b := bytecode.DecodeNewFunc(in)
			exec := cb.u.FunctionDecls[b.FunctionDecl]
			f.set(b.Dst, ObjectValue(vm.newClosure(f, exec, (*f.ref(b.Scope)).scope())))

		case bytecode.OpNewFuncExpID, bytecode.OpNewGeneratorFuncExpID, bytecode.OpNewAsyncFuncExpID, bytecode.OpNewAsyncGeneratorFuncExpID:
			b := bytecode.DecodeNewFuncExp(in)
			exec := cb.u.FunctionExprs[b.FunctionDecl]
			f.set(b.Dst, ObjectValue(vm.newClosure(f, exec, (*f.ref(b.Scope)).scope())))

		case bytecode.OpCreateThisID:
			b := bytecode.DecodeCreateThis(in)
			f.set(b.Dst, ObjectValue(vm.createThis(&cb.createThisCaches[b.MetadataID], (*f.ref(b.Callee)).ObjectRef(), b.InlineCapacity)))

		case bytecode.OpToThisID:
			b := bytecode.DecodeToThis(in)
			f.set(b.SrcDst, vm.toThis((*f.ref(b.SrcDst)), b.EcmaMode))

		case bytecode.OpCheckTdzID:
			b := bytecode.DecodeCheckTdz(in)
			if (*f.ref(b.TargetVirtualRegister)).IsEmpty() {
				if b.TargetVirtualRegister == bytecode.CallFrameSlotThisArgument {
					vm.throwReferenceError("'super()' must be called in derived constructor before accessing |this| or returning non-object.")
				}
				vm.throwReferenceError("Cannot access uninitialized variable.")
			}

		// --- Arguments ----------------------------------------------------------------

		case bytecode.OpArgumentCountID:
			b := bytecode.DecodeArgumentCount(in)
			f.set(b.Dst, Int(f.argc-1))

		case bytecode.OpGetArgumentID:
			b := bytecode.DecodeGetArgument(in)
			if int(b.Index) < f.argc {
				f.set(b.Dst, f.regs[f.nl+bytecode.CallFrameSlotThisArgument+int(b.Index)])
			} else {
				f.set(b.Dst, Undefined)
			}

		case bytecode.OpCreateRestID:
			b := bytecode.DecodeCreateRest(in)
			args := f.arguments()
			var rest []Value
			if int(b.NumParametersToSkip) < len(args) {
				rest = copyArgs(args[b.NumParametersToSkip:])
			}
			f.set(b.Dst, ObjectValue(r.NewArray(rest...)))

		case bytecode.OpCreateDirectArgumentsID:
			b := bytecode.DecodeCreateDirectArguments(in)
			f.set(b.Dst, ObjectValue(vm.createDirectArguments(f)))

		case bytecode.OpGetFromArgumentsID:
			b := bytecode.DecodeGetFromArguments(in)
			f.set(b.Dst, (*f.ref(b.Arguments)).ObjectRef().internal.(*argumentsData).getMapped(int(b.Index)))

		case bytecode.OpPutToArgumentsID:
			b := bytecode.DecodePutToArguments(in)
			(*f.ref(b.Arguments)).ObjectRef().internal.(*argumentsData).setMapped(int(b.Index), (*f.ref(b.Value)))

		// --- Scopes -------------------------------------------------------------------

		case bytecode.OpCreateLexicalEnvironmentID, bytecode.OpCreateGeneratorFrameEnvironmentID:
			b := bytecode.DecodeCreateLexicalEnvironment(in)
			table := (*f.ref(b.SymbolTable)).cell().(*bytecode.SymbolTable)
			f.set(b.Dst, cellValue(newLexicalScope((*f.ref(b.Scope)).scope(), table, (*f.ref(b.InitialValue)))))

		case bytecode.OpResolveScopeID:
			b := bytecode.DecodeResolveScope(in)
			scope := (*f.ref(b.Scope)).scope()
			switch b.ResolveType {
			case bytecode.ClosureVar, bytecode.ResolvedClosureVar:
				f.set(b.Dst, cellValue(skipScopes(scope, b.LocalScopeDepth)))
			default:
				if isGlobalResolveType(b.ResolveType) {
					scope = vm.resolveGlobalScope(&cb.resolveScopeCaches[b.MetadataID], cb.realm, scope, cb.identifier(b.Var))
					f.set(b.Dst, cellValue(scope))
					// Every read of a global variable, and of a variable of an
					// enclosing function (which the bytecompiler cannot see), is this
					// instruction followed by get_from_scope on its result: when that
					// instruction's cache hits, run both in one dispatch.
					if bytecode.OpcodeID(ins[next]) == bytecode.OpGetFromScopeID {
						g := bytecode.DecodeGetFromScope(ins[next:])
						if g.Scope == b.Dst && isGlobalResolveType(g.GetPutInfo.ResolveType()) {
							switch scope.kind {
							case scopeGlobalObject:
								o := scope.object
								if v, ok := cb.getFromScopeCaches[g.MetadataID].lookup(vm, o, ObjectValue(o)); ok {
									f.set(g.Dst, v)
									next += bytecode.OpGetFromScopeID.Length()
								}
							case scopeLexical:
								if c := &cb.getScopeSlotCaches[g.MetadataID]; scope.table == c.table {
									f.set(g.Dst, scope.slots[c.offset])
									next += bytecode.OpGetFromScopeID.Length()
								}
							case scopeGlobalLexical:
								// let, const and class of scripts; empty is the TDZ.
								if c := &cb.getScopeSlotCaches[g.MetadataID]; scope.table == c.table {
									if v := scope.slots[c.offset]; !v.IsEmpty() {
										f.set(g.Dst, v)
										next += bytecode.OpGetFromScopeID.Length()
									}
								}
							}
						}
					}
					break
				}
				f.set(b.Dst, cellValue(vm.resolveScope(scope, cb.identifier(b.Var))))
			}

		case bytecode.OpGetFromScopeID:
			b := bytecode.DecodeGetFromScope(in)
			scope := (*f.ref(b.Scope)).scope()
			switch b.GetPutInfo.ResolveType() {
			case bytecode.ClosureVar, bytecode.ResolvedClosureVar:
				f.set(b.Dst, scope.slots[b.Offset])
			default:
				if scope.kind == scopeGlobalObject && isGlobalResolveType(b.GetPutInfo.ResolveType()) {
					f.set(b.Dst, vm.getGlobalProperty(&cb.getFromScopeCaches[b.MetadataID], scope, f.key(b.Var), b.GetPutInfo.ResolveMode()))
					break
				}
				if scope.kind == scopeLexical || scope.kind == scopeGlobalLexical {
					// Global lexical bindings are TDZ-checked by getFromScope.
					if slot := cb.getScopeSlotCaches[b.MetadataID].slot(scope, cb.identifier(b.Var), false); slot != nil && !slot.IsEmpty() {
						f.set(b.Dst, *slot)
						break
					}
				}
				f.set(b.Dst, vm.getFromScope(scope, cb.identifier(b.Var), b.GetPutInfo.ResolveMode()))
			}

		case bytecode.OpPutToScopeID:
			b := bytecode.DecodePutToScope(in)
			scope := (*f.ref(b.Scope)).scope()
			switch b.GetPutInfo.ResolveType() {
			case bytecode.ClosureVar, bytecode.ResolvedClosureVar:
				scope.slots[b.Offset] = (*f.ref(b.Value))
			default:
				// On the global object, initializing a var (var x = ... at top
				// level) is an assignment as well.
				if scope.kind == scopeGlobalObject && isGlobalResolveType(b.GetPutInfo.ResolveType()) {
					vm.putGlobalProperty(&cb.putToScopeCaches[b.MetadataID], scope, f.key(b.Var), (*f.ref(b.Value)), b.GetPutInfo)
					break
				}
				if scope.kind == scopeLexical ||
					scope.kind == scopeGlobalLexical && !b.GetPutInfo.InitializationMode().IsInitialization() {
					if slot := cb.putScopeSlotCaches[b.MetadataID].slot(scope, cb.identifier(b.Var), true); slot != nil &&
						(scope.kind == scopeLexical || !slot.IsEmpty()) {
						*slot = (*f.ref(b.Value))
						break
					}
				}
				vm.putToScope(scope, cb.identifier(b.Var), (*f.ref(b.Value)), b.GetPutInfo)
			}

		// --- Property access ----------------------------------------------------------

		case bytecode.OpGetByIdID:
			b := bytecode.DecodeGetById(in)
			f.set(b.Dst, vm.getById(&cb.getByIdCaches[b.MetadataID], (*f.ref(b.Base)), f.key(b.Property)))

		case bytecode.OpGetLengthID:
			b := bytecode.DecodeGetLength(in)
			base := (*f.ref(b.Base))
			switch {
			case base.IsString():
				f.set(b.Dst, Int(base.StringRef().Length()))
			case base.IsObject() && base.ObjectRef().class == ClassArray:
				if a, ok := base.ObjectRef().exotic.(*arrayObject); ok {
					f.set(b.Dst, Number(float64(a.length)))
					break
				}
				fallthrough
			default:
				f.set(b.Dst, vm.getById(&cb.getLengthCaches[b.MetadataID], base, PropertyKey{name: lengthIdent}))
			}

		case bytecode.OpGetByIdDirectID:
			b := bytecode.DecodeGetByIdDirect(in)
			base := (*f.ref(b.Base))
			v := Undefined
			if o := base.AsObject(); o != nil {
				if p, ok := o.GetOwnProperty(f.key(b.Property)); ok {
					v = p.getValue(base)
				}
			}
			f.set(b.Dst, v)

		case bytecode.OpGetByValID:
			b := bytecode.DecodeGetByVal(in)
			cb.getByValProfiles[b.MetadataID] |= byValProfileOf(*f.ref(b.Base))
			v := vm.getByVal((*f.ref(b.Base)), (*f.ref(b.Property)))
			f.set(b.Dst, v)
			if v.IsNumber() {
				if dst, ok := f.orZero(ins[next:], b.Dst); ok {
					f.set(dst, int32Value(toInt32(v.num())))
					next += bytecode.OpBitorID.Length()
				}
			}

		case bytecode.OpPutByIdID:
			b := bytecode.DecodePutById(in)
			vm.putById(&cb.putByIdCaches[b.MetadataID], (*f.ref(b.Base)), f.key(b.Property), (*f.ref(b.Value)),
				b.Flags.IsDirect(), b.Flags.ECMAMode().IsStrict())

		case bytecode.OpPutByValID:
			b := bytecode.DecodePutByVal(in)
			cb.putByValProfiles[b.MetadataID] |= byValProfileOf(*f.ref(b.Base))
			vm.putByVal((*f.ref(b.Base)), (*f.ref(b.Property)), (*f.ref(b.Value)), b.EcmaMode.IsStrict())

		case bytecode.OpPutByValDirectID:
			b := bytecode.DecodePutByValDirect(in)
			vm.putDirect((*f.ref(b.Base)), vm.ToPropertyKey((*f.ref(b.Property))), (*f.ref(b.Value)))

		case bytecode.OpInByIdID:
			b := bytecode.DecodeInById(in)
			f.set(b.Dst, Bool(vm.inOperator((*f.ref(b.Base)), f.key(b.Property))))

		case bytecode.OpInByValID:
			b := bytecode.DecodeInByVal(in)
			base := (*f.ref(b.Base))
			if !base.IsObject() {
				vm.throwTypeError("Cannot use 'in' operator to search for a key in %s", vm.describeForError(base))
			}
			f.set(b.Dst, Bool(vm.inOperator(base, vm.ToPropertyKey((*f.ref(b.Property))))))

		case bytecode.OpDefineDataPropertyID:
			b := bytecode.DecodeDefineDataProperty(in)
			attrs := bytecode.DefinePropertyAttributes(vm.ToInt32((*f.ref(b.Attributes))))
			d := descriptorFromAttributes(attrs)
			d.Value, d.Has = (*f.ref(b.Value)), d.Has|HasValue
			(*f.ref(b.Base)).ObjectRef().DefinePropertyOrThrow(vm.ToPropertyKey((*f.ref(b.Property))), d)

		case bytecode.OpGetPrivateNameID:
			b := bytecode.DecodeGetPrivateName(in)
			f.set(b.Dst, vm.getPrivateName((*f.ref(b.Base)), (*f.ref(b.Property))))

		case bytecode.OpPutPrivateNameID:
			b := bytecode.DecodePutPrivateName(in)
			vm.putPrivateName((*f.ref(b.Base)), (*f.ref(b.Property)), (*f.ref(b.Value)), b.PutKind)

		case bytecode.OpToNumberID:
			b := bytecode.DecodeToNumber(in)
			v := (*f.ref(b.Operand))
			if !v.IsNumber() {
				v = Number(vm.ToNumber(v))
			}
			f.set(b.Dst, v)

		case bytecode.OpToNumericID:
			b := bytecode.DecodeToNumeric(in)
			f.set(b.Dst, vm.ToNumeric((*f.ref(b.Operand))))

		case bytecode.OpToStringID:
			b := bytecode.DecodeToString(in)
			f.set(b.Dst, StringValue(vm.ToString((*f.ref(b.Operand)))))

		case bytecode.OpToPrimitiveID:
			b := bytecode.DecodeToPrimitive(in)
			f.set(b.Dst, vm.ToPrimitive((*f.ref(b.Src)), HintDefault))

		case bytecode.OpToObjectID:
			b := bytecode.DecodeToObject(in)
			v := (*f.ref(b.Operand))
			if v.IsNullish() {
				vm.throwTypeError("%s", cb.identifier(b.Message).String())
			}
			f.set(b.Dst, ObjectValue(vm.ToObject(v)))

		case bytecode.OpStrcatID:
			b := bytecode.DecodeStrcat(in)
			s := emptyString
			for i := 0; i < int(b.Count); i++ {
				s = Concat(s, vm.ToString(f.getChecked(b.Src-bytecode.VirtualRegister(i))))
			}
			f.set(b.Dst, StringValue(s))

		case bytecode.OpTypeofID:
			b := bytecode.DecodeTypeof(in)
			f.set(b.Dst, StringValue(typeofStrings[(*f.ref(b.Value)).TypeOf()]))

		case bytecode.OpTypeofIsUndefinedID:
			b := bytecode.DecodeTypeofIsUndefined(in)
			f.set(b.Dst, Bool((*f.ref(b.Operand)).TypeOf() == "undefined"))

		case bytecode.OpTypeofIsObjectID:
			b := bytecode.DecodeTypeofIsObject(in)
			v := (*f.ref(b.Operand))
			f.set(b.Dst, Bool(v.IsNull() || v.IsObject() && !v.ObjectRef().IsCallable() && !v.ObjectRef().htmlDDA))

		case bytecode.OpTypeofIsFunctionID:
			b := bytecode.DecodeTypeofIsFunction(in)
			f.set(b.Dst, Bool((*f.ref(b.Operand)).TypeOf() == "function"))

		case bytecode.OpIsUndefinedOrNullID:
			b := bytecode.DecodeIsUndefinedOrNull(in)
			f.set(b.Dst, Bool((*f.ref(b.Operand)).IsNullish()))

		case bytecode.OpIsObjectID:
			b := bytecode.DecodeIsObject(in)
			f.set(b.Dst, Bool((*f.ref(b.Operand)).IsObject()))

		case bytecode.OpIsCallableID:
			b := bytecode.DecodeIsCallable(in)
			f.set(b.Dst, Bool((*f.ref(b.Operand)).IsCallable()))

		case bytecode.OpIsEmptyID:
			b := bytecode.DecodeIsEmpty(in)
			f.set(b.Dst, Bool((*f.ref(b.Operand)).IsEmpty()))

		case bytecode.OpIsCellWithTypeID:
			b := bytecode.DecodeIsCellWithType(in)
			f.set(b.Dst, Bool(isCellWithType((*f.ref(b.Operand)), b.Type)))

		case bytecode.OpEqNullID:
			b := bytecode.DecodeEqNull(in)
			f.set(b.Dst, Bool((*f.ref(b.Operand)).isNullishOrHTMLDDA()))

		case bytecode.OpNeqNullID:
			b := bytecode.DecodeNeqNull(in)
			f.set(b.Dst, Bool(!(*f.ref(b.Operand)).isNullishOrHTMLDDA()))

		case bytecode.OpNotID:
			b := bytecode.DecodeNot(in)
			f.set(b.Dst, Bool(!(*f.ref(b.Operand)).ToBoolean()))

		// --- Arithmetic ---------------------------------------------------------------

		case bytecode.OpAddID:
			b := bytecode.DecodeAdd(in)
			if l, r := (*f.ref(b.Lhs)), (*f.ref(b.Rhs)); l.IsNumber() && r.IsNumber() {
				sum := l.num() + r.num()
				f.set(b.Dst, Number(sum))
				if dst, ok := f.orZero(ins[next:], b.Dst); ok {
					f.set(dst, int32Value(toInt32(sum)))
					next += bytecode.OpBitorID.Length()
				}
			} else {
				f.set(b.Dst, vm.Add(l, r))
			}
		case bytecode.OpSubID:
			b := bytecode.DecodeSub(in)
			if l, r := (*f.ref(b.Lhs)), (*f.ref(b.Rhs)); l.IsNumber() && r.IsNumber() {
				diff := l.num() - r.num()
				f.set(b.Dst, Number(diff))
				if dst, ok := f.orZero(ins[next:], b.Dst); ok {
					f.set(dst, int32Value(toInt32(diff)))
					next += bytecode.OpBitorID.Length()
				}
			} else {
				f.set(b.Dst, vm.Arith(opSub, l, r))
			}
		case bytecode.OpMulID:
			b := bytecode.DecodeMul(in)
			if l, r := (*f.ref(b.Lhs)), (*f.ref(b.Rhs)); l.IsNumber() && r.IsNumber() {
				f.set(b.Dst, Number(l.num()*r.num()))
			} else {
				f.set(b.Dst, vm.Arith(opMul, l, r))
			}
		case bytecode.OpDivID:
			b := bytecode.DecodeDiv(in)
			if l, r := (*f.ref(b.Lhs)), (*f.ref(b.Rhs)); l.IsNumber() && r.IsNumber() {
				f.set(b.Dst, Number(l.num()/r.num()))
			} else {
				f.set(b.Dst, vm.Arith(opDiv, l, r))
			}
		case bytecode.OpModID:
			b := bytecode.DecodeMod(in)
			f.set(b.Dst, vm.Arith(opMod, (*f.ref(b.Lhs)), (*f.ref(b.Rhs))))
		case bytecode.OpLshiftID:
			b := bytecode.DecodeLshift(in)
			if l, r := (*f.ref(b.Lhs)), (*f.ref(b.Rhs)); l.IsNumber() && r.IsNumber() {
				f.set(b.Dst, int32Value(toInt32(l.num())<<(uint32(toInt32(r.num()))&31)))
			} else {
				f.set(b.Dst, vm.Arith(opLshift, l, r))
			}
		case bytecode.OpRshiftID:
			b := bytecode.DecodeRshift(in)
			if l, r := (*f.ref(b.Lhs)), (*f.ref(b.Rhs)); l.IsNumber() && r.IsNumber() {
				i := toInt32(l.num()) >> (uint32(toInt32(r.num())) & 31)
				f.set(b.Dst, int32Value(i))
				// asm.js reads memory as HEAP32[p >> 2]: run the typed array
				// read in the same dispatch.
				if bytecode.OpcodeID(ins[next]) == bytecode.OpGetByValID && i >= 0 {
					g := bytecode.DecodeGetByVal(ins[next:])
					if g.Property == b.Dst {
						if o := (*f.ref(g.Base)).AsObject(); o != nil && o.class == ClassTypedArray {
							if v, ok := typedArrayOf(o).fastGet(uint32(i)); ok {
								f.set(g.Dst, v)
								next += bytecode.OpGetByValID.Length()
							}
						}
					}
				}
			} else {
				f.set(b.Dst, vm.Arith(opRshift, l, r))
			}
		case bytecode.OpUrshiftID:
			b := bytecode.DecodeUrshift(in)
			if l, r := (*f.ref(b.Lhs)), (*f.ref(b.Rhs)); l.IsNumber() && r.IsNumber() {
				f.set(b.Dst, Number(float64(uint32(toInt32(l.num()))>>(uint32(toInt32(r.num()))&31))))
			} else {
				f.set(b.Dst, vm.Arith(opUrshift, l, r))
			}
		case bytecode.OpBitandID:
			b := bytecode.DecodeBitand(in)
			if l, r := (*f.ref(b.Lhs)), (*f.ref(b.Rhs)); l.IsNumber() && r.IsNumber() {
				f.set(b.Dst, int32Value(toInt32(l.num())&toInt32(r.num())))
			} else {
				f.set(b.Dst, vm.Arith(opBitand, l, r))
			}
		case bytecode.OpBitorID:
			b := bytecode.DecodeBitor(in)
			if l, r := (*f.ref(b.Lhs)), (*f.ref(b.Rhs)); l.IsNumber() && r.IsNumber() {
				f.set(b.Dst, int32Value(toInt32(l.num())|toInt32(r.num())))
			} else {
				f.set(b.Dst, vm.Arith(opBitor, l, r))
			}
		case bytecode.OpBitxorID:
			b := bytecode.DecodeBitxor(in)
			if l, r := (*f.ref(b.Lhs)), (*f.ref(b.Rhs)); l.IsNumber() && r.IsNumber() {
				f.set(b.Dst, int32Value(toInt32(l.num())^toInt32(r.num())))
			} else {
				f.set(b.Dst, vm.Arith(opBitxor, l, r))
			}
		case bytecode.OpUnsignedID:
			b := bytecode.DecodeUnsigned(in)
			f.set(b.Dst, Number(float64(uint32(vm.ToInt32((*f.ref(b.Operand)))))))
		case bytecode.OpNegateID:
			b := bytecode.DecodeNegate(in)
			f.set(b.Dst, vm.Negate((*f.ref(b.Operand))))
		case bytecode.OpBitnotID:
			b := bytecode.DecodeBitnot(in)
			f.set(b.Dst, vm.BitNot((*f.ref(b.Operand))))
		case bytecode.OpIncID:
			b := bytecode.DecodeInc(in)
			f.set(b.SrcDst, vm.Increment((*f.ref(b.SrcDst)), 1))
		case bytecode.OpDecID:
			b := bytecode.DecodeDec(in)
			f.set(b.SrcDst, vm.Increment((*f.ref(b.SrcDst)), -1))

		// --- Comparison ---------------------------------------------------------------

		case bytecode.OpEqID:
			b := bytecode.DecodeEq(in)
			f.set(b.Dst, Bool(vm.IsLooselyEqual((*f.ref(b.Lhs)), (*f.ref(b.Rhs)))))
		case bytecode.OpNeqID:
			b := bytecode.DecodeNeq(in)
			f.set(b.Dst, Bool(!vm.IsLooselyEqual((*f.ref(b.Lhs)), (*f.ref(b.Rhs)))))
		case bytecode.OpStricteqID:
			b := bytecode.DecodeStricteq(in)
			f.set(b.Dst, Bool(StrictEquals((*f.ref(b.Lhs)), (*f.ref(b.Rhs)))))
		case bytecode.OpNstricteqID:
			b := bytecode.DecodeNstricteq(in)
			f.set(b.Dst, Bool(!StrictEquals((*f.ref(b.Lhs)), (*f.ref(b.Rhs)))))
		case bytecode.OpLessID:
			b := bytecode.DecodeLess(in)
			f.set(b.Dst, Bool(vm.Less((*f.ref(b.Lhs)), (*f.ref(b.Rhs)))))
		case bytecode.OpLesseqID:
			b := bytecode.DecodeLesseq(in)
			f.set(b.Dst, Bool(vm.LessEq((*f.ref(b.Lhs)), (*f.ref(b.Rhs)))))
		case bytecode.OpGreaterID:
			b := bytecode.DecodeGreater(in)
			f.set(b.Dst, Bool(vm.Greater((*f.ref(b.Lhs)), (*f.ref(b.Rhs)))))
		case bytecode.OpGreatereqID:
			b := bytecode.DecodeGreatereq(in)
			f.set(b.Dst, Bool(vm.GreaterEq((*f.ref(b.Lhs)), (*f.ref(b.Rhs)))))
		case bytecode.OpBelowID:
			b := bytecode.DecodeBelow(in)
			f.set(b.Dst, Bool(uint32(vm.ToInt32((*f.ref(b.Lhs)))) < uint32(vm.ToInt32((*f.ref(b.Rhs))))))
		case bytecode.OpBeloweqID:
			b := bytecode.DecodeBeloweq(in)
			f.set(b.Dst, Bool(uint32(vm.ToInt32((*f.ref(b.Lhs)))) <= uint32(vm.ToInt32((*f.ref(b.Rhs))))))

		case bytecode.OpInstanceofID:
			b := bytecode.DecodeInstanceof(in)
			f.set(b.Dst, Bool(vm.InstanceOf((*f.ref(b.Value)), (*f.ref(b.Constructor)))))

		// --- Jumps --------------------------------------------------------------------

		case bytecode.OpJmpID:
			next = pc + int(bytecode.DecodeJmp(in).TargetLabel)
		case bytecode.OpJtrueID:
			b := bytecode.DecodeJtrue(in)
			if (*f.ref(b.Condition)).ToBoolean() {
				next = pc + int(b.TargetLabel)
			}
		case bytecode.OpJfalseID:
			b := bytecode.DecodeJfalse(in)
			if !(*f.ref(b.Condition)).ToBoolean() {
				next = pc + int(b.TargetLabel)
			}
		case bytecode.OpJeqNullID:
			b := bytecode.DecodeJeqNull(in)
			if (*f.ref(b.Value)).isNullishOrHTMLDDA() {
				next = pc + int(b.TargetLabel)
			}
		case bytecode.OpJneqNullID:
			b := bytecode.DecodeJneqNull(in)
			if !(*f.ref(b.Value)).isNullishOrHTMLDDA() {
				next = pc + int(b.TargetLabel)
			}
		case bytecode.OpJundefinedOrNullID:
			b := bytecode.DecodeJundefinedOrNull(in)
			if (*f.ref(b.Value)).IsNullish() {
				next = pc + int(b.TargetLabel)
			}
		case bytecode.OpJnundefinedOrNullID:
			b := bytecode.DecodeJnundefinedOrNull(in)
			if !(*f.ref(b.Value)).IsNullish() {
				next = pc + int(b.TargetLabel)
			}
		case bytecode.OpJeqPtrID:
			b := bytecode.DecodeJeqPtr(in)
			if sameCell((*f.ref(b.Value)), (*f.ref(b.SpecialPointer))) {
				next = pc + int(b.TargetLabel)
			}
		case bytecode.OpJneqPtrID:
			b := bytecode.DecodeJneqPtr(in)
			if !sameCell((*f.ref(b.Value)), (*f.ref(b.SpecialPointer))) {
				next = pc + int(b.TargetLabel)
			}
		case bytecode.OpJeqID:
			b := bytecode.DecodeJeq(in)
			if vm.IsLooselyEqual((*f.ref(b.Lhs)), (*f.ref(b.Rhs))) {
				next = pc + int(b.TargetLabel)
			}
		case bytecode.OpJneqID:
			b := bytecode.DecodeJneq(in)
			if !vm.IsLooselyEqual((*f.ref(b.Lhs)), (*f.ref(b.Rhs))) {
				next = pc + int(b.TargetLabel)
			}
		case bytecode.OpJstricteqID:
			b := bytecode.DecodeJstricteq(in)
			if StrictEquals((*f.ref(b.Lhs)), (*f.ref(b.Rhs))) {
				next = pc + int(b.TargetLabel)
			}
		case bytecode.OpJnstricteqID:
			b := bytecode.DecodeJnstricteq(in)
			if !StrictEquals((*f.ref(b.Lhs)), (*f.ref(b.Rhs))) {
				next = pc + int(b.TargetLabel)
			}
		case bytecode.OpJlessID:
			b := bytecode.DecodeJless(in)
			if vm.Less((*f.ref(b.Lhs)), (*f.ref(b.Rhs))) {
				next = pc + int(b.TargetLabel)
			}
		case bytecode.OpJlesseqID:
			b := bytecode.DecodeJlesseq(in)
			if vm.LessEq((*f.ref(b.Lhs)), (*f.ref(b.Rhs))) {
				next = pc + int(b.TargetLabel)
			}
		case bytecode.OpJgreaterID:
			b := bytecode.DecodeJgreater(in)
			if vm.Greater((*f.ref(b.Lhs)), (*f.ref(b.Rhs))) {
				next = pc + int(b.TargetLabel)
			}
		case bytecode.OpJgreatereqID:
			b := bytecode.DecodeJgreatereq(in)
			if vm.GreaterEq((*f.ref(b.Lhs)), (*f.ref(b.Rhs))) {
				next = pc + int(b.TargetLabel)
			}
		case bytecode.OpJnlessID:
			b := bytecode.DecodeJnless(in)
			if !vm.Less((*f.ref(b.Lhs)), (*f.ref(b.Rhs))) {
				next = pc + int(b.TargetLabel)
			}
		case bytecode.OpJnlesseqID:
			b := bytecode.DecodeJnlesseq(in)
			if !vm.LessEq((*f.ref(b.Lhs)), (*f.ref(b.Rhs))) {
				next = pc + int(b.TargetLabel)
			}
		case bytecode.OpJngreaterID:
			b := bytecode.DecodeJngreater(in)
			if !vm.Greater((*f.ref(b.Lhs)), (*f.ref(b.Rhs))) {
				next = pc + int(b.TargetLabel)
			}
		case bytecode.OpJngreatereqID:
			b := bytecode.DecodeJngreatereq(in)
			if !vm.GreaterEq((*f.ref(b.Lhs)), (*f.ref(b.Rhs))) {
				next = pc + int(b.TargetLabel)
			}
		case bytecode.OpJbelowID:
			b := bytecode.DecodeJbelow(in)
			if uint32(vm.ToInt32((*f.ref(b.Lhs)))) < uint32(vm.ToInt32((*f.ref(b.Rhs)))) {
				next = pc + int(b.TargetLabel)
			}
		case bytecode.OpJbeloweqID:
			b := bytecode.DecodeJbeloweq(in)
			if uint32(vm.ToInt32((*f.ref(b.Lhs)))) <= uint32(vm.ToInt32((*f.ref(b.Rhs)))) {
				next = pc + int(b.TargetLabel)
			}

		case bytecode.OpSwitchImmID:
			b := bytecode.DecodeSwitchImm(in)
			table := &cb.u.SwitchJumpTables[b.TableIndex]
			v := (*f.ref(b.Scrutinee))
			offset := table.DefaultOffset
			if v.IsNumber() {
				if i := int32(v.num()); float64(i) == v.num() {
					offset = table.OffsetForValue(i)
				}
			}
			next = pc + int(offset)

		case bytecode.OpSwitchCharID:
			b := bytecode.DecodeSwitchChar(in)
			table := &cb.u.SwitchJumpTables[b.TableIndex]
			v := (*f.ref(b.Scrutinee))
			offset := table.DefaultOffset
			if v.IsString() && v.StringRef().Length() == 1 {
				offset = table.OffsetForValue(int32(v.StringRef().At(0)))
			}
			next = pc + int(offset)

		case bytecode.OpSwitchStringID:
			b := bytecode.DecodeSwitchString(in)
			table := &cb.u.StringSwitchJumpTables[b.TableIndex]
			v := (*f.ref(b.Scrutinee))
			offset := table.DefaultOffset
			if v.IsString() {
				offset = table.OffsetForValue(v.StringRef().String())
			}
			next = pc + int(offset)

		// --- Calls --------------------------------------------------------------------

		case bytecode.OpCallID:
			b := bytecode.DecodeCall(in)
			cb.callProfiles[b.MetadataID].note(*f.ref(b.Callee))
			this, args := f.callArgs(b.Argv, b.Argc)
			f.set(b.Dst, vm.callValue((*f.ref(b.Callee)), this, args))

		case bytecode.OpCallIgnoreResultID:
			b := bytecode.DecodeCallIgnoreResult(in)
			cb.callIgnoreProfiles[b.MetadataID].note(*f.ref(b.Callee))
			this, args := f.callArgs(b.Argv, b.Argc)
			vm.callValue((*f.ref(b.Callee)), this, args)

		case bytecode.OpTailCallID:
			b := bytecode.DecodeTailCall(in)
			cb.tailCallProfiles[b.MetadataID].note(*f.ref(b.Callee))
			this, args := f.callArgs(b.Argv, b.Argc)
			return vm.tailCall((*f.ref(b.Callee)), this, args)

		case bytecode.OpCallVarargsID:
			b := bytecode.DecodeCallVarargs(in)
			args := vm.varargs(f, (*f.ref(b.Arguments)), b.FirstVarArg)
			f.set(b.Dst, vm.callValue((*f.ref(b.Callee)), thisForCall((*f.ref(b.ThisValue))), args))

		case bytecode.OpTailCallVarargsID:
			b := bytecode.DecodeTailCallVarargs(in)
			// (A tail call frees this frame first: no slice of it.)
			args := slices.Clone(vm.varargs(f, (*f.ref(b.Arguments)), b.FirstVarArg))
			return vm.tailCall((*f.ref(b.Callee)), thisForCall((*f.ref(b.ThisValue))), args)

		case bytecode.OpConstructID:
			b := bytecode.DecodeConstruct(in)
			newTarget, args := f.callArgs(b.Argv, b.Argc)
			f.set(b.Dst, vm.constructValue((*f.ref(b.Callee)), args, newTarget.AsObject()))

		case bytecode.OpConstructVarargsID:
			b := bytecode.DecodeConstructVarargs(in)
			args := vm.varargs(f, (*f.ref(b.Arguments)), b.FirstVarArg)
			f.set(b.Dst, vm.constructValue((*f.ref(b.Callee)), args, (*f.ref(b.ThisValue)).AsObject()))

		case bytecode.OpIteratorOpenID:
			b := bytecode.DecodeIteratorOpen(in)
			if fastArrayIterable(cb.realm, (*f.ref(b.Iterable)), (*f.ref(b.SymbolIterator))) {
				f.set(b.Iterator, ObjectValue(cb.realm.fastArraySentinel))
				f.set(b.Next, Int(0))
				break
			}
			iterator := vm.callValue((*f.ref(b.SymbolIterator)), (*f.ref(b.Iterable)), nil)
			if !iterator.IsObject() {
				vm.throwTypeError("Iterator result interface is not an object.")
			}
			f.set(b.Iterator, iterator)
			f.set(b.Next, iterator.ObjectRef().GetKey(nextKey))

		case bytecode.OpIteratorNextID:
			b := bytecode.DecodeIteratorNext(in)
			if (*f.ref(b.Iterator)).AsObject() == cb.realm.fastArraySentinel {
				v, index, done := fastArrayNext((*f.ref(b.Iterable)).ObjectRef(), (*f.ref(b.Next)))
				f.set(b.Done, Bool(done))
				if !done {
					f.set(b.Value, v)
					f.set(b.Next, index)
				}
				break
			}
			result := vm.callValue((*f.ref(b.Next)), (*f.ref(b.Iterator)), nil)
			ro := result.AsObject()
			if ro == nil {
				vm.throwTypeError("Iterator result interface is not an object.")
			}
			done := ro.GetKey(doneKey)
			f.set(b.Done, done)
			if !done.ToBoolean() {
				f.set(b.Value, ro.GetKey(valueKey))
			}

		case bytecode.OpGetPropertyEnumeratorID:
			b := bytecode.DecodeGetPropertyEnumerator(in)
			f.set(b.Dst, vm.propertyEnumerator((*f.ref(b.Base))))

		case bytecode.OpEnumeratorNextID:
			b := bytecode.DecodeEnumeratorNext(in)
			e := (*f.ref(b.Enumerator)).cell().(*propertyNameEnumerator)
			f.set(b.PropertyName, e.next(r))

		case bytecode.OpEnumeratorGetByValID:
			b := bytecode.DecodeEnumeratorGetByVal(in)
			f.set(b.Dst, vm.getByVal((*f.ref(b.Base)), (*f.ref(b.PropertyName))))

		case bytecode.OpEnumeratorInByValID:
			b := bytecode.DecodeEnumeratorInByVal(in)
			f.set(b.Dst, Bool(vm.inOperator((*f.ref(b.Base)), vm.ToPropertyKey((*f.ref(b.PropertyName))))))

		case bytecode.OpEnumeratorHasOwnPropertyID:
			b := bytecode.DecodeEnumeratorHasOwnProperty(in)
			o := vm.ToObject((*f.ref(b.Base)))
			f.set(b.Dst, Bool(o.HasOwnProperty(vm.ToPropertyKey((*f.ref(b.PropertyName))))))

		case bytecode.OpCreatePromiseID:
			b := bytecode.DecodeCreatePromise(in)
			proto := vm.GetPrototypeFromConstructor((*f.ref(b.Callee)).ObjectRef(), func(r *Realm) *Object { return r.PromisePrototype })
			p := r.newPromise()
			p.proto = proto
			f.set(b.Dst, ObjectValue(p))

		default:
			vm.runCold(f, op, in)
		}
		f.pc = next
	}
}

// runCold executes the instructions that are neither hot nor change the
// control flow. Keeping them out of run keeps run small enough for the Go
// compiler to inline the operand decoders and register accessors into it.
func (vm *VM) runCold(f *Frame, op bytecode.OpcodeID, in []uint32) {
	cb := f.cb
	r := cb.realm
	switch op {
	case bytecode.OpNopID, bytecode.OpDebugID,
		bytecode.OpProfileTypeID, bytecode.OpProfileControlFlowID, bytecode.OpLogShadowChickenPrologueID,
		bytecode.OpLogShadowChickenTailID, bytecode.OpIdentityWithProfileID, bytecode.OpSuperSamplerBeginID,
		bytecode.OpSuperSamplerEndID:

	case bytecode.OpThrowID:
		b := bytecode.DecodeThrow(in)
		v := f.get(b.Value)
		if exc, ok := v.cell().(*Exception); ok && v.Kind() == KindCell {
			panic(exc) // rethrow of an op_catch exception register
		}
		panic(&Exception{Value: v})

	case bytecode.OpThrowStaticErrorID:
		b := bytecode.DecodeThrowStaticError(in)
		msg := f.get(b.Message)
		text := ""
		if msg.IsString() {
			text = msg.StringRef().String()
		}
		panic(&Exception{Value: ObjectValue(vm.NewError(ErrorKind(b.ErrorType), text))})

	case bytecode.OpCatchID:
		b := bytecode.DecodeCatch(in)
		exc := f.exception
		f.exception = nil
		f.set(b.Exception, cellValue(exc))
		f.set(b.ThrownValue, exc.Value)

	case bytecode.OpUnreachableID:
		panic("vm: reached op_unreachable")

	case bytecode.OpYieldID:
		panic("vm: op_yield after generatorification")

	// --- Constants and object creation -------------------------------------------

	case bytecode.OpNewArrayWithSpreadID:
		b := bytecode.DecodeNewArrayWithSpread(in)
		bits := cb.u.BitVectors[b.BitVector]
		var values []Value
		for i := 0; i < int(b.Argc); i++ {
			v := f.getChecked(b.Argv - bytecode.VirtualRegister(i))
			if bits[i] {
				values = append(values, v.cell().(*spreadList).values...)
			} else {
				values = append(values, v)
			}
		}
		f.set(b.Dst, ObjectValue(r.NewArray(values...)))

	case bytecode.OpSpreadID:
		b := bytecode.DecodeSpread(in)
		f.set(b.Dst, cellValue(&spreadList{vm.iterableToList(f.get(b.Argument))}))

	case bytecode.OpNewArrayWithSpeciesID:
		b := bytecode.DecodeNewArrayWithSpecies(in)
		f.set(b.Dst, ObjectValue(vm.ArraySpeciesCreate(f.get(b.Array).AsObject(), int64(vm.ToNumber(f.get(b.Length))))))

	case bytecode.OpNewRegExpID:
		b := bytecode.DecodeNewRegExp(in)
		re := f.get(b.Regexp).cell().(*bytecode.RegExp)
		f.set(b.Dst, ObjectValue(vm.newRegExpFromLiteral(re)))

	case bytecode.OpSetFunctionNameID:
		b := bytecode.DecodeSetFunctionName(in)
		vm.setFunctionName(f.get(b.Function).ObjectRef(), f.get(b.Name))

	case bytecode.OpCreateScopedArgumentsID:
		b := bytecode.DecodeCreateScopedArguments(in)
		f.set(b.Dst, ObjectValue(vm.createScopedArguments(f, f.get(b.Scope).scope())))

	case bytecode.OpCreateClonedArgumentsID:
		b := bytecode.DecodeCreateClonedArguments(in)
		f.set(b.Dst, ObjectValue(vm.createClonedArguments(f)))

	case bytecode.OpGetParentScopeID:
		b := bytecode.DecodeGetParentScope(in)
		f.set(b.Dst, cellValue(f.get(b.Scope).scope().parent))

	case bytecode.OpPushWithScopeID:
		b := bytecode.DecodePushWithScope(in)
		f.set(b.Dst, cellValue(vm.newWithScope(f.get(b.CurrentScope).scope(), f.get(b.NewScope))))

	case bytecode.OpResolveScopeForHoistingFuncDeclInEvalID:
		b := bytecode.DecodeResolveScopeForHoistingFuncDeclInEval(in)
		f.set(b.Dst, vm.resolveScopeForHoistingFuncDeclInEval(f.get(b.Scope).scope(), cb.identifier(b.Property)))

	case bytecode.OpGetByIdWithThisID:
		b := bytecode.DecodeGetByIdWithThis(in)
		f.set(b.Dst, vm.getWithThis(f.get(b.Base), f.key(b.Property), f.get(b.ThisValue)))

	case bytecode.OpGetByValWithThisID:
		b := bytecode.DecodeGetByValWithThis(in)
		base := f.get(b.Base)
		vm.requireObjectCoercible(base, f.get(b.Property))
		f.set(b.Dst, vm.getWithThis(base, vm.ToPropertyKey(f.get(b.Property)), f.get(b.ThisValue)))

	case bytecode.OpPutByIdWithThisID:
		b := bytecode.DecodePutByIdWithThis(in)
		vm.putWithThis(f.get(b.Base), f.key(b.Property), f.get(b.Value), f.get(b.ThisValue), b.EcmaMode.IsStrict())

	case bytecode.OpPutByValWithThisID:
		b := bytecode.DecodePutByValWithThis(in)
		k := vm.ToPropertyKey(f.get(b.Property))
		vm.putWithThis(f.get(b.Base), k, f.get(b.Value), f.get(b.ThisValue), b.EcmaMode.IsStrict())

	case bytecode.OpDelByIdID:
		b := bytecode.DecodeDelById(in)
		f.set(b.Dst, Bool(vm.deleteProperty(f.get(b.Base), f.key(b.Property), b.EcmaMode.IsStrict())))

	case bytecode.OpDelByValID:
		b := bytecode.DecodeDelByVal(in)
		base := f.get(b.Base)
		obj := vm.toObjectForAccess(base, f.get(b.Property))
		k := vm.ToPropertyKey(f.get(b.Property))
		f.set(b.Dst, Bool(vm.deleteFromObject(obj, k, b.EcmaMode.IsStrict())))

	case bytecode.OpPutGetterByIdID:
		b := bytecode.DecodePutGetterById(in)
		vm.putAccessor(f.get(b.Base), f.key(b.Property), b.Attributes, f.get(b.Accessor).AsObject(), nil, true, false)

	case bytecode.OpPutSetterByIdID:
		b := bytecode.DecodePutSetterById(in)
		vm.putAccessor(f.get(b.Base), f.key(b.Property), b.Attributes, nil, f.get(b.Accessor).AsObject(), false, true)

	case bytecode.OpPutGetterSetterByIdID:
		b := bytecode.DecodePutGetterSetterById(in)
		vm.putAccessor(f.get(b.Base), f.key(b.Property), b.Attributes, f.get(b.Getter).AsObject(), f.get(b.Setter).AsObject(), true, true)

	case bytecode.OpPutGetterByValID:
		b := bytecode.DecodePutGetterByVal(in)
		vm.putAccessor(f.get(b.Base), vm.ToPropertyKey(f.get(b.Property)), b.Attributes, f.get(b.Accessor).AsObject(), nil, true, false)

	case bytecode.OpPutSetterByValID:
		b := bytecode.DecodePutSetterByVal(in)
		vm.putAccessor(f.get(b.Base), vm.ToPropertyKey(f.get(b.Property)), b.Attributes, nil, f.get(b.Accessor).AsObject(), false, true)

	case bytecode.OpDefineAccessorPropertyID:
		b := bytecode.DecodeDefineAccessorProperty(in)
		attrs := bytecode.DefinePropertyAttributes(vm.ToInt32(f.get(b.Attributes)))
		d := descriptorFromAttributes(attrs)
		if attrs.HasGet() {
			d.Get, d.Has = f.get(b.Getter).AsObject(), d.Has|HasGet
		}
		if attrs.HasSet() {
			d.Set, d.Has = f.get(b.Setter).AsObject(), d.Has|HasSet
		}
		f.get(b.Base).ObjectRef().DefinePropertyOrThrow(vm.ToPropertyKey(f.get(b.Property)), d)

	case bytecode.OpGetPrototypeOfID:
		b := bytecode.DecodeGetPrototypeOf(in)
		v := f.get(b.Value)
		o := v.AsObject()
		if o == nil {
			o = vm.ToObject(v)
		}
		f.set(b.Dst, ObjectValue(o.GetPrototypeOf()))

	case bytecode.OpGetInternalFieldID:
		b := bytecode.DecodeGetInternalField(in)
		f.set(b.Dst, f.get(b.Base).ObjectRef().extra.fields[b.Index])

	case bytecode.OpPutInternalFieldID:
		b := bytecode.DecodePutInternalField(in)
		f.get(b.Base).ObjectRef().extra.fields[b.Index] = f.get(b.Value)

	// --- Private names ------------------------------------------------------------

	case bytecode.OpHasPrivateNameID:
		b := bytecode.DecodeHasPrivateName(in)
		base := f.get(b.Base)
		if !base.IsObject() {
			vm.throwTypeError("Cannot use 'in' operator to search for a private field in %s", vm.describeForError(base))
		}
		f.set(b.Dst, Bool(base.ObjectRef().props.has(SymKey(f.get(b.Property).SymbolRef()))))

	case bytecode.OpSetPrivateBrandID:
		b := bytecode.DecodeSetPrivateBrand(in)
		o := f.get(b.Base).AsObject()
		k := SymKey(f.get(b.Brand).SymbolRef())
		if o.props.has(k) {
			vm.throwTypeError("Cannot install same private methods on object more than once.")
		}
		if !o.extensible {
			vm.throwTypeError("Cannot install private methods on a non-extensible object")
		}
		o.setOwn(k, dataProperty(Undefined, 0))

	case bytecode.OpCheckPrivateBrandID:
		b := bytecode.DecodeCheckPrivateBrand(in)
		o := f.get(b.Base).AsObject()
		if o == nil || !o.props.has(SymKey(f.get(b.Brand).SymbolRef())) {
			vm.throwTypeError("Cannot access private method or accessor on an object that does not have it.")
		}

	case bytecode.OpHasPrivateBrandID:
		b := bytecode.DecodeHasPrivateBrand(in)
		base := f.get(b.Base)
		if !base.IsObject() {
			vm.throwTypeError("Cannot use 'in' operator to search for a private field in %s", vm.describeForError(base))
		}
		f.set(b.Dst, Bool(base.ObjectRef().props.has(SymKey(f.get(b.Brand).SymbolRef()))))

	// --- Conversions and type tests -----------------------------------------------

	case bytecode.OpToPropertyKeyID:
		b := bytecode.DecodeToPropertyKey(in)
		v := f.get(b.Src)
		if !v.IsString() && !v.IsSymbol() {
			v = vm.ToPropertyKey(v).Value()
		}
		f.set(b.Dst, v)

	case bytecode.OpToPropertyKeyOrNumberID:
		b := bytecode.DecodeToPropertyKeyOrNumber(in)
		v := f.get(b.Src)
		if !v.IsString() && !v.IsSymbol() && !v.IsNumber() {
			v = vm.ToPropertyKey(v).Value()
		}
		f.set(b.Dst, v)

	case bytecode.OpIsBooleanID:
		b := bytecode.DecodeIsBoolean(in)
		f.set(b.Dst, Bool(f.get(b.Operand).IsBool()))

	case bytecode.OpIsNumberID:
		b := bytecode.DecodeIsNumber(in)
		f.set(b.Dst, Bool(f.get(b.Operand).IsNumber()))

	case bytecode.OpIsBigIntID:
		b := bytecode.DecodeIsBigInt(in)
		f.set(b.Dst, Bool(f.get(b.Operand).IsBigInt()))

	case bytecode.OpIsConstructorID:
		b := bytecode.DecodeIsConstructor(in)
		f.set(b.Dst, Bool(f.get(b.Operand).IsConstructor()))

	case bytecode.OpHasStructureWithFlagsID:
		b := bytecode.DecodeHasStructureWithFlags(in)
		f.set(b.Dst, False)

	case bytecode.OpPowID:
		b := bytecode.DecodePow(in)
		f.set(b.Dst, vm.Arith(opPow, f.get(b.Lhs), f.get(b.Rhs)))
	case bytecode.OpCallDirectEvalID:
		b := bytecode.DecodeCallDirectEval(in)
		this, args := f.callArgs(b.Argv, b.Argc)
		callee := f.get(b.Callee)
		if callee.AsObject() == r.evalFunction {
			f.set(b.Dst, vm.directEval(f, copyArgs(args), f.get(b.ThisValue), f.get(b.Scope).scope(),
				parser.LexicallyScopedFeatures(b.LexicallyScopedFeatures)))
		} else {
			f.set(b.Dst, vm.callValue(callee, this, args))
		}

	case bytecode.OpSuperConstructID:
		b := bytecode.DecodeSuperConstruct(in)
		newTarget, args := f.callArgs(b.Argv, b.Argc)
		callee := f.get(b.Callee)
		if !callee.IsConstructor() {
			vm.throwTypeError("Super constructor is not a constructor")
		}
		f.set(b.Dst, vm.Construct(callee.ObjectRef(), args, newTarget.AsObject()))

	case bytecode.OpSuperConstructVarargsID:
		b := bytecode.DecodeSuperConstructVarargs(in)
		args := vm.varargs(f, f.get(b.Arguments), b.FirstVarArg)
		callee := f.get(b.Callee)
		if !callee.IsConstructor() {
			vm.throwTypeError("Super constructor is not a constructor")
		}
		f.set(b.Dst, vm.Construct(callee.ObjectRef(), args, f.get(b.ThisValue).AsObject()))

	// --- Iteration ----------------------------------------------------------------

	case bytecode.OpIteratorCloseCheckID:
		// Generic iteration only: there is always an iterator to close.

	case bytecode.OpAsyncIteratorOpenID:
		b := bytecode.DecodeAsyncIteratorOpen(in)
		symbolIterator := f.get(b.SymbolIterator)
		var iterator Value
		if symbolIterator.IsNullish() {
			iterator = ObjectValue(vm.createAsyncFromSyncIterator(vm.getIterator(f.get(b.Iterable))))
		} else {
			iterator = vm.callValue(symbolIterator, f.get(b.Iterable), nil)
		}
		if !iterator.IsObject() {
			vm.throwTypeError("Iterator result interface is not an object.")
		}
		f.set(b.Iterator, iterator)
		f.set(b.Next, iterator.ObjectRef().GetKey(nextKey))

	case bytecode.OpAsyncIteratorNextID:
		b := bytecode.DecodeAsyncIteratorNext(in)
		var args []Value
		if b.HasValue {
			args = []Value{f.regs[f.nl-int(b.StackOffset)+bytecode.CallFrameSlotFirstArgument]}
		}
		f.set(b.Dst, vm.callValue(f.get(b.Next), f.get(b.Iterator), args))

	case bytecode.OpEnumeratorPutByValID:
		b := bytecode.DecodeEnumeratorPutByVal(in)
		vm.putByVal(f.get(b.Base), f.get(b.PropertyName), f.get(b.Value), b.EcmaMode.IsStrict())

	// --- Generators and async functions -------------------------------------------

	case bytecode.OpCreateGeneratorID:
		b := bytecode.DecodeCreateGenerator(in)
		f.set(b.Dst, ObjectValue(vm.createGenerator(f.get(b.Callee).ObjectRef(), ClassGenerator)))

	case bytecode.OpCreateAsyncGeneratorID:
		b := bytecode.DecodeCreateAsyncGenerator(in)
		f.set(b.Dst, ObjectValue(vm.createGenerator(f.get(b.Callee).ObjectRef(), ClassAsyncGenerator)))

	case bytecode.OpNewGeneratorID:
		b := bytecode.DecodeNewGenerator(in)
		f.set(b.Dst, ObjectValue(r.newGeneratorObject(r.GeneratorPrototype, ClassGenerator)))

	case bytecode.OpNewAsyncFunctionGeneratorID:
		b := bytecode.DecodeNewAsyncFunctionGenerator(in)
		f.set(b.Dst, ObjectValue(r.newGeneratorObject(nil, ClassAsyncFunctionGenerator)))

	case bytecode.OpNewPromiseID:
		b := bytecode.DecodeNewPromise(in)
		f.set(b.Dst, ObjectValue(r.newPromise()))

	default:
		panic("vm: unimplemented opcode " + op.String())
	}
}

var (
	nextKey  = StrKey("next")
	doneKey  = StrKey("done")
	valueKey = StrKey("value")
)

var typeofStrings = map[string]*String{}

func init() {
	for _, s := range []string{"undefined", "object", "boolean", "number", "string", "symbol", "bigint", "function"} {
		// With their property key cached: switch_string in generated code
		// compares keys (switch (typeof x) in hash-map).
		t := NewString(s)
		t.Key()
		typeofStrings[s] = t
	}
}

// sameCell is pointer equality for op_jeq_ptr.
func sameCell(a, b Value) bool {
	return a.p != nil && a.p == b.p
}

func isCellWithType(v Value, t bytecode.JSType) bool {
	switch t {
	case bytecode.StringType:
		return v.IsString()
	case bytecode.SymbolType:
		return v.IsSymbol()
	case bytecode.HeapBigIntType:
		return v.IsBigInt()
	}
	o := v.AsObject()
	if o == nil {
		return false
	}
	switch t {
	case bytecode.ArrayType, bytecode.DerivedArrayType:
		return o.class == ClassArray
	case bytecode.JSGeneratorType:
		return o.class == ClassGenerator
	case bytecode.JSAsyncGeneratorType:
		return o.class == ClassAsyncGenerator
	case bytecode.JSPromiseType:
		return o.class == ClassPromise
	case bytecode.ProxyObjectType:
		return o.class == ClassProxy
	case bytecode.RegExpObjectType:
		return o.class == ClassRegExp
	case bytecode.JSMapType:
		return o.class == ClassMap
	case bytecode.JSSetType:
		return o.class == ClassSet
	}
	return false
}

// toThis is op_to_this.
func (vm *VM) toThis(v Value, mode bytecode.ECMAMode) Value {
	v = thisForCall(v)
	if mode.IsStrict() {
		return v
	}
	switch v.Kind() {
	case KindObject:
		return v
	case KindUndefined, KindNull:
		return ObjectValue(vm.realm.GlobalThis)
	case KindEmpty:
		return v
	}
	return ObjectValue(vm.ToObject(v))
}

// descriptorFromAttributes decodes op_define_*_property attributes.
func descriptorFromAttributes(a bytecode.DefinePropertyAttributes) PropertyDescriptor {
	var d PropertyDescriptor
	if v, ok := a.Configurable(); ok {
		d.Configurable, d.Has = v, d.Has|HasConfigurable
	}
	if v, ok := a.Enumerable(); ok {
		d.Enumerable, d.Has = v, d.Has|HasEnumerable
	}
	if v, ok := a.Writable(); ok {
		d.Writable, d.Has = v, d.Has|HasWritable
	}
	return d
}

// --- Property access helpers -----------------------------------------------------------

func (vm *VM) getByKey(base Value, k PropertyKey) Value {
	if o := base.AsObject(); o != nil {
		return o.Get(k, base)
	}
	if base.IsNullish() {
		vm.throwTypeError("%s is not an object (evaluating '%s')", base.TypeOfForMessage(), k)
	}
	return vm.getPrimitiveProperty(base, k)
}

func (vm *VM) requireObjectCoercible(base Value, property Value) {
	if base.IsNullish() {
		desc := "property"
		if property.IsString() || property.IsNumber() {
			desc = "'" + safeToString(property) + "'"
		}
		vm.throwTypeError("Cannot read %s of %s", desc, base.TypeOfForMessage())
	}
}

func (vm *VM) getByVal(base Value, property Value) Value {
	if o := base.AsObject(); o != nil && property.IsNumber() {
		if i := uint32(property.num()); float64(i) == property.num() && i != math.MaxUint32 {
			if v, ok := o.fastIndex(i); ok {
				return v
			}
			if t := typedArrayOf(o); t != nil {
				if v, ok := t.fastGet(i); ok {
					return v
				}
			}
		}
	}
	if base.IsString() && property.IsNumber() {
		s := base.StringRef()
		if i := int(property.num()); float64(i) == property.num() && i >= 0 && i < s.Length() {
			return StringValue(s.Substring(i, i+1))
		}
	}
	vm.requireObjectCoercible(base, property)
	return vm.getByKey(base, vm.ToPropertyKey(property))
}

// getWithThis is a super property read.
func (vm *VM) getWithThis(base Value, k PropertyKey, this Value) Value {
	if base.IsNullish() {
		vm.throwTypeError("Cannot read property '%s' of %s", k, base.TypeOfForMessage())
	}
	return vm.ToObject(base).Get(k, this)
}

func (vm *VM) putByKey(base Value, k PropertyKey, v Value, strict bool) {
	o := base.AsObject()
	if o == nil {
		if base.IsNullish() {
			vm.throwTypeError("Cannot set property '%s' of %s", k, base.TypeOfForMessage())
		}
		o = vm.ToObject(base)
	}
	if !o.Set(k, v, base) && strict {
		vm.throwTypeError("Attempted to assign to readonly property '%s'.", k)
	}
}

func (vm *VM) putByVal(base Value, property Value, v Value, strict bool) {
	if o := base.AsObject(); o != nil && property.IsNumber() && o.exotic != nil {
		if i := uint32(property.num()); float64(i) == property.num() && i != math.MaxUint32 {
			if a, ok := o.exotic.(*arrayObject); ok {
				if a.setIndexFast(o, i, v) {
					return
				}
			} else if t := typedArrayOf(o); t != nil && t.fastSet(i, v) {
				return
			}
		}
	}
	if base.IsNullish() {
		vm.throwTypeError("Cannot set property of %s", base.TypeOfForMessage())
	}
	vm.putByKey(base, vm.ToPropertyKey(property), v, strict)
}

// putWithThis is a super property write.
func (vm *VM) putWithThis(base Value, k PropertyKey, v Value, this Value, strict bool) {
	if base.IsNullish() {
		vm.throwTypeError("Cannot set property '%s' of %s", k, base.TypeOfForMessage())
	}
	if !vm.ToObject(base).Set(k, v, this) && strict {
		vm.throwTypeError("Attempted to assign to readonly property '%s'.", k)
	}
}

// putDirect defines an own enumerable data property (object and array
// literals, class fields).
func (vm *VM) putDirect(base Value, k PropertyKey, v Value) {
	o := base.AsObject()
	if k.IsPrivate() {
		o.setOwn(k, dataProperty(v, DefaultFlags))
		return
	}
	if k.sym == nil && k.name == protoIdent && o.class != ClassFunction {
		// __proto__ in literals is handled by the generator; a direct put
		// still defines an own property.
	}
	o.CreateDataPropertyOrThrow(k, v)
}

var protoIdent = ident.FromString("__proto__")

func (vm *VM) toObjectForAccess(base Value, property Value) *Object {
	if base.IsNullish() {
		vm.throwTypeError("Cannot convert %s to object", base.TypeOfForMessage())
	}
	return vm.ToObject(base)
}

func (vm *VM) deleteProperty(base Value, k PropertyKey, strict bool) bool {
	if s, ok := base.cell().(*Scope); ok && base.Kind() == KindCell {
		// delete of an unqualified name: base is the resolved scope.
		switch s.kind {
		case scopeGlobalObject, scopeWith:
			return vm.deleteFromObject(s.object, k, strict)
		case scopeLexical, scopeGlobalLexical:
			if s.evalVars != nil && s.evalVars.HasOwnProperty(k) {
				return s.evalVars.Delete(k)
			}
		}
		return false
	}
	if base.IsNullish() {
		vm.throwTypeError("Cannot delete property '%s' of %s", k, base.TypeOfForMessage())
	}
	return vm.deleteFromObject(vm.ToObject(base), k, strict)
}

func (vm *VM) deleteFromObject(o *Object, k PropertyKey, strict bool) bool {
	ok := o.Delete(k)
	if !ok && strict {
		vm.throwTypeError("Unable to delete property.")
	}
	return ok
}

func (vm *VM) inOperator(base Value, k PropertyKey) bool {
	o := base.AsObject()
	if o == nil {
		vm.throwTypeError("Cannot use 'in' operator to search for '%s' in %s", k, vm.describeForError(base))
	}
	return o.HasProperty(k)
}

// putAccessor is op_put_getter/setter_by_id/val.
func (vm *VM) putAccessor(base Value, k PropertyKey, attributes uint32, get, set *Object, hasGet, hasSet bool) {
	const dontEnum, dontDelete = 1 << 2, 1 << 3
	d := PropertyDescriptor{Enumerable: attributes&dontEnum == 0, Configurable: attributes&dontDelete == 0,
		Has: HasEnumerable | HasConfigurable}
	if hasGet {
		d.Get, d.Has = get, d.Has|HasGet
	}
	if hasSet {
		d.Set, d.Has = set, d.Has|HasSet
	}
	o := base.ObjectRef()
	if k.IsPrivate() {
		o.ordinaryDefineOwnProperty(k, d)
		return
	}
	o.DefinePropertyOrThrow(k, d)
}

// --- Private names ---------------------------------------------------------------------

func (vm *VM) getPrivateName(base Value, name Value) Value {
	o := base.AsObject()
	k := SymKey(name.SymbolRef())
	if o == nil {
		vm.throwTypeError("Cannot access invalid private field (evaluating '%s')", k)
	}
	p, ok := o.props.get(k)
	if !ok {
		vm.throwTypeError("Cannot access invalid private field (evaluating '%s')", k)
	}
	return p.getValue(base)
}

func (vm *VM) putPrivateName(base Value, name Value, v Value, kind bytecode.PrivateFieldPutKind) {
	o := base.AsObject()
	k := SymKey(name.SymbolRef())
	if o == nil {
		vm.throwTypeError("Cannot access invalid private field")
	}
	p, ok := o.props.get(k)
	if kind == bytecode.PrivateFieldPutKindDefine {
		if ok {
			vm.throwTypeError("Attempted to redefine existing private field '%s'", k)
		}
		if !o.extensible {
			vm.throwTypeError("Cannot define private field '%s' on a non-extensible object", k)
		}
		o.setOwn(k, dataProperty(v, Writable))
		return
	}
	if !ok {
		vm.throwTypeError("Cannot access invalid private field (evaluating '%s')", k)
	}
	if p.IsAccessor() {
		setter := p.Accessor().Set
		if setter == nil {
			vm.throwTypeError("Cannot set private accessor without a setter")
		}
		vm.Call(ObjectValue(setter), base, v)
		return
	}
	if !p.Writable() {
		vm.throwTypeError("Cannot assign to private method")
	}
	o.props.vals[o.props.find(k)] = v
}

// arrayWithLength is new Array(len) (op_new_array_with_size).
func (vm *VM) arrayWithLength(length Value) *Object {
	if !length.IsNumber() {
		return vm.realm.NewArray(length)
	}
	n := uint32(length.num())
	if float64(n) != length.num() {
		vm.throwRangeError("Array size is not a small enough positive integer.")
	}
	return vm.arrayWithHoles(n, nil)
}

// newClosure creates the function object for exec in scope, created by
// code running in f.
func (vm *VM) newClosure(f *Frame, exec *bytecode.UnlinkedFunctionExecutable, scope *Scope) *Object {
	o := f.cb.realm.newJSFunction(exec, scope)
	o.fn.(*jsFunction).insideOrdinary = !exec.IsArrowFunction() || f.insideOrdinaryFunction()
	return o
}
