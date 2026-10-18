package bytecompiler

import (
	"github.com/baslentfert/jsc2go/bytecode"
	"github.com/baslentfert/jsc2go/ident"
	"github.com/baslentfert/jsc2go/parser"
)

// Calls, construction, function objects and arrays (BytecodeGenerator.cpp,
// plus CallArguments from NodesCodegen.cpp).

// CallArguments allocates the registers of an outgoing call: "this"
// followed by the arguments, laid out so that the callee frame is aligned.
type CallArguments struct {
	argumentsNode *parser.ArgumentsNode
	argv          []*RegisterID
}

// newCallArguments allocates registers for this plus the arguments of
// argumentsNode plus additionalArguments; the caller's refScope holds them.
func (g *Generator) newCallArguments(argumentsNode *parser.ArgumentsNode, additionalArguments int) *CallArguments {
	argumentCountIncludingThis := 1 + additionalArguments // 'this' register
	if argumentsNode != nil {
		argumentCountIncludingThis += len(argumentsNode.Args)
	}

	argvSize := argumentCountIncludingThis
	if (bytecode.CallFrameHeaderSizeInRegisters+argvSize)%stackAlignmentRegisters != 0 {
		argvSize++
	}
	argvSize++ // for the stack offset adjustment case

	allocated := make([]*RegisterID, argvSize)
	// Index 0 is left out here and allocated below if alignment needs it.
	index := len(allocated)
	g.newTemporaries(len(allocated)-1, func(slot *RegisterID) {
		index--
		allocated[index] = g.keep(slot)
	})

	c := &CallArguments{argumentsNode: argumentsNode}
	if (-allocated[1].Index()+bytecode.CallFrameHeaderSizeInRegisters)%stackAlignmentRegisters != 0 {
		allocated[0] = g.keep(g.newTemporary())
		c.argv = allocated[:argumentCountIncludingThis]
	} else {
		c.argv = allocated[1 : 1+argumentCountIncludingThis]
	}
	return c
}

func (c *CallArguments) thisRegister() *RegisterID            { return c.argv[0] }
func (c *CallArguments) argumentRegister(i int) *RegisterID   { return c.argv[i+1] }
func (c *CallArguments) argumentCountIncludingThis() int      { return len(c.argv) }
func (c *CallArguments) ArgumentsNode() *parser.ArgumentsNode { return c.argumentsNode }
func (c *CallArguments) stackOffset() uint32 {
	return uint32(-c.argv[0].Index() + bytecode.CallFrameHeaderSizeInRegisters)
}

// reserveCallFrame allocates the registers the callee frame header uses.
func (g *Generator) reserveCallFrame() {
	for range bytecode.CallFrameHeaderSizeInRegisters {
		g.keep(g.newTemporary())
	}
}

// makeFunction creates the executable for a nested function.
func (g *Generator) makeFunction(metadata *parser.FunctionMetadataNode) *bytecode.UnlinkedFunctionExecutable {
	newDerivedContextType := parser.DerivedContextTypeNone
	newEvalContextType := parser.EvalContextTypeFunctionEvalContext
	needsClassFieldInitializer := metadata.NeedsClassFieldInitializer
	privateBrandRequirement := metadata.PrivateBrandRequirement
	switch metadata.ParseMode {
	case parser.ArrowFunctionMode, parser.AsyncArrowFunctionMode, parser.AsyncArrowFunctionBodyMode:
		if g.constructorKind() == parser.ConstructorKindExtends || g.isDerivedConstructorContext() {
			newDerivedContextType = parser.DerivedContextTypeDerivedConstructorContext
			needsClassFieldInitializer = g.codeBlock.Info.NeedsClassFieldInitializer
			privateBrandRequirement = g.codeBlock.Info.PrivateBrandRequirement
		} else if g.codeBlock.Info.IsClassContext || g.isDerivedClassContext() {
			newDerivedContextType = parser.DerivedContextTypeDerivedMethodContext
		}
		newEvalContextType = g.codeBlock.Info.EvalContextType
	}

	variablesUnderTDZ := g.getVariablesUnderTDZ()
	var generatorOrAsyncWrapperFunctionParameterNames []ident.Identifier
	parentPrivateNameEnvironment := g.getAvailablePrivateAccessNames()

	parseMode := metadata.ParseMode
	constructAbility := bytecode.CannotConstruct
	if parseMode.CanConstruct() {
		constructAbility = bytecode.CanConstruct
	}
	if parseMode == parser.MethodMode && metadata.ConstructorKind != parser.ConstructorKindNone {
		constructAbility = bytecode.CanConstruct
	}

	if g.codeBlock.ParseMode().IsGeneratorOrAsyncFunctionWrapper() && parseMode.IsGeneratorOrAsyncFunctionBody() {
		generatorOrAsyncWrapperFunctionParameterNames = g.getParameterNames()
	}

	kind := bytecode.UnlinkedNormalFunction
	if g.isBuiltinFunction {
		kind = bytecode.UnlinkedBuiltinFunction
	}
	return bytecode.NewUnlinkedFunctionExecutable(g.sourceCode, metadata, bytecode.UnlinkedFunctionExecutableConfig{
		Kind:                    kind,
		ConstructAbility:        constructAbility,
		InlineAttribute:         bytecode.InlineAttributeNone,
		ScriptMode:              g.scriptMode(),
		ParentScopeTDZVariables: variablesUnderTDZ,
		GeneratorOrAsyncWrapperFunctionParameterNames: generatorOrAsyncWrapperFunctionParameterNames,
		ParentPrivateNameEnvironment:                  parentPrivateNameEnvironment,
		DerivedContextType:                            newDerivedContextType,
		EvalContextType:                               newEvalContextType,
		NeedsClassFieldInitializer:                    needsClassFieldInitializer,
		PrivateBrandRequirement:                       privateBrandRequirement,
	})
}

// emitNewArray creates an array from the first length elements, which must
// not contain holes or spreads.
func (g *Generator) emitNewArray(dst *RegisterID, elements []*parser.ElementNode, length int, recommendedIndexingType bytecode.IndexingType) *RegisterID {
	defer g.refScope()()
	var argv []*RegisterID
	for _, n := range elements {
		if length == 0 {
			break
		}
		length--
		argv = append(argv, g.keep(g.newTemporary()))
		// op_new_array requires a sequential range of registers.
		if len(argv) > 1 && argv[len(argv)-1].Index() != argv[len(argv)-2].Index()-1 {
			panic("bytecompiler: op_new_array registers are not sequential")
		}
		g.emitNode(argv[len(argv)-1], n.Value)
	}
	var first bytecode.VirtualRegister
	if len(argv) > 0 {
		first = argv[0].VirtualRegister()
	}
	bytecode.EmitNewArray(g, vr(dst), first, uint32(len(argv)), recommendedIndexingType)
	return dst
}

func (g *Generator) emitNewArrayByReversingArguments(dst *RegisterID, callArguments *CallArguments) *RegisterID {
	defer g.refScope()()
	length := callArguments.argumentCountIncludingThis() - 1
	temporary := g.keep(g.newTemporary())
	for index := 0; index < length/2; index++ {
		low := callArguments.argumentRegister(index)
		high := callArguments.argumentRegister(length - 1 - index)
		g.move(temporary, low)
		g.move(low, high)
		g.move(high, temporary)
	}
	var first bytecode.VirtualRegister
	if length > 0 {
		first = callArguments.argumentRegister(length - 1).VirtualRegister()
	}
	bytecode.EmitNewArray(g, vr(dst), first, uint32(length), bytecode.ArrayWithUndecided)
	return dst
}

func (g *Generator) emitNewArrayWithSpread(dst *RegisterID, elements []*parser.ElementNode) *RegisterID {
	defer g.refScope()()
	bitVector := make([]bool, 0, len(elements))
	argv := make([]*RegisterID, 0, len(elements))
	for _, node := range elements {
		_, isSpread := node.Value.(*parser.SpreadExpressionNode)
		bitVector = append(bitVector, isSpread)
		argv = append(argv, g.keep(g.newTemporary()))
		// op_new_array_with_spread requires a sequential range of registers.
		if len(argv) > 1 && argv[len(argv)-1].Index() != argv[len(argv)-2].Index()-1 {
			panic("bytecompiler: op_new_array_with_spread registers are not sequential")
		}
	}

	for i, node := range elements {
		g.scoped(func() {
			if spread, ok := node.Value.(*parser.SpreadExpressionNode); ok {
				tmp := g.keep(g.newTemporary())
				g.emitNode(tmp, spread.Expression)
				g.emitExpressionInfo(spread.Divot, spread.DivotStart, spread.DivotEnd)
				bytecode.EmitSpread(g, vr(argv[i]), vr(tmp))
			} else {
				g.emitNode(argv[i], node.Value)
			}
		})
	}

	bitVectorIndex := g.codeBlock.AddBitVector(bitVector)
	bytecode.EmitNewArrayWithSpread(g, vr(dst), vr(argv[0]), uint32(len(argv)), uint32(bitVectorIndex))
	return dst
}

func (g *Generator) emitNewArrayWithSize(dst, length *RegisterID) *RegisterID {
	bytecode.EmitNewArrayWithSize(g, vr(dst), vr(length))
	return dst
}

func (g *Generator) emitNewArrayWithSpecies(dst, length, array *RegisterID) *RegisterID {
	bytecode.EmitNewArrayWithSpecies(g, vr(dst), vr(length), vr(array), g.nextValueProfileIndex())
	return dst
}

func (g *Generator) emitNewArrayBuffer(dst *RegisterID, array *bytecode.ArrayBuffer, recommendedIndexingType bytecode.IndexingType) *RegisterID {
	bytecode.EmitNewArrayBuffer(g, vr(dst), vr(g.addConstantValue(array, bytecode.SourceCodeRepresentationOther)), recommendedIndexingType)
	return dst
}

func (g *Generator) emitNewRegExp(dst *RegisterID, regExp *bytecode.RegExp) *RegisterID {
	bytecode.EmitNewRegExp(g, vr(dst), vr(g.addConstantValue(regExp, bytecode.SourceCodeRepresentationOther)))
	return dst
}

func (g *Generator) emitNewFunctionExpressionCommon(dst *RegisterID, function *parser.FunctionMetadataNode) {
	index := uint32(g.codeBlock.AddFunctionExpr(g.makeFunction(function)))

	switch function.ParseMode {
	case parser.GeneratorWrapperFunctionMode, parser.GeneratorWrapperMethodMode:
		bytecode.EmitNewGeneratorFuncExp(g, vr(dst), vr(g.scopeRegister), index)
	case parser.AsyncFunctionMode, parser.AsyncMethodMode, parser.AsyncArrowFunctionMode:
		bytecode.EmitNewAsyncFuncExp(g, vr(dst), vr(g.scopeRegister), index)
	case parser.AsyncGeneratorWrapperFunctionMode, parser.AsyncGeneratorWrapperMethodMode:
		bytecode.EmitNewAsyncGeneratorFuncExp(g, vr(dst), vr(g.scopeRegister), index)
	default:
		bytecode.EmitNewFuncExp(g, vr(dst), vr(g.scopeRegister), index)
	}
}

func (g *Generator) emitNewFunctionExpression(dst *RegisterID, fn parser.BaseFuncExprNode) *RegisterID {
	g.emitNewFunctionExpressionCommon(dst, fn.Metadata())
	return dst
}

func (g *Generator) emitNewDefaultConstructor(dst *RegisterID, constructorKind parser.ConstructorKind, name, ecmaName ident.Identifier,
	classSource parser.SourceCode, needsClassFieldInitializer bool, privateBrandRequirement parser.PrivateBrandRequirement) *RegisterID {
	executable := createDefaultConstructor(constructorKind, name, needsClassFieldInitializer, privateBrandRequirement)
	executable.SetEcmaName(ecmaName)
	executable.ClassSource = classSource

	index := uint32(g.codeBlock.AddFunctionExpr(executable))
	bytecode.EmitNewFuncExp(g, vr(dst), vr(g.scopeRegister), index)
	return dst
}

func (g *Generator) emitNewClassFieldInitializerFunction(dst *RegisterID, classElementDefinitions []parser.ClassElementDefinition, isDerived bool) *RegisterID {
	newDerivedContextType := parser.DerivedContextTypeNone
	superBinding := parser.SuperBindingNotNeeded
	if isDerived {
		newDerivedContextType = parser.DerivedContextTypeDerivedMethodContext
		superBinding = parser.SuperBindingNeeded
	}

	variablesUnderTDZ := g.getVariablesUnderTDZ()
	parentPrivateNameEnvironment := g.getAvailablePrivateAccessNames()

	metadata := &parser.FunctionMetadataNode{
		ImplementationVisibility: parser.ImplementationVisibilityPrivate,
		LexicallyScopedFeatures:  parser.StrictModeLexicallyScopedFeature,
		ConstructorKind:          parser.ConstructorKindNone,
		SuperBinding:             superBinding,
		ParseMode:                parser.ClassFieldInitializerMode,
	}
	metadata.FinishParsing(g.sourceCode, ident.Null, parser.FunctionModeMethodDefinition)

	kind := bytecode.UnlinkedNormalFunction
	if g.isBuiltinFunction {
		kind = bytecode.UnlinkedBuiltinFunction
	}
	initializer := bytecode.NewUnlinkedFunctionExecutable(g.sourceCode, metadata, bytecode.UnlinkedFunctionExecutableConfig{
		Kind:                         kind,
		ConstructAbility:             bytecode.CannotConstruct,
		InlineAttribute:              bytecode.InlineAttributeAlways,
		ScriptMode:                   g.scriptMode(),
		ParentScopeTDZVariables:      variablesUnderTDZ,
		ParentPrivateNameEnvironment: parentPrivateNameEnvironment,
		DerivedContextType:           newDerivedContextType,
		EvalContextType:              parser.EvalContextTypeInstanceFieldEvalContext,
	})
	if len(classElementDefinitions) > 0 {
		initializer.ClassElementDefinitions = classElementDefinitions
	}

	index := uint32(g.codeBlock.AddFunctionExpr(initializer))
	bytecode.EmitNewFuncExp(g, vr(dst), vr(g.scopeRegister), index)
	return dst
}

func (g *Generator) emitNewFunction(dst *RegisterID, function *parser.FunctionMetadataNode) *RegisterID {
	index := uint32(g.codeBlock.AddFunctionDecl(g.makeFunction(function)))
	switch mode := function.ParseMode; {
	case mode.IsGeneratorWrapper():
		bytecode.EmitNewGeneratorFunc(g, vr(dst), vr(g.scopeRegister), index)
	case mode == parser.AsyncFunctionMode:
		bytecode.EmitNewAsyncFunc(g, vr(dst), vr(g.scopeRegister), index)
	case mode.IsAsyncGeneratorWrapper():
		bytecode.EmitNewAsyncGeneratorFunc(g, vr(dst), vr(g.scopeRegister), index)
	default:
		bytecode.EmitNewFunc(g, vr(dst), vr(g.scopeRegister), index)
	}
	return dst
}

var nameIdent = ident.FromString("name")

// shouldSetFunctionName reports whether an anonymous function or class
// should get its name from the binding it is assigned to.
func (g *Generator) shouldSetFunctionName(node parser.ExpressionNode) bool {
	switch n := node.(type) {
	case parser.BaseFuncExprNode:
		return n.Metadata().EcmaName().IsNull()
	case *parser.ClassExprNode:
		return n.EcmaName().IsNull() && !n.HasStaticProperty(nameIdent)
	}
	return false
}

func (g *Generator) emitSetFunctionNameFromIdentifier(value *RegisterID, id ident.Identifier) {
	defer g.refScope()()
	name := g.keep(g.emitLoadIdentifier(g.newTemporary(), id))
	// FIXME: We should use an op_call to an internal function here instead.
	bytecode.EmitSetFunctionName(g, vr(value), vr(name))
}

func (g *Generator) emitSetFunctionName(value, name *RegisterID) {
	bytecode.EmitSetFunctionName(g, vr(value), vr(name))
}

func (g *Generator) emitAsyncIteratorOpen(iterator, next, symbolIterator *RegisterID, iterable *CallArguments, node *parser.ThrowableExpressionData) {
	defer g.refScope()()
	g.reserveCallFrame() // mirrors emitIteratorOpen

	if g.shouldEmitDebugHooks() {
		g.emitDebugHookAt(bytecode.WillExecuteExpression, node.DivotStart, nil)
	}

	g.emitExpressionInfo(node.Divot, node.DivotStart, node.DivotEnd)
	bytecode.EmitAsyncIteratorOpen(g, vr(iterator), vr(next), vr(symbolIterator), vr(iterable.thisRegister()), iterable.stackOffset(),
		g.nextValueProfileIndex(), g.nextValueProfileIndex(), g.nextValueProfileIndex())
}

func (g *Generator) emitGetGenericAsyncIterator(iterator, next, subject *RegisterID, node *parser.ThrowableExpressionData) {
	defer g.refScope()()
	g.emitExpressionInfo(node.Divot, node.DivotStart, node.DivotEnd)
	symbolAsyncIterator := g.keep(g.emitGetByID(g.newTemporary(), subject, ident.AsyncIteratorSymbol))
	args := g.newCallArguments(nil, 0)
	g.move(args.thisRegister(), subject)
	g.emitAsyncIteratorOpen(iterator, next, symbolAsyncIterator, args, node)
}

// emitAsyncIteratorNext may be called with dst aliasing value: value is
// copied into the argument register before dst is written.
func (g *Generator) emitAsyncIteratorNext(dst, next, iterator, value *RegisterID, node *parser.ThrowableExpressionData) *RegisterID {
	defer g.refScope()()
	argc := 0
	if value != nil {
		argc = 1
	}
	nextArguments := g.newCallArguments(nil, argc)
	g.move(nextArguments.thisRegister(), iterator)
	if value != nil {
		g.move(nextArguments.argumentRegister(0), value)
	}

	// The generic path calls next.call(iterator), so the callee frame header
	// below argv must be covered by numCalleeLocals.
	g.reserveCallFrame()

	g.emitExpressionInfo(node.Divot, node.DivotStart, node.DivotEnd)
	bytecode.EmitAsyncIteratorNext(g, vr(g.kill(dst)), vr(next), vr(nextArguments.thisRegister()), vr(g.generatorRegister), value != nil,
		nextArguments.stackOffset(), g.nextValueProfileIndex())
	return dst
}

func (g *Generator) emitCall(dst, fn *RegisterID, expectedFunction ExpectedFunction, callArguments *CallArguments, divot, divotStart, divotEnd parser.TextPosition, debuggableCall DebuggableCall) *RegisterID {
	return g.emitCallOp(bytecode.OpCallID, dst, fn, expectedFunction, callArguments, divot, divotStart, divotEnd, debuggableCall)
}

func (g *Generator) emitCallIgnoreResult(dst, fn *RegisterID, expectedFunction ExpectedFunction, callArguments *CallArguments, divot, divotStart, divotEnd parser.TextPosition, debuggableCall DebuggableCall) {
	g.emitCallOp(bytecode.OpCallIgnoreResultID, dst, fn, expectedFunction, callArguments, divot, divotStart, divotEnd, debuggableCall)
}

func (g *Generator) emitCallInTailPosition(dst, fn *RegisterID, expectedFunction ExpectedFunction, callArguments *CallArguments, divot, divotStart, divotEnd parser.TextPosition, debuggableCall DebuggableCall) *RegisterID {
	if g.allowTailCallOptimization {
		g.codeBlock.HasTailCalls = true
		return g.emitCallOp(bytecode.OpTailCallID, dst, fn, expectedFunction, callArguments, divot, divotStart, divotEnd, debuggableCall)
	}
	if g.allowCallIgnoreResultOptimization {
		return g.emitCallOp(bytecode.OpCallIgnoreResultID, dst, fn, expectedFunction, callArguments, divot, divotStart, divotEnd, debuggableCall)
	}
	return g.emitCallOp(bytecode.OpCallID, dst, fn, expectedFunction, callArguments, divot, divotStart, divotEnd, debuggableCall)
}

func (g *Generator) emitCallDirectEval(dst, fn *RegisterID, callArguments *CallArguments, divot, divotStart, divotEnd parser.TextPosition, debuggableCall DebuggableCall) *RegisterID {
	return g.emitCallOp(bytecode.OpCallDirectEvalID, dst, fn, NoExpectedFunction, callArguments, divot, divotStart, divotEnd, debuggableCall)
}

var (
	objectIdent       = ident.FromString("Object")
	arrayIdent        = ident.FromString("Array")
	objectPrivateName = ident.PrivateSymbol("Object")
	arrayPrivateName  = ident.PrivateSymbol("Array")
)

func (g *Generator) expectedFunctionForIdentifier(id ident.Identifier) ExpectedFunction {
	if g.shouldEmitDebugHooks() {
		return NoExpectedFunction
	}
	if id == objectIdent || id == objectPrivateName {
		return ExpectObjectConstructor
	}
	if id == arrayIdent || id == arrayPrivateName {
		return ExpectArrayConstructor
	}
	return NoExpectedFunction
}

// emitExpectedFunctionSnippet inlines Object() and Array(n) when the callee
// is the original constructor. The behavior is the same for call and
// construct.
func (g *Generator) emitExpectedFunctionSnippet(dst, fn *RegisterID, expectedFunction ExpectedFunction, callArguments *CallArguments, done *Label) ExpectedFunction {
	realCall := g.newLabel()
	switch expectedFunction {
	case ExpectObjectConstructor:
		// With arguments there is nothing interesting to do.
		if callArguments.argumentCountIncludingThis() >= 2 {
			return NoExpectedFunction
		}

		bytecode.EmitJneqPtr(g, vr(fn), vr(g.moveLinkTimeConstant(nil, bytecode.LinkTimeConstantObject)), g.bind(realCall))

		if dst != g.IgnoredResult() {
			g.emitNewObject(dst)
		}

	case ExpectArrayConstructor:
		// Only "new Array()" and "new Array(foo)" are inlined: call
		// arguments are in the opposite order of what op_new_array expects.
		if callArguments.argumentCountIncludingThis() > 2 {
			return NoExpectedFunction
		}

		bytecode.EmitJneqPtr(g, vr(fn), vr(g.moveLinkTimeConstant(nil, bytecode.LinkTimeConstantArray)), g.bind(realCall))

		if dst != g.IgnoredResult() {
			if callArguments.argumentCountIncludingThis() == 2 {
				g.emitNewArrayWithSize(dst, callArguments.argumentRegister(0))
			} else {
				bytecode.EmitNewArray(g, vr(dst), 0, 0, bytecode.ArrayWithUndecided)
			}
		}

	default:
		return NoExpectedFunction
	}

	bytecode.EmitJmp(g, g.bind(done))
	g.emitLabel(realCall)

	return expectedFunction
}

func (g *Generator) computeFeaturesForCallDirectEval() parser.LexicallyScopedFeatures {
	features := g.lexicallyScopedFeatures()
	for i := len(g.lexicalScopeStack) - 1; i >= 0; i-- {
		if g.lexicalScopeStack[i].isWithScope {
			features |= parser.TaintedByWithScopeLexicallyScopedFeature
			break
		}
	}
	return features
}

// varargsOpFor maps a call opcode to its varargs form (VarArgsOp<>).
func varargsOpFor(op bytecode.OpcodeID) bytecode.OpcodeID {
	switch op {
	case bytecode.OpCallID, bytecode.OpCallIgnoreResultID, bytecode.OpCallDirectEvalID:
		return bytecode.OpCallVarargsID
	case bytecode.OpTailCallID:
		return bytecode.OpTailCallVarargsID
	case bytecode.OpConstructID:
		return bytecode.OpConstructVarargsID
	case bytecode.OpSuperConstructID:
		return bytecode.OpSuperConstructVarargsID
	}
	panic("bytecompiler: no varargs form of " + op.String())
}

// singleSpreadArgument returns the spread expression if the arguments are
// exactly one spread, f(...x).
func singleSpreadArgument(argumentsNode *parser.ArgumentsNode) (*parser.SpreadExpressionNode, bool) {
	if argumentsNode == nil || len(argumentsNode.Args) == 0 {
		return nil, false
	}
	spread, ok := argumentsNode.Args[0].(*parser.SpreadExpressionNode)
	if !ok {
		return nil, false
	}
	if len(argumentsNode.Args) != 1 {
		panic("bytecompiler: spread argument must be the only argument")
	}
	return spread, true
}

// arrayOfSingleSpread returns s if expression is [...s].
func arrayOfSingleSpread(expression parser.ExpressionNode) (*parser.SpreadExpressionNode, bool) {
	array, ok := expression.(*parser.ArrayNode)
	if !ok || len(array.Elements) != 1 {
		return nil, false
	}
	spread, ok := array.Elements[0].Value.(*parser.SpreadExpressionNode)
	return spread, ok
}

func (g *Generator) emitCallOp(op bytecode.OpcodeID, dst, fn *RegisterID, expectedFunction ExpectedFunction, callArguments *CallArguments,
	divot, divotStart, divotEnd parser.TextPosition, debuggableCall DebuggableCall) *RegisterID {
	defer g.refScope()()

	// Generate code for arguments.
	if callArguments.argumentsNode != nil {
		if spreadArg, ok := singleSpreadArgument(callArguments.argumentsNode); ok {
			expression := spreadArg.Expression
			if spread, ok := arrayOfSingleSpread(expression); ok {
				// f(...[...x]) spreads x directly.
				argumentRegister := g.keep(g.tempDestination(g.emitNode(callArguments.argumentRegister(0), spread.Expression)))
				g.emitExpressionInfo(spread.Divot, spread.DivotStart, spread.DivotEnd)
				bytecode.EmitSpread(g, vr(argumentRegister), vr(argumentRegister))
				return g.emitCallVarargsOp(varargsOpFor(op), dst, fn, callArguments.thisRegister(), argumentRegister, g.newTemporary(), 0, divot, divotStart, divotEnd, debuggableCall)
			}
			argumentRegister := g.keep(g.emitExpr(expression, callArguments.argumentRegister(0)))
			return g.emitCallVarargsOp(varargsOpFor(op), dst, fn, callArguments.thisRegister(), argumentRegister, g.newTemporary(), 0, divot, divotStart, divotEnd, debuggableCall)
		}
		for i, n := range callArguments.argumentsNode.Args {
			g.emitNode(callArguments.argumentRegister(i), n)
		}
	}

	g.reserveCallFrame()

	if g.shouldEmitDebugHooks() && debuggableCall == DebuggableCallYes {
		g.emitDebugHookAt(bytecode.WillExecuteExpression, divotStart, nil)
	}

	g.emitExpressionInfo(divot, divotStart, divotEnd)

	done := g.newLabel()
	expectedFunction = g.emitExpectedFunctionSnippet(dst, fn, expectedFunction, callArguments, done)

	if op == bytecode.OpTailCallID {
		g.emitLogShadowChickenTailIfNecessary()
	}

	argc := uint32(callArguments.argumentCountIncludingThis())
	argv := callArguments.stackOffset()
	switch op {
	case bytecode.OpCallDirectEvalID:
		bytecode.EmitCallDirectEval(g, vr(dst), vr(fn), argc, argv, vr(&g.thisRegister), vr(g.scopeRegister),
			uint32(g.computeFeaturesForCallDirectEval()), g.nextValueProfileIndex())
	case bytecode.OpCallIgnoreResultID:
		bytecode.EmitCallIgnoreResult(g, vr(fn), argc, argv)
		if g.shouldEmitTypeProfilerHooks() {
			g.emitLoad(dst, bytecode.Undefined{})
		}
	case bytecode.OpTailCallID:
		bytecode.EmitTailCall(g, vr(dst), vr(fn), argc, argv)
	default:
		bytecode.EmitCall(g, vr(dst), vr(fn), argc, argv, g.nextValueProfileIndex())
	}

	if expectedFunction != NoExpectedFunction {
		g.emitLabel(done)
	}
	return dst
}

func (g *Generator) emitCallVarargs(dst, fn, thisRegister, arguments, firstFreeRegister *RegisterID, firstVarArgOffset int32, divot, divotStart, divotEnd parser.TextPosition, debuggableCall DebuggableCall) *RegisterID {
	return g.emitCallVarargsOp(bytecode.OpCallVarargsID, dst, fn, thisRegister, arguments, firstFreeRegister, firstVarArgOffset, divot, divotStart, divotEnd, debuggableCall)
}

func (g *Generator) emitCallVarargsInTailPosition(dst, fn, thisRegister, arguments, firstFreeRegister *RegisterID, firstVarArgOffset int32, divot, divotStart, divotEnd parser.TextPosition, debuggableCall DebuggableCall) *RegisterID {
	op := bytecode.OpCallVarargsID
	if g.allowTailCallOptimization {
		op = bytecode.OpTailCallVarargsID
	}
	return g.emitCallVarargsOp(op, dst, fn, thisRegister, arguments, firstFreeRegister, firstVarArgOffset, divot, divotStart, divotEnd, debuggableCall)
}

func (g *Generator) emitConstructVarargs(dst, fn, thisRegister, arguments, firstFreeRegister *RegisterID, firstVarArgOffset int32, divot, divotStart, divotEnd parser.TextPosition, debuggableCall DebuggableCall) *RegisterID {
	return g.emitCallVarargsOp(bytecode.OpConstructVarargsID, dst, fn, thisRegister, arguments, firstFreeRegister, firstVarArgOffset, divot, divotStart, divotEnd, debuggableCall)
}

func (g *Generator) emitSuperConstructVarargs(dst, fn, thisRegister, arguments, firstFreeRegister *RegisterID, firstVarArgOffset int32, divot, divotStart, divotEnd parser.TextPosition, debuggableCall DebuggableCall) *RegisterID {
	return g.emitCallVarargsOp(bytecode.OpSuperConstructVarargsID, dst, fn, thisRegister, arguments, firstFreeRegister, firstVarArgOffset, divot, divotStart, divotEnd, debuggableCall)
}

func (g *Generator) emitCallVarargsOp(op bytecode.OpcodeID, dst, fn, thisRegister, arguments, firstFreeRegister *RegisterID, firstVarArgOffset int32,
	divot, divotStart, divotEnd parser.TextPosition, debuggableCall DebuggableCall) *RegisterID {
	if g.shouldEmitDebugHooks() && debuggableCall == DebuggableCallYes {
		g.emitDebugHookAt(bytecode.WillExecuteExpression, divotStart, nil)
	}

	g.emitExpressionInfo(divot, divotStart, divotEnd)

	if op == bytecode.OpTailCallVarargsID {
		g.emitLogShadowChickenTailIfNecessary()
	}

	var args bytecode.VirtualRegister
	if arguments != nil {
		args = arguments.VirtualRegister()
	}
	switch op {
	case bytecode.OpTailCallVarargsID:
		bytecode.EmitTailCallVarargs(g, vr(dst), vr(fn), vr(thisRegister), args, vr(firstFreeRegister), firstVarArgOffset)
	case bytecode.OpCallVarargsID:
		bytecode.EmitCallVarargs(g, vr(dst), vr(fn), vr(thisRegister), args, vr(firstFreeRegister), firstVarArgOffset, g.nextValueProfileIndex())
	case bytecode.OpConstructVarargsID:
		bytecode.EmitConstructVarargs(g, vr(dst), vr(fn), vr(thisRegister), args, vr(firstFreeRegister), firstVarArgOffset, g.nextValueProfileIndex())
	case bytecode.OpSuperConstructVarargsID:
		bytecode.EmitSuperConstructVarargs(g, vr(dst), vr(fn), vr(thisRegister), args, vr(firstFreeRegister), firstVarArgOffset, g.nextValueProfileIndex())
	default:
		panic("bytecompiler: not a varargs call " + op.String())
	}
	return dst
}

// alwaysUseShadowChicken is Options::alwaysUseShadowChicken().
const alwaysUseShadowChicken = false

func (g *Generator) emitLogShadowChickenPrologueIfNecessary() {
	if !g.shouldEmitDebugHooks() && !alwaysUseShadowChicken {
		return
	}
	bytecode.EmitLogShadowChickenPrologue(g, vr(g.scopeRegister))
}

func (g *Generator) emitLogShadowChickenTailIfNecessary() {
	if !g.shouldEmitDebugHooks() && !alwaysUseShadowChicken {
		return
	}
	bytecode.EmitLogShadowChickenTail(g, vr(&g.thisRegister), vr(g.scopeRegister))
}

func (g *Generator) emitCallDefineProperty(newObj, propertyNameRegister, valueRegister, getterRegister, setterRegister *RegisterID, options int, position parser.TextPosition) {
	defer g.refScope()()
	attributes := bytecode.NewDefinePropertyAttributes()
	if options&PropertyConfigurable != 0 {
		attributes.SetConfigurable(true)
	}
	if options&PropertyWritable != 0 {
		attributes.SetWritable(true)
	} else if valueRegister != nil {
		attributes.SetWritable(false)
	}
	if options&PropertyEnumerable != 0 {
		attributes.SetEnumerable(true)
	}
	if valueRegister != nil {
		attributes.SetValue()
	}
	if getterRegister != nil {
		attributes.SetGet()
	}
	if setterRegister != nil {
		attributes.SetSet()
	}

	g.emitExpressionInfo(position, position, position)

	attributesRegister := func() *RegisterID {
		return g.emitLoad(nil, bytecode.JSNumber(float64(attributes)))
	}
	if attributes.HasGet() || attributes.HasSet() {
		var throwTypeErrorFunction *RegisterID
		if !attributes.HasGet() || !attributes.HasSet() {
			throwTypeErrorFunction = g.keep(g.moveLinkTimeConstant(nil, bytecode.LinkTimeConstantThrowTypeErrorFunction))
		}
		getter, setter := getterRegister, setterRegister
		if !attributes.HasGet() {
			getter = throwTypeErrorFunction
		}
		if !attributes.HasSet() {
			setter = throwTypeErrorFunction
		}
		bytecode.EmitDefineAccessorProperty(g, vr(newObj), vr(propertyNameRegister), vr(getter), vr(setter), vr(attributesRegister()))
	} else {
		bytecode.EmitDefineDataProperty(g, vr(newObj), vr(propertyNameRegister), vr(valueRegister), vr(attributesRegister()))
	}
}

func (g *Generator) emitReturn(src *RegisterID) *RegisterID {
	// Normal functions and naked constructors handle `return` plainly.
	if g.isConstructor() && g.constructorKind() != parser.ConstructorKindNaked {
		isDerived := g.constructorKind() == parser.ConstructorKindExtends
		srcIsThis := src.Index() == g.thisRegister.Index()

		if !srcIsThis {
			defer g.refScope()()
			isObjectLabel := g.newLabel()
			g.emitJumpIfTrue(g.emitIsObject(g.newTemporary(), src), isObjectLabel)

			if isDerived {
				isUndefinedLabel := g.newLabel()
				g.emitJumpIfTrue(g.emitIsUndefined(g.newTemporary(), src), isUndefinedLabel)

				className := g.scopeNode.(*parser.FunctionNode).Ident.String()
				if className == "" {
					g.emitThrowTypeError("Cannot return a non-object type in the constructor of a derived class.")
				} else {
					g.emitThrowTypeError("Cannot return a non-object type in the constructor of a derived class " + className + ".")
				}

				g.emitLabel(isUndefinedLabel)
			}

			bytecode.EmitRet(g, vr(g.ensureThis()))
			g.emitLabel(isObjectLabel)
		}
	}

	bytecode.EmitRet(g, vr(src))
	return src
}

func (g *Generator) emitConstructImpl(op bytecode.OpcodeID, dst, fn, lazyThis *RegisterID, expectedFunction ExpectedFunction, callArguments *CallArguments,
	divot, divotStart, divotEnd parser.TextPosition, isDefaultDerivedConstructorCall bool) *RegisterID {
	defer g.refScope()()

	// Generate code for arguments.
	if argumentsNode := callArguments.argumentsNode; argumentsNode != nil {
		if spreadArg, ok := singleSpreadArgument(argumentsNode); ok {
			expression := spreadArg.Expression
			if spread, ok := arrayOfSingleSpread(expression); ok {
				argumentRegister := g.keep(g.tempDestination(g.emitNode(callArguments.argumentRegister(0), spread.Expression)))

				if !isDefaultDerivedConstructorCall {
					g.emitExpressionInfo(spread.Divot, spread.DivotStart, spread.DivotEnd)
					bytecode.EmitSpread(g, vr(argumentRegister), vr(argumentRegister))
				}

				g.move(callArguments.thisRegister(), lazyThis)
				return g.emitCallVarargsOp(varargsOpFor(op), dst, fn, callArguments.thisRegister(), argumentRegister, g.newTemporary(), 0, divot, divotStart, divotEnd, DebuggableCallNo)
			}
			argumentRegister := g.keep(g.emitExpr(expression, callArguments.argumentRegister(0)))
			g.move(callArguments.thisRegister(), lazyThis)
			return g.emitCallVarargsOp(varargsOpFor(op), dst, fn, callArguments.thisRegister(), argumentRegister, g.newTemporary(), 0, divot, divotStart, divotEnd, DebuggableCallNo)
		}

		for i, n := range argumentsNode.Args {
			g.emitNode(callArguments.argumentRegister(i), n)
		}
	}

	g.move(callArguments.thisRegister(), lazyThis)

	g.reserveCallFrame()

	g.emitExpressionInfo(divot, divotStart, divotEnd)

	done := g.newLabel()
	expectedFunction = g.emitExpectedFunctionSnippet(dst, fn, expectedFunction, callArguments, done)

	argc := uint32(callArguments.argumentCountIncludingThis())
	if op == bytecode.OpSuperConstructID {
		bytecode.EmitSuperConstruct(g, vr(dst), vr(fn), argc, callArguments.stackOffset(), g.nextValueProfileIndex())
	} else {
		bytecode.EmitConstruct(g, vr(dst), vr(fn), argc, callArguments.stackOffset(), g.nextValueProfileIndex())
	}

	if expectedFunction != NoExpectedFunction {
		g.emitLabel(done)
	}
	return dst
}

func (g *Generator) emitConstruct(dst, fn, lazyThis *RegisterID, expectedFunction ExpectedFunction, callArguments *CallArguments, divot, divotStart, divotEnd parser.TextPosition) *RegisterID {
	return g.emitConstructImpl(bytecode.OpConstructID, dst, fn, lazyThis, expectedFunction, callArguments, divot, divotStart, divotEnd, false)
}

func (g *Generator) emitSuperConstruct(dst, fn, lazyThis *RegisterID, expectedFunction ExpectedFunction, callArguments *CallArguments, divot, divotStart, divotEnd parser.TextPosition, isDefaultDerivedConstructorCall bool) *RegisterID {
	return g.emitConstructImpl(bytecode.OpSuperConstructID, dst, fn, lazyThis, expectedFunction, callArguments, divot, divotStart, divotEnd, isDefaultDerivedConstructorCall)
}

func (g *Generator) emitStrcat(dst, src *RegisterID, count int) *RegisterID {
	bytecode.EmitStrcat(g, vr(dst), vr(src), int32(count))
	return dst
}

func (g *Generator) emitToPrimitive(dst, src *RegisterID) {
	bytecode.EmitToPrimitive(g, vr(dst), vr(src))
}

func (g *Generator) emitToPropertyKey(dst, src *RegisterID) *RegisterID {
	bytecode.EmitToPropertyKey(g, vr(dst), vr(src))
	return dst
}

func (g *Generator) emitToPropertyKeyOrNumber(dst, src *RegisterID) *RegisterID {
	bytecode.EmitToPropertyKeyOrNumber(g, vr(dst), vr(src))
	return dst
}

func (g *Generator) emitPushWithScope(objectScope *RegisterID) *RegisterID {
	g.pushLocalControlFlowScope()
	newScope := g.newBlockScopeVariable()
	newScope.ref()

	bytecode.EmitPushWithScope(g, vr(newScope), vr(g.scopeRegister), vr(objectScope))

	g.move(g.scopeRegister, newScope)
	g.lexicalScopeStack = append(g.lexicalScopeStack, lexicalScopeStackEntry{nil, newScope, true, 0})
	return newScope
}

func (g *Generator) emitGetParentScope(dst, scope *RegisterID) *RegisterID {
	bytecode.EmitGetParentScope(g, vr(dst), vr(scope))
	return dst
}

func (g *Generator) emitPopWithScope() {
	g.emitGetParentScope(g.scopeRegister, g.scopeRegister)
	g.popLocalControlFlowScope()
	stackEntry := g.lexicalScopeStack[len(g.lexicalScopeStack)-1]
	g.lexicalScopeStack = g.lexicalScopeStack[:len(g.lexicalScopeStack)-1]
	stackEntry.scope.deref()
	if !stackEntry.isWithScope {
		panic("bytecompiler: popped scope is not a with scope")
	}
}

// emitDebugHookAt is emitDebugHook(DebugHookType, const JSTextPosition&, RegisterID*).
func (g *Generator) emitDebugHookAt(debugHookType bytecode.DebugHookType, divot parser.TextPosition, data *RegisterID) {
	if !g.shouldEmitDebugHooks() {
		return
	}

	if g.lastDebugHook.position == divot && g.lastDebugHook.typ == debugHookType {
		return
	}

	g.lastDebugHook.position = divot
	g.lastDebugHook.typ = debugHookType

	g.emitExpressionInfo(divot, divot, divot)

	if data == nil {
		data = g.emitLoad(nil, bytecode.Undefined{})
	}
	bytecode.EmitDebug(g, debugHookType, vr(data))
}

// emitDebugHook emits a WillExecuteStatement hook for a node.
func (g *Generator) emitDebugHook(node parser.Node, data *RegisterID) {
	// DebuggerStatementNode emits its own special debug hook.
	if _, ok := node.(*parser.DebuggerStatementNode); ok {
		return
	}
	g.emitDebugHookAt(bytecode.WillExecuteStatement, node.BaseNode().Position(), data)
}

func (g *Generator) emitWillLeaveCallFrameDebugHook() {
	g.emitDebugHookAt(bytecode.WillLeaveCallFrame, g.scope.Position(), nil)
}
