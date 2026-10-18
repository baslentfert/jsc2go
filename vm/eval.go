package vm

import (
	"strings"

	"github.com/baslentfert/jsc2go/bytecode"
	"github.com/baslentfert/jsc2go/bytecompiler"
	"github.com/baslentfert/jsc2go/ident"
	"github.com/baslentfert/jsc2go/parser"
)

// eval (Interpreter.cpp's eval and Interpreter::executeEval, PerformEval
// 19.2.1.1 and EvalDeclarationInstantiation 19.2.1.3).

// indirectEval is a call of %eval% that is not a direct eval.
func (vm *VM) indirectEval(r *Realm, x Value) Value {
	if !x.IsString() {
		return x
	}
	return vm.performEval(r, x.StringRef(), r.globalLexical, ObjectValue(r.GlobalThis), bytecompiler.EvalConfig{}, false)
}

// directEval is op_call_direct_eval when the callee is %eval%.
func (vm *VM) directEval(f *Frame, args []Value, this Value, scope *Scope, features parser.LexicallyScopedFeatures) Value {
	if len(args) == 0 {
		return Undefined
	}
	x := args[0]
	if !x.IsString() {
		return x
	}
	insideOrdinary := f.insideOrdinaryFunction()
	key := evalCacheKey{pc: f.pc, source: x.StringRef().String(), insideOrdinary: insideOrdinary}
	if code, ok := f.cb.evalCache[key]; ok {
		return vm.runEval(f.cb.realm, code, scope, this, insideOrdinary)
	}
	u := f.cb.u
	isArrowContext := u.IsArrowFunction() || u.Info.IsArrowFunctionContext
	derived := u.Info.DerivedContextType
	if !isArrowContext && u.Info.IsClassContext {
		if u.IsConstructor() {
			derived = parser.DerivedContextTypeDerivedConstructorContext
		} else {
			derived = parser.DerivedContextTypeDerivedMethodContext
		}
	}
	evalContext := parser.EvalContextTypeNone
	if u.ParseMode().IsFunction() || u.CodeType == bytecode.EvalCode {
		evalContext = u.Info.EvalContextType
	}
	cfg := bytecompiler.EvalConfig{
		LexicallyScopedFeatures:    features,
		EvalContextType:            evalContext,
		DerivedContextType:         derived,
		IsArrowFunctionContext:     isArrowContext,
		NeedsClassFieldInitializer: u.Info.NeedsClassFieldInitializer,
		PrivateBrandRequirement:    u.Info.PrivateBrandRequirement,
		IsInsideOrdinaryFunction:   insideOrdinary,
	}
	var tdz []ident.Identifier
	privateNames := &parser.PrivateNameEnvironment{}
	for s := scope; s != nil; s = s.parent {
		if s.kind != scopeLexical && s.kind != scopeModule || s.table == nil {
			continue
		}
		switch s.table.ScopeType {
		case bytecode.LexicalScope, bytecode.CatchScope, bytecode.CatchScopeWithSimpleParameter:
		default:
			if s.kind != scopeModule {
				continue
			}
		}
		for name := range s.table.All {
			tdz = append(tdz, name)
		}
		if s.table.HasPrivateNames() {
			for name, entry := range s.table.PrivateNames().All {
				if !privateNames.Contains(name) {
					e, _ := privateNames.Add(name)
					*e = entry
				}
			}
		}
	}
	if tdz == nil {
		tdz = []ident.Identifier{}
	}
	cfg.VariablesUnderTDZ = tdz
	if privateNames.Len() > 0 {
		cfg.PrivateNames = privateNames
	}
	code := vm.compileEval(x.StringRef(), cfg)
	// Like JSC, cache by call site and source (the scope chain at a call
	// site, and so the compilation, is fixed), except code with template
	// literals: every evaluation of those makes new template objects.
	if len(f.cb.evalCache) < maxEvalCacheEntries && !strings.ContainsRune(key.source, '`') {
		if f.cb.evalCache == nil {
			f.cb.evalCache = make(map[evalCacheKey]evalCode)
		}
		f.cb.evalCache[key] = code
	}
	return vm.runEval(f.cb.realm, code, scope, this, insideOrdinary)
}

func (vm *VM) performEval(r *Realm, src *String, scope *Scope, this Value, cfg bytecompiler.EvalConfig, insideOrdinary bool) Value {
	return vm.runEval(r, vm.compileEval(src, cfg), scope, this, insideOrdinary)
}

func (vm *VM) compileEval(src *String, cfg bytecompiler.EvalConfig) evalCode {
	provider := parser.NewSourceProviderUTF16(append([]uint16(nil), src.UTF16()...), "eval", parser.SourceTypeProgram)
	provider.Origin = vm.activeOrigin()
	source := parser.NewSourceCode(provider)
	ucb, err := bytecompiler.CompileEval(source, cfg, 0)
	if err != nil {
		vm.throwSyntaxError("%s", err.Message)
	}
	return evalCode{ucb, source}
}

// runEval runs compiled eval code (the rest of PerformEval).
func (vm *VM) runEval(r *Realm, code evalCode, scope *Scope, this Value, insideOrdinary bool) Value {
	ucb, source := code.ucb, code.source
	saved := vm.realm
	vm.realm = r
	defer func() { vm.realm = saved }()

	strict := ucb.LexicallyScopedFeatures&parser.StrictModeLexicallyScopedFeature != 0
	vars := ucb.EvalVariables
	funcs := ucb.FunctionDecls
	hoisting := ucb.FunctionHoistingCandidates
	if len(vars) > 0 || len(funcs) > 0 || len(hoisting) > 0 {
		var varScope *Scope
		if strict && (len(vars) > 0 || len(funcs) > 0) {
			table := bytecode.NewSymbolTable()
			table.ScopeType = bytecode.VarScope
			scope = &Scope{kind: scopeLexical, parent: scope, table: table, evalVars: r.newObject(nil)}
			varScope = scope
		} else {
			for s := scope; ; s = s.parent {
				if s.kind == scopeGlobalObject || s.kind == scopeLexical && s.table != nil && s.table.ScopeType == bytecode.VarScope {
					varScope = s
					break
				}
			}
		}
		if !strict {
			for _, name := range vars {
				if vm.resolveScopeForHoistingFuncDeclInEval(scope, name).IsUndefined() {
					vm.throwSyntaxError("Can't create duplicate variable in eval: '%s'", name)
				}
			}
			for _, fn := range funcs {
				if vm.resolveScopeForHoistingFuncDeclInEval(scope, fn.Name()).IsUndefined() {
					vm.throwSyntaxError("Can't create duplicate variable in eval: '%s'", fn.Name())
				}
			}
		}
		isGlobal := varScope.kind == scopeGlobalObject
		g := varScope.object
		if isGlobal {
			for _, fn := range funcs {
				if !vm.canDeclareGlobalFunction(g, IdentKey(fn.Name())) {
					vm.throwTypeError("Can't declare global function '%s'", fn.Name())
				}
			}
			if !g.IsExtensible() {
				for _, name := range vars {
					if !g.HasOwnProperty(IdentKey(name)) {
						vm.throwTypeError("Can't declare global variable '%s'", name)
					}
				}
			}
		}
		ensureBinding := func(name ident.Identifier) {
			if varScope.kind == scopeLexical {
				if _, _, ok := varScope.slotFor(name); ok {
					return
				}
				if varScope.evalVars == nil {
					varScope.evalVars = r.newObject(nil)
					vm.varInjection++ // resolve_scope caches must walk again
				}
				k := IdentKey(name)
				if !varScope.evalVars.HasOwnProperty(k) {
					varScope.evalVars.CreateDataProperty(k, Undefined)
				}
			}
		}
		if !strict {
			for _, name := range hoisting {
				if vm.resolveScopeForHoistingFuncDeclInEval(scope, name).IsUndefined() {
					continue
				}
				if isGlobal {
					if g.HasOwnProperty(IdentKey(name)) || g.IsExtensible() {
						vm.createGlobalVarBinding(g, IdentKey(name), true)
						r.varNames[name] = struct{}{}
					}
				} else {
					ensureBinding(name)
				}
			}
		}
		for _, fn := range funcs {
			if isGlobal {
				vm.createGlobalFunctionBinding(g, IdentKey(fn.Name()), Undefined, true)
				r.varNames[fn.Name()] = struct{}{}
			} else {
				ensureBinding(fn.Name())
			}
		}
		for _, name := range vars {
			if isGlobal {
				vm.createGlobalVarBinding(g, IdentKey(name), true)
				r.varNames[name] = struct{}{}
			} else {
				ensureBinding(name)
			}
		}
	}

	cb := r.link(ucb, source)
	frame := vm.newFrame(cb, nil, scope, this, nil)
	frame.insideOrdinary = insideOrdinary
	vm.enterCall()
	defer vm.leaveCall()
	return vm.execute(frame)
}

// resolveScopeForHoistingFuncDeclInEval (JSScope::resolveScopeForHoistingFuncDeclInEval)
// returns the var scope that a hoisted eval declaration binds in, or
// undefined if a lexical declaration conflicts.
func (vm *VM) resolveScopeForHoistingFuncDeclInEval(scope *Scope, name ident.Identifier) Value {
	s := scope
	for ; s.parent != nil; s = s.parent {
		if s.kind == scopeWith || s.kind == scopeLexical && s.table != nil && s.table.ScopeType == bytecode.CatchScopeWithSimpleParameter {
			continue
		}
		if vm.hasBinding(s, name) {
			break
		}
		if s.kind == scopeLexical && s.table != nil && s.table.ScopeType == bytecode.VarScope {
			break
		}
	}
	if s.kind == scopeGlobalObject || s.kind == scopeLexical && s.table != nil && s.table.ScopeType == bytecode.VarScope {
		return cellValue(s)
	}
	return Undefined
}

// insideOrdinaryFunction reports whether the frame's code is inside a
// non-arrow function (ScriptExecutable::isInsideOrdinaryFunction).
func (f *Frame) insideOrdinaryFunction() bool {
	if f.callee != nil {
		if j, ok := f.callee.fn.(*jsFunction); ok {
			return j.insideOrdinary
		}
	}
	return f.insideOrdinary
}
