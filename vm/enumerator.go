package vm

// for-in enumeration (JSPropertyNameEnumerator; EnumerateObjectProperties,
// 14.7.5.9). The keys are collected up front like JSC does; each key is
// checked again when it is visited so that deleted properties are skipped.

type propertyNameEnumerator struct {
	keys   []PropertyKey
	owners []*Object
	// owner, if set, owns every key (owners is nil): an object whose
	// prototypes add no keys.
	owner *Object
	pos   int

	// For an enumeration from a shape's cache (JSC's OwnStructureMode):
	// all keys are base's own, names holds their strings, and while base
	// keeps shape none needs checking again.
	base  *Object
	shape *shape
	names []Value
}

// enumCache is the keys for-in visits for objects of one shape whose
// prototypes have no enumerable properties (JSPropertyNameEnumerator,
// cached in JSC's StructureRareData): the shapes of the prototypes are
// checked when it is used.
type enumCache struct {
	keys  []PropertyKey
	names []Value
	chain []protoLink
}

// cachedEnumerator returns the enumeration of o from its shape's cache,
// making the cache if o qualifies: an ordinary object without indexed
// properties whose shape is shared, whose prototypes are ordinary, have
// no indexed properties and no enumerable string-keyed ones.
func (vm *VM) cachedEnumerator(o *Object) *propertyNameEnumerator {
	s := o.props.shape
	if o.exotic != nil || s == nil || s.dictionary || s.lazy || len(o.elements) > 0 || o.extra != nil {
		return nil
	}
	if c := s.enum; c != nil {
		// The prototypes must be the ones the cache was made for, with
		// their shapes (so still without enumerable keys) and still
		// without indexed properties (which do not change a shape).
		p, ok := o.proto, true
		for i := range c.chain {
			if !c.chain[i].matches(p) || len(p.elements) > 0 || len(p.sparse()) > 0 {
				ok = false
				break
			}
			p = p.proto
		}
		if ok && p == nil {
			return &propertyNameEnumerator{keys: c.keys, names: c.names, base: o, shape: s}
		}
	}
	var chain []protoLink
	for p := o.proto; p != nil; p = p.proto {
		ps := p.props.shape
		if !ordinaryExotic(p) || ps == nil || ps.dictionary || ps.lazy || len(p.elements) > 0 || len(p.sparse()) > 0 {
			return nil
		}
		for i := range ps.keys {
			if ps.flags[i]&Enumerable != 0 && !ps.keys[i].IsSymbol() {
				return nil
			}
		}
		chain = append(chain, linkFor(p))
	}
	c := &enumCache{chain: chain}
	for i, k := range s.keys {
		if s.flags[i]&Enumerable != 0 && !k.IsSymbol() && !k.IsPrivate() {
			c.keys = append(c.keys, k)
			c.names = append(c.names, StringValue(NewString(k.String())))
		}
	}
	s.enum = c
	return &propertyNameEnumerator{keys: c.keys, names: c.names, base: o, shape: s}
}

// ordinaryChain reports whether o and its prototypes are ordinary objects.
func ordinaryChain(o *Object) bool {
	for p := o; p != nil; p = p.proto {
		if !ordinaryExotic(p) {
			return false
		}
	}
	return true
}

// ordinaryExotic reports whether o's properties behave as an ordinary
// object's: no exotic methods, or only Object.prototype's immutable
// prototype (which every ordinary object's chain ends with; counting it
// as exotic kept the for-in cache from being used at all).
func ordinaryExotic(o *Object) bool {
	switch o.exotic.(type) {
	case nil, immutablePrototypeObject:
		return true
	}
	return false
}

// shadowed reports whether one of the objects has own property k.
func shadowed(objs []*Object, k PropertyKey) bool {
	for _, o := range objs {
		if _, ok := o.GetOwnProperty(k); ok {
			return true
		}
	}
	return false
}

// noEnumerableProtos reports whether o's prototypes are ordinary objects
// without indexed properties or enumerable string-keyed ones, so that
// for-in visits only o's own keys.
func noEnumerableProtos(o *Object) bool {
	for p := o.proto; p != nil; p = p.proto {
		ps := p.props.shape
		if !ordinaryExotic(p) || ps == nil || ps.lazy || len(p.elements) > 0 || len(p.sparse()) > 0 {
			return false
		}
		for i := range ps.keys {
			if ps.flags[i]&Enumerable != 0 && !ps.keys[i].IsSymbol() {
				return false
			}
		}
	}
	return true
}

// propertyEnumerator is op_get_property_enumerator.
func (vm *VM) propertyEnumerator(base Value) Value {
	if base.IsNullish() {
		return vm.realm.emptyEnumerator
	}
	o := vm.ToObject(base)
	if e := vm.cachedEnumerator(o); e != nil {
		return cellValue(e)
	}
	if o.exotic == nil && noEnumerableProtos(o) {
		// Only o's own keys, which are distinct: no set of the keys seen
		// and no owner per key (for-in over a dictionary of many keys
		// made a map of them every time; tagcloud).
		e := &propertyNameEnumerator{owner: o}
		for _, k := range o.OwnPropertyKeys() {
			if k.IsSymbol() {
				continue
			}
			if p, ok := o.GetOwnProperty(k); ok && p.Enumerable() {
				e.keys = append(e.keys, k)
			}
		}
		return cellValue(e)
	}
	if ordinaryChain(o) {
		// Ordinary objects only: a prototype's key is shadowed when an
		// object before it has the key, which a lookup tells without a
		// set of every key seen (tagcloud's objects have hundreds of keys
		// and Object.prototype a few enumerable methods: the set took 88
		// MB per run).
		e := &propertyNameEnumerator{}
		var chain []*Object
		for obj := o; obj != nil; obj = obj.proto {
			for _, k := range obj.OwnPropertyKeys() {
				if k.IsSymbol() || shadowed(chain, k) {
					continue
				}
				if p, ok := obj.GetOwnProperty(k); ok && p.Enumerable() {
					e.keys = append(e.keys, k)
					e.owners = append(e.owners, obj)
				}
			}
			chain = append(chain, obj)
		}
		return cellValue(e)
	}
	e := &propertyNameEnumerator{}
	visited := make(map[PropertyKey]bool)
	for obj := o; obj != nil; obj = obj.GetPrototypeOf() {
		for _, k := range obj.OwnPropertyKeys() {
			if k.IsSymbol() || visited[k] {
				continue
			}
			visited[k] = true
			p, ok := obj.GetOwnProperty(k)
			if !ok || !p.Enumerable() {
				continue
			}
			e.keys = append(e.keys, k)
			e.owners = append(e.owners, obj)
		}
	}
	return cellValue(e)
}

// next returns the next property name, or the sentinel string at the end.
func (e *propertyNameEnumerator) next(r *Realm) Value {
	if e.base != nil {
		for e.pos < len(e.keys) {
			i := e.pos
			e.pos++
			if e.base.props.shape != e.shape {
				// The object changed: check the key, as below.
				if p, ok := e.base.GetOwnProperty(e.keys[i]); !ok || !p.Enumerable() {
					continue
				}
			}
			return e.names[i]
		}
		return r.sentinelString
	}
	for e.pos < len(e.keys) {
		k, owner := e.keys[e.pos], e.owner
		if e.owners != nil {
			owner = e.owners[e.pos]
		}
		e.pos++
		if owner != nil {
			if p, ok := owner.GetOwnProperty(k); !ok || !p.Enumerable() {
				continue
			}
		}
		return StringValue(NewString(k.String()))
	}
	return r.sentinelString
}
