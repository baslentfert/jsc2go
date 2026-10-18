package jit

// An assembler for the AArch64 instructions the JIT uses. It only encodes
// (any platform can build code; only Supported platforms can run it).

// Reg is a general-purpose register X0-X30; ZR (31) is the zero register
// where an instruction allows it.
type Reg uint32

// FReg is a floating-point register D0-D31.
type FReg uint32

const ZR Reg = 31

// ArmCond is an arm64 condition code.
type ArmCond uint32

const (
	AEQ ArmCond = iota
	ANE
	AHS // unsigned >=
	ALO // unsigned <
	AMI
	APL
	AVS
	AVC
	AHI // unsigned >
	ALS // unsigned <=
	AGE
	ALT
	AGT
	ALE
	AAL
)

// Invert returns the opposite condition.
func (c ArmCond) Invert() ArmCond { return c ^ 1 }

// Label is a position in the code, bound once.
type Label struct {
	pos  int   // instruction index (byte offset on x86), -1 while unbound
	head int32 // its last fixup in the assembler's fixList (index+1), 0: none
}

type fixup struct {
	at   int
	kind uint8 // fixB, fixBCond, fixCB
}

const (
	fixB = iota
	fixBCond
	fixCB
)

// Asm collects instructions.
type Asm struct {
	Code []uint32
	// waiting are the labels that got a fixup while unbound (see
	// Masm.Unbound).
	waiting []*Label
	fixes   fixList
	// OutOfRange is set when a branch target is too far for its
	// instruction (±128 MB for B, ±1 MB for conditional branches): the
	// code must not be used.
	OutOfRange bool
	// Far makes conditional branches reach as far as B: each is an inverted
	// conditional branch over a B.
	Far bool
}

func (a *Asm) emit(i uint32) { a.Code = append(a.Code, i) }

// Len is the number of instructions emitted.
func (a *Asm) Len() int { return len(a.Code) }

// NewLabel returns an unbound label.
func NewLabel() *Label {
	return &Label{pos: -1}
}

// Bind binds l to the current position and patches the branches to it.
func (a *Asm) Bind(l *Label) {
	l.pos = len(a.Code)
	a.fixes.each(l, func(at int, kind uint8) { a.patch(fixup{at: at, kind: kind}, l.pos) })
	l.head = 0
}

func (a *Asm) patch(f fixup, target int) {
	d := target - f.at
	off := uint32(d)
	switch f.kind {
	case fixB:
		if d < -1<<25 || d >= 1<<25 {
			a.OutOfRange = true
		}
		a.Code[f.at] |= off & (1<<26 - 1)
	case fixBCond, fixCB:
		if d < -1<<18 || d >= 1<<18 {
			a.OutOfRange = true
		}
		a.Code[f.at] |= (off & (1<<19 - 1)) << 5
	}
}

func (a *Asm) branch(i uint32, kind uint8, l *Label) {
	f := fixup{at: len(a.Code), kind: kind}
	a.emit(i)
	if l.pos >= 0 {
		a.patch(f, l.pos)
	} else {
		if l.head == 0 {
			a.waiting = append(a.waiting, l)
		}
		a.fixes.add(l, f.at, f.kind)
	}
}

// Branches.

func (a *Asm) B(l *Label) { a.branch(0x14000000, fixB, l) }

func (a *Asm) BCond(c ArmCond, l *Label) {
	if a.Far {
		a.over(0x54000000|uint32(c.Invert()), l)
		return
	}
	a.branch(0x54000000|uint32(c), fixBCond, l)
}

func (a *Asm) CBZ(r Reg, l *Label) {
	if a.Far {
		a.over(0xb5000000|uint32(r), l) // CBNZ over B
		return
	}
	a.branch(0xb4000000|uint32(r), fixCB, l)
}

func (a *Asm) CBNZ(r Reg, l *Label) {
	if a.Far {
		a.over(0xb4000000|uint32(r), l) // CBZ over B
		return
	}
	a.branch(0xb5000000|uint32(r), fixCB, l)
}

// over emits the negated conditional branch i over a B to l (Far).
func (a *Asm) over(i uint32, l *Label) {
	a.emit(i | 2<<5) // skip the B
	a.B(l)
}
func (a *Asm) RET() { a.emit(0xd65f03c0) }

// BLR Xn: call the address in Xn (the return address goes to LR, X30).
func (a *Asm) BLR(n Reg) { a.emit(0xd63f0000 | uint32(n)<<5) }

// LR is the link register, which BL and BLR set and RET returns to.
const LR Reg = 30

// PushPair stores t at SP-16 and moves SP down by 16 (STR Xt, [SP, #-16]!):
// SP must stay 16-byte aligned, so every push takes 16 bytes.
func (a *Asm) PushPair(t Reg) { a.emit(0xf81f0fe0 | uint32(t)) }

// PopPair loads t from SP and moves SP up by 16 (LDR Xt, [SP], #16).
func (a *Asm) PopPair(t Reg) { a.emit(0xf84107e0 | uint32(t)) }

// Moves and constants.

// MOV Xd, Xm (ORR Xd, XZR, Xm).
func (a *Asm) MOV(d, m Reg) { a.emit(0xaa0003e0 | uint32(m)<<16 | uint32(d)) }

// MOVImm loads a 64-bit constant with MOVZ and MOVKs.
func (a *Asm) MOVImm(d Reg, v uint64) {
	first := true
	for shift := uint32(0); shift < 64; shift += 16 {
		part := uint32(v>>shift) & 0xffff
		if part == 0 && !(first && shift == 48) {
			continue
		}
		if first {
			a.emit(0xd2800000 | shift/16<<21 | part<<5 | uint32(d)) // MOVZ
			first = false
		} else {
			a.emit(0xf2800000 | shift/16<<21 | part<<5 | uint32(d)) // MOVK
		}
	}
	if first {
		a.emit(0xd2800000 | uint32(d)) // MOVZ d, #0
	}
}

// Loads and stores with an unsigned, scaled immediate offset; off must be
// a multiple of the access size and in range (checked by the caller with
// FitsLoadOffset).

// FitsLoadOffset reports whether off can be the offset of a size-byte
// access.
func FitsLoadOffset(off, size int) bool {
	return off >= 0 && off%size == 0 && off/size < 4096
}

func ldst(op uint32, size int, t, n uint32, off int) uint32 {
	return op | uint32(off/size)<<10 | n<<5 | t
}

func (a *Asm) LDR(t, n Reg, off int)       { a.emit(ldst(0xf9400000, 8, uint32(t), uint32(n), off)) }
func (a *Asm) STR(t, n Reg, off int)       { a.emit(ldst(0xf9000000, 8, uint32(t), uint32(n), off)) }
func (a *Asm) LDRW(t, n Reg, off int)      { a.emit(ldst(0xb9400000, 4, uint32(t), uint32(n), off)) }
func (a *Asm) LDRB(t, n Reg, off int)      { a.emit(ldst(0x39400000, 1, uint32(t), uint32(n), off)) }
func (a *Asm) LDRH(t, n Reg, off int)      { a.emit(ldst(0x79400000, 2, uint32(t), uint32(n), off)) }
func (a *Asm) STRW(t, n Reg, off int)      { a.emit(ldst(0xb9000000, 4, uint32(t), uint32(n), off)) }
func (a *Asm) LDRD(t FReg, n Reg, off int) { a.emit(ldst(0xfd400000, 8, uint32(t), uint32(n), off)) }
func (a *Asm) STRD(t FReg, n Reg, off int) { a.emit(ldst(0xfd000000, 8, uint32(t), uint32(n), off)) }

// STP stores t and t2 at [n, #off]: off is a multiple of 8 in [-512, 504].
func (a *Asm) STP(t, t2, n Reg, off int) {
	a.emit(0xa9000000 | (uint32(off/8)&0x7f)<<15 | uint32(t2)<<10 | uint32(n)<<5 | uint32(t))
}

// Register-offset loads and stores: [Xn, Xm, LSL #log2(size)].

func ldstReg(op uint32, t, n, m uint32, shift bool) uint32 {
	i := op | m<<16 | 0x3<<13 | n<<5 | t // option LSL (UXTX)
	if shift {
		i |= 1 << 12
	}
	return i
}

func (a *Asm) LDRReg(t, n, m Reg) { a.emit(ldstReg(0xf8600800, uint32(t), uint32(n), uint32(m), true)) }
func (a *Asm) STRReg(t, n, m Reg) { a.emit(ldstReg(0xf8200800, uint32(t), uint32(n), uint32(m), true)) }
func (a *Asm) LDRWReg(t, n, m Reg) {
	a.emit(ldstReg(0xb8600800, uint32(t), uint32(n), uint32(m), true))
}
func (a *Asm) LDRSWReg(t, n, m Reg) {
	a.emit(ldstReg(0xb8a00800, uint32(t), uint32(n), uint32(m), true))
}
func (a *Asm) STRWReg(t, n, m Reg) {
	a.emit(ldstReg(0xb8200800, uint32(t), uint32(n), uint32(m), true))
}
func (a *Asm) LDRHReg(t, n, m Reg) {
	a.emit(ldstReg(0x78600800, uint32(t), uint32(n), uint32(m), true))
}
func (a *Asm) LDRSHReg(t, n, m Reg) {
	a.emit(ldstReg(0x78e00800, uint32(t), uint32(n), uint32(m), true))
}
func (a *Asm) STRHReg(t, n, m Reg) {
	a.emit(ldstReg(0x78200800, uint32(t), uint32(n), uint32(m), true))
}
func (a *Asm) LDRBReg(t, n, m Reg) {
	a.emit(ldstReg(0x38600800, uint32(t), uint32(n), uint32(m), false))
}
func (a *Asm) LDRSBReg(t, n, m Reg) {
	a.emit(ldstReg(0x38e00800, uint32(t), uint32(n), uint32(m), false))
}
func (a *Asm) STRBReg(t, n, m Reg) {
	a.emit(ldstReg(0x38200800, uint32(t), uint32(n), uint32(m), false))
}
func (a *Asm) LDRDReg(t FReg, n, m Reg) {
	a.emit(ldstReg(0xfc600800, uint32(t), uint32(n), uint32(m), true))
}
func (a *Asm) STRDReg(t FReg, n, m Reg) {
	a.emit(ldstReg(0xfc200800, uint32(t), uint32(n), uint32(m), true))
}
func (a *Asm) LDRSReg(t FReg, n, m Reg) {
	a.emit(ldstReg(0xbc600800, uint32(t), uint32(n), uint32(m), true))
}
func (a *Asm) STRSReg(t FReg, n, m Reg) {
	a.emit(ldstReg(0xbc200800, uint32(t), uint32(n), uint32(m), true))
}

// Integer arithmetic (64-bit unless named W).

func (a *Asm) ADD(d, n, m Reg)  { a.emit(0x8b000000 | uint32(m)<<16 | uint32(n)<<5 | uint32(d)) }
func (a *Asm) SUB(d, n, m Reg)  { a.emit(0xcb000000 | uint32(m)<<16 | uint32(n)<<5 | uint32(d)) }
func (a *Asm) ADDW(d, n, m Reg) { a.emit(0x0b000000 | uint32(m)<<16 | uint32(n)<<5 | uint32(d)) }
func (a *Asm) SUBW(d, n, m Reg) { a.emit(0x4b000000 | uint32(m)<<16 | uint32(n)<<5 | uint32(d)) }
func (a *Asm) MULW(d, n, m Reg) { a.emit(0x1b007c00 | uint32(m)<<16 | uint32(n)<<5 | uint32(d)) }

// ADDImm and SUBImm take a 12-bit immediate.
func (a *Asm) ADDImm(d, n Reg, imm uint32) { a.emit(0x91000000 | imm<<10 | uint32(n)<<5 | uint32(d)) }
func (a *Asm) SUBImm(d, n Reg, imm uint32) { a.emit(0xd1000000 | imm<<10 | uint32(n)<<5 | uint32(d)) }

// SUBSImm (CMP when d is ZR) sets the flags.
func (a *Asm) CMPImm(n Reg, imm uint32) { a.emit(0xf100001f | imm<<10 | uint32(n)<<5) }
func (a *Asm) CMP(n, m Reg)             { a.emit(0xeb00001f | uint32(m)<<16 | uint32(n)<<5) }
func (a *Asm) CMPW(n, m Reg)            { a.emit(0x6b00001f | uint32(m)<<16 | uint32(n)<<5) }

func (a *Asm) ANDW(d, n, m Reg) { a.emit(0x0a000000 | uint32(m)<<16 | uint32(n)<<5 | uint32(d)) }
func (a *Asm) ORRW(d, n, m Reg) { a.emit(0x2a000000 | uint32(m)<<16 | uint32(n)<<5 | uint32(d)) }
func (a *Asm) EORW(d, n, m Reg) { a.emit(0x4a000000 | uint32(m)<<16 | uint32(n)<<5 | uint32(d)) }
func (a *Asm) EOR(d, n, m Reg)  { a.emit(0xca000000 | uint32(m)<<16 | uint32(n)<<5 | uint32(d)) }
func (a *Asm) LSLW(d, n, m Reg) { a.emit(0x1ac02000 | uint32(m)<<16 | uint32(n)<<5 | uint32(d)) }
func (a *Asm) LSRW(d, n, m Reg) { a.emit(0x1ac02400 | uint32(m)<<16 | uint32(n)<<5 | uint32(d)) }
func (a *Asm) ASRW(d, n, m Reg) { a.emit(0x1ac02800 | uint32(m)<<16 | uint32(n)<<5 | uint32(d)) }

// MVNW is bitwise not (ORN Wd, WZR, Wm).
func (a *Asm) MVNW(d, m Reg) { a.emit(0x2a2003e0 | uint32(m)<<16 | uint32(d)) }

// SXTW sign-extends Wn into Xd.
func (a *Asm) SXTW(d, n Reg) { a.emit(0x93407c00 | uint32(n)<<5 | uint32(d)) }

// UXTW zero-extends Wn into Xd (MOV Wd, Wn).
func (a *Asm) UXTW(d, n Reg) { a.emit(0x2a0003e0 | uint32(n)<<16 | uint32(d)) }

// CSET sets d to 1 if c holds, else 0.
func (a *Asm) CSET(d Reg, c ArmCond) { a.emit(0x9a9f07e0 | uint32(c.Invert())<<12 | uint32(d)) }

// Floating point (double precision).

func (a *Asm) FMOVToF(d FReg, n Reg)   { a.emit(0x9e670000 | uint32(n)<<5 | uint32(d)) }
func (a *Asm) FMOVFromF(d Reg, n FReg) { a.emit(0x9e660000 | uint32(n)<<5 | uint32(d)) }
func (a *Asm) FADD(d, n, m FReg)       { a.emit(0x1e602800 | uint32(m)<<16 | uint32(n)<<5 | uint32(d)) }
func (a *Asm) FSUB(d, n, m FReg)       { a.emit(0x1e603800 | uint32(m)<<16 | uint32(n)<<5 | uint32(d)) }
func (a *Asm) FMUL(d, n, m FReg)       { a.emit(0x1e600800 | uint32(m)<<16 | uint32(n)<<5 | uint32(d)) }
func (a *Asm) FDIV(d, n, m FReg)       { a.emit(0x1e601800 | uint32(m)<<16 | uint32(n)<<5 | uint32(d)) }
func (a *Asm) FNEG(d, n FReg)          { a.emit(0x1e614000 | uint32(n)<<5 | uint32(d)) }
func (a *Asm) FMOV(d, n FReg)          { a.emit(0x1e604000 | uint32(n)<<5 | uint32(d)) }
func (a *Asm) BR(n Reg)                { a.emit(0xd61f0000 | uint32(n)<<5) }
func (a *Asm) FSQRT(d, n FReg)         { a.emit(0x1e61c000 | uint32(n)<<5 | uint32(d)) }
func (a *Asm) FABS(d, n FReg)          { a.emit(0x1e60c000 | uint32(n)<<5 | uint32(d)) }
func (a *Asm) FCMP(n, m FReg)          { a.emit(0x1e602000 | uint32(m)<<16 | uint32(n)<<5) }

// SCVTFW converts the signed Wn to Dd, UCVTFW the unsigned one.
func (a *Asm) SCVTFW(d FReg, n Reg) { a.emit(0x1e620000 | uint32(n)<<5 | uint32(d)) }
func (a *Asm) UCVTFW(d FReg, n Reg) { a.emit(0x1e630000 | uint32(n)<<5 | uint32(d)) }

// SCVTF converts the signed Xn to Dd.
func (a *Asm) SCVTF(d FReg, n Reg) { a.emit(0x9e620000 | uint32(n)<<5 | uint32(d)) }

// FCVTZS truncates Dn to the signed Xd (saturating; NaN gives 0).
func (a *Asm) FCVTZS(d Reg, n FReg) { a.emit(0x9e780000 | uint32(n)<<5 | uint32(d)) }

// FCVTZSW truncates Dn to the signed Wd (saturating; NaN gives 0).
func (a *Asm) FCVTZSW(d Reg, n FReg) { a.emit(0x1e780000 | uint32(n)<<5 | uint32(d)) }

// FCVTDS widens the single Sn to Dd; FCVTSD narrows Dn to Sd.
func (a *Asm) FCVTDS(d, n FReg) { a.emit(0x1e22c000 | uint32(n)<<5 | uint32(d)) }
func (a *Asm) FCVTSD(d, n FReg) { a.emit(0x1e624000 | uint32(n)<<5 | uint32(d)) }

// ADDShift is ADD Xd, Xn, Xm, LSL #shift.
func (a *Asm) ADDShift(d, n, m Reg, shift uint32) {
	a.emit(0x8b000000 | uint32(m)<<16 | shift<<10 | uint32(n)<<5 | uint32(d))
}

// SDIVW is signed 32-bit division (rounding toward zero; x/0 is 0).
func (a *Asm) SDIVW(d, n, m Reg) { a.emit(0x1ac00c00 | uint32(m)<<16 | uint32(n)<<5 | uint32(d)) }

// MSUBW is Wd = Wa - Wn*Wm.
func (a *Asm) MSUBW(d, n, m, ra Reg) {
	a.emit(0x1b008000 | uint32(m)<<16 | uint32(ra)<<10 | uint32(n)<<5 | uint32(d))
}

// CMPWImm compares Wn with a 12-bit immediate.
func (a *Asm) CMPWImm(n Reg, imm uint32) { a.emit(0x7100001f | imm<<10 | uint32(n)<<5) }

// FCMPZero compares Dn with +0.0.
func (a *Asm) FCMPZero(n FReg) { a.emit(0x1e602008 | uint32(n)<<5) }

// LDRBOff loads the byte at Xn+off (zero-extended).
func (a *Asm) LDRBOff(t, n Reg, off int) { a.LDRB(t, n, off) }

// Truncate drops the instructions from n on; branches among them to the
// given labels are forgotten.
func (a *Asm) Truncate(n int, labels ...*Label) {
	a.Code = a.Code[:n]
	for _, l := range append(labels, a.waiting...) {
		a.fixes.drop(l, n)
	}
}
