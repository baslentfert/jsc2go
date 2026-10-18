package parser

import (
	"strconv"

	"github.com/baslentfert/jsc2go/ident"
)

// Function, parameter and class parsing (Parser.cpp).

func (p *Parser) isArrowFunctionParameters(ctx treeBuilder) bool {
	if p.match(OPENPAREN) {
		sp := p.createSavePoint(ctx)
		p.next(0)
		isArrowFunction := false
		if p.consume(CLOSEPAREN, 0) {
			isArrowFunction = p.match(ARROWFUNCTION)
		} else {
			checker := &syntaxChecker{}
			// A fake scope keeps parseFormalParameters from declaring
			// variables in the current scope.
			fakeScope := p.newAutoPopScope(p.pushScope())
			defer fakeScope.cleanup()
			fakeScope.scope.setSourceParseMode(ArrowFunctionMode)
			p.resetImplementationVisibilityIfNeeded()

			parametersCount := 0
			isArrowFunction = p.parseFormalParameters(checker, checker.createFormalParameterList(), true, false, &parametersCount) &&
				p.consume(CLOSEPAREN, 0) && p.match(ARROWFUNCTION)
			p.propagateError()
			fakeScope.pop(checker.needsFreeVariableInfo())
		}
		p.restoreSavePoint(ctx, sp)
		return isArrowFunction
	}

	if p.matchSpecIdentifier() {
		if p.isDisallowedIdentifierAwait(&p.token) {
			p.semanticFail("Cannot use 'await' as a parameter name ", p.disallowedIdentifierAwaitReason())
		}
		sp := p.createSavePoint(ctx)
		p.next(0)
		isArrowFunction := p.match(ARROWFUNCTION)
		p.restoreSavePoint(ctx, sp)
		return isArrowFunction
	}
	return false
}

func (p *Parser) parseArrowFunctionSingleExpressionBodySourceElements(ctx treeBuilder) TreeSourceElements {
	location := p.tokenLocation()
	start := p.tokenStartPosition()

	p.enterRecursion()
	defer p.exitRecursion()
	e := p.parseAssignmentExpression(ctx)
	p.failIfFalse(e != nil, "Cannot parse the arrow function expression")

	ctx.setEndOffset(e, p.lastTokenLocation.EndOffset)
	end := p.tokenEndPosition()

	elements := ctx.createSourceElements()
	body := ctx.createReturnStatement(location, e, start, end)
	ctx.setEndOffset(body, p.lastTokenLocation.EndOffset)
	p.recordPauseLocation(ctx.breakpointLocation(body))
	ctx.appendStatement(elements, body)
	return elements
}

// declareRestOrNormalParameter declares a parameter; a duplicate is only an
// error in some contexts, so it is reported through duplicateIdentifier.
func (p *Parser) declareRestOrNormalParameter(name ident.Identifier, duplicateIdentifier *ident.Identifier) bool {
	result := p.declareParameter(name)
	if result&DeclarationInvalidStrictMode != 0 && p.strictMode() {
		p.semanticFailIfTrue(isEvalOrArgumentsIdentifier(name), "Cannot destructure to a parameter name '", name, "' in strict mode")
		if last := p.parserState.lastFunctionName; !last.IsNull() && name == last {
			p.semanticFail("Cannot declare a parameter named '", name, "' as it shadows the name of a strict mode function")
		}
		p.semanticFailureDueToKeyword("parameter name")
		if !p.lexer.IsReparsingFunction() && p.hasDeclaredParameter(name) {
			p.semanticFail("Cannot declare a parameter named '", name, "' in strict mode as it has already been declared")
		}
		p.semanticFail("Cannot declare a parameter named '", name, "' in strict mode")
	}
	if result&DeclarationInvalidDuplicateDeclaration != 0 && duplicateIdentifier != nil {
		*duplicateIdentifier = name
	}
	return true
}

func (p *Parser) createBindingPattern(ctx treeBuilder, kind DestructuringKind, exportType exportType, name ident.Identifier, tok *Token, bindingContext AssignmentContext, duplicateIdentifier *ident.Identifier) TreeDestructuringPattern {
	switch kind {
	case DestructureToVariables:
		result := p.declareVariable(name, VarDeclaration, DeclarationNotImported)
		p.failIfTrueIfStrict(result&DeclarationInvalidStrictMode != 0, "Cannot declare a variable named '", name, "' in strict mode")
		p.semanticFailIfTrue(result&DeclarationInvalidDuplicateDeclaration != 0, "Cannot declare a var variable that shadows a let/const/class variable: '", name, "'")
	case DestructureToLet, DestructureToConst, DestructureToCatchParameters:
		t := LetDeclaration
		if kind == DestructureToConst {
			t = ConstDeclaration
		}
		result := p.declareVariable(name, t, DeclarationNotImported)
		if result != DeclarationValid {
			p.failIfTrueIfStrict(result&DeclarationInvalidStrictMode != 0, "Cannot destructure to a variable named '", name, "' in strict mode")
			p.failIfTrue(result&DeclarationInvalidDuplicateDeclaration != 0, "Cannot declare a lexical variable twice: '", name, "'")
		}
	case DestructureToParameters:
		p.declareRestOrNormalParameter(name, duplicateIdentifier)
		p.propagateError()
	case DestructureToExpressions:
	}

	if exportType == exported {
		p.semanticFailIfFalse(p.exportName(name), "Cannot export a duplicate name '", name, "'")
		p.moduleScopeData.ExportBinding(name, name)
	}
	return ctx.createBindingLocation(tok.Location(), name, tok.Start, tok.End, bindingContext)
}

func (p *Parser) parseFormalParameters(ctx treeBuilder, list TreeFormalParameterList, isArrowFunction, isMethod bool, parameterCount *int) bool {
	hasDefaultParameterValues := false
	hasDestructuringPattern := false
	isRestParameter := false
	duplicateParameter := ident.Null
	restParameterStart := 0
	for {
		var parameter TreeDestructuringPattern
		var defaultValue TreeExpression

		if p.match(CLOSEPAREN) {
			break
		}

		if p.consume(DOTDOTDOT, 0) {
			p.semanticFailIfTrue(p.isDisallowedIdentifierAwait(&p.token), "Cannot use 'await' as a parameter name in an async function")
			pat := p.parseDestructuringPattern(ctx, DestructureToParameters, notExported, &duplicateParameter, &hasDestructuringPattern, AssignmentContextDeclarationStatement, 0)
			p.propagateError()
			parameter = ctx.createRestParameter(pat, restParameterStart)
			p.failIfTrue(p.match(COMMA), "Rest parameter should be the last parameter in a function declaration")
			isRestParameter = true
		} else {
			parameter = p.parseDestructuringPattern(ctx, DestructureToParameters, notExported, &duplicateParameter, &hasDestructuringPattern, AssignmentContextDeclarationStatement, 0)
		}
		p.failIfFalse(parameter != nil, "Cannot parse parameter pattern")
		if !isRestParameter {
			defaultValue = p.parseDefaultValueForDestructuringPattern(ctx)
			if defaultValue != nil {
				hasDefaultParameterValues = true
			}
		}
		p.propagateError()
		if !duplicateParameter.IsNull() {
			p.semanticFailIfTrue(hasDefaultParameterValues, "Duplicate parameter '", duplicateParameter, "' not allowed in function with default parameter values")
			p.semanticFailIfTrue(hasDestructuringPattern, "Duplicate parameter '", duplicateParameter, "' not allowed in function with destructuring parameters")
			p.semanticFailIfTrue(isRestParameter, "Duplicate parameter '", duplicateParameter, "' not allowed in function with a rest parameter")
			p.semanticFailIfTrue(isArrowFunction, "Duplicate parameter '", duplicateParameter, "' not allowed in an arrow function")
			p.semanticFailIfTrue(isMethod, "Duplicate parameter '", duplicateParameter, "' not allowed in a method")
		}
		if isRestParameter || defaultValue != nil || hasDestructuringPattern {
			p.currentScope.setHasNonSimpleParameterList()
		}
		ctx.appendParameter(list, parameter, defaultValue)
		if !isRestParameter {
			restParameterStart++
			if !hasDefaultParameterValues {
				*parameterCount++
			}
		}
		if isRestParameter || !p.consume(COMMA, 0) {
			break
		}
	}
	return true
}

func adjustSuperBindingForBaseConstructor(constructorKind ConstructorKind, expectedSuperBinding SuperBinding, mode SourceParseMode, scopeNeedsSuperBinding, currentScopeUsesEval bool, innerArrowFunctionFeatures InnerArrowFunctionCodeFeatures) SuperBinding {
	if expectedSuperBinding == SuperBindingNotNeeded {
		return SuperBindingNotNeeded
	}
	if constructorKind == ConstructorKindNone && modes(AsyncGeneratorWrapperMethodMode, GeneratorWrapperMethodMode, AsyncMethodMode).contains(mode) {
		return SuperBindingNeeded
	}
	if constructorKind == ConstructorKindNone || constructorKind == ConstructorKindBase {
		isSuperUsedInInnerArrowFunction := innerArrowFunctionFeatures&SuperPropertyInnerArrowFunctionFeature != 0
		if scopeNeedsSuperBinding || isSuperUsedInInnerArrowFunction || currentScopeUsesEval {
			return SuperBindingNeeded
		}
		return SuperBindingNotNeeded
	}
	return SuperBindingNeeded
}

func adjustSuperBindingForScope(constructorKind ConstructorKind, expectedSuperBinding SuperBinding, mode SourceParseMode, s *Scope) SuperBinding {
	return adjustSuperBindingForBaseConstructor(constructorKind, expectedSuperBinding, mode, s.needsSuperBinding, s.usesEval, s.innerArrowFunctionFeatures)
}

func (p *Parser) parseFunctionBody(ctx treeBuilder, checker *syntaxChecker, startLocation TokenLocation, functionStart, functionNameStart, parametersStart int, constructorKind ConstructorKind, superBinding SuperBinding, bodyType FunctionBodyType, parameterCount int) TreeFunctionBody {
	defer setForScope(&p.parserState.isParsingClassFieldInitializer, bodyType != StandardFunctionBodyBlock && p.parserState.isParsingClassFieldInitializer)()
	defer setForScope(&p.parserState.classFieldInitMasksAsync, !p.parseMode.IsAsyncFunction() && p.parserState.classFieldInitMasksAsync)()
	isArrowFunctionBodyExpression := bodyType == ArrowFunctionBodyExpression
	if !isArrowFunctionBodyExpression {
		p.next(0)
		if p.match(CLOSEBRACE) {
			sb := adjustSuperBindingForScope(constructorKind, superBinding, p.parseMode, p.currentScope)
			return ctx.createFunctionMetadata(startLocation, p.tokenLocation(), functionStart, functionNameStart, parametersStart, p.currentImplementationVisibility(), p.lexicallyScopedFeatures(), constructorKind, sb, parameterCount, p.parseMode, isArrowFunctionBodyExpression)
		}
	}

	defer saveForScope(&p.statementDepth)()
	p.statementDepth = 0
	// Nested function bodies are only syntax checked; they are reparsed with
	// the AST builder when the function is first compiled.
	if bodyType == ArrowFunctionBodyExpression {
		p.failIfFalse(p.parseArrowFunctionSingleExpressionBodySourceElements(checker) != nil, "Cannot parse body of this arrow function")
	} else {
		msg := "Cannot parse body of this arrow function"
		if bodyType == StandardFunctionBodyBlock {
			msg = "Cannot parse body of this function"
		}
		p.failIfFalse(p.parseSourceElements(checker, CheckForStrictMode) != nil, msg)
	}
	sb := adjustSuperBindingForScope(constructorKind, superBinding, p.parseMode, p.currentScope)
	visibility := p.currentImplementationVisibility()
	if p.parseMode.IsAsyncFunctionWrapper() && p.currentScope.usesAwait {
		visibility = max(ImplementationVisibilityPrivate, visibility)
		p.currentScope.setImplementationVisibility(visibility)
	}
	return ctx.createFunctionMetadata(startLocation, p.tokenLocation(), functionStart, functionNameStart, parametersStart, visibility, p.lexicallyScopedFeatures(), constructorKind, sb, parameterCount, p.parseMode, isArrowFunctionBodyExpression)
}

func stringArticleForFunctionMode(mode SourceParseMode) string {
	switch mode {
	case GetterMode, SetterMode, NormalFunctionMode, MethodMode, GeneratorBodyMode, GeneratorWrapperFunctionMode, GeneratorWrapperMethodMode:
		return "a "
	}
	return "an "
}

func stringForFunctionMode(mode SourceParseMode) string {
	switch mode {
	case GetterMode:
		return "getter"
	case SetterMode:
		return "setter"
	case NormalFunctionMode:
		return "function"
	case MethodMode:
		return "method"
	case GeneratorWrapperFunctionMode, GeneratorBodyMode:
		return "generator function"
	case GeneratorWrapperMethodMode:
		return "generator method"
	case ArrowFunctionMode:
		return "arrow function"
	case AsyncFunctionMode, AsyncFunctionBodyMode:
		return "async function"
	case AsyncMethodMode:
		return "async method"
	case AsyncArrowFunctionBodyMode, AsyncArrowFunctionMode:
		return "async arrow function"
	case AsyncGeneratorWrapperFunctionMode, AsyncGeneratorBodyMode:
		return "async generator function"
	case AsyncGeneratorWrapperMethodMode:
		return "async generator method"
	}
	return ""
}

func (p *Parser) parseFunctionParameters(ctx treeBuilder, info *ParserFunctionInfo) TreeFormalParameterList {
	mode := p.parseMode
	list := ctx.createFormalParameterList()
	if mode == ClassStaticBlockMode {
		return list
	}
	defer setForScope(&p.parserState.functionParsePhase, functionParsePhaseParameters)()

	if mode == ArrowFunctionMode || mode == AsyncArrowFunctionMode {
		if !p.matchSpecIdentifier() && !p.match(OPENPAREN) {
			p.semanticFailureDueToKeyword(stringForFunctionMode(mode), " name")
			p.failWithMessage("Expected an arrow function input parameter")
		}
		if p.consume(OPENPAREN, 0) {
			if p.match(CLOSEPAREN) {
				info.ParameterCount = 0
			} else {
				p.failIfFalse(p.parseFormalParameters(ctx, list, true, false, &info.ParameterCount), "Cannot parse parameters for this ", stringForFunctionMode(mode))
			}
			p.consumeOrFail(CLOSEPAREN, "Expected a ')' or a ',' after a parameter declaration")
		} else {
			info.ParameterCount = 1
			parameter := p.parseDestructuringPattern(ctx, DestructureToParameters, notExported, nil, nil, AssignmentContextDeclarationStatement, 0)
			p.failIfFalse(parameter != nil, "Cannot parse parameter pattern")
			ctx.appendParameter(list, parameter, nil)
		}
		return list
	}

	if !p.consume(OPENPAREN, 0) {
		p.semanticFailureDueToKeyword(stringForFunctionMode(mode), " name")
		p.failWithMessage("Expected an opening '(' before a ", stringForFunctionMode(mode), "'s parameter list")
	}

	switch mode {
	case GetterMode:
		p.consumeOrFail(CLOSEPAREN, "getter functions must have no parameters")
		info.ParameterCount = 0
	case SetterMode:
		p.failIfTrue(p.match(CLOSEPAREN), "setter functions must have one parameter")
		duplicateParameter := ident.Null
		hasDestructuringPattern := false
		parameter := p.parseDestructuringPattern(ctx, DestructureToParameters, notExported, &duplicateParameter, &hasDestructuringPattern, AssignmentContextDeclarationStatement, 0)
		p.failIfFalse(parameter != nil, "setter functions must have one parameter")
		defaultValue := p.parseDefaultValueForDestructuringPattern(ctx)
		p.propagateError()
		if defaultValue != nil || hasDestructuringPattern {
			p.semanticFailIfTrue(!duplicateParameter.IsNull(), "Duplicate parameter '", duplicateParameter, "' not allowed in function with non-simple parameter list")
			p.currentScope.setHasNonSimpleParameterList()
		}
		ctx.appendParameter(list, parameter, defaultValue)
		info.ParameterCount = 1
		if defaultValue != nil {
			info.ParameterCount = 0
		}
		p.failIfTrue(p.match(COMMA), "setter functions must have one parameter")
		p.consumeOrFail(CLOSEPAREN, "Expected a ')' after a parameter declaration")
	default:
		if p.match(CLOSEPAREN) {
			info.ParameterCount = 0
		} else {
			p.failIfFalse(p.parseFormalParameters(ctx, list, false, mode.IsMethod(), &info.ParameterCount), "Cannot parse parameters for this ", stringForFunctionMode(mode))
		}
		p.consumeOrFail(CLOSEPAREN, "Expected a ')' or a ',' after a parameter declaration")
	}
	return list
}

// createGeneratorParameters declares the hidden parameters of a generator
// or async function body.
func (p *Parser) createGeneratorParameters(ctx treeBuilder, parameterCount *int) TreeFormalParameterList {
	parameters := ctx.createFormalParameterList()
	location := p.tokenLocation()
	position := p.tokenStartPosition()
	for _, name := range []ident.Identifier{
		ident.GeneratorPrivateName,
		ident.GeneratorStatePrivateName,
		ident.GeneratorValuePrivateName,
		ident.GeneratorResumeModePrivateName,
		ident.GeneratorFramePrivateName,
	} {
		p.declareParameter(name)
		binding := ctx.createBindingLocation(location, name, position, position, AssignmentContextDeclarationStatement)
		ctx.appendParameter(parameters, binding, nil)
		*parameterCount++
	}
	return parameters
}

func (p *Parser) parseFunctionInfo(ctx treeBuilder, requirements FunctionNameRequirements, nameIsInContainingScope bool, constructorKind ConstructorKind, expectedSuperBinding SuperBinding, functionStart int, info *ParserFunctionInfo, definitionType functionDefinitionType, functionConstructorParametersEndPosition int) bool {
	mode := p.parseMode
	parentScope := p.currentScope

	functionNameIsAwait := p.isPossiblyEscapedAwait(&p.token)
	isDisallowedAwaitFunctionNameReason := ""
	if functionNameIsAwait && !p.canUseIdentifierAwait() {
		isDisallowedAwaitFunctionNameReason = p.disallowedIdentifierAwaitReason()
	}

	functionScope := p.newAutoPopScope(p.pushScope())
	defer functionScope.cleanup()
	functionScope.scope.setSourceParseMode(mode)
	p.resetImplementationVisibilityIfNeeded()
	functionScope.scope.expectedSuperBinding = expectedSuperBinding
	functionScope.scope.constructorKind = constructorKind

	defer setForScope(&p.parserState.functionParsePhase, functionParsePhaseBody)()
	functionNameStart := int(p.token.Start)
	lastFunctionName := p.parserState.lastFunctionName
	p.parserState.lastFunctionName = ident.Null
	parametersStart := -1
	var startLocation TokenLocation
	var functionBodyType FunctionBodyType

	tryLoadCachedFunction := func() bool {
		if !ctx.canUseFunctionCache() {
			return false
		}
		cached := p.findCachedFunctionInfo(parametersStart)
		if cached == nil {
			return false
		}
		endLocation := TokenLocation{StartOffset: cached.lastTokenStartOffset}
		bodyType := StandardFunctionBodyBlock
		if mode == ArrowFunctionMode || mode == AsyncArrowFunctionMode {
			bodyType = ArrowFunctionBodyBlock
			if cached.isBodyArrowExpression {
				bodyType = ArrowFunctionBodyExpression
			}
		}
		sb := adjustSuperBindingForBaseConstructor(cached.constructorKind, cached.expectedSuperBinding, mode, cached.needsSuperBinding, cached.usesEval, cached.innerArrowFunctionFeatures)
		info.Body = ctx.createFunctionMetadata(startLocation, endLocation, functionStart, functionNameStart, parametersStart, cached.implementationVisibility, cached.lexicallyScopedFeatures, cached.constructorKind, sb, cached.parameterCount, mode, bodyType == ArrowFunctionBodyExpression)
		info.EndOffset = cached.endFunctionOffset
		info.ParameterCount = cached.parameterCount

		s := functionScope.scope
		s.usesEval = cached.usesEval
		s.usesImportMeta = cached.usesImportMeta
		s.lexicallyScopedFeatures = cached.lexicallyScopedFeatures
		s.innerArrowFunctionFeatures = cached.innerArrowFunctionFeatures
		s.implementationVisibility = cached.implementationVisibility
		s.needsFullActivation = cached.needsFullActivation
		s.needsSuperBinding = cached.needsSuperBinding
		for _, v := range cached.usedVariables {
			s.usedVariables[len(s.usedVariables)-1].add(v)
		}
		functionScope.pop(ctx.needsFreeVariableInfo())

		p.token = cached.endFunctionToken()
		p.lexer.SetOffset(int(p.token.End))
		if bodyType == ArrowFunctionBodyExpression {
			p.next(0)
			ctx.setEndOffset(info.Body, p.lexer.CurrentOffset())
		} else {
			ctx.setEndOffset(info.Body, p.lexer.CurrentOffset())
			p.next(0)
		}
		return true
	}

	checker := &syntaxChecker{}

	var oldState parserState
	if mode == ArrowFunctionMode || mode == AsyncArrowFunctionMode {
		startLocation = p.tokenLocation()
		parametersStart = int(p.token.Start)
		info.StartOffset = parametersStart

		if tryLoadCachedFunction() {
			return true
		}

		p.parserState.lastFunctionName = lastFunctionName
		oldState = p.internalSaveParserState(ctx)
		func() {
			// Arrow parameters are parsed with [+Yield] so that yield
			// expressions in them are early errors (ES6 #sec-arrow-function-definitions).
			defer setForScope(&functionScope.scope.isGeneratorFunction, parentScope.isGeneratorFunction)()
			defer setForScope(&p.parserState.allowAwait, !parentScope.isAsyncFunction && !mode.IsAsyncFunction())()
			p.parseFunctionParameters(checker, info)
			p.propagateError()
		}()

		p.matchOrFail(ARROWFUNCTION, "Expected a '=>' after arrow function parameter declaration")
		if p.lexer.HasLineTerminatorBeforeToken() {
			p.failDueToUnexpectedToken()
		}

		// A '{' after '=>' means a block body.
		p.next(0)
		functionBodyType = ArrowFunctionBodyExpression
		if p.match(OPENBRACE) {
			functionBodyType = ArrowFunctionBodyBlock
		}
	} else {
		// The names of function and async function expressions may be "yield"
		// even inside generators.
		canUseYield := !p.strictMode()
		if !(definitionType == functionDefinitionExpression && (mode == NormalFunctionMode || mode == AsyncFunctionMode)) {
			canUseYield = canUseYield && !parentScope.isGeneratorFunction
		}

		if requirements != FunctionNameRequirementsUnnamed {
			if p.matchSpecIdentifierWith(canUseYield, functionNameIsAwait) {
				info.Name = p.token.Data.Ident
				p.parserState.lastFunctionName = info.Name
				switch {
				case isDisallowedAwaitFunctionNameReason != "":
					p.semanticFailIfTrue(definitionType == functionDefinitionDeclaration || mode.IsAsyncFunctionOrAsyncGeneratorWrapper(), "Cannot declare function named 'await' ", isDisallowedAwaitFunctionNameReason)
				case mode.IsAsyncFunctionOrAsyncGeneratorWrapper() && p.match(AWAIT) && definitionType == functionDefinitionExpression:
					p.semanticFail("Cannot declare ", stringForFunctionMode(mode), " named 'await'")
				case mode.IsGeneratorOrAsyncGeneratorWrapper() && p.match(YIELD) && definitionType == functionDefinitionExpression:
					p.semanticFail("Cannot declare ", stringForFunctionMode(mode), " named 'yield'")
				}
				p.next(0)
				if !nameIsInContainingScope {
					p.failIfTrueIfStrict(functionScope.scope.declareCallee(info.Name)&DeclarationInvalidStrictMode != 0, "'", info.Name, "' is not a valid ", stringForFunctionMode(mode), " name in strict mode")
				}
			} else if requirements == FunctionNameRequirementsNamed {
				if p.match(OPENPAREN) {
					p.semanticFailIfTrue(mode == NormalFunctionMode, "Function statements must have a name")
					p.semanticFailIfTrue(mode == AsyncFunctionMode, "Async function statements must have a name")
				}
				p.semanticFailureDueToKeyword(stringForFunctionMode(mode), " name")
				p.failDueToUnexpectedToken()
			}
		}

		startLocation = p.tokenLocation()
		parametersStart = int(p.token.Start)
		info.StartOffset = parametersStart

		if tryLoadCachedFunction() {
			return true
		}

		p.parserState.lastFunctionName = lastFunctionName
		oldState = p.internalSaveParserState(ctx)
		func() {
			defer setForScope(&p.parserState.allowAwait, !mode.IsAsyncFunction())()
			p.parseFunctionParameters(checker, info)
			p.propagateError()
		}()

		p.matchOrFail(OPENBRACE, "Expected an opening '{' at the start of a ", stringForFunctionMode(mode), " body")

		// From the Function constructor, the parameters must be exactly the
		// string supplied as parameters.
		if functionConstructorParametersEndPosition >= 0 {
			p.semanticFailIfFalse(int(p.lastTokenEndPosition()) == functionConstructorParametersEndPosition, "Parameters should match arguments offered as parameters in Function constructor")
		}
		functionBodyType = StandardFunctionBodyBlock
	}

	// Collect capture candidates from parameter expressions that are not
	// parameters themselves, before the body's declarations hide them
	// (https://bugs.webkit.org/show_bug.cgi?id=156962).
	var nonLocalCapturesFromParameterExpressions identSet
	functionScope.scope.forEachUsedVariable(func(id ident.Identifier) bool {
		if !functionScope.scope.hasDeclaredParameter(id) {
			nonLocalCapturesFromParameterExpressions.add(id)
			if ctx.needsFreeVariableInfo() {
				parentScope.addClosedVariableCandidateUnconditionally(id)
			}
		}
		return true
	})

	performParsingFunctionBody := func() TreeFunctionBody {
		return p.parseFunctionBody(ctx, checker, startLocation, functionStart, functionNameStart, parametersStart, constructorKind, expectedSuperBinding, functionBodyType, info.ParameterCount)
	}

	visibility := p.currentImplementationVisibility()
	if mode.IsGeneratorOrAsyncFunctionWrapper() {
		func() {
			bodyScope := p.newAutoPopScope(p.pushScope())
			defer bodyScope.cleanup()
			innerParseMode := GeneratorBodyMode
			if mode.IsAsyncFunctionOrAsyncGeneratorWrapper() {
				innerParseMode = getAsyncFunctionBodyParseMode(mode)
			}
			bodyScope.scope.setSourceParseMode(innerParseMode)
			p.resetImplementationVisibilityIfNeeded()
			bodyScope.scope.constructorKind = ConstructorKindNone
			bodyScope.scope.expectedSuperBinding = expectedSuperBinding
			// Disallow 'use strict' in the implicit inner function if needed.
			if functionScope.scope.hasNonSimpleParameterList {
				bodyScope.scope.setHasNonSimpleParameterList()
			}
			info.Body = performParsingFunctionBody()
			// A "use strict" in the body makes the wrapper strict too.
			if bodyScope.scope.strictMode() {
				functionScope.scope.setStrictMode()
			}
			visibility = bodyScope.scope.implementationVisibility
			bodyScope.pop(ctx.needsFreeVariableInfo())
		}()
	} else {
		info.Body = performParsingFunctionBody()
	}

	p.restoreParserState(ctx, oldState)
	p.failIfFalse(info.Body != nil, "Cannot parse the body of this ", stringForFunctionMode(mode))
	ctx.setEndOffset(info.Body, p.lexer.CurrentOffset())
	if functionScope.scope.strictMode() && requirements != FunctionNameRequirementsUnnamed {
		p.semanticFailIfTrue(info.Name == ident.Arguments, "'", info.Name, "' is not a valid function name in strict mode")
		p.semanticFailIfTrue(info.Name == ident.Eval, "'", info.Name, "' is not a valid function name in strict mode")
		p.semanticFailIfTrue(info.Name == ident.Yield, "'", info.Name, "' is not a valid function name in strict mode")
	}

	location := p.token.Location()
	info.EndOffset = int(p.token.Start)
	if functionBodyType == ArrowFunctionBodyExpression {
		location = p.locationBeforeLastToken()
		info.EndOffset = location.EndOffset
	} else {
		p.recordFunctionEntryLocation(TextPosition(startLocation.StartOffset))
		p.recordFunctionLeaveLocation(TextPosition(location.StartOffset))
	}

	// Cache what the enclosing scopes need, so later parses can skip the body.
	// The minimum is "x=>x" or "function(){}" plus 4 characters.
	minimumSourceLengthToCache := 8
	if functionBodyType == StandardFunctionBodyBlock {
		minimumSourceLengthToCache = 16
	}
	var newInfo *sourceProviderCacheItem
	sourceLength := info.EndOffset - info.StartOffset
	var params sourceProviderCacheItemCreationParameters
	var precomputedFreeVariables []ident.Identifier
	if ctx.canUseFunctionCache() && p.functionCache != nil && sourceLength > minimumSourceLengthToCache {
		s := functionScope.scope
		params.endFunctionOffset = info.EndOffset
		params.lastTokenStartOffset = location.StartOffset
		params.lastTokenEndOffset = location.EndOffset
		params.parameterCount = info.ParameterCount
		params.constructorKind = constructorKind
		params.expectedSuperBinding = expectedSuperBinding
		params.implementationVisibility = visibility
		if functionBodyType == ArrowFunctionBodyExpression {
			params.isBodyArrowExpression = true
			params.tokenType = p.token.Type
		}
		params.usesEval = s.usesEval
		params.usesImportMeta = s.usesImportMeta
		params.lexicallyScopedFeatures = s.lexicallyScopedFeatures
		params.needsFullActivation = s.needsFullActivation
		params.innerArrowFunctionFeatures = s.innerArrowFunctionFeatures
		params.needsSuperBinding = s.needsSuperBinding
		free := s.freeVariables(&identSet{})
		params.freeVariableCount = len(free)
		params.usedVariables = append(free, nonLocalCapturesFromParameterExpressions.keys...)
		precomputedFreeVariables = params.freeVariables()
		if precomputedFreeVariables == nil {
			precomputedFreeVariables = []ident.Identifier{}
		}
		newInfo = newSourceProviderCacheItem(&params)
	}

	functionScope.popWithPrecomputed(ctx.needsFreeVariableInfo(), precomputedFreeVariables)

	if functionBodyType != ArrowFunctionBodyExpression {
		p.consumeOrFail(CLOSEBRACE, "Expected a closing '}' after a ", stringForFunctionMode(mode), " body")
	} else if ctx.createsAST() {
		// The last token was lexed in the body's context (strings may not have
		// been built), so lex it again.
		p.lexCurrentTokenAgainUnderCurrentContext(ctx)
	}

	if newInfo != nil {
		p.functionCache.add(info.StartOffset, newInfo)
	}
	return true
}

func (p *Parser) parseFunctionDeclaration(ctx treeBuilder, declarationType functionDeclarationType, exportType exportType, defaultContext DeclarationDefaultContext, functionConstructorParametersEndPosition int) TreeStatement {
	location := p.tokenLocation()
	functionStart := p.tokenStart()
	p.next(0)
	parseMode := NormalFunctionMode
	if p.match(TIMES) {
		p.failIfTrue(declarationType == functionDeclarationTypeStatement, "Cannot use generator function declaration in single-statement context")
		p.next(0)
		parseMode = GeneratorWrapperFunctionMode
	}
	defer setForScope(&p.parseMode, parseMode)()

	var info ParserFunctionInfo
	requirements := FunctionNameRequirementsNamed
	if defaultContext == DeclarationDefaultExportDefault {
		// `export default function () {}` needs no name; it is called "*default*".
		requirements = FunctionNameRequirementsNone
		info.Name = ident.StarDefaultPrivateName
	}

	p.failIfFalse(p.parseFunctionInfo(ctx, requirements, true, ConstructorKindNone, SuperBindingNotNeeded, functionStart, &info, functionDefinitionDeclaration, functionConstructorParametersEndPosition), "Cannot parse this function")

	result, declScope := p.declareFunction(info.Name)
	p.failIfTrueIfStrict(result&DeclarationInvalidStrictMode != 0, "Cannot declare a function named '", info.Name, "' in strict mode")
	p.semanticFailIfTrue(result&DeclarationInvalidDuplicateDeclaration != 0, "Cannot declare a function that shadows a let/const/class/function variable '", info.Name, "'")
	if exportType == exported {
		p.semanticFailIfFalse(p.exportName(info.Name), "Cannot export a duplicate function name: '", info.Name, "'")
		p.moduleScopeData.ExportBinding(info.Name, info.Name)
	}

	statement := ctx.createFuncDeclStatement(location, &info)
	if ctx.createsAST() {
		md := metadata(&info)
		declScope.appendFunction(md)
		// Annex B.3.3: sloppy-mode block functions may also be hoisted as vars.
		isSloppyModeHoistingCandidate := p.statementDepth != 1 && !p.strictMode() && p.parseMode == NormalFunctionMode
		if isSloppyModeHoistingCandidate {
			declScope.addSloppyModeFunctionHoistingCandidate(md, false)
		}
	}
	return statement
}

func (p *Parser) parseAsyncFunctionDeclaration(ctx treeBuilder, functionStart int, exportType exportType, defaultContext DeclarationDefaultContext, functionConstructorParametersEndPosition int) TreeStatement {
	location := p.tokenLocation()
	p.next(0)
	var info ParserFunctionInfo
	parseMode := AsyncFunctionMode
	if p.consume(TIMES, 0) {
		parseMode = AsyncGeneratorWrapperFunctionMode
	}
	defer setForScope(&p.parseMode, parseMode)()

	requirements := FunctionNameRequirementsNamed
	if defaultContext == DeclarationDefaultExportDefault {
		requirements = FunctionNameRequirementsNone
		info.Name = ident.StarDefaultPrivateName
	}

	p.failIfFalse(p.parseFunctionInfo(ctx, requirements, true, ConstructorKindNone, SuperBindingNotNeeded, functionStart, &info, functionDefinitionDeclaration, functionConstructorParametersEndPosition), "Cannot parse this async function")
	p.failIfFalse(!info.Name.IsNull(), "Async function statements must have a name")

	result, declScope := p.declareFunction(info.Name)
	p.failIfTrueIfStrict(result&DeclarationInvalidStrictMode != 0, "Cannot declare an async function named '", info.Name, "' in strict mode")
	p.semanticFailIfTrue(result&DeclarationInvalidDuplicateDeclaration != 0, "Cannot declare an async function that shadows a let/const/class/function variable '", info.Name, "'")
	if exportType == exported {
		p.semanticFailIfFalse(p.exportName(info.Name), "Cannot export a duplicate function name: '", info.Name, "'")
		p.moduleScopeData.ExportBinding(info.Name, info.Name)
	}

	statement := ctx.createFuncDeclStatement(location, &info)
	if ctx.createsAST() {
		declScope.appendFunction(metadata(&info))
	}
	return statement
}

func (p *Parser) parseClassDeclaration(ctx treeBuilder, exportType exportType, defaultContext DeclarationDefaultContext) TreeStatement {
	location := p.tokenLocation()
	classStart := p.tokenStartPosition()

	var info ParserClassInfo
	requirements := FunctionNameRequirementsNamed
	if defaultContext == DeclarationDefaultExportDefault {
		// `export default class {}` needs no name; it is called "*default*".
		requirements = FunctionNameRequirementsNone
		info.ClassName = ident.StarDefaultPrivateName
	}

	classExpr := p.parseClass(ctx, requirements, &info)
	p.failIfFalse(classExpr != nil, "Failed to parse class")

	result := p.declareVariable(info.ClassName, LetDeclaration, DeclarationNotImported)
	p.semanticFailIfTrue(result&DeclarationInvalidDuplicateDeclaration != 0, "Cannot declare a class twice: '", info.ClassName, "'")
	if exportType == exported {
		p.semanticFailIfFalse(p.exportName(info.ClassName), "Cannot export a duplicate class name: '", info.ClassName, "'")
		p.moduleScopeData.ExportBinding(info.ClassName, info.ClassName)
	}

	classEnd := p.lastTokenEndPosition()
	return ctx.createClassDeclStatement(location, classExpr, classStart, classEnd)
}

const (
	instanceComputedNamePrefix = "instanceComputedName"
	staticComputedNamePrefix   = "staticComputedName"
)

func (p *Parser) parseClass(ctx treeBuilder, requirements FunctionNameRequirements, info *ParserClassInfo) TreeExpression {
	start := p.tokenStartPosition()
	location := p.tokenLocation()
	info.StartOffset = location.StartOffset

	// Two scopes: class heritage evaluation must see the class name binding
	// but not the class's private names, e.g.
	//
	//     class A extends (class { constructor() { print(A); print(A.#test); } }) {
	//         static #test = 42;
	//     }
	//
	// where A is fine but A.#test is a SyntaxError.
	classHeadScope := p.newAutoPopScope(p.pushScope())
	defer classHeadScope.cleanup()
	classHeadScope.scope.setIsLexicalScope()
	classHeadScope.scope.preventVarDeclarations()
	classHeadScope.scope.setStrictMode()
	p.next(0)
	p.semanticFailIfTrue(p.currentScope.isStaticBlock && p.match(AWAIT), "Cannot use 'await' as a class name within static block")

	if p.match(IDENT) || p.isAllowedIdentifierAwait(&p.token) {
		info.ClassName = p.token.Data.Ident
		p.next(0)
		p.failIfTrue(classHeadScope.scope.declareLexicalVariable(info.ClassName, true, DeclarationNotImported, false, false)&DeclarationInvalidStrictMode != 0, "'", info.ClassName, "' is not a valid class name")
	} else if requirements == FunctionNameRequirementsNamed {
		p.semanticFailIfTrue(p.match(OPENBRACE), "Class statements must have a name")
		p.semanticFailureDueToKeyword("class name")
		p.failDueToUnexpectedToken()
	}

	divot := start
	var parentClass TreeExpression
	if p.consume(EXTENDS, 0) {
		divot = p.tokenStartPosition()
		parentClass = p.parseMemberExpression(ctx)
		p.failIfFalse(parentClass != nil, "Cannot parse the parent class name")
	}
	constructorKind := ConstructorKindBase
	if parentClass != nil {
		constructorKind = ConstructorKindExtends
	}

	classHeadEnd := p.lastTokenEndPosition()
	p.consumeOrFail(OPENBRACE, "Expected opening '{' at the start of a class body")

	classScope := p.newAutoPopScope(p.pushScope())
	defer classScope.cleanup()
	classScope.scope.setIsLexicalScope()
	classScope.scope.preventVarDeclarations()
	classScope.scope.setStrictMode()
	classScope.scope.isClassScope = true

	declaresPrivateMethod := false
	declaresPrivateAccessor := false
	declaresStaticPrivateMethod := false
	declaresStaticPrivateAccessor := false

	var constructor TreeExpression
	var classElements, classElementsTail TreePropertyList
	nextInstanceComputedFieldID := 0
	nextStaticComputedFieldID := 0
	for !p.match(CLOSEBRACE) {
		if p.consume(SEMICOLON, 0) {
			continue
		}

		methodLocation := p.tokenLocation()
		functionStart := p.tokenStart()

		// "static" is a non-reserved keyword in sloppy mode, for compatibility.
		tag := ClassElementTagInstance
		parseMode := MethodMode
		propertyType := PropertyConstant
		if p.match(RESERVED_IF_STRICT) && p.token.Data.Ident == ident.Static {
			sp := p.createSavePoint(ctx)
			p.next(0)
			if p.match(OPENPAREN) || p.match(SEMICOLON) || p.match(EQUAL) {
				// "static()" is a method and "static" a field.
				p.restoreSavePoint(ctx, sp)
			} else {
				tag = ClassElementTagStatic
				functionStart = p.tokenStart()
				if p.match(OPENBRACE) {
					parseMode = ClassStaticBlockMode
				}
			}
		}

		name := ident.Null
		var computedPropertyName TreeExpression
		isGetter := false
		isSetter := false
		if p.consume(TIMES, 0) {
			parseMode = GeneratorWrapperMethodMode
		}

	parseMethod:
		for {
			switch t := p.token.Type; {
			case t == STRING:
				name = p.token.Data.Ident
				p.next(0)
			case t == BIGINT:
				var ok bool
				name, ok = bigIntToDecimal(p.token.Data.BigIntString, p.token.Data.Radix)
				p.failIfFalse(ok, "Cannot parse big int property name")
				p.next(0)
			case t == ESCAPED_KEYWORD || t == IDENT || t == AWAIT:
				if t != AWAIT && p.token.Data.Ident == ident.Async && !p.token.Data.Escaped && !parseMode.IsGeneratorMethod() && !parseMode.IsAsyncMethod() {
					p.next(0)
					// A field called 'async' without initializer is matched by SEMICOLON.
					if p.match(OPENPAREN) || p.match(COLON) || p.match(SEMICOLON) || p.match(EQUAL) || p.lexer.HasLineTerminatorBeforeToken() {
						name = ident.Async
						break parseMethod
					}
					if p.consume(TIMES, 0) {
						parseMode = AsyncGeneratorWrapperMethodMode
					} else {
						parseMode = AsyncMethodMode
					}
					continue parseMethod
				}
				name = p.token.Data.Ident
				escaped := p.token.Data.Escaped
				p.next(0)
				if parseMode == MethodMode && !escaped && (p.matchIdentifierOrKeyword() || p.match(STRING) || p.match(DOUBLE) || p.match(INTEGER) || p.match(BIGINT) || p.match(OPENBRACKET) || p.match(PRIVATENAME)) {
					isGetter = name == ident.Get
					isSetter = name == ident.Set
				}
			case t == DOUBLE || t == INTEGER:
				name = ident.FromNumber(p.token.Data.Double)
				p.next(0)
			case t == OPENBRACKET:
				p.next(0)
				p.semanticFailIfTrue(p.currentScope.isStaticBlock && p.match(IDENT) && p.isArgumentsIdentifier(), "Cannot use 'arguments' as an identifier in static block")
				computedPropertyName = p.parseAssignmentExpression(ctx)
				propertyType |= PropertyComputed
				p.failIfFalse(computedPropertyName != nil, "Cannot parse computed property name")
				p.handleProductionOrFail(CLOSEBRACKET, "]", "end", "computed property name")
			case t == PRIVATENAME:
				name = p.token.Data.Ident
				p.failIfTrue(isGetter || isSetter, "Cannot parse class method with private name")
				p.next(0)
				if p.match(OPENPAREN) {
					p.semanticFailIfTrue(classScope.scope.declarePrivateMethod(name, tag)&DeclarationInvalidDuplicateDeclaration != 0, "Cannot declare private method twice")
					p.semanticFailIfTrue(name == ident.ConstructorPrivateField, "Cannot declare a private method named '#constructor'")
					if tag == ClassElementTagStatic {
						declaresStaticPrivateMethod = true
					} else {
						declaresPrivateMethod = true
					}
					propertyType |= PropertyPrivateMethod
					break parseMethod
				}
				p.failIfTrue(p.match(OPENPAREN), "Cannot parse class method with private name")
				p.semanticFailIfTrue(classScope.scope.declarePrivateField(name)&DeclarationInvalidDuplicateDeclaration != 0, "Cannot declare private field twice")
				propertyType |= PropertyPrivateField
			case t == OPENBRACE:
				p.failIfFalse(parseMode == ClassStaticBlockMode, "Cannot parse static block without 'static'")
				propertyType |= PropertyBlock
			case t&KeywordTokenFlag != 0:
				// A keyword used as a method name, handled like a string.
				name = p.token.Data.Ident
				p.next(0)
			default:
				p.failDueToUnexpectedToken()
			}
			break
		}

		var property TreeProperty
		switch {
		case isGetter || isSetter:
			if p.match(PRIVATENAME) {
				name = p.token.Data.Ident
				var result DeclarationResult
				if isSetter {
					result = classScope.scope.declarePrivateSetter(name, tag)
				} else {
					result = classScope.scope.declarePrivateGetter(name, tag)
				}
				p.semanticFailIfTrue(result&DeclarationInvalidDuplicateDeclaration != 0, "Declared private setter with an already used name")
				kind, other := "getter", "setter"
				if isSetter {
					kind, other = "setter", "getter"
				}
				if tag == ClassElementTagStatic {
					p.semanticFailIfTrue(result&DeclarationInvalidPrivateStaticNonStatic != 0, "Cannot declare a private static ", kind, " if there is a non-static private ", other, " with used name")
					declaresStaticPrivateAccessor = true
				} else {
					p.semanticFailIfTrue(result&DeclarationInvalidPrivateStaticNonStatic != 0, "Cannot declare a private non-static ", kind, " if there is a static private ", other, " with used name")
					declaresPrivateAccessor = true
				}
				if isSetter {
					propertyType |= PropertyPrivateSetter
				} else {
					propertyType |= PropertyPrivateGetter
				}
			} else {
				propertyType &^= PropertyConstant
				if isGetter {
					propertyType |= PropertyGetter
				} else {
					propertyType |= PropertySetter
				}
			}
			property = p.parseGetterSetter(ctx, propertyType, functionStart, ConstructorKindNone, tag)
			p.failIfFalse(property != nil, "Cannot parse this method")

		case !p.match(OPENPAREN) && parseMode == MethodMode:
			// A class field.
			if !name.IsNull() {
				p.semanticFailIfTrue(name == ident.Constructor, "Cannot declare class field named 'constructor'")
				p.semanticFailIfTrue(name == ident.ConstructorPrivateField, "Cannot declare private class field named '#constructor'")
				if tag == ClassElementTagStatic {
					p.semanticFailIfTrue(name == ident.Prototype, "Cannot declare a static field named 'prototype'")
				}
			}

			if computedPropertyName != nil {
				if tag == ClassElementTagInstance {
					name = ident.PrivateSymbol(instanceComputedNamePrefix + strconv.Itoa(nextInstanceComputedFieldID))
					nextInstanceComputedFieldID++
				} else {
					name = ident.PrivateSymbol(staticComputedNamePrefix + strconv.Itoa(nextStaticComputedFieldID))
					nextStaticComputedFieldID++
				}
				classScope.scope.declareLexicalVariable(name, true, DeclarationNotImported, false, false)
				classScope.scope.useVariable(name, false)
				classScope.scope.addClosedVariableCandidateUnconditionally(name)
			}

			var initializer TreeExpression
			if p.consume(EQUAL, 0) {
				usedVariablesSize := p.currentScope.currentUsedVariablesSize()
				p.currentScope.pushUsedVariableSet()
				func() {
					defer setForScope(&p.parserState.isParsingClassFieldInitializer, true)()
					defer setForScope(&p.parserState.classFieldInitMasksAsync, true)()
					classScope.scope.expectedSuperBinding = SuperBindingNeeded
					initializer = p.parseAssignmentExpression(ctx)
					classScope.scope.expectedSuperBinding = SuperBindingNotNeeded
					p.failIfFalse(initializer != nil, "Cannot parse initializer for class field")
				}()
				classScope.scope.markLastUsedVariablesSetAsCaptured(usedVariablesSize)
			}
			p.failIfFalse(p.autoSemiColon(), "Expected a ';' following a class field")
			inferName := InferNameDisallowed
			if initializer != nil {
				inferName = InferNameAllowed
			}
			if computedPropertyName != nil {
				property = ctx.createNamedComputedProperty(name, computedPropertyName, initializer, propertyType, SuperBindingNotNeeded, tag)
			} else {
				property = ctx.createProperty(name, initializer, propertyType, SuperBindingNotNeeded, inferName, tag)
			}

		case parseMode == ClassStaticBlockMode:
			p.matchOrFail(OPENBRACE, "Expected block statement for class static block")
			usedVariablesSize := p.currentScope.currentUsedVariablesSize()
			p.currentScope.pushUsedVariableSet()
			func() {
				defer saveForScope(&p.statementDepth)()
				p.statementDepth = 0
				p.failIfFalse(p.parseBlockStatement(ctx, blockTypeStaticBlock) != nil, "Cannot parse class static block")
			}()
			name = ident.StaticInitializerBlockPrivateName
			property = ctx.createPropertyWithoutValue(name, propertyType, SuperBindingNeeded, tag)
			classScope.scope.markLastUsedVariablesSetAsCaptured(usedVariablesSize)

		default:
			var methodInfo ParserFunctionInfo
			isConstructor := tag == ClassElementTagInstance && name == ident.Constructor
			if isConstructor && parseMode != MethodMode {
				p.semanticFail("Cannot declare ", stringArticleForFunctionMode(parseMode), stringForFunctionMode(parseMode), " named 'constructor'")
			}

			methodInfo.Name = name
			if isConstructor {
				methodInfo.Name = info.ClassName
			}
			kind := ConstructorKindNone
			if isConstructor {
				kind = constructorKind
			}
			func() {
				defer setForScope(&p.parseMode, parseMode)()
				p.failIfFalse(p.parseFunctionInfo(ctx, FunctionNameRequirementsUnnamed, false, kind, SuperBindingNeeded, functionStart, &methodInfo, functionDefinitionMethod, -1), "Cannot parse this method")
			}()

			method := ctx.createMethodDefinition(methodLocation, &methodInfo)
			if isConstructor {
				p.semanticFailIfTrue(constructor != nil, "Cannot declare multiple constructors in a single class")
				constructor = method
				continue
			}

			p.semanticFailIfTrue(tag == ClassElementTagStatic && methodInfo.Name == ident.Prototype, "Cannot declare a static method named 'prototype'")

			if computedPropertyName != nil {
				property = ctx.createComputedProperty(computedPropertyName, method, propertyType, SuperBindingNeeded, tag)
			} else {
				property = ctx.createProperty(methodInfo.Name, method, propertyType, SuperBindingNeeded, InferNameAllowed, tag)
			}
		}

		if classElementsTail != nil {
			classElementsTail = ctx.createPropertyList(methodLocation, property, classElementsTail)
		} else {
			classElements = ctx.createPropertyList(methodLocation, property, nil)
			classElementsTail = classElements
		}
	}

	info.EndOffset = p.tokenLocation().EndOffset - 1
	p.consumeOrFail(CLOSEBRACE, "Expected a closing '}' after a class body")

	if declaresPrivateMethod || declaresPrivateAccessor || declaresStaticPrivateMethod || declaresStaticPrivateAccessor {
		for _, brand := range []ident.Identifier{ident.PrivateBrandPrivateName, ident.PrivateClassBrandPrivateName} {
			classScope.scope.declareLexicalVariable(brand, true, DeclarationNotImported, false, false)
			classScope.scope.useVariable(brand, false)
			classScope.scope.addClosedVariableCandidateUnconditionally(brand)
		}
	}

	if classElements != nil {
		ctx.setHasPrivateAccessors(classElements, declaresPrivateAccessor || declaresStaticPrivateAccessor)
	}

	lexicalEnvironment, _ := classScope.pop(ctx.needsFreeVariableInfo())
	classHeadEnvironment, _ := classHeadScope.pop(ctx.needsFreeVariableInfo())
	return ctx.createClassExpr(location, info, classHeadEnvironment, lexicalEnvironment, constructor, parentClass, classElements, start, divot, classHeadEnd)
}

func (p *Parser) parseClassFieldInitializerSourceElements(ctx treeBuilder, definitions []ClassElementDefinition) TreeSourceElements {
	elements := ctx.createSourceElements()
	p.currentScope.isClassScope = true

	// Clear errors from parsing anything before the initializer expressions.
	p.lexer.ClearErrorCodeAndBuffers()

	for _, definition := range definitions {
		position := definition.Position
		var statement TreeStatement
		if definition.Kind == ClassElementStaticInitializationBlock {
			loc := TokenLocation{StartOffset: int(position), EndOffset: int(position)}
			p.restoreLexerState(lexerState{startOffset: int(position), lastTokenLocation: loc, lastTokenType: ERRORTOK})
			startLocation := p.tokenLocation()
			startPosition := p.tokenStartPosition()
			expressionStart := p.tokenStart()

			p.next(0) // static

			info := ParserFunctionInfo{Name: ident.Null}
			func() {
				defer setForScope(&p.parseMode, ClassStaticBlockMode)()
				p.failIfFalse(p.parseFunctionInfo(ctx, FunctionNameRequirementsNone, false, ConstructorKindNone, SuperBindingNeeded, expressionStart, &info, functionDefinitionExpression, -1), "Cannot parse static block function")
			}()
			e := ctx.createFunctionExpr(startLocation, &info)
			e = ctx.makeStaticBlockFunctionCallNode(startLocation, e, p.lastTokenEndPosition(), startPosition, p.lastTokenEndPosition())
			statement = ctx.createExprStatement(startLocation, e, startPosition)
		} else {
			location := TokenLocation{StartOffset: int(position)}
			var initializer TreeExpression
			if definition.InitializerPosition.IsSet() {
				loc := TokenLocation{StartOffset: int(definition.InitializerPosition), EndOffset: int(definition.InitializerPosition)}
				p.restoreLexerState(lexerState{startOffset: int(definition.InitializerPosition), lastTokenLocation: loc, lastTokenType: ERRORTOK})
				// parseExpression is deliberately more permissive than the
				// parseAssignmentExpression used by parseClass: `foo = (1, 2)` must
				// reparse, and its open paren has already been skipped.
				initializer = p.parseExpression(ctx)
				p.failIfFalse(initializer != nil, "Cannot parse expression statement")
			}

			t := DefineFieldName
			switch definition.Kind {
			case ClassElementFieldWithComputedPropertyKey:
				t = DefineFieldComputedName
			case ClassElementFieldWithPrivatePropertyKey:
				t = DefineFieldPrivateName
				p.currentScope.useVariable(definition.Ident, false)
			}
			statement = ctx.createDefineField(location, definition.Ident, initializer, t)
		}
		ctx.appendStatement(elements, statement)
	}

	// Make parseInner believe the entire SourceCode was parsed.
	p.token.Type = EOFTOK
	return elements
}

func (p *Parser) parsePropertyMethod(ctx treeBuilder, methodName ident.Identifier, functionStart int) TreeExpression {
	methodLocation := p.tokenLocation()
	info := ParserFunctionInfo{Name: methodName}
	p.failIfFalse(p.parseFunctionInfo(ctx, FunctionNameRequirementsUnnamed, false, ConstructorKindNone, SuperBindingNeeded, functionStart, &info, functionDefinitionMethod, -1), "Cannot parse this method")
	return ctx.createMethodDefinition(methodLocation, &info)
}

func (p *Parser) parseGetterSetter(ctx treeBuilder, t PropertyType, getterOrSetterStartOffset int, constructorKind ConstructorKind, tag ClassElementTag) TreeProperty {
	stringPropertyName := ident.Null
	numericPropertyName := 0.0
	var computedPropertyName TreeExpression

	location := p.tokenLocation()

	matchesPrivateName := p.match(PRIVATENAME)
	switch {
	case p.matchSpecIdentifier() || p.match(STRING) || matchesPrivateName || p.token.Type&KeywordTokenFlag != 0:
		stringPropertyName = p.token.Data.Ident
		p.semanticFailIfTrue(tag == ClassElementTagStatic && stringPropertyName == ident.Prototype, "Cannot declare a static method named 'prototype'")
		p.semanticFailIfTrue(tag == ClassElementTagInstance && stringPropertyName == ident.Constructor, "Cannot declare a getter or setter named 'constructor'")
		p.semanticFailIfTrue(stringPropertyName == ident.ConstructorPrivateField, "Cannot declare a private accessor named '#constructor'")
		if p.match(PRIVATENAME) {
			p.semanticFailIfTrue(tag == ClassElementTagNo, "Cannot declare a private setter or getter outside a class")
		}
		p.next(0)
	case p.match(DOUBLE) || p.match(INTEGER):
		numericPropertyName = p.token.Data.Double
		p.next(0)
	case p.match(BIGINT):
		var ok bool
		stringPropertyName, ok = bigIntToDecimal(p.token.Data.BigIntString, p.token.Data.Radix)
		p.failIfFalse(ok, "Cannot parse big int property name")
		p.next(0)
	case p.consume(OPENBRACKET, 0):
		computedPropertyName = p.parseAssignmentExpression(ctx)
		p.failIfFalse(computedPropertyName != nil, "Cannot parse computed property name")
		p.handleProductionOrFail(CLOSEBRACKET, "]", "end", "computed property name")
	default:
		p.failDueToUnexpectedToken()
	}

	var info ParserFunctionInfo
	parseAccessor := func(mode SourceParseMode, listMsg, parseMsg string) {
		p.failIfFalse(p.match(OPENPAREN), listMsg)
		defer setForScope(&p.parseMode, mode)()
		p.failIfFalse(p.parseFunctionInfo(ctx, FunctionNameRequirementsUnnamed, false, constructorKind, SuperBindingNeeded, getterOrSetterStartOffset, &info, functionDefinitionMethod, -1), parseMsg)
	}
	switch {
	case t&PropertyGetter != 0:
		parseAccessor(GetterMode, "Expected a parameter list for getter definition", "Cannot parse getter definition")
	case t&PropertySetter != 0:
		parseAccessor(SetterMode, "Expected a parameter list for setter definition", "Cannot parse setter definition")
	case t&PropertyPrivateSetter != 0:
		parseAccessor(SetterMode, "Expected a parameter list for private setter definition", "Cannot parse private setter definition")
	case t&PropertyPrivateGetter != 0:
		parseAccessor(GetterMode, "Expected a parameter list for private getter definition", "Cannot parse private getter definition")
	}

	if !stringPropertyName.IsNull() {
		return ctx.createGetterOrSetterProperty(location, t, stringPropertyName, &info, tag)
	}
	if computedPropertyName != nil {
		return ctx.createComputedGetterOrSetterProperty(location, t|PropertyComputed, computedPropertyName, &info, tag)
	}
	return ctx.createNumericGetterOrSetterProperty(location, t, numericPropertyName, &info, tag)
}

func (p *Parser) parseClassExpression(ctx treeBuilder) TreeExpression {
	defer saveForScope(&p.parserState.nonLHSCount)()
	info := ParserClassInfo{ClassName: ident.Null}
	return p.parseClass(ctx, FunctionNameRequirementsNone, &info)
}

func (p *Parser) parseFunctionExpression(ctx treeBuilder) TreeExpression {
	defer saveForScope(&p.parserState.nonLHSCount)()
	location := p.tokenLocation()
	functionStart := p.tokenStart()
	p.next(0)
	info := ParserFunctionInfo{Name: ident.Null}
	parseMode := NormalFunctionMode
	if p.consume(TIMES, 0) {
		parseMode = GeneratorWrapperFunctionMode
	}
	defer setForScope(&p.parseMode, parseMode)()

	constructorKind := ConstructorKindNone
	if p.currentScope.isGlobalCode {
		constructorKind = p.constructorKindForTopLevelFunctionExpressions
	}
	expectedSuperBinding := SuperBindingNotNeeded
	if constructorKind == ConstructorKindExtends {
		expectedSuperBinding = SuperBindingNeeded
	}

	p.failIfFalse(p.parseFunctionInfo(ctx, FunctionNameRequirementsNone, false, constructorKind, expectedSuperBinding, functionStart, &info, functionDefinitionExpression, -1), "Cannot parse function expression")
	return ctx.createFunctionExpr(location, &info)
}

func (p *Parser) parseAsyncFunctionExpression(ctx treeBuilder, location TokenLocation) TreeExpression {
	p.next(0)
	parseMode := AsyncFunctionMode
	if p.consume(TIMES, 0) {
		parseMode = AsyncGeneratorWrapperFunctionMode
	}
	defer setForScope(&p.parseMode, parseMode)()

	info := ParserFunctionInfo{Name: ident.Null}
	msg := "Cannot parse async generator function expression"
	if parseMode == AsyncFunctionMode {
		msg = "Cannot parse async function expression"
	}
	p.failIfFalse(p.parseFunctionInfo(ctx, FunctionNameRequirementsNone, false, ConstructorKindNone, SuperBindingNotNeeded, location.StartOffset, &info, functionDefinitionExpression, -1), msg)
	return ctx.createFunctionExpr(location, &info)
}

func (p *Parser) parseArrowFunctionExpression(ctx treeBuilder, isAsync bool, location TokenLocation) TreeExpression {
	info := ParserFunctionInfo{Name: ident.Null}
	mode := ArrowFunctionMode
	if isAsync {
		mode = AsyncArrowFunctionMode
	}
	defer setForScope(&p.parseMode, mode)()
	p.failIfFalse(p.parseFunctionInfo(ctx, FunctionNameRequirementsUnnamed, true, ConstructorKindNone, SuperBindingNotNeeded, location.StartOffset, &info, functionDefinitionExpression, -1), "Cannot parse arrow function expression")
	return ctx.createArrowFunctionExpr(location, &info)
}
