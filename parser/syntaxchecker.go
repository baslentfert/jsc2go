package parser

import "github.com/baslentfert/jsc2go/ident"

// syntaxChecker is the port of JavaScriptCore/parser/SyntaxChecker.h. It
// builds nothing: every "node" is a small integer describing what kind of
// expression it was, which is all the parser needs for its checks.
type syntaxChecker struct {
	topBinaryExpr syntaxResult
	topUnaryToken TokenType
}

// syntaxResult is SyntaxChecker's ExpressionType. The zero value is never
// produced; failure is a nil interface, as in the AST builder.
type syntaxResult int

const metaPropertyBit syntaxResult = 1 << 30

const (
	scNone syntaxResult = iota
	scResolveEvalExpr
	scResolveExpr
	scIntegerExpr
	scDoubleExpr
	scStringExpr
	scBigIntExpr
	scThisExpr
	scNullExpr
	scBoolExpr
	scRegExpExpr
	scObjectLiteralExpr
	scFunctionExpr
	scClassExpr
	scSuperExpr
	scImportExpr
	scBracketExpr
	scDotExpr
	scCallExpr
	scNewExpr
	scPreExpr
	scPostExpr
	scUnaryExpr
	scBinaryExpr
	scOptionalChain
	scPrivateDotExpr
	scConditionalExpr
	scAssignmentExpr
	scTypeofExpr
	scDeleteExpr
	scArrayLiteralExpr
	scBindingDestructuring
	scRestParameter
	scArrayDestructuring
	scObjectDestructuring
	scSourceElementsResult
	scFunctionBodyResult
	scSpreadExpr
	scObjectSpreadExpr
	scArgumentsResult
	scPropertyListResult
	scArgumentsListResult
	scElementsListResult
	scStatementResult
	scFormalParameterListResult
	scClauseResult
	scClauseListResult
	scCommaExpr
	scDestructuringAssignment
	scTemplateStringResult
	scTemplateStringListResult
	scTemplateExpressionListResult
	scTemplateExpr
	scTaggedTemplateExpr
	scYieldExpr
	scAwaitExpr
	scModuleNameResult
	scPrivateIdentifier
	scImportSpecifierResult
	scImportSpecifierListResult
	scImportAttributesListResult
	scExportSpecifierResult
	scExportSpecifierListResult

	scNewTargetExpr  = metaPropertyBit | 0
	scImportMetaExpr = metaPropertyBit | 1
)

func (syntaxResult) treeExpression()           {}
func (syntaxResult) treeStatement()            {}
func (syntaxResult) treeDestructuringPattern() {}
func (syntaxResult) treeSourceElements()       {}
func (syntaxResult) treeFormalParameterList()  {}
func (syntaxResult) treeFunctionBody()         {}

// syntaxProperty is SyntaxChecker::Property.
type syntaxProperty struct {
	t                       PropertyType
	isUnderscoreProtoSetter bool
}

func (syntaxProperty) treeProperty() {}

// sc returns a syntax result as a builder value; scNone (0) becomes nil.
func sc(r syntaxResult) TreeExpression {
	if r == scNone {
		return nil
	}
	return r
}

func scOf(e any) syntaxResult {
	r, _ := e.(syntaxResult)
	return r
}

var _ treeBuilder = (*syntaxChecker)(nil)

func (*syntaxChecker) createsAST() bool              { return false }
func (*syntaxChecker) needsFreeVariableInfo() bool   { return false }
func (*syntaxChecker) canUseFunctionCache() bool     { return true }
func (*syntaxChecker) dontBuildKeywords() LexerFlags { return LexerDontBuildKeywords }
func (*syntaxChecker) dontBuildStrings() LexerFlags  { return LexerDontBuildStrings }

func (c *syntaxChecker) pushBinaryExprContext() int {
	saved := c.topBinaryExpr
	c.topBinaryExpr = scNone
	return int(saved)
}

func (c *syntaxChecker) popBinaryExprContext(saved int) { c.topBinaryExpr = syntaxResult(saved) }

func (c *syntaxChecker) pushUnaryExprContext() int {
	saved := c.topUnaryToken
	c.topUnaryToken = 0
	return int(saved)
}

func (c *syntaxChecker) popUnaryExprContext(saved int) { c.topUnaryToken = TokenType(saved) }

func (*syntaxChecker) features() CodeFeatures { return 0 }
func (*syntaxChecker) numConstants() int      { return 0 }

func (*syntaxChecker) createSourceElements() TreeSourceElements          { return scSourceElementsResult }
func (*syntaxChecker) appendStatement(TreeSourceElements, TreeStatement) {}

func (*syntaxChecker) makeStaticBlockFunctionCallNode(TokenLocation, TreeExpression, TextPosition, TextPosition, TextPosition) TreeExpression {
	return scCallExpr
}
func (*syntaxChecker) makeFunctionCallNode(TokenLocation, TreeExpression, bool, TreeArguments, TextPosition, TextPosition, TextPosition, int, bool) TreeExpression {
	return scCallExpr
}
func (*syntaxChecker) createCommaExpr(TokenLocation, TreeExpression) TreeExpression {
	return scCommaExpr
}
func (*syntaxChecker) appendToCommaExpr(TokenLocation, TreeExpression, TreeExpression) TreeExpression {
	return scCommaExpr
}
func (*syntaxChecker) makePrefixNode(TokenLocation, TreeExpression, Operator, TextPosition, TextPosition, TextPosition) TreeExpression {
	return scPreExpr
}
func (*syntaxChecker) makePostfixNode(TokenLocation, TreeExpression, Operator, TextPosition, TextPosition, TextPosition) TreeExpression {
	return scPostExpr
}
func (*syntaxChecker) makeTypeOfNode(TokenLocation, TreeExpression, TextPosition, TextPosition, TextPosition) TreeExpression {
	return scTypeofExpr
}
func (*syntaxChecker) makeDeleteNode(TokenLocation, TreeExpression, TextPosition, TextPosition, TextPosition) TreeExpression {
	return scDeleteExpr
}
func (*syntaxChecker) makeNegateNode(TokenLocation, TreeExpression) TreeExpression {
	return scUnaryExpr
}
func (*syntaxChecker) makeBitwiseNotNode(TokenLocation, TreeExpression) TreeExpression {
	return scUnaryExpr
}
func (*syntaxChecker) createLogicalNot(TokenLocation, TreeExpression) TreeExpression {
	return scUnaryExpr
}
func (*syntaxChecker) createUnaryPlus(TokenLocation, TreeExpression) TreeExpression {
	return scUnaryExpr
}
func (*syntaxChecker) createVoid(TokenLocation, TreeExpression) TreeExpression { return scUnaryExpr }
func (*syntaxChecker) createImportExpr(TokenLocation, TreeExpression, TreeExpression, bool, TextPosition, TextPosition, TextPosition) TreeExpression {
	return scImportExpr
}
func (*syntaxChecker) createThisExpr(TokenLocation) TreeExpression      { return scThisExpr }
func (*syntaxChecker) createSuperExpr(TokenLocation) TreeExpression     { return scSuperExpr }
func (*syntaxChecker) createNewTargetExpr(TokenLocation) TreeExpression { return scNewTargetExpr }
func (*syntaxChecker) createImportMetaExpr(TokenLocation, TreeExpression) TreeExpression {
	return scImportMetaExpr
}
func (*syntaxChecker) setIsParenthesized(TreeExpression)    {}
func (*syntaxChecker) isMetaProperty(e TreeExpression) bool { return scOf(e)&metaPropertyBit != 0 }
func (*syntaxChecker) isNewTarget(e TreeExpression) bool    { return scOf(e) == scNewTargetExpr }
func (*syntaxChecker) isImportMeta(e TreeExpression) bool   { return scOf(e) == scImportMetaExpr }
func (*syntaxChecker) createResolve(TokenLocation, ident.Identifier, TextPosition, TextPosition, bool) TreeExpression {
	return scResolveExpr
}
func (*syntaxChecker) createPrivateIdentifierNode(TokenLocation, ident.Identifier) TreeExpression {
	return scPrivateIdentifier
}
func (*syntaxChecker) createObjectLiteral(TokenLocation, TreePropertyList) TreeExpression {
	return scObjectLiteralExpr
}
func (*syntaxChecker) createArray(TokenLocation, int, TreeElementList) TreeExpression {
	return scArrayLiteralExpr
}
func (*syntaxChecker) createDoubleExpr(TokenLocation, float64) TreeExpression  { return scDoubleExpr }
func (*syntaxChecker) createIntegerExpr(TokenLocation, float64) TreeExpression { return scIntegerExpr }
func (*syntaxChecker) createBigInt(TokenLocation, ident.Identifier, uint8) TreeExpression {
	return scBigIntExpr
}
func (*syntaxChecker) createString(TokenLocation, ident.Identifier) TreeExpression {
	return scStringExpr
}
func (*syntaxChecker) createBoolean(TokenLocation, bool) TreeExpression { return scBoolExpr }
func (*syntaxChecker) createNull(TokenLocation) TreeExpression          { return scNullExpr }
func (*syntaxChecker) createBracketAccess(TokenLocation, TreeExpression, TreeExpression, bool, TextPosition, TextPosition, TextPosition) TreeExpression {
	return scBracketExpr
}
func (*syntaxChecker) createDotAccess(_ TokenLocation, _ TreeExpression, _ ident.Identifier, t DotType, _, _, _ TextPosition) TreeExpression {
	if t == DotTypePrivateMember {
		return scPrivateDotExpr
	}
	return scDotExpr
}
func (*syntaxChecker) createRegExp(_ TokenLocation, pattern, flags ident.Identifier, _ TextPosition, skipSyntaxCheck bool) TreeExpression {
	if !skipSyntaxCheck && RegExpSyntaxChecker(pattern.String(), flags.String()) != "" {
		return nil
	}
	return scRegExpExpr
}
func (*syntaxChecker) createNewExpr(TokenLocation, TreeExpression, TreeArguments, TextPosition, TextPosition, TextPosition) TreeExpression {
	return scNewExpr
}
func (*syntaxChecker) createOptionalChain(TokenLocation, TreeExpression, TreeExpression, bool) TreeExpression {
	return scOptionalChain
}
func (*syntaxChecker) createConditionalExpr(TokenLocation, TreeExpression, TreeExpression, TreeExpression) TreeExpression {
	return scConditionalExpr
}
func (*syntaxChecker) createAssignResolve(TokenLocation, ident.Identifier, TreeExpression, TextPosition, TextPosition, TextPosition, AssignmentContext) TreeExpression {
	return scAssignmentExpr
}
func (*syntaxChecker) createEmptyVarExpression(TokenLocation, ident.Identifier) TreeExpression {
	return scAssignmentExpr
}
func (*syntaxChecker) createEmptyLetExpression(TokenLocation, ident.Identifier) TreeExpression {
	return scAssignmentExpr
}
func (*syntaxChecker) createYield(TokenLocation, TreeExpression, bool, TextPosition, TextPosition, TextPosition) TreeExpression {
	return scYieldExpr
}
func (*syntaxChecker) createAwait(TokenLocation, TreeExpression, TextPosition, TextPosition, TextPosition) TreeExpression {
	return scAwaitExpr
}
func (*syntaxChecker) createClassExpr(TokenLocation, *ParserClassInfo, VariableEnvironment, VariableEnvironment, TreeExpression, TreeExpression, TreePropertyList, TextPosition, TextPosition, TextPosition) TreeExpression {
	return scClassExpr
}
func (*syntaxChecker) createFunctionExpr(TokenLocation, *ParserFunctionInfo) TreeExpression {
	return scFunctionExpr
}
func (*syntaxChecker) createGeneratorFunctionBody(TokenLocation, *ParserFunctionInfo, ident.Identifier) TreeExpression {
	return scFunctionExpr
}
func (*syntaxChecker) createAsyncFunctionBody(TokenLocation, *ParserFunctionInfo, SourceParseMode, ident.Identifier) TreeExpression {
	return scFunctionExpr
}
func (*syntaxChecker) createFunctionMetadata(TokenLocation, TokenLocation, int, int, int, ImplementationVisibility, LexicallyScopedFeatures, ConstructorKind, SuperBinding, int, SourceParseMode, bool) TreeFunctionBody {
	return scFunctionBodyResult
}
func (*syntaxChecker) createArrowFunctionExpr(TokenLocation, *ParserFunctionInfo) TreeExpression {
	return scFunctionExpr
}
func (*syntaxChecker) createMethodDefinition(TokenLocation, *ParserFunctionInfo) TreeExpression {
	return scFunctionExpr
}
func (*syntaxChecker) createArguments(TreeArgumentsList, bool) TreeArguments {
	return scArgumentsResult
}
func (*syntaxChecker) createSpreadExpression(TokenLocation, TreeExpression, TextPosition, TextPosition, TextPosition) TreeExpression {
	return scSpreadExpr
}
func (*syntaxChecker) createObjectSpreadExpression(TokenLocation, TreeExpression, TextPosition, TextPosition, TextPosition) TreeExpression {
	return scObjectSpreadExpr
}
func (*syntaxChecker) createTemplateString(TokenLocation, ident.Identifier, ident.Identifier) TreeExpression {
	return scTemplateStringResult
}
func (*syntaxChecker) createTemplateStringList(TreeTemplateStringList, TreeExpression) TreeTemplateStringList {
	return scTemplateStringListResult
}
func (*syntaxChecker) createTemplateExpressionList(TreeTemplateExpressionList, TreeExpression) TreeTemplateExpressionList {
	return scTemplateExpressionListResult
}
func (*syntaxChecker) createTemplateLiteral(TokenLocation, TreeTemplateStringList, TreeTemplateExpressionList) TreeExpression {
	return scTemplateExpr
}
func (*syntaxChecker) createTaggedTemplate(TokenLocation, TreeExpression, TreeExpression, TextPosition, TextPosition, TextPosition) TreeExpression {
	return scTaggedTemplateExpr
}
func (*syntaxChecker) createArgumentsList(TokenLocation, TreeArgumentsList, TreeExpression) TreeArgumentsList {
	return scArgumentsListResult
}

func (*syntaxChecker) createProperty(name ident.Identifier, _ TreeExpression, t PropertyType, sb SuperBinding, _ InferName, tag ClassElementTag) TreeProperty {
	return syntaxProperty{t, isUnderscoreProtoSetter(name, t, sb == SuperBindingNeeded, tag != ClassElementTagNo)}
}
func (*syntaxChecker) createPropertyWithoutValue(_ ident.Identifier, t PropertyType, _ SuperBinding, _ ClassElementTag) TreeProperty {
	return syntaxProperty{t: t}
}
func (*syntaxChecker) createComputedProperty(_, _ TreeExpression, t PropertyType, _ SuperBinding, _ ClassElementTag) TreeProperty {
	return syntaxProperty{t: t}
}
func (*syntaxChecker) createSpreadProperty(_ TreeExpression, t PropertyType, _ SuperBinding, _ ClassElementTag) TreeProperty {
	return syntaxProperty{t: t}
}
func (*syntaxChecker) createNamedComputedProperty(_ ident.Identifier, _, _ TreeExpression, t PropertyType, _ SuperBinding, _ ClassElementTag) TreeProperty {
	return syntaxProperty{t: t}
}
func (*syntaxChecker) createGetterOrSetterProperty(_ TokenLocation, t PropertyType, _ ident.Identifier, _ *ParserFunctionInfo, _ ClassElementTag) TreeProperty {
	return syntaxProperty{t: t}
}
func (*syntaxChecker) createComputedGetterOrSetterProperty(_ TokenLocation, t PropertyType, _ TreeExpression, _ *ParserFunctionInfo, _ ClassElementTag) TreeProperty {
	return syntaxProperty{t: t}
}
func (*syntaxChecker) createNumericGetterOrSetterProperty(_ TokenLocation, t PropertyType, _ float64, _ *ParserFunctionInfo, _ ClassElementTag) TreeProperty {
	return syntaxProperty{t: t}
}
func (*syntaxChecker) createPropertyList(TokenLocation, TreeProperty, TreePropertyList) TreePropertyList {
	return scPropertyListResult
}
func (*syntaxChecker) setHasPrivateAccessors(TreePropertyList, bool) {}

func (*syntaxChecker) createElementList(TreeElementList, int, TreeExpression) TreeElementList {
	return scElementsListResult
}
func (*syntaxChecker) createElementListFromArguments(TreeArgumentsList) TreeElementList {
	return scElementsListResult
}
func (*syntaxChecker) createFormalParameterList() TreeFormalParameterList {
	return scFormalParameterListResult
}
func (*syntaxChecker) appendParameter(TreeFormalParameterList, TreeDestructuringPattern, TreeExpression) {
}
func (*syntaxChecker) createClause(TreeExpression, TreeSourceElements) TreeClause {
	return scClauseResult
}
func (*syntaxChecker) createClauseList(TreeClauseList, TreeClause) TreeClauseList {
	return scClauseListResult
}

func (*syntaxChecker) createFuncDeclStatement(TokenLocation, *ParserFunctionInfo) TreeStatement {
	return scStatementResult
}

// createDefineField returns 0 in JSC: class field initializers are only
// parsed with the AST builder.
func (*syntaxChecker) createDefineField(TokenLocation, ident.Identifier, TreeExpression, DefineFieldType) TreeStatement {
	return nil
}
func (*syntaxChecker) createClassDeclStatement(TokenLocation, TreeExpression, TextPosition, TextPosition) TreeStatement {
	return scStatementResult
}
func (*syntaxChecker) createBlockStatement(TokenLocation, TreeSourceElements, VariableEnvironment, FunctionStack) TreeStatement {
	return scStatementResult
}
func (*syntaxChecker) createExprStatement(TokenLocation, TreeExpression, TextPosition) TreeStatement {
	return scStatementResult
}
func (*syntaxChecker) createIfStatement(TokenLocation, TreeExpression, TreeStatement, TreeStatement) TreeStatement {
	return scStatementResult
}
func (*syntaxChecker) createForLoop(TokenLocation, TreeExpression, TreeExpression, TreeExpression, TreeStatement, VariableEnvironment, bool) TreeStatement {
	return scStatementResult
}
func (*syntaxChecker) createForInLoop(TokenLocation, TreeExpression, TreeExpression, TreeStatement, TokenLocation, TextPosition, TextPosition, TextPosition, VariableEnvironment) TreeStatement {
	return scStatementResult
}
func (*syntaxChecker) createForInLoopWithPattern(TokenLocation, TreeDestructuringPattern, TreeExpression, TreeStatement, TokenLocation, TextPosition, TextPosition, TextPosition, VariableEnvironment) TreeStatement {
	return scStatementResult
}
func (*syntaxChecker) createForOfLoop(bool, TokenLocation, TreeExpression, TreeExpression, TreeStatement, TokenLocation, TextPosition, TextPosition, TextPosition, VariableEnvironment) TreeStatement {
	return scStatementResult
}
func (*syntaxChecker) createForOfLoopWithPattern(bool, TokenLocation, TreeDestructuringPattern, TreeExpression, TreeStatement, TokenLocation, TextPosition, TextPosition, TextPosition, VariableEnvironment) TreeStatement {
	return scStatementResult
}
func (*syntaxChecker) createEmptyStatement(TokenLocation) TreeStatement { return scStatementResult }
func (*syntaxChecker) createDeclarationStatement(TokenLocation, TreeExpression) TreeStatement {
	return scStatementResult
}
func (*syntaxChecker) createReturnStatement(TokenLocation, TreeExpression, TextPosition, TextPosition) TreeStatement {
	return scStatementResult
}
func (*syntaxChecker) createBreakStatement(TokenLocation, ident.Identifier, TextPosition, TextPosition) TreeStatement {
	return scStatementResult
}
func (*syntaxChecker) createContinueStatement(TokenLocation, ident.Identifier, TextPosition, TextPosition) TreeStatement {
	return scStatementResult
}
func (*syntaxChecker) createTryStatement(TokenLocation, TreeStatement, TreeDestructuringPattern, TreeStatement, TreeStatement, VariableEnvironment) TreeStatement {
	return scStatementResult
}
func (*syntaxChecker) createSwitchStatement(TokenLocation, TreeExpression, TreeClauseList, TreeClause, TreeClauseList, VariableEnvironment, FunctionStack) TreeStatement {
	return scStatementResult
}
func (*syntaxChecker) createWhileStatement(TokenLocation, TreeExpression, TreeStatement) TreeStatement {
	return scStatementResult
}
func (*syntaxChecker) createWithStatement(TokenLocation, TreeExpression, TreeStatement, int, TextPosition) TreeStatement {
	return scStatementResult
}
func (*syntaxChecker) createDoWhileStatement(TokenLocation, TreeStatement, TreeExpression) TreeStatement {
	return scStatementResult
}
func (*syntaxChecker) createLabelStatement(TokenLocation, ident.Identifier, TreeStatement, TextPosition, TextPosition) TreeStatement {
	return scStatementResult
}
func (*syntaxChecker) createThrowStatement(TokenLocation, TreeExpression, TextPosition, TextPosition) TreeStatement {
	return scStatementResult
}
func (*syntaxChecker) createDebugger(TokenLocation) TreeStatement { return scStatementResult }

func (*syntaxChecker) createModuleName(TokenLocation, ident.Identifier) TreeModuleName {
	return scModuleNameResult
}
func (*syntaxChecker) createImportSpecifier(TokenLocation, ident.Identifier, ident.Identifier) TreeImportSpecifier {
	return scImportSpecifierResult
}
func (*syntaxChecker) createImportSpecifierList() TreeImportSpecifierList {
	return scImportSpecifierListResult
}
func (*syntaxChecker) appendImportSpecifier(TreeImportSpecifierList, TreeImportSpecifier) {}
func (*syntaxChecker) createImportAttributesList() TreeImportAttributesList {
	return scImportAttributesListResult
}
func (*syntaxChecker) appendImportAttribute(TreeImportAttributesList, ident.Identifier, ident.Identifier) {
}
func (*syntaxChecker) createImportDeclaration(TokenLocation, ImportType, TreeImportSpecifierList, TreeModuleName, TreeImportAttributesList) TreeStatement {
	return scStatementResult
}
func (*syntaxChecker) createExportAllDeclaration(TokenLocation, TreeModuleName, TreeImportAttributesList) TreeStatement {
	return scStatementResult
}
func (*syntaxChecker) createExportDefaultDeclaration(TokenLocation, TreeStatement, ident.Identifier) TreeStatement {
	return scStatementResult
}
func (*syntaxChecker) createExportLocalDeclaration(TokenLocation, TreeStatement) TreeStatement {
	return scStatementResult
}
func (*syntaxChecker) createExportNamedDeclaration(TokenLocation, TreeExportSpecifierList, TreeModuleName, TreeImportAttributesList) TreeStatement {
	return scStatementResult
}
func (*syntaxChecker) createExportSpecifier(TokenLocation, ident.Identifier, ident.Identifier) TreeExportSpecifier {
	return scExportSpecifierResult
}
func (*syntaxChecker) createExportSpecifierList() TreeExportSpecifierList {
	return scExportSpecifierListResult
}
func (*syntaxChecker) appendExportSpecifier(TreeExportSpecifierList, TreeExportSpecifier) {}

// The syntax checker tracks only the single top expression of the binary
// and unary stacks, enough for the parser's "is this a resolve?" checks.

func (c *syntaxChecker) appendBinaryExpressionInfo(operandStackDepth int, current TreeExpression, _, _, _ TextPosition, _ bool) int {
	if c.topBinaryExpr == scNone {
		c.topBinaryExpr = scOf(current)
	} else {
		c.topBinaryExpr = scBinaryExpr
	}
	return operandStackDepth + 1
}

func (*syntaxChecker) operatorStackPop(operatorStackDepth int) int { return operatorStackDepth - 1 }
func (*syntaxChecker) operatorStackShouldReduce(int) bool          { return true }
func (c *syntaxChecker) getFromOperandStack(int) TreeBinaryOperand {
	return c.topBinaryExpr
}
func (*syntaxChecker) shrinkOperandStackBy(operandStackDepth int, amount int) int {
	return operandStackDepth - amount
}
func (*syntaxChecker) appendBinaryOperation(_ TokenLocation, operandStackDepth int, _, _ TreeBinaryOperand) int {
	return operandStackDepth + 1
}
func (*syntaxChecker) operatorStackAppend(operatorStackDepth int, _ TokenType, _ int) int {
	return operatorStackDepth + 1
}
func (c *syntaxChecker) popOperandStack() TreeExpression {
	r := c.topBinaryExpr
	c.topBinaryExpr = scNone
	return sc(r)
}

func (c *syntaxChecker) appendUnaryToken(_ int, t TokenType, _ TextPosition) int {
	c.topUnaryToken = t
	return 1
}
func (c *syntaxChecker) unaryTokenStackLastType() TokenType   { return c.topUnaryToken }
func (*syntaxChecker) unaryTokenStackLastStart() TextPosition { return 0 }
func (*syntaxChecker) unaryTokenStackRemoveLast(int) int      { return 0 }
func (*syntaxChecker) unaryTokenStackDepth() int              { return 0 }
func (*syntaxChecker) setUnaryTokenStackDepth(int)            {}

func (*syntaxChecker) assignmentStackAppend(int, TreeExpression, TextPosition, TextPosition, int, Operator) int {
	return 1
}
func (*syntaxChecker) createAssignment(TokenLocation, int, TreeExpression, int, int, TextPosition) (TreeExpression, int) {
	return scAssignmentExpr, 0
}

func (*syntaxChecker) getType(p TreeProperty) PropertyType { return p.(syntaxProperty).t }
func (*syntaxChecker) isUnderscoreProtoSetter(p TreeProperty) bool {
	return p.(syntaxProperty).isUnderscoreProtoSetter
}
func (*syntaxChecker) isResolve(e TreeExpression) bool {
	r := scOf(e)
	return r == scResolveExpr || r == scResolveEvalExpr
}

func (*syntaxChecker) createDestructuringAssignment(TokenLocation, TreeDestructuringPattern, TreeExpression) TreeExpression {
	return scDestructuringAssignment
}
func (*syntaxChecker) createArrayPattern(TokenLocation) TreeDestructuringPattern {
	return scArrayDestructuring
}
func (*syntaxChecker) appendArrayPatternSkipEntry(TreeDestructuringPattern, TokenLocation) {}
func (*syntaxChecker) appendArrayPatternEntry(TreeDestructuringPattern, TokenLocation, TreeDestructuringPattern, TreeExpression) {
}
func (*syntaxChecker) appendArrayPatternRestEntry(TreeDestructuringPattern, TokenLocation, TreeDestructuringPattern) {
}
func (*syntaxChecker) finishArrayPattern(TreeDestructuringPattern, TextPosition, TextPosition, TextPosition) {
}
func (*syntaxChecker) createObjectPattern(TokenLocation) TreeDestructuringPattern {
	return scObjectDestructuring
}
func (*syntaxChecker) appendObjectPatternEntry(TreeDestructuringPattern, TokenLocation, bool, ident.Identifier, TreeDestructuringPattern, TreeExpression) {
}
func (*syntaxChecker) appendObjectPatternComputedEntry(TreeDestructuringPattern, TokenLocation, TreeExpression, TreeDestructuringPattern, TreeExpression) {
}
func (*syntaxChecker) appendObjectPatternRestEntry(TreeDestructuringPattern, TokenLocation, TreeDestructuringPattern) {
}
func (*syntaxChecker) setContainsObjectRestElement(TreeDestructuringPattern, bool) {}
func (*syntaxChecker) setContainsComputedProperty(TreeDestructuringPattern, bool)  {}
func (*syntaxChecker) finishObjectPattern(TreeDestructuringPattern, TextPosition, TextPosition, TextPosition) {
}
func (*syntaxChecker) createBindingLocation(TokenLocation, ident.Identifier, TextPosition, TextPosition, AssignmentContext) TreeDestructuringPattern {
	return scBindingDestructuring
}
func (*syntaxChecker) createRestParameter(TreeDestructuringPattern, int) TreeDestructuringPattern {
	return scRestParameter
}
func (*syntaxChecker) createAssignmentElement(TreeExpression, TextPosition, TextPosition) TreeDestructuringPattern {
	return scBindingDestructuring
}

func (*syntaxChecker) isBindingNode(p TreeDestructuringPattern) bool {
	return scOf(p) == scBindingDestructuring
}
func (*syntaxChecker) isLocation(e TreeExpression) bool {
	switch scOf(e) {
	case scResolveExpr, scDotExpr, scPrivateDotExpr, scBracketExpr:
		return true
	}
	return false
}
func (*syntaxChecker) isPrivateLocation(e TreeExpression) bool { return scOf(e) == scPrivateDotExpr }
func (c *syntaxChecker) isAssignmentLocation(e TreeExpression) bool {
	return c.isLocation(e) || scOf(e) == scDestructuringAssignment
}
func (*syntaxChecker) isObjectLiteral(e TreeExpression) bool { return scOf(e) == scObjectLiteralExpr }
func (*syntaxChecker) isArrayLiteral(e TreeExpression) bool  { return scOf(e) == scArrayLiteralExpr }
func (c *syntaxChecker) isObjectOrArrayLiteral(e TreeExpression) bool {
	return c.isObjectLiteral(e) || c.isArrayLiteral(e)
}
func (*syntaxChecker) isFunctionCall(e TreeExpression) bool       { return scOf(e) == scCallExpr }
func (*syntaxChecker) shouldSkipPauseLocation(TreeStatement) bool { return true }

func (*syntaxChecker) setEndOffset(any, int)               {}
func (*syntaxChecker) endOffset(any) int                   { return 0 }
func (*syntaxChecker) setStartOffset(any, int)             {}
func (*syntaxChecker) breakpointLocation(any) TextPosition { return NoTextPosition }
func (*syntaxChecker) propagateArgumentsUse()              {}
func (*syntaxChecker) hasArgumentsFeature() bool           { return true }
