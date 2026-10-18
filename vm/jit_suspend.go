//go:build ((arm64 || amd64) && (darwin || linux)) || (amd64 && windows)

package vm

import (
	"unsafe"

	"github.com/baslentfert/jsc2go/jit"
)

// Suspending generated code (JSC's calls from JIT code into C++).
//
// Generated code cannot call Go. When code that runs nested in other
// generated code (a direct call, jit_calls.go) stops for a slow path or a
// builtin, it used to return to its caller, which stopped too, up to Go,
// where finishNative made interpreter frames of the whole chain and
// finished them one by one: about 50 ns a frame. Now such code suspends
// instead: it jumps back to jit.CallS, leaving its callers' frames on the
// stack; jitSuspended makes the call or slow path and has the code go on
// right after it. Only when the code cannot go on (no entry after the
// instruction, the collector started marking, a proper tail call, or a
// call that keeps stopping: jitSuspendMax) does it return to its caller as
// before. What the Go code throws goes straight through the suspended
// code's frames: callers record the pc of their call in their frame, so
// that the outermost frame's handlers apply, and whoever catches the
// exception frees the suspended frames (jitUnwindTo).

// callJIT runs generated code at entry with a frame's registers and the
// frame, letting nested code suspend.
func (vm *VM) callJIT(entry, regs, frame uintptr) uintptr {
	if !jit.Suspends {
		ret := jit.Call(entry, regs, frame, 0, 0)
		vm.clearDirtyRegs()
		return ret
	}
	if vm.jitCalls == len(vm.jitStates) {
		vm.jitStates = append(vm.jitStates, new(jit.State))
	}
	st, saved := vm.jitStates[vm.jitCalls], vm.jitSuspend
	vm.jitCalls++
	vm.jitSuspend = st
	ret := jit.CallS(entry, regs, frame, st)
	vm.jitSuspend = saved
	if vm.jitCalls--; vm.jitCalls == 0 {
		vm.jitGraveyard = nil
	}
	// (An exception passing through leaves them to the next call or to
	// allocRegs.)
	vm.clearDirtyRegs()
	return ret
}

var offVMJitSuspend = int(unsafe.Offsetof(VM{}.jitSuspend))

// suspendThunk is the shared code exits of calls and slow paths jump to,
// with S0 = the exit word and S1 = the code's jitCode: at the outermost
// code (no direct call in progress) or outside callJIT it returns S0 to
// Go as before; else it records the code's state and returns
// jit.ExitSuspended to CallS. 0 where code cannot suspend.
var suspendThunk = func() uintptr {
	if !jit.Suspends {
		return 0
	}
	var m jit.Masm
	plain := jit.NewLabel()
	m.Load64(jit.S2, rFrame, offFrameVM)
	m.Load64(jit.S3, jit.S2, offVMJitDepth)
	m.BranchZero(jit.S3, plain)
	m.Load64(jit.S4, jit.S2, offVMJitSuspend)
	m.BranchZero(jit.S4, plain)
	m.Store64(jit.S0, jit.S4, jit.OffStateWord)
	m.Store64(jit.S1, jit.S4, jit.OffStateOwner)
	m.Store64(rFrame, jit.S4, jit.OffStateFrame)
	m.Store64(rRegs, jit.S4, jit.OffStateRegs)
	m.Suspend(jit.S4)
	m.Bind(plain)
	m.ReturnReg(jit.S0)
	code, err := jit.NewBytes(m.Code())
	if err != nil {
		return 0
	}
	suspendCode = code
	return code.Entry()
}()

var suspendCode *jit.Code

// jitSuspendMax is how many times code called directly may suspend in a
// call before its callers stop instead (from then on its stops cost what
// stops of the outermost code cost: a loop of builtin calls in a function
// called directly suspended some 13 ns a call slower than it stops as the
// outermost). Measured: 2 lost most of the parsers' gain (espree +4%
// against +20% with 4), and limits by the callers' depth traded
// benchmarks against each other.
const jitSuspendMax = 4

func init() { jit.Suspended = jitSuspended }

// jitSuspended makes the call or slow path suspended code stopped for, in
// Go, and sets where the code goes on (see the top of the file).
func jitSuspended(st *jit.State) {
	f := (*Frame)(st.Frame)
	vm := f.vm
	code := (*jitCode)(st.Owner)
	word := st.Word
	at := int(word >> jitExitBits)
	st.Resume = 0 // return word to the code's caller, unless set below
	if f.jitSuspends++; f.jitSuspends > jitSuspendMax || jit.BarrierEnabled() {
		// A call that keeps stopping (a loop of builtin calls): its code
		// goes on as the outermost from here, where stopping costs less
		// than suspending.
		return
	}
	next := vm.runSuspended(f, at, word)
	if next == at {
		return // a proper tail call: the interpreter makes it
	}
	o := code.resumes[next]
	if o == 0 || jit.BarrierEnabled() {
		// The slow path ran: the interpreter goes on after it.
		st.Word = uintptr(next)<<jitExitBits | jitExitLeave
		return
	}
	f.jitFuel = jitFuel
	st.Resume = code.code.Entry() + uintptr(o-1)
}

// runSuspended runs the instruction at at of suspended frame f in Go, with
// the suspended chain's frames set aside, and returns the pc after it.
// Without a deferred function (which cost a third of a suspension): when
// the instruction throws, whoever catches the exception puts the VM back
// (jitUnwindTo).
func (vm *VM) runSuspended(f *Frame, at int, word uintptr) int {
	// The registers above the suspended chain are dead: clear what its
	// returned calls left, so that a loop that stays in generated code
	// (its fuel and slow paths come here) keeps nothing alive for long.
	vm.clearDirtyRegs()
	if vm.jitSuspends == len(vm.jitFrameSets) {
		set := make([]*Frame, jitMaxDepth)
		for i := range set {
			set[i] = new(Frame)
		}
		vm.jitFrameSets = append(vm.jitFrameSets, set)
		vm.jitSaved = append(vm.jitSaved, jitSaved{})
	}
	sv := &vm.jitSaved[vm.jitSuspends]
	sv.frames, sv.depth, sv.current = vm.jitFrames, vm.jitDepth, vm.currentFrame
	vm.jitFrames, vm.jitDepth, vm.currentFrame = vm.jitFrameSets[vm.jitSuspends], 0, f
	vm.jitSuspends++
	f.pc = at
	var next int
	if word&jitExitMask == jitExitNative {
		next = vm.jitNativeCall(f, at)
	} else {
		next = vm.jitSlowPath(f, at)
	}
	vm.jitSuspends--
	vm.jitFrames, vm.jitDepth, vm.currentFrame = sv.frames, sv.depth, sv.current
	sv.current = nil
	return next
}

// clearDirtyRegs zeroes the registers that returned callees of generated
// code left above sp (VM.spDirty).
func (vm *VM) clearDirtyRegs() {
	if vm.spDirty > vm.sp {
		clear(vm.regStack[vm.sp:vm.spDirty])
		vm.spDirty = vm.sp
	}
}
