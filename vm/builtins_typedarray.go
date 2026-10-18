package vm

import (
	"encoding/binary"
	"math"
	"slices"
)

// ArrayBuffer, SharedArrayBuffer, %TypedArray% and its subclasses, DataView
// (JSArrayBufferConstructor/Prototype, JSTypedArrayViewConstructor/Prototype,
// JSGenericTypedArrayViewConstructor, JSDataViewPrototype).

const maxArrayBufferLength = 1 << 32

// allocateArrayBuffer is AllocateArrayBuffer (25.1.3.1) with an optional
// maximum length (resizable when maxLength >= 0).
func (vm *VM) allocateArrayBuffer(newTarget *Object, length int64, maxLength int64, shared bool) *Object {
	fallback := func(r *Realm) *Object { return r.ArrayBufferPrototype }
	if shared {
		fallback = func(r *Realm) *Object { return r.SharedArrayBufferPrototype }
	}
	if maxLength >= 0 && length > maxLength {
		vm.throwRangeError("ArrayBuffer length exceeds maxByteLength")
	}
	proto := vm.GetPrototypeFromConstructor(newTarget, fallback)
	if length > maxArrayBufferLength || maxLength > maxArrayBufferLength {
		vm.throwRangeError("Out of memory")
	}
	o := vm.realm.newObject(proto)
	o.class = ClassArrayBuffer
	if shared {
		o.class = ClassSharedArrayBuffer
	}
	b := &arrayBufferData{shared: shared, maxLength: int(length)}
	if maxLength >= 0 {
		b.resizable = true
		b.maxLength = int(maxLength)
		b.data = make([]byte, length, maxLength)
	} else {
		b.data = make([]byte, length)
	}
	o.internal = b
	return o
}

func (vm *VM) newArrayBuffer(length int) *Object {
	return vm.allocateArrayBuffer(vm.realm.GlobalObject.GetStr("ArrayBuffer").AsObject(), int64(length), -1, false)
}

// detachArrayBuffer is DetachArrayBuffer (25.1.3.5).
func (vm *VM) detachArrayBuffer(v Value, key Value) {
	o := v.AsObject()
	if o == nil || o.class != ClassArrayBuffer {
		vm.throwTypeError("detachArrayBuffer requires an ArrayBuffer")
	}
	b := bufferOf(o)
	if !SameValue(b.detachKey, key) {
		vm.throwTypeError("ArrayBuffer detach key mismatch")
	}
	b.data = nil
	b.detached = true
	vm.detachEpoch++ // views' vectors are stale
}

func (vm *VM) thisBuffer(c *Call, name string, shared bool) *arrayBufferData {
	want := ClassArrayBuffer
	if shared {
		want = ClassSharedArrayBuffer
	}
	if o := c.This.AsObject(); o != nil && o.class == want {
		return bufferOf(o)
	}
	vm.throwTypeError("%s called on incompatible receiver", name)
	return nil
}

func (r *Realm) initTypedArrays() {
	vm := r.vm
	r.initArrayBuffer(false)
	r.initArrayBuffer(true)

	// %TypedArray%
	taProto := r.NewObject()
	r.TypedArrayPrototype = taProto
	ta := r.newNativeConstructor("TypedArray", 0, func(c *Call) Value {
		vm.throwTypeError("Abstract class TypedArray not directly constructable")
		return Undefined
	})
	ta.putDirect(StrKey("prototype"), ObjectValue(taProto), 0)
	taProto.putDirect(StrKey("constructor"), ObjectValue(ta), BuiltinFlags)
	r.TypedArrayConstructor = ta
	r.speciesGetter(ta)
	r.initTypedArrayStatics(ta)
	r.initTypedArrayPrototype(taProto)

	for k := kindInt8; k <= kindBigUint64; k++ {
		kind := k
		proto := r.newObject(taProto)
		r.TypedArrayPrototypes[kind] = proto
		c := r.constructor(kind.String(), 3, func(c *Call) Value {
			if c.NewTarget == nil {
				vm.throwTypeError("%s constructor requires 'new'", kind)
			}
			return ObjectValue(vm.constructTypedArray(c, kind))
		}, proto)
		c.proto = ta
		c.putDirect(StrKey("BYTES_PER_ELEMENT"), Int(kind.size()), 0)
		proto.putDirect(StrKey("BYTES_PER_ELEMENT"), Int(kind.size()), 0)
		r.TypedArrayConstructors[kind] = c
	}
	r.initUint8ArrayBase64()
	r.initDataView()
	r.initAtomics()
}

func (r *Realm) initArrayBuffer(shared bool) {
	vm := r.vm
	proto := r.NewObject()
	name := "ArrayBuffer"
	if shared {
		name = "SharedArrayBuffer"
		r.SharedArrayBufferPrototype = proto
	} else {
		r.ArrayBufferPrototype = proto
	}
	c := r.constructor(name, 1, func(c *Call) Value {
		if c.NewTarget == nil {
			vm.throwTypeError("%s constructor requires 'new'", name)
		}
		length := vm.ToIndex(c.Arg(0))
		maxLength := int64(-1)
		if opts := c.Arg(1).AsObject(); opts != nil {
			if m := opts.GetStr("maxByteLength"); !m.IsUndefined() {
				maxLength = vm.ToIndex(m)
			}
		}
		return ObjectValue(vm.allocateArrayBuffer(c.NewTarget, length, maxLength, shared))
	}, proto)
	r.speciesGetter(c)
	r.toStringTag(proto, name)
	if !shared {
		r.method(c, "isView", 1, func(c *Call) Value {
			o := c.Arg(0).AsObject()
			return Bool(o != nil && (o.class == ClassTypedArray || o.class == ClassDataView))
		})
	}
	r.getter(proto, StrKey("byteLength"), func(c *Call) Value {
		return Int(vm.thisBuffer(c, name+".prototype.byteLength", shared).byteLength())
	})
	r.getter(proto, StrKey("maxByteLength"), func(c *Call) Value {
		b := vm.thisBuffer(c, name+".prototype.maxByteLength", shared)
		if b.detached {
			return Int(0)
		}
		if b.resizable {
			return Int(b.maxLength)
		}
		return Int(len(b.data))
	})
	resizableName := "resizable"
	if shared {
		resizableName = "growable"
	}
	r.getter(proto, StrKey(resizableName), func(c *Call) Value {
		return Bool(vm.thisBuffer(c, name+".prototype."+resizableName, shared).resizable)
	})
	if !shared {
		r.getter(proto, StrKey("detached"), func(c *Call) Value {
			return Bool(vm.thisBuffer(c, "ArrayBuffer.prototype.detached", false).detached)
		})
	}
	resizeName := "resize"
	if shared {
		resizeName = "grow"
	}
	r.method(proto, resizeName, 1, func(c *Call) Value {
		b := vm.thisBuffer(c, name+".prototype."+resizeName, shared)
		if !b.resizable {
			vm.throwTypeError("%s is not resizable", name)
		}
		newLength := vm.ToIndex(c.Arg(0))
		if b.detached {
			vm.throwTypeError("ArrayBuffer is detached")
		}
		if newLength > int64(b.maxLength) || shared && newLength < int64(len(b.data)) {
			vm.throwRangeError("Invalid length for %s", resizeName)
		}
		old := len(b.data)
		b.data = b.data[:newLength]
		if int(newLength) > old {
			clear(b.data[old:])
		}
		return Undefined
	})
	r.method(proto, "slice", 2, func(c *Call) Value {
		b := vm.thisBuffer(c, name+".prototype.slice", shared)
		if b.detached {
			vm.throwTypeError("ArrayBuffer is detached")
		}
		n := int64(len(b.data))
		first := vm.relativeIndex(c.Arg(0), n, 0)
		final := vm.relativeIndex(c.Arg(1), n, n)
		newLen := max(final-first, 0)
		defaultCtor := c.Realm().GlobalObject.GetStr(name).AsObject()
		ctor := vm.SpeciesConstructor(c.This.ObjectRef(), defaultCtor)
		newObj := vm.Construct(ctor, []Value{Number(float64(newLen))}, nil).AsObject()
		if newObj == nil || newObj.class != c.This.ObjectRef().class {
			vm.throwTypeError("Species constructor did not return an %s", name)
		}
		nb := bufferOf(newObj)
		if !shared && nb.detached {
			vm.throwTypeError("ArrayBuffer is detached")
		}
		if newObj == c.This.ObjectRef() {
			vm.throwTypeError("Species constructor returned the same buffer")
		}
		if int64(len(nb.data)) < newLen {
			vm.throwTypeError("Species constructor returned a too small buffer")
		}
		if b.detached {
			vm.throwTypeError("ArrayBuffer is detached")
		}
		cur := int64(len(b.data))
		if end := min(final, cur); first < end {
			copy(nb.data[:newLen], b.data[first:end])
		}
		return ObjectValue(newObj)
	})
	if !shared {
		transfer := func(name string, preserve bool) {
			r.method(proto, name, 0, func(c *Call) Value {
				b := vm.thisBuffer(c, "ArrayBuffer.prototype."+name, false)
				var newLength int64
				if l := c.Arg(0); l.IsUndefined() {
					newLength = int64(b.byteLength())
				} else {
					newLength = vm.ToIndex(l)
				}
				if b.detached {
					vm.throwTypeError("ArrayBuffer is detached")
				}
				maxLength := int64(-1)
				if preserve && b.resizable {
					maxLength = int64(b.maxLength)
				}
				ab := c.Realm().GlobalObject.GetStr("ArrayBuffer").AsObject()
				newObj := vm.allocateArrayBuffer(ab, newLength, maxLength, false)
				nb := bufferOf(newObj)
				if !b.detachKey.IsUndefined() {
					vm.throwTypeError("ArrayBuffer cannot be detached")
				}
				copy(nb.data, b.data)
				b.data, b.detached = nil, true
				vm.detachEpoch++ // views' vectors are stale
				return ObjectValue(newObj)
			})
		}
		transfer("transfer", true)
		transfer("transferToFixedLength", false)
	}
}

// --- %TypedArray% statics ----------------------------------------------------------------

// typedArrayCreate is TypedArrayCreateFromConstructor (23.2.4.2).
func (vm *VM) typedArrayCreate(c *Object, args []Value) (*Object, *typedArray) {
	o := vm.Construct(c, args, nil).AsObject()
	o, t, n := vm.validateTypedArray(ObjectValue(o), "TypedArray species")
	if len(args) == 1 && args[0].IsNumber() && float64(n) < args[0].Float() {
		vm.throwTypeError("TypedArray species constructor returned a too short array")
	}
	return o, t
}

// typedArraySpeciesCreate (23.2.4.1).
func (vm *VM) typedArraySpeciesCreate(exemplar *Object, args []Value) (*Object, *typedArray) {
	t := typedArrayOf(exemplar)
	def := vm.realm.TypedArrayConstructors[t.kind]
	c := vm.SpeciesConstructor(exemplar, def)
	o, nt := vm.typedArrayCreate(c, args)
	if nt.kind.isBigInt() != t.kind.isBigInt() {
		vm.throwTypeError("TypedArray species constructor returned an array of the wrong content type")
	}
	return o, nt
}

func (r *Realm) initTypedArrayStatics(ta *Object) {
	vm := r.vm
	r.method(ta, "from", 1, func(c *Call) Value {
		if !c.This.IsConstructor() {
			vm.throwTypeError("TypedArray.from requires that |this| be a constructor")
		}
		mapfn, thisArg := c.Arg(1), c.Arg(2)
		mapping := !mapfn.IsUndefined()
		if mapping && !mapfn.IsCallable() {
			vm.throwTypeError("TypedArray.from: the mapper must be a function")
		}
		source := c.Arg(0)
		var values []Value
		usingIterator := vm.GetMethod(source, SymKey(SymbolIterator))
		if !usingIterator.IsUndefined() {
			rec := vm.getIteratorFromMethod(source, usingIterator)
			for {
				v, ok := vm.step(rec)
				if !ok {
					break
				}
				values = append(values, v)
			}
		} else {
			o := vm.ToObject(source)
			n := vm.LengthOfArrayLike(o)
			target, t := vm.typedArrayCreate(c.This.ObjectRef(), []Value{Number(float64(n))})
			for k := int64(0); k < n; k++ {
				v := o.getIdx(k)
				if mapping {
					v = vm.Call(mapfn, thisArg, v, Number(float64(k)))
				}
				target.SetOrThrow(IndexKey(uint32(k)), v)
			}
			_ = t
			return ObjectValue(target)
		}
		target, _ := vm.typedArrayCreate(c.This.ObjectRef(), []Value{Int(len(values))})
		for k, v := range values {
			if mapping {
				v = vm.Call(mapfn, thisArg, v, Int(k))
			}
			target.SetOrThrow(IndexKey(uint32(k)), v)
		}
		return ObjectValue(target)
	})
	r.method(ta, "of", 0, func(c *Call) Value {
		if !c.This.IsConstructor() {
			vm.throwTypeError("TypedArray.of requires that |this| be a constructor")
		}
		target, _ := vm.typedArrayCreate(c.This.ObjectRef(), []Value{Int(len(c.Args))})
		for k, v := range c.Args {
			target.SetOrThrow(IndexKey(uint32(k)), v)
		}
		return ObjectValue(target)
	})
}

// constructTypedArray is the TypedArray constructor (23.2.5.1).
func (vm *VM) constructTypedArray(c *Call, kind typedArrayKind) *Object {
	// The prototype is read by AllocateTypedArray: after ToIndex for a
	// length argument, before anything else for an object argument.
	var proto *Object
	newTarget := c.NewTarget
	allocate := func() {
		if proto == nil {
			proto = vm.GetPrototypeFromConstructor(newTarget, func(r *Realm) *Object { return r.TypedArrayPrototypes[kind] })
		}
	}
	newTA := func(buffer *Object, byteOffset, length int) *Object {
		allocate()
		o := vm.realm.newObject(proto)
		o.class = ClassTypedArray
		o.exotic = typedArrayObject{}
		o.internal = vm.newTypedArrayData(kind, buffer, byteOffset, length)
		return o
	}
	size := kind.size()
	first := c.Arg(0)
	fo := first.AsObject()
	if fo == nil {
		n := vm.ToIndex(first)
		buffer := vm.newArrayBuffer(int(n) * size)
		return newTA(buffer, 0, int(n))
	}
	allocate()
	switch {
	case fo.class == ClassTypedArray:
		src := typedArrayOf(fo)
		if src.isOutOfBounds() {
			vm.throwTypeError("Source TypedArray is detached or out of bounds")
		}
		n := src.length()
		if src.kind.isBigInt() != kind.isBigInt() {
			vm.throwTypeError("Cannot mix BigInt and Number typed arrays")
		}
		buffer := vm.newArrayBuffer(n * size)
		o := newTA(buffer, 0, n)
		t := typedArrayOf(o)
		for i := 0; i < n; i++ {
			t.setElement(i, src.getElement(i))
		}
		return o
	case fo.class == ClassArrayBuffer || fo.class == ClassSharedArrayBuffer:
		offset := vm.ToIndex(c.Arg(1))
		if offset%int64(size) != 0 {
			vm.throwRangeError("Start offset of %s should be a multiple of %d", kind, size)
		}
		lengthArg := c.Arg(2)
		var newLength int64
		if !lengthArg.IsUndefined() {
			newLength = vm.ToIndex(lengthArg)
		}
		b := bufferOf(fo)
		if b.detached {
			vm.throwTypeError("Buffer is detached")
		}
		bufLen := int64(len(b.data))
		if lengthArg.IsUndefined() && b.resizable {
			if offset > bufLen {
				vm.throwRangeError("Start offset is outside the bounds of the buffer")
			}
			return newTA(fo, int(offset), -1)
		}
		var byteLength int64
		if lengthArg.IsUndefined() {
			if bufLen%int64(size) != 0 {
				vm.throwRangeError("Byte length of %s should be a multiple of %d", kind, size)
			}
			byteLength = bufLen - offset
			if byteLength < 0 {
				vm.throwRangeError("Start offset is outside the bounds of the buffer")
			}
		} else {
			byteLength = newLength * int64(size)
			if offset+byteLength > bufLen {
				vm.throwRangeError("Length is out of range of the buffer")
			}
		}
		return newTA(fo, int(offset), int(byteLength/int64(size)))
	}
	// An iterable or array-like object.
	var values []Value
	usingIterator := vm.GetMethod(first, SymKey(SymbolIterator))
	if !usingIterator.IsUndefined() {
		rec := vm.getIteratorFromMethod(first, usingIterator)
		for {
			v, ok := vm.step(rec)
			if !ok {
				break
			}
			values = append(values, v)
		}
		buffer := vm.newArrayBuffer(len(values) * size)
		o := newTA(buffer, 0, len(values))
		for i, v := range values {
			o.SetOrThrow(IndexKey(uint32(i)), v)
		}
		return o
	}
	n := vm.LengthOfArrayLike(fo)
	if n*int64(size) > maxArrayBufferLength {
		vm.throwRangeError("Out of memory")
	}
	buffer := vm.newArrayBuffer(int(n) * size)
	o := newTA(buffer, 0, int(n))
	for i := int64(0); i < n; i++ {
		o.SetOrThrow(IndexKey(uint32(i)), fo.getIdx(i))
	}
	return o
}

// --- %TypedArray%.prototype --------------------------------------------------------------

func (r *Realm) initTypedArrayPrototype(proto *Object) {
	vm := r.vm
	thisTA := func(c *Call, name string) (*Object, *typedArray, int) {
		return vm.validateTypedArray(c.This, "%TypedArray%.prototype."+name)
	}
	plainTA := func(c *Call, name string) *typedArray {
		if o := c.This.AsObject(); o != nil {
			if t := typedArrayOf(o); t != nil {
				return t
			}
		}
		vm.throwTypeError("%%TypedArray%%.prototype.%s requires that |this| be a TypedArray", name)
		return nil
	}
	r.getter(proto, StrKey("buffer"), func(c *Call) Value { return ObjectValue(plainTA(c, "buffer").buffer) })
	r.getter(proto, StrKey("byteLength"), func(c *Call) Value { return Int(plainTA(c, "byteLength").byteLength()) })
	r.getter(proto, StrKey("byteOffset"), func(c *Call) Value {
		t := plainTA(c, "byteOffset")
		if t.isOutOfBounds() {
			return Int(0)
		}
		return Int(t.byteOffset)
	})
	r.typedArrayLength = r.getter(proto, StrKey("length"), func(c *Call) Value { return Int(plainTA(c, "length").length()) })
	r.getter(proto, SymKey(SymbolToStringTag), func(c *Call) Value {
		if o := c.This.AsObject(); o != nil {
			if t := typedArrayOf(o); t != nil {
				return Str(t.kind.String())
			}
		}
		return Undefined
	})

	r.method(proto, "at", 1, func(c *Call) Value {
		o, _, n := thisTA(c, "at")
		rel := vm.ToIntegerOrInfinity(c.Arg(0))
		k := rel
		if rel < 0 {
			k = float64(n) + rel
		}
		if k < 0 || k >= float64(n) {
			return Undefined
		}
		return o.GetKey(IndexKey(uint32(k)))
	})
	r.method(proto, "copyWithin", 2, func(c *Call) Value {
		o, t, n := thisTA(c, "copyWithin")
		to := vm.relativeIndex(c.Arg(0), int64(n), 0)
		from := vm.relativeIndex(c.Arg(1), int64(n), 0)
		final := vm.relativeIndex(c.Arg(2), int64(n), int64(n))
		count := min(final-from, int64(n)-to)
		if count > 0 {
			if t.isOutOfBounds() {
				vm.throwTypeError("TypedArray is detached or out of bounds")
			}
			n2 := int64(t.length())
			size := int64(t.kind.size())
			byteOffset := int64(t.byteOffset)
			bufferByteLimit := n2*size + byteOffset
			toByte := to*size + byteOffset
			fromByte := from*size + byteOffset
			countBytes := count * size
			countBytes = min(countBytes, bufferByteLimit-fromByte, bufferByteLimit-toByte)
			if countBytes > 0 {
				data := t.buf().data
				copy(data[toByte:toByte+countBytes], data[fromByte:fromByte+countBytes])
			}
		}
		return ObjectValue(o)
	})
	r.method(proto, "entries", 0, func(c *Call) Value {
		o, _, _ := thisTA(c, "entries")
		return ObjectValue(c.Realm().newArrayIterator(o, iterateEntries))
	})
	r.method(proto, "keys", 0, func(c *Call) Value {
		o, _, _ := thisTA(c, "keys")
		return ObjectValue(c.Realm().newArrayIterator(o, iterateKeys))
	})
	values := r.method(proto, "values", 0, func(c *Call) Value {
		o, _, _ := thisTA(c, "values")
		return ObjectValue(c.Realm().newArrayIterator(o, iterateValues))
	})
	proto.putDirect(SymKey(SymbolIterator), ObjectValue(values), BuiltinFlags)

	callback := func(c *Call, name string) Value {
		f := c.Arg(0)
		if !f.IsCallable() {
			vm.throwTypeError("%%TypedArray%%.prototype.%s callback must be a function", name)
		}
		return f
	}
	r.method(proto, "every", 1, func(c *Call) Value {
		o, _, n := thisTA(c, "every")
		f := callback(c, "every")
		for k := 0; k < n; k++ {
			if !vm.Call(f, c.Arg(1), o.GetKey(IndexKey(uint32(k))), Int(k), ObjectValue(o)).ToBoolean() {
				return False
			}
		}
		return True
	})
	r.method(proto, "some", 1, func(c *Call) Value {
		o, _, n := thisTA(c, "some")
		f := callback(c, "some")
		for k := 0; k < n; k++ {
			if vm.Call(f, c.Arg(1), o.GetKey(IndexKey(uint32(k))), Int(k), ObjectValue(o)).ToBoolean() {
				return True
			}
		}
		return False
	})
	r.method(proto, "forEach", 1, func(c *Call) Value {
		o, _, n := thisTA(c, "forEach")
		f := callback(c, "forEach")
		for k := 0; k < n; k++ {
			vm.Call(f, c.Arg(1), o.GetKey(IndexKey(uint32(k))), Int(k), ObjectValue(o))
		}
		return Undefined
	})
	r.method(proto, "fill", 1, func(c *Call) Value {
		o, t, n := thisTA(c, "fill")
		v := vm.toTypedValue(t.kind, c.Arg(0))
		k := vm.relativeIndex(c.Arg(1), int64(n), 0)
		final := vm.relativeIndex(c.Arg(2), int64(n), int64(n))
		if t.isOutOfBounds() {
			vm.throwTypeError("TypedArray is detached or out of bounds")
		}
		final = min(final, int64(t.length()))
		for ; k < final; k++ {
			t.setElement(int(k), v)
		}
		return ObjectValue(o)
	})
	r.method(proto, "filter", 1, func(c *Call) Value {
		o, _, n := thisTA(c, "filter")
		f := callback(c, "filter")
		var kept []Value
		for k := 0; k < n; k++ {
			v := o.GetKey(IndexKey(uint32(k)))
			if vm.Call(f, c.Arg(1), v, Int(k), ObjectValue(o)).ToBoolean() {
				kept = append(kept, v)
			}
		}
		a, _ := vm.typedArraySpeciesCreate(o, []Value{Int(len(kept))})
		for i, v := range kept {
			a.SetOrThrow(IndexKey(uint32(i)), v)
		}
		return ObjectValue(a)
	})
	find := func(name string, fromEnd, wantIndex bool) {
		r.method(proto, name, 1, func(c *Call) Value {
			o, _, n := thisTA(c, name)
			f := callback(c, name)
			for i := 0; i < n; i++ {
				k := i
				if fromEnd {
					k = n - 1 - i
				}
				v := o.GetKey(IndexKey(uint32(k)))
				if vm.Call(f, c.Arg(1), v, Int(k), ObjectValue(o)).ToBoolean() {
					if wantIndex {
						return Int(k)
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
	find("find", false, false)
	find("findIndex", false, true)
	find("findLast", true, false)
	find("findLastIndex", true, true)
	r.method(proto, "includes", 1, func(c *Call) Value {
		o, _, n := thisTA(c, "includes")
		if n == 0 {
			return False
		}
		k := vm.relativeIndex(c.Arg(1), int64(n), 0)
		for ; k < int64(n); k++ {
			if SameValueZero(o.GetKey(IndexKey(uint32(k))), c.Arg(0)) {
				return True
			}
		}
		return False
	})
	r.method(proto, "indexOf", 1, func(c *Call) Value {
		o, _, n := thisTA(c, "indexOf")
		if n == 0 {
			return Int(-1)
		}
		k := vm.relativeIndex(c.Arg(1), int64(n), 0)
		for ; k < int64(n); k++ {
			key := IndexKey(uint32(k))
			if o.HasProperty(key) && StrictEquals(o.GetKey(key), c.Arg(0)) {
				return Number(float64(k))
			}
		}
		return Int(-1)
	})
	r.method(proto, "lastIndexOf", 1, func(c *Call) Value {
		o, _, n := thisTA(c, "lastIndexOf")
		if n == 0 {
			return Int(-1)
		}
		k := int64(n - 1)
		if len(c.Args) > 1 {
			rel := vm.ToIntegerOrInfinity(c.Args[1])
			if rel >= 0 {
				k = int64(math.Min(rel, float64(n-1)))
			} else {
				k = int64(float64(n) + rel)
			}
		}
		for ; k >= 0; k-- {
			key := IndexKey(uint32(k))
			if o.HasProperty(key) && StrictEquals(o.GetKey(key), c.Arg(0)) {
				return Number(float64(k))
			}
		}
		return Int(-1)
	})
	r.method(proto, "join", 1, func(c *Call) Value {
		o, t, n := thisTA(c, "join")
		sep := NewString(",")
		if s := c.Arg(0); !s.IsUndefined() {
			sep = vm.ToString(s)
		}
		var b stringBuilder
		for k := 0; k < n; k++ {
			if k > 0 {
				b.WriteString(sep)
			}
			if k < t.length() {
				v := o.GetKey(IndexKey(uint32(k)))
				if !v.IsUndefined() {
					b.WriteString(vm.ToString(v))
				}
			}
		}
		return StringValue(b.String())
	})
	r.method(proto, "map", 1, func(c *Call) Value {
		o, _, n := thisTA(c, "map")
		f := callback(c, "map")
		a, _ := vm.typedArraySpeciesCreate(o, []Value{Int(n)})
		for k := 0; k < n; k++ {
			v := vm.Call(f, c.Arg(1), o.GetKey(IndexKey(uint32(k))), Int(k), ObjectValue(o))
			a.SetOrThrow(IndexKey(uint32(k)), v)
		}
		return ObjectValue(a)
	})
	reduce := func(name string, right bool) {
		r.method(proto, name, 1, func(c *Call) Value {
			o, _, n := thisTA(c, name)
			f := callback(c, name)
			k, step, end := 0, 1, n
			if right {
				k, step, end = n-1, -1, -1
			}
			var acc Value
			if len(c.Args) >= 2 {
				acc = c.Args[1]
			} else {
				if n == 0 {
					vm.throwTypeError("Reduce of empty array with no initial value")
				}
				acc = o.GetKey(IndexKey(uint32(k)))
				k += step
			}
			for ; k != end; k += step {
				acc = vm.Call(f, Undefined, acc, o.GetKey(IndexKey(uint32(k))), Int(k), ObjectValue(o))
			}
			return acc
		})
	}
	reduce("reduce", false)
	reduce("reduceRight", true)
	r.method(proto, "reverse", 0, func(c *Call) Value {
		o, t, n := thisTA(c, "reverse")
		for lower, upper := 0, n-1; lower < upper; lower, upper = lower+1, upper-1 {
			lv, uv := t.getElement(lower), t.getElement(upper)
			t.setElement(lower, uv)
			t.setElement(upper, lv)
		}
		return ObjectValue(o)
	})
	r.method(proto, "set", 1, func(c *Call) Value {
		this := c.This
		_, t := func() (*Object, *typedArray) {
			o := this.AsObject()
			if o == nil || typedArrayOf(o) == nil {
				vm.throwTypeError("%%TypedArray%%.prototype.set requires that |this| be a TypedArray")
			}
			return o, typedArrayOf(o)
		}()
		offset := vm.ToIntegerOrInfinity(c.Arg(1))
		if offset < 0 {
			vm.throwRangeError("offset must be >= 0")
		}
		src := c.Arg(0)
		if so := src.AsObject(); so != nil && typedArrayOf(so) != nil {
			st := typedArrayOf(so)
			if t.isOutOfBounds() || st.isOutOfBounds() {
				vm.throwTypeError("TypedArray is detached or out of bounds")
			}
			targetLen, srcLen := t.length(), st.length()
			if math.IsInf(offset, 1) || srcLen+int(offset) > targetLen {
				vm.throwRangeError("offset is out of bounds")
			}
			if t.kind.isBigInt() != st.kind.isBigInt() {
				vm.throwTypeError("Cannot mix BigInt and Number typed arrays")
			}
			if st.kind == t.kind {
				// Same type: copy the bytes (preserving NaN payloads); copy
				// handles overlapping views of one buffer like memmove.
				size := t.kind.size()
				dst := t.buf().data[t.byteOffset+int(offset)*size:]
				copy(dst[:srcLen*size], st.buf().data[st.byteOffset:st.byteOffset+srcLen*size])
				return Undefined
			}
			vals := make([]Value, srcLen)
			for i := range vals {
				vals[i] = st.getElement(i)
			}
			for i, v := range vals {
				t.setElement(int(offset)+i, v)
			}
			return Undefined
		}
		if t.isOutOfBounds() {
			vm.throwTypeError("TypedArray is detached or out of bounds")
		}
		targetLen := t.length()
		so := vm.ToObject(src)
		srcLen := vm.LengthOfArrayLike(so)
		if math.IsInf(offset, 1) || srcLen+int64(offset) > int64(targetLen) {
			vm.throwRangeError("offset is out of bounds")
		}
		for i := int64(0); i < srcLen; i++ {
			v := so.getIdx(i)
			vm.typedArraySetElement(t, offset+float64(i), v)
		}
		return Undefined
	})
	r.method(proto, "slice", 2, func(c *Call) Value {
		o, t, n := thisTA(c, "slice")
		start := vm.relativeIndex(c.Arg(0), int64(n), 0)
		final := vm.relativeIndex(c.Arg(1), int64(n), int64(n))
		count := max(final-start, 0)
		a, at := vm.typedArraySpeciesCreate(o, []Value{Number(float64(count))})
		if count > 0 {
			if t.isOutOfBounds() {
				vm.throwTypeError("TypedArray is detached or out of bounds")
			}
			final = min(final, int64(t.length()))
			if at.kind == t.kind {
				size := int64(t.kind.size())
				srcByte := int64(t.byteOffset) + start*size
				targetByte := int64(at.byteOffset)
				limit := targetByte + min(max(final-start, 0)*size, at.byteLength64())
				src, dst := t.buf().data, at.buf().data
				for targetByte < limit && srcByte < int64(len(src)) {
					dst[targetByte] = src[srcByte]
					srcByte++
					targetByte++
				}
			} else {
				nIdx := 0
				for k := start; k < final; k++ {
					a.SetOrThrow(IndexKey(uint32(nIdx)), o.GetKey(IndexKey(uint32(k))))
					nIdx++
				}
			}
		}
		return ObjectValue(a)
	})
	r.method(proto, "sort", 1, func(c *Call) Value {
		cmp := c.Arg(0)
		if !cmp.IsUndefined() && !cmp.IsCallable() {
			vm.throwTypeError("%%TypedArray%%.prototype.sort requires the comparator argument to be a function or undefined")
		}
		o, t, n := thisTA(c, "sort")
		items := make([]Value, n)
		for i := range items {
			items[i] = t.getElement(i)
		}
		vm.sortValues(items, func(x, y Value) int { return vm.compareTypedArrayElements(x, y, cmp) })
		for i, v := range items {
			if i < t.length() {
				t.setElement(i, v)
			}
		}
		return ObjectValue(o)
	})
	r.method(proto, "toSorted", 1, func(c *Call) Value {
		cmp := c.Arg(0)
		if !cmp.IsUndefined() && !cmp.IsCallable() {
			vm.throwTypeError("%%TypedArray%%.prototype.toSorted requires the comparator argument to be a function or undefined")
		}
		_, t, n := thisTA(c, "toSorted")
		a := vm.newTypedArrayOfKind(t.kind, n)
		at := typedArrayOf(a)
		items := make([]Value, n)
		for i := range items {
			items[i] = t.getElement(i)
		}
		vm.sortValues(items, func(x, y Value) int { return vm.compareTypedArrayElements(x, y, cmp) })
		for i, v := range items {
			at.setElement(i, v)
		}
		return ObjectValue(a)
	})
	r.method(proto, "toReversed", 0, func(c *Call) Value {
		_, t, n := thisTA(c, "toReversed")
		a := vm.newTypedArrayOfKind(t.kind, n)
		at := typedArrayOf(a)
		for i := 0; i < n; i++ {
			at.setElement(i, t.getElement(n-1-i))
		}
		return ObjectValue(a)
	})
	r.method(proto, "with", 2, func(c *Call) Value {
		_, t, n := thisTA(c, "with")
		rel := vm.ToIntegerOrInfinity(c.Arg(0))
		actual := rel
		if rel < 0 {
			actual = float64(n) + rel
		}
		v := vm.toTypedValue(t.kind, c.Arg(1))
		if !t.isValidIndex(actual) {
			vm.throwRangeError("%%TypedArray%%.prototype.with index out of range")
		}
		a := vm.newTypedArrayOfKind(t.kind, n)
		at := typedArrayOf(a)
		for k := 0; k < n; k++ {
			key := IndexKey(uint32(k))
			if k == int(actual) {
				a.SetOrThrow(key, v)
			} else {
				a.SetOrThrow(key, c.This.ObjectRef().GetKey(key))
			}
		}
		_ = at
		return ObjectValue(a)
	})
	r.method(proto, "subarray", 2, func(c *Call) Value {
		o := c.This.AsObject()
		var t *typedArray
		if o != nil {
			t = typedArrayOf(o)
		}
		if t == nil {
			vm.throwTypeError("%%TypedArray%%.prototype.subarray requires that |this| be a TypedArray")
		}
		srcLen := int64(t.length())
		begin := vm.relativeIndex(c.Arg(0), srcLen, 0)
		size := int64(t.kind.size())
		beginByteOffset := int64(t.byteOffset) + begin*size
		var args []Value
		if t.arrayLength < 0 && c.Arg(1).IsUndefined() {
			args = []Value{ObjectValue(t.buffer), Number(float64(beginByteOffset))}
		} else {
			end := vm.relativeIndex(c.Arg(1), srcLen, srcLen)
			newLength := max(end-begin, 0)
			args = []Value{ObjectValue(t.buffer), Number(float64(beginByteOffset)), Number(float64(newLength))}
		}
		def := c.Realm().TypedArrayConstructors[t.kind]
		ctor := vm.SpeciesConstructor(o, def)
		a, at := vm.typedArrayCreate(ctor, args)
		if at.kind.isBigInt() != t.kind.isBigInt() {
			vm.throwTypeError("TypedArray species constructor returned an array of the wrong content type")
		}
		return ObjectValue(a)
	})
	r.method(proto, "toLocaleString", 0, func(c *Call) Value {
		o, _, n := thisTA(c, "toLocaleString")
		var b stringBuilder
		for k := 0; k < n; k++ {
			if k > 0 {
				b.WriteUnit(',')
			}
			e := o.GetKey(IndexKey(uint32(k)))
			if !e.IsNullish() {
				b.WriteString(vm.ToString(vm.Invoke(e, StrKey("toLocaleString"))))
			}
		}
		return StringValue(b.String())
	})
	proto.putDirect(StrKey("toString"), r.ArrayPrototype.GetStr("toString"), BuiltinFlags)
}

func (t *typedArray) byteLength64() int64 { return int64(t.byteLength()) }

func (vm *VM) newTypedArrayOfKind(kind typedArrayKind, n int) *Object {
	r := vm.realm
	o := r.newObject(r.TypedArrayPrototypes[kind])
	o.class = ClassTypedArray
	o.exotic = typedArrayObject{}
	o.internal = vm.newTypedArrayData(kind, vm.newArrayBuffer(n*kind.size()), 0, n)
	return o
}

// compareTypedArrayElements (23.2.4.7).
func (vm *VM) compareTypedArrayElements(x, y Value, cmp Value) int {
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
	if x.IsBigInt() {
		return x.BigIntRef().Cmp(y.BigIntRef())
	}
	a, b := x.Float(), y.Float()
	switch {
	case math.IsNaN(a) && math.IsNaN(b):
		return 0
	case math.IsNaN(a):
		return 1
	case math.IsNaN(b):
		return -1
	case a < b:
		return -1
	case a > b:
		return 1
	case a == 0 && b == 0:
		if math.Signbit(a) && !math.Signbit(b) {
			return -1
		}
		if !math.Signbit(a) && math.Signbit(b) {
			return 1
		}
	}
	return 0
}

// --- DataView ------------------------------------------------------------------------------

type dataView struct {
	buffer     *Object
	byteOffset int
	byteLength int // -1: length-tracking
}

func (d *dataView) buf() *arrayBufferData { return bufferOf(d.buffer) }

func (d *dataView) isOutOfBounds() bool {
	b := d.buf()
	if b.detached {
		return true
	}
	if d.byteOffset > len(b.data) {
		return true
	}
	return d.byteLength >= 0 && d.byteOffset+d.byteLength > len(b.data)
}

func (d *dataView) viewByteLength() int {
	if d.byteLength >= 0 {
		return d.byteLength
	}
	return len(d.buf().data) - d.byteOffset
}

func (r *Realm) initDataView() {
	vm := r.vm
	proto := r.NewObject()
	r.DataViewPrototype = proto
	r.constructor("DataView", 1, func(c *Call) Value {
		if c.NewTarget == nil {
			vm.throwTypeError("DataView constructor requires 'new'")
		}
		bo := c.Arg(0).AsObject()
		if bo == nil || bo.class != ClassArrayBuffer && bo.class != ClassSharedArrayBuffer {
			vm.throwTypeError("DataView requires an ArrayBuffer")
		}
		offset := vm.ToIndex(c.Arg(1))
		b := bufferOf(bo)
		if b.detached {
			vm.throwTypeError("Buffer is detached")
		}
		bufLen := int64(len(b.data))
		if offset > bufLen {
			vm.throwRangeError("Start offset is outside the bounds of the buffer")
		}
		lengthArg := c.Arg(2)
		viewLen := int64(-1)
		if lengthArg.IsUndefined() {
			if !b.resizable {
				viewLen = bufLen - offset
			}
		} else {
			viewLen = vm.ToIndex(lengthArg)
			if offset+viewLen > bufLen {
				vm.throwRangeError("Length is out of range of the buffer")
			}
		}
		o := vm.OrdinaryCreateFromConstructor(c.NewTarget, func(r *Realm) *Object { return r.DataViewPrototype })
		if b.detached {
			vm.throwTypeError("Buffer is detached")
		}
		bufLen = int64(len(b.data))
		if offset > bufLen || viewLen >= 0 && offset+viewLen > bufLen {
			vm.throwRangeError("Length is out of range of the buffer")
		}
		o.class = ClassDataView
		o.internal = &dataView{buffer: bo, byteOffset: int(offset), byteLength: int(viewLen)}
		return ObjectValue(o)
	}, proto)
	r.toStringTag(proto, "DataView")
	thisView := func(c *Call, name string) *dataView {
		if o := c.This.AsObject(); o != nil && o.class == ClassDataView {
			return o.internal.(*dataView)
		}
		vm.throwTypeError("DataView.prototype.%s called on incompatible receiver", name)
		return nil
	}
	r.getter(proto, StrKey("buffer"), func(c *Call) Value { return ObjectValue(thisView(c, "buffer").buffer) })
	r.dataViewByteLength = r.getter(proto, StrKey("byteLength"), func(c *Call) Value {
		d := thisView(c, "byteLength")
		if d.isOutOfBounds() {
			vm.throwTypeError("DataView is detached or out of bounds")
		}
		return Int(d.viewByteLength())
	})
	r.getter(proto, StrKey("byteOffset"), func(c *Call) Value {
		d := thisView(c, "byteOffset")
		if d.isOutOfBounds() {
			vm.throwTypeError("DataView is detached or out of bounds")
		}
		return Int(d.byteOffset)
	})
	accessor := func(name string, kind typedArrayKind) {
		r.method(proto, "get"+name, 1, func(c *Call) Value {
			d := thisView(c, "get"+name)
			index := vm.ToIndex(c.Arg(0))
			little := c.Arg(1).ToBoolean()
			if d.isOutOfBounds() {
				vm.throwTypeError("DataView is detached or out of bounds")
			}
			size := int64(kind.size())
			if index+size > int64(d.viewByteLength()) {
				vm.throwRangeError("Out of bounds access")
			}
			off := int64(d.byteOffset) + index
			// (On the stack: async-fs makes 0.7 million a run.)
			var tmp [8]byte
			raw := tmp[:size]
			copy(raw, d.buf().data[off:off+size])
			if !little {
				slices.Reverse(raw)
			}
			return decodeElement(kind, raw)
		})
		r.method(proto, "set"+name, 2, func(c *Call) Value {
			d := thisView(c, "set"+name)
			index := vm.ToIndex(c.Arg(0))
			v := vm.toTypedValue(kind, c.Arg(1))
			little := c.Arg(2).ToBoolean()
			if d.isOutOfBounds() {
				vm.throwTypeError("DataView is detached or out of bounds")
			}
			size := int64(kind.size())
			if index+size > int64(d.viewByteLength()) {
				vm.throwRangeError("Out of bounds access")
			}
			var tmp [8]byte
			raw := tmp[:size]
			encodeElement(kind, raw, v)
			if !little {
				slices.Reverse(raw)
			}
			off := int64(d.byteOffset) + index
			copy(d.buf().data[off:off+size], raw)
			return Undefined
		})
	}
	accessor("Int8", kindInt8)
	accessor("Uint8", kindUint8)
	accessor("Int16", kindInt16)
	accessor("Uint16", kindUint16)
	accessor("Int32", kindInt32)
	accessor("Uint32", kindUint32)
	accessor("Float16", kindFloat16)
	accessor("Float32", kindFloat32)
	accessor("Float64", kindFloat64)
	accessor("BigInt64", kindBigInt64)
	accessor("BigUint64", kindBigUint64)
	_ = binary.LittleEndian
}
