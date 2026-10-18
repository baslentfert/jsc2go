//go:build ((arm64 || amd64) && (darwin || linux)) || (amd64 && windows)

package vm

import (
	"unsafe"

	"github.com/baslentfert/jsc2go/bytecode"
	"github.com/baslentfert/jsc2go/jit"
)

// Calls from generated code to generated code (JSC's baseline JIT calls,
// with a call link info per call site).
//
// A call site whose callee is the JavaScript function its inline cache
// (callIC) remembers, and whose generated code covers the whole function,
// calls that code directly: the caller's code takes registers from the
// VM's register stack and a frame from vm.jitFrames, fills them like
// newFrame, and calls the callee's entry stub (jit.Masm.CallEntry). If the
// callee's code returns (op_ret), the caller takes the result from the
// frame's ret, frees the registers and the frame, and goes on: Go is not
// involved. Any other way the callee's code stops (a guard, a slow path,
// a call it cannot make itself) makes every caller in generated code stop
// too, with jitExitNested at its call; Go then turns the frames of that
// chain into ordinary frames (finishNative), finishes them innermost first
// with the interpreter, and goes back into the outermost one's code. All
// state is in the registers in memory, so nothing is lost on the way.
//
// Only some callees qualify (fillCallIC): JavaScript functions of the same
// realm, not class constructors, without exception handlers, whose code
// has a whole-function entry. The machine code also checks the call depth
// against MaxDepth and the register stack's room, and takes the slow path
// (Go's call) otherwise.

// jitMaxDepth bounds the chain of generated-code calls in one call of
// generated code (its native stack use, 32 bytes a call on amd64 and 64 on
// arm64, must stay within jit.Call's reserve).
const jitMaxDepth = 64

// jitExitNested is the exit code of a caller whose callee's code stopped
// (see above); its pc is the call's.
const jitExitNested = 5

// jitExitNative is the exit code of a call (call, call_ignore_result or
// tail_call) of a builtin without a thunk, or of new of a builtin: Go
// calls or constructs it directly (jitNativeCall), without the slow path's
// dispatch and call cache.
const jitExitNative = 6

// jitNativeCall makes the call at pc of f, whose callee is a builtin, and
// returns the pc after it.
func (vm *VM) jitNativeCall(f *Frame, pc int) int {
	in := f.cb.u.Instructions[pc:]
	op := bytecode.OpcodeID(in[0])
	var callee, dst bytecode.VirtualRegister
	var argc, argv uint32
	switch op {
	case bytecode.OpCallID:
		b := bytecode.DecodeCall(in)
		callee, dst, argc, argv = b.Callee, b.Dst, b.Argc, b.Argv
	case bytecode.OpTailCallID:
		// A builtin in tail position: an ordinary call, like the slow
		// path's (the ret after it returns the result).
		b := bytecode.DecodeTailCall(in)
		callee, dst, argc, argv = b.Callee, b.Dst, b.Argc, b.Argv
	case bytecode.OpConstructID:
		b := bytecode.DecodeConstruct(in)
		newTarget, args := f.callArgs(b.Argv, b.Argc)
		f.set(b.Dst, vm.constructValue(*f.ref(b.Callee), args, newTarget.AsObject()))
		return pc + op.Length()
	default:
		b := bytecode.DecodeCallIgnoreResult(in)
		callee, argc, argv = b.Callee, b.Argc, b.Argv
	}
	o := f.ref(callee).AsObject()
	this, args := f.callArgs(argv, argc)
	v := o.fn.(*nativeFunction).call(o, this, args)
	if op != bytecode.OpCallIgnoreResultID {
		f.set(dst, v)
	}
	return pc + op.Length()
}

// callIC is a call site's inline cache: polymorphic, since a method call
// such as task.run() often reaches a few different functions.
type callIC struct {
	cases [callICCases]callCase
	next  uint8 // the case filled next when all are in use
}

const callICCases = 4

// callCase is one function a call site calls directly, or (a closure
// case, exec set and callee nil) every function of one executable in the
// caller's realm: JSC's closure calls (linkPolymorphicCall), since a
// function expression evaluated in a loop or a call makes a new function
// object each time, with the same code block. The callee's scope is
// always read from the callee.
type callCase struct {
	callee *Object // the function called; nil: empty or a closure case
	exec   *bytecode.UnlinkedFunctionExecutable
	entry  uintptr   // its code's whole-function entry stub
	ent    *jitEntry // that entry (whose disabled is checked)
	code   *jitCode  // keeps the callee's code alive
	cb     *CodeBlock
	nl     int64 // the callee's NumCalleeLocals
	size   int64 // registers its frame takes at this call site
	consts unsafe.Pointer
	// nvars are the callee's vars (bytecode NumVars), which its frame
	// starts with undefined; extra the parameters this site passes no
	// argument for. The other registers are written before they are read
	// (JSC's op_enter clears only the vars too).
	nvars, extra int64
	// argc is the arguments it was filled for, this included (the
	// arguments a call_varargs site forwards vary).
	argc int64
}

// Layouts the machine code uses.
var (
	offICCallee = int(unsafe.Offsetof(callCase{}.callee))
	offICEntry  = int(unsafe.Offsetof(callCase{}.entry))
	offICEnt    = int(unsafe.Offsetof(callCase{}.ent))
	offICCB     = int(unsafe.Offsetof(callCase{}.cb))
	offICExec   = int(unsafe.Offsetof(callCase{}.exec))
	offICNL     = int(unsafe.Offsetof(callCase{}.nl))
	offICSize   = int(unsafe.Offsetof(callCase{}.size))
	offICConsts = int(unsafe.Offsetof(callCase{}.consts))
	offICNVars  = int(unsafe.Offsetof(callCase{}.nvars))
	offICExtra  = int(unsafe.Offsetof(callCase{}.extra))
	offICArgc   = int(unsafe.Offsetof(callCase{}.argc))

	offEntryDisabled = int(unsafe.Offsetof(jitEntry{}.disabled))

	offFrameCB         = int(unsafe.Offsetof(Frame{}.cb))
	offFrameRegs       = int(unsafe.Offsetof(Frame{}.regs))
	offFrameNL         = int(unsafe.Offsetof(Frame{}.nl))
	offFrameArgc       = int(unsafe.Offsetof(Frame{}.argc))
	offFrameCallee     = int(unsafe.Offsetof(Frame{}.callee))
	offFrameCaller     = int(unsafe.Offsetof(Frame{}.caller))
	offFrameStackBase  = int(unsafe.Offsetof(Frame{}.stackBase))
	offFrameBase       = int(unsafe.Offsetof(Frame{}.base))
	offFrameConsts     = int(unsafe.Offsetof(Frame{}.consts))
	offFrameVM         = int(unsafe.Offsetof(Frame{}.vm))
	offFrameJitExit    = int(unsafe.Offsetof(Frame{}.jitExit))
	offFrameSuspends   = int(unsafe.Offsetof(Frame{}.jitSuspends))
	offFramePC         = int(unsafe.Offsetof(Frame{}.pc))
	offFrameInlineRoom = int(unsafe.Offsetof(Frame{}.inlineRoom))

	offVMDepth = int(unsafe.Offsetof(VM{}.depth))
	// (atomic.Int64 is an int64.)
	offVMCallLimit = int(unsafe.Offsetof(VM{}.callLimit))
	offVMRegStack  = int(unsafe.Offsetof(VM{}.regStack))
	offVMSp        = int(unsafe.Offsetof(VM{}.sp))
	offVMSpDirty   = int(unsafe.Offsetof(VM{}.spDirty))
	offVMJitFrames = int(unsafe.Offsetof(VM{}.jitFrames))
	offVMJitDepth  = int(unsafe.Offsetof(VM{}.jitDepth))
)

// nativeCall compiles call or call_ignore_result (dst < 0) at pc: the
// direct call of a cached callee's code, and the slow path (Go's call,
// which fills the cache) for everything else.
func (c *jitCompiler) nativeCall(pc int, callee bytecode.VirtualRegister, argc, argv uint32, dst bytecode.VirtualRegister, hasDst bool) {
	// The caller's this and arguments: registers this, this+1, ...
	this := bytecode.VirtualRegister(bytecode.CallFrameSlotThisArgument - int(argv))
	c.nativeCallFrom(pc, callee, this, argc, dst, hasDst, false)
}

// nativeCallFrom is nativeCall with this in register this; forwarded, the
// arguments are the caller's own (call_varargs of f.apply(this,
// arguments)): as many as the case was filled for, else the slow path.
func (c *jitCompiler) nativeCallFrom(pc int, callee, this bytecode.VirtualRegister, argc uint32, dst bytecode.VirtualRegister, hasDst, forwarded bool) {
	m := &c.m
	next := pc + bytecode.OpcodeID(c.cb.u.Instructions[pc]).Length()
	slow := c.slow(pc, next)
	// (Shared by an optimized loop's copy of the instruction.)
	ic := c.callICs[pc]
	if ic == nil {
		ic = &callIC{}
		c.callICs[pc] = ic
	}

	// S0, S1 = callee; S2 = the cache's case for it (an empty case's
	// callee is nil, which no object is).
	c.loadValue(jit.S0, jit.S1, callee)
	m.Branch64Imm(jit.NE, jit.S1, uint32(tagObject), slow)
	intrinsicDone := jit.NewLabel()
	hit := jit.NewLabel()
	c.callCases(ic, hit, forwarded)
	if op := bytecode.OpcodeID(c.cb.u.Instructions[pc]); op != bytecode.OpConstructID && !forwarded && c.off(this) >= 0 {
		// A builtin with a thunk (jit_thunks.go; JSC links such calls to
		// the thunk): after the cache, so that calls of cached functions
		// pay nothing for it, in cold code.
		thunkCall := jit.NewLabel()
		native := slow
		if op == bytecode.OpCallID || op == bytecode.OpCallIgnoreResultID || op == bytecode.OpTailCallID {
			native = c.nativeExits[pc]
			if native == nil {
				native = jit.NewLabel()
				c.nativeExits[pc] = native
			}
			if o := c.opt; o != nil {
				native = c.flushTo(o, pc, native) // Go makes the call
			}
		}
		m.B(thunkCall)
		c.cold = append(c.cold, func() {
			m.Bind(thunkCall)
			m.Load64(jit.S9, jit.S0, offFn)
			m.MovImm(jit.S8, uint64(nativeFunctionItab))
			notNative := jit.NewLabel()
			m.Branch64(jit.NE, jit.S9, jit.S8, notNative)
			m.Load64(jit.S9, jit.S0, offFn+8)
			m.Load16(jit.S9, jit.S9, offNFIntrinsic)
			m.MovImm(jit.S8, uint64(uintptr(unsafe.Pointer(&jitThunks[0]))))
			m.AddShift64(jit.S8, jit.S8, jit.S9, 3)
			m.Load64(jit.S9, jit.S8, 0)
			m.BranchZero(jit.S9, native)
			m.AddImm64(jit.S7, rRegs, uint32(c.off(this)))
			m.MovImm(jit.S6, uint64(argc))
			m.CallThunk(jit.S9)
			// A case the thunk leaves to the builtin (charCodeAt out of
			// range, push of several values, ...): the callee is a builtin,
			// so its own exit rather than the slow path (chai and base64
			// took the slow path 1.1 million times a run so).
			m.BranchNonZero(jit.S0, native)
			if hasDst {
				c.storeValue(dst, jit.S1, jit.S2)
			}
			m.B(intrinsicDone)
			m.Bind(notNative)
			c.closureCases(ic, hit, slow, forwarded)
		})
	} else {
		closures := jit.NewLabel()
		m.B(closures)
		c.cold = append(c.cold, func() {
			m.Bind(closures)
			if op := bytecode.OpcodeID(c.cb.u.Instructions[pc]); op == bytecode.OpConstructID {
				// new of a builtin (Date, String, Array, ...): Go
				// constructs it directly (jitNativeCall), without the slow
				// path's dispatch (date-format-xparb made 0.8 million a run).
				native := c.nativeExits[pc]
				if native == nil {
					native = jit.NewLabel()
					c.nativeExits[pc] = native
				}
				m.Load64(jit.S9, jit.S0, offFn)
				m.MovImm(jit.S8, uint64(nativeFunctionItab))
				m.Branch64(jit.EQ, jit.S9, jit.S8, native)
			}
			c.closureCases(ic, hit, slow, forwarded)
		})
	}
	m.Bind(hit)
	if o := c.opt; o != nil {
		// Other generated code, whose optimized loops use the same
		// registers (an optimized loop's call, jit_opt.go).
		c.optFlush(o, pc, nil)
		c.directCall(pc, this, argc, dst, hasDst, slow, forwarded)
		c.optReload(o, pc+bytecode.OpcodeID(c.cb.u.Instructions[pc]).Length())
	} else {
		c.directCall(pc, this, argc, dst, hasDst, slow, forwarded)
	}
	m.Bind(intrinsicDone)
}

// callCases branches to hit with S2 = the case of call cache ic for the
// callee S0, if it has one whose code is enabled; else it falls through.
func (c *jitCompiler) callCases(ic *callIC, hit *jit.Label, forwarded bool) {
	m := &c.m
	for i := range ic.cases {
		miss := jit.NewLabel()
		m.MovImm(jit.S2, uint64(uintptr(unsafe.Pointer(&ic.cases[i]))))
		m.Load64(jit.S9, jit.S2, offICCallee)
		m.Branch64(jit.NE, jit.S0, jit.S9, miss)
		if forwarded {
			c.argcCase(miss)
		}
		m.Load64(jit.S9, jit.S2, offICEnt)
		m.Load8(jit.S9, jit.S9, offEntryDisabled)
		m.BranchZero(jit.S9, hit)
		m.Bind(miss)
	}
}

// argcCase branches to miss unless case S2 is for as many arguments as
// the caller's frame has (forwarded arguments: one case per count).
func (c *jitCompiler) argcCase(miss *jit.Label) {
	c.m.Load64(jit.S8, jit.S2, offICArgc)
	c.m.Load64(rTmp, rFrame, offFrameArgc)
	c.m.Branch64(jit.NE, jit.S8, rTmp, miss)
}

// closureCases branches to hit with S2 = the closure case of call cache ic
// for callee S0, if it has one for the callee's executable whose code is
// enabled, else to miss. It keeps S0 and S1.
func (c *jitCompiler) closureCases(ic *callIC, hit, miss *jit.Label, forwarded bool) {
	m := &c.m
	m.Load64(jit.S9, jit.S0, offFn)
	m.MovImm(jit.S8, uint64(jsFunctionItab))
	m.Branch64(jit.NE, jit.S9, jit.S8, miss)
	// Of the caller's realm, whose code blocks the cases are.
	m.Load64(jit.S9, jit.S0, offRealm)
	m.MovImm(jit.S8, uint64(uintptr(unsafe.Pointer(c.cb.realm))))
	m.Branch64(jit.NE, jit.S9, jit.S8, miss)
	m.Load64(jit.S9, jit.S0, offFn+8) // *jsFunction
	m.Load64(jit.S9, jit.S9, offFnExec)
	for i := range ic.cases {
		next := jit.NewLabel()
		m.MovImm(jit.S2, uint64(uintptr(unsafe.Pointer(&ic.cases[i]))))
		// (An identity case's callee is set; it was checked already.)
		m.Load64(jit.S8, jit.S2, offICCallee)
		m.BranchNonZero(jit.S8, next)
		m.Load64(jit.S8, jit.S2, offICExec)
		m.Branch64(jit.NE, jit.S8, jit.S9, next)
		if forwarded {
			c.argcCase(next)
		}
		m.Load64(jit.S8, jit.S2, offICEnt)
		m.Load8(jit.S8, jit.S8, offEntryDisabled)
		m.BranchZero(jit.S8, hit)
		m.Bind(next)
	}
	m.B(miss)
}

// directCall calls the code of callee S0, S1 (S2 = its call cache case)
// for the instruction at pc, with this and the arguments in registers
// this, this+1, ... (argc of them, this included), storing the result to
// dst; it takes the slow path when the call cannot be made here.
func (c *jitCompiler) directCall(pc int, this bytecode.VirtualRegister, argc uint32, dst bytecode.VirtualRegister, hasDst bool, slow *jit.Label, forwarded bool) {
	m := &c.m
	if forwarded {
		// The caller's own arguments: as many as the case is for.
		m.Load64(jit.S9, jit.S2, offICArgc)
		m.Load64(rTmp, rFrame, offFrameArgc)
		m.Branch64(jit.NE, jit.S9, rTmp, slow)
	}
	// thisForCall: a call of a variable has its scope as this, which is
	// undefined but for a with statement's (the slow path).
	// (A constant this, as in f.apply(null, arguments), is neither.)
	thisConst, constThis := c.constant(this)
	if !constThis {
		thisChecked := jit.NewLabel()
		m.Load64(jit.S9, rRegs, c.off(this)+8)
		m.Branch64Imm(jit.EQ, jit.S9, uint32(tagCell), slow)
		m.Branch64Imm(jit.NE, jit.S9, uint32(tagScope), thisChecked)
		m.Load64(jit.S9, rRegs, c.off(this))
		m.Load8(jit.S9, jit.S9, offScopeKind)
		m.Branch32Imm(jit.EQ, jit.S9, uint32(scopeWith), slow)
		m.Bind(thisChecked)
	}
	// S3 = vm: room in the call depth, the chain and the register stack.
	m.Load64(jit.S3, rFrame, offFrameVM)
	m.Load64(jit.S4, jit.S3, offVMDepth)
	// (callLimit: MaxDepth, or -1 after Interrupt, when the slow path's
	// call stops: also recursion without loops.)
	m.Load64(jit.S9, jit.S3, offVMCallLimit)
	m.Branch64(jit.SGE, jit.S4, jit.S9, slow)
	m.Load64(jit.S4, jit.S3, offVMJitDepth)
	m.Branch64Imm(jit.UGE, jit.S4, jitMaxDepth, slow)
	m.Load64(jit.S7, jit.S3, offVMJitFrames)
	m.BranchZero(jit.S7, slow)
	m.Load64(jit.S5, jit.S3, offVMSp)
	m.Load64(jit.S6, jit.S2, offICSize)
	m.Add64(jit.S9, jit.S5, jit.S6)
	m.Load64(rTmp, jit.S3, offVMRegStack+8) // len
	m.Branch64(jit.UGT, jit.S9, rTmp, slow)

	// Commit: the registers (S8) and the frame (S7).
	m.Store64(jit.S9, jit.S3, offVMSp)
	dirtyOK := jit.NewLabel()
	m.Load64(rTmp, jit.S3, offVMSpDirty)
	m.Branch64(jit.UGE, rTmp, jit.S9, dirtyOK)
	m.Store64(jit.S9, jit.S3, offVMSpDirty)
	m.Bind(dirtyOK)
	m.AddShift64(jit.S7, jit.S7, jit.S4, 3)
	m.Load64(jit.S7, jit.S7, 0)
	m.AddImm64(jit.S4, jit.S4, 1)
	m.Store64(jit.S4, jit.S3, offVMJitDepth)
	m.Load64(jit.S4, jit.S3, offVMDepth)
	m.AddImm64(jit.S4, jit.S4, 1)
	m.Store64(jit.S4, jit.S3, offVMDepth)
	m.Load64(jit.S8, jit.S3, offVMRegStack)
	m.AddShift64(jit.S8, jit.S8, jit.S5, 4)
	// The frame, like newFrame. A free frame in vm.jitFrames has zero pc,
	// exception, ret, callee, scope and jitExit (finishNative zeroes the
	// frames it takes over); the rest is set here.
	m.Load64(jit.S9, jit.S2, offICCB)
	m.Store64(jit.S9, jit.S7, offFrameCB)
	m.Store64(jit.S8, jit.S7, offFrameRegs)
	m.Store64(jit.S6, jit.S7, offFrameRegs+8)
	m.Store64(jit.S6, jit.S7, offFrameRegs+16)
	m.Load64(jit.S9, jit.S2, offICNL)
	m.Store64(jit.S9, jit.S7, offFrameNL)
	m.AddShift64(jit.S9, jit.S8, jit.S9, 4) // &regs[nl]
	m.Store64(jit.S9, jit.S7, offFrameBase)
	if forwarded {
		m.Load64(rTmp, jit.S2, offICArgc)
	} else {
		m.MovImm(rTmp, uint64(argc))
	}
	m.Store64(rTmp, jit.S7, offFrameArgc)
	m.Store64(jit.S0, jit.S7, offFrameCallee)
	m.Load64(rTmp, jit.S0, offFn+8) // *jsFunction
	m.Load64(rTmp, rTmp, offFnScope)
	m.Store64(rTmp, jit.S7, offFrameScope)
	m.Store64(rFrame, jit.S7, offFrameCaller)
	m.Store64(jit.S5, jit.S7, offFrameStackBase)
	m.Load64(rTmp, jit.S2, offICConsts)
	m.Store64(rTmp, jit.S7, offFrameConsts)
	m.MovImm(rTmp, jitFuel)
	m.Store64(rTmp, jit.S7, offFrameFuel)
	m.Store64(jit.S3, jit.S7, offFrameVM)
	m.StoreZero64(jit.S7, offFrameSuspends)
	m.StoreZero64(jit.S7, offFrameInlineRoom)
	// The registers (S9 = the callee's register 0): the callee, this and
	// the arguments; the rest are zero, which is undefined.
	m.Store64(jit.S0, jit.S9, bytecode.CallFrameSlotCallee*valueSize)
	m.Store64(jit.S1, jit.S9, bytecode.CallFrameSlotCallee*valueSize+8)
	// this (a scope here means undefined).
	if constThis {
		// Its words (the code block's constants keep a pointer alive).
		m.MovImm(rTmp, thisConst.u)
		m.Store64(rTmp, jit.S9, bytecode.CallFrameSlotThisArgument*valueSize+8)
		m.MovImm(rTmp, uint64(uintptr(thisConst.p)))
		m.Store64(rTmp, jit.S9, bytecode.CallFrameSlotThisArgument*valueSize)
	} else {
		thisScope, thisCopied := jit.NewLabel(), jit.NewLabel()
		m.Load64(rTmp, rRegs, c.off(this)+8)
		m.Branch64Imm(jit.EQ, rTmp, uint32(tagScope), thisScope)
		m.Store64(rTmp, jit.S9, bytecode.CallFrameSlotThisArgument*valueSize+8)
		m.Load64(rTmp, rRegs, c.off(this))
		m.Store64(rTmp, jit.S9, bytecode.CallFrameSlotThisArgument*valueSize)
		m.B(thisCopied)
		m.Bind(thisScope)
		m.StoreZero128(jit.S9, bytecode.CallFrameSlotThisArgument*valueSize)
		m.Bind(thisCopied)
	}
	if forwarded {
		// The caller's arguments (registers this+1, ... of its frame).
		loop, copied := jit.NewLabel(), jit.NewLabel()
		m.Load64(rTmp, jit.S2, offICArgc)
		m.SubImm64(rTmp, rTmp, 1)
		m.BranchZero(rTmp, copied)
		m.AddImm64(jit.S4, rRegs, uint32(c.off(bytecode.VirtualRegister(bytecode.CallFrameSlotThisArgument+1))))
		m.AddImm64(jit.S5, jit.S9, uint32((bytecode.CallFrameSlotThisArgument+1)*valueSize))
		m.Bind(loop)
		m.Load64(jit.S6, jit.S4, 0)
		m.Store64(jit.S6, jit.S5, 0)
		m.Load64(jit.S6, jit.S4, 8)
		m.Store64(jit.S6, jit.S5, 8)
		m.AddImm64(jit.S4, jit.S4, uint32(valueSize))
		m.AddImm64(jit.S5, jit.S5, uint32(valueSize))
		m.SubImm64(rTmp, rTmp, 1)
		m.BranchNonZero(rTmp, loop)
		m.Bind(copied)
	}
	for i := 1; i < int(argc); i++ {
		m.Load64(rTmp, rRegs, c.off(this+bytecode.VirtualRegister(i)))
		m.Store64(rTmp, jit.S9, (bytecode.CallFrameSlotThisArgument+i)*valueSize)
		m.Load64(rTmp, rRegs, c.off(this+bytecode.VirtualRegister(i))+8)
		m.Store64(rTmp, jit.S9, (bytecode.CallFrameSlotThisArgument+i)*valueSize+8)
	}
	// The registers may hold an earlier callee's values (VM.spDirty):
	// undefined for the vars, below register 0, and for the parameters
	// without an argument.
	varsLoop, varsDone, extraLoop, extraDone := jit.NewLabel(), jit.NewLabel(), jit.NewLabel(), jit.NewLabel()
	m.Load64(rTmp, jit.S2, offICNVars)
	m.BranchZero(rTmp, varsDone)
	m.Mov(jit.S4, jit.S9)
	m.Bind(varsLoop)
	m.SubImm64(jit.S4, jit.S4, uint32(valueSize))
	m.StoreZero128(jit.S4, 0)
	m.SubImm64(rTmp, rTmp, 1)
	m.BranchNonZero(rTmp, varsLoop)
	m.Bind(varsDone)
	m.Load64(rTmp, jit.S2, offICExtra)
	m.BranchZero(rTmp, extraDone)
	if forwarded {
		m.Load64(jit.S5, jit.S2, offICArgc)
		m.AddShift64(jit.S4, jit.S9, jit.S5, 4)
		m.AddImm64(jit.S4, jit.S4, uint32(bytecode.CallFrameSlotThisArgument*valueSize))
	} else {
		m.AddImm64(jit.S4, jit.S9, uint32((bytecode.CallFrameSlotThisArgument+int(argc))*valueSize))
	}
	m.Bind(extraLoop)
	m.StoreZero128(jit.S4, 0)
	m.AddImm64(jit.S4, jit.S4, uint32(valueSize))
	m.SubImm64(rTmp, rTmp, 1)
	m.BranchNonZero(rTmp, extraLoop)
	m.Bind(extraDone)

	// The caller's pc is the call's while the callee runs: what the callee
	// suspended for may throw straight through this frame's code to the
	// handlers that pc selects (jit_suspend.go).
	m.MovImm(rTmp, uint64(pc))
	m.Store64(rTmp, rFrame, offFramePC)
	// The call; the callee clobbers every register but RMask.
	m.Push(rRegs)
	m.Push(rFrame)
	m.Push(jit.S7)
	m.Load64(jit.S9, jit.S2, offICEntry)
	m.CallEntry(jit.S9, jit.S8, jit.S7)
	m.Pop(jit.S7)
	m.Pop(rFrame)
	m.Pop(rRegs)

	nested := jit.NewLabel()
	done := jit.NewLabel()
	m.MovImm(rTmp, jitExitMask)
	m.Alu32(jit.And32, jit.S9, jit.S0, rTmp)
	m.Branch32Imm(jit.NE, jit.S9, jitExitReturn, nested)
	if hasDst {
		m.Load64(rTmp, jit.S7, offFrameRet)
		m.Store64(rTmp, rRegs, c.off(dst))
		m.Load64(rTmp, jit.S7, offFrameRet+8)
		m.Store64(rTmp, rRegs, c.off(dst)+8)
	}
	// Free the registers (left as they are: VM.spDirty) and the frame. If
	// the register stack was replaced while the callee ran, its registers
	// are in the old one, which frames below still use: clear them (cold).
	m.Load64(jit.S3, rFrame, offFrameVM)
	m.Load64(jit.S5, jit.S7, offFrameStackBase)
	oldStack, freed := jit.NewLabel(), jit.NewLabel()
	m.Load64(jit.S6, jit.S3, offVMRegStack)
	m.AddShift64(jit.S6, jit.S6, jit.S5, 4)
	m.Load64(jit.S8, jit.S7, offFrameRegs)
	m.Branch64(jit.NE, jit.S6, jit.S8, oldStack)
	c.cold = append(c.cold, func() {
		m.Bind(oldStack)
		clearLoop := jit.NewLabel()
		m.Load64(jit.S6, jit.S7, offFrameRegs+8) // a frame has at least the header
		m.Bind(clearLoop)
		m.StoreZero128(jit.S8, 0)
		m.AddImm64(jit.S8, jit.S8, uint32(valueSize))
		m.SubImm64(jit.S6, jit.S6, 1)
		m.BranchNonZero(jit.S6, clearLoop)
		m.B(freed)
	})
	m.Bind(freed)
	m.Store64(jit.S5, jit.S3, offVMSp)
	m.Load64(jit.S4, jit.S3, offVMJitDepth)
	m.SubImm64(jit.S4, jit.S4, 1)
	m.Store64(jit.S4, jit.S3, offVMJitDepth)
	m.Load64(jit.S4, jit.S3, offVMDepth)
	m.SubImm64(jit.S4, jit.S4, 1)
	m.Store64(jit.S4, jit.S3, offVMDepth)
	// Generated code writes only ret and jitFuel of its frame, and the next
	// call sets every field the call sets here: clear ret, and the callee
	// and scope, so that a free frame keeps no closure alive. (The whole
	// frame took 20 stores per call.)
	m.StoreZero64(jit.S7, offFrameRet)
	m.StoreZero64(jit.S7, offFrameRet+8)
	m.StoreZero64(jit.S7, offFrameCallee)
	m.StoreZero64(jit.S7, offFrameScope)
	m.B(done)
	// The callee's code stopped: keep its exit code, and stop too.
	m.Bind(nested)
	m.Store64(jit.S0, jit.S7, offFrameJitExit)
	m.B(c.nestedExit(pc))
	m.Bind(done)
}

var (
	offStrU16              = int(unsafe.Offsetof(String{}.data))
	offExtra               = int(unsafe.Offsetof(Object{}.extra))
	offArrayLengthWritable = int(unsafe.Offsetof(arrayObject{}.lengthWritable))
)

// nestedExit is the exit for a call at pc whose callee's code stopped.
func (c *jitCompiler) nestedExit(pc int) *jit.Label {
	l := c.nestedExits[pc]
	if l == nil {
		l = jit.NewLabel()
		c.nestedExits[pc] = l
	}
	return l
}

// fillCallIC caches the callee of the call (or construct, for new) at pc
// of f's code, which Go makes now, if generated code can call it directly
// from then on.
func (vm *VM) fillCallIC(f *Frame, pc int, calleeValue Value, argc int, construct bool) {
	code := f.cb.jit.code
	if code == nil || code.callICs == nil {
		return
	}
	ic := code.callICs[pc]
	o := calleeValue.AsObject()
	if ic == nil || o == nil {
		return
	}
	j, ok := o.fn.(*jsFunction)
	if !ok {
		return
	}
	// For new, the construct code: its this register is new.target, which
	// the caller's this register holds (op_construct), so the call is made
	// the same way.
	kind := bytecode.CodeForCall
	if construct {
		if !j.isConstructor() {
			return
		}
		kind = bytecode.CodeForConstruct
	} else if j.classConstructor {
		return
	}
	cb := j.code[kind]
	if cb == nil {
		// A function not called yet (this runs before the call): a new
		// closure of an executable whose code another closure linked.
		cb = o.realm.linkedCode(j.exec, kind)
		if cb == nil {
			return
		}
		j.code[kind] = cb
	}
	if cb.realm != f.cb.realm || len(cb.u.ExceptionHandlers) != 0 {
		return
	}
	cc := cb.jit.code
	if cc == nil || cc.entries[0] == nil || cc.entries[0].disabled {
		return
	}
	if vm.jitFrames == nil {
		vm.jitFrames = make([]*Frame, jitMaxDepth)
		for i := range vm.jitFrames {
			vm.jitFrames[i] = new(Frame)
		}
	}
	e := cc.entries[0]
	nl := cb.u.NumCalleeLocals
	// Another function of a cached executable: one case for all of them.
	for i := range ic.cases {
		cs := &ic.cases[i]
		if cs.cb != cb || cs.argc != int64(argc) {
			continue
		}
		if cs.callee == nil {
			return // a closure case already (whose code was disabled)
		}
		if cs.callee != o {
			cs.callee = nil
			return
		}
	}
	k := -1
	for i := range ic.cases {
		if ic.cases[i].callee == o && ic.cases[i].argc == int64(argc) || ic.cases[i].callee == nil && ic.cases[i].exec == nil {
			k = i
			break
		}
	}
	if k < 0 {
		k = int(ic.next)
		ic.next = (ic.next + 1) % callICCases
	}
	ic.cases[k] = callCase{callee: o, exec: j.exec, entry: cc.code.Entry() + uintptr(e.offset), ent: e, code: cc, cb: cb,
		nl: int64(nl), size: int64(nl + bytecode.CallFrameSlotThisArgument + max(argc, cb.u.NumParameters)),
		consts: unsafe.Pointer(unsafe.SliceData(cb.constants)),
		nvars:  int64(cb.u.NumVars), extra: int64(max(cb.u.NumParameters-argc, 0)), argc: int64(argc)}
}

// fillGetterIC caches the getter that get_by_id at pc of f's code read
// property k of base with, if the code calls getters (getterSite).
func (vm *VM) fillGetterIC(f *Frame, pc int, base Value, k PropertyKey) {
	code := f.cb.jit.code
	if code == nil || code.callICs == nil || code.callICs[pc] == nil {
		return
	}
	o := base.AsObject()
	if o == nil {
		o = vm.primitiveBase(base, k)
	}
	for h := o; h != nil && icOrdinary(h, k); h = h.proto {
		if i := h.props.find(k); i >= 0 {
			if p := h.props.at(i); p.IsAccessor() && p.Accessor().Get != nil {
				vm.fillCallIC(f, pc, ObjectValue(p.Accessor().Get), 1, false)
			}
			return
		}
	}
}

// finishNative turns the frames of the chain of calls generated code made
// itself (vm.jitFrames[:vm.jitDepth]) into ordinary frames and finishes
// them with the interpreter, innermost first. top is the frame whose code
// made the first call; the result is that call's.
func (vm *VM) finishNative(top *Frame) Value {
	n := vm.jitDepth
	frames := make([]*Frame, n)
	for i := range n {
		g := vm.allocFrame()
		*g = *vm.jitFrames[i]
		*vm.jitFrames[i] = Frame{}
		if i == 0 {
			g.caller = top
		} else {
			g.caller = frames[i-1]
		}
		frames[i] = g
	}
	vm.jitDepth = 0
	// The frames hold a call depth each (taken by the generated code) and
	// their registers, which are freed innermost first, also when a frame
	// throws.
	left := n
	defer func() {
		for i := left - 1; i >= 0; i-- {
			vm.releaseFrame(frames[i])
			vm.depth--
		}
		vm.currentFrame = top
	}()
	var result Value
	for i := n - 1; i >= 0; i-- {
		g := frames[i]
		vm.currentFrame = g
		exit := g.jitExit
		g.jitExit = 0
		if i == n-1 {
			result = vm.finishFrame(g, exit)
		} else {
			// g's code waits at its call for frames[i+1]'s result.
			at := int(exit >> jitExitBits)
			in := g.cb.u.Instructions[at:]
			setCallResult(g, in, result)
			result = vm.resumeFrame(g, at+bytecode.OpcodeID(in[0]).Length())
		}
		caller := g.caller
		vm.releaseFrame(g)
		vm.depth--
		left = i
		if tc := vm.pendingTailCall; tc != nil {
			// g ended in a proper tail call (the interpreter ran its
			// tail_call): make it now that g is gone.
			vm.pendingTailCall = nil
			vm.currentFrame = caller
			result = vm.callValue(ObjectValue(tc.callee), tc.this, tc.args)
		}
	}
	return result
}

// allocFrame takes a zero frame from the pool.
func (vm *VM) allocFrame() *Frame {
	if n := len(vm.freeFrames); n > 0 {
		f := vm.freeFrames[n-1]
		vm.freeFrames = vm.freeFrames[:n-1]
		return f
	}
	return new(Frame)
}

// finishFrame runs g, whose generated code stopped with exit code ret, to
// its return.
func (vm *VM) finishFrame(g *Frame, ret uintptr) Value {
	code := g.cb.jit.code
	if code == nil && ret&jitExitMask == jitExitNested {
		// g's code was thrown away while it ran (by another activation of
		// the same function), and a call its code made stopped: the frames
		// of that chain wait in vm.jitFrames. Finish them, then the
		// interpreter goes on after the call. (Running g from the call
		// instead left them there, counted in jitDepth; the next calls of
		// generated code stacked on them and, freeing them later, set sp
		// below live frames, whose registers were then cleared.)
		at := int(ret >> jitExitBits)
		g.pc = at
		result := vm.finishNative(g)
		in := g.cb.u.Instructions[at:]
		setCallResult(g, in, result)
		g.pc = at + bytecode.OpcodeID(in[0]).Length()
		return vm.run(g)
	}
	if code == nil || ret&jitExitMask == jitExitLeave {
		// Jettisoned while the frames inside it were finished or while g's
		// code was suspended, or the code stopped after its slow path ran
		// (jitSuspended).
		g.pc = int(ret >> jitExitBits)
		return vm.run(g)
	}
	ret = vm.jitContinue(g, code, ret)
	vm.checkReprofile(g.cb, code)
	if ret&jitExitMask == jitExitReturn {
		v := g.ret
		g.ret = Value{}
		return v
	}
	g.pc = int(ret >> jitExitBits)
	return vm.run(g)
}

// resumeFrame runs g from pc next on, in its generated code if there is an
// entry there.
func (vm *VM) resumeFrame(g *Frame, next int) Value {
	code := g.cb.jit.code
	if code == nil || code.resumes[next] == 0 || jit.BarrierEnabled() {
		// (code is nil when it was jettisoned while the frames inside g
		// were finished.)
		g.pc = next
		return vm.run(g)
	}
	o := code.resumes[next]
	g.jitFuel = jitFuel
	ret := vm.callJIT(code.code.Entry()+uintptr(o-1), uintptr(unsafe.Pointer(&g.regs[0])), uintptr(unsafe.Pointer(g)))
	code.code.KeepAlive()
	return vm.finishFrame(g, ret)
}

// setCallResult stores the result of the call or construct in to its
// destination (call_ignore_result has none).
func setCallResult(f *Frame, in []uint32, result Value) {
	switch bytecode.OpcodeID(in[0]) {
	case bytecode.OpCallID:
		f.set(bytecode.DecodeCall(in).Dst, result)
	case bytecode.OpTailCallID:
		f.set(bytecode.DecodeTailCall(in).Dst, result)
	case bytecode.OpConstructID:
		f.set(bytecode.DecodeConstruct(in).Dst, result)
	case bytecode.OpCallVarargsID:
		f.set(bytecode.DecodeCallVarargs(in).Dst, result)
	case bytecode.OpGetByIdID:
		// A getter the code called (getterSite).
		f.set(bytecode.DecodeGetById(in).Dst, result)
	}
}
