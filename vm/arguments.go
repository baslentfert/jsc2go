package vm

import "github.com/baslentfert/jsc2go/bytecode"

// Arguments objects (DirectArguments, ScopedArguments, ClonedArguments;
// ECMA-262 10.4.4). Mapped arguments alias the parameters: for
// DirectArguments the parameters live in the object's own storage (the
// bytecode reads them with op_get_from_arguments), for ScopedArguments they
// live in the function's lexical environment.

type argumentsData struct {
	// mapped tells which indexes are mapped; while it is nil, the first
	// numMapped are (DirectArguments, until one is unmapped).
	mapped    []bool
	numMapped int
	direct    []Value
	scope     *Scope
	offsets   []bytecode.ScopeOffset
}

// getMapped reads parameter i's storage.
func (a *argumentsData) getMapped(i int) Value {
	if a.scope != nil {
		return a.scope.slots[a.offsets[i]]
	}
	return a.direct[i]
}

func (a *argumentsData) setMapped(i int, v Value) {
	if a.scope != nil {
		a.scope.slots[a.offsets[i]] = v
		return
	}
	a.direct[i] = v
}

func (a *argumentsData) isMapped(k PropertyKey) (int, bool) {
	i, ok := k.Index()
	if !ok {
		return 0, false
	}
	if a.mapped == nil {
		return int(i), int64(i) < int64(a.numMapped)
	}
	if int64(i) >= int64(len(a.mapped)) || !a.mapped[i] {
		return 0, false
	}
	return int(i), true
}

// unmap ends the mapping of index i.
func (a *argumentsData) unmap(i int) {
	if a.mapped == nil {
		a.mapped = make([]bool, a.numMapped)
		for j := range a.mapped {
			a.mapped[j] = true
		}
	}
	a.mapped[i] = false
}

type argumentsObject struct{ ordinaryObject }

func argumentsOf(o *Object) *argumentsData { return o.internal.(*argumentsData) }

func (argumentsObject) getOwnProperty(o *Object, k PropertyKey) (Property, bool) {
	p, ok := o.ordinaryGetOwnProperty(k)
	if !ok {
		return p, false
	}
	a := argumentsOf(o)
	if i, mapped := a.isMapped(k); mapped {
		p.Value = a.getMapped(i)
	}
	return p, true
}

func (argumentsObject) defineOwnProperty(o *Object, k PropertyKey, d PropertyDescriptor) bool {
	a := argumentsOf(o)
	i, mapped := a.isMapped(k)
	newDesc := d
	if mapped && d.IsDataDescriptor() && d.Has&HasValue == 0 && d.Has&HasWritable != 0 && !d.Writable {
		newDesc.Value = a.getMapped(i)
		newDesc.Has |= HasValue
	}
	if mapped {
		// Keep the ordinary slot in sync before validating.
		if p, ok := o.ordinaryGetOwnProperty(k); ok && !p.IsAccessor() {
			p.Value = a.getMapped(i)
			o.storeProperty(k, p)
		}
	}
	if !o.ordinaryDefineOwnProperty(k, newDesc) {
		return false
	}
	if mapped {
		if d.IsAccessorDescriptor() {
			a.unmap(i)
		} else {
			if d.Has&HasValue != 0 {
				a.setMapped(i, d.Value)
			}
			if d.Has&HasWritable != 0 && !d.Writable {
				a.unmap(i)
			}
		}
	}
	return true
}

func (argumentsObject) get(o *Object, k PropertyKey, receiver Value) Value {
	a := argumentsOf(o)
	if i, mapped := a.isMapped(k); mapped {
		return a.getMapped(i)
	}
	return o.ordinaryGet(k, receiver)
}

func (argumentsObject) set(o *Object, k PropertyKey, v Value, receiver Value) bool {
	a := argumentsOf(o)
	if receiver.AsObject() == o {
		if i, mapped := a.isMapped(k); mapped {
			a.setMapped(i, v)
		}
	}
	return o.ordinarySet(k, v, receiver)
}

func (argumentsObject) delete(o *Object, k PropertyKey) bool {
	a := argumentsOf(o)
	i, mapped := a.isMapped(k)
	if !o.ordinaryDelete(k) {
		return false
	}
	if mapped {
		a.unmap(i)
	}
	return true
}

func (vm *VM) newArgumentsObject(f *Frame, mapped bool) *Object {
	r := vm.realm
	o := r.newObject(r.ObjectPrototype)
	o.class = ClassArguments
	args := f.arguments()
	o.elements = copyArgs(args)
	// Every arguments object of a realm starts with the same three
	// properties: after the first one, give the next ones the resulting
	// shape directly instead of adding the properties one by one (shared
	// shapes never change, so they can be reused).
	c := &r.argumentsShapes[0]
	if !mapped {
		c = &r.argumentsShapes[1]
	}
	if c.to != nil && o.props.shape == c.from {
		callee := ObjectValue(f.callee)
		if !mapped {
			callee = accessorProperty(r.ThrowTypeError, r.ThrowTypeError, 0).Value
		}
		o.props.shape = c.to
		o.props.vals = []Value{Int(len(args)), ObjectValue(r.arrayProtoValues), callee}
		return o
	}
	from := o.props.shape
	o.putDirect(argumentsLengthKey, Int(len(args)), Writable|Configurable)
	o.putDirect(SymKey(SymbolIterator), ObjectValue(r.arrayProtoValues), Writable|Configurable)
	if mapped {
		o.putDirect(argumentsCalleeKey, ObjectValue(f.callee), Writable|Configurable)
	} else {
		o.putDirectAccessor(argumentsCalleeKey, r.ThrowTypeError, r.ThrowTypeError, 0)
	}
	if !o.props.shape.dictionary {
		*c = argumentsShape{from: from, to: o.props.shape}
	}
	return o
}

// argumentsList is CreateListFromArrayLike of arguments object o (f.apply(this,
// arguments)) without going through its properties one by one; it reports
// false for objects other than arguments objects with their own length,
// iterator and callee still in their places and no holes.
func (vm *VM) argumentsList(o *Object) ([]Value, bool) {
	if o.class != ClassArguments || o.props.shape == nil {
		return nil, false
	}
	r := o.realm
	if r == nil || o.props.shape != r.argumentsShapes[0].to && o.props.shape != r.argumentsShapes[1].to {
		return nil, false
	}
	a, ok := o.internal.(*argumentsData)
	if !ok {
		return nil, false
	}
	length := o.props.vals[0]
	if !length.IsInt32() {
		return nil, false
	}
	n := int(length.num())
	if n < 0 || n > len(o.elements) {
		return nil, false
	}
	list := make([]Value, n)
	for i := range list {
		v := o.elements[i]
		if v.IsEmpty() {
			return nil, false
		}
		if _, mapped := a.isMapped(PropertyKey{idx: uint32(i) + 1}); mapped {
			v = a.getMapped(i)
		}
		list[i] = v
	}
	return list, true
}

var (
	argumentsLengthKey = StrKey("length")
	argumentsCalleeKey = StrKey("callee")
)

// argumentsShape is the shape an arguments object gets (to) from a new
// object's shape (from).
type argumentsShape struct{ from, to *shape }

// createDirectArguments is op_create_direct_arguments.
func (vm *VM) createDirectArguments(f *Frame) *Object {
	args := f.arguments()
	numParams := f.cb.u.NumParameters - 1
	r := vm.realm
	if c := &r.argumentsShapes[0]; c.to != nil && c.from == vm.rootShape {
		// The object, its data and its three properties in one allocation,
		// the elements and the storage in another (functions that use
		// arguments make one per call: raytrace's Class.create).
		b := new(directArgumentsBlock)
		n := max(len(args), numParams)
		buf := make([]Value, len(args)+n)
		var elements []Value
		if len(args) > 0 {
			elements = buf[:len(args):len(args)]
			copy(elements, args)
		}
		storage := buf[len(args):]
		copy(storage, args)
		for i := len(args); i < n; i++ {
			storage[i] = Undefined
		}
		b.data = argumentsData{numMapped: min(len(args), numParams), direct: storage}
		b.vals = [3]Value{Int(len(args)), ObjectValue(r.arrayProtoValues), ObjectValue(f.callee)}
		b.obj = Object{realm: r, proto: r.ObjectPrototype, extensible: true, class: ClassArguments, elements: elements,
			props: propertyMap{shape: c.to, vals: b.vals[:]}, internal: &b.data, exotic: argumentsObject{}}
		return &b.obj
	}
	o := vm.newArgumentsObject(f, true)
	storage := make([]Value, max(len(args), numParams))
	copy(storage, args)
	for i := len(args); i < len(storage); i++ {
		storage[i] = Undefined
	}
	o.internal = &argumentsData{numMapped: min(len(args), numParams), direct: storage}
	o.exotic = argumentsObject{}
	return o
}

// directArgumentsBlock is a DirectArguments object allocated at once.
type directArgumentsBlock struct {
	obj  Object
	data argumentsData
	vals [3]Value
}

// createScopedArguments is op_create_scoped_arguments.
func (vm *VM) createScopedArguments(f *Frame, scope *Scope) *Object {
	o := vm.newArgumentsObject(f, true)
	args := f.arguments()
	offsets := scope.table.Arguments()
	mapped := make([]bool, len(args))
	for i := range mapped {
		mapped[i] = i < len(offsets) && offsets[i].IsValid()
	}
	o.internal = &argumentsData{mapped: mapped, scope: scope, offsets: offsets}
	o.exotic = argumentsObject{}
	return o
}

// createClonedArguments is op_create_cloned_arguments (unmapped arguments).
func (vm *VM) createClonedArguments(f *Frame) *Object {
	return vm.newArgumentsObject(f, false)
}
