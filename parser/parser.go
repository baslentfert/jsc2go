package parser

import (
	"fmt"
	"strings"

	"github.com/baslentfert/jsc2go/ident"
)

// Port of the Parser class from JavaScriptCore/parser/Parser.{h,cpp}.
//
// Error handling: JSC returns 0 from every parse function and checks the
// result at each call site through macros (failIfFalse, propagateError, ...).
// Here a failure sets the error message and panics with parseBailout, which
// unwinds to the nearest recovery point: the top-level parse, or one of the
// few places where JSC speculatively parses and backtracks (see tryParse).
// C++ RAII cleanups (SetForScope, AutoPopScope, ...) become defers so they
// also run while unwinding.

type parseBailout struct{}

// maxRecursionDepth replaces JSC's VM::isSafeToRecurse stack check.
const maxRecursionDepth = 10000

type SourceElementsMode uint8

const (
	CheckForStrictMode SourceElementsMode = iota
	DontCheckForStrictMode
)

type FunctionBodyType uint8

const (
	ArrowFunctionBodyExpression FunctionBodyType = iota
	ArrowFunctionBodyBlock
	StandardFunctionBodyBlock
)

type FunctionNameRequirements uint8

const (
	FunctionNameRequirementsNone FunctionNameRequirements = iota
	FunctionNameRequirementsNamed
	FunctionNameRequirementsUnnamed
)

type DestructuringKind uint8

const (
	DestructureToVariables DestructuringKind = iota
	DestructureToLet
	DestructureToConst
	DestructureToCatchParameters
	DestructureToParameters
	DestructureToExpressions
)

type DeclarationDefaultContext uint8

const (
	DeclarationDefaultStandard DeclarationDefaultContext = iota
	DeclarationDefaultExportDefault
)

type ParsingContext uint8

const (
	ParsingContextNormal ParsingContext = iota
	ParsingContextFunctionConstructor
)

type exportType uint8

const (
	exported exportType = iota
	notExported
)

type functionDeclarationType uint8

const (
	functionDeclarationTypeDeclaration functionDeclarationType = iota
	functionDeclarationTypeStatement
)

type functionDefinitionType uint8

const (
	functionDefinitionExpression functionDefinitionType = iota
	functionDefinitionDeclaration
	functionDefinitionMethod
)

type blockType uint8

const (
	blockTypeNormal blockType = iota
	blockTypeCatchBlock
	blockTypeStaticBlock
)

type functionParsePhase uint8

const (
	functionParsePhaseParameters functionParsePhase = iota
	functionParsePhaseBody
)

type argumentType uint8

const (
	argumentTypeNormal argumentType = iota
	argumentTypeSpread
)

type varDeclarationListContext uint8

const (
	forLoopContext varDeclarationListContext = iota
	varDeclarationContext
)

type importSpecifierType uint8

const (
	namespaceImport importSpecifierType = iota
	namedImport
	defaultImport
)

// ClassElementDefinitionKind describes a class field initializer to reparse.
type ClassElementDefinitionKind uint8

const (
	ClassElementFieldWithLiteralPropertyKey ClassElementDefinitionKind = iota
	ClassElementFieldWithComputedPropertyKey
	ClassElementFieldWithPrivatePropertyKey
	ClassElementStaticInitializationBlock
)

// ClassElementDefinition is UnlinkedFunctionExecutable::ClassElementDefinition:
// the information needed to reparse a class's field initializers.
type ClassElementDefinition struct {
	Ident               ident.Identifier
	Position            TextPosition
	InitializerPosition TextPosition // NoTextPosition when there is no initializer
	Kind                ClassElementDefinitionKind
}

type parserState struct {
	assignmentCount                int
	nonLHSCount                    int
	nonTrivialExpressionCount      int
	returnStatementCount           int
	unaryTokenStackDepth           int
	functionParsePhase             functionParsePhase
	lastIdentifier                 ident.Identifier
	lastFunctionName               ident.Identifier
	lastPrivateName                ident.Identifier
	allowAwait                     bool
	isParsingClassFieldInitializer bool
	classFieldInitMasksAsync       bool
}

type lexerState struct {
	startOffset                  int
	lastTokenLocation            TokenLocation
	hasLineTerminatorBeforeToken bool
	lastTokenType                TokenType
}

type savePoint struct {
	parserState parserState
	lexerState  lexerState
}

type savePointWithError struct {
	savePoint
	lexerError         bool
	lexerErrorMessage  string
	parserErrorMessage string
	parserHasError     bool
}

type callOrApplyDepthScope struct {
	parent                *callOrApplyDepthScope
	depth                 int
	depthOfInnermostChild int
}

// Parser parses one SourceCode.
type Parser struct {
	options Options

	token             Token
	lastTokenLocation TokenLocation
	currentScope      *Scope
	scopeDepth        int

	source          *SourceCode
	lexer           *Lexer
	errorMessage    string
	hasErrorFlag    bool
	functionCache   *sourceProviderCache
	moduleScopeData *ModuleScopeData
	lastTokenType   TokenType
	statementDepth  int
	functionMode    FunctionMode

	immediateParentAllowsFunctionDeclarationInStatement bool
	insideSwitchCaseBody                                bool
	parsingBuiltin                                      bool
	isEvalContext                                       bool

	parserState                                   parserState
	allowsIn                                      bool
	implementationVisibility                      ImplementationVisibility
	parseMode                                     SourceParseMode
	constructorKindForTopLevelFunctionExpressions ConstructorKind
	isInsideOrdinaryFunction                      bool
	seenTaggedTemplateInNonReparsingFunctionMode  bool
	seenPrivateNameUseInNonReparsingFunctionMode  bool
	seenArgumentsDotLength                        bool

	callOrApplyDepthScope *callOrApplyDepthScope
	scriptMode            ScriptMode
	superBinding          SuperBinding
	hasStackOverflow      bool
	recursionDepth        int
}

// ParserConfig holds the parameters of JSC's Parser constructor.
type ParserConfig struct {
	ImplementationVisibility ImplementationVisibility
	BuiltinMode              BuiltinMode
	LexicallyScopedFeatures  LexicallyScopedFeatures
	ScriptMode               ScriptMode
	ParseMode                SourceParseMode
	FunctionMode             FunctionMode
	SuperBinding             SuperBinding
	ConstructorKind          ConstructorKind
	DerivedContextType       DerivedContextType
	IsEvalContext            bool
	EvalContextType          EvalContextType
	IsInsideOrdinaryFunction bool
	Options                  *Options
}

func newParser(source *SourceCode, cfg ParserConfig) *Parser {
	p := &Parser{
		source:                   source,
		functionMode:             cfg.FunctionMode,
		parsingBuiltin:           cfg.BuiltinMode == BuiltinModeBuiltin,
		allowsIn:                 true,
		implementationVisibility: cfg.ImplementationVisibility,
		parseMode:                cfg.ParseMode,
		isInsideOrdinaryFunction: cfg.IsInsideOrdinaryFunction,
		scriptMode:               cfg.ScriptMode,
		superBinding:             cfg.SuperBinding,
		isEvalContext:            cfg.IsEvalContext,
		lastTokenType:            ERRORTOK,
		options:                  DefaultOptions,
	}
	p.parserState.allowAwait = true
	p.parserState.functionParsePhase = functionParsePhaseBody
	if cfg.Options != nil {
		p.options = *cfg.Options
	}
	p.lexer = NewLexer(cfg.BuiltinMode, cfg.ScriptMode)
	p.lexer.SetCode(*source)
	p.token.Start = TextPosition(source.StartOffset)
	p.token.End = TextPosition(source.StartOffset)
	if UseSourceProviderCache {
		p.functionCache = &source.Provider.cache
	}

	scope := p.pushScope()
	scope.lexicallyScopedFeatures = cfg.LexicallyScopedFeatures
	scope.setSourceParseMode(cfg.ParseMode)
	scope.isEvalContext = cfg.IsEvalContext
	if cfg.IsEvalContext {
		scope.evalContextType = cfg.EvalContextType
	}
	if scope.isFunction {
		scope.constructorKind = cfg.ConstructorKind
	}
	scope.derivedContextType = cfg.DerivedContextType
	if cfg.DerivedContextType != DerivedContextTypeNone {
		scope.expectedSuperBinding = SuperBindingNeeded
	}
	if cfg.ParseMode.IsModule() {
		p.moduleScopeData = &ModuleScopeData{}
	}
	p.next(0)
	return p
}

// --- Errors -----------------------------------------------------------------------

func (p *Parser) hasError() bool { return p.hasErrorFlag }

func (p *Parser) setErrorMessage(msg string) {
	if msg == "" {
		msg = "Unparseable script"
	}
	p.errorMessage = msg
	p.hasErrorFlag = true
}

func (p *Parser) clearError() {
	p.errorMessage = ""
	p.hasErrorFlag = false
}

func (p *Parser) bail() { panic(parseBailout{}) }

// errorArg formats one piece of an error message like WTF's print would.
func errorArg(a any) string {
	switch v := a.(type) {
	case string:
		return v
	case ident.Identifier:
		return v.Description()
	case []uint16:
		return ident.EncodeWTF8(v)
	case TokenType:
		return v.String()
	}
	return fmt.Sprint(a)
}

// logError is JSC's logError(shouldPrintToken, args...): it records the
// first error only.
func (p *Parser) logError(shouldPrintToken bool, args ...any) {
	if p.hasError() {
		return
	}
	var b strings.Builder
	if len(args) == 0 {
		p.setErrorMessage(p.unexpectedTokenText())
		return
	}
	if shouldPrintToken {
		b.WriteString(p.unexpectedTokenText())
		b.WriteString(". ")
	}
	for _, a := range args {
		b.WriteString(errorArg(a))
	}
	b.WriteByte('.')
	p.setErrorMessage(b.String())
}

// propagateError bails out if an error is pending.
func (p *Parser) propagateError() {
	if p.hasError() {
		p.bail()
	}
}

func (p *Parser) internalFailWithMessage(shouldPrintToken bool, args ...any) {
	p.propagateError()
	p.logError(shouldPrintToken, args...)
	p.bail()
}

// handleErrorToken reports a lexer error token as an unexpected token.
func (p *Parser) handleErrorToken() {
	if p.token.Type == EOFTOK || p.token.Type&CanBeErrorTokenFlag != 0 {
		p.failDueToUnexpectedToken()
	}
}

func (p *Parser) failDueToUnexpectedToken() {
	p.logError(true)
	p.bail()
}

func (p *Parser) failWithMessage(args ...any) {
	p.handleErrorToken()
	p.propagateError()
	p.logError(true, args...)
	p.bail()
}

func (p *Parser) failIfFalse(cond bool, args ...any) {
	if !cond {
		p.handleErrorToken()
		p.internalFailWithMessage(true, args...)
	}
}

func (p *Parser) failIfTrue(cond bool, args ...any) { p.failIfFalse(!cond, args...) }

func (p *Parser) failIfTrueIfStrict(cond bool, args ...any) {
	if cond && p.strictMode() {
		p.internalFailWithMessage(false, args...)
	}
}

func (p *Parser) failIfFalseIfStrict(cond bool, args ...any) {
	if !cond && p.strictMode() {
		p.internalFailWithMessage(false, args...)
	}
}

func (p *Parser) consumeOrFail(t TokenType, args ...any) {
	p.consumeOrFailWithFlags(t, 0, args...)
}

func (p *Parser) consumeOrFailWithFlags(t TokenType, flags LexerFlags, args ...any) {
	if !p.consume(t, flags) {
		p.handleErrorToken()
		p.internalFailWithMessage(true, args...)
	}
}

func (p *Parser) matchOrFail(t TokenType, args ...any) {
	if !p.match(t) {
		p.handleErrorToken()
		p.internalFailWithMessage(true, args...)
	}
}

func (p *Parser) semanticFail(args ...any) { p.internalFailWithMessage(false, args...) }

func (p *Parser) semanticFailIfTrue(cond bool, args ...any) {
	if cond {
		p.internalFailWithMessage(false, args...)
	}
}

func (p *Parser) semanticFailIfFalse(cond bool, args ...any) {
	if !cond {
		p.internalFailWithMessage(false, args...)
	}
}

func (p *Parser) regexFail(msg string) {
	p.setErrorMessage(msg)
	p.bail()
}

func (p *Parser) handleProductionOrFail(t TokenType, tokenString, operation, production string) {
	p.consumeOrFail(t, "Expected '", tokenString, "' to ", operation, " a ", production)
}

func (p *Parser) handleProductionOrFail2(t TokenType, tokenString, operation, production string) {
	p.consumeOrFail(t, "Expected '", tokenString, "' to ", operation, " an ", production)
}

// semanticFailureDueToKeywordCheckingToken reports reserved words and
// keywords used as names.
func (p *Parser) semanticFailureDueToKeywordCheckingToken(tok *Token, args ...any) {
	text := p.tokenText(tok)
	with := func(prefix []any, suffix ...any) []any {
		out := append(prefix, args...)
		return append(out, suffix...)
	}
	p.semanticFailIfTrue(p.strictMode() && tok.Type == RESERVED_IF_STRICT, with([]any{"Cannot use the reserved word '", text, "' as a "}, " in strict mode")...)
	p.semanticFailIfTrue(tok.Type == RESERVED || tok.Type == RESERVED_IF_STRICT, with([]any{"Cannot use the reserved word '", text, "' as a "})...)
	if tok.Type&KeywordTokenFlag != 0 {
		p.semanticFailIfFalse(isContextualKeyword(tok), with([]any{"Cannot use the keyword '", text, "' as a "})...)
		if tok.Type == LET && p.strictMode() {
			p.semanticFail(with([]any{"Cannot use 'let' as a "}, " ", p.disallowedIdentifierLetReason())...)
		}
		if tok.Type == AWAIT && !p.canUseIdentifierAwait() {
			p.semanticFail(with([]any{"Cannot use 'await' as a "}, " ", p.disallowedIdentifierAwaitReason())...)
		}
		if tok.Type == YIELD && !p.canUseIdentifierYield() {
			p.semanticFail(with([]any{"Cannot use 'yield' as a "}, " ", p.disallowedIdentifierYieldReason())...)
		}
	}
}

func (p *Parser) semanticFailureDueToKeyword(args ...any) {
	tok := p.token
	p.semanticFailureDueToKeywordCheckingToken(&tok, args...)
}

// failIfStackOverflow / enterRecursion: JSC checks the native stack; we
// bound the recursion depth. Callers must defer p.exitRecursion().
func (p *Parser) enterRecursion() {
	p.recursionDepth++
	if p.recursionDepth > maxRecursionDepth {
		p.recursionDepth--
		p.propagateError()
		p.logError(false, "Stack exhausted")
		p.hasStackOverflow = true
		p.bail()
	}
}

func (p *Parser) exitRecursion() { p.recursionDepth-- }

// tryParse runs f and reports whether it completed without bailing out. On
// bailout the error message stays set, exactly as when a JSC parse function
// returns 0.
func tryParse[T any](p *Parser, f func() T) (result T, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			if _, isBailout := r.(parseBailout); !isBailout {
				panic(r)
			}
			ok = false
		}
	}()
	return f(), true
}

func (p *Parser) unexpectedTokenText() string {
	text := ident.EncodeWTF8(p.getToken())
	switch p.token.Type {
	case EOFTOK:
		return "Unexpected end of script"
	case UNTERMINATED_IDENTIFIER_ESCAPE_ERRORTOK, UNTERMINATED_IDENTIFIER_UNICODE_ESCAPE_ERRORTOK:
		return "Incomplete unicode escape in identifier: '" + text + "'"
	case UNTERMINATED_MULTILINE_COMMENT_ERRORTOK:
		return "Unterminated multiline comment"
	case UNTERMINATED_NUMERIC_LITERAL_ERRORTOK:
		return "Unterminated numeric literal '" + text + "'"
	case UNTERMINATED_STRING_LITERAL_ERRORTOK:
		return "Unterminated string literal '" + text + "'"
	case INVALID_IDENTIFIER_ESCAPE_ERRORTOK:
		return "Invalid escape in identifier: '" + text + "'"
	case ESCAPED_KEYWORD:
		return "Unexpected escaped characters in keyword token: '" + text + "'"
	case INVALID_IDENTIFIER_UNICODE_ESCAPE_ERRORTOK:
		return "Invalid unicode escape in identifier: '" + text + "'"
	case INVALID_NUMERIC_LITERAL_ERRORTOK:
		return "Invalid numeric literal: '" + text + "'"
	case UNTERMINATED_OCTAL_NUMBER_ERRORTOK:
		return "Invalid use of octal: '" + text + "'"
	case INVALID_STRING_LITERAL_ERRORTOK:
		return "Invalid string literal: '" + text + "'"
	case INVALID_UNICODE_ENCODING_ERRORTOK:
		return "Invalid unicode encoding: '" + text + "'"
	case INVALID_IDENTIFIER_UNICODE_ERRORTOK:
		return "Invalid unicode code point in identifier: '" + text + "'"
	case ERRORTOK:
		return "Unrecognized token '" + text + "'"
	case STRING:
		return "Unexpected string literal " + text
	case INTEGER, DOUBLE:
		return "Unexpected number '" + text + "'"
	case RESERVED_IF_STRICT:
		return "Unexpected use of reserved word '" + text + "' in strict mode"
	case RESERVED:
		return "Unexpected use of reserved word '" + text + "'"
	case INVALID_PRIVATE_NAME_ERRORTOK:
		return "Invalid private name '" + text + "'"
	case PRIVATENAME:
		return "Unexpected private name " + text
	case AWAIT, IDENT:
		return "Unexpected identifier '" + text + "'"
	}
	if p.token.Type&KeywordTokenFlag != 0 {
		return "Unexpected keyword '" + text + "'"
	}
	return "Unexpected token '" + text + "'"
}

// --- Tokens -------------------------------------------------------------------------

func (p *Parser) next(flags LexerFlags) {
	p.lastTokenLocation = p.token.Location()
	p.lastTokenType = p.token.Type
	p.token.Type = p.lexer.Lex(&p.token, flags, p.strictMode())
}

func (p *Parser) nextWithoutClearingLineTerminator(flags LexerFlags) {
	p.lastTokenLocation = p.token.Location()
	p.lastTokenType = p.token.Type
	p.token.Type = p.lexer.LexWithoutClearingLineTerminator(&p.token, flags, p.strictMode())
}

func (p *Parser) lexCurrentTokenAgainUnderCurrentContext(ctx treeBuilder) {
	sp := p.createSavePoint(ctx)
	p.restoreSavePoint(ctx, sp)
}

func (p *Parser) nextTokenIsColon() bool { return p.lexer.NextTokenIsColon() }

func (p *Parser) consume(expected TokenType, flags LexerFlags) bool {
	if p.token.Type == expected {
		p.next(flags)
		return true
	}
	return false
}

func (p *Parser) getToken() []uint16 { return p.tokenText(&p.token) }

func (p *Parser) tokenText(t *Token) []uint16 {
	if t.Start < 0 || t.End < t.Start || int(t.End) > len(p.lexer.code) {
		return nil
	}
	return p.lexer.TokenText(t)
}

func (p *Parser) match(expected TokenType) bool { return p.token.Type == expected }

func (p *Parser) matchContextualKeyword(id ident.Identifier) bool {
	return p.token.Type == IDENT && p.token.Data.Ident == id && !p.token.Data.Escaped
}

func isIdentifierOrKeyword(t *Token) bool {
	return t.Type == IDENT || t.Type&KeywordTokenFlag != 0
}

// isContextualKeyword: let, yield and await may be keywords or identifiers.
func isContextualKeyword(t *Token) bool {
	return t.Type >= FirstContextualKeywordToken && t.Type <= LastContextualKeywordToken
}

func (p *Parser) matchIdentifierOrKeyword() bool { return isIdentifierOrKeyword(&p.token) }

func (p *Parser) tokenStart() int                  { return int(p.token.Start) }
func (p *Parser) tokenStartPosition() TextPosition { return p.token.Start }
func (p *Parser) tokenEndPosition() TextPosition   { return p.token.End }
func (p *Parser) tokenLocation() TokenLocation     { return p.token.Location() }
func (p *Parser) lastTokenEndPosition() TextPosition {
	return TextPosition(p.lastTokenLocation.EndOffset)
}
func (p *Parser) locationBeforeLastToken() TokenLocation { return p.lastTokenLocation }

func (p *Parser) allowAutomaticSemicolon() bool {
	return p.match(CLOSEBRACE) || p.match(EOFTOK) || p.lexer.HasLineTerminatorBeforeToken()
}

func (p *Parser) autoSemiColon() bool {
	if p.token.Type == SEMICOLON {
		p.next(0)
		return true
	}
	return p.allowAutomaticSemicolon()
}

func (p *Parser) isArgumentsIdentifier() bool { return p.token.Data.Ident == ident.Arguments }

func (p *Parser) isPossiblyEscapedLet(t *Token) bool {
	return t.Type == LET || (t.Type == ESCAPED_KEYWORD && t.Data.Ident == ident.Let)
}

func (p *Parser) isAllowedIdentifierLet(t *Token) bool {
	return p.isPossiblyEscapedLet(t) && !p.strictMode()
}

func (p *Parser) isPossiblyEscapedAwait(t *Token) bool {
	return t.Type == AWAIT || (t.Type == ESCAPED_KEYWORD && t.Data.Ident == ident.Await)
}

func (p *Parser) isDisallowedIdentifierAwait(t *Token) bool {
	return p.isPossiblyEscapedAwait(t) && !p.canUseIdentifierAwait()
}

func (p *Parser) isAllowedIdentifierAwait(t *Token) bool {
	return p.isPossiblyEscapedAwait(t) && p.canUseIdentifierAwait()
}

func (p *Parser) canUseIdentifierAwait() bool {
	return p.parserState.allowAwait && !p.currentScope.isAsyncFunction && !p.currentScope.isStaticBlock && p.scriptMode != ScriptModeModule
}

func (p *Parser) isPossiblyEscapedYield(t *Token) bool {
	return t.Type == YIELD || (t.Type == ESCAPED_KEYWORD && t.Data.Ident == ident.Yield)
}

func (p *Parser) isDisallowedIdentifierYield(t *Token) bool {
	return p.isPossiblyEscapedYield(t) && !p.canUseIdentifierYield()
}

func (p *Parser) isAllowedIdentifierYield(t *Token) bool {
	return p.isPossiblyEscapedYield(t) && p.canUseIdentifierYield()
}

func (p *Parser) canUseIdentifierYield() bool {
	return !p.strictMode() && !p.currentScope.isGeneratorFunction
}

func (p *Parser) matchAllowedEscapedContextualKeyword() bool {
	id := p.token.Data.Ident
	return (id == ident.Let && !p.strictMode()) ||
		(id == ident.Await && p.canUseIdentifierAwait()) ||
		(id == ident.Yield && p.canUseIdentifierYield())
}

func (p *Parser) disallowedIdentifierLetReason() string { return "in strict mode" }

func (p *Parser) disallowedIdentifierAwaitReason() string {
	switch {
	case !p.parserState.allowAwait || p.currentScope.isAsyncFunction:
		return "in an async function"
	case p.currentScope.isStaticBlock:
		return "in a static block"
	case p.scriptMode == ScriptModeModule:
		return "in a module"
	}
	return ""
}

func (p *Parser) disallowedIdentifierYieldReason() string {
	switch {
	case p.strictMode():
		return "in strict mode"
	case p.currentScope.isGeneratorFunction:
		return "in a generator function"
	}
	return ""
}

func (p *Parser) matchSpecIdentifier() bool {
	return p.match(IDENT) || p.isAllowedIdentifierLet(&p.token) || p.isAllowedIdentifierYield(&p.token) || p.isPossiblyEscapedAwait(&p.token)
}

// matchSpecIdentifierWith is matchSpecIdentifier(canUseYield, isAwait).
func (p *Parser) matchSpecIdentifierWith(canUseYield, isAwait bool) bool {
	return isAwait || p.match(IDENT) || p.isAllowedIdentifierLet(&p.token) || (canUseYield && p.isPossiblyEscapedYield(&p.token))
}

func (p *Parser) matchIdentifierOrPossiblyEscapedContextualKeyword() bool {
	return p.match(IDENT) || p.isPossiblyEscapedLet(&p.token) || p.isPossiblyEscapedYield(&p.token) || p.isPossiblyEscapedAwait(&p.token)
}

// --- Save points --------------------------------------------------------------------

func (p *Parser) internalSaveParserState(ctx treeBuilder) parserState {
	s := p.parserState
	s.unaryTokenStackDepth = ctx.unaryTokenStackDepth()
	return s
}

func (p *Parser) restoreParserState(ctx treeBuilder, s parserState) {
	p.parserState = s
	ctx.setUnaryTokenStackDepth(s.unaryTokenStackDepth)
}

func (p *Parser) internalSaveLexerState() lexerState {
	return lexerState{
		startOffset:                  int(p.token.Start),
		lastTokenLocation:            p.lastTokenLocation,
		hasLineTerminatorBeforeToken: p.lexer.HasLineTerminatorBeforeToken(),
		lastTokenType:                p.lastTokenType,
	}
}

func (p *Parser) restoreLexerState(s lexerState) {
	// SetOffset clears lexer errors.
	p.lexer.SetOffset(s.startOffset)
	p.lexer.SetHasLineTerminatorBeforeToken(s.hasLineTerminatorBeforeToken)
	p.lastTokenType = s.lastTokenType
	p.token.Type = s.lastTokenType
	p.token.Start = TextPosition(s.lastTokenLocation.StartOffset)
	p.token.End = TextPosition(s.lastTokenLocation.EndOffset)
	p.nextWithoutClearingLineTerminator(0)
}

func (p *Parser) internalSaveState(ctx treeBuilder) savePoint {
	return savePoint{parserState: p.internalSaveParserState(ctx), lexerState: p.internalSaveLexerState()}
}

// swapSavePointForError saves the current (failed) state including its error
// and rewinds to old.
func (p *Parser) swapSavePointForError(ctx treeBuilder, old *savePoint) savePointWithError {
	sp := savePointWithError{savePoint: p.internalSaveState(ctx)}
	sp.lexerError = p.lexer.SawError()
	sp.lexerErrorMessage = p.lexer.ErrorMessage()
	sp.parserErrorMessage = p.errorMessage
	sp.parserHasError = p.hasErrorFlag
	// The new save point's unary stack may hold stale info; use old's.
	sp.parserState.unaryTokenStackDepth = old.parserState.unaryTokenStackDepth
	p.restoreSavePoint(ctx, *old)
	return sp
}

func (p *Parser) createSavePoint(ctx treeBuilder) savePoint { return p.internalSaveState(ctx) }

func (p *Parser) internalRestoreState(ctx treeBuilder, sp savePoint) {
	p.restoreLexerState(sp.lexerState)
	p.restoreParserState(ctx, sp.parserState)
}

func (p *Parser) restoreSavePointWithError(ctx treeBuilder, sp savePointWithError) {
	p.internalRestoreState(ctx, sp.savePoint)
	p.lexer.SetSawError(sp.lexerError)
	p.lexer.SetErrorMessage(sp.lexerErrorMessage)
	p.errorMessage = sp.parserErrorMessage
	p.hasErrorFlag = sp.parserHasError
}

func (p *Parser) restoreSavePoint(ctx treeBuilder, sp savePoint) {
	p.internalRestoreState(ctx, sp)
	p.clearError()
}

// --- Scopes -------------------------------------------------------------------------

func (p *Parser) strictMode() bool { return p.currentScope.strictMode() }
func (p *Parser) setStrictMode()   { p.currentScope.setStrictMode() }
func (p *Parser) lexicallyScopedFeatures() LexicallyScopedFeatures {
	return p.currentScope.lexicallyScopedFeatures
}
func (p *Parser) currentImplementationVisibility() ImplementationVisibility {
	return p.currentScope.implementationVisibility
}

func (p *Parser) isValidStrictMode() bool {
	if !p.currentScope.isValidStrictMode {
		return false
	}
	// Generator and async bodies: the wrapper's name or parameters may be invalid.
	if (p.currentScope.isGeneratorFunctionBoundary || p.currentScope.isAsyncFunctionBoundary) && p.currentScope.containingScope != nil {
		return p.currentScope.containingScope.isValidStrictMode
	}
	return true
}

func (p *Parser) currentVariableScope() *Scope {
	s := p.currentScope
	for !s.allowsVarDeclarations {
		s = s.containingScope
	}
	return s
}

func (p *Parser) currentLexicalDeclarationScope() *Scope {
	s := p.currentScope
	for !s.allowsLexicalDeclarations {
		s = s.containingScope
	}
	return s
}

// currentFunctionScope returns the closest function boundary, or the top-level scope.
func (p *Parser) currentFunctionScope() *Scope {
	s := p.currentScope
	for s.containingScope != nil && !s.isFunctionBoundary {
		s = s.containingScope
	}
	return s
}

func (p *Parser) closestParentOrdinaryFunctionNonLexicalScope() *Scope {
	s := p.currentScope
	for s.containingScope != nil {
		if s.isFunctionBoundary && !s.isGeneratorFunctionBoundary && !s.isAsyncFunctionBoundary && !s.isArrowFunctionBoundary {
			break
		}
		s = s.containingScope
	}
	return s
}

// closestScopeOwningArguments skips arrow functions and lexical scopes,
// which are transparent for `arguments`.
func (p *Parser) closestScopeOwningArguments() *Scope {
	s := p.currentScope
	for s.containingScope != nil {
		if s.isFunctionBoundary && !s.isArrowFunctionBoundary {
			break
		}
		s = s.containingScope
	}
	return s
}

func (p *Parser) closestClassScopeOrTopLevelScope() *Scope {
	s := p.currentScope
	for s.containingScope != nil && !s.isClassScope {
		s = s.containingScope
	}
	return s
}

func (p *Parser) pushScope() *Scope {
	visibility := p.implementationVisibility
	features := NoLexicallyScopedFeatures
	var isFunction, isGenerator, isArrow, isAsync, isStaticBlock bool
	if parent := p.currentScope; parent != nil {
		visibility = parent.implementationVisibility
		features = parent.lexicallyScopedFeatures
		isFunction = parent.isFunction
		isGenerator = parent.isGeneratorFunction
		isArrow = parent.isArrowFunction
		isAsync = parent.isAsyncFunction
		isStaticBlock = parent.isStaticBlock
	}
	p.currentScope = newScope(p.currentScope, visibility, features, isFunction, isGenerator, isArrow, isAsync, isStaticBlock)
	p.scopeDepth++
	return p.currentScope
}

// resetImplementationVisibilityIfNeeded: a function boundary inherits
// PrivateRecursive visibility only from an enclosing function.
func (p *Parser) resetImplementationVisibilityIfNeeded() {
	if !p.currentScope.isFunctionBoundary {
		return
	}
	for s := p.currentScope.containingScope; s != nil; s = s.containingScope {
		if !s.isFunctionBoundary {
			continue
		}
		if s.implementationVisibility != ImplementationVisibilityPrivateRecursive {
			p.currentScope.resetImplementationVisibility()
		}
		break
	}
}

// popScopeInternal pops the current scope (which must be scope) and returns
// its lexical environment and function declarations.
func (p *Parser) popScopeInternal(scope *Scope, shouldTrackClosedVariables bool, precomputedFreeVariables []ident.Identifier) (VariableEnvironment, FunctionStack) {
	if scope != p.currentScope {
		panic("parser: popping a scope that is not current")
	}
	last := p.currentScope
	parent := last.containingScope

	last.finalizeLexicalEnvironment()
	parent.collectFreeVariablesFrom(last, shouldTrackClosedVariables, precomputedFreeVariables)

	if last.hasSloppyModeFunctionHoistingCandidates() {
		last.bubbleSloppyModeFunctionHoistingCandidates(parent)
	}
	if last.isArrowFunction {
		last.setInnerArrowFunctionUsesEvalAndUseArgumentsIfNeeded()
	}
	if !(last.isFunctionBoundary && !last.isArrowFunctionBoundary) {
		parent.mergeInnerArrowFunctionFeatures(last.innerArrowFunctionFeatures)
	}
	if !last.isFunctionBoundary && last.needsFullActivation {
		parent.needsFullActivation = true
	}
	env, functions := last.takeLexicalEnvironment(), last.takeFunctionDeclarations()
	p.currentScope = parent
	p.scopeDepth--
	return env, functions
}

func (p *Parser) popScope(scope *Scope, shouldTrackClosedVariables bool) (VariableEnvironment, FunctionStack) {
	return p.popScopeInternal(scope, shouldTrackClosedVariables, nil)
}

// autoPopScope is JSC's AutoPopScope: pop() pops explicitly; cleanup (to be
// deferred) pops the scope if a failure unwound past it.
type autoPopScope struct {
	p      *Parser
	scope  *Scope
	popped bool
}

func (p *Parser) newAutoPopScope(s *Scope) *autoPopScope { return &autoPopScope{p: p, scope: s} }

func (a *autoPopScope) pop(shouldTrackClosedVariables bool) (VariableEnvironment, FunctionStack) {
	a.popped = true
	return a.p.popScopeInternal(a.scope, shouldTrackClosedVariables, nil)
}

func (a *autoPopScope) popWithPrecomputed(shouldTrackClosedVariables bool, precomputed []ident.Identifier) (VariableEnvironment, FunctionStack) {
	a.popped = true
	return a.p.popScopeInternal(a.scope, shouldTrackClosedVariables, precomputed)
}

func (a *autoPopScope) cleanup() {
	if !a.popped && a.p.currentScope == a.scope {
		a.popped = true
		a.p.popScopeInternal(a.scope, false, nil)
	}
}

// declareHoistedVariable declares a var in the closest var scope, recording
// it as "being hoisted" in each lexical scope it passes.
func (p *Parser) declareHoistedVariable(id ident.Identifier) DeclarationResult {
	s := p.currentScope
	for {
		// Annex B.3.5 exempts `try {} catch (e) { var e; }`.
		if s.hasLexicallyDeclaredVariable(id) && !s.isSimpleCatchParameterScope {
			return DeclarationInvalidDuplicateDeclaration
		}
		if s.allowsVarDeclarations {
			return s.declareVariable(id)
		}
		s.addVariableBeingHoisted(id)
		s = s.containingScope
	}
}

func (p *Parser) declareVariable(id ident.Identifier, t DeclarationType, importType DeclarationImportType) DeclarationResult {
	if t == VarDeclaration {
		return p.declareHoistedVariable(id)
	}
	// Lexical variables at the top level may not shadow arguments or vars.
	if !p.lexer.IsReparsingFunction() && p.statementDepth == 1 && (p.hasDeclaredParameter(id) || p.hasDeclaredVariable(id)) {
		return DeclarationInvalidDuplicateDeclaration
	}
	s := p.currentLexicalDeclarationScope()
	if s.isCatchBlockScope && s.containingScope.hasLexicallyDeclaredVariable(id) {
		return DeclarationInvalidDuplicateDeclaration
	}
	isAwaitUsing := t == AwaitUsingDeclaration
	isUsing := t == UsingDeclaration || isAwaitUsing
	return s.declareLexicalVariable(id, t == ConstDeclaration || isUsing, importType, isUsing, isAwaitUsing)
}

func (p *Parser) declareFunction(id ident.Identifier) (DeclarationResult, *Scope) {
	if p.statementDepth == 1 && !p.currentScope.isModuleCode {
		// Top-level functions are declared as vars for backwards compatibility
		// (both strict and sloppy), except in module code.
		s := p.currentVariableScope()
		return s.declareFunctionAsVar(id), s
	}
	s := p.currentLexicalDeclarationScope()
	if s.isCatchBlockScope && s.containingScope.hasLexicallyDeclaredVariable(id) {
		return DeclarationInvalidDuplicateDeclaration, s
	}
	isFunctionDeclaration := p.parseMode == NormalFunctionMode
	return s.declareFunctionAsLet(id, isFunctionDeclaration), s
}

func (p *Parser) hasDeclaredVariable(id ident.Identifier) bool {
	return p.currentVariableScope().hasDeclaredVariable(id)
}

func (p *Parser) hasDeclaredParameter(id ident.Identifier) bool {
	s := p.currentVariableScope()
	if s.isGeneratorFunctionBoundary || s.isAsyncFunctionBoundary {
		// Generator and async bodies: the parameters live in the wrapper.
		s = s.containingScope
	}
	return s.hasDeclaredParameter(id)
}

func (p *Parser) exportName(id ident.Identifier) bool { return p.moduleScopeData.ExportName(id) }

func (p *Parser) findCachedFunctionInfo(openBracePos int) *sourceProviderCacheItem {
	if p.functionCache == nil {
		return nil
	}
	return p.functionCache.get(openBracePos)
}

func (p *Parser) breakIsValid() bool {
	s := p.currentScope
	for !s.breakIsValid() {
		if !s.hasContainingScope() || s.isStaticBlockBoundary {
			return false
		}
		s = s.containingScope
	}
	return true
}

func (p *Parser) continueIsValid() bool {
	s := p.currentScope
	for !s.continueIsValid() {
		if !s.hasContainingScope() || s.isStaticBlockBoundary {
			return false
		}
		s = s.containingScope
	}
	return true
}

func (p *Parser) getLabel(label ident.Identifier) *scopeLabelInfo {
	s := p.currentScope
	for {
		if l := s.getLabel(label); l != nil {
			return l
		}
		if !s.hasContainingScope() {
			return nil
		}
		s = s.containingScope
	}
}

func (p *Parser) declareParameter(id ident.Identifier) DeclarationResult {
	return p.currentScope.declareParameter(id)
}

// setForScope is WTF's SetForScope: assign and return a restorer to defer.
func setForScope[T any](ptr *T, value T) func() {
	old := *ptr
	*ptr = value
	return func() { *ptr = old }
}

// saveForScope restores *ptr to its current value when the returned func runs.
func saveForScope[T any](ptr *T) func() {
	old := *ptr
	return func() { *ptr = old }
}

// --- Entry points ---------------------------------------------------------------------

// ErrorType classifies a parse failure (ParserError::ErrorType).
type ErrorType uint8

const (
	ErrorNone ErrorType = iota
	ErrorStackOverflow
	ErrorEval
	ErrorOutOfMemory
	ErrorSyntax
)

// SyntaxErrorType says whether more input could fix the error.
type SyntaxErrorType uint8

const (
	SyntaxErrorNone SyntaxErrorType = iota
	SyntaxErrorIrrecoverable
	SyntaxErrorUnterminatedLiteral
	SyntaxErrorRecoverable
)

// ParserError is JSC's ParserError.
type ParserError struct {
	Type            ErrorType
	SyntaxErrorType SyntaxErrorType
	Token           Token
	Message         string
	Line            int
}

func (e *ParserError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("SyntaxError: %s (line %d)", e.Message, e.Line)
	}
	return "SyntaxError: " + e.Message
}

type parseInnerResult struct {
	parameters           *FunctionParameters
	sourceElements       *SourceElements
	functionDeclarations FunctionStack
	varDeclarations      VariableEnvironment
	lexicalVariables     VariableEnvironment
	features             CodeFeatures
	numConstants         int
}

// ParseOptions carries the optional arguments of JSC's parse().
type ParseOptions struct {
	CalleeName                                    ident.Identifier
	ParsingContext                                ParsingContext
	FunctionConstructorParametersEndPosition      int // -1 if absent
	ParentScopePrivateNames                       *PrivateNameEnvironment
	ClassElementDefinitions                       []ClassElementDefinition
	ConstructorKindForTopLevelFunctionExpressions ConstructorKind
}

// parse is Parser::parse<ParsedNode>. kind selects the node type to build.
func (p *Parser) parse(kind parsedNodeKind, opts ParseOptions) (ParsedNode, *ParserError) {
	if kind == parsedFunction {
		p.lexer.SetIsReparsingFunction()
	}
	startLocation := p.tokenLocation()
	result, err := p.parseInnerRecovering(opts)

	errorOffset := p.lexer.CurrentOffset()
	lexError := p.lexer.SawError()
	lexErrorMessage := ""
	if lexError {
		lexErrorMessage = p.lexer.ErrorMessage()
	}
	p.lexer.Clear()

	if err == "" && !lexError {
		endLocation := TokenLocation{StartOffset: p.lexer.CurrentOffset()}
		node := p.makeParsedNode(kind, startLocation, endLocation, result)
		sn := node.Scope()
		sn.SetStartOffset(p.lexer.CurrentOffset())
		sn.SetEndOffset(p.lexer.CurrentOffset())
		if !p.parseMode.IsFunction() {
			p.source.Provider.SourceURLDirective = p.lexer.SourceURLDirective()
			p.source.Provider.SourceMappingURLDirective = p.lexer.SourceMappingURLDirective()
		}
		return node, nil
	}

	line := p.source.Provider.LineColumnForOffset(errorOffset).Line
	message := err
	if lexError {
		message = lexErrorMessage
	}
	// A syntax error can't happen when reparsing a function (it would have
	// been reported for the enclosing code), so it must be a stack overflow.
	if kind == parsedFunction || p.hasStackOverflow {
		return nil, &ParserError{Type: ErrorStackOverflow, SyntaxErrorType: SyntaxErrorNone, Token: p.token, Message: message, Line: line}
	}
	errorType := SyntaxErrorIrrecoverable
	switch {
	case p.token.Type == EOFTOK:
		errorType = SyntaxErrorRecoverable
	case p.token.Type&UnterminatedCanBeErrorTokenFlag != 0:
		// Multiline-capable unterminated literals are recoverable.
		if p.token.Type == UNTERMINATED_MULTILINE_COMMENT_ERRORTOK || p.token.Type == UNTERMINATED_TEMPLATE_LITERAL_ERRORTOK {
			errorType = SyntaxErrorRecoverable
		} else {
			errorType = SyntaxErrorUnterminatedLiteral
		}
	}
	t := ErrorSyntax
	if kind == parsedEval {
		t = ErrorEval
	}
	return nil, &ParserError{Type: t, SyntaxErrorType: errorType, Token: p.token, Message: message, Line: line}
}

// parseInnerRecovering runs parseInner and converts a bailout into an error message.
func (p *Parser) parseInnerRecovering(opts ParseOptions) (result *parseInnerResult, errMsg string) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(parseBailout); !ok {
				panic(r)
			}
			result = nil
			errMsg = p.errorMessage
			if !p.hasError() {
				errMsg = "Parser error"
			}
		}
	}()
	return p.parseInner(opts)
}

type parsedNodeKind uint8

const (
	parsedProgram parsedNodeKind = iota
	parsedEval
	parsedModuleProgram
	parsedFunction
)

func (p *Parser) makeParsedNode(kind parsedNodeKind, start, end TokenLocation, r *parseInnerResult) ParsedNode {
	sn := ScopeNode{
		StatementBase:                  newStatementBase(end),
		VariableEnvironmentNode:        VariableEnvironmentNode{LexicalVariables: r.lexicalVariables, FunctionStack: r.functionDeclarations},
		StartStartOffset:               start.StartOffset,
		Features:                       r.features,
		LexicallyScopedFeatures:        p.currentScope.lexicallyScopedFeatures,
		InnerArrowFunctionCodeFeatures: p.currentScope.innerArrowFunctionFeatures,
		Source:                         *p.source,
		VarDeclarations:                r.varDeclarations,
		NumConstants:                   r.numConstants,
		Statements:                     r.sourceElements,
	}
	switch kind {
	case parsedEval:
		return &EvalNode{sn}
	case parsedModuleProgram:
		return &ModuleProgramNode{ScopeNode: sn, UsesAwait: r.features&AwaitFeature != 0, ModuleScopeData: p.moduleScopeData}
	case parsedFunction:
		return &FunctionNode{ScopeNode: sn, Parameters: r.parameters}
	}
	return &ProgramNode{sn}
}

func (p *Parser) parseInner(opts ParseOptions) (*parseInnerResult, string) {
	ctx := newASTBuilder(p.source)
	parseMode := p.parseMode
	scope := p.currentScope
	scope.setIsLexicalScope()

	hasPrivateNames := scope.isEvalContext && opts.ParentScopePrivateNames != nil && opts.ParentScopePrivateNames.len() != 0
	if hasPrivateNames {
		scope.setIsPrivateNameScope()
		scope.lexicalVariables.AddPrivateNamesFrom(opts.ParentScopePrivateNames)
	}

	defer setForScope(&p.parserState.functionParsePhase, functionParsePhaseBody)()

	var parameters *FunctionParameters
	isArrowFunctionBodyExpression := parseMode == AsyncArrowFunctionBodyMode && !p.match(OPENBRACE)
	if p.lexer.IsReparsingFunction() {
		var info ParserFunctionInfo
		switch {
		case parseMode.IsGeneratorOrAsyncFunctionBody():
			parameters = p.createGeneratorParameters(ctx, &info.ParameterCount).(*FunctionParameters)
		case parseMode == ClassFieldInitializerMode:
			parameters = ctx.createFormalParameterList().(*FunctionParameters)
		default:
			parameters = p.parseFunctionParameters(ctx, &info).(*FunctionParameters)
		}
		if (parseMode == ArrowFunctionMode || parseMode == AsyncArrowFunctionMode) && !p.hasError() {
			if !p.match(ARROWFUNCTION) {
				return nil, "Parser error"
			}
			p.next(0)
			isArrowFunctionBodyExpression = !p.match(OPENBRACE)
		}
	}

	if functionNameIsInScope(opts.CalleeName, p.functionMode) {
		scope.declareCallee(opts.CalleeName)
	}

	if p.lexer.IsReparsingFunction() {
		p.statementDepth--
	}

	var elements TreeSourceElements
	if !p.hasError() {
		switch {
		case parseMode.IsAsyncFunctionWrapper():
			elements = p.parseAsyncFunctionSourceElements(ctx, opts.CalleeName, isArrowFunctionBodyExpression, CheckForStrictMode)
		case isArrowFunctionBodyExpression:
			elements = p.parseArrowFunctionSingleExpressionBodySourceElements(ctx)
		case parseMode.IsModule():
			elements = p.parseModuleSourceElements(ctx)
		case parseMode.IsGeneratorWrapper():
			elements = p.parseGeneratorFunctionSourceElements(ctx, opts.CalleeName, CheckForStrictMode)
		case parseMode.IsAsyncGeneratorWrapper():
			elements = p.parseAsyncGeneratorFunctionSourceElements(ctx, opts.CalleeName, isArrowFunctionBodyExpression, CheckForStrictMode)
		case opts.ParsingContext == ParsingContextFunctionConstructor:
			elements = p.parseSingleFunction(ctx, opts.FunctionConstructorParametersEndPosition)
		case parseMode == ClassFieldInitializerMode:
			elements = p.parseClassFieldInitializerSourceElements(ctx, opts.ClassElementDefinitions)
		default:
			elements = p.parseSourceElements(ctx, CheckForStrictMode)
		}
	}

	validEnding := p.consume(EOFTOK, 0)
	if elements == nil || !validEnding {
		if p.hasError() {
			return nil, p.errorMessage
		}
		return nil, "Parser error"
	}

	if !p.lexer.IsReparsingFunction() && p.seenPrivateNameUseInNonReparsingFunctionMode {
		errorMessage := ""
		scope.forEachUsedVariable(func(id ident.Identifier) bool {
			if !id.IsPrivateName() {
				return true
			}
			if opts.ParentScopePrivateNames != nil && opts.ParentScopePrivateNames.contains(id) {
				return true
			}
			if scope.lexicalVariables.Contains(id) {
				return true
			}
			errorMessage = "Cannot reference undeclared private names: \"" + id.String() + "\""
			return false
		})
		if errorMessage != "" {
			return nil, errorMessage
		}
	}

	// Hoisting must be finalized before computing captured variables.
	scope.finalizeSloppyModeFunctionHoisting()
	for _, id := range scope.getCapturedVars() {
		scope.declaredVariables.MarkVariableAsCaptured(id)
	}
	scope.finalizeLexicalEnvironment()

	if parseMode.IsGeneratorWrapper() || parseMode.IsAsyncFunctionOrAsyncGeneratorWrapper() {
		if scope.usedVariablesContains(ident.Arguments) {
			ctx.propagateArgumentsUse()
		}
	}

	features := ctx.features()
	if scope.shadowsArguments {
		features |= ShadowsArgumentsFeature
	}
	if p.seenTaggedTemplateInNonReparsingFunctionMode {
		features |= NoEvalCacheFeature
	}
	if scope.hasNonSimpleParameterList {
		features |= NonSimpleParameterListFeature
	}
	if scope.usesImportMeta {
		features |= ImportMetaFeature
	}
	if p.seenArgumentsDotLength && scope.hasDeclaredGlobalArguments() {
		features |= ArgumentsFeature
	}
	if scope.asyncFunctionBodyDoesNotUseAwait {
		features |= AsyncFunctionWithoutAwaitFeature
	}
	if scope.usesAwait {
		features |= AwaitFeature
	}

	return &parseInnerResult{
		parameters:           parameters,
		sourceElements:       sourceElements(elements),
		functionDeclarations: scope.takeFunctionDeclarations(),
		varDeclarations:      scope.takeDeclaredVariables(),
		lexicalVariables:     scope.takeLexicalEnvironment(),
		features:             features,
		numConstants:         ctx.numConstants(),
	}, ""
}

// ParseConfig configures a parse.
type ParseConfig struct {
	Name                     ident.Identifier
	ImplementationVisibility ImplementationVisibility
	BuiltinMode              BuiltinMode
	LexicallyScopedFeatures  LexicallyScopedFeatures
	ScriptMode               ScriptMode
	ParseMode                SourceParseMode
	FunctionMode             FunctionMode
	SuperBinding             SuperBinding
	ConstructorKind          ConstructorKind
	DerivedContextType       DerivedContextType
	EvalContextType          EvalContextType
	ParentScopePrivateNames  *PrivateNameEnvironment
	ClassElementDefinitions  []ClassElementDefinition
	IsInsideOrdinaryFunction bool
	Options                  *Options
}

func parseWith(kind parsedNodeKind, source SourceCode, cfg ParseConfig, opts ParseOptions) (ParsedNode, *ParserError) {
	p := newParser(&source, ParserConfig{
		ImplementationVisibility: cfg.ImplementationVisibility,
		BuiltinMode:              cfg.BuiltinMode,
		LexicallyScopedFeatures:  cfg.LexicallyScopedFeatures,
		ScriptMode:               cfg.ScriptMode,
		ParseMode:                cfg.ParseMode,
		FunctionMode:             cfg.FunctionMode,
		SuperBinding:             cfg.SuperBinding,
		ConstructorKind:          cfg.ConstructorKind,
		DerivedContextType:       cfg.DerivedContextType,
		IsEvalContext:            kind == parsedEval,
		EvalContextType:          cfg.EvalContextType,
		IsInsideOrdinaryFunction: cfg.IsInsideOrdinaryFunction,
		Options:                  cfg.Options,
	})
	p.constructorKindForTopLevelFunctionExpressions = opts.ConstructorKindForTopLevelFunctionExpressions
	return p.parse(kind, opts)
}

// ParseProgram parses a classic script (parseRootNode<ProgramNode>).
func ParseProgram(source SourceCode, features LexicallyScopedFeatures) (*ProgramNode, *ParserError) {
	n, err := parseWith(parsedProgram, source, ParseConfig{ParseMode: ProgramMode, LexicallyScopedFeatures: features, SuperBinding: SuperBindingNotNeeded}, ParseOptions{FunctionConstructorParametersEndPosition: -1})
	if err != nil {
		return nil, err
	}
	return n.(*ProgramNode), nil
}

// ParseModule parses module code (parseRootNode<ModuleProgramNode>).
func ParseModule(source SourceCode, mode SourceParseMode) (*ModuleProgramNode, *ParserError) {
	n, err := parseWith(parsedModuleProgram, source, ParseConfig{ParseMode: mode, ScriptMode: ScriptModeModule, LexicallyScopedFeatures: StrictModeLexicallyScopedFeature, SuperBinding: SuperBindingNotNeeded}, ParseOptions{FunctionConstructorParametersEndPosition: -1})
	if err != nil {
		return nil, err
	}
	return n.(*ModuleProgramNode), nil
}

// ParseEval parses eval code.
func ParseEval(source SourceCode, cfg ParseConfig) (*EvalNode, *ParserError) {
	cfg.ParseMode = ProgramMode
	n, err := parseWith(parsedEval, source, cfg, ParseOptions{CalleeName: cfg.Name, ParentScopePrivateNames: cfg.ParentScopePrivateNames, FunctionConstructorParametersEndPosition: -1})
	if err != nil {
		return nil, err
	}
	return n.(*EvalNode), nil
}

// ParseFunction reparses a function body (parse<FunctionNode>) when the
// function is first compiled. source covers the function's text from its
// parameters on, as recorded in its FunctionMetadataNode.
func ParseFunction(source SourceCode, cfg ParseConfig) (*FunctionNode, *ParserError) {
	n, err := parseWith(parsedFunction, source, cfg, ParseOptions{
		CalleeName:                               cfg.Name,
		ParentScopePrivateNames:                  cfg.ParentScopePrivateNames,
		ClassElementDefinitions:                  cfg.ClassElementDefinitions,
		FunctionConstructorParametersEndPosition: -1,
	})
	if err != nil {
		return nil, err
	}
	return n.(*FunctionNode), nil
}

// ParseFunctionForFunctionConstructor parses the source built by
// `new Function(...)`; parametersEndPosition is where the parameter list
// supplied by the caller ends.
func ParseFunctionForFunctionConstructor(source SourceCode, features LexicallyScopedFeatures, parametersEndPosition int) (*ProgramNode, *ParserError) {
	n, err := parseWith(parsedProgram, source, ParseConfig{ParseMode: ProgramMode, LexicallyScopedFeatures: features, SuperBinding: SuperBindingNotNeeded}, ParseOptions{ParsingContext: ParsingContextFunctionConstructor, FunctionConstructorParametersEndPosition: parametersEndPosition})
	if err != nil {
		return nil, err
	}
	return n.(*ProgramNode), nil
}
