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

// TestPrefiltersDifferential matches random patterns against random inputs
// with and without the prefilters.
func TestPrefiltersDifferential(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	atoms := []string{"a", "b", "ab", "abc", ".", "\\d", "\\w", "\\s", "[a-c]", "[^ab]", "x", "\\.", "\"", "\\/",
		"é", "\\u{1F600}", "\\b", "^", "$", "(?=a)", "(?!b)", "(?<=a)", "\\1", "[\\s\\S]"}
	quant := []string{"", "", "", "*", "+", "?", "{2}", "{1,3}", "*?", "+?"}
	var gen func(depth int) string
	gen = func(depth int) string {
		var b strings.Builder
		for n := 1 + r.Intn(4); n > 0; n-- {
			switch k := r.Intn(10); {
			case k < 6 || depth > 2:
				b.WriteString(atoms[r.Intn(len(atoms))])
			case k < 8:
				b.WriteString("(" + gen(depth+1) + ")")
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
	alphabet := []rune("aabbcx./\" é\n1😀")
	n := 2000
	if v, err := strconv.Atoi(os.Getenv("YARR_FUZZ_N")); err == nil {
		n = v
	}
	checked := 0
	for i := 0; i < n; i++ {
		src := gen(0)
		flags := []string{"", "i", "u", "m", "iu", "s"}[r.Intn(6)]
		fl, _ := ParseFlags(flags)
		p, err := CompilePattern(utf16.Encode([]rune(src)), fl)
		if err != NoError {
			continue
		}
		var in []rune
		for n := r.Intn(40); n > 0; n-- {
			in = append(in, alphabet[r.Intn(len(alphabet))])
		}
		u := utf16.Encode(in)
		latin1 := true
		for _, c := range u {
			if c > 0xFF {
				latin1 = false
			}
		}
		for start := 0; start <= len(u); start += 1 + r.Intn(5) {
			check := func(a, b []int, ea, eb error) {
				if ea == ErrHitLimit || eb == ErrHitLimit {
					return // skipping positions can keep a match under the step limit
				}
				if !slices.Equal(a, b) || (ea == nil) != (eb == nil) {
					t.Fatalf("/%s/%s on %q from %d: with prefilters %v %v, without %v %v", src, flags, string(in), start, a, ea, b, eb)
				}
			}
			prefiltersEnabled = true
			a, ea := Exec(p, u, start)
			a = slices.Clone(a)
			prefiltersEnabled = false
			b, eb := Exec(p, u, start)
			prefiltersEnabled = true
			check(a, b, ea, eb)
			if latin1 {
				l := make([]byte, len(u))
				for j, c := range u {
					l[j] = byte(c)
				}
				prefiltersEnabled = true
				a, ea := Exec(p, l, start)
				a = slices.Clone(a)
				prefiltersEnabled = false
				b, eb := Exec(p, l, start)
				prefiltersEnabled = true
				check(a, b, ea, eb)
			}
			checked++
		}
	}
	t.Logf("%d matches compared", checked)
}

// TestSetAlternativesDifferential compares the set-alternatives path
// (Pattern.sets) with the backtracking matcher on patterns made only of
// characters and classes, with i, y, m and s.
func TestSetAlternativesDifferential(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	atoms := []string{"a", "b", "c", "A", "x", "é", "[ab]", "[^ab]", "[a-c]", "[B-D]", ".", `[\s\S]`, `\d`, `\W`, `\.`, "-", `[^\s\S]`}
	alphabet := []rune("aAbBcCdx-.é\n1")
	n := 3000
	if v, err := strconv.Atoi(os.Getenv("YARR_FUZZ_N")); err == nil {
		n = v
	}
	used := 0
	for i := 0; i < n; i++ {
		var b strings.Builder
		for alts := 1 + r.Intn(4); alts > 0; alts-- {
			for k := r.Intn(5); k > 0; k-- {
				b.WriteString(atoms[r.Intn(len(atoms))])
			}
			if alts > 1 {
				b.WriteString("|")
			}
		}
		src := b.String()
		flags := []string{"", "i", "y", "iy", "m", "s", "is"}[r.Intn(7)]
		fl, _ := ParseFlags(flags)
		p, err := CompilePattern(utf16.Encode([]rune(src)), fl)
		if err != NoError {
			continue
		}
		if p.sets != nil {
			used++
		}
		var in []rune
		for k := r.Intn(30); k > 0; k-- {
			in = append(in, alphabet[r.Intn(len(alphabet))])
		}
		u := utf16.Encode(in)
		for start := 0; start <= len(u); start++ {
			prefiltersEnabled = true
			a, _ := Exec(p, u, start)
			a = slices.Clone(a)
			prefiltersEnabled = false
			c, _ := Exec(p, u, start)
			prefiltersEnabled = true
			if !slices.Equal(a, c) {
				t.Fatalf("/%s/%s on %q from %d: sets %v, matcher %v", src, flags, string(in), start, a, c)
			}
		}
	}
	if used < n/4 {
		t.Fatalf("only %d of %d patterns used sets", used, n)
	}
}
