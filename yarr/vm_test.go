package yarr

import (
	"math/rand"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf16"
)

// TestVMDifferential compares the program matcher (vm.go) with the tree
// matcher on random patterns: groups, quantifiers greedy and lazy,
// alternatives, lookaround in both directions, back references, named
// groups and modifiers, with every flag combination that compiles.
func TestVMDifferential(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	atoms := []string{"a", "b", "ab", "abc", ".", `\d`, `\w`, `\s`, "[a-c]", "[^ab]", "x", `\.`, "é", `\u{1F600}`,
		`\b`, `\B`, "^", "$", "(?=a)", "(?!b)", "(?<=a)", "(?<!b)", `\1`, `\2`, `[\s\S]`, "(?<n>a|b)", `\k<n>`,
		"(?i:a)", "(?m:^)", "A", "(?:)", "()"}
	quant := []string{"", "", "", "*", "+", "?", "{2}", "{1,3}", "{0,2}", "*?", "+?", "??", "{2,}?", "{1,2}?"}
	var gen func(depth int) string
	gen = func(depth int) string {
		var b strings.Builder
		for n := 1 + r.Intn(4); n > 0; n-- {
			switch k := r.Intn(10); {
			case k < 5 || depth > 2:
				b.WriteString(atoms[r.Intn(len(atoms))])
			case k < 7:
				b.WriteString("(" + gen(depth+1) + ")")
			case k < 8:
				b.WriteString("(?<=" + gen(depth+1) + ")")
			default:
				b.WriteString("(?:" + gen(depth+1) + "|" + gen(depth+1) + ")")
			}
			b.WriteString(quant[r.Intn(len(quant))])
		}
		if r.Intn(4) == 0 {
			b.WriteString("|" + gen(depth+1))
		}
		return b.String()
	}
	alphabet := []rune("aabbcxA./\" é\n1😀")
	n := 3000
	if v, err := strconv.Atoi(os.Getenv("YARR_FUZZ_N")); err == nil {
		n = v
	}
	valid, compiled := 0, 0
	for i := 0; i < n; i++ {
		src := gen(0)
		flags := []string{"", "i", "u", "m", "iu", "s", "y", "im", "v", "iv"}[r.Intn(10)]
		fl, _ := ParseFlags(flags)
		p, err := CompilePattern(utf16.Encode([]rune(src)), fl)
		if err != NoError {
			continue
		}
		if !needsProgram(p.body) {
			continue
		}
		valid++
		if compileProgram(p) == nil {
			continue
		}
		p.execs.Store(2) // compile at the first Exec
		compiled++
		var in []rune
		for k := r.Intn(30); k > 0; k-- {
			in = append(in, alphabet[r.Intn(len(alphabet))])
		}
		u := utf16.Encode(in)
		latin1 := !slices.ContainsFunc(u, func(c uint16) bool { return c > 0xFF })
		l := make([]byte, len(u))
		for j, c := range u {
			l[j] = byte(c)
		}
		for start := 0; start <= len(u); start += 1 + r.Intn(4) {
			vmEnabled = true
			a, ea := Exec(p, u, start)
			a = slices.Clone(a)
			vmEnabled = false
			b, eb := Exec(p, u, start)
			vmEnabled = true
			if ea == ErrHitLimit || eb == ErrHitLimit {
				continue // the matchers count steps differently
			}
			if !slices.Equal(a, b) || (ea == nil) != (eb == nil) {
				t.Fatalf("/%s/%s on %q from %d: program %v %v, tree %v %v", src, flags, string(in), start, a, ea, b, eb)
			}
			if latin1 {
				a, _ := Exec(p, l, start)
				a = slices.Clone(a)
				vmEnabled = false
				b, _ := Exec(p, l, start)
				vmEnabled = true
				if !slices.Equal(a, b) {
					t.Fatalf("/%s/%s on Latin-1 %q from %d: program %v, tree %v", src, flags, string(in), start, a, b)
				}
			}
		}
	}
	if compiled < valid*9/10 {
		t.Fatalf("only %d of %d valid patterns compiled to programs", compiled, valid)
	}
}

// BenchmarkMatchers runs typical patterns with the program matcher and the
// tree matcher.
func BenchmarkMatchers(b *testing.B) {
	cases := []struct{ re, flags, in string }{
		{`(\d+)-(\d+)-(\d+)`, "", "date: 2026-10-14, other text here 12-34-56 end"},
		{`^\s*(\w+)\s*=\s*(.*)$`, "m", "  name = some value with spaces\nkey=value"},
		{`[a-z]+@[a-z]+\.(com|org)`, "i", "contact us at Someone@Example.org or later"},
		{`(?:<(\w+)[^>]*>)(.*?)</\1>`, "", "text <b class=x>bold <i>it</i></b> more"},
		{`\b(\w)(\w*)\b`, "g", "the quick brown fox jumps over the lazy dog"},
		{`(a|b)*c`, "", "abababababababababababababc"},
		{`\bq3\b`, "g", strings.Repeat("var a1=q3(b2,c4)+q3x; function q3(z){return z} ", 20)},
		{`\b\w+\b`, "g", strings.Repeat("eval function p a c k e r ", 10)},
	}
	for _, c := range cases {
		fl, _ := ParseFlags(c.flags)
		p, err := CompilePattern(utf16.Encode([]rune(c.re)), fl)
		if err != NoError {
			b.Fatal(c.re, err)
		}
		in := []byte(c.in)
		for _, vm := range []bool{true, false} {
			name := "tree"
			if vm {
				name = "program"
			}
			b.Run(c.re+"/"+name, func(b *testing.B) {
				vmEnabled = vm
				defer func() { vmEnabled = true }()
				b.ReportAllocs()
				for b.Loop() {
					for pos := 0; pos <= len(in); {
						caps, _ := Exec(p, in, pos)
						if caps == nil {
							break
						}
						pos = max(caps[1], pos+1)
					}
				}
			})
		}
	}
}
