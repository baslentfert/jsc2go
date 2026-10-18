//go:build arm64 && (darwin || linux)

package jit

import (
	"math"
	"testing"
	"unsafe"
)

// run assembles body (which reads X0-X3 and leaves its result in X0) and
// calls it.
func run(t *testing.T, body func(a *Asm), args ...uintptr) uintptr {
	t.Helper()
	var a Asm
	body(&a)
	a.RET()
	c, err := New(a.Code)
	if err != nil {
		t.Fatal(err)
	}
	defer c.KeepAlive()
	var x [4]uintptr
	copy(x[:], args)
	return Call(c.Entry(), x[0], x[1], x[2], x[3])
}

func TestMOVImm(t *testing.T) {
	for _, v := range []uint64{0, 1, 0xffff, 0x10000, 0xfffc000000000000, 0x123456789abcdef0, math.MaxUint64} {
		if got := run(t, func(a *Asm) { a.MOVImm(0, v) }); uint64(got) != v {
			t.Errorf("MOVImm %#x: got %#x", v, got)
		}
	}
}

func TestFloat(t *testing.T) {
	f := func(op func(a *Asm)) float64 {
		bits := run(t, func(a *Asm) {
			a.FMOVToF(0, 0)
			a.FMOVToF(1, 1)
			op(a)
			a.FMOVFromF(0, 2)
		}, uintptr(math.Float64bits(7.5)), uintptr(math.Float64bits(2)))
		return math.Float64frombits(uint64(bits))
	}
	for name, c := range map[string]struct {
		op   func(a *Asm)
		want float64
	}{
		"add": {func(a *Asm) { a.FADD(2, 0, 1) }, 9.5},
		"sub": {func(a *Asm) { a.FSUB(2, 0, 1) }, 5.5},
		"mul": {func(a *Asm) { a.FMUL(2, 0, 1) }, 15},
		"div": {func(a *Asm) { a.FDIV(2, 0, 1) }, 3.75},
		"neg": {func(a *Asm) { a.FNEG(2, 0) }, -7.5},
	} {
		if got := f(c.op); got != c.want {
			t.Errorf("%s: got %v want %v", name, got, c.want)
		}
	}
}

func TestConvert(t *testing.T) {
	trunc := func(d float64) int64 {
		return int64(run(t, func(a *Asm) { a.FMOVToF(0, 0); a.FCVTZS(0, 0) }, uintptr(math.Float64bits(d))))
	}
	if trunc(-3.7) != -3 || trunc(1e20) != math.MaxInt64 || trunc(math.NaN()) != 0 {
		t.Error("FCVTZS")
	}
	toD := func(body func(a *Asm), x uintptr) float64 {
		return math.Float64frombits(uint64(run(t, func(a *Asm) { body(a); a.FMOVFromF(0, 0) }, x)))
	}
	if toD(func(a *Asm) { a.SCVTFW(0, 0) }, uintptr(uint32(0xfffffffe))) != -2 {
		t.Error("SCVTFW")
	}
	if toD(func(a *Asm) { a.UCVTFW(0, 0) }, uintptr(uint32(0xfffffffe))) != 4294967294 {
		t.Error("UCVTFW")
	}
}

func TestBranchesAndCompare(t *testing.T) {
	// x0 = x0 < x1 (signed) ? 1 : 2, via a forward and a backward branch.
	body := func(a *Asm) {
		less, done, top := NewLabel(), NewLabel(), NewLabel()
		a.B(top)
		a.Bind(less)
		a.MOVImm(0, 1)
		a.B(done)
		a.Bind(top)
		a.CMP(0, 1)
		a.BCond(ALT, less)
		a.MOVImm(0, 2)
		a.Bind(done)
	}
	if run(t, body, 1, 5) != 1 || run(t, body, 9, 5) != 2 {
		t.Error("B/BCond")
	}
	cset := func(x, y uintptr) uintptr { return run(t, func(a *Asm) { a.CMP(0, 1); a.CSET(0, ALO) }, x, y) }
	if cset(1, 2) != 1 || cset(2, 1) != 0 {
		t.Error("CSET")
	}
	cbz := func(x uintptr) uintptr {
		return run(t, func(a *Asm) {
			z := NewLabel()
			a.CBZ(0, z)
			a.MOVImm(0, 7)
			a.Bind(z)
		}, x)
	}
	if cbz(0) != 0 || cbz(3) != 7 {
		t.Error("CBZ")
	}
}

// armSink makes memory the tests pass to generated code escape to the heap:
// Call may grow the goroutine stack, which would move it.
var armSink []any

func TestMemory(t *testing.T) {
	buf := []uint64{10, 20, 30, 0}
	armSink = append(armSink, buf)
	p := uintptr(unsafe.Pointer(&buf[0]))
	got := run(t, func(a *Asm) {
		a.LDR(2, 0, 8)    // 20
		a.LDRReg(3, 0, 1) // buf[x1] = 30
		a.ADD(2, 2, 3)    // 50
		a.STR(2, 0, 24)   // buf[3] = 50
		a.MOVImm(4, 0)
		a.STRReg(4, 0, 4) // buf[0] = 0
		a.LDR(0, 0, 24)
	}, p, 2)
	if got != 50 || buf[3] != 50 || buf[0] != 0 {
		t.Errorf("got %d, buf %v", got, buf)
	}
	b := []byte{0xff, 0x80, 0x7f}
	armSink = append(armSink, b)
	pb := uintptr(unsafe.Pointer(&b[0]))
	if run(t, func(a *Asm) { a.LDRSBReg(0, 0, 1); a.SXTW(0, 0) }, pb, 1) != uintptr(0xffffffffffffff80) &&
		run(t, func(a *Asm) { a.LDRSBReg(0, 0, 1) }, pb, 1)&0xffffffff != 0xffffff80 {
		t.Error("LDRSB")
	}
	if run(t, func(a *Asm) { a.LDRBReg(0, 0, 1) }, pb, 1) != 0x80 {
		t.Error("LDRB")
	}
	f := []float64{1.5, 2.25}
	armSink = append(armSink, f)
	pf := uintptr(unsafe.Pointer(&f[0]))
	got = run(t, func(a *Asm) { a.LDRDReg(0, 0, 1); a.FADD(0, 0, 0); a.STRDReg(0, 0, ZR); a.FMOVFromF(0, 0) }, pf, 1)
	if math.Float64frombits(uint64(got)) != 4.5 || f[0] != 4.5 {
		t.Errorf("LDRD/STRD: %v %v", math.Float64frombits(uint64(got)), f)
	}
}

func TestInt32Ops(t *testing.T) {
	op := func(body func(a *Asm), x, y uint32) uint32 { return uint32(run(t, body, uintptr(x), uintptr(y))) }
	if op(func(a *Asm) { a.ADDW(0, 0, 1) }, 0x7fffffff, 1) != 0x80000000 ||
		op(func(a *Asm) { a.SUBW(0, 0, 1) }, 0, 1) != 0xffffffff ||
		op(func(a *Asm) { a.MULW(0, 0, 1) }, 0x10000, 0x10000) != 0 ||
		op(func(a *Asm) { a.ANDW(0, 0, 1) }, 0xf0f0, 0xff00) != 0xf000 ||
		op(func(a *Asm) { a.ORRW(0, 0, 1) }, 0xf0f0, 0xff00) != 0xfff0 ||
		op(func(a *Asm) { a.EORW(0, 0, 1) }, 0xf0f0, 0xff00) != 0x0ff0 ||
		op(func(a *Asm) { a.LSLW(0, 0, 1) }, 1, 33) != 2 ||
		op(func(a *Asm) { a.LSRW(0, 0, 1) }, 0x80000000, 31) != 1 ||
		op(func(a *Asm) { a.ASRW(0, 0, 1) }, 0x80000000, 31) != 0xffffffff ||
		op(func(a *Asm) { a.MVNW(0, 0) }, 0, 0) != 0xffffffff {
		t.Error("int32 ops")
	}
}

func TestDivMod(t *testing.T) {
	mod := func(x, y int32) int32 {
		return int32(run(t, func(a *Asm) { a.SDIVW(2, 0, 1); a.MSUBW(0, 2, 1, 0) }, uintptr(uint32(x)), uintptr(uint32(y))))
	}
	if mod(7, 3) != 1 || mod(-7, 3) != -1 || mod(7, -3) != 1 {
		t.Error("SDIVW/MSUBW")
	}
	if run(t, func(a *Asm) { a.ADDShift(0, 0, 1, 4) }, 1, 2) != 33 {
		t.Error("ADDShift")
	}
	zero := func(d float64) uintptr {
		return run(t, func(a *Asm) { a.FMOVToF(0, 0); a.FCMPZero(0); a.CSET(0, AEQ) }, uintptr(math.Float64bits(d)))
	}
	if zero(0) != 1 || zero(math.Copysign(0, -1)) != 1 || zero(1) != 0 || zero(math.NaN()) != 0 {
		t.Error("FCMPZero")
	}
}

func TestBranchRange(t *testing.T) {
	var a Asm
	far := NewLabel()
	a.BCond(AEQ, far)
	for i := 0; i < 1<<18; i++ {
		a.emit(0xd503201f) // nop
	}
	a.Bind(far)
	if !a.OutOfRange {
		t.Error("a conditional branch over 1 MB must be flagged")
	}
	var b Asm
	near := NewLabel()
	b.BCond(AEQ, near)
	for i := 0; i < 1000; i++ {
		b.emit(0xd503201f)
	}
	b.Bind(near)
	if b.OutOfRange {
		t.Error("near branch flagged")
	}
}

func TestFarBranches(t *testing.T) {
	body := func(a *Asm) {
		a.Far = true
		less, done := NewLabel(), NewLabel()
		a.CMP(0, 1)
		a.BCond(ALT, less)
		a.CBZ(0, done)
		a.MOVImm(0, 2)
		a.B(done)
		a.Bind(less)
		a.CBNZ(1, done)
		a.MOVImm(0, 9)
		a.Bind(done)
	}
	if run(t, body, 1, 5) != 1 || run(t, body, 9, 5) != 2 || run(t, body, 0, 0) != 0 {
		t.Error("far branches")
	}
}
