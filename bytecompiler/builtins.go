package bytecompiler

import (
	"github.com/baslentfert/jsc2go/bytecode"
	"github.com/baslentfert/jsc2go/ident"
	"github.com/baslentfert/jsc2go/parser"
)

// The default class constructors (BuiltinExecutables::createDefaultConstructor).
// Their bodies are compiled from fixed source text like any function.

var (
	baseConstructorSource    = newBuiltinSource("(function () { })")
	derivedConstructorSource = newBuiltinSource("(function (...args) { super(...args); })")
)

func newBuiltinSource(text string) parser.SourceCode {
	return parser.NewSourceCode(parser.NewSourceProvider(text, "", parser.SourceTypeProgram))
}

// DefaultConstructorSourceCode is the source of the default constructor of
// a base (Base) or derived (Extends) class.
func DefaultConstructorSourceCode(kind parser.ConstructorKind) parser.SourceCode {
	if kind == parser.ConstructorKindExtends {
		return derivedConstructorSource
	}
	return baseConstructorSource
}

// createDefaultConstructor builds the executable of a default constructor.
// The metadata is computed the way BuiltinExecutables does it for builtin
// source text "(function (params) { body })".
func createDefaultConstructor(kind parser.ConstructorKind, name ident.Identifier, needsClassFieldInitializer bool, privateBrandRequirement parser.PrivateBrandRequirement) *bytecode.UnlinkedFunctionExecutable {
	source := DefaultConstructorSourceCode(kind)
	text := source.String()

	const functionKeywordStart = len("(")
	const parametersStart = len("function (")
	parameterCount := 0 // "()" and "(...args)" both have no formal parameters
	closeBraceOffsetFromEnd := 1
	for text[len(text)-closeBraceOffsetFromEnd] != '}' {
		closeBraceOffsetFromEnd++
	}

	superBinding := parser.SuperBindingNotNeeded
	if kind == parser.ConstructorKindExtends {
		superBinding = parser.SuperBindingNeeded
	}
	metadata := &parser.FunctionMetadataNode{
		ImplementationVisibility: parser.ImplementationVisibilityPublic,
		LexicallyScopedFeatures:  parser.NoLexicallyScopedFeatures,
		ConstructorKind:          kind,
		SuperBinding:             superBinding,
		ParseMode:                parser.NormalFunctionMode,
		FunctionStart:            source.StartOffset + functionKeywordStart,
		FunctionNameStart:        source.StartOffset + parametersStart,
		ParametersStart:          source.StartOffset + parametersStart,
		StartStartOffset:         source.StartOffset + parametersStart,
		ParameterCount:           parameterCount,
	}
	body := source.SubExpression(source.StartOffset+parametersStart, source.StartOffset+len(text)-closeBraceOffsetFromEnd)
	metadata.FinishParsing(body, ident.Null, parser.FunctionModeFunctionExpression)
	metadata.Ident = name // overrideName

	return bytecode.NewUnlinkedFunctionExecutable(source, metadata, bytecode.UnlinkedFunctionExecutableConfig{
		Kind:                             bytecode.UnlinkedNormalFunction,
		ConstructAbility:                 bytecode.CanConstruct,
		InlineAttribute:                  bytecode.InlineAttributeAlways,
		ScriptMode:                       parser.ScriptModeClassic,
		DerivedContextType:               parser.DerivedContextTypeNone,
		EvalContextType:                  parser.EvalContextTypeFunctionEvalContext,
		NeedsClassFieldInitializer:       needsClassFieldInitializer,
		PrivateBrandRequirement:          privateBrandRequirement,
		IsBuiltinDefaultClassConstructor: true,
	})
}
