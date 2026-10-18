package bytecode

import (
	"fmt"
	"math"
	"math/big"
	"strconv"

	"github.com/baslentfert/jsc2go/wtf/dtoa"
)

// The constant pool of an UnlinkedCodeBlock. JSC stores JSValues there; we
// are independent of the runtime, so a constant is one of the Go types below
// and the runtime turns it into a JSValue when it links the code block.

// Constant is a value in a code block's constant pool.
type Constant interface{ isConstant() }

// SourceCodeRepresentation records how a number constant was written so the
// runtime can pick int32 or double representation.
type SourceCodeRepresentation uint8

const (
	SourceCodeRepresentationOther SourceCodeRepresentation = iota
	SourceCodeRepresentationInteger
	SourceCodeRepresentationDouble
	SourceCodeRepresentationLinkTimeConstant
)

type (
	// Empty is JSValue(): the TDZ / hole marker.
	Empty struct{}
	// Undefined is jsUndefined().
	Undefined struct{}
	// Null is jsNull().
	Null struct{}
	// Bool is a boolean constant.
	Bool bool
	// Number is a number constant. Int32 says whether JSC would encode it as
	// an int32 JSValue (integral, in range, not -0, not forced to double).
	Number struct {
		Value float64
		Int32 bool
	}
	// String is a string constant in WTF-8 (see package ident).
	String string
	// BigInt is a BigInt constant.
	BigInt struct{ Value *big.Int }
	// TemplateObject is JSTemplateObjectDescriptor: the strings of a tagged
	// template plus the source offset that identifies its call site.
	TemplateObject struct {
		Descriptor *TemplateObjectDescriptor
		EndOffset  int
	}
	// ArrayBuffer is the immutable butterfly of op_new_array_buffer.
	ArrayBuffer struct {
		Values       []Constant
		IndexingType IndexingType
	}
	// ForwardedArguments stands for the call frame's arguments in the
	// arguments operand of call_varargs (x.apply(y, arguments) where
	// arguments is only forwarded: parser.ArgumentsOnlyForwardedFeature).
	ForwardedArguments struct{}
)

func (Empty) isConstant()              {}
func (Undefined) isConstant()          {}
func (Null) isConstant()               {}
func (Bool) isConstant()               {}
func (Number) isConstant()             {}
func (String) isConstant()             {}
func (BigInt) isConstant()             {}
func (*TemplateObject) isConstant()    {}
func (*ArrayBuffer) isConstant()       {}
func (*SymbolTable) isConstant()       {}
func (ForwardedArguments) isConstant() {}

// JSNumber is jsNumber(double): it uses the int32 encoding when possible.
func JSNumber(v float64) Number {
	i := int32(v)
	return Number{Value: v, Int32: float64(i) == v && !(v == 0 && math.Signbit(v))}
}

// JSInt32 is jsNumber(int32_t).
func JSInt32(v int32) Number { return Number{Value: float64(v), Int32: true} }

// JSDoubleNumber is jsDoubleNumber: a number that is always a double.
func JSDoubleNumber(v float64) Number { return Number{Value: v} }

// IsInt32 reports whether c is an int32 number constant.
func IsInt32(c Constant) bool {
	n, ok := c.(Number)
	return ok && n.Int32
}

// TemplateObjectDescriptor holds the raw and cooked strings of a template
// literal. A cooked string is nil when it contains an invalid escape.
type TemplateObjectDescriptor struct {
	RawStrings    []string
	CookedStrings []*string
}

// ConstantKey returns a comparable key that identifies c for deduplication,
// distinguishing +0 from -0 and int32 from double encodings like JSC's
// EncodedJSValueWithRepresentation.
func ConstantKey(c Constant, repr SourceCodeRepresentation) any {
	type numberKey struct {
		bits  uint64
		int32 bool
		repr  SourceCodeRepresentation
	}
	switch v := c.(type) {
	case Number:
		return numberKey{math.Float64bits(v.Value), v.Int32, repr}
	case BigInt:
		return v.Value // pointer identity; BigInts are deduplicated by the generator
	}
	return c
}

// ConstantString formats a constant for bytecode dumps.
func ConstantString(c Constant) string {
	switch v := c.(type) {
	case Empty:
		return "<JSValue()>"
	case Undefined:
		return "Undefined"
	case Null:
		return "Null"
	case Bool:
		if v {
			return "true"
		}
		return "false"
	case Number:
		if v.Int32 {
			return "Int32: " + strconv.Itoa(int(v.Value))
		}
		return "Double: " + dtoa.NumberToString(v.Value)
	case String:
		return "String (atomic),8Bit:(1),length:(" + strconv.Itoa(len(v)) + "): " + string(v)
	case BigInt:
		return "BigInt: " + v.Value.String()
	case *TemplateObject:
		return fmt.Sprintf("TemplateObject(endOffset %d)", v.EndOffset)
	case *ArrayBuffer:
		return fmt.Sprintf("ArrayBuffer(%d)", len(v.Values))
	case *SymbolTable:
		return "SymbolTable"
	case LinkTimeConstant:
		return "LinkTimeConstant: " + v.String()
	}
	return fmt.Sprintf("%v", c)
}

// RegExp is a regular expression literal; the runtime compiles it when the
// code block is linked.
type RegExp struct {
	Pattern string
	Flags   string
}

func (*RegExp) isConstant() {}

// DefinePropertyAttributes packs the attributes of op_define_data_property
// and op_define_accessor_property: tri-state configurable, enumerable and
// writable, and presence bits for value, get and set.
type DefinePropertyAttributes uint32

const (
	definePropertyConfigurableShift = 0
	definePropertyEnumerableShift   = 2
	definePropertyWritableShift     = 4
	definePropertyValueShift        = 6
	definePropertyGetShift          = 7
	definePropertySetShift          = 8

	triStateFalse         = 0
	triStateTrue          = 1
	triStateIndeterminate = 2
)

// NewDefinePropertyAttributes returns attributes with everything unset.
func NewDefinePropertyAttributes() DefinePropertyAttributes {
	return triStateIndeterminate<<definePropertyConfigurableShift |
		triStateIndeterminate<<definePropertyEnumerableShift |
		triStateIndeterminate<<definePropertyWritableShift
}

func (a DefinePropertyAttributes) HasValue() bool { return a&(1<<definePropertyValueShift) != 0 }
func (a DefinePropertyAttributes) HasGet() bool   { return a&(1<<definePropertyGetShift) != 0 }
func (a DefinePropertyAttributes) HasSet() bool   { return a&(1<<definePropertySetShift) != 0 }

func (a *DefinePropertyAttributes) SetValue() { *a |= 1 << definePropertyValueShift }
func (a *DefinePropertyAttributes) SetGet()   { *a |= 1 << definePropertyGetShift }
func (a *DefinePropertyAttributes) SetSet()   { *a |= 1 << definePropertySetShift }

func (a *DefinePropertyAttributes) setTriState(shift uint, v bool) {
	*a &^= 3 << shift
	if v {
		*a |= triStateTrue << shift
	}
}

func (a *DefinePropertyAttributes) SetConfigurable(v bool) {
	a.setTriState(definePropertyConfigurableShift, v)
}
func (a *DefinePropertyAttributes) SetEnumerable(v bool) {
	a.setTriState(definePropertyEnumerableShift, v)
}
func (a *DefinePropertyAttributes) SetWritable(v bool) { a.setTriState(definePropertyWritableShift, v) }

func (a DefinePropertyAttributes) triState(shift uint) (value, ok bool) {
	switch (a >> shift) & 3 {
	case triStateTrue:
		return true, true
	case triStateFalse:
		return false, true
	}
	return false, false
}

// Configurable, Enumerable and Writable report the attribute and whether it
// is present.
func (a DefinePropertyAttributes) Configurable() (bool, bool) {
	return a.triState(definePropertyConfigurableShift)
}
func (a DefinePropertyAttributes) Enumerable() (bool, bool) {
	return a.triState(definePropertyEnumerableShift)
}
func (a DefinePropertyAttributes) Writable() (bool, bool) {
	return a.triState(definePropertyWritableShift)
}

// IndexingTypeForConstant is indexingTypeForValue: the array storage shape
// that can hold c.
func IndexingTypeForConstant(c Constant) IndexingType {
	if n, ok := c.(Number); ok {
		if n.Int32 {
			return Int32Shape
		}
		if n.Value == n.Value { // not NaN
			return DoubleShape
		}
	}
	return ContiguousShape
}

// LeastUpperBoundOfIndexingTypeAndConstant widens t to also hold c.
func LeastUpperBoundOfIndexingTypeAndConstant(t IndexingType, c Constant) IndexingType {
	return max(t, IndexingTypeForConstant(c)|(t&IsArray))
}
