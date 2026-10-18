//go:build amd64 && windows

package jit

import (
	"fmt"
	"syscall"
	"unsafe"
)

// Windows has no mmap: executable memory comes from VirtualAlloc, and
// VirtualProtect switches it from writable to executable (never both).
var (
	kernel32           = syscall.NewLazyDLL("kernel32.dll")
	procVirtualAlloc   = kernel32.NewProc("VirtualAlloc")
	procVirtualProtect = kernel32.NewProc("VirtualProtect")
	procVirtualFree    = kernel32.NewProc("VirtualFree")
	procFlushICache    = kernel32.NewProc("FlushInstructionCache")
)

const (
	memCommit       = 0x1000
	memReserve      = 0x2000
	memRelease      = 0x8000
	pageReadWrite   = 0x04
	pageExecuteRead = 0x20
)

func allocExec(code []byte) ([]byte, error) {
	size := uintptr(len(code))
	addr, _, err := procVirtualAlloc.Call(0, size, memCommit|memReserve, pageReadWrite)
	if addr == 0 {
		return nil, fmt.Errorf("jit: VirtualAlloc: %w", err)
	}
	// The memory is not managed by Go, so converting the address is safe.
	mem := unsafe.Slice((*byte)(*(*unsafe.Pointer)(unsafe.Pointer(&addr))), len(code))
	copy(mem, code)
	var old uint32
	if ok, _, err := procVirtualProtect.Call(addr, size, pageExecuteRead, uintptr(unsafe.Pointer(&old))); ok == 0 {
		freeExec(mem)
		return nil, fmt.Errorf("jit: VirtualProtect: %w", err)
	}
	// Not needed on x86, but what Windows documents for code written at run time.
	process, _ := syscall.GetCurrentProcess()
	procFlushICache.Call(uintptr(process), addr, size)
	return mem, nil
}

func freeExec(mem []byte) {
	procVirtualFree.Call(uintptr(unsafe.Pointer(unsafe.SliceData(mem))), 0, memRelease)
}
