package bytecompiler

import (
	"github.com/baslentfert/jsc2go/bytecode"
	"github.com/baslentfert/jsc2go/ident"
	"github.com/baslentfert/jsc2go/parser"
)

// Control flow: exceptions, finally, labels, switch, scope restoration and
// `using` declarations (BytecodeGenerator.cpp).

const (
	currentLexicalScopeIndex   = -2
	outermostLexicalScopeIndex = -1
)

func (g *Generator) currentLexicalScopeIndex() int {
	if len(g.lexicalScopeStack) == 0 {
		return outermostLexicalScopeIndex
	}
	return len(g.lexicalScopeStack) - 1
}

func (g *Generator) allocateScope() {
	g.scopeRegister = g.addVar()
	g.scopeRegister.ref()
	g.codeBlock.ScopeRegister = g.scopeRegister.VirtualRegister()
}

// newLabelScope pushes a break/continue target; the caller's refScope keeps
// it alive (C++: Ref<LabelScope>).
func (g *Generator) newLabelScope(typ LabelScopeType, name ident.Identifier) *LabelScope {
	g.shrinkLabelScopes()
	var continueTarget *Label
	if typ == LabelScopeLoop { // only loops have continue targets
		continueTarget = g.newLabel()
	}
	s := &LabelScope{typ: typ, name: name, scopeDepth: g.labelScopeDepth(), breakTarget: g.newLabel(), continueTarget: continueTarget}
	g.labelScopes = append(g.labelScopes, s)
	return g.keepLabelScope(s)
}

func (g *Generator) shrinkLabelScopes() {
	for n := len(g.labelScopes); n > 0 && g.labelScopes[n-1].refCount == 0; n-- {
		g.labelScopes[n-1] = nil
		g.labelScopes = g.labelScopes[:n-1]
	}
}

// breakTarget returns the statement `break name` leaves (name may be null).
func (g *Generator) breakTarget(name ident.Identifier) *LabelScope {
	g.shrinkLabelScopes()
	if len(g.labelScopes) == 0 {
		return nil
	}

	// We special-case the following, which is a syntax error in Firefox:
	//   label:
	//       break;
	if name.Len() == 0 {
		for i := len(g.labelScopes) - 1; i >= 0; i-- {
			if s := g.labelScopes[i]; s.typ != LabelScopeNamedLabel {
				return s
			}
		}
		return nil
	}

	for i := len(g.labelScopes) - 1; i >= 0; i-- {
		if s := g.labelScopes[i]; !s.name.IsNull() && s.name == name {
			return s
		}
	}
	return nil
}

// continueTarget returns the loop `continue name` continues.
func (g *Generator) continueTarget(name ident.Identifier) *LabelScope {
	g.shrinkLabelScopes()
	if len(g.labelScopes) == 0 {
		return nil
	}

	if name.Len() == 0 {
		for i := len(g.labelScopes) - 1; i >= 0; i-- {
			if s := g.labelScopes[i]; s.typ == LabelScopeLoop {
				return s
			}
		}
		return nil
	}

	// Continue to the loop nested nearest to the label scope named name.
	var result *LabelScope
	for i := len(g.labelScopes) - 1; i >= 0; i-- {
		s := g.labelScopes[i]
		if s.typ == LabelScopeLoop {
			result = s
		}
		if !s.name.IsNull() && s.name == name {
			return result // may be nil
		}
	}
	return nil
}

func (g *Generator) pushTry(start, handlerLabel *Label, handlerType bytecode.HandlerType) *tryData {
	result := &tryData{target: handlerLabel, handlerType: handlerType}
	g.tryContextStack = append(g.tryContextStack, tryContext{start: start, tryData: result})
	return result
}

func (g *Generator) popTry(td *tryData, end *Label) {
	g.usesExceptions = true
	top := g.tryContextStack[len(g.tryContextStack)-1]
	if top.tryData != td {
		panic("bytecompiler: unbalanced try")
	}
	g.tryRanges = append(g.tryRanges, tryRange{start: top.start, end: end, tryData: top.tryData})
	g.tryContextStack = g.tryContextStack[:len(g.tryContextStack)-1]
}

func (g *Generator) emitOutOfLineCatchHandler(thrownValueRegister, completionTypeRegister *RegisterID, data *tryData) {
	unused := g.newTemporary()
	g.emitOutOfLineExceptionHandler(unused, thrownValueRegister, completionTypeRegister, data)
}

func (g *Generator) emitOutOfLineFinallyHandler(exceptionRegister, completionTypeRegister *RegisterID, data *tryData) {
	unused := g.newTemporary()
	g.emitOutOfLineExceptionHandler(exceptionRegister, unused, completionTypeRegister, data)
}

// emitOutOfLineExceptionHandler queues the op_catch that generate() emits
// after the body.
func (g *Generator) emitOutOfLineExceptionHandler(exceptionRegister, thrownValueRegister, completionTypeRegister *RegisterID, data *tryData) {
	g.exceptionHandlersToEmit = append(g.exceptionHandlersToEmit, catchEntry{
		tryData:                data,
		exceptionRegister:      exceptionRegister.VirtualRegister(),
		thrownValueRegister:    thrownValueRegister.VirtualRegister(),
		completionTypeRegister: vr(completionTypeRegister),
	})
}

// restoreScopeRegisterTo sets the scope register to the scope that was
// current at lexical scope index lexicalScopeIndex.
func (g *Generator) restoreScopeRegisterTo(lexicalScopeIndex int) {
	if lexicalScopeIndex == currentLexicalScopeIndex {
		return // No change needed.
	}

	if lexicalScopeIndex != outermostLexicalScopeIndex {
		for i := lexicalScopeIndex; i >= 0; i-- {
			if scope := g.lexicalScopeStack[i].scope; scope != nil {
				g.move(g.scopeRegister, scope)
				return
			}
		}
	}
	// Without a local scope in this function/program we grab the outermost
	// scope of this code.
	if g.topLevelScopeRegister != nil {
		g.move(g.scopeRegister, g.topLevelScopeRegister)
	} else {
		bytecode.EmitGetScope(g, vr(g.scopeRegister))
	}
}

func (g *Generator) restoreScopeRegister() { g.restoreScopeRegisterTo(g.currentLexicalScopeIndex()) }

func (g *Generator) labelScopeDepthToLexicalScopeIndex(targetLabelScopeDepth int) int {
	scopeDelta := g.labelScopeDepth() - targetLabelScopeDepth
	if scopeDelta == 0 {
		return currentLexicalScopeIndex
	}
	return g.controlFlowScopeStack[targetLabelScopeDepth].lexicalScopeIndex
}

func (g *Generator) emitThrow(exc *RegisterID) {
	g.usesExceptions = true
	bytecode.EmitThrow(g, vr(exc))
}

func (g *Generator) emitArgumentCount(dst *RegisterID) *RegisterID {
	bytecode.EmitArgumentCount(g, vr(dst))
	return dst
}

func (g *Generator) labelScopeDepth() int { return g.localScopeDepth + g.finallyDepth }

func (g *Generator) emitThrowStaticErrorFromRegister(errorType bytecode.ErrorTypeWithExtension, raw *RegisterID) {
	defer g.refScope()()
	message := g.keep(g.newTemporary())
	g.emitToString(message, raw)
	bytecode.EmitThrowStaticError(g, vr(message), errorType)
}

func (g *Generator) emitThrowStaticError(errorType bytecode.ErrorTypeWithExtension, message string) {
	s := g.addStringConstant(ident.FromString(message))
	bytecode.EmitThrowStaticError(g, vr(g.addConstantValue(s, bytecode.SourceCodeRepresentationOther)), errorType)
}

func (g *Generator) emitThrowReferenceError(message string) {
	g.emitThrowStaticError(bytecode.ErrorTypeReferenceError, message)
}

func (g *Generator) emitThrowTypeError(message string) {
	g.emitThrowStaticError(bytecode.ErrorTypeTypeError, message)
}

func (g *Generator) emitThrowRangeError(message string) {
	g.emitThrowStaticError(bytecode.ErrorTypeRangeError, message)
}

func (g *Generator) emitThrowOutOfMemoryError() {
	g.emitThrowStaticError(bytecode.ErrorTypeOutOfMemoryError, "")
}

// emitPushFunctionNameScope binds a named function expression's name to the
// callee. In strict code the name acts like a const; in sloppy code
// assignments to it are silently ignored, which we get by not reporting the
// scope as lexical.
func (g *Generator) emitPushFunctionNameScope(property ident.Identifier, callee *RegisterID, isCaptured bool) {
	var nameScopeEnvironment parser.VariableEnvironment
	entry, _ := nameScopeEnvironment.Add(property)
	if isCaptured {
		entry.SetIsCaptured()
	}
	entry.SetIsConst() // The function name scope name acts like a const variable.
	g.pushLexicalScopeInternal(&nameScopeEnvironment, TDZCheckOptimize, NestedScopeIsNotNested, nil, notUnderTDZ, ScopeTypeFunctionNameScope, scopeRegisterVar)
	shouldTreatAsLexicalVariable := g.ecmaMode.IsStrict()
	top := g.lexicalScopeStack[len(g.lexicalScopeStack)-1]
	e, _ := top.symbolTable.Get(property)
	functionVar := g.variableForLocalEntry(property, e, top.symbolTableConstantIndex, shouldTreatAsLexicalVariable)
	g.emitPutToScope(top.scope, functionVar, callee, bytecode.ThrowIfNotFound, bytecode.InitializationModeNotInitialization)
}

func (g *Generator) pushLocalControlFlowScope() {
	g.controlFlowScopeStack = append(g.controlFlowScopeStack, controlFlowScope{lexicalScopeIndex: g.currentLexicalScopeIndex()})
	g.localScopeDepth++
	g.localScopeCount++
}

func (g *Generator) popLocalControlFlowScope() {
	if g.controlFlowScopeStack[len(g.controlFlowScopeStack)-1].isFinally {
		panic("bytecompiler: popping a finally scope as a local scope")
	}
	g.controlFlowScopeStack = g.controlFlowScopeStack[:len(g.controlFlowScopeStack)-1]
	g.localScopeDepth--
}

func (g *Generator) emitPushCatchScope(environment *parser.VariableEnvironment, scopeType ScopeType) {
	g.pushLexicalScopeInternal(environment, TDZCheckOptimize, NestedScopeIsNotNested, nil, underTDZ, scopeType, scopeRegisterBlock)
}

func (g *Generator) emitPopCatchScope(environment *parser.VariableEnvironment) {
	g.popLexicalScopeInternal(environment)
}

func (g *Generator) pushFinallyControlFlowScope(finallyContext *FinallyContext) {
	g.controlFlowScopeStack = append(g.controlFlowScopeStack, controlFlowScope{isFinally: true, lexicalScopeIndex: g.currentLexicalScopeIndex(), finallyContext: finallyContext})
	g.finallyDepth++
	g.currentFinallyContext = finallyContext
}

func (g *Generator) popFinallyControlFlowScope() {
	g.currentFinallyContext = g.currentFinallyContext.outerContext
	g.finallyDepth--
	g.controlFlowScopeStack = g.controlFlowScopeStack[:len(g.controlFlowScopeStack)-1]
}

// --- switch ----------------------------------------------------------------------

func (g *Generator) beginSwitch(scrutineeRegister *RegisterID, typ switchType) {
	switch typ {
	case switchTypeImmediate, switchTypeImmediateList:
		tableIndex := len(g.codeBlock.SwitchJumpTables)
		g.codeBlock.AddSwitchJumpTable()
		bytecode.EmitSwitchImm(g, uint32(tableIndex), vr(scrutineeRegister))
	case switchTypeCharacter, switchTypeCharacterList:
		tableIndex := len(g.codeBlock.SwitchJumpTables)
		g.codeBlock.AddSwitchJumpTable()
		bytecode.EmitSwitchChar(g, uint32(tableIndex), vr(scrutineeRegister))
	case switchTypeString:
		tableIndex := len(g.codeBlock.StringSwitchJumpTables)
		g.codeBlock.AddStringSwitchJumpTable()
		bytecode.EmitSwitchString(g, uint32(tableIndex), vr(scrutineeRegister))
	default:
		panic("bytecompiler: beginSwitch without a switch type")
	}
	g.switchContextStack = append(g.switchContextStack, switchInfo{g.lastInstruction, typ})
}

// switchKey returns the jump table key of a case clause.
func switchKey(node parser.ExpressionNode, typ switchType) int32 {
	switch typ {
	case switchTypeImmediate, switchTypeImmediateList:
		return int32(node.(*parser.NumberNode).Value)
	default:
		return int32(node.(*parser.StringNode).Value.UTF16()[0])
	}
}

func (g *Generator) endSwitch(labels []*Label, nodes []parser.ExpressionNode, defaultLabel *Label, minValue, maxValue int32) {
	info := g.switchContextStack[len(g.switchContextStack)-1]
	g.switchContextStack = g.switchContextStack[:len(g.switchContextStack)-1]
	tableIndex := g.instructions[info.bytecodeOffset+1] // table index is the first operand

	switch info.switchType {
	case switchTypeImmediate, switchTypeCharacter:
		jumpTable := &g.codeBlock.SwitchJumpTables[tableIndex]
		jumpTable.Min = minValue
		jumpTable.BranchOffsets = make([]int32, maxValue-minValue+1)
		for i, label := range labels {
			// The clause labels are emitted before this, so none is forward.
			key := switchKey(nodes[i], info.switchType) - minValue
			jumpTable.Add(key, int32(label.bindAt(info.bytecodeOffset)))
		}
		jumpTable.DefaultOffset = int32(defaultLabel.bindAt(info.bytecodeOffset))

	case switchTypeImmediateList, switchTypeCharacterList:
		jumpTable := &g.codeBlock.SwitchJumpTables[tableIndex]
		jumpTable.Min = 1<<31 - 1
		jumpTable.IsList = true
		branchOffsets := make([]int32, 0, len(labels)*2)
		alreadyHandled := make(map[int32]struct{})
		for i, label := range labels {
			key := switchKey(nodes[i], info.switchType)
			// With duplicate keys the first one wins.
			if _, dup := alreadyHandled[key]; dup {
				continue
			}
			alreadyHandled[key] = struct{}{}
			branchOffsets = append(branchOffsets, key, int32(label.bindAt(info.bytecodeOffset)))
		}
		jumpTable.BranchOffsets = branchOffsets
		jumpTable.DefaultOffset = int32(defaultLabel.bindAt(info.bytecodeOffset))

	case switchTypeString:
		jumpTable := &g.codeBlock.StringSwitchJumpTables[tableIndex]
		for i, label := range labels {
			clause := nodes[i].(*parser.StringNode).Value
			key := clause.String()
			if _, exists := jumpTable.OffsetTable[key]; exists {
				continue
			}
			jumpTable.OffsetTable[key] = bytecode.StringJumpTableEntry{
				BranchOffset: int32(label.bindAt(info.bytecodeOffset)),
				IndexInTable: uint32(len(jumpTable.OffsetTable)),
			}
			length := uint32(clause.Len())
			jumpTable.MinLength = min(jumpTable.MinLength, length)
			jumpTable.MaxLength = max(jumpTable.MaxLength, length)
		}
		jumpTable.DefaultOffset = int32(defaultLabel.bindAt(info.bytecodeOffset))
		if len(jumpTable.OffsetTable) == 0 {
			jumpTable.MinLength = 0
			jumpTable.MaxLength = 0
		}

	default:
		panic("bytecompiler: endSwitch without a switch type")
	}
}

// emitThrowExpressionTooDeepException marks the compilation as failed; the
// expression info still yields an accurate line number.
func (g *Generator) emitThrowExpressionTooDeepException() *RegisterID {
	g.expressionTooDeep = true
	return g.newTemporary()
}

func (g *Generator) isArgumentNumber(id ident.Identifier, argumentNumber int) bool {
	registerID := g.variable(id, ThisResolutionLocal).Local()
	if registerID == nil {
		return false
	}
	return registerID.Index() == bytecode.CallFrameSlotFirstArgument+argumentNumber
}

const readonlyPropertyWriteError = "Attempted to assign to readonly property."

// emitReadOnlyExceptionIfNeeded throws for writes to read-only variables:
// always in strict mode, and for const variables (not the callee name) in
// sloppy mode.
func (g *Generator) emitReadOnlyExceptionIfNeeded(variable Variable) bool {
	if g.ecmaMode.IsStrict() || variable.IsConst() {
		g.emitThrowTypeError(readonlyPropertyWriteError)
		return true
	}
	return false
}

// --- finally -----------------------------------------------------------------------

func (g *Generator) emitTryWithFinallyThatDoesNotShadowException(emitTry, emitFinally func(*Generator)) {
	defer g.refScope()()
	finallyLabel := g.newLabel()
	finallyContext := g.newFinallyContext(finallyLabel)
	g.pushFinallyControlFlowScope(finallyContext)
	g.emitTryWithFinallyThatDoesNotShadowExceptionIn(finallyContext, emitTry, emitFinally)
	g.popFinallyControlFlowScope()
}

func (g *Generator) emitTryWithFinallyThatDoesNotShadowExceptionIn(finallyContext *FinallyContext, emitTry, emitFinally func(*Generator)) {
	defer g.refScope()()
	tryStartLabel := g.newEmittedLabel()
	td := g.pushTry(tryStartLabel, finallyContext.finallyLabel, bytecode.HandlerSynthesizedFinally)
	emitTry(g)
	tryEndLabel := g.newEmittedLabel()
	g.popTry(td, tryEndLabel)

	done := g.newLabel()

	g.emitLabel(finallyContext.finallyLabel)
	g.emitOutOfLineFinallyHandler(finallyContext.completionValueRegister, finallyContext.completionTypeRegister, td)

	tryInFinallyStartLabel := g.newEmittedLabel()
	catchInFinallyLabel := g.newLabel()
	tryInFinallyData := g.pushTry(tryInFinallyStartLabel, catchInFinallyLabel, bytecode.HandlerSynthesizedCatch)
	emitFinally(g)
	tryInFinallyEndLabel := g.newEmittedLabel()
	g.popTry(tryInFinallyData, tryInFinallyEndLabel)

	g.emitFinallyCompletion(finallyContext, done)

	// If the finally block was entered because of an exception, exceptions
	// thrown by the finally block must not replace it: catch them and rethrow
	// the original one.
	g.emitLabel(catchInFinallyLabel)

	exceptionRegister := g.keep(g.newTemporary())
	g.emitOutOfLineCatchHandler(exceptionRegister, nil, tryInFinallyData)
	// A synthesized catch never resolves names, so the scope register need
	// not be restored.

	g.emitJumpIfTrue(g.emitEqualityOp(bytecode.OpStricteqID, g.newTemporary(), finallyContext.completionTypeRegister,
		g.emitLoadCompletionType(nil, CompletionThrow)), tryInFinallyEndLabel)
	g.emitThrow(exceptionRegister)

	g.emitLabel(done)
}

// emitJumpViaFinallyIfNeeded routes a break or continue to jumpTarget
// through the finally blocks between here and the target.
func (g *Generator) emitJumpViaFinallyIfNeeded(targetLabelScopeDepth int, jumpTarget *Label) bool {
	numberOfScopesToCheckForFinally := g.labelScopeDepth() - targetLabelScopeDepth
	if numberOfScopesToCheckForFinally == 0 {
		return false
	}

	var innermostFinallyContext, outermostFinallyContext *FinallyContext
	scopeIndex := len(g.controlFlowScopeStack) - 1
	for ; numberOfScopesToCheckForFinally > 0; numberOfScopesToCheckForFinally-- {
		scope := &g.controlFlowScopeStack[scopeIndex]
		scopeIndex--
		if scope.isFinallyScope() {
			finallyContext := scope.finallyContext
			if innermostFinallyContext == nil {
				innermostFinallyContext = finallyContext
			}
			outermostFinallyContext = finallyContext
			finallyContext.incNumberOfBreaksOrContinues()
		}
	}
	if outermostFinallyContext == nil {
		return false // No finallys to thread through.
	}

	jumpID := bytecodeOffsetToJumpID(len(g.instructions))
	lexicalScopeIndex := g.labelScopeDepthToLexicalScopeIndex(targetLabelScopeDepth)
	outermostFinallyContext.registerJump(jumpID, lexicalScopeIndex, jumpTarget)

	g.emitLoadCompletionType(innermostFinallyContext.completionTypeRegister, jumpID)
	g.emitJump(innermostFinallyContext.finallyLabel)
	return true // We'll be jumping to a finally block.
}

// emitReturnViaFinallyIfNeeded routes a return through the enclosing
// finally blocks.
func (g *Generator) emitReturnViaFinallyIfNeeded(returnRegister *RegisterID) bool {
	var innermostFinallyContext *FinallyContext
	for i := len(g.controlFlowScopeStack) - 1; i >= 0; i-- {
		scope := &g.controlFlowScopeStack[i]
		if scope.isFinallyScope() {
			finallyContext := scope.finallyContext
			if innermostFinallyContext == nil {
				innermostFinallyContext = finallyContext
			}
			finallyContext.setHandlesReturns()
		}
	}
	if innermostFinallyContext == nil {
		return false // No finallys to thread through.
	}

	g.emitLoadCompletionType(innermostFinallyContext.completionTypeRegister, CompletionReturn)
	g.move(innermostFinallyContext.completionValueRegister, returnRegister)
	g.emitJump(innermostFinallyContext.finallyLabel)
	return true // We'll be jumping to a finally block.
}

// emitFinallyCompletion emits the code at the end of a finally block that
// continues with the completion that entered it.
func (g *Generator) emitFinallyCompletion(context *FinallyContext, normalCompletionLabel *Label) {
	defer g.refScope()()
	isCompletion := func(t CompletionType) *RegisterID {
		return g.emitEqualityOp(bytecode.OpStricteqID, g.newTemporary(), context.completionTypeRegister, g.emitLoadCompletionType(nil, t))
	}

	if context.numberOfBreaksOrContinues > 0 || context.handlesReturns {
		g.emitJumpIfTrue(isCompletion(CompletionNormal), normalCompletionLabel)

		outerContext := context.outerContext
		numberOfJumps := len(context.jumps)

		// Break or continue completions whose target lies inside the next
		// outer finally context: no other finally blocks need to run, so
		// jump straight to the target (as a normal completion):
		//
		//     try {
		//         for (...) {
		//             try {
		//                 continue; // completionType = jumpID of the loop top
		//             } finally {
		//             } // jump to the top of the loop
		//         }
		//     } finally {
		//     }
		for _, jump := range context.jumps {
			nextLabel := g.newLabel()
			g.emitJumpIfFalse(isCompletion(jump.jumpID), nextLabel)

			g.restoreScopeRegisterTo(jump.targetLexicalScopeIndex)
			g.emitLoadCompletionType(context.completionTypeRegister, CompletionNormal)
			g.emitJump(jump.targetLabel)

			g.emitLabel(nextLabel)
		}

		// Completions that leave this finally context.
		if outerContext != nil {
			if context.handlesReturns {
				// A return inside an inner finally: hand the completion to the
				// next outer finally, which decides what happens next.
				isNotReturnLabel := g.newLabel()
				g.emitJumpIfFalse(isCompletion(CompletionReturn), isNotReturnLabel)

				g.move(outerContext.completionTypeRegister, context.completionTypeRegister)
				g.move(outerContext.completionValueRegister, context.completionValueRegister)
				g.emitJump(outerContext.finallyLabel)

				g.emitLabel(isNotReturnLabel)
			}

			hasBreaksOrContinuesThatEscapeCurrentFinally := int(context.numberOfBreaksOrContinues) > numberOfJumps
			if hasBreaksOrContinuesThatEscapeCurrentFinally {
				// A completion type above Throw is a break/continue jump ID
				// (Return was ruled out above). Pass it on to the next outer
				// finally, which must run before we reach the target.
				isThrowOrNormalLabel := g.newLabel()
				g.emitJumpIfTrue(g.emitBinaryOpT(bytecode.OpBeloweqID, g.newTemporary(), context.completionTypeRegister,
					g.emitLoadCompletionType(nil, CompletionThrow), 0), isThrowOrNormalLabel)

				g.move(outerContext.completionTypeRegister, context.completionTypeRegister)
				g.emitJump(outerContext.finallyLabel)

				g.emitLabel(isThrowOrNormalLabel)
			}
		} else if context.handlesReturns {
			// The outermost finally can execute the return.
			notReturnLabel := g.newLabel()
			g.emitJumpIfFalse(isCompletion(CompletionReturn), notReturnLabel)

			g.emitWillLeaveCallFrameDebugHook()
			g.emitReturn(context.completionValueRegister)

			g.emitLabel(notReturnLabel)
		}
	}

	// Only Normal and Throw completions are left.
	g.emitJumpIfFalse(isCompletion(CompletionThrow), normalCompletionLabel)

	// Rethrow the exception that entered the finally block. Outer finally
	// contexts have their own handlers, so nothing else is needed.
	g.emitThrow(context.completionValueRegister)
}

// --- Optional chains -------------------------------------------------------------

func (g *Generator) pushOptionalChainTarget() {
	g.optionalChainTargetStack = append(g.optionalChainTargetStack, g.newLabel())
}

func (g *Generator) pushOptionalChainTargetLabel(existingTarget *Label) {
	g.optionalChainTargetStack = append(g.optionalChainTargetStack, existingTarget)
}

func (g *Generator) popOptionalChainTarget() {
	n := len(g.optionalChainTargetStack)
	target := g.optionalChainTargetStack[n-1]
	g.optionalChainTargetStack = g.optionalChainTargetStack[:n-1]
	g.emitLabel(target)
}

func (g *Generator) popOptionalChainTargetWithResult(dst *RegisterID, isDelete bool) {
	endLabel := g.newLabel()
	g.emitJump(endLabel)

	g.popOptionalChainTarget()
	if isDelete {
		g.emitLoad(dst, bytecode.Bool(true))
	} else {
		g.emitLoad(dst, bytecode.Undefined{})
	}

	g.emitLabel(endLabel)
}

func (g *Generator) discardOptionalChainTarget() {
	g.optionalChainTargetStack = g.optionalChainTargetStack[:len(g.optionalChainTargetStack)-1]
}

func (g *Generator) emitOptionalCheck(src *RegisterID) {
	g.emitJumpIfTrue(g.emitIsUndefinedOrNull(g.newTemporary(), src), g.optionalChainTargetStack[len(g.optionalChainTargetStack)-1])
}

// --- using declarations ---------------------------------------------------------

func (g *Generator) currentUsingScope() *usingScope {
	return g.usingScopeStack[len(g.usingScopeStack)-1]
}

func (g *Generator) emitPrepareDisposable(value *RegisterID, divot parser.TextPosition, isAsync bool) {
	defer g.refScope()()
	scope := g.currentUsingScope()
	slot := &scope.slots[scope.nextSlot]
	scope.nextSlot++
	slot.isAsync = isAsync
	g.move(slot.value, value)

	c := bytecode.LinkTimeConstantGetDisposeMethod
	if isAsync {
		c = bytecode.LinkTimeConstantGetAsyncDisposeMethod
	}
	getDisposeMethodFunc := g.keep(g.moveLinkTimeConstant(nil, c))
	args := g.newCallArguments(nil, 1)
	g.emitLoad(args.thisRegister(), bytecode.Undefined{})
	g.move(args.argumentRegister(0), value)
	g.emitCall(slot.method, getDisposeMethodFunc, NoExpectedFunction, args, divot, divot, divot, DebuggableCallNo)

	// Mark the slot reached only after the method lookup succeeded: if it
	// threw, no resource record exists and the finally block skips the slot.
	if isAsync {
		g.emitLoad(slot.reached, bytecode.Bool(true))
	}
}

// emitUsingBodyScope runs emitBody in a try whose finally block disposes
// the resources of the scope's using declarations in reverse order
// (DisposeResources).
func (g *Generator) emitUsingBodyScope(usingCount int, hasAwaitUsing bool, emitBody func(*Generator)) {
	defer g.refScope()()

	// Allocate the slots and set the methods to undefined before the try, so
	// that the finally block sees defined values if an initializer throws.
	scope := &usingScope{hasAwaitUsing: hasAwaitUsing}
	g.usingScopeStack = append(g.usingScopeStack, scope)
	for range usingCount {
		valueCopy := g.keep(g.newTemporary())
		method := g.keep(g.newTemporary())
		var reached *RegisterID
		g.emitLoad(valueCopy, bytecode.Undefined{})
		g.emitLoad(method, bytecode.Undefined{})
		if hasAwaitUsing {
			reached = g.keep(g.newTemporary())
			g.emitLoad(reached, bytecode.Bool(false))
		}
		scope.slots = append(scope.slots, usingSlot{value: valueCopy, method: method, reached: reached})
	}

	thrownValue := g.keep(g.newTemporary())
	g.emitLoad(thrownValue, bytecode.Undefined{})

	finallyLabel := g.newLabel()
	finallyContext := g.newFinallyContext(finallyLabel)
	g.pushFinallyControlFlowScope(finallyContext)

	tryStartLabel := g.newEmittedLabel()
	td := g.pushTry(tryStartLabel, finallyContext.finallyLabel, bytecode.HandlerSynthesizedFinally)
	emitBody(g)
	tryEndLabel := g.newEmittedLabel()
	g.popTry(td, tryEndLabel)

	// Finally block: unrolled dispose calls in reverse order.
	done := g.newLabel()

	g.emitLabel(finallyContext.finallyLabel)
	g.emitOutOfLineExceptionHandler(finallyContext.completionValueRegister, thrownValue, finallyContext.completionTypeRegister, td)

	pendingError := g.keep(g.newTemporary())
	hasError := g.keep(g.newTemporary())
	disposeThrew := g.keep(g.newTemporary())
	g.emitLoad(pendingError, bytecode.Undefined{})
	g.emitLoad(hasError, bytecode.Bool(false))
	g.emitLoad(disposeThrew, bytecode.Bool(false))

	// If the body threw, seed pendingError with the thrown value.
	g.scoped(func() {
		afterInit := g.newLabel()
		g.emitJumpIfFalse(g.emitEqualityOp(bytecode.OpStricteqID, g.newTemporary(), finallyContext.completionTypeRegister,
			g.emitLoadCompletionType(nil, CompletionThrow)), afterInit)
		g.move(pendingError, thrownValue)
		g.emitLoad(hasError, bytecode.Bool(true))
		g.emitLabel(afterInit)
	})

	divot := parser.TextPosition(g.scope.StartOffset())

	// Async disposal state (DisposeResources): needsAwait and hasAwaited,
	// only for scopes with an await using declaration.
	var needsAwait, hasAwaited *RegisterID
	if hasAwaitUsing {
		needsAwait = g.keep(g.newTemporary())
		hasAwaited = g.keep(g.newTemporary())
		g.emitLoad(needsAwait, bytecode.Bool(false))
		g.emitLoad(hasAwaited, bytecode.Bool(false))
	}

	// Temporaries shared by the catch handlers of all slots.
	caughtException := g.keep(g.newTemporary())
	caughtValue := g.keep(g.newTemporary())
	suppressedErrorCtor := g.keep(g.newTemporary())

	emitSuppressedErrorCatch := func(trySlotData *tryData, catchLabel *Label) {
		defer g.refScope()()
		g.emitLabel(catchLabel)
		g.emitOutOfLineExceptionHandler(caughtException, caughtValue, nil, trySlotData)

		afterCatch := g.newLabel()
		firstError := g.newLabel()
		g.emitJumpIfFalse(hasError, firstError)

		g.moveLinkTimeConstant(suppressedErrorCtor, bytecode.LinkTimeConstantSuppressedError)
		seArgs := g.newCallArguments(nil, 2)
		g.move(seArgs.argumentRegister(0), caughtValue)
		g.move(seArgs.argumentRegister(1), pendingError)
		g.emitConstruct(pendingError, suppressedErrorCtor, suppressedErrorCtor, NoExpectedFunction, seArgs, divot, divot, divot)
		g.emitJump(afterCatch)

		g.emitLabel(firstError)
		g.move(pendingError, caughtValue)
		g.emitLoad(hasError, bytecode.Bool(true))

		g.emitLabel(afterCatch)
		g.emitLoad(disposeThrew, bytecode.Bool(true))
	}

	emitAwaitUndefined := func() {
		defer g.refScope()()
		tmp := g.keep(g.newTemporary())
		g.emitLoad(tmp, bytecode.Undefined{})
		g.emitAwait(tmp, tmp, divot)
	}

	// Dispose in reverse declaration order. needsAwait can only become true
	// after an async slot's null-value case, so the step 3.d check before
	// the first async slot (in disposal order) is dead and omitted.
	sawAsyncSlotInDisposalOrder := false
	for i := len(scope.slots) - 1; i >= 0; i-- {
		slot := scope.slots[i]
		g.scoped(func() {
			skipSlot := g.newLabel()

			if slot.isAsync {
				sawAsyncSlotInDisposalOrder = true
				// A declaration that was never reached adds no resource
				// record: skip it without setting needsAwait.
				g.emitJumpIfFalse(slot.reached, skipSlot)

				methodDefined := g.newLabel()
				g.emitJumpIfFalse(g.emitIsUndefined(g.newTemporary(), slot.method), methodDefined)

				// `await using x = null`: step 3.f sets needsAwait, no call.
				g.emitLoad(needsAwait, bytecode.Bool(true))
				g.emitJump(skipSlot)

				// Call the method and await the result.
				g.emitLabel(methodDefined)
				catchLabel := g.newLabel()
				trySlotStart := g.newEmittedLabel()
				trySlotData := g.pushTry(trySlotStart, catchLabel, bytecode.HandlerSynthesizedCatch)

				result := g.keep(g.newTemporary())
				disposeArgs := g.newCallArguments(nil, 0)
				g.move(disposeArgs.thisRegister(), slot.value)
				g.emitCall(result, slot.method, NoExpectedFunction, disposeArgs, divot, divot, divot, DebuggableCallNo)

				// A rejected Await still passes a microtask boundary, so set
				// hasAwaited first.
				g.emitLoad(hasAwaited, bytecode.Bool(true))
				g.emitAwait(result, result, divot)

				g.emitJump(skipSlot)
				trySlotEnd := g.newEmittedLabel()
				g.popTry(trySlotData, trySlotEnd)

				emitSuppressedErrorCatch(trySlotData, catchLabel)
			} else {
				// Step 3.d: if needsAwait && !hasAwaited, Await(undefined)
				// first. Only possible after an async slot.
				if sawAsyncSlotInDisposalOrder {
					skipAwaitCheck := g.newLabel()
					g.emitJumpIfFalse(needsAwait, skipAwaitCheck)
					g.emitJumpIfTrue(hasAwaited, skipAwaitCheck)
					emitAwaitUndefined()
					g.emitLoad(needsAwait, bytecode.Bool(false))
					g.emitLabel(skipAwaitCheck)
				}

				g.emitJumpIfTrue(g.emitIsUndefined(g.newTemporary(), slot.method), skipSlot)

				catchLabel := g.newLabel()
				trySlotStart := g.newEmittedLabel()
				trySlotData := g.pushTry(trySlotStart, catchLabel, bytecode.HandlerSynthesizedCatch)

				disposeArgs := g.newCallArguments(nil, 0)
				g.move(disposeArgs.thisRegister(), slot.value)
				g.emitCallIgnoreResult(g.newTemporary(), slot.method, NoExpectedFunction, disposeArgs, divot, divot, divot, DebuggableCallNo)

				g.emitJump(skipSlot)
				trySlotEnd := g.newEmittedLabel()
				g.popTry(trySlotData, trySlotEnd)

				emitSuppressedErrorCatch(trySlotData, catchLabel)
			}

			g.emitLabel(skipSlot)
		})
	}

	// Step 6: a trailing Await(undefined) if needsAwait && !hasAwaited. If
	// the first declared slot is sync, its step 3.d consumed it already.
	if hasAwaitUsing && scope.slots[0].isAsync {
		skipFinalAwait := g.newLabel()
		g.emitJumpIfFalse(needsAwait, skipFinalAwait)
		g.emitJumpIfTrue(hasAwaited, skipFinalAwait)
		emitAwaitUndefined()
		g.emitLabel(skipFinalAwait)
	}

	// If a dispose threw, throw the pending error (possibly a chain of
	// SuppressedErrors); this beats break/continue/return completions.
	afterDisposeCheck := g.newLabel()
	g.emitJumpIfFalse(disposeThrew, afterDisposeCheck)
	g.emitThrow(pendingError)
	g.emitLabel(afterDisposeCheck)

	g.emitFinallyCompletion(finallyContext, done)
	g.emitLabel(done)

	g.popFinallyControlFlowScope()
	g.usingScopeStack = g.usingScopeStack[:len(g.usingScopeStack)-1]
}

func (g *Generator) emitBodyWithUsingIfNeeded(usingCount int, hasAwaitUsing bool, emitBody func(*Generator)) {
	if usingCount > 0 {
		g.emitUsingBodyScope(usingCount, hasAwaitUsing, emitBody)
	} else {
		emitBody(g)
	}
}
