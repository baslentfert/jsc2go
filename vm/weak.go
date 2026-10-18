package vm

import (
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"weak"
)

// Weak collections and references on Go's weak pointers (WeakMapImpl,
// JSWeakObjectRef, JSFinalizationRegistry).
//
// A WeakMap or WeakSet keeps its entries on the keys: every key object or
// symbol has an ephemeron table from (weak pointers to) collections to
// values, so a value lives exactly as long as its key and the collection
// itself only remembers its keys weakly. When a collection is collected, a
// runtime cleanup queues its key list and the VM removes the collection's
// entries from the keys still alive at its next safe point.
//
// Runtime cleanups run on another goroutine, so they only touch the
// weakQueue; they must not reference the VM, or a pending cleanup would keep
// a dropped VM alive.

// weakCollection is the state of a WeakMap or WeakSet.
type weakCollection struct {
	self weak.Pointer[weakCollection]
	keys *weakKeyList
}

// weakKeyList remembers a collection's keys, weakly, so that its entries
// can be removed from them once the collection is gone.
type weakKeyList struct {
	owner      weak.Pointer[weakCollection]
	objects    []weak.Pointer[Object]
	symbols    []weak.Pointer[Symbol]
	compactLen int // len(objects)+len(symbols) after the last compaction
	// shared is the VM's table of entries for shared symbols.
	shared sharedEphemerons
}

type ephemeronTable map[weak.Pointer[weakCollection]]Value

// sharedEphemerons are a VM's entries for the well-known symbols, which
// all VMs share: kept on the symbol, VMs running on several goroutines
// would write the same map (test262's runner did, "concurrent map
// writes"), and one VM's entries would live in another's keys.
type sharedEphemerons map[*Symbol]*ephemeronTable

// table returns the ephemeron table of symbol s for collections whose VM
// keeps shared entries in m, or nil if it has none and create is false.
func (m sharedEphemerons) table(s *Symbol, create bool) *ephemeronTable {
	if !s.shared {
		return &s.ephemerons
	}
	t := m[s]
	if t == nil && create {
		t = new(ephemeronTable)
		m[s] = t
	}
	return t
}

// weakQueue receives what runtime cleanups report: collections and
// finalization registries that were collected, and cells whose targets
// were.
type weakQueue struct {
	pending    atomic.Bool
	mu         sync.Mutex
	lists      []*weakKeyList
	cells      []*finalizationCell
	registries []*finalizationCells
}

func (q *weakQueue) push(f func()) {
	q.mu.Lock()
	f()
	q.mu.Unlock()
	q.pending.Store(true)
}

func (vm *VM) newWeakCollection() *weakCollection {
	vm.sweepWeak()
	c := &weakCollection{}
	c.self = weak.Make(c)
	if vm.sharedEphemerons == nil {
		vm.sharedEphemerons = sharedEphemerons{}
	}
	c.keys = &weakKeyList{owner: c.self, shared: vm.sharedEphemerons}
	q := vm.weakQueue
	runtime.AddCleanup(c, func(l *weakKeyList) { q.push(func() { q.lists = append(q.lists, l) }) }, c.keys)
	return c
}

// ephemerons returns the ephemeron table of a key (an object or a symbol
// that canBeHeldWeakly accepts), or nil if it has none and create is false.
func (c *weakCollection) ephemerons(key Value, create bool) *ephemeronTable {
	var t *ephemeronTable
	if o := key.AsObject(); o != nil {
		if o.extra == nil {
			if !create {
				return nil
			}
			o.extra = &objectExtra{}
		}
		t = &o.extra.ephemerons
	} else if t = c.keys.shared.table(key.SymbolRef(), create); t == nil {
		return nil
	}
	if *t == nil {
		if !create {
			return nil
		}
		*t = make(ephemeronTable)
	}
	return t
}

func (c *weakCollection) get(key Value) (Value, bool) {
	if t := c.ephemerons(key, false); t != nil {
		v, ok := (*t)[c.self]
		return v, ok
	}
	return Undefined, false
}

func (c *weakCollection) set(key, v Value) {
	t := c.ephemerons(key, true)
	if _, ok := (*t)[c.self]; !ok {
		c.keys.add(key)
	}
	(*t)[c.self] = v
}

func (c *weakCollection) delete(key Value) bool {
	t := c.ephemerons(key, false)
	if t == nil {
		return false
	}
	_, ok := (*t)[c.self]
	delete(*t, c.self)
	return ok
}

func (l *weakKeyList) add(key Value) {
	if o := key.AsObject(); o != nil {
		l.objects = append(l.objects, weak.Make(o))
	} else {
		l.symbols = append(l.symbols, weak.Make(key.SymbolRef()))
	}
	if n := len(l.objects) + len(l.symbols); n >= 2*max(l.compactLen, 16) {
		l.compact()
	}
}

// compact drops the keys that died or no longer have an entry.
func (l *weakKeyList) compact() {
	objects := l.objects[:0]
	for _, w := range l.objects {
		if o := w.Value(); o != nil && o.extra != nil {
			if _, ok := o.extra.ephemerons[l.owner]; ok {
				objects = append(objects, w)
			}
		}
	}
	clear(l.objects[len(objects):])
	l.objects = objects
	symbols := l.symbols[:0]
	for _, w := range l.symbols {
		if s := w.Value(); s != nil {
			if t := l.shared.table(s, false); t != nil {
				if _, ok := (*t)[l.owner]; ok {
					symbols = append(symbols, w)
				}
			}
		}
	}
	clear(l.symbols[len(symbols):])
	l.symbols = symbols
	l.compactLen = len(l.objects) + len(l.symbols)
	// Give back the memory of lists that shrank a lot.
	if cap(l.objects) > 4*max(len(l.objects), 16) {
		l.objects = slices.Clone(l.objects)
	}
	if cap(l.symbols) > 4*max(len(l.symbols), 16) {
		l.symbols = slices.Clone(l.symbols)
	}
}

// removeEntries removes a collected collection's entries from its keys.
func (l *weakKeyList) removeEntries() {
	for _, w := range l.objects {
		if o := w.Value(); o != nil && o.extra != nil {
			delete(o.extra.ephemerons, l.owner)
			if len(o.extra.ephemerons) == 0 {
				o.extra.ephemerons = nil
			}
		}
	}
	for _, w := range l.symbols {
		if s := w.Value(); s != nil {
			if t := l.shared.table(s, false); t != nil {
				delete(*t, l.owner)
				if len(*t) == 0 {
					if s.shared {
						delete(l.shared, s)
					} else {
						*t = nil
					}
				}
			}
		}
	}
}

// sweepWeak processes what runtime cleanups reported since the last call.
// It runs on the VM's goroutine.
func (vm *VM) sweepWeak() {
	q := vm.weakQueue
	if !q.pending.Load() {
		return
	}
	q.mu.Lock()
	q.pending.Store(false)
	lists, cells, registries := q.lists, q.cells, q.registries
	q.lists, q.cells, q.registries = nil, nil, nil
	q.mu.Unlock()
	for _, l := range lists {
		l.removeEntries()
	}
	for _, cs := range registries {
		cs.drop()
	}
	for _, cell := range cells {
		cell.targetCollected(vm)
	}
}

// --- WeakRef -------------------------------------------------------------------------------

// weakRef is a WeakRef's [[WeakRefTarget]].
type weakRef struct {
	object weak.Pointer[Object]
	symbol weak.Pointer[Symbol]
}

func newWeakRef(target Value) *weakRef {
	if o := target.AsObject(); o != nil {
		return &weakRef{object: weak.Make(o)}
	}
	return &weakRef{symbol: weak.Make(target.SymbolRef())}
}

// deref is WeakRefDeref: undefined once the target is collected.
func (r *weakRef) deref() Value {
	if o := r.object.Value(); o != nil {
		return ObjectValue(o)
	}
	if s := r.symbol.Value(); s != nil {
		return SymbolValue(s)
	}
	return Undefined
}

// addToKeptObjects is AddToKeptObjects: v stays alive until the current
// job ends (ClearKeptObjects).
func (vm *VM) addToKeptObjects(v Value) {
	if vm.keptObjects == nil {
		vm.keptObjects = make(map[Value]struct{})
	}
	vm.keptObjects[v] = struct{}{}
}

// clearKeptObjects is ClearKeptObjects, at the end of every job.
func (vm *VM) clearKeptObjects() {
	if vm.keptObjects != nil {
		vm.keptObjects = nil
	}
	vm.sweepWeak()
}

// --- FinalizationRegistry --------------------------------------------------------------------

// finalizationRegistry is a FinalizationRegistry's state.
type finalizationRegistry struct {
	realm   *Realm
	cleanup Value
	cells   *finalizationCells
	// pending holds the cells whose targets were collected, waiting for a
	// cleanup job; scheduled is whether that job is queued.
	pending   []*finalizationCell
	scheduled bool
}

// finalizationCells are a registry's registered cells. A registry's own
// runtime cleanup gets them (not the registry) to stop their cells'
// cleanups when the registry is collected.
type finalizationCells struct {
	registry weak.Pointer[finalizationRegistry]
	cells    map[*finalizationCell]struct{}
}

type finalizationCell struct {
	owner       *finalizationCells
	held        Value
	tokenObject weak.Pointer[Object]
	tokenSymbol weak.Pointer[Symbol]
	stop        runtime.Cleanup
}

func (vm *VM) newFinalizationRegistry(r *Realm, cleanup Value) *finalizationRegistry {
	vm.sweepWeak()
	reg := &finalizationRegistry{realm: r, cleanup: cleanup}
	reg.cells = &finalizationCells{registry: weak.Make(reg), cells: map[*finalizationCell]struct{}{}}
	q := vm.weakQueue
	runtime.AddCleanup(reg, func(cs *finalizationCells) { q.push(func() { q.registries = append(q.registries, cs) }) }, reg.cells)
	return reg
}

func (reg *finalizationRegistry) register(vm *VM, target, held, token Value) {
	vm.sweepWeak()
	cell := &finalizationCell{owner: reg.cells, held: held}
	if o := token.AsObject(); o != nil {
		cell.tokenObject = weak.Make(o)
	} else if token.Kind() == KindSymbol {
		cell.tokenSymbol = weak.Make(token.SymbolRef())
	}
	q := vm.weakQueue
	report := func(c *finalizationCell) { q.push(func() { q.cells = append(q.cells, c) }) }
	if o := target.AsObject(); o != nil {
		cell.stop = runtime.AddCleanup(o, report, cell)
	} else {
		cell.stop = runtime.AddCleanup(target.SymbolRef(), report, cell)
	}
	reg.cells.cells[cell] = struct{}{}
}

func (reg *finalizationRegistry) unregister(vm *VM, token Value) bool {
	vm.sweepWeak()
	removed := false
	match := func(c *finalizationCell) bool {
		if o := token.AsObject(); o != nil {
			return c.tokenObject == weak.Make(o)
		}
		return c.tokenSymbol == weak.Make(token.SymbolRef())
	}
	for c := range reg.cells.cells {
		if match(c) {
			c.stop.Stop()
			delete(reg.cells.cells, c)
			removed = true
		}
	}
	// Cells whose targets died but whose callbacks have not run yet are
	// unregistered too.
	pending := reg.pending[:0]
	for _, c := range reg.pending {
		if match(c) {
			removed = true
			continue
		}
		pending = append(pending, c)
	}
	clear(reg.pending[len(pending):])
	reg.pending = pending
	return removed
}

// drop forgets the cells of a collected registry.
func (cs *finalizationCells) drop() {
	for c := range cs.cells {
		c.stop.Stop()
	}
	cs.cells = nil
}

// targetCollected queues the cleanup of a cell whose target was collected
// (HostEnqueueFinalizationRegistryCleanupJob).
func (c *finalizationCell) targetCollected(vm *VM) {
	cs := c.owner
	if _, ok := cs.cells[c]; !ok {
		return // unregistered, or the registry is gone
	}
	delete(cs.cells, c)
	reg := cs.registry.Value()
	if reg == nil {
		return
	}
	reg.pending = append(reg.pending, c)
	if !reg.scheduled {
		reg.scheduled = true
		vm.EnqueueJob(func() {
			reg.scheduled = false
			reg.cleanupPending(vm, reg.cleanup)
		})
	}
}

// cleanupPending is CleanupFinalizationRegistry.
func (reg *finalizationRegistry) cleanupPending(vm *VM, callback Value) {
	for len(reg.pending) > 0 {
		c := reg.pending[0]
		reg.pending[0] = nil
		reg.pending = reg.pending[1:]
		vm.Call(callback, Undefined, c.held)
	}
	reg.pending = nil
}
