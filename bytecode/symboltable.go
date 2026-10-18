package bytecode

import (
	"fmt"
	"math"

	"github.com/baslentfert/jsc2go/ident"
	"github.com/baslentfert/jsc2go/parser"
)

// Port of JavaScriptCore/runtime/{SymbolTable,VarOffset,ScopeOffset,
// DirectArgumentsOffset}.h. Without a JIT there are no watchpoints, so a
// SymbolTableEntry is a plain struct instead of JSC's packed fat/slim word.

// ScopeOffset is a slot index in a JSLexicalEnvironment.
type ScopeOffset uint32

// InvalidScopeOffset is the default-constructed ScopeOffset.
const InvalidScopeOffset = ScopeOffset(math.MaxUint32)

func (o ScopeOffset) IsValid() bool { return o != InvalidScopeOffset }

func (o ScopeOffset) String() string {
	if !o.IsValid() {
		return "scopeInvalid"
	}
	return fmt.Sprintf("scope%d", uint32(o))
}

// DirectArgumentsOffset is an index into a DirectArguments object.
type DirectArgumentsOffset uint32

const InvalidDirectArgumentsOffset = DirectArgumentsOffset(math.MaxUint32)

func (o DirectArgumentsOffset) IsValid() bool { return o != InvalidDirectArgumentsOffset }

// VarKind says where a variable lives.
type VarKind uint8

const (
	VarKindInvalid VarKind = iota
	VarKindScope
	VarKindStack
	VarKindDirectArgument
)

// VarOffset is a variable's location: a stack register, a scope slot or a
// DirectArguments slot.
type VarOffset struct {
	kind   VarKind
	offset uint32
}

// InvalidVarOffset is the default-constructed VarOffset.
var InvalidVarOffset = VarOffset{kind: VarKindInvalid, offset: math.MaxUint32}

func VarOffsetForStack(r VirtualRegister) VarOffset {
	if !r.IsValid() {
		return InvalidVarOffset
	}
	return VarOffset{kind: VarKindStack, offset: uint32(r)}
}

func VarOffsetForScope(o ScopeOffset) VarOffset {
	if !o.IsValid() {
		return InvalidVarOffset
	}
	return VarOffset{kind: VarKindScope, offset: uint32(o)}
}

func VarOffsetForDirectArgument(o DirectArgumentsOffset) VarOffset {
	if !o.IsValid() {
		return InvalidVarOffset
	}
	return VarOffset{kind: VarKindDirectArgument, offset: uint32(o)}
}

func (v VarOffset) IsValid() bool          { return v.kind != VarKindInvalid }
func (v VarOffset) Kind() VarKind          { return v.kind }
func (v VarOffset) IsStack() bool          { return v.kind == VarKindStack }
func (v VarOffset) IsScope() bool          { return v.kind == VarKindScope }
func (v VarOffset) IsDirectArgument() bool { return v.kind == VarKindDirectArgument }
func (v VarOffset) RawOffset() uint32      { return v.offset }

func (v VarOffset) StackOffset() VirtualRegister {
	if !v.IsStack() {
		panic("VarOffset: not a stack offset")
	}
	return VirtualRegister(int32(v.offset))
}

func (v VarOffset) ScopeOffset() ScopeOffset {
	if !v.IsScope() {
		panic("VarOffset: not a scope offset")
	}
	return ScopeOffset(v.offset)
}

func (v VarOffset) CapturedArgumentsOffset() DirectArgumentsOffset {
	if !v.IsDirectArgument() {
		panic("VarOffset: not a direct argument offset")
	}
	return DirectArgumentsOffset(v.offset)
}

// StackOffsetUnchecked returns InvalidVirtualRegister for non-stack offsets.
func (v VarOffset) StackOffsetUnchecked() VirtualRegister {
	if !v.IsStack() {
		return InvalidVirtualRegister
	}
	return VirtualRegister(int32(v.offset))
}

// ScopeOffsetUnchecked returns InvalidScopeOffset for non-scope offsets.
func (v VarOffset) ScopeOffsetUnchecked() ScopeOffset {
	if !v.IsScope() {
		return InvalidScopeOffset
	}
	return ScopeOffset(v.offset)
}

func (v VarOffset) String() string {
	switch v.kind {
	case VarKindScope:
		return ScopeOffset(v.offset).String()
	case VarKindStack:
		return "stack" + VirtualRegister(int32(v.offset)).String()
	case VarKindDirectArgument:
		return fmt.Sprintf("capturedArgument%d", v.offset)
	}
	return "invalid"
}

// SymbolTableEntry describes one variable of a SymbolTable.
type SymbolTableEntry struct {
	Offset   VarOffset
	ReadOnly bool
	DontEnum bool
}

func (e SymbolTableEntry) IsNull() bool { return !e.Offset.IsValid() }

// ConstantMode mirrors JSC's ConstantMode.
type ConstantMode uint8

const (
	IsConstant ConstantMode = iota
	IsVariable
)

func (e SymbolTableEntry) ConstantMode() ConstantMode {
	if e.ReadOnly {
		return IsConstant
	}
	return IsVariable
}

// SymbolTableScopeType is SymbolTable::ScopeType.
type SymbolTableScopeType uint8

const (
	VarScope SymbolTableScopeType = iota
	GlobalLexicalScope
	LexicalScope
	CatchScope
	CatchScopeWithSimpleParameter
	FunctionNameScope
)

// SymbolTable maps the names of a scope to their locations. It is shared
// between the bytecode generator (which fills it) and the runtime (which
// creates JSLexicalEnvironments from it). Iteration is in insertion order.
type SymbolTable struct {
	names          []ident.Identifier
	entries        map[ident.Identifier]*SymbolTableEntry
	maxScopeOffset ScopeOffset

	UsesSloppyEval     bool
	NestedLexicalScope bool
	ScopeType          SymbolTableScopeType

	privateNames parser.PrivateNameEnvironment

	// arguments is JSC's ScopedArgumentsTable: the scope slot of each named
	// parameter, used to create ScopedArguments objects.
	arguments    []ScopeOffset
	hasArguments bool
}

func NewSymbolTable() *SymbolTable {
	return &SymbolTable{entries: make(map[ident.Identifier]*SymbolTableEntry), maxScopeOffset: InvalidScopeOffset}
}

// Get returns the entry for name; ok is false if there is none.
func (t *SymbolTable) Get(name ident.Identifier) (SymbolTableEntry, bool) {
	if e, ok := t.entries[name]; ok {
		return *e, true
	}
	return SymbolTableEntry{Offset: InvalidVarOffset}, false
}

// Find returns a pointer to the entry for name so that it can be modified.
func (t *SymbolTable) Find(name ident.Identifier) *SymbolTableEntry { return t.entries[name] }

func (t *SymbolTable) Contains(name ident.Identifier) bool {
	_, ok := t.entries[name]
	return ok
}

func (t *SymbolTable) Size() int { return len(t.names) }

// All iterates the entries in insertion order.
func (t *SymbolTable) All(yield func(ident.Identifier, *SymbolTableEntry) bool) {
	for _, n := range t.names {
		if !yield(n, t.entries[n]) {
			return
		}
	}
}

// Add adds a new entry; name must not be present yet.
func (t *SymbolTable) Add(name ident.Identifier, entry SymbolTableEntry) {
	if _, ok := t.entries[name]; ok {
		panic("SymbolTable.Add: duplicate entry " + name.String())
	}
	t.Set(name, entry)
}

// Set adds or replaces the entry for name.
func (t *SymbolTable) Set(name ident.Identifier, entry SymbolTableEntry) {
	t.didUseVarOffset(entry.Offset)
	if e, ok := t.entries[name]; ok {
		*e = entry
		return
	}
	t.names = append(t.names, name)
	t.entries[name] = &entry
}

func (t *SymbolTable) MaxScopeOffset() ScopeOffset { return t.maxScopeOffset }

func (t *SymbolTable) didUseScopeOffset(o ScopeOffset) {
	if !t.maxScopeOffset.IsValid() || t.maxScopeOffset < o {
		t.maxScopeOffset = o
	}
}

func (t *SymbolTable) didUseVarOffset(o VarOffset) {
	if o.IsScope() {
		t.didUseScopeOffset(o.ScopeOffset())
	}
}

// ScopeSize is the number of slots a JSLexicalEnvironment for this table needs.
func (t *SymbolTable) ScopeSize() uint32 { return uint32(t.maxScopeOffset) + 1 } // wraps to 0 when invalid

func (t *SymbolTable) NextScopeOffset() ScopeOffset { return ScopeOffset(t.ScopeSize()) }

func (t *SymbolTable) TakeNextScopeOffset() ScopeOffset {
	r := t.NextScopeOffset()
	t.maxScopeOffset = r
	return r
}

// --- Private names ----------------------------------------------------------

func (t *SymbolTable) HasPrivateNames() bool { return t.privateNames.Len() > 0 }

func (t *SymbolTable) PrivateNames() *parser.PrivateNameEnvironment { return &t.privateNames }

func (t *SymbolTable) AddPrivateName(name ident.Identifier, entry parser.PrivateNameEntry) {
	e, isNew := t.privateNames.Add(name)
	if !isNew {
		panic("SymbolTable.AddPrivateName: duplicate private name " + name.String())
	}
	*e = entry
}

func (t *SymbolTable) HasPrivateName(name ident.Identifier) bool {
	return t.privateNames.Contains(name)
}

// --- Scoped arguments -------------------------------------------------------

func (t *SymbolTable) ArgumentsLength() uint32 { return uint32(len(t.arguments)) }

// HasArguments reports whether the table has a ScopedArgumentsTable.
func (t *SymbolTable) HasArguments() bool { return t.hasArguments }

func (t *SymbolTable) SetArgumentsLength(length uint32) {
	t.hasArguments = true
	if int(length) <= len(t.arguments) {
		t.arguments = t.arguments[:length]
		return
	}
	for len(t.arguments) < int(length) {
		t.arguments = append(t.arguments, InvalidScopeOffset)
	}
}

func (t *SymbolTable) ArgumentOffset(i uint32) ScopeOffset { return t.arguments[i] }

func (t *SymbolTable) SetArgumentOffset(i uint32, o ScopeOffset) { t.arguments[i] = o }

// Arguments returns the scope slots of the named parameters.
func (t *SymbolTable) Arguments() []ScopeOffset { return t.arguments }

// CloneScopePart copies the scope-allocated part of the table (JSC uses this
// to give each activation of a scope its own table when needed).
func (t *SymbolTable) CloneScopePart() *SymbolTable {
	c := NewSymbolTable()
	c.UsesSloppyEval = t.UsesSloppyEval
	c.NestedLexicalScope = t.NestedLexicalScope
	c.ScopeType = t.ScopeType
	for _, n := range t.names {
		e := t.entries[n]
		if e.Offset.IsScope() {
			c.Add(n, *e)
		}
	}
	c.maxScopeOffset = t.maxScopeOffset
	c.privateNames = t.privateNames.Clone()
	if t.hasArguments {
		c.hasArguments = true
		c.arguments = append([]ScopeOffset(nil), t.arguments...)
	}
	return c
}

func (t *SymbolTable) String() string {
	s := "SymbolTable{"
	for i, n := range t.names {
		if i > 0 {
			s += ", "
		}
		s += n.String() + ": " + t.entries[n].Offset.String()
	}
	return s + "}"
}
