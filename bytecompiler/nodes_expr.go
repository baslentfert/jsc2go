package bytecompiler

import (
	"github.com/baslentfert/jsc2go/bytecode"
	"github.com/baslentfert/jsc2go/ident"
	"github.com/baslentfert/jsc2go/parser"
)

// Code generation for primary expressions, literals and property access
// (NodesCodegen.cpp, ConstantNode through BaseDotNode).
//
// Each genXxx function is XxxNode::emitBytecode: it returns the register
// holding the value. dst, if non-nil, is where the caller would like the
// value (a crude form of copy propagation: `x = 1` loads 1 straight into x);
// if dst is given the function must return it. IgnoredResult() as dst means
// the value is not needed.

// RegExpValidator, if set, checks regular expression literals at compile
// time like JSC does with Yarr; an error is thrown as a SyntaxError when the
// literal is evaluated.
var RegExpValidator func(pattern, flags string) error

// emitThrowReferenceErrorAt is ThrowableExpressionData::emitThrowReferenceError.
func (g *Generator) emitThrowReferenceErrorAt(t *parser.ThrowableExpressionData, message string, dst *RegisterID) *RegisterID {
	g.emitExpressionInfo(t.Divot, t.DivotStart, t.DivotEnd)
	g.emitThrowReferenceError(message)
	if dst != nil {
		return dst
	}
	return g.newTemporary()
}

// genExprInConditionContext is ExpressionNode::emitBytecodeInConditionContext.
func (g *Generator) genExprInConditionContext(n parser.ExpressionNode, trueTarget, falseTarget *Label, fallThroughMode FallThroughMode) {
	result := g.emitNode(nil, n)
	if fallThroughMode == FallThroughMeansTrue {
		g.emitJumpIfFalse(result, falseTarget)
	} else {
		g.emitJumpIfTrue(result, trueTarget)
	}
}

// constantValue is ConstantNode::jsValue.
func (g *Generator) constantValue(n parser.ExpressionNode) (bytecode.Constant, bool) {
	switch n := n.(type) {
	case *parser.NullNode:
		return bytecode.Null{}, true
	case *parser.BooleanNode:
		return bytecode.Bool(n.Value), true
	case *parser.NumberNode:
		return bytecode.JSNumber(n.Value), true
	case *parser.StringNode:
		return g.addStringConstant(n.Value), true
	case *parser.BigIntNode:
		return g.addBigIntConstant(n.Value, n.Radix, n.Sign), true
	}
	return nil, false
}

// pureToBoolean is JSValue::pureToBoolean for constants.
func pureToBoolean(c bytecode.Constant) (value bool, known bool) {
	switch c := c.(type) {
	case bytecode.Null, bytecode.Undefined:
		return false, true
	case bytecode.Bool:
		return bool(c), true
	case bytecode.Number:
		return c.Value != 0 && c.Value == c.Value, true
	case bytecode.String:
		return len(c) > 0, true
	case bytecode.BigInt:
		return c.Value.Sign() != 0, true
	}
	return false, false
}

func (g *Generator) genConstantInConditionContext(n parser.ExpressionNode, trueTarget, falseTarget *Label, fallThroughMode FallThroughMode) {
	constant, _ := g.constantValue(n)
	value, known := pureToBoolean(constant)

	if n.BaseNode().NeedsDebugHook() && known {
		g.emitDebugHook(n, nil)
	}

	switch {
	case !known:
		g.genExprInConditionContext(n, trueTarget, falseTarget, fallThroughMode)
	case value && fallThroughMode == FallThroughMeansFalse:
		g.emitJump(trueTarget)
	case !value && fallThroughMode == FallThroughMeansTrue:
		g.emitJump(falseTarget)
	}
	// All other cases are unconditional fall-throughs, like "if (true)".
}

func (g *Generator) genConstant(n parser.ExpressionNode, dst *RegisterID) *RegisterID {
	if dst == g.IgnoredResult() {
		return nil
	}
	constant, _ := g.constantValue(n)
	return g.emitLoad(dst, constant)
}

func (g *Generator) genNumber(n *parser.NumberNode, dst *RegisterID) *RegisterID {
	if dst == g.IgnoredResult() {
		return nil
	}
	repr := bytecode.SourceCodeRepresentationDouble
	if n.IsInteger {
		repr = bytecode.SourceCodeRepresentationInteger
	}
	return g.emitLoadWithRepresentation(dst, bytecode.JSNumber(n.Value), repr)
}

func (g *Generator) genRegExp(n *parser.RegExpNode, dst *RegisterID) *RegisterID {
	if dst == g.IgnoredResult() {
		return nil
	}
	pattern, flags := n.Pattern.String(), n.Flags.String()
	if RegExpValidator != nil {
		if err := RegExpValidator(pattern, flags); err != nil {
			g.emitThrowStaticError(bytecode.ErrorTypeSyntaxError, err.Error())
			return g.emitLoad(g.finalDestination(dst), bytecode.Undefined{})
		}
	}
	return g.emitNewRegExp(g.finalDestination(dst), &bytecode.RegExp{Pattern: pattern, Flags: flags})
}

func (g *Generator) genThis(n *parser.ThisNode, dst *RegisterID) *RegisterID {
	g.ensureThis()
	if dst == g.IgnoredResult() {
		return nil
	}
	result := g.move(dst, &g.thisRegister)
	const thisLength = len("this")
	g.emitProfileTypeRange(&g.thisRegister, n.Position(), n.Position()+parser.TextPosition(thisLength))
	return result
}

// calleeRegisterRef returns a RegisterID for the callee slot.
func calleeRegisterRef() *RegisterID { return newRegisterID(bytecode.CallFrameSlotCallee) }

func (g *Generator) emitHomeObjectForCallee() *RegisterID {
	if (g.isDerivedClassContext() || g.isDerivedConstructorContext()) && g.parseMode() != parser.ClassFieldInitializerMode {
		derivedConstructor := g.emitLoadDerivedConstructorFromArrowFunctionLexicalEnvironment()
		return g.emitGetByID(g.newTemporary(), derivedConstructor, ident.HomeObjectPrivateName)
	}
	return g.emitGetByID(g.newTemporary(), calleeRegisterRef(), ident.HomeObjectPrivateName)
}

// emitSuperBaseForCallee returns the object super.x looks properties up on.
func (g *Generator) emitSuperBaseForCallee() *RegisterID {
	defer g.refScope()()
	homeObject := g.keep(g.emitHomeObjectForCallee())
	return g.emitGetPrototypeOf(g.newTemporary(), homeObject)
}

func (g *Generator) emitGetSuperFunctionForConstruct() *RegisterID {
	if g.isDerivedConstructorContext() {
		return g.emitGetPrototypeOf(g.newTemporary(), g.emitLoadDerivedConstructorFromArrowFunctionLexicalEnvironment())
	}
	return g.emitGetPrototypeOf(g.newTemporary(), calleeRegisterRef())
}

func (g *Generator) genSuper(n *parser.SuperNode, dst *RegisterID) *RegisterID {
	result := g.emitSuperBaseForCallee()
	return g.move(g.finalDestination(dst), result)
}

func (g *Generator) genImport(n *parser.ImportNode, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	importModule := g.keep(g.moveLinkTimeConstant(nil, bytecode.LinkTimeConstantImportModule))
	argumentCount := 1
	if n.Deferred {
		argumentCount = 3
	} else if n.Option != nil {
		argumentCount = 2
	}
	arguments := g.newCallArguments(nil, argumentCount)
	g.emitLoad(arguments.thisRegister(), bytecode.Undefined{})
	g.emitNode(arguments.argumentRegister(0), n.Expr)
	if n.Option != nil {
		g.emitNode(arguments.argumentRegister(1), n.Option)
	} else if n.Deferred {
		g.emitLoad(arguments.argumentRegister(1), bytecode.Undefined{})
	}
	if n.Deferred {
		g.emitLoad(arguments.argumentRegister(2), bytecode.Bool(true))
	}
	return g.emitCall(g.finalDestination(dst, importModule), importModule, NoExpectedFunction, arguments, n.Divot, n.DivotStart, n.DivotEnd, DebuggableCallNo)
}

func (g *Generator) genNewTarget(n *parser.NewTargetNode, dst *RegisterID) *RegisterID {
	if dst == g.IgnoredResult() {
		return nil
	}
	return g.move(dst, g.newTargetRegister)
}

func (g *Generator) genImportMeta(n *parser.ImportMetaNode, dst *RegisterID) *RegisterID {
	return g.emitNode(dst, n.Expr)
}

// isPure is ExpressionNode::isPure: evaluating the node has no side effects
// and its value cannot change during evaluation of other expressions.
func (g *Generator) isPure(n parser.ExpressionNode) bool {
	if parser.IsConstant(n) {
		return true
	}
	if r, ok := n.(*parser.ResolveNode); ok {
		return g.variable(r.Ident, ThisResolutionLocal).Offset().IsStack()
	}
	return false
}

// getFromScopeCanThrow is ResolveNode::getFromScopeCanThrow.
func (g *Generator) getFromScopeCanThrow(r *parser.ResolveNode) bool {
	v := g.variable(r.Ident, ThisResolutionLocal)
	if v.Offset().IsStack() || v.Offset().IsScope() {
		return !g.needsTDZCheck(v)
	}
	return true
}

func identLength(id ident.Identifier) parser.TextPosition {
	return parser.TextPosition(len(id.UTF16()))
}

func (g *Generator) genResolve(n *parser.ResolveNode, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	if n.Ident == ident.Arguments && g.argumentsForwarded {
		g.emitForwardedArgumentsObject()
	}
	v := g.variable(n.Ident, ThisResolutionLocal)
	divot := n.Start + identLength(n.Ident)
	if local := v.Local(); local != nil {
		g.emitExpressionInfo(divot, n.Start, divot)
		g.emitTDZCheckIfNecessary(v, local, nil)
		if dst == g.IgnoredResult() {
			return nil
		}
		g.emitProfileTypeVariable(local, v, n.Position(), n.Position()+identLength(n.Ident))
		return g.move(dst, local)
	}

	g.emitExpressionInfo(divot, n.Start, divot)
	scope := g.keep(g.emitResolveScope(dst, v))
	finalDest := g.finalDestination(dst)
	if !g.needsTDZCheck(v) {
		g.emitGetFromScope(finalDest, scope, v, bytecode.ThrowIfNotFound)
	} else {
		uncheckedResult := g.keep(g.newTemporary())
		g.emitGetFromScope(uncheckedResult, scope, v, bytecode.ThrowIfNotFound)
		g.emitTDZCheck(uncheckedResult)
		g.move(finalDest, uncheckedResult)
	}
	g.emitProfileTypeVariable(finalDest, v, n.Position(), n.Position()+identLength(n.Ident))
	return finalDest
}

func (g *Generator) genTemplateString(n *parser.TemplateStringNode, dst *RegisterID) *RegisterID {
	if dst == g.IgnoredResult() {
		return nil
	}
	return g.emitLoad(dst, g.addStringConstant(n.Cooked))
}

func (g *Generator) genTemplateLiteral(n *parser.TemplateLiteralNode, dst *RegisterID) *RegisterID {
	if len(n.TemplateExpressions) == 0 {
		// Without expressions there is exactly one template string.
		return g.emitNode(dst, n.TemplateStrings[0])
	}
	defer g.refScope()()

	var temporaryRegisters []*RegisterID
	emitString := func(s *parser.TemplateStringNode) {
		if s.Cooked.Len() > 0 {
			r := g.keep(g.newTemporary())
			temporaryRegisters = append(temporaryRegisters, r)
			g.emitNode(r, s)
		}
	}
	for i, expr := range n.TemplateExpressions {
		// Evaluate TemplateString.
		emitString(n.TemplateStrings[i])

		// Evaluate Expression.
		r := g.keep(g.newTemporary())
		temporaryRegisters = append(temporaryRegisters, r)
		g.emitNode(r, expr)
		g.emitToString(r, r)
	}
	// Evaluate tail TemplateString.
	emitString(n.TemplateStrings[len(n.TemplateExpressions)])

	if len(temporaryRegisters) == 1 {
		return g.move(g.finalDestination(dst, temporaryRegisters[0]), temporaryRegisters[0])
	}
	return g.emitStrcat(g.finalDestination(dst, temporaryRegisters[0]), temporaryRegisters[0], len(temporaryRegisters))
}

func (g *Generator) genTaggedTemplate(n *parser.TaggedTemplateNode, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	expectedFunction := NoExpectedFunction
	var tag, base *RegisterID
	optionalChain, _ := n.Tag.(*parser.OptionalChainNode)
	switch {
	case optionalChain != nil && parser.IsLocation(optionalChain.Expr):
		tag = g.keep(g.newTemporary())
		base = g.keep(g.newTemporary())
		g.emitOptionalChainCallee(optionalChain, tag, base)
	case !parser.IsLocation(n.Tag):
		tag = g.keep(g.newTemporary())
		tag = g.keep(g.emitNode(tag, n.Tag))
	default:
		switch t := n.Tag.(type) {
		case *parser.ResolveNode:
			identifier := t.Ident
			expectedFunction = g.expectedFunctionForIdentifier(identifier)

			v := g.variable(identifier, ThisResolutionLocal)
			if local := v.Local(); local != nil {
				g.emitTDZCheckIfNecessary(v, local, nil)
				tag = g.keep(g.move(g.newTemporary(), local))
			} else {
				tag = g.keep(g.newTemporary())
				base = g.keep(g.newTemporary())

				newDivot := n.DivotStart + identLength(identifier)
				g.emitExpressionInfo(newDivot, n.DivotStart, newDivot)
				g.move(base, g.emitResolveScope(base, v))
				g.emitGetFromScope(tag, base, v, bytecode.ThrowIfNotFound)
				g.emitTDZCheckIfNecessary(v, tag, nil)
			}
		case *parser.BracketAccessorNode:
			base = g.keep(g.newTemporary())
			base = g.keep(g.emitNode(base, t.Base))
			property := g.keep(g.emitNodeForProperty(nil, t.Subscript))
			if _, isSuper := t.Base.(*parser.SuperNode); isSuper {
				thisValue := g.keep(g.ensureThis())
				tag = g.keep(g.emitGetByValWithThis(g.newTemporary(), base, thisValue, property))
				base = thisValue
			} else {
				tag = g.keep(g.emitGetByVal(g.newTemporary(), base, property))
			}
		case *parser.DotAccessorNode:
			tag = g.keep(g.newTemporary())
			base = g.keep(g.newTemporary())
			base = g.keep(g.emitNode(base, t.Base))
			var thisValue *RegisterID
			tag = g.keep(g.emitGetPropertyValue(dotOf(t), tag, base, &thisValue))
			if thisValue != nil {
				base = thisValue
			}
		}
	}

	templateObject := g.keep(g.emitGetTemplateObject(nil, n))

	expressions := n.TemplateLiteral.TemplateExpressions
	callArguments := g.newCallArguments(nil, 1+len(expressions))
	if base != nil {
		g.move(callArguments.thisRegister(), base)
	} else {
		g.emitLoad(callArguments.thisRegister(), bytecode.Undefined{})
	}

	g.move(callArguments.argumentRegister(0), templateObject)
	for i, e := range expressions {
		g.emitNode(callArguments.argumentRegister(1+i), e)
	}

	return g.emitCallInTailPosition(g.finalDestination(dst, tag), tag, expectedFunction, callArguments, n.Divot, n.DivotStart, n.DivotEnd, DebuggableCallYes)
}

// --- Arrays ----------------------------------------------------------------------

func (g *Generator) genArray(n *parser.ArrayNode, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	hadVariableExpression := false
	length := 0

	recommendedIndexingType := bytecode.ArrayWithUndecided
	firstPut := 0
	for ; firstPut < len(n.Elements); firstPut++ {
		e := n.Elements[firstPut]
		if _, isSpread := e.Value.(*parser.SpreadExpressionNode); e.Elision > 0 || isSpread {
			break
		}
		if constant, ok := g.constantValue(e.Value); ok {
			recommendedIndexingType = bytecode.LeastUpperBoundOfIndexingTypeAndConstant(recommendedIndexingType, constant)
		} else {
			hadVariableExpression = true
		}
		length++
	}

	newArray := func(dst *RegisterID, elements []*parser.ElementNode, length int) *RegisterID {
		if length > 0 && !hadVariableExpression {
			recommendedIndexingType |= bytecode.CopyOnWrite
			array := &bytecode.ArrayBuffer{IndexingType: recommendedIndexingType}
			for _, element := range elements[:length] {
				constant, _ := g.constantValue(element.Value)
				array.Values = append(array.Values, constant)
			}
			return g.emitNewArrayBuffer(dst, array, recommendedIndexingType)
		}
		return g.emitNewArray(dst, elements, length, recommendedIndexingType)
	}

	if firstPut == len(n.Elements) && n.Elision == 0 {
		return newArray(g.finalDestination(dst), n.Elements, length)
	}

	if firstPut < len(n.Elements) {
		if _, isSpread := n.Elements[firstPut].Value.(*parser.SpreadExpressionNode); isSpread {
			hasElision := n.Elision > 0
			for _, e := range n.Elements[firstPut:] {
				if e.Elision > 0 {
					hasElision = true
					break
				}
			}
			if !hasElision {
				return g.emitNewArrayWithSpread(g.finalDestination(dst), n.Elements)
			}
		}
	}

	array := g.keep(newArray(g.tempDestination(dst), n.Elements, length))
	i := firstPut
	for ; i < len(n.Elements); i++ {
		e := n.Elements[i]
		if _, isSpread := e.Value.(*parser.SpreadExpressionNode); isSpread {
			break
		}
		g.scoped(func() {
			value := g.keep(g.emitNode(nil, e.Value))
			length += e.Elision

			index := g.keep(g.emitLoad(nil, bytecode.JSNumber(float64(length))))
			length++
			g.emitDirectPutByVal(array, index, value)
		})
	}

	if i == len(n.Elements) {
		if n.Elision > 0 {
			value := g.emitLoad(nil, bytecode.JSNumber(float64(n.Elision+length)))
			g.emitPutByID(array, ident.Length, value)
		}
		return g.move(dst, array)
	}

	// handleSpread:
	index := g.keep(g.emitLoad(g.newTemporary(), bytecode.JSNumber(float64(length))))
	spreader := func(g *Generator, value *RegisterID) {
		g.emitDirectPutByVal(array, index, value)
		g.emitInc(index)
	}
	int32Types := bytecode.OperandTypesOf(parser.OperandTypes{First: parser.NumberTypeIsInt32(), Second: parser.NumberTypeIsInt32()})
	for ; i < len(n.Elements); i++ {
		e := n.Elements[i]
		g.scoped(func() {
			if e.Elision > 0 {
				g.emitBinaryOpT(bytecode.OpAddID, index, index, g.emitLoad(nil, bytecode.JSNumber(float64(e.Elision))), int32Types)
			}
			if spread, ok := e.Value.(*parser.SpreadExpressionNode); ok {
				g.emitEnumeration(&spread.ThrowableExpressionData, spread.Expression, spreader, nil, nil)
			} else {
				g.emitDirectPutByVal(array, index, g.emitNode(nil, e.Value))
				g.emitInc(index)
			}
		})
	}

	if n.Elision > 0 {
		g.emitBinaryOpT(bytecode.OpAddID, index, index, g.emitLoad(nil, bytecode.JSNumber(float64(n.Elision))), int32Types)
		g.emitPutByID(array, ident.Length, index)
	}
	return g.move(dst, array)
}

// isSimpleArray reports whether an array literal has no holes or spreads.
func isSimpleArray(n *parser.ArrayNode) bool {
	if n.Elision > 0 {
		return false
	}
	for _, e := range n.Elements {
		if e.Elision > 0 {
			return false
		}
		if _, isSpread := e.Value.(*parser.SpreadExpressionNode); isSpread {
			return false
		}
	}
	return true
}

// arrayToArgumentList is ArrayNode::toArgumentList.
func arrayToArgumentList(n *parser.ArrayNode) *parser.ArgumentsNode {
	args := &parser.ArgumentsNode{}
	for _, e := range n.Elements {
		args.Args = append(args.Args, e.Value)
	}
	return args
}

// --- Object literals ---------------------------------------------------------------

func (g *Generator) genObjectLiteral(n *parser.ObjectLiteralNode, dst *RegisterID) *RegisterID {
	if n.List == nil {
		if dst == g.IgnoredResult() {
			return nil
		}
		return g.emitNewObject(g.finalDestination(dst))
	}
	defer g.refScope()()

	props := n.List.Properties
	var newObject *RegisterID
	if props[0].Type&parser.PropertySpread != 0 {
		spreadSource := props[0].Assign.(*parser.ObjectSpreadExpressionNode).Expression
		// Only one element and it is spread.
		if len(props) == 1 {
			function := g.keep(g.moveLinkTimeConstant(nil, bytecode.LinkTimeConstantCloneObject))
			src := g.keep(g.emitNode(nil, spreadSource))
			args := g.newCallArguments(nil, 0)
			g.move(args.thisRegister(), src)
			return g.emitCall(g.finalDestination(dst, function), function, NoExpectedFunction, args, n.Position(), n.Position(), n.Position(), DebuggableCallNo)
		}

		foundNonConstant := false
		for _, p := range props[1:] {
			if p.Type&(parser.PropertyConstant|parser.PropertyComputed|parser.PropertySpread) != 0 {
				continue
			}
			foundNonConstant = true
			break
		}

		// All properties are simple constants and the first is a spread:
		// clone the object, then materialize the rest.
		if !foundNonConstant {
			function := g.keep(g.moveLinkTimeConstant(nil, bytecode.LinkTimeConstantCloneObject))
			src := g.keep(g.emitNode(nil, spreadSource))
			args := g.newCallArguments(nil, 0)
			g.move(args.thisRegister(), src)
			newObject = g.keep(g.emitCall(g.tempDestination(dst), function, NoExpectedFunction, args, n.Position(), n.Position(), n.Position(), DebuggableCallNo))
			props = props[1:]
		}
	}

	if newObject == nil {
		newObject = g.keep(g.emitNewObject(g.tempDestination(dst)))
	}
	g.emitPropertyList(n.List, props, newObject, nil, nil, nil)
	return g.move(dst, newObject)
}

func (g *Generator) emitPutHomeObject(function, homeObject *RegisterID) {
	g.emitPutByID(function, ident.HomeObjectPrivateName, homeObject)
}

// emitDeclarePrivateFieldNames creates the private symbols of a class's
// #fields in scope.
func (g *Generator) emitDeclarePrivateFieldNames(list *parser.PropertyListNode, scope *RegisterID) {
	defer g.refScope()()
	var createPrivateSymbol *RegisterID
	for _, node := range list.Properties {
		if node.Type&parser.PropertyPrivateField == 0 {
			continue
		}
		if createPrivateSymbol == nil {
			createPrivateSymbol = g.keep(g.moveLinkTimeConstant(nil, bytecode.LinkTimeConstantCreatePrivateSymbol))
		}
		g.scoped(func() {
			arguments := g.newCallArguments(nil, 1)
			g.emitLoad(arguments.thisRegister(), bytecode.Undefined{})
			g.emitLoadIdentifier(arguments.argumentRegister(0), node.Name)
			symbol := g.keep(g.emitCall(g.finalDestination(nil, createPrivateSymbol), createPrivateSymbol, NoExpectedFunction, arguments,
				list.Position(), list.Position(), list.Position(), DebuggableCallNo))

			v := g.variable(node.Name, ThisResolutionLocal)
			g.emitPutToScope(scope, v, symbol, bytecode.DoNotThrowIfNotFound, bytecode.InitializationModeConstInitialization)
		})
	}
}

func needsHomeObject(node parser.ExpressionNode) bool {
	if f, ok := node.(parser.BaseFuncExprNode); ok {
		return f.Metadata().SuperBinding == parser.SuperBindingNeeded
	}
	return false
}

func makeClassElementDefinition(p *parser.PropertyNode) parser.ClassElementDefinition {
	initializerPosition := parser.NoTextPosition
	if p.Assign != nil {
		initializerPosition = p.Assign.BaseNode().Position()
	}
	kind := parser.ClassElementFieldWithLiteralPropertyKey
	switch {
	case p.IsStaticClassBlock():
		kind = parser.ClassElementStaticInitializationBlock
	case p.HasComputedName():
		kind = parser.ClassElementFieldWithComputedPropertyKey
	case p.IsPrivate():
		kind = parser.ClassElementFieldWithPrivatePropertyKey
	}
	return parser.ClassElementDefinition{Ident: p.Name, Position: p.Position, InitializerPosition: initializerPosition, Kind: kind}
}

type getterSetterPair struct{ first, second *parser.PropertyNode }

// getterSetterMap is an insertion-ordered map from names to accessor pairs.
type getterSetterMap struct {
	names []ident.Identifier
	pairs map[ident.Identifier]*getterSetterPair
}

func (m *getterSetterMap) add(name ident.Identifier, node *parser.PropertyNode) (*getterSetterPair, bool) {
	if m.pairs == nil {
		m.pairs = make(map[ident.Identifier]*getterSetterPair)
	}
	if p, ok := m.pairs[name]; ok {
		return p, false
	}
	p := &getterSetterPair{first: node}
	m.pairs[name] = p
	m.names = append(m.names, name)
	return p, true
}

func (m *getterSetterMap) contains(name ident.Identifier) bool {
	_, ok := m.pairs[name]
	return ok
}

// emitPropertyListBytecode is the class form of PropertyListNode::emitBytecode.
func (g *Generator) emitPropertyListBytecode(list *parser.PropertyListNode, constructor, prototype *RegisterID,
	instanceElementDefinitions, staticElementDefinitions *[]parser.ClassElementDefinition) *RegisterID {
	return g.emitPropertyList(list, list.Properties, constructor, prototype, instanceElementDefinitions, staticElementDefinitions)
}

// emitPropertyList is PropertyListNode::emitBytecode for the properties
// props of list (a suffix of list.Properties).
func (g *Generator) emitPropertyList(list *parser.PropertyListNode, props []*parser.PropertyNode, dstOrConstructor, prototype *RegisterID,
	instanceElementDefinitions, staticElementDefinitions *[]parser.ClassElementDefinition) *RegisterID {
	defer g.refScope()()

	if list.HasPrivateAccessors {
		var privateAccessorMap getterSetterMap
		for _, node := range props {
			if node.Type&(parser.PropertyPrivateGetter|parser.PropertyPrivateSetter) == 0 {
				continue
			}
			// Group private getters and setters to store them in one object.
			if pair, isNew := privateAccessorMap.add(node.Name, node); !isNew {
				pair.second = node
			}
		}

		// Then declare the private accessors.
		for _, name := range privateAccessorMap.names {
			pair := privateAccessorMap.pairs[name]
			g.scoped(func() {
				// FIXME: Use GetterSetter to store private accessors.
				getterSetterObj := g.keep(g.emitNewObject(g.newTemporary()))

				emitPutAccessor := func(propertyNode *parser.PropertyNode) {
					defer g.refScope()()
					base := dstOrConstructor
					if propertyNode.IsInstanceClassProperty() {
						base = prototype
					}
					value := g.keep(g.emitNode(nil, propertyNode.Assign))
					if propertyNode.NeedsSuperBinding {
						g.emitPutHomeObject(value, base)
					}
					setterOrGetterIdent := ident.SetPrivateName
					if propertyNode.Type&parser.PropertyPrivateGetter != 0 {
						setterOrGetterIdent = ident.GetPrivateName
					}
					g.emitDirectPutByID(getterSetterObj, setterOrGetterIdent, value)
				}

				if pair.first != nil {
					emitPutAccessor(pair.first)
				}
				if pair.second != nil {
					emitPutAccessor(pair.second)
				}

				v := g.variable(pair.first.Name, ThisResolutionLocal)
				g.emitPutToScope(g.scopeRegister, v, getterSetterObj, bytecode.DoNotThrowIfNotFound, bytecode.InitializationModeConstInitialization)
			})
		}
	}

	var dst *RegisterID
	i := 0

	// Fast case: regular value properties.
	for ; i < len(props) && props[i].Type&parser.PropertyConstant != 0; i++ {
		p := props[i]
		dst = dstOrConstructor
		if p.IsInstanceClassProperty() {
			dst = prototype
		}

		if p.Type&(parser.PropertyPrivateGetter|parser.PropertyPrivateSetter) != 0 {
			continue
		}

		if p.IsComputedClassField() {
			g.emitSaveComputedFieldName(p)
		}

		if p.IsInstanceClassField() && p.Type&parser.PropertyPrivateMethod == 0 {
			*instanceElementDefinitions = append(*instanceElementDefinitions, makeClassElementDefinition(p))
			continue
		}

		if p.IsStaticClassElement() {
			*staticElementDefinitions = append(*staticElementDefinitions, makeClassElementDefinition(p))
			continue
		}

		g.emitPutConstantProperty(list, dst, p)
	}

	if i == len(props) {
		return dstOrConstructor
	}

	// There are get/set properties. Pair getters and setters to define them
	// together, unless a constant property with the same name, a computed
	// property or a spread could override previous values.
	canOverrideProperties := false
	var instanceMap, staticMap getterSetterMap

	for _, node := range props[i:] {
		if node.Type&(parser.PropertyComputed|parser.PropertySpread) != 0 {
			canOverrideProperties = true
			break
		}

		m := &instanceMap
		if node.IsStaticClassProperty() {
			m = &staticMap
		}
		if node.Type&parser.PropertyConstant != 0 {
			if m.contains(node.Name) {
				canOverrideProperties = true
				break
			}
			continue
		}

		// Duplicates are possible.
		pair, isNew := m.add(node.Name, node)
		if !isNew {
			if pair.first.Type == node.Type {
				pair.first.IsOverriddenByDuplicate = true
				pair.first = node
			} else {
				if pair.second != nil {
					pair.second.IsOverriddenByDuplicate = true
				}
				pair.second = node
			}
		}
	}

	// The remaining properties.
	for ; i < len(props); i++ {
		node := props[i]
		dst = dstOrConstructor
		if node.IsInstanceClassProperty() {
			dst = prototype
		}

		if node.IsComputedClassField() {
			g.emitSaveComputedFieldName(node)
		}

		if node.Type&(parser.PropertyPrivateGetter|parser.PropertyPrivateSetter) != 0 {
			continue
		}

		if node.IsInstanceClassField() {
			*instanceElementDefinitions = append(*instanceElementDefinitions, makeClassElementDefinition(node))
			continue
		}

		if node.IsStaticClassElement() {
			*staticElementDefinitions = append(*staticElementDefinitions, makeClassElementDefinition(node))
			continue
		}

		// Regular values.
		if node.Type&parser.PropertyConstant != 0 {
			g.emitPutConstantProperty(list, dst, node)
			continue
		}
		if node.Type&parser.PropertySpread != 0 {
			g.emitNode(dst, node.Assign)
			continue
		}

		g.scoped(func() {
			value := g.keep(g.emitNode(nil, node.Assign))
			if needsHomeObject(node.Assign) {
				g.emitPutHomeObject(value, dst)
			}

			attributes := uint32(propertyAttributeAccessor)
			if node.IsClassProperty() {
				attributes |= propertyAttributeDontEnum
			}

			// A get/set property that a later computed property or spread
			// may override.
			if canOverrideProperties {
				// Computed accessors.
				if node.Type&parser.PropertyComputed != 0 {
					propertyName := g.keep(g.emitNode(nil, node.Expression))
					if g.shouldSetFunctionName(node.Assign) {
						propertyName = g.keep(g.emitToPropertyKey(g.newTemporary(), propertyName))
						g.emitSetFunctionName(value, propertyName)
					}
					if node.Type&parser.PropertyGetter != 0 {
						g.emitPutGetterByVal(dst, propertyName, attributes, value)
					} else {
						g.emitPutSetterByVal(dst, propertyName, attributes, value)
					}
					return
				}

				if node.Type&parser.PropertyGetter != 0 {
					g.emitPutGetterByID(dst, node.Name, attributes, value)
				} else {
					g.emitPutSetterByID(dst, node.Name, attributes, value)
				}
				return
			}

			// A get/set pair.
			m := &instanceMap
			if node.IsStaticClassProperty() {
				m = &staticMap
			}
			pair := m.pairs[node.Name]

			// Was this already generated as a part of its partner?
			if pair.second == node || node.IsOverriddenByDuplicate {
				return
			}

			// Generate the paired node now.
			var getterReg, setterReg, secondReg *RegisterID
			if node.Type&parser.PropertyGetter != 0 {
				getterReg = value
				if pair.second != nil {
					setterReg = g.keep(g.emitNode(nil, pair.second.Assign))
					secondReg = setterReg
				} else {
					setterReg = g.keep(g.emitLoad(nil, bytecode.Undefined{}))
				}
			} else {
				setterReg = value
				if pair.second != nil {
					getterReg = g.keep(g.emitNode(nil, pair.second.Assign))
					secondReg = getterReg
				} else {
					getterReg = g.keep(g.emitLoad(nil, bytecode.Undefined{}))
				}
			}

			if pair.second != nil && needsHomeObject(pair.second.Assign) {
				g.emitPutHomeObject(secondReg, dst)
			}

			g.emitPutGetterSetter(dst, node.Name, attributes, getterReg, setterReg)
		})
	}

	return dstOrConstructor
}

// Property attributes (PropertyAttribute) used by the generator.
const (
	propertyAttributeReadOnly = 1 << 1
	propertyAttributeDontEnum = 1 << 2
	propertyAttributeAccessor = 1 << 4
)

func (g *Generator) emitPutConstantProperty(list *parser.PropertyListNode, newObj *RegisterID, node *parser.PropertyNode) {
	defer g.refScope()()
	position := list.Position()

	if node.IsUnderscoreProtoSetter() {
		prototype := g.keep(g.emitNode(nil, node.Assign))
		g.emitDirectSetPrototypeOf(InvalidPrototypeIgnore, newObj, prototype, position, position, position)
		return
	}

	shouldSetFunctionName := g.shouldSetFunctionName(node.Assign)

	var propertyName *RegisterID
	if node.Name.IsNull() {
		propertyName = g.keep(g.newTemporary())
		if shouldSetFunctionName {
			g.emitToPropertyKey(propertyName, g.emitNode(nil, node.Expression))
		} else {
			g.emitNode(propertyName, node.Expression)
		}
	}

	value := g.keep(g.emitNode(nil, node.Assign))
	if needsHomeObject(node.Assign) {
		g.emitPutHomeObject(value, newObj)
	}

	if node.IsClassProperty() {
		if node.Type&parser.PropertyPrivateMethod != 0 {
			v := g.variable(node.Name, ThisResolutionLocal)
			g.emitPutToScope(g.scopeRegister, v, value, bytecode.DoNotThrowIfNotFound, bytecode.InitializationModeConstInitialization)
			return
		}

		if !node.Name.IsNull() {
			propertyName = g.keep(g.emitLoadIdentifier(nil, node.Name))
		}

		if shouldSetFunctionName {
			g.emitSetFunctionName(value, propertyName)
		}
		g.emitCallDefineProperty(newObj, propertyName, value, nil, nil, PropertyConfigurable|PropertyWritable, position)
		return
	}

	if !node.Name.IsNull() {
		index, isIndex := node.Name.ParseIndex()
		if !isIndex {
			g.emitDirectPutByID(newObj, node.Name, value)
			return
		}
		propertyName = g.keep(g.emitLoad(nil, bytecode.JSNumber(float64(index))))
		g.emitDirectPutByVal(newObj, propertyName, value)
		return
	}

	if shouldSetFunctionName {
		g.emitSetFunctionName(value, propertyName)
	}
	g.emitDirectPutByVal(newObj, propertyName, value)
}

// emitSaveComputedFieldName evaluates a computed class field key and saves
// it in the synthetic private name variable the field's Name refers to.
func (g *Generator) emitSaveComputedFieldName(node *parser.PropertyNode) {
	defer g.refScope()()
	v := g.variable(node.Name, ThisResolutionLocal)

	propertyExpr := g.keep(g.emitNode(nil, node.Expression))
	propertyName := g.keep(g.emitToPropertyKey(g.newTemporary(), propertyExpr))

	if node.IsStaticClassField() {
		validPropertyNameLabel := g.newLabel()
		prototypeString := g.keep(g.emitLoad(nil, g.addStringConstant(ident.Prototype)))
		stringTypes := bytecode.OperandTypesOf(parser.OperandTypes{First: parser.StringType(), Second: parser.StringType()})
		g.emitJumpIfFalse(g.emitBinaryOpT(bytecode.OpStricteqID, g.newTemporary(), prototypeString, propertyName, stringTypes), validPropertyNameLabel)
		g.emitThrowTypeError("Cannot declare a static field named 'prototype'")
		g.emitLabel(validPropertyNameLabel)
	}

	scope := g.keep(g.emitResolveScope(nil, v))
	g.emitPutToScope(scope, v, propertyName, bytecode.ThrowIfNotFound, bytecode.InitializationModeConstInitialization)
}

// --- Property access -----------------------------------------------------------

func isNonIndexStringElement(element parser.ExpressionNode) bool {
	s, ok := element.(*parser.StringNode)
	if !ok {
		return false
	}
	_, isIndex := s.Value.ParseIndex()
	return !isIndex
}

func isSuperNode(n parser.ExpressionNode) bool {
	_, ok := n.(*parser.SuperNode)
	return ok
}

func isOptionalChainBase(n parser.ExpressionNode) bool { return n.ExprBase().IsOptionalChainBase() }

func (g *Generator) genBracketAccessor(n *parser.BracketAccessorNode, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	if isSuperNode(n.Base) {
		finalDest := g.keep(g.finalDestination(dst))
		thisValue := g.keep(g.ensureThis())
		superBase := g.keep(g.emitSuperBaseForCallee())

		if isNonIndexStringElement(n.Subscript) {
			id := n.Subscript.(*parser.StringNode).Value
			g.emitExpressionInfo(n.Divot, n.DivotStart, n.DivotEnd)
			g.emitGetByIDWithThis(finalDest, superBase, thisValue, id)
		} else {
			subscript := g.keep(g.emitNodeForProperty(nil, n.Subscript))
			g.emitExpressionInfo(n.Divot, n.DivotStart, n.DivotEnd)
			g.emitGetByValWithThis(finalDest, superBase, thisValue, subscript)
		}

		g.emitProfileTypeRange(finalDest, n.DivotStart, n.DivotEnd)
		return finalDest
	}

	finalDest := g.keep(g.finalDestination(dst))

	subscriptIsNonIndexString := isNonIndexStringElement(n.Subscript)
	var base *RegisterID
	if subscriptIsNonIndexString {
		base = g.keep(g.emitNode(nil, n.Base))
	} else {
		base = g.emitNodeForLeftHandSide(n.Base, n.SubscriptHasAssignments, g.isPure(n.Subscript))
	}

	if isOptionalChainBase(n.Base) {
		g.emitOptionalCheck(base)
	}

	var ret *RegisterID
	if subscriptIsNonIndexString {
		g.emitExpressionInfo(n.Divot, n.DivotStart, n.DivotEnd)
		ret = g.emitGetByID(finalDest, base, n.Subscript.(*parser.StringNode).Value)
	} else {
		property := g.emitNodeForProperty(nil, n.Subscript)
		g.emitExpressionInfo(n.Divot, n.DivotStart, n.DivotEnd)
		ret = g.emitGetByVal(finalDest, base, property)
	}

	g.emitProfileTypeRange(finalDest, n.DivotStart, n.DivotEnd)
	return ret
}

func (g *Generator) genDotAccessor(n *parser.DotAccessorNode, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	finalDest := g.keep(g.finalDestination(dst))

	if g.shouldGetArgumentsDotLengthFast(n) {
		return g.emitArgumentCount(finalDest)
	}

	var base *RegisterID
	if isSuperNode(n.Base) {
		base = g.keep(g.emitSuperBaseForCallee())
	} else {
		base = g.keep(g.emitNode(nil, n.Base))
		if isOptionalChainBase(n.Base) {
			g.emitOptionalCheck(base)
		}
	}

	g.emitExpressionInfo(n.Divot, n.DivotStart, n.DivotEnd)
	var thisValue *RegisterID
	ret := g.emitGetPropertyValue(dotOf(n), finalDest, base, &thisValue)

	g.emitProfileTypeRange(finalDest, n.DivotStart, n.DivotEnd)
	return ret
}

// baseDot is what JSC's BaseDotNode holds: base.ident or base.#ident.
type baseDot struct {
	base     parser.ExpressionNode
	ident    ident.Identifier
	typ      parser.DotType
	position parser.TextPosition
}

func (d baseDot) isPrivateMember() bool { return d.typ == parser.DotTypePrivateMember }

func dotOf(n *parser.DotAccessorNode) baseDot {
	return baseDot{n.Base, n.Ident, n.Type, n.Position()}
}

// privateNameAccess resolves a private name and checks base's brand; it
// returns the scope holding the name.
func (g *Generator) privateBrandCheck(v Variable, base *RegisterID, isStatic bool) *RegisterID {
	scope := g.keep(g.emitResolveScope(nil, v)) // private names are always captured
	privateBrandSymbol := g.keep(g.emitGetPrivateBrand(g.newTemporary(), scope, isStatic))
	g.emitCheckPrivateBrand(base, privateBrandSymbol, isStatic)
	return scope
}

// emitGetPropertyValue is BaseDotNode::emitGetPropertyValue. thisValue is
// in/out: for super accesses it receives the this value (held by the
// caller's refScope).
func (g *Generator) emitGetPropertyValue(d baseDot, dst, base *RegisterID, thisValue **RegisterID) *RegisterID {
	if d.isPrivateMember() {
		privateTraits := g.getPrivateTraits(d.ident)
		switch {
		case privateTraits.IsMethod():
			v := g.variable(d.ident, ThisResolutionLocal)
			scope := g.privateBrandCheck(v, base, privateTraits.IsStatic())
			return g.emitGetFromScope(dst, scope, v, bytecode.ThrowIfNotFound)

		case privateTraits.IsGetter():
			v := g.variable(d.ident, ThisResolutionLocal)
			scope := g.privateBrandCheck(v, base, privateTraits.IsStatic())
			getterSetterObj := g.keep(g.emitGetFromScope(g.newTemporary(), scope, v, bytecode.ThrowIfNotFound))
			getterFunction := g.keep(g.emitDirectGetByID(g.newTemporary(), getterSetterObj, ident.GetPrivateName))
			args := g.newCallArguments(nil, 0)
			g.move(args.thisRegister(), base)
			return g.emitCall(dst, getterFunction, NoExpectedFunction, args, d.position, d.position, d.position, DebuggableCallYes)

		case privateTraits.IsSetter():
			// The brand check is still required by the spec.
			v := g.variable(d.ident, ThisResolutionLocal)
			g.privateBrandCheck(v, base, privateTraits.IsStatic())
			g.emitThrowTypeError("Trying to access an undefined private getter")
			return dst
		}

		v := g.variable(d.ident, ThisResolutionLocal)
		scope := g.keep(g.emitResolveScope(nil, v)) // private names are always captured
		privateName := g.keep(g.newTemporary())
		g.emitGetFromScope(privateName, scope, v, bytecode.DoNotThrowIfNotFound)
		return g.emitGetPrivateName(dst, base, privateName)
	}

	if isSuperNode(d.base) {
		if *thisValue == nil {
			*thisValue = g.keep(g.ensureThis())
		}
		return g.emitGetByIDWithThis(dst, base, *thisValue, d.ident)
	}

	return g.emitGetByID(dst, base, d.ident)
}

// emitPutProperty is BaseDotNode::emitPutProperty.
func (g *Generator) emitPutProperty(d baseDot, base, value *RegisterID, thisValue **RegisterID) *RegisterID {
	if d.isPrivateMember() {
		privateTraits := g.getPrivateTraits(d.ident)
		switch {
		case privateTraits.IsSetter():
			v := g.variable(d.ident, ThisResolutionLocal)
			scope := g.privateBrandCheck(v, base, privateTraits.IsStatic())
			getterSetterObj := g.keep(g.emitGetFromScope(g.newTemporary(), scope, v, bytecode.ThrowIfNotFound))
			setterFunction := g.keep(g.emitDirectGetByID(g.newTemporary(), getterSetterObj, ident.SetPrivateName))
			args := g.newCallArguments(nil, 1)
			g.move(args.thisRegister(), base)
			g.move(args.argumentRegister(0), value)
			g.emitCallIgnoreResult(g.newTemporary(), setterFunction, NoExpectedFunction, args, d.position, d.position, d.position, DebuggableCallYes)
			return value

		case privateTraits.IsGetter() || privateTraits.IsMethod():
			v := g.variable(d.ident, ThisResolutionLocal)
			g.privateBrandCheck(v, base, privateTraits.IsStatic())
			g.emitThrowTypeError("Trying to access an undefined private setter")
			return value
		}

		v := g.variable(d.ident, ThisResolutionLocal)
		scope := g.keep(g.emitResolveScope(nil, v)) // private names are always captured
		privateName := g.keep(g.newTemporary())
		g.emitGetFromScope(privateName, scope, v, bytecode.DoNotThrowIfNotFound)
		return g.emitPrivateFieldPut(base, privateName, value)
	}

	if isSuperNode(d.base) {
		if *thisValue == nil {
			*thisValue = g.keep(g.ensureThis())
		}
		return g.emitPutByIDWithThis(base, *thisValue, d.ident, value)
	}

	return g.emitPutByID(base, d.ident, value)
}

// emitForwardedArgumentsObject makes the arguments object of a function
// that forwards its arguments (argumentsForwarded) where it is read after
// all: x.apply(y, arguments) when x.apply is not the original. It makes
// the object the prologue left out and puts it in the "arguments"
// variable again.
func (g *Generator) emitForwardedArgumentsObject() {
	if g.ecmaMode.IsStrict() {
		bytecode.EmitCreateClonedArguments(g, vr(g.argumentsRegister))
	} else {
		bytecode.EmitCreateDirectArguments(g, vr(g.argumentsRegister))
	}
	if g.needToInitializeArguments {
		g.initializeVariable(g.variable(ident.Arguments, ThisResolutionLocal), g.argumentsRegister)
	}
}
