package bytecompiler

import (
	"github.com/baslentfert/jsc2go/bytecode"
	"github.com/baslentfert/jsc2go/ident"
	"github.com/baslentfert/jsc2go/parser"
)

// The node emission entry points and the small emit helpers that
// BytecodeGenerator.h defines inline.

// emitStatement emits a statement outside tail position.
func (g *Generator) emitStatement(dst *RegisterID, n parser.StatementNode) {
	g.withPoison(false, false, func() { g.emitStatementInTailPosition(dst, n) })
}

func (g *Generator) emitStatementInIgnoreResultPosition(n parser.StatementNode) {
	g.withPoison(false, g.defaultAllowCallIgnoreResultOptimization, func() {
		g.emitStatementInTailPosition(g.IgnoredResult(), n)
	})
}

// emitStatementInTailPosition emits n; dst, if given, must be a local or a
// referenced temporary.
func (g *Generator) emitStatementInTailPosition(dst *RegisterID, n parser.StatementNode) {
	if !g.enterRecursion() {
		g.emitThrowExpressionTooDeepException()
		return
	}
	defer g.leaveRecursion()
	if n.BaseNode().NeedsDebugHook() {
		g.emitDebugHook(n, nil)
	}
	g.emitStmt(n, dst)
}

// emitNode emits an expression outside tail position and returns the
// register holding its value.
func (g *Generator) emitNode(dst *RegisterID, n parser.ExpressionNode) (result *RegisterID) {
	g.withPoison(false, false, func() { result = g.emitNodeInTailPosition(dst, n) })
	return
}

func (g *Generator) emitNodeInTailPositionFromReturnNode(dst *RegisterID, n parser.ExpressionNode) (result *RegisterID) {
	g.withPoison(g.allowTailCallOptimization, false, func() { result = g.emitNodeInTailPosition(dst, n) })
	return
}

func (g *Generator) emitNodeInTailPositionFromExprStatementNode(dst *RegisterID, n parser.ExpressionNode) (result *RegisterID) {
	g.withPoison(false, g.allowCallIgnoreResultOptimization, func() { result = g.emitNodeInTailPosition(dst, n) })
	return
}

func (g *Generator) emitNodeInIgnoreResultPosition(n parser.ExpressionNode) (result *RegisterID) {
	g.withPoison(false, g.defaultAllowCallIgnoreResultOptimization, func() {
		result = g.emitNodeInTailPosition(g.IgnoredResult(), n)
	})
	return
}

func (g *Generator) emitNodeInTailPosition(dst *RegisterID, n parser.ExpressionNode) *RegisterID {
	if !g.enterRecursion() {
		return g.emitThrowExpressionTooDeepException()
	}
	defer g.leaveRecursion()
	if n.BaseNode().NeedsDebugHook() {
		g.emitDebugHook(n, nil)
	}
	return g.emitExpr(n, dst)
}

func (g *Generator) enterRecursion() bool {
	g.recursionDepth++
	if g.recursionDepth > maxRecursionDepth {
		g.recursionDepth--
		return false
	}
	return true
}

func (g *Generator) leaveRecursion() { g.recursionDepth-- }

func (g *Generator) emitDefineClassElements(n *parser.PropertyListNode, constructor, prototype *RegisterID,
	instanceElementDefinitions, staticElementDefinitions *[]parser.ClassElementDefinition) *RegisterID {
	if !g.enterRecursion() {
		return g.emitThrowExpressionTooDeepException()
	}
	defer g.leaveRecursion()
	if n.BaseNode().NeedsDebugHook() {
		g.emitDebugHook(n, nil)
	}
	return g.emitPropertyListBytecode(n, constructor, prototype, instanceElementDefinitions, staticElementDefinitions)
}

// emitNodeForProperty emits a property key; string keys that are array
// indices become numbers.
func (g *Generator) emitNodeForProperty(dst *RegisterID, node parser.ExpressionNode) *RegisterID {
	if s, ok := node.(*parser.StringNode); ok {
		if index, ok := s.Value.ParseIndex(); ok {
			return g.emitLoad(dst, bytecode.JSNumber(float64(index)))
		}
	}
	return g.emitNode(dst, node)
}

func (g *Generator) emitNodeInConditionContext(n parser.ExpressionNode, trueTarget, falseTarget *Label, fallThroughMode FallThroughMode) {
	if !g.enterRecursion() {
		g.emitThrowExpressionTooDeepException()
		return
	}
	defer g.leaveRecursion()
	g.emitBytecodeInConditionContext(n, trueTarget, falseTarget, fallThroughMode)
}

// FallThroughMode says which target code following a condition falls into.
type FallThroughMode uint8

const (
	FallThroughMeansTrue FallThroughMode = iota
	FallThroughMeansFalse
)

func invertFallThroughMode(m FallThroughMode) FallThroughMode {
	if m == FallThroughMeansTrue {
		return FallThroughMeansFalse
	}
	return FallThroughMeansTrue
}

// emitExpressionInfo records the source range of the next instruction for
// error messages.
func (g *Generator) emitExpressionInfo(divot, divotStart, divotEnd parser.TextPosition) {
	// Don't record data that could cause trouble later; we'd just report the
	// wrong position.
	if !divot.IsSet() || !divotStart.IsSet() || !divotEnd.IsSet() {
		return
	}
	if g.isBuiltinFunction {
		return
	}
	sourceOffset := g.sourceCode.StartOffset
	divotOffset := int(divot) - sourceOffset
	startOffset := int(divot) - int(divotStart)
	endOffset := int(divotEnd) - int(divot)
	g.codeBlock.AddExpressionInfo(uint32(len(g.instructions)), uint32(divotOffset), uint32(startOffset), uint32(endOffset))
}

func (g *Generator) leftHandSideNeedsCopy(rightHasAssignments, rightIsPure bool) bool {
	return (g.codeType != bytecode.FunctionCode || rightHasAssignments) && !rightIsPure
}

// emitNodeForLeftHandSide returns a referenced register (C++ RefPtr) held
// by the caller's refScope.
func (g *Generator) emitNodeForLeftHandSide(n parser.ExpressionNode, rightHasAssignments, rightIsPure bool) *RegisterID {
	if g.leftHandSideNeedsCopy(rightHasAssignments, rightIsPure) {
		dst := g.keep(g.newTemporary())
		g.emitNode(dst, n)
		return dst
	}
	return g.keep(g.emitNode(nil, n))
}

func (g *Generator) emitNodeForLeftHandSideForProperty(n parser.ExpressionNode, rightHasAssignments, rightIsPure bool) *RegisterID {
	if g.leftHandSideNeedsCopy(rightHasAssignments, rightIsPure) {
		dst := g.keep(g.newTemporary())
		g.emitNodeForProperty(dst, n)
		return dst
	}
	return g.keep(g.emitNodeForProperty(nil, n))
}

func (g *Generator) emitDirectSetPrototypeOf(mode InvalidPrototypeMode, base, prototype *RegisterID, divot, divotStart, divotEnd parser.TextPosition) *RegisterID {
	defer g.refScope()()
	c := bytecode.LinkTimeConstantSetPrototypeDirect
	if mode == InvalidPrototypeThrow {
		c = bytecode.LinkTimeConstantSetPrototypeDirectOrThrow
	}
	setPrototypeDirect := g.keep(g.moveLinkTimeConstant(nil, c))
	args := g.newCallArguments(nil, 1)
	g.move(args.thisRegister(), base)
	g.move(args.argumentRegister(0), prototype)
	g.emitCallIgnoreResult(g.newTemporary(), setPrototypeDirect, NoExpectedFunction, args, divot, divotStart, divotEnd, DebuggableCallNo)
	return base
}

// shouldGetArgumentsDotLengthFast reports whether arguments.length can be
// read without materializing the arguments object.
func (g *Generator) shouldGetArgumentsDotLengthFast(node parser.ExpressionNode) bool {
	return g.isFunctionNode() &&
		!g.needsArguments &&
		!g.hasShadowsArgumentsCodeFeature() &&
		parser.IsArgumentsLengthAccess(node) &&
		!g.parseMode().IsArrowFunction() &&
		!g.parseMode().IsGeneratorOrAsyncFunctionBody()
}

// --- Unary and binary operators ----------------------------------------------

type unaryEmitter func(bytecode.Emitter, bytecode.VirtualRegister, bytecode.VirtualRegister)

// emitUnaryOpT is the template emitUnaryOp<UnaryOp>(dst, src).
func (g *Generator) emitUnaryOpT(emit unaryEmitter, dst, src *RegisterID) *RegisterID {
	emit(g, vr(dst), vr(src))
	return dst
}

func (g *Generator) emitUnsigned(dst, src *RegisterID) *RegisterID {
	bytecode.EmitUnsigned(g, vr(dst), vr(src), g.addUnaryArithProfile())
	return dst
}

func (g *Generator) emitUnaryOp(op bytecode.OpcodeID, dst, src *RegisterID, t parser.ResultType) *RegisterID {
	switch op {
	case bytecode.OpNotID:
		bytecode.EmitNot(g, vr(dst), vr(src))
	case bytecode.OpNegateID:
		bytecode.EmitNegate(g, vr(dst), vr(src), g.addUnaryArithProfile(), bytecode.ResultTypeOf(t))
	case bytecode.OpBitnotID:
		bytecode.EmitBitnot(g, vr(dst), vr(src), g.addUnaryArithProfile())
	case bytecode.OpToNumberID:
		bytecode.EmitToNumber(g, vr(dst), vr(src), g.addUnaryArithProfile())
	case bytecode.OpToNumericID:
		bytecode.EmitToNumeric(g, vr(dst), vr(src), g.addUnaryArithProfile())
	default:
		panic("bytecompiler: not a unary operator " + op.String())
	}
	return dst
}

// emitBinaryOpT is the template emitBinaryOp<BinaryOp>: no peephole tricks.
func (g *Generator) emitBinaryOpT(op bytecode.OpcodeID, dst, src1, src2 *RegisterID, types bytecode.OperandTypes) *RegisterID {
	d, a, b := vr(dst), vr(src1), vr(src2)
	switch op {
	case bytecode.OpAddID:
		bytecode.EmitAdd(g, d, a, b, g.addBinaryArithProfile(), types)
	case bytecode.OpMulID:
		bytecode.EmitMul(g, d, a, b, g.addBinaryArithProfile(), types)
	case bytecode.OpSubID:
		bytecode.EmitSub(g, d, a, b, g.addBinaryArithProfile(), types)
	case bytecode.OpDivID:
		bytecode.EmitDiv(g, d, a, b, g.addBinaryArithProfile(), types)
	case bytecode.OpBitandID:
		bytecode.EmitBitand(g, d, a, b, g.addBinaryArithProfile(), types)
	case bytecode.OpBitorID:
		bytecode.EmitBitor(g, d, a, b, g.addBinaryArithProfile(), types)
	case bytecode.OpBitxorID:
		bytecode.EmitBitxor(g, d, a, b, g.addBinaryArithProfile(), types)
	case bytecode.OpLshiftID:
		bytecode.EmitLshift(g, d, a, b, g.addBinaryArithProfile())
	case bytecode.OpRshiftID:
		bytecode.EmitRshift(g, d, a, b, g.addBinaryArithProfile())
	case bytecode.OpEqID:
		bytecode.EmitEq(g, d, a, b)
	case bytecode.OpNeqID:
		bytecode.EmitNeq(g, d, a, b)
	case bytecode.OpStricteqID:
		bytecode.EmitStricteq(g, d, a, b)
	case bytecode.OpNstricteqID:
		bytecode.EmitNstricteq(g, d, a, b)
	case bytecode.OpLessID:
		bytecode.EmitLess(g, d, a, b)
	case bytecode.OpLesseqID:
		bytecode.EmitLesseq(g, d, a, b)
	case bytecode.OpGreaterID:
		bytecode.EmitGreater(g, d, a, b)
	case bytecode.OpGreatereqID:
		bytecode.EmitGreatereq(g, d, a, b)
	case bytecode.OpBelowID:
		bytecode.EmitBelow(g, d, a, b)
	case bytecode.OpBeloweqID:
		bytecode.EmitBeloweq(g, d, a, b)
	case bytecode.OpModID:
		bytecode.EmitMod(g, d, a, b)
	case bytecode.OpPowID:
		bytecode.EmitPow(g, d, a, b)
	case bytecode.OpUrshiftID:
		bytecode.EmitUrshift(g, d, a, b)
	default:
		panic("bytecompiler: not a binary operator " + op.String())
	}
	return dst
}

// emitBinaryOp emits a binary operator, using the typeof and empty string
// peephole optimizations where they apply.
func (g *Generator) emitBinaryOp(op bytecode.OpcodeID, dst, src1, src2 *RegisterID, types bytecode.OperandTypes) *RegisterID {
	switch op {
	case bytecode.OpLessID:
		// `typeof x < "u"` is `typeof x !== "undefined"`.
		if g.tryEmitTypeofIsUndefinedForStringComparison(true, dst, src1, src2) {
			return dst
		}
	case bytecode.OpGreaterID:
		// `typeof x > "u"` is `typeof x === "undefined"`.
		if g.tryEmitTypeofIsUndefinedForStringComparison(false, dst, src1, src2) {
			return dst
		}
	case bytecode.OpAddID:
		isConstantEmptyString := func(src *RegisterID) bool {
			s, ok := g.constantString(src)
			return ok && s == ""
		}
		if isConstantEmptyString(src1) {
			g.emitToPrimitive(dst, src2)
			return g.emitToString(dst, dst)
		}
		if isConstantEmptyString(src2) {
			g.emitToPrimitive(dst, src1)
			return g.emitToString(dst, dst)
		}
	}
	return g.emitBinaryOpT(op, dst, src1, src2, types)
}

// lastTypeofOperands returns the operands of the last instruction if it is
// op_typeof.
func (g *Generator) lastTypeofOperands() (dst, value bytecode.VirtualRegister, ok bool) {
	if !g.canDoPeepholeOptimization() || g.lastOpcodeID != bytecode.OpTypeofID {
		return 0, 0, false
	}
	return g.lastOperand(bytecode.OpTypeofDstIndex), g.lastOperand(bytecode.OpTypeofValueIndex), true
}

func (g *Generator) tryEmitTypeofIsUndefinedForStringComparison(isNotTypeofUndefined bool, dst, src1, src2 *RegisterID) bool {
	typeofDst, value, ok := g.lastTypeofOperands()
	if !ok || src1.VirtualRegister() != typeofDst || !src1.IsTemporary() {
		return false
	}
	if s, ok := g.constantString(src2); !ok || s != "u" {
		return false
	}

	g.rewind()
	if isNotTypeofUndefined {
		defer g.refScope()()
		temp := g.keep(g.newTemporary())
		bytecode.EmitTypeofIsUndefined(g, vr(temp), value)
		g.emitUnaryOpT(bytecode.EmitNot, dst, temp)
	} else {
		bytecode.EmitTypeofIsUndefined(g, vr(dst), value)
	}
	return true
}

// emitEqualityOp emits op_eq or op_stricteq, turning `typeof x == "..."`
// into a type test.
func (g *Generator) emitEqualityOp(op bytecode.OpcodeID, dst, src1, src2 *RegisterID) *RegisterID {
	if !g.emitEqualityOpImpl(dst, src1, src2) {
		if op == bytecode.OpEqID {
			bytecode.EmitEq(g, vr(dst), vr(src1), vr(src2))
		} else {
			bytecode.EmitStricteq(g, vr(dst), vr(src1), vr(src2))
		}
	}
	return dst
}

func (g *Generator) emitEqualityOpImpl(dst, src1, src2 *RegisterID) bool {
	typeofDst, value, ok := g.lastTypeofOperands()
	if !ok || src1.VirtualRegister() != typeofDst || !src1.IsTemporary() {
		return false
	}
	s, ok := g.constantString(src2)
	if !ok {
		return false
	}
	d := vr(dst)
	switch s {
	case "undefined":
		g.rewind()
		bytecode.EmitTypeofIsUndefined(g, d, value)
	case "boolean":
		g.rewind()
		bytecode.EmitIsBoolean(g, d, value)
	case "number":
		g.rewind()
		bytecode.EmitIsNumber(g, d, value)
	case "string":
		g.rewind()
		bytecode.EmitIsCellWithType(g, d, value, bytecode.StringType)
	case "symbol":
		g.rewind()
		bytecode.EmitIsCellWithType(g, d, value, bytecode.SymbolType)
	case "bigint":
		g.rewind()
		bytecode.EmitIsCellWithType(g, d, value, bytecode.HeapBigIntType)
	case "object":
		g.rewind()
		bytecode.EmitTypeofIsObject(g, d, value)
	case "function":
		g.rewind()
		bytecode.EmitTypeofIsFunction(g, d, value)
	default:
		return false
	}
	return true
}

func (g *Generator) emitToObject(dst, src *RegisterID, message ident.Identifier) *RegisterID {
	bytecode.EmitToObject(g, vr(dst), vr(src), g.addConstant(message), g.nextValueProfileIndex())
	return dst
}

func (g *Generator) emitToNumber(dst, src *RegisterID) *RegisterID {
	bytecode.EmitToNumber(g, vr(dst), vr(src), g.addUnaryArithProfile())
	return dst
}

func (g *Generator) emitToNumeric(dst, src *RegisterID) *RegisterID {
	bytecode.EmitToNumeric(g, vr(dst), vr(src), g.addUnaryArithProfile())
	return dst
}

func (g *Generator) emitToString(dst, src *RegisterID) *RegisterID {
	return g.emitUnaryOpT(bytecode.EmitToString, dst, src)
}

func (g *Generator) emitTypeOf(dst, src *RegisterID) *RegisterID {
	return g.emitUnaryOpT(bytecode.EmitTypeof, dst, src)
}

func (g *Generator) emitInc(srcDst *RegisterID) *RegisterID {
	bytecode.EmitInc(g, vr(srcDst), g.addUnaryArithProfile())
	return srcDst
}

func (g *Generator) emitDec(srcDst *RegisterID) *RegisterID {
	bytecode.EmitDec(g, vr(srcDst), g.addUnaryArithProfile())
	return srcDst
}

// --- Type tests ----------------------------------------------------------------

func (g *Generator) emitIsCellWithType(dst, src *RegisterID, t bytecode.JSType) *RegisterID {
	bytecode.EmitIsCellWithType(g, vr(dst), vr(src), t)
	return dst
}

func (g *Generator) emitIsGenerator(dst, src *RegisterID) *RegisterID {
	return g.emitIsCellWithType(dst, src, bytecode.JSGeneratorType)
}
func (g *Generator) emitIsIteratorHelper(dst, src *RegisterID) *RegisterID {
	return g.emitIsCellWithType(dst, src, bytecode.JSIteratorHelperType)
}
func (g *Generator) emitIsJSArray(dst, src *RegisterID) *RegisterID {
	return g.emitIsCellWithType(dst, src, bytecode.ArrayType)
}
func (g *Generator) emitIsPromise(dst, src *RegisterID) *RegisterID {
	return g.emitIsCellWithType(dst, src, bytecode.JSPromiseType)
}
func (g *Generator) emitIsProxyObject(dst, src *RegisterID) *RegisterID {
	return g.emitIsCellWithType(dst, src, bytecode.ProxyObjectType)
}
func (g *Generator) emitIsRegExpObject(dst, src *RegisterID) *RegisterID {
	return g.emitIsCellWithType(dst, src, bytecode.RegExpObjectType)
}
func (g *Generator) emitIsMap(dst, src *RegisterID) *RegisterID {
	return g.emitIsCellWithType(dst, src, bytecode.JSMapType)
}
func (g *Generator) emitIsSet(dst, src *RegisterID) *RegisterID {
	return g.emitIsCellWithType(dst, src, bytecode.JSSetType)
}
func (g *Generator) emitIsShadowRealm(dst, src *RegisterID) *RegisterID {
	return g.emitIsCellWithType(dst, src, bytecode.ShadowRealmType)
}
func (g *Generator) emitIsArrayIterator(dst, src *RegisterID) *RegisterID {
	return g.emitIsCellWithType(dst, src, bytecode.JSArrayIteratorType)
}
func (g *Generator) emitIsWrapForValidIterator(dst, src *RegisterID) *RegisterID {
	return g.emitIsCellWithType(dst, src, bytecode.JSWrapForValidIteratorType)
}
func (g *Generator) emitIsRegExpStringIterator(dst, src *RegisterID) *RegisterID {
	return g.emitIsCellWithType(dst, src, bytecode.JSRegExpStringIteratorType)
}
func (g *Generator) emitIsDerivedArray(dst, src *RegisterID) *RegisterID {
	return g.emitIsCellWithType(dst, src, bytecode.DerivedArrayType)
}
func (g *Generator) emitIsDisposableStack(dst, src *RegisterID) *RegisterID {
	return g.emitIsCellWithType(dst, src, bytecode.DisposableStackType)
}
func (g *Generator) emitIsAsyncDisposableStack(dst, src *RegisterID) *RegisterID {
	return g.emitIsCellWithType(dst, src, bytecode.AsyncDisposableStackType)
}

func (g *Generator) emitIsNull(dst, src *RegisterID) *RegisterID {
	return g.emitEqualityOp(bytecode.OpStricteqID, dst, src, g.emitLoad(nil, bytecode.Null{}))
}

func (g *Generator) emitIsUndefined(dst, src *RegisterID) *RegisterID {
	return g.emitEqualityOp(bytecode.OpStricteqID, dst, src, g.emitLoad(nil, bytecode.Undefined{}))
}

// --- Type and control flow profiler hooks ---------------------------------------

func (g *Generator) emitTypeProfilerExpressionInfo(startDivot, endDivot parser.TextPosition) {
	start := uint32(startDivot) // ranges are inclusive of their endpoints and 0 indexed
	end := uint32(endDivot) - 1 // end ranges already go one past the inclusive range
	g.codeBlock.AddTypeProfilerExpressionInfo(uint32(len(g.instructions)-1), start, end)
}

// emitProfileTypeFlag profiles something that is not in the program text
// (e.g. an implicit return undefined), so no expression info is emitted.
func (g *Generator) emitProfileTypeFlag(registerToProfile *RegisterID, flag bytecode.ProfileTypeBytecodeFlag) {
	if !g.shouldEmitTypeProfilerHooks() || registerToProfile == nil {
		return
	}
	bytecode.EmitProfileType(g, vr(registerToProfile), 0, flag, 0, g.resolveType())
}

// emitProfileTypeRange profiles a value without a global id.
func (g *Generator) emitProfileTypeRange(registerToProfile *RegisterID, startDivot, endDivot parser.TextPosition) {
	g.emitProfileTypeFlagRange(registerToProfile, bytecode.ProfileTypeBytecodeDoesNotHaveGlobalID, startDivot, endDivot)
}

func (g *Generator) emitProfileTypeFlagRange(registerToProfile *RegisterID, flag bytecode.ProfileTypeBytecodeFlag, startDivot, endDivot parser.TextPosition) {
	if !g.shouldEmitTypeProfilerHooks() || registerToProfile == nil {
		return
	}
	bytecode.EmitProfileType(g, vr(registerToProfile), 0, flag, 0, g.resolveType())
	g.emitTypeProfilerExpressionInfo(startDivot, endDivot)
}

// emitProfileTypeVariable profiles a variable of the program (a local,
// a resolved closure variable or a closure variable).
func (g *Generator) emitProfileTypeVariable(registerToProfile *RegisterID, v Variable, startDivot, endDivot parser.TextPosition) {
	if !g.shouldEmitTypeProfilerHooks() || registerToProfile == nil {
		return
	}
	var flag bytecode.ProfileTypeBytecodeFlag
	var symbolTableOrScopeDepth bytecode.SymbolTableOrScopeDepth
	if v.Local() != nil || v.Offset().IsScope() {
		flag = bytecode.ProfileTypeBytecodeLocallyResolved
		symbolTableOrScopeDepth = bytecode.SymbolTableOrScopeDepthForSymbolTable(bytecode.VirtualRegister(v.SymbolTableConstantIndex()))
	} else {
		flag = bytecode.ProfileTypeBytecodeClosureVar
		symbolTableOrScopeDepth = bytecode.SymbolTableOrScopeDepthForScopeDepth(uint32(g.localScopeDepth))
	}
	bytecode.EmitProfileType(g, vr(registerToProfile), symbolTableOrScopeDepth, flag, g.addConstant(v.Ident()), g.resolveType())
	g.emitTypeProfilerExpressionInfo(startDivot, endDivot)
}

func (g *Generator) emitProfileControlFlow(textOffset int) {
	if g.shouldEmitControlFlowProfilerHooks() {
		bytecode.EmitProfileControlFlow(g, int32(textOffset))
		g.codeBlock.OpProfileControlFlowPCs = append(g.codeBlock.OpProfileControlFlowPCs, uint32(g.lastInstruction))
	}
}
