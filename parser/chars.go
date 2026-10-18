package parser

import (
	"unicode"
	"unicode/utf16"
)

// Character classification for the lexer. JSC uses a 256-entry Latin-1 table
// plus ICU for everything else; here the ASCII range is a small table and the
// rest comes from Go's unicode package.

// asciiIdentStart and asciiIdentPart are bitsets over the 128 ASCII code points.
var asciiIdentStart, asciiIdentPart [2]uint64

func init() {
	for c := 0; c < 128; c++ {
		start := c == '$' || c == '_' || (c|0x20 >= 'a' && c|0x20 <= 'z')
		part := start || (c >= '0' && c <= '9')
		if start {
			asciiIdentStart[c>>6] |= 1 << (c & 63)
		}
		if part {
			asciiIdentPart[c>>6] |= 1 << (c & 63)
		}
	}
}

// ID_Start = L + Nl + Other_ID_Start - Pattern_Syntax - Pattern_White_Space (UAX #31).
func isNonASCIIIdentStart(r rune) bool {
	if !(unicode.IsLetter(r) || unicode.Is(unicode.Nl, r) || unicode.Is(unicode.Other_ID_Start, r)) {
		return false
	}
	return !unicode.Is(unicode.Pattern_Syntax, r) && !unicode.Is(unicode.Pattern_White_Space, r)
}

// ID_Continue = ID_Start + Mn + Mc + Nd + Pc + Other_ID_Continue - Pattern_Syntax - Pattern_White_Space.
// ECMAScript additionally allows ZWNJ and ZWJ in IdentifierPart.
func isNonASCIIIdentPart(r rune) bool {
	if r == 0x200C || r == 0x200D {
		return true
	}
	if isNonASCIIIdentStart(r) {
		return true
	}
	if !(unicode.In(r, unicode.Mn, unicode.Mc, unicode.Nd, unicode.Pc) || unicode.Is(unicode.Other_ID_Continue, r)) {
		return false
	}
	return !unicode.Is(unicode.Pattern_Syntax, r) && !unicode.Is(unicode.Pattern_White_Space, r)
}

// isIdentStart is JSC's isIdentStart for a full code point.
func isIdentStart(r rune) bool {
	if r < 128 {
		return r >= 0 && asciiIdentStart[r>>6]&(1<<(r&63)) != 0
	}
	return isNonASCIIIdentStart(r)
}

// isIdentPart is JSC's isIdentPart for a full code point.
func isIdentPart(r rune) bool {
	if r < 128 {
		return r >= 0 && asciiIdentPart[r>>6]&(1<<(r&63)) != 0
	}
	return isNonASCIIIdentPart(r)
}

// isSingleCharacterIdentPart classifies a single UTF-16 code unit; surrogates
// are never identifier parts on their own.
func isSingleCharacterIdentPart(c uint16) bool {
	return !utf16.IsSurrogate(rune(c)) && isIdentPart(rune(c))
}

// cannotBeIdentStart may give false negatives for non-Latin-1 characters, but
// never false positives. It is used to quickly check what follows a number.
func cannotBeIdentStart(c uint16) bool {
	if c < 256 {
		return !isIdentStart(rune(c)) && c != '\\'
	}
	return isWhiteSpace(c) || isLineTerminator(c)
}

// isWhiteSpace is ECMAScript WhiteSpace: TAB, VT, FF, SP, NBSP, ZWNBSP (BOM)
// and any other Zs code point. Line terminators are not included.
func isWhiteSpace(c uint16) bool {
	switch c {
	case '\t', 0x0B, 0x0C, ' ', 0xA0, 0xFEFF:
		return true
	}
	return c > 0xFF && unicode.Is(unicode.Zs, rune(c))
}

// isLineTerminator is ECMAScript LineTerminator: LF, CR, LS and PS.
func isLineTerminator(c uint16) bool {
	return c == '\r' || c == '\n' || c&^1 == 0x2028
}

func isASCIIDigit(c uint16) bool       { return c >= '0' && c <= '9' }
func isASCIIOctalDigit(c uint16) bool  { return c >= '0' && c <= '7' }
func isASCIIBinaryDigit(c uint16) bool { return c == '0' || c == '1' }
func isASCIIHexDigit(c uint16) bool {
	return isASCIIDigit(c) || (c|0x20 >= 'a' && c|0x20 <= 'f')
}

func toASCIIHexValue(c uint16) uint16 {
	if c <= '9' {
		return c - '0'
	}
	return c | 0x20 - 'a' + 10
}

func convertHex(c1, c2 uint16) uint16 { return toASCIIHexValue(c1)<<4 | toASCIIHexValue(c2) }

// isASCIIAlphaCaselessEqual reports whether c equals the lowercase ASCII letter lower, ignoring case.
func isASCIIAlphaCaselessEqual(c uint16, lower byte) bool { return c|0x20 == uint16(lower) }

// singleEscape maps \X to its character for the simple escapes, or 0 if more
// processing is needed (digits, x, u, line terminators, non-ASCII).
func singleEscape(c uint16) uint16 {
	switch c {
	case 'b':
		return 0x08
	case 'f':
		return 0x0C
	case 'n':
		return 0x0A
	case 'r':
		return 0x0D
	case 't':
		return 0x09
	case 'v':
		return 0x0B
	}
	if c < 32 || c >= 127 || isASCIIDigit(c) || c == 'u' || c == 'x' {
		return 0
	}
	return c
}
