package jit

import "unsafe"

// State lets generated code that runs nested in generated code (direct
// calls) stop for Go without unwinding its callers: the code jumps back to
// CallS, which calls Suspended, which runs what the code stopped for and
// tells CallS where to continue. The callers' frames stay where they are,
// in CallS's frame on the goroutine stack; they hold no addresses of the
// stack, so Go may move the stack meanwhile.
type State struct {
	// Base is the address of the return address of the outermost code
	// (CallS sets it whenever it runs generated code).
	Base uintptr
	// SP is the stack pointer of the stopped code (at its return address
	// to its caller), as it was when it stopped: CallS keeps its distance
	// to Base when Go has moved the stack.
	SP uintptr
	// Word is the stopped code's exit word; Suspended may replace it.
	Word uintptr
	// Owner is the stopped code's own descriptor (vm's jitCode); Frame
	// and Regs are its RFrame and RRegs.
	Owner, Frame, Regs unsafe.Pointer
	// Resume is where Suspended has the code continue, with Regs and
	// Frame as an entry's arguments; 0 returns Word from the stopped code
	// to its caller instead, as before suspending.
	Resume uintptr
	// LR is the stopped code's return address (arm64, where it is in a
	// register), and Ret where CallS goes on after the code it called
	// (arm64; on amd64 the return address on the stack at Base).
	LR, Ret uintptr
}

// Suspended runs what generated code suspended for (set by package vm).
var Suspended func(st *State)

// suspended is CallS's way into Suspended.
func suspended(st *State) { Suspended(st) }

// ExitSuspended is the exit word kind (low bits) with which suspending
// code returns to CallS.
const ExitSuspended = 7

// Layout of State for generated code.
var (
	OffStateBase  = int(unsafe.Offsetof(State{}.Base))
	OffStateSP    = int(unsafe.Offsetof(State{}.SP))
	OffStateWord  = int(unsafe.Offsetof(State{}.Word))
	OffStateOwner = int(unsafe.Offsetof(State{}.Owner))
	OffStateFrame = int(unsafe.Offsetof(State{}.Frame))
	OffStateRegs  = int(unsafe.Offsetof(State{}.Regs))
	OffStateLR    = int(unsafe.Offsetof(State{}.LR))
	OffStateRet   = int(unsafe.Offsetof(State{}.Ret))
)
