//go:build amd64

package jit

// Masm on amd64. Registers: RRegs R15, RFrame R14, RMask R13, S0-S9 RAX,
// RBX, RDX, RSI, RDI, R8, R9, R10, R12, RBP, T0 R11. Internal: RCX (shift
// counts, wide immediates, scratch) and XMM15. Generated code is called
// with the regs in RDI and the frame in RSI (Call's a0 and a1); Call saves
// what Go needs kept.
type Masm struct {
	x X86
}

// Unbound is the number of labels that branches jump to but that were
// never bound: code with any is broken (a branch to an unbound label is
// left jumping to its own end).
func (m *Masm) Unbound() int { return unbound(m.x.waiting) }

// Reserve makes room for about n bytes of code.
func (m *Masm) Reserve(n int) { m.x.Code = make([]byte, 0, n) }

// ReserveIn is Reserve with buf's memory, if it has room for n bytes
// (Release gives it back).
func (m *Masm) ReserveIn(buf *Buffer, n int) {
	m.x.fixes, m.x.waiting = buf.take()
	if cap(buf.b) >= n {
		m.x.Code, buf.b = buf.b[:0], nil
		return
	}
	m.Reserve(n)
}

// Release keeps the code's memory in buf for the next ReserveIn, unless
// it is larger than bufferMax; the code must not be used after it.
func (m *Masm) Release(buf *Buffer) {
	if cap(m.x.Code) <= bufferMax && cap(m.x.Code) > cap(buf.b) {
		buf.b = m.x.Code[:0]
	}
	buf.keep(m.x.fixes, m.x.waiting)
	m.x.Code, m.x.fixes, m.x.waiting = nil, fixList{}, nil
}

var regsX86 = [numR]byte{RRegs: 15, RFrame: 14, RMask: 13, S0: xRAX, S1: xRBX, S2: xRDX, S3: xRSI, S4: xRDI,
	S5: 8, S6: 9, S7: 10, S8: 12, S9: xRBP, T0: 11}

const xmmScratch = 15

func q(r R) byte  { return regsX86[r] }
func xf(f F) byte { return byte(f) }

func (m *Masm) Len() int         { return len(m.x.Code) }
func (m *Masm) Bind(l *Label)    { m.x.Bind(l) }
func (m *Masm) B(l *Label)       { m.x.Jmp(l) }
func (m *Masm) OutOfRange() bool { return false } // rel32 reaches everything
func (m *Masm) SetFar(bool)      {}
func (m *Masm) Code() []byte     { return m.x.Code }

// Truncate drops the code from byte n on, and fixups there of labels.
func (m *Masm) Truncate(n int, labels ...*Label) {
	m.x.Code = m.x.Code[:n]
	for _, l := range append(labels, m.x.waiting...) {
		m.x.fixes.drop(l, n)
	}
}

// EntryStub sets up the fixed registers and jumps to l.
func (m *Masm) EntryStub(l *Label) {
	m.x.Mov64(q(RRegs), xRDI)
	m.x.Mov64(q(RFrame), xRSI)
	m.x.MovImm(q(RMask), 0xfffc000000000000)
	m.x.Jmp(l)
}

// NativeCalls reports whether generated code may call generated code
// (Push, Pop and CallEntry).
const NativeCalls = true

func (m *Masm) Push(r R) { m.x.Push(q(r)) }
func (m *Masm) Pop(r R)  { m.x.Pop(q(r)) }

// CallEntry calls the entry stub at target with regs and frame (an entry
// stub's RDI and RSI). The callee returns its exit code in S0 (RAX) and
// clobbers every register but RMask; RRegs and RFrame are the callee's.
func (m *Masm) CallEntry(target, regs, frame R) {
	x := &m.x
	x.Mov64(xRCX, q(target))
	// Through the stack: regs and frame may be in either register.
	x.Push(q(regs))
	x.Push(q(frame))
	x.Pop(xRSI)
	x.Pop(xRDI)
	x.CallReg(xRCX)
}

// Return returns v to Call.
func (m *Masm) Return(v uint64) {
	m.x.MovImm(xRAX, v)
	m.x.Ret()
}

// CallThunk calls the thunk at target (shared generated code, JSC's
// thunks), which returns with ThunkReturn; registers are as the thunk
// leaves them.
func (m *Masm) CallThunk(target R) { m.x.CallReg(q(target)) }

// ThunkReturn returns from a thunk to its CallThunk.
func (m *Masm) ThunkReturn() { m.x.Ret() }

func (m *Masm) MovImm(d R, v uint64) { m.x.MovImm(q(d), v) }

func (m *Masm) Mov(d, s R) {
	if d != s {
		m.x.Mov64(q(d), q(s))
	}
}

func (m *Masm) Load64(d, base R, off int)   { m.x.Load64(q(d), xbase(q(base), int32(off))) }
func (m *Masm) Store64(s, base R, off int)  { m.x.Store64(q(s), xbase(q(base), int32(off))) }
func (m *Masm) StoreZero64(base R, off int) { m.x.StoreImm64(xbase(q(base), int32(off)), 0) }

// StoreZero128 stores 16 zero bytes at base+off (one SSE store).
func (m *Masm) StoreZero128(base R, off int) {
	m.x.Xorps(xmmScratch, xmmScratch)
	m.x.MovupsStore(xmmScratch, xbase(q(base), int32(off)))
}
func (m *Masm) Load32(d, base R, off int)  { m.x.Load32(q(d), xbase(q(base), int32(off))) }
func (m *Masm) Load8(d, base R, off int)   { m.x.LoadU8(q(d), xbase(q(base), int32(off))) }
func (m *Masm) Load16(d, base R, off int)  { m.x.LoadU16(q(d), xbase(q(base), int32(off))) }
func (m *Masm) Store32(s, base R, off int) { m.x.Store32(q(s), xbase(q(base), int32(off))) }

func (m *Masm) Add64(d, a, b R) { m.x.Lea(q(d), xindex(q(a), q(b), 0)) }

// AddShift64 is d = a + b<<shift.
func (m *Masm) AddShift64(d, a, b R, shift uint32) {
	if shift <= 3 {
		m.x.Lea(q(d), xindex(q(a), q(b), byte(shift)))
		return
	}
	m.x.Mov64(xRCX, q(b))
	m.x.ShlImm64(xRCX, byte(shift))
	m.x.Lea(q(d), xindex(q(a), xRCX, 0))
}

func (m *Masm) AddImm64(d, a R, imm uint32) { m.x.Lea(q(d), xbase(q(a), int32(imm))) }
func (m *Masm) SubImm64(d, a R, imm uint32) { m.x.Lea(q(d), xbase(q(a), -int32(imm))) }

// binary emits d = a op b for a two-operand instruction emit(dst, src),
// copying with mov and keeping b in scratch when d is b.
func (m *Masm) binary(d, a, b, scratch byte, commutative bool, emit func(dst, src byte), mov func(dst, src byte)) {
	switch {
	case d == a:
		emit(d, b)
	case d == b && commutative:
		emit(d, a)
	case d == b:
		mov(scratch, b)
		mov(d, a)
		emit(d, scratch)
	default:
		mov(d, a)
		emit(d, b)
	}
}

func (m *Masm) Xor64(d, a, b R) {
	m.binary(q(d), q(a), q(b), xRCX, true, func(x, y byte) { m.x.Alu(0x31, true, x, y) }, m.x.Mov64)
}

func (m *Masm) Alu32(op Op32, d, a, b R) {
	x, y, z := q(d), q(a), q(b)
	alu := func(opc byte, commutative bool) {
		m.binary(x, y, z, xRCX, commutative, func(dst, src byte) { m.x.Alu(opc, false, dst, src) }, m.x.Mov32)
	}
	shift := func(ext byte) {
		m.x.Mov32(xRCX, z)
		if x != y {
			m.x.Mov32(x, y)
		}
		m.x.ShiftCL32(ext, x)
	}
	switch op {
	case Add32:
		alu(0x01, true)
	case Sub32:
		alu(0x29, false)
	case Mul32:
		m.binary(x, y, z, xRCX, true, m.x.Imul32, m.x.Mov32)
	case And32:
		alu(0x21, true)
	case Or32:
		alu(0x09, true)
	case Xor32:
		alu(0x31, true)
	case Shl32:
		shift(4)
	case Shr32:
		shift(5)
	case Sar32:
		shift(7)
	}
}

func (m *Masm) Not32(d, a R) {
	if d != a {
		m.x.Mov32(q(d), q(a))
	}
	m.x.Not32(q(d))
}

func (m *Masm) ZeroExt32(d, a R) { m.x.Mov32(q(d), q(a)) }

// Rem32 is d = a % b (truncating) for b > 0. IDIV needs RAX and RDX (S0
// and S2), which are kept on the stack meanwhile.
func (m *Masm) Rem32(d, a, b R) {
	x := &m.x
	x.Push(xRAX)
	x.Push(xRDX)
	x.Mov32(xRCX, q(b))
	if q(a) != xRAX {
		x.Mov32(xRAX, q(a))
	}
	x.Cdq()
	x.Idiv32(xRCX)
	x.Mov32(xRCX, xRDX)
	x.Pop(xRDX)
	x.Pop(xRAX)
	x.Mov32(q(d), xRCX)
}

var xccs = [...]byte{EQ: xccE, NE: xccNE, ULT: xccB, ULE: xccBE, UGT: xccA, UGE: xccAE, SLT: xccL, SLE: xccLE,
	SGT: xccG, SGE: xccGE}

func (m *Masm) Branch64(c Cond, a, b R, l *Label) {
	m.x.Alu(0x39, true, q(a), q(b))
	m.x.Jcc(xccs[c], l)
}

func (m *Masm) Branch64Imm(c Cond, a R, imm uint32, l *Label) {
	m.x.AluImm(7, true, q(a), imm)
	m.x.Jcc(xccs[c], l)
}

func (m *Masm) Branch32(c Cond, a, b R, l *Label) {
	m.x.Alu(0x39, false, q(a), q(b))
	m.x.Jcc(xccs[c], l)
}

func (m *Masm) Branch32Imm(c Cond, a R, imm uint32, l *Label) {
	m.x.AluImm(7, false, q(a), imm)
	m.x.Jcc(xccs[c], l)
}

func (m *Masm) BranchZero(a R, l *Label) {
	m.x.Test64(q(a), q(a))
	m.x.Jcc(xccE, l)
}

func (m *Masm) BranchNonZero(a R, l *Label) {
	m.x.Test64(q(a), q(a))
	m.x.Jcc(xccNE, l)
}

// SetCond32 sets d to 1 if the 32-bit comparison of a and b holds, else 0.
func (m *Masm) SetCond32(c Cond, d, a, b R) {
	m.x.Alu(0x39, false, q(a), q(b))
	m.x.Setcc(xccs[c], q(d))
	m.x.MovZXB(q(d), q(d))
}

func (m *Masm) MovToF(d F, s R)   { m.x.MovqToX(xf(d), q(s)) }
func (m *Masm) MovFromF(d R, s F) { m.x.MovqFromX(q(d), xf(s)) }

var sdOps = [...]byte{FAdd: 0x58, FSub: 0x5C, FMul: 0x59, FDiv: 0x5E}

func (m *Masm) FArith(op FOp, d, a, b F) {
	commutative := op == FAdd || op == FMul
	m.binary(xf(d), xf(a), xf(b), xmmScratch, commutative, func(x, y byte) { m.x.Sd(sdOps[op], x, y) }, m.movapd)
}

// JumpReg jumps to the address in r (jmp r64).
func (m *Masm) JumpReg(r R) { m.x.op(0, false, []byte{0xFF}, 4, xreg(q(r)), false) }

// LabelOffset is the byte offset of bound label l in the code.
func (m *Masm) LabelOffset(l *Label) int {
	if l.pos < 0 {
		panic("jit: LabelOffset of an unbound label")
	}
	return l.pos
}

// FMov copies a to d.
func (m *Masm) FMov(d, a F) { m.movapd(xf(d), xf(a)) }

func (m *Masm) movapd(d, s byte) {
	if d != s {
		m.x.Movapd(d, s)
	}
}

func (m *Masm) FSqrt(d, a F) { m.x.Sd(0x51, xf(d), xf(a)) }

// FAbs clears the sign bit (it uses RCX).
func (m *Masm) FAbs(d, a F) {
	if d != a {
		m.x.Movapd(xf(d), xf(a))
	}
	m.x.MovImm(xRCX, 1<<63-1)
	m.x.MovqToX(xmmScratch, xRCX)
	m.x.Andpd(xf(d), xmmScratch)
}

// TruncS64 truncates s to an int64 in d; values out of range give
// math.MinInt64 (CVTTSD2SI's integer indefinite) on amd64 and saturate on
// arm64, so callers check the range first.
func (m *Masm) TruncS64(d R, s F) { m.x.Cvttsd2si64(q(d), xf(s)) }

func (m *Masm) FNeg(d, a F) {
	if d != a {
		m.x.Movapd(xf(d), xf(a))
	}
	m.x.MovImm(xRCX, 1<<63)
	m.x.MovqToX(xmmScratch, xRCX)
	m.x.Xorpd(xf(d), xmmScratch)
}

func (m *Masm) CvtS32(d F, s R) {
	m.x.Xorps(xf(d), xf(d))
	m.x.Cvtsi2sd32(xf(d), q(s))
}

func (m *Masm) CvtU32(d F, s R) {
	m.x.Mov32(xRCX, q(s)) // zero-extended to 64 bits
	m.x.Xorps(xf(d), xf(d))
	m.x.Cvtsi2sd64(xf(d), xRCX)
}

func (m *Masm) CvtS64(d F, s R) {
	m.x.Xorps(xf(d), xf(d))
	m.x.Cvtsi2sd64(xf(d), q(s))
}

func (m *Masm) CvtF32ToF64(d, s F) { m.x.Cvtss2sd(xf(d), xf(s)) }
func (m *Masm) CvtF64ToF32(d, s F) { m.x.Cvtsd2ss(xf(d), xf(s)) }

// TruncInt64 truncates s to d, exactly for |s| < 2^63; NaN and larger values
// (which CVTTSD2SI turns into 0x8000000000000000) branch to fail.
func (m *Masm) TruncInt64(d R, s F, fail *Label) {
	m.x.Cvttsd2si64(q(d), xf(s))
	m.x.MovImm(xRCX, 1<<63)
	m.x.Alu(0x39, true, q(d), xRCX)
	m.x.Jcc(xccE, fail)
}

// TruncExactInt32 sets d to s if s is an int32 exactly (-0 counts as 0) and
// branches to fail otherwise. It uses F7.
func (m *Masm) TruncExactInt32(d R, s F, fail *Label) {
	m.x.Cvttsd2si32(q(d), xf(s))
	m.x.Xorps(7, 7)
	m.x.Cvtsi2sd32(7, q(d))
	m.x.Ucomisd(xf(s), 7)
	m.x.Jcc(xccNE, fail)
	m.x.Jcc(xccP, fail)
}

// fcompare compares a and b with UCOMISD so that one flag test answers c
// (unordered sets ZF, PF and CF), returning that test; FEQ and FNE need PF
// too (eq reports which).
func (m *Masm) fcompare(c FCond, a, b F) (cc byte, eq bool) {
	switch c {
	case FLT:
		m.x.Ucomisd(xf(b), xf(a))
		return xccA, false
	case FLE:
		m.x.Ucomisd(xf(b), xf(a))
		return xccAE, false
	case FGT:
		m.x.Ucomisd(xf(a), xf(b))
		return xccA, false
	case FGE:
		m.x.Ucomisd(xf(a), xf(b))
		return xccAE, false
	case FNLT:
		m.x.Ucomisd(xf(b), xf(a))
		return xccBE, false
	case FNLE:
		m.x.Ucomisd(xf(b), xf(a))
		return xccB, false
	case FNGT:
		m.x.Ucomisd(xf(a), xf(b))
		return xccBE, false
	case FNGE:
		m.x.Ucomisd(xf(a), xf(b))
		return xccB, false
	case FEQ:
		m.x.Ucomisd(xf(a), xf(b))
		return xccE, true
	default: // FNE
		m.x.Ucomisd(xf(a), xf(b))
		return xccNE, true
	}
}

func (m *Masm) FBranch(c FCond, a, b F, l *Label) {
	cc, eq := m.fcompare(c, a, b)
	switch {
	case !eq:
		m.x.Jcc(cc, l)
	case c == FEQ:
		skip := NewLabel()
		m.x.Jcc(xccP, skip)
		m.x.Jcc(xccE, l)
		m.x.Bind(skip)
	default:
		m.x.Jcc(xccP, l)
		m.x.Jcc(xccNE, l)
	}
}

func (m *Masm) FSetBool(c FCond, d R, a, b F) {
	cc, eq := m.fcompare(c, a, b)
	m.x.Setcc(cc, q(d))
	if eq {
		if c == FEQ {
			m.x.Setcc(xccNP, xRCX)
			m.x.Alu(0x21, false, q(d), xRCX) // and: ordered and equal
		} else {
			m.x.Setcc(xccP, xRCX)
			m.x.Alu(0x09, false, q(d), xRCX) // or: unordered or not equal
		}
	}
	m.x.MovZXB(q(d), q(d))
}

// FBranchZeroOrNaN branches to l if a is ±0 or NaN (ZF is set for both).
func (m *Masm) FBranchZeroOrNaN(a F, l *Label) {
	m.x.Xorps(xmmScratch, xmmScratch)
	m.x.Ucomisd(xf(a), xmmScratch)
	m.x.Jcc(xccE, l)
}

// FBranchNaN branches to l if a is NaN.
func (m *Masm) FBranchNaN(a F, l *Label) {
	m.x.Ucomisd(xf(a), xf(a))
	m.x.Jcc(xccP, l)
}

var elemScale = [...]byte{ElemU8: 0, ElemS8: 0, ElemU16: 1, ElemS16: 1, ElemU32: 2, ElemS32: 2}

// LoadIdx loads element idx of type e at base into d (32 bits, zero- or
// sign-extended as e says).
func (m *Masm) LoadIdx(e Elem, d, base, idx R) {
	mem := xindex(q(base), q(idx), elemScale[e])
	switch e {
	case ElemU8:
		m.x.LoadU8(q(d), mem)
	case ElemS8:
		m.x.LoadS8(q(d), mem)
	case ElemU16:
		m.x.LoadU16(q(d), mem)
	case ElemS16:
		m.x.LoadS16(q(d), mem)
	default:
		m.x.Load32(q(d), mem)
	}
}

// StoreIdx stores the low bits of s as element idx of type e at base.
func (m *Masm) StoreIdx(e Elem, s, base, idx R) {
	mem := xindex(q(base), q(idx), elemScale[e])
	switch e {
	case ElemU8, ElemS8:
		m.x.Store8(q(s), mem)
	case ElemU16, ElemS16:
		m.x.Store16(q(s), mem)
	default:
		m.x.Store32(q(s), mem)
	}
}

func (m *Masm) LoadIdxF64(d F, base, idx R)  { m.x.MovsdLoad(xf(d), xindex(q(base), q(idx), 3)) }
func (m *Masm) StoreIdxF64(s F, base, idx R) { m.x.MovsdStore(xf(s), xindex(q(base), q(idx), 3)) }

// LoadIdxF32 loads a float32 element and widens it into d.
func (m *Masm) LoadIdxF32(d F, base, idx R) {
	m.x.MovssLoad(xf(d), xindex(q(base), q(idx), 2))
	m.x.Cvtss2sd(xf(d), xf(d))
}

// StoreIdxF32 narrows s and stores it as a float32 element.
func (m *Masm) StoreIdxF32(s F, base, idx R) {
	m.x.Cvtsd2ss(xmmScratch, xf(s))
	m.x.MovssStore(xmmScratch, xindex(q(base), q(idx), 2))
}

// ReservedF reports whether f is used by the Masm itself or by Go: X15 is
// Go's zero register (ABIInternal).
func ReservedF(f F) bool { return f == 15 }
