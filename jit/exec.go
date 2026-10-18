//go:build ((arm64 || amd64) && (darwin || linux)) || (amd64 && windows)

// Package jit holds the machine-level parts of jsc2go's numeric JIT tier:
// executable memory and the call into generated code. The code generator
// itself lives in package vm, next to the interpreter whose frames it uses.
package jit

import (
	"runtime"
	"unsafe"
	_ "unsafe" // linkname
)

// Supported reports whether this platform can run generated code.
const Supported = true

// Code is a block of executable machine code.
type Code struct {
	mem []byte
}

// New copies arm64 instruction words into fresh executable memory (see
// NewBytes).
func New(insns []uint32) (*Code, error) {
	return NewBytes(unsafe.Slice((*byte)(unsafe.Pointer(unsafe.SliceData(insns))), 4*len(insns)))
}

// NewBytes copies machine code into fresh executable memory. The memory is
// released when the Code is collected.
func NewBytes(code []byte) (*Code, error) {
	mem, err := allocExec(code)
	if err != nil {
		return nil, err
	}
	c := &Code{mem: mem}
	runtime.AddCleanup(c, freeExec, mem)
	return c, nil
}

// Entry is the address of the first instruction.
func (c *Code) Entry() uintptr { return uintptr(unsafe.Pointer(&c.mem[0])) }

// Call runs generated code at entry with R0-R3 set to the arguments and
// returns R0. Memory passed by address must not be on the goroutine stack:
// Call may grow (move) it before the code runs. The code must not touch R18 (platform), R28 (g), R29 or the
// stack, and must return with RET.
func Call(entry uintptr, a0, a1, a2, a3 uintptr) uintptr

// KeepAlive keeps c (its memory) alive up to this point.
func (c *Code) KeepAlive() { runtime.KeepAlive(c) }

// writeBarrier is the runtime's write barrier flag (the runtime makes it
// available to other packages with linkname).
//
//go:linkname writeBarrier runtime.writeBarrier
var writeBarrier struct {
	enabled bool
	pad     [3]byte
	alignme uint64
}

// BarrierEnabled reports whether the garbage collector is marking, when
// pointer writes need its write barrier. The flag only changes while the
// world is stopped, which cannot happen while a goroutine runs generated
// code (it is not preemptible): a value read before Call holds throughout.
func BarrierEnabled() bool { return writeBarrier.enabled }
