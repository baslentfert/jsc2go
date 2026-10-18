package jit

// The macro assembler (JSC's MacroAssembler): the operations the code
// generator in package vm is written in, each implemented per architecture
// (masm_arm64.go, masm_amd64.go). Registers are abstract; comparisons have
// JavaScript's meaning, NaN included, rather than a processor's flags.

// R is a general-purpose register.
type R uint8

const (
	RRegs  R = iota // the frame's regs[0] (fixed)
	RFrame          // the *Frame (fixed)
	RMask           // numberMask (fixed)
	S0              // S0-S9 and T0 are free for the code generator
	S1
	S2
	S3
	S4
	S5
	S6
	S7
	S8
	S9
	T0
	numR
)

// bufferMax is the largest code memory a Buffer keeps (Release): a VM
// keeps one, so that compiling does not allocate it every time (the code
// buffer was a large part of what compiling allocated), but not the
// memory of a huge function's code.
const bufferMax = 4 << 20

// Buffer is memory for code that a Masm can use again (ReserveIn).
type Buffer struct {
	b       []byte   // amd64
	w       []uint32 // arm64
	fixes   []fixNode
	waiting []*Label
}

// take gives a Masm buf's fixup and waiting lists.
func (buf *Buffer) take() (fixList, []*Label) {
	f, w := fixList{buf.fixes[:0]}, buf.waiting[:0]
	buf.fixes, buf.waiting = nil, nil
	return f, w
}

// keep keeps a Masm's fixup and waiting lists in buf, if not too large.
func (buf *Buffer) keep(f fixList, w []*Label) {
	if cap(f.f) <= bufferMax/16 && cap(f.f) > cap(buf.fixes) {
		buf.fixes = f.f[:0]
	}
	if cap(w) <= bufferMax/16 && cap(w) > cap(buf.waiting) {
		clear(w[:cap(w)]) // no labels kept alive
		buf.waiting = w[:0]
	}
}

// F is a floating-point (double) register F0-F7; F7 may be used by
// TruncExactInt32 as its scratch. F4 and F5 are the VM's number cache
// across instructions (vm/jit.go): no other code may write them.
type F uint8

// Cond is an integer comparison.
type Cond uint8

const (
	EQ Cond = iota
	NE
	ULT // unsigned
	ULE
	UGT
	UGE
	SLT // signed
	SLE
	SGT
	SGE
)

// FCond is a comparison of doubles with JavaScript's results for NaN:
// false for LT-EQ, true for NE and the negations.
type FCond uint8

const (
	FLT FCond = iota
	FLE
	FGT
	FGE
	FEQ
	FNE
	FNLT // !(a < b)
	FNLE
	FNGT
	FNGE
)

// Op32 is a 32-bit integer operation (results are zero-extended).
type Op32 uint8

const (
	Add32 Op32 = iota
	Sub32
	Mul32
	And32
	Or32
	Xor32
	Shl32 // shift counts are taken modulo 32
	Shr32 // unsigned
	Sar32
)

// FOp is a double operation.
type FOp uint8

const (
	FAdd FOp = iota
	FSub
	FMul
	FDiv
)

// Elem is the element type of an indexed load or store.
type Elem uint8

const (
	ElemU8 Elem = iota
	ElemS8
	ElemU16
	ElemS16
	ElemU32
	ElemS32
)

// unbound counts the labels among waiting that still have fixups.
func unbound(waiting []*Label) int {
	n := 0
	for _, l := range waiting {
		if l.pos < 0 && l.head != 0 {
			n++
		}
	}
	return n
}
