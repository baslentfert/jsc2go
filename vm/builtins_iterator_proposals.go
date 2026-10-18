package vm

import (
	"math"
	"slices"
)

// Iterator proposals: Iterator.prototype.includes, join, chunks and windows
// (iterator-includes, Iterator.prototype.join, iterator-chunking) and
// Iterator.zip / Iterator.zipKeyed (joint-iteration).

func (r *Realm) initIteratorProposals(iterCtor *Object) {
	vm := r.vm
	proto := r.IteratorPrototype

	thisObject := func(c *Call, name string) *Object {
		o := c.This.AsObject()
		if o == nil {
			vm.throwTypeError("Iterator.prototype.%s requires that |this| be an Object", name)
		}
		return o
	}
	// failClosing closes the not yet started iterator o and throws.
	failClosing := func(o *Object, kind ErrorKind, format string, args ...any) {
		vm.closeIteratorOnThrow(&iteratorRecord{iterator: o})
		vm.throwError(kind, format, args...)
	}
	newHelper := func(c *Call, rec *iteratorRecord, next func() (Value, bool)) Value {
		o := c.Realm().newObject(c.Realm().IteratorHelperPrototype)
		o.class = ClassIterator
		o.internal = &iteratorHelper{underlying: rec, next: next}
		return ObjectValue(o)
	}
	// sizeArg validates chunkSize / windowSize: an integral Number in
	// [1, 2^32-1], without coercion.
	sizeArg := func(o *Object, v Value, name string) int {
		if !v.IsNumber() {
			failClosing(o, ErrorKindTypeError, "%s must be a number", name)
		}
		n := v.Float()
		if !isIntegralNumber(v) {
			failClosing(o, ErrorKindTypeError, "%s must be an integer", name)
		}
		if n < 1 || n > math.MaxUint32 {
			failClosing(o, ErrorKindRangeError, "%s must be between 1 and 2^32-1", name)
		}
		return int(n)
	}

	r.method(proto, "includes", 1, func(c *Call) Value {
		o := thisObject(c, "includes")
		search, skipped := c.Arg(0), c.Arg(1)
		toSkip := 0.0
		if !skipped.IsUndefined() {
			if !skipped.IsNumber() {
				failClosing(o, ErrorKindTypeError, "skippedElements must be a number")
			}
			n := skipped.Float()
			switch {
			case math.IsInf(n, 1):
				toSkip = n
			case math.IsNaN(n) || !math.IsInf(n, 0) && n != math.Trunc(n):
				failClosing(o, ErrorKindTypeError, "skippedElements must be an integer")
			case n < 0 || n > maxSafeInteger:
				failClosing(o, ErrorKindRangeError, "skippedElements is out of range")
			default:
				toSkip = n
			}
		}
		rec := vm.getIteratorDirect(o)
		for skippedSoFar := 0.0; ; {
			v, ok := vm.step(rec)
			if !ok {
				return False
			}
			if skippedSoFar < toSkip {
				skippedSoFar++
				continue
			}
			if SameValueZero(v, search) {
				vm.closeIterator(rec)
				return True
			}
		}
	})

	r.method(proto, "join", 1, func(c *Call) Value {
		o := thisObject(c, "join")
		sep := NewString(",")
		if s := c.Arg(0); !s.IsUndefined() {
			vm.callClosingOnThrow(&iteratorRecord{iterator: o}, func() bool {
				sep = vm.ToString(s)
				return true
			})
		}
		rec := vm.getIteratorDirect(o)
		var b stringBuilder
		for first := true; ; first = false {
			v, ok := vm.step(rec)
			if !ok {
				return StringValue(b.String())
			}
			if !first {
				b.WriteString(sep)
			}
			if !v.IsNullish() {
				vm.callClosingOnThrow(rec, func() bool {
					b.WriteString(vm.ToString(v))
					return true
				})
			}
		}
	})

	r.method(proto, "chunks", 1, func(c *Call) Value {
		o := thisObject(c, "chunks")
		size := sizeArg(o, c.Arg(0), "chunkSize")
		realm := c.Realm() // the helper runs after the call: keep no c
		rec := vm.getIteratorDirect(o)
		done := false
		return newHelper(c, rec, func() (Value, bool) {
			if done {
				return Undefined, false
			}
			var buffer []Value
			for len(buffer) < size {
				v, ok := vm.step(rec)
				if !ok {
					done = true
					if len(buffer) == 0 {
						return Undefined, false
					}
					break
				}
				buffer = append(buffer, v)
			}
			return ObjectValue(realm.NewArray(buffer...)), true
		})
	})

	r.method(proto, "windows", 1, func(c *Call) Value {
		o := thisObject(c, "windows")
		size := sizeArg(o, c.Arg(0), "windowSize")
		realm := c.Realm() // the helper runs after the call: keep no c
		allowPartial := false
		switch u := c.Arg(1); {
		case u.IsUndefined():
		case u.IsString() && u.StringRef().String() == "only-full":
		case u.IsString() && u.StringRef().String() == "allow-partial":
			allowPartial = true
		default:
			failClosing(o, ErrorKindTypeError, `undersized must be "only-full" or "allow-partial"`)
		}
		rec := vm.getIteratorDirect(o)
		var buffer []Value
		done := false
		return newHelper(c, rec, func() (Value, bool) {
			for !done {
				v, ok := vm.step(rec)
				if !ok {
					done = true
					if allowPartial && len(buffer) > 0 && len(buffer) < size {
						return ObjectValue(realm.NewArray(buffer...)), true
					}
					break
				}
				if len(buffer) == size {
					buffer = buffer[1:]
				}
				buffer = append(buffer, v)
				if len(buffer) == size {
					return ObjectValue(realm.NewArray(slices.Clone(buffer)...)), true
				}
			}
			return Undefined, false
		})
	})

	r.method(iterCtor, "zip", 1, func(c *Call) Value {
		iterables := c.Arg(0)
		if !iterables.IsObject() {
			vm.throwTypeError("Iterator.zip requires an object")
		}
		mode, paddingOption := vm.zipOptions(c.Arg(1))
		var iters []*iteratorRecord
		input := vm.getIterator(iterables)
		for {
			var next Value
			var more bool
			if exc := vm.Try(func() { next, more = vm.step(input) }); exc != nil {
				vm.closeAll(iters, exc)
			}
			if !more {
				break
			}
			var it *iteratorRecord
			if exc := vm.Try(func() { it = vm.getIteratorFlattenable(next, false) }); exc != nil {
				vm.closeAll(append([]*iteratorRecord{input}, iters...), exc)
			}
			iters = append(iters, it)
		}
		padding := make([]Value, len(iters))
		if mode == "longest" && !paddingOption.IsUndefined() {
			var paddingIter *iteratorRecord
			if exc := vm.Try(func() { paddingIter = vm.getIterator(paddingOption) }); exc != nil {
				vm.closeAll(iters, exc)
			}
			using := true
			for i := range padding {
				var v Value
				if exc := vm.Try(func() { v, using = vm.step(paddingIter) }); exc != nil {
					vm.closeAll(iters, exc)
				}
				if !using {
					break
				}
				padding[i] = v
			}
			if using {
				if exc := vm.Try(func() { vm.closeIterator(paddingIter) }); exc != nil {
					vm.closeAll(iters, exc)
				}
			}
		}
		realm := c.Realm() // the results are made after the call: keep no c
		return vm.iteratorZip(realm, iters, mode, padding, func(results []Value) Value {
			return ObjectValue(realm.NewArray(results...))
		})
	})

	r.method(iterCtor, "zipKeyed", 1, func(c *Call) Value {
		iterables := c.Arg(0).AsObject()
		if iterables == nil {
			vm.throwTypeError("Iterator.zipKeyed requires an object")
		}
		mode, paddingOption := vm.zipOptions(c.Arg(1))
		var iters []*iteratorRecord
		var keys []PropertyKey
		for _, k := range iterables.OwnPropertyKeys() {
			var it *iteratorRecord
			if exc := vm.Try(func() {
				p, ok := iterables.GetOwnProperty(k)
				if !ok || !p.Enumerable() {
					return
				}
				v := iterables.GetKey(k)
				if v.IsUndefined() {
					return
				}
				it = vm.getIteratorFlattenable(v, false)
			}); exc != nil {
				vm.closeAll(iters, exc)
			}
			if it != nil {
				keys = append(keys, k)
				iters = append(iters, it)
			}
		}
		padding := make([]Value, len(iters))
		if mode == "longest" && !paddingOption.IsUndefined() {
			po := paddingOption.ObjectRef()
			for i, k := range keys {
				if exc := vm.Try(func() { padding[i] = po.GetKey(k) }); exc != nil {
					vm.closeAll(iters, exc)
				}
			}
		}
		realm := c.Realm() // the results are made after the call: keep no c
		return vm.iteratorZip(realm, iters, mode, padding, func(results []Value) Value {
			o := realm.newObject(nil)
			for i, k := range keys {
				o.CreateDataPropertyOrThrow(k, results[i])
			}
			return ObjectValue(o)
		})
	})
}

// zipOptions reads the mode and padding options of Iterator.zip(Keyed).
func (vm *VM) zipOptions(options Value) (mode string, padding Value) {
	if options.IsUndefined() {
		return "shortest", Undefined
	}
	o := options.AsObject()
	if o == nil {
		vm.throwTypeError("Iterator.zip options must be an object")
	}
	m := o.GetStr("mode")
	switch {
	case m.IsUndefined():
		mode = "shortest"
	case m.IsString() && slices.Contains([]string{"shortest", "longest", "strict"}, m.StringRef().String()):
		mode = m.StringRef().String()
	default:
		vm.throwTypeError(`Iterator.zip mode must be "shortest", "longest" or "strict"`)
	}
	padding = Undefined
	if mode == "longest" {
		padding = o.GetStr("padding")
		if !padding.IsUndefined() && !padding.IsObject() {
			vm.throwTypeError("Iterator.zip padding must be an object")
		}
	}
	return mode, padding
}

// closeAll is IteratorCloseAll: iterators are closed in reverse order; with
// a throw completion exc their errors are ignored and exc is rethrown.
func (vm *VM) closeAll(iters []*iteratorRecord, exc *Exception) {
	for i := len(iters) - 1; i >= 0; i-- {
		if exc != nil {
			vm.closeIteratorOnThrow(iters[i])
		} else {
			exc = vm.Try(func() { vm.closeIterator(iters[i]) })
		}
	}
	if exc != nil {
		panic(exc)
	}
}

// iteratorZip is IteratorZip.
func (vm *VM) iteratorZip(realm *Realm, iters []*iteratorRecord, mode string, padding []Value, finish func([]Value) Value) Value {
	iterCount := len(iters)
	open := slices.Clone(iters)
	remove := func(it *iteratorRecord) {
		open = slices.DeleteFunc(open, func(x *iteratorRecord) bool { return x == it })
	}
	typeError := func(msg string) *Exception {
		return &Exception{Value: ObjectValue(vm.NewError(ErrorKindTypeError, msg))}
	}
	h := &iteratorHelper{underlying: &iteratorRecord{iterator: realm.NewObject()}}
	h.onReturn = func() { vm.closeAll(open, nil) }
	h.next = func() (Value, bool) {
		if iterCount == 0 {
			return Undefined, false
		}
		results := make([]Value, iterCount)
		for i := 0; i < iterCount; i++ {
			it := iters[i]
			if it == nil {
				results[i] = padding[i]
				continue
			}
			var v Value
			var ok bool
			if exc := vm.Try(func() { v, ok = vm.step(it) }); exc != nil {
				remove(it)
				vm.closeAll(open, exc)
			}
			if ok {
				results[i] = v
				continue
			}
			remove(it)
			switch mode {
			case "shortest":
				vm.closeAll(open, nil)
				return Undefined, false
			case "strict":
				if i != 0 {
					vm.closeAll(open, typeError("Iterator.zip: iterators have different lengths"))
				}
				for k := 1; k < iterCount; k++ {
					var done bool
					if exc := vm.Try(func() { done = vm.iteratorStepDone(iters[k]) }); exc != nil {
						remove(iters[k])
						vm.closeAll(open, exc)
					}
					if !done {
						vm.closeAll(open, typeError("Iterator.zip: iterators have different lengths"))
					}
					remove(iters[k])
				}
				return Undefined, false
			default:
				if len(open) == 0 {
					return Undefined, false
				}
				iters[i] = nil
				results[i] = padding[i]
			}
		}
		return finish(results), true
	}
	o := realm.newObject(realm.IteratorHelperPrototype)
	o.class = ClassIterator
	o.internal = h
	return ObjectValue(o)
}

// iteratorStepDone is IteratorStep without reading the value: it reports
// whether the iterator is done.
func (vm *VM) iteratorStepDone(rec *iteratorRecord) bool {
	result := vm.Call(rec.next, ObjectValue(rec.iterator))
	ro := result.AsObject()
	if ro == nil {
		vm.throwTypeError("Iterator result interface is not an object.")
	}
	return ro.GetKey(doneKey).ToBoolean()
}
