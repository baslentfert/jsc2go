package vm

// Proxy exotic objects (ProxyObject.cpp, 10.5) and the Reflect object.

type proxyData struct {
	target  *Object
	handler *Object // nil once revoked
}

type proxyObject struct{ ordinaryObject }

// proxyCall makes a proxy for a callable target callable.
type proxyCall struct{ canConstruct bool }

func (p *proxyCall) isConstructor() bool { return p.canConstruct }

func proxyOf(o *Object) *proxyData { return o.internal.(*proxyData) }

// trap returns the handler's trap (or undefined) and the target.
func (vm *VM) proxyTrap(o *Object, name string) (Value, *Object, *Object) {
	p := proxyOf(o)
	handler, target := p.handler, p.target // before GetMethod, which may revoke
	if handler == nil {
		vm.throwTypeError("Proxy has been revoked")
	}
	trap := vm.GetMethod(ObjectValue(handler), StrKey(name))
	return trap, handler, target
}

func (vm *VM) newProxy(target, handler Value) *Object {
	t, h := target.AsObject(), handler.AsObject()
	if t == nil || h == nil {
		vm.throwTypeError("Proxy target and handler must be objects")
	}
	o := vm.realm.newObject(nil)
	o.class = ClassProxy
	o.exotic = proxyObject{}
	o.internal = &proxyData{target: t, handler: h}
	if t.IsCallable() {
		o.fn = &proxyCall{canConstruct: t.IsConstructor()}
	}
	return o
}

func (proxyObject) getPrototypeOf(o *Object) *Object {
	vm := o.vm()
	trap, handler, target := vm.proxyTrap(o, "getPrototypeOf")
	if trap.IsUndefined() {
		return target.GetPrototypeOf()
	}
	proto := vm.Call(trap, ObjectValue(handler), ObjectValue(target))
	if !proto.IsObject() && !proto.IsNull() {
		vm.throwTypeError("Proxy getPrototypeOf trap returned neither an object nor null")
	}
	if target.IsExtensible() {
		return proto.AsObject()
	}
	if proto.AsObject() != target.GetPrototypeOf() {
		vm.throwTypeError("Proxy getPrototypeOf trap returned a different prototype for a non-extensible target")
	}
	return proto.AsObject()
}

func (proxyObject) setPrototypeOf(o *Object, v *Object) bool {
	vm := o.vm()
	trap, handler, target := vm.proxyTrap(o, "setPrototypeOf")
	if trap.IsUndefined() {
		return target.SetPrototypeOf(v)
	}
	if !vm.Call(trap, ObjectValue(handler), ObjectValue(target), ObjectValue(v)).ToBoolean() {
		return false
	}
	if target.IsExtensible() {
		return true
	}
	if target.GetPrototypeOf() != v {
		vm.throwTypeError("Proxy setPrototypeOf trap returned true for a non-extensible target with a different prototype")
	}
	return true
}

func (proxyObject) isExtensible(o *Object) bool {
	vm := o.vm()
	trap, handler, target := vm.proxyTrap(o, "isExtensible")
	if trap.IsUndefined() {
		return target.IsExtensible()
	}
	result := vm.Call(trap, ObjectValue(handler), ObjectValue(target)).ToBoolean()
	if result != target.IsExtensible() {
		vm.throwTypeError("Proxy isExtensible trap result does not reflect the target")
	}
	return result
}

func (proxyObject) preventExtensions(o *Object) bool {
	vm := o.vm()
	trap, handler, target := vm.proxyTrap(o, "preventExtensions")
	if trap.IsUndefined() {
		return target.PreventExtensions()
	}
	result := vm.Call(trap, ObjectValue(handler), ObjectValue(target)).ToBoolean()
	if result && target.IsExtensible() {
		vm.throwTypeError("Proxy preventExtensions trap returned true but the target is extensible")
	}
	return result
}

func (proxyObject) getOwnProperty(o *Object, k PropertyKey) (Property, bool) {
	vm := o.vm()
	if k.IsPrivate() {
		return o.ordinaryGetOwnProperty(k)
	}
	trap, handler, target := vm.proxyTrap(o, "getOwnPropertyDescriptor")
	if trap.IsUndefined() {
		return target.GetOwnProperty(k)
	}
	result := vm.Call(trap, ObjectValue(handler), ObjectValue(target), k.Value())
	if !result.IsObject() && !result.IsUndefined() {
		vm.throwTypeError("Proxy getOwnPropertyDescriptor trap returned neither an object nor undefined")
	}
	targetDesc, targetHas := target.GetOwnProperty(k)
	if result.IsUndefined() {
		if !targetHas {
			return Property{}, false
		}
		if !targetDesc.Configurable() {
			vm.throwTypeError("Proxy getOwnPropertyDescriptor trap reported a non-configurable property as missing")
		}
		if !target.IsExtensible() {
			vm.throwTypeError("Proxy getOwnPropertyDescriptor trap reported an existing property of a non-extensible target as missing")
		}
		return Property{}, false
	}
	extensible := target.IsExtensible()
	d := vm.ToPropertyDescriptor(result)
	completePropertyDescriptor(&d)
	if !isCompatiblePropertyDescriptor(extensible, d, targetDesc, targetHas) {
		vm.throwTypeError("Proxy getOwnPropertyDescriptor trap returned an incompatible descriptor")
	}
	if !d.Configurable {
		if !targetHas || targetDesc.Configurable() {
			vm.throwTypeError("Proxy getOwnPropertyDescriptor trap reported a non-configurable property that is configurable or missing on the target")
		}
		if d.Has&HasWritable != 0 && !d.Writable && !targetDesc.IsAccessor() && targetDesc.Writable() {
			vm.throwTypeError("Proxy getOwnPropertyDescriptor trap reported a non-writable property that is writable on the target")
		}
	}
	if d.IsAccessorDescriptor() {
		return accessorProperty(d.Get, d.Set, d.flags()), true
	}
	return dataProperty(d.Value, d.flags()), true
}

func completePropertyDescriptor(d *PropertyDescriptor) {
	if d.IsGenericDescriptor() || d.IsDataDescriptor() {
		if d.Has&HasValue == 0 {
			d.Value = Undefined
		}
		d.Has |= HasValue | HasWritable
	} else {
		d.Has |= HasGet | HasSet
	}
	d.Has |= HasEnumerable | HasConfigurable
}

// isCompatiblePropertyDescriptor (10.1.6.2).
func isCompatiblePropertyDescriptor(extensible bool, d PropertyDescriptor, current Property, has bool) bool {
	if !has {
		return extensible
	}
	_, ok := validateAndApply(current, d)
	return ok
}

func (proxyObject) defineOwnProperty(o *Object, k PropertyKey, d PropertyDescriptor) bool {
	vm := o.vm()
	if k.IsPrivate() {
		return o.ordinaryDefineOwnProperty(k, d)
	}
	trap, handler, target := vm.proxyTrap(o, "defineProperty")
	if trap.IsUndefined() {
		return target.DefineOwnProperty(k, d)
	}
	descObj := vm.FromPropertyDescriptor(&d)
	if !vm.Call(trap, ObjectValue(handler), ObjectValue(target), k.Value(), ObjectValue(descObj)).ToBoolean() {
		return false
	}
	targetDesc, targetHas := target.GetOwnProperty(k)
	extensible := target.IsExtensible()
	settingConfigFalse := d.Has&HasConfigurable != 0 && !d.Configurable
	if !targetHas {
		if !extensible {
			vm.throwTypeError("Proxy defineProperty trap added a property to a non-extensible target")
		}
		if settingConfigFalse {
			vm.throwTypeError("Proxy defineProperty trap defined a non-configurable property that does not exist on the target")
		}
		return true
	}
	if !isCompatiblePropertyDescriptor(extensible, d, targetDesc, true) {
		vm.throwTypeError("Proxy defineProperty trap returned true for an incompatible descriptor")
	}
	if settingConfigFalse && targetDesc.Configurable() {
		vm.throwTypeError("Proxy defineProperty trap defined a non-configurable property that is configurable on the target")
	}
	if !targetDesc.IsAccessor() && !targetDesc.Configurable() && targetDesc.Writable() &&
		d.Has&HasWritable != 0 && !d.Writable {
		vm.throwTypeError("Proxy defineProperty trap made a writable non-configurable property non-writable")
	}
	return true
}

func (proxyObject) hasProperty(o *Object, k PropertyKey) bool {
	vm := o.vm()
	if k.IsPrivate() {
		return o.ordinaryHasProperty(k)
	}
	trap, handler, target := vm.proxyTrap(o, "has")
	if trap.IsUndefined() {
		return target.HasProperty(k)
	}
	result := vm.Call(trap, ObjectValue(handler), ObjectValue(target), k.Value()).ToBoolean()
	if !result {
		if targetDesc, ok := target.GetOwnProperty(k); ok {
			if !targetDesc.Configurable() {
				vm.throwTypeError("Proxy has trap reported a non-configurable property as missing")
			}
			if !target.IsExtensible() {
				vm.throwTypeError("Proxy has trap reported a property of a non-extensible target as missing")
			}
		}
	}
	return result
}

func (proxyObject) get(o *Object, k PropertyKey, receiver Value) Value {
	vm := o.vm()
	if k.IsPrivate() {
		return o.ordinaryGet(k, receiver)
	}
	trap, handler, target := vm.proxyTrap(o, "get")
	if trap.IsUndefined() {
		return target.Get(k, receiver)
	}
	v := vm.Call(trap, ObjectValue(handler), ObjectValue(target), k.Value(), receiver)
	if targetDesc, ok := target.GetOwnProperty(k); ok && !targetDesc.Configurable() {
		if !targetDesc.IsAccessor() && !targetDesc.Writable() && !SameValue(v, targetDesc.Value) {
			vm.throwTypeError("Proxy get trap returned a different value for a non-writable, non-configurable property")
		}
		if targetDesc.IsAccessor() && targetDesc.Accessor().Get == nil && !v.IsUndefined() {
			vm.throwTypeError("Proxy get trap returned a value for a non-configurable accessor property without a getter")
		}
	}
	return v
}

func (proxyObject) set(o *Object, k PropertyKey, v Value, receiver Value) bool {
	vm := o.vm()
	if k.IsPrivate() {
		return o.ordinarySet(k, v, receiver)
	}
	trap, handler, target := vm.proxyTrap(o, "set")
	if trap.IsUndefined() {
		return target.Set(k, v, receiver)
	}
	if !vm.Call(trap, ObjectValue(handler), ObjectValue(target), k.Value(), v, receiver).ToBoolean() {
		return false
	}
	if targetDesc, ok := target.GetOwnProperty(k); ok && !targetDesc.Configurable() {
		if !targetDesc.IsAccessor() && !targetDesc.Writable() && !SameValue(v, targetDesc.Value) {
			vm.throwTypeError("Proxy set trap changed a non-writable, non-configurable property")
		}
		if targetDesc.IsAccessor() && targetDesc.Accessor().Set == nil {
			vm.throwTypeError("Proxy set trap set a non-configurable accessor property without a setter")
		}
	}
	return true
}

func (proxyObject) delete(o *Object, k PropertyKey) bool {
	vm := o.vm()
	if k.IsPrivate() {
		return o.ordinaryDelete(k)
	}
	trap, handler, target := vm.proxyTrap(o, "deleteProperty")
	if trap.IsUndefined() {
		return target.Delete(k)
	}
	if !vm.Call(trap, ObjectValue(handler), ObjectValue(target), k.Value()).ToBoolean() {
		return false
	}
	targetDesc, ok := target.GetOwnProperty(k)
	if !ok {
		return true
	}
	if !targetDesc.Configurable() {
		vm.throwTypeError("Proxy deleteProperty trap deleted a non-configurable property")
	}
	if !target.IsExtensible() {
		vm.throwTypeError("Proxy deleteProperty trap deleted a property of a non-extensible target")
	}
	return true
}

func (proxyObject) ownKeys(o *Object) []PropertyKey {
	vm := o.vm()
	trap, handler, target := vm.proxyTrap(o, "ownKeys")
	if trap.IsUndefined() {
		return target.OwnPropertyKeys()
	}
	result := vm.Call(trap, ObjectValue(handler), ObjectValue(target))
	list := vm.CreateListFromArrayLike(result, true)
	keys := make([]PropertyKey, len(list))
	seen := make(map[PropertyKey]bool, len(list))
	for i, v := range list {
		k := vm.ToPropertyKey(v)
		if seen[k] {
			vm.throwTypeError("Proxy ownKeys trap returned duplicate entries")
		}
		seen[k] = true
		keys[i] = k
	}
	extensible := target.IsExtensible()
	targetKeys := target.OwnPropertyKeys()
	var configurable, nonconfigurable []PropertyKey
	for _, k := range targetKeys {
		if d, ok := target.GetOwnProperty(k); ok && !d.Configurable() {
			nonconfigurable = append(nonconfigurable, k)
		} else {
			configurable = append(configurable, k)
		}
	}
	if extensible && len(nonconfigurable) == 0 {
		return keys
	}
	unchecked := make(map[PropertyKey]bool, len(keys))
	for _, k := range keys {
		unchecked[k] = true
	}
	for _, k := range nonconfigurable {
		if !unchecked[k] {
			vm.throwTypeError("Proxy ownKeys trap result is missing a non-configurable key")
		}
		delete(unchecked, k)
	}
	if extensible {
		return keys
	}
	for _, k := range configurable {
		if !unchecked[k] {
			vm.throwTypeError("Proxy ownKeys trap result is missing a key of a non-extensible target")
		}
		delete(unchecked, k)
	}
	if len(unchecked) > 0 {
		vm.throwTypeError("Proxy ownKeys trap added keys to a non-extensible target")
	}
	return keys
}

func (p *proxyCall) call(f *Object, this Value, args []Value) Value {
	vm := f.vm()
	trap, handler, target := vm.proxyTrap(f, "apply")
	if trap.IsUndefined() {
		return vm.Call(ObjectValue(target), this, args...)
	}
	argArray := vm.realm.NewArray(copyArgs(args)...)
	return vm.Call(trap, ObjectValue(handler), ObjectValue(target), this, ObjectValue(argArray))
}

func (p *proxyCall) construct(f *Object, args []Value, newTarget *Object) Value {
	vm := f.vm()
	trap, handler, target := vm.proxyTrap(f, "construct")
	if trap.IsUndefined() {
		return vm.Construct(target, args, newTarget)
	}
	argArray := vm.realm.NewArray(copyArgs(args)...)
	result := vm.Call(trap, ObjectValue(handler), ObjectValue(target), ObjectValue(argArray), ObjectValue(newTarget))
	if !result.IsObject() {
		vm.throwTypeError("Proxy construct trap returned a non-object")
	}
	return result
}

func (r *Realm) initProxy() {
	vm := r.vm
	c := r.constructor("Proxy", 2, func(c *Call) Value {
		if c.NewTarget == nil {
			vm.throwTypeError("Proxy requires the new operator")
		}
		return ObjectValue(vm.newProxy(c.Arg(0), c.Arg(1)))
	}, nil)
	r.ProxyConstructor = c
	r.method(c, "revocable", 2, func(c *Call) Value {
		p := vm.newProxy(c.Arg(0), c.Arg(1))
		revoke := c.Realm().newNativeFunction("", 0, func(c *Call) Value {
			if p != nil {
				d := proxyOf(p)
				d.handler = nil
				d.target = nil
				p = nil
			}
			return Undefined
		})
		result := c.Realm().NewObject()
		result.CreateDataPropertyOrThrow(StrKey("proxy"), ObjectValue(p))
		result.CreateDataPropertyOrThrow(StrKey("revoke"), ObjectValue(revoke))
		return ObjectValue(result)
	})
}

func (r *Realm) initReflect() {
	vm := r.vm
	o := r.NewObject()
	r.GlobalObject.putDirect(StrKey("Reflect"), ObjectValue(o), BuiltinFlags)
	r.toStringTag(o, "Reflect")
	target := func(v Value, name string) *Object {
		t := v.AsObject()
		if t == nil {
			vm.throwTypeError("Reflect.%s requires the first argument be an object", name)
		}
		return t
	}
	r.method(o, "apply", 3, func(c *Call) Value {
		if !c.Arg(0).IsCallable() {
			vm.throwTypeError("Reflect.apply requires the first argument be a function")
		}
		args := vm.CreateListFromArrayLike(c.Arg(2), false)
		return vm.Call(c.Arg(0), c.Arg(1), args...)
	})
	r.method(o, "construct", 2, func(c *Call) Value {
		f := c.Arg(0)
		if !f.IsConstructor() {
			vm.throwTypeError("Reflect.construct requires the first argument be a constructor")
		}
		newTarget := f
		if len(c.Args) > 2 {
			newTarget = c.Args[2]
			if !newTarget.IsConstructor() {
				vm.throwTypeError("Reflect.construct requires the third argument be a constructor if present")
			}
		}
		args := vm.CreateListFromArrayLike(c.Arg(1), false)
		return vm.Construct(f.ObjectRef(), args, newTarget.ObjectRef())
	})
	r.method(o, "defineProperty", 3, func(c *Call) Value {
		t := target(c.Arg(0), "defineProperty")
		k := vm.ToPropertyKey(c.Arg(1))
		d := vm.ToPropertyDescriptor(c.Arg(2))
		return Bool(t.DefineOwnProperty(k, d))
	})
	r.method(o, "deleteProperty", 2, func(c *Call) Value {
		t := target(c.Arg(0), "deleteProperty")
		return Bool(t.Delete(vm.ToPropertyKey(c.Arg(1))))
	})
	r.method(o, "get", 2, func(c *Call) Value {
		t := target(c.Arg(0), "get")
		k := vm.ToPropertyKey(c.Arg(1))
		receiver := c.Arg(0)
		if len(c.Args) > 2 {
			receiver = c.Args[2]
		}
		return t.Get(k, receiver)
	})
	r.method(o, "getOwnPropertyDescriptor", 2, func(c *Call) Value {
		t := target(c.Arg(0), "getOwnPropertyDescriptor")
		p, ok := t.GetOwnProperty(vm.ToPropertyKey(c.Arg(1)))
		if !ok {
			return Undefined
		}
		d := descriptorFromProperty(p)
		return ObjectValue(vm.FromPropertyDescriptor(&d))
	})
	r.method(o, "getPrototypeOf", 1, func(c *Call) Value {
		return ObjectValue(target(c.Arg(0), "getPrototypeOf").GetPrototypeOf())
	})
	r.method(o, "has", 2, func(c *Call) Value {
		t := target(c.Arg(0), "has")
		return Bool(t.HasProperty(vm.ToPropertyKey(c.Arg(1))))
	})
	r.method(o, "isExtensible", 1, func(c *Call) Value {
		return Bool(target(c.Arg(0), "isExtensible").IsExtensible())
	})
	r.method(o, "ownKeys", 1, func(c *Call) Value {
		keys := target(c.Arg(0), "ownKeys").OwnPropertyKeys()
		values := make([]Value, len(keys))
		for i, k := range keys {
			values[i] = k.Value()
		}
		return ObjectValue(c.Realm().NewArray(values...))
	})
	r.method(o, "preventExtensions", 1, func(c *Call) Value {
		return Bool(target(c.Arg(0), "preventExtensions").PreventExtensions())
	})
	r.method(o, "set", 3, func(c *Call) Value {
		t := target(c.Arg(0), "set")
		k := vm.ToPropertyKey(c.Arg(1))
		receiver := c.Arg(0)
		if len(c.Args) > 3 {
			receiver = c.Args[3]
		}
		return Bool(t.Set(k, c.Arg(2), receiver))
	})
	r.method(o, "setPrototypeOf", 2, func(c *Call) Value {
		t := target(c.Arg(0), "setPrototypeOf")
		p := c.Arg(1)
		if !p.IsObject() && !p.IsNull() {
			vm.throwTypeError("Reflect.setPrototypeOf requires the second argument be an object or null")
		}
		return Bool(t.SetPrototypeOf(p.AsObject()))
	})
}
