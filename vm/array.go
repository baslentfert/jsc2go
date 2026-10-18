package vm

import "math"

// Array exotic objects (JSArray, ECMA-262 10.4.2). Elements live in the
// object's dense element vector or, when sparse or with unusual attributes,
// in its property map; the array keeps its length.

type arrayObject struct {
	ordinaryObject
	length         uint32
	lengthWritable bool
}

func (r *Realm) newArrayWithProto(values []Value, proto *Object) *Object {
	// The object and its arrayObject in one allocation (they fit the size
	// class of the object alone); an empty array also gets room for its
	// first elements, which most arrays made empty are pushed to.
	if values == nil {
		b := new(struct {
			o Object
			a arrayObject
			e [baseVectorLength]Value
		})
		b.a = arrayObject{lengthWritable: true}
		b.o = Object{realm: r, proto: proto, extensible: true, props: propertyMap{shape: r.vm.rootShape},
			class: ClassArray, elements: b.e[:0], exotic: &b.a}
		return &b.o
	}
	b := new(struct {
		o Object
		a arrayObject
	})
	b.a = arrayObject{length: uint32(len(values)), lengthWritable: true}
	b.o = Object{realm: r, proto: proto, extensible: true, props: propertyMap{shape: r.vm.rootShape},
		class: ClassArray, elements: values, exotic: &b.a}
	return &b.o
}

// NewArray creates an array holding values (taking ownership of the slice).
func (r *Realm) NewArray(values ...Value) *Object {
	return r.newArrayWithProto(values, r.ArrayPrototype)
}

// arrayCreate is ArrayCreate (10.4.2.2).
func (vm *VM) arrayCreate(length int64, proto *Object) *Object {
	if length > math.MaxUint32 {
		vm.throwRangeError("Array size is not a small enough positive integer.")
	}
	if proto == nil {
		proto = vm.realm.ArrayPrototype
	}
	o := vm.realm.newArrayWithProto(nil, proto)
	o.exotic.(*arrayObject).length = uint32(length)
	return o
}

// minArrayStorageConstructionLength is JSC's MIN_ARRAY_STORAGE_CONSTRUCTION_LENGTH:
// new Array(n) for a smaller n reserves its n elements (as holes), so
// that filling the array stores in place.
const minArrayStorageConstructionLength = 100000

// arrayWithHoles is ArrayCreate(n, proto) for new Array(n).
func (vm *VM) arrayWithHoles(n uint32, proto *Object) *Object {
	o := vm.arrayCreate(int64(n), proto)
	if n < minArrayStorageConstructionLength {
		o.elements = make([]Value, n)
		for i := range o.elements {
			o.elements[i] = Empty
		}
	}
	return o
}

func (o *Object) array() *arrayObject {
	a, _ := o.exotic.(*arrayObject)
	return a
}

// IsArray (7.2.2).
func (vm *VM) IsArray(v Value) bool {
	o := v.AsObject()
	for o != nil {
		if o.class == ClassArray {
			return true
		}
		if o.class != ClassProxy {
			return false
		}
		p := o.internal.(*proxyData)
		if p.handler == nil {
			vm.throwTypeError("Proxy has been revoked")
		}
		o = p.target
	}
	return false
}

func (a *arrayObject) lengthProperty() Property {
	flags := PropertyFlags(0)
	if a.lengthWritable {
		flags = Writable
	}
	return Property{Value: Number(float64(a.length)), Flags: flags}
}

func (a *arrayObject) getOwnProperty(o *Object, k PropertyKey) (Property, bool) {
	if k.sym == nil && k.name == lengthIdent {
		return a.lengthProperty(), true
	}
	return o.ordinaryGetOwnProperty(k)
}

func (a *arrayObject) defineOwnProperty(o *Object, k PropertyKey, d PropertyDescriptor) bool {
	if k.sym == nil {
		if k.name == lengthIdent {
			return a.setLength(o, d)
		}
		if i, ok := k.Index(); ok {
			if i >= a.length && !a.lengthWritable {
				return false
			}
			if !o.ordinaryDefineOwnProperty(k, d) {
				return false
			}
			if i >= a.length {
				a.length = i + 1
			}
			return true
		}
	}
	return o.ordinaryDefineOwnProperty(k, d)
}

// setLength is ArraySetLength (10.4.2.4).
func (a *arrayObject) setLength(o *Object, d PropertyDescriptor) bool {
	if d.Has&HasValue == 0 {
		p, ok := validateAndApply(a.lengthProperty(), d)
		if !ok {
			return false
		}
		a.lengthWritable = p.Writable()
		return true
	}
	vm := o.vm()
	newLen := vm.ToUint32(d.Value)
	if float64(newLen) != vm.ToNumber(d.Value) {
		vm.throwRangeError("Invalid array length")
	}
	d.Value = Number(float64(newLen))
	return a.setLengthValue(o, newLen, d)
}

func (a *arrayObject) setLengthValue(o *Object, newLen uint32, d PropertyDescriptor) bool {
	d.Has |= HasValue
	d.Value = Number(float64(newLen))
	if newLen >= a.length {
		p, ok := validateAndApply(a.lengthProperty(), d)
		if !ok {
			return false
		}
		a.length = newLen
		a.lengthWritable = p.Writable()
		return true
	}
	if !a.lengthWritable {
		return false
	}
	newWritable := d.Has&HasWritable == 0 || d.Writable
	if !newWritable {
		d.Writable = true
	}
	if _, ok := validateAndApply(a.lengthProperty(), d); !ok {
		return false
	}
	// Delete elements from the end, stopping at a non-configurable one:
	// everything above the highest non-configurable index goes.
	stop, blocked := newLen, false
	for i, p := range o.sparse() {
		if i >= newLen && !p.Configurable() && (!blocked || i+1 > stop) {
			stop, blocked = i+1, true
		}
	}
	if int64(len(o.elements)) > int64(stop) {
		clear(o.elements[stop:])
		o.elements = o.elements[:stop]
		o.trimElements()
	}
	for i := range o.sparse() {
		if i >= stop {
			delete(o.sparse(), i)
		}
	}
	a.length = stop
	if blocked {
		if !newWritable {
			a.lengthWritable = false
		}
		return false
	}
	if !newWritable {
		a.lengthWritable = false
	}
	return true
}

func (a *arrayObject) set(o *Object, k PropertyKey, v Value, receiver Value) bool {
	if receiver.AsObject() == o && k.sym == nil {
		if i, ok := k.Index(); ok && a.setIndexFast(o, i, v) {
			return true
		}
	}
	return o.ordinarySet(k, v, receiver)
}

// setIndexFast stores v at index i if that is a plain store to an existing
// element or an append; it reports whether it did.
func (a *arrayObject) setIndexFast(o *Object, i uint32, v Value) bool {
	if int64(i) < int64(len(o.elements)) {
		if !o.elements[i].IsEmpty() {
			o.elements[i] = v
			return true
		}
		// A hole: the prototype chain must not have the index either.
		if o.extensible && len(o.sparse()) == 0 {
			if p := o.proto; p == nil || !p.mayHaveIndexedProperties() {
				o.elements[i] = v
				return true
			}
		}
		return false
	}
	if i == a.length && int(i) == len(o.elements) && o.extensible && a.lengthWritable && len(o.sparse()) == 0 && i != math.MaxUint32 {
		// Appending: the prototype chain must not have the index (setters).
		if p := o.proto; p == nil || !p.mayHaveIndexedProperties() {
			o.elements = appendElements(o.elements, v)
			a.length++
			return true
		}
	}
	return false
}

// baseVectorLength is the capacity an array's elements get when the first
// ones are appended (JSC's BASE_CONTIGUOUS_VECTOR_LEN is 3; 4 Values fill
// Go's 64-byte size class): append alone grows them 1, 2, 4, one
// allocation each.
const baseVectorLength = 4

// appendElements is append for an array's elements, starting at
// baseVectorLength.
func appendElements(elements []Value, vs ...Value) []Value {
	if cap(elements) == 0 && len(vs) < baseVectorLength {
		elements = make([]Value, 0, baseVectorLength)
	}
	return append(elements, vs...)
}

// mayHaveIndexedProperties reports whether o or its prototypes may have
// index properties (conservatively).
func (o *Object) mayHaveIndexedProperties() bool {
	for p := o; p != nil; p = p.proto {
		if !p.hasOrdinaryIndexes() || len(p.elements) > 0 || len(p.sparse()) > 0 {
			return true
		}
	}
	return false
}

func (a *arrayObject) delete(o *Object, k PropertyKey) bool {
	if k.sym == nil && k.name == lengthIdent {
		return false
	}
	return o.ordinaryDelete(k)
}

func (a *arrayObject) ownKeys(o *Object) []PropertyKey {
	keys := o.ordinaryOwnKeys()
	// Insert "length" after the indices.
	n := 0
	for n < len(keys) {
		if _, ok := keys[n].Index(); !ok {
			break
		}
		n++
	}
	keys = append(keys, PropertyKey{})
	copy(keys[n+1:], keys[n:])
	keys[n] = PropertyKey{name: lengthIdent}
	return keys
}

// arrayPush appends v to an array object created by the engine.
func (o *Object) arrayPush(v Value) {
	a := o.array()
	if a != nil && a.setIndexFast(o, a.length, v) {
		return
	}
	n := o.vm().LengthOfArrayLike(o)
	o.CreateDataPropertyOrThrow(IndexKey(uint32(n)), v)
}

// denseArray returns o's array state if all of o's index properties live in
// o.elements, its length is writable and its prototypes have no index
// properties: then reads, writes and deletes of its indexes run no user code
// and a hole reads as undefined, so builtins may work on o.elements directly.
func (o *Object) denseArray() *arrayObject {
	a := o.array()
	if a == nil || !a.lengthWritable || len(o.sparse()) != 0 || int64(len(o.elements)) != int64(a.length) {
		return nil
	}
	if p := o.proto; p != nil && p.mayHaveIndexedProperties() {
		return nil
	}
	return a
}

// fastIndex reads own element i of an ordinary or array object without
// side effects; ok is false if the slow path is needed.
func (o *Object) fastIndex(i uint32) (Value, bool) {
	if int64(i) < int64(len(o.elements)) {
		if v := o.elements[i]; !v.IsEmpty() {
			if o.exotic == nil {
				return v, true
			}
			if _, isArray := o.exotic.(*arrayObject); isArray {
				return v, true
			}
		}
	}
	return Undefined, false
}
