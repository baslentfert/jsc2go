//go:build arm64 && (darwin || linux)

package jit

// Suspends reports whether CallS can suspend nested generated code.
const Suspends = true

// CallS is Call for code that may suspend (see State): entry gets a0 and
// a1 as Call's arguments.
func CallS(entry, a0, a1 uintptr, st *State) uintptr

// resumeSP is CallS's jump back into suspended code (call_arm64.s).
func resumeSP()

// lrReg is the link register.
const lrReg Reg = 30

// spReg is register 31 in the instructions where it means SP (ADD
// immediate).
const spReg Reg = 31

// Suspend ends the suspend thunk (package vm): it records the stack
// pointer and the link register (the stopped code's return address) in
// State st (in register st) and goes back into CallS, at st.Ret with SP at
// st.Base, with ExitSuspended in X0.
func (m *Masm) Suspend(st R) {
	a := &m.a
	a.ADDImm(xScratch, spReg, 0)
	a.STR(xScratch, g(st), OffStateSP)
	a.STR(lrReg, g(st), OffStateLR)
	a.LDR(xScratch, g(st), OffStateBase)
	a.LDR(xScratch2, g(st), OffStateRet)
	a.ADDImm(spReg, xScratch, 0)
	a.MOVImm(0, ExitSuspended)
	a.BR(xScratch2)
}

// ReturnReg returns r to Call, as Return returns a constant (in X0).
func (m *Masm) ReturnReg(r R) {
	if g(r) != 0 {
		m.a.MOV(0, g(r))
	}
	m.a.RET()
}
