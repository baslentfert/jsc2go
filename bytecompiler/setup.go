package bytecompiler

import (
	"fmt"

	"github.com/baslentfert/jsc2go/bytecode"
	"github.com/baslentfert/jsc2go/ident"
	"github.com/baslentfert/jsc2go/parser"
)

// Port of the BytecodeGenerator constructors and BytecodeGenerator::generate
// (BytecodeGenerator.cpp).

// Error is returned when code generation fails.
type Error = parser.ParserError

// Generate compiles node (a *parser.ProgramNode, *parser.FunctionNode,
// *parser.EvalNode or *parser.ModuleProgramNode) into codeBlock.
func Generate(node parser.ParsedNode, codeBlock *bytecode.UnlinkedCodeBlock, mode bytecode.CodeGenerationMode,
	parentScopeTDZVariables *bytecode.TDZEnvironmentLink, generatorOrAsyncWrapperFunctionParameterNames []ident.Identifier,
	privateNameEnvironment *parser.PrivateNameEnvironment) *Error {
	var g *Generator
	switch n := node.(type) {
	case *parser.ProgramNode:
		g = newProgramGenerator(n, codeBlock, mode)
	case *parser.FunctionNode:
		g = newFunctionGenerator(n, codeBlock, mode, parentScopeTDZVariables, generatorOrAsyncWrapperFunctionParameterNames, privateNameEnvironment)
	case *parser.EvalNode:
		g = newEvalGenerator(n, codeBlock, mode, parentScopeTDZVariables, privateNameEnvironment)
	case *parser.ModuleProgramNode:
		g = newModuleGenerator(n, codeBlock, mode)
	default:
		panic("bytecompiler: cannot generate code for this node")
	}
	err := g.generate()
	if err == nil {
		g.checkBalanced()
	}
	return err
}

// checkBalanced panics if code generation left state behind; it catches
// unbalanced reference scopes and context stacks.
func (g *Generator) checkBalanced() {
	switch {
	case len(g.kept) != 0:
		panic(fmt.Sprintf("bytecompiler: %d references not released", len(g.kept)))
	case len(g.tryContextStack) != 0:
		panic("bytecompiler: unbalanced try contexts")
	case len(g.switchContextStack) != 0:
		panic("bytecompiler: unbalanced switch contexts")
	case len(g.forInContextStack) != 0:
		panic("bytecompiler: unbalanced for-in contexts")
	case len(g.usingScopeStack) != 0:
		panic("bytecompiler: unbalanced using scopes")
	case len(g.optionalChainTargetStack) != 0:
		panic("bytecompiler: unbalanced optional chains")
	case g.currentFinallyContext != nil:
		panic("bytecompiler: unbalanced finally contexts")
	}
}

// scoped runs f in its own reference scope (a C++ block with RefPtrs).
func (g *Generator) scoped(f func()) {
	defer g.refScope()()
	f()
}

func (g *Generator) asyncFuncParametersTryCatchWrap(emitBytecode func()) {
	var td *tryData
	if info := g.asyncFuncParametersTryCatchInfo; info != nil {
		tryStartLabel := g.newEmittedLabel()
		td = g.pushTry(tryStartLabel, info.catchStartLabel, bytecode.HandlerSynthesizedCatch)
	}

	emitBytecode()

	if info := g.asyncFuncParametersTryCatchInfo; info != nil {
		tryEndLabel := g.newEmittedLabel()
		g.popTry(td, tryEndLabel)
		g.emitOutOfLineCatchHandler(info.thrownValue, nil, td)
	}
}

func (g *Generator) generate() *Error {
	defer g.refScope()()

	if g.outOfMemoryDuringConstruction {
		return &Error{Type: parser.ErrorOutOfMemory}
	}

	callingNonCallableConstructor := false
	switch g.constructorKind() {
	case parser.ConstructorKindNaked, parser.ConstructorKindBase, parser.ConstructorKindExtends:
		callingNonCallableConstructor = !g.isConstructor()
	}

	g.codeBlock.ThisRegister = g.thisRegister.VirtualRegister()

	g.emitLogShadowChickenPrologueIfNecessary()

	if !callingNonCallableConstructor {
		// If we have declared a variable named "arguments" and we are using
		// arguments then we should perform that assignment now.
		if g.needToInitializeArguments {
			g.initializeVariable(g.variable(ident.Arguments, ThisResolutionLocal), g.argumentsRegister)
		}

		shouldHoistInEval := g.codeType == bytecode.EvalCode && !g.ecmaMode.IsStrict()
		var topLevelScope *RegisterID
		for _, f := range g.functionsToInitialize {
			metadata := f.metadata
			g.scoped(func() {
				switch f.typ {
				case normalFunctionVariable:
					v := g.variable(metadata.Ident, ThisResolutionLocal)
					if local := v.Local(); local != nil {
						g.emitNewFunction(local, metadata)
					} else {
						temp := g.keep(g.newTemporary())
						g.emitNewFunction(temp, metadata)
						g.initializeVariable(v, temp)
					}
				case topLevelFunctionVariable:
					temp := g.keep(g.newTemporary())
					g.emitNewFunction(temp, metadata)
					if topLevelScope == nil {
						// This resolves to the top level scope or the global
						// object: the parser does not let let/const/class
						// variables share names with top level functions.
						var topLevelObjectScope *RegisterID
						if shouldHoistInEval {
							topLevelObjectScope = g.keep(g.emitResolveScopeForHoistingFuncDeclInEval(nil, metadata.Ident))
						} else {
							topLevelObjectScope = g.keep(g.emitResolveScope(nil, unresolvedVariable(metadata.Ident)))
						}
						topLevelScope = g.newBlockScopeVariable()
						topLevelScope.ref() // released after the loop
						g.move(topLevelScope, topLevelObjectScope)
					}
					if shouldHoistInEval {
						g.emitPutToScopeDynamic(topLevelScope, metadata.Ident, temp, bytecode.ThrowIfNotFound, bytecode.InitializationModeNotInitialization)
					} else {
						g.emitPutToScope(topLevelScope, unresolvedVariable(metadata.Ident), temp, bytecode.ThrowIfNotFound, bytecode.InitializationModeNotInitialization)
					}
				}
			})
		}
		if topLevelScope != nil {
			topLevelScope.deref()
		}

		g.emitScopeNodeBytecode()
	} else {
		// Terminate the basic block after the unconditional throw so that
		// bytecode analyses don't see an unterminated block.
		g.emitUnreachable()
	}

	for _, handler := range g.exceptionHandlersToEmit {
		realCatchTarget := g.newLabel()
		td := handler.tryData

		bytecode.EmitCatch(g, handler.exceptionRegister, handler.thrownValueRegister)
		realCatchTarget.setLocation(g, g.lastInstruction)
		if handler.completionTypeRegister.IsValid() {
			completionTypeRegister := &RegisterID{vr: handler.completionTypeRegister}
			completionType := CompletionNormal
			if td.handlerType == bytecode.HandlerFinally || td.handlerType == bytecode.HandlerSynthesizedFinally {
				completionType = CompletionThrow
			}
			g.emitLoadCompletionType(completionTypeRegister, completionType)
		}

		g.emitJump(td.target)
		td.target = realCatchTarget
	}

	if info := g.asyncFuncParametersTryCatchInfo; info != nil {
		g.emitLabel(info.catchStartLabel)
		divot := parser.TextPosition(g.scope.StartOffset())
		if g.promiseRegister != nil {
			rejectPromise := g.keep(g.moveLinkTimeConstant(nil, bytecode.LinkTimeConstantRejectPromiseWithFirstResolvingFunctionCallCheck))
			args := g.newCallArguments(nil, 2)
			g.emitLoad(args.thisRegister(), bytecode.Undefined{})
			g.move(args.argumentRegister(0), g.promiseRegister)
			g.move(args.argumentRegister(1), info.thrownValue)
			g.emitCallIgnoreResult(g.newTemporary(), rejectPromise, NoExpectedFunction, args, divot, divot, divot, DebuggableCallNo)
			g.emitReturn(g.promiseRegister)
		} else {
			// Without a promise yet we can just return @newRejectedPromise(thrownValue).
			newRejectedPromise := g.keep(g.moveLinkTimeConstant(nil, bytecode.LinkTimeConstantNewRejectedPromise))
			args := g.newCallArguments(nil, 1)
			g.emitLoad(args.thisRegister(), bytecode.Undefined{})
			g.move(args.argumentRegister(0), info.thrownValue)
			result := g.keep(g.newTemporary())
			g.emitCall(result, newRejectedPromise, NoExpectedFunction, args, divot, divot, divot, DebuggableCallNo)
			g.emitReturn(result)
		}
	}

	g.staticPropertyAnalyzer.killAll(g)

	for _, r := range g.tryRanges {
		start := r.start.bind()
		end := r.end.bind()
		// Empty try blocks and some finally blocks produce ranges with
		// end <= start; such handlers would never match, so skip them.
		if end <= start {
			continue
		}
		g.codeBlock.AddExceptionHandler(bytecode.HandlerInfo{
			Start:  uint32(start),
			End:    uint32(end),
			Target: uint32(r.tryData.target.bind()),
			Type:   r.tryData.handlerType,
		})
	}

	if g.needsGeneratorification {
		g.performGeneratorification()
	}

	if g.codeBlock.NumCalleeLocals >= bytecode.FirstConstantRegisterIndex {
		return &Error{Type: parser.ErrorOutOfMemory}
	}
	g.codeBlock.Instructions = g.instructions

	if g.expressionTooDeep {
		return &Error{Type: parser.ErrorOutOfMemory}
	}
	return nil
}

func newProgramGenerator(programNode *parser.ProgramNode, codeBlock *bytecode.UnlinkedCodeBlock, mode bytecode.CodeGenerationMode) *Generator {
	g := newGenerator(codeBlock, programNode, mode, bytecode.GlobalCode)
	defer g.refScope()()
	g.thisRegister.setIndex(bytecode.CallFrameSlotThisArgument)
	g.allowCallIgnoreResultOptimization = g.defaultAllowCallIgnoreResultOptimization
	g.needsToUpdateArrowFunctionContext = programNode.UsesArrowFunction() || programNode.UsesEval()
	g.ecmaMode = bytecode.ECMAModeFromBool(programNode.IsStrictMode())

	g.codeBlock.NumParameters = 1 // Allocate space for "this"

	g.emitEnter()

	g.allocateScope()

	for _, function := range programNode.FunctionStack {
		g.functionsToInitialize = append(g.functionsToInitialize, functionToInitialize{function, topLevelFunctionVariable})
	}

	codeBlock.VarDeclarations = programNode.VarDeclarations.Clone()
	codeBlock.LexicalDeclarations = programNode.LexicalVariables.Clone()
	// The program's lexical variables are not pushed on the TDZ stack: the
	// ResolveTypes of get_from_scope/put_to_scope check TDZ at link time.

	if g.needsToUpdateArrowFunctionContext {
		g.initializeArrowFunctionContextScopeIfNeeded(nil, false)
		g.emitPutThisToArrowFunctionContextScope()
	}
	return g
}

func newFunctionGenerator(functionNode *parser.FunctionNode, codeBlock *bytecode.UnlinkedCodeBlock, mode bytecode.CodeGenerationMode,
	parentScopeTDZVariables *bytecode.TDZEnvironmentLink, generatorOrAsyncWrapperFunctionParameterNames []ident.Identifier,
	parentPrivateNameEnvironment *parser.PrivateNameEnvironment) *Generator {
	g := newGenerator(codeBlock, functionNode, mode, bytecode.FunctionCode)
	defer g.refScope()()
	g.defaultAllowCallIgnoreResultOptimization = true
	g.isBuiltinFunction = codeBlock.IsBuiltinFunction()
	g.isBuiltinDefaultClassConstructor = codeBlock.Info.IsBuiltinDefaultClassConstructor
	g.usesSloppyEval = functionNode.UsesEval() && !functionNode.IsStrictMode()
	// Generator, async function and async generator bodies hold no tail
	// positions (IsInTailPosition steps 4 to 7). Naked constructors may tail
	// call since they have no special code for "return".
	g.allowTailCallOptimization = useTailCalls && !g.isConstructor() && g.constructorKind() == parser.ConstructorKindNone &&
		functionNode.IsStrictMode() && !g.parseMode().IsGeneratorOrAsyncFunctionBody()
	g.allowCallIgnoreResultOptimization = g.defaultAllowCallIgnoreResultOptimization
	g.needsToUpdateArrowFunctionContext = functionNode.UsesArrowFunction() || functionNode.UsesEval()
	g.ecmaMode = bytecode.ECMAModeFromBool(functionNode.IsStrictMode())
	g.derivedContextType = codeBlock.Info.DerivedContextType

	ecmaMode := g.ecmaMode
	g.pushPrivateAccessNames(parentPrivateNameEnvironment)

	functionSymbolTable := bytecode.NewSymbolTable()
	functionSymbolTable.UsesSloppyEval = g.usesSloppyEval
	symbolTableConstantIndex := 0

	g.cachedParentTDZ = parentScopeTDZVariables
	g.generatorOrAsyncWrapperFunctionParameterNames = generatorOrAsyncWrapperFunctionParameterNames

	parameters := functionNode.Parameters
	// IsSimpleParameterList is false if there are default values, a rest
	// parameter or destructuring patterns. Then parameters live in their own
	// scope and the arguments object is strict-mode like.
	isSimpleParameterList := parameters.IsSimpleParameterList

	parseMode := codeBlock.ParseMode()

	containsArrowOrEvalButNotInArrowBlock := ((functionNode.UsesArrowFunction() && functionNode.DoAnyInnerArrowFunctionsUseAnyFeature()) || g.usesEval()) && !g.codeBlock.IsArrowFunction()
	shouldCaptureSomeOfTheThings := g.shouldEmitDebugHooks() || functionNode.NeedsActivation() || containsArrowOrEvalButNotInArrowBlock

	shouldCaptureAllOfTheThings := g.shouldEmitDebugHooks() || g.usesEval()
	g.needsArguments = func() bool {
		if parseMode != parser.ClassFieldInitializerMode {
			if !codeBlock.IsArrowFunction() {
				if functionNode.UsesArrowFunction() && g.isArgumentsUsedInInnerArrowFunction() {
					return true
				}
				if functionNode.UsesArguments() {
					return true
				}
				if g.shouldEmitDebugHooks() {
					return true
				}
			}
			if g.usesEval() {
				return true
			}
		}
		return false
	}()

	g.argumentsForwarded = g.needsArguments && functionNode.Features&parser.ArgumentsOnlyForwardedFeature != 0 &&
		functionNode.Features&(parser.WithFeature|parser.ShadowsArgumentsFeature|parser.NonSimpleParameterListFeature) == 0 &&
		!codeBlock.IsArrowFunction() && !g.usesEval() && !g.shouldEmitDebugHooks() &&
		!(functionNode.UsesArrowFunction() && g.isArgumentsUsedInInnerArrowFunction()) &&
		!parseMode.IsGeneratorOrAsyncFunctionBody() && parseMode != parser.ClassFieldInitializerMode &&
		parameters.Size() == 0 // (a parameter assigned to would change the frame's argument)

	if parseMode.IsGeneratorOrAsyncFunctionBody() {
		g.needsGeneratorification = true
		// Generators and async functions never provide "arguments"; it
		// resolves to the wrapping function's.
		g.needsArguments = false
	} else if parseMode.IsGeneratorOrAsyncFunctionWrapper() {
		// The wrapper function provides "arguments" (and parameters) to the
		// body, which reads them from the wrapper's scope:
		//
		//    function *gen(a, b = hello())
		//    {
		//        return {
		//            @generatorNext: function (@generator, @generatorState, @generatorValue, @generatorResumeMode, @generatorFrame)
		//            {
		//                arguments;  // This `arguments` should reference to the gen's arguments.
		//                ...
		//            }
		//        }
		//    }
		//
		// Async functions without await inline their body, so nothing needs
		// capturing for them.
		if !(parseMode.IsAsyncFunctionWrapper() && functionNode.IsAsyncFunctionWithoutAwait()) {
			if g.needsArguments {
				shouldCaptureSomeOfTheThings = true
			}
			if parameters.Size() > 0 {
				shouldCaptureSomeOfTheThings = true
				shouldCaptureAllOfTheThings = true
			}
		}
	}

	if shouldCaptureAllOfTheThings {
		functionNode.VarDeclarations.MarkAllVariablesAsCaptured()
	}

	captures := func(uid ident.Identifier) bool {
		if !shouldCaptureSomeOfTheThings {
			return false
		}
		if g.needsArguments && uid == ident.Arguments {
			// Historically the arguments object is always captured here.
			return true
		}
		return functionNode.Captures(uid)
	}
	varKind := func(uid ident.Identifier) bytecode.VarKind {
		if captures(uid) {
			return bytecode.VarKindScope
		}
		return bytecode.VarKindStack
	}

	g.calleeRegister.setIndex(bytecode.CallFrameSlotCallee)

	g.initializeParameters(parameters)

	g.emitEnter()

	if parseMode.IsGeneratorOrAsyncFunctionBody() {
		g.generatorRegister = g.parameters[generatorArgumentGenerator]
	}

	g.allocateScope()

	switch g.constructorKind() {
	case parser.ConstructorKindNaked:
		if !g.isConstructor() {
			if name := functionNode.Ident.String(); name == "" {
				g.emitThrowTypeError("Cannot call a constructor without |new|")
			} else {
				g.emitThrowTypeError("Cannot call a constructor " + name + " without |new|")
			}
			return g
		}
	case parser.ConstructorKindBase, parser.ConstructorKindExtends:
		if !g.isConstructor() {
			if name := functionNode.Ident.String(); name == "" {
				g.emitThrowTypeError("Cannot call a class constructor without |new|")
			} else {
				g.emitThrowTypeError("Cannot call a class constructor " + name + " without |new|")
			}
			return g
		}
	}

	if !functionNode.Ident.IsNull() && functionNode.FunctionMode == parser.FunctionModeFunctionExpression {
		isDynamicScope := parser.FunctionNameScopeIsDynamic(g.usesEval(), ecmaMode.IsStrict())
		isFunctionNameCaptured := captures(functionNode.Ident)
		markAsCaptured := isDynamicScope || isFunctionNameCaptured
		g.emitPushFunctionNameScope(functionNode.Ident, &g.calleeRegister, markAsCaptured)
	}

	if shouldCaptureSomeOfTheThings {
		g.lexicalEnvironmentRegister = g.addVar()
	}

	if parseMode.IsGeneratorOrAsyncFunctionBody() || shouldCaptureSomeOfTheThings || g.shouldEmitTypeProfilerHooks() {
		symbolTableConstantIndex = g.addConstantValue(functionSymbolTable, bytecode.SourceCodeRepresentationOther).Index()
	}

	// The "var" environment can be created now unless there are default
	// parameter expressions: then its parent is the parameter environment.
	if isSimpleParameterList {
		g.initializeVarLexicalEnvironment(symbolTableConstantIndex, functionSymbolTable, shouldCaptureSomeOfTheThings)
	}

	// Parameters behave oddly when they share names with functions.
	for _, function := range functionNode.FunctionStack {
		g.functions[function.Ident] = struct{}{}
	}

	if g.needsArguments {
		// The arguments object lives in a private immutable variable (used
		// here and for DirectArguments parameter access) and the
		// user-visible "arguments" variable.
		g.argumentsRegister = g.addVar()
		g.argumentsRegister.ref()
	}

	if g.needsArguments && !ecmaMode.IsStrict() && isSimpleParameterList {
		// If any parameter is captured by name we use ScopedArguments and lift
		// all arguments into the activation; otherwise DirectArguments.
		capturesAnyParameterByName := false
		if functionNode.HasCapturedVariables() {
			for _, p := range parameters.Patterns {
				if captures(p.Pattern.(*parser.BindingNode).BoundProperty) {
					capturesAnyParameterByName = true
					break
				}
			}
		}

		if capturesAnyParameterByName {
			functionSymbolTable.SetArgumentsLength(uint32(parameters.Size()))

			// Each parameter either gets a name in the symbol table (a binding
			// without a function of the same name) or just a reserved slot;
			// either way its value is lifted into the scope.
			for i, p := range parameters.Patterns {
				offset := functionSymbolTable.TakeNextScopeOffset()
				functionSymbolTable.SetArgumentOffset(uint32(i), offset)

				varOrAnonymous := ^uint32(0)
				if name, ok := g.visibleNameForParameter(p.Pattern); ok {
					functionSymbolTable.Set(name, bytecode.SymbolTableEntry{Offset: bytecode.VarOffsetForScope(offset)})
					varOrAnonymous = g.addConstant(p.Pattern.(*parser.BindingNode).BoundProperty)
				}

				bytecode.EmitPutToScope(g, vr(g.lexicalEnvironmentRegister), varOrAnonymous, bytecode.VirtualRegisterForArgumentIncludingThis(1+i, 0),
					bytecode.MakeGetPutInfo(bytecode.ThrowIfNotFound, bytecode.ResolvedClosureVar, bytecode.InitializationModeScopedArgumentInitialization, ecmaMode),
					bytecode.SymbolTableOrScopeDepthForSymbolTable(bytecode.VirtualRegister(symbolTableConstantIndex)), uint32(offset))
			}

			// Create a scoped arguments object and copy the overflow
			// arguments into the scope (ScopedArguments::createByCopying()).
			bytecode.EmitCreateScopedArguments(g, vr(g.argumentsRegister), vr(g.lexicalEnvironmentRegister))
		} else {
			// All parameters go into the DirectArguments object.
			for i, p := range parameters.Patterns {
				if name, ok := g.visibleNameForParameter(p.Pattern); ok {
					functionSymbolTable.Set(name, bytecode.SymbolTableEntry{Offset: bytecode.VarOffsetForDirectArgument(bytecode.DirectArgumentsOffset(i))})
				}
			}
			if !g.argumentsForwarded {
				bytecode.EmitCreateDirectArguments(g, vr(g.argumentsRegister))
			}
		}
	} else if isSimpleParameterList {
		// Create the formal parameters the normal way; captured ones are
		// lifted into the scope. (Not possible with default parameter
		// expressions, whose parameters live in their own environment.)
		for i, p := range parameters.Patterns {
			name, ok := g.visibleNameForParameter(p.Pattern)
			if !ok {
				continue
			}
			if !captures(name) {
				// The easy case: the variable is the argument's stack slot.
				functionSymbolTable.Set(name, bytecode.SymbolTableEntry{Offset: bytecode.VarOffsetForStack(bytecode.VirtualRegisterForArgumentIncludingThis(1+i, 0))})
				continue
			}

			offset := functionSymbolTable.TakeNextScopeOffset()
			functionSymbolTable.Set(name, bytecode.SymbolTableEntry{Offset: bytecode.VarOffsetForScope(offset)})
			id := p.Pattern.(*parser.BindingNode).BoundProperty
			bytecode.EmitPutToScope(g, vr(g.lexicalEnvironmentRegister), g.addConstant(id), bytecode.VirtualRegisterForArgumentIncludingThis(1+i, 0),
				bytecode.MakeGetPutInfo(bytecode.ThrowIfNotFound, bytecode.ResolvedClosureVar, bytecode.InitializationModeNotInitialization, ecmaMode),
				bytecode.SymbolTableOrScopeDepthForSymbolTable(bytecode.VirtualRegister(symbolTableConstantIndex)), uint32(offset))
		}
	}

	if g.needsArguments && (ecmaMode.IsStrict() || !isSimpleParameterList) && !g.argumentsForwarded {
		// Allocate a cloned arguments object.
		bytecode.EmitCreateClonedArguments(g, vr(g.argumentsRegister))
	}

	// Some variables start out as something other than undefined:
	// "arguments" (unless a function or parameter shadows it) and functions
	// (which override everything). Simple parameters were initialized
	// above; generate() initializes "arguments" first and then the
	// functions, so that the overriding rules hold.
	//
	// "arguments" is overridden by everything except the callee: add it to
	// the symbol table unless it is a parameter.
	shouldCreateArgumentsVariableInParameterScope := false
	if g.needsArguments {
		// If a function or destructuring parameter is named "arguments",
		// createVariable() changes nothing and generate() writes "arguments"
		// before initializing them. A simple parameter named "arguments"
		// would already be initialized, so we must bail.
		haveParameterNamedArguments := false
		for _, p := range parameters.Patterns {
			if name, ok := g.visibleNameForParameter(p.Pattern); ok && name == ident.Arguments {
				haveParameterNamedArguments = true
				break
			}
		}

		pm := g.codeBlock.ParseMode()
		shouldCreateArgumentsVariable := !haveParameterNamedArguments &&
			pm != parser.ArrowFunctionMode && pm != parser.AsyncArrowFunctionMode && pm != parser.ClassFieldInitializerMode
		shouldCreateArgumentsVariableInParameterScope = shouldCreateArgumentsVariable && !isSimpleParameterList
		// Arrow functions load "arguments" from the parent scope.
		if shouldCreateArgumentsVariable && !shouldCreateArgumentsVariableInParameterScope {
			g.createVariable(ident.Arguments, varKind(ident.Arguments), functionSymbolTable, verifyExisting)
			g.needToInitializeArguments = true
		}
	}

	for _, function := range functionNode.FunctionStack {
		g.createVariable(function.Ident, varKind(function.Ident), functionSymbolTable, verifyExisting)
		g.functionsToInitialize = append(g.functionsToInitialize, functionToInitialize{function, normalFunctionVariable})
	}
	for name, entry := range functionNode.VarDeclarations.All {
		if !entry.IsVar() { // This is either a parameter or callee.
			continue
		}
		if shouldCreateArgumentsVariableInParameterScope && name == ident.Arguments {
			continue
		}
		if containsIdent(generatorOrAsyncWrapperFunctionParameterNames, name) {
			continue
		}
		g.createVariable(name, varKind(name), functionSymbolTable, ignoreExisting)
	}

	if functionNode.NeedsNewTargetRegisterForThisScope() || g.isNewTargetUsedInInnerArrowFunction() || g.usesEval() {
		g.newTargetRegister = g.addVar()
	}

	shouldEmitToThis := func() bool {
		if functionNode.UsesThis() || g.usesEval() || g.scope.DoAnyInnerArrowFunctionsUseThis() || g.scope.DoAnyInnerArrowFunctionsUseEval() {
			return true
		}
		if (functionNode.UsesSuperProperty() || g.scope.DoAnyInnerArrowFunctionsUseSuperProperty()) && !ecmaMode.IsStrict() {
			// Sloppy code converts |this| to an object that may be passed to
			// a strict function as |this|, which is observable.
			return true
		}
		return false
	}

	switch parseMode {
	case parser.GeneratorWrapperFunctionMode, parser.GeneratorWrapperMethodMode,
		parser.AsyncGeneratorWrapperMethodMode, parser.AsyncGeneratorWrapperFunctionMode:
		g.generatorRegister = g.addVar()
		// FIXME: Emit to_this only when Generator uses it.
		g.emitToThisSelf()

	case parser.AsyncArrowFunctionMode, parser.AsyncMethodMode, parser.AsyncFunctionMode:
		isAsyncFunctionWithoutAwait := g.scope.IsAsyncFunctionWithoutAwait()
		// An async function body without await needs no generator.
		if !isAsyncFunctionWithoutAwait {
			g.generatorRegister = g.addVar()
			g.promiseRegister = g.addVar()
		}

		willEmitToThis := false
		if parseMode != parser.AsyncArrowFunctionMode {
			// FIXME: Emit to_this only when AsyncFunctionBody uses it.
			if isAsyncFunctionWithoutAwait {
				willEmitToThis = shouldEmitToThis()
			} else {
				willEmitToThis = true
			}
		}
		if willEmitToThis {
			g.emitToThisSelf()
		}

		if !isAsyncFunctionWithoutAwait {
			g.emitNewPromise(g.promiseRegister)
			g.emitNewAsyncFunctionGenerator(g.generatorRegister)
			g.emitPutInternalField(g.generatorRegister, asyncFunctionGeneratorFieldContext, g.promiseRegister)
		}

	case parser.AsyncGeneratorBodyMode, parser.AsyncFunctionBodyMode, parser.AsyncArrowFunctionBodyMode, parser.GeneratorBodyMode:
		// |this| is already filled correctly before here.
		if g.newTargetRegister != nil {
			g.emitLoad(g.newTargetRegister, bytecode.Undefined{})
		}

	case parser.ArrowFunctionMode:

	default:
		if g.isConstructor() {
			if g.newTargetRegister != nil {
				g.move(g.newTargetRegister, &g.thisRegister)
			}
			switch g.constructorKind() {
			case parser.ConstructorKindNaked:
				// Naked constructors don't create |this| automatically.
			case parser.ConstructorKindNone, parser.ConstructorKindBase:
				g.emitCreateThis(&g.thisRegister)
				if g.privateBrandRequirement() == parser.PrivateBrandRequirementNeeded {
					g.emitInstallPrivateBrand(&g.thisRegister)
				}
				pos := g.scope.Position()
				g.emitInstanceFieldInitializationIfNeeded(&g.thisRegister, &g.calleeRegister, pos, pos, pos)
			case parser.ConstructorKindExtends:
				g.moveEmptyValue(&g.thisRegister)
			}
		} else {
			switch g.constructorKind() {
			case parser.ConstructorKindNone:
				if shouldEmitToThis() {
					g.emitToThisSelf()
				}
			default:
				panic("bytecompiler: constructor called without new")
			}
		}
	}

	// Arrow functions with default parameter expressions load |super| and
	// |this| first, because the default values can use them.
	isArrowMode := parseMode == parser.ArrowFunctionMode || parseMode == parser.AsyncArrowFunctionMode
	if (isArrowMode && !isSimpleParameterList) || parseMode == parser.AsyncArrowFunctionBodyMode {
		if functionNode.UsesThis() || functionNode.UsesSuperProperty() {
			g.emitLoadThisFromArrowFunctionLexicalEnvironment()
		}
		if g.scope.NeedsNewTargetRegisterForThisScope() {
			g.emitLoadNewTargetFromArrowFunctionLexicalEnvironment()
		}
	}

	if g.needsToUpdateArrowFunctionContext && !codeBlock.IsArrowFunction() {
		canReuseLexicalEnvironment := isSimpleParameterList
		g.initializeArrowFunctionContextScopeIfNeeded(functionSymbolTable, canReuseLexicalEnvironment)
		g.emitPutThisToArrowFunctionContextScope()
		g.emitPutNewTargetToArrowFunctionContextScope()
		g.emitPutDerivedConstructorToArrowFunctionContextScope()
	}

	if parseMode.IsAsyncFunctionWrapper() && !isSimpleParameterList {
		thrownValue := g.newTemporary()
		thrownValue.ref() // held for the whole compilation
		g.asyncFuncParametersTryCatchInfo = &asyncFuncParametersTryCatchInfo{catchStartLabel: g.newLabel(), thrownValue: thrownValue}
	}

	g.asyncFuncParametersTryCatchWrap(func() {
		// All addVar()s must happen before this, because default parameter
		// expressions use temporary registers.
		g.initializeDefaultParameterValuesAndSetupFunctionScopeStack(parameters, isSimpleParameterList, functionNode, functionSymbolTable,
			symbolTableConstantIndex, captures, shouldCreateArgumentsVariableInParameterScope)
	})

	// Without default parameter expressions, arrow functions load |this|
	// here: the symbol table stack this consults is set up just above.
	if isArrowMode && isSimpleParameterList {
		if functionNode.UsesThis() || functionNode.UsesSuperProperty() {
			g.emitLoadThisFromArrowFunctionLexicalEnvironment()
		}
		if g.scope.NeedsNewTargetRegisterForThisScope() {
			g.emitLoadNewTargetFromArrowFunctionLexicalEnvironment()
		}
	}

	if parseMode.IsGeneratorWrapper() {
		g.emitCreateGenerator(g.generatorRegister, &g.calleeRegister)
	} else if parseMode.IsAsyncGeneratorWrapper() {
		g.emitCreateAsyncGenerator(g.generatorRegister, &g.calleeRegister)
	}

	// The lexical environment is the generator frame: suspended registers
	// are stored in it under symbol keys that user code cannot reach.
	if parseMode.IsGeneratorOrAsyncFunctionBody() {
		g.generatorFrameSymbolTable = functionSymbolTable
		g.generatorFrameSymbolTableIndex = symbolTableConstantIndex
		if g.lexicalEnvironmentRegister != nil {
			g.move(g.generatorFrameRegister(), g.lexicalEnvironmentRegister)
		} else {
			// The generator may not need to save any registers, so avoid
			// creating an environment if possible: this marker is turned into
			// an environment creation (or a move of undefined) by
			// generatorification.
			bytecode.EmitCreateGeneratorFrameEnvironment(g, vr(g.generatorFrameRegister()), vr(g.scopeRegister),
				bytecode.VirtualRegister(symbolTableConstantIndex), vr(g.addConstantValue(bytecode.Undefined{}, bytecode.SourceCodeRepresentationOther)))
		}
		g.emitPutInternalField(g.generatorRegister, generatorFieldFrame, g.generatorFrameRegister())
	}

	// Top-level function declarations are generated in generate().
	g.pushLexicalScope(&g.scope.VariableEnvironmentNode, ScopeTypeLetConstScope, TDZCheckOptimize, NestedScopeIsNotNested, nil, false)
	return g
}

func newEvalGenerator(evalNode *parser.EvalNode, codeBlock *bytecode.UnlinkedCodeBlock, mode bytecode.CodeGenerationMode,
	parentScopeTDZVariables *bytecode.TDZEnvironmentLink, parentPrivateNameEnvironment *parser.PrivateNameEnvironment) *Generator {
	g := newGenerator(codeBlock, evalNode, mode, bytecode.EvalCode)
	defer g.refScope()()
	g.thisRegister.setIndex(bytecode.CallFrameSlotThisArgument)
	g.usesSloppyEval = evalNode.UsesEval() && !evalNode.IsStrictMode()
	g.allowCallIgnoreResultOptimization = g.defaultAllowCallIgnoreResultOptimization
	g.needsToUpdateArrowFunctionContext = evalNode.UsesArrowFunction() || evalNode.UsesEval()
	g.ecmaMode = bytecode.ECMAModeFromBool(evalNode.IsStrictMode())
	g.derivedContextType = codeBlock.Info.DerivedContextType

	g.codeBlock.NumParameters = 1

	g.pushPrivateAccessNames(parentPrivateNameEnvironment)

	g.cachedParentTDZ = parentScopeTDZVariables

	g.emitEnter()
	g.allocateScope()
	g.topLevelScopeRegister = g.addVar()
	g.topLevelScopeRegister.ref()
	g.move(g.topLevelScopeRegister, g.scopeRegister)

	for _, function := range evalNode.FunctionStack {
		g.codeBlock.AddFunctionDecl(g.makeFunction(function))
		g.functionsToInitialize = append(g.functionsToInitialize, functionToInitialize{function, topLevelFunctionVariable})
	}

	var variables, hoistedFunctions []ident.Identifier
	for name, entry := range evalNode.VarDeclarations.All {
		if entry.IsSloppyModeHoistedFunction() {
			hoistedFunctions = append(hoistedFunctions, name)
		} else if !entry.IsFunction() {
			variables = append(variables, name)
		}
	}
	codeBlock.EvalVariables = variables
	codeBlock.FunctionHoistingCandidates = hoistedFunctions

	if evalNode.NeedsNewTargetRegisterForThisScope() {
		g.newTargetRegister = g.addVar()
	}

	if codeBlock.Info.IsArrowFunctionContext && (evalNode.UsesThis() || evalNode.UsesSuperProperty()) {
		g.emitLoadThisFromArrowFunctionLexicalEnvironment()
	}

	if evalNode.NeedsNewTargetRegisterForThisScope() {
		g.emitLoadNewTargetFromArrowFunctionLexicalEnvironment()
	}

	if g.needsToUpdateArrowFunctionContext && !codeBlock.Info.IsArrowFunctionContext && !g.isDerivedConstructorContext() {
		g.initializeArrowFunctionContextScopeIfNeeded(nil, false)
		g.emitPutThisToArrowFunctionContextScope()
	}

	// Top-level function declarations are generated in generate().
	g.pushLexicalScope(&g.scope.VariableEnvironmentNode, ScopeTypeLetConstScope, TDZCheckOptimize, NestedScopeIsNotNested, nil, false)
	return g
}

func newModuleGenerator(moduleProgramNode *parser.ModuleProgramNode, codeBlock *bytecode.UnlinkedCodeBlock, mode bytecode.CodeGenerationMode) *Generator {
	g := newGenerator(codeBlock, moduleProgramNode, mode, bytecode.ModuleCode)
	defer g.refScope()()
	g.thisRegister.setIndex(bytecode.CallFrameSlotThisArgument)
	g.defaultAllowCallIgnoreResultOptimization = true
	g.allowCallIgnoreResultOptimization = g.defaultAllowCallIgnoreResultOptimization
	g.needsToUpdateArrowFunctionContext = moduleProgramNode.UsesArrowFunction() || moduleProgramNode.UsesEval()
	g.ecmaMode = bytecode.ECMAModeStrict

	codeBlock.VarDeclarations = moduleProgramNode.VarDeclarations.Clone()

	moduleEnvironmentSymbolTable := bytecode.NewSymbolTable()
	moduleEnvironmentSymbolTable.UsesSloppyEval = g.usesSloppyEval
	moduleEnvironmentSymbolTable.ScopeType = bytecode.LexicalScope

	shouldCaptureAllOfTheThings := g.shouldEmitDebugHooks() || g.usesEval()
	if shouldCaptureAllOfTheThings {
		moduleProgramNode.VarDeclarations.MarkAllVariablesAsCaptured()
	}

	lookUpVarKind := func(uid ident.Identifier, entry parser.VariableEnvironmentEntry) bytecode.VarKind {
		// Exported variables and namespace imports live in the module
		// environment so that other modules can reach them.
		if entry.IsExported() || entry.IsImportedNamespace() || entry.IsCaptured() {
			return bytecode.VarKindScope
		}
		if moduleProgramNode.Captures(uid) {
			return bytecode.VarKindScope
		}
		return bytecode.VarKindStack
	}

	if moduleProgramNode.UsesAwait {
		g.needsGeneratorification = true
		g.initializeNextParameter() // |this|
		for range generatorNumberOfArguments {
			g.initializeNextParameter()
		}
		g.generatorRegister = g.parameters[generatorArgumentGenerator]
	}

	g.emitEnter()
	g.allocateScope()
	g.topLevelScopeRegister = g.addVar()
	g.topLevelScopeRegister.ref()
	g.move(g.topLevelScopeRegister, g.scopeRegister)

	g.calleeRegister.setIndex(bytecode.CallFrameSlotCallee)

	g.codeBlock.NumParameters = generatorNumberOfArguments + 1 // "this" + async module arguments

	// Now declare all variables.
	g.createVariable(ident.StarNamespacePrivateName, bytecode.VarKindScope, moduleEnvironmentSymbolTable, verifyExisting)
	if moduleProgramNode.Features&parser.ImportMetaFeature != 0 {
		g.createVariable(ident.MetaPrivateName, bytecode.VarKindScope, moduleEnvironmentSymbolTable, verifyExisting)
	}

	for name, entry := range moduleProgramNode.VarDeclarations.All {
		if !entry.IsVar() { // This is either a parameter or callee.
			continue
		}
		// Imported bindings stay "Dynamic" here and are resolved to
		// ModuleVar when the code block is linked.
		if entry.IsImported() && !entry.IsImportedNamespace() {
			continue
		}
		g.createVariable(name, lookUpVarKind(name, *entry), moduleEnvironmentSymbolTable, ignoreExisting)
	}

	lexicalVariables := &moduleProgramNode.LexicalVariables
	g.instantiateLexicalVariables(lexicalVariables, ScopeTypeLetConstScope, moduleEnvironmentSymbolTable, scopeRegisterBlock,
		func(uid ident.Identifier, entry parser.VariableEnvironmentEntry) bytecode.VarKind {
			return lookUpVarKind(uid, entry)
		})

	// The symbol table is kept in the constant pool.
	var constantSymbolTable *RegisterID
	if g.shouldEmitTypeProfilerHooks() || moduleProgramNode.UsesAwait {
		constantSymbolTable = g.addConstantValue(moduleEnvironmentSymbolTable, bytecode.SourceCodeRepresentationOther)
	} else {
		constantSymbolTable = g.addConstantValue(moduleEnvironmentSymbolTable.CloneScopePart(), bytecode.SourceCodeRepresentationOther)
	}

	if moduleProgramNode.UsesAwait {
		g.generatorFrameSymbolTable = moduleEnvironmentSymbolTable
		g.generatorFrameSymbolTableIndex = constantSymbolTable.Index()
		g.emitPutInternalField(g.generatorRegister, moduleRecordFieldFrame, g.generatorFrameRegister())
	}

	g.pushTDZVariables(lexicalVariables, TDZCheckOptimize, underTDZ)
	g.lexicalScopeStack = append(g.lexicalScopeStack, lexicalScopeStackEntry{moduleEnvironmentSymbolTable, g.topLevelScopeRegister, false, constantSymbolTable.Index()})
	g.emitPrefillStackTDZVariables(lexicalVariables, moduleEnvironmentSymbolTable)

	// makeFunction needs the TDZ stack set up, so it comes after the above.
	for _, function := range moduleProgramNode.FunctionStack {
		entry, ok := lexicalVariables.Find(function.Ident)
		if !ok || entry.IsImported() {
			panic("bytecompiler: module function declaration is not a lexical variable")
		}

		if lookUpVarKind(function.Ident, *entry) == bytecode.VarKindScope {
			// Heap allocated function declarations (exported or captured) are
			// instantiated when the module environment is created
			// (ModuleDeclarationInstantiation step 16.a.iv.1), so that other
			// modules can call them before this module's body runs, e.g. with
			// circular imports.
			g.codeBlock.AddFunctionDecl(g.makeFunction(function))
		} else {
			// Stack allocated functions are created when the body runs.
			g.functionsToInitialize = append(g.functionsToInitialize, functionToInitialize{function, normalFunctionVariable})
		}
	}

	// Linking clones the top-most symbol table; the module environment is
	// created from the clone found at this constant offset.
	codeBlock.ModuleEnvironmentSymbolTableConstantRegisterOffset = constantSymbolTable.Index()
	return g
}

func containsIdent(list []ident.Identifier, id ident.Identifier) bool {
	for _, x := range list {
		if x == id {
			return true
		}
	}
	return false
}

func (g *Generator) initializeDefaultParameterValuesAndSetupFunctionScopeStack(parameters *parser.FunctionParameters, isSimpleParameterList bool,
	functionNode *parser.FunctionNode, functionSymbolTable *bytecode.SymbolTable, symbolTableConstantIndex int,
	captures func(ident.Identifier) bool, shouldCreateArgumentsVariableInParameterScope bool) {
	defer g.refScope()()

	type valueToMove struct {
		id    ident.Identifier
		value *RegisterID
	}
	var valuesToMoveIntoVars []valueToMove
	if !isSimpleParameterList {
		// FunctionDeclarationInstantiation step 21.
		var environment parser.VariableEnvironment
		var allParameterNames []ident.Identifier
		for _, p := range parameters.Patterns {
			allParameterNames = p.Pattern.CollectBoundIdentifiers(allParameterNames)
		}
		if shouldCreateArgumentsVariableInParameterScope {
			allParameterNames = append(allParameterNames, ident.Arguments)
		}
		parameterSet := make(map[ident.Identifier]struct{})
		for _, id := range allParameterNames {
			parameterSet[id] = struct{}{}
			entry, _ := environment.Add(id)
			entry.SetIsLet() // With default parameter expressions, parameters act like "let" variables.
			if captures(id) {
				entry.SetIsCaptured()
			}
		}
		// Step 25.
		g.pushLexicalScopeInternal(&environment, TDZCheckOptimize, NestedScopeIsNotNested, nil, underTDZ, ScopeTypeLetConstScope, scopeRegisterBlock)

		if shouldCreateArgumentsVariableInParameterScope {
			argumentsVariable := g.variable(ident.Arguments, ThisResolutionLocal)
			g.initializeVariable(argumentsVariable, g.argumentsRegister)
			g.liftTDZCheckIfPossible(argumentsVariable)
		}

		temp := g.keep(g.newTemporary())
		for i, parameter := range parameters.Patterns {
			if _, ok := parameter.Pattern.(*parser.RestParameterNode); ok {
				continue
			}
			g.scoped(func() {
				if i+1 < len(g.parameters) {
					g.move(temp, g.parameters[i+1])
				} else {
					g.emitGetArgument(temp, int32(i))
				}
				if parameter.DefaultValue != nil {
					skipDefaultParameterBecauseNotUndefined := g.newLabel()
					g.emitJumpIfFalse(g.emitIsUndefined(g.newTemporary(), temp), skipDefaultParameterBecauseNotUndefined)
					g.emitNode(temp, parameter.DefaultValue)
					g.emitLabel(skipDefaultParameterBecauseNotUndefined)
				}
				g.bindValue(parameter.Pattern, temp)
			})
		}

		if g.restParameter != nil {
			g.emitRestParameterNode(g.restParameter)
		}

		// Step 28: a "var" with a parameter's name starts out with the
		// parameter's value, though they are distinct bindings.
		for name, entry := range functionNode.VarDeclarations.All {
			if !entry.IsVar() { // This is either a parameter or callee.
				continue
			}
			if _, ok := parameterSet[name]; ok {
				v := g.variable(name, ThisResolutionLocal)
				scope := g.emitResolveScope(nil, v)
				value := g.keep(g.emitGetFromScope(g.newTemporary(), scope, v, bytecode.DoNotThrowIfNotFound))
				valuesToMoveIntoVars = append(valuesToMoveIntoVars, valueToMove{name, value})
			}
		}

		// With default parameter expressions the "var" environment record is
		// separate and has the parameter environment as its parent.
		hasCapturedVariables := g.lexicalEnvironmentRegister != nil
		g.initializeVarLexicalEnvironment(symbolTableConstantIndex, functionSymbolTable, hasCapturedVariables)
	}

	// Completes step 28.
	for _, m := range valuesToMoveIntoVars {
		v := g.variable(m.id, ThisResolutionLocal)
		scope := g.emitResolveScope(nil, v)
		g.emitPutToScope(scope, v, m.value, bytecode.DoNotThrowIfNotFound, bytecode.InitializationModeNotInitialization)
	}
}

func (g *Generator) needsDerivedConstructorInArrowFunctionLexicalEnvironment() bool {
	return g.codeBlock.Info.IsClassContext && g.isSuperUsedInInnerArrowFunction()
}

func (g *Generator) initializeArrowFunctionContextScopeIfNeeded(functionSymbolTable *bytecode.SymbolTable, canReuseLexicalEnvironment bool) {
	if canReuseLexicalEnvironment && g.lexicalEnvironmentRegister != nil {
		g.arrowFunctionContextLexicalEnvironmentRegister = g.lexicalEnvironmentRegister

		if g.isThisUsedInInnerArrowFunction() {
			offset := functionSymbolTable.TakeNextScopeOffset()
			functionSymbolTable.Add(ident.ThisPrivateName, bytecode.SymbolTableEntry{Offset: bytecode.VarOffsetForScope(offset)})
		}

		if g.codeType == bytecode.FunctionCode && g.isNewTargetUsedInInnerArrowFunction() {
			offset := functionSymbolTable.TakeNextScopeOffset()
			functionSymbolTable.Add(ident.NewTargetLocalPrivateName, bytecode.SymbolTableEntry{Offset: bytecode.VarOffsetForScope(offset)})
		}

		if g.needsDerivedConstructorInArrowFunctionLexicalEnvironment() {
			offset := functionSymbolTable.TakeNextScopeOffset()
			functionSymbolTable.Add(ident.DerivedConstructorPrivateName, bytecode.SymbolTableEntry{Offset: bytecode.VarOffsetForScope(offset)})
		}
		return
	}

	var environment parser.VariableEnvironment
	addCapturedLet := func(id ident.Identifier) {
		e, _ := environment.Add(id)
		e.SetIsCaptured()
		e.SetIsLet()
	}

	if g.isThisUsedInInnerArrowFunction() {
		addCapturedLet(ident.ThisPrivateName)
	}
	if g.codeType == bytecode.FunctionCode && g.isNewTargetUsedInInnerArrowFunction() {
		addCapturedLet(ident.NewTargetLocalPrivateName)
	}
	if g.needsDerivedConstructorInArrowFunctionLexicalEnvironment() {
		addCapturedLet(ident.DerivedConstructorPrivateName)
	}

	if environment.Size() > 0 {
		g.pushLexicalScopeInternal(&environment, TDZCheckOptimize, NestedScopeIsNotNested, nil, underTDZ, ScopeTypeLetConstScope, scopeRegisterBlock)
		g.arrowFunctionContextLexicalEnvironmentRegister = g.lexicalScopeStack[len(g.lexicalScopeStack)-1].scope
	}
}

// initializeNextParameter sets up the stack slot of the next parameter; it
// does nothing for the symbol table.
func (g *Generator) initializeNextParameter() *RegisterID {
	reg := bytecode.VirtualRegisterForArgumentIncludingThis(g.codeBlock.NumParameters, 0)
	parameter := newRegisterID(reg)
	g.parameters = append(g.parameters, parameter)
	g.codeBlock.NumParameters++
	return parameter
}

func (g *Generator) initializeParameters(parameters *parser.FunctionParameters) {
	// Tell the code block about the parameters and note the ones that need
	// destructuring.
	g.thisRegister.setIndex(g.initializeNextParameter().VirtualRegister()) // this

	nonSimpleArguments := false
	for _, parameter := range parameters.Patterns {
		if rest, ok := parameter.Pattern.(*parser.RestParameterNode); ok {
			g.restParameter = rest
			nonSimpleArguments = true
			continue
		}
		if parameter.DefaultValue != nil {
			nonSimpleArguments = true
			continue
		}
		if !nonSimpleArguments {
			g.initializeNextParameter()
		}
	}
}

func (g *Generator) initializeVarLexicalEnvironment(symbolTableConstantIndex int, functionSymbolTable *bytecode.SymbolTable, hasCapturedVariables bool) {
	if hasCapturedVariables {
		bytecode.EmitCreateLexicalEnvironment(g, vr(g.lexicalEnvironmentRegister), vr(g.scopeRegister),
			bytecode.VirtualRegister(symbolTableConstantIndex), vr(g.addConstantValue(bytecode.Undefined{}, bytecode.SourceCodeRepresentationOther)))

		g.move(g.scopeRegister, g.lexicalEnvironmentRegister)

		g.pushLocalControlFlowScope()
	}
	g.lexicalScopeStack = append(g.lexicalScopeStack, lexicalScopeStackEntry{functionSymbolTable, g.lexicalEnvironmentRegister, false, symbolTableConstantIndex})
	g.varScopeLexicalScopeStackIndex = len(g.lexicalScopeStack) - 1
}

// visibleNameForParameter returns the name a simple parameter binds, unless
// a function declaration shadows it.
func (g *Generator) visibleNameForParameter(pattern parser.DestructuringPatternNode) (ident.Identifier, bool) {
	if b, ok := pattern.(*parser.BindingNode); ok {
		if _, shadowed := g.functions[b.BoundProperty]; !shadowed {
			return b.BoundProperty, true
		}
	}
	return ident.Null, false
}

// Internal field indices of the generator objects (JSGenerator::Field etc.).
const (
	generatorFieldState = 0
	generatorFieldNext  = 1
	generatorFieldThis  = 2
	generatorFieldFrame = 3

	asyncFunctionGeneratorFieldContext = 4

	asyncGeneratorFieldQueue                    = 4
	asyncGeneratorFieldResumeValue              = 5
	asyncGeneratorFieldResumeMode               = 6
	asyncGeneratorFieldResumePromise            = 7
	asyncGeneratorFieldCachedDriverResult       = 8
	asyncGeneratorFieldCachedDriverResultTarget = 9

	moduleRecordFieldState = 0
	moduleRecordFieldFrame = 1
)

// useTailCalls is Options::useTailCalls().
const useTailCalls = true
