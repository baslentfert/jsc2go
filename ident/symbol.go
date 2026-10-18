package ident

import (
	"strconv"

	"github.com/baslentfert/jsc2go/wtf/dtoa"
)

// JSC distinguishes string identifiers from symbols (SymbolImpl), which are
// used for engine-internal names like @generator or *default*: a symbol never
// equals a string with the same text. We keep that distinction by prefixing a
// symbol's description with a 0xFF byte, which can never occur in WTF-8.
const symbolMarker = "\xff"

// PrivateSymbol returns the private symbol with the given description. Like
// JSC's privateSymbolRegistry, the same description yields the same symbol.
func PrivateSymbol(description string) Identifier {
	return FromString(symbolMarker + description)
}

// wellKnownSymbolMarker prefixes the well-known symbols such as
// Symbol.iterator, which unlike private symbols are visible to user code.
const wellKnownSymbolMarker = "\xfe"

// WellKnownSymbol returns the identifier of a well-known symbol, e.g.
// WellKnownSymbol("Symbol.iterator"). The runtime maps it to the symbol.
func WellKnownSymbol(description string) Identifier {
	return FromString(wellKnownSymbolMarker + description)
}

// IsSymbol reports whether id is a symbol rather than a string.
func (id Identifier) IsSymbol() bool {
	s := id.String()
	return len(s) > 0 && (s[0] == symbolMarker[0] || s[0] == wellKnownSymbolMarker[0])
}

// IsPrivateSymbol reports whether id is an engine-internal private symbol.
func (id Identifier) IsPrivateSymbol() bool {
	s := id.String()
	return len(s) > 0 && s[0] == symbolMarker[0]
}

// Description returns the text of id; for a symbol, its description.
func (id Identifier) Description() string {
	if id.IsSymbol() {
		return id.String()[1:]
	}
	return id.String()
}

// IsPrivateName reports whether id is a #private name (its text starts with '#').
func (id Identifier) IsPrivateName() bool {
	s := id.String()
	return len(s) > 0 && s[0] == '#'
}

// FromNumber returns the identifier for a numeric property name, formatted
// as JavaScript's Number::toString does (IdentifierArena::makeNumericIdentifier).
func FromNumber(n float64) Identifier {
	if i := int32(n); float64(i) == n && (i != 0 || 1/n > 0) {
		return FromString(strconv.Itoa(int(i)))
	}
	return FromString(dtoa.NumberToString(n))
}

// ParseIndex returns the array index id names, if any: a canonical decimal
// string for a number below 2^32-1 (WTF's parseIndex).
func (id Identifier) ParseIndex() (uint32, bool) {
	s := id.String()
	if len(s) == 0 || len(s) > 10 || (len(s) > 1 && s[0] == '0') {
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
