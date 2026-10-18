package vm

import (
	"os"
	"path/filepath"
	"testing"
)

// BenchmarkLoad measures loading a large program: parsing, compiling and
// running its top level, which mostly defines functions (JetStream2's
// sources, in the WebKit checkout).
func BenchmarkLoad(b *testing.B) {
	root := filepath.Join("..", "webkit", "PerformanceTests", "JetStream2")
	for _, name := range []string{"Octane/typescript-compiler.js", "Octane/pdfjs.js"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			b.Skip("JetStream2 not checked out")
		}
		src := string(data)
		b.Run(filepath.Base(name), func(b *testing.B) {
			b.SetBytes(int64(len(src)))
			for b.Loop() {
				v := New()
				if _, err := v.Eval(src, name); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
