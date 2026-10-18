package yarr

import (
	"slices"
	"testing"
	"unicode/utf16"
)

func TestExec(t *testing.T) {
	tests := []struct {
		pattern, flags, input string
		want                  []int
	}{
		{"abc", "", "xxabcxx", []int{2, 5}},
		{"a(b|c)+d", "", "abcbcd", []int{0, 6, 4, 5}},
		{"(a)|b", "", "b", []int{0, 1, -1, -1}},
		{"^abc$", "m", "x\nabc\ny", []int{2, 5}},
		{`(?<=\$)\d+`, "", "cost $42", []int{6, 8}},
		{`(?<!\$)\d+`, "", "$42 17", []int{2, 3}},
		{`(\w+)\s\1`, "", "hello hello", []int{0, 11, 0, 5}},
		{`(?<y>\d{4})-(?<m>\d{2})`, "", "2024-05", []int{0, 7, 0, 4, 5, 7}},
		{`ABC`, "i", "xabc", []int{1, 4}},
		{`\u{1F600}`, "u", "a\U0001F600", []int{1, 3}},
		{`.`, "u", "\U0001F600", []int{0, 2}},
		{`.`, "", "\U0001F600", []int{0, 1}},
		{`a*?b`, "", "aaab", []int{0, 4}},
		{`(a*)*b`, "", "aab", []int{0, 3, 0, 2}},
		{`(z)((a+)?(b+)?(c))*`, "", "zaacbbbcac", []int{0, 10, 0, 1, 8, 10, 8, 9, -1, -1, 9, 10}},
		{`\p{Script=Greek}+`, "u", "abγδε", []int{2, 5}},
		{`[\p{L}--[a-z]]+`, "v", "abCDe", []int{2, 4}},
		{`\bfoo\b`, "", "a foo b", []int{2, 5}},
		{`(?:(?<a>x)|(?<a>y))\k<a>`, "", "yy", []int{0, 2, -1, -1, 0, 1}},
		{`(?i:a)b`, "", "Ab", []int{0, 2}},
		{`[^]`, "", "\n", []int{0, 1}},
		{`\u017f`, "i", "s", nil},
		{`\u017f`, "iu", "s", []int{0, 1}},
	}
	for _, tt := range tests {
		flags, _ := ParseFlags(tt.flags)
		p, err := CompilePattern(utf16.Encode([]rune(tt.pattern)), flags)
		if err.hasError() {
			t.Errorf("%s/%s: %v", tt.pattern, tt.flags, err)
			continue
		}
		got, e := Exec(p, utf16.Encode([]rune(tt.input)), 0)
		if e != nil {
			t.Errorf("%s: %v", tt.pattern, e)
		}
		if !slices.Equal(got, tt.want) {
			t.Errorf("/%s/%s on %q = %v, want %v", tt.pattern, tt.flags, tt.input, got, tt.want)
		}
	}
}

func TestSyntaxErrors(t *testing.T) {
	for _, s := range []string{"(", "a**", "[b-a]", `\u{110000}`, "(?<a>x)(?<a>y)", `\p{Foo}`} {
		flags := FlagUnicode
		if _, err := CompilePattern(utf16.Encode([]rune(s)), flags); !err.hasError() {
			t.Errorf("/%s/u compiled without error", s)
		}
	}
}
