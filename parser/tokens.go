package parser

import (
	"fmt"

	"github.com/baslentfert/jsc2go/ident"
)

// TokenType mirrors JSC's JSTokenType, including its bit layout, because the
// parser reads operator precedence and flags directly out of the value:
//
//	0b000000000RTE00IIIIPPPPKUXXXXXXXX
//	R = right-associative bit
//	T = unterminated error flag
//	E = error flag
//	I = binary operator allows 'in'
//	P = binary operator precedence
//	K = keyword flag
//	U = unary operator flag
type TokenType uint32

const (
	UnaryOpTokenFlag                               TokenType = 1 << 8
	KeywordTokenFlag                               TokenType = 1 << 9
	BinaryOpTokenPrecedenceShift                             = 10
	BinaryOpTokenAllowsInPrecedenceAdditionalShift           = 4
	BinaryOpTokenPrecedenceMask                    TokenType = 15 << BinaryOpTokenPrecedenceShift
	CanBeErrorTokenFlag                            TokenType = 1 << (BinaryOpTokenAllowsInPrecedenceAdditionalShift + BinaryOpTokenPrecedenceShift + 6)
	UnterminatedCanBeErrorTokenFlag                TokenType = CanBeErrorTokenFlag << 1
	RightAssociativeBinaryOpTokenFlag              TokenType = UnterminatedCanBeErrorTokenFlag << 1
)

func binaryOpPrecedence(prec TokenType) TokenType {
	return prec<<BinaryOpTokenPrecedenceShift | prec<<(BinaryOpTokenPrecedenceShift+BinaryOpTokenAllowsInPrecedenceAdditionalShift)
}

func inOpPrecedence(prec TokenType) TokenType {
	return prec << (BinaryOpTokenPrecedenceShift + BinaryOpTokenAllowsInPrecedenceAdditionalShift)
}

// Keywords.
const (
	NULLTOKEN TokenType = KeywordTokenFlag + iota
	TRUETOKEN
	FALSETOKEN
	BREAK
	CASE
	DEFAULT
	FOR
	NEW
	VAR
	CONSTTOKEN
	CONTINUE
	FUNCTION
	RETURN
	IF
	THISTOKEN
	DO
	WHILE
	SWITCH
	WITH
	RESERVED
	RESERVED_IF_STRICT
	THROW
	TRY
	CATCH
	FINALLY
	DEBUGGER
	ELSE
	IMPORT
	EXPORT_
	CLASSTOKEN
	EXTENDS
	SUPER

	// Contextual keywords.
	LET
	YIELD
	AWAIT

	FirstContextualKeywordToken = LET
	LastContextualKeywordToken  = AWAIT
)

// Untagged punctuators and literals.
const (
	OPENBRACE TokenType = iota
	CLOSEBRACE
	OPENPAREN
	CLOSEPAREN
	OPENBRACKET
	CLOSEBRACKET
	COMMA
	QUESTION
	BACKQUOTE
	INTEGER
	DOUBLE
	BIGINT
	IDENT
	PRIVATENAME
	STRING
	TEMPLATE
	REGEXP
	SEMICOLON
	COLON
	DOT
	EOFTOK
	EQUAL
	PLUSEQUAL
	MINUSEQUAL
	MULTEQUAL
	DIVEQUAL
	LSHIFTEQUAL
	RSHIFTEQUAL
	URSHIFTEQUAL
	MODEQUAL
	POWEQUAL
	BITANDEQUAL
	BITXOREQUAL
	BITOREQUAL
	COALESCEEQUAL
	OREQUAL
	ANDEQUAL
	DOTDOTDOT
	ARROWFUNCTION
	QUESTIONDOT
	LastUntaggedToken
)

// Tagged tokens: unary and binary operators and error tokens.
const (
	PLUSPLUS       = 0 | UnaryOpTokenFlag
	MINUSMINUS     = 1 | UnaryOpTokenFlag
	AUTOPLUSPLUS   = 2 | UnaryOpTokenFlag
	AUTOMINUSMINUS = 3 | UnaryOpTokenFlag
	EXCLAMATION    = 4 | UnaryOpTokenFlag
	TILDE          = 5 | UnaryOpTokenFlag
	TYPEOF         = 6 | UnaryOpTokenFlag | KeywordTokenFlag
	VOIDTOKEN      = 7 | UnaryOpTokenFlag | KeywordTokenFlag
	DELETETOKEN    = 8 | UnaryOpTokenFlag | KeywordTokenFlag

	// Binary operator precedences, inlined from BINARY_OP_PRECEDENCE(prec)
	// so they stay constants: prec<<10 | prec<<14.
	COALESCE   = TokenType(0 | 1<<10 | 1<<14)
	OR         = TokenType(0 | 2<<10 | 2<<14)
	AND        = TokenType(0 | 3<<10 | 3<<14)
	BITOR      = TokenType(0 | 4<<10 | 4<<14)
	BITXOR     = TokenType(0 | 5<<10 | 5<<14)
	BITAND     = TokenType(0 | 6<<10 | 6<<14)
	EQEQ       = TokenType(0 | 7<<10 | 7<<14)
	NE         = TokenType(1 | 7<<10 | 7<<14)
	STREQ      = TokenType(2 | 7<<10 | 7<<14)
	STRNEQ     = TokenType(3 | 7<<10 | 7<<14)
	LT         = TokenType(0 | 8<<10 | 8<<14)
	GT         = TokenType(1 | 8<<10 | 8<<14)
	LE         = TokenType(2 | 8<<10 | 8<<14)
	GE         = TokenType(3 | 8<<10 | 8<<14)
	INSTANCEOF = TokenType(4|8<<10|8<<14) | KeywordTokenFlag
	INTOKEN    = TokenType(5|8<<14) | KeywordTokenFlag // IN_OP_PRECEDENCE(8): only valid where 'in' is allowed
	LSHIFT     = TokenType(0 | 9<<10 | 9<<14)
	RSHIFT     = TokenType(1 | 9<<10 | 9<<14)
	URSHIFT    = TokenType(2 | 9<<10 | 9<<14)
	PLUS       = TokenType(0|10<<10|10<<14) | UnaryOpTokenFlag
	MINUS      = TokenType(1|10<<10|10<<14) | UnaryOpTokenFlag
	TIMES      = TokenType(0 | 11<<10 | 11<<14)
	DIVIDE     = TokenType(1 | 11<<10 | 11<<14)
	MOD        = TokenType(2 | 11<<10 | 11<<14)
	POW        = TokenType(0|12<<10|12<<14) | RightAssociativeBinaryOpTokenFlag // POW must have the highest precedence.

	ERRORTOK                                        = 0 | CanBeErrorTokenFlag
	UNTERMINATED_IDENTIFIER_ESCAPE_ERRORTOK         = 0 | CanBeErrorTokenFlag | UnterminatedCanBeErrorTokenFlag
	INVALID_IDENTIFIER_ESCAPE_ERRORTOK              = 1 | CanBeErrorTokenFlag
	UNTERMINATED_IDENTIFIER_UNICODE_ESCAPE_ERRORTOK = 2 | CanBeErrorTokenFlag | UnterminatedCanBeErrorTokenFlag
	INVALID_IDENTIFIER_UNICODE_ESCAPE_ERRORTOK      = 3 | CanBeErrorTokenFlag
	UNTERMINATED_MULTILINE_COMMENT_ERRORTOK         = 4 | CanBeErrorTokenFlag | UnterminatedCanBeErrorTokenFlag
	UNTERMINATED_NUMERIC_LITERAL_ERRORTOK           = 5 | CanBeErrorTokenFlag | UnterminatedCanBeErrorTokenFlag
	UNTERMINATED_OCTAL_NUMBER_ERRORTOK              = 6 | CanBeErrorTokenFlag | UnterminatedCanBeErrorTokenFlag
	INVALID_NUMERIC_LITERAL_ERRORTOK                = 7 | CanBeErrorTokenFlag
	UNTERMINATED_STRING_LITERAL_ERRORTOK            = 8 | CanBeErrorTokenFlag | UnterminatedCanBeErrorTokenFlag
	INVALID_STRING_LITERAL_ERRORTOK                 = 9 | CanBeErrorTokenFlag
	INVALID_PRIVATE_NAME_ERRORTOK                   = 10 | CanBeErrorTokenFlag
	UNTERMINATED_HEX_NUMBER_ERRORTOK                = 11 | CanBeErrorTokenFlag | UnterminatedCanBeErrorTokenFlag
	UNTERMINATED_BINARY_NUMBER_ERRORTOK             = 12 | CanBeErrorTokenFlag | UnterminatedCanBeErrorTokenFlag
	UNTERMINATED_TEMPLATE_LITERAL_ERRORTOK          = 13 | CanBeErrorTokenFlag | UnterminatedCanBeErrorTokenFlag
	UNTERMINATED_REGEXP_LITERAL_ERRORTOK            = 14 | CanBeErrorTokenFlag | UnterminatedCanBeErrorTokenFlag
	INVALID_TEMPLATE_LITERAL_ERRORTOK               = 15 | CanBeErrorTokenFlag
	ESCAPED_KEYWORD                                 = 16 | CanBeErrorTokenFlag
	INVALID_UNICODE_ENCODING_ERRORTOK               = 17 | CanBeErrorTokenFlag
	INVALID_IDENTIFIER_UNICODE_ERRORTOK             = 18 | CanBeErrorTokenFlag
)

// JSTokenType must fit in 24 bits; this fails to compile if POW grows past that.
var _ = [1]struct{}{}[POW>>24]

// The precedences above are inlined so they stay constants; make sure they
// still agree with the BINARY_OP_PRECEDENCE / IN_OP_PRECEDENCE helpers.
func init() {
	if COALESCE != binaryOpPrecedence(1) || POW&^RightAssociativeBinaryOpTokenFlag != binaryOpPrecedence(12) || INTOKEN&^KeywordTokenFlag != 5|inOpPrecedence(8) {
		panic("parser: token precedence encoding drifted")
	}
}

// IsUpdateOp reports whether t is ++ or -- (prefix or postfix).
func (t TokenType) IsUpdateOp() bool { return t >= PLUSPLUS && t <= AUTOMINUSMINUS }

// IsUnaryOp reports whether t can start a unary expression.
func (t TokenType) IsUnaryOp() bool { return t&UnaryOpTokenFlag != 0 }

// IsKeyword reports whether t is a (non-contextual) keyword token.
func (t TokenType) IsKeyword() bool { return t&KeywordTokenFlag != 0 }

// IsError reports whether t is one of the error tokens.
func (t TokenType) IsError() bool { return t&CanBeErrorTokenFlag != 0 }

// IsUnterminatedError reports whether the error was caused by running out of input.
func (t TokenType) IsUnterminatedError() bool { return t&UnterminatedCanBeErrorTokenFlag != 0 }

// Precedence returns the binary operator precedence, or 0 if t is not a binary operator.
func (t TokenType) Precedence(allowIn bool) int {
	if allowIn {
		return int(t>>(BinaryOpTokenPrecedenceShift+BinaryOpTokenAllowsInPrecedenceAdditionalShift)) & 15
	}
	return int(t&BinaryOpTokenPrecedenceMask) >> BinaryOpTokenPrecedenceShift
}

var tokenNames = map[TokenType]string{
	NULLTOKEN: "null", TRUETOKEN: "true", FALSETOKEN: "false", BREAK: "break", CASE: "case",
	DEFAULT: "default", FOR: "for", NEW: "new", VAR: "var", CONSTTOKEN: "const", CONTINUE: "continue",
	FUNCTION: "function", RETURN: "return", IF: "if", THISTOKEN: "this", DO: "do", WHILE: "while",
	SWITCH: "switch", WITH: "with", RESERVED: "reserved", RESERVED_IF_STRICT: "reserved-if-strict",
	THROW: "throw", TRY: "try", CATCH: "catch", FINALLY: "finally", DEBUGGER: "debugger", ELSE: "else",
	IMPORT: "import", EXPORT_: "export", CLASSTOKEN: "class", EXTENDS: "extends", SUPER: "super",
	LET: "let", YIELD: "yield", AWAIT: "await",

	OPENBRACE: "{", CLOSEBRACE: "}", OPENPAREN: "(", CLOSEPAREN: ")", OPENBRACKET: "[",
	CLOSEBRACKET: "]", COMMA: ",", QUESTION: "?", BACKQUOTE: "`", INTEGER: "INTEGER",
	DOUBLE: "DOUBLE", BIGINT: "BIGINT", IDENT: "IDENT", PRIVATENAME: "PRIVATENAME",
	STRING: "STRING", TEMPLATE: "TEMPLATE", REGEXP: "REGEXP", SEMICOLON: ";", COLON: ":",
	DOT: ".", EOFTOK: "EOF", EQUAL: "=", PLUSEQUAL: "+=", MINUSEQUAL: "-=", MULTEQUAL: "*=",
	DIVEQUAL: "/=", LSHIFTEQUAL: "<<=", RSHIFTEQUAL: ">>=", URSHIFTEQUAL: ">>>=", MODEQUAL: "%=",
	POWEQUAL: "**=", BITANDEQUAL: "&=", BITXOREQUAL: "^=", BITOREQUAL: "|=", COALESCEEQUAL: "??=",
	OREQUAL: "||=", ANDEQUAL: "&&=", DOTDOTDOT: "...", ARROWFUNCTION: "=>", QUESTIONDOT: "?.",

	PLUSPLUS: "++", MINUSMINUS: "--", AUTOPLUSPLUS: "++", AUTOMINUSMINUS: "--", EXCLAMATION: "!",
	TILDE: "~", TYPEOF: "typeof", VOIDTOKEN: "void", DELETETOKEN: "delete", COALESCE: "??",
	OR: "||", AND: "&&", BITOR: "|", BITXOR: "^", BITAND: "&", EQEQ: "==", NE: "!=", STREQ: "===",
	STRNEQ: "!==", LT: "<", GT: ">", LE: "<=", GE: ">=", INSTANCEOF: "instanceof", INTOKEN: "in",
	LSHIFT: "<<", RSHIFT: ">>", URSHIFT: ">>>", PLUS: "+", MINUS: "-", TIMES: "*", DIVIDE: "/",
	MOD: "%", POW: "**",

	ERRORTOK:                                        "ERROR",
	UNTERMINATED_IDENTIFIER_ESCAPE_ERRORTOK:         "UNTERMINATED_IDENTIFIER_ESCAPE",
	INVALID_IDENTIFIER_ESCAPE_ERRORTOK:              "INVALID_IDENTIFIER_ESCAPE",
	UNTERMINATED_IDENTIFIER_UNICODE_ESCAPE_ERRORTOK: "UNTERMINATED_IDENTIFIER_UNICODE_ESCAPE",
	INVALID_IDENTIFIER_UNICODE_ESCAPE_ERRORTOK:      "INVALID_IDENTIFIER_UNICODE_ESCAPE",
	UNTERMINATED_MULTILINE_COMMENT_ERRORTOK:         "UNTERMINATED_MULTILINE_COMMENT",
	UNTERMINATED_NUMERIC_LITERAL_ERRORTOK:           "UNTERMINATED_NUMERIC_LITERAL",
	UNTERMINATED_OCTAL_NUMBER_ERRORTOK:              "UNTERMINATED_OCTAL_NUMBER",
	INVALID_NUMERIC_LITERAL_ERRORTOK:                "INVALID_NUMERIC_LITERAL",
	UNTERMINATED_STRING_LITERAL_ERRORTOK:            "UNTERMINATED_STRING_LITERAL",
	INVALID_STRING_LITERAL_ERRORTOK:                 "INVALID_STRING_LITERAL",
	INVALID_PRIVATE_NAME_ERRORTOK:                   "INVALID_PRIVATE_NAME",
	UNTERMINATED_HEX_NUMBER_ERRORTOK:                "UNTERMINATED_HEX_NUMBER",
	UNTERMINATED_BINARY_NUMBER_ERRORTOK:             "UNTERMINATED_BINARY_NUMBER",
	UNTERMINATED_TEMPLATE_LITERAL_ERRORTOK:          "UNTERMINATED_TEMPLATE_LITERAL",
	UNTERMINATED_REGEXP_LITERAL_ERRORTOK:            "UNTERMINATED_REGEXP_LITERAL",
	INVALID_TEMPLATE_LITERAL_ERRORTOK:               "INVALID_TEMPLATE_LITERAL",
	ESCAPED_KEYWORD:                                 "ESCAPED_KEYWORD",
	INVALID_UNICODE_ENCODING_ERRORTOK:               "INVALID_UNICODE_ENCODING",
	INVALID_IDENTIFIER_UNICODE_ERRORTOK:             "INVALID_IDENTIFIER_UNICODE",
}

func (t TokenType) String() string {
	if s, ok := tokenNames[t]; ok {
		return s
	}
	return fmt.Sprintf("TokenType(%#x)", uint32(t))
}

// TextPosition is a source offset in UTF-16 code units. JSC's JSTextPosition
// default-constructs to -1, which NoTextPosition represents here.
type TextPosition int

const NoTextPosition TextPosition = -1

// IsSet reports whether p refers to an actual position.
func (p TextPosition) IsSet() bool { return p != NoTextPosition }

// TokenData is JSC's JSTokenData. C++ uses a union; Go uses a plain struct,
// and which fields are meaningful depends on the token type:
//
//	IDENT, PRIVATENAME, STRING: Ident, Escaped
//	INTEGER, DOUBLE:            Double
//	BIGINT:                     BigIntString, Radix
//	TEMPLATE:                   Cooked (null if the escape was invalid), Raw, IsTail
//	REGEXP:                     Pattern, Flags
type TokenData struct {
	Ident        ident.Identifier
	Escaped      bool
	Double       float64
	BigIntString ident.Identifier
	Radix        uint8
	Cooked       ident.Identifier
	Raw          ident.Identifier
	IsTail       bool
	Pattern      ident.Identifier
	Flags        ident.Identifier
}

// TokenLocation is the [start, end) offset range of a token.
type TokenLocation struct {
	StartOffset int
	EndOffset   int
}

// Token is JSC's JSToken.
type Token struct {
	Type  TokenType
	Data  TokenData
	Start TextPosition
	End   TextPosition
}

func (t *Token) Location() TokenLocation {
	return TokenLocation{StartOffset: int(t.Start), EndOffset: int(t.End)}
}

func (t Token) String() string {
	switch t.Type {
	case IDENT, PRIVATENAME, STRING:
		return fmt.Sprintf("%v(%q)@%d", t.Type, t.Data.Ident.String(), t.Start)
	case INTEGER, DOUBLE:
		return fmt.Sprintf("%v(%v)@%d", t.Type, t.Data.Double, t.Start)
	case BIGINT:
		return fmt.Sprintf("BIGINT(%s, radix %d)@%d", t.Data.BigIntString.String(), t.Data.Radix, t.Start)
	case REGEXP:
		return fmt.Sprintf("REGEXP(/%s/%s)@%d", t.Data.Pattern.String(), t.Data.Flags.String(), t.Start)
	case TEMPLATE:
		return fmt.Sprintf("TEMPLATE(%q, tail=%v)@%d", t.Data.Cooked.String(), t.Data.IsTail, t.Start)
	}
	return fmt.Sprintf("%v@%d", t.Type, t.Start)
}
