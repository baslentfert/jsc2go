package vm

import (
	"github.com/baslentfert/jsc2go/bytecode"
	"github.com/baslentfert/jsc2go/ident"
)

// Scopes (JSScope and subclasses: JSLexicalEnvironment, JSWithScope,
// JSGlobalLexicalEnvironment, JSGlobalObject as a scope, JSModuleEnvironment).
//
// Statically resolved accesses (ClosureVar, ResolvedClosureVar) index slots
// directly; everything else is resolved by name, which is what JSC's slow
// paths do once watchpoints have fired.

type scopeKind uint8

const (
	scopeLexical scopeKind = iota
	scopeWith
	scopeGlobalLexical
	scopeGlobalObject
	scopeModule
)

// Scope is an environment record.
type Scope struct {
	kind   scopeKind
	parent *Scope
	table  *bytecode.SymbolTable
	slots  []Value
	// object is the binding object of with and global object scopes.
	object *Object
	// evalVars holds var bindings that sloppy direct eval added to a
	// function scope; they are deletable, so they live in an object.
	evalVars *Object
	// module is the module record of a module environment.
	module *moduleRecord
}

func newLexicalScope(parent *Scope, table *bytecode.SymbolTable, initial Value) *Scope {
	n := table.ScopeSize()
	var s *Scope
	if n > 0 && n <= scopeInline {
		// The scope and its slots in one allocation (create_lexical_
		// environment runs for every call of a function whose variables
		// closures capture).
		b := new(scopeBlock)
		s = &b.s
		s.slots = b.v[:n:n]
	} else {
		s = new(Scope)
		if n > 0 {
			s.slots = make([]Value, n)
		}
	}
	s.kind, s.parent, s.table = scopeLexical, parent, table
	for i := range s.slots {
		s.slots[i] = initial
	}
	return s
}

// scopeInline is the most slots a scope has in its own allocation.
const scopeInline = 4

// scopeBlock is a scope with its slots.
type scopeBlock struct {
	s Scope
	v [scopeInline]Value
}

func (s *Scope) Parent() *Scope { return s.parent }

// slotFor returns the slot of a scope-allocated variable in a lexical,
// global lexical or module scope.
func (s *Scope) slotFor(name ident.Identifier) (*Value, bytecode.SymbolTableEntry, bool) {
	if s.table == nil {
		return nil, bytecode.SymbolTableEntry{}, false
	}
	e, ok := s.table.Get(name)
	if !ok || !e.Offset.IsScope() {
		return nil, e, false
	}
	off := int(e.Offset.ScopeOffset())
	if off >= len(s.slots) {
		return nil, e, false
	}
	return &s.slots[off], e, true
}

// hasSlot reports whether s's symbol table binds name to a slot.
func hasSlot(s *Scope, name ident.Identifier) bool {
	_, _, ok := s.slotFor(name)
	return ok
}

// hasBinding is HasBinding of the environment record.
func (vm *VM) hasBinding(s *Scope, name ident.Identifier) bool {
	switch s.kind {
	case scopeLexical, scopeGlobalLexical:
		if _, _, ok := s.slotFor(name); ok {
			return true
		}
		return s.evalVars != nil && s.evalVars.HasOwnProperty(keyForIdentifier(name))
	case scopeModule:
		if _, _, ok := s.slotFor(name); ok {
			return true
		}
		return s.module != nil && s.module.hasImportBinding(name)
	case scopeWith:
		k := keyForIdentifier(name)
		if !s.object.HasProperty(k) {
			return false
		}
		unscopables := s.object.Get(SymKey(SymbolUnscopables), ObjectValue(s.object))
		if u := unscopables.AsObject(); u != nil {
			if u.Get(k, unscopables).ToBoolean() {
				return false
			}
		}
		return true
	case scopeGlobalObject:
		return s.object.HasProperty(keyForIdentifier(name))
	}
	return false
}

// resolveScope finds the scope that binds name, or the global object scope.
func (vm *VM) resolveScope(s *Scope, name ident.Identifier) *Scope {
	for ; ; s = s.parent {
		if s.parent == nil || vm.hasBinding(s, name) {
			return s
		}
	}
}

// skipScopes walks depth parents up.
func skipScopes(s *Scope, depth uint32) *Scope {
	for ; depth > 0; depth-- {
		s = s.parent
	}
	return s
}

func (vm *VM) throwTDZError(name ident.Identifier) {
	vm.throwReferenceError("Cannot access '%s' before initialization.", name.Description())
}

func (vm *VM) throwUndefinedVariableError(name ident.Identifier) {
	vm.throwReferenceError("Can't find variable: %s", name.Description())
}

// getFromScope reads name from the scope that resolveScope returned.
func (vm *VM) getFromScope(s *Scope, name ident.Identifier, mode bytecode.ResolveMode) Value {
	switch s.kind {
	case scopeLexical, scopeGlobalLexical, scopeModule:
		if slot, _, ok := s.slotFor(name); ok {
			// As in JSC only global lexical bindings are TDZ-checked here; other
			// reads are followed by check_tdz where needed.
			if slot.IsEmpty() && s.kind == scopeGlobalLexical {
				vm.throwTDZError(name)
			}
			return *slot
		}
		if s.kind == scopeModule && s.module != nil {
			if v, ok := s.module.getImportBinding(vm, name); ok {
				return v
			}
		}
		if s.evalVars != nil {
			k := keyForIdentifier(name)
			if s.evalVars.HasOwnProperty(k) {
				return s.evalVars.GetKey(k)
			}
		}
	case scopeWith, scopeGlobalObject:
		k := keyForIdentifier(name)
		if mode == bytecode.ThrowIfNotFound && !s.object.HasProperty(k) {
			vm.throwUndefinedVariableError(name)
		}
		return s.object.Get(k, ObjectValue(s.object))
	}
	if mode == bytecode.ThrowIfNotFound {
		vm.throwUndefinedVariableError(name)
	}
	return Undefined
}

// putToScope writes name in the scope that resolveScope returned.
func (vm *VM) putToScope(s *Scope, name ident.Identifier, v Value, info bytecode.GetPutInfo) {
	init := info.InitializationMode().IsInitialization()
	strict := info.ECMAMode().IsStrict()
	switch s.kind {
	case scopeLexical, scopeGlobalLexical, scopeModule:
		if slot, e, ok := s.slotFor(name); ok {
			if !init {
				// Like JSC, only global lexical bindings are TDZ-checked here; the
				// bytecode has explicit check_tdz for the others (and stores the
				// arrow function context's |this| without one).
				if slot.IsEmpty() && s.kind == scopeGlobalLexical {
					vm.throwTDZError(name)
				}
				if e.ReadOnly {
					if strict || s.table.ScopeType != bytecode.FunctionNameScope {
						vm.throwTypeError("Attempted to assign to readonly property.")
					}
					return
				}
			}
			*slot = v
			return
		}
		if s.evalVars != nil {
			k := keyForIdentifier(name)
			if s.evalVars.HasOwnProperty(k) {
				s.evalVars.Set(k, v, ObjectValue(s.evalVars))
				return
			}
			// SetMutableBinding on an environment whose (deletable, eval
			// created) binding was deleted: sloppy code recreates it.
			if strict {
				vm.throwUndefinedVariableError(name)
			}
			s.evalVars.CreateDataProperty(k, v)
			return
		}
		if s.kind == scopeModule && s.module != nil && s.module.hasImportBinding(name) {
			vm.throwTypeError("Attempted to assign to readonly property.")
		}
	case scopeWith:
		k := keyForIdentifier(name)
		if !s.object.HasProperty(k) && strict {
			vm.throwUndefinedVariableError(name)
		}
		if !s.object.Set(k, v, ObjectValue(s.object)) && strict {
			vm.throwTypeError("Attempted to assign to readonly property.")
		}
		return
	case scopeGlobalObject:
		k := keyForIdentifier(name)
		if info.ResolveMode() == bytecode.ThrowIfNotFound && !s.object.HasProperty(k) {
			vm.throwUndefinedVariableError(name)
		}
		if !s.object.Set(k, v, ObjectValue(s.object)) && strict {
			vm.throwTypeError("Attempted to assign to readonly property.")
		}
		return
	}
	if info.ResolveMode() == bytecode.ThrowIfNotFound {
		vm.throwUndefinedVariableError(name)
	}
}

// newWithScope is op_push_with_scope.
func (vm *VM) newWithScope(parent *Scope, v Value) *Scope {
	return &Scope{kind: scopeWith, parent: parent, object: vm.ToObject(v)}
}
