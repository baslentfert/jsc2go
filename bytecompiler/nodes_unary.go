package bytecompiler

import (
	"github.com/baslentfert/jsc2go/bytecode"
	"github.com/baslentfert/jsc2go/ident"
	"github.com/baslentfert/jsc2go/parser"
)

// Code generation for ++/--, delete, void, typeof and the unary operators
// (NodesCodegen.cpp, PostfixNode through LogicalNotNode).

// incDec is what PrefixNode and PostfixNode (a PrefixNode subclass in JSC)
// share.
type incDec struct {
	expr     parser.ExpressionNode
	op       parser.Operator
	t        *parser.ThrowablePrefixedSubExpressionData
	position parser.TextPosition
}

func (g *Generator) emitIncOrDec(srcDst *RegisterID, op parser.Operator) *RegisterID {
	if op == parser.OpPlusPlus {
		return g.emitInc(srcDst)
	}
	return g.emitDec(srcDst)
}

// emitPostIncOrDec updates srcDst and returns the old value (as a number).
func (g *Generator) emitPostIncOrDec(dst, srcDst *RegisterID, op parser.Operator) *RegisterID {
	if dst == srcDst {
		return g.emitToNumeric(g.finalDestination(dst), srcDst)
	}
	defer g.refScope()()
	tmp := g.keep(g.emitToNumeric(g.newTemporary(), srcDst))
	result := g.keep(g.tempDestination(srcDst))
	g.move(result, tmp)
	g.emitIncOrDec(result, op)
	g.move(srcDst, result)
	return g.move(dst, tmp)
}

func (g *Generator) genPostfix(n *parser.PostfixNode, dst *RegisterID) *RegisterID {
	d := incDec{n.Expr, n.Operator, &n.ThrowablePrefixedSubExpressionData, n.Position()}
	switch n.Expr.(type) {
	case *parser.ResolveNode:
		if dst == g.IgnoredResult() {
			return g.emitPrefixResolve(d, dst)
		}
		return g.emitPostfixResolve(d, dst)
	case *parser.BracketAccessorNode:
		if dst == g.IgnoredResult() {
			return g.emitPrefixBracket(d, dst)
		}
		return g.emitPostfixBracket(d, dst)
	case *parser.DotAccessorNode:
		if dst == g.IgnoredResult() {
			return g.emitPrefixDot(d, dst)
		}
		return g.emitPostfixDot(d, dst)
	}

	g.emitNode(nil, n.Expr)
	message := "Postfix -- operator applied to value that is not a reference."
	if n.Operator == parser.OpPlusPlus {
		message = "Postfix ++ operator applied to value that is not a reference."
	}
	return g.emitThrowReferenceErrorAt(&n.ThrowableExpressionData, message, dst)
}

func (g *Generator) genPrefix(n *parser.PrefixNode, dst *RegisterID) *RegisterID {
	d := incDec{n.Expr, n.Operator, &n.ThrowablePrefixedSubExpressionData, n.Position()}
	switch n.Expr.(type) {
	case *parser.ResolveNode:
		return g.emitPrefixResolve(d, dst)
	case *parser.BracketAccessorNode:
		return g.emitPrefixBracket(d, dst)
	case *parser.DotAccessorNode:
		return g.emitPrefixDot(d, dst)
	}

	g.emitNode(nil, n.Expr)
	message := "Prefix -- operator applied to value that is not a reference."
	if n.Operator == parser.OpPlusPlus {
		message = "Prefix ++ operator applied to value that is not a reference."
	}
	return g.emitThrowReferenceErrorAt(&n.ThrowableExpressionData, message, dst)
}

func (g *Generator) emitPostfixResolve(d incDec, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	id := d.expr.(*parser.ResolveNode).Ident

	v := g.variable(id, ThisResolutionLocal)
	newDivot := d.t.DivotStart + identLength(id)
	if local := v.Local(); local != nil {
		g.emitExpressionInfo(newDivot, d.t.DivotStart, newDivot)
		g.emitTDZCheckIfNecessary(v, local, nil)
		localReg := g.keep(local)
		if v.IsReadOnly() {
			g.emitReadOnlyExceptionIfNeeded(v)
			localReg = g.keep(g.move(g.tempDestination(dst), local))
		}
		oldValue := g.keep(g.emitPostIncOrDec(g.finalDestination(dst), localReg, d.op))
		g.emitProfileTypeVariable(localReg, v, d.t.DivotStart, d.t.DivotEnd)
		return oldValue
	}

	g.emitExpressionInfo(newDivot, d.t.DivotStart, newDivot)
	scope := g.keep(g.emitResolveScope(nil, v))
	value := g.keep(g.emitGetFromScope(g.newTemporary(), scope, v, bytecode.ThrowIfNotFound))
	g.emitTDZCheckIfNecessary(v, value, nil)
	if v.IsReadOnly() {
		if g.emitReadOnlyExceptionIfNeeded(v) {
			return value
		}
	}
	oldValue := g.keep(g.emitPostIncOrDec(g.finalDestination(dst), value, d.op))
	if !v.IsReadOnly() {
		g.emitPutToScope(scope, v, value, g.resolveModeForPut(), bytecode.InitializationModeNotInitialization)
		g.emitProfileTypeVariable(value, v, d.t.DivotStart, d.t.DivotEnd)
	}
	return oldValue
}

// resolveModeForPut throws for unresolvable assignments in strict mode only.
func (g *Generator) resolveModeForPut() bytecode.ResolveMode {
	if g.ecmaMode.IsStrict() {
		return bytecode.ThrowIfNotFound
	}
	return bytecode.DoNotThrowIfNotFound
}

// emitIncDecBracketOperands evaluates base[subscript] for ++/--, converting
// a non-literal key once so it is never evaluated twice.
func (g *Generator) emitIncDecBracketOperands(bracketAccessor *parser.BracketAccessorNode) (base, property *RegisterID) {
	subscript := bracketAccessor.Subscript
	base = g.emitNodeForLeftHandSide(bracketAccessor.Base, bracketAccessor.SubscriptHasAssignments, g.isPure(subscript))
	property = g.keep(g.emitNodeForProperty(nil, subscript))
	if !parser.IsNumber(subscript) && !parser.IsString(subscript) {
		// Don't evaluate the key at all if the base isn't subscriptable.
		g.emitRequireObjectCoercible(base, "Cannot access property of undefined or null")
		property = g.keep(g.emitToPropertyKeyOrNumber(g.newTemporary(), property))
	}
	return base, property
}

func (g *Generator) emitPostfixBracket(d incDec, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	bracketAccessor := d.expr.(*parser.BracketAccessorNode)
	baseNode := bracketAccessor.Base

	base, property := g.emitIncDecBracketOperands(bracketAccessor)

	g.emitExpressionInfo(bracketAccessor.Divot, bracketAccessor.DivotStart, bracketAccessor.DivotEnd)
	var value, thisValue *RegisterID
	if isSuperNode(baseNode) {
		thisValue = g.keep(g.ensureThis())
		value = g.keep(g.emitGetByValWithThis(g.newTemporary(), base, thisValue, property))
	} else {
		value = g.keep(g.emitGetByVal(g.newTemporary(), base, property))
	}
	oldValue := g.emitPostIncOrDec(g.tempDestination(dst), value, d.op)
	g.emitExpressionInfo(d.t.Divot, d.t.DivotStart, d.t.DivotEnd)
	if isSuperNode(baseNode) {
		g.emitPutByValWithThis(base, thisValue, property, value)
	} else {
		g.emitPutByVal(base, property, value)
	}
	g.emitProfileTypeRange(value, d.t.DivotStart, d.t.DivotEnd)
	return g.move(dst, oldValue)
}

// emitPrivateAccessorCall calls the private getter or setter stored in
// the getter/setter object of a private accessor.
func (g *Generator) emitPrivateGetterCall(dst, scope *RegisterID, v Variable, base *RegisterID, position parser.TextPosition) *RegisterID {
	getterSetterObj := g.keep(g.emitGetFromScope(g.newTemporary(), scope, v, bytecode.ThrowIfNotFound))
	getterFunction := g.keep(g.emitDirectGetByID(g.newTemporary(), getterSetterObj, ident.GetPrivateName))
	args := g.newCallArguments(nil, 0)
	g.move(args.thisRegister(), base)
	return g.emitCall(dst, getterFunction, NoExpectedFunction, args, position, position, position, DebuggableCallYes)
}

func (g *Generator) emitPrivateSetterCall(scope *RegisterID, v Variable, base, value *RegisterID, position parser.TextPosition) {
	getterSetterObj := g.keep(g.emitGetFromScope(g.newTemporary(), scope, v, bytecode.ThrowIfNotFound))
	setterFunction := g.keep(g.emitDirectGetByID(g.newTemporary(), getterSetterObj, ident.SetPrivateName))
	args := g.newCallArguments(nil, 1)
	g.move(args.thisRegister(), base)
	g.move(args.argumentRegister(0), value)
	g.emitCallIgnoreResult(g.newTemporary(), setterFunction, NoExpectedFunction, args, position, position, position, DebuggableCallYes)
}

func (g *Generator) emitPostfixDot(d incDec, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	dotAccessor := d.expr.(*parser.DotAccessorNode)
	baseNode := dotAccessor.Base
	baseIsSuper := isSuperNode(baseNode)
	id := dotAccessor.Ident

	base := g.keep(g.emitNode(nil, baseNode))

	g.emitExpressionInfo(dotAccessor.Divot, dotAccessor.DivotStart, dotAccessor.DivotEnd)

	if dotAccessor.IsPrivateMember() {
		privateTraits := g.getPrivateTraits(id)

		if privateTraits.IsField() {
			v := g.variable(id, ThisResolutionLocal)
			scope := g.keep(g.emitResolveScope(nil, v)) // private names are always captured
			privateName := g.keep(g.newTemporary())
			g.emitGetFromScope(privateName, scope, v, bytecode.DoNotThrowIfNotFound)

			value := g.keep(g.emitGetPrivateName(g.newTemporary(), base, privateName))
			oldValue := g.keep(g.emitPostIncOrDec(g.tempDestination(dst), value, d.op))
			g.emitExpressionInfo(d.t.Divot, d.t.DivotStart, d.t.DivotEnd)
			g.emitPrivateFieldPut(base, privateName, value)
			g.emitProfileTypeRange(value, d.t.DivotStart, d.t.DivotEnd)
			return g.move(dst, oldValue)
		}

		v := g.variable(id, ThisResolutionLocal)
		if privateTraits.IsMethod() {
			g.privateBrandCheck(v, base, privateTraits.IsStatic())
			g.emitExpressionInfo(d.t.Divot, d.t.DivotStart, d.t.DivotEnd)
			g.emitThrowTypeError("Trying to access an undefined private setter")
			return g.tempDestination(dst)
		}

		scope := g.privateBrandCheck(v, base, privateTraits.IsStatic())

		if !privateTraits.IsGetter() {
			g.emitThrowTypeError("Trying to access an undefined private getter")
			return g.tempDestination(dst)
		}
		value := g.keep(g.emitPrivateGetterCall(g.newTemporary(), scope, v, base, d.position))

		oldValue := g.keep(g.emitPostIncOrDec(g.tempDestination(dst), value, d.op))
		g.emitExpressionInfo(d.t.Divot, d.t.DivotStart, d.t.DivotEnd)

		if privateTraits.IsSetter() {
			g.emitPrivateSetterCall(scope, v, base, value, d.position)
			g.emitProfileTypeRange(value, d.t.DivotStart, d.t.DivotEnd)
			return g.move(dst, oldValue)
		}

		g.emitThrowTypeError("Trying to access an undefined private getter")
		return g.move(dst, oldValue)
	}

	var value, thisValue *RegisterID
	if baseIsSuper {
		thisValue = g.keep(g.ensureThis())
		value = g.keep(g.emitGetByIDWithThis(g.newTemporary(), base, thisValue, id))
	} else {
		value = g.keep(g.emitGetByID(g.newTemporary(), base, id))
	}
	oldValue := g.emitPostIncOrDec(g.tempDestination(dst), value, d.op)
	g.emitExpressionInfo(d.t.Divot, d.t.DivotStart, d.t.DivotEnd)
	if baseIsSuper {
		g.emitPutByIDWithThis(base, thisValue, id, value)
	} else {
		g.emitPutByID(base, id, value)
	}
	g.emitProfileTypeRange(value, d.t.DivotStart, d.t.DivotEnd)
	return g.move(dst, oldValue)
}

func (g *Generator) emitPrefixResolve(d incDec, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	id := d.expr.(*parser.ResolveNode).Ident

	v := g.variable(id, ThisResolutionLocal)
	if local := v.Local(); local != nil {
		g.emitTDZCheckIfNecessary(v, local, nil)
		localReg := g.keep(local)
		if v.IsReadOnly() {
			g.emitReadOnlyExceptionIfNeeded(v)
			localReg = g.keep(g.move(g.tempDestination(dst), localReg))
		} else if g.shouldEmitTypeProfilerHooks() {
			tempDst := g.keep(g.tempDestination(dst))
			g.move(tempDst, localReg)
			g.emitIncOrDec(tempDst, d.op)
			g.move(localReg, tempDst)
			g.emitProfileTypeVariable(localReg, v, d.t.DivotStart, d.t.DivotEnd)
			return g.move(dst, tempDst)
		}
		g.emitIncOrDec(localReg, d.op)
		return g.move(dst, localReg)
	}

	g.emitExpressionInfo(d.t.Divot, d.t.DivotStart, d.t.DivotEnd)
	scope := g.keep(g.emitResolveScope(dst, v))
	value := g.keep(g.emitGetFromScope(g.newTemporary(), scope, v, bytecode.ThrowIfNotFound))
	g.emitTDZCheckIfNecessary(v, value, nil)
	if v.IsReadOnly() {
		if g.emitReadOnlyExceptionIfNeeded(v) {
			return value
		}
	}

	g.emitIncOrDec(value, d.op)
	if !v.IsReadOnly() {
		g.emitPutToScope(scope, v, value, g.resolveModeForPut(), bytecode.InitializationModeNotInitialization)
		g.emitProfileTypeVariable(value, v, d.t.DivotStart, d.t.DivotEnd)
	}
	return g.move(dst, value)
}

func (g *Generator) emitPrefixBracket(d incDec, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	bracketAccessor := d.expr.(*parser.BracketAccessorNode)
	baseNode := bracketAccessor.Base

	base, property := g.emitIncDecBracketOperands(bracketAccessor)
	propDst := g.keep(g.tempDestination(dst))

	g.emitExpressionInfo(bracketAccessor.Divot, bracketAccessor.DivotStart, bracketAccessor.DivotEnd)
	var value, thisValue *RegisterID
	if isSuperNode(baseNode) {
		thisValue = g.keep(g.ensureThis())
		value = g.emitGetByValWithThis(propDst, base, thisValue, property)
	} else {
		value = g.emitGetByVal(propDst, base, property)
	}
	g.emitIncOrDec(value, d.op)
	g.emitExpressionInfo(d.t.Divot, d.t.DivotStart, d.t.DivotEnd)
	if isSuperNode(baseNode) {
		g.emitPutByValWithThis(base, thisValue, property, value)
	} else {
		g.emitPutByVal(base, property, value)
	}
	g.emitProfileTypeRange(value, d.t.DivotStart, d.t.DivotEnd)
	return g.move(dst, propDst)
}

func (g *Generator) emitPrefixDot(d incDec, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	dotAccessor := d.expr.(*parser.DotAccessorNode)
	baseNode := dotAccessor.Base
	id := dotAccessor.Ident

	base := g.keep(g.emitNode(nil, baseNode))
	propDst := g.keep(g.tempDestination(dst))

	g.emitExpressionInfo(dotAccessor.Divot, dotAccessor.DivotStart, dotAccessor.DivotEnd)
	var value *RegisterID
	if dotAccessor.IsPrivateMember() {
		privateTraits := g.getPrivateTraits(id)
		if privateTraits.IsField() {
			v := g.variable(id, ThisResolutionLocal)
			scope := g.keep(g.emitResolveScope(nil, v))
			privateName := g.keep(g.newTemporary())
			g.emitGetFromScope(privateName, scope, v, bytecode.DoNotThrowIfNotFound)

			value = g.emitGetPrivateName(propDst, base, privateName)
			g.emitIncOrDec(value, d.op)
			g.emitExpressionInfo(d.t.Divot, d.t.DivotStart, d.t.DivotEnd)
			g.emitPrivateFieldPut(base, privateName, value)
			g.emitProfileTypeRange(value, d.t.DivotStart, d.t.DivotEnd)
			return g.move(dst, propDst)
		}

		v := g.variable(id, ThisResolutionLocal)
		if privateTraits.IsMethod() {
			g.privateBrandCheck(v, base, privateTraits.IsStatic())
			g.emitExpressionInfo(d.t.Divot, d.t.DivotStart, d.t.DivotEnd)
			g.emitThrowTypeError("Trying to access an undefined private setter")
			return g.move(dst, propDst)
		}

		scope := g.privateBrandCheck(v, base, privateTraits.IsStatic())

		if !privateTraits.IsGetter() {
			g.emitThrowTypeError("Trying to access an undefined private getter")
			return g.move(dst, propDst)
		}
		value = g.emitPrivateGetterCall(propDst, scope, v, base, d.position)

		g.emitIncOrDec(value, d.op)
		g.emitExpressionInfo(d.t.Divot, d.t.DivotStart, d.t.DivotEnd)

		if privateTraits.IsSetter() {
			g.emitPrivateSetterCall(scope, v, base, value, d.position)
			g.emitProfileTypeRange(value, d.t.DivotStart, d.t.DivotEnd)
			return g.move(dst, propDst)
		}

		g.emitThrowTypeError("Trying to access an undefined private getter")
		return g.move(dst, propDst)
	}

	var thisValue *RegisterID
	if isSuperNode(baseNode) {
		thisValue = g.keep(g.ensureThis())
		value = g.emitGetByIDWithThis(propDst, base, thisValue, id)
	} else {
		value = g.emitGetByID(propDst, base, id)
	}
	g.emitIncOrDec(value, d.op)
	g.emitExpressionInfo(d.t.Divot, d.t.DivotStart, d.t.DivotEnd)
	if isSuperNode(baseNode) {
		g.emitPutByIDWithThis(base, thisValue, id, value)
	} else {
		g.emitPutByID(base, id, value)
	}
	g.emitProfileTypeRange(value, d.t.DivotStart, d.t.DivotEnd)
	return g.move(dst, propDst)
}

// --- delete, void, typeof ---------------------------------------------------------

func (g *Generator) genDeleteResolve(n *parser.DeleteResolveNode, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	v := g.variable(n.Ident, ThisResolutionLocal)
	if v.Local() != nil {
		return g.emitLoadBool(g.finalDestination(dst), false)
	}

	g.emitExpressionInfo(n.Divot, n.DivotStart, n.DivotEnd)
	base := g.keep(g.emitResolveScope(dst, v))
	return g.emitDeleteByID(g.finalDestination(dst, base), base, n.Ident)
}

func (g *Generator) genDeleteBracket(n *parser.DeleteBracketNode, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	finalDest := g.keep(g.finalDestination(dst))
	r0 := g.keep(g.emitNode(nil, n.Base))

	if isOptionalChainBase(n.Base) {
		g.emitOptionalCheck(r0)
	}

	r1 := g.keep(g.emitNode(nil, n.Subscript))
	g.emitExpressionInfo(n.Divot, n.DivotStart, n.DivotEnd)
	if isSuperNode(n.Base) {
		return g.emitThrowReferenceErrorAt(&n.ThrowableExpressionData, "Cannot delete a super property", dst)
	}
	return g.emitDeleteByVal(finalDest, r0, r1)
}

func (g *Generator) genDeleteDot(n *parser.DeleteDotNode, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	finalDest := g.keep(g.finalDestination(dst))
	r0 := g.keep(g.emitNode(nil, n.Base))

	if isOptionalChainBase(n.Base) {
		g.emitOptionalCheck(r0)
	}

	g.emitExpressionInfo(n.Divot, n.DivotStart, n.DivotEnd)
	if isSuperNode(n.Base) {
		return g.emitThrowReferenceErrorAt(&n.ThrowableExpressionData, "Cannot delete a super property", dst)
	}
	return g.emitDeleteByID(finalDest, r0, n.Ident)
}

func (g *Generator) genDeleteValue(n *parser.DeleteValueNode, dst *RegisterID) *RegisterID {
	g.emitNodeInIgnoreResultPosition(n.Expr)
	// delete on a non-location expression ignores the value and returns true.
	return g.emitLoadBool(g.finalDestination(dst), true)
}

func (g *Generator) genVoid(n *parser.VoidNode, dst *RegisterID) *RegisterID {
	g.emitNodeInIgnoreResultPosition(n.Expr)
	if dst == g.IgnoredResult() {
		return nil
	}
	return g.emitLoad(dst, bytecode.Undefined{})
}

func (g *Generator) genTypeOfResolve(n *parser.TypeOfResolveNode, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	v := g.variable(n.Ident, ThisResolutionLocal)
	newDivot := n.DivotEnd - identLength(n.Ident)
	if local := v.Local(); local != nil {
		g.emitExpressionInfo(newDivot, newDivot, n.DivotEnd)
		g.emitTDZCheckIfNecessary(v, local, nil)
		if dst == g.IgnoredResult() {
			return nil
		}
		return g.emitTypeOf(g.finalDestination(dst), local)
	}

	scope := g.keep(g.emitResolveScope(dst, v))
	value := g.keep(g.emitGetFromScope(g.newTemporary(), scope, v, bytecode.DoNotThrowIfNotFound))
	g.emitExpressionInfo(newDivot, newDivot, n.DivotEnd)
	g.emitTDZCheckIfNecessary(v, value, nil)
	if dst == g.IgnoredResult() {
		return nil
	}
	return g.emitTypeOf(g.finalDestination(dst, scope), value)
}

func (g *Generator) genTypeOfValue(n *parser.TypeOfValueNode, dst *RegisterID) *RegisterID {
	if dst == g.IgnoredResult() {
		g.emitNodeInIgnoreResultPosition(n.Expr)
		return nil
	}
	defer g.refScope()()
	src := g.keep(g.emitNode(nil, n.Expr))
	return g.emitTypeOf(g.finalDestination(dst), src)
}

// --- Unary operators ------------------------------------------------------------

func unaryOpcode(op parser.UnaryOpKind) bytecode.OpcodeID {
	switch op {
	case parser.UnaryPlus:
		return bytecode.OpToNumberID
	case parser.Negate:
		return bytecode.OpNegateID
	case parser.BitwiseNot:
		return bytecode.OpBitnotID
	default:
		return bytecode.OpNotID
	}
}

func (g *Generator) genUnaryOp(n *parser.UnaryOpNode, dst *RegisterID) *RegisterID {
	opcode := unaryOpcode(n.Op)
	// op_not is not observable, so skip it if the result is unused, as in
	// the common `!(function (a) { ... })(a);`.
	if dst == g.IgnoredResult() && opcode == bytecode.OpNotID {
		g.emitNodeInIgnoreResultPosition(n.Expr)
		return nil
	}
	defer g.refScope()()
	src := g.keep(g.emitNode(nil, n.Expr))
	g.emitExpressionInfo(n.Position(), n.Position(), n.Position())
	if n.Op == parser.UnaryPlus {
		return g.emitToNumber(g.finalDestination(dst), src)
	}
	return g.emitUnaryOp(opcode, g.finalDestination(dst), src, parser.ResultDescriptor(n.Expr))
}

// genLogicalNotInConditionContext swaps the targets.
func (g *Generator) genLogicalNotInConditionContext(n *parser.UnaryOpNode, trueTarget, falseTarget *Label, fallThroughMode FallThroughMode) {
	if n.NeedsDebugHook() {
		g.emitDebugHook(n, nil)
	}
	g.emitNodeInConditionContext(n.Expr, falseTarget, trueTarget, invertFallThroughMode(fallThroughMode))
}
