package bytecompiler

import (
	"github.com/baslentfert/jsc2go/bytecode"
	"github.com/baslentfert/jsc2go/ident"
	"github.com/baslentfert/jsc2go/parser"
)

// Entry points: parsing plus code generation for each kind of code
// (CodeCache.cpp's generateUnlinkedCodeBlockImpl and
// UnlinkedFunctionExecutable.cpp's generateUnlinkedFunctionCodeBlock).

func recordGlobalParse(cb *bytecode.UnlinkedCodeBlock, s *parser.ScopeNode, source parser.SourceCode) {
	cb.Features = s.Features
	cb.LexicallyScopedFeatures = s.LexicallyScopedFeatures
	cb.HasCapturedVariables = s.HasCapturedVariables()
	if p := source.Provider; p != nil {
		cb.SourceURLDirective = p.SourceURLDirective
		cb.SourceMappingURLDirective = p.SourceMappingURLDirective
	}
}

func globalExecutableInfo(parseMode parser.SourceParseMode, scriptMode parser.ScriptMode) bytecode.ExecutableInfo {
	return bytecode.ExecutableInfo{
		ConstructorKind: parser.ConstructorKindNone,
		ScriptMode:      scriptMode,
		SuperBinding:    parser.SuperBindingNotNeeded,
		ParseMode:       parseMode,
	}
}

// CompileProgram parses and compiles a classic script.
func CompileProgram(source parser.SourceCode, features parser.LexicallyScopedFeatures, mode bytecode.CodeGenerationMode) (*bytecode.UnlinkedCodeBlock, *Error) {
	program, err := parser.ParseProgram(source, features)
	if err != nil {
		return nil, err
	}
	cb := bytecode.NewUnlinkedCodeBlock(bytecode.GlobalCode, globalExecutableInfo(parser.ProgramMode, parser.ScriptModeClassic), mode)
	recordGlobalParse(cb, &program.ScopeNode, source)
	if err := Generate(program, cb, mode, nil, nil, nil); err != nil {
		return nil, err
	}
	return cb, nil
}

// CompileModule parses and compiles module code.
func CompileModule(source parser.SourceCode, mode bytecode.CodeGenerationMode) (*bytecode.UnlinkedCodeBlock, *Error) {
	module, err := parser.ParseModule(source, parser.ModuleEvaluateMode)
	if err != nil {
		return nil, err
	}
	cb := bytecode.NewUnlinkedCodeBlock(bytecode.ModuleCode, globalExecutableInfo(parser.ModuleEvaluateMode, parser.ScriptModeModule), mode)
	recordGlobalParse(cb, &module.ScopeNode, source)
	if err := Generate(module, cb, mode, nil, nil, nil); err != nil {
		return nil, err
	}
	return cb, nil
}

// EvalConfig describes the context of eval code.
type EvalConfig struct {
	LexicallyScopedFeatures    parser.LexicallyScopedFeatures
	EvalContextType            parser.EvalContextType
	DerivedContextType         parser.DerivedContextType
	IsArrowFunctionContext     bool
	NeedsClassFieldInitializer bool
	PrivateBrandRequirement    parser.PrivateBrandRequirement
	// VariablesUnderTDZ are the names in their TDZ at a direct eval.
	VariablesUnderTDZ []ident.Identifier
	// PrivateNames are the private names visible at a direct eval.
	PrivateNames *parser.PrivateNameEnvironment
	// IsInsideOrdinaryFunction is set for direct eval in a non-arrow function.
	IsInsideOrdinaryFunction bool
}

// CompileEval parses and compiles eval code.
func CompileEval(source parser.SourceCode, cfg EvalConfig, mode bytecode.CodeGenerationMode) (*bytecode.UnlinkedCodeBlock, *Error) {
	evalNode, err := parser.ParseEval(source, parser.ParseConfig{
		LexicallyScopedFeatures:  cfg.LexicallyScopedFeatures,
		SuperBinding:             parser.SuperBindingNotNeeded,
		DerivedContextType:       cfg.DerivedContextType,
		EvalContextType:          cfg.EvalContextType,
		ParentScopePrivateNames:  cfg.PrivateNames,
		IsInsideOrdinaryFunction: cfg.IsInsideOrdinaryFunction,
	})
	if err != nil {
		return nil, err
	}
	info := globalExecutableInfo(parser.ProgramMode, parser.ScriptModeClassic)
	info.DerivedContextType = cfg.DerivedContextType
	info.IsArrowFunctionContext = cfg.IsArrowFunctionContext
	info.EvalContextType = cfg.EvalContextType
	info.NeedsClassFieldInitializer = cfg.NeedsClassFieldInitializer
	info.PrivateBrandRequirement = cfg.PrivateBrandRequirement
	cb := bytecode.NewUnlinkedCodeBlock(bytecode.EvalCode, info, mode)
	recordGlobalParse(cb, &evalNode.ScopeNode, source)

	var parentTDZ *bytecode.TDZEnvironmentLink
	if cfg.VariablesUnderTDZ != nil {
		parentTDZ = bytecode.NewTDZEnvironmentLink(cfg.VariablesUnderTDZ, nil)
	}
	if err := Generate(evalNode, cb, mode, parentTDZ, nil, cfg.PrivateNames); err != nil {
		return nil, err
	}
	return cb, nil
}

// CodeBlockForFunction returns the compiled body of fn for kind, compiling
// it on first use.
func CodeBlockForFunction(fn *bytecode.UnlinkedFunctionExecutable, kind bytecode.CodeSpecializationKind, mode bytecode.CodeGenerationMode) (*bytecode.UnlinkedCodeBlock, *Error) {
	cached := &fn.CodeBlockForCall
	if kind == bytecode.CodeForConstruct {
		cached = &fn.CodeBlockForConstruct
	}
	if *cached != nil {
		return *cached, nil
	}

	builtinMode := parser.BuiltinModeNotBuiltin
	if fn.IsBuiltinFunction {
		builtinMode = parser.BuiltinModeBuiltin
	}
	function, err := parser.ParseFunction(fn.Source, parser.ParseConfig{
		Name:                     fn.Name(),
		ImplementationVisibility: fn.ImplementationVisibility,
		BuiltinMode:              builtinMode,
		LexicallyScopedFeatures:  fn.LexicallyScopedFeatures,
		ScriptMode:               fn.ScriptMode,
		ParseMode:                fn.ParseMode,
		FunctionMode:             fn.FunctionMode,
		SuperBinding:             fn.SuperBinding,
		ConstructorKind:          fn.ConstructorKind,
		DerivedContextType:       fn.DerivedContextType,
		EvalContextType:          parser.EvalContextTypeNone,
		ClassElementDefinitions:  fn.ClassElementDefinitions,
	})
	if err != nil {
		return nil, err
	}
	function.FinishParsing(fn.Name(), fn.FunctionMode)
	fn.RecordParse(function.Features, function.LexicallyScopedFeatures, function.HasCapturedVariables())

	isClassContext := fn.SuperBinding == parser.SuperBindingNeeded || fn.ParseMode == parser.ClassFieldInitializerMode
	cb := bytecode.NewUnlinkedCodeBlock(bytecode.FunctionCode, bytecode.ExecutableInfo{
		IsConstructor:                    kind == bytecode.CodeForConstruct,
		PrivateBrandRequirement:          fn.PrivateBrandRequirement,
		IsBuiltinFunction:                fn.IsBuiltinFunction,
		ConstructorKind:                  fn.ConstructorKind,
		ScriptMode:                       fn.ScriptMode,
		SuperBinding:                     fn.SuperBinding,
		ParseMode:                        fn.ParseMode,
		DerivedContextType:               fn.DerivedContextType,
		NeedsClassFieldInitializer:       fn.NeedsClassFieldInitializer,
		IsClassContext:                   isClassContext,
		EvalContextType:                  fn.EvalContextType,
		IsBuiltinDefaultClassConstructor: fn.IsBuiltinDefaultClassConstructor,
	}, mode)
	if err := Generate(function, cb, mode, fn.ParentScopeTDZVariables, fn.GeneratorOrAsyncWrapperFunctionParameterNames, fn.ParentPrivateNameEnvironment); err != nil {
		return nil, err
	}
	*cached = cb
	return cb, nil
}

// CompileModuleWithInfo parses, analyzes and compiles module code. The error
// is a *Error (syntax) or a *parser.ModuleAnalysisError.
func CompileModuleWithInfo(source parser.SourceCode, mode bytecode.CodeGenerationMode) (*bytecode.UnlinkedCodeBlock, *parser.ModuleInfo, error) {
	module, err := parser.ParseModule(source, parser.ModuleEvaluateMode)
	if err != nil {
		return nil, nil, err
	}
	info, aerr := parser.AnalyzeModule(module)
	if aerr != nil {
		return nil, nil, aerr
	}
	cb := bytecode.NewUnlinkedCodeBlock(bytecode.ModuleCode, globalExecutableInfo(parser.ModuleEvaluateMode, parser.ScriptModeModule), mode)
	recordGlobalParse(cb, &module.ScopeNode, source)
	if err := Generate(module, cb, mode, nil, nil, nil); err != nil {
		return nil, nil, err
	}
	return cb, info, nil
}
