package vm

// The iteration protocol (ECMA-262 7.4) plus %IteratorPrototype%,
// %AsyncIteratorPrototype%, array iterators and the Iterator helpers.

// iteratorRecord is an Iterator Record.
type iteratorRecord struct {
	iterator *Object
	next     Value
	done     bool
}

// getIterator is GetIterator(obj, sync).
func (vm *VM) getIterator(v Value) *iteratorRecord {
	method := vm.GetMethod(v, SymKey(SymbolIterator))
	if method.IsUndefined() {
		vm.throwTypeError("%s is not iterable", vm.describeForError(v))
	}
	return vm.getIteratorFromMethod(v, method)
}

func (vm *VM) getIteratorFromMethod(v Value, method Value) *iteratorRecord {
	it := vm.Call(method, v)
	o := it.AsObject()
	if o == nil {
		vm.throwTypeError("Iterator result interface is not an object.")
	}
	return &iteratorRecord{iterator: o, next: o.GetKey(nextKey)}
}

// getIteratorDirect is GetIteratorDirect (7.4.4).
func (vm *VM) getIteratorDirect(o *Object) *iteratorRecord {
	return &iteratorRecord{iterator: o, next: o.GetKey(nextKey)}
}

// step is IteratorStepValue: it returns the next value, or ok == false
// when the iterator is done. Errors mark the record done.
func (vm *VM) step(rec *iteratorRecord) (v Value, ok bool) {
	completed := false
	defer func() {
		if !completed {
			rec.done = true
		}
	}()
	result := vm.Call(rec.next, ObjectValue(rec.iterator))
	ro := result.AsObject()
	if ro == nil {
		vm.throwTypeError("Iterator result interface is not an object.")
	}
	if ro.GetKey(doneKey).ToBoolean() {
		rec.done = true
		completed = true
		return Undefined, false
	}
	v = ro.GetKey(valueKey)
	completed = true
	return v, true
}

// closeIterator is IteratorClose for a normal completion.
func (vm *VM) closeIterator(rec *iteratorRecord) {
	ret := vm.GetMethod(ObjectValue(rec.iterator), StrKey("return"))
	if ret.IsUndefined() {
		return
	}
	result := vm.Call(ret, ObjectValue(rec.iterator))
	if !result.IsObject() {
		vm.throwTypeError("Iterator result interface is not an object.")
	}
}

// closeIteratorOnThrow is IteratorClose for a throw completion: errors from
// return() are ignored in favor of the original exception.
func (vm *VM) closeIteratorOnThrow(rec *iteratorRecord) {
	vm.Try(func() {
		ret := vm.GetMethod(ObjectValue(rec.iterator), StrKey("return"))
		if !ret.IsUndefined() {
			vm.Call(ret, ObjectValue(rec.iterator))
		}
	})
}

// iterateRecord calls body for each value; it closes the iterator if body
// throws or returns false.
func (vm *VM) iterateRecord(rec *iteratorRecord, body func(Value) bool) {
	for {
		v, ok := vm.step(rec)
		if !ok {
			return
		}
		cont := vm.callClosingOnThrow(rec, func() bool { return body(v) })
		if !cont {
			vm.closeIterator(rec)
			return
		}
	}
}

// callClosingOnThrow runs f, closing the iterator if f throws.
func (vm *VM) callClosingOnThrow(rec *iteratorRecord, f func() bool) bool {
	ok := false
	mark := vm.jitMark()
	defer func() {
		if !ok {
			if r := recover(); r != nil {
				vm.jitUnwindTo(mark)
				if _, isExc := r.(*Exception); isExc {
					vm.closeIteratorOnThrow(rec)
				}
				panic(r)
			}
		}
	}()
	result := f()
	ok = true
	return result
}

// iterate iterates an iterable, closing the iterator on abrupt exits.
func (vm *VM) iterate(iterable Value, body func(Value) bool) {
	vm.iterateRecord(vm.getIterator(iterable), body)
}

func (vm *VM) iterateWithClose(iterable Value, body func(Value) bool) { vm.iterate(iterable, body) }

// iterableToList is IteratorToList(GetIterator(v)).
func (vm *VM) iterableToList(v Value) []Value {
	if o := v.AsObject(); o != nil && o.class == ClassArray && vm.hasPristineArrayIteration(o) {
		a := o.array()
		list := make([]Value, a.length)
		for i := range list {
			list[i] = o.getIndex(uint32(i))
		}
		return list
	}
	rec := vm.getIterator(v)
	var list []Value
	for {
		x, ok := vm.step(rec)
		if !ok {
			return list
		}
		list = append(list, x)
	}
}

// hasPristineArrayIteration reports whether iterating o is unobservable:
// an ordinary array whose iteration protocol is untouched.
// fastArrayIterable reports whether iterator_open may step through
// iterable, an array, by index instead of calling its iterator (JSC's
// IterationMode::FastArray): iterable[Symbol.iterator] (symbolIterator) is
// the realm's Array.prototype.values and %ArrayIteratorPrototype%.next is
// the built-in (JSC's array iterator protocol watchpoint). The iterator
// register then holds r.fastArraySentinel and next holds the index.
func fastArrayIterable(r *Realm, iterable, symbolIterator Value) bool {
	o := iterable.AsObject()
	if o == nil || r.arrayProtoValues == nil || symbolIterator.AsObject() != r.arrayProtoValues {
		return false
	}
	if _, ok := o.exotic.(*arrayObject); !ok {
		return false
	}
	p, ok := r.ArrayIteratorPrototype.ordinaryGetOwnProperty(nextKey)
	return ok && !p.IsAccessor() && p.Value.AsObject() == r.arrayIteratorNext
}

// fastArrayNext is iterator_next in fast mode: the element at index next
// of array o, as %ArrayIteratorPrototype%.next reads it (Get, so holes
// read through the prototype chain), or done past the length.
func fastArrayNext(o *Object, next Value) (v Value, index Value, done bool) {
	i := next.num()
	if i >= float64(o.array().length) {
		return Undefined, next, true
	}
	k := uint32(i)
	if int64(k) < int64(len(o.elements)) && !o.elements[k].IsEmpty() {
		v = o.elements[k]
	} else {
		v = o.Get(IndexKey(k), ObjectValue(o))
	}
	return v, Int(int(k) + 1), false
}

func (vm *VM) hasPristineArrayIteration(o *Object) bool {
	r := o.realm
	if o.proto != r.ArrayPrototype || len(o.sparse()) > 0 || o.props.len() > 0 {
		return false
	}
	if p, ok := r.ArrayPrototype.ordinaryGetOwnProperty(SymKey(SymbolIterator)); !ok || p.Value.AsObject() != r.arrayProtoValues {
		return false
	}
	if len(r.ArrayPrototype.sparse()) > 0 || len(r.ArrayPrototype.elements) > 0 || len(r.ObjectPrototype.elements) > 0 || len(r.ObjectPrototype.sparse()) > 0 {
		return false
	}
	if p, ok := r.ArrayIteratorPrototype.ordinaryGetOwnProperty(nextKey); !ok || p.Value.AsObject() != r.arrayIteratorNext {
		return false
	}
	return int64(len(o.elements)) == int64(o.array().length) && !hasHoles(o.elements)
}

func hasHoles(v []Value) bool {
	for _, x := range v {
		if x.IsEmpty() {
			return true
		}
	}
	return false
}

// CreateIterResultObject (7.4.14).
func (r *Realm) iterResult(v Value, done bool) Value {
	o := r.NewObject()
	o.props.set(valueKey, dataProperty(v, DefaultFlags))
	o.props.set(doneKey, dataProperty(Bool(done), DefaultFlags))
	return ObjectValue(o)
}

// --- Prototypes ------------------------------------------------------------------------

type arrayIteratorKind uint8

const (
	iterateKeys arrayIteratorKind = iota
	iterateValues
	iterateEntries
)

type arrayIterator struct {
	target *Object // nil when exhausted
	index  int64
	kind   arrayIteratorKind
}

func (r *Realm) newArrayIterator(target *Object, kind arrayIteratorKind) *Object {
	o := r.newObject(r.ArrayIteratorPrototype)
	o.class = ClassIterator
	o.internal = &arrayIterator{target: target, kind: kind}
	return o
}

func (r *Realm) initIterators() {
	vm := r.vm
	r.IteratorPrototype = r.NewObject()
	r.symbolMethod(r.IteratorPrototype, SymbolIterator, 0, func(c *Call) Value { return c.This }, BuiltinFlags)
	r.AsyncIteratorPrototype = r.NewObject()
	r.symbolMethod(r.AsyncIteratorPrototype, SymbolAsyncIterator, 0, func(c *Call) Value { return c.This }, BuiltinFlags)

	r.ArrayIteratorPrototype = r.newObject(r.IteratorPrototype)
	r.arrayIteratorNext = r.method(r.ArrayIteratorPrototype, "next", 0, func(c *Call) Value {
		o := c.This.AsObject()
		var it *arrayIterator
		if o != nil {
			it, _ = o.internal.(*arrayIterator)
		}
		if it == nil {
			vm.throwTypeError("%%ArrayIteratorPrototype%%.next requires that |this| be an Array Iterator instance")
		}
		if it.target == nil {
			return c.Realm().iterResult(Undefined, true)
		}
		var length int64
		if it.target.class == ClassTypedArray {
			ta := it.target.internal.(*typedArray)
			if ta.isOutOfBounds() {
				vm.throwTypeError("Underlying ArrayBuffer has been detached from the view or out-of-bounds")
			}
			length = int64(ta.length())
		} else {
			length = vm.LengthOfArrayLike(it.target)
		}
		if it.index >= length {
			it.target = nil
			return c.Realm().iterResult(Undefined, true)
		}
		i := it.index
		it.index++
		switch it.kind {
		case iterateKeys:
			return c.Realm().iterResult(Number(float64(i)), false)
		case iterateValues:
			return c.Realm().iterResult(it.target.Get(IndexKey(uint32(i)), ObjectValue(it.target)), false)
		}
		v := it.target.Get(IndexKey(uint32(i)), ObjectValue(it.target))
		return c.Realm().iterResult(ObjectValue(c.Realm().NewArray(Number(float64(i)), v)), false)
	})
	r.toStringTag(r.ArrayIteratorPrototype, "Array Iterator")

	r.initIteratorHelpers()
}
