package vm

import (
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"sync"
)

// SetMinHeap makes Go's collector wait until the heap has grown to at least
// n bytes before it starts a cycle, and use GOGC's percentage above that.
// It applies to the whole process (Go has one collector); 0 turns it off.
//
// Go's pacer starts a cycle when the heap reaches GOGC percent above the
// live heap, which for the few megabytes a script keeps alive means a cycle
// every few megabytes allocated: base64 and the date-format tests ran 170
// to 490 cycles in one or two seconds. While the collector marks, generated
// code is not entered (it writes no barriers), so those scripts ran in the
// interpreter up to a fifth of the time. A minimum heap makes such cycles
// rare; heaps above the minimum are paced as before. Unlike a ballast
// allocation it costs no memory beyond the garbage it lets accumulate (a
// ballast's pages are touched on Windows).
func SetMinHeap(n uint64) {
	minHeap.Lock()
	defer minHeap.Unlock()
	minHeap.bytes = n
	if !minHeap.started && n > 0 {
		minHeap.started = true
		minHeap.base = debug.SetGCPercent(100)
		debug.SetGCPercent(minHeap.base)
		armMinHeap()
	}
	adjustMinHeap()
}

var minHeap struct {
	sync.Mutex
	bytes   uint64
	base    int // GOGC when SetMinHeap was first called
	percent int // the percentage set last
	started bool
	samples [3]metrics.Sample
}

// armMinHeap runs adjustMinHeap after the next collection: a finalizer on
// an unreachable object runs once a cycle has found it unreachable.
func armMinHeap() {
	x := new([16]byte)
	runtime.SetFinalizer(x, func(*[16]byte) {
		minHeap.Lock()
		adjustMinHeap()
		on := minHeap.bytes > 0
		minHeap.Unlock()
		if on {
			armMinHeap()
		}
	})
}

// adjustMinHeap sets the GC percentage so that the next cycle starts at
// the larger of the minimum heap and the base percentage above the live
// heap. Go's heap goal is live + (live + stacks + globals) * percent / 100,
// so the percentage is computed against all three. minHeap must be locked.
func adjustMinHeap() {
	p := minHeap.base
	if minHeap.bytes > 0 && p > 0 {
		s := &minHeap.samples
		s[0].Name = "/gc/heap/live:bytes"
		s[1].Name = "/gc/scan/stack:bytes"
		s[2].Name = "/gc/scan/globals:bytes"
		metrics.Read(s[:])
		var v [3]uint64
		for i := range s {
			if s[i].Value.Kind() == metrics.KindUint64 {
				v[i] = s[i].Value.Uint64()
			}
		}
		// Before the first cycle the scan sizes are not known yet (zero),
		// while the pacer uses the real ones: leave the percentage then.
		live, roots := v[0], v[0]+v[1]+v[2]
		if v[1] != 0 && v[2] != 0 {
			if goal := live + roots*uint64(p)/100; goal < minHeap.bytes {
				p = int(min((minHeap.bytes-live)*100/roots, 10000))
			}
		}
	}
	if p != minHeap.percent {
		minHeap.percent = p
		debug.SetGCPercent(p)
	}
}
