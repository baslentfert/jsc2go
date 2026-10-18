//go:build ((arm64 || amd64) && (darwin || linux)) || (amd64 && windows)

package vm

import (
	"fmt"
	"maps"
	"math"
	"math/bits"
	"os"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"
	"unsafe"

	"github.com/baslentfert/jsc2go/bytecode"
	"github.com/baslentfert/jsc2go/ident"
	"github.com/baslentfert/jsc2go/jit"
)

// The JIT tier. Hot loops and functions are compiled to machine code that
// works on the frame's registers in memory, like JSC's baseline JIT: every
// instruction loads its operands from the frame and stores its result back,
// so the interpreter can take over at any instruction. An instruction whose
// operands are not what the code expects (a guard fails) returns to the
// interpreter at that instruction, before changing anything; so does every
// instruction the tier does not compile. Calls return to Go, which makes
// them and goes back into the generated code after them.
//
// The code is written for package jit's macro assembler, which each
// architecture implements (arm64 and amd64).
//
// Garbage collector safety: generated code writes values (pointers
// included) without write barriers, so it only runs while the garbage
// collector is not marking (the barrier is off); then the interpreter runs
// the loop. The phase cannot change while generated code runs: that takes
// stopping the world, and generated code is not preemptible. Loops return
// to the interpreter every jitFuel iterations, so that the goroutine
// reaches safe points for the scheduler and the garbage collector soon.

var jitEnabled = jit.Supported && os.Getenv("JSC2GO_JIT") != "0"

var jitLog = os.Getenv("JSC2GO_JITLOG") != ""

// jitCalls counts calls into generated code (for tests); atomic, since
// VMs on several goroutines enter generated code at once.
var jitCalls atomic.Int64

// jitThreshold is how hot a code block gets before it is compiled: each
// call counts jitEntryIncrement and each loop iteration 1, as JSC's
// execution counter does (thresholdForJITAfterWarmUp 500,
// executionCounterIncrementForEntry 15, ...ForLoop 1), so a function is
// compiled after about 34 calls (JSC2GO_JITTHRESHOLD overrides it, for
// testing).
var jitThreshold = func() int {
	if n, err := strconv.Atoi(os.Getenv("JSC2GO_JITTHRESHOLD")); err == nil && n >= 0 {
		return n
	}
	return 500
}()

const jitEntryIncrement = 15

const (
	// jitFuel is the number of loop iterations generated code runs before
	// returning to the interpreter.
	jitFuel = 1 << 14
	// maxRegs bounds the frames compiled.
	maxRegs = 2000
)

// The code generator's registers (package jit's abstract ones).
const (
	rRegs  = jit.RRegs
	rFrame = jit.RFrame
	rMask  = jit.RMask
	rTmp   = jit.T0
)

// jitCode is a code block's compiled code.
type jitCode struct {
	code *jit.Code
	// entries are by loop_hint pc, and at 0 for the function. A slice, not
	// a map: every call of a compiled function and every loop_hint of a
	// compiled code block looks here.
	entries  []*jitEntry
	nentries int
	// resumes are the offsets plus one of the entry stubs after calls,
	// by the pc of the instruction after the call (0: none).
	resumes []int32
	// callICs are the call sites' inline caches by pc (jit_calls.go).
	callICs []*callIC
	// spec is set when the code checks type facts on entry from Go;
	// specFails counts guard exits it took (see jitSpecFails), failsAt
	// those by pc.
	spec      bool
	specFails int
	failsAt   map[int]int
	// switchTables and keys are what the code's switches read.
	switchTables [][]uintptr
	keys         []PropertyKey
	// cells are the shapes and objects the code compares with or reads
	// from as constants (specialized property accesses): kept alive, so
	// that their addresses are not reused.
	cells []any
	// reprofile is set by the code when a get_by_val or put_by_val site
	// that had never run before compiling met a typed array (reprofileExit).
	reprofile *uint32
	// inlineSites are the calls whose callee the code inlined;
	// inlineStop is set when one of them stopped too often.
	inlineSites []*inlineSite
	inlineStop  *uint32
	// optLoops are the loops of the code the optimizing tier looked at
	// (jit_opt.go): one sets optWanted when it got hot (counting) or its
	// optimized version gave up too often (disabled).
	optLoops  []*optLoop
	optWanted *uint32
}

// jitEntry is a place the interpreter can enter generated code at.
type jitEntry struct {
	offset          int // byte offset of the entry stub
	runs, guardExit int
	disabled        bool
}

// jitState is a code block's tier-up state.
type jitState struct {
	hotness int
	failed  bool
	code    *jitCode
	// noSpecAt are the pcs whose fact checks failed often (JSC's frequent
	// exit sites): code compiled again proves no facts for the instruction
	// before them.
	noSpecAt map[int]bool
	// noSpec is set when code was thrown away too often: it is compiled
	// without type facts.
	noSpec   bool
	jettison int
	// noInlineAt are the calls whose inlined callee stopped too often
	// (inlineExitLimit): code compiled again calls it.
	noInlineAt map[int]bool
	// undefinedAt are the relational comparisons whose slow path saw
	// undefined (loadCompared).
	undefinedAt map[int]bool
	// reprofiled counts the times code was thrown away for reprofile
	// (at most jitReprofileMax; then unprofiled sites stay general).
	reprofiled int
	// optHot are the loops (by loop_hint) that ran often enough to get an
	// optimized version (jit_opt.go); optNoKeep the registers an
	// optimized version may not keep (not numbers at its entry), optNever
	// the loops that get none, optTries how often one was disabled.
	optHot    map[int]bool
	optNoKeep map[int]map[bytecode.VirtualRegister]bool
	optNever  map[int]bool
	optTries  map[int]int
}

// tryJIT runs generated code from the loop_hint at pc, or from the start of
// the function for pc 0, if there is any; it returns the pc the interpreter
// continues at.
func (vm *VM) tryJIT(f *Frame, pc int) (int, bool) {
	cb := f.cb
	s := &cb.jit
	if s.code == nil {
		if s.failed {
			return 0, false
		}
		inc := 1
		if pc == 0 {
			inc = jitEntryIncrement
		}
		if s.hotness += inc; s.hotness < jitThreshold {
			return 0, false
		}
		if !jitCandidate(cb) {
			s.failed = true
			return 0, false
		}
		s.code = compileJIT(cb)
		if s.code == nil {
			s.failed = true
			return 0, false
		}
	}
	e := s.code.entries[pc]
	if e == nil || e.disabled || jit.BarrierEnabled() {
		return 0, false
	}
	code := s.code
	f.jitFuel = jitFuel
	jitCalls.Add(1)
	ret := vm.callJIT(code.code.Entry()+uintptr(e.offset), uintptr(unsafe.Pointer(&f.regs[0])),
		uintptr(unsafe.Pointer(f)))
	ret = vm.jitContinue(f, code, ret)
	if ret&jitExitMask == jitExitReturn {
		// A return: the interpreter runs it (enterJIT reads the value).
		f.ret = Value{}
		ret = ret&^jitExitMask | jitExitLeave
	}
	e.runs++
	vm.checkReprofile(cb, code)
	resume := int(ret >> jitExitBits)
	if ret&jitExitMask == jitExitGuard && code.spec {
		vm.specFailed(cb, code, resume)
	}
	if ret&jitExitMask == jitExitGuard {
		// A guard failed: if that happens a lot, stop entering here.
		if jitLog && e.guardExit < 3 {
			println("jit: guard exit at", resume, bytecode.OpcodeID(cb.u.Instructions[resume]).String())
		}
		if e.guardExit++; e.guardExit > 64 && e.guardExit*4 > e.runs {
			e.disabled = true
			if jitLog {
				println("jit: disabled entry at", pc)
			}
		}
	}
	return resume, true
}

// specFailed notes a guard exit of code, cb's speculative code: after
// jitSpecFails of them the code is thrown away (its entries disabled, so
// that call caches do not enter it either) and compiled again without type
// facts when it is hot again (JSC's jettison and reoptimization).
func (vm *VM) specFailed(cb *CodeBlock, code *jitCode, pc int) {
	s := &cb.jit
	if code.failsAt == nil {
		code.failsAt = map[int]int{}
	}
	code.failsAt[pc]++
	if code.specFails++; code.specFails < jitSpecFails || s.code != code {
		return
	}
	for _, e := range code.entries {
		if e != nil {
			e.disabled = true
		}
	}
	if s.noSpecAt == nil {
		s.noSpecAt = map[int]bool{}
	}
	for at, n := range code.failsAt {
		if n*4 >= code.specFails {
			s.noSpecAt[at] = true
		}
	}
	// Compiled again at the next entry; after a few rounds without facts.
	s.jettison++
	if vm.jitCalls > 0 {
		vm.jitGraveyard = append(vm.jitGraveyard, s.code) // maybe running, suspended
	}
	s.code, s.noSpec, s.hotness = nil, s.jettison > 3, jitThreshold
	if jitLog {
		println("jit: jettisoned", cb.name)
	}
}

// jitContinue goes on after f's generated code stopped with exit code ret:
// calls and slow paths, and the calls the code made itself whose callee's
// code stopped (finishNative), are finished here and the code goes on
// after them, until it stops for something else (a guard, fuel, a return
// or code that is not compiled), whose exit code it returns.
func (vm *VM) jitContinue(f *Frame, code *jitCode, ret uintptr) uintptr {
	defer code.code.KeepAlive()
	for {
		at := int(ret >> jitExitBits)
		var next int
		switch ret & jitExitMask {
		case jitExitCall:
			f.pc = at // for the exception handlers if the callee throws
			next = vm.jitSlowPath(f, at)
			if next == at {
				// The interpreter runs the instruction (a proper tail call).
				return uintptr(at)<<jitExitBits | jitExitLeave
			}
		case jitExitNative:
			f.pc = at
			next = vm.jitNativeCall(f, at)
		case jitExitNested:
			f.pc = at
			result := vm.finishNative(f)
			in := f.cb.u.Instructions[at:]
			setCallResult(f, in, result)
			next = at + bytecode.OpcodeID(in[0]).Length()
		default:
			return ret
		}
		// Back into the generated code after it (unless the collector
		// started marking).
		o := code.resumes[next]
		if o == 0 || jit.BarrierEnabled() {
			return uintptr(next)<<jitExitBits | jitExitLeave
		}
		ret = vm.callJIT(code.code.Entry()+uintptr(o-1), uintptr(unsafe.Pointer(&f.regs[0])),
			uintptr(unsafe.Pointer(f)))
	}
}

// How generated code returned: the low two bits of its result, above which
// is the pc to resume at.
const (
	jitExitGuard  = 0 // an operand was not what the code handles
	jitExitFuel   = 1 // the loop ran jitFuel iterations
	jitExitLeave  = 2 // control reached code that is not compiled
	jitExitCall   = 3 // a call or slow path, which Go makes
	jitExitReturn = 4 // a return, with the result in the frame's ret

	jitExitBits = 3
	jitExitMask = 1<<jitExitBits - 1
)

// jitCall makes the call instruction in (call or call_ignore_result) of f
// as the interpreter does, and returns its length.
func (vm *VM) jitCall(f *Frame, in []uint32) int {
	switch op := bytecode.OpcodeID(in[0]); op {
	case bytecode.OpCallID:
		b := bytecode.DecodeCall(in)
		this, args := f.callArgs(b.Argv, b.Argc)
		callee := f.get(b.Callee)
		// JavaScript functions directly (callValue's and call's work).
		if o := callee.AsObject(); o != nil {
			if j, ok := o.fn.(*jsFunction); ok && !j.classConstructor {
				f.set(b.Dst, vm.runFunction(j.codeBlock(o, bytecode.CodeForCall), o, j.scope, this, args))
				return op.Length()
			}
		}
		f.set(b.Dst, vm.callValue(callee, this, args))
		return op.Length()
	default:
		b := bytecode.DecodeCallIgnoreResult(in)
		this, args := f.callArgs(b.Argv, b.Argc)
		vm.callValue(f.get(b.Callee), this, args)
		return op.Length()
	}
}

// jitOps are the instructions the tier compiles (some only for some
// operands), plus the ones that leave to the interpreter without harm.
var jitOps = func() (set [bytecode.NumberOfBytecodeIDs]bool) {
	for _, op := range []bytecode.OpcodeID{
		bytecode.OpAddID, bytecode.OpBelowID, bytecode.OpBeloweqID, bytecode.OpBitandID, bytecode.OpBitnotID,
		bytecode.OpBitorID, bytecode.OpBitxorID, bytecode.OpCallID, bytecode.OpCallIgnoreResultID,
		bytecode.OpCheckTrapsID, bytecode.OpDecID, bytecode.OpDivID, bytecode.OpEnterID, bytecode.OpEqID,
		bytecode.OpGetByIdID, bytecode.OpGetByValID, bytecode.OpGetFromScopeID, bytecode.OpGetLengthID,
		bytecode.OpGreaterID, bytecode.OpGreatereqID, bytecode.OpIncID, bytecode.OpJbelowID, bytecode.OpJbeloweqID,
		bytecode.OpJeqID, bytecode.OpJfalseID, bytecode.OpJgreaterID, bytecode.OpJgreatereqID, bytecode.OpJlessID,
		bytecode.OpJlesseqID, bytecode.OpJmpID, bytecode.OpJneqID, bytecode.OpJngreaterID, bytecode.OpJngreatereqID,
		bytecode.OpJnlessID, bytecode.OpJnlesseqID, bytecode.OpJnstricteqID, bytecode.OpJnundefinedOrNullID,
		bytecode.OpJstricteqID, bytecode.OpJtrueID, bytecode.OpJundefinedOrNullID, bytecode.OpLessID,
		bytecode.OpLesseqID, bytecode.OpLoopHintID, bytecode.OpLshiftID, bytecode.OpModID, bytecode.OpMovID,
		bytecode.OpMulID, bytecode.OpNegateID, bytecode.OpNeqID, bytecode.OpNopID, bytecode.OpNstricteqID,
		bytecode.OpPutByIdID, bytecode.OpPutByValID, bytecode.OpPutToScopeID, bytecode.OpResolveScopeID,
		bytecode.OpRshiftID, bytecode.OpStricteqID, bytecode.OpSubID, bytecode.OpToNumberID, bytecode.OpToNumericID,
		bytecode.OpToPropertyKeyOrNumberID, bytecode.OpUnsignedID, bytecode.OpUrshiftID,
		bytecode.OpRetID, bytecode.OpThrowID, bytecode.OpThrowStaticErrorID,
		// jit_slow.go
		bytecode.OpToThisID, bytecode.OpCheckTdzID, bytecode.OpNotID, bytecode.OpJeqNullID, bytecode.OpJneqNullID,
		bytecode.OpEqNullID, bytecode.OpNeqNullID, bytecode.OpJeqPtrID, bytecode.OpJneqPtrID,
		bytecode.OpNewObjectID, bytecode.OpNewArrayID, bytecode.OpNewFuncID, bytecode.OpNewFuncExpID,
		bytecode.OpNewRegExpID, bytecode.OpNewArrayWithSizeID, bytecode.OpIteratorNextID,
		bytecode.OpCreateThisID, bytecode.OpConstructID, bytecode.OpIsObjectID, bytecode.OpTypeofIsUndefinedID,
		bytecode.OpToPrimitiveID, bytecode.OpInstanceofID, bytecode.OpStrcatID, bytecode.OpCallVarargsID,
		bytecode.OpCreateLexicalEnvironmentID, bytecode.OpCreateDirectArgumentsID,
		bytecode.OpGetFromArgumentsID, bytecode.OpPutToArgumentsID, bytecode.OpIsCellWithTypeID,
		bytecode.OpToStringID, bytecode.OpGetParentScopeID, bytecode.OpTailCallID, bytecode.OpTypeofID,
		bytecode.OpTypeofIsObjectID, bytecode.OpTypeofIsFunctionID, bytecode.OpIsNumberID,
	} {
		set[op] = true
	}
	for op := range set {
		if jitGenericOps[op] || jitColdOps[op] {
			set[op] = true
		}
	}
	set[bytecode.OpTailCallVarargsID] = true
	return
}()

// jitCandidate is a cheap check before compiling: a code block with a loop,
// or whose instructions the tier may all compile (it can be entered at its
// start). Others would only cost compiling.
func jitCandidate(cb *CodeBlock) bool {
	ins := cb.u.Instructions
	all := true
	for pc := 0; pc < len(ins); {
		op := bytecode.OpcodeID(ins[pc])
		if op == bytecode.OpLoopHintID {
			return true
		}
		if !jitOps[op] {
			all = false
		}
		pc += op.Length()
	}
	return all
}

// Process constants: the interface words of the objects generated code
// recognizes.
var (
	arrayObjectItab = func() uintptr {
		var e exoticObject = (*arrayObject)(nil)
		return (*[2]uintptr)(unsafe.Pointer(&e))[0]
	}()
	stringObjectItab = func() uintptr {
		var e exoticObject = stringObject{}
		return (*[2]uintptr)(unsafe.Pointer(&e))[0]
	}()
	immutablePrototypeItab = func() uintptr {
		var e exoticObject = immutablePrototypeObject{}
		return (*[2]uintptr)(unsafe.Pointer(&e))[0]
	}()
	argumentsObjectItab = func() uintptr {
		var e exoticObject = argumentsObject{}
		return (*[2]uintptr)(unsafe.Pointer(&e))[0]
	}()
	argumentsDataType = func() uintptr {
		var e any = (*argumentsData)(nil)
		return (*[2]uintptr)(unsafe.Pointer(&e))[0]
	}()
	typedArrayType = func() uintptr {
		var e any = (*typedArray)(nil)
		return (*[2]uintptr)(unsafe.Pointer(&e))[0]
	}()
	arrayBufferDataType = func() uintptr {
		var e any = (*arrayBufferData)(nil)
		return (*[2]uintptr)(unsafe.Pointer(&e))[0]
	}()
	dataViewType = func() uintptr {
		var e any = (*dataView)(nil)
		return (*[2]uintptr)(unsafe.Pointer(&e))[0]
	}()
)

// Layouts generated code reads.
var (
	offElements      = int(unsafe.Offsetof(Object{}.elements))
	offExotic        = int(unsafe.Offsetof(Object{}.exotic))
	offInternal      = int(unsafe.Offsetof(Object{}.internal))
	offClass         = int(unsafe.Offsetof(Object{}.class))
	offProps         = int(unsafe.Offsetof(Object{}.props))
	offProto         = int(unsafe.Offsetof(Object{}.proto))
	offArrayLength   = int(unsafe.Offsetof(arrayObject{}.length))
	offArgsMapped    = int(unsafe.Offsetof(argumentsData{}.mapped))
	offArgsNumMapped = int(unsafe.Offsetof(argumentsData{}.numMapped))
	offArgsDirect    = int(unsafe.Offsetof(argumentsData{}.direct))
	offArgsScope     = int(unsafe.Offsetof(argumentsData{}.scope))
	offTAKind        = int(unsafe.Offsetof(typedArray{}.kind))
	offTABuffer      = int(unsafe.Offsetof(typedArray{}.buffer))
	offTAByteOff     = int(unsafe.Offsetof(typedArray{}.byteOffset))
	offTALength      = int(unsafe.Offsetof(typedArray{}.arrayLength))
	offTAVector      = int(unsafe.Offsetof(typedArray{}.vector))
	offTAVLen        = int(unsafe.Offsetof(typedArray{}.vlen))
	offTAVEpoch      = int(unsafe.Offsetof(typedArray{}.vepoch))
	offBufData       = int(unsafe.Offsetof(arrayBufferData{}.data))
	offBufDetached   = int(unsafe.Offsetof(arrayBufferData{}.detached))
	offDVBuffer      = int(unsafe.Offsetof(dataView{}.buffer))
	offDVByteOffset  = int(unsafe.Offsetof(dataView{}.byteOffset))
	offDVByteLength  = int(unsafe.Offsetof(dataView{}.byteLength))
	offStrLength     = int(unsafe.Offsetof(String{}.length))
	offFrameScope    = int(unsafe.Offsetof(Frame{}.scope))
	offFrameFuel     = int(unsafe.Offsetof(Frame{}.jitFuel))
	offFrameRet      = int(unsafe.Offsetof(Frame{}.ret))
	offShapeVer      = int(unsafe.Offsetof(shape{}.version))
	offGCShape       = int(unsafe.Offsetof(getByIdCache{}.shape))
	offGCVersion     = int(unsafe.Offsetof(getByIdCache{}.version))
	offGCDepth       = int(unsafe.Offsetof(getByIdCache{}.depth))
	offGCChain       = int(unsafe.Offsetof(getByIdCache{}.chain))
	offGCEpoch       = int(unsafe.Offsetof(getByIdCache{}.epoch))
	offGCHolder      = int(unsafe.Offsetof(getByIdCache{}.holder))
	offPCEpoch       = int(unsafe.Offsetof(putByIdCache{}.epoch))
	offObjectWatched = int(unsafe.Offsetof(Object{}.watched))
	offGCIndex       = int(unsafe.Offsetof(getByIdCache{}.index))
	offGCAccessor    = int(unsafe.Offsetof(getByIdCache{}.accessor))
	offGCMiss        = int(unsafe.Offsetof(getByIdCache{}.miss))
	offAccessorGet   = int(unsafe.Offsetof(Accessor{}.Get))
	offGCPoly        = int(unsafe.Offsetof(getByIdCache{}.poly))
	offGCPolyEntries = int(unsafe.Offsetof(getByIdPoly{}.entries))
	sizeGetByIdCache = int(unsafe.Sizeof(getByIdCache{}))
	sizeProtoLink    = int(unsafe.Sizeof(protoLink{}))
	offLinkObj       = int(unsafe.Offsetof(protoLink{}.obj))
	offLinkShape     = int(unsafe.Offsetof(protoLink{}.shape))
	offLinkVersion   = int(unsafe.Offsetof(protoLink{}.version))
	offPCShape       = int(unsafe.Offsetof(putByIdCache{}.shape))
	offPCVersion     = int(unsafe.Offsetof(putByIdCache{}.version))
	offPCIndex       = int(unsafe.Offsetof(putByIdCache{}.index))
	offPCNewShape    = int(unsafe.Offsetof(putByIdCache{}.newShape))
	offPCPoly        = int(unsafe.Offsetof(putByIdCache{}.poly))
	offPCDepth       = int(unsafe.Offsetof(putByIdCache{}.depth))
	offPCHolder      = int(unsafe.Offsetof(putByIdCache{}.holder))
	offPCChain       = int(unsafe.Offsetof(putByIdCache{}.chain))

	offObjectExtensible = int(unsafe.Offsetof(Object{}.extensible))
	offPCPolyEntries    = int(unsafe.Offsetof(putByIdPoly{}.entries))
	sizePutByIdCache    = int(unsafe.Sizeof(putByIdCache{}))
	offScopeKind        = int(unsafe.Offsetof(Scope{}.kind))
	offScopeParent      = int(unsafe.Offsetof(Scope{}.parent))
	offScopeTable       = int(unsafe.Offsetof(Scope{}.table))
	offScopeSlots       = int(unsafe.Offsetof(Scope{}.slots))
	offScopeObject      = int(unsafe.Offsetof(Scope{}.object))
	offScopeEval        = int(unsafe.Offsetof(Scope{}.evalVars))
	offRSRealm          = int(unsafe.Offsetof(resolveScopeCache{}.realm))
	offRSGen            = int(unsafe.Offsetof(resolveScopeCache{}.gen))
	offRSResult         = int(unsafe.Offsetof(resolveScopeCache{}.result))
	offRSScope          = int(unsafe.Offsetof(resolveScopeCache{}.scope))
	offRSInj            = int(unsafe.Offsetof(resolveScopeCache{}.inj))
	offRSTable          = int(unsafe.Offsetof(resolveScopeCache{}.table))
	offRSDepth          = int(unsafe.Offsetof(resolveScopeCache{}.depth))
	offSSTable          = int(unsafe.Offsetof(scopeSlotCache{}.table))
	offSSOffset         = int(unsafe.Offsetof(scopeSlotCache{}.offset))
)

// jitCompiler compiles one code block.
// Type facts (pakket 2, #1/#2), after JSC's DFG: within a basic block the
// compiler knows which registers hold numbers, from the checks and
// results of earlier instructions, and loadNum omits its tag check for
// them. Memory stays current at every instruction, so exits need nothing.
// But slow paths resume in the code: a fact an instruction's fast path
// proved (an operand loadNum checked, a number it stored) is checked
// again where its slow path resumes, as JSC's DFG checks values on OSR
// entry; if that check fails the code exits (a guard exit), and code
// whose speculation fails often is thrown away and compiled again without
// facts (as JSC jettisons code that exits often).
const (
	factNumber uint8 = 1 << iota
)

// jitSpec turns type facts on (JSC2GO_JITSPEC=0 turns them off).
var jitSpec = os.Getenv("JSC2GO_JITSPEC") != "0"

// jitSpecFails is how many failed fact checks make speculative code be
// thrown away.
const jitSpecFails = 16

type jitFact struct {
	r     bytecode.VirtualRegister
	f     uint8
	store bool // about the value the instruction stored to r
}

// jitFCache are the registers that keep numbers across instructions
// (pakket 2, #4), after JSC's DFG register bank in small: a number an
// instruction stored is kept in one of them too (write-through, so that
// memory stays current and spilling is free), and loadNum of that register
// takes it from there. They are forgotten at block starts, when the
// register is written, and at calls (callee code uses them); where a slow
// path resumes they are filled again from memory.
var jitFCache = [...]jit.F{4, 5}

type jitFEntry struct {
	r     bytecode.VirtualRegister
	valid bool
}

type jitCompiler struct {
	// switches are the jump tables to fill once the code is placed
	// (jit_switch.go); keepKeys the property keys whose atoms it compares.
	switches []jitSwitch
	keepKeys []PropertyKey
	// keepCells become jitCode.cells.
	keepCells []any

	// fcache is what jitFCache hold at the instruction being compiled
	// (their use order in fuse); pendingF are the stores of the current
	// instruction; fromF reports whether the last loadNum took its value
	// from the cache; refills are the cache states to load at resume pcs.
	fcache   [len(jitFCache)]jitFEntry
	fuse     uint8
	pendingF []jitFact
	fromF    bool
	curOp    bytecode.OpcodeID
	refills  map[int][len(jitFCache)]jitFEntry

	// facts are the type facts at the instruction being compiled; pending
	// are those it proves (applied after it for factProducers); factsOn is
	// false while cold code is emitted. slowResumes are the pcs its slow
	// paths resume at, and resumeChecks the facts to check at a resume pc.
	facts        map[bytecode.VirtualRegister]uint8
	defs         []bytecode.VirtualRegister // of the instruction (applyFacts)
	addDef       func(bytecode.VirtualRegister)
	pending      []jitFact
	factsOn      bool
	slowResumes  []int
	resumeChecks map[int][]jitFact

	cb        *CodeBlock
	m         jit.Masm
	nl        int
	labels    map[int]*jit.Label // instruction starts
	exits     map[int]*jit.Label // guard exits by pc
	fuel      map[int]*jit.Label // fuel exits by pc
	leaves    map[int]*jit.Label // exits to code that is not compiled, by pc
	callExits map[int]*jit.Label // exits to calls and slow paths, by pc
	resumeAt  map[int]bool       // pcs generated code continues at after those

	nestedExits map[int]*jit.Label // calls whose callee's code stopped, by pc
	nativeExits map[int]*jit.Label // calls of builtins without a thunk, by pc
	callICs     []*callIC          // by pc
	getter      *getterSite        // the get_by_id being compiled

	// cold are the rarely run parts of instructions, emitted after all
	// instructions so that they do not fill the hot code (and the
	// instruction cache) in between.
	cold []func()
	// reprofile is the code's jitCode.reprofile (reprofileExit).
	reprofile *uint32
	// inline is set in a compiler of an inlined callee (jit_inline.go);
	// inlineSites are the calls this compiler inlined, inlineStop the
	// code's jitCode.inlineStop.
	inline      *inlineCtx
	inlineSites []*inlineSite
	inlineStop  *uint32
	inlineBytes int // the machine code of its inlined calls
	inlineNeed  int // registers above sp its inlined calls use, at most

	// opt is the optimized loop being compiled (jit_opt.go), optLoops
	// those of the code by loop_hint, optGraph and optLiveAt liveness.
	opt       *optLoop
	optLoops  map[int]*optLoop
	optWanted *uint32                           // jitCode.optWanted
	optStarts []optStart                        // the optimized versions' instructions (JSC2GO_JITDUMP)
	fcacheAt  map[int][len(jitFCache)]jitFEntry // the number cache at each instruction
	optGraph  *bytecode.Graph
	optLiveAt map[int]bytecode.BitVector
}

// jitForceFar makes all code use long-reaching branches (for tests).
var jitForceFar bool

func compileJIT(cb *CodeBlock) *jitCode {
	makeJITThunks()
	code, outOfRange := compileJITWith(cb, jitForceFar)
	if outOfRange {
		// Huge code blocks (Emscripten's): long-reaching branches.
		code, _ = compileJITWith(cb, true)
	}
	return code
}

func compileJITWith(cb *CodeBlock, far bool) (*jitCode, bool) {
	u := cb.u
	if u.NumCalleeLocals+bytecode.CallFrameSlotThisArgument+max(u.NumParameters, 1) > maxRegs {
		return nil, false
	}
	c := &jitCompiler{cb: cb, nl: u.NumCalleeLocals, labels: map[int]*jit.Label{},
		exits: map[int]*jit.Label{}, fuel: map[int]*jit.Label{}, leaves: map[int]*jit.Label{},
		callExits: map[int]*jit.Label{}, resumeAt: map[int]bool{}, nestedExits: map[int]*jit.Label{},
		nativeExits: map[int]*jit.Label{}}
	m := &c.m
	// About the code a word of bytecode makes (140-280 bytes in mandreel
	// and Box2D), in the VM's buffer, which it keeps.
	m.ReserveIn(&cb.realm.vm.jitBuf, 64*len(u.Instructions))
	defer c.m.Release(&cb.realm.vm.jitBuf)
	m.SetFar(far)
	ins := u.Instructions
	if jit.NativeCalls {
		c.callICs = make([]*callIC, len(ins))
	}
	var pcs []int
	for pc := 0; pc < len(ins); pc += bytecode.OpcodeID(ins[pc]).Length() {
		pcs = append(pcs, pc)
		c.labels[pc] = jit.NewLabel()
	}
	supported := map[int]bool{}
	starts := map[int]int{} // byte offset by pc (for JSC2GO_JITDUMP)
	spec := jitSpec && !cb.jit.noSpec
	blockStart := map[int]bool{0: true}
	for _, t := range bytecode.ComputePreciseJumpTargets(u, ins) {
		blockStart[t] = true
	}
	c.facts = map[bytecode.VirtualRegister]uint8{}
	c.resumeChecks = map[int][]jitFact{}
	c.refills = map[int][len(jitFCache)]jitFEntry{}
	if jitOpt && jit.NativeCalls {
		c.planOptLoops(pcs)
		c.fcacheAt = map[int][len(jitFCache)]jitFEntry{}
	}
	for _, pc := range pcs {
		m.Bind(c.labels[pc])
		start := m.Len()
		starts[pc] = start
		if blockStart[pc] {
			clear(c.facts)
			c.fcache = [len(jitFCache)]jitFEntry{}
		}
		c.pending, c.slowResumes, c.factsOn = c.pending[:0], c.slowResumes[:0], spec
		c.pendingF, c.curOp = c.pendingF[:0], bytecode.OpcodeID(ins[pc])
		if c.optLoops != nil {
			c.fcacheAt[pc] = c.fcache
		}
		ok := c.instruction(pc, ins[pc:])
		c.factsOn = false
		if ok {
			supported[pc] = true
			c.applyFacts(pc, ins[pc:], blockStart)
			continue
		}
		clear(c.facts)
		c.fcache = [len(jitFCache)]jitFEntry{}
		if jitLog {
			println("jit: unsupported", bytecode.OpcodeID(ins[pc]).String(), "at", pc)
		}
		var labels []*jit.Label
		for _, ls := range []map[int]*jit.Label{c.exits, c.leaves, c.callExits, c.fuel, c.labels, c.nestedExits, c.nativeExits} {
			for _, l := range ls {
				labels = append(labels, l)
			}
		}
		m.Truncate(start, labels...)
		m.B(c.leave(pc))
	}
	c.emitOptLoops()
	// Entries: loops whose instructions are all compiled. A loop is a
	// backward jump to its header; its loop_hint is the first one at or
	// after the header.
	if c.reprofile == nil {
		c.reprofile = new(uint32) // for noteUndefinedCompare too
	}
	code := &jitCode{entries: make([]*jitEntry, len(ins)), resumes: make([]int32, len(ins)), callICs: c.callICs, reprofile: c.reprofile,
		inlineSites: c.inlineSites, inlineStop: c.inlineStop}
	for _, hint := range slices.Sorted(maps.Keys(c.optLoops)) {
		code.optLoops = append(code.optLoops, c.optLoops[hint])
		code.optWanted = c.optWanted
	}
	type loop struct{ start, end int }
	loops := map[int]loop{} // by loop_hint pc
	for _, j := range pcs {
		t, ok := jumpTarget(j, ins[j:])
		if !ok || t > j {
			continue
		}
		hint := -1
		for _, pc := range pcs {
			if pc >= t && pc <= j && bytecode.OpcodeID(ins[pc]) == bytecode.OpLoopHintID {
				hint = pc
				break
			}
		}
		if hint < 0 {
			continue
		}
		l, seen := loops[hint]
		if !seen {
			l = loop{t, j}
		}
		l.start, l.end = min(l.start, t), max(l.end, j)
		loops[hint] = l
	}
	for hint, l := range loops {
		ok := true
		for _, pc := range pcs {
			if pc >= l.start && pc <= l.end && !supported[pc] {
				// Throws exit to the interpreter; they are not taken in
				// loops that run long. A catch is reached only by an
				// exception, which the interpreter handles: it goes on at
				// the handler and enters the code again at the loop_hint.
				if op := bytecode.OpcodeID(ins[pc]); op == bytecode.OpThrowID || op == bytecode.OpThrowStaticErrorID || op == bytecode.OpCatchID {
					continue
				}
				ok = false
				if jitLog {
					println("jit: loop blocked by", bytecode.OpcodeID(ins[pc]).String())
				}
				break
			}
		}
		if !ok {
			continue
		}
		code.entries[hint] = &jitEntry{offset: m.Len()}
		code.nentries++
		c.entryStub(c.labels[hint])
	}
	// Functions all of whose instructions are compiled are entered at
	// their start (when called often), not just at their loops.
	whole := true
	for _, pc := range pcs {
		if !supported[pc] {
			switch bytecode.OpcodeID(ins[pc]) {
			case bytecode.OpRetID, bytecode.OpThrowID, bytecode.OpThrowStaticErrorID:
				continue // they leave to the interpreter
			}
			whole = false
			break
		}
	}
	if whole && len(pcs) > 0 {
		code.entries[0] = &jitEntry{offset: m.Len()}
		code.nentries++
		c.entryStub(c.labels[0])
	}
	for _, pc := range slices.Sorted(maps.Keys(c.resumeAt)) {
		if l := c.labels[pc]; l != nil && pc < len(ins) {
			code.resumes[pc] = int32(m.Len() + 1)
			checks, refill := c.resumeChecks[pc], c.refills[pc]
			if len(checks) > 0 || refill != ([len(jitFCache)]jitFEntry{}) {
				// Facts the fast path proved, checked on entry from Go,
				// and the number cache filled again from memory.
				check := jit.NewLabel()
				c.entryStub(check)
				c.cold = append(c.cold, func() {
					m.Bind(check)
					for _, f := range checks {
						m.Load64(rTmp, rRegs, c.off(f.r)+8)
						m.Branch64Imm(jit.ULE, rTmp, maxTag, c.exit(pc))
					}
					for k, e := range refill {
						if e.valid {
							m.Load64(rTmp, rRegs, c.off(e.r)+8)
							m.Xor64(rTmp, rTmp, rMask)
							m.MovToF(jitFCache[k], rTmp)
						}
					}
					m.B(l)
				})
				if len(checks) > 0 {
					code.spec = true
				}
			} else {
				c.entryStub(l)
			}
		}
	}
	if jitLog {
		println("jit: compiled", cb.name, "loops", len(loops), "entries", code.nentries)
	}
	if code.nentries == 0 {
		return nil, false
	}
	// Cold code may add more cold code.
	for i := 0; i < len(c.cold); i++ {
		c.cold[i]()
	}
	var suspend *jit.Label
	for kind, ls := range []map[int]*jit.Label{jitExitGuard: c.exits, jitExitFuel: c.fuel, jitExitLeave: c.leaves,
		jitExitCall: c.callExits, jitExitNested: c.nestedExits, jitExitNative: c.nativeExits} {
		for pc, l := range ls {
			m.Bind(l)
			word := uint64(pc)<<jitExitBits | uint64(kind)
			if (kind == jitExitCall || kind == jitExitNative) && suspendThunk != 0 {
				// Go makes the call or slow path while the callers'
				// code waits, if there are callers (jit_suspend.go).
				if suspend == nil {
					suspend = jit.NewLabel()
				}
				m.MovImm(jit.S0, word)
				m.B(suspend)
				continue
			}
			m.Return(word)
		}
	}
	if suspend != nil {
		m.Bind(suspend)
		plain := jit.NewLabel()
		m.Load64(jit.S2, rFrame, offFrameVM)
		m.Load64(jit.S3, jit.S2, offVMJitDepth)
		m.BranchZero(jit.S3, plain)
		m.MovImm(jit.S1, uint64(uintptr(unsafe.Pointer(code))))
		m.MovImm(jit.S2, uint64(suspendThunk))
		m.JumpReg(jit.S2)
		m.Bind(plain)
		m.ReturnReg(jit.S0) // the exit word to Go, as Return does
	}
	if n := m.Unbound(); n > 0 {
		// A branch to a label that was never bound jumps into the code at
		// random: a compiler bug, never run (and loud in tests).
		if testing.Testing() {
			panic(fmt.Sprintf("jit: %d unbound labels in %s", n, cb.name))
		}
		if jitLog {
			println("jit: unbound labels", n)
		}
		return nil, false
	}
	if m.OutOfRange() {
		if jitLog {
			println("jit: out of branch range", cb.name, m.Len())
		}
		return nil, true // too big for conditional branches to reach the exits
	}
	mem, err := jit.NewBytes(m.Code())
	if err != nil {
		return nil, false
	}
	code.code = mem
	for _, sw := range c.switches {
		for i, l := range sw.labels {
			sw.table[i] = mem.Entry() + uintptr(m.LabelOffset(l))
		}
		code.switchTables = append(code.switchTables, sw.table)
	}
	code.keys = c.keepKeys
	code.cells = c.keepCells
	if os.Getenv("JSC2GO_JITDUMP") != "" {
		println("jit: code", cb.name, "at", mem.Entry(), "size", m.Len())
		for _, pc := range pcs {
			println("jit:   ", mem.Entry()+uintptr(starts[pc]), pc, bytecode.OpcodeID(ins[pc]).String())
		}
		for _, s := range c.optStarts {
			println("jit:   ", mem.Entry()+uintptr(s.offset), s.pc, "opt:"+bytecode.OpcodeID(ins[s.pc]).String())
		}
	}
	return code, false
}

// entryStub emits an entry from Go to label l. In code with inlined calls
// it first clears the frame's inlineRoom: the activation may have checked
// it for other code (thrown away since), which needed less.
func (c *jitCompiler) entryStub(l *jit.Label) {
	m := &c.m
	if c.inlineNeed == 0 {
		m.EntryStub(l)
		return
	}
	clear := jit.NewLabel()
	m.EntryStub(clear)
	c.cold = append(c.cold, func() {
		m.Bind(clear)
		m.StoreZero64(rFrame, offFrameInlineRoom)
		m.B(l)
	})
}

// jumpTarget returns the target of a jump instruction.
func jumpTarget(pc int, in []uint32) (int, bool) {
	switch op := bytecode.OpcodeID(in[0]); op {
	case bytecode.OpJmpID:
		return pc + int(bytecode.DecodeJmp(in).TargetLabel), true
	case bytecode.OpJtrueID:
		return pc + int(bytecode.DecodeJtrue(in).TargetLabel), true
	case bytecode.OpJfalseID:
		return pc + int(bytecode.DecodeJfalse(in).TargetLabel), true
	case bytecode.OpJbelowID:
		return pc + int(bytecode.DecodeJbelow(in).TargetLabel), true
	case bytecode.OpJbeloweqID:
		return pc + int(bytecode.DecodeJbeloweq(in).TargetLabel), true
	case bytecode.OpJeqNullID:
		return pc + int(bytecode.DecodeJeqNull(in).TargetLabel), true
	case bytecode.OpJneqNullID:
		return pc + int(bytecode.DecodeJneqNull(in).TargetLabel), true
	case bytecode.OpJeqPtrID:
		return pc + int(bytecode.DecodeJeqPtr(in).TargetLabel), true
	case bytecode.OpJneqPtrID:
		return pc + int(bytecode.DecodeJneqPtr(in).TargetLabel), true
	}
	if l, _, _, ok := compareJump(in); ok {
		return pc + int(l), true
	}
	return 0, false
}

func (c *jitCompiler) exit(pc int) *jit.Label {
	if o := c.opt; o != nil {
		return c.deopt(o, pc, nil)
	}
	l := c.exits[pc]
	if l == nil {
		l = jit.NewLabel()
		c.exits[pc] = l
	}
	return l
}

// leave is the exit to the interpreter for control reaching pc, which is
// not compiled.
func (c *jitCompiler) leave(pc int) *jit.Label {
	if o := c.opt; o != nil {
		c.opt = nil
		l := c.leave(pc)
		c.opt = o
		return c.flushTo(o, pc, l)
	}
	l := c.leaves[pc]
	if l == nil {
		l = jit.NewLabel()
		c.leaves[pc] = l
	}
	return l
}

func (c *jitCompiler) target(pc int, label bytecode.BoundLabel) *jit.Label {
	t := pc + int(label)
	if o := c.opt; o != nil {
		if l := o.labels[t]; l != nil {
			return l
		}
		if l := c.labels[t]; l != nil {
			return c.flushTo(o, t, l)
		}
	}
	if l := c.labels[t]; l != nil {
		return l
	}
	return c.leave(t)
}

// off is the offset of register r from regs[0].
func (c *jitCompiler) off(r bytecode.VirtualRegister) int {
	if r >= bytecode.FirstConstantRegisterIndex {
		// A constant is not in the frame (c.constant): its offset would
		// read far beyond it (call_varargs with a constant this, and
		// appendString's rhs, did).
		panic("jit: register offset of a constant")
	}
	return (c.nl + int(r)) * valueSize
}

func (c *jitCompiler) constant(r bytecode.VirtualRegister) (Value, bool) {
	if r >= bytecode.FirstConstantRegisterIndex {
		return c.cb.constants[int(r)-bytecode.FirstConstantRegisterIndex], true
	}
	return Value{}, false
}

// loadNum loads number register r into d (leaving its bits in rTmp); it
// reports false if r is a constant that is not a number (the instruction is
// not compiled).
func (c *jitCompiler) loadNum(d jit.F, r bytecode.VirtualRegister, exit *jit.Label) bool {
	m := &c.m
	if v, ok := c.constant(r); ok {
		if !v.IsNumber() {
			return false
		}
		m.MovImm(rTmp, math.Float64bits(v.num()))
		m.MovToF(d, rTmp)
		return true
	}
	c.fromF = false
	if f, ok := c.optReg(r); ok {
		m.FMov(d, f)
		c.fromF = true
		return true
	}
	if c.factsOn {
		for k, e := range c.fcache {
			if e.valid && e.r == r {
				// In the number cache (its fact holds too).
				m.FMov(d, jitFCache[k])
				c.fromF = true
				c.fuse = uint8(k)
				return true
			}
		}
	}
	m.Load64(rTmp, rRegs, c.off(r)+8)
	if c.factsOn && c.facts[r]&factNumber != 0 {
		// Known to be a number: no check (type facts).
	} else {
		m.Branch64Imm(jit.ULE, rTmp, maxTag, exit)
		if c.factsOn {
			c.pending = append(c.pending, jitFact{r, factNumber, false})
		}
	}
	m.Xor64(rTmp, rTmp, rMask)
	m.MovToF(d, rTmp)
	return true
}

// loadCompared is loadNum for an operand of the relational comparison at
// pc. Where the comparison's slow path saw undefined (jitState.undefinedAt;
// 3d-raytrace calls a method without its last argument and compares with
// it half a million times a run), undefined loads NaN, its ToNumber, which
// every comparison is false with; the operand then gets no number fact.
func (c *jitCompiler) loadCompared(pc int, d jit.F, r bytecode.VirtualRegister, exit *jit.Label) bool {
	if _, ok := c.constant(r); ok || !c.cb.jit.undefinedAt[pc] {
		return c.loadNum(d, r, exit)
	}
	if _, ok := c.optReg(r); ok {
		return c.loadNum(d, r, exit)
	}
	m := &c.m
	c.fromF = false
	number, done := jit.NewLabel(), jit.NewLabel()
	m.Load64(rTmp, rRegs, c.off(r)+8)
	m.Branch64Imm(jit.UGT, rTmp, maxTag, number)
	m.BranchNonZero(rTmp, exit) // not undefined
	m.MovImm(rTmp, math.Float64bits(math.NaN()))
	m.MovToF(d, rTmp)
	m.B(done)
	m.Bind(number)
	m.Xor64(rTmp, rTmp, rMask)
	m.MovToF(d, rTmp)
	m.Bind(done)
	return true
}

// noteUndefinedCompare notes, in the slow path of a relational comparison
// at pc of f's code, an undefined operand: the code is compiled again with
// loadCompared's path for it (as reprofileExit, at most jitReprofileMax
// times per code block).
func (vm *VM) noteUndefinedCompare(f *Frame, pc int, lhs, rhs bytecode.VirtualRegister) {
	s := &f.cb.jit
	if s.code == nil || s.code.reprofile == nil || s.reprofiled >= jitReprofileMax || s.undefinedAt[pc] {
		return
	}
	if !f.ref(lhs).IsUndefined() && !f.ref(rhs).IsUndefined() {
		return
	}
	if s.undefinedAt == nil {
		s.undefinedAt = map[int]bool{}
	}
	s.undefinedAt[pc] = true
	*s.code.reprofile = 1
}

// storeWord stores a value without a pointer, whose u word is x, to
// register r.
func (c *jitCompiler) storeWord(r bytecode.VirtualRegister, x jit.R) {
	c.m.StoreZero64(rRegs, c.off(r))
	c.m.Store64(x, rRegs, c.off(r)+8)
}

// loadValue loads both words of r into p and u.
func (c *jitCompiler) loadValue(p, u jit.R, r bytecode.VirtualRegister) {
	if v, ok := c.constant(r); ok {
		// The code block's constants keep a pointer alive.
		c.m.MovImm(p, uint64(uintptr(v.p)))
		c.m.MovImm(u, v.u)
		return
	}
	if f, ok := c.optReg(r); ok {
		// Kept in a register by an optimized loop: the number boxed.
		c.m.MovFromF(u, f)
		c.m.Xor64(u, u, rMask)
		c.m.MovImm(p, 0)
		return
	}
	c.m.Load64(p, rRegs, c.off(r))
	c.m.Load64(u, rRegs, c.off(r)+8)
}

// storeValue stores the value p, u to register r.
func (c *jitCompiler) storeValue(r bytecode.VirtualRegister, p, u jit.R) {
	c.m.Store64(p, rRegs, c.off(r))
	c.m.Store64(u, rRegs, c.off(r)+8)
}

func (c *jitCompiler) storeNum(r bytecode.VirtualRegister, d jit.F) {
	if f, ok := c.optReg(r); ok {
		c.m.FMov(f, d)
		return
	}
	c.m.MovFromF(rTmp, d)
	c.m.Xor64(rTmp, rTmp, rMask)
	c.storeWord(r, rTmp)
	if c.factsOn {
		c.pending = append(c.pending, jitFact{r, factNumber, true})
		if factProducers[c.curOp] {
			k := c.fslot(r)
			c.m.FMov(jitFCache[k], d)
			c.pendingF = append(c.pendingF, jitFact{r: r, f: uint8(k)})
		}
	}
}

// fslot is the cache register to keep r in: the one that has it, else
// the one not used last.
func (c *jitCompiler) fslot(r bytecode.VirtualRegister) int {
	for k, e := range c.fcache {
		if e.valid && e.r == r {
			return k
		}
	}
	for _, f := range c.pendingF {
		if f.r == r {
			return int(f.f)
		}
	}
	return (int(c.fuse) + 1) % len(jitFCache)
}

// jitClobbersF are the instructions whose code may call other generated
// code (directCall), which uses the number cache registers.
var jitClobbersF = func() (s [bytecode.NumberOfBytecodeIDs]bool) {
	for _, op := range []bytecode.OpcodeID{bytecode.OpCallID, bytecode.OpCallIgnoreResultID, bytecode.OpConstructID, bytecode.OpCallVarargsID,
		bytecode.OpTailCallID, bytecode.OpGetByIdID} {
		s[op] = true
	}
	return
}()

// factProducers are the instructions whose fast path loads its operands
// with loadNum unconditionally (or goes to its slow path) and stores a
// number to its destination with storeNum on every path: their pending
// facts hold where they continue. Others' are dropped.
var factProducers = func() (s [bytecode.NumberOfBytecodeIDs]bool) {
	for _, op := range []bytecode.OpcodeID{bytecode.OpAddID, bytecode.OpSubID, bytecode.OpMulID, bytecode.OpDivID,
		bytecode.OpNegateID, bytecode.OpIncID, bytecode.OpDecID,
		bytecode.OpLessID, bytecode.OpLesseqID, bytecode.OpGreaterID, bytecode.OpGreatereqID,
		bytecode.OpJlessID, bytecode.OpJlesseqID, bytecode.OpJgreaterID, bytecode.OpJgreatereqID,
		bytecode.OpJnlessID, bytecode.OpJnlesseqID, bytecode.OpJngreaterID, bytecode.OpJngreatereqID,
		// Their operands go through int32Of (booleans too): only their
		// results, numbers, make facts.
		bytecode.OpBitandID, bytecode.OpBitorID, bytecode.OpBitxorID, bytecode.OpLshiftID, bytecode.OpRshiftID,
		bytecode.OpUrshiftID, bytecode.OpBitnotID, bytecode.OpUnsignedID} {
		s[op] = true
	}
	return
}()

// storeBool stores the boolean in x (0 or 1) to r.
func (c *jitCompiler) storeBool(r bytecode.VirtualRegister, x jit.R) {
	c.m.AddImm64(x, x, uint32(tagFalse))
	c.storeWord(r, x)
}

// int32Of loads r as an int32 into x: ToInt32 of a number, a boolean
// (Emscripten's code combines comparisons with & and |), undefined or null.
func (c *jitCompiler) int32Of(x jit.R, r bytecode.VirtualRegister, exit *jit.Label) bool {
	m := &c.m
	if v, ok := c.constant(r); ok {
		switch {
		case v.IsNumber():
			m.MovImm(x, uint64(uint32(toInt32(v.num()))))
		case v.u == tagTrue || v.u == tagFalse:
			m.MovImm(x, v.u-tagFalse)
		default:
			return false
		}
		return true
	}
	if f, ok := c.optReg(r); ok {
		m.TruncInt64(x, f, exit)
		return true
	}
	if c.factsOn {
		// A number known (type facts) or kept in the number cache: no
		// test for booleans.
		for k, e := range c.fcache {
			if e.valid && e.r == r {
				m.TruncInt64(x, jitFCache[k], exit)
				c.fuse = uint8(k)
				return true
			}
		}
		if c.facts[r]&factNumber != 0 {
			m.Load64(rTmp, rRegs, c.off(r)+8)
			m.Xor64(rTmp, rTmp, rMask)
			m.MovToF(6, rTmp)
			m.TruncInt64(x, 6, exit)
			return true
		}
	}
	other, done := jit.NewLabel(), jit.NewLabel()
	m.Load64(rTmp, rRegs, c.off(r)+8)
	m.Branch64Imm(jit.ULE, rTmp, maxTag, other)
	m.Xor64(rTmp, rTmp, rMask)
	m.MovToF(6, rTmp)
	m.TruncInt64(x, 6, exit)
	m.Bind(done)
	// undefined, null, false and true are the tags 0-3: true is 1, the
	// others 0 (md5 reads past the end of its array: x[i] & 0xffff).
	// Cold: inline, it made stanford-crypto-aes 3.6% slower.
	c.cold = append(c.cold, func() {
		m.Bind(other)
		m.Branch64Imm(jit.UGT, rTmp, uint32(tagTrue), exit)
		m.MovImm(x, 0)
		m.Branch64Imm(jit.NE, rTmp, uint32(tagTrue), done)
		m.MovImm(x, 1)
		m.B(done)
	})
	return true
}

// exactInt32 loads number r into x if it is an int32 exactly (-0 counts
// as 0), and exits otherwise; rTmp keeps the number's bits.
func (c *jitCompiler) exactInt32(x jit.R, r bytecode.VirtualRegister, exit *jit.Label) bool {
	if !c.loadNum(6, r, exit) {
		return false
	}
	if c.fromF {
		c.m.MovFromF(rTmp, 6) // the number's bits, as loadNum leaves them
	}
	c.m.TruncExactInt32(x, 6, exit)
	return true
}

// compareJump decodes the conditional jumps on number comparisons: the
// target, the operands and the comparison that takes the jump.
func compareJump(in []uint32) (bytecode.BoundLabel, [2]bytecode.VirtualRegister, jit.FCond, bool) {
	type ops = [2]bytecode.VirtualRegister
	switch bytecode.OpcodeID(in[0]) {
	case bytecode.OpJlessID:
		b := bytecode.DecodeJless(in)
		return b.TargetLabel, ops{b.Lhs, b.Rhs}, jit.FLT, true
	case bytecode.OpJlesseqID:
		b := bytecode.DecodeJlesseq(in)
		return b.TargetLabel, ops{b.Lhs, b.Rhs}, jit.FLE, true
	case bytecode.OpJgreaterID:
		b := bytecode.DecodeJgreater(in)
		return b.TargetLabel, ops{b.Lhs, b.Rhs}, jit.FGT, true
	case bytecode.OpJgreatereqID:
		b := bytecode.DecodeJgreatereq(in)
		return b.TargetLabel, ops{b.Lhs, b.Rhs}, jit.FGE, true
	case bytecode.OpJnlessID:
		b := bytecode.DecodeJnless(in)
		return b.TargetLabel, ops{b.Lhs, b.Rhs}, jit.FNLT, true
	case bytecode.OpJnlesseqID:
		b := bytecode.DecodeJnlesseq(in)
		return b.TargetLabel, ops{b.Lhs, b.Rhs}, jit.FNLE, true
	case bytecode.OpJngreaterID:
		b := bytecode.DecodeJngreater(in)
		return b.TargetLabel, ops{b.Lhs, b.Rhs}, jit.FNGT, true
	case bytecode.OpJngreatereqID:
		b := bytecode.DecodeJngreatereq(in)
		return b.TargetLabel, ops{b.Lhs, b.Rhs}, jit.FNGE, true
	// Equality (only the jump targets: jit_slow.go compiles these).
	case bytecode.OpJeqID:
		b := bytecode.DecodeJeq(in)
		return b.TargetLabel, ops{b.Lhs, b.Rhs}, jit.FEQ, true
	case bytecode.OpJstricteqID:
		b := bytecode.DecodeJstricteq(in)
		return b.TargetLabel, ops{b.Lhs, b.Rhs}, jit.FEQ, true
	case bytecode.OpJneqID:
		b := bytecode.DecodeJneq(in)
		return b.TargetLabel, ops{b.Lhs, b.Rhs}, jit.FNE, true
	case bytecode.OpJnstricteqID:
		b := bytecode.DecodeJnstricteq(in)
		return b.TargetLabel, ops{b.Lhs, b.Rhs}, jit.FNE, true
	}
	return 0, [2]bytecode.VirtualRegister{}, 0, false
}

// instruction compiles the instruction at pc; it reports false if it is not
// supported (the caller then emits an exit).
func (c *jitCompiler) instruction(pc int, in []uint32) bool {
	m := &c.m
	op := bytecode.OpcodeID(in[0])
	next := pc + op.Length()
	if c.inline != nil {
		if handled, ok := c.inlineInstruction(pc, op, in); handled {
			return ok
		}
	}
	exit := c.exit(pc)
	if jitSlowOps[op] {
		// Values the machine code does not handle take the slow path, as
		// JSC's baseline calls an operation (no guard exit, which would
		// disable the code's entries at sites that see them often).
		if t, ok := jumpTarget(pc, in); ok {
			exit = c.slow(pc, next, t)
		} else {
			exit = c.slow(pc, next)
		}
	}
	binaryFloat := func(dst, lhs, rhs bytecode.VirtualRegister, fop jit.FOp) bool {
		if !c.loadNum(0, lhs, exit) || !c.loadNum(1, rhs, exit) {
			return false
		}
		m.FArith(fop, 2, 0, 1)
		c.storeNum(dst, 2)
		return true
	}
	binaryInt := func(dst, lhs, rhs bytecode.VirtualRegister, iop jit.Op32, unsigned bool) bool {
		if !c.int32Of(jit.S0, lhs, exit) || !c.int32Of(jit.S1, rhs, exit) {
			return false
		}
		m.Alu32(iop, jit.S2, jit.S0, jit.S1)
		if unsigned {
			m.CvtU32(2, jit.S2)
		} else {
			m.CvtS32(2, jit.S2)
		}
		c.storeNum(dst, 2)
		return true
	}
	compare := func(dst, lhs, rhs bytecode.VirtualRegister, cond jit.FCond) bool {
		if !c.loadCompared(pc, 0, lhs, exit) || !c.loadCompared(pc, 1, rhs, exit) {
			return false
		}
		m.FSetBool(cond, jit.S0, 0, 1)
		c.storeBool(dst, jit.S0)
		return true
	}
	if c.allocInstruction(pc, op, in) || c.objectInstruction(pc, op, in) {
		return true
	}
	switch op {
	case bytecode.OpLoopHintID:
		m.Load64(rTmp, rFrame, offFrameFuel)
		m.SubImm64(rTmp, rTmp, 1)
		m.Store64(rTmp, rFrame, offFrameFuel)
		l := c.fuel[next]
		if l == nil {
			l = jit.NewLabel()
			c.fuel[next] = l
		}
		m.BranchZero(rTmp, l)
		if o := c.optLoops[pc]; o != nil && c.inline == nil {
			if o.counting {
				c.optCountHot(o)
			} else {
				// The optimized version (jit_opt.go), which comes back to
				// cont if it is not entered.
				m.B(o.entry)
				m.Bind(o.cont)
			}
		}
		return true
	case bytecode.OpTailCallID:
		// Like a call, whose result the ret after it returns; Go makes the
		// proper tail call when the stack is deep (jitSlowPath).
		b := bytecode.DecodeTailCall(in)
		if !jit.NativeCalls {
			m.B(c.slow(pc, next))
			return true
		}
		// As deep as the slow path makes it an ordinary call, and no
		// deeper: beyond, calls in tail position must not grow the stack
		// (a tail-recursive loop would end at the stack limit).
		m.Load64(jit.S0, rFrame, offFrameVM)
		m.Load64(jit.S0, jit.S0, offVMDepth)
		m.MovImm(jit.S1, uint64(c.cb.realm.vm.MaxDepth/4))
		m.Branch64(jit.SGE, jit.S0, jit.S1, c.slow(pc, next))
		c.nativeCall(pc, b.Callee, b.Argc, b.Argv, b.Dst, true)
		return true
	case bytecode.OpCallID, bytecode.OpCallIgnoreResultID:
		if !jit.NativeCalls {
			// Go makes the call (VM.tryJIT) and comes back after it.
			m.B(c.slow(pc, next))
			return true
		}
		var call, done *jit.Label
		var callee, dst bytecode.VirtualRegister
		var argc, argv uint32
		if op == bytecode.OpCallID {
			b := bytecode.DecodeCall(in)
			callee, dst, argc, argv = b.Callee, b.Dst, b.Argc, b.Argv
			call, done = c.tryInline(pc, c.cb.callProfiles[b.MetadataID], callee, argc, argv, dst, true)
		} else {
			b := bytecode.DecodeCallIgnoreResult(in)
			callee, argc, argv = b.Callee, b.Argc, b.Argv
			call, done = c.tryInline(pc, c.cb.callIgnoreProfiles[b.MetadataID], callee, argc, argv, 0, false)
		}
		if c.inline != nil {
			// In inlined code, a call is inlined too, or not compiled
			// (jit_inline.go); another callee there takes the slow path,
			// which makes the frames and has Go make the call.
			if call == nil {
				var p callProfile
				if op == bytecode.OpCallID {
					p = c.cb.callProfiles[bytecode.DecodeCall(in).MetadataID]
				} else {
					p = c.cb.callIgnoreProfiles[bytecode.DecodeCallIgnoreResult(in).MetadataID]
				}
				return c.inlineThunkCall(pc, p, callee, argc, argv, dst, op == bytecode.OpCallID)
			}
			m.Bind(call)
			m.B(c.slow(pc, next))
			m.Bind(done)
			return true
		}
		if call != nil {
			m.Bind(call)
		}
		c.nativeCall(pc, callee, argc, argv, dst, op == bytecode.OpCallID)
		if done != nil {
			m.Bind(done)
		}
		return true
	case bytecode.OpEnterID:
		// The locals are already undefined (see the interpreter).
		if r := c.cb.u.ScopeRegister; r.IsValid() {
			m.Load64(jit.S0, rFrame, offFrameScope)
			m.MovImm(jit.S1, uint64(tagScope))
			c.storeValue(r, jit.S0, jit.S1)
		}
		return true
	case bytecode.OpNopID, bytecode.OpCheckTrapsID:
		// Traps are checked by the interpreter, which generated code returns
		// to at least every jitFuel iterations.
		return true
	case bytecode.OpMovID:
		b := bytecode.DecodeMov(in)
		c.loadValue(jit.S0, jit.S1, b.Src)
		c.storeValue(b.Dst, jit.S0, jit.S1)
		return true
	case bytecode.OpToNumberID, bytecode.OpToNumericID, bytecode.OpToPropertyKeyOrNumberID:
		// Numbers stay numbers (other values exit).
		var dst, src bytecode.VirtualRegister
		switch op {
		case bytecode.OpToNumberID:
			b := bytecode.DecodeToNumber(in)
			dst, src = b.Dst, b.Operand
		case bytecode.OpToNumericID:
			b := bytecode.DecodeToNumeric(in)
			dst, src = b.Dst, b.Operand
		default:
			b := bytecode.DecodeToPropertyKeyOrNumber(in)
			dst, src = b.Dst, b.Src
		}
		if !c.loadNum(0, src, exit) {
			return false
		}
		c.storeNum(dst, 0)
		return true
	case bytecode.OpJundefinedOrNullID, bytecode.OpJnundefinedOrNullID:
		var r bytecode.VirtualRegister
		var label bytecode.BoundLabel
		if op == bytecode.OpJundefinedOrNullID {
			b := bytecode.DecodeJundefinedOrNull(in)
			r, label = b.Value, b.TargetLabel
		} else {
			b := bytecode.DecodeJnundefinedOrNull(in)
			r, label = b.Value, b.TargetLabel
		}
		c.loadValue(jit.S0, jit.S1, r)
		// undefined (0) and null (1) are the tags below 2 (with no pointer).
		other := jit.NewLabel()
		if op == bytecode.OpJundefinedOrNullID {
			m.BranchNonZero(jit.S0, other)
			m.Branch64Imm(jit.ULT, jit.S1, uint32(tagFalse), c.target(pc, label))
		} else {
			t := c.target(pc, label)
			m.BranchNonZero(jit.S0, t)
			m.Branch64Imm(jit.UGE, jit.S1, uint32(tagFalse), t)
		}
		m.Bind(other)
		return true
	case bytecode.OpAddID:
		// Numbers; appending ASCII to a string's append buffer (cold);
		// other values take the slow path.
		b := bytecode.DecodeAdd(in)
		slow := c.slow(pc, next)
		notNum := slow
		var strDone *jit.Label
		// A constant operand is not in a register: lhs must not be one, and
		// a constant rhs must be a string appendString can append.
		_, lhsConst := c.constant(b.Lhs)
		rhsStr, rhsConst := c.constant(b.Rhs)
		appendable := !lhsConst && (!rhsConst || appendableConstant(rhsStr))
		// (In an optimized loop only if none of them is kept in a register:
		// appendString reads and writes memory.)
		optOK := !c.optKept(b.Dst) && !c.optKept(b.Lhs) && !c.optKept(b.Rhs)
		if appendable && optOK && (!c.factsOn || c.cb.jit.noSpecAt[next]) {
			// Only where dst gets no number fact: the cold path stores a
			// string and continues after the instruction.
			notNum, strDone = jit.NewLabel(), jit.NewLabel()
			dst, lhs, rhs := b.Dst, b.Lhs, b.Rhs
			c.cold = append(c.cold, func() {
				m.Bind(notNum)
				c.appendString(dst, lhs, rhs, strDone, slow)
			})
		}
		if !c.loadNum(0, b.Lhs, notNum) || !c.loadNum(1, b.Rhs, notNum) {
			// A constant operand that is not a number (s + "x"): never a
			// number add. The append path must still come back here.
			m.B(notNum)
			if strDone != nil {
				m.Bind(strDone)
			}
			return true
		}
		m.FArith(jit.FAdd, 2, 0, 1)
		c.storeNum(b.Dst, 2)
		if strDone != nil {
			m.Bind(strDone)
		}
		return true
	case bytecode.OpSubID:
		b := bytecode.DecodeSub(in)
		return binaryFloat(b.Dst, b.Lhs, b.Rhs, jit.FSub)
	case bytecode.OpMulID:
		b := bytecode.DecodeMul(in)
		return binaryFloat(b.Dst, b.Lhs, b.Rhs, jit.FMul)
	case bytecode.OpDivID:
		b := bytecode.DecodeDiv(in)
		return binaryFloat(b.Dst, b.Lhs, b.Rhs, jit.FDiv)
	case bytecode.OpModID:
		b := bytecode.DecodeMod(in)
		// Integers only: lhs >= 0 or a nonzero result (no -0), rhs > 0.
		if !c.exactInt32(jit.S0, b.Lhs, exit) {
			return false
		}
		// -0 % x is -0: exactInt32 made it 0 (rTmp still has its bits).
		nonZero := jit.NewLabel()
		m.BranchNonZero(jit.S0, nonZero)
		m.BranchNonZero(rTmp, exit)
		m.Bind(nonZero)
		if !c.exactInt32(jit.S1, b.Rhs, exit) {
			return false
		}
		m.Branch32Imm(jit.SLE, jit.S1, 0, exit)
		m.Rem32(jit.S2, jit.S0, jit.S1)
		nonNeg := jit.NewLabel()
		m.Branch32Imm(jit.SGE, jit.S0, 0, nonNeg)
		m.BranchZero(jit.S2, exit) // -0
		m.Bind(nonNeg)
		m.CvtS32(2, jit.S2)
		c.storeNum(b.Dst, 2)
		return true
	case bytecode.OpNegateID:
		b := bytecode.DecodeNegate(in)
		if !c.loadNum(0, b.Operand, exit) {
			return false
		}
		m.FNeg(0, 0)
		c.storeNum(b.Dst, 0)
		return true
	case bytecode.OpIncID, bytecode.OpDecID:
		var r bytecode.VirtualRegister
		fop := jit.FAdd
		if op == bytecode.OpIncID {
			r = bytecode.DecodeInc(in).SrcDst
		} else {
			r, fop = bytecode.DecodeDec(in).SrcDst, jit.FSub
		}
		if !c.loadNum(0, r, exit) {
			return false
		}
		m.MovImm(rTmp, math.Float64bits(1))
		m.MovToF(1, rTmp)
		m.FArith(fop, 0, 0, 1)
		c.storeNum(r, 0)
		return true
	case bytecode.OpBitandID:
		b := bytecode.DecodeBitand(in)
		return binaryInt(b.Dst, b.Lhs, b.Rhs, jit.And32, false)
	case bytecode.OpBitorID:
		b := bytecode.DecodeBitor(in)
		return binaryInt(b.Dst, b.Lhs, b.Rhs, jit.Or32, false)
	case bytecode.OpBitxorID:
		b := bytecode.DecodeBitxor(in)
		return binaryInt(b.Dst, b.Lhs, b.Rhs, jit.Xor32, false)
	case bytecode.OpLshiftID:
		b := bytecode.DecodeLshift(in)
		return binaryInt(b.Dst, b.Lhs, b.Rhs, jit.Shl32, false)
	case bytecode.OpRshiftID:
		b := bytecode.DecodeRshift(in)
		return binaryInt(b.Dst, b.Lhs, b.Rhs, jit.Sar32, false)
	case bytecode.OpUrshiftID:
		b := bytecode.DecodeUrshift(in)
		return binaryInt(b.Dst, b.Lhs, b.Rhs, jit.Shr32, true)
	case bytecode.OpBitnotID:
		b := bytecode.DecodeBitnot(in)
		if !c.int32Of(jit.S0, b.Operand, exit) {
			return false
		}
		m.Not32(jit.S2, jit.S0)
		m.CvtS32(2, jit.S2)
		c.storeNum(b.Dst, 2)
		return true
	case bytecode.OpUnsignedID:
		b := bytecode.DecodeUnsigned(in)
		if !c.int32Of(jit.S0, b.Operand, exit) {
			return false
		}
		m.CvtU32(2, jit.S0)
		c.storeNum(b.Dst, 2)
		return true
	case bytecode.OpBelowID, bytecode.OpBeloweqID:
		// Unsigned comparison of the int32s (asm.js's (x>>>0) < (y>>>0)).
		var dst, lhs, rhs bytecode.VirtualRegister
		cond := jit.ULT
		if op == bytecode.OpBelowID {
			b := bytecode.DecodeBelow(in)
			dst, lhs, rhs = b.Dst, b.Lhs, b.Rhs
		} else {
			b := bytecode.DecodeBeloweq(in)
			dst, lhs, rhs, cond = b.Dst, b.Lhs, b.Rhs, jit.ULE
		}
		if !c.int32Of(jit.S0, lhs, exit) || !c.int32Of(jit.S1, rhs, exit) {
			return false
		}
		m.SetCond32(cond, jit.S2, jit.S0, jit.S1)
		c.storeBool(dst, jit.S2)
		return true
	case bytecode.OpJbelowID, bytecode.OpJbeloweqID:
		var lhs, rhs bytecode.VirtualRegister
		var label bytecode.BoundLabel
		cond := jit.ULT
		if op == bytecode.OpJbelowID {
			b := bytecode.DecodeJbelow(in)
			lhs, rhs, label = b.Lhs, b.Rhs, b.TargetLabel
		} else {
			b := bytecode.DecodeJbeloweq(in)
			lhs, rhs, label, cond = b.Lhs, b.Rhs, b.TargetLabel, jit.ULE
		}
		if !c.int32Of(jit.S0, lhs, exit) || !c.int32Of(jit.S1, rhs, exit) {
			return false
		}
		m.Branch32(cond, jit.S0, jit.S1, c.target(pc, label))
		return true
	case bytecode.OpLessID:
		b := bytecode.DecodeLess(in)
		return compare(b.Dst, b.Lhs, b.Rhs, jit.FLT)
	case bytecode.OpLesseqID:
		b := bytecode.DecodeLesseq(in)
		return compare(b.Dst, b.Lhs, b.Rhs, jit.FLE)
	case bytecode.OpGreaterID:
		b := bytecode.DecodeGreater(in)
		return compare(b.Dst, b.Lhs, b.Rhs, jit.FGT)
	case bytecode.OpGreatereqID:
		b := bytecode.DecodeGreatereq(in)
		return compare(b.Dst, b.Lhs, b.Rhs, jit.FGE)
	case bytecode.OpJmpID:
		m.B(c.target(pc, bytecode.DecodeJmp(in).TargetLabel))
		return true
	case bytecode.OpJtrueID, bytecode.OpJfalseID:
		var cond bytecode.VirtualRegister
		var label bytecode.BoundLabel
		if op == bytecode.OpJtrueID {
			b := bytecode.DecodeJtrue(in)
			cond, label = b.Condition, b.TargetLabel
		} else {
			b := bytecode.DecodeJfalse(in)
			cond, label = b.Condition, b.TargetLabel
		}
		truthy, falsy := jit.NewLabel(), jit.NewLabel()
		if op == bytecode.OpJtrueID {
			truthy = c.target(pc, label)
		} else {
			falsy = c.target(pc, label)
		}
		if v, ok := c.constant(cond); ok && v.p != nil {
			return false
		}
		c.loadValue(jit.S1, jit.S0, cond)
		c.toBoolean(truthy, falsy, exit)
		if op == bytecode.OpJtrueID {
			m.Bind(falsy)
		} else {
			m.Bind(truthy)
		}
		return true
	case bytecode.OpGetLengthID:
		b := bytecode.DecodeGetLength(in)
		if _, ok := c.constant(b.Base); ok {
			return false
		}
		str, done, other, after := jit.NewLabel(), jit.NewLabel(), jit.NewLabel(), jit.NewLabel()
		m.Load64(jit.S0, rRegs, c.off(b.Base)+8)
		m.Load64(jit.S1, rRegs, c.off(b.Base))
		slow := c.slow(pc, next)
		m.Branch64Imm(jit.EQ, jit.S0, uint32(tagString), str)
		m.Branch64Imm(jit.NE, jit.S0, uint32(tagObject), slow)
		m.Load64(jit.S2, jit.S1, offExotic)
		m.MovImm(jit.S3, uint64(arrayObjectItab))
		notArray := jit.NewLabel()
		m.Branch64(jit.NE, jit.S2, jit.S3, notArray)
		m.Load64(jit.S2, jit.S1, offExotic+8)
		m.Load32(jit.S2, jit.S2, offArrayLength)
		m.CvtU32(0, jit.S2)
		m.B(done)
		// A String object (new String(x).length in a loop, date-format-
		// xparb's leftPad): the length of the string in its exotic's data
		// word, a non-writable, non-configurable own property.
		m.Bind(notArray)
		m.MovImm(jit.S3, uint64(stringObjectItab))
		m.Branch64(jit.NE, jit.S2, jit.S3, other)
		m.Load64(jit.S1, jit.S1, offExotic+8) // *String
		m.Bind(str)
		m.Load64(jit.S2, jit.S1, offStrLength)
		m.CvtS64(0, jit.S2)
		m.Bind(done)
		c.storeNum(b.Dst, 0)
		m.Bind(after)
		// Other objects (cold): typed arrays whose length is the built-in
		// getter's, and length properties through the cache.
		cache := &c.cb.getLengthCaches[b.MetadataID]
		dst := b.Dst
		c.cold = append(c.cold, func() {
			m.Bind(other)
			m.Mov(jit.S0, jit.S1)
			c.lengthOfObject(cache, dst, done, after, slow)
		})
		return true
	// Property accesses the code does not handle (cache misses, other kinds
	// of objects) take the slow path rather than a guard exit: at sites that
	// see many shapes, guard exits disabled the code's entries, and the
	// whole function ran in the interpreter.
	case bytecode.OpGetByValID:
		b := bytecode.DecodeGetByVal(in)
		return c.byVal(b.Base, b.Property, b.Dst, false, c.cb.getByValProfiles[b.MetadataID], c.slow(pc, next), c.reprofileExit(pc, c.cb.getByValProfiles[b.MetadataID]))
	case bytecode.OpPutByValID:
		b := bytecode.DecodePutByVal(in)
		return c.byVal(b.Base, b.Property, b.Value, true, c.cb.putByValProfiles[b.MetadataID], c.slow(pc, next), c.reprofileExit(pc, c.cb.putByValProfiles[b.MetadataID]))
	case bytecode.OpGetByIdID, bytecode.OpPutByIdID:
		return c.scopeInstruction(pc, op, in, c.slow(pc, next))
	}
	if c.scopeInstruction(pc, op, in, exit) {
		return true
	}
	if label, ops, cond, ok := compareJump(in); ok {
		if !c.loadCompared(pc, 0, ops[0], exit) || !c.loadCompared(pc, 1, ops[1], exit) {
			return false
		}
		m.FBranch(cond, 0, 1, c.target(pc, label))
		return true
	}
	return false
}

// byVal compiles get_by_val (val is the destination) or put_by_val (val is
// the value) of an element of an array, a plain object's elements or a
// typed array.
func (c *jitCompiler) byVal(base, prop, val bytecode.VirtualRegister, put bool, profile uint16, exit, reprofile *jit.Label) bool {
	m := &c.m
	if _, ok := c.constant(base); ok {
		return false
	}
	// S0 = object, S1 = index (uint32, zero-extended).
	notObject := exit
	if !put {
		notObject = jit.NewLabel()
	}
	done := jit.NewLabel()
	if kind, ok := onlyTypedKind(profile, put); ok {
		if !c.typedByVal(base, prop, val, put, kind, done, exit) {
			return false
		}
	}
	m.Load64(jit.S0, rRegs, c.off(base)+8)
	m.Branch64Imm(jit.NE, jit.S0, uint32(tagObject), notObject)
	m.Load64(jit.S0, rRegs, c.off(base))
	if _, ok := c.constant(prop); !ok && !c.optKept(prop) {
		// A string key: the megamorphic tables (cold).
		strKey := jit.NewLabel()
		m.Load64(jit.S2, rRegs, c.off(prop)+8)
		m.Branch64Imm(jit.EQ, jit.S2, uint32(tagString), strKey)
		c.cold = append(c.cold, func() {
			m.Bind(strKey)
			c.megaByVal(prop, val, put, done, exit)
		})
	}
	if !c.exactInt32(jit.S1, prop, exit) {
		return false
	}
	m.ZeroExt32(jit.S1, jit.S1)
	if put {
		// S8, S9 = the value.
		c.loadValue(jit.S8, jit.S9, val)
	}
	elements, typed := jit.NewLabel(), jit.NewLabel()
	hole := jit.NewLabel()
	if !put {
		// A string's code unit (cold; JSC's IndexedStringLoad).
		c.cold = append(c.cold, func() {
			m.Bind(notObject)
			m.Branch64Imm(jit.NE, jit.S0, uint32(tagString), exit)
			c.exactInt32(jit.S4, prop, exit)
			m.Load64(jit.S8, rRegs, c.off(base))
			m.ZeroExt32(jit.S4, jit.S4)
			m.Load64(jit.S3, jit.S8, offStrLeft)
			m.BranchNonZero(jit.S3, exit) // a rope
			m.Load64(jit.S9, jit.S8, offStrLength)
			m.Branch64(jit.UGE, jit.S4, jit.S9, exit)
			loadChar(m)
			m.Branch64Imm(jit.UGE, jit.S5, 256, exit)
			m.MovImm(jit.S3, smallStringsBase)
			m.AddShift64(jit.S3, jit.S3, jit.S5, 3)
			m.Load64(jit.S4, jit.S3, 0)
			m.MovImm(jit.S5, uint64(tagString))
			c.storeValue(val, jit.S4, jit.S5)
			m.B(done)
		})
		// Holes and indexes past the end of an array with a sane chain
		// read undefined (cold); indexes from 2^31 up may be other keys.
		c.cold = append(c.cold, func() {
			m.Bind(hole)
			m.MovImm(jit.S5, math.MaxInt32)
			m.Branch64(jit.UGT, jit.S1, jit.S5, exit)
			arrayChainCheck(m, jit.S0, jit.S2, [4]jit.R{jit.S4, jit.S5, jit.S6, jit.S7}, exit)
			m.MovImm(jit.S4, 0)
			m.MovImm(jit.S5, uint64(tagUndefined))
			c.storeValue(val, jit.S4, jit.S5)
			m.B(done)
		})
	} else {
		// Storing to a hole of a plain array that is extensible (cold;
		// S3 = the element).
		c.cold = append(c.cold, func() {
			m.Bind(hole)
			plainArrayCheck(m, jit.S0, jit.S2, [4]jit.R{jit.S4, jit.S5, jit.S6, jit.S7}, exit)
			m.Load8(jit.S5, jit.S0, offObjectExtensible)
			m.BranchZero(jit.S5, exit)
			m.Store64(jit.S8, jit.S3, 0)
			m.Store64(jit.S9, jit.S3, 8)
			m.B(done)
		})
	}
	m.Load64(jit.S2, jit.S0, offExotic)
	m.BranchZero(jit.S2, elements)
	m.MovImm(jit.S3, uint64(arrayObjectItab))
	m.Branch64(jit.EQ, jit.S2, jit.S3, elements)
	if !put {
		// arguments[i] (cold; JSC's DirectArguments): S2 = the itab.
		args := jit.NewLabel()
		m.MovImm(jit.S3, uint64(argumentsObjectItab))
		m.Branch64(jit.EQ, jit.S2, jit.S3, args)
		c.cold = append(c.cold, func() {
			m.Bind(args)
			c.argumentsByVal(val, done, exit)
		})
	}
	m.Load8(jit.S2, jit.S0, offClass)
	if reprofile != nil {
		m.Branch64Imm(jit.EQ, jit.S2, uint32(ClassTypedArray), reprofile)
	} else {
		m.Branch64Imm(jit.EQ, jit.S2, uint32(ClassTypedArray), typed)
	}
	m.B(exit)

	// Elements: an existing element (not a hole: those read through the
	// prototype chain).
	m.Bind(elements)
	m.Load64(jit.S3, jit.S0, offElements+8) // len
	if put {
		// Past the end of the elements: appending to an array (setIndexFast)
		// that is extensible and whose elements have room for one more.
		// The index may lie within the length (new Array(n) has n holes
		// and no elements yet; hash-map fills such arrays from 0 up, 0.45
		// million stores a run that took the slow path), which then stays;
		// else it becomes index+1, if writable.
		appendL := jit.NewLabel()
		m.Branch64(jit.UGE, jit.S1, jit.S3, appendL)
		c.cold = append(c.cold, func() {
			m.Bind(appendL)
			m.Branch64(jit.NE, jit.S1, jit.S3, exit)
			arrayChainCheck(m, jit.S0, jit.S2, [4]jit.R{jit.S4, jit.S5, jit.S6, jit.S7}, exit)
			m.Load8(jit.S5, jit.S0, offObjectExtensible)
			m.BranchZero(jit.S5, exit)
			m.Load64(jit.S5, jit.S0, offElements+16) // cap
			m.Branch64(jit.UGE, jit.S1, jit.S5, exit)
			m.MovImm(jit.S5, 0xFFFFFFFE)
			m.Branch64(jit.UGE, jit.S1, jit.S5, exit)
			within := jit.NewLabel()
			m.Load32(jit.S6, jit.S2, offArrayLength)
			m.Branch64(jit.ULT, jit.S1, jit.S6, within)
			m.Load8(jit.S7, jit.S2, offArrayLengthWritable)
			m.BranchZero(jit.S7, exit)
			m.AddImm64(jit.S7, jit.S1, 1)
			m.Store32(jit.S7, jit.S2, offArrayLength)
			m.Bind(within)
			m.Load64(jit.S5, jit.S0, offElements)
			m.AddShift64(jit.S5, jit.S5, jit.S1, 4)
			m.Store64(jit.S8, jit.S5, 0)
			m.Store64(jit.S9, jit.S5, 8)
			m.AddImm64(jit.S4, jit.S1, 1)
			m.Store64(jit.S4, jit.S0, offElements+8)
			m.B(done)
		})
	} else {
		m.Branch64(jit.UGE, jit.S1, jit.S3, hole)
	}
	m.Load64(jit.S3, jit.S0, offElements) // ptr
	m.AddShift64(jit.S3, jit.S3, jit.S1, 4)
	m.Load64(jit.S5, jit.S3, 0)
	m.Load64(jit.S4, jit.S3, 8)
	m.Branch64Imm(jit.EQ, jit.S4, uint32(tagEmpty), hole)
	if put {
		m.Store64(jit.S8, jit.S3, 0)
		m.Store64(jit.S9, jit.S3, 8)
	} else {
		c.storeValue(val, jit.S5, jit.S4)
	}
	m.B(done)

	// Typed arrays (cold: their code is long, and most sites only see
	// plain arrays, whose path stays compact).
	c.cold = append(c.cold, func() {
		// Typed arrays of fixed length with their buffer entirely in bounds.
		m.Bind(typed)
		m.Load64(jit.S2, jit.S0, offInternal)
		m.MovImm(jit.S3, uint64(typedArrayType))
		m.Branch64(jit.NE, jit.S2, jit.S3, exit)
		m.Load64(jit.S2, jit.S0, offInternal+8) // *typedArray
		if put {
			// F2 = the value, which must be a number.
			m.Branch64Imm(jit.ULE, jit.S9, maxTag, exit)
			m.Xor64(jit.S9, jit.S9, rMask)
			m.MovToF(2, jit.S9)
		}
		// The view's own vector, while no buffer was detached since it was
		// made (typedArray.vector): no buffer or bounds to look at.
		viewMiss := jit.NewLabel()
		m.Load64(jit.S4, jit.S2, offTAVLen)
		m.Branch64(jit.UGE, jit.S1, jit.S4, viewMiss)
		m.Load64(jit.S5, jit.S2, offTAVEpoch)
		m.MovImm(jit.S6, uint64(uintptr(unsafe.Pointer(&c.cb.realm.vm.detachEpoch))))
		m.Load64(jit.S6, jit.S6, 0)
		m.Branch64(jit.NE, jit.S5, jit.S6, viewMiss)
		m.Load64(jit.S7, jit.S2, offTAVector)
		m.Load8(jit.S3, jit.S2, offTAKind)
		for _, k := range typedKinds {
			next := jit.NewLabel()
			m.Branch64Imm(jit.NE, jit.S3, uint32(k.kind), next)
			if put {
				c.storeElement(k.kind, exit)
			} else {
				c.loadElement(k.kind)
				c.storeNum(val, 0)
			}
			m.B(done)
			m.Bind(next)
		}
		m.B(exit)
		m.Bind(viewMiss)
		m.Load64(jit.S4, jit.S2, offTALength)
		m.Branch64Imm(jit.SLT, jit.S4, 0, exit) // length-tracking
		m.Branch64(jit.UGE, jit.S1, jit.S4, exit)
		m.Load64(jit.S5, jit.S2, offTABuffer)
		m.Load64(jit.S6, jit.S5, offInternal)
		m.MovImm(jit.S3, uint64(arrayBufferDataType))
		m.Branch64(jit.NE, jit.S6, jit.S3, exit)
		m.Load64(jit.S6, jit.S5, offInternal+8) // *arrayBufferData
		m.Load8(jit.S7, jit.S6, offBufDetached)
		m.BranchNonZero(jit.S7, exit)
		m.Load64(jit.S7, jit.S6, offBufData)   // data ptr
		m.Load64(jit.S8, jit.S6, offBufData+8) // data len
		m.Load64(jit.S9, jit.S2, offTAByteOff)
		m.Add64(jit.S7, jit.S7, jit.S9) // element 0
		m.Load8(jit.S3, jit.S2, offTAKind)
		for _, k := range typedKinds {
			next := jit.NewLabel()
			m.Branch64Imm(jit.NE, jit.S3, uint32(k.kind), next)
			// byteOffset + length<<shift must fit in the buffer.
			m.AddShift64(jit.S9, jit.S9, jit.S4, k.shift)
			m.Branch64(jit.UGT, jit.S9, jit.S8, exit)
			if put {
				c.storeElement(k.kind, exit)
			} else {
				c.loadElement(k.kind)
				c.storeNum(val, 0)
			}
			m.B(done)
			m.Bind(next)
		}
		m.B(exit)
	})
	m.Bind(done)
	return true
}

var (
	offStrKey  = int(unsafe.Offsetof(String{}.key)) // atomic.Pointer's pointer comes first
	offKeyName = int(unsafe.Offsetof(PropertyKey{}.name))
	offKeySym  = int(unsafe.Offsetof(PropertyKey{}.sym))
	offKeyIdx  = int(unsafe.Offsetof(PropertyKey{}.idx))
)

// argumentsByVal reads element S1 (uint32) of arguments object S0 into val
// when the object maps its parameters to its own storage (not to a scope)
// and no index was unmapped: a mapped index reads that storage, another
// one the element; holes and indexes past the elements exit.
func (c *jitCompiler) argumentsByVal(val bytecode.VirtualRegister, done, exit *jit.Label) {
	m := &c.m
	m.Load64(jit.S2, jit.S0, offInternal)
	m.MovImm(jit.S3, uint64(argumentsDataType))
	m.Branch64(jit.NE, jit.S2, jit.S3, exit)
	m.Load64(jit.S2, jit.S0, offInternal+8) // *argumentsData
	m.Load64(jit.S3, jit.S2, offArgsMapped)
	m.BranchNonZero(jit.S3, exit)
	m.Load64(jit.S3, jit.S2, offArgsScope)
	m.BranchNonZero(jit.S3, exit)
	// The element must exist (a deleted one reads the prototype chain).
	m.Load64(jit.S3, jit.S0, offElements+8)
	m.Branch64(jit.UGE, jit.S1, jit.S3, exit)
	m.Load64(jit.S3, jit.S0, offElements)
	m.AddShift64(jit.S3, jit.S3, jit.S1, 4)
	m.Load64(jit.S4, jit.S3, 8)
	m.Branch64Imm(jit.EQ, jit.S4, uint32(tagEmpty), exit)
	unmapped := jit.NewLabel()
	m.Load64(jit.S5, jit.S2, offArgsNumMapped)
	m.Branch64(jit.UGE, jit.S1, jit.S5, unmapped)
	m.Load64(jit.S3, jit.S2, offArgsDirect) // numMapped <= len(direct)
	m.AddShift64(jit.S3, jit.S3, jit.S1, 4)
	m.Bind(unmapped)
	m.Load64(jit.S5, jit.S3, 0)
	m.Load64(jit.S4, jit.S3, 8)
	c.storeValue(val, jit.S5, jit.S4)
	m.B(done)
}

// megaByVal is get_by_val (val is the destination) or put_by_val (val is
// the value) of plain object S0 with the string in register prop as key,
// through the VM's megamorphic tables and their thunks (megaByValKey);
// strings without a cached key, index keys and other objects take exit.
func (c *jitCompiler) megaByVal(prop, val bytecode.VirtualRegister, put bool, done, exit *jit.Label) {
	m := &c.m
	vm := c.cb.realm.vm
	if vm.megaGet == nil {
		vm.megaGet = new(megaGet)
	}
	if vm.megaPut == nil {
		vm.megaPut = new(megaPut)
	}
	table, thunk := unsafe.Pointer(vm.megaGet), megaGetThunk
	if put {
		table, thunk = unsafe.Pointer(vm.megaPut), megaPutThunk
	}
	if thunk == 0 || unsafe.Sizeof(ident.Identifier{}) != 8 {
		m.B(exit)
		return
	}
	m.Load64(jit.S2, jit.S0, offExotic)
	m.BranchNonZero(jit.S2, exit)
	m.Load64(jit.S4, rRegs, c.off(prop)) // *String
	m.Load64(jit.S4, jit.S4, offStrKey)  // *PropertyKey
	m.BranchZero(jit.S4, exit)
	m.Load32(jit.S5, jit.S4, offKeyIdx)
	m.BranchNonZero(jit.S5, exit)
	m.Load64(jit.S5, jit.S4, offKeySym)
	m.BranchNonZero(jit.S5, exit)
	m.Load64(jit.S8, jit.S4, offKeyName) // the atom
	if put {
		c.loadValue(jit.S1, jit.S2, val)
	}
	m.MovImm(jit.S7, uint64(uintptr(table)))
	m.MovImm(jit.S9, uint64(thunk))
	m.CallThunk(jit.S9)
	m.BranchNonZero(jit.S0, exit)
	if !put {
		c.storeValue(val, jit.S1, jit.S2)
	}
	m.B(done)
}

// onlyTypedKind reports the kind of typed array a get_by_val or put_by_val
// site's profile shows it saw alone, if one that generated code accesses
// (and stores to, for put).
func onlyTypedKind(profile uint16, put bool) (typedArrayKind, bool) {
	if profile == 0 || profile&byValOther != 0 || profile&(profile-1) != 0 {
		return 0, false
	}
	kind := typedArrayKind(bits.TrailingZeros16(profile))
	if put && kind == kindUint8Clamped {
		return 0, false
	}
	for _, k := range typedKinds {
		if k.kind == kind {
			return kind, true
		}
	}
	return 0, false
}

// typedByVal emits, ahead of byVal's code, the path of a site that saw
// only typed arrays of one kind (JSC's DFG specializes on the ArrayProfile
// so): this kind, an index within the view's own vector, no buffer
// detached since; anything else goes on to byVal's code. (Mandreel spent
// a quarter of its generated code in the typed array path byVal keeps
// cold, through a chain of kind checks.)
func (c *jitCompiler) typedByVal(base, prop, val bytecode.VirtualRegister, put bool, kind typedArrayKind, done, exit *jit.Label) bool {
	m := &c.m
	general := jit.NewLabel()
	m.Load64(jit.S0, rRegs, c.off(base)+8)
	m.Branch64Imm(jit.NE, jit.S0, uint32(tagObject), general)
	m.Load64(jit.S0, rRegs, c.off(base))
	m.Load64(jit.S2, jit.S0, offInternal)
	m.MovImm(jit.S3, uint64(typedArrayType))
	m.Branch64(jit.NE, jit.S2, jit.S3, general)
	if !c.exactInt32(jit.S1, prop, exit) {
		return false
	}
	m.ZeroExt32(jit.S1, jit.S1)
	m.Load64(jit.S2, jit.S0, offInternal+8) // *typedArray
	m.Load8(jit.S3, jit.S2, offTAKind)
	m.Branch64Imm(jit.NE, jit.S3, uint32(kind), general)
	m.Load64(jit.S4, jit.S2, offTAVLen)
	m.Branch64(jit.UGE, jit.S1, jit.S4, general)
	m.Load64(jit.S5, jit.S2, offTAVEpoch)
	m.MovImm(jit.S6, uint64(uintptr(unsafe.Pointer(&c.cb.realm.vm.detachEpoch))))
	m.Load64(jit.S6, jit.S6, 0)
	m.Branch64(jit.NE, jit.S5, jit.S6, general)
	m.Load64(jit.S7, jit.S2, offTAVector)
	// The other path may store anything to the destination: no facts.
	factsOn := c.factsOn
	c.factsOn = false
	if put {
		c.loadValue(jit.S8, jit.S9, val)
		m.Branch64Imm(jit.ULE, jit.S9, maxTag, general)
		m.Xor64(jit.S9, jit.S9, rMask)
		m.MovToF(2, jit.S9)
		c.storeElement(kind, general)
	} else {
		c.loadElement(kind)
		c.storeNum(val, 0)
	}
	c.factsOn = factsOn
	m.B(done)
	m.Bind(general)
	return true
}

// reprofileExit is, for a get_by_val or put_by_val site at pc that had
// not run when its code block was compiled (profile 0), where its code goes
// for a typed array: it notes that the code should be compiled again and
// exits to the interpreter, which runs the instruction and fills the
// profile, so that the code compiled next has the path of the kind the site
// sees first (typedByVal). JSC's DFG exits at code that never ran so (its
// ForceOSRExit). At most jitReprofileMax times per code block; nil
// otherwise.
func (c *jitCompiler) reprofileExit(pc int, profile uint16) *jit.Label {
	if profile != 0 || c.cb.jit.reprofiled >= jitReprofileMax || c.inline != nil {
		return nil
	}
	if c.reprofile == nil {
		c.reprofile = new(uint32)
	}
	m := &c.m
	l, flag := jit.NewLabel(), c.reprofile
	c.cold = append(c.cold, func() {
		m.Bind(l)
		m.MovImm(jit.S2, uint64(uintptr(unsafe.Pointer(flag))))
		m.MovImm(jit.S3, 1)
		m.Store32(jit.S3, jit.S2, 0)
		m.B(c.exit(pc))
	})
	return l
}

// jitReprofileMax bounds the recompilations for reprofile per code block.
const jitReprofileMax = 4

// jitReprofiles counts checkReprofile's jettisons (for tests; atomic, as
// jitCalls).
var jitReprofiles atomic.Int64

// checkReprofile throws away cb's code, which stopped, if it asked for
// that (reprofileExit): it is compiled again at the next entry, its
// entries are disabled so that callers' call caches leave it.
func (vm *VM) checkReprofile(cb *CodeBlock, code *jitCode) {
	s := &cb.jit
	reprofile := code.reprofile != nil && *code.reprofile != 0
	optWanted := code.optWanted != nil && *code.optWanted != 0
	if !reprofile && !optWanted && (code.inlineStop == nil || *code.inlineStop == 0) || s.code != code {
		return
	}
	for _, o := range code.optLoops {
		s.noteOptLoop(o)
	}
	for _, site := range code.inlineSites {
		if site.exits >= inlineExitLimit {
			if s.noInlineAt == nil {
				s.noInlineAt = map[int]bool{}
			}
			s.noInlineAt[site.pc] = true
		}
	}
	for _, e := range code.entries {
		if e != nil {
			e.disabled = true
		}
	}
	if vm.jitCalls > 0 {
		vm.jitGraveyard = append(vm.jitGraveyard, s.code) // maybe running, suspended
	}
	// For profiles, the interpreter warms it up again, so that the other
	// sites that had not run get theirs too; for an inlined call that
	// stopped too often, it is compiled again at the next entry.
	s.code = nil
	if reprofile {
		s.hotness = 0
		s.reprofiled++
		jitReprofiles.Add(1)
	} else {
		s.hotness = jitThreshold
	}
	if jitLog {
		println("jit: reprofile", cb.name)
	}
}

// typedKinds are the typed array kinds generated code accesses, most used
// first, with the shift of their element size.
var typedKinds = []struct {
	kind  typedArrayKind
	shift uint32
}{{kindInt32, 2}, {kindFloat64, 3}, {kindUint8, 0}, {kindFloat32, 2}, {kindInt8, 0},
	{kindInt16, 1}, {kindUint16, 1}, {kindUint32, 2}, {kindUint8Clamped, 0}}

// elemOf is the element type of an integer typed array kind.
func elemOf(kind typedArrayKind) jit.Elem {
	switch kind {
	case kindInt8:
		return jit.ElemS8
	case kindUint8, kindUint8Clamped:
		return jit.ElemU8
	case kindInt16:
		return jit.ElemS16
	case kindUint16:
		return jit.ElemU16
	case kindUint32:
		return jit.ElemU32
	}
	return jit.ElemS32
}

// loadElement loads element S1 of kind at S7 into F0.
func (c *jitCompiler) loadElement(kind typedArrayKind) {
	m := &c.m
	switch kind {
	case kindFloat32:
		m.LoadIdxF32(0, jit.S7, jit.S1)
		c.canonicalNaN(0)
	case kindFloat64:
		m.LoadIdxF64(0, jit.S7, jit.S1)
		c.canonicalNaN(0)
	default:
		e := elemOf(kind)
		m.LoadIdx(e, jit.S2, jit.S7, jit.S1)
		if e == jit.ElemU32 {
			m.CvtU32(0, jit.S2)
		} else {
			m.CvtS32(0, jit.S2)
		}
	}
}

// canonicalNaN replaces a NaN in d by the canonical one (what Number does;
// typed arrays may hold any NaN bits).
func (c *jitCompiler) canonicalNaN(d jit.F) {
	m := &c.m
	nan, done := jit.NewLabel(), jit.NewLabel()
	m.FBranchNaN(d, nan)
	m.B(done)
	m.Bind(nan)
	m.MovImm(rTmp, math.Float64bits(math.NaN()))
	m.MovToF(d, rTmp)
	m.Bind(done)
}

// storeElement stores F2 to element S1 of kind at S7.
func (c *jitCompiler) storeElement(kind typedArrayKind, exit *jit.Label) {
	m := &c.m
	switch kind {
	case kindFloat64:
		m.StoreIdxF64(2, jit.S7, jit.S1)
	case kindFloat32:
		m.StoreIdxF32(2, jit.S7, jit.S1)
	case kindUint8Clamped:
		m.B(exit)
	default:
		m.TruncInt64(jit.S2, 2, exit)
		m.StoreIdx(elemOf(kind), jit.S2, jit.S7, jit.S1)
	}
}

// cachedGet reads a property of object S0 through get_by_id cache c and its
// polymorphic cases (getByIdCache.lookup) into register dst; data
// properties only. Clobbers S1-S9.
func (c *jitCompiler) cachedGet(cache *getByIdCache, dst bytecode.VirtualRegister, exit *jit.Label) {
	c.cachedGetInto(cache, dst, true, exit)
}

var (
	offMegaShape    = int(unsafe.Offsetof(megaGetEntry{}.shape))
	offMegaName     = int(unsafe.Offsetof(megaGetEntry{}.name))
	offMegaVersion  = int(unsafe.Offsetof(megaGetEntry{}.version))
	offMegaIndex    = int(unsafe.Offsetof(megaGetEntry{}.index))
	offMegaHolder   = int(unsafe.Offsetof(megaGetEntry{}.holder))
	offMegaProto    = int(unsafe.Offsetof(megaGetEntry{}.proto))
	offMegaEpoch    = int(unsafe.Offsetof(megaGetEntry{}.epoch))
	offMegaEpochAt  = int(unsafe.Offsetof(megaGetEntry{}.epochAt))
	offMegaNewShape = int(unsafe.Offsetof(megaGetEntry{}.newShape))
)

// megaGetProbe looks own data property k of object S0 up in the VM's
// megamorphic cache (cold code, after the cases of a full cache missed)
// through the shared megaGetThunk, as JSC's sites call a shared stub: on
// a hit it stores it to dst and branches to done, else to exit.
func (c *jitCompiler) megaGetProbe(k PropertyKey, dst bytecode.VirtualRegister, done, exit *jit.Label) {
	m := &c.m
	vm := c.cb.realm.vm
	if vm.megaGet == nil {
		vm.megaGet = new(megaGet)
	}
	if megaGetThunk == 0 {
		m.B(exit)
		return
	}
	m.MovImm(jit.S7, uint64(uintptr(unsafe.Pointer(vm.megaGet))))
	m.MovImm(jit.S8, uint64(*(*uintptr)(unsafe.Pointer(&k.name))))
	m.MovImm(jit.S9, uint64(megaGetThunk))
	m.CallThunk(jit.S9)
	m.BranchNonZero(jit.S0, exit)
	c.storeValue(dst, jit.S1, jit.S2)
	m.B(done)
}

// cachedGetInto is cachedGet; with store false the property stays in S4, S5
// instead of going to dst.
func (c *jitCompiler) cachedGetInto(cache *getByIdCache, dst bytecode.VirtualRegister, store bool, exit *jit.Label) {
	c.cachedGetMega(cache, nil, dst, store, exit)
}

// cachedGetMega is cachedGetInto; with mega (a get_by_id's key) the
// megamorphic cache is looked at after the cases.
func (c *jitCompiler) cachedGetMega(cache *getByIdCache, mega *PropertyKey, dst bytecode.VirtualRegister, store bool, exit *jit.Label) {
	m := &c.m
	done := jit.NewLabel()
	miss := jit.NewLabel()
	var other *jit.Label
	if store {
		other = jit.NewLabel()
		getter := c.getter
		c.cold = append(c.cold, func() { c.otherCase(other, getter, dst, done, exit) })
	}
	if store {
		c.specializedGet(cache, dst, done)
		if cache.poly != nil {
			// A polymorphic site: its other cases as constants too (JSC's
			// polymorphic access stubs; richards' tasks are of four
			// classes, and reading each case from the cache took some 20
			// instructions).
			for i := range cache.poly.entries {
				c.specializedGetCase(&cache.poly.entries[i], dst, done)
			}
		}
	}
	m.MovImm(jit.S3, uint64(uintptr(unsafe.Pointer(cache))))
	c.getCase(dst, store, miss, done, other)
	m.Bind(miss)
	m.MovImm(jit.S3, uint64(uintptr(unsafe.Pointer(cache))))
	m.Load64(jit.S2, jit.S3, offGCPoly)
	m.BranchZero(jit.S2, exit)
	for i := 0; i < polyEntries; i++ {
		next := jit.NewLabel()
		m.AddImm64(jit.S3, jit.S2, uint32(offGCPolyEntries+i*sizeGetByIdCache))
		c.getCase(dst, store, next, done, other)
		m.Bind(next)
	}
	if mega != nil && mega.sym == nil && mega.idx == 0 && store {
		probe, k := jit.NewLabel(), *mega
		m.B(probe)
		c.cold = append(c.cold, func() {
			m.Bind(probe)
			c.megaGetProbe(k, dst, done, exit)
		})
	} else {
		m.B(exit)
	}
	m.Bind(done)
}

// toBoolean branches to truthy or falsy by ToBoolean of the value S1, S0
// (p and tag words), or to other for BigInts. Clobbers S2 and F0.
func (c *jitCompiler) toBoolean(truthy, falsy, other *jit.Label) {
	m := &c.m
	number, object, str := jit.NewLabel(), jit.NewLabel(), jit.NewLabel()
	m.Branch64Imm(jit.UGT, jit.S0, maxTag, number)
	m.Branch64Imm(jit.EQ, jit.S0, uint32(tagObject), object)
	m.Branch64Imm(jit.EQ, jit.S0, uint32(tagTrue), truthy)
	m.Branch64Imm(jit.ULE, jit.S0, uint32(tagFalse), falsy) // undefined, null, false
	m.Branch64Imm(jit.EQ, jit.S0, uint32(tagString), str)
	m.Branch64Imm(jit.EQ, jit.S0, uint32(tagSymbol), truthy)
	m.Branch64Imm(jit.UGT, jit.S0, uint32(tagTrue), other) // BigInts and the rest
	m.B(falsy)                                             // undefined, null, false
	// Objects are true but for [[IsHTMLDDA]] ones (document.all).
	m.Bind(object)
	m.Load8(jit.S2, jit.S1, offHTMLDDA)
	m.BranchNonZero(jit.S2, falsy)
	m.B(truthy)
	// Strings are true unless empty.
	m.Bind(str)
	m.Load64(jit.S2, jit.S1, offStrLength)
	m.BranchZero(jit.S2, falsy)
	m.B(truthy)
	m.Bind(number)
	m.Xor64(jit.S0, jit.S0, rMask)
	m.MovToF(0, jit.S0)
	m.FBranchZeroOrNaN(0, falsy)
	m.B(truthy)
}

// applyFacts updates the type facts after the instruction ins at pc:
// facts on the registers it writes are gone, those its fast path proved
// are added if it is a factProducer, and where its slow path resumes at
// the next instruction (not a block start) those are checked on entry.
func (c *jitCompiler) applyFacts(pc int, ins []uint32, blockStart map[int]bool) {
	op := bytecode.OpcodeID(ins[0])
	// (The callback is made once: this runs for every instruction.)
	c.defs = c.defs[:0]
	if c.addDef == nil {
		c.addDef = func(r bytecode.VirtualRegister) {
			delete(c.facts, r)
			c.defs = append(c.defs, r)
		}
	}
	for cp := 0; cp < 4; cp++ {
		bytecode.ComputeDefsForInstruction(c.cb.u, ins, cp, c.addDef)
	}
	next := pc + op.Length()
	for k := range c.fcache {
		if slices.Contains(c.defs, c.fcache[k].r) {
			c.fcache[k] = jitFEntry{}
		}
	}
	if jitClobbersF[op] {
		c.fcache = [len(jitFCache)]jitFEntry{}
	}
	defer func() {
		// Where a slow path resumes here the cache is filled again.
		if c.opt == nil && slices.Contains(c.slowResumes, next) && !blockStart[next] {
			c.refills[next] = c.fcache
		}
	}()
	if !factProducers[op] || len(c.pending) == 0 || c.cb.jit.noSpecAt[next] {
		return
	}
	for _, f := range c.pendingF {
		c.fcache[f.f] = jitFEntry{r: f.r, valid: true}
		c.fuse = f.f
	}
	// (An optimized loop's slow paths resume in the baseline code.)
	resumesNext := c.opt == nil && slices.Contains(c.slowResumes, next) && !blockStart[next]
	for _, f := range c.pending {
		if !f.store && slices.Contains(c.defs, f.r) || c.facts[f.r]&f.f == f.f {
			continue // a fact on the old value of a register written
		}
		c.facts[f.r] |= f.f
		if resumesNext {
			c.resumeChecks[next] = append(c.resumeChecks[next], f)
		}
	}
}

var (
	offStrApp   = int(unsafe.Offsetof(String{}.app))
	offAppB     = int(unsafe.Offsetof(appendBuffer{}.b))
	offAppASCII = int(unsafe.Offsetof(appendBuffer{}.ascii))
	offSPoolN   = int(unsafe.Offsetof(jitStringPool{}.n))
	offSPoolS   = int(unsafe.Offsetof(jitStringPool{}.strs))
)

// appendableConstant reports whether constant v can be appendString's t:
// a flat ASCII string, not empty, of at most maxAppendChunk units.
func appendableConstant(v Value) bool {
	if !v.IsString() {
		return false
	}
	t := v.StringRef()
	return t.left == nil && t.isASCII && t.length > 0 && t.length <= maxAppendChunk
}

// appendString is s + t for strings in lhs and rhs where Concat would
// append t to s's append buffer in place (appendConcat): s flat ASCII and
// ending where its buffer's used part ends, t flat ASCII and nonempty,
// room in the buffer. It copies t's bytes and takes the result's String
// from the VM's pool (jitStringPool), then stores it to dst and branches
// to done; anything else branches to slow. (base64's result += table[i]
// left generated code 2.6 million times a run.)
func (c *jitCompiler) appendString(dst, lhs, rhs bytecode.VirtualRegister, done, slow *jit.Label) {
	m := &c.m
	vm := c.cb.realm.vm
	vm.fillStringPool()
	m.Load64(jit.S1, rRegs, c.off(lhs)+8)
	m.Branch64Imm(jit.NE, jit.S1, uint32(tagString), slow)
	m.Load64(jit.S0, rRegs, c.off(lhs)) // s
	if v, ok := c.constant(rhs); ok {
		// A constant string (appendableConstant): its pointer; the code
		// block's constants keep it alive.
		m.MovImm(jit.S2, uint64(uintptr(unsafe.Pointer(v.StringRef()))))
	} else {
		m.Load64(jit.S3, rRegs, c.off(rhs)+8)
		m.Branch64Imm(jit.NE, jit.S3, uint32(tagString), slow)
		m.Load64(jit.S2, rRegs, c.off(rhs)) // t
	}
	// s: flat ASCII with an ASCII append buffer it ends.
	m.Load8(jit.S4, jit.S0, offStrIsASCII)
	m.BranchZero(jit.S4, slow)
	m.Load64(jit.S4, jit.S0, offStrLeft)
	m.BranchNonZero(jit.S4, slow)
	m.Load64(jit.S5, jit.S0, offStrApp) // ab
	m.BranchZero(jit.S5, slow)
	m.Load8(jit.S4, jit.S5, offAppASCII)
	m.BranchZero(jit.S4, slow)
	m.Load64(jit.S6, jit.S5, offAppB+8) // len(ab.b)
	m.Load64(jit.S7, jit.S0, offStrLength)
	m.Branch64(jit.NE, jit.S6, jit.S7, slow)
	// t: flat ASCII, not empty.
	m.Load8(jit.S4, jit.S2, offStrIsASCII)
	m.BranchZero(jit.S4, slow)
	m.Load64(jit.S4, jit.S2, offStrLeft)
	m.BranchNonZero(jit.S4, slow)
	m.Load64(jit.S8, jit.S2, offStrLength) // n
	m.BranchZero(jit.S8, slow)
	m.MovImm(jit.S4, maxAppendChunk)
	m.Branch64(jit.UGT, jit.S8, jit.S4, slow)
	// Room: len + n <= cap, and within maxStringLength.
	m.Add64(jit.S4, jit.S6, jit.S8) // the new length
	m.Load64(jit.S9, jit.S5, offAppB+16)
	m.Branch64(jit.UGT, jit.S4, jit.S9, slow)
	m.MovImm(jit.S9, maxStringLength)
	m.Branch64(jit.UGT, jit.S4, jit.S9, slow)
	// The result's String from the pool (before anything is written).
	pool := vm.jitStrings
	m.MovImm(jit.S7, uint64(uintptr(unsafe.Pointer(pool))))
	m.Load64(jit.S9, jit.S7, offSPoolN)
	m.BranchZero(jit.S9, slow)
	m.SubImm64(jit.S9, jit.S9, 1)
	m.Store64(jit.S9, jit.S7, offSPoolN)
	m.AddShift64(jit.S9, jit.S7, jit.S9, 3)
	m.Load64(jit.S7, jit.S9, offSPoolS) // the String
	m.StoreZero64(jit.S9, offSPoolS)
	// Copy t's n bytes to ab.b[len:] (S1 = from, S3 = to, S9 = i).
	m.Load64(jit.S1, jit.S2, offStrASCII)
	m.Load64(jit.S3, jit.S5, offAppB)
	m.Add64(jit.S3, jit.S3, jit.S6)
	m.MovImm(jit.S9, 0)
	loop := jit.NewLabel()
	m.Bind(loop)
	m.LoadIdx(jit.ElemU8, jit.S2, jit.S1, jit.S9)
	m.StoreIdx(jit.ElemU8, jit.S2, jit.S3, jit.S9)
	m.AddImm64(jit.S9, jit.S9, 1)
	m.Branch64(jit.ULT, jit.S9, jit.S8, loop)
	m.Store64(jit.S4, jit.S5, offAppB+8) // ab.b = ab.b[:len+n]
	// The String: data = &ab.b[0], length, the buffer (isASCII is set in
	// the pool).
	m.Load64(jit.S1, jit.S5, offAppB)
	m.Store64(jit.S1, jit.S7, offStrASCII)
	m.Store64(jit.S4, jit.S7, offStrLength)
	m.Store64(jit.S5, jit.S7, offStrApp)
	m.MovImm(jit.S1, uint64(tagString))
	c.storeValue(dst, jit.S7, jit.S1)
	m.B(done)
}

// lengthOfObject is get_length of object S0 that is not an array: the
// length of a typed array whose length is %TypedArray%.prototype's getter
// (to F0, then to num), else a length property found through cache.
func (c *jitCompiler) lengthOfObject(cache *getByIdCache, dst bytecode.VirtualRegister, num, done, exit *jit.Label) {
	m := &c.m
	typed, hit := jit.NewLabel(), jit.NewLabel()
	m.Load8(jit.S2, jit.S0, offClass)
	m.Branch64Imm(jit.EQ, jit.S2, uint32(ClassTypedArray), typed)
	c.cachedGetInto(cache, dst, true, exit)
	m.B(done)

	m.Bind(typed)
	m.Load64(jit.S2, jit.S0, offInternal)
	m.MovImm(jit.S3, uint64(typedArrayType))
	m.Branch64(jit.NE, jit.S2, jit.S3, exit)
	getter := c.cb.realm.typedArrayLength
	m.MovImm(jit.S3, uint64(uintptr(unsafe.Pointer(cache))))
	next := jit.NewLabel()
	c.getterCase(getter, next, hit)
	m.Bind(next)
	m.MovImm(jit.S3, uint64(uintptr(unsafe.Pointer(cache))))
	m.Load64(jit.S2, jit.S3, offGCPoly)
	m.BranchZero(jit.S2, exit)
	for i := 0; i < polyEntries; i++ {
		next := jit.NewLabel()
		m.AddImm64(jit.S3, jit.S2, uint32(offGCPolyEntries+i*sizeGetByIdCache))
		c.getterCase(getter, next, hit)
		m.Bind(next)
	}
	m.B(exit)

	// A typed array of fixed length with its buffer in bounds (others take
	// the slow path).
	m.Bind(hit)
	m.Load64(jit.S2, jit.S0, offInternal+8) // *typedArray
	m.Load64(jit.S4, jit.S2, offTALength)
	m.Branch64Imm(jit.SLT, jit.S4, 0, exit) // length-tracking
	m.Load64(jit.S5, jit.S2, offTABuffer)
	m.Load64(jit.S6, jit.S5, offInternal)
	m.MovImm(jit.S3, uint64(arrayBufferDataType))
	m.Branch64(jit.NE, jit.S6, jit.S3, exit)
	m.Load64(jit.S6, jit.S5, offInternal+8) // *arrayBufferData
	m.Load8(jit.S7, jit.S6, offBufDetached)
	m.BranchNonZero(jit.S7, exit)
	m.Load64(jit.S8, jit.S6, offBufData+8) // data len
	m.Load64(jit.S9, jit.S2, offTAByteOff)
	m.Load8(jit.S3, jit.S2, offTAKind)
	// byteOffset + length<<shift must fit in the buffer (the kinds are
	// grouped by size, in their order).
	inBounds := jit.NewLabel()
	for _, k := range []struct {
		upTo  typedArrayKind
		shift uint32
	}{{kindUint8Clamped, 0}, {kindUint16, 1}, {kindUint32, 2}, {kindFloat16, 1}, {kindFloat32, 2}, {kindBigUint64, 3}} {
		next := jit.NewLabel()
		m.Branch64Imm(jit.UGT, jit.S3, uint32(k.upTo), next)
		m.AddShift64(jit.S9, jit.S9, jit.S4, k.shift)
		m.B(inBounds)
		m.Bind(next)
	}
	m.B(exit)
	m.Bind(inBounds)
	m.Branch64(jit.UGT, jit.S9, jit.S8, exit)
	m.CvtS64(0, jit.S4)
	m.B(num)
}

// getterCase checks the cache case at S3 for object S0 like getCase, for
// a property that must be an accessor with getter as its getter: on a hit
// it branches to hit, otherwise to miss.
func (c *jitCompiler) getterCase(getter *Object, miss, hit *jit.Label) {
	m := &c.m
	m.Load8(jit.S6, jit.S3, offGCAccessor)
	m.BranchZero(jit.S6, miss)
	found := jit.NewLabel()
	c.getCaseHolder(miss, found)
	m.Bind(found)
	m.Load32(jit.S6, jit.S3, offGCIndex)
	m.Load64(jit.S7, jit.S9, offProps+8) // vals
	m.AddShift64(jit.S7, jit.S7, jit.S6, 4)
	m.Load64(jit.S4, jit.S7, 0) // the *Accessor
	m.Load64(jit.S4, jit.S4, offAccessorGet)
	m.MovImm(jit.S5, uint64(uintptr(unsafe.Pointer(getter))))
	m.Branch64(jit.NE, jit.S4, jit.S5, miss)
	m.B(hit)
}

// getterSite is a get_by_id whose accessor cases call their getter in
// the code (JSC's getter access case).
type getterSite struct {
	pc   int
	base bytecode.VirtualRegister
}

// getterCall calls the getter S0 of get_by_id site g with the base as
// this, through the site's call cache, which the slow path fills
// (fillGetterIC); the result goes to dst.
func (c *jitCompiler) getterCall(g *getterSite, dst bytecode.VirtualRegister, done, exit *jit.Label) {
	m := &c.m
	ic := c.callICs[g.pc]
	if ic == nil {
		ic = &callIC{}
		c.callICs[g.pc] = ic
	}
	m.MovImm(jit.S1, uint64(tagObject))
	hit := jit.NewLabel()
	c.callCases(ic, hit, false)
	c.closureCases(ic, hit, exit, false)
	m.Bind(hit)
	c.directCall(g.pc, g.base, 1, dst, true, exit, false)
	m.B(done)
}

// getCase checks the cache case at S3 for object S0: on a hit it stores the
// property to dst (or leaves it in S4, S5) and branches to done, otherwise
// it branches to miss. Accessor and miss cases whose receiver shape
// matches branch to other (otherCase, shared by a site's cases), or miss
// if it is nil.
func (c *jitCompiler) getCase(dst bytecode.VirtualRegister, store bool, miss, done, other *jit.Label) {
	m := &c.m
	if offGCMiss != offGCAccessor+1 {
		panic("vm: getByIdCache's accessor and miss are not adjacent")
	}
	if other == nil {
		other = miss
	}
	m.Load64(jit.S4, jit.S3, offGCShape)
	m.BranchZero(jit.S4, miss)
	m.Load64(jit.S5, jit.S0, offProps)
	m.Branch64(jit.NE, jit.S4, jit.S5, miss)
	m.Load16(jit.S6, jit.S3, offGCAccessor)
	m.BranchNonZero(jit.S6, other)
	found := jit.NewLabel()
	c.getCaseChain(miss, found)
	m.Bind(found)
	m.Load32(jit.S6, jit.S3, offGCIndex)
	m.Load64(jit.S7, jit.S9, offProps+8) // vals
	m.AddShift64(jit.S7, jit.S7, jit.S6, 4)
	m.Load64(jit.S4, jit.S7, 0)
	m.Load64(jit.S5, jit.S7, 8)
	if store {
		c.storeValue(dst, jit.S4, jit.S5)
	}
	m.B(done)
}

// specializedGet compiles the case a monomorphic get cache holds when the
// code is compiled into the code itself, as JSC's inline caches patch the
// structure and offset into the code: the receiver's shape and, for a
// dictionary (the global object), its version, and for a property of a
// prototype the first prototype and the VM's protoEpoch are compared with
// constants, and the property is read from a constant holder and slot. A
// receiver it does not fit falls through to the cache's cases. Object S0;
// branches to done with the property stored to dst.
func (c *jitCompiler) specializedGet(cache *getByIdCache, dst bytecode.VirtualRegister, done *jit.Label) {
	c.specializedGetCase(cache, dst, done)
}

// specializedGetCase is specializedGet for one case of a site's cache,
// the first or one of a polymorphic site's others.
func (c *jitCompiler) specializedGetCase(cache *getByIdCache, dst bytecode.VirtualRegister, done *jit.Label) {
	m := &c.m
	s := cache.shape
	if s == nil || cache.version != s.version || cache.accessor || cache.miss {
		return
	}
	vm := c.cb.realm.vm
	var holder *Object
	if cache.depth > 0 {
		// The chain must be current (its epoch), so that its holder is.
		if cache.epoch != vm.protoEpoch || cache.holder == nil || cache.holder.props.shape == nil ||
			cache.holder.props.shape.dictionary {
			return
		}
		holder = cache.holder
	}
	next := jit.NewLabel()
	c.keepCells = append(c.keepCells, s)
	m.Load64(jit.S5, jit.S0, offProps)
	m.MovImm(jit.S4, uint64(uintptr(unsafe.Pointer(s))))
	m.Branch64(jit.NE, jit.S4, jit.S5, next)
	if s.dictionary {
		m.Load32(jit.S5, jit.S5, offShapeVer)
		m.Branch32Imm(jit.NE, jit.S5, s.version, next)
	}
	h := jit.S0
	if holder != nil {
		c.keepCells = append(c.keepCells, cache.chain[0].obj, holder)
		m.Load64(jit.S5, jit.S0, offProto)
		m.MovImm(jit.S4, uint64(uintptr(unsafe.Pointer(cache.chain[0].obj))))
		m.Branch64(jit.NE, jit.S4, jit.S5, next)
		m.MovImm(jit.S5, uint64(uintptr(unsafe.Pointer(&vm.protoEpoch))))
		m.Load64(jit.S5, jit.S5, 0)
		m.MovImm(jit.S4, cache.epoch)
		m.Branch64(jit.NE, jit.S4, jit.S5, next)
		m.MovImm(jit.S9, uint64(uintptr(unsafe.Pointer(holder))))
		h = jit.S9
	}
	m.Load64(jit.S7, h, offProps+8) // vals
	m.Load64(jit.S4, jit.S7, int(cache.index)*valueSize)
	m.Load64(jit.S5, jit.S7, int(cache.index)*valueSize+8)
	c.storeValue(dst, jit.S4, jit.S5)
	m.B(done)
	m.Bind(next)
}

// otherCase is the accessor and miss cases of a site (cold), for the case
// at S3 whose receiver shape matched object S0: an absent property reads
// undefined, a getter is called if the site calls getters (getter); else,
// or if the prototypes changed, it branches to exit.
func (c *jitCompiler) otherCase(other *jit.Label, getter *getterSite, dst bytecode.VirtualRegister, done, exit *jit.Label) {
	m := &c.m
	m.Bind(other)
	found, accessor := jit.NewLabel(), jit.NewLabel()
	c.getCaseHolder(exit, found)
	m.Bind(found)
	m.Load8(jit.S6, jit.S3, offGCMiss)
	m.BranchZero(jit.S6, accessor)
	m.Load64(jit.S6, jit.S9, offProto)
	m.BranchNonZero(jit.S6, exit)
	m.MovImm(jit.S4, 0)
	m.MovImm(jit.S5, uint64(tagUndefined))
	c.storeValue(dst, jit.S4, jit.S5)
	m.B(done)
	m.Bind(accessor)
	if dv := c.cb.realm.dataViewByteLength; dv != nil {
		// DataView's byteLength, computed here (async-fs reads it in a
		// loop condition: 0.7 million slow paths a run).
		m.Load32(jit.S6, jit.S3, offGCIndex)
		m.Load64(jit.S7, jit.S9, offProps+8) // vals
		m.AddShift64(jit.S7, jit.S7, jit.S6, 4)
		m.Load64(jit.S1, jit.S7, 0) // the *Accessor
		m.Load64(jit.S1, jit.S1, offAccessorGet)
		m.MovImm(jit.S2, uint64(uintptr(unsafe.Pointer(dv))))
		notDV := jit.NewLabel()
		m.Branch64(jit.NE, jit.S1, jit.S2, notDV)
		c.dataViewByteLength(dst, done, exit)
		m.Bind(notDV)
	}
	if getter == nil {
		m.B(exit)
		return
	}
	m.Load32(jit.S6, jit.S3, offGCIndex)
	m.Load64(jit.S7, jit.S9, offProps+8) // vals
	m.AddShift64(jit.S7, jit.S7, jit.S6, 4)
	m.Load64(jit.S1, jit.S7, 0) // the *Accessor
	m.Load64(jit.S1, jit.S1, offAccessorGet)
	m.BranchZero(jit.S1, exit)
	m.Mov(jit.S0, jit.S1)
	c.getterCall(getter, dst, done, exit)
}

// dataViewByteLength stores DataView.prototype.byteLength of object S0 to
// dst and branches to done, for a DataView of fixed length within its
// buffer; anything else (another receiver, a length-tracking or detached
// view, one out of bounds) branches to exit, where the getter throws or
// computes it.
func (c *jitCompiler) dataViewByteLength(dst bytecode.VirtualRegister, done, exit *jit.Label) {
	m := &c.m
	m.Load64(jit.S2, jit.S0, offInternal)
	m.MovImm(jit.S4, uint64(dataViewType))
	m.Branch64(jit.NE, jit.S2, jit.S4, exit)
	m.Load64(jit.S2, jit.S0, offInternal+8) // *dataView
	m.Load64(jit.S4, jit.S2, offDVByteLength)
	m.Branch64Imm(jit.SLT, jit.S4, 0, exit) // length-tracking
	m.Load64(jit.S5, jit.S2, offDVBuffer)
	m.Load64(jit.S6, jit.S5, offInternal)
	m.MovImm(jit.S7, uint64(arrayBufferDataType))
	m.Branch64(jit.NE, jit.S6, jit.S7, exit)
	m.Load64(jit.S6, jit.S5, offInternal+8) // *arrayBufferData
	m.Load8(jit.S7, jit.S6, offBufDetached)
	m.BranchNonZero(jit.S7, exit)
	m.Load64(jit.S8, jit.S6, offBufData+8) // data len
	m.Load64(jit.S7, jit.S2, offDVByteOffset)
	m.Add64(jit.S7, jit.S7, jit.S4)
	m.Branch64(jit.UGT, jit.S7, jit.S8, exit)
	m.CvtS64(0, jit.S4)
	c.storeNum(dst, 0)
	m.B(done)
}

// getCaseHolder checks that object S0 has the receiver shape of the cache
// case at S3 and the prototypes walked theirs: then it branches to found
// with the property's holder in S9, otherwise to miss.
func (c *jitCompiler) getCaseHolder(miss, found *jit.Label) {
	m := &c.m
	m.Load64(jit.S4, jit.S3, offGCShape)
	m.BranchZero(jit.S4, miss)
	m.Load64(jit.S5, jit.S0, offProps)
	m.Branch64(jit.NE, jit.S4, jit.S5, miss)
	c.getCaseChain(miss, found)
}

// getCaseChain is getCaseHolder after the shape (S5) matched: the shape's
// version and the prototypes walked.
func (c *jitCompiler) getCaseChain(miss, found *jit.Label) {
	m := &c.m
	m.Load32(jit.S6, jit.S5, offShapeVer)
	m.Load32(jit.S7, jit.S3, offGCVersion)
	m.Branch32(jit.NE, jit.S6, jit.S7, miss)
	m.Load8(jit.S8, jit.S3, offGCDepth)
	m.Mov(jit.S9, jit.S0) // holder
	m.BranchZero(jit.S8, found)
	// The first prototype must be the chain's first; while the case's
	// epoch is current the rest of the chain is unchanged (see
	// VM.protoEpoch), and the holder is the case's.
	walk := jit.NewLabel()
	epoch := uint64(uintptr(unsafe.Pointer(&c.cb.realm.vm.protoEpoch)))
	m.Load64(rTmp, jit.S3, offGCChain)
	m.Load64(jit.S6, jit.S0, offProto)
	m.Load64(jit.S7, rTmp, offLinkObj)
	m.Branch64(jit.NE, jit.S6, jit.S7, miss)
	m.MovImm(jit.S6, epoch)
	m.Load64(jit.S6, jit.S6, 0)
	m.Load64(jit.S7, jit.S3, offGCEpoch)
	m.Branch64(jit.NE, jit.S6, jit.S7, walk)
	m.Load64(jit.S9, jit.S3, offGCHolder)
	m.B(found)
	c.cold = append(c.cold, func() {
		// Each prototype walked (T0 = its link in the chain) must be the
		// link's object, with its shape; then the epoch is current again.
		link := jit.NewLabel()
		m.Bind(walk)
		m.Bind(link)
		m.Load64(jit.S6, jit.S9, offProto)
		m.Load64(jit.S7, rTmp, offLinkObj)
		m.Branch64(jit.NE, jit.S6, jit.S7, miss)
		m.Load64(jit.S6, jit.S7, offProps)
		m.Load64(jit.S1, rTmp, offLinkShape)
		m.Branch64(jit.NE, jit.S6, jit.S1, miss)
		m.Load32(jit.S6, jit.S1, offShapeVer)
		m.Load32(jit.S1, rTmp, offLinkVersion)
		m.Branch32(jit.NE, jit.S6, jit.S1, miss)
		m.Mov(jit.S9, jit.S7)
		m.AddImm64(rTmp, rTmp, uint32(sizeProtoLink))
		m.SubImm64(jit.S8, jit.S8, 1)
		m.BranchNonZero(jit.S8, link)
		m.MovImm(jit.S6, epoch)
		m.Load64(jit.S6, jit.S6, 0)
		m.Store64(jit.S6, jit.S3, offGCEpoch)
		m.B(found)
	})
}

// cachedPut stores the value S1, S2 to a property of object S0 through
// put_by_id cache c and its polymorphic cases: replacing a data property,
// or adding one (direct: defining it, without the prototype chain).
// Clobbers S3-S9 (putCase uses them all).
func (c *jitCompiler) cachedPut(cache *putByIdCache, direct bool, mega *PropertyKey, exit *jit.Label) {
	m := &c.m
	done := jit.NewLabel()
	miss := jit.NewLabel()
	grow := jit.NewLabel()
	c.cold = append(c.cold, func() {
		m.Bind(grow)
		c.growStorage(done, exit)
	})
	if cache.poly == nil {
		c.specializedPutCase(cache, done)
	} else if allReplacing(cache) {
		// A polymorphic site's cases as constants too (see cachedGetMega),
		// if they all replace: with adding cases among them, the constant
		// ones only came before them (delta-blue -5%).
		c.specializedPutCase(cache, done)
		for i := range cache.poly.entries {
			c.specializedPutCase(&cache.poly.entries[i], done)
		}
	}
	m.MovImm(jit.S3, uint64(uintptr(unsafe.Pointer(cache))))
	c.putCase(miss, done, grow, direct)
	m.Bind(miss)
	m.MovImm(jit.S3, uint64(uintptr(unsafe.Pointer(cache))))
	m.Load64(jit.S3, jit.S3, offPCPoly)
	m.BranchZero(jit.S3, exit)
	for i := 0; i < polyEntries; i++ {
		// putCase clobbers every scratch register: reload the case's address.
		next := jit.NewLabel()
		if i > 0 {
			m.MovImm(jit.S3, uint64(uintptr(unsafe.Pointer(cache))))
			m.Load64(jit.S3, jit.S3, offPCPoly)
		}
		m.AddImm64(jit.S3, jit.S3, uint32(offPCPolyEntries+i*sizePutByIdCache))
		c.putCase(next, done, grow, direct)
		m.Bind(next)
	}
	if mega != nil && megaPutThunk != 0 {
		// The megamorphic cache (cold), through the shared megaPutThunk.
		vm := c.cb.realm.vm
		if vm.megaPut == nil {
			vm.megaPut = new(megaPut)
		}
		probe, k := jit.NewLabel(), *mega
		m.B(probe)
		c.cold = append(c.cold, func() {
			m.Bind(probe)
			m.MovImm(jit.S7, uint64(uintptr(unsafe.Pointer(vm.megaPut))))
			m.MovImm(jit.S8, uint64(*(*uintptr)(unsafe.Pointer(&k.name))))
			m.MovImm(jit.S9, uint64(megaPutThunk))
			m.CallThunk(jit.S9)
			m.BranchNonZero(jit.S0, exit)
			m.B(done)
		})
	} else {
		m.B(exit)
	}
	m.Bind(done)
}

// allReplacing reports whether every case of a polymorphic put cache
// replaces a property (none adds one).
func allReplacing(cache *putByIdCache) bool {
	if cache.newShape != nil {
		return false
	}
	for i := range cache.poly.entries {
		if e := &cache.poly.entries[i]; e.shape != nil && e.newShape != nil {
			return false
		}
	}
	return true
}

// specializedPutCase stores S1, S2 to object S0 for a replacing case of a
// put cache, with its shape and slot as constants (as specializedGet), and
// branches to done; else it falls through.
func (c *jitCompiler) specializedPutCase(cache *putByIdCache, done *jit.Label) {
	s := cache.shape
	if s == nil || cache.version != s.version || cache.newShape != nil {
		return
	}
	m := &c.m
	next := jit.NewLabel()
	c.keepCells = append(c.keepCells, s)
	m.Load64(jit.S5, jit.S0, offProps)
	m.MovImm(jit.S4, uint64(uintptr(unsafe.Pointer(s))))
	m.Branch64(jit.NE, jit.S4, jit.S5, next)
	if s.dictionary {
		m.Load32(jit.S5, jit.S5, offShapeVer)
		m.Branch32Imm(jit.NE, jit.S5, s.version, next)
	}
	m.Load64(jit.S7, jit.S0, offProps+8)
	m.Store64(jit.S1, jit.S7, int(cache.index)*valueSize)
	m.Store64(jit.S2, jit.S7, int(cache.index)*valueSize+8)
	m.B(done)
	m.Bind(next)
}

// putCase checks the put cache case at S3 for object S0 and stores S1, S2
// on a hit (then branches to done), or branches to miss; an add to an
// object whose storage is full branches to grow (growStorage).
func (c *jitCompiler) putCase(miss, done, grow *jit.Label, direct bool) {
	m := &c.m
	m.Load64(jit.S4, jit.S3, offPCShape)
	m.BranchZero(jit.S4, miss)
	m.Load64(jit.S5, jit.S0, offProps)
	m.Branch64(jit.NE, jit.S4, jit.S5, miss)
	m.Load32(jit.S6, jit.S5, offShapeVer)
	m.Load32(jit.S7, jit.S3, offPCVersion)
	m.Branch32(jit.NE, jit.S6, jit.S7, miss)
	add := jit.NewLabel()
	m.Load64(jit.S5, jit.S3, offPCNewShape)
	m.BranchNonZero(jit.S5, add)
	m.Load32(jit.S6, jit.S3, offPCIndex)
	m.Load64(jit.S7, jit.S0, offProps+8)
	m.AddShift64(jit.S7, jit.S7, jit.S6, 4)
	m.Store64(jit.S1, jit.S7, 0)
	m.Store64(jit.S2, jit.S7, 8)
	m.B(done)

	// Adding a property (S5 = the shape after it), as cachedPut in Go: the
	// object must be extensible, the prototype chain unchanged (unless the
	// property is defined directly), and its storage must have room.
	m.Bind(add)
	m.Load8(jit.S6, jit.S0, offObjectExtensible)
	m.BranchZero(jit.S6, miss)
	m.Load8(jit.S6, jit.S0, offObjectWatched) // the slow path bumps the epoch
	m.BranchNonZero(jit.S6, miss)
	if !direct {
		// protoChainMatches: the first prototype, then while the epoch is
		// current nothing else; else the links up to the cache's depth,
		// then the end of the chain or the prototype holding the property.
		// (T0 = the link, S7 = the links left, S6 = the prototype.)
		chainDone, link, walk, chainOK := jit.NewLabel(), jit.NewLabel(), jit.NewLabel(), jit.NewLabel()
		epoch := uint64(uintptr(unsafe.Pointer(&c.cb.realm.vm.protoEpoch)))
		m.Load8(jit.S7, jit.S3, offPCDepth)
		m.Load64(jit.S6, jit.S0, offProto)
		m.BranchZero(jit.S7, chainDone)
		m.Load64(rTmp, jit.S3, offPCChain)
		m.Load64(jit.S8, rTmp, offLinkObj)
		m.Branch64(jit.NE, jit.S6, jit.S8, miss)
		m.MovImm(jit.S8, epoch)
		m.Load64(jit.S8, jit.S8, 0)
		m.Load64(jit.S9, jit.S3, offPCEpoch)
		m.Branch64(jit.NE, jit.S8, jit.S9, walk)
		m.B(chainOK)
		c.cold = append(c.cold, func() {
			m.Bind(walk)
			m.Bind(link)
			m.Load64(jit.S8, rTmp, offLinkObj)
			m.Branch64(jit.NE, jit.S6, jit.S8, miss)
			m.Load64(jit.S8, jit.S6, offProps)
			m.Load64(jit.S9, rTmp, offLinkShape)
			m.Branch64(jit.NE, jit.S8, jit.S9, miss)
			m.Load32(jit.S8, jit.S8, offShapeVer)
			m.Load32(jit.S9, rTmp, offLinkVersion)
			m.Branch32(jit.NE, jit.S8, jit.S9, miss)
			m.Load64(jit.S6, jit.S6, offProto)
			m.AddImm64(rTmp, rTmp, uint32(sizeProtoLink))
			m.SubImm64(jit.S7, jit.S7, 1)
			m.BranchNonZero(jit.S7, link)
			holderOK := jit.NewLabel()
			m.Load8(jit.S8, jit.S3, offPCHolder)
			m.BranchNonZero(jit.S8, holderOK)
			m.BranchNonZero(jit.S6, miss)
			m.Bind(holderOK)
			m.MovImm(jit.S8, epoch)
			m.Load64(jit.S8, jit.S8, 0)
			m.Store64(jit.S8, jit.S3, offPCEpoch)
			m.B(chainOK)
		})
		m.Bind(chainDone)
		m.Load8(jit.S8, jit.S3, offPCHolder)
		m.BranchNonZero(jit.S8, chainOK)
		m.BranchNonZero(jit.S6, miss)
		m.Bind(chainOK)
	}
	// Room: len < cap of props.vals; then vals = append(vals, value).
	m.Load64(jit.S6, jit.S0, offProps+16)
	m.Load64(jit.S7, jit.S0, offProps+24)
	m.Branch64(jit.UGE, jit.S6, jit.S7, grow)
	m.Load64(jit.S8, jit.S0, offProps+8)
	m.AddShift64(jit.S8, jit.S8, jit.S6, 4)
	m.Store64(jit.S1, jit.S8, 0)
	m.Store64(jit.S2, jit.S8, 8)
	m.AddImm64(jit.S6, jit.S6, 1)
	m.Store64(jit.S6, jit.S0, offProps+16)
	m.Store64(jit.S5, jit.S0, offProps)
	m.B(done)
}

// ordinaryFor checks that object S0's internal methods are the ordinary
// ones for the named property k (icOrdinary). Clobbers S4, S5.
func (c *jitCompiler) ordinaryFor(k PropertyKey, exit *jit.Label) {
	m := &c.m
	ok := jit.NewLabel()
	m.Load64(jit.S4, jit.S0, offExotic)
	m.BranchZero(jit.S4, ok)
	m.MovImm(jit.S5, uint64(immutablePrototypeItab))
	m.Branch64(jit.EQ, jit.S4, jit.S5, ok)
	if k.name != lengthIdent {
		m.MovImm(jit.S5, uint64(arrayObjectItab))
		m.Branch64(jit.EQ, jit.S4, jit.S5, ok)
		m.MovImm(jit.S5, uint64(stringObjectItab))
		m.Branch64(jit.EQ, jit.S4, jit.S5, ok)
	}
	m.B(exit)
	m.Bind(ok)
}

// loadObject loads object register r into S0.
func (c *jitCompiler) loadObject(r bytecode.VirtualRegister, exit *jit.Label) bool {
	if _, ok := c.constant(r); ok {
		return false
	}
	c.m.Load64(jit.S0, rRegs, c.off(r)+8)
	c.m.Branch64Imm(jit.NE, jit.S0, uint32(tagObject), exit)
	c.m.Load64(jit.S0, rRegs, c.off(r))
	return true
}

// loadScope loads scope register r into S0.
func (c *jitCompiler) loadScope(r bytecode.VirtualRegister, exit *jit.Label) bool {
	if _, ok := c.constant(r); ok {
		return false
	}
	c.m.Load64(jit.S0, rRegs, c.off(r)+8)
	c.m.Branch64Imm(jit.NE, jit.S0, uint32(tagScope), exit)
	c.m.Load64(jit.S0, rRegs, c.off(r))
	return true
}

// slotOf leaves in S7 the address of the slot of scope S0 that scope slot
// cache c caches, exiting if S0's table is not the cached one.
func (c *jitCompiler) slotOf(cache *scopeSlotCache, exit *jit.Label) {
	m := &c.m
	m.MovImm(jit.S3, uint64(uintptr(unsafe.Pointer(cache))))
	m.Load64(jit.S4, jit.S3, offSSTable)
	m.BranchZero(jit.S4, exit)
	m.Load64(jit.S5, jit.S0, offScopeTable)
	m.Branch64(jit.NE, jit.S4, jit.S5, exit)
	m.Load64(jit.S6, jit.S3, offSSOffset)
	m.Load64(jit.S7, jit.S0, offScopeSlots)
	m.AddShift64(jit.S7, jit.S7, jit.S6, 4)
}

// closureCacheSlot emits, for a scope access of global resolve type whose
// slot cache holds a variable of an enclosing function or block (which
// the bytecompiler cannot see; JSC links these to ClosureVar), the cache
// as constants: if scope S0 has the cached symbol table, S7 is left with
// the slot's address, else it jumps to miss. It reports whether it
// emitted anything.
func (c *jitCompiler) closureCacheSlot(cache *scopeSlotCache, miss *jit.Label) bool {
	if cache.table == nil || cache.offset < 0 {
		return false
	}
	if lex := c.cb.realm.globalLexical; lex != nil && cache.table == lex.table {
		return false // global let and const (globalLexicalSlot)
	}
	m := &c.m
	c.keepCells = append(c.keepCells, cache.table)
	m.Load64(jit.S5, jit.S0, offScopeTable)
	m.MovImm(jit.S4, uint64(uintptr(unsafe.Pointer(cache.table))))
	m.Branch64(jit.NE, jit.S4, jit.S5, miss)
	m.Load64(jit.S7, jit.S0, offScopeSlots)
	m.AddImm64(jit.S7, jit.S7, uint32(cache.offset)*16)
	return true
}

// closureSlot leaves in S7 the address of slot offset of scope S0.
func (c *jitCompiler) closureSlot(offset uint32) {
	c.m.Load64(jit.S7, jit.S0, offScopeSlots)
	c.m.MovImm(jit.S6, uint64(offset))
	c.m.AddShift64(jit.S7, jit.S7, jit.S6, 4)
}

// scopeInstruction compiles get_by_id, put_by_id and the scope accesses
// (the instruction in at pc).
func (c *jitCompiler) scopeInstruction(pc int, op bytecode.OpcodeID, in []uint32, exit *jit.Label) bool {
	m := &c.m
	cb := c.cb
	switch op {
	case bytecode.OpGetByIdID:
		b := bytecode.DecodeGetById(in)
		k := cb.keys[b.Property]
		if _, ok := c.constant(b.Base); ok || k.idx != 0 {
			return false
		}
		// S0 = the object whose shape the cache checks: the base, or for a
		// string or a number its realm's prototype (vm.primitiveBase, in
		// cold code so that the path of objects is as before).
		primitive, cached := jit.NewLabel(), jit.NewLabel()
		m.Load64(jit.S0, rRegs, c.off(b.Base)+8)
		m.Branch64Imm(jit.NE, jit.S0, uint32(tagObject), primitive)
		m.Load64(jit.S0, rRegs, c.off(b.Base))
		c.ordinaryFor(k, exit)
		m.Bind(cached)
		c.cold = append(c.cold, func() {
			m.Bind(primitive)
			number := jit.NewLabel()
			m.Branch64Imm(jit.UGT, jit.S0, maxTag, number)
			if k.name != lengthIdent {
				m.Branch64Imm(jit.NE, jit.S0, uint32(tagString), exit)
				m.MovImm(jit.S0, uint64(uintptr(unsafe.Pointer(cb.realm.StringPrototype))))
				m.B(cached)
			} else {
				m.B(exit)
			}
			m.Bind(number)
			m.MovImm(jit.S0, uint64(uintptr(unsafe.Pointer(cb.realm.NumberPrototype))))
			m.B(cached)
		})
		if jit.NativeCalls && c.inline == nil && c.opt == nil {
			// (In an optimized loop a getter goes to the baseline code:
			// the call would take the registers the loop keeps.)
			c.getter = &getterSite{pc: pc, base: b.Base}
		}
		c.cachedGetMega(&cb.getByIdCaches[b.MetadataID], &k, b.Dst, true, exit)
		c.getter = nil
		return true
	case bytecode.OpPutByIdID:
		b := bytecode.DecodePutById(in)
		k := cb.keys[b.Property]
		if k.idx != 0 || !c.loadObject(b.Base, exit) {
			return false
		}
		c.ordinaryFor(k, exit)
		c.loadValue(jit.S1, jit.S2, b.Value)
		var mega *PropertyKey
		if !b.Flags.IsDirect() && k.sym == nil {
			mega = &k
		}
		c.cachedPut(&cb.putByIdCaches[b.MetadataID], b.Flags.IsDirect(), mega, exit)
		return true
	case bytecode.OpResolveScopeID:
		b := bytecode.DecodeResolveScope(in)
		c.fusedGlobalGet(pc, b)
		if !c.loadScope(b.Scope, exit) {
			return false
		}
		switch {
		case b.ResolveType == bytecode.ClosureVar || b.ResolveType == bytecode.ResolvedClosureVar:
			for i := uint32(0); i < b.LocalScopeDepth; i++ {
				m.Load64(jit.S0, jit.S0, offScopeParent)
			}
		case isGlobalResolveType(b.ResolveType):
			c.resolveGlobal(&cb.resolveScopeCaches[b.MetadataID], exit)
		default:
			// Dynamic (with, sloppy eval) and module scopes: the slow path,
			// as JSC's baseline calls operationResolveScope; returning
			// false would leave the whole loop or function to the
			// interpreter.
			m.B(exit)
			return true
		}
		m.MovImm(jit.S1, uint64(tagScope))
		c.storeValue(b.Dst, jit.S0, jit.S1)
		return true
	case bytecode.OpGetFromScopeID:
		b := bytecode.DecodeGetFromScope(in)
		var fastDone *jit.Label
		if isGlobalResolveType(b.GetPutInfo.ResolveType()) {
			fastDone = c.globalGet(b.Scope, &cb.getFromScopeCaches[b.MetadataID], b.Dst)
			if fastDone == nil {
				fastDone = c.globalLexicalSlot(b.Scope, &cb.getScopeSlotCaches[b.MetadataID], b.Dst, false)
			}
		}
		if !c.loadScope(b.Scope, exit) {
			return false
		}
		switch t := b.GetPutInfo.ResolveType(); {
		case t == bytecode.ClosureVar || t == bytecode.ResolvedClosureVar:
			c.closureSlot(b.Offset)
		case isGlobalResolveType(t):
			object, slot := jit.NewLabel(), jit.NewLabel()
			general, haveSlot := jit.NewLabel(), jit.NewLabel()
			if c.closureCacheSlot(&cb.getScopeSlotCaches[b.MetadataID], general) {
				m.B(haveSlot)
			}
			m.Bind(general)
			m.Load8(jit.S4, jit.S0, offScopeKind)
			m.Branch64Imm(jit.EQ, jit.S4, uint32(scopeGlobalObject), object)
			m.Branch64Imm(jit.EQ, jit.S4, uint32(scopeWith), exit)
			m.Branch64Imm(jit.UGT, jit.S4, uint32(scopeGlobalLexical), exit) // modules
			c.slotOf(&cb.getScopeSlotCaches[b.MetadataID], exit)
			m.Bind(haveSlot)
			// An empty slot is in its TDZ.
			m.Load64(jit.S4, jit.S7, 0)
			m.Load64(jit.S5, jit.S7, 8)
			m.BranchNonZero(jit.S4, slot)
			m.Branch64Imm(jit.EQ, jit.S5, uint32(tagEmpty), exit)
			m.B(slot)
			m.Bind(object)
			m.Load64(jit.S0, jit.S0, offScopeObject)
			done := jit.NewLabel()
			c.cachedGet(&cb.getFromScopeCaches[b.MetadataID], b.Dst, exit)
			m.B(done)
			m.Bind(slot)
			c.storeValue(b.Dst, jit.S4, jit.S5)
			m.Bind(done)
			if fastDone != nil {
				m.Bind(fastDone)
			}
			return true
		default:
			m.B(exit) // the slow path (resolve_scope)
			return true
		}
		m.Load64(jit.S4, jit.S7, 0)
		m.Load64(jit.S5, jit.S7, 8)
		c.storeValue(b.Dst, jit.S4, jit.S5)
		return true
	case bytecode.OpPutToScopeID:
		b := bytecode.DecodePutToScope(in)
		var fastDone *jit.Label
		if isGlobalResolveType(b.GetPutInfo.ResolveType()) && !b.GetPutInfo.InitializationMode().IsInitialization() {
			fastDone = c.globalLexicalSlot(b.Scope, &cb.putScopeSlotCaches[b.MetadataID], b.Value, true)
		}
		if !c.loadScope(b.Scope, exit) {
			return false
		}
		c.loadValue(jit.S1, jit.S2, b.Value)
		init := b.GetPutInfo.InitializationMode().IsInitialization()
		switch t := b.GetPutInfo.ResolveType(); {
		case t == bytecode.ClosureVar || t == bytecode.ResolvedClosureVar:
			c.closureSlot(b.Offset)
		case isGlobalResolveType(t):
			object, lexical, store := jit.NewLabel(), jit.NewLabel(), jit.NewLabel()
			general := jit.NewLabel()
			if c.closureCacheSlot(&cb.putScopeSlotCaches[b.MetadataID], general) {
				m.B(store)
			}
			m.Bind(general)
			m.Load8(jit.S4, jit.S0, offScopeKind)
			m.Branch64Imm(jit.EQ, jit.S4, uint32(scopeLexical), lexical)
			m.Branch64Imm(jit.EQ, jit.S4, uint32(scopeGlobalObject), object)
			m.Branch64Imm(jit.NE, jit.S4, uint32(scopeGlobalLexical), exit)
			if init {
				m.B(exit)
			}
			// Global let and class: not in the TDZ.
			c.slotOf(&cb.putScopeSlotCaches[b.MetadataID], exit)
			m.Load64(jit.S4, jit.S7, 0)
			m.Load64(jit.S5, jit.S7, 8)
			m.BranchNonZero(jit.S4, store)
			m.Branch64Imm(jit.EQ, jit.S5, uint32(tagEmpty), exit)
			m.B(store)
			m.Bind(lexical)
			c.slotOf(&cb.putScopeSlotCaches[b.MetadataID], exit)
			m.B(store)
			done := jit.NewLabel()
			// The global object, for var initializations too.
			m.Bind(object)
			m.Load64(jit.S0, jit.S0, offScopeObject)
			// Only replacing: putGlobalProperty never caches an add.
			c.cachedPut(&cb.putToScopeCaches[b.MetadataID], false, nil, exit)
			m.B(done)
			m.Bind(store)
			m.Store64(jit.S1, jit.S7, 0)
			m.Store64(jit.S2, jit.S7, 8)
			m.Bind(done)
			if fastDone != nil {
				m.Bind(fastDone)
			}
			return true
		default:
			m.B(exit) // the slow path (resolve_scope)
			return true
		}
		m.Store64(jit.S1, jit.S7, 0)
		m.Store64(jit.S2, jit.S7, 8)
		return true
	}
	return false
}

// globalGet emits, ahead of get_from_scope's code, the read of a global
// variable that the site's cache holds as an own data property of the
// global object: the scope register must hold the global object's scope
// and the object its shape (and version); the value is read through the
// object's fields at constant addresses. It returns the label after the
// instruction to bind, or nil when it emits nothing; otherwise the code
// goes on to get_from_scope's general code.
func (c *jitCompiler) globalGet(scope bytecode.VirtualRegister, cache *getByIdCache, dst bytecode.VirtualRegister) *jit.Label {
	m := &c.m
	r := c.cb.realm
	g := r.GlobalObject
	s := cache.shape
	if s == nil || g == nil || g.props.shape != s || cache.version != s.version || cache.poly != nil ||
		cache.accessor || cache.miss || cache.depth != 0 || r.globalScope == nil {
		return nil
	}
	if _, ok := c.constant(scope); ok {
		return nil
	}
	done, general := jit.NewLabel(), jit.NewLabel()
	c.keepCells = append(c.keepCells, s)
	m.Load64(jit.S0, rRegs, c.off(scope)) // a scope's pointer: only scopes hold it
	m.MovImm(jit.S4, uint64(uintptr(unsafe.Pointer(r.globalScope))))
	m.Branch64(jit.NE, jit.S0, jit.S4, general)
	m.MovImm(jit.S6, uint64(uintptr(unsafe.Pointer(&g.props))))
	m.Load64(jit.S5, jit.S6, 0) // the shape
	m.MovImm(jit.S4, uint64(uintptr(unsafe.Pointer(s))))
	m.Branch64(jit.NE, jit.S4, jit.S5, general)
	if s.dictionary {
		m.Load32(jit.S5, jit.S5, offShapeVer)
		m.Branch32Imm(jit.NE, jit.S5, s.version, general)
	}
	m.Load64(jit.S7, jit.S6, 8) // vals
	m.Load64(jit.S4, jit.S7, int(cache.index)*valueSize)
	m.Load64(jit.S5, jit.S7, int(cache.index)*valueSize+8)
	c.storeValue(dst, jit.S4, jit.S5)
	m.B(done)
	m.Bind(general)
	return done
}

// fusedGlobalGet emits, ahead of resolve_scope's code at pc, the fast
// path of it and the get_from_scope after it that reads a global object
// property through its result, as one: the resolve's checks against the
// constants its cache holds (resolveGlobal), then the read at the
// property's constant slot (globalGet) without loading and checking the
// scope register again, and on to after the get. Anything else falls
// through to both instructions' own code. (A global function's name read
// before each call took two instructions of some 16 and 12 machine
// instructions each: 10% of sha1 and md5.)
func (c *jitCompiler) fusedGlobalGet(pc int, b bytecode.OpResolveScope) {
	if c.opt != nil || c.inline != nil || !isGlobalResolveType(b.ResolveType) {
		return
	}
	ins := c.cb.u.Instructions
	next := pc + bytecode.OpResolveScopeID.Length()
	if next >= len(ins) || bytecode.OpcodeID(ins[next]) != bytecode.OpGetFromScopeID {
		return
	}
	g := bytecode.DecodeGetFromScope(ins[next:])
	after := next + bytecode.OpGetFromScopeID.Length()
	if g.Scope != b.Dst || !isGlobalResolveType(g.GetPutInfo.ResolveType()) || c.labels[after] == nil {
		return
	}
	r := c.cb.realm
	vm := r.vm
	rc := &c.cb.resolveScopeCaches[b.MetadataID]
	gc := &c.cb.getFromScopeCaches[g.MetadataID]
	obj := r.GlobalObject
	s := gc.shape
	if rc.table != nil || rc.result == nil || rc.scope == nil || rc.realm != r || rc.result != r.globalScope ||
		rc.gen != r.globalLexicalGen || rc.inj != vm.varInjection ||
		s == nil || obj == nil || obj.props.shape != s || gc.version != s.version || gc.poly != nil ||
		gc.accessor || gc.miss || gc.depth != 0 {
		return
	}
	if _, ok := c.constant(b.Scope); ok {
		return
	}
	m := &c.m
	no := jit.NewLabel()
	c.keepCells = append(c.keepCells, rc.scope, rc.result, s)
	m.Load64(jit.S0, rRegs, c.off(b.Scope)+8)
	m.Branch64Imm(jit.NE, jit.S0, uint32(tagScope), no)
	m.Load64(jit.S0, rRegs, c.off(b.Scope))
	m.MovImm(jit.S4, uint64(uintptr(unsafe.Pointer(rc.scope))))
	m.Branch64(jit.NE, jit.S0, jit.S4, no)
	m.MovImm(jit.S5, uint64(uintptr(unsafe.Pointer(&r.globalLexicalGen))))
	m.Load64(jit.S5, jit.S5, 0)
	m.MovImm(jit.S4, rc.gen)
	m.Branch64(jit.NE, jit.S4, jit.S5, no)
	m.MovImm(jit.S5, uint64(uintptr(unsafe.Pointer(&vm.varInjection))))
	m.Load64(jit.S5, jit.S5, 0)
	m.MovImm(jit.S4, rc.inj)
	m.Branch64(jit.NE, jit.S4, jit.S5, no)
	m.MovImm(jit.S6, uint64(uintptr(unsafe.Pointer(&obj.props))))
	m.Load64(jit.S5, jit.S6, 0) // the shape
	m.MovImm(jit.S4, uint64(uintptr(unsafe.Pointer(s))))
	m.Branch64(jit.NE, jit.S4, jit.S5, no)
	if s.dictionary {
		m.Load32(jit.S5, jit.S5, offShapeVer)
		m.Branch32Imm(jit.NE, jit.S5, s.version, no)
	}
	// The resolve's result (two stores are cheaper than liveness).
	if b.Dst != g.Dst {
		m.MovImm(jit.S0, uint64(uintptr(unsafe.Pointer(rc.result))))
		m.MovImm(jit.S1, uint64(tagScope))
		c.storeValue(b.Dst, jit.S0, jit.S1)
	}
	m.Load64(jit.S7, jit.S6, 8) // vals
	m.Load64(jit.S4, jit.S7, int(gc.index)*valueSize)
	m.Load64(jit.S5, jit.S7, int(gc.index)*valueSize+8)
	c.storeValue(g.Dst, jit.S4, jit.S5)
	m.B(c.labels[after])
	m.Bind(no)
}

// globalLexicalSlot emits, ahead of get_from_scope's (put: put_to_scope's)
// code, the access to a global let, const or class binding the site's
// slot cache holds: the scope register must hold the realm's global
// lexical scope; the slot is then read or written at a constant offset
// in its slots, unless it is in its TDZ. (Adding bindings appends to the
// symbol table and the slots, so offsets stay; only the slots' array may
// move, hence the load of its address rather than a constant.) reg is the
// destination (put: the value). It returns the label after the
// instruction to bind, or nil when it emits nothing.
func (c *jitCompiler) globalLexicalSlot(scope bytecode.VirtualRegister, cache *scopeSlotCache, reg bytecode.VirtualRegister, put bool) *jit.Label {
	m := &c.m
	r := c.cb.realm
	lex := r.globalLexical
	if lex == nil || cache.table == nil || cache.table != lex.table || cache.offset < 0 || cache.offset >= len(lex.slots) {
		return nil
	}
	if _, ok := c.constant(scope); ok {
		return nil
	}
	done, general := jit.NewLabel(), jit.NewLabel()
	c.keepCells = append(c.keepCells, lex)
	m.Load64(jit.S0, rRegs, c.off(scope)) // a scope's pointer: only scopes hold it
	m.MovImm(jit.S4, uint64(uintptr(unsafe.Pointer(lex))))
	m.Branch64(jit.NE, jit.S0, jit.S4, general)
	m.Load64(jit.S7, jit.S0, offScopeSlots)
	m.AddImm64(jit.S7, jit.S7, uint32(cache.offset)*16)
	// An empty slot is in its TDZ (the general code throws). A filled
	// cache should not name one (the access that would fill it throws
	// first), so this is a guard, not a path the tests reach.
	hasValue := jit.NewLabel()
	m.Load64(jit.S4, jit.S7, 0)
	m.Load64(jit.S5, jit.S7, 8)
	m.BranchNonZero(jit.S4, hasValue)
	m.Branch64Imm(jit.EQ, jit.S5, uint32(tagEmpty), general)
	m.Bind(hasValue)
	if put {
		c.loadValue(jit.S1, jit.S2, reg)
		m.Store64(jit.S1, jit.S7, 0)
		m.Store64(jit.S2, jit.S7, 8)
	} else {
		c.storeValue(reg, jit.S4, jit.S5)
	}
	m.B(done)
	m.Bind(general)
	return done
}

// resolveGlobal resolves scope S0 through resolve_scope cache c into S0
// (VM.resolveGlobalScope's cached cases). Clobbers S3-S7.
func (c *jitCompiler) resolveGlobal(cache *resolveScopeCache, exit *jit.Label) {
	m := &c.m
	r := c.cb.realm
	vm := r.vm
	generic := jit.NewLabel()
	if cache.table == nil && cache.result != nil && cache.scope != nil && cache.realm == r &&
		cache.gen == r.globalLexicalGen && cache.inj == vm.varInjection {
		// The answer the cache holds for the scope it last saw, as
		// constants: while the scope, the global lexical bindings and the
		// var injections are unchanged (JSC links resolve_scope to the
		// global object under its var injection watchpoint).
		c.keepCells = append(c.keepCells, cache.scope, cache.result)
		m.MovImm(jit.S4, uint64(uintptr(unsafe.Pointer(cache.scope))))
		m.Branch64(jit.NE, jit.S0, jit.S4, generic)
		m.MovImm(jit.S5, uint64(uintptr(unsafe.Pointer(&r.globalLexicalGen))))
		m.Load64(jit.S5, jit.S5, 0)
		m.MovImm(jit.S4, cache.gen)
		m.Branch64(jit.NE, jit.S4, jit.S5, generic)
		m.MovImm(jit.S5, uint64(uintptr(unsafe.Pointer(&vm.varInjection))))
		m.Load64(jit.S5, jit.S5, 0)
		m.MovImm(jit.S4, cache.inj)
		m.Branch64(jit.NE, jit.S4, jit.S5, generic)
		m.MovImm(jit.S0, uint64(uintptr(unsafe.Pointer(cache.result))))
		specialized := jit.NewLabel()
		m.B(specialized)
		defer m.Bind(specialized)
	}
	if cache.table != nil && vm.varInjection == 0 {
		// A function or block scope depth scopes up, as JSC's ClosureVar:
		// the depth and the table as constants, and while no eval ever
		// injected a var (so no scope has evalVars, which closureScope
		// checks), only the kinds on the way.
		c.keepCells = append(c.keepCells, cache.table)
		m.MovImm(jit.S5, uint64(uintptr(unsafe.Pointer(&vm.varInjection))))
		m.Load64(jit.S5, jit.S5, 0)
		m.BranchNonZero(jit.S5, generic)
		m.Mov(jit.S4, jit.S0)
		for i := 0; i < cache.depth; i++ {
			m.Load8(jit.S6, jit.S4, offScopeKind)
			m.BranchNonZero(jit.S6, generic) // not lexical
			m.Load64(jit.S4, jit.S4, offScopeParent)
		}
		m.Load64(jit.S6, jit.S4, offScopeTable)
		m.MovImm(jit.S5, uint64(uintptr(unsafe.Pointer(cache.table))))
		m.Branch64(jit.NE, jit.S5, jit.S6, generic)
		m.Mov(jit.S0, jit.S4)
		specialized := jit.NewLabel()
		m.B(specialized)
		defer m.Bind(specialized)
	}
	m.Bind(generic)
	m.MovImm(jit.S3, uint64(uintptr(unsafe.Pointer(cache))))
	m.Load64(jit.S4, jit.S3, offRSTable)
	global := jit.NewLabel()
	m.BranchZero(jit.S4, global)

	// A function or block scope depth scopes up (closureScope).
	m.Load64(jit.S5, jit.S3, offRSDepth)
	loop, check := jit.NewLabel(), jit.NewLabel()
	m.Bind(loop)
	m.BranchZero(jit.S5, check)
	m.Load8(jit.S6, jit.S0, offScopeKind)
	m.BranchNonZero(jit.S6, exit) // not lexical
	m.Load64(jit.S6, jit.S0, offScopeEval)
	m.BranchNonZero(jit.S6, exit)
	m.Load64(jit.S0, jit.S0, offScopeParent)
	m.SubImm64(jit.S5, jit.S5, 1)
	m.B(loop)
	m.Bind(check)
	m.Load8(jit.S6, jit.S0, offScopeKind)
	m.BranchNonZero(jit.S6, exit)
	m.Load64(jit.S6, jit.S0, offScopeTable)
	m.Branch64(jit.NE, jit.S6, jit.S4, exit)
	done := jit.NewLabel()
	m.B(done)

	// The realm's global scope, while no script added global lexical
	// bindings and nothing on the way can inject vars (noVarInjection).
	m.Bind(global)
	m.Load64(jit.S4, jit.S3, offRSRealm)
	m.MovImm(jit.S5, uint64(uintptr(unsafe.Pointer(r))))
	m.Branch64(jit.NE, jit.S4, jit.S5, exit)
	m.Load64(jit.S4, jit.S3, offRSGen)
	m.MovImm(jit.S5, uint64(uintptr(unsafe.Pointer(&r.globalLexicalGen))))
	m.Load64(jit.S5, jit.S5, 0)
	m.Branch64(jit.NE, jit.S4, jit.S5, exit)
	walk, found := jit.NewLabel(), jit.NewLabel()
	// The scope the cache last saw, while no eval injected vars: the
	// answer without walking the chain.
	m.Load64(jit.S4, jit.S3, offRSScope)
	m.Branch64(jit.NE, jit.S4, jit.S0, walk)
	m.Load64(jit.S4, jit.S3, offRSInj)
	m.MovImm(jit.S5, uint64(uintptr(unsafe.Pointer(&vm.varInjection))))
	m.Load64(jit.S5, jit.S5, 0)
	m.Branch64(jit.EQ, jit.S4, jit.S5, found)
	m.Bind(walk)
	m.BranchZero(jit.S0, found)
	m.Load8(jit.S6, jit.S0, offScopeKind)
	m.Branch64Imm(jit.EQ, jit.S6, uint32(scopeGlobalLexical), found)
	m.Branch64Imm(jit.EQ, jit.S6, uint32(scopeGlobalObject), found)
	m.BranchNonZero(jit.S6, exit) // with and module scopes
	m.Load64(jit.S6, jit.S0, offScopeEval)
	m.BranchNonZero(jit.S6, exit)
	m.Load64(jit.S0, jit.S0, offScopeParent)
	m.B(walk)
	m.Bind(found)
	m.Load64(jit.S0, jit.S3, offRSResult)
	m.BranchZero(jit.S0, exit)
	m.Bind(done)
}
