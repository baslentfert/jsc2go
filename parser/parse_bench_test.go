package parser

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"unicode/utf16"
)

// Real sources from JetStream2 (in the WebKit checkout, scripts/setup.sh):
// a parser benchmark in MB/s.
var benchSources = []string{
	"Octane/typescript-compiler.js",
	"Octane/pdfjs.js",
	"code-load/inspector-payload-minified.js",
	"Octane/mandreel.js",
}

func BenchmarkParseProgram(b *testing.B) {
	root := filepath.Join("..", "webkit", "PerformanceTests", "JetStream2")
	for _, name := range benchSources {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			b.Skip("JetStream2 not checked out")
		}
		src := string(data)
		b.Run(filepath.Base(name), func(b *testing.B) {
			b.SetBytes(int64(len(src)))
			for b.Loop() {
				code := NewSourceCode(NewSourceProvider(src, name, SourceTypeProgram))
				if _, err := ParseProgram(code, NoLexicallyScopedFeatures); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestEncodeUTF16(t *testing.T) {
	// encodeUTF16 must equal utf16.Encode([]rune(s)), invalid UTF-8 included.
	cases := []string{"", "abc", "héllo", "日本語", "😀x", "\xff", "a\xffb", "\xed\xa0\x80", "\xf0\x9f", "\xc3",
		"x\U0010FFFFy", "  ", string([]byte{0xe2, 0x82}), "tab\tnl\n\x00"}
	for i := 0; i < 200; i++ {
		b := make([]byte, i%17)
		for j := range b {
			b[j] = byte(i*31 + j*97)
		}
		cases = append(cases, string(b))
	}
	for _, s := range cases {
		want := utf16.Encode([]rune(s))
		if got := encodeUTF16(s); !slices.Equal(got, want) {
			t.Errorf("encodeUTF16(%q) = %v, want %v", s, got, want)
		}
	}
}
