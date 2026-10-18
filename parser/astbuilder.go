package parser

import (
	"math"
	"math/big"

	"github.com/baslentfert/jsc2go/ident"
	"github.com/baslentfert/jsc2go/yarr"
)

// astBuilder is the port of JavaScriptCore/parser/ASTBuilder.h: the tree
// builder that actually constructs AST nodes.
type astBuilder struct {
	sourceCode *SourceCode
	scope      struct {
		features     CodeFeatures
		numConstants int
		// argumentsUses counts the uses of arguments, argumentsForwards
		// those that are the second argument of x.apply(y, arguments)
		// (ArgumentsOnlyForwardedFeature).
		argumentsUses, argumentsForwards int
	}
	binaryOperandStack  []binaryOperand
	assignmentInfoStack []assignmentInfo
	binaryOperatorStack []binaryOperator
	unaryTokenStack     []unaryToken
	evalCount           int
}

type binaryOpInfo struct {
	start, divot, end TextPosition
	hasAssignment     bool
}

type binaryOperand struct {
	expr ExpressionNode
	info binaryOpInfo
}

type binaryOperator struct {
	op         TokenType
	precedence int
}

type assignmentInfo struct {
	node            ExpressionNode
	start, divot    TextPosition
	initAssignments int
	op              Operator
}

type unaryToken struct {
	t     TokenType
	start TextPosition
}

func newASTBuilder(source *SourceCode) *astBuilder { return &astBuilder{sourceCode: source} }

var _ treeBuilder = (*astBuilder)(nil)

func (*astBuilder) createsAST() bool              { return true }
func (*astBuilder) needsFreeVariableInfo() bool   { return true }
func (*astBuilder) canUseFunctionCache() bool     { return true }
func (*astBuilder) dontBuildKeywords() LexerFlags { return 0 }
func (*astBuilder) dontBuildStrings() LexerFlags  { return 0 }
func (*astBuilder) pushBinaryExprContext() int    { return 0 }
func (*astBuilder) popBinaryExprContext(int)      {}
func (*astBuilder) pushUnaryExprContext() int     { return 0 }
func (*astBuilder) popUnaryExprContext(int)       {}
func (b *astBuilder) features() CodeFeatures {
	f := b.scope.features
	if f&ArgumentsFeature != 0 && b.scope.argumentsForwards > 0 && b.scope.argumentsUses == b.scope.argumentsForwards {
		f |= ArgumentsOnlyForwardedFeature
	}
	return f
}
func (b *astBuilder) numConstants() int { return b.scope.numConstants }

func (b *astBuilder) incConstants()      { b.scope.numConstants++ }
func (b *astBuilder) usesThis()          { b.scope.features |= ThisFeature }
func (b *astBuilder) usesArrowFunction() { b.scope.features |= ArrowFunctionFeature }
func (b *astBuilder) usesArguments() {
	b.scope.features |= ArgumentsFeature
	b.scope.argumentsUses++
}
func (b *astBuilder) usesWith()          { b.scope.features |= WithFeature }
func (b *astBuilder) usesSuperCall()     { b.scope.features |= SuperCallFeature }
func (b *astBuilder) usesSuperProperty() { b.scope.features |= SuperPropertyFeature }
func (b *astBuilder) usesNewTarget()     { b.scope.features |= NewTargetFeature }
func (b *astBuilder) usesAwait()         { b.scope.features |= AwaitFeature }
func (b *astBuilder) usesEval() {
	b.evalCount++
	b.scope.features |= EvalFeature
}
func (b *astBuilder) propagateArgumentsUse()    { b.usesArguments() }
func (b *astBuilder) hasArgumentsFeature() bool { return b.scope.features&ArgumentsFeature != 0 }

// expr converts a builder value back to an AST expression (nil stays nil).
func expr(e TreeExpression) ExpressionNode {
	if e == nil {
		return nil
	}
	return e.(ExpressionNode)
}

func stmt(s TreeStatement) StatementNode {
	if s == nil {
		return nil
	}
	return s.(StatementNode)
}

func pattern(p TreeDestructuringPattern) DestructuringPatternNode {
	if p == nil {
		return nil
	}
	return p.(DestructuringPatternNode)
}

func sourceElements(s TreeSourceElements) *SourceElements {
	if s == nil {
		return nil
	}
	return s.(*SourceElements)
}

func setExceptionLocation(n throwableNode, divotStart, divot, divotEnd TextPosition) {
	n.Throwable().SetExceptionSourceCode(divot, divotStart, divotEnd)
}

// checkArgumentsLengthModification: `arguments.length` is excluded from
// ArgumentsFeature for a fast path, so modifying it must re-enable the feature.
func (b *astBuilder) checkArgumentsLengthModification(n ExpressionNode) {
	if n != nil && IsArgumentsLengthAccess(n) {
		b.usesArguments()
	}
}

// inferName gives an anonymous function or class expression a name.
func inferName(value ExpressionNode, name ident.Identifier) {
	switch v := value.(type) {
	case BaseFuncExprNode:
		v.Metadata().SetEcmaName(name)
	case *ClassExprNode:
		v.SetEcmaName(name)
	}
}

func (b *astBuilder) tryInferNameInPattern(p DestructuringPatternNode, defaultValue ExpressionNode) {
	if defaultValue == nil {
		return
	}
	switch p := p.(type) {
	case *BindingNode:
		inferName(defaultValue, p.BoundProperty)
	case *AssignmentElementNode:
		if r, ok := p.AssignmentTarget.(*ResolveNode); ok && !r.IsParenthesized() {
			inferName(defaultValue, r.Ident)
		}
	}
}

// --- Source elements and simple expressions ----------------------------------

func (b *astBuilder) createSourceElements() TreeSourceElements { return &SourceElements{} }

func (b *astBuilder) appendStatement(elements TreeSourceElements, s TreeStatement) {
	sourceElements(elements).Append(stmt(s))
}

func (b *astBuilder) createLogicalNot(loc TokenLocation, e TreeExpression) TreeExpression {
	if n, ok := e.(*NumberNode); ok {
		return b.createBoolean(loc, n.Value == 0 || math.IsNaN(n.Value))
	}
	return &UnaryOpNode{ExpressionBase: newExpressionBase(loc, BooleanType()), Op: LogicalNot, Expr: expr(e)}
}

func (b *astBuilder) createUnaryPlus(loc TokenLocation, e TreeExpression) TreeExpression {
	return &UnaryOpNode{ExpressionBase: newExpressionBase(loc, NumberType()), Op: UnaryPlus, Expr: expr(e)}
}

func (b *astBuilder) createVoid(loc TokenLocation, e TreeExpression) TreeExpression {
	b.incConstants()
	return &VoidNode{ExpressionBase: newExpr(loc), Expr: expr(e)}
}

func (b *astBuilder) createThisExpr(loc TokenLocation) TreeExpression {
	b.usesThis()
	return &ThisNode{newExpr(loc)}
}

func (b *astBuilder) createSuperExpr(loc TokenLocation) TreeExpression {
	return &SuperNode{newExpr(loc)}
}

func (b *astBuilder) createImportExpr(loc TokenLocation, e, option TreeExpression, deferred bool, start, divot, end TextPosition) TreeExpression {
	n := &ImportNode{ExpressionBase: newExpr(loc), Expr: expr(e), Option: expr(option), Deferred: deferred}
	setExceptionLocation(n, start, divot, end)
	return n
}

func (b *astBuilder) createNewTargetExpr(loc TokenLocation) TreeExpression {
	b.usesNewTarget()
	return &NewTargetNode{newExpr(loc)}
}

func (b *astBuilder) createImportMetaExpr(loc TokenLocation, e TreeExpression) TreeExpression {
	return &ImportMetaNode{ExpressionBase: newExpr(loc), Expr: expr(e)}
}

func (b *astBuilder) setIsParenthesized(e TreeExpression)  { expr(e).ExprBase().SetIsParenthesized() }
func (b *astBuilder) isMetaProperty(e TreeExpression) bool { return IsMetaProperty(expr(e)) }
func (b *astBuilder) isNewTarget(e TreeExpression) bool {
	_, ok := e.(*NewTargetNode)
	return ok
}
func (b *astBuilder) isImportMeta(e TreeExpression) bool {
	_, ok := e.(*ImportMetaNode)
	return ok
}

func (b *astBuilder) createResolve(loc TokenLocation, name ident.Identifier, start, end TextPosition, needToCheckUsesArguments bool) TreeExpression {
	if needToCheckUsesArguments && name == ident.Arguments {
		b.usesArguments()
	}
	// JSC turns @intrinsic names in builtin code into BytecodeIntrinsicNodes
	// here; builtins are not supported yet.
	return &ResolveNode{ExpressionBase: newExpr(loc), Ident: name, Start: start}
}

func (b *astBuilder) createPrivateIdentifierNode(loc TokenLocation, name ident.Identifier) TreeExpression {
	return &PrivateIdentifierNode{ExpressionBase: newExpr(loc), Ident: name}
}

func (b *astBuilder) createObjectLiteral(loc TokenLocation, properties TreePropertyList) TreeExpression {
	n := &ObjectLiteralNode{ExpressionBase: newExpr(loc)}
	if properties != nil {
		n.List = properties.(*PropertyListNode)
	}
	return n
}

func (b *astBuilder) createArray(loc TokenLocation, elisions int, elements TreeElementList) TreeExpression {
	if elisions != 0 {
		b.incConstants()
	}
	n := &ArrayNode{ExpressionBase: newExpr(loc), Elision: elisions}
	if elements != nil {
		n.Elements = elements.(*ElementList).Elements
	}
	return n
}

func (b *astBuilder) createDoubleExpr(loc TokenLocation, d float64) TreeExpression {
	b.incConstants()
	return newNumberNode(loc, d, false)
}

func (b *astBuilder) createIntegerExpr(loc TokenLocation, d float64) TreeExpression {
	b.incConstants()
	return newNumberNode(loc, d, true)
}

func (b *astBuilder) createBigInt(loc TokenLocation, value ident.Identifier, radix uint8) TreeExpression {
	b.incConstants()
	return &BigIntNode{ExpressionBase: newExpressionBase(loc, BigIntType()), Value: value, Radix: radix}
}

func (b *astBuilder) createString(loc TokenLocation, value ident.Identifier) TreeExpression {
	b.incConstants()
	return &StringNode{ExpressionBase: newExpressionBase(loc, StringType()), Value: value}
}

func (b *astBuilder) createBoolean(loc TokenLocation, v bool) TreeExpression {
	b.incConstants()
	return &BooleanNode{ExpressionBase: newExpressionBase(loc, BooleanType()), Value: v}
}

func (b *astBuilder) createNull(loc TokenLocation) TreeExpression {
	b.incConstants()
	return &NullNode{newExpressionBase(loc, NullType())}
}

func (b *astBuilder) createBracketAccess(loc TokenLocation, base, property TreeExpression, propertyHasAssignments bool, start, divot, end TextPosition) TreeExpression {
	if _, ok := base.(*SuperNode); ok {
		b.usesSuperProperty()
	}
	n := &BracketAccessorNode{ExpressionBase: newExpr(loc), Base: expr(base), Subscript: expr(property), SubscriptHasAssignments: propertyHasAssignments}
	setExceptionLocation(n, start, divot, end)
	return n
}

func (b *astBuilder) createDotAccess(loc TokenLocation, base TreeExpression, property ident.Identifier, t DotType, start, divot, end TextPosition) TreeExpression {
	if _, ok := base.(*SuperNode); ok {
		b.usesSuperProperty()
	}
	n := &DotAccessorNode{ExpressionBase: newExpr(loc), Base: expr(base), Ident: property, Type: t}
	setExceptionLocation(n, start, divot, end)
	return n
}

func (b *astBuilder) createSpreadExpression(loc TokenLocation, e TreeExpression, start, divot, end TextPosition) TreeExpression {
	n := &SpreadExpressionNode{ExpressionBase: newExpr(loc), Expression: expr(e)}
	setExceptionLocation(n, start, divot, end)
	return n
}

func (b *astBuilder) createObjectSpreadExpression(loc TokenLocation, e TreeExpression, start, divot, end TextPosition) TreeExpression {
	n := &ObjectSpreadExpressionNode{ExpressionBase: newExpr(loc), Expression: expr(e)}
	setExceptionLocation(n, start, divot, end)
	return n
}

// --- Templates ------------------------------------------------------------------

type templateStringList struct{ strings []*TemplateStringNode }
type templateExpressionList struct{ exprs []ExpressionNode }

func (b *astBuilder) createTemplateString(loc TokenLocation, cooked, raw ident.Identifier) TreeExpression {
	return &TemplateStringNode{ExpressionBase: newExpressionBase(loc, StringType()), Cooked: cooked, Raw: raw}
}

func (b *astBuilder) createTemplateStringList(list TreeTemplateStringList, s TreeExpression) TreeTemplateStringList {
	l, _ := list.(*templateStringList)
	if l == nil {
		l = &templateStringList{}
	}
	l.strings = append(l.strings, s.(*TemplateStringNode))
	return l
}

func (b *astBuilder) createTemplateExpressionList(list TreeTemplateExpressionList, e TreeExpression) TreeTemplateExpressionList {
	l, _ := list.(*templateExpressionList)
	if l == nil {
		l = &templateExpressionList{}
	}
	l.exprs = append(l.exprs, expr(e))
	return l
}

func (b *astBuilder) createTemplateLiteral(loc TokenLocation, strings TreeTemplateStringList, exprs TreeTemplateExpressionList) TreeExpression {
	n := &TemplateLiteralNode{ExpressionBase: newExpressionBase(loc, StringType()), TemplateStrings: strings.(*templateStringList).strings}
	if exprs != nil {
		n.TemplateExpressions = exprs.(*templateExpressionList).exprs
	}
	return n
}

func (b *astBuilder) createTaggedTemplate(loc TokenLocation, base, templateLiteral TreeExpression, start, divot, end TextPosition) TreeExpression {
	n := &TaggedTemplateNode{ExpressionBase: newExpr(loc), Tag: expr(base), TemplateLiteral: templateLiteral.(*TemplateLiteralNode)}
	setExceptionLocation(n, start, divot, end)
	n.SetEndOffset(int(end))
	return n
}

// RegExpSyntaxChecker validates a regular expression literal; it returns an
// error message, or "" if the pattern is valid.
var RegExpSyntaxChecker = checkRegExp

func checkRegExp(pattern, flags string) string {
	f, ok := yarr.ParseFlags(flags)
	if !ok {
		return "Invalid regular expression: invalid flags"
	}
	if _, err := yarr.CompilePattern(ident.DecodeWTF8(pattern), f); err != yarr.NoError {
		return err.Error()
	}
	return ""
}

func (b *astBuilder) createRegExp(loc TokenLocation, pattern, flags ident.Identifier, start TextPosition, skipSyntaxCheck bool) TreeExpression {
	if !skipSyntaxCheck && RegExpSyntaxChecker(pattern.String(), flags.String()) != "" {
		return nil
	}
	n := &RegExpNode{ExpressionBase: newExpr(loc), Pattern: pattern, Flags: flags}
	end := start + TextPosition(pattern.Len()+2) // + 2 for the two /'s
	setExceptionLocation(n, start, end, end)
	return n
}

func (b *astBuilder) createNewExpr(loc TokenLocation, e TreeExpression, args TreeArguments, start, divot, end TextPosition) TreeExpression {
	n := &NewExprNode{ExpressionBase: newExpr(loc), Expr: expr(e)}
	if args != nil {
		n.Args = args.(*ArgumentsNode)
	}
	setExceptionLocation(n, start, divot, end)
	return n
}

func (b *astBuilder) createOptionalChain(loc TokenLocation, base, e TreeExpression, isOutermost bool) TreeExpression {
	if base != nil {
		expr(base).ExprBase().SetIsOptionalChainBase()
	}
	return &OptionalChainNode{ExpressionBase: newExpr(loc), Expr: expr(e), IsOutermost: isOutermost}
}

func (b *astBuilder) createConditionalExpr(loc TokenLocation, condition, lhs, rhs TreeExpression) TreeExpression {
	return &ConditionalNode{ExpressionBase: newExpr(loc), Logical: expr(condition), Expr1: expr(lhs), Expr2: expr(rhs)}
}

func (b *astBuilder) createAssignResolve(loc TokenLocation, name ident.Identifier, rhs TreeExpression, start, divot, end TextPosition, ctx AssignmentContext) TreeExpression {
	inferName(expr(rhs), name)
	if ctx == AssignmentContextAwaitUsingDeclarationStatement {
		b.usesAwait()
	}
	n := &AssignResolveNode{ExpressionBase: newExpr(loc), Ident: name, Right: expr(rhs), AssignmentContext: ctx}
	setExceptionLocation(n, start, divot, end)
	return n
}

func (b *astBuilder) createEmptyVarExpression(loc TokenLocation, name ident.Identifier) TreeExpression {
	return &EmptyVarExpression{ExpressionBase: newExpr(loc), Ident: name}
}

func (b *astBuilder) createEmptyLetExpression(loc TokenLocation, name ident.Identifier) TreeExpression {
	return &EmptyLetExpression{ExpressionBase: newExpr(loc), Ident: name}
}

func (b *astBuilder) createYield(loc TokenLocation, argument TreeExpression, delegate bool, start, divot, end TextPosition) TreeExpression {
	n := &YieldExprNode{ExpressionBase: newExpr(loc), Argument: expr(argument), Delegate: delegate}
	if argument != nil {
		setExceptionLocation(n, start, divot, end)
	}
	return n
}

func (b *astBuilder) createAwait(loc TokenLocation, argument TreeExpression, start, divot, end TextPosition) TreeExpression {
	b.usesAwait()
	n := &AwaitExprNode{ExpressionBase: newExpr(loc), Argument: expr(argument)}
	setExceptionLocation(n, start, divot, end)
	return n
}

func (b *astBuilder) createDefineField(loc TokenLocation, name ident.Identifier, initializer TreeExpression, t DefineFieldType) TreeStatement {
	if initializer != nil && t != DefineFieldComputedName {
		inferName(expr(initializer), name)
	}
	return &DefineFieldNode{StatementBase: newStatementBase(loc), Ident: name, Assign: expr(initializer), Type: t}
}

func (b *astBuilder) createClassExpr(loc TokenLocation, info *ParserClassInfo, classHeadEnvironment, classEnvironment VariableEnvironment, constructor, parentClass TreeExpression, elements TreePropertyList, start, divot, end TextPosition) TreeExpression {
	var list *PropertyListNode
	if elements != nil {
		list = elements.(*PropertyListNode)
	}
	n := &ClassExprNode{
		ExpressionBase:          newExpr(loc),
		VariableEnvironmentNode: VariableEnvironmentNode{LexicalVariables: classEnvironment},
		ClassHeadEnvironment:    classHeadEnvironment,
		ClassSource:             b.sourceCode.SubExpression(info.StartOffset, info.EndOffset),
		Name:                    info.ClassName,
		ecmaName:                info.ClassName,
		ConstructorExpression:   expr(constructor),
		ClassHeritage:           expr(parentClass),
		ClassElements:           list,
		NeedsLexicalScope:       ShouldCreateLexicalScopeForClass(list),
	}
	setExceptionLocation(n, start, divot, end)
	return n
}

// --- Functions --------------------------------------------------------------------

func metadata(info *ParserFunctionInfo) *FunctionMetadataNode {
	return info.Body.(*FunctionMetadataNode)
}

func (b *astBuilder) createFunctionExpr(loc TokenLocation, info *ParserFunctionInfo) TreeExpression {
	n := &FuncExprNode{newFuncExprBase(loc, info.Name, metadata(info), b.sourceCode.SubExpression(info.StartOffset, info.EndOffset), FunctionModeFunctionExpression)}
	metadata(info).SetStartOffset(loc.StartOffset)
	return n
}

func (b *astBuilder) createGeneratorFunctionBody(loc TokenLocation, info *ParserFunctionInfo, name ident.Identifier) TreeExpression {
	n := b.createFunctionExpr(loc, info).(*FuncExprNode)
	if !name.IsNull() {
		n.Metadata().SetEcmaName(name)
	}
	return n
}

func (b *astBuilder) createAsyncFunctionBody(loc TokenLocation, info *ParserFunctionInfo, mode SourceParseMode, name ident.Identifier) TreeExpression {
	if mode == AsyncArrowFunctionBodyMode {
		end := info.EndOffset
		if metadata(info).IsArrowFunctionBodyExpression {
			end--
		}
		n := &FuncExprNode{newFuncExprBase(loc, info.Name, metadata(info), b.sourceCode.SubExpression(info.StartOffset, end), FunctionModeFunctionExpression)}
		if !name.IsNull() {
			n.Metadata().SetEcmaName(name)
		}
		metadata(info).SetStartOffset(loc.StartOffset)
		return n
	}
	n := b.createFunctionExpr(loc, info).(*FuncExprNode)
	if !name.IsNull() {
		n.Metadata().SetEcmaName(name)
	}
	return n
}

func (b *astBuilder) createMethodDefinition(loc TokenLocation, info *ParserFunctionInfo) TreeExpression {
	n := &MethodDefinitionNode{newFuncExprBase(loc, info.Name, metadata(info), b.sourceCode.SubExpression(info.StartOffset, info.EndOffset), FunctionModeMethodDefinition)}
	metadata(info).SetStartOffset(loc.StartOffset)
	return n
}

func (b *astBuilder) createFunctionMetadata(start, end TokenLocation, functionStart, functionNameStart, parametersStart int, visibility ImplementationVisibility, features LexicallyScopedFeatures, constructorKind ConstructorKind, superBinding SuperBinding, parameterCount int, mode SourceParseMode, isArrowFunctionBodyExpression bool) TreeFunctionBody {
	return &FunctionMetadataNode{
		NodeBase:                      newNodeBase(end),
		ImplementationVisibility:      visibility,
		LexicallyScopedFeatures:       features,
		SuperBinding:                  superBinding,
		ConstructorKind:               constructorKind,
		IsArrowFunctionBodyExpression: isArrowFunctionBodyExpression,
		ParseMode:                     mode,
		FunctionStart:                 functionStart,
		FunctionNameStart:             functionNameStart,
		ParametersStart:               parametersStart,
		StartStartOffset:              start.StartOffset,
		ParameterCount:                parameterCount,
	}
}

func (b *astBuilder) createArrowFunctionExpr(loc TokenLocation, info *ParserFunctionInfo) TreeExpression {
	b.usesArrowFunction()
	end := info.EndOffset
	if metadata(info).IsArrowFunctionBodyExpression {
		end--
	}
	n := &ArrowFuncExprNode{newFuncExprBase(loc, info.Name, metadata(info), b.sourceCode.SubExpression(info.StartOffset, end), FunctionModeFunctionExpression)}
	metadata(info).SetStartOffset(loc.StartOffset)
	return n
}

func (b *astBuilder) createArguments(list TreeArgumentsList, hasAssignments bool) TreeArguments {
	n := &ArgumentsNode{HasAssignments: hasAssignments}
	if list != nil {
		n.Args = list.(*ArgumentList).Args
	}
	return n
}

func (b *astBuilder) createArgumentsList(loc TokenLocation, list TreeArgumentsList, arg TreeExpression) TreeArgumentsList {
	l, _ := list.(*ArgumentList)
	if l == nil {
		l = &ArgumentList{}
	}
	l.Args = append(l.Args, expr(arg))
	return l
}

// --- Properties ---------------------------------------------------------------------

func (*PropertyNode) treeProperty() {}

func (b *astBuilder) createGetterOrSetterProperty(loc TokenLocation, t PropertyType, name ident.Identifier, info *ParserFunctionInfo, tag ClassElementTag) TreeProperty {
	md := metadata(info)
	md.SetStartOffset(loc.StartOffset)
	md.SetEcmaName(name)
	source := b.sourceCode.SubExpression(info.StartOffset, info.EndOffset)
	method := &MethodDefinitionNode{newFuncExprBase(loc, ident.Null, md, source, FunctionModeMethodDefinition)}
	return newPropertyNode(name, nil, method, t, SuperBindingNeeded, tag)
}

func (b *astBuilder) createComputedGetterOrSetterProperty(loc TokenLocation, t PropertyType, name TreeExpression, info *ParserFunctionInfo, tag ClassElementTag) TreeProperty {
	md := metadata(info)
	md.SetStartOffset(loc.StartOffset)
	source := b.sourceCode.SubExpression(info.StartOffset, info.EndOffset)
	method := &MethodDefinitionNode{newFuncExprBase(loc, ident.Null, md, source, FunctionModeMethodDefinition)}
	return newPropertyNode(ident.Null, expr(name), method, t, SuperBindingNeeded, tag)
}

func (b *astBuilder) createNumericGetterOrSetterProperty(loc TokenLocation, t PropertyType, name float64, info *ParserFunctionInfo, tag ClassElementTag) TreeProperty {
	md := metadata(info)
	md.SetStartOffset(loc.StartOffset)
	id := ident.FromNumber(name)
	md.SetEcmaName(id)
	source := b.sourceCode.SubExpression(info.StartOffset, info.EndOffset)
	method := &MethodDefinitionNode{newFuncExprBase(loc, ident.Null, md, source, FunctionModeMethodDefinition)}
	return newPropertyNode(id, nil, method, t, SuperBindingNeeded, tag)
}

func (b *astBuilder) createPropertyWithoutValue(name ident.Identifier, t PropertyType, sb SuperBinding, tag ClassElementTag) TreeProperty {
	return newPropertyNode(name, nil, nil, t, sb, tag)
}

func (b *astBuilder) createProperty(name ident.Identifier, value TreeExpression, t PropertyType, sb SuperBinding, infer InferName, tag ClassElementTag) TreeProperty {
	if infer == InferNameAllowed && value != nil {
		inferName(expr(value), name)
	}
	return newPropertyNode(name, nil, expr(value), t, sb, tag)
}

func (b *astBuilder) createSpreadProperty(value TreeExpression, t PropertyType, sb SuperBinding, tag ClassElementTag) TreeProperty {
	return newPropertyNode(ident.Null, nil, expr(value), t, sb, tag)
}

func (b *astBuilder) createComputedProperty(name, value TreeExpression, t PropertyType, sb SuperBinding, tag ClassElementTag) TreeProperty {
	return newPropertyNode(ident.Null, expr(name), expr(value), t, sb, tag)
}

func (b *astBuilder) createNamedComputedProperty(name ident.Identifier, nameExpr, value TreeExpression, t PropertyType, sb SuperBinding, tag ClassElementTag) TreeProperty {
	return newPropertyNode(name, expr(nameExpr), expr(value), t, sb, tag)
}

func (b *astBuilder) createPropertyList(loc TokenLocation, property TreeProperty, tail TreePropertyList) TreePropertyList {
	l, _ := tail.(*PropertyListNode)
	if l == nil {
		l = &PropertyListNode{ExpressionBase: newExpr(loc)}
	}
	p := property.(*PropertyNode)
	p.Position = TextPosition(loc.StartOffset)
	l.Properties = append(l.Properties, p)
	return l
}

func (b *astBuilder) setHasPrivateAccessors(list TreePropertyList, has bool) {
	if l, ok := list.(*PropertyListNode); ok && l != nil {
		l.HasPrivateAccessors = has
	}
}

func (b *astBuilder) createElementList(list TreeElementList, elisions int, e TreeExpression) TreeElementList {
	l, _ := list.(*ElementList)
	if l == nil {
		l = &ElementList{}
	}
	l.Elements = append(l.Elements, &ElementNode{Elision: elisions, Value: expr(e)})
	return l
}

func (b *astBuilder) createElementListFromArguments(args TreeArgumentsList) TreeElementList {
	l := &ElementList{}
	for _, a := range args.(*ArgumentList).Args {
		l.Elements = append(l.Elements, &ElementNode{Value: a})
	}
	return l
}

func (b *astBuilder) createFormalParameterList() TreeFormalParameterList {
	return NewFunctionParameters()
}

func (b *astBuilder) appendParameter(list TreeFormalParameterList, p TreeDestructuringPattern, defaultValue TreeExpression) {
	list.(*FunctionParameters).Append(pattern(p), expr(defaultValue))
	b.tryInferNameInPattern(pattern(p), expr(defaultValue))
}

func (b *astBuilder) createClause(e TreeExpression, statements TreeSourceElements) TreeClause {
	return &CaseClauseNode{Expr: expr(e), Statements: sourceElements(statements)}
}

func (b *astBuilder) createClauseList(list TreeClauseList, clause TreeClause) TreeClauseList {
	l, _ := list.(*ClauseList)
	if l == nil {
		l = &ClauseList{}
	}
	l.Clauses = append(l.Clauses, clause.(*CaseClauseNode))
	return l
}

// --- Statements ---------------------------------------------------------------------

func (b *astBuilder) createFuncDeclStatement(loc TokenLocation, info *ParserFunctionInfo) TreeStatement {
	md := metadata(info)
	md.FinishParsing(b.sourceCode.SubExpression(info.StartOffset, info.EndOffset), info.Name, FunctionModeFunctionDeclaration)
	n := &FuncDeclNode{StatementBase: newStatementBase(loc), Metadata: md}
	if info.Name == ident.Arguments {
		b.usesArguments()
	}
	md.SetStartOffset(loc.StartOffset)
	return n
}

func (b *astBuilder) createClassDeclStatement(loc TokenLocation, classExpr TreeExpression, classStart, classEnd TextPosition) TreeStatement {
	class := classExpr.(*ClassExprNode)
	assign := b.createAssignResolve(loc, class.Name, class, classStart, classStart+1, classEnd, AssignmentContextDeclarationStatement)
	n := &ClassDeclNode{StatementBase: newStatementBase(loc), ClassDeclaration: expr(assign)}
	n.SetStartOffset(loc.StartOffset)
	return n
}

func (b *astBuilder) createBlockStatement(loc TokenLocation, elements TreeSourceElements, lexicalVariables VariableEnvironment, functionStack FunctionStack) TreeStatement {
	n := &BlockNode{
		StatementBase:           newStatementBase(loc),
		VariableEnvironmentNode: VariableEnvironmentNode{LexicalVariables: lexicalVariables, FunctionStack: functionStack},
		Statements:              sourceElements(elements),
	}
	n.SetStartOffset(loc.StartOffset)
	return n
}

func (b *astBuilder) createExprStatement(loc TokenLocation, e TreeExpression, start TextPosition) TreeStatement {
	n := &ExprStatementNode{StatementBase: newStatementBase(loc), Expr: expr(e)}
	n.SetStartOffset(int(start))
	return n
}

func (b *astBuilder) createIfStatement(loc TokenLocation, condition TreeExpression, trueBlock, falseBlock TreeStatement) TreeStatement {
	n := &IfElseNode{StatementBase: newStatementBase(loc), Condition: expr(condition), IfBlock: stmt(trueBlock), ElseBlock: stmt(falseBlock)}
	n.SetStartOffset(loc.StartOffset)
	return n
}

func (b *astBuilder) createForLoop(loc TokenLocation, initializer, condition, iter TreeExpression, statements TreeStatement, lexicalVariables VariableEnvironment, initializerContainsClosure bool) TreeStatement {
	n := &ForNode{
		StatementBase:              newStatementBase(loc),
		VariableEnvironmentNode:    VariableEnvironmentNode{LexicalVariables: lexicalVariables},
		Expr1:                      expr(initializer),
		Expr2:                      expr(condition),
		Expr3:                      expr(iter),
		Statement:                  stmt(statements),
		InitializerContainsClosure: initializerContainsClosure,
	}
	n.SetStartOffset(loc.StartOffset)
	return n
}

func newEnumerationNode(loc TokenLocation, lhs, iter ExpressionNode, statement StatementNode, lexicalVariables VariableEnvironment) EnumerationNode {
	return EnumerationNode{
		StatementBase:           newStatementBase(loc),
		VariableEnvironmentNode: VariableEnvironmentNode{LexicalVariables: lexicalVariables},
		Lexpr:                   lhs,
		Expr:                    iter,
		Statement:               statement,
	}
}

func (b *astBuilder) createForInLoop(loc TokenLocation, lhs, iter TreeExpression, statements TreeStatement, declLoc TokenLocation, eStart, eDivot, eEnd TextPosition, lexicalVariables VariableEnvironment) TreeStatement {
	n := &ForInNode{newEnumerationNode(loc, expr(lhs), expr(iter), stmt(statements), lexicalVariables)}
	n.SetStartOffset(loc.StartOffset)
	setExceptionLocation(n, eStart, eDivot, eEnd)
	return n
}

func (b *astBuilder) createForInLoopWithPattern(loc TokenLocation, p TreeDestructuringPattern, iter TreeExpression, statements TreeStatement, declLoc TokenLocation, eStart, eDivot, eEnd TextPosition, lexicalVariables VariableEnvironment) TreeStatement {
	lexpr := &DestructuringAssignmentNode{ExpressionBase: newExpr(declLoc), Bindings: pattern(p)}
	return b.createForInLoop(loc, lexpr, iter, statements, declLoc, eStart, eDivot, eEnd, lexicalVariables)
}

func (b *astBuilder) createForOfLoop(isForAwait bool, loc TokenLocation, lhs, iter TreeExpression, statements TreeStatement, declLoc TokenLocation, eStart, eDivot, eEnd TextPosition, lexicalVariables VariableEnvironment) TreeStatement {
	n := &ForOfNode{EnumerationNode: newEnumerationNode(loc, expr(lhs), expr(iter), stmt(statements), lexicalVariables), IsForAwait: isForAwait}
	n.SetStartOffset(loc.StartOffset)
	setExceptionLocation(n, eStart, eDivot, eEnd)
	if isForAwait {
		b.usesAwait()
	}
	return n
}

func (b *astBuilder) createForOfLoopWithPattern(isForAwait bool, loc TokenLocation, p TreeDestructuringPattern, iter TreeExpression, statements TreeStatement, declLoc TokenLocation, eStart, eDivot, eEnd TextPosition, lexicalVariables VariableEnvironment) TreeStatement {
	lexpr := &DestructuringAssignmentNode{ExpressionBase: newExpr(declLoc), Bindings: pattern(p)}
	return b.createForOfLoop(isForAwait, loc, lexpr, iter, statements, declLoc, eStart, eDivot, eEnd, lexicalVariables)
}

func (b *astBuilder) isBindingNode(p TreeDestructuringPattern) bool {
	_, ok := p.(*BindingNode)
	return ok
}
func (b *astBuilder) isLocation(e TreeExpression) bool { return IsLocation(expr(e)) }
func (b *astBuilder) isAssignmentLocation(e TreeExpression) bool {
	return IsAssignmentLocation(expr(e))
}
func (b *astBuilder) isPrivateLocation(e TreeExpression) bool { return IsPrivateLocation(expr(e)) }
func (b *astBuilder) isObjectLiteral(e TreeExpression) bool   { return IsObjectLiteral(expr(e)) }
func (b *astBuilder) isArrayLiteral(e TreeExpression) bool    { return IsArrayLiteral(expr(e)) }
func (b *astBuilder) isObjectOrArrayLiteral(e TreeExpression) bool {
	return b.isObjectLiteral(e) || b.isArrayLiteral(e)
}
func (b *astBuilder) isFunctionCall(e TreeExpression) bool { return IsFunctionCall(expr(e)) }

func (b *astBuilder) shouldSkipPauseLocation(s TreeStatement) bool {
	if s == nil {
		return true
	}
	_, isLabel := s.(*LabelNode)
	return isLabel
}

func (b *astBuilder) createEmptyStatement(loc TokenLocation) TreeStatement {
	return &EmptyStatementNode{newStatementBase(loc)}
}

func (b *astBuilder) createDeclarationStatement(loc TokenLocation, e TreeExpression) TreeStatement {
	n := &DeclarationStatement{StatementBase: newStatementBase(loc), Expr: expr(e)}
	n.SetStartOffset(loc.StartOffset)
	return n
}

func (b *astBuilder) createReturnStatement(loc TokenLocation, e TreeExpression, start, end TextPosition) TreeStatement {
	n := &ReturnNode{StatementBase: newStatementBase(loc), Value: expr(e)}
	setExceptionLocation(n, start, end, end)
	n.SetStartOffset(int(start))
	return n
}

func (b *astBuilder) createBreakStatement(loc TokenLocation, label ident.Identifier, start, end TextPosition) TreeStatement {
	n := &BreakNode{StatementBase: newStatementBase(loc), Ident: label}
	setExceptionLocation(n, start, end, end)
	n.SetStartOffset(int(start))
	return n
}

func (b *astBuilder) createContinueStatement(loc TokenLocation, label ident.Identifier, start, end TextPosition) TreeStatement {
	n := &ContinueNode{StatementBase: newStatementBase(loc), Ident: label}
	setExceptionLocation(n, start, end, end)
	n.SetStartOffset(int(start))
	return n
}

func (b *astBuilder) createTryStatement(loc TokenLocation, tryBlock TreeStatement, catchPattern TreeDestructuringPattern, catchBlock, finallyBlock TreeStatement, catchEnvironment VariableEnvironment) TreeStatement {
	n := &TryNode{
		StatementBase:           newStatementBase(loc),
		VariableEnvironmentNode: VariableEnvironmentNode{LexicalVariables: catchEnvironment},
		TryBlock:                stmt(tryBlock),
		CatchPattern:            pattern(catchPattern),
		CatchBlock:              stmt(catchBlock),
		FinallyBlock:            stmt(finallyBlock),
	}
	n.SetStartOffset(loc.StartOffset)
	return n
}

func clauses(list TreeClauseList) []*CaseClauseNode {
	if list == nil {
		return nil
	}
	return list.(*ClauseList).Clauses
}

func (b *astBuilder) createSwitchStatement(loc TokenLocation, e TreeExpression, firstClauses TreeClauseList, defaultClause TreeClause, secondClauses TreeClauseList, lexicalVariables VariableEnvironment, functionStack FunctionStack) TreeStatement {
	block := &CaseBlockNode{List1: clauses(firstClauses), List2: clauses(secondClauses)}
	if defaultClause != nil {
		block.DefaultClause = defaultClause.(*CaseClauseNode)
	}
	n := &SwitchNode{
		StatementBase:           newStatementBase(loc),
		VariableEnvironmentNode: VariableEnvironmentNode{LexicalVariables: lexicalVariables, FunctionStack: functionStack},
		Expr:                    expr(e),
		Block:                   block,
	}
	n.SetStartOffset(loc.StartOffset)
	return n
}

func (b *astBuilder) createWhileStatement(loc TokenLocation, e TreeExpression, statement TreeStatement) TreeStatement {
	n := &WhileNode{StatementBase: newStatementBase(loc), Expr: expr(e), Statement: stmt(statement)}
	n.SetStartOffset(loc.StartOffset)
	return n
}

func (b *astBuilder) createDoWhileStatement(loc TokenLocation, statement TreeStatement, e TreeExpression) TreeStatement {
	n := &DoWhileNode{StatementBase: newStatementBase(loc), Statement: stmt(statement), Expr: expr(e)}
	n.SetStartOffset(loc.StartOffset)
	return n
}

func (b *astBuilder) createLabelStatement(loc TokenLocation, label ident.Identifier, statement TreeStatement, start, end TextPosition) TreeStatement {
	n := &LabelNode{StatementBase: newStatementBase(loc), Name: label, Statement: stmt(statement)}
	setExceptionLocation(n, start, end, end)
	return n
}

func (b *astBuilder) createWithStatement(loc TokenLocation, e TreeExpression, statement TreeStatement, start int, end TextPosition) TreeStatement {
	b.usesWith()
	n := &WithNode{StatementBase: newStatementBase(loc), Expr: expr(e), Statement: stmt(statement), Divot: end, ExpressionLength: uint32(int(end) - start)}
	n.SetStartOffset(loc.StartOffset)
	return n
}

func (b *astBuilder) createThrowStatement(loc TokenLocation, e TreeExpression, start, end TextPosition) TreeStatement {
	n := &ThrowNode{StatementBase: newStatementBase(loc), Expr: expr(e)}
	n.SetStartOffset(int(start))
	setExceptionLocation(n, start, end, end)
	return n
}

func (b *astBuilder) createDebugger(loc TokenLocation) TreeStatement {
	n := &DebuggerStatementNode{newStatementBase(loc)}
	n.SetStartOffset(loc.StartOffset)
	return n
}

// --- Modules ------------------------------------------------------------------------

func (b *astBuilder) createModuleName(loc TokenLocation, name ident.Identifier) TreeModuleName {
	return &ModuleNameNode{NodeBase: newNodeBase(loc), ModuleName: name}
}

func (b *astBuilder) createImportSpecifier(loc TokenLocation, importedName, localName ident.Identifier) TreeImportSpecifier {
	return &ImportSpecifierNode{NodeBase: newNodeBase(loc), ImportedName: importedName, LocalName: localName}
}

func (b *astBuilder) createImportSpecifierList() TreeImportSpecifierList {
	return &ImportSpecifierListNode{}
}

func (b *astBuilder) appendImportSpecifier(list TreeImportSpecifierList, specifier TreeImportSpecifier) {
	l := list.(*ImportSpecifierListNode)
	l.Specifiers = append(l.Specifiers, specifier.(*ImportSpecifierNode))
}

func (b *astBuilder) createImportAttributesList() TreeImportAttributesList {
	return &ImportAttributesListNode{}
}

func (b *astBuilder) appendImportAttribute(list TreeImportAttributesList, key, value ident.Identifier) {
	l := list.(*ImportAttributesListNode)
	l.Attributes = append(l.Attributes, ImportAttribute{key, value})
}

func attributes(list TreeImportAttributesList) *ImportAttributesListNode {
	if list == nil {
		return nil
	}
	return list.(*ImportAttributesListNode)
}

func moduleName(name TreeModuleName) *ModuleNameNode {
	if name == nil {
		return nil
	}
	return name.(*ModuleNameNode)
}

func (b *astBuilder) createImportDeclaration(loc TokenLocation, t ImportType, specifiers TreeImportSpecifierList, name TreeModuleName, attrs TreeImportAttributesList) TreeStatement {
	return &ImportDeclarationNode{
		moduleDeclarationBase: moduleDeclarationBase{newStatementBase(loc)},
		Type:                  t,
		SpecifierList:         specifiers.(*ImportSpecifierListNode),
		ModuleName:            moduleName(name),
		AttributesList:        attributes(attrs),
	}
}

func (b *astBuilder) createExportAllDeclaration(loc TokenLocation, name TreeModuleName, attrs TreeImportAttributesList) TreeStatement {
	return &ExportAllDeclarationNode{
		moduleDeclarationBase: moduleDeclarationBase{newStatementBase(loc)},
		ModuleName:            moduleName(name),
		AttributesList:        attributes(attrs),
	}
}

func (b *astBuilder) createExportDefaultDeclaration(loc TokenLocation, declaration TreeStatement, localName ident.Identifier) TreeStatement {
	return &ExportDefaultDeclarationNode{
		moduleDeclarationBase: moduleDeclarationBase{newStatementBase(loc)},
		Declaration:           stmt(declaration),
		LocalName:             localName,
	}
}

func (b *astBuilder) createExportLocalDeclaration(loc TokenLocation, declaration TreeStatement) TreeStatement {
	return &ExportLocalDeclarationNode{
		moduleDeclarationBase: moduleDeclarationBase{newStatementBase(loc)},
		Declaration:           stmt(declaration),
	}
}

func (b *astBuilder) createExportNamedDeclaration(loc TokenLocation, specifiers TreeExportSpecifierList, name TreeModuleName, attrs TreeImportAttributesList) TreeStatement {
	return &ExportNamedDeclarationNode{
		moduleDeclarationBase: moduleDeclarationBase{newStatementBase(loc)},
		SpecifierList:         specifiers.(*ExportSpecifierListNode),
		ModuleName:            moduleName(name),
		AttributesList:        attributes(attrs),
	}
}

func (b *astBuilder) createExportSpecifier(loc TokenLocation, localName, exportedName ident.Identifier) TreeExportSpecifier {
	return &ExportSpecifierNode{NodeBase: newNodeBase(loc), LocalName: localName, ExportedName: exportedName}
}

func (b *astBuilder) createExportSpecifierList() TreeExportSpecifierList {
	return &ExportSpecifierListNode{}
}

func (b *astBuilder) appendExportSpecifier(list TreeExportSpecifierList, specifier TreeExportSpecifier) {
	l := list.(*ExportSpecifierListNode)
	l.Specifiers = append(l.Specifiers, specifier.(*ExportSpecifierNode))
}

// --- Comma expressions --------------------------------------------------------------

func (b *astBuilder) createCommaExpr(loc TokenLocation, e TreeExpression) TreeExpression {
	return &CommaNode{ExpressionBase: newExpr(loc), Exprs: []ExpressionNode{expr(e)}}
}

// appendToCommaExpr appends to the comma expression; the parser keeps using
// the head node it got from createCommaExpr, so the same node is returned.
func (b *astBuilder) appendToCommaExpr(loc TokenLocation, tail, next TreeExpression) TreeExpression {
	c := tail.(*CommaNode)
	c.Exprs = append(c.Exprs, expr(next))
	return c
}

// --- Binary and unary expression stacks ------------------------------------------------

func (b *astBuilder) appendBinaryExpressionInfo(operandStackDepth int, current TreeExpression, exprStart, lhs, rhs TextPosition, hasAssignments bool) int {
	b.binaryOperandStack = append(b.binaryOperandStack, binaryOperand{expr(current), binaryOpInfo{exprStart, lhs, rhs, hasAssignments}})
	return operandStackDepth + 1
}

func (b *astBuilder) operatorStackPop(operatorStackDepth int) int {
	b.binaryOperatorStack = b.binaryOperatorStack[:len(b.binaryOperatorStack)-1]
	return operatorStackDepth - 1
}

// operatorStackShouldReduce: with equal precedence, right-associative
// operators (only **) do not reduce.
func (b *astBuilder) operatorStackShouldReduce(precedence int) bool {
	last := b.binaryOperatorStack[len(b.binaryOperatorStack)-1]
	if precedence == last.precedence {
		return last.op&RightAssociativeBinaryOpTokenFlag == 0
	}
	return precedence < last.precedence
}

func (b *astBuilder) getFromOperandStack(i int) TreeBinaryOperand {
	return b.binaryOperandStack[len(b.binaryOperandStack)+i]
}

func (b *astBuilder) shrinkOperandStackBy(operandStackDepth int, amount int) int {
	b.binaryOperandStack = b.binaryOperandStack[:len(b.binaryOperandStack)-amount]
	return operandStackDepth - amount
}

func (b *astBuilder) appendBinaryOperation(loc TokenLocation, operandStackDepth int, lhs, rhs TreeBinaryOperand) int {
	l, r := lhs.(binaryOperand), rhs.(binaryOperand)
	op := b.binaryOperatorStack[len(b.binaryOperatorStack)-1].op
	info := binaryOpInfo{start: l.info.start, divot: r.info.start, end: r.info.end, hasAssignment: l.info.hasAssignment || r.info.hasAssignment}
	b.binaryOperandStack = append(b.binaryOperandStack, binaryOperand{b.makeBinaryNode(loc, op, l, r), info})
	return operandStackDepth + 1
}

func (b *astBuilder) operatorStackAppend(operatorStackDepth int, op TokenType, precedence int) int {
	b.binaryOperatorStack = append(b.binaryOperatorStack, binaryOperator{op, precedence})
	return operatorStackDepth + 1
}

func (b *astBuilder) popOperandStack() TreeExpression {
	last := b.binaryOperandStack[len(b.binaryOperandStack)-1]
	b.binaryOperandStack = b.binaryOperandStack[:len(b.binaryOperandStack)-1]
	return last.expr
}

func (b *astBuilder) appendUnaryToken(tokenStackDepth int, t TokenType, start TextPosition) int {
	b.unaryTokenStack = append(b.unaryTokenStack, unaryToken{t, start})
	return tokenStackDepth + 1
}

func (b *astBuilder) unaryTokenStackLastType() TokenType {
	return b.unaryTokenStack[len(b.unaryTokenStack)-1].t
}

func (b *astBuilder) unaryTokenStackLastStart() TextPosition {
	return b.unaryTokenStack[len(b.unaryTokenStack)-1].start
}

func (b *astBuilder) unaryTokenStackRemoveLast(tokenStackDepth int) int {
	b.unaryTokenStack = b.unaryTokenStack[:len(b.unaryTokenStack)-1]
	return tokenStackDepth - 1
}

func (b *astBuilder) unaryTokenStackDepth() int { return len(b.unaryTokenStack) }

func (b *astBuilder) setUnaryTokenStackDepth(depth int) {
	if depth < len(b.unaryTokenStack) {
		b.unaryTokenStack = b.unaryTokenStack[:depth]
	}
}

func (b *astBuilder) assignmentStackAppend(assignmentStackDepth int, node TreeExpression, start, divot TextPosition, assignmentCount int, op Operator) int {
	b.assignmentInfoStack = append(b.assignmentInfoStack, assignmentInfo{expr(node), start, divot, assignmentCount, op})
	return assignmentStackDepth + 1
}

func (b *astBuilder) createAssignment(loc TokenLocation, assignmentStackDepth int, rhs TreeExpression, initialAssignmentCount, currentAssignmentCount int, lastTokenEnd TextPosition) (TreeExpression, int) {
	info := b.assignmentInfoStack[len(b.assignmentInfoStack)-1]
	b.checkArgumentsLengthModification(info.node)
	result := b.makeAssignNode(loc, info.node, info.op, expr(rhs), info.initAssignments != initialAssignmentCount, info.initAssignments != currentAssignmentCount, info.start, info.divot+1, lastTokenEnd)
	b.assignmentInfoStack = b.assignmentInfoStack[:len(b.assignmentInfoStack)-1]
	return result, assignmentStackDepth - 1
}

func (b *astBuilder) getType(p TreeProperty) PropertyType { return p.(*PropertyNode).Type }
func (b *astBuilder) isUnderscoreProtoSetter(p TreeProperty) bool {
	return p.(*PropertyNode).IsUnderscoreProtoSetter()
}
func (b *astBuilder) isResolve(e TreeExpression) bool { return IsResolveNode(expr(e)) }

// --- Destructuring --------------------------------------------------------------------

func (b *astBuilder) createDestructuringAssignment(loc TokenLocation, p TreeDestructuringPattern, initializer TreeExpression) TreeExpression {
	return &DestructuringAssignmentNode{ExpressionBase: newExpr(loc), Bindings: pattern(p), Initializer: expr(initializer)}
}

func (b *astBuilder) createArrayPattern(TokenLocation) TreeDestructuringPattern {
	return &ArrayPatternNode{}
}

func (b *astBuilder) appendArrayPatternSkipEntry(p TreeDestructuringPattern, _ TokenLocation) {
	a := p.(*ArrayPatternNode)
	a.TargetPatterns = append(a.TargetPatterns, ArrayPatternEntry{BindingType: ArrayPatternElision})
}

func (b *astBuilder) appendArrayPatternEntry(p TreeDestructuringPattern, _ TokenLocation, inner TreeDestructuringPattern, defaultValue TreeExpression) {
	a := p.(*ArrayPatternNode)
	a.TargetPatterns = append(a.TargetPatterns, ArrayPatternEntry{ArrayPatternElement, pattern(inner), expr(defaultValue)})
	b.tryInferNameInPattern(pattern(inner), expr(defaultValue))
}

func (b *astBuilder) appendArrayPatternRestEntry(p TreeDestructuringPattern, _ TokenLocation, inner TreeDestructuringPattern) {
	a := p.(*ArrayPatternNode)
	a.TargetPatterns = append(a.TargetPatterns, ArrayPatternEntry{BindingType: ArrayPatternRestElement, Pattern: pattern(inner)})
}

func (b *astBuilder) finishArrayPattern(p TreeDestructuringPattern, divotStart, divot, divotEnd TextPosition) {
	setExceptionLocation(p.(*ArrayPatternNode), divotStart, divot, divotEnd)
}

func (b *astBuilder) createObjectPattern(TokenLocation) TreeDestructuringPattern {
	return &ObjectPatternNode{}
}

func (b *astBuilder) appendObjectPatternEntry(p TreeDestructuringPattern, _ TokenLocation, wasString bool, name ident.Identifier, inner TreeDestructuringPattern, defaultValue TreeExpression) {
	o := p.(*ObjectPatternNode)
	o.TargetPatterns = append(o.TargetPatterns, ObjectPatternEntry{PropertyName: name, WasString: wasString, Pattern: pattern(inner), DefaultValue: expr(defaultValue), BindingType: ObjectPatternElement})
	b.tryInferNameInPattern(pattern(inner), expr(defaultValue))
}

func (b *astBuilder) appendObjectPatternComputedEntry(p TreeDestructuringPattern, _ TokenLocation, propertyExpression TreeExpression, inner TreeDestructuringPattern, defaultValue TreeExpression) {
	o := p.(*ObjectPatternNode)
	o.TargetPatterns = append(o.TargetPatterns, ObjectPatternEntry{PropertyExpression: expr(propertyExpression), Pattern: pattern(inner), DefaultValue: expr(defaultValue), BindingType: ObjectPatternElement})
	b.tryInferNameInPattern(pattern(inner), expr(defaultValue))
}

func (b *astBuilder) appendObjectPatternRestEntry(p TreeDestructuringPattern, _ TokenLocation, inner TreeDestructuringPattern) {
	o := p.(*ObjectPatternNode)
	o.TargetPatterns = append(o.TargetPatterns, ObjectPatternEntry{Pattern: pattern(inner), BindingType: ObjectPatternRestElement})
}

func (b *astBuilder) setContainsObjectRestElement(p TreeDestructuringPattern, contains bool) {
	p.(*ObjectPatternNode).ContainsRestElement = contains
}

func (b *astBuilder) setContainsComputedProperty(p TreeDestructuringPattern, contains bool) {
	p.(*ObjectPatternNode).ContainsComputedProperty = contains
}

func (b *astBuilder) finishObjectPattern(p TreeDestructuringPattern, divotStart, divot, divotEnd TextPosition) {
	setExceptionLocation(p.(*ObjectPatternNode), divotStart, divot, divotEnd)
}

func (b *astBuilder) createBindingLocation(_ TokenLocation, name ident.Identifier, start, end TextPosition, ctx AssignmentContext) TreeDestructuringPattern {
	if ctx == AssignmentContextAwaitUsingDeclarationStatement {
		b.usesAwait()
	}
	return &BindingNode{DivotStart: start, DivotEnd: end, BoundProperty: name, BindingContext: ctx}
}

func (b *astBuilder) createRestParameter(p TreeDestructuringPattern, numParametersToSkip int) TreeDestructuringPattern {
	return &RestParameterNode{Pattern: pattern(p), NumParametersToSkip: numParametersToSkip}
}

func (b *astBuilder) createAssignmentElement(target TreeExpression, start, end TextPosition) TreeDestructuringPattern {
	b.checkArgumentsLengthModification(expr(target))
	return &AssignmentElementNode{DivotStart: start, DivotEnd: end, AssignmentTarget: expr(target)}
}

// --- Offsets ------------------------------------------------------------------------

func (b *astBuilder) setEndOffset(node any, offset int) { node.(Node).BaseNode().SetEndOffset(offset) }
func (b *astBuilder) endOffset(node any) int            { return node.(Node).BaseNode().EndOffset() }

func (b *astBuilder) setStartOffset(node any, offset int) {
	if c, ok := node.(*CaseClauseNode); ok {
		c.StartOffset = offset
		return
	}
	node.(Node).BaseNode().SetStartOffset(offset)
}

func (b *astBuilder) breakpointLocation(node any) TextPosition {
	n := node.(Node).BaseNode()
	n.SetNeedsDebugHook()
	return n.Position()
}

// --- Node factories with constant folding (ASTBuilder.h, bottom half) --------------

func (b *astBuilder) makeTypeOfNode(loc TokenLocation, e TreeExpression, start, divot, end TextPosition) TreeExpression {
	if r, ok := e.(*ResolveNode); ok {
		n := &TypeOfResolveNode{ExpressionBase: newExpressionBase(loc, StringType()), Ident: r.Ident}
		setExceptionLocation(n, start, divot, end)
		return n
	}
	return &TypeOfValueNode{ExpressionBase: newExpressionBase(loc, StringType()), Expr: expr(e)}
}

func (b *astBuilder) makeDeleteNode(loc TokenLocation, te TreeExpression, start, divot, end TextPosition) TreeExpression {
	e := expr(te)
	if chain, ok := e.(*OptionalChainNode); ok && IsLocation(chain.Expr) {
		chain.Expr = expr(b.makeDeleteNode(loc, chain.Expr, start, divot, end))
		return chain
	}
	switch n := e.(type) {
	case *ResolveNode:
		d := &DeleteResolveNode{ExpressionBase: newExpressionBase(loc, BooleanType()), Ident: n.Ident}
		d.SetExceptionSourceCode(divot, start, end)
		return d
	case *BracketAccessorNode:
		d := &DeleteBracketNode{ExpressionBase: newExpressionBase(loc, BooleanType()), Base: n.Base, Subscript: n.Subscript}
		d.SetExceptionSourceCode(divot, start, end)
		return d
	case *DotAccessorNode:
		b.checkArgumentsLengthModification(e)
		d := &DeleteDotNode{ExpressionBase: newExpressionBase(loc, BooleanType()), Base: n.Base, Ident: n.Ident}
		d.SetExceptionSourceCode(divot, start, end)
		return d
	}
	return &DeleteValueNode{ExpressionBase: newExpressionBase(loc, BooleanType()), Expr: e}
}

// createNumberLike keeps the integer-ness of folded constants (IntegerNode vs DoubleNode).
func (b *astBuilder) createNumberLike(loc TokenLocation, v float64, isInteger bool) *NumberNode {
	return newNumberNode(loc, v, isInteger)
}

func (b *astBuilder) makeNegateNode(loc TokenLocation, te TreeExpression) TreeExpression {
	switch n := te.(type) {
	case *NumberNode:
		return b.createNumberLike(loc, -n.Value, n.IsInteger)
	case *BigIntNode:
		return &BigIntNode{ExpressionBase: newExpressionBase(loc, BigIntType()), Value: n.Value, Radix: n.Radix, Sign: !n.Sign}
	}
	e := expr(te)
	return &UnaryOpNode{ExpressionBase: newExpressionBase(loc, ResultTypeForUnaryArith(ResultDescriptor(e))), Op: Negate, Expr: e}
}

func (b *astBuilder) makeBitwiseNotNode(loc TokenLocation, te TreeExpression) TreeExpression {
	if n, ok := te.(*NumberNode); ok {
		return b.createNumberLike(loc, float64(^ToInt32(n.Value)), true)
	}
	return &UnaryOpNode{ExpressionBase: newExpressionBase(loc, ResultTypeForBitOp()), Op: BitwiseNot, Expr: expr(te)}
}

func numbers(e1, e2 ExpressionNode) (*NumberNode, *NumberNode, bool) {
	n1, ok1 := e1.(*NumberNode)
	n2, ok2 := e2.(*NumberNode)
	return n1, n2, ok1 && ok2
}

func (b *astBuilder) makePowNode(loc TokenLocation, e1, e2 ExpressionNode, rightHasAssignments bool) ExpressionNode {
	s1, s2 := StripUnaryPlus(e1), StripUnaryPlus(e2)
	if n1, n2, ok := numbers(s1, s2); ok {
		return b.createNumberLike(loc, JSPow(n1.Value, n2.Value), n1.IsInteger && n2.IsInteger)
	}
	if IsNumber(s1) {
		e1 = s1
	}
	if IsNumber(s2) {
		e2 = s2
	}
	return newBinaryOpNode(loc, BinPow, e1, e2, rightHasAssignments)
}

func (b *astBuilder) makeMultNode(loc TokenLocation, e1, e2 ExpressionNode, rightHasAssignments bool) ExpressionNode {
	e1, e2 = StripUnaryPlus(e1), StripUnaryPlus(e2)
	if n1, n2, ok := numbers(e1, e2); ok {
		return b.createNumberLike(loc, n1.Value*n2.Value, n1.IsInteger && n2.IsInteger)
	}
	if n, ok := e1.(*NumberNode); ok && n.Value == 1 {
		return b.createUnaryPlus(loc, e2).(ExpressionNode)
	}
	if n, ok := e2.(*NumberNode); ok && n.Value == 1 {
		return b.createUnaryPlus(loc, e1).(ExpressionNode)
	}
	return newBinaryOpNode(loc, BinMult, e1, e2, rightHasAssignments)
}

func (b *astBuilder) makeDivNode(loc TokenLocation, e1, e2 ExpressionNode, rightHasAssignments bool) ExpressionNode {
	e1, e2 = StripUnaryPlus(e1), StripUnaryPlus(e2)
	if n1, n2, ok := numbers(e1, e2); ok {
		result := n1.Value / n2.Value
		if truncateDoubleToInt64(result) == result {
			return b.createNumberLike(loc, result, n1.IsInteger && n2.IsInteger)
		}
		return b.createNumberLike(loc, result, false)
	}
	return newBinaryOpNode(loc, BinDiv, e1, e2, rightHasAssignments)
}

func (b *astBuilder) makeModNode(loc TokenLocation, e1, e2 ExpressionNode, rightHasAssignments bool) ExpressionNode {
	e1, e2 = StripUnaryPlus(e1), StripUnaryPlus(e2)
	if n1, n2, ok := numbers(e1, e2); ok {
		return b.createNumberLike(loc, math.Mod(n1.Value, n2.Value), true)
	}
	return newBinaryOpNode(loc, BinMod, e1, e2, rightHasAssignments)
}

func (b *astBuilder) makeAddNode(loc TokenLocation, e1, e2 ExpressionNode, rightHasAssignments bool) ExpressionNode {
	if n1, n2, ok := numbers(e1, e2); ok {
		return b.createNumberLike(loc, n1.Value+n2.Value, n1.IsInteger && n2.IsInteger)
	}
	return newBinaryOpNode(loc, BinAdd, e1, e2, rightHasAssignments)
}

func (b *astBuilder) makeSubNode(loc TokenLocation, e1, e2 ExpressionNode, rightHasAssignments bool) ExpressionNode {
	e1, e2 = StripUnaryPlus(e1), StripUnaryPlus(e2)
	if n1, n2, ok := numbers(e1, e2); ok {
		return b.createNumberLike(loc, n1.Value-n2.Value, n1.IsInteger && n2.IsInteger)
	}
	return newBinaryOpNode(loc, BinSub, e1, e2, rightHasAssignments)
}

// makeIntBinaryNode folds the int32 bitwise and shift operators.
func (b *astBuilder) makeIntBinaryNode(loc TokenLocation, op BinaryOpKind, e1, e2 ExpressionNode, rightHasAssignments bool) ExpressionNode {
	if n1, n2, ok := numbers(e1, e2); ok {
		var v float64
		switch op {
		case BinLeftShift:
			v = float64(ToInt32(n1.Value) << (ToUInt32(n2.Value) & 0x1f))
		case BinRightShift:
			v = float64(ToInt32(n1.Value) >> (ToUInt32(n2.Value) & 0x1f))
		case BinUnsignedRightShift:
			v = float64(ToUInt32(n1.Value) >> (ToUInt32(n2.Value) & 0x1f))
		case BinBitOr:
			v = float64(ToInt32(n1.Value) | ToInt32(n2.Value))
		case BinBitAnd:
			v = float64(ToInt32(n1.Value) & ToInt32(n2.Value))
		case BinBitXOr:
			v = float64(ToInt32(n1.Value) ^ ToInt32(n2.Value))
		}
		return b.createNumberLike(loc, v, true)
	}
	return newBinaryOpNode(loc, op, e1, e2, rightHasAssignments)
}

func (b *astBuilder) makeCoalesceNode(loc TokenLocation, e1, e2 ExpressionNode) ExpressionNode {
	rt := ResultTypeForCoalesce(ResultDescriptor(e1), ResultDescriptor(e2))
	// Optimization for `x?.y ?? z`.
	if chain, ok := e1.(*OptionalChainNode); ok && !IsDeleteNode(chain.Expr) {
		return &CoalesceNode{ExpressionBase: newExpressionBase(loc, rt), Expr1: chain.Expr, Expr2: e2, HasAbsorbedOptionalChain: true}
	}
	return &CoalesceNode{ExpressionBase: newExpressionBase(loc, rt), Expr1: e1, Expr2: e2}
}

func (b *astBuilder) makeStaticBlockFunctionCallNode(loc TokenLocation, fn TreeExpression, divot, divotStart, divotEnd TextPosition) TreeExpression {
	n := &StaticBlockFunctionCallNode{ExpressionBase: newExpr(loc), Expr: expr(fn)}
	n.SetExceptionSourceCode(divot, divotStart, divotEnd)
	return n
}

func (b *astBuilder) makeFunctionCallNode(loc TokenLocation, tfn TreeExpression, previousBaseWasSuper bool, targs TreeArguments, divotStart, divot, divotEnd TextPosition, callOrApplyChildDepth int, isOptionalCall bool) TreeExpression {
	fn := expr(tfn)
	args := targs.(*ArgumentsNode)
	if _, ok := fn.(*SuperNode); ok {
		b.usesSuperCall()
	}
	// JSC turns calls to @intrinsics into BytecodeIntrinsicNodes here (builtins only).

	if chain, ok := fn.(*OptionalChainNode); ok && isOptionalCall && IsLocation(chain.Expr) {
		// Preserve the `this` value in cases like `a?.b?.()`.
		return b.makeFunctionCallNode(loc, chain.Expr, previousBaseWasSuper, args, divotStart, divot, divotEnd, callOrApplyChildDepth, isOptionalCall)
	}

	switch f := fn.(type) {
	case *ResolveNode:
		if f.Ident == ident.Eval && !isOptionalCall {
			b.usesEval()
			n := &EvalFunctionCallNode{ExpressionBase: newExpr(loc), Args: args}
			n.SetExceptionSourceCode(divot, divotStart, divotEnd)
			return n
		}
		n := &FunctionCallResolveNode{ExpressionBase: newExpr(loc), Ident: f.Ident, Args: args, IsOptionalCall: isOptionalCall}
		n.SetExceptionSourceCode(divot, divotStart, divotEnd)
		return n
	case *BracketAccessorNode:
		n := &FunctionCallBracketNode{ExpressionBase: newExpr(loc), Base: f.Base, Subscript: f.Subscript, Args: args, SubscriptHasAssignments: f.SubscriptHasAssignments, IsOptionalCall: isOptionalCall}
		n.SetExceptionSourceCode(divot, divotStart, divotEnd)
		n.SetSubexpressionInfo(f.Divot, int(f.DivotEnd))
		return n
	case *DotAccessorNode:
		n := &FunctionCallDotNode{ExpressionBase: newExpr(loc), Kind: CallDotNormal, Base: f.Base, Ident: f.Ident, Type: f.Type, Args: args, IsOptionalCall: isOptionalCall}
		baseResolve, baseIsResolve := f.Base.(*ResolveNode)
		switch {
		case !previousBaseWasSuper && (f.Ident == ident.Call || f.Ident == ident.CallPrivateName):
			n.Kind = CallDotCall
			n.DistanceToInnermostCallOrApply = callOrApplyChildDepth
		case !previousBaseWasSuper && (f.Ident == ident.Apply || f.Ident == ident.ApplyPrivateName):
			// The bytecode generator does not inline Reflect.apply.
			if !baseIsResolve || baseResolve.Ident != ident.Reflect {
				n.Kind = CallDotApply
				n.DistanceToInnermostCallOrApply = callOrApplyChildDepth
				if len(args.Args) == 2 {
					if r, ok := args.Args[1].(*ResolveNode); ok && r.Ident == ident.Arguments {
						b.scope.argumentsForwards++
					}
				}
			}
		case !previousBaseWasSuper && f.Ident == ident.HasOwnProperty && len(args.Args) == 1 && IsResolveNode(args.Args[0]):
			// <resolveNode|thisNode>.hasOwnProperty(<resolveNode>)
			if _, isThis := f.Base.(*ThisNode); baseIsResolve || isThis {
				n.Kind = CallDotHasOwnProperty
			}
		case f.Ident == ident.Construct && baseIsResolve && baseResolve.Ident == ident.Reflect && len(args.Args) >= 2:
			n.Kind = CallDotReflectConstruct
		}
		n.SetExceptionSourceCode(divot, divotStart, divotEnd)
		n.SetSubexpressionInfo(f.Divot, int(f.DivotEnd))
		return n
	}
	n := &FunctionCallValueNode{ExpressionBase: newExpr(loc), Expr: fn, Args: args, IsOptionalCall: isOptionalCall}
	n.SetExceptionSourceCode(divot, divotStart, divotEnd)
	return n
}

func (b *astBuilder) makeBinaryNode(loc TokenLocation, token TokenType, lhs, rhs binaryOperand) ExpressionNode {
	l, r, ra := lhs.expr, rhs.expr, rhs.info.hasAssignment
	switch token {
	case COALESCE:
		return b.makeCoalesceNode(loc, l, r)
	case OR, AND:
		op := LogicalAnd
		if token == OR {
			op = LogicalOr
		}
		return &LogicalOpNode{ExpressionBase: newExpressionBase(loc, ResultTypeForLogicalOp(ResultDescriptor(l), ResultDescriptor(r))), Operator: op, Expr1: l, Expr2: r}
	case BITOR:
		return b.makeIntBinaryNode(loc, BinBitOr, l, r, ra)
	case BITXOR:
		return b.makeIntBinaryNode(loc, BinBitXOr, l, r, ra)
	case BITAND:
		return b.makeIntBinaryNode(loc, BinBitAnd, l, r, ra)
	case EQEQ:
		return newBinaryOpNode(loc, BinEqual, l, r, ra)
	case NE:
		return newBinaryOpNode(loc, BinNotEqual, l, r, ra)
	case STREQ:
		return newBinaryOpNode(loc, BinStrictEqual, l, r, ra)
	case STRNEQ:
		return newBinaryOpNode(loc, BinNotStrictEqual, l, r, ra)
	case LT:
		return newBinaryOpNode(loc, BinLess, l, r, ra)
	case GT:
		return newBinaryOpNode(loc, BinGreater, l, r, ra)
	case LE:
		return newBinaryOpNode(loc, BinLessEq, l, r, ra)
	case GE:
		return newBinaryOpNode(loc, BinGreaterEq, l, r, ra)
	case INSTANCEOF, INTOKEN:
		op := BinInstanceOf
		if token == INTOKEN {
			op = BinIn
		}
		n := newBinaryOpNode(loc, op, l, r, ra)
		setExceptionLocation(n, lhs.info.start, rhs.info.start, rhs.info.end)
		return n
	case LSHIFT:
		return b.makeIntBinaryNode(loc, BinLeftShift, l, r, ra)
	case RSHIFT:
		return b.makeIntBinaryNode(loc, BinRightShift, l, r, ra)
	case URSHIFT:
		return b.makeIntBinaryNode(loc, BinUnsignedRightShift, l, r, ra)
	case PLUS:
		return b.makeAddNode(loc, l, r, ra)
	case MINUS:
		return b.makeSubNode(loc, l, r, ra)
	case TIMES:
		return b.makeMultNode(loc, l, r, ra)
	case DIVIDE:
		return b.makeDivNode(loc, l, r, ra)
	case MOD:
		return b.makeModNode(loc, l, r, ra)
	case POW:
		return b.makePowNode(loc, l, r, ra)
	}
	panic("makeBinaryNode: unexpected token " + token.String())
}

func (b *astBuilder) makeAssignNode(loc TokenLocation, l ExpressionNode, op Operator, e ExpressionNode, locHasAssignments, exprHasAssignments bool, start, divot, end TextPosition) ExpressionNode {
	isShortCircuit := op == OpCoalesceEq || op == OpOrEq || op == OpAndEq
	switch n := l.(type) {
	case *ResolveNode:
		if op == OpEqual || isShortCircuit {
			if !n.IsParenthesized() {
				inferName(e, n.Ident)
			}
		}
		if op == OpEqual {
			a := &AssignResolveNode{ExpressionBase: newExpr(loc), Ident: n.Ident, Right: e, AssignmentContext: AssignmentContextAssignmentExpression}
			setExceptionLocation(a, start, divot, end)
			return a
		}
		if isShortCircuit {
			a := &ShortCircuitReadModifyResolveNode{ExpressionBase: newExpr(loc), Ident: n.Ident, Operator: op, Right: e, RightHasAssignments: exprHasAssignments}
			a.SetExceptionSourceCode(divot, start, end)
			return a
		}
		a := &ReadModifyResolveNode{ExpressionBase: newExpr(loc), Ident: n.Ident, Operator: op, Right: e, RightHasAssignments: exprHasAssignments}
		a.SetExceptionSourceCode(divot, start, end)
		return a

	case *BracketAccessorNode:
		if op == OpEqual {
			a := &AssignBracketNode{ExpressionBase: newExpr(loc), Base: n.Base, Subscript: n.Subscript, Right: e, SubscriptHasAssignments: locHasAssignments, RightHasAssignments: exprHasAssignments}
			a.SetExceptionSourceCode(n.Divot, start, end)
			return a
		}
		if isShortCircuit {
			a := &ShortCircuitReadModifyBracketNode{ExpressionBase: newExpr(loc), Base: n.Base, Subscript: n.Subscript, Operator: op, Right: e, SubscriptHasAssignments: locHasAssignments, RightHasAssignments: exprHasAssignments}
			a.SetExceptionSourceCode(divot, start, end)
			a.SetSubexpressionInfo(n.Divot, int(n.DivotEnd))
			return a
		}
		a := &ReadModifyBracketNode{ExpressionBase: newExpr(loc), Base: n.Base, Subscript: n.Subscript, Operator: op, Right: e, SubscriptHasAssignments: locHasAssignments, RightHasAssignments: exprHasAssignments}
		a.SetExceptionSourceCode(divot, start, end)
		a.SetSubexpressionInfo(n.Divot, int(n.DivotEnd))
		return a

	case *DotAccessorNode:
		if op == OpEqual {
			a := &AssignDotNode{ExpressionBase: newExpr(loc), Base: n.Base, Ident: n.Ident, Type: n.Type, Right: e, RightHasAssignments: exprHasAssignments}
			a.SetExceptionSourceCode(n.Divot, start, end)
			return a
		}
		if isShortCircuit {
			a := &ShortCircuitReadModifyDotNode{ExpressionBase: newExpr(loc), Base: n.Base, Ident: n.Ident, Type: n.Type, Operator: op, Right: e, RightHasAssignments: exprHasAssignments}
			a.SetExceptionSourceCode(divot, start, end)
			a.SetSubexpressionInfo(n.Divot, int(n.DivotEnd))
			return a
		}
		a := &ReadModifyDotNode{ExpressionBase: newExpr(loc), Base: n.Base, Ident: n.Ident, Type: n.Type, Operator: op, Right: e, RightHasAssignments: exprHasAssignments}
		a.SetExceptionSourceCode(divot, start, end)
		a.SetSubexpressionInfo(n.Divot, int(n.DivotEnd))
		return a
	}
	// Assigning to a call expression: a runtime ReferenceError (sloppy mode only).
	a := &AssignErrorNode{ExpressionBase: newExpr(loc), Left: l}
	a.SetExceptionSourceCode(divot, start, end)
	return a
}

func (b *astBuilder) makePrefixNode(loc TokenLocation, e TreeExpression, op Operator, start, divot, end TextPosition) TreeExpression {
	b.checkArgumentsLengthModification(expr(e))
	n := &PrefixNode{ExpressionBase: newExpr(loc), Expr: expr(e), Operator: op}
	n.SetExceptionSourceCode(divot, start, end)
	return n
}

func (b *astBuilder) makePostfixNode(loc TokenLocation, e TreeExpression, op Operator, start, divot, end TextPosition) TreeExpression {
	b.checkArgumentsLengthModification(expr(e))
	n := &PostfixNode{ExpressionBase: newExpr(loc), Expr: expr(e), Operator: op}
	n.SetExceptionSourceCode(divot, start, end)
	return n
}

// --- Numeric helpers -------------------------------------------------------------------

// ToInt32 is ECMAScript ToInt32.
func ToInt32(v float64) int32 { return int32(ToUInt32(v)) }

// ToUInt32 is ECMAScript ToUint32.
func ToUInt32(v float64) uint32 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	v = math.Trunc(v)
	if v >= -(1<<63) && v < 1<<63 {
		return uint32(int64(v))
	}
	m := math.Mod(v, 1<<32)
	if m < 0 {
		m += 1 << 32
	}
	return uint32(m)
}

// truncateDoubleToInt64 mirrors WTF's helper: NaN and out-of-range values
// map to INT64_MIN, as x86's cvttsd2si does.
func truncateDoubleToInt64(v float64) float64 {
	if math.IsNaN(v) || v >= 1<<63 || v < -(1<<63) {
		return -(1 << 63)
	}
	return float64(int64(v))
}

// JSPow is Math.pow / ** with ECMAScript semantics, which differ from Go's
// math.Pow for NaN exponents and for |base| == 1 with infinite exponents.
func JSPow(base, exponent float64) float64 {
	if math.IsNaN(exponent) {
		return math.NaN()
	}
	if math.IsInf(exponent, 0) && math.Abs(base) == 1 {
		return math.NaN()
	}
	return math.Pow(base, exponent)
}

// bigIntToDecimal converts BigInt literal digits to decimal (makeBigIntDecimalIdentifier).
func bigIntToDecimal(digits ident.Identifier, radix uint8) (ident.Identifier, bool) {
	if radix == 10 {
		return digits, true
	}
	n, ok := new(big.Int).SetString(digits.String(), int(radix))
	if !ok {
		return ident.Null, false
	}
	return ident.FromString(n.String()), true
}
