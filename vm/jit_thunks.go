//go:build ((arm64 || amd64) && (darwin || linux)) || (amd64 && windows)

package vm

import (
	"math"
	"sync"
	"unsafe"

	"github.com/baslentfert/jsc2go/jit"
)

// Thunks (JSC's jit/ThunkGenerators.cpp): generated code for the common
// cases of builtins with an Intrinsic, shared by all generated code. A
// call whose callee is not in the call cache and is a native function
// with a thunk calls the thunk (nativeCall); a thunk that cannot handle
// its arguments fails, and the call takes the slow path (Go's call), as
// JSC's thunks tail-call the host function.
//
// Like JSC's, the thunks expect a number of arguments and handle only
// what needs no allocation and has no side effects; where JSC's take an
// int32 path and fail for doubles, ours (numbers are doubles here) work
// on doubles. A thunk is entered with CallThunk:
//
//	in:  S7 = the address of the call's this register (the arguments
//	     follow it), S6 = argc (this included)
//	out: S0 = 0 and the result in S1, S2; or S0 = 1 (the slow path)
//
// Thunks may clobber S0-S9, T0 and the F registers; they keep RRegs,
// RFrame and RMask. They are made once per process (they do not depend
// on a VM or realm) and live for its lifetime.

var (
	jitThunksOnce sync.Once
	// jitThunks are the thunks' entries by Intrinsic (0: none).
	jitThunks [len(intrinsicNames)]uintptr
	// jitThunkCode keeps the thunks' memory.
	jitThunkCode []*jit.Code
)

var (
	nativeFunctionItab = func() uintptr {
		var f functionImpl = (*nativeFunction)(nil)
		return (*[2]uintptr)(unsafe.Pointer(&f))[0]
	}()
	jsFunctionItab = func() uintptr {
		var f functionImpl = (*jsFunction)(nil)
		return (*[2]uintptr)(unsafe.Pointer(&f))[0]
	}()
	offNFIntrinsic   = int(unsafe.Offsetof(nativeFunction{}.intrinsic))
	smallStringsBase = uint64(uintptr(unsafe.Pointer(&smallStrings[0])))
)

// makeJITThunks makes the thunks (once).
func makeJITThunks() {
	jitThunksOnce.Do(func() {
		for intrinsic, gen := range thunkGenerators {
			t := &thunk{fail: jit.NewLabel()}
			gen(t)
			t.m.Bind(t.fail)
			t.m.MovImm(jit.S0, 1)
			t.m.ThunkReturn()
			code, err := jit.NewBytes(t.m.Code())
			if err != nil {
				continue // the calls take the slow path
			}
			jitThunkCode = append(jitThunkCode, code)
			jitThunks[intrinsic] = code.Entry()
		}
		for _, mega := range []struct {
			gen   func(*thunk)
			entry *uintptr
		}{{(*thunk).megaGet, &megaGetThunk}, {(*thunk).megaPut, &megaPutThunk}} {
			t := &thunk{fail: jit.NewLabel()}
			mega.gen(t)
			t.m.Bind(t.fail)
			t.m.MovImm(jit.S0, 1)
			t.m.ThunkReturn()
			if code, err := jit.NewBytes(t.m.Code()); err == nil {
				jitThunkCode = append(jitThunkCode, code)
				*mega.entry = code.Entry()
			}
		}
	})
}

// megaGetThunk and megaPutThunk are the shared code that looks a property
// up in a VM's megamorphic get_by_id or put_by_id cache (megaGetProbe,
// cachedPut).
var megaGetThunk, megaPutThunk uintptr

// megaGet looks own data property S8 (the name's atom) of object S0 up
// in megaGet table S7. Out: S0 = 0 and the value in S1, S2, or S0 = 1.
func (t *thunk) megaGet() {
	m := &t.m
	t.megaSlot(true, nil)
	m.Load64(jit.S1, jit.S6, 0)
	m.Load64(jit.S2, jit.S6, 8)
	m.MovImm(jit.S0, 0)
	m.ThunkReturn()
}

// megaPut stores S1, S2 to own writable data property S8 of object S0
// found in megaPut table S7, or adds it (an entry with newShape). Out:
// S0 = 0, or S0 = 1 if it is not there.
func (t *thunk) megaPut() {
	m := &t.m
	add := jit.NewLabel()
	t.megaSlot(false, add)
	m.Store64(jit.S1, jit.S6, 0)
	m.Store64(jit.S2, jit.S6, 8)
	m.MovImm(jit.S0, 0)
	m.ThunkReturn()
	// An add (S7 = the entry, S4 = its new shape): an extensible object,
	// not watched, with the entry's prototype while the epoch is current,
	// and room in its storage; then as putCase's add.
	m.Bind(add)
	m.Load8(jit.S5, jit.S0, offObjectExtensible)
	m.BranchZero(jit.S5, t.fail)
	m.Load8(jit.S5, jit.S0, offObjectWatched)
	m.BranchNonZero(jit.S5, t.fail)
	m.Load64(jit.S5, jit.S0, offProto)
	m.Load64(jit.S6, jit.S7, offMegaProto)
	m.Branch64(jit.NE, jit.S5, jit.S6, t.fail)
	m.Load64(jit.S5, jit.S7, offMegaEpochAt)
	m.Load64(jit.S5, jit.S5, 0)
	m.Load64(jit.S6, jit.S7, offMegaEpoch)
	m.Branch64(jit.NE, jit.S5, jit.S6, t.fail)
	m.Load64(jit.S5, jit.S0, offProps+16) // len
	m.Load64(jit.S6, jit.S0, offProps+24) // cap
	m.Branch64(jit.UGE, jit.S5, jit.S6, t.fail)
	m.Load64(jit.S6, jit.S0, offProps+8)
	m.AddShift64(jit.S6, jit.S6, jit.S5, 4)
	m.Store64(jit.S1, jit.S6, 0)
	m.Store64(jit.S2, jit.S6, 8)
	m.AddImm64(jit.S5, jit.S5, 1)
	m.Store64(jit.S5, jit.S0, offProps+16)
	m.Store64(jit.S4, jit.S0, offProps)
	m.MovImm(jit.S0, 0)
	m.ThunkReturn()
}

// megaSlot finds property S8 (the name's atom) of object S0 in table S7
// (megaGet's layout) and leaves the address of its value in S6, or fails:
// the same hash as megaGetHash, on the low 32 bits.
func (t *thunk) megaSlot(protos bool, add *jit.Label) {
	m := &t.m
	if unsafe.Sizeof(megaGetEntry{}) != 64 {
		panic("vm: megaGetEntry is not 64 bytes")
	}
	m.Load64(jit.S5, jit.S0, offProps) // the shape
	m.MovImm(jit.S4, 4)
	m.Alu32(jit.Shr32, jit.S6, jit.S5, jit.S4)
	m.MovImm(jit.S4, 3)
	m.Alu32(jit.Shr32, jit.S9, jit.S8, jit.S4)
	m.Alu32(jit.Xor32, jit.S6, jit.S6, jit.S9)
	m.Load64(jit.S9, jit.S0, offProto)
	m.MovImm(jit.S4, 4)
	m.Alu32(jit.Shr32, jit.S9, jit.S9, jit.S4)
	m.Alu32(jit.Xor32, jit.S6, jit.S6, jit.S9)
	m.MovImm(jit.S4, megaGetSize-1)
	m.Alu32(jit.And32, jit.S6, jit.S6, jit.S4)
	m.AddShift64(jit.S7, jit.S7, jit.S6, 6)
	m.Load64(jit.S4, jit.S7, offMegaShape)
	m.Branch64(jit.NE, jit.S4, jit.S5, t.fail)
	m.Load64(jit.S4, jit.S7, offMegaName)
	m.Branch64(jit.NE, jit.S4, jit.S8, t.fail)
	m.Load32(jit.S4, jit.S7, offMegaVersion)
	m.Load32(jit.S6, jit.S5, offShapeVer)
	m.Branch32(jit.NE, jit.S4, jit.S6, t.fail)
	// A prototype's property (get only): the receiver's prototype must be
	// the entry's and the epoch current; then the holder's vals.
	m.Load64(jit.S4, jit.S7, offMegaHolder)
	if !protos {
		m.BranchNonZero(jit.S4, t.fail)
		m.Load64(jit.S4, jit.S7, offMegaNewShape)
		m.BranchNonZero(jit.S4, add)
	} else {
		own, done, check := jit.NewLabel(), jit.NewLabel(), jit.NewLabel()
		// An absent property (index -1, no holder): undefinedSlot once
		// the prototype and the epoch are checked.
		m.BranchNonZero(jit.S4, check)
		m.Load32(jit.S9, jit.S7, offMegaIndex)
		m.MovImm(jit.S6, 0xFFFFFFFF)
		m.Branch32(jit.NE, jit.S9, jit.S6, own)
		m.MovImm(jit.S4, uint64(uintptr(unsafe.Pointer(&undefinedSlot))))
		m.Bind(check)
		m.Load64(jit.S6, jit.S0, offProto)
		m.Load64(jit.S9, jit.S7, offMegaProto)
		m.Branch64(jit.NE, jit.S6, jit.S9, t.fail)
		m.Load64(jit.S6, jit.S7, offMegaEpochAt)
		m.Load64(jit.S6, jit.S6, 0)
		m.Load64(jit.S9, jit.S7, offMegaEpoch)
		m.Branch64(jit.NE, jit.S6, jit.S9, t.fail)
		m.Load64(jit.S9, jit.S7, offMegaHolder)
		absent := jit.NewLabel()
		m.BranchZero(jit.S9, absent)
		m.Load32(jit.S9, jit.S7, offMegaIndex)
		m.Load64(jit.S6, jit.S4, offProps+8) // the holder's vals
		m.AddShift64(jit.S6, jit.S6, jit.S9, 4)
		m.B(done)
		m.Bind(absent)
		m.Mov(jit.S6, jit.S4) // &undefinedSlot
		m.B(done)
		m.Bind(own)
		m.Load32(jit.S4, jit.S7, offMegaIndex)
		m.Load64(jit.S6, jit.S0, offProps+8) // vals
		m.AddShift64(jit.S6, jit.S6, jit.S4, 4)
		m.Bind(done)
		return
	}
	m.Load32(jit.S4, jit.S7, offMegaIndex)
	m.Load64(jit.S6, jit.S0, offProps+8) // vals
	m.AddShift64(jit.S6, jit.S6, jit.S4, 4)
}

// thunkGenerators are JSC's thunkGeneratorForIntrinsic (runtime/VM.cpp)
// for the intrinsics a thunk helps with here, plus push and pop. exp and
// log (calls of C functions in JSC) would not help without calls from
// generated code into Go; random, clz32 and the bound function, Wasm and
// internal ones are left out.
var thunkGenerators = map[Intrinsic]func(*thunk){
	CharCodeAtIntrinsic:                 func(t *thunk) { t.stringChar(false, false, false) },
	CharAtIntrinsic:                     func(t *thunk) { t.stringChar(true, false, false) },
	StringPrototypeAtIntrinsic:          func(t *thunk) { t.stringChar(true, true, false) },
	StringPrototypeCodePointAtIntrinsic: func(t *thunk) { t.stringChar(false, false, true) },
	FromCharCodeIntrinsic:               func(t *thunk) { t.fromCode(true) },
	FromCodePointIntrinsic:              func(t *thunk) { t.fromCode(false) },
	SqrtIntrinsic:                       func(t *thunk) { t.unary(func() { t.m.FSqrt(0, 0) }) },
	AbsIntrinsic:                        func(t *thunk) { t.unary(func() { t.m.FAbs(0, 0) }) },
	FloorIntrinsic:                      func(t *thunk) { t.unary(func() { t.integral(roundFloor) }) },
	CeilIntrinsic:                       func(t *thunk) { t.unary(func() { t.integral(roundCeil) }) },
	TruncIntrinsic:                      func(t *thunk) { t.unary(func() { t.integral(roundTrunc) }) },
	RoundIntrinsic:                      func(t *thunk) { t.unary(func() { t.integral(roundNearest) }) },
	ArrayIsArrayIntrinsic:               (*thunk).isArray,
	MaxIntrinsic:                        func(t *thunk) { t.minMax(true) },
	MinIntrinsic:                        func(t *thunk) { t.minMax(false) },
	IMulIntrinsic:                       (*thunk).imul,
	GlobalIsNaNIntrinsic:                func(t *thunk) { t.numberTest(true, false) },
	NumberIsNaNIntrinsic:                func(t *thunk) { t.numberTest(false, false) },
	GlobalIsFiniteIntrinsic:             func(t *thunk) { t.numberTest(true, true) },
	NumberIsFiniteIntrinsic:             func(t *thunk) { t.numberTest(false, true) },
	NumberIsSafeIntegerIntrinsic:        (*thunk).isSafeInteger,
	ObjectIsIntrinsic:                   (*thunk).objectIs,
	NumberConstructorIntrinsic:          func(t *thunk) { t.identity(func(l *jit.Label) { t.m.Branch64Imm(jit.ULE, jit.S2, maxTag, l) }) },
	StringConstructorIntrinsic: func(t *thunk) {
		t.identity(func(l *jit.Label) { t.m.Branch64Imm(jit.NE, jit.S2, uint32(tagString), l) })
	},
	StringPrototypeSliceIntrinsic: (*thunk).stringSlice,
	ArrayPushIntrinsic:            (*thunk).arrayPush,
	ArrayPopIntrinsic:             (*thunk).arrayPop,
}

// thunk is a thunk being generated (JSC's SpecializedThunkJIT).
type thunk struct {
	m    jit.Masm
	fail *jit.Label
}

// expect fails unless the call has n arguments.
func (t *thunk) expect(n int) { t.m.Branch64Imm(jit.NE, jit.S6, uint32(n+1), t.fail) }

// arg is the offset from S7 of argument i (-1: this).
func arg(i int) int { return (i + 1) * valueSize }

// loadArg loads argument i (-1: this) into p, u.
func (t *thunk) loadArg(i int, p, u jit.R) {
	t.m.Load64(p, jit.S7, arg(i))
	t.m.Load64(u, jit.S7, arg(i)+8)
}

// loadNumber loads number argument i into d, or fails.
func (t *thunk) loadNumber(i int, d jit.F) {
	m := &t.m
	m.Load64(jit.S3, jit.S7, arg(i)+8)
	m.Branch64Imm(jit.ULE, jit.S3, maxTag, t.fail)
	m.Xor64(jit.S3, jit.S3, rMask)
	m.MovToF(d, jit.S3)
}

// loadInt32 loads argument i, a number that is an int32, into x (zero
// extended), or fails.
func (t *thunk) loadInt32(i int, x jit.R) {
	t.loadNumber(i, 6)
	t.m.TruncExactInt32(x, 6, t.fail)
	t.m.ZeroExt32(x, x)
}

func (t *thunk) returnValue(p, u jit.R) {
	m := &t.m
	if p != jit.S1 {
		m.Mov(jit.S1, p)
	}
	if u != jit.S2 {
		m.Mov(jit.S2, u)
	}
	m.MovImm(jit.S0, 0)
	m.ThunkReturn()
}

func (t *thunk) returnNumber(d jit.F) {
	m := &t.m
	m.MovFromF(jit.S2, d)
	m.Xor64(jit.S2, jit.S2, rMask)
	m.MovImm(jit.S1, 0)
	m.MovImm(jit.S0, 0)
	m.ThunkReturn()
}

func (t *thunk) returnInt32(x jit.R) {
	t.m.CvtS32(0, x)
	t.returnNumber(0)
}

func (t *thunk) returnBool(b bool) {
	m := &t.m
	m.MovImm(jit.S1, 0)
	m.MovImm(jit.S2, boolWord(b)+tagFalse)
	m.MovImm(jit.S0, 0)
	m.ThunkReturn()
}

func (t *thunk) returnConstant(v float64) {
	t.m.MovImm(jit.S3, math.Float64bits(v))
	t.m.MovToF(0, jit.S3)
	t.returnNumber(0)
}

// returnSmallString returns the one-character string for code unit x
// (below 256).
func (t *thunk) returnSmallString(x jit.R) {
	m := &t.m
	m.MovImm(jit.S3, smallStringsBase)
	m.AddShift64(jit.S3, jit.S3, x, 3)
	m.Load64(jit.S1, jit.S3, 0)
	m.MovImm(jit.S2, uint64(tagString))
	m.MovImm(jit.S0, 0)
	m.ThunkReturn()
}

// loadFlatThis loads this, a string that is not a rope, into S8, or
// fails.
func (t *thunk) loadFlatThis() {
	m := &t.m
	m.Load64(jit.S3, jit.S7, arg(-1)+8)
	m.Branch64Imm(jit.NE, jit.S3, uint32(tagString), t.fail)
	m.Load64(jit.S8, jit.S7, arg(-1))
	m.Load64(jit.S3, jit.S8, offStrLeft)
	m.BranchNonZero(jit.S3, t.fail)
}

// loadChar loads code unit S4 (an index below the length) of flat string
// S8 into S5.
func (t *thunk) loadChar() { loadChar(&t.m) }

// loadChar is thunk.loadChar (it clobbers S3).
func loadChar(m *jit.Masm) {
	u16, done := jit.NewLabel(), jit.NewLabel()
	m.Load8(jit.S3, jit.S8, offStrIsASCII)
	m.BranchZero(jit.S3, u16)
	m.Load64(jit.S3, jit.S8, offStrASCII)
	m.Add64(jit.S3, jit.S3, jit.S4)
	m.Load8(jit.S5, jit.S3, 0)
	m.B(done)
	m.Bind(u16)
	m.Load64(jit.S3, jit.S8, offStrU16)
	m.AddShift64(jit.S3, jit.S3, jit.S4, 1)
	m.Load16(jit.S5, jit.S3, 0)
	m.Bind(done)
}

// stringChar is JSC's stringCharLoad and the thunks on it: charCodeAt,
// charAt (asString), at (asString, relative: negative indexes count from
// the end) and codePointAt (codePoint: a surrogate pair is one code
// point). Strings of code units from 256 up and indexes out of range for
// charAt and at fail, as do ropes.
func (t *thunk) stringChar(asString, relative, codePoint bool) {
	m := &t.m
	t.expect(1)
	t.loadFlatThis()
	t.loadInt32(0, jit.S4)
	m.Load64(jit.S9, jit.S8, offStrLength)
	if relative {
		nonNegative := jit.NewLabel()
		m.Branch32Imm(jit.SGE, jit.S4, 0, nonNegative)
		m.Alu32(jit.Add32, jit.S4, jit.S4, jit.S9)
		m.ZeroExt32(jit.S4, jit.S4)
		m.Bind(nonNegative)
	}
	m.Branch64(jit.UGE, jit.S4, jit.S9, t.fail) // negative indexes too
	t.loadChar()
	if asString {
		m.Branch64Imm(jit.UGE, jit.S5, 256, t.fail)
		t.returnSmallString(jit.S5)
		return
	}
	if codePoint {
		// A lead surrogate followed by a trail surrogate in a UTF-16 string.
		done := jit.NewLabel()
		m.Load8(jit.S3, jit.S8, offStrIsASCII)
		m.BranchNonZero(jit.S3, done)
		m.AddImm64(jit.S4, jit.S4, 1)
		m.Branch64(jit.UGE, jit.S4, jit.S9, done)
		m.MovImm(jit.S6, 0xFC00)
		m.Alu32(jit.And32, jit.S3, jit.S5, jit.S6)
		m.Branch32Imm(jit.NE, jit.S3, 0xD800, done)
		m.Load64(jit.S3, jit.S8, offStrU16)
		m.AddShift64(jit.S3, jit.S3, jit.S4, 1)
		m.Load16(jit.S2, jit.S3, 0)
		m.Alu32(jit.And32, jit.S3, jit.S2, jit.S6)
		m.Branch32Imm(jit.NE, jit.S3, 0xDC00, done)
		// (lead << 10) + trail - U16_SURROGATE_OFFSET
		m.MovImm(jit.S6, 10)
		m.Alu32(jit.Shl32, jit.S5, jit.S5, jit.S6)
		m.Alu32(jit.Add32, jit.S5, jit.S5, jit.S2)
		m.MovImm(jit.S6, (0xD800<<10)+0xDC00-0x10000)
		m.Alu32(jit.Sub32, jit.S5, jit.S5, jit.S6)
		m.Bind(done)
	}
	t.returnInt32(jit.S5)
}

// stringSlice is String.prototype.slice of a flat ASCII string with one
// or two int32 arguments, where the result is empty, one character, the
// whole string, or at least a quarter of it, which Substring shares the
// text of: a String from the VM's pool (appendString's) gets the part's
// text and length. Other cases fail (Go copies small parts, so that they
// do not keep all of a big string alive).
func (t *thunk) stringSlice() {
	m := &t.m
	two := jit.NewLabel()
	t.loadFlatThis()
	m.Load8(jit.S3, jit.S8, offStrIsASCII)
	m.BranchZero(jit.S3, t.fail)
	m.Load64(jit.S9, jit.S8, offStrLength) // n
	m.MovImm(jit.S3, math.MaxInt32)
	m.Branch64(jit.UGT, jit.S9, jit.S3, t.fail)
	// S4 = start, S5 = end (relativeIndex of int32 arguments).
	relative := func(i int, d jit.R) {
		nonNegative, done := jit.NewLabel(), jit.NewLabel()
		t.loadNumber(i, 6)
		m.TruncExactInt32(d, 6, t.fail)
		m.ZeroExt32(d, d)
		m.Branch32Imm(jit.SGE, d, 0, nonNegative)
		m.Alu32(jit.Add32, d, d, jit.S9) // n + d, below n
		m.ZeroExt32(d, d)
		m.Branch32Imm(jit.SGE, d, 0, done)
		m.MovImm(d, 0)
		m.B(done)
		m.Bind(nonNegative)
		m.Branch64(jit.ULE, d, jit.S9, done)
		m.Mov(d, jit.S9)
		m.Bind(done)
	}
	m.Branch64Imm(jit.EQ, jit.S6, 3, two)
	m.Branch64Imm(jit.NE, jit.S6, 2, t.fail)
	relative(0, jit.S4)
	m.Mov(jit.S5, jit.S9)
	sliced := jit.NewLabel()
	m.B(sliced)
	m.Bind(two)
	relative(0, jit.S4)
	relative(1, jit.S5)
	m.Bind(sliced)
	empty, whole, single := jit.NewLabel(), jit.NewLabel(), jit.NewLabel()
	m.Branch64(jit.UGE, jit.S4, jit.S5, empty)
	m.Alu32(jit.Sub32, jit.S2, jit.S5, jit.S4) // the length
	m.ZeroExt32(jit.S2, jit.S2)
	m.Branch64(jit.EQ, jit.S2, jit.S9, whole)
	m.Branch64Imm(jit.EQ, jit.S2, 1, single)
	m.MovImm(jit.S3, 0)
	m.AddShift64(jit.S3, jit.S3, jit.S2, 2)
	m.Branch64(jit.ULT, jit.S3, jit.S9, t.fail) // a small part: Go copies it
	// A String from the pool, if it has one.
	m.Load64(jit.S3, rFrame, offFrameVM)
	m.Load64(jit.S3, jit.S3, offVMJitStrings)
	m.BranchZero(jit.S3, t.fail)
	m.Load64(jit.S6, jit.S3, offSPoolN)
	m.BranchZero(jit.S6, t.fail)
	m.SubImm64(jit.S6, jit.S6, 1)
	m.Store64(jit.S6, jit.S3, offSPoolN)
	m.AddShift64(jit.S3, jit.S3, jit.S6, 3)
	m.Load64(jit.S1, jit.S3, offSPoolS)
	m.StoreZero64(jit.S3, offSPoolS)
	m.Load64(jit.S3, jit.S8, offStrASCII)
	m.Add64(jit.S3, jit.S3, jit.S4)
	m.Store64(jit.S3, jit.S1, offStrASCII)
	m.Store64(jit.S2, jit.S1, offStrLength)
	m.MovImm(jit.S2, uint64(tagString))
	m.MovImm(jit.S0, 0)
	m.ThunkReturn()
	m.Bind(single)
	t.loadChar()
	t.returnSmallString(jit.S5)
	m.Bind(whole)
	m.Mov(jit.S1, jit.S8)
	m.MovImm(jit.S2, uint64(tagString))
	m.MovImm(jit.S0, 0)
	m.ThunkReturn()
	m.Bind(empty)
	m.MovImm(jit.S1, uint64(uintptr(unsafe.Pointer(emptyString))))
	m.MovImm(jit.S2, uint64(tagString))
	m.MovImm(jit.S0, 0)
	m.ThunkReturn()
}

var offVMJitStrings = int(unsafe.Offsetof(VM{}.jitStrings))

// fromCode is String.fromCharCode (charCode: ToUint16 of an int32) and
// String.fromCodePoint for one argument that gives a code unit below 256.
func (t *thunk) fromCode(charCode bool) {
	m := &t.m
	t.expect(1)
	t.loadInt32(0, jit.S4)
	if charCode {
		m.MovImm(jit.S3, 0xFFFF)
		m.Alu32(jit.And32, jit.S4, jit.S4, jit.S3)
	}
	m.Branch64Imm(jit.UGE, jit.S4, 256, t.fail)
	t.returnSmallString(jit.S4)
}

// unary is a Math function of one number (in F0) whose result op leaves
// in F0.
func (t *thunk) unary(op func()) {
	t.expect(1)
	t.loadNumber(0, 0)
	op()
	t.returnNumber(0)
}

type roundMode uint8

const (
	roundFloor roundMode = iota
	roundCeil
	roundTrunc
	roundNearest
)

// integral rounds F0 in place to an integer (Math.floor, ceil, trunc and
// round) without the rounding instructions SSE4.1 adds: NaN, the
// infinities, the zeros and numbers of magnitude 2^52 and up are
// integers already; the others go through an int64, and a zero result
// keeps the operand's sign. round is JSC's: ceil(x), less 1 if
// ceil(x) - 0.5 is more than x (ties go up). Uses F1-F3 and S3.
func (t *thunk) integral(mode roundMode) {
	m := &t.m
	constant := func(f jit.F, v float64) {
		m.MovImm(jit.S3, math.Float64bits(v))
		m.MovToF(f, jit.S3)
	}
	done := jit.NewLabel()
	m.FAbs(1, 0)
	constant(2, 1<<52)
	m.FBranch(jit.FNLT, 1, 2, done) // NaN too
	constant(2, 0)
	m.FBranch(jit.FEQ, 0, 2, done)
	m.TruncS64(jit.S3, 0)
	m.CvtS64(1, jit.S3) // F1 = trunc(x)
	adjusted := jit.NewLabel()
	switch mode {
	case roundFloor:
		m.FBranch(jit.FLE, 1, 0, adjusted)
		constant(3, 1)
		m.FArith(jit.FSub, 1, 1, 3)
	case roundCeil, roundNearest:
		m.FBranch(jit.FGE, 1, 0, adjusted)
		constant(3, 1)
		m.FArith(jit.FAdd, 1, 1, 3)
	}
	m.Bind(adjusted)
	// A zero result has the operand's sign: ceil(-0.5) and trunc(-0.5)
	// are -0.
	nonZero := jit.NewLabel()
	m.FBranch(jit.FNE, 1, 2, nonZero)
	m.FBranch(jit.FGT, 0, 2, nonZero)
	constant(1, math.Copysign(0, -1))
	m.Bind(nonZero)
	if mode == roundNearest {
		keep := jit.NewLabel()
		constant(3, 0.5)
		m.FArith(jit.FSub, 3, 1, 3)
		m.FBranch(jit.FLE, 3, 0, keep)
		constant(3, 1)
		m.FArith(jit.FSub, 1, 1, 3)
		m.Bind(keep)
	}
	m.MovFromF(jit.S3, 1)
	m.MovToF(0, jit.S3)
	m.Bind(done)
}

// minMax is Math.max and Math.min of two numbers: NaN if either is NaN,
// and of equal operands +0 for max(+0, -0) and -0 for min.
func (t *thunk) minMax(max bool) {
	m := &t.m
	t.expect(2)
	t.loadNumber(0, 0)
	t.loadNumber(1, 1)
	first, second, equal := jit.NewLabel(), jit.NewLabel(), jit.NewLabel()
	m.FBranchNaN(0, first)
	m.FBranchNaN(1, second)
	if max {
		m.FBranch(jit.FGT, 0, 1, first)
		m.FBranch(jit.FLT, 0, 1, second)
	} else {
		m.FBranch(jit.FLT, 0, 1, first)
		m.FBranch(jit.FGT, 0, 1, second)
	}
	m.Bind(equal)
	// Equal: the same bits but for +0 and -0.
	m.MovFromF(jit.S3, 0)
	m.MovFromF(jit.S4, 1)
	m.Branch64(jit.EQ, jit.S3, jit.S4, first)
	t.returnConstant(map[bool]float64{true: 0, false: math.Copysign(0, -1)}[max])
	m.Bind(second)
	t.returnNumber(1)
	m.Bind(first)
	t.returnNumber(0)
}

// isArray is Array.isArray: arrays are, other objects and primitives are
// not; proxies (IsArray looks at their target) take the slow path.
func (t *thunk) isArray() {
	m := &t.m
	t.expect(1)
	t.loadArg(0, jit.S1, jit.S2)
	yes, no := jit.NewLabel(), jit.NewLabel()
	m.Branch64Imm(jit.NE, jit.S2, uint32(tagObject), no)
	m.Load64(jit.S3, jit.S1, offExotic)
	m.MovImm(jit.S4, uint64(arrayObjectItab))
	m.Branch64(jit.EQ, jit.S3, jit.S4, yes)
	m.MovImm(jit.S4, uint64(proxyObjectItab))
	m.Branch64(jit.EQ, jit.S3, jit.S4, t.fail)
	m.Bind(no)
	t.returnBool(false)
	m.Bind(yes)
	t.returnBool(true)
}

// imul is Math.imul of two numbers below 2^63 in magnitude (ToInt32 is
// then the low 32 bits of the truncated int64).
func (t *thunk) imul() {
	m := &t.m
	t.expect(2)
	for i, x := range []jit.R{jit.S4, jit.S5} {
		t.loadNumber(i, 0)
		m.FAbs(1, 0)
		m.MovImm(jit.S3, math.Float64bits(1<<63))
		m.MovToF(2, jit.S3)
		m.FBranch(jit.FNLT, 1, 2, t.fail) // NaN too
		m.TruncS64(x, 0)
	}
	m.Alu32(jit.Mul32, jit.S4, jit.S4, jit.S5)
	t.returnInt32(jit.S4)
}

// numberTest is isNaN and isFinite (global: other values than numbers
// fail, to convert in Go) and Number.isNaN and Number.isFinite (other
// values are false).
func (t *thunk) numberTest(global, finite bool) {
	m := &t.m
	t.expect(1)
	notNumber, yes := jit.NewLabel(), jit.NewLabel()
	m.Load64(jit.S3, jit.S7, arg(0)+8)
	if global {
		m.Branch64Imm(jit.ULE, jit.S3, maxTag, t.fail)
	} else {
		m.Branch64Imm(jit.ULE, jit.S3, maxTag, notNumber)
	}
	m.Xor64(jit.S3, jit.S3, rMask)
	m.MovToF(0, jit.S3)
	if finite {
		m.FAbs(1, 0)
		m.MovImm(jit.S3, math.Float64bits(math.Inf(1)))
		m.MovToF(2, jit.S3)
		m.FBranch(jit.FLT, 1, 2, yes) // false for NaN
	} else {
		m.FBranchNaN(0, yes)
	}
	t.returnBool(false)
	m.Bind(yes)
	t.returnBool(true)
	if !global {
		m.Bind(notNumber)
		t.returnBool(false)
	}
}

// isSafeInteger is Number.isSafeInteger: an integral number of magnitude
// up to 2^53-1; other values are false.
func (t *thunk) isSafeInteger() {
	m := &t.m
	t.expect(1)
	no := jit.NewLabel()
	m.Load64(jit.S3, jit.S7, arg(0)+8)
	m.Branch64Imm(jit.ULE, jit.S3, maxTag, no)
	m.Xor64(jit.S3, jit.S3, rMask)
	m.MovToF(0, jit.S3)
	m.FAbs(1, 0)
	m.MovImm(jit.S3, math.Float64bits(1<<53-1))
	m.MovToF(2, jit.S3)
	m.FBranch(jit.FNLE, 1, 2, no) // NaN too
	m.TruncS64(jit.S3, 0)
	m.CvtS64(1, jit.S3)
	m.FBranch(jit.FNE, 0, 1, no)
	t.returnBool(true)
	m.Bind(no)
	t.returnBool(false)
}

// objectIs is Object.is (SameValue): numbers by value, NaN equal to NaN
// and +0 unequal to -0; other values the same kind and pointer. Strings
// and BigInts at different addresses fail (compared in Go).
func (t *thunk) objectIs() {
	m := &t.m
	t.expect(2)
	yes, no, numbers := jit.NewLabel(), jit.NewLabel(), jit.NewLabel()
	t.loadArg(0, jit.S2, jit.S3)
	t.loadArg(1, jit.S4, jit.S5)
	m.Branch64Imm(jit.UGT, jit.S3, maxTag, numbers)
	m.Branch64Imm(jit.UGT, jit.S5, maxTag, no)
	m.Branch64(jit.NE, jit.S3, jit.S5, no)
	m.Branch64(jit.EQ, jit.S2, jit.S4, yes)
	m.Branch64Imm(jit.EQ, jit.S3, uint32(tagString), t.fail)
	m.Branch64Imm(jit.EQ, jit.S3, uint32(tagBigInt), t.fail)
	m.B(no)
	m.Bind(numbers)
	m.Branch64Imm(jit.ULE, jit.S5, maxTag, no)
	m.Branch64(jit.EQ, jit.S3, jit.S5, yes) // the same bits
	m.Xor64(jit.S3, jit.S3, rMask)
	m.Xor64(jit.S5, jit.S5, rMask)
	m.MovToF(0, jit.S3)
	m.MovToF(1, jit.S5)
	nan := jit.NewLabel()
	m.FBranchNaN(0, nan)
	m.B(no)
	m.Bind(nan)
	m.FBranchNaN(1, yes)
	m.B(no)
	m.Bind(yes)
	t.returnBool(true)
	m.Bind(no)
	t.returnBool(false)
}

// identity is Number(x) for a number and String(x) for a string: x
// itself (isKind branches to its label for other values, with the
// argument's u word in S2).
func (t *thunk) identity(isNotKind func(*jit.Label)) {
	t.expect(1)
	t.loadArg(0, jit.S1, jit.S2)
	isNotKind(t.fail)
	t.returnValue(jit.S1, jit.S2)
}

// plainArrayThis loads this into S2 and its arrayObject into S3 if it is
// a plain array (see plainArrayCheck), with its length in S4; or fails.
func (t *thunk) plainArrayThis() {
	m := &t.m
	m.Load64(jit.S3, jit.S7, arg(-1)+8)
	m.Branch64Imm(jit.NE, jit.S3, uint32(tagObject), t.fail)
	m.Load64(jit.S2, jit.S7, arg(-1))
	plainArrayCheck(m, jit.S2, jit.S3, [4]jit.R{jit.S4, jit.S5, jit.S8, jit.S9}, t.fail)
}

// plainArrayCheck loads the arrayObject of object o into a if it is an
// array that passes arrayChainCheck, with a writable length that is that
// of its elements (in tmp[0]); or branches to fail. It clobbers tmp.
func plainArrayCheck(m *jit.Masm, o, a jit.R, tmp [4]jit.R, fail *jit.Label) {
	arrayChainCheck(m, o, a, tmp, fail)
	m.Load8(tmp[0], a, offArrayLengthWritable)
	m.BranchZero(tmp[0], fail)
	m.Load32(tmp[0], a, offArrayLength)
	m.Load64(tmp[1], o, offElements+8)
	m.Branch64(jit.NE, tmp[0], tmp[1], fail)
}

// arrayChainCheck loads the arrayObject of object o into a if it is an
// array with no sparse indexes (no extra) whose prototypes have ordinary
// indexes and no indexed properties (no elements and no extra), as
// mayHaveIndexedProperties checks (JSC's arrays while the global object
// is not having a bad time): then its indexes are its elements, and holes
// read undefined. Otherwise it branches to fail. It clobbers tmp.
func arrayChainCheck(m *jit.Masm, o, a jit.R, tmp [4]jit.R, fail *jit.Label) {
	m.Load64(a, o, offExotic)
	m.MovImm(tmp[0], uint64(arrayObjectItab))
	m.Branch64(jit.NE, a, tmp[0], fail)
	m.Load64(a, o, offExotic+8) // *arrayObject
	m.Load64(tmp[0], o, offExtra)
	m.BranchNonZero(tmp[0], fail)
	// The prototypes (tmp[2]).
	loop, ordinary, done := jit.NewLabel(), jit.NewLabel(), jit.NewLabel()
	m.Load64(tmp[2], o, offProto)
	m.Bind(loop)
	m.BranchZero(tmp[2], done)
	m.Load64(tmp[0], tmp[2], offExotic)
	m.BranchZero(tmp[0], ordinary)
	m.MovImm(tmp[1], uint64(arrayObjectItab))
	m.Branch64(jit.EQ, tmp[0], tmp[1], ordinary)
	m.MovImm(tmp[1], uint64(immutablePrototypeItab))
	m.Branch64(jit.NE, tmp[0], tmp[1], fail)
	m.Bind(ordinary)
	m.Load64(tmp[0], tmp[2], offElements+8)
	m.BranchNonZero(tmp[0], fail)
	m.Load64(tmp[0], tmp[2], offExtra)
	m.BranchNonZero(tmp[0], fail)
	m.Load64(tmp[2], tmp[2], offProto)
	m.B(loop)
	m.Bind(done)
}

// arrayPush is Array.prototype.push(x) on a plain array that is
// extensible and whose elements have room for one more (Go grows them).
func (t *thunk) arrayPush() {
	m := &t.m
	t.expect(1)
	t.plainArrayThis()
	m.Load8(jit.S5, jit.S2, offObjectExtensible)
	m.BranchZero(jit.S5, t.fail)
	m.Load64(jit.S5, jit.S2, offElements+16) // cap
	m.Branch64(jit.UGE, jit.S4, jit.S5, t.fail)
	m.MovImm(jit.S5, 0xFFFFFFFE)
	m.Branch64(jit.UGE, jit.S4, jit.S5, t.fail)
	m.Load64(jit.S5, jit.S2, offElements)
	m.AddShift64(jit.S5, jit.S5, jit.S4, 4)
	t.loadArg(0, jit.S8, jit.S9)
	m.Store64(jit.S8, jit.S5, 0)
	m.Store64(jit.S9, jit.S5, 8)
	m.AddImm64(jit.S4, jit.S4, 1)
	m.Store64(jit.S4, jit.S2, offElements+8)
	m.Store32(jit.S4, jit.S3, offArrayLength)
	m.CvtS64(0, jit.S4)
	t.returnNumber(0)
}

// arrayPop is Array.prototype.pop() on a plain array whose last element
// is not a hole.
func (t *thunk) arrayPop() {
	m := &t.m
	t.expect(0)
	t.plainArrayThis()
	m.BranchZero(jit.S4, t.fail)
	m.SubImm64(jit.S4, jit.S4, 1)
	m.Load64(jit.S5, jit.S2, offElements)
	m.AddShift64(jit.S5, jit.S5, jit.S4, 4)
	m.Load64(jit.S8, jit.S5, 0)
	m.Load64(jit.S9, jit.S5, 8)
	m.Branch64Imm(jit.EQ, jit.S9, uint32(tagEmpty), t.fail)
	m.StoreZero64(jit.S5, 0)
	m.StoreZero64(jit.S5, 8)
	m.Store64(jit.S4, jit.S2, offElements+8)
	m.Store32(jit.S4, jit.S3, offArrayLength)
	t.returnValue(jit.S8, jit.S9)
}
