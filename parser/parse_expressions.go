package parser

import (
	"github.com/baslentfert/jsc2go/ident"
)

// Expression, destructuring and module parsing (Parser.cpp).

// --- Destructuring --------------------------------------------------------------------

func (p *Parser) tryParseDestructuringPatternExpression(ctx treeBuilder, bindingContext AssignmentContext) TreeDestructuringPattern {
	return p.parseDestructuringPattern(ctx, DestructureToExpressions, notExported, nil, nil, bindingContext, 0)
}

func (p *Parser) parseBindingOrAssignmentElement(ctx treeBuilder, kind DestructuringKind, exportType exportType, duplicateIdentifier *ident.Identifier, hasDestructuringPattern *bool, bindingContext AssignmentContext, depth int) TreeDestructuringPattern {
	if kind == DestructureToExpressions {
		return p.parseAssignmentElement(ctx, kind, exportType, duplicateIdentifier, hasDestructuringPattern, bindingContext, depth)
	}
	return p.parseDestructuringPattern(ctx, kind, exportType, duplicateIdentifier, hasDestructuringPattern, bindingContext, depth)
}

func (p *Parser) parseObjectRestAssignmentElement(ctx treeBuilder) TreeDestructuringPattern {
	startPosition := p.tokenStartPosition()
	element := p.parseMemberExpression(ctx)
	p.semanticFailIfTrue(element == nil || !ctx.isAssignmentLocation(element), "Invalid destructuring assignment target")
	if p.strictMode() && !p.parserState.lastIdentifier.IsNull() && ctx.isResolve(element) {
		last := p.parserState.lastIdentifier
		p.failIfTrue(isEvalOrArgumentsIdentifier(last), "Cannot modify '", last, "' in strict mode")
	}
	return ctx.createAssignmentElement(element, startPosition, p.lastTokenEndPosition())
}

func (p *Parser) parseAssignmentElement(ctx treeBuilder, kind DestructuringKind, exportType exportType, duplicateIdentifier *ident.Identifier, hasDestructuringPattern *bool, bindingContext AssignmentContext, depth int) TreeDestructuringPattern {
	if p.match(OPENBRACE) || p.match(OPENBRACKET) {
		sp := p.createSavePoint(ctx)
		target, _ := tryParse(p, func() TreeDestructuringPattern {
			return p.parseDestructuringPattern(ctx, kind, exportType, duplicateIdentifier, hasDestructuringPattern, bindingContext, depth)
		})
		if target != nil && !p.hasError() && !p.match(DOT) && !p.match(OPENBRACKET) && !p.match(OPENPAREN) && !p.match(BACKQUOTE) {
			return target
		}
		p.restoreSavePoint(ctx, sp)
	}

	startPosition := p.tokenStartPosition()
	element := p.parseMemberExpression(ctx)
	p.semanticFailIfFalse(element != nil && ctx.isAssignmentLocation(element), "Invalid destructuring assignment target")

	if p.strictMode() && !p.parserState.lastIdentifier.IsNull() && ctx.isResolve(element) {
		last := p.parserState.lastIdentifier
		p.failIfTrueIfStrict(isEvalOrArgumentsIdentifier(last), "Cannot modify '", last, "' in strict mode")
	}
	return ctx.createAssignmentElement(element, startPosition, p.lastTokenEndPosition())
}

func destructuringKindToVariableKindName(kind DestructuringKind) string {
	switch kind {
	case DestructureToLet, DestructureToConst:
		return "lexical variable name"
	case DestructureToVariables:
		return "variable name"
	case DestructureToParameters:
		return "parameter name"
	case DestructureToCatchParameters:
		return "catch parameter name"
	}
	return "expression name"
}

func (p *Parser) parseObjectRestElement(ctx treeBuilder, kind DestructuringKind, exportType exportType, duplicateIdentifier *ident.Identifier, bindingContext AssignmentContext) TreeDestructuringPattern {
	p.enterRecursion()
	defer p.exitRecursion()

	if !p.matchSpecIdentifier() {
		p.semanticFailureDueToKeyword(destructuringKindToVariableKindName(kind))
		p.failWithMessage("Expected a binding element")
	}
	p.failIfTrue(p.match(LET) && (kind == DestructureToLet || kind == DestructureToConst), "Cannot use 'let' as an identifier name for a LexicalDeclaration")
	if p.isDisallowedIdentifierAwait(&p.token) {
		p.semanticFail("Cannot use 'await' as a ", destructuringKindToVariableKindName(kind), " ", p.disallowedIdentifierAwaitReason())
	}
	tok := p.token
	pat := p.createBindingPattern(ctx, kind, exportType, p.token.Data.Ident, &tok, bindingContext, duplicateIdentifier)
	p.next(0)
	return pat
}

func (p *Parser) parseObjectRestBindingOrAssignmentElement(ctx treeBuilder, kind DestructuringKind, exportType exportType, duplicateIdentifier *ident.Identifier, bindingContext AssignmentContext) TreeDestructuringPattern {
	if kind == DestructureToExpressions {
		return p.parseObjectRestAssignmentElement(ctx)
	}
	return p.parseObjectRestElement(ctx, kind, exportType, duplicateIdentifier, bindingContext)
}

func (p *Parser) parseDestructuringPattern(ctx treeBuilder, kind DestructuringKind, exportType exportType, duplicateIdentifier *ident.Identifier, hasDestructuringPattern *bool, bindingContext AssignmentContext, depth int) TreeDestructuringPattern {
	p.enterRecursion()
	defer p.exitRecursion()
	p.parserState.assignmentCount++
	defer saveForScope(&p.parserState.nonLHSCount)()

	switch p.token.Type {
	case OPENBRACKET:
		divotStart := p.tokenStartPosition()
		arrayPattern := ctx.createArrayPattern(p.token.Location())
		p.next(0)
		if hasDestructuringPattern != nil {
			*hasDestructuringPattern = true
		}
		restElementWasFound := false
		for {
			for p.match(COMMA) {
				ctx.appendArrayPatternSkipEntry(arrayPattern, p.token.Location())
				p.next(0)
			}
			p.propagateError()

			if p.match(CLOSEBRACKET) {
				break
			}

			if p.match(DOTDOTDOT) {
				location := p.token.Location()
				p.next(0)
				inner := p.parseBindingOrAssignmentElement(ctx, kind, exportType, duplicateIdentifier, hasDestructuringPattern, bindingContext, depth+1)
				if kind == DestructureToExpressions && inner == nil {
					return nil
				}
				p.failIfFalse(inner != nil, "Cannot parse this destructuring pattern")
				ctx.appendArrayPatternRestEntry(arrayPattern, location, inner)
				restElementWasFound = true
				break
			}

			location := p.token.Location()
			inner := p.parseBindingOrAssignmentElement(ctx, kind, exportType, duplicateIdentifier, hasDestructuringPattern, bindingContext, depth+1)
			if kind == DestructureToExpressions && inner == nil {
				return nil
			}
			p.failIfFalse(inner != nil, "Cannot parse this destructuring pattern")
			defaultValue := p.parseDefaultValueForDestructuringPattern(ctx)
			p.propagateError()
			ctx.appendArrayPatternEntry(arrayPattern, location, inner, defaultValue)
			if !p.consume(COMMA, 0) {
				break
			}
		}

		msg := "Expected either a closing ']' or a ',' following an element destructuring pattern"
		if restElementWasFound {
			msg = "Expected a closing ']' following a rest element destructuring pattern"
		}
		p.consumeOrFail(CLOSEBRACKET, msg)
		ctx.finishArrayPattern(arrayPattern, divotStart, divotStart, p.lastTokenEndPosition())
		return arrayPattern

	case OPENBRACE:
		divotStart := p.tokenStartPosition()
		objectPattern := ctx.createObjectPattern(p.token.Location())
		p.next(0)
		if hasDestructuringPattern != nil {
			*hasDestructuringPattern = true
		}
		restElementWasFound := false
		for {
			wasString := false
			if p.match(CLOSEBRACE) {
				break
			}

			if p.match(DOTDOTDOT) {
				location := p.token.Location()
				p.next(0)
				inner := p.parseObjectRestBindingOrAssignmentElement(ctx, kind, exportType, duplicateIdentifier, bindingContext)
				p.propagateError()
				if inner == nil {
					return nil
				}
				ctx.appendObjectPatternRestEntry(objectPattern, location, inner)
				restElementWasFound = true
				ctx.setContainsObjectRestElement(objectPattern, restElementWasFound)
				break
			}

			propertyName := ident.Null
			var propertyExpression TreeExpression
			var inner TreeDestructuringPattern
			location := p.token.Location()
			escapedKeyword := p.match(ESCAPED_KEYWORD)
			if escapedKeyword || p.matchSpecIdentifier() {
				letMatched := p.match(LET)
				propertyName = p.token.Data.Ident
				identifierToken := p.token
				p.next(0)
				if p.consume(COLON, 0) {
					inner = p.parseBindingOrAssignmentElement(ctx, kind, exportType, duplicateIdentifier, hasDestructuringPattern, bindingContext, depth+1)
				} else {
					p.semanticFailIfTrue(letMatched && (kind == DestructureToLet || kind == DestructureToConst), "Cannot use the keyword 'let' as a lexical variable name")
					p.semanticFailIfTrue(escapedKeyword, "Cannot use abbreviated destructuring syntax for keyword '", propertyName, "'")
					if p.isDisallowedIdentifierAwait(&identifierToken) {
						p.semanticFail("Cannot use 'await' as a ", destructuringKindToVariableKindName(kind), " ", p.disallowedIdentifierAwaitReason())
					}
					if kind == DestructureToExpressions {
						p.failIfTrueIfStrict(isEvalOrArgumentsIdentifier(propertyName), "Cannot modify '", propertyName, "' in strict mode")
						if p.match(EQUAL) {
							p.currentScope.useVariable(propertyName, propertyName == ident.Eval)
						}
					}
					inner = p.createBindingPattern(ctx, kind, exportType, propertyName, &identifierToken, bindingContext, duplicateIdentifier)
				}
			} else {
				tokenType := p.token.Type
				switch tokenType {
				case DOUBLE, INTEGER:
					propertyName = ident.FromNumber(p.token.Data.Double)
				case STRING:
					propertyName = p.token.Data.Ident
					wasString = true
				case BIGINT:
					var ok bool
					propertyName, ok = bigIntToDecimal(p.token.Data.BigIntString, p.token.Data.Radix)
					p.failIfFalse(ok, "Cannot parse big int property name")
				case OPENBRACKET:
					p.next(0)
					propertyExpression = p.parseAssignmentExpression(ctx)
					p.failIfFalse(propertyExpression != nil, "Cannot parse computed property name")
					p.matchOrFail(CLOSEBRACKET, "Expected ']' to end end a computed property name")
				default:
					if p.token.Type != RESERVED && p.token.Type != RESERVED_IF_STRICT && p.token.Type&KeywordTokenFlag == 0 {
						if kind == DestructureToExpressions {
							return nil
						}
						p.failWithMessage("Expected a property name")
					}
					propertyName = p.token.Data.Ident
				}
				p.next(0)
				if !p.consume(COLON, 0) {
					if kind == DestructureToExpressions {
						return nil
					}
					p.semanticFailIfTrue(tokenType == RESERVED, "Cannot use abbreviated destructuring syntax for reserved name '", propertyName, "'")
					p.semanticFailIfTrue(tokenType == RESERVED_IF_STRICT, "Cannot use abbreviated destructuring syntax for reserved name '", propertyName, "' in strict mode")
					p.semanticFailIfTrue(tokenType&KeywordTokenFlag != 0, "Cannot use abbreviated destructuring syntax for keyword '", propertyName, "'")
					p.failWithMessage("Expected a ':' prior to a named destructuring property")
				}
				inner = p.parseBindingOrAssignmentElement(ctx, kind, exportType, duplicateIdentifier, hasDestructuringPattern, bindingContext, depth+1)
			}
			if kind == DestructureToExpressions && inner == nil {
				return nil
			}
			p.failIfFalse(inner != nil, "Cannot parse this destructuring pattern")
			defaultValue := p.parseDefaultValueForDestructuringPattern(ctx)
			p.propagateError()
			if propertyExpression != nil {
				ctx.appendObjectPatternComputedEntry(objectPattern, location, propertyExpression, inner, defaultValue)
				ctx.setContainsComputedProperty(objectPattern, true)
			} else {
				ctx.appendObjectPatternEntry(objectPattern, location, wasString, propertyName, inner, defaultValue)
			}
			if !p.consume(COMMA, 0) {
				break
			}
		}

		if kind == DestructureToExpressions && !p.match(CLOSEBRACE) {
			return nil
		}
		msg := "Expected either a closing '}' or an ',' after a property destructuring pattern"
		if restElementWasFound {
			msg = "Expected a closing '}' following a rest element destructuring pattern"
		}
		p.consumeOrFail(CLOSEBRACE, msg)
		ctx.finishObjectPattern(objectPattern, divotStart, divotStart, p.lastTokenEndPosition())
		return objectPattern
	}

	if !p.matchSpecIdentifier() {
		if kind == DestructureToExpressions {
			return nil
		}
		p.semanticFailureDueToKeyword(destructuringKindToVariableKindName(kind))
		p.failIfTrue(kind != DestructureToParameters && p.match(PRIVATENAME), "Cannot use a private name as a ", destructuringKindToVariableKindName(kind))
		p.failWithMessage("Expected a parameter pattern or a ')' in parameter list")
	}
	p.failIfTrue(p.match(LET) && (kind == DestructureToLet || kind == DestructureToConst), "Cannot use 'let' as an identifier name for a LexicalDeclaration")
	if p.isDisallowedIdentifierAwait(&p.token) {
		p.semanticFail("Cannot use 'await' as a ", destructuringKindToVariableKindName(kind), " ", p.disallowedIdentifierAwaitReason())
	}
	tok := p.token
	pat := p.createBindingPattern(ctx, kind, exportType, p.token.Data.Ident, &tok, bindingContext, duplicateIdentifier)
	p.next(0)
	return pat
}

func (p *Parser) parseDefaultValueForDestructuringPattern(ctx treeBuilder) TreeExpression {
	if !p.match(EQUAL) {
		return nil
	}
	p.next(ctx.dontBuildStrings()) // consume '='
	return p.parseAssignmentExpression(ctx)
}

// --- Expressions ----------------------------------------------------------------------

func (p *Parser) parseExpression(ctx treeBuilder) TreeExpression {
	p.enterRecursion()
	defer p.exitRecursion()
	headLocation := p.tokenLocation()
	node := p.parseAssignmentExpression(ctx)
	p.failIfFalse(node != nil, "Cannot parse expression")
	ctx.setEndOffset(node, p.lastTokenLocation.EndOffset)
	if !p.match(COMMA) {
		return node
	}
	p.recordPauseLocation(ctx.breakpointLocation(node))
	p.next(0)
	p.parserState.nonTrivialExpressionCount++
	p.parserState.nonLHSCount++
	tailLocation := p.tokenLocation()
	right := p.parseAssignmentExpression(ctx)
	p.failIfFalse(right != nil, "Cannot parse expression in a comma expression")
	p.recordPauseLocation(ctx.breakpointLocation(right))
	ctx.setEndOffset(right, p.lastTokenLocation.EndOffset)
	head := ctx.createCommaExpr(headLocation, node)
	tail := ctx.appendToCommaExpr(tailLocation, head, right)
	for p.match(COMMA) {
		p.next(ctx.dontBuildStrings())
		tailLocation = p.tokenLocation()
		right = p.parseAssignmentExpression(ctx)
		p.failIfFalse(right != nil, "Cannot parse expression in a comma expression")
		ctx.setEndOffset(right, p.lastTokenLocation.EndOffset)
		p.recordPauseLocation(ctx.breakpointLocation(right))
		tail = ctx.appendToCommaExpr(tailLocation, tail, right)
	}
	ctx.setEndOffset(head, p.lastTokenLocation.EndOffset)
	return head
}

func (p *Parser) metaPropertyName(ctx treeBuilder, e TreeExpression) string {
	if ctx.isNewTarget(e) {
		return "new.target"
	}
	if ctx.isImportMeta(e) {
		return "import.meta"
	}
	return "error"
}

// isSimpleAssignmentTarget: for web compatibility, a call expression on the
// left is not an early error in sloppy mode (tc39/ecma262#3568).
func (p *Parser) isSimpleAssignmentTarget(ctx treeBuilder, e TreeExpression, ignoreStrictCheck bool) bool {
	return ctx.isLocation(e) || (!(p.strictMode() || ignoreStrictCheck) && ctx.isFunctionCall(e))
}

func (p *Parser) parseDestructuringAssignment(ctx treeBuilder, sp *savePoint, location TokenLocation, isPossiblePattern bool) TreeExpression {
	expressionErrorLocation := p.swapSavePointForError(ctx, sp)
	pat, _ := tryParse(p, func() TreeDestructuringPattern {
		return p.tryParseDestructuringPatternExpression(ctx, AssignmentContextAssignmentExpression)
	})

	// Restoring the expression's error only when the input could have been a
	// pattern gives better error messages.
	if isPossiblePattern && (pat == nil || !p.match(EQUAL)) {
		p.restoreSavePointWithError(ctx, expressionErrorLocation)
		p.propagateError()
	}
	p.failIfFalse(pat != nil, "Cannot parse assignment pattern")
	p.consumeOrFail(EQUAL, "Expected '=' following assignment pattern")
	rhs := p.parseAssignmentExpression(ctx)
	if rhs == nil {
		p.propagateError()
	}
	return ctx.createDestructuringAssignment(location, pat, rhs)
}

// parseArrowFunctionCandidate retries an expression that failed (or was
// followed by =>) as an arrow function. It reports whether its result is final.
func (p *Parser) parseArrowFunctionCandidate(ctx treeBuilder, sp *savePoint, location TokenLocation, isArrowFunctionToken, wasOpenParen bool, usedVariablesSize int) (TreeExpression, bool) {
	errorRestorationSavePoint := p.swapSavePointForError(ctx, sp)
	isAsync := false
	if p.matchContextualKeyword(ident.Async) {
		p.next(0)
		if !p.lexer.HasLineTerminatorBeforeToken() && (p.match(OPENPAREN) || p.matchSpecIdentifier()) {
			isAsync = true
		} else {
			// `async => ...`: here "async" is a parameter name, not a keyword.
			p.restoreSavePoint(ctx, *sp)
		}
	}

	isArrow, _ := tryParse(p, func() bool { return p.isArrowFunctionParameters(ctx) })
	if isArrow && !p.hasError() {
		if wasOpenParen {
			p.currentScope.revertToPreviousUsedVariables(usedVariablesSize)
		}
		return p.parseArrowFunctionExpression(ctx, isAsync, location), true
	}

	// With an explicit => keep this (better) error rather than the earlier one.
	if isArrowFunctionToken && p.hasError() {
		p.bail()
	}

	p.restoreSavePointWithError(ctx, errorRestorationSavePoint)
	if isArrowFunctionToken {
		p.failDueToUnexpectedToken()
	}
	return nil, false
}

func (p *Parser) parseAssignmentExpression(ctx treeBuilder) TreeExpression {
	p.enterRecursion()
	defer p.exitRecursion()

	if p.match(YIELD) && !p.canUseIdentifierYield() {
		return p.parseYieldExpression(ctx)
	}

	start := p.tokenStartPosition()
	location := p.tokenLocation()
	initialAssignmentCount := p.parserState.assignmentCount
	initialNonLHSCount := p.parserState.nonLHSCount
	maybeAssignmentPattern := p.match(OPENBRACE) || p.match(OPENBRACKET)
	wasOpenParen := p.match(OPENPAREN)
	// isArrowFunctionParameters validates spec identifiers later.
	wasIdentifierOrKeyword := p.matchIdentifierOrKeyword() || p.token.Type == ESCAPED_KEYWORD
	maybeValidArrowFunctionStart := wasOpenParen || wasIdentifierOrKeyword
	var sp savePoint
	if maybeValidArrowFunctionStart || maybeAssignmentPattern {
		sp = p.createSavePoint(ctx)
	}
	usedVariablesSize := 0

	if wasOpenParen {
		usedVariablesSize = p.currentScope.currentUsedVariablesSize()
		p.currentScope.pushUsedVariableSet()
	}

	// The left-hand side may fail to parse as an expression and still be an
	// arrow function or a destructuring pattern; keep the error and retry below.
	lhs, _ := tryParse(p, func() TreeExpression { return p.parseConditionalExpression(ctx) })

	// In a class static block, `((x = await) => 0)` would otherwise loop;
	// await is strictly limited there, so propagate immediately.
	if lhs == nil && p.currentScope.isStaticBlock && p.match(AWAIT) {
		p.propagateError()
	}

	if maybeValidArrowFunctionStart && !p.match(EOFTOK) {
		isArrowFunctionToken := p.match(ARROWFUNCTION)
		if lhs == nil || isArrowFunctionToken {
			if result, final := p.parseArrowFunctionCandidate(ctx, &sp, location, isArrowFunctionToken, wasOpenParen, usedVariablesSize); final {
				return result
			}
		}
	}

	if lhs == nil && !maybeAssignmentPattern {
		p.propagateError()
	}

	if maybeAssignmentPattern && (lhs == nil || (p.match(EQUAL) && ctx.isObjectOrArrayLiteral(lhs))) {
		return p.parseDestructuringAssignment(ctx, &sp, location, lhs == nil)
	}

	p.failIfFalse(lhs != nil, "Cannot parse expression")
	if initialNonLHSCount != p.parserState.nonLHSCount {
		if p.token.Type >= EQUAL && p.token.Type <= ANDEQUAL {
			p.semanticFail("Left hand side of operator '", p.getToken(), "' must be a reference")
		}
		return lhs
	}

	assignmentStack := 0
	hadAssignment := false
loop:
	for {
		var op Operator
		switch p.token.Type {
		case EQUAL:
			op = OpEqual
		case PLUSEQUAL:
			op = OpPlusEq
		case MINUSEQUAL:
			op = OpMinusEq
		case MULTEQUAL:
			op = OpMultEq
		case DIVEQUAL:
			op = OpDivEq
		case LSHIFTEQUAL:
			op = OpLShift
		case RSHIFTEQUAL:
			op = OpRShift
		case URSHIFTEQUAL:
			op = OpURShift
		case BITANDEQUAL:
			op = OpBitAndEq
		case BITXOREQUAL:
			op = OpBitXOrEq
		case BITOREQUAL:
			op = OpBitOrEq
		case MODEQUAL:
			op = OpModEq
		case POWEQUAL:
			op = OpPowEq
		case COALESCEEQUAL:
			op = OpCoalesceEq
		case OREQUAL:
			op = OpOrEq
		case ANDEQUAL:
			op = OpAndEq
		default:
			break loop
		}
		p.parserState.nonTrivialExpressionCount++
		hadAssignment = true
		if ctx.isMetaProperty(lhs) {
			p.semanticFail(p.metaPropertyName(ctx, lhs), " can't be the left hand side of an assignment expression")
		}
		// Logical assignments to non-simple targets are early errors even in sloppy mode.
		p.semanticFailIfFalse(p.isSimpleAssignmentTarget(ctx, lhs, op == OpCoalesceEq || op == OpOrEq || op == OpAndEq), "Left side of assignment is not a reference")
		assignmentStack = ctx.assignmentStackAppend(assignmentStack, lhs, start, p.tokenStartPosition(), p.parserState.assignmentCount, op)
		start = p.tokenStartPosition()
		p.parserState.assignmentCount++
		p.next(ctx.dontBuildStrings())
		if p.strictMode() && !p.parserState.lastIdentifier.IsNull() && ctx.isResolve(lhs) {
			p.failIfTrueIfStrict(p.parserState.lastIdentifier == ident.Eval, "Cannot modify 'eval' in strict mode")
			p.failIfTrueIfStrict(p.parserState.lastIdentifier == ident.Arguments, "Cannot modify 'arguments' in strict mode")
			p.parserState.lastIdentifier = ident.Null
		}
		lhs = p.parseAssignmentExpression(ctx)
		p.failIfFalse(lhs != nil, "Cannot parse the right hand side of an assignment expression")
		if initialNonLHSCount != p.parserState.nonLHSCount {
			if p.token.Type >= EQUAL && p.token.Type <= ANDEQUAL {
				p.semanticFail("Left hand side of operator '", p.getToken(), "' must be a reference")
			}
			break
		}
	}
	if hadAssignment {
		p.parserState.nonLHSCount++
	}
	for assignmentStack != 0 {
		lhs, assignmentStack = ctx.createAssignment(location, assignmentStack, lhs, initialAssignmentCount, p.parserState.assignmentCount, p.lastTokenEndPosition())
	}
	return lhs
}

func (p *Parser) parseYieldExpression(ctx treeBuilder) TreeExpression {
	// YieldExpression[In] :
	//     yield
	//     yield [no LineTerminator here] AssignmentExpression[?In, Yield]
	//     yield [no LineTerminator here] * AssignmentExpression[?In, Yield]
	p.failIfFalse(p.currentScope.isGeneratorFunction && !p.currentScope.isArrowFunctionBoundary, "Cannot use yield expression out of generator")
	p.failIfTrue(p.parserState.functionParsePhase == functionParsePhaseParameters, "Cannot use yield expression within parameters")
	// https://github.com/tc39/ecma262/issues/3333
	p.failIfTrue(p.parserState.isParsingClassFieldInitializer, "Cannot use yield expression inside class field initializer expression")

	location := p.tokenLocation()
	divotStart := p.tokenStartPosition()
	sp := p.createSavePoint(ctx)
	p.next(0)
	if p.lexer.HasLineTerminatorBeforeToken() {
		return ctx.createYield(location, nil, false, 0, 0, 0)
	}

	delegate := p.consume(TIMES, 0)
	argumentStart := p.tokenStartPosition()
	argument, _ := tryParse(p, func() TreeExpression { return p.parseAssignmentExpression(ctx) })
	if argument == nil {
		p.restoreSavePoint(ctx, sp)
		p.next(0)
		return ctx.createYield(location, nil, false, 0, 0, 0)
	}
	return ctx.createYield(location, argument, delegate, divotStart, argumentStart, p.lastTokenEndPosition())
}

func (p *Parser) parseAwaitExpression(ctx treeBuilder) TreeExpression {
	location := p.tokenLocation()
	divotStart := p.tokenStartPosition()
	p.next(0)
	argumentStart := p.tokenStartPosition()
	argument := p.parseUnaryExpression(ctx)
	p.failIfFalse(argument != nil, "Failed to parse await expression")
	p.currentFunctionScope().usesAwait = true
	return ctx.createAwait(location, argument, divotStart, argumentStart, p.lastTokenEndPosition())
}

func (p *Parser) parseConditionalExpression(ctx treeBuilder) TreeExpression {
	location := p.tokenLocation()
	cond := p.parseBinaryExpression(ctx)
	p.failIfFalse(cond != nil, "Cannot parse expression")
	if !p.match(QUESTION) {
		return cond
	}
	p.parserState.nonTrivialExpressionCount++
	p.parserState.nonLHSCount++
	p.next(ctx.dontBuildStrings())
	var lhs TreeExpression
	func() {
		// `in` is allowed in the middle operand but not beyond it.
		defer setForScope(&p.allowsIn, true)()
		lhs = p.parseAssignmentExpression(ctx)
	}()
	p.failIfFalse(lhs != nil, "Cannot parse left hand side of ternary operator")
	ctx.setEndOffset(lhs, p.lastTokenLocation.EndOffset)
	p.consumeOrFailWithFlags(COLON, ctx.dontBuildStrings(), "Expected ':' in ternary operator")

	rhs := p.parseAssignmentExpression(ctx)
	p.failIfFalse(rhs != nil, "Cannot parse right hand side of ternary operator")
	ctx.setEndOffset(rhs, p.lastTokenLocation.EndOffset)
	return ctx.createConditionalExpr(location, cond, lhs, rhs)
}

func isUnaryOpExcludingUpdateOp(t TokenType) bool {
	return !t.IsUpdateOp() && t.IsUnaryOp()
}

// isBinaryOperator returns the token's precedence (shifted), or 0.
func (p *Parser) isBinaryOperator(t TokenType) int {
	if p.allowsIn {
		return int(t & (BinaryOpTokenPrecedenceMask << BinaryOpTokenAllowsInPrecedenceAdditionalShift))
	}
	return int(t & BinaryOpTokenPrecedenceMask)
}

func (p *Parser) parseBinaryExpression(ctx treeBuilder) TreeExpression {
	operandStackDepth := 0
	operatorStackDepth := 0
	defer ctx.popBinaryExprContext(ctx.pushBinaryExprContext())
	location := p.tokenLocation()
	hasLogicalOperator := false
	hasCoalesceOperator := false

	var previousOperator TokenType
	for {
		exprStart := p.tokenStartPosition()
		initialAssignments := p.parserState.assignmentCount
		leadingTokenTypeForUnaryExpression := p.token.Type

		var current TreeExpression
		if p.match(PRIVATENAME) {
			name := p.token.Data.Ident
			p.currentScope.usePrivateName(name)
			p.seenPrivateNameUseInNonReparsingFunctionMode = true
			p.next(0)
			p.semanticFailIfTrue(p.token.Type != INTOKEN || previousOperator >= INTOKEN, "Bare private name can only be used as the left-hand side of an `in` expression")
			current = ctx.createPrivateIdentifierNode(location, name)
		} else {
			current = p.parseUnaryExpression(ctx)
		}
		p.failIfFalse(current != nil, "Cannot parse expression")

		operandStackDepth = ctx.appendBinaryExpressionInfo(operandStackDepth, current, exprStart, p.lastTokenEndPosition(), p.lastTokenEndPosition(), initialAssignments != p.parserState.assignmentCount)
		precedence := p.isBinaryOperator(p.token.Type)
		if precedence == 0 {
			break
		}

		// The left side of ** is an UpdateExpression, so `-x ** y` is an early
		// error (ambiguous between -(x**y) and (-x)**y). Checking the token that
		// started the operand suffices because ** has the highest precedence.
		p.failIfTrue(p.match(POW) && isUnaryOpExcludingUpdateOp(leadingTokenTypeForUnaryExpression), "Ambiguous unary expression in the left hand side of the exponentiation expression; parentheses must be used to disambiguate the expression")

		// Mixing ?? with || or && is an early error; ?? has the lowest
		// precedence, so it suffices to check whether both ever appear.
		if p.match(AND) || p.match(OR) {
			hasLogicalOperator = true
		} else if p.match(COALESCE) {
			hasCoalesceOperator = true
		}
		p.failIfTrue(hasLogicalOperator && hasCoalesceOperator, "Coalescing and logical operators used together in the same expression; parentheses must be used to disambiguate")

		p.parserState.nonTrivialExpressionCount++
		p.parserState.nonLHSCount++
		operatorToken := p.token.Type
		p.next(ctx.dontBuildStrings())

		for operatorStackDepth != 0 && ctx.operatorStackShouldReduce(precedence) {
			rhs := ctx.getFromOperandStack(-1)
			lhs := ctx.getFromOperandStack(-2)
			operandStackDepth = ctx.shrinkOperandStackBy(operandStackDepth, 2)
			operandStackDepth = ctx.appendBinaryOperation(location, operandStackDepth, lhs, rhs)
			operatorStackDepth = ctx.operatorStackPop(operatorStackDepth)
		}
		operatorStackDepth = ctx.operatorStackAppend(operatorStackDepth, operatorToken, precedence)
		previousOperator = operatorToken
	}
	for operatorStackDepth != 0 {
		rhs := ctx.getFromOperandStack(-1)
		lhs := ctx.getFromOperandStack(-2)
		operandStackDepth = ctx.shrinkOperandStackBy(operandStackDepth, 2)
		operandStackDepth = ctx.appendBinaryOperation(location, operandStackDepth, lhs, rhs)
		operatorStackDepth = ctx.operatorStackPop(operatorStackDepth)
	}
	return ctx.popOperandStack()
}

func (p *Parser) parseProperty(ctx treeBuilder) TreeProperty {
	parseMode := MethodMode
	wasIdent := false
	timesPosition, asyncPosition := -1, -1
	functionStartOr := func(def int) int {
		if timesPosition >= 0 {
			return timesPosition
		}
		if asyncPosition >= 0 {
			return asyncPosition
		}
		return def
	}

	if p.match(TIMES) {
		timesPosition = int(p.token.Start)
		p.next(0)
		parseMode = GeneratorWrapperMethodMode
	}

	for {
		t := p.token.Type
		namedProperty := false
		switch {
		case t == ESCAPED_KEYWORD || t == IDENT:
			if p.token.Data.Ident == ident.Async && !p.token.Data.Escaped && parseMode == MethodMode {
				asyncPosition = int(p.token.Start)
				sp := p.createSavePoint(ctx)
				p.next(0)
				if p.match(COLON) || p.match(OPENPAREN) || p.match(COMMA) || p.match(CLOSEBRACE) {
					p.restoreSavePoint(ctx, sp)
					wasIdent = true
					namedProperty = true
					break
				}
				p.failIfTrue(p.lexer.HasLineTerminatorBeforeToken(), "Expected a property name following keyword 'async'")
				if p.consume(TIMES, 0) {
					parseMode = AsyncGeneratorWrapperMethodMode
				} else {
					parseMode = AsyncMethodMode
				}
				continue
			}
			if p.token.Data.Ident == ident.Async && !p.token.Data.Escaped {
				asyncPosition = int(p.token.Start)
			}
			wasIdent = true
			namedProperty = true
		case t == YIELD || t == AWAIT:
			wasIdent = true
			namedProperty = true
		case t == STRING:
			namedProperty = true
		case t == DOUBLE || t == INTEGER:
			functionStart := functionStartOr(int(p.token.Start))
			name := ident.FromNumber(p.token.Data.Double)
			p.next(0)
			if p.match(OPENPAREN) {
				method := p.parseMethodWithMode(ctx, parseMode, name, functionStart)
				return ctx.createProperty(name, method, PropertyConstant, SuperBindingNeeded, InferNameAllowed, ClassElementTagNo)
			}
			p.failIfTrue(parseMode != MethodMode, "Expected a parenthesis for argument list")
			p.consumeOrFail(COLON, "Expected ':' after property name")
			node := p.parseAssignmentExpression(ctx)
			p.failIfFalse(node != nil, "Cannot parse expression for property declaration")
			ctx.setEndOffset(node, p.lexer.CurrentOffset())
			return ctx.createProperty(name, node, PropertyConstant, SuperBindingNotNeeded, InferNameAllowed, ClassElementTagNo)
		case t == BIGINT:
			name, ok := bigIntToDecimal(p.token.Data.BigIntString, p.token.Data.Radix)
			p.failIfFalse(ok, "Cannot parse big int property name")
			functionStart := functionStartOr(int(p.token.Start))
			p.next(0)
			if p.match(OPENPAREN) {
				method := p.parseMethodWithMode(ctx, parseMode, name, functionStart)
				return ctx.createProperty(name, method, PropertyConstant, SuperBindingNeeded, InferNameAllowed, ClassElementTagNo)
			}
			p.failIfTrue(parseMode != MethodMode, "Expected a parenthesis for argument list")
			p.consumeOrFail(COLON, "Expected ':' after property name")
			node := p.parseAssignmentExpression(ctx)
			p.failIfFalse(node != nil, "Cannot parse expression for property declaration")
			ctx.setEndOffset(node, p.lexer.CurrentOffset())
			return ctx.createProperty(name, node, PropertyConstant, SuperBindingNotNeeded, InferNameAllowed, ClassElementTagNo)
		case t == OPENBRACKET:
			functionStart := functionStartOr(int(p.token.Start))
			p.next(0)
			propertyName := p.parseAssignmentExpression(ctx)
			p.failIfFalse(propertyName != nil, "Cannot parse computed property name")
			p.handleProductionOrFail(CLOSEBRACKET, "]", "end", "computed property name")
			if p.match(OPENPAREN) {
				method := p.parseMethodWithMode(ctx, parseMode, ident.Null, functionStart)
				return ctx.createComputedProperty(propertyName, method, PropertyConstant|PropertyComputed, SuperBindingNeeded, ClassElementTagNo)
			}
			p.failIfTrue(parseMode != MethodMode, "Expected a parenthesis for argument list")
			p.consumeOrFail(COLON, "Expected ':' after property name")
			node := p.parseAssignmentExpression(ctx)
			p.failIfFalse(node != nil, "Cannot parse expression for property declaration")
			ctx.setEndOffset(node, p.lexer.CurrentOffset())
			return ctx.createComputedProperty(propertyName, node, PropertyConstant|PropertyComputed, SuperBindingNotNeeded, ClassElementTagNo)
		case t == DOTDOTDOT:
			spreadLocation := p.token.Location()
			start := p.token.Start
			divot := p.token.End
			p.next(0)
			elem := p.parseAssignmentExpression(ctx)
			p.failIfFalse(elem != nil, "Cannot parse subject of a spread operation")
			node := ctx.createObjectSpreadExpression(spreadLocation, elem, start, divot, p.lastTokenEndPosition())
			return ctx.createSpreadProperty(node, PropertySpread, SuperBindingNotNeeded, ClassElementTagNo)
		default:
			p.failIfFalse(t&KeywordTokenFlag != 0, "Expected a property name")
			wasIdent = true // Treat keyword token as an identifier
			namedProperty = true
		}
		if namedProperty {
			return p.parseNamedProperty(ctx, parseMode, wasIdent, functionStartOr)
		}
	}
}

func (p *Parser) parseMethodWithMode(ctx treeBuilder, mode SourceParseMode, name ident.Identifier, functionStart int) TreeExpression {
	defer setForScope(&p.parseMode, mode)()
	return p.parsePropertyMethod(ctx, name, functionStart)
}

// parseNamedProperty is the namedProperty: label of parseProperty.
func (p *Parser) parseNamedProperty(ctx treeBuilder, parseMode SourceParseMode, wasIdent bool, functionStartOr func(int) int) TreeProperty {
	name := p.token.Data.Ident
	wasUnescapedIdent := wasIdent && !p.token.Data.Escaped
	getterOrSetterStartOffset := p.tokenStart()
	functionStart := functionStartOr(int(p.token.Start))
	identToken := p.token

	if wasUnescapedIdent && !parseMode.IsGeneratorMethod() && (name == ident.Get || name == ident.Set) {
		p.next(LexerIgnoreReservedWords)
	} else {
		p.next(ctx.dontBuildKeywords() | LexerIgnoreReservedWords)
	}

	if !parseMode.IsGeneratorMethod() && !parseMode.IsAsyncMethod() && p.match(COLON) {
		p.next(0)
		node := p.parseAssignmentExpression(ctx)
		p.failIfFalse(node != nil, "Cannot parse expression for property declaration")
		ctx.setEndOffset(node, p.lexer.CurrentOffset())
		inferName := InferNameAllowed
		if name == ident.UnderscoreProto {
			inferName = InferNameDisallowed
		}
		return ctx.createProperty(name, node, PropertyConstant, SuperBindingNotNeeded, inferName, ClassElementTagNo)
	}

	if p.match(OPENPAREN) {
		method := p.parseMethodWithMode(ctx, parseMode, name, functionStart)
		return ctx.createProperty(name, method, PropertyConstant, SuperBindingNeeded, InferNameAllowed, ClassElementTagNo)
	}
	p.failIfTrue(parseMode != MethodMode, "Expected a parenthesis for argument list")
	p.failIfFalse(wasIdent, "Expected an identifier as property name")

	if p.match(COMMA) || p.match(CLOSEBRACE) {
		p.semanticFailureDueToKeywordCheckingToken(&identToken, "shorthand property name")
		start := p.tokenStartPosition()
		location := p.tokenLocation()
		p.currentScope.useVariable(name, name == ident.Eval)
		node := ctx.createResolve(location, name, start, p.lastTokenEndPosition(), true)
		return ctx.createProperty(name, node, PropertyConstant|PropertyShorthand, SuperBindingNotNeeded, InferNameAllowed, ClassElementTagNo)
	}

	var t PropertyType
	if wasUnescapedIdent {
		switch name {
		case ident.Get:
			t = PropertyGetter
		case ident.Set:
			t = PropertySetter
		}
	}
	p.failIfFalse(t != 0, "Expected a ':' following the property name '", name, "'")
	return p.parseGetterSetter(ctx, t, getterOrSetterStartOffset, ConstructorKindNone, ClassElementTagNo)
}

func (p *Parser) parseObjectLiteral(ctx treeBuilder) TreeExpression {
	location := p.tokenLocation()
	p.consumeOrFail(OPENBRACE, "Expected opening '{' at the start of an object literal")

	defer saveForScope(&p.parserState.nonLHSCount)()
	if p.consume(CLOSEBRACE, 0) {
		return ctx.createObjectLiteral(location, nil)
	}

	property := p.parseProperty(ctx)
	p.failIfFalse(property != nil, "Cannot parse object literal property")

	seenProtoSetter := ctx.isUnderscoreProtoSetter(property)

	propertyList := ctx.createPropertyList(location, property, nil)
	tail := propertyList
	for p.consume(COMMA, 0) {
		if p.match(CLOSEBRACE) {
			break
		}
		propertyLocation := p.tokenLocation()
		property = p.parseProperty(ctx)
		p.failIfFalse(property != nil, "Cannot parse object literal property")
		if ctx.isUnderscoreProtoSetter(property) {
			// https://tc39.es/ecma262/#sec-__proto__-property-names-in-object-initializers
			p.semanticFailIfTrue(seenProtoSetter, "Attempted to redefine __proto__ property")
			seenProtoSetter = true
		}
		tail = ctx.createPropertyList(propertyLocation, property, tail)
	}

	p.handleProductionOrFail2(CLOSEBRACE, "}", "end", "object literal")
	return ctx.createObjectLiteral(location, propertyList)
}

func (p *Parser) parseArrayLiteral(ctx treeBuilder) TreeExpression {
	location := p.tokenLocation()
	p.consumeOrFailWithFlags(OPENBRACKET, ctx.dontBuildStrings(), "Expected an opening '[' at the beginning of an array literal")

	defer saveForScope(&p.parserState.nonLHSCount)()

	elisions := 0
	for p.match(COMMA) {
		p.next(ctx.dontBuildStrings())
		elisions++
	}
	if p.consume(CLOSEBRACKET, 0) {
		return ctx.createArray(location, elisions, nil)
	}

	parseElement := func() TreeExpression {
		if p.match(DOTDOTDOT) {
			spreadLocation := p.token.Location()
			start := p.token.Start
			divot := p.token.End
			p.next(0)
			spreadExpr := p.parseAssignmentExpression(ctx)
			p.failIfFalse(spreadExpr != nil, "Cannot parse subject of a spread operation")
			return ctx.createSpreadExpression(spreadLocation, spreadExpr, start, divot, p.lastTokenEndPosition())
		}
		return nil
	}

	elem := parseElement()
	if elem == nil {
		elem = p.parseAssignmentExpression(ctx)
	}
	p.failIfFalse(elem != nil, "Cannot parse array literal element")
	elementList := ctx.createElementList(nil, elisions, elem)
	tail := elementList
	for p.match(COMMA) {
		p.next(ctx.dontBuildStrings())
		elisions = 0
		for p.consume(COMMA, 0) {
			elisions++
		}
		if p.consume(CLOSEBRACKET, 0) {
			return ctx.createArray(location, elisions, elementList)
		}
		if spread := parseElement(); spread != nil {
			tail = ctx.createElementList(tail, elisions, spread)
			continue
		}
		elem := p.parseAssignmentExpression(ctx)
		p.failIfFalse(elem != nil, "Cannot parse array literal element")
		tail = ctx.createElementList(tail, elisions, elem)
	}

	if !p.consume(CLOSEBRACKET, 0) {
		p.failIfFalse(p.match(DOTDOTDOT), "Expected either a closing ']' or a ',' following an array element")
		p.semanticFail("The '...' operator should come before a target expression")
	}
	return ctx.createArray(location, 0, elementList)
}

func (p *Parser) parseTemplateString(ctx treeBuilder, isTemplateHead bool, rawMode RawStringsBuildMode, elementIsTail *bool) TreeExpression {
	if !isTemplateHead {
		p.matchOrFail(CLOSEBRACE, "Expected a closing '}' following an expression in template literal")
	}
	// Re-scan the token as a template element.
	p.token.Type = p.lexer.ScanTemplateString(&p.token, rawMode)
	p.matchOrFail(TEMPLATE, "Expected an template element")
	cooked := p.token.Data.Cooked
	raw := p.token.Data.Raw
	*elementIsTail = p.token.Data.IsTail
	location := p.tokenLocation()
	p.next(0)
	return ctx.createTemplateString(location, cooked, raw)
}

func (p *Parser) parseTemplateLiteral(ctx treeBuilder, rawMode RawStringsBuildMode) TreeExpression {
	defer saveForScope(&p.parserState.nonLHSCount)()
	location := p.tokenLocation()
	elementIsTail := false

	head := p.parseTemplateString(ctx, true, rawMode, &elementIsTail)
	p.failIfFalse(head != nil, "Cannot parse head template element")

	stringList := ctx.createTemplateStringList(nil, head)
	stringTail := stringList

	if elementIsTail {
		return ctx.createTemplateLiteral(location, stringList, nil)
	}

	p.failIfTrue(p.match(CLOSEBRACE), "Template literal expression cannot be empty")
	e := p.parseExpression(ctx)
	p.failIfFalse(e != nil, "Cannot parse expression in template literal")

	expressionList := ctx.createTemplateExpressionList(nil, e)
	expressionTail := expressionList

	s := p.parseTemplateString(ctx, false, rawMode, &elementIsTail)
	p.failIfFalse(s != nil, "Cannot parse template element")
	stringTail = ctx.createTemplateStringList(stringTail, s)

	for !elementIsTail {
		p.failIfTrue(p.match(CLOSEBRACE), "Template literal expression cannot be empty")
		e := p.parseExpression(ctx)
		p.failIfFalse(e != nil, "Cannot parse expression in template literal")
		expressionTail = ctx.createTemplateExpressionList(expressionTail, e)

		s := p.parseTemplateString(ctx, false, rawMode, &elementIsTail)
		p.failIfFalse(s != nil, "Cannot parse template element")
		stringTail = ctx.createTemplateStringList(stringTail, s)
	}
	return ctx.createTemplateLiteral(location, stringList, expressionList)
}

func (p *Parser) createResolveAndUseVariable(ctx treeBuilder, name ident.Identifier, isEval bool, start TextPosition, location TokenLocation) TreeExpression {
	p.currentScope.useVariable(name, isEval)
	p.parserState.lastIdentifier = name
	return ctx.createResolve(location, name, start, p.lastTokenEndPosition(), true)
}

// tryParseArgumentsDotLengthForFastPath recognizes `arguments.length`, which
// can be read from the call frame without materializing the arguments
// object. It is excluded from ArgumentsFeature except when it is modified or
// `arguments` is declared as a function-level variable.
func (p *Parser) tryParseArgumentsDotLengthForFastPath(ctx treeBuilder) TreeExpression {
	if ctx.hasArgumentsFeature() || !p.match(IDENT) || !p.isArgumentsIdentifier() {
		return nil
	}
	// Leave semantic errors to parsePrimaryExpression; these checks mirror its IDENT case.
	if p.currentScope.isStaticBlock || p.parserState.isParsingClassFieldInitializer || p.closestScopeOwningArguments().evalContextType == EvalContextTypeInstanceFieldEvalContext {
		return nil
	}

	argumentsSavePoint := p.createSavePoint(ctx)
	primaryStart := p.tokenStartPosition()
	primaryLocation := p.tokenLocation()
	p.next(0)
	if p.match(DOT) {
		argumentsDotSavePoint := p.createSavePoint(ctx)
		p.next(0)
		if p.match(IDENT) && p.token.Data.Ident == ident.Length {
			p.seenArgumentsDotLength = true
			p.currentScope.useVariable(ident.Arguments, false)
			p.parserState.lastIdentifier = ident.Arguments
			e := ctx.createResolve(primaryLocation, ident.Arguments, primaryStart, p.lastTokenEndPosition(), false)
			p.restoreSavePoint(ctx, argumentsDotSavePoint)
			return e
		}
	}
	p.restoreSavePoint(ctx, argumentsSavePoint)
	return nil
}

func (p *Parser) parsePrimaryExpression(ctx treeBuilder) TreeExpression {
	p.enterRecursion()
	defer p.exitRecursion()
	switch p.token.Type {
	case FUNCTION:
		return p.parseFunctionExpression(ctx)
	case CLASSTOKEN:
		return p.parseClassExpression(ctx)
	case OPENBRACE:
		return p.parseObjectLiteral(ctx)
	case OPENBRACKET:
		return p.parseArrayLiteral(ctx)
	case OPENPAREN:
		p.next(0)
		defer saveForScope(&p.parserState.nonLHSCount)()
		result := p.parseExpression(ctx)
		p.handleProductionOrFail(CLOSEPAREN, ")", "end", "compound expression")
		if result != nil {
			ctx.setIsParenthesized(result)
		}
		return result
	case THISTOKEN:
		location := p.tokenLocation()
		p.next(0)
		if p.currentScope.isArrowFunction {
			p.currentScope.setInnerArrowFunctionUsesThis()
		}
		return ctx.createThisExpr(location)
	case AWAIT:
		p.semanticFailIfTrue(p.currentScope.isStaticBlock, "The 'await' keyword is disallowed in the IdentifierReference position within static block")
		if p.parserState.functionParsePhase == functionParsePhaseParameters {
			p.semanticFailIfFalse(p.parserState.allowAwait, "Cannot use 'await' within a parameter default expression")
		} else if !p.parserState.classFieldInitMasksAsync && (p.currentFunctionScope().isAsyncFunctionBoundary || p.parseMode.IsModule()) {
			return p.parseAwaitExpression(ctx)
		}
		return p.parseIdentifierExpression(ctx)
	case IDENT:
		p.semanticFailIfTrue(p.currentScope.isStaticBlock && p.isArgumentsIdentifier(), "Cannot use 'arguments' as an identifier in static block")
		if p.token.Data.Ident == ident.Async && !p.token.Data.Escaped {
			functionStart := p.tokenStartPosition()
			name := p.token.Data.Ident
			location := p.tokenLocation()
			p.next(0)
			if p.match(FUNCTION) && !p.lexer.HasLineTerminatorBeforeToken() {
				return p.parseAsyncFunctionExpression(ctx, location)
			}
			// Avoid using the variable if it is an arrow function parameter.
			if p.match(ARROWFUNCTION) {
				return nil
			}
			return p.createResolveAndUseVariable(ctx, name, false, functionStart, location)
		}
		if p.parserState.isParsingClassFieldInitializer {
			p.failIfTrue(p.isArgumentsIdentifier(), "Cannot reference 'arguments' in class field initializer")
		}
		return p.parseIdentifierExpression(ctx)
	case BIGINT:
		value := p.token.Data.BigIntString
		radix := p.token.Data.Radix
		location := p.tokenLocation()
		p.next(0)
		return ctx.createBigInt(location, value, radix)
	case STRING:
		value := p.token.Data.Ident
		location := p.tokenLocation()
		p.next(0)
		return ctx.createString(location, value)
	case DOUBLE:
		d := p.token.Data.Double
		location := p.tokenLocation()
		p.next(0)
		return ctx.createDoubleExpr(location, d)
	case INTEGER:
		d := p.token.Data.Double
		location := p.tokenLocation()
		p.next(0)
		return ctx.createIntegerExpr(location, d)
	case NULLTOKEN:
		location := p.tokenLocation()
		p.next(0)
		return ctx.createNull(location)
	case TRUETOKEN:
		location := p.tokenLocation()
		p.next(0)
		return ctx.createBoolean(location, true)
	case FALSETOKEN:
		location := p.tokenLocation()
		p.next(0)
		return ctx.createBoolean(location, false)
	case DIVEQUAL, DIVIDE:
		// A regular expression literal.
		if p.match(DIVEQUAL) {
			p.token.Type = p.lexer.ScanRegExp(&p.token, '=')
		} else {
			p.token.Type = p.lexer.ScanRegExp(&p.token, 0)
		}
		p.matchOrFail(REGEXP, "Invalid regular expression")

		pattern := p.token.Data.Pattern
		flags := p.token.Data.Flags
		start := p.tokenStartPosition()
		location := p.tokenLocation()
		p.next(0)
		re := ctx.createRegExp(location, pattern, flags, start, p.lexer.IsReparsingFunction())
		if re == nil {
			p.regexFail(RegExpSyntaxChecker(pattern.String(), flags.String()))
		}
		return re
	case BACKQUOTE:
		return p.parseTemplateLiteral(ctx, DontBuildRawStrings)
	case YIELD:
		if p.canUseIdentifierYield() {
			return p.parseIdentifierExpression(ctx)
		}
		p.failDueToUnexpectedToken()
	case LET:
		if !p.strictMode() {
			return p.parseIdentifierExpression(ctx)
		}
		p.failDueToUnexpectedToken()
	case ESCAPED_KEYWORD:
		if p.matchAllowedEscapedContextualKeyword() {
			return p.parseIdentifierExpression(ctx)
		}
	}
	p.failDueToUnexpectedToken()
	return nil
}

// parseIdentifierExpression is the identifierExpression: label of parsePrimaryExpression.
func (p *Parser) parseIdentifierExpression(ctx treeBuilder) TreeExpression {
	start := p.tokenStartPosition()
	name := p.token.Data.Ident
	if name == ident.Arguments {
		p.failIfTrue(p.closestScopeOwningArguments().evalContextType == EvalContextTypeInstanceFieldEvalContext, "arguments is not valid in this context")
	}
	location := p.tokenLocation()
	p.next(0)

	// Avoid using the variable if it is an arrow function parameter.
	if p.match(ARROWFUNCTION) {
		return nil
	}
	return p.createResolveAndUseVariable(ctx, name, name == ident.Eval, start, location)
}

func (p *Parser) parseArguments(ctx treeBuilder) TreeArguments {
	p.consumeOrFailWithFlags(OPENPAREN, ctx.dontBuildStrings(), "Expected opening '(' at start of argument list")
	location := p.tokenLocation()
	if p.match(CLOSEPAREN) {
		p.next(0)
		return ctx.createArguments(nil, false)
	}
	argumentsStart := p.token.Start
	argumentsDivot := p.token.End

	initialAssignments := p.parserState.assignmentCount
	argType := argumentTypeNormal
	firstArg := p.parseArgument(ctx, &argType)
	p.failIfFalse(firstArg != nil, "Cannot parse function argument")
	p.semanticFailIfTrue(p.match(DOTDOTDOT), "The '...' operator should come before the target expression")

	hasSpread := argType == argumentTypeSpread
	argList := ctx.createArgumentsList(location, nil, firstArg)
	tail := argList

	for p.match(COMMA) {
		argumentLocation := p.tokenLocation()
		p.next(ctx.dontBuildStrings())
		if p.match(CLOSEPAREN) {
			break
		}
		arg := p.parseArgument(ctx, &argType)
		p.propagateError()
		p.semanticFailIfTrue(p.match(DOTDOTDOT), "The '...' operator should come before the target expression")
		if argType == argumentTypeSpread {
			hasSpread = true
		}
		tail = ctx.createArgumentsList(argumentLocation, tail, arg)
	}

	p.handleProductionOrFail2(CLOSEPAREN, ")", "end", "argument list")
	if hasSpread {
		spreadArray := ctx.createSpreadExpression(location, ctx.createArray(location, 0, ctx.createElementListFromArguments(argList)), argumentsStart, argumentsDivot, p.lastTokenEndPosition())
		return ctx.createArguments(ctx.createArgumentsList(location, nil, spreadArray), initialAssignments != p.parserState.assignmentCount)
	}
	return ctx.createArguments(argList, initialAssignments != p.parserState.assignmentCount)
}

func (p *Parser) parseArgument(ctx treeBuilder, t *argumentType) TreeExpression {
	if p.match(DOTDOTDOT) {
		spreadLocation := p.tokenLocation()
		start := p.token.Start
		divot := p.token.End
		p.next(0)
		spreadExpr := p.parseAssignmentExpression(ctx)
		p.propagateError()
		end := p.lastTokenEndPosition()
		*t = argumentTypeSpread
		return ctx.createSpreadExpression(spreadLocation, spreadExpr, start, divot, end)
	}
	*t = argumentTypeNormal
	return p.parseAssignmentExpression(ctx)
}

// enterCallOrApplyDepthScope tracks nesting of f.call(...)/f.apply(...) so the
// bytecode generator can bound how deeply it specializes them.
func (p *Parser) enterCallOrApplyDepthScope() *callOrApplyDepthScope {
	s := &callOrApplyDepthScope{parent: p.callOrApplyDepthScope}
	if s.parent != nil {
		s.depth = s.parent.depth + 1
	}
	s.depthOfInnermostChild = s.depth
	p.callOrApplyDepthScope = s
	return s
}

func (p *Parser) exitCallOrApplyDepthScope(s *callOrApplyDepthScope) {
	if s.parent != nil {
		s.parent.depthOfInnermostChild = max(s.depthOfInnermostChild, s.parent.depthOfInnermostChild)
	}
	p.callOrApplyDepthScope = s.parent
}

func (s *callOrApplyDepthScope) distanceToInnermostChild() int {
	return s.depthOfInnermostChild - s.depth
}

func (p *Parser) parseMemberExpression(ctx treeBuilder) TreeExpression {
	var base TreeExpression
	expressionStart := p.tokenStartPosition()
	location := p.tokenLocation()

	var newTokenStartPositions []TextPosition
	for p.match(NEW) {
		newTokenStartPositions = append(newTokenStartPositions, p.tokenStartPosition())
		p.next(0)
	}
	newCount := len(newTokenStartPositions)

	baseIsSuper := p.match(SUPER)
	previousBaseWasSuper := false
	baseIsImport := p.match(IMPORT)
	baseIsAsyncKeyword := false

	switch {
	case newCount != 0 && p.consume(DOT, 0):
		if p.matchContextualKeyword(ident.Target) {
			closestOrdinaryFunctionScope := p.closestParentOrdinaryFunctionNonLexicalScope()
			isClassFieldInitializer := p.parserState.isParsingClassFieldInitializer
			evalType := closestOrdinaryFunctionScope.evalContextType
			isFunctionEvalContextType := p.isInsideOrdinaryFunction && (evalType == EvalContextTypeFunctionEvalContext || evalType == EvalContextTypeInstanceFieldEvalContext)
			p.semanticFailIfFalse(p.currentScope.isFunction || p.currentScope.isStaticBlock || isFunctionEvalContextType || isClassFieldInitializer, "new.target is only valid inside functions or static blocks")
			if p.currentScope.isArrowFunction {
				p.semanticFailIfFalse(!closestOrdinaryFunctionScope.isGlobalCode || isFunctionEvalContextType || isClassFieldInitializer, "new.target is not valid inside arrow functions in global code")
				p.currentScope.setInnerArrowFunctionUsesNewTarget()
			}
			base = ctx.createNewTargetExpr(location)
			newCount--
			p.next(0)
		} else {
			p.failIfTrue(p.match(IDENT), "\"new.\" can only be followed with target")
			p.failDueToUnexpectedToken()
		}

	case baseIsSuper:
		closestOrdinaryFunctionScope := p.closestParentOrdinaryFunctionNonLexicalScope()
		classScope := p.closestClassScopeOrTopLevelScope()
		// A class scope deeper than the closest ordinary function means a class field initializer.
		isClassFieldInitializer := false
		for s := classScope.containingScope; s != nil; s = s.containingScope {
			if s == closestOrdinaryFunctionScope {
				isClassFieldInitializer = true
				break
			}
		}
		p.semanticFailIfFalse(p.currentScope.isFunction || isClassFieldInitializer || (closestOrdinaryFunctionScope.isEvalContext && closestOrdinaryFunctionScope.expectedSuperBinding == SuperBindingNeeded), "super is not valid in this context")
		base = ctx.createSuperExpr(location)
		p.next(0)
		p.failIfTrue(p.match(OPENPAREN) && p.currentScope.evalContextType == EvalContextTypeInstanceFieldEvalContext, "super call is not valid in this context")
		functionScope := p.currentFunctionScope()
		functionScope.needsSuperBinding = true
		// No need to recheck on reparse; for arrow functions it would even be
		// wrong, as the enclosing constructor or method is unknown then.
		if !p.lexer.IsReparsingFunction() {
			functionSuperBinding := closestOrdinaryFunctionScope.expectedSuperBinding
			if !functionScope.isArrowFunction && !closestOrdinaryFunctionScope.isEvalContext {
				functionSuperBinding = functionScope.expectedSuperBinding
			}
			p.semanticFailIfTrue(functionSuperBinding == SuperBindingNotNeeded && !isClassFieldInitializer, "super is not valid in this context")
		}

	case baseIsImport:
		p.next(0)
		expressionEnd := p.lastTokenEndPosition()
		isImportMeta := false
		deferred := false
		if p.consume(DOT, 0) {
			switch {
			case p.matchContextualKeyword(ident.Meta):
				p.semanticFailIfFalse(p.scriptMode == ScriptModeModule, "import.meta is only valid inside modules")
				base = ctx.createImportMetaExpr(location, p.createResolveAndUseVariable(ctx, ident.MetaPrivateName, false, expressionStart, location))
				p.currentScope.usesImportMeta = true
				isImportMeta = true
				p.next(0)
			case p.options.UseImportDefer && p.matchContextualKeyword(ident.Defer):
				// ImportCall : import . defer ImportCallArguments
				deferred = true
				p.next(0)
				expressionEnd = p.lastTokenEndPosition()
			default:
				msg := "\"import.\" can only be followed with meta"
				if p.options.UseImportDefer {
					msg = "\"import.\" can only be followed with meta or defer"
				}
				p.failIfTrue(p.match(IDENT), msg)
				p.failDueToUnexpectedToken()
			}
		}
		if !isImportMeta {
			p.semanticFailIfTrue(newCount != 0, "Cannot use new with import")
			p.consumeOrFail(OPENPAREN, "import call expects one or two arguments")
			func() {
				defer saveForScope(&p.parserState.nonLHSCount)()
				e := p.parseAssignmentExpression(ctx)
				p.failIfFalse(e != nil, "Cannot parse expression")
				var optionExpression TreeExpression
				if p.consume(COMMA, 0) {
					if !p.match(CLOSEPAREN) {
						optionExpression = p.parseAssignmentExpression(ctx)
						p.failIfFalse(optionExpression != nil, "Cannot parse expression")
						p.consume(COMMA, 0)
					}
				}
				p.consumeOrFail(CLOSEPAREN, "import call expects one or two arguments")
				base = ctx.createImportExpr(location, e, optionExpression, deferred, expressionStart, expressionEnd, p.lastTokenEndPosition())
			}()
		}

	default:
		isAsync := p.matchContextualKeyword(ident.Async)
		if e := p.tryParseArgumentsDotLengthForFastPath(ctx); e != nil {
			base = e
		} else {
			base = p.parsePrimaryExpression(ctx)
		}
		p.failIfFalse(base != nil, "Cannot parse base expression")
		if isAsync && ctx.isResolve(base) && !p.lexer.HasLineTerminatorBeforeToken() {
			baseIsAsyncKeyword = true
			if p.matchSpecIdentifier() {
				p.failDueToUnexpectedToken()
			}
		}
	}

	p.failIfFalse(base != nil, "Cannot parse base expression")

	for {
		var optionalChainBase TreeExpression
		var optionalChainLocation TokenLocation
		isOptionalCall := false
		t := p.token.Type

		if p.match(QUESTIONDOT) {
			p.semanticFailIfTrue(newCount != 0, "Cannot call constructor in an optional chain")
			p.semanticFailIfTrue(baseIsSuper, "Cannot use super as the base of an optional chain")
			optionalChainBase = base
			optionalChainLocation = p.tokenLocation()

			sp := p.createSavePoint(ctx)
			p.next(0)
			if p.match(OPENBRACKET) || p.match(OPENPAREN) || p.match(BACKQUOTE) {
				t = p.token.Type
			} else {
				t = DOT
				p.restoreSavePoint(ctx, sp)
			}
		}

	chain:
		for {
			switch t {
			case OPENBRACKET:
				p.parserState.nonTrivialExpressionCount++
				expressionDivot := p.tokenStartPosition()
				p.next(0)
				func() {
					defer saveForScope(&p.parserState.nonLHSCount)()
					initialAssignments := p.parserState.assignmentCount
					property := p.parseExpression(ctx)
					p.failIfFalse(property != nil, "Cannot parse subscript expression")
					base = ctx.createBracketAccess(location, base, property, initialAssignments != p.parserState.assignmentCount, expressionStart, expressionDivot, p.tokenEndPosition())
					if baseIsSuper && p.currentScope.isArrowFunction {
						p.currentFunctionScope().setInnerArrowFunctionUsesSuperProperty()
					}
					p.handleProductionOrFail(CLOSEBRACKET, "]", "end", "subscript expression")
				}()

			case OPENPAREN:
				if baseIsSuper {
					p.failIfTrue(p.parserState.isParsingClassFieldInitializer, "super call is not valid in class field initializer context")
				}
				p.parserState.nonTrivialExpressionCount++
				func() {
					defer saveForScope(&p.parserState.nonLHSCount)()
					if newCount != 0 {
						newCount--
						p.semanticFailIfTrue(baseIsSuper, "Cannot use new with super call")
						expressionEnd := p.lastTokenEndPosition()
						arguments := p.parseArguments(ctx)
						p.failIfFalse(arguments != nil, "Cannot parse call arguments")
						base = ctx.createNewExpr(location, base, arguments, expressionStart, expressionEnd, p.lastTokenEndPosition())
						return
					}
					usedVariablesSize := p.currentScope.currentUsedVariablesSize()
					expressionEnd := p.lastTokenEndPosition()
					var depthScope *callOrApplyDepthScope
					if ctx.createsAST() {
						if d, ok := base.(*DotAccessorNode); ok && (d.Ident == ident.Call || d.Ident == ident.Apply) {
							depthScope = p.enterCallOrApplyDepthScope()
							defer p.exitCallOrApplyDepthScope(depthScope)
						}
					}

					var arguments TreeArguments
					if baseIsAsyncKeyword {
						arguments, _ = tryParse(p, func() TreeArguments { return p.parseArguments(ctx) })
						if arguments == nil || p.match(ARROWFUNCTION) {
							p.currentScope.revertToPreviousUsedVariables(usedVariablesSize)
							p.failDueToUnexpectedToken()
						}
					} else {
						arguments = p.parseArguments(ctx)
					}
					p.failIfFalse(arguments != nil, "Cannot parse call arguments")
					if baseIsSuper {
						functionScope := p.currentFunctionScope()
						functionScope.hasDirectSuper = true
						// As above, skip the check when reparsing.
						if !p.lexer.IsReparsingFunction() {
							s := p.closestParentOrdinaryFunctionNonLexicalScope()
							p.semanticFailIfFalse(s.constructorKind == ConstructorKindExtends || (s.isEvalContext && s.derivedContextType == DerivedContextTypeDerivedConstructorContext), "super is not valid in this context")
						}
						if p.currentScope.isArrowFunction {
							functionScope.setInnerArrowFunctionUsesSuperCall()
						}
					}

					isOptionalCall = optionalChainLocation.EndOffset == int(expressionEnd)
					distance := 0
					if depthScope != nil {
						distance = depthScope.distanceToInnermostChild()
					}
					base = ctx.makeFunctionCallNode(location, base, previousBaseWasSuper, arguments, expressionStart, expressionEnd, p.lastTokenEndPosition(), distance, isOptionalCall)
				}()

			case DOT:
				p.parserState.nonTrivialExpressionCount++
				expressionDivot := p.tokenStartPosition()
				p.next(ctx.dontBuildKeywords() | LexerIgnoreReservedWords)
				name := p.token.Data.Ident
				dotType := DotTypeName
				if p.match(PRIVATENAME) {
					p.failIfTrue(baseIsSuper, "Cannot access private names from super")
					if p.currentScope.evalContextType == EvalContextTypeInstanceFieldEvalContext {
						p.semanticFailIfFalse(p.currentScope.hasPrivateName(name), "Cannot reference undeclared private field '", name, "'")
					}
					p.currentScope.usePrivateName(name)
					p.seenPrivateNameUseInNonReparsingFunctionMode = true
					p.parserState.lastPrivateName = name
					dotType = DotTypePrivateMember
					p.token.Type = IDENT
				}
				msg := "'.'"
				if optionalChainBase != nil {
					msg = "'?.'"
				}
				p.matchOrFail(IDENT, "Expected a property name after ", msg)
				base = ctx.createDotAccess(location, base, name, dotType, expressionStart, expressionDivot, p.tokenEndPosition())
				if baseIsSuper && p.currentScope.isArrowFunction {
					p.currentFunctionScope().setInnerArrowFunctionUsesSuperProperty()
				}
				p.next(0)

			case BACKQUOTE:
				p.semanticFailIfTrue(optionalChainBase != nil, "Cannot use tagged templates in an optional chain")
				p.semanticFailIfTrue(baseIsSuper, "Cannot use super as tag for tagged templates")
				expressionDivot := p.tokenStartPosition()
				func() {
					defer saveForScope(&p.parserState.nonLHSCount)()
					templateLiteral := p.parseTemplateLiteral(ctx, BuildRawStrings)
					p.failIfFalse(templateLiteral != nil, "Cannot parse template literal")
					base = ctx.createTaggedTemplate(location, base, templateLiteral, expressionStart, expressionDivot, p.lastTokenEndPosition())
				}()
				p.seenTaggedTemplateInNonReparsingFunctionMode = true

			default:
				break chain
			}
			previousBaseWasSuper = baseIsSuper
			baseIsSuper = false
			t = p.token.Type
		}
		if optionalChainBase != nil {
			chainBase := optionalChainBase
			if isOptionalCall {
				chainBase = nil
			}
			base = ctx.createOptionalChain(location, chainBase, base, !p.match(QUESTIONDOT))
		}
		if !p.match(QUESTIONDOT) {
			break
		}
	}

	if baseIsSuper {
		if newCount != 0 {
			p.semanticFail("Cannot use new with super call")
		}
		p.semanticFail("super is not valid in this context")
	}
	for newCount > 0 {
		newCount--
		base = ctx.createNewExpr(location, base, nil, expressionStart, newTokenStartPositions[newCount], p.lastTokenEndPosition())
	}
	return base
}

func operatorString(prefix bool, t TokenType) string {
	switch t {
	case MINUSMINUS, AUTOMINUSMINUS:
		if prefix {
			return "prefix-decrement"
		}
		return "decrement"
	case PLUSPLUS, AUTOPLUSPLUS:
		if prefix {
			return "prefix-increment"
		}
		return "increment"
	case EXCLAMATION:
		return "logical-not"
	case TILDE:
		return "bitwise-not"
	case TYPEOF:
		return "typeof"
	case VOIDTOKEN:
		return "void"
	case DELETETOKEN:
		return "delete"
	}
	return "error"
}

func (p *Parser) parseUnaryExpression(ctx treeBuilder) TreeExpression {
	defer ctx.popUnaryExprContext(ctx.pushUnaryExprContext())
	defer setForScope(&p.allowsIn, true)()
	tokenStackDepth := 0
	hasPrefixUpdateOp := false
	var lastOperator TokenType

	if p.match(AWAIT) && !p.parserState.classFieldInitMasksAsync && (p.currentFunctionScope().isAsyncFunctionBoundary || p.parseMode.IsModule()) {
		p.semanticFailIfTrue(p.currentScope.isStaticBlock, "Cannot use 'await' within static block")
		return p.parseAwaitExpression(ctx)
	}

	location := p.tokenLocation()

	for p.token.Type.IsUnaryOp() {
		if hasPrefixUpdateOp {
			p.semanticFail("The ", operatorString(true, lastOperator), " operator requires a reference expression")
		}
		if p.token.Type.IsUpdateOp() {
			hasPrefixUpdateOp = true
		}
		lastOperator = p.token.Type
		p.parserState.nonLHSCount++
		tokenStackDepth = ctx.appendUnaryToken(tokenStackDepth, p.token.Type, p.tokenStartPosition())
		p.next(0)
		p.parserState.nonTrivialExpressionCount++
	}
	subExprStart := p.tokenStartPosition()
	e := p.parseMemberExpression(ctx)
	if e == nil {
		if lastOperator != 0 {
			p.failIfTrue(true, "Cannot parse subexpression of ", operatorString(true, lastOperator), "operator")
		}
		p.failWithMessage("Cannot parse member expression")
	}
	if lastOperator.IsUpdateOp() {
		if ctx.isMetaProperty(e) {
			p.semanticFail(p.metaPropertyName(ctx, e), " can't come after a prefix operator")
		}
		op := "--"
		if lastOperator == PLUSPLUS || lastOperator == AUTOPLUSPLUS {
			op = "++"
		}
		p.semanticFailIfFalse(p.isSimpleAssignmentTarget(ctx, e, false), "Prefix ", op, " operator applied to value that is not a reference")
	}
	isEvalOrArguments := false
	if p.strictMode() && ctx.isResolve(e) {
		isEvalOrArguments = isEvalOrArgumentsIdentifier(p.parserState.lastIdentifier)
	}
	if isEvalOrArguments && hasPrefixUpdateOp {
		p.failIfTrueIfStrict(true, "Cannot modify '", p.parserState.lastIdentifier, "' in strict mode")
	}
	switch p.token.Type {
	case PLUSPLUS, MINUSMINUS:
		isIncrement := p.token.Type == PLUSPLUS
		if ctx.isMetaProperty(e) {
			p.semanticFail(p.metaPropertyName(ctx, e), " can't come before a postfix operator")
		}
		op, operator := "--", OpMinusMinus
		if isIncrement {
			op, operator = "++", OpPlusPlus
		}
		p.semanticFailIfFalse(p.isSimpleAssignmentTarget(ctx, e, false), "Postfix ", op, " operator applied to value that is not a reference")
		p.parserState.nonTrivialExpressionCount++
		p.parserState.nonLHSCount++
		e = ctx.makePostfixNode(location, e, operator, subExprStart, p.lastTokenEndPosition(), p.tokenEndPosition())
		p.parserState.assignmentCount++
		if isIncrement {
			p.failIfTrueIfStrict(isEvalOrArguments, "Cannot modify '", p.parserState.lastIdentifier, "' in strict mode")
		} else {
			p.failIfTrueIfStrict(isEvalOrArguments, "'", p.parserState.lastIdentifier, "' cannot be modified in strict mode")
		}
		if hasPrefixUpdateOp {
			p.semanticFail("The ", operatorString(false, lastOperator), " operator requires a reference expression")
		}
		p.next(0)
	}

	end := p.lastTokenEndPosition()
	for tokenStackDepth != 0 {
		subExprStart = ctx.unaryTokenStackLastStart()
		switch ctx.unaryTokenStackLastType() {
		case EXCLAMATION:
			e = ctx.createLogicalNot(location, e)
		case TILDE:
			e = ctx.makeBitwiseNotNode(location, e)
		case MINUS:
			e = ctx.makeNegateNode(location, e)
		case PLUS:
			e = ctx.createUnaryPlus(location, e)
		case PLUSPLUS, AUTOPLUSPLUS:
			e = ctx.makePrefixNode(location, e, OpPlusPlus, subExprStart, subExprStart+2, end)
			p.parserState.assignmentCount++
		case MINUSMINUS, AUTOMINUSMINUS:
			e = ctx.makePrefixNode(location, e, OpMinusMinus, subExprStart, subExprStart+2, end)
			p.parserState.assignmentCount++
		case TYPEOF:
			e = ctx.makeTypeOfNode(location, e, subExprStart, subExprStart, end)
		case VOIDTOKEN:
			e = ctx.createVoid(location, e)
		case DELETETOKEN:
			p.failIfTrueIfStrict(ctx.isResolve(e), "Cannot delete unqualified property '", p.parserState.lastIdentifier, "' in strict mode")
			p.semanticFailIfTrue(ctx.isPrivateLocation(e), "Cannot delete private field ", p.parserState.lastPrivateName)
			e = ctx.makeDeleteNode(location, e, ctx.unaryTokenStackLastStart(), end, end)
		default:
			panic("parseUnaryExpression: unexpected unary token")
		}
		tokenStackDepth = ctx.unaryTokenStackRemoveLast(tokenStackDepth)
	}
	return e
}

// --- Modules ----------------------------------------------------------------------------

func (p *Parser) parseModuleName(ctx treeBuilder) TreeModuleName {
	// ModuleSpecifier: http://www.ecma-international.org/ecma-262/6.0/#sec-imports
	location := p.tokenLocation()
	p.failIfFalse(p.match(STRING), "Imported modules names must be string literals")
	name := p.token.Data.Ident
	p.next(0)
	return ctx.createModuleName(location, name)
}

func hasUnpairedSurrogate(id ident.Identifier) bool {
	units := id.UTF16()
	for i := 0; i < len(units); i++ {
		u := units[i]
		switch {
		case u >= 0xD800 && u < 0xDC00:
			if i+1 >= len(units) || units[i+1] < 0xDC00 || units[i+1] > 0xDFFF {
				return true
			}
			i++
		case u >= 0xDC00 && u <= 0xDFFF:
			return true
		}
	}
	return false
}

func (p *Parser) parseImportClauseItem(ctx treeBuilder, specifierType importSpecifierType) TreeImportSpecifier {
	// ImportSpecifier, ImportedDefaultBinding or NameSpaceImport.
	location := p.tokenLocation()
	var localNameToken Token
	var importedName, localName ident.Identifier

	switch specifierType {
	case namespaceImport:
		// * as namespace
		importedName = ident.StarNamespacePrivateName
		p.next(0)
		p.failIfFalse(p.matchContextualKeyword(ident.As), "Expected 'as' before imported binding name")
		p.next(0)
		p.failIfFalse(p.matchSpecIdentifier(), "Expected a variable name for the import declaration")
		localNameToken = p.token
		localName = p.token.Data.Ident
		p.next(0)

	case namedImport:
		// A, A as B, "string" as B
		isModuleExportName := p.match(STRING)
		localName = p.token.Data.Ident
		importedName = localName
		localNameToken = p.token
		if isModuleExportName {
			p.failIfTrue(hasUnpairedSurrogate(localName), "Expected a well-formed-unicode string for the module export name")
		}
		p.next(0)
		useAs := p.matchContextualKeyword(ident.As)
		if isModuleExportName {
			p.failIfFalse(useAs, "Expected 'as' after the module export name string")
		}
		if useAs {
			p.next(0)
			p.failIfFalse(p.matchSpecIdentifier(), "Expected a variable name for the import declaration")
			localNameToken = p.token
			localName = p.token.Data.Ident
			p.next(0)
		}

	case defaultImport:
		localNameToken = p.token
		localName = p.token.Data.Ident
		importedName = ident.Default
		p.next(0)
	}

	p.semanticFailIfTrue(localNameToken.Type == AWAIT, "Cannot use 'await' as an imported binding name")
	p.semanticFailIfTrue(localNameToken.Type&KeywordTokenFlag != 0, "Cannot use keyword as imported binding name")
	importType := DeclarationImported
	if specifierType == namespaceImport {
		importType = DeclarationImportedNamespace
	}
	result := p.declareVariable(localName, ConstDeclaration, importType)
	if result != DeclarationValid {
		p.failIfTrueIfStrict(result&DeclarationInvalidStrictMode != 0, "Cannot declare an imported binding named ", localName, " in strict mode")
		p.semanticFailIfTrue(result&DeclarationInvalidDuplicateDeclaration != 0, "Cannot declare an imported binding name twice: '", localName, "'")
	}
	return ctx.createImportSpecifier(location, importedName, localName)
}

func (p *Parser) parseImportAttributes(ctx treeBuilder) TreeImportAttributesList {
	var keys identSet
	list := ctx.createImportAttributesList()
	p.consumeOrFail(OPENBRACE, "Expected opening '{' at the start of import attribute")
	for !p.match(CLOSEBRACE) {
		p.failIfFalse(p.matchIdentifierOrKeyword() || p.match(STRING), "Expected an attribute key")
		key := p.token.Data.Ident
		p.failIfFalse(keys.add(key), "A duplicate key for import attributes '", key, "'")
		p.next(0)
		p.consumeOrFail(COLON, "Expected ':' after attribute key")
		p.failIfFalse(p.match(STRING), "Expected an attribute value")
		value := p.token.Data.Ident
		p.next(0)
		ctx.appendImportAttribute(list, key, value)
		if !p.consume(COMMA, 0) {
			break
		}
	}
	p.handleProductionOrFail2(CLOSEBRACE, "}", "end", "import attribute")
	return list
}

// parseOptionalWithClause parses `[no LineTerminator here] with { ... }`.
func (p *Parser) parseOptionalWithClause(ctx treeBuilder) TreeImportAttributesList {
	if !p.lexer.HasLineTerminatorBeforeToken() && p.match(WITH) {
		p.next(0)
		list := p.parseImportAttributes(ctx)
		p.failIfFalse(list != nil, "Unable to parse import attributes")
		return list
	}
	return nil
}

func (p *Parser) parseImportDeclaration(ctx treeBuilder) TreeStatement {
	// http://www.ecma-international.org/ecma-262/6.0/#sec-imports
	importLocation := p.tokenLocation()
	p.next(0)

	specifierList := ctx.createImportSpecifierList()
	importType := ImportTypeNormal

	if p.match(STRING) {
		// import ModuleSpecifier [no LineTerminator here] WithClause? ;
		moduleName := p.parseModuleName(ctx)
		p.failIfFalse(moduleName != nil, "Cannot parse the module name")
		attributes := p.parseOptionalWithClause(ctx)
		p.failIfFalse(p.autoSemiColon(), "Expected a ';' following a targeted import declaration")
		return ctx.createImportDeclaration(importLocation, importType, specifierList, moduleName, attributes)
	}

	isFinishedParsingImport := false
	hasImportDefer := false
	if p.options.UseImportDefer && p.matchContextualKeyword(ident.Defer) {
		sp := p.createSavePoint(ctx)
		p.next(0)
		if p.match(TIMES) {
			// import defer NameSpaceImport FromClause ;
			importType = ImportTypeDeferred
			hasImportDefer = true
		} else {
			// import defer FromClause ;
			p.restoreSavePoint(ctx, sp)
		}
	}

	if p.matchSpecIdentifier() && !hasImportDefer {
		// ImportedDefaultBinding
		specifier := p.parseImportClauseItem(ctx, defaultImport)
		p.failIfFalse(specifier != nil, "Cannot parse the default import")
		ctx.appendImportSpecifier(specifierList, specifier)
		if p.match(COMMA) {
			p.next(0)
		} else {
			isFinishedParsingImport = true
		}
	}

	if !isFinishedParsingImport {
		if p.match(TIMES) {
			// import NameSpaceImport FromClause ;
			specifier := p.parseImportClauseItem(ctx, namespaceImport)
			p.failIfFalse(specifier != nil, "Cannot parse the namespace import")
			ctx.appendImportSpecifier(specifierList, specifier)
		} else {
			p.consumeOrFail(OPENBRACE, "Expected namespace import or import list")
			// NamedImports: { } | { ImportsList } | { ImportsList , }
			for !p.match(CLOSEBRACE) {
				p.failIfFalse(p.matchIdentifierOrKeyword() || p.match(STRING), "Expected an imported name or a module export name string for the import declaration")
				specifier := p.parseImportClauseItem(ctx, namedImport)
				p.failIfFalse(specifier != nil, "Cannot parse the named import")
				ctx.appendImportSpecifier(specifierList, specifier)
				if !p.consume(COMMA, 0) {
					break
				}
			}
			p.handleProductionOrFail2(CLOSEBRACE, "}", "end", "import list")
		}
	}

	// FromClause : from ModuleSpecifier
	p.failIfFalse(p.matchContextualKeyword(ident.From), "Expected 'from' before imported module name")
	p.next(0)

	moduleName := p.parseModuleName(ctx)
	p.failIfFalse(moduleName != nil, "Cannot parse the module name")
	attributes := p.parseOptionalWithClause(ctx)
	p.failIfFalse(p.autoSemiColon(), "Expected a ';' following a targeted import declaration")
	return ctx.createImportDeclaration(importLocation, importType, specifierList, moduleName, attributes)
}

type exportedLocalName struct{ local, exported ident.Identifier }

func (p *Parser) parseExportSpecifier(ctx treeBuilder, maybeExportedLocalNames *[]exportedLocalName, hasKeywordForLocalBindings, hasReferencedModuleExportNames *bool) TreeExportSpecifier {
	// ExportSpecifier: (IdentifierName | ModuleExportName) [as (IdentifierName | ModuleExportName)]
	location := p.tokenLocation()
	localName := p.token.Data.Ident
	exportedName := localName
	if p.match(STRING) {
		*hasReferencedModuleExportNames = true
		p.failIfTrue(hasUnpairedSurrogate(exportedName), "Expected a well-formed-unicode string for the module export name")
	} else if p.token.Type&KeywordTokenFlag != 0 {
		*hasKeywordForLocalBindings = true
	}
	p.next(0)

	if p.matchContextualKeyword(ident.As) {
		p.next(0)
		p.failIfFalse(p.matchIdentifierOrKeyword() || p.match(STRING), "Expected an exported name or a module export name string for the export declaration")
		exportedName = p.token.Data.Ident
		if p.match(STRING) {
			p.failIfTrue(hasUnpairedSurrogate(exportedName), "Expected a well-formed-unicode string for the module export name")
		}
		p.next(0)
	}

	p.semanticFailIfFalse(p.exportName(exportedName), "Cannot export a duplicate name '", exportedName, "'")
	*maybeExportedLocalNames = append(*maybeExportedLocalNames, exportedLocalName{localName, exportedName})
	return ctx.createExportSpecifier(location, localName, exportedName)
}

func (p *Parser) parseExportDeclaration(ctx treeBuilder) TreeStatement {
	// http://www.ecma-international.org/ecma-262/6.0/#sec-exports
	exportLocation := p.tokenLocation()
	p.next(0)

	switch p.token.Type {
	case TIMES:
		// export * FromClause ;
		// export * as (IdentifierName | ModuleExportName) FromClause ;
		p.next(0)
		exportedName := ident.Null
		var specifierLocation TokenLocation
		if p.matchContextualKeyword(ident.As) {
			p.next(0)
			specifierLocation = p.tokenLocation()
			p.failIfFalse(p.matchIdentifierOrKeyword() || p.match(STRING), "Expected an exported name or a module export name string for the export declaration")
			exportedName = p.token.Data.Ident
			if p.match(STRING) {
				p.failIfTrue(hasUnpairedSurrogate(exportedName), "Expected a well-formed-unicode string for the module export name")
			}
			p.next(0)
		}

		p.failIfFalse(p.matchContextualKeyword(ident.From), "Expected 'from' before exported module name")
		p.next(0)
		moduleName := p.parseModuleName(ctx)
		p.failIfFalse(moduleName != nil, "Cannot parse the 'from' clause")
		attributes := p.parseOptionalWithClause(ctx)
		p.failIfFalse(p.autoSemiColon(), "Expected a ';' following a targeted export declaration")

		if !exportedName.IsNull() {
			p.semanticFailIfFalse(p.exportName(exportedName), "Cannot export a duplicate name '", exportedName, "'")
			specifierList := ctx.createExportSpecifierList()
			specifier := ctx.createExportSpecifier(specifierLocation, ident.StarNamespacePrivateName, exportedName)
			ctx.appendExportSpecifier(specifierList, specifier)
			return ctx.createExportNamedDeclaration(exportLocation, specifierList, moduleName, attributes)
		}
		return ctx.createExportAllDeclaration(exportLocation, moduleName, attributes)

	case DEFAULT:
		// export default HoistableDeclaration[~Yield, ~Await, +Default]
		// export default ClassDeclaration[~Yield, ~Await, +Default]
		// export default [lookahead not-in { function, async [no LineTerminator here] function, class }] AssignmentExpression[+In, ~Yield, ~Await]
		p.next(0)

		var result TreeStatement
		isFunctionOrClassDeclaration := false
		localName := ident.Null

		startsWithFunction := p.match(FUNCTION)
		if startsWithFunction || p.match(CLASSTOKEN) {
			sp := p.createSavePoint(ctx)
			isFunctionOrClassDeclaration = true
			p.next(0)
			// ES6 generators
			if startsWithFunction && p.match(TIMES) {
				p.next(0)
			}
			if p.match(IDENT) {
				localName = p.token.Data.Ident
			}
			p.restoreSavePoint(ctx, sp)
		} else if p.matchContextualKeyword(ident.Async) {
			// export default async function [*] name() { }
			sp := p.createSavePoint(ctx)
			p.next(0)
			if p.match(FUNCTION) && !p.lexer.HasLineTerminatorBeforeToken() {
				p.next(0)
				p.consume(TIMES, 0) // async generators
				if p.match(IDENT) {
					localName = p.token.Data.Ident
				}
				isFunctionOrClassDeclaration = true
			}
			p.restoreSavePoint(ctx, sp)
		}

		if localName.IsNull() {
			localName = ident.StarDefaultPrivateName
		}

		if isFunctionOrClassDeclaration {
			switch {
			case startsWithFunction:
				func() {
					defer saveForScope(&p.statementDepth)()
					p.statementDepth = 1
					result = p.parseFunctionDeclaration(ctx, functionDeclarationTypeDeclaration, notExported, DeclarationDefaultExportDefault, -1)
				}()
			case p.match(CLASSTOKEN):
				result = p.parseClassDeclaration(ctx, notExported, DeclarationDefaultExportDefault)
			default:
				functionStart := int(p.token.Start)
				p.next(0)
				func() {
					defer saveForScope(&p.statementDepth)()
					p.statementDepth = 1
					result = p.parseAsyncFunctionDeclaration(ctx, functionStart, notExported, DeclarationDefaultExportDefault, -1)
				}()
			}
		} else {
			// `export default expr;` is treated like
			//     const *default* = expr;
			//     export { *default* as default }
			// where *default* is a private symbol invisible to users.
			location := p.tokenLocation()
			start := p.tokenStartPosition()
			e := p.parseAssignmentExpression(ctx)
			p.failIfFalse(e != nil, "Cannot parse expression")

			result := p.declareVariable(ident.StarDefaultPrivateName, ConstDeclaration, DeclarationNotImported)
			p.semanticFailIfTrue(result&DeclarationInvalidDuplicateDeclaration != 0, "Only one 'default' export is allowed")

			assignment := ctx.createAssignResolve(location, ident.StarDefaultPrivateName, e, start, start, p.tokenEndPosition(), AssignmentContextConstDeclarationStatement)
			statement := ctx.createExprStatement(location, assignment, start)
			p.failIfFalse(p.autoSemiColon(), "Expected a ';' following a targeted export declaration")
			p.semanticFailIfFalse(p.exportName(ident.Default), "Only one 'default' export is allowed")
			p.moduleScopeData.ExportBinding(localName, ident.Default)
			return ctx.createExportDefaultDeclaration(exportLocation, statement, localName)
		}
		p.failIfFalse(result != nil, "Cannot parse the declaration")

		p.semanticFailIfFalse(p.exportName(ident.Default), "Only one 'default' export is allowed")
		p.moduleScopeData.ExportBinding(localName, ident.Default)
		return ctx.createExportDefaultDeclaration(exportLocation, result, localName)

	case OPENBRACE:
		// export ExportClause [FromClause] ;
		// ExportClause : { } | { ExportsList } | { ExportsList , }
		p.next(0)

		specifierList := ctx.createExportSpecifierList()
		var maybeExportedLocalNames []exportedLocalName
		hasKeywordForLocalBindings := false
		hasReferencedModuleExportNames := false
		for !p.match(CLOSEBRACE) {
			p.failIfFalse(p.matchIdentifierOrKeyword() || p.match(STRING), "Expected a variable name or a module export name string for the export declaration")
			specifier := p.parseExportSpecifier(ctx, &maybeExportedLocalNames, &hasKeywordForLocalBindings, &hasReferencedModuleExportNames)
			p.failIfFalse(specifier != nil, "Cannot parse the named export")
			ctx.appendExportSpecifier(specifierList, specifier)
			if !p.consume(COMMA, 0) {
				break
			}
		}
		p.handleProductionOrFail2(CLOSEBRACE, "}", "end", "export list")

		var moduleName TreeModuleName
		var attributes TreeImportAttributesList
		if p.matchContextualKeyword(ident.From) {
			p.next(0)
			moduleName = p.parseModuleName(ctx)
			p.failIfFalse(moduleName != nil, "Cannot parse the 'from' clause")
			attributes = p.parseOptionalWithClause(ctx)
		} else {
			p.semanticFailIfTrue(hasReferencedModuleExportNames, "Cannot use module export names if they reference variable names in the current module")
		}
		p.failIfFalse(p.autoSemiColon(), "Expected a ';' following a targeted export declaration")

		if moduleName == nil {
			p.semanticFailIfTrue(hasKeywordForLocalBindings, "Cannot use keyword as exported variable name")
			// Without a module specifier the export refers to local bindings:
			// `export { A, B, C as D }` uses this module's A, B and C, while
			// `export { A, B, C as D } from "mod"` does not affect this scope.
			for _, n := range maybeExportedLocalNames {
				p.moduleScopeData.ExportBinding(n.local, n.exported)
			}
		}
		return ctx.createExportNamedDeclaration(exportLocation, specifierList, moduleName, attributes)
	}

	// export VariableStatement
	// export Declaration
	var result TreeStatement
	switch p.token.Type {
	case VAR:
		result = p.parseVariableDeclaration(ctx, VarDeclaration, exported)
	case CONSTTOKEN:
		result = p.parseVariableDeclaration(ctx, ConstDeclaration, exported)
	case LET:
		result = p.parseVariableDeclaration(ctx, LetDeclaration, exported)
	case FUNCTION:
		func() {
			defer saveForScope(&p.statementDepth)()
			p.statementDepth = 1
			result = p.parseFunctionDeclaration(ctx, functionDeclarationTypeDeclaration, exported, DeclarationDefaultStandard, -1)
		}()
	case CLASSTOKEN:
		result = p.parseClassDeclaration(ctx, exported, DeclarationDefaultStandard)
	default:
		if p.token.Type == IDENT && p.token.Data.Ident == ident.Async && !p.token.Data.Escaped {
			functionStart := int(p.token.Start)
			p.next(0)
			p.semanticFailIfFalse(p.match(FUNCTION) && !p.lexer.HasLineTerminatorBeforeToken(), "Expected 'function' keyword following 'async' keyword with no preceding line terminator")
			func() {
				defer saveForScope(&p.statementDepth)()
				p.statementDepth = 1
				result = p.parseAsyncFunctionDeclaration(ctx, functionStart, exported, DeclarationDefaultStandard, -1)
			}()
			break
		}
		p.failWithMessage("Expected either a declaration or a variable statement")
	}

	p.failIfFalse(result != nil, "Cannot parse the declaration")
	return ctx.createExportLocalDeclaration(exportLocation, result)
}
