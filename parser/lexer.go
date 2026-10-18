package parser

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"unicode"
	"unicode/utf16"

	"github.com/baslentfert/jsc2go/ident"
)

// Port of JavaScriptCore/parser/Lexer.{h,cpp}.
//
// JSC instantiates the lexer twice (Latin-1 and UTF-16 sources) and uses SIMD
// scans for hot loops. This port always lexes UTF-16 code units and relies on
// straightforward loops; the token stream and error behaviour are the same.

// LexerFlags mirrors JSC's OptionSet<LexerFlags>.
type LexerFlags uint8

const (
	LexerIgnoreReservedWords LexerFlags = 1 << iota
	LexerDontBuildStrings
	LexerDontBuildKeywords
)

func (f LexerFlags) Has(flag LexerFlags) bool { return f&flag != 0 }

// ScriptMode mirrors JSParserScriptMode.
type ScriptMode uint8

const (
	ScriptModeClassic ScriptMode = iota
	ScriptModeModule
)

// BuiltinMode mirrors JSParserBuiltinMode.
type BuiltinMode uint8

const (
	BuiltinModeNotBuiltin BuiltinMode = iota
	BuiltinModeBuiltin
)

// RawStringsBuildMode controls whether template literals record their raw text.
type RawStringsBuildMode uint8

const (
	BuildRawStrings RawStringsBuildMode = iota
	DontBuildRawStrings
)

type stringParseResult uint8

const (
	stringParsedSuccessfully stringParseResult = iota
	stringUnterminated
	stringCannotBeParsed
)

// keywords is JSC's mainTable from Keywords.table.
var keywords = map[string]TokenType{
	"null": NULLTOKEN, "true": TRUETOKEN, "false": FALSETOKEN, "await": AWAIT, "break": BREAK,
	"case": CASE, "catch": CATCH, "class": CLASSTOKEN, "const": CONSTTOKEN, "default": DEFAULT,
	"extends": EXTENDS, "finally": FINALLY, "for": FOR, "instanceof": INSTANCEOF, "new": NEW,
	"var": VAR, "let": LET, "continue": CONTINUE, "function": FUNCTION, "return": RETURN,
	"void": VOIDTOKEN, "delete": DELETETOKEN, "if": IF, "this": THISTOKEN, "do": DO, "while": WHILE,
	"else": ELSE, "in": INTOKEN, "super": SUPER, "switch": SWITCH, "throw": THROW, "try": TRY,
	"typeof": TYPEOF, "with": WITH, "debugger": DEBUGGER, "yield": YIELD, "enum": RESERVED,
	"export": EXPORT_, "import": IMPORT, "implements": RESERVED_IF_STRICT,
	"interface": RESERVED_IF_STRICT, "package": RESERVED_IF_STRICT, "private": RESERVED_IF_STRICT,
	"protected": RESERVED_IF_STRICT, "public": RESERVED_IF_STRICT, "static": RESERVED_IF_STRICT,
}

const maxKeywordLength = len("instanceof")

// lookupKeyword returns the keyword token for units, if any. Keywords are
// short ASCII words, so the lookup goes through a stack buffer and the
// map index on string(buf) does not allocate.
func lookupKeyword(units []uint16) (TokenType, bool) {
	if len(units) < 2 || len(units) > maxKeywordLength {
		return 0, false
	}
	var buf [maxKeywordLength]byte
	for i, u := range units {
		if u < 'a' || u > 'z' {
			return 0, false
		}
		buf[i] = byte(u)
	}
	t, ok := keywords[string(buf[:len(units)])]
	return t, ok
}

// IsLexerKeyword reports whether id is a keyword in JSC's mainTable.
func IsLexerKeyword(id ident.Identifier) bool {
	_, ok := keywords[id.String()]
	return ok
}

// Lexer turns source text into tokens on demand; the parser drives it and may
// rewind it with SetOffset.
type Lexer struct {
	source  SourceCode
	code    []uint16 // the full provider text; offsets index into it
	pos     int      // index of current in code
	end     int      // end offset of the SourceCode
	current uint16   // code[pos], or 0 at end of input

	lexErrorMessage              string
	hasLineTerminatorBeforeToken bool
	atLineStart                  bool
	parsingBuiltinFunction       bool
	isReparsingFunction          bool
	err                          bool

	buffer8                      []byte
	buffer16                     []uint16
	bufferForRawTemplateString16 []uint16

	sourceURLDirective        string
	sourceMappingURLDirective string
	scriptMode                ScriptMode

	// idents caches the identifiers of ASCII names: a name seen again is
	// looked up with its bytes in identBuf, without making a string or
	// going to ident's global atom table.
	idents   map[string]ident.Identifier
	identBuf []byte
}

// identifier returns the identifier of the name units.
func (l *Lexer) identifier(units []uint16) ident.Identifier {
	buf := l.identBuf[:0]
	for _, u := range units {
		if u >= 0x80 {
			return ident.FromUTF16(units)
		}
		buf = append(buf, byte(u))
	}
	l.identBuf = buf
	if id, ok := l.idents[string(buf)]; ok {
		return id
	}
	id := ident.FromString(string(buf))
	if l.idents == nil {
		l.idents = make(map[string]ident.Identifier)
	}
	l.idents[id.String()] = id
	return id
}

// NewLexer creates a lexer. Call SetCode before lexing.
func NewLexer(builtinMode BuiltinMode, scriptMode ScriptMode) *Lexer {
	return &Lexer{
		parsingBuiltinFunction: builtinMode == BuiltinModeBuiltin,
		scriptMode:             scriptMode,
	}
}

// SetCode prepares the lexer to scan source.
func (l *Lexer) SetCode(source SourceCode) {
	l.source = source
	l.code = source.Provider.Source()
	l.pos = source.StartOffset
	l.end = source.EndOffset
	l.err = false
	l.atLineStart = true
	l.lexErrorMessage = ""
	l.sourceURLDirective = ""
	l.sourceMappingURLDirective = ""
	l.buffer8 = l.buffer8[:0]
	l.buffer16 = l.buffer16[:0]
	l.bufferForRawTemplateString16 = l.bufferForRawTemplateString16[:0]
	l.loadCurrent()
}

func (l *Lexer) SetIsReparsingFunction()   { l.isReparsingFunction = true }
func (l *Lexer) IsReparsingFunction() bool { return l.isReparsingFunction }

func (l *Lexer) CurrentOffset() int            { return l.pos }
func (l *Lexer) CurrentPosition() TextPosition { return TextPosition(l.pos) }
func (l *Lexer) CodeLength() int               { return l.end - l.source.StartOffset }

func (l *Lexer) HasLineTerminatorBeforeToken() bool     { return l.hasLineTerminatorBeforeToken }
func (l *Lexer) SetHasLineTerminatorBeforeToken(b bool) { l.hasLineTerminatorBeforeToken = b }

func (l *Lexer) SawError() bool                    { return l.err }
func (l *Lexer) SetSawError(b bool)                { l.err = b }
func (l *Lexer) ErrorMessage() string              { return l.lexErrorMessage }
func (l *Lexer) SetErrorMessage(msg string)        { l.lexErrorMessage = msg }
func (l *Lexer) SourceURLDirective() string        { return l.sourceURLDirective }
func (l *Lexer) SourceMappingURLDirective() string { return l.sourceMappingURLDirective }

// Clear releases buffers; mirrors Lexer::clear.
func (l *Lexer) Clear() {
	l.buffer8 = nil
	l.buffer16 = nil
	l.bufferForRawTemplateString16 = nil
	l.isReparsingFunction = false
}

func (l *Lexer) ClearErrorCodeAndBuffers() {
	l.err = false
	l.lexErrorMessage = ""
	l.buffer8 = l.buffer8[:0]
	l.buffer16 = l.buffer16[:0]
}

// SetOffset rewinds (or advances) the lexer to an absolute source offset.
func (l *Lexer) SetOffset(offset int) {
	l.ClearErrorCodeAndBuffers()
	l.pos = offset
	l.loadCurrent()
}

// TokenText returns the source text of a token.
func (l *Lexer) TokenText(t *Token) []uint16 {
	return l.code[t.Start:t.End]
}

func (l *Lexer) loadCurrent() {
	if l.pos < l.end {
		l.current = l.code[l.pos]
	} else {
		l.current = 0
	}
}

func (l *Lexer) shift() {
	l.pos++
	l.loadCurrent()
}

func (l *Lexer) atEnd() bool { return l.current == 0 && l.pos >= l.end }

func (l *Lexer) peek(offset int) uint16 {
	if p := l.pos + offset; p < l.end {
		return l.code[p]
	}
	return 0
}

const errorCodePoint rune = -1

// currentCodePoint decodes a surrogate pair at the current position.
func (l *Lexer) currentCodePoint() rune {
	c := rune(l.current)
	if !utf16.IsSurrogate(c) {
		return c
	}
	trail := rune(l.peek(1))
	if c >= 0xDC00 || trail < 0xDC00 || trail > 0xDFFF {
		return errorCodePoint
	}
	return utf16.DecodeRune(c, trail)
}

func (l *Lexer) codePointAt(pos int) rune {
	c := rune(l.code[pos])
	if c >= 0xD800 && c < 0xDC00 && pos+1 < l.end {
		if r := utf16.DecodeRune(c, rune(l.code[pos+1])); r != unicode.ReplacementChar {
			return r
		}
	}
	return c
}

func (l *Lexer) shiftLineTerminator() {
	prev := l.current
	l.shift()
	if prev == '\r' && l.current == '\n' {
		l.shift()
	}
}

func (l *Lexer) skipWhitespace() {
	for isWhiteSpace(l.current) {
		l.shift()
	}
}

func (l *Lexer) recordUnicodeCodePoint(cp rune) {
	if cp < 0x10000 {
		l.buffer16 = append(l.buffer16, uint16(cp))
		return
	}
	hi, lo := utf16.EncodeRune(cp)
	l.buffer16 = append(l.buffer16, uint16(hi), uint16(lo))
}

func (l *Lexer) invalidCharacterMessage() string {
	switch l.current {
	case 0:
		return `Invalid character: '\0'`
	case 10:
		return `Invalid character: '\n'`
	case 11:
		return `Invalid character: '\v'`
	case 13:
		return `Invalid character: '\r'`
	case '#':
		return "Invalid character: '#'"
	case '@':
		return "Invalid character: '@'"
	case '`':
		return "Invalid character: '`'"
	}
	return fmt.Sprintf(`Invalid character '\u%04x'`, l.current)
}

// unicodeEscape is the result of parsing \u escapes: a code point, or one of
// the two sentinels below.
type unicodeEscape rune

const (
	unicodeEscapeIncomplete unicodeEscape = -2
	unicodeEscapeInvalid    unicodeEscape = -1
)

func (u unicodeEscape) valid() bool { return u >= 0 }

// parseUnicodeEscape parses what follows "\u": either XXXX or {X...}.
func (l *Lexer) parseUnicodeEscape() unicodeEscape {
	if l.current == '{' {
		l.shift()
		var cp rune
		for {
			if !isASCIIHexDigit(l.current) {
				if l.current != 0 {
					return unicodeEscapeInvalid
				}
				return unicodeEscapeIncomplete
			}
			cp = cp<<4 | rune(toASCIIHexValue(l.current))
			if cp > unicode.MaxRune {
				// For raw template literals we consume the NotCodePoint's hex digits.
				l.shift()
				for isASCIIHexDigit(l.current) {
					l.shift()
				}
				if l.atEnd() {
					return unicodeEscapeIncomplete
				}
				return unicodeEscapeInvalid
			}
			l.shift()
			if l.current == '}' {
				break
			}
		}
		l.shift()
		return unicodeEscape(cp)
	}

	c2, c3, c4 := l.peek(1), l.peek(2), l.peek(3)
	if !isASCIIHexDigit(l.current) || !isASCIIHexDigit(c2) || !isASCIIHexDigit(c3) || !isASCIIHexDigit(c4) {
		result := unicodeEscapeInvalid
		if l.pos+4 >= l.end {
			result = unicodeEscapeIncomplete
		}
		// For raw template literals we consume the NotEscapeSequence.
		for isASCIIHexDigit(l.current) {
			l.shift()
		}
		return result
	}
	cp := convertHex(l.current, c2)<<8 | convertHex(c3, c4)
	for range 4 {
		l.shift()
	}
	return unicodeEscape(cp)
}

// Lex scans the next token into tok and returns its type.
func (l *Lexer) Lex(tok *Token, flags LexerFlags, strictMode bool) TokenType {
	l.hasLineTerminatorBeforeToken = false
	return l.LexWithoutClearingLineTerminator(tok, flags, strictMode)
}

// NextTokenIsColon peeks past whitespace and line terminators for a ':'.
func (l *Lexer) NextTokenIsColon() bool {
	p := l.pos
	for p < l.end && (isWhiteSpace(l.code[p]) || isLineTerminator(l.code[p])) {
		p++
	}
	return p < l.end && l.code[p] == ':'
}

func isRestrKeyword(t TokenType) bool {
	return t == CONTINUE || t == BREAK || t == RETURN || t == THROW
}

// LexWithoutClearingLineTerminator is the main scanner.
func (l *Lexer) LexWithoutClearingLineTerminator(tok *Token, flags LexerFlags, strictMode bool) TokenType {
	data := &tok.Data
	token := TokenType(ERRORTOK)

start:
	l.skipWhitespace()
	tok.Start = l.CurrentPosition()

	c := l.current
	if c >= 128 {
		// Non-ASCII: identifier start, line terminator, or invalid.
		switch cp := l.codePointAt(l.pos); {
		case isLineTerminator(c):
			l.shiftLineTerminator()
			l.atLineStart = true
			l.hasLineTerminatorBeforeToken = true
			goto start
		case isNonASCIIIdentStart(cp):
			token = l.parseIdentifier(data, flags, strictMode)
			goto done
		default:
			return l.fail(tok, ERRORTOK, l.invalidCharacterMessage())
		}
	}

	switch c {
	case '>':
		l.shift()
		switch {
		case l.current == '>':
			l.shift()
			switch l.current {
			case '>':
				l.shift()
				token = URSHIFT
				if l.current == '=' {
					l.shift()
					token = URSHIFTEQUAL
				}
			case '=':
				l.shift()
				token = RSHIFTEQUAL
			default:
				token = RSHIFT
			}
		case l.current == '=':
			l.shift()
			token = GE
		default:
			token = GT
		}

	case '=':
		if l.peek(1) == '>' {
			token = ARROWFUNCTION
			l.shift()
			l.shift()
			break
		}
		l.shift()
		token = EQUAL
		if l.current == '=' {
			l.shift()
			token = EQEQ
			if l.current == '=' {
				l.shift()
				token = STREQ
			}
		}

	case '<':
		l.shift()
		if l.current == '!' && l.peek(1) == '-' && l.peek(2) == '-' && l.scriptMode == ScriptModeClassic {
			// <!-- starts a single-line comment (Annex B).
			if t, ok := l.scanSingleLineComment(tok, false); ok {
				return t
			}
			goto start
		}
		switch l.current {
		case '<':
			l.shift()
			token = LSHIFT
			if l.current == '=' {
				l.shift()
				token = LSHIFTEQUAL
			}
		case '=':
			l.shift()
			token = LE
		default:
			token = LT
		}

	case '!':
		l.shift()
		token = EXCLAMATION
		if l.current == '=' {
			l.shift()
			token = NE
			if l.current == '=' {
				l.shift()
				token = STRNEQ
			}
		}

	case '+':
		l.shift()
		switch l.current {
		case '+':
			l.shift()
			token = PLUSPLUS
			if l.hasLineTerminatorBeforeToken {
				token = AUTOPLUSPLUS
			}
		case '=':
			l.shift()
			token = PLUSEQUAL
		default:
			token = PLUS
		}

	case '-':
		l.shift()
		switch l.current {
		case '-':
			l.shift()
			if (l.atLineStart || l.hasLineTerminatorBeforeToken) && l.current == '>' && l.scriptMode == ScriptModeClassic {
				// --> at the start of a line is a single-line comment (Annex B).
				l.shift()
				if t, ok := l.scanSingleLineComment(tok, false); ok {
					return t
				}
				goto start
			}
			token = MINUSMINUS
			if l.hasLineTerminatorBeforeToken {
				token = AUTOMINUSMINUS
			}
		case '=':
			l.shift()
			token = MINUSEQUAL
		default:
			token = MINUS
		}

	case '*':
		l.shift()
		switch l.current {
		case '=':
			l.shift()
			token = MULTEQUAL
		case '*':
			l.shift()
			token = POW
			if l.current == '=' {
				l.shift()
				token = POWEQUAL
			}
		default:
			token = TIMES
		}

	case '/':
		l.shift()
		switch l.current {
		case '/':
			l.shift()
			if t, ok := l.scanSingleLineComment(tok, true); ok {
				return t
			}
			goto start
		case '*':
			l.shift()
			if l.parseMultilineComment() {
				goto start
			}
			return l.fail(tok, UNTERMINATED_MULTILINE_COMMENT_ERRORTOK, "Multiline comment was not closed properly")
		case '=':
			l.shift()
			token = DIVEQUAL
		default:
			token = DIVIDE
		}

	case '&':
		l.shift()
		switch l.current {
		case '&':
			l.shift()
			token = AND
			if l.current == '=' {
				l.shift()
				token = ANDEQUAL
			}
		case '=':
			l.shift()
			token = BITANDEQUAL
		default:
			token = BITAND
		}

	case '^':
		l.shift()
		token = BITXOR
		if l.current == '=' {
			l.shift()
			token = BITXOREQUAL
		}

	case '%':
		l.shift()
		token = MOD
		if l.current == '=' {
			l.shift()
			token = MODEQUAL
		}

	case '|':
		l.shift()
		switch l.current {
		case '=':
			l.shift()
			token = BITOREQUAL
		case '|':
			l.shift()
			token = OR
			if l.current == '=' {
				l.shift()
				token = OREQUAL
			}
		default:
			token = BITOR
		}

	case '(':
		l.shift()
		token = OPENPAREN
	case ')':
		l.shift()
		token = CLOSEPAREN
	case '[':
		l.shift()
		token = OPENBRACKET
	case ']':
		l.shift()
		token = CLOSEBRACKET
	case ',':
		l.shift()
		token = COMMA
	case ':':
		l.shift()
		token = COLON
	case '~':
		l.shift()
		token = TILDE
	case ';':
		l.shift()
		token = SEMICOLON
	case '`':
		l.shift()
		token = BACKQUOTE
	case '{':
		l.shift()
		token = OPENBRACE
	case '}':
		l.shift()
		token = CLOSEBRACE

	case '?':
		l.shift()
		switch {
		case l.current == '?':
			l.shift()
			token = COALESCE
			if l.current == '=' {
				l.shift()
				token = COALESCEEQUAL
			}
		case l.current == '.' && !isASCIIDigit(l.peek(1)):
			l.shift()
			token = QUESTIONDOT
		default:
			token = QUESTION
		}

	case '.':
		l.shift()
		if !isASCIIDigit(l.current) {
			if l.current == '.' && l.peek(1) == '.' {
				l.shift()
				l.shift()
				token = DOTDOTDOT
			} else {
				token = DOT
			}
			break
		}
		l.buffer8 = append(l.buffer8, '0')
		if t, msg := l.scanFraction(data); t != 0 {
			return l.fail(tok, t, msg)
		}
		token = DOUBLE
		if t, msg := l.checkAfterNumber("No identifiers allowed directly after numeric literal", 0); t != 0 {
			return l.fail(tok, t, msg)
		}

	case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		t, msg := l.scanNumber(data, strictMode)
		if t.IsError() {
			return l.fail(tok, t, msg)
		}
		token = t

	case '"', '\'':
		var result stringParseResult
		if flags.Has(LexerDontBuildStrings) {
			result = l.parseString(data, strictMode, false)
		} else {
			result = l.parseString(data, strictMode, true)
		}
		if result != stringParsedSuccessfully {
			t := TokenType(INVALID_STRING_LITERAL_ERRORTOK)
			if result == stringUnterminated {
				t = UNTERMINATED_STRING_LITERAL_ERRORTOK
			}
			return l.fail(tok, t, "")
		}
		l.shift()
		l.atLineStart = false
		tok.End = l.CurrentPosition()
		return STRING

	case '\\':
		token = l.parseIdentifier(data, flags, strictMode)

	case '\n', '\r':
		l.shiftLineTerminator()
		l.atLineStart = true
		l.hasLineTerminatorBeforeToken = true
		goto start

	case '#':
		// A hashbang is only permitted at the very start of the source text.
		next := l.peek(1)
		if next == '!' && l.pos == 0 {
			l.shift()
			l.shift()
			if t, ok := l.scanSingleLineComment(tok, false); ok {
				return t
			}
			goto start
		}
		var valid bool
		if next < 128 {
			valid = isIdentStart(rune(next)) || next == '\\'
		} else if l.pos+1 < l.end {
			valid = isNonASCIIIdentStart(l.codePointAt(l.pos + 1))
		}
		if !valid {
			return l.fail(tok, ERRORTOK, l.invalidCharacterMessage())
		}
		token = l.parseIdentifier(data, flags&^LexerDontBuildKeywords, strictMode)

	case '@':
		if !l.parsingBuiltinFunction {
			return l.fail(tok, ERRORTOK, l.invalidCharacterMessage())
		}
		token = l.parseIdentifier(data, flags, strictMode)

	case 0:
		if l.atEnd() {
			tok.End = l.CurrentPosition()
			return EOFTOK
		}
		return l.fail(tok, ERRORTOK, l.invalidCharacterMessage())

	default:
		if c < 128 && isIdentStart(rune(c)) {
			token = l.parseIdentifier(data, flags, strictMode)
			break
		}
		return l.fail(tok, ERRORTOK, l.invalidCharacterMessage())
	}

done:
	// Identifier error tokens (including ESCAPED_KEYWORD) come back without
	// setting the error flag, as in JSC; the parser decides what they mean.
	l.atLineStart = false
	tok.End = l.CurrentPosition()
	return token
}

// fail records an error token, like the returnError label in JSC.
func (l *Lexer) fail(tok *Token, t TokenType, msg string) TokenType {
	if msg != "" {
		l.lexErrorMessage = msg
	}
	l.err = true
	tok.End = l.CurrentPosition()
	return t
}

// scanSingleLineComment skips to the end of the line. It returns (token, true)
// when the caller must return immediately (EOF, or an automatic semicolon after
// a restricted keyword); otherwise the caller restarts scanning.
func (l *Lexer) scanSingleLineComment(tok *Token, checkForDirectives bool) (TokenType, bool) {
	if checkForDirectives && (l.current == '#' || l.current == '@') && isWhiteSpace(l.peek(1)) {
		// Script comment directives like "//# sourceURL=test.js".
		l.shift()
		l.shift()
		l.parseCommentDirective()
	}

	endPosition := l.CurrentPosition()
	for l.pos < l.end && !isLineTerminator(l.code[l.pos]) {
		l.pos++
	}
	if l.pos >= l.end {
		l.current = 0
		tok.End = endPosition
		return EOFTOK, true
	}
	l.loadCurrent()
	l.shiftLineTerminator()
	l.atLineStart = true
	l.hasLineTerminatorBeforeToken = true
	if !isRestrKeyword(tok.Type) {
		return 0, false
	}
	tok.End = endPosition
	return SEMICOLON, true
}

func (l *Lexer) parseMultilineComment() bool {
	for {
		for l.current == '*' {
			l.shift()
			if l.current == '/' {
				l.shift()
				return true
			}
		}
		if l.atEnd() {
			return false
		}
		if isLineTerminator(l.current) {
			l.shiftLineTerminator()
			l.hasLineTerminatorBeforeToken = true
		} else {
			l.shift()
		}
	}
}

func (l *Lexer) consume(s string) bool {
	for i := 0; i < len(s); i++ {
		if l.current != uint16(s[i]) {
			return false
		}
		l.shift()
	}
	return true
}

func (l *Lexer) parseCommentDirective() {
	if !l.consume("source") {
		return
	}
	if l.consume("URL=") {
		l.sourceURLDirective = l.parseCommentDirectiveValue()
		return
	}
	if l.consume("MappingURL=") {
		l.sourceMappingURLDirective = l.parseCommentDirectiveValue()
	}
}

func (l *Lexer) parseCommentDirectiveValue() string {
	l.skipWhitespace()
	start := l.pos
	for !isWhiteSpace(l.current) && !isLineTerminator(l.current) && l.current != '"' && l.current != '\'' && !l.atEnd() {
		l.shift()
	}
	value := l.code[start:l.pos]
	l.skipWhitespace()
	if !isLineTerminator(l.current) && !l.atEnd() {
		return ""
	}
	return ident.EncodeWTF8(value)
}

// parseIdentifier scans an identifier, keyword or private name (#x). Unlike
// JSC it does not special-case short sources or single characters; a keyword
// lookup on the finished identifier gives the same result.
func (l *Lexer) parseIdentifier(data *TokenData, flags LexerFlags, strictMode bool) TokenType {
	data.Escaped = false
	start := l.pos

	isPrivateName := l.current == '#'
	isBuiltinName := l.current == '@' && l.parsingBuiltinFunction
	if isBuiltinName {
		l.shift()
		if l.current == '@' {
			// @@name: well-known symbol. Builtins are not supported yet.
			l.shift()
		}
		start = l.pos
	}
	if isPrivateName {
		l.shift()
	}

	for isSingleCharacterIdentPart(l.current) {
		l.shift()
	}
	if utf16.IsSurrogate(rune(l.current)) || l.current == '\\' {
		return l.parseIdentifierSlowCase(data, flags, strictMode, start, isPrivateName)
	}

	units := l.code[start:l.pos]
	identType := TokenType(IDENT)
	if isPrivateName {
		identType = PRIVATENAME
	}

	shouldBuild := !flags.Has(LexerDontBuildKeywords) || l.parsingBuiltinFunction
	if !isPrivateName && !isBuiltinName && !flags.Has(LexerIgnoreReservedWords) {
		if kw, ok := lookupKeyword(units); ok {
			if kw == RESERVED_IF_STRICT && !strictMode {
				if shouldBuild {
					data.Ident = l.identifier(units)
				}
				return IDENT
			}
			// JSC hands the parser the keyword's identifier too.
			data.Ident = l.identifier(units)
			return kw
		}
	}

	if shouldBuild {
		data.Ident = l.identifier(units)
		if isBuiltinName {
			data.Ident = ident.FromString("@" + data.Ident.String())
		}
	} else {
		data.Ident = ident.Null
	}
	return identType
}

// parseIdentifierSlowCase handles identifiers containing \u escapes or
// characters outside the BMP.
func (l *Lexer) parseIdentifierSlowCase(data *TokenData, flags LexerFlags, strictMode bool, identifierStart int, isPrivateName bool) TokenType {
	identType := TokenType(IDENT)
	charsStart := identifierStart
	if isPrivateName {
		identType = PRIVATENAME
		charsStart++
	}
	l.buffer16 = l.buffer16[:0]
	segmentStart := identifierStart

	fillBuffer := func(isStart bool) TokenType {
		l.buffer16 = append(l.buffer16, l.code[segmentStart:l.pos]...)
		if l.current == '\\' {
			data.Escaped = true
			l.shift()
			if l.current != 'u' {
				if l.atEnd() {
					return UNTERMINATED_IDENTIFIER_ESCAPE_ERRORTOK
				}
				return INVALID_IDENTIFIER_ESCAPE_ERRORTOK
			}
			l.shift()
			ch := l.parseUnicodeEscape()
			if !ch.valid() {
				if ch == unicodeEscapeIncomplete {
					return UNTERMINATED_IDENTIFIER_UNICODE_ESCAPE_ERRORTOK
				}
				return INVALID_IDENTIFIER_UNICODE_ESCAPE_ERRORTOK
			}
			if (isStart && !isIdentStart(rune(ch))) || (!isStart && !isIdentPart(rune(ch))) {
				return INVALID_IDENTIFIER_UNICODE_ESCAPE_ERRORTOK
			}
			l.recordUnicodeCodePoint(rune(ch))
			segmentStart = l.pos
			return identType
		}

		cp := l.currentCodePoint()
		if cp == errorCodePoint {
			return INVALID_UNICODE_ENCODING_ERRORTOK
		}
		if (isStart && !isIdentStart(cp)) || (!isStart && !isIdentPart(cp)) {
			return INVALID_IDENTIFIER_UNICODE_ERRORTOK
		}
		if cp >= 0x10000 {
			l.buffer16 = append(l.buffer16, l.code[l.pos:l.pos+2]...)
			l.shift()
		} else {
			l.buffer16 = append(l.buffer16, l.current)
		}
		l.shift()
		segmentStart = l.pos
		return identType
	}

	if t := fillBuffer(charsStart == l.pos); t.IsError() {
		l.buffer16 = l.buffer16[:0]
		return t
	}
	for {
		if isSingleCharacterIdentPart(l.current) {
			l.shift()
			continue
		}
		if !utf16.IsSurrogate(rune(l.current)) && l.current != '\\' {
			break
		}
		if t := fillBuffer(false); t.IsError() {
			l.buffer16 = l.buffer16[:0]
			return t
		}
	}
	l.buffer16 = append(l.buffer16, l.code[segmentStart:l.pos]...)
	id := ident.FromUTF16(l.buffer16)
	data.Ident = id
	l.buffer16 = l.buffer16[:0]

	// Keywords are ASCII, so the slow path can only spell one through an
	// escape, and an escaped keyword is never a keyword.
	if !flags.Has(LexerIgnoreReservedWords) && !isPrivateName {
		if kw, ok := lookupKeyword(id.UTF16()); ok && (kw != RESERVED_IF_STRICT || strictMode) {
			return ESCAPED_KEYWORD
		}
	}
	return identType
}

// parseString scans a string literal; current is the opening quote. On
// success current is the closing quote.
func (l *Lexer) parseString(data *TokenData, strictMode, shouldBuildStrings bool) stringParseResult {
	quote := l.current
	l.shift()
	l.buffer16 = l.buffer16[:0]
	stringStart := l.pos

	for l.current != quote {
		if l.current == '\\' {
			if shouldBuildStrings {
				l.buffer16 = append(l.buffer16, l.code[stringStart:l.pos]...)
			}
			l.shift()
			if esc := singleEscape(l.current); esc != 0 {
				if shouldBuildStrings {
					l.buffer16 = append(l.buffer16, esc)
				}
				l.shift()
			} else if isLineTerminator(l.current) {
				l.shiftLineTerminator()
			} else if r := l.parseComplexEscape(strictMode, shouldBuildStrings); r != stringParsedSuccessfully {
				return r
			}
			stringStart = l.pos
			continue
		}
		if l.current < 0x0E || isLineTerminator(l.current) {
			// A raw newline or the end of input ends the literal with an error.
			if l.atEnd() || l.current == '\r' || l.current == '\n' {
				l.lexErrorMessage = "Unexpected EOF"
				if l.atEnd() {
					return stringUnterminated
				}
				return stringCannotBeParsed
			}
			// An unescaped LS or PS is allowed and stays part of the value.
		}
		l.shift()
	}

	if shouldBuildStrings {
		l.buffer16 = append(l.buffer16, l.code[stringStart:l.pos]...)
		data.Ident = ident.FromUTF16(l.buffer16)
	} else {
		data.Ident = ident.Null
	}
	l.buffer16 = l.buffer16[:0]
	return stringParsedSuccessfully
}

// parseComplexEscape handles \x, \u, numeric and identity escapes. current is
// the character after the backslash.
func (l *Lexer) parseComplexEscape(strictMode, shouldBuildStrings bool) stringParseResult {
	record := func(c uint16) {
		if shouldBuildStrings {
			l.buffer16 = append(l.buffer16, c)
		}
	}
	unterminatedOr := func(msg string) stringParseResult {
		l.lexErrorMessage = msg
		if l.atEnd() {
			return stringUnterminated
		}
		return stringCannotBeParsed
	}

	switch {
	case l.current == 'x':
		l.shift()
		if !isASCIIHexDigit(l.current) || !isASCIIHexDigit(l.peek(1)) {
			// For raw template literals we consume the NotEscapeSequence.
			if isASCIIHexDigit(l.current) {
				l.shift()
			}
			return unterminatedOr(`\x can only be followed by a hex character sequence`)
		}
		prev := l.current
		l.shift()
		record(convertHex(prev, l.current))
		l.shift()
		return stringParsedSuccessfully

	case l.current == 'u':
		l.shift()
		ch := l.parseUnicodeEscape()
		if ch.valid() {
			if shouldBuildStrings {
				l.recordUnicodeCodePoint(rune(ch))
			}
			return stringParsedSuccessfully
		}
		return unterminatedOr(`\u can only be followed by a Unicode character sequence`)

	case strictMode && isASCIIDigit(l.current):
		// The only valid numeric escape in strict mode is \0 not followed by a digit.
		c1 := l.current
		l.shift()
		if c1 != '0' || isASCIIDigit(l.current) {
			if c1 == '0' {
				l.shift()
			}
			return unterminatedOr(`The only valid numeric escape in strict mode is '\0'`)
		}
		record(0)
		return stringParsedSuccessfully

	case !strictMode && isASCIIOctalDigit(l.current):
		// Legacy octal escapes: up to three digits, value at most 0377.
		c1 := l.current
		l.shift()
		if !isASCIIOctalDigit(l.current) {
			record(c1 - '0')
			return stringParsedSuccessfully
		}
		c2 := l.current
		l.shift()
		if c1 <= '3' && isASCIIOctalDigit(l.current) {
			record((c1-'0')*64 + (c2-'0')*8 + l.current - '0')
			l.shift()
		} else {
			record((c1-'0')*8 + c2 - '0')
		}
		return stringParsedSuccessfully
	}

	if !l.atEnd() {
		record(l.current)
		l.shift()
		return stringParsedSuccessfully
	}
	l.lexErrorMessage = "Unterminated string constant"
	return stringUnterminated
}

// --- Numbers ---------------------------------------------------------------

// scanDigits appends digits accepted by isDigit to buffer8, honouring numeric
// separators. It stops (leaving current on the '_') at a separator that is not
// followed by a digit, or at any separator when separators are not allowed;
// the caller then reports the trailing identifier character as an error.
func (l *Lexer) scanDigits(isDigit func(uint16) bool, allowSeparators bool) {
	for {
		if l.current == '_' {
			if !allowSeparators || !isDigit(l.peek(1)) {
				return
			}
			l.shift()
		}
		if !isDigit(l.current) {
			return
		}
		l.buffer8 = append(l.buffer8, byte(l.current))
		l.shift()
	}
}

// checkAfterNumber reports an error if an identifier start directly follows a
// numeric literal, using errMsg/errTok for that case. It returns 0 on success.
func (l *Lexer) checkAfterNumber(errMsg string, errTok TokenType) (TokenType, string) {
	l.buffer8 = l.buffer8[:0]
	if cannotBeIdentStart(l.current) {
		return 0, ""
	}
	if isIdentStart(l.currentCodePoint()) {
		if errTok == 0 {
			errTok = INVALID_NUMERIC_LITERAL_ERRORTOK
			if l.atEnd() {
				errTok = UNTERMINATED_NUMERIC_LITERAL_ERRORTOK
			}
		}
		return errTok, errMsg
	}
	return 0, ""
}

// scanFraction parses digits after '.', then an optional exponent, and stores
// the value of buffer8 in data.Double. current is the first fraction digit.
func (l *Lexer) scanFraction(data *TokenData) (TokenType, string) {
	l.buffer8 = append(l.buffer8, '.')
	if !l.scanDigitsRequired(isASCIIDigit) {
		return l.numericError("Non-number found after decimal point")
	}
	if isASCIIAlphaCaselessEqual(l.current, 'e') && !l.parseNumberAfterExponentIndicator() {
		return l.numericError("Non-number found after exponent indicator")
	}
	data.Double = parseDouble(l.buffer8)
	return 0, ""
}

// scanDigitsRequired is scanDigits for a position where at least one digit has
// already been checked to be present, failing on a bad separator.
func (l *Lexer) scanDigitsRequired(isDigit func(uint16) bool) bool {
	l.scanDigits(isDigit, true)
	return l.current != '_'
}

func (l *Lexer) numericError(msg string) (TokenType, string) {
	if l.atEnd() {
		return UNTERMINATED_NUMERIC_LITERAL_ERRORTOK, msg
	}
	return INVALID_NUMERIC_LITERAL_ERRORTOK, msg
}

func (l *Lexer) parseNumberAfterExponentIndicator() bool {
	l.buffer8 = append(l.buffer8, 'e')
	l.shift()
	if l.current == '+' || l.current == '-' {
		l.buffer8 = append(l.buffer8, byte(l.current))
		l.shift()
	}
	if !isASCIIDigit(l.current) {
		return false
	}
	return l.scanDigitsRequired(isASCIIDigit)
}

// scanNumber scans a numeric literal starting with a decimal digit and
// returns the token type, or an error token and message.
func (l *Lexer) scanNumber(data *TokenData, strictMode bool) (TokenType, string) {
	l.buffer8 = l.buffer8[:0]

	if l.current == '0' {
		l.shift()
		switch {
		case isASCIIAlphaCaselessEqual(l.current, 'x'):
			return l.scanRadixInteger(data, 16, isASCIIHexDigit, "No hexadecimal digits after '0x'", "No space between hexadecimal literal and identifier", UNTERMINATED_HEX_NUMBER_ERRORTOK)
		case isASCIIAlphaCaselessEqual(l.current, 'b'):
			return l.scanRadixInteger(data, 2, isASCIIBinaryDigit, "No binary digits after '0b'", "No space between binary literal and identifier", UNTERMINATED_BINARY_NUMBER_ERRORTOK)
		case isASCIIAlphaCaselessEqual(l.current, 'o'):
			return l.scanRadixInteger(data, 8, isASCIIOctalDigit, "No octal digits after '0o'", "No space between octal literal and identifier", UNTERMINATED_OCTAL_NUMBER_ERRORTOK)
		case l.current == '_':
			return UNTERMINATED_OCTAL_NUMBER_ERRORTOK, "Numeric literals may not begin with 0_"
		case strictMode && isASCIIDigit(l.current):
			return UNTERMINATED_OCTAL_NUMBER_ERRORTOK, "Decimal integer literals with a leading zero are forbidden in strict mode"
		case isASCIIOctalDigit(l.current):
			// Legacy octal (0777) or, if a non-octal digit follows, legacy
			// decimal with a leading zero (0789). No separators allowed.
			startPos := l.pos
			l.scanDigits(isASCIIOctalDigit, false)
			if !isASCIIDigit(l.current) && l.current != '_' {
				data.Double = parseIntegerDigits(l.buffer8, 8)
				t, msg := l.checkAfterNumber("No identifiers allowed directly after numeric literal", 0)
				if t != 0 {
					return t, msg
				}
				return tokenTypeForIntegerLikeToken(data.Double), ""
			}
			l.pos = startPos
			l.loadCurrent()
			l.buffer8 = append(l.buffer8[:0], '0')
			return l.scanDecimal(data, true)
		case !isASCIIDigit(l.current) && l.current != '.' && cannotBeIdentStart(l.current):
			data.Double = 0
			return INTEGER, ""
		}
		l.buffer8 = append(l.buffer8, '0')
		return l.scanDecimal(data, isASCIIDigit(l.current))
	}
	return l.scanDecimal(data, false)
}

// scanDecimal scans the rest of a decimal literal. buffer8 may already hold a
// leading '0'; isLegacy marks a legacy leading-zero literal (no separators,
// no BigInt suffix).
func (l *Lexer) scanDecimal(data *TokenData, isLegacy bool) (TokenType, string) {
	l.scanDigits(isASCIIDigit, !isLegacy)

	if l.current == 'n' && !isLegacy {
		data.BigIntString = ident.FromString(string(l.buffer8))
		data.Radix = 10
		l.shift()
		if t, msg := l.checkAfterNumber("No identifiers allowed directly after numeric literal", 0); t != 0 {
			return t, msg
		}
		return BIGINT, ""
	}

	token := TokenType(INTEGER)
	if l.current == '.' {
		l.shift()
		if isASCIIDigit(l.current) {
			if t, msg := l.scanFractionDigits(); t != 0 {
				return t, msg
			}
		}
		token = DOUBLE
	}
	if isASCIIAlphaCaselessEqual(l.current, 'e') && !l.parseNumberAfterExponentIndicator() {
		return l.numericError("Non-number found after exponent indicator")
	}
	data.Double = parseDouble(l.buffer8)
	if token == INTEGER {
		token = tokenTypeForIntegerLikeToken(data.Double)
	}
	if t, msg := l.checkAfterNumber("No identifiers allowed directly after numeric literal", 0); t != 0 {
		return t, msg
	}
	return token, ""
}

func (l *Lexer) scanFractionDigits() (TokenType, string) {
	l.buffer8 = append(l.buffer8, '.')
	if !l.scanDigitsRequired(isASCIIDigit) {
		return l.numericError("Non-number found after decimal point")
	}
	return 0, ""
}

// scanRadixInteger scans 0x/0b/0o literals; current is the prefix letter.
func (l *Lexer) scanRadixInteger(data *TokenData, radix uint8, isDigit func(uint16) bool, noDigitsMsg, trailingIdentMsg string, errTok TokenType) (TokenType, string) {
	if !isDigit(l.peek(1)) {
		return errTok, noDigitsMsg
	}
	l.shift() // the prefix letter
	l.scanDigits(isDigit, true)

	token := TokenType(INTEGER)
	switch {
	case l.current == '_':
		data.Double = 0 // bad separator; reported below as a trailing identifier
	case radix != 16 && isASCIIDigit(l.current):
		// e.g. 0b12: JSC yields 0 and leaves the digit for the next token.
		data.Double = 0
	case l.current == 'n':
		data.BigIntString = ident.FromString(string(l.buffer8))
		data.Radix = radix
		token = BIGINT
		l.shift()
	default:
		data.Double = parseIntegerDigits(l.buffer8, int(radix))
	}
	if t, msg := l.checkAfterNumber(trailingIdentMsg, errTok); t != 0 {
		return t, msg
	}
	if token != BIGINT {
		token = tokenTypeForIntegerLikeToken(data.Double)
	}
	return token, ""
}

// tokenTypeForIntegerLikeToken returns INTEGER for values that are exactly
// representable as an int64 (excluding -0), DOUBLE otherwise.
func tokenTypeForIntegerLikeToken(v float64) TokenType {
	if (v != 0 || !math.Signbit(v)) && v >= -(1<<63) && v < 1<<63 && math.Trunc(v) == v {
		return INTEGER
	}
	return DOUBLE
}

// parseDouble converts an ASCII decimal literal (digits, '.', exponent) to the
// nearest double, as WTF::parseDouble does.
func parseDouble(digits []byte) float64 {
	v, err := strconv.ParseFloat(string(digits), 64)
	if err != nil {
		// Out-of-range values are returned as ±Inf by ParseFloat along with
		// an error, which is what JavaScript wants.
		if ne, ok := err.(*strconv.NumError); ok && ne.Err == strconv.ErrRange {
			return v
		}
		return math.NaN()
	}
	return v
}

// parseIntegerDigits converts digits in radix 2, 8, 10 or 16 to the nearest
// double. Short inputs take the exact uint64 path; long ones go through
// math/big for correct rounding (JSC's parseIntOverflow).
func parseIntegerDigits(digits []byte, radix int) float64 {
	if len(digits) == 0 {
		return 0
	}
	if v, err := strconv.ParseUint(string(digits), radix, 64); err == nil && v <= 1<<53 {
		return float64(v)
	}
	n, ok := new(big.Int).SetString(string(digits), radix)
	if !ok {
		return math.NaN()
	}
	f, _ := new(big.Float).SetInt(n).Float64()
	return f
}

// --- Regular expressions and templates -------------------------------------

// ScanRegExp scans a regular expression literal. The parser calls it after
// lexing '/' or '/=' as an operator; patternPrefix is '=' in the latter case.
func (l *Lexer) ScanRegExp(tok *Token, patternPrefix uint16) TokenType {
	data := &tok.Data
	l.buffer16 = l.buffer16[:0]
	lastWasEscape := false
	inBrackets := false

	if patternPrefix != 0 {
		l.buffer16 = append(l.buffer16, patternPrefix)
	}

	for {
		if isLineTerminator(l.current) || l.atEnd() {
			l.buffer16 = l.buffer16[:0]
			tok.End = l.CurrentPosition()
			l.err = true
			l.lexErrorMessage = "Unterminated regular expression literal '" + ident.EncodeWTF8(l.TokenText(tok)) + "'"
			return UNTERMINATED_REGEXP_LITERAL_ERRORTOK
		}
		prev := l.current
		l.shift()
		if prev == '/' && !lastWasEscape && !inBrackets {
			break
		}
		l.buffer16 = append(l.buffer16, prev)
		if lastWasEscape {
			lastWasEscape = false
			continue
		}
		switch prev {
		case '[':
			inBrackets = true
		case ']':
			inBrackets = false
		case '\\':
			lastWasEscape = true
		}
	}

	data.Pattern = ident.FromUTF16(l.buffer16)
	l.buffer16 = l.buffer16[:0]

	l.buffer8 = l.buffer8[:0]
	for l.current < 256 && isIdentPart(rune(l.current)) {
		l.buffer8 = append(l.buffer8, byte(l.current))
		l.shift()
	}
	if l.current >= 256 && !isWhiteSpace(l.current) && !isLineTerminator(l.current) {
		l.buffer8 = l.buffer8[:0]
		tok.End = l.CurrentPosition()
		l.err = true
		cp := "`invalid unicode character`"
		if r := l.currentCodePoint(); r != errorCodePoint {
			cp = string(r)
		}
		l.lexErrorMessage = "Invalid non-latin character in RexExp literal's flags '" + ident.EncodeWTF8(l.TokenText(tok)) + cp + "'"
		return INVALID_IDENTIFIER_UNICODE_ERRORTOK
	}
	data.Flags = ident.FromString(string(l.buffer8))
	l.buffer8 = l.buffer8[:0]

	l.atLineStart = false
	tok.End = l.CurrentPosition()
	return REGEXP
}

// ScanTemplateString scans a template chunk. The leading ` (head) or } (middle
// and tail) has already been consumed by the previous token.
func (l *Lexer) ScanTemplateString(tok *Token, mode RawStringsBuildMode) TokenType {
	token := TokenType(TEMPLATE)
	if result := l.parseTemplateLiteral(&tok.Data, mode); result != stringParsedSuccessfully {
		token = INVALID_TEMPLATE_LITERAL_ERRORTOK
		if result == stringUnterminated {
			token = UNTERMINATED_TEMPLATE_LITERAL_ERRORTOK
		}
		l.err = true
	}
	l.atLineStart = false
	tok.End = l.CurrentPosition()
	return token
}

func (l *Lexer) parseTemplateLiteral(data *TokenData, mode RawStringsBuildMode) stringParseResult {
	buildRaw := mode == BuildRawStrings
	parseCookedFailed := false
	l.buffer16 = l.buffer16[:0]
	l.bufferForRawTemplateString16 = l.bufferForRawTemplateString16[:0]
	stringStart := l.pos
	rawStringStart := l.pos

	for l.current != '`' {
		if l.current == '\\' {
			l.buffer16 = append(l.buffer16, l.code[stringStart:l.pos]...)
			l.shift()
			if esc := singleEscape(l.current); esc != 0 {
				l.buffer16 = append(l.buffer16, esc)
				l.shift()
			} else if isLineTerminator(l.current) {
				// Line continuation; normalize <CR> and <CR><LF> to <LF> in the raw string.
				if l.current == '\r' {
					if buildRaw {
						l.bufferForRawTemplateString16 = append(l.bufferForRawTemplateString16, l.code[rawStringStart:l.pos]...)
						l.bufferForRawTemplateString16 = append(l.bufferForRawTemplateString16, '\n')
					}
					l.shiftLineTerminator()
					rawStringStart = l.pos
				} else {
					l.shiftLineTerminator()
				}
			} else if r := l.parseComplexEscape(true, true); r != stringParsedSuccessfully {
				// Tagged templates may contain invalid escapes; the cooked value is then undefined.
				if buildRaw && r == stringCannotBeParsed {
					parseCookedFailed = true
				} else {
					return r
				}
			}
			stringStart = l.pos
			continue
		}

		if l.current == '$' && l.peek(1) == '{' {
			break
		}

		if l.current < 0x0E || isLineTerminator(l.current) {
			if l.atEnd() {
				l.lexErrorMessage = "Unexpected EOF"
				return stringUnterminated
			}
			if isLineTerminator(l.current) {
				if l.current == '\r' {
					// Normalize <CR> and <CR><LF> to <LF> in both cooked and raw.
					l.buffer16 = append(l.buffer16, l.code[stringStart:l.pos]...)
					if buildRaw {
						l.bufferForRawTemplateString16 = append(l.bufferForRawTemplateString16, l.code[rawStringStart:l.pos]...)
						l.bufferForRawTemplateString16 = append(l.bufferForRawTemplateString16, '\n')
					}
					l.buffer16 = append(l.buffer16, '\n')
					l.shiftLineTerminator()
					stringStart = l.pos
					rawStringStart = l.pos
				} else {
					l.shiftLineTerminator()
				}
				continue
			}
		}
		l.shift()
	}

	isTail := l.current == '`'
	l.buffer16 = append(l.buffer16, l.code[stringStart:l.pos]...)
	if buildRaw {
		l.bufferForRawTemplateString16 = append(l.bufferForRawTemplateString16, l.code[rawStringStart:l.pos]...)
	}

	data.Cooked = ident.Null
	if !parseCookedFailed {
		data.Cooked = ident.FromUTF16(l.buffer16)
	}
	data.Raw = ident.Null
	if buildRaw {
		data.Raw = ident.FromUTF16(l.bufferForRawTemplateString16)
	}
	data.IsTail = isTail

	l.buffer16 = l.buffer16[:0]
	l.bufferForRawTemplateString16 = l.bufferForRawTemplateString16[:0]

	if isTail {
		l.shift() // `
	} else {
		l.shift() // $
		l.shift() // {
	}
	return stringParsedSuccessfully
}
