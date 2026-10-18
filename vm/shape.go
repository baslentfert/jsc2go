package vm

// Shapes (JSC's Structure, QuickJS's JSShape): the property layout of an
// object — its own property keys in insertion order, with their
// attributes — is shared by every object whose properties were added in the
// same order with the same attributes, through a tree of transitions from an
// empty root. An object stores only the values, so a property's position
// and attributes are properties of the shape, which is what inline caches
// remember.
//
// Deleting a property, changing its attributes, sealing and freezing are
// shared transitions too (JSC's removePropertyTransition,
// attributeChangeTransition, sealTransition and freezeTransition), so
// objects that do the same keep sharing a shape. Objects with many
// properties, or whose shape is more than maxTransitionLength transitions
// from the root, get a dictionary shape of their own that is mutated in
// place; its version changes with every change so that caches can still
// validate it.
//
// Transitions are weak, like JSC's StructureTransitionTable: a shape that
// no object or cache uses any more is collected, so programs that make
// objects with ever new keys (dictionaries, JSON, private names of class
// evaluations) do not grow the tree forever. Transitions made while a realm
// is built are strong instead: the built-ins live as long as the VM, and
// making weak pointers takes a runtime-wide lock that VMs created in
// parallel would contend for.

import (
	"slices"
	"weak"
)

// shapeLinearLimit is the size up to which keys are searched linearly.
const shapeLinearLimit = 8

// maxTransitionLength is the number of transitions from the root beyond
// which objects switch to a dictionary shape (JSC's s_maxTransitionLength):
// with only adds, the number of keys.
const maxTransitionLength = 64

// transitionKind is what a transition does.
type transitionKind uint8

const (
	tkAdd    transitionKind = iota // add key with flags
	tkRemove                       // remove key
	tkFlags                        // change key's attributes to flags
	tkSeal                         // make every property non-configurable
	tkFreeze                       // and every data property read-only
)

type transitionKey struct {
	key   PropertyKey
	flags PropertyFlags
	kind  transitionKind
}

type shape struct {
	keys  []PropertyKey
	flags []PropertyFlags       // attributes of keys[i]
	index map[PropertyKey]int32 // when len(keys) > shapeLinearLimit

	// Transitions to the shapes with one more property; single holds the
	// first one so that the common case needs no map. purgedLen is the
	// size of transitions after its dead entries were last removed.
	single      transition
	transitions map[transitionKey]transition
	purgedLen   int

	tree *shapeTree // nil for an unshared root

	dictionary bool
	version    uint32 // bumped on every in-place change of a dictionary shape

	// via is the transition that made this shape from its parent, and
	// depth the number of transitions from the root.
	via   transitionKey
	depth uint16

	// enum caches for-in's keys for objects of this shape (enumerator.go).
	enum *enumCache

	// lazy is set when a key has lazyFlag (a function's prototype not made
	// yet), so that ordinaryGetOwnProperty tests one bool on other shapes.
	lazy bool
}

// shapeTree is the state shared by the shapes of one VM.
type shapeTree struct {
	// buildingRealms is non-zero while a realm's built-ins are made.
	buildingRealms int
}

// transition is an edge of the shape tree, strong or weak.
type transition struct {
	strong *shape
	weak   weak.Pointer[shape]
}

func (t transition) get() *shape {
	if t.strong != nil {
		return t.strong
	}
	return t.weak.Value()
}

// purgeWeak returns a copy of m without the entries whose referents were
// collected (a new map: deleting would keep the old one's memory).
func purgeWeak[K comparable, V any](m map[K]V, alive func(V) bool) map[K]V {
	live := make(map[K]V)
	for k, v := range m {
		if alive(v) {
			live[k] = v
		}
	}
	return live
}

func newRootShape(tree *shapeTree) *shape { return &shape{tree: tree} }

func (s *shape) lookup(k PropertyKey) int {
	if s.index != nil {
		if i, ok := s.index[k]; ok {
			return int(i)
		}
		return -1
	}
	for i := range s.keys {
		if s.keys[i] == k {
			return i
		}
	}
	return -1
}

func (s *shape) buildIndex() {
	if len(s.keys) <= shapeLinearLimit {
		s.index = nil
		return
	}
	s.index = make(map[PropertyKey]int32, len(s.keys))
	for i, k := range s.keys {
		s.index[k] = int32(i)
	}
}

// ownCopy returns a dictionary shape with s's properties, owned by one
// object.
func (s *shape) ownCopy() *shape {
	if s.dictionary {
		return s
	}
	d := &shape{keys: append([]PropertyKey(nil), s.keys...), flags: append([]PropertyFlags(nil), s.flags...), dictionary: true, lazy: s.lazy}
	d.buildIndex()
	return d
}

// withKey returns the shape after adding k (not present yet) with flags.
func (s *shape) withKey(k PropertyKey, flags PropertyFlags) *shape {
	if s.dictionary {
		s.lazy = s.lazy || flags&lazyFlag != 0
		s.keys = append(s.keys, k)
		s.flags = append(s.flags, flags)
		if s.index != nil {
			s.index[k] = int32(len(s.keys) - 1)
		} else if len(s.keys) > shapeLinearLimit {
			s.buildIndex()
		}
		s.version++
		return s
	}
	return s.transition(transitionKey{k, flags, tkAdd}, func() *shape {
		n := len(s.keys)
		keys := make([]PropertyKey, n+1, n+1+n/4)
		copy(keys, s.keys)
		keys[n] = k
		fl := make([]PropertyFlags, n+1, n+1+n/4)
		copy(fl, s.flags)
		fl[n] = flags
		child := &shape{keys: keys, flags: fl, lazy: s.lazy || flags&lazyFlag != 0}
		child.buildIndex()
		return child
	})
}

// transition returns the shared shape after transition tk from s, made by
// build when it does not exist yet (or a dictionary shape of the object's
// own when s is too many transitions from the root).
func (s *shape) transition(tk transitionKey, build func() *shape) *shape {
	if c := s.single.get(); c != nil && c.via == tk {
		return c
	}
	if t, ok := s.transitions[tk]; ok {
		if c := t.get(); c != nil {
			return c
		}
	}
	child := build()
	child.tree, child.via, child.depth = s.tree, tk, s.depth+1
	if child.depth > maxTransitionLength {
		// Too far to share: this object gets a layout of its own.
		return child.ownCopy()
	}
	var t transition
	if s.tree != nil && s.tree.buildingRealms > 0 {
		t.strong = child
	} else {
		t.weak = weak.Make(child)
	}
	if s.single.get() == nil {
		s.single = t
	} else {
		if s.transitions == nil {
			s.transitions = make(map[transitionKey]transition)
		}
		if len(s.transitions) >= 2*max(s.purgedLen, 8) {
			s.transitions = purgeWeak(s.transitions, func(t transition) bool { return t.get() != nil })
			s.purgedLen = len(s.transitions)
		}
		s.transitions[tk] = t
	}
	return child
}

// withFlags returns the shape after changing the attributes
// of the property at i.
func (s *shape) withFlags(i int, flags PropertyFlags) *shape {
	if !s.dictionary {
		return s.transition(transitionKey{s.keys[i], flags, tkFlags}, func() *shape {
			fl := slices.Clone(s.flags)
			fl[i] = flags
			return s.sameKeys(fl)
		})
	}
	s.flags[i] = flags
	s.lazy = slices.ContainsFunc(s.flags, func(f PropertyFlags) bool { return f&lazyFlag != 0 })
	s.version++
	return s
}

// sameKeys returns a shape with s's keys (shared: shapes that are not
// dictionaries never change them) and attributes flags.
func (s *shape) sameKeys(flags []PropertyFlags) *shape {
	return &shape{keys: s.keys, flags: flags, index: s.index, lazy: slices.ContainsFunc(flags, func(f PropertyFlags) bool { return f&lazyFlag != 0 })}
}

// withIntegrity returns the shape after sealing (level Sealed) or freezing
// all properties but private names; s itself when that changes nothing.
func (s *shape) withIntegrity(level IntegrityLevel) *shape {
	kind := tkSeal
	if level == Frozen {
		kind = tkFreeze
	}
	seal := func(i int) PropertyFlags {
		f := s.flags[i]
		if s.keys[i].IsPrivate() {
			return f
		}
		f &^= Configurable
		if level == Frozen && f&accessorFlag == 0 {
			f &^= Writable
		}
		return f
	}
	changes := false
	for i, f := range s.flags {
		changes = changes || seal(i) != f
	}
	switch {
	case !changes:
		return s
	case s.dictionary:
		for i := range s.flags {
			s.flags[i] = seal(i)
		}
		s.version++
		return s
	}
	return s.transition(transitionKey{kind: kind}, func() *shape {
		fl := make([]PropertyFlags, len(s.flags))
		for i := range fl {
			fl[i] = seal(i)
		}
		return s.sameKeys(fl)
	})
}

// withoutKey returns the shape after removing the key at i.
func (s *shape) withoutKey(i int) *shape {
	if !s.dictionary {
		return s.transition(transitionKey{key: s.keys[i], kind: tkRemove}, func() *shape {
			keys := slices.Delete(slices.Clone(s.keys), i, i+1)
			fl := slices.Delete(slices.Clone(s.flags), i, i+1)
			child := &shape{keys: keys, flags: fl, lazy: slices.ContainsFunc(fl, func(f PropertyFlags) bool { return f&lazyFlag != 0 })}
			child.buildIndex()
			return child
		})
	}
	s.keys = append(s.keys[:i], s.keys[i+1:]...)
	s.flags = append(s.flags[:i], s.flags[i+1:]...)
	s.buildIndex()
	s.version++
	return s
}

// propertyMap holds an object's own named properties: the shared layout
// plus this object's values (vals[i] belongs to shape.keys[i]; for an
// accessor it holds the *Accessor cell).
type propertyMap struct {
	shape *shape
	vals  []Value
}

func (m *propertyMap) find(k PropertyKey) int {
	if m.shape == nil {
		return -1
	}
	return m.shape.lookup(k)
}

// at returns the property at index i.
func (m *propertyMap) at(i int) Property { return Property{Value: m.vals[i], Flags: m.shape.flags[i]} }

func (m *propertyMap) get(k PropertyKey) (Property, bool) {
	if i := m.find(k); i >= 0 {
		return m.at(i), true
	}
	return Property{}, false
}

func (m *propertyMap) has(k PropertyKey) bool { return m.find(k) >= 0 }

func (m *propertyMap) set(k PropertyKey, p Property) {
	if i := m.find(k); i >= 0 {
		m.vals[i] = p.Value
		if m.shape.flags[i] != p.Flags {
			m.shape = m.shape.withFlags(i, p.Flags)
		}
		return
	}
	if m.shape == nil {
		m.shape = newRootShape(nil) // an object made without a realm: unshared root
	}
	m.shape = m.shape.withKey(k, p.Flags)
	if m.vals == nil {
		m.vals = make([]Value, 0, 4)
	}
	m.vals = append(m.vals, p.Value)
}

func (m *propertyMap) remove(k PropertyKey) {
	i := m.find(k)
	if i < 0 {
		return
	}
	m.shape = m.shape.withoutKey(i)
	m.vals = append(m.vals[:i], m.vals[i+1:]...)
}

func (m *propertyMap) len() int { return len(m.vals) }

// all iterates the keys in insertion order.
func (m *propertyMap) all(yield func(PropertyKey) bool) {
	for i := 0; i < len(m.vals); i++ {
		if !yield(m.shape.keys[i]) {
			return
		}
	}
}
