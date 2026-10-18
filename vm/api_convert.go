package vm

// Converting between Go and JavaScript values (the embedding API).
//
// Go values become plain JavaScript values: structs, maps and slices are
// copied into ordinary objects and arrays, so that generated code caches
// their properties (an object that ran Go code on every access would not
// be cacheable). The fields of one struct type are added in one order, so
// its objects share a shape. Go functions become native functions whose
// argument and result conversions are built once per function type.

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

// maxConvertDepth bounds the nesting ToValue and Export follow (a cycle in
// a Go or JavaScript value would otherwise not end).
const maxConvertDepth = 1000

var (
	valueType  = reflect.TypeOf(Value{})
	objectType = reflect.TypeOf((*Object)(nil))
	errorType  = reflect.TypeOf((*error)(nil)).Elem()
	timeType   = reflect.TypeOf(time.Time{})
	bigIntType = reflect.TypeOf((*big.Int)(nil))
	callType   = reflect.TypeOf(NativeFunc(nil))
	anyType    = reflect.TypeOf((*any)(nil)).Elem()
)

// structField is a struct field as JavaScript sees it.
type structField struct {
	name  string
	index []int
}

// structFields are the exported fields of struct type t, by their json
// tag's name if they have one ("-" hides a field) and otherwise their Go
// name, in declaration order. Cached per type (types are finite).
func structFields(t reflect.Type) []structField {
	if f, ok := structFieldCache.Load(t); ok {
		return f.([]structField)
	}
	var fields []structField
	for _, f := range reflect.VisibleFields(t) {
		if !f.IsExported() || f.Anonymous {
			continue
		}
		name := f.Name
		if tag, ok := f.Tag.Lookup("json"); ok {
			tag, _, _ = strings.Cut(tag, ",")
			if tag == "-" {
				continue
			}
			if tag != "" {
				name = tag
			}
		}
		fields = append(fields, structField{name, f.Index})
	}
	structFieldCache.Store(t, fields)
	return fields
}

var structFieldCache sync.Map // reflect.Type -> []structField

// ToValue converts a Go value to a JavaScript value of realm r:
//   - nil, and nil pointers, maps, slices and functions: null
//   - Value and *Object: as they are
//   - bool, integers, floats: booleans and numbers (integers beyond 2^53 lose precision)
//   - string: a string; *big.Int: a BigInt; time.Time: a Date
//   - error: an Error with its message
//   - []byte: a Uint8Array (copied)
//   - other slices and arrays: an array (copied)
//   - maps with string keys: an object (copied; keys in sorted order)
//   - structs and pointers to structs: an object with the exported fields
//     (copied; json tag names honored) and the exported methods
//   - NativeFunc (func(*Call) Value): a native function, as is
//   - other functions: a native function converting its arguments and
//     results (see FuncValue)
//
// Other values (channels, complex numbers, unsafe pointers) throw a
// TypeError.
func (r *Realm) ToValue(x any) Value {
	if x == nil {
		return Null
	}
	switch v := x.(type) {
	case Value:
		return v
	case *Object:
		return ObjectValue(v)
	case string:
		return Str(v)
	case bool:
		return Bool(v)
	case int:
		return Number(float64(v))
	case int64:
		return Number(float64(v))
	case float64:
		return Number(v)
	case NativeFunc:
		return ObjectValue(r.newNativeFunction("", 0, v))
	case func(*Call) Value:
		return ObjectValue(r.newNativeFunction("", 0, v))
	}
	return r.toValue(reflect.ValueOf(x), 0)
}

func (r *Realm) toValue(rv reflect.Value, depth int) Value {
	if depth > maxConvertDepth {
		r.vm.throwTypeError("ToValue: value nested too deep (a cycle?)")
	}
	if !rv.IsValid() {
		return Null
	}
	if h := r.vm.HostHooks.ToJS; h != nil {
		if v, ok := h(r, rv); ok {
			return v
		}
	}
	t := rv.Type()
	switch t {
	case valueType:
		return rv.Interface().(Value)
	case objectType:
		return ObjectValue(rv.Interface().(*Object))
	case timeType:
		return ObjectValue(r.newDate(rv.Interface().(time.Time)))
	case bigIntType:
		if rv.IsNil() {
			return Null
		}
		return BigIntValue(new(big.Int).Set(rv.Interface().(*big.Int)))
	}
	if t.Implements(errorType) && t.Kind() != reflect.Struct {
		if rv.Kind() == reflect.Pointer && rv.IsNil() || rv.Kind() == reflect.Interface && rv.IsNil() {
			return Null
		}
		return ObjectValue(r.newError(ErrorKindError, rv.Interface().(error).Error()))
	}
	switch t.Kind() {
	case reflect.Bool:
		return Bool(rv.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return Number(float64(rv.Int()))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return Number(float64(rv.Uint()))
	case reflect.Float32, reflect.Float64:
		return Number(rv.Float())
	case reflect.String:
		return Str(rv.String())
	case reflect.Interface:
		if rv.IsNil() {
			return Null
		}
		return r.toValue(rv.Elem(), depth+1)
	case reflect.Pointer:
		if rv.IsNil() {
			return Null
		}
		if t.Elem().Kind() == reflect.Struct && t.Elem() != timeType {
			return ObjectValue(r.structObject(rv.Elem(), rv, depth))
		}
		return r.toValue(rv.Elem(), depth+1)
	case reflect.Struct:
		return ObjectValue(r.structObject(rv, reflect.Value{}, depth))
	case reflect.Slice:
		if rv.IsNil() {
			return Null
		}
		if t.Elem().Kind() == reflect.Uint8 {
			return ObjectValue(r.newUint8Array(rv.Bytes()))
		}
		fallthrough
	case reflect.Array:
		vals := make([]Value, rv.Len())
		for i := range vals {
			vals[i] = r.toValue(rv.Index(i), depth+1)
		}
		return ObjectValue(r.NewArray(vals...))
	case reflect.Map:
		if rv.IsNil() {
			return Null
		}
		if t.Key().Kind() != reflect.String {
			r.vm.throwTypeError("ToValue: map keys must be strings, not %s", t.Key())
		}
		keys := rv.MapKeys()
		sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
		o := r.NewObject()
		for _, k := range keys {
			o.CreateDataPropertyOrThrow(StrKey(k.String()), r.toValue(rv.MapIndex(k), depth+1))
		}
		return ObjectValue(o)
	case reflect.Func:
		if rv.IsNil() {
			return Null
		}
		if t == callType {
			return ObjectValue(r.newNativeFunction("", 0, rv.Interface().(NativeFunc)))
		}
		return ObjectValue(r.funcObject(rv))
	}
	r.vm.throwTypeError("ToValue: cannot convert a Go %s", t)
	return Undefined
}

// structObject copies struct s into a plain object: its exported fields as
// data properties, in declaration order (so that all objects of one type
// share a shape), then its exported methods as non-enumerable functions,
// those of ptr's type if ptr is set (pointer receivers then see and change
// the Go value).
func (r *Realm) structObject(s, ptr reflect.Value, depth int) *Object {
	o := r.NewObject()
	for _, f := range structFields(s.Type()) {
		fv, err := s.FieldByIndexErr(f.index)
		v := Undefined
		if err == nil {
			v = r.toValue(fv, depth+1)
		}
		o.putDirect(StrKey(f.name), v, DefaultFlags)
	}
	recv := s
	if ptr.IsValid() {
		recv = ptr
	}
	for i := range recv.NumMethod() {
		m := recv.Type().Method(i)
		if !m.IsExported() {
			continue
		}
		o.putDirect(StrKey(m.Name), ObjectValue(r.funcObject(recv.Method(i))), Writable|Configurable)
	}
	return o
}

// newDate makes a Date of t.
func (r *Realm) newDate(t time.Time) *Object {
	o := r.newObject(r.DatePrototype)
	o.class = ClassDate
	o.internal = &dateData{timeClip(float64(t.UnixMilli()))}
	return o
}

// newUint8Array makes a Uint8Array holding a copy of b.
func (r *Realm) newUint8Array(b []byte) *Object {
	vm := r.vm
	ta := vm.Construct(r.TypedArrayConstructors[kindUint8], []Value{Number(float64(len(b)))}, nil).AsObject()
	for i, c := range b {
		ta.Set(IndexKey(uint32(i)), Number(float64(c)), ObjectValue(ta))
	}
	return ta
}

// FuncValue converts Go function f to a JavaScript function of realm r
// (ToValue does the same for every func value).
func (r *Realm) FuncValue(f any) Value { return r.ToValue(f) }

// funcConv is how a Go function type's arguments and results are converted,
// built once per type.
type funcConv struct {
	in       []reflect.Type // parameter types (the variadic one as its element type)
	variadic bool
	errLast  bool // the last result is an error, thrown when not nil
	results  int  // results besides that error
}

var funcConvCache sync.Map // reflect.Type -> *funcConv

func convFor(t reflect.Type) *funcConv {
	if c, ok := funcConvCache.Load(t); ok {
		return c.(*funcConv)
	}
	c := &funcConv{variadic: t.IsVariadic()}
	for i := range t.NumIn() {
		in := t.In(i)
		if c.variadic && i == t.NumIn()-1 {
			in = in.Elem()
		}
		c.in = append(c.in, in)
	}
	c.results = t.NumOut()
	if c.results > 0 && t.Out(c.results-1) == errorType {
		c.errLast = true
		c.results--
	}
	funcConvCache.Store(t, c)
	return c
}

// funcObject makes a native function that calls Go function f: each
// argument is converted to f's parameter type (missing ones are zero; a
// variadic parameter takes the rest), and the results to JavaScript (none:
// undefined; one: it; several: an array). A non-nil error result throws an
// Error with its message; a Go panic throws an Error too (JavaScript
// exceptions and interrupts pass through).
func (r *Realm) funcObject(f reflect.Value) *Object {
	t := f.Type()
	c := convFor(t)
	fixed := len(c.in)
	if c.variadic {
		fixed--
	}
	return r.newNativeFunction("", fixed, func(call *Call) (result Value) {
		vm := call.VM
		args := make([]reflect.Value, 0, len(c.in))
		for i := 0; i < fixed; i++ {
			args = append(args, vm.goArg(call.Arg(i), c.in[i], i))
		}
		if c.variadic {
			for i := fixed; i < len(call.Args); i++ {
				args = append(args, vm.goArg(call.Args[i], c.in[fixed], i))
			}
		}
		defer func() {
			if p := recover(); p != nil {
				switch p.(type) {
				case *Exception:
					panic(p)
				}
				if p == ErrInterrupted || p == errStringTooLong {
					panic(p)
				}
				vm.throwError(ErrorKindError, "Go panic: %v", p)
			}
		}()
		out := f.Call(args)
		if c.errLast {
			if err := out[len(out)-1]; !err.IsNil() {
				vm.Throw(ObjectValue(call.Realm().newError(ErrorKindError, err.Interface().(error).Error())))
			}
			out = out[:len(out)-1]
		}
		switch len(out) {
		case 0:
			return Undefined
		case 1:
			return call.Realm().toValue(out[0], 0)
		}
		vals := make([]Value, len(out))
		for i, o := range out {
			vals[i] = call.Realm().toValue(o, 0)
		}
		return ObjectValue(call.Realm().NewArray(vals...))
	})
}

// goArg converts argument i to Go type t, or throws a TypeError.
func (vm *VM) goArg(v Value, t reflect.Type, i int) reflect.Value {
	rv, err := vm.fromJS(v, t, 0)
	if err != nil {
		vm.throwTypeError("argument %d: %v", i+1, err)
	}
	return rv
}

// Export converts v to a Go value:
//   - undefined and null: nil; booleans: bool; strings: string
//   - numbers: int64 if integral and in range (not -0), else float64
//   - BigInts: *big.Int; Symbols: *Symbol
//   - arrays: []any; Dates: time.Time; Uint8Arrays: []byte (copies)
//   - functions: func(args ...any) (any, error), which calls the
//     function with undefined as this on the VM's goroutine
//   - other objects: map[string]any of their own enumerable string keys
//
// Nested values are converted the same way.
func (vm *VM) Export(v Value) any {
	return vm.export(v, 0)
}

func (vm *VM) export(v Value, depth int) any {
	if depth > maxConvertDepth {
		vm.throwTypeError("Export: value nested too deep (a cycle?)")
	}
	switch {
	case v.IsUndefined(), v.IsNull():
		return nil
	case v.u == tagTrue:
		return true
	case v.u == tagFalse:
		return false
	case v.IsNumber():
		f := v.num()
		if f == math.Trunc(f) && f >= -(1<<63) && f < 1<<63 && !(f == 0 && math.Signbit(f)) {
			return int64(f)
		}
		return f
	case v.IsString():
		return v.StringRef().GoString()
	case v.IsSymbol():
		return v.SymbolRef()
	case v.IsBigInt():
		return new(big.Int).Set(v.BigIntRef())
	}
	o := v.AsObject()
	if o == nil {
		return nil
	}
	switch {
	case o.IsCallable():
		return func(args ...any) (any, error) {
			vals := make([]Value, len(args))
			for i, a := range args {
				vals[i] = vm.realm.ToValue(a)
			}
			var res any
			exc := vm.Try(func() { res = vm.Export(vm.Call(v, Undefined, vals...)) })
			if exc != nil {
				return nil, exc
			}
			return res, nil
		}
	case o.class == ClassDate:
		return dateTime(o)
	case o.class == ClassArray:
		list := vm.CreateListFromArrayLike(v, false)
		out := make([]any, len(list))
		for i, e := range list {
			out[i] = vm.export(e, depth+1)
		}
		return out
	case o.class == ClassTypedArray && o.internal.(*typedArray).kind == kindUint8:
		return vm.uint8Bytes(o)
	}
	m := map[string]any{}
	for _, k := range o.OwnPropertyKeys() {
		if k.IsSymbol() {
			continue
		}
		if p, ok := o.GetOwnProperty(k); ok && p.Flags&Enumerable != 0 {
			m[k.String()] = vm.export(o.Get(k, v), depth+1)
		}
	}
	return m
}

// dateTime is a Date's time value as a time.Time (the zero time for an
// invalid date).
func dateTime(o *Object) time.Time {
	t := o.internal.(*dateData).t
	if math.IsNaN(t) {
		return time.Time{}
	}
	return time.UnixMilli(int64(t))
}

// uint8Bytes copies a Uint8Array's elements.
func (vm *VM) uint8Bytes(o *Object) []byte {
	n := int(vm.ToLength(o.GetStr("length")))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(vm.ToNumber(o.Get(IndexKey(uint32(i)), ObjectValue(o))))
	}
	return b
}

// ExportTo converts v into the Go value target points to (a non-nil
// pointer): numbers into integer types must be integral and in range;
// arrays and array-likes into slices and arrays; objects into maps with
// string keys and into structs (by the field names ToValue uses; missing
// properties leave fields as they are); Dates into time.Time; functions
// into func types (called on the VM's goroutine; a trailing error result
// receives an exception); anything into any (as Export) and Value. It
// returns an error for a value that does not fit.
func (vm *VM) ExportTo(v Value, target any) error {
	pv := reflect.ValueOf(target)
	if pv.Kind() != reflect.Pointer || pv.IsNil() {
		return errors.New("ExportTo: target must be a non-nil pointer")
	}
	var err error
	exc := vm.Try(func() {
		var rv reflect.Value
		if rv, err = vm.fromJS(v, pv.Elem().Type(), 0); err == nil {
			pv.Elem().Set(rv)
		}
	})
	if exc != nil {
		return exc
	}
	return err
}

// fromJS converts v to a Go value of type t.
func (vm *VM) fromJS(v Value, t reflect.Type, depth int) (reflect.Value, error) {
	if depth > maxConvertDepth {
		return reflect.Value{}, errors.New("value nested too deep (a cycle?)")
	}
	if h := vm.HostHooks.FromJS; h != nil {
		if x, ok := h(v, t); ok {
			return x, nil
		}
	}
	nullish := v.IsUndefined() || v.IsNull()
	switch t {
	case valueType:
		return reflect.ValueOf(v), nil
	case objectType:
		if nullish {
			return reflect.Zero(t), nil
		}
		if o := v.AsObject(); o != nil {
			return reflect.ValueOf(o), nil
		}
		return reflect.Value{}, fmt.Errorf("%s is not an object", vm.describe(v))
	case timeType:
		if o := v.AsObject(); o != nil && o.class == ClassDate {
			return reflect.ValueOf(dateTime(o)), nil
		}
		return reflect.Value{}, fmt.Errorf("%s is not a Date", vm.describe(v))
	case bigIntType:
		if v.IsBigInt() {
			return reflect.ValueOf(new(big.Int).Set(v.BigIntRef())), nil
		}
		return reflect.Value{}, fmt.Errorf("%s is not a BigInt", vm.describe(v))
	}
	switch t.Kind() {
	case reflect.Interface:
		if t == anyType || t.NumMethod() == 0 {
			x := vm.export(v, depth)
			if x == nil {
				return reflect.Zero(t), nil
			}
			return reflect.ValueOf(x), nil
		}
		return reflect.Value{}, fmt.Errorf("cannot convert to %s", t)
	case reflect.Bool:
		return reflect.ValueOf(v.ToBoolean()).Convert(t), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		f := vm.ToNumber(v)
		if f != math.Trunc(f) || math.IsInf(f, 0) {
			return reflect.Value{}, fmt.Errorf("%s is not an integer", vm.describe(v))
		}
		rv := reflect.New(t).Elem()
		if t.Kind() >= reflect.Uint {
			if f < 0 || rv.OverflowUint(uint64(f)) || f >= 1<<64 {
				return reflect.Value{}, fmt.Errorf("%v out of range for %s", f, t)
			}
			rv.SetUint(uint64(f))
		} else {
			if f < -(1<<63) || f >= 1<<63 || rv.OverflowInt(int64(f)) {
				return reflect.Value{}, fmt.Errorf("%v out of range for %s", f, t)
			}
			rv.SetInt(int64(f))
		}
		return rv, nil
	case reflect.Float32, reflect.Float64:
		return reflect.ValueOf(vm.ToNumber(v)).Convert(t), nil
	case reflect.String:
		return reflect.ValueOf(vm.ToString(v).GoString()).Convert(t), nil
	case reflect.Pointer:
		if nullish {
			return reflect.Zero(t), nil
		}
		ev, err := vm.fromJS(v, t.Elem(), depth+1)
		if err != nil {
			return reflect.Value{}, err
		}
		p := reflect.New(t.Elem())
		p.Elem().Set(ev)
		return p, nil
	case reflect.Slice:
		if nullish {
			return reflect.Zero(t), nil
		}
		if t.Elem().Kind() == reflect.Uint8 {
			if o := v.AsObject(); o != nil && o.class == ClassTypedArray && o.internal.(*typedArray).kind == kindUint8 {
				return reflect.ValueOf(vm.uint8Bytes(o)).Convert(t), nil
			}
		}
		if !v.IsObject() {
			return reflect.Value{}, fmt.Errorf("%s is not an array", vm.describe(v))
		}
		list := vm.CreateListFromArrayLike(v, false)
		s := reflect.MakeSlice(t, len(list), len(list))
		for i, e := range list {
			ev, err := vm.fromJS(e, t.Elem(), depth+1)
			if err != nil {
				return reflect.Value{}, fmt.Errorf("[%d]: %w", i, err)
			}
			s.Index(i).Set(ev)
		}
		return s, nil
	case reflect.Array:
		if !v.IsObject() {
			return reflect.Value{}, fmt.Errorf("%s is not an array", vm.describe(v))
		}
		list := vm.CreateListFromArrayLike(v, false)
		if len(list) != t.Len() {
			return reflect.Value{}, fmt.Errorf("array of %d elements for %s", len(list), t)
		}
		a := reflect.New(t).Elem()
		for i, e := range list {
			ev, err := vm.fromJS(e, t.Elem(), depth+1)
			if err != nil {
				return reflect.Value{}, fmt.Errorf("[%d]: %w", i, err)
			}
			a.Index(i).Set(ev)
		}
		return a, nil
	case reflect.Map:
		if nullish {
			return reflect.Zero(t), nil
		}
		o := v.AsObject()
		if o == nil || t.Key().Kind() != reflect.String {
			return reflect.Value{}, fmt.Errorf("%s cannot be a %s", vm.describe(v), t)
		}
		m := reflect.MakeMap(t)
		for _, k := range o.OwnPropertyKeys() {
			if k.IsSymbol() {
				continue
			}
			if p, ok := o.GetOwnProperty(k); !ok || p.Flags&Enumerable == 0 {
				continue
			}
			ev, err := vm.fromJS(o.Get(k, v), t.Elem(), depth+1)
			if err != nil {
				return reflect.Value{}, fmt.Errorf("%s: %w", k.String(), err)
			}
			m.SetMapIndex(reflect.ValueOf(k.String()).Convert(t.Key()), ev)
		}
		return m, nil
	case reflect.Struct:
		o := v.AsObject()
		if o == nil {
			return reflect.Value{}, fmt.Errorf("%s is not an object", vm.describe(v))
		}
		s := reflect.New(t).Elem()
		for _, f := range structFields(t) {
			pv := o.Get(StrKey(f.name), v)
			if pv.IsUndefined() {
				continue
			}
			fv, err := s.FieldByIndexErr(f.index)
			if err != nil {
				continue
			}
			ev, err := vm.fromJS(pv, fv.Type(), depth+1)
			if err != nil {
				return reflect.Value{}, fmt.Errorf("%s: %w", f.name, err)
			}
			fv.Set(ev)
		}
		return s, nil
	case reflect.Func:
		if nullish {
			return reflect.Zero(t), nil
		}
		if !v.IsCallable() {
			return reflect.Value{}, fmt.Errorf("%s is not a function", vm.describe(v))
		}
		return vm.jsFunc(v, t), nil
	}
	return reflect.Value{}, fmt.Errorf("cannot convert to %s", t)
}

// jsFunc makes a Go function of type t that calls JavaScript function f
// with undefined as this: arguments through ToValue, the result through
// fromJS. A trailing error result receives an exception (or a conversion
// error); without one, they panic as *Exception.
func (vm *VM) jsFunc(f Value, t reflect.Type) reflect.Value {
	c := convFor(t)
	return reflect.MakeFunc(t, func(in []reflect.Value) []reflect.Value {
		out := make([]reflect.Value, t.NumOut())
		for i := range out {
			out[i] = reflect.Zero(t.Out(i))
		}
		var err error
		exc := vm.Try(func() {
			args := make([]Value, 0, len(in))
			for i, a := range in {
				if c.variadic && i == len(in)-1 {
					for j := range a.Len() {
						args = append(args, vm.realm.toValue(a.Index(j), 0))
					}
					continue
				}
				args = append(args, vm.realm.toValue(a, 0))
			}
			res := vm.Call(f, Undefined, args...)
			if c.results > 0 {
				var rv reflect.Value
				if rv, err = vm.fromJS(res, t.Out(0), 0); err == nil {
					out[0] = rv
				}
			}
		})
		if exc != nil {
			err = exc
		}
		if err != nil {
			if !c.errLast {
				panic(err)
			}
			out[len(out)-1] = reflect.ValueOf(&err).Elem()
		}
		return out
	})
}

// describe names v for conversion errors without running JavaScript.
func (vm *VM) describe(v Value) string {
	switch {
	case v.IsUndefined():
		return "undefined"
	case v.IsNull():
		return "null"
	case v.IsString():
		return fmt.Sprintf("%q", v.StringRef().GoString())
	case v.IsNumber():
		return fmt.Sprint(v.num())
	case v.IsObject():
		if v.IsCallable() {
			return "a function"
		}
		return "an object"
	}
	return v.Kind().String()
}
