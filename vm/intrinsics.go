package vm

import (
	"math"
	"slices"
	"strings"
)

// Fast paths for hot builtins (JSC's intrinsics): they run without the
// Call allocation of a native call when their arguments are simple, and
// otherwise fall back to the full implementation.

func setFast(f *Object, fast fastNative) { f.fn.(*nativeFunction).fast = fast }

// markIntrinsics gives the builtin functions JSC marks with an Intrinsic
// theirs (builtinIntrinsics); owners this realm does not have are skipped.
func (r *Realm) markIntrinsics() {
	for _, b := range builtinIntrinsics {
		owner := r.GlobalObject
		for _, part := range strings.Split(b.owner, ".") {
			if owner == nil || part == "globalThis" {
				continue
			}
			owner = owner.getOwnData(part)
		}
		if owner == nil {
			continue
		}
		if f := owner.getOwnData(b.name); f != nil {
			if n, ok := f.fn.(*nativeFunction); ok {
				n.intrinsic = b.intrinsic
			}
		}
	}
	// JSC gives these theirs when it makes them (NumberConstructor.cpp,
	// StringConstructor.cpp), not in a property table.
	for name, intrinsic := range map[string]Intrinsic{"Number": NumberConstructorIntrinsic, "String": StringConstructorIntrinsic} {
		if f := r.GlobalObject.getOwnData(name); f != nil {
			if n, ok := f.fn.(*nativeFunction); ok {
				n.intrinsic = intrinsic
			}
		}
	}
}

// getOwnData returns o's own data property name if it is an object.
func (o *Object) getOwnData(name string) *Object {
	p, ok := o.GetOwnProperty(StrKey(name))
	if !ok || p.IsAccessor() {
		return nil
	}
	return p.Value.AsObject()
}

func (r *Realm) installIntrinsics() {
	r.markIntrinsics()
	arrayProto := r.ArrayPrototype
	setFast(arrayProto.GetStr("push").ObjectRef(), fastArrayPush)
	setFast(arrayProto.GetStr("pop").ObjectRef(), fastArrayPop)
	str := r.GlobalObject.GetStr("String").ObjectRef()
	setFast(str.GetStr("fromCharCode").ObjectRef(), fastFromCharCode)
	setFast(r.StringPrototype.GetStr("charCodeAt").ObjectRef(), fastCharCodeAt)
	m := r.GlobalObject.GetStr("Math").ObjectRef()
	for name, f := range map[string]func(float64) float64{
		"floor": math.Floor, "ceil": math.Ceil, "abs": math.Abs, "sqrt": math.Sqrt, "trunc": math.Trunc,
	} {
		setFast(m.GetStr(name).ObjectRef(), fastMathUnary(f))
	}
}

// plainArray returns the array behind this if its indices are a dense
// element vector that pushing to or popping from cannot observe.
func plainArray(this Value) (*Object, *arrayObject) {
	o := this.AsObject()
	if o == nil || o.class != ClassArray {
		return nil, nil
	}
	a, ok := o.exotic.(*arrayObject)
	if !ok || int(a.length) != len(o.elements) || !a.lengthWritable || len(o.sparse()) > 0 {
		return nil, nil
	}
	if p := o.proto; p != nil && p.mayHaveIndexedProperties() {
		return nil, nil
	}
	return o, a
}

func fastArrayPush(vm *VM, this Value, args []Value) (Value, bool) {
	o, a := plainArray(this)
	if o == nil || !o.extensible || uint64(a.length)+uint64(len(args)) >= math.MaxUint32 {
		return Undefined, false
	}
	o.elements = appendElements(o.elements, args...)
	a.length += uint32(len(args))
	return Number(float64(a.length)), true
}

func fastArrayPop(vm *VM, this Value, args []Value) (Value, bool) {
	o, a := plainArray(this)
	if o == nil || a.length == 0 || o.elements[a.length-1].IsEmpty() {
		return Undefined, false
	}
	n := a.length - 1
	v := o.elements[n]
	o.elements[n] = Undefined
	o.elements = o.elements[:n]
	a.length = n
	return v, true
}

func fastFromCharCode(vm *VM, this Value, args []Value) (Value, bool) {
	if len(args) != 1 || !args[0].IsNumber() {
		return Undefined, false
	}
	return StringValue(charString(uint16(toUint32(args[0].num())))), true
}

func fastCharCodeAt(vm *VM, this Value, args []Value) (Value, bool) {
	if !this.IsString() || len(args) != 1 || !args[0].IsNumber() {
		return Undefined, false
	}
	s, pos := this.StringRef(), args[0].num()
	if pos != math.Trunc(pos) || pos < 0 || pos >= float64(s.Length()) {
		return Undefined, false
	}
	return Int(int(s.At(int(pos)))), true
}

func fastMathUnary(f func(float64) float64) fastNative {
	return func(vm *VM, this Value, args []Value) (Value, bool) {
		if len(args) != 1 || !args[0].IsNumber() {
			return Undefined, false
		}
		return Number(f(args[0].num())), true
	}
}

// argAt is argument i of a fast native's arguments (undefined if absent).
func argAt(args []Value, i int) Value {
	if i < len(args) {
		return args[i]
	}
	return Undefined
}

// numberArgs reports whether args holds only numbers and undefined, which
// ToIntegerOrInfinity converts without side effects.
func numberArgs(args []Value) bool {
	for _, a := range args {
		if !a.IsNumber() && !a.IsUndefined() {
			return false
		}
	}
	return true
}

func fastMinMax(isMax bool) fastNative {
	return func(vm *VM, this Value, args []Value) (Value, bool) {
		result := posInf
		if isMax {
			result = negInf
		}
		for _, a := range args {
			if !a.IsNumber() {
				return Undefined, false
			}
			n := a.num()
			switch {
			case math.IsNaN(n) || math.IsNaN(result):
				result = math.NaN()
			case isMax && (n > result || n == 0 && result == 0 && !math.Signbit(n)):
				result = n
			case !isMax && (n < result || n == 0 && result == 0 && math.Signbit(n)):
				result = n
			}
		}
		return Number(result), true
	}
}

func fastParseInt(vm *VM, this Value, args []Value) (Value, bool) {
	if len(args) == 0 || !args[0].IsString() {
		return Undefined, false
	}
	radix := int32(0)
	if r := argAt(args, 1); r.IsNumber() {
		radix = toInt32(r.num())
	} else if !r.IsUndefined() {
		return Undefined, false
	}
	return Number(parseIntPrefix(trimJSSpaceStart(args[0].StringRef()), int(radix))), true
}

func fastStringSlice(vm *VM, this Value, args []Value) (Value, bool) {
	if !this.IsString() || !numberArgs(args) {
		return Undefined, false
	}
	s := this.StringRef()
	n := int64(s.Length())
	from := vm.relativeIndex(argAt(args, 0), n, 0)
	to := vm.relativeIndex(argAt(args, 1), n, n)
	if from >= to {
		return StringValue(emptyString), true
	}
	if p := vm.jitStrings; p == nil || p.n == 0 {
		vm.fillStringPool() // for the slice thunk (jit_thunks.go)
	}
	return StringValue(s.Substring(int(from), int(to))), true
}

func fastStringSubstring(vm *VM, this Value, args []Value) (Value, bool) {
	if !this.IsString() || !numberArgs(args) {
		return Undefined, false
	}
	s := this.StringRef()
	n := float64(s.Length())
	start := math.Min(math.Max(vm.ToIntegerOrInfinity(argAt(args, 0)), 0), n)
	end := n
	if e := argAt(args, 1); !e.IsUndefined() {
		end = math.Min(math.Max(vm.ToIntegerOrInfinity(e), 0), n)
	}
	if start > end {
		start, end = end, start
	}
	return StringValue(s.Substring(int(start), int(end))), true
}

func fastStringIndexOf(vm *VM, this Value, args []Value) (Value, bool) {
	if !this.IsString() || len(args) == 0 || !args[0].IsString() || !numberArgs(args[1:]) {
		return Undefined, false
	}
	s := this.StringRef()
	start := int(math.Min(math.Max(vm.ToIntegerOrInfinity(argAt(args, 1)), 0), float64(s.Length())))
	return Int(s.IndexOf(args[0].StringRef(), start)), true
}

// fastHasOwnProperty handles a string key (its cached property key) on an
// ordinary object or an array.
func fastHasOwnProperty(vm *VM, this Value, args []Value) (Value, bool) {
	o := this.AsObject()
	if o == nil || len(args) == 0 || !args[0].IsString() || (o.exotic != nil && o.class != ClassArray) {
		return Undefined, false
	}
	return Bool(o.HasOwnProperty(args[0].StringRef().Key())), true
}

// slotCache remembers where an object's property is for one shape.
type slotCache struct {
	shape   *shape
	version uint32
	index   int
}

func (c *slotCache) slot(o *Object, k PropertyKey) int {
	if s := o.props.shape; s != c.shape || s.version != c.version {
		c.shape, c.version, c.index = s, s.version, o.props.find(k)
	}
	return c.index
}

// arraySpeciesIsDefault reports whether ArraySpeciesCreate(o, n) is
// ArrayCreate(n) in realm r for array o of r (JSC's array species
// watchpoint): o has no own constructor, and Array.prototype.constructor
// and Array[Symbol.species] are the original ones.
func (r *Realm) arraySpeciesIsDefault(o *Object) bool {
	if o.proto != r.ArrayPrototype || o.props.find(constructorKey) >= 0 {
		return false
	}
	p := r.ArrayPrototype
	i := r.speciesSlots[0].slot(p, constructorKey)
	if i < 0 || p.props.shape.flags[i]&(accessorFlag|lazyFlag) != 0 || p.props.vals[i].AsObject() != r.ArrayConstructor {
		return false
	}
	c := r.ArrayConstructor
	i = r.speciesSlots[1].slot(c, SymKey(SymbolSpecies))
	return i >= 0 && c.props.vals[i] == r.arraySpecies
}

// fastArraySlice slices a plain array of the current realm into a new one.
func fastArraySlice(vm *VM, this Value, args []Value) (Value, bool) {
	o, _ := plainArray(this)
	if o == nil || !numberArgs(args) || !vm.realm.arraySpeciesIsDefault(o) {
		return Undefined, false
	}
	n := int64(len(o.elements))
	k := vm.relativeIndex(argAt(args, 0), n, 0)
	final := vm.relativeIndex(argAt(args, 1), n, n)
	if k >= final {
		return ObjectValue(vm.realm.NewArray()), true
	}
	src := o.elements[k:final]
	if slices.Contains(src, Empty) {
		return Undefined, false // holes: the prototypes may have elements
	}
	return ObjectValue(vm.realm.NewArray(slices.Clone(src)...)), true
}

// fastArrayIndexOf searches a plain array without holes.
func fastArrayIndexOf(vm *VM, this Value, args []Value) (Value, bool) {
	o, _ := plainArray(this)
	if o == nil || len(args) == 0 || !numberArgs(args[1:]) {
		return Undefined, false
	}
	n := int64(len(o.elements))
	if n == 0 {
		return Int(-1), true
	}
	x := args[0]
	for k := vm.relativeIndex(argAt(args, 1), n, 0); k < n; k++ {
		v := o.elements[k]
		if v.IsEmpty() {
			return Undefined, false
		}
		if StrictEquals(v, x) {
			return Number(float64(k)), true
		}
	}
	return Int(-1), true
}
