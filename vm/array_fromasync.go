package vm

// Array.fromAsync (proposal-array-from-async). The spec runs the body as an
// async function; here each Await becomes a Go continuation on the awaited
// promise, and any abrupt completion rejects the result promise.
func (vm *VM) arrayFromAsync(c *Call) Value {
	promise := vm.realm.newPromise()
	ctor, items, mapfn, thisArg := c.This, c.Arg(0), c.Arg(1), c.Arg(2)
	reject := func(v Value) { vm.rejectPromise(promise, v) }
	// try runs f and rejects the promise if it throws.
	try := func(f func()) bool {
		if exc := vm.Try(f); exc != nil {
			reject(exc.Value)
			return false
		}
		return true
	}
	mapping := !mapfn.IsUndefined()

	try(func() {
		if mapping && !mapfn.IsCallable() {
			vm.throwTypeError("Array.fromAsync: the mapper must be a function")
		}
		var rec *iteratorRecord
		if m := vm.GetMethod(items, SymKey(SymbolAsyncIterator)); !m.IsUndefined() {
			rec = vm.getIteratorFromMethod(items, m)
		} else if m := vm.GetMethod(items, SymKey(SymbolIterator)); !m.IsUndefined() {
			rec = vm.getIteratorDirect(vm.createAsyncFromSyncIterator(vm.getIteratorFromMethod(items, m)))
		}
		if rec != nil {
			vm.fromAsyncIterator(ctor, rec, mapping, mapfn, thisArg, promise)
		} else {
			vm.fromAsyncArrayLike(ctor, items, mapping, mapfn, thisArg, promise)
		}
	})
	return ObjectValue(promise)
}

func (vm *VM) fromAsyncIterator(ctor Value, rec *iteratorRecord, mapping bool, mapfn, thisArg Value, promise *Object) {
	var a *Object
	if ctor.IsConstructor() {
		a = vm.Construct(ctor.ObjectRef(), nil, nil).AsObject()
	} else {
		a = vm.arrayCreate(0, nil)
	}
	reject := func(v Value) { vm.rejectPromise(promise, v) }
	// closeWith is AsyncIteratorClose with a throw completion: the
	// iterator's return result is awaited but the original error wins.
	closeWith := func(err Value) {
		var result Value
		ok := vm.Try(func() {
			if ret := vm.GetMethod(ObjectValue(rec.iterator), StrKey("return")); !ret.IsUndefined() {
				result = vm.Call(ret, ObjectValue(rec.iterator))
			}
		}) == nil
		if !ok || result.IsUndefined() {
			reject(err)
			return
		}
		vm.await(result, func(Value, bool) { reject(err) })
	}
	k := int64(0)
	var next func()
	define := func(v Value) {
		if exc := vm.Try(func() { a.CreateDataPropertyOrThrow(indexKey64(k), v) }); exc != nil {
			closeWith(exc.Value)
			return
		}
		k++
		next()
	}
	next = func() {
		if k >= maxSafeInteger {
			closeWith(ObjectValue(vm.NewError(ErrorKindTypeError, "Array.fromAsync: too many items")))
			return
		}
		var result Value
		if exc := vm.Try(func() { result = vm.Call(rec.next, ObjectValue(rec.iterator)) }); exc != nil {
			reject(exc.Value)
			return
		}
		vm.await(result, func(res Value, rejected bool) {
			if rejected {
				reject(res)
				return
			}
			var v Value
			done := false
			if exc := vm.Try(func() {
				ro := res.AsObject()
				if ro == nil {
					vm.throwTypeError("Iterator result interface is not an object.")
				}
				if done = ro.GetKey(doneKey).ToBoolean(); done {
					vm.setLength(a, k)
					return
				}
				v = ro.GetKey(valueKey)
			}); exc != nil {
				reject(exc.Value)
				return
			}
			if done {
				vm.resolvePromise(promise, ObjectValue(a))
				return
			}
			if !mapping {
				define(v)
				return
			}
			var mapped Value
			if exc := vm.Try(func() { mapped = vm.Call(mapfn, thisArg, v, Number(float64(k))) }); exc != nil {
				closeWith(exc.Value)
				return
			}
			vm.await(mapped, func(mv Value, rejected bool) {
				if rejected {
					closeWith(mv)
					return
				}
				define(mv)
			})
		})
	}
	next()
}

func (vm *VM) fromAsyncArrayLike(ctor, items Value, mapping bool, mapfn, thisArg Value, promise *Object) {
	arrayLike := vm.ToObject(items)
	n := vm.LengthOfArrayLike(arrayLike)
	var a *Object
	if ctor.IsConstructor() {
		a = vm.Construct(ctor.ObjectRef(), []Value{Number(float64(n))}, nil).AsObject()
	} else {
		a = vm.arrayCreate(n, nil)
	}
	reject := func(v Value) { vm.rejectPromise(promise, v) }
	k := int64(0)
	var next func()
	define := func(v Value) {
		if exc := vm.Try(func() { a.CreateDataPropertyOrThrow(indexKey64(k), v) }); exc != nil {
			reject(exc.Value)
			return
		}
		k++
		next()
	}
	next = func() {
		if k >= n {
			if exc := vm.Try(func() { vm.setLength(a, n) }); exc != nil {
				reject(exc.Value)
				return
			}
			vm.resolvePromise(promise, ObjectValue(a))
			return
		}
		var v Value
		if exc := vm.Try(func() { v = arrayLike.getIdx(k) }); exc != nil {
			reject(exc.Value)
			return
		}
		vm.await(v, func(v Value, rejected bool) {
			if rejected {
				reject(v)
				return
			}
			if !mapping {
				define(v)
				return
			}
			var mapped Value
			if exc := vm.Try(func() { mapped = vm.Call(mapfn, thisArg, v, Number(float64(k))) }); exc != nil {
				reject(exc.Value)
				return
			}
			vm.await(mapped, func(mv Value, rejected bool) {
				if rejected {
					reject(mv)
					return
				}
				define(mv)
			})
		})
	}
	next()
}
