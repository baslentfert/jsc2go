package parser

// Port of JavaScriptCore/parser/ParserModes.h plus the small enums the parser
// shares with the rest of the engine (ConstructorKind, ImplementationVisibility, ...).

type SuperBinding uint8

const (
	SuperBindingNeeded SuperBinding = iota
	SuperBindingNotNeeded
)

type PrivateBrandRequirement uint8

const (
	PrivateBrandRequirementNone PrivateBrandRequirement = iota
	PrivateBrandRequirementNeeded
)

type FunctionMode uint8

const (
	FunctionModeNone FunctionMode = iota
	FunctionModeFunctionExpression
	FunctionModeFunctionDeclaration
	FunctionModeMethodDefinition
)

type ConstructorKind uint8

const (
	ConstructorKindNone ConstructorKind = iota
	ConstructorKindBase
	ConstructorKindExtends
	ConstructorKindNaked
)

// ImplementationVisibility controls whether a function shows up in stack traces.
type ImplementationVisibility uint8

const (
	ImplementationVisibilityPublic ImplementationVisibility = iota
	ImplementationVisibilityPrivate
	ImplementationVisibilityPrivateRecursive
)

type EvalContextType uint8

const (
	EvalContextTypeNone EvalContextType = iota
	EvalContextTypeFunctionEvalContext
	EvalContextTypeInstanceFieldEvalContext
)

type DerivedContextType uint8

const (
	DerivedContextTypeNone DerivedContextType = iota
	DerivedContextTypeDerivedConstructorContext
	DerivedContextTypeDerivedMethodContext
)

// SourceParseMode says what kind of code is being parsed. It must stay below
// 32 values so that sets of modes fit in a uint32.
type SourceParseMode uint8

const (
	NormalFunctionMode                SourceParseMode = 0
	GeneratorBodyMode                 SourceParseMode = 1
	GeneratorWrapperFunctionMode      SourceParseMode = 2
	GetterMode                        SourceParseMode = 3
	SetterMode                        SourceParseMode = 4
	MethodMode                        SourceParseMode = 5
	ArrowFunctionMode                 SourceParseMode = 6
	AsyncFunctionBodyMode             SourceParseMode = 7
	AsyncArrowFunctionBodyMode        SourceParseMode = 8
	AsyncFunctionMode                 SourceParseMode = 9
	AsyncMethodMode                   SourceParseMode = 10
	AsyncArrowFunctionMode            SourceParseMode = 11
	ProgramMode                       SourceParseMode = 12
	ModuleAnalyzeMode                 SourceParseMode = 13
	ModuleEvaluateMode                SourceParseMode = 14
	AsyncGeneratorBodyMode            SourceParseMode = 15
	AsyncGeneratorWrapperFunctionMode SourceParseMode = 16
	AsyncGeneratorWrapperMethodMode   SourceParseMode = 17
	GeneratorWrapperMethodMode        SourceParseMode = 18
	ClassFieldInitializerMode         SourceParseMode = 19
	ClassStaticBlockMode              SourceParseMode = 20
)

// modeSet is JSC's SourceParseModeSet: a bitmask of parse modes.
type modeSet uint32

func modes(ms ...SourceParseMode) modeSet {
	var s modeSet
	for _, m := range ms {
		s |= 1 << m
	}
	return s
}

func (s modeSet) contains(m SourceParseMode) bool { return s&(1<<m) != 0 }

var (
	functionParseModes = modes(NormalFunctionMode, GeneratorBodyMode, GeneratorWrapperFunctionMode,
		GeneratorWrapperMethodMode, GetterMode, SetterMode, MethodMode, ArrowFunctionMode,
		AsyncFunctionBodyMode, AsyncFunctionMode, AsyncMethodMode, AsyncArrowFunctionMode,
		AsyncArrowFunctionBodyMode, AsyncGeneratorBodyMode, AsyncGeneratorWrapperFunctionMode,
		AsyncGeneratorWrapperMethodMode, ClassFieldInitializerMode, ClassStaticBlockMode)
	asyncFunctionParseModes = modes(AsyncGeneratorWrapperFunctionMode, AsyncGeneratorBodyMode,
		AsyncGeneratorWrapperMethodMode, AsyncFunctionBodyMode, AsyncFunctionMode, AsyncMethodMode,
		AsyncArrowFunctionMode, AsyncArrowFunctionBodyMode)
	asyncArrowFunctionParseModes                   = modes(AsyncArrowFunctionMode, AsyncArrowFunctionBodyMode)
	asyncGeneratorParseModes                       = modes(AsyncGeneratorWrapperFunctionMode, AsyncGeneratorWrapperMethodMode, AsyncGeneratorBodyMode)
	asyncGeneratorWrapperParseModes                = modes(AsyncGeneratorWrapperFunctionMode, AsyncGeneratorWrapperMethodMode)
	asyncFunctionOrAsyncGeneratorWrapperParseModes = modes(AsyncArrowFunctionMode, AsyncFunctionMode,
		AsyncGeneratorWrapperFunctionMode, AsyncGeneratorWrapperMethodMode, AsyncMethodMode)
	asyncFunctionWrapperParseModes = modes(AsyncArrowFunctionMode, AsyncFunctionMode, AsyncMethodMode)
	asyncFunctionBodyParseModes    = modes(AsyncFunctionBodyMode, AsyncGeneratorBodyMode, AsyncArrowFunctionBodyMode)
	methodParseModes               = modes(GeneratorWrapperMethodMode, GetterMode, SetterMode, MethodMode,
		AsyncMethodMode, AsyncGeneratorWrapperMethodMode, ClassStaticBlockMode)
	generatorOrAsyncFunctionBodyParseModes = modes(GeneratorBodyMode, AsyncFunctionBodyMode,
		AsyncGeneratorBodyMode, AsyncArrowFunctionBodyMode)
	generatorOrAsyncFunctionWrapperParseModes = modes(GeneratorWrapperFunctionMode, GeneratorWrapperMethodMode,
		AsyncFunctionMode, AsyncArrowFunctionMode, AsyncGeneratorWrapperFunctionMode, AsyncMethodMode,
		AsyncGeneratorWrapperMethodMode)
	generatorOrAsyncGeneratorWrapperParseModes = modes(GeneratorWrapperFunctionMode, GeneratorWrapperMethodMode,
		AsyncGeneratorWrapperFunctionMode, AsyncGeneratorWrapperMethodMode)
	generatorParseModes        = modes(GeneratorBodyMode, GeneratorWrapperFunctionMode, GeneratorWrapperMethodMode)
	generatorWrapperParseModes = modes(GeneratorWrapperFunctionMode, GeneratorWrapperMethodMode)
	arrowFunctionParseModes    = modes(ArrowFunctionMode, AsyncArrowFunctionMode, AsyncArrowFunctionBodyMode)
	moduleParseModes           = modes(ModuleAnalyzeMode, ModuleEvaluateMode)
	programOrModuleParseModes  = modes(ProgramMode, ModuleAnalyzeMode, ModuleEvaluateMode)
)

func (m SourceParseMode) IsFunction() bool      { return functionParseModes.contains(m) }
func (m SourceParseMode) IsAsyncFunction() bool { return asyncFunctionParseModes.contains(m) }
func (m SourceParseMode) IsAsyncArrowFunction() bool {
	return asyncArrowFunctionParseModes.contains(m)
}
func (m SourceParseMode) IsAsyncGenerator() bool { return asyncGeneratorParseModes.contains(m) }
func (m SourceParseMode) IsAsyncGeneratorWrapper() bool {
	return asyncGeneratorWrapperParseModes.contains(m)
}
func (m SourceParseMode) IsAsyncFunctionOrAsyncGeneratorWrapper() bool {
	return asyncFunctionOrAsyncGeneratorWrapperParseModes.contains(m)
}
func (m SourceParseMode) IsAsyncFunctionWrapper() bool {
	return asyncFunctionWrapperParseModes.contains(m)
}
func (m SourceParseMode) IsAsyncFunctionBody() bool    { return asyncFunctionBodyParseModes.contains(m) }
func (m SourceParseMode) IsGeneratorMethod() bool      { return m == GeneratorWrapperMethodMode }
func (m SourceParseMode) IsAsyncMethod() bool          { return m == AsyncMethodMode }
func (m SourceParseMode) IsAsyncGeneratorMethod() bool { return m == AsyncGeneratorWrapperMethodMode }
func (m SourceParseMode) IsMethod() bool               { return methodParseModes.contains(m) }
func (m SourceParseMode) IsGeneratorOrAsyncFunctionBody() bool {
	return generatorOrAsyncFunctionBodyParseModes.contains(m)
}
func (m SourceParseMode) IsGeneratorOrAsyncFunctionWrapper() bool {
	return generatorOrAsyncFunctionWrapperParseModes.contains(m)
}
func (m SourceParseMode) IsGeneratorOrAsyncGeneratorWrapper() bool {
	return generatorOrAsyncGeneratorWrapperParseModes.contains(m)
}
func (m SourceParseMode) IsGenerator() bool        { return generatorParseModes.contains(m) }
func (m SourceParseMode) IsGeneratorWrapper() bool { return generatorWrapperParseModes.contains(m) }
func (m SourceParseMode) IsArrowFunction() bool    { return arrowFunctionParseModes.contains(m) }
func (m SourceParseMode) IsModule() bool           { return moduleParseModes.contains(m) }
func (m SourceParseMode) IsProgram() bool          { return m == ProgramMode }
func (m SourceParseMode) IsProgramOrModule() bool  { return programOrModuleParseModes.contains(m) }

// CanConstruct reports whether functions of this mode are constructors
// (constructAbilityForParseMode).
func (m SourceParseMode) CanConstruct() bool { return m == NormalFunctionMode }

func functionNameIsInScope(name identifierLike, mode FunctionMode) bool {
	return !name.IsNull() && mode == FunctionModeFunctionExpression
}

// FunctionNameScopeIsDynamic: with sloppy-mode eval, a function's name gets
// its own scope object so eval can declare and delete a shadowing binding.
func FunctionNameScopeIsDynamic(usesEval, isStrictMode bool) bool {
	return usesEval && !isStrictMode
}

type identifierLike interface{ IsNull() bool }

// LexicallyScopedFeatures are inherited by nested scopes (2 bits).
type LexicallyScopedFeatures uint8

const (
	NoLexicallyScopedFeatures                LexicallyScopedFeatures = 0
	StrictModeLexicallyScopedFeature         LexicallyScopedFeatures = 1 << 0
	TaintedByWithScopeLexicallyScopedFeature LexicallyScopedFeatures = 1 << 1
)

// CodeFeatures summarize what a ScopeNode's code uses (15 bits).
type CodeFeatures uint16

const (
	NoFeatures                       CodeFeatures = 0
	EvalFeature                      CodeFeatures = 1 << 0
	ArgumentsFeature                 CodeFeatures = 1 << 1
	WithFeature                      CodeFeatures = 1 << 2
	ThisFeature                      CodeFeatures = 1 << 3
	NonSimpleParameterListFeature    CodeFeatures = 1 << 4
	ShadowsArgumentsFeature          CodeFeatures = 1 << 5
	ArrowFunctionFeature             CodeFeatures = 1 << 6
	AwaitFeature                     CodeFeatures = 1 << 7
	SuperCallFeature                 CodeFeatures = 1 << 8
	SuperPropertyFeature             CodeFeatures = 1 << 9
	NewTargetFeature                 CodeFeatures = 1 << 10
	NoEvalCacheFeature               CodeFeatures = 1 << 11
	ImportMetaFeature                CodeFeatures = 1 << 12
	AsyncFunctionWithoutAwaitFeature CodeFeatures = 1 << 13
	// ArgumentsOnlyForwardedFeature: every use of the function's
	// arguments is the second argument of x.apply(y, arguments), which
	// the bytecode passes on from the call frame without an arguments
	// object when apply is Function.prototype.apply (jsc2go's own; JSC's
	// DFG eliminates such objects instead).
	ArgumentsOnlyForwardedFeature CodeFeatures = 1 << 14
)

// InnerArrowFunctionCodeFeatures record what nested arrow functions use from
// their enclosing function (6 bits).
type InnerArrowFunctionCodeFeatures uint8

const (
	NoInnerArrowFunctionFeatures           InnerArrowFunctionCodeFeatures = 0
	EvalInnerArrowFunctionFeature          InnerArrowFunctionCodeFeatures = 1 << 0
	ArgumentsInnerArrowFunctionFeature     InnerArrowFunctionCodeFeatures = 1 << 1
	ThisInnerArrowFunctionFeature          InnerArrowFunctionCodeFeatures = 1 << 2
	SuperCallInnerArrowFunctionFeature     InnerArrowFunctionCodeFeatures = 1 << 3
	SuperPropertyInnerArrowFunctionFeature InnerArrowFunctionCodeFeatures = 1 << 4
	NewTargetInnerArrowFunctionFeature     InnerArrowFunctionCodeFeatures = 1 << 5
)

// Options holds the runtime flags (JSC Options) that affect parsing.
type Options struct {
	UseExplicitResourceManagement bool // `using` and `await using` declarations
	UseImportDefer                bool // `import defer` and `import.defer()`
}

// DefaultOptions are used when a parse call does not supply its own.
var DefaultOptions = Options{UseExplicitResourceManagement: true}
