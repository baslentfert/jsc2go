package vm

import "math"

// The Iterator constructor and helpers (27.1.3, 27.1.4; JSC's
// JSIteratorConstructor, JSIteratorPrototype and IteratorHelpers.js).

type helperState uint8

const (
	helperSuspendedStart helperState = iota
	helperSuspendedYield
	helperExecuting
	helperCompleted
)

// iteratorHelper is an Iterator Helper object's generator state, written as
// an explicit state machine instead of an abstract closure.
type iteratorHelper struct {
	underlying *iteratorRecord
	next       func() (Value, bool)
	inner      *iteratorRecord // flatMap's current inner iterator
	// onReturn, if set, closes the underlying iterators instead (zip's
	// IteratorCloseAll over its open iterators).
	onReturn func()
	state    helperState
}

func (r *Realm) initIteratorHelpers() {
	vm := r.vm
	proto := r.IteratorPrototype

	iterCtor := r.constructor("Iterator", 0, func(c *Call) Value {
		if c.NewTarget == nil || c.NewTarget == c.Callee {
			vm.throwTypeError("Iterator cannot be constructed directly")
		}
		return ObjectValue(vm.OrdinaryCreateFromConstructor(c.NewTarget, func(r *Realm) *Object { return r.IteratorPrototype }))
	}, nil)
	iterCtor.putDirect(StrKey("prototype"), ObjectValue(proto), 0)

	// constructor and @@toStringTag are accessors whose setters ignore the
	// prototype itself (SetterThatIgnoresPrototypeProperties).
	ignoringSetter := func(k PropertyKey) NativeFunc {
		return func(c *Call) Value {
			o := c.This.AsObject()
			if o == nil {
				vm.throwTypeError("setter called on non-object")
			}
			if o == c.Realm().IteratorPrototype {
				vm.throwTypeError("Cannot assign to this property of Iterator.prototype")
			}
			if _, ok := o.GetOwnProperty(k); !ok {
				o.CreateDataPropertyOrThrow(k, c.Arg(0))
			} else {
				o.SetOrThrow(k, c.Arg(0))
			}
			return Undefined
		}
	}
	r.accessor(proto, StrKey("constructor"), func(c *Call) Value { return ObjectValue(iterCtor) }, ignoringSetter(StrKey("constructor")))
	r.accessor(proto, SymKey(SymbolToStringTag), func(c *Call) Value { return Str("Iterator") }, ignoringSetter(SymKey(SymbolToStringTag)))

	// %WrapForValidIteratorPrototype%
	wrapProto := r.newObject(proto)
	r.WrapForValidIteratorPrototype = wrapProto
	wrapped := func(c *Call) *iteratorRecord {
		o := c.This.AsObject()
		if o != nil {
			if rec, ok := o.internal.(*wrappedIterator); ok {
				return rec.rec
			}
		}
		vm.throwTypeError("not a wrapped iterator")
		return nil
	}
	r.method(wrapProto, "next", 0, func(c *Call) Value {
		rec := wrapped(c)
		return vm.Call(rec.next, ObjectValue(rec.iterator))
	})
	r.method(wrapProto, "return", 0, func(c *Call) Value {
		rec := wrapped(c)
		ret := vm.GetMethod(ObjectValue(rec.iterator), StrKey("return"))
		if ret.IsUndefined() {
			return c.Realm().iterResult(Undefined, true)
		}
		return vm.Call(ret, ObjectValue(rec.iterator))
	})

	r.method(iterCtor, "from", 1, func(c *Call) Value {
		rec := vm.getIteratorFlattenable(c.Arg(0), true)
		if vm.OrdinaryHasInstance(ObjectValue(iterCtor), ObjectValue(rec.iterator)) {
			return ObjectValue(rec.iterator)
		}
		w := c.Realm().newObject(c.Realm().WrapForValidIteratorPrototype)
		w.class = ClassIterator
		w.internal = &wrappedIterator{rec}
		return ObjectValue(w)
	})

	// %IteratorHelperPrototype%
	helperProto := r.newObject(proto)
	r.IteratorHelperPrototype = helperProto
	thisHelper := func(c *Call) *iteratorHelper {
		if o := c.This.AsObject(); o != nil {
			if h, ok := o.internal.(*iteratorHelper); ok {
				return h
			}
		}
		vm.throwTypeError("Iterator Helper method called on incompatible receiver")
		return nil
	}
	r.method(helperProto, "next", 0, func(c *Call) Value {
		h := thisHelper(c)
		switch h.state {
		case helperExecuting:
			vm.throwTypeError("Generator is already running")
		case helperCompleted:
			return c.Realm().iterResult(Undefined, true)
		}
		h.state = helperExecuting
		ok := false
		defer func() {
			if !ok {
				h.state = helperCompleted
			}
		}()
		v, more := h.next()
		ok = true
		if !more {
			h.state = helperCompleted
			return c.Realm().iterResult(Undefined, true)
		}
		h.state = helperSuspendedYield
		return c.Realm().iterResult(v, false)
	})
	r.method(helperProto, "return", 0, func(c *Call) Value {
		h := thisHelper(c)
		switch h.state {
		case helperExecuting:
			vm.throwTypeError("Generator is already running")
		case helperCompleted:
			return c.Realm().iterResult(Undefined, true)
		case helperSuspendedStart:
			h.state = helperCompleted
			if h.onReturn != nil {
				h.onReturn()
				return c.Realm().iterResult(Undefined, true)
			}
			vm.closeIterator(h.underlying)
			return c.Realm().iterResult(Undefined, true)
		}
		h.state = helperExecuting
		defer func() { h.state = helperCompleted }()
		if h.onReturn != nil {
			h.onReturn()
			return c.Realm().iterResult(Undefined, true)
		}
		if h.inner != nil {
			inner := h.inner
			h.inner = nil
			closedInner := false
			mark := vm.jitMark()
			func() {
				defer func() {
					if !closedInner {
						if r := recover(); r != nil {
							vm.jitUnwindTo(mark)
							vm.closeIteratorOnThrow(h.underlying)
							panic(r)
						}
					}
				}()
				vm.closeIterator(inner)
				closedInner = true
			}()
		}
		vm.closeIterator(h.underlying)
		return c.Realm().iterResult(Undefined, true)
	})
	r.toStringTag(helperProto, "Iterator Helper")

	// thisIterator validates |this| and returns the record with an
	// undefined next method (it is read after argument validation).
	thisObject := func(c *Call, name string) *Object {
		o := c.This.AsObject()
		if o == nil {
			vm.throwTypeError("Iterator.prototype.%s requires that |this| be an Object", name)
		}
		return o
	}
	requireCallable := func(o *Object, f Value, name string) {
		if !f.IsCallable() {
			vm.closeIteratorOnThrow(&iteratorRecord{iterator: o})
			vm.throwTypeError("Iterator.prototype.%s requires a callable argument", name)
		}
	}
	newHelper := func(c *Call, rec *iteratorRecord, next func(h *iteratorHelper) (Value, bool)) Value {
		o := c.Realm().newObject(c.Realm().IteratorHelperPrototype)
		o.class = ClassIterator
		h := &iteratorHelper{underlying: rec}
		h.next = func() (Value, bool) { return next(h) }
		o.internal = h
		return ObjectValue(o)
	}

	r.method(proto, "map", 1, func(c *Call) Value {
		o := thisObject(c, "map")
		mapper := c.Arg(0)
		requireCallable(o, mapper, "map")
		rec := vm.getIteratorDirect(o)
		counter := 0
		return newHelper(c, rec, func(h *iteratorHelper) (Value, bool) {
			v, ok := vm.step(rec)
			if !ok {
				return Undefined, false
			}
			var mapped Value
			vm.callClosingOnThrow(rec, func() bool {
				mapped = vm.Call(mapper, Undefined, v, Int(counter))
				return true
			})
			counter++
			return mapped, true
		})
	})
	r.method(proto, "filter", 1, func(c *Call) Value {
		o := thisObject(c, "filter")
		pred := c.Arg(0)
		requireCallable(o, pred, "filter")
		rec := vm.getIteratorDirect(o)
		counter := 0
		return newHelper(c, rec, func(h *iteratorHelper) (Value, bool) {
			for {
				v, ok := vm.step(rec)
				if !ok {
					return Undefined, false
				}
				selected := vm.callClosingOnThrow(rec, func() bool {
					return vm.Call(pred, Undefined, v, Int(counter)).ToBoolean()
				})
				counter++
				if selected {
					return v, true
				}
			}
		})
	})
	limitArg := func(o *Object, v Value) float64 {
		var n float64
		vm.callClosingOnThrow(&iteratorRecord{iterator: o}, func() bool {
			n = vm.ToNumber(v)
			if math.IsNaN(n) {
				vm.throwRangeError("limit must be a number")
			}
			n = integerOrInfinity(n)
			if n < 0 || !math.IsInf(n, 1) && n > maxSafeInteger {
				vm.throwRangeError("limit must be non-negative")
			}
			return true
		})
		return n
	}
	r.method(proto, "take", 1, func(c *Call) Value {
		o := thisObject(c, "take")
		remaining := limitArg(o, c.Arg(0))
		rec := vm.getIteratorDirect(o)
		return newHelper(c, rec, func(h *iteratorHelper) (Value, bool) {
			if remaining == 0 {
				vm.closeIterator(rec)
				return Undefined, false
			}
			if !math.IsInf(remaining, 1) {
				remaining--
			}
			return vm.step(rec)
		})
	})
	r.method(proto, "drop", 1, func(c *Call) Value {
		o := thisObject(c, "drop")
		remaining := limitArg(o, c.Arg(0))
		rec := vm.getIteratorDirect(o)
		return newHelper(c, rec, func(h *iteratorHelper) (Value, bool) {
			for remaining > 0 {
				if !math.IsInf(remaining, 1) {
					remaining--
				}
				if _, ok := vm.step(rec); !ok {
					return Undefined, false
				}
			}
			return vm.step(rec)
		})
	})
	r.method(proto, "flatMap", 1, func(c *Call) Value {
		o := thisObject(c, "flatMap")
		mapper := c.Arg(0)
		requireCallable(o, mapper, "flatMap")
		rec := vm.getIteratorDirect(o)
		counter := 0
		return newHelper(c, rec, func(h *iteratorHelper) (Value, bool) {
			for {
				if h.inner != nil {
					var v Value
					more := vm.callClosingOnThrow(rec, func() bool {
						var ok bool
						v, ok = vm.step(h.inner)
						return ok
					})
					if more {
						return v, true
					}
					h.inner = nil
				}
				v, ok := vm.step(rec)
				if !ok {
					return Undefined, false
				}
				vm.callClosingOnThrow(rec, func() bool {
					mapped := vm.Call(mapper, Undefined, v, Int(counter))
					h.inner = vm.getIteratorFlattenable(mapped, false)
					return true
				})
				counter++
			}
		})
	})

	// Eager methods.
	r.method(proto, "reduce", 1, func(c *Call) Value {
		o := thisObject(c, "reduce")
		reducer := c.Arg(0)
		requireCallable(o, reducer, "reduce")
		rec := vm.getIteratorDirect(o)
		var acc Value
		counter := 0
		if len(c.Args) < 2 {
			v, ok := vm.step(rec)
			if !ok {
				vm.throwTypeError("Iterator.prototype.reduce of empty iterator with no initial value")
			}
			acc = v
			counter = 1
		} else {
			acc = c.Args[1]
		}
		vm.iterateRecord(rec, func(v Value) bool {
			acc = vm.Call(reducer, Undefined, acc, v, Int(counter))
			counter++
			return true
		})
		return acc
	})
	r.method(proto, "toArray", 0, func(c *Call) Value {
		o := thisObject(c, "toArray")
		rec := vm.getIteratorDirect(o)
		var items []Value
		for {
			v, ok := vm.step(rec)
			if !ok {
				return ObjectValue(c.Realm().NewArray(items...))
			}
			items = append(items, v)
		}
	})
	r.method(proto, "forEach", 1, func(c *Call) Value {
		o := thisObject(c, "forEach")
		fn := c.Arg(0)
		requireCallable(o, fn, "forEach")
		rec := vm.getIteratorDirect(o)
		counter := 0
		vm.iterateRecord(rec, func(v Value) bool {
			vm.Call(fn, Undefined, v, Int(counter))
			counter++
			return true
		})
		return Undefined
	})
	searchMethod := func(name string, onMatch func(v Value) Value, onMatchStop bool, ifNone Value) {
		r.method(proto, name, 1, func(c *Call) Value {
			o := thisObject(c, name)
			pred := c.Arg(0)
			requireCallable(o, pred, name)
			rec := vm.getIteratorDirect(o)
			counter := 0
			result := ifNone
			vm.iterateRecord(rec, func(v Value) bool {
				matched := vm.Call(pred, Undefined, v, Int(counter)).ToBoolean()
				counter++
				if matched == onMatchStop {
					result = onMatch(v)
					return false
				}
				return true
			})
			return result
		})
	}
	searchMethod("some", func(Value) Value { return True }, true, False)
	searchMethod("every", func(Value) Value { return False }, false, True)
	searchMethod("find", func(v Value) Value { return v }, true, Undefined)

	r.symbolMethod(proto, SymbolDispose, 0, func(c *Call) Value {
		ret := vm.GetMethod(c.This, StrKey("return"))
		if !ret.IsUndefined() {
			vm.Call(ret, c.This)
		}
		return Undefined
	}, BuiltinFlags)
	r.symbolMethod(r.AsyncIteratorPrototype, SymbolAsyncDispose, 0, func(c *Call) Value {
		return vm.asyncIteratorDispose(c.This)
	}, BuiltinFlags)

	r.method(iterCtor, "concat", 0, func(c *Call) Value {
		type iterable struct {
			obj    Value
			method Value
		}
		var iterables []iterable
		for _, item := range c.Args {
			if !item.IsObject() {
				vm.throwTypeError("Iterator.concat requires objects")
			}
			m := vm.GetMethod(item, SymKey(SymbolIterator))
			if m.IsUndefined() {
				vm.throwTypeError("Iterator.concat requires iterables")
			}
			iterables = append(iterables, iterable{item, m})
		}
		index := 0
		var current *iteratorRecord
		o := c.Realm().newObject(c.Realm().IteratorHelperPrototype)
		o.class = ClassIterator
		h := &iteratorHelper{underlying: &iteratorRecord{iterator: c.Realm().NewObject()}}
		h.next = func() (Value, bool) {
			for {
				if current == nil {
					if index >= len(iterables) {
						return Undefined, false
					}
					it := iterables[index]
					index++
					current = vm.getIteratorFromMethod(it.obj, it.method)
				}
				h.inner = current
				result := vm.Call(current.next, ObjectValue(current.iterator))
				ro := result.AsObject()
				if ro == nil {
					vm.throwTypeError("Iterator result interface is not an object.")
				}
				if ro.GetKey(doneKey).ToBoolean() {
					current = nil
					h.inner = nil
					continue
				}
				return ro.GetKey(valueKey), true
			}
		}
		o.internal = h
		return ObjectValue(o)
	})
	r.initIteratorProposals(iterCtor)
}

type wrappedIterator struct{ rec *iteratorRecord }

// getIteratorFlattenable (7.4.12). With iterateStrings, string primitives
// are iterated; other primitives are rejected.
func (vm *VM) getIteratorFlattenable(v Value, iterateStrings bool) *iteratorRecord {
	if !v.IsObject() && !(iterateStrings && v.IsString()) {
		vm.throwTypeError("%s is not an object", vm.describeForError(v))
	}
	method := vm.GetMethod(v, SymKey(SymbolIterator))
	var it Value
	if method.IsUndefined() {
		it = v
	} else {
		it = vm.Call(method, v)
	}
	o := it.AsObject()
	if o == nil {
		vm.throwTypeError("Iterator is not an object")
	}
	return vm.getIteratorDirect(o)
}
