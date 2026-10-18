package bytecode

import (
	"sort"

	"github.com/baslentfert/jsc2go/ident"
	"github.com/baslentfert/jsc2go/parser"
)

// Port of JavaScriptCore/bytecode/{UnlinkedCodeBlock,UnlinkedCodeBlockGenerator,
// UnlinkedProgramCodeBlock,UnlinkedModuleProgramCodeBlock,UnlinkedEvalCodeBlock,
// UnlinkedFunctionCodeBlock,ExecutableInfo,HandlerInfo,ExpressionInfo,
// UnlinkedMetadataTable}.h.
//
// JSC splits building (UnlinkedCodeBlockGenerator) from the finished cell
// (UnlinkedCodeBlock and its subclasses). Here there is one struct that the
// bytecode generator fills in directly; the per-code-type fields of the
// subclasses live in it too.

// CodeType is the kind of code a code block holds.
type CodeType uint8

const (
	GlobalCode CodeType = iota
	EvalCode
	FunctionCode
	ModuleCode
)

func (t CodeType) String() string {
	switch t {
	case GlobalCode:
		return "Global"
	case EvalCode:
		return "Eval"
	case FunctionCode:
		return "Function"
	case ModuleCode:
		return "Module"
	}
	return "?"
}

// CodeGenerationMode is a set of instrumentation flags (OptionSet).
type CodeGenerationMode uint8

const (
	CodeGenerationModeDebugger CodeGenerationMode = 1 << iota
	CodeGenerationModeTypeProfiler
	CodeGenerationModeControlFlowProfiler
)

func (m CodeGenerationMode) Contains(f CodeGenerationMode) bool { return m&f == f }

// CodeSpecializationKind says whether code runs for [[Call]] or [[Construct]].
type CodeSpecializationKind uint8

const (
	CodeForCall CodeSpecializationKind = iota
	CodeForConstruct
)

// ExecutableInfo carries what a code block needs to know about its
// executable before code generation.
type ExecutableInfo struct {
	IsConstructor                    bool
	PrivateBrandRequirement          parser.PrivateBrandRequirement
	IsBuiltinFunction                bool
	ConstructorKind                  parser.ConstructorKind
	ScriptMode                       parser.ScriptMode
	SuperBinding                     parser.SuperBinding
	ParseMode                        parser.SourceParseMode
	DerivedContextType               parser.DerivedContextType
	NeedsClassFieldInitializer       bool
	IsArrowFunctionContext           bool
	IsClassContext                   bool
	EvalContextType                  parser.EvalContextType
	IsBuiltinDefaultClassConstructor bool
}

// HandlerType is the kind of an exception handler.
type HandlerType uint8

const (
	HandlerCatch HandlerType = iota
	HandlerFinally
	HandlerSynthesizedCatch
	HandlerSynthesizedFinally
)

func (t HandlerType) String() string {
	switch t {
	case HandlerCatch:
		return "catch"
	case HandlerFinally:
		return "finally"
	case HandlerSynthesizedCatch:
		return "synthesized catch"
	case HandlerSynthesizedFinally:
		return "synthesized finally"
	}
	return "?"
}

// HandlerInfo is UnlinkedHandlerInfo: [Start, End) is the protected range of
// instruction offsets, Target the offset of the op_catch.
type HandlerInfo struct {
	Start, End, Target uint32
	Type               HandlerType
}

func (h *HandlerInfo) IsCatchHandler() bool { return h.Type == HandlerCatch }

// RequiredHandler restricts handler lookup.
type RequiredHandler uint8

const (
	CatchHandler RequiredHandler = iota
	AnyHandler
)

// SimpleJumpTable is UnlinkedSimpleJumpTable (op_switch_imm / op_switch_char).
// Offsets are relative to the switch instruction; 0 means "use default".
type SimpleJumpTable struct {
	BranchOffsets []int32
	Min           int32
	DefaultOffset int32
	IsList        bool // sparse table of (key, offset) pairs
}

func (t *SimpleJumpTable) OffsetForValue(value int32) int32 {
	if t.IsList {
		for i := 0; i+1 < len(t.BranchOffsets); i += 2 {
			if t.BranchOffsets[i] == value {
				return t.BranchOffsets[i+1]
			}
		}
		return t.DefaultOffset
	}
	if value >= t.Min && uint32(value-t.Min) < uint32(len(t.BranchOffsets)) {
		if off := t.BranchOffsets[value-t.Min]; off != 0 {
			return off
		}
	}
	return t.DefaultOffset
}

func (t *SimpleJumpTable) Add(key, offset int32) {
	if t.BranchOffsets[key] == 0 {
		t.BranchOffsets[key] = offset
	}
}

// StringJumpTable is UnlinkedStringJumpTable (op_switch_string).
type StringJumpTable struct {
	OffsetTable   map[string]StringJumpTableEntry
	MinLength     uint32
	MaxLength     uint32
	DefaultOffset int32
}

type StringJumpTableEntry struct {
	BranchOffset int32
	IndexInTable uint32
}

func (t *StringJumpTable) OffsetForValue(value string) int32 {
	if loc, ok := t.OffsetTable[value]; ok {
		return loc.BranchOffset
	}
	return t.DefaultOffset
}

// ExpressionInfoEntry maps an instruction to the source range of the
// expression it evaluates: the divot (error position) and the start and end
// of the expression as offsets relative to the divot.
type ExpressionInfoEntry struct {
	InstPC      uint32
	Divot       uint32
	StartOffset uint32
	EndOffset   uint32
}

// MetadataTable counts per-opcode metadata entries (JSC's
// UnlinkedMetadataTable). The interpreter allocates the real metadata
// (inline caches, profiles) when it links the code block.
type MetadataTable struct {
	Counts           [NumberOfBytecodeIDs]uint32
	NumValueProfiles uint32
}

// AddEntry reserves a metadata entry for op and returns its index.
func (m *MetadataTable) AddEntry(op OpcodeID) uint32 {
	id := m.Counts[op]
	m.Counts[op]++
	return id
}

// AddValueProfile reserves a value profile and returns its index.
func (m *MetadataTable) AddValueProfile() uint32 {
	id := m.NumValueProfiles
	m.NumValueProfiles++
	return id
}

// UnlinkedCodeBlock is compiled bytecode that is not yet tied to a global
// object.
type UnlinkedCodeBlock struct {
	CodeType           CodeType
	Info               ExecutableInfo
	CodeGenerationMode CodeGenerationMode

	Instructions []uint32

	ThisRegister    VirtualRegister
	ScopeRegister   VirtualRegister
	NumVars         int
	NumCalleeLocals int
	NumParameters   int

	HasTailCalls   bool
	HasCheckpoints bool

	// Constant pools.
	Identifiers               []ident.Identifier
	Constants                 []Constant
	ConstantsRepresentation   []SourceCodeRepresentation
	FunctionDecls             []*UnlinkedFunctionExecutable
	FunctionExprs             []*UnlinkedFunctionExecutable
	BitVectors                [][]bool
	ConstantIdentifierSets    [][]ident.Identifier
	ExceptionHandlers         []HandlerInfo
	SwitchJumpTables          []SimpleJumpTable
	StringSwitchJumpTables    []StringJumpTable
	ExpressionInfo            []ExpressionInfoEntry
	OpProfileControlFlowPCs   []uint32
	TypeProfilerExpressionMap map[uint32][2]uint32

	Metadata               MetadataTable
	NumBinaryArithProfiles uint32
	NumUnaryArithProfiles  uint32

	// Linked is the VM's code block linked from this one for the first
	// realm that links it. Keeping it here instead of in a per-realm table
	// lets both be collected together once unused (eval code).
	Linked any

	// Parse results recorded on global code blocks (UnlinkedGlobalCodeBlock).
	Features                  parser.CodeFeatures
	LexicallyScopedFeatures   parser.LexicallyScopedFeatures
	HasCapturedVariables      bool
	SourceURLDirective        string
	SourceMappingURLDirective string

	// UnlinkedProgramCodeBlock / UnlinkedModuleProgramCodeBlock.
	VarDeclarations                                    parser.VariableEnvironment
	LexicalDeclarations                                parser.VariableEnvironment
	ModuleEnvironmentSymbolTableConstantRegisterOffset int

	// UnlinkedEvalCodeBlock.
	EvalVariables              []ident.Identifier
	FunctionHoistingCandidates []ident.Identifier
}

// NewUnlinkedCodeBlock creates an empty code block.
func NewUnlinkedCodeBlock(codeType CodeType, info ExecutableInfo, mode CodeGenerationMode) *UnlinkedCodeBlock {
	return &UnlinkedCodeBlock{
		CodeType:           codeType,
		Info:               info,
		CodeGenerationMode: mode,
		ThisRegister:       InvalidVirtualRegister,
		ScopeRegister:      InvalidVirtualRegister,
	}
}

func (cb *UnlinkedCodeBlock) IsConstructor() bool { return cb.Info.IsConstructor }
func (cb *UnlinkedCodeBlock) ParseMode() parser.SourceParseMode {
	return cb.Info.ParseMode
}
func (cb *UnlinkedCodeBlock) IsArrowFunction() bool {
	return cb.Info.ParseMode.IsArrowFunction()
}
func (cb *UnlinkedCodeBlock) IsBuiltinFunction() bool { return cb.Info.IsBuiltinFunction }

func (cb *UnlinkedCodeBlock) WasCompiledWithDebuggingOpcodes() bool {
	return cb.CodeGenerationMode.Contains(CodeGenerationModeDebugger)
}
func (cb *UnlinkedCodeBlock) WasCompiledWithTypeProfilerOpcodes() bool {
	return cb.CodeGenerationMode.Contains(CodeGenerationModeTypeProfiler)
}
func (cb *UnlinkedCodeBlock) WasCompiledWithControlFlowProfilerOpcodes() bool {
	return cb.CodeGenerationMode.Contains(CodeGenerationModeControlFlowProfiler)
}

// AddConstant appends c to the constant pool and returns its index.
func (cb *UnlinkedCodeBlock) AddConstant(c Constant, repr SourceCodeRepresentation) int {
	if _, ok := c.(LinkTimeConstant); ok {
		repr = SourceCodeRepresentationLinkTimeConstant
	}
	cb.Constants = append(cb.Constants, c)
	cb.ConstantsRepresentation = append(cb.ConstantsRepresentation, repr)
	return len(cb.Constants) - 1
}

// Constant returns the constant that reg (a constant register) refers to.
func (cb *UnlinkedCodeBlock) Constant(reg VirtualRegister) Constant {
	return cb.Constants[reg.ToConstantIndex()]
}

func (cb *UnlinkedCodeBlock) AddIdentifier(id ident.Identifier) int {
	cb.Identifiers = append(cb.Identifiers, id)
	return len(cb.Identifiers) - 1
}

func (cb *UnlinkedCodeBlock) AddFunctionDecl(f *UnlinkedFunctionExecutable) int {
	cb.FunctionDecls = append(cb.FunctionDecls, f)
	return len(cb.FunctionDecls) - 1
}

func (cb *UnlinkedCodeBlock) AddFunctionExpr(f *UnlinkedFunctionExecutable) int {
	cb.FunctionExprs = append(cb.FunctionExprs, f)
	return len(cb.FunctionExprs) - 1
}

func (cb *UnlinkedCodeBlock) AddBitVector(bv []bool) int {
	cb.BitVectors = append(cb.BitVectors, bv)
	return len(cb.BitVectors) - 1
}

func (cb *UnlinkedCodeBlock) AddSetConstant(set []ident.Identifier) int {
	cb.ConstantIdentifierSets = append(cb.ConstantIdentifierSets, set)
	return len(cb.ConstantIdentifierSets) - 1
}

func (cb *UnlinkedCodeBlock) AddExceptionHandler(h HandlerInfo) {
	cb.ExceptionHandlers = append(cb.ExceptionHandlers, h)
}

func (cb *UnlinkedCodeBlock) AddSwitchJumpTable() *SimpleJumpTable {
	cb.SwitchJumpTables = append(cb.SwitchJumpTables, SimpleJumpTable{})
	return &cb.SwitchJumpTables[len(cb.SwitchJumpTables)-1]
}

func (cb *UnlinkedCodeBlock) AddStringSwitchJumpTable() *StringJumpTable {
	cb.StringSwitchJumpTables = append(cb.StringSwitchJumpTables, StringJumpTable{
		OffsetTable: make(map[string]StringJumpTableEntry),
		MinLength:   ^uint32(0),
	})
	return &cb.StringSwitchJumpTables[len(cb.StringSwitchJumpTables)-1]
}

func (cb *UnlinkedCodeBlock) AddExpressionInfo(instPC, divot, startOffset, endOffset uint32) {
	cb.ExpressionInfo = append(cb.ExpressionInfo, ExpressionInfoEntry{instPC, divot, startOffset, endOffset})
}

func (cb *UnlinkedCodeBlock) AddTypeProfilerExpressionInfo(instPC, startDivot, endDivot uint32) {
	if cb.TypeProfilerExpressionMap == nil {
		cb.TypeProfilerExpressionMap = make(map[uint32][2]uint32)
	}
	cb.TypeProfilerExpressionMap[instPC] = [2]uint32{startDivot, endDivot}
}

// ExpressionInfoForInstPC returns the last entry at or before instPC.
func (cb *UnlinkedCodeBlock) ExpressionInfoForInstPC(instPC uint32) ExpressionInfoEntry {
	i := sort.Search(len(cb.ExpressionInfo), func(i int) bool { return cb.ExpressionInfo[i].InstPC > instPC })
	if i == 0 {
		return ExpressionInfoEntry{}
	}
	return cb.ExpressionInfo[i-1]
}

// HandlerForIndex returns the innermost handler covering the instruction at
// index, or nil.
func (cb *UnlinkedCodeBlock) HandlerForIndex(index uint32, required RequiredHandler) *HandlerInfo {
	for i := range cb.ExceptionHandlers {
		h := &cb.ExceptionHandlers[i]
		if required == CatchHandler && !h.IsCatchHandler() {
			continue
		}
		// Handlers are ordered innermost first.
		if h.Start <= index && h.End > index {
			return h
		}
	}
	return nil
}

// InstructionAt returns the instruction starting at offset.
func (cb *UnlinkedCodeBlock) InstructionAt(offset int) []uint32 {
	op := OpcodeID(cb.Instructions[offset])
	return cb.Instructions[offset : offset+op.Length()]
}

// TDZEnvironmentLink is a chain of sets of names that are in their temporal
// dead zone in enclosing scopes; nested functions inherit it.
type TDZEnvironmentLink struct {
	names  map[ident.Identifier]struct{}
	parent *TDZEnvironmentLink
}

func NewTDZEnvironmentLink(names []ident.Identifier, parent *TDZEnvironmentLink) *TDZEnvironmentLink {
	m := make(map[ident.Identifier]struct{}, len(names))
	for _, n := range names {
		m[n] = struct{}{}
	}
	return &TDZEnvironmentLink{names: m, parent: parent}
}

func (l *TDZEnvironmentLink) Parent() *TDZEnvironmentLink { return l.parent }

// Contains reports whether name is in this link (not its parents).
func (l *TDZEnvironmentLink) Contains(name ident.Identifier) bool {
	_, ok := l.names[name]
	return ok
}

// Names iterates the names of this link.
func (l *TDZEnvironmentLink) Names(yield func(ident.Identifier) bool) {
	for n := range l.names {
		if !yield(n) {
			return
		}
	}
}
