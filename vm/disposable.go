package vm

// Explicit resource management: GetDisposeMethod for using declarations,
// DisposableStack and AsyncDisposableStack (DisposableStackPrototype.js,
// AsyncDisposableStackPrototype.js).

// disposableResource is a DisposableResource record.
type disposableResource struct {
	value, method Value
}

// disposableStack holds [[DisposableState]] and [[DisposeCapability]].
type disposableStack struct {
	disposed  bool
	resources []disposableResource
}

// getDisposeMethod is GetDisposeMethod(V, hint) for using declarations;
// it returns undefined for null and undefined.
func (vm *VM) getDisposeMethod(v Value, async bool) Value {
	if v.IsNullish() {
		return Undefined
	}
	if !v.IsObject() {
		vm.throwTypeError("Disposable value must be an object, null, or undefined")
	}
	if async {
		method := vm.GetV(v, SymKey(SymbolAsyncDispose))
		if !method.IsNullish() {
			if !method.IsCallable() {
				vm.throwTypeError("@@asyncDispose must be callable")
			}
			return method
		}
		method = vm.GetV(v, SymKey(SymbolDispose))
		if method.IsNullish() {
			vm.throwTypeError("@@asyncDispose and @@dispose must not be undefined or null")
		}
		if !method.IsCallable() {
			vm.throwTypeError("@@dispose must be callable")
		}
		// Wrap the sync method so that it returns a promise.
		return ObjectValue(vm.realm.newNativeFunction("", 0, func(c *Call) Value {
			p := vm.realm.newPromise()
			if exc := vm.Try(func() { vm.Call(method, v) }); exc != nil {
				vm.rejectPromise(p, exc.Value)
			} else {
				vm.resolvePromise(p, Undefined)
			}
			return ObjectValue(p)
		}))
	}
	method := vm.GetV(v, SymKey(SymbolDispose))
	if method.IsNullish() {
		vm.throwTypeError("@@dispose must not be undefined or null")
	}
	if !method.IsCallable() {
		vm.throwTypeError("@@dispose must be callable")
	}
	return method
}

// suppress combines a new disposal error with the pending one.
func (vm *VM) suppress(err Value, pending Value, thrown bool) Value {
	if !thrown {
		return err
	}
	ctor := vm.realm.ErrorConstructors[ErrorKindSuppressedError]
	return vm.Construct(ctor, []Value{err, pending}, nil)
}

func (r *Realm) initDisposableStack() {
	r.initSyncDisposableStack()
	r.initAsyncDisposableStack()
}

func (r *Realm) initSyncDisposableStack() {
	vm := r.vm
	proto := r.NewObject()
	r.DisposableStackPrototype = proto
	r.constructor("DisposableStack", 0, func(c *Call) Value {
		if c.NewTarget == nil {
			vm.throwTypeError("DisposableStack constructor requires 'new'")
		}
		o := vm.OrdinaryCreateFromConstructor(c.NewTarget, func(r *Realm) *Object { return r.DisposableStackPrototype })
		o.class = ClassDisposableStack
		o.internal = &disposableStack{}
		return ObjectValue(o)
	}, proto)

	this := func(c *Call, name string) *disposableStack {
		if o := c.This.AsObject(); o != nil && o.class == ClassDisposableStack {
			return o.internal.(*disposableStack)
		}
		vm.throwTypeError("DisposableStack.prototype.%s requires that |this| be a DisposableStack object", name)
		return nil
	}
	pending := func(c *Call, name string) *disposableStack {
		s := this(c, name)
		if s.disposed {
			vm.throwError(ErrorKindReferenceError, "DisposableStack.prototype.%s requires that |this| be a pending DisposableStack object", name)
		}
		return s
	}
	callable := func(v Value, name, arg string) {
		if !v.IsCallable() {
			vm.throwTypeError("DisposableStack.prototype.%s requires that %s argument be a callable", name, arg)
		}
	}

	r.getter(proto, StrKey("disposed"), func(c *Call) Value { return Bool(this(c, "disposed").disposed) })
	dispose := r.method(proto, "dispose", 0, func(c *Call) Value {
		s := this(c, "dispose")
		if s.disposed {
			return Undefined
		}
		s.disposed = true
		resources := s.resources
		s.resources = nil
		var err Value
		thrown := false
		for i := len(resources) - 1; i >= 0; i-- {
			res := resources[i]
			if exc := vm.Try(func() { vm.Call(res.method, res.value) }); exc != nil {
				err = vm.suppress(exc.Value, err, thrown)
				thrown = true
			}
		}
		if thrown {
			panic(&Exception{Value: err})
		}
		return Undefined
	})
	proto.putDirect(SymKey(SymbolDispose), ObjectValue(dispose), BuiltinFlags)
	r.method(proto, "use", 1, func(c *Call) Value {
		s := pending(c, "use")
		v := c.Arg(0)
		if !v.IsNullish() {
			s.resources = append(s.resources, disposableResource{v, vm.getDisposeMethod(v, false)})
		}
		return v
	})
	r.method(proto, "adopt", 2, func(c *Call) Value {
		s := pending(c, "adopt")
		value, onDispose := c.Arg(0), c.Arg(1)
		callable(onDispose, "adopt", "onDispose")
		closure := r.newNativeFunction("", 0, func(*Call) Value {
			vm.Call(onDispose, Undefined, value)
			return Undefined
		})
		s.resources = append(s.resources, disposableResource{Undefined, ObjectValue(closure)})
		return value
	})
	r.method(proto, "defer", 1, func(c *Call) Value {
		s := pending(c, "defer")
		callable(c.Arg(0), "defer", "onDispose")
		s.resources = append(s.resources, disposableResource{Undefined, c.Arg(0)})
		return Undefined
	})
	r.method(proto, "move", 0, func(c *Call) Value {
		s := pending(c, "move")
		o := r.newObject(r.DisposableStackPrototype)
		o.class = ClassDisposableStack
		o.internal = &disposableStack{resources: s.resources}
		s.resources, s.disposed = nil, true
		return ObjectValue(o)
	})
	r.toStringTag(proto, "DisposableStack")
}

func (r *Realm) initAsyncDisposableStack() {
	vm := r.vm
	proto := r.NewObject()
	r.AsyncDisposableStackPrototype = proto
	r.constructor("AsyncDisposableStack", 0, func(c *Call) Value {
		if c.NewTarget == nil {
			vm.throwTypeError("AsyncDisposableStack constructor requires 'new'")
		}
		o := vm.OrdinaryCreateFromConstructor(c.NewTarget, func(r *Realm) *Object { return r.AsyncDisposableStackPrototype })
		o.class = ClassAsyncDisposableStack
		o.internal = &disposableStack{}
		return ObjectValue(o)
	}, proto)

	stackOf := func(v Value) *disposableStack {
		if o := v.AsObject(); o != nil && o.class == ClassAsyncDisposableStack {
			return o.internal.(*disposableStack)
		}
		return nil
	}
	this := func(c *Call, name string) *disposableStack {
		s := stackOf(c.This)
		if s == nil {
			vm.throwTypeError("AsyncDisposableStack.prototype.%s requires that |this| be a AsyncDisposableStack object", name)
		}
		return s
	}
	pending := func(c *Call, name string) *disposableStack {
		s := this(c, name)
		if s.disposed {
			vm.throwError(ErrorKindReferenceError, "AsyncDisposableStack.prototype.%s requires that |this| be a pending AsyncDisposableStack object", name)
		}
		return s
	}
	callable := func(v Value, name, arg string) {
		if !v.IsCallable() {
			vm.throwTypeError("AsyncDisposableStack.prototype.%s requires that %s argument be a callable", name, arg)
		}
	}

	r.getter(proto, StrKey("disposed"), func(c *Call) Value { return Bool(this(c, "disposed").disposed) })
	disposeAsync := r.method(proto, "disposeAsync", 0, func(c *Call) Value {
		promise := r.newPromise()
		s := stackOf(c.This)
		if s == nil {
			vm.rejectPromise(promise, ObjectValue(vm.NewError(ErrorKindTypeError,
				"AsyncDisposableStack.prototype.disposeAsync requires that |this| be a AsyncDisposableStack object")))
			return ObjectValue(promise)
		}
		if s.disposed {
			vm.resolvePromise(promise, Undefined)
			return ObjectValue(promise)
		}
		s.disposed = true
		resources := s.resources
		s.resources = nil
		vm.disposeResourcesAsync(resources, promise)
		return ObjectValue(promise)
	})
	proto.putDirect(SymKey(SymbolAsyncDispose), ObjectValue(disposeAsync), BuiltinFlags)
	r.method(proto, "use", 1, func(c *Call) Value {
		s := pending(c, "use")
		v := c.Arg(0)
		if v.IsNullish() {
			// A null resource still forces an await during disposal.
			s.resources = append(s.resources, disposableResource{Undefined, Undefined})
		} else {
			s.resources = append(s.resources, disposableResource{v, vm.getDisposeMethod(v, true)})
		}
		return v
	})
	r.method(proto, "adopt", 2, func(c *Call) Value {
		s := pending(c, "adopt")
		value, onDispose := c.Arg(0), c.Arg(1)
		callable(onDispose, "adopt", "onAsyncDispose")
		closure := r.newNativeFunction("", 0, func(*Call) Value { return vm.Call(onDispose, Undefined, value) })
		s.resources = append(s.resources, disposableResource{Undefined, ObjectValue(closure)})
		return value
	})
	r.method(proto, "defer", 1, func(c *Call) Value {
		s := pending(c, "defer")
		callable(c.Arg(0), "defer", "onAsyncDispose")
		s.resources = append(s.resources, disposableResource{Undefined, c.Arg(0)})
		return Undefined
	})
	r.method(proto, "move", 0, func(c *Call) Value {
		s := pending(c, "move")
		o := r.newObject(r.AsyncDisposableStackPrototype)
		o.class = ClassAsyncDisposableStack
		o.internal = &disposableStack{resources: s.resources}
		s.resources, s.disposed = nil, true
		return ObjectValue(o)
	})
	r.toStringTag(proto, "AsyncDisposableStack")
}

// disposeResourcesAsync is DisposeResources for an async dispose
// capability: resources are disposed in reverse order, awaiting each result,
// and promise settles with the combined outcome.
func (vm *VM) disposeResourcesAsync(resources []disposableResource, promise *Object) {
	i := len(resources)
	var err Value
	thrown, needsAwait, hasAwaited := false, false, false
	finish := func() {
		if thrown {
			vm.rejectPromise(promise, err)
		} else {
			vm.resolvePromise(promise, Undefined)
		}
	}
	var loop func()
	loop = func() {
		for i > 0 {
			i--
			res := resources[i]
			if res.method.IsUndefined() {
				needsAwait = true
				continue
			}
			var result Value
			if exc := vm.Try(func() { result = vm.Call(res.method, res.value) }); exc != nil {
				err = vm.suppress(exc.Value, err, thrown)
				thrown = true
				continue
			}
			hasAwaited = true
			vm.await(result, func(v Value, rejected bool) {
				if rejected {
					err = vm.suppress(v, err, thrown)
					thrown = true
				}
				loop()
			})
			return
		}
		if needsAwait && !hasAwaited {
			vm.await(Undefined, func(Value, bool) { finish() })
			return
		}
		finish()
	}
	loop()
}
