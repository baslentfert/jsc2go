// Package bytecode holds JSC's unlinked bytecode: opcodes, instruction
// streams, UnlinkedCodeBlock, SymbolTable and constants. It does not depend
// on the runtime; like JSC's unlinked code it can be shared between VMs.
package bytecode

//go:generate go run ./internal/genopcodes -in BytecodeList.rb -out opcodes_gen.go

import (
	"fmt"

	"github.com/baslentfert/jsc2go/parser"
)

// OpcodeID identifies a bytecode instruction.
type OpcodeID uint32

func (op OpcodeID) String() string {
	if op < NumberOfBytecodeIDs {
		return opcodeNames[op]
	}
	return fmt.Sprintf("OpcodeID(%d)", uint32(op))
}

// Length returns the instruction length in words, including the opcode.
func (op OpcodeID) Length() int { return 1 + int(opcodeLengths[op]) }

func (op OpcodeID) HasMetadata() bool { return opcodeHasMetadata[op] }

// HasCheckpoints reports whether op can exit in the middle of the instruction.
func (op OpcodeID) HasCheckpoints() bool { return opcodeNumberOfCheckpoints[op] > 0 }

// NumberOfCheckpoints is at least 1 (the whole instruction).
func (op OpcodeID) NumberOfCheckpoints() int {
	if n := opcodeNumberOfCheckpoints[op]; n > 0 {
		return int(n)
	}
	return 1
}
func (op OpcodeID) OperandNames() []string { return opcodeOperandNames[op] }
func (op OpcodeID) OperandKinds() []OperandKind {
	return opcodeOperandKinds[op]
}

// Emitter is what the generated Emit functions write to; the bytecode
// generator implements it.
type Emitter interface {
	RecordOpcode(OpcodeID)
	Write(words ...uint32)
	AddMetadataFor(OpcodeID) uint32
	SetUsesCheckpoints()
}

func boolWord(b bool) uint32 {
	if b {
		return 1
	}
	return 0
}

// OperandKind describes how an operand word is interpreted (for dumping and
// for analyses such as use/def).
type OperandKind uint8

const (
	OperandVirtualRegister OperandKind = iota
	OperandBoundLabel
	Operanduint32
	Operandint32
	Operandbool
	OperandMetadataID
	OperandECMAMode
	OperandSymbolTableOrScopeDepth
	OperandResolveType
	OperandIndexingType
	OperandGetPutInfo
	OperandResultType
	OperandPutByIdFlags
	OperandProfileTypeBytecodeFlag
	OperandPrivateFieldPutKind
	OperandOperandTypes
	OperandJSType
	OperandErrorTypeWithExtension
	OperandDebugHookType
)

// --- Registers ------------------------------------------------------------------------

// FirstConstantRegisterIndex: virtual registers at or above it name constants.
const FirstConstantRegisterIndex = 0x40000000

// Call frame layout, in registers (CallFrameSlot / CallFrame).
const (
	CallFrameSlotCodeBlock                  = 2 // after the caller frame and return PC
	CallFrameSlotCallee                     = 3
	CallFrameSlotArgumentCountIncludingThis = 4
	CallFrameSlotThisArgument               = 5
	CallFrameSlotFirstArgument              = 6
	CallFrameHeaderSizeInRegisters          = CallFrameSlotArgumentCountIncludingThis + 1
)

// VirtualRegister names a frame slot: negative values are locals, values from
// 0 cover the header and arguments, and constants start at FirstConstantRegisterIndex.
type VirtualRegister int32

const InvalidVirtualRegister VirtualRegister = 0x3fffffff

func VirtualRegisterForLocal(local int) VirtualRegister { return VirtualRegister(-1 - local) }

// VirtualRegisterForArgumentIncludingThis: argument 0 is `this`.
func VirtualRegisterForArgumentIncludingThis(argument, offset int) VirtualRegister {
	return VirtualRegister(argument + CallFrameSlotThisArgument + offset)
}

func VirtualRegisterForConstantIndex(index int) VirtualRegister {
	return VirtualRegister(index + FirstConstantRegisterIndex)
}

func (r VirtualRegister) IsValid() bool    { return r != InvalidVirtualRegister }
func (r VirtualRegister) IsLocal() bool    { return r < 0 }
func (r VirtualRegister) IsArgument() bool { return r >= 0 }
func (r VirtualRegister) IsHeader() bool   { return r >= 0 && r < CallFrameSlotThisArgument }
func (r VirtualRegister) IsConstant() bool { return r >= FirstConstantRegisterIndex }
func (r VirtualRegister) ToLocal() int     { return int(-1 - r) }
func (r VirtualRegister) ToArgument() int  { return int(r) - CallFrameSlotThisArgument }
func (r VirtualRegister) ToConstantIndex() int {
	return int(r) - FirstConstantRegisterIndex
}
func (r VirtualRegister) Offset() int { return int(r) }

func (r VirtualRegister) String() string {
	switch {
	case !r.IsValid():
		return "<invalid>"
	case r.IsConstant():
		return fmt.Sprintf("const%d", r.ToConstantIndex())
	case r.IsLocal():
		return fmt.Sprintf("loc%d", r.ToLocal())
	case r == CallFrameSlotCallee:
		return "callee"
	case r == CallFrameSlotThisArgument:
		return "this"
	case r.IsHeader():
		return fmt.Sprintf("header%d", int(r))
	}
	return fmt.Sprintf("arg%d", r.ToArgument())
}

// BoundLabel is a jump target, encoded as an offset relative to the jumping
// instruction (in words).
type BoundLabel int32

// --- Small enum operands ---------------------------------------------------------------

// ECMAMode is strict or sloppy.
type ECMAMode uint32

const (
	ECMAModeStrict ECMAMode = 0
	ECMAModeSloppy ECMAMode = 1
)

func ECMAModeFromBool(isStrict bool) ECMAMode {
	if isStrict {
		return ECMAModeStrict
	}
	return ECMAModeSloppy
}

func (m ECMAMode) IsStrict() bool { return m == ECMAModeStrict }

func (m ECMAMode) String() string {
	if m.IsStrict() {
		return "Strict"
	}
	return "Sloppy"
}

// SymbolTableOrScopeDepth holds either a constant register index of a
// symbol table or a scope depth.
type SymbolTableOrScopeDepth uint32

func SymbolTableOrScopeDepthForSymbolTable(reg VirtualRegister) SymbolTableOrScopeDepth {
	return SymbolTableOrScopeDepth(reg.ToConstantIndex())
}
func SymbolTableOrScopeDepthForScopeDepth(depth uint32) SymbolTableOrScopeDepth {
	return SymbolTableOrScopeDepth(depth)
}
func (s SymbolTableOrScopeDepth) SymbolTable() VirtualRegister {
	return VirtualRegisterForConstantIndex(int(s))
}
func (s SymbolTableOrScopeDepth) ScopeDepth() uint32 { return uint32(s) }

// ResolveMode says whether an unresolvable reference throws.
type ResolveMode uint8

const (
	ThrowIfNotFound ResolveMode = iota
	DoNotThrowIfNotFound
)

// ResolveType says how a variable access was resolved at compile time.
type ResolveType uint32

const (
	GlobalProperty ResolveType = iota
	GlobalVar
	GlobalLexicalVar
	ClosureVar
	ResolvedClosureVar
	ModuleVar
	GlobalPropertyWithVarInjectionChecks
	GlobalVarWithVarInjectionChecks
	GlobalLexicalVarWithVarInjectionChecks
	ClosureVarWithVarInjectionChecks
	UnresolvedProperty
	UnresolvedPropertyWithVarInjectionChecks
	Dynamic
)

var resolveTypeNames = [...]string{"GlobalProperty", "GlobalVar", "GlobalLexicalVar", "ClosureVar", "ResolvedClosureVar",
	"ModuleVar", "GlobalPropertyWithVarInjectionChecks", "GlobalVarWithVarInjectionChecks",
	"GlobalLexicalVarWithVarInjectionChecks", "ClosureVarWithVarInjectionChecks", "UnresolvedProperty",
	"UnresolvedPropertyWithVarInjectionChecks", "Dynamic"}

func (t ResolveType) String() string {
	if int(t) < len(resolveTypeNames) {
		return resolveTypeNames[t]
	}
	return fmt.Sprintf("ResolveType(%d)", uint32(t))
}

// MakeResolveType adds var injection checks when non-strict eval may inject bindings.
func MakeResolveType(t ResolveType, needsVarInjectionChecks bool) ResolveType {
	if !needsVarInjectionChecks {
		return t
	}
	switch t {
	case GlobalProperty:
		return GlobalPropertyWithVarInjectionChecks
	case GlobalVar:
		return GlobalVarWithVarInjectionChecks
	case GlobalLexicalVar:
		return GlobalLexicalVarWithVarInjectionChecks
	case ClosureVar, ResolvedClosureVar:
		return ClosureVarWithVarInjectionChecks
	case UnresolvedProperty:
		return UnresolvedPropertyWithVarInjectionChecks
	}
	return t
}

func (t ResolveType) NeedsVarInjectionChecks() bool {
	switch t {
	case GlobalProperty, GlobalVar, GlobalLexicalVar, ClosureVar, ResolvedClosureVar, ModuleVar, UnresolvedProperty:
		return false
	}
	return true
}

// InitializationMode distinguishes `let x = 1` from `x = 1`.
type InitializationMode uint8

const (
	InitializationModeInitialization InitializationMode = iota
	InitializationModeConstInitialization
	InitializationModeNotInitialization
	InitializationModeScopedArgumentInitialization
)

func (m InitializationMode) IsInitialization() bool {
	return m == InitializationModeInitialization || m == InitializationModeConstInitialization
}

// GetPutInfo packs resolve mode, resolve type, initialization mode and strictness.
type GetPutInfo uint32

const (
	getPutInfoIsStrictShift       = 30
	getPutInfoModeShift           = 20
	getPutInfoInitializationShift = 10
	getPutInfoTypeBits            = 1<<getPutInfoInitializationShift - 1
	getPutInfoInitializationBits  = (1<<getPutInfoModeShift - 1) &^ getPutInfoTypeBits
	getPutInfoModeBits            = (1<<30 - 1) &^ getPutInfoInitializationBits &^ getPutInfoTypeBits
	getPutInfoIsStrictBit         = 1 << 30
)

func MakeGetPutInfo(mode ResolveMode, t ResolveType, init InitializationMode, ecmaMode ECMAMode) GetPutInfo {
	var strict uint32
	if ecmaMode.IsStrict() {
		strict = 1
	}
	return GetPutInfo(strict<<getPutInfoIsStrictShift | uint32(mode)<<getPutInfoModeShift | uint32(init)<<getPutInfoInitializationShift | uint32(t))
}

func (g GetPutInfo) ResolveType() ResolveType { return ResolveType(g & getPutInfoTypeBits) }
func (g GetPutInfo) InitializationMode() InitializationMode {
	return InitializationMode((g & getPutInfoInitializationBits) >> getPutInfoInitializationShift)
}
func (g GetPutInfo) ResolveMode() ResolveMode {
	return ResolveMode((g & getPutInfoModeBits) >> getPutInfoModeShift)
}
func (g GetPutInfo) ECMAMode() ECMAMode {
	return ECMAModeFromBool(g&getPutInfoIsStrictBit != 0)
}

func (g GetPutInfo) String() string {
	modes := [...]string{"ThrowIfNotFound", "DoNotThrowIfNotFound"}
	inits := [...]string{"Initialization", "ConstInitialization", "NotInitialization", "ScopedArgumentInitialization"}
	return fmt.Sprintf("%s|%s|%s|%s", modes[g.ResolveMode()], g.ResolveType(), inits[g.InitializationMode()], g.ECMAMode())
}

// IndexingType is an array shape hint for array allocation.
type IndexingType uint8

const (
	NonArray            IndexingType = 0x0
	IsArray             IndexingType = 0x1
	NoIndexingShape     IndexingType = 0x0
	Int32Shape          IndexingType = 0x4
	DoubleShape         IndexingType = 0x6
	ContiguousShape     IndexingType = 0x8
	ArrayWithUndecided  IndexingType = IsArray | 0x2
	ArrayWithInt32      IndexingType = IsArray | Int32Shape
	ArrayWithDouble     IndexingType = IsArray | DoubleShape
	ArrayWithContiguous IndexingType = IsArray | ContiguousShape
	CopyOnWrite         IndexingType = 0x10
)

// ResultType and OperandTypes carry the parser's static type hints.
type ResultType uint32
type OperandTypes uint32

func ResultTypeOf(t parser.ResultType) ResultType       { return ResultType(t) }
func OperandTypesOf(t parser.OperandTypes) OperandTypes { return OperandTypes(t.Bits()) }

// PutByIdFlags: whether the put is direct (ignores setters) and its ECMA mode.
type PutByIdFlags uint32

func PutByIdFlagsCreate(mode ECMAMode) PutByIdFlags       { return PutByIdFlags(mode) }
func PutByIdFlagsCreateDirect(mode ECMAMode) PutByIdFlags { return PutByIdFlags(mode) | 0x80 }
func (f PutByIdFlags) IsDirect() bool                     { return f&0x80 != 0 }
func (f PutByIdFlags) ECMAMode() ECMAMode                 { return ECMAMode(f & 0x7f) }

// ProfileTypeBytecodeFlag classifies type-profiler hooks.
type ProfileTypeBytecodeFlag uint8

const (
	ProfileTypeBytecodeClosureVar ProfileTypeBytecodeFlag = iota
	ProfileTypeBytecodeLocallyResolved
	ProfileTypeBytecodeDoesNotHaveGlobalID
	ProfileTypeBytecodeFunctionArgument
	ProfileTypeBytecodeFunctionReturnStatement
)

// PrivateFieldPutKind distinguishes setting from defining a private field.
type PrivateFieldPutKind uint8

const (
	PrivateFieldPutKindNone   PrivateFieldPutKind = 0
	PrivateFieldPutKindSet    PrivateFieldPutKind = 1
	PrivateFieldPutKindDefine PrivateFieldPutKind = 2
)

// ErrorTypeWithExtension is the error class thrown by throw_static_error.
type ErrorTypeWithExtension uint8

const (
	ErrorTypeError ErrorTypeWithExtension = iota
	ErrorTypeEvalError
	ErrorTypeRangeError
	ErrorTypeReferenceError
	ErrorTypeSyntaxError
	ErrorTypeTypeError
	ErrorTypeURIError
	ErrorTypeAggregateError
	ErrorTypeSuppressedError
	ErrorTypeOutOfMemoryError
)

// DebugHookType is the reason passed to op_debug.
type DebugHookType uint8

const (
	WillExecuteProgram DebugHookType = iota
	DidExecuteProgram
	DidEnterCallFrame
	DidReachDebuggerStatement
	WillLeaveCallFrame
	WillExecuteStatement
	WillExecuteExpression
	WillAwait
	DidAwait
)

// JSType is a cell type tag (only the ones the bytecode generator tests for).
type JSType uint8

// Cell types tested by op_is_cell_with_type. The runtime assigns these to
// its cells; the values only need to agree between compiler and runtime.
const (
	StringType JSType = iota + 1
	SymbolType
	HeapBigIntType
	ArrayType
	DerivedArrayType
	JSGeneratorType
	JSAsyncGeneratorType
	JSIteratorHelperType
	JSPromiseType
	ProxyObjectType
	RegExpObjectType
	JSMapType
	JSSetType
	ShadowRealmType
	JSArrayIteratorType
	JSWrapForValidIteratorType
	JSRegExpStringIteratorType
	DisposableStackType
	AsyncDisposableStackType
)
