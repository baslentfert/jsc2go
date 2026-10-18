package vm

import (
	"strconv"

	"github.com/baslentfert/jsc2go/ident"
)

// Symbol is a JavaScript symbol (JSC's Symbol/SymbolImpl). Private symbols
// are engine-internal property names (JSC's PrivateSymbolImpl), including
// the symbols that back #private class members; they are invisible to
// reflection and are never intercepted by proxies.
type Symbol struct {
	description *String // nil: undefined
	private     bool
	// registered is the Symbol.for key of a registered symbol.
	registered *String
	// ephemerons are this symbol's entries in WeakMaps and WeakSets, but
	// for a shared symbol, whose entries each VM keeps (sharedEphemerons).
	ephemerons ephemeronTable
	// shared marks the well-known symbols, which all VMs use.
	shared bool
}

// NewSymbol makes a fresh symbol; description may be nil.
func NewSymbol(description *String) *Symbol { return &Symbol{description: description} }

func newPrivateSymbol(description string) *Symbol {
	return &Symbol{description: NewString(description), private: true}
}

func (s *Symbol) Description() *String { return s.description }
func (s *Symbol) IsPrivate() bool      { return s.private }

// DescriptiveString is SymbolDescriptiveString: "Symbol(desc)".
func (s *Symbol) DescriptiveString() string {
	if s.description == nil {
		return "Symbol()"
	}
	return "Symbol(" + s.description.String() + ")"
}

// PropertyKey is a property name: an array index, a string (as an interned
// identifier) or a symbol. It is comparable and used as a map key. Array
// indices are kept as numbers (idx = index+1) so that element access never
// interns strings; the constructors canonicalize, so a string key never
// spells an array index.
type PropertyKey struct {
	name ident.Identifier
	sym  *Symbol
	idx  uint32
}

// StrKey returns the key for the string s (WTF-8).
func StrKey(s string) PropertyKey {
	if i, ok := parseArrayIndex(s); ok {
		return PropertyKey{idx: i + 1}
	}
	return PropertyKey{name: ident.FromString(s)}
}

// IdentKey returns the key for a non-symbol identifier.
func IdentKey(id ident.Identifier) PropertyKey {
	if i, ok := id.ParseIndex(); ok {
		return PropertyKey{idx: i + 1}
	}
	return PropertyKey{name: id}
}

// parseArrayIndex parses a canonical array index (0 .. 2^32-2).
func parseArrayIndex(s string) (uint32, bool) {
	if len(s) == 0 || len(s) > 10 || len(s) > 1 && s[0] == '0' {
		return 0, false
	}
	var v uint64
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		v = v*10 + uint64(c-'0')
	}
	if v > 0xFFFFFFFE {
		return 0, false
	}
	return uint32(v), true
}

// SymKey returns the key for a symbol.
func SymKey(s *Symbol) PropertyKey { return PropertyKey{sym: s} }

// IndexKey returns the key for an array index (i < 2^32-1; 2^32-1 itself
// is not an index and becomes a string key).
func IndexKey(i uint32) PropertyKey {
	if i == 0xFFFFFFFF {
		return PropertyKey{name: ident.FromString("4294967295")}
	}
	return PropertyKey{idx: i + 1}
}

func (k PropertyKey) IsSymbol() bool  { return k.sym != nil }
func (k PropertyKey) Symbol() *Symbol { return k.sym }
func (k PropertyKey) IsPrivate() bool { return k.sym != nil && k.sym.private }
func (k PropertyKey) Ident() ident.Identifier {
	if k.idx != 0 {
		return ident.FromString(strconv.FormatUint(uint64(k.idx-1), 10))
	}
	return k.name
}

// Index returns the array index the key names, if any.
func (k PropertyKey) Index() (uint32, bool) {
	return k.idx - 1, k.idx != 0
}

// Value returns the key as a JavaScript value (string or symbol).
func (k PropertyKey) Value() Value {
	if k.sym != nil {
		return SymbolValue(k.sym)
	}
	if k.idx != 0 {
		return StringValue(NewString(strconv.FormatUint(uint64(k.idx-1), 10)))
	}
	return StringValue(NewString(k.name.String()))
}

// String formats the key for error messages.
func (k PropertyKey) String() string {
	if k.sym != nil {
		if k.sym.description == nil {
			return "Symbol()"
		}
		return k.sym.description.String()
	}
	if k.idx != 0 {
		return strconv.FormatUint(uint64(k.idx-1), 10)
	}
	return k.name.String()
}

// Well-known symbols, shared by all realms.
var (
	SymbolAsyncIterator      = wellKnown("Symbol.asyncIterator")
	SymbolHasInstance        = wellKnown("Symbol.hasInstance")
	SymbolIsConcatSpreadable = wellKnown("Symbol.isConcatSpreadable")
	SymbolIterator           = wellKnown("Symbol.iterator")
	SymbolMatch              = wellKnown("Symbol.match")
	SymbolMatchAll           = wellKnown("Symbol.matchAll")
	SymbolReplace            = wellKnown("Symbol.replace")
	SymbolSearch             = wellKnown("Symbol.search")
	SymbolSpecies            = wellKnown("Symbol.species")
	SymbolSplit              = wellKnown("Symbol.split")
	SymbolToPrimitive        = wellKnown("Symbol.toPrimitive")
	SymbolToStringTag        = wellKnown("Symbol.toStringTag")
	SymbolUnscopables        = wellKnown("Symbol.unscopables")
	SymbolDispose            = wellKnown("Symbol.dispose")
	SymbolAsyncDispose       = wellKnown("Symbol.asyncDispose")
)

var wellKnownSymbols = map[ident.Identifier]*Symbol{}

func wellKnown(description string) *Symbol {
	s := &Symbol{description: NewString(description), shared: true}
	wellKnownSymbols[ident.WellKnownSymbol(description)] = s
	return s
}

// Engine-internal private symbols for identifiers the bytecode uses as
// property names (@homeObject, @instanceFieldInitializer, ...). They are
// process-wide like JSC's privateSymbolRegistry.
var privateSymbols = map[ident.Identifier]*Symbol{}

// symbolForIdentifier maps a symbol identifier from bytecode to its symbol.
func symbolForIdentifier(id ident.Identifier) *Symbol {
	if s, ok := wellKnownSymbols[id]; ok {
		return s
	}
	privateSymbolsMu.Lock()
	defer privateSymbolsMu.Unlock()
	if s, ok := privateSymbols[id]; ok {
		return s
	}
	s := newPrivateSymbol(id.Description())
	privateSymbols[id] = s
	return s
}

// keyForIdentifier converts a bytecode identifier to a property key.
func keyForIdentifier(id ident.Identifier) PropertyKey {
	if id.IsSymbol() {
		return PropertyKey{sym: symbolForIdentifier(id)}
	}
	return IdentKey(id)
}
