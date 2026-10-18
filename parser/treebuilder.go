package parser

import "github.com/baslentfert/jsc2go/ident"

// The parser is written once against treeBuilder and runs with two builders,
// exactly like JSC's Parser<TreeBuilder> template:
//
//   - astBuilder (ASTBuilder.h) builds the AST.
//   - syntaxChecker (SyntaxChecker.h) only validates; it is used for nested
//     function bodies, which are reparsed lazily when first executed, and for
//     speculative lookahead.
//
// Values flowing through the builder are interfaces. A nil value means
// failure / "no node", like 0 in the C++ code.

// TreeExpression is an expression produced by a builder.
type TreeExpression interface{ treeExpression() }

// TreeStatement is a statement produced by a builder.
type TreeStatement interface{ treeStatement() }

// TreeDestructuringPattern is a binding or assignment pattern.
type TreeDestructuringPattern interface{ treeDestructuringPattern() }

// TreeSourceElements is a statement list.
type TreeSourceElements interface{ treeSourceElements() }

// TreeFormalParameterList is a function's parameter list.
type TreeFormalParameterList interface{ treeFormalParameterList() }

// TreeFunctionBody is a function's metadata.
type TreeFunctionBody interface{ treeFunctionBody() }

// TreeProperty is an object literal property or class element.
type TreeProperty interface{ treeProperty() }

// Less frequently used tree values are passed as `any`; each builder knows
// the concrete types it produced.
type (
	TreeArguments              = any
	TreeArgumentsList          = any
	TreePropertyList           = any
	TreeElementList            = any
	TreeTemplateStringList     = any
	TreeTemplateExpressionList = any
	TreeClause                 = any
	TreeClauseList             = any
	TreeModuleName             = any
	TreeImportSpecifier        = any
	TreeImportSpecifierList    = any
	TreeImportAttributesList   = any
	TreeExportSpecifier        = any
	TreeExportSpecifierList    = any
	TreeBinaryOperand          = any
)

// ParserFunctionInfo collects what parseFunctionInfo learns about a function.
type ParserFunctionInfo struct {
	Name           ident.Identifier
	Body           TreeFunctionBody
	ParameterCount int
	FunctionLength int
	StartOffset    int
	EndOffset      int
}

// ParserClassInfo collects what parseClass learns about a class.
type ParserClassInfo struct {
	ClassName   ident.Identifier
	StartOffset int
	EndOffset   int
}

// InferName says whether a property may give its value a function name.
type InferName uint8

const (
	InferNameAllowed InferName = iota
	InferNameDisallowed
)

type treeBuilder interface {
	createsAST() bool
	needsFreeVariableInfo() bool
	canUseFunctionCache() bool
	dontBuildKeywords() LexerFlags
	dontBuildStrings() LexerFlags

	// pushBinaryExprContext / pushUnaryExprContext are JSC's RAII
	// BinaryExprContext / UnaryExprContext; pass what they return to the
	// pop on exit (a closure to restore the state cost an allocation per
	// expression).
	pushBinaryExprContext() (saved int)
	popBinaryExprContext(saved int)
	pushUnaryExprContext() (saved int)
	popUnaryExprContext(saved int)

	features() CodeFeatures
	numConstants() int

	createSourceElements() TreeSourceElements
	appendStatement(TreeSourceElements, TreeStatement)

	makeStaticBlockFunctionCallNode(loc TokenLocation, expr TreeExpression, divot, divotStart, divotEnd TextPosition) TreeExpression
	makeFunctionCallNode(loc TokenLocation, fn TreeExpression, previousBaseWasSuper bool, args TreeArguments, divotStart, divot, divotEnd TextPosition, callOrApplyChildDepth int, isOptionalCall bool) TreeExpression
	createCommaExpr(loc TokenLocation, expr TreeExpression) TreeExpression
	appendToCommaExpr(loc TokenLocation, tail, next TreeExpression) TreeExpression
	makePrefixNode(loc TokenLocation, expr TreeExpression, op Operator, start, divot, end TextPosition) TreeExpression
	makePostfixNode(loc TokenLocation, expr TreeExpression, op Operator, start, divot, end TextPosition) TreeExpression
	makeTypeOfNode(loc TokenLocation, expr TreeExpression, start, divot, end TextPosition) TreeExpression
	makeDeleteNode(loc TokenLocation, expr TreeExpression, start, divot, end TextPosition) TreeExpression
	makeNegateNode(loc TokenLocation, expr TreeExpression) TreeExpression
	makeBitwiseNotNode(loc TokenLocation, expr TreeExpression) TreeExpression
	createLogicalNot(loc TokenLocation, expr TreeExpression) TreeExpression
	createUnaryPlus(loc TokenLocation, expr TreeExpression) TreeExpression
	createVoid(loc TokenLocation, expr TreeExpression) TreeExpression
	createImportExpr(loc TokenLocation, expr, option TreeExpression, deferred bool, start, divot, end TextPosition) TreeExpression
	createThisExpr(loc TokenLocation) TreeExpression
	createSuperExpr(loc TokenLocation) TreeExpression
	createNewTargetExpr(loc TokenLocation) TreeExpression
	createImportMetaExpr(loc TokenLocation, expr TreeExpression) TreeExpression
	setIsParenthesized(TreeExpression)
	isMetaProperty(TreeExpression) bool
	isNewTarget(TreeExpression) bool
	isImportMeta(TreeExpression) bool
	createResolve(loc TokenLocation, name ident.Identifier, start, end TextPosition, needToCheckUsesArguments bool) TreeExpression
	createPrivateIdentifierNode(loc TokenLocation, name ident.Identifier) TreeExpression
	createObjectLiteral(loc TokenLocation, properties TreePropertyList) TreeExpression
	createArray(loc TokenLocation, elisions int, elements TreeElementList) TreeExpression
	createDoubleExpr(loc TokenLocation, d float64) TreeExpression
	createIntegerExpr(loc TokenLocation, d float64) TreeExpression
	createBigInt(loc TokenLocation, value ident.Identifier, radix uint8) TreeExpression
	createString(loc TokenLocation, value ident.Identifier) TreeExpression
	createBoolean(loc TokenLocation, b bool) TreeExpression
	createNull(loc TokenLocation) TreeExpression
	createBracketAccess(loc TokenLocation, base, property TreeExpression, propertyHasAssignments bool, start, divot, end TextPosition) TreeExpression
	createDotAccess(loc TokenLocation, base TreeExpression, property ident.Identifier, t DotType, start, divot, end TextPosition) TreeExpression
	createRegExp(loc TokenLocation, pattern, flags ident.Identifier, start TextPosition, skipSyntaxCheck bool) TreeExpression
	createNewExpr(loc TokenLocation, expr TreeExpression, args TreeArguments, start, divot, end TextPosition) TreeExpression
	createOptionalChain(loc TokenLocation, base, expr TreeExpression, isOutermost bool) TreeExpression
	createConditionalExpr(loc TokenLocation, condition, lhs, rhs TreeExpression) TreeExpression
	createAssignResolve(loc TokenLocation, name ident.Identifier, rhs TreeExpression, start, divot, end TextPosition, ctx AssignmentContext) TreeExpression
	createEmptyVarExpression(loc TokenLocation, name ident.Identifier) TreeExpression
	createEmptyLetExpression(loc TokenLocation, name ident.Identifier) TreeExpression
	createYield(loc TokenLocation, argument TreeExpression, delegate bool, start, divot, end TextPosition) TreeExpression
	createAwait(loc TokenLocation, argument TreeExpression, start, divot, end TextPosition) TreeExpression
	createClassExpr(loc TokenLocation, info *ParserClassInfo, classHeadEnvironment, classEnvironment VariableEnvironment, constructor, parentClass TreeExpression, elements TreePropertyList, start, divot, end TextPosition) TreeExpression
	createFunctionExpr(loc TokenLocation, info *ParserFunctionInfo) TreeExpression
	createGeneratorFunctionBody(loc TokenLocation, info *ParserFunctionInfo, name ident.Identifier) TreeExpression
	createAsyncFunctionBody(loc TokenLocation, info *ParserFunctionInfo, mode SourceParseMode, name ident.Identifier) TreeExpression
	createFunctionMetadata(start, end TokenLocation, functionStart, functionNameStart, parametersStart int, visibility ImplementationVisibility, features LexicallyScopedFeatures, constructorKind ConstructorKind, superBinding SuperBinding, parameterCount int, mode SourceParseMode, isArrowFunctionBodyExpression bool) TreeFunctionBody
	createArrowFunctionExpr(loc TokenLocation, info *ParserFunctionInfo) TreeExpression
	createMethodDefinition(loc TokenLocation, info *ParserFunctionInfo) TreeExpression
	createArguments(list TreeArgumentsList, hasAssignments bool) TreeArguments
	createSpreadExpression(loc TokenLocation, expr TreeExpression, start, divot, end TextPosition) TreeExpression
	createObjectSpreadExpression(loc TokenLocation, expr TreeExpression, start, divot, end TextPosition) TreeExpression
	createTemplateString(loc TokenLocation, cooked, raw ident.Identifier) TreeExpression
	createTemplateStringList(list TreeTemplateStringList, s TreeExpression) TreeTemplateStringList
	createTemplateExpressionList(list TreeTemplateExpressionList, expr TreeExpression) TreeTemplateExpressionList
	createTemplateLiteral(loc TokenLocation, strings TreeTemplateStringList, exprs TreeTemplateExpressionList) TreeExpression
	createTaggedTemplate(loc TokenLocation, base, templateLiteral TreeExpression, start, divot, end TextPosition) TreeExpression
	createArgumentsList(loc TokenLocation, list TreeArgumentsList, arg TreeExpression) TreeArgumentsList

	// createProperty overloads.
	createProperty(name ident.Identifier, value TreeExpression, t PropertyType, sb SuperBinding, inferName InferName, tag ClassElementTag) TreeProperty
	createPropertyWithoutValue(name ident.Identifier, t PropertyType, sb SuperBinding, tag ClassElementTag) TreeProperty
	createComputedProperty(name, value TreeExpression, t PropertyType, sb SuperBinding, tag ClassElementTag) TreeProperty
	createSpreadProperty(value TreeExpression, t PropertyType, sb SuperBinding, tag ClassElementTag) TreeProperty
	createNamedComputedProperty(name ident.Identifier, nameExpr, value TreeExpression, t PropertyType, sb SuperBinding, tag ClassElementTag) TreeProperty
	createGetterOrSetterProperty(loc TokenLocation, t PropertyType, name ident.Identifier, info *ParserFunctionInfo, tag ClassElementTag) TreeProperty
	createComputedGetterOrSetterProperty(loc TokenLocation, t PropertyType, name TreeExpression, info *ParserFunctionInfo, tag ClassElementTag) TreeProperty
	createNumericGetterOrSetterProperty(loc TokenLocation, t PropertyType, name float64, info *ParserFunctionInfo, tag ClassElementTag) TreeProperty
	createPropertyList(loc TokenLocation, property TreeProperty, tail TreePropertyList) TreePropertyList
	setHasPrivateAccessors(list TreePropertyList, has bool)

	createElementList(list TreeElementList, elisions int, expr TreeExpression) TreeElementList
	createElementListFromArguments(args TreeArgumentsList) TreeElementList
	createFormalParameterList() TreeFormalParameterList
	appendParameter(list TreeFormalParameterList, pattern TreeDestructuringPattern, defaultValue TreeExpression)
	createClause(expr TreeExpression, statements TreeSourceElements) TreeClause
	createClauseList(list TreeClauseList, clause TreeClause) TreeClauseList

	createFuncDeclStatement(loc TokenLocation, info *ParserFunctionInfo) TreeStatement
	createDefineField(loc TokenLocation, name ident.Identifier, initializer TreeExpression, t DefineFieldType) TreeStatement
	createClassDeclStatement(loc TokenLocation, classExpr TreeExpression, classStart, classEnd TextPosition) TreeStatement
	createBlockStatement(loc TokenLocation, elements TreeSourceElements, lexicalVariables VariableEnvironment, functionStack FunctionStack) TreeStatement
	createExprStatement(loc TokenLocation, expr TreeExpression, start TextPosition) TreeStatement
	createIfStatement(loc TokenLocation, condition TreeExpression, trueBlock, falseBlock TreeStatement) TreeStatement
	createForLoop(loc TokenLocation, initializer, condition, iter TreeExpression, statements TreeStatement, lexicalVariables VariableEnvironment, initializerContainsClosure bool) TreeStatement
	createForInLoop(loc TokenLocation, lhs, iter TreeExpression, statements TreeStatement, declLoc TokenLocation, eStart, eDivot, eEnd TextPosition, lexicalVariables VariableEnvironment) TreeStatement
	createForInLoopWithPattern(loc TokenLocation, pattern TreeDestructuringPattern, iter TreeExpression, statements TreeStatement, declLoc TokenLocation, eStart, eDivot, eEnd TextPosition, lexicalVariables VariableEnvironment) TreeStatement
	createForOfLoop(isForAwait bool, loc TokenLocation, lhs, iter TreeExpression, statements TreeStatement, declLoc TokenLocation, eStart, eDivot, eEnd TextPosition, lexicalVariables VariableEnvironment) TreeStatement
	createForOfLoopWithPattern(isForAwait bool, loc TokenLocation, pattern TreeDestructuringPattern, iter TreeExpression, statements TreeStatement, declLoc TokenLocation, eStart, eDivot, eEnd TextPosition, lexicalVariables VariableEnvironment) TreeStatement
	createEmptyStatement(loc TokenLocation) TreeStatement
	createDeclarationStatement(loc TokenLocation, expr TreeExpression) TreeStatement
	createReturnStatement(loc TokenLocation, expr TreeExpression, start, end TextPosition) TreeStatement
	createBreakStatement(loc TokenLocation, label ident.Identifier, start, end TextPosition) TreeStatement
	createContinueStatement(loc TokenLocation, label ident.Identifier, start, end TextPosition) TreeStatement
	createTryStatement(loc TokenLocation, tryBlock TreeStatement, catchPattern TreeDestructuringPattern, catchBlock, finallyBlock TreeStatement, catchEnvironment VariableEnvironment) TreeStatement
	createSwitchStatement(loc TokenLocation, expr TreeExpression, firstClauses TreeClauseList, defaultClause TreeClause, secondClauses TreeClauseList, lexicalVariables VariableEnvironment, functionStack FunctionStack) TreeStatement
	createWhileStatement(loc TokenLocation, expr TreeExpression, statement TreeStatement) TreeStatement
	createWithStatement(loc TokenLocation, expr TreeExpression, statement TreeStatement, start int, end TextPosition) TreeStatement
	createDoWhileStatement(loc TokenLocation, statement TreeStatement, expr TreeExpression) TreeStatement
	createLabelStatement(loc TokenLocation, label ident.Identifier, statement TreeStatement, start, end TextPosition) TreeStatement
	createThrowStatement(loc TokenLocation, expr TreeExpression, start, end TextPosition) TreeStatement
	createDebugger(loc TokenLocation) TreeStatement

	createModuleName(loc TokenLocation, name ident.Identifier) TreeModuleName
	createImportSpecifier(loc TokenLocation, importedName, localName ident.Identifier) TreeImportSpecifier
	createImportSpecifierList() TreeImportSpecifierList
	appendImportSpecifier(list TreeImportSpecifierList, specifier TreeImportSpecifier)
	createImportAttributesList() TreeImportAttributesList
	appendImportAttribute(list TreeImportAttributesList, key, value ident.Identifier)
	createImportDeclaration(loc TokenLocation, t ImportType, specifiers TreeImportSpecifierList, moduleName TreeModuleName, attributes TreeImportAttributesList) TreeStatement
	createExportAllDeclaration(loc TokenLocation, moduleName TreeModuleName, attributes TreeImportAttributesList) TreeStatement
	createExportDefaultDeclaration(loc TokenLocation, declaration TreeStatement, localName ident.Identifier) TreeStatement
	createExportLocalDeclaration(loc TokenLocation, declaration TreeStatement) TreeStatement
	createExportNamedDeclaration(loc TokenLocation, specifiers TreeExportSpecifierList, moduleName TreeModuleName, attributes TreeImportAttributesList) TreeStatement
	createExportSpecifier(loc TokenLocation, localName, exportedName ident.Identifier) TreeExportSpecifier
	createExportSpecifierList() TreeExportSpecifierList
	appendExportSpecifier(list TreeExportSpecifierList, specifier TreeExportSpecifier)

	// Binary expression parsing state (operator-precedence stacks).
	appendBinaryExpressionInfo(operandStackDepth int, current TreeExpression, exprStart, lhs, rhs TextPosition, hasAssignments bool) int
	operatorStackPop(operatorStackDepth int) int
	operatorStackShouldReduce(precedence int) bool
	getFromOperandStack(i int) TreeBinaryOperand
	shrinkOperandStackBy(operandStackDepth int, amount int) int
	appendBinaryOperation(loc TokenLocation, operandStackDepth int, lhs, rhs TreeBinaryOperand) int
	operatorStackAppend(operatorStackDepth int, op TokenType, precedence int) int
	popOperandStack() TreeExpression

	// Unary expression parsing state.
	appendUnaryToken(tokenStackDepth int, t TokenType, start TextPosition) int
	unaryTokenStackLastType() TokenType
	unaryTokenStackLastStart() TextPosition
	unaryTokenStackRemoveLast(tokenStackDepth int) int
	unaryTokenStackDepth() int
	setUnaryTokenStackDepth(depth int)

	// Assignment parsing state.
	assignmentStackAppend(assignmentStackDepth int, node TreeExpression, start, divot TextPosition, assignmentCount int, op Operator) int
	createAssignment(loc TokenLocation, assignmentStackDepth int, rhs TreeExpression, initialAssignmentCount, currentAssignmentCount int, lastTokenEnd TextPosition) (TreeExpression, int)

	getType(TreeProperty) PropertyType
	isUnderscoreProtoSetter(TreeProperty) bool
	isResolve(TreeExpression) bool

	createDestructuringAssignment(loc TokenLocation, pattern TreeDestructuringPattern, initializer TreeExpression) TreeExpression
	createArrayPattern(loc TokenLocation) TreeDestructuringPattern
	appendArrayPatternSkipEntry(pattern TreeDestructuringPattern, loc TokenLocation)
	appendArrayPatternEntry(pattern TreeDestructuringPattern, loc TokenLocation, inner TreeDestructuringPattern, defaultValue TreeExpression)
	appendArrayPatternRestEntry(pattern TreeDestructuringPattern, loc TokenLocation, inner TreeDestructuringPattern)
	finishArrayPattern(pattern TreeDestructuringPattern, divotStart, divot, divotEnd TextPosition)
	createObjectPattern(loc TokenLocation) TreeDestructuringPattern
	appendObjectPatternEntry(pattern TreeDestructuringPattern, loc TokenLocation, wasString bool, name ident.Identifier, inner TreeDestructuringPattern, defaultValue TreeExpression)
	appendObjectPatternComputedEntry(pattern TreeDestructuringPattern, loc TokenLocation, propertyExpression TreeExpression, inner TreeDestructuringPattern, defaultValue TreeExpression)
	appendObjectPatternRestEntry(pattern TreeDestructuringPattern, loc TokenLocation, inner TreeDestructuringPattern)
	setContainsObjectRestElement(pattern TreeDestructuringPattern, contains bool)
	setContainsComputedProperty(pattern TreeDestructuringPattern, contains bool)
	finishObjectPattern(pattern TreeDestructuringPattern, divotStart, divot, divotEnd TextPosition)
	createBindingLocation(loc TokenLocation, name ident.Identifier, start, end TextPosition, ctx AssignmentContext) TreeDestructuringPattern
	createRestParameter(pattern TreeDestructuringPattern, numParametersToSkip int) TreeDestructuringPattern
	createAssignmentElement(target TreeExpression, start, end TextPosition) TreeDestructuringPattern

	isBindingNode(TreeDestructuringPattern) bool
	isLocation(TreeExpression) bool
	isPrivateLocation(TreeExpression) bool
	isAssignmentLocation(TreeExpression) bool
	isObjectLiteral(TreeExpression) bool
	isArrayLiteral(TreeExpression) bool
	isObjectOrArrayLiteral(TreeExpression) bool
	isFunctionCall(TreeExpression) bool
	shouldSkipPauseLocation(TreeStatement) bool

	// Offsets of statements, expressions, function bodies and case clauses.
	setEndOffset(node any, offset int)
	endOffset(node any) int
	setStartOffset(node any, offset int)
	breakpointLocation(node any) TextPosition

	propagateArgumentsUse()
	hasArgumentsFeature() bool
}
