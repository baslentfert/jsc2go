package parser

import (
	"slices"

	"github.com/baslentfert/jsc2go/ident"
)

// Port of JavaScriptCore/parser/VariableEnvironment.{h,cpp}.

// orderedMap is an insertion-ordered map. JSC iterates its (unordered) hash
// maps when generating bytecode; keeping insertion order makes our output
// deterministic without changing semantics.
type orderedMap[V any] struct {
	keys   []ident.Identifier
	values []V
	index  map[ident.Identifier]int // only past smallSet keys
}

// smallSet is the size up to which orderedMap and identSet search their
// keys instead of keeping a map: most scopes declare and use only a few
// names, and a map for each was a fifth of what parsing allocated.
const smallSet = 8

// indexOf returns the position of key, or -1.
func (m *orderedMap[V]) indexOf(key ident.Identifier) int {
	if m.index != nil {
		if i, ok := m.index[key]; ok {
			return i
		}
		return -1
	}
	return slices.Index(m.keys, key)
}

// add inserts key with the zero value if absent and returns a pointer to its
// value and whether it was newly added (JSC's AddResult).
func (m *orderedMap[V]) add(key ident.Identifier) (*V, bool) {
	if i := m.indexOf(key); i >= 0 {
		return &m.values[i], false
	}
	m.keys = append(m.keys, key)
	var zero V
	m.values = append(m.values, zero)
	if m.index != nil {
		m.index[key] = len(m.keys) - 1
	} else if len(m.keys) > smallSet {
		m.index = make(map[ident.Identifier]int, 2*len(m.keys))
		for i, k := range m.keys {
			m.index[k] = i
		}
	}
	return &m.values[len(m.values)-1], true
}

func (m *orderedMap[V]) find(key ident.Identifier) (*V, bool) {
	if i := m.indexOf(key); i >= 0 {
		return &m.values[i], true
	}
	return nil, false
}

func (m *orderedMap[V]) contains(key ident.Identifier) bool { return m.indexOf(key) >= 0 }

func (m *orderedMap[V]) remove(key ident.Identifier) bool {
	i := m.indexOf(key)
	if i < 0 {
		return false
	}
	m.keys = slices.Delete(m.keys, i, i+1)
	m.values = slices.Delete(m.values, i, i+1)
	if m.index != nil {
		delete(m.index, key)
		for j := i; j < len(m.keys); j++ {
			m.index[m.keys[j]] = j
		}
	}
	return true
}

func (m *orderedMap[V]) len() int { return len(m.keys) }

func (m *orderedMap[V]) clone() orderedMap[V] {
	c := orderedMap[V]{keys: slices.Clone(m.keys), values: slices.Clone(m.values)}
	if m.index != nil {
		c.index = make(map[ident.Identifier]int, len(m.index))
		for k, v := range m.index {
			c.index[k] = v
		}
	}
	return c
}

// VariableEnvironmentEntry describes one binding as a set of trait bits.
type VariableEnvironmentEntry uint16

const (
	entryIsCaptured VariableEnvironmentEntry = 1 << iota
	entryIsConst
	entryIsVar
	entryIsLet
	entryIsExported
	entryIsImported
	entryIsImportedNamespace
	entryIsFunction
	entryIsParameter
	entryIsSloppyModeHoistedFunction
	entryIsPrivateField
	entryIsPrivateMethod
	entryIsPrivateGetter
	entryIsPrivateSetter
	entryIsFunctionDeclaration
	entryIsUsing
)

func (e VariableEnvironmentEntry) IsCaptured() bool          { return e&entryIsCaptured != 0 }
func (e VariableEnvironmentEntry) IsConst() bool             { return e&entryIsConst != 0 }
func (e VariableEnvironmentEntry) IsVar() bool               { return e&entryIsVar != 0 }
func (e VariableEnvironmentEntry) IsLet() bool               { return e&entryIsLet != 0 }
func (e VariableEnvironmentEntry) IsExported() bool          { return e&entryIsExported != 0 }
func (e VariableEnvironmentEntry) IsImported() bool          { return e&entryIsImported != 0 }
func (e VariableEnvironmentEntry) IsImportedNamespace() bool { return e&entryIsImportedNamespace != 0 }
func (e VariableEnvironmentEntry) IsFunction() bool          { return e&entryIsFunction != 0 }
func (e VariableEnvironmentEntry) IsFunctionDeclaration() bool {
	return e&entryIsFunctionDeclaration != 0
}
func (e VariableEnvironmentEntry) IsParameter() bool { return e&entryIsParameter != 0 }
func (e VariableEnvironmentEntry) IsSloppyModeHoistedFunction() bool {
	return e&entryIsSloppyModeHoistedFunction != 0
}
func (e VariableEnvironmentEntry) IsPrivateField() bool  { return e&entryIsPrivateField != 0 }
func (e VariableEnvironmentEntry) IsPrivateMethod() bool { return e&entryIsPrivateMethod != 0 }
func (e VariableEnvironmentEntry) IsPrivateSetter() bool { return e&entryIsPrivateSetter != 0 }
func (e VariableEnvironmentEntry) IsPrivateGetter() bool { return e&entryIsPrivateGetter != 0 }
func (e VariableEnvironmentEntry) IsUsing() bool         { return e&entryIsUsing != 0 }

func (e *VariableEnvironmentEntry) SetIsCaptured()          { *e |= entryIsCaptured }
func (e *VariableEnvironmentEntry) SetIsConst()             { *e |= entryIsConst }
func (e *VariableEnvironmentEntry) SetIsVar()               { *e |= entryIsVar }
func (e *VariableEnvironmentEntry) SetIsLet()               { *e |= entryIsLet }
func (e *VariableEnvironmentEntry) SetIsExported()          { *e |= entryIsExported }
func (e *VariableEnvironmentEntry) SetIsImported()          { *e |= entryIsImported }
func (e *VariableEnvironmentEntry) SetIsImportedNamespace() { *e |= entryIsImportedNamespace }
func (e *VariableEnvironmentEntry) SetIsFunction()          { *e |= entryIsFunction }
func (e *VariableEnvironmentEntry) SetIsFunctionDeclaration() {
	*e |= entryIsFunctionDeclaration
}
func (e *VariableEnvironmentEntry) SetIsParameter() { *e |= entryIsParameter }
func (e *VariableEnvironmentEntry) SetIsSloppyModeHoistedFunction() {
	*e |= entryIsSloppyModeHoistedFunction
}
func (e *VariableEnvironmentEntry) SetIsPrivateField()  { *e |= entryIsPrivateField }
func (e *VariableEnvironmentEntry) SetIsPrivateMethod() { *e |= entryIsPrivateMethod }
func (e *VariableEnvironmentEntry) SetIsPrivateSetter() { *e |= entryIsPrivateSetter }
func (e *VariableEnvironmentEntry) SetIsPrivateGetter() { *e |= entryIsPrivateGetter }
func (e *VariableEnvironmentEntry) SetIsUsing()         { *e |= entryIsUsing }
func (e *VariableEnvironmentEntry) ClearIsVar()         { *e &^= entryIsVar }

// PrivateNameEntry describes a #private class element.
type PrivateNameEntry uint16

const (
	PrivateNameNone     PrivateNameEntry = 0
	PrivateNameIsMethod PrivateNameEntry = 1 << 0
	PrivateNameIsGetter PrivateNameEntry = 1 << 1
	PrivateNameIsSetter PrivateNameEntry = 1 << 2
	PrivateNameIsStatic PrivateNameEntry = 1 << 3
)

func (e PrivateNameEntry) IsMethod() bool { return e&PrivateNameIsMethod != 0 }
func (e PrivateNameEntry) IsGetter() bool { return e&PrivateNameIsGetter != 0 }
func (e PrivateNameEntry) IsSetter() bool { return e&PrivateNameIsSetter != 0 }
func (e PrivateNameEntry) IsStatic() bool { return e&PrivateNameIsStatic != 0 }
func (e PrivateNameEntry) IsPrivateMethodOrAccessor() bool {
	return e.IsMethod() || e.IsSetter() || e.IsGetter()
}
func (e PrivateNameEntry) IsField() bool { return !e.IsPrivateMethodOrAccessor() }

// PrivateNameEnvironment maps private names to their traits.
type PrivateNameEnvironment = orderedMap[PrivateNameEntry]

// PrivateDeclarationResult is the outcome of declaring a private accessor.
type PrivateDeclarationResult uint8

const (
	PrivateDeclarationSuccess PrivateDeclarationResult = iota
	PrivateDeclarationDuplicatedName
	PrivateDeclarationInvalidStaticNonStatic
)

// VariableEnvironment is the set of bindings declared in one scope.
type VariableEnvironment struct {
	m                        orderedMap[VariableEnvironmentEntry]
	isEverythingCaptured     bool
	hasAwaitUsingDeclaration bool
	privateNames             *PrivateNameEnvironment // JSC's RareData
}

// Clone returns a deep copy.
func (v *VariableEnvironment) Clone() VariableEnvironment {
	c := VariableEnvironment{m: v.m.clone(), isEverythingCaptured: v.isEverythingCaptured, hasAwaitUsingDeclaration: v.hasAwaitUsingDeclaration}
	if v.privateNames != nil {
		p := v.privateNames.clone()
		c.privateNames = &p
	}
	return c
}

// Add returns the entry for id, creating an empty one if needed.
func (v *VariableEnvironment) Add(id ident.Identifier) (entry *VariableEnvironmentEntry, isNewEntry bool) {
	return v.m.add(id)
}

func (v *VariableEnvironment) Find(id ident.Identifier) (*VariableEnvironmentEntry, bool) {
	return v.m.find(id)
}

func (v *VariableEnvironment) Contains(id ident.Identifier) bool { return v.m.contains(id) }
func (v *VariableEnvironment) Remove(id ident.Identifier) bool   { return v.m.remove(id) }

// Size counts bindings and private names; MapSize only bindings.
func (v *VariableEnvironment) Size() int    { return v.m.len() + v.PrivateNamesSize() }
func (v *VariableEnvironment) MapSize() int { return v.m.len() }
func (v *VariableEnvironment) IsEmpty() bool {
	return v.m.len() == 0 && v.PrivateNamesSize() == 0
}

// All iterates the bindings in insertion order.
func (v *VariableEnvironment) All(yield func(ident.Identifier, *VariableEnvironmentEntry) bool) {
	for i, k := range v.m.keys {
		if !yield(k, &v.m.values[i]) {
			return
		}
	}
}

func (v *VariableEnvironment) MarkVariableAsCapturedIfDefined(id ident.Identifier) {
	if e, ok := v.m.find(id); ok {
		e.SetIsCaptured()
	}
}

func (v *VariableEnvironment) MarkVariableAsCaptured(id ident.Identifier) {
	e, ok := v.m.find(id)
	if !ok {
		panic("VariableEnvironment: capturing undeclared variable " + id.String())
	}
	e.SetIsCaptured()
}

func (v *VariableEnvironment) MarkAllVariablesAsCaptured() {
	if v.isEverythingCaptured {
		return
	}
	v.isEverythingCaptured = true
	for i := range v.m.values {
		v.m.values[i].SetIsCaptured()
	}
}

func (v *VariableEnvironment) HasCapturedVariables() bool {
	if v.isEverythingCaptured {
		return v.Size() > 0
	}
	for _, e := range v.m.values {
		if e.IsCaptured() {
			return true
		}
	}
	return false
}

func (v *VariableEnvironment) Captures(id ident.Identifier) bool {
	if v.isEverythingCaptured {
		return true
	}
	e, ok := v.m.find(id)
	return ok && e.IsCaptured()
}

func (v *VariableEnvironment) MarkVariableAsImported(id ident.Identifier) {
	if e, ok := v.m.find(id); ok {
		e.SetIsImported()
	}
}

func (v *VariableEnvironment) MarkVariableAsExported(id ident.Identifier) {
	if e, ok := v.m.find(id); ok {
		e.SetIsExported()
	}
}

func (v *VariableEnvironment) IsEverythingCaptured() bool { return v.isEverythingCaptured }

func (v *VariableEnvironment) HasUsingDeclaration() bool {
	for _, e := range v.m.values {
		if e.IsUsing() {
			return true
		}
	}
	return false
}

func (v *VariableEnvironment) UsingDeclarationCount() int {
	n := 0
	for _, e := range v.m.values {
		if e.IsUsing() {
			n++
		}
	}
	return n
}

func (v *VariableEnvironment) HasAwaitUsingDeclaration() bool { return v.hasAwaitUsingDeclaration }
func (v *VariableEnvironment) SetHasAwaitUsingDeclaration()   { v.hasAwaitUsingDeclaration = true }

// --- Private names ----------------------------------------------------------

func (v *VariableEnvironment) ensurePrivateNames() *PrivateNameEnvironment {
	if v.privateNames == nil {
		v.privateNames = &PrivateNameEnvironment{}
	}
	return v.privateNames
}

func (v *VariableEnvironment) PrivateNamesSize() int {
	if v.privateNames == nil {
		return 0
	}
	return v.privateNames.len()
}

// PrivateNameEnvironment returns the private names, or nil if there are none.
func (v *VariableEnvironment) PrivateNameEnvironment() *PrivateNameEnvironment { return v.privateNames }

// PrivateNames iterates the private names in declaration order.
func (v *VariableEnvironment) PrivateNames(yield func(ident.Identifier, PrivateNameEntry) bool) {
	if v.privateNames == nil {
		return
	}
	for i, k := range v.privateNames.keys {
		if !yield(k, v.privateNames.values[i]) {
			return
		}
	}
}

func (v *VariableEnvironment) HasPrivateName(id ident.Identifier) bool {
	return v.privateNames != nil && v.privateNames.contains(id)
}

func (v *VariableEnvironment) AddPrivateName(id ident.Identifier) {
	v.ensurePrivateNames().add(id)
}

// AddPrivateNamesFrom copies private names visible from an enclosing class
// into this environment (used for direct eval inside a class).
func (v *VariableEnvironment) AddPrivateNamesFrom(env *PrivateNameEnvironment) {
	if env == nil {
		return
	}
	dst := v.ensurePrivateNames()
	for i, k := range env.keys {
		if e, isNew := dst.add(k); isNew {
			*e = env.values[i]
		}
	}
}

func (v *VariableEnvironment) HasStaticPrivateMethodOrAccessor() bool {
	for _, e := range v.PrivateNames {
		if e.IsPrivateMethodOrAccessor() && e.IsStatic() {
			return true
		}
	}
	return false
}

func (v *VariableEnvironment) HasInstancePrivateMethodOrAccessor() bool {
	for _, e := range v.PrivateNames {
		if e.IsPrivateMethodOrAccessor() && !e.IsStatic() {
			return true
		}
	}
	return false
}

// DeclarePrivateField declares #id as a field.
func (v *VariableEnvironment) DeclarePrivateField(id ident.Identifier) (isNewEntry bool) {
	v.ensurePrivateNames().add(id)
	e, isNew := v.m.add(id)
	if isNew {
		*e = entryIsPrivateField | entryIsConst | entryIsCaptured
	}
	return isNew
}

func (v *VariableEnvironment) declarePrivateMethod(id ident.Identifier, traits PrivateNameEntry) bool {
	names := v.ensurePrivateNames()
	if names.contains(id) {
		return false // declaring a duplicate private name
	}
	e, _ := v.m.add(id)
	*e = entryIsPrivateMethod | entryIsConst | entryIsCaptured
	meta, _ := names.add(id)
	*meta = PrivateNameIsMethod | traits
	return true
}

func (v *VariableEnvironment) DeclarePrivateMethod(id ident.Identifier) bool {
	return v.declarePrivateMethod(id, PrivateNameNone)
}

func (v *VariableEnvironment) DeclareStaticPrivateMethod(id ident.Identifier) bool {
	return v.declarePrivateMethod(id, PrivateNameIsStatic)
}

func (v *VariableEnvironment) declarePrivateAccessor(id ident.Identifier, accessor PrivateNameEntry) PrivateDeclarationResult {
	names := v.ensurePrivateNames()
	current, ok := names.find(id)
	if !ok {
		var traits VariableEnvironmentEntry
		if accessor.IsSetter() {
			traits = entryIsPrivateSetter
		} else {
			traits = entryIsPrivateGetter
		}
		e, _ := v.m.add(id)
		*e = traits | entryIsConst | entryIsCaptured
		meta, _ := names.add(id)
		*meta = accessor
		return PrivateDeclarationSuccess
	}
	// A getter may only pair with an existing setter and vice versa.
	if (accessor.IsSetter() && !current.IsGetter()) || (accessor.IsGetter() && !current.IsSetter()) {
		return PrivateDeclarationDuplicatedName
	}
	if accessor.IsStatic() != current.IsStatic() {
		return PrivateDeclarationInvalidStaticNonStatic
	}
	*current |= accessor
	e, _ := v.m.find(id)
	if accessor.IsSetter() {
		e.SetIsPrivateSetter()
	} else {
		e.SetIsPrivateGetter()
	}
	return PrivateDeclarationSuccess
}

func (v *VariableEnvironment) DeclarePrivateSetter(id ident.Identifier) PrivateDeclarationResult {
	return v.declarePrivateAccessor(id, PrivateNameIsSetter)
}

func (v *VariableEnvironment) DeclareStaticPrivateSetter(id ident.Identifier) PrivateDeclarationResult {
	return v.declarePrivateAccessor(id, PrivateNameIsSetter|PrivateNameIsStatic)
}

func (v *VariableEnvironment) DeclarePrivateGetter(id ident.Identifier) PrivateDeclarationResult {
	return v.declarePrivateAccessor(id, PrivateNameIsGetter)
}

func (v *VariableEnvironment) DeclareStaticPrivateGetter(id ident.Identifier) PrivateDeclarationResult {
	return v.declarePrivateAccessor(id, PrivateNameIsGetter|PrivateNameIsStatic)
}

// identSet is JSC's UniquedStringImplPtrSet / IdentifierSet. It keeps
// insertion order for deterministic iteration.
type identSet struct {
	keys  []ident.Identifier
	index map[ident.Identifier]struct{} // only past smallSet keys
}

func (s *identSet) add(id ident.Identifier) bool {
	if s.contains(id) {
		return false
	}
	s.keys = append(s.keys, id)
	if s.index != nil {
		s.index[id] = struct{}{}
	} else if len(s.keys) > smallSet {
		s.index = make(map[ident.Identifier]struct{}, 2*len(s.keys))
		for _, k := range s.keys {
			s.index[k] = struct{}{}
		}
	}
	return true
}

func (s *identSet) contains(id ident.Identifier) bool {
	if s.index != nil {
		_, ok := s.index[id]
		return ok
	}
	return slices.Contains(s.keys, id)
}

func (s *identSet) remove(id ident.Identifier) {
	if !s.contains(id) {
		return
	}
	if s.index != nil {
		delete(s.index, id)
	}
	s.keys = slices.DeleteFunc(s.keys, func(k ident.Identifier) bool { return k == id })
}

func (s *identSet) len() int { return len(s.keys) }

func (s *identSet) addAll(o *identSet) {
	for _, k := range o.keys {
		s.add(k)
	}
}

// Exported accessors so that other packages (the bytecode generator) can use
// PrivateNameEnvironment, which is an alias of orderedMap.

func (m *orderedMap[V]) Len() int                             { return m.len() }
func (m *orderedMap[V]) Find(key ident.Identifier) (*V, bool) { return m.find(key) }
func (m *orderedMap[V]) Contains(key ident.Identifier) bool   { return m.contains(key) }
func (m *orderedMap[V]) Add(key ident.Identifier) (*V, bool)  { return m.add(key) }
func (m *orderedMap[V]) Clone() orderedMap[V]                 { return m.clone() }

// All iterates the entries in insertion order.
func (m *orderedMap[V]) All(yield func(ident.Identifier, V) bool) {
	for i, k := range m.keys {
		if !yield(k, m.values[i]) {
			return
		}
	}
}
