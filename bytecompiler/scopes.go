package bytecompiler

import (
	"github.com/baslentfert/jsc2go/bytecode"
	"github.com/baslentfert/jsc2go/ident"
	"github.com/baslentfert/jsc2go/parser"
)

// Lexical scopes, variables and scope resolution (BytecodeGenerator.cpp).

// Scope slots that class scopes reserve for their brands
// (PrivateNameEntry::privateClassBrandOffset / privateBrandOffset).
const (
	privateClassBrandOffset = 0
	privateBrandOffset      = 1
)

type lookUpVarKindFunc func(ident.Identifier, parser.VariableEnvironmentEntry) bytecode.VarKind

func (g *Generator) instantiateLexicalVariables(lexicalVariables *parser.VariableEnvironment, scopeType ScopeType,
	symbolTable *bytecode.SymbolTable, scopeRegisterType scopeRegisterType, lookUpVarKind lookUpVarKindFunc) bool {
	hasCapturedVariables := false
	hasPrivateNames := lexicalVariables.PrivateNamesSize() > 0
	// @privateClassBrand and @privateBrand must get scope offsets 0 and 1:
	// define them first and skip them in the loop below.
	if scopeType == ScopeTypeClassScope && hasPrivateNames {
		hasCapturedVariables = true
		classBrand := symbolTable.TakeNextScopeOffset()
		if classBrand != privateClassBrandOffset {
			panic("bytecompiler: @privateClassBrand must be at scope offset 0")
		}
		symbolTable.Add(ident.PrivateClassBrandPrivateName, bytecode.SymbolTableEntry{Offset: bytecode.VarOffsetForScope(classBrand), ReadOnly: true})

		brand := symbolTable.TakeNextScopeOffset()
		if brand != privateBrandOffset {
			panic("bytecompiler: @privateBrand must be at scope offset 1")
		}
		symbolTable.Add(ident.PrivateBrandPrivateName, bytecode.SymbolTableEntry{Offset: bytecode.VarOffsetForScope(brand), ReadOnly: true})
	}

	for key, entry := range lexicalVariables.All {
		if scopeType == ScopeTypeClassScope && hasPrivateNames &&
			(key == ident.PrivateClassBrandPrivateName || key == ident.PrivateBrandPrivateName) {
			continue
		}

		// Imported bindings other than namespace imports are not allocated
		// like ordinary variables; only module environments have them.
		if entry.IsImported() && !entry.IsImportedNamespace() {
			continue
		}

		var varOffset bytecode.VarOffset
		if lookUpVarKind(key, *entry) == bytecode.VarKindScope {
			varOffset = bytecode.VarOffsetForScope(symbolTable.TakeNextScopeOffset())
			hasCapturedVariables = true
		} else {
			var local *RegisterID
			if scopeRegisterType == scopeRegisterBlock {
				local = g.newBlockScopeVariable()
				local.ref()
			} else {
				local = g.addVar()
			}
			varOffset = bytecode.VarOffsetForStack(local.VirtualRegister())
		}

		symbolTable.Add(key, bytecode.SymbolTableEntry{Offset: varOffset, ReadOnly: entry.IsConst()})

		// FIXME: only do this if there is an eval() within a nested scope.
		privateEnvironment := lexicalVariables.PrivateNameEnvironment()
		if privateEnvironment == nil {
			continue
		}
		if p, ok := privateEnvironment.Find(key); ok {
			symbolTable.AddPrivateName(key, *p)
		}
	}
	return hasCapturedVariables
}

// emitPrefillStackTDZVariables puts the TDZ marker into stack-allocated
// lexical variables; scope variables get it when the environment is created.
func (g *Generator) emitPrefillStackTDZVariables(lexicalVariables *parser.VariableEnvironment, symbolTable *bytecode.SymbolTable) {
	for key, entry := range lexicalVariables.All {
		if entry.IsImported() && !entry.IsImportedNamespace() {
			continue
		}
		if entry.IsFunction() {
			continue
		}
		e, ok := symbolTable.Get(key)
		if !ok {
			panic("bytecompiler: lexical variable missing from symbol table")
		}
		if e.Offset.IsScope() {
			continue
		}
		g.moveEmptyValue(g.registerFor(e.Offset.StackOffset()))
	}
}

func (g *Generator) pushLexicalScope(node *parser.VariableEnvironmentNode, scopeType ScopeType, tdzCheckOptimization TDZCheckOptimization,
	nestedScopeType NestedScopeType, constantSymbolTableResult **RegisterID, shouldInitializeBlockScopedFunctions bool) {
	environment := &node.LexicalVariables
	var constantSymbolTableResultTemp *RegisterID
	g.pushLexicalScopeInternal(environment, tdzCheckOptimization, nestedScopeType, &constantSymbolTableResultTemp, underTDZ, scopeType, scopeRegisterBlock)

	if shouldInitializeBlockScopedFunctions {
		g.initializeBlockScopedFunctions(environment, node.FunctionStack, constantSymbolTableResultTemp)
	}

	if constantSymbolTableResult != nil && constantSymbolTableResultTemp != nil {
		*constantSymbolTableResult = constantSymbolTableResultTemp
	}
}

func (g *Generator) pushClassHeadLexicalScope(environment *parser.VariableEnvironment) {
	g.pushLexicalScopeInternal(environment, TDZCheckOptimize, NestedScopeIsNested, nil, underTDZ, ScopeTypeLetConstScope, scopeRegisterBlock)
}

func (g *Generator) popClassHeadLexicalScope(environment *parser.VariableEnvironment) {
	g.popLexicalScopeInternal(environment)
}

func (g *Generator) pushLexicalScopeInternal(environment *parser.VariableEnvironment, tdzCheckOptimization TDZCheckOptimization, nestedScopeType NestedScopeType,
	constantSymbolTableResult **RegisterID, tdzRequirement tdzRequirement, scopeType ScopeType, scopeRegisterType scopeRegisterType) {
	if environment.Size() == 0 {
		return
	}

	if g.shouldEmitDebugHooks() {
		environment.MarkAllVariablesAsCaptured()
	}

	symbolTable := bytecode.NewSymbolTable()
	switch scopeType {
	case ScopeTypeCatchScope:
		symbolTable.ScopeType = bytecode.CatchScope
	case ScopeTypeCatchScopeWithSimpleParameter:
		symbolTable.ScopeType = bytecode.CatchScopeWithSimpleParameter
	case ScopeTypeLetConstScope, ScopeTypeClassScope:
		symbolTable.ScopeType = bytecode.LexicalScope
	case ScopeTypeFunctionNameScope:
		symbolTable.ScopeType = bytecode.FunctionNameScope
	}

	if nestedScopeType == NestedScopeIsNested {
		symbolTable.NestedLexicalScope = true
	}

	lookUpVarKind := func(_ ident.Identifier, entry parser.VariableEnvironmentEntry) bytecode.VarKind {
		if entry.IsCaptured() {
			return bytecode.VarKindScope
		}
		return bytecode.VarKindStack
	}

	hasCapturedVariables := g.instantiateLexicalVariables(environment, scopeType, symbolTable, scopeRegisterType, lookUpVarKind)

	var newScope, constantSymbolTable *RegisterID
	symbolTableConstantIndex := 0
	if g.shouldEmitTypeProfilerHooks() {
		constantSymbolTable = g.addConstantValue(symbolTable, bytecode.SourceCodeRepresentationOther)
		symbolTableConstantIndex = constantSymbolTable.Index()
	}
	if hasCapturedVariables {
		if scopeRegisterType == scopeRegisterBlock {
			newScope = g.newBlockScopeVariable()
			newScope.ref()
		} else {
			newScope = g.addVar()
		}
		if constantSymbolTable == nil {
			constantSymbolTable = g.addConstantValue(symbolTable.CloneScopePart(), bytecode.SourceCodeRepresentationOther)
			symbolTableConstantIndex = constantSymbolTable.Index()
		}
		if constantSymbolTableResult != nil {
			*constantSymbolTableResult = constantSymbolTable
		}

		var initialValue bytecode.Constant = bytecode.Undefined{}
		if tdzRequirement == underTDZ {
			initialValue = bytecode.Empty{}
		}
		bytecode.EmitCreateLexicalEnvironment(g, vr(newScope), vr(g.scopeRegister), bytecode.VirtualRegister(symbolTableConstantIndex),
			vr(g.addConstantValue(initialValue, bytecode.SourceCodeRepresentationOther)))

		g.move(g.scopeRegister, newScope)

		g.pushLocalControlFlowScope()
	}

	g.lexicalScopeStack = append(g.lexicalScopeStack, lexicalScopeStackEntry{symbolTable, newScope, false, symbolTableConstantIndex})
	g.pushTDZVariables(environment, tdzCheckOptimization, tdzRequirement)

	if tdzRequirement == underTDZ {
		g.emitPrefillStackTDZVariables(environment, symbolTable)
	}
}

// initializeBlockScopedFunctions turns block-level function declarations
// into let bindings initialized at the start of the block (without TDZ):
//
//	function foo() {
//	    if (c) {
//	        function foo() { ... }
//	        if (bar) { ... }
//	        function baz() { ... }
//	    }
//	}
//
// becomes
//
//	function foo() {
//	    if (c) {
//	        let foo = function foo() { ... }
//	        let baz = function baz() { ... }
//	        if (bar) { ... }
//	    }
//	}
func (g *Generator) initializeBlockScopedFunctions(environment *parser.VariableEnvironment, functionStack parser.FunctionStack, constantSymbolTable *RegisterID) {
	defer g.refScope()()

	if environment.Size() == 0 {
		if len(functionStack) != 0 {
			panic("bytecompiler: block functions without lexical environment")
		}
		return
	}

	if len(functionStack) == 0 {
		return
	}

	top := g.lexicalScopeStack[len(g.lexicalScopeStack)-1]
	symbolTable := top.symbolTable
	scope := top.scope
	temp := g.keep(g.newTemporary())
	symbolTableIndex := 0
	if constantSymbolTable != nil {
		symbolTableIndex = constantSymbolTable.Index()
	}
	for _, function := range functionStack {
		name := function.Ident
		if e, ok := environment.Find(name); !ok || !e.IsFunction() {
			panic("bytecompiler: block function is not declared as a function")
		}
		entry, ok := symbolTable.Get(name)
		if !ok {
			panic("bytecompiler: block function missing from symbol table")
		}
		g.emitNewFunctionExpressionCommon(temp, function)
		g.emitPutToScope(scope, g.variableForLocalEntry(name, entry, symbolTableIndex, true), temp, bytecode.DoNotThrowIfNotFound, bytecode.InitializationModeInitialization)
	}
}

// HoistSloppyModeFunctionIfNecessary copies a block-level function in sloppy
// mode to the var of the same name (Annex B.3.3).
func (g *Generator) hoistSloppyModeFunctionIfNecessary(metadata *parser.FunctionMetadataNode) {
	if !metadata.IsSloppyModeHoistedFunction {
		return
	}
	defer g.refScope()()

	functionName := metadata.Ident

	if containsIdent(g.generatorOrAsyncWrapperFunctionParameterNames, functionName) {
		return
	}

	currentFunctionVariable := g.variable(functionName, ThisResolutionLocal)
	var currentValue *RegisterID
	if local := currentFunctionVariable.Local(); local != nil {
		currentValue = g.keep(local)
	} else {
		scope := g.keep(g.emitResolveScope(nil, currentFunctionVariable))
		currentValue = g.keep(g.emitGetFromScope(g.newTemporary(), scope, currentFunctionVariable, bytecode.DoNotThrowIfNotFound))
	}

	switch g.codeType {
	case bytecode.FunctionCode:
		varScope := g.lexicalScopeStack[g.varScopeLexicalScopeStackIndex]
		entry, ok := varScope.symbolTable.Get(functionName)
		if functionName == ident.Arguments && !ok {
			// With a non-simple parameter list "arguments" may live in the
			// parameter scope, e.g.
			//   function foo(x = arguments) { { function arguments() { } } }
			varScope = g.lexicalScopeStack[g.varScopeLexicalScopeStackIndex-1]
			entry, ok = varScope.symbolTable.Get(functionName)
		}
		if !ok {
			panic("bytecompiler: hoisted function has no var binding")
		}
		g.emitPutToScope(varScope.scope, g.variableForLocalEntry(functionName, entry, varScope.symbolTableConstantIndex, false), currentValue,
			bytecode.DoNotThrowIfNotFound, bytecode.InitializationModeNotInitialization)

	case bytecode.GlobalCode, bytecode.EvalCode:
		scopeID := g.keep(g.emitResolveScopeForHoistingFuncDeclInEval(nil, functionName))
		isNotVarScopeLabel := g.newLabel()
		g.emitJumpIfTrue(g.emitIsUndefined(g.newTemporary(), scopeID), isNotVarScopeLabel)
		// Put to outer scope.
		g.emitPutToScopeDynamic(scopeID, functionName, currentValue, bytecode.DoNotThrowIfNotFound, bytecode.InitializationModeNotInitialization)
		g.emitLabel(isNotVarScopeLabel)

	case bytecode.ModuleCode:
		panic("bytecompiler: sloppy mode function hoisting in module code")
	}
}

func (g *Generator) emitResolveScopeForHoistingFuncDeclInEval(dst *RegisterID, property ident.Identifier) *RegisterID {
	result := g.keep(g.finalDestination(dst))
	scope := g.keep(g.newTemporary())
	if g.topLevelScopeRegister != nil {
		g.move(scope, g.topLevelScopeRegister)
	} else {
		bytecode.EmitGetScope(g, vr(scope))
	}
	bytecode.EmitResolveScopeForHoistingFuncDeclInEval(g, vr(g.kill(result)), vr(scope), g.addConstant(property))
	return result
}

func (g *Generator) popLexicalScope(node *parser.VariableEnvironmentNode) {
	g.popLexicalScopeInternal(&node.LexicalVariables)
}

// popLexicalScopeInternal only makes sense for block scopes (it releases
// their registers).
func (g *Generator) popLexicalScopeInternal(environment *parser.VariableEnvironment) {
	if environment.Size() == 0 {
		return
	}

	if g.shouldEmitDebugHooks() {
		environment.MarkAllVariablesAsCaptured()
	}

	stackEntry := g.lexicalScopeStack[len(g.lexicalScopeStack)-1]
	g.lexicalScopeStack = g.lexicalScopeStack[:len(g.lexicalScopeStack)-1]
	symbolTable := stackEntry.symbolTable
	hasCapturedVariables := false
	for key, entry := range environment.All {
		if entry.IsCaptured() {
			hasCapturedVariables = true
			continue
		}
		e, ok := symbolTable.Get(key)
		if !ok || !e.Offset.IsStack() {
			panic("bytecompiler: uncaptured lexical variable is not on the stack")
		}
		g.registerFor(e.Offset.StackOffset()).deref()
	}

	if hasCapturedVariables {
		g.emitGetParentScope(g.scopeRegister, stackEntry.scope)
		g.popLocalControlFlowScope()
		stackEntry.scope.deref()
	}

	g.tdzStack = g.tdzStack[:len(g.tdzStack)-1]
}

// prepareLexicalScopeForNextForLoopIteration gives each iteration of a for
// loop whose header variables are captured a fresh activation holding the
// previous iteration's values.
func (g *Generator) prepareLexicalScopeForNextForLoopIteration(node *parser.VariableEnvironmentNode, loopSymbolTable *RegisterID) {
	environment := &node.LexicalVariables
	if environment.Size() == 0 {
		return
	}
	if g.shouldEmitDebugHooks() {
		environment.MarkAllVariablesAsCaptured()
	}
	if !environment.HasCapturedVariables() {
		return
	}

	stackEntry := g.lexicalScopeStack[len(g.lexicalScopeStack)-1]
	symbolTable := stackEntry.symbolTable
	loopScope := stackEntry.scope

	type transition struct {
		value *RegisterID
		id    ident.Identifier
	}
	var activationValuesToCopyOver []transition
	for id, entry := range symbolTable.All {
		if !entry.Offset.IsScope() {
			continue
		}
		transitionValue := g.newBlockScopeVariable()
		transitionValue.ref()
		g.emitGetFromScope(transitionValue, loopScope, g.variableForLocalEntry(id, *entry, loopSymbolTable.Index(), true), bytecode.DoNotThrowIfNotFound)
		activationValuesToCopyOver = append(activationValuesToCopyOver, transition{transitionValue, id})
	}

	// Each iteration needs a new activation in the same register as the old
	// one: the body is compiled assuming the scope register is fixed.
	g.emitGetParentScope(g.scopeRegister, loopScope)

	bytecode.EmitCreateLexicalEnvironment(g, vr(loopScope), vr(g.scopeRegister), vr(loopSymbolTable),
		vr(g.addConstantValue(bytecode.Empty{}, bytecode.SourceCodeRepresentationOther)))

	g.move(g.scopeRegister, loopScope)

	for _, t := range activationValuesToCopyOver {
		entry, ok := symbolTable.Get(t.id)
		if !ok {
			panic("bytecompiler: loop variable missing from symbol table")
		}
		g.emitPutToScope(loopScope, g.variableForLocalEntry(t.id, entry, loopSymbolTable.Index(), true), t.value,
			bytecode.DoNotThrowIfNotFound, bytecode.InitializationModeNotInitialization)
		t.value.deref()
	}
}

// variable resolves property statically if possible.
func (g *Generator) variable(property ident.Identifier, thisResolutionType ThisResolutionType) Variable {
	if property == ident.ThisPrivateName && thisResolutionType == ThisResolutionLocal {
		return Variable{
			ident: property, offset: bytecode.VarOffsetForStack(g.thisRegister.VirtualRegister()), local: &g.thisRegister,
			readOnly: true, kind: SpecialVariable,
		}
	}

	// Lookups resolve statically unless they pass a "with" scope:
	//
	//	{ let x; with (o) { doSomethingWith(x); } }          // dynamic
	//	{ let x; with (o) { let x; doSomethingWith(x); } }   // static
	for i := len(g.lexicalScopeStack) - 1; i >= 0; i-- {
		stackEntry := &g.lexicalScopeStack[i]
		if stackEntry.isWithScope {
			return unresolvedVariable(property)
		}
		symbolTable := stackEntry.symbolTable
		entry, ok := symbolTable.Get(property)
		if !ok {
			continue
		}
		resultIsCallee := false
		if symbolTable.ScopeType == bytecode.FunctionNameScope {
			if g.usesSloppyEval {
				// An eval may have introduced a var with the function's name;
				// only a dynamic lookup can tell.
				return unresolvedVariable(property)
			}
			resultIsCallee = true
		}
		result := g.variableForLocalEntry(property, entry, stackEntry.symbolTableConstantIndex, symbolTable.ScopeType == bytecode.LexicalScope)
		if resultIsCallee {
			result.SetIsReadOnly()
		}
		return result
	}

	return unresolvedVariable(property)
}

func (g *Generator) variableForLocalEntry(property ident.Identifier, entry bytecode.SymbolTableEntry, symbolTableConstantIndex int, isLexicallyScoped bool) Variable {
	offset := entry.Offset
	var local *RegisterID
	if offset.IsStack() {
		local = g.registerFor(offset.StackOffset())
	}
	return Variable{
		ident: property, offset: offset, local: local, readOnly: entry.ReadOnly, kind: NormalVariable,
		symbolTableConstantIndex: symbolTableConstantIndex, isLexicallyScoped: isLexicallyScoped,
	}
}

// createVariable creates the variable, or checks that an existing one is
// compatible.
func (g *Generator) createVariable(property ident.Identifier, varKind bytecode.VarKind, symbolTable *bytecode.SymbolTable, mode existingVariableMode) {
	if entry, ok := symbolTable.Get(property); ok {
		if mode == ignoreExisting {
			return
		}
		// We can't change our minds about whether it's captured.
		if entry.Offset.Kind() != varKind {
			panic("bytecompiler: variable " + property.String() + " was already added as " + entry.Offset.String())
		}
		return
	}

	var varOffset bytecode.VarOffset
	if varKind == bytecode.VarKindScope {
		varOffset = bytecode.VarOffsetForScope(symbolTable.TakeNextScopeOffset())
	} else {
		varOffset = bytecode.VarOffsetForStack(bytecode.VirtualRegisterForLocal(len(g.calleeLocals)))
	}
	symbolTable.Add(property, bytecode.SymbolTableEntry{Offset: varOffset})

	if varKind == bytecode.VarKindStack {
		local := g.addVar()
		if local.VirtualRegister() != varOffset.StackOffset() {
			panic("bytecompiler: stack variable allocated at unexpected register")
		}
	}
}

// tryResolveVariable returns the variable that expr reads, if it is a plain
// name.
func (g *Generator) tryResolveVariable(expr parser.ExpressionNode) (Variable, bool) {
	switch e := expr.(type) {
	case *parser.ResolveNode:
		return g.variable(e.Ident, ThisResolutionLocal), true
	case *parser.AssignResolveNode:
		return g.variable(e.Ident, ThisResolutionLocal), true
	case *parser.ThisNode:
		// After ensureThis (which materializing |base| invokes), |this| is
		// in the local this-register.
		return g.variable(ident.ThisPrivateName, ThisResolutionLocal), true
	case *parser.CommaNode:
		return g.tryResolveVariable(e.Exprs[len(e.Exprs)-1])
	}
	return Variable{}, false
}

// resolveType is the least upper bound of the resolve type based on the
// local scopes; linking refines it with the intercepting scopes.
func (g *Generator) resolveType() bytecode.ResolveType {
	for i := len(g.lexicalScopeStack) - 1; i >= 0; i-- {
		if g.lexicalScopeStack[i].isWithScope {
			return bytecode.Dynamic
		}
		if g.usesSloppyEval && g.lexicalScopeStack[i].symbolTable.ScopeType == bytecode.FunctionNameScope {
			// Never assign to a function name scope.
			return bytecode.Dynamic
		}
	}

	if g.usesSloppyEval {
		return bytecode.GlobalPropertyWithVarInjectionChecks
	}
	return bytecode.GlobalProperty
}

func (g *Generator) emitResolveScope(dst *RegisterID, variable Variable) *RegisterID {
	switch variable.offset.Kind() {
	case bytecode.VarKindStack:
		return nil

	case bytecode.VarKindDirectArgument:
		return g.argumentsRegister

	case bytecode.VarKindScope:
		// This is the activation we allocated, not necessarily the current
		// scope.
		for i := len(g.lexicalScopeStack) - 1; i >= 0; i-- {
			stackEntry := &g.lexicalScopeStack[i]
			// No "with" scope may lie between us and a resolved scope.
			if stackEntry.isWithScope {
				panic("bytecompiler: scope variable resolved through a with scope")
			}
			if !stackEntry.symbolTable.Contains(variable.ident) {
				continue
			}
			if stackEntry.scope == nil {
				panic("bytecompiler: scope variable without scope register")
			}
			return stackEntry.scope
		}
		panic("bytecompiler: scope variable not found")

	default: // Invalid: non-local resolution.
		dst = g.tempDestination(dst)
		bytecode.EmitResolveScope(g, vr(g.kill(dst)), vr(g.scopeRegister), g.addConstant(variable.ident), g.resolveType(), uint32(g.localScopeDepth))
		return dst
	}
}

func (g *Generator) emitGetFromScope(dst, scope *RegisterID, variable Variable, resolveMode bytecode.ResolveMode) *RegisterID {
	switch variable.offset.Kind() {
	case bytecode.VarKindStack:
		return g.move(dst, variable.local)

	case bytecode.VarKindDirectArgument:
		bytecode.EmitGetFromArguments(g, vr(g.kill(dst)), vr(scope), uint32(variable.offset.CapturedArgumentsOffset()), g.nextValueProfileIndex())
		return dst

	default: // Scope or Invalid
		resolveType := bytecode.ResolvedClosureVar
		var offset uint32
		if variable.offset.IsScope() {
			offset = uint32(variable.offset.ScopeOffset())
		} else {
			resolveType = g.resolveType()
		}
		bytecode.EmitGetFromScope(g, vr(g.kill(dst)), vr(scope), g.addConstant(variable.ident),
			bytecode.MakeGetPutInfo(resolveMode, resolveType, bytecode.InitializationModeNotInitialization, g.ecmaMode),
			uint32(g.localScopeDepth), offset, g.nextValueProfileIndex())
		return dst
	}
}

func (g *Generator) emitPutToScope(scope *RegisterID, variable Variable, value *RegisterID, resolveMode bytecode.ResolveMode, initializationMode bytecode.InitializationMode) *RegisterID {
	switch variable.offset.Kind() {
	case bytecode.VarKindStack:
		g.move(variable.local, value)
		return value

	case bytecode.VarKindDirectArgument:
		bytecode.EmitPutToArguments(g, vr(scope), uint32(variable.offset.CapturedArgumentsOffset()), vr(value))
		return value

	default: // Scope or Invalid
		var getPutInfo bytecode.GetPutInfo
		var symbolTableOrScopeDepth bytecode.SymbolTableOrScopeDepth
		var offset uint32
		if variable.offset.IsScope() {
			offset = uint32(variable.offset.ScopeOffset())
			getPutInfo = bytecode.MakeGetPutInfo(resolveMode, bytecode.ResolvedClosureVar, initializationMode, g.ecmaMode)
			symbolTableOrScopeDepth = bytecode.SymbolTableOrScopeDepthForSymbolTable(bytecode.VirtualRegister(variable.symbolTableConstantIndex))
		} else {
			getPutInfo = bytecode.MakeGetPutInfo(resolveMode, g.resolveType(), initializationMode, g.ecmaMode)
			symbolTableOrScopeDepth = bytecode.SymbolTableOrScopeDepthForScopeDepth(uint32(g.localScopeDepth))
		}
		bytecode.EmitPutToScope(g, vr(scope), g.addConstant(variable.ident), vr(value), getPutInfo, symbolTableOrScopeDepth, offset)
		return value
	}
}

func (g *Generator) emitPutToScopeDynamic(scope *RegisterID, id ident.Identifier, value *RegisterID, resolveMode bytecode.ResolveMode, initializationMode bytecode.InitializationMode) *RegisterID {
	getPutInfo := bytecode.MakeGetPutInfo(resolveMode, bytecode.Dynamic, initializationMode, g.ecmaMode)
	bytecode.EmitPutToScope(g, vr(scope), g.addConstant(id), vr(value), getPutInfo, bytecode.SymbolTableOrScopeDepthForScopeDepth(uint32(g.localScopeDepth)), 0)
	return value
}

func (g *Generator) initializeVariable(variable Variable, value *RegisterID) *RegisterID {
	if !variable.offset.IsValid() {
		panic("bytecompiler: initializing an unresolved variable")
	}
	scope := g.emitResolveScope(nil, variable)
	return g.emitPutToScope(scope, variable, value, bytecode.ThrowIfNotFound, bytecode.InitializationModeNotInitialization)
}
