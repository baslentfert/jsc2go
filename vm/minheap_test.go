package vm

import (
	"runtime"
	"runtime/debug"
	"testing"
)

// TestMinHeap checks that a minimum heap makes collections rarer for a
// small live heap and that turning it off restores GOGC's percentage.
func TestMinHeap(t *testing.T) {
	before := debug.SetGCPercent(100)
	debug.SetGCPercent(before)
	cycles := func() uint32 {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		n := ms.NumGC
		var keep []*[64]byte
		for range 400000 { // about 26 MB of garbage
			keep = append(keep, new([64]byte))
			if len(keep) > 100 {
				keep = keep[:0]
			}
		}
		runtime.ReadMemStats(&ms)
		return ms.NumGC - n
	}
	runtime.GC()
	without := cycles()
	SetMinHeap(64 << 20)
	runtime.GC() // a cycle sets the percentage
	runtime.GC()
	with := cycles()
	SetMinHeap(0)
	if after := debug.SetGCPercent(before); after != before {
		t.Errorf("GC percent after SetMinHeap(0) = %d, want %d", after, before)
	}
	if with >= without || with > 2 {
		t.Errorf("collections with a 64 MB minimum heap: %d, without: %d", with, without)
	}
}
