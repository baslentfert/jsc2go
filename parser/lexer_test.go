package parser

import (
	"math"
	"testing"
)

func lexAll(t *testing.T, src string, strict bool) []Token {
	t.Helper()
	l := NewLexer(BuiltinModeNotBuiltin, ScriptModeClassic)
	l.SetCode(NewSourceCode(NewSourceProvider(src, "test.js", SourceTypeProgram)))
	var toks []Token
	var tok Token
	for range 10000 {
		typ := l.Lex(&tok, 0, strict)
		tok.Type = typ
		toks = append(toks, tok)
		if typ == EOFTOK || typ.IsError() {
			return toks
		}
	}
	t.Fatalf("lexer did not terminate on %q", src)
	return nil
}

func types(toks []Token) []TokenType {
	out := make([]TokenType, len(toks))
	for i, tok := range toks {
		out[i] = tok.Type
	}
	return out
}

func TestPunctuators(t *testing.T) {
	src := "{ } ( ) [ ] ; , < > <= >= == != === !== + - * % ** ++ -- << >> >>> & | ^ ! ~ && || ?? ? : = += -= *= %= **= <<= >>= >>>= &= |= ^= &&= ||= ??= => ... . ?. / /="
	want := []TokenType{OPENBRACE, CLOSEBRACE, OPENPAREN, CLOSEPAREN, OPENBRACKET, CLOSEBRACKET, SEMICOLON, COMMA,
		LT, GT, LE, GE, EQEQ, NE, STREQ, STRNEQ, PLUS, MINUS, TIMES, MOD, POW, PLUSPLUS, MINUSMINUS,
		LSHIFT, RSHIFT, URSHIFT, BITAND, BITOR, BITXOR, EXCLAMATION, TILDE, AND, OR, COALESCE, QUESTION, COLON,
		EQUAL, PLUSEQUAL, MINUSEQUAL, MULTEQUAL, MODEQUAL, POWEQUAL, LSHIFTEQUAL, RSHIFTEQUAL, URSHIFTEQUAL,
		BITANDEQUAL, BITOREQUAL, BITXOREQUAL, ANDEQUAL, OREQUAL, COALESCEEQUAL, ARROWFUNCTION, DOTDOTDOT, DOT,
		QUESTIONDOT, DIVIDE, DIVEQUAL, EOFTOK}
	got := types(lexAll(t, src, false))
	if len(got) != len(want) {
		t.Fatalf("got %d tokens %v, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("token %d: got %v, want %v", i, got[i], want[i])
		}
	}
}

func TestOptionalChainVsConditional(t *testing.T) {
	// a?.5:1 is a conditional, not an optional chain.
	got := types(lexAll(t, "a?.5:1", false))
	want := []TokenType{IDENT, QUESTION, DOUBLE, COLON, INTEGER, EOFTOK}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestKeywordsAndIdentifiers(t *testing.T) {
	cases := []struct {
		src    string
		strict bool
		want   TokenType
		ident  string
	}{
		{"var", false, VAR, "var"},
		{"instanceof", false, INSTANCEOF, "instanceof"},
		{"let", false, LET, "let"},
		{"static", false, IDENT, "static"},
		{"static", true, RESERVED_IF_STRICT, "static"},
		{"enum", false, RESERVED, "enum"},
		{"foo", false, IDENT, "foo"},
		{"$_a1", false, IDENT, "$_a1"},
		{"café", false, IDENT, "café"},
		{"π", false, IDENT, "π"},
		{"\\u0061bc", false, IDENT, "abc"},
		{"a\\u{62}c", false, IDENT, "abc"},
		{"v\\u0061r", false, ESCAPED_KEYWORD, "var"},
		{"st\\u0061tic", false, IDENT, "static"},
		{"st\\u0061tic", true, ESCAPED_KEYWORD, "static"},
		{"𝑥", false, IDENT, "𝑥"},
		{"#priv", false, PRIVATENAME, "#priv"},
		{"#if", false, PRIVATENAME, "#if"},
	}
	for _, c := range cases {
		toks := lexAll(t, c.src, c.strict)
		if toks[0].Type != c.want {
			t.Errorf("%q: got %v, want %v", c.src, toks[0].Type, c.want)
			continue
		}
		if got := toks[0].Data.Ident.String(); got != c.ident {
			t.Errorf("%q: ident %q, want %q", c.src, got, c.ident)
		}
		if toks[0].End != TextPosition(len([]rune(c.src))) && c.src != "𝑥" {
			t.Errorf("%q: end %d", c.src, toks[0].End)
		}
	}
}

func TestIdentifierErrors(t *testing.T) {
	cases := map[string]TokenType{
		"\\x":         INVALID_IDENTIFIER_ESCAPE_ERRORTOK,
		"\\":          UNTERMINATED_IDENTIFIER_ESCAPE_ERRORTOK,
		"\\u00":       UNTERMINATED_IDENTIFIER_UNICODE_ESCAPE_ERRORTOK,
		"\\u0030":     INVALID_IDENTIFIER_UNICODE_ESCAPE_ERRORTOK, // '0' cannot start an identifier
		"a\\u002d":    INVALID_IDENTIFIER_UNICODE_ESCAPE_ERRORTOK, // '-' is not an identifier part
		"·":           ERRORTOK,                                   // middle dot is part-only
		"#1":          ERRORTOK,
		"@foo":        ERRORTOK,
		"\x01":        ERRORTOK,
		"\\u{110000}": INVALID_IDENTIFIER_UNICODE_ESCAPE_ERRORTOK,
	}
	for src, want := range cases {
		if got := lexAll(t, src, false)[0].Type; got != want {
			t.Errorf("%q: got %v, want %v", src, got, want)
		}
	}
}

func TestNumbers(t *testing.T) {
	cases := []struct {
		src string
		typ TokenType
		val float64
	}{
		{"0", INTEGER, 0},
		{"42", INTEGER, 42},
		{"1_000_000", INTEGER, 1e6},
		{"3.25", DOUBLE, 3.25},
		{".5", DOUBLE, 0.5},
		{"5.", DOUBLE, 5},
		{"1e3", INTEGER, 1000},
		{"1.5e-3", DOUBLE, 0.0015},
		{"2E+2", INTEGER, 200},
		{"0x1F", INTEGER, 31},
		{"0XfF_fF", INTEGER, 0xffff},
		{"0b1010", INTEGER, 10},
		{"0o777", INTEGER, 511},
		{"0777", INTEGER, 511},
		{"0789", INTEGER, 789},
		{"08.5", DOUBLE, 8.5},
		{"0.1", DOUBLE, 0.1},
		{"0e5", INTEGER, 0},
		{"123456789012345678901234567890", DOUBLE, 123456789012345678901234567890},
		{"0xFFFFFFFFFFFFFFFFF", DOUBLE, 0xFFFFFFFFFFFFFFFFF},
		{"9007199254740993", INTEGER, 9007199254740992}, // rounds to even
		{"1e400", DOUBLE, math.Inf(1)},
		{"4294967296", INTEGER, 4294967296},
	}
	for _, c := range cases {
		tok := lexAll(t, c.src, false)[0]
		if tok.Type != c.typ || tok.Data.Double != c.val {
			t.Errorf("%q: got %v %v, want %v %v", c.src, tok.Type, tok.Data.Double, c.typ, c.val)
		}
		if int(tok.End) != len(c.src) {
			t.Errorf("%q: end %d, want %d", c.src, tok.End, len(c.src))
		}
	}
}

func TestBigInts(t *testing.T) {
	cases := []struct {
		src    string
		digits string
		radix  uint8
	}{
		{"0n", "0", 10},
		{"123n", "123", 10},
		{"1_0n", "10", 10},
		{"0xFFn", "FF", 16},
		{"0b101n", "101", 2},
		{"0o17n", "17", 8},
	}
	for _, c := range cases {
		tok := lexAll(t, c.src, false)[0]
		if tok.Type != BIGINT || tok.Data.BigIntString.String() != c.digits || tok.Data.Radix != c.radix {
			t.Errorf("%q: got %v", c.src, tok)
		}
	}
}

func TestNumberErrors(t *testing.T) {
	cases := []struct {
		src    string
		strict bool
		want   TokenType
	}{
		{"3in", false, INVALID_NUMERIC_LITERAL_ERRORTOK},
		{"1_", false, INVALID_NUMERIC_LITERAL_ERRORTOK},
		{"1__0", false, INVALID_NUMERIC_LITERAL_ERRORTOK},
		{"0_1", false, UNTERMINATED_OCTAL_NUMBER_ERRORTOK},
		{"07", true, UNTERMINATED_OCTAL_NUMBER_ERRORTOK},
		{"0x", false, UNTERMINATED_HEX_NUMBER_ERRORTOK},
		{"0xg", false, UNTERMINATED_HEX_NUMBER_ERRORTOK},
		{"0x1g", false, UNTERMINATED_HEX_NUMBER_ERRORTOK},
		{"0b", false, UNTERMINATED_BINARY_NUMBER_ERRORTOK},
		{"1e", false, UNTERMINATED_NUMERIC_LITERAL_ERRORTOK},
		{"1e+x", false, INVALID_NUMERIC_LITERAL_ERRORTOK},
		{"07n", false, INVALID_NUMERIC_LITERAL_ERRORTOK},
		{"1.5n", false, INVALID_NUMERIC_LITERAL_ERRORTOK},
	}
	for _, c := range cases {
		toks := lexAll(t, c.src, c.strict)
		last := toks[len(toks)-1]
		if last.Type != c.want {
			t.Errorf("%q: got %v, want %v", c.src, types(toks), c.want)
		}
	}
}

func TestStrings(t *testing.T) {
	cases := []struct {
		src    string
		strict bool
		want   string
	}{
		{`"abc"`, false, "abc"},
		{`'a"b'`, false, `a"b`},
		{`"\n\t\r\b\f\v\0"`, false, "\n\t\r\b\f\v\x00"},
		{`"\x41B\u{43}"`, false, "ABC"},
		{`"\u{1F600}"`, false, "😀"},
		{`"\101"`, false, "A"},
		{`"\7"`, false, "\x07"},
		{`"\08"`, false, "\x008"},
		{"\"a\\\nb\"", false, "ab"},
		{"\"a\\\r\nb\"", false, "ab"},
		{"\"a b\"", false, "a b"},
		{`"\q"`, false, "q"},
		{`"\ud800"`, false, "\xed\xa0\x80"}, // lone surrogate, WTF-8
	}
	for _, c := range cases {
		tok := lexAll(t, c.src, c.strict)[0]
		if tok.Type != STRING || tok.Data.Ident.String() != c.want {
			t.Errorf("%s: got %v %q, want %q", c.src, tok.Type, tok.Data.Ident.String(), c.want)
		}
	}
}

func TestStringErrors(t *testing.T) {
	cases := []struct {
		src    string
		strict bool
		want   TokenType
	}{
		{`"abc`, false, UNTERMINATED_STRING_LITERAL_ERRORTOK},
		{"\"a\nb\"", false, INVALID_STRING_LITERAL_ERRORTOK},
		{`"\x4"`, false, INVALID_STRING_LITERAL_ERRORTOK},
		{`"\u12"`, false, INVALID_STRING_LITERAL_ERRORTOK},
		{`"\1"`, true, INVALID_STRING_LITERAL_ERRORTOK},
		{`"\01"`, true, INVALID_STRING_LITERAL_ERRORTOK},
		{`"\`, false, UNTERMINATED_STRING_LITERAL_ERRORTOK},
	}
	for _, c := range cases {
		if got := lexAll(t, c.src, c.strict)[0].Type; got != c.want {
			t.Errorf("%s: got %v, want %v", c.src, got, c.want)
		}
	}
}

func TestComments(t *testing.T) {
	got := types(lexAll(t, "a // c\n/* x\n y */ b /**/ c", false))
	want := []TokenType{IDENT, IDENT, IDENT, EOFTOK}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}

	// Hashbang only at offset 0.
	if got := types(lexAll(t, "#!/usr/bin/env node\nx", false)); got[0] != IDENT {
		t.Errorf("hashbang: got %v", got)
	}
	if got := lexAll(t, " #!x", false)[0].Type; got != ERRORTOK {
		t.Errorf("late hashbang: got %v", got)
	}

	// HTML-like comments in classic scripts.
	if got := types(lexAll(t, "a <!-- b\nc\n--> d\ne", false)); len(got) != 4 {
		t.Errorf("html comments: got %v", got)
	}

	if got := lexAll(t, "/* x", false)[0].Type; got != UNTERMINATED_MULTILINE_COMMENT_ERRORTOK {
		t.Errorf("unterminated comment: got %v", got)
	}
}

func TestLineTerminatorTracking(t *testing.T) {
	l := NewLexer(BuiltinModeNotBuiltin, ScriptModeClassic)
	l.SetCode(NewSourceCode(NewSourceProvider("a\n++b /*\n*/ c", "", SourceTypeProgram)))
	var tok Token
	l.Lex(&tok, 0, false)
	if l.HasLineTerminatorBeforeToken() {
		t.Error("a: unexpected line terminator")
	}
	if typ := l.Lex(&tok, 0, false); typ != AUTOPLUSPLUS || !l.HasLineTerminatorBeforeToken() {
		t.Errorf("++ after newline: got %v, lt=%v", typ, l.HasLineTerminatorBeforeToken())
	}
	l.Lex(&tok, 0, false)
	if typ := l.Lex(&tok, 0, false); typ != IDENT || !l.HasLineTerminatorBeforeToken() {
		t.Errorf("multi-line comment should count as a line terminator")
	}
}

func TestRegExp(t *testing.T) {
	l := NewLexer(BuiltinModeNotBuiltin, ScriptModeClassic)
	l.SetCode(NewSourceCode(NewSourceProvider(`/a[/]\/b/gi.x`, "", SourceTypeProgram)))
	var tok Token
	if typ := l.Lex(&tok, 0, false); typ != DIVIDE {
		t.Fatalf("got %v", typ)
	}
	if typ := l.ScanRegExp(&tok, 0); typ != REGEXP {
		t.Fatalf("got %v: %s", typ, l.ErrorMessage())
	}
	if p, f := tok.Data.Pattern.String(), tok.Data.Flags.String(); p != `a[/]\/b` || f != "gi" {
		t.Errorf("got /%s/%s", p, f)
	}
	if typ := l.Lex(&tok, 0, false); typ != DOT {
		t.Errorf("after regexp: got %v", typ)
	}

	l.SetCode(NewSourceCode(NewSourceProvider("/=x/", "", SourceTypeProgram)))
	l.Lex(&tok, 0, false)
	if typ := l.ScanRegExp(&tok, '='); typ != REGEXP || tok.Data.Pattern.String() != "=x" {
		t.Errorf("/= prefix: got %v %q", typ, tok.Data.Pattern.String())
	}

	l.SetCode(NewSourceCode(NewSourceProvider("/abc\n/", "", SourceTypeProgram)))
	l.Lex(&tok, 0, false)
	if typ := l.ScanRegExp(&tok, 0); typ != UNTERMINATED_REGEXP_LITERAL_ERRORTOK {
		t.Errorf("unterminated: got %v", typ)
	}
}

func TestTemplates(t *testing.T) {
	l := NewLexer(BuiltinModeNotBuiltin, ScriptModeClassic)
	l.SetCode(NewSourceCode(NewSourceProvider("`a\\n${x}b\r\nc`", "", SourceTypeProgram)))
	var tok Token
	if typ := l.Lex(&tok, 0, false); typ != BACKQUOTE {
		t.Fatalf("got %v", typ)
	}
	if typ := l.ScanTemplateString(&tok, BuildRawStrings); typ != TEMPLATE {
		t.Fatalf("got %v", typ)
	}
	if tok.Data.Cooked.String() != "a\n" || tok.Data.Raw.String() != `a\n` || tok.Data.IsTail {
		t.Errorf("head: cooked %q raw %q tail %v", tok.Data.Cooked.String(), tok.Data.Raw.String(), tok.Data.IsTail)
	}
	if typ := l.Lex(&tok, 0, false); typ != IDENT {
		t.Fatalf("got %v", typ)
	}
	if typ := l.Lex(&tok, 0, false); typ != CLOSEBRACE {
		t.Fatalf("got %v", typ)
	}
	if typ := l.ScanTemplateString(&tok, BuildRawStrings); typ != TEMPLATE {
		t.Fatalf("got %v", typ)
	}
	if tok.Data.Cooked.String() != "b\nc" || tok.Data.Raw.String() != "b\nc" || !tok.Data.IsTail {
		t.Errorf("tail: cooked %q raw %q tail %v", tok.Data.Cooked.String(), tok.Data.Raw.String(), tok.Data.IsTail)
	}

	// Invalid escapes are allowed in tagged templates: cooked is null.
	l.SetCode(NewSourceCode(NewSourceProvider("`\\unicode`", "", SourceTypeProgram)))
	l.Lex(&tok, 0, false)
	if typ := l.ScanTemplateString(&tok, BuildRawStrings); typ != TEMPLATE || !tok.Data.Cooked.IsNull() || tok.Data.Raw.String() != `\unicode` {
		t.Errorf("invalid escape: got %v cooked=%v raw=%q", typ, tok.Data.Cooked.IsNull(), tok.Data.Raw.String())
	}
	l.SetCode(NewSourceCode(NewSourceProvider("`\\unicode`", "", SourceTypeProgram)))
	l.Lex(&tok, 0, false)
	if typ := l.ScanTemplateString(&tok, DontBuildRawStrings); typ != INVALID_TEMPLATE_LITERAL_ERRORTOK {
		t.Errorf("invalid escape, untagged: got %v", typ)
	}
}

func TestSourceDirectives(t *testing.T) {
	l := NewLexer(BuiltinModeNotBuiltin, ScriptModeClassic)
	l.SetCode(NewSourceCode(NewSourceProvider("x\n//# sourceURL=foo.js\n//@ sourceMappingURL=foo.map", "", SourceTypeProgram)))
	var tok Token
	for l.Lex(&tok, 0, false) != EOFTOK {
	}
	if l.SourceURLDirective() != "foo.js" || l.SourceMappingURLDirective() != "foo.map" {
		t.Errorf("got %q %q", l.SourceURLDirective(), l.SourceMappingURLDirective())
	}
}

func TestLineColumn(t *testing.T) {
	p := NewSourceProvider("ab\ncd\r\nef g", "", SourceTypeProgram)
	for _, c := range []struct{ off, line, col int }{{0, 1, 1}, {1, 1, 2}, {3, 2, 1}, {7, 3, 1}, {10, 4, 1}} {
		if got := p.LineColumnForOffset(c.off); got != (LineColumn{c.line, c.col}) {
			t.Errorf("offset %d: got %v, want %d:%d", c.off, got, c.line, c.col)
		}
	}
}
