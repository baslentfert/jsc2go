// Package yarr is a port of JavaScriptCore's Yarr regular expression engine
// (Yet Another Regex Runtime): the pattern parser, the pattern compiler and
// the backtracking bytecode interpreter. The JIT is not ported.
//
// Patterns and subjects are UTF-16 code units, as in JavaScript.
package yarr

import "strings"

// Stack space (in frame slots) needed by the backtracking state of each term
// kind (Yarr.h).
const (
	stackSpaceForBackTrackInfoPatternCharacter       = 2
	stackSpaceForBackTrackInfoCharacterClass         = 2
	stackSpaceForBackTrackInfoBackReference          = 3
	stackSpaceForBackTrackInfoAlternative            = 1
	stackSpaceForBackTrackInfoParentheticalAssertion = 1
	stackSpaceForBackTrackInfoParenthesesOnce        = 2
	stackSpaceForBackTrackInfoParenthesesTerminal    = 2
	stackSpaceForBackTrackInfoParentheses            = 4
	stackSpaceForDotStarEnclosure                    = 1
)

const (
	quantifyInfinite   = ^uint32(0)
	quantifyInfinite64 = ^uint64(0)
	// OffsetNoMatch marks an unset capture offset.
	OffsetNoMatch = ^uint32(0)
	// matchLimit bounds the number of recursive matchDisjunction calls.
	matchLimit = 100_000_000
)

// MatchResult is JSRegExpResult.
type MatchResult int8

const (
	resultMatch         MatchResult = 1
	resultNoMatch       MatchResult = 0
	resultErrorNoMatch  MatchResult = -1
	resultErrorHitLimit MatchResult = -3
	resultErrorNoMemory MatchResult = -4
)

// BuiltInCharacterClassID names \d, \s, \w, . and the Unicode properties
// (BaseUnicodePropertyID + property index).
type BuiltInCharacterClassID uint32

const (
	DigitClassID BuiltInCharacterClassID = iota
	SpaceClassID
	WordClassID
	DotClassID
	BaseUnicodePropertyID
)

// --- Flags ----------------------------------------------------------------------------

// Flags is a set of regular expression flags (YarrFlags.h).
type Flags uint16

const (
	FlagHasIndices Flags = 1 << iota
	FlagGlobal
	FlagIgnoreCase
	FlagMultiline
	FlagDotAll
	FlagUnicode
	FlagUnicodeSets
	FlagSticky
)

// flagLetters are in alphabetical order, which is also the order of the bits.
const flagLetters = "dgimsuvy"

func (f Flags) Has(x Flags) bool { return f&x != 0 }

// ParseFlags parses a flags string; ok is false for unknown or repeated
// flags, or for u together with v.
func ParseFlags(s string) (Flags, bool) {
	var flags Flags
	for i := 0; i < len(s); i++ {
		j := strings.IndexByte(flagLetters, s[i])
		if j < 0 {
			return 0, false
		}
		bit := Flags(1) << j
		if flags.Has(bit) {
			return 0, false
		}
		flags |= bit
	}
	if flags.Has(FlagUnicode) && flags.Has(FlagUnicodeSets) {
		return 0, false
	}
	return flags, true
}

// String returns the canonical flags string.
func (f Flags) String() string {
	var b []byte
	for i := 0; i < len(flagLetters); i++ {
		if f&(1<<i) != 0 {
			b = append(b, flagLetters[i])
		}
	}
	return string(b)
}

// CompileMode says how strictly a pattern is parsed.
type CompileMode uint8

const (
	CompileModeLegacy CompileMode = iota
	CompileModeUnicode
	CompileModeUnicodeSets
)

func compileModeFor(f Flags) CompileMode {
	switch {
	case f.Has(FlagUnicode):
		return CompileModeUnicode
	case f.Has(FlagUnicodeSets):
		return CompileModeUnicodeSets
	}
	return CompileModeLegacy
}

// --- Errors -----------------------------------------------------------------------------

// ErrorCode is a pattern compilation error (YarrErrorCode.h).
type ErrorCode uint8

const (
	NoError ErrorCode = iota
	ErrPatternTooLarge
	ErrQuantifierOutOfOrder
	ErrQuantifierWithoutAtom
	ErrQuantifierTooLarge
	ErrQuantifierIncomplete
	ErrCantQuantifyAtom
	ErrMissingParentheses
	ErrBracketUnmatched
	ErrParenthesesUnmatched
	ErrParenthesesTypeInvalid
	ErrInvalidGroupName
	ErrDuplicateGroupName
	ErrCharacterClassUnmatched
	ErrCharacterClassRangeOutOfOrder
	ErrCharacterClassRangeInvalid
	ErrClassStringDisjunctionUnmatched
	ErrEscapeUnterminated
	ErrInvalidUnicodeEscape
	ErrInvalidUnicodeCodePointEscape
	ErrInvalidBackreference
	ErrInvalidNamedBackReference
	ErrInvalidIdentityEscape
	ErrInvalidOctalEscape
	ErrInvalidControlLetterEscape
	ErrInvalidUnicodePropertyExpression
	ErrOffsetTooLarge
	ErrInvalidRegularExpressionFlags
	ErrInvalidClassSetOperation
	ErrNegatedClassSetMayContainStrings
	ErrInvalidClassSetCharacter
	ErrInvalidRegularExpressionModifier
	ErrTooManyCaptures
	ErrFrameTooLarge
	// Not a hard error: the pattern may work on another run.
	ErrTooManyDisjunctions
)

var errorMessages = [...]string{
	"",
	"regular expression too large",
	"numbers out of order in {} quantifier",
	"nothing to repeat",
	"number too large in {} quantifier",
	"incomplete {} quantifier for Unicode pattern",
	"invalid quantifier",
	"missing )",
	"unmatched ] or } bracket for Unicode pattern",
	"unmatched parentheses",
	"unrecognized character after (?",
	"invalid group specifier name",
	"duplicate group specifier name",
	"missing terminating ] for character class",
	"range out of order in character class",
	"invalid range in character class for Unicode pattern",
	"missing terminating } for class string disjunction",
	"\\ at end of pattern",
	"invalid Unicode \\u escape",
	"invalid Unicode code point \\u{} escape",
	"invalid backreference for Unicode pattern",
	"invalid \\k<> named backreference",
	"invalid escaped character for Unicode pattern",
	"invalid octal escape for Unicode pattern",
	"invalid \\c escape for Unicode pattern",
	"invalid property expression",
	"pattern exceeds string length limits",
	"invalid flags",
	"invalid operation in class set",
	"negated class set may contain strings",
	"invalid class set character",
	"invalid regular expression modifier",
	"too many captures",
	"too many frame slots for state",
	"too many nested disjunctions",
}

func (e ErrorCode) Error() string { return "Invalid regular expression: " + errorMessages[e] }

func (e ErrorCode) hasError() bool { return e != NoError }

// IsHardError reports whether the pattern can never match (a SyntaxError).
func (e ErrorCode) IsHardError() bool { return e != NoError && e < ErrTooManyDisjunctions }

// --- UTF-16 helpers ----------------------------------------------------------------------

const maxCodePoint = 0x10FFFF

func isLead(c rune) bool  { return c >= 0xD800 && c <= 0xDBFF }
func isTrail(c rune) bool { return c >= 0xDC00 && c <= 0xDFFF }
func isBMP(c rune) bool   { return c <= 0xFFFF }
func isSurrogate(c rune) bool {
	return c >= 0xD800 && c <= 0xDFFF
}
func isLatin1(c rune) bool { return c <= 0xFF }
func isASCII(c rune) bool  { return c <= 0x7F }

func supplementary(lead, trail rune) rune { return (lead-0xD800)<<10 + (trail - 0xDC00) + 0x10000 }

func u16Length(c rune) uint32 {
	if c > 0xFFFF {
		return 2
	}
	return 1
}

func isASCIIAlpha(c rune) bool { return c|0x20 >= 'a' && c|0x20 <= 'z' }
func isASCIIDigit(c rune) bool { return c >= '0' && c <= '9' }
func isASCIIHexDigit(c rune) bool {
	return isASCIIDigit(c) || c|0x20 >= 'a' && c|0x20 <= 'f'
}
func isASCIIOctalDigit(c rune) bool { return c >= '0' && c <= '7' }
func isASCIIAlphanumeric(c rune) bool {
	return isASCIIAlpha(c) || isASCIIDigit(c)
}
func toASCIIUpper(c rune) rune {
	if c >= 'a' && c <= 'z' {
		return c - 0x20
	}
	return c
}
func toASCIILower(c rune) rune {
	if c >= 'A' && c <= 'Z' {
		return c + 0x20
	}
	return c
}
func toASCIIHexValue(c rune) rune {
	if c <= '9' {
		return c - '0'
	}
	return c | 0x20 - 'a' + 10
}
