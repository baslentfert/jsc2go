package bytecompiler

import (
	"github.com/baslentfert/jsc2go/bytecode"
	"github.com/baslentfert/jsc2go/parser"
)

// Code generation for binary, logical, coalescing, optional chaining and
// conditional expressions (NodesCodegen.cpp, BinaryOpNode through
// ConditionalNode).

func binaryOpcode(op parser.BinaryOpKind) bytecode.OpcodeID {
	switch op {
	case parser.BinPow:
		return bytecode.OpPowID
	case parser.BinMult:
		return bytecode.OpMulID
	case parser.BinDiv:
		return bytecode.OpDivID
	case parser.BinMod:
		return bytecode.OpModID
	case parser.BinAdd:
		return bytecode.OpAddID
	case parser.BinSub:
		return bytecode.OpSubID
	case parser.BinLeftShift:
		return bytecode.OpLshiftID
	case parser.BinRightShift:
		return bytecode.OpRshiftID
	case parser.BinUnsignedRightShift:
		return bytecode.OpUrshiftID
	case parser.BinLess:
		return bytecode.OpLessID
	case parser.BinGreater:
		return bytecode.OpGreaterID
	case parser.BinLessEq:
		return bytecode.OpLesseqID
	case parser.BinGreaterEq:
		return bytecode.OpGreatereqID
	case parser.BinInstanceOf:
		return bytecode.OpInstanceofID
	case parser.BinIn:
		return bytecode.OpInByValID
	case parser.BinEqual:
		return bytecode.OpEqID
	case parser.BinNotEqual:
		return bytecode.OpNeqID
	case parser.BinStrictEqual:
		return bytecode.OpStricteqID
	case parser.BinNotStrictEqual:
		return bytecode.OpNstricteqID
	case parser.BinBitAnd:
		return bytecode.OpBitandID
	case parser.BinBitOr:
		return bytecode.OpBitorID
	default:
		return bytecode.OpBitxorID
	}
}

func operandTypes(a, b parser.ExpressionNode) bytecode.OperandTypes {
	return bytecode.OperandTypesOf(parser.OperandTypes{First: parser.ResultDescriptor(a), Second: parser.ResultDescriptor(b)})
}

// isStringAdd reports whether n is an addition that statically produces a
// string (AddNode with a definitely-string result).
func isStringAdd(n parser.ExpressionNode) bool {
	b, ok := n.(*parser.BinaryOpNode)
	return ok && b.Op == parser.BinAdd && parser.ResultDescriptor(b).DefinitelyIsString()
}

// emitStrcatNode is BinaryOpNode::emitStrcat: a tree of string additions
//
//	(a)...     (b)
//	     \   /
//	      (+)     (c)
//	         \   /
//	 [d]     ((+))
//	    \    /
//	     [+=]
//
// becomes a single op_strcat. n is the root addition ((+)) and lhs, if any,
// is the register of the left side of a += around it. The values are
// converted to primitives in the same order as the additions would.
func (g *Generator) emitStrcatNode(n *parser.BinaryOpNode, dst, lhs *RegisterID, emitExpressionInfoForMe *parser.ThrowableExpressionData) *RegisterID {
	defer g.refScope()()

	// The right children of the string additions, right to left; for the
	// example this is [c, b]. The leftmost child (a) is handled separately.
	reverseExpressionList := []parser.ExpressionNode{n.Expr2}
	leftMostAddChild := n.Expr1
	for isStringAdd(leftMostAddChild) {
		add := leftMostAddChild.(*parser.BinaryOpNode)
		reverseExpressionList = append(reverseExpressionList, add.Expr2)
		leftMostAddChild = add.Expr1
	}

	var temporaryRegisters []*RegisterID

	// A temporary for the converted left side of an assignment.
	if lhs != nil {
		temporaryRegisters = append(temporaryRegisters, g.keep(g.newTemporary()))
	}

	// The leftmost node, (a).
	leftMostAddChildTempRegister := g.keep(g.newTemporary())
	temporaryRegisters = append(temporaryRegisters, leftMostAddChildTempRegister)
	g.emitNode(leftMostAddChildTempRegister, leftMostAddChild)

	// The evaluation and conversion order of the additions is:
	//   evaluate (a), evaluate (b), convert (a), convert (b),
	//   evaluate (c), convert (c), [convert (d)]
	// so (a) is converted after (b) is generated. A string literal needs no
	// conversion.
	if parser.IsString(leftMostAddChild) {
		leftMostAddChildTempRegister = nil
	}

	for i := len(reverseExpressionList) - 1; i >= 0; i-- {
		node := reverseExpressionList[i]

		r := g.keep(g.newTemporary())
		temporaryRegisters = append(temporaryRegisters, r)
		g.emitNode(r, node)

		// After generating the second node, convert the leftmost operand.
		if leftMostAddChildTempRegister != nil {
			g.emitToPrimitive(leftMostAddChildTempRegister, leftMostAddChildTempRegister)
			leftMostAddChildTempRegister = nil
		}
		if !parser.IsString(node) {
			g.emitToPrimitive(r, r)
		}
	}

	// Some read-modify nodes need their expression info after the right
	// side has been generated.
	if emitExpressionInfoForMe != nil {
		g.emitExpressionInfo(emitExpressionInfoForMe.Divot, emitExpressionInfoForMe.DivotStart, emitExpressionInfoForMe.DivotEnd)
	}
	// Convert the left side of the assignment last.
	if lhs != nil {
		g.emitToPrimitive(temporaryRegisters[0], lhs)
	}

	return g.emitStrcat(g.finalDestination(dst, temporaryRegisters[0]), temporaryRegisters[0], len(temporaryRegisters))
}

// triState is WTF::TriState.
type triState uint8

const (
	triFalse triState = iota
	triTrue
	triIndeterminate
)

func canFoldToBranch(opcode bytecode.OpcodeID, branchExpression parser.ExpressionNode, constant bytecode.Constant) bool {
	expressionType := parser.ResultDescriptor(branchExpression)
	_, isBool := constant.(bytecode.Bool)
	n, isNumber := constant.(bytecode.Number)
	isInt32 := isNumber && n.Int32

	switch {
	case expressionType.DefinitelyIsBoolean() && isBool:
		return true
	case expressionType.DefinitelyIsBoolean() && isInt32 && (n.Value == 0 || n.Value == 1):
		// Strict equality is false when the types don't match.
		return opcode == bytecode.OpEqID || opcode == bytecode.OpNeqID
	case expressionType.IsInt32() && isInt32 && n.Value == 0:
		return true
	}
	return false
}

// tryFoldToBranch turns comparisons of a boolean or int32 expression with a
// constant into a branch on the expression itself.
func (g *Generator) tryFoldToBranch(n *parser.BinaryOpNode) (triState, parser.ExpressionNode) {
	var constantNode, branchExpression parser.ExpressionNode
	if parser.IsConstant(n.Expr1) {
		constantNode, branchExpression = n.Expr1, n.Expr2
	} else if parser.IsConstant(n.Expr2) {
		constantNode, branchExpression = n.Expr2, n.Expr1
	} else {
		return triIndeterminate, nil
	}

	opcode := binaryOpcode(n.Op)
	value, _ := g.constantValue(constantNode)
	if !canFoldToBranch(opcode, branchExpression, value) {
		return triIndeterminate, nil
	}

	truthy, _ := pureToBoolean(value)
	switch opcode {
	case bytecode.OpEqID, bytecode.OpStricteqID:
		if truthy {
			return triTrue, branchExpression
		}
		return triFalse, branchExpression
	case bytecode.OpNeqID, bytecode.OpNstricteqID:
		if !truthy {
			return triTrue, branchExpression
		}
		return triFalse, branchExpression
	}
	return triIndeterminate, nil
}

func (g *Generator) genBinaryOpInConditionContext(n *parser.BinaryOpNode, trueTarget, falseTarget *Label, fallThroughMode FallThroughMode) {
	branchCondition, branchExpression := g.tryFoldToBranch(n)

	if n.NeedsDebugHook() && branchCondition != triIndeterminate {
		g.emitDebugHook(n, nil)
	}

	switch branchCondition {
	case triIndeterminate:
		g.genExprInConditionContext(n, trueTarget, falseTarget, fallThroughMode)
	case triTrue:
		g.emitNodeInConditionContext(branchExpression, trueTarget, falseTarget, fallThroughMode)
	default:
		g.emitNodeInConditionContext(branchExpression, falseTarget, trueTarget, invertFallThroughMode(fallThroughMode))
	}
}

func (g *Generator) genBinaryOp(n *parser.BinaryOpNode, dst *RegisterID) *RegisterID {
	switch n.Op {
	case parser.BinEqual:
		return g.genEqual(n, dst)
	case parser.BinStrictEqual:
		return g.genStrictEqual(n, dst)
	case parser.BinInstanceOf:
		return g.genInstanceOf(n, dst)
	case parser.BinIn:
		return g.genIn(n, dst)
	}
	defer g.refScope()()

	opcode := binaryOpcode(n.Op)

	switch opcode {
	case bytecode.OpLessID, bytecode.OpLesseqID, bytecode.OpGreaterID, bytecode.OpGreatereqID:
		// Comparisons of unsigned values (x >>> 0, or non-negative int32
		// constants) become unsigned comparisons.
		const (
			uint32Result = iota + 1
			uint32Constant
		)
		isUInt32 := func(node parser.ExpressionNode) int {
			if b, ok := node.(*parser.BinaryOpNode); ok && b.Op == parser.BinUnsignedRightShift {
				return uint32Result
			}
			if num, ok := node.(*parser.NumberNode); ok && num.IsInteger {
				if v := bytecode.JSNumber(num.Value); v.Int32 && v.Value >= 0 {
					return uint32Constant
				}
			}
			return 0
		}
		leftResult, rightResult := isUInt32(n.Expr1), isUInt32(n.Expr2)
		if leftResult != 0 && rightResult != 0 && (leftResult == uint32Result || rightResult == uint32Result) {
			left, right := n.Expr1, n.Expr2
			if b, ok := left.(*parser.BinaryOpNode); ok {
				b.ShouldToUnsignedResult = false
			}
			if b, ok := right.(*parser.BinaryOpNode); ok {
				b.ShouldToUnsignedResult = false
			}
			src1 := g.emitNodeForLeftHandSide(left, n.RightHasAssignments, g.isPure(right))
			src2 := g.keep(g.emitNode(nil, right))
			g.emitExpressionInfo(n.Position(), n.Position(), n.Position())

			// Both sides are unsigned, so swapping operands is not observable.
			replaceOperands := false
			var resultOp bytecode.OpcodeID
			switch opcode {
			case bytecode.OpLessID:
				resultOp = bytecode.OpBelowID
			case bytecode.OpLesseqID:
				resultOp = bytecode.OpBeloweqID
			case bytecode.OpGreaterID:
				resultOp = bytecode.OpBelowID
				replaceOperands = true
			case bytecode.OpGreatereqID:
				resultOp = bytecode.OpBeloweqID
				replaceOperands = true
			}
			types := operandTypes(left, right)
			if replaceOperands {
				src1, src2 = src2, src1
				types = operandTypes(right, left)
			}
			return g.emitBinaryOp(resultOp, g.finalDestination(dst, src1), src1, src2, types)
		}
	}

	if opcode == bytecode.OpAddID && isStringAdd(n.Expr1) {
		g.emitExpressionInfo(n.Position(), n.Position(), n.Position())
		return g.emitStrcatNode(n, dst, nil, nil)
	}

	if opcode == bytecode.OpNeqID {
		_, null1 := n.Expr1.(*parser.NullNode)
		_, null2 := n.Expr2.(*parser.NullNode)
		if null1 || null2 {
			other := n.Expr1
			if null1 {
				other = n.Expr2
			}
			src := g.keep(g.emitNode(nil, other))
			return g.emitUnaryOpT(bytecode.EmitNeqNull, g.finalDestination(dst, src), src)
		}
	}

	left, right := n.Expr1, n.Expr2
	if (opcode == bytecode.OpNeqID || opcode == bytecode.OpNstricteqID) && parser.IsString(left) {
		left, right = right, left
	}

	src1 := g.emitNodeForLeftHandSide(left, n.RightHasAssignments, g.isPure(right))
	wasTypeof := g.lastOpcodeID == bytecode.OpTypeofID
	src2 := g.keep(g.emitNode(nil, right))
	g.emitExpressionInfo(n.Position(), n.Position(), n.Position())
	if wasTypeof && (opcode == bytecode.OpNeqID || opcode == bytecode.OpNstricteqID) {
		tmp := g.keep(g.tempDestination(dst))
		if opcode == bytecode.OpNeqID {
			g.emitEqualityOp(bytecode.OpEqID, g.finalDestination(tmp, src1), src1, src2)
		} else {
			g.emitEqualityOp(bytecode.OpStricteqID, g.finalDestination(tmp, src1), src1, src2)
		}
		return g.emitUnaryOpT(bytecode.EmitNot, g.finalDestination(dst, tmp), tmp)
	}
	result := g.emitBinaryOp(opcode, g.finalDestination(dst, src1), src1, src2, operandTypes(left, right))
	if n.ShouldToUnsignedResult && opcode == bytecode.OpUrshiftID && dst != g.IgnoredResult() {
		return g.emitUnsigned(result, result)
	}
	return result
}

func (g *Generator) genEqual(n *parser.BinaryOpNode, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	_, null1 := n.Expr1.(*parser.NullNode)
	_, null2 := n.Expr2.(*parser.NullNode)
	if null1 || null2 {
		other := n.Expr1
		if null1 {
			other = n.Expr2
		}
		src := g.keep(g.emitNode(nil, other))
		return g.emitUnaryOpT(bytecode.EmitEqNull, g.finalDestination(dst, src), src)
	}

	left, right := n.Expr1, n.Expr2
	if parser.IsString(left) {
		left, right = right, left
	}

	src1 := g.emitNodeForLeftHandSide(left, n.RightHasAssignments, g.isPure(n.Expr2))
	src2 := g.keep(g.emitNode(nil, right))
	return g.emitEqualityOp(bytecode.OpEqID, g.finalDestination(dst, src1), src1, src2)
}

func (g *Generator) genStrictEqual(n *parser.BinaryOpNode, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	left, right := n.Expr1, n.Expr2
	if parser.IsString(left) {
		left, right = right, left
	}

	src1 := g.emitNodeForLeftHandSide(left, n.RightHasAssignments, g.isPure(n.Expr2))
	src2 := g.keep(g.emitNode(nil, right))
	return g.emitEqualityOp(bytecode.OpStricteqID, g.finalDestination(dst, src1), src1, src2)
}

func (g *Generator) genInstanceOf(n *parser.BinaryOpNode, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	value := g.emitNodeForLeftHandSide(n.Expr1, n.RightHasAssignments, g.isPure(n.Expr2))
	dstReg := g.keep(g.finalDestination(dst, value))
	constructor := g.keep(g.emitNode(nil, n.Expr2))
	hasInstanceOrPrototype := g.keep(g.newTemporary())
	g.emitExpressionInfo(n.Divot, n.DivotStart, n.DivotEnd)
	return g.emitInstanceof(dstReg, value, constructor, hasInstanceOrPrototype)
}

func (g *Generator) genIn(n *parser.BinaryOpNode, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	if p, ok := n.Expr1.(*parser.PrivateIdentifierNode); ok {
		base := g.keep(g.emitNode(nil, n.Expr2))

		privateTraits := g.getPrivateTraits(p.Ident)
		v := g.variable(p.Ident, ThisResolutionLocal)
		scope := g.keep(g.emitResolveScope(nil, v)) // private names are always captured

		if privateTraits.IsField() {
			privateName := g.keep(g.emitGetFromScope(g.newTemporary(), scope, v, bytecode.DoNotThrowIfNotFound))
			return g.emitHasPrivateName(g.finalDestination(dst, base), base, privateName)
		}

		privateBrand := g.keep(g.emitGetPrivateBrand(g.newTemporary(), scope, privateTraits.IsStatic()))
		return g.emitHasPrivateBrand(g.finalDestination(dst, base), base, privateBrand, privateTraits.IsStatic())
	}

	if isNonIndexStringElement(n.Expr1) {
		base := g.keep(g.emitNode(nil, n.Expr2))
		g.emitExpressionInfo(n.Divot, n.DivotStart, n.DivotEnd)
		return g.emitInByID(g.finalDestination(dst, base), base, n.Expr1.(*parser.StringNode).Value)
	}

	key := g.emitNodeForLeftHandSide(n.Expr1, n.RightHasAssignments, g.isPure(n.Expr2))
	base := g.keep(g.emitNode(nil, n.Expr2))
	g.emitExpressionInfo(n.Divot, n.DivotStart, n.DivotEnd)
	return g.emitInByVal(g.finalDestination(dst, key), key, base)
}

// --- Logical operators ---------------------------------------------------------

func (g *Generator) genLogicalOp(n *parser.LogicalOpNode, dst *RegisterID) *RegisterID {
	if dst == g.IgnoredResult() {
		afterExpr1 := g.newLabel()
		afterExpr2 := g.newLabel()
		if n.Operator == parser.LogicalAnd {
			g.emitNodeInConditionContext(n.Expr1, afterExpr1, afterExpr2, FallThroughMeansTrue)
		} else {
			g.emitNodeInConditionContext(n.Expr1, afterExpr2, afterExpr1, FallThroughMeansFalse)
		}
		g.emitLabel(afterExpr1)

		g.emitNodeInTailPosition(dst, n.Expr2)
		g.emitLabel(afterExpr2)
		return dst
	}
	defer g.refScope()()

	temp := g.keep(g.tempDestination(dst))
	target := g.newLabel()

	g.emitNode(temp, n.Expr1)
	if n.Operator == parser.LogicalAnd {
		g.emitJumpIfFalse(temp, target)
	} else {
		g.emitJumpIfTrue(temp, target)
	}
	g.emitNodeInTailPosition(temp, n.Expr2)
	g.emitLabel(target)

	return g.move(dst, temp)
}

func (g *Generator) genLogicalOpInConditionContext(n *parser.LogicalOpNode, trueTarget, falseTarget *Label, fallThroughMode FallThroughMode) {
	if n.NeedsDebugHook() {
		g.emitDebugHook(n, nil)
	}

	afterExpr1 := g.newLabel()
	if n.Operator == parser.LogicalAnd {
		g.emitNodeInConditionContext(n.Expr1, afterExpr1, falseTarget, FallThroughMeansTrue)
	} else {
		g.emitNodeInConditionContext(n.Expr1, trueTarget, afterExpr1, FallThroughMeansFalse)
	}
	g.emitLabel(afterExpr1)

	g.emitNodeInConditionContext(n.Expr2, trueTarget, falseTarget, fallThroughMode)
}

func (g *Generator) genCoalesce(n *parser.CoalesceNode, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	temp := g.keep(g.tempDestination(dst))
	endLabel := g.newLabel()

	if n.HasAbsorbedOptionalChain {
		g.pushOptionalChainTarget()
	}
	g.emitNode(temp, n.Expr1)
	g.emitJumpIfFalse(g.emitIsUndefinedOrNull(g.newTemporary(), temp), endLabel)

	if n.HasAbsorbedOptionalChain {
		g.popOptionalChainTarget()
	}
	g.emitNodeInTailPosition(temp, n.Expr2)

	g.emitLabel(endLabel)
	return g.move(dst, temp)
}

func (g *Generator) genCoalesceInConditionContext(n *parser.CoalesceNode, trueTarget, falseTarget *Label, fallThroughMode FallThroughMode) {
	defer g.refScope()()
	if n.NeedsDebugHook() {
		g.emitDebugHook(n, nil)
	}

	nullishTarget := g.newLabel()

	if n.HasAbsorbedOptionalChain {
		g.pushOptionalChainTargetLabel(nullishTarget)
	}
	value := g.keep(g.emitNode(nil, n.Expr1))
	g.emitJumpIfTrue(g.emitIsUndefinedOrNull(g.newTemporary(), value), nullishTarget)
	if n.HasAbsorbedOptionalChain {
		g.discardOptionalChainTarget()
	}

	if fallThroughMode == FallThroughMeansTrue {
		g.emitJumpIfFalse(value, falseTarget)
		g.emitJump(trueTarget)
	} else {
		g.emitJumpIfTrue(value, trueTarget)
		g.emitJump(falseTarget)
	}

	g.emitLabel(nullishTarget)
	g.emitNodeInConditionContext(n.Expr2, trueTarget, falseTarget, fallThroughMode)
}

// --- Optional chains -------------------------------------------------------------

func (g *Generator) genOptionalChain(n *parser.OptionalChainNode, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	finalDest := g.keep(g.finalDestination(dst))

	if n.IsOutermost {
		g.pushOptionalChainTarget()
	}
	g.emitNodeInTailPosition(finalDest, n.Expr)
	if n.IsOutermost {
		g.popOptionalChainTargetWithResult(finalDest, parser.IsDeleteNode(n.Expr))
	}
	return finalDest
}

// emitOptionalChainCallee is OptionalChainNode::emitCallee: it evaluates
// the callee and this value of a call like a?.b(), loading undefined into
// both if the chain short-circuits.
func (g *Generator) emitOptionalChainCallee(n *parser.OptionalChainNode, function, thisRegister *RegisterID) {
	defer g.refScope()()
	g.pushOptionalChainTarget()
	switch e := n.Expr.(type) {
	case *parser.BracketAccessorNode:
		g.emitNode(thisRegister, e.Base)
		if isOptionalChainBase(e.Base) {
			g.emitOptionalCheck(thisRegister)
		}

		if isNonIndexStringElement(e.Subscript) {
			g.emitExpressionInfo(e.Divot, e.DivotStart, e.DivotEnd)
			g.emitGetByID(function, thisRegister, e.Subscript.(*parser.StringNode).Value)
		} else {
			property := g.keep(g.emitNodeForProperty(nil, e.Subscript))
			g.emitExpressionInfo(e.Divot, e.DivotStart, e.DivotEnd)
			g.emitGetByVal(function, thisRegister, property)
		}
	case *parser.DotAccessorNode:
		g.emitNode(thisRegister, e.Base)
		if isOptionalChainBase(e.Base) {
			g.emitOptionalCheck(thisRegister)
		}

		g.emitExpressionInfo(e.Divot, e.DivotStart, e.DivotEnd)
		var thisValue *RegisterID
		g.emitGetPropertyValue(dotOf(e), function, thisRegister, &thisValue)
	}

	endLabel := g.newLabel()
	g.emitJump(endLabel)
	g.popOptionalChainTarget()
	g.emitLoad(function, bytecode.Undefined{})
	g.emitLoad(thisRegister, bytecode.Undefined{})
	g.emitLabel(endLabel)
}

func (g *Generator) genOptionalChainInConditionContext(n *parser.OptionalChainNode, trueTarget, falseTarget *Label, fallThroughMode FallThroughMode) {
	if n.NeedsDebugHook() {
		g.emitDebugHook(n, nil)
	}

	if parser.IsDeleteNode(n.Expr) {
		g.genExprInConditionContext(n, trueTarget, falseTarget, fallThroughMode)
		return
	}

	// Short-circuiting produces undefined, which is falsy: send the bail-out
	// straight to falseTarget.
	if n.IsOutermost {
		g.pushOptionalChainTargetLabel(falseTarget)
	}
	g.emitNodeInConditionContext(n.Expr, trueTarget, falseTarget, fallThroughMode)
	if n.IsOutermost {
		g.discardOptionalChainTarget()
	}
}

// --- Conditional ----------------------------------------------------------------------

func (g *Generator) genConditional(n *parser.ConditionalNode, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	newDst := g.keep(g.finalDestination(dst))
	beforeElse := g.newLabel()
	afterElse := g.newLabel()

	beforeThen := g.newLabel()
	g.emitNodeInConditionContext(n.Logical, beforeThen, beforeElse, FallThroughMeansTrue)
	g.emitLabel(beforeThen)

	g.emitProfileControlFlow(n.Expr1.BaseNode().StartOffset())
	g.emitNodeInTailPosition(newDst, n.Expr1)
	g.emitJump(afterElse)

	g.emitLabel(beforeElse)
	g.emitProfileControlFlow(n.Expr1.BaseNode().EndOffset() + 1)
	g.emitNodeInTailPosition(newDst, n.Expr2)

	g.emitLabel(afterElse)

	g.emitProfileControlFlow(n.Expr2.BaseNode().EndOffset() + 1)

	return newDst
}

func (g *Generator) genConditionalInConditionContext(n *parser.ConditionalNode, trueTarget, falseTarget *Label, fallThroughMode FallThroughMode) {
	if n.NeedsDebugHook() {
		g.emitDebugHook(n, nil)
	}

	beforeThen := g.newLabel()
	beforeElse := g.newLabel()
	end := g.newLabel()

	g.emitNodeInConditionContext(n.Logical, beforeThen, beforeElse, FallThroughMeansTrue)
	g.emitLabel(beforeThen)

	g.emitNodeInConditionContext(n.Expr1, trueTarget, falseTarget, fallThroughMode)
	g.emitJump(end)

	g.emitLabel(beforeElse)
	g.emitNodeInConditionContext(n.Expr2, trueTarget, falseTarget, fallThroughMode)

	g.emitLabel(end)
}
