package bytecompiler

import (
	"github.com/baslentfert/jsc2go/bytecode"
	"github.com/baslentfert/jsc2go/ident"
	"github.com/baslentfert/jsc2go/parser"
)

// Property access, object creation and TDZ helpers (BytecodeGenerator.cpp).

func (g *Generator) emitMove(dst, src *RegisterID) *RegisterID {
	g.staticPropertyAnalyzer.mov(g, dst, src)
	if g.canDoPeepholeOptimization() && g.lastOpcodeID == bytecode.OpMovID {
		if g.lastOperand(bytecode.OpMovDstIndex) == dst.VirtualRegister() {
			g.rewind()
		}
	}
	bytecode.EmitMov(g, vr(dst), vr(src))
	return dst
}

func (g *Generator) emitInstanceof(dst, value, constructor, hasInstanceOrPrototype *RegisterID) *RegisterID {
	bytecode.EmitInstanceof(g, vr(dst), vr(value), vr(constructor), vr(hasInstanceOrPrototype), g.nextValueProfileIndex(), g.nextValueProfileIndex())
	return dst
}

// findForInContext returns the innermost for-in loop whose property
// variable is property.
func (g *Generator) findForInContext(property *RegisterID) *forInContext {
	for i := len(g.forInContextStack) - 1; i >= 0; i-- {
		if c := g.forInContextStack[i]; c.Local() == property {
			return c
		}
	}
	return nil
}

func (g *Generator) emitInByVal(dst, property, base *RegisterID) *RegisterID {
	if context := g.findForInContext(property); context != nil {
		bytecode.EmitEnumeratorInByVal(g, vr(dst), vr(base), vr(context.Mode()), vr(property), vr(context.PropertyOffset()), vr(context.Enumerator()))
		context.addInInst(g.lastInstruction, property.Index())
		return dst
	}
	bytecode.EmitInByVal(g, vr(dst), vr(base), vr(property))
	return dst
}

func (g *Generator) emitInByID(dst, base *RegisterID, property ident.Identifier) *RegisterID {
	bytecode.EmitInById(g, vr(dst), vr(base), g.addConstant(property))
	return dst
}

func (g *Generator) emitGetLength(dst, base *RegisterID) *RegisterID {
	bytecode.EmitGetLength(g, vr(g.kill(dst)), vr(base), g.nextValueProfileIndex())
	return dst
}

func (g *Generator) emitGetByID(dst, base *RegisterID, property ident.Identifier) *RegisterID {
	if property == ident.Length {
		bytecode.EmitGetLength(g, vr(g.kill(dst)), vr(base), g.nextValueProfileIndex())
	} else {
		bytecode.EmitGetById(g, vr(g.kill(dst)), vr(base), g.addConstant(property), g.nextValueProfileIndex())
	}
	return dst
}

func (g *Generator) emitGetByIDWithThis(dst, base, thisVal *RegisterID, property ident.Identifier) *RegisterID {
	bytecode.EmitGetByIdWithThis(g, vr(g.kill(dst)), vr(base), vr(thisVal), g.addConstant(property), g.nextValueProfileIndex())
	return dst
}

func (g *Generator) emitDirectGetByID(dst, base *RegisterID, property ident.Identifier) *RegisterID {
	bytecode.EmitGetByIdDirect(g, vr(g.kill(dst)), vr(base), g.addConstant(property), g.nextValueProfileIndex())
	return dst
}

func (g *Generator) emitPutByID(base *RegisterID, property ident.Identifier, value *RegisterID) *RegisterID {
	propertyIndex := g.addConstant(property)
	g.staticPropertyAnalyzer.putByID(base, propertyIndex)
	bytecode.EmitPutById(g, vr(base), propertyIndex, vr(value), bytecode.PutByIdFlagsCreate(g.ecmaMode)) // not direct
	return value
}

func (g *Generator) emitPutByIDWithThis(base, thisValue *RegisterID, property ident.Identifier, value *RegisterID) *RegisterID {
	propertyIndex := g.addConstant(property)
	bytecode.EmitPutByIdWithThis(g, vr(base), vr(thisValue), propertyIndex, vr(value), g.ecmaMode)
	return value
}

func (g *Generator) emitDirectPutByID(base *RegisterID, property ident.Identifier, value *RegisterID) *RegisterID {
	propertyIndex := g.addConstant(property)
	g.staticPropertyAnalyzer.putByID(base, propertyIndex)
	bytecode.EmitPutById(g, vr(base), propertyIndex, vr(value), bytecode.PutByIdFlagsCreateDirect(g.ecmaMode))
	return value
}

func (g *Generator) emitPutGetterByID(base *RegisterID, property ident.Identifier, attributes uint32, getter *RegisterID) {
	propertyIndex := g.addConstant(property)
	g.staticPropertyAnalyzer.putByID(base, propertyIndex)
	bytecode.EmitPutGetterById(g, vr(base), propertyIndex, attributes, vr(getter))
}

func (g *Generator) emitPutSetterByID(base *RegisterID, property ident.Identifier, attributes uint32, setter *RegisterID) {
	propertyIndex := g.addConstant(property)
	g.staticPropertyAnalyzer.putByID(base, propertyIndex)
	bytecode.EmitPutSetterById(g, vr(base), propertyIndex, attributes, vr(setter))
}

func (g *Generator) emitPutGetterSetter(base *RegisterID, property ident.Identifier, attributes uint32, getter, setter *RegisterID) {
	propertyIndex := g.addConstant(property)
	g.staticPropertyAnalyzer.putByID(base, propertyIndex)
	bytecode.EmitPutGetterSetterById(g, vr(base), propertyIndex, attributes, vr(getter), vr(setter))
}

func (g *Generator) emitPutGetterByVal(base, property *RegisterID, attributes uint32, getter *RegisterID) {
	bytecode.EmitPutGetterByVal(g, vr(base), vr(property), attributes, vr(getter))
}

func (g *Generator) emitPutSetterByVal(base, property *RegisterID, attributes uint32, setter *RegisterID) {
	bytecode.EmitPutSetterByVal(g, vr(base), vr(property), attributes, vr(setter))
}

func (g *Generator) emitPutGeneratorFields(nextFunction *RegisterID) {
	g.emitPutInternalField(g.generatorRegister, generatorFieldNext, nextFunction)

	// Arrow functions in derived constructors don't store 'this': it may not
	// be initialized yet if super is called later.
	if !(g.isDerivedConstructorContext() && g.codeBlock.ParseMode() == parser.AsyncArrowFunctionMode) {
		g.emitPutInternalField(g.generatorRegister, generatorFieldThis, &g.thisRegister)
	}
}

func (g *Generator) emitPutAsyncGeneratorFields(nextFunction *RegisterID) {
	g.emitPutInternalField(g.generatorRegister, generatorFieldNext, nextFunction)
	g.emitPutInternalField(g.generatorRegister, generatorFieldThis, &g.thisRegister)
}

func (g *Generator) emitDeleteByID(dst, base *RegisterID, property ident.Identifier) *RegisterID {
	bytecode.EmitDelById(g, vr(dst), vr(base), g.addConstant(property), g.ecmaMode)
	return dst
}

func (g *Generator) emitGetByVal(dst, base, property *RegisterID) *RegisterID {
	if context := g.findForInContext(property); context != nil {
		bytecode.EmitEnumeratorGetByVal(g, vr(g.kill(dst)), vr(base), vr(context.Mode()), vr(property), vr(context.PropertyOffset()), vr(context.Enumerator()), g.nextValueProfileIndex())
		context.addGetInst(g.lastInstruction, property.Index())
		return dst
	}
	bytecode.EmitGetByVal(g, vr(g.kill(dst)), vr(base), vr(property), g.nextValueProfileIndex())
	return dst
}

func (g *Generator) emitGetByValWithThis(dst, base, thisValue, property *RegisterID) *RegisterID {
	bytecode.EmitGetByValWithThis(g, vr(g.kill(dst)), vr(base), vr(thisValue), vr(property), g.nextValueProfileIndex())
	return dst
}

func (g *Generator) emitGetPrototypeOf(dst, value *RegisterID) *RegisterID {
	bytecode.EmitGetPrototypeOf(g, vr(dst), vr(value), g.nextValueProfileIndex())
	return dst
}

func (g *Generator) emitPutByVal(base, property, value *RegisterID) *RegisterID {
	if context := g.findForInContext(property); context != nil {
		return g.emitEnumeratorPutByVal(context, base, property, value)
	}
	bytecode.EmitPutByVal(g, vr(base), vr(property), vr(value), g.ecmaMode)
	return value
}

func (g *Generator) emitPutByValWithThis(base, thisValue, property, value *RegisterID) *RegisterID {
	bytecode.EmitPutByValWithThis(g, vr(base), vr(thisValue), vr(property), vr(value), g.ecmaMode)
	return value
}

func (g *Generator) emitPutByValWithECMAMode(base, thisValue, property, value *RegisterID, ecmaMode bytecode.ECMAMode) *RegisterID {
	bytecode.EmitPutByValWithThis(g, vr(base), vr(thisValue), vr(property), vr(value), ecmaMode)
	return value
}

func (g *Generator) emitEnumeratorPutByVal(context *forInContext, base, property, value *RegisterID) *RegisterID {
	bytecode.EmitEnumeratorPutByVal(g, vr(base), vr(context.Mode()), vr(property), vr(context.PropertyOffset()), vr(context.Enumerator()), vr(value), g.ecmaMode)
	context.addPutInst(g.lastInstruction, property.Index())
	return value
}

func (g *Generator) emitGetPrivateName(dst, base, property *RegisterID) *RegisterID {
	bytecode.EmitGetPrivateName(g, vr(dst), vr(base), vr(property), g.nextValueProfileIndex())
	return dst
}

func (g *Generator) emitHasPrivateName(dst, base, property *RegisterID) *RegisterID {
	bytecode.EmitHasPrivateName(g, vr(dst), vr(base), vr(property))
	return dst
}

func (g *Generator) emitHasStructureWithFlags(dst, src *RegisterID, flags uint32) *RegisterID {
	bytecode.EmitHasStructureWithFlags(g, vr(dst), vr(src), flags)
	return dst
}

func (g *Generator) emitDirectPutByVal(base, property, value *RegisterID) *RegisterID {
	bytecode.EmitPutByValDirect(g, vr(base), vr(property), vr(value), g.ecmaMode)
	return value
}

func (g *Generator) emitDeleteByVal(dst, base, property *RegisterID) *RegisterID {
	bytecode.EmitDelByVal(g, vr(dst), vr(base), vr(property), g.ecmaMode)
	return dst
}

func (g *Generator) emitGetInternalField(dst, base *RegisterID, index uint32) *RegisterID {
	bytecode.EmitGetInternalField(g, vr(dst), vr(base), index, g.nextValueProfileIndex())
	return dst
}

func (g *Generator) emitPutInternalField(base *RegisterID, index uint32, value *RegisterID) *RegisterID {
	bytecode.EmitPutInternalField(g, vr(base), index, vr(value))
	return value
}

func (g *Generator) emitDefinePrivateField(base, property, value *RegisterID) *RegisterID {
	bytecode.EmitPutPrivateName(g, vr(base), vr(property), vr(value), bytecode.PrivateFieldPutKindDefine)
	return value
}

func (g *Generator) emitCreatePrivateBrand(divot, divotStart, divotEnd parser.TextPosition) {
	defer g.refScope()()
	createPrivateSymbol := g.keep(g.moveLinkTimeConstant(nil, bytecode.LinkTimeConstantCreatePrivateSymbol))

	arguments := g.newCallArguments(nil, 1)
	g.emitLoad(arguments.thisRegister(), bytecode.Undefined{})
	g.emitLoad(arguments.argumentRegister(0), bytecode.String(""))
	newSymbol := g.emitCall(g.finalDestination(nil, createPrivateSymbol), createPrivateSymbol, NoExpectedFunction, arguments, divot, divotStart, divotEnd, DebuggableCallNo)

	privateBrandVar := g.variable(ident.PrivateBrandPrivateName, ThisResolutionLocal)
	g.emitPutToScope(g.scopeRegister, privateBrandVar, newSymbol, bytecode.DoNotThrowIfNotFound, bytecode.InitializationModeConstInitialization)
}

func (g *Generator) emitInstallPrivateBrand(target *RegisterID) {
	defer g.refScope()()
	privateBrandVar := g.variable(ident.PrivateBrandPrivateName, ThisResolutionLocal)
	privateBrandVarScope := g.keep(g.emitResolveScope(nil, privateBrandVar))
	privateBrandSymbol := g.emitGetPrivateBrand(g.newTemporary(), privateBrandVarScope, false)
	bytecode.EmitSetPrivateBrand(g, vr(target), vr(privateBrandSymbol))
}

func (g *Generator) emitInstallPrivateClassBrand(target *RegisterID) {
	privateBrandVar := g.variable(ident.PrivateClassBrandPrivateName, ThisResolutionLocal)
	g.emitPutToScope(g.scopeRegister, privateBrandVar, target, bytecode.DoNotThrowIfNotFound, bytecode.InitializationModeConstInitialization)
}

func (g *Generator) emitGetPrivateBrand(dst, scope *RegisterID, isStatic bool) *RegisterID {
	name, offset := ident.PrivateBrandPrivateName, uint32(privateBrandOffset)
	if isStatic {
		name, offset = ident.PrivateClassBrandPrivateName, privateClassBrandOffset
	}
	bytecode.EmitGetFromScope(g, vr(g.kill(dst)), vr(scope), g.addConstant(name),
		bytecode.MakeGetPutInfo(bytecode.ThrowIfNotFound, bytecode.ResolvedClosureVar, bytecode.InitializationModeNotInitialization, g.ecmaMode),
		0, offset, g.nextValueProfileIndex())
	return dst
}

func (g *Generator) emitPrivateFieldPut(base, property, value *RegisterID) *RegisterID {
	bytecode.EmitPutPrivateName(g, vr(base), vr(property), vr(value), bytecode.PrivateFieldPutKindSet)
	return value
}

func (g *Generator) emitHasPrivateBrand(dst, base, brand *RegisterID, isStatic bool) *RegisterID {
	if isStatic {
		defer g.refScope()()
		isObjectLabel := g.newLabel()
		g.emitJumpIfTrue(g.emitIsObject(g.newTemporary(), base), isObjectLabel)
		g.emitThrowTypeError("Cannot access static private method or accessor of a non-Object")
		g.emitLabel(isObjectLabel)
		g.emitEqualityOp(bytecode.OpStricteqID, dst, base, brand)
	} else {
		bytecode.EmitHasPrivateBrand(g, vr(dst), vr(base), vr(brand))
	}
	return dst
}

func (g *Generator) emitCheckPrivateBrand(base, brand *RegisterID, isStatic bool) {
	if isStatic {
		brandCheckOkLabel := g.newLabel()
		bytecode.EmitCheckTdz(g, vr(brand))
		g.emitJumpIfTrue(g.emitEqualityOp(bytecode.OpStricteqID, g.newTemporary(), base, brand), brandCheckOkLabel)
		g.emitThrowTypeError("Cannot access static private method or accessor")
		g.emitLabel(brandCheckOkLabel)
		return
	}
	bytecode.EmitCheckPrivateBrand(g, vr(base), vr(brand))
}

func (g *Generator) emitIdWithProfile(src *RegisterID, profile uint64) *RegisterID {
	bytecode.EmitIdentityWithProfile(g, vr(src), uint32(profile>>32), uint32(profile))
	return src
}

func (g *Generator) emitUnreachable() { bytecode.EmitUnreachable(g) }

func (g *Generator) emitGetArgument(dst *RegisterID, index int32) *RegisterID {
	bytecode.EmitGetArgument(g, vr(dst), index+1 /* including |this| */, g.nextValueProfileIndex())
	return dst
}

func (g *Generator) emitCreateThis(dst *RegisterID) *RegisterID {
	bytecode.EmitCreateThis(g, vr(dst), vr(dst), 0)
	g.staticPropertyAnalyzer.createThis(g, dst, g.lastInstruction)
	return dst
}

func (g *Generator) emitCreatePromise(dst, newTarget *RegisterID) *RegisterID {
	bytecode.EmitCreatePromise(g, vr(dst), vr(newTarget))
	return dst
}

func (g *Generator) emitNewPromise(dst *RegisterID) *RegisterID {
	bytecode.EmitNewPromise(g, vr(dst))
	return dst
}

func (g *Generator) emitCreateGenerator(dst, newTarget *RegisterID) *RegisterID {
	bytecode.EmitCreateGenerator(g, vr(dst), vr(newTarget))
	return dst
}

func (g *Generator) emitNewGenerator(dst *RegisterID) *RegisterID {
	bytecode.EmitNewGenerator(g, vr(dst))
	return dst
}

func (g *Generator) emitNewAsyncFunctionGenerator(dst *RegisterID) *RegisterID {
	bytecode.EmitNewAsyncFunctionGenerator(g, vr(dst))
	return dst
}

func (g *Generator) emitCreateAsyncGenerator(dst, newTarget *RegisterID) *RegisterID {
	bytecode.EmitCreateAsyncGenerator(g, vr(dst), vr(newTarget))
	return dst
}

func (g *Generator) emitInstanceFieldInitializationIfNeeded(dst, constructor *RegisterID, divot, divotStart, divotEnd parser.TextPosition) *RegisterID {
	if !(g.isConstructor() || g.isDerivedConstructorContext()) || !g.needsClassFieldInitializer() {
		return dst
	}
	defer g.refScope()()

	initializer := g.keep(g.emitDirectGetByID(g.newTemporary(), constructor, ident.InstanceFieldInitializerPrivateName))
	args := g.newCallArguments(nil, 0)
	g.emitMove(args.thisRegister(), dst)
	g.emitCallIgnoreResult(g.newTemporary(), initializer, NoExpectedFunction, args, divot, divotStart, divotEnd, DebuggableCallNo)
	return dst
}

func (g *Generator) emitTDZCheck(target *RegisterID) { bytecode.EmitCheckTdz(g, vr(target)) }

func (g *Generator) needsTDZCheck(variable Variable) bool {
	for i := len(g.tdzStack) - 1; i >= 0; i-- {
		if level, ok := g.tdzStack[i].m.find(variable.ident); ok {
			return level != tdzNotNeeded
		}
	}
	for env := g.cachedParentTDZ; env != nil; env = env.Parent() {
		if env.Contains(variable.ident) {
			return true
		}
	}
	return false
}

func (g *Generator) emitTDZCheckIfNecessary(variable Variable, target, scope *RegisterID) {
	if !g.needsTDZCheck(variable) {
		return
	}
	if target != nil {
		g.emitTDZCheck(target)
		return
	}
	defer g.refScope()()
	result := g.keep(g.emitGetFromScope(g.newTemporary(), scope, variable, bytecode.DoNotThrowIfNotFound))
	g.emitTDZCheck(result)
}

func (g *Generator) liftTDZCheckIfPossible(variable Variable) {
	for i := len(g.tdzStack) - 1; i >= 0; i-- {
		m := &g.tdzStack[i].m
		if level, ok := m.find(variable.ident); ok {
			if level == tdzOptimize {
				m.set(variable.ident, tdzNotNeeded)
			}
			break
		}
	}
}

// getPrivateTraits must only be called for declared private names.
func (g *Generator) getPrivateTraits(id ident.Identifier) parser.PrivateNameEntry {
	for i := len(g.privateNamesStack) - 1; i >= 0; i-- {
		if e, ok := g.privateNamesStack[i].Find(id); ok {
			return *e
		}
	}
	panic("bytecompiler: undeclared private name " + id.String())
}

func (g *Generator) pushPrivateAccessNames(environment *parser.PrivateNameEnvironment) {
	if environment == nil || environment.Len() == 0 {
		return
	}
	g.privateNamesStack = append(g.privateNamesStack, environment.Clone())
}

func (g *Generator) popPrivateAccessNames() {
	g.privateNamesStack = g.privateNamesStack[:len(g.privateNamesStack)-1]
}

func (g *Generator) pushTDZVariables(environment *parser.VariableEnvironment, optimization TDZCheckOptimization, requirement tdzRequirement) {
	if environment.Size() == 0 {
		return
	}

	level := tdzNotNeeded
	if requirement == underTDZ {
		if optimization == TDZCheckOptimize {
			level = tdzOptimize
		} else {
			level = tdzDoNotOptimize
		}
	}

	var m tdzMap
	for key, entry := range environment.All {
		if entry.IsFunction() {
			m.set(key, tdzNotNeeded)
		} else {
			m.set(key, level)
		}
	}
	g.tdzStack = append(g.tdzStack, tdzStackEntry{m: m})
}

func (g *Generator) getParameterNames() []ident.Identifier {
	fn := g.scopeNode.(*parser.FunctionNode)
	var names []ident.Identifier
	for _, p := range fn.Parameters.Patterns {
		names = p.Pattern.CollectBoundIdentifiers(names)
	}
	return names
}

// getAvailablePrivateAccessNames flattens the private name stack, inner
// names first; it returns nil if there are none.
func (g *Generator) getAvailablePrivateAccessNames() *parser.PrivateNameEnvironment {
	var result parser.PrivateNameEnvironment
	for i := len(g.privateNamesStack) - 1; i >= 0; i-- {
		for key, value := range g.privateNamesStack[i].All {
			if e, isNew := result.Add(key); isNew {
				*e = value
			}
		}
	}
	if result.Len() == 0 {
		return nil
	}
	return &result
}

// getVariablesUnderTDZ returns (and caches) the chain of names that are in
// their TDZ, for functions created here.
func (g *Generator) getVariablesUnderTDZ() *bytecode.TDZEnvironmentLink {
	parent := g.cachedParentTDZ
	if len(g.tdzStack) == 0 {
		return parent
	}

	if last := g.tdzStack[len(g.tdzStack)-1].link; last != nil {
		return last
	}

	for i := range g.tdzStack {
		entry := &g.tdzStack[i]
		if entry.link == nil {
			var names []ident.Identifier
			for _, n := range entry.m.names {
				if entry.m.level[n] != tdzNotNeeded {
					names = append(names, n)
				}
			}
			entry.link = bytecode.NewTDZEnvironmentLink(names, parent)
		}
		parent = entry.link
	}
	return parent
}

// preserveTDZStack saves a deep copy of the TDZ stack.
func (g *Generator) preserveTDZStack(preserved *PreservedTDZStack) {
	preserved.stack = cloneTDZStack(g.tdzStack)
}

func (g *Generator) restoreTDZStack(preserved *PreservedTDZStack) {
	g.tdzStack = cloneTDZStack(preserved.stack)
}

func cloneTDZStack(stack []tdzStackEntry) []tdzStackEntry {
	out := make([]tdzStackEntry, len(stack))
	for i, e := range stack {
		m := tdzMap{names: append([]ident.Identifier(nil), e.m.names...), level: make(map[ident.Identifier]tdzNecessityLevel, len(e.m.level))}
		for k, v := range e.m.level {
			m.level[k] = v
		}
		out[i] = tdzStackEntry{m: m, link: e.link}
	}
	return out
}

func (g *Generator) emitNewObject(dst *RegisterID) *RegisterID {
	bytecode.EmitNewObject(g, vr(dst), 0)
	g.staticPropertyAnalyzer.newObject(g, dst, g.lastInstruction)
	return dst
}
