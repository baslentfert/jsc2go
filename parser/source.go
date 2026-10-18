package parser

import (
	"slices"
	"sort"
	"sync"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/baslentfert/jsc2go/ident"
)

// SourceProviderSourceType mirrors JSC's SourceProviderSourceType.
type SourceProviderSourceType uint8

const (
	SourceTypeProgram SourceProviderSourceType = iota
	SourceTypeModule
	SourceTypeWebAssembly
	SourceTypeJSON
	SourceTypeImportMap
)

// SourceProvider owns the text of one script, stored as UTF-16 code units
// because every offset in the engine is a UTF-16 index, exactly as in JSC.
type SourceProvider struct {
	source     []uint16
	URL        string
	SourceType SourceProviderSourceType

	// Origin is host data identifying the script or module the source belongs
	// to (the referrer of import() in eval and Function code).
	Origin any

	lineStartsOnce sync.Once
	lineStarts     []int

	cache sourceProviderCache

	// Directives found by the lexer (//# sourceURL=..., //# sourceMappingURL=...).
	SourceURLDirective        string
	SourceMappingURLDirective string
}

// NewSourceProvider creates a provider from Go (UTF-8) source text.
func NewSourceProvider(source, url string, sourceType SourceProviderSourceType) *SourceProvider {
	return &SourceProvider{source: encodeUTF16(source), URL: url, SourceType: sourceType}
}

// encodeUTF16 is utf16.Encode([]rune(s)) (invalid UTF-8 becomes U+FFFD)
// in one pass, without the rune slice.
func encodeUTF16(s string) []uint16 {
	out := make([]uint16, 0, len(s))
	for i := 0; i < len(s); {
		if c := s[i]; c < utf8.RuneSelf {
			out = append(out, uint16(c))
			i++
			continue
		}
		r, n := utf8.DecodeRuneInString(s[i:])
		i += n
		if r >= 0x10000 {
			r1, r2 := utf16.EncodeRune(r)
			out = append(out, uint16(r1), uint16(r2))
		} else {
			out = append(out, uint16(r))
		}
	}
	return out
}

// NewSourceProviderUTF16 creates a provider from UTF-16 code units, which may
// contain lone surrogates. The slice is retained, not copied.
func NewSourceProviderUTF16(source []uint16, url string, sourceType SourceProviderSourceType) *SourceProvider {
	return &SourceProvider{source: source, URL: url, SourceType: sourceType}
}

// Source returns the full source text.
func (p *SourceProvider) Source() []uint16 { return p.source }

// Range returns the code units in [start, end).
func (p *SourceProvider) Range(start, end int) []uint16 { return p.source[start:end] }

// RangeString returns [start, end) as a Go string (WTF-8).
func (p *SourceProvider) RangeString(start, end int) string {
	return ident.EncodeWTF8(p.source[start:end])
}

// LineColumn is a 1-based line and column.
type LineColumn struct {
	Line, Column int
}

// LineColumnForOffset maps a UTF-16 offset to a 1-based line/column. Like
// JSC's LineStartTable, the line start table is built lazily on first use
// because the lexer itself does not track lines.
func (p *SourceProvider) LineColumnForOffset(offset int) LineColumn {
	p.lineStartsOnce.Do(p.buildLineStarts)
	line := sort.SearchInts(p.lineStarts, offset+1) - 1
	return LineColumn{Line: line + 1, Column: offset - p.lineStarts[line] + 1}
}

func (p *SourceProvider) buildLineStarts() {
	p.lineStarts = []int{0}
	src := p.source
	for i := 0; i < len(src); i++ {
		c := src[i]
		if !isLineTerminator(c) {
			continue
		}
		if c == '\r' && i+1 < len(src) && src[i+1] == '\n' {
			i++
		}
		p.lineStarts = append(p.lineStarts, i+1)
	}
	p.lineStarts = slices.Clip(p.lineStarts)
}

// SourceCode is a [StartOffset, EndOffset) slice of a provider, used both for
// whole programs and for individual function bodies.
type SourceCode struct {
	Provider    *SourceProvider
	StartOffset int
	EndOffset   int
}

// NewSourceCode covers the provider's full text.
func NewSourceCode(p *SourceProvider) SourceCode {
	return SourceCode{Provider: p, StartOffset: 0, EndOffset: len(p.source)}
}

func (s SourceCode) Length() int { return s.EndOffset - s.StartOffset }

func (s SourceCode) IsNull() bool { return s.Provider == nil }

// View returns the code units this SourceCode covers.
func (s SourceCode) View() []uint16 { return s.Provider.source[s.StartOffset:s.EndOffset] }

func (s SourceCode) String() string { return ident.EncodeWTF8(s.View()) }

// SubExpression returns the SourceCode for a function body, from its opening
// brace through its closing brace (inclusive).
func (s SourceCode) SubExpression(openBrace, closeBrace int) SourceCode {
	return SourceCode{Provider: s.Provider, StartOffset: openBrace, EndOffset: closeBrace + 1}
}
