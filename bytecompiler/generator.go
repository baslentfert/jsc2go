package bytecompiler

import (
	"github.com/baslentfert/jsc2go/bytecode"
	"github.com/baslentfert/jsc2go/ident"
	"github.com/baslentfert/jsc2go/parser"
)

// Port of JavaScriptCore/bytecompiler/{BytecodeGenerator.h,
// BytecodeGeneratorBase.h,BytecodeGeneratorBaseInlines.h}.

// ExpectedFunction lets calls to Object() and Array() take a fast path.
type ExpectedFunction uint8

const (
	NoExpectedFunction ExpectedFunction = iota
	ExpectObjectConstructor
	ExpectArrayConstructor
)

type EmitAwait bool
type DebuggableCall bool

const (
	EmitAwaitNo  EmitAwait = false
	EmitAwaitYes EmitAwait = true

	DebuggableCallNo  DebuggableCall = false
	DebuggableCallYes DebuggableCall = true
)

type ThisResolutionType uint8

const (
	ThisResolutionLocal ThisResolutionType = iota
	ThisResolutionScoped
)

type InvalidPrototypeMode uint8

const (
	InvalidPrototypeThrow InvalidPrototypeMode = iota
	InvalidPrototypeIgnore
)

// ScopeType is BytecodeGenerator::ScopeType.
type ScopeType uint8

const (
	ScopeTypeCatchScope ScopeType = iota
	ScopeTypeCatchScopeWithSimpleParameter
	ScopeTypeLetConstScope
	ScopeTypeFunctionNameScope
	ScopeTypeClassScope
)

type TDZCheckOptimization uint8

const (
	TDZCheckOptimize TDZCheckOptimization = iota
	TDZCheckDoNotOptimize
)

type NestedScopeType uint8

const (
	NestedScopeIsNested NestedScopeType = iota
	NestedScopeIsNotNested
)

type tdzRequirement uint8

const (
	underTDZ tdzRequirement = iota
	notUnderTDZ
)

type scopeRegisterType uint8

const (
	scopeRegisterVar scopeRegisterType = iota
	scopeRegisterBlock
)

type tdzNecessityLevel uint8

const (
	tdzNotNeeded tdzNecessityLevel = iota
	tdzOptimize
	tdzDoNotOptimize
)

type functionVariableType uint8

const (
	normalFunctionVariable functionVariableType = iota
	topLevelFunctionVariable
)

type existingVariableMode uint8

const (
	verifyExisting existingVariableMode = iota
	ignoreExisting
)

// Property descriptor options for emitCallDefineProperty.
const (
	PropertyConfigurable = 1
	PropertyWritable     = 1 << 1
	PropertyEnumerable   = 1 << 2
)

// VariableKind distinguishes ordinary variables from special ones such as
// the arguments object or the callee.
type VariableKind uint8

const (
	NormalVariable VariableKind = iota
	SpecialVariable
)

// Variable is the result of resolving an identifier at compile time.
type Variable struct {
	ident                    ident.Identifier
	offset                   bytecode.VarOffset
	local                    *RegisterID
	readOnly                 bool
	kind                     VariableKind
	symbolTableConstantIndex int
	isLexicallyScoped        bool
}

// unresolvedVariable is Variable(const Identifier&): a name that is looked up
// dynamically.
func unresolvedVariable(id ident.Identifier) Variable {
	return Variable{ident: id, offset: bytecode.InvalidVarOffset}
}

func (v Variable) IsResolved() bool              { return v.offset.IsValid() }
func (v Variable) SymbolTableConstantIndex() int { return v.symbolTableConstantIndex }
func (v Variable) Ident() ident.Identifier       { return v.ident }
func (v Variable) Offset() bytecode.VarOffset    { return v.offset }
func (v Variable) IsLocal() bool                 { return v.offset.IsStack() }
func (v Variable) Local() *RegisterID            { return v.local }
func (v Variable) IsReadOnly() bool              { return v.readOnly }
func (v Variable) IsSpecial() bool               { return v.kind != NormalVariable }
func (v Variable) IsConst() bool                 { return v.readOnly && v.isLexicallyScoped }
func (v *Variable) SetIsReadOnly()               { v.readOnly = true }

// CompletionType encodes how a finally block was entered. Values from
// numberOfCompletionTypes up are jump IDs of break/continue statements.
type CompletionType int32

const (
	CompletionNormal CompletionType = iota
	CompletionThrow
	CompletionReturn
	numberOfCompletionTypes
)

func bytecodeOffsetToJumpID(offset int) CompletionType {
	return CompletionType(offset + int(numberOfCompletionTypes))
}

// FinallyJump is a break or continue that has to run finally blocks first.
type FinallyJump struct {
	jumpID                  CompletionType
	targetLexicalScopeIndex int
	targetLabel             *Label
}

// FinallyContext tracks the jumps that leave a try block with a finally.
type FinallyContext struct {
	outerContext              *FinallyContext
	finallyLabel              *Label
	numberOfBreaksOrContinues uint32
	handlesReturns            bool
	jumps                     []FinallyJump
	completionTypeRegister    *RegisterID
	completionValueRegister   *RegisterID
}

// newFinallyContext allocates the completion record registers; the caller's
// refScope holds them.
func (g *Generator) newFinallyContext(finallyLabel *Label) *FinallyContext {
	fc := &FinallyContext{outerContext: g.currentFinallyContext, finallyLabel: finallyLabel}
	fc.completionTypeRegister = g.keep(g.newTemporary())
	fc.completionValueRegister = g.keep(g.newTemporary())
	g.emitLoadCompletionType(fc.completionTypeRegister, CompletionNormal)
	g.moveEmptyValue(fc.completionValueRegister)
	return fc
}

func (f *FinallyContext) OuterContext() *FinallyContext        { return f.outerContext }
func (f *FinallyContext) FinallyLabel() *Label                 { return f.finallyLabel }
func (f *FinallyContext) CompletionTypeRegister() *RegisterID  { return f.completionTypeRegister }
func (f *FinallyContext) CompletionValueRegister() *RegisterID { return f.completionValueRegister }
func (f *FinallyContext) NumberOfBreaksOrContinues() uint32    { return f.numberOfBreaksOrContinues }
func (f *FinallyContext) incNumberOfBreaksOrContinues()        { f.numberOfBreaksOrContinues++ }
func (f *FinallyContext) HandlesReturns() bool                 { return f.handlesReturns }
func (f *FinallyContext) setHandlesReturns()                   { f.handlesReturns = true }

func (f *FinallyContext) registerJump(jumpID CompletionType, lexicalScopeIndex int, target *Label) {
	target.retained++
	f.jumps = append(f.jumps, FinallyJump{jumpID, lexicalScopeIndex, target})
}

// controlFlowScope is an entry of the stack that break/continue/return walk.
type controlFlowScope struct {
	isFinally         bool // otherwise a label (lexical) scope
	lexicalScopeIndex int
	finallyContext    *FinallyContext
}

func (c *controlFlowScope) isLabelScope() bool   { return !c.isFinally }
func (c *controlFlowScope) isFinallyScope() bool { return c.isFinally }

// forInContext lets a for-in body access the enumerated property directly.
type forInContext struct {
	localRegister           *RegisterID
	propertyName            *RegisterID
	propertyOffset          *RegisterID
	enumerator              *RegisterID
	mode                    *RegisterID
	baseVariable            *Variable
	isValid                 bool
	bodyBytecodeStartOffset int

	inInsts                 []forInInst
	getInsts                []forInInst
	putInsts                []forInInst
	hasOwnPropertyJumpInsts [][2]int // branch instruction, generic path target
}

type forInInst struct {
	instIndex        int
	propertyRegIndex int
}

func (c *forInContext) IsValid() bool             { return c.isValid }
func (c *forInContext) invalidate()               { c.isValid = false }
func (c *forInContext) Local() *RegisterID        { return c.localRegister }
func (c *forInContext) PropertyName() *RegisterID { return c.propertyName }
func (c *forInContext) PropertyOffset() *RegisterID {
	return c.propertyOffset
}
func (c *forInContext) Enumerator() *RegisterID { return c.enumerator }
func (c *forInContext) Mode() *RegisterID       { return c.mode }

func (c *forInContext) addGetInst(instIndex, propertyRegIndex int) {
	c.getInsts = append(c.getInsts, forInInst{instIndex, propertyRegIndex})
}
func (c *forInContext) addPutInst(instIndex, propertyRegIndex int) {
	c.putInsts = append(c.putInsts, forInInst{instIndex, propertyRegIndex})
}
func (c *forInContext) addInInst(instIndex, propertyRegIndex int) {
	c.inInsts = append(c.inInsts, forInInst{instIndex, propertyRegIndex})
}
func (c *forInContext) addHasOwnPropertyJump(branchInstIndex, genericPathTarget int) {
	c.hasOwnPropertyJumpInsts = append(c.hasOwnPropertyJumpInsts, [2]int{branchInstIndex, genericPathTarget})
}

type tryData struct {
	target      *Label
	handlerType bytecode.HandlerType
}

type tryContext struct {
	start   *Label
	tryData *tryData
}

type tryRange struct {
	start, end *Label
	tryData    *tryData
}

type usingSlot struct {
	value, method, reached *RegisterID
	isAsync                bool
}

type usingScope struct {
	slots         []usingSlot
	nextSlot      int
	hasAwaitUsing bool
}

type lexicalScopeStackEntry struct {
	symbolTable              *bytecode.SymbolTable
	scope                    *RegisterID
	isWithScope              bool
	symbolTableConstantIndex int
}

// tdzMap is an insertion-ordered map from names to TDZ needs.
type tdzMap struct {
	names []ident.Identifier
	level map[ident.Identifier]tdzNecessityLevel
}

func (m *tdzMap) set(id ident.Identifier, l tdzNecessityLevel) {
	if m.level == nil {
		m.level = make(map[ident.Identifier]tdzNecessityLevel)
	}
	if _, ok := m.level[id]; !ok {
		m.names = append(m.names, id)
	}
	m.level[id] = l
}

func (m *tdzMap) find(id ident.Identifier) (tdzNecessityLevel, bool) {
	l, ok := m.level[id]
	return l, ok
}

type tdzStackEntry struct {
	m    tdzMap
	link *bytecode.TDZEnvironmentLink
}

// PreservedTDZStack is a saved copy of the TDZ stack.
type PreservedTDZStack struct{ stack []tdzStackEntry }

type catchEntry struct {
	tryData                *tryData
	exceptionRegister      bytecode.VirtualRegister
	thrownValueRegister    bytecode.VirtualRegister
	completionTypeRegister bytecode.VirtualRegister
}

type functionToInitialize struct {
	metadata *parser.FunctionMetadataNode
	typ      functionVariableType
}

type asyncFuncParametersTryCatchInfo struct {
	catchStartLabel *Label
	thrownValue     *RegisterID
}

type bigIntKey struct {
	id    ident.Identifier
	radix uint8
	sign  bool
}

type constantKey struct {
	key  any
	repr bytecode.SourceCodeRepresentation
}

// numberConstantKey identifies a number constant (bytecode.ConstantKey's
// numberKey, without the interface).
type numberConstantKey struct {
	bits  uint64
	int32 bool
	repr  bytecode.SourceCodeRepresentation
}

// Generator is BytecodeGenerator.
type Generator struct {
	// BytecodeGeneratorBase.
	instructions                  []uint32
	codeBlock                     *bytecode.UnlinkedCodeBlock
	outOfMemoryDuringConstruction bool
	lastOpcodeID                  bytecode.OpcodeID
	lastInstruction               int
	calleeLocals                  []*RegisterID
	registerSlots                 []*RegisterID // every RegisterID ever allocated, by local index

	kept []refCounted // see refScope

	codeGenerationMode bytecode.CodeGenerationMode

	lexicalScopeStack                             []lexicalScopeStackEntry
	cachedParentTDZ                               *bytecode.TDZEnvironmentLink
	generatorOrAsyncWrapperFunctionParameterNames []ident.Identifier
	tdzStack                                      []tdzStackEntry
	privateNamesStack                             []parser.PrivateNameEnvironment
	varScopeLexicalScopeStackIndex                int // -1 if unset

	scopeNode  parser.ParsedNode
	scope      *parser.ScopeNode
	sourceCode parser.SourceCode

	functions map[ident.Identifier]struct{}

	ignoredResultRegister RegisterID
	thisRegister          RegisterID
	calleeRegister        RegisterID

	scopeRegister         *RegisterID
	topLevelScopeRegister *RegisterID
	argumentsRegister     *RegisterID
	// argumentsForwarded: the function uses arguments only as
	// x.apply(y, arguments), so no arguments object is made at entry;
	// call_varargs passes the frame's arguments on when apply is the
	// original, and other reads make the object then (genResolve).
	argumentsForwarded                             bool
	lexicalEnvironmentRegister                     *RegisterID
	generatorRegister                              *RegisterID
	emptyValueRegister                             *RegisterID
	newTargetRegister                              *RegisterID
	isDerivedConstuctor                            *RegisterID
	linkTimeConstantRegisters                      map[bytecode.LinkTimeConstant]*RegisterID
	arrowFunctionContextLexicalEnvironmentRegister *RegisterID
	promiseRegister                                *RegisterID

	currentFinallyContext *FinallyContext

	parameters            []*RegisterID
	labelScopes           []*LabelScope
	constantPoolRegisters []*RegisterID

	finallyDepth    int
	localScopeDepth int
	localScopeCount int
	codeType        bytecode.CodeType

	controlFlowScopeStack []controlFlowScope
	switchContextStack    []switchInfo
	forInContextStack     []*forInContext
	tryContextStack       []tryContext
	usingScopeStack       []*usingScope

	yieldPoints                    int
	needsGeneratorification        bool
	generatorFrameSymbolTable      *bytecode.SymbolTable
	generatorFrameSymbolTableIndex int

	functionsToInitialize     []functionToInitialize
	needToInitializeArguments bool
	restParameter             *parser.RestParameterNode

	asyncFuncParametersTryCatchInfo *asyncFuncParametersTryCatchInfo

	tryRanges                []tryRange
	optionalChainTargetStack []*Label

	nextConstantOffset int

	// Constant pool deduplication.
	identifierMap         map[ident.Identifier]uint32
	jsValueMap            map[constantKey]int
	numberConstants       map[numberConstantKey]int
	stringMap             map[ident.Identifier]bytecode.String
	bigIntMap             map[bigIntKey]bytecode.BigInt
	templateDescriptorMap map[int]*bytecode.TemplateObject

	staticPropertyAnalyzer staticPropertyAnalyzer

	defaultAllowCallIgnoreResultOptimization bool
	usesExceptions                           bool
	expressionTooDeep                        bool
	isBuiltinFunction                        bool
	isBuiltinDefaultClassConstructor         bool
	usesSloppyEval                           bool
	allowTailCallOptimization                bool
	allowCallIgnoreResultOptimization        bool
	needsToUpdateArrowFunctionContext        bool
	needsArguments                           bool
	ecmaMode                                 bytecode.ECMAMode
	derivedContextType                       parser.DerivedContextType

	exceptionHandlersToEmit []catchEntry

	lastDebugHook struct {
		position parser.TextPosition
		typ      bytecode.DebugHookType
	}

	recursionDepth int
}

// maxRecursionDepth replaces VM::isSafeToRecurse().
const maxRecursionDepth = 10000

func newGenerator(codeBlock *bytecode.UnlinkedCodeBlock, node parser.ParsedNode, mode bytecode.CodeGenerationMode, codeType bytecode.CodeType) *Generator {
	g := &Generator{
		codeBlock:                      codeBlock,
		lastOpcodeID:                   bytecode.OpDebugID,
		codeGenerationMode:             mode,
		varScopeLexicalScopeStackIndex: -1,
		scopeNode:                      node,
		scope:                          node.Scope(),
		sourceCode:                     node.Scope().Source,
		functions:                      make(map[ident.Identifier]struct{}),
		linkTimeConstantRegisters:      make(map[bytecode.LinkTimeConstant]*RegisterID),
		codeType:                       codeType,
		identifierMap:                  make(map[ident.Identifier]uint32),
		jsValueMap:                     make(map[constantKey]int),
		numberConstants:                make(map[numberConstantKey]int),
		stringMap:                      make(map[ident.Identifier]bytecode.String),
		bigIntMap:                      make(map[bigIntKey]bytecode.BigInt),
		templateDescriptorMap:          make(map[int]*bytecode.TemplateObject),
	}
	g.ignoredResultRegister.vr = bytecode.InvalidVirtualRegister
	g.thisRegister.vr = bytecode.InvalidVirtualRegister
	g.calleeRegister.vr = bytecode.InvalidVirtualRegister
	g.lastDebugHook.position = parser.NoTextPosition
	g.lastDebugHook.typ = bytecode.DidExecuteProgram
	g.allocateCalleeSaveSpace(calleeSaveSpaceAsVirtualRegisters)
	return g
}

// calleeSaveSpaceAsVirtualRegisters is
// CodeBlock::llintBaselineCalleeSaveSpaceAsVirtualRegisters() on 64-bit
// platforms; it keeps register numbering identical to JSC's.
const calleeSaveSpaceAsVirtualRegisters = 1

// stackAlignmentRegisters is the stack alignment in registers (16 bytes).
const stackAlignmentRegisters = 2

// --- bytecode.Emitter ---------------------------------------------------------

// RecordOpcode is called by the generated Emit functions before an
// instruction is written.
func (g *Generator) RecordOpcode(op bytecode.OpcodeID) {
	g.lastInstruction = len(g.instructions)
	g.lastOpcodeID = op
}

func (g *Generator) Write(words ...uint32) { g.instructions = append(g.instructions, words...) }

func (g *Generator) AddMetadataFor(op bytecode.OpcodeID) uint32 {
	return g.codeBlock.Metadata.AddEntry(op)
}

func (g *Generator) SetUsesCheckpoints() { g.codeBlock.HasCheckpoints = true }

// nextValueProfileIndex allocates a value profile for an instruction.
func (g *Generator) nextValueProfileIndex() uint32 { return g.codeBlock.Metadata.AddValueProfile() }

func (g *Generator) addBinaryArithProfile() uint32 {
	id := g.codeBlock.NumBinaryArithProfiles
	g.codeBlock.NumBinaryArithProfiles++
	return id
}

func (g *Generator) addUnaryArithProfile() uint32 {
	id := g.codeBlock.NumUnaryArithProfiles
	g.codeBlock.NumUnaryArithProfiles++
	return id
}

// position is the offset at which the next instruction will be written.
func (g *Generator) position() int { return len(g.instructions) }

// bind returns label's target for the instruction about to be written.
func (g *Generator) bind(l *Label) bytecode.BoundLabel { return l.bindAt(g.position()) }

// --- Labels and registers (BytecodeGeneratorBase) -------------------------------

func (g *Generator) newLabel() *Label { return &Label{location: -1} }

func (g *Generator) newEmittedLabel() *Label {
	l := g.newLabel()
	g.emitLabel(l)
	return l
}

func (g *Generator) emitLabel(l *Label) {
	l.setLocation(g, g.position())
	g.lastOpcodeID = bytecode.OpDebugID // disables peephole optimizations
}

func (g *Generator) reclaimFreeRegisters() {
	for n := len(g.calleeLocals); n > 0 && g.calleeLocals[n-1].refCount == 0; n-- {
		g.calleeLocals = g.calleeLocals[:n-1]
	}
}

// newRegister allocates the next local. Like the SegmentedVector in C++, a
// reclaimed slot is rebuilt in place: the same *RegisterID is reused, so a
// raw pointer to a reclaimed register stays identical to the new one.
func (g *Generator) newRegister() *RegisterID {
	index := len(g.calleeLocals)
	vr := bytecode.VirtualRegisterForLocal(index)
	var r *RegisterID
	if index < len(g.registerSlots) {
		r = g.registerSlots[index]
		*r = RegisterID{vr: vr}
	} else {
		r = newRegisterID(vr)
		g.registerSlots = append(g.registerSlots, r)
	}
	g.calleeLocals = append(g.calleeLocals, r)
	n := max(g.codeBlock.NumCalleeLocals, len(g.calleeLocals))
	n = (n + stackAlignmentRegisters - 1) / stackAlignmentRegisters * stackAlignmentRegisters
	g.codeBlock.NumCalleeLocals = n
	return r
}

// newTemporary returns a register that is free once nothing references it.
func (g *Generator) newTemporary() *RegisterID {
	g.reclaimFreeRegisters()
	r := g.newRegister()
	r.setTemporary()
	return r
}

func (g *Generator) newTemporaries(count int, f func(*RegisterID)) {
	g.reclaimFreeRegisters()
	for range count {
		r := g.newRegister()
		r.setTemporary()
		f(r)
	}
}

// addVar allocates a register that is never freed.
func (g *Generator) addVar() *RegisterID {
	g.codeBlock.NumVars++
	r := g.newRegister()
	r.ref()
	return r
}

func (g *Generator) allocateCalleeSaveSpace(count int) {
	for range count {
		g.addVar()
	}
}

// newBlockScopeVariable allocates in the temporary area without marking the
// register temporary, so finalDestination() never reuses it.
func (g *Generator) newBlockScopeVariable() *RegisterID {
	g.reclaimFreeRegisters()
	return g.newRegister()
}

func (g *Generator) registerFor(reg bytecode.VirtualRegister) *RegisterID {
	if reg.IsLocal() {
		return g.calleeLocals[reg.ToLocal()]
	}
	if reg == bytecode.CallFrameSlotCallee {
		return &g.calleeRegister
	}
	return g.parameters[reg.ToArgument()]
}

// --- Simple accessors -----------------------------------------------------------

func (g *Generator) CodeBlock() *bytecode.UnlinkedCodeBlock { return g.codeBlock }
func (g *Generator) isConstructor() bool                    { return g.codeBlock.IsConstructor() }
func (g *Generator) allowsTailCallOptimization() bool       { return g.allowTailCallOptimization }
func (g *Generator) usesArrowFunction() bool                { return g.scope.UsesArrowFunction() }
func (g *Generator) usesEval() bool                         { return g.scope.UsesEval() }
func (g *Generator) usesThis() bool                         { return g.scope.UsesThis() }
func (g *Generator) isFunctionNode() bool {
	_, ok := g.scopeNode.(*parser.FunctionNode)
	return ok
}
func (g *Generator) hasShadowsArgumentsCodeFeature() bool {
	return g.scope.HasShadowsArgumentsFeature()
}
func (g *Generator) isAsyncFunctionWithoutAwait() bool { return g.scope.IsAsyncFunctionWithoutAwait() }
func (g *Generator) lexicallyScopedFeatures() parser.LexicallyScopedFeatures {
	return g.scope.LexicallyScopedFeatures
}
func (g *Generator) privateBrandRequirement() parser.PrivateBrandRequirement {
	return g.codeBlock.Info.PrivateBrandRequirement
}
func (g *Generator) constructorKind() parser.ConstructorKind { return g.codeBlock.Info.ConstructorKind }
func (g *Generator) superBinding() parser.SuperBinding       { return g.codeBlock.Info.SuperBinding }
func (g *Generator) scriptMode() parser.ScriptMode           { return g.codeBlock.Info.ScriptMode }
func (g *Generator) needsClassFieldInitializer() bool {
	return g.codeBlock.Info.NeedsClassFieldInitializer
}
func (g *Generator) ThisRegister() *RegisterID      { return &g.thisRegister }
func (g *Generator) ArgumentsRegister() *RegisterID { return g.argumentsRegister }
func (g *Generator) NewTarget() *RegisterID         { return g.newTargetRegister }
func (g *Generator) ScopeRegister() *RegisterID     { return g.scopeRegister }
func (g *Generator) GeneratorRegister() *RegisterID { return g.generatorRegister }
func (g *Generator) PromiseRegister() *RegisterID   { return g.promiseRegister }
func (g *Generator) IgnoredResult() *RegisterID     { return &g.ignoredResultRegister }

func (g *Generator) codeTypeOf() bytecode.CodeType { return g.codeType }
func (g *Generator) shouldBeConcernedWithCompletionValue() bool {
	return !g.defaultAllowCallIgnoreResultOptimization
}
func (g *Generator) shouldEmitDebugHooks() bool {
	return g.codeGenerationMode.Contains(bytecode.CodeGenerationModeDebugger) && !g.isBuiltinFunction
}
func (g *Generator) shouldEmitTypeProfilerHooks() bool {
	return g.codeGenerationMode.Contains(bytecode.CodeGenerationModeTypeProfiler)
}
func (g *Generator) shouldEmitControlFlowProfilerHooks() bool {
	return g.codeGenerationMode.Contains(bytecode.CodeGenerationModeControlFlowProfiler)
}
func (g *Generator) parseMode() parser.SourceParseMode { return g.codeBlock.ParseMode() }
func (g *Generator) isDerivedConstructorContext() bool {
	return g.derivedContextType == parser.DerivedContextTypeDerivedConstructorContext
}
func (g *Generator) isDerivedClassContext() bool {
	return g.derivedContextType == parser.DerivedContextTypeDerivedMethodContext
}
func (g *Generator) isArrowFunction() bool { return g.codeBlock.IsArrowFunction() }

func (g *Generator) disablePeepholeOptimization() { g.lastOpcodeID = bytecode.OpDebugID }
func (g *Generator) canDoPeepholeOptimization() bool {
	return g.lastOpcodeID != bytecode.OpDebugID
}

func (g *Generator) hasFinallyScopes() bool { return g.currentFinallyContext != nil }

// Generator registers of generator bodies: [this], @generator,
// @generatorState, @generatorValue, @generatorResumeMode, @generatorFrame.
const (
	generatorArgumentThisValue = iota
	generatorArgumentGenerator
	generatorArgumentState
	generatorArgumentValue
	generatorArgumentResumeMode
	generatorArgumentFrame
	generatorNumberOfArguments = generatorArgumentFrame
)

func (g *Generator) generatorStateRegister() *RegisterID {
	return g.parameters[generatorArgumentState]
}
func (g *Generator) generatorValueRegister() *RegisterID {
	return g.parameters[generatorArgumentValue]
}
func (g *Generator) generatorResumeModeRegister() *RegisterID {
	return g.parameters[generatorArgumentResumeMode]
}
func (g *Generator) generatorFrameRegister() *RegisterID {
	return g.parameters[generatorArgumentFrame]
}

// --- Destination helpers -----------------------------------------------------

// newTemporaryOr returns suggestion if it is a temporary.
func (g *Generator) newTemporaryOr(suggestion *RegisterID) *RegisterID {
	if suggestion.IsTemporary() {
		return suggestion
	}
	return g.newTemporary()
}

// tempDestination returns a place for intermediate values that reuses dst
// when that is safe.
func (g *Generator) tempDestination(dst *RegisterID) *RegisterID {
	if dst != nil && dst != g.IgnoredResult() && dst.IsTemporary() {
		return dst
	}
	return g.newTemporary()
}

// finalDestination returns the place to write an operation's result.
func (g *Generator) finalDestination(originalDst *RegisterID, tempDst ...*RegisterID) *RegisterID {
	if originalDst != nil && originalDst != g.IgnoredResult() {
		return originalDst
	}
	if len(tempDst) > 0 && tempDst[0] != nil && tempDst[0].IsTemporary() {
		return tempDst[0]
	}
	return g.newTemporary()
}

func (g *Generator) destinationForAssignResult(dst *RegisterID) *RegisterID {
	if dst != nil && dst != g.IgnoredResult() {
		if dst.IsTemporary() {
			return dst
		}
		return g.newTemporary()
	}
	return nil
}

// move moves src to dst unless dst is nil or src; it returns where the value
// ended up (nil if dst is the ignored result).
func (g *Generator) move(dst, src *RegisterID) *RegisterID {
	if dst == g.IgnoredResult() {
		return nil
	}
	if dst != nil && dst != src {
		return g.emitMove(dst, src)
	}
	return src
}

func (g *Generator) kill(dst *RegisterID) *RegisterID {
	g.staticPropertyAnalyzer.killRegister(g, dst)
	return dst
}

// withPoison runs f with tail-call and ignore-result optimizations set as
// given (C++ SetForScope pairs).
func (g *Generator) withPoison(allowTailCall, allowIgnoreResult bool, f func()) {
	savedTail, savedIgnore := g.allowTailCallOptimization, g.allowCallIgnoreResultOptimization
	g.allowTailCallOptimization, g.allowCallIgnoreResultOptimization = allowTailCall, allowIgnoreResult
	defer func() {
		g.allowTailCallOptimization, g.allowCallIgnoreResultOptimization = savedTail, savedIgnore
	}()
	f()
}

// strictModeScope switches to strict mode until the returned function runs.
func (g *Generator) strictModeScope() func() {
	saved := g.ecmaMode
	g.ecmaMode = bytecode.ECMAModeStrict
	return func() { g.ecmaMode = saved }
}

// switchType is SwitchInfo::SwitchType.
type switchType uint8

const (
	switchTypeNone switchType = iota
	switchTypeImmediate
	switchTypeCharacter
	switchTypeImmediateList
	switchTypeCharacterList
	switchTypeString
)

type switchInfo struct {
	bytecodeOffset int
	switchType     switchType
}
