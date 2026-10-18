//go:build arm64 && (darwin || linux)

package jit

import (
	"runtime"
	"testing"
)

func TestCallAdd(t *testing.T) {
	// add x0, x0, x1; ret
	c, err := New([]uint32{0x8b010000, 0xd65f03c0})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		if got := Call(c.Entry(), 40, 2, 0, 0); got != 42 {
			t.Fatalf("got %d", got)
		}
	}
	c.KeepAlive()
	runtime.GC()
}
