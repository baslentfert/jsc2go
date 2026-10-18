package vm

import (
	"strings"
	"sync"
	"testing"
)

// TestWeakMapSharedSymbolsConcurrentVMs uses well-known symbols, which all
// VMs share, as WeakMap and WeakSet keys from VMs on several goroutines at
// once: each VM keeps its own entries for them (sharedEphemerons), so
// they neither race on one map nor see each other's entries.
func TestWeakMapSharedSymbolsConcurrentVMs(t *testing.T) {
	const src = `
		var syms = [Symbol.iterator, Symbol.hasInstance, Symbol.toPrimitive, Symbol.species];
		var out = 0;
		for (var r = 0; r < 3000; r++) {
			var wm = new WeakMap(), ws = new WeakSet();
			for (var i = 0; i < syms.length; i++) { wm.set(syms[i], r + i); ws.add(syms[i]); }
			for (var i = 0; i < syms.length; i++) {
				if (wm.get(syms[i]) !== r + i || !ws.has(syms[i])) out = -1e9;
				if (r % 2) { wm.delete(syms[i]); ws.delete(syms[i]); }
			}
			out += wm.has(Symbol.iterator) ? 1 : 0;
		}
		print(out);`
	var wg sync.WaitGroup
	results := make([]string, 8)
	for g := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v := New()
			var out strings.Builder
			v.Print = func(s string) { out.WriteString(s) }
			if _, err := v.Eval(src, "shared.js"); err != nil {
				out.WriteString("error: " + err.Error())
			}
			results[g] = out.String()
		}()
	}
	wg.Wait()
	for g, r := range results {
		if r != "1500" {
			t.Errorf("VM %d printed %q, want 1500", g, r)
		}
	}
}
