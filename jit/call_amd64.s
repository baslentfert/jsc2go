//go:build amd64 && (darwin || linux || windows)

#include "textflag.h"
#include "funcdata.h"

// func Call(entry uintptr, a0, a1, a2, a3 uintptr) uintptr
//
// Generated code gets the arguments in DI, SI, DX and CX and returns in AX.
// It may use all general registers but SP; BP, R12-R15 and BX are saved
// here (Go keeps g in R14, and BP is the frame pointer).
//
// Generated code calls generated code (CallEntry), which pushes return
// addresses and saved registers: 32 bytes a call, at most 64 deep (see
// vm.jitMaxDepth). The frame reserves 2560 bytes for that: SP moves up by
// 2560 for the call, so the pushes land in this frame instead of below the
// stack Go knows about. The function is not NOSPLIT, so Go grows the stack
// first if needed (memory passed by address must therefore be on the heap).
TEXT ·Call(SB), $2616-48
	MOVQ	BP, 0(SP)
	MOVQ	BX, 8(SP)
	MOVQ	R12, 16(SP)
	MOVQ	R13, 24(SP)
	MOVQ	R14, 32(SP)
	MOVQ	R15, 40(SP)
	MOVQ	entry+0(FP), R11
	MOVQ	a0+8(FP), DI
	MOVQ	a1+16(FP), SI
	MOVQ	a2+24(FP), DX
	MOVQ	a3+32(FP), CX
	ADDQ	$2560, SP
	CALL	R11
	SUBQ	$2560, SP
	MOVQ	0(SP), BP
	MOVQ	8(SP), BX
	MOVQ	16(SP), R12
	MOVQ	24(SP), R13
	MOVQ	32(SP), R14
	MOVQ	40(SP), R15
	MOVQ	AX, ret+40(FP)
	RET

// func CallS(entry, a0, a1 uintptr, st *State) uintptr
//
// Call for code that may suspend (State). Frame: 0(SP) the argument of
// suspended, 8-40(SP) saved registers, 48(SP) BP-SP (Go's frame pointer,
// as an offset so that it survives a moved stack), and above that 2560
// bytes for generated code, entered with SP at 2632(SP). Its return address
// lies at 2624(SP) (st.Base). Code that suspends records its SP in st.SP,
// sets SP to st.Base and
// returns ExitSuspended here, leaving the frames of its callers in place;
// after suspended, CallS jumps to st.Resume with SP back at the stopped
// code's return address, or returns st.Word to that address (the code's
// caller sees the exit it would have seen without suspending).
TEXT ·CallS(SB), $2632-40
	NO_LOCAL_POINTERS
	MOVQ	BX, 8(SP)
	MOVQ	R12, 16(SP)
	MOVQ	R13, 24(SP)
	MOVQ	R14, 32(SP)
	MOVQ	R15, 40(SP)
	MOVQ	BP, AX
	SUBQ	SP, AX
	MOVQ	AX, 48(SP)
	MOVQ	entry+0(FP), R11
	MOVQ	a0+8(FP), DI
	MOVQ	a1+16(FP), SI
	MOVQ	st+24(FP), R10
	LEAQ	2624(SP), AX
	MOVQ	AX, 0(R10)
	ADJSP	$-2632
	CALL	R11
	ADJSP	$2632
	MOVQ	AX, CX
	ANDQ	$7, CX
	CMPQ	CX, $7
	JNE	done
	MOVQ	48(SP), BP
	ADDQ	SP, BP
	MOVQ	st+24(FP), R10
	MOVQ	R10, 0(SP)
	CALL	·suspended(SB)
	MOVQ	st+24(FP), R10
	MOVQ	0(R10), CX
	SUBQ	8(R10), CX
	LEAQ	2624(SP), AX
	MOVQ	AX, 0(R10)
	SUBQ	CX, AX
	MOVQ	48(R10), R11
	MOVQ	40(R10), DI
	MOVQ	32(R10), SI
	MOVQ	16(R10), CX
	// A function that writes SP is one Go cannot unwind (SPWRITE): leave
	// that to resumeSP, which is never on the stack. (The assembler pops
	// this frame before the jump; resumeSP sets SP anyway.)
	JMP	·resumeSP(SB)
done:
	MOVQ	8(SP), BX
	MOVQ	16(SP), R12
	MOVQ	24(SP), R13
	MOVQ	32(SP), R14
	MOVQ	40(SP), R15
	MOVQ	AX, ret+32(FP)
	RET

// resumeSP continues suspended generated code for CallS: SP = AX, then a
// jump to R11 (with DI and SI), or with R11 = 0 a return of CX to the
// address at SP.
TEXT ·resumeSP(SB), NOSPLIT|NOFRAME, $0-0
	MOVQ	AX, SP
	TESTQ	R11, R11
	JZ	ret
	JMP	R11
ret:
	MOVQ	CX, AX
	RET
