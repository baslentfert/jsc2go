//go:build arm64 && (darwin || linux)

#include "textflag.h"
#include "funcdata.h"

// func Call(entry uintptr, a0, a1, a2, a3 uintptr) uintptr
//
// Generated code gets the arguments in R0-R3 and returns in R0. It may use
// all general registers but RSP, R18 (the platform's), R26-R29 (Go's
// closure context, g and frame pointer) and LR, which it saves itself.
//
// Generated code calls generated code (CallEntry), which pushes LR and
// saved registers: 64 bytes a call (every push takes 16 bytes, to keep RSP
// aligned), at most 64 deep (see vm.jitMaxDepth). The frame reserves 4096
// bytes for that above the saved LR at 0(RSP): RSP moves up by 4112 for the
// call, so the pushes land in this frame instead of below the stack Go
// knows about. The function is not NOSPLIT, so Go grows the stack first if
// needed (memory passed by address must therefore be on the heap).
TEXT ·Call(SB), $4112-48
	MOVD	entry+0(FP), R8
	MOVD	a0+8(FP), R0
	MOVD	a1+16(FP), R1
	MOVD	a2+24(FP), R2
	MOVD	a3+32(FP), R3
	ADD	$4112, RSP, RSP
	CALL	(R8)
	SUB	$4112, RSP, RSP
	MOVD	R0, ret+40(FP)
	RET

// func CallS(entry, a0, a1 uintptr, st *State) uintptr
//
// Call for code that may suspend (State). As Call, the generated code runs
// with RSP at the top of this frame (st.Base), with 512 bytes more room
// than Call's 4096: a direct call takes 64 bytes (every push is 16), so
// 64 calls deep take all of 4096, and suspending at the bottom of such a
// chain pushes more (it overwrote CallS's saved LR and the argument at
// 8(RSP)); callsEnter records where
// this function goes on after it (st.Ret, the code's LR) and jumps in.
// Code that suspends records its RSP and LR, sets RSP to st.Base and
// jumps to st.Ret with ExitSuspended in R0; after suspended, CallS has
// resumeSP put RSP and LR back (keeping RSP's distance to st.Base, which
// moves with the stack) and jump to st.Resume, or return st.Word to the
// stopped code's caller. 8(RSP) is suspended's argument. Registers
// written on the way back avoid R13-R15 and R19 (the callers' code keeps
// them) and R26-R29 (Go's).
TEXT ·CallS(SB), $4624-40
	NO_LOCAL_POINTERS
	MOVD	entry+0(FP), R8
	MOVD	a0+8(FP), R0
	MOVD	a1+16(FP), R1
	MOVD	st+24(FP), R10
	ADD	$4624, RSP, RSP
	MOVD	RSP, R9
	MOVD	R9, 0(R10)
	CALL	callsEnter<>(SB)
	SUB	$4624, RSP, RSP
	AND	$7, R0, R9
	CMP	$7, R9
	BNE	done
	MOVD	st+24(FP), R10
	MOVD	R10, 8(RSP)
	CALL	·suspended(SB)
	MOVD	st+24(FP), R10
	MOVD	0(R10), R9
	MOVD	8(R10), R11
	SUB	R11, R9, R9
	ADD	$4624, RSP, R11
	MOVD	R11, 0(R10)
	SUB	R9, R11, R11
	MOVD	48(R10), R12
	MOVD	40(R10), R0
	MOVD	32(R10), R1
	MOVD	16(R10), R7
	MOVD	56(R10), R6
	JMP	·resumeSP(SB)
done:
	MOVD	R0, ret+32(FP)
	RET

// callsEnter records its return address (CallS's, after the call to it)
// in st.Ret (R10) and jumps to the generated code at R8, which returns
// there.
TEXT callsEnter<>(SB), NOSPLIT|NOFRAME, $0-0
	MOVD	LR, 64(R10)
	JMP	(R8)

// resumeSP continues suspended generated code for CallS: RSP = R11, LR =
// R6, then a jump to R12 (with R0 and R1), or with R12 = 0 a return of R7
// to LR. Writing RSP makes a function one Go cannot unwind; this one is
// never on the stack.
TEXT ·resumeSP(SB), NOSPLIT|NOFRAME, $0-0
	MOVD	R11, RSP
	MOVD	R6, LR
	CBZ	R12, ret
	JMP	(R12)
ret:
	MOVD	R7, R0
	RET
