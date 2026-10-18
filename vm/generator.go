package vm

// Generators, async functions and async generators. The bytecode generator
// turns their bodies into resumable functions (generatorification); this
// file drives them: JSGenerator / GeneratorPrototype.js, JSAsyncFunction's
// drive functions and JSAsyncGenerator's queue (JSMicrotask.cpp).

// Generator states and fields (JSGenerator::State, JSGenerator::Field).
const (
	genStateCompleted     = -1
	genStateExecuting     = -2
	genStateInit          = 0
	asyncGenStateDraining = -3

	genFieldState   = 0
	genFieldNext    = 1
	genFieldThis    = 2
	genFieldFrame   = 3
	genFieldContext = 4 // JSAsyncFunctionGenerator: the promise

	resumeNormal = 0
	resumeReturn = 1
	resumeThrow  = 2

	asyncGenReasonAwait        = 0
	asyncGenReasonYield        = 1
	asyncGenReasonYieldNoAwait = 2
	asyncGenReasonMask         = 3
	asyncGenReasonShift        = 2
)

func (r *Realm) newGeneratorObject(proto *Object, class Class) *Object {
	o := r.newObject(proto)
	o.class = class
	n := 4
	switch class {
	case ClassAsyncFunctionGenerator:
		n = 5
	case ClassAsyncGenerator:
		o.internal = &asyncGeneratorData{}
	}
	o.extra = &objectExtra{fields: makeFields(n)}
	o.extra.fields[genFieldState] = Int(genStateInit)
	return o
}

// createGenerator is op_create_generator / op_create_async_generator.
func (vm *VM) createGenerator(callee *Object, class Class) *Object {
	fallback := func(r *Realm) *Object { return r.GeneratorPrototype }
	if class == ClassAsyncGenerator {
		fallback = func(r *Realm) *Object { return r.AsyncGeneratorPrototype }
	}
	proto := vm.GetPrototypeFromConstructor(callee, fallback)
	return vm.realm.newGeneratorObject(proto, class)
}

func genState(g *Object) int32 { return int32(g.extra.fields[genFieldState].Float()) }

func setGenState(g *Object, s int32) { g.extra.fields[genFieldState] = Int(int(s)) }

// callGeneratorBody calls the generatorified body: next(this, generator,
// state, value, resumeMode, frame).
func (vm *VM) callGeneratorBody(g *Object, state int32, value Value, mode int32) Value {
	return vm.Call(g.extra.fields[genFieldNext], g.extra.fields[genFieldThis], ObjectValue(g), Int(int(state)), value,
		Int(int(mode)), g.extra.fields[genFieldFrame])
}

// generatorResume is @generatorResume.
func (vm *VM) generatorResume(g *Object, state int32, value Value, mode int32) Value {
	done := true
	if state != genStateCompleted {
		setGenState(g, genStateExecuting)
		ok := false
		func() {
			defer func() {
				if !ok {
					setGenState(g, genStateCompleted)
				}
			}()
			value = vm.callGeneratorBody(g, state, value, mode)
			ok = true
		}()
		done = genState(g) == genStateExecuting
		if done {
			setGenState(g, genStateCompleted)
		}
	}
	return vm.realm.iterResult(value, done)
}

func (r *Realm) initGenerators() {
	vm := r.vm

	// %GeneratorFunction%, %GeneratorFunction.prototype% (%GeneratorPrototype%'s
	// constructor), %GeneratorPrototype%.
	makeFamily := func(name string, kind dynamicKind, fnProto **Object, instanceProto **Object, instanceParent *Object, tag string) *Object {
		fp := r.newObject(r.FunctionPrototype)
		*fnProto = fp
		ip := r.newObject(instanceParent)
		*instanceProto = ip
		ctor := r.newNativeConstructor(name, 1, func(c *Call) Value {
			return ObjectValue(vm.createDynamicFunction(c, kind))
		})
		ctor.proto = r.FunctionConstructor
		ctor.putDirect(StrKey("prototype"), ObjectValue(fp), 0)
		fp.putDirect(StrKey("constructor"), ObjectValue(ctor), Configurable)
		if ip != nil && instanceParent != nil {
			fp.putDirect(StrKey("prototype"), ObjectValue(ip), Configurable)
			ip.putDirect(StrKey("constructor"), ObjectValue(fp), Configurable)
			r.toStringTag(ip, tag)
		}
		r.toStringTag(fp, name)
		return ctor
	}
	makeFamily("GeneratorFunction", dynamicGenerator, &r.GeneratorFunctionPrototype, &r.GeneratorPrototype, r.IteratorPrototype, "Generator")
	var unused *Object
	makeFamily("AsyncFunction", dynamicAsync, &r.AsyncFunctionPrototype, &unused, nil, "")
	r.AsyncIteratorPrototype.proto = r.ObjectPrototype
	makeFamily("AsyncGeneratorFunction", dynamicAsyncGenerator, &r.AsyncGeneratorFunctionPrototype, &r.AsyncGeneratorPrototype, r.AsyncIteratorPrototype, "AsyncGenerator")

	// %GeneratorPrototype% methods.
	gp := r.GeneratorPrototype
	thisGenerator := func(c *Call) *Object {
		o := c.This.AsObject()
		if o == nil || o.class != ClassGenerator {
			vm.throwTypeError("|this| should be a generator")
		}
		if genState(o) == genStateExecuting {
			vm.throwTypeError("Generator is executing")
		}
		return o
	}
	r.method(gp, "next", 1, func(c *Call) Value {
		g := thisGenerator(c)
		state := genState(g)
		v := c.Arg(0)
		if state == genStateCompleted {
			v = Undefined
		}
		return vm.generatorResume(g, state, v, resumeNormal)
	})
	r.method(gp, "return", 1, func(c *Call) Value {
		g := thisGenerator(c)
		return vm.generatorResume(g, genState(g), c.Arg(0), resumeReturn)
	})
	r.method(gp, "throw", 1, func(c *Call) Value {
		g := thisGenerator(c)
		state := genState(g)
		if state == genStateCompleted {
			panic(&Exception{Value: c.Arg(0)})
		}
		return vm.generatorResume(g, state, c.Arg(0), resumeThrow)
	})

	// %AsyncGeneratorPrototype% methods.
	agp := r.AsyncGeneratorPrototype
	asyncGenMethod := func(name string, f func(c *Call, g *Object, promise *Object)) {
		r.method(agp, name, 1, func(c *Call) Value {
			promise := c.Realm().newPromise()
			g := c.This.AsObject()
			if g == nil || g.class != ClassAsyncGenerator {
				vm.rejectPromise(promise, ObjectValue(c.Realm().newError(ErrorKindTypeError, "|this| should be an async generator")))
				return ObjectValue(promise)
			}
			f(c, g, promise)
			return ObjectValue(promise)
		})
	}
	asyncGenMethod("next", func(c *Call, g *Object, promise *Object) {
		state := genState(g)
		if state == genStateCompleted {
			vm.resolvePromise(promise, g.realm.iterResult(Undefined, true))
			return
		}
		asyncGen(g).enqueue(c.Arg(0), resumeNormal, promise)
		if state == genStateInit || isSuspendedYield(state) {
			vm.asyncGeneratorResume(g)
		}
	})
	asyncGenMethod("return", func(c *Call, g *Object, promise *Object) {
		asyncGen(g).enqueue(c.Arg(0), resumeReturn, promise)
		state := genState(g)
		switch {
		case state == genStateInit || state == genStateCompleted:
			setGenState(g, asyncGenStateDraining)
			vm.asyncGeneratorAwaitReturn(g)
		case isSuspendedYield(state):
			vm.asyncGeneratorResume(g)
		}
	})
	asyncGenMethod("throw", func(c *Call, g *Object, promise *Object) {
		state := genState(g)
		if state == genStateInit {
			setGenState(g, genStateCompleted)
			state = genStateCompleted
		}
		if state == genStateCompleted {
			vm.rejectPromise(promise, c.Arg(0))
			return
		}
		asyncGen(g).enqueue(c.Arg(0), resumeThrow, promise)
		if isSuspendedYield(state) {
			vm.asyncGeneratorResume(g)
		}
	})

	r.initAsyncFromSyncIterator()
}

// --- Async functions -------------------------------------------------------------------

// asyncFunctionAwait is asyncFunctionArrangeAwaitResume: it resumes the
// suspended body of g once value settles.
func (vm *VM) asyncFunctionAwait(g *Object, value Value) {
	vm.await(value, func(result Value, rejected bool) {
		mode := int32(resumeNormal)
		if rejected {
			mode = resumeThrow
		}
		vm.asyncFunctionBodyCall(g, result, mode)
	})
}

// asyncFunctionBodyCall is asyncFunctionGeneratorBodyCall.
func (vm *VM) asyncFunctionBodyCall(g *Object, resolution Value, mode int32) {
	state := genState(g)
	setGenState(g, genStateExecuting)
	var value Value
	promise := g.extra.fields[genFieldContext].ObjectRef()
	if exc := vm.Try(func() { value = vm.callGeneratorBody(g, state, resolution, mode) }); exc != nil {
		vm.rejectPromise(promise, exc.Value)
		return
	}
	if genState(g) == genStateExecuting {
		vm.resolvePromise(promise, value)
		return
	}
	vm.asyncFunctionAwait(g, value)
}

// --- Async generators --------------------------------------------------------------------

type asyncGenRequest struct {
	value   Value
	mode    int32
	promise *Object
}

type asyncGeneratorData struct {
	queue []asyncGenRequest
}

func asyncGen(g *Object) *asyncGeneratorData { return g.internal.(*asyncGeneratorData) }

func (d *asyncGeneratorData) enqueue(v Value, mode int32, p *Object) {
	d.queue = append(d.queue, asyncGenRequest{v, mode, p})
}

func isSuspendedYield(state int32) bool {
	return state > 0 && state&asyncGenReasonMask == asyncGenReasonYield
}

func (vm *VM) asyncGeneratorResume(g *Object) {
	req := asyncGen(g).queue[0]
	vm.asyncGeneratorUnwrapYieldResumption(g, req.value, req.mode)
}

// asyncGeneratorUnwrapYieldResumption (27.6.3.7).
func (vm *VM) asyncGeneratorUnwrapYieldResumption(g *Object, value Value, mode int32) {
	if mode != resumeReturn {
		vm.asyncGeneratorBodyCall(g, value, mode)
		return
	}
	state := genState(g)
	setGenState(g, state&^asyncGenReasonMask|asyncGenReasonAwait)
	vm.await(value, func(result Value, rejected bool) {
		if rejected {
			vm.asyncGeneratorBodyCall(g, result, resumeThrow)
		} else {
			vm.asyncGeneratorBodyCall(g, result, resumeReturn)
		}
	})
}

// asyncGeneratorBodyCall runs the body until it suspends or completes.
func (vm *VM) asyncGeneratorBodyCall(g *Object, value Value, mode int32) {
	state := genState(g)
	setGenState(g, genStateExecuting)
	var result Value
	exc := vm.Try(func() {
		result = vm.Call(g.extra.fields[genFieldNext], g.extra.fields[genFieldThis], ObjectValue(g),
			Int(int(state>>asyncGenReasonShift)), value, Int(int(mode)), g.extra.fields[genFieldFrame])
	})
	state = genState(g)
	if exc == nil && state > 0 {
		vm.asyncGeneratorDispatchSuspend(g, result)
		return
	}
	setGenState(g, asyncGenStateDraining)
	if exc != nil {
		vm.asyncGeneratorCompleteStep(g, exc.Value, true, true)
	} else {
		vm.asyncGeneratorCompleteStep(g, result, false, true)
	}
	vm.asyncGeneratorDrainQueue(g)
}

func (vm *VM) asyncGeneratorDispatchSuspend(g *Object, value Value) {
	state := genState(g)
	switch state & asyncGenReasonMask {
	case asyncGenReasonAwait:
		vm.await(value, func(result Value, rejected bool) {
			if rejected {
				vm.asyncGeneratorBodyCall(g, result, resumeThrow)
			} else {
				vm.asyncGeneratorBodyCall(g, result, resumeNormal)
			}
		})
	case asyncGenReasonYield:
		setGenState(g, state&^asyncGenReasonMask|asyncGenReasonAwait)
		vm.await(value, func(result Value, rejected bool) {
			if rejected {
				vm.asyncGeneratorBodyCall(g, result, resumeThrow)
			} else {
				vm.asyncGeneratorYield(g, result)
			}
		})
	case asyncGenReasonYieldNoAwait:
		vm.asyncGeneratorYield(g, value)
	}
}

// asyncGeneratorYield (27.6.3.8, steps after the Await).
func (vm *VM) asyncGeneratorYield(g *Object, value Value) {
	state := genState(g)
	setGenState(g, state&^asyncGenReasonMask|asyncGenReasonAwait)
	vm.asyncGeneratorCompleteStep(g, value, false, false)
	if q := asyncGen(g).queue; len(q) > 0 {
		vm.asyncGeneratorUnwrapYieldResumption(g, q[0].value, q[0].mode)
		return
	}
	state = genState(g)
	setGenState(g, state&^asyncGenReasonMask|asyncGenReasonYield)
}

// asyncGeneratorCompleteStep (27.6.3.4).
func (vm *VM) asyncGeneratorCompleteStep(g *Object, value Value, isThrow, done bool) {
	d := asyncGen(g)
	req := d.queue[0]
	d.queue = d.queue[1:]
	if isThrow {
		vm.rejectPromise(req.promise, value)
		return
	}
	vm.resolvePromise(req.promise, g.realm.iterResult(value, done))
}

// asyncGeneratorAwaitReturn (27.6.3.9).
func (vm *VM) asyncGeneratorAwaitReturn(g *Object) {
	req := asyncGen(g).queue[0]
	vm.await(req.value, func(result Value, rejected bool) {
		vm.asyncGeneratorCompleteStep(g, result, rejected, true)
		vm.asyncGeneratorDrainQueue(g)
	})
}

// asyncGeneratorDrainQueue (27.6.3.10).
func (vm *VM) asyncGeneratorDrainQueue(g *Object) {
	d := asyncGen(g)
	for len(d.queue) > 0 {
		req := d.queue[0]
		if req.mode == resumeReturn {
			vm.asyncGeneratorAwaitReturn(g)
			return
		}
		if req.mode == resumeThrow {
			vm.asyncGeneratorCompleteStep(g, req.value, true, true)
		} else {
			vm.asyncGeneratorCompleteStep(g, Undefined, false, true)
		}
	}
	setGenState(g, genStateCompleted)
}

// --- %AsyncFromSyncIteratorPrototype% ------------------------------------------------------

type asyncFromSync struct{ rec *iteratorRecord }

// createAsyncFromSyncIterator (27.1.6.1).
func (vm *VM) createAsyncFromSyncIterator(rec *iteratorRecord) *Object {
	o := vm.realm.newObject(vm.realm.AsyncFromSyncIteratorPrototype)
	o.class = ClassIterator
	o.internal = &asyncFromSync{rec}
	return o
}

func (r *Realm) initAsyncFromSyncIterator() {
	vm := r.vm
	proto := r.newObject(r.AsyncIteratorPrototype)
	r.AsyncFromSyncIteratorPrototype = proto
	// continuation is AsyncFromSyncIteratorContinuation (27.1.6.4).
	continuation := func(result Value, promise *Object, rec *iteratorRecord, closeOnRejection bool) {
		ro := result.AsObject()
		var done bool
		var value Value
		if exc := vm.Try(func() {
			done = ro.GetKey(doneKey).ToBoolean()
			value = ro.GetKey(valueKey)
		}); exc != nil {
			vm.rejectPromise(promise, exc.Value)
			return
		}
		var wrapped Value
		if exc := vm.Try(func() { wrapped = vm.promiseResolve(vm.realm.PromiseConstructor, value) }); exc != nil {
			if !done && closeOnRejection {
				vm.closeIteratorOnThrow(rec)
			}
			vm.rejectPromise(promise, exc.Value)
			return
		}
		realm := vm.realm
		vm.promiseThenNative(wrapped.ObjectRef(), func(v Value, rejected bool) {
			if rejected {
				if !done && closeOnRejection {
					vm.closeIteratorOnThrow(rec)
				}
				vm.rejectPromise(promise, v)
				return
			}
			vm.resolvePromise(promise, realm.iterResult(v, done))
		})
	}
	thisRec := func(c *Call) *iteratorRecord { return c.This.ObjectRef().internal.(*asyncFromSync).rec }
	r.method(proto, "next", 1, func(c *Call) Value {
		promise := c.Realm().newPromise()
		rec := thisRec(c)
		var result Value
		if exc := vm.Try(func() {
			if len(c.Args) > 0 {
				result = vm.Call(rec.next, ObjectValue(rec.iterator), c.Args[0])
			} else {
				result = vm.Call(rec.next, ObjectValue(rec.iterator))
			}
			if !result.IsObject() {
				vm.throwTypeError("Iterator result interface is not an object.")
			}
		}); exc != nil {
			vm.rejectPromise(promise, exc.Value)
			return ObjectValue(promise)
		}
		continuation(result, promise, rec, true)
		return ObjectValue(promise)
	})
	r.method(proto, "return", 1, func(c *Call) Value {
		promise := c.Realm().newPromise()
		rec := thisRec(c)
		var result Value
		if exc := vm.Try(func() {
			ret := vm.GetMethod(ObjectValue(rec.iterator), StrKey("return"))
			if ret.IsUndefined() {
				result = Undefined
				return
			}
			if len(c.Args) > 0 {
				result = vm.Call(ret, ObjectValue(rec.iterator), c.Args[0])
			} else {
				result = vm.Call(ret, ObjectValue(rec.iterator))
			}
			if !result.IsObject() {
				vm.throwTypeError("Iterator result interface is not an object.")
			}
		}); exc != nil {
			vm.rejectPromise(promise, exc.Value)
			return ObjectValue(promise)
		}
		if result.IsUndefined() {
			vm.resolvePromise(promise, c.Realm().iterResult(c.Arg(0), true))
			return ObjectValue(promise)
		}
		continuation(result, promise, rec, false)
		return ObjectValue(promise)
	})
	r.method(proto, "throw", 1, func(c *Call) Value {
		promise := c.Realm().newPromise()
		rec := thisRec(c)
		var result Value
		missing := false
		if exc := vm.Try(func() {
			th := vm.GetMethod(ObjectValue(rec.iterator), StrKey("throw"))
			if th.IsUndefined() {
				missing = true
				return
			}
			if len(c.Args) > 0 {
				result = vm.Call(th, ObjectValue(rec.iterator), c.Args[0])
			} else {
				result = vm.Call(th, ObjectValue(rec.iterator))
			}
			if !result.IsObject() {
				vm.throwTypeError("Iterator result interface is not an object.")
			}
		}); exc != nil {
			vm.rejectPromise(promise, exc.Value)
			return ObjectValue(promise)
		}
		if missing {
			if exc := vm.Try(func() { vm.closeIterator(rec) }); exc != nil {
				vm.rejectPromise(promise, exc.Value)
				return ObjectValue(promise)
			}
			vm.rejectPromise(promise, ObjectValue(c.Realm().newError(ErrorKindTypeError, "The iterator does not provide a 'throw' method")))
			return ObjectValue(promise)
		}
		continuation(result, promise, rec, true)
		return ObjectValue(promise)
	})
}

// asyncIteratorDispose is %AsyncIteratorPrototype%[@@asyncDispose].
func (vm *VM) asyncIteratorDispose(this Value) Value {
	promise := vm.realm.newPromise()
	var ret Value
	if exc := vm.Try(func() { ret = vm.GetMethod(this, StrKey("return")) }); exc != nil {
		vm.rejectPromise(promise, exc.Value)
		return ObjectValue(promise)
	}
	if ret.IsUndefined() {
		vm.resolvePromise(promise, Undefined)
		return ObjectValue(promise)
	}
	var result Value
	if exc := vm.Try(func() { result = vm.Call(ret, this) }); exc != nil {
		vm.rejectPromise(promise, exc.Value)
		return ObjectValue(promise)
	}
	vm.await(result, func(v Value, rejected bool) {
		if rejected {
			vm.rejectPromise(promise, v)
		} else {
			vm.resolvePromise(promise, Undefined)
		}
	})
	return ObjectValue(promise)
}
