package bytecompiler

import (
	"github.com/baslentfert/jsc2go/bytecode"
	"github.com/baslentfert/jsc2go/parser"
)

// Code generation for program, eval and function bodies, function
// expressions, yield, await and class fields (NodesCodegen.cpp, ScopeNode
// through DefineFieldNode).

// Generator states (JSGenerator::State).
const (
	generatorStateCompleted = -1
	generatorStateExecuting = -2
	generatorStateInit      = 0
)

// emitScopeNodeBytecode emits the root node being compiled.
func (g *Generator) emitScopeNodeBytecode() {
	switch n := g.scopeNode.(type) {
	case *parser.ProgramNode, *parser.ModuleProgramNode:
		g.emitProgramNodeBytecode(n.Scope())
	case *parser.EvalNode:
		g.emitEvalNodeBytecode(n)
	case *parser.FunctionNode:
		g.emitFunctionNodeBytecode(n)
	}
}

func (g *Generator) emitStatementsBytecode(s *parser.ScopeNode, dst *RegisterID) {
	if s.Statements == nil {
		return
	}
	g.emitSourceElements(s.Statements, dst)
}

func (g *Generator) emitProgramNodeBytecode(s *parser.ScopeNode) {
	defer g.refScope()()
	g.emitDebugHookAt(bytecode.WillExecuteProgram, parser.TextPosition(s.StartStartOffset), nil)

	dstRegister := g.keep(g.newTemporary())
	g.emitLoad(dstRegister, bytecode.Undefined{})
	g.emitProfileControlFlow(s.StartStartOffset)

	g.emitBodyWithUsingIfNeeded(s.UsingDeclarationCount(), s.HasAwaitUsingDeclaration(), func(g *Generator) {
		g.emitStatementsBytecode(s, dstRegister)
	})

	g.emitDebugHookAt(bytecode.DidExecuteProgram, parser.TextPosition(s.StartOffset()), nil)
	g.emitReturn(dstRegister)
}

func (g *Generator) emitEvalNodeBytecode(n *parser.EvalNode) {
	defer g.refScope()()
	g.emitDebugHookAt(bytecode.WillExecuteProgram, parser.TextPosition(n.StartStartOffset), nil)

	dstRegister := g.keep(g.newTemporary())
	g.emitLoad(dstRegister, bytecode.Undefined{})

	g.emitBodyWithUsingIfNeeded(n.UsingDeclarationCount(), n.HasAwaitUsingDeclaration(), func(g *Generator) {
		g.emitStatementsBytecode(&n.ScopeNode, dstRegister)
	})

	g.emitDebugHookAt(bytecode.DidExecuteProgram, n.Position(), nil)
	g.emitReturn(dstRegister)
}

// wrapperBodyFunction returns the function expression that the body of a
// generator or async wrapper consists of.
func wrapperBodyFunction(n *parser.FunctionNode) parser.BaseFuncExprNode {
	return n.SingleStatement().(*parser.ExprStatementNode).Expr.(parser.BaseFuncExprNode)
}

func (g *Generator) emitFunctionNodeBytecode(n *parser.FunctionNode) {
	defer g.refScope()()
	if g.shouldEmitTypeProfilerHooks() {
		// Non-simple parameter lists are handled by bindValue.
		if n.Parameters.IsSimpleParameterList {
			for i, p := range n.Parameters.Patterns {
				bindingNode := p.Pattern.(*parser.BindingNode)
				reg := newRegisterID(bytecode.VirtualRegister(bytecode.CallFrameSlotFirstArgument + i))
				g.emitProfileTypeFlagRange(reg, bytecode.ProfileTypeBytecodeFunctionArgument, bindingNode.DivotStart, bindingNode.DivotEnd)
			}
		}
	}

	g.emitProfileControlFlow(n.StartStartOffset)
	g.emitDebugHookAt(bytecode.DidEnterCallFrame, parser.TextPosition(n.StartStartOffset), nil)

	switch g.parseMode() {
	case parser.GeneratorWrapperFunctionMode, parser.GeneratorWrapperMethodMode,
		parser.AsyncGeneratorWrapperMethodMode, parser.AsyncGeneratorWrapperFunctionMode:
		funcExpr := wrapperBodyFunction(n)

		next := g.keep(g.newTemporary())
		g.emitNode(next, funcExpr)

		if g.superBinding() == parser.SuperBindingNeeded {
			homeObject := g.keep(g.emitHomeObjectForCallee())
			g.emitPutHomeObject(next, homeObject)
		}

		if g.parseMode().IsGeneratorWrapper() {
			g.emitPutGeneratorFields(next)
		} else {
			g.emitPutAsyncGeneratorFields(next)
		}

		g.emitDebugHookAt(bytecode.WillLeaveCallFrame, n.Position(), nil)
		g.emitReturn(g.generatorRegister)

	case parser.AsyncFunctionMode, parser.AsyncMethodMode, parser.AsyncArrowFunctionMode:
		g.emitAsyncFunctionWrapperBody(n)

	case parser.AsyncGeneratorBodyMode, parser.GeneratorBodyMode:
		generatorBodyLabel := g.newLabel()
		g.emitJumpIfTrue(g.isResumeMode(ResumeModeNormal), generatorBodyLabel)

		throwLabel := g.newLabel()
		g.emitJumpIfTrue(g.isResumeMode(ResumeModeThrow), throwLabel)

		g.emitReturn(g.generatorValueRegister())

		g.emitLabel(throwLabel)
		g.emitThrow(g.generatorValueRegister())

		g.emitLabel(generatorBodyLabel)
		fallthrough

	case parser.AsyncArrowFunctionBodyMode, parser.AsyncFunctionBodyMode:
		g.emitBodyWithUsingIfNeeded(n.UsingDeclarationCount(), n.HasAwaitUsingDeclaration(), func(g *Generator) {
			g.emitStatementsBytecode(&n.ScopeNode, g.IgnoredResult())
		})

		g.emitReturn(g.emitLoad(nil, bytecode.Undefined{}))

	default:
		g.emitBodyWithUsingIfNeeded(n.UsingDeclarationCount(), n.HasAwaitUsingDeclaration(), func(g *Generator) {
			g.emitStatementsBytecode(&n.ScopeNode, g.IgnoredResult())
		})

		// A function that ends in a return (a single block ending in one, or
		// an arrow function's expression body) needs no implicit return.
		// With using declarations the body ends in a `done` label that needs
		// a terminal instruction anyway.
		var returnNode *parser.ReturnNode
		if s := n.SingleStatement(); s != nil && n.UsingDeclarationCount() == 0 {
			switch s := s.(type) {
			case *parser.ReturnNode:
				returnNode = s
			case *parser.BlockNode:
				if s.Statements != nil {
					returnNode, _ = s.Statements.LastStatement().(*parser.ReturnNode)
				}
			}
		}

		if returnNode == nil {
			var r0 *RegisterID
			if g.isConstructor() && g.constructorKind() != parser.ConstructorKindNaked {
				r0 = g.ensureThis()
			} else {
				r0 = g.emitLoad(nil, bytecode.Undefined{})
			}
			// No expression info: this is not in the source.
			g.emitProfileTypeFlag(r0, bytecode.ProfileTypeBytecodeFunctionReturnStatement)
			g.emitWillLeaveCallFrameDebugHook()
			g.emitReturn(r0)
		}
	}
}

// emitAsyncFunctionWrapperBody emits the body of an async function: it
// either runs the body inline (no await) or creates the body generator and
// drives it.
func (g *Generator) emitAsyncFunctionWrapperBody(n *parser.FunctionNode) {
	defer g.refScope()()
	divot := parser.TextPosition(n.StartOffset())

	callLinkTimeConstant := func(dst *RegisterID, c bytecode.LinkTimeConstant, args ...*RegisterID) {
		defer g.refScope()()
		function := g.keep(g.moveLinkTimeConstant(nil, c))
		callArgs := g.newCallArguments(nil, len(args))
		g.emitLoad(callArgs.thisRegister(), bytecode.Undefined{})
		for i, a := range args {
			g.move(callArgs.argumentRegister(i), a)
		}
		if dst == nil {
			g.emitCallIgnoreResult(g.newTemporary(), function, NoExpectedFunction, callArgs, divot, divot, divot, DebuggableCallNo)
		} else {
			g.emitCall(dst, function, NoExpectedFunction, callArgs, divot, divot, divot, DebuggableCallNo)
		}
	}

	if g.isAsyncFunctionWithoutAwait() {
		// Without await nothing ever resumes, so the body is inlined into
		// the wrapper: no body function, no specially captured arguments
		// and no generator. The semantics come from try/catch/finally:
		//
		//	try {
		//	    body; transfer the completion value (normal or return)
		//	} catch (error) {
		//	    transfer error with completion type throw
		//	} finally {
		//	    if (completion type is throw)
		//	        return @newRejectedPromise(completion value);
		//	    return @newResolvedPromise(completion value);
		//	}
		if g.parseMode() == parser.AsyncArrowFunctionMode && g.isThisUsedInInnerArrowFunction() {
			g.emitLoadThisFromArrowFunctionLexicalEnvironment()
		}

		// `async function empty() { }` is `return @newResolvedPromise(undefined)`.
		if n.IsEmptyBody() {
			result := g.keep(g.newTemporary())
			callLinkTimeConstant(result, bytecode.LinkTimeConstantNewResolvedPromise, g.emitLoad(nil, bytecode.Undefined{}))
			g.emitWillLeaveCallFrameDebugHook()
			g.emitReturn(result)
			return
		}

		// `return V` in the body goes to the finally block with completion
		// type Return and value V; falling through keeps type Normal.
		finallyLabel := g.newLabel()
		finallyContext := g.newFinallyContext(finallyLabel)
		g.pushFinallyControlFlowScope(finallyContext)
		g.emitLoad(finallyContext.completionValueRegister, bytecode.Undefined{})

		catchLabel := g.newLabel()
		tryStartLabel := g.newEmittedLabel()
		td := g.pushTry(tryStartLabel, catchLabel, bytecode.HandlerFinally)
		g.emitBodyWithUsingIfNeeded(n.UsingDeclarationCount(), n.HasAwaitUsingDeclaration(), func(g *Generator) {
			g.emitStatementsBytecode(&n.ScopeNode, g.IgnoredResult())
		})
		g.emitJump(finallyLabel)
		tryEndLabel := g.newEmittedLabel()
		g.popTry(td, tryEndLabel)

		// The runtime puts the thrown value in the completion value register
		// and Throw in the completion type register.
		g.emitLabel(catchLabel)
		g.emitOutOfLineCatchHandler(finallyContext.completionValueRegister, finallyContext.completionTypeRegister, td)

		g.popFinallyControlFlowScope()

		// Throw: rejected promise; anything else: resolved promise.
		g.emitLabel(finallyLabel)
		resolveCase := g.newLabel()
		throwType := g.keep(g.emitLoad(nil, bytecode.JSInt32(int32(CompletionThrow))))
		g.emitJumpIfFalse(g.emitEqualityOp(bytecode.OpStricteqID, g.newTemporary(), finallyContext.completionTypeRegister, throwType), resolveCase)

		g.scoped(func() {
			result := g.keep(g.newTemporary())
			callLinkTimeConstant(result, bytecode.LinkTimeConstantNewRejectedPromise, finallyContext.completionValueRegister)
			g.emitWillLeaveCallFrameDebugHook()
			g.emitReturn(result)
		})

		g.emitLabel(resolveCase)
		g.scoped(func() {
			result := g.keep(g.newTemporary())
			callLinkTimeConstant(result, bytecode.LinkTimeConstantNewResolvedPromise, finallyContext.completionValueRegister)
			g.emitWillLeaveCallFrameDebugHook()
			g.emitReturn(result)
		})
		return
	}

	// The full async function: a body function driven by a generator.
	funcExpr := wrapperBodyFunction(n)

	next := g.keep(g.newTemporary())
	g.emitNode(next, funcExpr)

	if g.superBinding() == parser.SuperBindingNeeded || (g.parseMode() == parser.AsyncArrowFunctionMode && g.isSuperUsedInInnerArrowFunction()) {
		homeObject := g.keep(g.emitHomeObjectForCallee())
		g.emitPutHomeObject(next, homeObject)
	}

	if g.parseMode() == parser.AsyncArrowFunctionMode && g.isThisUsedInInnerArrowFunction() {
		g.emitLoadThisFromArrowFunctionLexicalEnvironment()
	}

	// Arrow functions in derived constructors don't store 'this': it may not
	// be initialized yet if super is called later.
	var generatorThis *RegisterID
	if !(g.isDerivedConstructorContext() && g.parseMode() == parser.AsyncArrowFunctionMode) {
		generatorThis = g.keep(&g.thisRegister)
		g.emitPutInternalField(g.generatorRegister, generatorFieldThis, generatorThis)
	} else {
		generatorThis = g.keep(g.emitLoad(nil, bytecode.Undefined{}))
	}

	g.emitPutInternalField(g.generatorRegister, generatorFieldNext, next)
	g.emitPutInternalField(g.generatorRegister, generatorFieldState, g.emitLoad(nil, bytecode.JSInt32(generatorStateExecuting)))

	tryStartLabel := g.newEmittedLabel()
	catchLabel := g.newLabel()
	successLabel := g.newLabel()
	driveLabel := g.newLabel()

	td := g.pushTry(tryStartLabel, catchLabel, bytecode.HandlerSynthesizedCatch)

	nextResult := g.keep(g.newTemporary())
	g.scoped(func() {
		nextArgs := g.newCallArguments(nil, 5)
		g.move(nextArgs.thisRegister(), generatorThis)
		g.move(nextArgs.argumentRegister(0), g.generatorRegister)
		g.emitLoad(nextArgs.argumentRegister(1), bytecode.JSInt32(generatorStateInit))
		g.emitLoad(nextArgs.argumentRegister(2), bytecode.Undefined{})
		g.emitLoadResumeMode(nextArgs.argumentRegister(3), ResumeModeNormal)
		g.emitLoad(nextArgs.argumentRegister(4), bytecode.Undefined{})

		g.emitCall(nextResult, next, NoExpectedFunction, nextArgs, divot, divot, divot, DebuggableCallNo)
	})

	g.scoped(func() {
		currentState := g.keep(g.emitGetInternalField(g.newTemporary(), g.generatorRegister, generatorFieldState))
		executingState := g.keep(g.emitLoad(nil, bytecode.JSInt32(generatorStateExecuting)))
		g.emitJumpIfFalse(g.emitEqualityOp(bytecode.OpStricteqID, g.newTemporary(), currentState, executingState), driveLabel)
	})

	callLinkTimeConstant(nil, bytecode.LinkTimeConstantResolvePromiseWithFirstResolvingFunctionCallCheck, g.promiseRegister, nextResult)
	g.emitJump(successLabel)

	g.emitLabel(driveLabel)
	callLinkTimeConstant(nil, bytecode.LinkTimeConstantAsyncFunctionDrive, nextResult, g.generatorRegister)
	g.emitJump(successLabel)

	tryEndLabel := g.newEmittedLabel()
	g.popTry(td, tryEndLabel)

	g.scoped(func() {
		g.emitLabel(catchLabel)
		thrownValue := g.keep(g.newTemporary())
		g.emitOutOfLineCatchHandler(thrownValue, nil, td)
		callLinkTimeConstant(nil, bytecode.LinkTimeConstantRejectPromiseWithFirstResolvingFunctionCallCheck, g.promiseRegister, thrownValue)
	})

	g.emitLabel(successLabel)
	g.emitDebugHookAt(bytecode.WillLeaveCallFrame, n.Position(), nil)
	g.emitReturn(g.promiseRegister)
}

func (g *Generator) genFuncDecl(n *parser.FuncDeclNode) {
	g.hoistSloppyModeFunctionIfNecessary(n.Metadata)
}

func (g *Generator) genFuncExpr(n parser.BaseFuncExprNode, dst *RegisterID) *RegisterID {
	return g.emitNewFunctionExpression(g.finalDestination(dst), n)
}

func (g *Generator) genYield(n *parser.YieldExprNode, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	var value *RegisterID
	if !n.Delegate {
		var arg *RegisterID
		if n.Argument != nil {
			arg = g.keep(g.emitNode(nil, n.Argument))
		} else {
			arg = g.keep(g.emitLoad(nil, bytecode.Undefined{}))
		}
		value = g.keep(g.emitYield(arg))
	} else {
		arg := g.keep(g.emitNode(nil, n.Argument))
		value = g.keep(g.emitDelegateYield(arg, &n.ThrowableExpressionData))
	}
	if dst == g.IgnoredResult() {
		return nil
	}
	return g.move(g.finalDestination(dst), value)
}

func (g *Generator) genAwait(n *parser.AwaitExprNode, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	arg := g.keep(g.emitNode(nil, n.Argument))
	if dst == nil {
		dst = g.newTemporary()
	}
	return g.emitAwait(dst, arg, n.Position())
}

// genDefineField defines one class field on this (the body of the
// synthetic field initializer function).
func (g *Generator) genDefineField(n *parser.DefineFieldNode) {
	defer g.refScope()()
	value := g.keep(g.newTemporary())
	shouldSetFunctionName := false

	if n.Assign == nil {
		g.emitLoad(value, bytecode.Undefined{})
	} else {
		g.emitNode(value, n.Assign)
		shouldSetFunctionName = g.shouldSetFunctionName(n.Assign)
		if shouldSetFunctionName && n.Type != parser.DefineFieldComputedName {
			g.emitSetFunctionNameFromIdentifier(value, n.Ident)
		}
	}

	switch n.Type {
	case parser.DefineFieldName:
		restore := g.strictModeScope()
		if index, ok := n.Ident.ParseIndex(); ok {
			propertyName := g.keep(g.emitLoad(nil, bytecode.JSNumber(float64(index))))
			g.emitDirectPutByVal(&g.thisRegister, propertyName, value)
		} else {
			g.emitDirectPutByID(&g.thisRegister, n.Ident, value)
		}
		restore()

	case parser.DefineFieldPrivateName:
		v := g.variable(n.Ident, ThisResolutionLocal)
		g.emitExpressionInfo(n.Position(), n.Position(), n.Position()+identLength(n.Ident))
		scope := g.keep(g.emitResolveScope(nil, v))
		privateName := g.keep(g.newTemporary())
		g.emitGetFromScope(privateName, scope, v, bytecode.DoNotThrowIfNotFound)
		g.emitDefinePrivateField(&g.thisRegister, privateName, value)

	case parser.DefineFieldComputedName:
		// The key was evaluated with the class; Ident names the private
		// variable in the class scope that holds it.
		v := g.variable(n.Ident, ThisResolutionLocal)
		g.emitExpressionInfo(n.Position(), n.Position(), n.Position()+1)
		scope := g.keep(g.emitResolveScope(nil, v))
		privateName := g.keep(g.newTemporary())
		g.emitGetFromScope(privateName, scope, v, bytecode.ThrowIfNotFound)
		if shouldSetFunctionName {
			g.emitSetFunctionName(value, privateName)
		}
		g.emitProfileTypeVariable(privateName, v, n.Position(), n.Position()+identLength(n.Ident))
		restore := g.strictModeScope()
		g.emitDirectPutByVal(&g.thisRegister, privateName, value)
		restore()
	}
}

func (g *Generator) genClassDecl(n *parser.ClassDeclNode) {
	g.emitNode(nil, n.ClassDeclaration)
}
