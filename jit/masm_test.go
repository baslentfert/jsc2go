//go:build ((arm64 || amd64) && (darwin || linux)) || (amd64 && windows)

package jit

import (
	"math"
	"math/rand"
	"runtime"
	"testing"
	"unsafe"
)

// runMasm assembles body after an entry stub and runs it with mem as the
// regs (operands at 8-byte slots).
func runMasm(t *testing.T, mem []uint64, body func(m *Masm)) {
	t.Helper()
	var m Masm
	start := NewLabel()
	m.EntryStub(start)
	m.Bind(start)
	body(&m)
	m.Return(0)
	c, err := NewBytes(m.Code())
	if err != nil {
		t.Fatal(err)
	}
	// Call may grow the goroutine stack: the memory must be on the heap.
	heap := append([]uint64(nil), mem...)
	masmSink = heap
	Call(c.Entry(), uintptr(unsafe.Pointer(&heap[0])), 0, 0, 0)
	c.KeepAlive()
	copy(mem, heap)
	masmSink = nil
}

// masmSink makes runMasm's memory escape to the heap.
var masmSink []uint64

// bytesSink does the same for byte buffers the generated code reads.
var bytesSink []byte

// tableSink does the same for TestMasmJumpReg's jump table.
var tableSink []uintptr

var scratch = []R{S0, S1, S2, S3, S4, S5, S6, S7, S8, S9, T0}

func TestMasmAlu32(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	vals := []uint32{0, 1, 2, 31, 32, 33, 0x7fffffff, 0x80000000, 0xffffffff, 12345, 0xdeadbeef}
	ref := map[Op32]func(a, b uint32) uint32{
		Add32: func(a, b uint32) uint32 { return a + b },
		Sub32: func(a, b uint32) uint32 { return a - b },
		Mul32: func(a, b uint32) uint32 { return a * b },
		And32: func(a, b uint32) uint32 { return a & b },
		Or32:  func(a, b uint32) uint32 { return a | b },
		Xor32: func(a, b uint32) uint32 { return a ^ b },
		Shl32: func(a, b uint32) uint32 { return a << (b & 31) },
		Shr32: func(a, b uint32) uint32 { return a >> (b & 31) },
		Sar32: func(a, b uint32) uint32 { return uint32(int32(a) >> (b & 31)) },
	}
	for op, f := range ref {
		for i := 0; i < 60; i++ {
			a, b := vals[r.Intn(len(vals))], vals[r.Intn(len(vals))]
			// Registers, aliased in every way.
			ra, rb := scratch[r.Intn(len(scratch))], scratch[r.Intn(len(scratch))]
			rd := []R{ra, rb, scratch[r.Intn(len(scratch))]}[r.Intn(3)]
			if ra == rb {
				b = a
			}
			mem := []uint64{uint64(a), uint64(b), 0}
			runMasm(t, mem, func(m *Masm) {
				m.Load64(ra, RRegs, 0)
				m.Load64(rb, RRegs, 8)
				m.Alu32(op, rd, ra, rb)
				m.Store64(rd, RRegs, 16)
			})
			if want := uint64(f(a, b)); mem[2] != want {
				t.Fatalf("op %d %#x %#x (regs %d %d -> %d): got %#x want %#x", op, a, b, ra, rb, rd, mem[2], want)
			}
		}
	}
}

func TestMasmRemAndShift(t *testing.T) {
	for _, c := range [][2]int32{{7, 3}, {-7, 3}, {0, 5}, {-2147483648, 7}, {2147483647, 2147483647}} {
		for _, regs := range [][3]R{{S0, S1, S2}, {S2, S0, S0}, {S1, S2, S2}, {S2, S2, S1}, {T0, S9, S5}} {
			mem := []uint64{uint64(uint32(c[0])), uint64(uint32(c[1])), 0, 77, 88}
			runMasm(t, mem, func(m *Masm) {
				// S0 and S2 (RAX and RDX on amd64) must survive Rem32.
				m.Load64(S0, RRegs, 24)
				m.Load64(S2, RRegs, 32)
				m.Load64(regs[1], RRegs, 0)
				m.Load64(regs[2], RRegs, 8)
				if regs[1] == regs[2] {
					return
				}
				m.Rem32(regs[0], regs[1], regs[2])
				m.Store64(regs[0], RRegs, 16)
				if regs[0] != S0 && regs[1] != S0 && regs[2] != S0 {
					m.Store64(S0, RRegs, 24)
				}
			})
			if regs[1] == regs[2] {
				continue
			}
			if want := uint32(c[0] % c[1]); uint32(mem[2]) != want {
				t.Errorf("%d %% %d with %v: got %d", c[0], c[1], regs, int32(mem[2]))
			}
		}
	}
	for shift := uint32(0); shift <= 4; shift++ {
		mem := []uint64{1000, 3, 0}
		runMasm(t, mem, func(m *Masm) {
			m.Load64(S0, RRegs, 0)
			m.Load64(S1, RRegs, 8)
			m.AddShift64(S2, S0, S1, shift)
			m.Store64(S2, RRegs, 16)
		})
		if mem[2] != 1000+3<<shift {
			t.Errorf("AddShift64 %d: %d", shift, mem[2])
		}
	}
}

func TestMasmBranches(t *testing.T) {
	vals := []uint64{0, 1, 2, 0x7fffffff, 0x80000000, 0xffffffff, 1 << 40, math.MaxUint64}
	conds := map[Cond]func(a, b uint64) bool{
		EQ: func(a, b uint64) bool { return a == b }, NE: func(a, b uint64) bool { return a != b },
		ULT: func(a, b uint64) bool { return a < b }, ULE: func(a, b uint64) bool { return a <= b },
		UGT: func(a, b uint64) bool { return a > b }, UGE: func(a, b uint64) bool { return a >= b },
		SLT: func(a, b uint64) bool { return int64(a) < int64(b) }, SLE: func(a, b uint64) bool { return int64(a) <= int64(b) },
		SGT: func(a, b uint64) bool { return int64(a) > int64(b) }, SGE: func(a, b uint64) bool { return int64(a) >= int64(b) },
	}
	for c, f := range conds {
		for _, a := range vals {
			for _, b := range vals {
				mem := []uint64{a, b, 0, 0, 0}
				runMasm(t, mem, func(m *Masm) {
					taken, taken32, done, done32 := NewLabel(), NewLabel(), NewLabel(), NewLabel()
					m.Load64(S0, RRegs, 0)
					m.Load64(S1, RRegs, 8)
					m.MovImm(T0, 1)
					m.Branch64(c, S0, S1, taken)
					m.MovImm(T0, 2)
					m.Bind(taken)
					m.Store64(T0, RRegs, 16)
					m.MovImm(T0, 1)
					m.Branch32(c, S0, S1, taken32)
					m.MovImm(T0, 2)
					m.Bind(taken32)
					m.Store64(T0, RRegs, 24)
					m.SetCond32(c, S2, S0, S1)
					m.Store64(S2, RRegs, 32)
					_, _ = done, done32
				})
				if (mem[2] == 1) != f(a, b) {
					t.Errorf("Branch64 %d %#x %#x", c, a, b)
				}
				want32 := f(uint64(uint32(a)), uint64(uint32(b)))
				if c >= SLT {
					want32 = f(uint64(int64(int32(a))), uint64(int64(int32(b))))
				}
				if (mem[3] == 1) != want32 || (mem[4] == 1) != want32 {
					t.Errorf("Branch32/SetCond32 %d %#x %#x: %d %d", c, a, b, mem[3], mem[4])
				}
			}
		}
	}
}

func TestMasmFloat(t *testing.T) {
	nan := math.NaN()
	vals := []float64{0, math.Copysign(0, -1), 1, -1, 1.5, -2.5, 3e9, -3e9, 2147483647, -2147483648, 2147483648,
		1e19, -1e19, math.Inf(1), math.Inf(-1), nan, 9.2e18, -9.3e18}
	fconds := map[FCond]func(a, b float64) bool{
		FLT: func(a, b float64) bool { return a < b }, FLE: func(a, b float64) bool { return a <= b },
		FGT: func(a, b float64) bool { return a > b }, FGE: func(a, b float64) bool { return a >= b },
		FEQ: func(a, b float64) bool { return a == b }, FNE: func(a, b float64) bool { return a != b },
		FNLT: func(a, b float64) bool { return !(a < b) }, FNLE: func(a, b float64) bool { return !(a <= b) },
		FNGT: func(a, b float64) bool { return !(a > b) }, FNGE: func(a, b float64) bool { return !(a >= b) },
	}
	for _, a := range vals {
		for _, b := range vals {
			for c, f := range fconds {
				mem := []uint64{math.Float64bits(a), math.Float64bits(b), 0, 0}
				runMasm(t, mem, func(m *Masm) {
					taken := NewLabel()
					m.Load64(S0, RRegs, 0)
					m.Load64(S1, RRegs, 8)
					m.MovToF(3, S0)
					m.MovToF(4, S1)
					m.MovImm(T0, 1)
					m.FBranch(c, 3, 4, taken)
					m.MovImm(T0, 2)
					m.Bind(taken)
					m.Store64(T0, RRegs, 16)
					m.FSetBool(c, S5, 3, 4)
					m.Store64(S5, RRegs, 24)
				})
				if (mem[2] == 1) != f(a, b) || (mem[3] == 1) != f(a, b) {
					t.Errorf("FCond %d %v %v: branch %d set %d", c, a, b, mem[2], mem[3])
				}
			}
			for _, op := range []FOp{FAdd, FSub, FMul, FDiv} {
				for _, regs := range [][3]F{{2, 0, 1}, {0, 0, 1}, {1, 0, 1}, {0, 0, 0}} {
					mem := []uint64{math.Float64bits(a), math.Float64bits(b), 0}
					runMasm(t, mem, func(m *Masm) {
						m.Load64(S0, RRegs, 0)
						m.Load64(S1, RRegs, 8)
						m.MovToF(regs[1], S0)
						if regs[2] != regs[1] {
							m.MovToF(regs[2], S1)
						}
						m.FArith(op, regs[0], regs[1], regs[2])
						m.MovFromF(S2, regs[0])
						m.Store64(S2, RRegs, 16)
					})
					x, y := a, b
					if regs[2] == regs[1] {
						y = a
					}
					var want float64
					switch op {
					case FAdd:
						want = x + y
					case FSub:
						want = x - y
					case FMul:
						want = x * y
					case FDiv:
						want = x / y
					}
					if got := math.Float64frombits(mem[2]); got != want && !(math.IsNaN(got) && math.IsNaN(want)) {
						t.Errorf("FArith %d %v %v %v: %v want %v", op, x, y, regs, got, want)
					}
				}
			}
		}
		// Conversions.
		mem := []uint64{math.Float64bits(a), 0, 0, 0, 0, 0}
		runMasm(t, mem, func(m *Masm) {
			fail, fail2, next := NewLabel(), NewLabel(), NewLabel()
			m.Load64(S0, RRegs, 0)
			m.MovToF(2, S0)
			m.MovImm(T0, 1)
			m.TruncInt64(S1, 2, fail)
			m.Store64(S1, RRegs, 8)
			m.MovImm(T0, 0)
			m.Bind(fail)
			m.Store64(T0, RRegs, 16)
			m.MovImm(T0, 1)
			m.TruncExactInt32(S2, 2, fail2)
			m.ZeroExt32(S2, S2)
			m.Store64(S2, RRegs, 24)
			m.MovImm(T0, 0)
			m.Bind(fail2)
			m.Store64(T0, RRegs, 32)
			m.FNeg(5, 2)
			m.MovFromF(S3, 5)
			m.Store64(S3, RRegs, 40)
			m.B(next)
			m.Bind(next)
		})
		if mem[2] == 0 { // TruncInt64 succeeded: it must be exact
			if math.IsNaN(a) {
				if mem[1] != 0 {
					t.Errorf("TruncInt64(NaN) = %d", int64(mem[1]))
				}
			} else if int64(mem[1]) != int64(a) {
				t.Errorf("TruncInt64(%v) = %d", a, int64(mem[1]))
			}
		} else if !math.IsNaN(a) && math.Abs(a) < 9e18 {
			t.Errorf("TruncInt64(%v) failed", a)
		}
		isInt32 := a == math.Trunc(a) && a >= -2147483648 && a <= 2147483647
		if (mem[4] == 0) != isInt32 || isInt32 && int32(uint32(mem[3])) != int32(a) {
			t.Errorf("TruncExactInt32(%v): ok %v value %d", a, mem[4] == 0, int32(uint32(mem[3])))
		}
		if neg := math.Float64frombits(mem[5]); neg != -a && !math.IsNaN(a) || math.Signbit(neg) == math.Signbit(a) {
			t.Errorf("FNeg(%v) = %v", a, neg)
		}
	}
	for _, v := range []uint32{0, 1, 0x7fffffff, 0x80000000, 0xffffffff} {
		mem := []uint64{uint64(v), 0, 0, 0}
		runMasm(t, mem, func(m *Masm) {
			m.Load64(S0, RRegs, 0)
			m.CvtS32(0, S0)
			m.CvtU32(1, S0)
			m.CvtS64(2, S0)
			m.MovFromF(S1, 0)
			m.Store64(S1, RRegs, 8)
			m.MovFromF(S1, 1)
			m.Store64(S1, RRegs, 16)
			m.MovFromF(S1, 2)
			m.Store64(S1, RRegs, 24)
		})
		if math.Float64frombits(mem[1]) != float64(int32(v)) || math.Float64frombits(mem[2]) != float64(v) ||
			math.Float64frombits(mem[3]) != float64(int64(v)) {
			t.Errorf("Cvt %#x: %v %v %v", v, math.Float64frombits(mem[1]), math.Float64frombits(mem[2]), math.Float64frombits(mem[3]))
		}
	}
	for _, d := range []float64{0, -1.5, 1e10, nan} {
		mem := []uint64{math.Float64bits(d), 0, 0}
		runMasm(t, mem, func(m *Masm) {
			isNaN, zeroOrNaN := NewLabel(), NewLabel()
			m.Load64(S0, RRegs, 0)
			m.MovToF(6, S0)
			m.MovImm(T0, 0)
			m.FBranchNaN(6, isNaN)
			m.MovImm(T0, 1)
			m.Bind(isNaN)
			m.Store64(T0, RRegs, 8)
			m.MovImm(T0, 0)
			m.FBranchZeroOrNaN(6, zeroOrNaN)
			m.MovImm(T0, 1)
			m.Bind(zeroOrNaN)
			m.Store64(T0, RRegs, 16)
		})
		if (mem[1] == 0) != math.IsNaN(d) || (mem[2] == 0) != (d == 0 || math.IsNaN(d)) {
			t.Errorf("NaN branches %v: %d %d", d, mem[1], mem[2])
		}
	}
}

func TestMasmMemory(t *testing.T) {
	buf := make([]byte, 64)
	for i := range buf {
		buf[i] = byte(200 + i)
	}
	base := uint64(uintptr(unsafe.Pointer(&buf[0])))
	for e, want := range map[Elem]uint32{ElemU8: 0xCB, ElemS8: 0xFFFFFFCB, ElemU16: 0xCECD, ElemS16: 0xFFFFCECD,
		ElemU32: 0xDAD9D8D7, ElemS32: 0xDAD9D8D7} {
		idx := uint64(3)
		mem := []uint64{base, idx, 0}
		runMasm(t, mem, func(m *Masm) {
			m.Load64(S6, RRegs, 0)
			m.Load64(S9, RRegs, 8)
			m.LoadIdx(e, S8, S6, S9)
			m.Store64(S8, RRegs, 16)
		})
		_ = want
		scale := map[Elem]int{ElemU8: 1, ElemS8: 1, ElemU16: 2, ElemS16: 2, ElemU32: 4, ElemS32: 4}[e]
		var raw uint32
		for k := scale - 1; k >= 0; k-- {
			raw = raw<<8 | uint32(buf[3*scale+k])
		}
		switch e {
		case ElemS8:
			raw = uint32(int32(int8(raw)))
		case ElemS16:
			raw = uint32(int32(int16(raw)))
		}
		if uint32(mem[2]) != raw {
			t.Errorf("LoadIdx %d: %#x want %#x", e, uint32(mem[2]), raw)
		}
	}
	// Stores of each width, and float elements.
	f64 := []float64{0, 0, 0, 0}
	f32 := []float32{0, 0, 0, 0}
	ints := make([]byte, 16)
	mem := []uint64{uint64(uintptr(unsafe.Pointer(&f64[0]))), uint64(uintptr(unsafe.Pointer(&f32[0]))),
		uint64(uintptr(unsafe.Pointer(&ints[0]))), 2, math.Float64bits(1.25), 0x11223344, 0, 0}
	runMasm(t, mem, func(m *Masm) {
		m.Load64(S0, RRegs, 0)
		m.Load64(S3, RRegs, 8)
		m.Load64(S4, RRegs, 16)
		m.Load64(S9, RRegs, 24)
		m.Load64(T0, RRegs, 32)
		m.MovToF(1, T0)
		m.StoreIdxF64(1, S0, S9)
		m.StoreIdxF32(1, S3, S9)
		m.LoadIdxF64(2, S0, S9)
		m.LoadIdxF32(3, S3, S9)
		m.FArith(FAdd, 4, 2, 3)
		m.MovFromF(S5, 4)
		m.Store64(S5, RRegs, 48)
		m.Load64(S1, RRegs, 40)
		m.StoreIdx(ElemU8, S1, S4, S9)
		m.StoreIdx(ElemU16, S1, S4, S9)
		m.StoreIdx(ElemU32, S1, S4, S9)
		m.StoreZero64(RRegs, 56)
	})
	if f64[2] != 1.25 || f32[2] != 1.25 || math.Float64frombits(mem[6]) != 2.5 {
		t.Errorf("float elements: %v %v %v", f64, f32, math.Float64frombits(mem[6]))
	}
	if ints[2] != 0x44 || ints[4] != 0x44 || ints[5] != 0x33 || ints[8] != 0x44 || ints[11] != 0x11 {
		t.Errorf("int stores: % x", ints)
	}
}

// TestMasmLoadOffsets loads zero-extended bytes, halfwords and words at
// constant offsets (Load16 is how generated code reads UTF-16 strings),
// and stores a word (Store32: array lengths).
func TestMasmLoadOffsets(t *testing.T) {
	buf := make([]byte, 16)
	bytesSink = buf // generated code needs it on the heap (see masmSink)
	defer func() { bytesSink = nil }()
	for i := range buf {
		buf[i] = byte(0xF0 + i)
	}
	mem := []uint64{uint64(uintptr(unsafe.Pointer(&buf[0]))), 0, 0, 0}
	runMasm(t, mem, func(m *Masm) {
		m.Load64(S6, RRegs, 0)
		m.Load8(S7, S6, 3)
		m.Store64(S7, RRegs, 8)
		m.Load16(S7, S6, 6)
		m.Store64(S7, RRegs, 16)
		m.Load32(S7, S6, 8)
		m.Store64(S7, RRegs, 24)
		m.MovImm(S8, 0x1122334455667788)
		m.Store32(S8, S6, 12)
	})
	if mem[1] != 0xF3 || mem[2] != 0xF7F6 || mem[3] != 0xFBFAF9F8 {
		t.Errorf("loads: %#x %#x %#x", mem[1], mem[2], mem[3])
	}
	if got := [5]byte(buf[11:16]); got != [5]byte{0xFB, 0x88, 0x77, 0x66, 0x55} {
		t.Errorf("Store32 wrote % x", got)
	}
}

// TestMasmThunkCall calls shared generated code (a thunk) from generated
// code twice and returns from it with ThunkReturn; RRegs survives.
func TestMasmThunkCall(t *testing.T) {
	var tm Masm
	tm.Load64(S1, RRegs, 0)
	tm.AddImm64(S1, S1, 5)
	tm.Store64(S1, RRegs, 0)
	tm.ThunkReturn()
	thunk, err := NewBytes(tm.Code())
	if err != nil {
		t.Fatal(err)
	}
	mem := []uint64{10, uint64(thunk.Entry()), 0}
	runMasm(t, mem, func(m *Masm) {
		m.Load64(S9, RRegs, 8)
		m.CallThunk(S9)
		m.Load64(S9, RRegs, 8)
		m.CallThunk(S9)
		m.MovImm(S2, 1)
		m.Store64(S2, RRegs, 16)
	})
	thunk.KeepAlive()
	if mem[0] != 20 || mem[2] != 1 {
		t.Errorf("thunk calls: %d %d, want 20 1", mem[0], mem[2])
	}
}

// TestMasmFloatUnary checks FSqrt, FAbs and TruncS64.
func TestMasmFloatUnary(t *testing.T) {
	for _, v := range []float64{0, math.Copysign(0, -1), 2, 0.25, -1.5, 1e300, -3.7, 123456789.9, -2147483649.5,
		math.Inf(1), math.Inf(-1), math.NaN(), 4.5e15, -9.2e18} {
		mem := []uint64{math.Float64bits(v), 0, 0, 0}
		runMasm(t, mem, func(m *Masm) {
			m.Load64(S0, RRegs, 0)
			m.MovToF(1, S0)
			m.FSqrt(2, 1)
			m.MovFromF(S1, 2)
			m.Store64(S1, RRegs, 8)
			m.FAbs(3, 1)
			m.MovFromF(S1, 3)
			m.Store64(S1, RRegs, 16)
			m.TruncS64(S2, 1)
			m.Store64(S2, RRegs, 24)
		})
		sq, abs := math.Float64frombits(mem[1]), math.Float64frombits(mem[2])
		if want := math.Sqrt(v); !(sq == want || sq != sq && want != want) || math.Signbit(sq) != math.Signbit(want) && want == want {
			t.Errorf("FSqrt(%v) = %v, want %v", v, sq, want)
		}
		if want := math.Abs(v); mem[2] != math.Float64bits(want) && !(abs != abs && want != want) {
			t.Errorf("FAbs(%v) = %v, want %v", v, abs, want)
		}
		if math.Abs(v) < 9e18 && v == v {
			if want := int64(v); int64(mem[3]) != want {
				t.Errorf("TruncS64(%v) = %d, want %d", v, int64(mem[3]), want)
			}
		}
	}
}

// TestMasmFMov copies doubles between registers, the VM's number cache
// registers F4 and F5 included, keeping every bit (NaN payloads, -0).
func TestMasmFMov(t *testing.T) {
	for _, bits := range []uint64{0, 1 << 63, math.Float64bits(-1.5), math.Float64bits(1e300), 0x7ff8dead0000beef} {
		mem := []uint64{bits, 0, 0}
		runMasm(t, mem, func(m *Masm) {
			m.Load64(S0, RRegs, 0)
			m.MovToF(1, S0)
			m.FMov(4, 1)
			m.FMov(5, 4)
			m.FMov(0, 5)
			m.FMov(0, 0)
			m.MovFromF(S1, 0)
			m.Store64(S1, RRegs, 8)
			m.MovFromF(S2, 4)
			m.Store64(S2, RRegs, 16)
		})
		if mem[1] != bits || mem[2] != bits {
			t.Errorf("FMov of %#x: %#x, %#x", bits, mem[1], mem[2])
		}
	}
}

// TestMasmJumpReg jumps through a table of absolute code addresses, filled
// after the code is placed (as switch jump tables are): each index stores
// its own value.
func TestMasmJumpReg(t *testing.T) {
	for idx := uint64(0); idx < 3; idx++ {
		var m Masm
		start, done := NewLabel(), NewLabel()
		cases := []*Label{NewLabel(), NewLabel(), NewLabel()}
		// On the heap: Call may grow the stack, which would move a table on
		// it while the code jumps through its old address.
		table := make([]uintptr, len(cases))
		tableSink = table
		m.EntryStub(start)
		m.Bind(start)
		m.Load64(S0, RRegs, 0)
		m.MovImm(S1, uint64(uintptr(unsafe.Pointer(&table[0]))))
		m.AddShift64(S1, S1, S0, 3)
		m.Load64(S1, S1, 0)
		m.JumpReg(S1)
		for i, l := range cases {
			m.Bind(l)
			m.MovImm(S2, uint64(100+i))
			m.Store64(S2, RRegs, 8)
			m.B(done)
		}
		m.Bind(done)
		m.Return(0)
		c, err := NewBytes(m.Code())
		if err != nil {
			t.Fatal(err)
		}
		for i, l := range cases {
			table[i] = c.Entry() + uintptr(m.LabelOffset(l))
		}
		heap := []uint64{idx, 0}
		masmSink = heap
		Call(c.Entry(), uintptr(unsafe.Pointer(&heap[0])), 0, 0, 0)
		c.KeepAlive()
		runtime.KeepAlive(table)
		masmSink = nil
		if heap[1] != 100+idx {
			t.Errorf("index %d: jumped to case %d", idx, int(heap[1])-100)
		}
	}
}

// TestMasmStoreZero128 clears 16 bytes at several offsets (on arm64 STP
// within its range and two stores beyond it) and nothing around them.
func TestMasmStoreZero128(t *testing.T) {
	for _, off := range []int{0, 8, 16, 496, 504, 512, 1024} {
		mem := make([]uint64, off/8+4)
		for i := range mem {
			mem[i] = ^uint64(i)
		}
		runMasm(t, mem, func(m *Masm) { m.StoreZero128(RRegs, off) })
		for i, v := range mem {
			want := ^uint64(i)
			if i == off/8 || i == off/8+1 {
				want = 0
			}
			if v != want {
				t.Fatalf("off %d: mem[%d] = %#x, want %#x", off, i, v, want)
			}
		}
	}
}
