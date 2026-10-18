package parser

import (
	"github.com/baslentfert/jsc2go/ident"
)

// Statement parsing (Parser.cpp: parseSourceElements .. parseIfStatement).

func (p *Parser) parseSourceElements(ctx treeBuilder, mode SourceElementsMode) TreeSourceElements {
	const lengthOfUseStrictLiteral = 12 // "use strict".length
	elements := ctx.createSourceElements()
	directive := ident.Null
	directiveLiteralLength := 0
	shouldCheckForUseStrict := mode == CheckForStrictMode
	var sp savePoint
	if shouldCheckForUseStrict {
		sp = p.createSavePoint(ctx)
	}

	for {
		statement := p.parseStatementListItem(ctx, &directive, &directiveLiteralLength)
		if statement == nil {
			break
		}
		if shouldCheckForUseStrict {
			if !directive.IsNull() {
				// "use strict" must be the exact literal, without escapes or line continuations.
				if directiveLiteralLength == lengthOfUseStrictLiteral && directive == ident.UseStrict {
					p.setStrictMode()
					shouldCheckForUseStrict = false
					if !p.isValidStrictMode() {
						if name := p.parserState.lastFunctionName; !name.IsNull() {
							p.semanticFailIfTrue(name == ident.Arguments, "Cannot name a function 'arguments' in strict mode")
							p.semanticFailIfTrue(name == ident.Eval, "Cannot name a function 'eval' in strict mode")
						}
						p.semanticFailIfTrue(p.hasDeclaredVariable(ident.Arguments), "Cannot declare a variable named 'arguments' in strict mode")
						p.semanticFailIfTrue(p.hasDeclaredVariable(ident.Eval), "Cannot declare a variable named 'eval' in strict mode")
						p.semanticFailIfTrue(p.currentScope.hasNonSimpleParameterList, "'use strict' directive not allowed inside a function with a non-simple parameter list")
						p.semanticFailIfFalse(p.isValidStrictMode(), "Invalid parameters or function name in strict mode")
					}
					// Strict mode changed, so re-lex from the start of the list.
					p.restoreSavePoint(ctx, sp)
					p.propagateError()
					continue
				}
				// A directive, but not "use strict": the next statement may be one too.
				directive = ident.Null
			} else {
				// "use strict" is only valid in the directive prologue.
				shouldCheckForUseStrict = false
			}
		}
		ctx.appendStatement(elements, statement)
	}
	p.propagateError()
	return elements
}

func (p *Parser) parseModuleSourceElements(ctx treeBuilder) TreeSourceElements {
	elements := ctx.createSourceElements()
	checker := &syntaxChecker{}

loop:
	for {
		var statement TreeStatement
		isImportDeclaration := false
		if p.token.Type == IMPORT {
			sp := p.createSavePoint(ctx)
			p.next(0)
			isImportDeclaration = !p.match(OPENPAREN) && !p.match(DOT)
			p.restoreSavePoint(ctx, sp)
		}
		switch {
		case p.token.Type == EXPORT_:
			statement = p.parseExportDeclaration(ctx)
			if statement != nil {
				p.recordPauseLocation(ctx.breakpointLocation(statement))
			}
		case isImportDeclaration:
			statement = p.parseImportDeclaration(ctx)
			if statement != nil {
				p.recordPauseLocation(ctx.breakpointLocation(statement))
			}
		default:
			// Includes `import("...")` calls and `import.meta`.
			directive := ident.Null
			directiveLiteralLength := 0
			if p.parseMode == ModuleAnalyzeMode {
				if p.parseStatementListItem(checker, &directive, &directiveLiteralLength) == nil {
					break loop
				}
				continue
			}
			statement = p.parseStatementListItem(ctx, &directive, &directiveLiteralLength)
		}
		if statement == nil {
			break
		}
		ctx.appendStatement(elements, statement)
	}

	p.propagateError()

	for name := range p.moduleScopeData.ExportedBindings {
		if p.currentScope.hasDeclaredVariable(name) {
			p.currentScope.declaredVariables.MarkVariableAsExported(name)
			continue
		}
		if p.currentScope.hasLexicallyDeclaredVariable(name) {
			p.currentScope.lexicalVariables.MarkVariableAsExported(name)
			continue
		}
		p.semanticFail("Exported binding '", name, "' needs to refer to a top-level declared variable")
	}
	return elements
}

func (p *Parser) parseGeneratorFunctionSourceElements(ctx treeBuilder, name ident.Identifier, mode SourceElementsMode) TreeSourceElements {
	elements := ctx.createSourceElements()

	functionStart := p.tokenStart()
	startLocation := p.tokenLocation()
	start := p.tokenStartPosition()
	functionNameStart := int(p.token.Start)
	parametersStart := functionNameStart

	info := ParserFunctionInfo{Name: ident.Null}
	p.createGeneratorParameters(ctx, &info.ParameterCount)
	info.StartOffset = parametersStart

	func() {
		bodyScope := p.newAutoPopScope(p.pushScope())
		defer bodyScope.cleanup()
		bodyScope.scope.setSourceParseMode(GeneratorBodyMode)
		p.resetImplementationVisibilityIfNeeded()
		bodyScope.scope.constructorKind = ConstructorKindNone
		bodyScope.scope.expectedSuperBinding = p.superBinding

		checker := &syntaxChecker{}
		p.failIfFalse(p.parseSourceElements(checker, mode) != nil, "Cannot parse the body of a generator")
		bodyScope.pop(ctx.needsFreeVariableInfo())
	}()
	info.Body = ctx.createFunctionMetadata(startLocation, p.tokenLocation(), functionStart, functionNameStart, parametersStart, p.currentImplementationVisibility(), p.lexicallyScopedFeatures(), ConstructorKindNone, p.superBinding, info.ParameterCount, GeneratorBodyMode, false)

	// The body is parsed as its own source, so the current token is EOF; the
	// closing brace is the previous token.
	info.EndOffset = p.lastTokenLocation.StartOffset

	fnExpr := ctx.createGeneratorFunctionBody(startLocation, &info, name)
	ctx.appendStatement(elements, ctx.createExprStatement(startLocation, fnExpr, start))
	return elements
}

func getAsyncFunctionBodyParseMode(mode SourceParseMode) SourceParseMode {
	if mode.IsAsyncGeneratorWrapper() {
		return AsyncGeneratorBodyMode
	}
	if mode == AsyncArrowFunctionMode {
		return AsyncArrowFunctionBodyMode
	}
	return AsyncFunctionBodyMode
}

func (p *Parser) parseAsyncFunctionSourceElements(ctx treeBuilder, calleeName ident.Identifier, isArrowFunctionBodyExpression bool, mode SourceElementsMode) TreeSourceElements {
	functionStart := p.tokenStart()
	startLocation := p.tokenLocation()
	start := p.tokenStartPosition()
	functionNameStart := int(p.token.Start)
	parametersStart := functionNameStart

	bodyParseMode := getAsyncFunctionBodyParseMode(p.parseMode)
	defer setForScope(&p.parseMode, bodyParseMode)()

	bodyUsesAwait := false
	bodySavePoint := p.createSavePoint(ctx)
	func() {
		bodyScope := p.newAutoPopScope(p.pushScope())
		defer bodyScope.cleanup()
		bodyScope.scope.setSourceParseMode(p.parseMode)
		p.resetImplementationVisibilityIfNeeded()

		checker := &syntaxChecker{}
		if isArrowFunctionBodyExpression {
			p.failIfFalse(p.parseArrowFunctionSingleExpressionBodySourceElements(checker) != nil, "Cannot parse the body of async arrow function")
		} else {
			p.failIfFalse(p.parseSourceElements(checker, mode) != nil, "Cannot parse the body of async function")
		}
		bodyUsesAwait = bodyScope.scope.usesAwait
		// Without await the body is inlined into the wrapper; there is no
		// body function, so closed variables need not be tracked.
		bodyScope.pop(bodyUsesAwait && ctx.needsFreeVariableInfo())
	}()

	// Without await, re-parse with the AST builder directly in the wrapper's
	// scope, so lexical declarations land there:
	//
	//     async function test(a, b) { return 42; }
	if !bodyUsesAwait {
		p.restoreSavePoint(ctx, bodySavePoint)
		p.currentFunctionScope().asyncFunctionBodyDoesNotUseAwait = true
		if isArrowFunctionBodyExpression {
			return p.parseArrowFunctionSingleExpressionBodySourceElements(ctx)
		}
		return p.parseSourceElements(ctx, mode)
	}

	// The body uses await: create a body function with generator parameters.
	elements := ctx.createSourceElements()
	info := ParserFunctionInfo{Name: ident.Null}
	p.createGeneratorParameters(ctx, &info.ParameterCount)
	info.StartOffset = parametersStart

	visibility := p.currentImplementationVisibility()
	if visibility == ImplementationVisibilityPrivate {
		visibility = ImplementationVisibilityPublic
	}
	info.Body = ctx.createFunctionMetadata(startLocation, p.tokenLocation(), functionStart, functionNameStart, parametersStart, visibility, p.lexicallyScopedFeatures(), ConstructorKindNone, p.superBinding, info.ParameterCount, p.parseMode, isArrowFunctionBodyExpression)

	// The body is parsed as its own source, so the current token is EOF; the
	// closing brace is the previous token.
	if isArrowFunctionBodyExpression {
		info.EndOffset = p.tokenLocation().EndOffset
	} else {
		info.EndOffset = p.lastTokenLocation.StartOffset
	}

	fnExpr := ctx.createAsyncFunctionBody(startLocation, &info, bodyParseMode, calleeName)
	ctx.appendStatement(elements, ctx.createExprStatement(startLocation, fnExpr, start))
	return elements
}

func (p *Parser) parseAsyncGeneratorFunctionSourceElements(ctx treeBuilder, calleeName ident.Identifier, isArrowFunctionBodyExpression bool, mode SourceElementsMode) TreeSourceElements {
	elements := ctx.createSourceElements()

	functionStart := p.tokenStart()
	startLocation := p.tokenLocation()
	start := p.tokenStartPosition()
	functionNameStart := int(p.token.Start)
	parametersStart := functionNameStart

	info := ParserFunctionInfo{Name: ident.Null}
	p.createGeneratorParameters(ctx, &info.ParameterCount)
	info.StartOffset = parametersStart

	parseMode := AsyncGeneratorBodyMode
	defer setForScope(&p.parseMode, parseMode)()
	func() {
		bodyScope := p.newAutoPopScope(p.pushScope())
		defer bodyScope.cleanup()
		bodyScope.scope.setSourceParseMode(p.parseMode)
		p.resetImplementationVisibilityIfNeeded()

		checker := &syntaxChecker{}
		if isArrowFunctionBodyExpression {
			p.failIfFalse(p.parseArrowFunctionSingleExpressionBodySourceElements(checker) != nil, "Cannot parse the body of async arrow function")
		} else {
			p.failIfFalse(p.parseSourceElements(checker, mode) != nil, "Cannot parse the body of async function")
		}
		bodyScope.pop(ctx.needsFreeVariableInfo())
	}()
	info.Body = ctx.createFunctionMetadata(startLocation, p.tokenLocation(), functionStart, functionNameStart, parametersStart, p.currentImplementationVisibility(), p.lexicallyScopedFeatures(), ConstructorKindNone, p.superBinding, info.ParameterCount, parseMode, isArrowFunctionBodyExpression)

	if isArrowFunctionBodyExpression {
		info.EndOffset = p.tokenLocation().EndOffset
	} else {
		info.EndOffset = p.lastTokenLocation.StartOffset
	}

	fnExpr := ctx.createAsyncFunctionBody(startLocation, &info, parseMode, calleeName)
	ctx.appendStatement(elements, ctx.createExprStatement(startLocation, fnExpr, start))
	return elements
}

func (p *Parser) parseSingleFunction(ctx treeBuilder, functionConstructorParametersEndPosition int) TreeSourceElements {
	elements := ctx.createSourceElements()
	var statement TreeStatement
	switch {
	case p.token.Type == FUNCTION:
		statement = p.parseFunctionDeclaration(ctx, functionDeclarationTypeDeclaration, notExported, DeclarationDefaultStandard, functionConstructorParametersEndPosition)
	case p.token.Type == IDENT && p.token.Data.Ident == ident.Async && !p.token.Data.Escaped:
		functionStart := int(p.token.Start)
		p.next(0)
		p.failIfFalse(p.match(FUNCTION) && !p.lexer.HasLineTerminatorBeforeToken(), "Cannot parse the async function")
		statement = p.parseAsyncFunctionDeclaration(ctx, functionStart, notExported, DeclarationDefaultStandard, functionConstructorParametersEndPosition)
	default:
		p.failDueToUnexpectedToken()
	}
	if statement != nil {
		ctx.setEndOffset(statement, p.lastTokenLocation.EndOffset)
		ctx.appendStatement(elements, statement)
	}
	p.propagateError()
	return elements
}

func (p *Parser) parseStatementListItem(ctx treeBuilder, directive *ident.Identifier, directiveLiteralLength *int) TreeStatement {
	// http://www.ecma-international.org/ecma-262/6.0/index.html#sec-statements
	defer saveForScope(&p.statementDepth)()
	p.statementDepth++
	p.enterRecursion()
	defer p.exitRecursion()

	var result TreeStatement
	shouldSetEndOffset := true
	shouldSetPauseLocation := false

	switch p.token.Type {
	case CONSTTOKEN:
		result = p.parseVariableDeclaration(ctx, ConstDeclaration, notExported)
		shouldSetPauseLocation = true

	case LET:
		shouldParseVariableDeclaration := true
		if !p.strictMode() {
			sp := p.createSavePoint(ctx)
			p.next(0)
			// Deliberately not matchSpecIdentifier(): contextual keywords should
			// still go to parseVariableDeclaration so its errors stay consistent.
			if !p.matchIdentifierOrPossiblyEscapedContextualKeyword() && !p.match(OPENBRACE) && !p.match(OPENBRACKET) {
				shouldParseVariableDeclaration = false
			}
			p.restoreSavePoint(ctx, sp)
		}
		if shouldParseVariableDeclaration {
			result = p.parseVariableDeclaration(ctx, LetDeclaration, notExported)
		} else {
			result = p.parseExpressionOrLabelStatement(ctx, true)
		}
		shouldSetPauseLocation = !ctx.shouldSkipPauseLocation(result)

	case CLASSTOKEN:
		result = p.parseClassDeclaration(ctx, notExported, DeclarationDefaultStandard)

	case FUNCTION:
		result = p.parseFunctionDeclaration(ctx, functionDeclarationTypeDeclaration, notExported, DeclarationDefaultStandard, -1)

	case ESCAPED_KEYWORD, IDENT, AWAIT, YIELD:
		if p.token.Type == ESCAPED_KEYWORD && !p.matchAllowedEscapedContextualKeyword() {
			p.failDueToUnexpectedToken()
		}
		if r, done, setPause := p.parseIdentStatementListItem(ctx); done {
			result = r
			shouldSetPauseLocation = setPause
			break
		}
		if p.currentScope.isStaticBlock {
			p.failIfTrue(p.match(YIELD), "Cannot use 'yield' within static block")
			p.failIfTrue(p.match(AWAIT), "Cannot use 'await' within static block")
		}
		// Labeled statements are noticed here (and in parseStatement) because
		// sloppy mode allows `function foo() { label: function bar() {} }`.
		result = p.parseExpressionOrLabelStatement(ctx, true)
		shouldSetPauseLocation = !ctx.shouldSkipPauseLocation(result)

	default:
		p.statementDepth-- // parseStatement() increments the depth.
		result = p.parseStatement(ctx, directive, directiveLiteralLength)
		shouldSetEndOffset = false
	}

	if result != nil {
		if shouldSetEndOffset {
			ctx.setEndOffset(result, p.lastTokenLocation.EndOffset)
		}
		if shouldSetPauseLocation {
			p.recordPauseLocation(ctx.breakpointLocation(result))
		}
	}
	return result
}

// parseIdentStatementListItem handles the IDENT/AWAIT cases of
// parseStatementListItem that start a declaration: `using x = ...`,
// `async function`, and `await using x = ...`. It returns done=false to fall
// through to an expression or label statement; setPause reports whether a
// pause location should be recorded (not for async function declarations).
func (p *Parser) parseIdentStatementListItem(ctx treeBuilder) (result TreeStatement, done, setPause bool) {
	if p.token.Type == ESCAPED_KEYWORD || p.token.Type == IDENT {
		if p.options.UseExplicitResourceManagement && p.token.Data.Ident == ident.Using && !p.token.Data.Escaped {
			sp := p.createSavePoint(ctx)
			p.next(0)
			if !p.lexer.HasLineTerminatorBeforeToken() && p.matchSpecIdentifier() {
				p.restoreSavePoint(ctx, sp)
				p.semanticFailIfTrue(p.currentScope.isGlobalCode && !p.currentScope.isModuleCode && p.statementDepth == 1, "'using' declaration is not allowed at the top level of a script or eval")
				p.semanticFailIfTrue(p.insideSwitchCaseBody, "'using' declaration is not allowed directly in a switch case or default clause")
				return p.parseVariableDeclaration(ctx, UsingDeclaration, notExported), true, true
			}
			p.restoreSavePoint(ctx, sp)
		}
		if p.token.Data.Ident == ident.Async && !p.token.Data.Escaped {
			// Eagerly parse as an AsyncFunctionDeclaration; it could otherwise be
			// mistaken for an AsyncFunctionExpression.
			sp := p.createSavePoint(ctx)
			functionStart := int(p.token.Start)
			p.next(0)
			if p.match(FUNCTION) && !p.lexer.HasLineTerminatorBeforeToken() {
				return p.parseAsyncFunctionDeclaration(ctx, functionStart, notExported, DeclarationDefaultStandard, -1), true, false
			}
			p.restoreSavePoint(ctx, sp)
		}
	}
	if p.token.Type != YIELD && p.options.UseExplicitResourceManagement && p.match(AWAIT) && !p.parserState.classFieldInitMasksAsync &&
		(p.currentFunctionScope().isAsyncFunctionBoundary || p.parseMode.IsModule()) {
		sp := p.createSavePoint(ctx)
		p.next(0)
		if !p.lexer.HasLineTerminatorBeforeToken() && p.match(IDENT) && p.token.Data.Ident == ident.Using && !p.token.Data.Escaped {
			p.next(0)
			if !p.lexer.HasLineTerminatorBeforeToken() && p.matchSpecIdentifier() {
				p.restoreSavePoint(ctx, sp)
				p.semanticFailIfTrue(p.currentScope.isGlobalCode && !p.currentScope.isModuleCode && p.statementDepth == 1, "'await using' declaration is not allowed at the top level of a script or eval")
				p.semanticFailIfTrue(p.insideSwitchCaseBody, "'await using' declaration is not allowed directly in a switch case or default clause")
				p.currentFunctionScope().usesAwait = true
				return p.parseVariableDeclaration(ctx, AwaitUsingDeclaration, notExported), true, true
			}
		}
		p.restoreSavePoint(ctx, sp)
	}
	return nil, false, false
}

func (p *Parser) parseVariableDeclaration(ctx treeBuilder, declarationType DeclarationType, exportType exportType) TreeStatement {
	location := p.tokenLocation()
	var declarations int
	var lastPattern TreeDestructuringPattern
	var lastInitializer TreeExpression
	var scratch TextPosition
	var forLoopConstDoesNotHaveInitializer bool
	decls := p.parseVariableDeclarationList(ctx, &declarations, &lastPattern, &lastInitializer, &scratch, &scratch, &scratch, varDeclarationContext, declarationType, exportType, &forLoopConstDoesNotHaveInitializer)
	p.propagateError()
	p.failIfFalse(p.autoSemiColon(), "Expected ';' after variable declaration")
	return ctx.createDeclarationStatement(location, decls)
}

func (p *Parser) parseDoWhileStatement(ctx treeBuilder) TreeStatement {
	p.next(0)
	unused := ident.Null
	p.currentScope.startLoop()
	statement := p.parseStatement(ctx, &unused, nil)
	p.currentScope.endLoop()
	p.failIfFalse(statement != nil, "Expected a statement following 'do'")
	location := p.tokenLocation()
	p.handleProductionOrFail(WHILE, "while", "end", "do-while loop")
	p.handleProductionOrFail(OPENPAREN, "(", "start", "do-while loop condition")
	p.semanticFailIfTrue(p.match(CLOSEPAREN), "Must provide an expression as a do-while loop condition")
	e := p.parseExpression(ctx)
	p.failIfFalse(e != nil, "Unable to parse do-while loop condition")
	p.recordPauseLocation(ctx.breakpointLocation(e))
	p.handleProductionOrFail(CLOSEPAREN, ")", "end", "do-while loop condition")
	p.consume(SEMICOLON, 0) // Always performs automatic semicolon insertion.
	return ctx.createDoWhileStatement(location, statement, e)
}

func (p *Parser) parseWhileStatement(ctx treeBuilder) TreeStatement {
	location := p.tokenLocation()
	p.next(0)
	p.handleProductionOrFail(OPENPAREN, "(", "start", "while loop condition")
	p.semanticFailIfTrue(p.match(CLOSEPAREN), "Must provide an expression as a while loop condition")
	e := p.parseExpression(ctx)
	p.failIfFalse(e != nil, "Unable to parse while loop condition")
	p.recordPauseLocation(ctx.breakpointLocation(e))
	p.handleProductionOrFail(CLOSEPAREN, ")", "end", "while loop condition")

	unused := ident.Null
	p.currentScope.startLoop()
	statement := p.parseStatement(ctx, &unused, nil)
	p.currentScope.endLoop()
	p.failIfFalse(statement != nil, "Expected a statement as the body of a while loop")
	return ctx.createWhileStatement(location, e, statement)
}

func declarationTypeToVariableKind(t DeclarationType) string {
	switch t {
	case VarDeclaration:
		return "variable name"
	case LetDeclaration, ConstDeclaration:
		return "lexical variable name"
	}
	return "using variable name"
}

func assignmentContextFromDeclarationType(t DeclarationType) AssignmentContext {
	switch t {
	case ConstDeclaration:
		return AssignmentContextConstDeclarationStatement
	case UsingDeclaration:
		return AssignmentContextUsingDeclarationStatement
	case AwaitUsingDeclaration:
		return AssignmentContextAwaitUsingDeclarationStatement
	}
	return AssignmentContextDeclarationStatement
}

func destructuringKindFromDeclarationType(t DeclarationType) DestructuringKind {
	switch t {
	case LetDeclaration:
		return DestructureToLet
	case ConstDeclaration:
		return DestructureToConst
	}
	return DestructureToVariables
}

func (p *Parser) parseVariableDeclarationList(ctx treeBuilder, declarations *int, lastPattern *TreeDestructuringPattern, lastInitializer *TreeExpression, identStart, initStart, initEnd *TextPosition, listContext varDeclarationListContext, declarationType DeclarationType, exportType exportType, forLoopConstDoesNotHaveInitializer *bool) TreeExpression {
	var head TreeExpression
	var headLocation TokenLocation
	var tail TreeExpression
	var lastIdent ident.Identifier
	var lastIdentToken Token
	assignmentContext := assignmentContextFromDeclarationType(declarationType)
	isUsingDeclaration := declarationType == UsingDeclaration || declarationType == AwaitUsingDeclaration
	for {
		*lastPattern = nil
		lastIdent = ident.Null
		location := p.tokenLocation()
		p.next(0)
		if head == nil && declarationType == AwaitUsingDeclaration {
			p.next(0) // skip `using`
		}
		if head != nil {
			// Subsequent declarations start after the comma.
			location = p.tokenLocation()
		}
		var node TreeExpression
		*declarations++
		hasInitializer := false

		p.failIfTrue(p.match(PRIVATENAME), "Cannot use a private name to declare a variable")
		if isUsingDeclaration {
			p.failIfTrue(p.match(OPENBRACE) || p.match(OPENBRACKET), "'using' declarations cannot have a destructuring pattern")
			p.failIfFalse(p.matchSpecIdentifier(), "Expected an identifier name in 'using' declaration")
		}
		if p.matchSpecIdentifier() {
			p.semanticFailIfTrue(p.currentScope.isStaticBlock && p.isArgumentsIdentifier(), "Cannot use 'arguments' as an identifier in static block")
			p.failIfTrue(p.isPossiblyEscapedLet(&p.token) && (declarationType == LetDeclaration || declarationType == ConstDeclaration || isUsingDeclaration),
				"Cannot use 'let' as an identifier name for a LexicalDeclaration")
			if p.isDisallowedIdentifierAwait(&p.token) {
				p.semanticFail("Cannot use 'await' as a ", declarationTypeToVariableKind(declarationType), " ", p.disallowedIdentifierAwaitReason())
			}
			varStart := p.tokenStartPosition()
			varStartLocation := p.tokenLocation()
			*identStart = varStart
			name := p.token.Data.Ident
			lastIdent = name
			lastIdentToken = p.token
			p.next(0)
			hasInitializer = p.match(EQUAL)
			result := p.declareVariable(name, declarationType, DeclarationNotImported)
			if result != DeclarationValid {
				p.failIfTrueIfStrict(result&DeclarationInvalidStrictMode != 0, "Cannot declare a variable named ", name, " in strict mode")
				if result&DeclarationInvalidDuplicateDeclaration != 0 {
					p.semanticFailIfTrue(declarationType == LetDeclaration, "Cannot declare a let variable twice: '", name, "'")
					p.semanticFailIfTrue(declarationType == ConstDeclaration, "Cannot declare a const variable twice: '", name, "'")
					p.semanticFailIfTrue(isUsingDeclaration, "Cannot declare a using variable twice: '", name, "'")
					p.semanticFail("Cannot declare a var variable that shadows a let/const/class variable: '", name, "'")
				}
			}
			if exportType == exported {
				p.semanticFailIfFalse(p.exportName(name), "Cannot export a duplicate name '", name, "'")
				p.moduleScopeData.ExportBinding(name, name)
			}

			if hasInitializer {
				varDivot := p.tokenStartPosition() + 1
				*initStart = p.tokenStartPosition()
				p.next(ctx.dontBuildStrings()) // consume '='
				p.propagateError()
				initializer := p.parseAssignmentExpression(ctx)
				*initEnd = p.lastTokenEndPosition()
				*lastInitializer = initializer
				p.failIfFalse(initializer != nil, "Expected expression as the intializer for the variable '", name, "'")
				node = ctx.createAssignResolve(location, name, initializer, varStart, varDivot, p.lastTokenEndPosition(), assignmentContext)
			} else {
				if isUsingDeclaration {
					if listContext == forLoopContext {
						*forLoopConstDoesNotHaveInitializer = true
					} else {
						p.failIfFalse(false, "'using' declaration requires an initializer")
					}
				}
				if listContext == forLoopContext && declarationType == ConstDeclaration {
					*forLoopConstDoesNotHaveInitializer = true
				}
				p.failIfTrue(listContext != forLoopContext && declarationType == ConstDeclaration, "const declared variable '", name, "'", " must have an initializer")
				if declarationType == VarDeclaration {
					node = ctx.createEmptyVarExpression(varStartLocation, name)
				} else {
					node = ctx.createEmptyLetExpression(varStartLocation, name)
				}
			}
		} else {
			lastIdent = ident.Null
			var pat TreeDestructuringPattern
			func() {
				defer setForScope(&p.allowsIn, true)()
				pat = p.parseDestructuringPattern(ctx, destructuringKindFromDeclarationType(declarationType), exportType, nil, nil, assignmentContext, 0)
			}()
			p.failIfFalse(pat != nil, "Cannot parse this destructuring pattern")
			hasInitializer = p.match(EQUAL)
			p.failIfTrue(listContext == varDeclarationContext && !hasInitializer, "Expected an initializer in destructuring variable declaration")
			*lastPattern = pat
			if hasInitializer {
				p.next(ctx.dontBuildStrings()) // consume '='
				rhs := p.parseAssignmentExpression(ctx)
				p.propagateError()
				node = ctx.createDestructuringAssignment(location, pat, rhs)
				*lastInitializer = rhs
			}
		}

		if node != nil {
			if head == nil {
				head = node
				headLocation = location
			} else {
				if tail == nil {
					p.recordPauseLocation(ctx.breakpointLocation(head))
					head = ctx.createCommaExpr(headLocation, head)
					tail = head
				}
				p.recordPauseLocation(ctx.breakpointLocation(node))
				tail = ctx.appendToCommaExpr(location, tail, node)
			}
		}
		if !p.match(COMMA) {
			break
		}
	}
	if !lastIdent.IsNull() {
		*lastPattern = ctx.createBindingLocation(lastIdentToken.Location(), lastIdent, lastIdentToken.Start, lastIdentToken.End, assignmentContext)
	}
	return head
}

func (p *Parser) parseForStatement(ctx treeBuilder) TreeStatement {
	location := p.tokenLocation()
	isAwaitFor := false
	p.next(0)

	defer saveForScope(&p.statementDepth)()
	p.statementDepth++

	if p.match(AWAIT) {
		p.semanticFailIfFalse(p.currentScope.isAsyncFunction || p.parseMode.IsModule(), "for-await-of can only be used in an async function or async generator")
		isAwaitFor = true
		p.currentFunctionScope().usesAwait = true
		p.next(0)
	}

	p.handleProductionOrFail(OPENPAREN, "(", "start", "for-loop header")
	nonLHSCount := p.parserState.nonLHSCount
	declarations := 0
	declLocation := p.tokenLocation()
	declsStart := p.tokenStartPosition()
	var decls TreeExpression
	var pat TreeDestructuringPattern
	isVarDeclaration := p.match(VAR)
	isLetDeclaration := p.match(LET)
	isConstDeclaration := p.match(CONSTTOKEN)
	isUsingDeclaration := false
	isAwaitUsingDeclaration := false
	if p.options.UseExplicitResourceManagement && p.match(IDENT) && p.token.Data.Ident == ident.Using && !p.token.Data.Escaped {
		sp := p.createSavePoint(ctx)
		p.next(0)
		if !p.lexer.HasLineTerminatorBeforeToken() && p.matchSpecIdentifier() {
			if p.matchContextualKeyword(ident.Of) {
				// [lookahead != `using` `of`]: only a declaration if `of` is a
				// binding with an initializer, as in `for (using of = init; ...)`.
				p.next(0)
				if p.match(EQUAL) {
					isUsingDeclaration = true
				}
			} else {
				isUsingDeclaration = true
			}
		}
		p.restoreSavePoint(ctx, sp)
	} else if p.options.UseExplicitResourceManagement && p.match(AWAIT) && !p.parserState.classFieldInitMasksAsync &&
		(p.currentFunctionScope().isAsyncFunctionBoundary || p.parseMode.IsModule()) {
		sp := p.createSavePoint(ctx)
		p.next(0)
		if !p.lexer.HasLineTerminatorBeforeToken() && p.match(IDENT) && p.token.Data.Ident == ident.Using && !p.token.Data.Escaped {
			p.next(0)
			// Unlike `using`, `await using` has no [lookahead != of] restriction.
			if !p.lexer.HasLineTerminatorBeforeToken() && p.matchSpecIdentifier() {
				isAwaitUsingDeclaration = true
			}
		}
		p.restoreSavePoint(ctx, sp)
		if isAwaitUsingDeclaration {
			p.currentFunctionScope().usesAwait = true
		}
	}
	isAnyUsingDeclaration := isUsingDeclaration || isAwaitUsingDeclaration
	isLexicalDeclaration := isLetDeclaration || isConstDeclaration || isAnyUsingDeclaration
	forLoopConstDoesNotHaveInitializer := false
	forLoopInitializerContainsClosure := false

	var lexicalScope *autoPopScope
	defer func() {
		if lexicalScope != nil {
			lexicalScope.cleanup()
		}
	}()
	popLexicalScopeIfNecessary := func() VariableEnvironment {
		if isLexicalDeclaration {
			env, _ := lexicalScope.pop(ctx.needsFreeVariableInfo())
			return env
		}
		return VariableEnvironment{}
	}

	gotoStandardForLoop := false
	gotoEnumerationLoop := false

	if isVarDeclaration || isLexicalDeclaration {
		// for (var/let/const/using IDENT in/of expression) statement
		// for (var/let/const varDeclarationList; expressionOpt; expressionOpt)
		if isLexicalDeclaration {
			newScope := p.pushScope()
			newScope.setIsLexicalScope()
			newScope.preventVarDeclarations()
			lexicalScope = p.newAutoPopScope(newScope)
		}

		var forInTarget TreeDestructuringPattern
		var forInInitializer TreeExpression
		p.allowsIn = false
		var initStart, initEnd TextPosition
		var declarationType DeclarationType
		switch {
		case isVarDeclaration:
			declarationType = VarDeclaration
		case isLetDeclaration:
			declarationType = LetDeclaration
		case isConstDeclaration:
			declarationType = ConstDeclaration
		case isUsingDeclaration:
			declarationType = UsingDeclaration
		default:
			declarationType = AwaitUsingDeclaration
		}
		candidatesBefore := p.currentScope.closedVariableCandidates.len()
		decls = p.parseVariableDeclarationList(ctx, &declarations, &forInTarget, &forInInitializer, &declsStart, &initStart, &initEnd, forLoopContext, declarationType, notExported, &forLoopConstDoesNotHaveInitializer)
		forLoopInitializerContainsClosure = p.currentScope.closedVariableCandidates.len() > candidatesBefore
		p.allowsIn = true
		p.propagateError()

		if p.match(SEMICOLON) {
			gotoStandardForLoop = true
		} else {
			p.failIfFalse(declarations == 1, "can only declare a single variable in an enumeration")

			// for-in/of with a declaration.
			inLocation := p.tokenStartPosition()
			isOfEnumeration := false
			if !p.match(INTOKEN) {
				p.failIfFalse(p.matchContextualKeyword(ident.Of), "Expected either 'in' or 'of' in enumeration syntax")
				isOfEnumeration = true
				p.next(0)
			} else {
				p.failIfTrue(isAnyUsingDeclaration, "Cannot use 'using' declaration in for-in loop")
				p.failIfFalse(!isAwaitFor, "Expected 'of' in for-await syntax")
				p.next(0)
			}

			if forInInitializer != nil {
				p.semanticFailIfTrue(isOfEnumeration, "Cannot assign to the loop variable inside a for-of loop header")
				p.semanticFailIfTrue(p.strictMode() || isLetDeclaration || isConstDeclaration || !ctx.isBindingNode(forInTarget), "Cannot assign to the loop variable inside a for-in loop header")
			}

			// for-in takes an Expression; for-of and for-await-of an AssignmentExpression.
			var e TreeExpression
			if isOfEnumeration {
				e = p.parseAssignmentExpression(ctx)
			} else {
				e = p.parseExpression(ctx)
			}
			p.failIfFalse(e != nil, "Expected expression to enumerate")
			p.recordPauseLocation(ctx.breakpointLocation(e))
			exprEnd := p.lastTokenEndPosition()

			header := "for-in header"
			if isOfEnumeration {
				header = "for-of header"
			}
			p.handleProductionOrFail(CLOSEPAREN, ")", "end", header)

			unused := ident.Null
			p.currentScope.startLoop()
			statement := p.parseStatement(ctx, &unused, nil)
			p.currentScope.endLoop()
			kind := "in"
			if isOfEnumeration {
				kind = "of"
			}
			p.failIfFalse(statement != nil, "Expected statement as body of for-", kind, " statement")
			lexicalVariables := popLexicalScopeIfNecessary()
			if isOfEnumeration {
				return ctx.createForOfLoopWithPattern(isAwaitFor, location, forInTarget, e, statement, declLocation, declsStart, inLocation, exprEnd, lexicalVariables)
			}
			if isVarDeclaration && forInInitializer != nil {
				return ctx.createForInLoop(location, decls, e, statement, declLocation, declsStart, inLocation, exprEnd, lexicalVariables)
			}
			return ctx.createForInLoopWithPattern(location, forInTarget, e, statement, declLocation, declsStart, inLocation, exprEnd, lexicalVariables)
		}
	}

	if !gotoStandardForLoop && !p.match(SEMICOLON) {
		if p.match(OPENBRACE) || p.match(OPENBRACKET) {
			sp := p.createSavePoint(ctx)
			candidate, _ := tryParse(p, func() TreeDestructuringPattern {
				return p.tryParseDestructuringPatternExpression(ctx, AssignmentContextAssignmentExpression)
			})
			if candidate != nil && !p.hasError() && (p.match(INTOKEN) || p.matchContextualKeyword(ident.Of)) {
				pat = candidate
				gotoEnumerationLoop = true
			} else {
				pat = nil
				p.restoreSavePoint(ctx, sp)
			}
		}
		if !gotoEnumerationLoop {
			p.allowsIn = false
			decls = p.parseExpression(ctx)
			p.allowsIn = true
			p.failIfFalse(decls != nil, "Cannot parse for loop declarations")
			p.recordPauseLocation(ctx.breakpointLocation(decls))
		}
	}

	if gotoStandardForLoop || (!gotoEnumerationLoop && p.match(SEMICOLON)) {
		p.failIfFalse(!isAwaitFor, "Unexpected a ';' in for-await-of header")
		// Standard for loop.
		if decls != nil {
			p.recordPauseLocation(ctx.breakpointLocation(decls))
		}
		p.next(0)
		var condition TreeExpression
		p.failIfTrue(forLoopConstDoesNotHaveInitializer && isConstDeclaration, "const variables in for loops must have initializers")
		p.failIfTrue(forLoopConstDoesNotHaveInitializer && isAnyUsingDeclaration, "'using' declaration requires an initializer")

		if !p.match(SEMICOLON) {
			condition = p.parseExpression(ctx)
			p.failIfFalse(condition != nil, "Cannot parse for loop condition expression")
			p.recordPauseLocation(ctx.breakpointLocation(condition))
		}
		p.consumeOrFail(SEMICOLON, "Expected a ';' after the for loop condition expression")

		var increment TreeExpression
		if !p.match(CLOSEPAREN) {
			increment = p.parseExpression(ctx)
			p.failIfFalse(increment != nil, "Cannot parse for loop iteration expression")
			p.recordPauseLocation(ctx.breakpointLocation(increment))
		}
		p.handleProductionOrFail(CLOSEPAREN, ")", "end", "for-loop header")
		unused := ident.Null
		p.currentScope.startLoop()
		statement := p.parseStatement(ctx, &unused, nil)
		p.currentScope.endLoop()
		p.failIfFalse(statement != nil, "Expected a statement as the body of a for loop")
		lexicalVariables := popLexicalScopeIfNecessary()
		return ctx.createForLoop(location, decls, condition, increment, statement, lexicalVariables, forLoopInitializerContainsClosure)
	}

	// for-in and for-of loops without a declaration.
	p.failIfFalse(nonLHSCount == p.parserState.nonLHSCount, "Expected a reference on the left hand side of an enumeration statement")
	isOfEnumeration := false
	inLocation := p.tokenStartPosition()
	if !p.match(INTOKEN) {
		p.failIfFalse(p.matchContextualKeyword(ident.Of), "Expected either 'in' or 'of' in enumeration syntax")
		isOfEnumeration = true
		p.next(0)
	} else {
		p.failIfFalse(!isAwaitFor, "Expected 'of' in for-await syntax")
		p.next(0)
	}

	var e TreeExpression
	if isOfEnumeration {
		e = p.parseAssignmentExpression(ctx)
	} else {
		e = p.parseExpression(ctx)
	}
	kind := "in"
	if isOfEnumeration {
		kind = "of"
	}
	p.failIfFalse(e != nil, "Cannot parse subject for-", kind, " statement")
	p.recordPauseLocation(ctx.breakpointLocation(e))
	exprEnd := p.lastTokenEndPosition()

	header := "for-in header"
	if isOfEnumeration {
		header = "for-of header"
	}
	p.handleProductionOrFail(CLOSEPAREN, ")", "end", header)
	unused := ident.Null
	p.currentScope.startLoop()
	statement := p.parseStatement(ctx, &unused, nil)
	p.currentScope.endLoop()
	p.failIfFalse(statement != nil, "Expected a statement as the body of a for-", kind, " loop")
	if pat != nil {
		lexicalVariables := popLexicalScopeIfNecessary()
		if isOfEnumeration {
			return ctx.createForOfLoopWithPattern(isAwaitFor, location, pat, e, statement, declLocation, declsStart, inLocation, exprEnd, lexicalVariables)
		}
		return ctx.createForInLoopWithPattern(location, pat, e, statement, declLocation, declsStart, inLocation, exprEnd, lexicalVariables)
	}

	p.semanticFailIfFalse(p.isSimpleAssignmentTarget(ctx, decls, false), "Left side of assignment is not a reference")

	lexicalVariables := popLexicalScopeIfNecessary()
	if isOfEnumeration {
		return ctx.createForOfLoop(isAwaitFor, location, decls, e, statement, declLocation, declsStart, inLocation, exprEnd, lexicalVariables)
	}
	return ctx.createForInLoop(location, decls, e, statement, declLocation, declsStart, inLocation, exprEnd, lexicalVariables)
}

func (p *Parser) parseBreakStatement(ctx treeBuilder) TreeStatement {
	location := p.tokenLocation()
	start := p.tokenStartPosition()
	end := p.tokenEndPosition()
	p.next(0)

	isBreakValid, haveBreakValid := false, false
	if p.currentScope.isStaticBlock {
		isBreakValid, haveBreakValid = p.breakIsValid(), true
		p.semanticFailIfTrue(!p.currentScope.breakIsValid() && !isBreakValid, "'break' cannot cross static block boundary")
	}

	if p.autoSemiColon() {
		if !haveBreakValid {
			isBreakValid = p.breakIsValid()
		}
		p.semanticFailIfFalse(isBreakValid, "'break' is only valid inside a switch or loop statement")
		return ctx.createBreakStatement(location, ident.Null, start, end)
	}
	p.failIfFalse(p.matchSpecIdentifier(), "Expected an identifier as the target for a break statement")
	label := p.token.Data.Ident
	p.semanticFailIfFalse(p.getLabel(label) != nil, "Cannot use the undeclared label '", label, "'")
	end = p.tokenEndPosition()
	p.next(0)
	p.failIfFalse(p.autoSemiColon(), "Expected a ';' following a targeted break statement")
	return ctx.createBreakStatement(location, label, start, end)
}

func (p *Parser) parseContinueStatement(ctx treeBuilder) TreeStatement {
	location := p.tokenLocation()
	start := p.tokenStartPosition()
	end := p.tokenEndPosition()
	p.next(0)

	isContinueValid, haveContinueValid := false, false
	if p.currentScope.isStaticBlock {
		isContinueValid, haveContinueValid = p.continueIsValid(), true
		p.semanticFailIfTrue(!p.currentScope.continueIsValid() && !isContinueValid, "'continue' cannot cross static block boundary")
	}

	if p.autoSemiColon() {
		if !haveContinueValid {
			isContinueValid = p.continueIsValid()
		}
		p.semanticFailIfFalse(isContinueValid, "'continue' is only valid inside a loop statement")
		return ctx.createContinueStatement(location, ident.Null, start, end)
	}
	p.failIfFalse(p.matchSpecIdentifier(), "Expected an identifier as the target for a continue statement")
	label := p.token.Data.Ident
	info := p.getLabel(label)
	p.semanticFailIfFalse(info != nil, "Cannot use the undeclared label '", label, "'")
	p.semanticFailIfFalse(info.isLoop, "Cannot continue to the label '", label, "' as it is not targeting a loop")
	end = p.tokenEndPosition()
	p.next(0)
	p.failIfFalse(p.autoSemiColon(), "Expected a ';' following a targeted continue statement")
	return ctx.createContinueStatement(location, label, start, end)
}

func (p *Parser) parseReturnStatement(ctx treeBuilder) TreeStatement {
	p.parserState.returnStatementCount++
	location := p.tokenLocation()
	p.semanticFailIfFalse(p.currentScope.isFunction && !p.currentScope.isStaticBlock, "Return statements are only valid inside functions")
	start := p.tokenStartPosition()
	end := p.tokenEndPosition()
	p.next(0)
	// The automatic semicolon check comes first so that a line break after
	// `return` terminates the statement.
	if p.match(SEMICOLON) {
		end = p.tokenEndPosition()
	}
	if p.autoSemiColon() {
		return ctx.createReturnStatement(location, nil, start, end)
	}
	e := p.parseExpression(ctx)
	p.failIfFalse(e != nil, "Cannot parse the return expression")
	end = p.lastTokenEndPosition()
	if p.match(SEMICOLON) {
		end = p.tokenEndPosition()
	}
	p.failIfFalse(p.autoSemiColon(), "Expected a ';' following a return statement")
	return ctx.createReturnStatement(location, e, start, end)
}

func (p *Parser) parseThrowStatement(ctx treeBuilder) TreeStatement {
	location := p.tokenLocation()
	start := p.tokenStartPosition()
	p.next(0)
	p.failIfTrue(p.match(SEMICOLON), "Expected expression after 'throw'")
	p.semanticFailIfTrue(p.autoSemiColon(), "Cannot have a newline after 'throw'")

	e := p.parseExpression(ctx)
	p.failIfFalse(e != nil, "Cannot parse expression for throw statement")
	end := p.lastTokenEndPosition()
	p.failIfFalse(p.autoSemiColon(), "Expected a ';' after a throw statement")
	return ctx.createThrowStatement(location, e, start, end)
}

func (p *Parser) parseWithStatement(ctx treeBuilder) TreeStatement {
	location := p.tokenLocation()
	p.semanticFailIfTrue(p.strictMode(), "'with' statements are not valid in strict mode")
	p.currentScope.needsFullActivation = true
	p.next(0)

	p.handleProductionOrFail(OPENPAREN, "(", "start", "subject of a 'with' statement")
	start := p.tokenStart()
	e := p.parseExpression(ctx)
	p.failIfFalse(e != nil, "Cannot parse 'with' subject expression")
	p.recordPauseLocation(ctx.breakpointLocation(e))
	end := p.lastTokenEndPosition()
	p.handleProductionOrFail(CLOSEPAREN, ")", "start", "subject of a 'with' statement")

	withScope := p.newAutoPopScope(p.pushScope())
	defer withScope.cleanup()
	withScope.scope.setTaintedByWithScope()
	withScope.scope.preventAllVariableDeclarations()

	unused := ident.Null
	statement := p.parseStatement(ctx, &unused, nil)
	p.failIfFalse(statement != nil, "A 'with' statement must have a body")

	result := ctx.createWithStatement(location, e, statement, start, end)
	withScope.pop(ctx.needsFreeVariableInfo())
	return result
}

func (p *Parser) parseSwitchStatement(ctx treeBuilder) TreeStatement {
	location := p.tokenLocation()
	p.next(0)
	p.handleProductionOrFail(OPENPAREN, "(", "start", "subject of a 'switch'")
	e := p.parseExpression(ctx)
	p.failIfFalse(e != nil, "Cannot parse switch subject expression")
	p.recordPauseLocation(ctx.breakpointLocation(e))

	p.handleProductionOrFail(CLOSEPAREN, ")", "end", "subject of a 'switch'")
	p.handleProductionOrFail(OPENBRACE, "{", "start", "body of a 'switch'")
	lexicalScope := p.newAutoPopScope(p.pushScope())
	defer lexicalScope.cleanup()
	lexicalScope.scope.setIsLexicalScope()
	lexicalScope.scope.preventVarDeclarations()
	p.currentScope.startSwitch()
	firstClauses := p.parseSwitchClauses(ctx)
	p.propagateError()

	defaultClause := p.parseSwitchDefaultClause(ctx)
	p.propagateError()

	secondClauses := p.parseSwitchClauses(ctx)
	p.propagateError()
	p.currentScope.endSwitch()
	p.handleProductionOrFail(CLOSEBRACE, "}", "end", "body of a 'switch'")

	env, functions := lexicalScope.pop(ctx.needsFreeVariableInfo())
	return ctx.createSwitchStatement(location, e, firstClauses, defaultClause, secondClauses, env, functions)
}

func (p *Parser) parseSwitchClauses(ctx treeBuilder) TreeClauseList {
	if !p.match(CASE) {
		return nil
	}
	startOffset := p.tokenStart()
	p.next(0)
	condition := p.parseExpression(ctx)
	p.failIfFalse(condition != nil, "Cannot parse switch clause")
	p.consumeOrFail(COLON, "Expected a ':' after switch clause expression")
	defer setForScope(&p.insideSwitchCaseBody, true)()
	statements := p.parseSourceElements(ctx, DontCheckForStrictMode)
	p.failIfFalse(statements != nil, "Cannot parse the body of a switch clause")
	clause := ctx.createClause(condition, statements)
	ctx.setStartOffset(clause, startOffset)
	clauseList := ctx.createClauseList(nil, clause)
	tail := clauseList

	for p.match(CASE) {
		startOffset = p.tokenStart()
		p.next(0)
		condition := p.parseExpression(ctx)
		p.failIfFalse(condition != nil, "Cannot parse switch case expression")
		p.consumeOrFail(COLON, "Expected a ':' after switch clause expression")
		statements := p.parseSourceElements(ctx, DontCheckForStrictMode)
		p.failIfFalse(statements != nil, "Cannot parse the body of a switch clause")
		clause = ctx.createClause(condition, statements)
		ctx.setStartOffset(clause, startOffset)
		tail = ctx.createClauseList(tail, clause)
	}
	return clauseList
}

func (p *Parser) parseSwitchDefaultClause(ctx treeBuilder) TreeClause {
	if !p.match(DEFAULT) {
		return nil
	}
	startOffset := p.tokenStart()
	p.next(0)
	p.consumeOrFail(COLON, "Expected a ':' after switch default clause")
	defer setForScope(&p.insideSwitchCaseBody, true)()
	statements := p.parseSourceElements(ctx, DontCheckForStrictMode)
	p.failIfFalse(statements != nil, "Cannot parse the body of a switch default clause")
	result := ctx.createClause(nil, statements)
	ctx.setStartOffset(result, startOffset)
	return result
}

func (p *Parser) parseTryStatement(ctx treeBuilder) TreeStatement {
	location := p.tokenLocation()
	var tryBlock, catchBlock, finallyBlock TreeStatement
	var catchPattern TreeDestructuringPattern
	p.next(0)
	p.matchOrFail(OPENBRACE, "Expected a block statement as body of a try statement")

	returnStatementCountBeforeTryBlock := p.parserState.returnStatementCount
	tryBlock = p.parseBlockStatement(ctx, blockTypeNormal)
	p.failIfFalse(tryBlock != nil, "Cannot parse the body of try block")
	tryBlockContainsReturn := p.parserState.returnStatementCount != returnStatementCountBeforeTryBlock
	var catchEnvironment VariableEnvironment
	if p.consume(CATCH, 0) {
		if p.match(OPENBRACE) {
			catchBlock = p.parseBlockStatement(ctx, blockTypeNormal)
			p.failIfFalse(catchBlock != nil, "Unable to parse 'catch' block")
		} else {
			catchPattern, catchBlock, catchEnvironment = p.parseCatchClause(ctx)
		}
	}

	if p.consume(FINALLY, 0) {
		p.matchOrFail(OPENBRACE, "Expected block statement for finally body")
		finallyBlock = p.parseBlockStatement(ctx, blockTypeNormal)
		p.failIfFalse(finallyBlock != nil, "Cannot parse finally body")
	}
	p.failIfFalse(catchBlock != nil || finallyBlock != nil, "Try statements must have at least a catch or finally block")

	if tryBlockContainsReturn && finallyBlock == nil && p.currentFunctionScope().constructorKind == ConstructorKindExtends {
		// An empty finally keeps BytecodeGenerator::emitReturn() out of the try
		// block, so errors thrown by the derived-constructor return checks are
		// not caught by the catch block.
		finallyBlock = ctx.createEmptyStatement(location)
	}

	return ctx.createTryStatement(location, tryBlock, catchPattern, catchBlock, finallyBlock, catchEnvironment)
}

// parseCatchClause parses `(param) { ... }` after `catch`.
func (p *Parser) parseCatchClause(ctx treeBuilder) (TreeDestructuringPattern, TreeStatement, VariableEnvironment) {
	p.handleProductionOrFail(OPENPAREN, "(", "start", "'catch' target")
	defer saveForScope(&p.statementDepth)()
	p.semanticFailIfTrue(p.currentScope.isStaticBlock && p.match(AWAIT), "Cannot use 'await' as identifier within static block")
	p.statementDepth++
	catchScope := p.newAutoPopScope(p.pushScope())
	defer catchScope.cleanup()
	catchScope.scope.setIsLexicalScope()
	catchScope.scope.preventVarDeclarations()

	var catchPattern TreeDestructuringPattern
	name := ident.Null
	if p.matchSpecIdentifier() {
		catchScope.scope.isSimpleCatchParameterScope = true
		name = p.token.Data.Ident
		catchPattern = ctx.createBindingLocation(p.token.Location(), name, p.token.Start, p.token.End, AssignmentContextDeclarationStatement)
		p.next(0)
		p.failIfTrueIfStrict(catchScope.scope.declareLexicalVariable(name, false, DeclarationNotImported, false, false)&DeclarationInvalidStrictMode != 0, "Cannot declare a catch variable named '", name, "' in strict mode")
	} else {
		catchPattern = p.parseDestructuringPattern(ctx, DestructureToCatchParameters, notExported, nil, nil, AssignmentContextDeclarationStatement, 0)
		p.failIfFalse(catchPattern != nil, "Cannot parse this destructuring pattern")
	}
	p.handleProductionOrFail(CLOSEPAREN, ")", "end", "'catch' target")
	p.matchOrFail(OPENBRACE, "Expected exception handler to be a block statement")
	catchBlock := p.parseBlockStatement(ctx, blockTypeCatchBlock)
	p.failIfFalse(catchBlock != nil, "Unable to parse 'catch' block")
	// `try { } catch (/* never used */ error) { }`
	if !name.IsNull() && !catchScope.scope.usedVariablesContains(name) && !catchScope.scope.usesEval && !catchScope.scope.hasVariableBeingHoisted(name) {
		catchPattern = nil
	}
	env, _ := catchScope.pop(ctx.needsFreeVariableInfo())
	return catchPattern, catchBlock, env
}

func (p *Parser) parseDebuggerStatement(ctx treeBuilder) TreeStatement {
	location := p.tokenLocation()
	p.next(0)
	p.failIfFalse(p.autoSemiColon(), "Debugger keyword must be followed by a ';'")
	return ctx.createDebugger(location)
}

func (p *Parser) parseBlockStatement(ctx treeBuilder, t blockType) TreeStatement {
	// A block inside a switch clause allows using declarations again.
	defer setForScope(&p.insideSwitchCaseBody, false)()

	// The first block of a function is the function's own lexical scope.
	var lexicalScope *autoPopScope
	shouldPushLexicalScope := p.statementDepth > 0 || t == blockTypeStaticBlock
	if shouldPushLexicalScope {
		newScope := p.pushScope()
		newScope.setIsLexicalScope()
		switch t {
		case blockTypeCatchBlock:
			newScope.isCatchBlockScope = true
			newScope.preventVarDeclarations()
		case blockTypeStaticBlock:
			newScope.setSourceParseMode(ClassStaticBlockMode)
			newScope.expectedSuperBinding = SuperBindingNeeded
		default:
			newScope.preventVarDeclarations()
		}
		lexicalScope = p.newAutoPopScope(newScope)
		defer lexicalScope.cleanup()
	}
	location := p.tokenLocation()
	startOffset := int(p.token.Start)
	var env VariableEnvironment
	var functions FunctionStack
	p.next(0)
	if p.match(CLOSEBRACE) {
		endOffset := int(p.token.Start)
		p.next(0)
		if shouldPushLexicalScope {
			env, functions = lexicalScope.pop(ctx.needsFreeVariableInfo())
		}
		result := ctx.createBlockStatement(location, nil, env, functions)
		ctx.setStartOffset(result, startOffset)
		ctx.setEndOffset(result, endOffset)
		return result
	}
	subtree := p.parseSourceElements(ctx, DontCheckForStrictMode)
	p.failIfFalse(subtree != nil, "Cannot parse the body of the block statement")
	p.matchOrFail(CLOSEBRACE, "Expected a closing '}' at the end of a block statement")
	endOffset := int(p.token.Start)
	p.next(0)
	if shouldPushLexicalScope {
		env, functions = lexicalScope.pop(ctx.needsFreeVariableInfo())
	}
	result := ctx.createBlockStatement(location, subtree, env, functions)
	ctx.setStartOffset(result, startOffset)
	ctx.setEndOffset(result, endOffset)
	return result
}

func (p *Parser) parseStatement(ctx treeBuilder, directive *ident.Identifier, directiveLiteralLength *int) TreeStatement {
	defer saveForScope(&p.statementDepth)()
	p.statementDepth++
	nonTrivialExpressionCount := 0
	p.enterRecursion()
	defer p.exitRecursion()
	var result TreeStatement
	shouldSetEndOffset := true
	shouldSetPauseLocation := false
	parentAllowsFunctionDeclarationAsStatement := p.immediateParentAllowsFunctionDeclarationInStatement
	p.immediateParentAllowsFunctionDeclarationInStatement = false

	switch p.token.Type {
	case OPENBRACE:
		result = p.parseBlockStatement(ctx, blockTypeNormal)
		shouldSetEndOffset = false
	case VAR:
		result = p.parseVariableDeclaration(ctx, VarDeclaration, notExported)
		shouldSetPauseLocation = true
	case FUNCTION:
		result = p.parseFunctionDeclarationStatement(ctx, parentAllowsFunctionDeclarationAsStatement)
	case SEMICOLON:
		location := p.tokenLocation()
		p.next(0)
		result = ctx.createEmptyStatement(location)
	case IF:
		result = p.parseIfStatement(ctx)
	case DO:
		result = p.parseDoWhileStatement(ctx)
	case WHILE:
		result = p.parseWhileStatement(ctx)
	case FOR:
		result = p.parseForStatement(ctx)
	case CONTINUE:
		result = p.parseContinueStatement(ctx)
		shouldSetPauseLocation = true
	case BREAK:
		result = p.parseBreakStatement(ctx)
		shouldSetPauseLocation = true
	case RETURN:
		result = p.parseReturnStatement(ctx)
		shouldSetPauseLocation = true
	case WITH:
		result = p.parseWithStatement(ctx)
	case SWITCH:
		result = p.parseSwitchStatement(ctx)
	case THROW:
		result = p.parseThrowStatement(ctx)
		shouldSetPauseLocation = true
	case TRY:
		result = p.parseTryStatement(ctx)
	case DEBUGGER:
		result = p.parseDebuggerStatement(ctx)
		shouldSetPauseLocation = true
	case EOFTOK, CASE, CLOSEBRACE, DEFAULT:
		// These tokens end a list of source elements.
		return nil
	case ESCAPED_KEYWORD, LET, IDENT, AWAIT, YIELD:
		if p.token.Type == ESCAPED_KEYWORD && !p.matchAllowedEscapedContextualKeyword() {
			p.failDueToUnexpectedToken()
		}
		result = p.parseExpressionOrLabelStatement(ctx, false)
		shouldSetPauseLocation = !ctx.shouldSkipPauseLocation(result)
	default:
		if p.token.Type == STRING {
			*directive = p.token.Data.Ident
			if directiveLiteralLength != nil {
				loc := p.token.Location()
				*directiveLiteralLength = loc.EndOffset - loc.StartOffset
			}
			nonTrivialExpressionCount = p.parserState.nonTrivialExpressionCount
		}
		exprStatement := p.parseExpressionStatement(ctx)
		if !directive.IsNull() && nonTrivialExpressionCount != p.parserState.nonTrivialExpressionCount {
			*directive = ident.Null
		}
		result = exprStatement
		shouldSetPauseLocation = true
	}

	if result != nil {
		if shouldSetEndOffset {
			ctx.setEndOffset(result, p.lastTokenLocation.EndOffset)
		}
		if shouldSetPauseLocation {
			p.recordPauseLocation(ctx.breakpointLocation(result))
		}
	}
	return result
}

func (p *Parser) parseFunctionDeclarationStatement(ctx treeBuilder, parentAllowsFunctionDeclarationAsStatement bool) TreeStatement {
	p.semanticFailIfTrue(p.strictMode(), "Function declarations are only allowed inside blocks or switch statements in strict mode")
	p.failIfFalse(parentAllowsFunctionDeclarationAsStatement, "Function declarations are only allowed inside block statements or at the top level of a program")

	// A function declaration directly in an if/else statement is treated as
	// if it were wrapped in a block (Annex B.3.4):
	//     if (cond) function foo() { }   =>   if (cond) { function foo() { } }
	blockScope := p.newAutoPopScope(p.pushScope())
	defer blockScope.cleanup()
	blockScope.scope.setIsLexicalScope()
	blockScope.scope.preventVarDeclarations()
	location := p.tokenLocation()

	function := p.parseFunctionDeclaration(ctx, functionDeclarationTypeStatement, notExported, DeclarationDefaultStandard, -1)
	p.propagateError()
	p.failIfFalse(function != nil, "Expected valid function statement after 'function' keyword")
	elements := ctx.createSourceElements()
	ctx.appendStatement(elements, function)
	env, functions := blockScope.pop(ctx.needsFreeVariableInfo())
	return ctx.createBlockStatement(location, elements, env, functions)
}

type labelInfo struct {
	label      ident.Identifier
	start, end TextPosition
}

func (p *Parser) parseExpressionOrLabelStatement(ctx treeBuilder, allowFunctionDeclarationAsStatement bool) TreeStatement {
	// Expression and label statements are ambiguous at LL(1); look ahead for a colon.
	var labels []labelInfo
	var location TokenLocation
	for {
		if !p.nextTokenIsColon() {
			// An expression statement can't use break/continue, so any labels
			// collected so far can be dropped.
			return p.parseExpressionStatement(ctx)
		}

		if p.isPossiblyEscapedLet(&p.token) && p.strictMode() {
			p.semanticFail("Cannot use 'let' as a label ", p.disallowedIdentifierLetReason())
		}
		if p.isDisallowedIdentifierAwait(&p.token) {
			p.semanticFail("Cannot use 'await' as a label ", p.disallowedIdentifierAwaitReason())
		}
		if p.isDisallowedIdentifierYield(&p.token) {
			p.semanticFail("Cannot use 'yield' as a label ", p.disallowedIdentifierYieldReason())
		}

		label := p.token.Data.Ident
		start := p.tokenStartPosition()
		end := p.tokenEndPosition()
		location = p.tokenLocation()
		p.next(0)
		p.consumeOrFail(COLON, "Labels must be followed by a ':'")

		for _, l := range labels {
			p.failIfTrue(label == l.label, "Attempted to redeclare the label '", label, "'")
		}
		p.failIfTrue(p.getLabel(label) != nil, "Cannot find scope for the label '", label, "'")
		labels = append(labels, labelInfo{label, start, end})
		if !p.matchSpecIdentifier() {
			break
		}
	}
	isLoop := false
	switch p.token.Type {
	case FOR, WHILE, DO:
		isLoop = true
	}
	unused := ident.Null
	labelScope := p.currentScope
	for _, l := range labels {
		labelScope.pushLabel(l.label, isLoop)
	}
	p.immediateParentAllowsFunctionDeclarationInStatement = allowFunctionDeclarationAsStatement
	statement := p.parseStatement(ctx, &unused, nil)
	for range labels {
		labelScope.popLabel()
	}
	p.failIfFalse(statement != nil, "Cannot parse statement")
	for i := len(labels) - 1; i >= 0; i-- {
		statement = ctx.createLabelStatement(location, labels[i].label, statement, labels[i].start, labels[i].end)
	}
	return statement
}

func (p *Parser) parseExpressionStatement(ctx treeBuilder) TreeStatement {
	switch p.token.Type {
	// https://tc39.es/ecma262/#sec-expression-statement. Annex B.3.1 allows a
	// labelled FunctionDeclaration in sloppy mode; that is handled in parseStatement.
	case CLASSTOKEN:
		p.failWithMessage("'class' declaration is not directly within a block statement")
	case LET:
		sp := p.createSavePoint(ctx)
		p.next(0)
		p.failIfTrue(p.match(OPENBRACKET), "Cannot use lexical declaration in single-statement context")
		p.restoreSavePoint(ctx, sp)
	case IDENT:
		if p.token.Data.Ident == ident.Async && !p.token.Data.Escaped {
			sp := p.createSavePoint(ctx)
			p.next(0)
			p.failIfTrue(p.match(FUNCTION) && !p.lexer.HasLineTerminatorBeforeToken(), "Cannot use async function declaration in single-statement context")
			p.restoreSavePoint(ctx, sp)
		}
	}

	start := p.tokenStartPosition()
	location := p.tokenLocation()
	e := p.parseExpression(ctx)
	p.failIfFalse(e != nil, "Cannot parse expression statement")
	if !p.autoSemiColon() {
		p.failDueToUnexpectedToken()
	}
	return ctx.createExprStatement(location, e, start)
}

func (p *Parser) parseIfStatement(ctx treeBuilder) TreeStatement {
	ifLocation := p.tokenLocation()
	p.next(0)
	p.handleProductionOrFail2(OPENPAREN, "(", "start", "'if' condition")

	condition := p.parseExpression(ctx)
	p.failIfFalse(condition != nil, "Expected an expression as the condition for an if statement")
	p.recordPauseLocation(ctx.breakpointLocation(condition))
	p.handleProductionOrFail2(CLOSEPAREN, ")", "end", "'if' condition")

	unused := ident.Null
	p.immediateParentAllowsFunctionDeclarationInStatement = true
	trueBlock := p.parseStatement(ctx, &unused, nil)
	p.failIfFalse(trueBlock != nil, "Expected a statement as the body of an if block")

	if !p.match(ELSE) {
		return ctx.createIfStatement(ifLocation, condition, trueBlock, nil)
	}

	// else-if chains are parsed iteratively and folded from the right.
	type pendingIf struct {
		condition TreeExpression
		location  TokenLocation
	}
	var exprStack []pendingIf
	var statementStack []TreeStatement
	trailingElse := false
	for {
		tempLocation := p.tokenLocation()
		p.next(0)
		if !p.match(IF) {
			p.immediateParentAllowsFunctionDeclarationInStatement = true
			block := p.parseStatement(ctx, &unused, nil)
			p.failIfFalse(block != nil, "Expected a statement as the body of an else block")
			statementStack = append(statementStack, block)
			trailingElse = true
			break
		}
		p.next(0)

		p.handleProductionOrFail2(OPENPAREN, "(", "start", "'if' condition")
		innerCondition := p.parseExpression(ctx)
		p.failIfFalse(innerCondition != nil, "Expected an expression as the condition for an if statement")
		p.recordPauseLocation(ctx.breakpointLocation(innerCondition))
		p.handleProductionOrFail2(CLOSEPAREN, ")", "end", "'if' condition")
		p.immediateParentAllowsFunctionDeclarationInStatement = true
		innerTrueBlock := p.parseStatement(ctx, &unused, nil)
		p.failIfFalse(innerTrueBlock != nil, "Expected a statement as the body of an if block")
		exprStack = append(exprStack, pendingIf{innerCondition, tempLocation})
		statementStack = append(statementStack, innerTrueBlock)
		if !p.match(ELSE) {
			break
		}
	}

	pop := func() TreeStatement {
		s := statementStack[len(statementStack)-1]
		statementStack = statementStack[:len(statementStack)-1]
		return s
	}
	if !trailingElse {
		last := exprStack[len(exprStack)-1]
		exprStack = exprStack[:len(exprStack)-1]
		trueBlock := pop()
		ifStatement := ctx.createIfStatement(last.location, last.condition, trueBlock, nil)
		ctx.setEndOffset(ifStatement, ctx.endOffset(trueBlock))
		statementStack = append(statementStack, ifStatement)
	}
	for len(exprStack) > 0 {
		last := exprStack[len(exprStack)-1]
		exprStack = exprStack[:len(exprStack)-1]
		falseBlock := pop()
		trueBlock := pop()
		ifStatement := ctx.createIfStatement(last.location, last.condition, trueBlock, falseBlock)
		ctx.setEndOffset(ifStatement, ctx.endOffset(falseBlock))
		statementStack = append(statementStack, ifStatement)
	}
	return ctx.createIfStatement(ifLocation, condition, trueBlock, statementStack[len(statementStack)-1])
}

// recordPauseLocation and friends feed the debugger; there is no debugger yet.
func (p *Parser) recordPauseLocation(TextPosition)         {}
func (p *Parser) recordFunctionEntryLocation(TextPosition) {}
func (p *Parser) recordFunctionLeaveLocation(TextPosition) {}
