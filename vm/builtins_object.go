package vm

// Object constructor and Object.prototype (ObjectConstructor.cpp,
// ObjectPrototype.cpp).

func (r *Realm) initObject() {
	vm := r.vm
	proto := r.ObjectPrototype
	c := r.constructor("Object", 1, func(c *Call) Value {
		if c.NewTarget != nil && c.NewTarget != c.Callee {
			return ObjectValue(vm.OrdinaryCreateFromConstructor(c.NewTarget, func(r *Realm) *Object { return r.ObjectPrototype }))
		}
		v := c.Arg(0)
		if v.IsNullish() {
			return ObjectValue(c.Realm().NewObject())
		}
		return ObjectValue(vm.ToObject(v))
	}, proto)
	r.ObjectConstructor = c

	r.method(c, "assign", 2, func(c *Call) Value {
		to := vm.ToObject(c.Arg(0))
		for _, src := range c.Args[min(1, len(c.Args)):] {
			if src.IsNullish() {
				continue
			}
			from := vm.ToObject(src)
			for _, k := range from.OwnPropertyKeys() {
				if p, ok := from.GetOwnProperty(k); ok && p.Enumerable() {
					to.SetOrThrow(k, from.Get(k, ObjectValue(from)))
				}
			}
		}
		return ObjectValue(to)
	})
	r.method(c, "create", 2, func(c *Call) Value {
		p := c.Arg(0)
		if !p.IsObject() && !p.IsNull() {
			vm.throwTypeError("Object prototype may only be an Object or null.")
		}
		o := c.Realm().newObject(p.AsObject())
		if props := c.Arg(1); !props.IsUndefined() {
			vm.objectDefineProperties(o, props)
		}
		return ObjectValue(o)
	})
	r.method(c, "defineProperties", 2, func(c *Call) Value {
		o := c.Arg(0).AsObject()
		if o == nil {
			vm.throwTypeError("Object.defineProperties can only be called on Objects.")
		}
		vm.objectDefineProperties(o, c.Arg(1))
		return c.Arg(0)
	})
	r.method(c, "defineProperty", 3, func(c *Call) Value {
		o := c.Arg(0).AsObject()
		if o == nil {
			vm.throwTypeError("Object.defineProperty can only be called on Objects.")
		}
		k := vm.ToPropertyKey(c.Arg(1))
		d := vm.ToPropertyDescriptor(c.Arg(2))
		o.DefinePropertyOrThrow(k, d)
		return c.Arg(0)
	})
	r.method(c, "entries", 1, func(c *Call) Value {
		return ObjectValue(c.Realm().NewArray(vm.enumerableOwnProperties(vm.ToObject(c.Arg(0)), enumEntries)...))
	})
	r.method(c, "freeze", 1, func(c *Call) Value {
		if o := c.Arg(0).AsObject(); o != nil && !o.SetIntegrityLevel(Frozen) {
			vm.throwTypeError("Unable to freeze object")
		}
		return c.Arg(0)
	})
	r.method(c, "fromEntries", 1, func(c *Call) Value {
		iterable := c.Arg(0)
		if iterable.IsNullish() {
			vm.throwTypeError("Object.fromEntries requires an iterable")
		}
		o := c.Realm().NewObject()
		vm.iterate(iterable, func(entry Value) bool {
			e := entry.AsObject()
			if e == nil {
				vm.throwTypeError("Object.fromEntries requires the iterable to produce objects")
			}
			k := e.getIndex(0)
			v := e.getIndex(1)
			o.CreateDataPropertyOrThrow(vm.ToPropertyKey(k), v)
			return true
		})
		return ObjectValue(o)
	})
	r.method(c, "getOwnPropertyDescriptor", 2, func(c *Call) Value {
		o := vm.ToObject(c.Arg(0))
		k := vm.ToPropertyKey(c.Arg(1))
		p, ok := o.GetOwnProperty(k)
		if !ok {
			return Undefined
		}
		d := descriptorFromProperty(p)
		return ObjectValue(vm.FromPropertyDescriptor(&d))
	})
	r.method(c, "getOwnPropertyDescriptors", 1, func(c *Call) Value {
		o := vm.ToObject(c.Arg(0))
		result := c.Realm().NewObject()
		for _, k := range o.OwnPropertyKeys() {
			if p, ok := o.GetOwnProperty(k); ok {
				d := descriptorFromProperty(p)
				result.CreateDataPropertyOrThrow(k, ObjectValue(vm.FromPropertyDescriptor(&d)))
			}
		}
		return ObjectValue(result)
	})
	r.method(c, "getOwnPropertyNames", 1, func(c *Call) Value {
		return ObjectValue(c.Realm().NewArray(ownKeysOfType(vm.ToObject(c.Arg(0)), false)...))
	})
	r.method(c, "getOwnPropertySymbols", 1, func(c *Call) Value {
		return ObjectValue(c.Realm().NewArray(ownKeysOfType(vm.ToObject(c.Arg(0)), true)...))
	})
	r.method(c, "getPrototypeOf", 1, func(c *Call) Value {
		return ObjectValue(vm.ToObject(c.Arg(0)).GetPrototypeOf())
	})
	r.method(c, "groupBy", 2, func(c *Call) Value {
		groups := vm.groupBy(c.Arg(0), c.Arg(1), true)
		o := c.Realm().newObject(nil)
		for _, g := range groups {
			o.CreateDataPropertyOrThrow(vm.ToPropertyKey(g.key), ObjectValue(c.Realm().NewArray(g.values...)))
		}
		return ObjectValue(o)
	})
	r.method(c, "hasOwn", 2, func(c *Call) Value {
		o := vm.ToObject(c.Arg(0))
		return Bool(o.HasOwnProperty(vm.ToPropertyKey(c.Arg(1))))
	})
	r.method(c, "is", 2, func(c *Call) Value { return Bool(SameValue(c.Arg(0), c.Arg(1))) })
	r.method(c, "isExtensible", 1, func(c *Call) Value {
		o := c.Arg(0).AsObject()
		return Bool(o != nil && o.IsExtensible())
	})
	r.method(c, "isFrozen", 1, func(c *Call) Value {
		o := c.Arg(0).AsObject()
		return Bool(o == nil || o.TestIntegrityLevel(Frozen))
	})
	r.method(c, "isSealed", 1, func(c *Call) Value {
		o := c.Arg(0).AsObject()
		return Bool(o == nil || o.TestIntegrityLevel(Sealed))
	})
	r.method(c, "keys", 1, func(c *Call) Value {
		return ObjectValue(c.Realm().NewArray(vm.enumerableOwnProperties(vm.ToObject(c.Arg(0)), enumKeys)...))
	})
	r.method(c, "preventExtensions", 1, func(c *Call) Value {
		if o := c.Arg(0).AsObject(); o != nil && !o.PreventExtensions() {
			vm.throwTypeError("Unable to prevent extension in Object.preventExtensions")
		}
		return c.Arg(0)
	})
	r.method(c, "seal", 1, func(c *Call) Value {
		if o := c.Arg(0).AsObject(); o != nil && !o.SetIntegrityLevel(Sealed) {
			vm.throwTypeError("Unable to seal object")
		}
		return c.Arg(0)
	})
	r.method(c, "setPrototypeOf", 2, func(c *Call) Value {
		v, p := c.Arg(0), c.Arg(1)
		if v.IsNullish() {
			vm.throwTypeError("Object.setPrototypeOf requires an object")
		}
		if !p.IsObject() && !p.IsNull() {
			vm.throwTypeError("Object prototype may only be an Object or null.")
		}
		if o := v.AsObject(); o != nil && !o.SetPrototypeOf(p.AsObject()) {
			vm.throwTypeError("Object.setPrototypeOf failed")
		}
		return v
	})
	r.method(c, "values", 1, func(c *Call) Value {
		return ObjectValue(c.Realm().NewArray(vm.enumerableOwnProperties(vm.ToObject(c.Arg(0)), enumValues)...))
	})

	// Object.prototype
	setFast(r.method(proto, "hasOwnProperty", 1, func(c *Call) Value {
		k := vm.ToPropertyKey(c.Arg(0))
		return Bool(vm.ToObject(c.This).HasOwnProperty(k))
	}), fastHasOwnProperty)
	r.method(proto, "isPrototypeOf", 1, func(c *Call) Value {
		v := c.Arg(0).AsObject()
		if v == nil {
			return False
		}
		o := vm.ToObject(c.This)
		for {
			v = v.GetPrototypeOf()
			if v == nil {
				return False
			}
			if v == o {
				return True
			}
		}
	})
	r.method(proto, "propertyIsEnumerable", 1, func(c *Call) Value {
		k := vm.ToPropertyKey(c.Arg(0))
		p, ok := vm.ToObject(c.This).GetOwnProperty(k)
		return Bool(ok && p.Enumerable())
	})
	r.method(proto, "toLocaleString", 0, func(c *Call) Value {
		return vm.Invoke(c.This, StrKey("toString"))
	})
	r.method(proto, "toString", 0, func(c *Call) Value {
		return StringValue(vm.objectToString(c.This))
	})
	r.method(proto, "valueOf", 0, func(c *Call) Value { return ObjectValue(vm.ToObject(c.This)) })
	r.accessor(proto, StrKey("__proto__"), func(c *Call) Value {
		return ObjectValue(vm.ToObject(c.This).GetPrototypeOf())
	}, func(c *Call) Value {
		if c.This.IsNullish() {
			vm.throwTypeError("Object.prototype.__proto__ called on null or undefined")
		}
		p := c.Arg(0)
		if !p.IsObject() && !p.IsNull() {
			return Undefined
		}
		if o := c.This.AsObject(); o != nil && !o.SetPrototypeOf(p.AsObject()) {
			vm.throwTypeError("Object.prototype.__proto__ setter failed")
		}
		return Undefined
	})
	r.method(proto, "__defineGetter__", 2, func(c *Call) Value {
		o := vm.ToObject(c.This)
		if !c.Arg(1).IsCallable() {
			vm.throwTypeError("invalid getter usage")
		}
		d := PropertyDescriptor{Get: c.Arg(1).ObjectRef(), Enumerable: true, Configurable: true, Has: HasGet | HasEnumerable | HasConfigurable}
		o.DefinePropertyOrThrow(vm.ToPropertyKey(c.Arg(0)), d)
		return Undefined
	})
	r.method(proto, "__defineSetter__", 2, func(c *Call) Value {
		o := vm.ToObject(c.This)
		if !c.Arg(1).IsCallable() {
			vm.throwTypeError("invalid setter usage")
		}
		d := PropertyDescriptor{Set: c.Arg(1).ObjectRef(), Enumerable: true, Configurable: true, Has: HasSet | HasEnumerable | HasConfigurable}
		o.DefinePropertyOrThrow(vm.ToPropertyKey(c.Arg(0)), d)
		return Undefined
	})
	lookup := func(getter bool) NativeFunc {
		return func(c *Call) Value {
			o := vm.ToObject(c.This)
			k := vm.ToPropertyKey(c.Arg(0))
			for ; o != nil; o = o.GetPrototypeOf() {
				if p, ok := o.GetOwnProperty(k); ok {
					if !p.IsAccessor() {
						return Undefined
					}
					f := p.Accessor().Set
					if getter {
						f = p.Accessor().Get
					}
					if f == nil {
						return Undefined
					}
					return ObjectValue(f)
				}
			}
			return Undefined
		}
	}
	r.method(proto, "__lookupGetter__", 1, lookup(true))
	r.method(proto, "__lookupSetter__", 1, lookup(false))
}

func ownKeysOfType(o *Object, symbols bool) []Value {
	var keys []Value
	for _, k := range o.OwnPropertyKeys() {
		if k.IsSymbol() == symbols {
			keys = append(keys, k.Value())
		}
	}
	return keys
}

type enumKind uint8

const (
	enumKeys enumKind = iota
	enumValues
	enumEntries
)

// enumerableOwnProperties (7.3.23).
func (vm *VM) enumerableOwnProperties(o *Object, kind enumKind) []Value {
	var result []Value
	for _, k := range o.OwnPropertyKeys() {
		if k.IsSymbol() {
			continue
		}
		p, ok := o.GetOwnProperty(k)
		if !ok || !p.Enumerable() {
			continue
		}
		switch kind {
		case enumKeys:
			result = append(result, k.Value())
		case enumValues:
			result = append(result, o.Get(k, ObjectValue(o)))
		case enumEntries:
			v := o.Get(k, ObjectValue(o))
			result = append(result, ObjectValue(vm.realm.NewArray(k.Value(), v)))
		}
	}
	return result
}

// objectDefineProperties is ObjectDefineProperties (20.1.2.3.1).
func (vm *VM) objectDefineProperties(o *Object, properties Value) {
	props := vm.ToObject(properties)
	type pending struct {
		k PropertyKey
		d PropertyDescriptor
	}
	var descriptors []pending
	for _, k := range props.OwnPropertyKeys() {
		p, ok := props.GetOwnProperty(k)
		if !ok || !p.Enumerable() {
			continue
		}
		descriptors = append(descriptors, pending{k, vm.ToPropertyDescriptor(props.Get(k, ObjectValue(props)))})
	}
	for _, p := range descriptors {
		o.DefinePropertyOrThrow(p.k, p.d)
	}
}

// ToPropertyDescriptor (6.2.6.5).
func (vm *VM) ToPropertyDescriptor(v Value) PropertyDescriptor {
	o := v.AsObject()
	if o == nil {
		vm.throwTypeError("Property description must be an object.")
	}
	var d PropertyDescriptor
	field := func(name string) (Value, bool) {
		k := StrKey(name)
		if !o.HasProperty(k) {
			return Undefined, false
		}
		return o.Get(k, v), true
	}
	if x, ok := field("enumerable"); ok {
		d.Enumerable, d.Has = x.ToBoolean(), d.Has|HasEnumerable
	}
	if x, ok := field("configurable"); ok {
		d.Configurable, d.Has = x.ToBoolean(), d.Has|HasConfigurable
	}
	if x, ok := field("value"); ok {
		d.Value, d.Has = x, d.Has|HasValue
	}
	if x, ok := field("writable"); ok {
		d.Writable, d.Has = x.ToBoolean(), d.Has|HasWritable
	}
	if x, ok := field("get"); ok {
		if !x.IsUndefined() && !x.IsCallable() {
			vm.throwTypeError("Getter must be a function.")
		}
		d.Get, d.Has = x.AsObject(), d.Has|HasGet
	}
	if x, ok := field("set"); ok {
		if !x.IsUndefined() && !x.IsCallable() {
			vm.throwTypeError("Setter must be a function.")
		}
		d.Set, d.Has = x.AsObject(), d.Has|HasSet
	}
	if d.IsAccessorDescriptor() && d.IsDataDescriptor() {
		vm.throwTypeError("Invalid property.  A property cannot both have accessors and be writable or have a value")
	}
	return d
}

// FromPropertyDescriptor (6.2.6.4).
func (vm *VM) FromPropertyDescriptor(d *PropertyDescriptor) *Object {
	o := vm.realm.NewObject()
	if d.Has&HasValue != 0 {
		o.CreateDataProperty(StrKey("value"), d.Value)
	}
	if d.Has&HasWritable != 0 {
		o.CreateDataProperty(StrKey("writable"), Bool(d.Writable))
	}
	if d.Has&HasGet != 0 {
		o.CreateDataProperty(StrKey("get"), objectOrUndefined(d.Get))
	}
	if d.Has&HasSet != 0 {
		o.CreateDataProperty(StrKey("set"), objectOrUndefined(d.Set))
	}
	if d.Has&HasEnumerable != 0 {
		o.CreateDataProperty(StrKey("enumerable"), Bool(d.Enumerable))
	}
	if d.Has&HasConfigurable != 0 {
		o.CreateDataProperty(StrKey("configurable"), Bool(d.Configurable))
	}
	return o
}

func objectOrUndefined(o *Object) Value {
	if o == nil {
		return Undefined
	}
	return ObjectValue(o)
}

// objectToString is Object.prototype.toString (20.1.3.6).
func (vm *VM) objectToString(this Value) *String {
	switch this.Kind() {
	case KindUndefined:
		return NewString("[object Undefined]")
	case KindNull:
		return NewString("[object Null]")
	}
	o := vm.ToObject(this)
	builtinTag := "Object"
	switch {
	case vm.IsArray(ObjectValue(o)):
		builtinTag = "Array"
	case o.class == ClassArguments:
		builtinTag = "Arguments"
	case o.IsCallable():
		builtinTag = "Function"
	case o.class == ClassError:
		builtinTag = "Error"
	case o.class == ClassBoolean:
		builtinTag = "Boolean"
	case o.class == ClassNumber:
		builtinTag = "Number"
	case o.class == ClassString:
		builtinTag = "String"
	case o.class == ClassDate:
		builtinTag = "Date"
	case o.class == ClassRegExp:
		builtinTag = "RegExp"
	}
	tag := o.Get(SymKey(SymbolToStringTag), ObjectValue(o))
	if tag.IsString() {
		return Concat(NewString("[object "), Concat(tag.StringRef(), NewString("]")))
	}
	return NewString("[object " + builtinTag + "]")
}

type group struct {
	key    Value
	values []Value
}

// groupBy is GroupBy (7.3.35).
func (vm *VM) groupBy(items Value, callback Value, propertyKeys bool) []*group {
	if items.IsNullish() {
		vm.throwTypeError("groupBy requires an iterable")
	}
	if !callback.IsCallable() {
		vm.throwTypeError("groupBy requires a callable")
	}
	var groups []*group
	k := 0
	vm.iterateWithClose(items, func(v Value) bool {
		key := vm.Call(callback, Undefined, v, Int(k))
		if propertyKeys {
			key = vm.ToPropertyKey(key).Value()
		} else if key.IsNumber() && key.Float() == 0 {
			key = Number(0) // normalize -0
		}
		k++
		for _, g := range groups {
			if SameValueZero(g.key, key) {
				g.values = append(g.values, v)
				return true
			}
		}
		groups = append(groups, &group{key: key, values: []Value{v}})
		return true
	})
	return groups
}
