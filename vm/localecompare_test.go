package vm

import (
	"math/rand"
	"testing"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

// TestCompareAlnum checks compareAlnum against the collator on strings of
// letters and digits, and that it declines other strings.
func TestCompareAlnum(t *testing.T) {
	col := collate.New(language.Und)
	const chars = "0123456789aAbBcCzZyY"
	rnd := rand.New(rand.NewSource(1))
	word := func() string {
		b := make([]byte, rnd.Intn(6))
		for i := range b {
			b[i] = chars[rnd.Intn(len(chars))]
		}
		return string(b)
	}
	for range 200000 {
		a, b := word(), word()
		if rnd.Intn(4) == 0 {
			b = a[:rnd.Intn(len(a)+1)] + word()
		}
		got, ok := compareAlnum(NewString(a), NewString(b))
		if !ok {
			t.Fatalf("compareAlnum(%q, %q) declined", a, b)
		}
		if want := col.CompareString(a, b); got != want {
			t.Fatalf("compareAlnum(%q, %q) = %d, collator %d", a, b, got, want)
		}
	}
	for _, p := range [][2]string{{"a b", "ab"}, {"a-b", "a"}, {"é", "e"}, {"a", "a_"}} {
		if _, ok := compareAlnum(NewString(p[0]), NewString(p[1])); ok {
			t.Errorf("compareAlnum(%q, %q) did not decline", p[0], p[1])
		}
	}
}
