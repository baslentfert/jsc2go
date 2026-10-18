//go:build arm64

package jit

import (
	"math"
	"unsafe"
)

// Masm on arm64. Registers: RRegs X0, RFrame X19, RMask X15, S0-S9 X2-X11,
// T0 X16. Internal: X12 and X17 scratch, X13 and X14 the saturated results
// of FCVTZS (set by EntryStub), D15 scratch. Generated code is called with
// the regs in X0 and the frame in X1 (Call's a0 and a1).
type Masm struct {
	a Asm
}

// Unbound is the number of labels that branches jump to but that were
// never bound: code with any is broken.
func (m *Masm) Unbound() int { return unbound(m.a.waiting) }

// Reserve makes room for about n bytes of code.
func (m *Masm) Reserve(n int) { m.a.Code = make([]uint32, 0, n/4) }

// ReserveIn is Reserve with buf's memory, if it has room for n bytes
// (Release gives it back).
func (m *Masm) ReserveIn(buf *Buffer, n int) {
	m.a.fixes, m.a.waiting = buf.take()
	if 4*cap(buf.w) >= n {
		m.a.Code, buf.w = buf.w[:0], nil
		return
	}
	m.Reserve(n)
}

// Release keeps the code's memory in buf for the next ReserveIn, unless
// it is larger than bufferMax; the code must not be used after it.
func (m *Masm) Release(buf *Buffer) {
	if 4*cap(m.a.Code) <= bufferMax && cap(m.a.Code) > cap(buf.w) {
		buf.w = m.a.Code[:0]
	}
	buf.keep(m.a.fixes, m.a.waiting)
	m.a.Code, m.a.fixes, m.a.waiting = nil, fixList{}, nil
}

var regs64 = [numR]Reg{RRegs: 0, RFrame: 19, RMask: 15, S0: 2, S1: 3, S2: 4, S3: 5, S4: 6, S5: 7, S6: 8,
	S7: 9, S8: 10, S9: 11, T0: 16}

const (
	xScratch  Reg  = 12
	xScratch2 Reg  = 17
	xMin64    Reg  = 13
	xMax64    Reg  = 14
	dScratch  FReg = 15
)

func g(r R) Reg   { return regs64[r] }
func fr(f F) FReg { return FReg(f) }

func (m *Masm) Len() int                         { return 4 * len(m.a.Code) }
func (m *Masm) Bind(l *Label)                    { m.a.Bind(l) }
func (m *Masm) B(l *Label)                       { m.a.B(l) }
func (m *Masm) OutOfRange() bool                 { return m.a.OutOfRange }
func (m *Masm) SetFar(far bool)                  { m.a.Far = far }
func (m *Masm) Truncate(n int, labels ...*Label) { m.a.Truncate(n/4, labels...) }

// Code returns the machine code: a view of its words (arm64 is little
// endian), valid until the Masm changes.
func (m *Masm) Code() []byte {
	return unsafe.Slice((*byte)(unsafe.Pointer(unsafe.SliceData(m.a.Code))), 4*len(m.a.Code))
}

// EntryStub sets up the fixed registers and jumps to l.
func (m *Masm) EntryStub(l *Label) {
	a := &m.a
	a.MOV(g(RFrame), 1)
	a.MOVImm(g(RMask), 0xfffc000000000000)
	a.MOVImm(xMax64, math.MaxInt64)
	a.MOVImm(xMin64, 1<<63)
	a.B(l)
}

// NativeCalls reports whether generated code may call generated code
// (Push, Pop and CallEntry).
const NativeCalls = true

// Push and Pop take 16 bytes of stack each (SP stays 16-byte aligned).
func (m *Masm) Push(r R) { m.a.PushPair(g(r)) }
func (m *Masm) Pop(r R)  { m.a.PopPair(g(r)) }

// CallEntry calls the entry stub at target with regs and frame (an entry
// stub's X0 and X1). The callee returns its exit code in S0 and clobbers
// every register but RMask; RRegs and RFrame are the callee's. LR is
// saved on the stack around the call: the caller's own return to Call
// needs it.
func (m *Masm) CallEntry(target, regs, frame R) {
	a := &m.a
	// Through scratch registers: target, regs and frame may be X0 or X1.
	a.MOV(xScratch2, g(target))
	a.MOV(xScratch, g(regs))
	a.MOV(1, g(frame))
	a.MOV(0, xScratch)
	a.PushPair(LR)
	a.BLR(xScratch2)
	a.PopPair(LR)
	a.MOV(g(S0), 0)
}

// Return returns v to Call.
func (m *Masm) Return(v uint64) {
	m.a.MOVImm(0, v)
	m.a.RET()
}

// CallThunk calls the thunk at target (shared generated code, JSC's
// thunks), which returns with ThunkReturn; registers are as the thunk
// leaves them. LR is saved around the call, as in CallEntry.
func (m *Masm) CallThunk(target R) {
	m.a.MOV(xScratch2, g(target))
	m.a.PushPair(LR)
	m.a.BLR(xScratch2)
	m.a.PopPair(LR)
}

// ThunkReturn returns from a thunk to its CallThunk.
func (m *Masm) ThunkReturn() { m.a.RET() }

func (m *Masm) MovImm(d R, v uint64) { m.a.MOVImm(g(d), v) }
func (m *Masm) Mov(d, s R)           { m.a.MOV(g(d), g(s)) }

// address returns a register and offset for base+off that the scaled
// immediate forms reach.
func (m *Masm) address(base R, off, size int) (Reg, int) {
	if FitsLoadOffset(off, size) {
		return g(base), off
	}
	m.a.MOVImm(xScratch, uint64(off))
	m.a.ADD(xScratch, g(base), xScratch)
	return xScratch, 0
}

func (m *Masm) Load64(d, base R, off int) {
	b, o := m.address(base, off, 8)
	m.a.LDR(g(d), b, o)
}

func (m *Masm) Store64(s, base R, off int) {
	b, o := m.address(base, off, 8)
	m.a.STR(g(s), b, o)
}

func (m *Masm) StoreZero64(base R, off int) {
	b, o := m.address(base, off, 8)
	m.a.STR(ZR, b, o)
}

// StoreZero128 stores 16 zero bytes at base+off (STP of XZR).
func (m *Masm) StoreZero128(base R, off int) {
	if off%8 != 0 || off < -512 || off > 504 {
		m.StoreZero64(base, off)
		m.StoreZero64(base, off+8)
		return
	}
	m.a.STP(ZR, ZR, g(base), off)
}

func (m *Masm) Load32(d, base R, off int) {
	b, o := m.address(base, off, 4)
	m.a.LDRW(g(d), b, o)
}

func (m *Masm) Load8(d, base R, off int) {
	b, o := m.address(base, off, 1)
	m.a.LDRB(g(d), b, o)
}

func (m *Masm) Load16(d, base R, off int) {
	b, o := m.address(base, off, 2)
	m.a.LDRH(g(d), b, o)
}

func (m *Masm) Store32(s, base R, off int) {
	b, o := m.address(base, off, 4)
	m.a.STRW(g(s), b, o)
}

func (m *Masm) Add64(d, a, b R)                    { m.a.ADD(g(d), g(a), g(b)) }
func (m *Masm) AddShift64(d, a, b R, shift uint32) { m.a.ADDShift(g(d), g(a), g(b), shift) }
func (m *Masm) Xor64(d, a, b R)                    { m.a.EOR(g(d), g(a), g(b)) }

func (m *Masm) AddImm64(d, a R, imm uint32) {
	if imm < 4096 {
		m.a.ADDImm(g(d), g(a), imm)
		return
	}
	m.a.MOVImm(xScratch, uint64(imm))
	m.a.ADD(g(d), g(a), xScratch)
}

func (m *Masm) SubImm64(d, a R, imm uint32) {
	if imm < 4096 {
		m.a.SUBImm(g(d), g(a), imm)
		return
	}
	m.a.MOVImm(xScratch, uint64(imm))
	m.a.SUB(g(d), g(a), xScratch)
}

func (m *Masm) Alu32(op Op32, d, a, b R) {
	x, y, z := g(d), g(a), g(b)
	switch op {
	case Add32:
		m.a.ADDW(x, y, z)
	case Sub32:
		m.a.SUBW(x, y, z)
	case Mul32:
		m.a.MULW(x, y, z)
	case And32:
		m.a.ANDW(x, y, z)
	case Or32:
		m.a.ORRW(x, y, z)
	case Xor32:
		m.a.EORW(x, y, z)
	case Shl32:
		m.a.LSLW(x, y, z)
	case Shr32:
		m.a.LSRW(x, y, z)
	case Sar32:
		m.a.ASRW(x, y, z)
	}
}

func (m *Masm) Not32(d, a R)     { m.a.MVNW(g(d), g(a)) }
func (m *Masm) ZeroExt32(d, a R) { m.a.UXTW(g(d), g(a)) }

// Rem32 is d = a % b (truncating) for b > 0.
func (m *Masm) Rem32(d, a, b R) {
	m.a.SDIVW(xScratch, g(a), g(b))
	m.a.MSUBW(g(d), xScratch, g(b), g(a))
}

var conds = [...]ArmCond{EQ: AEQ, NE: ANE, ULT: ALO, ULE: ALS, UGT: AHI, UGE: AHS, SLT: ALT, SLE: ALE, SGT: AGT, SGE: AGE}

func armCond(c Cond) ArmCond { return conds[c] }

func (m *Masm) cmpImm(a Reg, imm uint32, w bool) {
	switch {
	case imm < 4096 && w:
		m.a.CMPWImm(a, imm)
	case imm < 4096:
		m.a.CMPImm(a, imm)
	default:
		m.a.MOVImm(xScratch, uint64(imm))
		if w {
			m.a.CMPW(a, xScratch)
		} else {
			m.a.CMP(a, xScratch)
		}
	}
}

func (m *Masm) Branch64(c Cond, a, b R, l *Label) {
	m.a.CMP(g(a), g(b))
	m.a.BCond(armCond(c), l)
}

func (m *Masm) Branch64Imm(c Cond, a R, imm uint32, l *Label) {
	m.cmpImm(g(a), imm, false)
	m.a.BCond(armCond(c), l)
}

func (m *Masm) Branch32(c Cond, a, b R, l *Label) {
	m.a.CMPW(g(a), g(b))
	m.a.BCond(armCond(c), l)
}

func (m *Masm) Branch32Imm(c Cond, a R, imm uint32, l *Label) {
	m.cmpImm(g(a), imm, true)
	m.a.BCond(armCond(c), l)
}

func (m *Masm) BranchZero(a R, l *Label)    { m.a.CBZ(g(a), l) }
func (m *Masm) BranchNonZero(a R, l *Label) { m.a.CBNZ(g(a), l) }

// SetCond32 sets d to 1 if the 32-bit comparison of a and b holds, else 0.
func (m *Masm) SetCond32(c Cond, d, a, b R) {
	m.a.CMPW(g(a), g(b))
	m.a.CSET(g(d), armCond(c))
}

func (m *Masm) MovToF(d F, s R)   { m.a.FMOVToF(fr(d), g(s)) }
func (m *Masm) MovFromF(d R, s F) { m.a.FMOVFromF(g(d), fr(s)) }

func (m *Masm) FArith(op FOp, d, a, b F) {
	switch op {
	case FAdd:
		m.a.FADD(fr(d), fr(a), fr(b))
	case FSub:
		m.a.FSUB(fr(d), fr(a), fr(b))
	case FMul:
		m.a.FMUL(fr(d), fr(a), fr(b))
	case FDiv:
		m.a.FDIV(fr(d), fr(a), fr(b))
	}
}

func (m *Masm) FNeg(d, a F) { m.a.FNEG(fr(d), fr(a)) }

// JumpReg jumps to the address in r (BR).
func (m *Masm) JumpReg(r R) { m.a.BR(g(r)) }

// LabelOffset is the byte offset of bound label l in the code.
func (m *Masm) LabelOffset(l *Label) int {
	if l.pos < 0 {
		panic("jit: LabelOffset of an unbound label")
	}
	return 4 * l.pos
}

// FMov copies a to d.
func (m *Masm) FMov(d, a F) {
	if d != a {
		m.a.FMOV(fr(d), fr(a))
	}
}
func (m *Masm) FSqrt(d, a F)       { m.a.FSQRT(fr(d), fr(a)) }
func (m *Masm) FAbs(d, a F)        { m.a.FABS(fr(d), fr(a)) }
func (m *Masm) TruncS64(d R, s F)  { m.a.FCVTZS(g(d), fr(s)) }
func (m *Masm) CvtS32(d F, s R)    { m.a.SCVTFW(fr(d), g(s)) }
func (m *Masm) CvtU32(d F, s R)    { m.a.UCVTFW(fr(d), g(s)) }
func (m *Masm) CvtS64(d F, s R)    { m.a.SCVTF(fr(d), g(s)) }
func (m *Masm) CvtF32ToF64(d, s F) { m.a.FCVTDS(fr(d), fr(s)) }
func (m *Masm) CvtF64ToF32(d, s F) { m.a.FCVTSD(fr(d), fr(s)) }

// TruncInt64 truncates s to d, exactly for |s| < 2^63 (NaN gives 0); it
// branches to fail for larger values.
func (m *Masm) TruncInt64(d R, s F, fail *Label) {
	m.a.FCVTZS(g(d), fr(s))
	m.a.CMP(g(d), xMax64)
	m.a.BCond(AEQ, fail)
	m.a.CMP(g(d), xMin64)
	m.a.BCond(AEQ, fail)
}

// TruncExactInt32 sets d to s if s is an int32 exactly (-0 counts as 0) and
// branches to fail otherwise. It uses F7.
func (m *Masm) TruncExactInt32(d R, s F, fail *Label) {
	m.a.FCVTZSW(g(d), fr(s))
	m.a.SCVTFW(7, g(d))
	m.a.FCMP(fr(s), 7)
	m.a.BCond(ANE, fail)
}

var fconds = [...]ArmCond{FLT: AMI, FLE: ALS, FGT: AGT, FGE: AGE, FEQ: AEQ, FNE: ANE, FNLT: APL, FNLE: AHI, FNGT: ALE, FNGE: ALT}

// The arm64 conditions after FCMP a, b that give JavaScript's results
// (unordered sets C and V).
func (m *Masm) FBranch(c FCond, a, b F, l *Label) {
	m.a.FCMP(fr(a), fr(b))
	m.a.BCond(fconds[c], l)
}

func (m *Masm) FSetBool(c FCond, d R, a, b F) {
	m.a.FCMP(fr(a), fr(b))
	m.a.CSET(g(d), fconds[c])
}

// FBranchZeroOrNaN branches to l if a is ±0 or NaN.
func (m *Masm) FBranchZeroOrNaN(a F, l *Label) {
	m.a.FCMPZero(fr(a))
	m.a.BCond(AEQ, l)
	m.a.BCond(AVS, l)
}

// FBranchNaN branches to l if a is NaN.
func (m *Masm) FBranchNaN(a F, l *Label) {
	m.a.FCMP(fr(a), fr(a))
	m.a.BCond(AVS, l)
}

// LoadIdx loads element idx of type e at base into d (32 bits, zero- or
// sign-extended as e says).
func (m *Masm) LoadIdx(e Elem, d, base, idx R) {
	x, b, i := g(d), g(base), g(idx)
	switch e {
	case ElemU8:
		m.a.LDRBReg(x, b, i)
	case ElemS8:
		m.a.LDRSBReg(x, b, i)
	case ElemU16:
		m.a.LDRHReg(x, b, i)
	case ElemS16:
		m.a.LDRSHReg(x, b, i)
	default:
		m.a.LDRWReg(x, b, i)
	}
}

// StoreIdx stores the low bits of s as element idx of type e at base.
func (m *Masm) StoreIdx(e Elem, s, base, idx R) {
	x, b, i := g(s), g(base), g(idx)
	switch e {
	case ElemU8, ElemS8:
		m.a.STRBReg(x, b, i)
	case ElemU16, ElemS16:
		m.a.STRHReg(x, b, i)
	default:
		m.a.STRWReg(x, b, i)
	}
}

func (m *Masm) LoadIdxF64(d F, base, idx R)  { m.a.LDRDReg(fr(d), g(base), g(idx)) }
func (m *Masm) StoreIdxF64(s F, base, idx R) { m.a.STRDReg(fr(s), g(base), g(idx)) }

// LoadIdxF32 loads a float32 element and widens it into d.
func (m *Masm) LoadIdxF32(d F, base, idx R) {
	m.a.LDRSReg(fr(d), g(base), g(idx))
	m.a.FCVTDS(fr(d), fr(d))
}

// StoreIdxF32 narrows s and stores it as a float32 element.
func (m *Masm) StoreIdxF32(s F, base, idx R) {
	m.a.FCVTSD(dScratch, fr(s))
	m.a.STRSReg(dScratch, g(base), g(idx))
}

// ReservedF reports whether f is used by the Masm itself (dScratch), so
// that code generators must not keep values in it.
func ReservedF(f F) bool { return FReg(f) == dScratch }
