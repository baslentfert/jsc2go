// Package vm is the JavaScript runtime and bytecode interpreter: values,
// objects, scopes, the built-in library and the interpreter loop that runs
// the code produced by package bytecompiler. It corresponds to JSC's
// runtime/ and interpreter/ directories (minus the JIT tiers).
package vm

import (
	"fmt"
	"math"
	"math/big"
	"unsafe"
)

// Kind is the type tag of a Value.
type Kind uint8

const (
	// KindUndefined is first so that the zero Value is undefined.
	KindUndefined Kind = iota
	// KindEmpty is JSValue(): the marker for holes and variables in their
	// temporal dead zone. It never escapes to JavaScript code.
	KindEmpty
	KindNull
	KindBool
	KindNumber
	KindString
	KindSymbol
	KindBigInt
	KindObject
	// KindCell holds engine-internal things kept in registers: scopes,
	// property name enumerators.
	KindCell
)

// Value is a JavaScript value in 16 bytes, a Go-friendly variant of JSC's
// NaN-boxing: the garbage collector must see pointers as pointers, so the
// pointer has a word of its own and the other word holds a number or tag.
//
// u values up to maxTag are tags: undefined (0, so the zero Value is
// undefined), null, booleans and empty, and the kinds whose payload is the
// pointer p. Every larger u is a number, stored as its bits XOR numberMask
// so that the tags correspond to NaN payloads no arithmetic produces;
// Number canonicalizes the NaNs that would collide (JSC's purifyNaN).
type Value struct {
	p unsafe.Pointer
	u uint64
}

const (
	tagUndefined uint64 = iota
	tagNull
	tagFalse
	tagTrue
	tagEmpty
	tagObject   // p is an *Object
	tagString   // p is a *String
	tagSymbol   // p is a *Symbol
	tagBigInt   // p is a *big.Int
	tagScope    // p is a *Scope
	tagAccessor // p is an *Accessor
	tagCell     // p is a *cellBox
	maxTag      = 15
)

const numberMask = 0xfffc000000000000

// cellBox holds the engine-internal things that rarely live in registers
// (enumerators, spread lists, symbol tables, exceptions, ...).
type cellBox struct{ c any }

var (
	Empty     = Value{u: tagEmpty}
	Undefined = Value{}
	Null      = Value{u: tagNull}
	True      = Value{u: tagTrue}
	False     = Value{u: tagFalse}
	NaN       = Number(math.NaN())
)

func Bool(b bool) Value {
	if b {
		return True
	}
	return False
}

func Number(f float64) Value {
	u := *(*uint64)(unsafe.Pointer(&f)) ^ numberMask // math.Float64bits, cheaper to inline
	if u <= maxTag {
		u = canonicalNaNWord
	}
	return Value{u: u}
}

// canonicalNaNWord is the u word of NaN.
const canonicalNaNWord = 0x7ff8000000000000 ^ numberMask

func Int(i int) Value { return Number(float64(i)) }

// int32Value is Number for int32 results, which need no NaN check.
func int32Value(i int32) Value    { return Value{u: math.Float64bits(float64(i)) ^ numberMask} }
func StringValue(s *String) Value { return Value{p: unsafe.Pointer(s), u: tagString} }
func SymbolValue(s *Symbol) Value { return Value{p: unsafe.Pointer(s), u: tagSymbol} }
func BigIntValue(b *big.Int) Value {
	return Value{p: unsafe.Pointer(b), u: tagBigInt}
}

// scopeValue is cellValue for a scope, cheap enough to inline.
func scopeValue(s *Scope) Value { return Value{p: unsafe.Pointer(s), u: tagScope} }

// cellValue wraps an engine-internal thing (scope, enumerator, ...).
func cellValue(c any) Value {
	switch c := c.(type) {
	case *Scope:
		return Value{p: unsafe.Pointer(c), u: tagScope}
	case *Accessor:
		return Value{p: unsafe.Pointer(c), u: tagAccessor}
	}
	return Value{p: unsafe.Pointer(&cellBox{c}), u: tagCell}
}

// ObjectValue wraps o; a nil object becomes null.
func ObjectValue(o *Object) Value {
	if o == nil {
		return Null
	}
	return Value{p: unsafe.Pointer(o), u: tagObject}
}

// Str makes a string value from Go (WTF-8) text.
func Str(s string) Value { return StringValue(NewString(s)) }

var tagKinds = [maxTag + 1]Kind{
	tagUndefined: KindUndefined, tagNull: KindNull, tagFalse: KindBool, tagTrue: KindBool,
	tagEmpty: KindEmpty, tagObject: KindObject, tagString: KindString, tagSymbol: KindSymbol,
	tagBigInt: KindBigInt, tagScope: KindCell, tagAccessor: KindCell, tagCell: KindCell,
}

// Kind returns the type tag of v.
func (v Value) Kind() Kind {
	if v.u > maxTag {
		return KindNumber
	}
	return tagKinds[v.u]
}

func (v Value) IsEmpty() bool       { return v.u == tagEmpty }
func (v Value) IsUndefined() bool   { return v.u == tagUndefined }
func (v Value) IsNull() bool        { return v.u == tagNull }
func (v Value) IsNullish() bool     { return v.u == tagUndefined || v.u == tagNull }
func (v Value) IsBool() bool        { return v.u == tagFalse || v.u == tagTrue }
func (v Value) IsNumber() bool      { return v.u > maxTag }
func (v Value) IsString() bool      { return v.u == tagString }
func (v Value) IsSymbol() bool      { return v.u == tagSymbol }
func (v Value) IsBigInt() bool      { return v.u == tagBigInt }
func (v Value) IsObject() bool      { return v.u == tagObject }
func (v Value) IsPrimitive() bool   { return v.u != tagObject }
func (v Value) Float() float64      { return math.Float64frombits(v.u ^ numberMask) }
func (v Value) BoolValue() bool     { return v.u == tagTrue }
func (v Value) StringRef() *String  { v.check(tagString); return (*String)(v.p) }
func (v Value) SymbolRef() *Symbol  { v.check(tagSymbol); return (*Symbol)(v.p) }
func (v Value) BigIntRef() *big.Int { v.check(tagBigInt); return (*big.Int)(v.p) }
func (v Value) ObjectRef() *Object  { v.check(tagObject); return (*Object)(v.p) }
func (v Value) IsCallable() bool    { return v.u == tagObject && (*Object)(v.p).IsCallable() }
func (v Value) IsConstructor() bool { return v.u == tagObject && (*Object)(v.p).IsConstructor() }
func (v Value) scope() *Scope       { v.check(tagScope); return (*Scope)(v.p) }

// check panics like the failed type assertion the typed accessors replace,
// instead of reinterpreting the wrong pointer type. It panics directly
// (the message is made by Error): a call would make check and the
// accessors too costly for Go to inline into the interpreter loop.
func (v Value) check(tag uint64) {
	if v.u != tag {
		panic(wrongKindError{v})
	}
}

type wrongKindError struct{ v Value }

func (e wrongKindError) Error() string {
	return fmt.Sprintf("vm: Value of kind %v used as another kind", e.v.Kind())
}

var kindNames = [...]string{"undefined", "empty", "null", "boolean", "number", "string", "symbol", "bigint", "object", "cell"}

func (k Kind) String() string { return kindNames[k] }

// num is Float for values known to be numbers.
func (v Value) num() float64 { return math.Float64frombits(v.u ^ numberMask) }

// boolNumber is ToNumber of a boolean.
func (v Value) boolNumber() float64 {
	if v.u == tagTrue {
		return 1
	}
	return 0
}

// cell returns the engine-internal thing v holds, or nil.
func (v Value) cell() any {
	switch v.u {
	case tagScope:
		return (*Scope)(v.p)
	case tagAccessor:
		return (*Accessor)(v.p)
	case tagCell:
		return (*cellBox)(v.p).c
	}
	return nil
}

// AsObject returns the object v holds, or nil.
func (v Value) AsObject() *Object {
	if v.u != tagObject {
		return nil
	}
	return (*Object)(v.p)
}

// IsInt32 reports whether v is a number with an int32 value (not -0).
func (v Value) IsInt32() bool {
	if !v.IsNumber() {
		return false
	}
	n := v.num()
	i := int32(n)
	return float64(i) == n && !(n == 0 && math.Signbit(n))
}

// TypeOf is the typeof operator.
func (v Value) TypeOf() string {
	switch v.Kind() {
	case KindUndefined:
		return "undefined"
	case KindNull:
		return "object"
	case KindBool:
		return "boolean"
	case KindNumber:
		return "number"
	case KindString:
		return "string"
	case KindSymbol:
		return "symbol"
	case KindBigInt:
		return "bigint"
	case KindObject:
		o := v.ObjectRef()
		if o.htmlDDA {
			return "undefined"
		}
		if o.IsCallable() {
			return "function"
		}
		return "object"
	}
	return "undefined"
}

// ToBoolean (7.1.2).
func (v Value) ToBoolean() bool {
	switch v.Kind() {
	case KindBool:
		return v.u == tagTrue
	case KindNumber:
		n := v.num()
		return n != 0 && !math.IsNaN(n)
	case KindString:
		return v.StringRef().Length() > 0
	case KindBigInt:
		return v.BigIntRef().Sign() != 0
	case KindSymbol:
		return true
	case KindObject:
		return !v.ObjectRef().htmlDDA
	}
	return false
}

// StrictEquals is IsStrictlyEqual (7.2.15).
func StrictEquals(a, b Value) bool {
	if a.Kind() != b.Kind() {
		return false
	}
	switch a.Kind() {
	case KindUndefined, KindNull, KindEmpty:
		return true
	case KindBool:
		return a.u == b.u
	case KindNumber:
		return a.num() == b.num()
	case KindString:
		return a.StringRef().Equals(b.StringRef())
	case KindBigInt:
		return a.BigIntRef().Cmp(b.BigIntRef()) == 0
	}
	return a.p == b.p
}

// SameValue (7.2.10).
func SameValue(a, b Value) bool {
	if a.IsNumber() && b.IsNumber() {
		x, y := a.num(), b.num()
		if math.IsNaN(x) && math.IsNaN(y) {
			return true
		}
		if x == 0 && y == 0 {
			return math.Signbit(x) == math.Signbit(y)
		}
		return x == y
	}
	return StrictEquals(a, b)
}

// SameValueZero (7.2.11).
func SameValueZero(a, b Value) bool {
	if a.IsNumber() && b.IsNumber() && math.IsNaN(a.num()) && math.IsNaN(b.num()) {
		return true
	}
	return StrictEquals(a, b)
}

// isNullishOrHTMLDDA reports whether v == null: undefined, null or an
// [[IsHTMLDDA]] object.
func (v Value) isNullishOrHTMLDDA() bool {
	return v.IsUndefined() || v.IsNull() || v.IsObject() && v.ObjectRef().htmlDDA
}
