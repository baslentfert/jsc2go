//go:build ((arm64 || amd64) && (darwin || linux)) || (amd64 && windows)

package vm

import (
	"unsafe"

	"github.com/baslentfert/jsc2go/bytecode"
	"github.com/baslentfert/jsc2go/jit"
)

// Objects made by generated code (create_this and new_object).
//
// Generated code cannot call Go's allocator, so Go keeps a few objects with
// inline storage ready per VM (jitObjectPool, one per capacity class),
// made by newObjectWithCapacity without a prototype: their storage, shape
// and attributes are those of a new object already. Generated code takes
// one, clears its slot in the pool (the pool must not keep objects alive),
// and sets the prototype and the realm. When a pool is empty the
// instruction takes its slow path, where Go makes the object as before and
// fills the pool again.

var (
	offPoolObjs = int(unsafe.Offsetof(jitObjectPool{}.objs))
	offPoolN    = int(unsafe.Offsetof(jitObjectPool{}.n))
	offRealm    = int(unsafe.Offsetof(Object{}.realm))
	sizePool    = int(unsafe.Sizeof(jitObjectPool{}))
)

// poolClass returns the pool for objects with inline capacity n.
func poolClass(n uint32) (int, bool) {
	for i, c := range jitPoolCapacities {
		if n >= 1 && n <= c {
			return i, true
		}
	}
	return 0, false
}

// refillObjectPool fills the pool for inline capacity n again once it is
// empty (create_this's and new_object's slow paths).
func (vm *VM) refillObjectPool(n uint32) {
	if i, ok := poolClass(n); ok && vm.jitPools != nil && vm.jitPools[i].n == 0 {
		vm.fillObjectPool(i)
	}
}

// fillObjectPool fills the pool of class i.
func (vm *VM) fillObjectPool(i int) {
	if vm.jitPools == nil {
		vm.jitPools = new([len(jitPoolCapacities)]jitObjectPool)
	}
	p := &vm.jitPools[i]
	for p.n < jitPoolSize {
		o := vm.realm.newObjectWithCapacity(nil, jitPoolCapacities[i])
		p.objs[p.n] = o
		p.n++
	}
}

// fillSlotPools fills the storage pools that are empty (put_by_id's slow
// path, and when code that grows objects is compiled).
func (vm *VM) fillSlotPools() {
	if vm.jitSlots == nil {
		vm.jitSlots = new([len(jitSlotCapacities)]jitSlotPool)
	}
	for i := range vm.jitSlots {
		p := &vm.jitSlots[i]
		if p.n != 0 {
			continue
		}
		for p.n < jitPoolSize {
			s := make([]Value, jitSlotCapacities[i])
			p.arrs[p.n] = unsafe.Pointer(&s[0])
			p.n++
		}
	}
}

var (
	offSlotArrs  = int(unsafe.Offsetof(jitSlotPool{}.arrs))
	offSlotN     = int(unsafe.Offsetof(jitSlotPool{}.n))
	sizeSlotPool = int(unsafe.Sizeof(jitSlotPool{}))
)

// growStorage is the end of putCase when object S0's property storage is
// full (S6 = its length, S7 = its capacity): the storage grows into twice
// the capacity (at least 4) from the pools, then the value S1, S2 is added with the new
// shape S5 (JSC's transitions that reallocate the out-of-line storage). It
// branches to done, or to exit when there is no storage ready.
func (c *jitCompiler) growStorage(done, exit *jit.Label) {
	m := &c.m
	vm := c.cb.realm.vm
	vm.fillSlotPools()
	// S3 = the pool, S7 = the new capacity: 4 below 4, else twice it.
	have := jit.NewLabel()
	for i, capacity := range jitSlotCapacities {
		next := jit.NewLabel()
		if i == 0 {
			m.Branch64Imm(jit.UGE, jit.S7, uint32(capacity), next)
		} else {
			m.Branch64Imm(jit.NE, jit.S7, uint32(capacity/2), next)
		}
		m.MovImm(jit.S3, uint64(uintptr(unsafe.Pointer(&vm.jitSlots[i]))))
		m.MovImm(jit.S7, uint64(capacity))
		m.B(have)
		m.Bind(next)
	}
	m.B(exit)
	m.Bind(have)
	// Take the storage (S9) and clear its slot in the pool.
	m.Load64(jit.S8, jit.S3, offSlotN)
	m.BranchZero(jit.S8, exit)
	m.SubImm64(jit.S8, jit.S8, 1)
	m.Store64(jit.S8, jit.S3, offSlotN)
	m.AddShift64(jit.S4, jit.S3, jit.S8, 3)
	m.Load64(jit.S9, jit.S4, offSlotArrs)
	m.StoreZero64(jit.S4, offSlotArrs)
	// Copy the S6 values (S4 = from, S3 = to, S8 = left).
	loop, copied := jit.NewLabel(), jit.NewLabel()
	m.Load64(jit.S4, jit.S0, offProps+8)
	m.Mov(jit.S3, jit.S9)
	m.Mov(jit.S8, jit.S6)
	m.BranchZero(jit.S8, copied)
	m.Bind(loop)
	m.Load64(rTmp, jit.S4, 0)
	m.Store64(rTmp, jit.S3, 0)
	m.Load64(rTmp, jit.S4, 8)
	m.Store64(rTmp, jit.S3, 8)
	m.AddImm64(jit.S4, jit.S4, uint32(valueSize))
	m.AddImm64(jit.S3, jit.S3, uint32(valueSize))
	m.SubImm64(jit.S8, jit.S8, 1)
	m.BranchNonZero(jit.S8, loop)
	m.Bind(copied)
	m.Store64(jit.S9, jit.S0, offProps+8)
	m.Store64(jit.S7, jit.S0, offProps+24)
	// Add the property.
	m.AddShift64(jit.S8, jit.S9, jit.S6, 4)
	m.Store64(jit.S1, jit.S8, 0)
	m.Store64(jit.S2, jit.S8, 8)
	m.AddImm64(jit.S6, jit.S6, 1)
	m.Store64(jit.S6, jit.S0, offProps+16)
	m.Store64(jit.S5, jit.S0, offProps)
	m.B(done)
}

var (
	offAPoolN = int(unsafe.Offsetof(jitArrayPool{}.n))
	offAPoolA = int(unsafe.Offsetof(jitArrayPool{}.arrs))
)

// newSmallArray compiles new_array of n <= baseVectorLength values in
// registers argv, argv-1, ...: an empty array from the VM's pool (its
// elements are its inline storage) gets the realm's Array.prototype, the
// values and the length. An empty pool takes slow, where Go makes the
// array and fills the pool again. (Array literals left generated code 8
// million times over the benchmarks: espree, acorn, typescript.)
func (c *jitCompiler) newSmallArray(dst, argv bytecode.VirtualRegister, n int, slow *jit.Label) {
	m := &c.m
	c.takeArray(slow)
	if n > 0 {
		m.Load64(jit.S5, jit.S9, offElements) // the inline storage
		for i := 0; i < n; i++ {
			c.loadValue(jit.S0, jit.S1, argv-bytecode.VirtualRegister(i))
			m.Store64(jit.S0, jit.S5, i*valueSize)
			m.Store64(jit.S1, jit.S5, i*valueSize+8)
		}
		m.MovImm(jit.S0, uint64(n))
		m.Store64(jit.S0, jit.S9, offElements+8) // len
		m.Load64(jit.S5, jit.S9, offExotic+8)    // *arrayObject
		m.Store32(jit.S0, jit.S5, offArrayLength)
	}
	m.MovImm(jit.S1, uint64(tagObject))
	c.storeValue(dst, jit.S9, jit.S1)
}

// newArrayWithSize compiles new Array(n) (new_array_with_size) for n of
// at most baseVectorLength: an array from the pool with n holes in its
// inline storage, as arrayWithHoles makes it; other lengths take the slow
// path. (ML and crypto-aes made 2.1 million a run through Go, nearly all
// of length 4 or less.)
func (c *jitCompiler) newArrayWithSize(dst, length bytecode.VirtualRegister, slow *jit.Label) {
	m := &c.m
	if !c.exactInt32(jit.S3, length, slow) {
		m.B(slow)
		return
	}
	m.Branch64Imm(jit.UGT, jit.S3, baseVectorLength, slow) // (a negative one too)
	c.takeArray(slow)
	m.Load64(jit.S5, jit.S9, offElements) // the inline storage
	m.MovImm(jit.S0, tagEmpty)
	filled := jit.NewLabel()
	for i := 0; i < baseVectorLength; i++ {
		m.Branch64Imm(jit.ULE, jit.S3, uint32(i), filled)
		m.StoreZero64(jit.S5, i*valueSize)
		m.Store64(jit.S0, jit.S5, i*valueSize+8)
	}
	m.Bind(filled)
	m.Store64(jit.S3, jit.S9, offElements+8) // len
	m.Load64(jit.S5, jit.S9, offExotic+8)    // *arrayObject
	m.Store32(jit.S3, jit.S5, offArrayLength)
	m.MovImm(jit.S1, uint64(tagObject))
	c.storeValue(dst, jit.S9, jit.S1)
}

// takeArray takes an empty array from the VM's pool into S9, with the
// realm's Array.prototype, or branches to slow when the pool is empty. It
// uses S4 and S6-S8.
func (c *jitCompiler) takeArray(slow *jit.Label) {
	m := &c.m
	r := c.cb.realm
	r.vm.fillArrayPool()
	m.MovImm(jit.S6, uint64(uintptr(unsafe.Pointer(r.vm.jitArrays))))
	m.Load64(jit.S7, jit.S6, offAPoolN)
	m.BranchZero(jit.S7, slow)
	m.SubImm64(jit.S7, jit.S7, 1)
	m.Store64(jit.S7, jit.S6, offAPoolN)
	m.AddShift64(jit.S8, jit.S6, jit.S7, 3)
	m.Load64(jit.S9, jit.S8, offAPoolA)
	m.StoreZero64(jit.S8, offAPoolA)
	m.MovImm(jit.S4, uint64(uintptr(unsafe.Pointer(r.ArrayPrototype))))
	m.Store64(jit.S4, jit.S9, offProto)
	m.MovImm(rTmp, uint64(uintptr(unsafe.Pointer(r))))
	m.Store64(rTmp, jit.S9, offRealm)
}

var (
	offCPoolN   = int(unsafe.Offsetof(jitClosurePool{}.n))
	offCPoolB   = int(unsafe.Offsetof(jitClosurePool{}.blocks))
	offFnExec   = int(unsafe.Offsetof(jsFunction{}.exec))
	offFnScope  = int(unsafe.Offsetof(jsFunction{}.scope))
	offBlockJ   = int(unsafe.Offsetof(closureBlock{}.j))
	offBlockV   = int(unsafe.Offsetof(closureBlock{}.v))
	sizeBlockVs = len(closureBlock{}.v)
)

// newClosure compiles new_func and new_func_exp of an ordinary function
// (not an arrow, generator, async or class constructor, whose closures
// differ by frame or need objects of their own): a closure from the VM's
// pool gets what Go's newJSFunction gives it, taken from a closure made
// now as a template (its prototype, shape and length, name and lazy
// prototype values), and the scope from register scope. (Closures left
// generated code 5.7 million times over the benchmarks: uglify, jshint,
// lebab.)
func (c *jitCompiler) newClosure(exec *bytecode.UnlinkedFunctionExecutable, dst, scope bytecode.VirtualRegister, slow *jit.Label) {
	m := &c.m
	r := c.cb.realm
	mode := exec.ParseMode
	if exec.IsArrowFunction() || mode.IsGeneratorWrapper() || mode.IsAsyncGeneratorWrapper() ||
		mode.IsAsyncFunctionWrapper() || exec.IsClassConstructorFunction() {
		m.B(slow)
		return
	}
	if _, ok := c.constant(scope); ok {
		m.B(slow) // the scope is read from its register
		return
	}
	tmpl := r.newJSFunction(exec, nil)
	vals := tmpl.props.vals
	if len(vals) > sizeBlockVs {
		m.B(slow)
		return
	}
	for _, v := range vals {
		if v.IsObject() {
			m.B(slow) // a prototype object of its own
			return
		}
	}
	c.keepCells = append(c.keepCells, tmpl.props.shape)
	r.vm.fillClosurePool()
	m.MovImm(jit.S6, uint64(uintptr(unsafe.Pointer(r.vm.jitClosures))))
	m.Load64(jit.S7, jit.S6, offCPoolN)
	m.BranchZero(jit.S7, slow)
	m.SubImm64(jit.S7, jit.S7, 1)
	m.Store64(jit.S7, jit.S6, offCPoolN)
	m.AddShift64(jit.S8, jit.S6, jit.S7, 3)
	m.Load64(jit.S9, jit.S8, offCPoolB) // the block, its object first
	m.StoreZero64(jit.S8, offCPoolB)
	m.MovImm(jit.S4, uint64(uintptr(unsafe.Pointer(tmpl.proto))))
	m.Store64(jit.S4, jit.S9, offProto)
	m.MovImm(jit.S4, uint64(uintptr(unsafe.Pointer(r))))
	m.Store64(jit.S4, jit.S9, offRealm)
	m.MovImm(jit.S4, uint64(uintptr(unsafe.Pointer(tmpl.props.shape))))
	m.Store64(jit.S4, jit.S9, offProps)
	for i, v := range vals {
		m.MovImm(jit.S4, uint64(uintptr(unsafe.Pointer(v.p))))
		m.Store64(jit.S4, jit.S9, offBlockV+i*valueSize)
		m.MovImm(jit.S4, v.u)
		m.Store64(jit.S4, jit.S9, offBlockV+i*valueSize+8)
	}
	m.MovImm(jit.S4, uint64(len(vals)))
	m.Store64(jit.S4, jit.S9, offProps+16) // len(vals)
	m.MovImm(jit.S4, uint64(uintptr(unsafe.Pointer(exec))))
	m.Store64(jit.S4, jit.S9, offBlockJ+offFnExec)
	m.Load64(jit.S4, rRegs, c.off(scope)) // the Scope
	m.Store64(jit.S4, jit.S9, offBlockJ+offFnScope)
	m.MovImm(jit.S1, uint64(tagObject))
	c.storeValue(dst, jit.S9, jit.S1)
}

var (
	offScPoolN  = int(unsafe.Offsetof(jitScopePool{}.n))
	offScPoolB  = int(unsafe.Offsetof(jitScopePool{}.blocks))
	offScSlots  = int(unsafe.Offsetof(Scope{}.slots))
	offScTableJ = int(unsafe.Offsetof(Scope{}.table))
	offScBlockV = int(unsafe.Offsetof(scopeBlock{}.v))
)

// newScope compiles create_lexical_environment of a symbol table (a
// constant) with up to scopeInline slots: a scope from the VM's pool gets
// the parent from register scope, the table, and the initial value in its
// slots. (5.2 million times over the benchmarks: uglify, coffeescript,
// lebab.)
func (c *jitCompiler) newScope(dst, scope, table, initial bytecode.VirtualRegister, slow *jit.Label) {
	m := &c.m
	tv, ok := c.constant(table)
	if !ok {
		m.B(slow)
		return
	}
	t, ok := tv.cell().(*bytecode.SymbolTable)
	if !ok {
		m.B(slow)
		return
	}
	n := int(t.ScopeSize())
	if _, isConst := c.constant(scope); isConst || n < 1 || n > scopeInline {
		m.B(slow)
		return
	}
	vm := c.cb.realm.vm
	vm.fillScopePool()
	m.MovImm(jit.S6, uint64(uintptr(unsafe.Pointer(vm.jitScopes))))
	m.Load64(jit.S7, jit.S6, offScPoolN)
	m.BranchZero(jit.S7, slow)
	m.SubImm64(jit.S7, jit.S7, 1)
	m.Store64(jit.S7, jit.S6, offScPoolN)
	m.AddShift64(jit.S8, jit.S6, jit.S7, 3)
	m.Load64(jit.S9, jit.S8, offScPoolB) // the block, its scope first
	m.StoreZero64(jit.S8, offScPoolB)
	m.Load64(jit.S4, rRegs, c.off(scope))
	m.Store64(jit.S4, jit.S9, offScopeParent)
	m.MovImm(jit.S4, uint64(uintptr(unsafe.Pointer(t))))
	m.Store64(jit.S4, jit.S9, offScTableJ)
	m.MovImm(jit.S4, uint64(n))
	m.Store64(jit.S4, jit.S9, offScSlots+8)  // len
	m.Store64(jit.S4, jit.S9, offScSlots+16) // cap
	c.loadValue(jit.S0, jit.S1, initial)
	m.AddImm64(jit.S5, jit.S9, uint32(offScBlockV)) // the inline slots
	m.Store64(jit.S5, jit.S9, offScSlots)
	for i := 0; i < n; i++ {
		m.Store64(jit.S0, jit.S5, i*valueSize)
		m.Store64(jit.S1, jit.S5, i*valueSize+8)
	}
	m.MovImm(jit.S1, uint64(tagScope))
	c.storeValue(dst, jit.S9, jit.S1)
}

var (
	offRPoolN  = int(unsafe.Offsetof(jitRegExpPool{}.n))
	offRPoolB  = int(unsafe.Offsetof(jitRegExpPool{}.blocks))
	offRBlockD = int(unsafe.Offsetof(regExpBlock{}.d))
	offRBlockV = int(unsafe.Offsetof(regExpBlock{}.v))
	sizeRData  = int(unsafe.Sizeof(regExpData{}))
)

// newRegExp compiles new_reg_exp of a constant literal: a RegExp object
// from the VM's pool gets what newRegExpFromLiteral gives it, copied from
// one made now as a template (its prototype, shape, data word by word, and
// lastIndex 0). (2 million times a run in OfflineAssembler; the slow
// path also made the source string again each time.)
func (c *jitCompiler) newRegExp(dst, literal bytecode.VirtualRegister, slow *jit.Label) {
	m := &c.m
	v, ok := c.constant(literal)
	if !ok || sizeRData%8 != 0 {
		m.B(slow)
		return
	}
	re, ok := v.cell().(*bytecode.RegExp)
	if !ok {
		m.B(slow)
		return
	}
	vm := c.cb.realm.vm
	saved := vm.realm
	vm.realm = c.cb.realm
	tmpl := vm.newRegExpFromLiteral(re)
	vm.realm = saved
	if len(tmpl.props.vals) != 1 {
		m.B(slow)
		return
	}
	c.keepCells = append(c.keepCells, tmpl)
	data := tmpl.internal.(*regExpData)
	vm.fillRegExpPool()
	m.MovImm(jit.S6, uint64(uintptr(unsafe.Pointer(vm.jitRegExps))))
	m.Load64(jit.S7, jit.S6, offRPoolN)
	m.BranchZero(jit.S7, slow)
	m.SubImm64(jit.S7, jit.S7, 1)
	m.Store64(jit.S7, jit.S6, offRPoolN)
	m.AddShift64(jit.S8, jit.S6, jit.S7, 3)
	m.Load64(jit.S9, jit.S8, offRPoolB) // the block, its object first
	m.StoreZero64(jit.S8, offRPoolB)
	m.MovImm(jit.S4, uint64(uintptr(unsafe.Pointer(tmpl.proto))))
	m.Store64(jit.S4, jit.S9, offProto)
	m.MovImm(jit.S4, uint64(uintptr(unsafe.Pointer(c.cb.realm))))
	m.Store64(jit.S4, jit.S9, offRealm)
	m.MovImm(jit.S4, uint64(uintptr(unsafe.Pointer(tmpl.props.shape))))
	m.Store64(jit.S4, jit.S9, offProps)
	lastIndex := tmpl.props.vals[0]
	m.MovImm(jit.S4, uint64(uintptr(lastIndex.p)))
	m.Store64(jit.S4, jit.S9, offRBlockV)
	m.MovImm(jit.S4, lastIndex.u)
	m.Store64(jit.S4, jit.S9, offRBlockV+8)
	m.MovImm(jit.S4, 1)
	m.Store64(jit.S4, jit.S9, offProps+16) // len(vals)
	m.MovImm(jit.S5, uint64(uintptr(unsafe.Pointer(data))))
	for off := 0; off < sizeRData; off += 8 {
		m.Load64(jit.S4, jit.S5, off)
		m.Store64(jit.S4, jit.S9, offRBlockD+off)
	}
	m.MovImm(jit.S1, uint64(tagObject))
	c.storeValue(dst, jit.S9, jit.S1)
}

// allocObject emits taking an object of class cls from the pool into S9,
// with prototype S4 and the code block's realm, or branching to slow.
func (c *jitCompiler) allocObject(cls int, slow *jit.Label) {
	m := &c.m
	vm := c.cb.realm.vm
	vm.fillObjectPool(cls)
	pool := &vm.jitPools[cls]
	m.MovImm(jit.S6, uint64(uintptr(unsafe.Pointer(pool))))
	m.Load64(jit.S7, jit.S6, offPoolN)
	m.BranchZero(jit.S7, slow)
	m.SubImm64(jit.S7, jit.S7, 1)
	m.Store64(jit.S7, jit.S6, offPoolN)
	m.AddShift64(jit.S8, jit.S6, jit.S7, 3)
	m.Load64(jit.S9, jit.S8, offPoolObjs)
	m.StoreZero64(jit.S8, offPoolObjs)
	m.Store64(jit.S4, jit.S9, offProto)
	m.MovImm(rTmp, uint64(uintptr(unsafe.Pointer(c.cb.realm))))
	m.Store64(rTmp, jit.S9, offRealm)
}

// allocInstruction compiles create_this and new_object; it reports false
// for other instructions.
func (c *jitCompiler) allocInstruction(pc int, op bytecode.OpcodeID, in []uint32) bool {
	m := &c.m
	next := pc + op.Length()
	switch op {
	case bytecode.OpCreateThisID:
		// OrdinaryCreateFromConstructor: newTarget.prototype through the
		// create_this cache (dst and callee are the same register, so the
		// prototype stays in S4, S5 until the object exists).
		b := bytecode.DecodeCreateThis(in)
		slow := c.slow(pc, next)
		cls, ok := poolClass(thisCapacity(b.InlineCapacity))
		if !ok || !c.loadObject(b.Callee, slow) {
			m.B(slow)
			return true
		}
		c.ordinaryFor(prototypeKey, slow)
		c.cachedGetInto(&c.cb.createThisCaches[b.MetadataID], b.Dst, false, slow)
		m.Branch64Imm(jit.NE, jit.S5, uint32(tagObject), slow)
		c.allocObject(cls, slow)
		m.MovImm(rTmp, uint64(tagObject))
		c.storeValue(b.Dst, jit.S9, rTmp)
		return true
	case bytecode.OpNewObjectID:
		b := bytecode.DecodeNewObject(in)
		slow := c.slow(pc, next)
		cls, ok := poolClass(b.InlineCapacity)
		if !ok {
			m.B(slow)
			return true
		}
		m.MovImm(jit.S4, uint64(uintptr(unsafe.Pointer(c.cb.realm.ObjectPrototype))))
		c.allocObject(cls, slow)
		m.MovImm(rTmp, uint64(tagObject))
		c.storeValue(b.Dst, jit.S9, rTmp)
		return true
	}
	return false
}

// iteratorNext compiles iterator_next of a for-of over an array in the
// fast mode (iterator_open left fastArraySentinel in the iterator register
// and the index in next; fastArrayNext): the next element, or done at the
// end; holes, elements past the storage and other iterators take the slow
// path. (UniPoker, coffeescript and prepack ran 1.4 million a run in Go.)
func (c *jitCompiler) iteratorNext(b bytecode.OpIteratorNext, slow *jit.Label) {
	m := &c.m
	_, constIterator := c.constant(b.Iterator)
	_, constIterable := c.constant(b.Iterable)
	if constIterator || constIterable {
		m.B(slow)
		return
	}
	m.Load64(jit.S0, rRegs, c.off(b.Iterator)+8)
	m.Branch64Imm(jit.NE, jit.S0, uint32(tagObject), slow)
	m.Load64(jit.S0, rRegs, c.off(b.Iterator))
	m.MovImm(jit.S1, uint64(uintptr(unsafe.Pointer(c.cb.realm.fastArraySentinel))))
	m.Branch64(jit.NE, jit.S0, jit.S1, slow)
	// The iterable: an array (S2), its length (S3).
	m.Load64(jit.S0, rRegs, c.off(b.Iterable)+8)
	m.Branch64Imm(jit.NE, jit.S0, uint32(tagObject), slow)
	m.Load64(jit.S2, rRegs, c.off(b.Iterable))
	m.Load64(jit.S3, jit.S2, offExotic)
	m.MovImm(jit.S4, uint64(arrayObjectItab))
	m.Branch64(jit.NE, jit.S3, jit.S4, slow)
	m.Load64(jit.S3, jit.S2, offExotic+8)
	m.Load32(jit.S3, jit.S3, offArrayLength)
	// The index (S1).
	if !c.exactInt32(jit.S1, b.Next, slow) {
		m.B(slow)
		return
	}
	done, end := jit.NewLabel(), jit.NewLabel()
	m.Branch64(jit.UGE, jit.S1, jit.S3, done)
	m.Load64(jit.S4, jit.S2, offElements+8)
	m.Branch64(jit.UGE, jit.S1, jit.S4, slow)
	m.Load64(jit.S4, jit.S2, offElements)
	m.AddShift64(jit.S4, jit.S4, jit.S1, 4)
	m.Load64(jit.S6, jit.S4, 8)
	m.Branch64Imm(jit.EQ, jit.S6, uint32(tagEmpty), slow) // a hole
	m.Load64(jit.S5, jit.S4, 0)
	c.storeValue(b.Value, jit.S5, jit.S6)
	m.AddImm64(jit.S1, jit.S1, 1)
	m.CvtS64(2, jit.S1) // (2^31 at most)
	c.storeNum(b.Next, 2)
	m.MovImm(jit.S0, uint64(tagFalse))
	c.storeWord(b.Done, jit.S0)
	m.B(end)
	m.Bind(done)
	m.MovImm(jit.S0, uint64(tagTrue))
	c.storeWord(b.Done, jit.S0)
	m.Bind(end)
}
