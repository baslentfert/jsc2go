package vm

import (
	"math"
	"sort"
)

// Map, Set, WeakMap, WeakSet, WeakRef and FinalizationRegistry
// (JSMap/JSSet with their OrderedHashTable, JSWeakMap, ...).

// mapKey normalizes a value for SameValueZero lookups.
type mapKey struct {
	kind Kind
	n    float64
	s    string
	p    any
}

func keyOf(v Value) mapKey {
	switch v.Kind() {
	case KindNumber:
		n := v.num()
		if n == 0 {
			n = 0 // -0 and +0 are the same key
		}
		if math.IsNaN(n) {
			return mapKey{kind: KindNumber, s: "NaN"}
		}
		return mapKey{kind: KindNumber, n: n}
	case KindString:
		return mapKey{kind: KindString, s: v.StringRef().String()}
	case KindBigInt:
		return mapKey{kind: KindBigInt, s: v.BigIntRef().String()}
	case KindBool:
		return mapKey{kind: KindBool, n: v.boolNumber()}
	case KindUndefined, KindNull:
		return mapKey{kind: v.Kind()}
	}
	return mapKey{kind: v.Kind(), p: v.p}
}

type orderedEntry struct {
	key, value Value
	seq        uint64
	deleted    bool
}

// orderedMap is an insertion-ordered hash table whose iterators survive
// deletions, additions and compaction (they remember a sequence number).
type orderedMap struct {
	entries []orderedEntry
	index   map[mapKey]int
	nextSeq uint64
	deleted int
	size    int
}

func newOrderedMap() *orderedMap { return &orderedMap{index: make(map[mapKey]int)} }

func (m *orderedMap) get(k Value) (Value, bool) {
	if i, ok := m.index[keyOf(k)]; ok {
		return m.entries[i].value, true
	}
	return Undefined, false
}

func (m *orderedMap) has(k Value) bool {
	_, ok := m.index[keyOf(k)]
	return ok
}

func (m *orderedMap) set(k, v Value) {
	key := keyOf(k)
	if i, ok := m.index[key]; ok {
		m.entries[i].value = v
		return
	}
	if k.IsNumber() && k.num() == 0 {
		k = Number(0)
	}
	m.nextSeq++
	m.entries = append(m.entries, orderedEntry{key: k, value: v, seq: m.nextSeq})
	m.index[key] = len(m.entries) - 1
	m.size++
}

func (m *orderedMap) delete(k Value) bool {
	key := keyOf(k)
	i, ok := m.index[key]
	if !ok {
		return false
	}
	delete(m.index, key)
	m.entries[i] = orderedEntry{seq: m.entries[i].seq, deleted: true}
	m.deleted++
	m.size--
	if m.deleted > 32 && m.deleted > len(m.entries)/2 {
		m.compact()
	}
	return true
}

func (m *orderedMap) clear() {
	for i := range m.entries {
		m.entries[i] = orderedEntry{seq: m.entries[i].seq, deleted: true}
	}
	clear(m.index)
	m.deleted = len(m.entries)
	m.size = 0
	m.compact()
}

func (m *orderedMap) compact() {
	n := 0
	for _, e := range m.entries {
		if !e.deleted {
			m.entries[n] = e
			m.index[keyOf(e.key)] = n
			n++
		}
	}
	clear(m.entries[n:])
	m.entries = m.entries[:n]
	m.deleted = 0
}

// cursor is an iterator position: the sequence number of the last entry
// visited.
type cursor struct {
	m    *orderedMap
	last uint64
	done bool
}

func (c *cursor) next() (orderedEntry, bool) {
	if c.done || c.m == nil {
		return orderedEntry{}, false
	}
	entries := c.m.entries
	i := sort.Search(len(entries), func(i int) bool { return entries[i].seq > c.last })
	for ; i < len(entries); i++ {
		if !entries[i].deleted {
			c.last = entries[i].seq
			return entries[i], true
		}
	}
	c.done = true
	return orderedEntry{}, false
}

func (r *Realm) initMapSet() {
	vm := r.vm

	// Map
	mapProto := r.NewObject()
	r.MapPrototype = mapProto
	mapCtor := r.constructor("Map", 0, func(c *Call) Value {
		if c.NewTarget == nil {
			vm.throwTypeError("Map constructor requires 'new'")
		}
		o := vm.OrdinaryCreateFromConstructor(c.NewTarget, func(r *Realm) *Object { return r.MapPrototype })
		o.class = ClassMap
		o.internal = newOrderedMap()
		if it := c.Arg(0); !it.IsNullish() {
			adder := o.GetStr("set")
			if !adder.IsCallable() {
				vm.throwTypeError("Map.prototype.set is not a function")
			}
			vm.addEntriesFromIterable(ObjectValue(o), it, adder)
		}
		return ObjectValue(o)
	}, mapProto)
	r.speciesGetter(mapCtor)
	thisMap := func(c *Call, name string) *orderedMap {
		if o := c.This.AsObject(); o != nil && o.class == ClassMap {
			return o.internal.(*orderedMap)
		}
		vm.throwTypeError("Map.prototype.%s called on incompatible receiver", name)
		return nil
	}
	r.method(mapProto, "clear", 0, func(c *Call) Value { thisMap(c, "clear").clear(); return Undefined })
	r.method(mapProto, "delete", 1, func(c *Call) Value { return Bool(thisMap(c, "delete").delete(c.Arg(0))) })
	setFast(r.method(mapProto, "get", 1, func(c *Call) Value {
		v, _ := thisMap(c, "get").get(c.Arg(0))
		return v
	}), func(vm *VM, this Value, args []Value) (Value, bool) {
		if o := this.AsObject(); o != nil && o.class == ClassMap {
			v, _ := o.internal.(*orderedMap).get(argAt(args, 0))
			return v, true
		}
		return Undefined, false
	})
	setFast(r.method(mapProto, "has", 1, func(c *Call) Value { return Bool(thisMap(c, "has").has(c.Arg(0))) }),
		func(vm *VM, this Value, args []Value) (Value, bool) {
			if o := this.AsObject(); o != nil && o.class == ClassMap {
				return Bool(o.internal.(*orderedMap).has(argAt(args, 0))), true
			}
			return Undefined, false
		})
	r.method(mapProto, "set", 2, func(c *Call) Value {
		thisMap(c, "set").set(c.Arg(0), c.Arg(1))
		return c.This
	})
	r.method(mapProto, "getOrInsert", 2, func(c *Call) Value {
		m := thisMap(c, "getOrInsert")
		if v, ok := m.get(c.Arg(0)); ok {
			return v
		}
		m.set(c.Arg(0), c.Arg(1))
		return c.Arg(1)
	})
	r.method(mapProto, "getOrInsertComputed", 2, func(c *Call) Value {
		m := thisMap(c, "getOrInsertComputed")
		if !c.Arg(1).IsCallable() {
			vm.throwTypeError("Map.prototype.getOrInsertComputed requires a callback")
		}
		if v, ok := m.get(c.Arg(0)); ok {
			return v
		}
		key := c.Arg(0)
		if key.IsNumber() && key.Float() == 0 {
			key = Number(0)
		}
		v := vm.Call(c.Arg(1), Undefined, key)
		m.set(key, v)
		return v
	})
	r.getter(mapProto, StrKey("size"), func(c *Call) Value { return Int(thisMap(c, "size").size) })
	r.method(mapProto, "forEach", 1, func(c *Call) Value {
		m := thisMap(c, "forEach")
		f := c.Arg(0)
		if !f.IsCallable() {
			vm.throwTypeError("Map.prototype.forEach callback must be a function")
		}
		cur := &cursor{m: m}
		for e, ok := cur.next(); ok; e, ok = cur.next() {
			vm.Call(f, c.Arg(1), e.value, e.key, c.This)
		}
		return Undefined
	})
	r.MapIteratorPrototype = r.newObject(r.IteratorPrototype)
	r.toStringTag(r.MapIteratorPrototype, "Map Iterator")
	r.method(r.MapIteratorPrototype, "next", 0, func(c *Call) Value {
		return vm.collectionIteratorNext(c, "Map Iterator")
	})
	mapIter := func(name string, kind arrayIteratorKind) *Object {
		return r.method(mapProto, name, 0, func(c *Call) Value {
			m := thisMap(c, name)
			it := c.Realm().newObject(c.Realm().MapIteratorPrototype)
			it.class = ClassIterator
			it.internal = &collectionIterator{cur: &cursor{m: m}, kind: kind, tag: "Map Iterator"}
			return ObjectValue(it)
		})
	}
	mapIter("keys", iterateKeys)
	mapIter("values", iterateValues)
	entries := mapIter("entries", iterateEntries)
	mapProto.putDirect(SymKey(SymbolIterator), ObjectValue(entries), BuiltinFlags)
	r.toStringTag(mapProto, "Map")
	r.method(mapCtor, "groupBy", 2, func(c *Call) Value {
		groups := vm.groupBy(c.Arg(0), c.Arg(1), false)
		o := c.Realm().newObject(c.Realm().MapPrototype)
		o.class = ClassMap
		m := newOrderedMap()
		o.internal = m
		for _, g := range groups {
			m.set(g.key, ObjectValue(c.Realm().NewArray(g.values...)))
		}
		return ObjectValue(o)
	})

	// Set
	setProto := r.NewObject()
	r.SetPrototype = setProto
	setCtor := r.constructor("Set", 0, func(c *Call) Value {
		if c.NewTarget == nil {
			vm.throwTypeError("Set constructor requires 'new'")
		}
		o := vm.OrdinaryCreateFromConstructor(c.NewTarget, func(r *Realm) *Object { return r.SetPrototype })
		o.class = ClassSet
		o.internal = newOrderedMap()
		if it := c.Arg(0); !it.IsNullish() {
			adder := o.GetStr("add")
			if !adder.IsCallable() {
				vm.throwTypeError("Set.prototype.add is not a function")
			}
			vm.iterate(it, func(v Value) bool {
				vm.Call(adder, ObjectValue(o), v)
				return true
			})
		}
		return ObjectValue(o)
	}, setProto)
	r.speciesGetter(setCtor)
	thisSet := func(c *Call, name string) *orderedMap {
		if o := c.This.AsObject(); o != nil && o.class == ClassSet {
			return o.internal.(*orderedMap)
		}
		vm.throwTypeError("Set.prototype.%s called on incompatible receiver", name)
		return nil
	}
	r.method(setProto, "add", 1, func(c *Call) Value {
		m := thisSet(c, "add")
		if !m.has(c.Arg(0)) {
			m.set(c.Arg(0), c.Arg(0))
		}
		return c.This
	})
	r.method(setProto, "clear", 0, func(c *Call) Value { thisSet(c, "clear").clear(); return Undefined })
	r.method(setProto, "delete", 1, func(c *Call) Value { return Bool(thisSet(c, "delete").delete(c.Arg(0))) })
	r.method(setProto, "has", 1, func(c *Call) Value { return Bool(thisSet(c, "has").has(c.Arg(0))) })
	r.getter(setProto, StrKey("size"), func(c *Call) Value { return Int(thisSet(c, "size").size) })
	r.method(setProto, "forEach", 1, func(c *Call) Value {
		m := thisSet(c, "forEach")
		f := c.Arg(0)
		if !f.IsCallable() {
			vm.throwTypeError("Set.prototype.forEach callback must be a function")
		}
		cur := &cursor{m: m}
		for e, ok := cur.next(); ok; e, ok = cur.next() {
			vm.Call(f, c.Arg(1), e.key, e.key, c.This)
		}
		return Undefined
	})
	r.SetIteratorPrototype = r.newObject(r.IteratorPrototype)
	r.toStringTag(r.SetIteratorPrototype, "Set Iterator")
	r.method(r.SetIteratorPrototype, "next", 0, func(c *Call) Value {
		return vm.collectionIteratorNext(c, "Set Iterator")
	})
	setIter := func(name string, kind arrayIteratorKind) *Object {
		return r.method(setProto, name, 0, func(c *Call) Value {
			m := thisSet(c, name)
			it := c.Realm().newObject(c.Realm().SetIteratorPrototype)
			it.class = ClassIterator
			it.internal = &collectionIterator{cur: &cursor{m: m}, kind: kind, tag: "Set Iterator"}
			return ObjectValue(it)
		})
	}
	values := setIter("values", iterateValues)
	setProto.putDirect(StrKey("keys"), ObjectValue(values), BuiltinFlags)
	setIter("entries", iterateEntries)
	setProto.putDirect(SymKey(SymbolIterator), ObjectValue(values), BuiltinFlags)
	r.toStringTag(setProto, "Set")
	r.initSetMethods(setProto, thisSet)
}

type collectionIterator struct {
	cur  *cursor
	kind arrayIteratorKind
	tag  string
}

func (vm *VM) collectionIteratorNext(c *Call, tag string) Value {
	o := c.This.AsObject()
	var it *collectionIterator
	if o != nil {
		it, _ = o.internal.(*collectionIterator)
	}
	if it == nil || it.tag != tag {
		vm.throwTypeError("%%%sPrototype%%.next requires that |this| be a %s", tag, tag)
	}
	e, ok := it.cur.next()
	if !ok {
		return c.Realm().iterResult(Undefined, true)
	}
	switch it.kind {
	case iterateKeys:
		return c.Realm().iterResult(e.key, false)
	case iterateValues:
		return c.Realm().iterResult(e.value, false)
	}
	return c.Realm().iterResult(ObjectValue(c.Realm().NewArray(e.key, e.value)), false)
}

// addEntriesFromIterable (24.1.1.2).
func (vm *VM) addEntriesFromIterable(target Value, iterable Value, adder Value) {
	vm.iterate(iterable, func(item Value) bool {
		o := item.AsObject()
		if o == nil {
			vm.throwTypeError("Iterator value %s is not an entry object", vm.describeForError(item))
		}
		k := o.getIndex(0)
		v := o.getIndex(1)
		vm.Call(adder, target, k, v)
		return true
	})
}

// --- Set methods (union, intersection, ...) -------------------------------------------

type setRecord struct {
	obj  *Object
	size float64
	has  Value
	keys Value
}

// getSetRecord (24.2.1.2).
func (vm *VM) getSetRecord(v Value) *setRecord {
	o := v.AsObject()
	if o == nil {
		vm.throwTypeError("Set operation argument must be an object")
	}
	rawSize := o.GetStr("size")
	numSize := vm.ToNumber(rawSize)
	if math.IsNaN(numSize) {
		vm.throwTypeError("Set operation argument has an invalid size")
	}
	intSize := integerOrInfinity(numSize)
	if intSize < 0 {
		vm.throwRangeError("Set operation argument has a negative size")
	}
	has := o.GetStr("has")
	if !has.IsCallable() {
		vm.throwTypeError("Set operation argument has no callable 'has'")
	}
	keys := o.GetStr("keys")
	if !keys.IsCallable() {
		vm.throwTypeError("Set operation argument has no callable 'keys'")
	}
	return &setRecord{obj: o, size: intSize, has: has, keys: keys}
}

func (vm *VM) setRecordKeys(rec *setRecord) *iteratorRecord {
	it := vm.Call(rec.keys, ObjectValue(rec.obj))
	o := it.AsObject()
	if o == nil {
		vm.throwTypeError("keys() did not return an object")
	}
	return vm.getIteratorDirect(o)
}

func (r *Realm) initSetMethods(proto *Object, thisSet func(*Call, string) *orderedMap) {
	vm := r.vm
	newSet := func(c *Call, m *orderedMap) Value {
		o := c.Realm().newObject(c.Realm().SetPrototype)
		o.class = ClassSet
		o.internal = m
		return ObjectValue(o)
	}
	copySet := func(m *orderedMap) *orderedMap {
		n := newOrderedMap()
		cur := &cursor{m: m}
		for e, ok := cur.next(); ok; e, ok = cur.next() {
			n.set(e.key, e.key)
		}
		return n
	}
	nextKey := func(it *iteratorRecord) (Value, bool) {
		v, ok := vm.step(it)
		if ok && v.IsNumber() && v.Float() == 0 {
			v = Number(0)
		}
		return v, ok
	}
	r.method(proto, "union", 1, func(c *Call) Value {
		m := thisSet(c, "union")
		other := vm.getSetRecord(c.Arg(0))
		it := vm.setRecordKeys(other)
		result := copySet(m)
		for v, ok := nextKey(it); ok; v, ok = nextKey(it) {
			if !result.has(v) {
				result.set(v, v)
			}
		}
		return newSet(c, result)
	})
	r.method(proto, "intersection", 1, func(c *Call) Value {
		m := thisSet(c, "intersection")
		other := vm.getSetRecord(c.Arg(0))
		result := newOrderedMap()
		if float64(m.size) <= other.size {
			cur := &cursor{m: m}
			for e, ok := cur.next(); ok; e, ok = cur.next() {
				if vm.Call(other.has, ObjectValue(other.obj), e.key).ToBoolean() {
					result.set(e.key, e.key)
				}
			}
		} else {
			it := vm.setRecordKeys(other)
			for v, ok := nextKey(it); ok; v, ok = nextKey(it) {
				if m.has(v) && !result.has(v) {
					result.set(v, v)
				}
			}
		}
		return newSet(c, result)
	})
	r.method(proto, "difference", 1, func(c *Call) Value {
		m := thisSet(c, "difference")
		other := vm.getSetRecord(c.Arg(0))
		result := copySet(m)
		if float64(m.size) <= other.size {
			// Iterate the copy: has() may mutate this set.
			cur := &cursor{m: result}
			for e, ok := cur.next(); ok; e, ok = cur.next() {
				if vm.Call(other.has, ObjectValue(other.obj), e.key).ToBoolean() {
					result.delete(e.key)
				}
			}
		} else {
			it := vm.setRecordKeys(other)
			for v, ok := nextKey(it); ok; v, ok = nextKey(it) {
				result.delete(v)
			}
		}
		return newSet(c, result)
	})
	r.method(proto, "symmetricDifference", 1, func(c *Call) Value {
		m := thisSet(c, "symmetricDifference")
		other := vm.getSetRecord(c.Arg(0))
		it := vm.setRecordKeys(other)
		result := copySet(m)
		for v, ok := nextKey(it); ok; v, ok = nextKey(it) {
			if m.has(v) {
				result.delete(v)
			} else if !result.has(v) {
				result.set(v, v)
			}
		}
		return newSet(c, result)
	})
	r.method(proto, "isSubsetOf", 1, func(c *Call) Value {
		m := thisSet(c, "isSubsetOf")
		other := vm.getSetRecord(c.Arg(0))
		if float64(m.size) > other.size {
			return False
		}
		cur := &cursor{m: m}
		for e, ok := cur.next(); ok; e, ok = cur.next() {
			if !vm.Call(other.has, ObjectValue(other.obj), e.key).ToBoolean() {
				return False
			}
		}
		return True
	})
	r.method(proto, "isSupersetOf", 1, func(c *Call) Value {
		m := thisSet(c, "isSupersetOf")
		other := vm.getSetRecord(c.Arg(0))
		if float64(m.size) < other.size {
			return False
		}
		it := vm.setRecordKeys(other)
		for v, ok := nextKey(it); ok; v, ok = nextKey(it) {
			if !m.has(v) {
				vm.closeIterator(it)
				return False
			}
		}
		return True
	})
	r.method(proto, "isDisjointFrom", 1, func(c *Call) Value {
		m := thisSet(c, "isDisjointFrom")
		other := vm.getSetRecord(c.Arg(0))
		if float64(m.size) <= other.size {
			cur := &cursor{m: m}
			for e, ok := cur.next(); ok; e, ok = cur.next() {
				if vm.Call(other.has, ObjectValue(other.obj), e.key).ToBoolean() {
					return False
				}
			}
			return True
		}
		it := vm.setRecordKeys(other)
		for v, ok := nextKey(it); ok; v, ok = nextKey(it) {
			if m.has(v) {
				vm.closeIterator(it)
				return False
			}
		}
		return True
	})
}

// --- Weak collections --------------------------------------------------------------------

// canBeHeldWeakly (9.13).
func canBeHeldWeakly(v Value) bool {
	switch v.Kind() {
	case KindObject:
		return true
	case KindSymbol:
		return v.SymbolRef().registered == nil
	}
	return false
}

func (r *Realm) initWeak() {
	vm := r.vm
	weakCollectionArg := func(c *Call, class Class, name string) *weakCollection {
		if o := c.This.AsObject(); o != nil && o.class == class {
			return o.internal.(*weakCollection)
		}
		vm.throwTypeError("%s called on incompatible receiver", name)
		return nil
	}

	weakMapProto := r.NewObject()
	r.WeakMapPrototype = weakMapProto
	r.constructor("WeakMap", 0, func(c *Call) Value {
		if c.NewTarget == nil {
			vm.throwTypeError("WeakMap constructor requires 'new'")
		}
		o := vm.OrdinaryCreateFromConstructor(c.NewTarget, func(r *Realm) *Object { return r.WeakMapPrototype })
		o.class = ClassWeakMap
		o.internal = vm.newWeakCollection()
		if it := c.Arg(0); !it.IsNullish() {
			adder := o.GetStr("set")
			if !adder.IsCallable() {
				vm.throwTypeError("WeakMap.prototype.set is not a function")
			}
			vm.addEntriesFromIterable(ObjectValue(o), it, adder)
		}
		return ObjectValue(o)
	}, weakMapProto)
	thisWeakMap := func(c *Call, name string) *weakCollection {
		return weakCollectionArg(c, ClassWeakMap, "WeakMap.prototype."+name)
	}
	r.method(weakMapProto, "delete", 1, func(c *Call) Value {
		m := thisWeakMap(c, "delete")
		return Bool(canBeHeldWeakly(c.Arg(0)) && m.delete(c.Arg(0)))
	})
	r.method(weakMapProto, "get", 1, func(c *Call) Value {
		m := thisWeakMap(c, "get")
		if !canBeHeldWeakly(c.Arg(0)) {
			return Undefined
		}
		v, _ := m.get(c.Arg(0))
		return v
	})
	r.method(weakMapProto, "has", 1, func(c *Call) Value {
		m := thisWeakMap(c, "has")
		if !canBeHeldWeakly(c.Arg(0)) {
			return False
		}
		_, ok := m.get(c.Arg(0))
		return Bool(ok)
	})
	r.method(weakMapProto, "set", 2, func(c *Call) Value {
		m := thisWeakMap(c, "set")
		if !canBeHeldWeakly(c.Arg(0)) {
			vm.throwTypeError("WeakMap keys must be objects or non-registered symbols")
		}
		vm.sweepWeak()
		m.set(c.Arg(0), c.Arg(1))
		return c.This
	})
	r.method(weakMapProto, "getOrInsert", 2, func(c *Call) Value {
		m := thisWeakMap(c, "getOrInsert")
		if !canBeHeldWeakly(c.Arg(0)) {
			vm.throwTypeError("WeakMap keys must be objects or non-registered symbols")
		}
		if v, ok := m.get(c.Arg(0)); ok {
			return v
		}
		vm.sweepWeak()
		m.set(c.Arg(0), c.Arg(1))
		return c.Arg(1)
	})
	r.method(weakMapProto, "getOrInsertComputed", 2, func(c *Call) Value {
		m := thisWeakMap(c, "getOrInsertComputed")
		if !canBeHeldWeakly(c.Arg(0)) {
			vm.throwTypeError("WeakMap keys must be objects or non-registered symbols")
		}
		if !c.Arg(1).IsCallable() {
			vm.throwTypeError("WeakMap.prototype.getOrInsertComputed requires a callback")
		}
		if v, ok := m.get(c.Arg(0)); ok {
			return v
		}
		v := vm.Call(c.Arg(1), Undefined, c.Arg(0))
		vm.sweepWeak()
		m.set(c.Arg(0), v)
		return v
	})
	r.toStringTag(weakMapProto, "WeakMap")

	weakSetProto := r.NewObject()
	r.WeakSetPrototype = weakSetProto
	r.constructor("WeakSet", 0, func(c *Call) Value {
		if c.NewTarget == nil {
			vm.throwTypeError("WeakSet constructor requires 'new'")
		}
		o := vm.OrdinaryCreateFromConstructor(c.NewTarget, func(r *Realm) *Object { return r.WeakSetPrototype })
		o.class = ClassWeakSet
		o.internal = vm.newWeakCollection()
		if it := c.Arg(0); !it.IsNullish() {
			adder := o.GetStr("add")
			if !adder.IsCallable() {
				vm.throwTypeError("WeakSet.prototype.add is not a function")
			}
			vm.iterate(it, func(v Value) bool {
				vm.Call(adder, ObjectValue(o), v)
				return true
			})
		}
		return ObjectValue(o)
	}, weakSetProto)
	thisWeakSet := func(c *Call, name string) *weakCollection {
		return weakCollectionArg(c, ClassWeakSet, "WeakSet.prototype."+name)
	}
	r.method(weakSetProto, "add", 1, func(c *Call) Value {
		m := thisWeakSet(c, "add")
		if !canBeHeldWeakly(c.Arg(0)) {
			vm.throwTypeError("WeakSet values must be objects or non-registered symbols")
		}
		vm.sweepWeak()
		m.set(c.Arg(0), True)
		return c.This
	})
	r.method(weakSetProto, "delete", 1, func(c *Call) Value {
		m := thisWeakSet(c, "delete")
		return Bool(canBeHeldWeakly(c.Arg(0)) && m.delete(c.Arg(0)))
	})
	r.method(weakSetProto, "has", 1, func(c *Call) Value {
		m := thisWeakSet(c, "has")
		if !canBeHeldWeakly(c.Arg(0)) {
			return False
		}
		_, ok := m.get(c.Arg(0))
		return Bool(ok)
	})
	r.toStringTag(weakSetProto, "WeakSet")

	weakRefProto := r.NewObject()
	r.WeakRefPrototype = weakRefProto
	r.constructor("WeakRef", 1, func(c *Call) Value {
		if c.NewTarget == nil {
			vm.throwTypeError("WeakRef constructor requires 'new'")
		}
		if !canBeHeldWeakly(c.Arg(0)) {
			vm.throwTypeError("WeakRef target must be an object or non-registered symbol")
		}
		o := vm.OrdinaryCreateFromConstructor(c.NewTarget, func(r *Realm) *Object { return r.WeakRefPrototype })
		o.class = ClassWeakRef
		vm.addToKeptObjects(c.Arg(0))
		o.internal = newWeakRef(c.Arg(0))
		return ObjectValue(o)
	}, weakRefProto)
	r.method(weakRefProto, "deref", 0, func(c *Call) Value {
		if o := c.This.AsObject(); o != nil && o.class == ClassWeakRef {
			v := o.internal.(*weakRef).deref()
			if !v.IsUndefined() {
				vm.addToKeptObjects(v)
			}
			return v
		}
		vm.throwTypeError("WeakRef.prototype.deref called on incompatible receiver")
		return Undefined
	})
	r.toStringTag(weakRefProto, "WeakRef")

	frProto := r.NewObject()
	r.FinalizationRegistryPrototype = frProto
	r.constructor("FinalizationRegistry", 1, func(c *Call) Value {
		if c.NewTarget == nil {
			vm.throwTypeError("FinalizationRegistry constructor requires 'new'")
		}
		if !c.Arg(0).IsCallable() {
			vm.throwTypeError("FinalizationRegistry requires a callable cleanup callback")
		}
		o := vm.OrdinaryCreateFromConstructor(c.NewTarget, func(r *Realm) *Object { return r.FinalizationRegistryPrototype })
		o.class = ClassFinalizationRegistry
		o.internal = vm.newFinalizationRegistry(c.Realm(), c.Arg(0))
		return ObjectValue(o)
	}, frProto)
	thisRegistry := func(c *Call, name string) *finalizationRegistry {
		if o := c.This.AsObject(); o != nil && o.class == ClassFinalizationRegistry {
			return o.internal.(*finalizationRegistry)
		}
		vm.throwTypeError("FinalizationRegistry.prototype.%s called on incompatible receiver", name)
		return nil
	}
	r.method(frProto, "register", 2, func(c *Call) Value {
		reg := thisRegistry(c, "register")
		target, held, token := c.Arg(0), c.Arg(1), c.Arg(2)
		if !canBeHeldWeakly(target) {
			vm.throwTypeError("FinalizationRegistry.prototype.register target must be an object or non-registered symbol")
		}
		if SameValue(target, held) {
			vm.throwTypeError("FinalizationRegistry.prototype.register target and held value must differ")
		}
		if !canBeHeldWeakly(token) && !token.IsUndefined() {
			vm.throwTypeError("FinalizationRegistry.prototype.register unregister token must be an object or non-registered symbol")
		}
		reg.register(vm, target, held, token)
		return Undefined
	})
	r.method(frProto, "unregister", 1, func(c *Call) Value {
		reg := thisRegistry(c, "unregister")
		token := c.Arg(0)
		if !canBeHeldWeakly(token) {
			vm.throwTypeError("FinalizationRegistry.prototype.unregister token must be an object or non-registered symbol")
		}
		return Bool(reg.unregister(vm, token))
	})
	r.method(frProto, "cleanupSome", 0, func(c *Call) Value {
		reg := thisRegistry(c, "cleanupSome")
		cb := c.Arg(0)
		if !cb.IsUndefined() && !cb.IsCallable() {
			vm.throwTypeError("FinalizationRegistry.prototype.cleanupSome callback must be callable")
		}
		if cb.IsUndefined() {
			cb = reg.cleanup
		}
		vm.sweepWeak()
		reg.cleanupPending(vm, cb)
		return Undefined
	})
	r.toStringTag(frProto, "FinalizationRegistry")
}
