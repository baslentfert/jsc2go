package bytecompiler

import (
	"github.com/baslentfert/jsc2go/bytecode"
	"github.com/baslentfert/jsc2go/parser"
)

// Code generation for assignments and the comma operator (NodesCodegen.cpp,
// ReadModifyResolveNode through CommaNode).

// emitReadModifyAssignment emits `src1 op= right` into dst. expressionInfo,
// if given, is emitted after the right side; readOnlyVariable, if given, is
// checked for a read-only write after the right side is evaluated.
func (g *Generator) emitReadModifyAssignment(dst, src1 *RegisterID, right parser.ExpressionNode, op parser.Operator, types bytecode.OperandTypes,
	expressionInfo *parser.ThrowableExpressionData, readOnlyVariable *Variable) *RegisterID {
	var opcode bytecode.OpcodeID
	switch op {
	case parser.OpMultEq:
		opcode = bytecode.OpMulID
	case parser.OpDivEq:
		opcode = bytecode.OpDivID
	case parser.OpPlusEq:
		if isStringAdd(right) {
			result := g.emitStrcatNode(right.(*parser.BinaryOpNode), dst, src1, expressionInfo)
			if readOnlyVariable != nil {
				g.emitReadOnlyExceptionIfNeeded(*readOnlyVariable)
			}
			return result
		}
		opcode = bytecode.OpAddID
	case parser.OpMinusEq:
		opcode = bytecode.OpSubID
	case parser.OpLShift:
		opcode = bytecode.OpLshiftID
	case parser.OpRShift:
		opcode = bytecode.OpRshiftID
	case parser.OpURShift:
		opcode = bytecode.OpUrshiftID
	case parser.OpBitAndEq:
		opcode = bytecode.OpBitandID
	case parser.OpBitXOrEq:
		opcode = bytecode.OpBitxorID
	case parser.OpBitOrEq:
		opcode = bytecode.OpBitorID
	case parser.OpModEq:
		opcode = bytecode.OpModID
	case parser.OpPowEq:
		opcode = bytecode.OpPowID
	default:
		panic("bytecompiler: not a read-modify-write operator")
	}

	src2 := g.emitNode(nil, right)

	if readOnlyVariable != nil {
		if g.emitReadOnlyExceptionIfNeeded(*readOnlyVariable) {
			return src2
		}
	}

	if expressionInfo != nil {
		g.emitExpressionInfo(expressionInfo.Divot, expressionInfo.DivotStart, expressionInfo.DivotEnd)
	}

	result := g.emitBinaryOp(opcode, dst, src1, src2, types)
	if op == parser.OpURShift {
		return g.emitUnsigned(result, result)
	}
	return result
}

// readModifyTypes is OperandTypes(unknownType, right's type).
func readModifyTypes(right parser.ExpressionNode) bytecode.OperandTypes {
	return bytecode.OperandTypesOf(parser.OperandTypes{First: parser.UnknownType(), Second: parser.ResultDescriptor(right)})
}

func (g *Generator) genReadModifyResolve(n *parser.ReadModifyResolveNode, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	newDivot := n.DivotStart + identLength(n.Ident)
	v := g.variable(n.Ident, ThisResolutionLocal)
	types := readModifyTypes(n.Right)
	if local := v.Local(); local != nil {
		g.emitTDZCheckIfNecessary(v, local, nil)
		if v.IsReadOnly() {
			result := g.emitReadModifyAssignment(g.finalDestination(dst), local, n.Right, n.Operator, types, nil, &v)
			g.emitProfileTypeRange(result, n.DivotStart, n.DivotEnd)
			return result
		}

		if g.leftHandSideNeedsCopy(n.RightHasAssignments, g.isPure(n.Right)) {
			result := g.keep(g.newTemporary())
			g.move(result, local)
			g.emitReadModifyAssignment(result, result, n.Right, n.Operator, types, nil, nil)
			g.move(local, result)
			g.emitProfileTypeRange(local, n.DivotStart, n.DivotEnd)
			return g.move(dst, result)
		}

		result := g.emitReadModifyAssignment(local, local, n.Right, n.Operator, types, nil, nil)
		g.emitProfileTypeRange(result, n.DivotStart, n.DivotEnd)
		return g.move(dst, result)
	}

	g.emitExpressionInfo(newDivot, n.DivotStart, newDivot)
	scope := g.keep(g.emitResolveScope(nil, v))
	value := g.keep(g.emitGetFromScope(g.newTemporary(), scope, v, bytecode.ThrowIfNotFound))
	g.emitTDZCheckIfNecessary(v, value, nil)
	var readOnly *Variable
	if v.IsReadOnly() {
		readOnly = &v
	}
	result := g.keep(g.emitReadModifyAssignment(g.finalDestination(dst, value), value, n.Right, n.Operator, types, &n.ThrowableExpressionData, readOnly))
	returnResult := result
	if !v.IsReadOnly() {
		returnResult = g.emitPutToScope(scope, v, result, g.resolveModeForPut(), bytecode.InitializationModeNotInitialization)
		g.emitProfileTypeVariable(result, v, n.DivotStart, n.DivotEnd)
	}
	return returnResult
}

// emitShortCircuitAssignment jumps to afterAssignment if `value op= ...`
// does not assign.
func (g *Generator) emitShortCircuitAssignment(value *RegisterID, op parser.Operator, afterAssignment *Label) {
	switch op {
	case parser.OpCoalesceEq:
		g.emitJumpIfFalse(g.emitIsUndefinedOrNull(g.newTemporary(), value), afterAssignment)
	case parser.OpOrEq:
		g.emitJumpIfTrue(value, afterAssignment)
	case parser.OpAndEq:
		g.emitJumpIfFalse(value, afterAssignment)
	default:
		panic("bytecompiler: not a short-circuit assignment operator")
	}
}

func (g *Generator) genShortCircuitReadModifyResolve(n *parser.ShortCircuitReadModifyResolveNode, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	newDivot := n.DivotStart + identLength(n.Ident)

	v := g.variable(n.Ident, ThisResolutionLocal)
	isReadOnly := v.IsReadOnly()

	if local := g.keep(v.Local()); local != nil {
		g.emitTDZCheckIfNecessary(v, local, nil)

		if isReadOnly {
			result := g.keep(g.tempDestination(dst))
			g.move(result, local)

			afterAssignment := g.newLabel()
			g.emitShortCircuitAssignment(result, n.Operator, afterAssignment)

			g.emitNode(result, n.Right) // Execute side effects first.
			if !g.emitReadOnlyExceptionIfNeeded(v) {
				g.emitProfileTypeRange(result, n.DivotStart, n.DivotEnd)
			}

			g.emitLabel(afterAssignment)
			return g.move(dst, result)
		}

		if g.leftHandSideNeedsCopy(n.RightHasAssignments, g.isPure(n.Right)) {
			result := g.keep(g.tempDestination(dst))
			g.move(result, local)

			afterAssignment := g.newLabel()
			g.emitShortCircuitAssignment(result, n.Operator, afterAssignment)

			g.emitNode(result, n.Right)
			g.move(local, result)
			g.emitProfileTypeVariable(result, v, n.DivotStart, n.DivotEnd)

			g.emitLabel(afterAssignment)
			return g.move(dst, result)
		}

		result := local

		afterAssignment := g.newLabel()
		g.emitShortCircuitAssignment(result, n.Operator, afterAssignment)

		g.emitNode(result, n.Right)
		g.emitProfileTypeVariable(result, v, n.DivotStart, n.DivotEnd)

		g.emitLabel(afterAssignment)
		return g.move(dst, result)
	}

	g.emitExpressionInfo(newDivot, n.DivotStart, newDivot)
	scope := g.keep(g.emitResolveScope(nil, v))

	uncheckedResult := g.keep(g.newTemporary())

	g.emitGetFromScope(uncheckedResult, scope, v, bytecode.ThrowIfNotFound)
	g.emitTDZCheckIfNecessary(v, uncheckedResult, nil)

	afterAssignment := g.newLabel()
	g.emitShortCircuitAssignment(uncheckedResult, n.Operator, afterAssignment)

	g.emitNode(uncheckedResult, n.Right) // Execute side effects first.

	threwException := isReadOnly && g.emitReadOnlyExceptionIfNeeded(v)

	if !threwException {
		g.emitExpressionInfo(n.Divot, n.DivotStart, n.DivotEnd)
	}

	if !isReadOnly {
		g.emitPutToScope(scope, v, uncheckedResult, g.resolveModeForPut(), bytecode.InitializationModeNotInitialization)
		g.emitProfileTypeVariable(uncheckedResult, v, n.DivotStart, n.DivotEnd)
	}

	g.emitLabel(afterAssignment)
	return g.move(g.finalDestination(dst, uncheckedResult), uncheckedResult)
}

func initializationModeForAssignmentContext(context parser.AssignmentContext) bytecode.InitializationMode {
	switch context {
	case parser.AssignmentContextDeclarationStatement:
		return bytecode.InitializationModeInitialization
	case parser.AssignmentContextConstDeclarationStatement, parser.AssignmentContextUsingDeclarationStatement,
		parser.AssignmentContextAwaitUsingDeclarationStatement:
		return bytecode.InitializationModeConstInitialization
	}
	return bytecode.InitializationModeNotInitialization
}

func isUsingOrAwaitUsingAssignmentContext(context parser.AssignmentContext) bool {
	return context == parser.AssignmentContextUsingDeclarationStatement || context == parser.AssignmentContextAwaitUsingDeclarationStatement
}

func (g *Generator) genAssignResolve(n *parser.AssignResolveNode, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	v := g.variable(n.Ident, ThisResolutionLocal)
	context := n.AssignmentContext
	isReadOnly := v.IsReadOnly() && context != parser.AssignmentContextConstDeclarationStatement && !isUsingOrAwaitUsingAssignmentContext(context)
	newDivot := n.DivotStart + identLength(n.Ident)
	isAwaitUsing := context == parser.AssignmentContextAwaitUsingDeclarationStatement
	if local := v.Local(); local != nil {
		var result *RegisterID

		switch {
		case isReadOnly:
			result = g.emitNode(dst, n.Right) // Execute side effects first.

			if context == parser.AssignmentContextAssignmentExpression {
				g.emitExpressionInfo(newDivot, n.DivotStart, newDivot)
				g.emitTDZCheckIfNecessary(v, local, nil)
			}

			g.emitReadOnlyExceptionIfNeeded(v)
			g.emitProfileTypeVariable(result, v, n.DivotStart, n.DivotEnd)
		case (context == parser.AssignmentContextAssignmentExpression && g.needsTDZCheck(v)) || v.IsSpecial():
			tempDst := g.keep(g.tempDestination(dst))
			g.emitNode(tempDst, n.Right) // Execute side effects first.

			if context == parser.AssignmentContextAssignmentExpression {
				g.emitExpressionInfo(newDivot, n.DivotStart, newDivot)
				g.emitTDZCheckIfNecessary(v, local, nil)
			}

			g.move(local, tempDst)
			g.emitProfileTypeVariable(local, v, n.DivotStart, n.DivotEnd)
			result = g.move(dst, tempDst)
		default:
			right := g.emitNode(local, n.Right)
			g.emitProfileTypeVariable(right, v, n.DivotStart, n.DivotEnd)
			result = g.move(dst, right)
		}

		if isUsingOrAwaitUsingAssignmentContext(context) {
			g.emitPrepareDisposable(local, n.DivotStart, isAwaitUsing)
		}

		if context != parser.AssignmentContextAssignmentExpression {
			g.liftTDZCheckIfPossible(v)
		}
		return result
	}

	if g.ecmaMode.IsStrict() {
		g.emitExpressionInfo(newDivot, n.DivotStart, newDivot)
	}
	scope := g.keep(g.emitResolveScope(nil, v))
	if context == parser.AssignmentContextAssignmentExpression {
		g.emitExpressionInfo(newDivot, n.DivotStart, newDivot)
		g.emitTDZCheckIfNecessary(v, nil, scope)
	}
	if dst == g.IgnoredResult() {
		dst = nil
	}
	result := g.keep(g.emitNode(dst, n.Right)) // Execute side effects first.
	g.emitExpressionInfo(newDivot, n.DivotStart, newDivot)
	if isReadOnly {
		if g.emitReadOnlyExceptionIfNeeded(v) {
			return result
		}
	}
	returnResult := result
	if !isReadOnly {
		returnResult = g.emitPutToScope(scope, v, result, g.resolveModeForPut(), initializationModeForAssignmentContext(context))
		g.emitProfileTypeVariable(result, v, n.DivotStart, n.DivotEnd)
	}

	if isUsingOrAwaitUsingAssignmentContext(context) {
		g.emitPrepareDisposable(result, n.DivotStart, isAwaitUsing)
	}

	if context != parser.AssignmentContextAssignmentExpression {
		g.liftTDZCheckIfPossible(v)
	}
	return returnResult
}

func (g *Generator) genAssignDot(n *parser.AssignDotNode, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	base := g.emitNodeForLeftHandSide(n.Base, n.RightHasAssignments, g.isPure(n.Right))
	value := g.keep(g.destinationForAssignResult(dst))
	result := g.keep(g.emitNode(value, n.Right))
	g.emitExpressionInfo(n.Divot, n.DivotStart, n.DivotEnd)
	forwardResult := result
	if dst != g.IgnoredResult() {
		forwardResult = g.keep(g.move(g.tempDestination(result), result))
	}
	var thisValue *RegisterID
	g.emitPutProperty(baseDot{n.Base, n.Ident, n.Type, n.Position()}, base, forwardResult, &thisValue)
	g.emitProfileTypeRange(forwardResult, n.DivotStart, n.DivotEnd)
	return g.move(dst, forwardResult)
}

func (g *Generator) genReadModifyDot(n *parser.ReadModifyDotNode, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	d := baseDot{n.Base, n.Ident, n.Type, n.Position()}
	base := g.emitNodeForLeftHandSide(n.Base, n.RightHasAssignments, g.isPure(n.Right))

	g.emitExpressionInfo(n.SubexpressionDivot(), n.SubexpressionStart(), n.SubexpressionEnd())
	var thisValue *RegisterID
	value := g.keep(g.emitGetPropertyValue(d, g.tempDestination(dst), base, &thisValue))

	updatedValue := g.emitReadModifyAssignment(g.finalDestination(dst, value), value, n.Right, n.Operator, readModifyTypes(n.Right), nil, nil)

	g.emitExpressionInfo(n.Divot, n.DivotStart, n.DivotEnd)
	ret := g.keep(g.emitPutProperty(d, base, updatedValue, &thisValue))
	g.emitProfileTypeRange(updatedValue, n.DivotStart, n.DivotEnd)
	return ret
}

func (g *Generator) genShortCircuitReadModifyDot(n *parser.ShortCircuitReadModifyDotNode, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	d := baseDot{n.Base, n.Ident, n.Type, n.Position()}
	base := g.emitNodeForLeftHandSide(n.Base, n.RightHasAssignments, g.isPure(n.Right))
	var thisValue *RegisterID

	result := g.keep(g.tempDestination(dst))

	g.emitExpressionInfo(n.SubexpressionDivot(), n.SubexpressionStart(), n.SubexpressionEnd())
	g.emitGetPropertyValue(d, result, base, &thisValue)
	afterAssignment := g.newLabel()
	g.emitShortCircuitAssignment(result, n.Operator, afterAssignment)

	g.emitNode(result, n.Right)
	g.emitExpressionInfo(n.Divot, n.DivotStart, n.DivotEnd)
	g.emitPutProperty(d, base, result, &thisValue)
	g.emitProfileTypeRange(result, n.DivotStart, n.DivotEnd)

	g.emitLabel(afterAssignment)
	return g.move(dst, result)
}

func (g *Generator) genAssignError(n *parser.AssignErrorNode, dst *RegisterID) *RegisterID {
	g.emitNode(nil, n.Left)
	return g.emitThrowReferenceErrorAt(&n.ThrowableExpressionData, "Left side of assignment is not a reference.", dst)
}

func (g *Generator) genAssignBracket(n *parser.AssignBracketNode, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	var context *forInContext
	if r, ok := n.Subscript.(*parser.ResolveNode); ok {
		argumentVariable := g.variable(r.Ident, ThisResolutionLocal)
		if argumentVariable.IsLocal() {
			context = g.findForInContext(argumentVariable.Local())
		}
	}

	base := g.emitNodeForLeftHandSide(n.Base, n.SubscriptHasAssignments || n.RightHasAssignments, g.isPure(n.Subscript) && g.isPure(n.Right))
	property := g.emitNodeForLeftHandSideForProperty(n.Subscript, n.RightHasAssignments, g.isPure(n.Right))
	value := g.keep(g.destinationForAssignResult(dst))
	result := g.keep(g.emitNode(value, n.Right))

	g.emitExpressionInfo(n.Divot, n.DivotStart, n.DivotEnd)
	forwardResult := result
	if dst != g.IgnoredResult() {
		forwardResult = g.move(g.tempDestination(result), result)
	}

	if isNonIndexStringElement(n.Subscript) {
		id := n.Subscript.(*parser.StringNode).Value
		if isSuperNode(n.Base) {
			thisValue := g.keep(g.ensureThis())
			g.emitPutByIDWithThis(base, thisValue, id, forwardResult)
		} else {
			g.emitPutByID(base, id, forwardResult)
		}
	} else if isSuperNode(n.Base) {
		thisValue := g.keep(g.ensureThis())
		g.emitPutByValWithThis(base, thisValue, property, forwardResult)
	} else if context != nil {
		g.emitEnumeratorPutByVal(context, base, property, forwardResult)
	} else {
		g.emitPutByVal(base, property, forwardResult)
	}

	g.emitProfileTypeRange(forwardResult, n.DivotStart, n.DivotEnd)
	return g.move(dst, forwardResult)
}

// emitBracketReadModifyOperands evaluates base[subscript] for read-modify
// bracket assignments; a non-literal key is converted once.
func (g *Generator) emitBracketReadModifyOperands(base, subscript, right parser.ExpressionNode, subscriptHasAssignments, rightHasAssignments bool) (*RegisterID, *RegisterID) {
	baseReg := g.emitNodeForLeftHandSide(base, subscriptHasAssignments || rightHasAssignments, g.isPure(subscript) && g.isPure(right))
	property := g.emitNodeForLeftHandSideForProperty(subscript, rightHasAssignments, g.isPure(right))
	if !parser.IsNumber(subscript) && !parser.IsString(subscript) {
		// Don't evaluate the key at all if the base isn't subscriptable.
		g.emitRequireObjectCoercible(baseReg, "Cannot access property of undefined or null")
		property = g.keep(g.emitToPropertyKeyOrNumber(g.newTemporary(), property))
	}
	return baseReg, property
}

func (g *Generator) genReadModifyBracket(n *parser.ReadModifyBracketNode, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	base, property := g.emitBracketReadModifyOperands(n.Base, n.Subscript, n.Right, n.SubscriptHasAssignments, n.RightHasAssignments)

	g.emitExpressionInfo(n.SubexpressionDivot(), n.SubexpressionStart(), n.SubexpressionEnd())
	var value, thisValue *RegisterID
	if isSuperNode(n.Base) {
		thisValue = g.keep(g.ensureThis())
		value = g.keep(g.emitGetByValWithThis(g.tempDestination(dst), base, thisValue, property))
	} else {
		value = g.keep(g.emitGetByVal(g.tempDestination(dst), base, property))
	}
	updatedValue := g.emitReadModifyAssignment(g.finalDestination(dst, value), value, n.Right, n.Operator, readModifyTypes(n.Right), nil, nil)

	g.emitExpressionInfo(n.Divot, n.DivotStart, n.DivotEnd)
	if isSuperNode(n.Base) {
		g.emitPutByValWithThis(base, thisValue, property, updatedValue)
	} else {
		g.emitPutByVal(base, property, updatedValue)
	}
	g.emitProfileTypeRange(updatedValue, n.DivotStart, n.DivotEnd)

	return updatedValue
}

func (g *Generator) genShortCircuitReadModifyBracket(n *parser.ShortCircuitReadModifyBracketNode, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	base, property := g.emitBracketReadModifyOperands(n.Base, n.Subscript, n.Right, n.SubscriptHasAssignments, n.RightHasAssignments)

	var thisValue *RegisterID
	result := g.keep(g.tempDestination(dst))

	g.emitExpressionInfo(n.SubexpressionDivot(), n.SubexpressionStart(), n.SubexpressionEnd())
	if isSuperNode(n.Base) {
		thisValue = g.keep(g.ensureThis())
		g.emitGetByValWithThis(result, base, thisValue, property)
	} else {
		g.emitGetByVal(result, base, property)
	}

	afterAssignment := g.newLabel()
	g.emitShortCircuitAssignment(result, n.Operator, afterAssignment)

	g.emitNode(result, n.Right)
	g.emitExpressionInfo(n.Divot, n.DivotStart, n.DivotEnd)
	if isSuperNode(n.Base) {
		g.emitPutByValWithThis(base, thisValue, property, result)
	} else {
		g.emitPutByVal(base, property, result)
	}
	g.emitProfileTypeRange(result, n.DivotStart, n.DivotEnd)

	g.emitLabel(afterAssignment)
	return g.move(dst, result)
}

func (g *Generator) genComma(n *parser.CommaNode, dst *RegisterID) *RegisterID {
	last := len(n.Exprs) - 1
	for _, e := range n.Exprs[:last] {
		g.emitNodeInIgnoreResultPosition(e)
	}
	return g.emitNodeInTailPosition(dst, n.Exprs[last])
}

func (g *Generator) genCommaInConditionContext(n *parser.CommaNode, trueTarget, falseTarget *Label, fallThroughMode FallThroughMode) {
	if n.NeedsDebugHook() {
		g.emitDebugHook(n, nil)
	}
	last := len(n.Exprs) - 1
	for _, e := range n.Exprs[:last] {
		g.emitNodeInIgnoreResultPosition(e)
	}
	g.emitNodeInConditionContext(n.Exprs[last], trueTarget, falseTarget, fallThroughMode)
}
