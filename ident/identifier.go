// Package ident implements JSC's Identifier: an interned, immutable JavaScript
// string used for property names, variable names and string literals.
//
// JSC interns identifiers in a per-VM AtomStringTable so they compare by
// pointer; here an Identifier is a pointer to an atom of the process-wide
// atom table (atoms.go): two Identifiers with the same contents are ==, and
// the comparison is a single word compare.
//
// JavaScript strings are sequences of UTF-16 code units and may contain lone
// surrogates. The interned value is therefore stored as WTF-8 (UTF-8 that also
// allows encoded surrogate code points), which round-trips any []uint16.
package ident

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Identifier is an interned JavaScript string. The zero value is the null
// identifier (JSC's nullptr / null String), distinct from the empty string.
type Identifier struct {
	a *atom
}

// Null is the null identifier.
var Null Identifier

// Empty is the interned empty string.
var Empty = FromString("")

// FromString interns a Go string. s must be valid WTF-8; any valid UTF-8
// string qualifies.
func FromString(s string) Identifier {
	return Identifier{intern(s)}
}

// FromUTF16 interns a sequence of UTF-16 code units.
func FromUTF16(units []uint16) Identifier {
	return FromString(EncodeWTF8(units))
}

// IsNull reports whether id is the null identifier.
func (id Identifier) IsNull() bool { return id == Null }

// String returns the WTF-8 contents. The null identifier yields "".
func (id Identifier) String() string {
	if id.IsNull() {
		return ""
	}
	return id.a.s
}

// UTF16 returns the contents as UTF-16 code units.
func (id Identifier) UTF16() []uint16 { return DecodeWTF8(id.Description()) }

// Len returns the length in UTF-16 code units, as JavaScript's .length would.
func (id Identifier) Len() int {
	s := id.Description()
	n := 0
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c < 0x80:
			i++
			n++
		case c < 0xE0:
			i += 2
			n++
		case c < 0xF0:
			i += 3
			n++
		default:
			i += 4
			n += 2
		}
	}
	return n
}

// EncodeWTF8 converts UTF-16 code units to WTF-8. Paired surrogates become a
// single 4-byte sequence; lone surrogates are encoded as 3-byte sequences.
func EncodeWTF8(units []uint16) string {
	ascii := true
	for _, u := range units {
		if u >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		b := make([]byte, len(units))
		for i, u := range units {
			b[i] = byte(u)
		}
		return string(b)
	}

	var sb strings.Builder
	sb.Grow(len(units) * 2)
	for i := 0; i < len(units); i++ {
		u := rune(units[i])
		if utf16.IsSurrogate(u) && u < 0xDC00 && i+1 < len(units) {
			if r := utf16.DecodeRune(u, rune(units[i+1])); r != utf8.RuneError {
				sb.WriteRune(r)
				i++
				continue
			}
		}
		if utf16.IsSurrogate(u) {
			sb.WriteByte(byte(0xE0 | u>>12))
			sb.WriteByte(byte(0x80 | (u>>6)&0x3F))
			sb.WriteByte(byte(0x80 | u&0x3F))
			continue
		}
		sb.WriteRune(u)
	}
	return sb.String()
}

// DecodeWTF8 converts WTF-8 back to UTF-16 code units.
func DecodeWTF8(s string) []uint16 {
	out := make([]uint16, 0, len(s))
	for i := 0; i < len(s); {
		c := s[i]
		if c < 0x80 {
			out = append(out, uint16(c))
			i++
			continue
		}
		// Encoded surrogates are rejected by utf8.DecodeRuneInString, so
		// handle the 3-byte form explicitly.
		if c == 0xED && i+2 < len(s) && s[i+1] >= 0xA0 {
			out = append(out, uint16(c&0x0F)<<12|uint16(s[i+1]&0x3F)<<6|uint16(s[i+2]&0x3F))
			i += 3
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		if r >= 0x10000 {
			hi, lo := utf16.EncodeRune(r)
			out = append(out, uint16(hi), uint16(lo))
		} else {
			out = append(out, uint16(r))
		}
	}
	return out
}
