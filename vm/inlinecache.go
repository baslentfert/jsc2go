package vm

import (
	"unsafe"

	"github.com/baslentfert/jsc2go/bytecode"
	"github.com/baslentfert/jsc2go/ident"
)

// Inline caches for get_by_id and put_by_id, kept in the code block's
// metadata (JSC's GetByIdModeMetadata / PutByIdModeMetadata, simplified to
// monomorphic caches). A cache remembers where a property was found for a
// receiver shape; since a shape fixes the keys of an object, a matching
// shape means the same slot holds the property. Prototypes on the way are
// validated by their shapes too, standing in for JSC's watchpoints.

// protoLink is one prototype on a cached lookup path.
type protoLink struct {
	obj     *Object
	shape   *shape
	version uint32
}

func (l *protoLink) matches(o *Object) bool {
	return o == l.obj && o.props.shape == l.shape && l.shape.version == l.version
}

// sameFirstProto reports whether a cache case with prototype chain chain
// was made for receivers like o. A shape does not fix an object's
// prototype (JSC's structures do): objects of different classes often
// share one, and a case for one class is not stale for another, which
// gets a polymorphic case of its own.
func sameFirstProto(chain []protoLink, o *Object) bool {
	return len(chain) == 0 || chain[0].obj == o.proto
}

// watchChain marks the prototypes of a cache case's chain watched.
func (vm *VM) watchChain(chain []protoLink) {
	for i := range chain {
		chain[i].obj.watched = true
	}
}

func linkFor(o *Object) protoLink {
	return protoLink{o, o.props.shape, o.props.shape.version}
}

// maxGetProtoDepth is how deep in the prototype chain a get_by_id cache
// finds properties, and a put_by_id cache checks it when adding one: class
// hierarchies (Babylon's parser, 7 to 10 levels; TypeScript's nodes) need
// more than a few levels, and JSC's caches have no such limit.
const maxGetProtoDepth = 16

type getByIdCache struct {
	shape   *shape // receiver shape; nil when empty
	version uint32
	depth   uint8 // prototypes walked; the last one holds the property
	// chain are the prototypes walked (made only when there are any).
	chain    []protoLink
	index    int32
	accessor bool // the property is an accessor (fixed by the holder's shape)
	// miss: the property is absent (JSC's Miss access case); the last
	// prototype walked must have no prototype. Only get_by_id and
	// get_length cache misses, which read them as undefined.
	miss bool
	// poly holds the further cases of a polymorphic site.
	poly *getByIdPoly
	// epoch is the VM's protoEpoch when the chain was last checked, and
	// holder the last prototype walked: while the epoch is current, a
	// receiver with the case's shape and first prototype finds the
	// property in holder without checking the chain.
	epoch  uint64
	holder *Object
}

// polyEntries is the number of further receiver shapes a polymorphic site
// caches (like JSC's polymorphic access cases, up to four in all). Sites
// that only ever see one shape do not allocate them.
const polyEntries = 3

type getByIdPoly struct {
	entries [polyEntries]getByIdCache
	next    uint8 // the entry to replace next
}

type putByIdCache struct {
	shape   *shape // receiver shape before the store; nil when empty
	version uint32
	index   int32 // replace: slot of the writable data property
	// add: the shape after adding the key, and the prototype chain, which
	// must still lack the key (no setters or read-only properties) or, if
	// holder is set, end at a prototype where it is a writable data property
	// (the store then shadows it, like a class's default field values).
	newShape *shape
	depth    uint8
	holder   bool
	// chain are the prototypes walked (made only when there are any; its
	// array is reused when the case is refilled, as getByIdCache's).
	chain []protoLink
	// poly holds the further cases of a polymorphic site.
	poly *putByIdPoly
	// epoch is the VM's protoEpoch when the chain was last checked (see
	// getByIdCache).
	epoch uint64
}

type putByIdPoly struct {
	entries [polyEntries]putByIdCache
	next    uint8
}

// icOrdinary reports whether o's internal methods for the named property k are
// the ordinary ones, so that its property map is the whole truth.
func icOrdinary(o *Object, k PropertyKey) bool {
	switch o.exotic.(type) {
	case nil, immutablePrototypeObject:
		return true
	case *arrayObject, stringObject:
		// Only length and the indices of an array or a String object are
		// exotic.
		return k.name != lengthIdent
	case typedArrayObject:
		// Only canonical numeric keys are a typed array's own.
		_, num := canonicalNumericIndex(k)
		return !num
	}
	return false
}

// primitiveBase returns the object whose properties a get_by_id of key k
// on primitive base reads through the cache, as JSC caches primitive
// bases: the realm's String.prototype for a string (but its length, which
// is the string's own) and Number.prototype for a number; nil otherwise.
func (vm *VM) primitiveBase(base Value, k PropertyKey) *Object {
	switch {
	case base.IsString():
		if k.name != lengthIdent {
			return vm.realm.StringPrototype
		}
	case base.IsNumber():
		return vm.realm.NumberPrototype
	}
	return nil
}

func (cb *CodeBlock) allocCaches() {
	m := &cb.u.Metadata
	cb.getByIdCaches = make([]getByIdCache, m.Counts[bytecode.OpGetByIdID])
	cb.getLengthCaches = make([]getByIdCache, m.Counts[bytecode.OpGetLengthID])
	cb.putByIdCaches = make([]putByIdCache, m.Counts[bytecode.OpPutByIdID])
	cb.resolveScopeCaches = make([]resolveScopeCache, m.Counts[bytecode.OpResolveScopeID])
	cb.getFromScopeCaches = make([]getByIdCache, m.Counts[bytecode.OpGetFromScopeID])
	cb.putToScopeCaches = make([]putByIdCache, m.Counts[bytecode.OpPutToScopeID])
	cb.getScopeSlotCaches = make([]scopeSlotCache, m.Counts[bytecode.OpGetFromScopeID])
	cb.putScopeSlotCaches = make([]scopeSlotCache, m.Counts[bytecode.OpPutToScopeID])
	cb.createThisCaches = make([]getByIdCache, m.Counts[bytecode.OpCreateThisID])
	cb.instanceofCaches = make([]getByIdCache, m.Counts[bytecode.OpInstanceofID])
	cb.getByValProfiles = make([]uint16, m.Counts[bytecode.OpGetByValID])
	cb.putByValProfiles = make([]uint16, m.Counts[bytecode.OpPutByValID])
	cb.callProfiles = make([]callProfile, m.Counts[bytecode.OpCallID])
	cb.callIgnoreProfiles = make([]callProfile, m.Counts[bytecode.OpCallIgnoreResultID])
	cb.tailCallProfiles = make([]callProfile, m.Counts[bytecode.OpTailCallID])
}

// callProfile is the callee a call site saw: one function, two (callee2,
// which the JIT inlines both of), or more (poly). It keeps them alive as
// long as the code block, as the call caches do.
type callProfile struct {
	callee, callee2 *Object // callee2: a second one (polymorphic of two)
	poly            bool    // more than two
}

// note records a call of v.
func (p *callProfile) note(v Value) {
	o := v.AsObject()
	switch {
	case p.poly || p.callee == o || p.callee2 == o && o != nil:
	case p.callee == nil && o != nil:
		p.callee = o
	case p.callee2 == nil && o != nil:
		p.callee2 = o
	default:
		p.callee, p.callee2, p.poly = nil, nil, true
	}
}

// byValOther is the profile bit of a base other than a typed array; bit k
// is a typed array of kind k.
const byValOther = 1 << 15

// byValProfileOf is the profile bit of base v.
func byValProfileOf(v Value) uint16 {
	if o := v.AsObject(); o != nil && o.class == ClassTypedArray {
		if ta, ok := o.internal.(*typedArray); ok {
			return 1 << ta.kind
		}
	}
	return byValOther
}

// megaGet is JSC's MegamorphicCache (runtime/MegamorphicCache.h) for
// get_by_id: one table per VM of own data properties by shape and name,
// which sites that see more shapes than their cache has cases read from
// (in Go and in generated code). Its size is fixed; an entry keeps its
// shape and name alive until another takes its place.
type megaGet [megaGetSize]megaGetEntry

const megaGetSize = 2048 // a power of two

type megaGetEntry struct {
	shape   *shape
	name    ident.Identifier
	version uint32
	index   int32
	// For a property of a prototype (JSC's MegamorphicCache has these
	// too), holder holds it at index: valid for receivers whose prototype
	// is proto while the VM's protoEpoch (at epochAt) is still epoch. The
	// sites that miss most are AST walkers calling methods of many node
	// classes (typescript, babylon, coffeescript). An index of -1 (holder
	// nil) is an absent property, read as undefined under the same
	// conditions: obj[key] for keys that are not there (typescript and
	// prepack 600,000 times a run).
	holder  *Object
	proto   *Object
	epoch   uint64
	epochAt *uint64
	// newShape, in the put table, makes the entry an add: the object gets
	// newShape and the value appended (JSC's MegamorphicCache stores with
	// a transition), while its prototype is proto and the epoch current,
	// so that the chain still has no setter or read-only property k.
	newShape *shape
}

// megaGetHash is the entry of shape s, name and prototype proto (classes
// often share a shape, so the prototype keeps their entries apart): the
// same arithmetic as generated code's, on the low 32 bits of the pointers.
func megaGetHash(s *shape, name ident.Identifier, proto *Object) uint32 {
	return (uint32(uintptr(unsafe.Pointer(s)))>>4 ^ megaGetNameHash(name) ^
		uint32(uintptr(unsafe.Pointer(proto)))>>4) & (megaGetSize - 1)
}

func megaGetNameHash(name ident.Identifier) uint32 {
	return uint32(*(*uintptr)(unsafe.Pointer(&name)) >> 3)
}

// lookup returns data property name of o, its own or a prototype's, if
// the table has it.
func (t *megaGet) lookup(o *Object, k PropertyKey) (Value, bool) {
	s := o.props.shape
	e := &t[megaGetHash(s, k.name, o.proto)]
	if e.shape != s || e.name != k.name || e.version != s.version {
		return Undefined, false
	}
	if h := e.holder; h != nil || e.index < 0 {
		if o.proto != e.proto || *e.epochAt != e.epoch {
			return Undefined, false
		}
		if h == nil {
			return Undefined, true // absent
		}
		return h.props.vals[e.index], true
	}
	return o.props.vals[e.index], true
}

// fill enters data property k of o: its own, or one found through
// ordinary prototypes (marked watched, as the get caches do).
func (t *megaGet) fill(vm *VM, o *Object, k PropertyKey) {
	s := o.props.shape
	if s == nil || k.sym != nil || k.idx != 0 {
		return
	}
	h := megaGetHash(s, k.name, o.proto)
	if i := o.props.find(k); i >= 0 {
		if s.flags[i]&(accessorFlag|lazyFlag) == 0 {
			t[h] = megaGetEntry{shape: s, name: k.name, version: s.version, index: int32(i)}
		}
		return
	}
	p := o.proto
	for depth := 0; p != nil; depth++ {
		if depth == maxGetProtoDepth || !icOrdinary(p, k) || p.props.shape == nil {
			return
		}
		if i := p.props.find(k); i >= 0 {
			if p.props.shape.flags[i]&(accessorFlag|lazyFlag) != 0 {
				return
			}
			for q := o.proto; q != p; q = q.proto {
				q.watched = true
			}
			p.watched = true
			t[h] = megaGetEntry{shape: s, name: k.name, version: s.version, index: int32(i),
				holder: p, proto: o.proto, epoch: vm.protoEpoch, epochAt: &vm.protoEpoch}
			return
		}
		p = p.proto
	}
	// Absent from o and every prototype.
	for q := o.proto; q != nil; q = q.proto {
		q.watched = true
	}
	t[h] = megaGetEntry{shape: s, name: k.name, version: s.version, index: -1,
		proto: o.proto, epoch: vm.protoEpoch, epochAt: &vm.protoEpoch}
}

// undefinedSlot is the value generated code reads for an absent property
// in the megamorphic table.
var undefinedSlot = Undefined

// megaPut is the megamorphic cache for put_by_id (JSC's MegamorphicCache
// has store entries too): megaGet's layout, for own writable data
// properties, which a store replaces without looking at the prototypes.
type megaPut = megaGet

// fillPut enters own writable data property k of o.
func (t *megaPut) fillPut(o *Object, k PropertyKey) {
	s := o.props.shape
	if s == nil || k.sym != nil || k.idx != 0 {
		return
	}
	i := o.props.find(k)
	if i < 0 || s.flags[i]&(accessorFlag|lazyFlag|Writable) != Writable {
		return
	}
	t[megaGetHash(s, k.name, o.proto)] = megaGetEntry{shape: s, name: k.name, version: s.version, index: int32(i)}
}

// fillAdd enters the add of k to o, whose shape was before and now is
// after: a shared transition, through prototypes that have no k or a
// writable data property k (marked watched, as for the put caches).
func (t *megaPut) fillAdd(vm *VM, o *Object, before *shape, k PropertyKey) {
	after := o.props.shape
	if k.sym != nil || k.idx != 0 || before == nil || after == nil || before.dictionary || after.dictionary ||
		len(after.keys) != len(before.keys)+1 || after.via != (transitionKey{k, DefaultFlags, tkAdd}) {
		return
	}
	for p, depth := o.proto, 0; p != nil; p, depth = p.proto, depth+1 {
		if depth == maxGetProtoDepth || !icOrdinary(p, k) || p.props.shape == nil {
			return
		}
		if i := p.props.find(k); i >= 0 {
			if p.props.shape.flags[i]&(accessorFlag|lazyFlag|Writable) != Writable {
				return
			}
			break
		}
	}
	for p := o.proto; p != nil; p = p.proto {
		p.watched = true
		if p.props.find(k) >= 0 {
			break
		}
	}
	t[megaGetHash(before, k.name, o.proto)] = megaGetEntry{shape: before, name: k.name, version: before.version,
		proto: o.proto, epoch: vm.protoEpoch, epochAt: &vm.protoEpoch, newShape: after}
}

// put stores v to own writable data property k of o, or adds it, if the
// table has it.
func (t *megaPut) put(o *Object, k PropertyKey, v Value) bool {
	s := o.props.shape
	e := &t[megaGetHash(s, k.name, o.proto)]
	if e.shape != s || e.name != k.name || e.version != s.version || e.holder != nil {
		return false
	}
	if e.newShape != nil {
		if !o.extensible || o.watched || o.proto != e.proto || *e.epochAt != e.epoch {
			return false
		}
		o.props.shape = e.newShape
		o.props.vals = append(o.props.vals, v)
		return true
	}
	o.props.vals[e.index] = v
	return true
}

// megaByValKey returns the object and key of a get_by_val or put_by_val
// whose key is a string naming a property (not an index) of a plain
// object: generated code looks those up in the megamorphic tables (JSC's
// IndexedMegamorphicLoad and IndexedMegamorphicStore), with the key's atom
// from the string's cached key.
func megaByValKey(base, p Value) (*Object, PropertyKey, bool) {
	o := base.AsObject()
	if o == nil || o.exotic != nil || !p.IsString() {
		return nil, PropertyKey{}, false
	}
	k := p.StringRef().Key()
	if k.idx != 0 || k.sym != nil {
		return nil, PropertyKey{}, false
	}
	return o, k, true
}

// fillMegaGetByVal enters base[p] in the megamorphic get table after a
// get_by_val's slow path.
func (vm *VM) fillMegaGetByVal(base, p Value) {
	if o, k, ok := megaByValKey(base, p); ok {
		if vm.megaGet == nil {
			vm.megaGet = new(megaGet)
		}
		vm.megaGet.fill(vm, o, k)
	}
}

// fillMegaPutByVal enters base[p] in the megamorphic put table after a
// put_by_val's slow path.
func (vm *VM) fillMegaPutByVal(base, p Value) {
	if o, k, ok := megaByValKey(base, p); ok {
		if vm.megaPut == nil {
			vm.megaPut = new(megaPut)
		}
		vm.megaPut.fillPut(o, k)
	}
}

// megamorphic reports whether all of c's cases are taken.
func (c *putByIdCache) megamorphic() bool {
	if c.shape == nil || c.poly == nil {
		return false
	}
	for i := range c.poly.entries {
		if c.poly.entries[i].shape == nil {
			return false
		}
	}
	return true
}

// megamorphic reports whether all of c's cases are taken.
func (c *getByIdCache) megamorphic() bool {
	if c.shape == nil || c.poly == nil {
		return false
	}
	for i := range c.poly.entries {
		if c.poly.entries[i].shape == nil {
			return false
		}
	}
	return true
}

// getById is get_by_id with its inline cache.
func (vm *VM) getById(c *getByIdCache, base Value, k PropertyKey) Value {
	o := base.AsObject()
	if o == nil && k.idx == 0 {
		o = vm.primitiveBase(base, k) // the receiver for getters stays base
	}
	if o == nil || k.idx != 0 || !icOrdinary(o, k) {
		return vm.getByKey(base, k)
	}
	if v, ok := c.lookup(vm, o, base); ok {
		return v
	}
	if c.poly != nil {
		for i := range c.poly.entries {
			if v, ok := c.poly.entries[i].lookup(vm, o, base); ok {
				return v
			}
		}
	}
	mega := c.megamorphic() && k.sym == nil
	if mega {
		if vm.megaGet == nil {
			vm.megaGet = new(megaGet)
		}
		if v, ok := vm.megaGet.lookup(o, k); ok {
			return v
		}
	}
	v := o.Get(k, base)
	if mega {
		vm.megaGet.fill(vm, o, k)
	}
	// The primary case is refilled when empty or stale (o's shape changed
	// in place); other shapes, and other prototypes (sameFirstProto), get
	// the further cases.
	if p := c.poly; c.shape == nil || c.shape == o.props.shape && sameFirstProto(c.chain, o) {
		vm.fillGetById(c, o, k, true)
		c.poly = p
	} else {
		if p == nil {
			p = new(getByIdPoly)
			c.poly = p
		}
		vm.fillGetById(&p.entries[p.next], o, k, true)
		p.next = (p.next + 1) % polyEntries
	}
	return v
}

// fillGetById fills cache case c for property k of o; with misses also
// when k is absent.
func (vm *VM) fillGetById(c *getByIdCache, o *Object, k PropertyKey, misses bool) {
	if o.props.shape == nil {
		return
	}
	// The chain's array is reused: megamorphic sites refill their cases
	// all the time, and allocating a chain each time showed in chai and
	// prepack. Its old links are cleared, not to keep their objects alive.
	chain := c.chain[:0]
	clear(chain[:cap(chain)])
	*c = getByIdCache{shape: o.props.shape, version: o.props.shape.version, chain: chain}
	holder := o
	for depth := 0; ; depth++ {
		if i := holder.props.find(k); i >= 0 {
			if holder.props.shape.flags[i]&lazyFlag != 0 {
				break // not made yet: not cached
			}
			c.depth, c.index = uint8(depth), int32(i)
			c.accessor = holder.props.shape.flags[i]&accessorFlag != 0
			vm.watchChain(c.chain)
			c.epoch, c.holder = vm.protoEpoch, holder
			return
		}
		p := holder.proto
		if p == nil && misses {
			c.depth, c.miss = uint8(depth), true
			vm.watchChain(c.chain)
			c.epoch, c.holder = vm.protoEpoch, holder
			return
		}
		if p == nil || depth == maxGetProtoDepth || !icOrdinary(p, k) || p.props.shape == nil {
			break
		}
		c.chain = append(c.chain, linkFor(p))
		holder = p
	}
	c.shape, c.chain = nil, c.chain[:0] // not found or not cacheable
}

// putById is put_by_id with its inline cache.
func (vm *VM) putById(c *putByIdCache, base Value, k PropertyKey, v Value, direct, strict bool) {
	o := base.AsObject()
	if o == nil || k.idx != 0 || !icOrdinary(o, k) || o.props.shape == nil {
		vm.putByIdSlow(base, k, v, direct, strict)
		return
	}
	s := o.props.shape
	if vm.cachedPut(c, o, v, direct) {
		return
	}
	if c.poly != nil {
		for i := range c.poly.entries {
			if vm.cachedPut(&c.poly.entries[i], o, v, direct) {
				return
			}
		}
	}
	if !direct && k.sym == nil && c.megamorphic() {
		if vm.megaPut == nil {
			vm.megaPut = new(megaPut)
		}
		if vm.megaPut.put(o, k, v) {
			return
		}
		vm.putByIdSlow(base, k, v, direct, strict)
		if o.props.shape == s {
			vm.megaPut.fillPut(o, k)
		} else if o.extensible && !o.watched {
			vm.megaPut.fillAdd(vm, o, s, k)
		}
	} else {
		vm.putByIdSlow(base, k, v, direct, strict)
	}
	if p := c.poly; c.shape == nil || c.shape == s && sameFirstProto(c.chain, o) {
		vm.fillPutById(c, o, s, k, direct)
		c.poly = p
	} else {
		if p == nil {
			p = new(putByIdPoly)
			c.poly = p
		}
		vm.fillPutById(&p.entries[p.next], o, s, k, direct)
		p.next = (p.next + 1) % polyEntries
	}
}

// cachedPut does the store of cache case c if it applies to o.
func (vm *VM) cachedPut(c *putByIdCache, o *Object, v Value, direct bool) bool {
	s := o.props.shape
	if s != c.shape || s.version != c.version {
		return false
	}
	if c.newShape == nil {
		// The shape fixes the attributes: the slot was a writable data
		// property when the cache was filled and still is.
		o.props.vals[c.index] = v
		return true
	}
	// Adding to a watched object changes its layout: the slow path bumps
	// the epoch.
	if o.extensible && !o.watched && (direct || vm.protoChainMatches(c, o)) {
		o.props.shape = c.newShape
		o.props.vals = append(o.props.vals, v)
		return true
	}
	return false
}

func (vm *VM) protoChainMatches(c *putByIdCache, o *Object) bool {
	p := o.proto
	if c.depth == 0 {
		return p == nil || c.holder
	}
	if p != c.chain[0].obj {
		return false
	}
	if c.epoch == vm.protoEpoch {
		return true
	}
	for i := range int(c.depth) {
		if !c.chain[i].matches(p) {
			return false
		}
		p = p.proto
	}
	if p == nil || c.holder {
		c.epoch = vm.protoEpoch
		return true
	}
	return false
}

func (vm *VM) putByIdSlow(base Value, k PropertyKey, v Value, direct, strict bool) {
	if direct {
		vm.putDirect(base, k, v)
	} else {
		vm.putByKey(base, k, v, strict)
	}
}

// fillPutById caches the store just done on o, whose shape was before.
func (vm *VM) fillPutById(c *putByIdCache, o *Object, before *shape, k PropertyKey, direct bool) {
	after := o.props.shape
	chain := c.chain[:0]
	clear(chain[:cap(chain)])
	*c = putByIdCache{chain: chain}
	if after == before {
		if i := after.lookup(k); i >= 0 && after.flags[i]&(accessorFlag|lazyFlag|Writable) == Writable {
			*c = putByIdCache{shape: after, version: after.version, index: int32(i), chain: chain}
		}
		return
	}
	// An added property: cacheable when the transition is shared.
	if before.dictionary || after.dictionary || len(after.keys) != len(before.keys)+1 ||
		after.via != (transitionKey{k, DefaultFlags, tkAdd}) {
		return
	}
	nc := putByIdCache{shape: before, version: before.version, newShape: after, chain: chain}
	if !direct {
		p := o.proto
		for p != nil {
			if int(nc.depth) == maxGetProtoDepth || !icOrdinary(p, k) || p.props.shape == nil {
				clear(nc.chain)
				return
			}
			nc.chain = append(nc.chain, linkFor(p))
			nc.depth++
			if i := p.props.find(k); i >= 0 {
				// OrdinarySet stops here: a writable data property means an own
				// property on o, anything else is not cached.
				if p.props.shape.flags[i]&(accessorFlag|lazyFlag|Writable) != Writable {
					return
				}
				nc.holder = true
				break
			}
			p = p.proto
		}
		vm.watchChain(nc.chain)
		nc.epoch = vm.protoEpoch
	}
	*c = nc
}

// lookup returns the cached property of o (a receiver whose own and
// prototype shapes must still match the cache).
func (c *getByIdCache) lookup(vm *VM, o *Object, receiver Value) (Value, bool) {
	s := o.props.shape
	if s != c.shape || s.version != c.version {
		return Undefined, false
	}
	holder := o
	if c.depth > 0 {
		if o.proto != c.chain[0].obj {
			return Undefined, false
		}
		if c.epoch == vm.protoEpoch {
			holder = c.holder
		} else {
			for i := range int(c.depth) {
				l := &c.chain[i]
				if !l.matches(holder.proto) {
					return Undefined, false
				}
				holder = l.obj
			}
			c.epoch = vm.protoEpoch
		}
	}
	if c.miss {
		return Undefined, c.depth > 0 || holder.proto == nil
	}
	v := holder.props.vals[c.index]
	if !c.accessor {
		return v, true
	}
	return Property{Value: v, Flags: accessorFlag}.getValue(receiver), true
}

// --- Global variables (JSC's GlobalProperty resolution) --------------------------------

// resolveScopeCache remembers which global scope a global variable access
// resolved to. The resolve types it is used for were proven at compile time
// not to be bound by any enclosing function or block scope, so the answer is
// the global lexical scope or the global object, and it only changes when a
// script adds global lexical bindings.
//
// The bytecompiler compiles a function without its enclosing functions'
// scopes, so it also emits global resolve types for their variables; JSC
// rewrites those to ClosureVar when it links the code block to its scope
// (JSScope::abstractResolve). Code blocks here are linked per realm, so the
// cache does it instead: it remembers how many scopes up the binding scope
// is and its symbol table, which are the same for every run of the
// instruction unless eval or with injects a binding on the way.
type resolveScopeCache struct {
	realm  *Realm
	gen    uint64
	result *Scope
	// scope is the last scope seen without var injection on its chain, at
	// VM.varInjection inj: the answer for it holds while inj is current.
	scope *Scope
	inj   uint64
	// table and depth describe a function or block scope that binds the
	// variable (table is nil if result is used instead).
	table *bytecode.SymbolTable
	depth int
}

// scopeSlotCache is get_from_scope's and put_to_scope's cache for a
// variable of a function, block or the global lexical scope: the slot it
// has in scopes with symbol table table.
type scopeSlotCache struct {
	table  *bytecode.SymbolTable
	offset int
}

// isGlobalResolveType reports the resolve types whose scope is the realm's
// global lexical scope or global object (no eval can inject variables).
func isGlobalResolveType(t bytecode.ResolveType) bool {
	switch t {
	case bytecode.GlobalProperty, bytecode.GlobalVar, bytecode.GlobalLexicalVar, bytecode.UnresolvedProperty:
		return true
	}
	return false
}

// resolveGlobalScope is resolve_scope for a global resolve type.
func (vm *VM) resolveGlobalScope(c *resolveScopeCache, r *Realm, scope *Scope, name ident.Identifier) *Scope {
	if c.table != nil {
		if s := closureScope(scope, c.depth); s != nil && s.table == c.table {
			return s
		}
	} else if c.realm == r && c.gen == r.globalLexicalGen {
		if c.scope == scope && c.inj == vm.varInjection {
			return c.result
		}
		if noVarInjection(scope, name) {
			c.scope, c.inj = scope, vm.varInjection
			return c.result
		}
	}
	result := vm.resolveScope(scope, name)
	// Cache only answers that are the realm's own global scopes or function
	// and block scopes.
	switch {
	case result == r.globalLexical || result == r.globalScope:
		*c = resolveScopeCache{realm: r, gen: r.globalLexicalGen, result: result}
		if noVarInjection(scope, name) {
			c.scope, c.inj = scope, vm.varInjection
		}
	case result.kind == scopeLexical && hasSlot(result, name):
		// The symbol table binds the name in every scope it describes (unlike
		// eval-created bindings).
		depth := 0
		for s := scope; s != result; s = s.parent {
			depth++
		}
		if closureScope(scope, depth) == result {
			*c = resolveScopeCache{table: result.table, depth: depth}
		}
	}
	return result
}

// closureScope walks depth scopes up from scope, or returns nil if one of
// the scopes on the way could have gained a binding at run time.
func closureScope(scope *Scope, depth int) *Scope {
	s := scope
	for ; depth > 0; depth-- {
		if s.kind != scopeLexical || s.evalVars != nil {
			return nil
		}
		s = s.parent
	}
	if s.kind != scopeLexical {
		return nil
	}
	return s
}

// scopeSlot returns the slot of name in a function or block scope through
// cache c.
func (c *scopeSlotCache) slot(s *Scope, name ident.Identifier, forPut bool) *Value {
	if s.table == c.table {
		return &s.slots[c.offset]
	}
	slot, e, ok := s.slotFor(name)
	if !ok || forPut && e.ReadOnly {
		return nil
	}
	*c = scopeSlotCache{table: s.table, offset: int(e.Offset.ScopeOffset())}
	return slot
}

// getGlobalProperty is get_from_scope on the global object.
func (vm *VM) getGlobalProperty(c *getByIdCache, scope *Scope, k PropertyKey, mode bytecode.ResolveMode) Value {
	o := scope.object
	if v, ok := c.lookup(vm, o, ObjectValue(o)); ok {
		return v
	}
	v := vm.getFromScope(scope, k.Ident(), mode)
	vm.fillGetById(c, o, k, false)
	return v
}

// putGlobalProperty is put_to_scope on the global object. Only overwriting
// an existing writable property is cached: creating one has to go through
// the strict mode ReferenceError check.
func (vm *VM) putGlobalProperty(c *putByIdCache, scope *Scope, k PropertyKey, v Value, info bytecode.GetPutInfo) {
	o := scope.object
	if s := o.props.shape; s == c.shape && s.version == c.version && c.newShape == nil {
		o.props.vals[c.index] = v // the shape fixes the attributes
		return
	}
	before := o.props.shape
	vm.putToScope(scope, k.Ident(), v, info)
	vm.fillPutById(c, o, before, k, false)
	if c.newShape != nil {
		*c = putByIdCache{}
	}
}

// noVarInjection reports whether no scope between scope and the global
// scopes can bind name at run time although the compiler proved it does
// not bind it statically: vars added by sloppy direct eval, with scopes
// and module imports (JSC's var injection watchpoint).
func noVarInjection(scope *Scope, name ident.Identifier) bool {
	for s := scope; s != nil; s = s.parent {
		switch s.kind {
		case scopeGlobalLexical, scopeGlobalObject:
			return true
		case scopeWith:
			return false
		case scopeModule:
			if s.module != nil && s.module.hasImportBinding(name) {
				return false
			}
		}
		if s.evalVars != nil {
			return false
		}
	}
	return true
}

var prototypeKey = StrKey("prototype")

// defaultThisCapacity is the inline capacity of the objects of
// constructors the bytecompiler saw add no properties to this. Like JSC's
// ObjectAllocationProfile, assume a helper initializes them (empty objects
// are rare): Class.create's constructors call this.initialize, and the
// objects would otherwise grow at their first property.
const defaultThisCapacity = 4

// thisCapacity is the inline capacity of the objects create_this makes
// with the bytecompiler's capacity n.
func thisCapacity(n uint32) uint32 {
	if n == 0 {
		return defaultThisCapacity
	}
	return n
}

// newObjectWithCapacity creates an ordinary object with room for n
// properties (op_new_object's and op_create_this's inline capacity, from
// the bytecompiler's static property analysis).
func (r *Realm) newObjectWithCapacity(proto *Object, n uint32) *Object {
	var o *Object
	var slots []Value
	// Inline storage: the object and its first property slots in one
	// allocation (JSC's inline capacity). Larger objects grow out of it.
	switch {
	case n == 0:
		return r.newObject(proto)
	case n <= 2:
		x := new(struct {
			o Object
			s [2]Value
		})
		o, slots = &x.o, x.s[:0]
	case n <= 4:
		x := new(struct {
			o Object
			s [4]Value
		})
		o, slots = &x.o, x.s[:0]
	case n <= 8:
		x := new(struct {
			o Object
			s [8]Value
		})
		o, slots = &x.o, x.s[:0]
	default:
		o = r.newObject(proto)
		o.props.vals = make([]Value, 0, n)
		return o
	}
	*o = Object{realm: r, proto: proto, extensible: true, props: propertyMap{shape: r.vm.rootShape, vals: slots}}
	return o
}

// createThis is op_create_this: OrdinaryCreateFromConstructor(newTarget,
// "%Object.prototype%") with newTarget.prototype read through a cache.
func (vm *VM) createThis(c *getByIdCache, newTarget *Object, capacity uint32) *Object {
	capacity = thisCapacity(capacity)
	proto := vm.getById(c, ObjectValue(newTarget), prototypeKey).AsObject()
	if proto == nil {
		proto = vm.functionRealm(newTarget).ObjectPrototype
	}
	return vm.realm.newObjectWithCapacity(proto, capacity)
}
