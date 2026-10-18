//go:build (arm64 || amd64) && (darwin || linux)

package jit

import "syscall"

// allocExec maps pages, copies code into them and makes them executable
// (never writable and executable at once).
func allocExec(code []byte) ([]byte, error) {
	page := syscall.Getpagesize()
	size := (len(code) + page - 1) &^ (page - 1)
	mem, err := syscall.Mmap(-1, 0, size, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_PRIVATE|syscall.MAP_ANON)
	if err != nil {
		return nil, err
	}
	copy(mem, code)
	if err := syscall.Mprotect(mem, syscall.PROT_READ|syscall.PROT_EXEC); err != nil {
		syscall.Munmap(mem)
		return nil, err
	}
	return mem, nil
}

func freeExec(mem []byte) { syscall.Munmap(mem) }
