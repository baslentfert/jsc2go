package parser

import (
	"slices"

	"github.com/baslentfert/jsc2go/ident"
)

// Port of the Scope struct from JavaScriptCore/parser/Parser.h. A Scope is
// pushed for every function, block, catch clause, class and so on, and tracks
// declarations, used variables and captured variables.

// DeclarationResult is a bit mask describing what was wrong with a declaration.
type DeclarationResult uint8

const (
	DeclarationValid                         DeclarationResult = 0
	DeclarationInvalidStrictMode             DeclarationResult = 1 << 0
	DeclarationInvalidDuplicateDeclaration   DeclarationResult = 1 << 1
	DeclarationInvalidPrivateStaticNonStatic DeclarationResult = 1 << 2
)

type DeclarationType uint8

const (
	VarDeclaration DeclarationType = iota
	LetDeclaration
	ConstDeclaration
	UsingDeclaration
	AwaitUsingDeclaration
)

type DeclarationImportType uint8

const (
	DeclarationImported DeclarationImportType = iota
	DeclarationImportedNamespace
	DeclarationNotImported
)

type scopeLabelInfo struct {
	label  ident.Identifier
	isLoop bool
}

type needsDuplicateDeclarationCheck bool

func isEvalOrArgumentsIdentifier(id ident.Identifier) bool {
	return id == ident.Eval || id == ident.Arguments
}

type Scope struct {
	containingScope      *Scope
	functionDeclarations FunctionStack

	shadowsArguments                 bool
	usesEval                         bool
	usesImportMeta                   bool
	needsFullActivation              bool
	hasDirectSuper                   bool
	needsSuperBinding                bool
	allowsVarDeclarations            bool
	allowsLexicalDeclarations        bool
	isFunction                       bool
	isGeneratorFunction              bool
	isGeneratorFunctionBoundary      bool
	isArrowFunction                  bool
	isArrowFunctionBoundary          bool
	isAsyncFunction                  bool
	isAsyncFunctionBoundary          bool
	isLexicalScope                   bool
	isGlobalCode                     bool
	isModuleCode                     bool
	isSimpleCatchParameterScope      bool
	isCatchBlockScope                bool
	isStaticBlock                    bool
	isStaticBlockBoundary            bool
	isFunctionBoundary               bool
	isValidStrictMode                bool
	hasArguments                     bool
	isEvalContext                    bool
	hasNonSimpleParameterList        bool
	isClassScope                     bool
	asyncFunctionBodyDoesNotUseAwait bool
	usesAwait                        bool

	loopDepth                  int
	switchDepth                int
	expectedSuperBinding       SuperBinding
	implementationVisibility   ImplementationVisibility
	lexicallyScopedFeatures    LexicallyScopedFeatures
	constructorKind            ConstructorKind
	innerArrowFunctionFeatures InnerArrowFunctionCodeFeatures

	sloppyModeFunctionHoistingCandidates orderedMap[sloppyCandidate]
	closedVariableCandidates             identSet

	lexicalVariables      VariableEnvironment
	declaredVariables     VariableEnvironment
	declaredParameters    identSet
	variablesBeingHoisted identSet
	labels                []scopeLabelInfo
	evalContextType       EvalContextType
	derivedContextType    DerivedContextType

	lastAddedUsedVariable ident.Identifier
	usedVariables         []*identSet
}

// sloppyCandidate is a function subject to Annex B.3.3 hoisting. The map is
// keyed by the function's name only to keep insertion order; the metadata
// pointer is the real identity, so names may repeat across entries.
type sloppyCandidate struct {
	metadata   *FunctionMetadataNode
	needsCheck needsDuplicateDeclarationCheck
}

func newScope(containing *Scope, visibility ImplementationVisibility, features LexicallyScopedFeatures, isFunction, isGeneratorFunction, isArrowFunction, isAsyncFunction, isStaticBlock bool) *Scope {
	return &Scope{
		containingScope:           containing,
		isFunction:                isFunction,
		isGeneratorFunction:       isGeneratorFunction,
		isArrowFunction:           isArrowFunction,
		isAsyncFunction:           isAsyncFunction,
		isStaticBlock:             isStaticBlock,
		implementationVisibility:  visibility,
		lexicallyScopedFeatures:   features,
		allowsVarDeclarations:     true,
		allowsLexicalDeclarations: true,
		isValidStrictMode:         true,
		expectedSuperBinding:      SuperBindingNotNeeded,
		usedVariables:             []*identSet{{}},
	}
}

func (s *Scope) ImplementationVisibility() ImplementationVisibility {
	return s.implementationVisibility
}
func (s *Scope) resetImplementationVisibility() {
	s.implementationVisibility = ImplementationVisibilityPublic
}
func (s *Scope) setImplementationVisibility(v ImplementationVisibility) {
	s.implementationVisibility = v
}

func (s *Scope) startSwitch()          { s.switchDepth++ }
func (s *Scope) endSwitch()            { s.switchDepth-- }
func (s *Scope) startLoop()            { s.loopDepth++ }
func (s *Scope) endLoop()              { s.loopDepth-- }
func (s *Scope) inLoop() bool          { return s.loopDepth != 0 }
func (s *Scope) breakIsValid() bool    { return s.loopDepth != 0 || s.switchDepth != 0 }
func (s *Scope) continueIsValid() bool { return s.loopDepth != 0 }

func (s *Scope) pushLabel(label ident.Identifier, isLoop bool) {
	s.labels = append(s.labels, scopeLabelInfo{label, isLoop})
}

func (s *Scope) popLabel() { s.labels = s.labels[:len(s.labels)-1] }

func (s *Scope) getLabel(label ident.Identifier) *scopeLabelInfo {
	for i := len(s.labels) - 1; i >= 0; i-- {
		if s.labels[i].label == label {
			return &s.labels[i]
		}
	}
	return nil
}

func (s *Scope) hasContainingScope() bool { return s.containingScope != nil && !s.isFunctionBoundary }

func (s *Scope) setSourceParseMode(mode SourceParseMode) {
	switch mode {
	case AsyncGeneratorBodyMode:
		s.setIsAsyncGeneratorFunctionBody()
	case AsyncArrowFunctionBodyMode:
		s.setIsAsyncArrowFunctionBody()
	case AsyncFunctionBodyMode:
		s.setIsAsyncFunctionBody()
	case GeneratorBodyMode:
		s.setIsGeneratorFunctionBody()
	case GeneratorWrapperFunctionMode, GeneratorWrapperMethodMode:
		s.setIsGeneratorFunction()
	case AsyncGeneratorWrapperMethodMode, AsyncGeneratorWrapperFunctionMode:
		s.setIsAsyncGeneratorFunction()
	case NormalFunctionMode, GetterMode, SetterMode, MethodMode, ClassFieldInitializerMode:
		s.setIsFunction()
	case ClassStaticBlockMode:
		s.setIsFunction()
		s.setIsStaticBlock()
	case ArrowFunctionMode:
		s.setIsArrowFunction()
	case AsyncFunctionMode, AsyncMethodMode:
		s.setIsAsyncFunction()
	case AsyncArrowFunctionMode:
		s.setIsAsyncArrowFunction()
	case ProgramMode:
		s.isGlobalCode = true
	case ModuleAnalyzeMode, ModuleEvaluateMode:
		s.isGlobalCode = true
		s.isModuleCode = true
	}
}

func (s *Scope) setIsStaticBlock() {
	s.isStaticBlock = true
	s.isStaticBlockBoundary = true
}

func (s *Scope) setIsLexicalScope() {
	s.isLexicalScope = true
	s.allowsLexicalDeclarations = true
}

func (s *Scope) setIsPrivateNameScope()   { s.isClassScope = true }
func (s *Scope) isPrivateNameScope() bool { return s.isClassScope }

func (s *Scope) setIsFunction() {
	s.isFunction = true
	s.isFunctionBoundary = true
	s.hasArguments = true
	s.setIsLexicalScope()
	s.isGeneratorFunction = false
	s.isGeneratorFunctionBoundary = false
	s.isArrowFunctionBoundary = false
	s.isArrowFunction = false
	s.isAsyncFunction = false
	s.isAsyncFunctionBoundary = false
	s.isStaticBlock = false
	s.isStaticBlockBoundary = false
}

func (s *Scope) setIsGeneratorFunction() {
	s.setIsFunction()
	s.isGeneratorFunction = true
}

func (s *Scope) setIsGeneratorFunctionBody() {
	s.setIsFunction()
	s.hasArguments = false
	s.isGeneratorFunction = true
	s.isGeneratorFunctionBoundary = true
}

func (s *Scope) setIsArrowFunction() {
	s.setIsFunction()
	s.isArrowFunctionBoundary = true
	s.isArrowFunction = true
}

func (s *Scope) setIsAsyncArrowFunction() {
	s.setIsArrowFunction()
	s.isAsyncFunction = true
}

func (s *Scope) setIsAsyncFunction() {
	s.setIsFunction()
	s.isAsyncFunction = true
}

func (s *Scope) setIsAsyncGeneratorFunction() {
	s.setIsFunction()
	s.isAsyncFunction = true
	s.isGeneratorFunction = true
}

func (s *Scope) setIsAsyncGeneratorFunctionBody() {
	s.setIsFunction()
	s.hasArguments = false
	s.isGeneratorFunction = true
	s.isGeneratorFunctionBoundary = true
	s.isAsyncFunction = true
	s.isAsyncFunctionBoundary = true
}

func (s *Scope) setIsAsyncFunctionBody() {
	s.setIsFunction()
	s.hasArguments = false
	s.isAsyncFunction = true
	s.isAsyncFunctionBoundary = true
}

func (s *Scope) setIsAsyncArrowFunctionBody() {
	s.setIsArrowFunction()
	s.hasArguments = false
	s.isAsyncFunction = true
	s.isAsyncFunctionBoundary = true
}

// finalizeLexicalEnvironment decides which lexical variables are captured.
func (s *Scope) finalizeLexicalEnvironment() {
	if s.usesEval || s.needsFullActivation {
		s.lexicalVariables.MarkAllVariablesAsCaptured()
	} else {
		s.computeLexicallyCapturedVariablesAndPurgeCandidates()
	}
}

func (s *Scope) takeLexicalEnvironment() VariableEnvironment {
	env := s.lexicalVariables
	s.lexicalVariables = VariableEnvironment{}
	return env
}

func (s *Scope) takeDeclaredVariables() VariableEnvironment {
	env := s.declaredVariables
	s.declaredVariables = VariableEnvironment{}
	return env
}

// computeLexicallyCapturedVariablesAndPurgeCandidates: lexical variables may
// be declared after use, so captures are only resolved when the scope is popped.
func (s *Scope) computeLexicallyCapturedVariablesAndPurgeCandidates() {
	if s.lexicalVariables.Size() != 0 && s.closedVariableCandidates.len() != 0 {
		for _, id := range s.closedVariableCandidates.keys {
			s.lexicalVariables.MarkVariableAsCapturedIfDefined(id)
		}
	}
	for id, e := range s.lexicalVariables.All {
		if e.IsCaptured() {
			s.closedVariableCandidates.remove(id)
		}
	}
}

func (s *Scope) declareCallee(id ident.Identifier) DeclarationResult {
	e, _ := s.declaredVariables.Add(id)
	// Track whether the callee is captured, but it must not act like a var.
	e.ClearIsVar()
	if isEvalOrArgumentsIdentifier(id) {
		return DeclarationInvalidStrictMode
	}
	return DeclarationValid
}

func (s *Scope) declareVariable(id ident.Identifier) DeclarationResult {
	result := DeclarationValid
	isValidStrictMode := !isEvalOrArgumentsIdentifier(id)
	s.isValidStrictMode = s.isValidStrictMode && isValidStrictMode
	e, _ := s.declaredVariables.Add(id)
	e.SetIsVar()
	if !isValidStrictMode {
		result |= DeclarationInvalidStrictMode
	}
	return result
}

func (s *Scope) declareFunctionAsVar(id ident.Identifier) DeclarationResult {
	result := DeclarationValid
	isValidStrictMode := !isEvalOrArgumentsIdentifier(id)
	if !isValidStrictMode {
		result |= DeclarationInvalidStrictMode
	}
	s.isValidStrictMode = s.isValidStrictMode && isValidStrictMode
	e, _ := s.declaredVariables.Add(id)
	e.SetIsVar()
	e.SetIsFunction()
	if s.lexicalVariables.Contains(id) {
		result |= DeclarationInvalidDuplicateDeclaration
	}
	return result
}

func (s *Scope) declareFunctionAsLet(id ident.Identifier, isFunctionDeclaration bool) DeclarationResult {
	result := DeclarationValid
	isValidStrictMode := !isEvalOrArgumentsIdentifier(id)
	if !isValidStrictMode {
		result |= DeclarationInvalidStrictMode
	}
	s.isValidStrictMode = s.isValidStrictMode && isValidStrictMode

	e, isNew := s.lexicalVariables.Add(id)
	if !isNew {
		if s.strictMode() || !e.IsFunctionDeclaration() || !isFunctionDeclaration {
			result |= DeclarationInvalidDuplicateDeclaration
		}
	}
	if s.declaredVariables.Contains(id) || s.variablesBeingHoisted.contains(id) {
		result |= DeclarationInvalidDuplicateDeclaration
	}
	e.SetIsLet()
	e.SetIsFunction()
	if isFunctionDeclaration {
		e.SetIsFunctionDeclaration()
	}
	return result
}

func (s *Scope) addVariableBeingHoisted(id ident.Identifier) { s.variablesBeingHoisted.add(id) }

func (s *Scope) addSloppyModeFunctionHoistingCandidate(md *FunctionMetadataNode, check needsDuplicateDeclarationCheck) {
	// JSC keys this map by the metadata pointer; we keep a list with
	// set-semantics on the pointer and last-write-wins for the check flag.
	for i := range s.sloppyModeFunctionHoistingCandidates.values {
		if s.sloppyModeFunctionHoistingCandidates.values[i].metadata == md {
			s.sloppyModeFunctionHoistingCandidates.values[i].needsCheck = check
			return
		}
	}
	s.sloppyModeFunctionHoistingCandidates.keys = append(s.sloppyModeFunctionHoistingCandidates.keys, md.Ident)
	s.sloppyModeFunctionHoistingCandidates.values = append(s.sloppyModeFunctionHoistingCandidates.values, sloppyCandidate{md, check})
}

func (s *Scope) appendFunction(md *FunctionMetadataNode) {
	s.functionDeclarations = append(s.functionDeclarations, md)
}

func (s *Scope) takeFunctionDeclarations() FunctionStack {
	f := s.functionDeclarations
	s.functionDeclarations = nil
	return f
}

func (s *Scope) declareLexicalVariable(id ident.Identifier, isConstant bool, importType DeclarationImportType, isUsing, isAwaitUsing bool) DeclarationResult {
	result := DeclarationValid
	isValidStrictMode := !isEvalOrArgumentsIdentifier(id)
	s.isValidStrictMode = s.isValidStrictMode && isValidStrictMode
	e, isNew := s.lexicalVariables.Add(id)
	if isConstant {
		e.SetIsConst()
	} else {
		e.SetIsLet()
	}
	if isUsing {
		e.SetIsUsing()
	}
	if isAwaitUsing {
		s.lexicalVariables.SetHasAwaitUsingDeclaration()
	}
	switch importType {
	case DeclarationImported:
		e.SetIsImported()
	case DeclarationImportedNamespace:
		e.SetIsImported()
		e.SetIsImportedNamespace()
	}
	if !isNew || s.variablesBeingHoisted.contains(id) {
		result |= DeclarationInvalidDuplicateDeclaration
	}
	if !isValidStrictMode {
		result |= DeclarationInvalidStrictMode
	}
	return result
}

func (s *Scope) hasDeclaredGlobalArguments() bool {
	return s.hasLexicallyDeclaredVariable(ident.Arguments) || s.hasDeclaredVariable(ident.Arguments) || s.shadowsArguments
}

// hasDeclaredVariable ignores the callee, which is not a "var".
func (s *Scope) hasDeclaredVariable(id ident.Identifier) bool {
	e, ok := s.declaredVariables.Find(id)
	return ok && e.IsVar()
}

func (s *Scope) hasLexicallyDeclaredVariable(id ident.Identifier) bool {
	return s.lexicalVariables.Contains(id)
}

func (s *Scope) hasVariableBeingHoisted(id ident.Identifier) bool {
	return s.variablesBeingHoisted.contains(id)
}

func (s *Scope) hasPrivateName(id ident.Identifier) bool {
	return s.lexicalVariables.HasPrivateName(id)
}

func (s *Scope) declarePrivateMethod(id ident.Identifier, tag ClassElementTag) DeclarationResult {
	var ok bool
	if tag == ClassElementTagStatic {
		ok = s.lexicalVariables.DeclareStaticPrivateMethod(id)
	} else {
		ok = s.lexicalVariables.DeclarePrivateMethod(id)
	}
	if !ok {
		return DeclarationInvalidDuplicateDeclaration
	}
	return DeclarationValid
}

func (s *Scope) declarePrivateAccessor(id ident.Identifier, tag ClassElementTag, isSetter bool) DeclarationResult {
	var r PrivateDeclarationResult
	switch {
	case isSetter && tag == ClassElementTagStatic:
		r = s.lexicalVariables.DeclareStaticPrivateSetter(id)
	case isSetter:
		r = s.lexicalVariables.DeclarePrivateSetter(id)
	case tag == ClassElementTagStatic:
		r = s.lexicalVariables.DeclareStaticPrivateGetter(id)
	default:
		r = s.lexicalVariables.DeclarePrivateGetter(id)
	}
	switch r {
	case PrivateDeclarationDuplicatedName:
		return DeclarationInvalidDuplicateDeclaration
	case PrivateDeclarationInvalidStaticNonStatic:
		return DeclarationInvalidPrivateStaticNonStatic
	}
	return DeclarationValid
}

func (s *Scope) declarePrivateSetter(id ident.Identifier, tag ClassElementTag) DeclarationResult {
	return s.declarePrivateAccessor(id, tag, true)
}

func (s *Scope) declarePrivateGetter(id ident.Identifier, tag ClassElementTag) DeclarationResult {
	return s.declarePrivateAccessor(id, tag, false)
}

func (s *Scope) declarePrivateField(id ident.Identifier) DeclarationResult {
	if !s.lexicalVariables.DeclarePrivateField(id) {
		return DeclarationInvalidDuplicateDeclaration
	}
	return DeclarationValid
}

func (s *Scope) hasDeclaredParameter(id ident.Identifier) bool {
	return s.declaredParameters.contains(id) || s.hasDeclaredVariable(id)
}

func (s *Scope) preventAllVariableDeclarations() {
	s.allowsVarDeclarations = false
	s.allowsLexicalDeclarations = false
}

func (s *Scope) preventVarDeclarations() { s.allowsVarDeclarations = false }

func (s *Scope) declareParameter(id ident.Identifier) DeclarationResult {
	result := DeclarationValid
	isArgumentsIdent := id == ident.Arguments
	e, isNew := s.declaredVariables.Add(id)
	isDuplicateParameter := !isNew && e.IsParameter()
	isValidStrictMode := !isDuplicateParameter && id != ident.Eval && !isArgumentsIdent
	e.ClearIsVar()
	e.SetIsParameter()
	s.isValidStrictMode = s.isValidStrictMode && isValidStrictMode
	s.declaredParameters.add(id)
	if !isValidStrictMode {
		result |= DeclarationInvalidStrictMode
	}
	if isArgumentsIdent {
		s.shadowsArguments = true
	}
	if isDuplicateParameter {
		result |= DeclarationInvalidDuplicateDeclaration
	}
	return result
}

func (s *Scope) usedVariablesContains(id ident.Identifier) bool {
	for _, set := range s.usedVariables {
		if set.contains(id) {
			return true
		}
	}
	return false
}

// forEachUsedVariable iterates all used variables, stopping when f returns false.
func (s *Scope) forEachUsedVariable(f func(ident.Identifier) bool) {
	for _, set := range s.usedVariables {
		for _, id := range set.keys {
			if !f(id) {
				return
			}
		}
	}
}

func (s *Scope) useVariable(id ident.Identifier, isEval bool) {
	s.usesEval = s.usesEval || isEval
	if id == s.lastAddedUsedVariable {
		return
	}
	s.lastAddedUsedVariable = id
	s.usedVariables[len(s.usedVariables)-1].add(id)
}

func (s *Scope) usePrivateName(id ident.Identifier) { s.useVariable(id, false) }

func (s *Scope) pushUsedVariableSet() {
	s.usedVariables = append(s.usedVariables, &identSet{})
	s.lastAddedUsedVariable = ident.Null
}

func (s *Scope) currentUsedVariablesSize() int { return len(s.usedVariables) }

func (s *Scope) revertToPreviousUsedVariables(size int) {
	if size < len(s.usedVariables) {
		s.usedVariables = s.usedVariables[:size]
	}
	for len(s.usedVariables) < size {
		s.usedVariables = append(s.usedVariables, &identSet{})
	}
	s.lastAddedUsedVariable = ident.Null
}

func (s *Scope) setInnerArrowFunctionUsesSuperCall() {
	s.innerArrowFunctionFeatures |= SuperCallInnerArrowFunctionFeature
}
func (s *Scope) setInnerArrowFunctionUsesSuperProperty() {
	s.innerArrowFunctionFeatures |= SuperPropertyInnerArrowFunctionFeature
}
func (s *Scope) setInnerArrowFunctionUsesEval() {
	s.innerArrowFunctionFeatures |= EvalInnerArrowFunctionFeature
}
func (s *Scope) setInnerArrowFunctionUsesThis() {
	s.innerArrowFunctionFeatures |= ThisInnerArrowFunctionFeature
}
func (s *Scope) setInnerArrowFunctionUsesNewTarget() {
	s.innerArrowFunctionFeatures |= NewTargetInnerArrowFunctionFeature
}
func (s *Scope) setInnerArrowFunctionUsesArguments() {
	s.innerArrowFunctionFeatures |= ArgumentsInnerArrowFunctionFeature
}

func (s *Scope) setInnerArrowFunctionUsesEvalAndUseArgumentsIfNeeded() {
	if s.usesEval {
		s.setInnerArrowFunctionUsesEval()
	}
	if s.usedVariablesContains(ident.Arguments) {
		s.setInnerArrowFunctionUsesArguments()
	}
}

func (s *Scope) addClosedVariableCandidateUnconditionally(id ident.Identifier) {
	s.closedVariableCandidates.add(id)
}

func (s *Scope) markLastUsedVariablesSetAsCaptured(from int) {
	for _, set := range s.usedVariables[from:] {
		for _, id := range set.keys {
			s.closedVariableCandidates.add(id)
		}
	}
}

// collectFreeVariablesFrom propagates a popped scope's free variables into
// this (its parent) scope. If precomputed is non-nil it already holds the
// nested scope's free variables (from the function cache path).
func (s *Scope) collectFreeVariablesFrom(nested *Scope, shouldTrackClosedVariables bool, precomputed []ident.Identifier) {
	if nested.usesEval {
		s.usesEval = true
	}
	if nested.usesImportMeta {
		s.usesImportMeta = true
	}

	destination := s.usedVariables[len(s.usedVariables)-1]
	// A non-arrow function's own `arguments` does not propagate out.
	argumentsIdentifierOrNull := ident.Null
	if nested.isFunctionBoundary && nested.hasArguments && !nested.isArrowFunctionBoundary {
		argumentsIdentifierOrNull = ident.Arguments
	}
	// Only inner functions and catch clauses capture: a plain lexical block does not.
	doTrackClosedVariables := shouldTrackClosedVariables && (nested.isFunctionBoundary || !nested.isLexicalScope)
	propagate := func(id ident.Identifier) {
		if id == argumentsIdentifierOrNull && !id.IsNull() {
			return
		}
		destination.add(id)
		if doTrackClosedVariables {
			s.closedVariableCandidates.add(id)
		}
	}

	if precomputed != nil {
		for _, id := range precomputed {
			propagate(id)
		}
	} else {
		for _, set := range nested.usedVariables {
			for _, id := range set.keys {
				if nested.declaredVariables.Contains(id) || nested.lexicalVariables.Contains(id) {
					continue
				}
				propagate(id)
			}
		}
	}
	// Closed variable candidates flow downwards within the same function;
	// captures across functions are realized via usedVariables.
	if shouldTrackClosedVariables && !nested.isFunctionBoundary && nested.closedVariableCandidates.len() != 0 {
		s.closedVariableCandidates.addAll(&nested.closedVariableCandidates)
	}
}

func (s *Scope) mergeInnerArrowFunctionFeatures(f InnerArrowFunctionCodeFeatures) {
	s.innerArrowFunctionFeatures |= f
}

// finalizeSloppyModeFunctionHoisting implements ES6 Annex B.3.3: a block
// function is also hoisted as a var unless that would conflict with a
// let/const/class declaration or a parameter of the same name.
func (s *Scope) finalizeSloppyModeFunctionHoisting() {
	for _, c := range s.sloppyModeFunctionHoistingCandidates.values {
		name := c.metadata.Ident
		if s.lexicalVariables.Contains(name) {
			continue
		}
		e, isNew := s.declaredVariables.Add(name)
		if isNew {
			e.SetIsSloppyModeHoistedFunction()
		} else if e.IsParameter() {
			continue
		}
		e.SetIsVar()
		c.metadata.SetIsSloppyModeHoistedFunction()
	}
}

func (s *Scope) bubbleSloppyModeFunctionHoistingCandidates(parent *Scope) {
	for _, c := range s.sloppyModeFunctionHoistingCandidates.values {
		needsCheck := bool(c.needsCheck)
		if !needsCheck || !s.lexicalVariables.Contains(c.metadata.Ident) || s.isSimpleCatchParameterScope {
			parent.addSloppyModeFunctionHoistingCandidate(c.metadata, true)
		}
	}
}

func (s *Scope) hasSloppyModeFunctionHoistingCandidates() bool {
	return len(s.sloppyModeFunctionHoistingCandidates.values) != 0
}

// getCapturedVars returns the declared variables captured by inner functions.
func (s *Scope) getCapturedVars() []ident.Identifier {
	var captured []ident.Identifier
	if s.needsFullActivation || s.usesEval {
		for id := range s.declaredVariables.All {
			captured = append(captured, id)
		}
		return captured
	}
	for _, id := range s.closedVariableCandidates.keys {
		// Refer to declaredVariables directly so the callee counts as captured too.
		if s.declaredVariables.Contains(id) {
			captured = append(captured, id)
		}
	}
	return captured
}

func (s *Scope) setStrictMode() { s.lexicallyScopedFeatures |= StrictModeLexicallyScopedFeature }
func (s *Scope) setTaintedByWithScope() {
	s.lexicallyScopedFeatures |= TaintedByWithScopeLexicallyScopedFeature
}
func (s *Scope) strictMode() bool {
	return s.lexicallyScopedFeatures&StrictModeLexicallyScopedFeature != 0
}
func (s *Scope) setHasNonSimpleParameterList() {
	s.isValidStrictMode = false
	s.hasNonSimpleParameterList = true
}

// freeVariables lists used variables not declared in this scope plus the
// captures from parameter expressions (fillParametersForSourceProviderCache).
func (s *Scope) freeVariables(capturesFromParameterExpressions *identSet) []ident.Identifier {
	var out []ident.Identifier
	for _, set := range s.usedVariables {
		for _, id := range set.keys {
			if s.declaredVariables.Contains(id) || s.lexicalVariables.Contains(id) {
				continue
			}
			out = append(out, id)
		}
	}
	return append(out, capturesFromParameterExpressions.keys...)
}

// cloneUsedVariables is used by the function cache to remember free variables.
func cloneIdents(ids []ident.Identifier) []ident.Identifier { return slices.Clone(ids) }
