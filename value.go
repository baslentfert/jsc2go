package jsc2go

import (
	"errors"
	"math"

	"github.com/baslentfert/jsc2go/vm"
)

var errNotObject = errors.New("jsc2go: Set on a value that is not an object")

// Value is a JavaScript value. The zero Value is undefined.
type Value struct {
	rt *Runtime
	v  vm.Value
}

// Undefined is the value undefined.
func Undefined() Value { return Value{} }

// Null is the value null.
func Null() Value { return Value{v: vm.Null} }

// raw is v as the engine sees it (the zero vm.Value is undefined too).
func (v Value) raw() vm.Value { return v.v }

// Raw is the engine's value underneath, for what this package does not
// cover.
func (v Value) Raw() vm.Value { return v.v }

// IsUndefined, IsNull, IsObject, IsFunction, IsNumber, IsString and IsBool
// report v's type.
func (v Value) IsUndefined() bool { return v.v.IsUndefined() }
func (v Value) IsNull() bool      { return v.v.IsNull() }
func (v Value) IsObject() bool    { return v.v.IsObject() }
func (v Value) IsFunction() bool  { return v.v.IsCallable() }
func (v Value) IsNumber() bool    { return v.v.IsNumber() }
func (v Value) IsString() bool    { return v.v.IsString() }
func (v Value) IsBool() bool      { return v.v.Kind() == vm.KindBool }

// Export converts v to a Go value: nil, bool, int64 (integral numbers) or
// float64, string, []any, map[string]any, time.Time (Dates), []byte
// (Uint8Arrays), *big.Int (BigInts), or func(args ...any) (any, error)
// (functions).
func (v Value) Export() any {
	if v.rt == nil {
		// Undefined() and Null() (only those are made without a Runtime).
		return nil
	}
	var x any
	if exc := v.rt.vm.Try(func() { x = v.rt.vm.Export(v.v) }); exc != nil {
		panic(v.rt.wrapErr(exc))
	}
	return x
}

// String is v converted to a string as JavaScript's String(v) does.
func (v Value) String() string {
	if v.v.IsString() {
		return v.v.StringRef().GoString()
	}
	if v.rt == nil {
		if v.v.IsNull() {
			return "null"
		}
		return "undefined"
	}
	var s string
	if exc := v.rt.vm.Try(func() { s = v.rt.vm.ToString(v.v).GoString() }); exc != nil {
		return v.rt.wrapErr(exc).Error()
	}
	return s
}

// ToFloat is v converted to a number (NaN if it is not one).
func (v Value) ToFloat() float64 {
	if v.v.IsNumber() {
		return v.v.Float()
	}
	if v.rt == nil {
		return math.NaN()
	}
	f := math.NaN()
	v.rt.vm.Try(func() { f = v.rt.vm.ToNumber(v.v) })
	return f
}

// ToInteger is v converted to a number and truncated (0 for NaN).
func (v Value) ToInteger() int64 {
	f := v.ToFloat()
	if math.IsNaN(f) {
		return 0
	}
	return int64(f)
}

// ToBoolean is v's truthiness.
func (v Value) ToBoolean() bool { return v.v.ToBoolean() }

// Get returns property name of object v (undefined if v is not an object
// or has no such property; a getter that throws gives undefined).
func (v Value) Get(name string) Value {
	o := v.v.AsObject()
	if o == nil || v.rt == nil {
		return Undefined()
	}
	var r vm.Value
	v.rt.vm.Try(func() { r = o.GetStr(name) })
	return Value{v.rt, r}
}

// Set sets property name of object v to x converted with ToValue.
func (v Value) Set(name string, x any) error {
	o := v.v.AsObject()
	if o == nil || v.rt == nil {
		return errNotObject
	}
	if exc := v.rt.vm.Try(func() { o.SetOrThrow(vm.StrKey(name), v.rt.vm.Realm().ToValue(x)) }); exc != nil {
		return v.rt.wrapErr(exc)
	}
	return nil
}
