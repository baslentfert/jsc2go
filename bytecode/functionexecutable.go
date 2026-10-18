package bytecode

import (
	"github.com/baslentfert/jsc2go/ident"
	"github.com/baslentfert/jsc2go/parser"
)

// Port of JavaScriptCore/bytecode/UnlinkedFunctionExecutable.{h,cpp}.
//
// An UnlinkedFunctionExecutable describes a function found while generating
// the enclosing code. Its body is reparsed and compiled on first call; the
// compiled code blocks are cached here. Compilation lives in package
// bytecompiler (which depends on this package), see
// bytecompiler.UnlinkedCodeBlockFor.

// ConstructAbility says whether a function can be called with new.
type ConstructAbility uint8

const (
	CanConstruct ConstructAbility = iota
	CannotConstruct
)

// InlineAttribute is a JIT inlining hint; kept for builtins.
type InlineAttribute uint8

const (
	InlineAttributeNone InlineAttribute = iota
	InlineAttributeAlways
)

// UnlinkedFunctionKind distinguishes builtins from user functions.
type UnlinkedFunctionKind uint8

const (
	UnlinkedNormalFunction UnlinkedFunctionKind = iota
	UnlinkedBuiltinFunction
)

type UnlinkedFunctionExecutable struct {
	HasCapturedVariables bool
	// UnlinkedFunctionStart/End are absolute source offsets of the function
	// text (for Function.prototype.toString).
	UnlinkedFunctionStart int
	UnlinkedFunctionEnd   int
	// StartOffset is the offset of the body source relative to the parent
	// source; SourceLength its length.
	StartOffset           int
	SourceLength          int
	ParametersStartOffset int
	ParameterCount        int // excluding this

	// Source is the function's text from its parameters through the closing
	// brace. JSC recomputes it from the parent source (StartOffset) because
	// its code cache shares executables between sources; without a code
	// cache we keep it directly.
	Source parser.SourceCode

	ConstructAbility                 ConstructAbility
	ScriptMode                       parser.ScriptMode
	SuperBinding                     parser.SuperBinding
	NeedsClassFieldInitializer       bool
	SingletonHasBeenInvalidated      bool
	IsBuiltinFunction                bool
	IsBuiltinDefaultClassConstructor bool
	PrivateBrandRequirement          parser.PrivateBrandRequirement
	Features                         parser.CodeFeatures
	ConstructorKind                  parser.ConstructorKind
	ParseMode                        parser.SourceParseMode
	ImplementationVisibility         parser.ImplementationVisibility
	LexicallyScopedFeatures          parser.LexicallyScopedFeatures
	FunctionMode                     parser.FunctionMode
	DerivedContextType               parser.DerivedContextType
	InlineAttribute                  InlineAttribute
	EvalContextType                  parser.EvalContextType
	hasName                          bool

	ecmaName ident.Identifier
	// NameString is the VM's string for the name property of functions
	// made from this executable (vm.newJSFunction), shared by all of them.
	NameString any

	// CodeBlockForCall/Construct cache the compiled bodies.
	CodeBlockForCall      *UnlinkedCodeBlock
	CodeBlockForConstruct *UnlinkedCodeBlock

	ParentScopeTDZVariables *TDZEnvironmentLink

	// Rare data.
	ClassSource                                   parser.SourceCode
	SourceURLDirective                            string
	SourceMappingURLDirective                     string
	GeneratorOrAsyncWrapperFunctionParameterNames []ident.Identifier
	ClassElementDefinitions                       []parser.ClassElementDefinition
	ParentPrivateNameEnvironment                  *parser.PrivateNameEnvironment
}

// UnlinkedFunctionExecutableConfig holds the creation arguments that do not
// come from the FunctionMetadataNode.
type UnlinkedFunctionExecutableConfig struct {
	Kind                                          UnlinkedFunctionKind
	ConstructAbility                              ConstructAbility
	InlineAttribute                               InlineAttribute
	ScriptMode                                    parser.ScriptMode
	ParentScopeTDZVariables                       *TDZEnvironmentLink
	GeneratorOrAsyncWrapperFunctionParameterNames []ident.Identifier
	ParentPrivateNameEnvironment                  *parser.PrivateNameEnvironment
	DerivedContextType                            parser.DerivedContextType
	EvalContextType                               parser.EvalContextType
	NeedsClassFieldInitializer                    bool
	PrivateBrandRequirement                       parser.PrivateBrandRequirement
	IsBuiltinDefaultClassConstructor              bool
}

// NewUnlinkedFunctionExecutable creates the executable for the function
// described by node, which was found in parentSource.
func NewUnlinkedFunctionExecutable(parentSource parser.SourceCode, node *parser.FunctionMetadataNode, cfg UnlinkedFunctionExecutableConfig) *UnlinkedFunctionExecutable {
	e := &UnlinkedFunctionExecutable{
		UnlinkedFunctionStart:            node.FunctionStart,
		ConstructAbility:                 cfg.ConstructAbility,
		StartOffset:                      node.Source.StartOffset - parentSource.StartOffset,
		ScriptMode:                       cfg.ScriptMode,
		SourceLength:                     node.Source.Length(),
		SuperBinding:                     node.SuperBinding,
		ParametersStartOffset:            node.ParametersStart,
		UnlinkedFunctionEnd:              node.StartStartOffset + node.Source.Length() - 1,
		NeedsClassFieldInitializer:       cfg.NeedsClassFieldInitializer,
		ParameterCount:                   node.ParameterCount,
		IsBuiltinFunction:                cfg.Kind == UnlinkedBuiltinFunction,
		IsBuiltinDefaultClassConstructor: cfg.IsBuiltinDefaultClassConstructor,
		PrivateBrandRequirement:          cfg.PrivateBrandRequirement,
		ConstructorKind:                  node.ConstructorKind,
		ParseMode:                        node.ParseMode,
		ImplementationVisibility:         node.ImplementationVisibility,
		LexicallyScopedFeatures:          node.LexicallyScopedFeatures,
		FunctionMode:                     node.FunctionMode,
		DerivedContextType:               cfg.DerivedContextType,
		InlineAttribute:                  cfg.InlineAttribute,
		EvalContextType:                  cfg.EvalContextType,
		hasName:                          !node.Ident.IsNull(),
		ecmaName:                         node.EcmaName(),
		ParentScopeTDZVariables:          cfg.ParentScopeTDZVariables,
		Source:                           node.Source,
		ClassSource:                      node.ClassSource,
		GeneratorOrAsyncWrapperFunctionParameterNames: cfg.GeneratorOrAsyncWrapperFunctionParameterNames,
		ParentPrivateNameEnvironment:                  cfg.ParentPrivateNameEnvironment,
	}
	return e
}

// Name is the function's own name, or the null identifier.
func (e *UnlinkedFunctionExecutable) Name() ident.Identifier {
	if e.hasName {
		return e.ecmaName
	}
	return ident.Null
}

// EcmaName is the name used for the function's name property.
func (e *UnlinkedFunctionExecutable) EcmaName() ident.Identifier { return e.ecmaName }

func (e *UnlinkedFunctionExecutable) SetEcmaName(name ident.Identifier) {
	e.ecmaName = name
	e.NameString = nil
}

func (e *UnlinkedFunctionExecutable) IsInStrictContext() bool {
	return e.LexicallyScopedFeatures&parser.StrictModeLexicallyScopedFeature != 0
}

func (e *UnlinkedFunctionExecutable) IsArrowFunction() bool { return e.ParseMode.IsArrowFunction() }

func (e *UnlinkedFunctionExecutable) IsClassConstructorFunction() bool {
	switch e.ConstructorKind {
	case parser.ConstructorKindBase, parser.ConstructorKindExtends:
		return true
	}
	return false
}

func (e *UnlinkedFunctionExecutable) IsClass() bool { return !e.ClassSource.IsNull() }

// RecordParse stores what the reparse of the body found.
func (e *UnlinkedFunctionExecutable) RecordParse(features parser.CodeFeatures, lexicallyScopedFeatures parser.LexicallyScopedFeatures, hasCapturedVariables bool) {
	e.Features = features
	e.LexicallyScopedFeatures = lexicallyScopedFeatures
	e.HasCapturedVariables = hasCapturedVariables
}

// LinkedSourceCode returns the function's source within parentSource.
func (e *UnlinkedFunctionExecutable) LinkedSourceCode(parentSource parser.SourceCode) parser.SourceCode {
	start := parentSource.StartOffset + e.StartOffset
	return parser.SourceCode{Provider: parentSource.Provider, StartOffset: start, EndOffset: start + e.SourceLength}
}
