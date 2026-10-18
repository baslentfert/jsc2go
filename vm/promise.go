package vm

// Promises (JSPromise, JSPromiseConstructor, JSPromisePrototype, 27.2).

type promiseState uint8

const (
	promisePending promiseState = iota
	promiseFulfilled
	promiseRejected
)

type promiseData struct {
	state     promiseState
	result    Value
	reactions []*promiseReaction
	isHandled bool
	// alreadyResolved is [[AlreadyResolved]] for resolutions that do not go
	// through user-visible resolving functions.
	alreadyResolved bool
}

type promiseCapability struct {
	promise         Value
	resolve, reject Value
}

// promiseReaction is a PromiseReaction Record. A reaction either calls
// JavaScript handlers and settles a capability, or runs a Go continuation.
type promiseReaction struct {
	capability  *promiseCapability
	onFulfilled Value
	onRejected  Value
	native      func(v Value, rejected bool)
}

func promiseOf(o *Object) *promiseData {
	if p, ok := o.internal.(*promiseData); ok {
		return p
	}
	return nil
}

func (r *Realm) newPromise() *Object {
	o := r.newObject(r.PromisePrototype)
	o.class = ClassPromise
	o.internal = &promiseData{result: Undefined}
	return o
}

func isPromise(v Value) bool {
	o := v.AsObject()
	return o != nil && o.class == ClassPromise && promiseOf(o) != nil
}

// resolvePromise runs the promise resolve function steps (27.2.1.3.2) for
// p, unless p was already resolved.
func (vm *VM) resolvePromise(p *Object, resolution Value) {
	d := promiseOf(p)
	if d.alreadyResolved {
		return
	}
	d.alreadyResolved = true
	vm.resolvePromiseUnchecked(p, resolution)
}

func (vm *VM) resolvePromiseUnchecked(p *Object, resolution Value) {
	if resolution.AsObject() == p {
		vm.settlePromise(p, promiseRejected, ObjectValue(vm.NewError(ErrorKindTypeError, "Cannot resolve a promise with itself")))
		return
	}
	ro := resolution.AsObject()
	if ro == nil {
		vm.settlePromise(p, promiseFulfilled, resolution)
		return
	}
	var then Value
	if exc := vm.Try(func() { then = ro.GetKey(StrKey("then")) }); exc != nil {
		vm.settlePromise(p, promiseRejected, exc.Value)
		return
	}
	if !then.IsCallable() {
		vm.settlePromise(p, promiseFulfilled, resolution)
		return
	}
	realm := vm.realm
	vm.EnqueueJob(func() {
		// NewPromiseResolveThenableJob
		resolve, reject := realm.createResolvingFunctions(p)
		if exc := vm.Try(func() { vm.Call(then, resolution, ObjectValue(resolve), ObjectValue(reject)) }); exc != nil {
			vm.Call(ObjectValue(reject), Undefined, exc.Value)
		}
	})
}

// rejectPromise is the promise reject function guarded by alreadyResolved.
func (vm *VM) rejectPromise(p *Object, reason Value) {
	d := promiseOf(p)
	if d.alreadyResolved {
		return
	}
	d.alreadyResolved = true
	vm.settlePromise(p, promiseRejected, reason)
}

// settlePromise is FulfillPromise / RejectPromise.
func (vm *VM) settlePromise(p *Object, state promiseState, v Value) {
	d := promiseOf(p)
	if d.state != promisePending {
		return
	}
	reactions := d.reactions
	d.reactions = nil
	d.state = state
	d.result = v
	d.alreadyResolved = true
	if state == promiseRejected && !d.isHandled {
		if t := vm.HostHooks.PromiseRejectionTracker; t != nil {
			t(p, "reject")
		}
	}
	for _, reaction := range reactions {
		vm.enqueueReaction(reaction, state, v)
	}
}

func (vm *VM) enqueueReaction(reaction *promiseReaction, state promiseState, v Value) {
	vm.EnqueueJob(func() {
		// PromiseReactionJob
		rejected := state == promiseRejected
		if reaction.native != nil {
			reaction.native(v, rejected)
			return
		}
		handler := reaction.onFulfilled
		if rejected {
			handler = reaction.onRejected
		}
		var result Value
		var exc *Exception
		if handler.IsUndefined() {
			if rejected {
				exc = &Exception{Value: v}
			} else {
				result = v
			}
		} else {
			exc = vm.Try(func() { result = vm.Call(handler, Undefined, v) })
		}
		cap := reaction.capability
		if cap == nil {
			if exc != nil {
				panic(exc)
			}
			return
		}
		if exc != nil {
			vm.Call(cap.reject, Undefined, exc.Value)
		} else {
			vm.Call(cap.resolve, Undefined, result)
		}
	})
}

// createResolvingFunctions (27.2.1.3).
func (r *Realm) createResolvingFunctions(p *Object) (resolve, reject *Object) {
	vm := r.vm
	alreadyResolved := false
	resolve = r.newNativeFunction("", 1, func(c *Call) Value {
		if alreadyResolved {
			return Undefined
		}
		alreadyResolved = true
		vm.resolvePromiseUnchecked(p, c.Arg(0))
		return Undefined
	})
	reject = r.newNativeFunction("", 1, func(c *Call) Value {
		if alreadyResolved {
			return Undefined
		}
		alreadyResolved = true
		vm.settlePromise(p, promiseRejected, c.Arg(0))
		return Undefined
	})
	return resolve, reject
}

// performPromiseThen (27.2.5.4.1).
func (vm *VM) performPromiseThen(p *Object, onFulfilled, onRejected Value, cap *promiseCapability) {
	if !onFulfilled.IsCallable() {
		onFulfilled = Undefined
	}
	if !onRejected.IsCallable() {
		onRejected = Undefined
	}
	vm.addReaction(p, &promiseReaction{capability: cap, onFulfilled: onFulfilled, onRejected: onRejected})
}

// promiseThenNative registers a Go continuation on p.
func (vm *VM) promiseThenNative(p *Object, k func(v Value, rejected bool)) {
	vm.addReaction(p, &promiseReaction{native: k})
}

func (vm *VM) addReaction(p *Object, reaction *promiseReaction) {
	d := promiseOf(p)
	switch d.state {
	case promisePending:
		d.reactions = append(d.reactions, reaction)
	case promiseFulfilled:
		vm.enqueueReaction(reaction, promiseFulfilled, d.result)
	case promiseRejected:
		if !d.isHandled {
			if t := vm.HostHooks.PromiseRejectionTracker; t != nil {
				t(p, "handle")
			}
		}
		vm.enqueueReaction(reaction, promiseRejected, d.result)
	}
	d.isHandled = true
}

// newPromiseCapability (27.2.1.5).
func (vm *VM) newPromiseCapability(c Value) *promiseCapability {
	if !c.IsConstructor() {
		vm.throwTypeError("Promise capability requires a constructor")
	}
	cap := &promiseCapability{resolve: Undefined, reject: Undefined}
	executor := vm.realm.newNativeFunction("", 2, func(call *Call) Value {
		if !cap.resolve.IsUndefined() || !cap.reject.IsUndefined() {
			vm.throwTypeError("Promise capability executor called more than once")
		}
		cap.resolve = call.Arg(0)
		cap.reject = call.Arg(1)
		return Undefined
	})
	promise := vm.Construct(c.ObjectRef(), []Value{ObjectValue(executor)}, nil)
	if !cap.resolve.IsCallable() || !cap.reject.IsCallable() {
		vm.throwTypeError("Promise resolve or reject function is not callable")
	}
	cap.promise = promise
	return cap
}

// promiseResolve is PromiseResolve(C, x) (27.2.4.7.1).
func (vm *VM) promiseResolve(c *Object, x Value) Value {
	if isPromise(x) {
		if SameValue(x.ObjectRef().GetStr("constructor"), ObjectValue(c)) {
			return x
		}
	}
	if c == vm.realm.PromiseConstructor {
		p := vm.realm.newPromise()
		vm.resolvePromise(p, x)
		return ObjectValue(p)
	}
	cap := vm.newPromiseCapability(ObjectValue(c))
	vm.Call(cap.resolve, Undefined, x)
	return cap.promise
}

// await suspends nothing by itself: it arranges for k to run when v
// settles (Await steps 2-9 without the suspension, which generators do).
func (vm *VM) await(v Value, k func(result Value, rejected bool)) {
	var promise Value
	if exc := vm.Try(func() { promise = vm.promiseResolve(vm.realm.PromiseConstructor, v) }); exc != nil {
		k(exc.Value, true)
		return
	}
	vm.promiseThenNative(promise.ObjectRef(), k)
}

func (r *Realm) initPromise() {
	vm := r.vm
	proto := r.NewObject()
	r.PromisePrototype = proto
	c := r.constructor("Promise", 1, func(c *Call) Value {
		if c.NewTarget == nil {
			vm.throwTypeError("Promise constructor cannot be called without new")
		}
		executor := c.Arg(0)
		if !executor.IsCallable() {
			vm.throwTypeError("Promise constructor takes a function argument")
		}
		o := vm.OrdinaryCreateFromConstructor(c.NewTarget, func(r *Realm) *Object { return r.PromisePrototype })
		o.class = ClassPromise
		o.internal = &promiseData{result: Undefined}
		resolve, reject := c.Realm().createResolvingFunctions(o)
		if exc := vm.Try(func() { vm.Call(executor, Undefined, ObjectValue(resolve), ObjectValue(reject)) }); exc != nil {
			vm.Call(ObjectValue(reject), Undefined, exc.Value)
		}
		return ObjectValue(o)
	}, proto)
	r.PromiseConstructor = c
	r.speciesGetter(c)
	r.toStringTag(proto, "Promise")

	r.promiseThen = r.method(proto, "then", 2, func(c *Call) Value {
		p := c.This.AsObject()
		if p == nil || !isPromise(c.This) {
			vm.throwTypeError("Promise.prototype.then requires that |this| be a promise")
		}
		ctor := vm.SpeciesConstructor(p, c.Realm().PromiseConstructor)
		cap := vm.newPromiseCapability(ObjectValue(ctor))
		vm.performPromiseThen(p, c.Arg(0), c.Arg(1), cap)
		return cap.promise
	})
	r.method(proto, "catch", 1, func(c *Call) Value {
		return vm.Invoke(c.This, StrKey("then"), Undefined, c.Arg(0))
	})
	r.method(proto, "finally", 1, func(c *Call) Value {
		p := c.This.AsObject()
		if p == nil {
			vm.throwTypeError("Promise.prototype.finally requires that |this| be an object")
		}
		ctor := vm.SpeciesConstructor(p, c.Realm().PromiseConstructor)
		onFinally := c.Arg(0)
		if !onFinally.IsCallable() {
			return vm.Invoke(c.This, StrKey("then"), onFinally, onFinally)
		}
		realm := c.Realm()
		thenFinally := realm.newNativeFunction("", 1, func(call *Call) Value {
			value := call.Arg(0)
			result := vm.Call(onFinally, Undefined)
			promise := vm.promiseResolve(ctor, result)
			valueThunk := realm.newNativeFunction("", 0, func(*Call) Value { return value })
			return vm.Invoke(promise, StrKey("then"), ObjectValue(valueThunk))
		})
		catchFinally := realm.newNativeFunction("", 1, func(call *Call) Value {
			reason := call.Arg(0)
			result := vm.Call(onFinally, Undefined)
			promise := vm.promiseResolve(ctor, result)
			thrower := realm.newNativeFunction("", 0, func(*Call) Value {
				panic(&Exception{Value: reason})
			})
			return vm.Invoke(promise, StrKey("then"), ObjectValue(thrower))
		})
		return vm.Invoke(c.This, StrKey("then"), ObjectValue(thenFinally), ObjectValue(catchFinally))
	})

	r.promiseResolve = r.method(c, "resolve", 1, func(c *Call) Value {
		ctor := c.This.AsObject()
		if ctor == nil {
			vm.throwTypeError("Promise.resolve requires that |this| be an object")
		}
		return vm.promiseResolve(ctor, c.Arg(0))
	})
	r.method(c, "reject", 1, func(c *Call) Value {
		cap := vm.newPromiseCapability(c.This)
		vm.Call(cap.reject, Undefined, c.Arg(0))
		return cap.promise
	})
	r.method(c, "withResolvers", 0, func(c *Call) Value {
		cap := vm.newPromiseCapability(c.This)
		o := c.Realm().NewObject()
		o.CreateDataPropertyOrThrow(StrKey("promise"), cap.promise)
		o.CreateDataPropertyOrThrow(StrKey("resolve"), cap.resolve)
		o.CreateDataPropertyOrThrow(StrKey("reject"), cap.reject)
		return ObjectValue(o)
	})
	r.method(c, "try", 1, func(c *Call) Value {
		if !c.This.IsObject() {
			vm.throwTypeError("Promise.try requires that |this| be an object")
		}
		var result Value
		var args []Value
		if len(c.Args) > 1 {
			args = c.Args[1:]
		}
		if exc := vm.Try(func() { result = vm.Call(c.Arg(0), Undefined, args...) }); exc != nil {
			cap := vm.newPromiseCapability(c.This)
			vm.Call(cap.reject, Undefined, exc.Value)
			return cap.promise
		}
		// A returned promise of the right constructor is not wrapped.
		return vm.promiseResolve(c.This.ObjectRef(), result)
	})

	combinator := func(name string, body func(c *Call, cap *promiseCapability, rec *iteratorRecord, resolveFn Value)) {
		r.method(c, name, 1, func(call *Call) Value {
			cap := vm.newPromiseCapability(call.This)
			var resolveFn Value
			var rec *iteratorRecord
			if exc := vm.Try(func() {
				resolveFn = call.This.ObjectRef().GetStr("resolve")
				if !resolveFn.IsCallable() {
					vm.throwTypeError("Promise resolve is not a function")
				}
			}); exc != nil {
				vm.Call(cap.reject, Undefined, exc.Value)
				return cap.promise
			}
			if exc := vm.Try(func() { rec = vm.getIterator(call.Arg(0)) }); exc != nil {
				vm.Call(cap.reject, Undefined, exc.Value)
				return cap.promise
			}
			if exc := vm.Try(func() { body(call, cap, rec, resolveFn) }); exc != nil {
				if !rec.done {
					vm.closeIteratorOnThrow(rec)
				}
				vm.Call(cap.reject, Undefined, exc.Value)
			}
			return cap.promise
		})
	}

	// all, allSettled and any share their structure.
	type settleKind uint8
	const (
		settleAll settleKind = iota
		settleAllSettled
		settleAny
	)
	gather := func(kind settleKind) func(c *Call, cap *promiseCapability, rec *iteratorRecord, resolveFn Value) {
		return func(call *Call, cap *promiseCapability, rec *iteratorRecord, resolveFn Value) {
			realm := call.Realm()
			var values []Value
			remaining := 1
			finish := func() {
				arr := ObjectValue(realm.NewArray(values...))
				if kind == settleAny {
					agg := realm.newError(ErrorKindAggregateError, "All promises were rejected")
					agg.putDirect(StrKey("errors"), arr, Writable|Configurable)
					vm.Call(cap.reject, Undefined, ObjectValue(agg))
					return
				}
				vm.Call(cap.resolve, Undefined, arr)
			}
			index := 0
			for {
				next, ok := vm.step(rec)
				if !ok {
					break
				}
				values = append(values, Undefined)
				nextPromise := vm.Call(resolveFn, call.This, next)
				i := index
				called := false
				element := func(f func(v Value)) Value {
					return ObjectValue(realm.newNativeFunction("", 1, func(c *Call) Value {
						if called {
							return Undefined
						}
						called = true
						f(c.Arg(0))
						remaining--
						if remaining == 0 {
							finish()
						}
						return Undefined
					}))
				}
				var onFulfilled, onRejected Value
				switch kind {
				case settleAll:
					onFulfilled = element(func(v Value) { values[i] = v })
					onRejected = cap.reject
				case settleAllSettled:
					record := func(status string, key string) func(Value) {
						return func(v Value) {
							o := realm.NewObject()
							o.CreateDataPropertyOrThrow(StrKey("status"), Str(status))
							o.CreateDataPropertyOrThrow(StrKey(key), v)
							values[i] = ObjectValue(o)
						}
					}
					onFulfilled = element(record("fulfilled", "value"))
					onRejected = element(record("rejected", "reason"))
				case settleAny:
					onFulfilled = cap.resolve
					onRejected = element(func(v Value) { values[i] = v })
				}
				remaining++
				vm.Invoke(nextPromise, StrKey("then"), onFulfilled, onRejected)
				index++
			}
			remaining--
			if remaining == 0 {
				finish()
			}
		}
	}
	combinator("all", gather(settleAll))
	combinator("allSettled", gather(settleAllSettled))
	combinator("any", gather(settleAny))
	combinator("race", func(call *Call, cap *promiseCapability, rec *iteratorRecord, resolveFn Value) {
		for {
			next, ok := vm.step(rec)
			if !ok {
				return
			}
			nextPromise := vm.Call(resolveFn, call.This, next)
			vm.Invoke(nextPromise, StrKey("then"), cap.resolve, cap.reject)
		}
	})
}
