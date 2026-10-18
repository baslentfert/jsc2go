package vm

import "unsafe"

// The pools of objects generated code makes objects from (jit_alloc.go),
// and of property storage it grows objects into (jit.go's cachedPut).
// Their types are here, without the JIT's build constraint, because VM
// has fields of them on every platform.

// jitPoolSize is the number of objects of a class kept ready.
const jitPoolSize = 32

// jitObjectPool holds ready objects of one inline capacity.
type jitObjectPool struct {
	objs [jitPoolSize]*Object
	n    int64
}

// jitPoolCapacities are the pools' inline capacities (newObjectWithCapacity's
// classes).
var jitPoolCapacities = [...]uint32{2, 4, 8}

// jitSlotPool holds ready property storage of one capacity: the first
// elements of zero arrays of Values.
type jitSlotPool struct {
	arrs [jitPoolSize]unsafe.Pointer
	n    int64
}

// jitSlotCapacities are the capacities of the storage pools: an object
// whose storage of capacity c is full grows into one of 2c, as append
// grows it.
var jitSlotCapacities = [...]int64{4, 8, 16, 32}

// jitStringPool holds empty ASCII Strings for generated code's string
// appends (appendString), like the object pools. 256 of them: with 32,
// base64 left its code every 32 appends to refill it, through the
// nested exits of its callers (14% of its time); 256 empty Strings are
// 16 KB per VM.
type jitStringPool struct {
	strs [256]*String
	n    int
}

// fillStringPool fills the VM's string pool once it is empty.
func (vm *VM) fillStringPool() {
	if vm.jitStrings == nil {
		vm.jitStrings = new(jitStringPool)
	}
	p := vm.jitStrings
	for p.n < len(p.strs) {
		p.strs[p.n] = &String{isASCII: true}
		p.n++
	}
}

// jitArrayPool holds empty arrays with inline room for baseVectorLength
// elements for generated code's new_array (newSmallArray).
type jitArrayPool struct {
	arrs [32]*Object
	n    int
}

// fillArrayPool fills the VM's array pool once it is empty. The arrays
// get their prototype and realm when they are taken.
func (vm *VM) fillArrayPool() {
	if vm.jitArrays == nil {
		vm.jitArrays = new(jitArrayPool)
	}
	p := vm.jitArrays
	for p.n < len(p.arrs) {
		p.arrs[p.n] = vm.realm.newArrayWithProto(nil, nil)
		p.n++
	}
}

// jitClosurePool holds closures for generated code's new_func and
// new_func_exp (newClosure in jit_alloc.go): made by newClosureBlock, with
// insideOrdinary set (generated code makes closures of ordinary
// functions only).
type jitClosurePool struct {
	blocks [32]*closureBlock
	n      int
}

// fillClosurePool fills the VM's closure pool once it is empty.
func (vm *VM) fillClosurePool() {
	if vm.jitClosures == nil {
		vm.jitClosures = new(jitClosurePool)
	}
	p := vm.jitClosures
	for p.n < len(p.blocks) {
		b := vm.realm.newClosureBlock()
		b.j.insideOrdinary = true
		p.blocks[p.n] = b
		p.n++
	}
}

// jitScopePool holds lexical scopes with inline slots for generated
// code's create_lexical_environment (newScope in jit_alloc.go).
type jitScopePool struct {
	blocks [32]*scopeBlock
	n      int
}

// fillScopePool fills the VM's scope pool once it is empty.
func (vm *VM) fillScopePool() {
	if vm.jitScopes == nil {
		vm.jitScopes = new(jitScopePool)
	}
	p := vm.jitScopes
	for p.n < len(p.blocks) {
		b := new(scopeBlock)
		b.s.kind, b.s.slots = scopeLexical, b.v[:0:0]
		p.blocks[p.n] = b
		p.n++
	}
}

// jitRegExpPool holds RegExp objects for generated code's new_reg_exp
// (newRegExp in jit_alloc.go).
type jitRegExpPool struct {
	blocks [32]*regExpBlock
	n      int
}

// fillRegExpPool fills the VM's RegExp pool once it is empty.
func (vm *VM) fillRegExpPool() {
	if vm.jitRegExps == nil {
		vm.jitRegExps = new(jitRegExpPool)
	}
	p := vm.jitRegExps
	for p.n < len(p.blocks) {
		p.blocks[p.n] = vm.realm.newRegExpBlock()
		p.n++
	}
}
