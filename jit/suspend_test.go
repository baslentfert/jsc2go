//go:build ((arm64 || amd64) && (darwin || linux)) || (amd64 && windows)

package jit

import (
	"testing"
	"unsafe"
)

// TestStateLayout checks the offsets CallS's assembly uses.
func TestStateLayout(t *testing.T) {
	var st State
	got := []uintptr{unsafe.Offsetof(st.Base), unsafe.Offsetof(st.SP), unsafe.Offsetof(st.Word),
		unsafe.Offsetof(st.Owner), unsafe.Offsetof(st.Frame), unsafe.Offsetof(st.Regs), unsafe.Offsetof(st.Resume),
		unsafe.Offsetof(st.LR), unsafe.Offsetof(st.Ret)}
	for i, o := range got {
		if o != uintptr(8*i) {
			t.Fatalf("State field %d at %d, CallS expects %d", i, o, 8*i)
		}
	}
}
