package ident

import (
	"slices"
	"testing"
)

func TestWTF8RoundTrip(t *testing.T) {
	cases := [][]uint16{
		{},
		{'a', 'b'},
		{0xE9},           // é
		{0xD83D, 0xDE00}, // 😀 as a surrogate pair
		{0xD800},         // lone lead surrogate
		{0xDC00, 'x'},    // lone trail surrogate
		{0xDC00, 0xD800}, // reversed pair stays two lone surrogates
		{'a', 0xD83D, 0xDE00, 0xD83D},
	}
	for _, units := range cases {
		if got := DecodeWTF8(EncodeWTF8(units)); !slices.Equal(got, units) {
			t.Errorf("%x: round trip gave %x", units, got)
		}
		if got := FromUTF16(units).Len(); got != len(units) {
			t.Errorf("%x: Len %d", units, got)
		}
	}
}

func TestInterning(t *testing.T) {
	a := FromUTF16([]uint16{'f', 'o', 'o'})
	b := FromString("foo")
	if a != b {
		t.Error("identical identifiers must compare equal")
	}
	if Null == Empty || !Null.IsNull() || Empty.IsNull() {
		t.Error("null and empty identifiers must differ")
	}
}
