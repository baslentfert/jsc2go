package bytecompiler

import (
	"github.com/baslentfert/jsc2go/bytecode"
	"github.com/baslentfert/jsc2go/ident"
	"github.com/baslentfert/jsc2go/parser"
)

// Code generation for new and call expressions (NodesCodegen.cpp,
// NewExprNode through ReflectConstructFunctionCallDotNode).

func (g *Generator) genNewExpr(n *parser.NewExprNode, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	expectedFunction := NoExpectedFunction
	if r, ok := n.Expr.(*parser.ResolveNode); ok {
		expectedFunction = g.expectedFunctionForIdentifier(r.Ident)
	}

	var fn *RegisterID
	if n.Args != nil && n.Args.HasAssignments {
		fn = g.keep(g.newTemporary())
	}
	fn = g.keep(g.emitNode(fn, n.Expr))
	returnValue := g.keep(g.finalDestination(dst, fn))
	callArguments := g.newCallArguments(n.Args, 0)
	return g.emitConstruct(returnValue, fn, fn, expectedFunction, callArguments, n.Divot, n.DivotStart, n.DivotEnd)
}

func (g *Generator) genEvalFunctionCall(n *parser.EvalFunctionCallNode, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	// Load 'this' before calling eval in derived constructors: an arrow
	// function may have created it by calling super():
	//
	//	class B extends A {
	//	    constructor() {
	//	        var arrow = () => super();
	//	        arrow();
	//	        eval("this.id = 'B'");
	//	    }
	//	}
	if g.constructorKind() == parser.ConstructorKindExtends && g.needsToUpdateArrowFunctionContext && g.isThisUsedInInnerArrowFunction() {
		g.emitLoadThisFromArrowFunctionLexicalEnvironment()
	}

	v := g.variable(ident.Eval, ThisResolutionLocal)
	local := g.keep(v.Local())
	var fn *RegisterID
	if local != nil {
		g.emitTDZCheckIfNecessary(v, local, nil)
		fn = g.keep(g.move(g.tempDestination(dst), local))
	} else {
		fn = g.keep(g.newTemporary())
	}
	callArguments := g.newCallArguments(n.Args, 0)

	if local != nil {
		g.emitLoad(callArguments.thisRegister(), bytecode.Undefined{})
	} else {
		newDivot := n.DivotStart + 4
		g.emitExpressionInfo(newDivot, n.DivotStart, newDivot)
		g.move(callArguments.thisRegister(), g.emitResolveScope(callArguments.thisRegister(), v))
		g.emitGetFromScope(fn, callArguments.thisRegister(), v, bytecode.ThrowIfNotFound)
		g.emitTDZCheckIfNecessary(v, fn, nil)
	}

	returnValue := g.keep(g.finalDestination(dst, fn))

	if spread, ok := singleSpreadArgument(n.Args); ok {
		notEvalFunction := g.newLabel()
		done := g.newLabel()
		g.emitJumpIfNotEvalFunction(fn, notEvalFunction)

		g.scoped(func() {
			spreadRegister := g.keep(g.emitNode(nil, spread.Expression))
			g.emitExpressionInfo(spread.Divot, spread.DivotStart, spread.DivotEnd)

			directEvalArguments := g.newCallArguments(nil, 1)
			g.move(directEvalArguments.thisRegister(), callArguments.thisRegister())
			g.emitGetByVal(directEvalArguments.argumentRegister(0), spreadRegister, g.emitLoad(nil, bytecode.JSInt32(0)))
			g.emitCallDirectEval(returnValue, fn, directEvalArguments, n.Divot, n.DivotStart, n.DivotEnd, DebuggableCallNo)
			g.emitJump(done)
		})

		g.emitLabel(notEvalFunction)
		g.emitCallInTailPosition(returnValue, fn, NoExpectedFunction, callArguments, n.Divot, n.DivotStart, n.DivotEnd, DebuggableCallYes)
		g.emitLabel(done)
	} else if g.allowsTailCallOptimization() {
		// The callee is only a direct eval if it is the built-in eval;
		// otherwise this is an ordinary call, which may be a tail call.
		notEvalFunction := g.newLabel()
		done := g.newLabel()
		g.emitJumpIfNotEvalFunction(fn, notEvalFunction)

		g.emitCallDirectEval(returnValue, fn, callArguments, n.Divot, n.DivotStart, n.DivotEnd, DebuggableCallNo)
		g.emitJump(done)

		g.emitLabel(notEvalFunction)
		g.emitCallInTailPosition(returnValue, fn, NoExpectedFunction, callArguments, n.Divot, n.DivotStart, n.DivotEnd, DebuggableCallYes)
		g.emitLabel(done)
	} else {
		g.emitCallDirectEval(returnValue, fn, callArguments, n.Divot, n.DivotStart, n.DivotEnd, DebuggableCallNo)
	}

	return returnValue
}

func (g *Generator) genFunctionCallValue(n *parser.FunctionCallValueNode, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	if isSuperNode(n.Expr) {
		fn := g.keep(g.emitGetSuperFunctionForConstruct())
		returnValue := g.keep(g.finalDestination(dst, fn))

		isDefaultDerivedConstructorCall := g.isBuiltinDefaultClassConstructor && g.constructorKind() == parser.ConstructorKindExtends
		callArguments := g.newCallArguments(n.Args, 0)

		ret := g.emitSuperConstruct(returnValue, fn, g.newTargetRegister, NoExpectedFunction, callArguments, n.Divot, n.DivotStart, n.DivotEnd, isDefaultDerivedConstructorCall)

		isConstructorKindDerived := g.constructorKind() == parser.ConstructorKindExtends
		doWeUseArrowFunctionInConstructor := isConstructorKindDerived && g.needsToUpdateArrowFunctionContext

		if g.isDerivedConstructorContext() || (doWeUseArrowFunctionInConstructor && g.isSuperCallUsedInInnerArrowFunction()) {
			g.emitLoadThisFromArrowFunctionLexicalEnvironment()
		}

		thisIsEmptyLabel := g.newLabel()
		g.emitJumpIfTrue(g.emitIsEmpty(g.newTemporary(), &g.thisRegister), thisIsEmptyLabel)
		g.emitThrowReferenceError("'super()' can't be called more than once in a constructor.")
		g.emitLabel(thisIsEmptyLabel)

		g.move(&g.thisRegister, ret)

		if g.isDerivedConstructorContext() || doWeUseArrowFunctionInConstructor {
			g.emitPutThisToArrowFunctionContextScope()
		}

		// Initialize instance fields after the super call.
		if g.privateBrandRequirement() == parser.PrivateBrandRequirementNeeded {
			g.emitInstallPrivateBrand(&g.thisRegister)
		}

		if g.needsClassFieldInitializer() {
			fn = g.keep(g.emitLoadDerivedConstructor())
			g.emitInstanceFieldInitializationIfNeeded(&g.thisRegister, fn, n.Divot, n.DivotStart, n.DivotEnd)
		}
		return ret
	}

	if optionalChain, ok := n.Expr.(*parser.OptionalChainNode); ok && parser.IsLocation(optionalChain.Expr) {
		fn := g.keep(g.tempDestination(dst))
		returnValue := g.keep(g.finalDestination(dst, fn))
		callArguments := g.newCallArguments(n.Args, 0)
		g.emitOptionalChainCallee(optionalChain, fn, callArguments.thisRegister())
		ret := g.emitCallInTailPosition(returnValue, fn, NoExpectedFunction, callArguments, n.Divot, n.DivotStart, n.DivotEnd, DebuggableCallYes)
		g.emitProfileTypeRange(returnValue, n.DivotStart, n.DivotEnd)
		return ret
	}

	var fn *RegisterID
	if n.Args != nil && n.Args.HasAssignments {
		fn = g.keep(g.newTemporary())
	}
	fn = g.keep(g.emitNode(fn, n.Expr))
	returnValue := g.keep(g.finalDestination(dst, fn))
	if n.IsOptionalCall {
		g.emitOptionalCheck(fn)
	}

	callArguments := g.newCallArguments(n.Args, 0)
	g.emitLoad(callArguments.thisRegister(), bytecode.Undefined{})
	ret := g.emitCallInTailPosition(returnValue, fn, NoExpectedFunction, callArguments, n.Divot, n.DivotStart, n.DivotEnd, DebuggableCallYes)
	g.emitProfileTypeRange(returnValue, n.DivotStart, n.DivotEnd)
	return ret
}

func (g *Generator) genStaticBlockFunctionCall(n *parser.StaticBlockFunctionCallNode, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	homeObject := g.keep(g.emitHomeObjectForCallee())
	function := g.keep(g.emitNode(nil, n.Expr))
	g.emitPutHomeObject(function, homeObject)
	returnValue := g.keep(g.finalDestination(dst, function))

	callArguments := g.newCallArguments(nil, 0)
	g.move(callArguments.thisRegister(), &g.thisRegister)
	result := g.keep(g.emitCallInTailPosition(returnValue, function, NoExpectedFunction, callArguments, n.Divot, n.DivotStart, n.DivotEnd, DebuggableCallYes))

	g.emitProfileTypeRange(returnValue, n.DivotStart, n.DivotEnd)
	return result
}

func (g *Generator) genFunctionCallResolve(n *parser.FunctionCallResolveNode, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	expectedFunction := g.expectedFunctionForIdentifier(n.Ident)

	v := g.variable(n.Ident, ThisResolutionLocal)
	local := g.keep(v.Local())
	var fn *RegisterID
	newDivot := n.DivotStart + identLength(n.Ident)
	if local != nil {
		g.emitExpressionInfo(newDivot, n.DivotStart, newDivot)
		g.emitTDZCheckIfNecessary(v, local, nil)
		if n.Args.HasAssignments {
			fn = g.keep(g.move(g.tempDestination(dst), local))
		} else {
			fn = local
		}
	} else {
		fn = g.keep(g.tempDestination(dst))
	}
	callArguments := g.newCallArguments(n.Args, 0)

	if local != nil {
		g.emitLoad(callArguments.thisRegister(), bytecode.Undefined{})
		// A function in a local variable is not one of the built-in
		// constructors.
		expectedFunction = NoExpectedFunction
	} else {
		g.emitExpressionInfo(newDivot, n.DivotStart, newDivot)
		g.move(callArguments.thisRegister(), g.emitResolveScope(callArguments.thisRegister(), v))
		g.emitGetFromScope(fn, callArguments.thisRegister(), v, bytecode.ThrowIfNotFound)
		g.emitTDZCheckIfNecessary(v, fn, nil)
	}

	returnValue := g.keep(g.finalDestination(dst, fn))
	if n.IsOptionalCall {
		g.emitOptionalCheck(fn)
	}

	ret := g.emitCallInTailPosition(returnValue, fn, expectedFunction, callArguments, n.Divot, n.DivotStart, n.DivotEnd, DebuggableCallYes)
	g.emitProfileTypeRange(returnValue, n.DivotStart, n.DivotEnd)
	return ret
}

func (g *Generator) genFunctionCallBracket(n *parser.FunctionCallBracketNode, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	function := g.keep(g.tempDestination(dst))
	returnValue := g.keep(g.finalDestination(dst, function))
	baseIsSuper := isSuperNode(n.Base)
	subscriptIsNonIndexString := isNonIndexStringElement(n.Subscript)

	var base *RegisterID
	if baseIsSuper {
		base = g.keep(g.emitSuperBaseForCallee())
	} else {
		if subscriptIsNonIndexString {
			base = g.keep(g.emitNode(nil, n.Base))
		} else {
			base = g.emitNodeForLeftHandSide(n.Base, n.SubscriptHasAssignments, g.isPure(n.Subscript))
		}
		if isOptionalChainBase(n.Base) {
			g.emitOptionalCheck(base)
		}
	}

	var thisRegister *RegisterID
	if baseIsSuper {
		// Once this is not in its TDZ it cannot go back, so one check is enough.
		thisRegister = g.keep(g.ensureThis())
	}
	if subscriptIsNonIndexString {
		g.emitExpressionInfo(n.SubexpressionDivot(), n.SubexpressionStart(), n.SubexpressionEnd())
		id := n.Subscript.(*parser.StringNode).Value
		if baseIsSuper {
			g.emitGetByIDWithThis(function, base, thisRegister, id)
		} else {
			g.emitGetByID(function, base, id)
		}
	} else {
		property := g.keep(g.emitNodeForProperty(nil, n.Subscript))
		g.emitExpressionInfo(n.SubexpressionDivot(), n.SubexpressionStart(), n.SubexpressionEnd())
		if baseIsSuper {
			g.emitGetByValWithThis(function, base, thisRegister, property)
		} else {
			g.emitGetByVal(function, base, property)
		}
	}
	if n.IsOptionalCall {
		g.emitOptionalCheck(function)
	}

	callArguments := g.newCallArguments(n.Args, 0)
	if baseIsSuper {
		g.emitTDZCheck(&g.thisRegister)
		g.move(callArguments.thisRegister(), thisRegister)
	} else {
		g.move(callArguments.thisRegister(), base)
	}
	ret := g.emitCallInTailPosition(returnValue, function, NoExpectedFunction, callArguments, n.Divot, n.DivotStart, n.DivotEnd, DebuggableCallYes)
	g.emitProfileTypeRange(returnValue, n.DivotStart, n.DivotEnd)
	return ret
}

func callDotOf(n *parser.FunctionCallDotNode) baseDot {
	return baseDot{n.Base, n.Ident, n.Type, n.Position()}
}

func (g *Generator) genFunctionCallDot(n *parser.FunctionCallDotNode, dst *RegisterID) *RegisterID {
	switch n.Kind {
	case parser.CallDotCall:
		return g.genCallFunctionCallDot(n, dst)
	case parser.CallDotApply:
		return g.genApplyFunctionCallDot(n, dst)
	case parser.CallDotHasOwnProperty:
		return g.genHasOwnPropertyFunctionCallDot(n, dst)
	case parser.CallDotReflectConstruct:
		return g.genReflectConstructFunctionCallDot(n, dst)
	}
	defer g.refScope()()

	function := g.keep(g.tempDestination(dst))
	returnValue := g.keep(g.finalDestination(dst, function))
	callArguments := g.newCallArguments(n.Args, 0)
	baseIsSuper := isSuperNode(n.Base)
	shouldGetArgumentsDotLengthFast := g.shouldGetArgumentsDotLengthFast(n)
	switch {
	case baseIsSuper:
		g.move(callArguments.thisRegister(), g.ensureThis())
	case shouldGetArgumentsDotLengthFast:
		g.emitLoad(callArguments.thisRegister(), bytecode.Undefined{})
	default:
		g.emitNode(callArguments.thisRegister(), n.Base)
		if isOptionalChainBase(n.Base) {
			g.emitOptionalCheck(callArguments.thisRegister())
		}
	}
	g.emitExpressionInfo(n.SubexpressionDivot(), n.SubexpressionStart(), n.SubexpressionEnd())

	if shouldGetArgumentsDotLengthFast {
		g.emitArgumentCount(function)
	} else {
		base := callArguments.thisRegister()
		if baseIsSuper {
			base = g.keep(g.emitSuperBaseForCallee())
		}
		var thisValue *RegisterID
		g.emitGetPropertyValue(callDotOf(n), function, base, &thisValue)
	}

	if n.IsOptionalCall {
		g.emitOptionalCheck(function)
	}

	ret := g.emitCallInTailPosition(returnValue, function, NoExpectedFunction, callArguments, n.Divot, n.DivotStart, n.DivotEnd, DebuggableCallYes)
	g.emitProfileTypeRange(returnValue, n.DivotStart, n.DivotEnd)
	return ret
}

const maxDistanceToInnermostCallOrApply = 2

// genCallFunctionCallDot is CallFunctionCallDotNode: f.call(thisArg, ...args)
// becomes a direct call when f.call is Function.prototype.call.
func (g *Generator) genCallFunctionCallDot(n *parser.FunctionCallDotNode, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	returnValue := g.keep(g.finalDestination(dst))
	base := g.keep(g.emitNode(nil, n.Base))

	if isOptionalChainBase(n.Base) {
		g.emitOptionalCheck(base)
	}

	g.emitExpressionInfo(n.SubexpressionDivot(), n.SubexpressionStart(), n.SubexpressionEnd())

	var function *RegisterID
	makeFunction := func() {
		if isSuperNode(n.Base) {
			thisValue := g.keep(g.ensureThis())
			function = g.keep(g.emitGetByIDWithThis(g.tempDestination(dst), base, thisValue, ident.Call))
		} else {
			function = g.keep(g.emitGetByID(g.tempDestination(dst), base, ident.Call))
		}
		if n.IsOptionalCall {
			g.emitOptionalCheck(function)
		}
	}

	emitCallCheck := !g.isBuiltinFunction
	if n.DistanceToInnermostCallOrApply > maxDistanceToInnermostCallOrApply && emitCallCheck {
		makeFunction()
		callArguments := g.newCallArguments(n.Args, 0)
		g.move(callArguments.thisRegister(), base)
		g.emitCallInTailPosition(returnValue, function, NoExpectedFunction, callArguments, n.Divot, n.DivotStart, n.DivotEnd, DebuggableCallYes)
		g.move(dst, returnValue)
		return returnValue
	}

	realCall := g.newLabel()
	end := g.newLabel()

	if emitCallCheck {
		makeFunction()
		g.emitJumpIfNotFunctionCall(function, realCall)
	}
	g.scoped(func() {
		if spread, ok := singleSpreadArgument(n.Args); ok {
			argumentsRegister := g.keep(g.emitNode(nil, spread.Expression))
			g.emitExpressionInfo(spread.Divot, spread.DivotStart, spread.DivotEnd)
			thisRegister := g.keep(g.emitGetByVal(g.newTemporary(), argumentsRegister, g.emitLoad(nil, bytecode.JSInt32(0))))
			g.emitCallVarargsInTailPosition(returnValue, base, thisRegister, argumentsRegister, g.newTemporary(), 1, n.Divot, n.DivotStart, n.DivotEnd, DebuggableCallYes)
		} else if len(n.Args.Args) > 0 {
			rest := &parser.ArgumentsNode{Args: n.Args.Args[1:], HasAssignments: n.Args.HasAssignments}
			realFunction := g.keep(g.move(g.tempDestination(dst), base))
			callArguments := g.newCallArguments(rest, 0)
			g.emitNode(callArguments.thisRegister(), n.Args.Args[0])
			g.emitCallInTailPosition(returnValue, realFunction, NoExpectedFunction, callArguments, n.Divot, n.DivotStart, n.DivotEnd, DebuggableCallYes)
		} else {
			realFunction := g.keep(g.move(g.tempDestination(dst), base))
			callArguments := g.newCallArguments(n.Args, 0)
			g.emitLoad(callArguments.thisRegister(), bytecode.Undefined{})
			g.emitCallInTailPosition(returnValue, realFunction, NoExpectedFunction, callArguments, n.Divot, n.DivotStart, n.DivotEnd, DebuggableCallYes)
		}
	})
	if emitCallCheck {
		g.emitJump(end)
		g.emitLabel(realCall)
		g.scoped(func() {
			callArguments := g.newCallArguments(n.Args, 0)
			g.move(callArguments.thisRegister(), base)
			g.emitCallInTailPosition(returnValue, function, NoExpectedFunction, callArguments, n.Divot, n.DivotStart, n.DivotEnd, DebuggableCallYes)
		})
		g.emitLabel(end)
	}
	g.emitProfileTypeRange(returnValue, n.DivotStart, n.DivotEnd)
	return returnValue
}

// genHasOwnPropertyFunctionCallDot is HasOwnPropertyFunctionCallDotNode:
// o.hasOwnProperty(key) inside `for (key in o)` uses the enumerator.
func (g *Generator) genHasOwnPropertyFunctionCallDot(n *parser.FunctionCallDotNode, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	returnValue := g.keep(g.finalDestination(dst))
	base := g.keep(g.emitNode(nil, n.Base))

	if isOptionalChainBase(n.Base) {
		g.emitOptionalCheck(base)
	}

	g.emitExpressionInfo(n.SubexpressionDivot(), n.SubexpressionStart(), n.SubexpressionEnd())

	function := g.keep(g.emitGetByID(g.newTemporary(), base, ident.HasOwnProperty))
	if n.IsOptionalCall {
		g.emitOptionalCheck(function)
	}

	argument := n.Args.Args[0]
	var context *forInContext
	argumentVariable := g.variable(argument.(*parser.ResolveNode).Ident, ThisResolutionLocal)
	if argumentVariable.IsLocal() {
		context = g.findForInContext(argumentVariable.Local())
	}

	canUseFastHasOwnProperty := func() bool {
		if context == nil || context.baseVariable == nil {
			return false
		}
		switch b := n.Base.(type) {
		case *parser.ResolveNode:
			return g.variable(b.Ident, ThisResolutionLocal) == *context.baseVariable
		case *parser.ThisNode:
			// After ensureThis (invoked when materializing base), |this|
			// is in the local this register.
			return g.variable(ident.ThisPrivateName, ThisResolutionLocal) == *context.baseVariable
		}
		return false
	}

	if canUseFastHasOwnProperty() {
		// The base register may hold a different object in each iteration;
		// that is fine, the structure check rejects replaced objects.
		realCall := g.newLabel()
		end := g.newLabel()

		branchInsnOffset := g.emitWideJumpIfNotFunctionHasOwnProperty(function, realCall)
		g.emitEnumeratorHasOwnProperty(returnValue, base, context.Mode(), g.emitNode(nil, argument), context.PropertyOffset(), context.Enumerator())
		g.emitJump(end)

		g.emitLabel(realCall)
		g.scoped(func() {
			callArguments := g.newCallArguments(n.Args, 0)
			g.move(callArguments.thisRegister(), base)
			g.emitCallInTailPosition(returnValue, function, NoExpectedFunction, callArguments, n.Divot, n.DivotStart, n.DivotEnd, DebuggableCallYes)
		})

		g.emitLabel(end)

		g.recordHasOwnPropertyInForInLoop(context, branchInsnOffset, realCall)
	} else {
		callArguments := g.newCallArguments(n.Args, 0)
		g.move(callArguments.thisRegister(), base)
		g.emitCallInTailPosition(returnValue, function, NoExpectedFunction, callArguments, n.Divot, n.DivotStart, n.DivotEnd, DebuggableCallYes)
	}

	g.emitProfileTypeRange(returnValue, n.DivotStart, n.DivotEnd)
	return returnValue
}

func areTrivialApplyArguments(args *parser.ArgumentsNode) bool {
	if len(args.Args) <= 1 {
		return true
	}
	if len(args.Args) == 2 {
		a, ok := args.Args[1].(*parser.ArrayNode)
		return ok && isSimpleArray(a)
	}
	return false
}

// genApplyFunctionCallDot is ApplyFunctionCallDotNode. Simple cases become
// plain calls: f.apply() and f.apply(x) are f.call(x), and
// f.apply(x, [a, b]) is f.call(x, a, b) without allocating the array.
func (g *Generator) genApplyFunctionCallDot(n *parser.FunctionCallDotNode, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	mayBeCall := areTrivialApplyArguments(n.Args)

	returnValue := g.keep(g.finalDestination(dst))
	base := g.keep(g.emitNode(nil, n.Base))

	if isOptionalChainBase(n.Base) {
		g.emitOptionalCheck(base)
	}

	var function *RegisterID
	makeFunction := func() {
		if isSuperNode(n.Base) {
			thisValue := g.keep(g.ensureThis())
			function = g.keep(g.emitGetByIDWithThis(g.tempDestination(dst), base, thisValue, ident.Apply))
		} else {
			function = g.keep(g.emitGetByID(g.tempDestination(dst), base, ident.Apply))
		}
		if n.IsOptionalCall {
			g.emitOptionalCheck(function)
		}
	}

	emitCallCheck := !g.isBuiltinFunction
	if n.DistanceToInnermostCallOrApply > maxDistanceToInnermostCallOrApply && emitCallCheck {
		makeFunction()
		callArguments := g.newCallArguments(n.Args, 0)
		g.move(callArguments.thisRegister(), base)
		g.emitCallInTailPosition(returnValue, function, NoExpectedFunction, callArguments, n.Divot, n.DivotStart, n.DivotEnd, DebuggableCallYes)
		g.move(dst, returnValue)
		return returnValue
	}

	realCall := g.newLabel()
	end := g.newLabel()
	g.emitExpressionInfo(n.SubexpressionDivot(), n.SubexpressionStart(), n.SubexpressionEnd())
	if emitCallCheck {
		makeFunction()
		g.emitJumpIfNotFunctionApply(function, realCall)
	}
	g.scoped(func() {
		args := n.Args.Args
		switch {
		case mayBeCall && len(args) > 0:
			if spread, ok := args[0].(*parser.SpreadExpressionNode); ok {
				realFunction := g.keep(g.move(g.newTemporary(), base))
				index := g.keep(g.emitLoad(g.newTemporary(), bytecode.JSInt32(0)))
				thisRegister := g.keep(g.emitLoad(g.newTemporary(), bytecode.Undefined{}))
				argumentsRegister := g.keep(g.emitLoad(g.newTemporary(), bytecode.Undefined{}))

				extractor := func(g *Generator, value *RegisterID) {
					haveThis := g.newLabel()
					end := g.newLabel()
					g.emitJumpIfFalse(g.emitEqualityOp(bytecode.OpStricteqID, g.newTemporary(), index, g.emitLoad(nil, bytecode.JSInt32(0))), haveThis)
					g.move(thisRegister, value)
					g.emitLoad(index, bytecode.JSInt32(1))
					g.emitJump(end)
					g.emitLabel(haveThis)
					g.emitJumpIfFalse(g.emitEqualityOp(bytecode.OpStricteqID, g.newTemporary(), index, g.emitLoad(nil, bytecode.JSInt32(1))), end)
					g.move(argumentsRegister, value)
					g.emitLoad(index, bytecode.JSInt32(2))
					g.emitLabel(end)
				}
				g.emitEnumeration(&n.ThrowableExpressionData, spread.Expression, extractor, nil, nil)
				g.emitCallVarargsInTailPosition(returnValue, realFunction, thisRegister, argumentsRegister, g.newTemporary(), 0, n.Divot, n.DivotStart, n.DivotEnd, DebuggableCallYes)
			} else if len(args) > 1 {
				rest := arrayToArgumentList(args[1].(*parser.ArrayNode))
				realFunction := g.keep(g.move(g.tempDestination(dst), base))
				callArguments := g.newCallArguments(rest, 0)
				g.emitNode(callArguments.thisRegister(), args[0])
				g.emitCallInTailPosition(returnValue, realFunction, NoExpectedFunction, callArguments, n.Divot, n.DivotStart, n.DivotEnd, DebuggableCallYes)
			} else {
				rest := &parser.ArgumentsNode{}
				realFunction := g.keep(g.move(g.tempDestination(dst), base))
				callArguments := g.newCallArguments(rest, 0)
				g.emitNode(callArguments.thisRegister(), args[0])
				g.emitCallInTailPosition(returnValue, realFunction, NoExpectedFunction, callArguments, n.Divot, n.DivotStart, n.DivotEnd, DebuggableCallYes)
			}
		case mayBeCall:
			realFunction := g.keep(g.move(g.tempDestination(dst), base))
			callArguments := g.newCallArguments(n.Args, 0)
			g.emitLoad(callArguments.thisRegister(), bytecode.Undefined{})
			g.emitCallInTailPosition(returnValue, realFunction, NoExpectedFunction, callArguments, n.Divot, n.DivotStart, n.DivotEnd, DebuggableCallYes)
		default:
			realFunction := g.keep(g.move(g.tempDestination(dst), base))
			thisRegister := g.keep(g.emitNode(nil, args[0]))
			var argsRegister *RegisterID
			if r, ok := args[1].(*parser.ResolveNode); ok && r.Ident == ident.Arguments && g.argumentsForwarded {
				// The frame's own arguments, without an arguments object.
				argsRegister = g.keep(g.emitLoad(nil, bytecode.ForwardedArguments{}))
			} else {
				argsRegister = g.keep(g.emitNode(nil, args[1]))
			}

			// Function.prototype.apply ignores extra arguments, but they
			// are still evaluated for their side effects.
			for _, extra := range args[2:] {
				g.emitNode(nil, extra)
			}

			g.emitCallVarargsInTailPosition(returnValue, realFunction, thisRegister, argsRegister, g.newTemporary(), 0, n.Divot, n.DivotStart, n.DivotEnd, DebuggableCallYes)
		}
	})
	if emitCallCheck {
		g.emitJump(end)
		g.emitLabel(realCall)
		g.scoped(func() {
			callArguments := g.newCallArguments(n.Args, 0)
			g.move(callArguments.thisRegister(), base)
			g.emitCallInTailPosition(returnValue, function, NoExpectedFunction, callArguments, n.Divot, n.DivotStart, n.DivotEnd, DebuggableCallYes)
		})
		g.emitLabel(end)
	}
	g.emitProfileTypeRange(returnValue, n.DivotStart, n.DivotEnd)
	return returnValue
}

// genReflectConstructFunctionCallDot is ReflectConstructFunctionCallDotNode:
// Reflect.construct(target, args, newTarget) becomes a construct when
// Reflect.construct is the original.
func (g *Generator) genReflectConstructFunctionCallDot(n *parser.FunctionCallDotNode, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	args := n.Args.Args
	argumentsListNode := args[1]
	newTargetNodes := args[2:]

	function := g.keep(g.tempDestination(dst))
	returnValue := g.keep(g.finalDestination(dst, function))

	callArguments := g.newCallArguments(nil, len(args))

	g.emitNode(callArguments.thisRegister(), n.Base)
	if isOptionalChainBase(n.Base) {
		g.emitOptionalCheck(callArguments.thisRegister())
	}
	g.emitExpressionInfo(n.SubexpressionDivot(), n.SubexpressionStart(), n.SubexpressionEnd())
	g.emitGetByID(function, callArguments.thisRegister(), ident.Construct)
	if n.IsOptionalCall {
		g.emitOptionalCheck(function)
	}

	target := callArguments.argumentRegister(0)
	argumentsList := callArguments.argumentRegister(1)
	newTarget := target
	if len(newTargetNodes) > 0 {
		newTarget = callArguments.argumentRegister(2)
	}

	g.emitNode(target, args[0])
	var constructArguments *CallArguments
	if array, ok := argumentsListNode.(*parser.ArrayNode); ok && isSimpleArray(array) {
		constructArguments = g.newCallArguments(nil, len(array.Elements))
		for i, element := range array.Elements {
			g.emitNode(constructArguments.argumentRegister(i), element.Value)
		}
	} else {
		g.emitNode(argumentsList, argumentsListNode)
	}
	for i, argument := range newTargetNodes {
		g.emitNode(callArguments.argumentRegister(2+i), argument)
	}

	realCall := g.newLabel()
	end := g.newLabel()
	g.emitDebugHookAt(bytecode.WillExecuteExpression, n.DivotStart, nil)
	g.emitJumpIfNotReflectConstruct(function, realCall)
	g.emitJumpIfFalse(g.emitIsConstructor(g.newTemporary(), target), realCall)
	if len(newTargetNodes) > 0 {
		g.emitJumpIfFalse(g.emitIsConstructor(g.newTemporary(), newTarget), realCall)
	}
	if constructArguments != nil {
		g.emitConstruct(returnValue, target, newTarget, NoExpectedFunction, constructArguments, n.Divot, n.DivotStart, n.DivotEnd)
	} else {
		if !parser.IsArrayLiteral(argumentsListNode) {
			g.emitJumpIfFalse(g.emitIsObject(g.newTemporary(), argumentsList), realCall)
		}
		g.emitConstructVarargs(returnValue, target, newTarget, argumentsList, g.newTemporary(), 0, n.Divot, n.DivotStart, n.DivotEnd, DebuggableCallNo)
	}
	g.emitJump(end)

	g.emitLabel(realCall)
	if constructArguments != nil {
		g.emitNewArrayByReversingArguments(argumentsList, constructArguments)
	}
	ret := g.emitCallInTailPosition(returnValue, function, NoExpectedFunction, callArguments, n.Divot, n.DivotStart, n.DivotEnd, DebuggableCallYes)
	g.emitLabel(end)
	g.emitProfileTypeRange(returnValue, n.DivotStart, n.DivotEnd)
	return ret
}
