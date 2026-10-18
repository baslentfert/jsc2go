package vm

import "slices"

// Objects (JSObject, JSFinalObject and the internal methods of ECMA-262
// 10.1). JSC's Structures, butterflies and inline caches are replaced by an
// ordered property map plus a dense element vector; exotic objects (arrays,
// proxies, arguments, ...) override internal methods through the exotic
// interface.

// PropertyFlags are the attributes of a property.
type PropertyFlags uint8

const (
	Writable PropertyFlags = 1 << iota
	Enumerable
	Configurable
	// accessorFlag marks accessor properties; Value then holds an *Accessor.
	accessorFlag
	// lazyFlag marks a function's prototype property that is not made yet
	// (its value is empty): reifyPrototype makes it when it is looked at.
	// Inline caches never cache such a property, and code that treats
	// properties without accessorFlag as plain data must test both.
	lazyFlag

	// DefaultFlags are the attributes of properties created by assignment.
	DefaultFlags = Writable | Enumerable | Configurable
	// BuiltinFlags are the attributes of built-in methods.
	BuiltinFlags = Writable | Configurable
)

// Accessor is the getter/setter pair of an accessor property.
type Accessor struct {
	Get, Set *Object // nil: undefined
}

// Property is a stored property: a data value or an accessor pair.
type Property struct {
	Value Value
	Flags PropertyFlags
}

func (p Property) IsAccessor() bool   { return p.Flags&accessorFlag != 0 }
func (p Property) Writable() bool     { return p.Flags&Writable != 0 }
func (p Property) Enumerable() bool   { return p.Flags&Enumerable != 0 }
func (p Property) Configurable() bool { return p.Flags&Configurable != 0 }
func (p Property) Accessor() *Accessor {
	p.Value.check(tagAccessor)
	return (*Accessor)(p.Value.p)
}

func dataProperty(v Value, flags PropertyFlags) Property { return Property{Value: v, Flags: flags} }

func accessorProperty(get, set *Object, flags PropertyFlags) Property {
	return Property{Value: cellValue(&Accessor{Get: get, Set: set}), Flags: flags&^Writable | accessorFlag}
}

// getValue reads the property for receiver (calling a getter).
func (p Property) getValue(receiver Value) Value {
	if p.Flags&accessorFlag == 0 {
		return p.Value
	}
	g := p.Accessor().Get
	if g == nil {
		return Undefined
	}
	return g.vm().Call(ObjectValue(g), receiver)
}

// --- Property descriptors ------------------------------------------------------------

// DescriptorFields says which fields of a PropertyDescriptor are present.
type DescriptorFields uint8

const (
	HasValue DescriptorFields = 1 << iota
	HasGet
	HasSet
	HasWritable
	HasEnumerable
	HasConfigurable
)

// PropertyDescriptor is the specification type (6.2.6), with presence bits.
type PropertyDescriptor struct {
	Value        Value
	Get, Set     *Object // nil means undefined when HasGet/HasSet
	Writable     bool
	Enumerable   bool
	Configurable bool
	Has          DescriptorFields
}

func (d *PropertyDescriptor) IsAccessorDescriptor() bool { return d.Has&(HasGet|HasSet) != 0 }
func (d *PropertyDescriptor) IsDataDescriptor() bool     { return d.Has&(HasValue|HasWritable) != 0 }
func (d *PropertyDescriptor) IsGenericDescriptor() bool {
	return !d.IsAccessorDescriptor() && !d.IsDataDescriptor()
}

// DataDescriptor returns a complete data descriptor.
func DataDescriptor(v Value, flags PropertyFlags) PropertyDescriptor {
	return PropertyDescriptor{Value: v, Writable: flags&Writable != 0, Enumerable: flags&Enumerable != 0,
		Configurable: flags&Configurable != 0, Has: HasValue | HasWritable | HasEnumerable | HasConfigurable}
}

// AccessorDescriptor returns a complete accessor descriptor.
func AccessorDescriptor(get, set *Object, flags PropertyFlags) PropertyDescriptor {
	return PropertyDescriptor{Get: get, Set: set, Enumerable: flags&Enumerable != 0,
		Configurable: flags&Configurable != 0, Has: HasGet | HasSet | HasEnumerable | HasConfigurable}
}

func descriptorFromProperty(p Property) PropertyDescriptor {
	if p.IsAccessor() {
		a := p.Accessor()
		return AccessorDescriptor(a.Get, a.Set, p.Flags)
	}
	return DataDescriptor(p.Value, p.Flags)
}

func (d *PropertyDescriptor) flags() PropertyFlags {
	var f PropertyFlags
	if d.Writable {
		f |= Writable
	}
	if d.Enumerable {
		f |= Enumerable
	}
	if d.Configurable {
		f |= Configurable
	}
	return f
}

// --- Objects -------------------------------------------------------------------------

// Class identifies the kind of built-in object, for brand checks and for
// Object.prototype.toString.
type Class uint8

const (
	ClassObject Class = iota
	ClassArray
	ClassFunction
	ClassError
	ClassBoolean
	ClassNumber
	ClassString
	ClassSymbol
	ClassBigInt
	ClassDate
	ClassRegExp
	ClassArguments
	ClassMap
	ClassSet
	ClassWeakMap
	ClassWeakSet
	ClassWeakRef
	ClassFinalizationRegistry
	ClassPromise
	ClassProxy
	ClassGenerator
	ClassAsyncGenerator
	ClassAsyncFunctionGenerator
	ClassArrayBuffer
	ClassSharedArrayBuffer
	ClassDataView
	ClassTypedArray
	ClassIterator // built-in iterators (array, map, set, string, regexp string, helpers)
	ClassModuleNamespace
	ClassGlobal
	ClassDisposableStack
	ClassAsyncDisposableStack
	ClassShadowRealm
)

// Object is a JavaScript object.
type Object struct {
	realm *Realm
	proto *Object
	props propertyMap
	// elements holds index properties that are plain writable, enumerable,
	// configurable data properties; Empty marks a hole. Other index
	// properties (far apart, or with other attributes) live in
	// extra.sparse.
	elements []Value
	// exotic overrides internal methods; nil for ordinary objects.
	exotic exoticObject
	// fn makes the object callable.
	fn functionImpl
	// internal holds class-specific state ([[BooleanData]], map storage, ...).
	internal any
	// extra holds state few objects need, keeping Object small.
	extra      *objectExtra
	extensible bool
	class      Class
	// htmlDDA marks an [[IsHTMLDDA]] object (Annex B.3.6, document.all).
	htmlDDA bool
	// watched marks an object a cache case walked through as a prototype:
	// changes of its layout bump the VM's protoEpoch (layoutChanged).
	watched bool
}

// layoutChanged is called after o's shape, its shape's version or its
// prototype changed: when caches rely on o, their epoch is no longer
// current.
func (o *Object) layoutChanged() {
	if o.watched {
		o.realm.vm.protoEpoch++
	}
}

// setOwn sets named own property k of o (props.set), bumping the epoch
// when a watched object's layout changes.
func (o *Object) setOwn(k PropertyKey, p Property) {
	if !o.watched {
		o.props.set(k, p)
		return
	}
	s := o.props.shape
	var v uint32
	if s != nil {
		v = s.version
	}
	o.props.set(k, p)
	if o.props.shape != s || o.props.shape.version != v {
		o.layoutChanged()
	}
}

// objectExtra is the rarely used part of an Object.
type objectExtra struct {
	// sparse holds index properties not kept in elements.
	sparse map[uint32]Property
	// fields are JSInternalFieldObjectImpl fields (generators, promises).
	fields []Value
	// ephemerons are this object's entries in WeakMaps and WeakSets.
	ephemerons ephemeronTable
}

// sparse returns the sparse index properties (nil if none).
func (o *Object) sparse() map[uint32]Property {
	if o.extra == nil {
		return nil
	}
	return o.extra.sparse
}

// exoticObject overrides the internal methods of an object. Implementations
// embed ordinaryObject and override what differs.
type exoticObject interface {
	getPrototypeOf(o *Object) *Object
	setPrototypeOf(o *Object, proto *Object) bool
	isExtensible(o *Object) bool
	preventExtensions(o *Object) bool
	getOwnProperty(o *Object, k PropertyKey) (Property, bool)
	defineOwnProperty(o *Object, k PropertyKey, d PropertyDescriptor) bool
	hasProperty(o *Object, k PropertyKey) bool
	get(o *Object, k PropertyKey, receiver Value) Value
	set(o *Object, k PropertyKey, v Value, receiver Value) bool
	delete(o *Object, k PropertyKey) bool
	ownKeys(o *Object) []PropertyKey
}

// ordinaryObject implements the ordinary internal methods.
type ordinaryObject struct{}

func (ordinaryObject) getPrototypeOf(o *Object) *Object         { return o.proto }
func (ordinaryObject) setPrototypeOf(o *Object, p *Object) bool { return o.ordinarySetPrototypeOf(p) }
func (ordinaryObject) isExtensible(o *Object) bool              { return o.extensible }
func (ordinaryObject) preventExtensions(o *Object) bool {
	o.ordinaryPreventExtensions()
	return true
}
func (ordinaryObject) getOwnProperty(o *Object, k PropertyKey) (Property, bool) {
	return o.ordinaryGetOwnProperty(k)
}
func (ordinaryObject) defineOwnProperty(o *Object, k PropertyKey, d PropertyDescriptor) bool {
	return o.ordinaryDefineOwnProperty(k, d)
}
func (ordinaryObject) hasProperty(o *Object, k PropertyKey) bool { return o.ordinaryHasProperty(k) }
func (ordinaryObject) get(o *Object, k PropertyKey, receiver Value) Value {
	return o.ordinaryGet(k, receiver)
}
func (ordinaryObject) set(o *Object, k PropertyKey, v Value, receiver Value) bool {
	return o.ordinarySet(k, v, receiver)
}
func (ordinaryObject) delete(o *Object, k PropertyKey) bool { return o.ordinaryDelete(k) }
func (ordinaryObject) ownKeys(o *Object) []PropertyKey      { return o.ordinaryOwnKeys() }

// newObject creates an ordinary extensible object.
func (r *Realm) newObject(proto *Object) *Object {
	return &Object{realm: r, proto: proto, extensible: true, props: propertyMap{shape: r.vm.rootShape}}
}

// NewObject creates a plain object inheriting from Object.prototype.
func (r *Realm) NewObject() *Object { return r.newObject(r.ObjectPrototype) }

func (o *Object) vm() *VM          { return o.realm.vm }
func (o *Object) Realm() *Realm    { return o.realm }
func (o *Object) Class() Class     { return o.class }
func (o *Object) IsCallable() bool { return o.fn != nil }
func (o *Object) IsConstructor() bool {
	return o.fn != nil && o.fn.isConstructor()
}

// Prototype returns the [[Prototype]] slot of an ordinary object.
func (o *Object) Prototype() *Object { return o.proto }

// --- Internal method dispatch --------------------------------------------------------

func (o *Object) GetPrototypeOf() *Object {
	if o.exotic != nil {
		return o.exotic.getPrototypeOf(o)
	}
	return o.proto
}

func (o *Object) SetPrototypeOf(p *Object) bool {
	if o.exotic != nil {
		return o.exotic.setPrototypeOf(o, p)
	}
	return o.ordinarySetPrototypeOf(p)
}

func (o *Object) IsExtensible() bool {
	if o.exotic != nil {
		return o.exotic.isExtensible(o)
	}
	return o.extensible
}

func (o *Object) PreventExtensions() bool {
	if o.exotic != nil {
		return o.exotic.preventExtensions(o)
	}
	o.ordinaryPreventExtensions()
	return true
}

func (o *Object) GetOwnProperty(k PropertyKey) (Property, bool) {
	if o.exotic != nil {
		return o.exotic.getOwnProperty(o, k)
	}
	return o.ordinaryGetOwnProperty(k)
}

func (o *Object) DefineOwnProperty(k PropertyKey, d PropertyDescriptor) bool {
	if o.exotic != nil {
		return o.exotic.defineOwnProperty(o, k, d)
	}
	return o.ordinaryDefineOwnProperty(k, d)
}

func (o *Object) HasProperty(k PropertyKey) bool {
	for obj := o; obj != nil; obj = obj.proto {
		if obj.exotic != nil {
			return obj.exotic.hasProperty(obj, k)
		}
		if _, ok := obj.ordinaryGetOwnProperty(k); ok {
			return true
		}
	}
	return false
}

func (o *Object) Get(k PropertyKey, receiver Value) Value {
	for obj := o; obj != nil; obj = obj.proto {
		if obj.exotic != nil {
			return obj.exotic.get(obj, k, receiver)
		}
		if p, ok := obj.ordinaryGetOwnProperty(k); ok {
			return p.getValue(receiver)
		}
	}
	return Undefined
}

func (o *Object) Set(k PropertyKey, v Value, receiver Value) bool {
	if o.exotic != nil {
		return o.exotic.set(o, k, v, receiver)
	}
	return o.ordinarySet(k, v, receiver)
}

func (o *Object) Delete(k PropertyKey) bool {
	if o.exotic != nil {
		return o.exotic.delete(o, k)
	}
	return o.ordinaryDelete(k)
}

func (o *Object) OwnPropertyKeys() []PropertyKey {
	if o.exotic != nil {
		return o.exotic.ownKeys(o)
	}
	return o.ordinaryOwnKeys()
}

// --- Ordinary internal methods -------------------------------------------------------

func (o *Object) ordinarySetPrototypeOf(p *Object) bool {
	if p == o.proto {
		return true
	}
	if !o.extensible {
		return false
	}
	for q := p; q != nil; {
		if q == o {
			return false
		}
		if q.class == ClassProxy {
			break
		}
		q = q.proto
	}
	o.proto = p
	o.layoutChanged()
	return true
}

func (o *Object) ordinaryPreventExtensions() {
	if !o.extensible {
		return
	}
	o.extensible = false
}

func (o *Object) ordinaryGetOwnProperty(k PropertyKey) (Property, bool) {
	if k.sym == nil {
		if i, ok := k.Index(); ok {
			return o.getOwnIndex(i)
		}
	}
	if o.props.shape.lazy && k == prototypeKey {
		o.reifyPrototype()
	}
	if p, ok := o.props.get(k); ok {
		return p, true
	}
	return Property{}, false
}

// getOwnIndex looks up an own index property of an ordinary object.
func (o *Object) getOwnIndex(i uint32) (Property, bool) {
	if int64(i) < int64(len(o.elements)) {
		if v := o.elements[i]; !v.IsEmpty() {
			return Property{Value: v, Flags: DefaultFlags}, true
		}
	}
	if p, ok := o.sparse()[i]; ok {
		return p, true
	}
	return Property{}, false
}

func (o *Object) ordinaryHasProperty(k PropertyKey) bool {
	if _, ok := o.GetOwnProperty(k); ok {
		return true
	}
	if p := o.GetPrototypeOf(); p != nil {
		return p.HasProperty(k)
	}
	return false
}

func (o *Object) ordinaryGet(k PropertyKey, receiver Value) Value {
	p, ok := o.GetOwnProperty(k)
	if !ok {
		if parent := o.GetPrototypeOf(); parent != nil {
			return parent.Get(k, receiver)
		}
		return Undefined
	}
	return p.getValue(receiver)
}

// ordinarySet is OrdinarySet / OrdinarySetWithOwnDescriptor.
func (o *Object) ordinarySet(k PropertyKey, v Value, receiver Value) bool {
	// Fast path: an own writable data property on the receiver itself.
	if ro := receiver.AsObject(); ro == o && o.exotic == nil {
		if i, ok := k.Index(); ok {
			if int64(i) < int64(len(o.elements)) && !o.elements[i].IsEmpty() {
				o.elements[i] = v
				return true
			}
		} else if i := o.props.find(k); i >= 0 && o.props.shape.flags[i]&(accessorFlag|lazyFlag|Writable) == Writable {
			o.props.vals[i] = v
			return true
		}
	}
	ownDesc, ok := o.GetOwnProperty(k)
	if !ok {
		if parent := o.GetPrototypeOf(); parent != nil {
			return parent.Set(k, v, receiver)
		}
		ownDesc = Property{Value: Undefined, Flags: DefaultFlags}
	}
	return setWithOwnDescriptor(k, v, receiver, ownDesc)
}

func setWithOwnDescriptor(k PropertyKey, v Value, receiver Value, ownDesc Property) bool {
	if !ownDesc.IsAccessor() {
		if !ownDesc.Writable() {
			return false
		}
		ro := receiver.AsObject()
		if ro == nil {
			return false
		}
		if existing, ok := ro.GetOwnProperty(k); ok {
			if existing.IsAccessor() || !existing.Writable() {
				return false
			}
			return ro.DefineOwnProperty(k, PropertyDescriptor{Value: v, Has: HasValue})
		}
		return ro.DefineOwnProperty(k, DataDescriptor(v, DefaultFlags))
	}
	setter := ownDesc.Accessor().Set
	if setter == nil {
		return false
	}
	setter.vm().Call(ObjectValue(setter), receiver, v)
	return true
}

func (o *Object) ordinaryDelete(k PropertyKey) bool {
	if k.sym == nil {
		if i, ok := k.Index(); ok {
			return o.deleteIndex(i)
		}
	}
	p, ok := o.props.get(k)
	if !ok {
		return true
	}
	if !p.Configurable() {
		return false
	}
	o.props.remove(k)
	o.layoutChanged()
	return true
}

// deleteIndex is OrdinaryDelete for an index property.
func (o *Object) deleteIndex(i uint32) bool {
	if int64(i) < int64(len(o.elements)) && !o.elements[i].IsEmpty() {
		o.elements[i] = Empty
		if int(i) == len(o.elements)-1 {
			o.trimElements()
		}
		return true
	}
	p, ok := o.sparse()[i]
	if !ok {
		return true
	}
	if !p.Configurable() {
		return false
	}
	delete(o.sparse(), i)
	return true
}

func (o *Object) trimElements() {
	n := len(o.elements)
	for n > 0 && o.elements[n-1].IsEmpty() {
		n--
	}
	o.elements = o.elements[:n]
}

// ordinaryOwnKeys is OrdinaryOwnPropertyKeys: indices ascending, then
// strings and symbols in creation order. Private symbols are omitted.
func (o *Object) ordinaryOwnKeys() []PropertyKey {
	keys := make([]PropertyKey, 0, len(o.elements)+len(o.sparse())+o.props.len())
	for _, i := range o.indexKeys() {
		keys = append(keys, IndexKey(i))
	}
	for k := range o.props.all {
		if k.sym == nil {
			keys = append(keys, k)
		}
	}
	for k := range o.props.all {
		if k.sym != nil && !k.sym.private {
			keys = append(keys, k)
		}
	}
	return keys
}

// ordinaryDefineOwnProperty is ValidateAndApplyPropertyDescriptor on o.
func (o *Object) ordinaryDefineOwnProperty(k PropertyKey, d PropertyDescriptor) bool {
	current, exists := o.ordinaryGetOwnProperty(k)
	if !exists {
		if !o.IsExtensible() && !k.IsPrivate() {
			return false
		}
		var p Property
		if d.IsAccessorDescriptor() {
			p = accessorProperty(d.Get, d.Set, d.flags())
		} else {
			v := d.Value
			if d.Has&HasValue == 0 {
				v = Undefined
			}
			p = dataProperty(v, d.flags())
		}
		o.storeProperty(k, p)
		return true
	}
	p, ok := validateAndApply(current, d)
	if !ok {
		return false
	}
	o.storeProperty(k, p)
	return true
}

// validateAndApply checks d against the existing property and returns the
// updated property (ValidateAndApplyPropertyDescriptor steps 4-6).
func validateAndApply(current Property, d PropertyDescriptor) (Property, bool) {
	if !current.Configurable() {
		if d.Has&HasConfigurable != 0 && d.Configurable {
			return current, false
		}
		if d.Has&HasEnumerable != 0 && d.Enumerable != current.Enumerable() {
			return current, false
		}
		if !d.IsGenericDescriptor() && d.IsAccessorDescriptor() != current.IsAccessor() {
			return current, false
		}
		if current.IsAccessor() {
			a := current.Accessor()
			if d.Has&HasGet != 0 && d.Get != a.Get {
				return current, false
			}
			if d.Has&HasSet != 0 && d.Set != a.Set {
				return current, false
			}
		} else if !current.Writable() {
			if d.Has&HasWritable != 0 && d.Writable {
				return current, false
			}
			if d.Has&HasValue != 0 && !SameValue(d.Value, current.Value) {
				return current, false
			}
		}
	}

	flags := current.Flags & (Enumerable | Configurable)
	if d.Has&HasEnumerable != 0 {
		flags = flags&^Enumerable | boolFlag(d.Enumerable, Enumerable)
	}
	if d.Has&HasConfigurable != 0 {
		flags = flags&^Configurable | boolFlag(d.Configurable, Configurable)
	}

	switch {
	case d.IsAccessorDescriptor():
		var get, set *Object
		if current.IsAccessor() {
			a := current.Accessor()
			get, set = a.Get, a.Set
		}
		if d.Has&HasGet != 0 {
			get = d.Get
		}
		if d.Has&HasSet != 0 {
			set = d.Set
		}
		return accessorProperty(get, set, flags), true
	case d.IsDataDescriptor():
		v := Undefined
		if !current.IsAccessor() {
			v = current.Value
			flags |= current.Flags & Writable
		}
		if d.Has&HasValue != 0 {
			v = d.Value
		}
		if d.Has&HasWritable != 0 {
			flags = flags&^Writable | boolFlag(d.Writable, Writable)
		}
		return dataProperty(v, flags), true
	}
	// Generic descriptor: keep the kind.
	if current.IsAccessor() {
		a := current.Accessor()
		return accessorProperty(a.Get, a.Set, flags), true
	}
	return dataProperty(current.Value, flags|current.Flags&Writable), true
}

func boolFlag(b bool, f PropertyFlags) PropertyFlags {
	if b {
		return f
	}
	return 0
}

// storeProperty writes p as an own property, choosing between the element
// vector and the property map.
func (o *Object) storeProperty(k PropertyKey, p Property) {
	if k.sym == nil {
		if i, ok := k.Index(); ok {
			o.storeIndex(i, p)
			return
		}
	}
	o.setOwn(k, p)
}

const maxDenseGap = 1024

func (o *Object) storeIndex(i uint32, p Property) {
	n := int64(len(o.elements))
	idx := int64(i)
	if p.Flags == DefaultFlags && idx <= n+maxDenseGap {
		delete(o.sparse(), i)
		for int64(len(o.elements)) <= idx {
			o.elements = append(o.elements, Empty)
		}
		o.elements[i] = p.Value
		return
	}
	if idx < n {
		o.elements[i] = Empty
	}
	if o.extra == nil {
		o.extra = &objectExtra{}
	}
	if o.extra.sparse == nil {
		o.extra.sparse = make(map[uint32]Property)
	}
	o.extra.sparse[i] = p
}

// indexKeys returns the own index keys in ascending order.
func (o *Object) indexKeys() []uint32 {
	indices := make([]uint32, 0, len(o.elements)+len(o.sparse()))
	for i, v := range o.elements {
		if !v.IsEmpty() {
			indices = append(indices, uint32(i))
		}
	}
	if len(o.sparse()) > 0 {
		for i := range o.sparse() {
			indices = append(indices, i)
		}
		slices.Sort(indices)
	}
	return indices
}

// --- Abstract operations on objects (7.3) --------------------------------------------

// GetV reads property k of o with o as receiver.
func (o *Object) GetKey(k PropertyKey) Value { return o.Get(k, ObjectValue(o)) }

// GetStr reads a string-named property.
func (o *Object) GetStr(name string) Value { return o.Get(StrKey(name), ObjectValue(o)) }

// SetOrThrow is Set(O, P, V, true).
func (o *Object) SetOrThrow(k PropertyKey, v Value) {
	if !o.Set(k, v, ObjectValue(o)) {
		o.vm().throwTypeError("Attempted to assign to readonly property '%s'.", k)
	}
}

// CreateDataProperty defines an enumerable, writable, configurable property.
func (o *Object) CreateDataProperty(k PropertyKey, v Value) bool {
	if o.exotic == nil {
		if p, ok := o.ordinaryGetOwnProperty(k); !ok && o.extensible || ok && p.Configurable() {
			o.storeProperty(k, dataProperty(v, DefaultFlags))
			return true
		}
	}
	return o.DefineOwnProperty(k, DataDescriptor(v, DefaultFlags))
}

func (o *Object) CreateDataPropertyOrThrow(k PropertyKey, v Value) {
	if !o.CreateDataProperty(k, v) {
		o.vm().throwTypeError("Attempting to define property '%s' on an object that is not extensible or has a non-configurable property.", k)
	}
}

func (o *Object) DefinePropertyOrThrow(k PropertyKey, d PropertyDescriptor) {
	if !o.DefineOwnProperty(k, d) {
		o.vm().throwTypeError("Attempting to change attributes of a non-configurable property '%s'.", k)
	}
}

func (o *Object) DeletePropertyOrThrow(k PropertyKey) {
	if !o.Delete(k) {
		o.vm().throwTypeError("Unable to delete property '%s'.", k)
	}
}

func (o *Object) HasOwnProperty(k PropertyKey) bool {
	_, ok := o.GetOwnProperty(k)
	return ok
}

// putDirect defines or overwrites an own property without checks; used to
// set up built-in objects.
func (o *Object) putDirect(k PropertyKey, v Value, flags PropertyFlags) {
	o.storeProperty(k, dataProperty(v, flags))
}

func (o *Object) putDirectAccessor(k PropertyKey, get, set *Object, flags PropertyFlags) {
	o.storeProperty(k, accessorProperty(get, set, flags))
}

// IntegrityLevel is sealed or frozen.
type IntegrityLevel uint8

const (
	Sealed IntegrityLevel = iota
	Frozen
)

// SetIntegrityLevel (7.3.15).
func (o *Object) SetIntegrityLevel(level IntegrityLevel) bool {
	if !o.PreventExtensions() {
		return false
	}
	if o.exotic == nil && len(o.elements) == 0 && len(o.sparse()) == 0 && o.props.shape != nil && !o.props.shape.lazy {
		// Only named properties, none made lazily: one transition.
		s, v := o.props.shape, o.props.shape.version
		if n := s.withIntegrity(level); n != s || n.version != v {
			o.props.shape = n
			o.layoutChanged()
		}
		return true
	}
	keys := o.OwnPropertyKeys()
	if level == Sealed {
		for _, k := range keys {
			o.DefinePropertyOrThrow(k, PropertyDescriptor{Configurable: false, Has: HasConfigurable})
		}
		return true
	}
	for _, k := range keys {
		current, ok := o.GetOwnProperty(k)
		if !ok {
			continue
		}
		d := PropertyDescriptor{Has: HasConfigurable}
		if !current.IsAccessor() {
			d.Has |= HasWritable
		}
		o.DefinePropertyOrThrow(k, d)
	}
	return true
}

// TestIntegrityLevel (7.3.16).
func (o *Object) TestIntegrityLevel(level IntegrityLevel) bool {
	if o.IsExtensible() {
		return false
	}
	for _, k := range o.OwnPropertyKeys() {
		p, ok := o.GetOwnProperty(k)
		if !ok {
			continue
		}
		if p.Configurable() {
			return false
		}
		if level == Frozen && !p.IsAccessor() && p.Writable() {
			return false
		}
	}
	return true
}

// getIndex reads element i (with o as receiver).
func (o *Object) getIndex(i uint32) Value { return o.getIdx(int64(i)) }

// fieldsFor allocates n internal fields initialized to undefined.
func makeFields(n int) []Value {
	f := make([]Value, n)
	for i := range f {
		f[i] = Undefined
	}
	return f
}
