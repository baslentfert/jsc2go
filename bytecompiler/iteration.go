package bytecompiler

import (
	"github.com/baslentfert/jsc2go/bytecode"
	"github.com/baslentfert/jsc2go/ident"
	"github.com/baslentfert/jsc2go/parser"
)

// Iteration, generators, await, for-in contexts, arrow function contexts and
// misc helpers (BytecodeGenerator.cpp).

var (
	doneIdent   = ident.FromString("done")
	valueIdent  = ident.FromString("value")
	nextIdent   = ident.FromString("next")
	returnIdent = ident.FromString("return")
	throwIdent  = ident.FromString("throw")
)

// emitEnumeration emits a for-of style loop over subjectNode, calling
// callBack with the register holding each value.
func (g *Generator) emitEnumeration(node *parser.ThrowableExpressionData, subjectNode parser.ExpressionNode, callBack func(*Generator, *RegisterID),
	forLoopNode *parser.ForOfNode, forLoopSymbolTable *RegisterID) {
	defer g.refScope()()

	if forLoopNode != nil && forLoopNode.IsForAwait {
		subject := g.keep(g.newTemporary())
		g.emitNode(subject, subjectNode)

		iterator := g.keep(g.newTemporary())
		nextMethod := g.keep(g.newTemporary())

		g.emitGetGenericAsyncIterator(iterator, nextMethod, subject, node)

		loopDone := g.newLabel()

		finallyLabel := g.newLabel()
		finallyContext := g.newFinallyContext(finallyLabel)
		g.pushFinallyControlFlowScope(finallyContext)

		g.scoped(func() {
			scope := g.newLabelScope(LabelScopeLoop, ident.Null)
			value := g.keep(g.newTemporary())
			g.emitLoad(value, bytecode.Undefined{})

			g.emitJump(scope.continueTarget)

			loopStart := g.newLabel()
			g.emitLabel(loopStart)
			g.emitLoopHint()

			g.emitTryWithFinallyThatDoesNotShadowExceptionIn(finallyContext, func(g *Generator) {
				callBack(g, value)
				g.emitJump(scope.continueTarget)
			}, func(g *Generator) {
				g.emitIteratorGenericClose(iterator, node, EmitAwaitYes)
			})

			g.emitLabel(scope.continueTarget)
			g.prepareLexicalScopeForNextForLoopIteration(&forLoopNode.VariableEnvironmentNode, forLoopSymbolTable)
			g.emitDebugHook(forLoopNode.Lexpr, nil)

			g.scoped(func() {
				g.emitAsyncIteratorNext(value, nextMethod, iterator, nil, node)
				g.emitAwait(value, value, node.Divot)

				typeIsObject := g.newLabel()
				g.emitJumpIfTrue(g.emitIsObject(g.newTemporary(), value), typeIsObject)
				g.emitThrowTypeError("Iterator result interface is not an object.")
				g.emitLabel(typeIsObject)

				g.emitJumpIfTrue(g.emitGetByID(g.newTemporary(), value, doneIdent), loopDone)
				g.emitGetByID(value, value, valueIdent)
				g.emitJump(loopStart)
			})

			breakLabelIsBound := scope.breakTargetMayBeBound()
			if breakLabelIsBound {
				g.emitLabel(scope.breakTarget)
			}
			g.popFinallyControlFlowScope()
			if breakLabelIsBound {
				// IteratorClose sequence for break-ed control flow.
				g.emitIteratorGenericClose(iterator, node, EmitAwaitYes)
			}
		})
		g.emitLabel(loopDone)
		return
	}

	iterable := g.keep(g.newTemporary())
	g.emitNode(iterable, subjectNode)

	nextOrIndex := g.keep(g.newTemporary())
	iterator := g.keep(g.newTemporary())
	g.scoped(func() {
		g.emitExpressionInfo(node.Divot, node.DivotStart, node.DivotEnd)
		iteratorSymbol := g.keep(g.emitGetByID(g.newTemporary(), iterable, ident.IteratorSymbol))
		args := g.newCallArguments(nil, 0)
		g.move(args.thisRegister(), iterable)
		g.emitIteratorOpen(iterator, nextOrIndex, iteratorSymbol, args, node)
	})

	loopDone := g.newLabel()

	// The iterator registers must outlive the finally context.
	finallyLabel := g.newLabel()
	finallyContext := g.newFinallyContext(finallyLabel)
	g.pushFinallyControlFlowScope(finallyContext)

	g.scoped(func() {
		scope := g.newLabelScope(LabelScopeLoop, ident.Null)
		value := g.keep(g.newTemporary())
		g.emitLoad(value, bytecode.Undefined{})

		loopStart := g.newLabel()
		g.emitLabel(loopStart)
		g.emitLabel(scope.continueTarget)
		g.emitLoopHint()

		if forLoopNode != nil {
			g.prepareLexicalScopeForNextForLoopIteration(&forLoopNode.VariableEnvironmentNode, forLoopSymbolTable)
			g.emitDebugHook(forLoopNode.Lexpr, nil)
		}

		g.scoped(func() {
			done := g.keep(g.newTemporary())
			nextArgs := g.newCallArguments(nil, 0)
			g.move(nextArgs.thisRegister(), iterator)

			g.emitIteratorNext(done, value, iterable, nextOrIndex, nextArgs, node)
			g.emitJumpIfTrue(done, loopDone)
		})

		g.emitTryWithFinallyThatDoesNotShadowExceptionIn(finallyContext, func(g *Generator) {
			callBack(g, value)
			g.emitJump(loopStart)
		}, func(g *Generator) {
			g.emitIteratorClose(iterator, nextOrIndex, iterable, node)
		})

		breakLabelIsBound := scope.breakTargetMayBeBound()
		if breakLabelIsBound {
			g.emitLabel(scope.breakTarget)
		}
		g.popFinallyControlFlowScope()
		if breakLabelIsBound {
			// IteratorClose sequence for break-ed control flow.
			g.emitIteratorClose(iterator, nextOrIndex, iterable, node)
		}
	})
	g.emitLabel(loopDone)
}

func (g *Generator) emitGetTemplateObject(dst *RegisterID, taggedTemplate *parser.TaggedTemplateNode) *RegisterID {
	descriptor := &bytecode.TemplateObjectDescriptor{}
	for _, s := range taggedTemplate.TemplateLiteral.TemplateStrings {
		descriptor.RawStrings = append(descriptor.RawStrings, s.Raw.String())
		if s.Cooked.IsNull() {
			descriptor.CookedStrings = append(descriptor.CookedStrings, nil)
		} else {
			cooked := s.Cooked.String()
			descriptor.CookedStrings = append(descriptor.CookedStrings, &cooked)
		}
	}
	constant := g.addTemplateObjectConstant(descriptor, taggedTemplate.EndOffset())
	if dst == nil {
		return constant
	}
	return g.move(dst, constant)
}

func (g *Generator) emitGetGlobalPrivate(dst *RegisterID, property ident.Identifier) *RegisterID {
	defer g.refScope()()
	dst = g.tempDestination(dst)
	v := g.variable(property, ThisResolutionLocal)
	if local := v.Local(); local != nil {
		return g.move(dst, local)
	}

	scope := g.keep(g.newTemporary())
	g.move(scope, g.emitResolveScope(scope, v))
	return g.emitGetFromScope(dst, scope, v, bytecode.ThrowIfNotFound)
}

func (g *Generator) emitEnumeratorNext(propertyName, mode, index, base, enumerator *RegisterID) {
	bytecode.EmitEnumeratorNext(g, vr(propertyName), vr(mode), vr(index), vr(base), vr(enumerator))
}

func (g *Generator) emitEnumeratorHasOwnProperty(dst, base, mode, propertyName, index, enumerator *RegisterID) *RegisterID {
	bytecode.EmitEnumeratorHasOwnProperty(g, vr(dst), vr(base), vr(mode), vr(propertyName), vr(index), vr(enumerator))
	return dst
}

func (g *Generator) emitGetPropertyEnumerator(dst, base *RegisterID) *RegisterID {
	bytecode.EmitGetPropertyEnumerator(g, vr(dst), vr(base))
	return dst
}

func (g *Generator) emitIsObject(dst, src *RegisterID) *RegisterID {
	bytecode.EmitIsObject(g, vr(dst), vr(src))
	return dst
}

func (g *Generator) emitIsCallable(dst, src *RegisterID) *RegisterID {
	bytecode.EmitIsCallable(g, vr(dst), vr(src))
	return dst
}

func (g *Generator) emitIsConstructor(dst, src *RegisterID) *RegisterID {
	bytecode.EmitIsConstructor(g, vr(dst), vr(src))
	return dst
}

func (g *Generator) emitIsNumber(dst, src *RegisterID) *RegisterID {
	bytecode.EmitIsNumber(g, vr(dst), vr(src))
	return dst
}

func (g *Generator) emitIsUndefinedOrNull(dst, src *RegisterID) *RegisterID {
	bytecode.EmitIsUndefinedOrNull(g, vr(dst), vr(src))
	return dst
}

func (g *Generator) emitIsEmpty(dst, src *RegisterID) *RegisterID {
	bytecode.EmitIsEmpty(g, vr(dst), vr(src))
	return dst
}

// --- Arrow function context -------------------------------------------------

func (g *Generator) emitLoadArrowFunctionLexicalEnvironment(id ident.Identifier) *RegisterID {
	return g.emitResolveScope(nil, g.variable(id, ThisResolutionScoped))
}

func (g *Generator) emitLoadThisFromArrowFunctionLexicalEnvironment() {
	g.emitGetFromScope(&g.thisRegister, g.emitLoadArrowFunctionLexicalEnvironment(ident.ThisPrivateName),
		g.variable(ident.ThisPrivateName, ThisResolutionScoped), bytecode.DoNotThrowIfNotFound)
}

func (g *Generator) emitLoadNewTargetFromArrowFunctionLexicalEnvironment() *RegisterID {
	newTargetVar := g.variable(ident.NewTargetLocalPrivateName, ThisResolutionLocal)
	return g.emitGetFromScope(g.newTargetRegister, g.emitLoadArrowFunctionLexicalEnvironment(ident.NewTargetLocalPrivateName), newTargetVar, bytecode.ThrowIfNotFound)
}

func (g *Generator) emitLoadDerivedConstructorFromArrowFunctionLexicalEnvironment() *RegisterID {
	protoScopeVar := g.variable(ident.DerivedConstructorPrivateName, ThisResolutionLocal)
	return g.emitGetFromScope(g.newTemporary(), g.emitLoadArrowFunctionLexicalEnvironment(ident.DerivedConstructorPrivateName), protoScopeVar, bytecode.ThrowIfNotFound)
}

func (g *Generator) emitLoadDerivedConstructor() *RegisterID {
	if g.constructorKind() == parser.ConstructorKindExtends {
		return &g.calleeRegister
	}
	return g.emitLoadDerivedConstructorFromArrowFunctionLexicalEnvironment()
}

// ensureThis returns the this register, checking that super() has been
// called in derived constructors.
func (g *Generator) ensureThis() *RegisterID {
	if g.constructorKind() == parser.ConstructorKindExtends || g.isDerivedConstructorContext() {
		if (g.needsToUpdateArrowFunctionContext && g.isSuperCallUsedInInnerArrowFunction()) || g.codeBlock.ParseMode() == parser.AsyncArrowFunctionBodyMode {
			g.emitLoadThisFromArrowFunctionLexicalEnvironment()
		}
		g.emitTDZCheck(&g.thisRegister)
	}
	return &g.thisRegister
}

func (g *Generator) isThisUsedInInnerArrowFunction() bool {
	s := g.scope
	return s.DoAnyInnerArrowFunctionsUseThis() || s.DoAnyInnerArrowFunctionsUseSuperProperty() ||
		s.DoAnyInnerArrowFunctionsUseSuperCall() || s.DoAnyInnerArrowFunctionsUseEval() || g.usesEval()
}

func (g *Generator) isArgumentsUsedInInnerArrowFunction() bool {
	return g.scope.DoAnyInnerArrowFunctionsUseArguments() || g.scope.DoAnyInnerArrowFunctionsUseEval()
}

func (g *Generator) isNewTargetUsedInInnerArrowFunction() bool {
	s := g.scope
	return s.DoAnyInnerArrowFunctionsUseNewTarget() || s.DoAnyInnerArrowFunctionsUseSuperCall() ||
		s.DoAnyInnerArrowFunctionsUseEval() || g.usesEval()
}

func (g *Generator) isSuperUsedInInnerArrowFunction() bool {
	s := g.scope
	return s.DoAnyInnerArrowFunctionsUseSuperCall() || s.DoAnyInnerArrowFunctionsUseSuperProperty() ||
		s.DoAnyInnerArrowFunctionsUseEval() || g.usesEval()
}

func (g *Generator) isSuperCallUsedInInnerArrowFunction() bool {
	return g.scope.DoAnyInnerArrowFunctionsUseSuperCall() || g.scope.DoAnyInnerArrowFunctionsUseEval() || g.usesEval()
}

func (g *Generator) emitPutNewTargetToArrowFunctionContextScope() {
	if g.isNewTargetUsedInInnerArrowFunction() {
		newTargetVar := g.variable(ident.NewTargetLocalPrivateName, ThisResolutionLocal)
		g.emitPutToScope(g.arrowFunctionContextLexicalEnvironmentRegister, newTargetVar, g.newTargetRegister, bytecode.DoNotThrowIfNotFound, bytecode.InitializationModeInitialization)
	}
}

func (g *Generator) emitPutDerivedConstructorToArrowFunctionContextScope() {
	if g.needsDerivedConstructorInArrowFunctionLexicalEnvironment() {
		protoScope := g.variable(ident.DerivedConstructorPrivateName, ThisResolutionLocal)
		g.emitPutToScope(g.arrowFunctionContextLexicalEnvironmentRegister, protoScope, &g.calleeRegister, bytecode.DoNotThrowIfNotFound, bytecode.InitializationModeInitialization)
	}
}

func (g *Generator) emitPutThisToArrowFunctionContextScope() {
	if g.isThisUsedInInnerArrowFunction() || (g.scope.UsesSuperCall() && g.codeType == bytecode.EvalCode) {
		thisVar := g.variable(ident.ThisPrivateName, ThisResolutionScoped)
		scope := g.arrowFunctionContextLexicalEnvironmentRegister
		if g.isDerivedConstructorContext() {
			scope = g.emitLoadArrowFunctionLexicalEnvironment(ident.ThisPrivateName)
		}
		g.emitPutToScope(scope, thisVar, &g.thisRegister, bytecode.ThrowIfNotFound, bytecode.InitializationModeNotInitialization)
	}
}

// --- for-in ------------------------------------------------------------------------

// pushForInScope starts a for-in body in which the property variable
// (localRegister) can be accessed through the enumerator fast path. The
// registers are held until popForInScope.
func (g *Generator) pushForInScope(localRegister, propertyNameRegister, propertyOffsetRegister, enumeratorRegister, modeRegister *RegisterID, baseVariable *Variable) {
	if localRegister == nil {
		return
	}
	c := &forInContext{
		localRegister:           localRegister,
		propertyName:            propertyNameRegister,
		propertyOffset:          propertyOffsetRegister,
		enumerator:              enumeratorRegister,
		mode:                    modeRegister,
		baseVariable:            baseVariable,
		isValid:                 true,
		bodyBytecodeStartOffset: len(g.instructions),
	}
	for _, r := range []*RegisterID{localRegister, propertyNameRegister, propertyOffsetRegister, enumeratorRegister, modeRegister} {
		if r != nil {
			r.ref()
		}
	}
	g.forInContextStack = append(g.forInContextStack, c)
	g.disablePeepholeOptimization()
}

func (g *Generator) popForInScope(localRegister *RegisterID) {
	if localRegister == nil {
		return
	}
	c := g.forInContextStack[len(g.forInContextStack)-1]
	c.finalize(g, len(g.instructions))
	g.forInContextStack = g.forInContextStack[:len(g.forInContextStack)-1]
	for _, r := range []*RegisterID{c.localRegister, c.propertyName, c.propertyOffset, c.enumerator, c.mode} {
		if r != nil {
			r.deref()
		}
	}
}

// overwrite emits replacement code over the instruction at offset, padding
// the rest of the old instruction with nops. The replacement must not be
// longer than the original.
func (g *Generator) overwrite(offset int, emit func()) {
	end := offset + bytecode.OpcodeID(g.instructions[offset]).Length()
	full := g.instructions
	g.instructions = full[:offset]
	g.disablePeepholeOptimization()
	emit()
	for len(g.instructions) < end {
		bytecode.EmitNop(g)
	}
	if len(g.instructions) != end {
		panic("bytecompiler: replacement instruction is longer than the original")
	}
	g.instructions = full
	g.disablePeepholeOptimization()
}

// finalize falls back to ordinary property accesses if the loop variable
// may be reassigned in the body. (Reassignment or capture of the variable
// is rare, so a simple def scan is good enough.)
func (c *forInContext) finalize(g *Generator, bodyBytecodeEndOffset int) {
	escaped := false
	local := c.localRegister.VirtualRegister()
	for offset := c.bodyBytecodeStartOffset; !escaped && offset < bodyBytecodeEndOffset; {
		ins := g.instructions[offset:]
		op := bytecode.OpcodeID(ins[0])
		for checkpoint := op.NumberOfCheckpoints() - 1; checkpoint >= 0; checkpoint-- {
			bytecode.ComputeDefsForInstruction(g.codeBlock, ins, checkpoint, func(operand bytecode.VirtualRegister) {
				if operand == local {
					escaped = true
				}
			})
		}
		offset += op.Length()
	}

	if !escaped {
		return
	}

	for _, inst := range c.getInsts {
		old := bytecode.DecodeEnumeratorGetByVal(g.instructions[inst.instIndex:])
		g.overwrite(inst.instIndex, func() {
			bytecode.EmitGetByVal(g, old.Dst, old.Base, bytecode.VirtualRegister(inst.propertyRegIndex), g.nextValueProfileIndex())
		})
	}

	for _, inst := range c.inInsts {
		old := bytecode.DecodeEnumeratorInByVal(g.instructions[inst.instIndex:])
		g.overwrite(inst.instIndex, func() {
			bytecode.EmitInByVal(g, old.Dst, old.Base, bytecode.VirtualRegister(inst.propertyRegIndex))
		})
	}

	for _, inst := range c.putInsts {
		old := bytecode.DecodeEnumeratorPutByVal(g.instructions[inst.instIndex:])
		g.overwrite(inst.instIndex, func() {
			bytecode.EmitPutByVal(g, old.Base, bytecode.VirtualRegister(inst.propertyRegIndex), old.Value, old.EcmaMode)
		})
	}

	for _, j := range c.hasOwnPropertyJumpInsts {
		branchInstIndex, newBranchTarget := j[0], j[1]
		if bytecode.OpcodeID(g.instructions[branchInstIndex]) != bytecode.OpJneqPtrID {
			panic("bytecompiler: for-in hasOwnProperty branch is not op_jneq_ptr")
		}
		g.overwrite(branchInstIndex, func() {
			bytecode.EmitJmp(g, bytecode.BoundLabel(newBranchTarget-branchInstIndex))
		})
	}
}

func (g *Generator) emitRestParameter(result *RegisterID, numParametersToSkip int) *RegisterID {
	bytecode.EmitCreateRest(g, vr(result), uint32(numParametersToSkip))
	return result
}

func (g *Generator) emitRequireObjectCoercible(value *RegisterID, message string) {
	target := g.newLabel()
	bytecode.EmitJnundefinedOrNull(g, vr(value), g.bind(target))
	g.emitThrowTypeError(message)
	g.emitLabel(target)
}

func (g *Generator) emitRequireObjectCoercibleForDestructuring(value *RegisterID, propertyName ident.Identifier) {
	target := g.newLabel()
	bytecode.EmitJnundefinedOrNull(g, vr(value), g.bind(target))
	if !propertyName.IsNull() {
		g.emitThrowTypeError("Cannot destructure property '" + propertyName.String() + "' from null or undefined value")
	} else {
		g.emitThrowTypeError("Cannot destructure null or undefined value")
	}
	g.emitLabel(target)
}

// --- Generators and await ------------------------------------------------------

// AsyncGeneratorSuspendReason is JSAsyncGenerator::AsyncGeneratorSuspendReason.
type AsyncGeneratorSuspendReason int32

const (
	SuspendReasonAwait AsyncGeneratorSuspendReason = iota
	SuspendReasonYield
	// SuspendReasonYieldNoAwait is used by yield*: the driver delivers the
	// value without awaiting it.
	SuspendReasonYieldNoAwait
)

// AsyncGeneratorReasonShift is JSAsyncGenerator::reasonShift.
const AsyncGeneratorReasonShift = 2

func (g *Generator) emitYieldPoint(argument *RegisterID, reason AsyncGeneratorSuspendReason) {
	mergePoint := g.newLabel()
	yieldPointIndex := g.yieldPoints
	g.yieldPoints++
	state := int32(yieldPointIndex) + 1
	if g.parseMode() == parser.AsyncGeneratorBodyMode {
		state = state<<AsyncGeneratorReasonShift | int32(reason)
	}

	g.emitGeneratorStateChange(state)

	// Split the try ranges here; they restart at the merge point.
	savePoint := g.newEmittedLabel()
	for i := len(g.tryContextStack) - 1; i >= 0; i-- {
		context := &g.tryContextStack[i]
		g.tryRanges = append(g.tryRanges, tryRange{start: context.start, end: savePoint, tryData: context.tryData})
		context.start = mergePoint
	}
	savedTryContextStack := g.tryContextStack
	g.tryContextStack = nil

	bytecode.EmitYield(g, uint32(yieldPointIndex), vr(argument))

	// Restore the try contexts, which now start at the merge point.
	g.tryContextStack = savedTryContextStack
	g.emitLabel(mergePoint)
}

func (g *Generator) isResumeMode(mode ResumeMode) *RegisterID {
	return g.emitEqualityOp(bytecode.OpStricteqID, g.newTemporary(), g.generatorResumeModeRegister(), g.emitLoadResumeMode(nil, mode))
}

func (g *Generator) emitYield(argument *RegisterID) *RegisterID {
	// Async generators' drivers await the operand (reason Yield); yield*
	// uses YieldNoAwait instead (see emitDelegateYield).
	g.emitYieldPoint(argument, SuspendReasonYield)

	normalLabel := g.newLabel()
	g.emitJumpIfTrue(g.isResumeMode(ResumeModeNormal), normalLabel)

	throwLabel := g.newLabel()
	g.emitJumpIfTrue(g.isResumeMode(ResumeModeThrow), throwLabel)

	// Return.
	g.scoped(func() {
		returnRegister := g.keep(g.generatorValueRegister())
		if !g.emitReturnViaFinallyIfNeeded(returnRegister) {
			g.emitReturn(returnRegister)
		}
	})

	// Throw.
	g.emitLabel(throwLabel)
	g.emitThrow(g.generatorValueRegister())

	// Normal.
	g.emitLabel(normalLabel)
	return g.generatorValueRegister()
}

func (g *Generator) emitAwait(dst, src *RegisterID, position parser.TextPosition) *RegisterID {
	g.emitDebugHookAt(bytecode.WillAwait, position, g.generatorRegister)

	g.emitYieldPoint(src, SuspendReasonAwait)

	normalLabel := g.newLabel()
	g.emitJumpIfTrue(g.isResumeMode(ResumeModeNormal), normalLabel)

	g.emitThrow(g.generatorValueRegister())

	g.emitLabel(normalLabel)
	result := g.move(dst, g.generatorValueRegister())

	g.emitDebugHookAt(bytecode.DidAwait, position, g.generatorRegister)
	return result
}

func (g *Generator) emitCallIterator(iterator, argument *RegisterID, node *parser.ThrowableExpressionData) *RegisterID {
	defer g.refScope()()
	args := g.newCallArguments(nil, 0)
	g.move(args.thisRegister(), argument)
	g.emitCall(iterator, iterator, NoExpectedFunction, args, node.Divot, node.DivotStart, node.DivotEnd, DebuggableCallNo)
	return iterator
}

func (g *Generator) emitIteratorOpen(iterator, nextOrIndex, symbolIterator *RegisterID, iterable *CallArguments, node *parser.ThrowableExpressionData) {
	defer g.refScope()()
	g.reserveCallFrame()

	if g.shouldEmitDebugHooks() {
		g.emitDebugHookAt(bytecode.WillExecuteExpression, node.DivotStart, nil)
	}

	g.emitExpressionInfo(node.Divot, node.DivotStart, node.DivotEnd)
	bytecode.EmitIteratorOpen(g, vr(iterator), vr(nextOrIndex), vr(symbolIterator), vr(iterable.thisRegister()), iterable.stackOffset(),
		g.nextValueProfileIndex(), g.nextValueProfileIndex(), g.nextValueProfileIndex())
}

func (g *Generator) emitIteratorNext(done, value, iterable, nextOrIndex *RegisterID, iterator *CallArguments, node *parser.ThrowableExpressionData) {
	defer g.refScope()()
	g.reserveCallFrame()

	if g.shouldEmitDebugHooks() {
		g.emitDebugHookAt(bytecode.WillExecuteExpression, node.DivotStart, nil)
	}

	g.emitExpressionInfo(node.Divot, node.DivotStart, node.DivotEnd)
	bytecode.EmitIteratorNext(g, vr(done), vr(value), vr(iterable), vr(nextOrIndex), vr(iterator.thisRegister()), iterator.stackOffset(),
		g.nextValueProfileIndex(), g.nextValueProfileIndex(), g.nextValueProfileIndex())
}

// emitGetGenericIterator calls argument[Symbol.iterator](); the result is
// held by the caller's refScope.
func (g *Generator) emitGetGenericIterator(argument *RegisterID, node *parser.ThrowableExpressionData) *RegisterID {
	g.emitExpressionInfo(node.Divot, node.DivotStart, node.DivotEnd)
	iterator := g.keep(g.emitGetByID(g.newTemporary(), argument, ident.IteratorSymbol))
	g.emitCallIterator(iterator, argument, node)
	return iterator
}

func (g *Generator) emitIteratorGenericNext(dst, nextMethod, iterator *RegisterID, node *parser.ThrowableExpressionData, doEmitAwait EmitAwait) *RegisterID {
	g.scoped(func() {
		nextArguments := g.newCallArguments(nil, 0)
		g.move(nextArguments.thisRegister(), iterator)
		g.emitCall(dst, nextMethod, NoExpectedFunction, nextArguments, node.Divot, node.DivotStart, node.DivotEnd, DebuggableCallNo)

		if doEmitAwait == EmitAwaitYes {
			g.emitAwait(dst, dst, node.Divot)
		}
	})
	typeIsObject := g.newLabel()
	g.emitJumpIfTrue(g.emitIsObject(g.newTemporary(), dst), typeIsObject)
	g.emitThrowTypeError("Iterator result interface is not an object.")
	g.emitLabel(typeIsObject)
	return dst
}

func (g *Generator) emitIteratorGenericNextWithValue(dst, nextMethod, iterator, value *RegisterID, node *parser.ThrowableExpressionData) *RegisterID {
	defer g.refScope()()
	nextArguments := g.newCallArguments(nil, 1)
	g.move(nextArguments.thisRegister(), iterator)
	g.move(nextArguments.argumentRegister(0), value)
	g.emitCall(dst, nextMethod, NoExpectedFunction, nextArguments, node.Divot, node.DivotStart, node.DivotEnd, DebuggableCallNo)
	return dst
}

func (g *Generator) emitIteratorGenericClose(iterator *RegisterID, node *parser.ThrowableExpressionData, doEmitAwait EmitAwait) {
	defer g.refScope()()
	done := g.newLabel()
	returnMethod := g.keep(g.emitGetByID(g.newTemporary(), iterator, returnIdent))
	g.emitJumpIfTrue(g.emitIsUndefinedOrNull(g.newTemporary(), returnMethod), done)

	value := g.keep(g.newTemporary())
	returnArguments := g.newCallArguments(nil, 0)
	g.move(returnArguments.thisRegister(), iterator)
	g.emitCall(value, returnMethod, NoExpectedFunction, returnArguments, node.Divot, node.DivotStart, node.DivotEnd, DebuggableCallNo)

	if doEmitAwait == EmitAwaitYes {
		g.emitAwait(value, value, node.Divot)
	}

	g.emitJumpIfTrue(g.emitIsObject(g.newTemporary(), value), done)
	g.emitThrowTypeError("Iterator result interface is not an object.")
	g.emitLabel(done)
}

func (g *Generator) emitIteratorClose(iterator, nextOrIndex, iterable *RegisterID, node *parser.ThrowableExpressionData) {
	done := g.newLabel()
	bytecode.EmitIteratorCloseCheck(g, vr(iterator), vr(nextOrIndex), vr(iterable), g.bind(done))
	g.emitIteratorGenericClose(iterator, node, EmitAwaitNo)
	g.emitLabel(done)
}

// emitDelegateYield emits yield*; the result is held by the caller's
// refScope.
func (g *Generator) emitDelegateYield(argument *RegisterID, node *parser.ThrowableExpressionData) *RegisterID {
	isAsync := g.parseMode() == parser.AsyncGeneratorBodyMode
	emitAwaitInClose := EmitAwaitNo
	if isAsync {
		emitAwaitInClose = EmitAwaitYes
	}

	value := g.keep(g.newTemporary())
	g.scoped(func() {
		var iterator, nextMethod *RegisterID
		if isAsync {
			iterator = g.keep(g.newTemporary())
			nextMethod = g.keep(g.newTemporary())
			g.emitGetGenericAsyncIterator(iterator, nextMethod, argument, node)
		} else {
			iterator = g.emitGetGenericIterator(argument, node)
			nextMethod = g.keep(g.emitGetByID(g.newTemporary(), iterator, nextIdent))
		}

		loopDone := g.newLabel()
		nextElement := g.newLabel()
		g.emitLoad(value, bytecode.Undefined{})

		g.emitJump(nextElement)

		loopStart := g.newLabel()
		g.emitLabel(loopStart)
		g.emitLoopHint()

		branchOnResult := g.newLabel()
		g.scoped(func() {
			// yield* delivers the inner iterator's value without another
			// Await (async results are awaited below).
			g.emitYieldPoint(value, SuspendReasonYieldNoAwait)
			g.move(value, g.generatorValueRegister())

			normalLabel := g.newLabel()
			g.emitJumpIfTrue(g.isResumeMode(ResumeModeNormal), normalLabel)

			returnLabel := g.newLabel()
			g.emitJumpIfTrue(g.isResumeMode(ResumeModeReturn), returnLabel)

			// Throw. throw() and return() are called generically.
			g.scoped(func() {
				throwMethodFound := g.newLabel()
				throwMethod := g.keep(g.emitGetByID(g.newTemporary(), iterator, throwIdent))
				g.emitJumpIfFalse(g.emitIsUndefinedOrNull(g.newTemporary(), throwMethod), throwMethodFound)

				g.emitIteratorGenericClose(iterator, node, emitAwaitInClose)

				g.emitThrowTypeError("The iterator, to which yield* delegated iteration, does not have a 'throw' method.")

				g.emitLabel(throwMethodFound)
				throwArguments := g.newCallArguments(nil, 1)
				g.move(throwArguments.thisRegister(), iterator)
				g.move(throwArguments.argumentRegister(0), value)
				g.emitCall(value, throwMethod, NoExpectedFunction, throwArguments, node.Divot, node.DivotStart, node.DivotEnd, DebuggableCallNo)

				g.emitJump(branchOnResult)
			})

			// Return.
			g.emitLabel(returnLabel)
			g.scoped(func() {
				returnMethodFound := g.newLabel()
				returnMethod := g.keep(g.emitGetByID(g.newTemporary(), iterator, returnIdent))
				g.emitJumpIfFalse(g.emitIsUndefinedOrNull(g.newTemporary(), returnMethod), returnMethodFound)

				if isAsync {
					g.emitAwait(value, value, node.Divot)
				}

				returnSequence := g.newLabel()
				g.emitJump(returnSequence)

				g.emitLabel(returnMethodFound)
				returnArguments := g.newCallArguments(nil, 1)
				g.move(returnArguments.thisRegister(), iterator)
				g.move(returnArguments.argumentRegister(0), value)
				g.emitCall(value, returnMethod, NoExpectedFunction, returnArguments, node.Divot, node.DivotStart, node.DivotEnd, DebuggableCallNo)

				if isAsync {
					g.emitAwait(value, value, node.Divot)
				}

				returnIteratorResultIsObject := g.newLabel()
				g.emitJumpIfTrue(g.emitIsObject(g.newTemporary(), value), returnIteratorResultIsObject)
				g.emitThrowTypeError("Iterator result interface is not an object.")

				g.emitLabel(returnIteratorResultIsObject)

				returnFromGenerator := g.newLabel()
				g.emitJumpIfTrue(g.emitGetByID(g.newTemporary(), value, doneIdent), returnFromGenerator)

				g.emitGetByID(value, value, valueIdent)
				g.emitJump(loopStart)

				g.emitLabel(returnFromGenerator)
				g.emitGetByID(value, value, valueIdent)

				g.emitLabel(returnSequence)
				if !g.emitReturnViaFinallyIfNeeded(value) {
					g.emitReturn(value)
				}
			})

			// Normal.
			g.emitLabel(normalLabel)
		})

		g.emitLabel(nextElement)
		if isAsync {
			g.emitAsyncIteratorNext(value, nextMethod, iterator, value, node)
		} else {
			g.emitIteratorGenericNextWithValue(value, nextMethod, iterator, value, node)
		}

		g.emitLabel(branchOnResult)

		if isAsync {
			g.emitAwait(value, value, node.Divot)
		}

		iteratorValueIsObject := g.newLabel()
		g.emitJumpIfTrue(g.emitIsObject(g.newTemporary(), value), iteratorValueIsObject)
		g.emitThrowTypeError("Iterator result interface is not an object.")
		g.emitLabel(iteratorValueIsObject)

		g.emitJumpIfTrue(g.emitGetByID(g.newTemporary(), value, doneIdent), loopDone)
		g.emitGetByID(value, value, valueIdent)

		g.emitJump(loopStart)
		g.emitLabel(loopDone)
	})

	g.emitGetByID(value, value, valueIdent)
	return value
}

func (g *Generator) emitGeneratorStateChange(state int32) {
	// FIXME: many yield points create many constants; op_inc the old state?
	completedState := g.emitLoad(nil, bytecode.JSInt32(state))
	field := uint32(generatorFieldState)
	if g.parseMode().IsModule() {
		field = moduleRecordFieldState
	}
	g.emitPutInternalField(g.generatorRegister, field, completedState)
}

func (g *Generator) emitToThis(srcDst *RegisterID) *RegisterID {
	bytecode.EmitToThis(g, vr(g.kill(srcDst)), g.ecmaMode, g.nextValueProfileIndex())
	return srcDst
}

// emitToThisSelf is emitToThis() on the this register.
func (g *Generator) emitToThisSelf() { g.emitToThis(&g.thisRegister) }
