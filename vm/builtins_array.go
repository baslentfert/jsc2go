package vm

import (
	"math"
	"slices"
)

// The Array constructor and Array.prototype (ArrayConstructor.cpp,
// ArrayPrototype.cpp/.js).

const maxSafeInteger = 1<<53 - 1

// relativeIndex clamps ToIntegerOrInfinity(v) relative to length.
func (vm *VM) relativeIndex(v Value, length int64, defaultValue int64) int64 {
	if v.IsUndefined() {
		return defaultValue
	}
	rel := vm.ToIntegerOrInfinity(v)
	if rel < 0 {
		return int64(math.Max(float64(length)+rel, 0))
	}
	return int64(math.Min(rel, float64(length)))
}

func (vm *VM) callbackArg(c *Call, name string) Value {
	f := c.Arg(0)
	if !f.IsCallable() {
		vm.throwTypeError("Array.prototype.%s callback must be a function", name)
	}
	return f
}

// hasIndex is HasProperty(o, ToString(i)) with a fast path for chains of
// ordinary and array objects.
func (o *Object) hasIndex(i int64) bool {
	if i < 0 || i >= math.MaxUint32 {
		return o.HasProperty(indexKey64(i))
	}
	for obj := o; obj != nil; obj = obj.proto {
		if !obj.hasOrdinaryIndexes() {
			return obj.HasProperty(IndexKey(uint32(i)))
		}
		if _, ok := obj.getOwnIndex(uint32(i)); ok {
			return true
		}
	}
	return false
}

// hasOrdinaryIndexes reports whether index properties of o are ordinary
// (o is an ordinary or array object).
func (o *Object) hasOrdinaryIndexes() bool {
	if o.exotic == nil {
		return true
	}
	switch o.exotic.(type) {
	case *arrayObject, immutablePrototypeObject:
		return true
	}
	return false
}

func indexKey64(i int64) PropertyKey {
	if i >= 0 && i < math.MaxUint32 {
		return IndexKey(uint32(i))
	}
	return StrKey(NumberToString(float64(i)).String())
}

// getIdx is Get(o, ToString(i)).
func (o *Object) getIdx(i int64) Value {
	if i < 0 || i >= math.MaxUint32 {
		return o.Get(indexKey64(i), ObjectValue(o))
	}
	receiver := ObjectValue(o)
	for obj := o; obj != nil; obj = obj.proto {
		if !obj.hasOrdinaryIndexes() {
			return obj.Get(IndexKey(uint32(i)), receiver)
		}
		if p, ok := obj.getOwnIndex(uint32(i)); ok {
			return p.getValue(receiver)
		}
	}
	return Undefined
}

func (o *Object) setIdx(i int64, v Value) { o.SetOrThrow(indexKey64(i), v) }

func (vm *VM) setLength(o *Object, n int64) { o.SetOrThrow(StrKey("length"), Number(float64(n))) }

// ArraySpeciesCreate (10.4.2.3).
func (vm *VM) ArraySpeciesCreate(original *Object, length int64) *Object {
	if !vm.IsArray(ObjectValue(original)) {
		return vm.arrayCreate(length, nil)
	}
	c := original.Get(constructorKey, ObjectValue(original))
	if co := c.AsObject(); co != nil && co.IsConstructor() {
		if realm := vm.functionRealm(co); realm != vm.realm && co == realm.ArrayConstructor {
			c = Undefined
		}
	}
	if co := c.AsObject(); co != nil {
		c = co.Get(SymKey(SymbolSpecies), c)
		if c.IsNull() {
			c = Undefined
		}
	}
	if c.IsUndefined() {
		return vm.arrayCreate(length, nil)
	}
	if !c.IsConstructor() {
		vm.throwTypeError("Array species is not a constructor")
	}
	return vm.Construct(c.ObjectRef(), []Value{Number(float64(length))}, nil).AsObject()
}

var joinKey = StrKey("join")

func (r *Realm) initArray() {
	vm := r.vm
	proto := r.newArrayWithProto(nil, r.ObjectPrototype)
	r.ArrayPrototype = proto
	c := r.constructor("Array", 1, func(c *Call) Value {
		newTarget := c.NewTarget
		if newTarget == nil {
			newTarget = c.Callee
		}
		proto := vm.GetPrototypeFromConstructor(newTarget, func(r *Realm) *Object { return r.ArrayPrototype })
		switch len(c.Args) {
		case 0:
			return ObjectValue(vm.arrayCreate(0, proto))
		case 1:
			l := c.Args[0]
			if !l.IsNumber() {
				a := vm.arrayCreate(0, proto)
				a.CreateDataPropertyOrThrow(IndexKey(0), l)
				return ObjectValue(a)
			}
			n := uint32(l.Float())
			if float64(n) != l.Float() {
				vm.throwRangeError("Array size is not a small enough positive integer.")
			}
			return ObjectValue(vm.arrayWithHoles(n, proto))
		}
		return ObjectValue(c.Realm().newArrayWithProto(copyArgs(c.Args), proto))
	}, proto)
	r.ArrayConstructor = c
	r.speciesGetter(c)
	r.arraySpecies = c.props.vals[c.props.find(SymKey(SymbolSpecies))]

	r.method(c, "isArray", 1, func(c *Call) Value { return Bool(vm.IsArray(c.Arg(0))) })
	r.method(c, "of", 0, func(c *Call) Value {
		var a *Object
		if c.This.IsConstructor() {
			a = vm.Construct(c.This.ObjectRef(), []Value{Int(len(c.Args))}, nil).AsObject()
		} else {
			a = vm.arrayCreate(int64(len(c.Args)), nil)
		}
		for i, v := range c.Args {
			a.CreateDataPropertyOrThrow(IndexKey(uint32(i)), v)
		}
		vm.setLength(a, int64(len(c.Args)))
		return ObjectValue(a)
	})
	r.method(c, "from", 1, func(c *Call) Value {
		items, mapfn, thisArg := c.Arg(0), c.Arg(1), c.Arg(2)
		mapping := !mapfn.IsUndefined()
		if mapping && !mapfn.IsCallable() {
			vm.throwTypeError("Array.from: the mapper must be a function")
		}
		usingIterator := vm.GetMethod(items, SymKey(SymbolIterator))
		if !usingIterator.IsUndefined() {
			var a *Object
			if c.This.IsConstructor() {
				a = vm.Construct(c.This.ObjectRef(), nil, nil).AsObject()
			} else {
				a = vm.arrayCreate(0, nil)
			}
			rec := vm.getIteratorFromMethod(items, usingIterator)
			k := int64(0)
			vm.iterateRecord(rec, func(v Value) bool {
				if k >= maxSafeInteger {
					vm.throwTypeError("Array.from: too many items")
				}
				if mapping {
					v = vm.Call(mapfn, thisArg, v, Number(float64(k)))
				}
				a.CreateDataPropertyOrThrow(indexKey64(k), v)
				k++
				return true
			})
			vm.setLength(a, k)
			return ObjectValue(a)
		}
		arrayLike := vm.ToObject(items)
		n := vm.LengthOfArrayLike(arrayLike)
		var a *Object
		if c.This.IsConstructor() {
			a = vm.Construct(c.This.ObjectRef(), []Value{Number(float64(n))}, nil).AsObject()
		} else {
			a = vm.arrayCreate(n, nil)
		}
		for k := int64(0); k < n; k++ {
			v := arrayLike.getIdx(k)
			if mapping {
				v = vm.Call(mapfn, thisArg, v, Number(float64(k)))
			}
			a.CreateDataPropertyOrThrow(indexKey64(k), v)
		}
		vm.setLength(a, n)
		return ObjectValue(a)
	})
	r.method(c, "fromAsync", 1, func(c *Call) Value { return vm.arrayFromAsync(c) })

	// Array.prototype
	r.method(proto, "at", 1, func(c *Call) Value {
		o := vm.ToObject(c.This)
		n := vm.LengthOfArrayLike(o)
		rel := vm.ToIntegerOrInfinity(c.Arg(0))
		k := rel
		if rel < 0 {
			k = float64(n) + rel
		}
		if k < 0 || k >= float64(n) {
			return Undefined
		}
		return o.getIdx(int64(k))
	})
	r.method(proto, "concat", 1, func(c *Call) Value {
		o := vm.ToObject(c.This)
		a := vm.ArraySpeciesCreate(o, 0)
		n := int64(0)
		items := append([]Value{ObjectValue(o)}, c.Args...)
		for _, e := range items {
			if vm.isConcatSpreadable(e) {
				eo := e.ObjectRef()
				length := vm.LengthOfArrayLike(eo)
				if n+length > maxSafeInteger {
					vm.throwTypeError("Array.prototype.concat result is too long")
				}
				for k := int64(0); k < length; k++ {
					if eo.hasIndex(k) {
						a.CreateDataPropertyOrThrow(indexKey64(n), eo.getIdx(k))
					}
					n++
				}
			} else {
				if n >= maxSafeInteger {
					vm.throwTypeError("Array.prototype.concat result is too long")
				}
				a.CreateDataPropertyOrThrow(indexKey64(n), e)
				n++
			}
		}
		vm.setLength(a, n)
		return ObjectValue(a)
	})
	r.method(proto, "copyWithin", 2, func(c *Call) Value {
		o := vm.ToObject(c.This)
		n := vm.LengthOfArrayLike(o)
		to := vm.relativeIndex(c.Arg(0), n, 0)
		from := vm.relativeIndex(c.Arg(1), n, 0)
		final := vm.relativeIndex(c.Arg(2), n, n)
		count := min(final-from, n-to)
		dir := int64(1)
		if from < to && to < from+count {
			dir = -1
			from += count - 1
			to += count - 1
		}
		for ; count > 0; count-- {
			if o.hasIndex(from) {
				o.setIdx(to, o.getIdx(from))
			} else {
				o.DeletePropertyOrThrow(indexKey64(to))
			}
			from += dir
			to += dir
		}
		return ObjectValue(o)
	})
	r.method(proto, "entries", 0, func(c *Call) Value {
		return ObjectValue(c.Realm().newArrayIterator(vm.ToObject(c.This), iterateEntries))
	})
	r.method(proto, "keys", 0, func(c *Call) Value {
		return ObjectValue(c.Realm().newArrayIterator(vm.ToObject(c.This), iterateKeys))
	})
	r.arrayProtoValues = r.method(proto, "values", 0, func(c *Call) Value {
		return ObjectValue(c.Realm().newArrayIterator(vm.ToObject(c.This), iterateValues))
	})
	proto.putDirect(SymKey(SymbolIterator), ObjectValue(r.arrayProtoValues), BuiltinFlags)

	// Callback-based iteration methods.
	r.method(proto, "every", 1, func(c *Call) Value {
		o := vm.ToObject(c.This)
		n := vm.LengthOfArrayLike(o)
		f := vm.callbackArg(c, "every")
		for k := int64(0); k < n; k++ {
			if o.hasIndex(k) {
				if !vm.Call(f, c.Arg(1), o.getIdx(k), Number(float64(k)), ObjectValue(o)).ToBoolean() {
					return False
				}
			}
		}
		return True
	})
	r.method(proto, "some", 1, func(c *Call) Value {
		o := vm.ToObject(c.This)
		n := vm.LengthOfArrayLike(o)
		f := vm.callbackArg(c, "some")
		for k := int64(0); k < n; k++ {
			if o.hasIndex(k) {
				if vm.Call(f, c.Arg(1), o.getIdx(k), Number(float64(k)), ObjectValue(o)).ToBoolean() {
					return True
				}
			}
		}
		return False
	})
	r.method(proto, "forEach", 1, func(c *Call) Value {
		o := vm.ToObject(c.This)
		n := vm.LengthOfArrayLike(o)
		f := vm.callbackArg(c, "forEach")
		for k := int64(0); k < n; k++ {
			if o.hasIndex(k) {
				vm.Call(f, c.Arg(1), o.getIdx(k), Number(float64(k)), ObjectValue(o))
			}
		}
		return Undefined
	})
	r.method(proto, "map", 1, func(c *Call) Value {
		o := vm.ToObject(c.This)
		n := vm.LengthOfArrayLike(o)
		f := vm.callbackArg(c, "map")
		a := vm.ArraySpeciesCreate(o, n)
		for k := int64(0); k < n; k++ {
			if o.hasIndex(k) {
				v := vm.Call(f, c.Arg(1), o.getIdx(k), Number(float64(k)), ObjectValue(o))
				a.CreateDataPropertyOrThrow(indexKey64(k), v)
			}
		}
		return ObjectValue(a)
	})
	r.method(proto, "filter", 1, func(c *Call) Value {
		o := vm.ToObject(c.This)
		n := vm.LengthOfArrayLike(o)
		f := vm.callbackArg(c, "filter")
		a := vm.ArraySpeciesCreate(o, 0)
		to := int64(0)
		for k := int64(0); k < n; k++ {
			if o.hasIndex(k) {
				v := o.getIdx(k)
				if vm.Call(f, c.Arg(1), v, Number(float64(k)), ObjectValue(o)).ToBoolean() {
					a.CreateDataPropertyOrThrow(indexKey64(to), v)
					to++
				}
			}
		}
		return ObjectValue(a)
	})
	findMethod := func(name string, fromEnd bool, wantIndex bool) {
		r.method(proto, name, 1, func(c *Call) Value {
			o := vm.ToObject(c.This)
			n := vm.LengthOfArrayLike(o)
			f := vm.callbackArg(c, name)
			for i := int64(0); i < n; i++ {
				k := i
				if fromEnd {
					k = n - 1 - i
				}
				v := o.getIdx(k)
				if vm.Call(f, c.Arg(1), v, Number(float64(k)), ObjectValue(o)).ToBoolean() {
					if wantIndex {
						return Number(float64(k))
					}
					return v
				}
			}
			if wantIndex {
				return Int(-1)
			}
			return Undefined
		})
	}
	findMethod("find", false, false)
	findMethod("findIndex", false, true)
	findMethod("findLast", true, false)
	findMethod("findLastIndex", true, true)
	reduce := func(name string, right bool) {
		r.method(proto, name, 1, func(c *Call) Value {
			o := vm.ToObject(c.This)
			n := vm.LengthOfArrayLike(o)
			f := vm.callbackArg(c, name)
			k, step, end := int64(0), int64(1), n
			if right {
				k, step, end = n-1, -1, -1
			}
			var acc Value
			if len(c.Args) >= 2 {
				acc = c.Args[1]
			} else {
				found := false
				for ; k != end; k += step {
					if o.hasIndex(k) {
						acc = o.getIdx(k)
						found = true
						k += step
						break
					}
				}
				if !found {
					vm.throwTypeError("Reduce of empty array with no initial value")
				}
			}
			for ; k != end; k += step {
				if o.hasIndex(k) {
					acc = vm.Call(f, Undefined, acc, o.getIdx(k), Number(float64(k)), ObjectValue(o))
				}
			}
			return acc
		})
	}
	reduce("reduce", false)
	reduce("reduceRight", true)

	r.method(proto, "fill", 1, func(c *Call) Value {
		o := vm.ToObject(c.This)
		n := vm.LengthOfArrayLike(o)
		k := vm.relativeIndex(c.Arg(1), n, 0)
		final := vm.relativeIndex(c.Arg(2), n, n)
		for ; k < final; k++ {
			o.setIdx(k, c.Arg(0))
		}
		return ObjectValue(o)
	})
	r.method(proto, "flat", 0, func(c *Call) Value {
		o := vm.ToObject(c.This)
		n := vm.LengthOfArrayLike(o)
		depth := 1.0
		if d := c.Arg(0); !d.IsUndefined() {
			depth = vm.ToIntegerOrInfinity(d)
			if depth < 0 {
				depth = 0
			}
		}
		a := vm.ArraySpeciesCreate(o, 0)
		vm.flattenIntoArray(a, o, n, 0, depth, Undefined, Undefined)
		return ObjectValue(a)
	})
	r.method(proto, "flatMap", 1, func(c *Call) Value {
		o := vm.ToObject(c.This)
		n := vm.LengthOfArrayLike(o)
		f := vm.callbackArg(c, "flatMap")
		a := vm.ArraySpeciesCreate(o, 0)
		vm.flattenIntoArray(a, o, n, 0, 1, f, c.Arg(1))
		return ObjectValue(a)
	})
	r.method(proto, "includes", 1, func(c *Call) Value {
		o := vm.ToObject(c.This)
		n := vm.LengthOfArrayLike(o)
		if n == 0 {
			return False
		}
		k := vm.relativeIndex(c.Arg(1), n, 0)
		for ; k < n; k++ {
			if SameValueZero(o.getIdx(k), c.Arg(0)) {
				return True
			}
		}
		return False
	})
	setFast(r.method(proto, "indexOf", 1, func(c *Call) Value {
		o := vm.ToObject(c.This)
		n := vm.LengthOfArrayLike(o)
		if n == 0 {
			return Int(-1)
		}
		k := vm.relativeIndex(c.Arg(1), n, 0)
		for ; k < n; k++ {
			if o.hasIndex(k) && StrictEquals(o.getIdx(k), c.Arg(0)) {
				return Number(float64(k))
			}
		}
		return Int(-1)
	}), fastArrayIndexOf)
	r.method(proto, "lastIndexOf", 1, func(c *Call) Value {
		o := vm.ToObject(c.This)
		n := vm.LengthOfArrayLike(o)
		if n == 0 {
			return Int(-1)
		}
		k := n - 1
		if len(c.Args) > 1 {
			rel := vm.ToIntegerOrInfinity(c.Args[1])
			if rel >= 0 {
				k = int64(math.Min(rel, float64(n-1)))
			} else {
				k = int64(float64(n) + rel)
			}
		}
		for ; k >= 0; k-- {
			if o.hasIndex(k) && StrictEquals(o.getIdx(k), c.Arg(0)) {
				return Number(float64(k))
			}
		}
		return Int(-1)
	})
	r.method(proto, "join", 1, func(c *Call) Value {
		o := vm.ToObject(c.This)
		return StringValue(vm.arrayJoin(o, c.Arg(0)))
	})
	r.method(proto, "toString", 0, func(c *Call) Value {
		o := vm.ToObject(c.This)
		join := o.Get(joinKey, ObjectValue(o))
		if !join.IsCallable() {
			return StringValue(vm.objectToString(ObjectValue(o)))
		}
		return vm.Call(join, ObjectValue(o))
	})
	r.method(proto, "toLocaleString", 0, func(c *Call) Value {
		o := vm.ToObject(c.This)
		if vm.enterJoinCycle(o) {
			return StringValue(emptyString)
		}
		defer vm.leaveJoinCycle(o)
		n := vm.LengthOfArrayLike(o)
		var b stringBuilder
		for k := int64(0); k < n; k++ {
			if k > 0 {
				b.WriteUnit(',')
			}
			e := o.getIdx(k)
			if !e.IsNullish() {
				b.WriteString(vm.ToString(vm.Invoke(e, StrKey("toLocaleString"))))
			}
		}
		return StringValue(b.String())
	})
	r.method(proto, "pop", 0, func(c *Call) Value {
		o := vm.ToObject(c.This)
		if a := o.denseArray(); a != nil && a.length > 0 {
			n := len(o.elements) - 1
			v := o.elements[n]
			o.elements[n] = Undefined // drop the reference
			o.elements = o.elements[:n]
			a.length--
			if v.IsEmpty() {
				return Undefined
			}
			return v
		}
		n := vm.LengthOfArrayLike(o)
		if n == 0 {
			vm.setLength(o, 0)
			return Undefined
		}
		v := o.getIdx(n - 1)
		o.DeletePropertyOrThrow(indexKey64(n - 1))
		vm.setLength(o, n-1)
		return v
	})
	r.method(proto, "push", 1, func(c *Call) Value {
		o := vm.ToObject(c.This)
		if a := o.array(); a != nil && o.class == ClassArray {
			ok := true
			for _, v := range c.Args {
				if !a.setIndexFast(o, a.length, v) {
					ok = false
					break
				}
			}
			if ok && (len(c.Args) > 0 || a.lengthWritable) {
				return Number(float64(a.length))
			}
			// Fall back for the rest (only reached before any store).
		}
		n := vm.LengthOfArrayLike(o)
		if n+int64(len(c.Args)) > maxSafeInteger {
			vm.throwTypeError("Array.prototype.push result is too long")
		}
		for _, v := range c.Args {
			o.setIdx(n, v)
			n++
		}
		vm.setLength(o, n)
		return Number(float64(n))
	})
	r.method(proto, "reverse", 0, func(c *Call) Value {
		o := vm.ToObject(c.This)
		n := vm.LengthOfArrayLike(o)
		for lower := int64(0); lower < n/2; lower++ {
			upper := n - 1 - lower
			lowerExists := o.hasIndex(lower)
			var lowerValue, upperValue Value
			if lowerExists {
				lowerValue = o.getIdx(lower)
			}
			upperExists := o.hasIndex(upper)
			if upperExists {
				upperValue = o.getIdx(upper)
			}
			switch {
			case lowerExists && upperExists:
				o.setIdx(lower, upperValue)
				o.setIdx(upper, lowerValue)
			case upperExists:
				o.setIdx(lower, upperValue)
				o.DeletePropertyOrThrow(indexKey64(upper))
			case lowerExists:
				o.DeletePropertyOrThrow(indexKey64(lower))
				o.setIdx(upper, lowerValue)
			}
		}
		return ObjectValue(o)
	})
	r.method(proto, "shift", 0, func(c *Call) Value {
		o := vm.ToObject(c.This)
		if a := o.denseArray(); a != nil && a.length > 0 {
			// Moving every element down one (holes included, as the spec's
			// deletes do) is dropping the first: O(1) like JSC's index bias.
			v := o.elements[0]
			o.elements[0] = Undefined // drop the reference
			o.elements = o.elements[1:]
			a.length--
			if v.IsEmpty() {
				return Undefined
			}
			return v
		}
		n := vm.LengthOfArrayLike(o)
		if n == 0 {
			vm.setLength(o, 0)
			return Undefined
		}
		first := o.getIdx(0)
		for k := int64(1); k < n; k++ {
			if o.hasIndex(k) {
				o.setIdx(k-1, o.getIdx(k))
			} else {
				o.DeletePropertyOrThrow(indexKey64(k - 1))
			}
		}
		o.DeletePropertyOrThrow(indexKey64(n - 1))
		vm.setLength(o, n-1)
		return first
	})
	r.method(proto, "unshift", 1, func(c *Call) Value {
		o := vm.ToObject(c.This)
		if a := o.denseArray(); a != nil && o.extensible && int64(a.length)+int64(len(c.Args)) < math.MaxUint32 {
			o.elements = slices.Insert(o.elements, 0, c.Args...)
			a.length += uint32(len(c.Args))
			return Number(float64(a.length))
		}
		n := vm.LengthOfArrayLike(o)
		argc := int64(len(c.Args))
		if argc > 0 {
			if n+argc > maxSafeInteger {
				vm.throwTypeError("Array.prototype.unshift result is too long")
			}
			for k := n; k > 0; k-- {
				from, to := k-1, k+argc-1
				if o.hasIndex(from) {
					o.setIdx(to, o.getIdx(from))
				} else {
					o.DeletePropertyOrThrow(indexKey64(to))
				}
			}
			for j, v := range c.Args {
				o.setIdx(int64(j), v)
			}
		}
		vm.setLength(o, n+argc)
		return Number(float64(n + argc))
	})
	setFast(r.method(proto, "slice", 2, func(c *Call) Value {
		o := vm.ToObject(c.This)
		n := vm.LengthOfArrayLike(o)
		k := vm.relativeIndex(c.Arg(0), n, 0)
		final := vm.relativeIndex(c.Arg(1), n, n)
		count := max(final-k, 0)
		a := vm.ArraySpeciesCreate(o, count)
		i := int64(0)
		for ; k < final; k++ {
			if o.hasIndex(k) {
				a.CreateDataPropertyOrThrow(indexKey64(i), o.getIdx(k))
			}
			i++
		}
		vm.setLength(a, i)
		return ObjectValue(a)
	}), fastArraySlice)
	r.method(proto, "splice", 2, func(c *Call) Value {
		o := vm.ToObject(c.This)
		n := vm.LengthOfArrayLike(o)
		start := vm.relativeIndex(c.Arg(0), n, 0)
		var insert []Value
		var deleteCount int64
		switch len(c.Args) {
		case 0:
		case 1:
			deleteCount = n - start
		default:
			dc := vm.ToIntegerOrInfinity(c.Args[1])
			deleteCount = int64(math.Min(math.Max(dc, 0), float64(n-start)))
			insert = c.Args[2:]
		}
		itemCount := int64(len(insert))
		if n+itemCount-deleteCount > maxSafeInteger {
			vm.throwTypeError("Array.prototype.splice result is too long")
		}
		a := vm.ArraySpeciesCreate(o, deleteCount)
		for k := int64(0); k < deleteCount; k++ {
			if o.hasIndex(start + k) {
				a.CreateDataPropertyOrThrow(indexKey64(k), o.getIdx(start+k))
			}
		}
		vm.setLength(a, deleteCount)
		if itemCount < deleteCount {
			for k := start; k < n-deleteCount; k++ {
				from, to := k+deleteCount, k+itemCount
				if o.hasIndex(from) {
					o.setIdx(to, o.getIdx(from))
				} else {
					o.DeletePropertyOrThrow(indexKey64(to))
				}
			}
			for k := n; k > n-deleteCount+itemCount; k-- {
				o.DeletePropertyOrThrow(indexKey64(k - 1))
			}
		} else if itemCount > deleteCount {
			for k := n - deleteCount; k > start; k-- {
				from, to := k+deleteCount-1, k+itemCount-1
				if o.hasIndex(from) {
					o.setIdx(to, o.getIdx(from))
				} else {
					o.DeletePropertyOrThrow(indexKey64(to))
				}
			}
		}
		for i, v := range insert {
			o.setIdx(start+int64(i), v)
		}
		vm.setLength(o, n-deleteCount+itemCount)
		return ObjectValue(a)
	})
	r.method(proto, "sort", 1, func(c *Call) Value {
		cmp := c.Arg(0)
		if !cmp.IsUndefined() && !cmp.IsCallable() {
			vm.throwTypeError("Array.prototype.sort requires the comparator argument to be a function or undefined")
		}
		o := vm.ToObject(c.This)
		n := vm.LengthOfArrayLike(o)
		items := vm.sortIndexedProperties(o, n, cmp, true)
		i := int64(0)
		for ; i < int64(len(items)); i++ {
			o.setIdx(i, items[i])
		}
		for ; i < n; i++ {
			o.DeletePropertyOrThrow(indexKey64(i))
		}
		return ObjectValue(o)
	})
	r.method(proto, "toSorted", 1, func(c *Call) Value {
		cmp := c.Arg(0)
		if !cmp.IsUndefined() && !cmp.IsCallable() {
			vm.throwTypeError("Array.prototype.toSorted requires the comparator argument to be a function or undefined")
		}
		o := vm.ToObject(c.This)
		n := vm.LengthOfArrayLike(o)
		a := vm.arrayCreate(n, nil)
		items := vm.sortIndexedProperties(o, n, cmp, false)
		for i, v := range items {
			a.CreateDataPropertyOrThrow(indexKey64(int64(i)), v)
		}
		return ObjectValue(a)
	})
	r.method(proto, "toReversed", 0, func(c *Call) Value {
		o := vm.ToObject(c.This)
		n := vm.LengthOfArrayLike(o)
		a := vm.arrayCreate(n, nil)
		for k := int64(0); k < n; k++ {
			a.CreateDataPropertyOrThrow(indexKey64(k), o.getIdx(n-1-k))
		}
		return ObjectValue(a)
	})
	r.method(proto, "toSpliced", 2, func(c *Call) Value {
		o := vm.ToObject(c.This)
		n := vm.LengthOfArrayLike(o)
		start := vm.relativeIndex(c.Arg(0), n, 0)
		var insert []Value
		var skip int64
		switch len(c.Args) {
		case 0:
		case 1:
			skip = n - start
		default:
			dc := vm.ToIntegerOrInfinity(c.Args[1])
			skip = int64(math.Min(math.Max(dc, 0), float64(n-start)))
			insert = c.Args[2:]
		}
		newLen := n + int64(len(insert)) - skip
		if newLen > maxSafeInteger {
			vm.throwTypeError("Array.prototype.toSpliced result is too long")
		}
		a := vm.arrayCreate(newLen, nil)
		i := int64(0)
		for ; i < start; i++ {
			a.CreateDataPropertyOrThrow(indexKey64(i), o.getIdx(i))
		}
		for _, v := range insert {
			a.CreateDataPropertyOrThrow(indexKey64(i), v)
			i++
		}
		for r := start + skip; i < newLen; r++ {
			a.CreateDataPropertyOrThrow(indexKey64(i), o.getIdx(r))
			i++
		}
		return ObjectValue(a)
	})
	r.method(proto, "with", 2, func(c *Call) Value {
		o := vm.ToObject(c.This)
		n := vm.LengthOfArrayLike(o)
		rel := vm.ToIntegerOrInfinity(c.Arg(0))
		actual := rel
		if rel < 0 {
			actual = float64(n) + rel
		}
		if actual >= float64(n) || actual < 0 {
			vm.throwRangeError("Array.prototype.with index out of range")
		}
		a := vm.arrayCreate(n, nil)
		for k := int64(0); k < n; k++ {
			v := c.Arg(1)
			if k != int64(actual) {
				v = o.getIdx(k)
			}
			a.CreateDataPropertyOrThrow(indexKey64(k), v)
		}
		return ObjectValue(a)
	})

	unscopables := r.newObject(nil)
	for _, name := range []string{"at", "copyWithin", "entries", "fill", "find", "findIndex", "findLast",
		"findLastIndex", "flat", "flatMap", "includes", "keys", "toReversed", "toSorted", "toSpliced", "values"} {
		unscopables.putDirect(StrKey(name), True, DefaultFlags)
	}
	proto.putDirect(SymKey(SymbolUnscopables), ObjectValue(unscopables), Configurable)
}

func (vm *VM) isConcatSpreadable(v Value) bool {
	o := v.AsObject()
	if o == nil {
		return false
	}
	s := o.Get(SymKey(SymbolIsConcatSpreadable), v)
	if !s.IsUndefined() {
		return s.ToBoolean()
	}
	return vm.IsArray(v)
}

// flattenIntoArray (23.1.3.13.1).
func (vm *VM) flattenIntoArray(target, source *Object, sourceLen, start int64, depth float64, mapper, thisArg Value) int64 {
	targetIndex := start
	for k := int64(0); k < sourceLen; k++ {
		if !source.hasIndex(k) {
			continue
		}
		e := source.getIdx(k)
		if !mapper.IsUndefined() {
			e = vm.Call(mapper, thisArg, e, Number(float64(k)), ObjectValue(source))
		}
		if depth > 0 && vm.IsArray(e) {
			eo := e.ObjectRef()
			targetIndex = vm.flattenIntoArray(target, eo, vm.LengthOfArrayLike(eo), targetIndex, depth-1, Undefined, Undefined)
			continue
		}
		if targetIndex >= maxSafeInteger {
			vm.throwTypeError("flattened array is too long")
		}
		target.CreateDataPropertyOrThrow(indexKey64(targetIndex), e)
		targetIndex++
	}
	return targetIndex
}

// joinStack guards against cyclic joins (like JSC's JSStringJoiner cycle check).
func (vm *VM) enterJoinCycle(o *Object) bool {
	if slices.Contains(vm.joinStack, o) {
		return true
	}
	vm.joinStack = append(vm.joinStack, o)
	return false
}

func (vm *VM) leaveJoinCycle(o *Object) { vm.joinStack = vm.joinStack[:len(vm.joinStack)-1] }

func (vm *VM) arrayJoin(o *Object, separator Value) *String {
	n := vm.LengthOfArrayLike(o)
	sep := NewString(",")
	if !separator.IsUndefined() {
		sep = vm.ToString(separator)
	}
	if vm.enterJoinCycle(o) {
		return emptyString
	}
	defer vm.leaveJoinCycle(o)
	var b stringBuilder
	for k := int64(0); k < n; k++ {
		if k > 0 {
			b.WriteString(sep)
		}
		e := o.getIdx(k)
		if !e.IsNullish() {
			b.WriteString(vm.ToString(e))
		}
	}
	return b.String()
}

// sortIndexedProperties (23.1.3.30.1) returns the sorted values.
func (vm *VM) sortIndexedProperties(o *Object, n int64, cmp Value, skipHoles bool) []Value {
	var items []Value
	for k := int64(0); k < n; k++ {
		if skipHoles {
			if o.hasIndex(k) {
				items = append(items, o.getIdx(k))
			}
		} else {
			items = append(items, o.getIdx(k))
		}
	}
	vm.sortValues(items, func(x, y Value) int { return vm.compareArrayElements(x, y, cmp) })
	return items
}

// sortValues is a stable merge sort that tolerates inconsistent comparators.
func (vm *VM) sortValues(items []Value, less func(x, y Value) int) {
	if len(items) < 2 {
		return
	}
	tmp := make([]Value, len(items))
	var sortRange func(lo, hi int)
	sortRange = func(lo, hi int) {
		if hi-lo < 2 {
			return
		}
		if hi-lo <= 8 {
			for i := lo + 1; i < hi; i++ {
				for j := i; j > lo && less(items[j-1], items[j]) > 0; j-- {
					items[j-1], items[j] = items[j], items[j-1]
				}
			}
			return
		}
		mid := (lo + hi) / 2
		sortRange(lo, mid)
		sortRange(mid, hi)
		i, j, k := lo, mid, lo
		for i < mid && j < hi {
			if less(items[i], items[j]) <= 0 {
				tmp[k] = items[i]
				i++
			} else {
				tmp[k] = items[j]
				j++
			}
			k++
		}
		for ; i < mid; i++ {
			tmp[k] = items[i]
			k++
		}
		for ; j < hi; j++ {
			tmp[k] = items[j]
			k++
		}
		copy(items[lo:hi], tmp[lo:hi])
	}
	sortRange(0, len(items))
}

// compareArrayElements (23.1.3.30.2).
func (vm *VM) compareArrayElements(x, y Value, cmp Value) int {
	switch {
	case x.IsUndefined() && y.IsUndefined():
		return 0
	case x.IsUndefined():
		return 1
	case y.IsUndefined():
		return -1
	}
	if !cmp.IsUndefined() {
		v := vm.ToNumber(vm.Call(cmp, Undefined, x, y))
		switch {
		case v < 0:
			return -1
		case v > 0:
			return 1
		}
		return 0
	}
	if x.IsString() && y.IsString() {
		return x.StringRef().Compare(y.StringRef())
	}
	return vm.ToString(x).Compare(vm.ToString(y))
}
