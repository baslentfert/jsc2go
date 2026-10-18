//go:build amd64 && (darwin || linux || windows)

package jit

// Suspends reports whether CallS can suspend nested generated code.
const Suspends = true

// CallS is Call for code that may suspend (see State): entry gets a0 and
// a1 as Call's arguments.
func CallS(entry, a0, a1 uintptr, st *State) uintptr

// Suspend ends the suspend thunk (package vm): it records the stack
// pointer in State st (in register st) and goes back into CallS with
// ExitSuspended, as a return would but by a jump: a return there would
// not match the CPU's prediction of returns, and every return of the
// suspended chain afterwards would miss it too. It clobbers S5-S7.
func (m *Masm) Suspend(st R) {
	m.x.Mov64(q(S5), xRSP)
	m.Store64(S5, st, OffStateSP)
	m.Load64(S6, st, OffStateBase)
	m.Load64(S7, S6, 0) // CallS's return address
	m.AddImm64(S6, S6, 8)
	m.x.Mov64(xRSP, q(S6))
	m.x.MovImm(xRAX, ExitSuspended)
	m.JumpReg(S7)
}

// ReturnReg returns r to Call, as Return returns a constant.
func (m *Masm) ReturnReg(r R) {
	if q(r) != xRAX {
		m.x.Mov64(xRAX, q(r))
	}
	m.x.Ret()
}

// resumeSP is CallS's jump back into suspended code (call_amd64.s).
func resumeSP()
